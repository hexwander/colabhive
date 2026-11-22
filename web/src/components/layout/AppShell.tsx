import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  ArrowLeft,
  ArrowUpRight,
  Camera,
  Cloud,
  CornerDownLeft,
  Eye,
  EyeOff,
  Gauge,
  Globe,
  HeartHandshake,
  KeyRound,
  Lock,
  LogOut,
  Moon,
  MoreHorizontal,
  PanelLeftClose,
  PanelLeftOpen,
  RefreshCw,
  Route,
  Search,
  ShieldCheck,
  Sparkles,
  Sun,
  TerminalSquare,
  Users,
  X,
  Zap,
} from 'lucide-react';
import type {
  Account,
  DaemonHealthStatus,
  Experiment,
  QuotaSummary,
  RuntimeSession,
} from '../../types';
import {
  ADMIN_TOKEN_CHANGED_EVENT,
  api,
  APIError,
  clearStoredAdminToken,
  formatCU,
  getStoredAdminToken,
  setStoredAdminToken,
  UNAUTHORIZED_EVENT,
  type WSConnectionState,
} from '../../lib/api';
import { IS_STATIC_DEMO_BUILD } from '../../lib/demoSiteAdapter';
import { useI18n } from '../../i18n';
import { getObservatoryHomePath, type AppSection, type ParsedRoute } from './navigation';
import { ShareQuotaModal } from './ShareQuotaModal';
import { DEMO_ACCOUNTS } from './telemetryUi';
import {
  ActionButton,
  AnimatedNumber,
  CircularQuotaRing,
  cn,
  DialogModal,
  IconButton,
  openQuickStartGuide,
  StatusPill,
  type QuotaRingTone,
} from '../ui/primitives';
import {
  BrandMark,
  useNativeWindowControls,
  WindowsCaptionButtons,
} from '../ui/BrandLogo';

export interface AppShellProps {
  route: ParsedRoute & { navigate: (to: string, replace?: boolean) => void };
  summary: QuotaSummary;
  accounts?: Account[];
  experiments?: Experiment[];
  sessions?: RuntimeSession[];
  health: DaemonHealthStatus;
  wsState: WSConnectionState;
  offlineSnapshot: boolean;
  onRefreshTelemetry: () => void;
  onOpenConnectDrawer: (keyPrefix?: string) => void;
  children: React.ReactNode;
}

const SIDEBAR_COLLAPSED_KEY = 'colabhive.sidebar.collapsed';
const DISMISSED_ALERTS_KEY = 'colabhive_dismissed_alerts_v1';

function readSidebarCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === 'true';
  } catch {
    return false;
  }
}

function readDismissedAlerts(): string[] {
  try {
    const raw = localStorage.getItem(DISMISSED_ALERTS_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

/** Closes a popover on Escape or on a pointer press outside `[data-dismiss-scope=<scope>]`. */
function useDismiss(scope: string, open: boolean, onDismiss: () => void): void {
  const onDismissRef = useRef(onDismiss);
  onDismissRef.current = onDismiss;

  useEffect(() => {
    if (!open) return;
    const handlePointer = (e: PointerEvent) => {
      const target = e.target instanceof Element ? e.target : null;
      if (!target?.closest(`[data-dismiss-scope="${scope}"]`)) onDismissRef.current();
    };
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onDismissRef.current();
    };
    document.addEventListener('pointerdown', handlePointer);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('pointerdown', handlePointer);
      document.removeEventListener('keydown', handleKey);
    };
  }, [open, scope]);
}

// ============================================================================
// Account Ring & Live Sidebar Helpers
// ============================================================================

interface AccountRingChipData {
  id: string;
  shortLabel: string;
  fullLabel: string;
  pct: number;
  cu: number;
  tone: QuotaRingTone;
}

function resolveAccountCycleCap(acc: Account): number {
  const tierDefault =
    acc.subscription_tier === 'AI_ULTRA'
      ? 2500
      : acc.subscription_tier === 'PRO_PLUS'
        ? 500
        : 100;
  return Math.max(acc.reference_cycle_cu || tierDefault, acc.paid_cu_balance || 0, 1);
}

function computeAccountPct(acc: Account): number {
  const usable = acc.is_contributor ? acc.schedulable_cu : acc.paid_cu_balance;
  const cap = resolveAccountCycleCap(acc);
  if (cap <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round((usable / cap) * 100)));
}

function deriveAccountRingTone(acc: Account, pct: number): QuotaRingTone {
  if (
    acc.health === 'AUTH_EXPIRED' ||
    acc.health === 'PROXY_ERROR' ||
    acc.health === 'QUARANTINED_DENYLIST' ||
    pct <= 15
  ) {
    return 'coral';
  }
  if (pct <= 38 || acc.expiring_soon_cu > 0) {
    return pct <= 38 ? 'amber' : 'emerald';
  }
  if (acc.subscription_tier === 'FREE') {
    return 'sky';
  }
  return 'emerald';
}

function deriveShortAccountLabel(acc: Account, index: number, zh: boolean, privacyMask: boolean): string {
  if (acc.is_contributor) {
    if (privacyMask) return zh ? '共享' : 'Shared';
    const rawName = acc.alias?.replace(/^Contrib-/i, '').trim();
    if (rawName && rawName.length <= 8) return zh ? `${rawName} 共享` : rawName;
    return zh ? '共享' : 'Shared';
  }
  if (acc.subscription_tier === 'AI_ULTRA') return 'Ultra';
  if (acc.subscription_tier === 'PRO_PLUS') return index > 0 ? `Pro+ #${index + 1}` : 'Pro+';
  if (acc.subscription_tier === 'PRO') return 'Pro';
  if (acc.subscription_tier === 'PAYG') return 'PAYG';
  return 'Free';
}

interface LiveTaskRow {
  id: string;
  title: string;
  subtitle: string;
  gpuTag: string;
  progressText?: string;
  href: string;
  tone: 'emerald' | 'coral' | 'amber';
}

function buildLiveTaskRows(
  experiments: Experiment[],
  sessions: RuntimeSession[],
  accounts: Account[],
  summary: QuotaSummary,
  zh: boolean,
  offlineSnapshot = false,
): LiveTaskRow[] {
  const rows: LiveTaskRow[] = [];

  for (const exp of experiments) {
    if (exp.status === 'RUNNING' || exp.status === 'PROVISIONING') {
      const shardPcts = exp.shards?.map((s) => s.progress_pct ?? 0) ?? [];
      const avgPct =
        shardPcts.length > 0
          ? Math.round(shardPcts.reduce((a, b) => a + b, 0) / shardPcts.length)
          : Math.round((exp.completed_shards / Math.max(1, exp.num_shards)) * 100);
      rows.push({
        id: exp.id,
        title: exp.title || exp.id,
        subtitle: zh
          ? `工作中 · ${exp.running_shards || exp.num_shards} 分片并行 · ${formatCU(exp.aggregate_burn_rate_cu_hr)} CU/h`
          : `Running · ${exp.running_shards || exp.num_shards} shards · ${formatCU(exp.aggregate_burn_rate_cu_hr)} CU/h`,
        gpuTag: `${exp.num_shards}×${exp.accelerator}`,
        progressText: `${avgPct}%`,
        href: `/exp/${encodeURIComponent(exp.id)}`,
        tone: exp.failed_shards > 0 ? 'coral' : 'emerald',
      });
    }
  }

  for (const sess of sessions) {
    if (rows.length >= 3) break;
    if (
      sess.status === 'BUSY' ||
      sess.status === 'PROVISIONING' ||
      sess.status === 'IDLE' ||
      sess.status === 'RECONNECTING'
    ) {
      rows.push({
        id: sess.id,
        title: sess.notebook_name || sess.name || sess.id,
        subtitle: sess.adopted_external
          ? zh
            ? `云端在跑实例（已自动同步） · ${formatCU(sess.burn_rate_cu_hr)} CU/h`
            : `Synced Cloud VM · ${formatCU(sess.burn_rate_cu_hr)} CU/h`
          : zh
            ? `${sess.status === 'BUSY' ? '执行中' : '在线保活'} · Keepalive ${sess.keepalive_healthy ? '正常' : '重连'}`
            : `${sess.status === 'BUSY' ? 'Busy' : 'Keepalive ready'} · ${formatCU(sess.burn_rate_cu_hr)} CU/h`,
        gpuTag: sess.accelerator,
        href: `/exp/${encodeURIComponent(sess.id)}`,
        tone: sess.status === 'BUSY' && !sess.adopted_external ? 'coral' : 'emerald',
      });
    }
  }

  // Fallback derived from accounts' active_session_summary
  if (rows.length === 0 && summary.active_vms > 0) {
    const workingAccounts = accounts.filter((a) => a.active_session_summary || a.active_assignments > 0);
    if (offlineSnapshot && workingAccounts.length > 0) {
      rows.push({
        id: 'live-exp-qwen',
        title: zh ? 'Qwen3-27B 4×G4 评测 · 76%' : 'Qwen3-27B 4×G4 Eval · 76%',
        subtitle: zh
          ? '工作中 · Keepalive 8s · Waste-Zero 调度'
          : 'Running · Keepalive 8s · Waste-Zero routing',
        gpuTag: `${Math.max(2, workingAccounts.length)}×G4`,
        href: '/exp',
        tone: 'coral',
      });
      if (workingAccounts.length >= 2) {
        rows.push({
          id: 'live-sess-a100',
          title: zh ? 'Claude A100 推理沙盒' : 'Claude A100 Eval Sandbox',
          subtitle: zh ? '在线保活 · 显存缓存已固定' : 'Sticky kernel · VRAM cache warm',
          gpuTag: 'A100',
          href: '/exp',
          tone: 'emerald',
        });
      }
    } else if (workingAccounts.length > 0) {
      for (const acc of workingAccounts.slice(0, 2)) {
        rows.push({
          id: `live-acc-${acc.id}`,
          title:
            acc.active_session_summary ||
            (zh
              ? `${acc.alias || acc.email.split('@')[0]} 云端在跑实例`
              : `${acc.alias || acc.email.split('@')[0]} Cloud VM`),
          subtitle: zh
            ? `云端在跑实例 · ${formatCU(acc.live_burn_rate_cu_hr)} CU/h`
            : `Synced Cloud VM · ${formatCU(acc.live_burn_rate_cu_hr)} CU/h`,
          gpuTag: `${acc.active_assignments || 1} GPU`,
          href: '/exp',
          tone: 'emerald',
        });
      }
    }
  }

  return rows.slice(0, 3);
}

interface WatchlistNotice {
  id: string;
  title: string;
  subtitle: string;
  tone: 'coral' | 'amber' | 'emerald';
  href: string;
}

function daysUntilIso(iso?: string): number | null {
  if (!iso) return null;
  const ms = Date.parse(iso) - Date.now();
  if (Number.isNaN(ms)) return null;
  return Math.max(0, Math.ceil(ms / (24 * 3600 * 1000)));
}

function buildWatchlistNotices(
  accounts: Account[],
  zh: boolean,
  privacyMask: boolean,
): WatchlistNotice[] {
  const notices: WatchlistNotice[] = [];

  // 1. Critical health / low balance
  for (let i = 0; i < accounts.length; i += 1) {
    const acc = accounts[i];
    const pct = computeAccountPct(acc);
    const name = deriveShortAccountLabel(acc, i, zh, privacyMask);
    if (acc.health === 'AUTH_EXPIRED' || acc.health === 'PROXY_ERROR') {
      notices.push({
        id: `crit-${acc.id}`,
        title: zh ? `${name} · 凭证或代理需处理` : `${name} · Sign-in / proxy check needed`,
        subtitle: zh ? '点击前往账号池一键修复' : 'Open Account Pool to reconnect',
        tone: 'coral',
        href: '/pool',
      });
    } else if (pct <= 18) {
      notices.push({
        id: `low-${acc.id}`,
        title: zh
          ? `${name} · 仅剩 ${Math.round(acc.schedulable_cu)} CU (${pct}%)`
          : `${name} · ${Math.round(acc.schedulable_cu)} CU left (${pct}%)`,
        subtitle: zh ? '余量偏低，已自动降权至替补席' : 'Low balance, deprioritized automatically',
        tone: 'coral',
        href: '/pool',
      });
    }
  }

  // 2. Expiring batch quota notice (e.g. Pro+ #2 · 118 CU 剩N天到期)
  for (let i = 0; i < accounts.length; i += 1) {
    if (notices.length >= 2) break;
    const acc = accounts[i];
    if (acc.expiring_soon_cu > 0) {
      const days = daysUntilIso(acc.earliest_batch_expires_at) ?? 4;
      const name = deriveShortAccountLabel(acc, i, zh, privacyMask);
      notices.push({
        id: `exp-${acc.id}`,
        title: zh
          ? `${name} · ${Math.round(acc.expiring_soon_cu)} CU 剩${days}天到期`
          : `${name} · ${Math.round(acc.expiring_soon_cu)} CU expires in ${days}d`,
        subtitle: zh
          ? 'Waste-Zero 策略优先消耗防浪费'
          : 'Prioritized by Waste-Zero anti-waste routing',
        tone: days <= 7 ? 'coral' : 'amber',
        href: '/pool',
      });
    }
  }

  // 3. Contributor safety floor notice (e.g. Alice 共享 · 60 CU 保底保护中)
  for (let i = 0; i < accounts.length; i += 1) {
    if (notices.length >= 2) break;
    const acc = accounts[i];
    if (acc.is_contributor) {
      const floor = Math.round(acc.guardrails?.min_reserve_cu ?? 50);
      const name = deriveShortAccountLabel(acc, i, zh, privacyMask);
      notices.push({
        id: `contrib-${acc.id}`,
        title: zh
          ? `${name} · ${floor} CU 保底保护中`
          : `${name} · ${floor} CU reserve floor active`,
        subtitle: zh
          ? '云盘隔离锁死 · 低于保底自动停用'
          : 'Drive isolated · Stops automatically at reserve floor',
        tone: 'emerald',
        href: '/pool',
      });
    }
  }

  // Fallback if custom pool has neither expiring nor contributor accounts
  if (notices.length === 0 && accounts[0]) {
    const first = accounts[0];
    const pct = computeAccountPct(first);
    notices.push({
      id: `ok-${first.id}`,
      title: zh
        ? `主力卡池充裕 · 剩余 ${Math.round(first.schedulable_cu)} CU (${pct}%)`
        : `Primary pool healthy · ${Math.round(first.schedulable_cu)} CU (${pct}%)`,
      subtitle: zh ? '全部账号节奏健康，无临期风险' : 'All pooled accounts are on a healthy pace',
      tone: 'emerald',
      href: '/pool',
    });
  }

  return notices.slice(0, 2);
}

// ============================================================================
// Admin Token Popover Form & Mobile Menu Row
// ============================================================================

interface AdminTokenFormProps {
  zh: boolean;
  draft: string;
  authRequired?: boolean;
  hasToken?: boolean;
  onDraftChange: (value: string) => void;
  onSubmit: (e: React.FormEvent) => void;
  onLock?: () => void;
  onCancel: () => void;
  saveLabel: string;
  cancelLabel: string;
}

function AdminTokenForm({
  zh,
  draft,
  authRequired,
  hasToken,
  onDraftChange,
  onSubmit,
  onLock,
  onCancel,
  saveLabel,
  cancelLabel,
}: AdminTokenFormProps) {
  return (
    <form onSubmit={onSubmit} className="space-y-2.5">
      <div>
        <div className="text-[12.5px] font-semibold text-fg">
          {authRequired
            ? zh
              ? '管理员访问口令'
              : 'Admin Access Token'
            : zh
              ? '管理员访问口令（可选）'
              : 'Admin access token (optional)'}
        </div>
        <p className="mt-0.5 text-[11.5px] leading-relaxed text-fg-muted">
          {authRequired
            ? zh
              ? '当前服务已开启访问保护，可在此更换口令或立即锁定控制台。'
              : 'Access protection is active. Update your token or lock the console below.'
            : zh
              ? '如果本地服务设置了访问口令，在这里填写。只在自己电脑上用可以留空。'
              : 'Enter it only if your local service requires one. Leave empty on your own machine.'}
        </p>
      </div>
      <input
        type="password"
        value={draft}
        onChange={(e) => onDraftChange(e.target.value)}
        placeholder={zh ? '粘贴管理员口令' : 'Paste admin token'}
        autoComplete="off"
        className="ui-input ch-num"
      />
      <div className="flex items-center justify-between gap-1.5 pt-0.5">
        {(authRequired || hasToken) && onLock ? (
          <ActionButton
            variant="ghost"
            size="xs"
            icon={<LogOut />}
            onClick={onLock}
            style={{ color: 'var(--coral-text)' }}
          >
            {zh ? '退出登录 / 锁定' : 'Lock / Sign Out'}
          </ActionButton>
        ) : (
          <span />
        )}
        <div className="flex items-center gap-1.5">
          <ActionButton variant="ghost" onClick={onCancel}>
            {cancelLabel}
          </ActionButton>
          <ActionButton variant="primary" type="submit">
            {saveLabel}
          </ActionButton>
        </div>
      </div>
    </form>
  );
}

// ============================================================================
// Admin Access Gate (`ColabHive 管理控制台验证` — Warm-Neutral Apple Studio Card)
// ============================================================================

function AdminAccessGateScreen({
  zh,
  theme,
  health,
  languageSwitchTitle,
  themeSwitchTitle,
  onToggleLocale,
  onToggleTheme,
  onUnlocked,
}: {
  zh: boolean;
  theme: 'light' | 'dark';
  health: DaemonHealthStatus;
  languageSwitchTitle: string;
  themeSwitchTitle: string;
  onToggleLocale: () => void;
  onToggleTheme: () => void;
  onUnlocked: (verifiedToken: string) => void;
}) {
  const [tokenInput, setTokenInput] = useState('');
  const [showToken, setShowToken] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    const raf = window.requestAnimationFrame(() => inputRef.current?.focus());
    return () => window.cancelAnimationFrame(raf);
  }, []);

  const isCloud = health.deployment_mode === 'cloud';

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const cleaned = tokenInput.trim();
    if (!cleaned) {
      setErrorMsg(
        zh ? '请输入管理员访问口令 (COLABHIVE_ADMIN_TOKEN)' : 'Please enter your COLABHIVE_ADMIN_TOKEN',
      );
      return;
    }
    if (cleaned.startsWith('ch_sk_')) {
      setErrorMsg(
        zh
          ? '这是 AI 助手专用受限密钥 (ch_sk_…)，仅用于远程 MCP 算力调用；管理控制台请输入管理员口令 (COLABHIVE_ADMIN_TOKEN)'
          : 'This is a scoped ch_sk_… agent key for MCP compute calls. Enter COLABHIVE_ADMIN_TOKEN to unlock the admin console.',
      );
      return;
    }

    setVerifying(true);
    setErrorMsg(null);
    try {
      await api.verifyAdminToken(cleaned);
      setStoredAdminToken(cleaned);
      onUnlocked(cleaned);
    } catch (err) {
      if (err instanceof APIError) {
        if (err.status === 429) {
          setErrorMsg(
            zh
              ? '验证尝试过于频繁，已触发防爆破保护，请稍候约 60 秒再试'
              : 'Too many failed attempts. Temporarily rate-limited — please retry in 60 seconds.',
          );
        } else if (err.status === 401 || err.status === 403) {
          setErrorMsg(
            zh
              ? '管理员访问口令无效，请核对启动参数 --admin-token 或环境变量 COLABHIVE_ADMIN_TOKEN'
              : 'Invalid admin token. Verify --admin-token or COLABHIVE_ADMIN_TOKEN on your server.',
          );
        } else {
          setErrorMsg(err.message);
        }
      } else {
        setErrorMsg(
          zh ? '无法连接服务端进行口令校验，请检查网络后重试' : 'Could not reach server to verify token.',
        );
      }
    } finally {
      setVerifying(false);
    }
  };

  return (
    <div
      className="min-h-dvh flex flex-col text-fg"
      style={{ backgroundColor: 'var(--surface-0)' }}
    >
      <header
        data-colabhive-drag
        className="h-14 px-5 sm:px-7 flex items-center justify-between gap-3 shrink-0"
      >
        <div className="flex items-center gap-2.5 min-w-0">
          <BrandMark />
          <span className="ui-title text-[15px] font-semibold tracking-tight">ColabHive</span>
        </div>

        <div className="flex items-center gap-1.5 shrink-0">
          <ActionButton
            variant="ghost"
            icon={<Globe />}
            onClick={onToggleLocale}
            title={languageSwitchTitle}
          >
            {zh ? '中文' : 'EN'}
          </ActionButton>
          <IconButton label={themeSwitchTitle} onClick={onToggleTheme}>
            {theme === 'dark' ? <Moon className="w-4 h-4" /> : <Sun className="w-4 h-4" />}
          </IconButton>
          <WindowsCaptionButtons zh={zh} />
        </div>
      </header>

      <main className="flex-1 flex items-center justify-center px-4 py-8">
        <div
          className="w-full max-w-[432px] rounded-[22px] border p-6 sm:p-7 space-y-5"
          style={{
            backgroundColor: 'var(--surface-2)',
            borderColor: 'var(--border-subtle)',
            boxShadow: 'var(--shadow-card)',
          }}
        >
          <div className="space-y-3">
            <div className="flex items-center justify-between gap-2">
              <span
                className="inline-flex items-center justify-center w-10 h-10 rounded-[13px]"
                style={{
                  backgroundColor: 'var(--brand-bg)',
                  boxShadow: '0 2px 8px rgba(0, 0, 0, 0.14)',
                }}
              >
                <Lock className="w-[18px] h-[18px] text-white" />
              </span>

              <StatusPill
                tone={isCloud ? 'sky' : 'emerald'}
                size="sm"
                icon={isCloud ? <Cloud /> : <ShieldCheck />}
              >
                {isCloud
                  ? zh
                    ? '云端部署模式 · Cloud Server'
                    : 'Cloud Server Mode'
                  : zh
                    ? '本地安全模式'
                    : 'Local Protected Mode'}
              </StatusPill>
            </div>

            <div className="space-y-1">
              <h1 className="text-[19px] font-bold tracking-tight text-fg">
                {zh ? 'ColabHive 管理控制台验证' : 'ColabHive Admin Access Gate'}
              </h1>
              <p className="text-[12.5px] leading-relaxed text-fg-secondary">
                {isCloud
                  ? zh
                    ? '当前运行于云端安全隔离模式。请输入管理员口令 (COLABHIVE_ADMIN_TOKEN) 解锁账号池、保险箱与路由策略控制台。'
                    : 'Running in hardened Cloud Server mode. Enter COLABHIVE_ADMIN_TOKEN to unlock the account pool, vault, and router console.'
                  : zh
                    ? '当前节点已配置管理员访问保护。验证口令后即可管理 Google 算力池与 AI 路由配置。'
                    : 'This node requires administrator authentication. Verify your token to manage pooled accounts and routing.'}
              </p>
            </div>
          </div>

          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-1.5">
              <label
                htmlFor="colabhive-admin-gate-token"
                className="flex items-center justify-between text-[12px] font-semibold text-fg-secondary"
              >
                <span>{zh ? '管理员访问口令' : 'Admin Bearer Token'}</span>
                <span className="ch-num font-normal text-[11px] text-fg-muted">
                  COLABHIVE_ADMIN_TOKEN
                </span>
              </label>

              <div className="relative">
                <input
                  ref={inputRef}
                  id="colabhive-admin-gate-token"
                  type={showToken ? 'text' : 'password'}
                  value={tokenInput}
                  onChange={(e) => {
                    setTokenInput(e.target.value);
                    if (errorMsg) setErrorMsg(null);
                  }}
                  placeholder={zh ? '输入或粘贴管理员口令…' : 'Enter admin token…'}
                  autoComplete="current-password"
                  spellCheck={false}
                  className="ui-input ch-num h-10 pr-10 text-[13px] w-full"
                />
                <button
                  type="button"
                  onClick={() => setShowToken((v) => !v)}
                  aria-label={
                    showToken
                      ? zh
                        ? '隐藏口令'
                        : 'Hide token'
                      : zh
                        ? '显示口令'
                        : 'Show token'
                  }
                  title={
                    showToken
                      ? zh
                        ? '隐藏口令'
                        : 'Hide token'
                      : zh
                        ? '显示口令'
                        : 'Show token'
                  }
                  className="absolute right-2 top-1/2 -translate-y-1/2 inline-flex items-center justify-center w-7 h-7 rounded-lg text-fg-muted hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer bg-transparent border-0"
                >
                  {showToken ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
              </div>
            </div>

            {errorMsg && (
              <div
                role="alert"
                data-tone="coral"
                className="flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[12px] leading-relaxed"
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

            <ActionButton
              variant="primary"
              size="md"
              type="submit"
              fullWidth
              loading={verifying}
              icon={<ShieldCheck />}
            >
              {zh ? '验证并进入控制台' : 'Verify & Unlock Console'}
            </ActionButton>
          </form>

          <div
            className="pt-3.5 border-t flex items-center justify-between gap-2 text-[11px] text-fg-muted"
            style={{ borderColor: 'var(--border-faint)' }}
          >
            <span className="inline-flex items-center gap-1.5">
              <ShieldCheck className="w-3.5 h-3.5 shrink-0 text-[var(--emerald-text)]" />
              {zh
                ? '口令仅保存在当前浏览器本地 · AES-256-GCM 保险箱'
                : 'Stored locally in browser · AES-256-GCM vault'}
            </span>
            <span className="ch-num shrink-0">v{health.version || '1.0.0'}</span>
          </div>
        </div>
      </main>
    </div>
  );
}

interface MenuRowProps {
  icon: React.ReactNode;
  label: React.ReactNode;
  value?: React.ReactNode;
  active?: boolean;
  onClick?: () => void;
  href?: string;
}

function MenuRow({ icon, label, value, active, onClick, href }: MenuRowProps) {
  const className =
    'flex items-center gap-2.5 w-full h-9 px-2.5 rounded-lg text-left text-[13px] no-underline cursor-pointer text-fg transition-colors hover:bg-[var(--hover-fill)]';
  const inner = (
    <>
      <span
        className="shrink-0 [&>svg]:w-4 [&>svg]:h-4"
        style={{ color: active ? 'var(--emerald-text)' : 'var(--text-secondary)' }}
      >
        {icon}
      </span>
      <span className="flex-1 truncate">{label}</span>
      {value != null && <span className="shrink-0 text-[12px] text-fg-muted">{value}</span>}
    </>
  );
  if (href) {
    return (
      <a href={href} onClick={onClick} className={className}>
        {inner}
      </a>
    );
  }
  return (
    <button type="button" onClick={onClick} className={className}>
      {inner}
    </button>
  );
}

// ============================================================================
// Quick-Jump Palette Modal (`⌘K`)
// ============================================================================

interface QuickJumpItem {
  id: string;
  group: string;
  title: string;
  subtitle: string;
  badge?: React.ReactNode;
  icon: React.ReactNode;
  onSelect: () => void;
}

function QuickSearchModal({
  open,
  onClose,
  zh,
  placeholder,
  items,
}: {
  open: boolean;
  onClose: () => void;
  zh: boolean;
  placeholder: string;
  items: QuickJumpItem[];
}) {
  const [query, setQuery] = useState('');
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (!open) return;
    setQuery('');
    setActiveIndex(0);
    const frame = window.requestAnimationFrame(() => inputRef.current?.focus());
    return () => window.cancelAnimationFrame(frame);
  }, [open]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items.filter(
      (item) =>
        item.title.toLowerCase().includes(q) ||
        item.subtitle.toLowerCase().includes(q) ||
        item.group.toLowerCase().includes(q),
    );
  }, [items, query]);

  useEffect(() => {
    setActiveIndex(0);
  }, [query]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (filtered.length === 0) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActiveIndex((prev) => (prev + 1) % filtered.length);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActiveIndex((prev) => (prev - 1 + filtered.length) % filtered.length);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const picked = filtered[activeIndex] ?? filtered[0];
      if (picked) {
        onClose();
        picked.onSelect();
      }
    }
  };

  return (
    <DialogModal
      open={open}
      onClose={onClose}
      size="lg"
      icon={<Search />}
      title={zh ? '快速跳转与全局搜索' : 'Quick Jump & Search'}
      subtitle={
        zh
          ? '搜索页面、Colab 账号余额、运行中实验或一键接入 AI 助手'
          : 'Jump to routes, pooled accounts, live jobs, or AI tools'
      }
      className="space-y-3"
    >
      <div className="relative" onKeyDown={handleKeyDown}>
        <Search className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-fg-muted" />
        <input
          ref={inputRef}
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={placeholder}
          className="ui-input h-10 pl-9 pr-14 text-[13.5px]"
        />
        <kbd className="ch-num pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 px-1.5 py-0.5 rounded text-[10.5px] text-fg-muted border border-line-faint bg-surface-1">
          ESC
        </kbd>
      </div>

      {filtered.length === 0 ? (
        <div className="py-8 text-center text-[13px] text-fg-muted">
          {zh ? '没有找到匹配的页面、账号或实验' : 'No matching routes, accounts, or experiments'}
        </div>
      ) : (
        <div className="max-h-[340px] overflow-y-auto space-y-1 pr-0.5 pb-2">
          {filtered.map((item, idx) => {
            const active = idx === activeIndex;
            return (
              <button
                key={item.id}
                type="button"
                onMouseEnter={() => setActiveIndex(idx)}
                onClick={() => {
                  onClose();
                  item.onSelect();
                }}
                className={cn(
                  'flex items-center gap-3 w-full px-3 py-2.5 rounded-xl text-left cursor-pointer transition-colors',
                  active ? 'bg-surface-1 text-fg' : 'text-fg-secondary hover:bg-[var(--hover-fill)]',
                )}
              >
                <span className="flex items-center justify-center w-8 h-8 rounded-lg shrink-0 bg-surface-2 border border-line text-fg [&>svg]:w-4 [&>svg]:h-4">
                  {item.icon}
                </span>
                <span className="flex-1 min-w-0">
                  <span className="flex items-center gap-2">
                    <span className="truncate text-[13.5px] font-semibold text-fg">
                      {item.title}
                    </span>
                    <span className="shrink-0 text-[10.5px] px-1.5 py-0.5 rounded-full bg-[var(--neutral-soft)] text-fg-muted">
                      {item.group}
                    </span>
                  </span>
                  <span className="block truncate text-[11.5px] text-fg-muted">
                    {item.subtitle}
                  </span>
                </span>
                {item.badge != null && <span className="shrink-0">{item.badge}</span>}
                <CornerDownLeft
                  className={cn(
                    'w-3.5 h-3.5 shrink-0 transition-opacity',
                    active ? 'opacity-75 text-fg' : 'opacity-0',
                  )}
                />
              </button>
            );
          })}
        </div>
      )}
    </DialogModal>
  );
}

// ============================================================================
// Flat Sidebar Navigation Definitions (Equal-Weight 14px Primary Rows!)
// ============================================================================

interface FlatNavItem {
  key: string;
  section?: AppSection;
  href?: string;
  onClick?: () => void;
  label: string;
  icon: React.ComponentType<{ className?: string; style?: React.CSSProperties }>;
  trailing?: React.ReactNode;
  liveDot?: 'emerald' | 'coral';
  externalHint?: boolean;
}

export function AppShell({
  route,
  summary,
  accounts: accountsProp,
  experiments = [],
  sessions = [],
  health,
  wsState,
  offlineSnapshot,
  onRefreshTelemetry,
  onOpenConnectDrawer,
  children,
}: AppShellProps) {
  const {
    locale,
    toggleLocale,
    theme,
    toggleTheme,
    privacyMask,
    togglePrivacyMask,
    displayEmail,
    t,
  } = useI18n();
  const zh = locale === 'zh-CN';

  const accounts = accountsProp ?? (offlineSnapshot ? DEMO_ACCOUNTS : []);

  const [collapsed, setCollapsed] = useState<boolean>(readSidebarCollapsed);
  const [dismissedAlerts, setDismissedAlerts] = useState<string[]>(readDismissedAlerts);
  const [searchOpen, setSearchOpen] = useState(false);
  const [shareModalOpen, setShareModalOpen] = useState(false);
  const [authPopoverOpen, setAuthPopoverOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [mobileTokenOpen, setMobileTokenOpen] = useState(false);
  const [adminTokenDraft, setAdminTokenDraft] = useState<string>(
    () => getStoredAdminToken() || '',
  );
  const [hasAdminToken, setHasAdminToken] = useState<boolean>(() => Boolean(getStoredAdminToken()));
  const [authGateLocked, setAuthGateLocked] = useState<boolean>(false);
  const [retrying, setRetrying] = useState(false);

  useEffect(() => {
    const handleUnauthorized = () => {
      setAuthGateLocked(true);
    };
    const handleTokenChanged = () => {
      const tok = getStoredAdminToken();
      setHasAdminToken(Boolean(tok));
      setAdminTokenDraft(tok || '');
      if (tok) {
        setAuthGateLocked(false);
      }
    };
    window.addEventListener(UNAUTHORIZED_EVENT, handleUnauthorized);
    window.addEventListener(ADMIN_TOKEN_CHANGED_EVENT, handleTokenChanged);
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, handleUnauthorized);
      window.removeEventListener(ADMIN_TOKEN_CHANGED_EVENT, handleTokenChanged);
    };
  }, []);

  useEffect(() => {
    try {
      localStorage.setItem(SIDEBAR_COLLAPSED_KEY, String(collapsed));
    } catch {
      // Ignore storage access errors in private browsing contexts
    }
  }, [collapsed]);

  useEffect(() => {
    try {
      localStorage.setItem(DISMISSED_ALERTS_KEY, JSON.stringify(dismissedAlerts));
    } catch {
      // Ignore storage access errors in private browsing contexts
    }
  }, [dismissedAlerts]);

  const dismissAlert = useCallback((alertId: string) => {
    setDismissedAlerts((prev) => (prev.includes(alertId) ? prev : [...prev, alertId]));
  }, []);

  useEffect(() => {
    if (!retrying) return;
    const timer = window.setTimeout(() => setRetrying(false), 900);
    return () => window.clearTimeout(timer);
  }, [retrying]);

  // Global Cmd+K / Ctrl+K shortcut for the Quick-Jump Search Palette
  useEffect(() => {
    if (route.isPublicPortal) return;
    const handleKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setSearchOpen((prev) => !prev);
      }
    };
    window.addEventListener('keydown', handleKey);
    return () => window.removeEventListener('keydown', handleKey);
  }, [route.isPublicPortal]);

  // Zero-flash theme switch
  const themeSwitchTimerRef = useRef<number | null>(null);
  const themeSwitchRafRef = useRef<number | null>(null);

  const cancelThemeSwitchRelease = useCallback(() => {
    if (themeSwitchTimerRef.current !== null) {
      window.clearTimeout(themeSwitchTimerRef.current);
      themeSwitchTimerRef.current = null;
    }
    if (themeSwitchRafRef.current !== null) {
      window.cancelAnimationFrame(themeSwitchRafRef.current);
      themeSwitchRafRef.current = null;
    }
  }, []);

  const handleToggleTheme = useCallback(() => {
    const root = document.documentElement;
    cancelThemeSwitchRelease();

    root.setAttribute('data-theme-switching', '');
    root.setAttribute('data-theme', theme === 'dark' ? 'light' : 'dark');
    toggleTheme();
    void root.offsetHeight;

    const release = () => {
      cancelThemeSwitchRelease();
      void root.offsetHeight;
      root.removeAttribute('data-theme-switching');
    };

    themeSwitchRafRef.current = window.requestAnimationFrame(() => {
      themeSwitchRafRef.current = window.requestAnimationFrame(release);
    });
    themeSwitchTimerRef.current = window.setTimeout(release, 80);
  }, [theme, toggleTheme, cancelThemeSwitchRelease]);

  useEffect(() => {
    const root = document.documentElement;
    if (root.hasAttribute('data-theme-switching')) {
      void root.offsetHeight;
    }
  }, [theme]);

  useEffect(
    () => () => {
      cancelThemeSwitchRelease();
      document.documentElement.removeAttribute('data-theme-switching');
    },
    [cancelThemeSwitchRelease],
  );

  useDismiss('admin-token', authPopoverOpen, () => setAuthPopoverOpen(false));
  useDismiss('mobile-menu', mobileMenuOpen, () => {
    setMobileMenuOpen(false);
    setMobileTokenOpen(false);
  });

  const themeLabel = theme === 'dark' ? (zh ? '深色' : 'Dark') : zh ? '浅色' : 'Light';
  const themeSwitchTitle =
    theme === 'dark'
      ? zh
        ? `切换到${t('nav.theme_light')}`
        : `Switch to ${t('nav.theme_light')}`
      : zh
        ? `切换到${t('nav.theme_dark')}`
        : `Switch to ${t('nav.theme_dark')}`;
  const languageSwitchTitle = zh ? 'Switch to English' : '切换到中文';

  // ==========================================================================
  // Mode 1: Public friend-share portal — minimal chrome
  // ==========================================================================
  const observatoryHref = getObservatoryHomePath();
  const winControls = useNativeWindowControls();
  const macTrafficInset =
    winControls.frameless && winControls.platform === 'macos'
      ? winControls.trafficLightInset || 76
      : 0;

  if (route.isPublicPortal) {
    return (
      <div className="min-h-dvh flex flex-col">
        <header
          data-colabhive-drag
          className="ui-chrome ui-chrome-top sticky top-0 z-30 h-14 px-4 sm:px-6 flex items-center justify-between gap-3"
          style={macTrafficInset > 0 ? { paddingLeft: macTrafficInset + 12 } : undefined}
        >
          <div className="flex items-center gap-3 min-w-0">
            <BrandMark />
            <span className="ui-title text-[15px] font-semibold">{t('app.name')}</span>
            {IS_STATIC_DEMO_BUILD && (
              <button
                type="button"
                onClick={() => route.navigate('/')}
                className="ui-btn ui-btn-secondary ui-btn-xs"
              >
                <ArrowLeft className="w-3 h-3" />
                <span>{zh ? '返回产品主页' : 'Product Home'}</span>
              </button>
            )}
            <StatusPill
              tone="emerald"
              icon={<ShieldCheck />}
              className="hidden sm:inline-flex"
            >
              {zh ? '不碰你的云盘 · 安全共享页' : 'No Drive Access · Safe Share Portal'}
            </StatusPill>
          </div>

          <div className="flex items-center gap-1 shrink-0">
            <ActionButton
              variant="ghost"
              icon={<Globe />}
              onClick={toggleLocale}
              title={languageSwitchTitle}
            >
              {zh ? '中文' : 'EN'}
            </ActionButton>
            <IconButton label={themeSwitchTitle} onClick={handleToggleTheme}>
              {theme === 'dark' ? <Moon className="w-4 h-4" /> : <Sun className="w-4 h-4" />}
            </IconButton>
            <WindowsCaptionButtons zh={zh} />
          </div>
        </header>

        <main className="flex-1 w-full max-w-4xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
          {children}
        </main>

        <footer className="w-full max-w-4xl mx-auto px-4 sm:px-6 py-6 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2 text-[11.5px] text-fg-muted">
          <span className="inline-flex items-center gap-1.5">
            <Lock className="w-3 h-3 shrink-0" />
            {zh
              ? '登录凭证 AES-256-GCM 加密保存 · 运行环境拿不到你的 Google 凭据 · 随时可自助收回'
              : 'AES-256-GCM encrypted sign-in data · Runtimes cannot read your Google credentials · Take it back anytime'}
          </span>
          <a
            href={observatoryHref}
            className="inline-flex items-center gap-1 no-underline text-fg-secondary transition-colors hover:text-fg"
          >
            {zh ? '打开控制台' : 'Open console'}
            <ArrowUpRight className="w-3 h-3" />
          </a>
        </footer>
      </div>
    );
  }

  // ==========================================================================
  // Mode 1b: Admin Access Gate when `health.auth_required` is active
  // ==========================================================================
  const handleLockConsole = () => {
    clearStoredAdminToken();
    setHasAdminToken(false);
    setAdminTokenDraft('');
    setAuthPopoverOpen(false);
    setMobileMenuOpen(false);
    setMobileTokenOpen(false);
    setAuthGateLocked(true);
  };

  if (!offlineSnapshot && health?.auth_required && (!hasAdminToken || authGateLocked)) {
    return (
      <AdminAccessGateScreen
        zh={zh}
        theme={theme}
        health={health}
        languageSwitchTitle={languageSwitchTitle}
        themeSwitchTitle={themeSwitchTitle}
        onToggleLocale={toggleLocale}
        onToggleTheme={handleToggleTheme}
        onUnlocked={(verifiedToken) => {
          setHasAdminToken(true);
          setAdminTokenDraft(verifiedToken);
          setAuthGateLocked(false);
          onRefreshTelemetry();
        }}
      />
    );
  }

  // ==========================================================================
  // Mode 2: Console Workspace — Structured Sidebar + Top Ring Header
  // ==========================================================================
  const isBurning = summary.active_burn_rate_cu_hr > 0;
  const poolCuText = `${Math.round(summary.total_available_cu).toLocaleString('en-US')} CU`;
  const activeLabel = `${summary.active_vms} ${zh ? '运行中' : 'Running'}`;

  // Top 3 pooled account ring chips (used in Top Header Right Strip & Sidebar Bottom)
  const accountRingChips: AccountRingChipData[] = accounts.slice(0, 3).map((acc, idx) => {
    const pct = computeAccountPct(acc);
    const cap = resolveAccountCycleCap(acc);
    const usable = acc.is_contributor ? acc.schedulable_cu : acc.paid_cu_balance;
    const tone = deriveAccountRingTone(acc, pct);
    return {
      id: acc.id,
      shortLabel: deriveShortAccountLabel(acc, idx, zh, privacyMask),
      fullLabel: `${acc.alias || displayEmail(acc.email)} · ${formatCU(usable)} / ${formatCU(cap, 0)} CU (${pct}%)`,
      pct,
      cu: usable,
      tone,
    };
  });

  // Live Tasks (`运行中`) & Quota Watchlist (`额度提醒`)
  const liveTaskRows = buildLiveTaskRows(experiments, sessions, accounts, summary, zh, offlineSnapshot);
  const watchlistNotices = buildWatchlistNotices(accounts, zh, privacyMask);
  const visibleAlerts = watchlistNotices.filter((n) => !dismissedAlerts.includes(n.id));

  const dismissAllAlerts = () => {
    setDismissedAlerts((prev) => {
      const next = new Set(prev);
      for (const notice of watchlistNotices) {
        next.add(notice.id);
      }
      return Array.from(next);
    });
  };

  const closeMobileMenu = () => {
    setMobileMenuOpen(false);
    setMobileTokenOpen(false);
  };

  const handleOpenGuide = () => {
    closeMobileMenu();
    openQuickStartGuide({ navigate: route.navigate });
  };

  // Flat, equal-weight 14px grouped navigation (ZERO nested tiny secondary sub-menus!)
  const workspaceItems: FlatNavItem[] = [
    {
      key: 'observatory',
      section: 'observatory',
      href: observatoryHref,
      label: t('nav.observatory'),
      icon: Gauge,
      trailing: (
        <AnimatedNumber
          value={summary.total_available_cu}
          suffix=" CU"
          className="text-[11px] text-fg-muted"
        />
      ),
    },
    {
      key: 'pool',
      section: 'pool',
      href: '/pool',
      label: t('nav.pool'),
      icon: Users,
      trailing: (
        <span className="ch-num text-[11px] text-fg-muted">
          {summary.accounts_healthy}/{summary.accounts_total}
        </span>
      ),
    },
    {
      key: 'workbench',
      section: 'workbench',
      href: '/exp',
      label: t('nav.workbench'),
      icon: TerminalSquare,
      liveDot: summary.active_vms > 0 ? (isBurning ? 'coral' : 'emerald') : undefined,
      trailing:
        summary.active_vms > 0 ? (
          <span className="ch-num text-[11px] font-medium text-fg-secondary">
            {summary.active_vms} {zh ? '卡' : 'GPU'}
          </span>
        ) : undefined,
    },
  ];

  const systemItems: FlatNavItem[] = [
    {
      key: 'router',
      section: 'router',
      href: '/router',
      label: t('nav.router'),
      icon: Route,
      trailing: <span className="ch-num text-[10.5px] text-fg-muted">MCP</span>,
    },
    {
      key: 'settings',
      section: 'settings',
      href: '/cfg',
      label: t('nav.settings'),
      icon: KeyRound,
      trailing: <span className="ch-num text-[10.5px] text-fg-muted">AES</span>,
    },
    {
      key: 'quick_guide',
      onClick: handleOpenGuide,
      label: t('nav.quick_guide'),
      icon: Sparkles,
      trailing: (
        <span className="px-1.5 py-0.5 rounded-full text-[10.5px] font-medium bg-[var(--neutral-soft)] text-fg-secondary">
          {zh ? '3 步' : '3 steps'}
        </span>
      ),
    },
    {
      key: 'contribute',
      href: '/contribute',
      label: t('nav.contribute'),
      icon: HeartHandshake,
      externalHint: true,
    },
  ];

  const allRouteNavItems = [...workspaceItems, ...systemItems];
  const activeNavItem = allRouteNavItems.find(
    (item) => item.section && item.section === route.section,
  );
  const currentGroupLabel = workspaceItems.some((item) => item.section === route.section)
    ? t('nav.group_workspace')
    : t('nav.group_system');
  const currentPageLabel = activeNavItem?.label ?? t('app.name');

  // Quick-jump items for Cmd+K modal
  const quickJumpItems: QuickJumpItem[] = [
    ...workspaceItems
      .filter((i) => i.href)
      .map((i) => {
        const Icon = i.icon;
        return {
          id: `nav-${i.key}`,
          group: t('nav.group_workspace'),
          title: i.label,
          subtitle: i.href!,
          icon: <Icon />,
          onSelect: () => route.navigate(i.href!),
        };
      }),
    ...systemItems.map((i) => {
      const Icon = i.icon;
      return {
        id: `sys-${i.key}`,
        group: t('nav.group_system'),
        title: i.label,
        subtitle: i.href ?? t('nav.quick_guide_hint'),
        icon: <Icon />,
        onSelect: () => {
          if (i.onClick) i.onClick();
          else if (i.href) route.navigate(i.href);
        },
      };
    }),
    ...accounts.map((acc, idx) => {
      const pct = computeAccountPct(acc);
      const tone = deriveAccountRingTone(acc, pct);
      return {
        id: `acc-${acc.id}`,
        group: zh ? '账号池' : 'Account',
        title: acc.alias || displayEmail(acc.email),
        subtitle: `${acc.subscription_tier} · ${formatCU(acc.schedulable_cu)} CU ${
          zh ? '可用' : 'usable'
        }`,
        badge: <CircularQuotaRing value={pct} size={28} strokeWidth={2.8} tone={tone} />,
        icon: <Users />,
        onSelect: () => route.navigate('/pool'),
      };
    }),
    ...liveTaskRows.map((task) => ({
      id: `task-${task.id}`,
      group: t('nav.live_tasks'),
      title: task.title,
      subtitle: task.subtitle,
      badge: (
        <span className="ch-tier-tag text-[10.5px] font-semibold text-fg">{task.gpuTag}</span>
      ),
      icon: <TerminalSquare />,
      onSelect: () => route.navigate(task.href),
    })),
    {
      id: 'tool-connect-ai',
      group: 'AI MCP',
      title: zh ? '一键接入 Antigravity / Claude Code / Codex / Cursor' : '1-Click Connect AI Coding Assistants',
      subtitle: zh ? '打开右侧 MCP 自动写入抽屉' : 'Open 1-Click MCP config sync drawer',
      icon: <Zap />,
      onSelect: () => onOpenConnectDrawer(),
    },
  ];

  // --- Local daemon health status --------------------------------------------
  const healthState: 'ready' | 'preview' | 'degraded' = offlineSnapshot
    ? 'preview'
    : health.healthy
      ? 'ready'
      : 'degraded';
  const healthColor =
    healthState === 'ready'
      ? 'var(--emerald)'
      : healthState === 'preview'
        ? 'var(--amber)'
        : 'var(--coral)';
  const healthLabel =
    healthState === 'ready'
      ? t('nav.status_connected')
      : healthState === 'preview'
        ? t('nav.status_demo')
        : t('nav.status_degraded');
  const latencyLabel = wsState.latencyMs ? `· ${wsState.latencyMs}ms` : '';
  const wsLabel = wsState.connected
    ? `${zh ? '已连接' : 'Connected'}${wsState.latencyMs ? ` · ${wsState.latencyMs}ms` : ''}`
    : wsState.reconnecting
      ? zh
        ? '重连中…'
        : 'Reconnecting…'
      : zh
        ? '待命'
        : 'Standby';

  const healthRows: { label: string; value: string }[] = [
    { label: zh ? '版本' : 'Version', value: `v${health.version || '1.0'}` },
    { label: zh ? '防掉线保活' : 'Keep-alive', value: `${health.keepalive_loop_sec || 8}s` },
    {
      label: zh ? '卡死任务清理' : 'Stuck-job cleanup',
      value: health.anti_zombie_armed ? (zh ? '已开启' : 'On') : zh ? '待命' : 'Standby',
    },
    {
      label: zh ? '好友账号保护' : 'Friend account protection',
      value: health.contributor_firewall_on ? (zh ? '已开启' : 'On') : zh ? '未开启' : 'Off',
    },
    { label: zh ? '登录凭证加密' : 'Sign-in encryption', value: 'AES-256-GCM' },
    { label: zh ? '实时连接' : 'Live link', value: wsLabel },
  ];

  const handleRetrySync = () => {
    setRetrying(true);
    onRefreshTelemetry();
  };

  const handleSaveAdminToken = (e: React.FormEvent) => {
    e.preventDefault();
    const token = adminTokenDraft.trim();
    setStoredAdminToken(token || null);
    setHasAdminToken(Boolean(token));
    setAuthPopoverOpen(false);
    setMobileTokenOpen(false);
    onRefreshTelemetry();
  };

  const healthDetails = (
    <div
      role="tooltip"
      className={cn(
        'ui-float ui-float-pop pointer-events-none absolute z-50 w-64 rounded-xl p-3',
        'invisible opacity-0 transition-opacity duration-150',
        'group-hover/health:visible group-hover/health:opacity-100',
        'group-focus-within/health:visible group-focus-within/health:opacity-100',
        collapsed ? 'left-full bottom-0 ml-3' : 'left-0 bottom-full mb-2',
      )}
    >
      <div className="flex items-center gap-2 text-[12.5px] font-semibold text-fg">
        <span className="w-1.5 h-1.5 rounded-full" style={{ backgroundColor: healthColor }} />
        {healthLabel}
      </div>
      {healthState === 'preview' && (
        <p className="mt-1.5 text-[11.5px] leading-relaxed text-fg-secondary">
          {zh
            ? '还没连上本地 ColabHive 服务（默认端口 7860），现在看到的是演示数据。点击可重试连接。'
            : 'The local ColabHive service (default port 7860) is not reachable, so demo data is shown. Click to retry.'}
        </p>
      )}
      <dl className="mt-2.5 space-y-1.5">
        {healthRows.map((row) => (
          <div key={row.label} className="flex items-center justify-between gap-3 text-[11.5px]">
            <dt className="text-fg-muted">{row.label}</dt>
            <dd className="ch-num text-fg-secondary">{row.value}</dd>
          </div>
        ))}
      </dl>
    </div>
  );

  const privacyTitle = zh
    ? '一键遮住所有邮箱、代理 IP 和密钥，截图分享更安心'
    : 'Hide every email, proxy IP and secret so screenshots are safe to share';

  const renderFlatNavRow = (item: FlatNavItem) => {
    const Icon = item.icon;
    const active = Boolean(item.section && item.section === route.section);
    const rowClass = cn(
      'ui-nav-zone w-full text-left',
      collapsed ? 'justify-center h-9' : 'gap-2.5 h-9 px-2.5',
    );

    const inner = (
      <>
        <span className="ui-nav-zone-icon relative">
          {active && !collapsed ? (
            <span
              className="w-2 h-2 rounded-full"
              style={{
                backgroundColor:
                  item.liveDot === 'coral' ? 'var(--coral)' : 'var(--emerald)',
              }}
            />
          ) : (
            <Icon className="w-[15px] h-[15px]" />
          )}
          {collapsed && item.liveDot && (
            <span
              className="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full"
              style={{
                backgroundColor:
                  item.liveDot === 'coral' ? 'var(--coral)' : 'var(--emerald)',
              }}
            />
          )}
        </span>
        {!collapsed && (
          <>
            <span className="flex-1 min-w-0 truncate text-[14px] leading-5">{item.label}</span>
            {item.liveDot && !active && (
              <span
                className={item.liveDot === 'coral' ? 'ch-burn-dot' : 'ch-live-dot'}
                aria-hidden="true"
              />
            )}
            {item.trailing != null && <span className="shrink-0">{item.trailing}</span>}
            {item.externalHint && (
              <ArrowUpRight className="w-3.5 h-3.5 shrink-0 text-fg-muted" />
            )}
          </>
        )}
      </>
    );

    if (item.href) {
      return (
        <a
          key={item.key}
          href={item.href}
          data-active={active}
          aria-current={active ? 'page' : undefined}
          title={collapsed ? item.label : undefined}
          className={rowClass}
        >
          {inner}
        </a>
      );
    }

    return (
      <button
        key={item.key}
        type="button"
        onClick={item.onClick}
        title={collapsed ? item.label : undefined}
        className={rowClass}
      >
        {inner}
      </button>
    );
  };

  const mobileTabs: { key: string; section: AppSection; href: string; label: string; icon: FlatNavItem['icon']; live?: boolean }[] = [
    { key: 'observatory', section: 'observatory', href: observatoryHref, label: zh ? '总览' : 'Overview', icon: Gauge },
    { key: 'pool', section: 'pool', href: '/pool', label: zh ? '账号池' : 'Pool', icon: Users },
    { key: 'workbench', section: 'workbench', href: '/exp', label: zh ? '实验' : 'Jobs', icon: TerminalSquare, live: summary.active_vms > 0 },
    { key: 'router', section: 'router', href: '/router', label: zh ? '路由' : 'Routing', icon: Route },
    { key: 'settings', section: 'settings', href: '/cfg', label: zh ? '密钥' : 'Keys', icon: KeyRound },
  ];

  return (
    <div className="min-h-dvh flex">
      {/* ================================================================== */}
      {/* Left Sidebar (`268px` Stardust Warm Stone Rail — Zero Dead Space!)  */}
      {/* ================================================================== */}
      <aside
        className={cn(
          'ui-chrome-sidebar hidden md:flex flex-col shrink-0 sticky top-0 h-dvh z-40',
          'transition-[width] duration-300 ease-[cubic-bezier(0.22,1,0.36,1)]',
          collapsed ? 'w-[64px]' : 'w-[268px]',
        )}
      >
        {/* 1. Brand + Collapse + Quick Search Input (`⌘K`) */}
        <div
          data-colabhive-drag
          className={cn(
            'h-13 shrink-0 flex items-center',
            collapsed ? 'justify-center pt-2' : 'justify-between pl-3.5 pr-2.5 pt-1.5',
          )}
          style={
            !collapsed && macTrafficInset > 0
              ? { paddingLeft: macTrafficInset + 8 }
              : collapsed && macTrafficInset > 0
                ? { marginTop: 26 }
                : undefined
          }
        >
          <a
            href={observatoryHref}
            className="flex items-center gap-2 min-w-0 no-underline"
            title={t('app.name')}
          >
            <BrandMark />
            {!collapsed && (
              <span className="ui-title text-[14.5px] font-semibold truncate">
                {t('app.name')}
              </span>
            )}
          </a>
          {!collapsed && (
            <IconButton
              label={zh ? '收起侧栏' : 'Collapse sidebar'}
              size="sm"
              onClick={() => setCollapsed(true)}
            >
              <PanelLeftClose className="w-4 h-4" />
            </IconButton>
          )}
        </div>

        {collapsed ? (
          <div className="flex flex-col items-center gap-1.5 py-2">
            <IconButton
              label={zh ? '展开侧栏' : 'Expand sidebar'}
              onClick={() => setCollapsed(false)}
            >
              <PanelLeftOpen className="w-4 h-4" />
            </IconButton>
            <IconButton
              label={`${t('nav.search_placeholder')} (⌘K)`}
              onClick={() => setSearchOpen(true)}
            >
              <Search className="w-4 h-4" />
            </IconButton>
          </div>
        ) : (
          <div className="shrink-0 px-3 pt-1 pb-2.5">
            <button
              type="button"
              onClick={() => setSearchOpen(true)}
              className="flex items-center gap-2 w-full h-8.5 px-2.5 rounded-[10px] border text-left cursor-pointer transition-[background-color,border-color,box-shadow] duration-150 hover:border-line-strong"
              style={{
                backgroundColor: 'var(--surface-2)',
                borderColor: 'var(--border-subtle)',
                boxShadow: 'var(--shadow-control)',
              }}
            >
              <Search className="w-3.5 h-3.5 shrink-0 text-fg-muted" />
              <span className="flex-1 min-w-0 truncate text-[12.5px] text-fg-muted">
                {t('nav.search_placeholder')}
              </span>
              <kbd className="ch-num shrink-0 px-1.5 py-0.5 rounded text-[10.5px] text-fg-muted bg-[var(--neutral-soft)]">
                ⌘K
              </kbd>
            </button>
          </div>
        )}

        {/* Scrollable Structured Middle Body */}
        <div className="flex-1 min-h-0 overflow-y-auto px-3 pb-2 space-y-3.5">
          {/* 2. `运行中` Live Tasks Block */}
          {!collapsed && (
            <section aria-label={t('nav.live_tasks')} className="space-y-1.5">
              <div className="flex items-center justify-between px-1">
                <span className="text-[11px] font-semibold tracking-wide text-fg-muted">
                  {t('nav.live_tasks')}
                </span>
                <span className="ch-num text-[11px] font-medium text-fg-muted">
                  {liveTaskRows.length}
                </span>
              </div>

              {liveTaskRows.length > 0 ? (
                <div className="space-y-1">
                  {liveTaskRows.map((task, idx) => (
                    <button
                      key={task.id}
                      type="button"
                      onClick={() => route.navigate(task.href)}
                      className={cn(
                        'flex flex-col gap-0.5 w-full px-2.5 py-2 rounded-[11px] text-left cursor-pointer transition-colors',
                        idx === 0
                          ? 'bg-surface-2 border border-line shadow-[var(--shadow-control)] hover:border-line-strong'
                          : 'hover:bg-[var(--hover-fill)]',
                      )}
                    >
                      <span className="flex items-center justify-between gap-2 min-w-0">
                        <span className="flex items-center gap-1.5 min-w-0">
                          <span
                            className="w-1.5 h-1.5 rounded-full shrink-0"
                            style={{
                              backgroundColor:
                                task.tone === 'coral' ? 'var(--coral)' : 'var(--emerald)',
                            }}
                          />
                          <span className="truncate text-[12.5px] font-semibold text-fg">
                            {task.title}
                          </span>
                        </span>
                        <span className="ch-num shrink-0 text-[10.5px] font-medium px-1.5 py-0.5 rounded-md bg-[var(--neutral-soft)] text-fg-secondary">
                          {task.gpuTag}
                        </span>
                      </span>
                      <span className="pl-3 truncate text-[11px] text-fg-muted">
                        {task.subtitle}
                      </span>
                    </button>
                  ))}
                </div>
              ) : (
                <button
                  type="button"
                  onClick={() => route.navigate('/exp')}
                  className="flex items-center justify-between gap-2 w-full px-2.5 py-2 rounded-[11px] bg-surface-2/70 border border-line-faint text-left cursor-pointer hover:bg-surface-2"
                >
                  <span className="flex items-center gap-1.5 min-w-0 text-[12px] text-fg-secondary">
                    <span
                      className="w-1.5 h-1.5 rounded-full shrink-0"
                      style={{ backgroundColor: 'var(--emerald)' }}
                    />
                    <span className="truncate">
                      {zh ? '当前空闲 · 0 CU/h 待命中' : 'Idle · 0 CU/h standby'}
                    </span>
                  </span>
                  <span className="text-[11px] text-fg-muted">{zh ? '启动' : 'Run'}</span>
                </button>
              )}
            </section>
          )}

          {/* 3. `额度提醒` Watchlist Block (hidden completely when visibleAlerts.length === 0) */}
          {!collapsed && visibleAlerts.length > 0 && (
            <section aria-label={t('nav.watchlist')} className="space-y-1">
              <div className="flex items-center justify-between px-1">
                <div className="flex items-center gap-1.5 min-w-0">
                  <span className="text-[11px] font-semibold tracking-wide text-fg-muted">
                    {t('nav.watchlist')}
                  </span>
                  <span
                    className="ch-num inline-flex items-center justify-center min-w-[18px] h-[18px] px-1.5 rounded-full text-[10.5px] font-bold text-white"
                    style={{ backgroundColor: 'var(--coral)' }}
                  >
                    {visibleAlerts.length}
                  </span>
                </div>
                <button
                  type="button"
                  onClick={dismissAllAlerts}
                  title={zh ? '忽略全部当前提醒' : 'Dismiss all current alerts'}
                  className="text-[11px] font-medium text-fg-muted hover:text-fg px-1.5 py-0.5 rounded-md transition-colors hover:bg-[var(--hover-fill)] cursor-pointer bg-transparent border-0"
                >
                  {zh ? '忽略全部' : 'Clear'}
                </button>
              </div>

              <div className="space-y-0.5">
                {visibleAlerts.map((alert) => {
                  const dotColor =
                    alert.tone === 'coral'
                      ? 'var(--coral)'
                      : alert.tone === 'amber'
                        ? 'var(--amber)'
                        : 'var(--emerald)';
                  const subColor =
                    alert.tone === 'coral'
                      ? 'var(--coral-text)'
                      : alert.tone === 'amber'
                        ? 'var(--amber-text)'
                        : 'var(--emerald-text)';
                  return (
                    <div
                      key={alert.id}
                      role="button"
                      tabIndex={0}
                      onClick={() => route.navigate(alert.href)}
                      onKeyDown={(e) => {
                        if (e.target !== e.currentTarget) return;
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault();
                          route.navigate(alert.href);
                        }
                      }}
                      className="group/alert flex items-start justify-between gap-1.5 w-full pl-2.5 pr-1.5 py-1.5 rounded-[10px] text-left cursor-pointer transition-colors hover:bg-[var(--hover-fill)]"
                    >
                      <span className="flex flex-col gap-0.5 min-w-0 flex-1">
                        <span className="flex items-center gap-1.5 min-w-0">
                          <span
                            className="w-1.5 h-1.5 rounded-full shrink-0"
                            style={{ backgroundColor: dotColor }}
                          />
                          <span className="truncate text-[12.5px] font-medium text-fg">
                            {alert.title}
                          </span>
                        </span>
                        <span
                          className="pl-3 truncate text-[11px] font-medium"
                          style={{ color: subColor }}
                        >
                          {alert.subtitle}
                        </span>
                      </span>
                      <button
                        type="button"
                        aria-label={zh ? '忽略此提醒' : 'Dismiss alert'}
                        title={zh ? '忽略此提醒' : 'Dismiss'}
                        onClick={(e) => {
                          e.stopPropagation();
                          dismissAlert(alert.id);
                        }}
                        className="shrink-0 inline-flex items-center justify-center w-5 h-5 mt-0.5 rounded-md text-fg-muted hover:text-fg hover:bg-surface-2 transition-colors cursor-pointer bg-transparent border-0"
                      >
                        <X className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  );
                })}
              </div>
            </section>
          )}

          {/* 4a. Group `工作台` (Flat 14px Primary Rows) */}
          <nav aria-label={t('nav.group_workspace')} className="space-y-0.5">
            {!collapsed && (
              <div className="px-1 pb-1 text-[11px] font-semibold tracking-wide text-fg-muted">
                {t('nav.group_workspace')}
              </div>
            )}
            {workspaceItems.map(renderFlatNavRow)}
          </nav>

          {/* 4b. Group `接入与系统` (Flat 14px Primary Rows — Zero Indented Sub-Menus!) */}
          <nav aria-label={t('nav.group_system')} className="space-y-0.5">
            {!collapsed && (
              <div className="px-1 pb-1 text-[11px] font-semibold tracking-wide text-fg-muted">
                {t('nav.group_system')}
              </div>
            )}
            {systemItems.map(renderFlatNavRow)}
          </nav>
        </div>

        {/* 5. Bottom Cluster: `卡池实时余量` Mini Account Rings + Utility Bar */}
        <div
          className="shrink-0 px-3 pt-2.5 pb-2.5 border-t space-y-2"
          style={{ borderColor: 'var(--border-faint)' }}
        >
          {!collapsed && accountRingChips.length > 0 && (
            <button
              type="button"
              onClick={() => route.navigate('/pool')}
              className="w-full rounded-xl border p-2 text-left cursor-pointer transition-colors hover:border-line-strong"
              style={{
                backgroundColor: 'var(--surface-2)',
                borderColor: 'var(--border-subtle)',
                boxShadow: 'var(--shadow-control)',
              }}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] font-semibold text-fg-muted">
                  {t('nav.pool_rings')}
                </span>
                <span className="ch-num text-[11px] font-semibold text-fg">{poolCuText}</span>
              </div>
              <div className="mt-1.5 grid grid-cols-3 gap-1">
                {accountRingChips.map((chip) => (
                  <span
                    key={chip.id}
                    title={chip.fullLabel}
                    className="inline-flex items-center gap-1 min-w-0 px-1 py-0.5 rounded-md bg-[var(--neutral-soft)]"
                  >
                    <CircularQuotaRing
                      value={chip.pct}
                      size={14}
                      strokeWidth={2.3}
                      tone={chip.tone}
                      showValue={false}
                    />
                    <span className="truncate text-[10.5px] font-medium text-fg-secondary">
                      {chip.shortLabel}
                    </span>
                    <span className="ch-num ml-auto text-[10px] font-semibold text-fg">
                      {chip.pct}%
                    </span>
                  </span>
                ))}
              </div>
            </button>
          )}

          {/* Compact Bottom Utility Strip: Health Dot + 中文/EN + 浅色/深色 + 隐藏隐私 + 晒图卡 + 锁 */}
          <div
            className={cn(
              'relative flex',
              collapsed ? 'flex-col items-center gap-1' : 'items-center justify-between gap-1',
            )}
          >
            <div className="group/health min-w-0">
              <button
                type="button"
                onClick={healthState === 'preview' ? handleRetrySync : undefined}
                aria-label={`${healthLabel} ${latencyLabel}`.trim()}
                className={cn(
                  'inline-flex items-center gap-1.5 h-7 rounded-lg text-[11.5px] text-fg-secondary transition-colors hover:bg-[var(--hover-fill)]',
                  collapsed ? 'w-7 justify-center' : 'px-1.5 max-w-[106px]',
                  healthState === 'preview' ? 'cursor-pointer hover:text-fg' : 'cursor-default',
                )}
              >
                {retrying ? (
                  <RefreshCw className="w-3 h-3 shrink-0 animate-spin" />
                ) : (
                  <span
                    className="w-1.5 h-1.5 rounded-full shrink-0"
                    style={{ backgroundColor: healthColor }}
                  />
                )}
                {!collapsed && (
                  <span className="truncate">
                    {healthState === 'ready'
                      ? zh
                        ? '已连接'
                        : 'Live'
                      : healthState === 'preview'
                        ? zh
                          ? '演示'
                          : 'Demo'
                        : zh
                          ? '异常'
                          : 'Issue'}
                  </span>
                )}
              </button>
              {healthDetails}
            </div>

            <div
              className={cn(
                'flex shrink-0',
                collapsed ? 'flex-col items-center gap-1' : 'items-center gap-0.5',
              )}
            >
              <button
                type="button"
                onClick={toggleLocale}
                title={languageSwitchTitle}
                aria-label={languageSwitchTitle}
                className="ui-icon-btn w-7 h-7 text-[11px] font-semibold"
              >
                {zh ? '中' : 'EN'}
              </button>

              <IconButton
                size="sm"
                className="w-7 h-7"
                label={themeSwitchTitle}
                onClick={handleToggleTheme}
              >
                {theme === 'dark' ? (
                  <Moon className="w-[14px] h-[14px]" />
                ) : (
                  <Sun className="w-[14px] h-[14px]" />
                )}
              </IconButton>

              <IconButton
                size="sm"
                className="w-7 h-7"
                label={privacyMask ? t('nav.privacy_mask_on') : t('nav.privacy_mask')}
                title={privacyTitle}
                onClick={togglePrivacyMask}
                style={
                  privacyMask
                    ? { color: 'var(--emerald-text)', backgroundColor: 'var(--emerald-soft)' }
                    : undefined
                }
              >
                {privacyMask ? (
                  <EyeOff className="w-[14px] h-[14px]" />
                ) : (
                  <Eye className="w-[14px] h-[14px]" />
                )}
              </IconButton>

              <IconButton
                size="sm"
                className="w-7 h-7"
                label={t('nav.share_card')}
                onClick={() => setShareModalOpen(true)}
              >
                <Camera className="w-[14px] h-[14px]" />
              </IconButton>

              <div data-dismiss-scope="admin-token" className="inline-flex items-center gap-0.5">
                {health.auth_required && hasAdminToken && !collapsed && (
                  <button
                    type="button"
                    onClick={handleLockConsole}
                    title={zh ? '退出登录并锁定管理控制台' : 'Sign out and lock console'}
                    className="inline-flex items-center gap-1 h-7 px-2 rounded-lg text-[11px] font-medium transition-colors hover:bg-[var(--hover-fill)] cursor-pointer bg-transparent border-0"
                    style={{ color: 'var(--text-secondary)' }}
                  >
                    <LogOut className="w-3.5 h-3.5" />
                    <span>{zh ? '锁定' : 'Lock'}</span>
                  </button>
                )}

                <IconButton
                  size="sm"
                  className="w-7 h-7"
                  label={
                    health.auth_required
                      ? zh
                        ? '管理员访问口令与锁定'
                        : 'Admin token & lock'
                      : zh
                        ? '管理员访问口令（可选）'
                        : 'Admin access token (optional)'
                  }
                  aria-expanded={authPopoverOpen}
                  onClick={() => setAuthPopoverOpen((v) => !v)}
                >
                  <Lock
                    className="w-[14px] h-[14px]"
                    style={hasAdminToken ? { color: 'var(--emerald-text)' } : undefined}
                  />
                </IconButton>

                {authPopoverOpen && (
                  <div
                    className={cn(
                      'ui-float ui-float-pop ui-anim-pop absolute z-50 w-72 rounded-xl p-3.5',
                      collapsed ? 'left-full bottom-0 ml-3' : 'left-0 bottom-full mb-2',
                    )}
                  >
                    <AdminTokenForm
                      zh={zh}
                      draft={adminTokenDraft}
                      authRequired={health.auth_required}
                      hasToken={hasAdminToken}
                      onDraftChange={setAdminTokenDraft}
                      onSubmit={handleSaveAdminToken}
                      onLock={handleLockConsole}
                      onCancel={() => setAuthPopoverOpen(false)}
                      saveLabel={t('common.save')}
                      cancelLabel={t('common.cancel')}
                    />
                  </div>
                )}
              </div>
            </div>
          </div>
        </div>
      </aside>

      {/* ================================================================== */}
      {/* Main Column: Top Ring Header + L0 Warm Stone Canvas                 */}
      {/* ================================================================== */}
      <div className="flex-1 min-w-0 flex flex-col">
        <header
          data-colabhive-drag
          className="ui-chrome ui-chrome-top sticky top-0 z-30 h-14 shrink-0 px-4 sm:px-6 flex items-center justify-between gap-3"
        >
          {/* Left: Stardust Breadcrumb + Demo Status Pill */}
          <div className="flex items-center gap-2.5 min-w-0">
            <a href={observatoryHref} className="md:hidden shrink-0" aria-label={t('app.name')}>
              <BrandMark />
            </a>

            {IS_STATIC_DEMO_BUILD && (
              <button
                type="button"
                onClick={() => route.navigate('/')}
                className="ui-btn ui-btn-secondary ui-btn-xs shrink-0"
                title={zh ? '返回 ColabHive 产品主页' : 'Back to ColabHive Product Home'}
              >
                <ArrowLeft className="w-3 h-3" />
                <span>{zh ? '返回产品主页' : 'Product Home'}</span>
              </button>
            )}

            <nav
              aria-label={zh ? '当前位置' : 'Breadcrumb'}
              className="flex items-center gap-2 min-w-0 text-[13px]"
            >
              <Sparkles
                className="hidden sm:block w-3.5 h-3.5 shrink-0"
                style={{ color: 'var(--coral)' }}
                aria-hidden="true"
              />
              <span className="hidden sm:inline text-fg-muted">{currentGroupLabel}</span>
              <span className="hidden sm:inline text-fg-muted opacity-45">/</span>
              {route.experimentId ? (
                <>
                  <a
                    href="/exp"
                    className="shrink-0 no-underline text-fg-secondary transition-colors hover:text-fg"
                  >
                    {currentPageLabel}
                  </a>
                  <span className="text-fg-muted opacity-45">/</span>
                  <span className="ch-num min-w-0 truncate text-[13px] font-semibold text-fg">
                    {route.experimentId}
                  </span>
                </>
              ) : (
                <span className="min-w-0 truncate font-semibold text-fg">{currentPageLabel}</span>
              )}
            </nav>

            {!offlineSnapshot && health.deployment_mode === 'cloud' && (
              <StatusPill tone="sky" icon={<Cloud />} className="hidden lg:inline-flex shrink-0">
                {zh ? '云端模式 · Cloud' : 'Cloud Server'}
              </StatusPill>
            )}

            {offlineSnapshot && (
              <StatusPill tone="amber" dot className="hidden lg:inline-flex shrink-0 pr-1">
                <span>{t('nav.status_demo')}</span>
                <button
                  type="button"
                  onClick={handleRetrySync}
                  title={t('nav.retry_connect')}
                  aria-label={t('nav.retry_connect')}
                  className="inline-flex items-center justify-center w-4 h-4 rounded-full cursor-pointer transition-colors hover:bg-[var(--amber-soft)]"
                >
                  <RefreshCw className={cn('w-2.5 h-2.5', retrying && 'animate-spin')} />
                </button>
              </StatusPill>
            )}
          </div>

          {/* Right: Inline Top-3 Account SVG Progress Rings Capsule + Ink CTA Pill */}
          <div className="flex items-center gap-2 shrink-0">
            <IconButton
              label={`${t('nav.search_placeholder')} (⌘K)`}
              onClick={() => setSearchOpen(true)}
              className="md:hidden"
            >
              <Search className="w-4 h-4" />
            </IconButton>

            {accountRingChips.length > 0 && (
              <button
                type="button"
                onClick={() => route.navigate('/pool')}
                title={`${t('nav.top_pool_label')}: ${poolCuText} · ${activeLabel}`}
                className="hidden sm:inline-flex items-center gap-2.5 h-8.5 pl-3 pr-3.5 rounded-full border cursor-pointer transition-[border-color,background-color,box-shadow,transform] duration-200 hover:border-line-strong active:scale-[0.98]"
                style={{
                  backgroundColor: 'var(--surface-2)',
                  borderColor: 'var(--border-subtle)',
                  boxShadow: 'var(--shadow-control)',
                }}
              >
                <span className="hidden xl:inline text-[11.5px] font-medium text-fg-muted">
                  {t('nav.top_pool_label')}
                </span>
                {accountRingChips.map((chip, idx) => (
                  <React.Fragment key={chip.id}>
                    {idx > 0 && (
                      <span
                        className="hidden md:inline-block w-px h-3"
                        style={{ backgroundColor: 'var(--border-subtle)' }}
                        aria-hidden="true"
                      />
                    )}
                    <span
                      className={cn(
                        'inline-flex items-center gap-1.5 text-[12px]',
                        idx >= 2 && 'hidden lg:inline-flex',
                      )}
                    >
                      <CircularQuotaRing
                        value={chip.pct}
                        size={16}
                        strokeWidth={2.5}
                        tone={chip.tone}
                        showValue={false}
                      />
                      <span className="font-semibold text-fg">{chip.shortLabel}</span>
                      <span
                        className="ch-num font-semibold"
                        style={{
                          color:
                            chip.tone === 'coral'
                              ? 'var(--coral-text)'
                              : chip.tone === 'amber'
                                ? 'var(--amber-text)'
                                : 'var(--emerald-text)',
                        }}
                      >
                        {chip.pct}%
                      </span>
                    </span>
                  </React.Fragment>
                ))}
              </button>
            )}

            <button
              type="button"
              onClick={() => onOpenConnectDrawer()}
              aria-label={t('nav.connect_agent')}
              className="ui-btn ui-btn-primary ui-btn-md"
            >
              <Zap className="w-3.5 h-3.5" />
              <span className="hidden min-[420px]:inline">{t('nav.connect_agent')}</span>
            </button>

            <div className="md:hidden" data-dismiss-scope="mobile-menu">
              <IconButton
                label={zh ? '更多' : 'More'}
                aria-expanded={mobileMenuOpen}
                onClick={() => {
                  setMobileMenuOpen((v) => !v);
                  setMobileTokenOpen(false);
                }}
              >
                <MoreHorizontal className="w-4 h-4" />
              </IconButton>
            </div>

            <WindowsCaptionButtons zh={zh} />
          </div>
        </header>

        {/* Mobile utility popover */}
        {mobileMenuOpen && (
          <div
            data-dismiss-scope="mobile-menu"
            className="ui-float ui-float-pop ui-anim-pop md:hidden fixed right-3 top-[60px] z-40 w-64 rounded-2xl p-2"
          >
            {mobileTokenOpen ? (
              <div className="p-1.5">
                <AdminTokenForm
                  zh={zh}
                  draft={adminTokenDraft}
                  authRequired={health.auth_required}
                  hasToken={hasAdminToken}
                  onDraftChange={setAdminTokenDraft}
                  onSubmit={handleSaveAdminToken}
                  onLock={handleLockConsole}
                  onCancel={() => setMobileTokenOpen(false)}
                  saveLabel={t('common.save')}
                  cancelLabel={t('common.cancel')}
                />
              </div>
            ) : (
              <>
                <button
                  type="button"
                  onClick={healthState === 'preview' ? handleRetrySync : undefined}
                  className="flex items-center gap-2 w-full px-2.5 pt-1.5 pb-1 text-left text-[12px] text-fg-secondary"
                >
                  <span
                    className="w-1.5 h-1.5 rounded-full shrink-0"
                    style={{ backgroundColor: healthColor }}
                  />
                  <span className="flex-1 truncate">
                    {healthLabel}
                    {latencyLabel && <span className="ch-num text-fg-muted"> {latencyLabel}</span>}
                  </span>
                  {healthState === 'preview' && (
                    <RefreshCw className={cn('w-3 h-3 shrink-0', retrying && 'animate-spin')} />
                  )}
                </button>
                <div className="ch-num px-2.5 pb-2 text-[11.5px] text-fg-muted">
                  {poolCuText} · {activeLabel} · {formatCU(summary.active_burn_rate_cu_hr)} CU/h
                </div>
                <div className="mx-1 mb-1 border-t" style={{ borderColor: 'var(--border-faint)' }} />
                <MenuRow
                  icon={<Sparkles />}
                  label={t('nav.quick_guide')}
                  value={zh ? '3 步' : '3 steps'}
                  onClick={handleOpenGuide}
                />
                <MenuRow
                  icon={<Globe />}
                  label={zh ? '语言' : 'Language'}
                  value={zh ? '中文' : 'English'}
                  onClick={toggleLocale}
                />
                <MenuRow
                  icon={theme === 'dark' ? <Moon /> : <Sun />}
                  label={zh ? '主题' : 'Theme'}
                  value={themeLabel}
                  onClick={handleToggleTheme}
                />
                <MenuRow
                  icon={privacyMask ? <EyeOff /> : <Eye />}
                  label={t('nav.privacy_mask')}
                  value={privacyMask ? (zh ? '已开启' : 'On') : zh ? '已关闭' : 'Off'}
                  active={privacyMask}
                  onClick={togglePrivacyMask}
                />
                <MenuRow
                  icon={<Camera />}
                  label={t('nav.share_card')}
                  onClick={() => {
                    closeMobileMenu();
                    setShareModalOpen(true);
                  }}
                />
                <MenuRow
                  icon={<Lock />}
                  label={zh ? '管理员口令' : 'Admin token'}
                  value={hasAdminToken ? (zh ? '已设置' : 'Set') : undefined}
                  active={hasAdminToken}
                  onClick={() => setMobileTokenOpen(true)}
                />
                {health.auth_required && hasAdminToken && (
                  <MenuRow
                    icon={<LogOut />}
                    label={zh ? '退出登录 / 锁定' : 'Lock / Sign Out'}
                    onClick={handleLockConsole}
                  />
                )}
                <MenuRow
                  icon={<HeartHandshake />}
                  label={t('nav.contribute')}
                  href="/contribute"
                  onClick={closeMobileMenu}
                />
              </>
            )}
          </div>
        )}

        {/* Content canvas (L0) */}
        <main className="flex-1 min-w-0 overflow-x-clip">
          <div className="max-w-[1600px] w-full mx-auto px-5 py-6 sm:px-8 sm:py-7">{children}</div>
          <div className="h-16 md:hidden" aria-hidden="true" />
        </main>
      </div>

      {/* ================================================================== */}
      {/* Mobile Bottom Tab Bar (<768px): All 5 Primary Sections              */}
      {/* ================================================================== */}
      <nav
        aria-label={zh ? '主导航' : 'Primary'}
        className="ui-chrome ui-chrome-bottom md:hidden fixed bottom-0 inset-x-0 z-30 grid grid-cols-5 pb-[env(safe-area-inset-bottom)]"
      >
        {mobileTabs.map((tab) => {
          const Icon = tab.icon;
          const active = route.section === tab.section;
          return (
            <a
              key={tab.key}
              href={tab.href}
              aria-current={active ? 'page' : undefined}
              className={cn(
                'flex flex-col items-center justify-center gap-1 h-14 no-underline text-[10px] font-medium transition-colors',
                active ? 'text-fg font-semibold' : 'text-fg-muted',
              )}
            >
              <span
                className="relative flex items-center justify-center w-10 h-6 rounded-full transition-colors duration-200"
                style={active ? { backgroundColor: 'var(--accent-soft)' } : undefined}
              >
                <Icon className="w-4 h-4" />
                {tab.live && (
                  <span
                    className="absolute top-0.5 right-1.5 w-1.5 h-1.5 rounded-full"
                    style={{
                      backgroundColor: isBurning ? 'var(--coral)' : 'var(--emerald)',
                    }}
                  />
                )}
              </span>
              <span className="max-w-[68px] truncate">{tab.label}</span>
            </a>
          );
        })}
      </nav>

      <QuickSearchModal
        open={searchOpen}
        onClose={() => setSearchOpen(false)}
        zh={zh}
        placeholder={t('nav.search_placeholder')}
        items={quickJumpItems}
      />

      <ShareQuotaModal
        open={shareModalOpen}
        onClose={() => setShareModalOpen(false)}
        summary={summary}
      />
    </div>
  );
}
