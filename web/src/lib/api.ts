import { useEffect, useRef, useState } from 'react';
import type {
  Account,
  AccountPatch,
  AccountVerifyRequest,
  AccountVerifyResult,
  AgentAPIKey,
  AgentClientConfigStatus,
  AgentClientID,
  AgentInjectRequest,
  CellMutationRequest,
  CreateAPIKeyRequest,
  CredentialBundle,
  CredentialExportRequest,
  CredentialImportRequest,
  DaemonHealthStatus,
  DiscoveredLocalCredential,
  Experiment,
  ExperimentLaunchRequest,
  ExperimentTeardownResult,
  HardwareSpec,
  KernelExecRequest,
  KernelExecResult,
  NotebookCell,
  OAuthPendingSession,
  OAuthStartRequest,
  ProxyConfig,
  ProxyProbeResult,
  QuotaSummary,
  RemoteFileEntry,
  RouteBinding,
  RouterPolicy,
  RuntimeSession,
  SavingsReceiptStats,
  SessionAllocRequest,
  SystemSelfTestResult,
  ToolTraceEvent,
  WSEvent,
} from '../types';
import {
  handleDemoApiRequest,
  isDemoSiteMode,
  subscribeDemoWsEvents,
  triggerDemoFileDownload,
} from './demoSiteAdapter';

export class APIError extends Error {
  status: number;
  details?: unknown;

  constructor(message: string, status: number, details?: unknown) {
    super(message);
    this.name = 'APIError';
    this.status = status;
    this.details = details;
  }
}

export const ADMIN_TOKEN_STORAGE_KEY = 'colabhive.admin_token';
const LEGACY_ADMIN_TOKEN_STORAGE_KEY = 'ch_admin_token';

export const UNAUTHORIZED_EVENT = 'colabhive:unauthorized';
export const ADMIN_TOKEN_CHANGED_EVENT = 'colabhive:admin-token-changed';

export type { DaemonHealthStatus };

export function getStoredAdminToken(): string | null {
  try {
    const current = localStorage.getItem(ADMIN_TOKEN_STORAGE_KEY);
    if (current && current.trim()) {
      return current.trim();
    }
    const legacy = localStorage.getItem(LEGACY_ADMIN_TOKEN_STORAGE_KEY);
    if (legacy && legacy.trim()) {
      const cleaned = legacy.trim();
      localStorage.setItem(ADMIN_TOKEN_STORAGE_KEY, cleaned);
      localStorage.removeItem(LEGACY_ADMIN_TOKEN_STORAGE_KEY);
      return cleaned;
    }
    return null;
  } catch {
    return null;
  }
}

export function setStoredAdminToken(token: string | null): void {
  const cleaned = token ? token.trim() : '';
  try {
    if (cleaned) {
      localStorage.setItem(ADMIN_TOKEN_STORAGE_KEY, cleaned);
    } else {
      localStorage.removeItem(ADMIN_TOKEN_STORAGE_KEY);
    }
    localStorage.removeItem(LEGACY_ADMIN_TOKEN_STORAGE_KEY);
  } catch {
    // Ignore storage access errors in private browsing contexts
  }
  if (typeof window !== 'undefined') {
    window.dispatchEvent(
      new CustomEvent(ADMIN_TOKEN_CHANGED_EVENT, { detail: { hasToken: Boolean(cleaned) } }),
    );
  }
}

export function clearStoredAdminToken(): void {
  setStoredAdminToken(null);
}

interface RequestOptions extends RequestInit {
  skipUnauthorizedEvent?: boolean;
}

function isPublicPortalApiPath(path: string): boolean {
  return (
    path.startsWith('/api/public/') ||
    path.startsWith('/api/v1/contribute/portal/') ||
    path.startsWith('/api/v1/contribute/manage/')
  );
}

function bytesToBase64(bytes: Uint8Array): string {
  if (bytes.length === 0) return '';
  const CHUNK = 0x8000;
  const parts: string[] = [];
  for (let i = 0; i < bytes.length; i += CHUNK) {
    const sub = bytes.subarray(i, i + CHUNK);
    parts.push(String.fromCharCode(...sub));
  }
  return btoa(parts.join(''));
}

function parseContentDispositionFilename(disposition: string | null): string | null {
  if (!disposition) return null;
  const utf8Match = disposition.match(/filename\*=UTF-8''([^;\r\n]+)/i);
  if (utf8Match?.[1]) {
    try {
      return decodeURIComponent(utf8Match[1].trim().replace(/^"|"$/g, ''));
    } catch {
      // fall through
    }
  }
  const quotedMatch = disposition.match(/filename="([^"]+)"/i);
  if (quotedMatch?.[1]) return quotedMatch[1];
  const plainMatch = disposition.match(/filename=([^;\r\n]+)/i);
  if (plainMatch?.[1]) return plainMatch[1].trim().replace(/^"|"$/g, '');
  return null;
}

async function request<T>(path: string, init: RequestOptions = {}): Promise<T> {
  const { skipUnauthorizedEvent, ...fetchInit } = init;
  if (isDemoSiteMode()) {
    return handleDemoApiRequest<T>(path, fetchInit);
  }
  const headers = new Headers(fetchInit.headers);
  if (!headers.has('Accept')) {
    headers.set('Accept', 'application/json');
  }
  if (fetchInit.body && !(fetchInit.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  const adminToken = getStoredAdminToken();
  if (adminToken && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${adminToken}`);
  }

  const res = await fetch(path, { ...fetchInit, headers });
  if (res.status === 204) {
    return undefined as T;
  }

  const contentType = res.headers.get('content-type') || '';
  const isJson = contentType.includes('application/json');
  const body = isJson ? await res.json().catch(() => null) : await res.text().catch(() => '');

  if (!res.ok) {
    if (
      res.status === 401 &&
      !skipUnauthorizedEvent &&
      path.startsWith('/api/v1/') &&
      !isPublicPortalApiPath(path) &&
      typeof window !== 'undefined'
    ) {
      window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT, { detail: { path } }));
    }
    const msg =
      (body && typeof body === 'object' && 'error' in body && String((body as { error: unknown }).error)) ||
      (body && typeof body === 'object' && 'message' in body && String((body as { message: unknown }).message)) ||
      (typeof body === 'string' && body) ||
      `HTTP ${res.status} ${res.statusText}`;
    throw new APIError(msg, res.status, body);
  }

  return body as T;
}

// ============================================================================
// Typed REST Client (`/api/v1/*` & `/api/public/contribute/*`)
// ============================================================================

export const api = {
  // System Health, Admin Token Verification & Hardware Catalog
  getHealth: () => request<DaemonHealthStatus>('/api/v1/health', { skipUnauthorizedEvent: true }),
  verifyAdminToken: (candidateToken: string) =>
    request<QuotaSummary>('/api/v1/pool/summary', {
      headers: { Authorization: `Bearer ${candidateToken.trim()}` },
      skipUnauthorizedEvent: true,
    }),
  getHardwareCatalog: () => request<HardwareSpec[]>('/api/v1/hardware'),
  runSystemSelfTest: (): Promise<SystemSelfTestResult> =>
    request<SystemSelfTestResult>('/api/v1/system/selftest', { method: 'POST' }),
  createWsTicket: (): Promise<{ ticket?: string }> =>
    request<{ ticket?: string }>('/api/v1/ws-ticket', {
      method: 'POST',
      skipUnauthorizedEvent: true,
    }),

  // Observatory & Account Pool
  getQuotaSummary: (refresh = false) =>
    request<QuotaSummary>(`/api/v1/pool/summary${refresh ? '?refresh=true' : ''}`),
  listAccounts: () => request<Account[]>('/api/v1/accounts'),
  getAccount: (id: string) => request<Account>(`/api/v1/accounts/${encodeURIComponent(id)}`),
  updateAccount: (id: string, patch: AccountPatch) =>
    request<Account>(`/api/v1/accounts/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    }),
  deleteAccount: (id: string) =>
    request<void>(`/api/v1/accounts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  refreshAccountQuota: (id: string) =>
    request<Account>(`/api/v1/accounts/${encodeURIComponent(id)}/refresh`, { method: 'POST' }),
  verifyAccount: (id: string, req?: AccountVerifyRequest): Promise<AccountVerifyResult> =>
    request<AccountVerifyResult>(`/api/v1/accounts/${encodeURIComponent(id)}/verify`, {
      method: 'POST',
      body: JSON.stringify(req ?? {}),
    }),

  // 4-Step Owner OAuth2 PKCE Wizard
  startOAuth: (payload: OAuthStartRequest) =>
    request<OAuthPendingSession>('/api/v1/accounts/oauth/start', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  completeOAuth: (state: string, codeOrUrl: string) =>
    request<Account>('/api/v1/accounts/oauth/exchange', {
      method: 'POST',
      body: JSON.stringify({ state, raw_input: codeOrUrl, code: codeOrUrl }),
    }),

  // v1.1 Savings Receipt
  getSavingsReceipt: (signal?: AbortSignal) =>
    request<SavingsReceiptStats>('/api/v1/stats/savings-card', { signal }),

  // Proxy Pool Management
  listProxies: () => request<ProxyConfig[]>('/api/v1/proxies'),
  saveProxy: (proxy: Partial<ProxyConfig> & { name: string; url: string }) =>
    request<ProxyConfig>('/api/v1/proxies', {
      method: 'POST',
      body: JSON.stringify(proxy),
    }),
  deleteProxy: (id: string) =>
    request<void>(`/api/v1/proxies/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  testProxy: (url: string) =>
    request<ProxyProbeResult>('/api/v1/proxies/test', {
      method: 'POST',
      body: JSON.stringify({ url }),
    }),

  // Credential Portability (`keyringrc.cfg`, `gcloud` ADC, JSON, `.chvault`)
  scanLocalCredentials: () => request<DiscoveredLocalCredential[]>('/api/v1/import/scan-local'),
  importCredentials: (payload: CredentialImportRequest) =>
    request<Account[]>('/api/v1/pool/import', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  exportVaultBundle: (payload: CredentialExportRequest) =>
    request<CredentialBundle>('/api/v1/pool/export', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  // Runtime Sessions & Workbench
  listSessions: (params?: {
    account_id?: string;
    experiment_id?: string;
    active_only?: boolean;
    limit?: number;
    all?: boolean;
    sync?: boolean;
  }) => {
    const qs = new URLSearchParams();
    if (params?.account_id) qs.set('account_id', params.account_id);
    if (params?.experiment_id) qs.set('experiment_id', params.experiment_id);
    if (params?.active_only) qs.set('active_only', 'true');
    if (params?.all) qs.set('all', 'true');
    else if (typeof params?.limit === 'number') qs.set('limit', String(params.limit));
    if (params?.sync) qs.set('sync', 'true');
    const query = qs.toString();
    return request<RuntimeSession[]>(`/api/v1/sessions${query ? `?${query}` : ''}`);
  },
  syncRemoteSessions: (): Promise<{
    sessions: RuntimeSession[];
    adopted_count: number;
    active_count: number;
  }> =>
    request<{
      sessions: RuntimeSession[];
      adopted_count: number;
      active_count: number;
    }>('/api/v1/sessions/sync', { method: 'POST' }),
  createSession: (payload: SessionAllocRequest) =>
    request<RuntimeSession>('/api/v1/sessions', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  getSession: (id: string) =>
    request<RuntimeSession>(`/api/v1/sessions/${encodeURIComponent(id)}`),
  stopSession: (id: string) =>
    request<void>(`/api/v1/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  restartKernel: (id: string) =>
    request<{ restarted: boolean }>(
      `/api/v1/sessions/${encodeURIComponent(id)}/restart-kernel`,
      { method: 'POST' },
    ),
  interruptKernel: (id: string) =>
    request<{ interrupted: boolean }>(
      `/api/v1/sessions/${encodeURIComponent(id)}/interrupt`,
      { method: 'POST' },
    ),
  hotRestartWorker: (id: string) =>
    request<{ restarted: boolean }>(`/api/v1/sessions/${encodeURIComponent(id)}/hot-restart`, {
      method: 'POST',
    }),
  getSessionWebUrl: (id: string) =>
    request<{ url: string }>(`/api/v1/sessions/${encodeURIComponent(id)}/open-url`),
  executeCode: (sessionId: string, payload: Omit<KernelExecRequest, 'session_id'>) =>
    request<KernelExecResult>(`/api/v1/sessions/${encodeURIComponent(sessionId)}/exec`, {
      method: 'POST',
      body: JSON.stringify({ ...payload, session_id: sessionId }),
    }),
  getCells: (sessionId: string, includeOutputs = true) =>
    request<NotebookCell[]>(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/cells?include_outputs=${includeOutputs}`,
    ),
  mutateCell: (sessionId: string, payload: CellMutationRequest) =>
    request<{ cell?: NotebookCell; result?: KernelExecResult }>(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/cells`,
      {
        method: 'POST',
        body: JSON.stringify(payload),
      },
    ),

  // Remote File System & Package Installer
  listFiles: (sessionId: string, remotePath = '/content') =>
    request<RemoteFileEntry[]>(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/files?path=${encodeURIComponent(remotePath)}`,
    ),
  uploadFile: async (
    sessionId: string,
    remotePath: string,
    fileOrBase64: File | Blob | string,
  ): Promise<{ uploaded: boolean; remote_path: string; bytes: number }> => {
    const endpoint = `/api/v1/sessions/${encodeURIComponent(sessionId)}/files`;
    if (typeof fileOrBase64 === 'string') {
      return request<{ uploaded: boolean; remote_path: string; bytes: number }>(endpoint, {
        method: 'POST',
        body: JSON.stringify({
          remote_path: remotePath,
          format: 'base64',
          content_base64: fileOrBase64,
        }),
      });
    }

    const CHUNK_BYTES = 4 * 1024 * 1024;
    if (fileOrBase64.size <= CHUNK_BYTES) {
      const buf = await fileOrBase64.arrayBuffer();
      const b64 = bytesToBase64(new Uint8Array(buf));
      return request<{ uploaded: boolean; remote_path: string; bytes: number }>(endpoint, {
        method: 'POST',
        body: JSON.stringify({
          remote_path: remotePath,
          format: 'base64',
          content_base64: b64,
        }),
      });
    }

    const totalChunks = Math.ceil(fileOrBase64.size / CHUNK_BYTES);
    let lastRes: { uploaded: boolean; remote_path: string; bytes: number } | undefined;
    for (let i = 0; i < totalChunks; i += 1) {
      const start = i * CHUNK_BYTES;
      const end = Math.min(fileOrBase64.size, start + CHUNK_BYTES);
      const buf = await fileOrBase64.slice(start, end).arrayBuffer();
      const b64 = bytesToBase64(new Uint8Array(buf));
      const chunkNum = i === totalChunks - 1 ? -1 : i + 1;
      lastRes = await request<{ uploaded: boolean; remote_path: string; bytes: number }>(endpoint, {
        method: 'POST',
        body: JSON.stringify({
          remote_path: remotePath,
          format: 'base64',
          chunk: chunkNum,
          content_base64: b64,
        }),
      });
    }
    return lastRes!;
  },
  downloadFileUrl: (sessionId: string, remotePath: string) =>
    `/api/v1/sessions/${encodeURIComponent(sessionId)}/files/download?path=${encodeURIComponent(remotePath)}`,
  downloadSessionFile: async (
    sessionId: string,
    remotePath: string,
    suggestedFilename?: string,
  ): Promise<void> => {
    if (isDemoSiteMode()) {
      triggerDemoFileDownload(remotePath, suggestedFilename);
      return;
    }
    const path = `/api/v1/sessions/${encodeURIComponent(sessionId)}/files/download?path=${encodeURIComponent(remotePath)}`;
    const headers = new Headers();
    const adminToken = getStoredAdminToken();
    if (adminToken) {
      headers.set('Authorization', `Bearer ${adminToken}`);
    }

    const res = await fetch(path, { method: 'GET', headers });
    if (!res.ok) {
      if (res.status === 401 && typeof window !== 'undefined') {
        window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT, { detail: { path } }));
      }
      const contentType = res.headers.get('content-type') || '';
      const isJson = contentType.includes('application/json');
      const body = isJson ? await res.json().catch(() => null) : await res.text().catch(() => '');
      const msg =
        (body &&
          typeof body === 'object' &&
          'error' in body &&
          String((body as { error: unknown }).error)) ||
        (body &&
          typeof body === 'object' &&
          'message' in body &&
          String((body as { message: unknown }).message)) ||
        (typeof body === 'string' && body) ||
        `HTTP ${res.status} ${res.statusText}`;
      throw new APIError(msg, res.status, body);
    }

    const blob = await res.blob();
    const filename =
      parseContentDispositionFilename(res.headers.get('content-disposition')) ||
      suggestedFilename ||
      remotePath.split('/').filter(Boolean).pop() ||
      'download';

    const blobUrl = URL.createObjectURL(blob);
    try {
      const link = document.createElement('a');
      link.href = blobUrl;
      link.download = filename;
      link.style.display = 'none';
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
    } finally {
      window.setTimeout(() => URL.revokeObjectURL(blobUrl), 1000);
    }
  },
  deleteFile: (sessionId: string, remotePath: string) =>
    request<void>(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/files?path=${encodeURIComponent(remotePath)}`,
      { method: 'DELETE' },
    ),
  installPackages: (sessionId: string, packages: string[], requirementsTxt?: string) =>
    request<KernelExecResult>(`/api/v1/sessions/${encodeURIComponent(sessionId)}/install`, {
      method: 'POST',
      body: JSON.stringify({ packages, requirements_txt: requirementsTxt }),
    }),

  // Parallel Sharded Experiments
  listExperiments: () => request<Experiment[]>('/api/v1/experiments'),
  getExperiment: (id: string) =>
    request<Experiment>(`/api/v1/experiments/${encodeURIComponent(id)}`),
  launchExperiment: (payload: ExperimentLaunchRequest) =>
    request<Experiment>('/api/v1/experiments', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  teardownExperiment: (id: string, forceKill = false) =>
    request<ExperimentTeardownResult>(
      `/api/v1/experiments/${encodeURIComponent(id)}/teardown`,
      {
        method: 'POST',
        body: JSON.stringify({ force_kill_running: forceKill }),
      },
    ),

  // Agent Router, Sticky Sessions, API Keys & `cc-switch` Config Sync
  getRouterPolicy: () => request<RouterPolicy>('/api/v1/router/policy'),
  updateRouterPolicy: (policy: RouterPolicy) =>
    request<RouterPolicy>('/api/v1/router/policy', {
      method: 'PUT',
      body: JSON.stringify(policy),
    }),
  listStickyBindings: () => request<RouteBinding[]>('/api/v1/router/sticky'),
  unbindStickySession: (stickyKey: string) =>
    request<void>(`/api/v1/router/sticky/${encodeURIComponent(stickyKey)}`, {
      method: 'DELETE',
    }),
  listTraces: (limit = 100) =>
    request<ToolTraceEvent[]>(`/api/v1/router/traces?limit=${limit}`),

  listAPIKeys: () => request<AgentAPIKey[]>('/api/v1/keys'),
  createAPIKey: (payload: CreateAPIKeyRequest) =>
    request<AgentAPIKey>('/api/v1/keys', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  revokeAPIKey: (id: string) =>
    request<void>(`/api/v1/keys/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  listAgentClients: (apiKey?: string, signal?: AbortSignal) => {
    const qs = apiKey ? `?api_key=${encodeURIComponent(apiKey)}` : '';
    return request<AgentClientConfigStatus[]>(`/api/v1/agents/clients${qs}`, { signal });
  },
  injectAgentConfig: (payload: AgentInjectRequest) =>
    request<AgentClientConfigStatus>('/api/v1/agents/inject', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  removeAgentConfig: (clientId: AgentClientID) =>
    request<AgentClientConfigStatus>(`/api/v1/agents/inject/${encodeURIComponent(clientId)}`, {
      method: 'DELETE',
    }),

  // Native Desktop Shell & System Tray Bridge (`/api/v1/desktop/*`)
  desktopAction: (action: string, route?: string) =>
    request<{
      ok?: boolean;
      handled?: boolean;
      bridge_active?: boolean;
      native?: boolean;
      desktop_shell?: boolean;
      autostart?: boolean;
      autostart_enabled?: boolean;
      state?: {
        desktop_shell?: boolean;
        autostart?: boolean;
        autostart_enabled?: boolean;
        tray_visible?: boolean;
        main_window_visible?: boolean;
        [key: string]: unknown;
      };
      [key: string]: unknown;
    }>('/api/v1/desktop/action', {
      method: 'POST',
      body: JSON.stringify(route ? { action, route, path: route } : { action }),
    }),
  getDesktopState: () =>
    request<{
      native?: boolean;
      bridge_active?: boolean;
      desktop_shell?: boolean;
      autostart?: boolean;
      autostart_enabled?: boolean;
      tray_visible?: boolean;
      main_visible?: boolean;
      main_window_visible?: boolean;
      [key: string]: unknown;
    }>('/api/v1/desktop/state'),
};

// ============================================================================
// Auto-Reconnecting `/ws/events` Real-Time Telemetry Hook
// ============================================================================

export interface WSConnectionState {
  connected: boolean;
  reconnecting: boolean;
  latencyMs: number | null;
  lastEventAt: number | null;
}

export function useColabHiveEvents(
  onEvent?: (event: WSEvent) => void,
  enabled = true,
): WSConnectionState {
  const [state, setState] = useState<WSConnectionState>({
    connected: false,
    reconnecting: false,
    latencyMs: null,
    lastEventAt: null,
  });

  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;

  useEffect(() => {
    if (!enabled || typeof window === 'undefined') {
      return;
    }

    if (isDemoSiteMode()) {
      setState({
        connected: true,
        reconnecting: false,
        latencyMs: 12,
        lastEventAt: Date.now(),
      });
      const unsubscribe = subscribeDemoWsEvents((evt) => {
        const now = Date.now();
        setState((prev) => ({
          ...prev,
          connected: true,
          reconnecting: false,
          latencyMs: 10 + (now % 7),
          lastEventAt: now,
        }));
        handlerRef.current?.(evt);
      });
      return () => {
        unsubscribe();
      };
    }

    let disposed = false;
    let ws: WebSocket | null = null;
    let retryTimer: number | null = null;
    let attempt = 0;
    let connectSeq = 0;

    const scheduleReconnect = () => {
      if (disposed) return;
      if (retryTimer !== null) {
        window.clearTimeout(retryTimer);
      }
      const baseMs = Math.min(1000 * Math.pow(1.8, attempt), 15000);
      const jitter = Math.round(baseMs * (0.8 + Math.random() * 0.4));
      attempt += 1;
      retryTimer = window.setTimeout(() => {
        retryTimer = null;
        void connect();
      }, jitter);
    };

    const connect = async () => {
      if (disposed) return;
      const mySeq = ++connectSeq;
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const token = getStoredAdminToken();
      let query = '';

      if (token) {
        try {
          const ticketRes = await api.createWsTicket();
          if (disposed || mySeq !== connectSeq) return;
          if (ticketRes?.ticket) {
            query = `?ticket=${encodeURIComponent(ticketRes.ticket)}`;
          } else {
            setState((prev) => ({ ...prev, connected: false, reconnecting: true }));
            scheduleReconnect();
            return;
          }
        } catch (err) {
          if (disposed || mySeq !== connectSeq) return;
          if (err instanceof APIError && (err.status === 401 || err.status === 403)) {
            setState((prev) => ({ ...prev, connected: false, reconnecting: false }));
            window.dispatchEvent(
              new CustomEvent(UNAUTHORIZED_EVENT, { detail: { path: '/api/v1/ws-ticket' } }),
            );
            return;
          }
          if (err instanceof APIError && err.status === 404) {
            // Legacy daemon fallback ONLY when POST /api/v1/ws-ticket returns 404
            query = `?token=${encodeURIComponent(token)}`;
          } else {
            setState((prev) => ({ ...prev, connected: false, reconnecting: true }));
            scheduleReconnect();
            return;
          }
        }
      }

      if (disposed || mySeq !== connectSeq) return;
      const wsUrl = `${proto}//${window.location.host}/ws/events${query}`;

      const openedAt = performance.now();
      let socket: WebSocket;
      try {
        socket = new WebSocket(wsUrl);
        ws = socket;
      } catch {
        scheduleReconnect();
        return;
      }

      socket.onopen = () => {
        if (disposed || mySeq !== connectSeq) return;
        attempt = 0;
        setState((prev) => ({
          ...prev,
          connected: true,
          reconnecting: false,
          latencyMs: Math.max(1, Math.round(performance.now() - openedAt)),
        }));
      };

      socket.onmessage = (msg) => {
        if (disposed || mySeq !== connectSeq) return;
        try {
          const parsed = JSON.parse(String(msg.data)) as WSEvent;
          const now = Date.now();
          const serverTs = parsed.timestamp ? Date.parse(parsed.timestamp) : NaN;
          const rtt = !Number.isNaN(serverTs)
            ? Math.max(1, Math.min(999, Math.abs(now - serverTs)))
            : null;
          setState((prev) => ({
            ...prev,
            lastEventAt: now,
            latencyMs: rtt ?? prev.latencyMs,
          }));
          handlerRef.current?.(parsed);
        } catch {
          // Ignore malformed non-JSON frames
        }
      };

      socket.onclose = () => {
        if (disposed || mySeq !== connectSeq) return;
        setState((prev) => ({ ...prev, connected: false, reconnecting: true }));
        scheduleReconnect();
      };

      socket.onerror = () => {
        socket.close();
      };
    };

    const handleTokenChange = () => {
      if (disposed) return;
      connectSeq += 1;
      if (retryTimer !== null) {
        window.clearTimeout(retryTimer);
        retryTimer = null;
      }
      attempt = 0;
      if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
        ws.onclose = null;
        ws.close();
      }
      void connect();
    };

    window.addEventListener(ADMIN_TOKEN_CHANGED_EVENT, handleTokenChange);
    void connect();

    return () => {
      disposed = true;
      connectSeq += 1;
      window.removeEventListener(ADMIN_TOKEN_CHANGED_EVENT, handleTokenChange);
      if (retryTimer !== null) {
        window.clearTimeout(retryTimer);
      }
      if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
        ws.onclose = null;
        ws.close();
      }
    };
  }, [enabled]);

  return state;
}

// ============================================================================
// Privacy-Mask & Precision Telemetry Formatters
// ============================================================================

/**
 * Masks an email for community screenshots (`alex.chen@stanford-ai.example` -> `ale***@stanford-ai.example`).
 */
export function maskEmail(email: string | undefined | null, masked = true): string {
  if (!email) return '—';
  if (!masked) return email;
  const atIdx = email.indexOf('@');
  if (atIdx <= 0) {
    return email.length <= 3 ? '***' : `${email.slice(0, 2)}***`;
  }
  const local = email.slice(0, atIdx);
  const domain = email.slice(atIdx);
  const visible = local.slice(0, Math.min(3, Math.max(1, local.length - 1)));
  return `${visible}***${domain}`;
}

/**
 * Masks an IPv4/IPv6 address (`198.51.100.42` -> `198.51.*.*`).
 */
export function maskIP(ip: string | undefined | null, masked = true): string {
  if (!ip) return '—';
  if (!masked) return ip;
  if (ip.includes('.')) {
    const parts = ip.split('.');
    if (parts.length === 4) {
      return `${parts[0]}.${parts[1]}.*.*`;
    }
  }
  if (ip.includes(':')) {
    const parts = ip.split(':');
    return `${parts.slice(0, 2).join(':')}:*:*`;
  }
  return '***';
}

/**
 * Masks credentials and host octets inside a proxy URL (`socks5://user:pass@38.12.90.4:1080` -> `socks5://38.12.*.*:1080`).
 */
export function maskProxyUrl(url: string | undefined | null, masked = true): string {
  if (!url) return 'Direct';
  try {
    const parsed = new URL(url);
    const host = masked ? maskIP(parsed.hostname, true) : parsed.hostname;
    const port = parsed.port ? `:${parsed.port}` : '';
    return `${parsed.protocol}//${host}${port}`;
  } catch {
    return masked ? url.replace(/\/\/.*@/, '//***@') : url;
  }
}

/**
 * Masks session IDs, endpoints, or API keys when privacy mask is toggled on.
 */
export function maskSecret(value: string | undefined | null, masked = true): string {
  if (!value) return '—';
  if (!masked) return value;
  if (value.length <= 8) return `${value.slice(0, 2)}***`;
  return `${value.slice(0, 6)}…${value.slice(-3)}`;
}

/**
 * Formats Compute Units with fixed precision (`1,428.42`).
 */
export function formatCU(cu: number | undefined | null, decimals = 2): string {
  if (cu === undefined || cu === null || Number.isNaN(cu)) return '0.00';
  return cu.toLocaleString('en-US', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

/**
 * Formats decimal hours into crisp `Xh YYm` (`49.94` -> `49h 56m`).
 */
export function formatHours(hours: number | undefined | null): string {
  if (hours === undefined || hours === null || !Number.isFinite(hours)) return '∞';
  if (hours <= 0) return '0h 00m';
  const totalMinutes = Math.round(hours * 60);
  const h = Math.floor(totalMinutes / 60);
  const m = totalMinutes % 60;
  return `${h}h ${String(m).padStart(2, '0')}m`;
}

/**
 * Formats byte counts into compact human units (`51.4 GB`).
 */
export function formatBytes(bytes: number | undefined | null): string {
  if (!bytes || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let idx = 0;
  let val = bytes;
  while (val >= 1024 && idx < units.length - 1) {
    val /= 1024;
    idx += 1;
  }
  return `${val.toFixed(idx >= 3 ? 1 : 0)} ${units[idx]}`;
}
