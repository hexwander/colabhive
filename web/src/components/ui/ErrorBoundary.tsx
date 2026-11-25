import React from 'react';

const CHUNK_RELOAD_STORAGE_KEY = 'colabhive.chunk_reload_at';
const CHUNK_RELOAD_COOLDOWN_MS = 15_000;

export function isChunkLoadError(err: unknown): boolean {
  const msg = err instanceof Error ? err.message : String(err ?? '');
  const lower = msg.toLowerCase();
  return (
    lower.includes('failed to fetch dynamically imported module') ||
    lower.includes('importing a module script failed') ||
    lower.includes('error loading dynamically imported module') ||
    lower.includes('loading chunk') ||
    lower.includes('chunkloaderror') ||
    lower.includes('mime type of "text/html"')
  );
}

/**
 * Triggers a single guarded page reload when a stale cached index.html references
 * a deleted hashed JS chunk after an upgrade. Returns true if a reload was initiated.
 */
export function attemptOneShotChunkReload(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    const prev = Number(window.sessionStorage.getItem(CHUNK_RELOAD_STORAGE_KEY) || '0');
    const now = Date.now();
    if (Number.isFinite(prev) && prev > 0 && now - prev < CHUNK_RELOAD_COOLDOWN_MS) {
      return false;
    }
    window.sessionStorage.setItem(CHUNK_RELOAD_STORAGE_KEY, String(now));
    window.location.reload();
    return true;
  } catch {
    return false;
  }
}

export function lazyWithRetry<T extends React.ComponentType<any>>(
  loader: () => Promise<{ default: T }>,
): React.LazyExoticComponent<T> {
  return React.lazy(async () => {
    try {
      return await loader();
    } catch (firstErr) {
      if (isChunkLoadError(firstErr)) {
        if (attemptOneShotChunkReload()) {
          // Hold suspense while the browser navigates to the refreshed page.
          return new Promise<{ default: T }>(() => {});
        }
      }
      throw firstErr;
    }
  });
}

interface ErrorBoundaryProps {
  children: React.ReactNode;
  scope?: 'root' | 'route';
  resetKey?: string;
  onResetHome?: () => void;
}

interface ErrorBoundaryState {
  hasError: boolean;
  errorMessage: string;
}

export class AppErrorBoundary extends React.Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = {
    hasError: false,
    errorMessage: '',
  };

  static getDerivedStateFromError(error: unknown): ErrorBoundaryState {
    const message = error instanceof Error ? error.message : String(error ?? 'Unknown render error');
    return {
      hasError: true,
      errorMessage: message.slice(0, 400),
    };
  }

  componentDidCatch(error: unknown): void {
    if (isChunkLoadError(error)) {
      attemptOneShotChunkReload();
    }
  }

  componentDidUpdate(prevProps: ErrorBoundaryProps): void {
    if (this.state.hasError && prevProps.resetKey !== this.props.resetKey) {
      this.setState({ hasError: false, errorMessage: '' });
    }
  }

  private handleReload = () => {
    if (typeof window !== 'undefined') {
      window.location.reload();
    }
  };

  private handleGoHome = () => {
    this.setState({ hasError: false, errorMessage: '' });
    if (this.props.onResetHome) {
      this.props.onResetHome();
      return;
    }
    if (typeof window !== 'undefined') {
      window.location.href = '/';
    }
  };

  render(): React.ReactNode {
    if (!this.state.hasError) {
      return this.props.children;
    }

    const isRoot = this.props.scope !== 'route';
    return (
      <div
        role="alert"
        style={{
          minHeight: isRoot ? '100dvh' : '320px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          padding: '24px',
          backgroundColor: 'var(--surface-0, #F7F7F5)',
          color: 'var(--text-primary, #18181B)',
          fontFamily:
            "'Plus Jakarta Sans', 'Noto Sans SC', -apple-system, BlinkMacSystemFont, sans-serif",
        }}
      >
        <div
          style={{
            width: '100%',
            maxWidth: '460px',
            borderRadius: '20px',
            border: '1px solid var(--border-subtle, #E4E4E0)',
            backgroundColor: 'var(--surface-1, #FFFFFF)',
            padding: '24px',
            boxShadow: '0 14px 36px -18px rgba(24, 24, 27, 0.16)',
          }}
        >
          <div style={{ fontSize: '16px', fontWeight: 700, marginBottom: '8px' }}>
            界面渲染遇到异常，已防止白屏
          </div>
          <p
            style={{
              fontSize: '13px',
              lineHeight: 1.6,
              color: 'var(--text-secondary, #52525B)',
              margin: '0 0 14px 0',
            }}
          >
            前端组件遇到未预期数据或缓存版本更新，可一键刷新恢复（后台算力与任务不受影响）。
          </p>
          {this.state.errorMessage && (
            <pre
              style={{
                fontSize: '11.5px',
                lineHeight: 1.45,
                padding: '10px 12px',
                borderRadius: '12px',
                backgroundColor: 'var(--surface-2, #F2F2EE)',
                color: 'var(--text-secondary, #52525B)',
                overflowX: 'auto',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
                margin: '0 0 16px 0',
              }}
            >
              {this.state.errorMessage}
            </pre>
          )}
          <div style={{ display: 'flex', gap: '10px', flexWrap: 'wrap' }}>
            <button
              type="button"
              onClick={this.handleReload}
              style={{
                height: '36px',
                padding: '0 16px',
                borderRadius: '10px',
                border: 'none',
                backgroundColor: '#18181B',
                color: '#FAF9F5',
                fontSize: '13px',
                fontWeight: 600,
                cursor: 'pointer',
              }}
            >
              立即刷新恢复
            </button>
            <button
              type="button"
              onClick={this.handleGoHome}
              style={{
                height: '36px',
                padding: '0 16px',
                borderRadius: '10px',
                border: '1px solid var(--border-subtle, #D4D4CE)',
                backgroundColor: 'transparent',
                color: 'var(--text-primary, #18181B)',
                fontSize: '13px',
                fontWeight: 600,
                cursor: 'pointer',
              }}
            >
              返回算力总览
            </button>
          </div>
        </div>
      </div>
    );
  }
}
