package pool_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
)

func TestScanLocalCredentialsResolvesRealEmailAndInPoolStatus(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a keyring_pass.cfg WITHOUT any email/account field — exactly like real colab-cli!
	iniNoEmail, err := pool.FormatPlaintextKeyringINI(pool.ParsedOAuthCredential{
		Email:        "",
		ClientID:     models.OfficialColabOAuthClientID,
		ClientSecret: models.OfficialColabOAuthClientSecret,
		TokenURI:     models.OfficialGoogleTokenURI,
		RefreshToken: "1//0proplus_refresh_02",
		AccessToken:  "",
		Scopes:       models.OwnerOAuthScopes,
	})
	if err != nil {
		t.Fatalf("FormatPlaintextKeyringINI: %v", err)
	}
	keyringPath := filepath.Join(tmpDir, "keyring_pass.cfg")
	if err := os.WriteFile(keyringPath, []byte(iniNoEmail), 0o600); err != nil {
		t.Fatal(err)
	}

	_, svc, _ := setupTestPool(t, func(c *pool.Config) {
		c.LocalScanRoots = []pool.LocalScanCandidate{
			{SourceType: "keyring_plaintext", SourcePath: keyringPath, Environment: "wsl2"},
		}
	})

	// 1. First scan BEFORE import: must resolve real Google email via OAuth userinfo & mark status="ready"!
	found1, err := svc.ScanLocalCredentials(ctx)
	if err != nil || len(found1) != 1 {
		t.Fatalf("ScanLocalCredentials(1) failed: len=%d err=%v", len(found1), err)
	}
	if found1[0].Email == "" {
		t.Fatalf("expected ScanLocalCredentials to resolve real email even when keyring_pass.cfg omits account/email, got %+v", found1[0])
	}
	if found1[0].EmailPreview == "" {
		t.Fatalf("expected non-empty EmailPreview, got %+v", found1[0])
	}
	if found1[0].AlreadyInPool {
		t.Fatalf("expected AlreadyInPool=false before import, got true")
	}
	if found1[0].Status != "ready" {
		t.Fatalf("expected Status='ready' before import, got %q", found1[0].Status)
	}

	// 2. Import the discovered credential.
	imported, err := svc.ImportCredentials(ctx, models.CredentialImportRequest{
		Format:        "discovered",
		DiscoveredIDs: []string{found1[0].ID},
	})
	if err != nil || len(imported) != 1 {
		t.Fatalf("ImportCredentials(discovered) failed: len=%d err=%v", len(imported), err)
	}
	if imported[0].Email != found1[0].Email {
		t.Fatalf("imported email %q != scanned email %q", imported[0].Email, found1[0].Email)
	}

	// 3. Re-scan AFTER import: must match local SQLite pool account & mark AlreadyInPool=true, Status="in_pool"!
	found2, err := svc.ScanLocalCredentials(ctx)
	if err != nil || len(found2) != 1 {
		t.Fatalf("ScanLocalCredentials(2) failed: len=%d err=%v", len(found2), err)
	}
	if !found2[0].AlreadyInPool || found2[0].Status != "in_pool" {
		t.Fatalf("expected AlreadyInPool=true and Status='in_pool' after import, got %+v", found2[0])
	}
	if found2[0].ExistingAccountID != imported[0].ID {
		t.Fatalf("expected ExistingAccountID=%q, got %q", imported[0].ID, found2[0].ExistingAccountID)
	}
}
