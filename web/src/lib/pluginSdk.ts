import { APIError, getStoredAdminToken } from './api';
import type {
  AssetKind,
  CapabilitiesSnapshot,
  EnhancePromptResponse,
  MediaAsset,
  MediaEngineSummary,
  MediaJob,
  MediaJobEvent,
  MediaJobRequest,
  MediaModelInfo,
  SandboxSession,
} from '../types';

export const STUDIO_PIN_STORAGE_KEY = 'colabhive.pinned.media_studio';
export const STUDIO_PIN_CHANGED_EVENT = 'colabhive:studio-pin-changed';

export function isStudioPinnedToSidebar(): boolean {
  try {
    return localStorage.getItem(STUDIO_PIN_STORAGE_KEY) === 'true';
  } catch {
    return false;
  }
}

export function setStudioPinnedToSidebar(pinned: boolean): void {
  try {
    if (pinned) {
      localStorage.setItem(STUDIO_PIN_STORAGE_KEY, 'true');
    } else {
      localStorage.removeItem(STUDIO_PIN_STORAGE_KEY);
    }
  } catch {
    // ignore storage errors
  }
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent(STUDIO_PIN_CHANGED_EVENT, { detail: { pinned } }));
  }
}

function sdkHeaders(extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = { ...(extra ?? {}) };
  const tok = getStoredAdminToken();
  if (tok) {
    headers['Authorization'] = `Bearer ${tok}`;
  }
  return headers;
}

async function sdkFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = sdkHeaders(init?.headers as Record<string, string> | undefined);
  const res = await fetch(path, { ...init, headers });
  const text = await res.text();
  let body: unknown = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = text;
    }
  }
  if (!res.ok) {
    const msg =
      (body && typeof body === 'object' && ('error' in body || 'message' in body)
        ? String((body as Record<string, unknown>).error ?? (body as Record<string, unknown>).message)
        : '') || `SDK request failed (HTTP ${res.status})`;
    throw new APIError(msg, res.status);
  }
  return body as T;
}

/**
 * Public Plugin Platform v2 Host SDK client (`/sdk/v1/*`).
 * ColabHive's built-in Native Media Studio strictly dogfoods ONLY these public endpoints,
 * guaranteeing that any third-party local UI plugin has 100% feature parity.
 */
export const pluginSdk = {
  getCapabilities(): Promise<CapabilitiesSnapshot> {
    return sdkFetch<CapabilitiesSnapshot>('/sdk/v1/capabilities');
  },

  reloadUIPlugins(): Promise<CapabilitiesSnapshot> {
    return sdkFetch<CapabilitiesSnapshot>('/sdk/v1/ui-plugins/reload', { method: 'POST' });
  },

  setUIPreference(capability: string, uiPluginId: string): Promise<{ ui_preferences: Record<string, string> }> {
    return sdkFetch(`/sdk/v1/ui-preferences/${encodeURIComponent(capability)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ui_plugin_id: uiPluginId }),
    });
  },

  openUIPlugin(pluginId: string): Promise<SandboxSession> {
    return sdkFetch<SandboxSession>(`/sdk/v1/ui-plugins/${encodeURIComponent(pluginId)}/open`, {
      method: 'POST',
    });
  },

  listAssets(params?: { kind?: AssetKind | ''; engine_id?: string; q?: string; limit?: number }): Promise<MediaAsset[]> {
    const qs = new URLSearchParams();
    if (params?.kind) qs.set('kind', params.kind);
    if (params?.engine_id) qs.set('engine_id', params.engine_id);
    if (params?.q) qs.set('q', params.q);
    if (params?.limit) qs.set('limit', String(params.limit));
    const qStr = qs.toString();
    return sdkFetch<MediaAsset[]>(`/sdk/v1/assets${qStr ? `?${qStr}` : ''}`);
  },

  uploadAsset(file: File, meta?: Partial<MediaAsset>): Promise<MediaAsset> {
    const fd = new FormData();
    fd.append('file', file);
    if (meta) {
      fd.append('meta', JSON.stringify(meta));
    }
    return sdkFetch<MediaAsset>('/sdk/v1/assets', { method: 'POST', body: fd });
  },

  deleteAsset(assetId: string): Promise<{ deleted: boolean; id: string }> {
    return sdkFetch(`/sdk/v1/assets/${encodeURIComponent(assetId)}`, { method: 'DELETE' });
  },

  assetFileUrl(assetId: string): string {
    const tok = getStoredAdminToken();
    const base = `/sdk/v1/assets/${encodeURIComponent(assetId)}/file`;
    return tok ? `${base}?token=${encodeURIComponent(tok)}` : base;
  },

  listMediaEngines(): Promise<MediaEngineSummary[]> {
    return sdkFetch<MediaEngineSummary[]>('/sdk/v1/media/engines');
  },

  async listEngineModels(instanceId: string, refresh = false): Promise<MediaModelInfo[]> {
    const res = await sdkFetch<{ models: MediaModelInfo[] }>(
      `/sdk/v1/media/instances/${encodeURIComponent(instanceId)}/models${refresh ? '?refresh=1' : ''}`,
    );
    return Array.isArray(res?.models) ? res.models : [];
  },

  getModelDefaults(instanceId: string, modelId: string): Promise<{ defaults?: Record<string, unknown>; schema?: Record<string, unknown> }> {
    return sdkFetch(`/sdk/v1/media/instances/${encodeURIComponent(instanceId)}/models/${encodeURIComponent(modelId)}/defaults`);
  },

  createMediaJob(req: MediaJobRequest): Promise<MediaJob> {
    return sdkFetch<MediaJob>('/sdk/v1/media/jobs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    });
  },

  listMediaJobs(): Promise<MediaJob[]> {
    return sdkFetch<MediaJob[]>('/sdk/v1/media/jobs');
  },

  cancelMediaJob(jobId: string): Promise<MediaJob> {
    return sdkFetch<MediaJob>(`/sdk/v1/media/jobs/${encodeURIComponent(jobId)}/cancel`, {
      method: 'POST',
    });
  },

  subscribeMediaJobEvents(jobId: string, onEvent: (evt: MediaJobEvent) => void, onError?: () => void): () => void {
    const tok = getStoredAdminToken();
    const url = `/sdk/v1/media/jobs/${encodeURIComponent(jobId)}/events${tok ? `?token=${encodeURIComponent(tok)}` : ''}`;
    const es = new EventSource(url);
    es.onmessage = (msg) => {
      try {
        const parsed = JSON.parse(msg.data) as MediaJobEvent;
        onEvent(parsed);
        if (parsed.type === 'done' || parsed.type === 'error' || parsed.type === 'canceled') {
          es.close();
        }
      } catch {
        // Ignore malformed SSE comment/ping frames.
      }
    };
    es.onerror = () => {
      onError?.();
    };
    return () => {
      es.close();
    };
  },

  enhancePrompt(req: { prompt: string; mode?: string; language?: 'en' | 'zh'; style?: string }): Promise<EnhancePromptResponse> {
    return sdkFetch<EnhancePromptResponse>('/sdk/v1/capabilities/llm.openai.v1/enhance-prompt', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    });
  },
};
