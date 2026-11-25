package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/colabhive/colabhive/internal/events"
	"github.com/colabhive/colabhive/internal/mockcolab"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/router"
	"github.com/colabhive/colabhive/internal/runtime"
	"github.com/colabhive/colabhive/internal/store"
)

func newV11WiredTestServer(t *testing.T, mode string, adminToken string) (*Server, func()) {
	t.Helper()
	ctx := context.Background()

	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	hub := events.NewHub()
	mockSrv := mockcolab.NewServer()
	masterKey := "v11-integration-test-master-secret-32b"

	poolCfg := mockSrv.PoolConfig(masterKey)
	poolSvc := pool.NewService(st, hub, poolCfg)
	rtr := router.NewService(st, poolSvc, hub)
	rt := runtime.NewConcreteEngine(st, poolSvc, rtr, hub, runtime.Options{
		TunnelBaseURL:    mockSrv.URL,
		PublicAPIBaseURL: mockSrv.URL,
		ArtifactsDir:     t.TempDir(),
	})
	if _, err := mockcolab.SeedDemoCluster(ctx, st, poolSvc, mockSrv); err != nil {
		t.Fatalf("SeedDemoCluster: %v", err)
	}

	srv := New(st, poolSvc, rt, rtr, hub, nil, Config{
		Version:      "1.1.0",
		Mode:         mode,
		AdminToken:   adminToken,
		DataDir:      t.TempDir(),
		ArtifactsDir: t.TempDir(),
		MasterSecret: masterKey,
		DemoMode:     true,
	})

	cleanup := func() {
		_ = srv.Close()
		_ = rt.Close()
		mockSrv.Close()
		_ = st.Close()
	}
	return srv, cleanup
}

func TestV11EndToEndLocalLifecycle(t *testing.T) {
	srv, cleanup := newV11WiredTestServer(t, "local", "")
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Custom Hugging Face GGUF start + OpenAI SSE + Anthropic SSE
	startBody := `{"model_id":"https://huggingface.co/unsloth/gemma-3-27b-it-GGUF:Q4_K_M"}`
	resp, err := http.Post(ts.URL+"/api/v1/llm/start", "application/json", strings.NewReader(startBody))
	if err != nil {
		t.Fatalf("POST /api/v1/llm/start: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 on llm start, got %d: %s", resp.StatusCode, string(raw))
	}
	var llmStatus models.LLMGatewayStatus
	if err := json.NewDecoder(resp.Body).Decode(&llmStatus); err != nil {
		t.Fatalf("decode llm status: %v", err)
	}
	if !llmStatus.Running || !strings.HasPrefix(llmStatus.ActiveModelID, "hf.co/") {
		t.Fatalf("expected running hf.co model, got %+v", llmStatus)
	}

	// OpenAI SSE streaming
	chatReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"`+llmStatus.ActiveModelID+`","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	chatReq.Header.Set("Content-Type", "application/json")
	chatResp, err := http.DefaultClient.Do(chatReq)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	chatRaw, _ := io.ReadAll(chatResp.Body)
	_ = chatResp.Body.Close()
	if chatResp.StatusCode != http.StatusOK || !strings.Contains(string(chatRaw), "data: [DONE]") {
		t.Fatalf("expected OpenAI SSE stream with [DONE], got %d: %s", chatResp.StatusCode, string(chatRaw))
	}

	// Anthropic Messages SSE via x-api-key header
	anthReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages", strings.NewReader(`{"model":"`+llmStatus.ActiveModelID+`","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	anthReq.Header.Set("Content-Type", "application/json")
	anthReq.Header.Set("x-api-key", "ollama")
	anthReq.Header.Set("anthropic-version", "2023-06-01")
	anthResp, err := http.DefaultClient.Do(anthReq)
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	anthRaw, _ := io.ReadAll(anthResp.Body)
	_ = anthResp.Body.Close()
	if anthResp.StatusCode != http.StatusOK || !strings.Contains(string(anthRaw), "event: message_stop") {
		t.Fatalf("expected Anthropic SSE with message_stop, got %d: %s", anthResp.StatusCode, string(anthRaw))
	}

	// 2. Launch 1-Click AI App Store instance + verify localhost bridge & web proxy
	appResp, err := http.Post(ts.URL+"/api/v1/apps/launch", "application/json", strings.NewReader(`{"template_id":"jupyterlab","preferred_local_port":0}`))
	if err != nil {
		t.Fatalf("POST /api/v1/apps/launch: %v", err)
	}
	var inst models.AppInstance
	if err := json.NewDecoder(appResp.Body).Decode(&inst); err != nil {
		t.Fatalf("decode app instance: %v", err)
	}
	_ = appResp.Body.Close()
	if appResp.StatusCode != http.StatusOK || !inst.LocalBridgeActive || inst.LocalURL == "" {
		t.Fatalf("expected active localhost bridge, got status=%d inst=%+v", appResp.StatusCode, inst)
	}

	localResp, err := http.Get(inst.LocalURL)
	if err != nil {
		t.Fatalf("GET LocalURL %s: %v", inst.LocalURL, err)
	}
	_ = localResp.Body.Close()
	if localResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from LocalURL, got %d", localResp.StatusCode)
	}

	proxyResp, err := http.Get(ts.URL + inst.WebProxyURL)
	if err != nil {
		t.Fatalf("GET WebProxyURL %s: %v", inst.WebProxyURL, err)
	}
	_ = proxyResp.Body.Close()
	if proxyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from WebProxyURL, got %d", proxyResp.StatusCode)
	}

	// 3. Checkpoint Sync + Cross-Account Resume Relay
	syncBody, _ := json.Marshal(models.CheckpointSyncRequest{
		SessionID:   inst.SessionID,
		JobTitle:    "Wan2.2 Video Render",
		JobKind:     "app",
		ProgressPct: 78.5,
	})
	syncResp, err := http.Post(ts.URL+"/api/v1/checkpoints/sync", "application/json", bytes.NewReader(syncBody))
	if err != nil {
		t.Fatalf("POST /api/v1/checkpoints/sync: %v", err)
	}
	var ckpt models.CheckpointRecord
	_ = json.NewDecoder(syncResp.Body).Decode(&ckpt)
	_ = syncResp.Body.Close()
	if syncResp.StatusCode != http.StatusOK || ckpt.ID == "" {
		t.Fatalf("expected checkpoint sync 200, got %d %+v", syncResp.StatusCode, ckpt)
	}

	resumeResp, err := http.Post(ts.URL+"/api/v1/checkpoints/"+ckpt.ID+"/resume", "application/json", strings.NewReader(`{"strategy":"free-first"}`))
	if err != nil {
		t.Fatalf("POST /api/v1/checkpoints/resume: %v", err)
	}
	var resumed models.CheckpointRecord
	_ = json.NewDecoder(resumeResp.Body).Decode(&resumed)
	_ = resumeResp.Body.Close()
	if resumeResp.StatusCode != http.StatusOK || resumed.Status != "resumed" || resumed.RelayCount < 1 {
		t.Fatalf("expected resumed checkpoint with relay_count>=1, got %d %+v", resumeResp.StatusCode, resumed)
	}

	// 4. Verify PII-Free Savings Receipt
	cardResp, err := http.Get(ts.URL + "/api/v1/stats/savings-card")
	if err != nil {
		t.Fatalf("GET /api/v1/stats/savings-card: %v", err)
	}
	cardRaw, _ := io.ReadAll(cardResp.Body)
	_ = cardResp.Body.Close()
	if cardResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on savings-card, got %d: %s", cardResp.StatusCode, string(cardRaw))
	}
	if strings.Contains(string(cardRaw), "@gmail.com") {
		t.Fatalf("PII leaked in savings-card: %s", string(cardRaw))
	}
	var stats models.SavingsReceiptStats
	if err := json.Unmarshal(cardRaw, &stats); err != nil {
		t.Fatalf("decode savings card: %v", err)
	}
	if stats.AutoResumeRelays < 1 {
		t.Fatalf("expected auto-resume relays >= 1, got %+v", stats)
	}
}

func TestV11CloudModeAuthBoundary(t *testing.T) {
	const adminTok = "ch_adm_cloud_v11_test_token"
	srv, cleanup := newV11WiredTestServer(t, "cloud", adminTok)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Unauthenticated requests to /v1/*, /api/tags, /apps/proxy/* MUST return 401.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/models"},
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodGet, "/api/tags"},
		{http.MethodGet, "/apps/proxy/app_unauth/"},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s failed: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 unauthenticated for %s %s, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}

	// 2. Mint a scoped `ch_sk_...` key using admin token.
	keyReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/keys", strings.NewReader(`{"name":"llm-tenant"}`))
	keyReq.Header.Set("Authorization", "Bearer "+adminTok)
	keyReq.Header.Set("Content-Type", "application/json")
	keyResp, err := http.DefaultClient.Do(keyReq)
	if err != nil {
		t.Fatalf("mint scoped key: %v", err)
	}
	var keyObj models.AgentAPIKey
	_ = json.NewDecoder(keyResp.Body).Decode(&keyObj)
	_ = keyResp.Body.Close()
	if (keyResp.StatusCode != http.StatusOK && keyResp.StatusCode != http.StatusCreated) || keyObj.RawKey == "" {
		t.Fatalf("expected 200/201 scoped key, got %d %+v", keyResp.StatusCode, keyObj)
	}

	// 3. Scoped `ch_sk_...` key CAN access ONLY read-only LLM discovery routes
	//    (`/v1/models`, `/v1/models/{model}`, `/api/tags`, `/api/version`, `/api/v1/llm/catalog`, `/api/v1/llm/status`),
	//    and MUST receive 403 Forbidden on ALL LLM inference, inspection, global unload, and admin routes!
	for _, allowedReadPath := range []string{
		"/v1/models",
		"/v1/models/qwen3-8b",
		"/api/tags",
		"/api/version",
		"/api/v1/llm/catalog",
		"/api/v1/llm/status",
	} {
		mReq, _ := http.NewRequest(http.MethodGet, ts.URL+allowedReadPath, nil)
		mReq.Header.Set("x-api-key", keyObj.RawKey)
		mResp, err := http.DefaultClient.Do(mReq)
		if err != nil {
			t.Fatalf("GET %s with x-api-key: %v", allowedReadPath, err)
		}
		_ = mResp.Body.Close()
		if mResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 on read-only discovery %s with scoped key, got %d", allowedReadPath, mResp.StatusCode)
		}
	}

	for _, forbiddenTC := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1/chat/completions", `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`},
		{http.MethodPost, "/v1/responses", `{"model":"qwen3-8b","input":"hi"}`},
		{http.MethodPost, "/v1/messages", `{"model":"qwen3-8b","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`},
		{http.MethodPost, "/v1/messages/count_tokens", `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`},
		{http.MethodPost, "/api/chat", `{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}]}`},
		{http.MethodPost, "/api/generate", `{"model":"qwen3:8b","keep_alive":0}`},
		{http.MethodGet, "/api/ps", ""},
		{http.MethodPost, "/api/show", `{"model":"qwen3:8b"}`},
		{http.MethodPost, "/api/v1/llm/chat/completions", `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`},
		{http.MethodPost, "/api/v1/llm/start", `{"model_id":"qwen3-8b"}`},
		{http.MethodPost, "/api/v1/llm/stop", `{}`},
		{http.MethodPost, "/api/v1/apps/launch", `{"template_id":"jupyterlab"}`},
	} {
		aReq, _ := http.NewRequest(forbiddenTC.method, ts.URL+forbiddenTC.path, strings.NewReader(forbiddenTC.body))
		aReq.Header.Set("Authorization", "Bearer "+keyObj.RawKey)
		aReq.Header.Set("Content-Type", "application/json")
		aResp, err := http.DefaultClient.Do(aReq)
		if err != nil {
			t.Fatalf("%s %s with scoped key: %v", forbiddenTC.method, forbiddenTC.path, err)
		}
		_ = aResp.Body.Close()
		if aResp.StatusCode != http.StatusForbidden {
			t.Fatalf("SECURITY FAILURE: expected 403 Forbidden for scoped key on %s %s, got %d", forbiddenTC.method, forbiddenTC.path, aResp.StatusCode)
		}
	}

	// 3b. Verify AdminToken CAN invoke LLM inference & unload routes cleanly in cloud mode.
	admChatReq, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen3-8b","stream":false,"messages":[{"role":"user","content":"admin ping"}]}`),
	)
	admChatReq.Header.Set("Authorization", "Bearer "+adminTok)
	admChatReq.Header.Set("Content-Type", "application/json")
	admChatResp, err := http.DefaultClient.Do(admChatReq)
	if err != nil {
		t.Fatalf("admin POST /v1/chat/completions: %v", err)
	}
	_ = admChatResp.Body.Close()
	if admChatResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on admin /v1/chat/completions, got %d", admChatResp.StatusCode)
	}

	admUnloadReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/generate", strings.NewReader(`{"model":"qwen3:8b","keep_alive":0}`))
	admUnloadReq.Header.Set("Authorization", "Bearer "+adminTok)
	admUnloadReq.Header.Set("Content-Type", "application/json")
	admUnloadResp, err := http.DefaultClient.Do(admUnloadReq)
	if err != nil {
		t.Fatalf("admin POST /api/generate unload: %v", err)
	}
	_ = admUnloadResp.Body.Close()
	if admUnloadResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on admin /api/generate unload, got %d", admUnloadResp.StatusCode)
	}

	// 4. Cloud-mode App launch MUST lock off host `127.0.0.1` port binding.
	lReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/apps/launch", strings.NewReader(`{"template_id":"jupyterlab","preferred_local_port":8188}`))
	lReq.Header.Set("Authorization", "Bearer "+adminTok)
	lReq.Header.Set("Content-Type", "application/json")
	lResp, err := http.DefaultClient.Do(lReq)
	if err != nil {
		t.Fatalf("cloud app launch: %v", err)
	}
	var cloudInst models.AppInstance
	_ = json.NewDecoder(lResp.Body).Decode(&cloudInst)
	_ = lResp.Body.Close()
	if lResp.StatusCode != http.StatusOK || cloudInst.LocalBridgeActive || cloudInst.LocalPort != 0 {
		t.Fatalf("expected cloud mode to disable host port binding, got %d %+v", lResp.StatusCode, cloudInst)
	}
}

func TestKeylessLoopbackExactPortIsolationAndWSCheckOrigin(t *testing.T) {
	// 1. Direct table-driven tests for isSameOriginLoopback
	cases := []struct {
		origin  string
		reqHost string
		want    bool
	}{
		{"http://127.0.0.1:7860", "127.0.0.1:7860", true},
		{"http://localhost:7860", "127.0.0.1:7860", true},
		{"http://[::1]:7860", "127.0.0.1:7860", true},
		{"http://127.0.0.1:80", "127.0.0.1", true},
		{"http://localhost", "127.0.0.1", true},
		// Cross-port loopback Studio origins MUST be rejected!
		{"http://127.0.0.1:8188", "127.0.0.1:7860", false},
		{"http://localhost:8188", "127.0.0.1:7860", false},
		{"http://localhost:5173", "127.0.0.1:7860", false},
		{"http://127.0.0.1", "127.0.0.1:7860", false},
		{"https://127.0.0.1", "127.0.0.1", false},
		// Non-loopback or malformed origins MUST be rejected!
		{"https://evil.example:7860", "127.0.0.1:7860", false},
		{"null", "127.0.0.1:7860", false},
		{"", "127.0.0.1:7860", false},
		{"http://127.0.0.1:7860", "", false},
		{"http://user@127.0.0.1:7860", "127.0.0.1:7860", false},
		{"http://127.0.0.1:7860/evil", "127.0.0.1:7860", false},
	}
	for _, tc := range cases {
		if got := isSameOriginLoopback(tc.origin, tc.reqHost); got != tc.want {
			t.Errorf("isSameOriginLoopback(%q, %q) = %v, want %v", tc.origin, tc.reqHost, got, tc.want)
		}
	}

	// 2. End-to-end keyless local daemon HTTP + CORS + Sec-Fetch-Site + WebSocket tests
	srv, cleanup := newV11WiredTestServer(t, "local", "")
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	parsedTS, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse ts.URL: %v", err)
	}
	samePortLocalhost := "http://localhost:" + parsedTS.Port()
	samePortLoopback := "http://127.0.0.1:" + parsedTS.Port()

	// Cross-port local Studio (127.0.0.1:8188) MUST receive 403 Forbidden and NO Access-Control-Allow-Origin!
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/health", ""},
		{http.MethodOptions, "/api/v1/accounts", ""},
		{http.MethodPost, "/api/v1/llm/stop", `{}`},
		{http.MethodPost, "/api/v1/pool/export", `{}`},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Origin", "http://127.0.0.1:8188")
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("%s %s cross-port request: %v", tc.method, tc.path, doErr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("SECURITY FAILURE: expected 403 for cross-port Studio Origin on %s %s, got %d", tc.method, tc.path, resp.StatusCode)
		}
		if acao := resp.Header.Get("Access-Control-Allow-Origin"); acao != "" {
			t.Fatalf("SECURITY FAILURE: Access-Control-Allow-Origin must be empty on rejected cross-port request, got %q", acao)
		}
	}

	// Exact same-port loopback origins (`127.0.0.1:<port>` and `localhost:<port>`) ARE allowed and reflected cleanly.
	for _, allowedOrigin := range []string{samePortLocalhost, samePortLoopback} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/health", nil)
		req.Header.Set("Origin", allowedOrigin)
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("GET /api/v1/health with %s: %v", allowedOrigin, doErr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Access-Control-Allow-Origin") != allowedOrigin {
			t.Fatalf("expected 200 and ACAO=%q, got status=%d ACAO=%q", allowedOrigin, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
		}
	}

	// Sec-Fetch-Site: cross-site on state-changing routes in keyless mode MUST be blocked with 403 even without Origin!
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, ts.URL+"/api/v1/llm/stop", strings.NewReader(`{}`))
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("%s with Sec-Fetch-Site cross-site: %v", method, doErr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 for %s with Sec-Fetch-Site: cross-site, got %d", method, resp.StatusCode)
		}
	}
	// GET navigation with Sec-Fetch-Site: cross-site (no Origin) remains allowed.
	getNavReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/health", nil)
	getNavReq.Header.Set("Sec-Fetch-Site", "cross-site")
	getNavResp, err := http.DefaultClient.Do(getNavReq)
	if err != nil {
		t.Fatalf("GET with Sec-Fetch-Site cross-site: %v", err)
	}
	_ = getNavResp.Body.Close()
	if getNavResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for GET with Sec-Fetch-Site: cross-site, got %d", getNavResp.StatusCode)
	}

	// WebSocket `/ws/events`: cross-port Studio Origin `http://127.0.0.1:8188` MUST be rejected with 403!
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"
	crossPortWSHdr := http.Header{}
	crossPortWSHdr.Set("Origin", "http://127.0.0.1:8188")
	_, wsCrossResp, wsCrossErr := websocket.DefaultDialer.Dial(wsURL, crossPortWSHdr)
	if wsCrossErr == nil || wsCrossResp == nil || wsCrossResp.StatusCode != http.StatusForbidden {
		t.Fatalf("SECURITY FAILURE: expected 403 for cross-port Studio WS Origin, got err=%v resp=%+v", wsCrossErr, wsCrossResp)
	}

	// Verify handleWSEvents's own CheckOrigin rejects cross-port loopback directly (defense in depth without withCORS).
	directWSSrv := httptest.NewServer(http.HandlerFunc(srv.handleWSEvents))
	defer directWSSrv.Close()
	directWSURL := "ws" + strings.TrimPrefix(directWSSrv.URL, "http")
	_, directCrossResp, directCrossErr := websocket.DefaultDialer.Dial(directWSURL, crossPortWSHdr)
	if directCrossErr == nil || directCrossResp == nil || directCrossResp.StatusCode != http.StatusForbidden {
		t.Fatalf("SECURITY FAILURE: direct handleWSEvents CheckOrigin must reject cross-port Origin with 403, got err=%v resp=%+v", directCrossErr, directCrossResp)
	}

	// Same-port loopback Origin (`http://localhost:<port>`) CAN open `/ws/events` cleanly!
	samePortWSHdr := http.Header{}
	samePortWSHdr.Set("Origin", samePortLocalhost)
	wsConn, wsSameResp, wsSameErr := websocket.DefaultDialer.Dial(wsURL, samePortWSHdr)
	if wsSameErr != nil || wsSameResp == nil || wsSameResp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected same-port loopback WS Origin %q to upgrade, got err=%v resp=%+v", samePortLocalhost, wsSameErr, wsSameResp)
	}
	_ = wsConn.Close()

	// 3. Local desktop AI clients (Cherry Studio Electron, SillyTavern, Open WebUI, Cursor)
	// MUST be allowed on standard local LLM wire routes (`/v1/*`) even with Sec-Fetch-Site: cross-site,
	// Origin: null / file:// / localhost:<other>, and placeholder API keys, while public internet origins stay 403!
	for _, tc := range []struct {
		name         string
		origin       string
		secFetchSite string
		wantBlocked  bool
	}{
		{"cherry-studio-no-origin-cross-site", "", "cross-site", false},
		{"cherry-studio-null-origin", "null", "cross-site", false},
		{"cherry-studio-file-origin", "file://", "cross-site", false},
		{"cherry-studio-app-origin", "app://cherry-studio", "cross-site", false},
		{"sillytavern-loopback-other-port", "http://127.0.0.1:8000", "same-site", false},
		{"openwebui-localhost-3000", "http://localhost:3000", "same-site", false},
		{"public-website-blocked", "https://evil.example.com", "cross-site", true},
	} {
		req, _ := http.NewRequest(
			http.MethodPost,
			ts.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"qwen3.5-9b-heretic","messages":[{"role":"user","content":"hi"}]}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer colabhive-local")
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.secFetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", tc.secFetchSite)
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("%s: %v", tc.name, doErr)
		}
		_ = resp.Body.Close()
		if tc.wantBlocked {
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("SECURITY FAILURE (%s): expected 403 on untrusted origin, got %d", tc.name, resp.StatusCode)
			}
		} else if resp.StatusCode == http.StatusForbidden {
			t.Fatalf("REGRESSION (%s): local desktop AI client must NOT get 403 on /v1/chat/completions, got 403", tc.name)
		}
	}
}
