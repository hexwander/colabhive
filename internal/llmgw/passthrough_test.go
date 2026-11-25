package llmgw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

func TestPrepareNativeOllamaPayloadPreservesToolsAndMapsThinkFlags(t *testing.T) {
	tag := "hf.co/mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF:Q4_K_M"

	// 1. OpenAI Chat preserves tools & maps think=false -> reasoning_effort="none"
	rawChat := []byte(`{"model":"qwen3.5-9b-heretic","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_weather"}}],"think":false,"stream":true}`)
	outChat, wantStream, err := prepareNativeOllamaPayload("/v1/chat/completions", rawChat, tag)
	if err != nil || !wantStream {
		t.Fatalf("prepareNativeOllamaPayload chat: err=%v stream=%v", err, wantStream)
	}
	var parsedChat map[string]any
	_ = json.Unmarshal(outChat, &parsedChat)
	if parsedChat["model"] != tag || parsedChat["reasoning_effort"] != "none" {
		t.Fatalf("unexpected chat payload: %s", string(outChat))
	}
	if _, hasThink := parsedChat["think"]; hasThink {
		t.Fatalf("expected non-standard think key removed on /v1/chat/completions")
	}
	if _, hasTools := parsedChat["tools"]; !hasTools {
		t.Fatalf("expected tools preserved intact on pass-through: %s", string(outChat))
	}

	// 2. OpenAI Responses maps think=false -> reasoning={"effort":"none"}
	outResp, _, err := prepareNativeOllamaPayload("/v1/responses", []byte(`{"model":"qwen3.5-9b-heretic","input":"hi","think":false}`), tag)
	if err != nil {
		t.Fatalf("prepareNativeOllamaPayload responses: %v", err)
	}
	if !strings.Contains(string(outResp), `"effort":"none"`) {
		t.Fatalf("expected reasoning.effort=none on /v1/responses, got %s", string(outResp))
	}

	// 3. Anthropic Messages maps think=false -> thinking={"type":"disabled"}
	outAnth, _, err := prepareNativeOllamaPayload("/v1/messages", []byte(`{"model":"qwen3.5-9b-heretic","messages":[{"role":"user","content":"hi"}],"think":false}`), tag)
	if err != nil {
		t.Fatalf("prepareNativeOllamaPayload anthropic: %v", err)
	}
	if !strings.Contains(string(outAnth), `"type":"disabled"`) {
		t.Fatalf("expected thinking.type=disabled on /v1/messages, got %s", string(outAnth))
	}
}

func TestNativeSSETransformerMirrorsChatReasoningAndShimsResponsesLifecycle(t *testing.T) {
	// 1. /v1/chat/completions mirrors Ollama `delta.reasoning` -> `delta.reasoning_content` & rewrites model
	var chatOut bytes.Buffer
	chatTr := newNativeSSETransformer("/v1/chat/completions", "qwen3.5-9b-heretic", func(b []byte) error {
		chatOut.Write(b)
		return nil
	})
	rawOllamaChatSSE := "data: {\"model\":\"hf.co/example:Q4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning\":\"Step 1\"}}]}\n\n" +
		"data: {\"model\":\"hf.co/example:Q4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello!\"}}]}\n\n" +
		"data: [DONE]\n\n"
	if err := chatTr.Write([]byte(rawOllamaChatSSE)); err != nil {
		t.Fatalf("chatTr.Write: %v", err)
	}
	_ = chatTr.Flush()
	gotChat := chatOut.String()
	if !strings.Contains(gotChat, `"reasoning_content":"Step 1"`) || !strings.Contains(gotChat, `"reasoning":"Step 1"`) {
		t.Fatalf("expected both reasoning and reasoning_content mirrored, got:\n%s", gotChat)
	}
	if !strings.Contains(gotChat, `"model":"qwen3.5-9b-heretic"`) {
		t.Fatalf("expected model rewritten to client ID, got:\n%s", gotChat)
	}

	// 2. /v1/responses injects missing reasoning_summary_part.added & .done for Ollama 0.40 -> Cherry Studio!
	var respOut bytes.Buffer
	respTr := newNativeSSETransformer("/v1/responses", "qwen3.5-9b-heretic", func(b []byte) error {
		respOut.Write(b)
		return nil
	})
	rawOllamaRespSSE := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","model":"hf.co/example:Q4"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_123","type":"reasoning","summary":[]}}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","sequence_number":2,"item_id":"rs_123","output_index":0,"summary_index":0,"delta":"Thinking..."}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"rs_123","type":"reasoning","status":"completed"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"Hi!"}`,
		"",
	}, "\n") + "\n"
	if err := respTr.Write([]byte(rawOllamaRespSSE)); err != nil {
		t.Fatalf("respTr.Write: %v", err)
	}
	_ = respTr.Flush()
	gotResp := respOut.String()
	order := []string{
		"event: response.output_item.added",
		"event: response.reasoning_summary_part.added",
		"event: response.reasoning_summary_text.delta",
		"event: response.reasoning_summary_part.done",
		"event: response.output_item.done",
		"event: response.output_text.delta",
	}
	prev := -1
	for _, tok := range order {
		pos := strings.Index(gotResp, tok)
		if pos < 0 || pos <= prev {
			t.Fatalf("expected %q in order after %d in shimmed Responses SSE, got:\n%s", tok, prev, gotResp)
		}
		prev = pos
	}
	if !strings.Contains(gotResp, `"model":"qwen3.5-9b-heretic"`) {
		t.Fatalf("expected response.model rewritten to client ID, got:\n%s", gotResp)
	}
}

func TestProxyNativeOllamaV1StreamsAndFallsBackOn404(t *testing.T) {
	// 1. Live streaming pass-through succeeds with shimmed reasoning_content
	rt := &stubLLMExecRuntime{
		execFn: func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
			if req.OnStdout != nil {
				req.OnStdout(`COLABHIVE_HTTP_META:{"status":200,"content_type":"text/event-stream"}` + "\n")
				chunk := "data: {\"model\":\"hf.co/example:Q4\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"Plan\",\"content\":\"\"}}]}\n\n" +
					"data: {\"model\":\"hf.co/example:Q4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done\"}}]}\n\n" +
					"data: [DONE]\n\n"
				req.OnStdout("COLABHIVE_HTTP_CHUNK:" + base64.StdEncoding.EncodeToString([]byte(chunk)) + "\n")
				req.OnStdout(`COLABHIVE_HTTP_END:{"ok":true}` + "\n")
			}
			return &models.KernelExecResult{Status: "ok"}, nil
		},
	}
	svc := NewService(nil, rt, nil, nil, Config{DemoMode: false})
	t.Cleanup(func() { _ = svc.Close() })
	spec, _ := svc.ResolveModel(DefaultModelID)
	svc.stateMu.Lock()
	svc.activeSpec = spec
	svc.liveOllama = true
	svc.status.Running = true
	svc.status.State = models.LLMStateReady
	svc.status.SessionID = "sess_native_1"
	svc.status.ActiveModelID = spec.ID
	svc.stateMu.Unlock()

	rec := httptest.NewRecorder()
	handled, err := svc.ProxyNativeOllamaV1(
		context.Background(),
		"/v1/chat/completions",
		spec.ID,
		[]byte(`{"model":"`+spec.ID+`","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		http.Header{},
		rec,
	)
	if err != nil || !handled {
		t.Fatalf("ProxyNativeOllamaV1 expected handled=true err=nil, got handled=%v err=%v", handled, err)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reasoning_content":"Plan"`) || !strings.Contains(rec.Body.String(), `"content":"Done"`) {
		t.Fatalf("unexpected pass-through response: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. Remote 404 (older Ollama without /v1/responses) returns errNativePassThroughUnsupported without writing headers
	rt404 := &stubLLMExecRuntime{
		execFn: func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
			if req.OnStdout != nil {
				req.OnStdout(`COLABHIVE_HTTP_META:{"status":404,"content_type":"text/plain"}` + "\n")
			}
			return &models.KernelExecResult{Status: "ok"}, nil
		},
	}
	svc.rt = rt404
	rec404 := httptest.NewRecorder()
	handled404, err404 := svc.ProxyNativeOllamaV1(
		context.Background(),
		"/v1/responses",
		spec.ID,
		[]byte(`{"model":"`+spec.ID+`","input":"hi","stream":true}`),
		http.Header{},
		rec404,
	)
	if handled404 || !errors.Is(err404, errNativePassThroughUnsupported) {
		t.Fatalf("expected fallback errNativePassThroughUnsupported on 404, got handled=%v err=%v", handled404, err404)
	}
	if rec404.Body.Len() != 0 {
		t.Fatalf("expected zero bytes written on 404 fallback, got %q", rec404.Body.String())
	}
}

func TestSafeOllamaConcurrencyByGPUTier(t *testing.T) {
	cases := []struct {
		name        string
		gpu         models.AcceleratorType
		spec        models.LLMModelSpec
		wantPar     int
		wantCtx     int
	}{
		{"T4 9B", models.AcceleratorT4, models.LLMModelSpec{MinVRAMGB: 8, ContextLength: 32768}, 2, 8192},
		{"T4 12B", models.AcceleratorT4, models.LLMModelSpec{MinVRAMGB: 10, ContextLength: 65536}, 2, 8192},
		{"T4 tight 14B+", models.AcceleratorT4, models.LLMModelSpec{MinVRAMGB: 14, ContextLength: 32768}, 1, 8192},
		{"L4 9B", models.AcceleratorL4, models.LLMModelSpec{MinVRAMGB: 8, ContextLength: 32768}, 3, 16384},
		{"L4 27B", models.AcceleratorL4, models.LLMModelSpec{MinVRAMGB: 20, ContextLength: 65536}, 2, 8192},
		{"A100 9B", models.AcceleratorA100, models.LLMModelSpec{MinVRAMGB: 8, ContextLength: 65536}, 4, 16384},
		{"A100 31B", models.AcceleratorA100, models.LLMModelSpec{MinVRAMGB: 24, ContextLength: 131072}, 2, 32768},
		{"G4 27B", models.AcceleratorG4, models.LLMModelSpec{MinVRAMGB: 20, ContextLength: 65536}, 4, 32768},
		{"G4 72B", models.AcceleratorG4, models.LLMModelSpec{MinVRAMGB: 72, ContextLength: 131072}, 4, 16384},
		{"G4 96B MoE", models.AcceleratorG4, models.LLMModelSpec{MinVRAMGB: 96, ContextLength: 131072}, 2, 16384},
	}
	for _, tc := range cases {
		gotPar, gotCtx := safeOllamaConcurrency(tc.gpu, tc.spec)
		if gotPar != tc.wantPar || gotCtx != tc.wantCtx {
			t.Errorf("%s: got (parallel=%d, ctx=%d), want (parallel=%d, ctx=%d)", tc.name, gotPar, gotCtx, tc.wantPar, tc.wantCtx)
		}
	}
}

func TestBuildOllamaBootstrapIncludesGPUNumParallel(t *testing.T) {
	script := buildOllamaBootstrapPythonWithID("qwen3:8b", "boot_test_1", 16384, 3)
	if !strings.Contains(script, `__ch_num_parallel = 3`) {
		t.Fatalf("expected __ch_num_parallel = 3 in bootstrap script")
	}
	if !strings.Contains(script, `OLLAMA_NUM_PARALLEL`) || !strings.Contains(script, `str(__ch_num_parallel)`) {
		t.Fatalf("expected OLLAMA_NUM_PARALLEL wired to __ch_num_parallel in bootstrap script")
	}
}

type tunnelTestRuntime struct {
	models.RuntimeEngine
	sess      *models.RuntimeSession
	execCalls atomic.Int32
	stopped   atomic.Bool
}

func (r *tunnelTestRuntime) GetSession(_ context.Context, id string) (*models.RuntimeSession, error) {
	if r.sess != nil && r.sess.ID == id {
		cp := *r.sess
		return &cp, nil
	}
	return nil, models.ErrSessionNotFound
}

func (r *tunnelTestRuntime) StopSession(_ context.Context, _ string) error {
	r.stopped.Store(true)
	return nil
}

func (r *tunnelTestRuntime) ExecuteCode(_ context.Context, _ models.KernelExecRequest) (*models.KernelExecResult, error) {
	r.execCalls.Add(1)
	return &models.KernelExecResult{Status: "ok"}, nil
}

type notifyingResponseRecorder struct {
	*httptest.ResponseRecorder
	onWrite func(p []byte)
}

func (n *notifyingResponseRecorder) Write(p []byte) (int, error) {
	nw, err := n.ResponseRecorder.Write(p)
	if nw > 0 && n.onWrite != nil {
		n.onWrite(p[:nw])
	}
	return nw, err
}

func (n *notifyingResponseRecorder) Flush() {
	n.ResponseRecorder.Flush()
}

func TestProxyNativeOllamaV1_ConcurrentParallelStreamingAndIndependentCancelOverCommTunnel(t *testing.T) {
	// 1. Mock Ollama HTTP server verifying >=2 concurrent streaming requests enter simultaneously
	//    and canceling req1 immediately tears down ONLY req1's TCP connection while req2 completes!
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	bothInside := make(chan struct{})
	var bothOnce sync.Once
	req1UpstreamCanceled := make(chan struct{})
	var cancel1Once sync.Once

	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)

		switch r.URL.Path {
		case "/v1/chat/completions":
			fmt.Fprint(w, "data: {\"model\":\"hf.co/example:Q4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning\":\"Plan A\"}}]}\n\n")
			if fl != nil {
				fl.Flush()
			}
			// Hold open until both requests are simultaneously in flight, then wait for independent cancel.
			select {
			case <-r.Context().Done():
				cancel1Once.Do(func() { close(req1UpstreamCanceled) })
				return
			case <-time.After(5 * time.Second):
				return
			}

		case "/v1/responses":
			// Wait until BOTH requests are simultaneously inside Ollama!
			select {
			case <-bothInside:
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
				return
			}
			// Emit Ollama 0.40 Responses SSE (deliberately omitting reasoning_summary_part.added to verify shim too!)
			fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_p2\",\"model\":\"hf.co/example:Q4\"}}\n\n")
			fmt.Fprint(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"rs_p2\",\"type\":\"reasoning\",\"summary\":[]}}\n\n")
			fmt.Fprint(w, "event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"sequence_number\":2,\"item_id\":\"rs_p2\",\"output_index\":0,\"summary_index\":0,\"delta\":\"Parallel think\"}\n\n")
			if fl != nil {
				fl.Flush()
			}
			// Wait for req1 to be canceled independently, then finish req2 cleanly!
			select {
			case <-req1UpstreamCanceled:
			case <-time.After(5 * time.Second):
			}
			fmt.Fprint(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":0,\"item\":{\"id\":\"rs_p2\",\"type\":\"reasoning\",\"status\":\"completed\"}}\n\n")
			fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":4,\"item_id\":\"msg_p2\",\"output_index\":1,\"content_index\":0,\"delta\":\"Parallel answer!\"}\n\n")
			fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":5,\"response\":{\"id\":\"resp_p2\",\"model\":\"hf.co/example:Q4\",\"usage\":{\"output_tokens\":12}}}\n\n")
			if fl != nil {
				fl.Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollamaSrv.Close()

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(ollamaSrv.URL, "http://"))
	if err != nil {
		t.Fatalf("parse mock ollama port: %v", err)
	}
	var ollamaPort int
	_, _ = fmt.Sscanf(portStr, "%d", &ollamaPort)

	// 2. Mock Jupyter server providing /api/kernels + /api/kernels/{id}/channels Comm TCP tunnel.
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	jupyterSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/kernels" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"k-llm-tunnel"}`))
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
				var m struct {
					Header  map[string]any `json:"header"`
					Content map[string]any `json:"content"`
				}
				if json.Unmarshal(raw, &m) != nil {
					continue
				}
				mType, _ := m.Header["msg_type"].(string)
				switch mType {
				case "execute_request":
					sendJSON(map[string]any{
						"channel":       "shell",
						"header":        map[string]any{"msg_type": "execute_reply"},
						"parent_header": map[string]any{"msg_id": m.Header["msg_id"]},
						"metadata":      map[string]any{},
						"content":       map[string]any{"status": "ok"},
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
							sendJSON(map[string]any{
								"header":  map[string]any{"msg_type": "comm_msg"},
								"content": map[string]any{"comm_id": commID, "data": map[string]any{"op": "err", "sid": sid, "err": dErr.Error()}},
							})
							continue
						}
						socksMu.Lock()
						socks[sid] = conn
						socksMu.Unlock()
						sendJSON(map[string]any{
							"header":  map[string]any{"msg_type": "comm_msg"},
							"content": map[string]any{"comm_id": commID, "data": map[string]any{"op": "opened", "sid": sid}},
						})
						go func(streamID string, c net.Conn) {
							buf := make([]byte, 32*1024)
							for {
								n, nErr := c.Read(buf)
								if n > 0 {
									sendJSON(map[string]any{
										"header": map[string]any{"msg_type": "comm_msg"},
										"content": map[string]any{
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
									sendJSON(map[string]any{
										"header":  map[string]any{"msg_type": "comm_msg"},
										"content": map[string]any{"comm_id": commID, "data": map[string]any{"op": "close", "sid": streamID}},
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

	// 3. Configure live LLM Gateway Service pointing at our mock Jupyter Comm tunnel & mock Ollama port.
	rt := &tunnelTestRuntime{
		sess: &models.RuntimeSession{
			ID:          "sess_parallel_llm",
			Status:      models.SessionIdle,
			Accelerator: models.AcceleratorT4,
			ProxyURL:    jupyterSrv.URL,
			ProxyToken:  "valid-test-token",
		},
	}
	svc := NewService(nil, rt, nil, nil, Config{DemoMode: false})
	svc.ollamaPort = ollamaPort
	t.Cleanup(func() { _ = svc.Close() })

	spec, _ := svc.ResolveModel(DefaultModelID)
	svc.stateMu.Lock()
	svc.activeSpec = spec
	svc.liveOllama = true
	svc.status.Running = true
	svc.status.State = models.LLMStateReady
	svc.status.SessionID = rt.sess.ID
	svc.status.ActiveModelID = spec.ID
	svc.status.AssignedGPU = models.AcceleratorT4
	svc.stateMu.Unlock()

	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	var wg sync.WaitGroup
	wg.Add(2)

	// Concurrent Request 1: /v1/chat/completions (cancels mid-stream AFTER first shimmed chunk arrives & both are inside Ollama).
	req1SawChunk := make(chan struct{})
	var req1ChunkOnce sync.Once
	rec1Base := httptest.NewRecorder()
	rec1 := &notifyingResponseRecorder{
		ResponseRecorder: rec1Base,
		onWrite: func(p []byte) {
			if strings.Contains(string(p), `"reasoning_content":"Plan A"`) {
				req1ChunkOnce.Do(func() { close(req1SawChunk) })
			}
		},
	}
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+spec.ID+`","messages":[{"role":"user","content":"topic 1"}],"stream":true}`)).WithContext(ctx1)
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec1, req)
	}()

	// Concurrent Request 2: /v1/responses (must run simultaneously with req1 and finish cleanly after req1 cancels!).
	rec2 := httptest.NewRecorder()
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+spec.ID+`","input":"topic 2","stream":true}`))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec2, req)
	}()

	// Wait until BOTH requests are simultaneously inside mock Ollama AND req1 has received its first shimmed SSE chunk, then cancel ONLY req1!
	select {
	case <-bothInside:
	case <-time.After(5 * time.Second):
		cancel1()
		t.Fatalf("timed out waiting for 2 concurrent LLM requests to enter Ollama simultaneously (maxInFlight=%d)", maxInFlight.Load())
	}
	select {
	case <-req1SawChunk:
		cancel1()
	case <-time.After(5 * time.Second):
		cancel1()
		t.Fatalf("timed out waiting for req1 first shimmed chunk, got:\n%s", rec1Base.Body.String())
	}

	wg.Wait()

	if maxInFlight.Load() < 2 {
		t.Fatalf("expected >=2 simultaneous parallel LLM requests over Comm tunnel, got max=%d", maxInFlight.Load())
	}
	if rt.execCalls.Load() != 0 {
		t.Fatalf("expected zero ExecuteCode calls when Comm TCP tunnel succeeds, got %d", rt.execCalls.Load())
	}
	if !strings.Contains(rec1.Body.String(), `"reasoning_content":"Plan A"`) {
		t.Fatalf("expected req1 to have streamed shimmed reasoning_content before cancel, got:\n%s", rec1.Body.String())
	}
	body2 := rec2.Body.String()
	if rec2.Code != http.StatusOK ||
		!strings.Contains(body2, "event: response.reasoning_summary_part.added") ||
		!strings.Contains(body2, "Parallel think") ||
		!strings.Contains(body2, "event: response.reasoning_summary_part.done") ||
		!strings.Contains(body2, "Parallel answer!") ||
		!strings.Contains(body2, `"model":"`+spec.ID+`"`) {
		t.Fatalf("expected req2 to complete cleanly with full shimmed Responses SSE after req1 cancel, got code=%d body:\n%s", rec2.Code, body2)
	}

	// Verify Stop() tears down the session's Comm tunnel cleanly.
	if _, stopErr := svc.Stop(context.Background()); stopErr != nil {
		t.Fatalf("Stop failed: %v", stopErr)
	}
	svc.tunnelMu.Lock()
	remainingTunnels := len(svc.ollamaTunnels)
	svc.tunnelMu.Unlock()
	if remainingTunnels != 0 {
		t.Fatalf("expected all session Comm tunnels closed on Stop(), got %d remaining", remainingTunnels)
	}
}
