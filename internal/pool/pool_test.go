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
	"sync"
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

func TestExternalContributorPortalGuardrailsAndRevoke(t *testing.T) {
	fixedNow := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) // 23:00 Asia/Shanghai (inside 22:00->09:00 window)
	currentNow := fixedNow

	ctx := context.Background()
	st, svc, mockSrv := setupTestPool(t, func(c *pool.Config) {
		c.NowFunc = func() time.Time { return currentNow }
	})

	inv, err := svc.CreateInvite(ctx, models.CreateInviteRequest{
		PoolTitle:      "NeurIPS Evaluation Pool",
		MaintainerName: "Prof. Zhang",
		MaxUses:        5,
		TTLHours:       48,
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	guardrails := models.ContributorGuardrails{
		MinReserveCU:      50.0,
		MaxDonatedCUTotal: 100.0,
		AllowPaidCUBurn:   true,
		AllowedGPUs:       []models.AcceleratorType{models.AcceleratorG4, models.AcceleratorL4, models.AcceleratorT4},
		MaxConcurrentVMs:  1,
		ScheduleWindow: &models.ScheduleWindow{
			Enabled:    true,
			Timezone:   "Asia/Shanghai",
			DaysOfWeek: []int{0, 1, 2, 3, 4, 5, 6},
			StartHour:  "22:00",
			EndHour:    "09:00",
		},
	}

	pending, err := svc.StartContribution(ctx, inv.Code, guardrails, models.OAuthModeRemoteCopyPaste)
	if err != nil {
		t.Fatalf("StartContribution: %v", err)
	}
	// Verify LEAST-PRIVILEGE scope: colaboratory included, drive & cloud-platform EXCLUDED!
	if strings.Contains(pending.AuthURL, "drive") || strings.Contains(pending.AuthURL, "cloud-platform") {
		t.Fatalf("security violation: contributor AuthURL requested Drive or GCP scope: %s", pending.AuthURL)
	}
	if !strings.Contains(pending.AuthURL, "colaboratory") {
		t.Fatalf("expected colaboratory scope in AuthURL: %s", pending.AuthURL)
	}

	receipt, err := svc.CompleteContribution(ctx, inv.Code, pending.State, "4/0A_contrib_alice")
	if err != nil {
		t.Fatalf("CompleteContribution: %v", err)
	}
	if !strings.HasPrefix(receipt.ReceiptToken, "ch_rcpt_") {
		t.Fatalf("expected ch_rcpt_ prefix, got %q", receipt.ReceiptToken)
	}

	contribAcc, err := svc.GetAccount(ctx, receipt.Status.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if !contribAcc.Guardrails.BlockDrivePropagation {
		t.Fatal("expected BlockDrivePropagation=true on contributor account")
	}

	// Verify contributor accounts are NEVER exported in .chvault bundles
	exported, err := svc.ExportVaultBundle(ctx, models.CredentialExportRequest{Encrypt: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Accounts) != 0 {
		t.Fatalf("security violation: contributor account leaked into ExportVaultBundle: %+v", exported.Accounts)
	}

	// Test CanScheduleAccount guardrails:
	// 1. Inside 22:00->09:00 Asia/Shanghai window & allowed G4 -> allowed!
	if ok, reason := svc.CanScheduleAccount(contribAcc, models.AcceleratorG4); !ok {
		t.Fatalf("expected G4 scheduling allowed, got false (%s)", reason)
	}
	// 2. Disallowed GPU (H100 not in AllowedGPUs) -> rejected!
	if ok, _ := svc.CanScheduleAccount(contribAcc, models.AcceleratorH100); ok {
		t.Fatal("expected H100 rejected by contributor AllowedGPUs")
	}
	// 3. Outside schedule window (06:00 UTC == 14:00 Asia/Shanghai) -> rejected!
	currentNow = time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	if ok, reason := svc.CanScheduleAccount(contribAcc, models.AcceleratorG4); ok {
		t.Fatalf("expected scheduling rejected outside window, got allowed (%s)", reason)
	}
	currentNow = fixedNow

	// 4. Burn 99 CU out of 100 CU MaxDonatedCUTotal -> remaining 1 CU < 2.415 CU (15m G4 runway) -> rejected!
	if err := svc.RecordAccountCUBurn(ctx, contribAcc.ID, 99.0); err != nil {
		t.Fatal(err)
	}
	contribAfterBurn, _ := svc.GetAccount(ctx, contribAcc.ID)
	if ok, _ := svc.CanScheduleAccount(contribAfterBurn, models.AcceleratorG4); ok {
		t.Fatal("expected G4 rejected when remaining donation cap is below 15-minute safety runway")
	}

	// 5. Contributor self-service guardrail update via receipt token
	paused := true
	updatedStatus, err := svc.UpdateContributionGuardrails(ctx, receipt.ReceiptToken, models.ContributorGuardrailsPatch{
		Paused: &paused,
	})
	if err != nil || !updatedStatus.Guardrails.Paused {
		t.Fatalf("UpdateContributionGuardrails failed: %+v (%v)", updatedStatus, err)
	}

	// Seed an active session on this contributor account to verify revoke stops it
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:        "sess_contrib_vm1",
		Name:      "contrib_vm1",
		AccountID: contribAcc.ID,
		Status:    models.SessionBusy,
	})

	// 6. 1-Click Self-Service Revoke -> calls oauth2.googleapis.com/revoke + wipes tokens + stops session
	if err := svc.RevokeContribution(ctx, receipt.ReceiptToken); err != nil {
		t.Fatalf("RevokeContribution failed: %v", err)
	}
	if mockSrv.RevokeCalls() != 1 {
		t.Fatalf("expected 1 call to Google /revoke endpoint, got %d", mockSrv.RevokeCalls())
	}
	rawStored, _ := st.GetAccount(ctx, contribAcc.ID)
	if rawStored.EncryptedRefreshToken != "" || rawStored.EncryptedAccessToken != "" {
		t.Fatal("expected encrypted OAuth tokens to be permanently wiped on RevokeContribution")
	}
	if rawStored.Health != models.HealthRevokedByUser {
		t.Fatalf("expected HealthRevokedByUser, got %s", rawStored.Health)
	}
	stoppedSess, _ := st.GetSession(ctx, "sess_contrib_vm1")
	if stoppedSess.Status != models.SessionStopped {
		t.Fatalf("expected active session to be marked STOPPED on revoke, got %s", stoppedSess.Status)
	}

	// 7. Verify revoked contributor balance is excluded from QuotaSummary rollups
	postRevokeSummary, err := svc.GetQuotaSummary(ctx)
	if err != nil {
		t.Fatalf("GetQuotaSummary after revoke failed: %v", err)
	}
	if postRevokeSummary.TotalAvailableCU != 0 || postRevokeSummary.SchedulableCU != 0 || postRevokeSummary.AccountsContributor != 0 || postRevokeSummary.AccountsTotal != 0 {
		t.Fatalf("revoked contributor leaked into QuotaSummary: %+v", postRevokeSummary)
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

func TestCompleteCredentialContributionSecurityOwnerGuardAndRevokeDrain(t *testing.T) {
	ctx := context.Background()

	var failRevoke atomic.Bool
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mockSrv := mockcolab.NewServer()
	defer mockSrv.Close()

	revokeProxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failRevoke.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"upstream_503"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
	}))
	defer revokeProxySrv.Close()

	cfg := mockSrv.PoolConfig("contrib-security-test")
	cfg.GoogleRevokeURI = revokeProxySrv.URL + "/revoke"
	svc := pool.NewConcreteService(st, events.NewHub(), cfg)

	// 1. Seed a real Owner account (`IsContributor: false`).
	ownerPending, _ := svc.StartOAuth(ctx, models.OAuthStartRequest{Alias: "Owner Ultra"})
	ownerAcc, err := svc.CompleteOAuth(ctx, ownerPending.State, "4/0A_ultra_01")
	if err != nil {
		t.Fatalf("CompleteOAuth owner: %v", err)
	}
	if ownerAcc.IsContributor {
		t.Fatal("expected owner account IsContributor=false")
	}

	// 2. Create single-use contributor invite (`MaxUses: 1`).
	inv, err := svc.CreateInvite(ctx, models.CreateInviteRequest{
		PoolTitle: "Security Test Pool",
		MaxUses:   1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 3. Invalid invite code MUST be rejected on CompleteCredentialContribution.
	validDonorCred := []byte(`{"refresh_token":"1//0contrib_refresh_alice","access_token":"ya29.contrib_access_alice"}`)
	if _, err := svc.CompleteCredentialContribution(ctx, "ch_inv_nonexistent", models.ContributorGuardrails{}, validDonorCred); err == nil {
		t.Fatal("expected fake invite code to be rejected")
	}

	// 4. Attempting to overwrite the existing Owner account via credential donation OR OAuth MUST be rejected!
	ownerCredAttempt := []byte(`{"email":"lab-ultra-01@stanford-ai.org","refresh_token":"1//0ultra_refresh_01","access_token":"ya29.ultra_access_01"}`)
	if _, err := svc.CompleteCredentialContribution(ctx, inv.Code, models.ContributorGuardrails{}, ownerCredAttempt); err == nil || !strings.Contains(err.Error(), "cannot overwrite an owner account") {
		t.Fatalf("expected owner overwrite via CompleteCredentialContribution to be rejected, got: %v", err)
	}
	contribOAuthPending, err := svc.StartContribution(ctx, inv.Code, models.ContributorGuardrails{}, models.OAuthModeRemoteCopyPaste)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteContribution(ctx, inv.Code, contribOAuthPending.State, "4/0A_ultra_01"); err == nil || !strings.Contains(err.Error(), "cannot overwrite an owner account") {
		t.Fatalf("expected owner overwrite via CompleteContribution OAuth to be rejected, got: %v", err)
	}
	ownerReloaded, _ := svc.GetAccount(ctx, ownerAcc.ID)
	if ownerReloaded.IsContributor {
		t.Fatal("owner account was mutated to IsContributor=true")
	}

	// 5. Concurrent MaxUses=1 race: out of 6 concurrent donations, at most 1 succeeds!
	mockSrv.RegisterAccount(mockcolab.MockAccount{
		Email:            "donor-race@uni.edu",
		SubscriptionTier: "SUBSCRIPTION_TIER_PRO",
		PaidCUBalance:    100,
		MaxVMs:           2,
		EligibleGPUs:     []string{"G4", "T4"},
		RefreshToken:     "1//0donor_race_rt",
		AccessToken:      "ya29.donor_race_at",
	})
	raceCred := []byte(`{"refresh_token":"1//0donor_race_rt","access_token":"ya29.donor_race_at"}`)
	var wg sync.WaitGroup
	var successes atomic.Int32
	var winningReceipt *models.ContributionReceipt
	var winMu sync.Mutex
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rcpt, cErr := svc.CompleteCredentialContribution(ctx, inv.Code, models.ContributorGuardrails{
				MinReserveCU:    20,
				AllowPaidCUBurn: true,
			}, raceCred)
			if cErr == nil && rcpt != nil {
				successes.Add(1)
				winMu.Lock()
				winningReceipt = rcpt
				winMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || winningReceipt == nil {
		t.Fatalf("expected exactly 1 redemption on MaxUses=1 invite, got %d", successes.Load())
	}

	// Verify working ManageURL receipt lookup + contributor security flags!
	rcptStatus, err := svc.GetContributionByReceipt(ctx, winningReceipt.ReceiptToken)
	if err != nil || !rcptStatus.Guardrails.BlockDrivePropagation {
		t.Fatalf("GetContributionByReceipt failed after CompleteCredentialContribution: %+v (%v)", rcptStatus, err)
	}
	donatedAcc, _ := st.GetAccount(ctx, rcptStatus.AccountID)
	if !donatedAcc.IsContributor || donatedAcc.ReceiptTokenHash == "" {
		t.Fatalf("expected IsContributor=true & persisted ReceiptTokenHash, got %+v", donatedAcc)
	}

	// 6. Wire SetSessionDrainer & verify RevokeContribution drains BEFORE revoking, preserves token on 503, and wipes on 400 invalid_token!
	var drainedAccounts []string
	var sawTokenDuringDrain bool
	svc.SetSessionDrainer(func(dCtx context.Context, accountID string) error {
		drainedAccounts = append(drainedAccounts, accountID)
		tok, _, tErr := svc.AcquireAccessTokenForTeardown(dCtx, accountID)
		if tErr == nil && tok != "" {
			sawTokenDuringDrain = true
		}
		return nil
	})

	failRevoke.Store(true)
	if err := svc.RevokeContribution(ctx, winningReceipt.ReceiptToken); err == nil {
		t.Fatal("expected RevokeContribution to return error when Google /revoke returns HTTP 503")
	}
	afterFailedRevoke, _ := st.GetAccount(ctx, donatedAcc.ID)
	if afterFailedRevoke.Health != models.HealthRevokedByUser || afterFailedRevoke.Enabled {
		t.Fatalf("expected immediate scheduling block even when Google /revoke fails: %+v", afterFailedRevoke)
	}
	if afterFailedRevoke.EncryptedRefreshToken == "" {
		t.Fatal("EncryptedRefreshToken must NOT be wiped before Google confirms revocation")
	}
	if !sawTokenDuringDrain || len(drainedAccounts) == 0 {
		t.Fatal("expected sessionDrainer to run with valid teardown access token before OAuth revoke")
	}

	// Retry once Google returns 400 invalid_token -> succeeds & wipes EncryptedRefreshToken!
	failRevoke.Store(false)
	if err := svc.RevokeContribution(ctx, winningReceipt.ReceiptToken); err != nil {
		t.Fatalf("expected RevokeContribution retry to succeed on HTTP 400 invalid_token, got: %v", err)
	}
	afterConfirmedRevoke, _ := st.GetAccount(ctx, donatedAcc.ID)
	if afterConfirmedRevoke.EncryptedRefreshToken != "" || afterConfirmedRevoke.EncryptedAccessToken != "" {
		t.Fatal("expected OAuth tokens wiped after confirmed Google revocation")
	}

	// 7. DeleteAccount invokes sessionDrainer and marks active sessions STOPPED.
	_ = st.SaveSession(ctx, &models.RuntimeSession{
		ID:        "sess_owner_del",
		Name:      "owner-del",
		AccountID: ownerAcc.ID,
		Status:    models.SessionIdle,
	})
	drainedAccounts = nil
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

func TestContributorMismatchedTokenPairAndOwnerReceiptClear(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mockSrv := mockcolab.NewServer()
	defer mockSrv.Close()
	svc := pool.NewConcreteService(st, events.NewHub(), mockSrv.PoolConfig("pair-check-secret"))

	ownerPending, _ := svc.StartOAuth(ctx, models.OAuthStartRequest{Alias: "Owner Main"})
	ownerAcc, err := svc.CompleteOAuth(ctx, ownerPending.State, "4/0A_ultra_01")
	if err != nil {
		t.Fatal(err)
	}

	mockSrv.RegisterAccount(mockcolab.MockAccount{
		Email:            "attacker-donor@uni.edu",
		SubscriptionTier: "SUBSCRIPTION_TIER_NONE",
		RefreshToken:     "1//0attacker_rt",
		AccessToken:      "ya29.attacker_at",
	})

	inv, err := svc.CreateInvite(ctx, models.CreateInviteRequest{PoolTitle: "Pair Guard Pool", MaxUses: 5})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Mismatched (OwnerRefreshToken, AttackerAccessToken) MUST be rejected!
	mismatched := []byte(`{"email":"attacker-donor@uni.edu","refresh_token":"1//0ultra_refresh_01","access_token":"ya29.attacker_at"}`)
	if _, mErr := svc.CompleteCredentialContribution(ctx, inv.Code, models.ContributorGuardrails{}, mismatched); mErr == nil {
		t.Fatal("SECURITY FAILURE: CompleteCredentialContribution accepted mismatched (OwnerRefreshToken, AttackerAccessToken)!")
	}

	// 2. Valid donor succeeds; if that account later completes Owner OAuth, ReceiptTokenHash MUST be cleared and receipt endpoints MUST return not found!
	validDonor := []byte(`{"refresh_token":"1//0attacker_rt","access_token":"ya29.attacker_at"}`)
	rcpt, err := svc.CompleteCredentialContribution(ctx, inv.Code, models.ContributorGuardrails{}, validDonor)
	if err != nil {
		t.Fatalf("valid donor failed: %v", err)
	}
	mockSrv.RegisterAccount(mockcolab.MockAccount{
		Email:            "attacker-donor@uni.edu",
		SubscriptionTier: "SUBSCRIPTION_TIER_PRO",
		AuthCode:         "4/0A_promote_owner",
		RefreshToken:     "1//0promoted_owner_rt",
		AccessToken:      "ya29.promoted_owner_at",
	})
	promotePending, _ := svc.StartOAuth(ctx, models.OAuthStartRequest{Alias: "Promoted Owner"})
	promoted, err := svc.CompleteOAuth(ctx, promotePending.State, "4/0A_promote_owner")
	if err != nil {
		t.Fatalf("owner promotion CompleteOAuth failed: %v", err)
	}
	reloadedPromoted, _ := st.GetAccount(ctx, promoted.ID)
	if reloadedPromoted.IsContributor || reloadedPromoted.ReceiptTokenHash != "" {
		t.Fatalf("expected owner CompleteOAuth to clear IsContributor & ReceiptTokenHash, got %+v", reloadedPromoted)
	}
	if _, gErr := svc.GetContributionByReceipt(ctx, rcpt.ReceiptToken); gErr == nil || !strings.Contains(gErr.Error(), "not found") {
		t.Fatalf("expected GetContributionByReceipt on promoted owner to fail with not found, got %v", gErr)
	}
	_ = ownerAcc
}
