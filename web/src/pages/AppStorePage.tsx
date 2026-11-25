import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  ArrowRight,
  Check,
  ChevronDown,
  ChevronUp,
  Clock,
  Copy,
  Cpu,
  ExternalLink,
  FileJson,
  Film,
  FolderSync,
  HardDriveDownload,
  Layers,
  Package,
  Play,
  Puzzle,
  RefreshCw,
  RotateCcw,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Square,
  Terminal,
  Trash2,
  Upload,
  Workflow,
  Zap,
} from 'lucide-react';
import type {
  Account,
  AppCategory,
  AppInstance,
  AppLaunchRequest,
  AppPluginManifest,
  AppTemplate,
  CheckpointPolicy,
  CheckpointRecord,
  QuotaSummary,
  WSEvent,
} from '../types';
import { formatBytes } from '../lib/api';
import { formatFriendlyAppError } from '../lib/friendlyErrors';
import { pluginSdk } from '../lib/pluginSdk';
import { isDemoSiteMode } from '../lib/demoSiteAdapter';
import { V11_DEMO_EVENT, WS_V11_EVENT, sanitizeAccountLabel, v11Api } from '../lib/apiV11';
import { useI18n } from '../i18n';
import { navigate as fallbackNavigate } from '../components/layout/navigation';
import {
  AppLaunchDrawer,
  IDLE_SHUTDOWN_PRESETS,
  getGpuOptionMeta,
} from '../components/ui/AppLaunchDrawer';
import {
  ActionButton,
  DialogModal,
  InsetWell,
  LayeredCard,
  PageHeader,
  SegmentedQuotaBar,
  StatusPill,
  Switch,
  cn,
} from '../components/ui/primitives';

export interface AppStorePageProps {
  accounts?: Account[];
  summary?: QuotaSummary;
  deploymentMode?: 'local' | 'cloud';
  onNavigate?: (to: string) => void;
}

type CategoryFilter = 'all' | 'plugins' | AppCategory;

const SAMPLE_PLUGIN_MANIFESTS: Array<{
  key: string;
  labelZh: string;
  labelEn: string;
  manifest: AppPluginManifest;
}> = [
  {
    key: 'fooocus-lite',
    labelZh: '🎨 Fooocus 极简出图插件',
    labelEn: '🎨 Fooocus Simple Paint',
    manifest: {
      id: 'fooocus-studio',
      name: 'Fooocus Instant Photo Studio',
      name_zh: 'Fooocus 零参数极简画图工作台',
      tagline: 'Midjourney-like effortless prompting on free T4 GPUs',
      tagline_zh: '像 Midjourney 一样只输一句话就能出大片，免调复杂节点',
      description:
        'Community plugin running Fooocus WebUI on cloud VRAM with automatic local port bridge.',
      description_zh:
        '第三方社区插件：免配节点和采样器，适合零基础直接出海报与写实人像。',
      category: 'image',
      icon: '🖼️',
      repo_url: 'https://github.com/lllyasviel/Fooocus',
      start_command: 'python entry_with_update.py --listen 127.0.0.1 --port 7865',
      remote_port: 7865,
      local_port: 7865,
      min_vram_gb: 8,
      recommended_gpu: 'T4',
      free_tier_compatible: true,
      est_boot_sec: 50,
      highlights: ['Zero-Config Prompting', 'SDXL Realism', 'Free T4 Ready'],
      highlights_zh: ['免调参数秒出图', 'SDXL 写实人像', '免费 T4 显卡可跑'],
      supports_port_bridge: true,
      author: 'community-dev',
      version: '1.0.0',
    },
  },
  {
    key: 'liveportrait',
    labelZh: '🎬 LivePortrait 照片开口插件',
    labelEn: '🎬 LivePortrait Animator',
    manifest: {
      id: 'liveportrait-studio',
      name: 'LivePortrait AI Avatar Animator',
      name_zh: 'LivePortrait 人像表情驱动工作台',
      tagline: 'Animate still portraits with natural expressions and head motion',
      tagline_zh: '上传一张静态人像照片 + 驱动视频，一键生成自然生动的数字人表情',
      description:
        'Community plugin packaging LivePortrait Gradio studio for one-click cloud GPU animation.',
      description_zh:
        '通过 colabhive-app.json 接入的社区插件，在免费云端显卡上运行 LivePortrait Gradio 界面。',
      category: 'video',
      icon: '🎭',
      repo_url: 'https://github.com/KwaiVGI/LivePortrait',
      start_command: 'python app.py --server_name 127.0.0.1 --server_port 8890',
      remote_port: 8890,
      local_port: 8890,
      min_vram_gb: 10,
      recommended_gpu: 'T4',
      free_tier_compatible: true,
      est_boot_sec: 48,
      highlights: ['Portrait Retargeting', 'Gradio WebUI', 'Private Loopback'],
      highlights_zh: ['静态照秒变动态表情', 'Gradio 可视化界面', '本机私有直连'],
      supports_port_bridge: true,
      author: 'community-dev',
      version: '1.0.0',
    },
  },
];

/**
 * Extracts a verified localhost bridge host:port strictly ONLY when the local
 * bridge is active and a real loopback URL is reported.
 */
function extractVerifiedLocalHostPort(inst: AppInstance): string | null {
  if (!inst.local_bridge_active || !inst.local_url) {
    return null;
  }
  const cleaned = inst.local_url
    .replace(/^https?:\/\//i, '')
    .replace(/[/?#].*$/, '')
    .replace(/\/+$/, '');
  return cleaned || null;
}

function formatTimeAgo(iso: string | undefined, zh: boolean): string {
  if (!iso) return zh ? '刚刚' : 'just now';
  const diffSec = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 1000));
  if (Number.isNaN(diffSec) || diffSec < 60) return zh ? '刚刚' : 'just now';
  const mins = Math.floor(diffSec / 60);
  if (mins < 60) return zh ? `${mins} 分钟前` : `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  return zh ? `${hrs} 小时前` : `${hrs}h ago`;
}

function isAbortError(err: unknown): boolean {
  return Boolean(err && typeof err === 'object' && 'name' in err && (err as { name?: unknown }).name === 'AbortError');
}

// ============================================================================
// Interactive Demo Studio Preview Modal (strictly for `isDemoSiteMode()`)
// ============================================================================

function DemoStudioPreviewModal({
  instance,
  onClose,
  zh,
}: {
  instance: AppInstance | null;
  onClose: () => void;
  zh: boolean;
}) {
  const [prompt, setPrompt] = useState(
    '柔和晨光下的极简玻璃咖啡馆，胶片颗粒质感，Flux.1 Schnell 超清商业海报',
  );
  const [generating, setGenerating] = useState(false);
  const [generatedCount, setGeneratedCount] = useState(1);
  const demoTimerRef = useRef<number | null>(null);

  useEffect(() => {
    return () => {
      if (demoTimerRef.current !== null) window.clearTimeout(demoTimerRef.current);
    };
  }, []);

  if (!instance) return null;

  const hostPort = extractVerifiedLocalHostPort(instance);

  const handleRunDemoSample = () => {
    setGenerating(true);
    if (demoTimerRef.current !== null) window.clearTimeout(demoTimerRef.current);
    demoTimerRef.current = window.setTimeout(() => {
      setGenerating(false);
      setGeneratedCount((c) => c + 1);
    }, 900);
  };

  return (
    <DialogModal
      open={Boolean(instance)}
      onClose={onClose}
      size="xl"
      icon={<Sparkles />}
      title={zh ? `${instance.name_zh} · 实时工作台预览` : `${instance.name} · Live Studio`}
      subtitle={
        hostPort
          ? zh
            ? `本机安全直连地址 http://${hostPort}（在桌面或本地运行 ColabHive 时将直接打开真实 WebUI）`
            : `Bridged via private local address http://${hostPort}`
          : zh
            ? '交互演示预览模式'
            : 'Interactive Demo Preview'
      }
      footer={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-[12px] text-fg-muted">
            {zh
              ? '🛡️ 输出目录正自动定时同步回本机，防止免费卡到点丢图'
              : '🛡️ Output folder auto-synced locally to protect against free-tier resets'}
          </span>
          <div className="flex items-center gap-2">
            {hostPort && instance.local_url && (
              <ActionButton
                size="sm"
                variant="secondary"
                icon={<ExternalLink />}
                onClick={() => {
                  window.open(instance.local_url, '_blank', 'noopener,noreferrer');
                }}
              >
                {zh ? `新窗口打开 ${hostPort}` : `Open ${hostPort}`}
              </ActionButton>
            )}
            <ActionButton size="sm" variant="primary" onClick={onClose}>
              {zh ? '完成返回' : 'Done'}
            </ActionButton>
          </div>
        </div>
      }
    >
      <div className="space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2 px-3.5 py-2.5 rounded-xl bg-surface-1 border border-line-faint text-[12.5px]">
          <span className="flex items-center gap-2 font-medium text-fg">
            <span className="ui-dot ui-dot-pulse" style={{ backgroundColor: 'var(--emerald)' }} />
            {zh
              ? `运行于 ${instance.assigned_gpu ?? 'GPU'} · ${sanitizeAccountLabel(instance.account_label, zh)} · 模型：${instance.selected_model_name || '空白环境'}`
              : `Running on ${instance.assigned_gpu ?? 'GPU'} · ${sanitizeAccountLabel(instance.account_label, zh)}`}
          </span>
          {hostPort && (
            <StatusPill size="xs" tone="emerald" mono>
              {`http://${hostPort}`}
            </StatusPill>
          )}
        </div>

        <InsetWell
          padding="md"
          label={zh ? '✨ 在线交互演示台（零真实额度消耗）' : '✨ Interactive Studio Sandbox'}
        >
          <div className="space-y-3">
            <textarea
              rows={2}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              className="w-full rounded-xl border border-line bg-surface-2 px-3 py-2 text-[13px] text-fg outline-none"
            />
            <div className="flex items-center justify-between gap-2">
              <span className="text-[12px] text-fg-muted">
                {zh ? '演示沙盒模式（零真实额度消耗）' : 'Demo sandbox (0 quota burn)'}
              </span>
              <ActionButton
                size="sm"
                variant="primary"
                loading={generating}
                icon={<Zap />}
                onClick={handleRunDemoSample}
              >
                {zh ? '立即体验生成' : 'Run Sample'}
              </ActionButton>
            </div>
          </div>
        </InsetWell>

        <div
          className="rounded-2xl p-6 border border-line text-center space-y-2"
          style={{
            background:
              'radial-gradient(circle at 50% 20%, rgba(245,158,11,0.14), rgba(16,185,129,0.08))',
          }}
        >
          <div className="text-[28px]">
            {instance.category === 'workspace' ? '📓' : '🎨'}
          </div>
          <div className="text-[14px] font-semibold text-fg">
            {zh
              ? `已完成第 #${generatedCount} 批演示渲染任务`
              : `Completed demo render #${generatedCount}`}
          </div>
          <p className="text-[12.5px] text-fg-secondary max-w-md mx-auto">{prompt}</p>
        </div>
      </div>
    </DialogModal>
  );
}

// ============================================================================
// Plugin Manifest Import Modal (`colabhive-app.json` -> `POST /api/v1/apps/plugins`)
// ============================================================================

function InstallPluginModal({
  open,
  onClose,
  onInstalled,
  zh,
}: {
  open: boolean;
  onClose: () => void;
  onInstalled: (tpl: AppTemplate) => void;
  zh: boolean;
}) {
  const [manifestText, setManifestText] = useState<string>('');
  const [dragging, setDragging] = useState<boolean>(false);
  const [installing, setInstalling] = useState<boolean>(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (!open) return;
    setErrorMsg(null);
  }, [open]);

  const parsedPreview = useMemo<{
    ok: boolean;
    manifest?: Partial<AppPluginManifest>;
    error?: string;
  }>(() => {
    const trimmed = manifestText.trim();
    if (!trimmed) return { ok: false };
    try {
      const parsed: unknown = JSON.parse(trimmed);
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        return {
          ok: false,
          error: zh ? '根节点必须是 JSON 对象' : 'Root must be a JSON object',
        };
      }
      const m = parsed as Partial<AppPluginManifest>;
      if (!m.id || !/^[a-z0-9][a-z0-9-]{1,48}$/.test(m.id)) {
        return {
          ok: false,
          error: zh
            ? '缺少有效 id（仅限小写字母、数字和连字符，2–49 位）'
            : 'Invalid or missing id (lowercase letters, digits, hyphens, 2–49 chars)',
        };
      }
      if (
        !m.remote_port ||
        !Number.isInteger(Number(m.remote_port)) ||
        Number(m.remote_port) < 1024 ||
        Number(m.remote_port) > 65535
      ) {
        return {
          ok: false,
          error: zh ? 'remote_port 需为 1024–65535 整数' : 'remote_port must be 1024–65535',
        };
      }
      return { ok: true, manifest: m };
    } catch {
      return {
        ok: false,
        error: zh ? 'JSON 语法格式有误，请检查引号或逗号' : 'Invalid JSON syntax',
      };
    }
  }, [manifestText, zh]);

  const handleLoadFile = async (file: File | undefined) => {
    if (!file) return;
    try {
      const content = await file.text();
      setManifestText(content);
      setErrorMsg(null);
    } catch {
      setErrorMsg(zh ? '读取插件文件失败，请直接粘贴 JSON 内容' : 'Could not read plugin JSON file');
    }
  };

  const handleSubmit = async () => {
    if (!manifestText.trim()) return;
    setInstalling(true);
    setErrorMsg(null);
    try {
      const installed = await v11Api.installAppPlugin(manifestText.trim());
      onInstalled(installed);
      setManifestText('');
      onClose();
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : String(err));
    } finally {
      setInstalling(false);
    }
  };

  return (
    <DialogModal
      open={open}
      onClose={onClose}
      size="xl"
      icon={<Puzzle />}
      title={zh ? '安装第三方插件 / 开发者接入' : 'Install Third-Party Plugin / Developer SDK'}
      subtitle={
        zh
          ? '拖入或粘贴一份声明式 colabhive-app.json 清单即可把任意开源 AI 应用发布进商店（POST /api/v1/apps/plugins）'
          : 'Drop or paste a declarative colabhive-app.json manifest to add any open-source AI studio.'
      }
      footer={
        <div className="flex flex-wrap items-center justify-between gap-2 w-full">
          <span className="text-[11.5px] text-fg-muted">
            {zh
              ? '安全校验：仓库链接与启动命令均受严格白名单保护，防命令注入'
              : 'Validated with strict GitHub URL & start command allowlists'}
          </span>
          <div className="flex items-center gap-2">
            <ActionButton size="sm" variant="ghost" onClick={onClose}>
              {zh ? '取消' : 'Cancel'}
            </ActionButton>
            <ActionButton
              size="sm"
              variant="primary"
              loading={installing}
              disabled={!parsedPreview.ok}
              icon={<Puzzle />}
              onClick={() => void handleSubmit()}
            >
              {zh ? '验证并安装插件' : 'Validate & Install Plugin'}
            </ActionButton>
          </div>
        </div>
      }
    >
      <div className="space-y-4">
        <InsetWell
          padding="sm"
          label={zh ? '✨ 一键填入示例插件清单 (colabhive-app.json)' : '✨ 1-Click Sample Plugin Manifests'}
          headerRight={
            <StatusPill size="xs" tone="emerald" mono>
              ~/.colabhive/plugins/*.json
            </StatusPill>
          }
        >
          <div className="flex flex-wrap items-center gap-2">
            {SAMPLE_PLUGIN_MANIFESTS.map((sample) => (
              <button
                key={sample.key}
                type="button"
                onClick={() => {
                  setManifestText(JSON.stringify(sample.manifest, null, 2));
                  setErrorMsg(null);
                }}
                className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl border border-line bg-surface-2 hover:border-line-strong text-[12px] font-semibold text-fg transition-colors cursor-pointer"
              >
                <span>{zh ? sample.labelZh : sample.labelEn}</span>
              </button>
            ))}
          </div>
        </InsetWell>

        <div
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => {
            e.preventDefault();
            setDragging(false);
            const f = e.dataTransfer.files?.[0];
            void handleLoadFile(f);
          }}
          className={cn(
            'rounded-2xl border-2 border-dashed p-3.5 transition-colors space-y-3',
            dragging
              ? 'border-[var(--emerald)] bg-[var(--emerald-soft)]'
              : 'border-line-strong bg-surface-1/75',
          )}
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2 text-[12.5px] font-semibold text-fg">
              <FileJson className="w-4 h-4 shrink-0" style={{ color: 'var(--amber)' }} />
              <span>
                {zh
                  ? '拖入 colabhive-app.json 文件，或在下方粘贴清单内容'
                  : 'Drop colabhive-app.json here or paste manifest JSON below'}
              </span>
            </div>

            <div className="flex items-center gap-2">
              <input
                ref={fileInputRef}
                type="file"
                accept=".json,application/json"
                className="hidden"
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  void handleLoadFile(f);
                  e.target.value = '';
                }}
              />
              <ActionButton
                size="xs"
                variant="secondary"
                icon={<Upload />}
                onClick={() => fileInputRef.current?.click()}
              >
                {zh ? '选择 .json 文件' : 'Choose .json File'}
              </ActionButton>
            </div>
          </div>

          <textarea
            rows={10}
            value={manifestText}
            onChange={(e) => {
              setManifestText(e.target.value);
              setErrorMsg(null);
            }}
            spellCheck={false}
            placeholder={
              zh
                ? '{\n  "id": "my-ai-studio",\n  "name": "My AI Studio",\n  "name_zh": "我的开源 AI 应用",\n  "category": "image",\n  "repo_url": "https://github.com/owner/repo",\n  "start_command": "python app.py --port 7860",\n  "remote_port": 7860\n}'
                : '{\n  "id": "my-ai-studio",\n  "name": "My AI Studio",\n  "name_zh": "My AI Studio",\n  "category": "image",\n  "repo_url": "https://github.com/owner/repo",\n  "start_command": "python app.py --port 7860",\n  "remote_port": 7860\n}'
            }
            className="ui-input ch-num w-full py-2.5 text-[12px] leading-relaxed resize-y"
          />
        </div>

        {parsedPreview.ok && parsedPreview.manifest && (
          <div
            className="rounded-xl border px-3.5 py-2.5 flex flex-wrap items-center justify-between gap-2 text-[12px]"
            style={{
              backgroundColor: 'var(--emerald-soft)',
              borderColor: 'var(--emerald-border)',
              color: 'var(--emerald-text)',
            }}
          >
            <span className="flex items-center gap-2 font-semibold">
              <Check className="w-4 h-4 shrink-0" />
              <span>
                {parsedPreview.manifest.icon || '🧩'}{' '}
                {zh
                  ? parsedPreview.manifest.name_zh || parsedPreview.manifest.name
                  : parsedPreview.manifest.name || parsedPreview.manifest.name_zh}
              </span>
            </span>
            <span className="ch-num text-[11.5px]">
              id: {parsedPreview.manifest.id} · {parsedPreview.manifest.category} · port{' '}
              {parsedPreview.manifest.remote_port}
            </span>
          </div>
        )}

        {!parsedPreview.ok && parsedPreview.error && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[12px]"
            style={{
              backgroundColor: 'var(--amber-soft)',
              borderColor: 'var(--amber-border)',
              color: 'var(--amber-text)',
            }}
          >
            <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
            <span>{parsedPreview.error}</span>
          </div>
        )}

        {errorMsg && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[12.5px]"
            style={{
              backgroundColor: 'var(--coral-soft)',
              borderColor: 'var(--coral-border)',
              color: 'var(--coral-text)',
            }}
          >
            <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
            <span>{errorMsg}</span>
          </div>
        )}
      </div>
    </DialogModal>
  );
}

// ============================================================================
// Main `AppStorePage` Component (`/apps`)
// ============================================================================

export function AppStorePage({
  accounts = [],
  summary: _summary,
  deploymentMode = 'local',
  onNavigate,
}: AppStorePageProps = {}) {
  const { locale } = useI18n();
  const zh = locale === 'zh-CN';
  const nav = onNavigate ?? fallbackNavigate;

  const [templates, setTemplates] = useState<AppTemplate[]>([]);
  const [instances, setInstances] = useState<AppInstance[]>([]);
  const [category, setCategory] = useState<CategoryFilter>('all');

  // Running instance inline idle auto-shutdown minutes
  const [idleMinutesByInst, setIdleMinutesByInst] = useState<Record<string, number>>({});
  const [idleSavingId, setIdleSavingId] = useState<string | null>(null);

  // Unified 3-Step Launch Drawer & Third-Party Plugin Modal
  const [drawerTemplate, setDrawerTemplate] = useState<AppTemplate | null>(null);
  const [pluginModalOpen, setPluginModalOpen] = useState(false);

  // Action busy states & feedback
  const [launchingId, setLaunchingId] = useState<string | null>(null);
  const [deletingPluginId, setDeletingPluginId] = useState<string | null>(null);
  const [instanceBusyId, setInstanceBusyId] = useState<string | null>(null);
  const [stopErrorByInstId, setStopErrorByInstId] = useState<Record<string, string>>({});
  const [logPanelPrefByInstId, setLogPanelPrefByInstId] = useState<Record<string, boolean>>({});
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const [drawerLaunchError, setDrawerLaunchError] = useState<string | null>(null);
  const [lastFailedLaunchReq, setLastFailedLaunchReq] = useState<AppLaunchRequest | null>(null);
  const [actionBanner, setActionBanner] = useState<{
    tone: 'ok' | 'err';
    text: string;
  } | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [previewInstance, setPreviewInstance] = useState<AppInstance | null>(null);

  // Compact Collapsible Checkpoint Relay state (collapsed by default!)
  const [policy, setPolicy] = useState<CheckpointPolicy | null>(null);
  const [checkpoints, setCheckpoints] = useState<CheckpointRecord[]>([]);
  const [checkpointsOpen, setCheckpointsOpen] = useState<boolean>(false);
  const [policyBusy, setPolicyBusy] = useState(false);
  const [syncingNow, setSyncingNow] = useState(false);
  const [resumingId, setResumingId] = useState<string | null>(null);

  const mountedRef = useRef(true);
  const requestSeqRef = useRef(0);
  const activeRefreshAbortRef = useRef<AbortController | null>(null);
  const wsRefreshTimerRef = useRef<number | null>(null);
  const toastTimerRef = useRef<number | null>(null);
  const copyTimerRef = useRef<number | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      activeRefreshAbortRef.current?.abort();
      if (wsRefreshTimerRef.current !== null) window.clearTimeout(wsRefreshTimerRef.current);
      if (toastTimerRef.current !== null) window.clearTimeout(toastTimerRef.current);
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
    };
  }, []);

  const showToast = useCallback((text: string, tone: 'ok' | 'err' = 'ok') => {
    if (!mountedRef.current) return;
    setActionBanner({ tone, text });
    if (toastTimerRef.current !== null) window.clearTimeout(toastTimerRef.current);
    // Keep error banners open until dismissed so users can read suggestions & trigger waitlist retry.
    if (tone === 'ok') {
      toastTimerRef.current = window.setTimeout(() => {
        if (mountedRef.current) {
          setActionBanner((curr) => (curr?.text === text ? null : curr));
        }
      }, 4500);
    }
  }, []);

  // Fetch static app templates ONCE on mount (and explicitly after plugin install/delete)
  const fetchTemplates = useCallback(async () => {
    try {
      const tpls = await v11Api.getAppTemplates();
      if (!mountedRef.current) return;
      setTemplates(tpls);
    } catch (err) {
      if (!mountedRef.current || isAbortError(err)) return;
      setLoadError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  // Dynamic operational refresh: cancels any superseded in-flight request via AbortController
  const refreshDynamic = useCallback(async () => {
    if (wsRefreshTimerRef.current !== null) {
      window.clearTimeout(wsRefreshTimerRef.current);
      wsRefreshTimerRef.current = null;
    }
    activeRefreshAbortRef.current?.abort();
    const ctrl = new AbortController();
    activeRefreshAbortRef.current = ctrl;
    const seq = ++requestSeqRef.current;

    try {
      const [insts, pol, ckpts] = await Promise.all([
        v11Api.getAppInstances(ctrl.signal),
        v11Api.getCheckpointPolicy(ctrl.signal),
        v11Api.listCheckpoints(ctrl.signal),
      ]);
      if (!mountedRef.current || ctrl.signal.aborted || seq !== requestSeqRef.current) return;
      setInstances(insts);
      setPolicy(pol);
      setCheckpoints(ckpts);
      setLoadError(null);
    } catch (err) {
      if (!mountedRef.current || ctrl.signal.aborted || isAbortError(err) || seq !== requestSeqRef.current) {
        return;
      }
      setLoadError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  // Trailing 250ms coalescer for WS event bursts so rapid events collapse into 1 clean refresh
  const scheduleDynamicRefresh = useCallback(() => {
    if (wsRefreshTimerRef.current !== null) {
      window.clearTimeout(wsRefreshTimerRef.current);
    }
    wsRefreshTimerRef.current = window.setTimeout(() => {
      wsRefreshTimerRef.current = null;
      void refreshDynamic();
    }, 250);
  }, [refreshDynamic]);

  useEffect(() => {
    void fetchTemplates();
    void refreshDynamic();
  }, [fetchTemplates, refreshDynamic]);

  // Subscribe to SINGLE root WebSocket custom events (`colabhive:ws-v11`) & demo updates
  useEffect(() => {
    const onWsV11 = (e: Event) => {
      const evt = (e as CustomEvent<WSEvent>).detail;
      if (evt?.type === 'app_instance_updated' || evt?.type === 'checkpoint_relay') {
        scheduleDynamicRefresh();
      }
    };
    const onLocalChange = () => {
      void fetchTemplates();
      scheduleDynamicRefresh();
    };
    window.addEventListener(WS_V11_EVENT, onWsV11);
    window.addEventListener(V11_DEMO_EVENT, onLocalChange);
    return () => {
      window.removeEventListener(WS_V11_EVENT, onWsV11);
      window.removeEventListener(V11_DEMO_EVENT, onLocalChange);
    };
  }, [fetchTemplates, scheduleDynamicRefresh]);

  const activeInstances = useMemo(
    () => instances.filter((i) => i.state !== 'stopped'),
    [instances],
  );

  const hasStartingInstance = useMemo(
    () =>
      Boolean(launchingId) ||
      instances.some((i) => i.state === 'starting' || i.state === 'failover'),
    [launchingId, instances],
  );

  // Fast 1.5s poll while any app is allocating/installing so live progress % and deploy logs stream smoothly.
  useEffect(() => {
    if (!hasStartingInstance) return;
    const pollId = window.setInterval(() => {
      void v11Api
        .getAppInstances()
        .then((next) => {
          if (mountedRef.current) setInstances(next);
        })
        .catch(() => undefined);
    }, 1500);
    return () => window.clearInterval(pollId);
  }, [hasStartingInstance]);

  const drawerLaunchingInstance = useMemo(() => {
    if (!drawerTemplate) return null;
    return (
      instances.find(
        (i) => i.template_id === drawerTemplate.id && (i.state === 'starting' || i.state === 'failover'),
      ) ?? null
    );
  }, [drawerTemplate, instances]);

  // At least one running app instance with a real session_id is required for manual snapshot
  const runningSnapshotTarget = useMemo(
    () =>
      activeInstances.find(
        (i) => (i.state === 'running' || i.state === 'failover') && Boolean(i.session_id),
      ) ?? null,
    [activeInstances],
  );

  const builtinChatTemplate = useMemo(
    () => templates.find((t) => t.id === 'llm-chat') ?? null,
    [templates],
  );

  const launchableTemplates = useMemo(
    () => templates.filter((t) => t.id !== 'llm-chat'),
    [templates],
  );

  const pluginTemplatesCount = useMemo(
    () => launchableTemplates.filter((t) => Boolean(t.is_plugin)).length,
    [launchableTemplates],
  );

  // Show only relevant category filters (zero dead empty tabs, clean typography without emojis)
  const categoryFilterPills = useMemo(() => {
    const pills: Array<{ id: CategoryFilter; labelZh: string; labelEn: string }> = [
      { id: 'all', labelZh: '全部应用', labelEn: 'All Apps' },
      { id: 'video', labelZh: '视频与生图 (WanGP)', labelEn: 'Video & Image (WanGP)' },
      { id: 'image', labelZh: '节点工坊 (ComfyUI)', labelEn: 'Node Studio (ComfyUI)' },
      { id: 'workspace', labelZh: '大模型部署与微调', labelEn: 'LLM & Fine-Tuning' },
    ];
    if (launchableTemplates.some((t) => t.category === 'audio')) {
      pills.push({ id: 'audio', labelZh: '语音插件', labelEn: 'Audio Plugins' });
    }
    if (pluginTemplatesCount > 0) {
      pills.push({
        id: 'plugins',
        labelZh: `第三方插件 (${pluginTemplatesCount})`,
        labelEn: `Plugins (${pluginTemplatesCount})`,
      });
    }
    return pills;
  }, [launchableTemplates, pluginTemplatesCount]);

  const filteredTemplates = useMemo(() => {
    if (category === 'all') return launchableTemplates;
    if (category === 'plugins') return launchableTemplates.filter((t) => Boolean(t.is_plugin));
    return launchableTemplates.filter((t) => t.category === category);
  }, [launchableTemplates, category]);

  const showFlagshipChatCard = category === 'all' || category === 'workspace';
  const isCloudDeployment = deploymentMode === 'cloud' && !isDemoSiteMode();

  const copyText = useCallback(async (key: string, val: string) => {
    try {
      await navigator.clipboard.writeText(val);
      if (!mountedRef.current) return;
      setCopiedKey(key);
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
      copyTimerRef.current = window.setTimeout(() => {
        if (mountedRef.current) setCopiedKey(null);
      }, 1800);
    } catch {
      // ignore clipboard denial
    }
  }, []);

  const handleOpenAppUI = useCallback(
    async (inst: AppInstance) => {
      if (isDemoSiteMode()) {
        setPreviewInstance(inst);
        return;
      }
      // Plugin Platform v2: headless media.v1 engines open in 0ms local Native Studio
      // (or the user's selected third-party community UI plugin!).
      if (inst.media_api_base && inst.launch_mode !== 'native_webui') {
        try {
          const caps = await pluginSdk.getCapabilities();
          const prefId = caps.ui_preferences?.['media.v1'] || 'builtin:media-studio';
          const prefPlugin = caps.ui_plugins.find((u) => u.id === prefId);
          if (prefPlugin && prefPlugin.mode === 'local_bundle') {
            const sess = await pluginSdk.openUIPlugin(prefPlugin.id);
            window.open(sess.url, '_blank', 'noopener,noreferrer');
            return;
          }
        } catch {
          // Fall back cleanly to built-in Native Media Studio route.
        }
        onNavigate?.(inst.studio_route || '/studio/media');
        return;
      }
      if (!inst.local_bridge_active || !inst.local_url) {
        showToast(
          zh
            ? '云端模式暂不支持本地端口桥接，请使用桌面/本机模式或配置独立隔离代理域名'
            : 'Cloud mode requires an isolated-origin app proxy; local port bridging is unavailable.',
          'err',
        );
        return;
      }
      window.open(inst.local_url, '_blank', 'noopener,noreferrer');
    },
    [onNavigate, showToast, zh],
  );

  const handleConfirmLaunchFromDrawer = useCallback(
    async (req: AppLaunchRequest) => {
      if (isCloudDeployment) {
        showToast(
          zh
            ? '云端模式暂不支持本地端口桥接，请使用桌面/本机模式或配置独立隔离代理域名'
            : 'Cloud mode does not support local port bridging.',
          'err',
        );
        return;
      }

      setLaunchingId(req.template_id);
      setDrawerLaunchError(null);
      setActionBanner(null);
      try {
        const launched = await v11Api.launchApp(req);
        if (!mountedRef.current) return;
        setLastFailedLaunchReq(null);
        setDrawerLaunchError(null);
        setDrawerTemplate(null);
        await refreshDynamic();
        const verifiedAddr = extractVerifiedLocalHostPort(launched);
        const gpuLabel = launched.assigned_gpu ?? req.preferred_gpu ?? 'GPU';
        showToast(
          verifiedAddr
            ? zh
              ? `🎉 「${launched.name_zh}」已在 ${gpuLabel} 启动！本机直连 ${verifiedAddr}`
              : `🎉 "${launched.name}" is live on ${gpuLabel} (${verifiedAddr})`
            : zh
              ? `🎉 「${launched.name_zh}」已在云端 ${gpuLabel} 启动`
              : `🎉 "${launched.name}" launched on ${gpuLabel}`,
          'ok',
        );
      } catch (err) {
        const rawErr = err instanceof Error ? err.message : String(err);
        if (mountedRef.current) {
          setLastFailedLaunchReq(req);
          setDrawerLaunchError(rawErr);
        }
        showToast(rawErr, 'err');
      } finally {
        if (mountedRef.current) setLaunchingId(null);
      }
    },
    [isCloudDeployment, refreshDynamic, showToast, zh],
  );

  const handleUpdateInstanceIdleTimeout = useCallback(
    async (inst: AppInstance, nextMins: number) => {
      const prevMins = idleMinutesByInst[inst.id] ?? inst.idle_auto_stop_min ?? 45;
      setIdleMinutesByInst((prev) => ({ ...prev, [inst.id]: nextMins }));
      setIdleSavingId(inst.id);
      try {
        const updated = await v11Api.updateAppIdleTimeout(inst.id, nextMins);
        if (!mountedRef.current) return;
        setInstances((prev) => prev.map((item) => (item.id === updated.id ? updated : item)));
        setIdleMinutesByInst((prev) => ({ ...prev, [inst.id]: updated.idle_auto_stop_min }));
        showToast(
          zh
            ? `已保存：${updated.idle_auto_stop_min} 分钟无操作自动关机省额度`
            : `Saved idle auto-stop: ${updated.idle_auto_stop_min} min`,
          'ok',
        );
      } catch (err) {
        if (!mountedRef.current) return;
        setIdleMinutesByInst((prev) => ({ ...prev, [inst.id]: prevMins }));
        showToast(
          err instanceof Error
            ? err.message
            : zh
              ? '保存自动关机时间失败，已还原原设置'
              : 'Failed to persist idle auto-stop timeout; reverted.',
          'err',
        );
      } finally {
        if (mountedRef.current) setIdleSavingId(null);
      }
    },
    [idleMinutesByInst, showToast, zh],
  );

  const handleDeletePlugin = useCallback(
    async (tpl: AppTemplate) => {
      setDeletingPluginId(tpl.id);
      try {
        await v11Api.deleteAppPlugin(tpl.id);
        await fetchTemplates();
        showToast(
          zh
            ? `已卸载第三方插件「${tpl.name_zh || tpl.name}」`
            : `Uninstalled plugin "${tpl.name}"`,
          'ok',
        );
      } catch (err) {
        showToast(err instanceof Error ? err.message : String(err), 'err');
      } finally {
        if (mountedRef.current) setDeletingPluginId(null);
      }
    },
    [fetchTemplates, showToast, zh],
  );

  const handleStopInstance = useCallback(
    async (inst: AppInstance, force = false) => {
      setInstanceBusyId(inst.id);
      setStopErrorByInstId((prev) => {
        if (!(inst.id in prev)) return prev;
        const next = { ...prev };
        delete next[inst.id];
        return next;
      });
      try {
        await v11Api.stopApp(inst.id, force);
        await refreshDynamic();
        showToast(
          force
            ? zh
              ? `已强制停止「${inst.name_zh}」并立即释放云端显卡`
              : `Force-stopped "${inst.name}" and released cloud GPU`
            : zh
              ? `已安全停止「${inst.name_zh}」并释放云端显卡`
              : `Stopped "${inst.name}" and released cloud GPU`,
          'ok',
        );
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        if (mountedRef.current) {
          setStopErrorByInstId((prev) => ({ ...prev, [inst.id]: msg }));
        }
        showToast(
          zh
            ? `停止前备份或停机失败（已保留实例防丢数据）：${msg}`
            : `Pre-stop backup or teardown failed (VM preserved): ${msg}`,
          'err',
        );
      } finally {
        if (mountedRef.current) setInstanceBusyId(null);
      }
    },
    [refreshDynamic, showToast, zh],
  );

  const handleRestartInstance = useCallback(
    async (inst: AppInstance) => {
      setInstanceBusyId(inst.id);
      try {
        await v11Api.restartApp(inst.id);
        await refreshDynamic();
        showToast(zh ? `「${inst.name_zh}」已重新就绪` : `Restarted "${inst.name}"`, 'ok');
      } catch (err) {
        showToast(err instanceof Error ? err.message : String(err), 'err');
      } finally {
        if (mountedRef.current) setInstanceBusyId(null);
      }
    },
    [refreshDynamic, showToast, zh],
  );

  const handleToggleAutoResume = useCallback(
    async (enabled: boolean) => {
      if (!policy) return;
      setPolicyBusy(true);
      try {
        const next = await v11Api.updateCheckpointPolicy({
          ...policy,
          auto_resume_enabled: enabled,
        });
        if (!mountedRef.current) return;
        setPolicy(next);
        showToast(
          enabled
            ? zh
              ? '🛡️ 已开启「掉线自动换号续跑」'
              : '🛡️ Auto-Checkpoint Relay enabled'
            : zh
              ? '已暂停自动换号续跑'
              : 'Auto-Checkpoint Relay paused',
          'ok',
        );
      } catch (err) {
        showToast(err instanceof Error ? err.message : String(err), 'err');
      } finally {
        if (mountedRef.current) setPolicyBusy(false);
      }
    },
    [policy, showToast, zh],
  );

  const handleManualSyncSnapshot = useCallback(async () => {
    if (!runningSnapshotTarget || !runningSnapshotTarget.session_id) {
      showToast(
        zh
          ? '请先启动至少一个运行中的应用实例，再保存进度快照'
          : 'Launch at least one running app instance before saving a snapshot.',
        'err',
      );
      return;
    }

    const remotePath = runningSnapshotTarget.checkpoint_dir || '/content/outputs';
    setSyncingNow(true);
    try {
      const rec = await v11Api.syncCheckpoint({
        session_id: runningSnapshotTarget.session_id,
        job_id: runningSnapshotTarget.id,
        job_title: `${runningSnapshotTarget.name_zh} · 进度快照`,
        job_kind: 'app',
        remote_path: remotePath,
      });
      if (!mountedRef.current) return;
      setCheckpoints((prev) => [rec, ...prev.filter((c) => c.id !== rec.id)]);
      setCheckpointsOpen(true);
      showToast(
        zh
          ? `📸 已从 ${remotePath} 保存最新进度快照回本机`
          : `📸 Saved snapshot from ${remotePath} to local disk`,
        'ok',
      );
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), 'err');
    } finally {
      if (mountedRef.current) setSyncingNow(false);
    }
  }, [runningSnapshotTarget, showToast, zh]);

  const handleResumeCheckpoint = useCallback(
    async (ckpt: CheckpointRecord) => {
      setResumingId(ckpt.id);
      try {
        const updated = await v11Api.resumeCheckpoint(ckpt.id, {
          strategy: 'free-first',
        });
        if (!mountedRef.current) return;
        setCheckpoints((prev) => prev.map((c) => (c.id === updated.id ? updated : c)));
        showToast(
          zh
            ? `⚡ 已从 ${updated.progress_pct}% 快照切换到 ${updated.resumed_account_label ?? '下一空闲账号'} 接着跑`
            : `⚡ Resumed from ${updated.progress_pct}% on ${updated.resumed_account_label ?? 'next healthy account'}`,
          'ok',
        );
      } catch (err) {
        showToast(err instanceof Error ? err.message : String(err), 'err');
      } finally {
        if (mountedRef.current) setResumingId(null);
      }
    },
    [showToast, zh],
  );

  const snapshotDisabledTitle = !runningSnapshotTarget
    ? zh
      ? '请先启动至少一个运行中的应用实例后再保存进度快照'
      : 'Start at least one running app instance first to save a snapshot'
    : zh
      ? `同步目录：${runningSnapshotTarget.checkpoint_dir || '/content/outputs'}`
      : `Sync dir: ${runningSnapshotTarget.checkpoint_dir || '/content/outputs'}`;

  return (
    <div className="space-y-6 pb-10">
      {/* 1. Calm Page Header */}
      <PageHeader
        title={zh ? 'AI 应用商店' : 'AI App Store'}
        subtitle={
          zh
            ? '把 Google AI Ultra 96GB G4 / H100 与免费 T4 榨干回本：一键部署无审查大模型 (/v1)、WanGP 视频生图全家桶、ComfyUI、Unsloth Studio 零代码微调与第三方插件，用完自动停表防烧 CU。'
            : 'Turn pooled Colab 96GB G4, H100 & Free T4 GPUs into 1-click Uncensored LLM /v1 APIs, WanGP Video & Image Studio, ComfyUI, Unsloth Fine-Tuning, and custom colabhive-app.json plugins with idle auto-stop.'
        }
        badge={
          <StatusPill tone="emerald" dot pulse={activeInstances.length > 0}>
            {activeInstances.length > 0
              ? zh
                ? `${activeInstances.length} 个应用运行中`
                : `${activeInstances.length} Running`
              : zh
                ? '空闲 · 0 消耗'
                : 'Idle · 0 Burn'}
          </StatusPill>
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <ActionButton
              size="sm"
              variant="secondary"
              icon={<Sparkles />}
              onClick={() => onNavigate?.('/studio/media')}
            >
              {zh ? '本地画廊 · 原生创作台' : 'Media Studio & Gallery'}
            </ActionButton>
            <ActionButton
              size="sm"
              variant="secondary"
              icon={<Puzzle />}
              onClick={() => setPluginModalOpen(true)}
            >
              {zh ? '安装第三方插件' : 'Install Plugin'}
            </ActionButton>
          </div>
        }
      />

      {/* Cloud Mode Launch Restriction Banner */}
      {isCloudDeployment && (
        <div
          role="note"
          className="px-4 py-3 rounded-2xl border text-[12.5px] font-medium flex items-start gap-2.5"
          style={{
            backgroundColor: 'var(--amber-soft)',
            borderColor: 'var(--amber-border)',
            color: 'var(--amber-text)',
          }}
        >
          <ShieldAlert className="w-4 h-4 shrink-0 mt-0.5" />
          <span>
            {zh
              ? '云端模式暂不支持本地端口桥接，请使用桌面/本机模式或配置独立隔离代理域名；本地端口类应用已禁用启动以防空耗 GPU。内置「AI 大模型聊天」可正常在云端使用。'
              : 'Cloud mode does not support local 127.0.0.1 port bridging yet. Port-bridged studio launches are disabled to avoid allocating unreachable GPUs. Built-in AI Chat remains available.'}
          </span>
        </div>
      )}

      {/* Honest Live Load Error Banner */}
      {loadError && (
        <div
          role="alert"
          className="px-4 py-3 rounded-2xl border text-[13px] font-medium flex flex-wrap items-center justify-between gap-3"
          style={{
            backgroundColor: 'var(--coral-soft)',
            borderColor: 'var(--coral-border)',
            color: 'var(--coral-text)',
          }}
        >
          <span className="flex items-center gap-2">
            <AlertCircle className="w-4 h-4 shrink-0" />
            <span>
              {zh ? `获取应用商店数据失败：${loadError}` : `Failed to load App Store data: ${loadError}`}
            </span>
          </span>
          <ActionButton
            size="xs"
            variant="secondary"
            icon={<RefreshCw />}
            onClick={() => {
              void fetchTemplates();
              void refreshDynamic();
            }}
          >
            {zh ? '重试' : 'Retry'}
          </ActionButton>
        </div>
      )}

      {/* Feedback Toast / Friendly Actionable Error Banner */}
      {actionBanner && (() => {
        const friendlyErr =
          actionBanner.tone === 'err' ? formatFriendlyAppError(actionBanner.text, zh) : null;
        return (
          <div
            role={actionBanner.tone === 'err' ? 'alert' : 'status'}
            className="px-4 py-3.5 rounded-2xl border text-[13px] flex items-start justify-between gap-4 ui-enter"
            style={
              actionBanner.tone === 'err'
                ? {
                    backgroundColor: 'var(--coral-soft)',
                    borderColor: 'var(--coral-border)',
                    color: 'var(--coral-text)',
                  }
                : {
                    backgroundColor: 'var(--emerald-soft)',
                    borderColor: 'var(--emerald-border)',
                    color: 'var(--emerald-text)',
                  }
            }
          >
            <div className="flex items-start gap-2.5 min-w-0 flex-1">
              {actionBanner.tone === 'err' ? (
                <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
              ) : (
                <Sparkles className="w-4 h-4 shrink-0 mt-0.5" />
              )}
              {friendlyErr ? (
                <div className="min-w-0 flex-1 space-y-1">
                  <div className="font-semibold text-[13.5px] leading-snug">
                    {friendlyErr.title}
                  </div>
                  <div className="text-[12.5px] opacity-90 leading-relaxed">
                    {friendlyErr.summary}
                  </div>
                  {friendlyErr.suggestion && (
                    <div className="text-[12.5px] opacity-95 leading-relaxed">
                      {friendlyErr.suggestion}
                    </div>
                  )}
                  <div className="flex flex-wrap items-center gap-3 pt-1">
                    {friendlyErr.canQueueRetry && lastFailedLaunchReq && !launchingId && (
                      <button
                        type="button"
                        onClick={() => {
                          const targetTpl = templates.find((t) => t.id === lastFailedLaunchReq.template_id) ?? null;
                          if (targetTpl) setDrawerTemplate(targetTpl);
                          void handleConfirmLaunchFromDrawer({
                            ...lastFailedLaunchReq,
                            queue_retry_enabled: true,
                            queue_max_wait_sec: lastFailedLaunchReq.queue_max_wait_sec ?? 600,
                          });
                        }}
                        className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-[12px] font-semibold cursor-pointer border transition-opacity hover:opacity-90 whitespace-nowrap"
                        style={{
                          backgroundColor: 'var(--accent)',
                          color: 'var(--accent-fg)',
                          borderColor: 'var(--accent)',
                        }}
                      >
                        <Sparkles className="w-3.5 h-3.5" />
                        {zh ? '开启安全候补抢卡重试' : 'Retry with Safe GPU Waitlist'}
                      </button>
                    )}
                    {friendlyErr.raw && friendlyErr.raw !== friendlyErr.summary && (
                      <details className="text-[11.5px] opacity-80">
                        <summary className="cursor-pointer select-none hover:opacity-100">
                          {zh ? '查看技术详情' : 'Technical details'}
                        </summary>
                        <pre className="mt-1.5 p-2 rounded-lg bg-black/10 text-[11px] whitespace-pre-wrap break-all font-mono">
                          {friendlyErr.raw}
                        </pre>
                      </details>
                    )}
                  </div>
                </div>
              ) : (
                <span className="font-medium leading-relaxed">{actionBanner.text}</span>
              )}
            </div>
            <button
              type="button"
              onClick={() => setActionBanner(null)}
              className="shrink-0 whitespace-nowrap text-[12px] font-medium underline opacity-80 hover:opacity-100 cursor-pointer bg-transparent border-0 pt-0.5"
            >
              {zh ? '知道了' : 'Dismiss'}
            </button>
          </div>
        );
      })()}

      {/* 2. Transparent Running App Cards */}
      {activeInstances.length > 0 && (
        <LayeredCard
          padding="md"
          title={
            zh
              ? `🟢 正在运行的云端应用 (${activeInstances.length})`
              : `🟢 Running Cloud Studios (${activeInstances.length})`
          }
          subtitle={
            zh
              ? '实时展示所用显卡、执行账号、已加载模型与阶段进度，用完点击停止即可立即释放 GPU'
              : 'Live GPU, account, loaded model, and stage progress — stop anytime to release cloud VRAM.'
          }
        >
          <div className="space-y-3.5">
            {activeInstances.map((inst) => {
              const verifiedHostPort = extractVerifiedLocalHostPort(inst);
              const fullLocalUrl = inst.local_bridge_active && inst.local_url ? inst.local_url : null;
              const isHeadlessStudio = Boolean(inst.media_api_base && inst.launch_mode !== 'native_webui');
              const isCloudProxyBlocked = !isHeadlessStudio && !inst.local_bridge_active && !isDemoSiteMode();
              const canOpenStudio = isDemoSiteMode() || isHeadlessStudio || Boolean(fullLocalUrl);
              const currentIdleMin = idleMinutesByInst[inst.id] ?? inst.idle_auto_stop_min ?? 45;
              const isBusy = instanceBusyId === inst.id;
              const isSavingIdle = idleSavingId === inst.id;
              const isAddrCopied = copiedKey === `inst-${inst.id}`;
              const gpuMeta = getGpuOptionMeta(inst.assigned_gpu);
              const matchedTpl = templates.find((t) => t.id === inst.template_id);
              const matchedModelOpt = matchedTpl?.model_options?.find((m) => m.id === inst.selected_model_id);
              const modelLabel =
                (matchedModelOpt ? (zh ? matchedModelOpt.name_zh : matchedModelOpt.name) : '') ||
                inst.selected_model_name ||
                (inst.model_download_url
                  ? inst.model_download_url.split('/').pop() || (zh ? '自定义模型' : 'Custom Model')
                  : zh
                    ? '空白环境'
                    : 'Blank Environment');
              const pct = Math.max(0, Math.min(100, Math.round(inst.progress_pct ?? 0)));
              const stopError = stopErrorByInstId[inst.id];

              return (
                <div
                  key={inst.id}
                  className="rounded-2xl border p-4 space-y-3.5 transition-all"
                  style={{
                    backgroundColor: 'var(--surface-1)',
                    borderColor:
                      inst.state === 'error' || stopError
                        ? 'var(--coral-border)'
                        : 'var(--emerald-border)',
                  }}
                >
                  {/* Top Row: Identity + Transparent GPU/Account/Model Pills + Controls */}
                  <div className="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-3">
                    <div className="space-y-2 min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-[15px] font-semibold text-fg">
                          {zh ? inst.name_zh : inst.name}
                        </span>
                        <StatusPill
                          size="xs"
                          tone={inst.state === 'error' ? 'coral' : inst.state === 'starting' || inst.state === 'failover' ? 'amber' : 'emerald'}
                          dot
                          pulse={inst.state === 'running' || inst.state === 'starting' || inst.state === 'failover'}
                        >
                          {inst.state === 'starting'
                            ? zh
                              ? `启动中 ${pct}%`
                              : `Starting ${pct}%`
                            : inst.state === 'failover'
                              ? zh
                                ? `换号续跑中 ${pct}%`
                                : `Relaying ${pct}%`
                              : inst.state === 'error'
                                ? zh
                                  ? '异常'
                                  : 'Error'
                                : zh
                                  ? '运行就绪'
                                  : 'Running'}
                        </StatusPill>
                      </div>

                      {/* Transparent Hardware + Account + Loaded Model Badges */}
                      <div className="flex flex-wrap items-center gap-2">
                        <StatusPill
                          size="xs"
                          tone={inst.free_tier_active ? 'emerald' : 'amber'}
                          mono
                        >
                          {`⚡ ${gpuMeta.label} (${gpuMeta.vramLabel}) · ${sanitizeAccountLabel(inst.account_label, zh)}`}
                        </StatusPill>
                        <StatusPill size="xs" tone="neutral" icon={<Package />}>
                          {`📦 ${modelLabel}`}
                        </StatusPill>
                      </div>

                      {/* Real-time Stage Progress Bar */}
                      <div className="space-y-1 pt-0.5 max-w-xl">
                        <div className="flex items-center justify-between gap-2 text-[12px]">
                          <span className="text-fg-secondary truncate">
                            {inst.error_message ||
                              (zh
                                ? inst.status_step_zh || '应用已在云端显卡运行中'
                                : inst.status_step || 'Running on cloud GPU')}
                          </span>
                          <span className="ch-num text-[11.5px] font-semibold text-fg-muted shrink-0">
                            {pct}%
                          </span>
                        </div>
                        <SegmentedQuotaBar
                          value={pct}
                          segments={20}
                          tone={
                            inst.state === 'error'
                              ? 'coral'
                              : inst.state === 'starting' || inst.state === 'failover'
                                ? 'amber'
                                : 'emerald'
                          }
                        />
                      </div>
                    </div>

                    {/* Open UI + Restart + Stop */}
                    <div className="flex flex-wrap items-center gap-2 shrink-0">
                      <ActionButton
                        size="md"
                        variant="primary"
                        disabled={!canOpenStudio || inst.state === 'starting'}
                        title={
                          isCloudProxyBlocked
                            ? zh
                              ? '云端模式暂不支持本地端口桥接，请使用桌面/本机模式或配置独立隔离代理域名'
                              : 'Cloud mode requires an isolated-origin app proxy'
                            : undefined
                        }
                        icon={<ExternalLink />}
                        onClick={() => handleOpenAppUI(inst)}
                      >
                        {zh ? '打开界面' : 'Open Studio'}
                      </ActionButton>
                      {inst.state === 'starting' ? (
                        <ActionButton
                          size="sm"
                          variant="danger"
                          loading={isBusy}
                          icon={<Square />}
                          onClick={() => void handleStopInstance(inst, true)}
                        >
                          {zh ? '取消部署并释放显卡' : 'Cancel & Release GPU'}
                        </ActionButton>
                      ) : (
                        <>
                          <ActionButton
                            size="sm"
                            variant="secondary"
                            loading={isBusy}
                            icon={<RotateCcw />}
                            onClick={() => void handleRestartInstance(inst)}
                          >
                            {zh ? '重启' : 'Restart'}
                          </ActionButton>
                          <ActionButton
                            size="sm"
                            variant="secondary"
                            loading={isBusy}
                            icon={<Square />}
                            onClick={() => void handleStopInstance(inst, false)}
                          >
                            {zh ? '停止释放' : 'Stop'}
                          </ActionButton>
                        </>
                      )}
                    </div>
                  </div>

                  {/* Collapsible Live / Recent App Deployment Logs */}
                  {(inst.state === 'starting' || inst.state === 'failover' || (inst.deploy_logs?.length ?? 0) > 0) && (() => {
                    const instLogs = inst.deploy_logs ?? [];
                    const logsOpen =
                      logPanelPrefByInstId[inst.id] ??
                      (inst.state === 'starting' || inst.state === 'failover' || inst.state === 'error');
                    return (
                      <div
                        className="rounded-xl border overflow-hidden"
                        style={{
                          borderColor:
                            inst.state === 'error'
                              ? 'var(--coral-border)'
                              : 'var(--border-subtle)',
                          backgroundColor: 'var(--surface-2)',
                        }}
                      >
                        <div
                          className="px-3 py-2 flex items-center justify-between gap-2 text-[11.5px]"
                          style={{
                            borderBottom: logsOpen ? '1px solid var(--border-faint)' : undefined,
                          }}
                        >
                          <button
                            type="button"
                            onClick={() =>
                              setLogPanelPrefByInstId((prev) => ({
                                ...prev,
                                [inst.id]: !logsOpen,
                              }))
                            }
                            className="inline-flex items-center gap-1.5 font-semibold text-fg hover:text-accent cursor-pointer transition-colors"
                          >
                            {logsOpen ? (
                              <ChevronUp className="w-3.5 h-3.5 text-fg-muted" />
                            ) : (
                              <ChevronDown className="w-3.5 h-3.5 text-fg-muted" />
                            )}
                            <Terminal className="w-3.5 h-3.5 text-fg-muted" />
                            <span>
                              {inst.state === 'starting' || inst.state === 'failover'
                                ? zh
                                  ? '实时部署日志'
                                  : 'Live Deploy Logs'
                                : zh
                                  ? '部署日志'
                                  : 'Deploy Logs'}
                            </span>
                            <span
                              className="px-1.5 py-0.5 rounded-md text-[10.5px] ch-num font-medium"
                              style={{
                                backgroundColor: 'var(--surface-3, rgba(120,113,108,0.14))',
                                color: 'var(--text-secondary)',
                              }}
                            >
                              {zh ? `${instLogs.length} 条` : `${instLogs.length} lines`}
                            </span>
                          </button>

                          {instLogs.length > 0 && (
                            <button
                              type="button"
                              onClick={() => void copyText(`logs-${inst.id}`, instLogs.join('\n'))}
                              className="inline-flex items-center gap-1 px-2 py-1 rounded-lg border text-[11px] font-medium text-fg-secondary hover:text-fg bg-surface-1 cursor-pointer transition-colors"
                              style={{ borderColor: 'var(--border-faint)' }}
                            >
                              {copiedKey === `logs-${inst.id}` ? (
                                <Check className="w-3 h-3" style={{ color: 'var(--emerald-text)' }} />
                              ) : (
                                <Copy className="w-3 h-3" />
                              )}
                              <span>
                                {copiedKey === `logs-${inst.id}`
                                  ? zh
                                    ? '已复制'
                                    : 'Copied'
                                  : zh
                                    ? '复制日志'
                                    : 'Copy logs'}
                              </span>
                            </button>
                          )}
                        </div>

                        {logsOpen && (
                          <div
                            className="max-h-52 overflow-y-auto px-3.5 py-2.5 font-mono text-[11.5px] leading-relaxed space-y-1"
                            style={{ backgroundColor: '#18181B', color: '#F4F4F5' }}
                          >
                            {instLogs.length === 0 ? (
                              <div className="text-zinc-400 animate-pulse">
                                {zh ? '等待云端显卡返回部署日志…' : 'Waiting for cloud GPU deployment logs…'}
                              </div>
                            ) : (
                              instLogs.map((ln, idx) => {
                                const lower = ln.toLowerCase();
                                const isErrLine =
                                  ln.includes('失败') || lower.includes('error') || lower.includes('failed');
                                const isSuccessLine =
                                  ln.includes('已自动释放') ||
                                  ln.includes('部署完成') ||
                                  ln.includes('已分配云端显卡') ||
                                  ln.includes('已就绪');
                                return (
                                  <div
                                    key={`${idx}-${ln.slice(0, 24)}`}
                                    className="break-all whitespace-pre-wrap"
                                    style={{
                                      color: isErrLine
                                        ? '#FCA5A5'
                                        : isSuccessLine
                                          ? '#6EE7B7'
                                          : '#E4E4E7',
                                    }}
                                  >
                                    {ln}
                                  </div>
                                );
                              })
                            )}
                          </div>
                        )}
                      </div>
                    );
                  })()}

                  {/* Pre-Stop Checkpoint Sync Failure -> Clear Retry / Force Stop Strip */}
                  {stopError && (
                    <div
                      role="alert"
                      className="rounded-xl border px-3.5 py-2.5 flex flex-col sm:flex-row sm:items-center justify-between gap-2.5 text-[12px]"
                      style={{
                        backgroundColor: 'var(--coral-soft)',
                        borderColor: 'var(--coral-border)',
                        color: 'var(--coral-text)',
                      }}
                    >
                      <div className="flex items-start gap-2 min-w-0">
                        <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
                        <div className="min-w-0">
                          <div className="font-semibold">
                            {zh
                              ? '停机前自动快照备份失败，已暂缓销毁云端显卡以防丢数据'
                              : 'Pre-stop checkpoint backup failed — VM kept alive to prevent data loss'}
                          </div>
                          <div className="opacity-90 break-words">{stopError}</div>
                        </div>
                      </div>
                      <div className="flex items-center gap-2 shrink-0">
                        <ActionButton
                          size="xs"
                          variant="secondary"
                          loading={isBusy}
                          icon={<RotateCcw />}
                          onClick={() => void handleStopInstance(inst, false)}
                        >
                          {zh ? '重试备份并停止' : 'Retry Backup & Stop'}
                        </ActionButton>
                        <ActionButton
                          size="xs"
                          variant="coral"
                          loading={isBusy}
                          icon={<Square />}
                          onClick={() => void handleStopInstance(inst, true)}
                        >
                          {zh ? '跳过备份强制停止' : 'Force Stop'}
                        </ActionButton>
                      </div>
                    </div>
                  )}

                  {/* Bottom Strip: Verified Localhost Address + Persistent Idle Timeout */}
                  <div className="pt-3 border-t border-line-faint flex flex-col xl:flex-row xl:items-center xl:justify-between gap-3">
                    <div className="flex flex-wrap items-center gap-2.5">
                      {verifiedHostPort && fullLocalUrl ? (
                        <div className="inline-flex items-center gap-2 px-3 py-1.5 rounded-xl bg-surface-2 border border-line">
                          <span className="text-[11.5px] font-medium text-fg-muted">
                            {zh ? '本机私有直连：' : 'Local Bridge:'}
                          </span>
                          <code className="ch-num text-[12.5px] font-bold text-fg">
                            {verifiedHostPort}
                          </code>
                          <button
                            type="button"
                            onClick={() => void copyText(`inst-${inst.id}`, fullLocalUrl)}
                            className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[11.5px] font-medium bg-surface-1 hover:bg-surface-2 border border-line-faint text-fg transition-colors cursor-pointer"
                          >
                            {isAddrCopied ? (
                              <Check className="w-3 h-3" style={{ color: 'var(--emerald)' }} />
                            ) : (
                              <Copy className="w-3 h-3" />
                            )}
                            {isAddrCopied ? (zh ? '已复制' : 'Copied') : zh ? '复制' : 'Copy'}
                          </button>
                        </div>
                      ) : isCloudProxyBlocked ? (
                        <StatusPill size="sm" tone="amber" icon={<ShieldAlert />}>
                          {zh
                            ? '云端模式暂不支持本地端口桥接，请使用桌面/本机模式或配置独立隔离代理域名'
                            : 'Cloud mode requires desktop/local mode or an isolated app proxy domain'}
                        </StatusPill>
                      ) : (
                        <span className="text-[12px] text-fg-muted">
                          {zh ? '本机端口桥接准备中' : 'Localhost bridge pending'}
                        </span>
                      )}
                    </div>

                    <div className="flex items-center gap-2 shrink-0">
                      <Clock className="w-3.5 h-3.5 text-fg-muted shrink-0" />
                      <label className="text-[12px] text-fg-secondary">
                        {zh ? '防忘关机：' : 'Auto-stop:'}
                      </label>
                      <select
                        value={currentIdleMin}
                        disabled={isSavingIdle}
                        onChange={(e) => {
                          const mins = Number(e.target.value);
                          void handleUpdateInstanceIdleTimeout(inst, mins);
                        }}
                        className="rounded-lg border border-line bg-surface-2 px-2.5 py-1 text-[12px] font-medium text-fg outline-none disabled:opacity-60"
                      >
                        {IDLE_SHUTDOWN_PRESETS.map((m) => (
                          <option key={m} value={m}>
                            {zh ? `${m} 分钟无人操作自动关机` : `${m}m idle auto-stop`}
                          </option>
                        ))}
                      </select>
                    </div>
                  </div>

                  {inst.last_failover_note && (
                    <div
                      className="flex items-center gap-2 px-3 py-1.5 rounded-lg text-[12px] font-medium"
                      style={{
                        backgroundColor: 'var(--emerald-soft)',
                        color: 'var(--emerald-text)',
                      }}
                    >
                      <ShieldCheck className="w-3.5 h-3.5 shrink-0" />
                      <span>{sanitizeAccountLabel(inst.last_failover_note, zh)}</span>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </LayeredCard>
      )}

      {/* 3. Dynamic Category Filter Pills */}
      <div
        role="group"
        aria-label={zh ? '应用分类筛选' : 'Filter apps by category'}
        className="flex flex-wrap items-center gap-2"
      >
        {categoryFilterPills.map((cat) => {
          const selected = category === cat.id;
          return (
            <button
              key={cat.id}
              type="button"
              aria-pressed={selected}
              onClick={() => setCategory(cat.id)}
              className={cn(
                'px-3.5 py-1.5 rounded-full text-[12.5px] font-medium transition-all border cursor-pointer',
                selected
                  ? 'shadow-sm'
                  : 'bg-surface-2 text-fg-secondary border-line hover:text-fg hover:border-line-strong',
              )}
              style={
                selected
                  ? {
                      backgroundColor: 'var(--accent)',
                      color: 'var(--accent-fg)',
                      borderColor: 'var(--accent)',
                    }
                  : undefined
              }
            >
              {zh ? cat.labelZh : cat.labelEn}
            </button>
          );
        })}
      </div>

      {/* 4. Core App 1 (Flagship Built-In): Uncensored Open-Weight LLM Deployer (/v1) */}
      {showFlagshipChatCard && (
        <LayeredCard
          padding="md"
          staggerIndex={0}
          className="rounded-[22px]"
          style={{
            borderColor: 'var(--emerald-border)',
            backgroundImage:
              'linear-gradient(135deg, var(--emerald-soft) 0%, var(--surface-2) 52%, var(--surface-2) 100%)',
          }}
        >
          <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-5">
            <div className="flex items-start gap-4 min-w-0">
              <div
                className="w-13 h-13 rounded-2xl border flex items-center justify-center shrink-0 shadow-sm"
                style={{
                  backgroundColor: 'var(--surface-2)',
                  borderColor: 'var(--emerald-border)',
                  color: 'var(--emerald-text)',
                }}
              >
                <Zap className="w-6 h-6" />
              </div>
              <div className="min-w-0 space-y-1.5">
                <div className="flex flex-wrap items-center gap-2">
                  <StatusPill size="xs" tone="emerald" dot pulse>
                    {zh ? '2026 无审查大模型榜' : '2026 Uncensored LLM'}
                  </StatusPill>
                  <StatusPill size="xs" tone="amber">
                    {zh ? 'Heretic / Abliterated · 输出本机 /v1' : 'Heretic / Abliterated · Local /v1'}
                  </StatusPill>
                </div>
                <h2 className="text-[17px] font-bold tracking-tight text-fg">
                  {zh
                    ? builtinChatTemplate?.name_zh || '无审查开源大模型一键部署 (/v1)'
                    : builtinChatTemplate?.name || 'Uncensored Open-Weight LLM Deployer (/v1)'}
                </h2>
                <p className="text-[12.5px] leading-relaxed text-fg-secondary max-w-2xl">
                  {zh
                    ? builtinChatTemplate?.description_zh ||
                      '精选 DeepSeek-V4.1-Flash-1017、GLM-5.3-Flash、Qwen3.8-Flash-Next、Qwen3.8-27B 与 Gemma 4 等 2026 年 11 月最热 Heretic / Abliterated 无审查 GGUF 模型（支持任意 hf.co 链接），一键部署并吐出本机 /v1 接口直连 SillyTavern 酒馆、Cherry Studio 与 Cursor。'
                    : builtinChatTemplate?.description ||
                      'Deploy top Nov 2026 uncensored GGUFs (DeepSeek-V4.1-Flash-1017, GLM-5.3-Flash, Qwen3.8-Flash-Next, Qwen3.8-27B, Gemma 4, or any hf.co repo) onto pooled Colab GPUs and connect SillyTavern, Cherry Studio, or Cursor to localhost /v1.'}
                </p>
              </div>
            </div>

            <div className="flex flex-col sm:flex-row lg:flex-col items-stretch sm:items-center lg:items-end gap-1.5 shrink-0">
              <ActionButton
                size="md"
                variant="primary"
                icon={<Zap />}
                onClick={() => nav('/apps/llm')}
              >
                {zh ? '配置并部署模型' : 'Deploy Uncensored LLM'}
              </ActionButton>
              <span className="text-[11px] text-fg-muted ch-num">127.0.0.1:7860/v1</span>
            </div>
          </div>
        </LayeredCard>
      )}

      {/* 5. Launchable Core Apps & Community Plugins Grid (Symmetrical 2-column layout, pinned footers) */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 items-stretch">
        {filteredTemplates.map((tpl, idx) => {
          const runningInst = activeInstances.find((i) => i.template_id === tpl.id);
          const isLaunching = launchingId === tpl.id;
          const canOpenRunning = Boolean(
            runningInst &&
              (isDemoSiteMode() ||
                Boolean(runningInst.media_api_base && runningInst.launch_mode !== 'native_webui') ||
                (runningInst.local_bridge_active && runningInst.local_url)),
          );
          const modelCount = tpl.model_options?.length ?? 0;

          const iconVisual = (() => {
            if (tpl.id === 'wangp' || tpl.category === 'video') {
              return {
                node: <Film className="w-5 h-5" />,
                bg: 'var(--amber-soft)',
                border: 'var(--amber-border)',
                color: 'var(--amber-text)',
              };
            }
            if (tpl.id === 'comfyui') {
              return {
                node: <Workflow className="w-5 h-5" />,
                bg: 'var(--sky-soft)',
                border: 'var(--sky-border)',
                color: 'var(--sky-text)',
              };
            }
            if (tpl.id === 'unsloth-studio') {
              return {
                node: <Cpu className="w-5 h-5" />,
                bg: 'var(--emerald-soft)',
                border: 'var(--emerald-border)',
                color: 'var(--emerald-text)',
              };
            }
            if (tpl.id === 'jupyterlab' || tpl.category === 'workspace') {
              return {
                node: <Terminal className="w-5 h-5" />,
                bg: 'var(--surface-1)',
                border: 'var(--border-subtle)',
                color: 'var(--text-primary)',
              };
            }
            if (tpl.category === 'image') {
              return {
                node: <Sparkles className="w-5 h-5" />,
                bg: 'var(--sky-soft)',
                border: 'var(--sky-border)',
                color: 'var(--sky-text)',
              };
            }
            return {
              node: <Puzzle className="w-5 h-5" />,
              bg: 'var(--surface-1)',
              border: 'var(--border-subtle)',
              color: 'var(--text-secondary)',
            };
          })();

          const cardFooter = (
            <div className="space-y-2">
              {isCloudDeployment && !runningInst && (
                <div className="flex items-start gap-1.5 text-[11.5px] text-[var(--amber-text)]">
                  <ShieldAlert className="w-3.5 h-3.5 shrink-0 mt-0.5" />
                  <span>
                    {zh
                      ? '云端模式暂不支持本地端口桥接，请使用桌面/本机模式或配置独立隔离代理域名'
                      : 'Cloud mode requires desktop/local mode or isolated proxy domain'}
                  </span>
                </div>
              )}

              {runningInst ? (
                <ActionButton
                  size="md"
                  variant="primary"
                  fullWidth
                  disabled={!canOpenRunning || runningInst.state === 'starting'}
                  icon={<ExternalLink />}
                  onClick={() => handleOpenAppUI(runningInst)}
                >
                  {canOpenRunning
                    ? zh
                      ? '运行中 · 打开界面'
                      : 'Running · Open Studio'
                    : zh
                      ? '运行中（需隔离域名代理）'
                      : 'Running (Isolated Proxy Req.)'}
                </ActionButton>
              ) : (
                <div className="flex items-center gap-2">
                  <ActionButton
                    size="md"
                    variant="primary"
                    fullWidth
                    disabled={isCloudDeployment}
                    loading={isLaunching}
                    icon={<Play />}
                    onClick={() => setDrawerTemplate(tpl)}
                  >
                    {isCloudDeployment
                      ? zh
                        ? '云端模式不可启动'
                        : 'Disabled in Cloud Mode'
                      : zh
                        ? '选择模型与显卡启动'
                        : 'Configure & Launch'}
                  </ActionButton>
                  {tpl.is_plugin && (
                    <ActionButton
                      size="md"
                      variant="secondary"
                      loading={deletingPluginId === tpl.id}
                      icon={<Trash2 />}
                      title={zh ? '卸载此第三方插件' : 'Uninstall this plugin'}
                      aria-label={zh ? `卸载插件 ${tpl.name_zh || tpl.name}` : `Uninstall ${tpl.name}`}
                      onClick={() => void handleDeletePlugin(tpl)}
                    >
                      {zh ? '卸载' : 'Remove'}
                    </ActionButton>
                  )}
                </div>
              )}
            </div>
          );

          return (
            <LayeredCard
              key={tpl.id}
              padding="md"
              staggerIndex={idx + 1}
              className="h-full"
              footer={cardFooter}
            >
              <div className="space-y-3.5">
                {/* App Icon + Title + Badges */}
                <div className="flex items-start justify-between gap-3">
                  <div className="flex items-center gap-3 min-w-0">
                    <div
                      className="w-10 h-10 rounded-xl border flex items-center justify-center shrink-0"
                      style={{
                        backgroundColor: iconVisual.bg,
                        borderColor: iconVisual.border,
                        color: iconVisual.color,
                      }}
                    >
                      {iconVisual.node}
                    </div>
                    <div className="min-w-0">
                      <div className="flex items-center gap-1.5 min-w-0">
                        <h3 className="text-[15px] font-semibold text-fg leading-snug truncate">
                          {zh ? tpl.name_zh : tpl.name}
                        </h3>
                        {tpl.is_plugin && (
                          <StatusPill size="xs" tone="sky" className="shrink-0">
                            {zh ? '插件' : 'Plugin'}
                          </StatusPill>
                        )}
                      </div>
                      <p className="mt-0.5 text-[11.5px] font-medium text-fg-muted truncate">
                        {zh ? `推荐 ${tpl.recommended_gpu}` : `Rec. ${tpl.recommended_gpu}`}
                        {modelCount > 0
                          ? zh
                            ? ` · ${modelCount} 套模型预设`
                            : ` · ${modelCount} presets`
                          : ''}
                        {tpl.supports_custom_model_url
                          ? zh
                            ? ' · 支持自定义直链'
                            : ' · Custom URL'
                          : ''}
                      </p>
                    </div>
                  </div>

                  {tpl.free_tier_compatible ? (
                    <StatusPill size="xs" tone="emerald" className="shrink-0">
                      {zh ? '免费卡可跑' : 'Free GPU'}
                    </StatusPill>
                  ) : (
                    <StatusPill size="xs" tone="amber" className="shrink-0" mono>
                      {tpl.recommended_gpu}
                    </StatusPill>
                  )}
                </div>

                {/* Single Clean Summary */}
                <p className="text-[12.5px] leading-relaxed text-fg-secondary line-clamp-2 min-h-[40px]">
                  {zh ? tpl.tagline_zh : tpl.tagline}
                </p>

                {/* Crisp 3-line Feature Bullets */}
                <ul className="space-y-1.5 pt-0.5">
                  {(zh ? tpl.highlights_zh : tpl.highlights).slice(0, 3).map((hl) => (
                    <li
                      key={hl}
                      className="flex items-start gap-2 text-[12px] leading-snug text-fg-secondary"
                    >
                      <Check
                        className="w-3.5 h-3.5 shrink-0 mt-0.5"
                        style={{ color: 'var(--emerald-text)' }}
                      />
                      <span className="line-clamp-1" title={hl}>
                        {hl}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            </LayeredCard>
          );
        })}
      </div>

      {/* Compact Horizontal Third-Party Plugin Install Bar */}
      <button
        type="button"
        onClick={() => setPluginModalOpen(true)}
        className="w-full rounded-2xl border border-dashed border-line-strong bg-surface-1/50 hover:bg-surface-2 hover:border-[var(--accent-border)] px-4 sm:px-5 py-3.5 flex flex-wrap items-center justify-between gap-3 text-left transition-all cursor-pointer"
      >
        <div className="flex items-center gap-3 min-w-0">
          <span
            className="w-9 h-9 rounded-xl flex items-center justify-center shrink-0"
            style={{ backgroundColor: 'var(--amber-soft)', color: 'var(--amber-text)' }}
          >
            <Puzzle className="w-4 h-4" />
          </span>
          <div className="min-w-0">
            <div className="text-[13.5px] font-semibold text-fg">
              {zh ? '安装第三方插件' : 'Install Third-Party Plugin'}
            </div>
            <p className="text-[12px] text-fg-secondary truncate">
              {zh
                ? '拖入或粘贴 colabhive-app.json 清单，把任意开源 AI WebUI 接入应用商店'
                : 'Drop or paste a colabhive-app.json manifest to add any open-source AI studio'}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <StatusPill size="xs" tone="neutral" mono>
            colabhive-app.json
          </StatusPill>
          <ArrowRight className="w-4 h-4 text-fg-muted" />
        </div>
      </button>

      {/* 6. Compact Collapsible Auto-Checkpoint Relay Bar (Collapsed by Default!) */}
      <LayeredCard padding="none" className="overflow-hidden">
        <div className="px-4 sm:px-5 py-3 flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-2.5 min-w-0">
            <ShieldCheck
              className="w-4 h-4 shrink-0"
              style={{
                color: policy?.auto_resume_enabled ? 'var(--emerald-text)' : 'var(--text-muted)',
              }}
            />
            <span className="text-[13.5px] font-semibold text-fg">
              {zh ? '掉线自动换号续跑' : 'Auto-Checkpoint Relay'}
            </span>
            <StatusPill size="xs" tone={policy?.auto_resume_enabled ? 'emerald' : 'neutral'} dot>
              {policy?.auto_resume_enabled
                ? zh
                  ? `已开启 · ${checkpoints.length} 条快照`
                  : `ON · ${checkpoints.length} snapshots`
                : zh
                  ? `已暂停 · ${checkpoints.length} 条快照`
                  : `Paused · ${checkpoints.length} snapshots`}
            </StatusPill>
            {runningSnapshotTarget && (
              <span className="hidden lg:inline ch-num text-[11.5px] text-fg-muted truncate">
                {runningSnapshotTarget.checkpoint_dir || '/content/outputs'}
              </span>
            )}
          </div>

          <div className="flex flex-wrap items-center gap-2 shrink-0">
            <Switch
              checked={policy?.auto_resume_enabled ?? false}
              disabled={policyBusy || !policy}
              label={zh ? '掉线自动换号续跑总开关' : 'Auto-Checkpoint Relay toggle'}
              onChange={(checked) => void handleToggleAutoResume(checked)}
            />
            <ActionButton
              size="xs"
              variant="secondary"
              loading={syncingNow}
              disabled={!runningSnapshotTarget}
              title={snapshotDisabledTitle}
              icon={<HardDriveDownload />}
              onClick={() => void handleManualSyncSnapshot()}
            >
              {zh ? '立即保存快照' : 'Save Snapshot'}
            </ActionButton>
            <button
              type="button"
              aria-expanded={checkpointsOpen}
              onClick={() => setCheckpointsOpen((v) => !v)}
              className="inline-flex items-center gap-1 h-7 px-2.5 rounded-lg text-[12px] font-medium text-fg-secondary hover:text-fg hover:bg-surface-1 transition-colors cursor-pointer bg-transparent border border-line-faint"
            >
              <span>
                {checkpointsOpen
                  ? zh
                    ? '收起快照'
                    : 'Hide'
                  : zh
                    ? `查看快照 (${checkpoints.length})`
                    : `Snapshots (${checkpoints.length})`}
              </span>
              {checkpointsOpen ? (
                <ChevronUp className="w-3.5 h-3.5" />
              ) : (
                <ChevronDown className="w-3.5 h-3.5" />
              )}
            </button>
          </div>
        </div>

        {checkpointsOpen && (
          <div className="px-4 sm:px-5 pb-4 pt-3 border-t border-line-faint space-y-3 bg-surface-1/40">
            <div className="flex items-center justify-between gap-2 text-[12px] text-fg-muted">
              <span className="flex items-center gap-1.5">
                <Layers className="w-3.5 h-3.5" />
                {zh
                  ? `定时备份间隔：每 ${Math.max(1, Math.round((policy?.sync_interval_sec ?? 180) / 60))} 分钟自动同步云端输出至本机，掉线后可一键换号续跑`
                  : `Auto-syncs every ${Math.max(1, Math.round((policy?.sync_interval_sec ?? 180) / 60))}m to local disk for instant account relay`}
              </span>
              <button
                type="button"
                onClick={() => void refreshDynamic()}
                className="inline-flex items-center gap-1 text-[11.5px] text-fg-secondary hover:text-fg cursor-pointer bg-transparent border-0"
              >
                <RefreshCw className="w-3 h-3" />
                {zh ? '刷新' : 'Refresh'}
              </button>
            </div>

            {checkpoints.length === 0 ? (
              <div className="rounded-xl border border-line-faint bg-surface-2/60 p-4 text-center text-[12px] text-fg-muted">
                {zh
                  ? '暂无快照记录：启动应用后将自动定时备份，或点击上方「立即保存快照」。'
                  : 'No checkpoints yet — launch an app or click Save Snapshot.'}
              </div>
            ) : (
              <div className="space-y-2.5">
                {checkpoints.map((ckpt) => {
                  const isResumed = ckpt.status === 'resumed' || Boolean(ckpt.resumed_account_label);
                  const srcLabel = sanitizeAccountLabel(ckpt.source_account_label, zh);
                  const dstLabel = ckpt.resumed_account_label
                    ? sanitizeAccountLabel(ckpt.resumed_account_label, zh)
                    : zh
                      ? '待命账号'
                      : 'Standby';
                  const note = zh
                    ? ckpt.last_relay_note_zh ||
                      `${srcLabel} → ${dstLabel} (${ckpt.progress_pct}%)`
                    : ckpt.last_relay_note_en ||
                      `${srcLabel} -> ${dstLabel} (${ckpt.progress_pct}%)`;

                  return (
                    <div
                      key={ckpt.id}
                      className="rounded-xl border border-line bg-surface-2 p-3 flex flex-col md:flex-row md:items-center justify-between gap-3"
                    >
                      <div className="min-w-0 space-y-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-[13px] font-semibold text-fg truncate">
                            {ckpt.job_title}
                          </span>
                          <StatusPill size="xs" tone={isResumed ? 'emerald' : 'amber'}>
                            {isResumed
                              ? zh
                                ? `已续跑 ${Math.max(1, ckpt.relay_count)} 次`
                                : `Relayed ${Math.max(1, ckpt.relay_count)}×`
                              : zh
                                ? '快照已就绪'
                                : 'Synced'}
                          </StatusPill>
                          <span className="ch-num text-[11px] text-fg-muted">
                            {ckpt.remote_path} · {ckpt.file_count} {zh ? '文件' : 'files'} ·{' '}
                            {formatBytes(ckpt.total_bytes)} · {formatTimeAgo(ckpt.updated_at, zh)}
                          </span>
                        </div>
                        <div className="text-[12px] text-fg-secondary flex items-center gap-1.5">
                          <FolderSync className="w-3.5 h-3.5 shrink-0 text-[var(--emerald-text)]" />
                          <span className="truncate">{note}</span>
                        </div>
                      </div>

                      <div className="flex items-center gap-2 shrink-0">
                        <span className="ch-num text-[11.5px] text-fg-muted hidden sm:inline-flex items-center gap-1">
                          {srcLabel}
                          <ArrowRight className="w-3 h-3 text-[var(--emerald)]" />
                          {dstLabel}
                        </span>
                        <ActionButton
                          size="xs"
                          variant="secondary"
                          loading={resumingId === ckpt.id}
                          icon={<Play />}
                          onClick={() => void handleResumeCheckpoint(ckpt)}
                        >
                          {zh ? '换号接着跑' : 'Relay Now'}
                        </ActionButton>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        )}
      </LayeredCard>

      {/* Unified 3-Step Transparent Launch Drawer */}
      <AppLaunchDrawer
        open={Boolean(drawerTemplate)}
        template={drawerTemplate}
        accounts={accounts}
        defaultAutoResume={policy?.auto_resume_enabled ?? true}
        launching={Boolean(drawerTemplate && (launchingId === drawerTemplate.id || drawerLaunchingInstance))}
        launchingInstance={drawerLaunchingInstance}
        launchError={drawerLaunchError}
        cancelingLaunch={Boolean(drawerLaunchingInstance && instanceBusyId === drawerLaunchingInstance.id)}
        isCloudMode={isCloudDeployment}
        onClose={() => {
          setDrawerTemplate(null);
          setDrawerLaunchError(null);
        }}
        onConfirm={handleConfirmLaunchFromDrawer}
        onCancelLaunch={async (instId) => {
          const target = instances.find((i) => i.id === instId);
          if (target) {
            await handleStopInstance(target, true);
          } else {
            try {
              await v11Api.stopApp(instId, true);
              await refreshDynamic();
            } catch {
              // ignore
            }
          }
        }}
      />

      <InstallPluginModal
        open={pluginModalOpen}
        onClose={() => setPluginModalOpen(false)}
        onInstalled={(tpl) => {
          void fetchTemplates();
          showToast(
            zh
              ? `🧩 插件「${tpl.name_zh}」已安装到 AI 应用商店！`
              : `🧩 Plugin "${tpl.name}" installed into the AI App Store!`,
            'ok',
          );
        }}
        zh={zh}
      />

      <DemoStudioPreviewModal
        instance={previewInstance}
        onClose={() => setPreviewInstance(null)}
        zh={zh}
      />
    </div>
  );
}

export default AppStorePage;
