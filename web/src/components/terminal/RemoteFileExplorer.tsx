import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  CornerLeftUp,
  Download,
  FileText,
  Folder,
  FolderOpen,
  Package,
  RefreshCw,
  Trash2,
  Upload,
  X,
} from 'lucide-react';
import type { KernelExecResult, RemoteFileEntry } from '../../types';
import { api, formatBytes } from '../../lib/api';
import { useI18n } from '../../i18n';
import { ActionButton, EmptyState, IconButton, InsetWell, StatusPill, cn } from '../ui';
import { DEMO_REMOTE_FILES, formatDurationMs } from './workbenchMockData';

export interface RemoteFileExplorerProps {
  sessionId?: string;
  isOfflineDemo?: boolean;
  onPushStreamText?: (stream: 'stdout' | 'stderr' | 'colab', text: string) => void;
}

/** Shortcuts to the three places people actually look, labelled by purpose. */
const QUICK_PATHS = [
  { path: '/content', zhLabel: '/content 工作目录', enLabel: '/content workspace' },
  {
    path: '/content/checkpoints',
    zhLabel: '/content/checkpoints 训练存档',
    enLabel: '/content/checkpoints saves',
  },
  {
    path: '/root/.cache/huggingface',
    zhLabel: 'HuggingFace 权重缓存',
    enLabel: 'HuggingFace weight cache',
  },
];

const PACKAGE_BUNDLES = [
  {
    zhLabel: '大模型推理与评测',
    enLabel: 'vLLM + Eval',
    pkgs: 'vllm transformers accelerate datasets',
  },
  {
    zhLabel: 'QLoRA 微调',
    enLabel: 'QLoRA + PEFT',
    pkgs: 'peft bitsandbytes trl safetensors',
  },
  {
    zhLabel: '编译加速组件',
    enLabel: 'Triton + Einops',
    pkgs: 'triton einops ninja packaging',
  },
  {
    zhLabel: '画图与数据分析',
    enLabel: 'Matplotlib + SciPy',
    pkgs: 'matplotlib seaborn scipy pandas',
  },
];

export function RemoteFileExplorer({
  sessionId,
  isOfflineDemo = false,
  onPushStreamText,
}: RemoteFileExplorerProps) {
  const { locale } = useI18n();
  const isZh = locale === 'zh-CN';

  const [currentPath, setCurrentPath] = useState('/content');
  const [entries, setEntries] = useState<RemoteFileEntry[]>(DEMO_REMOTE_FILES);
  const [loadingFiles, setLoadingFiles] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [downloadingPath, setDownloadingPath] = useState<string | null>(null);
  const [dragActive, setDragActive] = useState(false);
  const [statusBanner, setStatusBanner] = useState<{
    kind: 'ok' | 'err';
    message: string;
  } | null>(null);

  // `uv pip install` state
  const [pkgInput, setPkgInput] = useState('transformers accelerate safetensors');
  const [installing, setInstalling] = useState(false);
  const [installResult, setInstallResult] = useState<KernelExecResult | null>(null);

  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const loadDirectory = useCallback(
    async (targetPath: string) => {
      if (!sessionId || isOfflineDemo) {
        if (targetPath === '/content') {
          setEntries(DEMO_REMOTE_FILES);
        } else if (targetPath.includes('checkpoints')) {
          setEntries([
            {
              name: 'step_0100_adapter.safetensors',
              path: `${targetPath}/step_0100_adapter.safetensors`,
              type: 'file',
              size_bytes: 168_420_000,
              last_modified: new Date(Date.now() - 180_000).toISOString(),
              synced: true,
            },
            {
              name: 'trainer_state.json',
              path: `${targetPath}/trainer_state.json`,
              type: 'file',
              size_bytes: 12_840,
              last_modified: new Date(Date.now() - 90_000).toISOString(),
              synced: true,
            },
          ]);
        } else {
          setEntries([
            {
              name: 'loss_curve.png',
              path: `${targetPath}/loss_curve.png`,
              type: 'file',
              size_bytes: 68_420,
              last_modified: new Date().toISOString(),
              synced: true,
            },
          ]);
        }
        return;
      }

      setLoadingFiles(true);
      try {
        const list = await api.listFiles(sessionId, targetPath);
        setEntries(Array.isArray(list) ? list : []);
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        setStatusBanner({ kind: 'err', message: msg });
      } finally {
        setLoadingFiles(false);
      }
    },
    [sessionId, isOfflineDemo],
  );

  useEffect(() => {
    void loadDirectory(currentPath);
  }, [currentPath, loadDirectory]);

  const navigateUp = () => {
    if (currentPath === '/' || !currentPath) return;
    const parts = currentPath.split('/').filter(Boolean);
    parts.pop();
    setCurrentPath('/' + parts.join('/'));
  };

  const handleFileUploadList = async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    setUploading(true);
    setStatusBanner(null);

    try {
      for (const file of Array.from(files)) {
        const remoteDest = `${currentPath === '/' ? '' : currentPath}/${file.name}`;

        if (!sessionId || isOfflineDemo) {
          await new Promise((r) => window.setTimeout(r, 260));
          setEntries((prev) => [
            {
              name: file.name,
              path: remoteDest,
              type: 'file',
              size_bytes: file.size,
              last_modified: new Date().toISOString(),
              synced: true,
            },
            ...prev.filter((e) => e.path !== remoteDest),
          ]);
        } else {
          await api.uploadFile(sessionId, remoteDest, file);
          await loadDirectory(currentPath);
        }

        onPushStreamText?.(
          'colab',
          `[upload] Synced ${file.name} (${formatBytes(file.size)}) -> ${remoteDest}`,
        );
        setStatusBanner({
          kind: 'ok',
          message: isZh
            ? `已上传 ${file.name} (${formatBytes(file.size)}) 至 ${remoteDest}`
            : `Uploaded ${file.name} (${formatBytes(file.size)}) -> ${remoteDest}`,
        });
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setStatusBanner({ kind: 'err', message: msg });
    } finally {
      setUploading(false);
      if (fileInputRef.current) {
        fileInputRef.current.value = '';
      }
    }
  };

  const handleDownloadEntry = async (entry: RemoteFileEntry) => {
    setDownloadingPath(entry.path);
    setStatusBanner(null);
    try {
      if (!sessionId || isOfflineDemo) {
        const blob = new Blob([`# ColabHive Checkpoint Preview: ${entry.path}\n`], {
          type: 'text/plain;charset=utf-8',
        });
        const blobUrl = URL.createObjectURL(blob);
        try {
          const link = document.createElement('a');
          link.href = blobUrl;
          link.download = entry.name;
          link.style.display = 'none';
          document.body.appendChild(link);
          link.click();
          document.body.removeChild(link);
        } finally {
          window.setTimeout(() => URL.revokeObjectURL(blobUrl), 1000);
        }
      } else {
        await api.downloadSessionFile(sessionId, entry.path, entry.name);
      }
      onPushStreamText?.('colab', `[download] Saved ${entry.name} (${formatBytes(entry.size_bytes)})`);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setStatusBanner({
        kind: 'err',
        message: isZh ? `下载失败：${msg}` : `Download failed: ${msg}`,
      });
    } finally {
      setDownloadingPath(null);
    }
  };

  const handleDeleteEntry = async (entry: RemoteFileEntry) => {
    try {
      if (!sessionId || isOfflineDemo) {
        setEntries((prev) => prev.filter((item) => item.path !== entry.path));
      } else {
        await api.deleteFile(sessionId, entry.path);
        await loadDirectory(currentPath);
      }
      onPushStreamText?.('colab', `[rm] Deleted remote path ${entry.path}`);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setStatusBanner({ kind: 'err', message: msg });
    }
  };

  const handleInstallPackages = async (e: React.FormEvent) => {
    e.preventDefault();
    const pkgs = pkgInput
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    if (pkgs.length === 0 || installing) return;

    setInstalling(true);
    setInstallResult(null);
    try {
      if (!sessionId || isOfflineDemo) {
        await new Promise((r) => window.setTimeout(r, 480));
        const sim: KernelExecResult = {
          session: sessionId || 'demo',
          executionId: `uv_${Date.now()}`,
          executionCount: 0,
          status: 'ok',
          stdout: `Resolved ${pkgs.length} packages in 182ms\nAudited & installed: ${pkgs.join(', ')}\n`,
          stderr: '',
          outputTruncated: false,
          durationMs: 482,
        };
        setInstallResult(sim);
        onPushStreamText?.('colab', `[uv pip install] ${pkgs.join(' ')} -> OK (482ms)`);
      } else {
        const res = await api.installPackages(sessionId, pkgs);
        setInstallResult(res);
        onPushStreamText?.(
          'colab',
          `[uv pip install] ${pkgs.join(' ')} -> ${res.status.toUpperCase()} (${formatDurationMs(res.durationMs)})`,
        );
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setStatusBanner({ kind: 'err', message: msg });
    } finally {
      setInstalling(false);
    }
  };

  return (
    <div className="space-y-4">
      {/* 1. Remote file browser + dropzone */}
      <InsetWell
        padding="none"
        className={cn(
          'overflow-hidden transition-colors',
          dragActive && 'border-[var(--accent-border)] bg-accent-soft/30',
        )}
        onDragOver={(e) => {
          e.preventDefault();
          setDragActive(true);
        }}
        onDragLeave={() => setDragActive(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragActive(false);
          void handleFileUploadList(e.dataTransfer.files);
        }}
      >
        {/* Top bar: quick paths + actions */}
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 px-3 py-2.5">
          <div className="flex items-center gap-1.5 min-w-0 overflow-x-auto">
            {QUICK_PATHS.map((quick) => {
              const active = currentPath === quick.path;
              return (
                <button
                  key={quick.path}
                  type="button"
                  onClick={() => setCurrentPath(quick.path)}
                  aria-pressed={active}
                  title={isZh ? `跳转到 ${quick.path}` : `Jump to ${quick.path}`}
                  className={cn(
                    'ch-num shrink-0 inline-flex items-center h-6 px-2.5 rounded-full border text-[11px] cursor-pointer transition-colors',
                    active
                      ? 'border-transparent bg-[var(--accent)] text-[var(--accent-fg)] font-semibold shadow-[var(--shadow-control)]'
                      : 'border-line bg-surface-2 text-fg-secondary hover:border-line-strong hover:text-fg',
                  )}
                >
                  {isZh ? quick.zhLabel : quick.enLabel}
                </button>
              );
            })}
          </div>

          <div className="flex items-center gap-1.5 shrink-0 ml-auto">
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => void handleFileUploadList(e.target.files)}
            />
            <ActionButton
              variant="secondary"
              size="sm"
              icon={<Upload />}
              loading={uploading}
              title={
                isZh
                  ? '把本地脚本或数据上传到云端实例的当前目录'
                  : 'Upload local scripts or data into the current cloud folder'
              }
              onClick={() => fileInputRef.current?.click()}
            >
              {isZh ? '上传文件' : 'Upload File'}
            </ActionButton>
            <IconButton
              label={isZh ? '刷新文件列表' : 'Refresh'}
              disabled={loadingFiles}
              onClick={() => void loadDirectory(currentPath)}
            >
              <RefreshCw className={cn('w-3.5 h-3.5', loadingFiles && 'animate-spin')} />
            </IconButton>
          </div>
        </div>

        {/* Current path */}
        <div className="flex items-center justify-between gap-3 px-3 py-2 border-t border-line-faint">
          <div className="flex items-center gap-2 min-w-0">
            <button
              type="button"
              onClick={navigateUp}
              disabled={currentPath === '/'}
              aria-label={isZh ? '返回上级目录' : 'Up one level'}
              title={isZh ? '返回上级目录' : 'Up one level'}
              className="ch-num shrink-0 inline-flex items-center gap-1 h-6 px-1.5 rounded-md border border-line bg-surface-2 text-[11px] text-fg-secondary cursor-pointer transition-colors hover:border-line-strong hover:text-fg disabled:opacity-45 disabled:cursor-not-allowed"
            >
              <CornerLeftUp className="w-3 h-3" />
              <span>..</span>
            </button>
            <FolderOpen className="w-3.5 h-3.5 shrink-0 text-fg-muted" />
            <span className="font-mono ch-num truncate text-[12px] font-medium text-fg">
              {currentPath}
            </span>
          </div>
          <span className="hidden sm:inline shrink-0 text-[11.5px] text-fg-muted">
            {dragActive
              ? isZh
                ? '松开以上传到当前目录'
                : 'Drop to upload here'
              : isZh
                ? '可拖拽文件到此处上传'
                : 'Drag & drop files to upload'}
          </span>
        </div>

        {/* Status notice */}
        {statusBanner && (
          <div
            data-tone={statusBanner.kind === 'ok' ? 'emerald' : 'coral'}
            role="status"
            className="flex items-center justify-between gap-2 px-3 py-2 border-t border-line-faint text-[12px]"
            style={{ backgroundColor: 'var(--tone-soft)', color: 'var(--tone-fg)' }}
          >
            <span className="ch-num truncate">{statusBanner.message}</span>
            <button
              type="button"
              onClick={() => setStatusBanner(null)}
              aria-label={isZh ? '关闭提示' : 'Dismiss'}
              className="shrink-0 inline-flex items-center justify-center w-5 h-5 rounded-md cursor-pointer opacity-70 transition-opacity hover:opacity-100"
            >
              <X className="w-3.5 h-3.5" />
            </button>
          </div>
        )}

        {/* File rows */}
        <div className="border-t border-line-faint divide-y divide-line-faint">
          {entries.length === 0 ? (
            <EmptyState
              icon={<Folder />}
              title={isZh ? '这个文件夹还是空的' : 'This folder is empty'}
              description={
                isZh
                  ? '上传脚本或数据文件，或点击上方快捷入口切换到其他位置。'
                  : 'Upload a script or dataset, or jump to another location above.'
              }
              className="py-8"
            />
          ) : (
            entries.map((entry) => {
              const isDir = entry.type === 'directory';
              const isDownloading = downloadingPath === entry.path;

              return (
                <div
                  key={entry.path}
                  className="flex items-center justify-between gap-3 pl-3.5 pr-2 h-10 transition-colors hover:bg-[var(--hover-fill)]"
                >
                  <div className="flex items-center gap-2.5 min-w-0">
                    {isDir ? (
                      <Folder className="w-4 h-4 shrink-0 text-accent-text" />
                    ) : (
                      <FileText className="w-4 h-4 shrink-0 text-fg-muted" />
                    )}
                    {isDir ? (
                      <button
                        type="button"
                        onClick={() => setCurrentPath(entry.path)}
                        className="ch-num truncate text-left text-[12.5px] font-medium text-fg cursor-pointer hover:underline underline-offset-2"
                      >
                        {entry.name}/
                      </button>
                    ) : (
                      <span className="ch-num truncate text-[12.5px] text-fg">{entry.name}</span>
                    )}

                    {entry.synced && (
                      <StatusPill size="xs" tone="emerald" dot className="shrink-0">
                        {isZh ? '已同步' : 'Synced'}
                      </StatusPill>
                    )}
                  </div>

                  <div className="flex items-center gap-1.5 shrink-0">
                    <span className="ch-num mr-1 text-[11.5px] text-fg-muted">
                      {isDir ? (isZh ? '文件夹' : 'folder') : formatBytes(entry.size_bytes)}
                    </span>

                    {!isDir && (
                      <button
                        type="button"
                        disabled={isDownloading}
                        onClick={() => void handleDownloadEntry(entry)}
                        className="ui-btn ui-btn-ghost ui-btn-xs disabled:opacity-50"
                        title={isZh ? '下载这个文件到本地电脑' : 'Download to this computer'}
                      >
                        <Download className={cn('w-3.5 h-3.5 shrink-0', isDownloading && 'animate-pulse')} />
                        <span className="hidden sm:inline">
                          {isDownloading
                            ? isZh
                              ? '下载中…'
                              : 'Downloading…'
                            : isZh
                              ? '下载'
                              : 'Download'}
                        </span>
                      </button>
                    )}

                    <IconButton
                      size="sm"
                      label={isZh ? '删除云端文件' : 'Delete cloud file'}
                      onClick={() => void handleDeleteEntry(entry)}
                      className="hover:text-[var(--coral-text)]"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </IconButton>
                  </div>
                </div>
              );
            })
          )}
        </div>
      </InsetWell>

      {/* 2. Package installer (`uv pip install`) */}
      <InsetWell padding="md" className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2 min-w-0">
            <Package className="w-4 h-4 shrink-0 text-fg-muted" />
            <span className="text-[12.5px] font-medium text-fg">
              {isZh
                ? '一键安装 Python 依赖包 · uv 极速安装'
                : 'Install Python Packages · fast uv installer'}
            </span>
          </div>
          <span className="text-[11.5px] text-fg-muted">
            {isZh
              ? '直接装进当前云端实例，装完立刻可用'
              : 'Installs into this cloud session and is ready right away'}
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          {PACKAGE_BUNDLES.map((b) => (
            <button
              key={b.enLabel}
              type="button"
              onClick={() => setPkgInput(b.pkgs)}
              title={isZh ? `填入：${b.pkgs}` : `Fill in: ${b.pkgs}`}
              className={cn(
                'inline-flex items-center h-6 px-2.5 rounded-full border border-line bg-surface-2 text-[11px] font-medium text-fg-secondary cursor-pointer transition-colors hover:border-line-strong hover:text-fg shadow-[var(--shadow-control)]',
                !isZh && 'ch-num',
              )}
            >
              + {isZh ? b.zhLabel : b.enLabel}
            </button>
          ))}
        </div>

        <form onSubmit={handleInstallPackages} className="flex items-center gap-2">
          <input
            type="text"
            value={pkgInput}
            onChange={(e) => setPkgInput(e.target.value)}
            placeholder="vllm transformers accelerate…"
            aria-label={isZh ? '要安装的依赖包（空格分隔）' : 'Packages to install (space separated)'}
            spellCheck={false}
            className="ui-input font-mono ch-num flex-1 min-w-0"
          />
          <ActionButton
            type="submit"
            variant="primary"
            size="md"
            icon={<Package />}
            loading={installing}
            disabled={!pkgInput.trim()}
          >
            {isZh ? '立即安装' : 'Install Now'}
          </ActionButton>
        </form>

        {installResult && (
          <div className="ui-code">
            <div
              className="flex items-center justify-between gap-3 h-8 px-3 border-b text-[11px]"
              style={{
                borderColor: 'rgba(255, 255, 255, 0.06)',
                backgroundColor: 'rgba(255, 255, 255, 0.025)',
                color: 'var(--code-muted)',
              }}
            >
              <span className="ch-num inline-flex items-center gap-2">
                <span
                  className="w-1.5 h-1.5 rounded-full"
                  style={{
                    backgroundColor:
                      installResult.status === 'ok' ? 'var(--emerald)' : 'var(--coral)',
                  }}
                />
                {installResult.status === 'ok'
                  ? isZh
                    ? '安装完成'
                    : 'installed'
                  : isZh
                    ? '安装失败'
                    : 'failed'}
              </span>
              <span className="ch-num">{formatDurationMs(installResult.durationMs)}</span>
            </div>
            <pre
              className="px-3 py-2.5 whitespace-pre-wrap break-words max-h-56 overflow-y-auto"
              style={{ color: installResult.status === 'ok' ? 'var(--code-fg)' : '#fb856e' }}
            >
              {installResult.stdout || installResult.stderr || 'Completed'}
            </pre>
          </div>
        )}
      </InsetWell>
    </div>
  );
}
