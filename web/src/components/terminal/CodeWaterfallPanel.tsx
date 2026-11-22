import { useMemo, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Clock,
  Download,
  Image as ImageIcon,
  Loader2,
  Maximize2,
  Play,
  Plus,
  RotateCw,
  Square,
} from 'lucide-react';
import type {
  ExecutionStep,
  KernelExecResult,
  KernelRichOutput,
  NotebookCell,
} from '../../types';
import { api, formatBytes } from '../../lib/api';
import { useI18n } from '../../i18n';
import {
  ActionButton,
  DialogModal,
  EmptyState,
  IconButton,
  InsetWell,
  StatusPill,
  cn,
} from '../ui';
import { buildScientificPlotDataUri, formatDurationMs } from './workbenchMockData';

export interface ExecutedCellEntry {
  id: string;
  code: string;
  submittedAt: string;
  running?: boolean;
  result?: KernelExecResult;
}

export interface CodeWaterfallPanelProps {
  sessionId?: string;
  sessionName?: string;
  timeline?: ExecutionStep[];
  cells?: NotebookCell[];
  execEntries: ExecutedCellEntry[];
  extraArtifacts?: KernelRichOutput[];
  onAppendExecEntry: (entry: ExecutedCellEntry) => void;
  onUpdateExecEntry: (id: string, patch: Partial<ExecutedCellEntry>) => void;
  onPushStreamText?: (stream: 'stdout' | 'stderr' | 'colab', text: string, rich?: KernelRichOutput) => void;
  galleryOnly?: boolean;
  isOfflineDemo?: boolean;
}

/**
 * Output wells are dedicated code surfaces: warm charcoal in both themes
 * (`--code-bg`), so the tints below are fixed light-on-dark Stardust values.
 */
const OUT_ERR = '#fb856e';
const OUT_HAIRLINE = 'rgba(255, 255, 255, 0.07)';

function stripAnsiCodes(raw: string): string {
  // eslint-disable-next-line no-control-regex
  return raw.replace(/\x1b\[[0-9;]*m/g, '');
}

function resolveRichOutputImageSrc(out: KernelRichOutput, idx: number): string | null {
  if (out.inlineBase64) {
    if (out.inlineBase64.startsWith('data:')) return out.inlineBase64;
    const mime = out.mimeTypes?.find((m) => m.startsWith('image/')) || 'image/png';
    return `data:${mime};base64,${out.inlineBase64}`;
  }
  if (out.artifactUrl) {
    return out.artifactUrl;
  }
  if (out.mimeTypes?.some((m) => m.startsWith('image/'))) {
    const variants: Array<'loss' | 'throughput' | 'vram'> = ['loss', 'throughput', 'vram'];
    return buildScientificPlotDataUri(
      variants[idx % variants.length],
      out.title || out.filePath?.split('/').pop() || `matplotlib_figure_${idx + 1}.png`,
    );
  }
  return null;
}

/**
 * De-duplicated rich outputs across experiment artifacts, notebook cells and
 * interactive runs. Shared with the Workbench so its tab count always matches
 * the gallery.
 */
export function collectRichOutputs(
  extraArtifacts: KernelRichOutput[] = [],
  cells: NotebookCell[] = [],
  execEntries: ExecutedCellEntry[] = [],
): KernelRichOutput[] {
  const collected: KernelRichOutput[] = [];
  const seenKeys = new Set<string>();

  const addOut = (o: KernelRichOutput) => {
    const key = `${o.filePath || ''}|${o.title || ''}|${o.artifactUrl || ''}|${o.sizeBytes}`;
    if (seenKeys.has(key)) return;
    seenKeys.add(key);
    collected.push(o);
  };

  extraArtifacts.forEach(addOut);
  cells.forEach((c) => c.outputs?.forEach(addOut));
  execEntries.forEach((e) => e.result?.richOutputs?.forEach(addOut));

  return collected;
}

/** One-click starter snippets, labelled by what they do rather than by command. */
const CODE_PRESETS = [
  {
    key: 'gpu-status',
    zhLabel: '查看显卡状态 (nvidia-smi)',
    enLabel: 'GPU Status (nvidia-smi)',
    code: '!nvidia-smi --query-gpu=index,name,memory.used,memory.total,utilization.gpu,temperature.gpu --format=csv',
  },
  {
    key: 'torch-check',
    zhLabel: '测试 PyTorch 与显存',
    enLabel: 'Test PyTorch & VRAM',
    code: [
      'import torch',
      'dev = torch.cuda.get_device_properties(0) if torch.cuda.is_available() else None',
      'print("CUDA Available:", torch.cuda.is_available())',
      'if dev:',
      '    print(f"GPU: {dev.name} | VRAM: {dev.total_memory / 1e9:.1f} GB | SM {dev.major}.{dev.minor}")',
    ].join('\n'),
  },
  {
    key: 'processes',
    zhLabel: '查看运行中的进程',
    enLabel: 'Running Processes',
    code: '!ps -eo pid,ppid,stat,pcpu,pmem,etime,comm | head -n 12',
  },
  {
    key: 'sample-plot',
    zhLabel: '生成示例训练曲线图',
    enLabel: 'Sample Training Chart',
    code: [
      'import matplotlib.pyplot as plt, numpy as np',
      'steps = np.linspace(0, 500, 100)',
      'loss = 2.1 * np.exp(-steps / 140) + 0.26',
      'plt.figure(figsize=(7, 3.6), dpi=140)',
      'plt.plot(steps, loss, label="Qwen3-27B Eval Loss")',
      'plt.legend(); plt.tight_layout(); plt.show()',
    ].join('\n'),
  },
  {
    key: 'model-cache',
    zhLabel: '查看已下载的模型权重',
    enLabel: 'Downloaded Model Weights',
    code: '!du -sh /root/.cache/huggingface/* /content/* 2>/dev/null || ls -la /content',
  },
];

export function CodeWaterfallPanel({
  sessionId,
  sessionName,
  timeline = [],
  cells = [],
  execEntries,
  extraArtifacts = [],
  onAppendExecEntry,
  onUpdateExecEntry,
  onPushStreamText,
  galleryOnly = false,
  isOfflineDemo = false,
}: CodeWaterfallPanelProps) {
  const { locale } = useI18n();
  const isZh = locale === 'zh-CN';

  const [codeDraft, setCodeDraft] = useState<string>(CODE_PRESETS[0].code);
  const [isRunning, setIsRunning] = useState(false);
  const [interrupting, setInterrupting] = useState(false);
  const [lightboxItem, setLightboxItem] = useState<{
    src: string;
    output: KernelRichOutput;
  } | null>(null);

  // Aggregate all rich outputs across experiment artifacts, notebook cells, and interactive runs
  const allRichOutputs = useMemo(
    () => collectRichOutputs(extraArtifacts, cells, execEntries),
    [extraArtifacts, cells, execEntries],
  );

  const nextExecutionNumber = useMemo(() => {
    const maxExec = execEntries.reduce(
      (max, item) => Math.max(max, item.result?.executionCount ?? 0),
      cells.length,
    );
    return maxExec + 1;
  }, [execEntries, cells.length]);

  const handleRunCode = async (overrideCode?: string, saveAsCell = false) => {
    const source = (overrideCode ?? codeDraft).trim();
    if (!source || isRunning) return;

    const entryId = `run_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`;
    const countNum = nextExecutionNumber;

    onAppendExecEntry({
      id: entryId,
      code: source,
      submittedAt: new Date().toISOString(),
      running: true,
    });
    setIsRunning(true);

    try {
      if (!sessionId || isOfflineDemo) {
        // Deterministic local simulation when previewing offline
        await new Promise((r) => window.setTimeout(r, 340));
        const wantsPlot = source.includes('matplotlib') || source.includes('plt.');
        const wantsError = source.includes('raise ') || source.includes('1/0');
        const richPlot: KernelRichOutput | undefined = wantsPlot
          ? {
              index: 0,
              mimeTypes: ['image/png'],
              title: `cell_${countNum}_figure.png`,
              filePath: `/content/.colabhive_outputs/cell_${countNum}_figure.png`,
              sizeBytes: 58_120,
              artifactUrl: buildScientificPlotDataUri(
                'loss',
                `Cell [${countNum}] · Live Matplotlib Render`,
              ),
            }
          : undefined;

        const simResult: KernelExecResult = wantsError
          ? {
              session: sessionId || 'demo',
              executionId: entryId,
              executionCount: countNum,
              status: 'error',
              stdout: '',
              stderr: '',
              outputTruncated: false,
              durationMs: 92,
              executionError: {
                name: 'RuntimeError',
                value: 'Simulated kernel exception triggered by user snippet',
                traceback: [
                  'Traceback (most recent call last):',
                  `  File "<ipython-input-${countNum}>", line 1, in <module>`,
                  `    ${source.split('\n')[0]}`,
                  'RuntimeError: Simulated kernel exception triggered by user snippet',
                ],
              },
            }
          : {
              session: sessionId || 'demo',
              executionId: entryId,
              executionCount: countNum,
              status: 'ok',
              stdout: source.startsWith('!nvidia-smi')
                ? '0, NVIDIA RTX PRO 6000 Blackwell Server Edition, 52429 MiB, 98304 MiB, 94 %, 67\n'
                : source.startsWith('!ps')
                  ? '  PID  PPID STAT %CPU %MEM     ELAPSED COMMAND\n 4182   891 R    98.4 31.2       20:14 python3\n  891     1 S     1.2  0.8       20:22 jupyter-kernel\n'
                  : `[Kernel ${sessionName || 'G4'}] Executed ${source.split('\n').length} line(s) cleanly.\n`,
              stderr: '',
              outputTruncated: false,
              result: wantsPlot ? '<Figure size 980x504 with 1 Axes>' : undefined,
              richOutputs: richPlot ? [richPlot] : [],
              durationMs: 342,
            };

        onUpdateExecEntry(entryId, { running: false, result: simResult });
        if (simResult.stdout) {
          onPushStreamText?.('stdout', `[Cell #${countNum}] ${simResult.stdout.trim()}`, richPlot);
        }
        return;
      }

      if (saveAsCell) {
        const mut = await api.mutateCell(sessionId, {
          operation: 'add_code',
          content: source,
          run_on_add: true,
          timeout_sec: 120,
        });
        if (mut.result) {
          onUpdateExecEntry(entryId, { running: false, result: mut.result });
          if (mut.result.stdout) {
            onPushStreamText?.(
              'stdout',
              mut.result.stdout,
              mut.result.richOutputs?.[0],
            );
          }
          return;
        }
      }

      const res = await api.executeCode(sessionId, {
        code: source,
        idle_timeout_sec: 120,
        store_history: true,
        inline_images: true,
        caller_name: 'workbench-ui',
      });

      onUpdateExecEntry(entryId, { running: false, result: res });
      if (res.stdout) {
        onPushStreamText?.('stdout', res.stdout, res.richOutputs?.[0]);
      }
      if (res.stderr) {
        onPushStreamText?.('stderr', res.stderr);
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      onUpdateExecEntry(entryId, {
        running: false,
        result: {
          session: sessionId || 'unknown',
          executionId: entryId,
          executionCount: countNum,
          status: 'error',
          stdout: '',
          stderr: msg,
          outputTruncated: false,
          durationMs: 0,
          executionError: {
            name: 'ExecutionRequestError',
            value: msg,
            traceback: [msg],
          },
        },
      });
      onPushStreamText?.('stderr', `[exec error] ${msg}`);
    } finally {
      setIsRunning(false);
    }
  };

  const handleInterrupt = async () => {
    if (!sessionId || interrupting) return;
    setInterrupting(true);
    try {
      if (!isOfflineDemo) {
        await api.interruptKernel(sessionId);
      }
      onPushStreamText?.('colab', '[SIGINT] Sent KeyboardInterrupt to remote IPython kernel');
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      onPushStreamText?.('stderr', `[interrupt failed] ${msg}`);
    } finally {
      setInterrupting(false);
      setIsRunning(false);
    }
  };

  const lightbox = (
    <RichOutputLightbox
      item={lightboxItem}
      isZh={isZh}
      onClose={() => setLightboxItem(null)}
    />
  );

  // --------------------------------------------------------------------------
  // Rich Outputs mode: figure gallery only
  // --------------------------------------------------------------------------
  if (galleryOnly) {
    return (
      <>
        {allRichOutputs.length === 0 ? (
          <InsetWell padding="none">
            <EmptyState
              icon={<ImageIcon />}
              title={isZh ? '还没有生成图表' : 'No charts yet'}
              description={
                isZh
                  ? '在「交互运行代码」里点击「生成示例训练曲线图」并运行，代码画出的图表会实时出现在这里。'
                  : 'Run the “Sample Training Chart” snippet under Run Code and the charts your code draws will appear here.'
              }
            />
          </InsetWell>
        ) : (
          <div className="space-y-3">
            <div className="flex items-center justify-between gap-2 px-0.5">
              <span className="text-[12.5px] font-medium text-fg-secondary">
                {isZh ? '代码生成的图表与图片' : 'Charts and images from your code'}
              </span>
              <span className="ch-num text-[11.5px] text-fg-muted">
                {allRichOutputs.length}{' '}
                {isZh ? '张' : allRichOutputs.length === 1 ? 'chart' : 'charts'}
              </span>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-4 ui-stagger">
              {allRichOutputs.map((out, idx) => {
                const imgSrc = resolveRichOutputImageSrc(out, idx);
                const title =
                  out.title || out.filePath?.split('/').pop() || `figure_${out.index + 1}.png`;
                return (
                  <InsetWell
                    key={`${title}-${idx}`}
                    padding="none"
                    className="overflow-hidden flex flex-col"
                  >
                    <div className="flex items-center justify-between gap-2 pl-3.5 pr-2 h-10">
                      <span
                        className="ch-num min-w-0 truncate text-[12px] font-medium text-fg"
                        title={out.filePath || title}
                      >
                        {title}
                      </span>
                      <div className="flex items-center gap-1 shrink-0">
                        <span className="ch-num text-[11px] text-fg-muted">
                          {formatBytes(out.sizeBytes)}
                        </span>
                        {imgSrc && (
                          <IconButton
                            size="sm"
                            label={isZh ? '放大查看' : 'Open preview'}
                            onClick={() => setLightboxItem({ src: imgSrc, output: out })}
                          >
                            <Maximize2 className="w-3.5 h-3.5" />
                          </IconButton>
                        )}
                      </div>
                    </div>

                    {imgSrc ? (
                      <button
                        type="button"
                        onClick={() => setLightboxItem({ src: imgSrc, output: out })}
                        aria-label={isZh ? `放大查看 ${title}` : `Open ${title}`}
                        className="group block w-full overflow-hidden cursor-zoom-in"
                        style={{ backgroundColor: 'var(--code-bg)' }}
                      >
                        <img
                          src={imgSrc}
                          alt={title}
                          className="w-full h-48 object-contain transition-transform duration-300 group-hover:scale-[1.015]"
                        />
                      </button>
                    ) : (
                      <pre
                        className="m-0 px-3.5 py-3 text-[11.5px] leading-relaxed overflow-x-auto"
                        style={{
                          backgroundColor: 'var(--code-bg)',
                          color: 'var(--code-fg)',
                          fontFamily: 'var(--font-mono)',
                        }}
                      >
                        {out.textData || out.filePath || (isZh ? '文件型输出' : 'File output')}
                      </pre>
                    )}

                    {out.filePath && (
                      <div className="ch-num px-3.5 py-2 truncate text-[11px] text-fg-muted">
                        {out.filePath}
                      </div>
                    )}
                  </InsetWell>
                );
              })}
            </div>
          </div>
        )}
        {lightbox}
      </>
    );
  }

  // --------------------------------------------------------------------------
  // Notebook & Run mode
  // --------------------------------------------------------------------------
  const completedStages = timeline.filter((s) => s.status === 'completed').length;

  return (
    <div className="space-y-4">
      {/* 1. Compact execution stages */}
      {timeline.length > 0 && (
        <InsetWell
          padding="sm"
          label={isZh ? '任务执行进度' : 'Execution stages'}
          headerRight={
            <span className="ch-num text-[11px] text-fg-muted">
              {completedStages}/{timeline.length} {isZh ? '步已完成' : 'done'}
            </span>
          }
        >
          <ol className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-x-5 gap-y-1.5">
            {timeline.map((step) => {
              const isDone = step.status === 'completed';
              const isRun = step.status === 'running';
              const isFail = step.status === 'failed';
              return (
                <li
                  key={step.id}
                  className="flex items-center justify-between gap-2 min-w-0 text-[12px]"
                  title={step.detail ? `${step.label} · ${step.detail}` : step.label}
                >
                  <span className="flex items-center gap-2 min-w-0">
                    {isDone ? (
                      <CheckCircle2
                        className="w-3.5 h-3.5 shrink-0"
                        style={{ color: 'var(--emerald)' }}
                      />
                    ) : isRun ? (
                      <Loader2
                        className="w-3.5 h-3.5 shrink-0 animate-spin"
                        style={{ color: 'var(--accent)' }}
                      />
                    ) : isFail ? (
                      <AlertTriangle
                        className="w-3.5 h-3.5 shrink-0"
                        style={{ color: 'var(--coral)' }}
                      />
                    ) : (
                      <Clock className="w-3.5 h-3.5 shrink-0 text-fg-muted" />
                    )}
                    <span
                      className={cn(
                        'truncate',
                        isRun ? 'font-medium text-fg' : isDone ? 'text-fg-secondary' : 'text-fg-muted',
                      )}
                      style={isFail ? { color: 'var(--coral-text)' } : undefined}
                    >
                      {step.label}
                    </span>
                  </span>
                  <span className="ch-num shrink-0 text-[11px] text-fg-muted">
                    {step.status === 'pending'
                      ? isZh
                        ? '等待中'
                        : 'pending'
                      : formatDurationMs(step.duration_ms)}
                  </span>
                </li>
              );
            })}
          </ol>
        </InsetWell>
      )}

      {/* 2. Code input well */}
      <InsetWell padding="none" className="overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 px-3 py-2.5">
          <div className="flex items-center gap-1.5 min-w-0 overflow-x-auto">
            <span
              className="ch-num shrink-0 inline-flex items-center h-6 px-2.5 rounded-full text-[11px] font-semibold"
              style={{
                backgroundColor: 'var(--brand-bg)',
                color: 'var(--brand-fg)',
              }}
              title={isZh ? `即将运行第 ${nextExecutionNumber} 段代码` : `Next run: #${nextExecutionNumber}`}
            >
              In [{nextExecutionNumber}]
            </span>
            {CODE_PRESETS.map((preset) => (
              <button
                key={preset.key}
                type="button"
                onClick={() => setCodeDraft(preset.code)}
                title={
                  isZh
                    ? '点击把这段示例代码填入下方输入框'
                    : 'Click to load this snippet into the editor below'
                }
                className={cn(
                  'shrink-0 inline-flex items-center h-6 px-2.5 rounded-full border border-line bg-surface-2 text-[11px] font-medium text-fg-secondary cursor-pointer transition-colors hover:border-line-strong hover:text-fg shadow-[var(--shadow-control)]',
                  !isZh && 'ch-num',
                )}
              >
                {isZh ? preset.zhLabel : preset.enLabel}
              </button>
            ))}
          </div>

          <div className="flex items-center gap-1.5 shrink-0 ml-auto">
            <ActionButton
              variant="ghost"
              size="sm"
              icon={<Square />}
              onClick={() => void handleInterrupt()}
              disabled={!sessionId}
              loading={interrupting}
              title={
                isZh
                  ? '立即中断正在运行的代码（相当于按下 Ctrl+C），云端实例和已加载的模型不受影响'
                  : 'Interrupt the running code (like Ctrl+C); the session and loaded models stay'
              }
            >
              {isZh ? '中断执行' : 'Interrupt'}
            </ActionButton>
            <ActionButton
              variant="secondary"
              size="sm"
              icon={<Plus />}
              onClick={() => void handleRunCode(undefined, true)}
              disabled={isRunning || !codeDraft.trim()}
              title={
                isZh
                  ? '把这段代码保存为 Notebook 单元并立即运行'
                  : 'Save this code as a notebook cell and run it right away'
              }
            >
              {isZh ? '保存并运行' : 'Save & Run'}
            </ActionButton>
            <ActionButton
              variant="primary"
              size="sm"
              icon={<Play />}
              onClick={() => void handleRunCode()}
              disabled={!codeDraft.trim()}
              loading={isRunning}
              title={isZh ? '在云端显卡上运行（快捷键 Ctrl+Enter）' : 'Run on the cloud GPU (Ctrl+Enter)'}
            >
              {isZh ? '运行代码' : 'Run Code'}
            </ActionButton>
          </div>
        </div>

        <textarea
          value={codeDraft}
          onChange={(e) => setCodeDraft(e.target.value)}
          onKeyDown={(e) => {
            if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
              e.preventDefault();
              void handleRunCode();
            }
          }}
          rows={5}
          spellCheck={false}
          aria-label={isZh ? '代码输入' : 'Code input'}
          placeholder={
            isZh
              ? '输入 Python 代码，或以 ! 开头的命令行指令，按 Ctrl+Enter 在云端显卡上运行…'
              : 'Python code or a !shell command — press Ctrl+Enter to run on the cloud GPU…'
          }
          className="block w-full px-3.5 py-3 border-0 border-t outline-none resize-y text-[12px] leading-[1.65] placeholder:text-[var(--code-muted)] focus-visible:outline-none"
          style={{
            backgroundColor: 'var(--code-bg)',
            borderColor: 'var(--code-border)',
            color: 'var(--code-fg)',
            fontFamily: 'var(--font-mono)',
          }}
        />
      </InsetWell>

      {/* 3. Cell stream (newest first) */}
      {execEntries.length === 0 ? (
        <p className="px-1 text-[12px] text-fg-muted">
          {isZh
            ? '运行过的代码和结果会显示在这里，最新的排在最上面。'
            : 'Code you run and its results appear here, newest first.'}
        </p>
      ) : (
        <div className="space-y-3">
          {[...execEntries].reverse().map((entry, revIdx) => {
            const res = entry.result;
            const countLabel = res?.executionCount ?? (entry.running ? '*' : '?');
            const hasError = res?.status === 'error' || Boolean(res?.executionError);

            return (
              <InsetWell key={entry.id} padding="none" className="overflow-hidden">
                {/* Cell header */}
                <div className="flex items-center justify-between gap-2 pl-3.5 pr-2 h-9">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="ch-num text-[11.5px] font-semibold text-fg">
                      In [{countLabel}]
                    </span>
                    {entry.running ? (
                      <StatusPill size="xs" tone="emerald" icon={<Loader2 className="animate-spin" />}>
                        {isZh ? '运行中' : 'running'}
                      </StatusPill>
                    ) : res ? (
                      <StatusPill
                        size="xs"
                        mono={!isZh}
                        tone={hasError ? 'coral' : res.status === 'abort' ? 'amber' : 'emerald'}
                      >
                        {hasError
                          ? isZh
                            ? '出错'
                            : 'error'
                          : isZh
                            ? res.status === 'ok'
                              ? '成功'
                              : '已中断'
                            : res.status}
                      </StatusPill>
                    ) : null}
                  </div>

                  <div className="flex items-center gap-2 shrink-0">
                    {res && (
                      <span className="ch-num text-[11px] text-fg-muted">
                        {formatDurationMs(res.durationMs)}
                      </span>
                    )}
                    <ActionButton
                      variant="ghost"
                      size="xs"
                      icon={<RotateCw />}
                      onClick={() => void handleRunCode(entry.code)}
                      disabled={isRunning}
                      title={isZh ? '用同样的代码再运行一次' : 'Run this code again'}
                    >
                      {isZh ? '再运行一次' : 'Re-run'}
                    </ActionButton>
                  </div>
                </div>

                {/* Source */}
                <pre
                  className="m-0 px-3.5 py-2.5 border-t border-line-faint text-[12px] leading-[1.65] text-fg overflow-x-auto"
                  style={{ fontFamily: 'var(--font-mono)' }}
                >
                  {entry.code}
                </pre>

                {/* Outputs */}
                {res && (
                  <div
                    className="px-3.5 py-3 space-y-2.5 border-t"
                    style={{
                      backgroundColor: 'var(--code-bg)',
                      borderColor: 'var(--code-border)',
                      fontFamily: 'var(--font-mono)',
                    }}
                  >
                    {res.stdout && (
                      <pre
                        className="m-0 whitespace-pre-wrap break-words text-[11.5px] leading-[1.65]"
                        style={{ color: 'var(--code-fg)' }}
                      >
                        {stripAnsiCodes(res.stdout)}
                      </pre>
                    )}

                    {res.stderr && !res.executionError && (
                      <pre
                        className="m-0 whitespace-pre-wrap break-words text-[11.5px] leading-[1.65]"
                        style={{ color: OUT_ERR }}
                      >
                        {stripAnsiCodes(res.stderr)}
                      </pre>
                    )}

                    {/* Return value */}
                    {res.result && (
                      <div
                        className="flex items-start gap-2.5 pt-2.5 border-t"
                        style={{ borderColor: OUT_HAIRLINE }}
                      >
                        <span
                          className="shrink-0 text-[11.5px] font-medium"
                          style={{ color: 'var(--code-string)' }}
                        >
                          Out [{countLabel}]
                        </span>
                        <pre
                          className="m-0 whitespace-pre-wrap break-words text-[11.5px] leading-[1.65]"
                          style={{ color: 'var(--code-key)' }}
                        >
                          {res.result}
                        </pre>
                      </div>
                    )}

                    {/* Python traceback */}
                    {res.executionError && (
                      <div
                        className="rounded-lg border px-3 py-2.5 space-y-1.5"
                        style={{
                          borderColor: 'rgba(229, 83, 61, 0.32)',
                          backgroundColor: 'rgba(229, 83, 61, 0.09)',
                        }}
                      >
                        <div
                          className="flex items-start gap-1.5 text-[11.5px] font-medium"
                          style={{ color: OUT_ERR }}
                        >
                          <AlertTriangle className="w-3.5 h-3.5 shrink-0 mt-px" />
                          <span className="break-words min-w-0">
                            {res.executionError.name}: {res.executionError.value}
                          </span>
                        </div>
                        {res.executionError.traceback?.length > 0 && (
                          <pre
                            className="m-0 text-[11px] leading-relaxed whitespace-pre-wrap break-words"
                            style={{ color: '#fecaca' }}
                          >
                            {res.executionError.traceback.map(stripAnsiCodes).join('\n')}
                          </pre>
                        )}
                      </div>
                    )}

                    {/* Inline rich output thumbnails */}
                    {res.richOutputs && res.richOutputs.length > 0 && (
                      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5 pt-0.5">
                        {res.richOutputs.map((ro, rIdx) => {
                          const src = resolveRichOutputImageSrc(ro, revIdx + rIdx);
                          if (!src) return null;
                          return (
                            <button
                              key={rIdx}
                              type="button"
                              onClick={() => setLightboxItem({ src, output: ro })}
                              className="group rounded-lg border overflow-hidden text-left cursor-zoom-in transition-colors hover:border-white/20"
                              style={{
                                borderColor: OUT_HAIRLINE,
                                backgroundColor: 'rgba(255, 255, 255, 0.02)',
                              }}
                            >
                              <img
                                src={src}
                                alt={ro.title || `Output ${rIdx}`}
                                className="w-full h-40 object-contain transition-transform duration-300 group-hover:scale-[1.01]"
                              />
                              <div
                                className="flex items-center justify-between gap-2 px-2.5 py-1.5 border-t text-[10.5px]"
                                style={{ borderColor: OUT_HAIRLINE, color: 'var(--code-muted)' }}
                              >
                                <span className="truncate">
                                  {ro.title || ro.filePath || 'figure.png'}
                                </span>
                                <span className="shrink-0">{formatBytes(ro.sizeBytes)}</span>
                              </div>
                            </button>
                          );
                        })}
                      </div>
                    )}
                  </div>
                )}
              </InsetWell>
            );
          })}
        </div>
      )}

      {lightbox}
    </div>
  );
}

function RichOutputLightbox({
  item,
  isZh,
  onClose,
}: {
  item: { src: string; output: KernelRichOutput } | null;
  isZh: boolean;
  onClose: () => void;
}) {
  const output = item?.output;
  const title = output?.title || output?.filePath?.split('/').pop() || 'colabhive_plot.png';

  const handleDownload = () => {
    if (!item) return;
    const a = document.createElement('a');
    a.href = item.src;
    a.download = title;
    a.rel = 'noopener';
    document.body.appendChild(a);
    a.click();
    a.remove();
  };

  return (
    <DialogModal
      open={item !== null}
      onClose={onClose}
      size="2xl"
      icon={<ImageIcon />}
      title={<span className="ch-num">{title}</span>}
      subtitle={
        output ? (
          <span className="ch-num">
            {output.filePath ? `${output.filePath} · ` : ''}
            {formatBytes(output.sizeBytes)}
          </span>
        ) : undefined
      }
      footer={
        <div className="flex items-center justify-end gap-2">
          <ActionButton variant="ghost" onClick={onClose}>
            {isZh ? '关闭' : 'Close'}
          </ActionButton>
          <ActionButton variant="secondary" icon={<Download />} onClick={handleDownload}>
            {isZh ? '下载图表' : 'Download'}
          </ActionButton>
        </div>
      }
    >
      {item && (
        <div
          className="flex items-center justify-center rounded-xl border overflow-hidden"
          style={{ backgroundColor: 'var(--code-bg)', borderColor: 'var(--code-border)' }}
        >
          <img src={item.src} alt={title} className="max-h-[64vh] w-auto max-w-full object-contain" />
        </div>
      )}
    </DialogModal>
  );
}
