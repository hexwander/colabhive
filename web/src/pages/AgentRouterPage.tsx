import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ArrowRight,
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  Globe,
  KeyRound,
  Loader2,
  Pin,
  RefreshCw,
  Sparkles,
  Unlink,
  Zap,
} from 'lucide-react';
import {
  ALL_ROUTING_STRATEGIES,
  type AcceleratorType,
  type Account,
  type AgentClientConfigStatus,
  type AgentClientID,
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
import { buildFallbackClientConfigs } from '../components/layout/telemetryUi';
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

const AGENT_CONNECTED_EVENT = 'colabhive:agent-connected';
const SETTINGS_PATH = '/cfg';

type RouterTab = 'strategy' | 'sticky' | 'logs';

/** The six flagship tools surfaced in the Quick Connect bar. */
interface QuickConnectTool {
  key: string;
  /** Matching MCP client; omitted for the bundled `colab` CLI. */
  clientId?: AgentClientID;
  label: string;
  monogram: string;
}

const QUICK_CONNECT_TOOLS: QuickConnectTool[] = [
  { key: 'claude-code', clientId: 'claude-code', label: 'Claude Code', monogram: 'CC' },
  { key: 'antigravity', clientId: 'antigravity', label: 'Antigravity', monogram: 'AG' },
  { key: 'cursor', clientId: 'cursor', label: 'Cursor', monogram: 'CU' },
  { key: 'codex', clientId: 'codex', label: 'Codex', monogram: 'CX' },
  { key: 'gemini-cli', clientId: 'gemini-cli', label: 'Gemini CLI', monogram: 'GM' },
  { key: 'colab-cli', label: 'colab CLI', monogram: '>_' },
];

/** Offline snapshot of the client list (same shape the local service returns). */
function fallbackClientStatuses(): AgentClientConfigStatus[] {
  return buildFallbackClientConfigs(
    typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860',
    '',
    'http',
  );
}

export function AgentRouterPage({
  onOpenConnect,
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
  const [clientStatuses, setClientStatuses] = useState<AgentClientConfigStatus[]>([]);

  const [loading, setLoading] = useState(true);
  const [isOfflineDemo, setIsOfflineDemo] = useState(false);
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [policySavedFlash, setPolicySavedFlash] = useState(false);
  const [contributorPreemptEnabled, setContributorPreemptEnabled] = useState(true);
  const [unbindingKey, setUnbindingKey] = useState<string | null>(null);

  const [streamPaused, setStreamPaused] = useState(false);
  const [callerFilter, setCallerFilter] = useState<string | null>(null);
  const [mcpCopied, setMcpCopied] = useState(false);

  // Progressive disclosure below the topology: one focused panel at a time
  const [activeTab, setActiveTab] = useState<RouterTab>('strategy');
  // The tab card joins the page entrance cascade once; later tab switches
  // should appear immediately rather than wait out the stagger delay.
  const [tabCardStagger, setTabCardStagger] = useState(2);
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

  /** Re-reads which AI tools are detected / connected on this machine. */
  const refreshClientStatuses = useCallback(async () => {
    const list = await api.listAgentClients().catch(() => fallbackClientStatuses());
    setClientStatuses(Array.isArray(list) ? list : fallbackClientStatuses());
  }, []);

  const loadRouterState = useCallback(async () => {
    try {
      const [pol, stickyList, traceList, sessList, accList, clientList] = await Promise.all([
        api.getRouterPolicy(),
        api.listStickyBindings(),
        api.listTraces(100),
        api.listSessions().catch(() => [] as RuntimeSession[]),
        api.listAccounts().catch(() => [] as Account[]),
        api.listAgentClients().catch(() => fallbackClientStatuses()),
      ]);

      if (pol) setPolicy(pol);
      setBindings(Array.isArray(stickyList) ? stickyList : []);
      setTraces(Array.isArray(traceList) ? traceList : []);
      setSessions(Array.isArray(sessList) ? sessList : []);
      setAccounts(Array.isArray(accList) ? accList : []);
      setClientStatuses(Array.isArray(clientList) ? clientList : fallbackClientStatuses());
      setIsOfflineDemo(false);
      setTickSec(0);
    } catch {
      activateDemoState();
      // The router endpoints are unreachable: still show the detection snapshot
      setClientStatuses((prev) => (prev.length > 0 ? prev : fallbackClientStatuses()));
    } finally {
      setLoading(false);
    }
  }, [activateDemoState]);

  useEffect(() => {
    void loadRouterState();
  }, [loadRouterState]);

  // Changes made inside the Connect drawer show up on the tiles right away
  useEffect(() => {
    const handleAgentConnected = () => void refreshClientStatuses();
    window.addEventListener(AGENT_CONNECTED_EVENT, handleAgentConnected);
    return () => window.removeEventListener(AGENT_CONNECTED_EVENT, handleAgentConnected);
  }, [refreshClientStatuses]);

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
      window.setTimeout(() => setPolicySavedFlash(false), 1800);
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

  const isCloudMode = deploymentMode === 'cloud' || Boolean(clientStatuses[0]?.cloud_mode);

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

  // Quick Connect bar: live detection state per flagship tool
  const clientStatusById = useMemo(() => {
    const m = new Map<AgentClientID, AgentClientConfigStatus>();
    clientStatuses.forEach((c) => m.set(c.client_id, c));
    return m;
  }, [clientStatuses]);

  const quickTools = useMemo(
    () =>
      QUICK_CONNECT_TOOLS.map((tool) => {
        const status = tool.clientId ? clientStatusById.get(tool.clientId) : undefined;
        return {
          ...tool,
          isInjected: Boolean(status?.injected),
          // The bundled `colab` CLI needs no config file, so it is always ready
          isDetected: tool.clientId ? Boolean(status?.detected) : true,
        };
      }),
    [clientStatusById],
  );
  const injectedCount = quickTools.filter((tool) => tool.isInjected).length;
  const detectedCount = quickTools.filter((tool) => !tool.isInjected && tool.isDetected).length;

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
  const strictChainActive = policy.fallback_chain?.length === 2;
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

      {/* Layer 0 — Quick Connect bar */}
      <LayeredCard padding="sm" staggerIndex={0}>
        <div className="space-y-3.5">
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-3">
            <div className="flex items-center gap-3 min-w-0">
              <span className="ui-glow-badge w-8 h-8 rounded-xl" aria-hidden="true">
                {isCloudMode ? <Globe className="w-4 h-4" /> : <Zap className="w-4 h-4" />}
              </span>
              <div className="min-w-0">
                <h2 className="text-[14px] font-semibold leading-snug text-fg">
                  {isCloudMode
                    ? isZh
                      ? '云端远程网关接入（Cloud Remote MCP）'
                      : 'Cloud Remote MCP Gateway'
                    : isZh
                      ? '一键连接你的 AI 工具'
                      : 'Quick Connect Your AI Tools'}
                </h2>
                <p className="mt-0.5 text-[12px] leading-relaxed text-fg-secondary">
                  {isCloudMode
                    ? isZh
                      ? '点击任一编程助手即可一键签发独立 ch_sk_... 密钥，并生成本机 colabhive connect 命令或远程 Streamable HTTP 配置'
                      : 'Click any AI assistant to mint a scoped ch_sk_... key and copy a 1-line laptop colabhive connect command or remote HTTP snippet'
                    : isZh
                      ? '自动检测本机已安装的编程助手，点击即可一键写入配置（首次修改自动备份，随时可撤回）'
                      : 'Auto-detects installed AI assistants — click any tool for 1-click setup with automatic backup'}
                </p>
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2 shrink-0">
              {isCloudMode ? (
                <StatusPill tone="sky" dot>
                  {isZh ? '支持本机 CLI 一行绑定远程网关' : 'Remote HTTP + Laptop CLI'}
                </StatusPill>
              ) : (
                <StatusPill tone={injectedCount > 0 ? 'emerald' : 'coral'} dot>
                  {injectedCount > 0
                    ? isZh
                      ? `已接入 ${injectedCount} 个工具`
                      : `${injectedCount} connected`
                    : detectedCount > 0
                      ? isZh
                        ? `检测到 ${detectedCount} 个可接入工具`
                        : `${detectedCount} detected`
                      : isZh
                        ? `${quickTools.length} 款工具可一键接入`
                        : `${quickTools.length} tools ready`}
                </StatusPill>
              )}

              <a
                href={SETTINGS_PATH}
                onClick={(e) => {
                  if (onNavigate) {
                    e.preventDefault();
                    onNavigate(SETTINGS_PATH);
                  }
                }}
                className="ui-btn ui-btn-secondary ui-btn-md no-underline"
                title={
                  isZh
                    ? '管理 AI 工具使用的访问密钥与网络代理'
                    : 'Manage the access keys and network proxies your AI tools use'
                }
              >
                <KeyRound className="w-3.5 h-3.5 shrink-0" />
                {isZh ? '密钥与代理设置' : 'Keys & Proxies'}
                <ArrowRight className="w-3 h-3 shrink-0" />
              </a>
            </div>
          </div>

          <div className="rounded-2xl bg-surface-1 border border-line-faint p-2">
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6 gap-2.5 ui-stagger">
              {quickTools.map((tool) => (
                <button
                  key={tool.key}
                  type="button"
                  onClick={() => onOpenConnect?.()}
                  data-done={(!isCloudMode && tool.isInjected) || undefined}
                  title={
                    isCloudMode
                      ? isZh
                        ? `点击生成 ${tool.label} 远程接入命令与专属 ch_sk_... 密钥`
                        : `Click to mint a key and copy remote setup for ${tool.label}`
                      : tool.isInjected
                        ? isZh
                          ? `${tool.label} 已接入，点击管理或撤回配置`
                          : `${tool.label} is connected — click to manage or undo`
                        : isZh
                          ? `点击一键接入 ${tool.label}`
                          : `Click to connect ${tool.label}`
                  }
                  className={cn(
                    'group relative flex flex-col justify-between min-w-0 rounded-[14px] border p-3 cursor-pointer text-left transition-all duration-200 bg-surface-2 hover:bg-surface-1 hover:-translate-y-[1px]',
                    !isCloudMode && tool.isInjected
                      ? 'border-[var(--emerald-border)] shadow-[var(--shadow-control)] hover:border-[var(--emerald)]'
                      : 'border-line shadow-[var(--shadow-control)] hover:border-line-strong hover:shadow-[var(--shadow-card)]',
                  )}
                >
                  <span className="flex items-center gap-2.5 min-w-0">
                    <span
                      aria-hidden="true"
                      className="ch-num flex items-center justify-center w-8 h-8 shrink-0 rounded-[10px] text-[11px] font-bold tracking-tight"
                      style={{
                        backgroundColor: 'var(--brand-bg)',
                        color: 'var(--brand-fg)',
                        boxShadow: '0 1px 2px rgba(0, 0, 0, 0.14)',
                      }}
                    >
                      {tool.monogram}
                    </span>
                    <span className="truncate text-[13px] font-semibold text-fg">{tool.label}</span>
                  </span>

                  <span className="mt-2.5 flex min-w-0">
                    {isCloudMode ? (
                      <StatusPill size="xs" tone="sky" dot className="max-w-full">
                        <span className="truncate">{isZh ? '远程接入' : 'Remote MCP'}</span>
                      </StatusPill>
                    ) : tool.isInjected ? (
                      <StatusPill size="xs" tone="emerald" dot className="max-w-full">
                        <span className="truncate">{isZh ? '已接入' : 'Connected'}</span>
                      </StatusPill>
                    ) : tool.isDetected ? (
                      <StatusPill size="xs" tone="coral" dot className="max-w-full">
                        <span className="truncate">{isZh ? '一键接入' : '1-Click Connect'}</span>
                      </StatusPill>
                    ) : (
                      <StatusPill size="xs" tone="neutral" className="max-w-full">
                        <span className="truncate">{isZh ? '一键接入' : '1-Click Connect'}</span>
                      </StatusPill>
                    )}
                  </span>
                </button>
              ))}
            </div>
          </div>
        </div>
      </LayeredCard>

      {/* Layer 1 — live routing topology */}
      <RouterTopologyGraph
        staggerIndex={1}
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
                    const selected = policy.default_strategy === strat;
                    const isRecommended = strat === 'waste-zero';
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
                          selected
                            ? 'bg-surface-2 border-[1.5px] border-[var(--accent)] shadow-[var(--shadow-card)]'
                            : 'bg-surface-1/75 border border-line-faint hover:bg-surface-2 hover:border-line-strong',
                        )}
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div className="flex items-start gap-2.5 min-w-0">
                            <span
                              aria-hidden="true"
                              className={cn(
                                'mt-[2px] flex items-center justify-center w-4 h-4 shrink-0 rounded-full border transition-colors',
                                selected
                                  ? 'bg-[var(--accent)] border-[var(--accent)]'
                                  : 'bg-surface-2 border-line-strong',
                              )}
                            >
                              {selected && (
                                <Check
                                  className="w-2.5 h-2.5 stroke-[3]"
                                  style={{ color: 'var(--accent-fg)' }}
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
                            tone={isRecommended ? 'emerald' : selected ? 'accent' : 'neutral'}
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
                      variant="secondary"
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
                      variant="secondary"
                      title={
                        isZh
                          ? '大模型保护：只在 H100 (80GB) 与 G4 (96GB) 之间换卡，绝不降到 40GB 显卡导致爆显存'
                          : 'Large-model protection: only switches between H100 (80GB) and G4 (96GB); never drops to a 40GB GPU'
                      }
                      onClick={() =>
                        void persistPolicy({
                          ...policy,
                          auto_fallback_enabled: true,
                          fallback_chain: LARGE_VRAM_WATERFALL,
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

                {/* Reorderable GPU order */}
                <div
                  className={cn(
                    'flex flex-wrap items-center gap-1.5 transition-opacity',
                    !policy.auto_fallback_enabled && 'opacity-50',
                  )}
                >
                  {fallbackChain.map((tier, idx, arr) => (
                    <React.Fragment key={`${tier}-${idx}`}>
                      <div className="inline-flex items-center h-7 px-0.5 rounded-lg border border-line bg-surface-2 shadow-[var(--shadow-control)]">
                        <button
                          type="button"
                          disabled={idx === 0 || !policy.auto_fallback_enabled}
                          onClick={() => moveFallbackTier(idx, -1)}
                          aria-label={isZh ? `提高 ${tier} 优先级` : `Move ${tier} earlier`}
                          title={isZh ? '提高优先级' : 'Move earlier'}
                          className="inline-flex items-center justify-center w-5 h-5 rounded-md text-fg-muted cursor-pointer transition-colors hover:bg-[var(--hover-fill)] hover:text-fg disabled:opacity-30 disabled:cursor-not-allowed"
                        >
                          <ChevronLeft className="w-3 h-3" />
                        </button>
                        <span className="ch-num inline-flex items-center gap-1.5 px-1 text-[12px] font-medium text-fg">
                          <span
                            className="w-1.5 h-1.5 rounded-full"
                            style={{ backgroundColor: getAcceleratorCssColor(tier) }}
                          />
                          {tier}
                        </span>
                        <button
                          type="button"
                          disabled={idx === arr.length - 1 || !policy.auto_fallback_enabled}
                          onClick={() => moveFallbackTier(idx, 1)}
                          aria-label={isZh ? `降低 ${tier} 优先级` : `Move ${tier} later`}
                          title={isZh ? '降低优先级' : 'Move later'}
                          className="inline-flex items-center justify-center w-5 h-5 rounded-md text-fg-muted cursor-pointer transition-colors hover:bg-[var(--hover-fill)] hover:text-fg disabled:opacity-30 disabled:cursor-not-allowed"
                        >
                          <ChevronRight className="w-3 h-3" />
                        </button>
                      </div>
                      {idx < arr.length - 1 && (
                        <ArrowRight className="w-3 h-3 shrink-0 text-fg-muted" aria-hidden="true" />
                      )}
                    </React.Fragment>
                  ))}

                  {strictChainActive && (
                    <StatusPill tone="amber" size="sm" className="ml-1">
                      {isZh
                        ? '大显存保护：绝不降到 40GB 以下防爆显存'
                        : '≥80GB VRAM Protected (Prevents OOM)'}
                    </StatusPill>
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
