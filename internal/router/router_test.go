package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

// ============================================================================
// In-Memory Test Mocks (Zero external package dependencies)
// ============================================================================

type memHub struct {
	mu     sync.Mutex
	events []models.WSEvent
}

func (h *memHub) Publish(ev models.WSEvent) {
	h.mu.Lock()
	h.events = append(h.events, ev)
	h.mu.Unlock()
}

func (h *memHub) Subscribe(_ context.Context) (<-chan models.WSEvent, func()) {
	ch := make(chan models.WSEvent, 8)
	return ch, func() { close(ch) }
}

func (h *memHub) ClientCount() int { return 0 }

type memStore struct {
	mu       sync.RWMutex
	accounts map[string]*models.Account
	sessions map[string]*models.RuntimeSession
	keys     map[string]*models.AgentAPIKey
	policy   *models.RouterPolicy
	traces   []models.ToolTraceEvent
}

func newMemStore() *memStore {
	pol := models.DefaultRouterPolicy()
	return &memStore{
		accounts: make(map[string]*models.Account),
		sessions: make(map[string]*models.RuntimeSession),
		keys:     make(map[string]*models.AgentAPIKey),
		policy:   &pol,
	}
}

func (m *memStore) SaveAccount(_ context.Context, acc *models.Account) error {
	m.mu.Lock()
	cp := *acc
	m.accounts[acc.ID] = &cp
	m.mu.Unlock()
	return nil
}
func (m *memStore) GetAccount(_ context.Context, id string) (*models.Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if a, ok := m.accounts[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}
func (m *memStore) GetAccountByEmail(_ context.Context, email string) (*models.Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.accounts {
		if strings.EqualFold(a.Email, email) {
			cp := *a
			return &cp, nil
		}
	}
	return nil, nil
}
func (m *memStore) GetAccountByReceiptHash(_ context.Context, _ string) (*models.Account, error) {
	return nil, nil
}
func (m *memStore) ListAccounts(_ context.Context) ([]*models.Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*models.Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}
func (m *memStore) DeleteAccount(_ context.Context, id string) error {
	m.mu.Lock()
	delete(m.accounts, id)
	m.mu.Unlock()
	return nil
}

func (m *memStore) SaveProxy(_ context.Context, _ *models.ProxyConfig) error      { return nil }
func (m *memStore) GetProxy(_ context.Context, _ string) (*models.ProxyConfig, error) { return nil, nil }
func (m *memStore) ListProxies(_ context.Context) ([]*models.ProxyConfig, error)  { return nil, nil }
func (m *memStore) DeleteProxy(_ context.Context, _ string) error                 { return nil }

func (m *memStore) SaveInvite(_ context.Context, _ *models.ContributorInvite) error { return nil }
func (m *memStore) GetInvite(_ context.Context, _ string) (*models.ContributorInvite, error) {
	return nil, nil
}
func (m *memStore) ListInvites(_ context.Context) ([]*models.ContributorInvite, error) {
	return nil, nil
}
func (m *memStore) DeleteInvite(_ context.Context, _ string) error { return nil }

func (m *memStore) SaveSession(_ context.Context, sess *models.RuntimeSession) error {
	m.mu.Lock()
	cp := *sess
	m.sessions[sess.ID] = &cp
	m.mu.Unlock()
	return nil
}
func (m *memStore) GetSession(_ context.Context, id string) (*models.RuntimeSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, nil
}
func (m *memStore) GetSessionByName(_ context.Context, name string) (*models.RuntimeSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s.Name == name {
			cp := *s
			return &cp, nil
		}
	}
	return nil, nil
}
func (m *memStore) ListSessions(_ context.Context) ([]*models.RuntimeSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*models.RuntimeSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}
func (m *memStore) DeleteSession(_ context.Context, id string) error {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

func (m *memStore) SaveExperiment(_ context.Context, _ *models.Experiment) error { return nil }
func (m *memStore) GetExperiment(_ context.Context, _ string) (*models.Experiment, error) {
	return nil, nil
}
func (m *memStore) ListExperiments(_ context.Context) ([]*models.Experiment, error) { return nil, nil }
func (m *memStore) DeleteExperiment(_ context.Context, _ string) error              { return nil }

func (m *memStore) SaveAPIKey(_ context.Context, key *models.AgentAPIKey) error {
	m.mu.Lock()
	cp := *key
	m.keys[key.ID] = &cp
	m.mu.Unlock()
	return nil
}
func (m *memStore) GetAPIKey(_ context.Context, id string) (*models.AgentAPIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if k, ok := m.keys[id]; ok {
		cp := *k
		return &cp, nil
	}
	return nil, nil
}
func (m *memStore) GetAPIKeyByHash(_ context.Context, keyHash string) (*models.AgentAPIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, k := range m.keys {
		if k.KeyHash == keyHash {
			cp := *k
			return &cp, nil
		}
	}
	return nil, nil
}
func (m *memStore) ListAPIKeys(_ context.Context) ([]*models.AgentAPIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*models.AgentAPIKey, 0, len(m.keys))
	for _, k := range m.keys {
		cp := *k
		out = append(out, &cp)
	}
	return out, nil
}
func (m *memStore) DeleteAPIKey(_ context.Context, id string) error {
	m.mu.Lock()
	delete(m.keys, id)
	m.mu.Unlock()
	return nil
}

func (m *memStore) GetRouterPolicy(_ context.Context) (*models.RouterPolicy, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := *m.policy
	return &cp, nil
}
func (m *memStore) SaveRouterPolicy(_ context.Context, policy *models.RouterPolicy) error {
	m.mu.Lock()
	cp := *policy
	m.policy = &cp
	m.mu.Unlock()
	return nil
}
func (m *memStore) AppendToolTrace(_ context.Context, trace *models.ToolTraceEvent) error {
	m.mu.Lock()
	m.traces = append([]models.ToolTraceEvent{*trace}, m.traces...)
	m.mu.Unlock()
	return nil
}
func (m *memStore) ListToolTraces(_ context.Context, limit int) ([]models.ToolTraceEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit > len(m.traces) {
		limit = len(m.traces)
	}
	out := make([]models.ToolTraceEvent, limit)
	copy(out, m.traces[:limit])
	return out, nil
}
func (m *memStore) RecordBurnSample(_ context.Context, _ models.AcceleratorType, _ models.MachineShape, _ float64) error {
	return nil
}
func (m *memStore) GetCalibratedBurnRate(_ context.Context, _ models.AcceleratorType, _ models.MachineShape) (float64, bool, error) {
	return 0, false, nil
}
func (m *memStore) AddSavedWaste(_ context.Context, _ float64, _ bool) error { return nil }
func (m *memStore) GetSavedWasteStats(_ context.Context) (models.SavedWasteStats, error) {
	return models.SavedWasteStats{}, nil
}
func (m *memStore) Close() error { return nil }

// ============================================================================
// Unit Tests
// ============================================================================

func TestSchedulingStrategiesAndFallback(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	hub := &memHub{}
	svc := NewServiceWithDirs(st, nil, hub, t.TempDir(), "", "", "linux")

	soon := time.Now().UTC().Add(48 * time.Hour)
	accUltra := &models.Account{
		ID:               "acc_ultra",
		Email:            "ultra@lab.dev",
		SubscriptionTier: models.TierAIUltra,
		Health:           models.HealthHealthy,
		Enabled:          true,
		MaxConcurrentVMs: 4,
		PaidCUBalance:    480.0,
		SchedulableCU:    480.0,
		GPUEntitlements: map[models.AcceleratorType]models.GPUEntitlement{
			models.AcceleratorH100: {Accelerator: models.AcceleratorH100, Status: models.EntitlementTierRestricted},
			models.AcceleratorG4:   {Accelerator: models.AcceleratorG4, Status: models.EntitlementAvailable},
		},
	}
	accExpiring := &models.Account{
		ID:                     "acc_exp",
		Email:                  "expiring@lab.dev",
		SubscriptionTier:       models.TierPro,
		Health:                 models.HealthHealthy,
		Enabled:                true,
		MaxConcurrentVMs:       2,
		PaidCUBalance:          95.0,
		SchedulableCU:          95.0,
		ExpiringSoonCU:         45.0,
		EarliestBatchExpiresAt: &soon,
		GPUEntitlements: map[models.AcceleratorType]models.GPUEntitlement{
			models.AcceleratorH100: {Accelerator: models.AcceleratorH100, Status: models.EntitlementIneligible},
			models.AcceleratorG4:   {Accelerator: models.AcceleratorG4, Status: models.EntitlementAvailable},
		},
	}
	accContribFloor := &models.Account{
		ID:               "acc_contrib",
		Email:            "donor@uni.edu",
		SubscriptionTier: models.TierProPlus,
		Health:           models.HealthHealthy,
		Enabled:          true,
		IsContributor:    true,
		MaxConcurrentVMs: 2,
		PaidCUBalance:    51.0, // Only 1.0 CU above 50.0 floor -> must be protected!
		Guardrails: models.ContributorGuardrails{
			MinReserveCU:    50.0,
			AllowPaidCUBurn: true,
			AllowedGPUs:     []models.AcceleratorType{models.AcceleratorG4, models.AcceleratorT4},
		},
	}
	accFree := &models.Account{
		ID:               "acc_free",
		Email:            "free@gmail.com",
		SubscriptionTier: models.TierFree,
		Health:           models.HealthHealthy,
		Enabled:          true,
		MaxConcurrentVMs: 1,
		FreeQuota: &models.FreeQuotaInfo{
			RemainingMilliCCU: 14500,
			RemainingFreeCU:   14.5,
		},
	}

	_ = st.SaveAccount(ctx, accUltra)
	_ = st.SaveAccount(ctx, accExpiring)
	_ = st.SaveAccount(ctx, accContribFloor)
	_ = st.SaveAccount(ctx, accFree)

	// 1. Auto-GPU-Tier-Fallback: H100 restricted across pool -> automatically steps down to G4,
	//    and Waste-Zero selects acc_exp because it has 45 CU expiring in 48h!
	acc, tier, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator:  models.AcceleratorH100,
		Strategy:     models.StrategyWasteZero,
		AutoFallback: true,
	})
	if err != nil {
		t.Fatalf("SelectAccountAndTier failed: %v", err)
	}
	if tier != models.AcceleratorG4 {
		t.Fatalf("expected fallback tier G4, got %s", tier)
	}
	if acc.ID != "acc_exp" {
		t.Fatalf("expected waste-zero to pick expiring account acc_exp, got %s", acc.ID)
	}

	// 2. Max-Balance picks acc_ultra (480 CU > 95 CU) while skipping acc_contrib (below runway floor).
	accMax, tierMax, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator: models.AcceleratorG4,
		Strategy:    models.StrategyMaxBalance,
	})
	if err != nil {
		t.Fatalf("MaxBalance failed: %v", err)
	}
	if accMax.ID != "acc_ultra" || tierMax != models.AcceleratorG4 {
		t.Fatalf("expected acc_ultra on G4, got %s on %s", accMax.ID, tierMax)
	}

	// 3. Contributor Reserve Floor rejects explicit selection of acc_contrib when balance (51) - floor (50) = 1.0 < 2.415 CU runway.
	_, _, err = svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator:     models.AcceleratorG4,
		AccountSelector: "donor@uni.edu",
	})
	if err == nil {
		t.Fatalf("expected contributor reserve floor to reject scheduling when within 1 CU of MinReserveCU")
	}

	// 4. Free-Opportunistic picks acc_free on T4 before touching Paid CU accounts!
	accF, tierF, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator: models.AcceleratorT4,
		Strategy:    models.StrategyFreeOpportunistic,
	})
	if err != nil {
		t.Fatalf("FreeOpportunistic failed: %v", err)
	}
	if accF.ID != "acc_free" || tierF != models.AcceleratorT4 {
		t.Fatalf("expected acc_free on T4, got %s on %s", accF.ID, tierF)
	}

	// 5. Round-Robin alternates cleanly across acc_exp and acc_ultra.
	r1, _, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator: models.AcceleratorG4,
		Strategy:    models.StrategyRoundRobin,
	})
	if err != nil {
		t.Fatal(err)
	}
	r2, _, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator: models.AcceleratorG4,
		Strategy:    models.StrategyRoundRobin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r1.ID == r2.ID {
		t.Fatalf("expected round-robin to alternate between 2 healthy accounts, got %s twice", r1.ID)
	}
}

func TestStickySessionAndStickyEscape(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	svc := NewServiceWithDirs(st, nil, &memHub{}, t.TempDir(), "", "", "linux")

	_ = st.SaveAccount(ctx, &models.Account{
		ID:            "acc_1",
		Email:         "a1@lab.dev",
		Health:        models.HealthHealthy,
		Enabled:       true,
		PaidCUBalance: 100,
	})
	_ = st.SaveAccount(ctx, &models.Account{
		ID:            "acc_2",
		Email:         "a2@lab.dev",
		Health:        models.HealthHealthy,
		Enabled:       true,
		PaidCUBalance: 300,
	})
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:          "sess_warm",
		Name:        "warm-g4",
		AccountID:   "acc_1",
		Accelerator: models.AcceleratorG4,
		Status:      models.SessionIdle,
		KernelID:    "kern-123",
	})

	if err := svc.BindStickySession(ctx, models.RouteBinding{
		StickyKey:    "mcp-sess-01",
		ClientName:   "Antigravity",
		SessionID:    "sess_warm",
		SessionName:  "warm-g4",
		AccountID:    "acc_1",
		AccountEmail: "a1@lab.dev",
		Accelerator:  models.AcceleratorG4,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, ok := svc.ResolveStickySession(ctx, "mcp-sess-01")
	if !ok || resolved.ID != "sess_warm" {
		t.Fatalf("expected sticky hit on sess_warm, got ok=%v", ok)
	}

	// Sticky account affinity routes new default/sticky G4 request to acc_1 even though acc_2 has higher balance.
	chosen, _, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator: models.AcceleratorG4,
		StickyKey:   "mcp-sess-01",
	})
	if err != nil || chosen.ID != "acc_1" {
		t.Fatalf("expected sticky account acc_1 on default strategy, got %v (err=%v)", chosen, err)
	}

	// Explicit non-sticky strategy (e.g. max-balance) must NOT be overridden by StickySessionEnabled!
	chosenExplicit, _, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator: models.AcceleratorG4,
		Strategy:    models.StrategyMaxBalance,
		StickyKey:   "mcp-sess-01",
	})
	if err != nil || chosenExplicit.ID != "acc_2" {
		t.Fatalf("expected explicit max-balance strategy to pick acc_2 (300 CU), got %v (err=%v)", chosenExplicit, err)
	}

	// Simulate VM death (ZOMBIE_ALERT) -> Sticky Escape must unbind and fail over to acc_2!
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:        "sess_warm",
		Name:      "warm-g4",
		AccountID: "acc_1",
		Status:    models.SessionZombieAlert,
	})
	_ = st.SaveAccount(ctx, &models.Account{
		ID:      "acc_1",
		Email:   "a1@lab.dev",
		Health:  models.HealthRevokedByUser,
		Enabled: true,
	})

	if _, ok := svc.ResolveStickySession(ctx, "mcp-sess-01"); ok {
		t.Fatalf("expected Sticky Escape to invalidate dead/revoked session")
	}

	escaped, _, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator: models.AcceleratorG4,
		Strategy:    models.StrategyStickySession,
		StickyKey:   "mcp-sess-01",
	})
	if err != nil || escaped.ID != "acc_2" {
		t.Fatalf("expected Sticky Escape failover to acc_2, got %v (err=%v)", escaped, err)
	}
}

func TestScopedAPIKeysAndTraces(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	hub := &memHub{}
	svc := NewServiceWithDirs(st, nil, hub, t.TempDir(), "", "", "linux")

	created, err := svc.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:             "Claude Code Bot",
		AllowedGPUs:      []models.AcceleratorType{models.AcceleratorG4, models.AcceleratorL4},
		MaxBudgetCUTotal: 10.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.RawKey, "ch_sk_live_") {
		t.Fatalf("unexpected raw key format: %s", created.RawKey)
	}

	// Verify listed keys never leak RawKey.
	listed, err := svc.ListAPIKeys(ctx)
	if err != nil || len(listed) != 1 || listed[0].RawKey != "" {
		t.Fatalf("expected 1 masked key, got %+v", listed)
	}

	// Authenticate with Bearer prefix.
	authed, err := svc.AuthenticateAPIKey(ctx, "Bearer "+created.RawKey)
	if err != nil || authed.ID != created.ID {
		t.Fatalf("AuthenticateAPIKey failed: %v", err)
	}

	// Record trace consuming 10.5 CU -> triggers budget exhaustion on subsequent auth!
	if err := svc.RecordTrace(ctx, models.ToolTraceEvent{
		Caller:         "claude-code",
		APIKeyPrefix:   created.KeyPrefix,
		Transport:      models.TransportMCPHTTP,
		ToolOrEndpoint: "execute_code",
		CUDelta:        10.5,
		Status:         "ok",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.AuthenticateAPIKey(ctx, created.RawKey); err == nil {
		t.Fatalf("expected ErrAPIKeyBudgetExceeded after exceeding 10.0 CU cap")
	}
}

func TestSurgicalAgentConfigInjectorAllFormats(t *testing.T) {
	ctx := context.Background()
	tmpHome := t.TempDir()
	tmpAppData := filepath.Join(tmpHome, "AppData", "Roaming")
	svc := NewServiceWithDirs(newMemStore(), nil, &memHub{}, tmpHome, tmpAppData, "", "windows")

	// Pre-populate existing user configs with unrelated MCP servers & comments to verify zero clobbering!
	agyPath := filepath.Join(tmpHome, ".gemini", "config", "mcp_config.json")
	_ = os.MkdirAll(filepath.Dir(agyPath), 0o755)
	origAgy := "{\n  \"mcpServers\": {\n    \"github\": {\n      \"url\": \"https://mcp.github.com\"\n    }\n  }\n}\n"
	_ = os.WriteFile(agyPath, []byte(origAgy), 0o600)

	codexPath := filepath.Join(tmpHome, ".codex", "config.toml")
	_ = os.MkdirAll(filepath.Dir(codexPath), 0o755)
	origCodex := "# User Codex settings\nmodel = \"gpt-5-codex\"\n\n[mcp_servers.filesystem]\ncommand = \"npx\"\nargs = [\"-y\", \"@modelcontextprotocol/server-filesystem\"]\n"
	_ = os.WriteFile(codexPath, []byte(origCodex), 0o600)

	openCodePath := filepath.Join(tmpHome, ".config", "opencode", "opencode.jsonc")
	_ = os.MkdirAll(filepath.Dir(openCodePath), 0o755)
	origOpenCode := "// Personal OpenCode config — keep this comment!\n{\n  \"theme\": \"obsidian\",\n  \"mcp\": {\n    \"existing\": { \"type\": \"remote\", \"url\": \"http://localhost:9000/mcp\", \"enabled\": true, },\n  },\n}\n"
	_ = os.WriteFile(openCodePath, []byte(origOpenCode), 0o600)

	// Inject across Antigravity, Codex, OpenCode, Claude Code, Claude Desktop, Gemini CLI, Cursor, Windsurf.
	for _, cid := range models.AllSupportedAgentClients {
		st, err := svc.InjectAgentConfig(ctx, models.AgentInjectRequest{
			ClientID:      cid,
			ServerBaseURL: "http://127.0.0.1:7860",
			APIKey:        "ch_sk_live_test123",
		})
		if err != nil {
			t.Fatalf("InjectAgentConfig(%s) failed: %v", cid, err)
		}
		if !st.Injected {
			t.Fatalf("expected %s to report Injected=true", cid)
		}
	}

	// Verify first-write backups were created for pre-existing files and untouched!
	agyBackup := filepath.Join(tmpHome, ".colabhive", "backups", "first-write", "antigravity.json.bak")
	bakBytes, err := os.ReadFile(agyBackup)
	if err != nil || string(bakBytes) != origAgy {
		t.Fatalf("first-write backup mismatch: err=%v got=%q", err, string(bakBytes))
	}

	// Re-inject with a new key -> first-write backup MUST still contain original pre-ColabHive file!
	_, err = svc.InjectAgentConfig(ctx, models.AgentInjectRequest{
		ClientID:      models.AgentAntigravity,
		ServerBaseURL: "http://127.0.0.1:7860",
		APIKey:        "ch_sk_live_updated999",
	})
	if err != nil {
		t.Fatal(err)
	}
	bakBytesAfter, _ := os.ReadFile(agyBackup)
	if string(bakBytesAfter) != origAgy {
		t.Fatalf("first-write backup was overwritten on second write!")
	}

	// Verify Codex TOML kept `model = "gpt-5-codex"` and `[mcp_servers.filesystem]`.
	codexRaw, _ := os.ReadFile(codexPath)
	if !strings.Contains(string(codexRaw), `model = "gpt-5-codex"`) || !strings.Contains(string(codexRaw), "[mcp_servers.filesystem]") {
		t.Fatalf("Codex injection clobbered user TOML: %s", string(codexRaw))
	}

	// Verify OpenCode JSONC preserved leading `// Personal OpenCode config` comment and `existing` MCP!
	ocRaw, _ := os.ReadFile(openCodePath)
	if !strings.Contains(string(ocRaw), "// Personal OpenCode config") || !strings.Contains(string(ocRaw), `"existing"`) {
		t.Fatalf("OpenCode JSONC injection lost comment or sibling server: %s", string(ocRaw))
	}

	// Surgically remove all 8 -> unrelated MCP servers and comments remain intact!
	for _, cid := range models.AllSupportedAgentClients {
		st, err := svc.RemoveAgentConfig(ctx, cid)
		if err != nil {
			t.Fatalf("RemoveAgentConfig(%s) failed: %v", cid, err)
		}
		if st.Injected {
			t.Fatalf("expected %s Injected=false after removal", cid)
		}
	}

	agyAfterRemove, _ := os.ReadFile(agyPath)
	if !strings.Contains(string(agyAfterRemove), "https://mcp.github.com") || strings.Contains(string(agyAfterRemove), "colabhive") {
		t.Fatalf("Antigravity surgical removal failed: %s", string(agyAfterRemove))
	}
	codexAfterRemove, _ := os.ReadFile(codexPath)
	if !strings.Contains(string(codexAfterRemove), "[mcp_servers.filesystem]") || strings.Contains(string(codexAfterRemove), "colabhive") {
		t.Fatalf("Codex surgical removal failed: %s", string(codexAfterRemove))
	}
}

func TestConcurrentAcquireAccountSlotShards4ZeroStampede(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	svc := NewServiceWithDirs(st, nil, &memHub{}, t.TempDir(), "", "", "linux")

	// Seed 2 AI Ultra + 2 Pro+ accounts (all idle) — previously all 4 parallel shards stampeded acc_1 (2500 CU)!
	balances := []float64{2500, 1800, 500, 420}
	tiers := []models.SubscriptionTier{models.TierAIUltra, models.TierAIUltra, models.TierProPlus, models.TierProPlus}
	for i := 0; i < 4; i++ {
		id := "acc_shard_" + string(rune('a'+i))
		_ = st.SaveAccount(ctx, &models.Account{
			ID:               id,
			Email:            id + "@lab.dev",
			SubscriptionTier: tiers[i],
			Health:           models.HealthHealthy,
			Enabled:          true,
			MaxConcurrentVMs: 5,
			PaidCUBalance:    balances[i],
			SchedulableCU:    balances[i],
		})
	}

	key, err := svc.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:             "4-Shard Runner",
		MaxConcurrentVMs: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, strat := range []models.RoutingStrategy{models.StrategyWasteZero, models.StrategyRoundRobin} {
		startGate := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		picked := make(map[string]int)
		releases := make([]func(), 0, 4)

		for sIdx := 0; sIdx < 4; sIdx++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startGate
				acc, tier, release, aErr := svc.AcquireAccountSlot(ctx, models.SessionAllocRequest{
					Accelerator: models.AcceleratorG4,
					Strategy:    strat,
					APIKeyID:    key.ID,
				})
				if aErr != nil {
					t.Errorf("AcquireAccountSlot(%s) failed: %v", strat, aErr)
					return
				}
				if tier != models.AcceleratorG4 {
					t.Errorf("expected G4, got %s", tier)
				}
				mu.Lock()
				picked[acc.ID]++
				releases = append(releases, release)
				mu.Unlock()
			}()
		}
		close(startGate)
		wg.Wait()

		if len(picked) != 4 {
			t.Fatalf("strategy %s stampeded accounts: expected 4 distinct accounts across 4 concurrent shards, got %v", strat, picked)
		}

		// 5th in-flight slot must be rejected by APIKey.MaxConcurrentVMs=4 before any SaveSession!
		if _, _, _, err5 := svc.AcquireAccountSlot(ctx, models.SessionAllocRequest{
			Accelerator: models.AcceleratorG4,
			Strategy:    strat,
			APIKeyID:    key.ID,
		}); err5 == nil {
			t.Fatalf("expected 5th concurrent slot to be rejected by APIKey.MaxConcurrentVMs=4")
		}

		for _, rel := range releases {
			rel()
		}
	}
}

func TestFreeTierWithoutFreeQuotaInfoRoundRobinRingAndACLHardening(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	now := time.Date(2026, 11, 23, 18, 30, 0, 0, time.UTC) // Tuesday 02:30 Asia/Shanghai (overnight from Monday!)
	svc := NewServiceWithDirs(st, nil, &memHub{}, t.TempDir(), "", "", "windows")
	svc.NowFunc = func() time.Time { return now }

	// 1. Real Google FREE account (FreeQuota == nil, PaidCUBalance == 0) vs AI Ultra (2500 CU) on T4
	_ = st.SaveAccount(ctx, &models.Account{
		ID:               "acc_ultra_paid",
		Email:            "ultra@lab.dev",
		SubscriptionTier: models.TierAIUltra,
		Health:           models.HealthHealthy,
		Enabled:          true,
		Groups:           []string{"owner"},
		MaxConcurrentVMs: 5,
		PaidCUBalance:    2500,
		SchedulableCU:    2500,
	})
	_ = st.SaveAccount(ctx, &models.Account{
		ID:               "acc_real_free",
		Email:            "free-nofq@gmail.com",
		SubscriptionTier: models.TierFree,
		Health:           models.HealthHealthy,
		Enabled:          true,
		Groups:           []string{"sandbox"},
		MaxConcurrentVMs: 1,
		PaidCUBalance:    0,
		FreeQuota:        nil, // Real /tun/m/ccu-info omits freeCcuQuotaInfo!
	})

	chosenT4, _, err := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator: models.AcceleratorT4,
		Strategy:    models.StrategyWasteZero,
	})
	if err != nil || chosenT4.ID != "acc_real_free" {
		t.Fatalf("expected waste-zero to pick real FREE account (0 paid burn) before 2500 CU Ultra on T4, got %+v (%v)", chosenT4, err)
	}

	// 2. Round-Robin ring cursor across 3 accounts when 1 account has an external browser tab open (ActiveAssignments=1):
	//    must NOT pin 100% of sequential calls onto a single 0-load account or starve acc_2!
	stRR := newMemStore()
	svcRR := NewServiceWithDirs(stRR, nil, &memHub{}, t.TempDir(), "", "", "linux")
	for i, load := range []int{0, 1, 1} {
		id := "rr_" + string(rune('1'+i))
		_ = stRR.SaveAccount(ctx, &models.Account{
			ID:                id,
			Email:             id + "@lab.dev",
			SubscriptionTier:  models.TierAIUltra,
			Health:            models.HealthHealthy,
			Enabled:           true,
			MaxConcurrentVMs:  5,
			ActiveAssignments: load,
			PaidCUBalance:     500,
			SchedulableCU:     500,
		})
	}
	seenRR := make(map[string]int)
	for c := 0; c < 6; c++ {
		picked, _, rErr := svcRR.SelectAccountAndTier(ctx, models.SessionAllocRequest{
			Accelerator: models.AcceleratorG4,
			Strategy:    models.StrategyRoundRobin,
		})
		if rErr != nil {
			t.Fatal(rErr)
		}
		seenRR[picked.ID]++
	}
	for _, id := range []string{"rr_1", "rr_2", "rr_3"} {
		if seenRR[id] != 2 {
			t.Fatalf("expected round-robin to visit each of 3 non-saturated accounts twice in 6 turns, got %v", seenRR)
		}
	}

	// 3. Scoped APIKey AllowedGroups empty-intersection privilege escalation MUST be blocked!
	sandboxKey, err := svc.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:             "Sandbox Key",
		AllowedGroups:    []string{"sandbox"},
		MaxBudgetCUDaily: 5.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, escErr := svc.SelectAccountAndTier(ctx, models.SessionAllocRequest{
		Accelerator:   models.AcceleratorT4,
		APIKeyID:      sandboxKey.ID,
		AllowedGroups: []string{"owner"}, // Disjoint from ["sandbox"]!
	}); escErr == nil {
		t.Fatal("security violation: disjoint AllowedGroups bypassed key ACL instead of returning ErrAPIKeyBudgetExceeded")
	}

	// 4. Sticky session external-VM guard, APIKey isolation, & StickyAgentCount rollup
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:              "sess_ext_nb",
		Name:            "colab-g4-ext",
		AccountID:       "acc_ultra_paid",
		Accelerator:     models.AcceleratorG4,
		Status:          models.SessionIdle,
		AdoptedExternal: true,
	})
	_ = svc.BindStickySession(ctx, models.RouteBinding{
		StickyKey: "accident-ext",
		SessionID: "sess_ext_nb",
		AccountID: "acc_ultra_paid",
	})
	if _, ok := svc.ResolveStickySession(ctx, "accident-ext"); ok {
		t.Fatal("expected ResolveStickySession to reject unintended AdoptedExternal session")
	}

	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:          "sess_key_a",
		Name:        "key-a-vm",
		AccountID:   "acc_ultra_paid",
		Accelerator: models.AcceleratorG4,
		Status:      models.SessionIdle,
		APIKeyID:    "key_other",
		StickyKey:   "shared-sticky",
	})
	_ = svc.BindStickySession(ctx, models.RouteBinding{
		StickyKey: "shared-sticky",
		APIKeyID:  "key_other",
		SessionID: "sess_key_a",
		AccountID: "acc_ultra_paid",
	})
	accWithSticky, _ := st.GetAccount(ctx, "acc_ultra_paid")
	if accWithSticky.StickyAgentCount != 1 {
		t.Fatalf("expected Account.StickyAgentCount=1, got %d", accWithSticky.StickyAgentCount)
	}
	if _, ok := svc.ResolveStickySessionForKey(ctx, "shared-sticky", sandboxKey.ID); ok {
		t.Fatal("security violation: sandboxKey hijacked key_other's sticky session")
	}

	// 5. Daily APIKey CU budget resets at UTC midnight
	_ = svc.RecordTrace(ctx, models.ToolTraceEvent{
		Caller:         "bot",
		APIKeyPrefix:   sandboxKey.KeyPrefix,
		Transport:      models.TransportMCPHTTP,
		ToolOrEndpoint: "execute_code",
		CUDelta:        5.5,
	})
	if _, authErr := svc.AuthenticateAPIKey(ctx, sandboxKey.RawKey); authErr == nil {
		t.Fatal("expected daily CU budget exceeded on Day 1")
	}
	now = now.Add(24 * time.Hour)
	if _, authErr := svc.AuthenticateAPIKey(ctx, sandboxKey.RawKey); authErr != nil {
		t.Fatalf("expected daily CU budget reset after UTC midnight, got %v", authErr)
	}

	// 6. Overnight ScheduleWindow (Monday 22:00 -> 09:00 Asia/Shanghai) allows Tuesday 02:30 Asia/Shanghai!
	winMondayOvernight := &models.ScheduleWindow{
		Enabled:    true,
		Timezone:   "Asia/Shanghai",
		DaysOfWeek: []int{int(time.Monday)},
		StartHour:  "22:00",
		EndHour:    "09:00",
	}
	tueEarlyUTC := time.Date(2026, 11, 23, 18, 30, 0, 0, time.UTC) // 2026-11-24 (Tue) 02:30 CST -> started Mon 22:00!
	if !withinScheduleWindow(winMondayOvernight, tueEarlyUTC) {
		t.Fatal("expected Tuesday 02:30 Asia/Shanghai to match Monday 22:00->09:00 overnight window")
	}
	monEarlyUTC := time.Date(2026, 11, 22, 18, 30, 0, 0, time.UTC) // 2026-11-23 (Mon) 02:30 CST -> started Sun 22:00!
	if withinScheduleWindow(winMondayOvernight, monEarlyUTC) {
		t.Fatal("expected Monday 02:30 Asia/Shanghai (started Sunday night) to be rejected by Monday-only overnight window")
	}
}

func TestRecordAPIKeyCUBurnAndRevokeConcurrency(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	svc := NewServiceWithDirs(st, nil, &memHub{}, t.TempDir(), "", "", "linux")

	created, err := svc.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
		Name:             "Budgeted Key",
		MaxBudgetCUDaily: 2.0,
		MaxBudgetCUTotal: 5.0,
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.RecordAPIKeyCUBurn(ctx, created.ID, 0.15)
			_, _ = svc.AuthenticateAPIKey(ctx, created.RawKey)
		}()
	}
	wg.Wait()

	stored, _ := st.GetAPIKey(ctx, created.ID)
	if stored == nil || stored.UsedCUToday < 1.49 || stored.UsedCUToday > 1.51 {
		t.Fatalf("expected atomic UsedCUToday=1.50 across 10 concurrent burns, got %+v", stored)
	}

	exceeded, reason := svc.RecordAPIKeyCUBurn(ctx, created.ID, 0.60)
	if !exceeded || !strings.Contains(strings.ToLower(reason), "daily cu budget exhausted") {
		t.Fatalf("expected daily budget exceeded=true at 2.10/2.00 CU, got exceeded=%v reason=%q", exceeded, reason)
	}

	// Revoke key -> AuthenticateAPIKey fails and key is never resurrected!
	if err := svc.RevokeAPIKey(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateAPIKey(ctx, created.RawKey); err == nil {
		t.Fatal("expected revoked API key authentication to fail")
	}
	if afterRevoke, _ := st.GetAPIKey(ctx, created.ID); afterRevoke != nil {
		t.Fatalf("revoked API key was resurrected: %+v", afterRevoke)
	}
}

func TestNamespacedStickyBindingsTenantIsolation(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()

	_ = st.SaveAccount(ctx, &models.Account{
		ID:               "acc_shared",
		Email:            "shared@lab.dev",
		SubscriptionTier: models.TierAIUltra,
		Health:           models.HealthHealthy,
		Enabled:          true,
		MaxConcurrentVMs: 6,
		PaidCUBalance:    500,
		SchedulableCU:    500,
	})

	svc := NewServiceWithDirs(st, nil, &memHub{}, t.TempDir(), "", "", "linux")
	kA, _ := svc.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Tenant A"})
	kB, _ := svc.CreateAPIKey(ctx, models.CreateAPIKeyRequest{Name: "Tenant B"})

	for _, s := range []*models.RuntimeSession{
		{ID: "sess_admin", Name: "admin-vm", AccountID: "acc_shared", Accelerator: models.AcceleratorG4, Status: models.SessionIdle, StickyKey: "shared-sticky"},
		{ID: "sess_a", Name: "tenant-a-vm", AccountID: "acc_shared", Accelerator: models.AcceleratorG4, Status: models.SessionIdle, APIKeyID: kA.ID, StickyKey: "shared-sticky"},
		{ID: "sess_b", Name: "tenant-b-vm", AccountID: "acc_shared", Accelerator: models.AcceleratorG4, Status: models.SessionIdle, APIKeyID: kB.ID, StickyKey: "shared-sticky"},
	} {
		if err := st.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	_ = svc.BindStickySession(ctx, models.RouteBinding{StickyKey: "shared-sticky", SessionID: "sess_admin", AccountID: "acc_shared"})
	_ = svc.BindStickySession(ctx, models.RouteBinding{StickyKey: "shared-sticky", APIKeyID: kA.ID, SessionID: "sess_a", AccountID: "acc_shared"})
	_ = svc.BindStickySession(ctx, models.RouteBinding{StickyKey: "shared-sticky", APIKeyID: kB.ID, SessionID: "sess_b", AccountID: "acc_shared"})

	resAdmin, okAdmin := svc.ResolveStickySession(ctx, "shared-sticky")
	resA, okA := svc.ResolveStickySessionForKey(ctx, "shared-sticky", kA.ID)
	resB, okB := svc.ResolveStickySessionForKey(ctx, "shared-sticky", kB.ID)
	if !okAdmin || resAdmin.ID != "sess_admin" || !okA || resA.ID != "sess_a" || !okB || resB.ID != "sess_b" {
		t.Fatalf("expected isolated sticky bindings across admin/kA/kB, got admin=%+v a=%+v b=%+v", resAdmin, resA, resB)
	}

	// Unbinding Tenant A MUST NOT disturb Tenant B or Admin!
	_ = svc.UnbindStickySessionForKey(ctx, "shared-sticky", kA.ID)
	if _, ok := svc.ResolveStickySessionForKey(ctx, "shared-sticky", kA.ID); ok {
		t.Fatal("expected Tenant A sticky binding to be unbound")
	}
	if gotB, ok := svc.ResolveStickySessionForKey(ctx, "shared-sticky", kB.ID); !ok || gotB.ID != "sess_b" {
		t.Fatalf("Tenant A unbind broke Tenant B sticky binding: %+v", gotB)
	}
	if gotAdm, ok := svc.ResolveStickySession(ctx, "shared-sticky"); !ok || gotAdm.ID != "sess_admin" {
		t.Fatalf("Tenant A unbind broke Admin sticky binding: %+v", gotAdm)
	}
}
