import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from 'react';
import { Activity, Bot, GitBranch, Network, Pin, Terminal, X } from 'lucide-react';
import {
  DEFAULT_HARDWARE_CATALOG,
  type Account,
  type RouteBinding,
  type RouterPolicy,
  type RuntimeSession,
  type SubscriptionTier,
  type ToolTraceEvent,
} from '../../types';
import { formatHours } from '../../lib/api';
import { useI18n } from '../../i18n';
import { getFriendlySessionDisplay } from '../../lib/sessionLabels';
import { AnimatedNumber, CircularQuotaRing, LayeredCard, StatusPill, cn } from '../ui';
import { STRATEGY_METADATA } from '../terminal/workbenchMockData';

export interface RouterTopologyGraphProps {
  policy: RouterPolicy;
  bindings: RouteBinding[];
  sessions: RuntimeSession[];
  accounts: Account[];
  traces: ToolTraceEvent[];
  selectedCallerFilter?: string | null;
  onSelectCallerFilter?: (caller: string | null) => void;
  /** Position in the page entrance cascade (forwarded to `LayeredCard`). */
  staggerIndex?: number;
}

interface CallerNode {
  id: string;
  name: string;
  transport: string;
  keyPrefix?: string;
  activeCalls: number;
  boundSessionIds: string[];
  hasFallback: boolean;
}

interface ComputedEdge {
  id: string;
  d: string;
  /** Node ids at each end (`caller:*`, `router`, `sess:*`, `acc:*`). */
  from: string;
  to: string;
  active: boolean;
  isFallback: boolean;
}

const ROUTER_ID = 'router';

type NodeTone = 'emerald' | 'sky' | 'coral';

/**
 * Shared node shell: crisp Stardust pure-white card (`#FFFFFF` light / `#1E1E1C` dark)
 * resting on the recessed warm-stone topology canvas with neutral ambient shadows.
 */
const NODE_BASE =
  'relative w-full rounded-2xl border px-3 py-2.5 text-left focus:outline-none focus-visible:outline-none transition-[translate,transform,box-shadow,border-color,opacity] duration-300 ease-[var(--ease-spring)] hover:-translate-y-[1px] bg-surface-2 hover:bg-[var(--surface-2-hover)]';

const NODE_SHADOW_REST = 'var(--shadow-card)';
const NODE_SHADOW_BY_TONE: Record<NodeTone, string> = {
  emerald:
    '0 0 0 1.5px var(--emerald-border), 0 0 0 4px var(--emerald-soft), var(--shadow-card-hover)',
  sky: '0 0 0 1.5px var(--sky-border), 0 0 0 4px var(--sky-soft), var(--shadow-card-hover)',
  coral: '0 0 0 1.5px var(--coral-border), 0 0 0 4px var(--coral-soft), var(--shadow-card-hover)',
};
const NODE_BORDER_BY_TONE: Record<NodeTone, string> = {
  emerald: 'var(--emerald-border)',
  sky: 'var(--sky-border)',
  coral: 'var(--coral-border)',
};

const PORT_BASE =
  'hidden lg:block absolute top-1/2 -translate-y-1/2 w-2 h-2 rounded-full border border-line-strong bg-surface-2 transition-colors';

const TIER_LABEL: Record<SubscriptionTier, { zh: string; en: string }> = {
  AI_ULTRA: { zh: 'Ultra', en: 'Ultra' },
  PRO_PLUS: { zh: 'Pro+', en: 'Pro+' },
  PRO: { zh: 'Pro', en: 'Pro' },
  PAYG: { zh: '按量付费', en: 'Pay-as-you-go' },
  FREE: { zh: '免费版', en: 'Free' },
};

/** Plain-words label for a session's lifecycle state. */
function sessionStatusLabel(status: RuntimeSession['status'], isZh: boolean): string {
  switch (status) {
    case 'BUSY':
      return isZh ? '运行中' : 'Running';
    case 'IDLE':
      return isZh ? '空闲待命' : 'Idle';
    case 'PROVISIONING':
      return isZh ? '启动中' : 'Starting';
    case 'RECONNECTING':
      return isZh ? '重连中' : 'Reconnecting';
    case 'DRAINING':
      return isZh ? '迁移中' : 'Migrating';
    case 'ZOMBIE_ALERT':
      return isZh ? '进程卡死' : 'Hung';
    case 'ERROR':
      return isZh ? '异常' : 'Error';
    default:
      return isZh ? '已停止' : 'Stopped';
  }
}

function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const query = window.matchMedia('(prefers-reduced-motion: reduce)');
    const sync = () => setReduced(query.matches);
    sync();
    query.addEventListener('change', sync);
    return () => query.removeEventListener('change', sync);
  }, []);
  return reduced;
}

export function RouterTopologyGraph({
  policy,
  bindings,
  sessions,
  accounts,
  traces,
  selectedCallerFilter,
  onSelectCallerFilter,
  staggerIndex,
}: RouterTopologyGraphProps) {
  const { locale, displayEmail, displayProxy, displaySecret } = useI18n();
  const isZh = locale === 'zh-CN';
  const reducedMotion = usePrefersReducedMotion();

  const containerRef = useRef<HTMLDivElement | null>(null);
  const portRefs = useRef<Map<string, HTMLElement>>(new Map());

  const [hoveredNodeId, setHoveredNodeId] = useState<string | null>(null);
  const [edges, setEdges] = useState<ComputedEdge[]>([]);

  const setPortRef = useCallback((portKey: string, el: HTMLElement | null) => {
    if (el) {
      portRefs.current.set(portKey, el);
    } else {
      portRefs.current.delete(portKey);
    }
  }, []);

  // Stage 1: caller nodes from bindings + traces + canonical MCP clients
  const callerNodes = useMemo<CallerNode[]>(() => {
    const map = new Map<string, CallerNode>();

    bindings.forEach((b) => {
      const key = b.client_name || 'MCP Agent';
      const existing = map.get(key);
      if (existing) {
        existing.activeCalls += b.active_calls;
        if (!existing.boundSessionIds.includes(b.session_id)) {
          existing.boundSessionIds.push(b.session_id);
        }
      } else {
        map.set(key, {
          id: `caller:${key}`,
          name: key,
          transport: b.sticky_key.startsWith('sdk:')
            ? 'Python SDK'
            : b.sticky_key.startsWith('cli:')
              ? 'CLI'
              : 'MCP HTTP',
          keyPrefix: b.api_key_prefix,
          activeCalls: Math.max(1, b.active_calls),
          boundSessionIds: [b.session_id],
          hasFallback: false,
        });
      }
    });

    const liveSessions = sessions.filter((s) => s.status !== 'STOPPED' && s.status !== 'LOST');
    const resolveTraceSessionIds = (tr: ToolTraceEvent): string[] => {
      if (tr.session_id) return [tr.session_id];
      if ((tr.account_id || tr.cu_delta > 0) && tr.request_preview) {
        const match = /"experiment_id"\s*:\s*"([^"]+)"/.exec(tr.request_preview);
        if (match) {
          const expId = match[1];
          const expSessionIds = liveSessions
            .filter((s) => s.experiment_id === expId)
            .map((s) => s.id);
          if (expSessionIds.length > 0) return expSessionIds;
        }
      }
      return [];
    };

    traces.slice(0, 20).forEach((tr) => {
      const key = tr.caller || 'Agent';
      const matchedIds = resolveTraceSessionIds(tr);
      const existing = map.get(key);
      if (existing) {
        matchedIds.forEach((sid) => {
          if (!existing.boundSessionIds.includes(sid)) {
            existing.boundSessionIds.push(sid);
          }
        });
        if (!existing.keyPrefix && tr.api_key_prefix) {
          existing.keyPrefix = tr.api_key_prefix;
        }
        if (tr.status === 'fallback') existing.hasFallback = true;
      } else {
        map.set(key, {
          id: `caller:${key}`,
          name: key,
          transport: tr.transport.toUpperCase(),
          keyPrefix: tr.api_key_prefix,
          activeCalls: 0,
          boundSessionIds: matchedIds,
          hasFallback: tr.status === 'fallback',
        });
      }
    });

    // Ensure flagship agent clients appear even on a fresh idle cluster
    const defaults: Array<{ name: string; transport: string }> = [
      { name: 'Antigravity IDE', transport: 'MCP HTTP' },
      { name: 'Claude Code CLI', transport: 'MCP HTTP' },
      { name: 'Codex CLI', transport: 'MCP STDIO' },
      { name: 'Python HivePool SDK', transport: 'SDK' },
      { name: 'colabhive CLI', transport: 'CLI' },
    ];
    for (const def of defaults) {
      if (map.size >= 5) break;
      if (!map.has(def.name)) {
        map.set(def.name, {
          id: `caller:${def.name}`,
          name: def.name,
          transport: def.transport,
          activeCalls: 0,
          boundSessionIds: [],
          hasFallback: false,
        });
      }
    }

    return Array.from(map.values());
  }, [bindings, traces, sessions]);

  // Stage 3: cloud GPU instances (ALL live active sessions + binding stubs — never silently capped at 5)
  const topologySessions = useMemo(() => {
    const byId = new Map<string, RuntimeSession>();
    sessions
      .filter((s) => s.status !== 'STOPPED' && s.status !== 'LOST')
      .forEach((s) => byId.set(s.id, s));

    bindings.forEach((b) => {
      if (!byId.has(b.session_id)) {
        byId.set(b.session_id, {
          id: b.session_id,
          name: b.session_name,
          account_id: b.account_id,
          account_email: b.account_email,
          backend: 'public',
          endpoint: b.session_id,
          notebook_hash: '',
          proxy_url: '',
          variant: 'GPU',
          requested_accelerator: b.accelerator,
          accelerator: b.accelerator,
          fallback_applied: false,
          shape: 'HIGH_RAM',
          status: b.active_calls > 0 ? 'BUSY' : 'IDLE',
          kernel_id: b.kernel_id,
          burn_rate_cu_hr:
            DEFAULT_HARDWARE_CATALOG.find((h) => h.accelerator === b.accelerator)
              ?.default_burn_rate_cu_hr ?? 9.66,
          accumulated_cu: 0,
          colab_web_url: '',
          sticky_key: b.sticky_key,
          keepalive_healthy: true,
          zmq_reconnect_count: 0,
          uptime_sec: 600,
          created_at: b.pinned_at,
          updated_at: b.last_active_at,
        });
      }
    });

    return Array.from(byId.values()).sort((a, b) => {
      const aBusy = a.status === 'BUSY' ? 0 : 1;
      const bBusy = b.status === 'BUSY' ? 0 : 1;
      if (aBusy !== bBusy) return aBusy - bBusy;
      if (a.burn_rate_cu_hr !== b.burn_rate_cu_hr) return b.burn_rate_cu_hr - a.burn_rate_cu_hr;
      return (a.name || a.id).localeCompare(b.name || b.id);
    });
  }, [sessions, bindings]);

  // Stage 4: ALL active pooled accounts (never silently capped at 5)
  const topologyAccounts = useMemo(() => {
    return accounts.filter((a) => a.health !== 'REVOKED_BY_USER');
  }, [accounts]);

  // Live header telemetry: calls in flight and the pool's current CU burn
  const totalActiveCalls = useMemo(
    () => bindings.reduce((sum, b) => sum + Math.max(0, b.active_calls || 0), 0),
    [bindings],
  );
  const totalLiveBurnCuHr = useMemo(
    () => topologySessions.reduce((sum, s) => sum + Math.max(0, s.burn_rate_cu_hr || 0), 0),
    [topologySessions],
  );

  // Measure port coordinates and compute smooth cubic bezier edges
  const recomputeEdges = useCallback(() => {
    const root = containerRef.current;
    if (!root) return;
    const rootRect = root.getBoundingClientRect();

    const getCoords = (key: string): { x: number; y: number } | null => {
      const el = portRefs.current.get(key);
      // Ports are `display: none` below the desktop breakpoint
      if (!el || el.offsetParent === null) return null;

      // Layout offsets ignore transforms, so the staggered entrance and the
      // hover lift never skew an edge. A port is centred with
      // `top: 50%` + `translateY(-50%)`, so its `offsetTop` is already its
      // vertical centre.
      let x = el.offsetLeft + el.offsetWidth / 2;
      let y = el.offsetTop;
      let parent = el.offsetParent as HTMLElement | null;
      while (parent && parent !== root) {
        x += parent.offsetLeft + parent.clientLeft;
        y += parent.offsetTop + parent.clientTop;
        parent = parent.offsetParent as HTMLElement | null;
      }
      if (parent === root) return { x, y };

      // Unexpected offset chain: fall back to viewport boxes
      const r = el.getBoundingClientRect();
      return {
        x: r.left - rootRect.left + r.width / 2,
        y: r.top - rootRect.top + r.height / 2,
      };
    };

    const makeBezier = (p1: { x: number; y: number }, p2: { x: number; y: number }) => {
      const dx = Math.max(36, Math.abs(p2.x - p1.x) * 0.48);
      return `M ${p1.x.toFixed(1)} ${p1.y.toFixed(1)} C ${(p1.x + dx).toFixed(1)} ${p1.y.toFixed(1)}, ${(p2.x - dx).toFixed(1)} ${p2.y.toFixed(1)}, ${p2.x.toFixed(1)} ${p2.y.toFixed(1)}`;
    };

    const nextEdges: ComputedEdge[] = [];
    const routerIn = getCoords('router:in');
    const routerOut = getCoords('router:out');

    // AI tools -> Router (treat sticky-bound callers as active affinity edges)
    if (routerIn) {
      callerNodes.forEach((caller) => {
        const p1 = getCoords(`out:${caller.id}`);
        if (!p1) return;
        nextEdges.push({
          id: `e:${caller.id}->router`,
          d: makeBezier(p1, routerIn),
          from: caller.id,
          to: ROUTER_ID,
          active: caller.activeCalls > 0 || caller.boundSessionIds.length > 0,
          isFallback: caller.hasFallback,
        });
      });
    }

    // Router -> Cloud GPUs
    if (routerOut) {
      topologySessions.forEach((sess) => {
        const p2 = getCoords(`in:sess:${sess.id}`);
        if (!p2) return;
        const hasOwner = callerNodes.some((c) => c.boundSessionIds.includes(sess.id));
        nextEdges.push({
          id: `e:router->sess:${sess.id}`,
          d: makeBezier(routerOut, p2),
          from: ROUTER_ID,
          to: `sess:${sess.id}`,
          active: sess.status === 'BUSY' || sess.burn_rate_cu_hr > 0 || hasOwner,
          isFallback: sess.fallback_applied,
        });
      });
    }

    // Cloud GPUs -> Accounts
    topologySessions.forEach((sess) => {
      const p1 = getCoords(`out:sess:${sess.id}`);
      const p2 = getCoords(`in:acc:${sess.account_id}`);
      if (!p1 || !p2) return;
      nextEdges.push({
        id: `e:sess:${sess.id}->acc:${sess.account_id}`,
        d: makeBezier(p1, p2),
        from: `sess:${sess.id}`,
        to: `acc:${sess.account_id}`,
        active: sess.status === 'BUSY' || sess.burn_rate_cu_hr > 0,
        isFallback: sess.fallback_applied,
      });
    });

    setEdges(nextEdges);
  }, [callerNodes, topologySessions, topologyAccounts]);

  useEffect(() => {
    let raf = window.requestAnimationFrame(recomputeEdges);
    const schedule = () => {
      window.cancelAnimationFrame(raf);
      raf = window.requestAnimationFrame(recomputeEdges);
    };

    const root = containerRef.current;
    if (!root || typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', schedule);
      return () => {
        window.cancelAnimationFrame(raf);
        window.removeEventListener('resize', schedule);
      };
    }

    const observer = new ResizeObserver(schedule);
    observer.observe(root);
    return () => {
      window.cancelAnimationFrame(raf);
      observer.disconnect();
    };
  }, [recomputeEdges, topologyAccounts.length]);

  // --------------------------------------------------------------------------
  // Focus model: hovering (or selecting) a node lights up its end-to-end chain
  // --------------------------------------------------------------------------
  const activeFocusId =
    hoveredNodeId || (selectedCallerFilter ? `caller:${selectedCallerFilter}` : null);

  const focusChain = useMemo<Set<string> | null>(() => {
    if (!activeFocusId) return null;
    const chain = new Set<string>([activeFocusId]);
    const sessionIdSet = new Set(topologySessions.map((s) => s.id));

    const addSession = (sess: RuntimeSession) => {
      chain.add(`sess:${sess.id}`);
      chain.add(`acc:${sess.account_id}`);
    };
    const addOwners = (sessionId: string) => {
      callerNodes.forEach((c) => {
        if (c.boundSessionIds.includes(sessionId)) chain.add(c.id);
      });
    };

    if (activeFocusId === ROUTER_ID) {
      callerNodes.forEach((c) => {
        if (c.activeCalls > 0 || c.boundSessionIds.length > 0 || c.hasFallback) {
          chain.add(c.id);
        }
      });
      topologySessions.forEach((sess) => {
        const hasOwner = callerNodes.some((c) => c.boundSessionIds.includes(sess.id));
        if (
          sess.status === 'BUSY' ||
          sess.burn_rate_cu_hr > 0 ||
          sess.fallback_applied ||
          hasOwner
        ) {
          addSession(sess);
        }
      });
    } else if (activeFocusId.startsWith('caller:')) {
      chain.add(ROUTER_ID);
      const caller = callerNodes.find((c) => c.id === activeFocusId);
      caller?.boundSessionIds
        .filter((sid) => sessionIdSet.has(sid))
        .forEach((sid) => {
          const sess = topologySessions.find((s) => s.id === sid);
          if (sess) addSession(sess);
        });
    } else if (activeFocusId.startsWith('sess:')) {
      chain.add(ROUTER_ID);
      const sess = topologySessions.find((s) => `sess:${s.id}` === activeFocusId);
      if (sess) {
        addSession(sess);
        addOwners(sess.id);
      }
    } else if (activeFocusId.startsWith('acc:')) {
      const accSessions = topologySessions.filter((s) => `acc:${s.account_id}` === activeFocusId);
      if (accSessions.length > 0) {
        chain.add(ROUTER_ID);
        accSessions.forEach((sess) => {
          addSession(sess);
          addOwners(sess.id);
        });
      }
    }

    return chain;
  }, [activeFocusId, callerNodes, topologySessions]);

  const isDimmed = (nodeId: string) => focusChain !== null && !focusChain.has(nodeId);
  const isLit = (nodeId: string) => focusChain !== null && focusChain.has(nodeId);

  const hoverHandlers = (nodeId: string) => ({
    onMouseEnter: () => setHoveredNodeId(nodeId),
    onMouseLeave: () => setHoveredNodeId(null),
    onFocus: () => setHoveredNodeId(nodeId),
    onBlur: () => setHoveredNodeId(null),
  });

  const nodeClass = (nodeId: string, extra?: string) =>
    cn(NODE_BASE, isDimmed(nodeId) && 'opacity-40', extra);

  /** Whisper-soft glass edge at rest; semantic glow ring matching node state in focus. */
  const nodeStyle = (
    nodeId: string,
    tone: NodeTone = 'emerald',
    pinned = false,
  ): CSSProperties => {
    const lit = isLit(nodeId) || pinned;
    return {
      borderColor: lit ? NODE_BORDER_BY_TONE[tone] : 'var(--glass-card-border)',
      boxShadow: lit ? NODE_SHADOW_BY_TONE[tone] : NODE_SHADOW_REST,
    };
  };

  /** Semantic port dot fill: coral for fallback, emerald for active/bound, sky on standby hover. */
  const portStyle = (nodeId: string, liveColor: string | null): CSSProperties | undefined => {
    const color = liveColor ?? (isLit(nodeId) ? 'var(--sky)' : null);
    if (!color) return undefined;
    return {
      backgroundColor: color,
      borderColor: 'var(--surface-2)',
    };
  };

  const focusedCaller = activeFocusId?.startsWith('caller:')
    ? callerNodes.find((c) => c.id === activeFocusId)
    : undefined;
  const focusedSession = activeFocusId?.startsWith('sess:')
    ? topologySessions.find((s) => `sess:${s.id}` === activeFocusId)
    : undefined;

  const routerInLive = focusedCaller
    ? focusedCaller.hasFallback
      ? 'var(--coral)'
      : focusedCaller.activeCalls > 0 || focusedCaller.boundSessionIds.length > 0
        ? 'var(--emerald)'
        : null
    : callerNodes.some((c) => !c.hasFallback && (c.activeCalls > 0 || c.boundSessionIds.length > 0))
      ? 'var(--emerald)'
      : callerNodes.some((c) => c.hasFallback)
        ? 'var(--coral)'
        : null;

  const routerOutLive = focusedSession
    ? focusedSession.fallback_applied
      ? 'var(--coral)'
      : focusedSession.status === 'BUSY' || focusedSession.burn_rate_cu_hr > 0
        ? 'var(--emerald)'
        : null
    : topologySessions.some(
          (s) =>
            !s.fallback_applied &&
            (s.status === 'BUSY' ||
              s.burn_rate_cu_hr > 0 ||
              callerNodes.some((c) => c.boundSessionIds.includes(s.id))),
        )
      ? 'var(--emerald)'
      : topologySessions.some((s) => s.fallback_applied)
        ? 'var(--coral)'
        : null;

  const routerTone: NodeTone =
    focusedCaller?.hasFallback || focusedSession?.fallback_applied
      ? 'coral'
      : topologySessions.length > 0 ||
          callerNodes.some((c) => c.activeCalls > 0 || c.boundSessionIds.length > 0)
        ? 'emerald'
        : 'sky';

  const strategyMeta =
    STRATEGY_METADATA[policy.default_strategy] || STRATEGY_METADATA['waste-zero'];
  const strategyTitle = isZh ? strategyMeta.zhTitle : strategyMeta.enTitle;
  const [strategyName, ...strategyTaglineParts] = strategyTitle.split(' · ');
  const strategyTagline = strategyTaglineParts.join(' · ');
  const fallbackChain = policy.fallback_chain || ['H100', 'G4', 'A100', 'L4', 'T4'];
  const keepWarmMinutes = Math.round(policy.sticky_idle_ttl_sec / 60);

  return (
    <LayeredCard
      padding="none"
      staggerIndex={staggerIndex}
      icon={<Network />}
      title={isZh ? '实时选号与调度全景' : 'Live Routing Map'}
      subtitle={
        isZh
          ? '悬停任意节点查看完整链路，点击某个 AI 工具可筛选它的调用记录。'
          : 'Hover any node to trace its full path. Click an AI tool to filter its call log.'
      }
      headerRight={
        <div className="flex flex-wrap items-center justify-end gap-x-3.5 gap-y-1.5 text-[11.5px] text-fg-secondary">
          <StatusPill
            size="sm"
            tone={totalActiveCalls > 0 ? 'emerald' : 'neutral'}
            icon={<Activity />}
            title={
              isZh
                ? '当前正在执行的 AI 调用数，以及整个算力池的实时额度消耗速度'
                : 'AI calls in flight and the pool-wide live quota burn rate'
            }
          >
            <AnimatedNumber value={totalActiveCalls} />
            <span>{isZh ? '个调用进行中' : 'active calls'}</span>
            <span aria-hidden="true" className="opacity-50">
              ·
            </span>
            <AnimatedNumber value={totalLiveBurnCuHr} decimals={2} />
            <span>CU/h</span>
          </StatusPill>

          <span className="hidden md:inline-flex items-center gap-1.5">
            <span
              className="w-2 h-2 rounded-full"
              style={{ backgroundColor: 'var(--emerald)' }}
            />
            {isZh ? '正在运行 / 保持环境中' : 'Running / kept warm'}
          </span>
          <span className="hidden md:inline-flex items-center gap-1.5">
            <span
              className="w-3.5 border-t-[1.5px] border-dashed"
              style={{ borderColor: 'var(--coral)' }}
            />
            {isZh ? '缺卡自动换卡' : 'Auto-switched GPU'}
          </span>
          {selectedCallerFilter && onSelectCallerFilter && (
            <button
              type="button"
              onClick={() => onSelectCallerFilter(null)}
              className="inline-flex items-center gap-1 h-6 pl-2.5 pr-1.5 rounded-full border border-transparent bg-[var(--accent)] text-[var(--accent-fg)] text-[11px] font-semibold cursor-pointer transition-opacity hover:opacity-85"
              title={isZh ? '清除 AI 工具筛选' : 'Clear AI tool filter'}
            >
              <span className="max-w-[10rem] truncate">{selectedCallerFilter}</span>
              <X className="w-3 h-3" />
            </button>
          )}
        </div>
      }
    >
      <div
        ref={containerRef}
        className="relative rounded-b-[19px] bg-surface-1 border-t border-line-faint p-5 sm:p-6 lg:p-7"
      >
        {/* Bezier flow overlay (desktop). Fades in as the staggered nodes settle. */}
        <svg
          className="hidden lg:block pointer-events-none absolute inset-0 w-full h-full z-0 ui-anim-fade"
          style={{ animationDelay: '240ms', animationDuration: '320ms' }}
          aria-hidden="true"
        >
          {edges.map((edge) => {
            const inChain =
              focusChain !== null && focusChain.has(edge.from) && focusChain.has(edge.to);
            const dimmed = focusChain !== null && !inChain;
            const live = edge.active || edge.isFallback;
            const stroke = edge.isFallback
              ? 'var(--coral)'
              : edge.active
                ? 'var(--emerald)'
                : inChain
                  ? 'var(--sky)'
                  : 'var(--border-strong)';
            const strokeOpacity = live ? (inChain ? 0.96 : 0.85) : inChain ? 0.85 : 0.55;
            const strokeWidth = inChain ? (live ? 2 : 1.75) : live ? 1.6 : 1.15;
            const pulseDur = edge.isFallback ? '3.4s' : '2.6s';

            return (
              <g
                key={edge.id}
                style={{ opacity: dimmed ? 0.18 : 1, transition: 'opacity 180ms ease' }}
              >
                {/* Soft ambient glow under live or focused paths */}
                {(live || inChain) && (
                  <path
                    d={edge.d}
                    fill="none"
                    stroke={stroke}
                    strokeWidth={inChain ? 6.5 : 5}
                    strokeLinecap="round"
                    strokeOpacity={inChain ? 0.16 : 0.1}
                    style={{ transition: 'stroke 180ms ease, stroke-opacity 180ms ease' }}
                  />
                )}

                {/* Crisp main stroke */}
                <path
                  d={edge.d}
                  fill="none"
                  stroke={stroke}
                  strokeWidth={strokeWidth}
                  strokeLinecap="round"
                  strokeDasharray={edge.isFallback ? '5 4' : undefined}
                  strokeOpacity={strokeOpacity}
                  style={{ transition: 'stroke 180ms ease, stroke-opacity 180ms ease' }}
                />

                {/* Flow pulse gliding along genuinely active paths */}
                {(edge.active || (edge.isFallback && inChain)) && !dimmed && !reducedMotion && (
                  <g>
                    <circle r="5.5" fill={stroke} opacity="0.22" />
                    <circle r="2.6" fill={stroke} />
                    <animateMotion
                      dur={pulseDur}
                      repeatCount="indefinite"
                      path={edge.d}
                      keyPoints="0;1"
                      calcMode="spline"
                      keyTimes="0;1"
                      keySplines="0.42 0 0.58 1"
                    />
                    <animate
                      attributeName="opacity"
                      dur={pulseDur}
                      repeatCount="indefinite"
                      values="0;1;1;0"
                      keyTimes="0;0.14;0.86;1"
                    />
                  </g>
                )}
              </g>
            );
          })}
        </svg>

        <div className="relative z-10 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-5 lg:gap-9 items-center">
          {/* COLUMN 1 — AI tools */}
          <div className="space-y-2 min-w-0 ui-stagger">
            <ColumnCaption
              label={isZh ? '已接入的 AI 工具' : 'AI Tools'}
              count={callerNodes.length}
            />

            {callerNodes.map((caller) => {
              const selected = selectedCallerFilter === caller.name;
              const hasAffinity = caller.boundSessionIds.length > 0;
              const isConnected = caller.activeCalls > 0 || hasAffinity;
              const callerTone: NodeTone = caller.hasFallback
                ? 'coral'
                : isConnected
                  ? 'emerald'
                  : 'sky';
              const callerIconColor = selected
                ? callerTone === 'coral'
                  ? 'text-coral-text'
                  : callerTone === 'emerald'
                    ? 'text-emerald-text'
                    : 'text-sky-text'
                : 'text-fg-muted';
              const usesShell =
                caller.transport.includes('CLI') || caller.transport.includes('SDK');
              return (
                <button
                  key={caller.id}
                  type="button"
                  aria-pressed={selected}
                  title={
                    isZh
                      ? `点击只看 ${caller.name} 的调用记录`
                      : `Click to show only ${caller.name}'s calls`
                  }
                  onClick={() => onSelectCallerFilter?.(selected ? null : caller.name)}
                  {...hoverHandlers(caller.id)}
                  className={nodeClass(caller.id, 'cursor-pointer')}
                  style={nodeStyle(caller.id, callerTone, selected)}
                >
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2 min-w-0">
                      {usesShell ? (
                        <Terminal className={cn('w-3.5 h-3.5 shrink-0', callerIconColor)} />
                      ) : (
                        <Bot className={cn('w-3.5 h-3.5 shrink-0', callerIconColor)} />
                      )}
                      <span className="truncate text-[12.5px] font-medium text-fg">
                        {caller.name}
                      </span>
                    </div>
                    <StatusPill
                      size="xs"
                      mono
                      tone={selected ? callerTone : 'neutral'}
                      className="shrink-0"
                    >
                      {caller.transport}
                    </StatusPill>
                  </div>

                  <div className="ch-num mt-1.5 flex items-center justify-between gap-2 text-[11px] text-fg-muted">
                    <span className="truncate">
                      {caller.keyPrefix ? displaySecret(caller.keyPrefix) : 'ch_sk_live_***'}
                    </span>
                    <span
                      className="shrink-0"
                      style={
                        caller.activeCalls > 0
                          ? { color: 'var(--emerald-text)' }
                          : caller.hasFallback
                            ? { color: 'var(--coral-text)' }
                            : hasAffinity
                              ? { color: 'var(--emerald-text)' }
                              : undefined
                      }
                    >
                      {caller.activeCalls > 0 ? (
                        <>
                          <AnimatedNumber value={caller.activeCalls} />{' '}
                          {isZh ? '正在调用' : 'calling'}
                        </>
                      ) : caller.hasFallback ? (
                        isZh ? (
                          '缺卡已切'
                        ) : (
                          'Switched'
                        )
                      ) : hasAffinity ? (
                        isZh ? (
                          '环境保持中'
                        ) : (
                          'Kept warm'
                        )
                      ) : isZh ? (
                        '待命中'
                      ) : (
                        'Standby'
                      )}
                    </span>
                  </div>

                  <span
                    ref={(el) => setPortRef(`out:${caller.id}`, el)}
                    className={cn(PORT_BASE, '-right-1')}
                    style={portStyle(
                      caller.id,
                      caller.hasFallback
                        ? 'var(--coral)'
                        : isConnected
                          ? 'var(--emerald)'
                          : null,
                    )}
                  />
                </button>
              );
            })}
          </div>

          {/* COLUMN 2 — Smart router */}
          <div className="space-y-2 min-w-0 ui-stagger">
            <ColumnCaption label={isZh ? '智能选号引擎' : 'Smart Router'} />

            <div
              tabIndex={0}
              {...hoverHandlers(ROUTER_ID)}
              className={nodeClass(ROUTER_ID, 'px-3.5 py-3.5 space-y-3')}
              style={nodeStyle(ROUTER_ID, routerTone)}
            >
              <span
                ref={(el) => setPortRef('router:in', el)}
                className={cn(PORT_BASE, '-left-1')}
                style={portStyle(ROUTER_ID, routerInLive)}
              />
              <span
                ref={(el) => setPortRef('router:out', el)}
                className={cn(PORT_BASE, '-right-1')}
                style={portStyle(ROUTER_ID, routerOutLive)}
              />

              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-semibold text-fg">{strategyName}</div>
                  {strategyTagline && (
                    <div
                      className="mt-0.5 truncate text-[11.5px] text-fg-secondary"
                      title={strategyTagline}
                    >
                      {strategyTagline}
                    </div>
                  )}
                </div>
                <span data-tone="emerald" className="ui-dot ui-dot-pulse mt-1.5" />
              </div>

              <dl className="space-y-2 pt-3 border-t border-line-faint text-[11.5px]">
                <div className="flex items-center justify-between gap-2">
                  <dt className="inline-flex items-center gap-1.5 shrink-0 text-fg-muted">
                    <Pin className="w-3 h-3" />
                    {isZh ? '环境保持' : 'Keep-warm'}
                  </dt>
                  <dd className="ch-num min-w-0 truncate text-fg-secondary">
                    {policy.sticky_session_enabled
                      ? isZh
                        ? `会话环境保持 ${keepWarmMinutes} 分钟`
                        : `Keep warm ${keepWarmMinutes}m`
                      : isZh
                        ? '已关闭'
                        : 'Off'}
                  </dd>
                </div>

                <div className="flex items-center justify-between gap-2">
                  <dt className="inline-flex items-center gap-1.5 shrink-0 text-fg-muted">
                    <GitBranch className="w-3 h-3" />
                    {isZh ? '缺卡自动换卡' : 'GPU auto-switch'}
                  </dt>
                  <dd className="min-w-0 truncate text-fg-secondary">
                    {policy.auto_fallback_enabled
                      ? isZh
                        ? '已开启顺延'
                        : 'On'
                      : isZh
                        ? '仅指定显卡'
                        : 'Requested GPU only'}
                  </dd>
                </div>
              </dl>

              <div
                className={cn(
                  'ch-num truncate rounded-lg border border-line-faint bg-surface-1 px-2 py-1.5 text-[11px] text-fg-secondary',
                  !policy.auto_fallback_enabled && 'opacity-50',
                )}
                title={
                  isZh
                    ? `缺卡时的换卡顺序：${fallbackChain.join(' → ')}`
                    : `GPU order on stockouts: ${fallbackChain.join(' → ')}`
                }
              >
                {fallbackChain.join(' → ')}
              </div>
            </div>
          </div>

          {/* COLUMN 3 — Cloud GPU instances */}
          <div className="space-y-2 min-w-0 ui-stagger">
            <ColumnCaption
              label={isZh ? '云端 GPU 实例' : 'Cloud GPUs'}
              count={topologySessions.length}
            />

            {topologySessions.length === 0 ? (
              <ColumnNote>
                {isZh
                  ? '暂无运行中的云端实例，AI 调用时会按需自动分配。'
                  : 'No cloud GPUs running. One is allocated on demand.'}
              </ColumnNote>
            ) : (
              topologySessions.map((sess) => {
                const nodeKey = `sess:${sess.id}`;
                const busy = sess.status === 'BUSY';
                const hasOwner = callerNodes.some((c) => c.boundSessionIds.includes(sess.id));
                const isSessionLive = busy || sess.burn_rate_cu_hr > 0 || hasOwner;
                const sessionTone: NodeTone = sess.fallback_applied
                  ? 'coral'
                  : isSessionLive
                    ? 'emerald'
                    : 'sky';
                const live = sess.fallback_applied
                  ? 'var(--coral)'
                  : isSessionLive
                    ? 'var(--emerald)'
                    : null;
                return (
                  <div
                    key={sess.id}
                    tabIndex={0}
                    {...hoverHandlers(nodeKey)}
                    className={nodeClass(nodeKey)}
                    style={nodeStyle(nodeKey, sessionTone)}
                  >
                    <span
                      ref={(el) => setPortRef(`in:sess:${sess.id}`, el)}
                      className={cn(PORT_BASE, '-left-1')}
                      style={portStyle(nodeKey, live)}
                    />
                    <span
                      ref={(el) => setPortRef(`out:sess:${sess.id}`, el)}
                      className={cn(PORT_BASE, '-right-1')}
                      style={portStyle(nodeKey, live)}
                    />

                    <div className="flex items-center justify-between gap-2">
                      <span
                        className="truncate text-[12.5px] font-medium text-fg"
                        title={getFriendlySessionDisplay(sess, isZh).prefixedTitle}
                      >
                        {getFriendlySessionDisplay(sess, isZh).prefixedTitle}
                      </span>
                      <StatusPill
                        size="xs"
                        tone={
                          sess.fallback_applied
                            ? 'coral'
                            : getFriendlySessionDisplay(sess, isZh).isLLM
                              ? 'amber'
                              : busy
                                ? 'emerald'
                                : 'neutral'
                        }
                        mono
                        className="shrink-0"
                      >
                        {sess.accelerator}
                      </StatusPill>
                    </div>

                    <div className="mt-1.5 flex items-center justify-between gap-2 text-[11px] text-fg-muted">
                      {sess.fallback_applied ? (
                        <span
                          className="ch-num truncate font-medium"
                          style={{ color: 'var(--coral-text)' }}
                          title={
                            isZh
                              ? `${sess.requested_accelerator} 缺货，已自动换用 ${sess.accelerator}`
                              : `${sess.requested_accelerator} was out of stock; switched to ${sess.accelerator}`
                          }
                        >
                          {isZh ? '缺卡已切 ' : 'Switched '}
                          {sess.requested_accelerator} → {sess.accelerator}
                        </span>
                      ) : getFriendlySessionDisplay(sess, isZh).isLLM ? (
                        <span
                          className="truncate font-medium"
                          style={{ color: 'var(--amber-text)' }}
                        >
                          {isZh ? '大模型推理服务 · /v1' : 'LLM Inference · /v1'}
                        </span>
                      ) : sess.sticky_key ? (
                        <span className="inline-flex items-center gap-1 truncate">
                          <Pin className="w-3 h-3 shrink-0" />
                          {isZh ? '固定环境' : 'Pinned'}
                        </span>
                      ) : (
                        <span className="truncate">
                          {sess.shard_index !== undefined
                            ? isZh
                              ? `并行卡 #${sess.shard_index}`
                              : `Shard #${sess.shard_index}`
                            : isZh
                              ? '云端实例'
                              : 'Instance'}
                        </span>
                      )}
                      <span className="inline-flex items-center gap-1 shrink-0">
                        <span style={busy ? { color: 'var(--emerald-text)' } : undefined}>
                          {sessionStatusLabel(sess.status, isZh)}
                        </span>
                        <span aria-hidden="true">·</span>
                        <span>
                          <AnimatedNumber value={sess.burn_rate_cu_hr} decimals={2} /> CU/h
                        </span>
                      </span>
                    </div>
                  </div>
                );
              })
            )}
          </div>

          {/* COLUMN 4 — Colab account pool */}
          <div className="space-y-2 min-w-0 ui-stagger">
            <ColumnCaption
              label={isZh ? 'Colab 账号池' : 'Account Pool'}
              count={topologyAccounts.length}
            />

            {topologyAccounts.length === 0 ? (
              <ColumnNote>
                {isZh ? '账号池还是空的，请先添加一个 Colab 账号。' : 'No pooled accounts yet.'}
              </ColumnNote>
            ) : (
              topologyAccounts.map((acc) => {
                const nodeKey = `acc:${acc.id}`;
                const g4Rate = acc.gpu_entitlements?.G4?.calibrated_burn_rate || 9.66;
                const estG4Hours = acc.schedulable_cu / g4Rate;
                const burning = acc.live_burn_rate_cu_hr > 0;
                const accSessions = topologySessions.filter((s) => s.account_id === acc.id);
                const allFallback =
                  accSessions.length > 0 && accSessions.every((s) => s.fallback_applied);
                const isAccLive =
                  burning || accSessions.some((s) => s.status === 'BUSY' || s.burn_rate_cu_hr > 0);
                const accTone: NodeTone = allFallback ? 'coral' : isAccLive ? 'emerald' : 'sky';
                const accPortLive = allFallback
                  ? 'var(--coral)'
                  : isAccLive
                    ? 'var(--emerald)'
                    : null;
                const tier = TIER_LABEL[acc.subscription_tier];
                const cycleDenom =
                  acc.reference_cycle_cu > 0
                    ? acc.reference_cycle_cu
                    : acc.subscription_tier === 'AI_ULTRA' || acc.subscription_tier === 'PRO_PLUS'
                      ? 500
                      : 100;
                const quotaPct =
                  acc.subscription_tier === 'FREE' && acc.paid_cu_balance <= 0
                    ? Math.min(
                        100,
                        Math.round(((acc.free_quota?.remaining_free_cu ?? 12) / 20) * 100),
                      )
                    : Math.max(
                        0,
                        Math.min(100, Math.round((acc.schedulable_cu / cycleDenom) * 100)),
                      );

                return (
                  <div
                    key={acc.id}
                    tabIndex={0}
                    {...hoverHandlers(nodeKey)}
                    className={nodeClass(nodeKey)}
                    style={nodeStyle(nodeKey, accTone)}
                  >
                    <span
                      ref={(el) => setPortRef(`in:acc:${acc.id}`, el)}
                      className={cn(PORT_BASE, '-left-1')}
                      style={portStyle(nodeKey, accPortLive)}
                    />

                    <div className="flex items-start justify-between gap-2.5">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-1.5 min-w-0">
                          <span className="ch-num truncate text-[12px] font-semibold text-fg">
                            {displayEmail(acc.email)}
                          </span>
                          <StatusPill
                            size="xs"
                            tone={acc.is_contributor ? 'sky' : 'neutral'}
                            className="shrink-0"
                          >
                            {acc.is_contributor
                              ? isZh
                                ? '好友共享'
                                : 'Shared'
                              : tier
                                ? isZh
                                  ? tier.zh
                                  : tier.en
                                : acc.subscription_tier}
                          </StatusPill>
                        </div>

                        <div className="ch-num mt-1 flex items-center justify-between gap-2 text-[11px] text-fg-muted">
                          <span
                            className="truncate"
                            title={
                              isZh
                                ? `可调度额度，约可连续跑 G4 ${formatHours(estG4Hours)}`
                                : `Schedulable quota, about ${formatHours(estG4Hours)} of G4 time`
                            }
                          >
                            <AnimatedNumber value={acc.schedulable_cu} decimals={1} /> CU · G4≈
                            {formatHours(estG4Hours)}
                          </span>
                          <span
                            className="shrink-0 font-medium"
                            style={burning ? { color: 'var(--emerald-text)' } : undefined}
                          >
                            <AnimatedNumber value={acc.live_burn_rate_cu_hr} decimals={2} />
                            /h
                          </span>
                        </div>
                      </div>

                      <CircularQuotaRing value={quotaPct} size={28} strokeWidth={3} />
                    </div>

                    {acc.proxy && (
                      <div className="ch-num mt-1 truncate text-[10.5px] text-fg-muted">
                        {displayProxy(acc.proxy.masked_url).replace(/%2A/gi, '*')} ·{' '}
                        {acc.proxy.latency_ms}ms
                      </div>
                    )}
                  </div>
                );
              })
            )}
          </div>
        </div>
      </div>
    </LayeredCard>
  );
}

function ColumnCaption({ label, count }: { label: string; count?: number }) {
  return (
    <div className="text-[11.5px] font-medium text-fg-muted flex items-center justify-between px-1">
      <span>{label}</span>
      {count !== undefined && <span className="ch-num">{count}</span>}
    </div>
  );
}

function ColumnNote({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-xl border border-dashed border-line px-3 py-4 text-center text-[12px] text-fg-muted">
      {children}
    </div>
  );
}
