package appstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/checkpoint"
	"github.com/colabhive/colabhive/internal/commtunnel"
	"github.com/colabhive/colabhive/internal/models"
)

func isColabPort8080RuntimeProxy(sess *models.RuntimeSession) bool {
	return commtunnel.IsColabPort8080RuntimeProxy(sess)
}

// isTrustedLoopbackHost reports whether hostPort refers strictly to a loopback IP (127.0.0.0/8, ::1)
// or RFC 6761 localhost / *.localhost host so forwarding Host/X-Forwarded-Host to Gradio/JupyterLab/ComfyUI
// keeps `window.gradio_config.root`, redirects, and same-origin WebSocket checks pinned to the exact bridge origin.
func isTrustedLoopbackHost(hostPort string) bool {
	trimmed := strings.TrimSpace(hostPort)
	if trimmed == "" {
		return false
	}
	host := trimmed
	if h, _, err := net.SplitHostPort(trimmed); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// isSameHostOrigin returns true only when rawOrigin is a clean http/https origin whose host:port
// exactly matches inHost, ensuring we never launder third-party cross-origin requests into 127.0.0.1.
func isSameHostOrigin(rawOrigin, inHost string) bool {
	orig := strings.TrimSpace(rawOrigin)
	host := strings.TrimSpace(inHost)
	if orig == "" || host == "" || strings.EqualFold(orig, "null") {
		return false
	}
	u, err := url.Parse(orig)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

func (m *Manager) getOrCreateCommTunnel(sessionID string, remotePort int) *commtunnel.Tunnel {
	key := sessionID + ":" + strconv.Itoa(remotePort)
	m.tunnelsMu.Lock()
	defer m.tunnelsMu.Unlock()
	if m.tunnels == nil {
		m.tunnels = make(map[string]*commtunnel.Tunnel)
	}
	if existing, ok := m.tunnels[key]; ok && existing != nil && !existing.IsClosed() {
		return existing
	}

	refreshSession := func(ctx context.Context) (*models.RuntimeSession, error) {
		if m.rt == nil {
			return nil, errors.New("runtime engine unavailable")
		}
		_, _ = m.rt.ExecuteCode(ctx, models.KernelExecRequest{
			SessionID:               sessionID,
			Code:                    "1",
			Silent:                  true,
			StoreHistory:            false,
			IdleTimeoutSec:          10,
			CallerName:              "appstore-tunnel-refresh",
			SuppressOutputBroadcast: true,
		})
		return m.rt.GetSession(ctx, sessionID)
	}

	t := commtunnel.New(commtunnel.Options{
		SessionID:  sessionID,
		RemotePort: remotePort,
		GetSession: func(ctx context.Context) (*models.RuntimeSession, error) {
			if m.rt == nil {
				return nil, errors.New("runtime engine unavailable")
			}
			sess, err := m.rt.GetSession(ctx, sessionID)
			if err != nil || sess == nil {
				return sess, err
			}
			if sess.ProxyToken == "" || (sess.ProxyTokenExpiresAt != nil && time.Until(*sess.ProxyTokenExpiresAt) <= 5*time.Minute) {
				if fresh, rErr := refreshSession(ctx); rErr == nil && fresh != nil {
					return fresh, nil
				}
			}
			return sess, nil
		},
		RefreshSession: refreshSession,
		GetEgressProxy: func(ctx context.Context, sess *models.RuntimeSession) string {
			if m.pool == nil || sess == nil || strings.TrimSpace(sess.AccountID) == "" {
				return ""
			}
			if _, proxyURL, err := m.pool.AcquireAccessToken(ctx, sess.AccountID); err == nil {
				return proxyURL
			}
			return ""
		},
	})
	m.tunnels[key] = t
	return t
}

func (m *Manager) closeCommTunnelsForSession(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	prefix := sessionID + ":"
	m.tunnelsMu.Lock()
	var toClose []*commtunnel.Tunnel
	for k, t := range m.tunnels {
		if strings.HasPrefix(k, prefix) {
			delete(m.tunnels, k)
			if t != nil {
				toClose = append(toClose, t)
			}
		}
	}
	m.tunnelsMu.Unlock()
	for _, t := range toClose {
		_ = t.Close()
	}
}

func (m *Manager) closeAllCommTunnels() {
	m.tunnelsMu.Lock()
	toClose := make([]*commtunnel.Tunnel, 0, len(m.tunnels))
	for k, t := range m.tunnels {
		delete(m.tunnels, k)
		if t != nil {
			toClose = append(toClose, t)
		}
	}
	m.tunnelsMu.Unlock()
	for _, t := range toClose {
		_ = t.Close()
	}
}

// tryCommTunnelReverseProxy proxies HTTP and WebSocket traffic over Colab's authenticated port-8080
// Jupyter Comm TCP tunnel directly to 127.0.0.1:<remotePort> on the remote Colab GPU VM.
func (m *Manager) tryCommTunnelReverseProxy(
	w http.ResponseWriter,
	r *http.Request,
	sess *models.RuntimeSession,
	remotePort int,
	escapedSubPath string,
	sanitizedQuery string,
	cookiePrefix string,
) bool {
	if !commtunnel.IsColabPort8080RuntimeProxy(sess) || remotePort <= 0 || remotePort > 65535 {
		return false
	}

	tunnel := m.getOrCreateCommTunnel(sess.ID, remotePort)
	targetHost := fmt.Sprintf("127.0.0.1:%d", remotePort)
	if escapedSubPath == "" {
		escapedSubPath = "/"
	}
	if !strings.HasPrefix(escapedSubPath, "/") {
		escapedSubPath = "/" + escapedSubPath
	}
	decodedSubPath, uErr := url.PathUnescape(escapedSubPath)
	if uErr != nil {
		decodedSubPath = escapedSubPath
	}

	inHost := strings.TrimSpace(r.Host)
	preserveLoopbackHost := isTrustedLoopbackHost(inHost)
	outHost := targetHost
	if preserveLoopbackHost {
		outHost = inHost
	}
	rp := &httputil.ReverseProxy{
		Transport:     tunnel.Transport(),
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = targetHost
			pr.Out.Host = outHost
			pr.Out.URL.Path = decodedSubPath
			pr.Out.URL.RawPath = escapedSubPath
			pr.Out.URL.RawQuery = sanitizedQuery

			cleanHdr := make(http.Header)
			for _, kv := range filterOutboundProxyHeaders(pr.In.Header, true, cookiePrefix) {
				cleanHdr.Add(kv[0], kv[1])
			}
			if preserveLoopbackHost {
				cleanHdr.Set("X-Forwarded-Host", inHost)
				cleanHdr.Set("X-Forwarded-Proto", "http")
			}
			// Preserve same-origin checks inside JupyterLab/ComfyUI/Gradio ONLY when caller Origin matches our own Host.
			if orig := pr.In.Header.Get("Origin"); isSameHostOrigin(orig, inHost) {
				cleanHdr.Set("Origin", "http://"+outHost)
			}
			pr.Out.Header = cleanHdr
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp == nil || len(resp.Header) == 0 {
				return nil
			}
			if cookiePrefix != "" {
				if rawSetCookies := resp.Header.Values("Set-Cookie"); len(rawSetCookies) > 0 {
					resp.Header.Del("Set-Cookie")
					for _, sc := range rawSetCookies {
						resp.Header.Add("Set-Cookie", scopeOutboundSetCookie(sc, cookiePrefix))
					}
				}
			}
			if loc := resp.Header.Get("Location"); loc != "" && inHost != "" {
				prefix := "http://" + targetHost
				if strings.HasPrefix(loc, prefix) {
					resp.Header.Set("Location", "http://"+inHost+strings.TrimPrefix(loc, prefix))
				}
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, err error) {
			writeBridgeJSONError(rw, http.StatusBadGateway, "Colab comm tunnel proxy error: "+checkpoint.RedactEmailStrings(err.Error()))
		},
	}
	rp.ServeHTTP(w, r)
	return true
}

// GetInstanceMediaClient returns an HTTP client and base URL targeting a running engine instance's
// `media.v1` HTTP/SSE API (e.g. `http://127.0.0.1:7861/colabhive/v1`) over ColabHive's multiplexed
// Comm Tunnel v3 (or direct runtime proxy / localhost bridge in non-Colab or test environments).
// It also refreshes `LastActiveAt` so active Media Studio sessions are not idle-stopped.
func (m *Manager) GetInstanceMediaClient(ctx context.Context, instanceID string) (*http.Client, string, *models.AppInstance, error) {
	targetID := strings.TrimSpace(instanceID)
	m.mu.Lock()
	mi, ok := m.instances[targetID]
	if !ok || mi == nil || mi.inst == nil {
		m.mu.Unlock()
		return nil, "", nil, ErrAppInstanceNotFound
	}
	if mi.inst.State != models.AppStateRunning {
		st := mi.inst.State
		m.mu.Unlock()
		return nil, "", nil, fmt.Errorf("engine instance %s is not running (state=%s)", targetID, st)
	}
	mi.inst.LastActiveAt = time.Now().UTC()
	instSnap := cloneAppInstance(mi.inst)
	m.mu.Unlock()

	apiBase := strings.TrimRight(strings.TrimSpace(instSnap.MediaAPIBase), "/")
	if apiBase == "" {
		return nil, "", instSnap, fmt.Errorf("engine instance %s does not expose a headless media.v1 API (launch_mode=%s)", targetID, instSnap.LaunchMode)
	}
	if m.rt == nil || strings.TrimSpace(instSnap.SessionID) == "" {
		return nil, "", instSnap, errors.New("engine instance has no active cloud runtime session")
	}

	sess, err := m.rt.GetSession(ctx, instSnap.SessionID)
	if err != nil || sess == nil {
		if err == nil {
			err = errors.New("runtime session unavailable")
		}
		return nil, "", instSnap, err
	}

	// Real Google Colab Port-8080 Comm Tunnel v3 path (zlib + credit flow control).
	if commtunnel.IsColabPort8080RuntimeProxy(sess) {
		tunnel := m.getOrCreateCommTunnel(sess.ID, instSnap.RemotePort)
		base := fmt.Sprintf("http://127.0.0.1:%d%s", instSnap.RemotePort, apiBase)
		return &http.Client{Transport: tunnel.Transport()}, base, instSnap, nil
	}

	// Direct HTTP proxy URL path (used in tests and direct-routed GPU servers).
	if pURL := strings.TrimRight(strings.TrimSpace(sess.ProxyURL), "/"); strings.HasPrefix(strings.ToLower(pURL), "http://") || strings.HasPrefix(strings.ToLower(pURL), "https://") {
		tr := &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     60 * time.Second,
		}
		return &http.Client{Transport: tr}, pURL + apiBase, instSnap, nil
	}

	// Local bridge fallback if bound.
	if instSnap.LocalBridgeActive && strings.TrimSpace(instSnap.LocalURL) != "" {
		return &http.Client{}, strings.TrimRight(instSnap.LocalURL, "/") + apiBase, instSnap, nil
	}

	return nil, "", instSnap, errors.New("no reachable transport for engine media.v1 API")
}
