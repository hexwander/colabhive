// Package commtunnel implements a full-duplex multiplexed TCP-over-Jupyter-Comm tunnel
// over Google Colab's authenticated port-8080 Jupyter WebSocket (`/api/kernels/{id}/channels`).
// Both ColabHive App Store Studios (ComfyUI, WanGP, JupyterLab) and the Uncensored LLM Gateway
// (Ollama `127.0.0.1:11434`) share this transport for true concurrent multi-stream HTTP/SSE/WS access.
package commtunnel

import (
	"bufio"
	"bytes"
	"compress/zlib"
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
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	xproxy "golang.org/x/net/proxy"

	"github.com/colabhive/colabhive/internal/models"
)

const (
	// maxCommStreamBufferBytes bounds unread bytes queued per stream so a single stalled consumer
	// can neither block the shared WS readLoop (head-of-line blocking) nor OOM the local process.
	maxCommStreamBufferBytes = 64 << 20 // 64 MiB

	// commFlowCtrlWindowBytes caps total unacknowledged wire bytes queued inside Colab's remote
	// Jupyter WebSocket outbound buffer so huge payloads (e.g. WanGP's 11.8 MB Gradio HTML)
	// cannot head-of-line block parallel requests or `{"op":"opened"}` control frames.
	commFlowCtrlWindowBytes = 384 * 1024

	// commFlowCtrlAckBatchBytes triggers an immediate credit refill ack to keep WAN links saturated.
	commFlowCtrlAckBatchBytes = 64 * 1024

	// maxDecompressedChunkBytes caps per-frame zlib inflation against malformed or hostile payloads.
	maxDecompressedChunkBytes = 4 << 20 // 4 MiB
)

// ErrStreamOverflow is returned when a stalled reader allows more than 64 MiB of unread bytes
// to accumulate on a single multiplexed TCP stream.
var ErrStreamOverflow = errors.New("comm tunnel stream receive buffer exceeded 64 MiB limit")

var errProxyAuthRejected = errors.New("colab runtime proxy rejected credentials")

// ServerBootstrapPython runs inside a dedicated lightweight python3 kernel on a real Google Colab VM.
// It registers an IPython Comm target ("colabhive_tcp_tunnel") that multiplexes arbitrary full-duplex
// TCP streams (HTTP/1.1, SSE token streams, large binary downloads, Range requests, and WebSocket upgrades)
// to 127.0.0.1:<port> over Colab's official authenticated port-8080 Jupyter WebSocket, with optional
// per-chunk zlib compression (7x-10x speedup on Gradio/ComfyUI HTML/JSON) and credit-based flow control
// to eliminate WebSocket outbound buffer head-of-line blocking.
const ServerBootstrapPython = `import base64, socket, threading, zlib
from IPython import get_ipython

if globals().get("_ch_tunnel_version") != 3:
    _ch_tunnels = {}
    _ch_tunnels_lock = threading.Lock()
    _ch_send_lock = threading.Lock()
    _ch_active_close_ev = None
    _ch_active_fc_cond = None

    def _ch_close_sock(s):
        try:
            s.shutdown(socket.SHUT_RDWR)
        except Exception:
            pass
        try:
            s.close()
        except Exception:
            pass

    def _ch_close_all_tunnels():
        global _ch_active_close_ev, _ch_active_fc_cond
        with _ch_tunnels_lock:
            prev_ev = _ch_active_close_ev
            prev_cond = _ch_active_fc_cond
            _ch_active_close_ev = None
            _ch_active_fc_cond = None
            old_socks = list(_ch_tunnels.values())
            _ch_tunnels.clear()
        if prev_ev is not None:
            prev_ev.set()
        if prev_cond is not None:
            with prev_cond:
                prev_cond.notify_all()
        for old_sock in old_socks:
            _ch_close_sock(old_sock)

    def _ch_register_tunnel(comm, open_msg):
        global _ch_active_close_ev, _ch_active_fc_cond
        # Close any sockets & reader threads left over from a prior dropped WebSocket / comm.
        _ch_close_all_tunnels()
        comm_closed = threading.Event()
        fc_cond = threading.Condition()
        init_d = (open_msg or {}).get("content", {}).get("data", {}) or {}
        flow_ctrl = bool(init_d.get("flow_ctrl"))
        win_bytes = int(init_d.get("win") or 393216)
        if win_bytes < 65536:
            win_bytes = 65536
        use_zlib = bool(init_d.get("zlib", True))
        fc_state = {"inflight": 0}
        with _ch_tunnels_lock:
            _ch_active_close_ev = comm_closed
            _ch_active_fc_cond = fc_cond

        def _send(obj):
            if comm_closed.is_set():
                return
            with _ch_send_lock:
                if not comm_closed.is_set():
                    comm.send(obj)

        def _reader(sid, sock):
            try:
                while not comm_closed.is_set():
                    data = sock.recv(131072)
                    if not data:
                        break
                    zflag = 0
                    payload = data
                    if use_zlib and len(data) >= 512:
                        zd = zlib.compress(data, 1)
                        if len(zd) + 64 < len(data):
                            payload = zd
                            zflag = 1
                    b64 = base64.b64encode(payload).decode("ascii")
                    wlen = len(b64)
                    if flow_ctrl:
                        with fc_cond:
                            while (not comm_closed.is_set()) and fc_state["inflight"] > 0 and (fc_state["inflight"] + wlen > win_bytes):
                                fc_cond.wait(timeout=0.5)
                            if comm_closed.is_set():
                                break
                            fc_state["inflight"] += wlen
                    msg_obj = {"op": "data", "sid": sid, "b64": b64}
                    if zflag:
                        msg_obj["z"] = 1
                    if flow_ctrl:
                        msg_obj["w"] = wlen
                    _send(msg_obj)
            except Exception:
                pass
            finally:
                _ch_close_sock(sock)
                with _ch_tunnels_lock:
                    if _ch_tunnels.get(sid) is sock:
                        _ch_tunnels.pop(sid, None)
                if not comm_closed.is_set():
                    try:
                        _send({"op": "close", "sid": sid})
                    except Exception:
                        pass

        @comm.on_close
        def _on_close(msg):
            comm_closed.set()
            with fc_cond:
                fc_cond.notify_all()
            with _ch_tunnels_lock:
                is_current = (_ch_active_close_ev is comm_closed)
            if is_current:
                _ch_close_all_tunnels()

        @comm.on_msg
        def _on_msg(msg):
            if comm_closed.is_set():
                return
            d = (msg or {}).get("content", {}).get("data", {})
            op = d.get("op")
            if op == "ack":
                n = int(d.get("n") or 0)
                if flow_ctrl and n > 0:
                    with fc_cond:
                        fc_state["inflight"] = max(0, fc_state["inflight"] - n)
                        fc_cond.notify_all()
                return
            sid = d.get("sid")
            if op == "open":
                port = int(d.get("port", 8188))
                try:
                    s = socket.create_connection(("127.0.0.1", port), timeout=10)
                    # Clear connect timeout so idle WebSockets (/ws) and slow >10s responses stay alive!
                    s.settimeout(None)
                    s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
                    stale = False
                    with _ch_tunnels_lock:
                        if comm_closed.is_set():
                            stale = True
                        else:
                            _ch_tunnels[sid] = s
                    if stale:
                        _ch_close_sock(s)
                        return
                    _send({"op": "opened", "sid": sid})
                    threading.Thread(target=_reader, args=(sid, s), daemon=True).start()
                except Exception as exc:
                    _send({"op": "err", "sid": sid, "err": str(exc)})
            elif op == "data":
                with _ch_tunnels_lock:
                    s = _ch_tunnels.get(sid)
                if s is not None:
                    try:
                        s.sendall(base64.b64decode(d.get("b64", "")))
                    except Exception:
                        _ch_close_sock(s)
                        with _ch_tunnels_lock:
                            if _ch_tunnels.get(sid) is s:
                                _ch_tunnels.pop(sid, None)
                        try:
                            _send({"op": "close", "sid": sid})
                        except Exception:
                            pass
            elif op == "close":
                with _ch_tunnels_lock:
                    s = _ch_tunnels.pop(sid, None)
                if s is not None:
                    _ch_close_sock(s)

    _ip = get_ipython()
    _ip.kernel.comm_manager.register_target("colabhive_tcp_tunnel", _ch_register_tunnel)
    _ch_tunnel_version = 3

print("COLABHIVE_COMM_TUNNEL_READY")
`

// IsColabPort8080RuntimeProxy returns true for real Google Colab `*.prod.r.colab.dev` runtime proxies
// whose JWT is scoped exclusively to port 8080 (Jupyter), requiring the Jupyter Comm TCP tunnel to reach
// secondary app ports (8188, 7861, 8890, 8888, 11434, etc.).
func IsColabPort8080RuntimeProxy(sess *models.RuntimeSession) bool {
	if sess == nil || strings.TrimSpace(sess.ProxyURL) == "" || strings.TrimSpace(sess.ProxyToken) == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(sess.ProxyURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Hostname()), ".prod.r.colab.dev")
}

// SupportsJupyterCommTunnel returns true when sess exposes a live HTTP/HTTPS Jupyter proxy endpoint
// with an authentication token (excluding `demo://` simulation sessions and empty mock endpoints).
func SupportsJupyterCommTunnel(sess *models.RuntimeSession) bool {
	if sess == nil || strings.TrimSpace(sess.ProxyURL) == "" || strings.TrimSpace(sess.ProxyToken) == "" {
		return false
	}
	raw := strings.TrimSpace(sess.ProxyURL)
	if strings.HasPrefix(strings.ToLower(raw), "demo://") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return true
}

// Options configures a multiplexed Jupyter Comm TCP tunnel to a single remote loopback port.
type Options struct {
	SessionID             string
	RemotePort            int
	GetSession            func(ctx context.Context) (*models.RuntimeSession, error)
	RefreshSession        func(ctx context.Context) (*models.RuntimeSession, error)
	GetEgressProxy        func(ctx context.Context, sess *models.RuntimeSession) string
	ResponseHeaderTimeout time.Duration
}

type commJupyterMsg struct {
	Channel      string         `json:"channel,omitempty"`
	Header       map[string]any `json:"header"`
	ParentHeader map[string]any `json:"parent_header"`
	Metadata     map[string]any `json:"metadata"`
	Content      map[string]any `json:"content"`
}

// commConn bundles a single outer Jupyter WebSocket connection with its own Comm ID,
// write mutex, flow-control ack accounting, and sync.Once-protected teardown signal so concurrent
// Close()/readLoop()/reconnect paths can never double-close channels or interfere across generations.
type commConn struct {
	ws            *websocket.Conn
	commID        string
	clientSession string
	done          chan struct{}
	doneOnce      sync.Once
	writeMu       sync.Mutex
	lastRecvNano  atomic.Int64
	ackPending    atomic.Int64
	ackImmediate  chan struct{}
	ackDeferred   chan struct{}
}

func newCommConn(ws *websocket.Conn, commID, clientSession string) *commConn {
	c := &commConn{
		ws:            ws,
		commID:        commID,
		clientSession: clientSession,
		done:          make(chan struct{}),
		ackImmediate:  make(chan struct{}, 1),
		ackDeferred:   make(chan struct{}, 1),
	}
	c.lastRecvNano.Store(time.Now().UnixNano())
	return c
}

func (c *commConn) recordFlowCtrlBytes(n int64) {
	if c == nil || n <= 0 {
		return
	}
	if c.ackPending.Add(n) >= commFlowCtrlAckBatchBytes {
		select {
		case c.ackImmediate <- struct{}{}:
		default:
		}
		return
	}
	select {
	case c.ackDeferred <- struct{}{}:
	default:
	}
}

func (c *commConn) closeDone() {
	if c == nil {
		return
	}
	c.doneOnce.Do(func() {
		close(c.done)
	})
}

var defaultOuterTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          32,
	MaxIdleConnsPerHost:   16,
	IdleConnTimeout:       60 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	DisableCompression:    true,
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
		creds := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))
		authHeader = "Proxy-Authorization: Basic " + creds + "\r\n"
	}
	return func(ctx context.Context, _, targetAddr string) (net.Conn, error) {
		rawConn, err := (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(rawConn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
		connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", targetAddr, targetAddr, authHeader)
		if _, err := io.WriteString(tlsConn, connectReq); err != nil {
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
		if resp.StatusCode != http.StatusOK {
			_ = tlsConn.Close()
			return nil, fmt.Errorf("HTTPS proxy CONNECT rejected: %s", resp.Status)
		}
		return &bufferedTLSProxyConn{Conn: tlsConn, r: br}, nil
	}
}

func buildOuterClientAndDialer(rawProxyURL string) (*http.Client, *websocket.Dialer, func()) {
	trimmed := strings.TrimSpace(rawProxyURL)
	if trimmed == "" || strings.EqualFold(trimmed, "direct") || strings.HasPrefix(strings.ToLower(trimmed), "demo://") {
		return &http.Client{
				Transport: defaultOuterTransport,
				Timeout:   15 * time.Second,
			}, &websocket.Dialer{
				Proxy:            http.ProxyFromEnvironment,
				HandshakeTimeout: 10 * time.Second,
			}, func() {}
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return &http.Client{
				Transport: defaultOuterTransport,
				Timeout:   15 * time.Second,
			}, &websocket.Dialer{
				Proxy:            http.ProxyFromEnvironment,
				HandshakeTimeout: 10 * time.Second,
			}, func() {}
	}

	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}
	wsDialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		if dialer, dErr := xproxy.FromURL(parsed, &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}); dErr == nil {
			if ctxDialer, ok := dialer.(xproxy.ContextDialer); ok {
				tr.DialContext = ctxDialer.DialContext
				wsDialer.NetDialContext = ctxDialer.DialContext
			} else {
				dialFn := func(_ context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
				tr.DialContext = dialFn
				wsDialer.NetDialContext = dialFn
			}
		} else {
			tr.Proxy = http.ProxyFromEnvironment
			wsDialer.Proxy = http.ProxyFromEnvironment
		}
	case "http":
		tr.Proxy = http.ProxyURL(parsed)
		wsDialer.Proxy = http.ProxyURL(parsed)
	case "https":
		tr.Proxy = http.ProxyURL(parsed)
		wsDialer.NetDialContext = dialHTTPSProxyConnect(parsed)
	default:
		tr.Proxy = http.ProxyFromEnvironment
		wsDialer.Proxy = http.ProxyFromEnvironment
	}

	return &http.Client{
			Transport: tr,
			Timeout:   15 * time.Second,
		}, wsDialer, func() {
			tr.CloseIdleConnections()
		}
}

// Tunnel multiplexes concurrent full-duplex TCP connections to `127.0.0.1:<RemotePort>` inside
// a Colab GPU VM over a single persistent Jupyter Comm WebSocket on port 8080.
type Tunnel struct {
	sessionID      string
	remotePort     int
	getSession     func(ctx context.Context) (*models.RuntimeSession, error)
	refreshSession func(ctx context.Context) (*models.RuntimeSession, error)
	getEgressProxy func(ctx context.Context, sess *models.RuntimeSession) string

	connectMu sync.Mutex
	mu        sync.Mutex
	closed    bool

	proxyURL       string
	proxyToken     string
	egressProxyURL string
	tunnelKernelID string
	active         *commConn

	streams   map[string]*commStream
	streamSeq atomic.Uint64
	transport *http.Transport
}

// New constructs a multiplexed Comm TCP Tunnel targeting `127.0.0.1:<opts.RemotePort>`.
func New(opts Options) *Tunnel {
	hdrTimeout := opts.ResponseHeaderTimeout
	if hdrTimeout <= 0 {
		hdrTimeout = 120 * time.Second
	}
	t := &Tunnel{
		sessionID:      opts.SessionID,
		remotePort:     opts.RemotePort,
		getSession:     opts.GetSession,
		refreshSession: opts.RefreshSession,
		getEgressProxy: opts.GetEgressProxy,
		streams:        make(map[string]*commStream),
	}
	t.transport = &http.Transport{
		DialContext:           t.DialContext,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: hdrTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}
	return t
}

// Transport returns the shared HTTP/1.1 RoundTripper backed by this multiplexed TCP tunnel.
func (t *Tunnel) Transport() *http.Transport {
	return t.transport
}

// IsClosed reports whether Close() has been called on this tunnel.
func (t *Tunnel) IsClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// detachConn cleanly tears down a single outer WebSocket generation without touching
// any newer connection or its streams if a reconnect has already replaced t.active.
func (t *Tunnel) detachConn(cc *commConn) {
	if cc == nil {
		return
	}
	cc.closeDone()
	if cc.ws != nil {
		_ = cc.ws.Close()
	}

	t.mu.Lock()
	var broken []*commStream
	if t.active == cc {
		t.active = nil
		broken = make([]*commStream, 0, len(t.streams))
		for _, s := range t.streams {
			broken = append(broken, s)
		}
		t.streams = make(map[string]*commStream)
	}
	t.mu.Unlock()

	for _, s := range broken {
		s.closeLocal(io.EOF, false)
	}
}

// Close shuts down the outer WebSocket, aborts all in-flight streams, and asynchronously deletes
// the dedicated lightweight tunnel kernel on the remote Colab VM.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cc := t.active
	t.active = nil
	pURL := t.proxyURL
	pTok := t.proxyToken
	egressProxy := t.egressProxyURL
	kID := t.tunnelKernelID
	streams := make([]*commStream, 0, len(t.streams))
	for _, s := range t.streams {
		streams = append(streams, s)
	}
	t.streams = make(map[string]*commStream)
	t.mu.Unlock()

	if cc != nil {
		cc.closeDone()
		if cc.ws != nil {
			_ = cc.ws.Close()
		}
	}
	for _, s := range streams {
		s.closeLocal(io.EOF, true)
	}
	if t.transport != nil {
		t.transport.CloseIdleConnections()
	}

	if pURL != "" && pTok != "" && kID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(pURL, "/")+"/api/kernels/"+url.PathEscape(kID), nil)
			if err != nil {
				return
			}
			req.Header.Set("X-Colab-Runtime-Proxy-Token", pTok)
			req.Header.Set("X-Colab-Client-Agent", "colab-cli")
			hc, _, cleanup := buildOuterClientAndDialer(egressProxy)
			defer cleanup()
			hc.Timeout = 4 * time.Second
			resp, err := hc.Do(req)
			if err == nil && resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
	}
	return nil
}

func (t *Tunnel) ensureConnected(ctx context.Context) (*commConn, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("comm tunnel closed")
	}
	if t.active != nil {
		cc := t.active
		t.mu.Unlock()
		return cc, nil
	}
	t.mu.Unlock()

	t.connectMu.Lock()
	defer t.connectMu.Unlock()

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("comm tunnel closed")
	}
	if t.active != nil {
		cc := t.active
		t.mu.Unlock()
		return cc, nil
	}
	existingKernelID := t.tunnelKernelID
	t.mu.Unlock()

	if t.getSession == nil {
		return nil, errors.New("comm tunnel missing session lookup")
	}
	sess, err := t.getSession(ctx)
	if err != nil || sess == nil {
		if err == nil {
			err = errors.New("runtime session unavailable")
		}
		return nil, err
	}

	cc, err := t.connectWithSessionLocked(ctx, sess, existingKernelID)
	if err != nil && errors.Is(err, errProxyAuthRejected) && t.refreshSession != nil && ctx.Err() == nil {
		if refreshed, rErr := t.refreshSession(ctx); rErr == nil && refreshed != nil {
			cc, err = t.connectWithSessionLocked(ctx, refreshed, existingKernelID)
		}
	}
	return cc, err
}

func (t *Tunnel) connectWithSessionLocked(
	ctx context.Context,
	sess *models.RuntimeSession,
	existingKernelID string,
) (*commConn, error) {
	pURL := strings.TrimRight(strings.TrimSpace(sess.ProxyURL), "/")
	pTok := strings.TrimSpace(sess.ProxyToken)
	if pURL == "" || pTok == "" {
		return nil, errors.New("session missing Colab proxy URL or token")
	}

	egressProxy := ""
	if t.getEgressProxy != nil {
		egressProxy = strings.TrimSpace(t.getEgressProxy(ctx, sess))
	}
	hc, wsDialer, cleanup := buildOuterClientAndDialer(egressProxy)
	defer cleanup()

	kernelID := existingKernelID
	if kernelID == "" {
		var kErr error
		kernelID, kErr = createColabTunnelKernel(ctx, hc, pURL, pTok)
		if kErr != nil {
			return nil, fmt.Errorf("create tunnel kernel: %w", kErr)
		}
	}

	clientSess := uuid.NewString()
	conn, err := dialColabTunnelWS(ctx, wsDialer, pURL, pTok, kernelID, clientSess)
	if err != nil && existingKernelID != "" && !errors.Is(err, errProxyAuthRejected) {
		// Stale tunnel kernel — create a fresh kernel and retry once.
		freshKernelID, kErr := createColabTunnelKernel(ctx, hc, pURL, pTok)
		if kErr != nil {
			return nil, fmt.Errorf("recreate tunnel kernel: %w", kErr)
		}
		kernelID = freshKernelID
		conn, err = dialColabTunnelWS(ctx, wsDialer, pURL, pTok, kernelID, clientSess)
	}
	if err != nil {
		return nil, fmt.Errorf("dial tunnel kernel WS: %w", err)
	}

	// Always execute the idempotent Comm target bootstrap on every fresh WS connection:
	// a reused kernel ID can keep its ID after a Jupyter auto-restart while wiping Python globals.
	if bErr := executeCommBootstrapOnWS(ctx, conn, clientSess); bErr != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bootstrap comm tunnel handler: %w", bErr)
	}

	commID := uuid.NewString()
	openMsg := commJupyterMsg{
		Channel: "shell",
		Header: map[string]any{
			"msg_id":   uuid.NewString(),
			"session":  clientSess,
			"username": "colabhive",
			"date":     time.Now().UTC().Format(time.RFC3339Nano),
			"msg_type": "comm_open",
			"version":  "5.3",
		},
		ParentHeader: map[string]any{},
		Metadata:     map[string]any{},
		Content: map[string]any{
			"comm_id":     commID,
			"target_name": "colabhive_tcp_tunnel",
			"data": map[string]any{
				"flow_ctrl": true,
				"win":       commFlowCtrlWindowBytes,
				"zlib":      true,
			},
		},
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if wErr := conn.WriteJSON(openMsg); wErr != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send comm_open: %w", wErr)
	}
	_ = conn.SetWriteDeadline(time.Time{})

	cc := newCommConn(conn, commID, clientSess)
	conn.SetPongHandler(func(string) error {
		cc.lastRecvNano.Store(time.Now().UnixNano())
		return nil
	})
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		cc.closeDone()
		_ = conn.Close()
		return nil, errors.New("comm tunnel closed")
	}
	t.proxyURL = pURL
	t.proxyToken = pTok
	t.egressProxyURL = egressProxy
	t.tunnelKernelID = kernelID
	t.active = cc
	t.mu.Unlock()

	go t.readLoop(cc)
	go t.ackLoop(cc)
	go t.pingLoop(cc)
	return cc, nil
}

func createColabTunnelKernel(ctx context.Context, hc *http.Client, proxyURL, proxyToken string) (string, error) {
	body := []byte(`{"name":"python3"}`)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 800 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL+"/api/kernels", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
		req.Header.Set("X-Colab-Client-Agent", "colab-cli")
		resp, err := hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return "", fmt.Errorf("POST /api/kernels HTTP %d: %w", resp.StatusCode, errProxyAuthRejected)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var parsed struct {
				ID string `json:"id"`
			}
			clean := bytes.TrimPrefix(raw, []byte(")]}'\n"))
			if jErr := json.Unmarshal(clean, &parsed); jErr == nil && parsed.ID != "" {
				return parsed.ID, nil
			}
		}
		lastErr = fmt.Errorf("POST /api/kernels HTTP %d", resp.StatusCode)
	}
	return "", lastErr
}

func dialColabTunnelWS(
	ctx context.Context,
	dialer *websocket.Dialer,
	proxyURL, proxyToken, kernelID, clientSess string,
) (*websocket.Conn, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	parsed.Path = path.Join(parsed.Path, "/api/kernels", kernelID, "channels")
	q := parsed.Query()
	q.Set("session_id", clientSess)
	q.Set("colab-runtime-proxy-token", proxyToken)
	parsed.RawQuery = q.Encode()

	headers := http.Header{}
	headers.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
	headers.Set("X-Colab-Client-Agent", "colab-cli")

	if dialer == nil {
		dialer = &websocket.Dialer{
			Proxy:            http.ProxyFromEnvironment,
			HandshakeTimeout: 10 * time.Second,
		}
	}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 1000 * time.Millisecond):
			}
		}
		conn, resp, dErr := dialer.DialContext(ctx, parsed.String(), headers)
		if dErr == nil {
			return conn, nil
		}
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		lastErr = dErr
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return nil, fmt.Errorf("HTTP %d: %w (%v)", resp.StatusCode, errProxyAuthRejected, dErr)
		}
	}
	return nil, lastErr
}

func executeCommBootstrapOnWS(ctx context.Context, conn *websocket.Conn, clientSess string) error {
	msgID := uuid.NewString()
	req := commJupyterMsg{
		Channel: "shell",
		Header: map[string]any{
			"msg_id":   msgID,
			"session":  clientSess,
			"username": "colabhive",
			"date":     time.Now().UTC().Format(time.RFC3339Nano),
			"msg_type": "execute_request",
			"version":  "5.3",
		},
		ParentHeader: map[string]any{},
		Metadata:     map[string]any{},
		Content: map[string]any{
			"code":             ServerBootstrapPython,
			"silent":           false,
			"store_history":    false,
			"user_expressions": map[string]any{},
			"allow_stdin":      false,
			"stop_on_error":    true,
		},
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(req); err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Time{})

	dl := time.Now().Add(15 * time.Second)
	if ctxDL, ok := ctx.Deadline(); ok && ctxDL.Before(dl) {
		dl = ctxDL
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = conn.SetReadDeadline(dl)
		mt, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if mt != websocket.TextMessage {
			continue
		}
		var m commJupyterMsg
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		pID, _ := m.ParentHeader["msg_id"].(string)
		if pID != msgID {
			continue
		}
		mType, _ := m.Header["msg_type"].(string)
		if mType == "execute_reply" {
			_ = conn.SetReadDeadline(time.Time{})
			st, _ := m.Content["status"].(string)
			if st != "" && st != "ok" {
				ev, _ := m.Content["evalue"].(string)
				return fmt.Errorf("comm bootstrap execute_reply status=%s %s", st, ev)
			}
			return nil
		}
	}
}

func (t *Tunnel) sendCommData(data map[string]any) error {
	return t.sendCommDataWithDeadline(nil, data, time.Time{})
}

func (t *Tunnel) sendCommDataWithDeadline(target *commConn, data map[string]any, deadline time.Time) error {
	cc := target
	if cc == nil {
		t.mu.Lock()
		cc = t.active
		t.mu.Unlock()
	}
	if cc == nil || cc.ws == nil || cc.commID == "" {
		return errors.New("comm tunnel not connected")
	}

	wsDL := time.Now().Add(15 * time.Second)
	if !deadline.IsZero() && deadline.Before(wsDL) {
		wsDL = deadline
	}
	if time.Until(wsDL) <= 0 {
		return &net.OpError{Op: "write", Net: "tcp", Err: osTimeoutErr{}}
	}

	msg := commJupyterMsg{
		Channel: "shell",
		Header: map[string]any{
			"msg_id":   uuid.NewString(),
			"session":  cc.clientSession,
			"username": "colabhive",
			"date":     time.Now().UTC().Format(time.RFC3339Nano),
			"msg_type": "comm_msg",
			"version":  "5.3",
		},
		ParentHeader: map[string]any{},
		Metadata:     map[string]any{},
		Content: map[string]any{
			"comm_id": cc.commID,
			"data":    data,
		},
	}

	cc.writeMu.Lock()
	defer cc.writeMu.Unlock()
	_ = cc.ws.SetWriteDeadline(wsDL)
	err := cc.ws.WriteJSON(msg)
	_ = cc.ws.SetWriteDeadline(time.Time{})
	return err
}

func (t *Tunnel) pingLoop(cc *commConn) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cc.done:
			return
		case <-ticker.C:
			cc.writeMu.Lock()
			err := cc.ws.WriteControl(websocket.PingMessage, []byte("ch"), time.Now().Add(5*time.Second))
			cc.writeMu.Unlock()
			if err != nil {
				_ = cc.ws.Close()
				return
			}
		}
	}
}

func (t *Tunnel) ackLoop(cc *commConn) {
	var deferTimer *time.Timer
	var deferCh <-chan time.Time
	flush := func() bool {
		n := cc.ackPending.Swap(0)
		if n <= 0 {
			return true
		}
		if err := t.sendCommDataWithDeadline(cc, map[string]any{"op": "ack", "n": n}, time.Now().Add(10*time.Second)); err != nil {
			_ = cc.ws.Close()
			return false
		}
		return true
	}
	defer func() {
		if deferTimer != nil {
			deferTimer.Stop()
		}
	}()

	for {
		select {
		case <-cc.done:
			return
		case <-cc.ackImmediate:
			if deferTimer != nil {
				deferTimer.Stop()
				deferTimer = nil
				deferCh = nil
			}
			if !flush() {
				return
			}
		case <-cc.ackDeferred:
			if deferTimer == nil {
				deferTimer = time.NewTimer(60 * time.Millisecond)
				deferCh = deferTimer.C
			}
		case <-deferCh:
			deferTimer = nil
			deferCh = nil
			if !flush() {
				return
			}
		}
	}
}

func isCompressedFlag(v any) bool {
	switch tv := v.(type) {
	case bool:
		return tv
	case float64:
		return tv != 0
	case int:
		return tv != 0
	case int64:
		return tv != 0
	default:
		return false
	}
}

func parseFlowCtrlWireBytes(v any) int64 {
	switch tv := v.(type) {
	case float64:
		if tv > 0 {
			return int64(tv)
		}
	case int:
		if tv > 0 {
			return int64(tv)
		}
	case int64:
		if tv > 0 {
			return tv
		}
	}
	return 0
}

func inflateZlibChunk(raw []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, maxDecompressedChunkBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecompressedChunkBytes {
		return nil, fmt.Errorf("decompressed comm chunk exceeded %d byte safety limit", maxDecompressedChunkBytes)
	}
	return out, nil
}

func (t *Tunnel) readLoop(cc *commConn) {
	defer t.detachConn(cc)

	for {
		_ = cc.ws.SetReadDeadline(time.Time{})
		mt, raw, err := cc.ws.ReadMessage()
		if err != nil {
			return
		}
		cc.lastRecvNano.Store(time.Now().UnixNano())
		if mt != websocket.TextMessage {
			continue
		}
		var m commJupyterMsg
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		mType, _ := m.Header["msg_type"].(string)
		if mType != "comm_msg" {
			continue
		}
		// Ignore any stale comm_msg belonging to an older comm_id.
		if msgCommID, _ := m.Content["comm_id"].(string); msgCommID != "" && msgCommID != cc.commID {
			continue
		}
		data, _ := m.Content["data"].(map[string]any)
		if data == nil {
			continue
		}
		op, _ := data["op"].(string)
		if op == "data" {
			// Always credit remote flow-control window even if this stream was closed locally
			// mid-transfer, otherwise aborted browser requests would leak window credits forever.
			if wBytes := parseFlowCtrlWireBytes(data["w"]); wBytes > 0 {
				cc.recordFlowCtrlBytes(wBytes)
			}
		}
		sid, _ := data["sid"].(string)
		if sid == "" {
			continue
		}

		t.mu.Lock()
		st := t.streams[sid]
		t.mu.Unlock()
		if st == nil {
			continue
		}

		switch op {
		case "opened":
			st.markOpened(nil)
		case "err":
			errMsg, _ := data["err"].(string)
			if errMsg == "" {
				errMsg = "remote dial failed"
			}
			st.markOpened(errors.New(errMsg))
			t.removeStream(sid)
			st.closeLocal(errors.New(errMsg), true)
		case "data":
			b64, _ := data["b64"].(string)
			if b64 != "" {
				chunk, dErr := base64.StdEncoding.DecodeString(b64)
				if dErr == nil && len(chunk) > 0 {
					if isCompressedFlag(data["z"]) {
						inflated, zErr := inflateZlibChunk(chunk)
						if zErr != nil {
							t.removeStream(sid)
							st.closeLocal(zErr, true)
							go func(targetConn *commConn, sID string) {
								_ = t.sendCommDataWithDeadline(targetConn, map[string]any{"op": "close", "sid": sID}, time.Time{})
							}(cc, sid)
							continue
						}
						chunk = inflated
					}
					// pushData is strictly O(1) non-blocking; if a single stalled stream exceeds
					// maxCommStreamBufferBytes, abort ONLY that stream without blocking sibling streams.
					if len(chunk) > 0 && st.pushData(chunk) {
						t.removeStream(sid)
						go func(targetConn *commConn, sID string) {
							_ = t.sendCommDataWithDeadline(targetConn, map[string]any{"op": "close", "sid": sID}, time.Time{})
						}(cc, sid)
					}
				}
			}
		case "close":
			t.removeStream(sid)
			st.closeLocal(io.EOF, false)
		}
	}
}

func (t *Tunnel) removeStream(sid string) {
	t.mu.Lock()
	delete(t.streams, sid)
	t.mu.Unlock()
}

// DialContext opens a brand-new multiplexed full-duplex TCP stream to `127.0.0.1:<RemotePort>`
// on the remote Colab VM over the persistent Jupyter Comm WebSocket.
func (t *Tunnel) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	cc, err := t.ensureConnected(ctx)
	if err != nil {
		return nil, err
	}

	sid := fmt.Sprintf("s%d", t.streamSeq.Add(1))
	st := newCommStream(t, cc, sid)

	t.mu.Lock()
	if t.closed || t.active != cc {
		t.mu.Unlock()
		return nil, errors.New("comm tunnel disconnected")
	}
	t.streams[sid] = st
	t.mu.Unlock()

	if err := t.sendCommDataWithDeadline(cc, map[string]any{
		"op":   "open",
		"sid":  sid,
		"port": t.remotePort,
	}, time.Time{}); err != nil {
		t.removeStream(sid)
		// Force clean teardown of ONLY the broken outer WS generation and reconnect once with a fresh stream.
		t.detachConn(cc)
		cc2, rErr := t.ensureConnected(ctx)
		if rErr != nil {
			return nil, fmt.Errorf("comm tunnel dial reconnect: %w", rErr)
		}
		sid = fmt.Sprintf("s%d", t.streamSeq.Add(1))
		st = newCommStream(t, cc2, sid)
		t.mu.Lock()
		if t.closed || t.active != cc2 {
			t.mu.Unlock()
			return nil, errors.New("comm tunnel disconnected during reconnect")
		}
		t.streams[sid] = st
		t.mu.Unlock()
		if err2 := t.sendCommDataWithDeadline(cc2, map[string]any{"op": "open", "sid": sid, "port": t.remotePort}, time.Time{}); err2 != nil {
			t.removeStream(sid)
			return nil, err2
		}
	}

	openTimer := time.NewTimer(25 * time.Second)
	defer openTimer.Stop()
	select {
	case err := <-st.openedCh:
		if err != nil {
			t.removeStream(sid)
			return nil, err
		}
		return st, nil
	case <-st.closedCh:
		t.removeStream(sid)
		return nil, errors.New("comm tunnel stream closed before open")
	case <-openTimer.C:
		t.removeStream(sid)
		_ = st.Close()
		// If the outer WebSocket has received zero frames/pongs for >15s, detach it so
		// the very next request establishes a healthy tunnel instead of staying wedged.
		if st.cc != nil && time.Since(time.Unix(0, st.cc.lastRecvNano.Load())) > 15*time.Second {
			t.detachConn(st.cc)
		}
		return nil, fmt.Errorf("timed out opening remote TCP port %d over Colab comm tunnel", t.remotePort)
	case <-ctx.Done():
		t.removeStream(sid)
		_ = st.Close()
		return nil, ctx.Err()
	}
}

type dummyTunnelAddr string

func (d dummyTunnelAddr) Network() string { return "tcp" }
func (d dummyTunnelAddr) String() string  { return string(d) }

type commStream struct {
	tunnel    *Tunnel
	cc        *commConn
	sid       string
	openedCh  chan error
	openOnce  sync.Once
	closedCh  chan struct{}
	closeOnce sync.Once
	notifyCh  chan struct{}

	mu          sync.Mutex
	chunks      [][]byte
	queuedBytes int
	closed      bool
	dropUnread  bool
	closeErr    error
	readDL      time.Time
	writeDL     time.Time
	deadlineCh  chan struct{}
}

func newCommStream(tunnel *Tunnel, cc *commConn, sid string) *commStream {
	return &commStream{
		tunnel:     tunnel,
		cc:         cc,
		sid:        sid,
		openedCh:   make(chan error, 1),
		closedCh:   make(chan struct{}),
		notifyCh:   make(chan struct{}, 1),
		deadlineCh: make(chan struct{}),
	}
}

func (s *commStream) markOpened(err error) {
	s.openOnce.Do(func() {
		s.openedCh <- err
	})
}

// pushData appends an incoming chunk without ever blocking the shared readLoop.
// Returns true if this stream overflowed maxCommStreamBufferBytes and was aborted locally.
func (s *commStream) pushData(chunk []byte) bool {
	if len(chunk) == 0 {
		return false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	if s.queuedBytes+len(chunk) > maxCommStreamBufferBytes {
		s.closed = true
		s.dropUnread = true
		s.chunks = nil
		s.queuedBytes = 0
		if s.closeErr == nil {
			s.closeErr = ErrStreamOverflow
		}
		s.mu.Unlock()
		s.closeOnce.Do(func() {
			close(s.closedCh)
		})
		return true
	}
	s.chunks = append(s.chunks, chunk)
	s.queuedBytes += len(chunk)
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
	s.mu.Unlock()
	return false
}

func (s *commStream) closeLocal(err error, dropUnread bool) {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.closeErr = err
	}
	if dropUnread {
		s.dropUnread = true
		s.chunks = nil
		s.queuedBytes = 0
	}
	s.mu.Unlock()
	s.closeOnce.Do(func() {
		close(s.closedCh)
	})
}

func (s *commStream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.chunks) > 0 && !s.dropUnread {
			chunk := s.chunks[0]
			n := copy(p, chunk)
			if n < len(chunk) {
				s.chunks[0] = chunk[n:]
				s.queuedBytes -= n
			} else {
				s.chunks[0] = nil
				s.chunks = s.chunks[1:]
				s.queuedBytes -= len(chunk)
				if len(s.chunks) == 0 {
					s.chunks = nil
				} else if cap(s.chunks) > 64 && len(s.chunks)*4 < cap(s.chunks) {
					s.chunks = append([][]byte(nil), s.chunks...)
				}
			}
			s.mu.Unlock()
			return n, nil
		}
		if s.closed {
			err := s.closeErr
			s.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
		dl := s.readDL
		dlCh := s.deadlineCh
		s.mu.Unlock()

		var timerCh <-chan time.Time
		var timer *time.Timer
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, &net.OpError{Op: "read", Net: "tcp", Err: osTimeoutErr{}}
			}
			timer = time.NewTimer(d)
			timerCh = timer.C
		}

		select {
		case <-s.notifyCh:
			if timer != nil {
				timer.Stop()
			}
		case <-s.closedCh:
			if timer != nil {
				timer.Stop()
			}
		case <-dlCh:
			if timer != nil {
				timer.Stop()
			}
		case <-timerCh:
			return 0, &net.OpError{Op: "read", Net: "tcp", Err: osTimeoutErr{}}
		}
	}
}

type osTimeoutErr struct{}

func (osTimeoutErr) Error() string   { return "i/o timeout" }
func (osTimeoutErr) Timeout() bool   { return true }
func (osTimeoutErr) Temporary() bool { return true }

func (s *commStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	dl := s.writeDL
	s.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}

	const maxChunk = 48 * 1024
	total := 0
	for len(p) > 0 {
		if !dl.IsZero() && time.Until(dl) <= 0 {
			return total, &net.OpError{Op: "write", Net: "tcp", Err: osTimeoutErr{}}
		}
		n := len(p)
		if n > maxChunk {
			n = maxChunk
		}
		chunk := p[:n]
		if err := s.tunnel.sendCommDataWithDeadline(s.cc, map[string]any{
			"op":  "data",
			"sid": s.sid,
			"b64": base64.StdEncoding.EncodeToString(chunk),
		}, dl); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (s *commStream) Close() error {
	s.tunnel.removeStream(s.sid)
	s.closeLocal(net.ErrClosed, true)
	_ = s.tunnel.sendCommDataWithDeadline(s.cc, map[string]any{
		"op":  "close",
		"sid": s.sid,
	}, time.Time{})
	return nil
}

func (s *commStream) LocalAddr() net.Addr { return dummyTunnelAddr("127.0.0.1:0") }
func (s *commStream) RemoteAddr() net.Addr {
	return dummyTunnelAddr(fmt.Sprintf("127.0.0.1:%d", s.tunnel.remotePort))
}

func (s *commStream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.readDL = t
	s.writeDL = t
	oldCh := s.deadlineCh
	s.deadlineCh = make(chan struct{})
	s.mu.Unlock()
	close(oldCh)
	return nil
}

func (s *commStream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.readDL = t
	oldCh := s.deadlineCh
	s.deadlineCh = make(chan struct{})
	s.mu.Unlock()
	close(oldCh)
	return nil
}

func (s *commStream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.writeDL = t
	s.mu.Unlock()
	return nil
}
