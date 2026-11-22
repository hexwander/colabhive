import { useCallback, useEffect, useMemo, useState } from 'react';
import { IS_STATIC_DEMO_BUILD, isDemoSiteMode } from '../../lib/demoSiteAdapter';

export type AppSection =
  | 'landing'
  | 'observatory'
  | 'pool'
  | 'workbench'
  | 'router'
  | 'settings'
  | 'contribute'
  | 'tray';

export interface ParsedRoute {
  pathname: string;
  search: string;
  section: AppSection;
  isPublicPortal: boolean;
  experimentId?: string;
  inviteCode?: string;
  receiptToken?: string;
}

const NAV_EVENT = 'colabhive:navigate';

/**
 * Normalized router basename for GitHub Pages subpath deployments
 * (e.g. `https://hexwander.github.io/colabhive/` -> `"/colabhive"`),
 * or `""` when hosted at root `/` or relative `./`.
 */
export function getRouterBasePath(): string {
  const raw = import.meta.env.BASE_URL || '/';
  if (!raw || raw === '/' || raw === './' || raw === '.') {
    return '';
  }
  const withLeading = raw.startsWith('/') ? raw : `/${raw}`;
  return withLeading.replace(/\/+$/, '');
}

/**
 * Strips the GitHub Pages base prefix (`/colabhive`) from a browser pathname
 * so all internal route matchers operate on canonical paths (`/`, `/overview`, `/pool`, ...).
 */
export function stripBasePath(rawPathname: string): string {
  const base = getRouterBasePath();
  let p = rawPathname || '/';
  if (base && (p === base || p.startsWith(`${base}/`))) {
    p = p.slice(base.length) || '/';
  }
  return p.replace(/\/+$/, '') || '/';
}

/**
 * Prepends the GitHub Pages base prefix (`/colabhive`) to an internal route target
 * before pushing onto `window.history`.
 */
export function withBasePath(target: string): string {
  if (!target) return getRouterBasePath() || '/';
  if (target.startsWith('http://') || target.startsWith('https://') || target.startsWith('#')) {
    return target;
  }
  const base = getRouterBasePath();
  const normalized = target.startsWith('/') ? target : `/${target}`;
  if (!base) {
    return normalized;
  }
  if (normalized === base || normalized.startsWith(`${base}/`)) {
    return normalized;
  }
  if (normalized === '/') {
    return `${base}/`;
  }
  if (normalized.startsWith('/?')) {
    return `${base}/${normalized.slice(2) ? `?${normalized.slice(2)}` : ''}`;
  }
  return `${base}${normalized}`;
}

/**
 * Canonical Observatory home route:
 * - `/overview` on the static GitHub Pages Demo Site (`/` is the Product Marketing Landing Page)
 * - `/` on a local/cloud ColabHive daemon
 */
export function getObservatoryHomePath(): string {
  return IS_STATIC_DEMO_BUILD ? '/overview' : '/';
}

export function parseRoute(pathname: string, search = ''): ParsedRoute {
  // Persist `?demo=1` in sessionStorage whenever present on initial or client navigation
  if (search) {
    void isDemoSiteMode();
  }

  const clean = stripBasePath(pathname);

  // Flagship Product Marketing Website & Live Interactive Sandbox
  if (clean === '/landing' || clean === '/showcase') {
    return { pathname: clean, search, section: 'landing', isPublicPortal: false };
  }

  // Full Observatory Console explicit alias (`/overview` & `/console`)
  if (
    clean === '/overview' ||
    clean === '/console' ||
    clean.startsWith('/overview/') ||
    clean.startsWith('/console/')
  ) {
    return { pathname: clean, search, section: 'observatory', isPublicPortal: false };
  }

  // Standalone native Windows System Tray Popover (`/tray`)
  if (clean === '/tray' || clean.startsWith('/tray/')) {
    return { pathname: clean, search, section: 'tray', isPublicPortal: false };
  }

  // Self-service contributor receipt routes:
  // `/contribute/manage/:token` or `/my-contribution/:token`
  const manageMatch =
    clean.match(/^\/contribute\/manage\/([^/]+)$/) ||
    clean.match(/^\/my-contribution\/([^/]+)$/);
  if (manageMatch) {
    return {
      pathname: clean,
      search,
      section: 'contribute',
      isPublicPortal: true,
      receiptToken: decodeURIComponent(manageMatch[1]),
    };
  }

  // Public contributor onboarding invite route: `/contribute/:code` or `/contribute`
  if (clean === '/contribute') {
    return {
      pathname: clean,
      search,
      section: 'contribute',
      isPublicPortal: true,
    };
  }
  const inviteMatch = clean.match(/^\/contribute\/([^/]+)$/);
  if (inviteMatch) {
    return {
      pathname: clean,
      search,
      section: 'contribute',
      isPublicPortal: true,
      inviteCode: decodeURIComponent(inviteMatch[1]),
    };
  }

  if (clean === '/pool' || clean.startsWith('/pool/')) {
    return { pathname: clean, search, section: 'pool', isPublicPortal: false };
  }

  if (clean === '/exp') {
    return { pathname: clean, search, section: 'workbench', isPublicPortal: false };
  }
  const expMatch = clean.match(/^\/exp\/([^/]+)$/);
  if (expMatch) {
    return {
      pathname: clean,
      search,
      section: 'workbench',
      isPublicPortal: false,
      experimentId: decodeURIComponent(expMatch[1]),
    };
  }

  if (clean === '/router' || clean.startsWith('/router/')) {
    return { pathname: clean, search, section: 'router', isPublicPortal: false };
  }

  if (clean === '/cfg' || clean === '/settings' || clean.startsWith('/cfg/')) {
    return { pathname: clean, search, section: 'settings', isPublicPortal: false };
  }

  // Root `/`: renders Product Landing Page on GitHub Pages (`VITE_DEMO_SITE === 'true'`),
  // and Observatory Console on real local/cloud daemon builds.
  if (clean === '/' && IS_STATIC_DEMO_BUILD) {
    return { pathname: clean, search, section: 'landing', isPublicPortal: false };
  }

  return { pathname: clean, search, section: 'observatory', isPublicPortal: false };
}

export function navigate(to: string, replace = false): void {
  if (typeof window === 'undefined') return;
  const fullTarget = withBasePath(to);
  const current = `${window.location.pathname}${window.location.search}`;
  if (current === fullTarget) return;
  if (replace) {
    window.history.replaceState(null, '', fullTarget);
  } else {
    window.history.pushState(null, '', fullTarget);
  }
  window.scrollTo({ top: 0, behavior: 'instant' as ScrollBehavior });
  window.dispatchEvent(new CustomEvent(NAV_EVENT, { detail: { to: fullTarget } }));
}

export function useRouter(): ParsedRoute & { navigate: (to: string, replace?: boolean) => void } {
  const [loc, setLoc] = useState<{ pathname: string; search: string }>(() => {
    if (typeof window === 'undefined') {
      return { pathname: '/', search: '' };
    }
    return { pathname: window.location.pathname, search: window.location.search };
  });

  useEffect(() => {
    if (typeof window === 'undefined') return;

    const syncLocation = () => {
      setLoc({ pathname: window.location.pathname, search: window.location.search });
    };

    const handleDocClick = (e: MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) {
        return;
      }
      const target = (e.target as HTMLElement | null)?.closest?.('a');
      if (!target) return;
      if (target.target && target.target !== '_self') return;
      if (target.hasAttribute('download')) return;

      const href = target.getAttribute('href');
      if (!href || !href.startsWith('/') || href.startsWith('//')) return;
      if (href.startsWith('/api/') || href.startsWith('/mcp') || href.startsWith('/ws/')) return;

      e.preventDefault();
      navigate(href);
    };

    window.addEventListener('popstate', syncLocation);
    window.addEventListener(NAV_EVENT, syncLocation);
    document.addEventListener('click', handleDocClick);

    return () => {
      window.removeEventListener('popstate', syncLocation);
      window.removeEventListener(NAV_EVENT, syncLocation);
      document.removeEventListener('click', handleDocClick);
    };
  }, []);

  const parsed = useMemo(() => parseRoute(loc.pathname, loc.search), [loc.pathname, loc.search]);
  const go = useCallback((to: string, replace = false) => navigate(to, replace), []);

  return useMemo(() => ({ ...parsed, navigate: go }), [parsed, go]);
}
