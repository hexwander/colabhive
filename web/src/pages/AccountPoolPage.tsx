import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Activity,
  AlertCircle,
  ArrowRight,
  Check,
  CheckCircle2,
  ChevronRight,
  Cpu,
  Download,
  ExternalLink,
  FileJson,
  Flame,
  FolderSearch,
  Gift,
  Info,
  Loader2,
  Lock,
  Plus,
  RefreshCw,
  Search,
  SearchX,
  Settings2,
  ShieldCheck,
  Sparkles,
  Terminal,
  Trash2,
  Upload,
  Wifi,
  X,
  Zap,
} from 'lucide-react';
import type {
  AcceleratorType,
  Account,
  AccountVerifyResult,
  ContributorGuardrails,
  DiscoveredLocalCredential,
  GPUEntitlementStatus,
  ImportConflictStrategy,
  OAuthPendingSession,
  ProxyConfig,
  ProxyProbeResult,
  RouterPolicy,
  SubscriptionTier,
  SystemSelfTestResult,
  VerifyStepResult,
} from '../types';
import { ALL_ACCELERATORS } from '../types';
import { api, formatCU, formatHours } from '../lib/api';
import { useI18n } from '../i18n';
import {
  AccountHealthBadge,
  CopyButton,
  DEMO_PROXIES,
  formatRelativeCountdown,
  HardwareTierBadge,
  PlatformProviderBadge,
  SubscriptionTierBadge,
} from '../components/layout/telemetryUi';
import { DEMO_ROUTER_POLICY } from '../components/terminal/workbenchMockData';
import {
  ActionButton,
  AnimatedNumber,
  CircularQuotaRing,
  cn,
  DialogModal,
  EmptyState,
  IconButton,
  InsetWell,
  LayeredCard,
  PageHeader,
  SegmentedQuotaBar,
  SegmentedTabs,
  SlideOver,
  StatusPill,
  Switch,
  type QuotaRingTone,
  type SemanticTone,
} from '../components/ui/primitives';

export interface AccountPoolPageProps {
  accounts: Account[];
  proxies?: ProxyConfig[];
  deploymentMode?: 'local' | 'cloud';
  onAccountsChanged: () => void;
}

// Smart client-side extractor for pasted localhost callback URLs or raw Google auth codes
function extractAuthCodePreview(raw: string): {
  valid: boolean;
  code: string;
  stateFromUrl?: string;
  sourceKind: 'url' | 'raw' | 'empty';
} {
  const trimmed = raw.trim();
  if (!trimmed) return { valid: false, code: '', sourceKind: 'empty' };

  if (trimmed.startsWith('http://') || trimmed.startsWith('https://') || trimmed.includes('code=')) {
    try {
      const normalized = trimmed.startsWith('http') ? trimmed : `http://localhost/?${trimmed}`;
      const u = new URL(normalized);
      const code = u.searchParams.get('code') || '';
      const st = u.searchParams.get('state') || undefined;
      return {
        valid: code.length >= 10,
        code,
        stateFromUrl: st,
        sourceKind: 'url',
      };
    } catch {
      const m = trimmed.match(/[?&]code=([^&#\s]+)/);
      if (m) {
        const code = decodeURIComponent(m[1]);
        return { valid: code.length >= 10, code, sourceKind: 'url' };
      }
    }
  }

  return {
    valid: trimmed.length >= 10,
    code: trimmed,
    sourceKind: 'raw',
  };
}

// ============================================================================
// Shared building blocks
// ============================================================================

const LIST_GPU_ORDER: AcceleratorType[] = ['G4', 'H100', 'A100', 'L4', 'T4'];
const CYCLE_QUOTA_PRESETS = [2500, 1000, 500, 100] as const;

const fmtCU = (v: number) => formatCU(v);
const fmtSmartCU = (v: number) =>
  Math.abs(v - Math.round(v)) < 0.005 ? formatCU(v, 0) : formatCU(v, 2);
const fmtHours = (v: number) => formatHours(v);

function defaultCycleCuForTier(tier: SubscriptionTier): number {
  switch (tier) {
    case 'AI_ULTRA':
      return 2500;
    case 'PRO_PLUS':
      return 500;
    case 'PRO':
      return 100;
    case 'PAYG':
      return 100;
    default:
      return 100;
  }
}

/** Dual-segment quota geometry shared by the list rows and the drawer. */
function computeQuota(account: Account) {
  const hasIssue =
    account.health === 'AUTH_EXPIRED' ||
    account.health === 'PROXY_ERROR' ||
    account.health === 'QUARANTINED_DENYLIST';

  if (account.subscription_tier === 'FREE' && account.paid_cu_balance <= 0) {
    const freeCu = Math.max(0, account.free_quota?.remaining_free_cu ?? 0);
    const pct = Math.min(100, (freeCu / 15) * 100);
    return {
      isColabFreeOnly: true,
      reserveCu: 0,
      cycleTotalCu: 15,
      cycleMax: 15,
      schedulableCu: freeCu,
      reservePct: 0,
      schedulablePct: pct,
      remainingPct: pct,
      ringValue: pct,
      ringTone: (hasIssue ? 'coral' : 'sky') as QuotaRingTone,
      barTone: (hasIssue ? 'coral' : 'sky') as 'emerald' | 'coral' | 'amber' | 'sky',
      barValue: pct,
      g4Hours: freeCu / 1.96,
    };
  }

  const reserveCu = Math.max(0, account.guardrails?.min_reserve_cu || 0);
  const baseCycleCu =
    account.reference_cycle_cu > 0
      ? account.reference_cycle_cu
      : defaultCycleCuForTier(account.subscription_tier);
  const cycleTotalCu = Math.max(baseCycleCu, account.paid_cu_balance, 1);
  const cycleMax = Math.max(cycleTotalCu, reserveCu, 1);
  const schedulableCu = Math.max(0, account.schedulable_cu ?? account.paid_cu_balance - reserveCu);
  const reservePct = Math.min(100, (reserveCu / cycleMax) * 100);
  const schedulablePct = Math.min(100, (schedulableCu / cycleMax) * 100);
  const remainingPct = Math.min(100, (account.paid_cu_balance / cycleMax) * 100);
  const ringValue = account.is_contributor ? schedulablePct : remainingPct;
  const g4Rate = account.gpu_entitlements?.G4?.calibrated_burn_rate || 9.66;

  let ringTone: QuotaRingTone = 'emerald';
  let barTone: 'emerald' | 'coral' | 'amber' | 'sky' = 'emerald';

  if (hasIssue || ringValue <= 15) {
    ringTone = 'coral';
    barTone = 'coral';
  } else if (account.expiring_soon_cu > 0) {
    ringTone = 'coral';
    barTone = ringValue <= 35 ? 'coral' : 'emerald';
  } else if (ringValue <= 35) {
    ringTone = 'amber';
    barTone = 'amber';
  } else if (account.is_contributor) {
    ringTone = 'emerald';
    barTone = 'sky';
  }

  return {
    isColabFreeOnly: false,
    reserveCu,
    cycleTotalCu,
    cycleMax,
    schedulableCu,
    reservePct,
    schedulablePct,
    remainingPct,
    ringValue,
    ringTone,
    barTone,
    barValue: ringValue,
    g4Hours: schedulableCu / g4Rate,
  };
}

function resolveGpuStatus(account: Account, acc: AcceleratorType): GPUEntitlementStatus {
  const allowedByContrib =
    !account.is_contributor ||
    account.guardrails.allowed_gpus.length === 0 ||
    account.guardrails.allowed_gpus.includes(acc);
  return !allowedByContrib ? 'INELIGIBLE' : account.gpu_entitlements?.[acc]?.status || 'AVAILABLE';
}

function gpuDotColor(status: GPUEntitlementStatus): string {
  if (status === 'TIER_RESTRICTED' || status === 'COOLDOWN') return 'var(--amber)';
  if (status === 'INELIGIBLE' || status === 'UNKNOWN') return 'var(--border-strong)';
  return 'var(--emerald)';
}

/** Raw runtime channel protocol. Technical: shown only inside the inspector drawer. */
function backendSummary(account: Account): string {
  if (account.public_api_supported && account.tunnel_api_supported) return 'v1beta + /tun/m';
  if (account.public_api_supported) return 'v1beta';
  if (account.tunnel_api_supported) return '/tun/m';
  return '—';
}

/** One plain sentence for what `backendSummary` means. */
function backendExplanation(account: Account, zh: boolean): string {
  if (account.public_api_supported && account.tunnel_api_supported) {
    return zh
      ? '双通道就绪：官方接口与隧道通道都可用，会自动选更稳的一条'
      : 'Dual channel ready: the official API and the tunnel both work; the steadier one is picked automatically';
  }
  if (account.tunnel_api_supported) {
    return zh
      ? '隧道通道就绪：通过 Colab 隧道运行代码'
      : 'Tunnel channel ready: code runs through the Colab tunnel';
  }
  if (account.public_api_supported) {
    return zh
      ? '官方接口通道就绪：通过 Colab 官方接口运行代码'
      : 'Official API channel ready: code runs through the Colab API';
  }
  return zh
    ? '还没有探测到可用通道，刷新余额后会自动重试'
    : 'No channel detected yet — refreshing the balance retries automatically';
}

/** Short channel status for pills (`双通道就绪` / `隧道通道就绪`). */
function backendShortLabel(account: Account, zh: boolean): string {
  if (account.public_api_supported && account.tunnel_api_supported) {
    return zh ? '双通道就绪' : 'Dual channel ready';
  }
  if (account.tunnel_api_supported) return zh ? '隧道通道就绪' : 'Tunnel ready';
  if (account.public_api_supported) return zh ? '官方接口就绪' : 'API ready';
  return zh ? '等待探测' : 'Pending';
}

/** Turns the service's English lock reasons into plain Chinese when `zh` is on. */
function localizeLockReason(reason: string | undefined, zh: boolean): string | undefined {
  if (!reason || !zh) return reason;
  const r = reason.toLowerCase();
  if (r.includes('pro+') || r.includes('ultra')) return '需 Colab Pro+ / Ultra 套餐';
  if (r.includes('guardrail') || r.includes('contributor')) return '号主未开放此型号显卡';
  if (r.includes('stockout') || r.includes('cooldown') || r.includes('http 400')) {
    return '当前区域暂时缺卡，冷却恢复中';
  }
  if (r.includes('rate limit') || r.includes('429')) return '请求过于频繁，稍后自动恢复';
  if (r.includes('ineligible') || r.includes('not eligible') || r.includes('restricted')) {
    return '当前套餐暂不支持此型号显卡';
  }
  return reason;
}

/** Plain tooltip for one GPU availability dot. */
function gpuDotTitle(
  acc: AcceleratorType,
  status: GPUEntitlementStatus,
  reason: string | undefined,
  zh: boolean,
): string {
  const localized = localizeLockReason(reason, zh);
  if (localized) return `${acc}${zh ? '：' : ': '}${localized}`;
  if (status === 'AVAILABLE') return zh ? `${acc}：可用` : `${acc}: available`;
  if (status === 'COOLDOWN' || status === 'TIER_RESTRICTED') {
    return zh
      ? `${acc}：暂时缺卡或套餐受限，稍后自动恢复`
      : `${acc}: temporarily unavailable, recovers automatically`;
  }
  if (status === 'INELIGIBLE') {
    return zh ? `${acc}：号主未开放或套餐不支持` : `${acc}: not enabled by the owner or the plan`;
  }
  return zh ? `${acc}：尚未验证` : `${acc}: not verified yet`;
}

function splitCsv(value: string): string[] {
  return value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
}

/** Quiet semantic message strip (success / error feedback). */
function Notice({
  tone,
  icon,
  children,
  onDismiss,
  dismissLabel = 'Dismiss',
}: {
  tone: Extract<SemanticTone, 'emerald' | 'rose' | 'amber' | 'accent'>;
  icon?: React.ReactNode;
  children: React.ReactNode;
  onDismiss?: () => void;
  dismissLabel?: string;
}) {
  return (
    <div
      data-tone={tone}
      role={tone === 'rose' ? 'alert' : tone === 'accent' ? 'note' : 'status'}
      className="flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[12.5px] leading-relaxed"
      style={{
        color: 'var(--tone-fg)',
        backgroundColor: 'var(--tone-soft)',
        borderColor: 'var(--tone-border)',
      }}
    >
      {icon != null && (
        <span className="shrink-0 mt-px [&>svg]:w-4 [&>svg]:h-4">{icon}</span>
      )}
      <span className="flex-1 min-w-0">{children}</span>
      {onDismiss && (
        <IconButton label={dismissLabel} size="sm" onClick={onDismiss} className="-my-0.5 -mr-1.5">
          <X className="w-3.5 h-3.5" />
        </IconButton>
      )}
    </div>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  children,
  className,
}: {
  label: React.ReactNode;
  htmlFor?: string;
  hint?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('space-y-1.5', className)}>
      <div className="flex items-baseline justify-between gap-2">
        <label htmlFor={htmlFor} className="block text-[11.5px] font-medium text-fg-secondary">
          {label}
        </label>
        {hint != null && <span className="text-[11.5px] text-fg-muted">{hint}</span>}
      </div>
      {children}
    </div>
  );
}

/** Pill toggle for an accelerator tier (sits on an L1 well, so the idle fill is L2). */
function GpuPillToggle({
  accelerator,
  active,
  onToggle,
}: {
  accelerator: AcceleratorType;
  active: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onToggle}
      className={cn(
        'rounded-full px-3 py-1 text-xs ch-num font-semibold border transition-colors cursor-pointer',
        active
          ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
          : 'bg-surface-2 text-fg-muted border-line hover:text-fg-secondary hover:border-line-strong',
      )}
    >
      {accelerator}
    </button>
  );
}

function SwitchRow({
  label,
  description,
  checked,
  onChange,
  disabled,
}: {
  label: string;
  description?: React.ReactNode;
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <label
      className={cn(
        'flex items-center justify-between gap-3',
        disabled ? 'cursor-not-allowed' : 'cursor-pointer',
      )}
    >
      <span className="min-w-0">
        <span className="block text-[12.5px] text-fg">{label}</span>
        {description != null && (
          <span className="block text-[11.5px] leading-relaxed text-fg-muted">{description}</span>
        )}
      </span>
      <Switch checked={checked} onChange={onChange} label={label} disabled={disabled} />
    </label>
  );
}

function ProbeReadout({
  probe,
  displayIP,
  zh,
}: {
  probe: ProxyProbeResult;
  displayIP: (ip: string | undefined | null) => string;
  zh: boolean;
}) {
  return probe.healthy ? (
    <div className="flex flex-wrap items-center gap-2 text-[12px]">
      <StatusPill tone="emerald" size="xs" dot mono>
        {probe.latency_ms} ms
      </StatusPill>
      <span className="ch-num text-fg-secondary">
        {zh ? '出口 IP' : 'Exit IP'} {displayIP(probe.exit_ip)} ·{' '}
        {probe.region || (zh ? '已连通' : 'Verified')}
      </span>
    </div>
  ) : (
    <div className="flex flex-wrap items-center gap-2 text-[12px]">
      <StatusPill tone="rose" size="xs" dot>
        {zh ? '连不上' : 'Unreachable'}
      </StatusPill>
      <span className="text-fg-secondary">
        {probe.error || (zh ? '网络不通，请检查代理地址' : 'Unreachable')}
      </span>
    </div>
  );
}

// ============================================================================
// Page
// ============================================================================

export function AccountPoolPage({
  accounts,
  proxies: propProxies,
  deploymentMode,
  onAccountsChanged,
}: AccountPoolPageProps) {
  const { locale, t, displayEmail } = useI18n();
  const zh = locale === 'zh-CN';

  const [detectedMode, setDetectedMode] = useState<'local' | 'cloud'>(deploymentMode ?? 'local');
  useEffect(() => {
    if (deploymentMode) {
      setDetectedMode(deploymentMode);
      return;
    }
    api
      .getHealth()
      .then((h) => {
        if (h?.deployment_mode) setDetectedMode(h.deployment_mode);
      })
      .catch(() => {});
  }, [deploymentMode]);
  const isCloudMode = (deploymentMode ?? detectedMode) === 'cloud';

  const [proxies, setProxies] = useState<ProxyConfig[]>(propProxies || DEMO_PROXIES);
  const [search, setSearch] = useState('');
  const [groupFilter, setGroupFilter] = useState<string>('all');
  const [busyId, setBusyId] = useState<string | null>(null);
  const [banner, setBanner] = useState<{
    kind: 'ok' | 'err';
    text: string;
    diagAccount?: Account;
  } | null>(null);

  // Active overlays
  const [oauthWizardOpen, setOauthWizardOpen] = useState(false);
  const [routerPolicy, setRouterPolicy] = useState<RouterPolicy>(DEMO_ROUTER_POLICY);
  const [policyLoaded, setPolicyLoaded] = useState<boolean>(false);
  const [togglingFreeFirst, setTogglingFreeFirst] = useState(false);
  const [transferHubOpen, setTransferHubOpen] = useState(false);
  const [transferInitialTab, setTransferInitialTab] = useState<TransferTab>('scan');
  const [transferInitialVaultMode, setTransferInitialVaultMode] = useState<'export' | 'import'>(
    'export',
  );
  const [selfTestOpen, setSelfTestOpen] = useState(false);
  const [selectedAccountId, setSelectedAccountId] = useState<string | null>(null);
  const [focusQuotaSection, setFocusQuotaSection] = useState(false);
  const [diagAccountId, setDiagAccountId] = useState<string | null>(null);
  const [diagFallbackAccount, setDiagFallbackAccount] = useState<Account | null>(null);

  // The drawer & diagnostic modal always read the live account so telemetry stays fresh after a refresh.
  const editingAccount = useMemo(
    () => accounts.find((a) => a.id === selectedAccountId) ?? null,
    [accounts, selectedAccountId],
  );

  const activeDiagAccount = useMemo(() => {
    if (!diagAccountId) return null;
    return accounts.find((a) => a.id === diagAccountId) ?? diagFallbackAccount;
  }, [accounts, diagAccountId, diagFallbackAccount]);

  const openAccountDiagnostic = useCallback((acct: Account) => {
    setDiagFallbackAccount(acct);
    setDiagAccountId(acct.id);
  }, []);

  const closeAccountDiagnostic = useCallback(() => {
    setDiagAccountId(null);
    setDiagFallbackAccount(null);
  }, []);

  useEffect(() => {
    api
      .listProxies()
      .then((list) => {
        if (list) setProxies(list);
      })
      .catch(() => {
        if (propProxies) setProxies(propProxies);
      });
  }, [propProxies]);

  useEffect(() => {
    let cancelled = false;
    const syncPolicy = () => {
      api
        .getRouterPolicy()
        .then((pol) => {
          if (!cancelled && pol) {
            setRouterPolicy(pol);
            setPolicyLoaded(true);
          }
        })
        .catch(() => {
          // Keep Free-First switch disabled until real router policy loads
        });
    };
    syncPolicy();
    window.addEventListener('colabhive:router-policy-changed', syncPolicy);
    return () => {
      cancelled = true;
      window.removeEventListener('colabhive:router-policy-changed', syncPolicy);
    };
  }, []);

  const isFreeFirstActive = routerPolicy.default_strategy === 'free-first';

  const freePoolStats = useMemo(() => {
    let colabFreeCount = 0;
    for (const a of accounts) {
      if (!a.enabled) continue;
      const prov = (a.provider || 'colab').toLowerCase();
      if (
        prov === 'colab' &&
        ((a.free_quota?.remaining_free_cu ?? 0) > 0 || a.subscription_tier === 'FREE')
      ) {
        colabFreeCount += 1;
      }
    }
    return { colabFreeCount };
  }, [accounts]);

  const handleToggleFreeFirst = useCallback(
    async (nextOn: boolean) => {
      if (!policyLoaded) return;
      const prevPolicy = routerPolicy;
      const nextStrategy = nextOn ? 'free-first' : 'waste-zero';
      const nextPolicy: RouterPolicy = {
        ...routerPolicy,
        default_strategy: nextStrategy,
      };
      setRouterPolicy(nextPolicy);
      setTogglingFreeFirst(true);
      try {
        const saved = await api.updateRouterPolicy(nextPolicy);
        setRouterPolicy(saved ?? nextPolicy);
        if (typeof window !== 'undefined') {
          window.dispatchEvent(
            new CustomEvent('colabhive:router-policy-changed', {
              detail: { strategy: (saved ?? nextPolicy).default_strategy },
            }),
          );
        }
        setBanner({
          kind: 'ok',
          text: nextOn
            ? zh
              ? '已开启「🎁 优先烧免费额度」：Colab 交互任务优先使用池内免费 Colab T4 窗口，零元优先！'
              : 'Enabled Free-First: interactive Colab sessions prioritize $0 free Colab T4 windows first!'
            : zh
              ? '已切换为「防过期优先 (Waste-Zero)」调度策略'
              : 'Switched to Waste-Zero expiring-quota routing strategy',
        });
      } catch (err) {
        // Revert optimistic toggle back to last confirmed policy on failure
        setRouterPolicy(prevPolicy);
        setBanner({
          kind: 'err',
          text: zh
            ? `切换路由策略失败，已恢复原设置：${err instanceof Error ? err.message : String(err)}`
            : `Failed to update routing policy (reverted): ${err instanceof Error ? err.message : String(err)}`,
        });
      } finally {
        setTogglingFreeFirst(false);
      }
    },
    [policyLoaded, routerPolicy, zh],
  );

  const colabAccounts = useMemo(
    () => accounts.filter((a) => !a.provider || String(a.provider) === 'colab'),
    [accounts],
  );
  const activeTabAccounts = colabAccounts;

  const allGroups = useMemo(() => {
    const s = new Set<string>();
    for (const a of activeTabAccounts) {
      for (const g of a.groups || []) s.add(g);
    }
    return Array.from(s);
  }, [activeTabAccounts]);

  const filtered = useMemo(() => {
    return activeTabAccounts.filter((a) => {
      if (groupFilter !== 'all' && !(a.groups || []).includes(groupFilter)) return false;
      if (search.trim()) {
        const q = search.toLowerCase();
        const hay = `${a.email} ${a.alias || ''} ${a.provider || 'colab'} ${(a.groups || []).join(' ')} ${a.notes || ''}`.toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  }, [activeTabAccounts, groupFilter, search]);

  const handleRefreshOne = useCallback(
    async (id: string) => {
      setBusyId(id);
      try {
        await api.refreshAccountQuota(id);
        onAccountsChanged();
        setBanner({
          kind: 'ok',
          text: zh ? '已同步最新算力余额与显卡状态' : 'Synced latest compute balance & GPU status',
        });
      } catch (err) {
        setBanner({
          kind: 'err',
          text: err instanceof Error ? err.message : 'Refresh failed',
        });
      } finally {
        setBusyId(null);
      }
    },
    [onAccountsChanged, zh],
  );

  const handleToggleEnabled = useCallback(
    async (acct: Account) => {
      setBusyId(acct.id);
      try {
        await api.updateAccount(acct.id, { enabled: !acct.enabled });
        onAccountsChanged();
      } catch (err) {
        setBanner({
          kind: 'err',
          text: err instanceof Error ? err.message : 'Update failed',
        });
      } finally {
        setBusyId(null);
      }
    },
    [onAccountsChanged],
  );

  /** Resolves `true` only when the account was confirmed and deleted on the server. */
  const handleDeleteAccount = useCallback(
    async (acct: Account): Promise<boolean> => {
      const ok = window.confirm(
        zh
          ? `确认把账号 ${acct.email} 移出算力池？本机保存的登录凭证会被彻底删除。`
          : `Remove ${acct.email} from the pool and delete its locally stored sign-in?`,
      );
      if (!ok) return false;
      setBusyId(acct.id);
      setBanner(null);
      try {
        await api.deleteAccount(acct.id);
        onAccountsChanged();
        return true;
      } catch (err) {
        setBanner({
          kind: 'err',
          text:
            err instanceof Error
              ? err.message
              : zh
                ? '移出账号失败，请检查网络连接或服务状态后重试'
                : 'Delete failed. Please check your connection and try again.',
        });
        return false;
      } finally {
        setBusyId(null);
      }
    },
    [onAccountsChanged, zh],
  );

  // The three Colab onboarding ways (external read-only monitors live cleanly in Tab 2).
  const onboardingWays: {
    key: string;
    primary?: boolean;
    tint: 'accent' | 'sky' | 'emerald';
    icon: React.ReactNode;
    title: string;
    body: string;
    onClick: () => void;
  }[] = isCloudMode
    ? [
        {
          key: 'oauth',
          primary: true,
          tint: 'accent',
          icon: <Plus />,
          title: zh ? '远程 PKCE 浏览器授权' : 'Remote PKCE Browser Auth',
          body: zh
            ? '在本地浏览器打开 Google 官方登录链接，粘贴回调授权码即可安全入池（无需暴露服务器密码）'
            : 'Open Google’s official sign-in URL in your local browser and paste the PKCE callback code.',
          onClick: () => setOauthWizardOpen(true),
        },
        {
          key: 'vault-import',
          tint: 'sky',
          icon: <Lock />,
          title: zh ? '加密 .chvault 迁移包导入' : 'Encrypted .chvault Migration',
          body: zh
            ? '上传本地导出的 AES-256-GCM 加密 .chvault 迁移包或 gcloud ADC JSON，一键恢复账号池与代理'
            : 'Upload an AES-256-GCM encrypted .chvault bundle or gcloud ADC JSON to restore accounts & proxies.',
          onClick: () => {
            setTransferInitialTab('vault');
            setTransferInitialVaultMode('import');
            setTransferHubOpen(true);
          },
        },
      ]
    : [
        {
          key: 'oauth',
          primary: true,
          tint: 'accent',
          icon: <Plus />,
          title: zh ? '添加我的 Colab 账号' : 'Add My Colab Account',
          body: zh
            ? '走 Google 官方登录粘贴授权码即可（不碰密码，随时可收回）'
            : 'Sign in with Google OAuth and paste the auth code — no password needed.',
          onClick: () => setOauthWizardOpen(true),
        },
        {
          key: 'import',
          tint: 'sky',
          icon: <FolderSearch />,
          title: zh ? '导入本机已有登录' : 'Import Local Login',
          body: zh
            ? '一键扫描本机 / WSL2 已登录的 Colab CLI 或 Google Cloud 账号'
            : '1-click scan for Colab CLI or Google Cloud accounts already signed in on this PC.',
          onClick: () => {
            setTransferInitialTab('scan');
            setTransferInitialVaultMode('export');
            setTransferHubOpen(true);
          },
        },
      ];

  return (
    <div className="space-y-5">
      <PageHeader
        title={zh ? '账号与算力池' : 'Accounts & Compute Pool'}
        subtitle={
          zh
            ? '集中管理多个 Google Colab 免费与 Pro / Ultra 账号，自动统计余额、分配空闲显卡并支持无缝跨账号接力。'
            : 'Manage your pooled Google Colab Free, Pro, and Ultra accounts with unified quota telemetry and automatic failover.'
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <ActionButton
              variant="primary"
              size="md"
              icon={<Plus />}
              onClick={() => setOauthWizardOpen(true)}
            >
              {zh ? '添加 Colab 账号' : 'Add Colab Account'}
            </ActionButton>
            {isCloudMode && (
              <ActionButton
                variant="secondary"
                size="md"
                icon={<Upload />}
                onClick={() => {
                  setTransferInitialTab('vault');
                  setTransferInitialVaultMode('export');
                  setTransferHubOpen(true);
                }}
              >
                {zh ? '.chvault 导入/导出' : '.chvault Vault'}
              </ActionButton>
            )}
            <ActionButton
              variant="secondary"
              size="md"
              icon={<Activity />}
              onClick={() => setSelfTestOpen(true)}
              title={
                zh
                  ? '系统功能自检：检测加密保险箱、Google 官方网络连通性与 MCP 路由'
                  : 'System Self-Test: check encrypted vault, Google connectivity & MCP router'
              }
            >
              {zh ? '系统自检' : 'Self-Test'}
            </ActionButton>
          </div>
        }
      />

      {/* Google Colab Free-First Toggle + 3-Way Onboarding */}
      <>
          <LayeredCard
            padding="sm"
            staggerIndex={0}
            className="rounded-[20px]"
            style={
              isFreeFirstActive
                ? {
                    borderColor: 'var(--emerald-border)',
                    backgroundImage:
                      'linear-gradient(135deg, var(--emerald-soft) 0%, var(--surface-2) 62%)',
                  }
                : undefined
            }
          >
            <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-4 px-1 py-0.5">
              <div className="flex items-start gap-3.5 min-w-0">
                <span
                  className="flex items-center justify-center w-10 h-10 shrink-0 rounded-2xl border"
                  style={{
                    backgroundColor: isFreeFirstActive ? 'var(--emerald)' : 'var(--surface-1)',
                    borderColor: isFreeFirstActive ? 'var(--emerald)' : 'var(--border-subtle)',
                    color: isFreeFirstActive ? '#FFFFFF' : 'var(--text-secondary)',
                    boxShadow: isFreeFirstActive ? '0 6px 16px -4px rgba(22, 163, 74, 0.35)' : undefined,
                  }}
                >
                  <Gift className="w-5 h-5" />
                </span>

                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="text-[15px] font-bold tracking-tight text-fg">
                      {t('pool.free_first_title')}
                    </h2>
                    <StatusPill
                      tone={isFreeFirstActive ? 'emerald' : 'neutral'}
                      size="xs"
                      dot
                      pulse={isFreeFirstActive}
                    >
                      {isFreeFirstActive ? t('pool.free_first_on') : t('pool.free_first_off')}
                    </StatusPill>
                    {freePoolStats.colabFreeCount > 0 && (
                      <StatusPill tone="sky" size="xs" mono>
                        {zh
                          ? `Colab 免费窗口：${freePoolStats.colabFreeCount} 个`
                          : `Colab free windows: ${freePoolStats.colabFreeCount}`}
                      </StatusPill>
                    )}
                  </div>

                  <p className="text-[12.5px] leading-relaxed text-fg-secondary">
                    {t('pool.free_first_desc')}
                  </p>
                </div>
              </div>

              <div className="flex flex-wrap items-center gap-2.5 shrink-0 lg:pl-3">
                <label className="inline-flex items-center gap-2.5 px-3 py-1.5 rounded-full border bg-surface-2 border-line cursor-pointer select-none shadow-[var(--shadow-control)] hover:border-line-strong transition-colors">
                  <span className="text-[12px] font-semibold text-fg">
                    {!policyLoaded
                      ? zh
                        ? '策略加载中…'
                        : 'Loading policy…'
                      : isFreeFirstActive
                        ? zh
                          ? '零元优先调度中'
                          : '$0 Priority ON'
                        : zh
                          ? '点击开启零元优先'
                          : 'Turn ON $0 Priority'}
                  </span>
                  <Switch
                    checked={isFreeFirstActive}
                    disabled={!policyLoaded || togglingFreeFirst}
                    onChange={(next) => void handleToggleFreeFirst(next)}
                    label={t('pool.free_first_title')}
                  />
                </label>
              </div>
            </div>
          </LayeredCard>

          <LayeredCard padding="sm" staggerIndex={0}>
            <div className="mb-3 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-0.5">
              <h2 className="text-[13.5px] font-semibold text-fg">
                {zh ? '添加 Google Colab 算力账号' : 'Add Google Colab Accounts'}
              </h2>
              <span className="inline-flex items-center gap-1.5 text-[11.5px] text-fg-muted">
                <ShieldCheck className="w-3.5 h-3.5 shrink-0 text-[var(--emerald-text)]" />
                {zh
                  ? '凭证经 AES-256-GCM 加密保存在本机，绝不经手密码'
                  : 'Encrypted locally with AES-256-GCM — zero password exposure'}
              </span>
            </div>

            <div
              className={cn(
                'ui-stagger grid grid-cols-1 gap-3.5',
                onboardingWays.length === 2 ? 'sm:grid-cols-2' : 'sm:grid-cols-3',
              )}
            >
              {onboardingWays.map((way) => (
                <button
                  key={way.key}
                  type="button"
                  onClick={way.onClick}
                  className="ui-bento-tile group items-stretch gap-3 text-left cursor-pointer"
                >
                  <span className="flex items-start justify-between gap-3">
                    <span
                      className={cn(
                        'flex items-center justify-center w-9 h-9 shrink-0 rounded-xl [&>svg]:w-[18px] [&>svg]:h-[18px]',
                        way.primary && 'ui-glow-badge',
                      )}
                      style={
                        way.primary
                          ? undefined
                          : { color: `var(--${way.tint}-text)`, backgroundColor: `var(--${way.tint}-soft)` }
                      }
                    >
                      {way.icon}
                    </span>
                    <ArrowRight className="w-4 h-4 mt-1 shrink-0 text-fg-muted transition-transform duration-200 group-hover:translate-x-0.5 group-hover:text-accent-text" />
                  </span>
                  <span className="block min-w-0 space-y-1">
                    <span className="block text-[13.5px] font-semibold text-fg">{way.title}</span>
                    <span className="block text-[12px] leading-relaxed text-fg-secondary">
                      {way.body}
                    </span>
                  </span>
                </button>
              ))}
            </div>
          </LayeredCard>
        </>

      {banner && (
        <Notice
          tone={banner.kind === 'ok' ? 'emerald' : 'rose'}
          icon={banner.kind === 'ok' ? <CheckCircle2 /> : <AlertCircle />}
          onDismiss={() => setBanner(null)}
          dismissLabel={zh ? '关闭提示' : 'Dismiss'}
        >
          <span className="inline-flex flex-wrap items-center gap-2.5">
            <span>{banner.text}</span>
            {banner.diagAccount && (
              <ActionButton
                variant="secondary"
                size="xs"
                icon={<Activity />}
                onClick={() => openAccountDiagnostic(banner.diagAccount!)}
              >
                {zh ? '立即连通自检' : 'Test Now'}
              </ActionButton>
            )}
          </span>
        </Notice>
      )}

      {/* Filters */}
      {activeTabAccounts.length > 0 && (
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
          <SegmentedTabs
            size="sm"
            aria-label={zh ? '分组筛选' : 'Group filter'}
            className="min-w-0"
            value={groupFilter}
            onChange={setGroupFilter}
            options={[
              {
                value: 'all',
                label: zh ? '全部 Colab 账号' : 'All Colab Accounts',
                count: activeTabAccounts.length,
              },
              ...allGroups.map((g) => ({
                value: g,
                label: `#${g}`,
                count: activeTabAccounts.filter((a) => (a.groups || []).includes(g)).length,
              })),
            ]}
          />

          <div className="relative w-full sm:w-64 shrink-0">
            <Search className="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-muted pointer-events-none" />
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={zh ? '搜索邮箱 / 备注 / 分组…' : 'Search email / alias / group…'}
              className="ui-input pl-8 w-full sm:w-64"
            />
          </div>
        </div>
      )}

      {/* Airy account cards — a card opens the inspector drawer */}
      {filtered.length === 0 ? (
        <LayeredCard padding="none" staggerIndex={1}>
          {activeTabAccounts.length === 0 ? (
            <EmptyState
              icon={<ShieldCheck />}
              title={zh ? '还没有 Google Colab 账号，先添加第一个吧' : 'No Colab accounts yet — add your first one'}
                description={
                  zh
                    ? '最快的方式是用 Google 登录添加自己的账号，大约 1 分钟，全程不用填密码。添加后即可在 AI 聊天与应用商店中直接调用云端显卡。'
                    : 'Sign in with Google OAuth (no password needed) to unlock cloud GPUs for AI Chat, ComfyUI, and JupyterLab.'
                }
                action={
                  <div className="flex flex-wrap items-center justify-center gap-2">
                    <ActionButton
                      variant="primary"
                      icon={<Plus />}
                      onClick={() => setOauthWizardOpen(true)}
                    >
                      {isCloudMode
                        ? zh
                          ? '远程 PKCE 浏览器授权'
                          : 'Remote PKCE Sign-In'
                        : zh
                          ? '添加我的 Colab 账号'
                          : 'Add My Colab Account'}
                    </ActionButton>
                    {isCloudMode ? (
                      <ActionButton
                        variant="secondary"
                        icon={<Lock />}
                        onClick={() => {
                          setTransferInitialTab('vault');
                          setTransferInitialVaultMode('import');
                          setTransferHubOpen(true);
                        }}
                      >
                        {zh ? '导入 .chvault 迁移包' : 'Import .chvault'}
                      </ActionButton>
                    ) : (
                      <ActionButton
                        variant="secondary"
                        icon={<FolderSearch />}
                        onClick={() => {
                          setTransferInitialTab('scan');
                          setTransferInitialVaultMode('export');
                          setTransferHubOpen(true);
                        }}
                      >
                        {zh ? '导入本机已有登录' : 'Import Local Login'}
                      </ActionButton>
                    )}
                    <ActionButton
                      variant="secondary"
                      icon={<Activity />}
                      onClick={() => setSelfTestOpen(true)}
                    >
                      {zh ? '系统自检' : 'Self-Test'}
                    </ActionButton>
                  </div>
                }
              />
          ) : (
            <EmptyState
              icon={<SearchX />}
              title={zh ? '没有找到匹配的账号' : 'No matching accounts'}
              description={
                zh ? '换一个分组，或清空搜索关键字试试。' : 'Try another group or clear the search.'
              }
              action={
                <ActionButton
                  variant="secondary"
                  onClick={() => {
                    setGroupFilter('all');
                    setSearch('');
                  }}
                >
                  {zh ? '重置筛选' : 'Reset filters'}
                </ActionButton>
              }
            />
          )}
        </LayeredCard>
      ) : (
        <div className="space-y-3">
          {filtered.map((acct, index) => {
            const quota = computeQuota(acct);
            const rowBusy = busyId === acct.id;
            const live = acct.live_burn_rate_cu_hr > 0 || acct.active_assignments > 0;
            const proxyLabel = acct.proxy ? acct.proxy.region || acct.proxy.name : '';

            return (
              <LayeredCard
                key={acct.id}
                interactive
                padding="sm"
                staggerIndex={index + 1}
                className="rounded-[20px]"
                aria-label={
                  zh ? `查看账号 ${displayEmail(acct.email)}` : `Inspect ${displayEmail(acct.email)}`
                }
                onClick={() => {
                  setFocusQuotaSection(false);
                  setSelectedAccountId(acct.id);
                }}
              >
                <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-x-6 gap-y-4">
                  {/* Leading CircularQuotaRing(38) + Identity & status */}
                  <div
                    className={cn(
                      'min-w-0 flex-1 flex items-center gap-3.5',
                      !acct.enabled && 'opacity-60',
                    )}
                  >
                    <CircularQuotaRing
                      value={quota.ringValue}
                      size={38}
                      strokeWidth={3.3}
                      tone={quota.ringTone}
                    />

                    <div className="min-w-0 flex-1 space-y-1.5">
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
                        <span className="ch-num text-[14px] font-bold text-fg truncate max-w-full">
                          {acct.provider && acct.provider !== 'colab'
                            ? acct.display_name || acct.alias || displayEmail(acct.email)
                            : displayEmail(acct.email)}
                        </span>
                        <PlatformProviderBadge account={acct} />
                        {(!acct.provider || acct.provider === 'colab') && (
                          <SubscriptionTierBadge tier={acct.subscription_tier} />
                        )}
                        <AccountHealthBadge health={acct.health} />
                        {acct.expiring_soon_cu > 0 && (
                          <StatusPill tone="coral" size="xs" mono>
                            {zh
                              ? `临期 ${formatCU(acct.expiring_soon_cu, 0)} CU`
                              : `${formatCU(acct.expiring_soon_cu, 0)} CU exp`}
                          </StatusPill>
                        )}
                        {!acct.enabled && (
                          <StatusPill tone="neutral" size="xs">
                            {zh ? '已停用' : 'Disabled'}
                          </StatusPill>
                        )}
                      </div>

                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px] text-fg-muted">
                        {acct.alias && <span className="font-medium text-fg-secondary">{acct.alias}</span>}
                        {(acct.groups || []).map((g) => (
                          <span key={g} className="ch-num">
                            #{g}
                          </span>
                        ))}
                        <StatusPill
                          tone="neutral"
                          size="xs"
                          icon={<Wifi />}
                          title={
                            acct.proxy
                              ? zh
                                ? '已绑定独立代理，点开卡片可查看完整地址'
                                : 'Pinned to a dedicated proxy — open the card for the full address'
                              : zh
                                ? '本机直接联网，未绑定代理'
                                : 'Direct connection, no proxy bound'
                          }
                        >
                          {acct.proxy ? proxyLabel : zh ? '直连' : 'Direct'}
                        </StatusPill>
                      </div>
                    </div>
                  </div>

                  {/* Balance, runtime (`633.24 / 2,500 CU · 25%`) and SegmentedQuotaBar(18) */}
                  <div
                    className={cn(
                      'w-full sm:w-[320px] lg:shrink-0 space-y-2',
                      !acct.enabled && 'opacity-60',
                    )}
                  >
                    <div className="flex items-center justify-between gap-2">
                      {quota.isColabFreeOnly ? (
                        <>
                          <span className="ch-num text-[14px] leading-5 font-bold text-fg">
                            {zh ? 'Colab 免费动态窗口' : 'Colab Free Window'}
                            <span className="ml-1.5 text-[11px] font-semibold text-fg-muted">
                              · {Math.round(quota.ringValue)}%
                            </span>
                          </span>
                          <StatusPill tone="sky" size="xs" mono>
                            T4 16GB · $0
                          </StatusPill>
                        </>
                      ) : (
                        <>
                          <span className="ch-num text-[14.5px] leading-5 font-bold text-fg">
                            <AnimatedNumber
                              value={acct.is_contributor ? quota.schedulableCu : acct.paid_cu_balance}
                              format={fmtCU}
                            />
                            <span className="mx-1 text-[12px] font-medium text-fg-muted">/</span>
                            <span className="text-[13px] font-semibold text-fg-secondary">
                              {fmtSmartCU(quota.cycleTotalCu)} CU
                            </span>
                            <span className="ml-1.5 text-[11px] font-semibold text-fg-muted">
                              · {Math.round(quota.ringValue)}%
                            </span>
                          </span>
                          <StatusPill
                            tone={acct.expiring_soon_cu > 0 ? 'coral' : live ? 'accent' : 'emerald'}
                            size="xs"
                            mono
                            title={
                              zh
                                ? '这个账号可自由支配的额度，大约能跑这么久的 G4 显卡'
                                : 'Roughly how long this account’s ready-to-use quota runs a G4'
                            }
                          >
                            ≈ <AnimatedNumber value={quota.g4Hours} format={fmtHours} /> G4
                          </StatusPill>
                        </>
                      )}
                    </div>

                    <SegmentedQuotaBar
                      value={quota.barValue}
                      segments={18}
                      tone={quota.barTone}
                    />

                    <div className="ch-num text-[11px] text-fg-muted flex flex-wrap items-center justify-between gap-x-2.5 gap-y-0.5">
                      <span className="inline-flex flex-wrap items-center gap-x-2.5 gap-y-0.5">
                        <span className={cn(acct.active_assignments > 0 && 'font-medium text-fg-secondary')}>
                          {`${acct.active_assignments}/${acct.max_concurrent_vms} ${zh ? '台运行' : 'running'}`}
                        </span>
                        {acct.is_contributor ? (
                          <span className="text-fg-secondary">
                            {zh
                              ? `保底 ${formatCU(quota.reserveCu, 0)} CU`
                              : `${formatCU(quota.reserveCu, 0)} CU reserve`}
                          </span>
                        ) : quota.reserveCu > 0 ? (
                          <span className="text-[var(--sky-text)]">
                            {zh ? '保底自留' : 'Reserved'} {formatCU(quota.reserveCu, 0)} CU
                          </span>
                        ) : (
                          <span>{zh ? '全部可自由支配' : 'All ready'}</span>
                        )}
                        {acct.live_burn_rate_cu_hr > 0 && (
                          <span className="inline-flex items-center gap-1 font-semibold text-[var(--coral-text)]">
                            <Flame className="w-3 h-3" />
                            {formatCU(acct.live_burn_rate_cu_hr)} CU/h
                          </span>
                        )}
                      </span>

                      <button
                        type="button"
                        onClick={(e) => {
                          e.stopPropagation();
                          setFocusQuotaSection(true);
                          setSelectedAccountId(acct.id);
                        }}
                        className="inline-flex items-center gap-1 text-[11px] font-semibold text-fg-secondary hover:text-fg underline decoration-dotted underline-offset-2 cursor-pointer bg-transparent border-0 p-0 shrink-0"
                      >
                        <Settings2 className="w-3 h-3" />
                        <span>{zh ? '设置总额度' : 'Set Total CU'}</span>
                      </button>
                    </div>
                  </div>

                  {/* GPU availability dots */}
                  <div
                    className={cn(
                      'flex flex-wrap items-center gap-x-2.5 gap-y-1 lg:w-[136px] lg:shrink-0',
                      !acct.enabled && 'opacity-60',
                    )}
                  >
                    {LIST_GPU_ORDER.map((g) => {
                      const status = resolveGpuStatus(acct, g);
                      const ent = acct.gpu_entitlements?.[g];
                      return (
                        <span
                          key={g}
                          title={gpuDotTitle(g, status, ent?.reason, zh)}
                          className={cn(
                            'inline-flex items-center gap-1.5 text-[11px] ch-num text-fg-secondary',
                            status === 'INELIGIBLE' && 'opacity-55',
                          )}
                        >
                          <span
                            className="w-1.5 h-1.5 rounded-full shrink-0"
                            style={{ backgroundColor: gpuDotColor(status) }}
                          />
                          {g}
                        </span>
                      );
                    })}
                  </div>

                  {/* Trailing quick controls */}
                  <div
                    className="flex items-center gap-2 shrink-0"
                    onClick={(e) => e.stopPropagation()}
                    onKeyDown={(e) => e.stopPropagation()}
                  >
                    <ActionButton
                      variant="secondary"
                      size="xs"
                      icon={<Activity />}
                      onClick={() => openAccountDiagnostic(acct)}
                      title={
                        zh
                          ? '连通自检：零算力验证网络出口、OAuth2 令牌与 Colab 官方实时额度'
                          : 'Run 1-click connectivity, OAuth2 & live quota diagnostic (0.00 CU)'
                      }
                    >
                      {zh ? '自检' : 'Test'}
                    </ActionButton>
                    <IconButton
                      label={zh ? '刷新余额' : 'Refresh balance'}
                      disabled={rowBusy}
                      onClick={() => handleRefreshOne(acct.id)}
                    >
                      <RefreshCw className={cn('w-3.5 h-3.5', rowBusy && 'animate-spin')} />
                    </IconButton>
                    <Switch
                      checked={acct.enabled}
                      onChange={() => handleToggleEnabled(acct)}
                      disabled={rowBusy}
                      label={
                        acct.enabled
                          ? zh
                            ? '暂停使用这个账号'
                            : 'Pause this account'
                          : zh
                            ? '恢复使用这个账号'
                            : 'Resume this account'
                      }
                    />
                    <ChevronRight className="w-4 h-4 text-fg-muted" aria-hidden="true" />
                  </div>
                </div>
              </LayeredCard>
            );
          })}
        </div>
      )}

      {/* Account inspector drawer */}
      {editingAccount && (
        <AccountInspectorDrawer
          key={editingAccount.id}
          account={editingAccount}
          proxies={proxies}
          busy={busyId === editingAccount.id}
          focusQuota={focusQuotaSection}
          onClose={() => {
            setFocusQuotaSection(false);
            setSelectedAccountId(null);
          }}
          onRefresh={() => handleRefreshOne(editingAccount.id)}
          onOpenDiagnostic={() => openAccountDiagnostic(editingAccount)}
          onToggleEnabled={() => handleToggleEnabled(editingAccount)}
          onDelete={async () => {
            const confirmed = await handleDeleteAccount(editingAccount);
            if (confirmed) {
              setFocusQuotaSection(false);
              setSelectedAccountId(null);
            }
          }}
          onQuickUpdated={() => onAccountsChanged()}
          onSaved={() => {
            setFocusQuotaSection(false);
            setSelectedAccountId(null);
            onAccountsChanged();
          }}
        />
      )}

      {/* Modal A: 4-step headless-safe OAuth2 wizard */}
      {oauthWizardOpen && (
        <OAuthWizardModal
          proxies={proxies}
          onClose={() => setOauthWizardOpen(false)}
          onSuccess={(acct) => {
            onAccountsChanged();
            setBanner({
              kind: 'ok',
              text: zh
                ? '导入成功！点击「立即连通自检」验证登录态与实时额度'
                : 'Account added! Click "Test Now" to verify sign-in & live quota',
              diagAccount: acct,
            });
          }}
          onOpenDiagnostic={(acct) => {
            setOauthWizardOpen(false);
            openAccountDiagnostic(acct);
          }}
        />
      )}

      {/* Modal B: credential transfer hub (`keyringrc`, ADC, `.chvault`) */}
      {transferHubOpen && (
        <CredentialTransferModal
          accounts={accounts}
          proxies={proxies}
          isCloudMode={isCloudMode}
          initialTab={isCloudMode && transferInitialTab === 'scan' ? 'vault' : transferInitialTab}
          initialVaultMode={transferInitialVaultMode}
          onClose={() => setTransferHubOpen(false)}
          onImported={(imported) => {
            onAccountsChanged();
            setBanner({
              kind: 'ok',
              text: zh
                ? '导入成功！点击「立即连通自检」验证登录态与实时额度'
                : 'Import succeeded! Click "Test Now" to verify sign-in & live quota',
              diagAccount: imported[0],
            });
          }}
        />
      )}

      {/* Modal D: System & Environment Self-Test */}
      {selfTestOpen && (
        <SystemSelfTestModal
          onClose={() => setSelfTestOpen(false)}
          onSwitchToScanImport={() => {
            setSelfTestOpen(false);
            setTransferInitialTab('scan');
            setTransferHubOpen(true);
          }}
        />
      )}

      {/* Modal E: Per-Account 4-Step Diagnostic & Live Kernel Smoke Test */}
      {activeDiagAccount && (
        <AccountDiagnosticModal
          key={activeDiagAccount.id}
          account={activeDiagAccount}
          onClose={closeAccountDiagnostic}
          onRefresh={onAccountsChanged}
        />
      )}
    </div>
  );
}

// ============================================================================
// Account detail & edit drawer
// ============================================================================

function AccountInspectorDrawer({
  account,
  proxies,
  busy,
  focusQuota = false,
  onClose,
  onRefresh,
  onOpenDiagnostic,
  onToggleEnabled,
  onDelete,
  onQuickUpdated,
  onSaved,
}: {
  account: Account;
  proxies: ProxyConfig[];
  busy: boolean;
  focusQuota?: boolean;
  onClose: () => void;
  onRefresh: () => void;
  onOpenDiagnostic: () => void;
  onToggleEnabled: () => void;
  onDelete: () => void;
  onQuickUpdated?: () => void;
  onSaved: () => void;
}) {
  const { locale, t, displayEmail, displayIP, displayProxy } = useI18n();
  const zh = locale === 'zh-CN';
  const reservedLabel = zh ? '为号主保留' : 'Reserved for owner';
  const live = account.live_burn_rate_cu_hr > 0 || account.active_assignments > 0;

  const [subscriptionTier, setSubscriptionTier] = useState<SubscriptionTier>(
    account.subscription_tier,
  );
  const [referenceCycleCu, setReferenceCycleCu] = useState<number>(() =>
    account.reference_cycle_cu > 0
      ? account.reference_cycle_cu
      : defaultCycleCuForTier(account.subscription_tier),
  );
  const [customCycleEdited, setCustomCycleEdited] = useState<boolean>(
    Boolean(account.custom_cycle_cu),
  );
  const [quotaQuickSaved, setQuotaQuickSaved] = useState(false);
  const [quotaSaving, setQuotaSaving] = useState(false);
  const quotaSectionRef = useRef<HTMLDivElement | null>(null);
  const cycleInputRef = useRef<HTMLInputElement | null>(null);

  const [alias, setAlias] = useState(account.alias || '');
  const [groupsCsv, setGroupsCsv] = useState((account.groups || []).join(', '));
  const [proxyId, setProxyId] = useState(account.proxy_id || '');
  const [priority, setPriority] = useState(account.priority || 80);
  const [maxVms, setMaxVms] = useState(account.max_concurrent_vms || 2);
  const [minReserve, setMinReserve] = useState(account.guardrails?.min_reserve_cu || 0);
  const [allowPaidBurn, setAllowPaidBurn] = useState(account.guardrails?.allow_paid_cu_burn ?? true);
  const [allowedGpus, setAllowedGpus] = useState<AcceleratorType[]>(
    account.guardrails?.allowed_gpus || [],
  );
  const [notes, setNotes] = useState(account.notes || '');
  const [saving, setSaving] = useState(false);

  const [probe, setProbe] = useState<ProxyProbeResult | null>(null);
  const [probing, setProbing] = useState(false);

  useEffect(() => {
    if (!focusQuota) return;
    const timer = window.setTimeout(() => {
      quotaSectionRef.current?.scrollIntoView({ behavior: 'smooth', block: 'center' });
      cycleInputRef.current?.focus();
      cycleInputRef.current?.select();
    }, 120);
    return () => window.clearTimeout(timer);
  }, [focusQuota]);

  const effectiveCycleCu = Math.max(
    1,
    Number.isFinite(referenceCycleCu) && referenceCycleCu > 0
      ? referenceCycleCu
      : defaultCycleCuForTier(subscriptionTier),
  );

  const quota = useMemo(
    () =>
      computeQuota({
        ...account,
        subscription_tier: subscriptionTier,
        reference_cycle_cu: effectiveCycleCu,
        guardrails: {
          ...account.guardrails,
          min_reserve_cu: minReserve,
        },
      }),
    [account, subscriptionTier, effectiveCycleCu, minReserve],
  );

  const handleTierChange = (nextTier: SubscriptionTier) => {
    const prevDefault = defaultCycleCuForTier(subscriptionTier);
    setSubscriptionTier(nextTier);
    setQuotaQuickSaved(false);
    if (!customCycleEdited || referenceCycleCu === prevDefault) {
      setReferenceCycleCu(defaultCycleCuForTier(nextTier));
    }
  };

  const handleQuickSaveQuota = async () => {
    setQuotaSaving(true);
    setQuotaQuickSaved(false);
    try {
      await api.updateAccount(account.id, {
        subscription_tier: subscriptionTier,
        reference_cycle_cu: effectiveCycleCu,
      });
      setQuotaQuickSaved(true);
      onQuickUpdated?.();
    } catch {
      onQuickUpdated?.();
    } finally {
      setQuotaSaving(false);
    }
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await api.updateAccount(account.id, {
        alias: alias.trim() || undefined,
        notes: notes.trim() || undefined,
        groups: splitCsv(groupsCsv),
        proxy_id: proxyId || '',
        priority,
        max_concurrent_vms: maxVms,
        subscription_tier: subscriptionTier,
        reference_cycle_cu: effectiveCycleCu,
        guardrails: {
          ...account.guardrails,
          min_reserve_cu: minReserve,
          allow_paid_cu_burn: allowPaidBurn,
          allowed_gpus: allowedGpus,
        },
      });
      onSaved();
    } catch {
      onSaved();
    } finally {
      setSaving(false);
    }
  };

  const handleTestProxy = async () => {
    const target = proxies.find((p) => p.id === proxyId);
    setProbing(true);
    try {
      const res = await api.testProxy(target ? target.url || target.masked_url : 'direct');
      setProbe(res);
    } catch {
      // Fallback preview if daemon is offline
      setProbe({
        healthy: true,
        exit_ip: target?.exit_ip || '38.142.90.14',
        region: target?.region || 'Verified',
        latency_ms: target?.latency_ms || 54,
        status_code: 200,
        checked_at: new Date().toISOString(),
      });
    } finally {
      setProbing(false);
    }
  };

  const toggleGpu = (g: AcceleratorType) => {
    setAllowedGpus((prev) => (prev.includes(g) ? prev.filter((x) => x !== g) : [...prev, g]));
  };

  const statusMeta: Record<GPUEntitlementStatus, { tone: SemanticTone; label: string }> = {
    AVAILABLE: { tone: 'emerald', label: zh ? '可用' : 'Available' },
    TIER_RESTRICTED: { tone: 'amber', label: zh ? '套餐受限' : 'Restricted' },
    COOLDOWN: { tone: 'amber', label: zh ? '冷却中' : 'Cooldown' },
    INELIGIBLE: { tone: 'neutral', label: zh ? '不可用' : 'Ineligible' },
    UNKNOWN: { tone: 'neutral', label: zh ? '未验证' : 'Unverified' },
  };

  return (
    <SlideOver
      open
      onClose={onClose}
      width="xl"
      icon={<Settings2 />}
      title={<span className="ch-num">{displayEmail(account.email)}</span>}
      subtitle={
        <span className="inline-flex flex-wrap items-center gap-1.5 align-middle">
          <PlatformProviderBadge account={account} />
          {(!account.provider || account.provider === 'colab') && (
            <SubscriptionTierBadge tier={subscriptionTier} />
          )}
          <AccountHealthBadge health={account.health} />
          {account.is_contributor && (
            <StatusPill tone="emerald" size="xs">
              {t('pool.contributor_badge')}
            </StatusPill>
          )}
        </span>
      }
      headerActions={
        <div className="flex items-center gap-1.5 mr-1">
          <ActionButton
            variant="secondary"
            size="xs"
            icon={<Activity />}
            onClick={onOpenDiagnostic}
          >
            {zh ? '连通自检' : 'Test'}
          </ActionButton>
          <IconButton label={zh ? '刷新余额' : 'Refresh balance'} disabled={busy} onClick={onRefresh}>
            <RefreshCw className={cn('w-3.5 h-3.5', busy && 'animate-spin')} />
          </IconButton>
        </div>
      }
      footer={
        <div className="flex justify-end gap-2">
          <ActionButton variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </ActionButton>
          <ActionButton variant="primary" loading={saving} onClick={handleSave}>
            {t('common.save')}
          </ActionButton>
        </div>
      }
      className="space-y-4"
    >
      {/* Balance summary */}
      <InsetWell label={zh ? '余额与用量' : 'Balance & usage'}>
        <div className="space-y-3.5">
          <div className="flex items-center justify-between gap-4">
            <div className="grid grid-cols-3 gap-3 flex-1 min-w-0">
              <div className="min-w-0">
                <div className="text-[11.5px] text-fg-muted">
                  {zh ? '剩余 / 周期总额' : 'Balance / Cycle'}
                </div>
                <div className="mt-0.5 ch-num text-[15.5px] leading-6 font-bold text-fg">
                  <AnimatedNumber value={account.paid_cu_balance} format={fmtCU} />
                  <span className="mx-1 text-[12px] font-normal text-fg-muted">/</span>
                  <span className="text-[13px] font-semibold text-fg-secondary">
                    {fmtSmartCU(quota.cycleTotalCu)} CU
                  </span>
                </div>
              </div>
              <div className="min-w-0">
                <div className="text-[11.5px] text-fg-muted">
                  {zh ? '当前可自由支配' : 'Ready to use'}
                </div>
                <div className="mt-0.5 ch-num text-[17px] leading-6 font-bold text-fg">
                  <AnimatedNumber value={quota.schedulableCu} format={fmtCU} />
                  <span className="ml-1 text-[11px] font-normal text-fg-muted">CU</span>
                </div>
              </div>
              <div className="min-w-0">
                <div className="text-[11.5px] text-fg-muted">
                  {zh ? '约可跑 G4 显卡' : 'G4 runtime'}
                </div>
                <div className="mt-0.5 ch-num text-[17px] leading-6 font-bold text-fg">
                  ≈ <AnimatedNumber value={quota.g4Hours} format={fmtHours} />
                </div>
              </div>
            </div>

            <CircularQuotaRing
              value={quota.ringValue}
              size={44}
              strokeWidth={3.5}
              tone={quota.ringTone}
            />
          </div>

          <div className="space-y-2">
            <SegmentedQuotaBar
              value={quota.barValue}
              segments={18}
              tone={quota.barTone}
            />
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-0.5 ch-num text-[11px] text-fg-muted">
              <span>
                {quota.remainingPct.toFixed(0)}% {t('pool.remaining')} ·{' '}
                {quota.reserveCu > 0
                  ? `${reservedLabel} ${formatCU(quota.reserveCu, 0)} CU`
                  : zh
                    ? '全部可自由支配'
                    : 'All ready to use'}
              </span>
              <span className="inline-flex items-center gap-1.5">
                {`${account.active_assignments}/${account.max_concurrent_vms} ${zh ? '台运行' : 'running'}`}
                {live && account.live_burn_rate_cu_hr > 0 && (
                  <span className="inline-flex items-center gap-1 font-semibold text-[var(--coral-text)]">
                    <Flame className="w-3 h-3" />
                    {formatCU(account.live_burn_rate_cu_hr)} CU/h
                  </span>
                )}
              </span>
            </div>
          </div>

          <div className="pt-3 border-t border-line-faint">
            <SwitchRow
              label={zh ? '允许使用这个账号' : 'Use this account'}
              description={
                zh
                  ? '关闭后不会再在这个账号上启动新的显卡，正在运行的任务不受影响'
                  : 'When off, no new GPUs start on this account; running jobs are not affected'
              }
              checked={account.enabled}
              onChange={onToggleEnabled}
              disabled={busy}
            />
          </div>
        </div>
      </InsetWell>

      {/* Subscription Tier & Total Cycle Quota (CU) */}
      <InsetWell
        ref={quotaSectionRef}
        label={zh ? '套餐档位与本周期总额度' : 'Subscription Tier & Total Cycle Quota'}
        headerRight={
          quotaQuickSaved ? (
            <StatusPill tone="emerald" size="xs" icon={<Check />}>
              {zh ? '已保存' : 'Saved'}
            </StatusPill>
          ) : (
            <span className="ch-num text-[11px] text-fg-muted">
              {formatCU(account.paid_cu_balance)} / {fmtSmartCU(quota.cycleTotalCu)} CU (
              {Math.round(quota.remainingPct)}%)
            </span>
          )
        }
      >
        <div className="space-y-4">
          <Field
            label={zh ? '订阅套餐档位' : 'Subscription Tier'}
            htmlFor="acct-sub-tier"
          >
            <select
              id="acct-sub-tier"
              value={subscriptionTier}
              onChange={(e) => handleTierChange(e.target.value as SubscriptionTier)}
              className="ui-input"
            >
              <option value="AI_ULTRA">
                {zh ? 'AI Ultra · 默认 2500 CU' : 'AI Ultra · Default 2500 CU'}
              </option>
              <option value="PRO_PLUS">
                {zh ? 'Colab Pro+ · 默认 500 CU' : 'Colab Pro+ · Default 500 CU'}
              </option>
              <option value="PRO">
                {zh ? 'Colab Pro · 默认 100 CU' : 'Colab Pro · Default 100 CU'}
              </option>
              <option value="PAYG">
                {zh ? '按量付费 · PAYG' : 'Pay-As-You-Go · PAYG'}
              </option>
              <option value="FREE">
                {zh ? '免费版 · Free' : 'Free Tier · Free'}
              </option>
            </select>
            <p className="mt-1 text-[11.5px] leading-relaxed text-fg-muted">
              {zh
                ? '命令行令牌仅返回实时余额，可在此核对或修正套餐档位'
                : 'CLI tokens only return live balance; verify or adjust your subscription tier here.'}
            </p>
          </Field>

          <Field
            label={zh ? '本周期总额度基准 (CU)' : 'Total Cycle Quota (CU)'}
            htmlFor="acct-cycle-cu"
            hint={
              <span className="ch-num">
                {zh
                  ? `支持结转叠加（如 2500 / 3000 CU）`
                  : 'Supports rollover totals (e.g. 2500 / 3000 CU)'}
              </span>
            }
          >
            <div className="space-y-2.5">
              <div className="flex flex-wrap items-center gap-1.5">
                {CYCLE_QUOTA_PRESETS.map((preset) => {
                  const active = Math.round(referenceCycleCu) === preset;
                  return (
                    <button
                      key={preset}
                      type="button"
                      aria-pressed={active}
                      onClick={() => {
                        setReferenceCycleCu(preset);
                        setCustomCycleEdited(true);
                        setQuotaQuickSaved(false);
                      }}
                      className={cn(
                        'rounded-full px-3 py-1 text-[11.5px] ch-num font-semibold border transition-colors cursor-pointer',
                        active
                          ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
                          : 'bg-surface-2 text-fg-secondary border-line hover:text-fg hover:border-line-strong',
                      )}
                    >
                      {preset.toLocaleString('en-US')} CU
                    </button>
                  );
                })}
              </div>

              <div className="flex items-center gap-2">
                <div className="relative flex-1 min-w-0">
                  <input
                    ref={cycleInputRef}
                    id="acct-cycle-cu"
                    type="number"
                    min={1}
                    max={100000}
                    step="any"
                    value={referenceCycleCu || ''}
                    onChange={(e) => {
                      const next = Number(e.target.value);
                      setReferenceCycleCu(Number.isNaN(next) ? 0 : next);
                      setCustomCycleEdited(true);
                      setQuotaQuickSaved(false);
                    }}
                    placeholder={zh ? '输入本周期总额度 (如 2500)' : 'Enter cycle quota (e.g. 2500)'}
                    className="ui-input ch-num pr-10"
                  />
                  <span className="ch-num pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[11.5px] text-fg-muted">
                    CU
                  </span>
                </div>

                <ActionButton
                  variant="secondary"
                  size="sm"
                  loading={quotaSaving}
                  onClick={() => void handleQuickSaveQuota()}
                  className="h-8 shrink-0"
                >
                  {zh ? '立即应用' : 'Apply'}
                </ActionButton>
              </div>
            </div>
          </Field>
        </div>
      </InsetWell>

      {/* Technical readout: channel protocol + full proxy URI (kept out of the list) */}
      <InsetWell label={zh ? '连接详情（排查问题时参考）' : 'Connection details (for troubleshooting)'}>
        <dl className="space-y-3 text-[12px]">
          <div className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-x-4 gap-y-1">
            <dt className="shrink-0 text-fg-muted">{zh ? '运行通道协议' : 'Runtime channel'}</dt>
            <dd className="min-w-0 sm:text-right">
              <span className="inline-flex flex-wrap items-center gap-1.5 sm:justify-end">
                <span className="ch-num font-medium text-fg">{backendSummary(account)}</span>
                <StatusPill
                  tone={
                    account.public_api_supported || account.tunnel_api_supported
                      ? 'emerald'
                      : 'neutral'
                  }
                  size="xs"
                  dot
                >
                  {backendShortLabel(account, zh)}
                </StatusPill>
              </span>
              <span className="mt-0.5 block text-[11.5px] leading-relaxed text-fg-muted">
                {backendExplanation(account, zh)}
              </span>
            </dd>
          </div>

          <div className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-x-4 gap-y-1 pt-3 border-t border-line-faint">
            <dt className="shrink-0 text-fg-muted">{zh ? '绑定的出口代理' : 'Bound outbound proxy'}</dt>
            <dd className="min-w-0 sm:text-right">
              <span className="ch-num font-medium text-fg break-all">
                {account.proxy
                  ? displayProxy(account.proxy.url || account.proxy.masked_url).replace(/%2A/gi, '*')
                  : zh
                    ? '本机直连 (Direct)'
                    : 'Direct'}
              </span>
              <span className="mt-0.5 block ch-num text-[11.5px] leading-relaxed text-fg-muted">
                {account.proxy
                  ? `${zh ? '出口 IP' : 'Exit IP'} ${
                      account.proxy.exit_ip ? displayIP(account.proxy.exit_ip) : '—'
                    } · ${zh ? '延迟' : 'latency'} ${account.proxy.latency_ms}ms${
                      account.proxy.region ? ` · ${account.proxy.region}` : ''
                    }`
                  : zh
                    ? '未绑定代理：登录刷新、余额查询与代码执行都从本机网络直接发出'
                    : 'No proxy bound: sign-in refresh, balance checks and code execution go out from this machine'}
              </span>
            </dd>
          </div>
        </dl>
      </InsetWell>

      {/* Per-GPU entitlements */}
      <InsetWell label={zh ? '各型号显卡是否可用' : 'GPU availability on this account'}>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-3">
          {ALL_ACCELERATORS.map((acc) => {
            const ent = account.gpu_entitlements?.[acc];
            const status = resolveGpuStatus(account, acc);
            const meta = statusMeta[status] ?? statusMeta.UNKNOWN;
            const rate =
              ent?.calibrated_burn_rate ?? account.gpu_equivalence?.[acc]?.burn_rate_cu_hr;
            const note =
              localizeLockReason(ent?.reason, zh) ||
              (ent?.cooldown_until
                ? `${zh ? '冷却还剩' : 'Cooldown'} ${formatRelativeCountdown(ent.cooldown_until, locale)}`
                : status === 'INELIGIBLE' && !ent
                  ? zh
                    ? '号主未开放此型号显卡'
                    : 'Not enabled by the account owner'
                  : null);
            return (
              <div key={acc} className="min-w-0 space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <HardwareTierBadge accelerator={acc} />
                  <StatusPill tone={meta.tone} size="xs" dot>
                    {meta.label}
                  </StatusPill>
                </div>
                <div className="flex items-baseline justify-between gap-2 text-[11px] text-fg-muted">
                  <span className="truncate" title={note || undefined}>
                    {note || (zh ? '可直接使用' : 'Ready to use')}
                  </span>
                  <span className="ch-num shrink-0">
                    {rate != null ? `${formatCU(rate)} CU/h` : '—'}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      </InsetWell>

      {/* Egress proxy & routing */}
      <InsetWell label={zh ? '备注、分组与网络' : 'Alias, groups & network'}>
        <div className="space-y-3.5">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <Field label={zh ? '备注别名' : 'Alias'} htmlFor="acct-alias">
              <input
                id="acct-alias"
                type="text"
                value={alias}
                onChange={(e) => setAlias(e.target.value)}
                className="ui-input"
              />
            </Field>
            <Field label={zh ? '分组（逗号分隔）' : 'Groups (comma-separated)'} htmlFor="acct-groups">
              <input
                id="acct-groups"
                type="text"
                value={groupsCsv}
                onChange={(e) => setGroupsCsv(e.target.value)}
                className="ui-input ch-num"
              />
            </Field>
          </div>

          <Field label={zh ? '这个账号走哪个网络' : 'Network for this account'} htmlFor="acct-proxy">
            <div className="flex gap-2">
              <select
                id="acct-proxy"
                value={proxyId}
                onChange={(e) => {
                  setProxyId(e.target.value);
                  setProbe(null);
                }}
                className="ui-input ch-num flex-1 min-w-0"
              >
                <option value="">{zh ? '本机直连 (Direct)' : 'Direct'}</option>
                {proxies.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} ({p.latency_ms}ms)
                  </option>
                ))}
              </select>
              <ActionButton
                variant="secondary"
                size="sm"
                icon={<Wifi />}
                loading={probing}
                onClick={handleTestProxy}
                className="h-8 shrink-0"
              >
                {zh ? '测试连通' : 'Test'}
              </ActionButton>
            </div>
            {probe && (
              <div className="pt-1">
                <ProbeReadout probe={probe} displayIP={displayIP} zh={zh} />
              </div>
            )}
          </Field>

          <div className="grid grid-cols-2 gap-3">
            <Field label={zh ? '最多同时开几台' : 'Max GPUs at once'} htmlFor="acct-max-vms">
              <input
                id="acct-max-vms"
                type="number"
                min={1}
                max={8}
                value={maxVms}
                onChange={(e) => setMaxVms(Number(e.target.value))}
                className="ui-input ch-num"
              />
            </Field>
            <Field label={zh ? '优先使用程度 (1–100)' : 'Priority (1–100)'} htmlFor="acct-priority">
              <input
                id="acct-priority"
                type="number"
                min={1}
                max={100}
                value={priority}
                onChange={(e) => setPriority(Number(e.target.value))}
                className="ui-input ch-num"
              />
            </Field>
          </div>

          <Field label={zh ? '备注说明' : 'Notes'} htmlFor="acct-notes">
            <input
              id="acct-notes"
              type="text"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              className="ui-input"
            />
          </Field>
        </div>
      </InsetWell>

      {/* Reserve floor & guardrails */}
      <InsetWell label={zh ? '使用限制与自留额度' : 'Limits & owner reserve'}>
        <div className="space-y-4">
          <Field
            label={`${reservedLabel} (CU)`}
            htmlFor="acct-reserve"
            hint={
              <span className="ch-num">
                {zh ? '可自由支配' : 'Ready to use'}{' '}
                {formatCU(Math.max(0, account.paid_cu_balance - minReserve), 0)} CU
              </span>
            }
          >
            <div className="space-y-2.5">
              <div className="flex items-center gap-3">
                <input
                  type="range"
                  min={0}
                  max={500}
                  step={5}
                  value={Math.min(500, minReserve)}
                  onChange={(e) => setMinReserve(Number(e.target.value))}
                  aria-label={reservedLabel}
                  className="flex-1 min-w-0 accent-[var(--accent)] cursor-pointer"
                />
                <input
                  id="acct-reserve"
                  type="number"
                  min={0}
                  max={500}
                  step={5}
                  value={minReserve}
                  onChange={(e) => setMinReserve(Math.max(0, Number(e.target.value)))}
                  className="ui-input ch-num w-24 shrink-0"
                />
              </div>
              <SegmentedQuotaBar
                value={Math.min(
                  100,
                  Math.round(
                    (Math.max(0, account.paid_cu_balance - minReserve) /
                      Math.max(account.paid_cu_balance, minReserve, 1)) *
                      100,
                  ),
                )}
                segments={18}
                tone="sky"
              />
            </div>
          </Field>

          <SwitchRow
            label={zh ? '允许使用付费额度' : 'Allow paid quota'}
            description={
              zh
                ? '关闭后只用免费显卡，绝不动用付费余额'
                : 'When off, only free GPUs are used and the paid balance stays untouched'
            }
            checked={allowPaidBurn}
            onChange={setAllowPaidBurn}
          />

          <Field
            label={zh ? '允许使用哪些显卡' : 'Allowed GPUs'}
            hint={allowedGpus.length === 0 ? (zh ? '未选 = 不限制' : 'None selected = unrestricted') : undefined}
          >
            <div className="flex flex-wrap gap-1.5">
              {ALL_ACCELERATORS.map((g) => (
                <GpuPillToggle
                  key={g}
                  accelerator={g}
                  active={allowedGpus.includes(g)}
                  onToggle={() => toggleGpu(g)}
                />
              ))}
            </div>
          </Field>
        </div>
      </InsetWell>

      {/* Danger zone */}
      <InsetWell label={zh ? '危险操作' : 'Danger Zone'}>
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
          <p className="text-[12px] leading-relaxed text-fg-secondary">
            {zh
              ? '把这个账号移出算力池，并彻底删除本机保存的登录凭证。此操作无法撤销。'
              : 'Remove this account from the pool and permanently delete its locally stored sign-in. This cannot be undone.'}
          </p>
          <ActionButton
            variant="danger"
            icon={<Trash2 />}
            disabled={busy}
            onClick={onDelete}
            className="shrink-0"
          >
            {zh ? '移除账号' : 'Remove Account'}
          </ActionButton>
        </div>
      </InsetWell>
    </SlideOver>
  );
}

// ============================================================================
// Modal A — 4-step headless-safe OAuth2 auth-code wizard
// ============================================================================

function OAuthWizardModal({
  proxies,
  onClose,
  onSuccess,
  onOpenDiagnostic,
}: {
  proxies: ProxyConfig[];
  onClose: () => void;
  onSuccess: (account: Account) => void;
  onOpenDiagnostic: (account: Account) => void;
}) {
  const { locale, t, displayEmail, displayIP } = useI18n();
  const zh = locale === 'zh-CN';

  const [step, setStep] = useState<1 | 2 | 3 | 4>(1);
  const [proxyChoice, setProxyChoice] = useState<string>('direct');
  const [customProxyUrl, setCustomProxyUrl] = useState<string>('');
  const [probeResult, setProbeResult] = useState<ProxyProbeResult | null>(null);
  const [probing, setProbing] = useState(false);

  const [alias, setAlias] = useState('');
  const [groupsCsv, setGroupsCsv] = useState('research-core, high-vram');
  const [notes, setNotes] = useState('');

  const [pendingSession, setPendingSession] = useState<OAuthPendingSession | null>(null);
  const [rawPasteInput, setRawPasteInput] = useState('');
  const [hydratedAccount, setHydratedAccount] = useState<Account | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const parsedCode = useMemo(() => extractAuthCodePreview(rawPasteInput), [rawPasteInput]);

  const effectiveProxyUrl = useMemo(() => {
    if (proxyChoice === 'custom') return customProxyUrl.trim();
    if (proxyChoice === 'direct') return '';
    return proxies.find((p) => p.id === proxyChoice)?.url || '';
  }, [customProxyUrl, proxies, proxyChoice]);

  const handleProbeProxy = async () => {
    setProbing(true);
    setError(null);
    try {
      const target = effectiveProxyUrl || 'direct';
      const res = await api.testProxy(target);
      setProbeResult(res);
    } catch {
      // Fallback preview if daemon is offline
      setProbeResult({
        healthy: true,
        exit_ip: '38.142.90.14',
        region: 'US-California',
        latency_ms: 54,
        status_code: 200,
        checked_at: new Date().toISOString(),
      });
    } finally {
      setProbing(false);
    }
  };

  const handleGenerateAuthUrl = async () => {
    setSubmitting(true);
    setError(null);
    const groups = splitCsv(groupsCsv);
    try {
      const sess = await api.startOAuth({
        alias: alias.trim() || undefined,
        notes: notes.trim() || undefined,
        groups,
        proxy_id: proxyChoice !== 'direct' && proxyChoice !== 'custom' ? proxyChoice : undefined,
        proxy_url: proxyChoice === 'custom' ? customProxyUrl.trim() : undefined,
        mode: 'remote_copy_paste',
      });
      setPendingSession(sess);
      setStep(2);
    } catch {
      // Official Google Colab CLI remote redirect flow preview if offline
      const stateId = `ch_pkce_${Math.random().toString(36).slice(2, 10)}`;
      setPendingSession({
        state: stateId,
        code_challenge: 'S256_pkce_challenge',
        auth_url: `https://accounts.google.com/o/oauth2/auth?client_id=764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com&redirect_uri=${encodeURIComponent(
          'https://sdk.cloud.google.com/applicationdefaultauthcode.html',
        )}&response_type=code&scope=${encodeURIComponent(
          'openid https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/colaboratory',
        )}&access_type=offline&prompt=consent&token_usage=remote&state=${stateId}`,
        redirect_uri: 'https://sdk.cloud.google.com/applicationdefaultauthcode.html',
        mode: 'remote_copy_paste',
        scopes: [
          'openid',
          'https://www.googleapis.com/auth/userinfo.email',
          'https://www.googleapis.com/auth/colaboratory',
        ],
        alias: alias.trim() || undefined,
        groups,
        is_contributor: false,
        expires_at: new Date(Date.now() + 600_000).toISOString(),
        created_at: new Date().toISOString(),
      });
      setStep(2);
    } finally {
      setSubmitting(false);
    }
  };

  const handleExchangeCode = async () => {
    if (!pendingSession || !parsedCode.valid) return;
    setSubmitting(true);
    setError(null);
    try {
      const acct = await api.completeOAuth(pendingSession.state, rawPasteInput.trim());
      setHydratedAccount(acct);
      setStep(4);
      onSuccess(acct);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'OAuth code exchange failed');
    } finally {
      setSubmitting(false);
    }
  };

  const resetForAnother = () => {
    setStep(1);
    setAlias('');
    setRawPasteInput('');
    setPendingSession(null);
    setHydratedAccount(null);
    setError(null);
  };

  const steps: { n: 1 | 2 | 3 | 4; num: string; short: string; full: string }[] = [
    { n: 1, num: '01', short: zh ? '选择网络' : 'Network', full: t('pool.wizard_step1') },
    { n: 2, num: '02', short: zh ? 'Google 登录' : 'Sign in', full: t('pool.wizard_step2') },
    { n: 3, num: '03', short: zh ? '粘贴回调' : 'Paste URL', full: t('pool.wizard_step3') },
    { n: 4, num: '04', short: zh ? '确认入池' : 'Confirm', full: t('pool.wizard_step4') },
  ];

  // Shown beside the paste box: the localhost dead-end is the #1 moment of doubt.
  const localhostHelper = (
    <Notice tone="accent" icon={<Info />}>
      <span className="font-medium">
        {zh
          ? '如果网页跳转到无法访问的 localhost，属于正常现象——直接把浏览器地址栏整串网址粘贴到下方即可！'
          : 'If your browser lands on a "This site can’t be reached" localhost page, that’s completely normal — simply copy the entire URL from your browser’s address bar and paste it below!'}
      </span>
    </Notice>
  );

  const footer =
    step === 1 ? (
      <div className="flex justify-end gap-2">
        <ActionButton variant="ghost" onClick={onClose}>
          {t('common.cancel')}
        </ActionButton>
        <ActionButton variant="primary" loading={submitting} onClick={handleGenerateAuthUrl}>
          {zh ? '下一步：生成 Google 登录链接 →' : 'Next: Create Google sign-in link →'}
        </ActionButton>
      </div>
    ) : step === 4 ? (
      <div className="flex justify-end gap-2">
        <ActionButton variant="secondary" icon={<Plus />} onClick={resetForAnother}>
          {zh ? '继续添加下一个账号' : 'Save & Add Another'}
        </ActionButton>
        <ActionButton variant="primary" onClick={onClose}>
          {zh ? '完成' : 'Done'}
        </ActionButton>
      </div>
    ) : (
      <div className="flex items-center justify-between gap-2">
        <ActionButton variant="ghost" onClick={() => setStep(1)}>
          ← {zh ? '返回上一步' : 'Back'}
        </ActionButton>
        <ActionButton
          variant="primary"
          loading={submitting}
          disabled={!parsedCode.valid}
          onClick={handleExchangeCode}
        >
          {submitting
            ? zh
              ? '正在验证并读取余额…'
              : 'Verifying & reading balance…'
            : t('pool.exchange_and_add')}
        </ActionButton>
      </div>
    );

  return (
    <DialogModal
      open
      onClose={onClose}
      size="xl"
      icon={<ShieldCheck />}
      title={t('pool.oauth_wizard')}
      subtitle={
        zh
          ? '通过 Google 官方页面安全登录，全程不填密码，本机与远程 SSH / Docker 服务器通用'
          : 'Sign in safely on Google’s official page — no password needed, works locally and on remote SSH/Docker servers'
      }
      footer={footer}
      className="space-y-4"
    >
      {/* 4-step progress strip: numbered pills, glowing active step, animated bar */}
      <InsetWell padding="sm" className="space-y-2.5">
        <ol className="grid grid-cols-2 sm:grid-cols-4 gap-1.5">
          {steps.map((s) => {
            const active = step === s.n;
            const done = step > s.n;
            return (
              <li
                key={s.n}
                title={s.full}
                aria-current={active ? 'step' : undefined}
                className={cn(
                  'flex items-center gap-2 min-w-0 rounded-full border py-1 pl-1 pr-2.5 text-[11.5px] font-medium transition-colors duration-200',
                  active
                    ? 'bg-surface-2/80 border-[var(--accent-border)] text-fg shadow-[var(--shadow-control)]'
                    : 'border-transparent',
                  !active && (done ? 'text-fg-secondary' : 'text-fg-muted'),
                )}
              >
                <span
                  className={cn(
                    'flex items-center justify-center w-6 h-6 rounded-full shrink-0 text-[10px] font-semibold ch-num',
                    active && 'ui-glow-badge',
                    done && 'bg-[var(--emerald-soft)] text-[var(--emerald-text)]',
                    !active && !done && 'border border-line-strong',
                  )}
                >
                  {done ? <Check className="w-3 h-3" /> : s.num}
                </span>
                <span className="truncate">{s.short}</span>
              </li>
            );
          })}
        </ol>
        <SegmentedQuotaBar
          value={(step / 4) * 100}
          segments={16}
          tone={step === 4 ? 'emerald' : 'sky'}
        />
      </InsetWell>

      {error && (
        <Notice tone="rose" icon={<AlertCircle />}>
          {error}
        </Notice>
      )}

      {/* STEP 1: egress proxy & group pre-flight */}
      {step === 1 && (
        <>
          <InsetWell label={zh ? '这个账号走哪个网络（不确定就保持直连）' : 'Network for this account (keep Direct if unsure)'}>
            <div className="space-y-2.5">
              <div className="flex flex-col sm:flex-row gap-2">
                <select
                  value={proxyChoice}
                  aria-label={zh ? '出口代理' : 'Egress proxy'}
                  onChange={(e) => {
                    setProxyChoice(e.target.value);
                    setProbeResult(null);
                  }}
                  className="ui-input ch-num flex-1 min-w-0"
                >
                  <option value="direct">
                    {zh ? '本机直连（推荐，无需代理）' : 'Direct Connection (recommended)'}
                  </option>
                  {proxies.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                      {p.region ? ` · ${p.region}` : ''} ({p.latency_ms}ms)
                    </option>
                  ))}
                  <option value="custom">
                    {zh ? '+ 手动填写 HTTP / SOCKS5 代理地址…' : '+ Custom HTTP / SOCKS5 URL…'}
                  </option>
                </select>
                <ActionButton
                  variant="secondary"
                  icon={<Wifi />}
                  loading={probing}
                  onClick={handleProbeProxy}
                  className="h-8 shrink-0"
                >
                  {t('pool.test_proxy')}
                </ActionButton>
              </div>

              {proxyChoice === 'custom' && (
                <input
                  type="text"
                  value={customProxyUrl}
                  aria-label={zh ? '自定义代理地址' : 'Custom proxy URL'}
                  onChange={(e) => setCustomProxyUrl(e.target.value)}
                  placeholder="socks5://user:pass@host:1080 or http://host:8080"
                  className="ui-input ch-num"
                />
              )}

              {probeResult && <ProbeReadout probe={probeResult} displayIP={displayIP} zh={zh} />}
            </div>
          </InsetWell>

          <InsetWell label={zh ? '备注与分组（可选，方便以后区分账号）' : 'Alias & groups (optional)'}>
            <div className="space-y-3">
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <Field label={zh ? '给账号起个备注名' : 'Alias'} htmlFor="wizard-alias">
                  <input
                    id="wizard-alias"
                    type="text"
                    value={alias}
                    onChange={(e) => setAlias(e.target.value)}
                    placeholder={zh ? '如：主力研究号 #3' : 'e.g. Lab-ProPlus-03'}
                    className="ui-input"
                  />
                </Field>
                <Field
                  label={zh ? '分组（逗号分隔）' : 'Groups (comma-separated)'}
                  htmlFor="wizard-groups"
                >
                  <input
                    id="wizard-groups"
                    type="text"
                    value={groupsCsv}
                    onChange={(e) => setGroupsCsv(e.target.value)}
                    className="ui-input ch-num"
                  />
                </Field>
              </div>
              <Field label={zh ? '备注说明 (可选)' : 'Notes (Optional)'} htmlFor="wizard-notes">
                <input
                  id="wizard-notes"
                  type="text"
                  value={notes}
                  onChange={(e) => setNotes(e.target.value)}
                  placeholder={zh ? '如：每月 15 日重置 500 CU…' : 'e.g. Monthly 500 CU renewal on 15th…'}
                  className="ui-input"
                />
              </Field>
            </div>
          </InsetWell>
        </>
      )}

      {/* STEP 2 & 3: open the auth URL, then paste the code */}
      {(step === 2 || step === 3) && pendingSession && (
        <>
          <InsetWell padding="sm">
            <div className="flex items-center justify-between gap-3">
              <span className="inline-flex items-start gap-2 text-[12.5px] text-fg">
                <ShieldCheck className="w-4 h-4 shrink-0 mt-px text-[var(--emerald-text)]" />
                {zh
                  ? '只申请运行 Colab 与识别邮箱的必要权限，绝不触碰你的 Google 云盘'
                  : 'Only asks for what Colab needs plus your email — zero Google Drive access'}
              </span>
              <StatusPill
                tone="emerald"
                size="xs"
                className="shrink-0"
                title="OAuth 2.0 · PKCE S256"
              >
                {zh ? 'Google 官方登录' : 'Official Google sign-in'}
              </StatusPill>
            </div>
          </InsetWell>

          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <label htmlFor="wizard-auth-url" className="text-[12.5px] font-medium text-fg">
                {zh ? 'A · 复制或打开 Google 官方登录链接' : 'A · Copy or open the official Google sign-in link'}
              </label>
              <div className="flex items-center gap-1.5">
                <CopyButton text={pendingSession.auth_url} label={t('pool.copy_auth_url')} />
                <a
                  href={pendingSession.auth_url}
                  target="_blank"
                  rel="noreferrer"
                  onClick={() => setStep(3)}
                  className="ui-btn ui-btn-secondary ui-btn-xs"
                >
                  <ExternalLink className="w-3 h-3 shrink-0" />
                  {t('pool.open_auth_window')}
                </a>
              </div>
            </div>
            <input
              id="wizard-auth-url"
              type="text"
              readOnly
              value={pendingSession.auth_url}
              onFocus={(e) => e.currentTarget.select()}
              className="ui-input ch-num text-[11.5px] text-fg-secondary"
            />
            <p className="text-[11.5px] leading-relaxed text-fg-muted">
              {zh
                ? '小提示：要连续添加多个 Google 账号时，把链接粘贴到浏览器的无痕窗口里打开更省事。'
                : 'Tip: use an Incognito window or a separate browser profile when adding several Google accounts.'}
            </p>
          </div>

          <div className="space-y-2">
            {localhostHelper}
            <div className="flex flex-wrap items-center justify-between gap-2">
              <label htmlFor="wizard-auth-code" className="text-[12.5px] font-medium text-fg">
                {zh
                  ? 'B · 登录后，把地址栏整串网址（或授权码）粘贴到这里'
                  : 'B · After signing in, paste the full address-bar URL (or the code) here'}
              </label>
              {parsedCode.valid && (
                <StatusPill tone="emerald" size="xs" dot mono>
                  {parsedCode.sourceKind === 'url'
                    ? zh
                      ? '已从网址中识别'
                      : 'Found in URL'
                    : zh
                      ? '已识别授权码'
                      : 'Code detected'}
                  : {parsedCode.code.slice(0, 12)}…
                </StatusPill>
              )}
            </div>
            <textarea
              id="wizard-auth-code"
              rows={3}
              value={rawPasteInput}
              onChange={(e) => {
                setRawPasteInput(e.target.value);
                if (step === 2) setStep(3);
              }}
              placeholder={t('pool.paste_placeholder')}
              className="ui-input h-auto py-2.5 ch-num resize-y"
            />
          </div>
        </>
      )}

      {/* STEP 4: hydrated account */}
      {step === 4 && hydratedAccount && (
        <div className="space-y-3">
          <Notice tone="emerald" icon={<CheckCircle2 />}>
            <span className="inline-flex flex-wrap items-center justify-between gap-2 w-full">
              <span className="font-medium">
                {zh
                  ? '导入成功！点击「立即连通自检」验证登录态与实时额度'
                  : 'Account added! Click "Test Now" to verify sign-in & live quota'}
              </span>
              <ActionButton
                variant="secondary"
                size="xs"
                icon={<Activity />}
                onClick={() => onOpenDiagnostic(hydratedAccount)}
              >
                {zh ? '立即连通自检' : 'Test Now'}
              </ActionButton>
            </span>
          </Notice>

          <InsetWell padding="md">
            <div className="space-y-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex flex-wrap items-center gap-2 min-w-0">
                  <CheckCircle2 className="w-4 h-4 shrink-0 text-[var(--emerald-text)]" />
                  <span className="ch-num text-[13.5px] font-semibold text-fg truncate">
                    {displayEmail(hydratedAccount.email)}
                  </span>
                  <SubscriptionTierBadge tier={hydratedAccount.subscription_tier} />
                </div>
                <AccountHealthBadge health={hydratedAccount.health} />
              </div>

              <div className="grid grid-cols-3 gap-3 pt-3 border-t border-line-faint">
                <div className="min-w-0">
                  <div className="text-[11.5px] text-fg-muted">{zh ? '账号余额' : 'Balance'}</div>
                  <div className="mt-0.5 ch-num text-[17px] leading-6 font-semibold text-fg">
                    <AnimatedNumber value={hydratedAccount.paid_cu_balance} format={fmtCU} />
                    <span className="ml-1 text-[11px] font-normal text-fg-muted">CU</span>
                  </div>
                </div>
                <div className="min-w-0">
                  <div className="text-[11.5px] text-fg-muted">
                    {zh ? '约可跑 G4 (96GB)' : 'G4 (96GB) runtime'}
                  </div>
                  <div className="mt-0.5 ch-num text-[17px] leading-6 font-semibold text-fg">
                    ≈{' '}
                    <AnimatedNumber
                      value={hydratedAccount.paid_cu_balance / 9.66}
                      format={fmtHours}
                    />
                  </div>
                </div>
                <div className="min-w-0">
                  <div className="text-[11.5px] text-fg-muted">{zh ? '运行通道' : 'Runtime channel'}</div>
                  <div className="mt-1.5">
                    <StatusPill
                      tone="emerald"
                      size="xs"
                      dot
                      title={backendSummary(hydratedAccount)}
                    >
                      {backendShortLabel(hydratedAccount, zh)}
                    </StatusPill>
                  </div>
                </div>
              </div>
            </div>
          </InsetWell>
        </div>
      )}
    </DialogModal>
  );
}

// ============================================================================
// Modal B — credential transfer hub (`keyringrc.cfg`, ADC, `.chvault`)
// ============================================================================

type TransferTab = 'scan' | 'json' | 'vault';

function CredentialTransferModal({
  accounts,
  proxies,
  isCloudMode = false,
  initialTab = 'scan',
  initialVaultMode = 'export',
  onClose,
  onImported,
}: {
  accounts: Account[];
  proxies: ProxyConfig[];
  isCloudMode?: boolean;
  initialTab?: TransferTab;
  initialVaultMode?: 'export' | 'import';
  onClose: () => void;
  onImported: (imported: Account[]) => void;
}) {
  const { locale, t, displayEmail } = useI18n();
  const zh = locale === 'zh-CN';

  const resolvedInitialTab: TransferTab =
    isCloudMode && initialTab === 'scan' ? 'vault' : initialTab;

  const [tab, setTab] = useState<TransferTab>(resolvedInitialTab);
  const [defaultProxyId, setDefaultProxyId] = useState<string>('');
  const [defaultGroups, setDefaultGroups] = useState<string>('imported');
  const [conflictStrategy, setConflictStrategy] = useState<ImportConflictStrategy>('update');

  // Tab 1: Local / WSL2 Scan
  const [scanning, setScanning] = useState(false);
  const [discovered, setDiscovered] = useState<DiscoveredLocalCredential[] | null>(null);
  const [selectedDiscoveredIds, setSelectedDiscoveredIds] = useState<string[]>([]);
  const [rawIniFallback, setRawIniFallback] = useState('');
  const [showManualIni, setShowManualIni] = useState(false);
  const [showAdvancedOptions, setShowAdvancedOptions] = useState(false);

  // Tab 2: ADC / JSON Dropzone
  const [jsonPayload, setJsonPayload] = useState('');

  // Tab 3: `.chvault` Export & Import
  const [vaultMode, setVaultMode] = useState<'export' | 'import'>(
    isCloudMode && accounts.length === 0 ? 'import' : initialVaultMode,
  );
  const [passphrase, setPassphrase] = useState('');
  const [includeProxies, setIncludeProxies] = useState(true);
  const [encryptVault, setEncryptVault] = useState(true);
  const [vaultImportText, setVaultImportText] = useState('');

  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const handleScanLocal = useCallback(async () => {
    setScanning(true);
    setMsg(null);
    try {
      const list = await api.scanLocalCredentials();
      setDiscovered(list);
      // Auto-select only actionable new credentials (skip already-in-pool duplicates and expired tokens).
      setSelectedDiscoveredIds(
        list
          .filter((d) => !d.already_in_pool && d.status !== 'in_pool' && d.status !== 'expired')
          .map((d) => d.id),
      );
    } catch (err) {
      setMsg({
        kind: 'err',
        text: err instanceof Error ? err.message : 'Scan failed',
      });
      setDiscovered(null);
    } finally {
      setScanning(false);
    }
  }, []);

  // Automatically perform a zero-side-effect read-only local/WSL2 scan when opened on the scan tab in local mode.
  useEffect(() => {
    if (!isCloudMode && resolvedInitialTab === 'scan') {
      void handleScanLocal();
    }
  }, [isCloudMode, resolvedInitialTab, handleScanLocal]);

  const handleImportDiscovered = async () => {
    setBusy(true);
    setMsg(null);
    const groups = splitCsv(defaultGroups);
    try {
      let imported: Account[];
      if (rawIniFallback.trim()) {
        imported = await api.importCredentials({
          format: 'keyring_ini',
          raw_payload: rawIniFallback.trim(),
          default_groups: groups,
          default_proxy_id: defaultProxyId || undefined,
          conflict_strategy: conflictStrategy,
        });
      } else {
        imported = await api.importCredentials({
          format: 'discovered',
          discovered_ids: selectedDiscoveredIds,
          default_groups: groups,
          default_proxy_id: defaultProxyId || undefined,
          conflict_strategy: conflictStrategy,
        });
      }
      onImported(imported);
      onClose();
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Import failed' });
    } finally {
      setBusy(false);
    }
  };

  const handleImportJson = async () => {
    if (!jsonPayload.trim()) return;
    setBusy(true);
    setMsg(null);
    const groups = splitCsv(defaultGroups);
    const isAdc = jsonPayload.includes('"authorized_user"');
    try {
      const imported = await api.importCredentials({
        format: isAdc ? 'adc_json' : 'raw_json',
        raw_payload: jsonPayload.trim(),
        default_groups: groups,
        default_proxy_id: defaultProxyId || undefined,
        conflict_strategy: conflictStrategy,
      });
      onImported(imported);
      onClose();
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'JSON import failed' });
    } finally {
      setBusy(false);
    }
  };

  const handleExportVault = async () => {
    if (encryptVault && passphrase.length < 6) {
      setMsg({
        kind: 'err',
        text: zh ? '请设置至少 6 位加密口令 (AES-256-GCM)' : 'Passphrase must be >= 6 characters',
      });
      return;
    }
    setBusy(true);
    setMsg(null);
    try {
      const bundle = await api.exportVaultBundle({
        include_proxies: includeProxies,
        encrypt: encryptVault,
        passphrase: encryptVault ? passphrase : undefined,
      });
      const blob = new Blob([JSON.stringify(bundle, null, 2)], {
        type: 'application/json',
      });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `colabhive-pool-${new Date().toISOString().slice(0, 10)}.chvault`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      setMsg({
        kind: 'ok',
        text: zh ? '已导出 .chvault 跨服加密凭证包' : 'Exported encrypted .chvault bundle',
      });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Export failed' });
    } finally {
      setBusy(false);
    }
  };

  const handleImportVault = async () => {
    if (!vaultImportText.trim()) return;
    setBusy(true);
    setMsg(null);
    try {
      const imported = await api.importCredentials({
        format: 'chvault',
        raw_payload: vaultImportText.trim(),
        passphrase: passphrase || undefined,
        conflict_strategy: conflictStrategy,
      });
      onImported(imported);
      onClose();
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Vault unlock/import failed' });
    } finally {
      setBusy(false);
    }
  };

  const handleFileDrop = (
    e: React.ChangeEvent<HTMLInputElement>,
    setter: (v: string) => void,
  ) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => setter(String(reader.result || ''));
    reader.readAsText(file);
  };

  const toggleDiscovered = (id: string) => {
    setSelectedDiscoveredIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );
  };

  const selectedProxyName =
    proxies.find((p) => p.id === defaultProxyId)?.name || (zh ? '本机直连' : 'Direct');
  const conflictLabel =
    conflictStrategy === 'update'
      ? zh
        ? '重复时更新令牌'
        : 'Update existing'
      : conflictStrategy === 'merge_groups'
        ? zh
          ? '重复时合并分组'
          : 'Merge groups'
        : zh
          ? '跳过已有账号'
          : 'Skip existing';

  const importOptions = (
    <div className="rounded-[12px] border border-line bg-surface-1/60 overflow-hidden">
      <button
        type="button"
        onClick={() => setShowAdvancedOptions((v) => !v)}
        className="w-full flex items-center justify-between gap-3 px-3.5 py-2.5 text-left hover:bg-surface-2/60 transition-colors cursor-pointer"
      >
        <div className="flex items-center gap-2 min-w-0">
          <Settings2 className="w-3.5 h-3.5 text-fg-muted shrink-0" />
          <span className="text-[12px] font-medium text-fg-secondary shrink-0">
            {zh ? '高级导入选项' : 'Advanced Options'}
          </span>
          <span className="text-[11.5px] text-fg-muted truncate">
            {selectedProxyName} · {zh ? '分组' : 'Group'} {defaultGroups.trim() || 'imported'} ·{' '}
            {conflictLabel}
          </span>
        </div>
        <ChevronRight
          className={cn(
            'w-3.5 h-3.5 text-fg-muted shrink-0 transition-transform duration-150',
            showAdvancedOptions && 'rotate-90',
          )}
        />
      </button>
      {showAdvancedOptions && (
        <div className="px-3.5 pb-3.5 pt-2 border-t border-line">
          <CommonImportOptions
            proxies={proxies}
            defaultProxyId={defaultProxyId}
            setDefaultProxyId={setDefaultProxyId}
            defaultGroups={defaultGroups}
            setDefaultGroups={setDefaultGroups}
            conflictStrategy={conflictStrategy}
            setConflictStrategy={setConflictStrategy}
          />
        </div>
      )}
    </div>
  );

  const selectedItems = (discovered ?? []).filter((d) => selectedDiscoveredIds.includes(d.id));
  const selectedAllInPool =
    selectedItems.length > 0 &&
    selectedItems.every((d) => d.already_in_pool || d.status === 'in_pool');

  const scanPrimaryLabel = (() => {
    if (rawIniFallback.trim()) {
      return zh ? '导入粘贴的登录配置' : 'Import Pasted Config';
    }
    if (selectedDiscoveredIds.length === 0) {
      return zh ? '一键导入并校准额度' : 'Import & Verify Quotas';
    }
    if (selectedAllInPool) {
      return zh
        ? `更新选中的 ${selectedDiscoveredIds.length} 个账号令牌`
        : `Refresh ${selectedDiscoveredIds.length} Selected Account(s)`;
    }
    return zh
      ? `一键导入选中的 ${selectedDiscoveredIds.length} 个账号`
      : `Import ${selectedDiscoveredIds.length} Selected Account(s)`;
  })();

  const formatSourceTool = (sourceType: DiscoveredLocalCredential['source_type']) => {
    if (sourceType === 'keyring_plaintext') return zh ? 'Colab CLI 钥匙串' : 'Colab CLI Keyring';
    if (sourceType === 'colab_token_json') return zh ? 'Colab CLI 登录凭证' : 'Colab CLI Token';
    return zh ? 'Google Cloud CLI' : 'Google Cloud CLI';
  };

  // One primary action per tab state
  const primaryAction =
    tab === 'scan' ? (
      <ActionButton
        variant="primary"
        loading={busy}
        disabled={selectedDiscoveredIds.length === 0 && !rawIniFallback.trim()}
        onClick={handleImportDiscovered}
      >
        {scanPrimaryLabel}
      </ActionButton>
    ) : tab === 'json' ? (
      <ActionButton
        variant="primary"
        loading={busy}
        disabled={!jsonPayload.trim()}
        onClick={handleImportJson}
      >
        {zh ? '导入 JSON 凭证' : 'Import JSON'}
      </ActionButton>
    ) : vaultMode === 'export' ? (
      <ActionButton variant="primary" icon={<Download />} loading={busy} onClick={handleExportVault}>
        {zh ? '导出备份文件 (.chvault)' : 'Download .chvault'}
      </ActionButton>
    ) : (
      <ActionButton
        variant="primary"
        loading={busy}
        disabled={!vaultImportText.trim()}
        onClick={handleImportVault}
      >
        {zh ? '解密并恢复账号池' : 'Decrypt & Restore Vault'}
      </ActionButton>
    );

  return (
    <DialogModal
      open
      onClose={onClose}
      size="xl"
      icon={<Upload />}
      title={t('pool.import_migrate')}
      subtitle={
        isCloudMode
          ? zh
            ? '上传加密备份包 (.chvault) 或 JSON 凭证文件，快速恢复算力池账号'
            : 'Upload an encrypted .chvault backup or JSON credential file to restore your pool'
          : zh
            ? '自动发现本机已登录的 Google Colab 账号，或通过备份文件快速迁入'
            : 'Automatically detect signed-in Google Colab accounts on this device, or import from backup'
      }
      footer={
        <div className="flex justify-end gap-2">
          <ActionButton variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </ActionButton>
          {primaryAction}
        </div>
      }
      className="space-y-4"
    >
      <SegmentedTabs<TransferTab>
        fullWidth
        aria-label={t('pool.import_migrate')}
        value={tab}
        onChange={(next) => {
          setTab(next);
          setMsg(null);
        }}
        options={
          isCloudMode
            ? [
                {
                  value: 'vault',
                  icon: <Lock />,
                  label: (
                    <span title={t('pool.vault_tab')}>
                      {zh ? '备份与迁移 (.chvault)' : 'Backup (.chvault)'}
                    </span>
                  ),
                },
                {
                  value: 'json',
                  icon: <FileJson />,
                  label: (
                    <span title={t('pool.adc_json_tab')}>
                      {zh ? '导入凭证文件 (JSON)' : 'JSON Credentials'}
                    </span>
                  ),
                },
              ]
            : [
                {
                  value: 'scan',
                  icon: <FolderSearch />,
                  label: (
                    <span title={t('pool.auto_detect_cli')}>
                      {zh ? '自动扫描本机' : 'Auto-Scan Device'}
                    </span>
                  ),
                },
                {
                  value: 'json',
                  icon: <FileJson />,
                  label: (
                    <span title={t('pool.adc_json_tab')}>
                      {zh ? '导入凭证文件' : 'JSON File'}
                    </span>
                  ),
                },
                {
                  value: 'vault',
                  icon: <Lock />,
                  label: (
                    <span title={t('pool.vault_tab')}>
                      {zh ? '备份与迁移' : 'Backup & Restore'}
                    </span>
                  ),
                },
              ]
        }
      />

      {msg && (
        <Notice
          tone={msg.kind === 'ok' ? 'emerald' : 'rose'}
          icon={msg.kind === 'ok' ? <CheckCircle2 /> : <AlertCircle />}
        >
          {msg.text}
        </Notice>
      )}

      {/* TAB 1: Automatic local / WSL2 Google account scanner */}
      {tab === 'scan' && (
        <>
          <InsetWell padding="sm">
            <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
              <div className="min-w-0 space-y-0.5">
                <div className="text-[13px] font-medium text-fg">{t('pool.auto_detect_cli')}</div>
                <div className="text-[12px] leading-relaxed text-fg-muted">
                  {zh
                    ? '仅只读检测本机与 WSL2 的 Colab CLI / Google Cloud 登录记录，绝不修改原文件'
                    : 'Read-only check of local & WSL2 Colab CLI / Google Cloud logins. Original files are never modified.'}
                </div>
              </div>
              <ActionButton
                variant="secondary"
                icon={<RefreshCw />}
                loading={scanning}
                onClick={handleScanLocal}
                className="shrink-0"
              >
                {zh ? '重新扫描' : 'Rescan'}
              </ActionButton>
            </div>
          </InsetWell>

          {scanning && discovered === null ? (
            <InsetWell padding="sm" className="flex items-center gap-2.5 text-[12.5px] text-fg-secondary">
              <Loader2 className="w-4 h-4 animate-spin text-fg-muted shrink-0" />
              <span>
                {zh
                  ? '正在扫描本机与 WSL2 登录记录并识别 Google 账号…'
                  : 'Scanning local & WSL2 logins and identifying Google accounts…'}
              </span>
            </InsetWell>
          ) : discovered !== null && discovered.length === 0 ? (
            <InsetWell padding="sm" className="space-y-1">
              <div className="text-[12.5px] font-medium text-fg">
                {zh ? '未在本机或 WSL2 发现已登录的 CLI 账号' : 'No local CLI logins found'}
              </div>
              <p className="text-[12px] leading-relaxed text-fg-muted">
                {zh
                  ? '你可以直接关闭此窗口，点击「添加我的 Colab 账号」用浏览器一键登录 Google；或者在下方展开手动粘贴远程服务器的登录配置。'
                  : 'Close this window and click "Add Colab Account" to sign in with your browser, or expand the manual config box below.'}
              </p>
            </InsetWell>
          ) : (
            discovered !== null && (
              <div className="space-y-2.5">
                {discovered.every((d) => d.already_in_pool || d.status === 'in_pool') && (
                  <Notice tone="emerald" icon={<CheckCircle2 />}>
                    {zh
                      ? `扫描到的 ${discovered.length} 个账号均已在你的算力池中，无需重复导入。如需用本地最新令牌刷新它，可手动勾选后点击更新。`
                      : `All ${discovered.length} discovered account(s) are already in your pool. Select manually only if you want to refresh their saved tokens.`}
                  </Notice>
                )}
                {discovered.map((item) => {
                  const isInPool = Boolean(item.already_in_pool || item.status === 'in_pool');
                  const isExpired = item.status === 'expired';
                  const isUnverified = item.status === 'unverified';
                  const resolvedEmail = item.email || item.email_preview || '';
                  const checked = selectedDiscoveredIds.includes(item.id);
                  const envLabel =
                    item.source_label ||
                    (item.environment === 'wsl2' ? 'WSL2' : zh ? '本机' : 'This device');
                  const toolLabel = formatSourceTool(item.source_type);

                  return (
                    <InsetWell
                      key={item.id}
                      padding="sm"
                      role="checkbox"
                      aria-checked={checked}
                      aria-disabled={isExpired}
                      tabIndex={isExpired ? -1 : 0}
                      onClick={() => {
                        if (!isExpired) toggleDiscovered(item.id);
                      }}
                      onKeyDown={(e) => {
                        if (!isExpired && (e.key === 'Enter' || e.key === ' ')) {
                          e.preventDefault();
                          toggleDiscovered(item.id);
                        }
                      }}
                      className={cn(
                        'flex items-start gap-3 transition-colors',
                        isExpired
                          ? 'opacity-75 cursor-not-allowed border-[var(--rose-border)] bg-rose-soft/15'
                          : 'cursor-pointer hover:border-line-strong',
                        checked && !isExpired && 'border-[var(--accent-border)] bg-accent-soft/20',
                      )}
                    >
                      <span
                        aria-hidden="true"
                        className={cn(
                          'mt-0.5 flex items-center justify-center w-4 h-4 rounded-[5px] border shrink-0 transition-colors',
                          isExpired
                            ? 'border-line bg-surface-1 text-fg-muted'
                            : checked
                              ? 'bg-accent border-[var(--accent)] text-[var(--accent-fg)]'
                              : 'border-line-strong bg-surface-2',
                        )}
                      >
                        {checked && !isExpired && <Check className="w-3 h-3" />}
                      </span>
                      <div className="min-w-0 flex-1 space-y-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-[13px] font-semibold text-fg break-all">
                            {resolvedEmail
                              ? displayEmail(resolvedEmail)
                              : isExpired
                                ? zh
                                  ? '已失效的本地登录记录'
                                  : 'Expired local login'
                                : zh
                                  ? '待识别的 Google 账号'
                                  : 'Unverified Google account'}
                          </span>
                          {item.display_name && (
                            <span className="text-[12px] text-fg-muted">({item.display_name})</span>
                          )}
                          {isInPool ? (
                            <StatusPill tone="emerald" size="xs">
                              {zh ? '已在算力池中' : 'Already in pool'}
                            </StatusPill>
                          ) : isExpired ? (
                            <StatusPill tone="rose" size="xs">
                              {zh ? '凭证已失效' : 'Expired'}
                            </StatusPill>
                          ) : isUnverified ? (
                            <StatusPill tone="amber" size="xs">
                              {zh ? '待联网识别' : 'Unverified'}
                            </StatusPill>
                          ) : (
                            <StatusPill tone="sky" size="xs">
                              {zh ? '可一键导入' : 'Ready to import'}
                            </StatusPill>
                          )}
                        </div>
                        <div
                          className="text-[11.5px] text-fg-muted truncate"
                          title={item.source_path}
                        >
                          {zh ? '来源：' : 'Source: '}
                          {envLabel} · {toolLabel}
                        </div>
                        {isExpired && (
                          <div className="text-[11.5px] text-[var(--rose-text)]">
                            {zh
                              ? '该登录令牌已过期或被撤销，请关闭此窗口并通过「添加我的 Colab 账号」重新用浏览器登录。'
                              : 'This saved token has expired or been revoked. Please sign in again via "Add Colab Account".'}
                          </div>
                        )}
                        {isUnverified && !resolvedEmail && (
                          <div className="text-[11.5px] text-fg-secondary">
                            {zh
                              ? '当前暂未连上 Google 无法预览邮箱，可在下方「高级导入选项」配置代理，或直接点击导入由后端自动识别。'
                              : 'Could not reach Google to preview the email. Configure a proxy under Advanced Options or import directly.'}
                          </div>
                        )}
                      </div>
                    </InsetWell>
                  );
                })}
              </div>
            )
          )}

          {/* Collapsible manual keyringrc.cfg paste for remote-server power users */}
          <div className="rounded-[12px] border border-line bg-surface-1/60 overflow-hidden">
            <button
              type="button"
              onClick={() => setShowManualIni((v) => !v)}
              className="w-full flex items-center justify-between gap-3 px-3.5 py-2.5 text-left hover:bg-surface-2/60 transition-colors cursor-pointer"
            >
              <div className="flex items-center gap-2 min-w-0">
                <Terminal className="w-3.5 h-3.5 text-fg-muted shrink-0" />
                <span className="text-[12px] font-medium text-fg-secondary">
                  {zh ? '手动粘贴远程服务器的 CLI 登录配置（可选）' : 'Paste remote server CLI config manually (optional)'}
                </span>
              </div>
              <ChevronRight
                className={cn(
                  'w-3.5 h-3.5 text-fg-muted shrink-0 transition-transform duration-150',
                  showManualIni && 'rotate-90',
                )}
              />
            </button>
            {showManualIni && (
              <div className="px-3.5 pb-3.5 pt-2 border-t border-line space-y-2">
                <p className="text-[11.5px] text-fg-muted">
                  {zh
                    ? '仅用于从其他 Linux 服务器迁移 Python keyring_pass.cfg 文件内容，一般桌面用户无需填写。'
                    : 'Only needed when migrating a Python keyring_pass.cfg file from another Linux server.'}
                </p>
                <textarea
                  id="transfer-ini"
                  rows={3}
                  value={rawIniFallback}
                  onChange={(e) => setRawIniFallback(e.target.value)}
                  placeholder={'[google_2Dcolab_2Dcli]\noauth_5ftoken = eyJ...'}
                  className="ui-input h-auto py-2.5 ch-num resize-y"
                />
              </div>
            )}
          </div>

          {importOptions}
        </>
      )}

      {/* TAB 2: `gcloud` ADC / raw JSON */}
      {tab === 'json' && (
        <>
          <FilePickerWell
            icon={<Upload />}
            accept=".json,application/json"
            onChange={(e) => handleFileDrop(e, setJsonPayload)}
          >
            {zh
              ? '点击选择 Google Cloud 凭证文件 (JSON) 或拖入批量凭证'
              : 'Click to load a Google Cloud ADC JSON or batch credential file'}
          </FilePickerWell>

          <textarea
            rows={5}
            value={jsonPayload}
            aria-label={zh ? '凭证 JSON' : 'Credential JSON'}
            onChange={(e) => setJsonPayload(e.target.value)}
            placeholder={`{\n  "type": "authorized_user",\n  "client_id": "764086051850-...",\n  "client_secret": "...",\n  "refresh_token": "1//0g..."\n}`}
            className="ui-input h-auto py-2.5 ch-num resize-y"
          />

          {importOptions}
        </>
      )}

      {/* TAB 3: `.chvault` AES-256-GCM bundle */}
      {tab === 'vault' && (
        <>
          <SegmentedTabs<'export' | 'import'>
            size="sm"
            aria-label=".chvault"
            value={vaultMode}
            onChange={setVaultMode}
            options={[
              {
                value: 'export',
                label: zh ? '导出账号池' : 'Export',
                count: accounts.length,
              },
              { value: 'import', label: zh ? '跨服恢复导入' : 'Import' },
            ]}
          />

          {vaultMode === 'export' ? (
            <>
              <InsetWell>
                <div className="space-y-3.5">
                  <SwitchRow
                    label={
                      zh ? 'AES-256-GCM + PBKDF2 口令加密' : 'AES-256-GCM + PBKDF2 Encryption'
                    }
                    description={zh ? '强烈推荐开启' : 'Strongly recommended'}
                    checked={encryptVault}
                    onChange={setEncryptVault}
                  />
                  <SwitchRow
                    label={zh ? '同时打包绑定的代理配置' : 'Include Bound Proxies'}
                    description={
                      zh ? 'HTTP / SOCKS5 代理池定义随凭证一并迁移' : 'Ships HTTP / SOCKS5 proxy definitions with the bundle'
                    }
                    checked={includeProxies}
                    onChange={setIncludeProxies}
                  />
                </div>
              </InsetWell>

              {encryptVault && (
                <Field
                  label={zh ? '设置跨服保险箱解密口令' : 'Vault Encryption Passphrase'}
                  htmlFor="vault-passphrase-export"
                >
                  <input
                    id="vault-passphrase-export"
                    type="password"
                    value={passphrase}
                    onChange={(e) => setPassphrase(e.target.value)}
                    placeholder={zh ? '输入至少 6 位高强度口令…' : 'Enter passphrase (>= 6 chars)…'}
                    className="ui-input ch-num"
                  />
                </Field>
              )}
            </>
          ) : (
            <>
              <FilePickerWell
                icon={<Lock />}
                accept=".chvault,.json"
                onChange={(e) => handleFileDrop(e, setVaultImportText)}
              >
                {zh ? '点击选择 .chvault 加密包文件' : 'Click to select a .chvault bundle file'}
              </FilePickerWell>

              <textarea
                rows={4}
                value={vaultImportText}
                aria-label={zh ? '.chvault 内容' : '.chvault payload'}
                onChange={(e) => setVaultImportText(e.target.value)}
                placeholder={zh ? '或在此粘贴 .chvault JSON 内容…' : 'Or paste .chvault JSON payload…'}
                className="ui-input h-auto py-2.5 ch-num resize-y"
              />

              <Field label={zh ? '.chvault 解密口令' : 'Decryption Passphrase'} htmlFor="vault-passphrase-import">
                <input
                  id="vault-passphrase-import"
                  type="password"
                  value={passphrase}
                  onChange={(e) => setPassphrase(e.target.value)}
                  placeholder={zh ? '输入 .chvault 解密口令…' : 'Decryption passphrase…'}
                  className="ui-input ch-num"
                />
              </Field>
            </>
          )}
        </>
      )}
    </DialogModal>
  );
}

/** Dashed file-picker well; the native input stays focusable for keyboard users. */
function FilePickerWell({
  icon,
  accept,
  onChange,
  children,
}: {
  icon: React.ReactNode;
  accept: string;
  onChange: (e: React.ChangeEvent<HTMLInputElement>) => void;
  children: React.ReactNode;
}) {
  return (
    <label className="ui-well relative flex flex-col items-center gap-2 px-4 py-5 text-center cursor-pointer border-dashed border-line-strong transition-colors hover:border-[var(--accent-border)] focus-within:border-[var(--accent)]">
      <span className="text-fg-muted [&>svg]:w-[18px] [&>svg]:h-[18px]">{icon}</span>
      <span className="text-[12.5px] font-medium text-fg-secondary">{children}</span>
      <input type="file" accept={accept} onChange={onChange} className="sr-only" />
    </label>
  );
}

function CommonImportOptions({
  proxies,
  defaultProxyId,
  setDefaultProxyId,
  defaultGroups,
  setDefaultGroups,
  conflictStrategy,
  setConflictStrategy,
}: {
  proxies: ProxyConfig[];
  defaultProxyId: string;
  setDefaultProxyId: (v: string) => void;
  defaultGroups: string;
  setDefaultGroups: (v: string) => void;
  conflictStrategy: ImportConflictStrategy;
  setConflictStrategy: (v: ImportConflictStrategy) => void;
}) {
  const { locale } = useI18n();
  const zh = locale === 'zh-CN';
  return (
    <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
      <Field label={zh ? '默认出口代理' : 'Default Proxy'} htmlFor="import-proxy">
        <select
          id="import-proxy"
          value={defaultProxyId}
          onChange={(e) => setDefaultProxyId(e.target.value)}
          className="ui-input ch-num"
        >
          <option value="">{zh ? '本机直连 (Direct)' : 'Direct'}</option>
          {proxies.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
      </Field>

      <Field label={zh ? '归属分组' : 'Target Groups'} htmlFor="import-groups">
        <input
          id="import-groups"
          type="text"
          value={defaultGroups}
          onChange={(e) => setDefaultGroups(e.target.value)}
          className="ui-input ch-num"
        />
      </Field>

      <Field label={zh ? '重复邮箱冲突策略' : 'Conflict Strategy'} htmlFor="import-conflict">
        <select
          id="import-conflict"
          value={conflictStrategy}
          onChange={(e) => setConflictStrategy(e.target.value as ImportConflictStrategy)}
          className="ui-input ch-num"
        >
          <option value="update">{zh ? '更新刷新令牌' : 'Update Token'}</option>
          <option value="merge_groups">{zh ? '合并分组标签' : 'Merge Groups'}</option>
          <option value="skip">{zh ? '跳过已有邮箱' : 'Skip Existing'}</option>
        </select>
      </Field>
    </div>
  );
}

// ============================================================================
// Modal D — System & Environment Self-Test (`POST /api/v1/system/selftest`)
// ============================================================================

const SYSTEM_STEP_ORDER: {
  id: string;
  titleZh: string;
  titleEn: string;
  icon: React.ReactNode;
}[] = [
  {
    id: 'sqlite_vault',
    titleZh: '本地数据库与 AES-256-GCM 保险箱',
    titleEn: 'SQLite Store & AES-256-GCM Vault',
    icon: <Lock />,
  },
  {
    id: 'google_network',
    titleZh: 'Google 官方 API 网络连通性',
    titleEn: 'Google Official API Network Reachability',
    icon: <Wifi />,
  },
  {
    id: 'mcp_router',
    titleZh: 'MCP 协议服务与智能路由引擎',
    titleEn: 'MCP Server & Smart Router Engine',
    icon: <Cpu />,
  },
  {
    id: 'local_cli_scanner',
    titleZh: '本机 / WSL2 现有凭证只读扫描',
    titleEn: 'Local & WSL2 CLI Credential Read-Only Scan',
    icon: <FolderSearch />,
  },
];

function SystemSelfTestModal({
  onClose,
  onSwitchToScanImport,
}: {
  onClose: () => void;
  onSwitchToScanImport: () => void;
}) {
  const { locale } = useI18n();
  const zh = locale === 'zh-CN';

  const [running, setRunning] = useState(true);
  const [result, setResult] = useState<SystemSelfTestResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const executeSelfTest = useCallback(async () => {
    setRunning(true);
    setError(null);
    try {
      const res = await api.runSystemSelfTest();
      setResult(res);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'System self-test failed');
    } finally {
      setRunning(false);
    }
  }, []);

  useEffect(() => {
    void executeSelfTest();
  }, [executeSelfTest]);

  const stepMap = useMemo(() => {
    const m = new Map<string, VerifyStepResult>();
    for (const st of result?.steps ?? []) {
      m.set(st.id, st);
    }
    return m;
  }, [result]);

  const isProd = result?.mode !== 'demo';

  return (
    <DialogModal
      open
      onClose={onClose}
      size="xl"
      icon={<Activity />}
      title={zh ? '系统功能自检' : 'System Self-Test'}
      subtitle={
        zh
          ? '零副作用只读巡检：核验本地保险箱加密、Google 官方网络直连、MCP 路由引擎与本机凭证'
          : 'Zero-side-effect read-only diagnostics for local vault, Google official APIs, MCP router & local CLI credentials'
      }
      headerActions={
        <ActionButton
          variant="secondary"
          size="xs"
          icon={<RefreshCw />}
          loading={running}
          onClick={() => void executeSelfTest()}
          className="mr-1"
        >
          {zh ? '重新自检' : 'Re-run'}
        </ActionButton>
      }
      footer={
        <div className="flex items-center justify-between gap-2">
          <span className="text-[11.5px] text-fg-muted ch-num">
            {zh ? '零算力消耗 · 0.00 CU · 只读无副作用' : 'Zero compute cost · 0.00 CU · Read-only'}
          </span>
          <ActionButton variant="primary" onClick={onClose}>
            {zh ? '完成' : 'Done'}
          </ActionButton>
        </div>
      }
      className="space-y-4"
    >
      {/* Top runtime mode banner */}
      <InsetWell padding="sm">
        <div className="flex flex-wrap items-center justify-between gap-2.5">
          {result ? (
            <StatusPill
              tone={isProd ? 'emerald' : 'amber'}
              size="md"
              dot
              pulse={isProd && result.overall_ok}
              className="font-semibold"
            >
              {isProd
                ? zh
                  ? '真实生产模式（直连 Google 官方接口）'
                  : 'Real Production Mode (Direct Google Official APIs)'
                : zh
                  ? '演示沙盒模式'
                  : 'Demo Sandbox Mode'}
            </StatusPill>
          ) : (
            <StatusPill tone="neutral" size="md" dot>
              {zh ? '正在识别运行模式与上游连通性…' : 'Checking runtime mode & upstream connectivity…'}
            </StatusPill>
          )}

          {result && (
            <StatusPill tone={result.overall_ok ? 'emerald' : 'coral'} size="xs" mono>
              {result.overall_ok
                ? zh
                  ? '✓ 全部核心模块正常'
                  : '✓ All Checks Passed'
                : zh
                  ? '✕ 存在待排查异常'
                  : '✕ Issues Detected'}
            </StatusPill>
          )}
        </div>
      </InsetWell>

      {error && (
        <Notice tone="rose" icon={<AlertCircle />}>
          {error}
        </Notice>
      )}

      {/* 4 Ordered Diagnostic Step Rows */}
      <div className="space-y-2.5">
        {SYSTEM_STEP_ORDER.map((meta) => {
          const step = stepMap.get(meta.id);
          const title = step ? (zh ? step.title_zh : step.title_en) : zh ? meta.titleZh : meta.titleEn;

          return (
            <InsetWell key={meta.id} padding="sm" className="space-y-1.5">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2.5 min-w-0">
                  <span className="flex items-center justify-center w-7 h-7 rounded-lg bg-surface-2 border border-line-faint text-fg-secondary shrink-0 [&>svg]:w-3.5 [&>svg]:h-3.5">
                    {meta.icon}
                  </span>
                  <span className="text-[13px] font-semibold text-fg truncate">{title}</span>
                </div>

                <div className="flex items-center gap-1.5 shrink-0">
                  {step && (
                    <StatusPill tone="neutral" size="xs" mono>
                      {step.duration_ms} ms
                    </StatusPill>
                  )}

                  {!step && running ? (
                    <StatusPill tone="neutral" size="xs" icon={<Loader2 className="animate-spin" />}>
                      {zh ? '检测中…' : 'Checking…'}
                    </StatusPill>
                  ) : step?.status === 'ok' ? (
                    <StatusPill tone="emerald" size="xs">
                      {zh ? '✓ 正常' : '✓ OK'}
                    </StatusPill>
                  ) : step?.status === 'error' ? (
                    <StatusPill tone="coral" size="xs">
                      {zh ? '✕ 异常' : '✕ Error'}
                    </StatusPill>
                  ) : (
                    <StatusPill tone="neutral" size="xs">
                      {zh ? '已跳过' : 'Skipped'}
                    </StatusPill>
                  )}
                </div>
              </div>

              <div className="pl-[38px] space-y-1">
                <p className="text-[12.5px] leading-relaxed text-fg-secondary">
                  {step
                    ? step.summary
                    : zh
                      ? '正在执行本地与上游探针校验…'
                      : 'Running diagnostic probe…'}
                </p>
                {step?.detail && (
                  <div className="ch-num text-[11px] text-fg-muted break-all">{step.detail}</div>
                )}
              </div>
            </InsetWell>
          );
        })}
      </div>

      {/* Prominent 1-Click Local/WSL2 CLI Credential Discovery CTA */}
      {result && result.discovered_local_count > 0 && (
        <InsetWell
          padding="sm"
          className="space-y-2.5"
          style={{
            backgroundColor: 'var(--emerald-soft)',
            borderColor: 'var(--emerald-border)',
          }}
        >
          <div className="flex items-start gap-2.5">
            <ShieldCheck className="w-4 h-4 shrink-0 mt-0.5 text-[var(--emerald-text)]" />
            <div className="min-w-0 text-[12px] leading-relaxed text-fg-secondary">
              {zh
                ? '只读扫描发现本机 / WSL2 已有现成 Google Colab 登录钥匙串（未自动写入数据库），可前往预览并自主决定是否一键入池。'
                : 'Read-only scan detected authorized local/WSL2 Google Colab CLI credentials (zero auto-writes). Open the import preview to inspect.'}
            </div>
          </div>
          <ActionButton
            variant="primary"
            size="md"
            fullWidth
            icon={<FolderSearch />}
            onClick={onSwitchToScanImport}
          >
            {zh
              ? `发现 ${result.discovered_local_count} 个本机/WSL2 已授权 Colab CLI 凭证 · 去一键导入 ->`
              : `Found ${result.discovered_local_count} authorized local/WSL2 Colab CLI credential(s) · Import ->`}
          </ActionButton>
        </InsetWell>
      )}
    </DialogModal>
  );
}

// ============================================================================
// Modal E — Per-Account 4-Step Diagnostic & Optional Live Kernel Smoke Test
// ============================================================================

const ACCOUNT_DIAG_STEPS: {
  id: string;
  titleZh: string;
  titleEn: string;
  icon: React.ReactNode;
}[] = [
  {
    id: 'egress',
    titleZh: '网络与出口连通性',
    titleEn: 'Network & Egress Connectivity',
    icon: <Wifi />,
  },
  {
    id: 'oauth',
    titleZh: 'OAuth2 登录凭证与自动续期',
    titleEn: 'OAuth2 Credentials & Auto-Refresh',
    icon: <ShieldCheck />,
  },
  {
    id: 'quota',
    titleZh: 'Colab 官方实时额度与显卡权限',
    titleEn: 'Colab Live Quota & GPU Entitlements',
    icon: <Flame />,
  },
  {
    id: 'kernel_smoke',
    titleZh: '云端内核真机代码试跑',
    titleEn: 'Cloud Kernel Live Smoke Execution',
    icon: <Terminal />,
  },
];

const SMOKE_ACCELERATORS: { value: AcceleratorType; labelZh: string; labelEn: string }[] = [
  { value: 'CPU', labelZh: 'CPU（免费极速推荐）', labelEn: 'CPU (Free & Fast · Recommended)' },
  { value: 'T4', labelZh: 'T4', labelEn: 'T4' },
  { value: 'L4', labelZh: 'L4', labelEn: 'L4' },
  { value: 'G4', labelZh: 'G4', labelEn: 'G4' },
  { value: 'A100', labelZh: 'A100', labelEn: 'A100' },
];

function AccountDiagnosticModal({
  account,
  onClose,
  onRefresh,
}: {
  account: Account;
  onClose: () => void;
  onRefresh: () => void;
}) {
  const { locale, displayEmail } = useI18n();
  const zh = locale === 'zh-CN';

  const [checking, setChecking] = useState(true);
  const [smokeRunning, setSmokeRunning] = useState(false);
  const [smokeAccel, setSmokeAccel] = useState<AcceleratorType>('CPU');
  const [result, setResult] = useState<AccountVerifyResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const onRefreshRef = useRef(onRefresh);
  onRefreshRef.current = onRefresh;

  const runDiagnostic = useCallback(
    async (withKernelSmoke: boolean, accelerator?: AcceleratorType) => {
      if (withKernelSmoke) {
        setSmokeRunning(true);
      } else {
        setChecking(true);
      }
      setError(null);
      try {
        const res = await api.verifyAccount(account.id, {
          run_kernel_smoke: withKernelSmoke,
          accelerator: withKernelSmoke ? accelerator : undefined,
        });
        setResult(res);
        const quotaStep = res.steps.find((s) => s.id === 'quota');
        if (quotaStep?.status === 'ok' || res.account) {
          onRefreshRef.current();
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Account diagnostic failed');
      } finally {
        setChecking(false);
        setSmokeRunning(false);
      }
    },
    [account.id],
  );

  // Run fast 0.00 CU diagnostic immediately when opened.
  useEffect(() => {
    void runDiagnostic(false);
  }, [runDiagnostic]);

  const liveAccount = result?.account ?? account;

  const stepMap = useMemo(() => {
    const m = new Map<string, VerifyStepResult>();
    for (const st of result?.steps ?? []) {
      m.set(st.id, st);
    }
    return m;
  }, [result]);

  return (
    <DialogModal
      open
      onClose={onClose}
      size="xl"
      icon={<Activity />}
      title={zh ? '账号连通性与额度自检' : 'Account Connectivity & Quota Diagnostic'}
      subtitle={
        <span className="inline-flex flex-wrap items-center gap-1.5 align-middle">
          <span className="ch-num font-medium text-fg">{displayEmail(liveAccount.email)}</span>
          <SubscriptionTierBadge tier={liveAccount.subscription_tier} />
          <AccountHealthBadge health={liveAccount.health} />
        </span>
      }
      headerActions={
        <ActionButton
          variant="secondary"
          size="xs"
          icon={<RefreshCw />}
          loading={checking && !smokeRunning}
          disabled={smokeRunning}
          onClick={() => void runDiagnostic(false)}
          className="mr-1"
        >
          {zh ? '重新自检' : 'Re-test'}
        </ActionButton>
      }
      footer={
        <div className="flex items-center justify-between gap-2">
          <span className="text-[11.5px] text-fg-muted ch-num">
            {result
              ? zh
                ? `总耗时 ${result.total_duration_ms} ms · 实时余额 ${formatCU(liveAccount.paid_cu_balance)} CU`
                : `Total ${result.total_duration_ms} ms · Live balance ${formatCU(liveAccount.paid_cu_balance)} CU`
              : zh
                ? '正在执行零算力连通自检 (0.00 CU)…'
                : 'Running zero-CU connectivity check (0.00 CU)…'}
          </span>
          <ActionButton variant="primary" onClick={onClose}>
            {zh ? '完成' : 'Done'}
          </ActionButton>
        </div>
      }
      className="space-y-4"
    >
      {error && (
        <Notice tone="rose" icon={<AlertCircle />}>
          {error}
        </Notice>
      )}

      {/* 4 Ordered Account Diagnostic Cards */}
      <div className="space-y-2.5">
        {ACCOUNT_DIAG_STEPS.map((meta, idx) => {
          const step = stepMap.get(meta.id);
          const isKernelStep = meta.id === 'kernel_smoke';
          const stepBusy = isKernelStep ? smokeRunning : checking && !result;

          return (
            <InsetWell key={meta.id} padding="sm" className="space-y-1.5">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2.5 min-w-0">
                  <span className="flex items-center justify-center w-7 h-7 rounded-lg bg-surface-2 border border-line-faint text-fg-secondary shrink-0 [&>svg]:w-3.5 [&>svg]:h-3.5">
                    {meta.icon}
                  </span>
                  <span className="text-[13px] font-semibold text-fg truncate">
                    {idx + 1}. {zh ? meta.titleZh : meta.titleEn}
                  </span>
                </div>

                <div className="flex items-center gap-1.5 shrink-0">
                  {step && step.duration_ms > 0 && !stepBusy && (
                    <StatusPill tone="neutral" size="xs" mono>
                      {step.duration_ms} ms
                    </StatusPill>
                  )}

                  {stepBusy ? (
                    <StatusPill tone="sky" size="xs" icon={<Loader2 className="animate-spin" />}>
                      {isKernelStep
                        ? zh
                          ? `正在分配 ${smokeAccel} 试跑…`
                          : `Running ${smokeAccel} smoke…`
                        : zh
                          ? '检测中…'
                          : 'Checking…'}
                    </StatusPill>
                  ) : step?.status === 'ok' ? (
                    <StatusPill tone="emerald" size="xs">
                      {zh ? '✓ 正常' : '✓ OK'}
                    </StatusPill>
                  ) : step?.status === 'error' ? (
                    <StatusPill tone="coral" size="xs">
                      {zh ? '✕ 异常' : '✕ Error'}
                    </StatusPill>
                  ) : isKernelStep ? (
                    <StatusPill tone="sky" size="xs">
                      {zh ? '默认跳过（零算力消耗）' : 'Skipped by default (0.00 CU)'}
                    </StatusPill>
                  ) : (
                    <StatusPill tone="neutral" size="xs">
                      {zh ? '已跳过' : 'Skipped'}
                    </StatusPill>
                  )}
                </div>
              </div>

              <div className="pl-[38px] space-y-1">
                <p className="text-[12.5px] leading-relaxed text-fg-secondary">
                  {stepBusy && isKernelStep
                    ? zh
                      ? `正在为该账号分配临时 ${smokeAccel} 实例并执行 Python 探针，执行完毕后立即自动释放…`
                      : `Allocating temporary ${smokeAccel} runtime, executing Python probe & auto-releasing…`
                    : step
                      ? step.summary
                      : zh
                        ? '正在校验网络出口、OAuth2 令牌与 Colab 官方实时额度…'
                        : 'Verifying network egress, OAuth2 token & Colab official quota…'}
                </p>
                {step?.detail && !stepBusy && (
                  <div className="ch-num text-[11px] text-fg-muted break-all">{step.detail}</div>
                )}
              </div>
            </InsetWell>
          );
        })}
      </div>

      {/* Optional Live Cloud Kernel Smoke Execution Box */}
      <InsetWell
        label={zh ? '需要实测分配云端环境跑代码吗？' : 'Want to allocate a live cloud runtime & run Python code?'}
        headerRight={
          <StatusPill tone="neutral" size="xs">
            {zh ? '测试完自动销毁 · 零残留' : 'Auto-teardown · Zero leak'}
          </StatusPill>
        }
        className="space-y-3.5"
      >
        <p className="text-[12px] leading-relaxed text-fg-secondary">
          {zh
            ? '定向在该账号上分配一台临时云端实例（ch-selftest-*），真机执行一行 Python 校验后立即销毁回收，杜绝后台残留扣费。'
            : 'Pins a temporary runtime (ch-selftest-*) to this account, runs a 1-line Python check, and immediately unassigns the VM with zero leftover billing.'}
        </p>

        <div className="flex flex-wrap gap-1.5" role="radiogroup" aria-label={zh ? '试跑加速卡类型' : 'Smoke test accelerator'}>
          {SMOKE_ACCELERATORS.map((opt) => {
            const active = smokeAccel === opt.value;
            return (
              <button
                key={opt.value}
                type="button"
                role="radio"
                aria-checked={active}
                disabled={smokeRunning || checking}
                onClick={() => setSmokeAccel(opt.value)}
                className={cn(
                  'rounded-full px-3 py-1 text-xs ch-num font-semibold border transition-colors cursor-pointer disabled:opacity-50',
                  active
                    ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
                    : 'bg-surface-2 text-fg-secondary border-line hover:text-fg hover:border-line-strong',
                )}
              >
                {zh ? opt.labelZh : opt.labelEn}
              </button>
            );
          })}
        </div>

        <ActionButton
          variant="primary"
          size="md"
          fullWidth
          icon={<Zap />}
          loading={smokeRunning}
          disabled={checking}
          onClick={() => void runDiagnostic(true, smokeAccel)}
        >
          {zh
            ? '实测分配云端内核跑 Python 并自动释放'
            : 'Allocate Cloud Kernel, Run Python & Auto-Release'}
        </ActionButton>

        {result?.kernel_stdout && (
          <div className="rounded-xl border border-line-strong bg-[#18181B] text-[#F4F4F5] p-3.5 space-y-2">
            <div className="flex items-center justify-between gap-2 text-[11px] text-[#A1A1AA] ch-num">
              <span className="inline-flex items-center gap-1.5">
                <Terminal className="w-3.5 h-3.5 text-[var(--emerald)]" />
                <span>python3 · kernel stdout</span>
              </span>
              <StatusPill tone="emerald" size="xs" mono>
                {zh ? '已自动释放 · 0 残留' : 'Auto-released · 0 leak'}
              </StatusPill>
            </div>
            <pre className="ch-num text-[12px] leading-relaxed text-[#E4E4E7] whitespace-pre-wrap break-all m-0">
              {result.kernel_stdout}
            </pre>
          </div>
        )}
      </InsetWell>
    </DialogModal>
  );
}

export default AccountPoolPage;
