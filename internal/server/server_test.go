package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/colabhive/colabhive/internal/mcp"
	"github.com/colabhive/colabhive/internal/mockcolab"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/router"
	"github.com/colabhive/colabhive/internal/runtime"
	"github.com/colabhive/colabhive/internal/store"
)

type srvTestHub struct {
	mu   sync.Mutex
	subs map[chan models.WSEvent]struct{}
}

func newSrvTestHub() *srvTestHub {
	return &srvTestHub{subs: make(map[chan models.WSEvent]struct{})}
}
func (h *srvTestHub) Publish(ev models.WSEvent) {
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	h.mu.Unlock()
}
func (h *srvTestHub) Subscribe(_ context.Context) (<-chan models.WSEvent, func()) {
	ch := make(chan models.WSEvent, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}
func (h *srvTestHub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

type srvMemStore struct {
	mu          sync.RWMutex
	accounts    map[string]*models.Account
	proxies     map[string]*models.ProxyConfig
	invites     map[string]*models.ContributorInvite
	sessions    map[string]*models.RuntimeSession
	experiments map[string]*models.Experiment
	keys        map[string]*models.AgentAPIKey
	policy      models.RouterPolicy
	traces      []models.ToolTraceEvent
	waste       models.SavedWasteStats
}

func newSrvMemStore() *srvMemStore {
	return &srvMemStore{
		accounts:    make(map[string]*models.Account),
		proxies:     make(map[string]*models.ProxyConfig),
		invites:     make(map[string]*models.ContributorInvite),
		sessions:    make(map[string]*models.RuntimeSession),
		experiments: make(map[string]*models.Experiment),
		keys:        make(map[string]*models.AgentAPIKey),
		policy:      models.DefaultRouterPolicy(),
	}
}

func (s *srvMemStore) SaveAccount(_ context.Context, a *models.Account) error {
	s.mu.Lock()
	cp := *a
	s.accounts[a.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) GetAccount(_ context.Context, id string) (*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.accounts[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}
func (s *srvMemStore) GetAccountByEmail(_ context.Context, email string) (*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.accounts {
		if strings.EqualFold(a.Email, email) {
			cp := *a
			return &cp, nil
		}
	}
	return nil, nil
}
func (s *srvMemStore) GetAccountByReceiptHash(_ context.Context, _ string) (*models.Account, error) {
	return nil, nil
}
func (s *srvMemStore) ListAccounts(_ context.Context) ([]*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}
func (s *srvMemStore) DeleteAccount(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.accounts, id)
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) SaveProxy(_ context.Context, p *models.ProxyConfig) error {
	s.mu.Lock()
	cp := *p
	s.proxies[p.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) GetProxy(_ context.Context, id string) (*models.ProxyConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.proxies[id]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, nil
}
func (s *srvMemStore) ListProxies(_ context.Context) ([]*models.ProxyConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.ProxyConfig
	for _, p := range s.proxies {
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}
func (s *srvMemStore) DeleteProxy(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.proxies, id)
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) SaveInvite(_ context.Context, inv *models.ContributorInvite) error {
	s.mu.Lock()
	cp := *inv
	s.invites[inv.Code] = &cp
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) GetInvite(_ context.Context, code string) (*models.ContributorInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if inv, ok := s.invites[code]; ok {
		cp := *inv
		return &cp, nil
	}
	return nil, nil
}
func (s *srvMemStore) ListInvites(_ context.Context) ([]*models.ContributorInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.ContributorInvite
	for _, inv := range s.invites {
		cp := *inv
		out = append(out, &cp)
	}
	return out, nil
}
func (s *srvMemStore) DeleteInvite(_ context.Context, code string) error {
	s.mu.Lock()
	delete(s.invites, code)
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) SaveSession(_ context.Context, sess *models.RuntimeSession) error {
	s.mu.Lock()
	cp := *sess
	cp.Cells = append([]models.NotebookCell(nil), sess.Cells...)
	s.sessions[sess.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) GetSession(_ context.Context, id string) (*models.RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.sessions[id]; ok {
		cp := *v
		cp.Cells = append([]models.NotebookCell(nil), v.Cells...)
		return &cp, nil
	}
	return nil, nil
}
func (s *srvMemStore) GetSessionByName(_ context.Context, name string) (*models.RuntimeSession, error) {
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
func (s *srvMemStore) ListSessions(_ context.Context) ([]*models.RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.RuntimeSession
	for _, v := range s.sessions {
		cp := *v
		cp.Cells = append([]models.NotebookCell(nil), v.Cells...)
		out = append(out, &cp)
	}
	return out, nil
}
func (s *srvMemStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) SaveExperiment(_ context.Context, exp *models.Experiment) error {
	s.mu.Lock()
	cp := *exp
	cp.Shards = append([]models.ExperimentShard(nil), exp.Shards...)
	s.experiments[exp.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) GetExperiment(_ context.Context, id string) (*models.Experiment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.experiments[id]; ok {
		cp := *v
		cp.Shards = append([]models.ExperimentShard(nil), v.Shards...)
		return &cp, nil
	}
	return nil, nil
}
func (s *srvMemStore) ListExperiments(_ context.Context) ([]*models.Experiment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.Experiment
	for _, v := range s.experiments {
		cp := *v
		out = append(out, &cp)
	}
	return out, nil
}
func (s *srvMemStore) DeleteExperiment(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.experiments, id)
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) SaveAPIKey(_ context.Context, k *models.AgentAPIKey) error {
	s.mu.Lock()
	cp := *k
	s.keys[k.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) GetAPIKey(_ context.Context, id string) (*models.AgentAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if k, ok := s.keys[id]; ok {
		cp := *k
		return &cp, nil
	}
	return nil, nil
}
func (s *srvMemStore) GetAPIKeyByHash(_ context.Context, hash string) (*models.AgentAPIKey, error) {
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
func (s *srvMemStore) ListAPIKeys(_ context.Context) ([]*models.AgentAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.AgentAPIKey
	for _, k := range s.keys {
		cp := *k
		out = append(out, &cp)
	}
	return out, nil
}
func (s *srvMemStore) DeleteAPIKey(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.keys, id)
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) GetRouterPolicy(_ context.Context) (*models.RouterPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.policy
	return &cp, nil
}
func (s *srvMemStore) SaveRouterPolicy(_ context.Context, p *models.RouterPolicy) error {
	s.mu.Lock()
	s.policy = *p
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) AppendToolTrace(_ context.Context, tr *models.ToolTraceEvent) error {
	s.mu.Lock()
	s.traces = append([]models.ToolTraceEvent{*tr}, s.traces...)
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) ListToolTraces(_ context.Context, limit int) ([]models.ToolTraceEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit > len(s.traces) {
		limit = len(s.traces)
	}
	out := make([]models.ToolTraceEvent, limit)
	copy(out, s.traces[:limit])
	return out, nil
}
func (s *srvMemStore) RecordBurnSample(_ context.Context, _ models.AcceleratorType, _ models.MachineShape, _ float64) error {
	return nil
}
func (s *srvMemStore) GetCalibratedBurnRate(_ context.Context, _ models.AcceleratorType, _ models.MachineShape) (float64, bool, error) {
	return 0, false, nil
}
func (s *srvMemStore) AddSavedWaste(_ context.Context, saved float64, zombie bool) error {
	s.mu.Lock()
	s.waste.SavedIdleWasteCU += saved
	if zombie {
		s.waste.ZombieAbortsCount++
	} else {
		s.waste.AutoTeardownsCount++
	}
	s.mu.Unlock()
	return nil
}
func (s *srvMemStore) GetSavedWasteStats(_ context.Context) (models.SavedWasteStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.waste, nil
}
func (s *srvMemStore) Close() error { return nil }

func TestServerRESTWebSocketSPAAndDaemonCLI(t *testing.T) {
	ctx := context.Background()
	st := newSrvMemStore()
	hub := newSrvTestHub()
	artDir := t.TempDir()
	webDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<!doctype html><title>ColabHive SPA</title>"), 0o644)

	rtr := router.NewServiceWithDirs(st, nil, hub, t.TempDir(), "", "", "linux")
	eng := runtime.NewConcreteEngine(st, nil, rtr, hub, runtime.Options{
		DemoMode:               true,
		ArtifactsDir:           artDir,
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	if err := SeedDemoCluster(ctx, st, rtr, artDir, "http://127.0.0.1:7860"); err != nil {
		t.Fatalf("SeedDemoCluster failed: %v", err)
	}

	mcpSrv := mcp.NewServer(nil, eng, rtr, hub)
	srv := New(st, nil, eng, rtr, hub, mcpSrv, Config{
		Version:      "1.0.0-test",
		WebDir:       webDir,
		ArtifactsDir: artDir,
		DemoMode:     true,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Health, Hardware, Demo Quota Summary, Accounts, Experiments, Sticky, Traces
	respHealth, err := http.Get(ts.URL + "/api/v1/health")
	if err != nil || respHealth.StatusCode != 200 {
		t.Fatalf("health failed: err=%v resp=%+v", err, respHealth)
	}
	var health models.DaemonHealthStatus
	_ = json.NewDecoder(respHealth.Body).Decode(&health)
	_ = respHealth.Body.Close()
	if !health.Healthy || !health.AntiZombieArmed || health.ActiveSessions < 4 {
		t.Fatalf("unexpected health payload: %+v", health)
	}

	respSum, err := http.Get(ts.URL + "/api/v1/pool/summary")
	if err != nil || respSum.StatusCode != 200 {
		t.Fatal(err)
	}
	var sum models.QuotaSummary
	_ = json.NewDecoder(respSum.Body).Decode(&sum)
	_ = respSum.Body.Close()
	if sum.AccountsTotal != 4 || sum.TotalAvailableCU < 1400 {
		t.Fatalf("unexpected demo QuotaSummary: %+v", sum)
	}

	// 2. Public Contributor Portal (`/api/public/contribute/ch_inv_demo2026`)
	respInv, err := http.Get(ts.URL + "/api/public/contribute/ch_inv_demo2026")
	if err != nil || respInv.StatusCode != 200 {
		t.Fatalf("public invite failed: err=%v", err)
	}
	_ = respInv.Body.Close()

	// 3. SPA fallback for `/pool`, `/exp`, `/router`, `/cfg`, `/contribute/ch_inv_demo2026`
	for _, route := range []string{"/", "/pool", "/exp", "/router", "/cfg", "/contribute/ch_inv_demo2026"} {
		r, gErr := http.Get(ts.URL + route)
		if gErr != nil || r.StatusCode != 200 {
			t.Fatalf("SPA fallback failed for %s: err=%v", route, gErr)
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		_ = r.Body.Close()
		if !strings.Contains(buf.String(), "ColabHive SPA") {
			t.Fatalf("expected index.html on %s, got %q", route, buf.String())
		}
	}

	// 4. WebSocket `/ws/events` real-time telemetry test
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket connect failed: %v", err)
	}
	defer wsConn.Close()

	_ = wsConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var initialEv models.WSEvent
	if err := wsConn.ReadJSON(&initialEv); err != nil || initialEv.Type != models.WSEventHeartbeat {
		t.Fatalf("expected immediate heartbeat frame on /ws/events, got err=%v ev=%+v", err, initialEv)
	}

	hub.Publish(models.WSEvent{
		Type:      models.WSEventSessionOutput,
		Timestamp: time.Now().UTC(),
		Payload:   map[string]any{"text": "epoch 1 loss=0.31"},
	})
	var liveEv models.WSEvent
	if err := wsConn.ReadJSON(&liveEv); err != nil || liveEv.Type != models.WSEventSessionOutput {
		t.Fatalf("expected live session_output event, got err=%v ev=%+v", err, liveEv)
	}

	// 5. End-to-End `RunDaemonCLI` tests (`usage`, `new`, `exec`, `upload`, `download`, `url`, `stop`, `run`)
	ver := VersionInfo{Version: "1.0.0"}
	var outBuf, errBuf bytes.Buffer

	// `usage --gpu-hours`
	code := RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "usage", "--gpu-hours"}, nil, &outBuf, &errBuf, ver)
	if code != 0 || !strings.Contains(outBuf.String(), "Current balance:") || !strings.Contains(outBuf.String(), "G4 96GB") {
		t.Fatalf("CLI usage failed (code=%d): out=%s err=%s", code, outBuf.String(), errBuf.String())
	}

	// `new -s cli_test --gpu G4 --high-mem`
	outBuf.Reset()
	errBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "new", "-s", "cli_test", "--gpu", "G4", "--high-mem"}, nil, &outBuf, &errBuf, ver)
	if code != 0 || !strings.Contains(outBuf.String(), "Session READY") {
		t.Fatalf("CLI new failed (code=%d): out=%s err=%s", code, outBuf.String(), errBuf.String())
	}

	// `exec -s cli_test "!nvidia-smi"`
	outBuf.Reset()
	errBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "exec", "-s", "cli_test", "!nvidia-smi"}, nil, &outBuf, &errBuf, ver)
	if code != 0 || !strings.Contains(outBuf.String(), "RTX PRO 6000 Blackwell") {
		t.Fatalf("CLI exec failed (code=%d): out=%s err=%s", code, outBuf.String(), errBuf.String())
	}

	// `upload` & `download`
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "hello.txt")
	dstFile := filepath.Join(tmpDir, "roundtrip.txt")
	_ = os.WriteFile(srcFile, []byte("hello-colabhive"), 0o644)

	outBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "upload", "-s", "cli_test", srcFile, "/content/hello.txt"}, nil, &outBuf, &errBuf, ver)
	if code != 0 {
		t.Fatalf("CLI upload failed: %s", errBuf.String())
	}
	outBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "download", "-s", "cli_test", "/content/hello.txt", dstFile}, nil, &outBuf, &errBuf, ver)
	if code != 0 {
		t.Fatalf("CLI download failed: %s", errBuf.String())
	}
	dlBytes, _ := os.ReadFile(dstFile)
	if string(dlBytes) != "hello-colabhive" {
		t.Fatalf("roundtrip mismatch: %q", string(dlBytes))
	}

	// `url -s cli_test` (raw pipeable URL without `[colab]` prefix)
	outBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "url", "-s", "cli_test"}, nil, &outBuf, &errBuf, ver)
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(outBuf.String()), "https://colab.research.google.com/notebooks/empty.ipynb?dbu=") {
		t.Fatalf("CLI url unexpected output: %q", outBuf.String())
	}

	// `run`: missing script pre-flight MUST exit 2 without allocating VM!
	outBuf.Reset()
	errBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "run", filepath.Join(tmpDir, "nonexistent.py")}, nil, &outBuf, &errBuf, ver)
	if code != 2 {
		t.Fatalf("expected exit code 2 on missing script, got %d", code)
	}

	// `run`: pure script stdout on `out`, lifecycle chatter on `errOut`, and CPython `sys.exit(7)` propagation!
	pyJob := filepath.Join(tmpDir, "job.py")
	_ = os.WriteFile(pyJob, []byte("print('PURE_JSONL_ROW')\n"), 0o644)
	outBuf.Reset()
	errBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "run", "--gpu", "G4", pyJob}, nil, &outBuf, &errBuf, ver)
	if code != 0 || strings.TrimSpace(outBuf.String()) != "PURE_JSONL_ROW" || !strings.Contains(errBuf.String(), "[colab] Session terminated.") {
		t.Fatalf("CLI run stream separation failed (code=%d): stdout=%q stderr=%q", code, outBuf.String(), errBuf.String())
	}

	pyExit7 := filepath.Join(tmpDir, "exit7.py")
	_ = os.WriteFile(pyExit7, []byte("import sys\nsys.exit(7)\n"), 0o644)
	outBuf.Reset()
	errBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "run", "--gpu", "T4", pyExit7}, nil, &outBuf, &errBuf, ver)
	if code != 7 {
		t.Fatalf("expected SystemExit(7) propagation to exit code 7, got %d", code)
	}

	// Verify `colab sessions --json` and `GET /api/v1/sessions?active_only=true` exclude STOPPED sessions from prior `run` calls!
	outBuf.Reset()
	errBuf.Reset()
	code = RunDaemonCLI(ctx, "colab", []string{"--server", ts.URL, "sessions", "--json"}, nil, &outBuf, &errBuf, ver)
	if code != 0 {
		t.Fatalf("colab sessions --json failed: %s", errBuf.String())
	}
	var jsonSessions []models.RuntimeSession
	if err := json.Unmarshal(outBuf.Bytes(), &jsonSessions); err != nil {
		t.Fatalf("failed to parse colab sessions --json output: %v", err)
	}
	for _, sItem := range jsonSessions {
		if sItem.Status == models.SessionStopped || sItem.Status == models.SessionLost {
			t.Fatalf("colab sessions --json leaked dead session %s (%s)", sItem.Name, sItem.Status)
		}
	}
}

func TestAccountVerifyAndSystemSelfTest(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer st.Close()

	hub := newSrvTestHub()
	mockSrv := mockcolab.NewServer()
	defer mockSrv.Close()

	// Create a hermetic local credential file for read-only ScanLocalCredentials verification.
	tmpCredDir := t.TempDir()
	localTokenPath := filepath.Join(tmpCredDir, "token.json")
	_ = os.WriteFile(localTokenPath, []byte(`{
		"client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
		"client_secret": "d-FL95Q19q7MQmFpd7hHD0Ty",
		"refresh_token": "1//0gLocalCliRefreshTokenPreview123456",
		"scopes": ["https://www.googleapis.com/auth/colaboratory", "email"]
	}`), 0o600)

	certsProbe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer certsProbe.Close()

	poolCfg := mockSrv.PoolConfig("test-master-secret")
	poolCfg.LocalScanRoots = []pool.LocalScanCandidate{
		{
			SourceType:  "colab_token_json",
			SourcePath:  localTokenPath,
			Environment: "wsl2",
		},
	}

	poolSvc := pool.NewService(st, hub, poolCfg)
	seeded, err := mockcolab.SeedDemoCluster(ctx, st, poolSvc, mockSrv)
	if err != nil || len(seeded.Accounts) == 0 {
		t.Fatalf("mockcolab.SeedDemoCluster failed: %v", err)
	}

	artDir := t.TempDir()
	rtr := router.NewServiceWithDirs(st, poolSvc, hub, t.TempDir(), "", "", "linux")
	eng := runtime.NewConcreteEngine(st, poolSvc, rtr, hub, runtime.Options{
		DemoMode:               false,
		TunnelBaseURL:          mockSrv.URL,
		PublicAPIBaseURL:       mockSrv.URL,
		ArtifactsDir:           artDir,
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	mcpSrv := mcp.NewServer(poolSvc, eng, rtr, hub)
	srv := New(st, poolSvc, eng, rtr, hub, mcpSrv, Config{
		Version:        "1.0.0-diag-test",
		ArtifactsDir:   artDir,
		GoogleProbeURL: certsProbe.URL,
		DemoMode:       false, // Exercise production diagnostic code path
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	beforeAccounts, _ := st.ListAccounts(ctx)

	// 1. POST /api/v1/system/selftest (0 side effects, read-only local scan)
	respSelf, err := http.Post(ts.URL+"/api/v1/system/selftest", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/v1/system/selftest failed: %v", err)
	}
	if respSelf.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on system selftest, got %d", respSelf.StatusCode)
	}
	var selfRes models.SystemSelfTestResult
	if err := json.NewDecoder(respSelf.Body).Decode(&selfRes); err != nil {
		t.Fatalf("decode SystemSelfTestResult: %v", err)
	}
	_ = respSelf.Body.Close()

	if !selfRes.OverallOK || selfRes.Mode != "production" || selfRes.DiscoveredLocalCount != 1 {
		t.Fatalf("unexpected SystemSelfTestResult: %+v", selfRes)
	}
	if len(selfRes.Steps) != 4 {
		t.Fatalf("expected 4 system selftest steps, got %d", len(selfRes.Steps))
	}
	wantSelfIDs := []string{"sqlite_vault", "google_network", "mcp_router", "local_cli_scanner"}
	for i, wantID := range wantSelfIDs {
		if selfRes.Steps[i].ID != wantID || selfRes.Steps[i].Status != models.VerifyStepOK {
			t.Fatalf("step[%d] mismatch: want (%s, ok), got (%s, %s: %s / %s)",
				i, wantID, selfRes.Steps[i].ID, selfRes.Steps[i].Status, selfRes.Steps[i].Summary, selfRes.Steps[i].Detail)
		}
	}

	// Ensure ScanLocalCredentials inside selftest NEVER auto-imported the local token!
	afterAccounts, _ := st.ListAccounts(ctx)
	if len(afterAccounts) != len(beforeAccounts) {
		t.Fatalf("system selftest mutated accounts count: before=%d after=%d", len(beforeAccounts), len(afterAccounts))
	}

	// Pick a healthy non-demo-id or seeded account and verify both run_kernel_smoke=false and true.
	targetAcc := seeded.Accounts[0]

	// 2. POST /api/v1/accounts/{id}/verify with run_kernel_smoke=false (zero CU cost)
	respVerFast, err := http.Post(
		ts.URL+"/api/v1/accounts/"+targetAcc.ID+"/verify",
		"application/json",
		strings.NewReader(`{"run_kernel_smoke":false}`),
	)
	if err != nil {
		t.Fatalf("verify (fast) failed: %v", err)
	}
	if respVerFast.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on verify, got %d", respVerFast.StatusCode)
	}
	var verFast models.AccountVerifyResult
	if err := json.NewDecoder(respVerFast.Body).Decode(&verFast); err != nil {
		t.Fatalf("decode AccountVerifyResult: %v", err)
	}
	_ = respVerFast.Body.Close()

	if !verFast.OverallOK || verFast.AccountID != targetAcc.ID || len(verFast.Steps) != 4 {
		t.Fatalf("unexpected fast AccountVerifyResult: %+v", verFast)
	}
	if verFast.Steps[0].ID != "egress" || verFast.Steps[0].Status != models.VerifyStepOK ||
		verFast.Steps[1].ID != "oauth" || verFast.Steps[1].Status != models.VerifyStepOK ||
		verFast.Steps[2].ID != "quota" || verFast.Steps[2].Status != models.VerifyStepOK ||
		verFast.Steps[3].ID != "kernel_smoke" || verFast.Steps[3].Status != models.VerifyStepSkipped {
		t.Fatalf("unexpected step statuses on run_kernel_smoke=false: %+v", verFast.Steps)
	}
	if verFast.KernelStdout != "" || verFast.Account == nil {
		t.Fatalf("expected empty kernel_stdout and non-nil account snapshot, got %+v", verFast)
	}

	// 3. POST /api/v1/accounts/{id}/verify with run_kernel_smoke=true (allocates ch-selftest-*, runs Python, tears down)
	respVerSmoke, err := http.Post(
		ts.URL+"/api/v1/accounts/"+targetAcc.ID+"/verify",
		"application/json",
		strings.NewReader(`{"run_kernel_smoke":true}`),
	)
	if err != nil {
		t.Fatalf("verify (smoke) failed: %v", err)
	}
	if respVerSmoke.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on smoke verify, got %d", respVerSmoke.StatusCode)
	}
	var verSmoke models.AccountVerifyResult
	if err := json.NewDecoder(respVerSmoke.Body).Decode(&verSmoke); err != nil {
		t.Fatalf("decode smoke AccountVerifyResult: %v", err)
	}
	_ = respVerSmoke.Body.Close()

	if !verSmoke.OverallOK || len(verSmoke.Steps) != 4 {
		t.Fatalf("smoke AccountVerifyResult failed: %+v", verSmoke)
	}
	for i, stStep := range verSmoke.Steps {
		if stStep.Status != models.VerifyStepOK {
			t.Fatalf("smoke step[%d] (%s) not ok: %s (%s)", i, stStep.ID, stStep.Summary, stStep.Detail)
		}
	}
	if !strings.Contains(verSmoke.KernelStdout, "COLABHIVE_SELFTEST_OK") {
		t.Fatalf("expected COLABHIVE_SELFTEST_OK in kernel_stdout, got %q", verSmoke.KernelStdout)
	}

	// Verify ephemeral ch-selftest-* VM was torn down with ZERO leak!
	activeSessions, err := eng.ListSessions(ctx, models.SessionFilter{ActiveOnly: true})
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	for _, sItem := range activeSessions {
		if strings.HasPrefix(sItem.Name, "ch-selftest-") {
			t.Fatalf("ephemeral selftest session leaked: %+v", sItem)
		}
	}

	// 4. Graceful HTTP 412 concurrency-limit handling during smoke test on a non-demo production account
	//    at Google's MaxVMs limit: NEVER stops the pre-existing session, and returns status="ok"!
	mockSrv.RegisterAccount(mockcolab.MockAccount{
		Email:            "researcher-prod@example.com",
		DisplayName:      "Researcher Production",
		SubscriptionTier: "SUBSCRIPTION_TIER_AI_ULTRA",
		PaidCUBalance:    645.80,
		MaxVMs:           1, // Simulate Google upstream enforcing concurrency cap at 1 active VM
		EligibleGPUs:     []string{"G4", "A100", "T4"},
		AuthCode:         "4/0A_researcher_prod",
	})
	pendingOAuth, err := poolSvc.StartOAuth(ctx, models.OAuthStartRequest{Alias: "Researcher Node"})
	if err != nil {
		t.Fatalf("StartOAuth failed: %v", err)
	}
	liveAcc, err := poolSvc.CompleteOAuth(ctx, pendingOAuth.State, "4/0A_researcher_prod")
	if err != nil {
		t.Fatalf("CompleteOAuth failed: %v", err)
	}
	liveSess, err := eng.CreateSession(ctx, models.SessionAllocRequest{
		Name:            "existing-user-training",
		AccountSelector: liveAcc.ID,
		Accelerator:     models.AcceleratorCPU,
	})
	if err != nil {
		t.Fatalf("CreateSession for pre-existing workload failed: %v", err)
	}
	respVer412, err := http.Post(
		ts.URL+"/api/v1/accounts/"+liveAcc.ID+"/verify",
		"application/json",
		strings.NewReader(`{"run_kernel_smoke":true}`),
	)
	if err != nil {
		t.Fatalf("verify (412 smoke) failed: %v", err)
	}
	var ver412 models.AccountVerifyResult
	if err := json.NewDecoder(respVer412.Body).Decode(&ver412); err != nil {
		t.Fatalf("decode 412 AccountVerifyResult: %v", err)
	}
	_ = respVer412.Body.Close()
	if !ver412.OverallOK || len(ver412.Steps) != 4 || ver412.Steps[3].Status != models.VerifyStepOK {
		t.Fatalf("expected graceful overall_ok=true & step[3]=ok on Google 412 with active VMs, got %+v", ver412)
	}
	if !strings.Contains(ver412.Steps[3].Summary, "该账号当前已有 1 台 Colab 实例正在运行") {
		t.Fatalf("expected human concurrency message in step[3] summary, got %q", ver412.Steps[3].Summary)
	}
	stillRunning, err := eng.GetSession(ctx, liveSess.ID)
	if err != nil || stillRunning.Status == models.SessionStopped {
		t.Fatalf("safety violation: 412 smoke test stopped pre-existing user session: %+v (%v)", stillRunning, err)
	}
	_ = eng.StopSession(ctx, liveSess.ID)

	// 5. Unknown account returns 404
	resp404, err := http.Post(ts.URL+"/api/v1/accounts/acc_does_not_exist/verify", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown account verify, got %d", resp404.StatusCode)
	}

	// 6. POST /api/v1/sessions/sync + PATCH /api/v1/accounts/{id} custom cycle quota & tier wire contract
	extEp, _ := mockSrv.SeedExternalAssignment(
		"researcher-prod@example.com",
		models.AcceleratorG4,
		true,
		"mmlu_pro_eval.ipynb",
		"busy",
		map[string][]byte{
			"worker.log": []byte("step=10 loss=0.33\nstep=20 loss=0.29\n"),
		},
	)
	respSync, err := http.Post(ts.URL+"/api/v1/sessions/sync", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/v1/sessions/sync failed: %v", err)
	}
	if respSync.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on POST /api/v1/sessions/sync, got %d", respSync.StatusCode)
	}
	var syncPayload models.SessionSyncResult
	if err := json.NewDecoder(respSync.Body).Decode(&syncPayload); err != nil {
		t.Fatalf("decode SessionSyncResult: %v", err)
	}
	_ = respSync.Body.Close()
	if syncPayload.AdoptedCount != 1 || syncPayload.ActiveCount < 1 {
		t.Fatalf("expected adopted_count=1 active_count>=1, got %+v", syncPayload)
	}
	foundExt := false
	for _, sItem := range syncPayload.Sessions {
		if sItem != nil && sItem.Endpoint == extEp && sItem.AdoptedExternal && len(sItem.RecentLogs) == 2 {
			foundExt = true
		}
	}
	if !foundExt {
		t.Fatalf("expected synced external session %s with RecentLogs in POST /api/v1/sessions/sync response", extEp)
	}

	patchReq, _ := http.NewRequest(
		http.MethodPatch,
		ts.URL+"/api/v1/accounts/"+liveAcc.ID,
		strings.NewReader(`{"subscription_tier":"AI_ULTRA","reference_cycle_cu":2500}`),
	)
	patchReq.Header.Set("Content-Type", "application/json")
	respPatch, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("PATCH /api/v1/accounts/{id} failed: %v", err)
	}
	if respPatch.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on PATCH account, got %d", respPatch.StatusCode)
	}
	var patchedAcc models.Account
	if err := json.NewDecoder(respPatch.Body).Decode(&patchedAcc); err != nil {
		t.Fatalf("decode patched account: %v", err)
	}
	_ = respPatch.Body.Close()
	if patchedAcc.SubscriptionTier != models.TierAIUltra || patchedAcc.ReferenceCycleCU != 2500 || !patchedAcc.CustomCycleCU {
		t.Fatalf("unexpected patched account: tier=%s ref=%.2f custom=%v",
			patchedAcc.SubscriptionTier, patchedAcc.ReferenceCycleCU, patchedAcc.CustomCycleCU)
	}
	mockSrv.UnassignExternalEndpoint(extEp)
}

func TestAgentInjector2026FormatsDaemonInfoAndDesktopBridge(t *testing.T) {
	ctx := context.Background()
	st := newSrvMemStore()
	hub := newSrvTestHub()
	tmpHome := t.TempDir()
	tmpAppData := filepath.Join(tmpHome, "AppData", "Roaming")

	// Pre-populate an existing OpenCode `opencode.json` (WITHOUT `opencode.jsonc`) with 2 user MCP servers!
	openCodeJSONPath := filepath.Join(tmpHome, ".config", "opencode", "opencode.json")
	_ = os.MkdirAll(filepath.Dir(openCodeJSONPath), 0o755)
	_ = os.WriteFile(openCodeJSONPath, []byte(`{
  "model": "anthropic/claude-sonnet-4-6",
  "mcp": {
    "filesystem": { "type": "local", "command": ["npx", "-y", "fs"], "enabled": true },
    "ripgrep": { "type": "remote", "url": "http://127.0.0.1:9100/mcp", "enabled": true }
  }
}`), 0o600)

	// Create fake `colabhive.exe` and `colabhive-desktop.exe` binaries to test `ResolveStdioCommand` & `stdio` injection!
	fakeBinDir := filepath.Join(t.TempDir(), "bin")
	_ = os.MkdirAll(fakeBinDir, 0o755)
	fakeCLIExe := filepath.Join(fakeBinDir, "colabhive.exe")
	fakeDesktopExe := filepath.Join(fakeBinDir, "colabhive-desktop.exe")
	_ = os.WriteFile(fakeCLIExe, []byte("MZ"), 0o755)
	_ = os.WriteFile(fakeDesktopExe, []byte("MZ"), 0o755)
	if canonCLI, cErr := filepath.EvalSymlinks(fakeCLIExe); cErr == nil && canonCLI != "" {
		fakeCLIExe = canonCLI
	}

	if got := router.ResolveStdioCommand(fakeDesktopExe, false); got != fakeCLIExe {
		t.Fatalf("expected colabhive-desktop.exe to resolve sibling colabhive.exe (%s), got %s", fakeCLIExe, got)
	}
	if got := router.ResolveStdioCommand(fakeCLIExe, true); got != "colabhive" {
		t.Fatalf("expected WSL2 stdio command fallback to 'colabhive', got %s", got)
	}
	if got := router.ResolveStdioCommand(filepath.Join(fakeBinDir, "router.test.exe"), false); got != "colabhive" {
		t.Fatalf("expected .test.exe binary fallback to 'colabhive', got %s", got)
	}

	t.Setenv("COLABHIVE_BIN_PATH", fakeDesktopExe)

	rtr := router.NewServiceWithDirs(st, nil, hub, tmpHome, tmpAppData, "", "windows")
	eng := runtime.NewConcreteEngine(st, nil, rtr, hub, runtime.Options{
		DemoMode:               true,
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	mcpSrv := mcp.NewServer(nil, eng, rtr, hub)
	srv := New(st, nil, eng, rtr, hub, mcpSrv, Config{
		Version:   "1.0.0-injector-test",
		PublicURL: "http://127.0.0.1:7860",
		DemoMode:  true,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Verify `/api/v1/agents/clients` strips broken `colabhive://` DeepLinkURI and sanitizes placeholder keys!
	respClients, err := http.Get(ts.URL + "/api/v1/agents/clients?api_key=ch_sk_live_default")
	if err != nil || respClients.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/agents/clients failed: err=%v resp=%+v", err, respClients)
	}
	var clientList []models.AgentClientConfigStatus
	_ = json.NewDecoder(respClients.Body).Decode(&clientList)
	_ = respClients.Body.Close()
	if len(clientList) != 8 {
		t.Fatalf("expected 8 agent client configs, got %d", len(clientList))
	}
	for _, c := range clientList {
		if strings.HasPrefix(c.DeepLinkURI, "colabhive://") {
			t.Fatalf("client %s still emits unregistered colabhive:// deep link: %s", c.ClientID, c.DeepLinkURI)
		}
		if strings.Contains(c.Snippet, "ch_sk_live_default") || strings.Contains(c.Snippet, "bearer_token_env_var") {
			t.Fatalf("client %s snippet leaked placeholder key or broken env var: %s", c.ClientID, c.Snippet)
		}
		if c.ClientID == models.AgentCursor && !strings.HasPrefix(c.DeepLinkURI, "cursor://anysphere.cursor-deeplink/mcp/install") {
			t.Fatalf("expected Cursor to expose registered cursor:// deep link, got %q", c.DeepLinkURI)
		}
	}

	// 2. Placeholder key injection (`ch_sk_live_8f9a..._live_token` / `ch_sk_live_default`) MUST write keyless open-access configs!
	injectReq := func(cid models.AgentClientID, apiKey string, transport string) models.AgentClientConfigStatus {
		t.Helper()
		b, _ := json.Marshal(models.AgentInjectRequest{
			ClientID:      cid,
			ServerBaseURL: "http://127.0.0.1:7860",
			APIKey:        apiKey,
			Transport:     transport,
		})
		r, pErr := http.Post(ts.URL+"/api/v1/agents/inject", "application/json", bytes.NewReader(b))
		if pErr != nil || r.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/v1/agents/inject (%s) failed: err=%v resp=%+v", cid, pErr, r)
		}
		var out models.AgentClientConfigStatus
		_ = json.NewDecoder(r.Body).Decode(&out)
		_ = r.Body.Close()
		return out
	}

	codexKeyless := injectReq(models.AgentCodex, "ch_sk_live_8f9a..._live_token", "http")
	codexRaw, _ := os.ReadFile(codexKeyless.ConfigPath)
	if strings.Contains(string(codexRaw), "bearer_token_env_var") ||
		strings.Contains(string(codexRaw), "http_headers") ||
		strings.Contains(string(codexRaw), "_live_token") ||
		!strings.Contains(string(codexRaw), `url = "http://127.0.0.1:7860/mcp"`) ||
		!strings.Contains(string(codexRaw), `tool_timeout_sec = 1800`) {
		t.Fatalf("unexpected keyless Codex config.toml:\n%s", string(codexRaw))
	}

	// Inject Codex WITH a real minted key -> `http_headers = { Authorization = "Bearer ..." }`, NO `?key=`, NO `bearer_token_env_var`!
	codexAuthed := injectReq(models.AgentCodex, "ch_sk_live_real987654", "http")
	codexAuthRaw, _ := os.ReadFile(codexAuthed.ConfigPath)
	if strings.Contains(string(codexAuthRaw), "bearer_token_env_var") ||
		strings.Contains(string(codexAuthRaw), "?key=") ||
		!strings.Contains(string(codexAuthRaw), `http_headers = { Authorization = "Bearer ch_sk_live_real987654" }`) ||
		!strings.Contains(string(codexAuthRaw), `tool_timeout_sec = 1800`) {
		t.Fatalf("unexpected authenticated Codex config.toml:\n%s", string(codexAuthRaw))
	}

	// 3. OpenCode MUST target existing `opencode.json` in place without creating a shadowing `opencode.jsonc`!
	ocStatus := injectReq(models.AgentOpenCode, "ch_sk_live_real987654", "http")
	if ocStatus.ConfigPath != openCodeJSONPath {
		t.Fatalf("expected OpenCode to target existing opencode.json (%s), got %s", openCodeJSONPath, ocStatus.ConfigPath)
	}
	if _, statErr := os.Stat(filepath.Join(tmpHome, ".config", "opencode", "opencode.jsonc")); !os.IsNotExist(statErr) {
		t.Fatalf("OpenCode injection created shadowing opencode.jsonc next to existing opencode.json!")
	}
	ocUpdatedBytes, _ := os.ReadFile(openCodeJSONPath)
	if !strings.Contains(string(ocUpdatedBytes), `"filesystem"`) ||
		!strings.Contains(string(ocUpdatedBytes), `"ripgrep"`) ||
		!strings.Contains(string(ocUpdatedBytes), `"colabhive"`) {
		t.Fatalf("OpenCode opencode.json lost sibling servers or colabhive: %s", string(ocUpdatedBytes))
	}

	// 4. `stdio` mode on Claude Desktop & Claude Code resolves absolute `fakeCLIExe` and sets `"type": "stdio"` on Claude Code!
	cdStatus := injectReq(models.AgentClaudeDesktop, "ch_sk_live_default", "stdio")
	cdBytes, _ := os.ReadFile(cdStatus.ConfigPath)
	var cdDoc map[string]map[string]map[string]any
	_ = json.Unmarshal(cdBytes, &cdDoc)
	if cdDoc["mcpServers"]["colabhive"]["command"] != fakeCLIExe {
		t.Fatalf("expected Claude Desktop command=%q, got %+v", fakeCLIExe, cdDoc["mcpServers"]["colabhive"])
	}

	ccStdioStatus := injectReq(models.AgentClaudeCode, "ch_sk_live_real987654", "stdio")
	ccStdioBytes, _ := os.ReadFile(ccStdioStatus.ConfigPath)
	var ccDoc map[string]map[string]map[string]any
	_ = json.Unmarshal(ccStdioBytes, &ccDoc)
	if ccDoc["mcpServers"]["colabhive"]["command"] != fakeCLIExe || ccDoc["mcpServers"]["colabhive"]["type"] != "stdio" {
		t.Fatalf("expected Claude Code stdio command=%q and type=stdio, got %+v", fakeCLIExe, ccDoc["mcpServers"]["colabhive"])
	}

	// 5. Antigravity, Windsurf, Gemini CLI exact 2026 schemas (no `?key=` leak, Gemini CLI timeout=1800000, Windsurf headers)!
	agyStatus := injectReq(models.AgentAntigravity, "ch_sk_live_real987654", "http")
	agyBytes, _ := os.ReadFile(agyStatus.ConfigPath)
	if strings.Contains(string(agyBytes), "?key=") || !strings.Contains(string(agyBytes), `"url": "http://127.0.0.1:7860/mcp"`) {
		t.Fatalf("Antigravity config leaked ?key= or missed clean url:\n%s", string(agyBytes))
	}

	wsStatus := injectReq(models.AgentWindsurf, "ch_sk_live_real987654", "http")
	wsBytes, _ := os.ReadFile(wsStatus.ConfigPath)
	if strings.Contains(string(wsBytes), "?key=") ||
		!strings.Contains(string(wsBytes), `"serverUrl": "http://127.0.0.1:7860/mcp"`) ||
		!strings.Contains(string(wsBytes), `"Authorization": "Bearer ch_sk_live_real987654"`) {
		t.Fatalf("Windsurf config missing headers or leaked ?key=:\n%s", string(wsBytes))
	}

	gemStatus := injectReq(models.AgentGeminiCLI, "", "http")
	gemBytes, _ := os.ReadFile(gemStatus.ConfigPath)
	if !strings.Contains(string(gemBytes), `"timeout": 1800000`) {
		t.Fatalf("expected Gemini CLI timeout 1800000 (30 min), got:\n%s", string(gemBytes))
	}

	// 6. DaemonInfo (`WriteDaemonInfoTo` / `ReadDaemonInfoFrom`) preserves real `db_path` across `:memory:` runs!
	daemonFile := filepath.Join(t.TempDir(), "daemon.json")
	prodDB := filepath.Join(tmpHome, "data", "colabhive_prod.db")
	if err := WriteDaemonInfoTo(daemonFile, DaemonInfo{
		URL:    "http://127.0.0.1:17860",
		PID:    4242,
		DBPath: prodDB,
	}); err != nil {
		t.Fatalf("WriteDaemonInfoTo failed: %v", err)
	}
	// Subsequent `:memory:` write updates URL/PID without clobbering `colabhive_prod.db`!
	if err := WriteDaemonInfoTo(daemonFile, DaemonInfo{
		URL:    "http://127.0.0.1:17861",
		PID:    4343,
		DBPath: ":memory:",
	}); err != nil {
		t.Fatalf("WriteDaemonInfoTo (:memory:) failed: %v", err)
	}
	readInfo, err := ReadDaemonInfoFrom(daemonFile)
	if err != nil || readInfo.URL != "http://127.0.0.1:17861" || readInfo.DBPath != prodDB {
		t.Fatalf("unexpected DaemonInfo roundtrip: err=%v info=%+v", err, readInfo)
	}

	// 7. Desktop Bridge (`GET /api/v1/desktop/state` + `POST /api/v1/desktop/action` + `SetDesktopBridge`)
	var bridgeMu sync.Mutex
	curDesktop := DesktopState{
		DesktopShell:      true,
		AutoStartEnabled:  false,
		MainWindowVisible: false,
		TrayVisible:       true,
	}
	srv.SetDesktopBridge(func(_ context.Context, req DesktopActionRequest) (DesktopState, error) {
		bridgeMu.Lock()
		defer bridgeMu.Unlock()
		switch req.Action {
		case "open-main", "open-route":
			curDesktop.MainWindowVisible = true
			curDesktop.TrayVisible = false
		case "hide-tray":
			curDesktop.TrayVisible = false
		case "toggle-autostart":
			curDesktop.AutoStartEnabled = !curDesktop.AutoStartEnabled
		case "quit":
			curDesktop.MainWindowVisible = false
			curDesktop.TrayVisible = false
		}
		curDesktop.LastAction = req.Action
		return curDesktop, nil
	})

	actResp, err := http.Post(
		ts.URL+"/api/v1/desktop/action",
		"application/json",
		strings.NewReader(`{"action":"toggle-autostart"}`),
	)
	if err != nil || actResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/desktop/action failed: err=%v resp=%+v", err, actResp)
	}
	_ = actResp.Body.Close()

	actOpenResp, err := http.Post(
		ts.URL+"/api/v1/desktop/action",
		"application/json",
		strings.NewReader(`{"action":"open-main","route":"/router"}`),
	)
	if err != nil || actOpenResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/desktop/action (open-main) failed: err=%v resp=%+v", err, actOpenResp)
	}
	_ = actOpenResp.Body.Close()

	stateResp, err := http.Get(ts.URL + "/api/v1/desktop/state")
	if err != nil || stateResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/desktop/state failed: err=%v resp=%+v", err, stateResp)
	}
	var gotState DesktopState
	_ = json.NewDecoder(stateResp.Body).Decode(&gotState)
	_ = stateResp.Body.Close()
	if !gotState.DesktopShell || !gotState.AutoStartEnabled || !gotState.MainWindowVisible || gotState.TrayVisible {
		t.Fatalf("unexpected DesktopState after actions: %+v", gotState)
	}
	_ = ctx
}

func TestCloudModeSecurityPrivilegeSeparationTrustProxyRateLimitAndLocalConnectCLI(t *testing.T) {
	ctx := context.Background()
	st := newSrvMemStore()
	hub := newSrvTestHub()
	artDir := t.TempDir()
	tmpServerHome := t.TempDir()

	rtr := router.NewServiceWithDirs(st, nil, hub, tmpServerHome, "", "", "linux")
	eng := runtime.NewConcreteEngine(st, nil, rtr, hub, runtime.Options{
		DemoMode:               true,
		ArtifactsDir:           artDir,
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	if err := SeedDemoCluster(ctx, st, rtr, artDir, "https://colab.example.com"); err != nil {
		t.Fatalf("SeedDemoCluster failed: %v", err)
	}

	// Seed an AdoptedExternal session + an Admin-owned session to verify session isolation!
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:              "sess_ext_user",
		Name:            "external-notebook",
		AccountID:       "acc_demo_ultra",
		AccountEmail:    "ultra@colabhive.demo",
		Accelerator:     models.AcceleratorG4,
		Status:          models.SessionBusy,
		AdoptedExternal: true,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})

	keyA, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:             "Laptop-Agent-A",
		DefaultStrategy:  models.StrategyWasteZero,
		MaxBudgetCUDaily: 100,
		MaxBudgetCUTotal: 500,
		MaxConcurrentVMs: 4,
	})
	if err != nil || keyA.RawKey == "" {
		t.Fatalf("CreateAPIKey A failed: %v", err)
	}
	keyB, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:             "Laptop-Agent-B",
		DefaultStrategy:  models.StrategyWasteZero,
		MaxBudgetCUDaily: 100,
		MaxBudgetCUTotal: 500,
		MaxConcurrentVMs: 4,
	})
	if err != nil || keyB.RawKey == "" {
		t.Fatalf("CreateAPIKey B failed: %v", err)
	}

	const adminSecret = "cloud-admin-super-secret-2026"

	mcpSrv := mcp.NewServer(nil, eng, rtr, hub)
	cloudSrvUntrustedProxy := New(st, nil, eng, rtr, hub, mcpSrv, Config{
		Version:               "1.0.0-cloud",
		Mode:                  "cloud",
		TrustProxy:            false,
		AdminToken:            adminSecret,
		ArtifactsDir:          artDir,
		DemoMode:              true,
		PortalRateLimitPerMin: 5,
		AuthFailLimitPerMin:   4,
	})
	tsUntrusted := httptest.NewServer(cloudSrvUntrustedProxy.Handler())
	defer tsUntrusted.Close()

	doReq := func(baseURL, method, path, bearer, body string, extraHeaders map[string]string) (*http.Response, []byte) {
		t.Helper()
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req, rErr := http.NewRequest(method, baseURL+path, rdr)
		if rErr != nil {
			t.Fatalf("NewRequest failed: %v", rErr)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		resp, dErr := http.DefaultClient.Do(req)
		if dErr != nil {
			t.Fatalf("HTTP %s %s failed: %v", method, path, dErr)
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		return resp, buf.Bytes()
	}

	// 1. Unauthenticated `GET /api/v1/health` in cloud mode:
	//    - Returns 200 OK with deployment_mode="cloud", auth_required=true, redacted operational counts (0),
	//    - Ignores spoofed X-Forwarded-Proto/Host when TrustProxy=false!
	hResp, hBody := doReq(tsUntrusted.URL, http.MethodGet, "/api/v1/health", "", "", map[string]string{
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "spoofed.attacker.evil",
	})
	if hResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on unauthenticated /api/v1/health, got %d: %s", hResp.StatusCode, string(hBody))
	}
	var unauthHealth models.DaemonHealthStatus
	if err := json.Unmarshal(hBody, &unauthHealth); err != nil {
		t.Fatalf("decode unauth health: %v", err)
	}
	if !unauthHealth.Healthy ||
		unauthHealth.DeploymentMode != "cloud" ||
		!unauthHealth.AuthRequired ||
		unauthHealth.ActiveSessions != 0 ||
		unauthHealth.ActiveExperiments != 0 ||
		unauthHealth.ConnectedWSClients != 0 {
		t.Fatalf("expected redacted cloud health on unauthenticated probe, got %+v", unauthHealth)
	}
	if strings.Contains(unauthHealth.PublicURL, "spoofed.attacker.evil") || strings.HasPrefix(unauthHealth.PublicURL, "https://") {
		t.Fatalf("TrustProxy=false leaked spoofed X-Forwarded-Proto/Host in public_url: %q", unauthHealth.PublicURL)
	}

	// Authenticated `GET /api/v1/health` returns real operational counts!
	hAuthResp, hAuthBody := doReq(tsUntrusted.URL, http.MethodGet, "/api/v1/health", adminSecret, "", nil)
	if hAuthResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on authenticated /api/v1/health, got %d", hAuthResp.StatusCode)
	}
	var authHealth models.DaemonHealthStatus
	_ = json.Unmarshal(hAuthBody, &authHealth)
	if authHealth.ActiveSessions < 4 {
		t.Fatalf("expected live ActiveSessions >= 4 when authenticated with admin token, got %+v", authHealth)
	}

	// 2. Cloud mode returns 403 Forbidden on host-FS & desktop-only endpoints EVEN with valid AdminToken!
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/import/scan-local", ""},
		{http.MethodPost, "/api/v1/agents/inject", `{"client_id":"cursor"}`},
		{http.MethodDelete, "/api/v1/agents/inject/cursor", ""},
		{http.MethodPost, "/api/v1/desktop/action", `{"action":"open-main"}`},
	} {
		resp, raw := doReq(tsUntrusted.URL, tc.method, tc.path, adminSecret, tc.body, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden in cloud mode on %s %s, got %d: %s", tc.method, tc.path, resp.StatusCode, string(raw))
		}
	}

	// 3. `ch_sk_...` Privilege Separation:
	//    - Allowed (200 OK): `/api/v1/pool/summary`, `/api/v1/pool/quota`, `/api/v1/quota`, `/mcp` (`tools/list`)
	for _, okPath := range []string{"/api/v1/pool/summary", "/api/v1/pool/quota", "/api/v1/quota"} {
		resp, raw := doReq(tsUntrusted.URL, http.MethodGet, okPath, keyA.RawKey, "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for ch_sk_ key on %s, got %d: %s", okPath, resp.StatusCode, string(raw))
		}
	}
	mcpResp, mcpBody := doReq(
		tsUntrusted.URL,
		http.MethodPost,
		"/mcp",
		keyA.RawKey,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		nil,
	)
	if mcpResp.StatusCode != http.StatusOK || !strings.Contains(string(mcpBody), "execute_code") {
		t.Fatalf("expected 200 OK on POST /mcp with ch_sk_ key, got %d: %s", mcpResp.StatusCode, string(mcpBody))
	}

	//    - Forbidden (403) for `ch_sk_...` on ALL Admin/Vault/Account/Proxy/Key/Policy/Injector/Desktop/Sync routes!
	for _, adminOnly := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/keys", ""},
		{http.MethodPost, "/api/v1/keys", `{"name":"escalate"}`},
		{http.MethodGet, "/api/v1/accounts", ""},
		{http.MethodDelete, "/api/v1/accounts/acc_demo_ultra", ""},
		{http.MethodPost, "/api/v1/pool/export", `{"include_proxies":true}`},
		{http.MethodPost, "/api/v1/pool/import", `{"format":"raw_json","raw_payload":"{}"}`},
		{http.MethodGet, "/api/v1/proxies", ""},
		{http.MethodGet, "/api/v1/router/policy", ""},
		{http.MethodPut, "/api/v1/router/policy", `{"default_strategy":"round-robin"}`},
		{http.MethodGet, "/api/v1/agents/clients", ""},
		{http.MethodPost, "/api/v1/agents/inject", `{"client_id":"cursor"}`},
		{http.MethodPost, "/api/v1/desktop/action", `{"action":"open-main"}`},
		{http.MethodPost, "/api/v1/sessions/sync", `{}`},
		{http.MethodPost, "/api/v1/system/selftest", `{}`},
	} {
		resp, raw := doReq(tsUntrusted.URL, adminOnly.method, adminOnly.path, keyA.RawKey, adminOnly.body, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("privilege escalation leak: ch_sk_ key got %d (want 403) on %s %s: %s",
				resp.StatusCode, adminOnly.method, adminOnly.path, string(raw))
		}
	}

	//    - Session Ownership Isolation between KeyA, KeyB, and AdoptedExternal sessions!
	sessCreateResp, sessCreateBody := doReq(
		tsUntrusted.URL,
		http.MethodPost,
		"/api/v1/sessions",
		keyA.RawKey,
		`{"name":"agent-a-vm","gpu":"G4"}`,
		nil,
	)
	if sessCreateResp.StatusCode != http.StatusOK {
		t.Fatalf("KeyA POST /api/v1/sessions failed (%d): %s", sessCreateResp.StatusCode, string(sessCreateBody))
	}
	var sessA models.RuntimeSession
	_ = json.Unmarshal(sessCreateBody, &sessA)

	// KeyA can view & execute on its own session, and list returns ONLY KeyA's session!
	listAResp, listABody := doReq(tsUntrusted.URL, http.MethodGet, "/api/v1/sessions", keyA.RawKey, "", nil)
	if listAResp.StatusCode != http.StatusOK {
		t.Fatalf("KeyA GET /api/v1/sessions failed: %d", listAResp.StatusCode)
	}
	var keyASessions []models.RuntimeSession
	_ = json.Unmarshal(listABody, &keyASessions)
	if len(keyASessions) != 1 || keyASessions[0].ID != sessA.ID {
		t.Fatalf("expected KeyA session list to contain ONLY %s, got %+v", sessA.ID, keyASessions)
	}

	// KeyB CANNOT view, exec on, or delete KeyA's session OR AdoptedExternal session!
	for _, forbiddenSessID := range []string{sessA.ID, "sess_ext_user"} {
		gResp, _ := doReq(tsUntrusted.URL, http.MethodGet, "/api/v1/sessions/"+forbiddenSessID, keyB.RawKey, "", nil)
		if gResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 when KeyB inspects %s, got %d", forbiddenSessID, gResp.StatusCode)
		}
		eResp, _ := doReq(tsUntrusted.URL, http.MethodPost, "/api/v1/sessions/"+forbiddenSessID+"/exec", keyB.RawKey, `{"code":"print(1)"}`, nil)
		if eResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 when KeyB executes on %s, got %d", forbiddenSessID, eResp.StatusCode)
		}
		dResp, _ := doReq(tsUntrusted.URL, http.MethodDelete, "/api/v1/sessions/"+forbiddenSessID, keyB.RawKey, "", nil)
		if dResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 when KeyB deletes %s, got %d", forbiddenSessID, dResp.StatusCode)
		}
	}

	// 4. `--trust-proxy` == true honors X-Forwarded-Proto, X-Forwarded-Host, and X-Forwarded-For!
	cloudSrvTrustedProxy := New(st, nil, eng, rtr, hub, mcpSrv, Config{
		Version:               "1.0.0-cloud",
		Mode:                  "cloud",
		TrustProxy:            true,
		AdminToken:            adminSecret,
		ArtifactsDir:          artDir,
		DemoMode:              true,
		PortalRateLimitPerMin: 3,
		AuthFailLimitPerMin:   3,
	})
	tsTrusted := httptest.NewServer(cloudSrvTrustedProxy.Handler())
	defer tsTrusted.Close()

	proxyHdrs := map[string]string{
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "hive.research-cloud.io",
		"X-Forwarded-For":   "198.51.100.77, 10.0.0.2",
	}
	thResp, thBody := doReq(tsTrusted.URL, http.MethodGet, "/api/v1/health", "", "", proxyHdrs)
	if thResp.StatusCode != http.StatusOK {
		t.Fatalf("trusted proxy health failed: %d", thResp.StatusCode)
	}
	var trustedHealth models.DaemonHealthStatus
	_ = json.Unmarshal(thBody, &trustedHealth)
	if trustedHealth.PublicURL != "https://hive.research-cloud.io" {
		t.Fatalf("expected PublicURL='https://hive.research-cloud.io' with TrustProxy=true, got %q", trustedHealth.PublicURL)
	}

	clientsResp, clientsRaw := doReq(
		tsTrusted.URL,
		http.MethodGet,
		"/api/v1/agents/clients?api_key="+keyA.RawKey,
		adminSecret,
		"",
		proxyHdrs,
	)
	if clientsResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/agents/clients in cloud mode failed (%d): %s", clientsResp.StatusCode, string(clientsRaw))
	}
	var cloudClients []struct {
		models.AgentClientConfigStatus
		SupportsAutoInject bool `json:"supports_auto_inject"`
		CloudMode          bool `json:"cloud_mode"`
	}
	if err := json.Unmarshal(clientsRaw, &cloudClients); err != nil || len(cloudClients) != 8 {
		t.Fatalf("decode cloud clients failed: err=%v len=%d", err, len(cloudClients))
	}
	for _, cc := range cloudClients {
		if cc.SupportsAutoInject || !cc.CloudMode {
			t.Fatalf("expected supports_auto_inject=false & cloud_mode=true for %s, got %+v", cc.ClientID, cc)
		}
		if !strings.Contains(cc.Snippet, "https://hive.research-cloud.io/mcp") || !strings.Contains(cc.Snippet, keyA.RawKey) {
			t.Fatalf("expected remote https://hive.research-cloud.io/mcp + Bearer key in %s snippet, got:\n%s", cc.ClientID, cc.Snippet)
		}
	}

	// 5. Brute-force 401 -> 429 lockout & Contributor Portal 429 rate limiting!
	for i := 1; i <= 3; i++ {
		badResp, _ := doReq(tsTrusted.URL, http.MethodGet, "/api/v1/accounts", "wrong-token-attempt", "", map[string]string{
			"X-Forwarded-For": "203.0.113.55",
		})
		if badResp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401 Unauthorized before lockout, got %d", i, badResp.StatusCode)
		}
	}
	lockedResp, _ := doReq(tsTrusted.URL, http.MethodGet, "/api/v1/accounts", "wrong-token-attempt", "", map[string]string{
		"X-Forwarded-For": "203.0.113.55",
	})
	if lockedResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests after exceeding AuthFailLimitPerMin, got %d", lockedResp.StatusCode)
	}
	// A different client IP behind the trusted proxy is unaffected!
	otherIPResp, _ := doReq(tsTrusted.URL, http.MethodGet, "/api/v1/accounts", adminSecret, "", map[string]string{
		"X-Forwarded-For": "203.0.113.99",
	})
	if otherIPResp.StatusCode != http.StatusOK {
		t.Fatalf("expected independent IP 203.0.113.99 to succeed (200), got %d", otherIPResp.StatusCode)
	}

	// Public Contributor Portal rate limit (`PortalRateLimitPerMin: 3`)
	for i := 1; i <= 3; i++ {
		pResp, _ := doReq(tsTrusted.URL, http.MethodGet, "/api/public/contribute/ch_inv_demo2026", "", "", map[string]string{
			"X-Forwarded-For": "192.0.2.88",
		})
		if pResp.StatusCode != http.StatusOK {
			t.Fatalf("portal req %d: expected 200, got %d", i, pResp.StatusCode)
		}
	}
	pRateResp, _ := doReq(tsTrusted.URL, http.MethodGet, "/api/public/contribute/ch_inv_demo2026", "", "", map[string]string{
		"X-Forwarded-For": "192.0.2.88",
	})
	if pRateResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests on public portal burst, got %d", pRateResp.StatusCode)
	}

	// 6. Laptop-to-Cloud `colabhive connect` & `colabhive disconnect` CLI writes locally against a temp home dir
	//    WITHOUT hitting the remote server's disabled `/api/v1/agents/inject` endpoint!
	laptopHome := t.TempDir()
	cursorCfgPath := filepath.Join(laptopHome, ".cursor", "mcp.json")
	_ = os.MkdirAll(filepath.Dir(cursorCfgPath), 0o755)
	_ = os.WriteFile(cursorCfgPath, []byte(`{"mcpServers":{"existing-tool":{"url":"http://localhost:9000/mcp"}}}`), 0o600)

	var cliOut, cliErr bytes.Buffer
	ver := VersionInfo{Version: "1.0.0"}
	code := RunDaemonCLI(
		ctx,
		"colabhive",
		[]string{
			"connect", "cursor",
			"--server", "https://hive.research-cloud.io",
			"--key", keyA.RawKey,
			"--transport", "http",
			"--home", laptopHome,
		},
		nil,
		&cliOut,
		&cliErr,
		ver,
	)
	if code != 0 {
		t.Fatalf("colabhive connect cursor failed (code=%d): out=%s err=%s", code, cliOut.String(), cliErr.String())
	}
	cursorRaw, err := os.ReadFile(cursorCfgPath)
	if err != nil {
		t.Fatalf("read injected cursor mcp.json: %v", err)
	}
	if !strings.Contains(string(cursorRaw), `"existing-tool"`) ||
		!strings.Contains(string(cursorRaw), `"https://hive.research-cloud.io/mcp"`) ||
		!strings.Contains(string(cursorRaw), `"Bearer `+keyA.RawKey+`"`) {
		t.Fatalf("unexpected laptop cursor mcp.json:\n%s", string(cursorRaw))
	}

	// Also connect Codex (`config.toml`) locally!
	cliOut.Reset()
	cliErr.Reset()
	code = RunDaemonCLI(
		ctx,
		"colabhive",
		[]string{
			"connect", "codex",
			"--server", "https://hive.research-cloud.io",
			"--key", keyA.RawKey,
			"--home", laptopHome,
		},
		nil,
		&cliOut,
		&cliErr,
		ver,
	)
	if code != 0 {
		t.Fatalf("colabhive connect codex failed (code=%d): %s", code, cliErr.String())
	}
	codexRaw, err := os.ReadFile(filepath.Join(laptopHome, ".codex", "config.toml"))
	if err != nil || !strings.Contains(string(codexRaw), `url = "https://hive.research-cloud.io/mcp"`) || !strings.Contains(string(codexRaw), keyA.RawKey) {
		t.Fatalf("unexpected laptop codex config.toml (err=%v):\n%s", err, string(codexRaw))
	}

	// `colabhive disconnect cursor` removes colabhive while preserving `existing-tool`!
	cliOut.Reset()
	cliErr.Reset()
	code = RunDaemonCLI(ctx, "colabhive", []string{"disconnect", "cursor", "--home", laptopHome}, nil, &cliOut, &cliErr, ver)
	if code != 0 {
		t.Fatalf("colabhive disconnect cursor failed (code=%d): %s", code, cliErr.String())
	}
	cursorAfterDisconnect, _ := os.ReadFile(cursorCfgPath)
	if !strings.Contains(string(cursorAfterDisconnect), `"existing-tool"`) || strings.Contains(string(cursorAfterDisconnect), `"colabhive"`) {
		t.Fatalf("disconnect failed to preserve sibling or remove colabhive:\n%s", string(cursorAfterDisconnect))
	}
}

func TestArtifactTraversalRESTHostFSPortalInviteWSTicketOriginAndInjectorTOML(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	artDir := filepath.Join(dataRoot, "artifacts")
	_ = os.MkdirAll(artDir, 0o755)

	// Place a fake vault DB sibling `<dataRoot>/colabhive.db` right next to `<dataRoot>/artifacts`
	// to reproduce the exact P0-1 traversal layout!
	fakeVaultDB := filepath.Join(dataRoot, "colabhive.db")
	const vaultSentinel = "SQLite format 3\x00SUPER_SECRET_REFRESH_TOKEN"
	_ = os.WriteFile(fakeVaultDB, []byte(vaultSentinel), 0o600)

	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	hub := newSrvTestHub()
	mockSrv := mockcolab.NewServer()
	defer mockSrv.Close()

	poolCfg := mockSrv.PoolConfig("unit-test-master-secret-32-bytes!!")
	poolSvc := pool.NewService(st, hub, poolCfg)
	if _, sErr := mockcolab.SeedDemoCluster(ctx, st, poolSvc, mockSrv); sErr != nil {
		t.Fatal(sErr)
	}
	rtr := router.NewServiceWithDirs(st, poolSvc, hub, t.TempDir(), "", "", "linux")
	eng := runtime.NewConcreteEngine(st, poolSvc, rtr, hub, runtime.Options{
		DemoMode:               true,
		ArtifactsDir:           artDir,
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	if err := SeedDemoCluster(ctx, st, rtr, artDir, "https://cloud.colabhive.example"); err != nil {
		t.Fatal(err)
	}

	const adminTok = "adm-hardened-2026"
	mcpSrv := mcp.NewServer(poolSvc, eng, rtr, hub)
	cloudSrv := New(st, poolSvc, eng, rtr, hub, mcpSrv, Config{
		Version:             "1.0.0-sec",
		Mode:                "cloud",
		TrustProxy:          true,
		AdminToken:          adminTok,
		ArtifactsDir:        artDir,
		AuthFailLimitPerMin: 3,
	})
	tsCloud := httptest.NewServer(cloudSrv.Handler())
	defer tsCloud.Close()

	keyA, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Sec-Key-A", MaxConcurrentVMs: 4})
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Sec-Key-B", MaxConcurrentVMs: 4})
	if err != nil {
		t.Fatal(err)
	}

	rawReq := func(baseURL, method, rawPath, bearer, body string, hdrs map[string]string) (*http.Response, string) {
		t.Helper()
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req, rErr := http.NewRequest(method, baseURL+rawPath, rdr)
		if rErr != nil {
			t.Fatal(rErr)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		client := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, dErr := client.Do(req)
		if dErr != nil {
			t.Fatal(dErr)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		return resp, b.String()
	}

	// 1. [P0-1] Artifact Path Traversal (`/api/v1/artifacts/%2e%2e/colabhive.db` etc.) MUST return 400/403
	//    and NEVER expose `colabhive.db` (both unauthenticated AND authenticated)!
	for _, travPath := range []string{
		"/api/v1/artifacts/%2e%2e/colabhive.db",
		"/api/v1/artifacts/..%2fcolabhive.db",
		"/api/v1/artifacts/sess_demo_qwen_s0/%2e%2e",
		"/api/v1/artifacts/sess_demo_qwen_s0/..%2f..%2fcolabhive.db",
		"/api/v1/artifacts/../colabhive.db",
	} {
		for _, tok := range []string{"", adminTok} {
			resp, body := rawReq(tsCloud.URL, http.MethodGet, travPath, tok, "", nil)
			if strings.Contains(body, "SUPER_SECRET_REFRESH_TOKEN") || strings.Contains(body, "SQLite format 3") {
				t.Fatalf("CRITICAL VAULT LEAK on %s: leaked SQLite DB contents!", travPath)
			}
			if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusForbidden {
				t.Fatalf("expected 400 or 403 on %s (tok=%q), got %d: %s", travPath, tok, resp.StatusCode, body)
			}
		}
	}

	// Seed a real session artifact under `<artDir>/sess_art_sec/loss.png` and verify:
	// - Unauthenticated scanner fetch without HMAC signature is rejected (401/403)
	// - Authenticated `GET /api/v1/sessions/sess_art_sec` returns HMAC-signed `artifactUrl` (`?exp=...&sig=...`) that loads cleanly (200 OK)!
	_ = os.MkdirAll(filepath.Join(artDir, "sess_art_sec"), 0o755)
	_ = os.WriteFile(filepath.Join(artDir, "sess_art_sec", "loss.png"), []byte("\x89PNG\r\n\x1a\nPLOT"), 0o644)
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:          "sess_art_sec",
		Name:        "art-sec-nb",
		Accelerator: models.AcceleratorG4,
		Status:      models.SessionIdle,
		Cells: []models.NotebookCell{
			{
				CellID:   "c1",
				CellType: "code",
				Outputs: []models.KernelRichOutput{
					{
						Index:       0,
						MimeTypes:   []string{"image/png"},
						ArtifactURL: "/api/v1/artifacts/sess_art_sec/loss.png",
					},
				},
			},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})

	unauthArtResp, _ := rawReq(tsCloud.URL, http.MethodGet, "/api/v1/artifacts/sess_art_sec/loss.png", "", "", map[string]string{
		"X-Forwarded-For": "198.51.100.11",
	})
	if unauthArtResp.StatusCode != http.StatusUnauthorized && unauthArtResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 401/403 on unsigned unauthenticated artifact fetch in cloud mode, got %d", unauthArtResp.StatusCode)
	}

	sessGetResp, sessGetBody := rawReq(tsCloud.URL, http.MethodGet, "/api/v1/sessions/sess_art_sec", adminTok, "", nil)
	if sessGetResp.StatusCode != http.StatusOK {
		t.Fatalf("GET session failed: %d", sessGetResp.StatusCode)
	}
	var seededSess models.RuntimeSession
	_ = json.Unmarshal([]byte(sessGetBody), &seededSess)
	signedURL := seededSess.Cells[0].Outputs[0].ArtifactURL
	if !strings.Contains(signedURL, "sig=") || !strings.Contains(signedURL, "exp=") {
		t.Fatalf("expected HMAC-signed ArtifactURL in cloud mode, got %q", signedURL)
	}
	signedFetchResp, signedFetchBody := rawReq(tsCloud.URL, http.MethodGet, signedURL, "", "", nil)
	if signedFetchResp.StatusCode != http.StatusOK || signedFetchBody != "\x89PNG\r\n\x1a\nPLOT" {
		t.Fatalf("expected 200 OK on valid HMAC-signed ArtifactURL, got %d", signedFetchResp.StatusCode)
	}
	// KeyB attempting to fetch admin/other-key artifact directly WITHOUT HMAC signature MUST get 403 Forbidden!
	bArtResp, _ := rawReq(tsCloud.URL, http.MethodGet, "/api/v1/artifacts/sess_art_sec/loss.png", keyB.RawKey, "", nil)
	if bArtResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when KeyB fetches unauthorized session artifact without HMAC sig, got %d", bArtResp.StatusCode)
	}

	// 2. [P0-2] REST Host-FS Path Rejection (`local_file_path`, `requirements_path`, `local_output_dir`, `script_path`, `upload_files[].local_path`, unsafe `experiment_id`)!
	sessACreateResp, sessACreateRaw := rawReq(tsCloud.URL, http.MethodPost, "/api/v1/sessions", keyA.RawKey, `{"name":"sec-sess-a","gpu":"G4"}`, nil)
	if sessACreateResp.StatusCode != http.StatusOK {
		t.Fatalf("create session A failed: %d %s", sessACreateResp.StatusCode, sessACreateRaw)
	}
	var sessA models.RuntimeSession
	_ = json.Unmarshal([]byte(sessACreateRaw), &sessA)

	for _, badREST := range []struct {
		path string
		body string
	}{
		{"/api/v1/exec", `{"local_file_path":"/etc/passwd"}`},
		{"/api/v1/sessions/" + sessA.ID + "/exec", `{"local_file":"/etc/passwd"}`},
		{"/api/v1/sessions/" + sessA.ID + "/install", `{"requirements_path":"/etc/passwd"}`},
		{"/api/v1/experiments", `{"experiment_id":"exp_bad_1","script_path":"/etc/passwd","num_shards":1}`},
		{"/api/v1/experiments", `{"experiment_id":"exp_bad_2","script_code":"print(1)","local_output_dir":"/tmp/pwn","num_shards":1}`},
		{"/api/v1/experiments", `{"experiment_id":"exp_bad_3","script_code":"print(1)","upload_files":[{"local_path":"/etc/passwd","remote_path":"/content/x"}]}`},
		{"/api/v1/experiments", `{"experiment_id":"../escape","script_code":"print(1)"}`},
	} {
		r, raw := rawReq(tsCloud.URL, http.MethodPost, badREST.path, keyA.RawKey, badREST.body, nil)
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request rejecting host path on %s (%s), got %d: %s", badREST.path, badREST.body, r.StatusCode, raw)
		}
	}

	// Inline `script_code` + `upload_files[].content_base64` succeeds; duplicate `experiment_id` returns 409 Conflict;
	// and KeyB gets 403 Forbidden on KeyA's experiment!
	inlineB64 := base64.StdEncoding.EncodeToString([]byte("{\"lr\":0.001}"))
	expCreateResp, expCreateRaw := rawReq(
		tsCloud.URL,
		http.MethodPost,
		"/api/v1/experiments",
		keyA.RawKey,
		`{"experiment_id":"exp_inline_sec","script_code":"print('ok')","num_shards":1,"gpu":"G4","upload_files":[{"remote_path":"/content/hp.json","content_base64":"`+inlineB64+`"}]}`,
		nil,
	)
	if expCreateResp.StatusCode != http.StatusOK {
		t.Fatalf("inline experiment launch failed (%d): %s", expCreateResp.StatusCode, expCreateRaw)
	}
	dupExpResp, _ := rawReq(
		tsCloud.URL,
		http.MethodPost,
		"/api/v1/experiments",
		keyB.RawKey,
		`{"experiment_id":"exp_inline_sec","script_code":"print('overwrite')","num_shards":1}`,
		nil,
	)
	if dupExpResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on duplicate experiment_id, got %d", dupExpResp.StatusCode)
	}
	bGetExpResp, _ := rawReq(tsCloud.URL, http.MethodGet, "/api/v1/experiments/exp_inline_sec", keyB.RawKey, "", nil)
	if bGetExpResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when KeyB inspects KeyA's experiment, got %d", bGetExpResp.StatusCode)
	}

	// 3. [P0-4 / P0-3] Public Contributor `/complete` & `/import` Invite Validation & Owner Account Protection!
	pastTime := time.Now().UTC().Add(-1 * time.Hour)
	_ = st.SaveInvite(ctx, &models.ContributorInvite{
		Code:      "ch_inv_revoked",
		PoolTitle: "Revoked Invite",
		Revoked:   true,
		CreatedAt: time.Now().UTC(),
	})
	_ = st.SaveInvite(ctx, &models.ContributorInvite{
		Code:      "ch_inv_expired",
		PoolTitle: "Expired Invite",
		ExpiresAt: &pastTime,
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
	})
	_ = st.SaveInvite(ctx, &models.ContributorInvite{
		Code:      "ch_inv_exhausted",
		PoolTitle: "Exhausted Invite",
		MaxUses:   2,
		UsedCount: 2,
		CreatedAt: time.Now().UTC(),
	})

	ownerBefore, _ := st.GetAccountByEmail(ctx, "lab-ultra-01@colabhive.dev")
	attackCredPayload := `{"credential":{"email":"lab-ultra-01@colabhive.dev","client_id":"764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com","client_secret":"d-FL95Q19q7MQmFpd7hHD0Ty","refresh_token":"1//evil_attacker_token"}}`

	respMissingInv, _ := rawReq(tsCloud.URL, http.MethodPost, "/api/public/contribute/ch_inv_does_not_exist/complete", "", attackCredPayload, nil)
	if respMissingInv.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on fake contributor invite code, got %d", respMissingInv.StatusCode)
	}
	for _, deadCode := range []string{"ch_inv_revoked", "ch_inv_expired", "ch_inv_exhausted"} {
		rDead, rawDead := rawReq(tsCloud.URL, http.MethodPost, "/api/public/contribute/"+deadCode+"/complete", "", attackCredPayload, nil)
		if rDead.StatusCode != http.StatusGone && rDead.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 410/404 on inactive invite %s, got %d: %s", deadCode, rDead.StatusCode, rawDead)
		}
	}
	// Even on an active invite (`ch_inv_demo2026`), attempting to overwrite an existing owner account MUST be rejected!
	respOverwriteOwner, rawOverwrite := rawReq(tsCloud.URL, http.MethodPost, "/api/public/contribute/ch_inv_demo2026/complete", "", attackCredPayload, nil)
	if respOverwriteOwner.StatusCode == http.StatusOK {
		t.Fatalf("CRITICAL: public /complete allowed overwriting owner account: %s", rawOverwrite)
	}
	ownerAfter, _ := st.GetAccountByEmail(ctx, "lab-ultra-01@colabhive.dev")
	if ownerBefore != nil && ownerAfter != nil && ownerAfter.IsContributor {
		t.Fatalf("CRITICAL: owner account IsContributor mutated to true!")
	}

	// 4. [P1-17] Single-Use 60s WebSocket Ticket (`POST /api/v1/ws-ticket` -> `/ws/events?ticket=...`)!
	unauthTicketResp, _ := rawReq(tsCloud.URL, http.MethodPost, "/api/v1/ws-ticket", "", `{}`, map[string]string{
		"X-Forwarded-For": "198.51.100.12",
	})
	if unauthTicketResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated POST /api/v1/ws-ticket, got %d", unauthTicketResp.StatusCode)
	}
	ticketResp, ticketRaw := rawReq(tsCloud.URL, http.MethodPost, "/api/v1/ws-ticket", adminTok, `{}`, nil)
	if ticketResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/ws-ticket failed (%d): %s", ticketResp.StatusCode, ticketRaw)
	}
	var ticketObj struct {
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal([]byte(ticketRaw), &ticketObj)
	if !strings.HasPrefix(ticketObj.Ticket, "wst_") {
		t.Fatalf("expected wst_ ticket, got %q", ticketObj.Ticket)
	}

	wsTicketURL := "ws" + strings.TrimPrefix(tsCloud.URL, "http") + "/ws/events?ticket=" + ticketObj.Ticket
	wsConn1, _, err := websocket.DefaultDialer.Dial(wsTicketURL, nil)
	if err != nil {
		t.Fatalf("WebSocket connect with valid single-use ticket failed: %v", err)
	}
	_ = wsConn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	var hb models.WSEvent
	if err := wsConn1.ReadJSON(&hb); err != nil || hb.Type != models.WSEventHeartbeat {
		t.Fatalf("expected heartbeat over ticket-authenticated WS, got err=%v ev=%+v", err, hb)
	}
	_ = wsConn1.Close()

	// Replaying the consumed single-use ticket MUST fail with 401!
	replayHdr := http.Header{}
	replayHdr.Set("X-Forwarded-For", "198.51.100.13")
	_, replayWSResp, replayErr := websocket.DefaultDialer.Dial(wsTicketURL, replayHdr)
	if replayErr == nil || replayWSResp == nil || replayWSResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected single-use WS ticket replay to fail with 401, got err=%v resp=%+v", replayErr, replayWSResp)
	}

	// 5. [P0-5 & P1-14] Spoofed `X-Forwarded-For` left-most hop ignored & valid AdminToken never locked out by NAT peers!
	for i := 0; i < 4; i++ {
		// Attacker prepends a random spoofed IP (`1.1.1.x`) while real reverse proxy appends `198.51.100.200`.
		rSpoof, _ := rawReq(tsCloud.URL, http.MethodGet, "/api/v1/accounts", "wrong-tok", "", map[string]string{
			"X-Forwarded-For": "1.1.1." + string(rune('1'+i)) + ", 198.51.100.200",
		})
		if i == 3 && rSpoof.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected attacker IP 198.51.100.200 to be locked out despite spoofed left-most XFF, got %d", rSpoof.StatusCode)
		}
	}
	// Legitimate Admin behind the exact same locked-out NAT IP `198.51.100.200` STILL succeeds with valid AdminToken!
	adminBehindNAT, _ := rawReq(tsCloud.URL, http.MethodGet, "/api/v1/accounts", adminTok, "", map[string]string{
		"X-Forwarded-For": "198.51.100.200",
	})
	if adminBehindNAT.StatusCode != http.StatusOK {
		t.Fatalf("valid AdminToken was locked out by NAT peer failures (got %d, want 200)!", adminBehindNAT.StatusCode)
	}

	// 6. [P0-6 / P0-4] Keyless Local Mode Origin Guard (CORS & WebSocket) + Multipart Upload + DesktopState `"autostart"`!
	localSrv := New(st, poolSvc, eng, rtr, hub, mcp.NewServer(poolSvc, eng, rtr, hub), Config{
		Version:      "1.0.0-local",
		Mode:         "local",
		AdminToken:   "",
		ArtifactsDir: artDir,
		DemoMode:     true,
	})
	localSrv.SetDesktopBridge(func(_ context.Context, _ DesktopActionRequest) (DesktopState, error) {
		return DesktopState{DesktopShell: true, AutoStartEnabled: true, TrayVisible: true}, nil
	})
	tsLocal := httptest.NewServer(localSrv.Handler())
	defer tsLocal.Close()

	evilCORS, _ := rawReq(tsLocal.URL, http.MethodPost, "/api/v1/pool/export", "", `{}`, map[string]string{
		"Origin": "https://malicious.site.example",
	})
	if evilCORS.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on cross-origin drive-by request in keyless local mode, got %d", evilCORS.StatusCode)
	}
	loopCORS, _ := rawReq(tsLocal.URL, http.MethodGet, "/api/v1/health", "", "", map[string]string{
		"Origin": "http://localhost:5173",
	})
	if loopCORS.StatusCode != http.StatusOK || loopCORS.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("expected loopback Origin http://localhost:5173 allowed, got status=%d hdr=%q",
			loopCORS.StatusCode, loopCORS.Header.Get("Access-Control-Allow-Origin"))
	}

	evilWSHdr := http.Header{}
	evilWSHdr.Set("Origin", "https://malicious.site.example")
	_, evilWSResp, evilWSErr := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(tsLocal.URL, "http")+"/ws/events", evilWSHdr)
	if evilWSErr == nil || evilWSResp == nil || evilWSResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on cross-origin WebSocket in keyless local mode, got err=%v resp=%+v", evilWSErr, evilWSResp)
	}

	// Verify `DesktopState` JSON contains BOTH `"autostart_enabled":true` AND `"autostart":true`!
	_, deskStateRaw := rawReq(tsLocal.URL, http.MethodGet, "/api/v1/desktop/state", "", "", nil)
	if !strings.Contains(deskStateRaw, `"autostart_enabled":true`) || !strings.Contains(deskStateRaw, `"autostart":true`) {
		t.Fatalf("expected both autostart_enabled and autostart in /api/v1/desktop/state JSON: %s", deskStateRaw)
	}

	// Verify multipart `FormData` upload (`path` + `file`) without duplicate Base64!
	var mpBuf bytes.Buffer
	mpWriter := multipart.NewWriter(&mpBuf)
	_ = mpWriter.WriteField("path", "/content/multipart_ok.bin")
	fw, _ := mpWriter.CreateFormFile("file", "multipart_ok.bin")
	_, _ = fw.Write([]byte("MULTIPART_BINARY_DATA_2026"))
	_ = mpWriter.Close()

	mpReq, _ := http.NewRequest(http.MethodPost, tsCloud.URL+"/api/v1/sessions/"+sessA.ID+"/files", &mpBuf)
	mpReq.Header.Set("Content-Type", mpWriter.FormDataContentType())
	mpReq.Header.Set("Authorization", "Bearer "+keyA.RawKey)
	mpResp, err := http.DefaultClient.Do(mpReq)
	if err != nil || mpResp.StatusCode != http.StatusOK {
		t.Fatalf("multipart file upload failed: err=%v resp=%+v", err, mpResp)
	}
	_ = mpResp.Body.Close()

	dlCheckResp, dlCheckBody := rawReq(tsCloud.URL, http.MethodGet, "/api/v1/sessions/"+sessA.ID+"/files/download?path=/content/multipart_ok.bin", keyA.RawKey, "", nil)
	if dlCheckResp.StatusCode != http.StatusOK || dlCheckBody != "MULTIPART_BINARY_DATA_2026" {
		t.Fatalf("multipart uploaded file download mismatch: status=%d body=%q", dlCheckResp.StatusCode, dlCheckBody)
	}

	// 7. [P2-4] Codex TOML surgical removal of quoted `[mcp_servers."colabhive"]` & preservation of `[...]` inside `"""..."""`!
	tomlHome := t.TempDir()
	codexPath := filepath.Join(tomlHome, ".codex", "config.toml")
	_ = os.MkdirAll(filepath.Dir(codexPath), 0o755)
	trickyTOML := `model = "gpt-5"
instructions = """
Keep this literal block intact:
[mcp_servers.other_inside_multiline]
url = "http://do-not-truncate"
"""

[mcp_servers."colabhive"]
url = "http://127.0.0.1:7860/mcp"

[mcp_servers.keep_sibling]
url = "http://127.0.0.1:9999/mcp"
`
	_ = os.WriteFile(codexPath, []byte(trickyTOML), 0o600)
	tomlInjector := router.NewServiceWithDirs(nil, nil, nil, tomlHome, "", "", "linux")
	if _, rErr := tomlInjector.RemoveAgentConfig(ctx, models.AgentCodex); rErr != nil {
		t.Fatalf("RemoveAgentConfig on quoted/multiline Codex TOML failed: %v", rErr)
	}
	afterTOML, _ := os.ReadFile(codexPath)
	afterStr := string(afterTOML)
	if strings.Contains(afterStr, `colabhive`) ||
		!strings.Contains(afterStr, `[mcp_servers.other_inside_multiline]`) ||
		!strings.Contains(afterStr, `[mcp_servers.keep_sibling]`) {
		t.Fatalf("unexpected Codex TOML after removing quoted colabhive table:\n%s", afterStr)
	}

	// 8. [P1-9] `colab run --shards 2` polls experiment status until terminal completion before `/teardown`!
	shardScript := filepath.Join(t.TempDir(), "shard_job.py")
	_ = os.WriteFile(shardScript, []byte("print('shard done')\n"), 0o644)
	var shardOut, shardErr bytes.Buffer
	shardExit := RunDaemonCLI(
		ctx,
		"colab",
		[]string{"--server", tsCloud.URL, "--api-key", keyA.RawKey, "--shards", "2", "run", "--gpu", "G4", shardScript},
		nil,
		&shardOut,
		&shardErr,
		VersionInfo{Version: "1.0.0"},
	)
	if shardExit != 0 || !strings.Contains(shardErr.String(), "completed") {
		t.Fatalf("colab run --shards 2 failed (code=%d): out=%q err=%q", shardExit, shardOut.String(), shardErr.String())
	}

	// 9. Wave-5 Security Regressions:
	//    - Dual query `?token=<scoped>&key=ch_sk_live_default` on mounted `/mcp` MUST NOT promote scoped key to admin!
	//    - Artifact route `/api/v1/artifacts/{victimSessID}/...` MUST reject session-name spoofing (`Name == victimSessID`)!
	//    - Scoped REST session/experiment responses MUST redact `account_email`, `account_id`, `local_output_dir`, `merged_jsonl_path`!
	//    - Keyless local mode MUST reject non-loopback `Host` header (DNS rebinding guard) with 403 Forbidden!
	respDualMCP, rawDualMCP := rawReq(
		tsCloud.URL+"/mcp?token="+keyA.RawKey+"&key=ch_sk_live_default",
		http.MethodPost,
		"",
		"",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pool_quota_summary","arguments":{}}}`,
		nil,
	)
	if respDualMCP.StatusCode != http.StatusOK || !strings.Contains(rawDualMCP, `"api_key_budget"`) {
		t.Fatalf("SECURITY FAILURE: /mcp?token=<scoped>&key=ch_sk_live_default bypassed scoped auth (%d): %s", respDualMCP.StatusCode, rawDualMCP)
	}

	// Seed an artifact under `sess_victim_deleted_99` on disk, then create a session owned by keyA whose Name is `sess_victim_deleted_99`!
	victimDir := filepath.Join(artDir, "sess_victim_deleted_99")
	_ = os.MkdirAll(victimDir, 0o755)
	_ = os.WriteFile(filepath.Join(victimDir, "secret.png"), []byte("TOP_SECRET_PNG"), 0o600)
	spoofSessResp, spoofSessRaw := rawReq(tsCloud.URL, http.MethodPost, "/api/v1/sessions", keyA.RawKey, `{"name":"sess_victim_deleted_99","gpu":"G4"}`, nil)
	if spoofSessResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/sessions failed (%d): %s", spoofSessResp.StatusCode, spoofSessRaw)
	}
	if strings.Contains(spoofSessRaw, "@colabhive.dev") || strings.Contains(spoofSessRaw, `"account_email":"lab-`) {
		t.Fatalf("scoped POST /api/v1/sessions leaked owner account email: %s", spoofSessRaw)
	}
	spoofArtResp, _ := rawReq(tsCloud.URL, http.MethodGet, "/api/v1/artifacts/sess_victim_deleted_99/secret.png", keyA.RawKey, "", nil)
	if spoofArtResp.StatusCode != http.StatusForbidden {
		t.Fatalf("SECURITY FAILURE: scoped key accessed victim artifact by spoofing session Name (HTTP %d)", spoofArtResp.StatusCode)
	}

	scopedExpResp, scopedExpRaw := rawReq(tsCloud.URL, http.MethodPost, "/api/v1/experiments", keyA.RawKey, `{"experiment_id":"exp_scoped_redact_1","gpu":"G4","num_shards":1,"script_code":"print(1)"}`, nil)
	if scopedExpResp.StatusCode != http.StatusOK {
		t.Fatalf("scoped POST /api/v1/experiments failed (%d): %s", scopedExpResp.StatusCode, scopedExpRaw)
	}
	if strings.Contains(scopedExpRaw, "@colabhive.dev") || strings.Contains(scopedExpRaw, "colabhive-runs") {
		t.Fatalf("scoped experiment response leaked owner email or host LocalOutputDir: %s", scopedExpRaw)
	}
	dupCaseExpResp, _ := rawReq(tsCloud.URL, http.MethodPost, "/api/v1/experiments", keyA.RawKey, `{"experiment_id":"EXP_SCOPED_REDACT_1","gpu":"G4","num_shards":1,"script_code":"print(1)"}`, nil)
	if dupCaseExpResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on case-insensitive duplicate experiment_id, got %d", dupCaseExpResp.StatusCode)
	}

	// Verify keyless local server blocks DNS rebinding (`Host: attacker.evil:7860`) with 403 Forbidden!
	localSrvDNS := New(st, poolSvc, eng, rtr, hub, mcp.NewServer(poolSvc, eng, rtr, hub), Config{
		Mode:         "local",
		ArtifactsDir: artDir,
		DemoMode:     true,
	})
	tsLocalDNS := httptest.NewServer(localSrvDNS.Handler())
	defer tsLocalDNS.Close()

	rebindReq, _ := http.NewRequest(http.MethodGet, tsLocalDNS.URL+"/api/v1/accounts", nil)
	rebindReq.Host = "attacker-rebind.evil:7860"
	rebindResp, rErr := http.DefaultClient.Do(rebindReq)
	if rErr != nil {
		t.Fatal(rErr)
	}
	_ = rebindResp.Body.Close()
	if rebindResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on DNS rebinding Host header in keyless local mode, got %d", rebindResp.StatusCode)
	}
}
