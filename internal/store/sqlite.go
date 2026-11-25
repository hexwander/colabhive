// Package store implements models.Store backed by pure-Go SQLite (modernc.org/sqlite)
// with WAL mode, schema migrations, explicit secret column persistence, EWMA burn-rate
// calibration, and saved idle-waste accounting.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested entity does not exist in SQLite.
// It wraps sql.ErrNoRows for standard errors.Is compatibility.
var ErrNotFound = sql.ErrNoRows

const ewmaAlpha = 0.30

var sqliteOpenMu sync.Mutex

// SQLiteStore implements models.Store over pure-Go modernc.org/sqlite.
type SQLiteStore struct {
	db      *sql.DB
	writeMu sync.Mutex
}

var _ models.Store = (*SQLiteStore)(nil)

// OpenSQLite opens (or creates) a SQLite database at dbPath, enables WAL & busy_timeout=5000,
// runs idempotent schema migrations, and returns a thread-safe models.Store.
// Pass ":memory:" for isolated in-memory unit tests.
func OpenSQLite(dbPath string) (models.Store, error) {
	sqliteOpenMu.Lock()
	defer sqliteOpenMu.Unlock()

	trimmed := strings.TrimSpace(dbPath)
	isMemory := trimmed == "" || trimmed == ":memory:" || strings.Contains(trimmed, "mode=memory")
	if trimmed == "" {
		trimmed = ":memory:"
	}

	if !isMemory && !strings.HasPrefix(trimmed, "file:") {
		dir := filepath.Dir(trimmed)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("store: create db directory %q: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", trimmed)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %q: %w", trimmed, err)
	}

	// Single-connection serialization guarantees shared :memory: state and eliminates
	// SQLITE_BUSY write-upgrade contention across concurrent goroutines while WAL speeds disk syncs.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	pragmas := []string{
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: apply pragma %q: %w", p, err)
		}
	}

	s := &SQLiteStore{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
	`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 1`).Scan(&count); err != nil {
		return fmt.Errorf("store: check migration v1: %w", err)
	}
	if count > 0 {
		if err := s.ensureAccountsProviderSecretColumnLocked(ctx); err != nil {
			return err
		}
		if err := s.ensureCaseUniqueExperimentsIndexLocked(ctx); err != nil {
			return err
		}
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ddl := []string{
		`CREATE TABLE IF NOT EXISTS proxies (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			protocol TEXT NOT NULL,
			url TEXT NOT NULL,
			created_at TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS accounts (
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
			encrypted_provider_secret TEXT NOT NULL DEFAULT '',
			oauth_client_id TEXT NOT NULL DEFAULT '',
			oauth_client_secret TEXT NOT NULL DEFAULT '',
			receipt_token_hash TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_email_lower ON accounts(LOWER(email));`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_receipt_hash ON accounts(receipt_token_hash);`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_proxy_id ON accounts(proxy_id);`,
		`CREATE TABLE IF NOT EXISTS invites (
			code TEXT PRIMARY KEY,
			revoked INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			account_id TEXT NOT NULL,
			experiment_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			proxy_token TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_name ON sessions(name);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_account ON sessions(account_id);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_experiment ON sessions(experiment_id);`,
		`CREATE TABLE IF NOT EXISTS experiments (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_experiments_id_lower ON experiments(LOWER(id));`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_hash TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS router_policy (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			updated_at TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS tool_traces (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_tool_traces_ts ON tool_traces(timestamp DESC, seq DESC);`,
		`CREATE TABLE IF NOT EXISTS burn_calibration (
			accelerator TEXT NOT NULL,
			shape TEXT NOT NULL,
			ewma_rate REAL NOT NULL,
			sample_count INTEGER NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (accelerator, shape)
		);`,
		`CREATE TABLE IF NOT EXISTS saved_waste_stats (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			saved_idle_waste_cu REAL NOT NULL DEFAULT 0,
			auto_teardowns_count INTEGER NOT NULL DEFAULT 0,
			zombie_aborts_count INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);`,
	}

	for _, stmt := range ddl {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: exec migration ddl: %w", err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations (version, applied_at) VALUES (1, ?)`, now); err != nil {
		return fmt.Errorf("store: record migration v1: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.ensureAccountsProviderSecretColumnLocked(ctx)
}

func (s *SQLiteStore) ensureAccountsProviderSecretColumnLocked(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(accounts);`)
	if err != nil {
		return fmt.Errorf("store: inspect accounts columns: %w", err)
	}
	hasTable := false
	hasProviderSecret := false
	for rows.Next() {
		hasTable = true
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if sErr := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); sErr != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan accounts table_info: %w", sErr)
		}
		if strings.EqualFold(strings.TrimSpace(name), "encrypted_provider_secret") {
			hasProviderSecret = true
		}
	}
	if rErr := rows.Err(); rErr != nil {
		_ = rows.Close()
		return fmt.Errorf("store: iterate accounts table_info: %w", rErr)
	}
	_ = rows.Close()

	if !hasTable || hasProviderSecret {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE accounts ADD COLUMN encrypted_provider_secret TEXT NOT NULL DEFAULT '';`); err != nil {
		return fmt.Errorf("store: add encrypted_provider_secret column to accounts: %w", err)
	}
	return nil
}

// Close closes the underlying SQLite database handle.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ============================================================================
// Accounts
// ============================================================================

func normalizeAccountDefaults(acc *models.Account) models.Account {
	cp := *acc
	now := time.Now().UTC()
	if cp.ID == "" {
		cp.ID = models.NewPrefixedID("acc_")
		acc.ID = cp.ID
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
		acc.CreatedAt = now
	}
	cp.UpdatedAt = now
	acc.UpdatedAt = now
	cp.Provider = models.NormalizeProviderType(string(cp.Provider))
	if strings.TrimSpace(string(acc.Provider)) == "" {
		acc.Provider = cp.Provider
	}
	if cp.Groups == nil {
		cp.Groups = []string{}
	}
	if cp.Tags == nil {
		cp.Tags = []string{}
	}
	if cp.GPUEntitlements == nil {
		cp.GPUEntitlements = map[models.AcceleratorType]models.GPUEntitlement{}
	}
	if cp.Guardrails.AllowedGPUs == nil {
		cp.Guardrails.AllowedGPUs = []models.AcceleratorType{}
	}
	// Never persist raw contributor receipt token inside payload_json.
	cp.Guardrails.ReceiptToken = ""
	return cp
}

func (s *SQLiteStore) SaveAccount(ctx context.Context, acc *models.Account) error {
	if acc == nil {
		return errors.New("store: nil account")
	}
	if strings.TrimSpace(acc.Email) == "" {
		return errors.New("store: account email is required")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	clean := normalizeAccountDefaults(acc)
	receiptHash := acc.ReceiptTokenHash

	// Monotonic owner/revocation guard across processes sharing SQLite (e.g. daemon + embedded MCP):
	// 1. Once an account row is promoted to an owner account (`is_contributor = 0`), a stale concurrent
	//    refresh/quota upsert that read `is_contributor = 1` earlier MUST NEVER revert `is_contributor`
	//    back to `1` or resurrect a cleared `receipt_token_hash`!
	// 2. Once a contributor account's receipt has been cleared (`receipt_token_hash = ''`, e.g. on revoke),
	//    only a fresh re-donation (`clean.Enabled && clean.Health == models.HealthHealthy`) may install a
	//    brand-new receipt hash — a stale refresh cannot restore the revoked hash!
	var existingIsContrib int
	var existingReceiptHash, existingHealth string
	if qErr := s.db.QueryRowContext(
		ctx,
		`SELECT is_contributor, receipt_token_hash, health FROM accounts WHERE id = ?`,
		clean.ID,
	).Scan(&existingIsContrib, &existingReceiptHash, &existingHealth); qErr == nil {
		if existingIsContrib == 0 && clean.IsContributor {
			clean.IsContributor = false
			clean.InviteCode = ""
			receiptHash = ""
		} else if existingIsContrib == 1 && existingReceiptHash == "" && receiptHash != "" {
			if !clean.Enabled || clean.Health == models.HealthRevokedByUser || existingHealth == string(models.HealthRevokedByUser) && clean.InviteCode == "" {
				receiptHash = ""
			}
		}
	}

	payload, err := json.Marshal(clean)
	if err != nil {
		return fmt.Errorf("store: marshal account: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO accounts (
			id, email, subscription_tier, health, enabled, priority, proxy_id,
			is_contributor, invite_code, encrypted_refresh_token, encrypted_access_token,
			encrypted_provider_secret, oauth_client_id, oauth_client_secret, receipt_token_hash,
			created_at, updated_at, payload_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			email = excluded.email,
			subscription_tier = excluded.subscription_tier,
			health = excluded.health,
			enabled = excluded.enabled,
			priority = excluded.priority,
			proxy_id = excluded.proxy_id,
			is_contributor = excluded.is_contributor,
			invite_code = excluded.invite_code,
			encrypted_refresh_token = excluded.encrypted_refresh_token,
			encrypted_access_token = excluded.encrypted_access_token,
			encrypted_provider_secret = excluded.encrypted_provider_secret,
			oauth_client_id = excluded.oauth_client_id,
			oauth_client_secret = excluded.oauth_client_secret,
			receipt_token_hash = excluded.receipt_token_hash,
			updated_at = excluded.updated_at,
			payload_json = excluded.payload_json
	`,
		clean.ID,
		clean.Email,
		string(clean.SubscriptionTier),
		string(clean.Health),
		boolToInt(clean.Enabled),
		clean.Priority,
		clean.ProxyID,
		boolToInt(clean.IsContributor),
		clean.InviteCode,
		acc.EncryptedRefreshToken,
		acc.EncryptedAccessToken,
		acc.EncryptedProviderSecret,
		acc.OAuthClientID,
		acc.OAuthClientSecret,
		receiptHash,
		clean.CreatedAt.UTC().Format(time.RFC3339Nano),
		clean.UpdatedAt.UTC().Format(time.RFC3339Nano),
		string(payload),
	)
	if err != nil {
		return fmt.Errorf("store: save account %q: %w", clean.ID, err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccountRow(r rowScanner) (*models.Account, error) {
	var (
		encRefresh        string
		encAccess         string
		encProviderSecret string
		clientID          string
		clientSecret      string
		receiptHash       string
		payloadJSON       string
	)
	if err := r.Scan(&encRefresh, &encAccess, &encProviderSecret, &clientID, &clientSecret, &receiptHash, &payloadJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: account", ErrNotFound)
		}
		return nil, fmt.Errorf("store: scan account: %w", err)
	}

	var acc models.Account
	if err := json.Unmarshal([]byte(payloadJSON), &acc); err != nil {
		return nil, fmt.Errorf("store: unmarshal account: %w", err)
	}
	acc.EncryptedRefreshToken = encRefresh
	acc.EncryptedAccessToken = encAccess
	acc.EncryptedProviderSecret = encProviderSecret
	acc.OAuthClientID = clientID
	acc.OAuthClientSecret = clientSecret
	acc.ReceiptTokenHash = receiptHash
	if strings.TrimSpace(string(acc.Provider)) == "" {
		acc.Provider = models.ProviderColab
	} else {
		acc.Provider = models.NormalizeProviderType(string(acc.Provider))
	}
	if acc.Groups == nil {
		acc.Groups = []string{}
	}
	if acc.Tags == nil {
		acc.Tags = []string{}
	}
	if acc.GPUEntitlements == nil {
		acc.GPUEntitlements = map[models.AcceleratorType]models.GPUEntitlement{}
	}
	return &acc, nil
}

const accountSelectCols = `encrypted_refresh_token, encrypted_access_token, encrypted_provider_secret, oauth_client_id, oauth_client_secret, receipt_token_hash, payload_json`

func (s *SQLiteStore) GetAccount(ctx context.Context, id string) (*models.Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountSelectCols+` FROM accounts WHERE id = ?`, id)
	acc, err := scanAccountRow(row)
	if err != nil {
		return nil, err
	}
	s.hydrateAccountProxy(ctx, acc)
	return acc, nil
}

func (s *SQLiteStore) GetAccountByEmail(ctx context.Context, email string) (*models.Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountSelectCols+` FROM accounts WHERE LOWER(email) = LOWER(?)`, strings.TrimSpace(email))
	acc, err := scanAccountRow(row)
	if err != nil {
		return nil, err
	}
	s.hydrateAccountProxy(ctx, acc)
	return acc, nil
}

func (s *SQLiteStore) GetAccountByReceiptHash(ctx context.Context, receiptHash string) (*models.Account, error) {
	if strings.TrimSpace(receiptHash) == "" {
		return nil, fmt.Errorf("%w: empty receipt hash", ErrNotFound)
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+accountSelectCols+` FROM accounts WHERE receipt_token_hash = ?`, receiptHash)
	acc, err := scanAccountRow(row)
	if err != nil {
		return nil, err
	}
	s.hydrateAccountProxy(ctx, acc)
	return acc, nil
}

func (s *SQLiteStore) ListAccounts(ctx context.Context) ([]*models.Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountSelectCols+` FROM accounts ORDER BY priority DESC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()

	out := make([]*models.Account, 0)
	for rows.Next() {
		acc, err := scanAccountRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		proxies, err := s.ListProxies(ctx)
		if err == nil && len(proxies) > 0 {
			pmap := make(map[string]*models.ProxyConfig, len(proxies))
			for _, p := range proxies {
				cp := *p
				cp.URL = "" // Do not leak raw proxy credentials inside Account JSON views
				pmap[p.ID] = &cp
			}
			for _, acc := range out {
				if acc.ProxyID != "" {
					if p, ok := pmap[acc.ProxyID]; ok {
						acc.Proxy = p
					}
				}
			}
		}
	}
	return out, nil
}

func (s *SQLiteStore) hydrateAccountProxy(ctx context.Context, acc *models.Account) {
	if acc == nil || acc.ProxyID == "" {
		return
	}
	if p, err := s.GetProxy(ctx, acc.ProxyID); err == nil {
		cp := *p
		cp.URL = ""
		acc.Proxy = &cp
	}
}

func (s *SQLiteStore) DeleteAccount(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete account %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: account %s", ErrNotFound, id)
	}
	return nil
}

// ============================================================================
// Proxies
// ============================================================================

func (s *SQLiteStore) SaveProxy(ctx context.Context, proxy *models.ProxyConfig) error {
	if proxy == nil {
		return errors.New("store: nil proxy")
	}
	if proxy.ID == "" {
		proxy.ID = models.NewPrefixedID("prx_")
	}
	if proxy.CreatedAt.IsZero() {
		proxy.CreatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(proxy)
	if err != nil {
		return fmt.Errorf("store: marshal proxy: %w", err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO proxies (id, name, protocol, url, created_at, payload_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			protocol = excluded.protocol,
			url = excluded.url,
			payload_json = excluded.payload_json
	`, proxy.ID, proxy.Name, string(proxy.Protocol), proxy.URL, proxy.CreatedAt.UTC().Format(time.RFC3339Nano), string(payload))
	if err != nil {
		return fmt.Errorf("store: save proxy %q: %w", proxy.ID, err)
	}
	return nil
}

func (s *SQLiteStore) GetProxy(ctx context.Context, id string) (*models.ProxyConfig, error) {
	var (
		rawURL      string
		payloadJSON string
		bound       int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT p.url, p.payload_json, (SELECT COUNT(*) FROM accounts a WHERE a.proxy_id = p.id)
		FROM proxies p WHERE p.id = ?
	`, id).Scan(&rawURL, &payloadJSON, &bound)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: proxy %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("store: get proxy %q: %w", id, err)
	}

	var cfg models.ProxyConfig
	if err := json.Unmarshal([]byte(payloadJSON), &cfg); err != nil {
		return nil, fmt.Errorf("store: unmarshal proxy: %w", err)
	}
	cfg.URL = rawURL
	cfg.BoundAccounts = bound
	return &cfg, nil
}

func (s *SQLiteStore) ListProxies(ctx context.Context) ([]*models.ProxyConfig, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.url, p.payload_json, (SELECT COUNT(*) FROM accounts a WHERE a.proxy_id = p.id)
		FROM proxies p ORDER BY p.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list proxies: %w", err)
	}
	defer rows.Close()

	out := make([]*models.ProxyConfig, 0)
	for rows.Next() {
		var (
			rawURL      string
			payloadJSON string
			bound       int
		)
		if err := rows.Scan(&rawURL, &payloadJSON, &bound); err != nil {
			return nil, err
		}
		var cfg models.ProxyConfig
		if err := json.Unmarshal([]byte(payloadJSON), &cfg); err != nil {
			return nil, err
		}
		cfg.URL = rawURL
		cfg.BoundAccounts = bound
		out = append(out, &cfg)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteProxy(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET proxy_id = '' WHERE proxy_id = ?`, id); err != nil {
		return fmt.Errorf("store: unbind accounts from proxy %q: %w", id, err)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete proxy %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: proxy %s", ErrNotFound, id)
	}
	return nil
}

// ============================================================================
// Runtime Sessions
// ============================================================================

func (s *SQLiteStore) SaveSession(ctx context.Context, sess *models.RuntimeSession) error {
	if sess == nil {
		return errors.New("store: nil session")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	if sess.ID == "" {
		sess.ID = models.NewPrefixedID("sess_")
	}
	if sess.Name == "" {
		sess.Name = sess.ID
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	sess.UpdatedAt = now
	if sess.Cells == nil {
		sess.Cells = []models.NotebookCell{}
	}
	if sess.Timeline == nil {
		sess.Timeline = []models.ExecutionStep{}
	}

	clean := *sess
	payload, err := json.Marshal(clean)
	if err != nil {
		return fmt.Errorf("store: marshal session: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, name, account_id, experiment_id, status, proxy_token, created_at, updated_at, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			account_id = excluded.account_id,
			experiment_id = excluded.experiment_id,
			status = excluded.status,
			proxy_token = excluded.proxy_token,
			updated_at = excluded.updated_at,
			payload_json = excluded.payload_json
		WHERE sessions.status NOT IN ('STOPPED', 'LOST') OR excluded.status IN ('STOPPED', 'LOST')
	`,
		clean.ID,
		clean.Name,
		clean.AccountID,
		clean.ExperimentID,
		string(clean.Status),
		clean.ProxyToken,
		clean.CreatedAt.UTC().Format(time.RFC3339Nano),
		clean.UpdatedAt.UTC().Format(time.RFC3339Nano),
		string(payload),
	)
	if err != nil {
		return fmt.Errorf("store: save session %q: %w", clean.ID, err)
	}
	return nil
}

func scanSessionRow(r rowScanner) (*models.RuntimeSession, error) {
	var proxyToken, payloadJSON string
	if err := r.Scan(&proxyToken, &payloadJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: session", ErrNotFound)
		}
		return nil, fmt.Errorf("store: scan session: %w", err)
	}
	var sess models.RuntimeSession
	if err := json.Unmarshal([]byte(payloadJSON), &sess); err != nil {
		return nil, fmt.Errorf("store: unmarshal session: %w", err)
	}
	sess.ProxyToken = proxyToken
	if sess.Cells == nil {
		sess.Cells = []models.NotebookCell{}
	}
	if sess.Timeline == nil {
		sess.Timeline = []models.ExecutionStep{}
	}
	return &sess, nil
}

func (s *SQLiteStore) GetSession(ctx context.Context, id string) (*models.RuntimeSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT proxy_token, payload_json FROM sessions WHERE id = ?`, id)
	return scanSessionRow(row)
}

func (s *SQLiteStore) GetSessionByName(ctx context.Context, name string) (*models.RuntimeSession, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT proxy_token, payload_json FROM sessions
		WHERE name = ?
		ORDER BY CASE WHEN status IN ('STOPPED', 'LOST', 'ERROR') THEN 1 ELSE 0 END ASC, created_at DESC
		LIMIT 1
	`, name)
	return scanSessionRow(row)
}

func (s *SQLiteStore) ListSessions(ctx context.Context) ([]*models.RuntimeSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proxy_token, payload_json FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()

	out := make([]*models.RuntimeSession, 0)
	for rows.Next() {
		sess, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete session %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	return nil
}

// ============================================================================
// Sharded Experiments
// ============================================================================

func (s *SQLiteStore) SaveExperiment(ctx context.Context, exp *models.Experiment) error {
	if exp == nil {
		return errors.New("store: nil experiment")
	}
	now := time.Now().UTC()
	if exp.ID == "" {
		exp.ID = models.NewPrefixedID("exp_")
	} else if !isASCIIStoreExperimentID(exp.ID) {
		return fmt.Errorf("store: invalid experiment ID %q: must be 1..128 ASCII [A-Za-z0-9._-] characters", exp.ID)
	}
	if exp.CreatedAt.IsZero() {
		exp.CreatedAt = now
	}
	exp.UpdatedAt = now
	if exp.Shards == nil {
		exp.Shards = []models.ExperimentShard{}
	}

	payload, err := json.Marshal(exp)
	if err != nil {
		return fmt.Errorf("store: marshal experiment: %w", err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var exactExists int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiments WHERE id = ?`, exp.ID).Scan(&exactExists)
	if exactExists == 0 {
		var conflictingID string
		if cErr := s.db.QueryRowContext(
			ctx,
			`SELECT id FROM experiments WHERE LOWER(id) = LOWER(?) AND id != ? LIMIT 1`,
			exp.ID,
			exp.ID,
		).Scan(&conflictingID); cErr == nil && conflictingID != "" {
			return fmt.Errorf("store: experiment %q already exists as %q", exp.ID, conflictingID)
		}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO experiments (id, status, created_at, updated_at, payload_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			updated_at = excluded.updated_at,
			payload_json = excluded.payload_json
	`,
		exp.ID,
		string(exp.Status),
		exp.CreatedAt.UTC().Format(time.RFC3339Nano),
		exp.UpdatedAt.UTC().Format(time.RFC3339Nano),
		string(payload),
	)
	if err != nil {
		return fmt.Errorf("store: save experiment %q: %w", exp.ID, err)
	}
	return nil
}

func (s *SQLiteStore) GetExperiment(ctx context.Context, id string) (*models.Experiment, error) {
	var payloadJSON string
	err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM experiments WHERE id = ?`, id).Scan(&payloadJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: experiment %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("store: get experiment %q: %w", id, err)
	}
	var exp models.Experiment
	if err := json.Unmarshal([]byte(payloadJSON), &exp); err != nil {
		return nil, fmt.Errorf("store: unmarshal experiment: %w", err)
	}
	if exp.Shards == nil {
		exp.Shards = []models.ExperimentShard{}
	}
	return &exp, nil
}

func (s *SQLiteStore) ListExperiments(ctx context.Context) ([]*models.Experiment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM experiments ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list experiments: %w", err)
	}
	defer rows.Close()

	out := make([]*models.Experiment, 0)
	for rows.Next() {
		var payloadJSON string
		if err := rows.Scan(&payloadJSON); err != nil {
			return nil, err
		}
		var exp models.Experiment
		if err := json.Unmarshal([]byte(payloadJSON), &exp); err != nil {
			return nil, err
		}
		if exp.Shards == nil {
			exp.Shards = []models.ExperimentShard{}
		}
		out = append(out, &exp)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteExperiment(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx, `DELETE FROM experiments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete experiment %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: experiment %s", ErrNotFound, id)
	}
	return nil
}

// ============================================================================
// Scoped Agent API Keys (`ch_sk_...`)
// ============================================================================

func (s *SQLiteStore) SaveAPIKey(ctx context.Context, key *models.AgentAPIKey) error {
	if key == nil {
		return errors.New("store: nil api key")
	}
	if strings.TrimSpace(key.KeyHash) == "" {
		return errors.New("store: api key KeyHash is required")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if key.ID == "" {
		key.ID = models.NewPrefixedID("key_")
	}
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}

	// Strip RawKey before writing payload_json so raw secret is never stored at rest.
	clean := *key
	clean.RawKey = ""
	if clean.AllowedGroups == nil {
		clean.AllowedGroups = []string{}
	}
	if clean.AllowedGPUs == nil {
		clean.AllowedGPUs = []models.AcceleratorType{}
	}

	payload, err := json.Marshal(clean)
	if err != nil {
		return fmt.Errorf("store: marshal api key: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO api_keys (id, key_hash, created_at, payload_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			key_hash = excluded.key_hash,
			payload_json = excluded.payload_json
	`, clean.ID, key.KeyHash, clean.CreatedAt.UTC().Format(time.RFC3339Nano), string(payload))
	if err != nil {
		return fmt.Errorf("store: save api key %q: %w", clean.ID, err)
	}
	return nil
}

func scanAPIKeyRow(r rowScanner) (*models.AgentAPIKey, error) {
	var keyHash, payloadJSON string
	if err := r.Scan(&keyHash, &payloadJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: api key", ErrNotFound)
		}
		return nil, fmt.Errorf("store: scan api key: %w", err)
	}
	var k models.AgentAPIKey
	if err := json.Unmarshal([]byte(payloadJSON), &k); err != nil {
		return nil, fmt.Errorf("store: unmarshal api key: %w", err)
	}
	k.KeyHash = keyHash
	if k.AllowedGroups == nil {
		k.AllowedGroups = []string{}
	}
	if k.AllowedGPUs == nil {
		k.AllowedGPUs = []models.AcceleratorType{}
	}
	return &k, nil
}

func (s *SQLiteStore) GetAPIKey(ctx context.Context, id string) (*models.AgentAPIKey, error) {
	row := s.db.QueryRowContext(ctx, `SELECT key_hash, payload_json FROM api_keys WHERE id = ?`, id)
	return scanAPIKeyRow(row)
}

func (s *SQLiteStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*models.AgentAPIKey, error) {
	row := s.db.QueryRowContext(ctx, `SELECT key_hash, payload_json FROM api_keys WHERE key_hash = ?`, keyHash)
	return scanAPIKeyRow(row)
}

func (s *SQLiteStore) ListAPIKeys(ctx context.Context) ([]*models.AgentAPIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key_hash, payload_json FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	defer rows.Close()

	out := make([]*models.AgentAPIKey, 0)
	for rows.Next() {
		k, err := scanAPIKeyRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteAPIKey(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete api key %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: api key %s", ErrNotFound, id)
	}
	return nil
}

// ============================================================================
// Router Policy & Call Traces
// ============================================================================

func (s *SQLiteStore) GetRouterPolicy(ctx context.Context) (*models.RouterPolicy, error) {
	var payloadJSON string
	err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM router_policy WHERE id = 1`).Scan(&payloadJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			def := models.DefaultRouterPolicy()
			return &def, nil
		}
		return nil, fmt.Errorf("store: get router policy: %w", err)
	}
	var pol models.RouterPolicy
	if err := json.Unmarshal([]byte(payloadJSON), &pol); err != nil {
		return nil, fmt.Errorf("store: unmarshal router policy: %w", err)
	}
	return &pol, nil
}

func (s *SQLiteStore) SaveRouterPolicy(ctx context.Context, policy *models.RouterPolicy) error {
	if policy == nil {
		return errors.New("store: nil router policy")
	}
	policy.UpdatedAt = time.Now().UTC()
	payload, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("store: marshal router policy: %w", err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO router_policy (id, updated_at, payload_json)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			updated_at = excluded.updated_at,
			payload_json = excluded.payload_json
	`, policy.UpdatedAt.Format(time.RFC3339Nano), string(payload))
	if err != nil {
		return fmt.Errorf("store: save router policy: %w", err)
	}
	return nil
}

func (s *SQLiteStore) AppendToolTrace(ctx context.Context, trace *models.ToolTraceEvent) error {
	if trace == nil {
		return errors.New("store: nil tool trace")
	}
	if trace.ID == "" {
		trace.ID = models.NewPrefixedID("trc_")
	}
	if trace.Timestamp.IsZero() {
		trace.Timestamp = time.Now().UTC()
	}
	payload, err := json.Marshal(trace)
	if err != nil {
		return fmt.Errorf("store: marshal tool trace: %w", err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_traces (id, timestamp, payload_json) VALUES (?, ?, ?)
	`, trace.ID, trace.Timestamp.UTC().Format(time.RFC3339Nano), string(payload)); err != nil {
		return fmt.Errorf("store: append tool trace: %w", err)
	}

	// Prune ancient rows beyond 5,000 traces to keep SQLite compact.
	_, _ = s.db.ExecContext(ctx, `
		DELETE FROM tool_traces WHERE seq NOT IN (
			SELECT seq FROM tool_traces ORDER BY seq DESC LIMIT 5000
		)
	`)
	return nil
}

func (s *SQLiteStore) ListToolTraces(ctx context.Context, limit int) ([]models.ToolTraceEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload_json FROM tool_traces ORDER BY timestamp DESC, seq DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list tool traces: %w", err)
	}
	defer rows.Close()

	out := make([]models.ToolTraceEvent, 0)
	for rows.Next() {
		var payloadJSON string
		if err := rows.Scan(&payloadJSON); err != nil {
			return nil, err
		}
		var ev models.ToolTraceEvent
		if err := json.Unmarshal([]byte(payloadJSON), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ============================================================================
// Empirical Burn Calibration EWMA & Saved Waste Ledger
// ============================================================================

func (s *SQLiteStore) RecordBurnSample(ctx context.Context, acc models.AcceleratorType, shape models.MachineShape, cuPerHour float64) error {
	if cuPerHour <= 0 {
		return nil
	}
	if shape == "" {
		shape = models.ShapeStandard
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var (
		prevRate  float64
		prevCount int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT ewma_rate, sample_count FROM burn_calibration WHERE accelerator = ? AND shape = ?
	`, string(acc), string(shape)).Scan(&prevRate, &prevCount)

	var nextRate float64
	var nextCount int
	if errors.Is(err, sql.ErrNoRows) {
		nextRate = cuPerHour
		nextCount = 1
	} else if err != nil {
		return fmt.Errorf("store: query prior burn rate: %w", err)
	} else {
		nextRate = ewmaAlpha*cuPerHour + (1.0-ewmaAlpha)*prevRate
		nextCount = prevCount + 1
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO burn_calibration (accelerator, shape, ewma_rate, sample_count, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(accelerator, shape) DO UPDATE SET
			ewma_rate = excluded.ewma_rate,
			sample_count = excluded.sample_count,
			updated_at = excluded.updated_at
	`, string(acc), string(shape), nextRate, nextCount, now)
	if err != nil {
		return fmt.Errorf("store: save burn sample: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetCalibratedBurnRate(ctx context.Context, acc models.AcceleratorType, shape models.MachineShape) (float64, bool, error) {
	if shape != "" {
		var rate float64
		err := s.db.QueryRowContext(ctx, `
			SELECT ewma_rate FROM burn_calibration WHERE accelerator = ? AND shape = ?
		`, string(acc), string(shape)).Scan(&rate)
		if err == nil {
			return rate, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, fmt.Errorf("store: get calibrated burn rate: %w", err)
		}
	}

	// Fallback: return any calibrated shape for the accelerator if exact shape had no samples.
	var rate float64
	err := s.db.QueryRowContext(ctx, `
		SELECT ewma_rate FROM burn_calibration WHERE accelerator = ? ORDER BY sample_count DESC LIMIT 1
	`, string(acc)).Scan(&rate)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("store: get fallback calibrated burn rate: %w", err)
	}
	return rate, true, nil
}

func (s *SQLiteStore) AddSavedWaste(ctx context.Context, savedCU float64, isZombieAbort bool) error {
	if savedCU < 0 {
		savedCU = 0
	}
	zombieInc := 0
	if isZombieAbort {
		zombieInc = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO saved_waste_stats (id, saved_idle_waste_cu, auto_teardowns_count, zombie_aborts_count, updated_at)
		VALUES (1, ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			saved_idle_waste_cu = saved_waste_stats.saved_idle_waste_cu + excluded.saved_idle_waste_cu,
			auto_teardowns_count = saved_waste_stats.auto_teardowns_count + 1,
			zombie_aborts_count = saved_waste_stats.zombie_aborts_count + excluded.zombie_aborts_count,
			updated_at = excluded.updated_at
	`, savedCU, zombieInc, now)
	if err != nil {
		return fmt.Errorf("store: add saved waste: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetSavedWasteStats(ctx context.Context) (models.SavedWasteStats, error) {
	var stats models.SavedWasteStats
	err := s.db.QueryRowContext(ctx, `
		SELECT saved_idle_waste_cu, auto_teardowns_count, zombie_aborts_count
		FROM saved_waste_stats WHERE id = 1
	`).Scan(&stats.SavedIdleWasteCU, &stats.AutoTeardownsCount, &stats.ZombieAbortsCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.SavedWasteStats{}, nil
		}
		return models.SavedWasteStats{}, fmt.Errorf("store: get saved waste stats: %w", err)
	}
	return stats, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *SQLiteStore) ensureCaseUniqueExperimentsIndexLocked(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin legacy experiment migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id, payload_json FROM experiments ORDER BY created_at ASC, rowid ASC`)
	if err != nil {
		return fmt.Errorf("store: scan legacy experiments for case collisions: %w", err)
	}
	type legacyExpRow struct {
		id      string
		payload string
	}
	var all []legacyExpRow
	for rows.Next() {
		var r legacyExpRow
		if sErr := rows.Scan(&r.id, &r.payload); sErr != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan legacy experiment row: %w", sErr)
		}
		all = append(all, r)
	}
	if rErr := rows.Err(); rErr != nil {
		_ = rows.Close()
		return rErr
	}
	_ = rows.Close()

	reservedLower := make(map[string]bool, len(all))
	for _, r := range all {
		if isASCIIStoreExperimentID(r.id) {
			reservedLower[strings.ToLower(r.id)] = true
		}
	}

	seenLower := make(map[string]bool, len(all))
	winnerOutDirs := make(map[string]string, len(all))
	winnerAPIKeyIDs := make(map[string]string, len(all))
	type dirRenameOp struct {
		from string
		to   string
	}
	var renamedDirs []dirRenameOp
	var createdCopyDirs []string

	rollbackRenamed := func() {
		for _, cpDir := range createdCopyDirs {
			_ = os.RemoveAll(cpDir)
		}
		for i := len(renamedDirs) - 1; i >= 0; i-- {
			_ = os.Rename(renamedDirs[i].to, renamedDirs[i].from)
		}
	}

	for idx, r := range all {
		asciiValid := isASCIIStoreExperimentID(r.id)
		lower := strings.ToLower(r.id)
		if asciiValid && !seenLower[lower] {
			seenLower[lower] = true
			var wExp models.Experiment
			wDir := filepath.Join(os.TempDir(), "colabhive-runs", r.id)
			wKey := ""
			if json.Unmarshal([]byte(r.payload), &wExp) == nil {
				if strings.TrimSpace(wExp.LocalOutputDir) != "" {
					wDir = strings.TrimRight(wExp.LocalOutputDir, `/\`)
				}
				wKey = strings.TrimSpace(wExp.APIKeyID)
			}
			winnerOutDirs[lower] = wDir
			winnerAPIKeyIDs[lower] = wKey
			continue
		}
		baseASCII := sanitizeStoreExperimentIDASCII(r.id)
		suffix := idx
		newID := fmt.Sprintf("%s_legacy_%d", baseASCII, suffix)
		for reservedLower[strings.ToLower(newID)] || seenLower[strings.ToLower(newID)] {
			suffix++
			newID = fmt.Sprintf("%s_legacy_%d", baseASCII, suffix)
		}
		reservedLower[strings.ToLower(newID)] = true
		seenLower[strings.ToLower(newID)] = true

		updatedPayload := r.payload
		var exp models.Experiment
		if json.Unmarshal([]byte(r.payload), &exp) == nil {
			exp.ID = newID
			oldOutDir := strings.TrimRight(exp.LocalOutputDir, `/\`)
			if oldOutDir == "" {
				oldOutDir = filepath.Join(os.TempDir(), "colabhive-runs", r.id)
			}
			candidateNewDir := oldOutDir
			if strings.EqualFold(filepath.Base(oldOutDir), r.id) {
				candidateNewDir = filepath.Join(filepath.Dir(oldOutDir), newID)
			}
			sharedWithWinner := isSameExistingPath(oldOutDir, winnerOutDirs[lower])
			oldExists := dirExists(oldOutDir)
			samePrincipal := strings.TrimSpace(exp.APIKeyID) == winnerAPIKeyIDs[lower]

			// Lossless directory handling (NEVER calls `os.RemoveAll` on `oldOutDir`):
			// - Distinct existing directory (`!sharedWithWinner`): `os.Rename(oldOutDir, candidateNewDir)` when `candidateNewDir`
			//   does not exist yet; otherwise keep `exp.LocalOutputDir = oldOutDir` untouched.
			// - Shared case-folded directory (`sharedWithWinner`): if owned by the same principal (`samePrincipal`), recursively
			//   copy cached artifacts into `candidateNewDir` so stopped-shard outputs remain reachable without touching `winnerDir`;
			//   if owned by a different principal, isolate `candidateNewDir` without copying another tenant's files!
			if candidateNewDir != oldOutDir {
				if oldExists && !sharedWithWinner {
					if !dirExists(candidateNewDir) {
						if rErr := os.Rename(oldOutDir, candidateNewDir); rErr != nil {
							rollbackRenamed()
							return fmt.Errorf("store: rename legacy experiment output dir %q -> %q: %w", oldOutDir, candidateNewDir, rErr)
						}
						renamedDirs = append(renamedDirs, dirRenameOp{from: oldOutDir, to: candidateNewDir})
						exp.LocalOutputDir = candidateNewDir
						if oldMerged := strings.TrimSpace(exp.MergedJSONLPath); oldMerged != "" && strings.EqualFold(filepath.Base(filepath.Dir(oldMerged)), r.id) {
							exp.MergedJSONLPath = filepath.Join(candidateNewDir, filepath.Base(oldMerged))
						}
					} else {
						exp.LocalOutputDir = oldOutDir
					}
				} else if oldExists && sharedWithWinner {
					for dirExists(candidateNewDir) {
						suffix++
						newID = fmt.Sprintf("%s_legacy_%d", baseASCII, suffix)
						for reservedLower[strings.ToLower(newID)] || seenLower[strings.ToLower(newID)] {
							suffix++
							newID = fmt.Sprintf("%s_legacy_%d", baseASCII, suffix)
						}
						reservedLower[strings.ToLower(newID)] = true
						seenLower[strings.ToLower(newID)] = true
						exp.ID = newID
						candidateNewDir = filepath.Join(filepath.Dir(oldOutDir), newID)
					}
					if samePrincipal {
						if cpErr := copyDirTreeLossless(oldOutDir, candidateNewDir); cpErr != nil {
							rollbackRenamed()
							return fmt.Errorf("store: copy shared legacy experiment output dir %q -> %q: %w", oldOutDir, candidateNewDir, cpErr)
						}
						createdCopyDirs = append(createdCopyDirs, candidateNewDir)
					} else {
						// Copy only shard checkpoints/notebooks explicitly recorded by `exp` (`sh.SyncedRecords > 0 || sh.CheckpointExpected`)
						// so stopped shards on `exp` keep their cached checkpoints without pulling unrelated files!
						if cpErr := copyOwnedShardFiles(oldOutDir, candidateNewDir, &exp); cpErr != nil {
							rollbackRenamed()
							return fmt.Errorf("store: copy owned shard checkpoints %q -> %q: %w", oldOutDir, candidateNewDir, cpErr)
						}
						createdCopyDirs = append(createdCopyDirs, candidateNewDir)
					}
					exp.LocalOutputDir = candidateNewDir
					if oldMerged := strings.TrimSpace(exp.MergedJSONLPath); oldMerged != "" && strings.EqualFold(filepath.Base(filepath.Dir(oldMerged)), r.id) {
						exp.MergedJSONLPath = filepath.Join(candidateNewDir, filepath.Base(oldMerged))
					}
				} else {
					if strings.TrimSpace(exp.LocalOutputDir) != "" || sharedWithWinner {
						exp.LocalOutputDir = candidateNewDir
					}
					if oldMerged := strings.TrimSpace(exp.MergedJSONLPath); oldMerged != "" && strings.EqualFold(filepath.Base(filepath.Dir(oldMerged)), r.id) {
						exp.MergedJSONLPath = filepath.Join(candidateNewDir, filepath.Base(oldMerged))
					}
				}
			}
			if b, mErr := json.Marshal(&exp); mErr == nil {
				updatedPayload = string(b)
			}
		}
		if _, uErr := tx.ExecContext(
			ctx,
			`UPDATE experiments SET id = ?, payload_json = ? WHERE id = ?`,
			newID,
			updatedPayload,
			r.id,
		); uErr != nil {
			rollbackRenamed()
			return fmt.Errorf("store: rename legacy case-colliding experiment %q -> %q: %w", r.id, newID, uErr)
		}

		// Atomically update linked sessions' `experiment_id` column and `payload_json` inside the same SQLite transaction!
		sRows, sQErr := tx.QueryContext(ctx, `SELECT id, payload_json FROM sessions WHERE experiment_id = ?`, r.id)
		if sQErr != nil {
			rollbackRenamed()
			return fmt.Errorf("store: query sessions linked to %q: %w", r.id, sQErr)
		}
		type sessLink struct {
			id      string
			payload string
		}
		var linked []sessLink
		for sRows.Next() {
			var sl sessLink
			if sErr := sRows.Scan(&sl.id, &sl.payload); sErr != nil {
				_ = sRows.Close()
				rollbackRenamed()
				return fmt.Errorf("store: scan linked session row: %w", sErr)
			}
			linked = append(linked, sl)
		}
		if srErr := sRows.Err(); srErr != nil {
			_ = sRows.Close()
			rollbackRenamed()
			return srErr
		}
		_ = sRows.Close()

		for _, sl := range linked {
			nextPayload := sl.payload
			var sessObj models.RuntimeSession
			if json.Unmarshal([]byte(sl.payload), &sessObj) == nil {
				sessObj.ExperimentID = newID
				if b, mErr := json.Marshal(&sessObj); mErr == nil {
					nextPayload = string(b)
				}
			}
			if _, suErr := tx.ExecContext(
				ctx,
				`UPDATE sessions SET experiment_id = ?, payload_json = ? WHERE id = ?`,
				newID,
				nextPayload,
				sl.id,
			); suErr != nil {
				rollbackRenamed()
				return fmt.Errorf("store: update session %q experiment_id -> %q: %w", sl.id, newID, suErr)
			}
		}
	}

	if _, idxErr := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_experiments_id_lower ON experiments(LOWER(id));`); idxErr != nil {
		rollbackRenamed()
		return fmt.Errorf("store: create case-insensitive experiment index: %w", idxErr)
	}
	if cErr := tx.Commit(); cErr != nil {
		rollbackRenamed()
		return cErr
	}
	return nil
}

func isASCIIStoreExperimentID(id string) bool {
	if len(id) == 0 || len(id) > 128 || id[0] == '.' || id[len(id)-1] == '.' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return !isStoreReservedWindowsStem(id)
}

func isStoreReservedWindowsStem(id string) bool {
	stem := id
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

func sanitizeStoreExperimentIDASCII(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw) && b.Len() < 96; i++ {
		c := raw[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if isAlphaNum || c == '_' || c == '-' || c == '.' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	res := strings.Trim(b.String(), ".")
	if res == "" || isStoreReservedWindowsStem(res) {
		res = "exp_" + res
		res = strings.Trim(res, ".")
	}
	return res
}

func dirExists(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isSameExistingPath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	fiA, errA := os.Stat(a)
	fiB, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return os.SameFile(fiA, fiB)
}

func copyOwnedShardFiles(srcDir, dstDir string, exp *models.Experiment) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	if exp == nil {
		return nil
	}
	allowed := make(map[string]bool)
	for _, sh := range exp.Shards {
		if sh.SyncedRecords > 0 || sh.CheckpointExpected || sh.FinalCheckpointSaved || sh.Status == "COMPLETED" || sh.Status == "COMPLETED_AND_UNASSIGNED" {
			allowed[fmt.Sprintf("shard_%d.jsonl", sh.ShardID)] = true
		}
		if strings.TrimSpace(sh.SessionName) != "" {
			allowed[sh.SessionName+".ipynb"] = true
		}
		if strings.TrimSpace(sh.SessionID) != "" {
			allowed[sh.SessionID+".ipynb"] = true
		}
	}
	for name := range allowed {
		srcPath := filepath.Join(srcDir, name)
		fi, err := os.Stat(srcPath)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, rErr := os.ReadFile(srcPath)
		if rErr != nil {
			return rErr
		}
		if wErr := os.WriteFile(filepath.Join(dstDir, name), data, 0o644); wErr != nil {
			return wErr
		}
	}
	return nil
}

func copyDirTreeLossless(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		srcPath := filepath.Join(srcDir, ent.Name())
		dstPath := filepath.Join(dstDir, ent.Name())
		if ent.IsDir() {
			if err := copyDirTreeLossless(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		info, iErr := ent.Info()
		if iErr != nil || !info.Mode().IsRegular() {
			continue
		}
		data, rErr := os.ReadFile(srcPath)
		if rErr != nil {
			return rErr
		}
		if wErr := os.WriteFile(dstPath, data, 0o644); wErr != nil {
			return wErr
		}
	}
	return nil
}
