package commtunnel

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/colabhive/colabhive/internal/models"
)

func TestCommTunnel_ConcurrentCloseAndDetachNoDoubleClosePanic(t *testing.T) {
	for i := 0; i < 200; i++ {
		tunnel := New(Options{SessionID: "sess-stress", RemotePort: 8188})
		cc := newCommConn(nil, "comm-1", "client-1")
		st := newCommStream(tunnel, cc, "s1")
		tunnel.mu.Lock()
		tunnel.active = cc
		tunnel.streams["s1"] = st
		tunnel.mu.Unlock()

		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = tunnel.Close()
		}()
		go func() {
			defer wg.Done()
			tunnel.detachConn(cc)
		}()
		go func() {
			defer wg.Done()
			cc.closeDone()
		}()
		wg.Wait()

		select {
		case <-cc.done:
		default:
			t.Fatalf("iteration %d: expected cc.done to be closed", i)
		}
		select {
		case <-st.closedCh:
		default:
			t.Fatalf("iteration %d: expected stream to be closed", i)
		}
	}
}

func TestCommTunnel_StaleDetachDoesNotWipeNewConnectionStreams(t *testing.T) {
	tunnel := New(Options{SessionID: "sess-gen", RemotePort: 8188})
	oldCC := newCommConn(nil, "comm-old", "client-old")
	newCC := newCommConn(nil, "comm-new", "client-new")
	newStream := newCommStream(tunnel, newCC, "s-new")

	tunnel.mu.Lock()
	tunnel.active = newCC
	tunnel.streams["s-new"] = newStream
	tunnel.mu.Unlock()

	tunnel.detachConn(oldCC)

	tunnel.mu.Lock()
	gotActive := tunnel.active
	gotStream := tunnel.streams["s-new"]
	tunnel.mu.Unlock()

	if gotActive != newCC {
		t.Fatalf("expected active connection to remain newCC")
	}
	if gotStream != newStream {
		t.Fatalf("expected new connection stream to remain registered")
	}
	select {
	case <-newStream.closedCh:
		t.Fatalf("stale detachConn must NOT close streams belonging to newCC")
	default:
	}
}

func TestCommStream_NonBlockingPushDataNoHeadOfLineBlocking(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverDone := make(chan struct{})
	clientDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(serverDone)

		// Compress 4 MiB zero/repeated chunks via zlib (`z: true`) so 17 frames inflate to
		// 68 MiB (> 64 MiB maxCommStreamBufferBytes) while keeping wire JSON tiny (~70 KB),
		// ensuring deterministic sub-second execution even under `go test -race` on 2-core CI.
		var zBuf bytes.Buffer
		zw := zlib.NewWriter(&zBuf)
		_, _ = zw.Write(bytes.Repeat([]byte("A"), 4<<20))
		_ = zw.Close()
		bigB64 := base64.StdEncoding.EncodeToString(zBuf.Bytes())
		for i := 0; i < 17; i++ {
			msg := commJupyterMsg{
				Header: map[string]any{"msg_type": "comm_msg"},
				Content: map[string]any{
					"comm_id": "comm-hol",
					"data": map[string]any{
						"op":  "data",
						"sid": "s-slow",
						"b64": bigB64,
						"z":   true,
					},
				},
			}
			if wErr := conn.WriteJSON(msg); wErr != nil {
				return
			}
		}

		fastMsg := commJupyterMsg{
			Header: map[string]any{"msg_type": "comm_msg"},
			Content: map[string]any{
				"comm_id": "comm-hol",
				"data": map[string]any{
					"op":  "data",
					"sid": "s-fast",
					"b64": base64.StdEncoding.EncodeToString([]byte("hello-fast-sibling")),
				},
			},
		}
		_ = conn.WriteJSON(fastMsg)
		_ = conn.WriteJSON(commJupyterMsg{
			Header: map[string]any{"msg_type": "comm_msg"},
			Content: map[string]any{
				"comm_id": "comm-hol",
				"data":    map[string]any{"op": "close", "sid": "s-fast"},
			},
		})
		select {
		case <-clientDone:
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientWS, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial mock WS: %v", err)
	}

	tunnel := New(Options{SessionID: "sess-hol", RemotePort: 8188})
	cc := newCommConn(clientWS, "comm-hol", "client-hol")
	slow := newCommStream(tunnel, cc, "s-slow")
	fast := newCommStream(tunnel, cc, "s-fast")
	tunnel.mu.Lock()
	tunnel.active = cc
	tunnel.streams["s-slow"] = slow
	tunnel.streams["s-fast"] = fast
	tunnel.mu.Unlock()

	go tunnel.readLoop(cc)
	defer tunnel.Close()

	_ = fast.SetReadDeadline(time.Now().Add(15 * time.Second))
	gotFast, err := io.ReadAll(fast)
	if err != nil {
		close(clientDone)
		t.Fatalf("healthy sibling stream failed: %v", err)
	}
	if string(gotFast) != "hello-fast-sibling" {
		close(clientDone)
		t.Fatalf("unexpected sibling payload: %q", string(gotFast))
	}

	buf := make([]byte, 16)
	_, slowErr := slow.Read(buf)
	close(clientDone)
	if !errors.Is(slowErr, ErrStreamOverflow) {
		t.Fatalf("expected stalled stream to return ErrStreamOverflow, got %v", slowErr)
	}

	<-serverDone
}

func TestCommStream_ReadDrainsBeforeRemoteEOFAndWakesOnDeadline(t *testing.T) {
	tunnel := New(Options{SessionID: "sess-stream", RemotePort: 8188})
	cc := newCommConn(nil, "comm-1", "client-1")
	st := newCommStream(tunnel, cc, "s1")

	st.pushData([]byte("part1-"))
	st.pushData([]byte("part2"))
	st.closeLocal(io.EOF, false)

	all, err := io.ReadAll(st)
	if err != nil {
		t.Fatalf("ReadAll after remote EOF failed: %v", err)
	}
	if string(all) != "part1-part2" {
		t.Fatalf("expected full drained payload %q, got %q", "part1-part2", string(all))
	}

	st2 := newCommStream(tunnel, cc, "s2")
	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 8)
		_, rErr := st2.Read(buf)
		errCh <- rErr
	}()
	time.Sleep(25 * time.Millisecond)
	_ = st2.SetReadDeadline(time.Now().Add(-time.Second))

	select {
	case rErr := <-errCh:
		var netErr net.Error
		if !errors.As(rErr, &netErr) || !netErr.Timeout() {
			t.Fatalf("expected timeout net.Error after SetReadDeadline, got %v", rErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("blocked Read did not wake up promptly after SetReadDeadline")
	}

	_ = st2.SetWriteDeadline(time.Now().Add(-time.Second))
	_, wErr := st2.Write([]byte("hello"))
	var netWErr net.Error
	if !errors.As(wErr, &netWErr) || !netWErr.Timeout() {
		t.Fatalf("expected timeout net.Error from Write after expired SetWriteDeadline, got %v", wErr)
	}
}

func TestServerBootstrapPython_SafetyInvariants(t *testing.T) {
	if !strings.Contains(ServerBootstrapPython, "s.settimeout(None)") {
		t.Fatalf("ServerBootstrapPython must call s.settimeout(None) after create_connection")
	}
	if !strings.Contains(ServerBootstrapPython, "_ch_close_all_tunnels()") {
		t.Fatalf("ServerBootstrapPython must close prior sockets on new comm registration")
	}
	if !strings.Contains(ServerBootstrapPython, "s.shutdown(socket.SHUT_RDWR)") {
		t.Fatalf("ServerBootstrapPython must call shutdown(SHUT_RDWR) to unblock Linux recv() promptly")
	}
	if !strings.Contains(ServerBootstrapPython, "@comm.on_close") {
		t.Fatalf("ServerBootstrapPython must register @comm.on_close handler")
	}
	if strings.Contains(ServerBootstrapPython, "sys.exit(") {
		t.Fatalf("ServerBootstrapPython must not contain sys.exit(")
	}
	if !strings.Contains(ServerBootstrapPython, "zlib.compress") {
		t.Fatalf("ServerBootstrapPython v3 must include zlib compression")
	}
	if !strings.Contains(ServerBootstrapPython, "fc_cond.notify_all()") {
		t.Fatalf("ServerBootstrapPython v3 must wake flow-control waiters on ack and close")
	}
}

func TestSupportsJupyterCommTunnel(t *testing.T) {
	cases := []struct {
		name string
		sess *models.RuntimeSession
		want bool
	}{
		{"nil", nil, false},
		{"demo", &models.RuntimeSession{ProxyURL: "demo://ep1", ProxyToken: "tok"}, false},
		{"missing token", &models.RuntimeSession{ProxyURL: "https://ep.prod.r.colab.dev", ProxyToken: ""}, false},
		{"colab port 8080", &models.RuntimeSession{ProxyURL: "https://abc.prod.r.colab.dev", ProxyToken: "jwt"}, true},
		{"local mock jupyter", &models.RuntimeSession{ProxyURL: "http://127.0.0.1:18080", ProxyToken: "tok"}, true},
	}
	for _, tc := range cases {
		if got := SupportsJupyterCommTunnel(tc.sess); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCommTunnel_EndToEndConcurrentHTTPAndIndependentCancel(t *testing.T) {
	// 1. Start a real loopback HTTP server simulating Ollama on a local ephemeral port.
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	bothInside := make(chan struct{})
	var bothOnce sync.Once
	req1Canceled := make(chan struct{})

	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		if cur >= 2 {
			bothOnce.Do(func() { close(bothInside) })
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)

		reqTag := r.URL.Query().Get("tag")
		fmt.Fprintf(w, "data: start-%s\n\n", reqTag)
		if fl != nil {
			fl.Flush()
		}

		// Wait until BOTH requests are concurrently inside the handler (proves true parallelism!).
		select {
		case <-bothInside:
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			return
		}

		if reqTag == "cancel-me" {
			// Wait for independent client cancel; must NOT disturb sibling request.
			select {
			case <-r.Context().Done():
				close(req1Canceled)
				return
			case <-time.After(5 * time.Second):
				return
			}
		}

		// Sibling request waits briefly for req1 to be canceled, then finishes streaming cleanly!
		select {
		case <-req1Canceled:
		case <-time.After(5 * time.Second):
		}
		fmt.Fprintf(w, "data: done-%s\n\n", reqTag)
		if fl != nil {
			fl.Flush()
		}
	}))
	defer upstreamSrv.Close()

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(upstreamSrv.URL, "http://"))
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}
	var upstreamPort int
	_, _ = fmt.Sscanf(portStr, "%d", &upstreamPort)

	// 2. Start a mock Jupyter server implementing POST /api/kernels + WS /api/kernels/{id}/channels
	//    with real TCP forwarding (mirroring ServerBootstrapPython) AND a one-time 401 refresh test.
	var authRefreshCalls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	jupyterSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Colab-Runtime-Proxy-Token")
		if tok != "fresh-token" {
			http.Error(w, "expired token", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/kernels" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"k-test-1"}`))
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/kernels/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/channels") {
			ws, uErr := upgrader.Upgrade(w, r, nil)
			if uErr != nil {
				return
			}
			defer ws.Close()

			var writeMu sync.Mutex
			sendJSON := func(v any) {
				writeMu.Lock()
				_ = ws.WriteJSON(v)
				writeMu.Unlock()
			}

			var socksMu sync.Mutex
			socks := make(map[string]net.Conn)
			commID := ""

			defer func() {
				socksMu.Lock()
				for _, c := range socks {
					_ = c.Close()
				}
				socksMu.Unlock()
			}()

			for {
				_, raw, rErr := ws.ReadMessage()
				if rErr != nil {
					return
				}
				var m commJupyterMsg
				if json.Unmarshal(raw, &m) != nil {
					continue
				}
				mType, _ := m.Header["msg_type"].(string)
				switch mType {
				case "execute_request":
					sendJSON(commJupyterMsg{
						Channel:      "shell",
						Header:       map[string]any{"msg_type": "execute_reply"},
						ParentHeader: map[string]any{"msg_id": m.Header["msg_id"]},
						Content:      map[string]any{"status": "ok"},
					})
				case "comm_open":
					commID, _ = m.Content["comm_id"].(string)
				case "comm_msg":
					d, _ := m.Content["data"].(map[string]any)
					op, _ := d["op"].(string)
					sid, _ := d["sid"].(string)
					switch op {
					case "open":
						pFloat, _ := d["port"].(float64)
						conn, dErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", int(pFloat)), 5*time.Second)
						if dErr != nil {
							sendJSON(commJupyterMsg{
								Header:  map[string]any{"msg_type": "comm_msg"},
								Content: map[string]any{"comm_id": commID, "data": map[string]any{"op": "err", "sid": sid, "err": dErr.Error()}},
							})
							continue
						}
						socksMu.Lock()
						socks[sid] = conn
						socksMu.Unlock()
						sendJSON(commJupyterMsg{
							Header:  map[string]any{"msg_type": "comm_msg"},
							Content: map[string]any{"comm_id": commID, "data": map[string]any{"op": "opened", "sid": sid}},
						})
						go func(streamID string, c net.Conn) {
							buf := make([]byte, 32*1024)
							for {
								n, nErr := c.Read(buf)
								if n > 0 {
									sendJSON(commJupyterMsg{
										Header: map[string]any{"msg_type": "comm_msg"},
										Content: map[string]any{
											"comm_id": commID,
											"data": map[string]any{
												"op":  "data",
												"sid": streamID,
												"b64": base64.StdEncoding.EncodeToString(buf[:n]),
											},
										},
									})
								}
								if nErr != nil {
									_ = c.Close()
									socksMu.Lock()
									delete(socks, streamID)
									socksMu.Unlock()
									sendJSON(commJupyterMsg{
										Header:  map[string]any{"msg_type": "comm_msg"},
										Content: map[string]any{"comm_id": commID, "data": map[string]any{"op": "close", "sid": streamID}},
									})
									return
								}
							}
						}(sid, conn)
					case "data":
						socksMu.Lock()
						c := socks[sid]
						socksMu.Unlock()
						if c != nil {
							b64, _ := d["b64"].(string)
							if payload, decErr := base64.StdEncoding.DecodeString(b64); decErr == nil {
								_, _ = c.Write(payload)
							}
						}
					case "close":
						socksMu.Lock()
						c := socks[sid]
						delete(socks, sid)
						socksMu.Unlock()
						if c != nil {
							_ = c.Close()
						}
					}
				}
			}
		}
	}))
	defer jupyterSrv.Close()

	// 3. Connect Tunnel starting with an expired token to verify RefreshSession recovery too!
	tunnel := New(Options{
		SessionID:  "sess-parallel",
		RemotePort: upstreamPort,
		GetSession: func(context.Context) (*models.RuntimeSession, error) {
			return &models.RuntimeSession{
				ID:         "sess-parallel",
				ProxyURL:   jupyterSrv.URL,
				ProxyToken: "stale-token",
			}, nil
		},
		RefreshSession: func(context.Context) (*models.RuntimeSession, error) {
			authRefreshCalls.Add(1)
			return &models.RuntimeSession{
				ID:         "sess-parallel",
				ProxyURL:   jupyterSrv.URL,
				ProxyToken: "fresh-token",
			}, nil
		},
	})
	defer tunnel.Close()

	hc := &http.Client{Transport: tunnel.Transport()}

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	var wg sync.WaitGroup
	wg.Add(2)

	// Request 1: opens SSE stream, then cancels independently once both requests are active.
	go func() {
		defer wg.Done()
		req, _ := http.NewRequestWithContext(ctx1, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/stream?tag=cancel-me", upstreamPort), nil)
		resp, doErr := hc.Do(req)
		if doErr != nil {
			t.Errorf("req1 failed to connect: %v", doErr)
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 256)
		_, _ = resp.Body.Read(buf)
		// Wait until both requests are simultaneously inside upstreamSrv, then abort ONLY req1!
		<-bothInside
		cancel1()
		_ = resp.Body.Close()
	}()

	// Request 2: must run concurrently with Request 1 and complete cleanly after Request 1 cancels!
	var req2Body string
	var req2Err error
	go func() {
		defer wg.Done()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/stream?tag=keep-going", upstreamPort), nil)
		resp, doErr := hc.Do(req)
		if doErr != nil {
			req2Err = doErr
			return
		}
		defer resp.Body.Close()
		raw, rErr := io.ReadAll(resp.Body)
		if rErr != nil {
			req2Err = rErr
			return
		}
		req2Body = string(raw)
	}()

	wg.Wait()

	if authRefreshCalls.Load() != 1 {
		t.Fatalf("expected RefreshSession to be invoked once on 401, got %d", authRefreshCalls.Load())
	}
	if maxInFlight.Load() < 2 {
		t.Fatalf("expected >=2 simultaneous in-flight HTTP streams over Comm tunnel, got max=%d", maxInFlight.Load())
	}
	if req2Err != nil {
		t.Fatalf("sibling req2 failed after req1 cancel: %v", req2Err)
	}
	if !strings.Contains(req2Body, "start-keep-going") || !strings.Contains(req2Body, "done-keep-going") {
		t.Fatalf("unexpected sibling req2 body: %q", req2Body)
	}
}

func TestCommTunnel_ZlibDecompressionAndFlowControlAckIncludingCanceledStream(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	wantPayload := strings.Repeat(`{"gradio":"wangp-config-chunk","ok":true}`, 2000)

	var zBuf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&zBuf, zlib.BestSpeed)
	if err != nil {
		t.Fatalf("zlib writer: %v", err)
	}
	_, _ = zw.Write([]byte(wantPayload))
	_ = zw.Close()
	zB64 := base64.StdEncoding.EncodeToString(zBuf.Bytes())

	var ackedTotal atomic.Int64
	ackDone := make(chan struct{})
	wantAckTotal := int64(len(zB64) + 70000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, uErr := upgrader.Upgrade(w, r, nil)
		if uErr != nil {
			return
		}
		defer conn.Close()

		// Read incoming ack frames from client ackLoop.
		go func() {
			for {
				_, raw, rErr := conn.ReadMessage()
				if rErr != nil {
					return
				}
				var m commJupyterMsg
				if json.Unmarshal(raw, &m) != nil {
					continue
				}
				d, _ := m.Content["data"].(map[string]any)
				if d != nil && d["op"] == "ack" {
					if n := parseFlowCtrlWireBytes(d["n"]); n > 0 {
						if ackedTotal.Add(n) >= wantAckTotal {
							select {
							case <-ackDone:
							default:
								close(ackDone)
							}
						}
					}
				}
			}
		}()

		// 1) Send a frame for an already-canceled/unknown stream ID ("s-ghost") with w=70000.
		// Flow control MUST still credit its wire bytes so the remote window never leaks!
		_ = conn.WriteJSON(commJupyterMsg{
			Header: map[string]any{"msg_type": "comm_msg"},
			Content: map[string]any{
				"comm_id": "comm-z",
				"data": map[string]any{
					"op":  "data",
					"sid": "s-ghost",
					"b64": base64.StdEncoding.EncodeToString([]byte("ghost")),
					"w":   70000,
				},
			},
		})
		// 2) Send zlib-compressed chunk on active stream "s-live".
		_ = conn.WriteJSON(commJupyterMsg{
			Header: map[string]any{"msg_type": "comm_msg"},
			Content: map[string]any{
				"comm_id": "comm-z",
				"data": map[string]any{
					"op":  "data",
					"sid": "s-live",
					"z":   1,
					"b64": zB64,
					"w":   len(zB64),
				},
			},
		})
		_ = conn.WriteJSON(commJupyterMsg{
			Header: map[string]any{"msg_type": "comm_msg"},
			Content: map[string]any{
				"comm_id": "comm-z",
				"data":    map[string]any{"op": "close", "sid": "s-live"},
			},
		})

		select {
		case <-ackDone:
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientWS, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial mock WS: %v", err)
	}

	tunnel := New(Options{SessionID: "sess-z", RemotePort: 7861})
	cc := newCommConn(clientWS, "comm-z", "client-z")
	live := newCommStream(tunnel, cc, "s-live")
	tunnel.mu.Lock()
	tunnel.active = cc
	tunnel.streams["s-live"] = live
	tunnel.mu.Unlock()

	go tunnel.readLoop(cc)
	go tunnel.ackLoop(cc)
	defer tunnel.Close()

	_ = live.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(live)
	if err != nil {
		t.Fatalf("read inflated stream: %v", err)
	}
	if string(got) != wantPayload {
		t.Fatalf("inflated payload mismatch: got len=%d want len=%d", len(got), len(wantPayload))
	}

	select {
	case <-ackDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected flow-control ack total >= %d (including canceled stream), got %d", wantAckTotal, ackedTotal.Load())
	}
}
