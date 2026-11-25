import { useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  Check,
  Clock,
  Copy,
  Cpu,
  HardDrive,
  Link2,
  Loader2,
  Play,
  ShieldAlert,
  Sparkles,
  Square,
  Terminal,
  Users,
} from 'lucide-react';
import type {
  AcceleratorType,
  Account,
  AppInstance,
  AppLaunchRequest,
  AppModelOption,
  AppTemplate,
} from '../../types';
import { formatCU, formatHours } from '../../lib/api';
import { formatFriendlyAppError } from '../../lib/friendlyErrors';
import { useI18n } from '../../i18n';
import {
  ActionButton,
  InsetWell,
  SlideOver,
  StatusPill,
  Switch,
  cn,
  type SemanticTone,
} from './primitives';

export interface LaunchGpuOption {
  accelerator: AcceleratorType;
  label: string;
  vramLabel: string;
  vramGb: number;
  freeTier: boolean;
  defaultBurnCuHr: number;
}

export const LAUNCH_GPU_OPTIONS: LaunchGpuOption[] = [
  {
    accelerator: 'T4',
    label: 'T4',
    vramLabel: '16GB',
    vramGb: 16,
    freeTier: true,
    defaultBurnCuHr: 1.96,
  },
  {
    accelerator: 'L4',
    label: 'L4',
    vramLabel: '24GB',
    vramGb: 24,
    freeTier: false,
    defaultBurnCuHr: 4.52,
  },
  {
    accelerator: 'A100',
    label: 'A100',
    vramLabel: '40/80GB',
    vramGb: 40,
    freeTier: false,
    defaultBurnCuHr: 8.88,
  },
  {
    accelerator: 'G4',
    label: 'G4 Blackwell',
    vramLabel: '96GB',
    vramGb: 96,
    freeTier: false,
    defaultBurnCuHr: 9.66,
  },
  {
    accelerator: 'H100',
    label: 'H100',
    vramLabel: '80GB',
    vramGb: 80,
    freeTier: false,
    defaultBurnCuHr: 12.5,
  },
  {
    accelerator: 'CPU',
    label: 'CPU',
    vramLabel: '0GB',
    vramGb: 0,
    freeTier: true,
    defaultBurnCuHr: 0.74,
  },
];

export const IDLE_SHUTDOWN_PRESETS = [30, 45, 60] as const;

export function getGpuOptionMeta(acc: AcceleratorType | undefined): LaunchGpuOption {
  return (
    LAUNCH_GPU_OPTIONS.find((g) => g.accelerator === acc) ?? {
      accelerator: acc ?? 'T4',
      label: acc ?? 'GPU',
      vramLabel: '16GB',
      vramGb: 16,
      freeTier: acc === 'T4' || acc === 'CPU',
      defaultBurnCuHr: 1.96,
    }
  );
}

export interface VramMatchInfo {
  status: 'ok' | 'tight' | 'insufficient';
  tone: SemanticTone;
  label: string;
}

/** Calculates live VRAM fit status from selected GPU capacity vs model required VRAM. */
export function getVramMatchInfo(
  gpuVramGb: number,
  requiredVramGb: number,
  zh: boolean,
): VramMatchInfo {
  if (requiredVramGb <= 0) {
    return {
      status: 'ok',
      tone: 'emerald',
      label: zh ? '✅ 显存充足' : '✅ Ready',
    };
  }
  if (gpuVramGb < requiredVramGb) {
    return {
      status: 'insufficient',
      tone: 'coral',
      label: zh ? '❌ 显存不足' : '❌ Too Small',
    };
  }
  if (gpuVramGb - requiredVramGb < 4) {
    return {
      status: 'tight',
      tone: 'amber',
      label: zh ? '⚠️ 显存偏紧' : '⚠️ Tight VRAM',
    };
  }
  return {
    status: 'ok',
    tone: 'emerald',
    label: zh ? '✅ 显存充足' : '✅ Plenty VRAM',
  };
}

/** Filters executable Google Colab accounts (safely skips any legacy non-Colab DB rows). */
export function filterExecutableColabAccounts(accounts: Account[]): Account[] {
  return accounts.filter((a) => !a.provider || String(a.provider) === 'colab');
}

/** Estimates usable remaining hours on a specific Colab account for the given GPU. */
export function formatColabAccountQuotaHint(
  acc: Account,
  gpu: AcceleratorType,
  zh: boolean,
): string {
  const meta = getGpuOptionMeta(gpu);
  if (acc.subscription_tier === 'FREE' && acc.paid_cu_balance <= 0) {
    const freeCu = Math.max(0, acc.free_quota?.remaining_free_cu ?? 0);
    if (gpu === 'T4' || gpu === 'CPU') {
      return freeCu > 0
        ? zh
          ? `免费卡窗口 · 剩 ~${formatCU(freeCu, 1)} CU`
          : `Free window · ~${formatCU(freeCu, 1)} CU`
        : zh
          ? '免费档 · 动态 T4 窗口'
          : 'Free tier · Dynamic T4';
    }
    return zh ? '免费档（仅限 T4 / CPU）' : 'Free tier (T4 / CPU only)';
  }

  const usableCu = acc.is_contributor ? acc.schedulable_cu : acc.paid_cu_balance;
  const burnRate =
    acc.gpu_entitlements?.[gpu]?.calibrated_burn_rate ||
    acc.gpu_equivalence?.[gpu]?.burn_rate_cu_hr ||
    meta.defaultBurnCuHr;
  const estHours = burnRate > 0 ? Math.max(0, usableCu / burnRate) : 0;
  return `${formatCU(usableCu, 1)} CU · ≈ ${formatHours(estHours)} ${gpu}`;
}

export function validateCustomModelDownloadUrl(
  raw: string,
  zh: boolean,
): { ok: true; url: string } | { ok: false; error: string } {
  const trimmed = raw.trim();
  if (!trimmed) {
    return {
      ok: false,
      error: zh
        ? '请粘贴 Hugging Face 或 CivitAI 模型直链（https://huggingface.co/... 或 https://civitai.com/...）'
        : 'Paste a direct Hugging Face or CivitAI model URL',
    };
  }
  if (/[\s;&|$`()<>'"\\!]/.test(trimmed)) {
    return {
      ok: false,
      error: zh
        ? '链接包含不允许的空格或特殊字符，请检查后重试'
        : 'URL contains disallowed whitespace or shell characters',
    };
  }
  try {
    const u = new URL(trimmed);
    const host = u.hostname.toLowerCase();
    const validHost =
      host === 'huggingface.co' ||
      host.endsWith('.huggingface.co') ||
      host === 'civitai.com' ||
      host.endsWith('.civitai.com');
    if (u.protocol !== 'https:' || !validHost || u.username || u.password) {
      return {
        ok: false,
        error: zh
          ? '仅支持 https://huggingface.co/... 或 https://civitai.com/... 官方下载链接'
          : 'Only https://huggingface.co/... or https://civitai.com/... URLs are supported',
      };
    }
    if (!u.pathname || u.pathname === '/') {
      return {
        ok: false,
        error: zh
          ? '请输入指向具体模型权重文件的完整下载路径'
          : 'Enter a full path to a model weight file',
      };
    }
    return { ok: true, url: trimmed };
  } catch {
    return {
      ok: false,
      error: zh ? '链接格式无效，请以 https:// 开头' : 'Invalid URL format (must start with https://)',
    };
  }
}

function formatModelVramBadge(opt: AppModelOption, zh: boolean): string {
  const idLower = opt.id.toLowerCase();
  if (idLower === 'blank') {
    return zh ? `0 GB 空白 · 需 ${opt.min_vram_gb}GB 显存` : `0 GB blank · ${opt.min_vram_gb}GB VRAM`;
  }
  if (opt.min_vram_gb <= 0) {
    return zh ? '0 GB 空白' : '0 GB preload';
  }
  if (idLower === 'z-image-turbo') {
    return zh ? `千万下载 · 需 ${opt.min_vram_gb}GB 显存` : `10M+ DL · ${opt.min_vram_gb}GB VRAM`;
  }
  if (idLower === 'qwen-image-fp8') {
    return zh ? `280万下载 · 需 ${opt.min_vram_gb}GB 显存` : `2.8M+ DL · ${opt.min_vram_gb}GB VRAM`;
  }
  if (idLower === 'flux2-dev-fp8') {
    return zh ? `92万下载 · 需 ${opt.min_vram_gb}GB 显存` : `920K+ DL · ${opt.min_vram_gb}GB VRAM`;
  }
  if (idLower === 'minimax-h3' || idLower === 'ltx-2.5' || idLower === 'wan-2.2' || idLower === 'image-pack') {
    return zh ? `界面按需拉取 · 需 ${opt.min_vram_gb}GB 显存` : `On-demand in UI · ${opt.min_vram_gb}GB VRAM`;
  }
  return zh ? `需 ~${opt.min_vram_gb} GB 显存` : `~${opt.min_vram_gb} GB VRAM`;
}

export interface AppLaunchDrawerProps {
  open: boolean;
  template: AppTemplate | null;
  accounts: Account[];
  defaultAutoResume?: boolean;
  launching?: boolean;
  launchingInstance?: AppInstance | null;
  launchError?: string | null;
  cancelingLaunch?: boolean;
  isCloudMode?: boolean;
  onClose: () => void;
  onConfirm: (req: AppLaunchRequest) => Promise<void> | void;
  onCancelLaunch?: (instanceId: string) => Promise<void> | void;
}

const CUSTOM_MODEL_SENTINEL = '__custom_model_url__';
const QUEUE_WAIT_PRESETS_MIN = [5, 10, 20] as const;

export function AppLaunchDrawer({
  open,
  template,
  accounts,
  defaultAutoResume = true,
  launching = false,
  launchingInstance = null,
  launchError = null,
  cancelingLaunch = false,
  isCloudMode = false,
  onClose,
  onConfirm,
  onCancelLaunch,
}: AppLaunchDrawerProps) {
  const { locale, displayEmail } = useI18n();
  const zh = locale === 'zh-CN';

  const modelOptions = useMemo(() => template?.model_options ?? [], [template]);
  const colabAccounts = useMemo(() => filterExecutableColabAccounts(accounts), [accounts]);

  const [selectedModelKey, setSelectedModelKey] = useState<string>('');
  const [customModelUrl, setCustomModelUrl] = useState<string>('');
  const [selectedGpu, setSelectedGpu] = useState<AcceleratorType>('T4');
  const [selectedAccountId, setSelectedAccountId] = useState<string>('');
  const [idleMinutes, setIdleMinutes] = useState<number>(45);
  const [launchMode, setLaunchMode] = useState<'headless' | 'native_webui'>('headless');
  const [queueRetryEnabled, setQueueRetryEnabled] = useState<boolean>(false);
  const [queueMaxWaitMin, setQueueMaxWaitMin] = useState<number>(10);
  const [autoFallback, setAutoFallback] = useState<boolean>(false);
  const [launchElapsedSec, setLaunchElapsedSec] = useState<number>(0);
  const [copiedLogs, setCopiedLogs] = useState<boolean>(false);
  const logBoxRef = useRef<HTMLDivElement | null>(null);

  const launchLogs = launchingInstance?.deploy_logs ?? [];

  useEffect(() => {
    if (!launching) {
      setLaunchElapsedSec(0);
      return;
    }
    const baseMs = launchingInstance?.started_at ? Date.parse(launchingInstance.started_at) : Date.now();
    const startMs = Number.isFinite(baseMs) ? baseMs : Date.now();
    setLaunchElapsedSec(Math.max(0, Math.floor((Date.now() - startMs) / 1000)));
    const id = window.setInterval(() => {
      setLaunchElapsedSec(Math.max(0, Math.floor((Date.now() - startMs) / 1000)));
    }, 1000);
    return () => window.clearInterval(id);
  }, [launching, launchingInstance?.started_at]);

  useEffect(() => {
    if (!logBoxRef.current) return;
    logBoxRef.current.scrollTop = logBoxRef.current.scrollHeight;
  }, [launchLogs.length]);

  // Reset defaults cleanly whenever drawer opens for a template
  useEffect(() => {
    if (!open || !template) return;
    const firstOpt = template.model_options?.[0];
    setSelectedModelKey(firstOpt ? firstOpt.id : '');
    setCustomModelUrl('');
    setSelectedGpu(firstOpt?.recommended_gpu ?? template.recommended_gpu ?? 'T4');
    setSelectedAccountId('');
    setIdleMinutes(45);
    setLaunchMode(template.media_api_base ? 'headless' : 'native_webui');
    setQueueRetryEnabled(false);
    setQueueMaxWaitMin(10);
    setAutoFallback(false);
  }, [open, template]);

  const isCustomModel =
    Boolean(template?.supports_custom_model_url) && selectedModelKey === CUSTOM_MODEL_SENTINEL;

  const activeModelOption = useMemo(
    () => (isCustomModel ? null : modelOptions.find((m) => m.id === selectedModelKey) ?? modelOptions[0] ?? null),
    [isCustomModel, modelOptions, selectedModelKey],
  );

  const requiredVramGb = isCustomModel
    ? Math.max(12, template?.min_vram_gb ?? 12)
    : activeModelOption?.min_vram_gb ?? template?.min_vram_gb ?? 0;

  const recommendedGpu: AcceleratorType = isCustomModel
    ? 'L4'
    : activeModelOption?.recommended_gpu ?? template?.recommended_gpu ?? 'T4';

  const customUrlValidation = useMemo(() => {
    if (!isCustomModel) return null;
    return validateCustomModelDownloadUrl(customModelUrl, zh);
  }, [isCustomModel, customModelUrl, zh]);

  const selectedGpuMeta = getGpuOptionMeta(selectedGpu);
  const selectedVramMatch = getVramMatchInfo(selectedGpuMeta.vramGb, requiredVramGb, zh);

  // Count how many enabled Colab accounts appear eligible for each GPU
  const eligibleCountByGpu = useMemo(() => {
    const counts: Partial<Record<AcceleratorType, number>> = {};
    for (const opt of LAUNCH_GPU_OPTIONS) {
      let n = 0;
      for (const acc of colabAccounts) {
        if (!acc.enabled || acc.health === 'AUTH_EXPIRED' || acc.health === 'QUARANTINED_DENYLIST') {
          continue;
        }
        const ent = acc.gpu_entitlements?.[opt.accelerator];
        const restricted = ent?.status === 'TIER_RESTRICTED' || ent?.status === 'INELIGIBLE';
        if (!restricted) n += 1;
      }
      counts[opt.accelerator] = n;
    }
    return counts;
  }, [colabAccounts]);

  const handleSelectModelOption = (nextKey: string) => {
    setSelectedModelKey(nextKey);
    if (nextKey === CUSTOM_MODEL_SENTINEL) {
      setSelectedGpu('L4');
      return;
    }
    const found = modelOptions.find((m) => m.id === nextKey);
    if (found?.recommended_gpu) {
      setSelectedGpu(found.recommended_gpu);
    }
  };

  const friendlyLaunchErr = useMemo(
    () => (launchError && !launching ? formatFriendlyAppError(launchError, zh) : null),
    [launchError, launching, zh],
  );

  if (!template) return null;

  const appDisplayName = zh ? template.name_zh || template.name : template.name || template.name_zh;
  const selectedAccount = colabAccounts.find((a) => a.id === selectedAccountId) ?? null;
  const accountSummaryText = selectedAccount
    ? selectedAccount.alias || displayEmail(selectedAccount.email)
    : zh
      ? '自动智能分配'
      : 'Auto-Route';

  const modelSummaryText = isCustomModel
    ? customUrlValidation?.ok
      ? `${zh ? '自定义权重' : 'Custom'} (${customUrlValidation.url.split('/').pop() || 'file'})`
      : zh
        ? '自定义模型链接'
        : 'Custom Model URL'
    : activeModelOption
      ? zh
        ? activeModelOption.name_zh || activeModelOption.name
        : activeModelOption.name || activeModelOption.name_zh
      : zh
        ? '默认运行环境'
        : 'Default Runtime';

  const canSubmit =
    !launching &&
    !isCloudMode &&
    (!isCustomModel || Boolean(customUrlValidation?.ok));

  const buildLaunchRequest = (overrides?: Partial<AppLaunchRequest>): AppLaunchRequest => {
    const isFreeGpu = selectedGpu === 'T4' || selectedGpu === 'CPU';
    const nextQueue = overrides?.queue_retry_enabled ?? queueRetryEnabled;
    const nextFallback = overrides?.auto_fallback ?? autoFallback;
    return {
      template_id: template.id,
      preferred_gpu: selectedGpu,
      account_id: selectedAccountId || undefined,
      strategy: isFreeGpu ? 'free-first' : 'waste-zero',
      selected_model_id: isCustomModel ? undefined : activeModelOption?.id || undefined,
      model_download_url: isCustomModel && customUrlValidation?.ok ? customUrlValidation.url : undefined,
      preferred_local_port: template.default_local_port,
      idle_auto_stop_min: idleMinutes,
      auto_resume_enabled: defaultAutoResume,
      launch_mode: template.media_api_base ? launchMode : undefined,
      auto_fallback: nextFallback,
      queue_retry_enabled: nextQueue,
      queue_max_wait_sec: nextQueue ? queueMaxWaitMin * 60 : undefined,
      ...overrides,
    };
  };

  const handleConfirmClick = () => {
    if (!canSubmit) return;
    void onConfirm(buildLaunchRequest());
  };

  const handleEnableQueueAndRetry = () => {
    if (!canSubmit) return;
    setQueueRetryEnabled(true);
    void onConfirm(
      buildLaunchRequest({
        queue_retry_enabled: true,
        queue_max_wait_sec: queueMaxWaitMin * 60,
      }),
    );
  };

  return (
    <SlideOver
      open={open}
      onClose={onClose}
      width="2xl"
      icon={<span className="text-[16px] leading-none">{template.icon || '🚀'}</span>}
      title={
        zh ? `配置并启动 · ${appDisplayName}` : `Configure & Launch · ${appDisplayName}`
      }
      subtitle={
        zh
          ? '3 步透明确认模型权重、目标显卡与执行账号 —— 所选即所得，不偷偷替换配置'
          : '3-step transparent launch: choose model weights, exact GPU tier, and Colab account.'
      }
      className="space-y-5"
      footer={
        <div className="space-y-3">
          {/* Step 3 Crisp 1-Line Transparent Confirmation Summary */}
          <div
            className="rounded-xl border px-3.5 py-2.5 flex items-start gap-2.5 text-[12.5px] leading-relaxed"
            style={{
              backgroundColor:
                selectedVramMatch.status === 'insufficient'
                  ? 'var(--amber-soft)'
                  : 'var(--surface-1)',
              borderColor:
                selectedVramMatch.status === 'insufficient'
                  ? 'var(--amber-border)'
                  : 'var(--border-subtle)',
            }}
          >
            <Sparkles
              className="w-4 h-4 shrink-0 mt-0.5"
              style={{
                color:
                  selectedVramMatch.status === 'insufficient'
                    ? 'var(--amber-text)'
                    : 'var(--emerald-text)',
              }}
            />
            <div className="min-w-0 flex-1">
              <div className="font-semibold text-fg">
                {zh ? (
                  <>
                    即将使用{' '}
                    <span className="ch-num text-[var(--emerald-text)]">
                      [{selectedGpuMeta.label} {selectedGpuMeta.vramLabel} · {accountSummaryText}]
                    </span>{' '}
                    启动 <span>[{appDisplayName}]</span>，加载 <span>[{modelSummaryText}]</span>
                  </>
                ) : (
                  <>
                    Launching <span>[{appDisplayName}]</span> on{' '}
                    <span className="ch-num text-[var(--emerald-text)]">
                      [{selectedGpuMeta.label} {selectedGpuMeta.vramLabel} · {accountSummaryText}]
                    </span>{' '}
                    with <span>[{modelSummaryText}]</span>
                  </>
                )}
              </div>
              {selectedVramMatch.status === 'insufficient' && (
                <div className="mt-1 text-[11.5px] text-[var(--amber-text)]">
                  {zh
                    ? `提醒：当前所选 ${selectedGpuMeta.label} (${selectedGpuMeta.vramLabel}) 显存低于模型建议值 (${requiredVramGb}GB)，建议切换至 ${recommendedGpu} 或更高显存显卡。`
                    : `Heads up: ${selectedGpuMeta.label} (${selectedGpuMeta.vramLabel}) is below the recommended ${requiredVramGb}GB VRAM. Consider picking ${recommendedGpu}.`}
                </div>
              )}
              {launching && (
                <div className="mt-2 space-y-2">
                  <div className="flex items-center justify-between gap-2 text-[11.5px] text-fg-secondary">
                    <div className="flex items-center gap-2 min-w-0">
                      <Loader2 className="w-3.5 h-3.5 animate-spin shrink-0 text-[var(--emerald)]" />
                      <span className="truncate font-medium text-fg">
                        {zh
                          ? launchingInstance?.status_step_zh || '正在云端分配显卡、安装应用并加载依赖…'
                          : launchingInstance?.status_step || 'Allocating cloud GPU & provisioning studio…'}
                      </span>
                    </div>
                    <span className="ch-num shrink-0 font-semibold text-fg-muted">
                      {Math.max(5, Math.min(99, Math.round(launchingInstance?.progress_pct ?? 10)))}% ·{' '}
                      {String(Math.floor(launchElapsedSec / 60)).padStart(2, '0')}:
                      {String(launchElapsedSec % 60).padStart(2, '0')}
                    </span>
                  </div>
                  <div
                    className="h-1.5 w-full rounded-full overflow-hidden"
                    style={{ backgroundColor: 'var(--surface-3, rgba(120,113,108,0.16))' }}
                  >
                    <div
                      className="h-full rounded-full transition-[width] duration-500 ease-out"
                      style={{
                        width: `${Math.max(8, Math.min(99, Math.round(launchingInstance?.progress_pct ?? 10)))}%`,
                        backgroundColor: 'var(--emerald)',
                      }}
                    />
                  </div>
                  <div
                    className="rounded-lg border overflow-hidden"
                    style={{ borderColor: 'var(--border-subtle)', backgroundColor: '#18181B' }}
                  >
                    <div
                      className="px-2.5 py-1.5 flex items-center justify-between gap-2 text-[10.5px] text-zinc-300"
                      style={{ borderBottom: '1px solid rgba(255,255,255,0.08)' }}
                    >
                      <span className="inline-flex items-center gap-1.5 font-medium">
                        <Terminal className="w-3 h-3 text-emerald-400" />
                        {zh ? '实时部署日志' : 'Live Deploy Logs'}
                        <span className="text-zinc-500 ch-num">
                          ({zh ? `${launchLogs.length} 条` : `${launchLogs.length} lines`})
                        </span>
                      </span>
                      {launchLogs.length > 0 && (
                        <button
                          type="button"
                          onClick={() => {
                            void navigator.clipboard.writeText(launchLogs.join('\n')).then(() => {
                              setCopiedLogs(true);
                              window.setTimeout(() => setCopiedLogs(false), 1600);
                            }).catch(() => undefined);
                          }}
                          className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-200 cursor-pointer"
                        >
                          {copiedLogs ? <Check className="w-3 h-3 text-emerald-400" /> : <Copy className="w-3 h-3" />}
                          <span>{copiedLogs ? (zh ? '已复制' : 'Copied') : zh ? '复制' : 'Copy'}</span>
                        </button>
                      )}
                    </div>
                    <div
                      ref={logBoxRef}
                      className="max-h-36 overflow-y-auto px-2.5 py-2 font-mono text-[11px] leading-relaxed space-y-0.5 text-zinc-100"
                    >
                      {launchLogs.length === 0 ? (
                        <div className="text-zinc-400 animate-pulse">
                          {zh ? '正在连接云端显卡并拉取实时部署日志…' : 'Connecting to cloud GPU for live deploy logs…'}
                        </div>
                      ) : (
                        launchLogs.map((ln, i) => (
                          <div
                            key={`${i}-${ln.slice(0, 20)}`}
                            className="break-all whitespace-pre-wrap"
                            style={{
                              color:
                                ln.includes('失败') || ln.toLowerCase().includes('error')
                                  ? '#FCA5A5'
                                  : ln.includes('已分配') || ln.includes('就绪') || ln.includes('已自动释放')
                                    ? '#6EE7B7'
                                    : '#E4E4E7',
                            }}
                          >
                            {ln}
                          </div>
                        ))
                      )}
                    </div>
                  </div>
                </div>
              )}
            </div>
          </div>

          {friendlyLaunchErr && (
            <div
              role="alert"
              className="rounded-xl border px-3.5 py-2.5 text-[12px] space-y-1.5"
              style={{
                backgroundColor: 'var(--coral-soft)',
                borderColor: 'var(--coral-border)',
                color: 'var(--coral-text)',
              }}
            >
              <div className="flex items-start gap-2">
                <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
                <div className="min-w-0 flex-1 space-y-1">
                  <div className="font-semibold leading-snug">{friendlyLaunchErr.title}</div>
                  <div className="opacity-90 leading-relaxed">{friendlyLaunchErr.summary}</div>
                  {friendlyLaunchErr.suggestion && (
                    <div className="opacity-90 leading-relaxed">{friendlyLaunchErr.suggestion}</div>
                  )}
                </div>
              </div>
              {friendlyLaunchErr.canQueueRetry && (
                <div className="flex flex-wrap items-center gap-2 pt-1 pl-6">
                  <button
                    type="button"
                    disabled={!canSubmit}
                    onClick={handleEnableQueueAndRetry}
                    className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-[11.5px] font-semibold cursor-pointer border transition-colors"
                    style={{
                      backgroundColor: 'var(--accent)',
                      color: 'var(--accent-fg)',
                      borderColor: 'var(--accent)',
                    }}
                  >
                    <Sparkles className="w-3 h-3" />
                    {zh ? '立即开启安全候补抢卡重试' : 'Retry with Safe GPU Waitlist'}
                  </button>
                </div>
              )}
            </div>
          )}

          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-[11.5px] text-fg-muted ch-num">
              {zh
                ? `本机私有直连：127.0.0.1:${template.default_local_port} · ${idleMinutes} 分钟防忘关机`
                : `Loopback: 127.0.0.1:${template.default_local_port} · ${idleMinutes}m auto-stop`}
            </span>
            <div className="flex items-center gap-2">
              <ActionButton variant="ghost" size="sm" onClick={onClose}>
                {launching
                  ? zh
                    ? '后台继续（收起窗口）'
                    : 'Minimize (Keep Deploying)'
                  : zh
                    ? '取消'
                    : 'Cancel'}
              </ActionButton>
              {launching && launchingInstance?.id && onCancelLaunch ? (
                <ActionButton
                  variant="danger"
                  size="sm"
                  loading={cancelingLaunch}
                  icon={<Square />}
                  onClick={() => void onCancelLaunch(launchingInstance.id)}
                >
                  {zh ? '取消部署并释放显卡' : 'Cancel & Release GPU'}
                </ActionButton>
              ) : (
                <ActionButton
                  variant="primary"
                  size="md"
                  loading={launching}
                  disabled={!canSubmit}
                  icon={<Play />}
                  onClick={handleConfirmClick}
                >
                  {launching
                    ? queueRetryEnabled
                      ? zh
                        ? '正在安全候补抢卡 / 启动…'
                        : 'Waitlisting / Launching…'
                      : zh
                        ? '正在启动并加载…'
                        : 'Launching…'
                    : queueRetryEnabled
                      ? zh
                        ? `候补抢卡并启动 (${selectedGpu})`
                        : `Queue & Launch (${selectedGpu})`
                      : zh
                        ? '确认并立即启动'
                        : 'Confirm & Launch'}
                </ActionButton>
              )}
            </div>
          </div>
        </div>
      }
    >
      {isCloudMode && (
        <div
          role="alert"
          className="rounded-xl border px-3.5 py-3 flex items-start gap-2.5 text-[12.5px]"
          style={{
            backgroundColor: 'var(--amber-soft)',
            borderColor: 'var(--amber-border)',
            color: 'var(--amber-text)',
          }}
        >
          <ShieldAlert className="w-4 h-4 shrink-0 mt-0.5" />
          <span>
            {zh
              ? '云端模式暂不支持本地端口桥接，请使用桌面/本机模式或配置独立隔离代理域名后再启动应用。'
              : 'Cloud mode does not support local port bridging yet. Use desktop/local mode or configure an isolated app proxy domain.'}
          </span>
        </div>
      )}

      {/* ==================================================================== */}
      {/* Step 1 — 选择要加载的模型 / 权重                                        */}
      {/* ==================================================================== */}
      <section aria-labelledby="launch-step-1" className="space-y-3">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span
              className="ch-num inline-flex items-center justify-center w-5 h-5 rounded-full text-[11px] font-bold"
              style={{
                backgroundColor: 'var(--accent)',
                color: 'var(--accent-fg)',
              }}
            >
              1
            </span>
            <h3 id="launch-step-1" className="text-[14px] font-semibold text-fg">
              {zh ? '选择要加载的模型 / 权重' : 'Step 1 · Choose Model / Weights'}
            </h3>
          </div>
          <span className="text-[11.5px] text-fg-muted">
            {zh ? '将真实预下载到云端显卡目录' : 'Provisioned directly inside VM'}
          </span>
        </div>

        {modelOptions.length > 0 || template.supports_custom_model_url ? (
          <div role="radiogroup" aria-label={zh ? '模型权重选项' : 'Model options'} className="space-y-2.5">
            {modelOptions.map((opt) => {
              const selected = !isCustomModel && activeModelOption?.id === opt.id;
              return (
                <button
                  key={opt.id}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  disabled={launching}
                  onClick={() => handleSelectModelOption(opt.id)}
                  className={cn(
                    'w-full rounded-2xl border p-3.5 text-left transition-all cursor-pointer flex items-start justify-between gap-3',
                    selected
                      ? 'bg-surface-2 border-[var(--accent)] shadow-[var(--shadow-control)]'
                      : 'bg-surface-1/70 border-line-subtle hover:border-line-strong hover:bg-surface-2/60',
                  )}
                >
                  <div className="flex items-start gap-2.5 min-w-0 flex-1">
                    <span
                      className={cn(
                        'mt-0.5 w-4 h-4 rounded-full border flex items-center justify-center shrink-0 transition-colors',
                        selected
                          ? 'border-[var(--accent)] bg-[var(--accent)] text-[var(--accent-fg)]'
                          : 'border-line-strong bg-surface-2',
                      )}
                    >
                      {selected && <Check className="w-2.5 h-2.5" />}
                    </span>
                    <div className="min-w-0 space-y-1">
                      <div className="text-[13.5px] font-semibold text-fg leading-snug">
                        {zh ? opt.name_zh || opt.name : opt.name || opt.name_zh}
                      </div>
                      {(opt.summary_zh || opt.summary) && (
                        <p className="text-[12px] leading-relaxed text-fg-secondary m-0">
                          {zh ? opt.summary_zh || opt.summary : opt.summary || opt.summary_zh}
                        </p>
                      )}
                    </div>
                  </div>

                  <div className="flex flex-col items-end gap-1.5 shrink-0">
                    <StatusPill size="xs" tone="neutral" mono icon={<HardDrive />}>
                      {formatModelVramBadge(opt, zh)}
                    </StatusPill>
                    <StatusPill
                      size="xs"
                      tone={opt.recommended_gpu === 'T4' || opt.recommended_gpu === 'CPU' ? 'emerald' : 'amber'}
                      mono
                    >
                      {zh ? `推荐 ${opt.recommended_gpu}` : `Rec. ${opt.recommended_gpu}`}
                    </StatusPill>
                  </div>
                </button>
              );
            })}

            {template.supports_custom_model_url && (
              <div
                className={cn(
                  'rounded-2xl border p-3.5 transition-all space-y-3',
                  isCustomModel
                    ? 'bg-surface-2 border-[var(--accent)] shadow-[var(--shadow-control)]'
                    : 'bg-surface-1/70 border-line-subtle hover:border-line-strong',
                )}
              >
                <button
                  type="button"
                  role="radio"
                  aria-checked={isCustomModel}
                  disabled={launching}
                  onClick={() => handleSelectModelOption(CUSTOM_MODEL_SENTINEL)}
                  className="w-full flex items-start justify-between gap-3 text-left cursor-pointer bg-transparent border-0 p-0"
                >
                  <div className="flex items-start gap-2.5 min-w-0">
                    <span
                      className={cn(
                        'mt-0.5 w-4 h-4 rounded-full border flex items-center justify-center shrink-0 transition-colors',
                        isCustomModel
                          ? 'border-[var(--accent)] bg-[var(--accent)] text-[var(--accent-fg)]'
                          : 'border-line-strong bg-surface-2',
                      )}
                    >
                      {isCustomModel && <Check className="w-2.5 h-2.5" />}
                    </span>
                    <div className="min-w-0 space-y-0.5">
                      <div className="text-[13.5px] font-semibold text-fg flex items-center gap-1.5">
                        <Link2 className="w-3.5 h-3.5 shrink-0 text-[var(--amber-text)]" />
                        <span>
                          {zh
                            ? '粘贴自定义模型下载链接 (Hugging Face / CivitAI)'
                            : 'Paste Custom Model Download URL (Hugging Face / CivitAI)'}
                        </span>
                      </div>
                      <p className="text-[12px] text-fg-secondary m-0">
                        {zh
                          ? '自动下载 .safetensors / .ckpt 权重到 ComfyUI models/checkpoints 目录'
                          : 'Downloads .safetensors / .ckpt weights directly into ComfyUI models/checkpoints'}
                      </p>
                    </div>
                  </div>
                  <StatusPill size="xs" tone="sky" mono>
                    HF / CivitAI
                  </StatusPill>
                </button>

                {isCustomModel && (
                  <div className="pl-6 space-y-1.5">
                    <input
                      type="url"
                      value={customModelUrl}
                      disabled={launching}
                      onChange={(e) => setCustomModelUrl(e.target.value)}
                      placeholder="https://huggingface.co/.../resolve/main/model.safetensors"
                      className="ui-input ch-num w-full text-[12px]"
                    />
                    {customUrlValidation && !customUrlValidation.ok && (
                      <div className="flex items-center gap-1.5 text-[11.5px] text-[var(--coral-text)]">
                        <AlertCircle className="w-3.5 h-3.5 shrink-0" />
                        <span>{customUrlValidation.error}</span>
                      </div>
                    )}
                    {customUrlValidation?.ok && (
                      <div className="flex items-center gap-1.5 text-[11.5px] text-[var(--emerald-text)]">
                        <Check className="w-3.5 h-3.5 shrink-0" />
                        <span>
                          {zh
                            ? '下载链接格式校验通过，启动时将自动拉取并校验文件大小'
                            : 'URL validated — weight file will be fetched and verified on boot'}
                        </span>
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        ) : (
          <InsetWell padding="sm">
            <div className="flex flex-wrap items-center justify-between gap-2 text-[12.5px]">
              <span className="font-medium text-fg">
                {zh ? '插件默认运行环境（由清单脚本自动准备）' : 'Plugin default runtime environment'}
              </span>
              <StatusPill size="xs" tone="neutral" mono>
                {template.min_vram_gb > 0
                  ? zh
                    ? `建议 ≥ ${template.min_vram_gb}GB 显存 · 推荐 ${template.recommended_gpu}`
                    : `≥ ${template.min_vram_gb}GB VRAM · Rec. ${template.recommended_gpu}`
                  : zh
                    ? '0 GB 显存要求'
                    : '0 GB VRAM required'}
              </StatusPill>
            </div>
          </InsetWell>
        )}
      </section>

      {/* ==================================================================== */}
      {/* Step 2 — 选择调用哪张显卡 & 账号（100% 透明）                             */}
      {/* ==================================================================== */}
      <section aria-labelledby="launch-step-2" className="space-y-4">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span
              className="ch-num inline-flex items-center justify-center w-5 h-5 rounded-full text-[11px] font-bold"
              style={{
                backgroundColor: 'var(--accent)',
                color: 'var(--accent-fg)',
              }}
            >
              2
            </span>
            <h3 id="launch-step-2" className="text-[14px] font-semibold text-fg">
              {zh ? '选择调用哪张显卡 & 账号' : 'Step 2 · Choose GPU & Account'}
            </h3>
          </div>
          <span className="text-[11.5px] text-fg-muted">
            {zh ? '100% 透明指定，绝不暗中降级' : 'Explicit selection · Never silently overridden'}
          </span>
        </div>

        {/* GPU Picker Cards Grid */}
        <div className="space-y-2">
          <div className="flex items-center justify-between text-[12px] font-medium text-fg-secondary">
            <span className="inline-flex items-center gap-1.5">
              <Cpu className="w-3.5 h-3.5 text-fg-muted" />
              {zh ? '目标显卡规格与显存匹配' : 'Target GPU & Live VRAM Fit'}
            </span>
            <span className="ch-num text-[11px] text-fg-muted">
              {requiredVramGb > 0
                ? zh
                  ? `所选模型需要 ≥ ${requiredVramGb} GB 显存`
                  : `Model needs ≥ ${requiredVramGb} GB VRAM`
                : zh
                  ? '无强制显存要求'
                  : 'No strict VRAM minimum'}
            </span>
          </div>

          <div
            role="radiogroup"
            aria-label={zh ? '选择显卡规格' : 'Select GPU tier'}
            className="grid grid-cols-2 sm:grid-cols-3 gap-2.5"
          >
            {LAUNCH_GPU_OPTIONS.map((gpu) => {
              const selected = selectedGpu === gpu.accelerator;
              const match = getVramMatchInfo(gpu.vramGb, requiredVramGb, zh);
              const isRec = gpu.accelerator === recommendedGpu;
              const availAccounts = eligibleCountByGpu[gpu.accelerator] ?? 0;

              return (
                <button
                  key={gpu.accelerator}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  disabled={launching}
                  onClick={() => setSelectedGpu(gpu.accelerator)}
                  className={cn(
                    'rounded-2xl border p-3 text-left transition-all cursor-pointer flex flex-col justify-between gap-2',
                    selected
                      ? 'bg-surface-2 border-[var(--accent)] shadow-[var(--shadow-control)]'
                      : 'bg-surface-1/70 border-line-subtle hover:border-line-strong hover:bg-surface-2/60',
                  )}
                >
                  <div className="flex items-start justify-between gap-1.5">
                    <div>
                      <div className="text-[13px] font-bold text-fg leading-tight">
                        {gpu.label}
                      </div>
                      <div className="ch-num text-[11.5px] font-medium text-fg-secondary">
                        {gpu.vramLabel}
                      </div>
                    </div>
                    {isRec ? (
                      <StatusPill size="xs" tone="accent">
                        {zh ? '推荐' : 'Rec'}
                      </StatusPill>
                    ) : gpu.freeTier ? (
                      <StatusPill size="xs" tone="emerald">
                        {zh ? '免费档' : 'Free'}
                      </StatusPill>
                    ) : null}
                  </div>

                  <div className="space-y-1">
                    <StatusPill size="xs" tone={match.tone} className="w-full justify-center">
                      {match.label}
                    </StatusPill>
                    {colabAccounts.length > 0 && (
                      <div className="ch-num text-[10.5px] text-fg-muted text-center">
                        {availAccounts > 0
                          ? zh
                            ? `${availAccounts} 个 Colab 号支持`
                            : `${availAccounts} Colab acct`
                          : zh
                            ? '池内暂不可用'
                            : 'No eligible acct'}
                      </div>
                    )}
                  </div>
                </button>
              );
            })}
          </div>
        </div>

        {/* Account Assignment Selector (Strictly Colab-Only!) */}
        <InsetWell
          padding="sm"
          label={
            <span className="inline-flex items-center gap-1.5">
              <Users className="w-3.5 h-3.5" />
              {zh ? '执行账号分配（仅限 Google Colab 算力池）' : 'Account Assignment (Executable Colab Pool Only)'}
            </span>
          }
          headerRight={
            <span className="ch-num text-[11px] text-fg-muted">
              {zh ? `${colabAccounts.length} 个可执行账号` : `${colabAccounts.length} Colab accounts`}
            </span>
          }
          className="space-y-2"
        >
          <div role="radiogroup" aria-label={zh ? '账号分配方式' : 'Account selection'} className="space-y-2">
            {/* Option A: Auto Smart Routing */}
            <button
              type="button"
              role="radio"
              aria-checked={selectedAccountId === ''}
              disabled={launching}
              onClick={() => setSelectedAccountId('')}
              className={cn(
                'w-full rounded-xl border px-3 py-2.5 text-left transition-all cursor-pointer flex items-center justify-between gap-2',
                selectedAccountId === ''
                  ? 'bg-surface-2 border-[var(--accent)] shadow-[var(--shadow-control)]'
                  : 'bg-surface-2/50 border-line-faint hover:border-line-strong',
              )}
            >
              <div className="flex items-center gap-2.5 min-w-0">
                <span
                  className={cn(
                    'w-4 h-4 rounded-full border flex items-center justify-center shrink-0',
                    selectedAccountId === ''
                      ? 'border-[var(--accent)] bg-[var(--accent)] text-[var(--accent-fg)]'
                      : 'border-line-strong bg-surface-2',
                  )}
                >
                  {selectedAccountId === '' && <Check className="w-2.5 h-2.5" />}
                </span>
                <div className="min-w-0">
                  <div className="text-[12.5px] font-semibold text-fg">
                    {zh ? '🤖 自动智能分配（免费卡优先 / 省额度）' : '🤖 Auto Smart Routing (Free-First / Save CU)'}
                  </div>
                  <div className="text-[11px] text-fg-muted">
                    {zh
                      ? '自动选择最健康且满足所选显卡的 Colab 账号，避免额度过期浪费'
                      : 'Automatically picks the healthiest eligible Colab account'}
                  </div>
                </div>
              </div>
              <StatusPill size="xs" tone="emerald">
                {zh ? '推荐' : 'Auto'}
              </StatusPill>
            </button>

            {/* Option B: Explicit Colab Account Pick */}
            {colabAccounts.length > 0 && (
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 pt-0.5">
                {colabAccounts.map((acc) => {
                  const selected = selectedAccountId === acc.id;
                  const ent = acc.gpu_entitlements?.[selectedGpu];
                  const isRestricted =
                    !acc.enabled ||
                    acc.health === 'AUTH_EXPIRED' ||
                    ent?.status === 'TIER_RESTRICTED' ||
                    ent?.status === 'INELIGIBLE';
                  const title = acc.alias || displayEmail(acc.email);

                  return (
                    <button
                      key={acc.id}
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      disabled={launching || !acc.enabled}
                      onClick={() => setSelectedAccountId(acc.id)}
                      className={cn(
                        'rounded-xl border px-3 py-2 text-left transition-all cursor-pointer flex items-start justify-between gap-2',
                        selected
                          ? 'bg-surface-2 border-[var(--accent)] shadow-[var(--shadow-control)]'
                          : 'bg-surface-2/40 border-line-faint hover:border-line-strong',
                        !acc.enabled && 'opacity-50 cursor-not-allowed',
                      )}
                    >
                      <div className="min-w-0 space-y-0.5">
                        <div className="flex items-center gap-1.5 min-w-0">
                          <span
                            className="w-1.5 h-1.5 rounded-full shrink-0"
                            style={{
                              backgroundColor: isRestricted ? 'var(--amber)' : 'var(--emerald)',
                            }}
                          />
                          <span className="text-[12px] font-semibold text-fg truncate">{title}</span>
                        </div>
                        <div className="ch-num text-[11px] text-fg-muted truncate">
                          {formatColabAccountQuotaHint(acc, selectedGpu, zh)}
                        </div>
                      </div>
                      <StatusPill
                        size="xs"
                        tone={isRestricted ? 'amber' : 'neutral'}
                        className="shrink-0"
                      >
                        {acc.subscription_tier === 'AI_ULTRA'
                          ? 'Ultra'
                          : acc.subscription_tier === 'PRO_PLUS'
                            ? 'Pro+'
                            : acc.subscription_tier === 'PRO'
                              ? 'Pro'
                              : acc.subscription_tier === 'FREE'
                                ? 'Free'
                                : 'PAYG'}
                      </StatusPill>
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        </InsetWell>

        {/* Plugin Platform v2 Launch Mode Selector for media.v1 engines (WanGP, etc.) */}
        {Boolean(template.media_api_base) && (
          <div className="rounded-2xl border border-line-subtle bg-surface-1/60 px-3.5 py-3 space-y-2">
            <div className="flex items-center justify-between gap-2">
              <span className="text-[12.5px] font-semibold text-fg">
                {zh ? '界面运行模式（Plugin Platform v2）' : 'Studio UI Mode (Plugin Platform v2)'}
              </span>
              <StatusPill size="xs" tone="accent">
                {zh ? '支持社区 UI 替换' : 'Swappable UI'}
              </StatusPill>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2" role="radiogroup">
              <button
                type="button"
                role="radio"
                aria-checked={launchMode === 'headless'}
                disabled={launching}
                onClick={() => setLaunchMode('headless')}
                className={cn(
                  'rounded-xl border px-3 py-2 text-left transition-all cursor-pointer',
                  launchMode === 'headless'
                    ? 'border-[var(--accent)] bg-surface-2 shadow-[var(--shadow-control)]'
                    : 'border-line-faint bg-surface-1 hover:border-line-strong',
                )}
              >
                <div className="text-[12px] font-semibold text-fg flex items-center justify-between gap-1">
                  <span>{zh ? '⚡ 原生创作台（无头引擎）' : '⚡ Native Media Studio (Headless)'}</span>
                  <StatusPill size="xs" tone="emerald">{zh ? '推荐' : 'Rec'}</StatusPill>
                </div>
                <div className="text-[11px] text-fg-muted mt-0.5">
                  {zh
                    ? '0ms 本地秒开 + 实时去噪预览 + 本地永久画廊 + 可切换社区插件 UI'
                    : '0ms local UI + live preview + permanent local gallery + swappable UI plugins'}
                </div>
              </button>
              <button
                type="button"
                role="radio"
                aria-checked={launchMode === 'native_webui'}
                disabled={launching}
                onClick={() => setLaunchMode('native_webui')}
                className={cn(
                  'rounded-xl border px-3 py-2 text-left transition-all cursor-pointer',
                  launchMode === 'native_webui'
                    ? 'border-[var(--accent)] bg-surface-2 shadow-[var(--shadow-control)]'
                    : 'border-line-faint bg-surface-1 hover:border-line-strong',
                )}
              >
                <div className="text-[12px] font-semibold text-fg">
                  {zh ? '🌐 原版 Gradio 网页（专家模式）' : '🌐 Classic Gradio WebUI (Expert)'}
                </div>
                <div className="text-[11px] text-fg-muted mt-0.5">
                  {zh
                    ? '保留上游完整原生表单（Showrunner / Mask / 高级参数全开）'
                    : 'Full upstream Gradio web interface via Comm Tunnel v3'}
                </div>
              </button>
            </div>
          </div>
        )}

        {/* Safe GPU Waitlist & Auto-Fallback Protection (Anti-503/412 Shortage) */}
        <div className="rounded-2xl border border-line-subtle bg-surface-1/60 px-3.5 py-3 space-y-3">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0 space-y-0.5">
              <div className="flex flex-wrap items-center gap-1.5 text-[12.5px] font-semibold text-fg">
                <span>{zh ? '🎯 缺卡自动候补（安全抢卡模式）' : '🎯 Safe GPU Waitlist (Auto-Retry on Shortage)'}</span>
                <StatusPill size="xs" tone="emerald">
                  {zh ? '礼貌退避防风控' : 'Anti-Ban Backoff'}
                </StatusPill>
              </div>
              <div className="text-[11.5px] text-fg-muted leading-relaxed">
                {zh
                  ? '遇到 Google Colab 503/412 无卡或账号并发满时，按 15–45 秒安全间隔自动候补重试（绝不高频狂刷），抢到卡立即自动启动，随时可取消（0 消耗）。'
                  : 'On Colab 503/412 capacity shortage or full concurrency, retries politely every 15–45s with jitter and auto-boots as soon as a GPU opens (cancel anytime, 0 CU burned).'}
              </div>
            </div>
            <Switch
              checked={queueRetryEnabled}
              disabled={launching}
              onChange={(next) => setQueueRetryEnabled(next)}
              aria-label={zh ? '开启安全候补抢卡模式' : 'Enable safe GPU waitlist'}
            />
          </div>

          {queueRetryEnabled && (
            <div className="flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-line-faint">
              <span className="text-[11.5px] text-fg-secondary">
                {zh ? '最长候补等待时长' : 'Max waitlist duration'}
              </span>
              <div className="flex items-center gap-1.5" role="radiogroup">
                {QUEUE_WAIT_PRESETS_MIN.map((mins) => {
                  const active = queueMaxWaitMin === mins;
                  return (
                    <button
                      key={mins}
                      type="button"
                      role="radio"
                      aria-checked={active}
                      disabled={launching}
                      onClick={() => setQueueMaxWaitMin(mins)}
                      className={cn(
                        'px-2.5 py-0.5 rounded-full text-[11.5px] ch-num font-semibold border transition-all cursor-pointer',
                        active
                          ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
                          : 'bg-surface-2 text-fg-secondary border-line-subtle hover:border-line-strong',
                      )}
                    >
                      {zh ? `${mins} 分钟` : `${mins} min`}
                    </button>
                  );
                })}
              </div>
            </div>
          )}

          <div className="flex items-start justify-between gap-3 pt-2 border-t border-line-faint">
            <div className="min-w-0 space-y-0.5">
              <div className="text-[12.5px] font-semibold text-fg">
                {zh ? '🔀 缺卡自动顺延其他档位显卡' : '🔀 Auto-Fallback to Next Available GPU'}
              </div>
              <div className="text-[11.5px] text-fg-muted leading-relaxed">
                {zh
                  ? '若所选显卡（如 G4 / A100）全池无货，允许自动顺延尝试下一档可用卡（H100→G4→A100→L4→T4）。'
                  : 'If your chosen GPU is out of stock pool-wide, automatically try the next tier in the fallback chain.'}
              </div>
            </div>
            <Switch
              checked={autoFallback}
              disabled={launching}
              onChange={(next) => setAutoFallback(next)}
              aria-label={zh ? '允许缺卡自动顺延显卡' : 'Allow GPU auto-fallback'}
            />
          </div>
        </div>

        {/* Idle Auto-Stop Duration Pills (`30 / 45 / 60 分钟`) */}
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-2xl border border-line-subtle bg-surface-1/60 px-3.5 py-2.5">
          <div className="flex items-center gap-2 text-[12.5px] font-medium text-fg">
            <Clock className="w-4 h-4 text-fg-muted shrink-0" />
            <span>{zh ? '防忘关机时长（无人操作自动停机省额度）' : 'Idle Auto-Stop Protection'}</span>
          </div>
          <div className="flex items-center gap-1.5" role="radiogroup" aria-label={zh ? '防忘关机时长' : 'Idle auto-stop duration'}>
            {IDLE_SHUTDOWN_PRESETS.map((mins) => {
              const active = idleMinutes === mins;
              return (
                <button
                  key={mins}
                  type="button"
                  role="radio"
                  aria-checked={active}
                  disabled={launching}
                  onClick={() => setIdleMinutes(mins)}
                  className={cn(
                    'px-3 py-1 rounded-full text-[12px] ch-num font-semibold border transition-all cursor-pointer',
                    active
                      ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
                      : 'bg-surface-2 text-fg-secondary border-line-subtle hover:border-line-strong hover:text-fg',
                  )}
                >
                  {zh ? `${mins} 分钟` : `${mins} min`}
                </button>
              );
            })}
          </div>
        </div>
      </section>
    </SlideOver>
  );
}

export default AppLaunchDrawer;
