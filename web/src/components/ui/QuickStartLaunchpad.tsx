/**
 * QuickStartLaunchpad — the v3 "3 步快速开始" onboarding bento.
 *
 * Three horizontal glass step cards with live completion state, one-click
 * actions, a "how do I connect real accounts?" helper, and a collapsed
 * one-line form whose state persists in `localStorage`.
 *
 * It works with zero props (reading `QuickStartProvider`) or with explicit
 * props; explicit props win. `openQuickStartGuide()` re-opens it from anywhere.
 *
 * This file deliberately imports nothing from `./primitives` (which re-exports
 * it), so the two modules never form an import cycle. It builds on the same
 * `.ui-*` CSS recipes instead.
 */
import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from 'react';
import clsx from 'clsx';
import {
  ArrowRight,
  Check,
  ChevronDown,
  ChevronUp,
  Copy,
  Info,
  Lock,
  RefreshCw,
  Route,
  ShieldCheck,
  Sparkles,
  TerminalSquare,
  Undo2,
  UserPlus,
  Users,
  Zap,
} from 'lucide-react';
import type { Account, QuotaSummary } from '../../types';
import { api } from '../../lib/api';
import { useI18n, type TranslationKey } from '../../i18n';
import { navigate as routerNavigate } from '../layout/navigation';

// ============================================================================
// Shared keys, events and module state
// ============================================================================

/** DOM id of the launchpad root (expanded or collapsed). */
export const QUICKSTART_ELEMENT_ID = 'colabhive-quickstart-launchpad';

const COLLAPSED_STORAGE_KEY = 'colabhive_quickstart_collapsed_v3';
const AGENT_CONNECTED_STORAGE_KEY = 'colabhive_agent_connected_v3';
const OPEN_EVENT = 'colabhive:quickstart-open';
const AGENT_CONNECTED_EVENT = 'colabhive:agent-connected';

/** How long after `openQuickStartGuide()` a freshly mounted launchpad still reacts to it. */
const PENDING_OPEN_WINDOW_MS = 4000;
const PULSE_DURATION_MS = 1300;

function readFlag(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeFlag(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Ignore storage errors (private browsing)
  }
}

// --- Registry of page-owned launchpads --------------------------------------
// `App` mounts a `hostFallback` launchpad above the overview page. If a page
// also renders its own `<QuickStartLaunchpad />`, the fallback steps aside so
// exactly one launchpad is ever visible.

let pageInstanceCount = 0;
const registryListeners = new Set<() => void>();

function subscribeRegistry(listener: () => void): () => void {
  registryListeners.add(listener);
  return () => {
    registryListeners.delete(listener);
  };
}

function getPageInstanceCount(): number {
  return pageInstanceCount;
}

function registerPageInstance(): () => void {
  pageInstanceCount += 1;
  registryListeners.forEach((listener) => listener());
  return () => {
    pageInstanceCount = Math.max(0, pageInstanceCount - 1);
    registryListeners.forEach((listener) => listener());
  };
}

// --- Re-open from anywhere ----------------------------------------------------

let pendingOpenAt = 0;

/**
 * Re-opens the Quick Start guide: clears the collapsed state, navigates to `/`
 * when needed, then the launchpad scrolls itself into view and pulses once.
 * Safe to call when the launchpad is not mounted yet.
 */
export function openQuickStartGuide(options?: { navigate?: (to: string) => void }): void {
  if (typeof window === 'undefined') return;
  writeFlag(COLLAPSED_STORAGE_KEY, 'false');
  // A launchpad that mounts after this call (route change) picks the request up.
  pendingOpenAt = Date.now();
  window.dispatchEvent(new CustomEvent(OPEN_EVENT));
  if (window.location.pathname !== '/') {
    (options?.navigate ?? routerNavigate)('/');
  }
}

async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      document.body.removeChild(ta);
      return ok;
    } catch {
      return false;
    }
  }
}

// ============================================================================
// Context
// ============================================================================

export interface QuickStartContextValue {
  summary?: QuotaSummary;
  accounts?: Account[];
  /** True while the dashboard shows the built-in demo snapshot (local service unreachable). */
  offlineSnapshot?: boolean;
  /** False until the first telemetry sync has settled. Defaults to `true`. */
  telemetryReady?: boolean;
  onOpenConnectDrawer?: (keyPrefix?: string) => void;
  onNavigate?: (path: string) => void;
  onRefreshTelemetry?: () => void;
}

const QuickStartContext = createContext<QuickStartContextValue | null>(null);

export interface QuickStartProviderProps {
  value: QuickStartContextValue;
  children: React.ReactNode;
}

/** Supplies live telemetry and actions to any `<QuickStartLaunchpad />` below it. */
export function QuickStartProvider({ value, children }: QuickStartProviderProps) {
  return <QuickStartContext.Provider value={value}>{children}</QuickStartContext.Provider>;
}

// ============================================================================
// Small building blocks (CSS recipes from `src/index.css`)
// ============================================================================

type Tone = 'neutral' | 'accent' | 'emerald' | 'amber';

function Pill({
  tone = 'neutral',
  dot = false,
  pulse = false,
  children,
}: {
  tone?: Tone;
  dot?: boolean;
  pulse?: boolean;
  children: React.ReactNode;
}) {
  return (
    <span data-tone={tone} className="ui-pill max-w-full">
      {dot && <span className={clsx('ui-dot', pulse && 'ui-dot-pulse')} />}
      <span className="truncate">{children}</span>
    </span>
  );
}

function CommandRow({
  label,
  command,
  copyLabel,
  copiedLabel,
}: {
  label: string;
  command: string;
  copyLabel: string;
  copiedLabel: string;
}) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1800);
    return () => window.clearTimeout(timer);
  }, [copied]);

  return (
    <div className="ui-code flex items-center gap-2.5 pl-3 pr-1.5 h-9">
      <span className="shrink-0 text-[10.5px] font-medium" style={{ color: 'var(--code-muted)' }}>
        {label}
      </span>
      <code className="ch-num flex-1 min-w-0 truncate text-[12px]">{command}</code>
      <button
        type="button"
        onClick={async () => {
          if (await copyToClipboard(command)) setCopied(true);
        }}
        className="inline-flex items-center gap-1.5 h-6 px-2 shrink-0 rounded-md text-[11px] font-medium cursor-pointer transition-colors hover:bg-white/[0.07]"
        style={{ color: copied ? 'var(--code-string)' : 'var(--code-fg)' }}
      >
        {copied ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
        <span>{copied ? copiedLabel : copyLabel}</span>
      </button>
    </div>
  );
}

function SafetyPoint({
  icon,
  title,
  body,
}: {
  icon: React.ReactNode;
  title: string;
  body: string;
}) {
  return (
    <div className="flex items-start gap-2.5 min-w-0">
      <span
        className="flex items-center justify-center w-7 h-7 shrink-0 rounded-lg [&>svg]:w-3.5 [&>svg]:h-3.5"
        style={{ color: 'var(--emerald-text)', backgroundColor: 'var(--emerald-soft)' }}
      >
        {icon}
      </span>
      <div className="min-w-0">
        <div className="text-[12.5px] font-semibold text-fg">{title}</div>
        <p className="mt-0.5 text-[12px] leading-relaxed text-fg-secondary">{body}</p>
      </div>
    </div>
  );
}

interface StepTileProps {
  index: number;
  stepLabel: string;
  doneLabel: string;
  done: boolean;
  badge: React.ReactNode;
  title: string;
  body: string;
  actions: React.ReactNode;
}

function StepTile({ index, stepLabel, doneLabel, done, badge, title, body, actions }: StepTileProps) {
  return (
    <div className="ui-bento-tile" data-done={done}>
      <div className="flex items-center justify-between gap-2 min-h-6">
        <div className="flex items-center gap-2 min-w-0">
          <span
            className="ch-num flex items-center justify-center w-6 h-6 shrink-0 rounded-full text-[11px] font-semibold"
            style={
              done
                ? { color: '#ffffff', backgroundColor: 'var(--emerald)' }
                : { color: 'var(--accent-text)', backgroundColor: 'var(--accent-soft)' }
            }
            aria-hidden="true"
          >
            {done ? <Check className="w-3.5 h-3.5" strokeWidth={3} /> : `0${index}`}
          </span>
          <span className="text-[11px] font-medium text-fg-muted">
            {stepLabel}
            {done && <span className="sr-only"> · {doneLabel}</span>}
          </span>
        </div>
        <div className="min-w-0 shrink">{badge}</div>
      </div>

      <h3 className="mt-3 text-[14.5px] font-semibold leading-snug text-fg">{title}</h3>
      <p className="mt-1.5 flex-1 text-[12.5px] leading-relaxed text-fg-secondary">{body}</p>

      <div className="mt-4 flex flex-wrap items-center gap-2">{actions}</div>
    </div>
  );
}

// ============================================================================
// QuickStartLaunchpad
// ============================================================================

export interface QuickStartLaunchpadProps {
  summary?: QuotaSummary;
  accounts?: Account[];
  /** True while demo data is shown because the local service is unreachable. */
  offlineSnapshot?: boolean;
  /** Alias for `offlineSnapshot`. */
  isOfflineDemo?: boolean;
  /** Opens the "Connect AI assistant" drawer. */
  onOpenConnect?: () => void;
  /** Alias for `onOpenConnect`. */
  onConnectAgent?: () => void;
  onNavigate?: (path: string) => void;
  onRefreshTelemetry?: () => void;
  /**
   * Marks the app-level fallback instance: it renders nothing while any
   * page-owned `<QuickStartLaunchpad />` is mounted.
   */
  hostFallback?: boolean;
  className?: string;
}

export function QuickStartLaunchpad({
  summary: summaryProp,
  accounts: accountsProp,
  offlineSnapshot: offlineSnapshotProp,
  isOfflineDemo,
  onOpenConnect,
  onConnectAgent,
  onNavigate,
  onRefreshTelemetry,
  hostFallback = false,
  className,
}: QuickStartLaunchpadProps) {
  const ctx = useContext(QuickStartContext);
  const { t } = useI18n();

  const summary = summaryProp ?? ctx?.summary;
  const accounts = accountsProp ?? ctx?.accounts ?? [];
  const isDemo = isOfflineDemo ?? offlineSnapshotProp ?? ctx?.offlineSnapshot ?? false;
  const telemetryReady = ctx?.telemetryReady ?? true;
  const openConnect = onOpenConnect ?? onConnectAgent ?? ctx?.onOpenConnectDrawer;
  const go = onNavigate ?? ctx?.onNavigate ?? routerNavigate;
  const refreshTelemetry = onRefreshTelemetry ?? ctx?.onRefreshTelemetry;

  // --- Exactly-one guard ------------------------------------------------------
  const pageInstances = useSyncExternalStore(
    subscribeRegistry,
    getPageInstanceCount,
    getPageInstanceCount,
  );
  useLayoutEffect(() => {
    if (hostFallback) return;
    return registerPageInstance();
  }, [hostFallback]);
  const suppressed = hostFallback && pageInstances > 0;

  // --- Collapsed state + re-open pulse ---------------------------------------
  const rootRef = useRef<HTMLElement | null>(null);
  const [collapsed, setCollapsed] = useState<boolean>(
    () => readFlag(COLLAPSED_STORAGE_KEY) === 'true',
  );
  const [helperOpen, setHelperOpen] = useState(false);
  const [pulseToken, setPulseToken] = useState(0);

  const setCollapsedPersisted = useCallback((next: boolean) => {
    setCollapsed(next);
    writeFlag(COLLAPSED_STORAGE_KEY, String(next));
  }, []);

  const reveal = useCallback(() => {
    pendingOpenAt = 0;
    setCollapsed(false);
    setPulseToken((token) => token + 1);
  }, []);

  useEffect(() => {
    if (suppressed) return;
    // Opened from another route: the request arrived before this mounted.
    if (pendingOpenAt > 0 && Date.now() - pendingOpenAt < PENDING_OPEN_WINDOW_MS) reveal();
    window.addEventListener(OPEN_EVENT, reveal);
    return () => window.removeEventListener(OPEN_EVENT, reveal);
  }, [suppressed, reveal]);

  // After the expanded layout has committed: scroll to it, then let the ring fade.
  useEffect(() => {
    if (pulseToken === 0) return;
    rootRef.current?.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    const timer = window.setTimeout(() => setPulseToken(0), PULSE_DURATION_MS);
    return () => window.clearTimeout(timer);
  }, [pulseToken]);

  // --- Step 2: is an AI assistant connected? ---------------------------------
  const [agentConnected, setAgentConnected] = useState<boolean>(
    () => readFlag(AGENT_CONNECTED_STORAGE_KEY) === 'true',
  );

  useEffect(() => {
    const handleAgentEvent = (event: Event) => {
      const detail = (event as CustomEvent<{ connected?: boolean } | undefined>).detail;
      setAgentConnected(detail?.connected !== false);
    };
    // Another tab connected an assistant or toggled the guide.
    const handleStorage = (event: StorageEvent) => {
      if (event.key === AGENT_CONNECTED_STORAGE_KEY) setAgentConnected(event.newValue === 'true');
      if (event.key === COLLAPSED_STORAGE_KEY) setCollapsed(event.newValue === 'true');
    };
    window.addEventListener(AGENT_CONNECTED_EVENT, handleAgentEvent);
    window.addEventListener('storage', handleStorage);
    return () => {
      window.removeEventListener(AGENT_CONNECTED_EVENT, handleAgentEvent);
      window.removeEventListener('storage', handleStorage);
    };
  }, []);

  // Ask the local service once it is reachable: an assistant may already have
  // been connected from the CLI or an earlier browser profile.
  useEffect(() => {
    if (suppressed || isDemo || !telemetryReady) return;
    let cancelled = false;
    api
      .listAgentClients()
      .then((clients) => {
        if (!cancelled && clients.some((client) => client.injected)) setAgentConnected(true);
      })
      .catch(() => {
        // Keep the locally remembered state
      });
    return () => {
      cancelled = true;
    };
  }, [suppressed, isDemo, telemetryReady]);

  // --- Demo helper: retry the local service -----------------------------------
  const [retryState, setRetryState] = useState<'idle' | 'busy' | 'failed'>('idle');
  const isDemoRef = useRef(isDemo);
  isDemoRef.current = isDemo;

  useEffect(() => {
    if (retryState !== 'busy') return;
    const timer = window.setTimeout(
      () => setRetryState(isDemoRef.current ? 'failed' : 'idle'),
      1600,
    );
    return () => window.clearTimeout(timer);
  }, [retryState]);

  useEffect(() => {
    if (!isDemo) setRetryState('idle');
  }, [isDemo]);

  // --- Step 3: copy the test prompt -------------------------------------------
  const [promptCopied, setPromptCopied] = useState(false);
  useEffect(() => {
    if (!promptCopied) return;
    const timer = window.setTimeout(() => setPromptCopied(false), 2000);
    return () => window.clearTimeout(timer);
  }, [promptCopied]);

  if (suppressed) return null;

  // --- Derived step state -----------------------------------------------------
  const tn = (key: TranslationKey, n: number) => t(key).replace('{n}', String(n));
  const accountCount = accounts.length;
  const activeVms = summary?.active_vms ?? 0;
  const live = telemetryReady && !isDemo;

  const step1Done = live && accountCount > 0;
  const step2Done = agentConnected;
  const step3Done = live && activeVms > 0;
  const doneCount = [step1Done, step2Done, step3Done].filter(Boolean).length;
  const allDone = doneCount === 3;

  const handleOpenConnect = () => {
    if (openConnect) openConnect();
    else go('/router');
  };

  const handleRetry = () => {
    if (!refreshTelemetry) return;
    setRetryState('busy');
    refreshTelemetry();
  };

  const pulseRing =
    pulseToken > 0 ? (
      <span key={pulseToken} aria-hidden="true" className="ui-highlight-ring" />
    ) : null;

  const progressPill = (
    <Pill tone={allDone ? 'emerald' : doneCount > 0 ? 'accent' : 'neutral'} dot={doneCount > 0}>
      <span className="ch-num">{tn('quickstart.progress', doneCount)}</span>
    </Pill>
  );

  // ==========================================================================
  // Collapsed: one quiet glass line
  // ==========================================================================
  if (collapsed) {
    return (
      <section
        id={QUICKSTART_ELEMENT_ID}
        ref={rootRef}
        aria-label={t('quickstart.title')}
        className={clsx(
          'ui-card ui-enter scroll-mt-20 flex items-center gap-3 pl-3 pr-2.5 py-2',
          className,
        )}
      >
        {pulseRing}
        <span className="ui-glow-badge w-7 h-7 rounded-[9px]" aria-hidden="true">
          <Sparkles className="w-3.5 h-3.5" />
        </span>
        <p className="flex-1 min-w-0 truncate text-[12.5px] text-fg-secondary">
          {tn('quickstart.collapsed_line', doneCount)}
        </p>
        <button
          type="button"
          aria-expanded={false}
          onClick={() => setCollapsedPersisted(false)}
          className="ui-btn ui-btn-secondary ui-btn-xs shrink-0"
        >
          <ChevronDown className="w-3 h-3" />
          <span>{t('quickstart.expand')}</span>
        </button>
      </section>
    );
  }

  // ==========================================================================
  // Expanded: header, optional helper, three bento steps
  // ==========================================================================
  const isWindows =
    typeof navigator !== 'undefined' &&
    /windows|win32|win64/i.test(`${navigator.platform} ${navigator.userAgent}`);
  const commandRows = [
    { label: t('quickstart.cmd_generic'), command: 'colabhive serve' },
    { label: t('quickstart.cmd_windows'), command: './bin/colabhive.exe serve' },
  ];
  if (isWindows) commandRows.reverse();

  const helperPanelId = `${QUICKSTART_ELEMENT_ID}-helper`;

  return (
    <section
      id={QUICKSTART_ELEMENT_ID}
      ref={rootRef}
      aria-label={t('quickstart.title')}
      className={clsx('ui-card ui-enter scroll-mt-20 p-4 sm:p-5', className)}
    >
      {pulseRing}

      {/* Subtle warm emerald sheen behind the header: purely decorative */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 rounded-[inherit] opacity-60"
        style={{
          backgroundImage:
            'radial-gradient(480px 160px at 0% 0%, var(--emerald-soft), transparent 72%)',
        }}
      />

      <div className="relative">
        {/* ---------------------------------------------------------------- */}
        {/* Header row                                                        */}
        {/* ---------------------------------------------------------------- */}
        <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
          <div className="flex items-start gap-3 min-w-0">
            <span className="ui-glow-badge w-10 h-10 rounded-xl" aria-hidden="true">
              <Sparkles className="w-[18px] h-[18px]" />
            </span>
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
                <h2 className="ui-title text-[17px] font-semibold">{t('quickstart.title')}</h2>
                {progressPill}
              </div>
              <p className="mt-1 max-w-2xl text-[12.5px] leading-relaxed text-fg-secondary">
                {t('quickstart.value_prop')}
              </p>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2 lg:shrink-0 lg:justify-end">
            <button
              type="button"
              aria-expanded={helperOpen}
              aria-controls={helperPanelId}
              onClick={() => setHelperOpen((open) => !open)}
              data-tone={isDemo ? 'amber' : 'neutral'}
              className={clsx(
                'ui-pill ui-pill-md max-w-full cursor-pointer transition-[filter,transform] duration-200 hover:brightness-[0.97] active:scale-[0.98]',
              )}
            >
              {isDemo ? (
                <span className="ui-dot ui-dot-pulse" />
              ) : (
                <ShieldCheck className="w-3 h-3 shrink-0" />
              )}
              <span className="truncate">
                {isDemo ? t('quickstart.demo_pill') : t('quickstart.real_pill')}
              </span>
              {helperOpen ? (
                <ChevronUp className="w-3 h-3 shrink-0" />
              ) : (
                <ChevronDown className="w-3 h-3 shrink-0" />
              )}
            </button>

            <button
              type="button"
              aria-expanded={true}
              onClick={() => setCollapsedPersisted(true)}
              className="ui-btn ui-btn-ghost ui-btn-xs"
            >
              <ChevronUp className="w-3 h-3" />
              <span>{t('quickstart.collapse')}</span>
            </button>
          </div>
        </div>

        {/* Completion track: the fill shimmers while there is still a step to do */}
        <div
          className="mt-3.5 h-1 w-full rounded-full overflow-hidden"
          style={{ backgroundColor: 'var(--neutral-border)' }}
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={3}
          aria-valuenow={doneCount}
          aria-label={tn('quickstart.progress', doneCount)}
        >
          <div
            className={clsx(
              'h-full rounded-full transition-[width] duration-500 ease-out',
              !allDone && doneCount > 0 && 'ui-shimmer-bar',
            )}
            style={{
              width: `${(doneCount / 3) * 100}%`,
              backgroundColor: 'var(--emerald)',
            }}
          />
        </div>

        {/* ---------------------------------------------------------------- */}
        {/* Expandable helper                                                 */}
        {/* ---------------------------------------------------------------- */}
        {helperOpen && (
          <div id={helperPanelId} className="ui-well ui-anim-pop mt-3.5 p-3.5 sm:p-4">
            <div className="flex items-center gap-1.5 text-[12.5px] font-semibold text-fg">
              <Info className="w-3.5 h-3.5 shrink-0 text-fg-muted" />
              {isDemo ? t('quickstart.helper_title_demo') : t('quickstart.helper_title_real')}
            </div>

            {isDemo ? (
              <div className="mt-3 grid grid-cols-1 lg:grid-cols-2 gap-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2 text-[12.5px] font-medium text-fg">
                    <span
                      className="ch-num flex items-center justify-center w-5 h-5 shrink-0 rounded-full text-[10.5px] font-semibold"
                      style={{ color: 'var(--amber-text)', backgroundColor: 'var(--amber-soft)' }}
                    >
                      1
                    </span>
                    {t('quickstart.demo_step1')}
                  </div>
                  <div className="mt-2 space-y-1.5">
                    {commandRows.map((row) => (
                      <CommandRow
                        key={row.command}
                        label={row.label}
                        command={row.command}
                        copyLabel={t('common.copy')}
                        copiedLabel={t('common.copied')}
                      />
                    ))}
                  </div>
                </div>

                <div className="min-w-0">
                  <div className="flex items-center gap-2 text-[12.5px] font-medium text-fg">
                    <span
                      className="ch-num flex items-center justify-center w-5 h-5 shrink-0 rounded-full text-[10.5px] font-semibold"
                      style={{ color: 'var(--amber-text)', backgroundColor: 'var(--amber-soft)' }}
                    >
                      2
                    </span>
                    {t('quickstart.demo_step2')}
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-2">
                    <button
                      type="button"
                      onClick={handleRetry}
                      disabled={!refreshTelemetry || retryState === 'busy'}
                      className="ui-btn ui-btn-primary ui-btn-md"
                    >
                      <RefreshCw
                        className={clsx('w-3.5 h-3.5', retryState === 'busy' && 'animate-spin')}
                      />
                      <span>
                        {retryState === 'busy' ? t('quickstart.retrying') : t('quickstart.retry')}
                      </span>
                    </button>
                    <ArrowRight className="w-3.5 h-3.5 shrink-0 text-fg-muted" aria-hidden="true" />
                    <button
                      type="button"
                      onClick={() => go('/pool')}
                      className="ui-btn ui-btn-secondary ui-btn-md"
                    >
                      <UserPlus className="w-3.5 h-3.5" />
                      <span>{t('pool.add_account')}</span>
                    </button>
                  </div>
                  {retryState === 'failed' && (
                    <p
                      role="status"
                      className="mt-2 text-[12px] leading-relaxed"
                      style={{ color: 'var(--amber-text)' }}
                    >
                      {t('quickstart.retry_failed')}
                    </p>
                  )}
                  <p className="mt-2 text-[11.5px] leading-relaxed text-fg-muted">
                    {t('quickstart.demo_note')}
                  </p>
                </div>
              </div>
            ) : (
              <div className="mt-3 grid grid-cols-1 md:grid-cols-3 gap-4">
                <SafetyPoint
                  icon={<ShieldCheck />}
                  title={t('quickstart.safe_oauth_title')}
                  body={t('quickstart.safe_oauth_body')}
                />
                <SafetyPoint
                  icon={<Lock />}
                  title={t('quickstart.safe_vault_title')}
                  body={t('quickstart.safe_vault_body')}
                />
                <SafetyPoint
                  icon={<Undo2 />}
                  title={t('quickstart.safe_revoke_title')}
                  body={t('quickstart.safe_revoke_body')}
                />
              </div>
            )}
          </div>
        )}

        {/* ---------------------------------------------------------------- */}
        {/* Three bento steps                                                 */}
        {/* ---------------------------------------------------------------- */}
        <div className="ui-stagger mt-4 grid grid-cols-1 lg:grid-cols-3 gap-3.5">
          <StepTile
            index={1}
            stepLabel={tn('quickstart.step', 1)}
            doneLabel={t('quickstart.done')}
            done={step1Done}
            badge={
              !telemetryReady ? null : step1Done ? (
                <Pill tone="emerald" dot>
                  {tn('quickstart.s1_live_badge', accountCount)}
                </Pill>
              ) : isDemo ? (
                <Pill tone="amber" dot>
                  {tn('quickstart.s1_demo_badge', accountCount)}
                </Pill>
              ) : (
                <Pill>{t('quickstart.s1_todo_badge')}</Pill>
              )
            }
            title={t('quickstart.s1_title')}
            body={t('quickstart.s1_body')}
            actions={
              <>
                <button
                  type="button"
                  onClick={() => go('/pool')}
                  className={clsx(
                    'ui-btn ui-btn-md',
                    step1Done ? 'ui-btn-secondary' : 'ui-btn-primary',
                  )}
                >
                  <UserPlus className="w-3.5 h-3.5" />
                  <span>{t('quickstart.s1_primary')}</span>
                </button>
                <button
                  type="button"
                  onClick={() => go('/pool')}
                  className="ui-btn ui-btn-secondary ui-btn-md"
                >
                  <Users className="w-3.5 h-3.5" />
                  <span>{t('quickstart.s1_secondary')}</span>
                </button>
              </>
            }
          />

          <StepTile
            index={2}
            stepLabel={tn('quickstart.step', 2)}
            doneLabel={t('quickstart.done')}
            done={step2Done}
            badge={
              step2Done ? (
                <Pill tone="emerald" dot>
                  {t('quickstart.s2_live_badge')}
                </Pill>
              ) : (
                <Pill>{t('quickstart.s2_todo_badge')}</Pill>
              )
            }
            title={t('quickstart.s2_title')}
            body={t('quickstart.s2_body')}
            actions={
              <>
                <button
                  type="button"
                  onClick={handleOpenConnect}
                  className={clsx(
                    'ui-btn ui-btn-md',
                    step2Done ? 'ui-btn-secondary' : 'ui-btn-primary',
                  )}
                >
                  <Zap className="w-3.5 h-3.5" />
                  <span>{t('quickstart.s2_primary')}</span>
                </button>
                <button
                  type="button"
                  onClick={() => go('/router')}
                  className="ui-btn ui-btn-ghost ui-btn-md"
                >
                  <Route className="w-3.5 h-3.5" />
                  <span>{t('quickstart.s2_secondary')}</span>
                </button>
              </>
            }
          />

          <StepTile
            index={3}
            stepLabel={tn('quickstart.step', 3)}
            doneLabel={t('quickstart.done')}
            done={step3Done}
            badge={
              !telemetryReady ? null : step3Done ? (
                <Pill tone="emerald" dot pulse>
                  {tn('quickstart.s3_live_badge', activeVms)}
                </Pill>
              ) : isDemo && activeVms > 0 ? (
                <Pill tone="amber" dot>
                  {tn('quickstart.s3_demo_badge', activeVms)}
                </Pill>
              ) : (
                <Pill>{t('quickstart.s3_todo_badge')}</Pill>
              )
            }
            title={t('quickstart.s3_title')}
            body={t('quickstart.s3_body')}
            actions={
              <>
                <button
                  type="button"
                  onClick={() => go('/exp')}
                  className="ui-btn ui-btn-accent-soft ui-btn-md"
                >
                  <TerminalSquare className="w-3.5 h-3.5" />
                  <span>{t('quickstart.s3_primary')}</span>
                </button>
                <button
                  type="button"
                  onClick={async () => {
                    if (await copyToClipboard(t('quickstart.test_prompt'))) setPromptCopied(true);
                  }}
                  className="ui-btn ui-btn-secondary ui-btn-md"
                  style={
                    promptCopied
                      ? { color: 'var(--emerald-text)', borderColor: 'var(--emerald-border)' }
                      : undefined
                  }
                >
                  {promptCopied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                  <span aria-live="polite">
                    {promptCopied ? t('quickstart.s3_copied') : t('quickstart.s3_secondary')}
                  </span>
                </button>
              </>
            }
          />
        </div>
      </div>
    </section>
  );
}
