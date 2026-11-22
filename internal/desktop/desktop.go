// Package desktop implements ColabHive's Native Desktop Shell across Windows
// (pure-Go `CGO_ENABLED=0` Frameless Win32 + WebView2 + Magpie `/tray` Flyout)
// and macOS Apple Silicon / Intel (`ColabHive.app` Frameless Cocoa + `WKWebView` + Menu Bar `NSPopover`).
package desktop

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/server"
)

const (
	// SingletonMutexName enforces single-instance desktop tray execution per Windows session.
	SingletonMutexName = `Local\ColabHiveDesktopSingleton`

	// TrayHostClassName is the Win32 window class registered for IPC wakeup and Shell_NotifyIconW callbacks.
	TrayHostClassName = "ColabHiveDesktopTrayHost"

	// MainWindowClassName is the Win32 window class for the 1380x880 ColabHive desktop shell.
	MainWindowClassName = "ColabHiveDesktopMainWindow"

	// TrayFlyoutClassName is the Win32 window class for the 396x580 borderless Magpie tray popover.
	TrayFlyoutClassName = "ColabHiveDesktopTrayFlyout"

	// DefaultMainLogicalWidth and DefaultMainLogicalHeight define the 96-DPI main window dimensions.
	DefaultMainLogicalWidth  = 1380
	DefaultMainLogicalHeight = 880

	// DefaultFlyoutLogicalWidth and DefaultFlyoutLogicalHeight define the 96-DPI Magpie tray popover size.
	DefaultFlyoutLogicalWidth  = 396
	DefaultFlyoutLogicalHeight = 580

	// DefaultFlyoutGapPx is the logical pixel gap between the taskbar edge and the tray flyout.
	DefaultFlyoutGapPx = 8

	// DefaultMacOSTrafficLightInsetPx is the left inset reserved for macOS native 🔴🟡🟢 window controls.
	DefaultMacOSTrafficLightInsetPx = 76
)

// Config configures the ColabHive Native Desktop & System Tray / Menu Bar application.
type Config struct {
	Version         string
	Commit          string
	BuildDate       string
	Mode            string
	Host            string
	Port            int
	DataDir         string
	DBPath          string
	WebDir          string
	AttachURL       string
	AdminToken      string
	MasterKey       string
	DefaultStrategy string
	KeepaliveSec    int
	Demo            bool
	StartMinimized  bool
}

// NormalizeRoute sanitizes an SPA route string from WebView2/WKWebView or `/api/v1/desktop/action`
// into a clean leading-slash path (e.g. `"/router"`, `"/pool"`, `"/exp/abc"`).
func NormalizeRoute(route string) string {
	r := strings.TrimSpace(route)
	if r == "" {
		return "/"
	}
	lower := strings.ToLower(r)
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "vbscript:") {
		return "/"
	}
	// Strip full origin if caller passed `http://127.0.0.1:7860/router`.
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if schemeIdx := strings.Index(r, "://"); schemeIdx >= 0 {
			rest := r[schemeIdx+3:]
			if slashIdx := strings.IndexByte(rest, '/'); slashIdx >= 0 {
				r = rest[slashIdx:]
			} else {
				return "/"
			}
		} else {
			return "/"
		}
	}
	if !strings.HasPrefix(r, "/") {
		r = "/" + r
	}
	return r
}

func joinBaseAndRoute(baseURL, route string) string {
	normRoute := NormalizeRoute(route)
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return normRoute
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		if u.Host == "" {
			return normRoute
		}
		return u.Scheme + "://" + u.Host + normRoute
	}
	b := strings.TrimRight(raw, "/")
	if b == "" {
		return normRoute
	}
	if len(b) > 8 {
		if idx := strings.Index(b[8:], "/"); idx >= 0 {
			b = b[:8+idx]
		}
	}
	return b + normRoute
}

// webViewBridgeEnvelope represents JSON messages sent via WebView2 `window.chrome.webview.postMessage(...)`
// or macOS `window.webkit.messageHandlers.colabhive.postMessage(...)`.
type webViewBridgeEnvelope struct {
	Type    string `json:"type"`
	ReqID   string `json:"req_id"`
	Action  string `json:"action"`
	Route   string `json:"route"`
	Path    string `json:"path"`
	Edge    string `json:"edge"`
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled"`
}

// ParseWebViewMessage parses a raw WebView2/WKWebView JSON or string payload
// into a `server.DesktopActionRequest` (or `"open-external"` with `externalURL`).
func ParseWebViewMessage(raw string) (req server.DesktopActionRequest, externalURL string, ok bool) {
	req, externalURL, _, ok = ParseWebViewMessageWithAck(raw)
	return req, externalURL, ok
}

// ParseWebViewMessageWithAck parses a raw WebView message along with an optional `req_id` correlation token.
func ParseWebViewMessageWithAck(raw string) (req server.DesktopActionRequest, externalURL string, reqID string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" || trimmed == "undefined" {
		return req, "", "", false
	}

	// Case 1: JSON string literal (e.g. `"\"open-main\""` or `"\"{\\\"action\\\":\\\"quit\\\"}\""` from GetWebMessageAsJSON)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			inner := strings.TrimSpace(unquoted)
			if strings.HasPrefix(inner, "{") {
				trimmed = inner
			} else {
				req, externalURL, ok = parsePlainActionToken(inner)
				return req, externalURL, "", ok
			}
		}
	}

	// Case 2: JSON object payload (`{ "type": "colabhive:desktop-action", "action": "open-route", "route": "/router" }`)
	if strings.HasPrefix(trimmed, "{") {
		var env webViewBridgeEnvelope
		if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
			return req, "", "", false
		}
		reqID = strings.TrimSpace(env.ReqID)
		act := strings.ToLower(strings.TrimSpace(env.Action))
		if act == "" {
			return req, "", reqID, false
		}
		if act == "open-external" {
			u := strings.TrimSpace(env.URL)
			if u == "" {
				u = strings.TrimSpace(env.Route)
			}
			if strings.HasPrefix(strings.ToLower(u), "http://") || strings.HasPrefix(strings.ToLower(u), "https://") {
				req.Action = "open-external"
				return req, u, reqID, true
			}
			return req, "", reqID, false
		}
		route := strings.TrimSpace(env.Route)
		if route == "" {
			route = strings.TrimSpace(env.Path)
		}
		if act == "window-resize" && strings.TrimSpace(env.Edge) != "" {
			route = strings.TrimSpace(env.Edge)
		}
		normRoute := NormalizeRoute(route)
		req = server.DesktopActionRequest{
			Action:  act,
			Route:   normRoute,
			Path:    normRoute,
			Enabled: env.Enabled,
		}
		return req, "", reqID, isSupportedDesktopAction(act)
	}

	// Case 3: Plain action string (`"open-main"`, `"window-minimize"`, `"quit"`, etc.)
	req, externalURL, ok = parsePlainActionToken(trimmed)
	return req, externalURL, "", ok
}

func parsePlainActionToken(token string) (req server.DesktopActionRequest, externalURL string, ok bool) {
	act := strings.ToLower(strings.TrimSpace(token))
	if !isSupportedDesktopAction(act) {
		return req, "", false
	}
	req = server.DesktopActionRequest{
		Action: act,
		Route:  "/",
		Path:   "/",
	}
	return req, "", true
}

func isSupportedDesktopAction(action string) bool {
	switch action {
	case "open-main", "open-route", "hide-tray", "show-tray", "toggle-tray",
		"quit", "toggle-autostart", "set-autostart", "state",
		"window-drag", "start-drag", "window-minimize",
		"window-maximize", "window-toggle-maximize", "window-close",
		"window-resize":
		return true
	default:
		return false
	}
}

// IsWindowControlAction reports whether `action` manipulates the frameless native main window chrome.
func IsWindowControlAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "window-drag", "start-drag", "window-minimize",
		"window-maximize", "window-toggle-maximize", "window-close", "window-resize":
		return true
	default:
		return false
	}
}

// BuildNativeWebViewBootstrapJS constructs the document-start JavaScript bridge injected into
// Windows Edge WebView2 (`platform="windows"`) and macOS WKWebView (`platform="macos"`).
// On the Main Window (`isTray == false`), it also exposes `window.__COLABHIVE_WINDOW_CONTROLS__`
// and binds `[data-colabhive-drag]` native titlebar drag/double-click-zoom + 8-way frameless resize edges.
func BuildNativeWebViewBootstrapJS(platform string, autostart bool, isTray bool) string {
	plat := strings.ToLower(strings.TrimSpace(platform))
	if plat != "macos" {
		plat = "windows"
	}
	autoLiteral := "false"
	if autostart {
		autoLiteral = "true"
	}
	isTrayLiteral := "false"
	if isTray {
		isTrayLiteral = "true"
	}
	insetLiteral := "0"
	if plat == "macos" {
		insetLiteral = "76"
	}
	platJSON, _ := json.Marshal(plat)

	const tpl = `(function(){
  if (window.__COLABHIVE_DESKTOP_BOOTSTRAPPED__) return;
  window.__COLABHIVE_DESKTOP_BOOTSTRAPPED__ = true;
  var PLATFORM = __PLATFORM_LITERAL__;
  var IS_TRAY_SURFACE = __IS_TRAY_LITERAL__;
  var st = window.__COLABHIVE_DESKTOP__ = window.__COLABHIVE_DESKTOP__ || {
    shell: true,
    platform: PLATFORM,
    autostart: __AUTOSTART_LITERAL__
  };
  st.shell = true;
  st.platform = PLATFORM;

  var nextSeq = 1;
  var pending = {};
  window.__COLABHIVE_DESKTOP_RESOLVE__ = function(id, payload) {
    var entry = pending[id];
    if (!entry) return;
    delete pending[id];
    clearTimeout(entry.timer);
    entry.resolve(payload || {});
  };

  function hasNativeWebView() {
    if (window.chrome && window.chrome.webview && typeof window.chrome.webview.postMessage === 'function') {
      return true;
    }
    if (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.colabhive && typeof window.webkit.messageHandlers.colabhive.postMessage === 'function') {
      return true;
    }
    return false;
  }

  function postNativeMessage(msg) {
    if (window.chrome && window.chrome.webview && typeof window.chrome.webview.postMessage === 'function') {
      window.chrome.webview.postMessage(msg);
      return true;
    }
    if (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.colabhive && typeof window.webkit.messageHandlers.colabhive.postMessage === 'function') {
      window.webkit.messageHandlers.colabhive.postMessage(msg);
      return true;
    }
    return false;
  }

  function isWindowAction(act) {
    return act === 'window-drag' || act === 'start-drag' || act === 'window-minimize' ||
      act === 'window-maximize' || act === 'window-toggle-maximize' || act === 'window-close' ||
      act === 'window-resize';
  }

  function dispatchViaWebView(p) {
    var act = String((p && p.action) || '').toLowerCase();
    var route = String((p && (p.route || p.path)) || '/');
    var edge = String((p && p.edge) || '');
    var reqId = 'wv_' + Date.now() + '_' + (nextSeq++);
    return new Promise(function(resolve) {
      if (act === 'quit' || act === 'window-drag' || act === 'start-drag' || act === 'window-resize') {
        try {
          postNativeMessage({
            type: 'colabhive:desktop-action',
            req_id: act === 'quit' ? reqId : '',
            action: act,
            route: route,
            path: route,
            edge: edge
          });
        } catch (err) {
          resolve({ error: String(err && err.message ? err.message : err) });
          return;
        }
        resolve({
          ok: true,
          handled: true,
          state: {
            desktop_shell: true,
            autostart_enabled: Boolean(st.autostart),
            tray_visible: false,
            last_action: act
          }
        });
        return;
      }
      var timer = setTimeout(function() {
        if (!pending[reqId]) return;
        delete pending[reqId];
        resolve({ error: 'desktop WebView2 bridge timed out' });
      }, 2500);
      pending[reqId] = { resolve: resolve, timer: timer };
      try {
        var msg = {
          type: 'colabhive:desktop-action',
          req_id: reqId,
          action: act,
          route: route,
          path: route
        };
        if (edge) msg.edge = edge;
        if (p && typeof p.enabled === 'boolean') {
          msg.enabled = p.enabled;
        }
        postNativeMessage(msg);
      } catch (err) {
        clearTimeout(timer);
        delete pending[reqId];
        resolve({ error: String(err && err.message ? err.message : err) });
      }
    });
  }

  if (!IS_TRAY_SURFACE && window.location.pathname.indexOf('/tray') !== 0) {
    var winCtrl = window.__COLABHIVE_WINDOW_CONTROLS__ = {
      frameless: true,
      platform: PLATFORM,
      maximized: false,
      trafficLightInset: __TRAFFIC_INSET_LITERAL__,
      startDrag: function() {
        postNativeMessage({ type: 'colabhive:desktop-action', action: 'window-drag' });
      },
      minimize: function() {
        return dispatchViaWebView({ action: 'window-minimize' });
      },
      maximize: function() {
        return dispatchViaWebView({ action: 'window-toggle-maximize' });
      },
      toggleMaximize: function() {
        return dispatchViaWebView({ action: 'window-toggle-maximize' });
      },
      close: function() {
        return dispatchViaWebView({ action: 'window-close' });
      }
    };

    window.addEventListener('colabhive:window-state', function(ev) {
      if (ev && ev.detail && typeof ev.detail.maximized === 'boolean') {
        winCtrl.maximized = ev.detail.maximized;
      }
    });

    var INTERACTIVE_SELECTOR = 'button, a, input, select, textarea, [role="button"], [data-no-drag], .no-drag, [contenteditable="true"]';
    var DRAG_REGION_SELECTOR = '[data-colabhive-drag], [data-window-drag]';

    function detectWindowsResizeEdge(clientX, clientY) {
      if (PLATFORM !== 'windows' || winCtrl.maximized) return '';
      var w = window.innerWidth || 0;
      var h = window.innerHeight || 0;
      if (w <= 0 || h <= 0) return '';
      var border = 5;
      var corner = 10;
      var nearLeft = clientX >= 0 && clientX < border;
      var nearRight = clientX <= w && clientX > w - border;
      var nearTop = clientY >= 0 && clientY < border;
      var nearBottom = clientY <= h && clientY > h - border;
      if (!nearLeft && !nearRight && !nearTop && !nearBottom) return '';
      var inCornerLeft = clientX < corner;
      var inCornerRight = clientX > w - corner;
      var inCornerTop = clientY < corner;
      var inCornerBottom = clientY > h - corner;
      if ((nearTop && inCornerLeft) || (nearLeft && inCornerTop)) return 'top-left';
      if ((nearTop && inCornerRight) || (nearRight && inCornerTop)) return 'top-right';
      if ((nearBottom && inCornerLeft) || (nearLeft && inCornerBottom)) return 'bottom-left';
      if ((nearBottom && inCornerRight) || (nearRight && inCornerBottom)) return 'bottom-right';
      if (nearTop) return 'top';
      if (nearBottom) return 'bottom';
      if (nearLeft) return 'left';
      if (nearRight) return 'right';
      return '';
    }

    if (PLATFORM === 'windows') {
      var activeEdgeCursor = '';
      document.addEventListener('mousemove', function(ev) {
        var edge = detectWindowsResizeEdge(ev.clientX, ev.clientY);
        var nextCursor = '';
        if (edge === 'top-left' || edge === 'bottom-right') nextCursor = 'nwse-resize';
        else if (edge === 'top-right' || edge === 'bottom-left') nextCursor = 'nesw-resize';
        else if (edge === 'left' || edge === 'right') nextCursor = 'ew-resize';
        else if (edge === 'top' || edge === 'bottom') nextCursor = 'ns-resize';
        if (nextCursor !== activeEdgeCursor) {
          activeEdgeCursor = nextCursor;
          if (document.documentElement) {
            document.documentElement.style.cursor = nextCursor;
          }
        }
      }, true);
    }

    document.addEventListener('mousedown', function(ev) {
      if (!ev || ev.button !== 0) return;
      var resizeEdge = detectWindowsResizeEdge(ev.clientX, ev.clientY);
      if (resizeEdge) {
        ev.preventDefault();
        ev.stopPropagation();
        postNativeMessage({ type: 'colabhive:desktop-action', action: 'window-resize', edge: resizeEdge });
        return;
      }
      if (ev.detail !== 1) return;
      var target = ev.target && ev.target.closest ? ev.target : null;
      if (!target) return;
      var dragEl = target.closest(DRAG_REGION_SELECTOR);
      if (!dragEl) return;
      if (target.closest(INTERACTIVE_SELECTOR)) return;
      postNativeMessage({ type: 'colabhive:desktop-action', action: 'window-drag' });
    }, true);

    document.addEventListener('dblclick', function(ev) {
      if (!ev || ev.button !== 0) return;
      if (detectWindowsResizeEdge(ev.clientX, ev.clientY)) return;
      var target = ev.target && ev.target.closest ? ev.target : null;
      if (!target) return;
      var dragEl = target.closest(DRAG_REGION_SELECTOR);
      if (!dragEl) return;
      if (target.closest(INTERACTIVE_SELECTOR)) return;
      ev.preventDefault();
      postNativeMessage({ type: 'colabhive:desktop-action', action: 'window-toggle-maximize' });
    }, true);
  }

  var origFetch = typeof window.fetch === 'function' ? window.fetch.bind(window) : null;
  if (origFetch) {
    window.fetch = function(input, init) {
      var u = '';
      try {
        u = typeof input === 'string' ? input : (input && input.url ? input.url : '');
      } catch (_) {}
      if (u.indexOf('/api/v1/desktop/state') !== -1) {
        return origFetch(input, init).then(function(resp) {
          if (!resp.ok) {
            if (!hasNativeWebView()) return resp;
            return dispatchViaWebView({ action: 'state' }).then(function(ack) {
              if (ack && ack.state && typeof ack.state.autostart_enabled === 'boolean') {
                st.autostart = ack.state.autostart_enabled;
              }
              return new Response(JSON.stringify({
                desktop_shell: true,
                native: true,
                bridge_active: true,
                platform: PLATFORM,
                frameless: !IS_TRAY_SURFACE,
                autostart: Boolean(st.autostart),
                autostart_enabled: Boolean(st.autostart),
                main_window_visible: ack && ack.state ? Boolean(ack.state.main_window_visible) : false,
                tray_visible: ack && ack.state ? Boolean(ack.state.tray_visible) : true
              }), { status: 200, headers: { 'Content-Type': 'application/json' } });
            });
          }
          return resp.clone().json().then(function(data) {
            if (data && data.desktop_shell === true) {
              if (typeof data.autostart_enabled === 'boolean') {
                st.autostart = data.autostart_enabled;
              } else if (typeof data.autostart === 'boolean') {
                st.autostart = data.autostart;
              }
              var merged = Object.assign({}, data, {
                native: true,
                bridge_active: true,
                platform: PLATFORM,
                frameless: !IS_TRAY_SURFACE,
                autostart: Boolean(st.autostart),
                autostart_enabled: Boolean(st.autostart)
              });
              return new Response(JSON.stringify(merged), {
                status: 200,
                headers: { 'Content-Type': 'application/json' }
              });
            }
            if (!hasNativeWebView()) return resp;
            return dispatchViaWebView({ action: 'state' }).then(function(ack) {
              if (ack && ack.state && typeof ack.state.autostart_enabled === 'boolean') {
                st.autostart = ack.state.autostart_enabled;
              }
              return new Response(JSON.stringify({
                desktop_shell: true,
                native: true,
                bridge_active: true,
                platform: PLATFORM,
                frameless: !IS_TRAY_SURFACE,
                autostart: Boolean(st.autostart),
                autostart_enabled: Boolean(st.autostart),
                main_window_visible: ack && ack.state ? Boolean(ack.state.main_window_visible) : false,
                tray_visible: ack && ack.state ? Boolean(ack.state.tray_visible) : true,
                server_url: (data && data.server_url) || (ack && ack.state && ack.state.server_url) || ''
              }), { status: 200, headers: { 'Content-Type': 'application/json' } });
            });
          }).catch(function() { return resp; });
        });
      }
      if (u.indexOf('/api/v1/desktop/action') !== -1) {
        var raw = init && typeof init.body === 'string' ? init.body : '{}';
        var p = {};
        try { p = JSON.parse(raw || '{}'); } catch (_) {}
        var act = String(p.action || '').toLowerCase();
        var route = String(p.route || p.path || '/');
        if (isWindowAction(act) && hasNativeWebView()) {
          return dispatchViaWebView(p).then(function(ack) {
            if (ack && ack.error) {
              return new Response(JSON.stringify({ error: ack.error }), {
                status: 500,
                headers: { 'Content-Type': 'application/json' }
              });
            }
            var ackState = (ack && ack.state) || {
              desktop_shell: true,
              autostart_enabled: Boolean(st.autostart),
              main_window_visible: act !== 'window-close',
              tray_visible: false,
              last_action: act
            };
            return new Response(JSON.stringify({
              ok: true,
              handled: true,
              bridge_active: true,
              native: true,
              desktop_shell: true,
              action: act,
              route: route,
              autostart: Boolean(st.autostart),
              state: ackState
            }), { status: 200, headers: { 'Content-Type': 'application/json' } });
          });
        }
        return origFetch(input, init).then(function(resp) {
          if (!resp.ok) {
            if (!hasNativeWebView()) return resp;
            return dispatchViaWebView(p).then(function(ack) {
              if (ack && ack.error) return resp;
              var fallbackState = (ack && ack.state) || {
                desktop_shell: true,
                autostart_enabled: Boolean(st.autostart),
                tray_visible: true,
                last_action: act
              };
              return new Response(JSON.stringify({
                ok: true,
                handled: true,
                bridge_active: true,
                native: true,
                desktop_shell: true,
                action: act,
                route: route,
                autostart: Boolean(st.autostart),
                state: fallbackState
              }), { status: 200, headers: { 'Content-Type': 'application/json' } });
            });
          }
          return resp.clone().json().then(function(data) {
            if (data && data.handled === true) {
              if (data.state && typeof data.state.autostart_enabled === 'boolean') {
                st.autostart = data.state.autostart_enabled;
              } else if (typeof data.autostart === 'boolean') {
                st.autostart = data.autostart;
              }
              var out = Object.assign({}, data, {
                bridge_active: true,
                native: true,
                autostart: Boolean(st.autostart)
              });
              return new Response(JSON.stringify(out), {
                status: 200,
                headers: { 'Content-Type': 'application/json' }
              });
            }
            if (data && data.handled === false && hasNativeWebView()) {
              return dispatchViaWebView(p).then(function(ack) {
                if (ack && ack.error) {
                  return new Response(JSON.stringify({ error: ack.error }), {
                    status: 500,
                    headers: { 'Content-Type': 'application/json' }
                  });
                }
                var ackState = (ack && ack.state) || {
                  desktop_shell: true,
                  autostart_enabled: Boolean(st.autostart),
                  tray_visible: true,
                  last_action: act
                };
                st.autostart = Boolean(ackState.autostart_enabled);
                return new Response(JSON.stringify({
                  ok: true,
                  handled: true,
                  bridge_active: true,
                  native: true,
                  desktop_shell: true,
                  action: act,
                  route: route,
                  autostart: Boolean(st.autostart),
                  state: ackState
                }), { status: 200, headers: { 'Content-Type': 'application/json' } });
              });
            }
            return resp;
          }).catch(function() { return resp; });
        });
      }
      return origFetch(input, init);
    };
  }

  var origOpen = typeof window.open === 'function' ? window.open.bind(window) : null;
  window.open = function(url, target, features) {
    try {
      var u = String(url || '');
      if (/^https?:\/\//i.test(u) && u.indexOf(window.location.origin) !== 0) {
        if (postNativeMessage({ type: 'colabhive:desktop-action', action: 'open-external', url: u })) {
          return null;
        }
      }
    } catch (_) {}
    return origOpen ? origOpen(url, target, features) : null;
  };
  document.addEventListener('click', function(ev) {
    try {
      var a = ev.target && ev.target.closest ? ev.target.closest('a[target="_blank"]') : null;
      if (!a) return;
      var href = a.href || '';
      if (/^https?:\/\//i.test(href) && href.indexOf(window.location.origin) !== 0) {
        if (hasNativeWebView()) {
          ev.preventDefault();
          postNativeMessage({ type: 'colabhive:desktop-action', action: 'open-external', url: href });
        }
      }
    } catch (_) {}
  }, true);
})();`

	out := strings.Replace(tpl, "__PLATFORM_LITERAL__", string(platJSON), 1)
	out = strings.Replace(out, "__IS_TRAY_LITERAL__", isTrayLiteral, 1)
	out = strings.Replace(out, "__AUTOSTART_LITERAL__", autoLiteral, 1)
	out = strings.Replace(out, "__TRAFFIC_INSET_LITERAL__", insetLiteral, 1)
	return out
}

// FormatTrayTooltip formats the concise Chinese telemetry tooltip for `NOTIFYICONDATAW.szTip[128]`
// (e.g. `"ColabHive · 剩余 633.2 CU · 3 台实例运行中"`), safely truncated below 127 UTF-16 code units.
func FormatTrayTooltip(summary *models.QuotaSummary, connected bool) string {
	var text string
	if !connected || summary == nil {
		text = "ColabHive · 本地算力网关运行中"
	} else {
		text = fmt.Sprintf(
			"ColabHive · 剩余 %.1f CU · %d 台实例运行中",
			summary.TotalAvailableCU,
			summary.ActiveVMs,
		)
	}
	return TruncateUTF16Units(text, 120)
}

// FormatPoolMenuLabel formats the live status item in the Right-Click Context Menu.
func FormatPoolMenuLabel(summary *models.QuotaSummary, connected bool) string {
	if !connected || summary == nil {
		return "算力池: 查看账号与实时额度"
	}
	return fmt.Sprintf("算力池: 剩余 %.1f CU · %d 台运行中", summary.TotalAvailableCU, summary.ActiveVMs)
}

// TruncateUTF16Units ensures a string encodes into at most `maxUnits` UTF-16 code units
// (excluding terminating NUL) so Win32 fixed-size buffers like `szTip[128]` never overflow.
func TruncateUTF16Units(s string, maxUnits int) string {
	if maxUnits <= 0 {
		return ""
	}
	encoded := utf16.Encode([]rune(s))
	if len(encoded) <= maxUnits {
		return s
	}
	var out []rune
	used := 0
	for _, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if used+w > maxUnits {
			break
		}
		out = append(out, r)
		used += w
	}
	return string(out)
}

// ToggleDebouncer prevents back-to-back duplicate toggles when `TrayPopoverPage.tsx`
// fires both `postMessage` and `POST /api/v1/desktop/action` for the same click.
type ToggleDebouncer struct {
	mu     sync.Mutex
	lastAt map[string]time.Time
	window time.Duration
}

// NewToggleDebouncer creates a debouncer with the specified suppression window.
func NewToggleDebouncer(window time.Duration) *ToggleDebouncer {
	if window <= 0 {
		window = 400 * time.Millisecond
	}
	return &ToggleDebouncer{
		lastAt: make(map[string]time.Time),
		window: window,
	}
}

// ShouldExecute records an action and returns false if a duplicate non-idempotent toggle
// already executed within `window`.
func (d *ToggleDebouncer) ShouldExecute(key string, now time.Time) bool {
	return d.ShouldExecuteWithin(key, now, 0)
}

// ShouldExecuteWithin records an action and returns false if `key` already executed within `customWindow`
// (falling back to `d.window` when `customWindow <= 0`).
func (d *ToggleDebouncer) ShouldExecuteWithin(key string, now time.Time, customWindow time.Duration) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := customWindow
	if w <= 0 {
		w = d.window
	}
	if prev, ok := d.lastAt[key]; ok && now.Sub(prev) < w {
		return false
	}
	d.lastAt[key] = now
	return true
}

// Rollback clears the last execution timestamp for `key` so a failed toggle does not suppress retries.
func (d *ToggleDebouncer) Rollback(key string) {
	d.mu.Lock()
	delete(d.lastAt, key)
	d.mu.Unlock()
}
