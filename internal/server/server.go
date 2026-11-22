// Package server implements ColabHive's unified HTTP, WebSocket (`/ws/events`),
// Streamable HTTP MCP (`/mcp`), Public Contributor Portal (`/api/public/contribute/*`),
// SPA static file server, `--demo` cluster seeder, and CLI client dispatcher.
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/colabhive/colabhive/internal/mcp"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/proxy"
)

const (
	maxLimiterTrackedKeys     = 8192
	maxUploadChunkBufferBytes = 128 << 20 // 128 MiB cap across in-flight chunked uploads
	uploadChunkEntryTTL       = 5 * time.Minute
	wsTicketTTL               = 60 * time.Second
	maxTrackedWSTickets       = 4096
	artifactSignatureTTL      = 24 * time.Hour
)

var errUploadChunkLimitExceeded = errors.New("chunked upload buffer limit (128 MiB) exceeded")

// DaemonInfo describes the active local ColabHive control-plane daemon stored at `~/.colabhive/daemon.json`
// so `colabhive mcp` (`stdio`), the CLI, and the Native Desktop Shell always discover the exact live port and DB path.
type DaemonInfo struct {
	URL       string    `json:"url"`
	PID       int       `json:"pid"`
	DBPath    string    `json:"db_path"`
	StartedAt time.Time `json:"started_at"`
}

// DefaultDaemonInfoPath resolves `~/.colabhive/daemon.json` (honoring `COLABHIVE_DAEMON_INFO_PATH` for tests).
func DefaultDaemonInfoPath() string {
	if custom := strings.TrimSpace(os.Getenv("COLABHIVE_DAEMON_INFO_PATH")); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.TempDir(), ".colabhive", "daemon.json")
	}
	return filepath.Join(home, ".colabhive", "daemon.json")
}

// WriteDaemonInfo writes `{url, pid, db_path, started_at}` atomically to `~/.colabhive/daemon.json`.
func WriteDaemonInfo(info DaemonInfo) error {
	return WriteDaemonInfoTo(DefaultDaemonInfoPath(), info)
}

// WriteDaemonInfoTo writes `DaemonInfo` atomically to an explicit file path without risking data loss.
func WriteDaemonInfoTo(path string, info DaemonInfo) error {
	info.URL = strings.TrimRight(strings.TrimSpace(info.URL), "/")
	if info.PID <= 0 {
		info.PID = os.Getpid()
	}
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now().UTC()
	}
	dbClean := strings.TrimSpace(info.DBPath)
	if dbClean != "" && dbClean != ":memory:" {
		if absDB, err := filepath.Abs(dbClean); err == nil {
			info.DBPath = absDB
		} else {
			info.DBPath = dbClean
		}
	} else {
		// Preserve any previously recorded production `db_path` when running `:memory:` demo sessions.
		if prev, err := ReadDaemonInfoFrom(path); err == nil && prev != nil && prev.DBPath != "" && prev.DBPath != ":memory:" {
			info.DBPath = prev.DBPath
		} else {
			info.DBPath = ""
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	tmp, err := os.CreateTemp(dir, ".daemon-info-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, wErr := tmp.Write(encoded); wErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return wErr
	}
	if cErr := tmp.Close(); cErr != nil {
		_ = os.Remove(tmpName)
		return cErr
	}
	_ = os.Chmod(tmpName, 0o600)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*15) * time.Millisecond)
		}
		if rErr := os.Rename(tmpName, path); rErr == nil {
			return nil
		} else {
			lastErr = rErr
		}
	}

	bakPath := fmt.Sprintf("%s.swap-%d", path, time.Now().UnixNano())
	if bErr := os.Rename(path, bakPath); bErr == nil {
		if rErr2 := os.Rename(tmpName, path); rErr2 == nil {
			_ = os.Remove(bakPath)
			return nil
		} else {
			_ = os.Rename(bakPath, path)
			_ = os.Remove(tmpName)
			return rErr2
		}
	}
	_ = os.Remove(tmpName)
	return lastErr
}

// ReadDaemonInfo reads the active or last-known `DaemonInfo` from `~/.colabhive/daemon.json`.
func ReadDaemonInfo() (*DaemonInfo, error) {
	return ReadDaemonInfoFrom(DefaultDaemonInfoPath())
}

// ReadDaemonInfoFrom reads `DaemonInfo` from an explicit file path.
func ReadDaemonInfoFrom(path string) (*DaemonInfo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info DaemonInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, err
	}
	info.URL = strings.TrimRight(strings.TrimSpace(info.URL), "/")
	info.DBPath = strings.TrimSpace(info.DBPath)
	return &info, nil
}

// DesktopActionRequest represents a command sent from the Magpie Tray Popover or Web UI to the Native Desktop Shell.
type DesktopActionRequest struct {
	Action  string `json:"action"`            // "open-main" | "open-route" | "hide-tray" | "show-tray" | "quit" | "toggle-autostart"
	Route   string `json:"route,omitempty"`   // Optional SPA route (e.g. "/cfg", "/router")
	Path    string `json:"path,omitempty"`    // Alias for Route
	Enabled *bool  `json:"enabled,omitempty"` // Optional explicit toggle value
}

// DesktopState describes the live runtime state of the Native Desktop Shell & System Tray host.
type DesktopState struct {
	DesktopShell      bool   `json:"desktop_shell"`
	AutoStartEnabled  bool   `json:"autostart_enabled"`
	MainWindowVisible bool   `json:"main_window_visible"`
	TrayVisible       bool   `json:"tray_visible"`
	ServerURL         string `json:"server_url,omitempty"`
	LastAction        string `json:"last_action,omitempty"`
}

// MarshalJSON emits both `"autostart_enabled"` and `"autostart"` for native bridge & HTTP compatibility.
func (d DesktopState) MarshalJSON() ([]byte, error) {
	type wireDesktopState struct {
		DesktopShell      bool   `json:"desktop_shell"`
		AutoStartEnabled  bool   `json:"autostart_enabled"`
		AutoStart         bool   `json:"autostart"`
		MainWindowVisible bool   `json:"main_window_visible"`
		TrayVisible       bool   `json:"tray_visible"`
		ServerURL         string `json:"server_url,omitempty"`
		LastAction        string `json:"last_action,omitempty"`
	}
	return json.Marshal(wireDesktopState{
		DesktopShell:      d.DesktopShell,
		AutoStartEnabled:  d.AutoStartEnabled,
		AutoStart:         d.AutoStartEnabled,
		MainWindowVisible: d.MainWindowVisible,
		TrayVisible:       d.TrayVisible,
		ServerURL:         d.ServerURL,
		LastAction:        d.LastAction,
	})
}

// UnmarshalJSON accepts either `"autostart_enabled"` or `"autostart"`.
func (d *DesktopState) UnmarshalJSON(raw []byte) error {
	var wire struct {
		DesktopShell      bool   `json:"desktop_shell"`
		AutoStartEnabled  *bool  `json:"autostart_enabled"`
		AutoStart         *bool  `json:"autostart"`
		MainWindowVisible bool   `json:"main_window_visible"`
		TrayVisible       bool   `json:"tray_visible"`
		ServerURL         string `json:"server_url"`
		LastAction        string `json:"last_action"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	d.DesktopShell = wire.DesktopShell
	if wire.AutoStartEnabled != nil {
		d.AutoStartEnabled = *wire.AutoStartEnabled
	} else if wire.AutoStart != nil {
		d.AutoStartEnabled = *wire.AutoStart
	}
	d.MainWindowVisible = wire.MainWindowVisible
	d.TrayVisible = wire.TrayVisible
	d.ServerURL = wire.ServerURL
	d.LastAction = wire.LastAction
	return nil
}

// DesktopBridgeFunc handles native window/tray/autostart actions from `/api/v1/desktop/action`
// and state queries (`req.Action == "state"`) from `/api/v1/desktop/state`.
type DesktopBridgeFunc func(ctx context.Context, req DesktopActionRequest) (DesktopState, error)

// Config holds HTTP server, security, static UI, and telemetry settings.
type Config struct {
	Version               string
	Mode                  string // "local" (default) | "cloud"
	TrustProxy            bool
	AdminToken            string
	WebDir                string
	ArtifactsDir          string
	PublicURL             string
	GoogleProbeURL        string
	DemoMode              bool
	KeepaliveIntervalSec  int
	ResourcePollSec       int
	PortalRateLimitPerMin int // Defaults to 60 req/min/IP
	AuthFailLimitPerMin   int // Defaults to 25 failed attempts/min/IP
}

type uploadChunkEntry struct {
	data      []byte
	updatedAt time.Time
}

type uploadChunkBuffer struct {
	mu         sync.Mutex
	entries    map[string]*uploadChunkEntry
	totalBytes int64
}

func (b *uploadChunkBuffer) pruneExpiredLocked(now time.Time) {
	cutoff := now.Add(-uploadChunkEntryTTL)
	for k, ent := range b.entries {
		if ent == nil || ent.updatedAt.Before(cutoff) {
			if ent != nil {
				b.totalBytes -= int64(len(ent.data))
			}
			delete(b.entries, k)
		}
	}
	if b.totalBytes < 0 {
		b.totalBytes = 0
	}
}

func (b *uploadChunkBuffer) handleChunk(bufKey string, chunk int, part []byte) (curLen int, completeData []byte, err error) {
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.entries == nil {
		b.entries = make(map[string]*uploadChunkEntry)
	}
	b.pruneExpiredLocked(now)

	existing := b.entries[bufKey]
	var prospectiveTotal int64
	if chunk == 1 {
		oldLen := int64(0)
		if existing != nil {
			oldLen = int64(len(existing.data))
		}
		prospectiveTotal = b.totalBytes - oldLen + int64(len(part))
	} else {
		prospectiveTotal = b.totalBytes + int64(len(part))
	}
	if prospectiveTotal > maxUploadChunkBufferBytes {
		return 0, nil, errUploadChunkLimitExceeded
	}

	if chunk == 1 || existing == nil {
		if existing != nil {
			b.totalBytes -= int64(len(existing.data))
		}
		cp := append([]byte(nil), part...)
		existing = &uploadChunkEntry{data: cp, updatedAt: now}
		b.entries[bufKey] = existing
		b.totalBytes += int64(len(cp))
	} else {
		existing.data = append(existing.data, part...)
		existing.updatedAt = now
		b.totalBytes += int64(len(part))
	}

	if chunk > 0 {
		return len(existing.data), nil, nil
	}

	// Final chunk (`chunk < 0`): extract completed buffer and release memory immediately.
	out := existing.data
	b.totalBytes -= int64(len(out))
	if b.totalBytes < 0 {
		b.totalBytes = 0
	}
	delete(b.entries, bufKey)
	return len(out), out, nil
}

type slidingWindowLimiter struct {
	mu          sync.Mutex
	window      time.Duration
	maxEvents   int
	hitsByKey   map[string][]time.Time
	lastCleanup time.Time
}

func newSlidingWindowLimiter(window time.Duration, maxEvents int) *slidingWindowLimiter {
	if window <= 0 {
		window = time.Minute
	}
	if maxEvents <= 0 {
		maxEvents = 60
	}
	return &slidingWindowLimiter{
		window:    window,
		maxEvents: maxEvents,
		hitsByKey: make(map[string][]time.Time),
	}
}

func (l *slidingWindowLimiter) sweepAllLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	for k, tsList := range l.hitsByKey {
		kept := tsList[:0]
		for _, ts := range tsList {
			if ts.After(cutoff) {
				kept = append(kept, ts)
			}
		}
		if len(kept) == 0 {
			delete(l.hitsByKey, k)
		} else {
			l.hitsByKey[k] = kept
		}
	}
	l.lastCleanup = now
}

func (l *slidingWindowLimiter) ensureCapacityForKeyLocked(now time.Time, key string) {
	if _, exists := l.hitsByKey[key]; exists || len(l.hitsByKey) < maxLimiterTrackedKeys {
		return
	}
	l.sweepAllLocked(now)
	for len(l.hitsByKey) >= maxLimiterTrackedKeys {
		var oldestKey string
		var oldestTS time.Time
		for k, tsList := range l.hitsByKey {
			if len(tsList) == 0 {
				oldestKey = k
				break
			}
			last := tsList[len(tsList)-1]
			if oldestKey == "" || last.Before(oldestTS) {
				oldestKey = k
				oldestTS = last
			}
		}
		if oldestKey == "" {
			break
		}
		delete(l.hitsByKey, oldestKey)
	}
}

func (l *slidingWindowLimiter) pruneLocked(now time.Time, key string) []time.Time {
	cutoff := now.Add(-l.window)
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= 2*l.window {
		l.sweepAllLocked(now)
	}
	cur := l.hitsByKey[key]
	if len(cur) == 0 {
		return nil
	}
	kept := cur[:0]
	for _, ts := range cur {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(l.hitsByKey, key)
		return nil
	}
	l.hitsByKey[key] = kept
	return kept
}

func (l *slidingWindowLimiter) allow(key string) bool {
	if l == nil {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.pruneLocked(now, key)
	if len(kept) >= l.maxEvents {
		return false
	}
	l.ensureCapacityForKeyLocked(now, key)
	l.hitsByKey[key] = append(kept, now)
	return true
}

func (l *slidingWindowLimiter) isExceeded(key string) bool {
	if l == nil {
		return false
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.pruneLocked(now, key)
	return len(kept) >= l.maxEvents
}

func (l *slidingWindowLimiter) record(key string) int {
	if l == nil {
		return 0
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.pruneLocked(now, key)
	l.ensureCapacityForKeyLocked(now, key)
	kept = append(kept, now)
	l.hitsByKey[key] = kept
	return len(kept)
}

type wsTicketEntry struct {
	expiresAt time.Time
	apiKey    *models.AgentAPIKey
}

// Server serves all REST `/api/v1/*`, `/api/public/contribute/*`, `/mcp`, `/ws/events`, and SPA routes.
type Server struct {
	st              models.Store
	pool            models.AccountPoolService
	runtime         models.RuntimeEngine
	router          models.RouterService
	hub             models.EventHub
	mcp             *mcp.Server
	txFactory       *proxy.TransportFactory
	cfg             Config
	startedAt       time.Time
	chunks          uploadChunkBuffer
	mux             *http.ServeMux
	portalLimiter   *slidingWindowLimiter
	authFailLimiter *slidingWindowLimiter

	artifactSignKey []byte

	wsTicketMu sync.Mutex
	wsTickets  map[string]wsTicketEntry

	expOwnerMu   sync.RWMutex
	expOwnerByID map[string]string

	desktopMu     sync.RWMutex
	desktopBridge DesktopBridgeFunc
}

type ctxKey string

const ctxKeyAPIKey ctxKey = "colabhive_api_key"

// SetDesktopBridge attaches a Native Desktop Shell & System Tray callback for `/api/v1/desktop/action` and `/api/v1/desktop/state`.
func (s *Server) SetDesktopBridge(fn DesktopBridgeFunc) {
	s.desktopMu.Lock()
	s.desktopBridge = fn
	s.desktopMu.Unlock()
}

// New creates a configured ColabHive HTTP/WS/MCP control-plane server.
func New(
	st models.Store,
	pool models.AccountPoolService,
	rt models.RuntimeEngine,
	rtr models.RouterService,
	hub models.EventHub,
	mcpSrv *mcp.Server,
	cfg Config,
) *Server {
	if cfg.Version == "" {
		cfg.Version = "1.0.0"
	}
	if cfg.KeepaliveIntervalSec <= 0 {
		cfg.KeepaliveIntervalSec = 8
	}
	if cfg.ResourcePollSec <= 0 {
		cfg.ResourcePollSec = 10
	}
	if cfg.ArtifactsDir == "" {
		cfg.ArtifactsDir = filepath.Join(os.TempDir(), "colabhive-artifacts")
	}
	if cfg.PortalRateLimitPerMin <= 0 {
		cfg.PortalRateLimitPerMin = 60
	}
	if cfg.AuthFailLimitPerMin <= 0 {
		cfg.AuthFailLimitPerMin = 25
	}
	if mcpSrv == nil {
		mcpSrv = mcp.NewServer(pool, rt, rtr, hub)
	}
	if cfg.AdminToken != "" {
		mcpSrv.SetAdminToken(cfg.AdminToken)
	}
	isCloud := strings.EqualFold(strings.TrimSpace(cfg.Mode), "cloud")
	mcpSrv.SetCloudMode(isCloud)

	if pool != nil && rt != nil {
		if ensurer, ok := any(pool).(interface {
			EnsureSessionDrainer(func(context.Context, string) error)
		}); ok {
			if stopper, okS := any(rt).(interface {
				StopManagedSessionsForAccount(context.Context, string) error
			}); okS {
				ensurer.EnsureSessionDrainer(stopper.StopManagedSessionsForAccount)
			} else {
				ensurer.EnsureSessionDrainer(func(dCtx context.Context, accountID string) error {
					sessions, lErr := rt.ListSessions(dCtx, models.SessionFilter{AccountID: accountID, ActiveOnly: true})
					if lErr != nil {
						return lErr
					}
					var firstErr error
					for _, sess := range sessions {
						if sess != nil && !sess.AdoptedExternal {
							if sErr := rt.StopSession(dCtx, sess.ID); sErr != nil && firstErr == nil {
								firstErr = sErr
							}
						}
					}
					return firstErr
				})
			}
		}
	}

	signSeed := make([]byte, 32)
	_, _ = rand.Read(signSeed)
	macInit := hmac.New(sha256.New, signSeed)
	_, _ = macInit.Write([]byte(cfg.AdminToken))
	artifactKey := macInit.Sum(nil)

	s := &Server{
		st:              st,
		pool:            pool,
		runtime:         rt,
		router:          rtr,
		hub:             hub,
		mcp:             mcpSrv,
		txFactory:       proxy.NewTransportFactory(),
		cfg:             cfg,
		startedAt:       time.Now().UTC(),
		chunks:          uploadChunkBuffer{entries: make(map[string]*uploadChunkEntry)},
		mux:             http.NewServeMux(),
		portalLimiter:   newSlidingWindowLimiter(time.Minute, cfg.PortalRateLimitPerMin),
		authFailLimiter: newSlidingWindowLimiter(time.Minute, cfg.AuthFailLimitPerMin),
		artifactSignKey: artifactKey,
		wsTickets:       make(map[string]wsTicketEntry),
		expOwnerByID:    make(map[string]string),
	}
	s.registerRoutes()
	return s
}

// Handler returns the root http.Handler wrapped with CORS and Bearer/API-Key authentication.
func (s *Server) Handler() http.Handler {
	return s.withCORS(s.withAuth(s.mux))
}

func (s *Server) registerRoutes() {
	// 1. System Health, Diagnostic Self-Test, Hardware Catalog & Short-Lived WS Ticket
	s.mux.HandleFunc("GET /api/v1/health", s.handleGetHealth)
	s.mux.HandleFunc("GET /api/v1/hardware", s.handleGetHardware)
	s.mux.HandleFunc("POST /api/v1/system/selftest", s.handleSystemSelfTest)
	s.mux.HandleFunc("POST /api/v1/ws-ticket", s.handleCreateWSTicket)

	// 2. Observatory & Account Pool (+ Python SDK aliases)
	s.mux.HandleFunc("GET /api/v1/pool/summary", s.handleGetQuotaSummary)
	s.mux.HandleFunc("GET /api/v1/pool/quota", s.handleGetQuotaSummary)
	s.mux.HandleFunc("GET /api/v1/quota", s.handleGetQuotaSummary)
	s.mux.HandleFunc("GET /api/v1/accounts", s.handleListAccounts)
	s.mux.HandleFunc("GET /api/v1/accounts/{id}", s.handleGetAccount)
	s.mux.HandleFunc("PATCH /api/v1/accounts/{id}", s.handleUpdateAccount)
	s.mux.HandleFunc("DELETE /api/v1/accounts/{id}", s.handleDeleteAccount)
	s.mux.HandleFunc("POST /api/v1/accounts/{id}/refresh", s.handleRefreshAccountQuota)
	s.mux.HandleFunc("POST /api/v1/accounts/{id}/verify", s.handleVerifyAccount)

	// 3. Owner OAuth2 PKCE Wizard
	s.mux.HandleFunc("POST /api/v1/accounts/oauth/start", s.handleStartOAuth)
	s.mux.HandleFunc("POST /api/v1/accounts/oauth/exchange", s.handleCompleteOAuth)

	// 4. Proxy Pool Management
	s.mux.HandleFunc("GET /api/v1/proxies", s.handleListProxies)
	s.mux.HandleFunc("POST /api/v1/proxies", s.handleSaveProxy)
	s.mux.HandleFunc("DELETE /api/v1/proxies/{id}", s.handleDeleteProxy)
	s.mux.HandleFunc("POST /api/v1/proxies/test", s.handleTestProxy)

	// 5. Credential Portability (`keyringrc.cfg`, `gcloud` ADC, JSON, `.chvault`)
	s.mux.HandleFunc("GET /api/v1/import/scan-local", s.handleScanLocalCredentials)
	s.mux.HandleFunc("POST /api/v1/pool/import", s.handleImportCredentials)
	s.mux.HandleFunc("POST /api/v1/pool/export", s.handleExportVaultBundle)

	// 6. Contributor Invites (Admin)
	s.mux.HandleFunc("GET /api/v1/contribute/invites", s.handleListInvites)
	s.mux.HandleFunc("POST /api/v1/contribute/invites", s.handleCreateInvite)
	s.mux.HandleFunc("DELETE /api/v1/contribute/invites/{code}", s.handleRevokeInvite)

	// 7. Public Guest Contributor Portal (`/api/public/contribute/*` + `/api/v1/contribute/portal/*` aliases)
	s.mux.HandleFunc("GET /api/public/contribute/{code}", s.handleGetPublicInvite)
	s.mux.HandleFunc("GET /api/v1/contribute/portal/{code}", s.handleGetPublicInvite)
	s.mux.HandleFunc("POST /api/public/contribute/{code}/start", s.handleStartContribution)
	s.mux.HandleFunc("POST /api/v1/contribute/portal/{code}/start", s.handleStartContribution)
	s.mux.HandleFunc("POST /api/public/contribute/{code}/complete", s.handleCompleteContribution)
	s.mux.HandleFunc("POST /api/public/contribute/{code}/import", s.handleCompleteContribution)
	s.mux.HandleFunc("POST /api/v1/contribute/portal/{code}/complete", s.handleCompleteContribution)

	s.mux.HandleFunc("GET /api/public/contribute/manage/{receiptToken}", s.handleGetContributionStatus)
	s.mux.HandleFunc("GET /api/v1/contribute/manage/{receiptToken}", s.handleGetContributionStatus)
	s.mux.HandleFunc("PATCH /api/public/contribute/manage/{receiptToken}", s.handleUpdateContributionGuardrails)
	s.mux.HandleFunc("PATCH /api/v1/contribute/manage/{receiptToken}", s.handleUpdateContributionGuardrails)
	s.mux.HandleFunc("POST /api/public/contribute/manage/{receiptToken}/revoke", s.handleRevokeContribution)
	s.mux.HandleFunc("POST /api/v1/contribute/manage/{receiptToken}/revoke", s.handleRevokeContribution)

	// 8. Runtime Sessions, Execution & Virtual Notebook Cells
	s.mux.HandleFunc("GET /api/v1/sessions", s.handleListSessions)
	s.mux.HandleFunc("POST /api/v1/sessions", s.handleCreateSession)
	s.mux.HandleFunc("POST /api/v1/sessions/sync", s.handleSyncSessions)
	s.mux.HandleFunc("POST /api/v1/exec", s.handleExecuteStickyCode)
	s.mux.HandleFunc("POST /api/v1/sessions/exec", s.handleExecuteStickyCode)
	s.mux.HandleFunc("GET /api/v1/sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{id}", s.handleStopSession)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/restart-kernel", s.handleRestartKernel)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/interrupt", s.handleInterruptKernel)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/hot-restart", s.handleHotRestartWorker)
	s.mux.HandleFunc("GET /api/v1/sessions/{id}/open-url", s.handleGetSessionWebURL)
	s.mux.HandleFunc("GET /api/v1/sessions/{id}/url", s.handleGetSessionWebURL)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/exec", s.handleExecuteSessionCode)
	s.mux.HandleFunc("GET /api/v1/sessions/{id}/cells", s.handleGetCells)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/cells", s.handleMutateCell)

	// 9. Remote File System, Package Installer & RichOutput Artifacts
	s.mux.HandleFunc("GET /api/v1/sessions/{id}/files", s.handleGetFilesOrDownload)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/files", s.handleUploadFile)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/files/upload", s.handleUploadFile)
	s.mux.HandleFunc("GET /api/v1/sessions/{id}/files/download", s.handleDownloadFile)
	s.mux.HandleFunc("DELETE /api/v1/sessions/{id}/files", s.handleDeleteFile)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/install", s.handleInstallPackages)
	s.mux.HandleFunc("POST /api/v1/sessions/{id}/packages", s.handleInstallPackages)
	s.mux.HandleFunc("GET /api/v1/artifacts/{sessionID}/{filename}", s.handleServeArtifact)

	// 10. Parallel Sharded Experiments
	s.mux.HandleFunc("GET /api/v1/experiments", s.handleListExperiments)
	s.mux.HandleFunc("POST /api/v1/experiments", s.handleLaunchExperiment)
	s.mux.HandleFunc("POST /api/v1/experiments/sharded", s.handleLaunchExperiment)
	s.mux.HandleFunc("GET /api/v1/experiments/{id}", s.handleGetExperiment)
	s.mux.HandleFunc("POST /api/v1/experiments/{id}/teardown", s.handleTeardownExperiment)
	s.mux.HandleFunc("POST /api/v1/experiments/{id}/collect", s.handleTeardownExperiment)

	// 11. Agent Router, Sticky Bindings, API Keys & `cc-switch` Config Injector
	s.mux.HandleFunc("GET /api/v1/router/policy", s.handleGetRouterPolicy)
	s.mux.HandleFunc("PUT /api/v1/router/policy", s.handleUpdateRouterPolicy)
	s.mux.HandleFunc("GET /api/v1/router/sticky", s.handleListStickyBindings)
	s.mux.HandleFunc("DELETE /api/v1/router/sticky/{stickyKey}", s.handleUnbindStickySession)
	s.mux.HandleFunc("GET /api/v1/router/traces", s.handleListTraces)

	s.mux.HandleFunc("GET /api/v1/keys", s.handleListAPIKeys)
	s.mux.HandleFunc("POST /api/v1/keys", s.handleCreateAPIKey)
	s.mux.HandleFunc("DELETE /api/v1/keys/{id}", s.handleRevokeAPIKey)

	s.mux.HandleFunc("GET /api/v1/agents/clients", s.handleListAgentClients)
	s.mux.HandleFunc("POST /api/v1/agents/inject", s.handleInjectAgentConfig)
	s.mux.HandleFunc("DELETE /api/v1/agents/inject/{clientId}", s.handleRemoveAgentConfig)

	// 11b. Native Desktop Shell & System Tray Bridge (`/api/v1/desktop/*`)
	s.mux.HandleFunc("GET /api/v1/desktop/state", s.handleGetDesktopState)
	s.mux.HandleFunc("POST /api/v1/desktop/action", s.handleDesktopAction)

	// 12. Streamable HTTP + SSE MCP Endpoints (`/mcp`, `/mcp/sse`, `/mcp/messages`)
	mcpHandler := s.mcp.Handler()
	s.mux.Handle("/mcp", mcpHandler)
	s.mux.Handle("/mcp/", mcpHandler)

	// 13. Real-Time Telemetry WebSocket (`/ws/events`)
	s.mux.HandleFunc("GET /ws/events", s.handleWSEvents)

	// 14. Static Web UI (`--web-dir`) & Client-Side SPA Fallback
	s.mux.HandleFunc("/", s.handleSPAOrNotFound)
}

// ============================================================================
// CORS, Origin Guard, Signed Artifacts, WS Tickets & Authentication
// ============================================================================

func isAllowedLoopbackHost(rawHost string) bool {
	h := strings.TrimSpace(rawHost)
	if h == "" {
		return true
	}
	if parsedHost, _, err := net.SplitHostPort(h); err == nil {
		h = parsedHost
	} else if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = strings.Trim(h, "[]")
	} else if strings.Count(h, ":") == 1 {
		return false
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func isAllowedLoopbackOrigin(origin string) bool {
	trimmed := strings.TrimSpace(origin)
	if trimmed == "" {
		return true
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if !s.isAuthRequired() {
			if !isAllowedLoopbackHost(r.Host) {
				writeJSONError(w, http.StatusForbidden, "forbidden: non-loopback Host header blocked in keyless local mode")
				return
			}
			if origin != "" && !isAllowedLoopbackOrigin(origin) {
				writeJSONError(w, http.StatusForbidden, "forbidden: cross-origin request blocked in keyless local mode")
				return
			}
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set(
			"Access-Control-Allow-Headers",
			"Authorization, Content-Type, Accept, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID, X-ColabHive-Key, X-ColabHive-Session-Id, X-ColabHive-Strategy, X-ColabHive-Account",
		)
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, MCP-Protocol-Version")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPublicPortalRateLimitedPath(p string) bool {
	return strings.HasPrefix(p, "/api/public/contribute/") ||
		p == "/api/public/contribute" ||
		strings.HasPrefix(p, "/api/v1/contribute/portal/") ||
		strings.HasPrefix(p, "/api/v1/contribute/manage/")
}

func isProtectedControlPlanePath(p string) bool {
	return strings.HasPrefix(p, "/api/") ||
		p == "/ws/events" ||
		p == "/mcp" ||
		strings.HasPrefix(p, "/mcp/")
}

func isPublicUnauthenticatedPath(p string) bool {
	if !isProtectedControlPlanePath(p) {
		return true
	}
	switch {
	case p == "/api/v1/health",
		p == "/api/v1/hardware",
		strings.HasPrefix(p, "/api/public/"),
		strings.HasPrefix(p, "/api/v1/contribute/portal/"),
		strings.HasPrefix(p, "/api/v1/contribute/manage/"):
		return true
	default:
		return false
	}
}

func extractSafeArtifactSegments(r *http.Request) (sessID string, fileName string, isArtifactRoute bool, valid bool) {
	if r == nil || r.URL == nil {
		return "", "", false, false
	}
	escPath := r.URL.EscapedPath()
	rawPath := r.URL.Path
	const prefix = "/api/v1/artifacts/"
	if !strings.HasPrefix(escPath, prefix) && !strings.HasPrefix(rawPath, prefix) && rawPath != "/api/v1/artifacts" {
		return "", "", false, false
	}
	if !strings.HasPrefix(escPath, prefix) {
		return "", "", true, false
	}
	rem := strings.TrimPrefix(escPath, prefix)
	parts := strings.Split(rem, "/")
	if len(parts) != 2 {
		return "", "", true, false
	}
	sSeg, err1 := url.PathUnescape(parts[0])
	fSeg, err2 := url.PathUnescape(parts[1])
	if err1 != nil || err2 != nil {
		return "", "", true, false
	}
	if !isSafePathSegment(sSeg) || !isSafePathSegment(fSeg) {
		return "", "", true, false
	}
	return sSeg, fSeg, true, true
}

func (s *Server) computeArtifactHMAC(sessID, fileName, expStr string) []byte {
	mac := hmac.New(sha256.New, s.artifactSignKey)
	_, _ = mac.Write([]byte(sessID))
	_, _ = mac.Write([]byte("/"))
	_, _ = mac.Write([]byte(fileName))
	_, _ = mac.Write([]byte(":"))
	_, _ = mac.Write([]byte(expStr))
	return mac.Sum(nil)
}

func (s *Server) signArtifactURL(rawURL string) string {
	if !s.isAuthRequired() || strings.TrimSpace(rawURL) == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	const prefix = "/api/v1/artifacts/"
	if !strings.HasPrefix(u.Path, prefix) {
		return rawURL
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
	if len(parts) != 2 || !isSafePathSegment(parts[0]) || !isSafePathSegment(parts[1]) {
		return rawURL
	}
	q := u.Query()
	if q.Get("sig") != "" {
		return rawURL
	}
	expUnix := strconv.FormatInt(time.Now().UTC().Add(artifactSignatureTTL).Unix(), 10)
	sigHex := hex.EncodeToString(s.computeArtifactHMAC(parts[0], parts[1], expUnix))
	q.Set("exp", expUnix)
	q.Set("sig", sigHex)
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) verifyArtifactSignature(sessID, fileName string, q url.Values) bool {
	if !isSafePathSegment(sessID) || !isSafePathSegment(fileName) {
		return false
	}
	expStr := strings.TrimSpace(q.Get("exp"))
	sigStr := strings.TrimSpace(q.Get("sig"))
	if expStr == "" || sigStr == "" {
		return false
	}
	expUnix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().UTC().Unix() > expUnix {
		return false
	}
	providedSig, err := hex.DecodeString(sigStr)
	if err != nil {
		return false
	}
	expectedSig := s.computeArtifactHMAC(sessID, fileName, expStr)
	return hmac.Equal(providedSig, expectedSig)
}

// isAgentKeyAllowedRoute enforces strict least-privilege separation for scoped `ch_sk_...` keys.
// Scoped agent keys may ONLY invoke MCP, read quota/health/hardware, mint WS tickets, execute code, and manage
// their own runtime sessions/experiments/artifacts — NEVER admin/account/vault/proxy/key/policy/injector/desktop routes.
func isAgentKeyAllowedRoute(p string) bool {
	clean := strings.TrimRight(p, "/")
	if clean == "" {
		clean = "/"
	}
	switch {
	case clean == "/mcp" || strings.HasPrefix(clean, "/mcp/"):
		return true
	case clean == "/api/v1/health",
		clean == "/api/v1/hardware",
		clean == "/api/v1/ws-ticket",
		clean == "/ws/events",
		clean == "/api/v1/pool/summary",
		clean == "/api/v1/pool/quota",
		clean == "/api/v1/quota",
		clean == "/api/v1/exec":
		return true
	case clean == "/api/v1/sessions/sync":
		return false
	case clean == "/api/v1/sessions" || strings.HasPrefix(clean, "/api/v1/sessions/"):
		return true
	case clean == "/api/v1/experiments" || strings.HasPrefix(clean, "/api/v1/experiments/"):
		return true
	case strings.HasPrefix(clean, "/api/v1/artifacts/"):
		return true
	default:
		return false
	}
}

func constantTimeTokenEqual(provided, expected string) bool {
	p := strings.TrimSpace(provided)
	e := strings.TrimSpace(expected)
	if p == "" || e == "" {
		return false
	}
	ph := sha256.Sum256([]byte(p))
	eh := sha256.Sum256([]byte(e))
	return subtle.ConstantTimeCompare(ph[:], eh[:]) == 1
}

func extractRequestToken(r *http.Request) string {
	raw := ""
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			raw = strings.TrimSpace(auth[7:])
		} else {
			raw = auth
		}
	} else if xk := strings.TrimSpace(r.Header.Get("X-ColabHive-Key")); xk != "" {
		raw = xk
	} else if qTok := strings.TrimSpace(r.URL.Query().Get("token")); qTok != "" {
		raw = qTok
	} else if qKey := strings.TrimSpace(r.URL.Query().Get("key")); qKey != "" {
		raw = qKey
	} else if r.URL.Path != "/api/v1/agents/clients" {
		// On `/api/v1/agents/clients`, `?api_key=` is the target snippet preview parameter, not caller auth.
		if qAPIKey := strings.TrimSpace(r.URL.Query().Get("api_key")); qAPIKey != "" {
			raw = qAPIKey
		}
	}
	return strings.TrimSpace(raw)
}

func (s *Server) isAuthRequired() bool {
	return s.isCloudMode() || strings.TrimSpace(s.cfg.AdminToken) != ""
}

func parseValidIPHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil && host != "" {
		s = host
	}
	s = strings.Trim(s, "[]")
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func isTrustedProxyPeer(rawAddr string) bool {
	ipStr := parseValidIPHost(rawAddr)
	if ipStr == "" {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func (s *Server) clientIP(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	remoteHost := parseValidIPHost(r.RemoteAddr)
	if remoteHost == "" {
		rawRemote := strings.TrimSpace(r.RemoteAddr)
		if host, _, err := net.SplitHostPort(rawRemote); err == nil && host != "" {
			remoteHost = host
		} else if rawRemote != "" {
			remoteHost = rawRemote
		} else {
			remoteHost = "unknown"
		}
	}

	// Only honor reverse-proxy forwarding headers when `TrustProxy` is enabled AND the direct TCP peer
	// is a loopback, RFC1918/RFC4193 private, or link-local reverse-proxy address!
	if s.cfg.TrustProxy && isTrustedProxyPeer(r.RemoteAddr) {
		xri := parseValidIPHost(r.Header.Get("X-Real-IP"))
		if xri != "" && !isTrustedProxyPeer(xri) {
			return xri
		}
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			parts := strings.Split(xff, ",")
			rightMostValid := ""
			// Walk from right to left so attacker-prepended `X-Forwarded-For` hops are ignored.
			for i := len(parts) - 1; i >= 0; i-- {
				cand := parseValidIPHost(parts[i])
				if cand == "" {
					continue
				}
				if rightMostValid == "" {
					rightMostValid = cand
				}
				if !isTrustedProxyPeer(cand) {
					return cand
				}
			}
			if xri != "" {
				return xri
			}
			if rightMostValid != "" {
				return rightMostValid
			}
		}
		if xri != "" {
			return xri
		}
	}

	return remoteHost
}

func (s *Server) pruneWSTicketsLocked(now time.Time) {
	for tok, ent := range s.wsTickets {
		if now.After(ent.expiresAt) {
			delete(s.wsTickets, tok)
		}
	}
	for len(s.wsTickets) >= maxTrackedWSTickets {
		var oldestTok string
		var oldestExp time.Time
		for tok, ent := range s.wsTickets {
			if oldestTok == "" || ent.expiresAt.Before(oldestExp) {
				oldestTok = tok
				oldestExp = ent.expiresAt
			}
		}
		if oldestTok == "" {
			break
		}
		delete(s.wsTickets, oldestTok)
	}
}

func (s *Server) issueWSTicket(apiKey *models.AgentAPIKey) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	ticket := "wst_" + hex.EncodeToString(buf)
	now := time.Now().UTC()

	s.wsTicketMu.Lock()
	s.pruneWSTicketsLocked(now)
	s.wsTickets[ticket] = wsTicketEntry{
		expiresAt: now.Add(wsTicketTTL),
		apiKey:    apiKey,
	}
	s.wsTicketMu.Unlock()
	return ticket, nil
}

func (s *Server) consumeWSTicket(rawTicket string) (wsTicketEntry, bool) {
	tok := strings.TrimSpace(rawTicket)
	if tok == "" {
		return wsTicketEntry{}, false
	}
	now := time.Now().UTC()
	s.wsTicketMu.Lock()
	defer s.wsTicketMu.Unlock()
	s.pruneWSTicketsLocked(now)
	ent, ok := s.wsTickets[tok]
	if !ok {
		return wsTicketEntry{}, false
	}
	// Strictly single-use: delete immediately upon lookup.
	delete(s.wsTickets, tok)
	if now.After(ent.expiresAt) {
		return wsTicketEntry{}, false
	}
	return ent, true
}

func (s *Server) handleCreateWSTicket(w http.ResponseWriter, r *http.Request) {
	ticket, err := s.issueWSTicket(s.requestAPIKey(r))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to issue WebSocket ticket")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket":         ticket,
		"expires_in_sec": int(wsTicketTTL.Seconds()),
	})
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)

		// Pre-mux path traversal defense on `/api/v1/artifacts/*` BEFORE Go's ServeMux can clean/redirect `..`
		// or serve unauthenticated files!
		artSessID, artFileName, isArtifactRoute, isValidArtifact := extractSafeArtifactSegments(r)
		if isArtifactRoute && !isValidArtifact {
			writeJSONError(w, http.StatusBadRequest, "invalid artifact path segment")
			return
		}

		if isPublicPortalRateLimitedPath(r.URL.Path) {
			if !s.portalLimiter.allow(ip) {
				w.Header().Set("Retry-After", "60")
				writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded: too many contributor portal requests, please retry shortly")
				return
			}
		}

		if isPublicUnauthenticatedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Allow `/api/v1/artifacts/{sessionID}/{filename}` unauthenticated ONLY in keyless local mode
		// OR when carrying a valid unexpired HMAC-SHA256 query signature (`?exp=...&sig=...`).
		if isArtifactRoute && isValidArtifact {
			if !s.isAuthRequired() && extractRequestToken(r) == "" {
				next.ServeHTTP(w, r)
				return
			}
			if s.verifyArtifactSignature(artSessID, artFileName, r.URL.Query()) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Single-use short-lived WebSocket ticket (`/ws/events?ticket=wst_...`)
		if r.URL.Path == "/ws/events" {
			if qTicket := strings.TrimSpace(r.URL.Query().Get("ticket")); qTicket != "" {
				if ent, ok := s.consumeWSTicket(qTicket); ok {
					if ent.apiKey != nil {
						r = r.WithContext(context.WithValue(r.Context(), ctxKeyAPIKey, ent.apiKey))
					}
					next.ServeHTTP(w, r)
					return
				}
				if s.authFailLimiter.isExceeded(ip) {
					w.Header().Set("Retry-After", "60")
					writeJSONError(w, http.StatusTooManyRequests, "too many failed authentication attempts; temporarily locked out")
					return
				}
				s.authFailLimiter.record(ip)
				writeJSONError(w, http.StatusUnauthorized, "unauthorized: invalid, expired, or already-used WebSocket ticket")
				return
			}
		}

		tok := extractRequestToken(r)
		adminTok := strings.TrimSpace(s.cfg.AdminToken)

		// 1. Verify valid AdminToken (constant time, zero DB cost) BEFORE `authFailLimiter` rejects,
		//    so legitimate admins are never locked out by NAT peers or proxy neighbours!
		if adminTok != "" && constantTimeTokenEqual(tok, adminTok) {
			next.ServeHTTP(w, r)
			return
		}

		if s.authFailLimiter.isExceeded(ip) {
			w.Header().Set("Retry-After", "60")
			writeJSONError(w, http.StatusTooManyRequests, "too many failed authentication attempts; temporarily locked out")
			return
		}

		// 2. Scoped `ch_sk_...` Agent API keys are strictly confined to runtime/MCP/read-quota routes.
		if strings.HasPrefix(tok, "ch_sk_") {
			if s.router == nil {
				s.authFailLimiter.record(ip)
				writeJSONError(w, http.StatusUnauthorized, "unauthorized: API key authentication unavailable")
				return
			}
			keyObj, err := s.router.AuthenticateAPIKey(r.Context(), tok)
			if err != nil {
				s.authFailLimiter.record(ip)
				writeJSONError(w, http.StatusUnauthorized, err.Error())
				return
			}
			if !isAgentKeyAllowedRoute(r.URL.Path) {
				writeJSONError(w, http.StatusForbidden, "forbidden: admin token required")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyAPIKey, keyObj)
			ctx = mcp.WithAuthenticatedAPIKey(ctx, keyObj)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// 3. Reject unrecognized non-empty tokens even in local mode, and require auth in cloud/protected mode.
		if tok != "" || s.isAuthRequired() {
			s.authFailLimiter.record(ip)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized: valid admin token or ch_sk_... API key required")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) wsEventAllowedForKey(ctx context.Context, ev models.WSEvent, key *models.AgentAPIKey) bool {
	if key == nil {
		return true
	}
	switch ev.Type {
	case models.WSEventHeartbeat:
		return true
	case models.WSEventSessionUpdated:
		if sess, ok := ev.Payload.(*models.RuntimeSession); ok && sess != nil {
			return !sess.AdoptedExternal && sess.APIKeyID == key.ID
		}
		return false
	case models.WSEventSessionOutput:
		var sid string
		switch ch := ev.Payload.(type) {
		case models.SessionStreamChunk:
			sid = ch.SessionID
		case *models.SessionStreamChunk:
			if ch != nil {
				sid = ch.SessionID
			}
		}
		if sid != "" && s.runtime != nil {
			if sess, err := s.runtime.GetSession(ctx, sid); err == nil && sess != nil {
				return sess.ID == sid && !sess.AdoptedExternal && sess.APIKeyID == key.ID
			}
		}
		return false
	case models.WSEventExperimentUpdated:
		if exp, ok := ev.Payload.(*models.Experiment); ok && exp != nil {
			return exp.APIKeyID == key.ID
		}
		return false
	default:
		return false
	}
}

func (s *Server) handleWSEvents(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 16384,
		CheckOrigin: func(req *http.Request) bool {
			if !s.isAuthRequired() {
				return isAllowedLoopbackHost(req.Host) && isAllowedLoopbackOrigin(req.Header.Get("Origin"))
			}
			return true
		},
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	callerKey := s.requestAPIKey(r)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Drain client ping/close frames in background so Gorilla WebSocket processes control frames.
	go func() {
		for {
			if _, _, rErr := conn.ReadMessage(); rErr != nil {
				cancel()
				return
			}
		}
	}()

	var writeMu sync.Mutex
	sendEvent := func(ev models.WSEvent) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(ev)
	}

	// Emit immediate initial heartbeat so UI measures RTT right on connect.
	_ = sendEvent(models.WSEvent{
		Type:      models.WSEventHeartbeat,
		Timestamp: time.Now().UTC(),
		Payload:   map[string]any{"status": "connected", "version": s.cfg.Version},
	})

	var subCh <-chan models.WSEvent
	var unsub func()
	if s.hub != nil {
		subCh, unsub = s.hub.Subscribe(ctx)
		defer unsub()
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-subCh:
			if !ok {
				return
			}
			if !s.wsEventAllowedForKey(ctx, ev, callerKey) {
				continue
			}
			if callerKey != nil {
				switch p := ev.Payload.(type) {
				case *models.RuntimeSession:
					ev.Payload = redactSessionForScopedCaller(p)
				case *models.Experiment:
					ev.Payload = redactExperimentForScopedCaller(p)
				case models.SessionStreamChunk:
					p.Text = redactEmailsInServerText(p.Text, "")
					if p.RichOutput != nil {
						roCopy := *p.RichOutput
						roCopy.TextData = redactEmailsInServerText(roCopy.TextData, "")
						roCopy.Title = redactEmailsInServerText(roCopy.Title, "")
						if s.isAuthRequired() {
							ro := s.signRichOutputs([]models.KernelRichOutput{roCopy})
							if len(ro) > 0 {
								roCopy = ro[0]
							}
						}
						p.RichOutput = &roCopy
					}
					ev.Payload = p
				case *models.SessionStreamChunk:
					if p != nil {
						cp := *p
						cp.Text = redactEmailsInServerText(cp.Text, "")
						if cp.RichOutput != nil {
							roCopy := *cp.RichOutput
							roCopy.TextData = redactEmailsInServerText(roCopy.TextData, "")
							roCopy.Title = redactEmailsInServerText(roCopy.Title, "")
							if s.isAuthRequired() {
								ro := s.signRichOutputs([]models.KernelRichOutput{roCopy})
								if len(ro) > 0 {
									roCopy = ro[0]
								}
							}
							cp.RichOutput = &roCopy
						}
						ev.Payload = &cp
					}
				}
			}
			if ev.Timestamp.IsZero() {
				ev.Timestamp = time.Now().UTC()
			}
			if err := sendEvent(ev); err != nil {
				return
			}
		case <-ticker.C:
			if err := sendEvent(models.WSEvent{
				Type:      models.WSEventHeartbeat,
				Timestamp: time.Now().UTC(),
				Payload:   map[string]any{"status": "ok"},
			}); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleSPAOrNotFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, http.StatusNotFound, "API endpoint not found: "+r.URL.Path)
		return
	}

	webDir := strings.TrimSpace(s.cfg.WebDir)
	if webDir == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":    "ColabHive Control Plane",
			"version": s.cfg.Version,
			"health":  "/api/v1/health",
			"mcp":     "/mcp",
		})
		return
	}

	cleanRel := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if cleanRel != "." && cleanRel != "" && !strings.Contains(cleanRel, "..") {
		candidate := filepath.Join(webDir, cleanRel)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			http.ServeFile(w, r, candidate)
			return
		}
	}

	// SPA Fallback for `/`, `/pool`, `/exp`, `/exp/:id`, `/router`, `/cfg`, `/contribute/*`, `/c/*`
	indexPath := filepath.Join(webDir, "index.html")
	if _, err := os.Stat(indexPath); err == nil {
		http.ServeFile(w, r, indexPath)
		return
	}

	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error":   msg,
		"message": msg,
		"status":  status,
	})
}
