package pool_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/events"
	"github.com/colabhive/colabhive/internal/mockcolab"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/store"
)

func setupTestPool(t *testing.T, extraCfg func(*pool.Config)) (models.Store, *pool.Service, *mockcolab.Server) {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mockSrv := mockcolab.NewServer()
	t.Cleanup(func() { _ = mockSrv.Close() })

	hub := events.NewHub()
	cfg := mockSrv.PoolConfig("unit-test-master-secret")
	if extraCfg != nil {
		extraCfg(&cfg)
	}
	svc := pool.NewConcreteService(st, hub, cfg)
	return st, svc, mockSrv
}

func TestOAuthPKCEStartAndCompleteRawAndURL(t *testing.T) {
	ctx := context.Background()
	st, svc, mockSrv := setupTestPool(t, nil)

	// 1. Start Owner OAuth flow
	pending, err := svc.StartOAuth(ctx, models.OAuthStartRequest{
		Alias:  "Ultra Node",
		Groups: []string{"ml-core"},
		Tags:   []string{"g4"},
	})
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	parsedAuthURL, err := url.Parse(pending.AuthURL)
	if err != nil {
		t.Fatalf("parse AuthURL: %v", err)
	}
	q := parsedAuthURL.Query()
	if q.Get("client_id") != models.OfficialColabOAuthClientID {
		t.Fatalf("expected official client_id, got %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != models.OfficialRemoteRedirectURI {
		t.Fatalf("expected official remote redirect_uri, got %q", q.Get("redirect_uri"))
	}
	if q.Get("token_usage") != "remote" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("missing token_usage=remote or S256 PKCE: %s", pending.AuthURL)
	}

	// Complete with raw code string
	acc1, err := svc.CompleteOAuth(ctx, pending.State, "  4/0A_ultra_01 \n")
	if err != nil {
		t.Fatalf("CompleteOAuth raw code failed: %v", err)
	}
	if acc1.Email != "lab-ultra-01@stanford-ai.org" || acc1.PaidCUBalance != 820.50 {
		t.Fatalf("unexpected completed account: %+v", acc1)
	}
	if mockSrv.TokenInfoSawAuthHeader() {
		t.Fatal("protocol violation: /tokeninfo must not receive Authorization header")
	}

	// Verify secrets are encrypted in SQLite and never serialized in JSON
	stored, err := st.GetAccount(ctx, acc1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored.EncryptedRefreshToken, "v1:") || strings.Contains(stored.EncryptedRefreshToken, "1//0") {
		t.Fatalf("expected AES-GCM encrypted refresh token, got %q", stored.EncryptedRefreshToken)
	}
	jsonBytes, _ := json.Marshal(stored)
	if strings.Contains(string(jsonBytes), "v1:") || strings.Contains(string(jsonBytes), "1//0") {
		t.Fatalf("Account JSON leaked encrypted/raw token: %s", string(jsonBytes))
	}

	// 2. Complete second account by pasting full Google callback URL (with URL-encoded slash %2F)
	pending2, err := svc.StartOAuth(ctx, models.OAuthStartRequest{Alias: "Berkeley Node"})
	if err != nil {
		t.Fatal(err)
	}
	pastedURL := "https://sdk.cloud.google.com/applicationdefaultauthcode.html?state=" +
		url.QueryEscape(pending2.State) + "&code=4%2F0A_proplus_02&scope=openid"
	acc2, err := svc.CompleteOAuth(ctx, "", pastedURL)
	if err != nil {
		t.Fatalf("CompleteOAuth pasted URL failed: %v", err)
	}
	if acc2.Email != "research-proplus-02@berkeley.edu" || acc2.SubscriptionTier != models.TierProPlus {
		t.Fatalf("unexpected second account: %+v", acc2)
	}
}

func TestProactiveTokenRefreshAndInvalidGrantTransition(t *testing.T) {
	ctx := context.Background()
	st, svc, mockSrv := setupTestPool(t, nil)

	pending, _ := svc.StartOAuth(ctx, models.OAuthStartRequest{})
	acc, err := svc.CompleteOAuth(ctx, pending.State, "4/0A_pro_03")
	if err != nil {
		t.Fatal(err)
	}

	// Force TokenExpiresAt to 2 minutes from now (within 5-minute proactive refresh margin)
	loaded, _ := st.GetAccount(ctx, acc.ID)
	soon := time.Now().UTC().Add(2 * time.Minute)
	loaded.TokenExpiresAt = &soon
	_ = st.SaveAccount(ctx, loaded)

	refreshedTok, _, err := svc.AcquireAccessToken(ctx, acc.ID)
	if err != nil {
		t.Fatalf("proactive AcquireAccessToken failed: %v", err)
	}
	if !strings.HasPrefix(refreshedTok, "ya29.refreshed_") {
		t.Fatalf("expected refreshed access token, got %q", refreshedTok)
	}

	// Simulate external revocation -> invalid_grant transitions account to AUTH_EXPIRED
	mockSrv.RevokeToken("1//0pro_refresh_03")
	loaded2, _ := st.GetAccount(ctx, acc.ID)
	expired := time.Now().UTC().Add(-1 * time.Minute)
	loaded2.TokenExpiresAt = &expired
	_ = st.SaveAccount(ctx, loaded2)

	if _, _, err := svc.AcquireAccessToken(ctx, acc.ID); err == nil {
		t.Fatal("expected error when refresh token returns invalid_grant")
	}
	afterRevoke, _ := svc.GetAccount(ctx, acc.ID)
	if afterRevoke.Health != models.HealthAuthExpired {
		t.Fatalf("expected HealthAuthExpired on invalid_grant, got %s", afterRevoke.Health)
	}
}

func TestDualQuotaPollingGPUEquivalenceAndEWMACalibration(t *testing.T) {
	ctx := context.Background()
	st, svc, _ := setupTestPool(t, nil)

	p1, _ := svc.StartOAuth(ctx, models.OAuthStartRequest{})
	acc, err := svc.CompleteOAuth(ctx, p1.State, "4/0A_ultra_01")
	if err != nil {
		t.Fatal(err)
	}

	// Record saved waste & burn calibration
	_ = st.RecordBurnSample(ctx, models.AcceleratorG4, models.ShapeHighRAM, 9.50)
	_ = st.AddSavedWaste(ctx, 15.25, true)

	summary, err := svc.RefreshAllQuotas(ctx)
	if err != nil {
		t.Fatalf("RefreshAllQuotas failed: %v", err)
	}
	if summary.AccountsTotal != 1 || summary.TotalAvailableCU != 820.50 {
		t.Fatalf("unexpected quota summary: %+v", summary)
	}
	if len(summary.GPUEquivalence) != 8 {
		t.Fatalf("expected 8 GPU equivalence entries, got %d", len(summary.GPUEquivalence))
	}
	g4Eq := summary.GPUEquivalence[models.AcceleratorG4]
	if g4Eq.CalibrationSource != models.CalibrationEmpirical || g4Eq.BurnRateCUHr != 9.50 || g4Eq.SchedulableHours <= 80 {
		t.Fatalf("unexpected G4 calibrated equivalence: %+v", g4Eq)
	}
	if summary.SavedIdleWasteCU != 15.25 || summary.ZombieAbortsCount != 1 {
		t.Fatalf("unexpected waste stats in summary: %+v", summary)
	}
	if acc.FreeQuota == nil || acc.FreeQuota.RemainingFreeCU != 18.50 {
		t.Fatalf("expected 18.50 Free CU parsed from 18500 milli-CCU, got %+v", acc.FreeQuota)
	}
}

func TestCredentialPortabilityKeyringADCJSONAndChvault(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Prepare realistic PlaintextKeyring (`[google_2Dcolab_2Dcli]` + `oauth_5ftoken`) & gcloud ADC files
	if got := pool.EscapeKeyringIdentifier("google-colab-cli"); got != "google_2Dcolab_2Dcli" {
		t.Fatalf("unexpected keyring escape: %q", got)
	}
	iniContent, err := pool.FormatPlaintextKeyringINI(pool.ParsedOAuthCredential{
		RefreshToken: "1//0ultra_refresh_01",
		AccessToken:  "ya29.ultra_access_01",
		Scopes:       models.OwnerOAuthScopes,
	})
	if err != nil {
		t.Fatalf("FormatPlaintextKeyringINI: %v", err)
	}
	keyringPassPath := filepath.Join(tmpDir, "keyring_pass.cfg")
	keyringRCPath := filepath.Join(tmpDir, "keyringrc.cfg")
	_ = os.WriteFile(keyringRCPath, []byte("[backend]\ndefault-keyring=keyrings.alt.file.PlaintextKeyring\n"), 0o600)
	_ = os.WriteFile(keyringPassPath, []byte(iniContent), 0o600)

	adcPath := filepath.Join(tmpDir, "application_default_credentials.json")
	adcJSON := `{
		"client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
		"client_secret": "d-FL95Q19q7MQmFpd7hHD0Ty",
		"quota_project_id": "colab-cli",
		"refresh_token": "1//0proplus_refresh_02",
		"type": "authorized_user"
	}`
	_ = os.WriteFile(adcPath, []byte(adcJSON), 0o600)

	st, svc, mockSrv := setupTestPool(t, func(c *pool.Config) {
		c.LocalScanRoots = []pool.LocalScanCandidate{
			{SourceType: "keyring_plaintext", SourcePath: keyringPassPath, KeyringRC: keyringRCPath, Environment: "wsl2"},
			{SourceType: "gcloud_adc", SourcePath: adcPath, Environment: "host"},
		}
	})

	// Zero-touch startup safety check: store must have 0 accounts before explicit scan/import!
	initialAccts, _ := svc.ListAccounts(ctx)
	if len(initialAccts) != 0 {
		t.Fatalf("startup safety violation: expected 0 accounts before explicit import, got %d", len(initialAccts))
	}

	discovered, err := svc.ScanLocalCredentials(ctx)
	if err != nil || len(discovered) != 2 {
		t.Fatalf("ScanLocalCredentials expected 2 items, got %d (%v)", len(discovered), err)
	}
	for _, d := range discovered {
		if strings.Contains(d.TokenPreview, "refresh") {
			t.Fatalf("TokenPreview leaked raw token suffix: %q", d.TokenPreview)
		}
	}

	imported, err := svc.ImportCredentials(ctx, models.CredentialImportRequest{
		Format:        "discovered",
		DiscoveredIDs: []string{discovered[0].ID, discovered[1].ID},
		DefaultGroups: []string{"imported-lab"},
	})
	if err != nil || len(imported) != 2 {
		t.Fatalf("ImportCredentials(discovered) failed: len=%d err=%v", len(imported), err)
	}

	// Export encrypted .chvault bundle and import into a second clean server instance
	bundle, err := svc.ExportVaultBundle(ctx, models.CredentialExportRequest{
		IncludeProxies: true,
		Encrypt:        true,
		Passphrase:     "cross-server-secret-2026",
	})
	if err != nil || !bundle.Encrypted {
		t.Fatalf("ExportVaultBundle failed: %+v (%v)", bundle, err)
	}
	bundleBytes, _ := json.Marshal(bundle)

	st2, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	svc2 := pool.NewConcreteService(st2, events.NewHub(), mockSrv.PoolConfig("second-server-master"))

	restored, err := svc2.ImportCredentials(ctx, models.CredentialImportRequest{
		Format:     "chvault",
		RawPayload: string(bundleBytes),
		Passphrase: "cross-server-secret-2026",
	})
	if err != nil || len(restored) != 2 {
		t.Fatalf("ImportCredentials(chvault) failed: len=%d err=%v", len(restored), err)
	}
	_ = st
}

func TestAccountGuardrailsAndScheduleWindow(t *testing.T) {
	fixedNow := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) // 23:00 Asia/Shanghai (inside 22:00->09:00 window)
	currentNow := fixedNow

	ctx := context.Background()
	_, svc, _ := setupTestPool(t, func(c *pool.Config) {
		c.NowFunc = func() time.Time { return currentNow }
	})

	pending, err := svc.StartOAuth(ctx, models.OAuthStartRequest{Alias: "Guarded Pro+"})
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	acc, err := svc.CompleteOAuth(ctx, pending.State, "4/0A_contrib_alice")
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}

	g := models.ContributorGuardrails{
		MinReserveCU:     50.0,
		AllowPaidCUBurn:  true,
		AllowedGPUs:      []models.AcceleratorType{models.AcceleratorG4, models.AcceleratorL4, models.AcceleratorT4},
		MaxConcurrentVMs: 1,
		ScheduleWindow: &models.ScheduleWindow{
			Enabled:    true,
			Timezone:   "Asia/Shanghai",
			DaysOfWeek: []int{0, 1, 2, 3, 4, 5, 6},
			StartHour:  "22:00",
			EndHour:    "09:00",
		},
	}
	acc, err = svc.UpdateAccount(ctx, acc.ID, models.AccountPatch{Guardrails: &g})
	if err != nil {
		t.Fatalf("UpdateAccount guardrails: %v", err)
	}

	// 1. Inside 22:00->09:00 Asia/Shanghai window & allowed G4 -> allowed!
	if ok, reason := svc.CanScheduleAccount(acc, models.AcceleratorG4); !ok {
		t.Fatalf("expected G4 scheduling allowed, got false (%s)", reason)
	}
	// 2. Disallowed GPU (H100 not in AllowedGPUs) -> rejected!
	if ok, _ := svc.CanScheduleAccount(acc, models.AcceleratorH100); ok {
		t.Fatal("expected H100 rejected by AllowedGPUs")
	}
	// 3. Outside schedule window (06:00 UTC == 14:00 Asia/Shanghai) -> rejected!
	currentNow = time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	if ok, reason := svc.CanScheduleAccount(acc, models.AcceleratorG4); ok {
		t.Fatalf("expected scheduling rejected outside window, got allowed (%s)", reason)
	}
	currentNow = fixedNow

	// 4. Paused guardrail -> rejected!
	g.Paused = true
	acc, _ = svc.UpdateAccount(ctx, acc.ID, models.AccountPatch{Guardrails: &g})
	if ok, _ := svc.CanScheduleAccount(acc, models.AcceleratorG4); ok {
		t.Fatal("expected paused account to be rejected")
	}
}

func TestTunnelOnlyAIUltraTierAndConcurrencyUpgrade(t *testing.T) {
	ctx := context.Background()

	// Simulate real Google Colab production behavior for colab-cli OAuth tokens (764086051850-...):
	// `/tun/m/ccu-info` succeeds with 200 OK + XSSI guard, while `colab.pa.googleapis.com/v1/user-info` returns 403.
	tunnelOnlySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tun/m/ccu-info":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(")]}'\n" + `{
				"currentBalance": 645.80,
				"consumptionRateHourly": 28.98,
				"assignmentsCount": 3,
				"eligibleGpus": ["G4", "A100", "T4"],
				"eligibleTpus": ["V6E1"]
			}`))
		case "/v1/user-info", "/v1beta/subscription":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"PERMISSION_DENIED"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer tunnelOnlySrv.Close()

	st, svc, _ := setupTestPool(t, func(c *pool.Config) {
		c.ColabTunnelDomain = tunnelOnlySrv.URL
		c.ColabGAPIDomain = tunnelOnlySrv.URL
		c.ColabPublicDomain = tunnelOnlySrv.URL
	})

	// Seed an imported account initially stored as FREE with MaxConcurrentVMs=1 (matching colab-cli import before first tunnel poll).
	seeded, err := svc.SeedAccountCredentials(
		ctx,
		&models.Account{
			ID:               "acc_lab_ultra_prod",
			Email:            "lab-ultra@example.com",
			SubscriptionTier: models.TierFree,
			MaxConcurrentVMs: 1,
			Health:           models.HealthMaxConcurrency,
			Enabled:          true,
			CreatedAt:        time.Now().UTC(),
		},
		"1//0lab_ultra_refresh_token",
		"ya29.lab_ultra_valid_access",
		1*time.Hour,
	)
	if err != nil {
		t.Fatalf("SeedAccountCredentials failed: %v", err)
	}
	if seeded.MaxConcurrentVMs != 1 {
		t.Fatalf("expected initial MaxConcurrentVMs=1 on FREE import, got %d", seeded.MaxConcurrentVMs)
	}

	updated, err := svc.RefreshAccountQuota(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("RefreshAccountQuota(tunnel-only) failed: %v", err)
	}
	if updated.SubscriptionTier != models.TierAIUltra {
		t.Fatalf("expected SubscriptionTier=AI_ULTRA from tunnel inference, got %s", updated.SubscriptionTier)
	}
	if updated.MaxConcurrentVMs != 5 {
		t.Fatalf("expected MaxConcurrentVMs upgraded from 1 to 5, got %d", updated.MaxConcurrentVMs)
	}
	if updated.ActiveAssignments != 3 || updated.PaidCUBalance != 645.80 {
		t.Fatalf("unexpected telemetry: balance=%.2f assignments=%d", updated.PaidCUBalance, updated.ActiveAssignments)
	}
	if updated.Health != models.HealthActiveWorking {
		t.Fatalf("expected Health=ACTIVE_WORKING (3/5, not MAX_CONCURRENCY), got %s (%s)", updated.Health, updated.HealthReason)
	}
	if ok, reason := svc.CanScheduleAccount(updated, models.AcceleratorG4); !ok {
		t.Fatalf("expected G4 scheduling allowed at 3/5 concurrency, got false (%s)", reason)
	}

	// Verify SQLite persistence of upgraded tier & MaxConcurrentVMs=5.
	persisted, err := st.GetAccount(ctx, seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.SubscriptionTier != models.TierAIUltra || persisted.MaxConcurrentVMs != 5 || persisted.Health != models.HealthActiveWorking {
		t.Fatalf("expected persisted AI_ULTRA / MaxConcurrentVMs=5 / ACTIVE_WORKING, got tier=%s max=%d health=%s",
			persisted.SubscriptionTier, persisted.MaxConcurrentVMs, persisted.Health)
	}
	if persisted.ReferenceCycleCU != 2500.0 {
		t.Fatalf("expected ReferenceCycleCU=2500.0 on AI_ULTRA account (not %.2f), got %.2f", persisted.PaidCUBalance, persisted.ReferenceCycleCU)
	}
}

func TestAIUltra2500CycleDenominatorOfficialSubscriptionAndCustomPatch(t *testing.T) {
	ctx := context.Background()

	// Verify TierStandardCycleCU constants
	if got := pool.TierStandardCycleCU(models.TierAIUltra); got != 2500.0 {
		t.Fatalf("expected AI_ULTRA standard cycle 2500.0, got %.2f", got)
	}
	if got := pool.TierStandardCycleCU(models.TierProPlus); got != 500.0 {
		t.Fatalf("expected PRO_PLUS standard cycle 500.0, got %.2f", got)
	}
	if got := pool.TierStandardCycleCU(models.TierPro); got != 100.0 {
		t.Fatalf("expected PRO standard cycle 100.0, got %.2f", got)
	}
	if got := pool.TierStandardCycleCU(models.TierPAYG); got != 100.0 {
		t.Fatalf("expected PAYG standard cycle 100.0, got %.2f", got)
	}
	if got := pool.TierStandardCycleCU(models.TierFree); got != 0 {
		t.Fatalf("expected FREE standard cycle 0, got %.2f", got)
	}

	// Simulate account with 633.24 CU where GAPI returns 403, /v1beta/subscription returns official AI_ULTRA,
	// and /tun/m/ccu-info returns {currentBalance: 633.24, consumptionRateHourly: 28.98, assignmentsCount: 3}.
	pubAndTunnelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tun/m/ccu-info":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(")]}'\n" + `{
				"currentBalance": 633.24,
				"consumptionRateHourly": 28.98,
				"assignmentsCount": 3
			}`))
		case "/v1/user-info":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"PERMISSION_DENIED"}`))
		case "/v1beta/subscription":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tier":"AI_ULTRA"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer pubAndTunnelSrv.Close()

	st, svc, _ := setupTestPool(t, func(c *pool.Config) {
		c.ColabTunnelDomain = pubAndTunnelSrv.URL
		c.ColabGAPIDomain = pubAndTunnelSrv.URL
		c.ColabPublicDomain = pubAndTunnelSrv.URL
	})

	// 1. Seed account with legacy ReferenceCycleCU=645.80 (like existing colabhive_prod.db) and verify auto-upgrade to 2500.0!
	seeded, err := svc.SeedAccountCredentials(
		ctx,
		&models.Account{
			ID:               "acc_ultra_633",
			Email:            "lab-ultra@example.com",
			SubscriptionTier: models.TierAIUltra,
			PaidCUBalance:    645.80,
			ReferenceCycleCU: 645.80,
			Enabled:          true,
			CreatedAt:        time.Now().UTC(),
		},
		"1//0ultra_633_ref",
		"ya29.ultra_633_acc",
		1*time.Hour,
	)
	if err != nil {
		t.Fatalf("SeedAccountCredentials: %v", err)
	}
	if seeded.ReferenceCycleCU != 2500.0 {
		t.Fatalf("expected legacy AI_ULTRA ReferenceCycleCU upgraded from 645.80 to 2500.0, got %.2f", seeded.ReferenceCycleCU)
	}

	// 2. RefreshAccountQuota with 633.24 CU -> ReferenceCycleCU stays 2500.0 (NOT 633.24!) and PublicAPISupported=true!
	refreshed, err := svc.RefreshAccountQuota(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("RefreshAccountQuota: %v", err)
	}
	if refreshed.SubscriptionTier != models.TierAIUltra || !refreshed.PublicAPISupported {
		t.Fatalf("expected AI_ULTRA with PublicAPISupported=true, got tier=%s pub=%v", refreshed.SubscriptionTier, refreshed.PublicAPISupported)
	}
	if refreshed.PaidCUBalance != 633.24 || refreshed.ReferenceCycleCU != 2500.0 {
		t.Fatalf("expected 633.24 / 2500.0 CU, got %.2f / %.2f CU", refreshed.PaidCUBalance, refreshed.ReferenceCycleCU)
	}

	// 3. Customize subscription_tier & reference_cycle_cu via UpdateAccount (PATCH /api/v1/accounts/{id} contract)
	proPlusTier := models.TierProPlus
	patchedTier, err := svc.UpdateAccount(ctx, seeded.ID, models.AccountPatch{
		SubscriptionTier: &proPlusTier,
	})
	if err != nil {
		t.Fatalf("UpdateAccount(subscription_tier=PRO_PLUS): %v", err)
	}
	if patchedTier.SubscriptionTier != models.TierProPlus {
		t.Fatalf("expected SubscriptionTier=PRO_PLUS, got %s", patchedTier.SubscriptionTier)
	}
	// Since PaidCUBalance is 633.24 (> 500 Pro+ pack) and CustomCycleCU is false, ReferenceCycleCU rolls up to 633.24.
	if patchedTier.ReferenceCycleCU != 633.24 {
		t.Fatalf("expected rollover ReferenceCycleCU=633.24 on PRO_PLUS with 633.24 balance, got %.2f", patchedTier.ReferenceCycleCU)
	}

	customCycle := 3000.0
	ultraTier := models.TierAIUltra
	patchedCustom, err := svc.UpdateAccount(ctx, seeded.ID, models.AccountPatch{
		SubscriptionTier: &ultraTier,
		ReferenceCycleCU: &customCycle,
	})
	if err != nil {
		t.Fatalf("UpdateAccount(custom 3000 CU): %v", err)
	}
	if !patchedCustom.CustomCycleCU || patchedCustom.ReferenceCycleCU != 3000.0 || patchedCustom.SubscriptionTier != models.TierAIUltra {
		t.Fatalf("expected CustomCycleCU=true & ReferenceCycleCU=3000.0 & AI_ULTRA, got %+v", patchedCustom)
	}

	// 4. Verify background RefreshAccountQuota respects CustomCycleCU=true (keeps 3000.0!) and persists in SQLite
	afterRefresh, err := svc.RefreshAccountQuota(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("RefreshAccountQuota after custom patch: %v", err)
	}
	if afterRefresh.ReferenceCycleCU != 3000.0 || !afterRefresh.CustomCycleCU {
		t.Fatalf("expected custom ReferenceCycleCU=3000.0 preserved across quota refresh, got %.2f (custom=%v)",
			afterRefresh.ReferenceCycleCU, afterRefresh.CustomCycleCU)
	}
	storedAfter, _ := st.GetAccount(ctx, seeded.ID)
	if storedAfter.ReferenceCycleCU != 3000.0 || !storedAfter.CustomCycleCU {
		t.Fatalf("expected SQLite persisted custom ReferenceCycleCU=3000.0, got %.2f (custom=%v)",
			storedAfter.ReferenceCycleCU, storedAfter.CustomCycleCU)
	}
}

func TestMaxConcurrencyCooldownOmittedTPUsMidnightResetAndExternalRollup(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 23, 50, 0, 0, time.UTC)

	// Tunnel server returning eligibleGpus WITHOUT eligibleTpus (real Colab production shape).
	tunSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tun/m/ccu-info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(")]}'\n" + `{
				"currentBalance": 700.00,
				"consumptionRateHourly": 28.98,
				"assignmentsCount": 3,
				"eligibleGpus": ["G4", "A100", "L4", "T4"]
			}`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer tunSrv.Close()

	st, svc, _ := setupTestPool(t, func(c *pool.Config) {
		c.ColabTunnelDomain = tunSrv.URL
		c.ColabGAPIDomain = tunSrv.URL
		c.ColabPublicDomain = tunSrv.URL
		c.NowFunc = func() time.Time { return now }
	})

	seeded, err := svc.SeedAccountCredentials(ctx, &models.Account{
		ID:               "acc_prod_1",
		Email:            "prod1@colabhive.dev",
		SubscriptionTier: models.TierAIUltra,
		MaxConcurrentVMs: 5,
		PaidCUBalance:    700,
		Enabled:          true,
		IsContributor:    true,
		Guardrails: models.ContributorGuardrails{
			MinReserveCU:      50,
			MaxDonatedCUDaily: 20,
			AllowPaidCUBurn:   true,
		},
	}, "1//0refresh_prod1", "ya29.access_prod1", 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// 1. RefreshAccountQuota with omitted eligibleTpus must NOT mark V6E1/V5E1 INELIGIBLE.
	refreshed, err := svc.RefreshAccountQuota(ctx, seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ent := refreshed.GPUEntitlements[models.AcceleratorV6E1]; ent.Status == models.EntitlementIneligible {
		t.Fatalf("expected V6E1 to remain eligible when eligibleTpus omitted, got %+v", ent)
	}

	// 2. ReconcileAccountSessionRollup across temporary local session MUST preserve Google's 3 external assignments & 28.98 CU/h!
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:           "sess_temp_local",
		Name:         "temp-g4",
		AccountID:    seeded.ID,
		Accelerator:  models.AcceleratorG4,
		Status:       models.SessionIdle,
		BurnRateCUHr: 9.66,
		CreatedAt:    now.Add(1 * time.Second),
	})
	rolledDuring, err := svc.ReconcileAccountSessionRollup(ctx, seeded.ID, nil, now.Add(2*time.Second))
	if err != nil || rolledDuring.ActiveAssignments != 4 || rolledDuring.LiveBurnRateCUHr != 38.64 {
		t.Fatalf("expected 4 active (3 external + 1 local) & 38.64 CU/h, got active=%d burn=%.2f err=%v",
			rolledDuring.ActiveAssignments, rolledDuring.LiveBurnRateCUHr, err)
	}
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:           "sess_temp_local",
		Name:         "temp-g4",
		AccountID:    seeded.ID,
		Accelerator:  models.AcceleratorG4,
		Status:       models.SessionStopped,
		BurnRateCUHr: 9.66,
		CreatedAt:    now.Add(1 * time.Second),
	})
	rolledAfter, err := svc.ReconcileAccountSessionRollup(ctx, seeded.ID, nil, now.Add(3*time.Second))
	if err != nil || rolledAfter.ActiveAssignments != 3 || rolledAfter.LiveBurnRateCUHr != 28.98 {
		t.Fatalf("expected external 3 assignments & 28.98 CU/h preserved after local stop, got active=%d burn=%.2f",
			rolledAfter.ActiveAssignments, rolledAfter.LiveBurnRateCUHr)
	}

	// 3. MarkAccountHealth(HealthMaxConcurrency, 10m) must NOT be clobbered during active CooldownUntil!
	if err := svc.MarkAccountHealth(ctx, seeded.ID, models.HealthMaxConcurrency, "HTTP 412", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	after412, _ := svc.GetAccount(ctx, seeded.ID)
	if after412.Health != models.HealthMaxConcurrency {
		t.Fatalf("expected HealthMaxConcurrency preserved during active cooldown, got %s", after412.Health)
	}
	if ok, _ := svc.CanScheduleAccount(after412, models.AcceleratorG4); ok {
		t.Fatal("expected CanScheduleAccount=false during active HTTP 412 cooldown")
	}

	// 4. Burn 20 CU on Day 1 -> hits daily donation cap; advance past UTC midnight (Day 2 + >10m cooldown) -> resets DonatedCUUsedToday to 0 and lifts 412 cooldown!
	if err := svc.RecordAccountCUBurn(ctx, seeded.ID, 20.0); err != nil {
		t.Fatal(err)
	}
	now = now.Add(15 * time.Minute) // 2026-11-22 00:05:00 UTC
	nextDayAcc, err := svc.GetAccount(ctx, seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if nextDayAcc.Guardrails.DonatedCUUsedToday != 0 || nextDayAcc.Guardrails.DonatedCUUsedTotal != 20.0 {
		t.Fatalf("expected midnight reset of DonatedCUUsedToday=0 (total=20), got today=%.2f total=%.2f",
			nextDayAcc.Guardrails.DonatedCUUsedToday, nextDayAcc.Guardrails.DonatedCUUsedTotal)
	}
	if nextDayAcc.Health == models.HealthMaxConcurrency {
		t.Fatalf("expected expired 10m HTTP 412 cooldown to lift, got %s", nextDayAcc.Health)
	}
	if ok, reason := svc.CanScheduleAccount(nextDayAcc, models.AcceleratorG4); !ok {
		t.Fatalf("expected G4 schedulable on Day 2 after midnight reset & cooldown expiry, got false (%s)", reason)
	}
}

func TestDeleteAccountDrainsActiveSessions(t *testing.T) {
	ctx := context.Background()
	st, svc, _ := setupTestPool(t, nil)

	ownerPending, _ := svc.StartOAuth(ctx, models.OAuthStartRequest{Alias: "Owner Ultra"})
	ownerAcc, err := svc.CompleteOAuth(ctx, ownerPending.State, "4/0A_ultra_01")
	if err != nil {
		t.Fatalf("CompleteOAuth owner: %v", err)
	}

	var drainedAccounts []string
	svc.SetSessionDrainer(func(_ context.Context, accountID string) error {
		drainedAccounts = append(drainedAccounts, accountID)
		return nil
	})

	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:        "sess_owner_del",
		Name:      "owner-del",
		AccountID: ownerAcc.ID,
		Status:    models.SessionIdle,
	})
	if err := svc.DeleteAccount(ctx, ownerAcc.ID); err != nil {
		t.Fatalf("DeleteAccount failed: %v", err)
	}
	if len(drainedAccounts) != 1 || drainedAccounts[0] != ownerAcc.ID {
		t.Fatalf("expected DeleteAccount to invoke sessionDrainer for %s, got %v", ownerAcc.ID, drainedAccounts)
	}
	delSess, _ := st.GetSession(ctx, "sess_owner_del")
	if delSess.Status != models.SessionStopped {
		t.Fatalf("expected session on deleted account to be STOPPED, got %s", delSess.Status)
	}
}

type saveCountSpyStore struct {
	models.Store
	saveAccountCalls atomic.Int32
}

func (s *saveCountSpyStore) SaveAccount(ctx context.Context, acc *models.Account) error {
	s.saveAccountCalls.Add(1)
	return s.Store.SaveAccount(ctx, acc)
}

func TestRefreshAccountQuotaNonColabReadOnlyNoOp(t *testing.T) {
	ctx := context.Background()
	rawSt, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer rawSt.Close()

	origPolledAt := time.Date(2026, 11, 20, 7, 30, 0, 0, time.UTC)
	const origCiphertext = "v1:encrypted-legacy-secret-must-never-change"

	legacyAcc := &models.Account{
		ID:                      "acc_legacy_ro_01",
		Email:                   "ro-legacy@external.pool",
		Alias:                   "Legacy Non-Colab Row",
		Provider:                models.ProviderType("legacy_external"),
		SubscriptionTier:        models.TierFree,
		Health:                  models.HealthAuthExpired,
		HealthReason:            "manual auth expired marker",
		Enabled:                 true,
		MaxConcurrentVMs:        2,
		EncryptedProviderSecret: origCiphertext,
		LastPolledAt:            &origPolledAt,
	}
	if err := rawSt.SaveAccount(ctx, legacyAcc); err != nil {
		t.Fatalf("SaveAccount(legacy): %v", err)
	}
	beforeRefresh, err := rawSt.GetAccount(ctx, legacyAcc.ID)
	if err != nil {
		t.Fatalf("GetAccount(before): %v", err)
	}

	spySt := &saveCountSpyStore{Store: rawSt}
	svc := pool.NewConcreteService(spySt, events.NewHub(), pool.Config{MasterSecret: "non-colab-ro-test"})

	got, err := svc.RefreshAccountQuota(ctx, legacyAcc.ID)
	if err != nil {
		t.Fatalf("RefreshAccountQuota(non-colab): %v", err)
	}
	if got == nil || got.Health != models.HealthAuthExpired {
		t.Fatalf("RefreshAccountQuota MUST NOT flip non-Colab Health; got %+v", got)
	}
	if spySt.saveAccountCalls.Load() != 0 {
		t.Fatalf("RefreshAccountQuota must be strictly read-only for non-Colab, got %d SaveAccount writes!", spySt.saveAccountCalls.Load())
	}

	// Verify DB row was NEVER rewritten: EncryptedProviderSecret, Health, HealthReason, UpdatedAt, LastPolledAt intact!
	persisted, err := rawSt.GetAccount(ctx, legacyAcc.ID)
	if err != nil {
		t.Fatalf("GetAccount(legacy): %v", err)
	}
	if persisted.EncryptedProviderSecret != origCiphertext {
		t.Fatalf("EncryptedProviderSecret mutated: got %q, want %q", persisted.EncryptedProviderSecret, origCiphertext)
	}
	if persisted.Health != models.HealthAuthExpired || persisted.HealthReason != "manual auth expired marker" {
		t.Fatalf("Health mutated in DB: health=%s reason=%q", persisted.Health, persisted.HealthReason)
	}
	if !persisted.UpdatedAt.Equal(beforeRefresh.UpdatedAt) || persisted.LastPolledAt == nil || !persisted.LastPolledAt.Equal(origPolledAt) {
		t.Fatalf("RefreshAccountQuota modified timestamps on non-Colab account: updatedAt=%v (was %v) lastPolledAt=%v",
			persisted.UpdatedAt, beforeRefresh.UpdatedAt, persisted.LastPolledAt)
	}

	// RefreshAllQuotas must also skip non-Colab accounts cleanly with ZERO writes!
	if _, err := svc.RefreshAllQuotas(ctx); err != nil {
		t.Fatalf("RefreshAllQuotas: %v", err)
	}
	if spySt.saveAccountCalls.Load() != 0 {
		t.Fatalf("RefreshAllQuotas must never write non-Colab accounts, got %d SaveAccount writes!", spySt.saveAccountCalls.Load())
	}
	persistedAfterAll, _ := rawSt.GetAccount(ctx, legacyAcc.ID)
	if persistedAfterAll.Health != models.HealthAuthExpired || persistedAfterAll.EncryptedProviderSecret != origCiphertext || !persistedAfterAll.UpdatedAt.Equal(beforeRefresh.UpdatedAt) {
		t.Fatalf("RefreshAllQuotas mutated non-Colab account: %+v", persistedAfterAll)
	}
}

func TestQuotaSummaryExcludesLegacyNonColabRows(t *testing.T) {
	ctx := context.Background()
	st, svc, _ := setupTestPool(t, nil)

	now := time.Now().UTC()
	colabAcc := &models.Account{
		ID:               "acc_colab_cap_1",
		Email:            "colab-cap@example.com",
		Provider:         models.ProviderColab,
		SubscriptionTier: models.TierPro,
		Health:           models.HealthHealthy,
		Enabled:          true,
		MaxConcurrentVMs: 2,
		PaidCUBalance:    100.0,
		SchedulableCU:    100.0,
		FreeQuota:        &models.FreeQuotaInfo{RemainingMilliCCU: 2000, RemainingFreeCU: 2.0},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	legacyExt := &models.Account{
		ID:               "acc_legacy_ext_cap_1",
		Email:            "ext-cap@legacy.pool",
		Provider:         models.ProviderType("legacy_external"),
		SubscriptionTier: models.TierFree,
		Health:           models.HealthHealthy,
		Enabled:          true,
		MaxConcurrentVMs: 4,
		FreeQuota:        &models.FreeQuotaInfo{RemainingMilliCCU: 58800, RemainingFreeCU: 58.8},
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	for _, acc := range []*models.Account{colabAcc, legacyExt} {
		if err := st.SaveAccount(ctx, acc); err != nil {
			t.Fatalf("SaveAccount(%s): %v", acc.ID, err)
		}
	}

	summary, err := svc.GetQuotaSummary(ctx)
	if err != nil {
		t.Fatalf("GetQuotaSummary: %v", err)
	}
	if summary.AccountsTotal != 1 || summary.AccountsHealthy != 1 {
		t.Fatalf("expected only Colab account counted in QuotaSummary, got total=%d healthy=%d",
			summary.AccountsTotal, summary.AccountsHealthy)
	}
	if summary.TotalVMSlots != 2 {
		t.Fatalf("expected TotalVMSlots=2 (Colab only), got %d", summary.TotalVMSlots)
	}
	if summary.FreeT4WindowsReady != 1 {
		t.Fatalf("expected FreeT4WindowsReady=1 (Colab only), got %d", summary.FreeT4WindowsReady)
	}
	t4Eq := summary.GPUEquivalence[models.AcceleratorT4]
	if t4Eq.EligibleAccounts != 1 || t4Eq.MaxParallelSlots != 2 || t4Eq.FreeWindowsReady != 1 {
		t.Fatalf("expected T4 GPUEquivalence strictly Colab-only, got %+v", t4Eq)
	}

	// CanScheduleAccount must reject legacy non-Colab accounts directly too.
	if ok, _ := svc.CanScheduleAccount(legacyExt, models.AcceleratorT4); ok {
		t.Fatalf("expected CanScheduleAccount(%s) == false for non-Colab provider %s", legacyExt.ID, legacyExt.Provider)
	}

	// ListAccounts (`/api/v1/accounts`) still loads both rows without error for DB compatibility.
	listed, err := svc.ListAccounts(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("expected 2 accounts visible in ListAccounts, got len=%d err=%v", len(listed), err)
	}
}

func TestReconcileAccountSessionRollupLiftsMaxConcurrencyWhenSlotFreed(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	st, svc, _ := setupTestPool(t, nil)
	cooldownUntil := now.Add(10 * time.Minute)
	acc := &models.Account{
		ID:                "acc_412_release",
		Email:             "slots@example.com",
		Provider:          models.ProviderColab,
		SubscriptionTier:  models.TierProPlus,
		PaidCUBalance:     65.0,
		SchedulableCU:     65.0,
		MaxConcurrentVMs:  4,
		ActiveAssignments: 2,
		Health:            models.HealthMaxConcurrency,
		HealthReason:      "HTTP 412 TooManyAssignments",
		CooldownUntil:     &cooldownUntil,
		Enabled:           true,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := st.SaveAccount(ctx, acc); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}

	// Only 1 active session remains (the user just stopped the 2nd session in the Workbench).
	if err := st.SaveSession(ctx, &models.RuntimeSession{
		ID:           "sess_still_busy",
		Name:         "w2_mech",
		AccountID:    acc.ID,
		Accelerator:  models.AcceleratorL4,
		Status:       models.SessionBusy,
		BurnRateCUHr: 1.94,
		CreatedAt:    now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveSession busy: %v", err)
	}
	if err := st.SaveSession(ctx, &models.RuntimeSession{
		ID:           "sess_just_stopped",
		Name:         "appbd_idle",
		AccountID:    acc.ID,
		Accelerator:  models.AcceleratorL4,
		Status:       models.SessionStopped,
		BurnRateCUHr: 1.94,
		CreatedAt:    now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveSession stopped: %v", err)
	}

	updated, err := svc.ReconcileAccountSessionRollup(ctx, acc.ID, nil, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("ReconcileAccountSessionRollup: %v", err)
	}
	if updated.ActiveAssignments != 1 {
		t.Fatalf("expected ActiveAssignments=1, got %d", updated.ActiveAssignments)
	}
	if updated.Health != models.HealthActiveWorking {
		t.Fatalf("expected MAX_CONCURRENCY lifted immediately to ACTIVE_WORKING when slot freed, got %s (%s)", updated.Health, updated.HealthReason)
	}
	if updated.CooldownUntil != nil {
		t.Fatalf("expected CooldownUntil cleared after freeing GPU slot, got %v", updated.CooldownUntil)
	}
	if ok, reason := svc.CanScheduleAccount(updated, models.AcceleratorT4); !ok {
		t.Fatalf("expected T4 scheduling allowed immediately after freeing GPU slot, got false (%s)", reason)
	}
}
