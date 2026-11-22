package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/router"
	"github.com/colabhive/colabhive/internal/runtime"
)

type mcpTestStore struct {
	mu          sync.RWMutex
	accounts    map[string]*models.Account
	sessions    map[string]*models.RuntimeSession
	experiments map[string]*models.Experiment
	keys        map[string]*models.AgentAPIKey
	traces      []models.ToolTraceEvent
	policy      models.RouterPolicy
}

func newMCPTestStore() *mcpTestStore {
	return &mcpTestStore{
		accounts:    make(map[string]*models.Account),
		sessions:    make(map[string]*models.RuntimeSession),
		experiments: make(map[string]*models.Experiment),
		keys:        make(map[string]*models.AgentAPIKey),
		policy:      models.DefaultRouterPolicy(),
	}
}

func (s *mcpTestStore) SaveAccount(_ context.Context, a *models.Account) error {
	s.mu.Lock()
	cp := *a
	s.accounts[a.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) GetAccount(_ context.Context, id string) (*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.accounts[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}
func (s *mcpTestStore) GetAccountByEmail(_ context.Context, _ string) (*models.Account, error) {
	return nil, nil
}
func (s *mcpTestStore) GetAccountByReceiptHash(_ context.Context, _ string) (*models.Account, error) {
	return nil, nil
}
func (s *mcpTestStore) ListAccounts(_ context.Context) ([]*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}
func (s *mcpTestStore) DeleteAccount(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.accounts, id)
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) SaveProxy(_ context.Context, _ *models.ProxyConfig) error { return nil }
func (s *mcpTestStore) GetProxy(_ context.Context, _ string) (*models.ProxyConfig, error) {
	return nil, nil
}
func (s *mcpTestStore) ListProxies(_ context.Context) ([]*models.ProxyConfig, error) { return nil, nil }
func (s *mcpTestStore) DeleteProxy(_ context.Context, _ string) error                { return nil }
func (s *mcpTestStore) SaveInvite(_ context.Context, _ *models.ContributorInvite) error {
	return nil
}
func (s *mcpTestStore) GetInvite(_ context.Context, _ string) (*models.ContributorInvite, error) {
	return nil, nil
}
func (s *mcpTestStore) ListInvites(_ context.Context) ([]*models.ContributorInvite, error) {
	return nil, nil
}
func (s *mcpTestStore) DeleteInvite(_ context.Context, _ string) error { return nil }

func (s *mcpTestStore) SaveSession(_ context.Context, sess *models.RuntimeSession) error {
	s.mu.Lock()
	cp := *sess
	cp.Cells = append([]models.NotebookCell(nil), sess.Cells...)
	s.sessions[sess.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) GetSession(_ context.Context, id string) (*models.RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.sessions[id]; ok {
		cp := *v
		cp.Cells = append([]models.NotebookCell(nil), v.Cells...)
		return &cp, nil
	}
	return nil, nil
}
func (s *mcpTestStore) GetSessionByName(_ context.Context, name string) (*models.RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.sessions {
		if v.Name == name {
			cp := *v
			cp.Cells = append([]models.NotebookCell(nil), v.Cells...)
			return &cp, nil
		}
	}
	return nil, nil
}
func (s *mcpTestStore) ListSessions(_ context.Context) ([]*models.RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.RuntimeSession, 0, len(s.sessions))
	for _, v := range s.sessions {
		cp := *v
		cp.Cells = append([]models.NotebookCell(nil), v.Cells...)
		out = append(out, &cp)
	}
	return out, nil
}
func (s *mcpTestStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) SaveExperiment(_ context.Context, exp *models.Experiment) error {
	s.mu.Lock()
	cp := *exp
	cp.Shards = append([]models.ExperimentShard(nil), exp.Shards...)
	s.experiments[exp.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) GetExperiment(_ context.Context, id string) (*models.Experiment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.experiments[id]; ok {
		cp := *v
		cp.Shards = append([]models.ExperimentShard(nil), v.Shards...)
		return &cp, nil
	}
	return nil, nil
}
func (s *mcpTestStore) ListExperiments(_ context.Context) ([]*models.Experiment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Experiment, 0, len(s.experiments))
	for _, v := range s.experiments {
		cp := *v
		out = append(out, &cp)
	}
	return out, nil
}
func (s *mcpTestStore) DeleteExperiment(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.experiments, id)
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) SaveAPIKey(_ context.Context, k *models.AgentAPIKey) error {
	s.mu.Lock()
	cp := *k
	s.keys[k.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) GetAPIKey(_ context.Context, id string) (*models.AgentAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if k, ok := s.keys[id]; ok {
		cp := *k
		return &cp, nil
	}
	return nil, nil
}
func (s *mcpTestStore) GetAPIKeyByHash(_ context.Context, hash string) (*models.AgentAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.keys {
		if k.KeyHash == hash {
			cp := *k
			return &cp, nil
		}
	}
	return nil, nil
}
func (s *mcpTestStore) ListAPIKeys(_ context.Context) ([]*models.AgentAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.AgentAPIKey
	for _, k := range s.keys {
		cp := *k
		out = append(out, &cp)
	}
	return out, nil
}
func (s *mcpTestStore) DeleteAPIKey(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.keys, id)
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) GetRouterPolicy(_ context.Context) (*models.RouterPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.policy
	return &cp, nil
}
func (s *mcpTestStore) SaveRouterPolicy(_ context.Context, p *models.RouterPolicy) error {
	s.mu.Lock()
	s.policy = *p
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) AppendToolTrace(_ context.Context, tr *models.ToolTraceEvent) error {
	s.mu.Lock()
	s.traces = append([]models.ToolTraceEvent{*tr}, s.traces...)
	s.mu.Unlock()
	return nil
}
func (s *mcpTestStore) ListToolTraces(_ context.Context, limit int) ([]models.ToolTraceEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit > len(s.traces) {
		limit = len(s.traces)
	}
	out := make([]models.ToolTraceEvent, limit)
	copy(out, s.traces[:limit])
	return out, nil
}
func (s *mcpTestStore) RecordBurnSample(_ context.Context, _ models.AcceleratorType, _ models.MachineShape, _ float64) error {
	return nil
}
func (s *mcpTestStore) GetCalibratedBurnRate(_ context.Context, _ models.AcceleratorType, _ models.MachineShape) (float64, bool, error) {
	return 0, false, nil
}
func (s *mcpTestStore) AddSavedWaste(_ context.Context, _ float64, _ bool) error { return nil }
func (s *mcpTestStore) GetSavedWasteStats(_ context.Context) (models.SavedWasteStats, error) {
	return models.SavedWasteStats{}, nil
}
func (s *mcpTestStore) Close() error { return nil }

func TestMCPStaticToolsStreamableHTTPStickyAndStdio(t *testing.T) {
	ctx := context.Background()
	st := newMCPTestStore()
	_ = st.SaveAccount(ctx, &models.Account{
		ID:               "acc_demo_1",
		Email:            "ultra@colabhive.dev",
		Health:           models.HealthHealthy,
		Enabled:          true,
		MaxConcurrentVMs: 4,
		PaidCUBalance:    500,
		SchedulableCU:    500,
		Tags:             []string{"demo"},
	})

	rtr := router.NewServiceWithDirs(st, nil, nil, t.TempDir(), "", "", "linux")
	eng := runtime.NewConcreteEngine(st, nil, rtr, nil, runtime.Options{
		DemoMode:               true,
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	keyObj, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Antigravity Test"})
	if err != nil {
		t.Fatal(err)
	}

	mcpSrv := NewServer(nil, eng, rtr, nil)
	mcpSrv.SetAdminToken("admin-secret")

	// 1. Verify ALL required tools from Suites A, B, and C are statically registered at startup!
	requiredTools := []string{
		// Suite A
		"list_runtime_specs", "get_subscription", "create_runtime", "list_runtimes",
		"delete_runtime", "execute_code", "add_code_cell", "run_code_cell",
		"get_cells", "update_cell", "delete_cell", "move_cell",
		// Suite B
		"colab_session_create", "colab_session_list", "colab_session_stop", "colab_exec",
		"colab_file_upload", "colab_file_download", "colab_file_list", "colab_install_packages",
		// Suite C
		"pool_quota_summary", "experiment_launch_sharded", "experiment_status", "experiment_collect_and_teardown",
	}
	registered := make(map[string]bool)
	for _, td := range mcpSrv.Tools() {
		registered[td.Name] = true
	}
	for _, reqName := range requiredTools {
		if !registered[reqName] {
			t.Fatalf("missing required static MCP tool %q", reqName)
		}
	}

	httpSrv := httptest.NewServer(mcpSrv.Handler())
	defer httpSrv.Close()

	postMCP := func(sessHeader string, rpcBody string) (string, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp?key="+keyObj.RawKey, strings.NewReader(rpcBody))
		req.Header.Set("Content-Type", "application/json")
		if sessHeader != "" {
			req.Header.Set("Mcp-Session-Id", sessHeader)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.Header.Get("Mcp-Session-Id"), out
	}

	// 2. Initialize -> receive `Mcp-Session-Id` sticky session header & `2025-03-26` protocolVersion
	mcpSessionID, initResp := postMCP("", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"antigravity","version":"2.0"}}}`)
	if mcpSessionID == "" {
		t.Fatalf("expected Mcp-Session-Id header on initialize")
	}
	resMap, _ := initResp["result"].(map[string]any)
	if resMap["protocolVersion"] != ProtocolVersion20250326 {
		t.Fatalf("unexpected protocolVersion: %+v", resMap)
	}

	// 3. `execute_code` auto-provisions a G4 runtime, pins it to `mcpSessionID`, and returns ImageContent when `inline_images: true`!
	_, execResp := postMCP(mcpSessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"execute_code","arguments":{"code":"import matplotlib.pyplot as plt\nplt.plot([1,2])","inline_images":true}}}`)
	execResult, _ := execResp["result"].(map[string]any)
	contentArr, _ := execResult["content"].([]any)
	if len(contentArr) < 2 {
		t.Fatalf("expected text + image content blocks on matplotlib execution, got %+v", contentArr)
	}

	// Verify sticky binding recorded in router!
	bindings, _ := rtr.ListStickyBindings(ctx)
	if len(bindings) != 1 || bindings[0].StickyKey != mcpSessionID {
		t.Fatalf("expected sticky binding for %s, got %+v", mcpSessionID, bindings)
	}

	// 4. `add_code_cell` -> `get_cells` reuses the sticky session without passing `session`!
	_, _ = postMCP(mcpSessionID, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add_code_cell","arguments":{"code":"print('hello cell')","run":true}}}`)
	_, cellsResp := postMCP(mcpSessionID, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_cells","arguments":{}}}`)
	cellsRaw, _ := json.Marshal(cellsResp)
	if !strings.Contains(string(cellsRaw), "hello cell") {
		t.Fatalf("expected cell in get_cells output: %s", string(cellsRaw))
	}

	// 5. `GET /mcp` SSE stream opens cleanly with `text/event-stream`.
	sseCtx, sseCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer sseCancel()
	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, httpSrv.URL+"/mcp?key="+keyObj.RawKey, nil)
	sseReq.Header.Set("Mcp-Session-Id", mcpSessionID)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sseResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %s", sseResp.Header.Get("Content-Type"))
	}
	_ = sseResp.Body.Close()

	// 6. `DELETE /mcp` unbinds sticky session.
	delReq, _ := http.NewRequest(http.MethodDelete, httpSrv.URL+"/mcp?key="+keyObj.RawKey, nil)
	delReq.Header.Set("Mcp-Session-Id", mcpSessionID)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /mcp failed: err=%v resp=%+v", err, delResp)
	}
	_ = delResp.Body.Close()

	// 7. `stdio` transport test (`tools/list` + `list_runtime_specs`).
	stdioIn := bytes.NewBufferString(
		"{\"jsonrpc\":\"2.0\",\"id\":10,\"method\":\"tools/list\"}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":11,\"method\":\"tools/call\",\"params\":{\"name\":\"list_runtime_specs\",\"arguments\":{}}}\n",
	)
	var stdioOut bytes.Buffer
	if err := mcpSrv.ServeStdio(ctx, stdioIn, &stdioOut); err != nil {
		t.Fatalf("ServeStdio failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdioOut.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "RTX PRO 6000 Blackwell") {
		t.Fatalf("unexpected stdio output: %s", stdioOut.String())
	}

	// Verify router trace inspector captured all tool calls!
	traces, _ := rtr.ListTraces(ctx, 20)
	if len(traces) < 4 {
		t.Fatalf("expected >=4 recorded MCP traces, got %d", len(traces))
	}
}

func TestMCPProtocol20250618NullIDTemplatesLegacySSEAndExternalVMGuard(t *testing.T) {
	ctx := context.Background()
	st := newMCPTestStore()
	_ = st.SaveAccount(ctx, &models.Account{
		ID:               "acc_owner",
		Email:            "owner@colabhive.dev",
		Health:           models.HealthHealthy,
		Enabled:          true,
		MaxConcurrentVMs: 4,
		PaidCUBalance:    600,
		SchedulableCU:    600,
		Tags:             []string{"owner"},
	})

	rtr := router.NewServiceWithDirs(st, nil, nil, t.TempDir(), "", "", "linux")
	eng := runtime.NewConcreteEngine(st, nil, rtr, nil, runtime.Options{
		DemoMode:               true,
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	mcpSrv := NewServer(nil, eng, rtr, nil)
	httpSrv := httptest.NewServer(mcpSrv.Handler())
	defer httpSrv.Close()

	// 1. CORS Preflight MUST expose MCP-Protocol-Version and Last-Event-ID.
	optReq, _ := http.NewRequest(http.MethodOptions, httpSrv.URL+"/mcp", nil)
	optResp, err := http.DefaultClient.Do(optReq)
	if err != nil || optResp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS /mcp failed: err=%v resp=%+v", err, optResp)
	}
	allowHdr := optResp.Header.Get("Access-Control-Allow-Headers")
	_ = optResp.Body.Close()
	if !strings.Contains(allowHdr, "MCP-Protocol-Version") || !strings.Contains(allowHdr, "Last-Event-ID") {
		t.Fatalf("missing MCP-Protocol-Version / Last-Event-ID in CORS Allow-Headers: %s", allowHdr)
	}

	// 2. Placeholder fake tokens (`ch_sk_live_default`, `*_live_token`, `...`) MUST fail closed (401 Unauthorized),
	//    NEVER silently stripping to `""` (unrestricted admin)!
	for _, fakeTok := range []string{"ch_sk_live_default", "ch_sk_live_8f9a..._live_token", "ch_sk_live_xxxxxxxxxxxxxxxx"} {
		reqFake, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		reqFake.Header.Set("Authorization", "Bearer "+fakeTok)
		respFake, fErr := http.DefaultClient.Do(reqFake)
		if fErr != nil {
			t.Fatal(fErr)
		}
		_ = respFake.Body.Close()
		if respFake.StatusCode != http.StatusUnauthorized {
			t.Fatalf("placeholder token %q caused HTTP %d instead of fail-closed 401 Unauthorized", fakeTok, respFake.StatusCode)
		}
	}
	reqBadKey, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	reqBadKey.Header.Set("Authorization", "Bearer ch_sk_live_invalidrealkey0001")
	respBadKey, err := http.DefaultClient.Do(reqBadKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = respBadKey.Body.Close()
	if respBadKey.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid real ch_sk_ key, got %d", respBadKey.StatusCode)
	}

	postRaw := func(body string) (int, []byte) {
		t.Helper()
		resp, pErr := http.Post(httpSrv.URL+"/mcp", "application/json", strings.NewReader(body))
		if pErr != nil {
			t.Fatal(pErr)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	// 3. `initialize` negotiates `2025-06-18` and `2025-11-25` cleanly.
	for _, ver := range []string{"2025-06-18", "2025-11-25", "2024-11-05"} {
		stCode, raw := postRaw(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + ver + `","clientInfo":{"name":"sdk-client","version":"1.13"}}}`)
		if stCode != http.StatusOK || !strings.Contains(string(raw), `"protocolVersion":"`+ver+`"`) {
			t.Fatalf("initialize negotiation failed for %s: code=%d body=%s", ver, stCode, string(raw))
		}
	}

	// 4. `id` omitted, `"id": null`, and client JSON-RPC Responses (`method == ""`) MUST return HTTP 202 Accepted with empty body!
	for _, nonReply := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":null,"method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":99,"result":{}}`,
		`[{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":null,"method":"cancelled"}]`,
	} {
		stCode, raw := postRaw(nonReply)
		if stCode != http.StatusAccepted || len(raw) != 0 {
			t.Fatalf("expected HTTP 202 Accepted + empty body for %s, got code=%d body=%q", nonReply, stCode, string(raw))
		}
	}

	// Empty JSON-RPC batch `[]` MUST return `-32600` Invalid Request (not 202).
	emptyBatchCode, emptyBatchBody := postRaw(`[]`)
	if emptyBatchCode != http.StatusOK || !strings.Contains(string(emptyBatchBody), `"-32600"`) && !strings.Contains(string(emptyBatchBody), `-32600`) {
		t.Fatalf("expected -32600 on empty batch [], got code=%d body=%s", emptyBatchCode, string(emptyBatchBody))
	}

	// 5. SDK methods `resources/templates/list`, `logging/setLevel`, `completion/complete` MUST succeed without -32601!
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"jsonrpc":"2.0","id":2,"method":"resources/templates/list"}`, `"resourceTemplates":[]`},
		{`{"jsonrpc":"2.0","id":3,"method":"logging/setLevel","params":{"level":"info"}}`, `"result":{}`},
		{`{"jsonrpc":"2.0","id":4,"method":"completion/complete","params":{}}`, `"hasMore":false`},
	} {
		stCode, raw := postRaw(tc.body)
		if stCode != http.StatusOK || !strings.Contains(string(raw), tc.want) || strings.Contains(string(raw), "-32601") {
			t.Fatalf("SDK method failed for %s: code=%d body=%s", tc.body, stCode, string(raw))
		}
	}

	// 6. Dual-stack `GET /mcp`:
	//    - Plain GET without `Accept: text/event-stream` and without `Mcp-Session-Id` -> 405 Method Not Allowed
	//    - GET `/mcp` WITH `Accept: text/event-stream` and NO `Mcp-Session-Id` / `MCP-Protocol-Version` -> Legacy 2024-11-05 SSE!
	plainGetResp, err := http.Get(httpSrv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = plainGetResp.Body.Close()
	if plainGetResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on plain GET /mcp without Accept: text/event-stream, got %d", plainGetResp.StatusCode)
	}

	validKey, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Windsurf Legacy SSE"})
	if err != nil {
		t.Fatal(err)
	}

	sseCtx, sseCancel := context.WithTimeout(ctx, 4*time.Second)
	defer sseCancel()
	legacyGet, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, httpSrv.URL+"/mcp?api_key="+validKey.RawKey, nil)
	legacyGet.Header.Set("Accept", "text/event-stream")
	legacyResp, err := http.DefaultClient.Do(legacyGet)
	if err != nil {
		t.Fatalf("Legacy GET /mcp SSE failed: %v", err)
	}
	defer legacyResp.Body.Close()
	if legacyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on Legacy GET /mcp SSE, got %d", legacyResp.StatusCode)
	}

	sseReader := bufio.NewReader(legacyResp.Body)
	eventLine, err := sseReader.ReadString('\n')
	if err != nil || !strings.HasPrefix(strings.TrimSpace(eventLine), "event: endpoint") {
		t.Fatalf("expected initial 'event: endpoint' on legacy GET /mcp SSE, got %q (err=%v)", eventLine, err)
	}
	dataLine, err := sseReader.ReadString('\n')
	if err != nil || !strings.Contains(dataLine, "/mcp/messages?session_id=") || !strings.Contains(dataLine, "&key="+validKey.RawKey) {
		t.Fatalf("unexpected endpoint data line: %q (err=%v)", dataLine, err)
	}
	_, _ = sseReader.ReadString('\n') // blank separator line

	endpointRel := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(dataLine), "data:"))
	msgPostResp, err := http.Post(
		httpSrv.URL+endpointRel,
		"application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":77,"method":"resources/templates/list"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = msgPostResp.Body.Close()
	if msgPostResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected immediate 202 Accepted from POST %s, got %d", endpointRel, msgPostResp.StatusCode)
	}

	// Read async JSON-RPC response delivered over the SSE stream!
	var gotAsyncMsg string
	for i := 0; i < 6; i++ {
		ln, rErr := sseReader.ReadString('\n')
		if rErr != nil {
			t.Fatalf("failed reading async SSE response: %v", rErr)
		}
		if strings.HasPrefix(strings.TrimSpace(ln), "data:") {
			gotAsyncMsg = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "data:"))
			break
		}
	}
	if !strings.Contains(gotAsyncMsg, `"id":77`) || !strings.Contains(gotAsyncMsg, `"resourceTemplates"`) {
		t.Fatalf("unexpected async SSE message payload: %s", gotAsyncMsg)
	}

	// 7. External-VM Protection (`AdoptedExternal == true`) + Scoped API Key ACL Forwarding:
	//    Seed a single active external browser session (`AdoptedExternal: true`).
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:              "sess_ext_user_nb",
		Name:            "external-browser-training",
		AccountID:       "acc_owner",
		AccountEmail:    "owner@colabhive.dev",
		Accelerator:     models.AcceleratorA100,
		Status:          models.SessionBusy,
		AdoptedExternal: true,
		NotebookName:    "important_paper_train.ipynb",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})

	// Calling `delete_runtime({})` or `colab_kernel_restart({})` without `session` MUST NEVER hijack or kill `sess_ext_user_nb`!
	for _, safeTool := range []string{"delete_runtime", "colab_kernel_restart", "get_cells"} {
		_, raw := postRaw(`{"jsonrpc":"2.0","id":80,"method":"tools/call","params":{"name":"` + safeTool + `","arguments":{}}}`)
		if !strings.Contains(string(raw), `"isError":true`) {
			t.Fatalf("expected %s without session to refuse targeting AdoptedExternal VM, got: %s", safeTool, string(raw))
		}
	}
	extCheck, _ := st.GetSession(ctx, "sess_ext_user_nb")
	if extCheck == nil || extCheck.Status != models.SessionBusy {
		t.Fatalf("CRITICAL: AdoptedExternal session was modified or stopped by default MCP fallback: %+v", extCheck)
	}

	// Scoped API Key with `AllowedGPUs: [T4]` & `AllowedGroups: ["sandbox-only"]` calling `execute_code` directly
	// (auto-provisioning without `create_runtime`) MUST forward `APIKeyID` and enforce ACL rejection!
	restrictedKey, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:          "Restricted Sandbox Bot",
		AllowedGroups: []string{"sandbox-only"},
		AllowedGPUs:   []models.AcceleratorType{models.AcceleratorT4},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqACL, _ := http.NewRequest(
		http.MethodPost,
		httpSrv.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":88,"method":"tools/call","params":{"name":"execute_code","arguments":{"code":"print(1)","gpu":"H100"}}}`),
	)
	reqACL.Header.Set("Content-Type", "application/json")
	reqACL.Header.Set("Authorization", "Bearer "+restrictedKey.RawKey)
	respACL, err := http.DefaultClient.Do(reqACL)
	if err != nil {
		t.Fatal(err)
	}
	aclBody, _ := io.ReadAll(respACL.Body)
	_ = respACL.Body.Close()
	if !strings.Contains(string(aclBody), `"isError":true`) {
		t.Fatalf("expected restricted ch_sk_ key auto-provision execute_code to be rejected by ACL, got: %s", string(aclBody))
	}

	// 8. Concurrent `ServeStdio`: `ping` heartbeat MUST reply immediately while a slow `tools/call` is blocked!
	blockCh := make(chan struct{})
	startedSlow := make(chan struct{})
	mcpSrv.registerTool(ToolDefinition{
		Name:        "test_slow_gpu_op",
		Description: "Blocks until released",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, _ callContext, _ map[string]any) (*CallToolResult, traceSummary, error) {
		close(startedSlow)
		<-blockCh
		return jsonToolResult(map[string]any{"done": true}), traceSummary{}, nil
	})

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stdioDone := make(chan error, 1)
	go func() {
		stdioDone <- mcpSrv.ServeStdio(ctx, inR, outW)
		_ = outW.Close()
	}()

	outScanner := bufio.NewScanner(outR)
	_, _ = inW.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":501,\"method\":\"tools/call\",\"params\":{\"name\":\"test_slow_gpu_op\",\"arguments\":{}}}\n"))
	<-startedSlow

	// Send `ping` while `test_slow_gpu_op` is still blocked!
	_, _ = inW.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":502,\"method\":\"ping\"}\n"))
	pingLineCh := make(chan string, 1)
	go func() {
		if outScanner.Scan() {
			pingLineCh <- outScanner.Text()
		}
	}()

	select {
	case pingResp := <-pingLineCh:
		if !strings.Contains(pingResp, `"id":502`) {
			t.Fatalf("expected ping id=502 before slow tool finished, got: %s", pingResp)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ServeStdio head-of-line blocked ping during slow tools/call!")
	}

	close(blockCh)
	_ = inW.Close()
	if !outScanner.Scan() || !strings.Contains(outScanner.Text(), `"id":501`) {
		t.Fatalf("expected slow tool response id=501 after unblock")
	}
	if err := <-stdioDone; err != nil {
		t.Fatalf("concurrent ServeStdio returned error: %v", err)
	}
}

type mockPoolForMCPQuota struct {
	models.AccountPoolService
	mu            sync.Mutex
	refreshCalled int
	accounts      []*models.Account
}

func (m *mockPoolForMCPQuota) RefreshAllQuotas(_ context.Context) (*models.QuotaSummary, error) {
	m.mu.Lock()
	m.refreshCalled++
	m.mu.Unlock()
	return &models.QuotaSummary{AccountsTotal: len(m.accounts), TotalAvailableCU: 900, SchedulableCU: 900}, nil
}

func (m *mockPoolForMCPQuota) GetQuotaSummary(_ context.Context) (*models.QuotaSummary, error) {
	return &models.QuotaSummary{AccountsTotal: len(m.accounts), TotalAvailableCU: 900, SchedulableCU: 900}, nil
}

func (m *mockPoolForMCPQuota) ListAccounts(_ context.Context) ([]*models.Account, error) {
	return m.accounts, nil
}

func TestMCPHostFSRejectionTenantIsolationAuthAndCancellation(t *testing.T) {
	ctx := context.Background()
	st := newMCPTestStore()
	ownerAcc := &models.Account{
		ID:               "acc_owner_secret",
		Email:            "secret-owner@corp.internal",
		Health:           models.HealthHealthy,
		Enabled:          true,
		Groups:           []string{"team-alpha"},
		MaxConcurrentVMs: 8,
		PaidCUBalance:    900,
		SchedulableCU:    900,
	}
	_ = st.SaveAccount(ctx, ownerAcc)

	mockPool := &mockPoolForMCPQuota{accounts: []*models.Account{ownerAcc}}
	rtr := router.NewServiceWithDirs(st, nil, nil, t.TempDir(), "", "", "linux")
	eng := runtime.NewConcreteEngine(st, nil, rtr, nil, runtime.Options{
		DemoMode:               true,
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	keyA, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:             "Tenant-A",
		AllowedGroups:    []string{"team-alpha"},
		MaxBudgetCUTotal: 150,
		MaxBudgetCUDaily: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:             "Tenant-B",
		AllowedGroups:    []string{"team-alpha"},
		MaxBudgetCUTotal: 150,
	})
	if err != nil {
		t.Fatal(err)
	}

	mcpSrv := NewServer(mockPool, eng, rtr, nil)
	httpSrv := httptest.NewServer(mcpSrv.Handler())
	defer httpSrv.Close()

	// 1. Unrecognized non-ch_sk_ bearer token MUST return 401 even when AdminToken == ""!
	reqUnrec, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	reqUnrec.Header.Set("Authorization", "Bearer arbitrary-non-chsk-token")
	respUnrec, err := http.DefaultClient.Do(reqUnrec)
	if err != nil {
		t.Fatal(err)
	}
	_ = respUnrec.Body.Close()
	if respUnrec.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unrecognized bearer token, got %d", respUnrec.StatusCode)
	}

	// 2. Cross-origin Origin header in keyless local mode MUST return 403 Forbidden!
	reqEvilOrigin, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	reqEvilOrigin.Header.Set("Origin", "https://evil.attacker.example")
	respEvilOrigin, err := http.DefaultClient.Do(reqEvilOrigin)
	if err != nil {
		t.Fatal(err)
	}
	_ = respEvilOrigin.Body.Close()
	if respEvilOrigin.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on cross-origin MCP request in keyless local mode, got %d", respEvilOrigin.StatusCode)
	}

	callToolHTTP := func(bearer string, mcpSid string, toolName string, args map[string]any) (string, map[string]any, string) {
		t.Helper()
		rpc := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      toolName,
				"arguments": args,
			},
		}
		b, _ := json.Marshal(rpc)
		req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if mcpSid != "" {
			req.Header.Set("Mcp-Session-Id", mcpSid)
		}
		resp, dErr := http.DefaultClient.Do(req)
		if dErr != nil {
			t.Fatal(dErr)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		resObj, _ := parsed["result"].(map[string]any)
		return resp.Header.Get("Mcp-Session-Id"), resObj, string(raw)
	}

	// 3. Create Tenant A session + Admin session + External session
	sidHeaderA, createResA, _ := callToolHTTP(keyA.RawKey, "", "create_runtime", map[string]any{"name": "tenant-a-vm", "gpu": "G4"})
	scA, _ := createResA["structuredContent"].(map[string]any)
	sessAID, _ := scA["id"].(string)
	if sessAID == "" {
		t.Fatalf("expected session ID for Tenant A, got %+v", createResA)
	}

	_, createAdminRes, _ := callToolHTTP("", "", "create_runtime", map[string]any{"name": "admin-vm", "gpu": "G4"})
	scAdmin, _ := createAdminRes["structuredContent"].(map[string]any)
	adminSessID, _ := scAdmin["id"].(string)

	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:              "sess_ext_isolated",
		Name:            "ext-nb",
		AccountID:       ownerAcc.ID,
		Accelerator:     models.AcceleratorA100,
		Status:          models.SessionBusy,
		AdoptedExternal: true,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})

	// 4. Remote HTTP MCP MUST reject ALL host-filesystem path parameters (`local_path`, `local_file`, `requirements_path`, `script_path`, `upload_files[].local_path`, `local_output_dir`)!
	hostSecretFile := filepath.Join(t.TempDir(), "host_secret.txt")
	_ = os.WriteFile(hostSecretFile, []byte("TOP_SECRET_VAULT"), 0o600)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"colab_file_upload", map[string]any{"session": sessAID, "local_path": hostSecretFile, "remote_path": "/content/leak.txt"}},
		{"colab_file_download", map[string]any{"session": sessAID, "remote_path": "/content/a.txt", "local_path": filepath.Join(t.TempDir(), "pwn.txt")}},
		{"colab_exec", map[string]any{"session": sessAID, "local_file": hostSecretFile}},
		{"colab_install_packages", map[string]any{"session": sessAID, "requirements_path": hostSecretFile}},
		{"experiment_launch_sharded", map[string]any{"experiment_id": "exp_host_leak1", "script_path": hostSecretFile}},
		{"experiment_launch_sharded", map[string]any{"experiment_id": "exp_host_leak2", "script_code": "print(1)", "local_output_dir": t.TempDir()}},
		{"experiment_launch_sharded", map[string]any{"experiment_id": "exp_host_leak3", "script_code": "print(1)", "upload_files": []any{map[string]any{"local_path": hostSecretFile, "remote_path": "/content/x"}}}},
		{"experiment_launch_sharded", map[string]any{"experiment_id": "../escape_exp", "script_code": "print(1)"}},
	} {
		_, resObj, raw := callToolHTTP(keyA.RawKey, sidHeaderA, tc.tool, tc.args)
		isErr, _ := resObj["isError"].(bool)
		if !isErr {
			t.Fatalf("SECURITY FAILURE: remote HTTP MCP allowed host path in %s (%+v): %s", tc.tool, tc.args, raw)
		}
	}

	// Inline `content` upload & inline `colab_file_download` (without `local_path`) MUST still work over remote HTTP MCP!
	_, upOK, upRaw := callToolHTTP(keyA.RawKey, sidHeaderA, "colab_file_upload", map[string]any{
		"session":     sessAID,
		"remote_path": "/content/inline_ok.txt",
		"content":     "SAFE_INLINE_PAYLOAD",
	})
	if upOK["isError"] == true {
		t.Fatalf("inline colab_file_upload failed: %s", upRaw)
	}
	_, dlOK, dlRaw := callToolHTTP(keyA.RawKey, sidHeaderA, "colab_file_download", map[string]any{
		"session":     sessAID,
		"remote_path": "/content/inline_ok.txt",
	})
	if dlOK["isError"] == true || !strings.Contains(dlRaw, "SAFE_INLINE_PAYLOAD") {
		t.Fatalf("inline colab_file_download failed: %s", dlRaw)
	}

	// 5. Cross-Tenant Isolation: Tenant B MUST NOT access Tenant A's session, Admin's session, or External session across ANY tool!
	for _, targetSess := range []string{sessAID, adminSessID, "sess_ext_isolated"} {
		for _, tool := range []string{
			"colab_session_status",
			"delete_runtime",
			"colab_session_stop",
			"open_colab_browser_connection",
			"execute_code",
			"colab_exec",
			"get_cells",
			"add_code_cell",
			"colab_file_list",
			"colab_file_upload",
			"colab_install_packages",
		} {
			args := map[string]any{"session": targetSess, "code": "print(1)", "remote_path": "/content/x", "content": "x", "packages": []string{"six"}}
			_, resObj, raw := callToolHTTP(keyB.RawKey, "", tool, args)
			if resObj["isError"] != true || !strings.Contains(strings.ToLower(raw), "forbidden") {
				t.Fatalf("TENANT ISOLATION FAILURE: KeyB accessed %s via %s: %s", targetSess, tool, raw)
			}
		}
	}

	// `list_runtimes` for KeyA returns ONLY KeyA's session and redacts owner email!
	_, listARes, listARaw := callToolHTTP(keyA.RawKey, sidHeaderA, "list_runtimes", map[string]any{})
	if strings.Contains(listARaw, adminSessID) || strings.Contains(listARaw, "sess_ext_isolated") || strings.Contains(listARaw, ownerAcc.Email) {
		t.Fatalf("list_runtimes leaked foreign sessions or owner email to KeyA: %s", listARaw)
	}
	if !strings.Contains(listARaw, sessAID) {
		t.Fatalf("expected KeyA's own session %s in list_runtimes: %s", listARes, listARaw)
	}

	// `pool_quota_summary` for KeyA MUST ignore `refresh_live=true`, redact owner email, and include `api_key_budget`!
	_, quotaResA, quotaRawA := callToolHTTP(keyA.RawKey, sidHeaderA, "pool_quota_summary", map[string]any{"refresh_live": true})
	if quotaResA["isError"] == true {
		t.Fatalf("pool_quota_summary failed for KeyA: %s", quotaRawA)
	}
	if mockPool.refreshCalled != 0 {
		t.Fatalf("scoped API key triggered upstream RefreshAllQuotas (%d calls)!", mockPool.refreshCalled)
	}
	if strings.Contains(quotaRawA, ownerAcc.Email) || !strings.Contains(quotaRawA, "remaining_cu_total") {
		t.Fatalf("pool_quota_summary leaked owner email or omitted api_key_budget: %s", quotaRawA)
	}

	// Experiment Tenant Isolation: KeyA launches inline sharded experiment; KeyB CANNOT inspect, overwrite, or teardown it!
	_, expARes, expARaw := callToolHTTP(keyA.RawKey, sidHeaderA, "experiment_launch_sharded", map[string]any{
		"experiment_id": "exp_tenant_a_only",
		"script_code":   "print('shard ok')",
		"num_shards":    2,
		"gpu":           "G4",
		"upload_files": []any{
			map[string]any{"remote_path": "/content/cfg.json", "content": `{"ok":true}`},
		},
	})
	if expARes["isError"] == true {
		t.Fatalf("KeyA inline experiment_launch_sharded failed: %s", expARaw)
	}
	_, dupExpRes, _ := callToolHTTP(keyB.RawKey, "", "experiment_launch_sharded", map[string]any{
		"experiment_id": "exp_tenant_a_only",
		"script_code":   "print('hijack')",
		"num_shards":    1,
	})
	if dupExpRes["isError"] != true {
		t.Fatalf("expected duplicate experiment_id launch by KeyB to fail!")
	}
	for _, expTool := range []string{"experiment_status", "experiment_collect_and_teardown"} {
		_, bExpRes, bExpRaw := callToolHTTP(keyB.RawKey, "", expTool, map[string]any{"experiment_id": "exp_tenant_a_only"})
		if bExpRes["isError"] != true || !strings.Contains(strings.ToLower(bExpRaw), "forbidden") {
			t.Fatalf("expected KeyB %s on KeyA's experiment to be forbidden, got: %s", expTool, bExpRaw)
		}
	}

	// 6. Unknown `POST /mcp/messages?session_id=nonexistent` MUST return 404 Not Found;
	//    `DELETE /mcp` by KeyB on KeyA's Mcp-Session-Id MUST return 403 Forbidden;
	//    `DELETE /mcp` by KeyA MUST close open `GET /mcp` SSE stream immediately!
	unknownMsgResp, err := http.Post(
		httpSrv.URL+"/mcp/messages?session_id=mcp-sse-doesnotexist&key="+keyA.RawKey,
		"application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = unknownMsgResp.Body.Close()
	if unknownMsgResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found on unknown /mcp/messages session_id, got %d", unknownMsgResp.StatusCode)
	}

	sseOpenCtx, sseOpenCancel := context.WithTimeout(ctx, 5*time.Second)
	defer sseOpenCancel()
	sseReqA, _ := http.NewRequestWithContext(sseOpenCtx, http.MethodGet, httpSrv.URL+"/mcp", nil)
	sseReqA.Header.Set("Authorization", "Bearer "+keyA.RawKey)
	sseReqA.Header.Set("Mcp-Session-Id", sidHeaderA)
	sseReqA.Header.Set("Accept", "text/event-stream")
	sseRespA, err := http.DefaultClient.Do(sseReqA)
	if err != nil {
		t.Fatal(err)
	}
	sseStreamDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, sseRespA.Body)
		_ = sseRespA.Body.Close()
		close(sseStreamDone)
	}()

	// KeyB attempts `DELETE /mcp` with KeyA's `Mcp-Session-Id` -> 403!
	delByB, _ := http.NewRequest(http.MethodDelete, httpSrv.URL+"/mcp", nil)
	delByB.Header.Set("Authorization", "Bearer "+keyB.RawKey)
	delByB.Header.Set("Mcp-Session-Id", sidHeaderA)
	delByBResp, err := http.DefaultClient.Do(delByB)
	if err != nil {
		t.Fatal(err)
	}
	_ = delByBResp.Body.Close()
	if delByBResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when KeyB attempts DELETE /mcp on KeyA's Mcp-Session-Id, got %d", delByBResp.StatusCode)
	}

	// KeyA sends `DELETE /mcp` -> 204 and closes open SSE stream promptly!
	delByA, _ := http.NewRequest(http.MethodDelete, httpSrv.URL+"/mcp", nil)
	delByA.Header.Set("Authorization", "Bearer "+keyA.RawKey)
	delByA.Header.Set("Mcp-Session-Id", sidHeaderA)
	delByAResp, err := http.DefaultClient.Do(delByA)
	if err != nil || delByAResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on KeyA DELETE /mcp, got err=%v resp=%+v", err, delByAResp)
	}
	_ = delByAResp.Body.Close()

	select {
	case <-sseStreamDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("DELETE /mcp did not close open SSE stream for %s!", sidHeaderA)
	}

	// 7. `ServeStdio` MUST fail closed on invalid `COLABHIVE_API_KEY` or `SetStdioAPIKey`,
	//    and `notifications/cancelled` MUST cancel an in-flight `tools/call` context!
	t.Setenv("COLABHIVE_API_KEY", "ch_sk_live_revoked_or_bogus_999999")
	var badStdioOut bytes.Buffer
	errBadStdio := mcpSrv.ServeStdio(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &badStdioOut)
	if errBadStdio == nil || badStdioOut.Len() > 0 {
		t.Fatalf("SECURITY FAILURE: ServeStdio failed open on invalid COLABHIVE_API_KEY: err=%v out=%q", errBadStdio, badStdioOut.String())
	}
	t.Setenv("COLABHIVE_API_KEY", "")

	mcpSrv.SetStdioAPIKey("ch_sk_live_explicit_flag_bogus")
	errBadFlag := mcpSrv.ServeStdio(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &badStdioOut)
	if errBadFlag == nil {
		t.Fatalf("SECURITY FAILURE: ServeStdio failed open on invalid SetStdioAPIKey!")
	}
	mcpSrv.SetStdioAPIKey("")

	cancelStarted := make(chan struct{})
	cancelObserved := make(chan struct{})
	mcpSrv.registerTool(ToolDefinition{
		Name:        "test_cancellable_op",
		Description: "Waits for ctx.Done()",
		InputSchema: map[string]any{"type": "object"},
	}, func(callCtx context.Context, _ callContext, _ map[string]any) (*CallToolResult, traceSummary, error) {
		close(cancelStarted)
		<-callCtx.Done()
		close(cancelObserved)
		return nil, traceSummary{}, callCtx.Err()
	})

	cInR, cInW := io.Pipe()
	cOutR, cOutW := io.Pipe()
	cDone := make(chan error, 1)
	go func() {
		cDone <- mcpSrv.ServeStdio(ctx, cInR, cOutW)
		_ = cOutW.Close()
	}()
	go func() {
		_, _ = io.Copy(io.Discard, cOutR)
	}()

	_, _ = cInW.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":909,\"method\":\"tools/call\",\"params\":{\"name\":\"test_cancellable_op\",\"arguments\":{}}}\n"))
	<-cancelStarted
	_, _ = cInW.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":{\"requestId\":909,\"reason\":\"user abort\"}}\n"))

	select {
	case <-cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatalf("notifications/cancelled did not cancel in-flight tools/call context!")
	}
	_ = cInW.Close()
	_ = <-cDone

	// 8. Wave-5 Security Regressions:
	//    - `SetStdioAPIKey("ch_sk_live_default")` MUST fail closed (never strip to "")
	//    - `POST /mcp?token=<scoped>&key=ch_sk_live_default` MUST stay scoped (never escalate to admin)
	//    - `pool_quota_summary` with unmatched `tag` MUST return 0 totals (not global totals)
	//    - Non-loopback `Host` header MUST return 403 Forbidden in keyless local mode!
	mcpSrv.SetStdioAPIKey("ch_sk_live_default")
	var placeholderStdioOut bytes.Buffer
	if errPlaceholder := mcpSrv.ServeStdio(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &placeholderStdioOut); errPlaceholder == nil {
		t.Fatal("SECURITY FAILURE: ServeStdio stripped placeholder key ch_sk_live_default to empty admin!")
	}
	mcpSrv.SetStdioAPIKey("")

	dualQueryBody := `{"jsonrpc":"2.0","id":77,"method":"tools/call","params":{"name":"pool_quota_summary","arguments":{"tag":"nonexistent_tag_xyz"}}}`
	reqDual, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp?token="+keyA.RawKey+"&key=ch_sk_live_default", strings.NewReader(dualQueryBody))
	reqDual.Header.Set("Content-Type", "application/json")
	respDual, dErr := http.DefaultClient.Do(reqDual)
	if dErr != nil {
		t.Fatal(dErr)
	}
	rawDual, _ := io.ReadAll(respDual.Body)
	_ = respDual.Body.Close()
	if respDual.StatusCode != http.StatusOK || !strings.Contains(string(rawDual), `"api_key_budget"`) || !strings.Contains(string(rawDual), `"total_available_cu":0`) {
		t.Fatalf("expected scoped response with 0 CU on unmatched tag & dual query params, got HTTP %d: %s", respDual.StatusCode, string(rawDual))
	}

	reqRebind, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	reqRebind.Host = "evil-rebind.attacker.example:7860"
	respRebind, rErr := http.DefaultClient.Do(reqRebind)
	if rErr != nil {
		t.Fatal(rErr)
	}
	_ = respRebind.Body.Close()
	if respRebind.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on DNS-rebinding Host header over keyless MCP, got %d", respRebind.StatusCode)
	}
}
