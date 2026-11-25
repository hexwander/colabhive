import { Suspense, useCallback, useEffect, useMemo, useState } from 'react';
import {
  DEFAULT_HARDWARE_CATALOG,
  type Account,
  type DaemonHealthStatus,
  type Experiment,
  type HardwareSpec,
  type QuotaSummary,
  type RuntimeSession,
  type WSEvent,
} from './types';
import {
  ADMIN_TOKEN_CHANGED_EVENT,
  api,
  APIError,
  getStoredAdminToken,
  UNAUTHORIZED_EVENT,
  useColabHiveEvents,
} from './lib/api';
import { WS_V11_EVENT } from './lib/apiV11';
import { useI18n } from './i18n';
import { AppShell } from './components/layout/AppShell';
import { useRouter } from './components/layout/navigation';
import {
  DEMO_ACCOUNTS,
  DEMO_DAEMON_HEALTH,
  DEMO_QUOTA_SUMMARY,
} from './components/layout/telemetryUi';
import { AgentConnectDrawer } from './components/connect/AgentConnectDrawer';
import {
  LayeredCard,
  QuickStartLaunchpad,
  QuickStartProvider,
  Skeleton,
  type QuickStartContextValue,
} from './components/ui/primitives';
import { ObservatoryPage } from './pages/ObservatoryPage';
import { TrayPopoverPage } from './pages/TrayPopoverPage';
import { LandingPage } from './pages/LandingPage';
import { getDemoSnapshot, isDemoSiteMode } from './lib/demoSiteAdapter';
import { AppErrorBoundary, lazyWithRetry } from './components/ui/ErrorBoundary';

const AccountPoolPage = lazyWithRetry(() => import('./pages/AccountPoolPage'));
const SettingsPage = lazyWithRetry(() => import('./pages/SettingsPage'));
const WorkbenchPage = lazyWithRetry(() => import('./pages/WorkbenchPage'));
const AgentRouterPage = lazyWithRetry(() => import('./pages/AgentRouterPage'));
const LLMDeployPage = lazyWithRetry(() => import('./pages/LLMDeployPage'));
const MediaStudioPage = lazyWithRetry(() => import('./pages/MediaStudioPage'));
const AppStorePage = lazyWithRetry(() => import('./pages/AppStorePage'));
const SavingsPage = lazyWithRetry(() => import('./pages/SavingsPage'));

/** Calm layered placeholder shown while a lazy route chunk loads. */
function RouteSkeleton() {
  const { t } = useI18n();
  return (
    <div className="flex flex-col gap-6" role="status" aria-live="polite">
      <span className="sr-only">{t('common.loading')}</span>

      <div className="space-y-2.5">
        <Skeleton className="h-7 w-56" />
        <Skeleton className="h-4 w-96 max-w-full" />
      </div>

      <div className="ui-stagger grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
        {[0, 1, 2, 3].map((i) => (
          <LayeredCard key={i} padding="md">
            <Skeleton className="h-3.5 w-24" />
            <Skeleton className="mt-3 h-8 w-32" />
            <Skeleton className="mt-3 h-3 w-40 max-w-full" />
          </LayeredCard>
        ))}
      </div>

      <LayeredCard padding="md">
        <div className="space-y-3">
          <Skeleton className="h-4 w-48" />
          <Skeleton className="h-24 w-full rounded-xl" />
          <Skeleton className="h-3 w-2/3" />
        </div>
      </LayeredCard>
    </div>
  );
}

export default function App() {
  const route = useRouter();
  const isTrayPopover = route.pathname.startsWith('/tray') || route.section === 'tray';
  const isLandingPage = route.section === 'landing';

  const [summary, setSummary] = useState<QuotaSummary>(() =>
    isDemoSiteMode() ? getDemoSnapshot().summary : DEMO_QUOTA_SUMMARY,
  );
  const [accounts, setAccounts] = useState<Account[]>(() =>
    isDemoSiteMode() ? getDemoSnapshot().accounts : DEMO_ACCOUNTS,
  );
  const [health, setHealth] = useState<DaemonHealthStatus>(DEMO_DAEMON_HEALTH);
  const [experiments, setExperiments] = useState<Experiment[]>(() =>
    isDemoSiteMode() ? getDemoSnapshot().experiments : [],
  );
  const [sessions, setSessions] = useState<RuntimeSession[]>(() =>
    isDemoSiteMode() ? getDemoSnapshot().sessions : [],
  );
  const [hardwareCatalog, setHardwareCatalog] =
    useState<HardwareSpec[]>(DEFAULT_HARDWARE_CATALOG);
  const [offlineSnapshot, setOfflineSnapshot] = useState<boolean>(false);
  // False until the first sync settles. Until then the demo placeholders are on
  // screen without `offlineSnapshot` being known, so nothing may claim "done".
  const [telemetryReady, setTelemetryReady] = useState<boolean>(false);
  const [refreshing, setRefreshing] = useState<boolean>(false);
  const [authLocked, setAuthLocked] = useState<boolean>(false);

  // Global 1-Click Agent Connect Drawer
  const [connectDrawerOpen, setConnectDrawerOpen] = useState<boolean>(false);
  const [connectInitialKey, setConnectInitialKey] = useState<string | undefined>(undefined);

  const openConnectDrawer = useCallback((keyPrefixOrRaw?: string) => {
    setConnectInitialKey(keyPrefixOrRaw);
    setConnectDrawerOpen(true);
  }, []);

  const syncTelemetry = useCallback(async (forceQuotaRefresh = false) => {
    setRefreshing(true);
    try {
      const h = await api.getHealth();
      setHealth(h);
      setOfflineSnapshot(false);

      if (h.auth_required && !getStoredAdminToken()) {
        setAuthLocked(true);
        return;
      }

      try {
        const [s, accts, hw, exps, sess] = await Promise.all([
          api.getQuotaSummary(forceQuotaRefresh),
          api.listAccounts(),
          api.getHardwareCatalog().catch(() => DEFAULT_HARDWARE_CATALOG),
          api.listExperiments().catch(() => [] as Experiment[]),
          api.listSessions().catch(() => [] as RuntimeSession[]),
        ]);
        if (s && typeof s === 'object') {
          setSummary(s);
        }
        setAccounts(Array.isArray(accts) ? accts : []);
        setExperiments(Array.isArray(exps) ? exps : []);
        setSessions(Array.isArray(sess) ? sess : []);
        if (Array.isArray(hw) && hw.length > 0) {
          setHardwareCatalog(hw);
        }
        setAuthLocked(false);
      } catch (innerErr) {
        if (innerErr instanceof APIError && (innerErr.status === 401 || innerErr.status === 403)) {
          setAuthLocked(true);
        }
      }
    } catch {
      // Backend daemon not running yet — retain crisp reference telemetry snapshot
      setOfflineSnapshot(true);
    } finally {
      setRefreshing(false);
      setTelemetryReady(true);
    }
  }, []);

  useEffect(() => {
    const onUnauthorized = () => setAuthLocked(true);
    const onTokenChanged = (e: Event) => {
      const detail = (e as CustomEvent<{ hasToken?: boolean }>).detail;
      if (detail?.hasToken) {
        setAuthLocked(false);
        void syncTelemetry(false);
      } else {
        setAuthLocked(true);
      }
    };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    window.addEventListener(ADMIN_TOKEN_CHANGED_EVENT, onTokenChanged);
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
      window.removeEventListener(ADMIN_TOKEN_CHANGED_EVENT, onTokenChanged);
    };
  }, [syncTelemetry]);

  useEffect(() => {
    if (route.isPublicPortal || isTrayPopover || isLandingPage) return;
    syncTelemetry(false);
    const interval = window.setInterval(() => {
      syncTelemetry(false);
    }, 30_000);
    return () => window.clearInterval(interval);
  }, [isLandingPage, isTrayPopover, route.isPublicPortal, syncTelemetry]);

  const handleRefreshOneAccount = useCallback(async (accountId: string) => {
    try {
      const updated = await api.refreshAccountQuota(accountId);
      setAccounts((prev) => prev.map((a) => (a.id === updated.id ? updated : a)));
      const nextSummary = await api.getQuotaSummary(false);
      setSummary(nextSummary);
    } catch {
      // Ignore in offline snapshot mode
    }
  }, []);

  // Real-time WebSocket updates (`/ws/events`)
  const handleWsEvent = useCallback((evt: WSEvent) => {
    if (evt.type === 'quota_updated' && evt.payload && typeof evt.payload === 'object') {
      const nextSum = evt.payload as QuotaSummary;
      if (typeof nextSum.total_available_cu === 'number') {
        setSummary(nextSum);
        setOfflineSnapshot(false);
      }
    } else if (evt.type === 'account_updated' && evt.payload && typeof evt.payload === 'object') {
      const updated = evt.payload as Account;
      if (!updated.id) return;
      setAccounts((prev) => {
        const exists = prev.some((a) => a.id === updated.id);
        return exists
          ? prev.map((a) => (a.id === updated.id ? updated : a))
          : [updated, ...prev];
      });
    } else if (evt.type === 'session_updated' && evt.payload && typeof evt.payload === 'object') {
      const updated = evt.payload as RuntimeSession;
      if (!updated.id) return;
      setSessions((prev) => {
        const exists = prev.some((s) => s.id === updated.id);
        return exists
          ? prev.map((s) => (s.id === updated.id ? updated : s))
          : [updated, ...prev];
      });
      api
        .getQuotaSummary(false)
        .then((s) => {
          if (s && typeof s === 'object') setSummary(s);
        })
        .catch(() => {});
    } else if (
      evt.type === 'experiment_updated' ||
      evt.type === 'guardrail_preempt' ||
      evt.type === 'zombie_alert'
    ) {
      if (evt.type === 'experiment_updated' && evt.payload && typeof evt.payload === 'object') {
        const updatedExp = evt.payload as Experiment;
        if (updatedExp.id) {
          setExperiments((prev) => {
            const exists = prev.some((e) => e.id === updatedExp.id);
            return exists
              ? prev.map((e) => (e.id === updatedExp.id ? updatedExp : e))
              : [updatedExp, ...prev];
          });
        }
      }
      api
        .getQuotaSummary(false)
        .then((s) => {
          if (s && typeof s === 'object') setSummary(s);
        })
        .catch(() => {});
    } else if (
      evt.type === 'llm_gateway_updated' ||
      evt.type === 'app_instance_updated' ||
      evt.type === 'checkpoint_relay' ||
      evt.type === 'provider_updated'
    ) {
      if (typeof window !== 'undefined') {
        window.dispatchEvent(new CustomEvent<WSEvent>(WS_V11_EVENT, { detail: evt }));
      }
    }
  }, []);

  const wsAllowed =
    !route.isPublicPortal &&
    !isTrayPopover &&
    !isLandingPage &&
    (!health.auth_required || (!authLocked && Boolean(getStoredAdminToken())));
  const wsState = useColabHiveEvents(handleWsEvent, wsAllowed);

  // Live inputs for the 3-step Quick Start launchpad, wherever it is rendered.
  const navigateTo = route.navigate;
  const quickStartValue = useMemo<QuickStartContextValue>(
    () => ({
      summary,
      accounts,
      offlineSnapshot,
      telemetryReady,
      onOpenConnectDrawer: openConnectDrawer,
      onNavigate: (path: string) => navigateTo(path),
      onRefreshTelemetry: () => {
        void syncTelemetry(true);
      },
    }),
    [summary, accounts, offlineSnapshot, telemetryReady, openConnectDrawer, navigateTo, syncTelemetry],
  );

  // Flagship Product Marketing Website & Live Interactive Sandbox (`/` on Pages, `/landing`, `/showcase`)
  if (isLandingPage) {
    return <LandingPage />;
  }

  // Standalone borderless 396×580 Magpie-style System Tray Popover (`/tray`)
  if (isTrayPopover) {
    return <TrayPopoverPage />;
  }

  return (
    <QuickStartProvider value={quickStartValue}>
      <AppShell
        route={route}
        summary={summary}
        accounts={accounts}
        experiments={experiments}
        sessions={sessions}
        health={health}
        wsState={wsState}
        offlineSnapshot={offlineSnapshot}
        onRefreshTelemetry={() => syncTelemetry(true)}
        onOpenConnectDrawer={openConnectDrawer}
        onOpenSavingsReceipt={() => route.navigate('/savings')}
      >
        <AppErrorBoundary
          scope="route"
          resetKey={`${route.section}:${route.pathname}`}
          onResetHome={() => route.navigate('/')}
        >
          <Suspense fallback={<RouteSkeleton />}>
          {route.section === 'chat' && (
            <LLMDeployPage
              accounts={accounts}
              summary={summary}
              authRequired={Boolean(health.auth_required)}
              publicUrl={health.public_url}
              onNavigate={(path) => route.navigate(path)}
            />
          )}

          {route.section === 'studio' && (
            <MediaStudioPage
              accounts={accounts}
              summary={summary}
              onNavigate={(path) => route.navigate(path)}
            />
          )}

          {route.section === 'apps' && (
            <AppStorePage
              accounts={accounts}
              summary={summary}
              deploymentMode={health.deployment_mode}
              onNavigate={(path) => route.navigate(path)}
            />
          )}

          {route.section === 'savings' && (
            <SavingsPage
              accounts={accounts}
              summary={summary}
              onNavigate={(path) => route.navigate(path)}
            />
          )}

          {route.section === 'observatory' && (
            <>
              {/* `hostFallback`: steps aside if the page renders its own launchpad. */}
              <QuickStartLaunchpad hostFallback className="mb-6" />
              <ObservatoryPage
                summary={summary}
                accounts={accounts}
                hardwareCatalog={hardwareCatalog}
                refreshing={refreshing}
                onRefreshAll={() => syncTelemetry(true)}
                onRefreshAccount={handleRefreshOneAccount}
              />
            </>
          )}

          {route.section === 'pool' && (
            <AccountPoolPage
              accounts={accounts}
              deploymentMode={health.deployment_mode}
              onAccountsChanged={() => syncTelemetry(true)}
            />
          )}

          {route.section === 'workbench' && (
            <WorkbenchPage
              experimentId={route.experimentId}
              onOpenConnect={() => openConnectDrawer()}
              onNavigate={(path) => route.navigate(path)}
              onSessionsSynced={() => void syncTelemetry(false)}
            />
          )}

          {route.section === 'router' && (
            <AgentRouterPage
              deploymentMode={health.deployment_mode}
              publicUrl={health.public_url}
              onOpenConnect={() => openConnectDrawer()}
              onNavigate={(path) => route.navigate(path)}
            />
          )}

          {route.section === 'settings' && (
            <SettingsPage
              health={health}
              onOpenConnectWithKey={(keyVal) => openConnectDrawer(keyVal)}
            />
          )}
          </Suspense>
        </AppErrorBoundary>
      </AppShell>

      <AgentConnectDrawer
        open={connectDrawerOpen}
        onClose={() => setConnectDrawerOpen(false)}
        initialKeyPrefix={connectInitialKey}
        deploymentMode={health.deployment_mode}
        publicUrl={health.public_url}
      />
    </QuickStartProvider>
  );
}
