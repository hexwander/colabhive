import React, { useMemo, useRef, useState } from 'react';
import {
  ArrowRight,
  Calculator,
  ChevronDown,
  ChevronUp,
  Cpu,
  Flame,
  Gauge,
  HeartHandshake,
  Lock,
  Plus,
  RefreshCw,
  SearchX,
  Settings2,
  Sparkles,
} from 'lucide-react';
import type {
  AcceleratorType,
  Account,
  GPUHourEquivalence,
  HardwareSpec,
  QuotaSummary,
  RateCalibrationSource,
  SubscriptionTier,
} from '../types';
import { ALL_ACCELERATORS, DEFAULT_HARDWARE_CATALOG } from '../types';
import { formatCU, formatHours } from '../lib/api';
import { useI18n } from '../i18n';
import { navigate } from '../components/layout/navigation';
import {
  formatRelativeCountdown,
  HardwareTierBadge,
} from '../components/layout/telemetryUi';
import {
  ActionButton,
  AnimatedNumber,
  CircularQuotaRing,
  cn,
  EmptyState,
  IconButton,
  InsetWell,
  LayeredCard,
  OrbitalCompassGraphic,
  QuickStartLaunchpad,
  SegmentedQuotaBar,
  SegmentedTabs,
  StatusPill,
  Switch,
  type QuotaRingTone,
  type SemanticTone,
} from '../components/ui/primitives';

export interface ObservatoryPageProps {
  summary: QuotaSummary;
  accounts: Account[];
  hardwareCatalog?: HardwareSpec[];
  refreshing?: boolean;
  onRefreshAll?: () => void;
  onRefreshAccount?: (accountId: string) => void;
  /** Route change handler. Falls back to the app router when omitted. */
  onNavigate?: (path: string) => void;
  /** Opens the "Connect AI assistant" drawer. Falls back to `QuickStartContext` when omitted. */
  onOpenConnect?: () => void;
}

type TierFilter = 'ALL' | 'AI_ULTRA' | 'PRO_PLUS' | 'PRO' | 'CONTRIBUTOR';

const SIM_GPU_OPTIONS: AcceleratorType[] = ['G4', 'H100', 'A100', 'L4', 'T4', 'V6E1'];

/** Warm Obsidian Ink elevation ring for the selected GPU tile in the calculator. */
const SELECTED_TILE_SHADOW =
  'inset 0 1px 0 var(--specular), 0 0 0 1.5px var(--accent), 0 14px 28px -14px rgba(24, 24, 27, 0.16)';

const fmtCU = (v: number) => formatCU(v);
const fmtCU0 = (v: number) => formatCU(v, 0);
const fmtSmartCU = (v: number) =>
  Math.abs(v - Math.round(v)) < 0.005 ? formatCU(v, 0) : formatCU(v, 2);
const fmtHours = (v: number) => formatHours(v);

/** Researcher-friendly "what is this GPU good for" copy (zh-CN). */
const GPU_WORKLOAD_ZH: Record<AcceleratorType, string> = {
  G4: '适合 26B+ 大模型全参微调与长上下文推理（96GB 大显存）',
  H100: '适合大模型高速训练、FP8 加速与大批量并发推理',
  A100: '适合 7B–14B 模型训练微调，CUDA / Triton 兼容性最佳',
  L4: '适合 7B–9B 推理、LoRA 微调与文生图任务（省额度首选）',
  T4: '适合代码调试、轻量评测与小模型实验（常有免费卡）',
  V6E1: '适合 JAX / XLA 框架与高性价比大规模矩阵计算',
  V5E1: '适合低成本特征提取、向量编码与轻量 TPU 实验',
  CPU: '适合数据清洗、结果汇总与无需显卡的预处理任务',
};

function describeWorkload(spec: HardwareSpec, zh: boolean): string {
  return zh ? (GPU_WORKLOAD_ZH[spec.accelerator] ?? spec.optimal_workload) : spec.optimal_workload;
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

function tierDisplayName(tier: SubscriptionTier, zh: boolean): string {
  switch (tier) {
    case 'AI_ULTRA':
      return 'AI Ultra';
    case 'PRO_PLUS':
      return 'Colab Pro+';
    case 'PRO':
      return 'Colab Pro';
    case 'PAYG':
      return zh ? '按量付费 PAYG' : 'Pay-As-You-Go';
    default:
      return zh ? '免费档 Free' : 'Free Tier';
  }
}

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

function computeAccountCycleTotal(account: Account): number {
  const ref =
    account.reference_cycle_cu > 0
      ? account.reference_cycle_cu
      : defaultCycleCuForTier(account.subscription_tier);
  return Math.max(ref, account.paid_cu_balance, 1);
}

function computeAccountCycleMax(account: Account): number {
  return Math.max(
    computeAccountCycleTotal(account),
    account.guardrails?.min_reserve_cu || 0,
    1,
  );
}

function computeAccountRemainPct(account: Account): number {
  const cycleMax = computeAccountCycleMax(account);
  const usable = account.is_contributor
    ? Math.max(0, account.schedulable_cu ?? account.paid_cu_balance - (account.guardrails?.min_reserve_cu || 0))
    : Math.max(0, account.paid_cu_balance);
  return Math.max(0, Math.min(100, (usable / cycleMax) * 100));
}

function isAccountWarning(account: Account): boolean {
  const pct = computeAccountRemainPct(account);
  if (account.expiring_soon_cu > 0) return true;
  if (pct <= 20) return true;
  return (
    account.health === 'AUTH_EXPIRED' ||
    account.health === 'PROXY_ERROR' ||
    account.health === 'COOLDOWN_RATE_LIMIT' ||
    account.health === 'QUOTA_LOW_FREE_ONLY' ||
    account.health === 'QUARANTINED_DENYLIST'
  );
}

function formatDaysCountdownShort(isoDate: string | undefined, zh: boolean): string {
  if (!isoDate) return zh ? '近期' : 'soon';
  const target = Date.parse(isoDate);
  if (Number.isNaN(target)) return zh ? '近期' : 'soon';
  const diffMs = target - Date.now();
  if (diffMs <= 0) return zh ? '今日' : 'today';
  const days = Math.ceil(diffMs / 86_400_000);
  if (days <= 1) return zh ? '1 天' : '1d';
  return zh ? `${days} 天` : `${days}d`;
}

function deriveBannerAccountName(account: Account, index: number, zh: boolean): string {
  if (account.alias) {
    if (account.is_contributor) {
      const clean = account.alias.replace(/^Contrib-/i, '').trim();
      return zh ? `${clean || '好友'} 共享` : `${clean || 'Friend'} Shared`;
    }
    return account.alias;
  }
  if (account.subscription_tier === 'AI_ULTRA') return 'Lab Ultra';
  if (account.subscription_tier === 'PRO_PLUS') return `Pro+ #${index + 1}`;
  if (account.subscription_tier === 'PRO') return `Pro #${index + 1}`;
  return account.email.split('@')[0] || `#${index + 1}`;
}

/** Friendly "how was this rate measured" pill for a GPU tile. */
function CalibrationPill({ source, zh }: { source: RateCalibrationSource; zh: boolean }) {
  if (source === 'live') {
    return (
      <StatusPill
        tone="emerald"
        size="xs"
        dot
        title={zh ? '按当前任务真实消耗测算，最准确' : 'Measured from live running tasks'}
      >
        {zh ? '实测耗速' : 'Live rate'}
      </StatusPill>
    );
  }
  if (source === 'empirical') {
    return (
      <StatusPill
        tone="sky"
        size="xs"
        dot
        title={zh ? '按近期历史任务平均消耗估算' : 'Estimated from historical usage'}
      >
        {zh ? '预估耗速' : 'Est. rate'}
      </StatusPill>
    );
  }
  return (
    <StatusPill
      tone="neutral"
      size="xs"
      dot
      title={zh ? '按 Colab 官方标准速率测算' : 'Colab official reference rate'}
    >
      {zh ? '官方标准' : 'Official std'}
    </StatusPill>
  );
}

export function ObservatoryPage({
  summary,
  accounts,
  hardwareCatalog = DEFAULT_HARDWARE_CATALOG,
  refreshing = false,
  onRefreshAll,
  onRefreshAccount,
  onNavigate,
  onOpenConnect,
}: ObservatoryPageProps) {
  const { locale, t, displayEmail } = useI18n();
  const zh = locale === 'zh-CN';
  const go = onNavigate ?? navigate;

  const readyLabel = zh ? '当前可自由支配' : 'Ready to use';

  // Top Subscription Tier Filter Strip state
  const [tierFilter, setTierFilter] = useState<TierFilter>('ALL');
  const [showAllAccounts, setShowAllAccounts] = useState<boolean>(false);

  // Matrix Scope & Controls
  const [matrixScope, setMatrixScope] = useState<'pool' | 'account'>('pool');
  const [selectedAccountId, setSelectedAccountId] = useState<string>('');
  const [deductReserves, setDeductReserves] = useState<boolean>(true);
  const matrixRef = useRef<HTMLDivElement | null>(null);
  const calcRef = useRef<HTMLDivElement | null>(null);

  // Parallel Shard Speedup Calculator (collapsed by default)
  const [shardCalcOpen, setShardCalcOpen] = useState<boolean>(false);
  const [simGpu, setSimGpu] = useState<AcceleratorType>('G4');
  const [simTotalGpuHours, setSimTotalGpuHours] = useState<number>(8);
  const [simShards, setSimShards] = useState<number>(4);

  const effectiveSelectedAccount = useMemo(() => {
    if (accounts.length === 0) return null;
    return accounts.find((a) => a.id === selectedAccountId) ?? accounts[0];
  }, [accounts, selectedAccountId]);

  const specByAcc = useMemo(() => {
    const m = new Map<AcceleratorType, HardwareSpec>();
    for (const s of hardwareCatalog) m.set(s.accelerator, s);
    for (const s of DEFAULT_HARDWARE_CATALOG) {
      if (!m.has(s.accelerator)) m.set(s.accelerator, s);
    }
    return m;
  }, [hardwareCatalog]);

  // Tier counts for the Top Subscription Tier Filter Strip
  const tierCounts = useMemo(() => {
    return {
      ALL: accounts.length,
      AI_ULTRA: accounts.filter((a) => a.subscription_tier === 'AI_ULTRA').length,
      PRO_PLUS: accounts.filter((a) => a.subscription_tier === 'PRO_PLUS' && !a.is_contributor).length +
        accounts.filter((a) => a.subscription_tier === 'PRO_PLUS' && a.is_contributor).length,
      PRO: accounts.filter((a) => a.subscription_tier === 'PRO').length,
      CONTRIBUTOR: accounts.filter((a) => a.is_contributor).length,
    };
  }, [accounts]);

  // Global health partition
  const { warningAccounts, abundantAccounts } = useMemo(() => {
    const warn: Account[] = [];
    const ok: Account[] = [];
    for (const a of accounts) {
      if (isAccountWarning(a)) warn.push(a);
      else ok.push(a);
    }
    return { warningAccounts: warn, abundantAccounts: ok };
  }, [accounts]);

  // Total G4 equivalent hours & Total Cycle Quota CU for the Global Quota Health Banner eyebrow
  const g4TotalHours = useMemo(() => {
    const poolG4 = summary.gpu_equivalence?.G4;
    if (poolG4 && poolG4.available_hours > 0) return poolG4.available_hours;
    const g4Spec = specByAcc.get('G4');
    const rate = g4Spec?.default_burn_rate_cu_hr || 9.66;
    return summary.total_available_cu / rate;
  }, [specByAcc, summary]);

  const totalCycleQuotaCu = useMemo(() => {
    if (accounts.length === 0) return 0;
    const sum = accounts.reduce((acc, a) => acc + computeAccountCycleTotal(a), 0);
    return Math.max(sum, summary.total_available_cu);
  }, [accounts, summary.total_available_cu]);

  // Filtered & smart-ordered accounts for the Stardust 2×2 Bento Grid:
  // Warning / Expiring first (Card 1) -> Contributor / Working (Card 2) -> Flagship Ultra / Abundant (Card 3)
  const filteredAccounts = useMemo(() => {
    const matching = accounts.filter((a) => {
      if (tierFilter === 'ALL') return true;
      if (tierFilter === 'CONTRIBUTOR') return a.is_contributor;
      return a.subscription_tier === tierFilter;
    });

    return [...matching].sort((a, b) => {
      const rankOf = (acc: Account): number => {
        if (isAccountWarning(acc)) return 0;
        if (acc.is_contributor) return 1;
        if (acc.active_assignments > 0 || acc.live_burn_rate_cu_hr > 0) return 2;
        if (acc.subscription_tier === 'AI_ULTRA') return 3;
        return 4;
      };
      const diff = rankOf(a) - rankOf(b);
      if (diff !== 0) return diff;
      return b.paid_cu_balance - a.paid_cu_balance;
    });
  }, [accounts, tierFilter]);

  // In default unfiltered mode with >3 accounts, show top 3 + Orbital Compass in the iconic 2×2 grid,
  // with a 1-click toggle bar underneath for any standby accounts.
  const canCollapseStandby = tierFilter === 'ALL' && filteredAccounts.length > 3;
  const visibleAccounts =
    canCollapseStandby && !showAllAccounts ? filteredAccounts.slice(0, 3) : filteredAccounts;
  const standbyAccounts =
    canCollapseStandby && !showAllAccounts ? filteredAccounts.slice(3) : [];
  // When visibleAccounts count is positive and even (e.g. 2 or 4 cards), span the Compass across 2 columns so the grid never has an empty cell.
  const compassSpansTwoCols = visibleAccounts.length > 0 && visibleAccounts.length % 2 === 0;

  // Compute 8-tier equivalence rows for either Pool-Total or Single-Account
  const matrixRows = useMemo(() => {
    return ALL_ACCELERATORS.map((acc) => {
      const spec = specByAcc.get(acc)!;

      if (matrixScope === 'account' && effectiveSelectedAccount) {
        const acct = effectiveSelectedAccount;
        const ent = acct.gpu_entitlements?.[acc];
        const precomputed = acct.gpu_equivalence?.[acc];
        const isLocked =
          Boolean(precomputed?.locked) ||
          ent?.status === 'TIER_RESTRICTED' ||
          ent?.status === 'INELIGIBLE' ||
          (acct.is_contributor &&
            acct.guardrails.allowed_gpus.length > 0 &&
            !acct.guardrails.allowed_gpus.includes(acc));

        const rate =
          ent?.calibrated_burn_rate ||
          precomputed?.burn_rate_cu_hr ||
          spec.default_burn_rate_cu_hr;
        const calSource =
          ent?.calibration_source || precomputed?.calibration_source || 'reference';

        const usableCu = deductReserves
          ? Math.max(0, acct.schedulable_cu ?? acct.paid_cu_balance - acct.guardrails.min_reserve_cu)
          : Math.max(0, acct.paid_cu_balance);

        const hours = isLocked ? 0 : rate > 0 ? usableCu / rate : 0;

        return {
          spec,
          burnRate: rate,
          calibrationSource: calSource,
          hours,
          eligibleAccounts: isLocked ? 0 : 1,
          maxParallelSlots: isLocked ? 0 : acct.max_concurrent_vms,
          freeWindows:
            spec.free_tier_eligible && (acct.free_quota?.remaining_free_cu ?? 0) > 0 ? 1 : 0,
          locked: isLocked,
          lockReason: ent?.reason,
        };
      }

      // Pool Total Scope
      const poolEq: GPUHourEquivalence | undefined = summary.gpu_equivalence?.[acc];
      const rate = poolEq?.burn_rate_cu_hr || spec.default_burn_rate_cu_hr;
      const calSource = poolEq?.calibration_source || 'reference';

      let hours = 0;
      if (poolEq) {
        hours = deductReserves ? poolEq.schedulable_hours : poolEq.available_hours;
      } else {
        const baseCu = deductReserves ? summary.schedulable_cu : summary.total_available_cu;
        hours = rate > 0 ? baseCu / rate : 0;
      }

      return {
        spec,
        burnRate: rate,
        calibrationSource: calSource,
        hours,
        eligibleAccounts: poolEq?.eligible_accounts ?? summary.accounts_healthy,
        maxParallelSlots: poolEq?.max_parallel_slots ?? summary.total_vm_slots,
        freeWindows:
          poolEq?.free_windows_ready ?? (spec.free_tier_eligible ? summary.free_t4_windows_ready : 0),
        locked: Boolean(poolEq?.locked),
        lockReason: undefined,
      };
    });
  }, [deductReserves, effectiveSelectedAccount, matrixScope, specByAcc, summary]);

  // Calculator maths (`1 GPU x 8h == 4 GPUs x 2h`)
  const simMetrics = useMemo(() => {
    const row = matrixRows.find((r) => r.spec.accelerator === simGpu) ?? matrixRows[0];
    const rate = row.burnRate || 9.66;
    const totalCuCost = simTotalGpuHours * rate;
    const serialWallHours = simTotalGpuHours;
    const parallelWallHours = simTotalGpuHours / Math.max(1, simShards);
    const parallelHourlyBurn = rate * simShards;
    const canFitSlots = row.maxParallelSlots >= simShards;
    const canAffordCu =
      (deductReserves ? summary.schedulable_cu : summary.total_available_cu) >= totalCuCost;

    return {
      row,
      rate,
      totalCuCost,
      serialWallHours,
      parallelWallHours,
      parallelHourlyBurn,
      canFitSlots,
      canAffordCu,
    };
  }, [deductReserves, matrixRows, simGpu, simShards, simTotalGpuHours, summary]);

  const simReady = simMetrics.canFitSlots && simMetrics.canAffordCu;

  const inspectAccountInMatrix = (accountId: string) => {
    setMatrixScope('account');
    setSelectedAccountId(accountId);
    matrixRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  };

  const selectGpuForSim = (acc: AcceleratorType) => {
    setSimGpu(acc);
    if (!shardCalcOpen) {
      setShardCalcOpen(true);
      window.requestAnimationFrame(() => {
        calcRef.current?.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      });
    }
  };

  const simGpuOptions = SIM_GPU_OPTIONS.includes(simGpu)
    ? SIM_GPU_OPTIONS
    : [...SIM_GPU_OPTIONS, simGpu];

  const tierPills: { key: TierFilter; label: string; count: number }[] = [
    { key: 'ALL', label: zh ? '全部' : 'All', count: tierCounts.ALL },
    { key: 'AI_ULTRA', label: 'AI Ultra', count: tierCounts.AI_ULTRA },
    { key: 'PRO_PLUS', label: 'Colab Pro+', count: tierCounts.PRO_PLUS },
    { key: 'PRO', label: 'Colab Pro', count: tierCounts.PRO },
    { key: 'CONTRIBUTOR', label: zh ? '好友共享' : 'Shared', count: tierCounts.CONTRIBUTOR },
  ];

  // Ordered banner summary pills: expiring/warning (Coral) -> contributor (Sky) -> healthy (Emerald)
  const bannerPills = useMemo(() => {
    const ordered = [...accounts].sort((a, b) => {
      const score = (x: Account) => (isAccountWarning(x) ? 0 : x.is_contributor ? 1 : 2);
      return score(a) - score(b);
    });

    return ordered.map((acct, idx) => {
      const name = deriveBannerAccountName(acct, idx, zh);
      const pct = Math.round(computeAccountRemainPct(acct));

      if (acct.expiring_soon_cu > 0) {
        const days = formatDaysCountdownShort(acct.earliest_batch_expires_at, zh);
        return {
          id: acct.id,
          tone: 'coral' as SemanticTone,
          text: zh
            ? `${name} · ${formatCU(acct.expiring_soon_cu, 0)} CU 剩 ${days}到期 · 优先消耗中`
            : `${name} · ${formatCU(acct.expiring_soon_cu, 0)} CU exp in ${days} · Prioritized`,
        };
      }

      if (isAccountWarning(acct)) {
        if (acct.is_contributor) {
          const reserve = Math.round(acct.guardrails?.min_reserve_cu || 0);
          const readyCu = Math.max(0, acct.schedulable_cu ?? acct.paid_cu_balance - reserve);
          return {
            id: acct.id,
            tone: 'coral' as SemanticTone,
            text: zh
              ? `${name} · 可调度 ~${formatCU(readyCu, 0)} CU (${pct}%) · 保底 ${reserve} CU`
              : `${name} · ~${formatCU(readyCu, 0)} CU ready (${pct}%) · ${reserve} CU reserve`,
          };
        }
        return {
          id: acct.id,
          tone: 'coral' as SemanticTone,
          text: zh
            ? `${name} · 余 ${fmtSmartCU(acct.paid_cu_balance)} / ${fmtSmartCU(computeAccountCycleTotal(acct))} CU (${pct}%) · 需关注`
            : `${name} · ${fmtSmartCU(acct.paid_cu_balance)} / ${fmtSmartCU(computeAccountCycleTotal(acct))} CU (${pct}%) · Low`,
        };
      }

      if (acct.is_contributor) {
        const reserve = Math.round(acct.guardrails?.min_reserve_cu || 0);
        return {
          id: acct.id,
          tone: 'sky' as SemanticTone,
          text: zh
            ? `${name} · ${fmtSmartCU(acct.schedulable_cu)} CU 可调度（自留 ${reserve} CU 保底）`
            : `${name} · ${fmtSmartCU(acct.schedulable_cu)} CU ready (${reserve} CU reserved)`,
        };
      }

      const stateWord =
        pct >= 80
          ? zh
            ? '极度充裕'
            : 'Abundant'
          : acct.active_assignments > 0
            ? zh
              ? '节奏健康'
              : 'Healthy pace'
            : zh
              ? '空闲可用'
              : 'Idle ready';

      return {
        id: acct.id,
        tone: 'emerald' as SemanticTone,
        text: `${name} · ${fmtSmartCU(acct.paid_cu_balance)} / ${fmtSmartCU(computeAccountCycleTotal(acct))} CU · ${stateWord}`,
      };
    });
  }, [accounts, zh]);

  return (
    <div className="space-y-5">
      {/* Collapsible QuickStart Launchpad (remembered when collapsed, re-openable from sidebar) */}
      <QuickStartLaunchpad
        summary={summary}
        accounts={accounts}
        onOpenConnect={onOpenConnect}
        onNavigate={onNavigate}
        onRefreshTelemetry={onRefreshAll}
      />

      {/* ===================================================================
       * 1. Top Subscription Tier Filter Strip (Image 2 Stardust Top Strip)
       * ================================================================= */}
      <LayeredCard padding="none" staggerIndex={0} className="rounded-[20px]">
        <div className="px-4 sm:px-5 py-3 flex flex-col lg:flex-row lg:items-center lg:justify-between gap-3">
          <div className="flex flex-wrap items-center gap-2 min-w-0">
            <span className="text-[12.5px] font-bold text-fg mr-1 shrink-0">
              {zh ? '按订阅档位筛选卡池' : 'Filter pool by tier'}
            </span>

            {tierPills.map((pill) => {
              const selected = tierFilter === pill.key;
              return (
                <button
                  key={pill.key}
                  type="button"
                  aria-pressed={selected}
                  onClick={() => setTierFilter(pill.key)}
                  className={cn(
                    'inline-flex items-center gap-1.5 h-7 px-3 rounded-full text-[12px] border transition-all cursor-pointer select-none',
                    selected
                      ? 'bg-surface-2 text-fg font-bold border-[var(--accent)] shadow-[var(--shadow-control)]'
                      : 'bg-surface-1 text-fg-secondary font-medium border-line-faint hover:text-fg hover:border-line-strong',
                  )}
                >
                  <span>{pill.label}</span>
                  <span
                    className={cn(
                      'ch-num text-[11px]',
                      selected ? 'text-fg font-semibold' : 'text-fg-muted',
                    )}
                  >
                    ({pill.count})
                  </span>
                </button>
              );
            })}
          </div>

          <div className="flex flex-wrap items-center gap-2 shrink-0 lg:justify-end">
            <ActionButton
              variant="secondary"
              size="sm"
              onClick={onRefreshAll}
              loading={refreshing}
              icon={<RefreshCw />}
            >
              {zh ? '刷新余额' : t('pool.refresh_all')}
            </ActionButton>
            <ActionButton
              variant="primary"
              size="sm"
              onClick={() => go('/pool')}
              icon={<Plus />}
            >
              {zh ? '添加账号' : 'Add Account'}
            </ActionButton>
          </div>
        </div>
      </LayeredCard>

      {/* ===================================================================
       * 2. Global Quota Health Banner Card (Image 2 Headline + Soft Pills)
       * ================================================================= */}
      <LayeredCard padding="md" staggerIndex={1} className="rounded-[20px]">
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2 text-[12px] text-fg-muted ch-num">
            <span>
              {zh ? (
                <>
                  全局算力健康度 · {accounts.length} 个已接入账号 · 总剩余{' '}
                  <AnimatedNumber value={summary.total_available_cu} format={fmtSmartCU} /> / 总额{' '}
                  <AnimatedNumber value={totalCycleQuotaCu} format={fmtSmartCU} /> CU（约可跑{' '}
                  <AnimatedNumber value={g4TotalHours} format={fmtHours} /> G4）
                </>
              ) : (
                <>
                  Global Compute Health · {accounts.length} pooled accounts ·{' '}
                  <AnimatedNumber value={summary.total_available_cu} format={fmtSmartCU} /> /{' '}
                  <AnimatedNumber value={totalCycleQuotaCu} format={fmtSmartCU} /> CU remaining (~
                  <AnimatedNumber value={g4TotalHours} format={fmtHours} /> G4)
                </>
              )}
            </span>

            <span className="text-[11.5px] text-fg-muted">
              {zh ? (
                <>
                  可自由调度 <AnimatedNumber value={summary.schedulable_cu} format={fmtCU0} /> CU · ≈ $
                  <AnimatedNumber value={summary.estimated_value_usd} format={fmtCU} />
                </>
              ) : (
                <>
                  Schedulable <AnimatedNumber value={summary.schedulable_cu} format={fmtCU0} /> CU · ≈ $
                  <AnimatedNumber value={summary.estimated_value_usd} format={fmtCU} />
                </>
              )}
            </span>
          </div>

          <h1 className="ui-title text-[20px] sm:text-[22px] font-bold tracking-tight text-fg">
            {accounts.length === 0 ? (
              zh ? (
                '尚未接入算力账号，添加首个账号即可汇总全局算力'
              ) : (
                'No accounts pooled yet — add your first account to unify compute'
              )
            ) : warningAccounts.length > 0 ? (
              <>
                <span className="text-[var(--coral-text)]">
                  {zh
                    ? `${warningAccounts.length} 个账号临期预警`
                    : `${warningAccounts.length} account${warningAccounts.length > 1 ? 's' : ''} expiring / low`}
                </span>
                {zh
                  ? `，${abundantAccounts.length} 个账号算力充裕`
                  : `, ${abundantAccounts.length} account${abundantAccounts.length === 1 ? '' : 's'} abundant`}
              </>
            ) : summary.active_vms > 0 ? (
              zh ? (
                `全部账号算力充裕，${summary.active_vms} 台 GPU 正在运行`
              ) : (
                `All accounts abundant · ${summary.active_vms} GPUs actively running`
              )
            ) : zh ? (
              `全部 ${accounts.length} 个账号算力充裕，随时可启动多卡实验`
            ) : (
              `All ${accounts.length} accounts abundant and ready`
            )}
          </h1>

          {bannerPills.length > 0 && (
            <div className="pt-0.5 flex flex-wrap items-center gap-2">
              {bannerPills.map((pill) => (
                <button
                  key={pill.id}
                  type="button"
                  onClick={() => inspectAccountInMatrix(pill.id)}
                  title={zh ? '点击在下方算力矩阵查看该账号可用时长' : 'Inspect this account in GPU matrix'}
                  className="cursor-pointer bg-transparent p-0 border-0 text-left transition-transform duration-150 hover:-translate-y-px active:translate-y-0"
                >
                  <StatusPill tone={pill.tone} size="md" className="ch-num font-semibold">
                    {pill.text}
                  </StatusPill>
                </button>
              ))}
            </div>
          )}
        </div>
      </LayeredCard>

      {/* ===================================================================
       * 3. Signature 2×2 Stardust Account & Orbital Compass Bento Grid
       * ================================================================= */}
      <section className="space-y-3" aria-label={zh ? '全局算力罗盘与账号配额' : 'Compute Compass & Account Quotas'}>
        {accounts.length === 0 ? (
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            <LayeredCard className="rounded-[20px]">
              <EmptyState
                icon={<Gauge />}
                title={zh ? '还没有添加任何 Colab 账号' : 'No Colab accounts added yet'}
                description={
                  zh
                    ? '用 Google 登录添加自己的账号（无需填密码），一键扫描导入本机 CLI 凭证，或发一条零云盘权限链接请朋友共享闲置算力。'
                    : 'Sign in with Google (no password), import local CLI credentials, or share a zero-Drive safety link with friends.'
                }
                action={
                  <ActionButton variant="primary" icon={<Plus />} onClick={() => go('/pool')}>
                    {t('pool.add_account')}
                  </ActionButton>
                }
              />
            </LayeredCard>

            <GlobalComputeCompassCard
              summary={summary}
              totalAccounts={0}
              healthyCount={0}
              warningCount={0}
              zh={zh}
              onSwitchStrategy={() => go('/router')}
              onOpenWorkbench={() => go('/exp')}
            />
          </div>
        ) : filteredAccounts.length === 0 ? (
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            <LayeredCard className="rounded-[20px]">
              <EmptyState
                icon={<SearchX />}
                title={zh ? '当前档位下暂无账号' : 'No accounts in this tier'}
                description={
                  zh
                    ? '切换到「全部」查看池内其他订阅档位账号，或前往账号池添加新账号。'
                    : 'Switch back to "All" to see other tiers, or add a new account.'
                }
                action={
                  <ActionButton variant="secondary" onClick={() => setTierFilter('ALL')}>
                    {zh ? '查看全部账号' : 'Show all accounts'}
                  </ActionButton>
                }
              />
            </LayeredCard>

            <GlobalComputeCompassCard
              summary={summary}
              totalAccounts={accounts.length}
              healthyCount={abundantAccounts.length}
              warningCount={warningAccounts.length}
              zh={zh}
              onSwitchStrategy={() => go('/router')}
              onOpenWorkbench={() => go('/exp')}
            />
          </div>
        ) : (
          <>
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              {visibleAccounts.map((acct, index) => (
                <AccountQuotaCard
                  key={acct.id}
                  account={acct}
                  staggerIndex={index + 2}
                  onRefresh={onRefreshAccount ? () => onRefreshAccount(acct.id) : undefined}
                  onInspectMatrix={() => inspectAccountInMatrix(acct.id)}
                  onOpenPool={() => go('/pool')}
                  onOpenWorkbench={() => go('/exp')}
                />
              ))}

              <GlobalComputeCompassCard
                summary={summary}
                totalAccounts={accounts.length}
                healthyCount={abundantAccounts.length}
                warningCount={warningAccounts.length}
                zh={zh}
                spanFull={compassSpansTwoCols}
                staggerIndex={visibleAccounts.length + 2}
                onSwitchStrategy={() => go('/router')}
                onOpenWorkbench={() => go('/exp')}
              />
            </div>

            {/* Compact standby toggle strip when >3 accounts exist in ALL mode */}
            {canCollapseStandby && (
              <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-2.5 rounded-[16px] bg-surface-1 border border-line-faint text-[12px]">
                <div className="flex flex-wrap items-center gap-2.5 min-w-0 text-fg-secondary">
                  {!showAllAccounts ? (
                    <>
                      <span className="font-medium text-fg">
                        {zh
                          ? `另有 ${standbyAccounts.length} 个后备账号待命：`
                          : `${standbyAccounts.length} standby account${standbyAccounts.length > 1 ? 's' : ''} ready:`}
                      </span>
                      {standbyAccounts.map((st) => {
                        const pct = Math.round(computeAccountRemainPct(st));
                        return (
                          <button
                            key={st.id}
                            type="button"
                            onClick={() => setShowAllAccounts(true)}
                            className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full bg-surface-2 border border-line text-fg ch-num cursor-pointer hover:border-line-strong"
                          >
                            <CircularQuotaRing
                              value={pct}
                              size={16}
                              strokeWidth={2.4}
                              tone="emerald"
                              showValue={false}
                            />
                            <span className="font-medium">
                              {st.alias || displayEmail(st.email)}
                            </span>
                            <span className="text-fg-muted">
                              {formatCU(st.paid_cu_balance, 0)} CU ({pct}%)
                            </span>
                          </button>
                        );
                      })}
                    </>
                  ) : (
                    <span>
                      {zh
                        ? `已展开全部 ${filteredAccounts.length} 个账号配额卡片`
                        : `Showing all ${filteredAccounts.length} account quota cards`}
                    </span>
                  )}
                </div>

                <button
                  type="button"
                  onClick={() => setShowAllAccounts((prev) => !prev)}
                  className="inline-flex items-center gap-1 text-[12px] font-semibold text-fg hover:text-fg-secondary cursor-pointer bg-transparent border-0 p-0 shrink-0"
                >
                  {showAllAccounts ? (
                    <>
                      <span>{zh ? '收起为 2×2 精选视图' : 'Compact 2×2 view'}</span>
                      <ChevronUp className="w-3.5 h-3.5" />
                    </>
                  ) : (
                    <>
                      <span>
                        {zh ? `展开全部 ${filteredAccounts.length} 张卡片` : `Expand all ${filteredAccounts.length} cards`}
                      </span>
                      <ChevronDown className="w-3.5 h-3.5" />
                    </>
                  )}
                </button>
              </div>
            )}
          </>
        )}
      </section>

      {/* ===================================================================
       * 4. 8-Tier Cloud GPU Equivalent Runtime Matrix (+ Speedup Calculator)
       * ================================================================= */}
      <LayeredCard ref={matrixRef} padding="none" staggerIndex={5} className="rounded-[20px] scroll-mt-6">
        <div className="flex flex-col xl:flex-row xl:items-start xl:justify-between gap-3 px-5 sm:px-6 py-4 border-b border-line-faint">
          <div className="min-w-0">
            <h3 className="text-[14.5px] font-bold leading-snug text-fg">{t('matrix.title')}</h3>
            <p className="mt-0.5 text-[12.5px] leading-relaxed text-fg-secondary">
              {t('matrix.subtitle')}
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2 xl:justify-end xl:shrink-0">
            <SegmentedTabs
              size="sm"
              aria-label={t('matrix.title')}
              value={matrixScope}
              onChange={setMatrixScope}
              options={[
                { value: 'pool', label: t('matrix.scope_pool') },
                { value: 'account', label: t('matrix.scope_account') },
              ]}
            />

            {matrixScope === 'account' && accounts.length > 0 && (
              <select
                value={effectiveSelectedAccount?.id ?? ''}
                onChange={(e) => setSelectedAccountId(e.target.value)}
                aria-label={t('matrix.scope_account')}
                className="ui-input ch-num w-auto max-w-[240px]"
              >
                {accounts.map((acc) => (
                  <option key={acc.id} value={acc.id}>
                    {acc.alias ? `${acc.alias} · ` : ''}
                    {displayEmail(acc.email)} ({formatCU(acc.schedulable_cu, 1)} CU)
                  </option>
                ))}
              </select>
            )}

            <label
              className="inline-flex items-center gap-2 px-1 text-[12px] text-fg-secondary select-none cursor-pointer"
              title={
                zh
                  ? '开启后只算你能随时动用的额度：已扣除共享账号号主给自己保留的部分'
                  : 'When on, hours only count quota you can spend right now — owner reserves on shared accounts are deducted'
              }
            >
              <Switch checked={deductReserves} onChange={setDeductReserves} label={readyLabel} />
              <span>{readyLabel}</span>
            </label>

            <ActionButton
              variant={shardCalcOpen ? 'primary' : 'secondary'}
              size="sm"
              icon={<Calculator />}
              aria-expanded={shardCalcOpen}
              onClick={() => setShardCalcOpen((open) => !open)}
            >
              {zh ? '并行省时计算器' : 'Speedup Calculator'}
            </ActionButton>
          </div>
        </div>

        <div className="p-5 sm:p-6 space-y-4">
          {/* Collapsible shard speedup calculator */}
          {shardCalcOpen && (
            <InsetWell ref={calcRef} padding="md" className="ui-anim-fade scroll-mt-6 space-y-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 min-w-0">
                  <span className="text-[13px] font-bold text-fg">
                    {t('matrix.shard_simulator')}
                  </span>
                  <span className="ch-num text-[12px] text-fg-secondary">
                    1× {simGpu} × {simTotalGpuHours}h = {simShards}× {simGpu} ×{' '}
                    <AnimatedNumber
                      value={simMetrics.parallelWallHours}
                      format={fmtHours}
                      animateOnMount={false}
                      durationMs={260}
                    />
                  </span>
                  <StatusPill tone="emerald" size="xs" mono>
                    {zh ? `快 ${simShards} 倍 · 不多花额度` : `${simShards}× faster · 0 extra CU`}
                  </StatusPill>
                </div>
                <ActionButton
                  variant="primary"
                  size="xs"
                  icon={<Sparkles />}
                  onClick={() => go('/exp')}
                >
                  {zh ? `去实验实况启动 ${simShards} 卡并行` : `Launch ${simShards}× in parallel`}
                </ActionButton>
              </div>

              <div className="grid grid-cols-1 md:grid-cols-3 gap-5">
                {/* Target accelerator */}
                <div className="space-y-2">
                  <div className="flex items-center justify-between gap-2 text-[12px]">
                    <span className="text-fg-secondary">{zh ? '用哪种显卡' : 'Which GPU'}</span>
                    <span className="ch-num text-fg-muted">
                      {simGpu} · {formatCU(simMetrics.rate)} CU/h
                    </span>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {simGpuOptions.map((g) => {
                      const active = simGpu === g;
                      return (
                        <button
                          key={g}
                          type="button"
                          aria-pressed={active}
                          onClick={() => setSimGpu(g)}
                          className={cn(
                            'rounded-full px-2.5 py-1 text-[11.5px] ch-num font-semibold border transition-colors cursor-pointer',
                            active
                              ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
                              : 'bg-surface-2 text-fg-secondary border-line hover:border-line-strong hover:text-fg',
                          )}
                        >
                          {g}
                        </button>
                      );
                    })}
                  </div>
                </div>

                {/* Single-GPU workload */}
                <div className="space-y-2">
                  <div className="flex items-center justify-between gap-2 text-[12px]">
                    <label htmlFor="sim-workload" className="text-fg-secondary">
                      {zh ? '只用 1 张卡要跑多久' : 'Runtime on a single GPU'}
                    </label>
                    <span className="ch-num font-semibold text-fg">
                      {simTotalGpuHours} {zh ? '小时' : 'h'}
                    </span>
                  </div>
                  <input
                    id="sim-workload"
                    type="range"
                    min={1}
                    max={48}
                    step={1}
                    value={simTotalGpuHours}
                    onChange={(e) => setSimTotalGpuHours(Number(e.target.value))}
                    className="w-full accent-[var(--accent)] cursor-pointer"
                  />
                  <div className="flex justify-between ch-num text-[10.5px] text-fg-muted">
                    <span>1h</span>
                    <span>48h</span>
                  </div>
                </div>

                {/* Parallel shards */}
                <div className="space-y-2">
                  <div className="flex items-center justify-between gap-2 text-[12px]">
                    <label htmlFor="sim-shards" className="text-fg-secondary">
                      {zh ? '同时用几张卡' : 'GPUs in parallel'}
                    </label>
                    <span className="ch-num font-semibold text-fg">{simShards}×</span>
                  </div>
                  <input
                    id="sim-shards"
                    type="range"
                    min={1}
                    max={8}
                    step={1}
                    value={simShards}
                    onChange={(e) => setSimShards(Number(e.target.value))}
                    className="w-full accent-[var(--accent)] cursor-pointer"
                  />
                  <div className="flex justify-between ch-num text-[10.5px] text-fg-muted">
                    <span>1×</span>
                    <span>8×</span>
                  </div>
                </div>
              </div>

              {/* Wall-clock comparison using SegmentedQuotaBar */}
              <div className="grid grid-cols-1 md:grid-cols-2 gap-x-6 gap-y-3 pt-1">
                <div className="space-y-1.5">
                  <div className="flex flex-wrap items-center justify-between gap-x-3 text-[12px]">
                    <span className="text-fg-secondary">
                      {zh ? '只用 1 张卡慢慢跑' : 'One GPU, serial'}
                    </span>
                    <span className="ch-num text-fg-secondary">
                      {formatHours(simMetrics.serialWallHours)} ·{' '}
                      <span className="font-semibold text-fg">
                        <AnimatedNumber
                          value={simMetrics.totalCuCost}
                          format={fmtCU}
                          animateOnMount={false}
                          durationMs={260}
                        />{' '}
                        CU
                      </span>
                    </span>
                  </div>
                  <SegmentedQuotaBar value={100} segments={22} tone="amber" />
                </div>

                <div className="space-y-1.5">
                  <div className="flex flex-wrap items-center justify-between gap-x-3 text-[12px]">
                    <span className="text-fg-secondary">
                      {zh ? `${simShards} 张卡同时跑` : `${simShards} GPUs at once`}
                    </span>
                    <span className="ch-num text-fg-secondary">
                      <AnimatedNumber
                        value={simMetrics.parallelWallHours}
                        format={fmtHours}
                        animateOnMount={false}
                        durationMs={260}
                        className="font-semibold text-[var(--emerald-text)]"
                      />{' '}
                      @ {formatCU(simMetrics.parallelHourlyBurn)} CU/h ·{' '}
                      <span className="font-semibold text-fg">
                        <AnimatedNumber
                          value={simMetrics.totalCuCost}
                          format={fmtCU}
                          animateOnMount={false}
                          durationMs={260}
                        />{' '}
                        CU
                      </span>
                    </span>
                  </div>
                  <SegmentedQuotaBar
                    value={Math.max(8, (1 / Math.max(1, simShards)) * 100)}
                    segments={22}
                    tone="emerald"
                  />
                </div>
              </div>

              <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-1.5 pt-3 border-t border-line-faint text-[11.5px]">
                <span className="text-fg-muted">{t('matrix.zero_extra_cu')}</span>
                <span
                  className={cn(
                    'inline-flex items-start gap-1.5',
                    simReady ? 'text-[var(--emerald-text)]' : 'text-[var(--coral-text)]',
                  )}
                >
                  <span
                    className="mt-[5px] w-1.5 h-1.5 rounded-full shrink-0"
                    style={{ backgroundColor: simReady ? 'var(--emerald)' : 'var(--coral)' }}
                  />
                  <span>
                    {simReady
                      ? zh
                        ? `现在最多能同时开 ${simMetrics.row.maxParallelSlots} 台 ${simGpu}，足够立即并行启动 ${simShards} 张`
                        : `Up to ${simMetrics.row.maxParallelSlots}× ${simGpu} can run at once — enough to start ${simShards} right now`
                      : zh
                        ? `现在最多只能同时开 ${simMetrics.row.maxParallelSlots} 台 ${simGpu}，或可用额度不够；可以多添加几个账号，或允许缺卡时自动换用其他型号`
                        : `Only ${simMetrics.row.maxParallelSlots}× ${simGpu} can run at once, or quota is short — add accounts or allow automatic GPU fallback`}
                  </span>
                </span>
              </div>
            </InsetWell>
          )}

          {/* 8 GPU bento tiles: clean monochrome-stone + emerald/ink pills */}
          <div className="ui-stagger grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
            {matrixRows.map((row) => {
              const { spec } = row;
              const selectedForSim = shardCalcOpen && simGpu === spec.accelerator;
              const lockReason = localizeLockReason(row.lockReason, zh);
              const description = lockReason || describeWorkload(spec, zh);

              return (
                <div
                  key={spec.accelerator}
                  role="button"
                  tabIndex={0}
                  aria-pressed={selectedForSim}
                  title={
                    zh
                      ? `点击用 ${spec.short_label} 估算多卡并行能省多少时间`
                      : `Estimate parallel speedup on ${spec.short_label}`
                  }
                  onClick={() => selectGpuForSim(spec.accelerator)}
                  onKeyDown={(e) => {
                    if (e.target !== e.currentTarget) return;
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault();
                      selectGpuForSim(spec.accelerator);
                    }
                  }}
                  className={cn(
                    'ui-bento-tile gap-3 cursor-pointer outline-none',
                    'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent-ring)]',
                    selectedForSim && 'bg-surface-2',
                  )}
                  style={selectedForSim ? { boxShadow: SELECTED_TILE_SHADOW } : undefined}
                >
                  <div className="flex items-center justify-between gap-2">
                    <HardwareTierBadge accelerator={spec.accelerator} label={spec.short_label} />
                    <CalibrationPill source={row.calibrationSource} zh={zh} />
                  </div>

                  <div className="flex items-baseline justify-between gap-2 min-h-[28px]">
                    {row.locked ? (
                      <StatusPill tone="amber" icon={<Lock />}>
                        {t('matrix.locked')}
                      </StatusPill>
                    ) : (
                      <AnimatedNumber
                        value={row.hours}
                        format={fmtHours}
                        className="text-[22px] leading-7 font-bold tracking-tight text-fg"
                      />
                    )}
                    <span className="ch-num text-[11.5px] text-fg-muted shrink-0">
                      @ {formatCU(row.burnRate)} CU/h
                    </span>
                  </div>

                  <div
                    className={cn(
                      'text-[11.5px] leading-relaxed line-clamp-2 min-h-[36px]',
                      lockReason ? 'text-[var(--amber-text)]' : 'text-fg-secondary',
                    )}
                    title={description}
                  >
                    {description}
                  </div>

                  <div className="mt-auto border-t border-line-faint pt-2 text-[11px] ch-num text-fg-muted flex items-center justify-between gap-2">
                    <span className="truncate min-w-0">
                      {zh
                        ? `${row.eligibleAccounts} 个账号 · 可同开 ${row.maxParallelSlots} 台`
                        : `${row.eligibleAccounts} accts · ${row.maxParallelSlots} at once`}
                    </span>
                    {row.freeWindows > 0 ? (
                      <span className="whitespace-nowrap shrink-0 font-semibold text-[var(--emerald-text)]">
                        +{row.freeWindows} {zh ? '台免费可用' : 'Free'}
                      </span>
                    ) : (
                      <span className="whitespace-nowrap shrink-0">
                        {spec.vram_gb > 0
                          ? zh
                            ? `${spec.vram_gb} GB 显存`
                            : `${spec.vram_gb} GB VRAM`
                          : zh
                            ? '云端 CPU'
                            : 'Cloud CPU'}
                      </span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      </LayeredCard>
    </div>
  );
}

// ============================================================================
// Stardust Account Quota Card (Image 2 2×2 Bento Card)
// ============================================================================

function AccountQuotaCard({
  account,
  staggerIndex,
  onRefresh,
  onInspectMatrix,
  onOpenPool,
  onOpenWorkbench,
}: {
  account: Account;
  staggerIndex?: number;
  onRefresh?: () => void;
  onInspectMatrix: () => void;
  onOpenPool: () => void;
  onOpenWorkbench: () => void;
}) {
  const { locale, displayEmail } = useI18n();
  const zh = locale === 'zh-CN';

  const cycleTotalCu = computeAccountCycleTotal(account);
  const cycleMax = computeAccountCycleMax(account);
  const reserveCu = Math.max(0, account.guardrails?.min_reserve_cu || 0);
  const schedulableCu = Math.max(
    0,
    account.schedulable_cu ?? account.paid_cu_balance - reserveCu,
  );

  const paidPct = Math.max(0, Math.min(100, (account.paid_cu_balance / cycleMax) * 100));
  const remainPct = computeAccountRemainPct(account);

  const hasAuthOrProxyErr =
    account.health === 'AUTH_EXPIRED' ||
    account.health === 'PROXY_ERROR' ||
    account.health === 'QUARANTINED_DENYLIST';
  const hasExpiring = account.expiring_soon_cu > 0;
  const isLow = remainPct <= 20 || account.health === 'QUOTA_LOW_FREE_ONLY';

  // Semantic state pill + CircularQuotaRing tone
  let semanticLabel: string;
  let semanticTone: SemanticTone;
  let ringTone: QuotaRingTone;
  let paidBarTone: 'emerald' | 'coral' | 'amber' | 'sky';

  if (hasAuthOrProxyErr || remainPct <= 15) {
    semanticLabel = zh ? '今日将耗尽' : 'Depleting';
    semanticTone = 'coral';
    ringTone = 'coral';
    paidBarTone = 'coral';
  } else if (hasExpiring) {
    semanticLabel = zh ? '临期优先' : 'Expiring First';
    semanticTone = 'coral';
    ringTone = 'coral';
    paidBarTone = paidPct <= 35 ? 'coral' : 'emerald';
  } else if (account.is_contributor) {
    semanticLabel = zh ? '好友共享' : 'Shared';
    semanticTone = 'sky';
    ringTone = remainPct <= 35 ? 'amber' : 'emerald';
    paidBarTone = remainPct <= 35 ? 'amber' : 'emerald';
  } else if (remainPct >= 80) {
    semanticLabel = zh ? '极度充裕' : 'Abundant';
    semanticTone = 'emerald';
    ringTone = 'emerald';
    paidBarTone = 'emerald';
  } else {
    semanticLabel = zh ? '节奏健康' : 'Healthy Pace';
    semanticTone = isLow ? 'amber' : 'emerald';
    ringTone = isLow ? 'amber' : 'emerald';
    paidBarTone = isLow ? 'amber' : 'emerald';
  }

  // Row B: Free T4 window OR Schedulable ratio with reserve floor / expiring batch
  const freeCu = account.free_quota?.remaining_free_cu ?? 0;
  let rowBLabel: string;
  let rowBPct: number;
  let rowBRightText: React.ReactNode;
  let rowBTone: 'emerald' | 'coral' | 'amber' | 'sky' = 'sky';

  if (reserveCu > 0) {
    rowBPct = Math.max(0, Math.min(100, Math.round(remainPct)));
    rowBTone = remainPct <= 15 ? 'coral' : 'sky';
    rowBLabel = zh
      ? `可自由调度比例 · 自留保底 ${formatCU(reserveCu, 0)} CU`
      : `Schedulable share · ${formatCU(reserveCu, 0)} CU owner reserve`;
    rowBRightText = <span className="ch-num text-[12.5px] font-bold text-fg">{rowBPct}%</span>;
  } else if (freeCu > 0) {
    rowBPct = Math.max(15, Math.min(100, Math.round((freeCu / 15) * 100)));
    rowBLabel = zh ? '免费 T4 刷新窗口 · 持续可用' : 'Free T4 dynamic window · Ready';
    rowBRightText = <span className="ch-num text-[12.5px] font-bold text-fg">{rowBPct}%</span>;
  } else if (hasExpiring) {
    const expCountdown = formatRelativeCountdown(account.earliest_batch_expires_at, locale);
    rowBPct = Math.max(8, Math.min(100, Math.round((account.expiring_soon_cu / cycleMax) * 100)));
    rowBTone = 'coral';
    rowBLabel = zh
      ? `临期额度批次 · ${formatCU(account.expiring_soon_cu, 0)} CU 剩 ${expCountdown}`
      : `Expiring batch · ${formatCU(account.expiring_soon_cu, 0)} CU (${expCountdown} left)`;
    rowBRightText = (
      <span className="ch-num text-[12.5px] font-bold text-[var(--coral-text)]">{rowBPct}%</span>
    );
  } else {
    rowBPct = Math.round(paidPct);
    rowBLabel = zh ? '可自由调度比例 · 全额可动用' : 'Schedulable share · Unreserved';
    rowBRightText = <span className="ch-num text-[12.5px] font-bold text-fg">{rowBPct}%</span>;
  }

  // Runtime equivalences for bottom footer
  const g4Rate = account.gpu_entitlements?.G4?.calibrated_burn_rate || 9.66;
  const a100Rate = account.gpu_entitlements?.A100?.calibrated_burn_rate || 8.88;
  const g4Hours = schedulableCu / g4Rate;
  const a100Hours = schedulableCu / a100Rate;

  const maskedEmail = account.guardrails?.mask_email_in_dashboard
    ? account.email.replace(/^(..).*(@.*)$/, '$1***$2')
    : account.email;
  const cardTitle = account.alias || account.display_name || displayEmail(maskedEmail);
  const regionOrGroup =
    account.proxy?.region || (account.groups?.[0] ? `#${account.groups[0]}` : zh ? '直连' : 'Direct');
  const contribPrefix = account.is_contributor ? (zh ? '好友共享 · ' : 'Shared · ') : '';
  const cardSubtitle = `${contribPrefix}${tierDisplayName(account.subscription_tier, zh)} · ${displayEmail(maskedEmail)} · ${regionOrGroup}`;

  const isWorking = account.active_assignments > 0 || account.live_burn_rate_cu_hr > 0;

  // Soft rounded 36px icon badge styling
  const badgeStyle: React.CSSProperties =
    semanticTone === 'coral'
      ? {
          color: 'var(--coral-text)',
          backgroundColor: 'var(--coral-soft)',
          borderColor: 'var(--coral-border)',
        }
      : semanticTone === 'sky'
        ? {
            color: 'var(--sky-text)',
            backgroundColor: 'var(--sky-soft)',
            borderColor: 'var(--sky-border)',
          }
        : account.subscription_tier === 'AI_ULTRA'
          ? {
              color: 'var(--emerald-text)',
              backgroundColor: 'var(--emerald-soft)',
              borderColor: 'var(--emerald-border)',
            }
          : {
              color: 'var(--text-primary)',
              backgroundColor: 'var(--surface-1)',
              borderColor: 'var(--border-subtle)',
            };

  return (
    <LayeredCard
      padding="md"
      interactive
      staggerIndex={staggerIndex}
      onClick={onInspectMatrix}
      className="rounded-[20px]"
      aria-label={
        zh ? `查看 ${cardTitle} 各显卡可用时长` : `Show GPU runtime equivalence for ${cardTitle}`
      }
    >
      <div className="flex flex-col justify-between gap-4 h-full">
        {/* Top Row: 36px Icon Badge + Bold Title + Semantic Pill + Subtitle | CircularQuotaRing(46) */}
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center gap-3 min-w-0">
            <span
              className="w-9 h-9 rounded-[11px] border flex items-center justify-center shrink-0"
              style={badgeStyle}
              aria-hidden="true"
            >
              {hasExpiring || hasAuthOrProxyErr ? (
                <Flame className="w-4 h-4" />
              ) : account.is_contributor ? (
                <HeartHandshake className="w-4 h-4" />
              ) : account.subscription_tier === 'AI_ULTRA' ? (
                <Sparkles className="w-4 h-4" />
              ) : (
                <Cpu className="w-4 h-4" />
              )}
            </span>

            <div className="min-w-0">
              <div className="flex items-center gap-2 min-w-0">
                <h3 className="text-[15.5px] font-bold tracking-tight text-fg truncate">
                  {cardTitle}
                </h3>
                <StatusPill tone={semanticTone} size="xs" className="shrink-0">
                  {semanticLabel}
                </StatusPill>
              </div>
              <p className="mt-0.5 text-[12px] text-fg-muted truncate ch-num" title={cardSubtitle}>
                {cardSubtitle}
              </p>
            </div>
          </div>

          <div className="flex items-center gap-1.5 shrink-0">
            {onRefresh && (
              <IconButton
                label={zh ? '刷新这个账号的余额' : 'Refresh balance'}
                size="sm"
                onClick={(e) => {
                  e.stopPropagation();
                  onRefresh();
                }}
              >
                <RefreshCw className="w-3.5 h-3.5" />
              </IconButton>
            )}
            <CircularQuotaRing value={remainPct} size={46} strokeWidth={3.6} tone={ringTone} />
          </div>
        </div>

        {/* Middle Dual Segmented Bars (22 micro-segments each) */}
        <div className="space-y-3.5 py-0.5">
          {/* Row A: Paid Compute Pool (`剩余 633.24 / 总额 2,500 CU`) */}
          <div className="space-y-1.5">
            <div className="flex items-baseline justify-between gap-2 text-[12px]">
              <span className="text-fg-secondary truncate">
                {zh ? (
                  <>
                    付费算力池 · 剩余{' '}
                    <span className="ch-num font-semibold text-fg">
                      <AnimatedNumber value={account.paid_cu_balance} format={fmtCU} />
                    </span>{' '}
                    / 总额{' '}
                    <span className="ch-num font-medium text-fg">
                      {fmtSmartCU(cycleTotalCu)} CU
                    </span>
                  </>
                ) : (
                  <>
                    Paid pool ·{' '}
                    <span className="ch-num font-semibold text-fg">
                      <AnimatedNumber value={account.paid_cu_balance} format={fmtCU} />
                    </span>{' '}
                    /{' '}
                    <span className="ch-num font-medium text-fg">
                      {fmtSmartCU(cycleTotalCu)} CU
                    </span>
                  </>
                )}
              </span>
              <span className="ch-num text-[12.5px] font-bold text-fg shrink-0">
                {Math.round(paidPct)}%
              </span>
            </div>
            <SegmentedQuotaBar value={paidPct} segments={22} tone={paidBarTone} />
          </div>

          {/* Row B: Schedulable Ratio / Reserve Floor OR Free T4 Refresh Window */}
          <div className="space-y-1.5">
            <div className="flex items-baseline justify-between gap-2 text-[12px]">
              <span className="text-fg-secondary truncate">{rowBLabel}</span>
              <span className="shrink-0">{rowBRightText}</span>
            </div>
            <SegmentedQuotaBar
              value={rowBPct}
              segments={22}
              tone={rowBTone}
            />
          </div>
        </div>

        {/* Bottom Footer: Left Equivalent Runtime | Right Dark #18181B or Coral Capsule + Rate/Detail Pill */}
        <div className="pt-3.5 border-t border-line-faint flex flex-wrap items-center justify-between gap-2">
          <span className="text-[12px] text-fg-muted ch-num truncate">
            {zh ? (
              <>
                约可跑 <AnimatedNumber value={g4Hours} format={fmtHours} /> G4 ·{' '}
                <AnimatedNumber value={a100Hours} format={fmtHours} /> A100
              </>
            ) : (
              <>
                ~<AnimatedNumber value={g4Hours} format={fmtHours} /> G4 ·{' '}
                <AnimatedNumber value={a100Hours} format={fmtHours} /> A100
              </>
            )}
          </span>

          <div
            className="flex items-center gap-1.5 shrink-0"
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => e.stopPropagation()}
          >
            {hasExpiring ? (
              <button
                type="button"
                onClick={isWorking ? onOpenWorkbench : onOpenPool}
                className="inline-flex items-center gap-1.5 h-6 px-2.5 rounded-full text-[11.5px] font-semibold ch-num cursor-pointer border border-transparent transition-opacity hover:opacity-90"
                style={{
                  backgroundColor: 'var(--coral)',
                  color: 'var(--coral-fg)',
                  boxShadow: 'var(--shadow-coral)',
                }}
              >
                <span className="w-1.5 h-1.5 rounded-full bg-white/90 shrink-0" />
                <span>
                  {isWorking
                    ? zh
                      ? `优先消耗中 · ${formatCU(account.live_burn_rate_cu_hr, 1)}/h`
                      : `Prioritized · ${formatCU(account.live_burn_rate_cu_hr, 1)}/h`
                    : zh
                      ? '优先消耗中'
                      : 'Prioritized'}
                </span>
              </button>
            ) : isWorking ? (
              <button
                type="button"
                onClick={onOpenWorkbench}
                className="inline-flex items-center gap-1.5 h-6 px-2.5 rounded-full text-[11.5px] font-semibold ch-num cursor-pointer border border-transparent transition-opacity hover:opacity-90"
                style={{
                  backgroundColor: 'var(--accent)',
                  color: 'var(--accent-fg)',
                }}
              >
                <span className="w-1.5 h-1.5 rounded-full bg-[var(--emerald)] shrink-0" />
                <span>
                  {zh
                    ? `${account.active_assignments} 台运行中 · ${formatCU(account.live_burn_rate_cu_hr, 1)}/h`
                    : `${account.active_assignments} running · ${formatCU(account.live_burn_rate_cu_hr, 1)}/h`}
                </span>
              </button>
            ) : (
              <span className="inline-flex items-center h-6 px-2.5 rounded-full text-[11.5px] font-medium ch-num bg-surface-1 text-fg-muted border border-line-faint">
                {zh ? '空闲待命 · 0/h' : 'Idle · 0/h'}
              </span>
            )}

            <button
              type="button"
              onClick={onOpenPool}
              title={zh ? '前往账号池查看速率与显卡详情' : 'Open account pool details & rates'}
              className="inline-flex items-center gap-1 h-6 px-2.5 rounded-full text-[11.5px] font-medium bg-surface-1 border border-line text-fg-secondary hover:text-fg hover:border-line-strong transition-colors cursor-pointer"
            >
              <span>{zh ? '○ 速率' : '○ Rates'}</span>
            </button>
          </div>
        </div>
      </div>
    </LayeredCard>
  );
}

// ============================================================================
// Signature "Global Compute Compass" Card (Slot #4)
// ============================================================================

function GlobalComputeCompassCard({
  summary,
  totalAccounts,
  healthyCount,
  warningCount,
  zh,
  spanFull = false,
  staggerIndex,
  onSwitchStrategy,
  onOpenWorkbench,
}: {
  summary: QuotaSummary;
  totalAccounts: number;
  healthyCount: number;
  warningCount: number;
  zh: boolean;
  spanFull?: boolean;
  staggerIndex?: number;
  onSwitchStrategy: () => void;
  onOpenWorkbench: () => void;
}) {
  const activeTracks = Math.max(summary.active_vms, summary.accounts_working);

  return (
    <LayeredCard
      padding="md"
      staggerIndex={staggerIndex}
      className={cn('rounded-[20px]', spanFull && 'lg:col-span-2')}
    >
      <div className="flex flex-col justify-between gap-4 h-full">
        {/* Top Pills: Warm neutral pill on left + Switch Strategy pill button on right */}
        <div className="flex items-center justify-between gap-2">
          <span className="inline-flex items-center gap-1.5 h-6 px-2.5 rounded-full text-[11.5px] font-semibold bg-surface-1 border border-line text-fg-secondary select-none">
            <Sparkles className="w-3 h-3 text-[var(--emerald-text)] shrink-0" />
            <span>{zh ? '全局算力罗盘' : 'Compute Compass'}</span>
          </span>

          <ActionButton
            variant="secondary"
            size="xs"
            icon={<Settings2 />}
            onClick={onSwitchStrategy}
          >
            {zh ? '切换策略' : 'Switch Strategy'}
          </ActionButton>
        </div>

        {/* Body: OrbitalCompassGraphic(98) on left + Bold headline + Dispatch summary + Pills */}
        <div className="my-auto py-1 flex flex-col sm:flex-row items-start sm:items-center gap-5">
          <OrbitalCompassGraphic
            size={98}
            healthyCount={Math.max(1, healthyCount)}
            warningCount={warningCount}
            activeCount={activeTracks}
          />

          <div className="min-w-0 flex-1 space-y-2">
            <h3 className="text-[17px] font-bold tracking-tight text-fg">
              {totalAccounts === 0
                ? zh
                  ? '等待算力入池'
                  : 'Awaiting Compute Pool'
                : zh
                  ? '全局算力充裕'
                  : 'Global Compute Abundant'}
            </h3>

            <p className="text-[12.5px] leading-relaxed text-fg-secondary">
              {zh ? (
                <>
                  优先消耗临期批次，主力大号保持充盈兜底；自动省下{' '}
                  <span className="ch-num font-semibold text-[var(--emerald-text)]">
                    +<AnimatedNumber value={summary.saved_idle_waste_cu} format={fmtCU0} /> CU
                  </span>{' '}
                  闲置额度
                </>
              ) : (
                <>
                  Expiring batches burn first while flagship accounts stay full as backup;{' '}
                  <span className="ch-num font-semibold text-[var(--emerald-text)]">
                    +<AnimatedNumber value={summary.saved_idle_waste_cu} format={fmtCU0} /> CU
                  </span>{' '}
                  saved from idle stops.
                </>
              )}
            </p>

            <div className="pt-0.5 flex flex-wrap items-center gap-1.5">
              <StatusPill tone="emerald" size="xs" mono>
                {zh ? `${healthyCount} 轨充盈` : `${healthyCount} abundant`}
              </StatusPill>
              <StatusPill tone={warningCount > 0 ? 'coral' : 'neutral'} size="xs" mono>
                {zh ? `${warningCount} 轨预警` : `${warningCount} warning`}
              </StatusPill>
              {summary.active_vms > 0 && (
                <StatusPill tone="sky" size="xs" mono>
                  {zh ? `${summary.active_vms} 台在跑` : `${summary.active_vms} active`}
                </StatusPill>
              )}
            </div>
          </div>
        </div>

        {/* Footer: Track monitor count on left + bold status on right */}
        <div className="pt-3.5 border-t border-line-faint flex flex-wrap items-center justify-between gap-2 text-[12px]">
          <span className="text-fg-muted ch-num">
            {zh
              ? `实时监测 ${totalAccounts} 条算力轨道 · 总耗速 ${formatCU(summary.active_burn_rate_cu_hr, 1)} CU/h`
              : `Monitoring ${totalAccounts} compute tracks · ${formatCU(summary.active_burn_rate_cu_hr, 1)} CU/h`}
          </span>

          <button
            type="button"
            onClick={onOpenWorkbench}
            className="inline-flex items-center gap-1 font-bold text-fg hover:text-fg-secondary transition-colors cursor-pointer bg-transparent border-0 p-0"
          >
            <span>{zh ? '智能调度就绪' : 'Router Ready'}</span>
            <ArrowRight className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
    </LayeredCard>
  );
}

export default ObservatoryPage;
