package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/colabhive/colabhive/internal/mockcolab"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/router"
	"github.com/colabhive/colabhive/internal/store"
)

// ============================================================================
// Self-Contained Test Store & Pool Mocks
// ============================================================================

type testHub struct {
	mu     sync.Mutex
	events []models.WSEvent
}

func (h *testHub) Publish(ev models.WSEvent) {
	h.mu.Lock()
	h.events = append(h.events, ev)
	h.mu.Unlock()
}
func (h *testHub) Subscribe(_ context.Context) (<-chan models.WSEvent, func()) {
	ch := make(chan models.WSEvent, 16)
	return ch, func() { close(ch) }
}
func (h *testHub) ClientCount() int { return 0 }

type testStore struct {
	mu          sync.RWMutex
	accounts    map[string]*models.Account
	sessions    map[string]*models.RuntimeSession
	experiments map[string]*models.Experiment
	policy      models.RouterPolicy
	waste       models.SavedWasteStats
}

func newTestStore() *testStore {
	return &testStore{
		accounts:    make(map[string]*models.Account),
		sessions:    make(map[string]*models.RuntimeSession),
		experiments: make(map[string]*models.Experiment),
		policy:      models.DefaultRouterPolicy(),
	}
}

func (s *testStore) SaveAccount(_ context.Context, acc *models.Account) error {
	s.mu.Lock()
	cp := *acc
	if acc.GPUEntitlements != nil {
		cp.GPUEntitlements = make(map[models.AcceleratorType]models.GPUEntitlement, len(acc.GPUEntitlements))
		for k, v := range acc.GPUEntitlements {
			cp.GPUEntitlements[k] = v
		}
	}
	s.accounts[acc.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *testStore) GetAccount(_ context.Context, id string) (*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.accounts[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}
func (s *testStore) GetAccountByEmail(_ context.Context, email string) (*models.Account, error) {
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
func (s *testStore) GetAccountByReceiptHash(_ context.Context, _ string) (*models.Account, error) {
	return nil, nil
}
func (s *testStore) ListAccounts(_ context.Context) ([]*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}
func (s *testStore) DeleteAccount(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.accounts, id)
	s.mu.Unlock()
	return nil
}
func (s *testStore) SaveProxy(_ context.Context, _ *models.ProxyConfig) error { return nil }
func (s *testStore) GetProxy(_ context.Context, _ string) (*models.ProxyConfig, error) {
	return nil, nil
}
func (s *testStore) ListProxies(_ context.Context) ([]*models.ProxyConfig, error) { return nil, nil }
func (s *testStore) DeleteProxy(_ context.Context, _ string) error                { return nil }
func (s *testStore) SaveInvite(_ context.Context, _ *models.ContributorInvite) error {
	return nil
}
func (s *testStore) GetInvite(_ context.Context, _ string) (*models.ContributorInvite, error) {
	return nil, nil
}
func (s *testStore) ListInvites(_ context.Context) ([]*models.ContributorInvite, error) {
	return nil, nil
}
func (s *testStore) DeleteInvite(_ context.Context, _ string) error { return nil }

func (s *testStore) SaveSession(_ context.Context, sess *models.RuntimeSession) error {
	s.mu.Lock()
	cp := *sess
	cp.Cells = append([]models.NotebookCell(nil), sess.Cells...)
	cp.RecentLogs = append([]string(nil), sess.RecentLogs...)
	s.sessions[sess.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *testStore) GetSession(_ context.Context, id string) (*models.RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sess, ok := s.sessions[id]; ok {
		cp := *sess
		cp.Cells = append([]models.NotebookCell(nil), sess.Cells...)
		cp.RecentLogs = append([]string(nil), sess.RecentLogs...)
		return &cp, nil
	}
	return nil, nil
}
func (s *testStore) GetSessionByName(_ context.Context, name string) (*models.RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		if sess.Name == name {
			cp := *sess
			cp.Cells = append([]models.NotebookCell(nil), sess.Cells...)
			cp.RecentLogs = append([]string(nil), sess.RecentLogs...)
			return &cp, nil
		}
	}
	return nil, nil
}
func (s *testStore) ListSessions(_ context.Context) ([]*models.RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.RuntimeSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		cp := *sess
		cp.Cells = append([]models.NotebookCell(nil), sess.Cells...)
		cp.RecentLogs = append([]string(nil), sess.RecentLogs...)
		out = append(out, &cp)
	}
	return out, nil
}
func (s *testStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	return nil
}

func (s *testStore) SaveExperiment(_ context.Context, exp *models.Experiment) error {
	s.mu.Lock()
	cp := *exp
	cp.Shards = append([]models.ExperimentShard(nil), exp.Shards...)
	s.experiments[exp.ID] = &cp
	s.mu.Unlock()
	return nil
}
func (s *testStore) GetExperiment(_ context.Context, id string) (*models.Experiment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if exp, ok := s.experiments[id]; ok {
		cp := *exp
		cp.Shards = append([]models.ExperimentShard(nil), exp.Shards...)
		return &cp, nil
	}
	return nil, nil
}
func (s *testStore) ListExperiments(_ context.Context) ([]*models.Experiment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Experiment, 0, len(s.experiments))
	for _, exp := range s.experiments {
		cp := *exp
		cp.Shards = append([]models.ExperimentShard(nil), exp.Shards...)
		out = append(out, &cp)
	}
	return out, nil
}
func (s *testStore) DeleteExperiment(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.experiments, id)
	s.mu.Unlock()
	return nil
}

func (s *testStore) SaveAPIKey(_ context.Context, _ *models.AgentAPIKey) error { return nil }
func (s *testStore) GetAPIKey(_ context.Context, _ string) (*models.AgentAPIKey, error) {
	return nil, nil
}
func (s *testStore) GetAPIKeyByHash(_ context.Context, _ string) (*models.AgentAPIKey, error) {
	return nil, nil
}
func (s *testStore) ListAPIKeys(_ context.Context) ([]*models.AgentAPIKey, error) { return nil, nil }
func (s *testStore) DeleteAPIKey(_ context.Context, _ string) error               { return nil }
func (s *testStore) GetRouterPolicy(_ context.Context) (*models.RouterPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.policy
	return &cp, nil
}
func (s *testStore) SaveRouterPolicy(_ context.Context, pol *models.RouterPolicy) error {
	s.mu.Lock()
	s.policy = *pol
	s.mu.Unlock()
	return nil
}
func (s *testStore) AppendToolTrace(_ context.Context, _ *models.ToolTraceEvent) error { return nil }
func (s *testStore) ListToolTraces(_ context.Context, _ int) ([]models.ToolTraceEvent, error) {
	return nil, nil
}
func (s *testStore) RecordBurnSample(_ context.Context, _ models.AcceleratorType, _ models.MachineShape, _ float64) error {
	return nil
}
func (s *testStore) GetCalibratedBurnRate(_ context.Context, _ models.AcceleratorType, _ models.MachineShape) (float64, bool, error) {
	return 0, false, nil
}
func (s *testStore) AddSavedWaste(_ context.Context, savedCU float64, isZombie bool) error {
	s.mu.Lock()
	s.waste.SavedIdleWasteCU += savedCU
	if isZombie {
		s.waste.ZombieAbortsCount++
	} else {
		s.waste.AutoTeardownsCount++
	}
	s.mu.Unlock()
	return nil
}
func (s *testStore) GetSavedWasteStats(_ context.Context) (models.SavedWasteStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.waste, nil
}
func (s *testStore) Close() error { return nil }

// ============================================================================
// Mock Colab Tunnel + Jupyter REST + ZMQ WebSocket Server
// ============================================================================

type fakeColabServer struct {
	srv           *httptest.Server
	mu            sync.Mutex
	files         map[string][]byte
	unassignedEps []string
	zombieMode    bool
	lastShape     string
}

func newFakeColabServer(t *testing.T) *fakeColabServer {
	t.Helper()
	fc := &fakeColabServer{
		files: make(map[string][]byte),
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	pngB64 := base64.StdEncoding.EncodeToString(GenerateDemoPlotPNG())

	mux := http.NewServeMux()

	// 1. 2-Step XSRF `/tun/m/assign` with HTTP 400 (H100) and HTTP 412 (acc_limit) simulation
	mux.HandleFunc("/tun/m/assign", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		acc := r.URL.Query().Get("accelerator")
		nbh := r.URL.Query().Get("nbh")
		if len(nbh) != 44 {
			http.Error(w, "invalid nbh length", http.StatusBadRequest)
			return
		}
		fc.mu.Lock()
		fc.lastShape = r.URL.Query().Get("shape")
		fc.mu.Unlock()

		if strings.Contains(auth, "acc_412") {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"error":"TooManyAssignmentsError"}`))
			return
		}
		if acc == "H100" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"error":"Accelerator H100 restricted"}`))
			return
		}

		if r.Method == http.MethodGet {
			resp := fmt.Sprintf(`{"acc":%q,"nbh":%q,"token":"xsrf-assign-tok","variant":"GPU"}`, acc, nbh)
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + resp))
			return
		}
		if r.Header.Get("X-Goog-Colab-Token") != "xsrf-assign-tok" {
			http.Error(w, "missing X-Goog-Colab-Token", http.StatusForbidden)
			return
		}
		ep := "gpu-" + strings.ToLower(acc) + "-s-test01"
		resp := fmt.Sprintf(
			`{"accelerator":%q,"endpoint":%q,"variant":1,"runtimeProxyInfo":{"token":"proxy-cap-tok","tokenExpiresInSeconds":3600,"url":%q}}`,
			acc, ep, fc.srv.URL,
		)
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + resp))
	})

	// 2. 2-Step XSRF `/tun/m/unassign/{ep}`
	mux.HandleFunc("/tun/m/unassign/", func(w http.ResponseWriter, r *http.Request) {
		ep := strings.TrimPrefix(r.URL.Path, "/tun/m/unassign/")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"token":"xsrf-unassign-tok"}`))
			return
		}
		if r.Header.Get("X-Goog-Colab-Token") != "xsrf-unassign-tok" {
			http.Error(w, "bad unassign xsrf", http.StatusForbidden)
			return
		}
		fc.mu.Lock()
		fc.unassignedEps = append(fc.unassignedEps, ep)
		fc.mu.Unlock()
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{}`))
	})

	// 3. Jupyter `/api/sessions` & `/api/kernels/`
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"jup-sess-1","kernel":{"id":"kern-abc-1"}}`))
	})
	mux.HandleFunc("/api/kernels/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/channels") {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				var req jupyterWireMsg
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				if asString(req.Header["msg_type"]) != "execute_request" {
					continue
				}
				code := asString(req.Content["code"])
				parent := req.Header

				_ = conn.WriteJSON(jupyterWireMsg{
					Channel:      "iopub",
					Header:       map[string]any{"msg_type": "status"},
					ParentHeader: parent,
					Content:      map[string]any{"execution_state": "busy"},
				})

				if strings.Contains(code, "/proc/") {
					fc.mu.Lock()
					z := fc.zombieMode
					fc.mu.Unlock()
					statLine := "4812 (python3 (worker)) R 1 4812 4812\n"
					if z {
						statLine = "4812 (python3 (crashed)) Z 1 4812 4812\n"
					}
					_ = conn.WriteJSON(jupyterWireMsg{
						Channel:      "iopub",
						Header:       map[string]any{"msg_type": "stream"},
						ParentHeader: parent,
						Content:      map[string]any{"name": "stdout", "text": statLine},
					})
				} else if strings.Contains(code, "drive.mount") {
					// Emit colab_request to test Contributor Auth-Propagation Firewall!
					_ = conn.WriteJSON(jupyterWireMsg{
						Channel:      "iopub",
						Header:       map[string]any{"msg_type": "colab_request", "msg_id": "cr-1"},
						ParentHeader: parent,
						Metadata:     map[string]any{"colab_msg_id": "cm-99", "colab_request_type": "request_auth"},
						Content:      map[string]any{"request": map[string]any{"authType": "dfs_ephemeral"}},
					})
					// Read firewall rejection reply on stdin
					var stdinReply jupyterWireMsg
					_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
					_ = conn.ReadJSON(&stdinReply)
				} else if strings.Contains(code, "sys.exit(42)") {
					_ = conn.WriteJSON(jupyterWireMsg{
						Channel:      "iopub",
						Header:       map[string]any{"msg_type": "error"},
						ParentHeader: parent,
						Content:      map[string]any{"ename": "SystemExit", "evalue": "42", "traceback": []string{}},
					})
				} else {
					_ = conn.WriteJSON(jupyterWireMsg{
						Channel:      "iopub",
						Header:       map[string]any{"msg_type": "stream"},
						ParentHeader: parent,
						Content:      map[string]any{"name": "stdout", "text": "Hello from G4 Blackwell\n"},
					})
					if strings.Contains(code, "plot_now") {
						_ = conn.WriteJSON(jupyterWireMsg{
							Channel:      "iopub",
							Header:       map[string]any{"msg_type": "display_data"},
							ParentHeader: parent,
							Content: map[string]any{
								"data": map[string]any{
									"image/png":  pngB64,
									"text/plain": "<Figure 480x260>",
								},
							},
						})
					}
					_ = conn.WriteJSON(jupyterWireMsg{
						Channel:      "iopub",
						Header:       map[string]any{"msg_type": "execute_result"},
						ParentHeader: parent,
						Content: map[string]any{
							"execution_count": 1,
							"data":            map[string]any{"text/plain": "'ok-val'"},
						},
					})
				}

				_ = conn.WriteJSON(jupyterWireMsg{
					Channel:      "iopub",
					Header:       map[string]any{"msg_type": "status"},
					ParentHeader: parent,
					Content:      map[string]any{"execution_state": "idle"},
				})
				_ = conn.WriteJSON(jupyterWireMsg{
					Channel:      "shell",
					Header:       map[string]any{"msg_type": "execute_reply"},
					ParentHeader: parent,
					Content:      map[string]any{"status": "ok", "execution_count": 1},
				})
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// 4. Out-of-band `/api/colab/resources` (with `)]}'\n` XSSI prefix!)
	mux.HandleFunc("/api/colab/resources", func(w http.ResponseWriter, r *http.Request) {
		body := `{"memory":{"totalBytes":89612345344,"freeBytes":61203947520},"disks":[{"filesystem":{"label":"kernel","totalBytes":253055090688,"usedBytes":42190848000}}],"gpus":[{"name":"NVIDIA RTX PRO 6000 Blackwell","memoryUsedBytes":55190323200,"memoryTotalBytes":103079215104,"gpuUtilization":0.95,"memoryUtilization":0.53,"temperatureC":66,"everUsed":true}]}`
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + body))
	})

	// 5. Jupyter `/api/contents/`
	mux.HandleFunc("/api/contents/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/api/contents/")
		switch r.Method {
		case http.MethodPut:
			var p struct {
				Content string `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			decoded, _ := base64.StdEncoding.DecodeString(p.Content)
			fc.mu.Lock()
			fc.files[rel] = decoded
			fc.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"type":"file"}`))
		case http.MethodGet:
			if r.URL.Query().Get("content") == "1" {
				fc.mu.Lock()
				b, ok := fc.files[rel]
				fc.mu.Unlock()
				if !ok {
					http.NotFound(w, r)
					return
				}
				resp, _ := json.Marshal(map[string]any{
					"type":    "file",
					"format":  "base64",
					"content": base64.StdEncoding.EncodeToString(b),
				})
				_, _ = w.Write(resp)
				return
			}
			_, _ = w.Write([]byte(`{"type":"directory","content":[{"name":"data.bin","path":"content/data.bin","type":"file","size":8}]}`))
		case http.MethodDelete:
			fc.mu.Lock()
			delete(fc.files, rel)
			fc.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}
	})

	fc.srv = httptest.NewServer(mux)
	t.Cleanup(fc.srv.Close)
	return fc
}

// ============================================================================
// Runtime Wire-Protocol, Firewall & Anti-Zombie Tests
// ============================================================================

func TestParseProcStatAntiZombie(t *testing.T) {
	cases := []struct {
		raw        string
		wantState  string
		wantAlive  bool
		wantZombie bool
	}{
		{"4812 (python3) R 1 4812 4812 0 -1", "R", true, false},
		{"4812 (python3 (shard worker)) S 1 4812", "S", true, false},
		{"4812 (train (weird) name) Z 1 4812", "Z", false, true},
		{"", "MISSING", false, false},
	}
	for _, tc := range cases {
		st, alive, z := ParseProcStat(tc.raw)
		if st != tc.wantState || alive != tc.wantAlive || z != tc.wantZombie {
			t.Fatalf("ParseProcStat(%q) = (%s, %v, %v), want (%s, %v, %v)",
				tc.raw, st, alive, z, tc.wantState, tc.wantAlive, tc.wantZombie)
		}
	}
}

func TestWireAssignFailoverJupyterWSFirewallAndZombieGuard(t *testing.T) {
	ctx := context.Background()
	fakeColab := newFakeColabServer(t)
	st := newTestStore()
	hub := &testHub{}

	// Account 1 returns HTTP 412; Account 2 is a Contributed Pro+ account (with Drive Firewall enabled!).
	_ = st.SaveAccount(ctx, &models.Account{
		ID:            "acc_412",
		Email:         "busy@lab.dev",
		Health:        models.HealthHealthy,
		Enabled:       true,
		Priority:      100, // Picked first, hits 412 -> fails over to acc_donor!
		PaidCUBalance: 400,
		SchedulableCU: 400,
	})
	_ = st.SaveAccount(ctx, &models.Account{
		ID:            "acc_donor",
		Email:         "donor@stanford.edu",
		Health:        models.HealthHealthy,
		Enabled:       true,
		IsContributor: true,
		PaidCUBalance: 300,
		SchedulableCU: 250,
		Guardrails: models.ContributorGuardrails{
			MinReserveCU:          50,
			AllowPaidCUBurn:       true,
			BlockDrivePropagation: true,
			AllowedGPUs:           []models.AcceleratorType{models.AcceleratorH100, models.AcceleratorG4, models.AcceleratorL4},
		},
	})

	rtr := router.NewServiceWithDirs(st, nil, hub, t.TempDir(), "", "", "linux")
	eng := NewConcreteEngine(st, nil, rtr, hub, Options{
		TunnelBaseURL:          fakeColab.srv.URL,
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	// Request H100 with AutoFallback:
	//   1) acc_412 on H100 -> HTTP 412 -> acc_412 marked MAX_CONCURRENCY
	//   2) acc_donor on H100 -> HTTP 400 -> H100 marked TIER_RESTRICTED
	//   3) acc_donor on G4 -> 2-step XSRF assign succeeds!
	sess, err := eng.CreateSession(ctx, models.SessionAllocRequest{
		Name:         "test-failover",
		Accelerator:  models.AcceleratorH100,
		HighMem:      true,
		Strategy:     models.StrategyMaxBalance,
		AutoFallback: true,
	})
	if err != nil {
		t.Fatalf("CreateSession failover failed: %v", err)
	}
	if sess.AccountID != "acc_donor" || sess.Accelerator != models.AcceleratorG4 || !sess.FallbackApplied {
		t.Fatalf("unexpected failover result: account=%s accel=%s fallback=%v", sess.AccountID, sess.Accelerator, sess.FallbackApplied)
	}
	if sess.Resources == nil || len(sess.Resources.GPUs) != 1 || sess.Resources.GPUs[0].TemperatureC != 66 {
		t.Fatalf("expected initial /api/colab/resources GPU telemetry, got %+v", sess.Resources)
	}

	// Verify L4 shape omission rule (`shape=hm` must NOT be sent for L4!).
	sessL4, err := eng.CreateSession(ctx, models.SessionAllocRequest{
		Name:        "test-l4",
		Accelerator: models.AcceleratorL4,
		HighMem:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fakeColab.lastShape != "" {
		t.Fatalf("expected shape=hm to be omitted for L4, got shape=%q", fakeColab.lastShape)
	}
	// At this point acc_donor (MaxConcurrentVMs=0 or let's check both sessions active -> stop sessL4 -> ActiveSessionSummary recomputes to test-failover!)
	accMid, _ := st.GetAccount(ctx, "acc_donor")
	if accMid.ActiveAssignments != 2 || !strings.Contains(accMid.ActiveSessionSummary, "test-failover") || !strings.Contains(accMid.ActiveSessionSummary, "test-l4") {
		t.Fatalf("expected ActiveAssignments=2 and combined summary before stop, got %+v", accMid)
	}
	_ = eng.StopSession(ctx, sessL4.ID)
	accAfterOneStop, _ := st.GetAccount(ctx, "acc_donor")
	if accAfterOneStop.ActiveAssignments != 1 || accAfterOneStop.Health != models.HealthActiveWorking || accAfterOneStop.ActiveSessionSummary != "test-failover (G4)" {
		t.Fatalf("expected ActiveAssignments=1, Health=ACTIVE_WORKING, summary='test-failover (G4)' after stopping L4, got assignments=%d health=%s summary=%q",
			accAfterOneStop.ActiveAssignments, accAfterOneStop.Health, accAfterOneStop.ActiveSessionSummary)
	}

	// Execute code emitting stdout + display_data PNG over ZMQ-over-WebSocket.
	execRes, err := eng.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID:      sess.ID,
		Code:           "plot_now()",
		IdleTimeoutSec: 10,
		InlineImages:   true,
	})
	if err != nil {
		t.Fatalf("ExecuteCode failed: %v", err)
	}
	if !strings.Contains(execRes.Stdout, "Hello from G4 Blackwell") || execRes.Result != "'ok-val'" {
		t.Fatalf("unexpected exec output: %+v", execRes)
	}
	if len(execRes.RichOutputs) != 1 || execRes.RichOutputs[0].InlineBase64 == "" {
		t.Fatalf("expected 1 rich PNG output with InlineBase64, got %+v", execRes.RichOutputs)
	}

	// Verify CPython SystemExit(42) exit-code extraction.
	exitRes, err := eng.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID: sess.ID,
		Code:      "import sys; sys.exit(42)",
	})
	if err != nil || exitRes.ExecutionError == nil || exitRes.ExecutionError.ExitCode == nil || *exitRes.ExecutionError.ExitCode != 42 {
		t.Fatalf("expected SystemExit(42), got err=%v execErr=%+v", err, exitRes.ExecutionError)
	}

	// Verify Contributor `colab_request` (`dfs_ephemeral`) Firewall blocks Drive mounting!
	fwRes, err := eng.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID: sess.ID,
		Code:      "from google.colab import drive; drive.mount('/content/drive')",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fwRes.Status != "error" || !strings.Contains(fwRes.Stderr, "Contributor Firewall") {
		t.Fatalf("expected Contributor Firewall rejection, got %+v", fwRes)
	}

	// Verify `/api/contents` file upload, list, download, delete.
	payload := []byte("checkpoint-bytes-123")
	if err := eng.UploadFile(ctx, sess.ID, "/content/data.bin", payload); err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}
	gotBytes, err := eng.DownloadFile(ctx, sess.ID, "/content/data.bin")
	if err != nil || !bytes.Equal(gotBytes, payload) {
		t.Fatalf("DownloadFile mismatch: err=%v got=%q", err, string(gotBytes))
	}
	files, err := eng.ListFiles(ctx, sess.ID, "/content")
	if err != nil || len(files) != 1 {
		t.Fatalf("ListFiles failed: err=%v files=%+v", err, files)
	}
	if err := eng.DeleteFile(ctx, sess.ID, "/content/data.bin"); err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}

	// Verify Anti-Zombie Watchdog (`Z` state -> automatic VM unassign + SavedWaste ledger credit!).
	sessLoaded, _ := st.GetSession(ctx, sess.ID)
	sessLoaded.Worker = &models.WorkerLiveness{PID: 4812, ProcState: "R", Alive: true}
	_ = st.SaveSession(ctx, sessLoaded)

	fakeColab.mu.Lock()
	fakeColab.zombieMode = true
	fakeColab.mu.Unlock()

	eng.SweepKeepaliveAndZombiesOnce(ctx, false)

	afterSweep, _ := st.GetSession(ctx, sess.ID)
	if afterSweep.Status != models.SessionStopped {
		t.Fatalf("expected zombie session to be auto-released & STOPPED, got %s", afterSweep.Status)
	}
	waste, _ := st.GetSavedWasteStats(ctx)
	if waste.ZombieAbortsCount != 1 {
		t.Fatalf("expected ZombieAbortsCount=1, got %+v", waste)
	}
}

func TestDemoDriverCellsAndShardedExperiment(t *testing.T) {
	ctx := context.Background()
	st := newTestStore()
	hub := &testHub{}
	for i := 1; i <= 3; i++ {
		_ = st.SaveAccount(ctx, &models.Account{
			ID:               fmt.Sprintf("acc_demo_%d", i),
			Email:            fmt.Sprintf("demo%d@colabhive.dev", i),
			Health:           models.HealthHealthy,
			Enabled:          true,
			MaxConcurrentVMs: 4,
			PaidCUBalance:    300,
			SchedulableCU:    300,
			Tags:             []string{"demo"},
		})
	}
	rtr := router.NewServiceWithDirs(st, nil, hub, t.TempDir(), "", "", "linux")
	eng := NewConcreteEngine(st, nil, rtr, hub, Options{
		DemoMode:               true,
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	// 1. Virtual Notebook Cell lifecycle + valid PNG plot verification
	sess, err := eng.CreateSession(ctx, models.SessionAllocRequest{
		Name:        "demo-wb",
		Accelerator: models.AcceleratorG4,
		HighMem:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cell1, exec1, err := eng.MutateCell(ctx, sess.ID, models.CellMutationRequest{
		Operation: "add_code",
		Content:   "import matplotlib.pyplot as plt\nplt.plot([1,2,3])",
		RunOnAdd:  true,
	})
	if err != nil || cell1 == nil || exec1 == nil || len(exec1.RichOutputs) != 1 {
		t.Fatalf("MutateCell(add_code+run) failed: err=%v exec=%+v", err, exec1)
	}
	artFile, _ := eng.ArtifactPath(sess.ID, "plot_001_00.png")
	rawPNG, err := os.ReadFile(artFile)
	if err != nil {
		t.Fatalf("expected PNG artifact on disk: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(rawPNG)); err != nil {
		t.Fatalf("generated demo PNG is not a valid PNG image: %v", err)
	}

	// 2. 3-Shard Multi-Account Experiment + Deterministic JSONL Merge & Zero-Leak Teardown
	outDir := t.TempDir()
	exp, err := eng.LaunchShardedExperiment(ctx, models.ExperimentLaunchRequest{
		ExperimentID:       "exp_demo_3x",
		Title:              "Qwen3-27B Eval",
		ScriptPath:         "eval_shard.py",
		Accelerator:        models.AcceleratorG4,
		HighMem:            true,
		NumShards:          3,
		Strategy:           models.StrategyRoundRobin,
		LocalOutputDir:     outDir,
		AutoStopOnComplete: true,
	})
	if err != nil {
		t.Fatalf("LaunchShardedExperiment failed: %v", err)
	}
	if exp.NumShards != 3 || len(exp.Shards) != 3 {
		t.Fatalf("expected 3 shards, got %+v", exp)
	}

	teardown, err := eng.CollectAndTeardownExperiment(ctx, exp.ID, true)
	if err != nil {
		t.Fatalf("CollectAndTeardownExperiment failed: %v", err)
	}
	if !teardown.AllVMsUnassigned || teardown.MergedRecordsCount != 75 {
		t.Fatalf("unexpected teardown summary: %+v", teardown)
	}
	mergedData, err := os.ReadFile(teardown.MergedJSONLPath)
	if err != nil || len(strings.Split(strings.TrimSpace(string(mergedData)), "\n")) != 75 {
		t.Fatalf("merged.jsonl missing or wrong line count: err=%v", err)
	}
}

func TestExternalAssignmentDiscoveryAdoptionLogStreamingAndUnassignLifecycle(t *testing.T) {
	ctx := context.Background()

	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	mockSrv := mockcolab.NewServer()
	defer mockSrv.Close()

	hub := &testHub{}
	poolSvc := pool.NewConcreteService(st, hub, mockSrv.PoolConfig("sync-test-secret"))
	rtr := router.NewService(st, poolSvc, hub)

	eng := NewConcreteEngine(st, poolSvc, rtr, hub, Options{
		TunnelBaseURL:          mockSrv.URL,
		PublicAPIBaseURL:       mockSrv.URL,
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	// 1. Register an AI Ultra user account on mockcolab with an ALREADY-RUNNING external G4 task
	//    (started via `colab` CLI / Web UI before ColabHive imported the account!).
	mockSrv.RegisterAccount(mockcolab.MockAccount{
		Email:            "lab-ultra@example.com",
		DisplayName:      "Lab AI Ultra",
		SubscriptionTier: "SUBSCRIPTION_TIER_AI_ULTRA",
		PaidCUBalance:    633.24,
		MaxVMs:           5,
		EligibleGPUs:     []string{"G4", "H100", "A100", "L4", "T4"},
		PublicAPIEnabled: true,
		AuthCode:         "4/0A_ext_sync_code",
		RefreshToken:     "1//0ext_sync_refresh",
		AccessToken:      "ya29.ext_sync_access",
	})

	workerLogContent := strings.Join([]string{
		"[2026-11-22 00:01:00] Loading Qwen3-27B BF16 weights onto NVIDIA RTX PRO 6000 Blackwell...",
		"[2026-11-22 00:01:12] Shard 2/3 initialized | VRAM=51.8/96.0 GB",
		"[2026-11-22 00:02:05] [step 40/200] loss=0.4128 | mmlu_pro=0.8510",
	}, "\n") + "\n"

	extEndpoint, extKernelID := mockSrv.SeedExternalAssignment(
		"lab-ultra@example.com",
		models.AcceleratorG4,
		true,
		"qwen3_27b_external_eval.ipynb",
		"busy",
		map[string][]byte{
			"shard_2.jsonl": []byte(`{"step":40,"acc":0.851}` + "\n"),
			"worker.log":    []byte(workerLogContent),
		},
	)

	// Onboard the account into ColabHive pool
	pending, err := poolSvc.StartOAuth(ctx, models.OAuthStartRequest{Alias: "Lab Ultra Main"})
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	acc, err := poolSvc.CompleteOAuth(ctx, pending.State, "4/0A_ext_sync_code")
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if acc.ReferenceCycleCU != 2500.0 {
		t.Fatalf("expected AI_ULTRA ReferenceCycleCU=2500.0, got %.2f", acc.ReferenceCycleCU)
	}

	// 2. Run SyncRemoteAssignments -> discovers & adopts external G4 VM non-intrusively!
	syncRes, err := eng.SyncRemoteAssignments(ctx)
	if err != nil {
		t.Fatalf("SyncRemoteAssignments failed: %v", err)
	}
	if syncRes.AdoptedCount != 1 || syncRes.ActiveCount != 1 || len(syncRes.Sessions) != 1 {
		t.Fatalf("expected AdoptedCount=1 ActiveCount=1, got %+v", syncRes)
	}

	adopted := syncRes.Sessions[0]
	if !adopted.AdoptedExternal {
		t.Fatalf("expected AdoptedExternal=true on adopted session: %+v", adopted)
	}
	if adopted.Endpoint != extEndpoint || adopted.Accelerator != models.AcceleratorG4 || adopted.Shape != models.ShapeHighRAM {
		t.Fatalf("unexpected adopted hardware metadata: endpoint=%s accel=%s shape=%s", adopted.Endpoint, adopted.Accelerator, adopted.Shape)
	}
	if adopted.KernelID != extKernelID {
		t.Fatalf("expected KernelID=%q bound from /api/sessions, got %q", extKernelID, adopted.KernelID)
	}
	if adopted.Status != models.SessionBusy {
		t.Fatalf("expected Status=BUSY (kernel busy + GPU 92%%), got %s", adopted.Status)
	}
	if adopted.NotebookName != "qwen3_27b_external_eval.ipynb" {
		t.Fatalf("expected NotebookName=qwen3_27b_external_eval.ipynb, got %q", adopted.NotebookName)
	}
	if !strings.HasPrefix(adopted.Name, "colab-g4-") || !strings.Contains(adopted.Name, "(") {
		t.Fatalf("expected informative adopted session name 'colab-g4-<short> (<hint>)', got %q", adopted.Name)
	}
	if adopted.Resources == nil || len(adopted.Resources.GPUs) != 1 || adopted.Resources.GPUs[0].MemoryTotalBytes <= 0 {
		t.Fatalf("expected populated VMResourceTelemetry from /api/colab/resources, got %+v", adopted.Resources)
	}
	if len(adopted.RecentLogs) != 3 || !strings.Contains(adopted.RecentLogs[2], "mmlu_pro=0.8510") {
		t.Fatalf("expected 3 tail lines in RecentLogs from /content/worker.log, got %+v", adopted.RecentLogs)
	}

	// Verify initial log lines were published over WebSocket as WSEventSessionOutput (`session_output`)
	hub.mu.Lock()
	sawStream := false
	for _, ev := range hub.events {
		if ev.Type == models.WSEventSessionStream {
			if chunk, ok := ev.Payload.(models.SessionStreamChunk); ok && chunk.SessionID == adopted.ID && strings.Contains(chunk.Text, "mmlu_pro=0.8510") {
				sawStream = true
			}
		}
	}
	hub.events = nil
	hub.mu.Unlock()
	if !sawStream {
		t.Fatal("expected initial worker.log tail published as WSEventSessionStream")
	}

	// 3. Append new progress line to /content/worker.log on remote VM -> verify idempotent sync (AdoptedCount=0) & delta stream!
	updatedLog := workerLogContent + "[2026-11-22 00:02:35] [step 60/200] loss=0.3891 | mmlu_pro=0.8640\n"
	mockSrv.WriteVMFile(extEndpoint, "worker.log", []byte(updatedLog))

	syncRes2, err := eng.SyncRemoteAssignments(ctx)
	if err != nil {
		t.Fatalf("second SyncRemoteAssignments failed: %v", err)
	}
	if syncRes2.AdoptedCount != 0 || syncRes2.ActiveCount != 1 {
		t.Fatalf("expected AdoptedCount=0 ActiveCount=1 on repeat sync, got %+v", syncRes2)
	}
	if len(syncRes2.Sessions[0].RecentLogs) != 4 || !strings.Contains(syncRes2.Sessions[0].RecentLogs[3], "step 60/200") {
		t.Fatalf("expected updated 4th log line in RecentLogs, got %+v", syncRes2.Sessions[0].RecentLogs)
	}

	hub.mu.Lock()
	sawDelta := false
	for _, ev := range hub.events {
		if ev.Type == models.WSEventSessionStream {
			if chunk, ok := ev.Payload.(models.SessionStreamChunk); ok && chunk.SessionID == adopted.ID {
				if strings.Contains(chunk.Text, "step 60/200") && !strings.Contains(chunk.Text, "Loading Qwen3-27B") {
					sawDelta = true
				}
			}
		}
	}
	hub.mu.Unlock()
	if !sawDelta {
		t.Fatal("expected only newly appended log line streamed on subsequent SyncRemoteAssignments")
	}

	// 4. Verify Watchdog / Guardrail loops NEVER auto-stop AdoptedExternal sessions!
	eng.SweepKeepaliveAndZombiesOnce(ctx, false)
	eng.PollResourcesAndGuardrailsOnce(ctx)
	stillAlive, err := eng.GetSession(ctx, adopted.ID)
	if err != nil || stillAlive.Status == models.SessionStopped {
		t.Fatalf("safety violation: watchdog stopped AdoptedExternal session: %+v (%v)", stillAlive, err)
	}

	// 5. Simulate external task finishing & unassigning upstream -> transitions AdoptedExternal session to STOPPED!
	mockSrv.UnassignExternalEndpoint(extEndpoint)

	syncRes3, err := eng.SyncRemoteAssignments(ctx)
	if err != nil {
		t.Fatalf("SyncRemoteAssignments after upstream unassign failed: %v", err)
	}
	if syncRes3.ActiveCount != 0 || len(syncRes3.Sessions) != 0 {
		t.Fatalf("expected 0 active sessions after upstream unassign, got %+v", syncRes3)
	}

	allListed, err := eng.ListSessions(ctx, models.SessionFilter{ActiveOnly: false})
	if err != nil || len(allListed) != 1 {
		t.Fatalf("ListSessions(ActiveOnly=false) expected 1 historical session, got %d (%v)", len(allListed), err)
	}
	if allListed[0].Status != models.SessionStopped || allListed[0].StoppedAt == nil {
		t.Fatalf("expected AdoptedExternal session transitioned to STOPPED, got status=%s stoppedAt=%v",
			allListed[0].Status, allListed[0].StoppedAt)
	}
	rolledAcc, _ := poolSvc.GetAccount(ctx, acc.ID)
	if rolledAcc.ActiveAssignments != 0 {
		t.Fatalf("expected account ActiveAssignments rolled back to 0 after upstream unassign, got %d", rolledAcc.ActiveAssignments)
	}
}

func TestCrossAccountRedundancyFailoverMidCallDeadVMRecoveryAndRealPIDWatchdog(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var (
		mu             sync.Mutex
		deadEndpoints  = make(map[string]bool)
		bootFailAcc4   = true
		spawnCounter   = 19400
	)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		rt := r.FormValue("refresh_token")
		if strings.Contains(rt, "invalid_grant") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"tok_%s","expires_in":3600}`, rt)))
	})
	mux.HandleFunc("/tun/m/assign", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		acc := r.URL.Query().Get("accelerator")
		nbh := r.URL.Query().Get("nbh")
		switch {
		case strings.Contains(auth, "acc_429"):
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"error":"rate_limited"}`))
			return
		case strings.Contains(auth, "acc_412"):
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"error":"TOO_MANY_ACTIVE_RUNTIMES"}`))
			return
		case strings.Contains(auth, "acc_400") && acc == "G4":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"error":"tier_restricted"}`))
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + fmt.Sprintf(`{"acc":%q,"nbh":%q,"token":"xsrf-1","variant":"GPU"}`, acc, nbh)))
			return
		}
		epTag := "healthy2"
		if strings.Contains(auth, "acc_boot") {
			epTag = "bootfail"
		} else if strings.Contains(auth, "acc_primary") {
			epTag = "primary1"
		} else if strings.Contains(auth, "shard_") {
			parts := strings.Split(auth, "shard_")
			epTag = "shard" + parts[len(parts)-1]
		}
		ep := fmt.Sprintf("gpu-%s-s-%s", strings.ToLower(acc), epTag)
		proxyURL := srv.URL + "/vm/" + ep
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + fmt.Sprintf(
			`{"accelerator":%q,"endpoint":%q,"variant":1,"runtimeProxyInfo":{"token":"ptok-%s","tokenExpiresInSeconds":3600,"url":%q}}`,
			acc, ep, epTag, proxyURL,
		)))
	})
	mux.HandleFunc("/tun/m/unassign/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"token":"u-tok"}`))
			return
		}
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{}`))
	})
	mux.HandleFunc("/tun/m/assignments", func(w http.ResponseWriter, r *http.Request) {
		// Return empty assignments when primary1 died upstream!
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"assignments":[]}`))
	})
	mux.HandleFunc("/vm/", func(w http.ResponseWriter, r *http.Request) {
		trimmed := strings.TrimPrefix(r.URL.Path, "/vm/")
		slash := strings.IndexByte(trimmed, '/')
		if slash < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ep := trimmed[:slash]
		sub := trimmed[slash:]

		mu.Lock()
		isDead := deadEndpoints[ep]
		failBoot := bootFailAcc4 && strings.Contains(ep, "bootfail")
		mu.Unlock()

		if isDead {
			http.Error(w, "VM preempted upstream", http.StatusBadGateway)
			return
		}
		if failBoot && (sub == "/api/sessions" || sub == "/api/kernels") {
			http.Error(w, "tornado proxy 500", http.StatusInternalServerError)
			return
		}

		switch {
		case sub == "/api/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"js-1","kernel":{"id":"k-1"}}`))
		case sub == "/api/colab/resources":
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"memory":{"totalBytes":68000000000,"freeBytes":50000000000},"gpus":[{"name":"NVIDIA RTX PRO 6000 Blackwell","memoryUsedBytes":1000000000,"memoryTotalBytes":103079215104,"gpuUtilization":0.5,"memoryUtilization":0.1,"temperatureC":55,"everUsed":true}]}`))
		case strings.HasPrefix(sub, "/api/contents/"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"type":"file"}`))
		case strings.HasSuffix(sub, "/channels"):
			conn, uErr := upgrader.Upgrade(w, r, nil)
			if uErr != nil {
				return
			}
			defer conn.Close()
			for {
				var req jupyterWireMsg
				if conn.ReadJSON(&req) != nil {
					return
				}
				if asString(req.Header["msg_type"]) != "execute_request" {
					continue
				}
				code := asString(req.Content["code"])
				parent := req.Header
				outText := fmt.Sprintf("executed on %s\n", ep)
				if strings.Contains(code, "subprocess.Popen") {
					mu.Lock()
					spawnCounter++
					realPID := spawnCounter
					mu.Unlock()
					outText = fmt.Sprintf("Started shard worker PID=%d\n", realPID)
				}
				_ = conn.WriteJSON(jupyterWireMsg{Channel: "iopub", Header: map[string]any{"msg_type": "stream"}, ParentHeader: parent, Content: map[string]any{"name": "stdout", "text": outText}})
				_ = conn.WriteJSON(jupyterWireMsg{Channel: "iopub", Header: map[string]any{"msg_type": "status"}, ParentHeader: parent, Content: map[string]any{"execution_state": "idle"}})
				_ = conn.WriteJSON(jupyterWireMsg{Channel: "shell", Header: map[string]any{"msg_type": "execute_reply"}, ParentHeader: parent, Content: map[string]any{"status": "ok", "execution_count": 1}})
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	hub := &testHub{}
	poolSvc := pool.NewConcreteService(st, hub, pool.Config{
		MasterSecret:      "failover-test-secret",
		GoogleTokenURI:    srv.URL + "/oauth/token",
		ColabTunnelDomain: srv.URL,
		ColabGAPIDomain:   srv.URL,
		ColabPublicDomain: srv.URL,
	})
	rtr := router.NewService(st, poolSvc, hub)
	eng := NewConcreteEngine(st, poolSvc, rtr, hub, Options{
		TunnelBaseURL:          srv.URL,
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	// Seed broken proxy config (dead port 127.0.0.1:1) for acc_proxy
	deadProxy, err := poolSvc.SaveProxy(ctx, &models.ProxyConfig{
		ID:   "prx_dead",
		Name: "Dead SOCKS5",
		URL:  "socks5://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}

	seedAcc := func(id, email, rt, accTok string, prio int, proxyID string) {
		exp := -5 * time.Minute // force refresh when accTok == ""
		if accTok != "" {
			exp = 1 * time.Hour
		}
		_, sErr := poolSvc.SeedAccountCredentials(ctx, &models.Account{
			ID:               id,
			Email:            email,
			SubscriptionTier: models.TierAIUltra,
			Health:           models.HealthHealthy,
			Enabled:          true,
			Priority:         prio,
			ProxyID:          proxyID,
			MaxConcurrentVMs: 5,
			PaidCUBalance:    600,
			SchedulableCU:    600,
			GPUEntitlements: map[models.AcceleratorType]models.GPUEntitlement{
				models.AcceleratorG4: {
					Accelerator:        models.AcceleratorG4,
					Status:             models.EntitlementAvailable,
					CalibratedBurnRate: 9.42,
					CalibrationSource:  models.CalibrationLive,
				},
			},
		}, rt, accTok, exp)
		if sErr != nil {
			t.Fatal(sErr)
		}
	}

	// Priority order ensures CreateSession hits EVERY failure mode in sequence before landing on acc_primary!
	seedAcc("acc_proxy", "proxy@lab.dev", "rt_proxy", "ya29.proxy", 100, deadProxy.ID)
	seedAcc("acc_oauth", "oauth@lab.dev", "rt_invalid_grant", "", 90, "")
	seedAcc("acc_429", "rl@lab.dev", "rt_429", "ya29.acc_429", 80, "")
	seedAcc("acc_412", "cap@lab.dev", "rt_412", "ya29.acc_412", 70, "")
	seedAcc("acc_400", "tier@lab.dev", "rt_400", "ya29.acc_400", 60, "")
	seedAcc("acc_boot", "boot@lab.dev", "rt_boot", "ya29.acc_boot", 50, "")
	seedAcc("acc_primary", "primary@lab.dev", "rt_pri", "ya29.acc_primary", 40, "")
	seedAcc("acc_backup", "backup@lab.dev", "rt_bak", "ya29.acc_backup", 30, "")

	// 1. CreateSession(G4) must survive proxy error -> invalid_grant -> 429 -> 412 -> 400 -> kernel 500 -> succeed on acc_primary!
	sess1, err := eng.CreateSession(ctx, models.SessionAllocRequest{
		Accelerator:  models.AcceleratorG4,
		HighMem:      true,
		AutoFallback: true,
		StickyKey:    "mcp-sticky-failover",
	})
	if err != nil {
		t.Fatalf("expected CreateSession to fail over across 6 broken accounts onto acc_primary, got error: %v", err)
	}
	if sess1.AccountID != "acc_primary" {
		t.Fatalf("expected session on acc_primary, got %s", sess1.AccountID)
	}

	// Verify exact health/entitlement classifications & preserved CalibratedBurnRate!
	checkHealth := func(id string, want models.AccountHealth) {
		a, _ := poolSvc.GetAccount(ctx, id)
		if a.Health != want {
			t.Fatalf("account %s health = %s, want %s (reason=%q)", id, a.Health, want, a.HealthReason)
		}
	}
	checkHealth("acc_proxy", models.HealthProxyError)
	checkHealth("acc_oauth", models.HealthAuthExpired)
	checkHealth("acc_429", models.HealthCooldownRateLimit)
	checkHealth("acc_412", models.HealthMaxConcurrency)

	acc400After, _ := poolSvc.GetAccount(ctx, "acc_400")
	if ent := acc400After.GPUEntitlements[models.AcceleratorG4]; ent.Status != models.EntitlementTierRestricted || ent.CalibratedBurnRate != 9.42 {
		t.Fatalf("expected acc_400 G4 TIER_RESTRICTED with preserved CalibratedBurnRate=9.42, got %+v", ent)
	}
	accBootAfter, _ := poolSvc.GetAccount(ctx, "acc_boot")
	if ent := accBootAfter.GPUEntitlements[models.AcceleratorG4]; ent.Status != models.EntitlementCooldown {
		t.Fatalf("expected acc_boot G4 COOLDOWN after bootstrap failure, got %+v", ent)
	}

	// 2. Mid-Call Dead VM Auto-Failover in ExecuteCode:
	//    Kill sess1's endpoint upstream -> implicit ExecuteCode(SessionID: "") marks sess1 LOST (KeepaliveHealthy=false)
	//    and transparently provisions a fresh VM on acc_backup and succeeds!
	mu.Lock()
	deadEndpoints[sess1.Endpoint] = true
	mu.Unlock()

	execRes, err := eng.ExecuteCode(ctx, models.KernelExecRequest{
		Code:             "print('resilient')",
		AutoProvisionGPU: models.AcceleratorG4,
		StickyKey:        "mcp-sticky-failover",
	})
	if err != nil {
		t.Fatalf("expected transparent mid-call failover to acc_backup on dead sticky VM, got: %v", err)
	}
	if !strings.Contains(execRes.Stdout, "healthy2") {
		t.Fatalf("expected execution on backup VM endpoint healthy2, got stdout=%q", execRes.Stdout)
	}
	oldSess, _ := st.GetSession(ctx, sess1.ID)
	if oldSess.Status != models.SessionLost || oldSess.KeepaliveHealthy {
		t.Fatalf("expected dead sticky VM marked LOST with KeepaliveHealthy=false, got status=%s healthy=%v",
			oldSess.Status, oldSess.KeepaliveHealthy)
	}

	// 3. AdoptedExternal protection: PollResourcesAndGuardrailsOnce must NOT charge contributor's external VM,
	//    and StopSession must refuse non-exact names on AdoptedExternal sessions!
	_, _ = poolSvc.SeedAccountCredentials(ctx, &models.Account{
		ID:               "acc_contrib_ext",
		Email:            "donor-ext@edu.org",
		SubscriptionTier: models.TierProPlus,
		Health:           models.HealthHealthy,
		Enabled:          true,
		IsContributor:    true,
		PaidCUBalance:    200,
		Guardrails: models.ContributorGuardrails{
			MinReserveCU:      50,
			MaxDonatedCUTotal: 100,
			AllowPaidCUBurn:   true,
		},
	}, "rt_cext", "ya29.cext", 1*time.Hour)
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:              "sess_donor_external",
		Name:            "colab-g4-personal",
		AccountID:       "acc_contrib_ext",
		Accelerator:     models.AcceleratorG4,
		Status:          models.SessionBusy,
		BurnRateCUHr:    9.66,
		AdoptedExternal: true,
		CreatedAt:       time.Now().UTC(),
	})
	eng.PollResourcesAndGuardrailsOnce(ctx)
	donorAfterPoll, _ := poolSvc.GetAccount(ctx, "acc_contrib_ext")
	if donorAfterPoll.Guardrails.DonatedCUUsedTotal != 0 || donorAfterPoll.Guardrails.DonatedCUUsedToday != 0 {
		t.Fatalf("contributor's personal AdoptedExternal VM was wrongly charged to DonatedCUUsed: %+v", donorAfterPoll.Guardrails)
	}
	if stopErr := eng.StopSession(ctx, "COLAB-G4-PERSONAL"); stopErr == nil {
		t.Fatal("expected case-folded fuzzy StopSession on AdoptedExternal session to be refused")
	}

	// 4. Enforce IdleAutoKillMin on non-external idle sessions
	apiKey, err := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:            "AutoKill Key",
		IdleAutoKillMin: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleExec := time.Now().UTC().Add(-10 * time.Minute)
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:              "sess_idle_reap",
		Name:            "idle-reap-vm",
		AccountID:       "acc_backup",
		Accelerator:     models.AcceleratorG4,
		Status:          models.SessionIdle,
		APIKeyID:        apiKey.ID,
		LastExecutionAt: &staleExec,
		CreatedAt:       staleExec,
	})
	eng.SweepKeepaliveAndZombiesOnce(ctx, false)
	reapedSess, _ := st.GetSession(ctx, "sess_idle_reap")
	if reapedSess.Status != models.SessionStopped {
		t.Fatalf("expected IdleAutoKillMin=5m to stop 10m-idle session, got %s", reapedSess.Status)
	}

	// 5. Shards=4 Parallel LaunchShardedExperiment on real (non-demo) engine -> 4 distinct accounts + real spawned PID!
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("acc_sh_%d", i)
		seedAcc(id, id+"@lab.dev", fmt.Sprintf("rt_sh_%d", i), fmt.Sprintf("ya29.shard_%d", i), 200, "")
	}
	exp, err := eng.LaunchShardedExperiment(ctx, models.ExperimentLaunchRequest{
		ExperimentID: "exp_real_4x",
		Accelerator:  models.AcceleratorG4,
		HighMem:      true,
		NumShards:    4,
		Strategy:     models.StrategyWasteZero,
	})
	if err != nil {
		t.Fatalf("LaunchShardedExperiment(4 shards) failed: %v", err)
	}
	shardAccounts := make(map[string]bool)
	for _, sh := range exp.Shards {
		shardAccounts[sh.AccountID] = true
		if sh.PID <= 19400 {
			t.Fatalf("expected real parsed PID (>19400) instead of hardcoded 4800+shard*17, got PID=%d", sh.PID)
		}
	}
	if len(shardAccounts) != 4 {
		t.Fatalf("expected 4 shards distributed across 4 distinct accounts, got %v", shardAccounts)
	}
}

func TestRuntimeAuditRegressionsLifecycleIsolationAndJupyterSafety(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var (
		mu                sync.Mutex
		unassignedEps     []string
		failUnassign      atomic.Bool
		requireFreshToken atomic.Bool
		dropPostWrite     atomic.Bool
		hangExecCh        chan struct{}
		delayedReplyErr   atomic.Bool
		execRequestsSeen  atomic.Int32
		uploadedContents  = make(map[string][]byte)
		liveEndpoint      atomic.Value
		firstAssignDone   = make(chan struct{})
		firstAssignOnce   sync.Once
	)
	liveEndpoint.Store("gpu-g4-s-audit01")

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var srv *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"ya29.audit_ok","expires_in":3600,"token_type":"Bearer"}`))
	})
	mux.HandleFunc("/tun/m/assign", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"acc":"G4","nbh":"` + r.URL.Query().Get("nbh") + `","token":"xsrf-1","variant":"GPU"}`))
			return
		}
		ep, _ := liveEndpoint.Load().(string)
		resp := fmt.Sprintf(
			`{"accelerator":"G4","endpoint":%q,"variant":1,"runtimeProxyInfo":{"token":"tok-initial","tokenExpiresInSeconds":3600,"url":%q}}`,
			ep, srv.URL,
		)
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + resp))
		firstAssignOnce.Do(func() { close(firstAssignDone) })
	})
	mux.HandleFunc("/tun/m/assignments", func(w http.ResponseWriter, r *http.Request) {
		ep, _ := liveEndpoint.Load().(string)
		if ep == "" {
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"assignments":[]}`))
			return
		}
		resp := fmt.Sprintf(
			`{"assignments":[{"accelerator":"G4","endpoint":%q,"variant":"GPU","runtimeProxyInfo":{"token":"tok-refreshed","tokenExpiresInSeconds":3600,"url":%q}}]}`,
			ep, srv.URL,
		)
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + resp))
	})
	mux.HandleFunc("/tun/m/unassign/", func(w http.ResponseWriter, r *http.Request) {
		if failUnassign.Load() {
			http.Error(w, "upstream unassign 500", http.StatusInternalServerError)
			return
		}
		ep := strings.TrimPrefix(r.URL.Path, "/tun/m/unassign/")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"token":"u-xsrf"}`))
			return
		}
		mu.Lock()
		unassignedEps = append(unassignedEps, ep)
		mu.Unlock()
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{}`))
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"js-audit","kernel":{"id":"k-audit"}}`))
	})
	mux.HandleFunc("/api/colab/resources", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"memory":{"totalBytes":68000000000,"freeBytes":50000000000},"gpus":[]}`))
	})
	mux.HandleFunc("/api/contents/", func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path is automatically unescaped by net/http ONLY if `?` and `#` were %3F / %23 escaped on wire!
		relPath := strings.TrimPrefix(r.URL.Path, "/api/contents/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			var body struct {
				Content string `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			decoded, _ := base64.StdEncoding.DecodeString(body.Content)
			uploadedContents[relPath] = decoded
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"type":"file"}`))
		case http.MethodGet:
			data, ok := uploadedContents[relPath]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"type":"file","format":"base64","content":%q}`, base64.StdEncoding.EncodeToString(data))))
		case http.MethodDelete:
			delete(uploadedContents, relPath)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("/api/kernels/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/channels") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if requireFreshToken.Load() && r.Header.Get("X-Colab-Runtime-Proxy-Token") != "tok-refreshed" {
			http.Error(w, "expired runtime proxy capability token", http.StatusUnauthorized)
			return
		}
		conn, uErr := upgrader.Upgrade(w, r, nil)
		if uErr != nil {
			return
		}
		defer conn.Close()
		for {
			var req jupyterWireMsg
			if conn.ReadJSON(&req) != nil {
				return
			}
			if asString(req.Header["msg_type"]) != "execute_request" {
				continue
			}
			code := asString(req.Content["code"])
			if !strings.Contains(code, "os.makedirs('/content'") {
				execRequestsSeen.Add(1)
			}
			mu.Lock()
			waitCh := hangExecCh
			mu.Unlock()
			if waitCh != nil && strings.Contains(code, "HANG_CELL") {
				<-waitCh
				return
			}
			if dropPostWrite.Load() && strings.Contains(code, "NON_IDEMPOTENT_MUTATION") {
				_ = conn.Close()
				return
			}
			parent := req.Header
			if delayedReplyErr.Load() && strings.Contains(code, "DELAYED_ERR") {
				// Send status:idle FIRST, then an unparented noise frame, then delayed execute_reply error!
				_ = conn.WriteJSON(jupyterWireMsg{Channel: "iopub", Header: map[string]any{"msg_type": "status"}, ParentHeader: parent, Content: map[string]any{"execution_state": "idle"}})
				_ = conn.WriteJSON(jupyterWireMsg{Channel: "iopub", Header: map[string]any{"msg_type": "status"}, ParentHeader: map[string]any{"msg_id": "unrelated_other_cell"}, Content: map[string]any{"execution_state": "busy"}})
				_ = conn.WriteJSON(jupyterWireMsg{Channel: "shell", Header: map[string]any{"msg_type": "execute_reply"}, ParentHeader: parent, Content: map[string]any{"status": "error", "ename": "RuntimeError", "evalue": "delayed failure", "execution_count": 2}})
				continue
			}
			outText := "ok\n"
			if strings.Contains(code, "subprocess.Popen") {
				outText = "Started shard 0/1 worker PID=29104\n"
			} else if strings.Contains(code, "p=29104") {
				mu.Lock()
				uploadedContents["content/shard_0.jsonl"] = []byte("{\"idx\":0,\"ok\":true}\n")
				mu.Unlock()
				outText = "\n__CH_META__:{\"exit_code\":0,\"has_jsonl\":true,\"records\":10,\"tail\":\"shard done\"}\n"
			}
			_ = conn.WriteJSON(jupyterWireMsg{Channel: "iopub", Header: map[string]any{"msg_type": "stream"}, ParentHeader: parent, Content: map[string]any{"name": "stdout", "text": outText}})
			_ = conn.WriteJSON(jupyterWireMsg{Channel: "iopub", Header: map[string]any{"msg_type": "status"}, ParentHeader: parent, Content: map[string]any{"execution_state": "idle"}})
			_ = conn.WriteJSON(jupyterWireMsg{Channel: "shell", Header: map[string]any{"msg_type": "execute_reply"}, ParentHeader: parent, Content: map[string]any{"status": "ok", "execution_count": 1}})
		}
	})

	srv = httptest.NewServer(mux)
	defer srv.Close()

	hub := &testHub{}
	poolSvc := pool.NewConcreteService(st, hub, pool.Config{
		MasterSecret:      "audit-test-secret",
		GoogleTokenURI:    srv.URL + "/oauth/token",
		ColabTunnelDomain: srv.URL,
		ColabGAPIDomain:   srv.URL,
		ColabPublicDomain: srv.URL,
	})
	rtr := router.NewService(st, poolSvc, hub)
	eng := NewConcreteEngine(st, poolSvc, rtr, hub, Options{
		TunnelBaseURL:          srv.URL,
		PostAssignWarmup:       350 * time.Millisecond,
		ResourcePollInterval:   3600 * time.Second, // 1h delta for exact CU math in poll test
		ArtifactsDir:           t.TempDir(),
		DisableBackgroundLoops: true,
	})
	defer eng.Close()

	seedAuditAcc := func(id, email string) {
		_, sErr := poolSvc.SeedAccountCredentials(ctx, &models.Account{
			ID:               id,
			Email:            email,
			SubscriptionTier: models.TierAIUltra,
			Health:           models.HealthHealthy,
			Enabled:          true,
			MaxConcurrentVMs: 4,
			PaidCUBalance:    500,
			SchedulableCU:    500,
		}, "rt_"+id, "ya29."+id, 1*time.Hour)
		if sErr != nil {
			t.Fatal(sErr)
		}
	}
	seedAuditAcc("acc_a1", "a1@lab.dev")
	seedAuditAcc("acc_a2", "a2@lab.dev")

	// 1. Warmup cancellation MUST unassign committed endpoint via WithoutCancel AND SyncRemoteAssignments must skip inFlightEndpoints!
	cancelCtx, cancelWarmup := context.WithCancel(ctx)
	var syncAdoptedDuringInFlight int
	var syncWg sync.WaitGroup
	syncWg.Add(1)
	go func() {
		defer syncWg.Done()
		select {
		case <-firstAssignDone:
		case <-time.After(10 * time.Second):
			cancelWarmup()
			return
		}
		time.Sleep(25 * time.Millisecond)
		if syncRes, sErr := eng.SyncRemoteAssignments(ctx); sErr == nil && syncRes != nil {
			mu.Lock()
			syncAdoptedDuringInFlight = syncRes.AdoptedCount
			mu.Unlock()
		}
		cancelWarmup()
	}()
	_, warmErr := eng.CreateSession(cancelCtx, models.SessionAllocRequest{Accelerator: models.AcceleratorG4})
	cancelWarmup()
	syncWg.Wait()
	if warmErr == nil {
		t.Fatal("expected CreateSession to fail when context cancels during PostAssignWarmup")
	}
	mu.Lock()
	unassignedAfterCancel := len(unassignedEps)
	adoptedInFlight := syncAdoptedDuringInFlight
	mu.Unlock()
	if unassignedAfterCancel != 1 {
		t.Fatalf("expected canceled warmup to unassign leaked endpoint once, got %d (%v)", unassignedAfterCancel, unassignedEps)
	}
	if adoptedInFlight != 0 {
		t.Fatalf("expected SyncRemoteAssignments to skip in-flight endpoint, got AdoptedCount=%d", adoptedInFlight)
	}

	// 2. Create scoped API key & session; test ownership isolation, `?`/`#` file names, WS 401 token refresh, delayed reply error, ctx cancel, and no post-write replay!
	keyA, _ := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Key A", MaxBudgetCUDaily: 5.0})
	keyB, _ := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Key B", MaxBudgetCUDaily: 5.0})

	sess, err := eng.CreateSession(ctx, models.SessionAllocRequest{
		Name:        "key-a-vm",
		Accelerator: models.AcceleratorG4,
		APIKeyID:    keyA.ID,
		StickyKey:   "sk-a",
	})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Cross-tenant ExecuteCode with keyB MUST be rejected!
	if _, err := eng.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID: sess.ID,
		APIKeyID:  keyB.ID,
		Code:      "print(1)",
	}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected cross-tenant ExecuteCode rejection, got: %v", err)
	}

	// Special `?` and `#` filename roundtrip via `/api/contents/` URL segment escaping!
	trickyPath := "/content/sub/report?v=1#final.txt"
	wantBytes := []byte("escaped-contents-ok")
	if err := eng.UploadFile(ctx, sess.ID, trickyPath, wantBytes); err != nil {
		t.Fatalf("UploadFile(?#): %v", err)
	}
	gotBytes, err := eng.DownloadFile(ctx, sess.ID, trickyPath)
	if err != nil || !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("DownloadFile(?#) mismatch: got=%q err=%v", string(gotBytes), err)
	}
	if err := eng.DeleteFile(ctx, sess.ID, trickyPath); err != nil {
		t.Fatalf("DeleteFile(?#): %v", err)
	}

	// WS 401 capability token expiry -> refreshes runtimeProxyInfo token WITHOUT marking Google account HealthAuthExpired!
	requireFreshToken.Store(true)
	res401, err := eng.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID: sess.ID,
		APIKeyID:  keyA.ID,
		Code:      "print('after_proxy_refresh')",
	})
	if err != nil || res401.Status != "ok" {
		t.Fatalf("expected WS 401 to refresh runtimeProxyInfo and succeed, got res=%+v err=%v", res401, err)
	}
	accAfter401, _ := poolSvc.GetAccount(ctx, sess.AccountID)
	if accAfter401.Health == models.HealthAuthExpired {
		t.Fatal("WS proxy 401 must NOT mark Google OAuth account HealthAuthExpired")
	}

	// Delayed execute_reply error after status:idle + foreign frame noise MUST preserve error!
	delayedReplyErr.Store(true)
	resDelayed, err := eng.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID: sess.ID,
		APIKeyID:  keyA.ID,
		Code:      "DELAYED_ERR",
	})
	delayedReplyErr.Store(false)
	if err != nil || resDelayed.Status != "error" || resDelayed.ExecutionError == nil || resDelayed.ExecutionError.Value != "delayed failure" {
		t.Fatalf("expected delayed execute_reply error to be captured, got res=%+v err=%v", resDelayed, err)
	}

	// Context cancellation during blocked ReadMessage unblocks immediately and does NOT mark VM LOST!
	ch := make(chan struct{})
	mu.Lock()
	hangExecCh = ch
	mu.Unlock()
	execCtx, cancelExec := context.WithTimeout(ctx, 60*time.Millisecond)
	_, hangErr := eng.ExecuteCode(execCtx, models.KernelExecRequest{
		SessionID: sess.ID,
		APIKeyID:  keyA.ID,
		Code:      "HANG_CELL",
	})
	cancelExec()
	close(ch)
	mu.Lock()
	hangExecCh = nil
	mu.Unlock()
	if !errors.Is(hangErr, context.DeadlineExceeded) && !errors.Is(hangErr, context.Canceled) {
		t.Fatalf("expected context error on canceled ReadMessage, got: %v", hangErr)
	}
	sessAfterCancel, _ := eng.GetSession(ctx, sess.ID)
	if sessAfterCancel.Status == models.SessionLost {
		t.Fatal("context cancellation must NOT mark healthy VM SessionLost")
	}

	// Post-write WS drop MUST NOT auto-replay non-idempotent code on a second VM!
	execRequestsSeen.Store(0)
	dropPostWrite.Store(true)
	_, dropErr := eng.ExecuteCode(ctx, models.KernelExecRequest{
		Code:             "NON_IDEMPOTENT_MUTATION",
		AutoProvisionGPU: models.AcceleratorG4,
		StickyKey:        "sk-a",
		APIKeyID:         keyA.ID,
	})
	dropPostWrite.Store(false)
	if dropErr == nil {
		t.Fatal("expected error when WS drops mid-execution after WriteJSON")
	}
	if execRequestsSeen.Load() != 1 {
		t.Fatalf("non-idempotent code was replayed %d times after post-write WS drop (expected 1)", execRequestsSeen.Load())
	}

	// Failed upstream unassign (HTTP 500) MUST return real error and NOT mark STOPPED!
	failUnassign.Store(true)
	if err := eng.StopSession(ctx, sess.ID); err == nil {
		t.Fatal("expected StopSession to return error when upstream /tun/m/unassign returns HTTP 500")
	}
	stillActive, _ := eng.GetSession(ctx, sess.ID)
	if stillActive.Status == models.SessionStopped {
		t.Fatal("StopSession marked session STOPPED despite upstream HTTP 500 failure")
	}
	failUnassign.Store(false)

	// PollResourcesAndGuardrailsOnce charges keyA's CU budget (1h * 9.66 CU/h > 5.0 daily cap) and preempts/stops sess!
	eng.PollResourcesAndGuardrailsOnce(ctx)
	preemptedSess, _ := eng.GetSession(ctx, sess.ID)
	if preemptedSess.Status != models.SessionStopped {
		t.Fatalf("expected session exceeding scoped APIKey daily CU budget to be preempted & STOPPED, got %s", preemptedSess.Status)
	}

	// Deleted account nil-pointer guard in watchdog: orphan active session row for non-existent account must never panic!
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:        "sess_deleted_acc",
		Name:      "deleted-acc-vm",
		AccountID: "acc_nonexistent_deleted",
		Endpoint:  "gpu-g4-s-ghost",
		Status:    models.SessionIdle,
	})
	eng.SweepKeepaliveAndZombiesOnce(ctx, true)
	eng.PollResourcesAndGuardrailsOnce(ctx)

	// Experiments: reject `..` ExperimentID, persist APIKeyID, reject duplicate ExperimentID, upload ContentBase64, and honor forceKill=false!
	if _, err := eng.LaunchShardedExperiment(ctx, models.ExperimentLaunchRequest{
		ExperimentID: "../escape",
		Accelerator:  models.AcceleratorG4,
		NumShards:    1,
	}); err == nil {
		t.Fatal("expected path-traversal ExperimentID to be rejected")
	}

	inlinePayload := []byte("inline-config-yaml")
	exp1, err := eng.LaunchShardedExperiment(ctx, models.ExperimentLaunchRequest{
		ExperimentID: "exp_inline_1",
		Accelerator:  models.AcceleratorG4,
		NumShards:    1,
		APIKeyID:     keyB.ID,
		ScriptCode:   "import time; time.sleep(10)",
		UploadFiles: []models.ExperimentFileSpec{
			{
				RemotePath:    "/content/config.yaml",
				ContentBase64: base64.StdEncoding.EncodeToString(inlinePayload),
			},
		},
	})
	if err != nil {
		t.Fatalf("LaunchShardedExperiment(exp_inline_1) failed: %v", err)
	}
	if exp1.APIKeyID != keyB.ID {
		t.Fatalf("expected persisted Experiment.APIKeyID=%q, got %q", keyB.ID, exp1.APIKeyID)
	}
	mu.Lock()
	gotInline := uploadedContents["content/config.yaml"]
	mu.Unlock()
	if !bytes.Equal(gotInline, inlinePayload) {
		t.Fatalf("expected inline ContentBase64 uploaded to VM, got %q", string(gotInline))
	}

	// Duplicate ExperimentID MUST return ErrExperimentAlreadyExists!
	if _, dupErr := eng.LaunchShardedExperiment(ctx, models.ExperimentLaunchRequest{
		ExperimentID: "exp_inline_1",
		Accelerator:  models.AcceleratorG4,
		NumShards:    1,
	}); !errors.Is(dupErr, ErrExperimentAlreadyExists) {
		t.Fatalf("expected ErrExperimentAlreadyExists on duplicate ExperimentID, got: %v", dupErr)
	}

	// CollectAndTeardownExperiment with forceKill=false MUST NOT kill still-running shards!
	tdKeepAlive, err := eng.CollectAndTeardownExperiment(ctx, exp1.ID, false)
	if err != nil {
		t.Fatalf("CollectAndTeardownExperiment(forceKill=false) failed: %v", err)
	}
	if tdKeepAlive.AllVMsUnassigned || tdKeepAlive.ActiveVMsRemaining != 1 {
		t.Fatalf("expected running shard kept alive when forceKill=false, got %+v", tdKeepAlive)
	}
	shardSess, _ := eng.GetSession(ctx, exp1.Shards[0].SessionID)
	if shardSess.Status == models.SessionStopped {
		t.Fatal("CollectAndTeardownExperiment(forceKill=false) wrongly stopped a running shard VM")
	}

	// Case-insensitive duplicate ExperimentID (`EXP_INLINE_1` vs `exp_inline_1`) MUST ALSO return ErrExperimentAlreadyExists!
	if _, dupCaseErr := eng.LaunchShardedExperiment(ctx, models.ExperimentLaunchRequest{
		ExperimentID: "EXP_INLINE_1",
		Accelerator:  models.AcceleratorG4,
		NumShards:    1,
	}); !errors.Is(dupCaseErr, ErrExperimentAlreadyExists) {
		t.Fatalf("expected case-insensitive ErrExperimentAlreadyExists on EXP_INLINE_1, got: %v", dupCaseErr)
	}

	// Tear down exp1 first so its account slot is free for subsequent checks!
	tdForce, err := eng.CollectAndTeardownExperiment(ctx, exp1.ID, true)
	if err != nil || !tdForce.AllVMsUnassigned || tdForce.ActiveVMsRemaining != 0 {
		t.Fatalf("expected forceKill=true to unassign running shard, got %+v (%v)", tdForce, err)
	}

	// CollectAndTeardownExperiment MUST NEVER match a failed shard (`SessionID == ""`) by `SessionName`!
	keyVictim, _ := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Victim-Key"})
	victimByName, err := eng.CreateSession(ctx, models.SessionAllocRequest{
		Name:        "exp_failed_victim_s0",
		Accelerator: models.AcceleratorG4,
		APIKeyID:    keyVictim.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession(victimByName) failed: %v", err)
	}
	_ = st.SaveExperiment(ctx, &models.Experiment{
		ID:        "exp_failed_victim",
		Title:     "Failed Shard Name Collision Test",
		APIKeyID:  keyB.ID,
		Status:    models.ExperimentFailed,
		NumShards: 1,
		Shards: []models.ExperimentShard{
			{
				ShardID:     0,
				SessionID:   "",
				SessionName: "exp_failed_victim_s0",
				Status:      "FAILED",
			},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if _, tdVictimErr := eng.CollectAndTeardownExperiment(ctx, "exp_failed_victim", true); tdVictimErr != nil {
		t.Fatalf("CollectAndTeardownExperiment(exp_failed_victim) failed: %v", tdVictimErr)
	}
	victimReloaded, _ := eng.GetSession(ctx, victimByName.ID)
	if victimReloaded == nil || victimReloaded.Status == models.SessionStopped {
		t.Fatalf("SECURITY FAILURE: CollectAndTeardownExperiment tore down another tenant's session matching SessionName: %+v", victimReloaded)
	}
	_ = eng.StopSession(ctx, victimByName.ID)

	// Real non-demo sharded experiment auto-transitions RUNNING -> COMPLETED_AND_UNASSIGNED when worker PID exits!
	expAuto, err := eng.LaunchShardedExperiment(ctx, models.ExperimentLaunchRequest{
		ExperimentID:       "exp_real_autocomp",
		Accelerator:        models.AcceleratorG4,
		NumShards:          1,
		AutoStopOnComplete: true,
		ScriptCode:         "print('real shard complete')\n",
	})
	if err != nil {
		t.Fatalf("LaunchShardedExperiment(exp_real_autocomp) failed: %v", err)
	}
	if expAuto.Status != models.ExperimentRunning {
		t.Fatalf("expected non-demo experiment to start in RUNNING state, got %s", expAuto.Status)
	}
	// SweepKeepaliveAndZombiesOnce sees empty `/proc/{pid}/stat` + exit_code=0 -> marks COMPLETED_AND_UNASSIGNED & stops VM!
	eng.SweepKeepaliveAndZombiesOnce(ctx, false)
	expAutoDone, err := eng.GetExperiment(ctx, "exp_real_autocomp")
	if err != nil {
		t.Fatal(err)
	}
	if expAutoDone.Status != models.ExperimentCompleted || expAutoDone.CompletedShards != 1 || expAutoDone.Shards[0].Status != "COMPLETED_AND_UNASSIGNED" || expAutoDone.SyncedRecordsTotal != 10 {
		t.Fatalf("expected real non-demo experiment to auto-transition to COMPLETED / COMPLETED_AND_UNASSIGNED with 10 records, got %+v", expAutoDone)
	}
}
