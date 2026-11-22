package vault

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/colabhive/colabhive/internal/models"
)

func TestCipherEncryptDecryptAtRest(t *testing.T) {
	c1 := NewCipher("prod-master-secret-9f81a2")
	c2 := NewCipher("different-master-secret")

	// Empty string roundtrip
	encEmpty, err := c1.EncryptString("")
	if err != nil || encEmpty != "" {
		t.Fatalf("expected empty encryption to return empty string, got %q (%v)", encEmpty, err)
	}
	decEmpty, err := c1.DecryptString("")
	if err != nil || decEmpty != "" {
		t.Fatalf("expected empty decryption to return empty string, got %q (%v)", decEmpty, err)
	}

	secret := "1//0gX9kL_refresh_token_super_secret"
	ctA, err := c1.EncryptString(secret)
	if err != nil {
		t.Fatalf("EncryptString failed: %v", err)
	}
	ctB, err := c1.EncryptString(secret)
	if err != nil {
		t.Fatalf("EncryptString second call failed: %v", err)
	}
	if ctA == ctB {
		t.Fatal("expected distinct random nonces across encryptions of identical plaintext")
	}
	if strings.Contains(ctA, secret) {
		t.Fatal("ciphertext leaked raw secret")
	}

	got, err := c1.DecryptString(ctA)
	if err != nil {
		t.Fatalf("DecryptString failed: %v", err)
	}
	if got != secret {
		t.Fatalf("expected %q, got %q", secret, got)
	}

	// Wrong key must fail authentication
	if _, err := c2.DecryptString(ctA); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed with wrong master key, got %v", err)
	}

	// Tampered ciphertext must fail authentication
	tampered := ctA[:len(ctA)-2] + "AA"
	if _, err := c1.DecryptString(tampered); err == nil {
		t.Fatal("expected error when decrypting tampered ciphertext")
	}
}

func TestPackAndUnpackBundlePBKDF2AndHKDF(t *testing.T) {
	accounts := []models.PortableAccountEntry{
		{
			Email:            "lab-ultra-01@gmail.com",
			Alias:            "Lab Ultra #1",
			SubscriptionTier: models.TierProPlus,
			Groups:           []string{"ml-research"},
			Tags:             []string{"g4", "a100"},
			ProxyURL:         "socks5://user:pass@10.0.0.12:1080",
			OAuth: models.PortableAccountOAuth{
				ClientID:     models.OfficialColabOAuthClientID,
				ClientSecret: models.OfficialColabOAuthClientSecret,
				TokenURI:     models.OfficialGoogleTokenURI,
				RefreshToken: "1//0gX9kL_test_refresh",
				Scopes:       models.OwnerOAuthScopes,
			},
			Guardrails: models.ContributorGuardrails{
				MinReserveCU:     50.0,
				MaxConcurrentVMs: 4,
			},
		},
	}
	proxies := []models.ProxyConfig{
		{
			ID:       "prx_01",
			Name:     "SJC SOCKS5",
			Protocol: models.ProxySOCKS5,
			URL:      "socks5://user:pass@10.0.0.12:1080",
		},
	}

	// 1. Encrypted PBKDF2-HMAC-SHA256 bundle (600,000 iterations)
	packed, err := PackBundle(accounts, proxies, "portable-vault-passphrase!")
	if err != nil {
		t.Fatalf("PackBundle failed: %v", err)
	}
	if !packed.Encrypted || len(packed.Accounts) != 0 || packed.KDF == nil || packed.Cipher == nil {
		t.Fatalf("expected encrypted envelope with stripped plaintext accounts: %+v", packed)
	}
	if packed.KDF.Iterations != DefaultPBKDF2Iterations {
		t.Fatalf("expected %d PBKDF2 iterations, got %d", DefaultPBKDF2Iterations, packed.KDF.Iterations)
	}

	rawJSON, err := json.Marshal(packed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawJSON), "1//0gX9kL_test_refresh") {
		t.Fatal("serialized .chvault leaked plaintext refresh token")
	}

	// Missing passphrase rejected
	if _, err := UnpackBundle(rawJSON, ""); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
	// Wrong passphrase rejected
	if _, err := UnpackBundle(rawJSON, "wrong-passphrase"); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}

	// Valid passphrase succeeds
	unpacked, err := UnpackBundle(rawJSON, "portable-vault-passphrase!")
	if err != nil {
		t.Fatalf("UnpackBundle failed: %v", err)
	}
	if len(unpacked.Accounts) != 1 || unpacked.Accounts[0].OAuth.RefreshToken != "1//0gX9kL_test_refresh" {
		t.Fatalf("unexpected unpacked accounts: %+v", unpacked.Accounts)
	}
	if len(unpacked.Proxies) != 1 || unpacked.Proxies[0].ID != "prx_01" {
		t.Fatalf("unexpected unpacked proxies: %+v", unpacked.Proxies)
	}

	// 2. HKDF-SHA256 bundle roundtrip
	hkdfPacked, err := PackBundleWithKDF(accounts, nil, "high-entropy-hkdf-secret", KDFAlgHKDFSHA256, 0)
	if err != nil {
		t.Fatalf("PackBundleWithKDF HKDF failed: %v", err)
	}
	hkdfBytes, _ := json.Marshal(hkdfPacked)
	hkdfUnpacked, err := UnpackBundle(hkdfBytes, "high-entropy-hkdf-secret")
	if err != nil {
		t.Fatalf("UnpackBundle HKDF failed: %v", err)
	}
	if len(hkdfUnpacked.Accounts) != 1 || hkdfUnpacked.Accounts[0].Email != "lab-ultra-01@gmail.com" {
		t.Fatalf("unexpected HKDF unpacked account: %+v", hkdfUnpacked.Accounts)
	}

	// 3. Plaintext admin bundle roundtrip
	plainPacked, err := PackBundle(accounts, proxies, "")
	if err != nil {
		t.Fatalf("plaintext PackBundle failed: %v", err)
	}
	if plainPacked.Encrypted {
		t.Fatal("expected Encrypted=false when passphrase is empty")
	}
	plainBytes, _ := json.Marshal(plainPacked)
	plainUnpacked, err := UnpackBundle(plainBytes, "")
	if err != nil || len(plainUnpacked.Accounts) != 1 {
		t.Fatalf("plaintext UnpackBundle failed: %v", err)
	}
}

func TestLoadOrCreateMasterKeyFilePersistenceAndIsolation(t *testing.T) {
	dir1 := filepath.Join(t.TempDir(), "install_a")
	dir2 := filepath.Join(t.TempDir(), "install_b")

	var wg sync.WaitGroup
	keys := make([]string, 8)
	for i := range keys {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			k, err := LoadOrCreateMasterKeyFile(dir1)
			if err != nil {
				t.Errorf("LoadOrCreateMasterKeyFile(dir1) failed: %v", err)
				return
			}
			keys[idx] = k
		}(i)
	}
	wg.Wait()

	key1 := keys[0]
	if len(key1) != 64 {
		t.Fatalf("expected 64-char hex random master key, got len=%d (%q)", len(key1), key1)
	}
	for i, k := range keys {
		if k != key1 {
			t.Fatalf("concurrent call %d returned different key %q != %q", i, k, key1)
		}
	}

	reloaded, err := LoadOrCreateMasterKeyFile(dir1)
	if err != nil || reloaded != key1 {
		t.Fatalf("expected persisted key reload %q, got %q (%v)", key1, reloaded, err)
	}

	key2, err := LoadOrCreateMasterKeyFile(dir2)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKeyFile(dir2) failed: %v", err)
	}
	if key2 == key1 {
		t.Fatal("distinct local installs must receive distinct random master keys")
	}
	if _, err := os.Stat(filepath.Join(dir1, MasterKeyFileName)); err != nil {
		t.Fatalf("expected .master.key on disk: %v", err)
	}
}

func TestLegacyFallbackCipherMigrationAndStrictCloudMode(t *testing.T) {
	legacyCipher := NewCipher("")
	legacyToken := "1//0gLegacyProdDBRefreshToken_hexwander"
	legacySealed, err := legacyCipher.EncryptString(legacyToken)
	if err != nil {
		t.Fatalf("legacy EncryptString failed: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "data")
	mk, err := LoadOrCreateMasterKeyFile(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKeyFile failed: %v", err)
	}

	// Local mode Cipher with `.master.key` MUST seamlessly decrypt legacy `colabhive_prod.db` ciphertexts!
	localCipher := NewCipher(mk)
	dec, err := localCipher.DecryptString(legacySealed)
	if err != nil || dec != legacyToken {
		t.Fatalf("expected seamless legacy fallback decryption, got %q (err=%v)", dec, err)
	}

	// Re-encrypting MUST seal with `.master.key`, which legacy default secret CANNOT decrypt!
	migratedSealed, err := localCipher.EncryptString(dec)
	if err != nil {
		t.Fatalf("migrated EncryptString failed: %v", err)
	}
	if _, err := legacyCipher.DecryptString(migratedSealed); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected legacyCipher to fail on .master.key ciphertext, got %v", err)
	}

	// Strict cloud Cipher MUST reject legacy fallback ciphertexts!
	strictCloud := NewStrictCipher(mk)
	if _, err := strictCloud.DecryptString(legacySealed); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected NewStrictCipher to reject legacy fallback ciphertext, got %v", err)
	}
	if gotStrict, err := strictCloud.DecryptString(migratedSealed); err != nil || gotStrict != legacyToken {
		t.Fatalf("expected NewStrictCipher to decrypt primary key ciphertext, got %q (%v)", gotStrict, err)
	}
}
