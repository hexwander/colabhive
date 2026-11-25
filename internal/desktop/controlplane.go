package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/colabhive/colabhive/internal/events"
	"github.com/colabhive/colabhive/internal/mcp"
	"github.com/colabhive/colabhive/internal/mockcolab"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/proxy"
	"github.com/colabhive/colabhive/internal/router"
	"github.com/colabhive/colabhive/internal/runtime"
	"github.com/colabhive/colabhive/internal/server"
	"github.com/colabhive/colabhive/internal/store"
	"github.com/colabhive/colabhive/internal/vault"
)

// ResolvedPaths holds the resolved filesystem locations for SQLite data and SPA static assets.
type ResolvedPaths struct {
	DataDir string
	DBPath  string
	WebDir  string
}

// ResolveRuntimePaths smartly discovers `data/colabhive_prod.db` (or `colabhive.db`) and `web/dist`
// across current working directory, executable directory (`bin/`), parent repo root (`<exeDir>/..`),
// cross-matrix build directory (`<exeDir>/../..`), and `~/.colabhive/daemon.json`'s `db_path`.
func ResolveRuntimePaths(cfg Config, exePath string, cwd string, daemonInfo *server.DaemonInfo) ResolvedPaths {
	var roots []string
	addRoot := func(dir string) {
		trimmed := strings.TrimSpace(dir)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if abs, err := filepath.Abs(cleaned); err == nil {
			cleaned = abs
		}
		for _, existing := range roots {
			if strings.EqualFold(existing, cleaned) {
				return
			}
		}
		roots = append(roots, cleaned)
	}

	if cwd != "" {
		addRoot(cwd)
	}
	if exePath != "" {
		exeDir := filepath.Dir(exePath)
		addRoot(exeDir)
		addRoot(filepath.Join(exeDir, ".."))
		addRoot(filepath.Join(exeDir, "..", ".."))
	}
	if daemonInfo != nil && daemonInfo.DBPath != "" && daemonInfo.DBPath != ":memory:" {
		dbDir := filepath.Dir(daemonInfo.DBPath)
		addRoot(filepath.Join(dbDir, ".."))
		addRoot(dbDir)
	}

	var out ResolvedPaths

	// 1. Resolve DBPath & DataDir
	explicitDB := strings.TrimSpace(cfg.DBPath)
	explicitDataDir := strings.TrimSpace(cfg.DataDir)

	switch {
	case explicitDB != "":
		out.DBPath = explicitDB
		if explicitDB != ":memory:" {
			if abs, err := filepath.Abs(explicitDB); err == nil {
				out.DBPath = abs
			}
			if explicitDataDir != "" {
				if absDir, err := filepath.Abs(explicitDataDir); err == nil {
					out.DataDir = absDir
				} else {
					out.DataDir = explicitDataDir
				}
			} else {
				out.DataDir = filepath.Dir(out.DBPath)
			}
		}
	case cfg.Demo:
		out.DBPath = ":memory:"
		if explicitDataDir != "" {
			out.DataDir = explicitDataDir
		} else {
			out.DataDir = filepath.Join(os.TempDir(), "colabhive-desktop-demo")
		}
	case explicitDataDir != "":
		dataDir := explicitDataDir
		if abs, err := filepath.Abs(dataDir); err == nil {
			dataDir = abs
		}
		out.DataDir = dataDir
		prodCand := filepath.Join(dataDir, "colabhive_prod.db")
		stdCand := filepath.Join(dataDir, "colabhive.db")
		if isRegularFile(prodCand) {
			out.DBPath = prodCand
		} else {
			out.DBPath = stdCand
		}
	default:
		// Prefer `~/.colabhive/daemon.json`'s known production DB if it exists on disk.
		if daemonInfo != nil && daemonInfo.DBPath != "" && daemonInfo.DBPath != ":memory:" && isRegularFile(daemonInfo.DBPath) {
			out.DBPath = daemonInfo.DBPath
			out.DataDir = filepath.Dir(daemonInfo.DBPath)
		}
		// Next search all candidate roots for `data/colabhive_prod.db`, then `data/colabhive.db`.
		if out.DBPath == "" {
			for _, name := range []string{"colabhive_prod.db", "colabhive.db"} {
				for _, r := range roots {
					cand := filepath.Join(r, "data", name)
					if isRegularFile(cand) {
						out.DBPath = cand
						out.DataDir = filepath.Dir(cand)
						break
					}
				}
				if out.DBPath != "" {
					break
				}
			}
		}
		// Fallback to a writable `data/colabhive.db` (preferring repo root if `web/dist` lives there and not inside a macOS `.app` bundle, else `~/.colabhive/data`).
		if out.DBPath == "" {
			for _, r := range roots {
				lowerR := strings.ToLower(filepath.ToSlash(r))
				if strings.Contains(lowerR, ".app/contents") || strings.HasSuffix(lowerR, ".app") {
					continue
				}
				if isRegularFile(filepath.Join(r, "web", "dist", "index.html")) {
					candDir := filepath.Join(r, "data")
					if os.MkdirAll(candDir, 0o755) == nil {
						out.DataDir = candDir
						out.DBPath = filepath.Join(candDir, "colabhive.db")
						break
					}
				}
			}
		}
		if out.DBPath == "" {
			if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
				homeData := filepath.Join(home, ".colabhive", "data")
				if os.MkdirAll(homeData, 0o700) == nil {
					out.DataDir = homeData
					out.DBPath = filepath.Join(homeData, "colabhive.db")
				}
			}
		}
		if out.DBPath == "" {
			fallbackDir := "./data"
			if len(roots) > 0 {
				fallbackDir = filepath.Join(roots[0], "data")
			}
			_ = os.MkdirAll(fallbackDir, 0o755)
			out.DataDir = fallbackDir
			out.DBPath = filepath.Join(fallbackDir, "colabhive.db")
		}
	}

	if out.DataDir == "" {
		out.DataDir = filepath.Join(os.TempDir(), "colabhive-data")
	}
	if out.DBPath != "" && out.DBPath != ":memory:" {
		_ = os.MkdirAll(filepath.Dir(out.DBPath), 0o755)
	}

	// 2. Resolve WebDir (`index.html`)
	if explicitWeb := strings.TrimSpace(cfg.WebDir); explicitWeb != "" {
		out.WebDir = explicitWeb
		if abs, err := filepath.Abs(explicitWeb); err == nil {
			out.WebDir = abs
		}
	} else {
		for _, r := range roots {
			for _, sub := range []string{
				filepath.Join(r, "web", "dist"),
				filepath.Join(r, "Resources", "web", "dist"),
				filepath.Join(r, "Contents", "Resources", "web", "dist"),
				filepath.Join(r, "dist"),
			} {
				if isRegularFile(filepath.Join(sub, "index.html")) {
					out.WebDir = sub
					break
				}
			}
			if out.WebDir != "" {
				break
			}
		}
	}

	return out
}

func isRegularFile(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// NormalizeLoopbackBaseURL normalizes wildcard bind URLs (`http://0.0.0.0:7860`) into `http://127.0.0.1:7860`
// so WebView2 and HTTP telemetry pollers connect cleanly over loopback.
func NormalizeLoopbackBaseURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	host := u.Hostname()
	port := u.Port()
	if host == "" || host == "0.0.0.0" || host == "::" || strings.EqualFold(host, "localhost") {
		if port != "" {
			u.Host = net.JoinHostPort("127.0.0.1", port)
		} else {
			u.Host = "127.0.0.1"
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

// ControlPlaneHandle represents either an attached external ColabHive daemon (`Owned == false`)
// or an in-process ColabHive control-plane HTTP/WS/MCP server (`Owned == true`).
type ControlPlaneHandle struct {
	BaseURL    string
	DBPath     string
	WebDir     string
	AdminToken string
	Owned      bool
	Server     *server.Server

	closeOnce sync.Once
	closeFn   func() error
}

// Close shuts down the in-process control-plane stack when `Owned == true` (no-op when attached to external daemon).
func (h *ControlPlaneHandle) Close() error {
	if h == nil || h.closeFn == nil {
		return nil
	}
	var err error
	h.closeOnce.Do(func() {
		err = h.closeFn()
	})
	return err
}

// FetchQuotaSummary queries `GET /api/v1/pool/summary` from the active control plane.
func (h *ControlPlaneHandle) FetchQuotaSummary(ctx context.Context, forceRefresh bool) (*models.QuotaSummary, error) {
	if h == nil || h.BaseURL == "" {
		return nil, fmt.Errorf("control plane URL not configured")
	}
	endpoint := h.BaseURL + "/api/v1/pool/summary"
	if forceRefresh {
		endpoint += "?refresh=true"
	}
	timeout := 5 * time.Second
	if forceRefresh {
		timeout = 25 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if tok := strings.TrimSpace(h.AdminToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pool summary HTTP %d", resp.StatusCode)
	}
	var summary models.QuotaSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

// SyncSessionsAndAccounts triggers `POST /api/v1/sessions/sync` followed by `GET /api/v1/pool/summary?refresh=true`.
func (h *ControlPlaneHandle) SyncSessionsAndAccounts(ctx context.Context) (*models.QuotaSummary, error) {
	if h == nil || h.BaseURL == "" {
		return nil, fmt.Errorf("control plane URL not configured")
	}
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	syncReq, err := http.NewRequestWithContext(syncCtx, http.MethodPost, h.BaseURL+"/api/v1/sessions/sync", strings.NewReader("{}"))
	if err == nil {
		syncReq.Header.Set("Content-Type", "application/json")
		if tok := strings.TrimSpace(h.AdminToken); tok != "" {
			syncReq.Header.Set("Authorization", "Bearer "+tok)
		}
		if resp, doErr := http.DefaultClient.Do(syncReq); doErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
	return h.FetchQuotaSummary(ctx, true)
}

// ProbeHealthyDaemon checks whether a genuine ColabHive control-plane daemon is already listening
// on `cfg.AttachURL`, `~/.colabhive/daemon.json`, or `http://127.0.0.1:<port>`.
func ProbeHealthyDaemon(ctx context.Context, cfg Config, daemonInfo *server.DaemonInfo) string {
	if explicit := NormalizeLoopbackBaseURL(cfg.AttachURL); explicit != "" {
		if isColabHiveHealthy(ctx, explicit, cfg.AdminToken) {
			return explicit
		}
		return ""
	}

	var candidates []string
	addCand := func(u string) {
		norm := NormalizeLoopbackBaseURL(u)
		if norm == "" {
			return
		}
		for _, c := range candidates {
			if strings.EqualFold(c, norm) {
				return
			}
		}
		candidates = append(candidates, norm)
	}

	port := cfg.Port
	if port <= 0 {
		port = 7860
	}
	if daemonInfo != nil && daemonInfo.URL != "" {
		addCand(daemonInfo.URL)
	}
	addCand(fmt.Sprintf("http://127.0.0.1:%d", port))

	for _, cand := range candidates {
		if isColabHiveHealthy(ctx, cand, cfg.AdminToken) {
			return cand
		}
	}
	return ""
}

func isColabHiveHealthy(ctx context.Context, baseURL string, adminToken string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, baseURL+"/api/v1/health?fast=1", nil)
	if err != nil {
		return false
	}
	if tok := strings.TrimSpace(adminToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	if healthy, ok := payload["healthy"].(bool); ok {
		return healthy
	}
	if status, ok := payload["status"].(string); ok && strings.EqualFold(status, "ok") {
		_, hasVer := payload["version"]
		return hasVer
	}
	return false
}

func isLoopbackPortBusy(port int) bool {
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return true
	}
	return false
}

// EnsureControlPlane attaches to a running ColabHive daemon if healthy, or boots the complete
// ColabHive control-plane stack (`SQLite + Pool + Router + Runtime + MCP + HTTP/WS`) in-process.
func EnsureControlPlane(ctx context.Context, cfg Config, bridge server.DesktopBridgeFunc) (*ControlPlaneHandle, error) {
	ephemeralOnly := cfg.Port <= 0 && (cfg.Demo || strings.TrimSpace(cfg.DBPath) == ":memory:")
	if cfg.Port <= 0 {
		cfg.Port = 7860
	}
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = "127.0.0.1"
	}
	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = "local"
	}
	if strings.TrimSpace(cfg.Version) == "" {
		cfg.Version = "1.1.0"
	}
	if cfg.KeepaliveSec <= 0 {
		cfg.KeepaliveSec = 8
	}

	daemonInfo, _ := server.ReadDaemonInfo()
	exePath, _ := os.Executable()
	cwd, _ := os.Getwd()
	paths := ResolveRuntimePaths(cfg, exePath, cwd, daemonInfo)

	// 1. Attach to existing live daemon unless forced into standalone `--demo` or custom `--db`.
	shouldProbeExisting := strings.TrimSpace(cfg.AttachURL) != "" || (!cfg.Demo && strings.TrimSpace(cfg.DBPath) == "")
	if shouldProbeExisting {
		if liveURL := ProbeHealthyDaemon(ctx, cfg, daemonInfo); liveURL != "" {
			return &ControlPlaneHandle{
				BaseURL:    liveURL,
				DBPath:     paths.DBPath,
				WebDir:     paths.WebDir,
				AdminToken: cfg.AdminToken,
				Owned:      false,
				closeFn:    func() error { return nil },
			}, nil
		}
		if strings.TrimSpace(cfg.AttachURL) != "" {
			return nil, fmt.Errorf("unable to reach ColabHive daemon at %s", cfg.AttachURL)
		}
	}

	// 2. Bind TCP listener first (`127.0.0.1:<port>`, falling back to `127.0.0.1:0` if busy by another app).
	var (
		ln  net.Listener
		err error
	)
	if !ephemeralOnly && !isLoopbackPortBusy(cfg.Port) {
		preferredAddr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
		ln, err = net.Listen("tcp", preferredAddr)
	}
	if ln == nil || err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("failed to bind loopback control plane listener: %w", err)
		}
	}

	actualPort := cfg.Port
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok && tcpAddr.Port > 0 {
		actualPort = tcpAddr.Port
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", actualPort)
	publicURL := fmt.Sprintf("http://localhost:%d", actualPort)

	// 3. Boot full in-process ColabHive stack.
	masterKey := strings.TrimSpace(cfg.MasterKey)
	explicitMasterKey := masterKey != ""
	if masterKey == "" && paths.DBPath != "" && paths.DBPath != ":memory:" {
		mk, mkErr := vault.LoadOrCreateMasterKeyForDB(paths.DBPath, paths.DataDir)
		if mkErr != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("failed to initialize master key file for %s: %w", paths.DBPath, mkErr)
		}
		masterKey = mk
	}

	st, err := store.OpenSQLite(paths.DBPath)
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("failed to open SQLite store (%s): %w", paths.DBPath, err)
	}

	hub := events.NewHub()
	txFactory := proxy.NewTransportFactory()

	var mockSrv *mockcolab.Server
	var poolCfg pool.Config
	tunnelURL := models.ColabTunnelDomain
	publicAPIURL := models.ColabPublicAPIDomain
	strictCloudVault := strings.EqualFold(strings.TrimSpace(cfg.Mode), "cloud") && explicitMasterKey

	if cfg.Demo {
		mockSrv = mockcolab.NewServer()
		poolCfg = mockSrv.PoolConfig(masterKey)
		poolCfg.StrictVaultKey = strictCloudVault
		poolCfg.PublicBaseURL = publicURL
		tunnelURL = mockSrv.URL
		publicAPIURL = mockSrv.URL
	} else {
		poolCfg = pool.Config{
			MasterSecret:            masterKey,
			StrictVaultKey:          strictCloudVault,
			PublicBaseURL:           publicURL,
			CustomOAuthClientID:     strings.TrimSpace(os.Getenv("COLABHIVE_OAUTH_CLIENT_ID")),
			CustomOAuthClientSecret: strings.TrimSpace(os.Getenv("COLABHIVE_OAUTH_CLIENT_SECRET")),
		}
	}

	poolSvc := pool.NewService(st, hub, poolCfg)
	rtr := router.NewService(st, poolSvc, hub)

	if strat := strings.TrimSpace(cfg.DefaultStrategy); strat != "" {
		if pol, pErr := rtr.GetPolicy(ctx); pErr == nil && pol != nil {
			pol.DefaultStrategy = router.NormalizeStrategy(models.RoutingStrategy(strat), models.StrategyWasteZero)
			_, _ = rtr.UpdatePolicy(ctx, *pol)
		}
	}

	artifactsDir := filepath.Join(paths.DataDir, "artifacts")
	if paths.DBPath == ":memory:" {
		artifactsDir = filepath.Join(os.TempDir(), "colabhive-desktop-demo-artifacts")
	}

	fallbackProbeDur := time.Duration(cfg.FallbackProbeSec) * time.Second
	if fallbackProbeDur < 5*time.Second {
		fallbackProbeDur = 28 * time.Second
	}

	rtEngine := runtime.NewConcreteEngine(st, poolSvc, rtr, hub, runtime.Options{
		DemoMode:              false,
		TunnelBaseURL:         tunnelURL,
		PublicAPIBaseURL:      publicAPIURL,
		KeepaliveInterval:     time.Duration(cfg.KeepaliveSec) * time.Second,
		ResourcePollInterval:  10 * time.Second,
		FallbackAssignTimeout: fallbackProbeDur,
		ArtifactsDir:          artifactsDir,
		HTTPClientFactory: func(proxyURL string) (*http.Client, *websocket.Dialer, error) {
			hc, hErr := txFactory.HTTPClient(proxyURL, 15*time.Minute)
			if hErr != nil {
				return nil, nil, hErr
			}
			wd, wErr := txFactory.WebSocketDialer(proxyURL, 20*time.Second)
			if wErr != nil {
				return nil, nil, wErr
			}
			return hc, wd, nil
		},
	})
	if drainer, ok := any(rtEngine).(interface {
		StopManagedSessionsForAccount(context.Context, string) error
	}); ok {
		if setter, sOk := any(poolSvc).(interface {
			SetSessionDrainer(func(context.Context, string) error)
		}); sOk {
			setter.SetSessionDrainer(drainer.StopManagedSessionsForAccount)
		}
	}

	if cfg.Demo {
		_, _ = mockcolab.SeedDemoCluster(ctx, st, poolSvc, mockSrv)
		_ = server.SeedDemoCluster(ctx, st, rtr, artifactsDir, publicURL)
	} else {
		rtEngine.TriggerSyncAsync()
	}

	mcpSrv := mcp.NewServer(poolSvc, rtEngine, rtr, hub)
	if strings.TrimSpace(cfg.AdminToken) != "" {
		mcpSrv.SetAdminToken(cfg.AdminToken)
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.Mode), "cloud") && strings.TrimSpace(cfg.MCPTrustedWorkspace) != "" {
		mcpSrv.SetTrustedWorkspaceDir(cfg.MCPTrustedWorkspace)
	}

	srv := server.New(st, poolSvc, rtEngine, rtr, hub, mcpSrv, server.Config{
		Version:              cfg.Version,
		Mode:                 cfg.Mode,
		AdminToken:           cfg.AdminToken,
		MCPTrustedWorkspace:  cfg.MCPTrustedWorkspace,
		WebDir:               paths.WebDir,
		DataDir:              paths.DataDir,
		ArtifactsDir:         artifactsDir,
		MasterSecret:         masterKey,
		StrictVaultKey:       strictCloudVault,
		PublicURL:            publicURL,
		DemoMode:             cfg.Demo,
		KeepaliveIntervalSec: cfg.KeepaliveSec,
		ResourcePollSec:      10,
	})
	if bridge != nil {
		srv.SetDesktopBridge(bridge)
	}

	if (!cfg.Demo && paths.DBPath != ":memory:") || strings.TrimSpace(os.Getenv("COLABHIVE_DAEMON_INFO_PATH")) != "" {
		_ = server.WriteDaemonInfo(server.DaemonInfo{
			URL:       baseURL,
			PID:       os.Getpid(),
			DBPath:    paths.DBPath,
			StartedAt: time.Now().UTC(),
		})
	}

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		_ = httpSrv.Serve(ln)
	}()

	closeFn := func() error {
		shutCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		_ = srv.Close()
		_ = rtEngine.Close()
		if mockSrv != nil {
			mockSrv.Close()
		}
		return st.Close()
	}

	return &ControlPlaneHandle{
		BaseURL:    baseURL,
		DBPath:     paths.DBPath,
		WebDir:     paths.WebDir,
		AdminToken: cfg.AdminToken,
		Owned:      true,
		Server:     srv,
		closeFn:    closeFn,
	}, nil
}
