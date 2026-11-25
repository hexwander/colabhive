import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  AlertCircle,
  AppWindow,
  ArrowUpRight,
  Box,
  Check,
  CheckCircle2,
  ChevronRight,
  Cloud,
  Copy,
  Cpu,
  ExternalLink,
  Eye,
  EyeOff,
  Loader2,
  Lock,
  LogOut,
  PanelBottomOpen,
  Power,
  RefreshCw,
  Settings,
  ShieldCheck,
  Zap,
} from 'lucide-react';
import type {
  AcceleratorType,
  Account,
  AgentClientConfigStatus,
  AgentClientID,
  DaemonHealthStatus,
  QuotaSummary,
  RuntimeSession,
  SubscriptionTier,
  ToolTraceEvent,
  WSEvent,
} from '../types';
import {
  ADMIN_TOKEN_CHANGED_EVENT,
  api,
  APIError,
  clearStoredAdminToken,
  formatCU,
  getStoredAdminToken,
  setStoredAdminToken,
  UNAUTHORIZED_EVENT,
  useColabHiveEvents,
} from '../lib/api';
import { isDemoSiteMode } from '../lib/demoSiteAdapter';
import {
  buildSmartColabWebUrl,
  canOpenColabWebForSession,
  findSessionAccount,
} from '../lib/colabUrl';
import { useI18n } from '../i18n';
import { getObservatoryHomePath, navigate, withBasePath } from '../components/layout/navigation';
import { AgentBrandIcon } from '../components/ui/AgentBrandIcon';
import {
  buildFallbackClientConfigs,
  DEMO_ACCOUNTS,
  DEMO_QUOTA_SUMMARY,
  formatRelativeCountdown,
  HardwareTierBadge,
} from '../components/layout/telemetryUi';
import {
  DEMO_SESSIONS,
  DEMO_TRACES,
  formatDurationMs,
  formatElapsedSec,
  normalizeUtilPct,
} from '../components/terminal/workbenchMockData';
import {
  CircularQuotaRing,
  cn,
  Switch,
  type QuotaRingTone,
} from '../components/ui/primitives';
import { BrandMark, postNativeDesktopMessage } from '../components/ui/BrandLogo';

// ============================================================================
// Native WebView2 + HTTP Desktop Bridge Helper
// ============================================================================

export type DesktopBridgeAction =
  | 'open-main'
  | 'open-route'
  | 'show-tray'
  | 'sync-now'
  | 'copy-mcp'
  | 'quit'
  | 'hide-tray'
  | 'toggle-autostart';

interface WebView2Bridge {
  postMessage: (message: unknown) => void;
}

function getWebView2Bridge(): WebView2Bridge | null {
  if (typeof window === 'undefined') return null;
  const win = window as Window & {
    chrome?: {
      webview?: WebView2Bridge;
    };
    webkit?: {
      messageHandlers?: {
        colabhive?: WebView2Bridge;
      };
    };
  };
  const webview = win.chrome?.webview ?? win.webkit?.messageHandlers?.colabhive;
  if (webview && typeof webview.postMessage === 'function') {
    return webview;
  }
  return null;
}

function extractAutostartFlag(
  payload?: {
    autostart_enabled?: boolean;
    autostart?: boolean;
    state?: { autostart_enabled?: boolean; autostart?: boolean };
  } | null,
): boolean | undefined {
  const autoEnabled = payload?.autostart_enabled ?? payload?.autostart;
  if (typeof autoEnabled === 'boolean') {
    return autoEnabled;
  }
  const nestedAuto = payload?.state?.autostart_enabled ?? payload?.state?.autostart;
  if (typeof nestedAuto === 'boolean') {
    return nestedAuto;
  }
  return undefined;
}

/**
 * Triggers a native desktop shell action via BOTH WebView2 `postMessage` (when
 * hosted inside the Windows System Tray flyout) AND `POST /api/v1/desktop/action`,
 * falling back cleanly to `window.open(..., '_blank')` when previewed in a browser.
 */
export async function triggerDesktopAction(
  action: DesktopBridgeAction,
  path = '/',
): Promise<{ nativeHandled: boolean; autostart?: boolean; error?: string }> {
  const hasNativeWebView = postNativeDesktopMessage({
    type: 'colabhive:desktop-action',
    action,
    route: path,
    path,
  });

  let serverHandled = false;
  let autostart: boolean | undefined;
  let error: string | undefined;
  try {
    const res = await api.desktopAction(action, path);
    if (res) {
      serverHandled = Boolean(
        res.handled || res.bridge_active || res.native || res.desktop_shell,
      );
      const resolvedAuto = extractAutostartFlag(res);
      if (typeof resolvedAuto === 'boolean' && (serverHandled || res.autostart !== undefined)) {
        autostart = resolvedAuto;
      }
    }
  } catch (err) {
    serverHandled = false;
    error = err instanceof Error ? err.message : String(err);
  }

  const nativeHandled = hasNativeWebView || serverHandled;

  if (!nativeHandled && typeof window !== 'undefined') {
    if (isDemoSiteMode()) {
      if (action === 'open-main') {
        navigate(getObservatoryHomePath());
      } else if (action === 'open-route') {
        const targetRoute = path === '/' ? getObservatoryHomePath() : path;
        navigate(targetRoute);
      }
      return { nativeHandled: true, autostart, error };
    }
    if (action === 'open-main' || action === 'open-route') {
      const targetUrl = withBasePath(path.startsWith('/') ? path : `/${path}`);
      window.open(targetUrl, '_blank', 'noopener,noreferrer');
    } else if (action === 'quit') {
      try {
        window.close();
      } catch {
        // Browser tabs not opened by script ignore window.close()
      }
    }
  }

  return { nativeHandled, autostart, error };
}

// ============================================================================
// Constants & Compact Formatters (Magpie Visual Craft · Warm Stone & Ink)
// ============================================================================

export type TrayTab = 'agents' | 'quota' | 'instances' | 'router';

const AGENT_CLIENT_ORDER: {
  id: AgentClientID;
  label: string;
  monogram: string;
  toneBg: string;
  toneText: string;
}[] = [
  {
    id: 'antigravity',
    label: 'Antigravity',
    monogram: 'AG',
    toneBg: 'rgba(24, 24, 27, 0.08)',
    toneText: 'var(--text-primary)',
  },
  {
    id: 'claude-code',
    label: 'Claude Code',
    monogram: 'CC',
    toneBg: 'var(--coral-soft)',
    toneText: 'var(--coral-text)',
  },
  {
    id: 'cursor',
    label: 'Cursor',
    monogram: 'CU',
    toneBg: 'var(--sky-soft)',
    toneText: 'var(--sky-text)',
  },
  {
    id: 'codex',
    label: 'Codex',
    monogram: 'CX',
    toneBg: 'var(--emerald-soft)',
    toneText: 'var(--emerald-text)',
  },
  {
    id: 'gemini-cli',
    label: 'Gemini CLI',
    monogram: 'GM',
    toneBg: 'var(--sky-soft)',
    toneText: 'var(--sky-text)',
  },
  {
    id: 'windsurf',
    label: 'Windsurf',
    monogram: 'WS',
    toneBg: 'var(--emerald-soft)',
    toneText: 'var(--emerald-text)',
  },
  {
    id: 'opencode',
    label: 'OpenCode',
    monogram: 'OC',
    toneBg: 'var(--amber-soft)',
    toneText: 'var(--amber-text)',
  },
  {
    id: 'claude-desktop',
    label: 'Claude Desktop',
    monogram: 'CD',
    toneBg: 'var(--coral-soft)',
    toneText: 'var(--coral-text)',
  },
];

const AGENT_CONNECTED_STORAGE_KEY = 'colabhive_agent_connected_v3';
const AGENT_CONNECTED_EVENT = 'colabhive:agent-connected';

function announceAgentConnected(connected: boolean): void {
  try {
    localStorage.setItem(AGENT_CONNECTED_STORAGE_KEY, String(connected));
  } catch {
    // Ignore private storage error
  }
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent(AGENT_CONNECTED_EVENT, { detail: { connected } }));
  }
}

function parseInitialTab(): TrayTab {
  if (typeof window === 'undefined') return 'quota';
  const raw = new URLSearchParams(window.location.search).get('tab')?.toLowerCase();
  if (raw === 'agent' || raw === 'agents') return 'agents';
  if (raw === 'instance' || raw === 'instances' || raw === 'vm') return 'instances';
  if (raw === 'router' || raw === 'route') return 'router';
  return 'quota';
}

function resolveAccountCycleCap(acc: Account): number {
  const tierDefault =
    acc.subscription_tier === 'AI_ULTRA'
      ? 2500
      : acc.subscription_tier === 'PRO_PLUS'
        ? 500
        : acc.subscription_tier === 'PRO'
          ? 100
          : 100;
  return Math.max(acc.reference_cycle_cu || tierDefault, acc.paid_cu_balance || 0, 1);
}

function tierEyebrowLabel(tier: SubscriptionTier): string {
  switch (tier) {
    case 'AI_ULTRA':
      return 'AI ULTRA';
    case 'PRO_PLUS':
      return 'COLAB PRO+';
    case 'PRO':
      return 'COLAB PRO';
    case 'PAYG':
      return 'PAYG';
    case 'FREE':
    default:
      return 'FREE PLAN';
  }
}

function tierDotColor(tier: SubscriptionTier): string {
  switch (tier) {
    case 'AI_ULTRA':
      return 'var(--emerald)';
    case 'PRO_PLUS':
    case 'PRO':
      return 'var(--sky)';
    case 'PAYG':
      return 'var(--amber)';
    case 'FREE':
    default:
      return 'var(--text-muted)';
  }
}

function shortAccountTag(acc: Account, maskedEmail: string): string {
  if (acc.alias && acc.alias.trim()) {
    return acc.alias.trim();
  }
  const at = maskedEmail.indexOf('@');
  return at > 0 ? maskedEmail.slice(0, at) : maskedEmail;
}

function deriveRingTone(acc: Account, pct: number): QuotaRingTone {
  if (
    acc.health === 'AUTH_EXPIRED' ||
    acc.health === 'PROXY_ERROR' ||
    acc.health === 'QUARANTINED_DENYLIST' ||
    pct <= 15
  ) {
    return 'coral';
  }
  if (pct <= 35) return 'amber';
  if (acc.subscription_tier === 'FREE') return 'sky';
  return 'emerald';
}

function barColorForTone(tone: QuotaRingTone): string {
  switch (tone) {
    case 'coral':
      return 'var(--coral)';
    case 'amber':
      return 'var(--amber)';
    case 'sky':
      return 'var(--sky)';
    case 'neutral':
      return 'var(--text-muted)';
    case 'emerald':
    default:
      return 'var(--emerald)';
  }
}

function formatTraceClock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '--:--:--';
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  const ss = String(d.getSeconds()).padStart(2, '0');
  return `${hh}:${mm}:${ss}`;
}

function formatUpdatedAgo(updatedAtMs: number, nowMs: number, zh: boolean): string {
  const sec = Math.max(0, Math.floor((nowMs - updatedAtMs) / 1000));
  if (sec < 15) return zh ? '刚刚更新' : 'Updated just now';
  if (sec < 60) return zh ? `${sec} 秒前更新` : `Updated ${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return zh ? `${min} 分钟前更新` : `Updated ${min}m ago`;
  const hr = Math.floor(min / 60);
  return zh ? `${hr} 小时前更新` : `Updated ${hr}h ago`;
}

function shortConfigHint(path: string, transport: 'http' | 'stdio'): string {
  const parts = path.split(/[\\/]/);
  const file = parts[parts.length - 1] || path;
  return `${file} · ${transport.toUpperCase()}`;
}

function cleanCallerName(caller?: string): string {
  if (!caller || !caller.trim()) return 'MCP Agent';
  return caller
    .replace(/\s+IDE$/i, '')
    .replace(/\s+CLI$/i, '')
    .trim();
}

function cleanToolName(tool?: string): string {
  if (!tool) return 'execute_code';
  return tool.replace(/^\/api\/v1\//, '');
}

// ============================================================================
// Crisp ColabHive Geometric Emblem Marks (Top Bar & 3-Node Center Hub)
// ============================================================================

function TrayBrandMark({ size = 20 }: { size?: number }) {
  return <BrandMark size={size} />;
}

/** Center node badge in the 3-node Magpie router flow card (`[Caller] ── (🐝) ── [Account · GPU]`). */
function HiveHubEmblem() {
  return (
    <span
      aria-hidden="true"
      className="inline-flex items-center justify-center w-10 h-10 rounded-full shrink-0 border"
      style={{
        backgroundColor: 'var(--surface-2)',
        borderColor: 'var(--border-subtle)',
        boxShadow: '0 2px 8px rgba(0, 0, 0, 0.065), 0 1px 2px rgba(0, 0, 0, 0.04)',
      }}
    >
      <BrandMark size={26} />
    </span>
  );
}

// ============================================================================
// TrayPopoverPage Component (`/tray`)
// ============================================================================

export interface TrayPopoverPageProps {
  embedded?: boolean;
  controlledTab?: TrayTab;
  onTabChange?: (tab: TrayTab) => void;
  summary?: QuotaSummary;
  accounts?: Account[];
  sessions?: RuntimeSession[];
  offlineSnapshot?: boolean;
  onRefreshParent?: () => Promise<void> | void;
}

export function TrayPopoverPage({
  embedded = false,
  controlledTab,
  onTabChange,
  summary: propSummary,
  accounts: propAccounts,
  sessions: propSessions,
  onRefreshParent,
}: TrayPopoverPageProps = {}) {
  const { locale, displayEmail } = useI18n();
  const zh = locale === 'zh-CN';

  const [internalTab, setInternalTab] = useState<TrayTab>(() =>
    embedded ? 'agents' : parseInitialTab(),
  );
  const activeTab = controlledTab ?? internalTab;
  const setActiveTab = useCallback(
    (next: TrayTab) => {
      setInternalTab(next);
      onTabChange?.(next);
    },
    [onTabChange],
  );

  const [summary, setSummary] = useState<QuotaSummary>(propSummary ?? DEMO_QUOTA_SUMMARY);
  const [accounts, setAccounts] = useState<Account[]>(propAccounts ?? DEMO_ACCOUNTS);
  const [sessions, setSessions] = useState<RuntimeSession[]>(propSessions ?? []);
  const [traces, setTraces] = useState<ToolTraceEvent[]>([]);
  const [clients, setClients] = useState<AgentClientConfigStatus[]>(() =>
    buildFallbackClientConfigs(
      typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860',
      '',
      'http',
    ),
  );

  const [syncing, setSyncing] = useState(false);
  const [syncingAccountId, setSyncingAccountId] = useState<string | null>(null);
  const [togglingClientId, setTogglingClientId] = useState<AgentClientID | null>(null);
  const [stoppingSessionId, setStoppingSessionId] = useState<string | null>(null);
  const [confirmExternalStopId, setConfirmExternalStopId] = useState<string | null>(null);
  const [openingColabId, setOpeningColabId] = useState<string | null>(null);

  const [autostart, setAutostart] = useState<boolean | null>(null);
  const [trayMode, setTrayMode] = useState<'panel' | 'menu'>(() => {
    if (embedded || typeof window === 'undefined') return 'panel';
    const qs = new URLSearchParams(window.location.search);
    if (qs.get('mode') === 'menu') return 'menu';
    const winMode = (window as Window & { __COLABHIVE_TRAY_MODE__?: string }).__COLABHIVE_TRAY_MODE__;
    return winMode === 'menu' ? 'menu' : 'panel';
  });
  const [menuCopiedMcp, setMenuCopiedMcp] = useState(false);
  const [health, setHealth] = useState<DaemonHealthStatus | null>(null);
  const [authGateLocked, setAuthGateLocked] = useState<boolean>(false);
  const [gateTokenInput, setGateTokenInput] = useState('');
  const [gateShowToken, setGateShowToken] = useState(false);
  const [gateVerifying, setGateVerifying] = useState(false);
  const [gateError, setGateError] = useState<string | null>(null);
  const [toast, setToast] = useState<{ tone: 'ok' | 'err'; text: string } | null>(null);
  const [lastUpdatedAt, setLastUpdatedAt] = useState<number>(() => Date.now());
  const [nowMs, setNowMs] = useState<number>(() => Date.now());

  // Listen for native WebView2 tray mode switches (`panel` <-> `menu`) and autostart updates
  useEffect(() => {
    if (embedded || typeof window === 'undefined') return;
    const win = window as Window & {
      __COLABHIVE_SET_TRAY_MODE__?: (mode: string) => void;
    };
    const applyMode = (raw: string) => {
      const next = raw === 'menu' ? 'menu' : 'panel';
      setTrayMode(next);
      if (next === 'menu') setMenuCopiedMcp(false);
    };
    win.__COLABHIVE_SET_TRAY_MODE__ = applyMode;
    const onTrayModeEvent = (evt: Event) => {
      const detail = (evt as CustomEvent<{ mode?: string }>).detail;
      if (detail?.mode) applyMode(detail.mode);
    };
    const onAutostartEvent = (evt: Event) => {
      const detail = (evt as CustomEvent<{ autostart?: boolean }>).detail;
      if (typeof detail?.autostart === 'boolean') {
        setAutostart(detail.autostart);
      }
    };
    window.addEventListener('colabhive:tray-mode', onTrayModeEvent);
    window.addEventListener('colabhive:autostart-state', onAutostartEvent);
    return () => {
      delete win.__COLABHIVE_SET_TRAY_MODE__;
      window.removeEventListener('colabhive:tray-mode', onTrayModeEvent);
      window.removeEventListener('colabhive:autostart-state', onAutostartEvent);
    };
  }, [embedded]);

  // Detect whether we are inside the 396x580 native flyout or a wide browser preview tab
  const [isCompactViewport, setIsCompactViewport] = useState<boolean>(() => {
    if (embedded) return false;
    if (typeof window === 'undefined') return true;
    if (getWebView2Bridge()) return true;
    const qs = new URLSearchParams(window.location.search);
    if (qs.get('native') === '1' || qs.get('embed') === '1') return true;
    return window.innerWidth <= 440;
  });

  useEffect(() => {
    if (embedded) {
      setIsCompactViewport(false);
      return;
    }
    if (typeof window === 'undefined') return;
    const onResize = () => {
      if (getWebView2Bridge()) {
        setIsCompactViewport(true);
        return;
      }
      const qs = new URLSearchParams(window.location.search);
      if (qs.get('native') === '1' || qs.get('embed') === '1') {
        setIsCompactViewport(true);
        return;
      }
      setIsCompactViewport(window.innerWidth <= 440);
    };
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, [embedded]);

  // Keep relative footer timestamp fresh every 15s
  useEffect(() => {
    const id = window.setInterval(() => setNowMs(Date.now()), 15_000);
    return () => window.clearInterval(id);
  }, []);

  // Auto-dismiss transient inline toast
  useEffect(() => {
    if (!toast) return;
    const id = window.setTimeout(() => setToast(null), 2800);
    return () => window.clearTimeout(id);
  }, [toast]);

  // Mirror parent telemetry updates when provided
  useEffect(() => {
    if (propSummary) setSummary(propSummary);
  }, [propSummary]);

  useEffect(() => {
    if (propAccounts) setAccounts(propAccounts);
  }, [propAccounts]);

  useEffect(() => {
    if (propSessions && propSessions.length > 0) setSessions(propSessions);
  }, [propSessions]);

  const syncAll = useCallback(
    async (deepRefresh = false) => {
      setSyncing(true);
      try {
        const h = await api.getHealth();
        setHealth(h);
        if (h.auth_required && !getStoredAdminToken()) {
          setAuthGateLocked(true);
          return;
        }

        const origin =
          typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860';
        const [nextSummary, nextAccounts, nextSessions, nextTraces, nextClients, deskState] =
          await Promise.all([
            api.getQuotaSummary(deepRefresh),
            api.listAccounts(),
            deepRefresh
              ? api
                  .syncRemoteSessions()
                  .then((r) => r.sessions)
                  .catch(() => api.listSessions())
              : api.listSessions(),
            api.listTraces(60).catch(() => [] as ToolTraceEvent[]),
            api
              .listAgentClients()
              .catch(() => buildFallbackClientConfigs(origin, '', 'http')),
            api.getDesktopState().catch(() => null),
          ]);

        setAuthGateLocked(false);
        setSummary(nextSummary);
        setAccounts(nextAccounts);
        setSessions(Array.isArray(nextSessions) ? nextSessions : []);
        setTraces(Array.isArray(nextTraces) ? nextTraces : []);
        if (Array.isArray(nextClients) && nextClients.length > 0) {
          setClients(nextClients);
        }
        const autoEnabled = deskState?.autostart_enabled ?? deskState?.autostart;
        const serverHasBridge = Boolean(
          deskState && (deskState.desktop_shell || deskState.native || deskState.bridge_active),
        );
        if (serverHasBridge && typeof autoEnabled === 'boolean') {
          setAutostart(autoEnabled);
        } else if (typeof window !== 'undefined' && getWebView2Bridge()) {
          const boot = (
            window as Window & {
              __COLABHIVE_DESKTOP__?: { autostart_enabled?: boolean; autostart?: boolean };
            }
          ).__COLABHIVE_DESKTOP__;
          const bootAuto = boot?.autostart_enabled ?? boot?.autostart;
          if (typeof bootAuto === 'boolean') {
            setAutostart(bootAuto);
          }
        }
        setLastUpdatedAt(Date.now());
        setNowMs(Date.now());
        if (deepRefresh && onRefreshParent) {
          void onRefreshParent();
        }
      } catch (err) {
        if (err instanceof APIError && (err.status === 401 || err.status === 403)) {
          setAuthGateLocked(true);
          return;
        }
        // Offline preview snapshot fallback
        setSummary((prev) => prev ?? DEMO_QUOTA_SUMMARY);
        setAccounts((prev) => (prev.length > 0 ? prev : DEMO_ACCOUNTS));
        setSessions((prev) => (prev.length > 0 ? prev : DEMO_SESSIONS));
        setTraces((prev) => (prev.length > 0 ? prev : DEMO_TRACES));
        setLastUpdatedAt(Date.now());
      } finally {
        setSyncing(false);
      }
    },
    [onRefreshParent],
  );

  useEffect(() => {
    const onUnauth = () => setAuthGateLocked(true);
    const onTokChanged = () => {
      const tok = getStoredAdminToken();
      if (tok) {
        setAuthGateLocked(false);
        void syncAll(false);
      } else {
        setAuthGateLocked(true);
      }
    };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauth);
    window.addEventListener(ADMIN_TOKEN_CHANGED_EVENT, onTokChanged);
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauth);
      window.removeEventListener(ADMIN_TOKEN_CHANGED_EVENT, onTokChanged);
    };
  }, [syncAll]);

  const handleTrayGateSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const cleaned = gateTokenInput.trim();
    if (!cleaned) {
      setGateError(zh ? '请输入管理员访问口令' : 'Please enter admin token');
      return;
    }
    if (cleaned.startsWith('ch_sk_')) {
      setGateError(
        zh
          ? '这是 AI 助手密钥 (ch_sk_…)，请输入管理员口令 (COLABHIVE_ADMIN_TOKEN)'
          : 'Enter COLABHIVE_ADMIN_TOKEN rather than a ch_sk_… agent key.',
      );
      return;
    }
    setGateVerifying(true);
    setGateError(null);
    try {
      await api.verifyAdminToken(cleaned);
      setStoredAdminToken(cleaned);
      setGateTokenInput('');
      setAuthGateLocked(false);
      await syncAll(false);
    } catch (err) {
      if (err instanceof APIError && err.status === 429) {
        setGateError(zh ? '尝试过于频繁，请稍候 60 秒再试' : 'Rate-limited, retry in 60s');
      } else {
        setGateError(zh ? '管理员口令无效，请核对后重试' : 'Invalid admin token');
      }
    } finally {
      setGateVerifying(false);
    }
  };

  useEffect(() => {
    void syncAll(false);
    const interval = window.setInterval(() => {
      void syncAll(false);
    }, 20_000);
    return () => window.clearInterval(interval);
  }, [syncAll]);

  // Live WebSocket stream updates (`/ws/events`)
  const handleWsEvent = useCallback((evt: WSEvent) => {
    if (!evt?.type) return;
    if (evt.type === 'quota_updated' && evt.payload) {
      setSummary(evt.payload as QuotaSummary);
      setLastUpdatedAt(Date.now());
    } else if (evt.type === 'account_updated' && evt.payload) {
      const updated = evt.payload as Account;
      setAccounts((prev) => {
        const exists = prev.some((a) => a.id === updated.id);
        return exists ? prev.map((a) => (a.id === updated.id ? updated : a)) : [updated, ...prev];
      });
      setLastUpdatedAt(Date.now());
    } else if (evt.type === 'session_updated' && evt.payload) {
      const updated = evt.payload as RuntimeSession;
      setSessions((prev) => {
        const exists = prev.some((s) => s.id === updated.id);
        return exists ? prev.map((s) => (s.id === updated.id ? updated : s)) : [updated, ...prev];
      });
      setLastUpdatedAt(Date.now());
    } else if (evt.type === 'trace_event' && evt.payload) {
      const incoming = evt.payload as ToolTraceEvent;
      setTraces((prev) => [incoming, ...prev.filter((t) => t.id !== incoming.id)].slice(0, 80));
      setLastUpdatedAt(Date.now());
    }
  }, []);

  const wsEnabled = !health?.auth_required || (!authGateLocked && Boolean(getStoredAdminToken()));
  useColabHiveEvents(handleWsEvent, wsEnabled);

  // Derived metrics
  const clientById = useMemo(() => {
    const m = new Map<AgentClientID, AgentClientConfigStatus>();
    for (const c of clients) m.set(c.client_id, c);
    return m;
  }, [clients]);

  const connectedCount = useMemo(
    () => clients.filter((c) => c.injected).length,
    [clients],
  );

  const activeSessions = useMemo(
    () => sessions.filter((s) => s.status !== 'STOPPED' && s.status !== 'LOST'),
    [sessions],
  );

  const totalCU = summary.total_available_cu ?? 0;
  const usdValue = (summary.estimated_value_usd ?? totalCU * 0.1).toFixed(2);
  const g4Rate = summary.gpu_equivalence?.G4?.burn_rate_cu_hr || 9.66;
  const g4HoursTotal = (
    summary.gpu_equivalence?.G4?.available_hours ?? (g4Rate > 0 ? totalCU / g4Rate : 0)
  ).toFixed(1);
  const liveBurnRate = useMemo(() => {
    if ((summary.active_burn_rate_cu_hr ?? 0) > 0) return summary.active_burn_rate_cu_hr;
    return activeSessions.reduce((acc, s) => acc + (s.burn_rate_cu_hr || 0), 0);
  }, [activeSessions, summary.active_burn_rate_cu_hr]);

  // Handlers
  const handleToggleAgent = useCallback(
    async (clientId: AgentClientID, currentlyInjected: boolean) => {
      setTogglingClientId(clientId);
      setToast(null);
      try {
        const origin =
          typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860';
        if (currentlyInjected) {
          const updated = await api.removeAgentConfig(clientId);
          setClients((prev) => {
            const next = prev.map((c) => (c.client_id === updated.client_id ? updated : c));
            announceAgentConnected(next.some((c) => c.injected));
            return next;
          });
          setToast({
            tone: 'ok',
            text: zh ? `已断开 ${updated.display_name}` : `Disconnected ${updated.display_name}`,
          });
        } else {
          // Keyless localhost open-access default — never injects fake placeholder tokens
          const updated = await api.injectAgentConfig({
            client_id: clientId,
            server_base_url: origin,
            transport: clientId === 'claude-desktop' ? 'stdio' : 'http',
            include_wsl2: true,
          });
          setClients((prev) => {
            const exists = prev.some((c) => c.client_id === updated.client_id);
            const next = exists
              ? prev.map((c) => (c.client_id === updated.client_id ? updated : c))
              : [...prev, updated];
            if (updated.injected) announceAgentConnected(true);
            return next;
          });
          setToast({
            tone: 'ok',
            text: zh
              ? `已一键接入 ${updated.display_name}`
              : `Connected ${updated.display_name}`,
          });
        }
      } catch (err) {
        setToast({
          tone: 'err',
          text:
            err instanceof Error
              ? err.message
              : zh
                ? '写入配置失败，请确认本地服务正在运行'
                : 'Failed to update agent config',
        });
      } finally {
        setTogglingClientId(null);
      }
    },
    [zh],
  );

  const handleRefreshOneAccount = useCallback(async (accountId: string) => {
    setSyncingAccountId(accountId);
    try {
      const updated = await api.refreshAccountQuota(accountId);
      setAccounts((prev) => prev.map((a) => (a.id === updated.id ? updated : a)));
      const nextSum = await api.getQuotaSummary(false);
      setSummary(nextSum);
      setLastUpdatedAt(Date.now());
    } catch {
      // Ignore in offline snapshot preview
    } finally {
      setSyncingAccountId(null);
    }
  }, []);

  const handleOpenColab = useCallback(
    async (sess: RuntimeSession) => {
      setOpeningColabId(sess.id);
      try {
        const res = await api.getSessionWebUrl(sess.id).catch(() => null);
        const rawTarget = res?.url || sess.colab_web_url;
        const target = buildSmartColabWebUrl(rawTarget, sess.account_email);
        if (target && typeof window !== 'undefined') {
          window.open(target, '_blank', 'noopener,noreferrer');
        }
      } finally {
        setOpeningColabId(null);
      }
    },
    [],
  );

  const handleReleaseSession = useCallback(
    async (sess: RuntimeSession) => {
      if (sess.adopted_external && confirmExternalStopId !== sess.id) {
        setConfirmExternalStopId(sess.id);
        return;
      }
      setConfirmExternalStopId(null);
      setStoppingSessionId(sess.id);
      try {
        await api.stopSession(sess.id);
        setSessions((prev) => prev.filter((s) => s.id !== sess.id));
        const nextSum = await api.getQuotaSummary(false).catch(() => null);
        if (nextSum) setSummary(nextSum);
        setToast({
          tone: 'ok',
          text: zh ? `已释放实例 ${sess.notebook_name || sess.name}` : `Released ${sess.name}`,
        });
      } catch (err) {
        setToast({
          tone: 'err',
          text: err instanceof Error ? err.message : zh ? '释放实例失败' : 'Failed to release VM',
        });
      } finally {
        setStoppingSessionId(null);
      }
    },
    [confirmExternalStopId, zh],
  );

  const handleToggleAutostart = useCallback(async () => {
    const res = await triggerDesktopAction('toggle-autostart');
    if (typeof res.autostart === 'boolean') {
      setAutostart(res.autostart);
    } else if (res.error && !getWebView2Bridge()) {
      setToast({
        tone: 'err',
        text: res.error,
      });
    } else if (res.nativeHandled) {
      setAutostart((prev) => (prev === null ? true : !prev));
    }
  }, []);

  const handleMenuCopyMCP = useCallback(async () => {
    const origin =
      typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860';
    const mcpUrl = `${origin.replace(/\/$/, '')}/mcp`;
    try {
      if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(mcpUrl);
      }
    } catch {
      // Native desktop shell also copies via Win32 clipboard in `copy-mcp`.
    }
    setMenuCopiedMcp(true);
    window.setTimeout(() => setMenuCopiedMcp(false), 1600);
    await triggerDesktopAction('copy-mcp');
  }, []);

  const handleMenuSyncNow = useCallback(async () => {
    void syncAll(true);
    await triggerDesktopAction('sync-now');
  }, [syncAll]);

  const handleMenuOpenPanel = useCallback(async () => {
    const res = await triggerDesktopAction('show-tray');
    if (!res.nativeHandled || !getWebView2Bridge()) {
      setTrayMode('panel');
    }
  }, []);

  // Router 3-node hero flow endpoints
  const latestTrace = traces[0];
  const recentCallerLabel = useMemo(() => {
    if (latestTrace?.caller) return cleanCallerName(latestTrace.caller);
    const firstConnected = clients.find((c) => c.injected);
    if (firstConnected) return firstConnected.display_name;
    return 'Antigravity';
  }, [clients, latestTrace]);

  const activeRouteTarget = useMemo(() => {
    if (latestTrace?.account_email || latestTrace?.assigned_gpu) {
      const matchedAcc = accounts.find(
        (a) => a.id === latestTrace.account_id || a.email === latestTrace.account_email,
      );
      const accLabel = matchedAcc
        ? shortAccountTag(matchedAcc, displayEmail(matchedAcc.email))
        : latestTrace.account_email
          ? displayEmail(latestTrace.account_email).split('@')[0]
          : 'Pool';
      const gpu = latestTrace.assigned_gpu || latestTrace.requested_gpu || 'G4';
      return { label: `${accLabel} · ${gpu}`, active: true };
    }
    if (activeSessions[0]) {
      const s = activeSessions[0];
      const matchedAcc = accounts.find((a) => a.id === s.account_id);
      const accLabel = matchedAcc
        ? shortAccountTag(matchedAcc, displayEmail(s.account_email))
        : displayEmail(s.account_email).split('@')[0];
      return { label: `${accLabel} · ${s.accelerator}`, active: true };
    }
    if (accounts[0]) {
      const a = accounts[0];
      return {
        label: `${shortAccountTag(a, displayEmail(a.email))} · G4`,
        active: a.enabled,
      };
    }
    return { label: 'Colab Pool · G4', active: false };
  }, [accounts, activeSessions, displayEmail, latestTrace]);

  const isTrayGateRequired = Boolean(
    health?.auth_required && (authGateLocked || !getStoredAdminToken()),
  );
  const isCloudTray = health?.deployment_mode === 'cloud';

  // ============================================================================
  // Magpie Popover Surface (396×580 Native Flyout / Centered Browser Preview)
  // ============================================================================

  const popoverSurface = isTrayGateRequired ? (
    <div
      className={cn(
        'flex flex-col select-none overflow-hidden text-fg',
        isCompactViewport
          ? 'w-full h-screen'
          : 'w-[396px] h-[580px] max-h-[calc(100vh-24px)] rounded-[18px] border',
      )}
      style={{
        backgroundColor: 'var(--surface-0)',
        borderColor: isCompactViewport ? 'transparent' : 'var(--border-float)',
        boxShadow: isCompactViewport
          ? 'none'
          : '0 24px 64px -12px rgba(0, 0, 0, 0.16), 0 4px 16px -4px rgba(0, 0, 0, 0.06), 0 0 0 1px rgba(24, 24, 27, 0.06)',
      }}
    >
      <header className="h-11 px-4 flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <TrayBrandMark size={20} />
          <span className="text-[15px] font-semibold tracking-[-0.02em] text-fg truncate">
            colabhive
          </span>
        </div>
        <span
          className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10.5px] font-semibold"
          style={{
            backgroundColor: isCloudTray ? 'var(--sky-soft)' : 'var(--emerald-soft)',
            color: isCloudTray ? 'var(--sky-text)' : 'var(--emerald-text)',
          }}
        >
          {isCloudTray ? <Cloud className="w-3 h-3" /> : <ShieldCheck className="w-3 h-3" />}
          {isCloudTray
            ? zh
              ? '云端部署模式 · Cloud Server'
              : 'Cloud Server'
            : zh
              ? '本地安全模式'
              : 'Local Protected'}
        </span>
      </header>

      <main className="flex-1 flex flex-col justify-center px-4 py-4">
        <div
          className="rounded-2xl bg-[#FFFFFF] dark:bg-[#1E1E1C] border p-4 space-y-4"
          style={{
            borderColor: 'var(--glass-card-border)',
            boxShadow: '0 2px 8px rgba(0, 0, 0, 0.04)',
          }}
        >
          <div className="flex items-center gap-2.5">
            <span
              className="inline-flex items-center justify-center w-9 h-9 rounded-xl shrink-0"
              style={{ backgroundColor: 'var(--brand-bg)' }}
            >
              <Lock className="w-4 h-4 text-white" />
            </span>
            <div className="min-w-0">
              <h1 className="text-[14.5px] font-bold text-fg truncate">
                {zh ? 'ColabHive 管理控制台验证' : 'ColabHive Admin Access Gate'}
              </h1>
              <p className="text-[11.5px] text-fg-secondary">
                {zh ? '输入管理员口令解锁快捷面板' : 'Enter admin token to unlock'}
              </p>
            </div>
          </div>

          <form onSubmit={handleTrayGateSubmit} className="space-y-3">
            <div className="relative">
              <input
                type={gateShowToken ? 'text' : 'password'}
                value={gateTokenInput}
                onChange={(e) => {
                  setGateTokenInput(e.target.value);
                  if (gateError) setGateError(null);
                }}
                placeholder="COLABHIVE_ADMIN_TOKEN"
                autoComplete="current-password"
                spellCheck={false}
                className="ui-input ch-num h-9 pr-9 text-[12.5px] w-full"
              />
              <button
                type="button"
                onClick={() => setGateShowToken((v) => !v)}
                aria-label={gateShowToken ? 'Hide token' : 'Show token'}
                className="absolute right-1.5 top-1/2 -translate-y-1/2 inline-flex items-center justify-center w-6 h-6 rounded-md text-fg-muted hover:text-fg cursor-pointer bg-transparent border-0"
              >
                {gateShowToken ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
              </button>
            </div>

            {gateError && (
              <div
                role="alert"
                className="flex items-start gap-1.5 rounded-xl border px-3 py-2 text-[11.5px] leading-snug"
                style={{
                  backgroundColor: 'var(--coral-soft)',
                  borderColor: 'var(--coral-border)',
                  color: 'var(--coral-text)',
                }}
              >
                <AlertCircle className="w-3.5 h-3.5 shrink-0 mt-0.5" />
                <span>{gateError}</span>
              </div>
            )}

            <button
              type="submit"
              disabled={gateVerifying}
              className="ui-btn ui-btn-primary ui-btn-md w-full justify-center"
            >
              {gateVerifying ? (
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
              ) : (
                <ShieldCheck className="w-3.5 h-3.5" />
              )}
              <span>{zh ? '验证并进入控制台' : 'Verify & Unlock Console'}</span>
            </button>
          </form>
        </div>
      </main>

      <footer
        className="h-10 px-4 border-t flex items-center justify-between gap-2 shrink-0 bg-[#F7F7F5]/90 dark:bg-[#141413]/90 text-[11.5px] text-fg-muted"
        style={{ borderColor: 'var(--chrome-border)' }}
      >
        <span>AES-256-GCM · Bearer Auth</span>
        <button
          type="button"
          onClick={() => void triggerDesktopAction('open-main', '/')}
          className="font-medium text-fg-secondary hover:text-fg cursor-pointer bg-transparent border-0"
        >
          {zh ? '打开主窗口' : 'Open ColabHive'}
        </button>
      </footer>
    </div>
  ) : (
    <div
      className={cn(
        'flex flex-col select-none overflow-hidden text-fg',
        isCompactViewport
          ? 'w-full h-screen'
          : 'w-[396px] h-[580px] max-h-[calc(100vh-24px)] rounded-[18px] border',
      )}
      style={{
        backgroundColor: 'var(--surface-0)',
        borderColor: isCompactViewport ? 'transparent' : 'var(--border-float)',
        boxShadow: isCompactViewport
          ? 'none'
          : '0 24px 64px -12px rgba(0, 0, 0, 0.16), 0 4px 16px -4px rgba(0, 0, 0, 0.06), 0 0 0 1px rgba(24, 24, 27, 0.06)',
      }}
    >
      {/* -------------------------------------------------------------------- */}
      {/* 1. Top Header (`h-11 px-4`: `[icon colabhive | ↻ ⚙]`)                */}
      {/* -------------------------------------------------------------------- */}
      <header className="h-11 px-4 flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <TrayBrandMark size={20} />
          <span className="text-[15px] font-semibold tracking-[-0.02em] text-fg truncate">
            colabhive
          </span>
        </div>

        <div className="flex items-center gap-1 shrink-0">
          <button
            type="button"
            title={zh ? '立即同步额度与云端实例' : 'Sync quotas & cloud VMs'}
            aria-label={zh ? '同步额度与云端实例' : 'Sync quotas and cloud VMs'}
            disabled={syncing}
            onClick={() => void syncAll(true)}
            className="inline-flex items-center justify-center w-7 h-7 rounded-lg text-fg-muted hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer disabled:opacity-50"
          >
            <RefreshCw className={cn('w-[15px] h-[15px]', syncing && 'animate-spin')} />
          </button>
          <button
            type="button"
            title={zh ? '打开密钥与系统设置' : 'Open settings'}
            aria-label={zh ? '打开设置' : 'Open settings'}
            onClick={() => void triggerDesktopAction('open-route', '/cfg')}
            className="inline-flex items-center justify-center w-7 h-7 rounded-lg text-fg-muted hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer"
          >
            <Settings className="w-[15px] h-[15px]" />
          </button>
          {health?.auth_required && Boolean(getStoredAdminToken()) && (
            <button
              type="button"
              title={zh ? '退出登录并锁定面板' : 'Lock tray popover'}
              aria-label={zh ? '锁定面板' : 'Lock'}
              onClick={() => {
                clearStoredAdminToken();
                setAuthGateLocked(true);
              }}
              className="inline-flex items-center justify-center w-7 h-7 rounded-lg text-fg-muted hover:text-[var(--coral-text)] hover:bg-[var(--hover-fill)] transition-colors cursor-pointer"
            >
              <LogOut className="w-[14px] h-[14px]" />
            </button>
          )}
        </div>
      </header>

      {/* -------------------------------------------------------------------- */}
      {/* 2. 4-Tab Segmented Pill Bar (`px-3.5`)                               */}
      {/* -------------------------------------------------------------------- */}
      <div className="px-3.5 pb-2 shrink-0">
        <div
          role="tablist"
          aria-label="ColabHive Tray Navigation"
          className="grid grid-cols-4 gap-0.5 p-[3px] rounded-xl bg-[#EFEFED] dark:bg-[#181817]"
        >
          {(
            [
              {
                id: 'agents' as TrayTab,
                label: (
                  <>
                    <span>Agent</span>
                    <span className="ml-1 ch-num opacity-80">{connectedCount || 8}</span>
                  </>
                ),
              },
              {
                id: 'quota' as TrayTab,
                label: <span>{zh ? '额度' : 'Quota'}</span>,
              },
              {
                id: 'instances' as TrayTab,
                label: (
                  <>
                    <span>{zh ? '实例' : 'VMs'}</span>
                    <span className="ml-1 ch-num opacity-80">{activeSessions.length}</span>
                  </>
                ),
              },
              {
                id: 'router' as TrayTab,
                label: <span>{zh ? '路由' : 'Router'}</span>,
              },
            ] as const
          ).map((tab) => {
            const selected = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                aria-selected={selected}
                onClick={() => setActiveTab(tab.id)}
                className={cn(
                  'h-[30px] px-2 rounded-[9px] text-[12.5px] inline-flex items-center justify-center whitespace-nowrap transition-all duration-150 cursor-pointer',
                  selected
                    ? 'bg-[#FFFFFF] dark:bg-[#262624] font-semibold text-fg shadow-[0_1px_3px_rgba(0,0,0,0.08),0_1px_1px_rgba(0,0,0,0.04)]'
                    : 'font-medium text-fg-muted hover:text-fg',
                )}
              >
                {tab.label}
              </button>
            );
          })}
        </div>
      </div>

      {/* Transient Action Toast */}
      {toast && (
        <div className="px-3.5 pb-1.5 shrink-0">
          <div
            data-tone={toast.tone === 'ok' ? 'emerald' : 'coral'}
            className="px-3 py-1.5 rounded-xl border text-[11.5px] font-medium flex items-center gap-1.5"
            style={{
              backgroundColor: 'var(--tone-soft)',
              borderColor: 'var(--tone-border)',
              color: 'var(--tone-fg)',
            }}
          >
            <CheckCircle2 className="w-3.5 h-3.5 shrink-0" />
            <span className="truncate">{toast.text}</span>
          </div>
        </div>
      )}

      {/* -------------------------------------------------------------------- */}
      {/* 3. Scrollable Tab Body                                               */}
      {/* -------------------------------------------------------------------- */}
      <main className="flex-1 min-h-0 overflow-y-auto px-3.5 py-1.5 space-y-3.5">
        {/* ================================================================== */}
        {/* TAB 1: `Agent N` — 1-Click `cc-switch` MCP Toggle List             */}
        {/* ================================================================== */}
        {activeTab === 'agents' && (
          <div className="space-y-2.5 pb-1">
            <div className="flex items-center justify-between px-0.5 text-[12px]">
              <div className="flex items-center gap-1.5 text-fg-secondary">
                <span
                  className="w-2 h-2 rounded-full shrink-0"
                  style={{
                    backgroundColor: connectedCount > 0 ? 'var(--emerald)' : 'var(--text-muted)',
                  }}
                />
                <span>
                  {zh ? '已接入 ' : 'Connected '}
                  <strong className="ch-num font-semibold text-fg">{connectedCount}</strong>
                  {zh ? ' / 8 · 本机免密直连' : ' / 8 · Keyless Local'}
                </span>
              </div>
              <button
                type="button"
                onClick={() => void triggerDesktopAction('open-route', '/router')}
                className="text-[11.5px] font-medium text-fg-secondary hover:text-fg cursor-pointer"
              >
                {zh ? '高级设置' : 'Configure'}
              </button>
            </div>

            <div
              className="rounded-2xl bg-[#FFFFFF] dark:bg-[#1E1E1C] border divide-y"
              style={{
                borderColor: 'var(--glass-card-border)',
                boxShadow: '0 1px 3px rgba(0, 0, 0, 0.025)',
              }}
            >
              {AGENT_CLIENT_ORDER.map((item) => {
                const st = clientById.get(item.id);
                const injected = Boolean(st?.injected);
                const detected = Boolean(st?.detected);
                const transport = item.id === 'claude-desktop' ? 'stdio' : (st?.transport ?? 'http');
                const busy = togglingClientId === item.id;

                return (
                  <div
                    key={item.id}
                    style={{ borderColor: 'var(--border-faint)' }}
                    className="px-3.5 py-2.5 flex items-center justify-between gap-2.5"
                  >
                    <div className="flex items-center gap-2.5 min-w-0">
                      <span
                        className="inline-flex items-center justify-center w-7 h-7 rounded-[8px] border shrink-0 bg-[#FFFFFF] dark:bg-[#262624] text-fg"
                        style={{
                          borderColor: 'var(--border-subtle)',
                          boxShadow: 'var(--shadow-control)',
                        }}
                      >
                        <AgentBrandIcon id={item.id} size={17} />
                      </span>
                      <div className="min-w-0">
                        <div className="flex items-center gap-1.5">
                          <span className="text-[13px] font-semibold text-fg truncate">
                            {item.label}
                          </span>
                          {detected && !injected && (
                            <span
                              className="w-1.5 h-1.5 rounded-full shrink-0"
                              title={zh ? '已检测到本机配置目录' : 'Detected on host'}
                              style={{ backgroundColor: 'var(--sky)' }}
                            />
                          )}
                        </div>
                        <div className="ch-num text-[10.5px] text-fg-muted truncate">
                          {st ? shortConfigHint(st.config_path, transport) : transport.toUpperCase()}
                        </div>
                      </div>
                    </div>

                    <div className="flex items-center gap-2 shrink-0">
                      {busy ? (
                        <Loader2 className="w-3.5 h-3.5 animate-spin text-fg-muted" />
                      ) : injected ? (
                        <span
                          className="px-1.5 py-0.5 rounded-md text-[10.5px] font-medium"
                          style={{
                            backgroundColor: 'var(--emerald-soft)',
                            color: 'var(--emerald-text)',
                          }}
                        >
                          {zh ? '已接入' : 'Active'}
                        </span>
                      ) : (
                        <button
                          type="button"
                          onClick={() => void handleToggleAgent(item.id, false)}
                          className="px-2 py-0.5 rounded-md text-[10.5px] font-medium border transition-colors hover:bg-[var(--hover-fill)] cursor-pointer"
                          style={{
                            borderColor: 'var(--control-border)',
                            color: 'var(--text-secondary)',
                          }}
                        >
                          {zh ? '一键接入' : 'Connect'}
                        </button>
                      )}
                      <Switch
                        checked={injected}
                        disabled={busy}
                        onChange={() => void handleToggleAgent(item.id, injected)}
                        label={`${item.label} MCP`}
                      />
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {/* ================================================================== */}
        {/* TAB 2: `额度` — Modeled 1:1 on Magpie Screenshot 1                 */}
        {/* ================================================================== */}
        {activeTab === 'quota' && (
          <div className="space-y-3.5 pb-1">
            {/* Section 1: 池内总余量 (Summary Balance Card) */}
            <section className="space-y-1.5">
              <div className="px-0.5 text-[11.5px] font-semibold text-fg-muted">
                {zh ? '池内总余量' : 'Pool Balance'}
              </div>
              <div
                className="rounded-2xl bg-[#FFFFFF] dark:bg-[#1E1E1C] border px-4 py-3"
                style={{
                  borderColor: 'var(--glass-card-border)',
                  boxShadow: '0 1px 3px rgba(0, 0, 0, 0.025)',
                }}
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-1.5 text-[12px] font-medium text-fg-secondary">
                    <Box className="w-3.5 h-3.5 text-fg-muted shrink-0" />
                    <span>
                      {zh
                        ? `Colab 算力池 · ${accounts.length} 个账号`
                        : `Colab Pool · ${accounts.length} accounts`}
                    </span>
                  </div>
                  {liveBurnRate > 0 ? (
                    <span
                      className="ch-num px-1.5 py-0.5 rounded-md text-[10.5px] font-medium"
                      style={{
                        backgroundColor: 'var(--amber-soft)',
                        color: 'var(--amber-text)',
                      }}
                    >
                      -{formatCU(liveBurnRate, 2)} CU/h
                    </span>
                  ) : (
                    <span
                      className="px-1.5 py-0.5 rounded-md text-[10.5px] font-medium"
                      style={{
                        backgroundColor: 'var(--neutral-soft)',
                        color: 'var(--text-muted)',
                      }}
                    >
                      {zh ? '空闲零消耗' : 'Idle · 0 CU/h'}
                    </span>
                  )}
                </div>

                <div className="mt-1.5 flex items-baseline justify-between gap-2">
                  <div className="ch-num text-[21px] font-bold tracking-tight text-fg leading-tight">
                    {formatCU(totalCU, 2)} <span className="text-[13px] font-semibold">CU</span>
                  </div>
                </div>

                <div className="mt-0.5 ch-num text-[11.5px] text-fg-muted">
                  {zh
                    ? `≈ $${usdValue} · 可跑 ${g4HoursTotal} 小时 G4`
                    : `≈ $${usdValue} · ~${g4HoursTotal}h on G4`}
                </div>
              </div>
            </section>

            {/* Per-Account Sections */}
            {accounts.map((acc) => {
              const maskedMail = displayEmail(acc.email);
              const tag = shortAccountTag(acc, maskedMail);

              const cap = resolveAccountCycleCap(acc);
              const usable = acc.is_contributor ? acc.schedulable_cu : acc.paid_cu_balance;
              const isFreeOnly = acc.subscription_tier === 'FREE' && acc.paid_cu_balance <= 0;
              const freeCu = acc.free_quota?.remaining_free_cu ?? 0;
              const quotaPct = isFreeOnly
                ? Math.min(100, Math.round((freeCu / 15) * 100))
                : Math.max(0, Math.min(100, Math.round((usable / cap) * 100)));
              const ringTone: QuotaRingTone = deriveRingTone(acc, quotaPct);

              const maxSlots = Math.max(1, acc.max_concurrent_vms || 2);
              const activeSlots = Math.max(0, acc.active_assignments || 0);
              const slotPct = Math.min(100, Math.round((activeSlots / maxSlots) * 100));

              const accG4Hours = (
                acc.gpu_equivalence?.G4?.schedulable_hours ??
                (g4Rate > 0 ? Math.max(0, usable) / g4Rate : 0)
              ).toFixed(1);

              const isRunning = activeSlots > 0 || acc.health === 'ACTIVE_WORKING';
              const isError =
                acc.health === 'AUTH_EXPIRED' ||
                acc.health === 'PROXY_ERROR' ||
                acc.health === 'QUARANTINED_DENYLIST';
              const isCooldown =
                acc.health === 'COOLDOWN_RATE_LIMIT' || acc.health === 'MAX_CONCURRENCY';

              const statusBadge = isError
                ? {
                    label: zh ? '需处理' : 'Alert',
                    bg: 'var(--coral-soft)',
                    fg: 'var(--coral-text)',
                  }
                : isCooldown
                  ? {
                      label: zh ? '冷却中' : 'Cooldown',
                      bg: 'var(--amber-soft)',
                      fg: 'var(--amber-text)',
                    }
                  : isRunning
                    ? {
                        label: zh ? '运行中' : 'Running',
                        bg: 'var(--emerald-soft)',
                        fg: 'var(--emerald-text)',
                      }
                    : {
                        label: zh ? '就绪' : 'Ready',
                        bg: 'var(--neutral-soft)',
                        fg: 'var(--text-secondary)',
                      };

              return (
                <section key={acc.id} className="space-y-1.5">
                  {/* Eyebrow Row: `[● AI ULTRA · LAB-ULTRA | expiry/burn | 运行中]` */}
                  <div className="flex items-center justify-between gap-2 px-0.5">
                    <div className="flex items-center gap-1.5 min-w-0">
                      <span
                        className="w-2.5 h-2.5 rounded-[3px] shrink-0"
                        style={{
                          backgroundColor: tierDotColor(acc.subscription_tier),
                        }}
                      />
                      <span className="text-[11px] font-semibold tracking-[0.04em] uppercase text-fg-muted truncate">
                        {`${tierEyebrowLabel(acc.subscription_tier)} · ${tag}`}
                      </span>
                    </div>

                    <div className="flex items-center gap-1.5 shrink-0">
                      {acc.expiring_soon_cu > 0 && acc.earliest_batch_expires_at ? (
                        <span
                          className="ch-num text-[11px] font-medium"
                          style={{ color: 'var(--amber-text)' }}
                        >
                          {formatCU(acc.expiring_soon_cu, 0)} CU ·{' '}
                          {formatRelativeCountdown(acc.earliest_batch_expires_at, locale)}
                          {zh ? '到期' : ''}
                        </span>
                      ) : acc.live_burn_rate_cu_hr > 0 ? (
                        <span
                          className="ch-num text-[11px] font-medium"
                          style={{ color: 'var(--amber-text)' }}
                        >
                          -{formatCU(acc.live_burn_rate_cu_hr, 2)} CU/h
                        </span>
                      ) : acc.cycle_resets_at ? (
                        <span className="ch-num text-[11px] text-fg-muted">
                          {formatRelativeCountdown(acc.cycle_resets_at, locale)}
                          {zh ? '重置' : ' reset'}
                        </span>
                      ) : null}

                      <span
                        className="px-1.5 py-0.5 rounded-md text-[10.5px] font-medium leading-none"
                        style={{
                          backgroundColor: statusBadge.bg,
                          color: statusBadge.fg,
                        }}
                      >
                        {statusBadge.label}
                      </span>
                    </div>
                  </div>

                  {/* Crisp White Account Card */}
                  <div
                    className="rounded-2xl bg-[#FFFFFF] dark:bg-[#1E1E1C] border px-4 pt-3.5 pb-2.5"
                    style={{
                      borderColor: 'var(--glass-card-border)',
                      boxShadow: '0 1px 3px rgba(0, 0, 0, 0.025)',
                    }}
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0 flex-1 pt-0.5">
                        <div className="text-[14px] font-semibold text-fg truncate">
                          {acc.display_name || maskedMail}
                        </div>
                        <div className="mt-1.5 ch-num text-[12px] text-fg-secondary truncate">
                          {isFreeOnly
                            ? zh
                              ? `免费动态池 · 剩余 ${formatCU(freeCu, 1)} CU`
                              : `Free dynamic pool · ${formatCU(freeCu, 1)} CU`
                            : zh
                              ? `剩余 ${formatCU(usable, 2)} / ${formatCU(cap, 0)} CU`
                              : `${formatCU(usable, 2)} / ${formatCU(cap, 0)} CU left`}
                        </div>
                      </div>

                      {/* 2 Crisp Magpie Circular Quota Rings (`余量 %` + `并发 N/M`) */}
                      <div className="flex items-start gap-2.5 shrink-0">
                        <div className="flex flex-col items-center">
                          <CircularQuotaRing
                            value={quotaPct}
                            size={44}
                            strokeWidth={3.5}
                            tone={ringTone}
                          />
                          <span className="mt-1 text-[10.5px] text-fg-muted leading-none">
                            {zh ? '余量' : 'Quota'}
                          </span>
                        </div>

                        <div className="flex flex-col items-center">
                          <CircularQuotaRing
                            value={slotPct}
                            size={44}
                            strokeWidth={3.5}
                            tone={activeSlots > 0 ? 'sky' : 'neutral'}
                            label={`${activeSlots}/${maxSlots}`}
                          />
                          <span className="mt-1 text-[10.5px] text-fg-muted leading-none">
                            {zh ? '并发' : 'Slots'}
                          </span>
                        </div>
                      </div>
                    </div>

                    {/* Clean Horizontal Progress / Pace Line */}
                    <div className="mt-2.5 h-[3px] w-full rounded-full overflow-hidden bg-black/[0.06] dark:bg-white/[0.08]">
                      <div
                        className="h-full rounded-full transition-all duration-500"
                        style={{
                          width: `${Math.max(quotaPct > 0 ? 3 : 0, quotaPct)}%`,
                          backgroundColor: barColorForTone(ringTone),
                        }}
                      />
                    </div>

                    {/* Hairline Footer (`↻ 在跑 N 台实例 · 约可跑 X 小时 G4`) */}
                    <div
                      className="mt-2.5 pt-2 border-t flex items-center justify-between gap-2 text-[11.5px] text-fg-secondary"
                      style={{ borderColor: 'var(--border-faint)' }}
                    >
                      <div className="flex items-center gap-1.5 min-w-0 truncate ch-num">
                        <button
                          type="button"
                          title={zh ? '刷新此账号余额' : 'Refresh account quota'}
                          onClick={() => void handleRefreshOneAccount(acc.id)}
                          className="text-fg-muted hover:text-fg transition-colors cursor-pointer shrink-0"
                        >
                          <RefreshCw
                            className={cn(
                              'w-3 h-3',
                              syncingAccountId === acc.id && 'animate-spin',
                            )}
                          />
                        </button>
                        <span className="truncate">
                          {zh
                            ? `在跑 ${activeSlots} 台实例 · 约可跑 ${accG4Hours} 小时 G4`
                            : `${activeSlots} active VM · ~${accG4Hours}h G4`}
                        </span>
                      </div>

                      {acc.is_contributor && (
                        <span className="text-[10.5px] text-fg-muted shrink-0">
                          {zh ? '好友共享' : 'Shared'}
                        </span>
                      )}
                    </div>
                  </div>
                </section>
              );
            })}
          </div>
        )}

        {/* ================================================================== */}
        {/* TAB 3: `实例 N` — Active Colab Cloud VMs & Synced External Tasks   */}
        {/* ================================================================== */}
        {activeTab === 'instances' && (
          <div className="space-y-2.5 pb-1">
            <div className="flex items-center justify-between px-0.5 text-[12px]">
              <div className="flex items-center gap-1.5 text-fg-secondary">
                <span
                  className="w-2 h-2 rounded-full shrink-0"
                  style={{
                    backgroundColor:
                      activeSessions.length > 0 ? 'var(--emerald)' : 'var(--text-muted)',
                  }}
                />
                <span>
                  {zh ? '在跑 ' : 'Active '}
                  <strong className="ch-num font-semibold text-fg">{activeSessions.length}</strong>
                  {zh ? ' 台 · 消耗 ' : ' VMs · '}
                  <strong className="ch-num font-semibold text-fg">
                    {formatCU(liveBurnRate, 2)}
                  </strong>
                  {' CU/h'}
                </span>
              </div>

              <button
                type="button"
                disabled={syncing}
                onClick={() => void syncAll(true)}
                className="inline-flex items-center gap-1 text-[11.5px] font-medium text-fg-secondary hover:text-fg cursor-pointer disabled:opacity-50"
              >
                <RefreshCw className={cn('w-3 h-3', syncing && 'animate-spin')} />
                <span>{zh ? '同步云端' : 'Sync Cloud'}</span>
              </button>
            </div>

            {activeSessions.length === 0 ? (
              <div
                className="rounded-2xl bg-[#FFFFFF] dark:bg-[#1E1E1C] border px-4 py-7 text-center space-y-2.5"
                style={{
                  borderColor: 'var(--glass-card-border)',
                  boxShadow: '0 1px 3px rgba(0, 0, 0, 0.025)',
                }}
              >
                <div
                  className="mx-auto w-9 h-9 rounded-xl flex items-center justify-center"
                  style={{
                    backgroundColor: 'var(--neutral-soft)',
                    color: 'var(--text-muted)',
                  }}
                >
                  <Cpu className="w-4 h-4" />
                </div>
                <div className="text-[13px] font-semibold text-fg">
                  {zh ? '当前没有运行中的云端实例' : 'No active cloud VMs'}
                </div>
                <p className="text-[11.5px] text-fg-muted leading-relaxed">
                  {zh
                    ? 'AI 助手调用 execute_code 时将自动分配最优显卡，也可手动同步网页端已开启的 Colab 实例。'
                    : 'VMs provision automatically when an agent runs code, or sync existing Colab notebooks.'}
                </p>
                <div className="pt-1 flex items-center justify-center gap-2">
                  <button
                    type="button"
                    onClick={() => void syncAll(true)}
                    className="ui-btn ui-btn-secondary ui-btn-xs"
                  >
                    <RefreshCw className={cn('w-3 h-3', syncing && 'animate-spin')} />
                    <span>{zh ? '扫描云端实例' : 'Scan Cloud'}</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => void triggerDesktopAction('open-route', '/exp')}
                    className="ui-btn ui-btn-primary ui-btn-xs"
                  >
                    <Zap className="w-3 h-3" />
                    <span>{zh ? '打开工作台' : 'Workbench'}</span>
                  </button>
                </div>
              </div>
            ) : (
              activeSessions.map((sess) => {
                const gpu = sess.resources?.gpus?.[0];
                const vramUsedGb = gpu
                  ? gpu.memory_used_bytes / 1_073_741_824
                  : 0;
                const vramTotalGb = gpu
                  ? gpu.memory_total_bytes / 1_073_741_824
                  : 0;
                const vramPct =
                  vramTotalGb > 0
                    ? Math.min(100, Math.round((vramUsedGb / vramTotalGb) * 100))
                    : 0;
                const gpuUtilPct = normalizeUtilPct(gpu?.gpu_utilization);
                const confirmingStop = confirmExternalStopId === sess.id;
                const stopping = stoppingSessionId === sess.id;

                return (
                  <div
                    key={sess.id}
                    className="rounded-2xl bg-[#FFFFFF] dark:bg-[#1E1E1C] border px-3.5 py-3 space-y-2.5"
                    style={{
                      borderColor: 'var(--glass-card-border)',
                      boxShadow: '0 1px 3px rgba(0, 0, 0, 0.025)',
                    }}
                  >
                    {/* Header: GPU Badge + Session Name + Burn Rate */}
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-1.5 min-w-0">
                        <HardwareTierBadge
                          accelerator={(sess.accelerator || 'G4') as AcceleratorType}
                          size="xs"
                        />
                        <span className="text-[13px] font-semibold text-fg truncate">
                          {sess.notebook_name || sess.name}
                        </span>
                        {sess.adopted_external && (
                          <span
                            className="px-1.5 py-0.5 rounded text-[10px] font-medium shrink-0"
                            style={{
                              backgroundColor: 'var(--sky-soft)',
                              color: 'var(--sky-text)',
                            }}
                          >
                            {zh ? '云端同步' : 'External'}
                          </span>
                        )}
                      </div>

                      <span
                        className="ch-num text-[11px] font-semibold shrink-0"
                        style={{ color: 'var(--amber-text)' }}
                      >
                        -{formatCU(sess.burn_rate_cu_hr, 2)} CU/h
                      </span>
                    </div>

                    {/* Account & Uptime Meta */}
                    <div className="flex items-center justify-between gap-2 text-[11px] text-fg-muted ch-num">
                      <span className="truncate">{displayEmail(sess.account_email)}</span>
                      <span className="shrink-0">
                        {formatElapsedSec(sess.uptime_sec)} ·{' '}
                        {sess.status === 'BUSY'
                          ? zh
                            ? '执行中'
                            : 'Busy'
                          : zh
                            ? '热备就绪'
                            : 'Warm'}
                      </span>
                    </div>

                    {/* Live VRAM & GPU% Bars */}
                    <div className="grid grid-cols-2 gap-2.5 pt-0.5">
                      <div className="space-y-1">
                        <div className="flex items-center justify-between text-[10.5px] ch-num text-fg-secondary">
                          <span>VRAM</span>
                          <span>
                            {vramTotalGb > 0
                              ? `${vramUsedGb.toFixed(1)}/${vramTotalGb.toFixed(0)}G`
                              : '—'}
                          </span>
                        </div>
                        <div className="h-1.5 rounded-full overflow-hidden bg-black/[0.06] dark:bg-white/[0.08]">
                          <div
                            className="h-full rounded-full transition-all duration-300"
                            style={{
                              width: `${vramPct}%`,
                              backgroundColor: 'var(--sky)',
                            }}
                          />
                        </div>
                      </div>

                      <div className="space-y-1">
                        <div className="flex items-center justify-between text-[10.5px] ch-num text-fg-secondary">
                          <span>GPU</span>
                          <span>
                            {gpuUtilPct}%
                            {gpu?.temperature_c ? ` · ${gpu.temperature_c}°C` : ''}
                          </span>
                        </div>
                        <div className="h-1.5 rounded-full overflow-hidden bg-black/[0.06] dark:bg-white/[0.08]">
                          <div
                            className="h-full rounded-full transition-all duration-300"
                            style={{
                              width: `${gpuUtilPct}%`,
                              backgroundColor:
                                gpuUtilPct > 85 ? 'var(--emerald)' : 'var(--emerald)',
                            }}
                          />
                        </div>
                      </div>
                    </div>

                    {/* 1-Click Action Footer: `工作台免登录查看` · `Colab 打开 (仅本人 Colab 号)` · `释放` */}
                    {(() => {
                      const sessAccount = findSessionAccount(sess, accounts);
                      const colabAccess = canOpenColabWebForSession(sess, sessAccount);
                      return (
                        <div
                          className="pt-2 border-t flex items-center justify-between gap-2 text-[11.5px]"
                          style={{ borderColor: 'var(--border-faint)' }}
                        >
                          <div className="flex items-center gap-2.5 min-w-0">
                            <button
                              type="button"
                              onClick={() =>
                                void triggerDesktopAction(
                                  'open-route',
                                  sess.experiment_id
                                    ? `/exp/${encodeURIComponent(sess.experiment_id)}`
                                    : `/exp/${encodeURIComponent(sess.id)}`,
                                )
                              }
                              title={
                                zh
                                  ? '在内置实验工作台免登录查看实时日志、执行代码与管理文件'
                                  : 'Open in built-in Workbench (cookie-free logs, code & files)'
                              }
                              className="inline-flex items-center gap-1 font-medium text-fg-secondary hover:text-fg cursor-pointer shrink-0"
                            >
                              <ArrowUpRight className="w-3 h-3" />
                              <span>{zh ? '工作台查看' : 'Workbench'}</span>
                            </button>

                            {colabAccess.allowed ? (
                              <button
                                type="button"
                                disabled={openingColabId === sess.id}
                                onClick={() => void handleOpenColab(sess)}
                                title={
                                  zh
                                    ? `需本机浏览器已登录 ${displayEmail(sess.account_email)}（自动附带 authuser）；未登录请点「工作台查看」免登录操作`
                                    : `Requires browser signed into ${displayEmail(sess.account_email)} (auto-appends authuser); otherwise use Workbench`
                                }
                                className="inline-flex items-center gap-1 font-medium text-fg-muted hover:text-fg cursor-pointer disabled:opacity-50 shrink-0"
                              >
                                <ExternalLink className="w-3 h-3" />
                                <span>{zh ? 'Colab 网页' : 'Colab Web'}</span>
                              </button>
                            ) : colabAccess.reason === 'contributor' ? (
                              <span
                                title={
                                  zh
                                    ? '好友授权共享账号不支持外部 Colab 网页登录，请点左侧「工作台查看」免登录操作'
                                    : 'Friend-shared OAuth account — use Workbench for cookie-free access'
                                }
                                className="inline-flex items-center gap-1 text-[10.5px] text-fg-muted truncate"
                              >
                                <ShieldCheck className="w-3 h-3 shrink-0" />
                                <span className="truncate">{zh ? '共享免登录' : 'Shared'}</span>
                              </span>
                            ) : null}
                          </div>

                          <button
                            type="button"
                            disabled={stopping}
                            onClick={() => void handleReleaseSession(sess)}
                            onBlur={() => {
                              if (confirmingStop) setConfirmExternalStopId(null);
                            }}
                            className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md font-medium transition-colors cursor-pointer disabled:opacity-50 shrink-0"
                            style={{
                              backgroundColor: confirmingStop
                                ? 'var(--coral)'
                                : 'var(--coral-soft)',
                              color: confirmingStop ? '#ffffff' : 'var(--coral-text)',
                            }}
                          >
                            <Power className="w-3 h-3" />
                            <span>
                              {stopping
                                ? zh
                                  ? '释放中…'
                                  : 'Stopping…'
                                : confirmingStop
                                  ? zh
                                    ? '确认释放?'
                                    : 'Confirm?'
                                  : zh
                                    ? '释放'
                                    : 'Release'}
                            </span>
                          </button>
                        </div>
                      );
                    })()}
                  </div>
                );
              })
            )}
          </div>
        )}

        {/* ================================================================== */}
        {/* TAB 4: `路由` — Modeled 1:1 on Magpie Screenshot 2                 */}
        {/* ================================================================== */}
        {activeTab === 'router' && (
          <div className="space-y-3 pb-1">
            {/* Subheader: `● 累计 N 次调用 · 实时消耗 X.XX CU/h | 打开路由` */}
            <div className="flex items-center justify-between gap-2 px-0.5 text-[12px]">
              <div className="flex items-center gap-1.5 min-w-0 text-fg-secondary truncate">
                <span
                  className="w-2 h-2 rounded-full shrink-0"
                  style={{ backgroundColor: 'var(--emerald)' }}
                />
                <span className="truncate">
                  {zh ? '累计 ' : 'Total '}
                  <strong className="ch-num font-semibold text-fg">{traces.length}</strong>
                  {zh ? ' 次调用 · 实时消耗 ' : ' calls · '}
                  <strong className="ch-num font-semibold text-fg">
                    {formatCU(liveBurnRate, 2)}
                  </strong>
                  {' CU/h'}
                </span>
              </div>

              <button
                type="button"
                onClick={() => void triggerDesktopAction('open-route', '/router')}
                className="text-[12px] font-medium text-fg-secondary hover:text-fg shrink-0 cursor-pointer"
              >
                {zh ? '打开路由' : 'Open Router'}
              </button>
            </div>

            {/* 3-Node Center Pill Flow Card (`[Caller] ── (🐝) ── [● Account · GPU]`) */}
            <div
              className="rounded-2xl bg-[#FFFFFF] dark:bg-[#1E1E1C] border px-3.5 py-5"
              style={{
                borderColor: 'var(--glass-card-border)',
                boxShadow: '0 1px 3px rgba(0, 0, 0, 0.025)',
              }}
            >
              <div className="flex items-center justify-between gap-1.5">
                {/* Left Node: Recent Agent Caller Pill */}
                <div
                  className="inline-flex items-center gap-1.5 h-7 px-2.5 rounded-full border max-w-[134px] min-w-0"
                  style={{
                    backgroundColor: 'var(--surface-0)',
                    borderColor: 'var(--border-subtle)',
                    boxShadow: '0 1px 2px rgba(0, 0, 0, 0.03)',
                  }}
                >
                  <span
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ backgroundColor: 'var(--sky)' }}
                  />
                  <span className="text-[11.5px] font-semibold text-fg truncate">
                    {recentCallerLabel}
                  </span>
                </div>

                {/* Connector Line 1 */}
                <div
                  className="flex-1 h-px min-w-[10px]"
                  style={{ backgroundColor: 'var(--border-strong)' }}
                />

                {/* Center Hub Circle (ColabHive Bee/Hexagon Emblem) */}
                <HiveHubEmblem />

                {/* Connector Line 2 */}
                <div
                  className="flex-1 h-px min-w-[10px]"
                  style={{ backgroundColor: 'var(--border-strong)' }}
                />

                {/* Right Node: Active Account · GPU Pill */}
                <div
                  className="inline-flex items-center gap-1.5 h-7 px-2.5 rounded-full border max-w-[138px] min-w-0"
                  style={{
                    backgroundColor: 'var(--surface-0)',
                    borderColor: 'var(--border-subtle)',
                    boxShadow: '0 1px 2px rgba(0, 0, 0, 0.03)',
                  }}
                >
                  <span
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{
                      backgroundColor: activeRouteTarget.active
                        ? 'var(--emerald)'
                        : 'var(--text-muted)',
                    }}
                  />
                  <span className="ch-num text-[11.5px] font-semibold text-fg truncate">
                    {activeRouteTarget.label}
                  </span>
                </div>
              </div>
            </div>

            {/* Live Scrolling Call Feed Card (Exact 2-Line Magpie Trace Rows) */}
            <div
              className="rounded-2xl bg-[#FFFFFF] dark:bg-[#1E1E1C] border divide-y"
              style={{
                borderColor: 'var(--glass-card-border)',
                boxShadow: '0 1px 3px rgba(0, 0, 0, 0.025)',
              }}
            >
              {traces.length === 0 ? (
                <div className="px-4 py-6 text-center text-[12px] text-fg-muted">
                  {zh
                    ? '暂无实时调用记录。AI 助手发起工具调用后将在此实时滚动显示。'
                    : 'No tool calls yet. Live MCP invocations stream here automatically.'}
                </div>
              ) : (
                traces.slice(0, 25).map((tr) => {
                  const caller = cleanCallerName(tr.caller);
                  const tool = cleanToolName(tr.tool_or_endpoint);
                  const accText = tr.account_email
                    ? displayEmail(tr.account_email).split('@')[0]
                    : 'pool';
                  const gpuText = tr.assigned_gpu || tr.requested_gpu || 'CPU';
                  const statusLabel =
                    tr.status === 'error'
                      ? zh
                        ? '失败'
                        : 'Error'
                      : tr.status === 'fallback'
                        ? zh
                          ? '自动换卡'
                          : 'Failover'
                        : zh
                          ? '成功'
                          : 'OK';
                  const statusColor =
                    tr.status === 'error'
                      ? 'var(--coral-text)'
                      : tr.status === 'fallback'
                        ? 'var(--amber-text)'
                        : 'var(--text-muted)';

                  return (
                    <div
                      key={tr.id}
                      style={{ borderColor: 'var(--border-faint)' }}
                      className="px-3.5 py-2.5 space-y-1"
                    >
                      {/* Row 1: Caller Icon + Caller Name + `[tool_name]` Grey Monospace Pill + HH:mm:ss */}
                      <div className="flex items-center gap-1.5 min-w-0">
                        <span
                          className="inline-flex items-center justify-center w-4 h-4 rounded-full shrink-0"
                          style={{
                            backgroundColor: 'var(--sky-soft)',
                            color: 'var(--sky-text)',
                          }}
                        >
                          <Zap className="w-2.5 h-2.5" />
                        </span>
                        <span className="text-[12.5px] font-semibold text-fg shrink-0 max-w-[108px] truncate">
                          {caller}
                        </span>
                        <span className="ch-num px-1.5 py-0.5 rounded-[5px] bg-[#F2F2F0] dark:bg-[#282826] text-[11px] text-fg-secondary truncate min-w-0">
                          {tool}
                        </span>
                        <span className="ch-num text-[11px] text-fg-muted ml-auto shrink-0">
                          {formatTraceClock(tr.timestamp)}
                        </span>
                      </div>

                      {/* Row 2: `→ account · GPU` on Left + `duration · status` on Right */}
                      <div className="flex items-center justify-between gap-2 pl-0.5 text-[11.5px] text-fg-muted">
                        <span className="ch-num truncate">
                          → {accText} · {gpuText}
                        </span>
                        <span className="ch-num shrink-0" style={{ color: statusColor }}>
                          {formatDurationMs(tr.duration_ms)} · {statusLabel}
                        </span>
                      </div>
                    </div>
                  );
                })
              )}
            </div>
          </div>
        )}
      </main>

      {/* -------------------------------------------------------------------- */}
      {/* 4. Pinned Bottom Footer Bar (`h-10 px-4 border-t`)                   */}
      {/* -------------------------------------------------------------------- */}
      <footer
        className="h-10 px-4 border-t flex items-center justify-between gap-2 shrink-0 bg-[#F7F7F5]/90 dark:bg-[#141413]/90"
        style={{ borderColor: 'var(--chrome-border)' }}
      >
        <div className="flex items-center gap-2 min-w-0 text-[12px] text-fg-muted">
          <span className="truncate">
            {syncing
              ? zh
                ? '正在同步…'
                : 'Syncing…'
              : formatUpdatedAgo(lastUpdatedAt, nowMs, zh)}
          </span>
          {autostart !== null && (
            <button
              type="button"
              onClick={() => void handleToggleAutostart()}
              title={zh ? '切换开机自启' : 'Toggle launch at login'}
              className="px-1.5 py-0.5 rounded text-[10.5px] font-medium transition-colors hover:bg-[var(--hover-fill)] cursor-pointer shrink-0"
              style={{
                color: autostart ? 'var(--emerald-text)' : 'var(--text-muted)',
              }}
            >
              {autostart ? (zh ? '自启已开' : 'Autostart On') : zh ? '开机自启' : 'Autostart'}
            </button>
          )}
        </div>

        <div className="flex items-center gap-3 shrink-0">
          <button
            type="button"
            onClick={() => void triggerDesktopAction('open-main', '/')}
            className="text-[12.5px] font-medium text-fg-secondary hover:text-fg transition-colors cursor-pointer"
          >
            {zh ? '打开主窗口' : 'Open ColabHive'}
          </button>
          <button
            type="button"
            onClick={() => {
              if (embedded || isDemoSiteMode()) {
                setToast({
                  tone: 'ok',
                  text: zh
                    ? '在线演示模式下托盘面板常驻可用'
                    : 'Tray stays active in interactive demo mode',
                });
                return;
              }
              void triggerDesktopAction('quit');
            }}
            className="text-[12.5px] font-medium text-fg-secondary hover:text-[var(--coral-text)] transition-colors cursor-pointer"
          >
            {zh ? '退出' : 'Quit'}
          </button>
        </div>
      </footer>
    </div>
  );

  // ============================================================================
  // Sleek Stardust System Tray Right-Click Context Menu (`240×324`)
  // ============================================================================
  if (!embedded && trayMode === 'menu') {
    const contextMenuCard = (
      <div
        role="menu"
        aria-label={zh ? 'ColabHive 托盘菜单' : 'ColabHive Tray Menu'}
        className={cn(
          'flex flex-col justify-between select-none overflow-hidden text-fg p-2',
          isCompactViewport
            ? 'w-full h-screen'
            : 'w-[256px] rounded-[14px] border',
        )}
        style={{
          backgroundColor: 'var(--surface-0)',
          borderColor: isCompactViewport ? 'transparent' : 'var(--border-float)',
          boxShadow: isCompactViewport
            ? 'none'
            : '0 20px 48px -12px rgba(0, 0, 0, 0.18), 0 4px 12px -2px rgba(0, 0, 0, 0.08), 0 0 0 1px rgba(24, 24, 27, 0.06)',
        }}
      >
        {/* Top Live Pool Status Pill */}
        <button
          type="button"
          role="menuitem"
          onClick={() => void triggerDesktopAction('open-route', '/pool')}
          className="w-full px-2.5 py-2 mb-1.5 rounded-[10px] border bg-[#FFFFFF] dark:bg-[#1E1E1C] hover:border-line-strong transition-colors flex items-center justify-between gap-2 text-left cursor-pointer"
          style={{
            borderColor: 'var(--glass-card-border)',
            boxShadow: '0 1px 2px rgba(0, 0, 0, 0.03)',
          }}
        >
          <div className="flex items-center gap-2 min-w-0">
            <TrayBrandMark size={18} />
            <div className="min-w-0">
              <div className="flex items-center gap-1.5">
                <span className="text-[12.5px] font-semibold tracking-[-0.015em] text-fg leading-none">
                  ColabHive
                </span>
                <span
                  className="w-1.5 h-1.5 rounded-full shrink-0"
                  style={{
                    backgroundColor: isTrayGateRequired ? 'var(--amber)' : 'var(--emerald)',
                  }}
                />
              </div>
              <div className="ch-num text-[11px] text-fg-muted mt-1 truncate leading-none">
                {isTrayGateRequired
                  ? zh
                    ? '等待管理员解锁'
                    : 'Admin unlock required'
                  : zh
                    ? `剩余 ${formatCU(totalCU)} CU · ${activeSessions.length} 台运行`
                    : `${formatCU(totalCU)} CU · ${activeSessions.length} active`}
              </div>
            </div>
          </div>
          <ChevronRight className="w-3.5 h-3.5 text-fg-muted shrink-0" />
        </button>

        {/* Primary Window Controls */}
        <div className="space-y-0.5">
          <button
            type="button"
            role="menuitem"
            onClick={() => void triggerDesktopAction('open-main', '/')}
            className="w-full h-[33px] px-2.5 rounded-[9px] flex items-center gap-2.5 text-[12.5px] font-semibold text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer text-left"
          >
            <AppWindow className="w-[15px] h-[15px] text-fg-secondary shrink-0" />
            <span className="truncate">{zh ? '打开 ColabHive 主窗口' : 'Open ColabHive'}</span>
          </button>

          <button
            type="button"
            role="menuitem"
            onClick={() => void handleMenuOpenPanel()}
            className="w-full h-[33px] px-2.5 rounded-[9px] flex items-center gap-2.5 text-[12.5px] font-medium text-fg-secondary hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer text-left"
          >
            <PanelBottomOpen className="w-[15px] h-[15px] text-fg-muted shrink-0" />
            <span className="truncate">{zh ? '打开快捷悬浮面板' : 'Open Quick Panel'}</span>
          </button>
        </div>

        <div className="my-1.5 h-px bg-[var(--border-faint)] mx-1" />

        {/* Utility Actions */}
        <div className="space-y-0.5">
          <button
            type="button"
            role="menuitem"
            onClick={() => void handleMenuSyncNow()}
            className="w-full h-[33px] px-2.5 rounded-[9px] flex items-center gap-2.5 text-[12.5px] font-medium text-fg-secondary hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer text-left"
          >
            <RefreshCw
              className={cn('w-[14px] h-[14px] text-fg-muted shrink-0', syncing && 'animate-spin')}
            />
            <span className="truncate">
              {syncing
                ? zh
                  ? '正在同步云端算力…'
                  : 'Syncing cloud pool…'
                : zh
                  ? '立即同步云端算力'
                  : 'Sync Cloud Quotas'}
            </span>
          </button>

          <button
            type="button"
            role="menuitem"
            onClick={() => void handleMenuCopyMCP()}
            className="w-full h-[33px] px-2.5 rounded-[9px] flex items-center gap-2.5 text-[12.5px] font-medium text-fg-secondary hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer text-left"
          >
            {menuCopiedMcp ? (
              <Check className="w-[14px] h-[14px] text-[var(--emerald-text)] shrink-0" />
            ) : (
              <Copy className="w-[14px] h-[14px] text-fg-muted shrink-0" />
            )}
            <span className="truncate">
              {menuCopiedMcp
                ? zh
                  ? '已复制 MCP 接口地址'
                  : 'Copied MCP Endpoint'
                : zh
                  ? '复制本机 MCP 接口地址'
                  : 'Copy Local MCP URL'}
            </span>
          </button>

          <button
            type="button"
            role="menuitemcheckbox"
            aria-checked={Boolean(autostart)}
            onClick={() => void handleToggleAutostart()}
            className="w-full h-[33px] px-2.5 rounded-[9px] flex items-center justify-between gap-2 text-[12.5px] font-medium text-fg-secondary hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer text-left"
          >
            <span className="flex items-center gap-2.5 min-w-0">
              <Power className="w-[14px] h-[14px] text-fg-muted shrink-0" />
              <span className="truncate">{zh ? '开机自动启动' : 'Launch at Login'}</span>
            </span>
            <span
              className="px-1.5 py-0.5 rounded-full text-[10px] font-semibold shrink-0 transition-colors"
              style={
                autostart
                  ? {
                      backgroundColor: 'var(--emerald-soft)',
                      color: 'var(--emerald-text)',
                    }
                  : {
                      backgroundColor: 'var(--surface-2)',
                      color: 'var(--text-muted)',
                    }
              }
            >
              {autostart ? (zh ? '已开启' : 'ON') : zh ? '关闭' : 'OFF'}
            </span>
          </button>
        </div>

        <div className="my-1.5 h-px bg-[var(--border-faint)] mx-1" />

        {/* Quit */}
        <button
          type="button"
          role="menuitem"
          onClick={() => {
            if (isDemoSiteMode()) {
              setTrayMode('panel');
              return;
            }
            void triggerDesktopAction('quit');
          }}
          className="w-full h-[33px] px-2.5 rounded-[9px] flex items-center gap-2.5 text-[12.5px] font-medium text-fg-secondary hover:bg-[var(--coral-soft)] hover:text-[var(--coral-text)] transition-colors cursor-pointer text-left"
        >
          <LogOut className="w-[14px] h-[14px] shrink-0 opacity-80" />
          <span className="truncate">{zh ? '退出 ColabHive' : 'Quit ColabHive'}</span>
        </button>
      </div>
    );

    if (isCompactViewport) {
      return contextMenuCard;
    }
    return (
      <div className="min-h-screen w-full flex items-center justify-center p-3">
        {contextMenuCard}
      </div>
    );
  }

  if (embedded || isCompactViewport) {
    return popoverSurface;
  }

  return (
    <div className="min-h-screen w-full flex items-center justify-center p-3">
      {popoverSurface}
    </div>
  );
}

export default TrayPopoverPage;
