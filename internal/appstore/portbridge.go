package appstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

const (
	bridgeStdoutMarker        = "__COLABHIVE_BRIDGE_JSON__:"
	bridgeMaxReqBodyBytes     = 16 << 20  // 16 MiB max request body for kernel HTTP bridge
	bridgeMaxStdoutFrameBytes = 384 << 10 // 384 KiB max stdout frame for bounded kernel HTTP bridge
	bridgeShutdownGraceDur    = 2 * time.Second
)

var (
	errBridgeBodyTooLarge  = errors.New("proxy request body exceeds 16 MiB limit")
	errBridgeFrameOversize = errors.New("upstream bridge response exceeded 384 KiB bounded stdout frame limit or was truncated")
)

var hopByHopHeaderNames = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

// strippedOutboundHeaderNames strips ColabHive control-plane auth headers and proxy framing headers.
// App cookies (`Cookie`, `Set-Cookie`) and WebSocket upgrade headers (`Upgrade`, `Connection: Upgrade`,
// `Sec-WebSocket-*`) are intentionally PRESERVED so authenticated & real-time Studio apps (ComfyUI,
// JupyterLab, Open WebUI, Gradio) work cleanly on their isolated localhost bridge origin.
var strippedOutboundHeaderNames = map[string]struct{}{
	"x-colabhive-key":             {},
	"x-api-key":                   {},
	"x-colab-runtime-proxy-token": {},
	"host":                        {},
	"content-length":              {},
	"origin":                      {},
	"referer":                     {},
	"forwarded":                   {},
	"x-forwarded-for":             {},
	"x-forwarded-host":            {},
	"x-forwarded-proto":           {},
	"x-real-ip":                   {},
}

var strippedControlPlaneQueryParams = map[string]struct{}{
	"token":   {},
	"key":     {},
	"api_key": {},
	"sig":     {},
	"exp":     {},
}

var (
	activeBridgeHostsMu sync.Mutex
	activeBridgeHosts   = make(map[string]struct{})
)

type trackedConn struct {
	net.Conn
	onClose func()
	once    sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.onClose)
	return err
}

// trackingListener tracks every accepted TCP connection (including hijacked WebSockets) so
// localBridge.Close() deterministically severs all in-flight writes and upgraded sockets before checkpoint sync!
type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newTrackingListener(ln net.Listener) *trackingListener {
	return &trackingListener{
		Listener: ln,
		conns:    make(map[net.Conn]struct{}),
	}
}

func (tl *trackingListener) Accept() (net.Conn, error) {
	conn, err := tl.Listener.Accept()
	if err != nil {
		return nil, err
	}
	var tc *trackedConn
	tc = &trackedConn{
		Conn: conn,
		onClose: func() {
			tl.mu.Lock()
			delete(tl.conns, tc)
			tl.mu.Unlock()
		},
	}
	tl.mu.Lock()
	tl.conns[tc] = struct{}{}
	tl.mu.Unlock()
	return tc, nil
}

func (tl *trackingListener) CloseAllActiveConns() {
	tl.mu.Lock()
	snapshot := make([]net.Conn, 0, len(tl.conns))
	for c := range tl.conns {
		snapshot = append(snapshot, c)
	}
	tl.mu.Unlock()
	for _, c := range snapshot {
		_ = c.Close()
	}
}

type localBridge struct {
	host         string
	reservedHost string
	port         int
	url          string
	ln           *trackingListener
	srv          *http.Server
	serveDone    chan struct{}
	closeOnce    sync.Once
}

// reserveDistinctLoopbackIP derives a collision-free per-instance loopback IPv4 address inside
// 127.0.0.0/8 (strictly excluding 127.0.0.1, .0, .255, and every currently active Studio bridge host)
// so concurrent local Studios NEVER share a browser origin or cookie jar!
func reserveDistinctLoopbackIP(instanceID string) string {
	activeBridgeHostsMu.Lock()
	defer activeBridgeHostsMu.Unlock()
	base := strings.TrimSpace(instanceID)
	for attempt := 0; attempt < 4096; attempt++ {
		seed := fmt.Sprintf("colabhive-studio-loopback:%s#%d", base, attempt)
		sum := sha256.Sum256([]byte(seed))
		a := int(sum[0])%254 + 1
		b := int(sum[1])%254 + 1
		c := int(sum[2])%253 + 2
		candidate := fmt.Sprintf("127.%d.%d.%d", a, b, c)
		if _, used := activeBridgeHosts[candidate]; !used {
			activeBridgeHosts[candidate] = struct{}{}
			return candidate
		}
	}
	return "127.254.254.254"
}

func releaseReservedBridgeHost(host string) {
	if host == "" {
		return
	}
	activeBridgeHostsMu.Lock()
	delete(activeBridgeHosts, host)
	activeBridgeHostsMu.Unlock()
}

func instanceLoopbackSubdomain(instanceID string) string {
	var b strings.Builder
	for i := 0; i < len(instanceID) && b.Len() < 24; i++ {
		c := instanceID[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		}
	}
	slug := b.String()
	if slug == "" {
		slug = "studio"
	}
	return slug + ".localhost"
}

func startLocalPortBridge(instanceID string, preferredPort int, handler http.Handler) (*localBridge, error) {
	if preferredPort < 0 || preferredPort > 65535 {
		preferredPort = 0
	}

	distinctIP := reserveDistinctLoopbackIP(instanceID)
	reservedHost := distinctIP
	var rawLn net.Listener
	var err error
	boundHost := distinctIP

	// 1. Prefer dedicated collision-free per-instance loopback IP (127.a.b.c — works natively on Windows & Linux
	//    with zero DNS dependency and gives every Studio a truly separate browser origin & cookie jar!).
	if preferredPort > 0 {
		rawLn, err = net.Listen("tcp", fmt.Sprintf("%s:%d", distinctIP, preferredPort))
	}
	if rawLn == nil || err != nil {
		rawLn, err = net.Listen("tcp", distinctIP+":0")
	}

	// 2. Fallback for platforms where binding non-127.0.0.1 loopback IPs requires lo0 aliases (e.g. macOS):
	//    bind 127.0.0.1:<port> and require RFC 6761 per-instance `<slug>.localhost:<port>` Host/Origin!
	if rawLn == nil || err != nil {
		releaseReservedBridgeHost(reservedHost)
		reservedHost = ""
		boundHost = instanceLoopbackSubdomain(instanceID)
		if preferredPort > 0 {
			rawLn, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
		}
		if rawLn == nil || err != nil {
			rawLn, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, fmt.Errorf("bind localhost port bridge: %w", err)
			}
		}
	}

	actualPort := preferredPort
	if tcpAddr, ok := rawLn.Addr().(*net.TCPAddr); ok && tcpAddr.Port > 0 {
		actualPort = tcpAddr.Port
	}

	trackedLn := newTrackingListener(rawLn)
	guardedHandler := withLoopbackBridgeGuard(boundHost, actualPort, handler)

	srv := &http.Server{
		Handler:           guardedHandler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	b := &localBridge{
		host:         boundHost,
		reservedHost: reservedHost,
		port:         actualPort,
		url:          fmt.Sprintf("http://%s:%d", boundHost, actualPort),
		ln:           trackedLn,
		srv:          srv,
		serveDone:    make(chan struct{}),
	}

	go func() {
		defer close(b.serveDone)
		_ = srv.Serve(trackedLn)
	}()

	return b, nil
}

// withLoopbackBridgeGuard enforces strict per-instance Host, Origin, and Sec-Fetch-Site validation on the bound
// isolated loopback authority (`127.a.b.c:<port>` or `<slug>.localhost:<port>`) to block DNS rebinding,
// cross-site / cross-origin browser attacks, and sibling Studio cookie sharing with HTTP 403 Forbidden.
func withLoopbackBridgeGuard(boundHost string, boundPort int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isExactLoopbackAuthority(r.Host, boundHost, boundPort) {
			writeBridgeJSONError(w, http.StatusForbidden, "forbidden: Host header must match isolated per-instance loopback authority")
			return
		}
		if fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); fetchSite == "cross-site" {
			writeBridgeJSONError(w, http.StatusForbidden, "forbidden: cross-site browser request blocked on private localhost bridge")
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
			if !isExactLoopbackOrigin(origin, boundHost, boundPort) {
				writeBridgeJSONError(w, http.StatusForbidden, "forbidden: cross-origin request blocked on private localhost bridge")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isExactLoopbackAuthority(rawHost string, boundHost string, boundPort int) bool {
	trimmed := strings.TrimSpace(rawHost)
	if trimmed == "" || strings.TrimSpace(boundHost) == "" || boundPort <= 0 {
		return false
	}
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		return false
	}
	if portStr != strconv.Itoa(boundPort) {
		return false
	}
	return strings.EqualFold(host, strings.TrimSpace(boundHost))
}

func isExactLoopbackOrigin(rawOrigin string, boundHost string, boundPort int) bool {
	trimmed := strings.TrimSpace(rawOrigin)
	if trimmed == "" || strings.EqualFold(trimmed, "null") {
		return false
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	if u.Scheme != "http" {
		return false
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return isExactLoopbackAuthority(u.Host, boundHost, boundPort)
}

func writeBridgeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":   msg,
		"message": msg,
		"status":  status,
	})
}

func (b *localBridge) Close() error {
	if b == nil {
		return nil
	}
	var closeErr error
	b.closeOnce.Do(func() {
		defer releaseReservedBridgeHost(b.reservedHost)
		ctx, cancel := context.WithTimeout(context.Background(), bridgeShutdownGraceDur)
		defer cancel()
		if b.srv != nil {
			if err := b.srv.Shutdown(ctx); err != nil {
				// Graceful drain timed out: force-close all remaining HTTP connections immediately!
				_ = b.srv.Close()
			}
		}
		if b.ln != nil {
			// Force-close every remaining tracked TCP connection (including hijacked WebSockets)
			// so ZERO in-flight Studio writes can succeed after Close() returns!
			b.ln.CloseAllActiveConns()
			if err := b.ln.Close(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "closed") {
				closeErr = err
			}
		}
		if b.serveDone != nil {
			select {
			case <-b.serveDone:
			case <-ctx.Done():
			}
		}
	})
	return closeErr
}

// SanitizeProxyRawQuery strips control-plane auth query parameters (token, key, api_key, sig, exp)
// while preserving the raw encoding and order of all legitimate app query parameters.
func SanitizeProxyRawQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		rawKey := part
		if eq := strings.IndexByte(part, '='); eq >= 0 {
			rawKey = part[:eq]
		}
		decodedKey, err := url.QueryUnescape(rawKey)
		if err != nil {
			decodedKey = rawKey
		}
		lk := strings.ToLower(strings.TrimSpace(decodedKey))
		if _, blocked := strippedControlPlaneQueryParams[lk]; blocked {
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, "&")
}

// SanitizeLocalBridgeRawQuery strips ColabHive control-plane auth query parameters while preserving
// app-native query parameters such as JupyterLab's own per-instance `?token=<random>` secret!
func SanitizeLocalBridgeRawQuery(rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		rawKey := part
		rawVal := ""
		if eq := strings.IndexByte(part, '='); eq >= 0 {
			rawKey = part[:eq]
			rawVal = part[eq+1:]
		}
		decodedKey, err := url.QueryUnescape(rawKey)
		if err != nil {
			decodedKey = rawKey
		}
		lk := strings.ToLower(strings.TrimSpace(decodedKey))
		switch lk {
		case "key", "api_key", "sig", "exp":
			continue
		case "token":
			decodedVal, vErr := url.QueryUnescape(rawVal)
			if vErr != nil {
				decodedVal = rawVal
			}
			lowVal := strings.ToLower(strings.TrimSpace(decodedVal))
			// Strip ONLY ColabHive control-plane tokens; preserve JupyterLab's own random token.
			if strings.HasPrefix(lowVal, "ch_adm_") || strings.HasPrefix(lowVal, "ch_sk_") {
				continue
			}
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, "&")
}

// bridgeCookiePrefix returns a deterministic, cookie-name-safe per-instance namespace prefix so
// sibling Studio bridges sharing `127.0.0.1` NEVER read or collide with each other's cookies.
func bridgeCookiePrefix(instanceID string) string {
	var b strings.Builder
	b.WriteString("__ch_")
	for i := 0; i < len(instanceID) && b.Len() < 28; i++ {
		c := instanceID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		}
	}
	if b.Len() == len("__ch_") {
		b.WriteString("app")
	}
	b.WriteByte('_')
	return b.String()
}

// filterAndUnscopeInboundCookieHeader keeps ONLY cookies belonging to `prefix` and strips the
// namespace prefix before forwarding to the upstream app, preventing sibling Studio cookie leaks.
func filterAndUnscopeInboundCookieHeader(rawCookie string, prefix string) string {
	if strings.TrimSpace(rawCookie) == "" || prefix == "" {
		return ""
	}
	parts := strings.Split(rawCookie, ";")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(trimmed[:eq])
		val := trimmed[eq+1:]
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		origName := strings.TrimPrefix(name, prefix)
		if origName == "" {
			continue
		}
		kept = append(kept, origName+"="+val)
	}
	return strings.Join(kept, "; ")
}

// scopeOutboundSetCookie namespaces an upstream `Set-Cookie` header under `prefix` and removes
// any `Domain=` attribute so the cookie stays bound to this Studio's isolated namespace.
func scopeOutboundSetCookie(rawSetCookie string, prefix string) string {
	trimmed := strings.TrimSpace(rawSetCookie)
	if trimmed == "" || prefix == "" {
		return rawSetCookie
	}
	parts := strings.Split(trimmed, ";")
	if len(parts) == 0 {
		return rawSetCookie
	}
	first := strings.TrimSpace(parts[0])
	eq := strings.IndexByte(first, '=')
	if eq <= 0 {
		return rawSetCookie
	}
	name := strings.TrimSpace(first[:eq])
	val := first[eq+1:]
	if name == "" {
		return rawSetCookie
	}
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	out := make([]string, 0, len(parts))
	out = append(out, name+"="+val)
	for _, attr := range parts[1:] {
		a := strings.TrimSpace(attr)
		if a == "" {
			continue
		}
		attrName := a
		if aEq := strings.IndexByte(a, '='); aEq >= 0 {
			attrName = strings.TrimSpace(a[:aEq])
		}
		if strings.EqualFold(attrName, "domain") {
			continue
		}
		out = append(out, a)
	}
	return strings.Join(out, "; ")
}

func hasControlPlaneAuthCredentials(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" ||
		strings.TrimSpace(r.Header.Get("X-ColabHive-Key")) != "" ||
		strings.TrimSpace(r.Header.Get("x-api-key")) != "" {
		return true
	}
	if r.URL != nil && r.URL.RawQuery != "" {
		for _, part := range strings.Split(r.URL.RawQuery, "&") {
			if part == "" {
				continue
			}
			rawKey := part
			if eq := strings.IndexByte(part, '='); eq >= 0 {
				rawKey = part[:eq]
			}
			decodedKey, err := url.QueryUnescape(rawKey)
			if err != nil {
				decodedKey = rawKey
			}
			lk := strings.ToLower(strings.TrimSpace(decodedKey))
			if _, blocked := strippedControlPlaneQueryParams[lk]; blocked {
				return true
			}
		}
	}
	return false
}

func headerContainsToken(vals []string, target string) bool {
	want := strings.ToLower(strings.TrimSpace(target))
	for _, v := range vals {
		for _, tok := range strings.Split(v, ",") {
			if strings.ToLower(strings.TrimSpace(tok)) == want {
				return true
			}
		}
	}
	return false
}

func isWebSocketUpgradeRequest(h http.Header) bool {
	if len(h) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(h.Get("Upgrade")), "websocket") &&
		headerContainsToken(h.Values("Connection"), "upgrade")
}

func isColabHiveControlPlaneAuthHeader(val string) bool {
	trimmed := strings.TrimSpace(val)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "bearer ") {
		tok := strings.TrimSpace(trimmed[len("bearer "):])
		lowTok := strings.ToLower(tok)
		if strings.HasPrefix(lowTok, "ch_adm_") || strings.HasPrefix(lowTok, "ch_sk_") {
			return true
		}
	}
	return false
}

func collectConnectionHopHeaders(h http.Header) map[string]struct{} {
	extra := make(map[string]struct{})
	for _, v := range h.Values("Connection") {
		for _, token := range strings.Split(v, ",") {
			if t := strings.ToLower(strings.TrimSpace(token)); t != "" {
				extra[t] = struct{}{}
			}
		}
	}
	return extra
}

func isHopByHopHeader(lowerKey string, connTokens map[string]struct{}) bool {
	if _, ok := hopByHopHeaderNames[lowerKey]; ok {
		return true
	}
	if _, ok := connTokens[lowerKey]; ok {
		return true
	}
	return false
}

// filterOutboundProxyHeaders strips ColabHive control-plane auth headers (`X-ColabHive-Key`, `x-api-key`,
// ColabHive bearer tokens `ch_adm_`/`ch_sk_`) and proxy/hop-by-hop framing headers while scoping app
// `Cookie` headers strictly to `cookiePrefix` (preventing sibling Studio cookie leakage on 127.0.0.1)
// and—when `allowWebSocketUpgrade` is true—preserving `Upgrade: websocket`, `Connection: Upgrade`,
// and `Sec-WebSocket-*` headers.
func filterOutboundProxyHeaders(in http.Header, allowWebSocketUpgrade bool, cookiePrefix string) [][2]string {
	if len(in) == 0 {
		return nil
	}
	wsUpgrade := allowWebSocketUpgrade && isWebSocketUpgradeRequest(in)
	connTokens := collectConnectionHopHeaders(in)
	out := make([][2]string, 0, len(in))
	var scopedCookies []string
	for k, vals := range in {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "" {
			continue
		}
		if wsUpgrade {
			if lk == "upgrade" {
				for _, v := range vals {
					out = append(out, [2]string{"Upgrade", v})
				}
				continue
			}
			if lk == "connection" {
				// Emit clean `Connection: Upgrade` without forwarding arbitrary hop-by-hop tokens.
				continue
			}
			if strings.HasPrefix(lk, "sec-websocket-") {
				for _, v := range vals {
					out = append(out, [2]string{k, v})
				}
				continue
			}
		}
		if isHopByHopHeader(lk, connTokens) {
			continue
		}
		if _, blocked := strippedOutboundHeaderNames[lk]; blocked {
			continue
		}
		if lk == "authorization" {
			for _, v := range vals {
				if !isColabHiveControlPlaneAuthHeader(v) {
					out = append(out, [2]string{k, v})
				}
			}
			continue
		}
		if lk == "cookie" {
			// Isolated per-instance host (`127.a.b.c:<port>` / `<slug>.localhost:<port>`) already provides
			// true browser-enforced origin & cookie-jar isolation; forward native cookies unmodified when
			// `cookiePrefix == ""` so JS-readable cookies like Jupyter `_xsrf` work natively!
			if cookiePrefix == "" {
				for _, v := range vals {
					if strings.TrimSpace(v) != "" {
						scopedCookies = append(scopedCookies, v)
					}
				}
			} else {
				for _, v := range vals {
					if unscoped := filterAndUnscopeInboundCookieHeader(v, cookiePrefix); unscoped != "" {
						scopedCookies = append(scopedCookies, unscoped)
					}
				}
			}
			continue
		}
		for _, v := range vals {
			out = append(out, [2]string{k, v})
		}
	}
	if len(scopedCookies) > 0 {
		out = append(out, [2]string{"Cookie", strings.Join(scopedCookies, "; ")})
	}
	if wsUpgrade {
		out = append(out, [2]string{"Connection", "Upgrade"})
	}
	return out
}

func extractEscapedSubPath(r *http.Request, id string, fallbackSubPath string) string {
	if r != nil && r.URL != nil {
		esc := r.URL.EscapedPath()
		prefix := "/apps/proxy/" + url.PathEscape(id)
		if strings.HasPrefix(esc, prefix+"/") {
			return "/" + strings.TrimPrefix(esc, prefix+"/")
		}
		if esc == prefix {
			return "/"
		}
	}
	if fallbackSubPath == "" {
		return "/"
	}
	return "/" + strings.TrimLeft(fallbackSubPath, "/")
}

// bridgeHeaderList preserves repeated headers (such as multiple Set-Cookie headers) while
// remaining wire-compatible with both [[k, v], ...], map[string][]string, and map[string]string.
type bridgeHeaderList [][2]string

func (b *bridgeHeaderList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*b = nil
		return nil
	}
	if trimmed[0] == '[' {
		var pairs [][]string
		if err := json.Unmarshal(trimmed, &pairs); err != nil {
			return err
		}
		out := make(bridgeHeaderList, 0, len(pairs))
		for _, p := range pairs {
			if len(p) >= 2 {
				out = append(out, [2]string{p[0], p[1]})
			}
		}
		*b = out
		return nil
	}
	var multi map[string][]string
	if err := json.Unmarshal(trimmed, &multi); err == nil {
		out := make(bridgeHeaderList, 0, len(multi))
		for k, vals := range multi {
			for _, v := range vals {
				out = append(out, [2]string{k, v})
			}
		}
		*b = out
		return nil
	}
	var single map[string]string
	if err := json.Unmarshal(trimmed, &single); err != nil {
		return err
	}
	out := make(bridgeHeaderList, 0, len(single))
	for k, v := range single {
		out = append(out, [2]string{k, v})
	}
	*b = out
	return nil
}

type pythonBridgeEnvelope struct {
	OK      bool             `json:"ok"`
	Status  int              `json:"status"`
	Headers bridgeHeaderList `json:"headers"`
	BodyB64 string           `json:"body_b64"`
	Error   string           `json:"error,omitempty"`
}

const pythonLoopbackHTTPBridgeScript = `import base64, json, os, urllib.error, urllib.request

class _NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

port = int(os.environ.get("CH_BRIDGE_PORT", "8188"))
method = os.environ.get("CH_BRIDGE_METHOD", "GET").upper()
target_path = os.environ.get("CH_BRIDGE_PATH", "/")
if not target_path.startswith("/"):
    target_path = "/" + target_path
headers_raw = os.environ.get("CH_BRIDGE_HEADERS_JSON", "[]")
body_b64 = os.environ.get("CH_BRIDGE_BODY_B64", "")

hdr_dict = {}
try:
    parsed_hdrs = json.loads(headers_raw)
    if isinstance(parsed_hdrs, list):
        for pair in parsed_hdrs:
            if isinstance(pair, list) and len(pair) >= 2:
                k, v = str(pair[0]), str(pair[1])
                if k in hdr_dict:
                    sep = "; " if k.lower() == "cookie" else ", "
                    hdr_dict[k] = hdr_dict[k] + sep + v
                else:
                    hdr_dict[k] = v
    elif isinstance(parsed_hdrs, dict):
        for k, v in parsed_hdrs.items():
            hdr_dict[str(k)] = str(v)
except Exception:
    hdr_dict = {}

body_bytes = base64.b64decode(body_b64) if body_b64 else None
url = f"http://127.0.0.1:{port}{target_path}"
req = urllib.request.Request(url, data=body_bytes, headers=hdr_dict, method=method)
opener = urllib.request.build_opener(_NoRedirectHandler())

# Bound raw body to 280 KiB so base64 + JSON envelope stays strictly under the 384 KiB stdout frame cap.
MAX_RESP_BYTES = 280 * 1024
try:
    with opener.open(req, timeout=25) as resp:
        raw = resp.read(MAX_RESP_BYTES + 1)
        if len(raw) > MAX_RESP_BYTES:
            payload = {
                "ok": False,
                "status": 502,
                "error": "upstream response exceeds 384 KiB bounded kernel bridge frame limit",
            }
        else:
            out_hdrs = [[k, v] for k, v in resp.getheaders()]
            payload = {
                "ok": True,
                "status": int(resp.status),
                "headers": out_hdrs,
                "body_b64": base64.b64encode(raw).decode("ascii"),
            }
except urllib.error.HTTPError as he:
    raw = he.read(MAX_RESP_BYTES + 1) if he.fp else b""
    if len(raw) > MAX_RESP_BYTES:
        payload = {
            "ok": False,
            "status": 502,
            "error": "upstream error response exceeds 384 KiB bounded kernel bridge frame limit",
        }
    else:
        out_hdrs = [[k, v] for k, v in he.headers.items()] if he.headers else []
        payload = {
            "ok": True,
            "status": int(he.code),
            "headers": out_hdrs,
            "body_b64": base64.b64encode(raw).decode("ascii"),
        }
except Exception as exc:
    payload = {
        "ok": False,
        "status": 502,
        "error": str(exc),
    }

print("__COLABHIVE_BRIDGE_JSON__:" + json.dumps(payload))
`

var directProxyTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          64,
	MaxIdleConnsPerHost:   16,
	IdleConnTimeout:       60 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	DisableCompression:    true,
}

// directProxyWSUpgradeTransport forces HTTP/1.1 (disabling HTTP/2 ALPN negotiation) so
// httputil.ReverseProxy can cleanly complete HTTP 101 Switching Protocols WebSocket upgrades.
var directProxyWSUpgradeTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     false,
	TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	MaxIdleConns:          32,
	MaxIdleConnsPerHost:   16,
	IdleConnTimeout:       60 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	DisableCompression:    true,
}

func isColabPerPortProxyDomain(lowerHost string) bool {
	return strings.HasSuffix(lowerHost, ".colab.googleusercontent.com") ||
		strings.HasSuffix(lowerHost, ".prod.colab.dev") ||
		strings.HasSuffix(lowerHost, ".colab.research.google.com")
}

// resolveDirectRuntimeProxyBase maps `sess.ProxyURL` to target `remotePort`.
// If `sess.ProxyURL` cannot be explicitly mapped to `remotePort` (for example, an unprefixed
// Jupyter proxy URL that would still point at Jupyter's port unchanged), it explicitly declines
// the direct proxy (`return nil, false`) so the caller falls back cleanly to the loopback Python bridge.
func resolveDirectRuntimeProxyBase(sess *models.RuntimeSession, remotePort int) (*url.URL, bool) {
	if sess == nil || remotePort <= 0 || remotePort > 65535 {
		return nil, false
	}
	rawURL := strings.TrimRight(strings.TrimSpace(sess.ProxyURL), "/")
	rawTok := strings.TrimSpace(sess.ProxyToken)
	if rawURL == "" || rawTok == "" {
		return nil, false
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false
	}

	hostName := u.Hostname()
	lowerHost := strings.ToLower(hostName)

	// 1. Google Colab runtime proxy hostnames expose per-port tunnels as `<port>-<endpoint>.<colab-domain>`.
	if isColabPerPortProxyDomain(lowerHost) {
		dashIdx := strings.IndexByte(hostName, '-')
		if dashIdx > 0 {
			prefixStr := hostName[:dashIdx]
			if prefixPort, pErr := strconv.Atoi(prefixStr); pErr == nil && prefixPort > 0 && prefixPort <= 65535 {
				newHost := strconv.Itoa(remotePort) + hostName[dashIdx:]
				if port := u.Port(); port != "" {
					u.Host = net.JoinHostPort(newHost, port)
				} else {
					u.Host = newHost
				}
				return u, true
			}
		}
		// Unprefixed Colab proxy URL cannot be rewritten to `remotePort`: decline direct proxy!
		return nil, false
	}

	// 2. Non-Colab / direct proxy URL: ONLY accept when its explicit port matches `remotePort`!
	if u.Port() == strconv.Itoa(remotePort) {
		return u, true
	}

	// Cannot map `sess.ProxyURL` to `remotePort` -> decline direct proxy and fall back to loopback bridge.
	return nil, false
}

// tryDirectRuntimeReverseProxy streams the request directly to a real Colab VM runtime proxy
// via httputil.ReverseProxy (supporting WebSockets, app cookies, large binary assets, Range requests,
// repeated Set-Cookie, and verbatim redirects with redirect-following disabled).
// Returns handled=true if an upstream HTTP/WS response was streamed, or handled=false if the direct
// proxy is unavailable/unmappable and the caller should fall back to the bounded kernel HTTP bridge.
func tryDirectRuntimeReverseProxy(
	w http.ResponseWriter,
	r *http.Request,
	sess *models.RuntimeSession,
	remotePort int,
	escapedSubPath string,
	sanitizedQuery string,
	cookiePrefix string,
) (handled bool) {
	targetBase, ok := resolveDirectRuntimeProxyBase(sess, remotePort)
	if !ok {
		return false
	}

	proxyToken := strings.TrimSpace(sess.ProxyToken)
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

	// Preserve small request bodies so we can cleanly fall back to the kernel bridge if direct dial fails,
	// while keeping large uploads (>16 MiB) zero-copy streamed.
	var replayBody []byte
	canReplay := false
	if r.Body != nil && r.Body != http.NoBody && r.ContentLength > 0 && r.ContentLength <= bridgeMaxReqBodyBytes {
		buf, err := io.ReadAll(io.LimitReader(r.Body, bridgeMaxReqBodyBytes+1))
		if err == nil && int64(len(buf)) <= bridgeMaxReqBodyBytes {
			replayBody = buf
			canReplay = true
			r.Body = io.NopCloser(bytes.NewReader(replayBody))
		}
	}

	transport := directProxyTransport
	if isWebSocketUpgradeRequest(r.Header) {
		transport = directProxyWSUpgradeTransport
	}

	// Explicitly construct a no-redirect RoundTripper check (http.Transport never follows redirects).
	noRedirectClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	directUnavailable := false
	rp := &httputil.ReverseProxy{
		Transport: noRedirectClient.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = targetBase.Scheme
			pr.Out.URL.Host = targetBase.Host
			pr.Out.Host = targetBase.Host

			baseEsc := strings.TrimRight(targetBase.EscapedPath(), "/")
			baseRaw := strings.TrimRight(targetBase.Path, "/")
			if baseEsc != "" {
				pr.Out.URL.Path = baseRaw + decodedSubPath
				pr.Out.URL.RawPath = baseEsc + escapedSubPath
			} else {
				pr.Out.URL.Path = decodedSubPath
				pr.Out.URL.RawPath = escapedSubPath
			}
			pr.Out.URL.RawQuery = sanitizedQuery

			// Scrub ONLY ColabHive control-plane auth headers while scoping app cookies per-instance & preserving WebSocket upgrades.
			cleanHdr := make(http.Header)
			for _, kv := range filterOutboundProxyHeaders(pr.In.Header, true, cookiePrefix) {
				cleanHdr.Add(kv[0], kv[1])
			}
			cleanHdr.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
			cleanHdr.Set("X-Colab-Client-Agent", "colab-cli")
			pr.Out.Header = cleanHdr
		},
		ModifyResponse: func(resp *http.Response) error {
			if cookiePrefix != "" && resp != nil && len(resp.Header) > 0 {
				if rawSetCookies := resp.Header.Values("Set-Cookie"); len(rawSetCookies) > 0 {
					resp.Header.Del("Set-Cookie")
					for _, sc := range rawSetCookies {
						resp.Header.Add("Set-Cookie", scopeOutboundSetCookie(sc, cookiePrefix))
					}
				}
			}
			return nil
		},
		ErrorHandler: func(_ http.ResponseWriter, _ *http.Request, _ error) {
			directUnavailable = true
		},
	}

	rp.ServeHTTP(w, r)
	if directUnavailable {
		if canReplay {
			r.Body = io.NopCloser(bytes.NewReader(replayBody))
		}
		return false
	}
	return true
}

func forwardRequestViaRuntimeBridge(
	ctx context.Context,
	rt models.RuntimeEngine,
	sessionID string,
	remotePort int,
	targetPathWithQuery string,
	r *http.Request,
	cookiePrefix string,
) (*pythonBridgeEnvelope, error) {
	if rt == nil || sessionID == "" || remotePort <= 0 {
		return nil, errors.New("runtime session unavailable for bridge forwarding")
	}

	var bodyB64 string
	if r.Body != nil && r.Body != http.NoBody {
		rawBody, err := io.ReadAll(io.LimitReader(r.Body, bridgeMaxReqBodyBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		if int64(len(rawBody)) > bridgeMaxReqBodyBytes {
			return nil, errBridgeBodyTooLarge
		}
		if len(rawBody) > 0 {
			bodyB64 = base64.StdEncoding.EncodeToString(rawBody)
		}
	}

	fwdHeaders := filterOutboundProxyHeaders(r.Header, false, cookiePrefix)
	hdrJSON, _ := json.Marshal(fwdHeaders)

	execRes, err := rt.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID: sessionID,
		Code:      pythonLoopbackHTTPBridgeScript,
		Env: map[string]string{
			"CH_BRIDGE_PORT":         strconv.Itoa(remotePort),
			"CH_BRIDGE_METHOD":       r.Method,
			"CH_BRIDGE_PATH":         targetPathWithQuery,
			"CH_BRIDGE_HEADERS_JSON": string(hdrJSON),
			"CH_BRIDGE_BODY_B64":     bodyB64,
		},
		Silent:         true,
		StoreHistory:   false,
		IdleTimeoutSec: 30,
		CallerName:     "appstore-portbridge",
	})
	if err != nil {
		return nil, fmt.Errorf("runtime bridge execution failed: %w", err)
	}
	if execRes == nil {
		return nil, errors.New("runtime bridge returned empty result")
	}
	if execRes.OutputTruncated || len(execRes.Stdout) > bridgeMaxStdoutFrameBytes {
		return nil, errBridgeFrameOversize
	}

	idx := strings.Index(execRes.Stdout, bridgeStdoutMarker)
	if idx < 0 {
		return nil, errors.New("runtime bridge did not emit structured response frame")
	}
	after := execRes.Stdout[idx+len(bridgeStdoutMarker):]
	if nl := strings.IndexByte(after, '\n'); nl >= 0 {
		after = after[:nl]
	}
	var env pythonBridgeEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(after)), &env); err != nil {
		return nil, fmt.Errorf("decode bridge response envelope: %w", err)
	}
	if !env.OK {
		msg := strings.TrimSpace(env.Error)
		if msg == "" {
			msg = "upstream app unreachable inside GPU runtime"
		}
		return nil, errors.New(msg)
	}
	return &env, nil
}

func writeBridgeEnvelopeResponse(w http.ResponseWriter, env *pythonBridgeEnvelope, cookiePrefix string) error {
	body, err := base64.StdEncoding.DecodeString(env.BodyB64)
	if err != nil {
		return fmt.Errorf("invalid base64 body from runtime bridge: %w", err)
	}

	// Collect hop-by-hop header names dynamically from any upstream Connection header(s).
	connTokens := make(map[string]struct{})
	for _, kv := range env.Headers {
		if strings.EqualFold(strings.TrimSpace(kv[0]), "Connection") {
			for _, tok := range strings.Split(kv[1], ",") {
				if t := strings.ToLower(strings.TrimSpace(tok)); t != "" {
					connTokens[t] = struct{}{}
				}
			}
		}
	}

	for _, kv := range env.Headers {
		k := strings.TrimSpace(kv[0])
		lk := strings.ToLower(k)
		if lk == "" || lk == "content-length" || isHopByHopHeader(lk, connTokens) {
			continue
		}
		val := kv[1]
		if lk == "set-cookie" && cookiePrefix != "" {
			val = scopeOutboundSetCookie(val, cookiePrefix)
		}
		// Use Add (not Set) so repeated headers like multiple Set-Cookie headers are faithfully preserved.
		w.Header().Add(k, val)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	status := env.Status
	if status < 100 || status > 599 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil
}

func serveInteractiveStudioPreview(
	w http.ResponseWriter,
	r *http.Request,
	inst *models.AppInstance,
	tpl models.AppTemplate,
	subPath string,
) {
	cleanSub := "/" + strings.TrimLeft(subPath, "/")

	// Support ComfyUI / Krita AI Diffusion / JSON health probes seamlessly in demo/preview mode.
	if cleanSub == "/system_stats" || cleanSub == "/api/health" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"instance_id":    inst.ID,
			"template_id":    inst.TemplateID,
			"selected_model": inst.SelectedModelID,
			"gpu":            inst.AssignedGPU,
			"mode":           "interactive_studio_preview",
			"system": map[string]any{
				"os":              "linux",
				"comfyui_version": "0.3.18",
				"python_version":  "3.12.7",
			},
			"devices": []map[string]any{
				{
					"name":       fmt.Sprintf("cuda:0 NVIDIA %s", inst.AssignedGPU),
					"type":       "cuda",
					"vram_total": int64(24) * 1024 * 1024 * 1024,
					"vram_free":  int64(20) * 1024 * 1024 * 1024,
				},
			},
		})
		return
	}

	icon := tpl.Icon
	if icon == "" {
		icon = "🚀"
	}
	titleZh := firstStr(inst.NameZh, tpl.NameZh, inst.Name, tpl.Name)
	titleEn := firstStr(inst.Name, tpl.Name)
	taglineZh := firstStr(tpl.TaglineZh, "零公网穿透 · 本机私有直连云端显卡工作台")
	gpuLabel := string(inst.AssignedGPU)
	if gpuLabel == "" {
		gpuLabel = string(tpl.RecommendedGPU)
	}
	acctLabel := firstStr(inst.AccountLabel, "算力池健康账号")
	modelLabel := firstStr(inst.SelectedModelName, inst.SelectedModelID, "默认环境")
	localConn := firstStr(inst.LocalURL, inst.WebProxyURL)
	tipZh := firstStr(tpl.PluginIntegrationTipZh, "无需任何公网穿透，本机浏览器与桌面创意软件直接安全连接。")

	var samplePrompt string
	var actionBtnLabel string
	switch inst.TemplateID {
	case "llm-chat":
		samplePrompt = "请对比 Qwen3.8-27B Abliterated 与 Gemma 4 31B Heretic 在中文长文、编程与自由创作上的区别。"
		actionBtnLabel = "⚡ 发送至无审查大模型 /v1 网关"
	case "wangp":
		samplePrompt = "赛博雨夜街头霓虹倒影，电影 35mm 镜头缓慢推进，MiniMax H3 原生音画同步 720p 6 步加速"
		actionBtnLabel = "🎬 在 WanGP 队列中运行云端视频生成"
	case "comfyui":
		samplePrompt = "温暖自然光下的极简东方茶室，细腻木纹与陶瓷质感，85mm 镜头景深，Z-Image Turbo 超清出图"
		actionBtnLabel = "🎨 在 ComfyUI 工作流中运行云端渲染"
	case "unsloth-studio":
		samplePrompt = "上传业务文档 JSONL -> Qwen3.5 9B QLoRA 2x 加速微调 -> 导出 Q4_K_M GGUF"
		actionBtnLabel = "🦥 在 Unsloth Studio 启动零代码微调"
	case "jupyterlab":
		samplePrompt = "import torch\nprint('CUDA Available:', torch.cuda.is_available(), '| GPU:', '" + gpuLabel + "')"
		actionBtnLabel = "📓 运行云端 GPU Python 单元"
	default:
		samplePrompt = "输入测试参数，验证社区插件 AI 应用本机直连响应..."
		actionBtnLabel = "🧩 执行云端推理测试"
	}

	pageHTML := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>%s · ColabHive Studio</title>
  <style>
    :root {
      --bg: #0C0D10;
      --card: #15171C;
      --border: rgba(255, 255, 255, 0.09);
      --text: #FAF9F5;
      --muted: #A1A1AA;
      --amber: #F59E0B;
      --emerald: #10B981;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
      background: radial-gradient(circle at top right, rgba(245, 158, 11, 0.12), transparent 45%%),
                  radial-gradient(circle at bottom left, rgba(16, 185, 129, 0.10), transparent 45%%),
                  var(--bg);
      color: var(--text);
      min-height: 100vh;
      padding: 32px 20px;
      line-height: 1.6;
    }
    .wrap { max-width: 920px; margin: 0 auto; }
    .header {
      display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 16px;
      padding: 22px 26px; background: var(--card); border: 1px solid var(--border); border-radius: 18px;
      margin-bottom: 20px;
    }
    .brand { display: flex; align-items: center; gap: 14px; }
    .icon {
      width: 52px; height: 52px; border-radius: 14px; display: grid; place-items: center;
      font-size: 26px; background: rgba(245, 158, 11, 0.14); border: 1px solid rgba(245, 158, 11, 0.32);
    }
    h1 { font-size: 20px; font-weight: 700; letter-spacing: -0.01em; }
    .sub { font-size: 13px; color: var(--muted); margin-top: 2px; }
    .badges { display: flex; gap: 8px; flex-wrap: wrap; }
    .pill {
      padding: 5px 12px; border-radius: 999px; font-size: 12px; font-weight: 600;
      border: 1px solid var(--border); background: rgba(255,255,255,0.04);
    }
    .pill-live { color: #34D399; border-color: rgba(16, 185, 129, 0.35); background: rgba(16, 185, 129, 0.12); }
    .pill-gpu { color: #FBBF24; border-color: rgba(245, 158, 11, 0.35); background: rgba(245, 158, 11, 0.12); }
    .grid { display: grid; grid-template-columns: 1.45fr 1fr; gap: 20px; }
    @media (max-width: 760px) { .grid { grid-template-columns: 1fr; } }
    .panel {
      background: var(--card); border: 1px solid var(--border); border-radius: 18px; padding: 24px;
    }
    .label { font-size: 12px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 10px; }
    textarea {
      width: 100%%; min-height: 118px; border-radius: 12px; padding: 14px;
      background: #0E1014; color: var(--text); border: 1px solid var(--border);
      font-family: inherit; font-size: 14px; resize: vertical; outline: none;
    }
    textarea:focus { border-color: var(--amber); }
    .btn {
      margin-top: 14px; width: 100%%; padding: 13px 18px; border-radius: 12px; border: none;
      background: linear-gradient(135deg, #FBBF24, #F59E0B); color: #111110;
      font-weight: 700; font-size: 14px; cursor: pointer; transition: transform 0.12s ease;
    }
    .btn:active { transform: scale(0.99); }
    .result {
      margin-top: 16px; padding: 14px 16px; border-radius: 12px;
      background: rgba(16, 185, 129, 0.09); border: 1px solid rgba(16, 185, 129, 0.28);
      font-size: 13px; color: #D1FAE5; display: none;
    }
    .kv { display: flex; justify-content: space-between; padding: 9px 0; border-bottom: 1px solid var(--border); font-size: 13px; gap: 12px; }
    .kv:last-child { border-bottom: none; }
    .k { color: var(--muted); }
    .v { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; color: var(--text); text-align: right; word-break: break-all; }
    .tip {
      margin-top: 16px; padding: 12px 14px; border-radius: 12px;
      background: rgba(245, 158, 11, 0.08); border: 1px solid rgba(245, 158, 11, 0.24);
      font-size: 12.5px; color: #FDE68A;
    }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="header">
      <div class="brand">
        <div class="icon">%s</div>
        <div>
          <h1>%s</h1>
          <div class="sub">%s · %s</div>
        </div>
      </div>
      <div class="badges">
        <span class="pill pill-live">● 本机私有直连已就绪</span>
        <span class="pill pill-gpu">⚡ %s 显卡加速</span>
      </div>
    </div>
    <div class="grid">
      <div class="panel">
        <div class="label">交互式创作与推理工作台 (Interactive Studio)</div>
        <textarea id="promptBox">%s</textarea>
        <button class="btn" id="runBtn" type="button">%s</button>
        <div class="result" id="outputBox"></div>
      </div>
      <div class="panel">
        <div class="label">零穿透连接与自动续跑状态</div>
        <div class="kv"><span class="k">应用实例 ID</span><span class="v">%s</span></div>
        <div class="kv"><span class="k">已加载模型</span><span class="v">%s</span></div>
        <div class="kv"><span class="k">驱动算力账号</span><span class="v">%s</span></div>
        <div class="kv"><span class="k">本机直连地址</span><span class="v">%s</span></div>
        <div class="kv"><span class="k">云端内部端口</span><span class="v">127.0.0.1:%d</span></div>
        <div class="kv"><span class="k">无人操作自动关机</span><span class="v">%d 分钟</span></div>
        <div class="kv"><span class="k">掉线自动换号续跑</span><span class="v">%s (已切换 %d 次)</span></div>
        <div class="tip">💡 %s</div>
      </div>
    </div>
  </div>
  <script>
    const btn = document.getElementById('runBtn');
    const out = document.getElementById('outputBox');
    const box = document.getElementById('promptBox');
    btn.addEventListener('click', () => {
      out.style.display = 'block';
      const ts = new Date().toLocaleTimeString();
      out.textContent = '✅ [' + ts + '] 已通过 ColabHive 私有直连通道提交至 %s 显卡执行：「' + box.value.slice(0, 68) + '」— 输出文件已纳入防丢快照保护！';
    });
  </script>
</body>
</html>`,
		html.EscapeString(titleZh),
		html.EscapeString(icon),
		html.EscapeString(titleZh),
		html.EscapeString(titleEn),
		html.EscapeString(taglineZh),
		html.EscapeString(gpuLabel),
		html.EscapeString(samplePrompt),
		html.EscapeString(actionBtnLabel),
		html.EscapeString(inst.ID),
		html.EscapeString(modelLabel),
		html.EscapeString(acctLabel),
		html.EscapeString(localConn),
		inst.RemotePort,
		inst.IdleAutoStopMin,
		boolLabelZh(inst.AutoResumeEnabled),
		inst.FailoverCount,
		html.EscapeString(tipZh),
		html.EscapeString(gpuLabel),
	)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, pageHTML)
}

func boolLabelZh(v bool) string {
	if v {
		return "已开启"
	}
	return "已关闭"
}

func firstStr(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
