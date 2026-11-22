// Package vault implements AES-256-GCM encryption at rest for Google OAuth2 tokens
// alongside PBKDF2-HMAC-SHA256 and HKDF-SHA256 portable `.chvault` bundle packing/unpacking.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

const (
	// MasterKeyFileName is the restricted (0600) per-install key file stored inside DataDir.
	MasterKeyFileName = ".master.key"
	// BundleSchema is the canonical schema identifier for `.chvault` portable bundles.
	BundleSchema = "colabhive.credential_bundle"
	// BundleVersion is the wire format version for `.chvault` bundles.
	BundleVersion = "1.0"

	// KDFAlgPBKDF2SHA256 is the default password-based KDF for `.chvault` bundles.
	KDFAlgPBKDF2SHA256 = "PBKDF2-HMAC-SHA256"
	// KDFAlgHKDFSHA256 is an alternative high-entropy secret KDF for `.chvault` bundles.
	KDFAlgHKDFSHA256 = "HKDF-SHA256"
	// CipherAlgAES256GCM is the authenticated cipher algorithm used across vault & bundles.
	CipherAlgAES256GCM = "AES-256-GCM"

	// DefaultPBKDF2Iterations matches OWASP & colab-protocol-spec.md §2.3 Format D.
	DefaultPBKDF2Iterations = 600_000

	defaultFallbackMasterSecret = "colabhive-local-default-master-key-v1"
	atRestHKDFSalt              = "colabhive-vault-salt-v1"
	atRestHKDFInfo              = "colabhive-at-rest-aes-256-gcm"
	bundleHKDFInfo              = "colabhive-chvault-bundle-aes-256-gcm"
	cipherPrefixV1              = "v1:"
)

var (
	// ErrPassphraseRequired indicates an encrypted `.chvault` bundle was opened without a passphrase.
	ErrPassphraseRequired = errors.New("passphrase required to decrypt .chvault bundle")
	// ErrDecryptionFailed indicates authentication tag mismatch (wrong key/passphrase or tampered payload).
	ErrDecryptionFailed = errors.New("vault decryption failed: invalid key/passphrase or corrupted ciphertext")

	masterKeyFileMu sync.Mutex
)

func persistSecretFileAtomic(dir string, keyPath string, secret string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("vault: create data directory: %w", err)
	}
	if raw, err := os.ReadFile(keyPath); err == nil {
		if existing := strings.TrimSpace(string(raw)); len(existing) >= 16 {
			_ = os.Chmod(keyPath, 0o600)
			return existing, nil
		}
		return "", fmt.Errorf("vault: existing master key file %s is invalid or truncated", keyPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("vault: read master key file %s: %w", keyPath, err)
	}

	tmpFile, tErr := os.CreateTemp(dir, ".master.key.tmp.*")
	if tErr != nil {
		return "", fmt.Errorf("vault: create temp master key file: %w", tErr)
	}
	tmpPath := tmpFile.Name()
	_, wErr := tmpFile.WriteString(secret + "\n")
	_ = tmpFile.Chmod(0o600)
	sErr := tmpFile.Sync()
	cErr := tmpFile.Close()
	if wErr != nil || sErr != nil || cErr != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("vault: write temp master key file: %v", wErr)
	}

	// Publish the already-written, Sync()ed, and closed temp file atomically without overwriting
	// an existing `.master.key` and without ever creating a 0-byte destination file!
	if pubErr := publishSyncedTempFileNoOverwrite(tmpPath, keyPath); pubErr == nil {
		_ = os.Chmod(keyPath, 0o600)
		return secret, nil
	} else {
		if raw, rErr := os.ReadFile(keyPath); rErr == nil {
			if existing := strings.TrimSpace(string(raw)); len(existing) >= 16 {
				_ = os.Chmod(keyPath, 0o600)
				return existing, nil
			}
			return "", fmt.Errorf("vault: existing master key file %s is invalid or truncated", keyPath)
		}
		return "", fmt.Errorf("vault: persist master key file %s: %w", keyPath, pubErr)
	}
}

// LoadOrCreateMasterKeyFile loads an existing per-install master secret from `<dataDir>/.master.key`
// or atomically generates and persists a random 32-byte (256-bit) hex secret with `0600` permissions.
func LoadOrCreateMasterKeyFile(dataDir string) (string, error) {
	dir := strings.TrimSpace(dataDir)
	if dir == "" {
		return "", errors.New("vault: empty data directory for master key file")
	}

	masterKeyFileMu.Lock()
	defer masterKeyFileMu.Unlock()

	keyPath := filepath.Join(dir, MasterKeyFileName)
	if raw, err := os.ReadFile(keyPath); err == nil {
		if existing := strings.TrimSpace(string(raw)); len(existing) >= 16 {
			_ = os.Chmod(keyPath, 0o600)
			return existing, nil
		}
	}

	randBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, randBytes); err != nil {
		return "", fmt.Errorf("vault: generate random master key: %w", err)
	}
	secret := hex.EncodeToString(randBytes)
	return persistSecretFileAtomic(dir, keyPath, secret)
}

// LoadOrCreateMasterKeyForDB resolves the canonical `.master.key` co-located alongside `dbPath`
// (`filepath.Dir(dbPath)/.master.key`) so `colabhive serve`, `colabhive mcp`, and `colabhive-desktop`
// always derive the identical AES-256-GCM key for a given SQLite vault even when `--data-dir` differs
// from `filepath.Dir(--db)`. If a legacy `<fallbackDataDir>/.master.key` already exists while
// `<dbDir>/.master.key` does not, it migrates that exact secret into `<dbDir>/.master.key`.
func LoadOrCreateMasterKeyForDB(dbPath string, fallbackDataDir string) (string, error) {
	cleanDB := strings.TrimSpace(dbPath)
	cleanDataDir := strings.TrimSpace(fallbackDataDir)

	dbDir := ""
	if cleanDB != "" && cleanDB != ":memory:" {
		if absDB, err := filepath.Abs(cleanDB); err == nil {
			dbDir = filepath.Dir(absDB)
		} else {
			dbDir = filepath.Dir(cleanDB)
		}
	}
	if cleanDataDir != "" {
		if absData, err := filepath.Abs(cleanDataDir); err == nil {
			cleanDataDir = absData
		}
	}

	if dbDir == "" {
		return LoadOrCreateMasterKeyFile(cleanDataDir)
	}

	dbKeyPath := filepath.Join(dbDir, MasterKeyFileName)
	masterKeyFileMu.Lock()
	if raw, err := os.ReadFile(dbKeyPath); err == nil {
		if existing := strings.TrimSpace(string(raw)); len(existing) >= 16 {
			_ = os.Chmod(dbKeyPath, 0o600)
			masterKeyFileMu.Unlock()
			return existing, nil
		}
		masterKeyFileMu.Unlock()
		return "", fmt.Errorf("vault: existing master key file %s is invalid or truncated", dbKeyPath)
	}
	if cleanDataDir != "" && !sameDirectoryPath(cleanDataDir, dbDir) {
		altKeyPath := filepath.Join(cleanDataDir, MasterKeyFileName)
		if rawAlt, errAlt := os.ReadFile(altKeyPath); errAlt == nil {
			if existingAlt := strings.TrimSpace(string(rawAlt)); len(existingAlt) >= 16 {
				persisted, pErr := persistSecretFileAtomic(dbDir, dbKeyPath, existingAlt)
				masterKeyFileMu.Unlock()
				if pErr != nil {
					return "", pErr
				}
				return persisted, nil
			}
		}
	}
	masterKeyFileMu.Unlock()

	return LoadOrCreateMasterKeyFile(dbDir)
}

func sameDirectoryPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	fiA, errA := os.Stat(a)
	fiB, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(fiA, fiB)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// Cipher provides concurrent-safe AES-256-GCM encryption for Account credentials at rest.
type Cipher struct {
	aead       cipher.AEAD
	legacyAEAD cipher.AEAD
}

func deriveAtRestAEAD(secret string) cipher.AEAD {
	key, err := hkdf.Key(sha256.New, []byte(secret), []byte(atRestHKDFSalt), atRestHKDFInfo, 32)
	if err != nil {
		panic(fmt.Sprintf("vault: hkdf key derivation failed: %v", err))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(fmt.Sprintf("vault: aes cipher init failed: %v", err))
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("vault: gcm init failed: %v", err))
	}
	return gcm
}

// NewCipher derives a 256-bit AES-GCM key via HKDF-SHA256 from masterSecret.
// If masterSecret is empty, a deterministic local fallback secret is used.
// When masterSecret differs from defaultFallbackMasterSecret, a read-only secondary
// legacyAEAD is retained so pre-existing local vaults (`colabhive_prod.db`) authenticate
// and decrypt seamlessly, while EncryptBytes always seals with the primary masterSecret.
func NewCipher(masterSecret string) *Cipher {
	return NewCipherWithLegacyFallback(masterSecret)
}

// NewCipherWithLegacyFallback constructs a Cipher whose EncryptBytes uses masterSecret
// and whose DecryptBytes falls back to verifying defaultFallbackMasterSecret ciphertexts.
func NewCipherWithLegacyFallback(masterSecret string) *Cipher {
	if masterSecret == "" {
		masterSecret = defaultFallbackMasterSecret
	}
	primary := deriveAtRestAEAD(masterSecret)
	var legacy cipher.AEAD
	if masterSecret != defaultFallbackMasterSecret {
		legacy = deriveAtRestAEAD(defaultFallbackMasterSecret)
	}
	return &Cipher{
		aead:       primary,
		legacyAEAD: legacy,
	}
}

// NewStrictCipher constructs a Cipher that strictly requires masterSecret without legacy fallback
// (used in `--mode cloud` when an explicit `COLABHIVE_MASTER_KEY` is configured).
func NewStrictCipher(masterSecret string) *Cipher {
	if masterSecret == "" {
		masterSecret = defaultFallbackMasterSecret
	}
	return &Cipher{aead: deriveAtRestAEAD(masterSecret)}
}

// EncryptBytes seals plaintext with a fresh 96-bit cryptographic random nonce using the primary key.
func (c *Cipher) EncryptBytes(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("vault: generate nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptBytes opens a nonce-prefixed AES-256-GCM payload, falling back to legacyAEAD when configured.
func (c *Cipher) DecryptBytes(sealed []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(sealed) < ns {
		return nil, ErrDecryptionFailed
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err == nil {
		return pt, nil
	}
	if c.legacyAEAD != nil && len(sealed) >= c.legacyAEAD.NonceSize() {
		lns := c.legacyAEAD.NonceSize()
		if legacyPT, lErr := c.legacyAEAD.Open(nil, sealed[:lns], sealed[lns:], nil); lErr == nil {
			return legacyPT, nil
		}
	}
	return nil, ErrDecryptionFailed
}

// EncryptString encrypts a non-empty string into a versioned base64 representation (`v1:<b64>`).
// An empty input string returns an empty string without error.
func (c *Cipher) EncryptString(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	sealed, err := c.EncryptBytes([]byte(plaintext))
	if err != nil {
		return "", err
	}
	return cipherPrefixV1 + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// DecryptString decrypts a string previously produced by EncryptString.
// An empty input string returns an empty string without error.
func (c *Cipher) DecryptString(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	rawB64 := strings.TrimPrefix(encoded, cipherPrefixV1)
	sealed, err := base64.RawStdEncoding.DecodeString(rawB64)
	if err != nil {
		// Tolerate padded standard base64 if supplied externally.
		sealed, err = base64.StdEncoding.DecodeString(rawB64)
		if err != nil {
			return "", fmt.Errorf("vault: invalid base64 ciphertext: %w", err)
		}
	}
	pt, err := c.DecryptBytes(sealed)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// PackBundle packs portable accounts and proxies into a `.chvault` CredentialBundle.
// When passphrase is non-empty, the inner payload is encrypted with PBKDF2-HMAC-SHA256 + AES-256-GCM.
func (c *Cipher) PackBundle(accounts []models.PortableAccountEntry, proxies []models.ProxyConfig, passphrase string) (*models.CredentialBundle, error) {
	return PackBundle(accounts, proxies, passphrase)
}

// UnpackBundle parses and (if encrypted) decrypts a `.chvault` JSON bundle.
func (c *Cipher) UnpackBundle(rawJSON []byte, passphrase string) (*models.CredentialBundle, error) {
	return UnpackBundle(rawJSON, passphrase)
}

type bundleInnerPayload struct {
	Accounts []models.PortableAccountEntry `json:"accounts"`
	Proxies  []models.ProxyConfig          `json:"proxies,omitempty"`
}

// PackBundle builds a `colabhive.credential_bundle` v1.0 envelope.
// If passphrase is empty, returns a plaintext bundle (`Encrypted: false`);
// otherwise encrypts with PBKDF2-HMAC-SHA256 (600,000 iterations) + AES-256-GCM.
func PackBundle(accounts []models.PortableAccountEntry, proxies []models.ProxyConfig, passphrase string) (*models.CredentialBundle, error) {
	return PackBundleWithKDF(accounts, proxies, passphrase, KDFAlgPBKDF2SHA256, DefaultPBKDF2Iterations)
}

// PackBundleWithKDF packs a `.chvault` bundle using either PBKDF2-HMAC-SHA256 or HKDF-SHA256.
func PackBundleWithKDF(
	accounts []models.PortableAccountEntry,
	proxies []models.ProxyConfig,
	passphrase string,
	kdfAlg string,
	iterations int,
) (*models.CredentialBundle, error) {
	now := time.Now().UTC()
	if passphrase == "" {
		return &models.CredentialBundle{
			Schema:     BundleSchema,
			Version:    BundleVersion,
			ExportedAt: now,
			Encrypted:  false,
			Accounts:   accounts,
			Proxies:    proxies,
		}, nil
	}

	inner := bundleInnerPayload{
		Accounts: accounts,
		Proxies:  proxies,
	}
	plainBytes, err := json.Marshal(inner)
	if err != nil {
		return nil, fmt.Errorf("vault: marshal bundle payload: %w", err)
	}

	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("vault: generate kdf salt: %w", err)
	}

	if kdfAlg == "" {
		kdfAlg = KDFAlgPBKDF2SHA256
	}
	if iterations <= 0 && kdfAlg == KDFAlgPBKDF2SHA256 {
		iterations = DefaultPBKDF2Iterations
	}

	key, err := deriveBundleKey(kdfAlg, passphrase, salt, iterations)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: gcm init: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("vault: generate gcm nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plainBytes, nil)

	return &models.CredentialBundle{
		Schema:     BundleSchema,
		Version:    BundleVersion,
		ExportedAt: now,
		Encrypted:  true,
		KDF: &models.VaultKDFParams{
			Algorithm:  kdfAlg,
			Iterations: iterations,
			SaltB64:    base64.StdEncoding.EncodeToString(salt),
		},
		Cipher: &models.VaultCipherPayload{
			Algorithm:     CipherAlgAES256GCM,
			NonceB64:      base64.StdEncoding.EncodeToString(nonce),
			CiphertextB64: base64.StdEncoding.EncodeToString(ciphertext),
		},
	}, nil
}

// UnpackBundle unmarshals rawJSON and decrypts the `.chvault` payload when `Encrypted == true`.
func UnpackBundle(rawJSON []byte, passphrase string) (*models.CredentialBundle, error) {
	var bundle models.CredentialBundle
	if err := json.Unmarshal(rawJSON, &bundle); err != nil {
		return nil, fmt.Errorf("vault: invalid bundle JSON: %w", err)
	}
	return UnpackBundleStruct(&bundle, passphrase)
}

// UnpackBundleStruct decrypts an in-memory CredentialBundle if Encrypted is true.
func UnpackBundleStruct(bundle *models.CredentialBundle, passphrase string) (*models.CredentialBundle, error) {
	if bundle == nil {
		return nil, errors.New("vault: nil credential bundle")
	}
	if bundle.Schema != "" && bundle.Schema != BundleSchema {
		return nil, fmt.Errorf("vault: unsupported bundle schema %q", bundle.Schema)
	}
	if !bundle.Encrypted {
		out := *bundle
		if out.Schema == "" {
			out.Schema = BundleSchema
		}
		if out.Version == "" {
			out.Version = BundleVersion
		}
		return &out, nil
	}

	if passphrase == "" {
		return nil, ErrPassphraseRequired
	}
	if bundle.KDF == nil || bundle.Cipher == nil {
		return nil, errors.New("vault: encrypted bundle missing kdf or cipher metadata")
	}
	if bundle.Cipher.Algorithm != "" && !strings.EqualFold(bundle.Cipher.Algorithm, CipherAlgAES256GCM) {
		return nil, fmt.Errorf("vault: unsupported cipher algorithm %q", bundle.Cipher.Algorithm)
	}

	salt, err := decodeB64Flexible(bundle.KDF.SaltB64)
	if err != nil {
		return nil, fmt.Errorf("vault: invalid kdf salt_b64: %w", err)
	}
	nonce, err := decodeB64Flexible(bundle.Cipher.NonceB64)
	if err != nil {
		return nil, fmt.Errorf("vault: invalid cipher nonce_b64: %w", err)
	}
	ciphertext, err := decodeB64Flexible(bundle.Cipher.CiphertextB64)
	if err != nil {
		return nil, fmt.Errorf("vault: invalid cipher ciphertext_b64: %w", err)
	}

	key, err := deriveBundleKey(bundle.KDF.Algorithm, passphrase, salt, bundle.KDF.Iterations)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: gcm init: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("vault: invalid gcm nonce length %d", len(nonce))
	}

	plainBytes, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecryptionFailed
	}

	var inner bundleInnerPayload
	if err := json.Unmarshal(plainBytes, &inner); err != nil || (inner.Accounts == nil && inner.Proxies == nil) {
		// Also tolerate encrypted raw []PortableAccountEntry arrays produced by external scripts.
		var rawAccounts []models.PortableAccountEntry
		if arrErr := json.Unmarshal(plainBytes, &rawAccounts); arrErr == nil {
			inner.Accounts = rawAccounts
		} else if err != nil {
			return nil, fmt.Errorf("vault: invalid decrypted bundle payload: %w", err)
		}
	}

	return &models.CredentialBundle{
		Schema:     BundleSchema,
		Version:    bundle.Version,
		ExportedAt: bundle.ExportedAt,
		Encrypted:  false,
		Accounts:   inner.Accounts,
		Proxies:    inner.Proxies,
	}, nil
}

func deriveBundleKey(alg string, passphrase string, salt []byte, iterations int) ([]byte, error) {
	switch strings.ToUpper(strings.TrimSpace(alg)) {
	case "", KDFAlgPBKDF2SHA256, "PBKDF2":
		if iterations < 1000 {
			return nil, fmt.Errorf("vault: pbkdf2 iterations %d below minimum safety threshold", iterations)
		}
		return pbkdf2.Key(sha256.New, passphrase, salt, iterations, 32)
	case KDFAlgHKDFSHA256, "HKDF":
		return hkdf.Key(sha256.New, []byte(passphrase), salt, bundleHKDFInfo, 32)
	default:
		return nil, fmt.Errorf("vault: unsupported kdf algorithm %q", alg)
	}
}

func decodeB64Flexible(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
