package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
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

	// 3. Runtime Sessions (preserving ProxyToken json:"-")
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

func TestEncryptedProviderSecretRoundTripAndDefaultProvider(t *testing.T) {
	ctx := context.Background()
	dbFile := filepath.Join(t.TempDir(), "provider_secret.db")
	st, err := OpenSQLite(dbFile)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	legacyExtAcc := &models.Account{
		ID:                      "acc_legacy_secret_1",
		Email:                   "alice@legacy.pool",
		Alias:                   "Legacy · alice",
		Provider:                models.ProviderType("legacy_external"),
		ProviderMeta:            &models.ProviderAccountMeta{Username: "alice"},
		SubscriptionTier:        models.TierFree,
		Health:                  models.HealthHealthy,
		Enabled:                 true,
		IsContributor:           true,
		ReceiptTokenHash:        "sha256_legacy_rcpt_hash_1",
		EncryptedProviderSecret: "v1:aes_gcm_sealed_legacy_secret_payload",
	}
	if err := st.SaveAccount(ctx, legacyExtAcc); err != nil {
		t.Fatalf("SaveAccount(legacyExt): %v", err)
	}

	legacyColab := &models.Account{
		ID:                    "acc_legacy_empty_provider",
		Email:                 "owner.colab@gmail.com",
		SubscriptionTier:      models.TierPro,
		Health:                models.HealthHealthy,
		Enabled:               true,
		EncryptedRefreshToken: "v1:colab_refresh_tok",
	}
	if err := st.SaveAccount(ctx, legacyColab); err != nil {
		t.Fatalf("SaveAccount(legacyColab): %v", err)
	}

	// Verify JSON serialization NEVER leaks EncryptedProviderSecret.
	wireBytes, err := json.Marshal(legacyExtAcc)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(wireBytes), "aes_gcm_sealed_legacy_secret_payload") {
		t.Fatalf("EncryptedProviderSecret leaked in JSON payload: %s", string(wireBytes))
	}

	// Verify round-trip across GetAccount, GetAccountByEmail, GetAccountByReceiptHash, and ListAccounts.
	byID, err := st.GetAccount(ctx, legacyExtAcc.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if byID.EncryptedProviderSecret != "v1:aes_gcm_sealed_legacy_secret_payload" || byID.Provider != models.ProviderType("legacy_external") {
		t.Fatalf("GetAccount mismatch: secret=%q provider=%q", byID.EncryptedProviderSecret, byID.Provider)
	}

	byEmail, err := st.GetAccountByEmail(ctx, "ALICE@LEGACY.POOL")
	if err != nil {
		t.Fatalf("GetAccountByEmail: %v", err)
	}
	if byEmail.EncryptedProviderSecret != "v1:aes_gcm_sealed_legacy_secret_payload" || byEmail.Provider != models.ProviderType("legacy_external") {
		t.Fatalf("GetAccountByEmail mismatch: secret=%q provider=%q", byEmail.EncryptedProviderSecret, byEmail.Provider)
	}

	byReceipt, err := st.GetAccountByReceiptHash(ctx, "sha256_legacy_rcpt_hash_1")
	if err != nil {
		t.Fatalf("GetAccountByReceiptHash: %v", err)
	}
	if byReceipt.EncryptedProviderSecret != "v1:aes_gcm_sealed_legacy_secret_payload" || byReceipt.Provider != models.ProviderType("legacy_external") {
		t.Fatalf("GetAccountByReceiptHash mismatch: secret=%q provider=%q", byReceipt.EncryptedProviderSecret, byReceipt.Provider)
	}

	all, err := st.ListAccounts(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAccounts expected 2 accounts, got %d (%v)", len(all), err)
	}
	foundLegacyExt := false
	foundColab := false
	for _, a := range all {
		switch a.ID {
		case legacyExtAcc.ID:
			foundLegacyExt = true
			if a.EncryptedProviderSecret != "v1:aes_gcm_sealed_legacy_secret_payload" || a.Provider != models.ProviderType("legacy_external") {
				t.Fatalf("ListAccounts legacyExt mismatch: secret=%q provider=%q", a.EncryptedProviderSecret, a.Provider)
			}
		case legacyColab.ID:
			foundColab = true
			if a.Provider != models.ProviderColab {
				t.Fatalf("expected empty Provider to default to %q, got %q", models.ProviderColab, a.Provider)
			}
		}
	}
	if !foundLegacyExt || !foundColab {
		t.Fatalf("missing expected accounts in ListAccounts: legacyExt=%v colab=%v", foundLegacyExt, foundColab)
	}
}

func TestLegacySQLiteProdDBMigrationPreservesExistingAccounts(t *testing.T) {
	ctx := context.Background()
	dbFile := filepath.Join(t.TempDir(), "legacy_colabhive_prod.db")

	// 1. Construct a pre-v1.1 SQLite schema (version 1 WITHOUT encrypted_provider_secret column).
	rawDB, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("sql.Open legacy db: %v", err)
	}
	legacyDDL := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);`,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-06-01T00:00:00Z');`,
		`CREATE TABLE proxies (id TEXT PRIMARY KEY, name TEXT NOT NULL, protocol TEXT NOT NULL, url TEXT NOT NULL, created_at TEXT NOT NULL, payload_json TEXT NOT NULL);`,
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL,
			subscription_tier TEXT NOT NULL,
			health TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			priority INTEGER NOT NULL DEFAULT 0,
			proxy_id TEXT NOT NULL DEFAULT '',
			is_contributor INTEGER NOT NULL DEFAULT 0,
			invite_code TEXT NOT NULL DEFAULT '',
			encrypted_refresh_token TEXT NOT NULL DEFAULT '',
			encrypted_access_token TEXT NOT NULL DEFAULT '',
			oauth_client_id TEXT NOT NULL DEFAULT '',
			oauth_client_secret TEXT NOT NULL DEFAULT '',
			receipt_token_hash TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, name TEXT NOT NULL, account_id TEXT NOT NULL, experiment_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, proxy_token TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, payload_json TEXT NOT NULL);`,
		`CREATE TABLE experiments (id TEXT PRIMARY KEY, status TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, payload_json TEXT NOT NULL);`,
		`INSERT INTO accounts (
			id, email, subscription_tier, health, enabled, priority,
			encrypted_refresh_token, encrypted_access_token, receipt_token_hash,
			created_at, updated_at, payload_json
		) VALUES (
			'acc_prod_colab_1',
			'legacy.owner@gmail.com',
			'AI_ULTRA',
			'HEALTHY',
			1,
			100,
			'v1:legacy_refresh_token',
			'v1:legacy_access_token',
			'legacy_rcpt_hash',
			'2026-06-01T00:00:00Z',
			'2026-06-01T00:00:00Z',
			'{"id":"acc_prod_colab_1","email":"legacy.owner@gmail.com","subscription_tier":"AI_ULTRA","health":"HEALTHY","enabled":true,"paid_cu_balance":2500}'
		);`,
	}
	for _, stmt := range legacyDDL {
		if _, eErr := rawDB.ExecContext(ctx, stmt); eErr != nil {
			_ = rawDB.Close()
			t.Fatalf("exec legacy DDL failed: %v", eErr)
		}
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close legacy rawDB: %v", err)
	}

	// 2. Open with OpenSQLite -> idempotent migration adds encrypted_provider_secret without touching existing rows.
	st, err := OpenSQLite(dbFile)
	if err != nil {
		t.Fatalf("OpenSQLite on legacy v1 db failed: %v", err)
	}

	legacyAcc, err := st.GetAccount(ctx, "acc_prod_colab_1")
	if err != nil {
		_ = st.Close()
		t.Fatalf("GetAccount(legacy): %v", err)
	}
	if legacyAcc.EncryptedRefreshToken != "v1:legacy_refresh_token" || legacyAcc.PaidCUBalance != 2500 {
		_ = st.Close()
		t.Fatalf("legacy account fields corrupted during migration: %+v", legacyAcc)
	}
	if legacyAcc.Provider != models.ProviderColab || legacyAcc.EffectiveProvider() != models.ProviderColab {
		_ = st.Close()
		t.Fatalf("expected legacy account Provider=%q, got %q", models.ProviderColab, legacyAcc.Provider)
	}
	if legacyAcc.EncryptedProviderSecret != "" {
		_ = st.Close()
		t.Fatalf("expected empty EncryptedProviderSecret on legacy row, got %q", legacyAcc.EncryptedProviderSecret)
	}

	// 3. Save a second account on the upgraded DB and re-open to verify migration idempotency.
	newAcc := &models.Account{
		ID:                      "acc_colab_migrated",
		Email:                   "ops@gmail.com",
		Provider:                models.ProviderColab,
		SubscriptionTier:        models.TierPAYG,
		Health:                  models.HealthHealthy,
		Enabled:                 true,
		EncryptedProviderSecret: "v1:sealed_secret_compat",
	}
	if err := st.SaveAccount(ctx, newAcc); err != nil {
		_ = st.Close()
		t.Fatalf("SaveAccount(newAcc) after migration: %v", err)
	}
	_ = st.Close()

	st2, err := OpenSQLite(dbFile)
	if err != nil {
		t.Fatalf("second idempotent OpenSQLite failed: %v", err)
	}
	defer st2.Close()

	reloadedAcc, err := st2.GetAccount(ctx, "acc_colab_migrated")
	if err != nil || reloadedAcc.EncryptedProviderSecret != "v1:sealed_secret_compat" || reloadedAcc.Provider != models.ProviderColab {
		t.Fatalf("reloaded migrated account mismatch: %+v (%v)", reloadedAcc, err)
	}
}
