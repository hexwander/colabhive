import React, { useEffect, useId, useState } from 'react';
import { Minus, Square, Copy as RestoreIcon, X } from 'lucide-react';
import { api } from '../../lib/api';
import { cn } from './primitives';

// ============================================================================
// Viral Flagship 3-Cell Isometric Honeycomb Tensor Squircle Emblem
//   - Top Crown Cell:   Golden Colab Amber (#FBBF24 -> #F59E0B)
//   - Bottom-Left Cell: Crisp Warm Alabaster (#FAF9F5 -> #E5E3DC)
//   - Bottom-Right Cell: Live Compute Emerald (#34D399 -> #10B981)
// ============================================================================

export interface BrandMarkProps {
  size?: number;
  className?: string;
}

export function BrandMark({ size = 28, className }: BrandMarkProps) {
  const uid = useId().replace(/:/g, '');
  const bgGradId = `ch-bg-${uid}`;
  const rimGradId = `ch-rim-${uid}`;
  const amberGradId = `ch-amb-${uid}`;
  const alabasterGradId = `ch-alb-${uid}`;
  const emeraldGradId = `ch-em-${uid}`;

  const radius = Math.round(size * 0.285);

  return (
    <span
      aria-hidden="true"
      className={cn('inline-flex items-center justify-center shrink-0 select-none', className)}
      style={{
        width: size,
        height: size,
        borderRadius: radius,
        boxShadow:
          '0 1px 2px rgba(0, 0, 0, 0.2), 0 6px 14px -3px rgba(0, 0, 0, 0.24)',
      }}
    >
      <svg
        viewBox="0 0 28 28"
        width={size}
        height={size}
        fill="none"
        className="block w-full h-full"
      >
        <defs>
          <linearGradient id={bgGradId} x1="14" y1="0" x2="14" y2="28" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#242320" />
            <stop offset="100%" stopColor="#111110" />
          </linearGradient>
          <linearGradient id={rimGradId} x1="14" y1="0.5" x2="14" y2="27.5" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#FFF8EB" stopOpacity="0.22" />
            <stop offset="100%" stopColor="#FFF8EB" stopOpacity="0.05" />
          </linearGradient>
          <linearGradient id={amberGradId} x1="14" y1="4.2" x2="14" y2="14.0" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#FCD34D" />
            <stop offset="100%" stopColor="#F59E0B" />
          </linearGradient>
          <linearGradient id={alabasterGradId} x1="9.3" y1="12.3" x2="9.3" y2="22.1" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#FAF9F5" />
            <stop offset="100%" stopColor="#DFDDD4" />
          </linearGradient>
          <linearGradient id={emeraldGradId} x1="18.7" y1="12.3" x2="18.7" y2="22.1" gradientUnits="userSpaceOnUse">
            <stop offset="0%" stopColor="#34D399" />
            <stop offset="100%" stopColor="#10B981" />
          </linearGradient>
        </defs>

        {/* Squircle Titanium-Obsidian Base + Specular Inner Rim */}
        <rect x="0" y="0" width="28" height="28" rx="7.8" fill={`url(#${bgGradId})`} />
        <rect
          x="0.6"
          y="0.6"
          width="26.8"
          height="26.8"
          rx="7.2"
          stroke={`url(#${rimGradId})`}
          strokeWidth="1"
        />

        {/* Top Crown Cell — Golden Colab Amber */}
        <polygon
          points="14,4.35 18.11,6.73 18.11,11.48 14,13.85 9.89,11.48 9.89,6.73"
          fill={`url(#${amberGradId})`}
          stroke={`url(#${amberGradId})`}
          strokeWidth="0.55"
          strokeLinejoin="round"
        />

        {/* Bottom-Left Cell — Crisp Studio Alabaster */}
        <polygon
          points="9.3,12.45 13.41,14.83 13.41,19.58 9.3,21.95 5.19,19.58 5.19,14.83"
          fill={`url(#${alabasterGradId})`}
          stroke={`url(#${alabasterGradId})`}
          strokeWidth="0.55"
          strokeLinejoin="round"
        />

        {/* Bottom-Right Cell — Live Compute Emerald */}
        <polygon
          points="18.7,12.45 22.81,14.83 22.81,19.58 18.7,21.95 14.59,19.58 14.59,14.83"
          fill={`url(#${emeraldGradId})`}
          stroke={`url(#${emeraldGradId})`}
          strokeWidth="0.55"
          strokeLinejoin="round"
        />
      </svg>
    </span>
  );
}

// ============================================================================
// Frameless Native Desktop Window Controls Bridge (Windows Win32 + macOS Cocoa)
// ============================================================================

export interface WindowControlsState {
  frameless: boolean;
  platform: 'windows' | 'macos' | 'web';
  maximized: boolean;
  trafficLightInset: number;
}

interface NativeWindowGlobals extends Window {
  __COLABHIVE_WINDOW_CONTROLS__?: {
    frameless?: boolean;
    platform?: 'windows' | 'macos';
    maximized?: boolean;
    trafficLightInset?: number;
  };
  chrome?: {
    webview?: {
      postMessage: (msg: unknown) => void;
    };
  };
  webkit?: {
    messageHandlers?: {
      colabhive?: {
        postMessage: (msg: unknown) => void;
      };
    };
  };
}

function readWindowControlsState(): WindowControlsState {
  if (typeof window === 'undefined') {
    return { frameless: false, platform: 'web', maximized: false, trafficLightInset: 0 };
  }
  const win = window as NativeWindowGlobals;
  const cfg = win.__COLABHIVE_WINDOW_CONTROLS__;
  if (!cfg?.frameless) {
    return { frameless: false, platform: 'web', maximized: false, trafficLightInset: 0 };
  }
  const platform = cfg.platform === 'macos' ? 'macos' : 'windows';
  return {
    frameless: true,
    platform,
    maximized: Boolean(cfg.maximized),
    trafficLightInset:
      platform === 'macos' ? (typeof cfg.trafficLightInset === 'number' ? cfg.trafficLightInset : 76) : 0,
  };
}

export function postNativeDesktopMessage(payload: Record<string, unknown>): boolean {
  if (typeof window === 'undefined') return false;
  const win = window as NativeWindowGlobals;
  let sent = false;
  if (win.chrome?.webview && typeof win.chrome.webview.postMessage === 'function') {
    try {
      win.chrome.webview.postMessage(payload);
      sent = true;
    } catch {
      // Ignore WebView2 error
    }
  }
  if (
    win.webkit?.messageHandlers?.colabhive &&
    typeof win.webkit.messageHandlers.colabhive.postMessage === 'function'
  ) {
    try {
      win.webkit.messageHandlers.colabhive.postMessage(payload);
      sent = true;
    } catch {
      // Ignore WKWebView error
    }
  }
  return sent;
}

export function triggerNativeWindowAction(
  action: 'window-minimize' | 'window-toggle-maximize' | 'window-close' | 'window-drag',
): void {
  postNativeDesktopMessage({
    type: 'colabhive:desktop-action',
    action,
  });
  void api.desktopAction(action).catch(() => {});
}

export function useNativeWindowControls(): WindowControlsState {
  const [state, setState] = useState<WindowControlsState>(readWindowControlsState);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    setState(readWindowControlsState());

    const onWindowState = (e: Event) => {
      const detail = (e as CustomEvent<Partial<WindowControlsState>>).detail;
      if (!detail) return;
      setState((prev) => ({
        frameless: detail.frameless ?? prev.frameless,
        platform: (detail.platform as WindowControlsState['platform']) ?? prev.platform,
        maximized: detail.maximized ?? prev.maximized,
        trafficLightInset: detail.trafficLightInset ?? prev.trafficLightInset,
      }));
    };

    window.addEventListener('colabhive:window-state', onWindowState);
    return () => window.removeEventListener('colabhive:window-state', onWindowState);
  }, []);

  return state;
}

/**
 * Sleek native-feeling caption buttons rendered ONLY inside the frameless Windows
 * desktop app (`window.__COLABHIVE_WINDOW_CONTROLS__.platform === 'windows'`).
 */
export function WindowsCaptionButtons({ zh }: { zh: boolean }) {
  const winCtrl = useNativeWindowControls();
  if (!winCtrl.frameless || winCtrl.platform !== 'windows') {
    return null;
  }

  return (
    <div
      data-no-drag
      className="inline-flex items-center gap-0.5 pl-1.5 ml-1 border-l border-line-faint shrink-0"
    >
      <button
        type="button"
        onClick={() => triggerNativeWindowAction('window-minimize')}
        aria-label={zh ? '最小化窗口' : 'Minimize'}
        title={zh ? '最小化' : 'Minimize'}
        className="inline-flex items-center justify-center w-8 h-7 rounded-lg text-fg-secondary hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer bg-transparent border-0"
      >
        <Minus className="w-3.5 h-3.5" />
      </button>

      <button
        type="button"
        onClick={() => triggerNativeWindowAction('window-toggle-maximize')}
        aria-label={winCtrl.maximized ? (zh ? '向下还原' : 'Restore') : zh ? '最大化' : 'Maximize'}
        title={winCtrl.maximized ? (zh ? '向下还原' : 'Restore') : zh ? '最大化' : 'Maximize'}
        className="inline-flex items-center justify-center w-8 h-7 rounded-lg text-fg-secondary hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer bg-transparent border-0"
      >
        {winCtrl.maximized ? (
          <RestoreIcon className="w-3 h-3" />
        ) : (
          <Square className="w-3 h-3" />
        )}
      </button>

      <button
        type="button"
        onClick={() => triggerNativeWindowAction('window-close')}
        aria-label={zh ? '最小化到系统托盘' : 'Minimize to System Tray'}
        title={zh ? '隐藏到任务栏托盘（后台继续保活）' : 'Hide to System Tray (keeps GPUs alive)'}
        className="inline-flex items-center justify-center w-8 h-7 rounded-lg text-fg-secondary hover:text-white hover:bg-[var(--coral)] transition-colors cursor-pointer bg-transparent border-0"
      >
        <X className="w-3.5 h-3.5" />
      </button>
    </div>
  );
}
