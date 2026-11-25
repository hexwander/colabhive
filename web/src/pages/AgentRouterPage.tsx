import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowRight,
  Check,
  Copy,
  Globe,
  GripVertical,
  KeyRound,
  Loader2,
  Pin,
  RefreshCw,
  Sparkles,
  Unlink,
} from 'lucide-react';
import {
  ALL_ROUTING_STRATEGIES,
  type AcceleratorType,
  type Account,
  type RouteBinding,
  type RouterPolicy,
  type RuntimeSession,
  type ToolTraceEvent,
  type WSEvent,
} from '../types';
import { api, formatBytes, useColabHiveEvents } from '../lib/api';
import { useI18n } from '../i18n';
import {
  ActionButton,
  AnimatedNumber,
  EmptyState,
  IconButton,
  InsetWell,
  LayeredCard,
  PageHeader,
  SegmentedTabs,
  StatusPill,
  Switch,
  cn,
} from '../components/ui';
import { RouterTopologyGraph } from '../components/topology/RouterTopologyGraph';
import { TraceInspectorPanel } from '../components/topology/TraceInspectorPanel';
import {
  DEMO_ACCOUNTS,
  DEMO_ROUTER_POLICY,
  DEMO_SESSIONS,
  DEMO_STICKY_BINDINGS,
  DEMO_TRACES,
  formatElapsedSec,
  getAcceleratorCssColor,
  STRATEGY_METADATA,
} from '../components/terminal/workbenchMockData';

export interface AgentRouterPageProps {
  onOpenConnect?: () => void;
  onNavigate?: (path: string) => void;
  deploymentMode?: 'local' | 'cloud';
  publicUrl?: string;
}

const GENERAL_WATERFALL: AcceleratorType[] = ['H100', 'G4', 'A100', 'L4', 'T4'];
const LARGE_VRAM_WATERFALL: AcceleratorType[] = ['H100', 'G4'];

const SETTINGS_PATH = '/cfg';

type RouterTab = 'strategy' | 'sticky' | 'logs';

export function AgentRouterPage({
  onNavigate,
  deploymentMode = 'local',
  publicUrl,
}: AgentRouterPageProps = {}) {
  const { locale, t, displayEmail, displayProxy, displaySecret } = useI18n();
  const isZh = locale === 'zh-CN';

  const [policy, setPolicy] = useState<RouterPolicy>(DEMO_ROUTER_POLICY);
  const [bindings, setBindings] = useState<RouteBinding[]>([]);
  const [traces, setTraces] = useState<ToolTraceEvent[]>([]);
  const [sessions, setSessions] = useState<RuntimeSession[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);

  const [loading, setLoading] = useState(true);
  const [isOfflineDemo, setIsOfflineDemo] = useState(false);
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [policySavedFlash, setPolicySavedFlash] = useState(false);
  const [contributorPreemptEnabled, setContributorPreemptEnabled] = useState(true);
  const [unbindingKey, setUnbindingKey] = useState<string | null>(null);

  const [streamPaused, setStreamPaused] = useState(false);
  const [callerFilter, setCallerFilter] = useState<string | null>(null);
  const [mcpCopied, setMcpCopied] = useState(false);
  const [draggingTier, setDraggingTier] = useState<AcceleratorType | null>(null);
  const [dragDraftChain, setDragDraftChain] = useState<AcceleratorType[] | null>(null);
  const dragDraftChainRef = useRef<AcceleratorType[] | null>(null);
  const dragCommittedRef = useRef(false);

  // Progressive disclosure below the topology: one focused panel at a time
  const [activeTab, setActiveTab] = useState<RouterTab>('strategy');
  // The tab card joins the page entrance cascade once; later tab switches
  // should appear immediately rather than wait out the stagger delay.
  const [tabCardStagger, setTabCardStagger] = useState(1);
  const selectTab = useCallback((next: RouterTab) => {
    setTabCardStagger(0);
    setActiveTab(next);
  }, []);

  // Live 1s countdown tick for the pinned-environment keep-warm timers
  const [tickSec, setTickSec] = useState(0);
  useEffect(() => {
    const id = window.setInterval(() => setTickSec((s) => s + 1), 1000);
    return () => window.clearInterval(id);
  }, []);

  const activateDemoState = useCallback(() => {
    setIsOfflineDemo(true);
    setPolicy(DEMO_ROUTER_POLICY);
    setBindings(DEMO_STICKY_BINDINGS);
    setTraces(DEMO_TRACES);
    setSessions(DEMO_SESSIONS);
    setAccounts(DEMO_ACCOUNTS);
  }, []);

  const loadRouterState = useCallback(async () => {
    try {
      const [pol, stickyList, traceList, sessList, accList] = await Promise.all([
        api.getRouterPolicy(),
        api.listStickyBindings(),
        api.listTraces(100),
        api.listSessions().catch(() => [] as RuntimeSession[]),
        api.listAccounts().catch(() => [] as Account[]),
      ]);

      if (pol) setPolicy(pol);
      setBindings(Array.isArray(stickyList) ? stickyList : []);
      setTraces(Array.isArray(traceList) ? traceList : []);
      setSessions(Array.isArray(sessList) ? sessList : []);
      setAccounts(Array.isArray(accList) ? accList : []);
      setIsOfflineDemo(false);
      setTickSec(0);
    } catch {
      activateDemoState();
    } finally {
      setLoading(false);
    }
  }, [activateDemoState]);

  useEffect(() => {
    void loadRouterState();
  }, [loadRouterState]);

  // Policy changes made elsewhere (e.g. Free-First toggle) show up right away
  useEffect(() => {
    const handlePolicyChanged = () => {
      api
        .getRouterPolicy()
        .then((pol) => {
          if (pol) setPolicy(pol);
        })
        .catch(() => {});
    };
    window.addEventListener('colabhive:router-policy-changed', handlePolicyChanged);
    return () => {
      window.removeEventListener('colabhive:router-policy-changed', handlePolicyChanged);
    };
  }, []);

  // Real-time WebSocket stream for `trace_event`, `session_updated`, `account_updated`
  const handleWSEvent = useCallback(
    (evt: WSEvent) => {
      if (!evt || !evt.type) return;

      if (evt.type === 'trace_event' && evt.payload && !streamPaused) {
        const incoming = evt.payload as ToolTraceEvent;
        setTraces((prev) => [incoming, ...prev.filter((x) => x.id !== incoming.id)].slice(0, 150));
        return;
      }

      if (evt.type === 'session_updated' && evt.payload) {
        const updated = evt.payload as RuntimeSession;
        setSessions((prev) => {
          const idx = prev.findIndex((s) => s.id === updated.id);
          if (idx === -1) return [updated, ...prev];
          const copy = [...prev];
          copy[idx] = updated;
          return copy;
        });
        return;
      }

      if (evt.type === 'account_updated' && evt.payload) {
        const updated = evt.payload as Account;
        setAccounts((prev) => {
          const idx = prev.findIndex((a) => a.id === updated.id);
          if (idx === -1) return [updated, ...prev];
          const copy = [...prev];
          copy[idx] = updated;
          return copy;
        });
      }
    },
    [streamPaused],
  );

  const wsState = useColabHiveEvents(handleWSEvent, !isOfflineDemo);

  const policySavedTimerRef = useRef<number | null>(null);
  useEffect(
    () => () => {
      if (policySavedTimerRef.current !== null) {
        window.clearTimeout(policySavedTimerRef.current);
      }
    },
    [],
  );

  const persistPolicy = async (next: RouterPolicy) => {
    setPolicy(next);
    setSavingPolicy(true);
    try {
      if (!isOfflineDemo) {
        const saved = await api.updateRouterPolicy(next);
        if (saved) setPolicy(saved);
      } else {
        await new Promise((r) => window.setTimeout(r, 180));
      }
      setPolicySavedFlash(true);
      if (policySavedTimerRef.current !== null) {
        window.clearTimeout(policySavedTimerRef.current);
      }
      policySavedTimerRef.current = window.setTimeout(() => {
        setPolicySavedFlash(false);
        policySavedTimerRef.current = null;
      }, 1800);
      if (typeof window !== 'undefined') {
        window.dispatchEvent(
          new CustomEvent('colabhive:router-policy-changed', {
            detail: { strategy: next.default_strategy },
          }),
        );
      }
    } catch {
      // Keep optimistic UI update if transient failure
    } finally {
      setSavingPolicy(false);
    }
  };

  const moveFallbackTier = (index: number, direction: -1 | 1) => {
    const chain = [...(policy.fallback_chain || GENERAL_WATERFALL)];
    const target = index + direction;
    if (target < 0 || target >= chain.length) return;
    const tmp = chain[index];
    chain[index] = chain[target];
    chain[target] = tmp;
    void persistPolicy({ ...policy, fallback_chain: chain });
  };

  const handleTierDragStart = (e: React.DragEvent<HTMLDivElement>, tier: AcceleratorType) => {
    if (!policy.auto_fallback_enabled) {
      e.preventDefault();
      return;
    }
    const base = [...(policy.fallback_chain || GENERAL_WATERFALL)];
    dragCommittedRef.current = false;
    dragDraftChainRef.current = base;
    setDraggingTier(tier);
    setDragDraftChain(base);
    e.dataTransfer.effectAllowed = 'move';
    try {
      e.dataTransfer.setData('text/plain', tier);
    } catch {
      // Ignore WebView restrictions on dataTransfer payloads
    }
  };

  const handleTierDragOver = (e: React.DragEvent<HTMLDivElement>, targetTier: AcceleratorType) => {
    if (!draggingTier || !policy.auto_fallback_enabled) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    if (draggingTier === targetTier) return;
    const current = dragDraftChainRef.current || [...(policy.fallback_chain || GENERAL_WATERFALL)];
    const fromIdx = current.indexOf(draggingTier);
    const toIdx = current.indexOf(targetTier);
    if (fromIdx < 0 || toIdx < 0 || fromIdx === toIdx) return;
    const next = [...current];
    const [moved] = next.splice(fromIdx, 1);
    next.splice(toIdx, 0, moved);
    dragDraftChainRef.current = next;
    setDragDraftChain(next);
  };

  const finishTierDrag = (commit: boolean) => {
    const finalChain = dragDraftChainRef.current;
    const origChain = policy.fallback_chain || GENERAL_WATERFALL;
    setDraggingTier(null);
    setDragDraftChain(null);
    dragDraftChainRef.current = null;
    if (!commit || dragCommittedRef.current || !finalChain) return;
    dragCommittedRef.current = true;
    const changed =
      finalChain.length !== origChain.length ||
      finalChain.some((tier, i) => tier !== origChain[i]);
    if (changed) {
      void persistPolicy({ ...policy, fallback_chain: finalChain });
    }
  };

  const handleUnbindSticky = async (stickyKey: string) => {
    setUnbindingKey(stickyKey);
    try {
      if (!isOfflineDemo) {
        await api.unbindStickySession(stickyKey);
      } else {
        await new Promise((r) => window.setTimeout(r, 220));
      }
      setBindings((prev) => prev.filter((b) => b.sticky_key !== stickyKey));
    } finally {
      setUnbindingKey(null);
    }
  };

  const isCloudMode = deploymentMode === 'cloud';

  const mcpEndpointUrl = useMemo(() => {
    const base = (
      publicUrl ||
      (typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860')
    ).replace(/\/+$/, '');
    return `${base}/mcp`;
  }, [publicUrl]);

  const copyMcpEndpoint = async () => {
    try {
      await navigator.clipboard.writeText(mcpEndpointUrl);
      setMcpCopied(true);
      window.setTimeout(() => setMcpCopied(false), 1600);
    } catch {
      // Ignore
    }
  };

  // Map sessionId -> session (for the cached-weights column)
  const sessionById = useMemo(() => {
    const m = new Map<string, RuntimeSession>();
    sessions.forEach((s) => m.set(s.id, s));
    return m;
  }, [sessions]);

  const fallbackSavesCount = useMemo(
    () => traces.filter((tr) => tr.status === 'fallback').length,
    [traces],
  );

  // Manual refresh feedback for the header control
  const [refreshing, setRefreshing] = useState(false);
  const handleRefresh = async () => {
    setRefreshing(true);
    try {
      await loadRouterState();
    } finally {
      setRefreshing(false);
    }
  };

  const fallbackChain = policy.fallback_chain || GENERAL_WATERFALL;
  const strictChainActive =
    fallbackChain.length === 2 &&
    fallbackChain.includes('H100') &&
    fallbackChain.includes('G4');
  const generalChainActive = !strictChainActive && policy.auto_fallback_enabled;
  const keepWarmMinutes = Math.round(policy.sticky_idle_ttl_sec / 60);

  const failoverLabel = isZh ? '自动换卡防报错' : 'Auto-switch GPU when out of stock';
  const reserveLabel = isZh ? '好友共享保底保护' : 'Friend Share Reserve Protection';
  const keepWarmLabel = isZh ? '会话环境保持时长' : 'Session Environment Keep-Warm';
  const hungCleanupLabel = isZh ? '卡死任务自动清理' : 'Auto-Release Hung Tasks';

  return (
    <div className="space-y-6 ch-reveal">
      <PageHeader
        title={t('router.title')}
        subtitle={
          isZh
            ? '把多个 Colab 账号汇成智能算力池，为你的 AI 编程助手自动选号、保留模型缓存、遇到缺卡自动换卡'
            : 'Connect your AI coding assistants in one click with smart account routing, warm model caches, and automatic GPU failover.'
        }
        badge={
          <>
            <StatusPill
              size="sm"
              dot
              pulse={wsState.connected && !isOfflineDemo}
              mono
              tone={isOfflineDemo ? 'amber' : wsState.connected ? 'emerald' : 'neutral'}
            >
              {isOfflineDemo
                ? isZh
                  ? '演示快照'
                  : 'Preview Snapshot'
                : wsState.connected
                  ? `${isZh ? '实时' : 'Live'} · ${wsState.latencyMs ?? 11}ms`
                  : isZh
                    ? '已同步'
                    : 'Synced'}
            </StatusPill>
            {isCloudMode && (
              <StatusPill size="sm" tone="sky" icon={<Globe />}>
                {isZh ? '云端远程网关 · Cloud MCP' : 'Cloud Remote MCP'}
              </StatusPill>
            )}
            {policySavedFlash && (
              <StatusPill size="sm" tone="emerald" icon={<Check />}>
                {isZh ? '已保存' : 'Saved'}
              </StatusPill>
            )}
          </>
        }
        actions={
          <>
            <button
              type="button"
              onClick={() => void copyMcpEndpoint()}
              title={
                isZh
                  ? `复制 MCP 接入地址：${mcpEndpointUrl}`
                  : `Copy the MCP endpoint URL: ${mcpEndpointUrl}`
              }
              className="h-8 px-3.5 rounded-full border border-line bg-surface-2 hover:bg-surface-1 hover:border-line-strong text-[12px] ch-num inline-flex items-center gap-1.5 transition-colors cursor-pointer shadow-[var(--shadow-control)]"
            >
              <span className="text-fg-muted">MCP</span>
              <span className="text-fg font-semibold">/mcp</span>
              {mcpCopied ? (
                <Check className="w-3 h-3" style={{ color: 'var(--emerald)' }} />
              ) : (
                <Copy className="w-3 h-3 text-fg-muted" />
              )}
            </button>

            <a
              href={SETTINGS_PATH}
              onClick={(e) => {
                if (onNavigate) {
                  e.preventDefault();
                  onNavigate(SETTINGS_PATH);
                }
              }}
              className="h-8 px-3 rounded-full border border-line bg-surface-2 hover:bg-surface-1 hover:border-line-strong text-[12px] font-medium text-fg-secondary hover:text-fg inline-flex items-center gap-1.5 transition-colors no-underline shadow-[var(--shadow-control)]"
              title={
                isZh
                  ? '管理 AI 工具使用的访问密钥与网络代理'
                  : 'Manage access keys and network proxies'
              }
            >
              <KeyRound className="w-3.5 h-3.5 shrink-0" />
              <span>{isZh ? '密钥与代理' : 'Keys & Proxies'}</span>
            </a>

            <IconButton
              label={isZh ? '刷新' : 'Refresh'}
              onClick={() => void handleRefresh()}
              disabled={refreshing}
            >
              <RefreshCw className={cn('w-4 h-4', (loading || refreshing) && 'animate-spin')} />
            </IconButton>

            {!isOfflineDemo && bindings.length === 0 && traces.length === 0 && (
              <ActionButton
                variant="ghost"
                size="md"
                icon={<Sparkles />}
                onClick={activateDemoState}
              >
                {isZh ? '预览演示' : 'Preview Demo'}
              </ActionButton>
            )}
          </>
        }
      />

      {/* Layer 1 — live routing topology */}
      <RouterTopologyGraph
        staggerIndex={0}
        policy={policy}
        bindings={bindings}
        sessions={sessions}
        accounts={accounts}
        traces={traces}
        selectedCallerFilter={callerFilter}
        onSelectCallerFilter={(caller) => {
          setCallerFilter(caller);
          // Selecting an AI tool filters the call log, so bring it into view
          if (caller) selectTab('logs');
        }}
      />

      {/* Layer 2 — progressive disclosure */}
      <div className="space-y-4">
        <SegmentedTabs<RouterTab>
          aria-label={isZh ? '选号规则视图' : 'Routing view'}
          value={activeTab}
          onChange={selectTab}
          options={[
            { value: 'strategy', label: isZh ? '智能选号与防报错' : 'Smart Routing & Failover' },
            {
              value: 'sticky',
              label: isZh ? '固定环境会话' : 'Pinned Environments',
              count: bindings.length,
            },
            {
              value: 'logs',
              label: isZh ? 'AI 调用记录' : 'AI Call Log',
              count: traces.length,
            },
          ]}
        />

        {/* Tab 1 — Routing strategy */}
        {activeTab === 'strategy' && (
          <LayeredCard padding="md" staggerIndex={tabCardStagger}>
            <div className="space-y-5">
              <div>
                <div className="mb-2.5 flex items-center justify-between gap-2">
                  <span
                    id="router-strategy-label"
                    className="text-[12.5px] font-medium text-fg-secondary"
                  >
                    {isZh ? '默认选号方式' : 'Default routing strategy'}
                  </span>
                  {savingPolicy && (
                    <span className="inline-flex items-center gap-1.5 text-[11.5px] text-fg-muted">
                      <Loader2 className="w-3 h-3 animate-spin" />
                      {isZh ? '保存中…' : 'Saving…'}
                    </span>
                  )}
                </div>

                <div
                  role="radiogroup"
                  aria-labelledby="router-strategy-label"
                  className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3 ui-stagger"
                >
                  {ALL_ROUTING_STRATEGIES.map((strat) => {
                    const meta = STRATEGY_METADATA[strat];
                    const effectiveStrategy =
                      policy.default_strategy === 'free-opportunistic'
                        ? 'free-first'
                        : policy.default_strategy;
                    const selected = effectiveStrategy === strat;
                    const isFreeFirst = strat === 'free-first';
                    const isRecommended = isFreeFirst || strat === 'waste-zero';
                    const [mainTitle, ...taglineParts] = (
                      isZh ? meta.zhTitle : meta.enTitle
                    ).split(' · ');
                    const tagline = taglineParts.join(' · ');
                    return (
                      <button
                        key={strat}
                        type="button"
                        role="radio"
                        aria-checked={selected}
                        onClick={() => void persistPolicy({ ...policy, default_strategy: strat })}
                        className={cn(
                          'rounded-2xl p-4 text-left cursor-pointer transition-all hover:-translate-y-[1px]',
                          selected && isFreeFirst
                            ? 'bg-surface-2 border-[1.5px] border-[var(--emerald)] shadow-[var(--shadow-card)]'
                            : selected
                              ? 'bg-surface-2 border-[1.5px] border-[var(--accent)] shadow-[var(--shadow-card)]'
                              : isFreeFirst
                                ? 'bg-[var(--emerald-soft)]/45 border border-[var(--emerald-border)] hover:bg-surface-2 hover:border-[var(--emerald)]'
                                : 'bg-surface-1/75 border border-line-faint hover:bg-surface-2 hover:border-line-strong',
                        )}
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div className="flex items-start gap-2.5 min-w-0">
                            <span
                              aria-hidden="true"
                              className={cn(
                                'mt-[2px] flex items-center justify-center w-4 h-4 shrink-0 rounded-full border transition-colors',
                                selected && isFreeFirst
                                  ? 'bg-[var(--emerald)] border-[var(--emerald)]'
                                  : selected
                                    ? 'bg-[var(--accent)] border-[var(--accent)]'
                                    : 'bg-surface-2 border-line-strong',
                              )}
                            >
                              {selected && (
                                <Check
                                  className="w-2.5 h-2.5 stroke-[3]"
                                  style={{ color: isFreeFirst ? '#FFFFFF' : 'var(--accent-fg)' }}
                                />
                              )}
                            </span>
                            <span className="min-w-0">
                              <span className="block truncate text-[13px] font-semibold text-fg">
                                {mainTitle}
                              </span>
                              {tagline && (
                                <span className="mt-0.5 block truncate text-[11.5px] font-medium text-fg-secondary">
                                  {tagline}
                                </span>
                              )}
                            </span>
                          </div>
                          <StatusPill
                            size="xs"
                            tone={
                              isFreeFirst
                                ? 'emerald'
                                : isRecommended
                                  ? 'sky'
                                  : selected
                                    ? 'accent'
                                    : 'neutral'
                            }
                            className="shrink-0"
                          >
                            {isZh ? meta.zhBadge : meta.enBadge}
                          </StatusPill>
                        </div>
                        <p className="mt-2 m-0 pl-[26px] text-[12px] leading-relaxed text-fg-secondary line-clamp-2">
                          {isZh ? meta.zhDesc : meta.enDesc}
                        </p>
                      </button>
                    );
                  })}
                </div>
              </div>

              {/* Well 1 — Automatic GPU failover */}
              <InsetWell padding="md" className="space-y-3.5">
                <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
                  <div className="min-w-0">
                    <div className="text-[13px] font-semibold text-fg">
                      {isZh
                        ? '自动换卡防报错（H100 缺卡自动切 G4/A100）'
                        : 'Auto-Switch GPU When Out of Stock (H100 → G4/A100)'}
                    </div>
                    <div className="mt-0.5 text-[12px] leading-relaxed text-fg-secondary">
                      {isZh ? (
                        <>
                          遇到目标显卡缺货或套餐不支持时，按下方顺序自动换用下一档可用显卡 · 已自动挽救{' '}
                          <AnimatedNumber value={fallbackSavesCount} className="font-medium text-fg" />{' '}
                          次任务
                        </>
                      ) : (
                        <>
                          Automatically switches to the next GPU in line on stockouts ·{' '}
                          <AnimatedNumber value={fallbackSavesCount} className="font-medium text-fg" />{' '}
                          requests saved
                        </>
                      )}
                    </div>
                  </div>

                  <div className="flex flex-wrap items-center gap-2 shrink-0">
                    <ActionButton
                      size="xs"
                      variant={generalChainActive ? 'primary' : 'secondary'}
                      aria-pressed={generalChainActive}
                      title={
                        isZh
                          ? '按 H100 → G4 → A100 → L4 → T4 的顺序依次尝试，最大化成功率'
                          : 'Tries H100 → G4 → A100 → L4 → T4 in order for the highest success rate'
                      }
                      onClick={() =>
                        void persistPolicy({
                          ...policy,
                          auto_fallback_enabled: true,
                          fallback_chain: GENERAL_WATERFALL,
                        })
                      }
                    >
                      {isZh ? '通用顺延 (H100→G4→A100→L4→T4)' : 'All GPUs (H100→G4→A100→L4→T4)'}
                    </ActionButton>
                    <ActionButton
                      size="xs"
                      variant={strictChainActive && policy.auto_fallback_enabled ? 'primary' : 'secondary'}
                      aria-pressed={strictChainActive && policy.auto_fallback_enabled}
                      title={
                        strictChainActive
                          ? isZh
                            ? '再次点击可取消大显存保护，恢复 5 档通用顺延'
                            : 'Click again to turn off ≥80GB protection and restore all 5 GPU tiers'
                          : isZh
                            ? '大模型保护：只在 H100 (80GB) 与 G4 (96GB) 之间换卡，绝不降到 40GB 显卡导致爆显存'
                            : 'Large-model protection: only switches between H100 (80GB) and G4 (96GB); never drops to a 40GB GPU'
                      }
                      onClick={() =>
                        void persistPolicy({
                          ...policy,
                          auto_fallback_enabled: true,
                          fallback_chain: strictChainActive
                            ? GENERAL_WATERFALL
                            : LARGE_VRAM_WATERFALL,
                        })
                      }
                    >
                      {isZh ? '大显存保护 ≥80GB (仅 H100↔G4)' : '≥80GB VRAM Only (H100↔G4)'}
                    </ActionButton>
                    <Switch
                      checked={policy.auto_fallback_enabled}
                      label={failoverLabel}
                      onChange={(next) =>
                        void persistPolicy({ ...policy, auto_fallback_enabled: next })
                      }
                    />
                  </div>
                </div>

                {/* Reorderable GPU order (drag-and-drop + keyboard Left/Right) */}
                <div
                  className={cn(
                    'flex flex-wrap items-center gap-1.5 transition-opacity',
                    !policy.auto_fallback_enabled && 'opacity-50',
                  )}
                  onDragOver={(e) => {
                    if (draggingTier) {
                      e.preventDefault();
                      e.dataTransfer.dropEffect = 'move';
                    }
                  }}
                  onDrop={(e) => {
                    e.preventDefault();
                    finishTierDrag(true);
                  }}
                >
                  {(dragDraftChain || fallbackChain).map((tier, idx, arr) => {
                    const isDragging = draggingTier === tier;
                    return (
                      <React.Fragment key={tier}>
                        <div
                          draggable={policy.auto_fallback_enabled}
                          tabIndex={policy.auto_fallback_enabled ? 0 : -1}
                          role="listitem"
                          aria-label={
                            isZh
                              ? `${tier}（拖动调整优先级，或用左右方向键移动）`
                              : `${tier} (drag or use Left/Right arrows to reorder)`
                          }
                          title={
                            isZh
                              ? '按住拖动调整换卡顺序（也支持键盘 ← / → 键）'
                              : 'Drag to reorder fallback priority (or press ← / →)'
                          }
                          onDragStart={(e) => handleTierDragStart(e, tier)}
                          onDragOver={(e) => handleTierDragOver(e, tier)}
                          onDrop={(e) => {
                            e.preventDefault();
                            e.stopPropagation();
                            finishTierDrag(true);
                          }}
                          onDragEnd={() => finishTierDrag(true)}
                          onKeyDown={(e) => {
                            if (!policy.auto_fallback_enabled) return;
                            if (e.key === 'ArrowLeft') {
                              e.preventDefault();
                              moveFallbackTier(idx, -1);
                            } else if (e.key === 'ArrowRight') {
                              e.preventDefault();
                              moveFallbackTier(idx, 1);
                            }
                          }}
                          className={cn(
                            'inline-flex items-center gap-1.5 h-7 pl-1.5 pr-2.5 rounded-lg border select-none transition-all duration-150 focus:outline-none focus:ring-2 focus:ring-line-strong',
                            policy.auto_fallback_enabled
                              ? 'cursor-grab active:cursor-grabbing hover:border-line-strong hover:bg-surface-1'
                              : 'cursor-not-allowed',
                            isDragging
                              ? 'border-[var(--brand-bg)] bg-surface-1 opacity-60 scale-[1.03] shadow-md'
                              : 'border-line bg-surface-2 shadow-[var(--shadow-control)]',
                          )}
                        >
                          <GripVertical
                            className="w-3 h-3 text-fg-muted/70 shrink-0"
                            aria-hidden="true"
                          />
                          <span
                            className="w-1.5 h-1.5 rounded-full shrink-0"
                            style={{ backgroundColor: getAcceleratorCssColor(tier) }}
                          />
                          <span className="ch-num text-[12px] font-medium text-fg">{tier}</span>
                        </div>
                        {idx < arr.length - 1 && (
                          <ArrowRight
                            className="w-3 h-3 shrink-0 text-fg-muted"
                            aria-hidden="true"
                          />
                        )}
                      </React.Fragment>
                    );
                  })}

                  {!strictChainActive && policy.auto_fallback_enabled && (
                    <span className="ml-1 text-[11px] text-fg-muted">
                      {isZh ? '按住拖动可调整顺序' : 'Drag chips to reorder'}
                    </span>
                  )}

                  {strictChainActive && (
                    <div className="inline-flex items-center gap-1.5 ml-1">
                      <StatusPill tone="amber" size="sm">
                        {isZh
                          ? '大显存保护：绝不降到 40GB 以下防爆显存'
                          : '≥80GB VRAM Protected (Prevents OOM)'}
                      </StatusPill>
                      <button
                        type="button"
                        onClick={() =>
                          void persistPolicy({
                            ...policy,
                            auto_fallback_enabled: true,
                            fallback_chain: GENERAL_WATERFALL,
                          })
                        }
                        className="text-[11.5px] font-medium text-fg-muted hover:text-fg underline underline-offset-2 cursor-pointer px-1"
                      >
                        {isZh ? '恢复 5 档' : 'Restore all 5'}
                      </button>
                    </div>
                  )}
                </div>
              </InsetWell>

              {/* Well 2 — Reserve protection & session keep-warm */}
              <InsetWell padding="md">
                <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 lg:gap-0 lg:divide-x lg:divide-line-faint">
                  <div className="lg:pr-5">
                    <SettingRow
                      title={reserveLabel}
                      note={
                        isZh
                          ? '在触及好友设定的保底额度前 5 分钟平滑迁移任务，并全程隔离 Google 云盘与登录凭据。'
                          : "Smoothly migrates jobs 5 minutes before reaching a friend's safety reserve floor, keeping Drive & credential isolation active."
                      }
                    >
                      <Switch
                        checked={contributorPreemptEnabled}
                        label={reserveLabel}
                        onChange={setContributorPreemptEnabled}
                      />
                    </SettingRow>
                  </div>

                  <div className="space-y-4 lg:pl-5">
                    <SettingRow
                      title={
                        <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
                          <span>{keepWarmLabel}</span>
                          <StatusPill
                            size="xs"
                            tone={policy.sticky_session_enabled ? 'emerald' : 'neutral'}
                          >
                            {policy.sticky_session_enabled
                              ? isZh
                                ? `会话环境保持 ${keepWarmMinutes} 分钟`
                                : `Keep warm ${keepWarmMinutes}m`
                              : isZh
                                ? '已关闭'
                                : 'Off'}
                          </StatusPill>
                        </span>
                      }
                      note={
                        isZh
                          ? 'AI 助手暂停操作期间，继续保留云端环境与已下载权重的时长；超时空闲自动释放显卡省额度。'
                          : 'How long the cloud environment and downloaded weights stay ready while your AI assistant is idle; the GPU is released afterwards to save quota.'
                      }
                    >
                      <select
                        value={policy.sticky_idle_ttl_sec}
                        disabled={!policy.sticky_session_enabled}
                        aria-label={keepWarmLabel}
                        onChange={(e) =>
                          void persistPolicy({
                            ...policy,
                            sticky_idle_ttl_sec: parseInt(e.target.value, 10),
                          })
                        }
                        className="ui-input w-[6.5rem] ch-num cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        {[15, 20, 30, 60].map((minutes) => (
                          <option
                            key={minutes}
                            value={minutes * 60}
                            className="bg-surface-2 text-fg"
                          >
                            {isZh ? `${minutes} 分钟` : `${minutes} min`}
                          </option>
                        ))}
                      </select>
                      <Switch
                        checked={policy.sticky_session_enabled}
                        label={keepWarmLabel}
                        onChange={(next) =>
                          void persistPolicy({ ...policy, sticky_session_enabled: next })
                        }
                      />
                    </SettingRow>

                    <div className="border-t border-line-faint" />

                    <SettingRow
                      title={hungCleanupLabel}
                      note={
                        isZh
                          ? '检测到后台 Python 进程卡死无响应时自动释放显卡，防止空转白扣额度。'
                          : 'Releases the GPU automatically when the background Python process hangs, so nothing burns quota while stuck.'
                      }
                    >
                      <Switch
                        checked={policy.auto_release_on_zombie}
                        label={hungCleanupLabel}
                        onChange={(next) =>
                          void persistPolicy({ ...policy, auto_release_on_zombie: next })
                        }
                      />
                    </SettingRow>
                  </div>
                </div>
              </InsetWell>
            </div>
          </LayeredCard>
        )}

        {/* Tab 2 — Pinned environments */}
        {activeTab === 'sticky' && (
          <LayeredCard
            padding="none"
            staggerIndex={tabCardStagger}
            icon={<Pin />}
            title={t('router.sticky_table')}
            headerRight={
              <StatusPill size="sm" tone={bindings.length > 0 ? 'emerald' : 'neutral'} dot>
                <AnimatedNumber value={bindings.length} />
                {isZh ? ' 个固定中' : ' pinned'}
              </StatusPill>
            }
          >
            {bindings.length === 0 ? (
              <EmptyState
                icon={<Pin />}
                title={isZh ? '当前没有固定环境的会话' : 'No pinned environments'}
                description={
                  isZh
                    ? 'AI 助手第一次运行代码时，会自动固定到一台已就绪的云端实例，后续调用直接复用已下载的模型权重。'
                    : 'The first time an AI assistant runs code, it is pinned to a ready cloud instance so later calls reuse the downloaded weights.'
                }
              />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full border-collapse text-left text-[12.5px]">
                  <thead>
                    <tr className="border-b border-line-faint text-[11.5px] text-fg-muted">
                      <th className="py-2.5 pl-5 pr-3 font-medium">
                        {isZh ? 'AI 工具与会话标识' : 'AI Tool & Session ID'}
                      </th>
                      <th className="py-2.5 px-3 font-medium">
                        {isZh ? '固定云端实例' : 'Pinned Cloud Instance'}
                      </th>
                      <th className="py-2.5 px-3 font-medium">
                        {isZh ? '所属账号与网络' : 'Account & Network'}
                      </th>
                      <th className="py-2.5 px-3 font-medium">
                        {isZh ? '显卡与权重缓存' : 'GPU & Cached Weights'}
                      </th>
                      <th className="py-2.5 px-3 font-medium">
                        {isZh ? '环境保留倒计时' : 'Keep-Warm Countdown'}
                      </th>
                      <th className="py-2.5 pl-3 pr-5 font-medium text-right">
                        <span className="sr-only">{isZh ? '操作' : 'Action'}</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line-faint">
                    {bindings.map((b) => {
                      const linkedSession = sessionById.get(b.session_id);
                      const hfBytes = linkedSession?.resources?.hf_cache_bytes;
                      const hasHfCache = Boolean(hfBytes && hfBytes > 0);
                      const remainingSec = Math.max(0, b.idle_ttl_remaining_sec - tickSec);
                      const sessionPath = `/exp/${encodeURIComponent(b.session_id)}`;

                      return (
                        <tr key={b.sticky_key} className="transition-colors hover:bg-[var(--hover-fill)]">
                          <td className="py-3 pl-5 pr-3">
                            <div className="font-medium text-fg">{b.client_name}</div>
                            <div className="font-mono ch-num mt-0.5 text-[11.5px] text-accent-text">
                              {displaySecret(b.sticky_key)}
                            </div>
                          </td>

                          <td className="py-3 px-3">
                            <a
                              href={sessionPath}
                              onClick={(e) => {
                                if (onNavigate) {
                                  e.preventDefault();
                                  onNavigate(sessionPath);
                                }
                              }}
                              title={
                                isZh
                                  ? '打开该实例的实时日志与显卡负载'
                                  : 'Open live logs and GPU load for this instance'
                              }
                              className="ch-num font-medium text-fg no-underline underline-offset-2 hover:underline hover:text-accent-text"
                            >
                              {b.session_name}
                            </a>
                            <div className="ch-num mt-0.5 text-[11px] text-fg-muted">
                              {isZh ? '实例 ' : 'instance '}
                              {displaySecret(b.kernel_id || b.session_id)}
                            </div>
                          </td>

                          <td className="py-3 px-3">
                            <div className="ch-num text-fg-secondary">
                              {displayEmail(b.account_email)}
                            </div>
                            {b.proxy_summary && (
                              <div className="ch-num mt-0.5 text-[11px] text-fg-muted">
                                {displayProxy(b.proxy_summary).replace(/%2A/gi, '*')}
                              </div>
                            )}
                          </td>

                          <td className="py-3 px-3">
                            <div className="flex flex-wrap items-center gap-1.5">
                              <StatusPill size="xs" mono>
                                {b.accelerator}
                              </StatusPill>
                              <StatusPill
                                size="xs"
                                tone={hasHfCache ? 'emerald' : 'neutral'}
                                title={
                                  isZh
                                    ? '该实例上已下载、可直接复用的模型权重'
                                    : 'Model weights already downloaded on this instance'
                                }
                              >
                                {hasHfCache
                                  ? isZh
                                    ? `已缓存权重 ${formatBytes(hfBytes)}`
                                    : `Cached ${formatBytes(hfBytes)}`
                                  : isZh
                                    ? '环境已就绪'
                                    : 'Environment ready'}
                              </StatusPill>
                            </div>
                          </td>

                          <td className="py-3 px-3 ch-num whitespace-nowrap">
                            <div className="font-medium text-fg">
                              {formatElapsedSec(remainingSec)}
                            </div>
                            <div className="mt-0.5 text-[11px] text-fg-muted">
                              <AnimatedNumber value={b.active_calls} />{' '}
                              {isZh ? '个任务执行中' : 'running'}
                            </div>
                          </td>

                          <td className="py-3 pl-3 pr-5 text-right">
                            <ActionButton
                              variant="danger"
                              size="xs"
                              icon={<Unlink />}
                              loading={unbindingKey === b.sticky_key}
                              title={
                                isZh
                                  ? '解除固定：该 AI 工具下次调用会重新选号分配实例'
                                  : 'Unpin: the next call from this tool is routed afresh'
                              }
                              onClick={() => void handleUnbindSticky(b.sticky_key)}
                            >
                              {isZh ? '释放绑定' : 'Unpin'}
                            </ActionButton>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </LayeredCard>
        )}

        {/* Tab 3 — AI call log */}
        {activeTab === 'logs' && (
          <TraceInspectorPanel
            staggerIndex={tabCardStagger}
            traces={traces}
            paused={streamPaused}
            onTogglePaused={() => setStreamPaused((p) => !p)}
            callerFilter={callerFilter}
            onClearCallerFilter={() => setCallerFilter(null)}
          />
        )}
      </div>
    </div>
  );
}

function SettingRow({
  title,
  note,
  children,
}: {
  title: React.ReactNode;
  note: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <div className="text-[12.5px] font-medium text-fg">{title}</div>
        <div className="mt-0.5 text-[12px] leading-relaxed text-fg-secondary">{note}</div>
      </div>
      <div className="flex items-center gap-2.5 shrink-0">{children}</div>
    </div>
  );
}

export default AgentRouterPage;
