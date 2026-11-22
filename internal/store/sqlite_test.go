package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

func TestSQLiteStoreFullLifecycleAndSecrets(t *testing.T) {
	ctx := context.Background()

	// Verify file-backed WAL mode + reopen persistence
	dbFile := filepath.Join(t.TempDir(), "colabhive_test.db")
	st, err := OpenSQLite(dbFile)
	if err != nil {
		t.Fatalf("OpenSQLite(%q) failed: %v", dbFile, err)
	}
	defer st.Close()

	// 1. Proxies CRUD
	prx := &models.ProxyConfig{
		ID:        "prx_sjc",
		Name:      "SJC Dedicated",
		Protocol:  models.ProxySOCKS5,
		URL:       "socks5://u:p@198.51.100.10:1080",
		MaskedURL: "socks5://198.51.*.*:1080",
		Healthy:   true,
	}
	if err := st.SaveProxy(ctx, prx); err != nil {
		t.Fatalf("SaveProxy: %v", err)
	}

	// 2. Accounts CRUD + Secret JSON:"-" Fields Preservation
	acc := &models.Account{
		ID:                    "acc_ultra_1",
		Email:                 "Researcher.One@Gmail.com",
		Alias:                 "Primary Ultra",
		SubscriptionTier:      models.TierAIUltra,
		Health:                models.HealthHealthy,
		Enabled:               true,
		Priority:              100,
		ProxyID:               prx.ID,
		PaidCUBalance:         482.5,
		IsContributor:         true,
		EncryptedRefreshToken: "v1:enc_refresh_abc",
		EncryptedAccessToken:  "v1:enc_access_xyz",
		OAuthClientID:         models.OfficialColabOAuthClientID,
		OAuthClientSecret:     models.OfficialColabOAuthClientSecret,
		ReceiptTokenHash:      "sha256_receipt_hash_001",
		Guardrails: models.ContributorGuardrails{
			MinReserveCU: 50.0,
			ReceiptToken: "ch_rcpt_must_not_persist_plaintext",
		},
	}
	if err := st.SaveAccount(ctx, acc); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}

	loadedAcc, err := st.GetAccountByEmail(ctx, "researcher.one@gmail.com")
	if err != nil {
		t.Fatalf("GetAccountByEmail: %v", err)
	}
	if loadedAcc.EncryptedRefreshToken != "v1:enc_refresh_abc" || loadedAcc.EncryptedAccessToken != "v1:enc_access_xyz" {
		t.Fatalf("encrypted tokens lost across SQLite roundtrip: %+v", loadedAcc)
	}
	if loadedAcc.OAuthClientSecret != models.OfficialColabOAuthClientSecret {
		t.Fatalf("oauth client secret lost: %q", loadedAcc.OAuthClientSecret)
	}
	if loadedAcc.Guardrails.ReceiptToken != "" {
		t.Fatalf("raw receipt token must be stripped at rest, got %q", loadedAcc.Guardrails.ReceiptToken)
	}
	if loadedAcc.Proxy == nil || loadedAcc.Proxy.URL != "" {
		t.Fatalf("expected hydrated masked proxy without raw URL, got %+v", loadedAcc.Proxy)
	}

	byRcpt, err := st.GetAccountByReceiptHash(ctx, "sha256_receipt_hash_001")
	if err != nil || byRcpt.ID != acc.ID {
		t.Fatalf("GetAccountByReceiptHash failed: %v", err)
	}

	loadedPrx, err := st.GetProxy(ctx, prx.ID)
	if err != nil || loadedPrx.BoundAccounts != 1 || loadedPrx.URL != prx.URL {
		t.Fatalf("GetProxy unexpected bound count or URL: %+v (%v)", loadedPrx, err)
	}

	// 3. Invites CRUD
	inv := &models.ContributorInvite{
		Code:           "ch_inv_lab2026",
		PoolTitle:      "ICML Cluster",
		MaintainerName: "Dr. Lin",
		MaxUses:        10,
	}
	if err := st.SaveInvite(ctx, inv); err != nil {
		t.Fatalf("SaveInvite: %v", err)
	}
	gotInv, err := st.GetInvite(ctx, inv.Code)
	if err != nil || gotInv.PoolTitle != "ICML Cluster" {
		t.Fatalf("GetInvite failed: %+v (%v)", gotInv, err)
	}

	// 4. Runtime Sessions (preserving ProxyToken json:"-")
	sess := &models.RuntimeSession{
		ID:          "sess_g4_01",
		Name:        "eval_g4_0",
		AccountID:   acc.ID,
		Endpoint:    "gpu-g4-s-abc123",
		ProxyURL:    "https://8080-gpu-g4.prod.colab.dev",
		ProxyToken:  "ChAKDm_secret_proxy_cap",
		Accelerator: models.AcceleratorG4,
		Status:      models.SessionIdle,
	}
	if err := st.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	gotSess, err := st.GetSessionByName(ctx, "eval_g4_0")
	if err != nil || gotSess.ProxyToken != "ChAKDm_secret_proxy_cap" {
		t.Fatalf("GetSessionByName lost ProxyToken: %+v (%v)", gotSess, err)
	}

	// 5. Sharded Experiments
	exp := &models.Experiment{
		ID:          "exp_qwen27b",
		Title:       "Qwen3-27B Eval",
		Status:      models.ExperimentRunning,
		Accelerator: models.AcceleratorG4,
		NumShards:   4,
		Shards:      []models.ExperimentShard{{ShardID: 0, SessionID: sess.ID, Status: "RUNNING"}},
	}
	if err := st.SaveExperiment(ctx, exp); err != nil {
		t.Fatalf("SaveExperiment: %v", err)
	}
	gotExp, err := st.GetExperiment(ctx, exp.ID)
	if err != nil || len(gotExp.Shards) != 1 {
		t.Fatalf("GetExperiment failed: %+v (%v)", gotExp, err)
	}

	// 6. Agent API Keys (preserving KeyHash json:"-" while stripping RawKey at rest)
	apiKey := &models.AgentAPIKey{
		ID:        "key_01",
		Name:      "Claude Code Local",
		KeyPrefix: "ch_sk_live_abcd",
		RawKey:    "ch_sk_live_abcd_raw_secret_once",
		KeyHash:   "sha256_key_hash_999",
		Enabled:   true,
	}
	if err := st.SaveAPIKey(ctx, apiKey); err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	if apiKey.RawKey == "" {
		t.Fatal("SaveAPIKey must not mutate caller's in-memory RawKey")
	}
	gotKey, err := st.GetAPIKeyByHash(ctx, "sha256_key_hash_999")
	if err != nil || gotKey.ID != apiKey.ID || gotKey.RawKey != "" {
		t.Fatalf("GetAPIKeyByHash unexpected result: %+v (%v)", gotKey, err)
	}

	// 7. RouterPolicy & ToolTraces
	pol, err := st.GetRouterPolicy(ctx)
	if err != nil || pol.DefaultStrategy != models.StrategyWasteZero {
		t.Fatalf("default GetRouterPolicy unexpected: %+v (%v)", pol, err)
	}
	pol.DefaultStrategy = models.StrategyMaxBalance
	if err := st.SaveRouterPolicy(ctx, pol); err != nil {
		t.Fatalf("SaveRouterPolicy: %v", err)
	}
	pol2, _ := st.GetRouterPolicy(ctx)
	if pol2.DefaultStrategy != models.StrategyMaxBalance {
		t.Fatalf("expected updated strategy, got %s", pol2.DefaultStrategy)
	}

	for i := 0; i < 3; i++ {
		if err := st.AppendToolTrace(ctx, &models.ToolTraceEvent{
			Caller:         "antigravity",
			Transport:      models.TransportMCPHTTP,
			ToolOrEndpoint: "execute_code",
			Status:         "ok",
			Timestamp:      time.Now().UTC().Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatalf("AppendToolTrace: %v", err)
		}
	}
	traces, err := st.ListToolTraces(ctx, 2)
	if err != nil || len(traces) != 2 {
		t.Fatalf("ListToolTraces(2) failed: len=%d err=%v", len(traces), err)
	}

	// 8. Empirical EWMA Burn Calibration & Saved Waste Stats
	if _, ok, err := st.GetCalibratedBurnRate(ctx, models.AcceleratorG4, models.ShapeHighRAM); err != nil || ok {
		t.Fatalf("expected no initial calibrated rate, got ok=%v err=%v", ok, err)
	}
	if err := st.RecordBurnSample(ctx, models.AcceleratorG4, models.ShapeHighRAM, 10.0); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordBurnSample(ctx, models.AcceleratorG4, models.ShapeHighRAM, 9.0); err != nil {
		t.Fatal(err)
	}
	rate, ok, err := st.GetCalibratedBurnRate(ctx, models.AcceleratorG4, models.ShapeHighRAM)
	if err != nil || !ok {
		t.Fatalf("GetCalibratedBurnRate failed: ok=%v err=%v", ok, err)
	}
	// EWMA: 0.3 * 9.0 + 0.7 * 10.0 = 9.70
	if math.Abs(rate-9.70) > 1e-6 {
		t.Fatalf("expected EWMA 9.70, got %.6f", rate)
	}

	if err := st.AddSavedWaste(ctx, 12.5, false); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSavedWaste(ctx, 7.5, true); err != nil {
		t.Fatal(err)
	}
	waste, err := st.GetSavedWasteStats(ctx)
	if err != nil || waste.SavedIdleWasteCU != 20.0 || waste.AutoTeardownsCount != 2 || waste.ZombieAbortsCount != 1 {
		t.Fatalf("unexpected SavedWasteStats: %+v (%v)", waste, err)
	}

	// 9. Deletions and ErrNotFound
	if err := st.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after DeleteSession, got %v", err)
	}
}

func TestSQLiteInMemoryConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite(:memory:) failed: %v", err)
	}
	defer st.Close()

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			acc := &models.Account{
				ID:               models.NewPrefixedID("acc_"),
				Email:            models.NewPrefixedID("u") + "@example.com",
				SubscriptionTier: models.TierPro,
				Health:           models.HealthHealthy,
				Enabled:          true,
			}
			if err := st.SaveAccount(ctx, acc); err != nil {
				t.Errorf("concurrent SaveAccount failed: %v", err)
			}
			if err := st.AppendToolTrace(ctx, &models.ToolTraceEvent{
				Caller:         "worker",
				ToolOrEndpoint: "pool_quota_summary",
				Status:         "ok",
			}); err != nil {
				t.Errorf("concurrent AppendToolTrace failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	accts, err := st.ListAccounts(ctx)
	if err != nil || len(accts) != 12 {
		t.Fatalf("expected 12 accounts in :memory:, got %d (%v)", len(accts), err)
	}
}

func TestConcurrentOpenSQLiteAndTerminalSessionGuard(t *testing.T) {
	ctx := context.Background()
	dbFile := filepath.Join(t.TempDir(), "concurrent_open.db")

	const n = 6
	stores := make([]models.Store, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s, err := OpenSQLite(dbFile)
			if err != nil {
				t.Errorf("concurrent OpenSQLite(%d) failed: %v", idx, err)
				return
			}
			stores[idx] = s
		}(i)
	}
	wg.Wait()
	defer func() {
		for _, s := range stores {
			if s != nil {
				_ = s.Close()
			}
		}
	}()

	st := stores[0]
	if st == nil {
		t.Fatal("expected non-nil store")
	}

	sess := &models.RuntimeSession{
		ID:        "sess_terminal_guard",
		Name:      "guard-vm",
		AccountID: "acc_1",
		Status:    models.SessionStopped,
	}
	if err := st.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession(STOPPED): %v", err)
	}

	// Stale writer attempting to overwrite STOPPED -> IDLE must be ignored!
	stale := &models.RuntimeSession{
		ID:        "sess_terminal_guard",
		Name:      "guard-vm",
		AccountID: "acc_1",
		Status:    models.SessionIdle,
	}
	if err := st.SaveSession(ctx, stale); err != nil {
		t.Fatalf("SaveSession(stale IDLE): %v", err)
	}
	got, err := st.GetSession(ctx, "sess_terminal_guard")
	if err != nil || got.Status != models.SessionStopped {
		t.Fatalf("expected STOPPED session to resist stale IDLE resurrection, got %+v (%v)", got, err)
	}
}
