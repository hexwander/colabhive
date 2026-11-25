// Package runtime implements ColabHive's Google Colab VM lifecycle manager,
// 2-step XSRF /tun/m/{assign,unassign} client with HTTP 400/412/503 pool failover,
// Jupyter REST + ZMQ-over-WebSocket v5.3 execution engine, contributor Drive firewall,
// 8s 15m-reaper keepalive + /proc/{pid}/stat anti-zombie watchdog, 10s /api/colab/resources
// GPU telemetry poller, parallel sharded experiment runner, and built-in Demo simulation driver.
package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	xproxy "golang.org/x/net/proxy"

	"github.com/colabhive/colabhive/internal/models"
)

var (
	// ErrSessionNotFound indicates the requested session ID or name does not exist.
	ErrSessionNotFound = models.ErrSessionNotFound
	// ErrExperimentNotFound indicates the requested experiment ID does not exist.
	ErrExperimentNotFound = errors.New("experiment not found")
	// ErrExperimentAlreadyExists indicates an experiment with the requested ID already exists.
	ErrExperimentAlreadyExists = errors.New("experiment already exists")
)

var _ models.RuntimeEngine = (*Engine)(nil)

// Options configures the RuntimeEngine. Zero values fall back to production defaults.
type Options struct {
	DemoMode               bool
	TunnelBaseURL          string
	PublicAPIBaseURL       string
	KeepaliveInterval      time.Duration
	TunnelPingInterval     time.Duration
	ResourcePollInterval   time.Duration
	RemoteSyncInterval     time.Duration
	PostAssignWarmup       time.Duration
	AssignTimeout          time.Duration
	FallbackAssignTimeout  time.Duration
	WSPreAcceptRetryDelay  time.Duration
	WSProbeTimeout         time.Duration
	ArtifactsDir           string
	DisableBackgroundLoops bool
	HTTPClientFactory      func(proxyURL string) (*http.Client, *websocket.Dialer, error)
}

type sessionMemState struct {
	mu             sync.Mutex
	proxyToken     string
	execCount      int
	preludeDone    bool
	virtualFiles   map[string][]byte
	installedPkgs  []string
	workerCmd      string
	workerLog      strings.Builder
	lastLogFile    string
	lastLogByteLen int
}

// Engine implements models.RuntimeEngine depending solely on models interfaces.
type Engine struct {
	st   models.Store
	pool models.AccountPoolService
	rtr  models.RouterService
	hub  models.EventHub
	opts Options

	mu          sync.RWMutex
	memState    map[string]*sessionMemState
	sessMu      sync.Mutex
	sessLocks   map[string]*sync.Mutex
	inFlightMu   sync.Mutex
	inFlightEps  map[string]map[string]bool
	inFlightProv map[string]int
	drainingAccs map[string]int
	expMu       sync.Mutex
	syncMu      sync.Mutex
	localAcctMu sync.Mutex

	bgMu     sync.Mutex
	bgClosed bool
	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWg     sync.WaitGroup
}

// NewEngine constructs a RuntimeEngine and starts background keepalive/telemetry watchdogs unless disabled.
func NewEngine(
	st models.Store,
	pool models.AccountPoolService,
	rtr models.RouterService,
	hub models.EventHub,
	opts Options,
) models.RuntimeEngine {
	return NewConcreteEngine(st, pool, rtr, hub, opts)
}

// NewConcreteEngine returns the concrete *Engine (useful when callers need Close() or ArtifactPath()).
func NewConcreteEngine(
	st models.Store,
	pool models.AccountPoolService,
	rtr models.RouterService,
	hub models.EventHub,
	opts Options,
) *Engine {
	if strings.TrimSpace(opts.TunnelBaseURL) == "" {
		opts.TunnelBaseURL = models.ColabTunnelDomain
	}
	opts.TunnelBaseURL = strings.TrimRight(opts.TunnelBaseURL, "/")
	if strings.TrimSpace(opts.PublicAPIBaseURL) == "" {
		opts.PublicAPIBaseURL = models.ColabPublicAPIDomain
	}
	opts.PublicAPIBaseURL = strings.TrimRight(opts.PublicAPIBaseURL, "/")
	if opts.KeepaliveInterval <= 0 {
		opts.KeepaliveInterval = 8 * time.Second
	}
	if opts.TunnelPingInterval <= 0 {
		opts.TunnelPingInterval = 60 * time.Second
	}
	if opts.ResourcePollInterval <= 0 {
		opts.ResourcePollInterval = 10 * time.Second
	}
	if opts.RemoteSyncInterval <= 0 {
		opts.RemoteSyncInterval = 20 * time.Second
	}
	if opts.AssignTimeout <= 0 {
		opts.AssignTimeout = 90 * time.Second
	}
	if opts.FallbackAssignTimeout <= 0 {
		opts.FallbackAssignTimeout = 28 * time.Second
	}
	if opts.WSPreAcceptRetryDelay <= 0 {
		opts.WSPreAcceptRetryDelay = 1200 * time.Millisecond
	}
	if opts.WSProbeTimeout <= 0 {
		opts.WSProbeTimeout = 4 * time.Second
	}
	if opts.ArtifactsDir == "" {
		opts.ArtifactsDir = filepath.Join(os.TempDir(), "colabhive-artifacts")
	}
	_ = os.MkdirAll(opts.ArtifactsDir, 0o755)

	ctx, cancel := context.WithCancel(context.Background())
	eng := &Engine{
		st:          st,
		pool:        pool,
		rtr:         rtr,
		hub:         hub,
		opts:        opts,
		memState:     make(map[string]*sessionMemState),
		sessLocks:    make(map[string]*sync.Mutex),
		inFlightEps:  make(map[string]map[string]bool),
		inFlightProv: make(map[string]int),
		drainingAccs: make(map[string]int),
		bgCtx:       ctx,
		bgCancel:    cancel,
	}

	if drainerSetter, ok := pool.(interface {
		SetSessionDrainer(func(context.Context, string) error)
	}); ok {
		drainerSetter.SetSessionDrainer(eng.StopManagedSessionsForAccount)
	}

	if !opts.DisableBackgroundLoops {
		eng.startBackgroundLoops()
	}
	return eng
}

// Close gracefully terminates background keepalive and resource-polling goroutines.
func (e *Engine) Close() error {
	e.bgMu.Lock()
	e.bgClosed = true
	if e.bgCancel != nil {
		e.bgCancel()
	}
	e.bgMu.Unlock()
	e.bgWg.Wait()
	return nil
}

// TriggerSyncAsync schedules a non-blocking SyncRemoteAssignments pass tracked by Engine's lifecycle WaitGroup.
func (e *Engine) TriggerSyncAsync() {
	if e.opts.DemoMode {
		return
	}
	e.bgMu.Lock()
	if e.bgClosed || e.bgCtx == nil || e.bgCtx.Err() != nil {
		e.bgMu.Unlock()
		return
	}
	e.bgWg.Add(1)
	e.bgMu.Unlock()

	go func() {
		defer e.bgWg.Done()
		syncCtx, cancel := context.WithTimeout(e.bgCtx, 15*time.Second)
		defer cancel()
		_, _ = e.SyncRemoteAssignments(syncCtx)
	}()
}

// isValidASCIIExperimentID enforces 1..128 ASCII `[A-Za-z0-9._-]` bytes (excluding leading/trailing
// dots and Windows reserved DOS device stems) so SQLite `LOWER(id)` and NTFS/APFS paths match 1:1.
func isValidASCIIExperimentID(id string) bool {
	if len(id) == 0 || len(id) > 128 || id[0] == '.' || id[len(id)-1] == '.' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return !isWindowsReservedDeviceStem(id)
}

func isWindowsReservedDeviceStem(id string) bool {
	stem := id
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// isSafePathComponent rejects empty strings, ".", "..", and path/volume separators or NUL bytes.
func isSafePathComponent(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	if strings.ContainsAny(seg, `/\:`+"\x00") {
		return false
	}
	return filepath.Base(seg) == seg
}

func sanitizeFileComponent(raw string, fallback string) string {
	trimmed := strings.TrimSpace(raw)
	for _, ch := range []string{"/", "\\", ":", "\x00"} {
		trimmed = strings.ReplaceAll(trimmed, ch, "_")
	}
	base := filepath.Base(trimmed)
	if !isSafePathComponent(base) {
		if isSafePathComponent(fallback) {
			return fallback
		}
		return "session"
	}
	return base
}

// ArtifactPath resolves a local file path inside ArtifactsDir safely (preventing path traversal).
func (e *Engine) ArtifactPath(sessionID string, filename string) (string, error) {
	rawSess := strings.TrimSpace(sessionID)
	rawFile := strings.TrimSpace(filename)
	if !isSafePathComponent(rawSess) || !isSafePathComponent(rawFile) {
		return "", errors.New("invalid artifact path")
	}
	full := filepath.Join(e.opts.ArtifactsDir, rawSess, rawFile)
	return full, nil
}

func (e *Engine) getOrCreateMemState(sessionID string, initialToken string) *sessionMemState {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.memState[sessionID]
	if !ok {
		st = &sessionMemState{
			proxyToken:   initialToken,
			virtualFiles: defaultDemoFiles(),
		}
		e.memState[sessionID] = st
	} else if initialToken != "" && st.proxyToken == "" {
		st.proxyToken = initialToken
	}
	return st
}

func (e *Engine) deleteMemState(sessionID string) {
	if sessionID == "" {
		return
	}
	e.mu.Lock()
	delete(e.memState, sessionID)
	e.mu.Unlock()
}

func (e *Engine) sessionMutex(sessionID string) *sync.Mutex {
	e.sessMu.Lock()
	defer e.sessMu.Unlock()
	if e.sessLocks == nil {
		e.sessLocks = make(map[string]*sync.Mutex)
	}
	m, ok := e.sessLocks[sessionID]
	if !ok {
		m = &sync.Mutex{}
		e.sessLocks[sessionID] = m
	}
	return m
}

func (e *Engine) deleteSessionMutex(sessionID string) {
	if sessionID == "" {
		return
	}
	e.sessMu.Lock()
	delete(e.sessLocks, sessionID)
	e.sessMu.Unlock()
}

// mutateSessionState serializes mutations per sessionID, reloads the latest persisted RuntimeSession
// from SQLite before applying mutate, and refuses to resurrect terminal STOPPED/LOST sessions
// when allowTerminalOverwrite is false.
func (e *Engine) mutateSessionState(
	ctx context.Context,
	fallback *models.RuntimeSession,
	allowTerminalOverwrite bool,
	mutate func(cur *models.RuntimeSession) bool,
) (*models.RuntimeSession, error) {
	if fallback == nil || fallback.ID == "" {
		return fallback, nil
	}
	mu := e.sessionMutex(fallback.ID)
	mu.Lock()
	defer mu.Unlock()

	cur := fallback
	if e.st != nil {
		fresh, err := e.st.GetSession(ctx, fallback.ID)
		if err == nil && fresh != nil {
			cur = fresh
		} else if errors.Is(err, sql.ErrNoRows) {
			return fallback, err
		}
	}

	if !allowTerminalOverwrite && (cur.Status == models.SessionStopped || cur.Status == models.SessionLost) {
		*fallback = *cur
		return cur, nil
	}

	var saveErr error
	if mutate(cur) && e.st != nil {
		saveErr = e.st.SaveSession(ctx, cur)
	}
	*fallback = *cur
	return cur, saveErr
}

func (e *Engine) beginAccountProvision(accountID string) error {
	if accountID == "" {
		return nil
	}
	e.inFlightMu.Lock()
	defer e.inFlightMu.Unlock()
	if e.drainingAccs != nil && e.drainingAccs[accountID] > 0 {
		return errors.New("runtime: account is currently draining or revoking")
	}
	if e.inFlightProv == nil {
		e.inFlightProv = make(map[string]int)
	}
	e.inFlightProv[accountID]++
	return nil
}

func (e *Engine) endAccountProvision(accountID string) {
	if accountID == "" {
		return
	}
	e.inFlightMu.Lock()
	if e.inFlightProv != nil {
		e.inFlightProv[accountID]--
		if e.inFlightProv[accountID] <= 0 {
			delete(e.inFlightProv, accountID)
		}
	}
	e.inFlightMu.Unlock()
}

func (e *Engine) markEndpointInFlight(accountID string, endpoint string) {
	if accountID == "" || endpoint == "" {
		return
	}
	e.inFlightMu.Lock()
	if e.inFlightEps == nil {
		e.inFlightEps = make(map[string]map[string]bool)
	}
	if e.inFlightEps[accountID] == nil {
		e.inFlightEps[accountID] = make(map[string]bool)
	}
	e.inFlightEps[accountID][endpoint] = true
	e.inFlightMu.Unlock()
}

func (e *Engine) clearEndpointInFlight(accountID string, endpoint string) {
	if accountID == "" || endpoint == "" {
		return
	}
	e.inFlightMu.Lock()
	if m := e.inFlightEps[accountID]; m != nil {
		delete(m, endpoint)
		if len(m) == 0 {
			delete(e.inFlightEps, accountID)
		}
	}
	e.inFlightMu.Unlock()
}

func (e *Engine) isEndpointInFlight(accountID string, endpoint string) bool {
	if endpoint == "" {
		return false
	}
	e.inFlightMu.Lock()
	defer e.inFlightMu.Unlock()
	if accountID != "" && e.inFlightEps[accountID] != nil && e.inFlightEps[accountID][endpoint] {
		return true
	}
	for _, m := range e.inFlightEps {
		if m[endpoint] {
			return true
		}
	}
	return false
}

func (e *Engine) isDemoSession(acc *models.Account, sess *models.RuntimeSession) bool {
	if e.opts.DemoMode {
		return true
	}
	if sess != nil && (strings.HasPrefix(sess.ProxyURL, "demo://") || strings.HasPrefix(sess.Endpoint, "demo-")) {
		return true
	}
	if acc != nil {
		if strings.HasPrefix(acc.ID, "acc_demo") || strings.HasSuffix(strings.ToLower(acc.Email), ".demo") {
			return true
		}
		for _, t := range acc.Tags {
			if strings.EqualFold(strings.TrimSpace(t), "demo") {
				return true
			}
		}
	}
	return false
}

// buildClientAndDialer creates an HTTP client and WebSocket dialer strictly pinned to proxyURL.
func (e *Engine) buildClientAndDialer(proxyURL string) (*http.Client, *websocket.Dialer, error) {
	if e.opts.HTTPClientFactory != nil {
		return e.opts.HTTPClientFactory(proxyURL)
	}

	transport := &http.Transport{
		Proxy:                 nil, // Never inherit ambient HTTP_PROXY when account is direct or pinned!
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	wsDialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	trimmed := strings.TrimSpace(proxyURL)
	if trimmed != "" && !strings.EqualFold(trimmed, "direct") && !strings.HasPrefix(trimmed, "demo://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid proxy URL %q: %w", trimmed, err)
		}
		scheme := strings.ToLower(parsed.Scheme)
		switch scheme {
		case "socks5", "socks5h":
			dialer, dErr := xproxy.FromURL(parsed, &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second})
			if dErr != nil {
				return nil, nil, fmt.Errorf("failed to create SOCKS5 dialer: %w", dErr)
			}
			if ctxDialer, ok := dialer.(xproxy.ContextDialer); ok {
				transport.DialContext = ctxDialer.DialContext
				wsDialer.NetDialContext = ctxDialer.DialContext
			} else {
				dialFn := func(_ context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
				transport.DialContext = dialFn
				wsDialer.NetDialContext = dialFn
			}
		case "http":
			transport.Proxy = http.ProxyURL(parsed)
			wsDialer.Proxy = http.ProxyURL(parsed)
		case "https":
			transport.Proxy = http.ProxyURL(parsed)
			wsDialer.NetDialContext = dialHTTPSProxyConnect(parsed)
		default:
			return nil, nil, fmt.Errorf("unsupported proxy scheme %q", scheme)
		}
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
	}
	return httpClient, wsDialer, nil
}

type bufferedTLSProxyConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedTLSProxyConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func dialHTTPSProxyConnect(u *url.URL) func(ctx context.Context, network, addr string) (net.Conn, error) {
	proxyAddr := u.Host
	if u.Port() == "" {
		proxyAddr = net.JoinHostPort(u.Hostname(), "443")
	}
	var authHeader string
	if u.User != nil {
		pw, _ := u.User.Password()
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pw))
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		rawConn, err := (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, proxyAddr)
		if err != nil {
			return nil, err
		}
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if net.ParseIP(u.Hostname()) == nil {
			tlsCfg.ServerName = u.Hostname()
		}
		tlsConn := tls.Client(rawConn, tlsCfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
		deadline := time.Now().Add(15 * time.Second)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		_ = tlsConn.SetDeadline(deadline)
		var reqBuf strings.Builder
		fmt.Fprintf(&reqBuf, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
		if authHeader != "" {
			fmt.Fprintf(&reqBuf, "Proxy-Authorization: %s\r\n", authHeader)
		}
		reqBuf.WriteString("\r\n")
		if _, err := io.WriteString(tlsConn, reqBuf.String()); err != nil {
			_ = tlsConn.Close()
			return nil, err
		}
		br := bufio.NewReader(tlsConn)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil {
			_ = tlsConn.Close()
			return nil, err
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = tlsConn.Close()
			return nil, fmt.Errorf("https proxy CONNECT HTTP %d", resp.StatusCode)
		}
		_ = tlsConn.SetDeadline(time.Time{})
		if br.Buffered() > 0 {
			return &bufferedTLSProxyConn{Conn: tlsConn, r: br}, nil
		}
		return tlsConn, nil
	}
}

func stripXSSIPrefix(raw []byte) []byte {
	prefix := []byte(models.ColabXSSIPrefix)
	if len(raw) >= len(prefix) && string(raw[:len(prefix)]) == models.ColabXSSIPrefix {
		return raw[len(prefix):]
	}
	// Also tolerate CRLF variant `)]}'\r\n`
	const crlfPrefix = ")]}'\r\n"
	if len(raw) >= len(crlfPrefix) && string(raw[:len(crlfPrefix)]) == crlfPrefix {
		return raw[len(crlfPrefix):]
	}
	return raw
}

// ============================================================================
// Session Provisioning (`GET + POST /tun/m/assign`) & Pool Failover
// ============================================================================

type assignResponseWire struct {
	// Step 1 XSRF response fields
	Acc     string `json:"acc,omitempty"`
	NBH     string `json:"nbh,omitempty"`
	Token   string `json:"token,omitempty"`
	Variant any    `json:"variant,omitempty"`

	// Step 2 / Already-assigned response fields
	Accelerator      string `json:"accelerator,omitempty"`
	Endpoint         string `json:"endpoint,omitempty"`
	MachineShape     any    `json:"machineShape,omitempty"`
	RuntimeProxyInfo *struct {
		Token                 string `json:"token"`
		TokenExpiresInSeconds int    `json:"tokenExpiresInSeconds"`
		URL                   string `json:"url"`
	} `json:"runtimeProxyInfo,omitempty"`
}

type httpStatusError struct {
	StatusCode int
	Body       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("colab backend HTTP %d: %s", e.StatusCode, e.Body)
}

type kernelBootstrapError struct {
	Endpoint string
	Err      error
}

func (e *kernelBootstrapError) Error() string {
	return fmt.Sprintf("failed to bootstrap Jupyter kernel on %s: %v", e.Endpoint, e.Err)
}

func (e *kernelBootstrapError) Unwrap() error {
	return e.Err
}

// assignCapacityTimeoutError indicates that POST /tun/m/assign timed out after Step 1 XSRF GET
// already proved the account's OAuth and proxy path healthy (e.g. out-of-stock GPU tier).
// Failing over on this error cools down ONLY the specific account:tier pair instead of marking
// the healthy account as HealthProxyError.
type assignCapacityTimeoutError struct {
	Tier    models.AcceleratorType
	Timeout time.Duration
	Err     error
}

func (e *assignCapacityTimeoutError) Error() string {
	return fmt.Sprintf("colab /tun/m/assign %s probe timed out after %s (capacity unavailable): %v", e.Tier, e.Timeout.Round(time.Millisecond), e.Err)
}

func (e *assignCapacityTimeoutError) Unwrap() error {
	return e.Err
}

func containsAcceleratorRuntime(list []models.AcceleratorType, target models.AcceleratorType) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func hasRemainingFallbackTierAfter(current models.AcceleratorType, candidateTiers []models.AcceleratorType) bool {
	for idx, tier := range candidateTiers {
		if tier == current {
			return idx < len(candidateTiers)-1
		}
	}
	return false
}

type accountSlotAcquirer interface {
	AcquireAccountSlot(ctx context.Context, req models.SessionAllocRequest) (*models.Account, models.AcceleratorType, func(), error)
}

type stickyKeyScopedResolver interface {
	ResolveStickySessionForKey(ctx context.Context, stickyKey string, callerAPIKeyID string) (*models.RuntimeSession, bool)
}

type stickyKeyScopedUnbinder interface {
	UnbindStickySessionForKey(ctx context.Context, stickyKey string, apiKeyID string) error
}

type poolAccountRollupSyncer interface {
	ReconcileAccountSessionRollup(ctx context.Context, accountID string, reconciledUpstreamCount *int, now time.Time) (*models.Account, error)
}

// CreateSession provisions a Google Colab CPU/GPU/TPU runtime via the smart pool router,
// holding atomic in-flight slot reservations and failing over across healthy pooled accounts
// on proxy/network errors, OAuth invalid_grant/401/403, HTTP 429, HTTP 412, HTTP 400, HTTP 5xx,
// and Jupyter kernel bootstrap failures.
func (e *Engine) CreateSession(ctx context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error) {
	requestedAcc := req.Accelerator
	if strings.TrimSpace(string(requestedAcc)) == "" {
		requestedAcc = models.AcceleratorCPU
	} else {
		norm, err := models.NormalizeAccelerator(string(requestedAcc))
		if err != nil {
			return nil, err
		}
		requestedAcc = norm
	}
	req.Accelerator = requestedAcc

	sessName := strings.TrimSpace(req.Name)
	if sessName != "" {
		sessName = sanitizeFileComponent(sessName, "")
	}
	if sessName == "" || sessName == "session" && strings.TrimSpace(req.Name) != "session" {
		sessName = "ch-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	}
	if e.st != nil {
		if existing, _ := e.st.GetSessionByName(ctx, sessName); existing != nil &&
			existing.Status != models.SessionStopped && existing.Status != models.SessionLost {
			sessName = sessName + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:4]
		}
	}

	numAccounts := e.countEnabledAccounts(ctx)
	if numAccounts < 1 {
		numAccounts = 1
	}
	candidateChain := req.FallbackChain
	autoFallback := req.AutoFallback
	var largeChain []models.AcceleratorType
	if e.rtr != nil {
		if pol, pErr := e.rtr.GetPolicy(ctx); pErr == nil && pol != nil {
			largeChain = pol.LargeVRAMFallbackChain
			if len(req.FallbackChain) == 0 {
				autoFallback = req.AutoFallback && pol.AutoFallbackEnabled
				if req.RequireLargeVRAM {
					candidateChain = pol.LargeVRAMFallbackChain
				} else {
					candidateChain = pol.FallbackChain
				}
			}
		}
	}
	candidateTiers := models.FallbackCandidatesFrom(requestedAcc, autoFallback, candidateChain)
	if autoFallback && len(req.FallbackChain) == 0 && !req.RequireLargeVRAM &&
		len(candidateTiers) == 1 && len(largeChain) > 0 &&
		!containsAcceleratorRuntime(candidateChain, requestedAcc) &&
		containsAcceleratorRuntime(largeChain, requestedAcc) {
		candidateTiers = models.FallbackCandidatesFrom(requestedAcc, autoFallback, largeChain)
	}
	maxAttempts := numAccounts * len(candidateTiers)
	if maxAttempts < 16 {
		maxAttempts = 16
	}

	var lastErr error
	triedPairs := make(map[string]bool)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		var stopLoop bool
		var retSess *models.RuntimeSession
		var retErr error

		func() {
			acc, assignedTier, releaseSlot, selErr := e.acquireCandidateSlot(ctx, req)
			if selErr != nil {
				if lastErr != nil {
					retErr = fmt.Errorf("%w (prior attempt: %w)", selErr, lastErr)
				} else {
					retErr = selErr
				}
				stopLoop = true
				return
			}
			defer releaseSlot()

			pairKey := acc.ID + ":" + string(assignedTier)
			if triedPairs[pairKey] && !e.isDemoSession(acc, nil) {
				stopLoop = true
				return
			}
			triedPairs[pairKey] = true

			fastFallbackProbe := autoFallback && hasRemainingFallbackTierAfter(assignedTier, candidateTiers)
			if req.OnProgress != nil {
				acctTag := strings.TrimSpace(acc.Alias)
				if acctTag == "" {
					acctTag = acc.ID
				}
				probeHintZh := ""
				if fastFallbackProbe {
					probeHintZh = "，若无库存将自动换档"
				}
				req.OnProgress(
					fmt.Sprintf("Requesting %s GPU on %s (attempt %d)...", assignedTier, acctTag, attempt+1),
					fmt.Sprintf("正在向 Google Colab 申请 %s 显卡（账号 %s · 第 %d 次探测%s）...", assignedTier, acctTag, attempt+1, probeHintZh),
				)
			}
			sess, provErr := e.provisionOnAccount(ctx, acc, assignedTier, requestedAcc, sessName, req, fastFallbackProbe)
			if provErr == nil {
				retSess = sess
				stopLoop = true
				return
			}
			if req.OnProgress != nil && ctx.Err() == nil {
				reasonZh := "暂不可用"
				reasonEn := "unavailable"
				lowerErr := strings.ToLower(provErr.Error())
				switch {
				case strings.Contains(lowerErr, "capacity unavailable"), strings.Contains(lowerErr, "timed out"), strings.Contains(lowerErr, "deadline exceeded"):
					reasonZh = "探测超时/暂无库存"
					reasonEn = "capacity probe timed out"
				case strings.Contains(lowerErr, "412"), strings.Contains(lowerErr, "concurrent"), strings.Contains(lowerErr, "too many"):
					reasonZh = "并发槽位已满"
					reasonEn = "concurrency limit reached"
				case strings.Contains(lowerErr, "503"):
					reasonZh = "Google 后端 503 繁忙"
					reasonEn = "Google backend 503 busy"
				}
				req.OnProgress(
					fmt.Sprintf("%s unavailable (%s), trying next candidate...", assignedTier, reasonEn),
					fmt.Sprintf("%s %s，正在自动切换下一档显卡或账号...", assignedTier, reasonZh),
				)
			}
			if ctx.Err() != nil {
				retErr = ctx.Err()
				stopLoop = true
				return
			}
			lastErr = provErr
			e.classifyAndRecordProvisionFailure(ctx, acc, assignedTier, pairKey, provErr, &req)
		}()

		if retSess != nil {
			return retSess, nil
		}
		if retErr != nil {
			return nil, retErr
		}
		if stopLoop {
			break
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("failed to allocate Colab runtime across pool")
}

func (e *Engine) classifyAndRecordProvisionFailure(
	ctx context.Context,
	acc *models.Account,
	assignedTier models.AcceleratorType,
	pairKey string,
	provErr error,
	req *models.SessionAllocRequest,
) {
	if acc == nil || req == nil || provErr == nil {
		return
	}
	if acc.EffectiveProvider() != models.ProviderColab {
		// Legacy non-Colab account rows cannot run Colab tunnel `/tun/m/assign`:
		// gracefully skip to the next Colab candidate WITHOUT poisoning the row.
		req.ExcludedAccountIDs = appendUniqueStr(req.ExcludedAccountIDs, acc.ID)
		return
	}

	// 1. Jupyter kernel bootstrap failure after /tun/m/assign succeeded -> 2m tier cooldown + exclude acc:tier.
	var kbErr *kernelBootstrapError
	if errors.As(provErr, &kbErr) {
		e.markGPUEntitlementSafe(ctx, acc.ID, assignedTier, models.EntitlementCooldown, provErr.Error(), 2*time.Minute)
		req.ExcludedAccountTiers = appendUniqueStr(req.ExcludedAccountTiers, pairKey)
		return
	}

	// 1b. Out-of-stock / capacity probe timeout on POST /tun/m/assign after Step 1 GET succeeded ->
	// cool down ONLY acc:tier (2m) so the healthy account immediately falls back to the next GPU tier!
	var capTimeoutErr *assignCapacityTimeoutError
	if errors.As(provErr, &capTimeoutErr) {
		e.markGPUEntitlementSafe(ctx, acc.ID, assignedTier, models.EntitlementCooldown, provErr.Error(), 2*time.Minute)
		req.ExcludedAccountTiers = appendUniqueStr(req.ExcludedAccountTiers, pairKey)
		return
	}

	// 2. Explicit HTTP status errors from Colab backend.
	var stErr *httpStatusError
	if errors.As(provErr, &stErr) {
		switch {
		case stErr.StatusCode == http.StatusUnauthorized || stErr.StatusCode == http.StatusForbidden:
			e.markAccountHealthSafe(ctx, acc.ID, models.HealthAuthExpired, fmt.Sprintf("HTTP %d auth rejected", stErr.StatusCode), 30*time.Minute)
			req.ExcludedAccountIDs = appendUniqueStr(req.ExcludedAccountIDs, acc.ID)
			return

		case stErr.StatusCode == http.StatusTooManyRequests:
			e.markAccountHealthSafe(ctx, acc.ID, models.HealthCooldownRateLimit, fmt.Sprintf("HTTP 429 rate limited: %s", stErr.Body), 5*time.Minute)
			req.ExcludedAccountIDs = appendUniqueStr(req.ExcludedAccountIDs, acc.ID)
			return

		case stErr.StatusCode == http.StatusPreconditionFailed:
			e.markAccountHealthSafe(ctx, acc.ID, models.HealthMaxConcurrency, "HTTP 412 TooManyAssignments", 10*time.Minute)
			req.ExcludedAccountIDs = appendUniqueStr(req.ExcludedAccountIDs, acc.ID)
			return

		case stErr.StatusCode == http.StatusBadRequest:
			e.markGPUEntitlementSafe(ctx, acc.ID, assignedTier, models.EntitlementTierRestricted, "HTTP 400 accelerator entitlement rejected", 30*time.Minute)
			req.ExcludedAccountTiers = appendUniqueStr(req.ExcludedAccountTiers, pairKey)
			return

		default: // 500, 502, 503, 504, 404, 409, etc.
			e.markGPUEntitlementSafe(ctx, acc.ID, assignedTier, models.EntitlementCooldown, fmt.Sprintf("HTTP %d transient capacity/backend error", stErr.StatusCode), 2*time.Minute)
			req.ExcludedAccountTiers = appendUniqueStr(req.ExcludedAccountTiers, pairKey)
			return
		}
	}

	// 3. OAuth invalid_grant / missing refresh token / HTTP 429 on token endpoint.
	lower := strings.ToLower(provErr.Error())
	if strings.Contains(lower, "token refresh http 429") {
		e.markAccountHealthSafe(ctx, acc.ID, models.HealthCooldownRateLimit, provErr.Error(), 5*time.Minute)
		req.ExcludedAccountIDs = appendUniqueStr(req.ExcludedAccountIDs, acc.ID)
		return
	}
	if isAuthFailureErr(provErr) {
		fresh := e.lookupAccountBestEffort(ctx, acc.ID)
		if fresh == nil || fresh.Health != models.HealthRevokedByUser {
			e.markAccountHealthSafe(ctx, acc.ID, models.HealthAuthExpired, provErr.Error(), 30*time.Minute)
		}
		req.ExcludedAccountIDs = appendUniqueStr(req.ExcludedAccountIDs, acc.ID)
		return
	}

	// 4. Proxy / SOCKS5 / TLS / net.Error transport failures.
	if isProxyOrNetworkFailure(provErr) {
		e.markAccountHealthSafe(ctx, acc.ID, models.HealthProxyError, provErr.Error(), 5*time.Minute)
		req.ExcludedAccountIDs = appendUniqueStr(req.ExcludedAccountIDs, acc.ID)
		return
	}

	// 5. Fallback for malformed wire payloads / missing XSRF token -> 2m tier cooldown + exclude acc:tier.
	e.markGPUEntitlementSafe(ctx, acc.ID, assignedTier, models.EntitlementCooldown, provErr.Error(), 2*time.Minute)
	req.ExcludedAccountTiers = appendUniqueStr(req.ExcludedAccountTiers, pairKey)
}

func isAuthFailureErr(err error) bool {
	if err == nil {
		return false
	}
	var stErr *httpStatusError
	if errors.As(err, &stErr) && (stErr.StatusCode == http.StatusUnauthorized || stErr.StatusCode == http.StatusForbidden) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, pat := range []string{
		"invalid_grant",
		"no refresh token",
		"re-authentication required",
		"auth_expired",
		"revoked",
		"decrypt refresh token",
		"token refresh http 400",
		"token refresh http 401",
		"token refresh http 403",
		"assignments http 401",
		"assignments http 403",
	} {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

func isProxyOrNetworkFailure(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, pat := range []string{
		"invalid proxy url",
		"socks5",
		"unsupported proxy scheme",
		"proxyconnect",
		"tls:",
		"handshake",
		"connection refused",
		"connection reset",
		"no such host",
		"i/o timeout",
		"unexpected eof",
	} {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

func appendUniqueStr(list []string, item string) []string {
	trimmed := strings.TrimSpace(item)
	if trimmed == "" {
		return list
	}
	for _, existing := range list {
		if existing == trimmed {
			return list
		}
	}
	return append(list, trimmed)
}

func (e *Engine) countEnabledAccounts(ctx context.Context) int {
	var accs []*models.Account
	if e.pool != nil {
		accs, _ = e.pool.ListAccounts(ctx)
	} else if e.st != nil {
		accs, _ = e.st.ListAccounts(ctx)
	}
	count := 0
	for _, a := range accs {
		if a != nil && a.Enabled {
			count++
		}
	}
	return count
}

func (e *Engine) acquireCandidateSlot(
	ctx context.Context,
	req models.SessionAllocRequest,
) (*models.Account, models.AcceleratorType, func(), error) {
	if e.rtr != nil {
		if acq, ok := e.rtr.(accountSlotAcquirer); ok {
			acc, tier, rawRelease, err := acq.AcquireAccountSlot(ctx, req)
			if rawRelease == nil {
				return acc, tier, func() {}, err
			}
			var once sync.Once
			return acc, tier, func() { once.Do(rawRelease) }, err
		}
		acc, tier, err := e.rtr.SelectAccountAndTier(ctx, req)
		return acc, tier, func() {}, err
	}
	acc, tier, err := e.selectCandidate(ctx, req)
	return acc, tier, func() {}, err
}

func (e *Engine) selectCandidate(ctx context.Context, req models.SessionAllocRequest) (*models.Account, models.AcceleratorType, error) {
	if e.rtr != nil {
		return e.rtr.SelectAccountAndTier(ctx, req)
	}
	excluded := make(map[string]bool, len(req.ExcludedAccountIDs))
	for _, id := range req.ExcludedAccountIDs {
		excluded[strings.TrimSpace(id)] = true
	}
	excludedPairs := make(map[string]bool, len(req.ExcludedAccountTiers))
	for _, p := range req.ExcludedAccountTiers {
		excludedPairs[strings.TrimSpace(p)] = true
	}
	if e.pool != nil {
		accs, err := e.pool.ListAccounts(ctx)
		if err != nil {
			return nil, "", err
		}
		for _, a := range accs {
			if a != nil && a.Enabled && a.Health.IsSchedulable() && !excluded[a.ID] && !excludedPairs[a.ID+":"+string(req.Accelerator)] {
				return a, req.Accelerator, nil
			}
		}
	}
	if e.st != nil {
		accs, err := e.st.ListAccounts(ctx)
		if err != nil {
			return nil, "", err
		}
		for _, a := range accs {
			if a != nil && a.Enabled && a.Health.IsSchedulable() && !excluded[a.ID] && !excludedPairs[a.ID+":"+string(req.Accelerator)] {
				return a, req.Accelerator, nil
			}
		}
	}
	if e.opts.DemoMode {
		return &models.Account{
			ID:               "acc_demo_default",
			Email:            "demo-ultra@colabhive.dev",
			SubscriptionTier: models.TierAIUltra,
			Health:           models.HealthHealthy,
			Enabled:          true,
			PaidCUBalance:    500.0,
			SchedulableCU:    500.0,
			Tags:             []string{"demo"},
		}, req.Accelerator, nil
	}
	return nil, "", errors.New("no accounts available in pool")
}

func (e *Engine) provisionOnAccount(
	ctx context.Context,
	acc *models.Account,
	tier models.AcceleratorType,
	requestedTier models.AcceleratorType,
	sessName string,
	req models.SessionAllocRequest,
	fastFallbackProbe bool,
) (*models.RuntimeSession, error) {
	now := time.Now().UTC()
	spec, ok := models.GetHardwareSpec(tier)
	if !ok {
		return nil, fmt.Errorf("unknown hardware tier %q", tier)
	}
	shape := models.EffectiveMachineShape(tier, req.HighMem)
	nbh := models.NewNotebookHash()
	stepStart := now

	if e.isDemoSession(acc, nil) {
		return e.provisionDemoSession(ctx, acc, tier, requestedTier, shape, spec, sessName, nbh, req)
	}

	if err := e.beginAccountProvision(acc.ID); err != nil {
		return nil, err
	}
	defer e.endAccountProvision(acc.ID)

	accessToken, proxyURL, err := e.acquireCredentials(ctx, acc)
	if err != nil {
		return nil, err
	}
	httpClient, wsDialer, err := e.buildClientAndDialer(proxyURL)
	if err != nil {
		return nil, err
	}

	assignURL := e.buildAssignURL(nbh, tier, spec, req.HighMem)

	// Step 1: Pre-Flight XSRF GET /tun/m/assign (metadata call; bound to 20s so it never hangs 15m).
	step1Ctx, cancelStep1 := context.WithTimeout(ctx, 20*time.Second)
	step1Wire, err := e.doColabAssignStep(step1Ctx, httpClient, http.MethodGet, assignURL, accessToken, "")
	cancelStep1()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	var finalWire *assignResponseWire
	if step1Wire.Endpoint != "" && step1Wire.RuntimeProxyInfo != nil {
		// Already assigned (reconciliation hit).
		finalWire = step1Wire
	} else {
		// Guard against Colab's silent A100 fallback when an unrecognized accelerator enum is sent.
		if step1Wire.Acc != "" && spec.WireAccelerator != "" &&
			!strings.EqualFold(step1Wire.Acc, spec.WireAccelerator) &&
			!(tier == models.AcceleratorCPU && strings.EqualFold(step1Wire.Acc, "NONE")) {
			return nil, &httpStatusError{
				StatusCode: http.StatusBadRequest,
				Body:       fmt.Sprintf("backend normalized requested %s to %s", spec.WireAccelerator, step1Wire.Acc),
			}
		}
		if step1Wire.Token == "" {
			return nil, errors.New("colab /tun/m/assign GET omitted XSRF token")
		}

		// Step 2: Commit Allocation POST /tun/m/assign with X-Goog-Colab-Token.
		// When auto_fallback has lower GPU tiers ready behind `tier`, tighten the single-tier
		// out-of-stock probe timeout to FallbackAssignTimeout (default 28s) so stockout failover
		// switches 3x-6x faster without waiting for a 90s-180s upstream stall!
		postTimeout := e.opts.AssignTimeout
		if postTimeout <= 0 {
			postTimeout = 90 * time.Second
		}
		if fastFallbackProbe {
			fbTimeout := e.opts.FallbackAssignTimeout
			if fbTimeout <= 0 {
				fbTimeout = 28 * time.Second
			}
			postTimeout = fbTimeout
		}

		postCtx, cancelPost := context.WithTimeout(ctx, postTimeout)
		step2Wire, postErr := e.doColabAssignStep(postCtx, httpClient, http.MethodPost, assignURL, accessToken, step1Wire.Token)
		postTimedOut := errors.Is(postCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancelPost()

		if postErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// On 502/503/504 or probe timeout, reconcile ONCE with a fast GET using the exact same `nbh`
			// so a VM that finished committing right at the boundary is adopted instead of double-billed!
			var stErr *httpStatusError
			if !errors.As(postErr, &stErr) || stErr.StatusCode >= 500 {
				reconTimeout := 8 * time.Second
				if !fastFallbackProbe {
					reconTimeout = 12 * time.Second
				}
				reconCtx, cancelRecon := context.WithTimeout(ctx, reconTimeout)
				recon, rErr := e.doColabAssignStep(reconCtx, httpClient, http.MethodGet, assignURL, accessToken, "")
				cancelRecon()
				if rErr == nil && recon.Endpoint != "" && recon.RuntimeProxyInfo != nil {
					step2Wire = recon
					postErr = nil
				}
			}
			if postErr != nil {
				if postTimedOut {
					return nil, &assignCapacityTimeoutError{
						Tier:    tier,
						Timeout: postTimeout,
						Err:     postErr,
					}
				}
				return nil, postErr
			}
		}
		finalWire = step2Wire
	}

	if finalWire == nil || finalWire.Endpoint == "" || finalWire.RuntimeProxyInfo == nil {
		return nil, errors.New("colab /tun/m/assign returned incomplete runtimeProxyInfo")
	}

	e.markEndpointInFlight(acc.ID, finalWire.Endpoint)
	committed := false
	var createdSessID string
	defer func() {
		if committed {
			e.clearEndpointInFlight(acc.ID, finalWire.Endpoint)
			return
		}
		if createdSessID != "" {
			e.deleteMemState(createdSessID)
			e.deleteSessionMutex(createdSessID)
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		// Keep endpoint tracked in `inFlightEps` if rollback unassign fails so `StopManagedSessionsForAccount`
		// can retry unassigning it before account revocation/deletion completes!
		if uErr := e.unassignEndpoint(cleanupCtx, httpClient, accessToken, finalWire.Endpoint); uErr == nil {
			e.clearEndpointInFlight(acc.ID, finalWire.Endpoint)
		}
	}()

	warmup := e.opts.PostAssignWarmup
	if warmup <= 0 && strings.Contains(strings.ToLower(e.opts.TunnelBaseURL), "colab.research.google.com") {
		// Google Colab's *.prod.r.colab.dev reverse proxy needs ~2.5s after /tun/m/assign
		// to propagate routes across Edge POPs; waiting prevents immediate WS handshake 404s.
		warmup = 2500 * time.Millisecond
	}
	if warmup > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(warmup):
		}
	}

	expSec := finalWire.RuntimeProxyInfo.TokenExpiresInSeconds
	if expSec <= 0 {
		expSec = 3600
	}
	tokExp := time.Now().UTC().Add(time.Duration(expSec) * time.Second)
	proxyBaseURL := strings.TrimRight(finalWire.RuntimeProxyInfo.URL, "/")
	proxyTok := finalWire.RuntimeProxyInfo.Token

	// Bootstrap persistent Jupyter notebook session & kernel on the VM proxy.
	kernelID, jupSessID, kErr := e.bootstrapJupyterKernel(ctx, httpClient, proxyBaseURL, proxyTok)
	if kErr != nil {
		return nil, &kernelBootstrapError{Endpoint: finalWire.Endpoint, Err: kErr}
	}

	burnRate := spec.DefaultBurnRateCUHr
	if e.st != nil {
		if cal, okCal, _ := e.st.GetCalibratedBurnRate(ctx, tier, shape); okCal && cal > 0 {
			burnRate = cal
		}
	}

	doneAt := time.Now().UTC()
	sess := &models.RuntimeSession{
		ID:                   models.NewPrefixedID("sess_"),
		Name:                 sessName,
		AccountID:            acc.ID,
		AccountEmail:         acc.Email,
		Backend:              models.BackendTunnelAPI,
		Endpoint:             finalWire.Endpoint,
		NotebookHash:         nbh,
		ProxyURL:             proxyBaseURL,
		ProxyToken:           proxyTok,
		ProxyTokenExpiresAt:  &tokExp,
		Variant:              spec.Variant,
		RequestedAccelerator: requestedTier,
		Accelerator:          tier,
		FallbackApplied:      tier != requestedTier,
		Shape:                shape,
		Status:               models.SessionIdle,
		KernelID:             kernelID,
		JupyterSessionID:     jupSessID,
		BurnRateCUHr:         burnRate,
		ColabWebURL:          models.BuildColabWebAttachURL(finalWire.Endpoint),
		ExperimentID:         req.ExperimentID,
		ShardIndex:           req.ShardIndex,
		StickyKey:            req.StickyKey,
		APIKeyID:             req.APIKeyID,
		KeepaliveHealthy:     true,
		LastKeepaliveAt:      &doneAt,
		Timeline: []models.ExecutionStep{
			{
				ID:          "step_assign",
				Label:       fmt.Sprintf("Assign %s VM (%s)", tier, acc.Email),
				Status:      "completed",
				DurationMs:  doneAt.Sub(stepStart).Milliseconds(),
				StartedAt:   stepStart,
				CompletedAt: &doneAt,
				Detail:      finalWire.Endpoint,
			},
		},
		Cells:     []models.NotebookCell{},
		CreatedAt: now,
		UpdatedAt: doneAt,
	}

	createdSessID = sess.ID
	mem := e.getOrCreateMemState(sess.ID, proxyTok)
	// Run idempotent /content working-directory prelude silently.
	_ = e.runSilentPrelude(ctx, wsDialer, acc, sess, mem)

	// Sample initial out-of-band /api/colab/resources telemetry best-effort.
	if res, rErr := e.fetchVMResources(ctx, httpClient, proxyBaseURL, proxyTok); rErr == nil && res != nil {
		sess.Resources = res
	}

	if err := e.persistNewSessionAndBind(ctx, acc, sess, req); err != nil {
		return nil, err
	}
	committed = true
	return sess, nil
}

func (e *Engine) persistNewSessionAndBind(
	ctx context.Context,
	acc *models.Account,
	sess *models.RuntimeSession,
	req models.SessionAllocRequest,
) error {
	if e.st != nil {
		if acc != nil && acc.ID != "" {
			if freshAcc := e.lookupAccountBestEffort(ctx, acc.ID); freshAcc == nil ||
				freshAcc.Health == models.HealthRevokedByUser ||
				(freshAcc.IsContributor && (!freshAcc.Enabled || freshAcc.Guardrails.Paused)) {
				return errors.New("runtime: account was revoked, paused, or deleted during VM provisioning")
			}
		}
		if sess.Endpoint != "" {
			if existingList, lErr := e.st.ListSessions(ctx); lErr == nil {
				for _, other := range existingList {
					if other != nil && other.ID != sess.ID && other.AccountID == acc.ID &&
						other.Endpoint == sess.Endpoint && other.AdoptedExternal &&
						other.Status != models.SessionStopped && other.Status != models.SessionLost {
						_ = e.st.DeleteSession(ctx, other.ID)
						e.deleteMemState(other.ID)
						e.deleteSessionMutex(other.ID)
					}
				}
			}
		}
		if err := e.st.SaveSession(ctx, sess); err != nil {
			return err
		}
		e.syncAccountSessionRollup(ctx, acc.ID, time.Now().UTC())
	}

	if req.StickyKey != "" && e.rtr != nil {
		shouldBind := true
		stratRaw := strings.ToLower(strings.TrimSpace(string(req.Strategy)))
		if stratRaw != "" && stratRaw != string(models.StrategyStickySession) && stratRaw != "sticky" && stratRaw != "sticky_session" {
			if existingSticky, ok := e.resolveStickyForCaller(ctx, req.StickyKey, req.APIKeyID); ok && existingSticky != nil && existingSticky.ID != sess.ID {
				shouldBind = false
			}
		}
		if shouldBind {
			proxySummary := "Direct"
			if acc.Proxy != nil && acc.Proxy.MaskedURL != "" {
				proxySummary = acc.Proxy.MaskedURL
			}
			_ = e.rtr.BindStickySession(ctx, models.RouteBinding{
				StickyKey:    req.StickyKey,
				ClientName:   firstNonEmpty(req.CallerName, "agent"),
				APIKeyID:     req.APIKeyID,
				Strategy:     req.Strategy,
				SessionID:    sess.ID,
				SessionName:  sess.Name,
				KernelID:     sess.KernelID,
				AccountID:    acc.ID,
				AccountEmail: acc.Email,
				ProxySummary: proxySummary,
				Accelerator:  sess.Accelerator,
			})
		}
	}

	e.publishEvent(models.WSEventSessionUpdated, sess)
	return nil
}

func (e *Engine) buildAssignURL(nbh string, tier models.AcceleratorType, spec models.HardwareSpec, highMem bool) string {
	q := url.Values{}
	q.Set("nbh", nbh)
	q.Set("authuser", "0")
	if tier != models.AcceleratorCPU {
		q.Set("variant", string(spec.Variant))
		q.Set("accelerator", spec.WireAccelerator)
	}
	if shapeParam := models.ResolveAssignShapeParam(tier, highMem); shapeParam != "" {
		q.Set("shape", shapeParam)
	}
	return fmt.Sprintf("%s/tun/m/assign?%s", e.opts.TunnelBaseURL, q.Encode())
}

func (e *Engine) doColabAssignStep(
	ctx context.Context,
	client *http.Client,
	method string,
	targetURL string,
	accessToken string,
	xsrfToken string,
) (*assignResponseWire, error) {
	req, err := http.NewRequestWithContext(ctx, method, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Colab-Client-Agent", "colab-cli")
	if method == http.MethodPost {
		req.Header.Set("X-Goog-Colab-Token", xsrfToken)
		req.ContentLength = 0
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	clean := stripXSSIPrefix(raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(clean)),
		}
	}

	var parsed assignResponseWire
	if len(bytes.TrimSpace(clean)) > 0 {
		if err := json.Unmarshal(clean, &parsed); err != nil {
			return nil, fmt.Errorf("failed to decode /tun/m/assign JSON: %w", err)
		}
	}
	return &parsed, nil
}

type teardownTokenAcquirer interface {
	AcquireAccessTokenForTeardown(ctx context.Context, accountID string) (accessToken string, proxyURL string, err error)
}

func (e *Engine) acquireCredentials(ctx context.Context, acc *models.Account) (string, string, error) {
	return e.acquireCredentialsInternal(ctx, acc, false)
}

func (e *Engine) acquireCredentialsForTeardown(ctx context.Context, acc *models.Account) (string, string, error) {
	return e.acquireCredentialsInternal(ctx, acc, true)
}

func (e *Engine) acquireCredentialsInternal(ctx context.Context, acc *models.Account, forTeardown bool) (string, string, error) {
	if acc == nil {
		if e.pool != nil {
			return "", "", errors.New("runtime: account not found or deleted")
		}
		return "mock-oauth-token-default", "", nil
	}
	if e.pool != nil {
		var (
			tok  string
			pURL string
			err  error
		)
		if forTeardown {
			if td, ok := e.pool.(teardownTokenAcquirer); ok {
				tok, pURL, err = td.AcquireAccessTokenForTeardown(ctx, acc.ID)
			} else {
				tok, pURL, err = e.pool.AcquireAccessToken(ctx, acc.ID)
			}
		} else {
			tok, pURL, err = e.pool.AcquireAccessToken(ctx, acc.ID)
		}
		if err == nil && tok != "" {
			return tok, pURL, nil
		}
		if err != nil {
			return "", "", err
		}
	}
	proxyURL := ""
	if acc.Proxy != nil {
		proxyURL = acc.Proxy.URL
	}
	return "mock-oauth-token-" + firstNonEmpty(acc.ID, "default"), proxyURL, nil
}

// ============================================================================
// Session Lookup, Lifecycle & Virtual Notebook Cells
// ============================================================================

func isStoreRowNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// GetSession resolves a RuntimeSession by either ID (`sess_...`), Name (`eval_g4_0`), or Endpoint.
// Database/storage read errors (distinct from sql.ErrNoRows / not-found) are propagated intact so
// callers such as StopSession NEVER misclassify a DB outage as ErrSessionNotFound!
func (e *Engine) GetSession(ctx context.Context, idOrName string) (*models.RuntimeSession, error) {
	key := strings.TrimSpace(idOrName)
	if key == "" {
		return nil, ErrSessionNotFound
	}
	if e.st == nil {
		return nil, ErrSessionNotFound
	}
	sess, err := e.st.GetSession(ctx, key)
	if err == nil && sess != nil {
		sess.UptimeSec = computeUptimeSec(sess)
		return sess, nil
	}
	if err != nil && !isStoreRowNotFound(err) {
		return nil, fmt.Errorf("load session %s from store: %w", key, err)
	}

	sess, err = e.st.GetSessionByName(ctx, key)
	if err == nil && sess != nil {
		sess.UptimeSec = computeUptimeSec(sess)
		return sess, nil
	}
	if err != nil && !isStoreRowNotFound(err) {
		return nil, fmt.Errorf("lookup session %s by name from store: %w", key, err)
	}

	all, err := e.st.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sessions from store for %s: %w", key, err)
	}
	for _, item := range all {
		if item != nil && (item.Endpoint == key || strings.EqualFold(item.Name, key)) {
			item.UptimeSec = computeUptimeSec(item)
			return item, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, key)
}

// ListSessions lists runtime sessions matching filter, optionally reconciling against `/tun/m/assignments`.
func (e *Engine) ListSessions(ctx context.Context, filter models.SessionFilter) ([]*models.RuntimeSession, error) {
	if e.st == nil {
		return []*models.RuntimeSession{}, nil
	}
	if filter.SyncBackend && !e.opts.DemoMode {
		_, _ = e.SyncRemoteAssignments(ctx)
	}
	all, err := e.st.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*models.RuntimeSession, 0, len(all))
	for _, s := range all {
		if s == nil {
			continue
		}
		if filter.AccountID != "" && s.AccountID != filter.AccountID && !strings.EqualFold(s.AccountEmail, filter.AccountID) {
			continue
		}
		if filter.ExperimentID != "" && s.ExperimentID != filter.ExperimentID {
			continue
		}
		if filter.ActiveOnly && (s.Status == models.SessionStopped || s.Status == models.SessionLost) {
			continue
		}
		s.UptimeSec = computeUptimeSec(s)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// StopManagedSessionsForAccount unassigns all active non-AdoptedExternal sessions (plus any in-flight
// provisioning endpoints) belonging to accountID.
// Wired into pool.Service.SetSessionDrainer so contributor revocation and account deletion never leak live VMs.
func (e *Engine) StopManagedSessionsForAccount(ctx context.Context, accountID string) error {
	targetAcc := strings.TrimSpace(accountID)
	if targetAcc == "" || e.st == nil {
		return nil
	}

	// Block new `beginAccountProvision` calls on targetAcc and wait for any in-flight `provisionOnAccount`
	// (including its rollback `unassignEndpoint` defer!) to complete before draining persisted sessions!
	e.inFlightMu.Lock()
	if e.drainingAccs == nil {
		e.drainingAccs = make(map[string]int)
	}
	e.drainingAccs[targetAcc]++
	e.inFlightMu.Unlock()
	defer func() {
		e.inFlightMu.Lock()
		if e.drainingAccs[targetAcc] <= 1 {
			delete(e.drainingAccs, targetAcc)
		} else {
			e.drainingAccs[targetAcc]--
		}
		e.inFlightMu.Unlock()
	}()

	waitDeadline := time.Now().Add(20 * time.Second)
	for {
		e.inFlightMu.Lock()
		provCount := e.inFlightProv[targetAcc]
		e.inFlightMu.Unlock()
		if provCount == 0 {
			break
		}
		if !time.Now().Before(waitDeadline) {
			return fmt.Errorf("runtime: timed out waiting for %d in-flight VM provisioning op(s) on account %s before drain", provCount, targetAcc)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}

	var firstErr error
	drainPass := func() error {
		sessions, err := e.st.ListSessions(ctx)
		if err != nil {
			return err
		}
		for _, sess := range sessions {
			if sess == nil || sess.AccountID != targetAcc || sess.AdoptedExternal {
				continue
			}
			if sess.Status == models.SessionStopped || sess.Status == models.SessionLost {
				e.deleteMemState(sess.ID)
				continue
			}
			if sErr := e.StopSession(ctx, sess.ID); sErr != nil && firstErr == nil {
				firstErr = sErr
			}
		}
		return nil
	}

	if err := drainPass(); err != nil {
		return err
	}

	// Also unassign any still-registered in-flight endpoints directly on Google, then run a final pass!
	e.inFlightMu.Lock()
	var eps []string
	for ep := range e.inFlightEps[targetAcc] {
		eps = append(eps, ep)
	}
	e.inFlightMu.Unlock()

	if len(eps) > 0 {
		acc := e.lookupAccountBestEffort(ctx, targetAcc)
		if acc == nil {
			acc = &models.Account{ID: targetAcc}
		}
		tok, proxyURL, tErr := e.acquireCredentialsForTeardown(ctx, acc)
		if tErr != nil || tok == "" {
			if tErr != nil {
				return tErr
			}
			return fmt.Errorf("runtime: missing teardown credentials for %d pending endpoint(s) on account %s", len(eps), targetAcc)
		}
		hc, _, cErr := e.buildClientAndDialer(proxyURL)
		if cErr != nil || hc == nil {
			if cErr != nil {
				return cErr
			}
			return fmt.Errorf("runtime: failed to build HTTP client for endpoint drain on account %s", targetAcc)
		}
		for _, ep := range eps {
			if uErr := e.unassignEndpoint(ctx, hc, tok, ep); uErr != nil {
				if firstErr == nil {
					firstErr = uErr
				}
			} else {
				e.clearEndpointInFlight(targetAcc, ep)
			}
		}
		if pErr := drainPass(); pErr != nil && firstErr == nil {
			firstErr = pErr
		}
	}

	return firstErr
}

// StopSession performs the zero-leak teardown sequence:
// (a) best-effort DELETE /api/kernels/{id},
// (b) 2-step XSRF GET + POST /tun/m/unassign/{endpoint} (verifying HTTP 2xx or 404),
// (c) unbinds sticky routing, cleans in-memory state, & records saved idle-waste CUs.
func (e *Engine) StopSession(ctx context.Context, idOrName string) error {
	target := strings.TrimSpace(idOrName)
	if target == "" {
		return ErrSessionNotFound
	}
	sess, err := e.GetSession(ctx, target)
	if err != nil {
		return err
	}
	if sess.AdoptedExternal && target != sess.ID && target != sess.Name && target != sess.Endpoint {
		return fmt.Errorf("refusing to stop external Colab runtime %q (%s) without exact session ID, name, or endpoint", sess.Name, sess.ID)
	}
	if sess.Status == models.SessionStopped {
		e.deleteMemState(sess.ID)
		return nil
	}

	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)
	mem.mu.Lock()
	proxyTok := firstNonEmpty(mem.proxyToken, sess.ProxyToken)
	mem.mu.Unlock()

	if !e.isDemoSession(acc, sess) && sess.Endpoint != "" {
		accessTok, proxyURL, cErr := e.acquireCredentialsForTeardown(ctx, acc)
		if cErr != nil {
			return fmt.Errorf("failed to acquire credentials to unassign %s: %w", sess.Endpoint, cErr)
		}
		httpClient, _, dErr := e.buildClientAndDialer(proxyURL)
		if dErr != nil {
			return fmt.Errorf("failed to build HTTP client to unassign %s: %w", sess.Endpoint, dErr)
		}
		if sess.KernelID != "" && sess.ProxyURL != "" && proxyTok != "" {
			_ = e.deleteJupyterKernel(ctx, httpClient, sess.ProxyURL, proxyTok, sess.KernelID)
		}
		if uErr := e.unassignEndpoint(ctx, httpClient, accessTok, sess.Endpoint); uErr != nil {
			return fmt.Errorf("failed to unassign upstream runtime %s: %w", sess.Endpoint, uErr)
		}
	}

	now := time.Now().UTC()
	alreadyTerminal := false
	_, _ = e.mutateSessionState(ctx, sess, true, func(cur *models.RuntimeSession) bool {
		if cur.Status == models.SessionStopped {
			alreadyTerminal = true
			return false
		}
		if cur.Status == models.SessionLost {
			alreadyTerminal = true
		}
		cur.Status = models.SessionStopped
		cur.StoppedAt = &now
		cur.ProxyToken = ""
		cur.UpdatedAt = now
		cur.UptimeSec = computeUptimeSec(cur)
		return true
	})

	e.deleteMemState(sess.ID)

	if e.st != nil {
		// Credit 15m of prevented idle-reaper waste on deterministic teardown of ColabHive-managed sessions!
		if !alreadyTerminal && !sess.AdoptedExternal && sess.BurnRateCUHr > 0 {
			_ = e.st.AddSavedWaste(ctx, sess.BurnRateCUHr*0.25, false)
		}
		if sess.AccountID != "" {
			e.syncAccountSessionRollup(ctx, sess.AccountID, now)
		}
	}

	if sess.StickyKey != "" && e.rtr != nil {
		e.unbindStickyForCaller(ctx, sess.StickyKey, sess.APIKeyID)
	}

	e.publishEvent(models.WSEventSessionUpdated, sess)
	return nil
}

func (e *Engine) unassignEndpoint(ctx context.Context, client *http.Client, accessToken string, endpoint string) error {
	unassignURL := fmt.Sprintf("%s/tun/m/unassign/%s?authuser=0", e.opts.TunnelBaseURL, url.PathEscape(endpoint))
	reqGet, err := http.NewRequestWithContext(ctx, http.MethodGet, unassignURL, nil)
	if err != nil {
		return err
	}
	reqGet.Header.Set("Authorization", "Bearer "+accessToken)
	reqGet.Header.Set("Accept", "application/json")
	reqGet.Header.Set("X-Colab-Client-Agent", "colab-cli")

	respGet, err := client.Do(reqGet)
	if err != nil {
		return err
	}
	rawGet, _ := io.ReadAll(io.LimitReader(respGet.Body, 1<<20))
	_ = respGet.Body.Close()
	if respGet.StatusCode == http.StatusNotFound {
		return nil
	}
	if respGet.StatusCode < 200 || respGet.StatusCode >= 300 {
		return &httpStatusError{
			StatusCode: respGet.StatusCode,
			Body:       strings.TrimSpace(string(stripXSSIPrefix(rawGet))),
		}
	}
	var tokObj struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(stripXSSIPrefix(rawGet), &tokObj)

	reqPost, err := http.NewRequestWithContext(ctx, http.MethodPost, unassignURL, nil)
	if err != nil {
		return err
	}
	reqPost.Header.Set("Authorization", "Bearer "+accessToken)
	reqPost.Header.Set("Accept", "application/json")
	reqPost.Header.Set("X-Colab-Client-Agent", "colab-cli")
	if tokObj.Token != "" {
		reqPost.Header.Set("X-Goog-Colab-Token", tokObj.Token)
	}
	reqPost.ContentLength = 0

	respPost, err := client.Do(reqPost)
	if err != nil {
		return err
	}
	rawPost, _ := io.ReadAll(io.LimitReader(respPost.Body, 1<<20))
	_ = respPost.Body.Close()
	if respPost.StatusCode == http.StatusNotFound || (respPost.StatusCode >= 200 && respPost.StatusCode < 300) {
		return nil
	}
	return &httpStatusError{
		StatusCode: respPost.StatusCode,
		Body:       strings.TrimSpace(string(stripXSSIPrefix(rawPost))),
	}
}

// RestartKernel resets the Python interpreter RAM/VRAM while preserving `/content` and `/root/.cache/huggingface`.
func (e *Engine) RestartKernel(ctx context.Context, idOrName string) error {
	sess, err := e.GetSession(ctx, idOrName)
	if err != nil {
		return err
	}
	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)

	if e.isDemoSession(acc, sess) {
		mem.mu.Lock()
		mem.execCount = 0
		mem.mu.Unlock()
	} else {
		proxyTok, httpClient, _, pErr := e.prepareSessionProxyClient(ctx, acc, sess, mem)
		if pErr != nil {
			return pErr
		}
		if err := e.postJupyterKernelAction(ctx, httpClient, sess.ProxyURL, proxyTok, sess.KernelID, "restart"); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
		cur.Status = models.SessionIdle
		cur.UpdatedAt = now
		return true
	})
	e.publishEvent(models.WSEventSessionUpdated, sess)
	return nil
}

// InterruptKernel sends SIGINT (`KeyboardInterrupt`) to the running Jupyter cell without losing RAM variables.
func (e *Engine) InterruptKernel(ctx context.Context, idOrName string) error {
	sess, err := e.GetSession(ctx, idOrName)
	if err != nil {
		return err
	}
	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)

	if !e.isDemoSession(acc, sess) {
		proxyTok, httpClient, _, pErr := e.prepareSessionProxyClient(ctx, acc, sess, mem)
		if pErr != nil {
			return pErr
		}
		if err := e.postJupyterKernelAction(ctx, httpClient, sess.ProxyURL, proxyTok, sess.KernelID, "interrupt"); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
		cur.Status = models.SessionIdle
		cur.UpdatedAt = now
		return true
	})
	e.publishEvent(models.WSEventSessionUpdated, sess)
	return nil
}

// HotRestartWorker kills and respawns the background worker PID while keeping the Colab VM
// and `/root/.cache/huggingface` 50–70 GB model weights hot on disk!
func (e *Engine) HotRestartWorker(ctx context.Context, idOrName string) error {
	sess, err := e.GetSession(ctx, idOrName)
	if err != nil {
		return err
	}
	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)
	now := time.Now().UTC()

	oldPID := 0
	if sess.Worker != nil {
		oldPID = sess.Worker.PID
	}
	newPID := 0
	if e.isDemoSession(acc, sess) {
		newPID = 4200 + (int(now.UnixNano()%1000) + 1)
	} else {
		mem.mu.Lock()
		cmd := mem.workerCmd
		mem.mu.Unlock()
		restartSnippet := fmt.Sprintf(
			"import os, signal\nif %d > 0:\n    try: os.kill(%d, signal.SIGTERM)\n    except Exception: pass\nprint('Hot-restarted worker (HF cache preserved)')\n",
			oldPID, oldPID,
		)
		if cmd != "" {
			restartSnippet += cmd + "\n"
		}
		execRes, execErr := e.ExecuteCode(ctx, models.KernelExecRequest{
			SessionID:      sess.ID,
			Code:           restartSnippet,
			Silent:         true,
			IdleTimeoutSec: 30,
		})
		if execErr == nil && execRes != nil {
			if parsedPID, ok := parseSpawnedPID(execRes.Stdout); ok {
				newPID = parsedPID
			}
		}
	}

	workerState := &models.WorkerLiveness{
		PID:            newPID,
		ProcState:      "R",
		Alive:          newPID > 0 || e.isDemoSession(acc, sess),
		ZombieDetected: false,
		LastCheckedAt:  &now,
	}
	nextStatus := models.SessionIdle
	if newPID > 0 || e.isDemoSession(acc, sess) {
		nextStatus = models.SessionBusy
	}
	step := models.ExecutionStep{
		ID:          models.NewPrefixedID("step_"),
		Label:       fmt.Sprintf("Hot-Restart Worker PID %d -> %d (HF Cache Kept)", oldPID, newPID),
		Status:      "completed",
		DurationMs:  180,
		StartedAt:   now,
		CompletedAt: &now,
	}
	_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
		cur.Worker = workerState
		cur.Status = nextStatus
		cur.Timeline = append(cur.Timeline, step)
		cur.UpdatedAt = now
		return true
	})

	e.publishStreamChunk(sess, "colab", fmt.Sprintf("[colabhive] Hot-restarted worker PID %d -> %d (/root/.cache/huggingface kept hot)\n", oldPID, newPID), nil)
	e.publishEvent(models.WSEventSessionUpdated, sess)
	return nil
}

// GetColabWebURL returns the instant browser-attachment URL (`?dbu=...#datalabBackendUrl=...`)
// that attaches Colab Web UI to this live headless runtime without allocating a second VM.
func (e *Engine) GetColabWebURL(ctx context.Context, idOrName string) (string, error) {
	sess, err := e.GetSession(ctx, idOrName)
	if err != nil {
		return "", err
	}
	if sess.ColabWebURL != "" {
		return sess.ColabWebURL, nil
	}
	if sess.Endpoint != "" {
		return models.BuildColabWebAttachURL(sess.Endpoint), nil
	}
	return "", errors.New("session missing endpoint")
}

// ============================================================================
// Virtual Notebook Cells (`get_cells`, `add_code_cell`, `run_code_cell`, etc.)
// ============================================================================

// GetCells returns the headless virtual `.ipynb` cell array for a session.
func (e *Engine) GetCells(ctx context.Context, sessionID string, includeOutputs bool) ([]models.NotebookCell, error) {
	sess, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]models.NotebookCell, len(sess.Cells))
	for i, c := range sess.Cells {
		cp := c
		cp.Index = i
		if !includeOutputs {
			cp.Outputs = nil
		}
		out[i] = cp
	}
	return out, nil
}

// MutateCell executes `add_code`, `add_text`, `update`, `run`, `delete`, or `move` on a session's virtual notebook.
func (e *Engine) MutateCell(
	ctx context.Context,
	sessionID string,
	req models.CellMutationRequest,
) (*models.NotebookCell, *models.KernelExecResult, error) {
	sess, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}

	op := strings.ToLower(strings.TrimSpace(req.Operation))

	switch op {
	case "add_code", "add_text":
		cellType := "code"
		if op == "add_text" {
			cellType = "markdown"
		}
		var created models.NotebookCell
		_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			cells := append([]models.NotebookCell(nil), cur.Cells...)
			newCell := models.NotebookCell{
				CellID:   models.NewPrefixedID("cell_"),
				CellType: cellType,
				Source:   req.Content,
				Outputs:  []models.KernelRichOutput{},
			}
			insertIdx := len(cells)
			if req.Index != nil && *req.Index >= 0 && *req.Index <= len(cells) {
				insertIdx = *req.Index
			}
			cells = append(cells[:insertIdx], append([]models.NotebookCell{newCell}, cells[insertIdx:]...)...)
			reindexCells(cells)
			cur.Cells = cells
			cur.UpdatedAt = time.Now().UTC()
			created = cells[insertIdx]
			return true
		})

		if cellType == "code" && req.RunOnAdd {
			res, execErr := e.ExecuteCode(ctx, models.KernelExecRequest{
				SessionID:      sess.ID,
				Code:           created.Source,
				IdleTimeoutSec: req.TimeoutSec,
				StoreHistory:   true,
			})
			if execErr != nil {
				return &created, nil, execErr
			}
			return e.attachExecResultToCell(ctx, sess.ID, created.CellID, res)
		}
		return &created, nil, nil

	case "update":
		var updated models.NotebookCell
		var opErr error
		_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			cells := append([]models.NotebookCell(nil), cur.Cells...)
			idx := findCellIndex(cells, req.CellID)
			if idx < 0 {
				opErr = fmt.Errorf("cell %q not found", req.CellID)
				return false
			}
			cells[idx].Source = req.Content
			reindexCells(cells)
			cur.Cells = cells
			cur.UpdatedAt = time.Now().UTC()
			updated = cells[idx]
			return true
		})
		if opErr != nil {
			return nil, nil, opErr
		}
		return &updated, nil, nil

	case "run":
		idx := findCellIndex(sess.Cells, req.CellID)
		if idx < 0 {
			return nil, nil, fmt.Errorf("cell %q not found", req.CellID)
		}
		target := sess.Cells[idx]
		if target.CellType != "code" {
			return &target, nil, fmt.Errorf("cell %q is %s, not code", req.CellID, target.CellType)
		}
		res, execErr := e.ExecuteCode(ctx, models.KernelExecRequest{
			SessionID:      sess.ID,
			Code:           target.Source,
			IdleTimeoutSec: req.TimeoutSec,
			StoreHistory:   true,
		})
		if execErr != nil {
			return &target, nil, execErr
		}
		return e.attachExecResultToCell(ctx, sess.ID, target.CellID, res)

	case "delete":
		var removed models.NotebookCell
		var opErr error
		_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			cells := append([]models.NotebookCell(nil), cur.Cells...)
			idx := findCellIndex(cells, req.CellID)
			if idx < 0 {
				opErr = fmt.Errorf("cell %q not found", req.CellID)
				return false
			}
			removed = cells[idx]
			cells = append(cells[:idx], cells[idx+1:]...)
			reindexCells(cells)
			cur.Cells = cells
			cur.UpdatedAt = time.Now().UTC()
			return true
		})
		if opErr != nil {
			return nil, nil, opErr
		}
		return &removed, nil, nil

	case "move":
		var moved models.NotebookCell
		var opErr error
		_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			cells := append([]models.NotebookCell(nil), cur.Cells...)
			idx := findCellIndex(cells, req.CellID)
			if idx < 0 {
				opErr = fmt.Errorf("cell %q not found", req.CellID)
				return false
			}
			newIdx := 0
			if req.Index != nil {
				newIdx = *req.Index
			}
			if newIdx < 0 {
				newIdx = 0
			}
			if newIdx >= len(cells) {
				newIdx = len(cells) - 1
			}
			moving := cells[idx]
			without := append(cells[:idx], cells[idx+1:]...)
			rebuilt := make([]models.NotebookCell, 0, len(cells))
			rebuilt = append(rebuilt, without[:newIdx]...)
			rebuilt = append(rebuilt, moving)
			rebuilt = append(rebuilt, without[newIdx:]...)
			reindexCells(rebuilt)
			cur.Cells = rebuilt
			cur.UpdatedAt = time.Now().UTC()
			moved = rebuilt[newIdx]
			return true
		})
		if opErr != nil {
			return nil, nil, opErr
		}
		return &moved, nil, nil

	default:
		return nil, nil, fmt.Errorf("unsupported cell operation %q", req.Operation)
	}
}

func (e *Engine) attachExecResultToCell(
	ctx context.Context,
	sessionID string,
	cellID string,
	res *models.KernelExecResult,
) (*models.NotebookCell, *models.KernelExecResult, error) {
	sess, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return nil, res, nil
	}
	var attached *models.NotebookCell
	_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
		idx := findCellIndex(cur.Cells, cellID)
		if idx < 0 {
			return false
		}
		cur.Cells[idx].ExecutionCount = res.ExecutionCount
		outputs := make([]models.KernelRichOutput, 0, len(res.RichOutputs)+1)
		if res.Stdout != "" || res.Result != "" {
			outputs = append(outputs, models.KernelRichOutput{
				Index:     0,
				MimeTypes: []string{"text/plain"},
				TextData:  strings.TrimSpace(res.Stdout + "\n" + res.Result),
				SizeBytes: int64(len(res.Stdout) + len(res.Result)),
			})
		}
		outputs = append(outputs, res.RichOutputs...)
		cur.Cells[idx].Outputs = outputs
		cur.UpdatedAt = time.Now().UTC()
		c := cur.Cells[idx]
		attached = &c
		return true
	})
	return attached, res, nil
}

func findCellIndex(cells []models.NotebookCell, cellID string) int {
	for i, c := range cells {
		if c.CellID == cellID {
			return i
		}
	}
	return -1
}

func reindexCells(cells []models.NotebookCell) {
	for i := range cells {
		cells[i].Index = i
	}
}

// ============================================================================
// Internal Helpers
// ============================================================================

func (e *Engine) resolveStickyForCaller(ctx context.Context, stickyKey string, apiKeyID string) (*models.RuntimeSession, bool) {
	if e.rtr == nil || strings.TrimSpace(stickyKey) == "" {
		return nil, false
	}
	if scoped, ok := e.rtr.(stickyKeyScopedResolver); ok {
		return scoped.ResolveStickySessionForKey(ctx, stickyKey, apiKeyID)
	}
	return e.rtr.ResolveStickySession(ctx, stickyKey)
}

func (e *Engine) unbindStickyForCaller(ctx context.Context, stickyKey string, apiKeyID string) {
	if e.rtr == nil || strings.TrimSpace(stickyKey) == "" {
		return
	}
	if scoped, ok := e.rtr.(stickyKeyScopedUnbinder); ok {
		_ = scoped.UnbindStickySessionForKey(ctx, stickyKey, apiKeyID)
		return
	}
	if strings.TrimSpace(apiKeyID) == "" {
		_ = e.rtr.UnbindStickySession(ctx, stickyKey)
	}
}

func (e *Engine) syncAccountSessionRollup(ctx context.Context, accountID string, now time.Time) {
	e.syncAccountSessionRollupWithUpstream(ctx, accountID, nil, now)
}

func (e *Engine) syncAccountSessionRollupWithUpstream(
	ctx context.Context,
	accountID string,
	reconciledUpstreamCount *int,
	now time.Time,
) {
	if accountID == "" {
		return
	}
	if e.pool != nil {
		if syncer, ok := e.pool.(poolAccountRollupSyncer); ok {
			_, _ = syncer.ReconcileAccountSessionRollup(ctx, accountID, reconciledUpstreamCount, now)
			return
		}
	}
	if e.st == nil {
		return
	}

	e.localAcctMu.Lock()
	storedAcc, err := e.st.GetAccount(ctx, accountID)
	if err != nil || storedAcc == nil {
		e.localAcctMu.Unlock()
		return
	}

	allSessions, err := e.st.ListSessions(ctx)
	if err != nil {
		e.localAcctMu.Unlock()
		return
	}

	var active []*models.RuntimeSession
	localBurn := 0.0
	postPollManagedBurn := 0.0
	for _, s := range allSessions {
		if s == nil || s.AccountID != accountID {
			continue
		}
		if s.Status == models.SessionStopped || s.Status == models.SessionLost || s.Status == models.SessionZombieAlert {
			continue
		}
		active = append(active, s)
		localBurn += s.BurnRateCUHr
		if !s.AdoptedExternal && (storedAcc.LastPolledAt == nil || s.CreatedAt.After(*storedAcc.LastPolledAt)) {
			postPollManagedBurn += s.BurnRateCUHr
		}
	}

	sort.Slice(active, func(i, j int) bool {
		return active[i].CreatedAt.Before(active[j].CreatedAt)
	})

	summaries := make([]string, 0, len(active))
	for i, s := range active {
		if i >= 3 {
			summaries = append(summaries, fmt.Sprintf("+%d more", len(active)-3))
			break
		}
		summaries = append(summaries, fmt.Sprintf("%s (%s)", s.Name, s.Accelerator))
	}

	if reconciledUpstreamCount != nil {
		storedAcc.UpstreamUnadoptedCount = 0
		if *reconciledUpstreamCount == 0 && len(active) == 0 {
			storedAcc.UpstreamAssignmentsCount = 0
			storedAcc.UpstreamBurnRateCUHr = 0
		} else if *reconciledUpstreamCount > storedAcc.UpstreamAssignmentsCount {
			storedAcc.UpstreamAssignmentsCount = *reconciledUpstreamCount
		}
	}

	effectiveActive := len(active) + storedAcc.UpstreamUnadoptedCount
	var effectiveBurn float64
	switch {
	case effectiveActive == 0:
		effectiveBurn = 0
	case storedAcc.UpstreamBurnRateCUHr > 0 && storedAcc.UpstreamAssignmentsCount > 0:
		if effectiveActive == storedAcc.UpstreamAssignmentsCount {
			effectiveBurn = storedAcc.UpstreamBurnRateCUHr
		} else if storedAcc.UpstreamUnadoptedCount > 0 {
			effectiveBurn = storedAcc.UpstreamBurnRateCUHr + localBurn
		} else if effectiveActive > storedAcc.UpstreamAssignmentsCount {
			effectiveBurn = storedAcc.UpstreamBurnRateCUHr + postPollManagedBurn
		} else {
			effectiveBurn = localBurn
		}
	default:
		effectiveBurn = localBurn
	}

	prevActive := storedAcc.ActiveAssignments
	storedAcc.ActiveAssignments = effectiveActive
	storedAcc.LiveBurnRateCUHr = math.Round(effectiveBurn*100) / 100
	storedAcc.ActiveSessionSummary = strings.Join(summaries, " + ")

	if effectiveActive < prevActive && storedAcc.Health == models.HealthMaxConcurrency {
		storedAcc.CooldownUntil = nil
		storedAcc.HealthReason = ""
	}
	hasActiveCooldown := storedAcc.CooldownUntil != nil && now.Before(*storedAcc.CooldownUntil)
	if effectiveActive == 0 {
		if storedAcc.Health == models.HealthActiveWorking ||
			(storedAcc.Health == models.HealthMaxConcurrency && !hasActiveCooldown) {
			storedAcc.Health = models.HealthHealthy
		}
	} else {
		if storedAcc.MaxConcurrentVMs > 0 && effectiveActive >= storedAcc.MaxConcurrentVMs {
			storedAcc.Health = models.HealthMaxConcurrency
		} else if storedAcc.Health == models.HealthHealthy ||
			storedAcc.Health == models.HealthActiveWorking ||
			(storedAcc.Health == models.HealthMaxConcurrency && !hasActiveCooldown) {
			storedAcc.Health = models.HealthActiveWorking
		}
	}

	storedAcc.UpdatedAt = now
	_ = e.st.SaveAccount(ctx, storedAcc)
	e.localAcctMu.Unlock()
	e.publishEvent(models.WSEventAccountUpdated, storedAcc)
}

func (e *Engine) lookupAccountBestEffort(ctx context.Context, accountID string) *models.Account {
	if accountID == "" {
		return nil
	}
	if e.pool != nil {
		if a, err := e.pool.GetAccount(ctx, accountID); err == nil && a != nil {
			return a
		}
	}
	if e.st != nil {
		if a, err := e.st.GetAccount(ctx, accountID); err == nil && a != nil {
			return a
		}
	}
	return nil
}

func (e *Engine) markGPUEntitlementSafe(
	ctx context.Context,
	accountID string,
	tier models.AcceleratorType,
	status models.GPUEntitlementStatus,
	reason string,
	cooldown time.Duration,
) {
	if accountID == "" {
		return
	}
	if e.pool != nil {
		_ = e.pool.MarkGPUEntitlement(ctx, accountID, tier, status, reason, cooldown)
		return
	}
	if e.st == nil {
		return
	}
	e.localAcctMu.Lock()
	defer e.localAcctMu.Unlock()
	acc, err := e.st.GetAccount(ctx, accountID)
	if err != nil || acc == nil {
		return
	}
	if acc.GPUEntitlements == nil {
		acc.GPUEntitlements = make(map[models.AcceleratorType]models.GPUEntitlement)
	}
	now := time.Now().UTC()
	ent := acc.GPUEntitlements[tier]
	ent.Accelerator = tier
	ent.Status = status
	ent.Reason = reason
	ent.VerifiedAt = &now
	if cooldown > 0 {
		until := now.Add(cooldown)
		ent.CooldownUntil = &until
	} else {
		ent.CooldownUntil = nil
	}
	acc.GPUEntitlements[tier] = ent
	_ = e.st.SaveAccount(ctx, acc)
}

func (e *Engine) markAccountHealthSafe(
	ctx context.Context,
	accountID string,
	health models.AccountHealth,
	reason string,
	cooldown time.Duration,
) {
	if accountID == "" {
		return
	}
	if e.pool != nil {
		_ = e.pool.MarkAccountHealth(ctx, accountID, health, reason, cooldown)
		return
	}
	if e.st == nil {
		return
	}
	e.localAcctMu.Lock()
	defer e.localAcctMu.Unlock()
	acc, err := e.st.GetAccount(ctx, accountID)
	if err != nil || acc == nil {
		return
	}
	acc.Health = health
	acc.HealthReason = reason
	if cooldown > 0 {
		until := time.Now().UTC().Add(cooldown)
		acc.CooldownUntil = &until
	} else {
		acc.CooldownUntil = nil
	}
	_ = e.st.SaveAccount(ctx, acc)
}

func (e *Engine) publishEvent(evType models.WSEventType, payload any) {
	if e.hub == nil {
		return
	}
	e.hub.Publish(models.WSEvent{
		Type:      evType,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	})
}

func (e *Engine) publishStreamChunk(
	sess *models.RuntimeSession,
	stream string,
	text string,
	rich *models.KernelRichOutput,
) {
	if e.hub == nil || sess == nil {
		return
	}
	chunk := models.SessionStreamChunk{
		SessionID:    sess.ID,
		ExperimentID: sess.ExperimentID,
		ShardIndex:   sess.ShardIndex,
		Stream:       stream,
		Text:         text,
		RichOutput:   rich,
		Timestamp:    time.Now().UTC(),
	}
	e.hub.Publish(models.WSEvent{
		Type:      models.WSEventSessionOutput,
		Timestamp: chunk.Timestamp,
		Payload:   chunk,
	})
}

func computeUptimeSec(sess *models.RuntimeSession) int64 {
	if sess == nil || sess.CreatedAt.IsZero() {
		return 0
	}
	end := time.Now().UTC()
	if sess.StoppedAt != nil && !sess.StoppedAt.IsZero() {
		end = *sess.StoppedAt
	}
	sec := int64(end.Sub(sess.CreatedAt).Seconds())
	if sec < 0 {
		return 0
	}
	return sec
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
