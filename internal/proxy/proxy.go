// Package proxy provides per-account sticky HTTP/HTTPS and SOCKS5 egress transports,
// WebSocket dialers with consistent User-Agent headers, URL masking, and RTT/exit-IP probing.
package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/gorilla/websocket"
	xproxy "golang.org/x/net/proxy"
)

const (
	// DefaultUserAgent is attached consistently across control-plane HTTP and WebSocket handshakes.
	DefaultUserAgent = "Mozilla/5.0 ColabHive/1.0 colab-cli/0.8.0"

	defaultHTTPTimeout      = 30 * time.Second
	defaultHandshakeTimeout = 20 * time.Second
	defaultProbeTimeout     = 10 * time.Second
	defaultProbeTargetURL   = "https://api.ipify.org?format=json"
)

var defaultFactory = NewTransportFactory()

// TransportFactory caches per-proxy sticky connection pools and constructs HTTP clients
// and gorilla/websocket Dialers isolated per account proxy URL.
type TransportFactory struct {
	mu             sync.RWMutex
	userAgent      string
	probeURL       string
	proxyTLSConfig *tls.Config
	transports     map[string]*http.Transport
}

// NewTransportFactory returns a new sticky HTTP/SOCKS5 TransportFactory.
func NewTransportFactory() *TransportFactory {
	return &TransportFactory{
		userAgent:  DefaultUserAgent,
		probeURL:   defaultProbeTargetURL,
		transports: make(map[string]*http.Transport),
	}
}

// SetUserAgent overrides the User-Agent string injected on outbound requests.
func (f *TransportFactory) SetUserAgent(ua string) {
	f.mu.Lock()
	if strings.TrimSpace(ua) != "" {
		f.userAgent = ua
	}
	f.mu.Unlock()
}

// SetProbeURL overrides the HTTP endpoint queried by ProbeProxy (useful in offline/unit tests).
func (f *TransportFactory) SetProbeURL(targetURL string) {
	f.mu.Lock()
	if strings.TrimSpace(targetURL) != "" {
		f.probeURL = targetURL
	}
	f.mu.Unlock()
}

// SetProxyTLSConfig sets an optional custom TLS client configuration for dialing HTTPS proxies (useful in unit tests).
func (f *TransportFactory) SetProxyTLSConfig(cfg *tls.Config) {
	f.mu.Lock()
	f.proxyTLSConfig = cfg
	f.transports = make(map[string]*http.Transport)
	f.mu.Unlock()
}

// UserAgent returns the factory's configured User-Agent header value.
func (f *TransportFactory) UserAgent() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.userAgent
}

// ParseProtocol validates rawURL and returns its canonical models.ProxyProtocol.
func ParseProtocol(rawURL string) (models.ProxyProtocol, *url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || strings.EqualFold(trimmed, "direct") {
		return models.ProxyDirect, nil, nil
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return "", nil, fmt.Errorf("proxy: invalid proxy URL %q", MaskProxyURL(trimmed))
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return models.ProxyHTTP, u, nil
	case "https":
		return models.ProxyHTTPS, u, nil
	case "socks5", "socks5h":
		return models.ProxySOCKS5, u, nil
	default:
		return "", nil, fmt.Errorf("proxy: unsupported proxy scheme %q (expected http, https, or socks5)", u.Scheme)
	}
}

// MaskProxyURL strips embedded credentials and masks IP octets (`socks5://u:p@38.12.90.4:1080` -> `socks5://38.12.*.*:1080`).
func MaskProxyURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || strings.EqualFold(trimmed, "direct") {
		return "Direct"
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		if idx := strings.Index(trimmed, "@"); idx != -1 {
			if schemeIdx := strings.Index(trimmed, "://"); schemeIdx != -1 && schemeIdx+3 < idx {
				return trimmed[:schemeIdx+3] + "***@" + trimmed[idx+1:]
			}
		}
		return "***"
	}

	host := u.Hostname()
	maskedHost := maskHost(host)
	port := u.Port()
	if port != "" {
		return fmt.Sprintf("%s://%s:%s", strings.ToLower(u.Scheme), maskedHost, port)
	}
	return fmt.Sprintf("%s://%s", strings.ToLower(u.Scheme), maskedHost)
}

func maskHost(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return fmt.Sprintf("%d.%d.*.*", v4[0], v4[1])
		}
		parts := strings.Split(host, ":")
		if len(parts) >= 2 {
			return fmt.Sprintf("%s:%s:*:*", parts[0], parts[1])
		}
		return "*:*"
	}
	return host
}

type userAgentRoundTripper struct {
	base      http.RoundTripper
	userAgent string
}

func (rt *userAgentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		cloned := req.Clone(req.Context())
		cloned.Header.Set("User-Agent", rt.userAgent)
		req = cloned
	}
	return rt.base.RoundTrip(req)
}

func buildDialContextForSOCKS5(u *url.URL) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	var auth *xproxy.Auth
	if u.User != nil {
		pw, _ := u.User.Password()
		auth = &xproxy.Auth{
			User:     u.User.Username(),
			Password: pw,
		}
	}
	baseDialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, baseDialer)
	if err != nil {
		return nil, fmt.Errorf("proxy: create socks5 dialer: %w", err)
	}
	if cd, ok := dialer.(xproxy.ContextDialer); ok {
		return cd.DialContext, nil
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			c, dErr := dialer.Dial(network, addr)
			ch <- result{c, dErr}
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-ch:
			return r.conn, r.err
		}
	}, nil
}

type bufferedNetConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedNetConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func buildDialContextForHTTPSProxy(
	u *url.URL,
	baseTLS *tls.Config,
	userAgent string,
) func(ctx context.Context, network, addr string) (net.Conn, error) {
	proxyAddr := u.Host
	if u.Port() == "" {
		proxyAddr = net.JoinHostPort(u.Hostname(), "443")
	}
	var authHeader string
	if u.User != nil {
		pw, _ := u.User.Password()
		creds := u.User.Username() + ":" + pw
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		baseDialer := &net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		rawConn, err := baseDialer.DialContext(ctx, network, proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("proxy: dial https proxy %s: %w", MaskProxyURL(u.String()), err)
		}

		var tlsCfg *tls.Config
		if baseTLS != nil {
			tlsCfg = baseTLS.Clone()
		} else {
			tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if tlsCfg.ServerName == "" && net.ParseIP(u.Hostname()) == nil {
			tlsCfg.ServerName = u.Hostname()
		}

		tlsConn := tls.Client(rawConn, tlsCfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("proxy: tls handshake with https proxy: %w", err)
		}

		deadline := time.Now().Add(15 * time.Second)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		_ = tlsConn.SetDeadline(deadline)

		var reqBuf strings.Builder
		fmt.Fprintf(&reqBuf, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
		if userAgent != "" {
			fmt.Fprintf(&reqBuf, "User-Agent: %s\r\n", userAgent)
		}
		if authHeader != "" {
			fmt.Fprintf(&reqBuf, "Proxy-Authorization: %s\r\n", authHeader)
		}
		reqBuf.WriteString("\r\n")

		if _, err := io.WriteString(tlsConn, reqBuf.String()); err != nil {
			_ = tlsConn.Close()
			return nil, fmt.Errorf("proxy: write CONNECT to https proxy: %w", err)
		}

		br := bufio.NewReader(tlsConn)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil {
			_ = tlsConn.Close()
			return nil, fmt.Errorf("proxy: read CONNECT response from https proxy: %w", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = tlsConn.Close()
			return nil, fmt.Errorf("proxy: https proxy CONNECT rejected with HTTP %d", resp.StatusCode)
		}

		_ = tlsConn.SetDeadline(time.Time{})
		if br.Buffered() > 0 {
			return &bufferedNetConn{Conn: tlsConn, r: br}, nil
		}
		return tlsConn, nil
	}
}

func (f *TransportFactory) getOrCreateTransport(rawProxyURL string) (*http.Transport, error) {
	key := strings.TrimSpace(rawProxyURL)
	if strings.EqualFold(key, "direct") {
		key = ""
	}

	f.mu.RLock()
	if tr, ok := f.transports[key]; ok {
		f.mu.RUnlock()
		return tr, nil
	}
	customTLS := f.proxyTLSConfig
	f.mu.RUnlock()

	proto, parsedURL, err := ParseProtocol(key)
	if err != nil {
		return nil, err
	}

	baseDialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if customTLS != nil {
		tlsCfg = customTLS.Clone()
		if tlsCfg.MinVersion == 0 {
			tlsCfg.MinVersion = tls.VersionTLS12
		}
	}

	tr := &http.Transport{
		DialContext:           baseDialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   12 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsCfg,
	}

	switch proto {
	case models.ProxyDirect:
		// Respect HTTP_PROXY / HTTPS_PROXY / NO_PROXY from environment when no per-account proxy is pinned.
		tr.Proxy = http.ProxyFromEnvironment
	case models.ProxyHTTP, models.ProxyHTTPS:
		tr.Proxy = http.ProxyURL(parsedURL)
	case models.ProxySOCKS5:
		dialCtx, err := buildDialContextForSOCKS5(parsedURL)
		if err != nil {
			return nil, err
		}
		tr.DialContext = dialCtx
	}

	f.mu.Lock()
	if existing, ok := f.transports[key]; ok {
		f.mu.Unlock()
		return existing, nil
	}
	f.transports[key] = tr
	f.mu.Unlock()
	return tr, nil
}

// HTTPClient returns a sticky *http.Client bound to rawProxyURL with consistent User-Agent injection.
func (f *TransportFactory) HTTPClient(rawProxyURL string, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	tr, err := f.getOrCreateTransport(rawProxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &userAgentRoundTripper{
			base:      tr,
			userAgent: f.UserAgent(),
		},
	}, nil
}

// WebSocketDialer returns a gorilla/websocket Dialer routed through rawProxyURL.
func (f *TransportFactory) WebSocketDialer(rawProxyURL string, handshakeTimeout time.Duration) (*websocket.Dialer, error) {
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultHandshakeTimeout
	}
	proto, parsedURL, err := ParseProtocol(rawProxyURL)
	if err != nil {
		return nil, err
	}

	f.mu.RLock()
	customTLS := f.proxyTLSConfig
	ua := f.userAgent
	f.mu.RUnlock()

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if customTLS != nil {
		tlsCfg = customTLS.Clone()
		if tlsCfg.MinVersion == 0 {
			tlsCfg.MinVersion = tls.VersionTLS12
		}
	}

	d := &websocket.Dialer{
		HandshakeTimeout:  handshakeTimeout,
		EnableCompression: false,
		TLSClientConfig:   tlsCfg,
	}

	switch proto {
	case models.ProxyDirect:
		d.Proxy = http.ProxyFromEnvironment
	case models.ProxyHTTP:
		d.Proxy = http.ProxyURL(parsedURL)
	case models.ProxyHTTPS:
		// gorilla/websocket only registers "http" in x/net/proxy; dial HTTPS proxy via TLS CONNECT tunnel!
		d.NetDialContext = buildDialContextForHTTPSProxy(parsedURL, customTLS, ua)
	case models.ProxySOCKS5:
		dialCtx, err := buildDialContextForSOCKS5(parsedURL)
		if err != nil {
			return nil, err
		}
		d.NetDialContext = dialCtx
	}
	return d, nil
}

// ProbeProxy tests connectivity through rawURL, measuring round-trip latency and exit IP.
func ProbeProxy(ctx context.Context, rawURL string) (*models.ProxyProbeResult, error) {
	return defaultFactory.ProbeProxy(ctx, rawURL)
}

// ProbeProxy tests connectivity through rawURL against the factory's configured probe endpoint.
func (f *TransportFactory) ProbeProxy(ctx context.Context, rawURL string) (*models.ProxyProbeResult, error) {
	f.mu.RLock()
	target := f.probeURL
	f.mu.RUnlock()
	return f.ProbeProxyWithTarget(ctx, rawURL, target)
}

// ProbeProxyWithTarget probes rawURL against an explicit target HTTP URL.
func (f *TransportFactory) ProbeProxyWithTarget(ctx context.Context, rawURL string, targetURL string) (*models.ProxyProbeResult, error) {
	now := time.Now().UTC()
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultProbeTimeout)
		defer cancel()
	}

	client, err := f.HTTPClient(rawURL, defaultProbeTimeout)
	if err != nil {
		return &models.ProxyProbeResult{
			Healthy:   false,
			Error:     err.Error(),
			CheckedAt: now,
		}, err
	}

	if strings.TrimSpace(targetURL) == "" {
		targetURL = defaultProbeTargetURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("proxy: create probe request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	latencyMs := time.Since(start).Milliseconds()
	if latencyMs <= 0 {
		latencyMs = 1
	}

	if err != nil {
		return &models.ProxyProbeResult{
			Healthy:   false,
			LatencyMs: latencyMs,
			Error:     err.Error(),
			CheckedAt: time.Now().UTC(),
		}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	exitIP, region := extractProbeIPAndRegion(body)

	healthy := resp.StatusCode >= 200 && resp.StatusCode < 400
	errMsg := ""
	if !healthy {
		errMsg = fmt.Sprintf("probe returned HTTP %d", resp.StatusCode)
	}

	return &models.ProxyProbeResult{
		Healthy:    healthy,
		ExitIP:     exitIP,
		Region:     region,
		LatencyMs:  latencyMs,
		StatusCode: resp.StatusCode,
		Error:      errMsg,
		CheckedAt:  time.Now().UTC(),
	}, nil
}

func extractProbeIPAndRegion(body []byte) (string, string) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "", ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
		var ip, region string
		for _, k := range []string{"ip", "origin", "exit_ip", "query"} {
			if v, ok := obj[k].(string); ok && v != "" {
				ip = strings.TrimSpace(strings.Split(v, ",")[0])
				break
			}
		}
		for _, k := range []string{"region", "country", "country_code", "loc"} {
			if v, ok := obj[k].(string); ok && v != "" {
				region = strings.TrimSpace(v)
				break
			}
		}
		return ip, region
	}
	if parsed := net.ParseIP(trimmed); parsed != nil {
		return parsed.String(), ""
	}
	return "", ""
}

// DefaultHeadersForWebSocket returns standard HTTP headers including the factory's User-Agent
// for gorilla/websocket Dial calls.
func (f *TransportFactory) DefaultHeadersForWebSocket(extra http.Header) http.Header {
	hdr := make(http.Header)
	for k, vals := range extra {
		for _, v := range vals {
			hdr.Add(k, v)
		}
	}
	if hdr.Get("User-Agent") == "" {
		hdr.Set("User-Agent", f.UserAgent())
	}
	return hdr
}

// ValidateProxyURL checks whether rawURL is empty/direct or a valid http/https/socks5 URL.
func ValidateProxyURL(rawURL string) error {
	_, _, err := ParseProtocol(rawURL)
	if err != nil {
		return errors.New(err.Error())
	}
	return nil
}
