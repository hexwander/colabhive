package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/server"
	"github.com/colabhive/colabhive/internal/store"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestFullWiredControlPlaneStackAndMCPStdio(t *testing.T) {
	ctx := context.Background()
	srv, cleanup, err := buildControlPlaneStack(ctx, serveOptions{
		Host:         "127.0.0.1",
		Port:         0,
		DataDir:      t.TempDir(),
		DBPath:       ":memory:",
		Demo:         true,
		KeepaliveSec: 60,
		PublicURL:    "http://127.0.0.1:7860",
	})
	if err != nil {
		t.Fatalf("buildControlPlaneStack failed: %v", err)
	}
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Verify full SQLite + Pool + MockColab + Router + Runtime + Server integration
	resp, err := http.Get(ts.URL + "/api/v1/pool/summary")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET /api/v1/pool/summary failed: err=%v", err)
	}
	var summary models.QuotaSummary
	_ = json.NewDecoder(resp.Body).Decode(&summary)
	_ = resp.Body.Close()
	if summary.AccountsTotal < 4 || summary.TotalAvailableCU <= 0 {
		t.Fatalf("expected >=4 seeded accounts in full SQLite stack, got %+v", summary)
	}

	// 2. Allocate a real G4 session over MockColab 2-step XSRF & execute Python over ZMQ-over-WebSocket!
	var outBuf, errBuf bytes.Buffer
	code := runMain(ctx, "colab", []string{"--server", ts.URL, "new", "-s", "e2e_g4", "--gpu", "G4", "--high-mem"}, nil, &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("colab new failed: %s", errBuf.String())
	}

	outBuf.Reset()
	errBuf.Reset()
	code = runMain(ctx, "colab", []string{"--server", ts.URL, "exec", "-s", "e2e_g4", "import torch; print(torch.cuda.get_device_name(0))"}, nil, &outBuf, &errBuf)
	if code != 0 || strings.TrimSpace(outBuf.String()) == "" {
		t.Fatalf("colab exec failed (code=%d): out=%q err=%q", code, outBuf.String(), errBuf.String())
	}

	// 3. Test `colabhive mcp stdio` bridged to the running daemon!
	mcpIn := bytes.NewBufferString(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-03-26\",\"clientInfo\":{\"name\":\"test-cli\",\"version\":\"1.0\"}}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"pool_quota_summary\",\"arguments\":{\"refresh_live\":false}}}\n",
	)
	var mcpOut, mcpErr bytes.Buffer
	code = runMain(ctx, "colabhive", []string{"mcp", "stdio", "--server", ts.URL}, mcpIn, &mcpOut, &mcpErr)
	if code != 0 {
		t.Fatalf("colabhive mcp stdio failed: %s", mcpErr.String())
	}
	lines := strings.Split(strings.TrimSpace(mcpOut.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "total_available_cu") {
		t.Fatalf("unexpected mcp stdio bridge output: %s", mcpOut.String())
	}

	// 4. Stop session cleanly
	outBuf.Reset()
	code = runMain(ctx, "colab", []string{"--server", ts.URL, "stop", "-s", "e2e_g4"}, nil, &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("colab stop failed: %s", errBuf.String())
	}
}

func TestDaemonJSONDiscoveryAndStdioBridgeJSONRPCErrorWrapping(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	daemonInfoPath := filepath.Join(tmpDir, "daemon.json")
	prodDBPath := filepath.Join(tmpDir, "colabhive_prod.db")
	t.Setenv("COLABHIVE_DAEMON_INFO_PATH", daemonInfoPath)

	// Seed an account directly into `colabhive_prod.db` so we can prove embedded fallback opens `prodDBPath`!
	st, err := store.OpenSQLite(prodDBPath)
	if err != nil {
		t.Fatalf("OpenSQLite(%s) failed: %v", prodDBPath, err)
	}
	_ = st.SaveAccount(ctx, &models.Account{
		ID:               "acc_prod_sqlite",
		Email:            "prod@colabhive.dev",
		SubscriptionTier: models.TierAIUltra,
		Health:           models.HealthHealthy,
		Enabled:          true,
		PaidCUBalance:    777.0,
		SchedulableCU:    777.0,
	})
	_ = st.Close()

	// Start an admin-token-protected HTTP control plane on a dynamic port and write `daemon.json`.
	srv, cleanup, err := buildControlPlaneStack(ctx, serveOptions{
		Host:         "127.0.0.1",
		Port:         0,
		DataDir:      tmpDir,
		DBPath:       prodDBPath,
		AdminToken:   "super-secret-admin-tok",
		KeepaliveSec: 60,
	})
	if err != nil {
		t.Fatalf("buildControlPlaneStack failed: %v", err)
	}
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if err := server.WriteDaemonInfo(server.DaemonInfo{
		URL:    ts.URL,
		DBPath: prodDBPath,
	}); err != nil {
		t.Fatalf("WriteDaemonInfo failed: %v", err)
	}

	// 1. Without token -> `colabhive mcp` auto-discovers `ts.URL` via `daemon.json`, hits HTTP 401,
	//    and MUST wrap 401 into a valid JSON-RPC `-32603` error object matching `id: 9` (never raw `{"error":...}` or exit 1)!
	mcpIn401 := bytes.NewBufferString(
		"{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"ping\"}\n" +
			"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n",
	)
	var out401, err401 bytes.Buffer
	code := runMain(ctx, "colabhive", []string{"mcp"}, mcpIn401, &out401, &err401)
	if code != 0 {
		t.Fatalf("expected exit 0 with JSON-RPC wrapped error, got code=%d stderr=%s", code, err401.String())
	}
	var rpcErrObj map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out401.Bytes()), &rpcErrObj); err != nil {
		t.Fatalf("stdout is not valid single-line JSON-RPC: %q (%v)", out401.String(), err)
	}
	if rpcErrObj["jsonrpc"] != "2.0" || rpcErrObj["id"] != float64(9) || rpcErrObj["error"] == nil {
		t.Fatalf("expected valid JSON-RPC 2.0 -32603 error for id=9, got: %+v", rpcErrObj)
	}

	// 2. With `COLABHIVE_ADMIN_TOKEN` -> `colabhive mcp` forwards admin token + `--strategy` over HTTP bridge cleanly!
	t.Setenv("COLABHIVE_ADMIN_TOKEN", "super-secret-admin-tok")
	mcpInOK := bytes.NewBufferString(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"pool_quota_summary\",\"arguments\":{}}}\n",
	)
	var outOK, errOK bytes.Buffer
	code = runMain(ctx, "colabhive", []string{"mcp", "--strategy", "round-robin"}, mcpInOK, &outOK, &errOK)
	if code != 0 || !strings.Contains(outOK.String(), "777") {
		t.Fatalf("expected bridged pool_quota_summary to return 777 CU from prodDB, code=%d out=%s err=%s", code, outOK.String(), errOK.String())
	}

	// 3. Shut down HTTP daemon -> `colabhive mcp` falls back to embedded SQLite using `daemon.json`'s `prodDBPath` (777 CU), NOT `./data/colabhive.db`!
	ts.Close()
	mcpInEmbedded := bytes.NewBufferString(
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"pool_quota_summary\",\"arguments\":{}}}\n",
	)
	var outEmb, errEmb bytes.Buffer
	code = runMain(ctx, "colabhive", []string{"mcp"}, mcpInEmbedded, &outEmb, &errEmb)
	if code != 0 || !strings.Contains(outEmb.String(), "777") {
		t.Fatalf("expected offline embedded fallback to use daemon.json prodDBPath (777 CU), code=%d out=%s err=%s", code, outEmb.String(), errEmb.String())
	}

	// 4. Verify `colabhive serve --mode cloud --trust-proxy` flag parsing, fail-fast admin token check,
	//    `--auto-admin-token` startup banner, and `colabhive connect` CLI dispatch!
	var cloudErr bytes.Buffer
	cloudOpts, err := parseServeOptions([]string{"--mode", "cloud", "--trust-proxy", "--admin-token", "tok123", "--db", prodDBPath}, &cloudErr)
	if err != nil || cloudOpts.Mode != "cloud" || !cloudOpts.TrustProxy || cloudOpts.AdminToken != "tok123" {
		t.Fatalf("unexpected cloud serveOptions: err=%v opts=%+v", err, cloudOpts)
	}

	// Without `--admin-token` or `--auto-admin-token`, `--mode cloud` MUST fail fast with exit code 2!
	t.Setenv("COLABHIVE_ADMIN_TOKEN", "")
	var failFastErr bytes.Buffer
	exitFailFast := runServe(ctx, []string{"--mode", "cloud", "--db", prodDBPath}, &failFastErr)
	if exitFailFast != 2 || !strings.Contains(failFastErr.String(), "--mode cloud requires --admin-token") {
		t.Fatalf("expected fail-fast exit 2 on --mode cloud without token, got code=%d stderr=%q", exitFailFast, failFastErr.String())
	}

	// With `--auto-admin-token`, `--mode cloud` generates `ch_adm_...`, prints CLOUD startup banner, and shuts down cleanly!
	serveCtx, cancelServe := context.WithCancel(ctx)
	var autoBanner lockedBuffer
	doneCh := make(chan int, 1)
	go func() {
		doneCh <- runServe(serveCtx, []string{
			"--mode", "cloud",
			"--trust-proxy",
			"--auto-admin-token",
			"--host", "127.0.0.1",
			"--port", "0",
			"--public-url", "https://cloud.colabhive.test",
			"--db", prodDBPath,
		}, &autoBanner)
	}()
	// Wait briefly for listener startup banner, then cancel context cleanly.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(autoBanner.String(), "Auto Admin Token") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	cancelServe()
	serveExit := <-doneCh
	if serveExit != 0 {
		t.Fatalf("expected clean exit 0 from auto-admin-token cloud server, got %d: %s", serveExit, autoBanner.String())
	}
	bannerStr := autoBanner.String()
	if !strings.Contains(bannerStr, "CLOUD (Hardened Remote Server)") ||
		!strings.Contains(bannerStr, "https://cloud.colabhive.test/mcp") ||
		!strings.Contains(bannerStr, "ch_adm_") {
		t.Fatalf("expected CLOUD mode, MCP endpoint, and ch_adm_ token in startup banner, got:\n%s", bannerStr)
	}

	// Verify `colabhive connect` via `runMain` against a temp home dir!
	tmpLaptopHome := t.TempDir()
	var connOut, connErr bytes.Buffer
	connCode := runMain(
		ctx,
		"colabhive",
		[]string{
			"connect", "claude-code",
			"--server", "https://cloud.colabhive.test",
			"--key", "ch_sk_live_laptop_e2e_key_99887766",
			"--home", tmpLaptopHome,
		},
		nil,
		&connOut,
		&connErr,
	)
	if connCode != 0 {
		t.Fatalf("runMain colabhive connect failed (code=%d): %s", connCode, connErr.String())
	}
	claudeCfgBytes, readErr := os.ReadFile(filepath.Join(tmpLaptopHome, ".claude.json"))
	if readErr != nil || !strings.Contains(string(claudeCfgBytes), "https://cloud.colabhive.test/mcp") {
		t.Fatalf("expected .claude.json written in temp laptop home, err=%v body=%s", readErr, string(claudeCfgBytes))
	}
}

func TestLocalLoopbackDefaultCloudMasterKeyStdioInvalidKeyAndStaleDaemonFallback(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. Default `--host` MUST be `"127.0.0.1"` in `local` mode and `"0.0.0.0"` in `cloud` mode when `COLABHIVE_HOST` is unset!
	t.Setenv("COLABHIVE_HOST", "")
	var parseErr bytes.Buffer
	localOpts, err := parseServeOptions([]string{"--data-dir", tmpDir}, &parseErr)
	if err != nil || localOpts.Host != "127.0.0.1" {
		t.Fatalf("expected local mode default Host='127.0.0.1', got err=%v host=%q", err, localOpts.Host)
	}
	cloudOpts, err := parseServeOptions([]string{"--mode", "cloud", "--data-dir", tmpDir}, &parseErr)
	if err != nil || cloudOpts.Host != "0.0.0.0" {
		t.Fatalf("expected cloud mode default Host='0.0.0.0', got err=%v host=%q", err, cloudOpts.Host)
	}

	// 2. `--mode cloud` WITH `--admin-token` but WITHOUT `--master-key` (and without `--auto-admin-token` / `--demo`)
	//    MUST fail fast with exit code 2!
	t.Setenv("COLABHIVE_MASTER_KEY", "")
	var cloudNoMasterErr bytes.Buffer
	exitNoMaster := runServe(ctx, []string{"--mode", "cloud", "--admin-token", "adm-tok", "--data-dir", tmpDir}, &cloudNoMasterErr)
	if exitNoMaster != 2 || !strings.Contains(cloudNoMasterErr.String(), "requires --master-key") {
		t.Fatalf("expected exit 2 on --mode cloud without master key, got code=%d stderr=%q", exitNoMaster, cloudNoMasterErr.String())
	}

	// 3. Local mode `buildControlPlaneStack` with empty `MasterKey` persists `<DataDir>/.master.key`!
	mkDir := t.TempDir()
	_, cleanup, err := buildControlPlaneStack(ctx, serveOptions{
		Mode:         "local",
		Host:         "127.0.0.1",
		DataDir:      mkDir,
		DBPath:       filepath.Join(mkDir, "local.db"),
		KeepaliveSec: 60,
	})
	if err != nil {
		t.Fatalf("buildControlPlaneStack local failed: %v", err)
	}
	cleanup()
	if rawMK, rErr := os.ReadFile(filepath.Join(mkDir, ".master.key")); rErr != nil || len(strings.TrimSpace(string(rawMK))) < 32 {
		t.Fatalf("expected persisted .master.key in %s, err=%v len=%d", mkDir, rErr, len(rawMK))
	}

	// 4. Embedded `colabhive mcp --embedded --api-key ch_sk_live_invalid...` MUST fail closed with exit code 1!
	var mcpOut, mcpErr bytes.Buffer
	exitBadKey := runMain(
		ctx,
		"colabhive",
		[]string{"mcp", "--embedded", "--db", filepath.Join(mkDir, "local.db"), "--api-key", "ch_sk_live_invalid_bogus_key_999"},
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"),
		&mcpOut,
		&mcpErr,
	)
	if exitBadKey == 0 || mcpOut.Len() > 0 {
		t.Fatalf("expected embedded colabhive mcp with invalid --api-key to fail closed, got code=%d out=%q err=%q",
			exitBadKey, mcpOut.String(), mcpErr.String())
	}

	// 5. Explicit `--server` pointing to an unreachable URL MUST fail fast with exit code 1 instead of opening a local DB!
	nonExistentDB := filepath.Join(t.TempDir(), "should_never_be_created.db")
	mcpOut.Reset()
	mcpErr.Reset()
	exitDeadServer := runMain(
		ctx,
		"colabhive",
		[]string{"mcp", "--server", "http://127.0.0.1:1", "--db", nonExistentDB},
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"),
		&mcpOut,
		&mcpErr,
	)
	if exitDeadServer == 0 {
		t.Fatalf("expected unreachable explicit --server to fail fast with exit != 0, got 0")
	}
	if _, statErr := os.Stat(nonExistentDB); !os.IsNotExist(statErr) {
		t.Fatalf("unreachable explicit --server silently created/opened local SQLite DB %s!", nonExistentDB)
	}

	// 6. Stale `daemon.json` ephemeral URL falls back to healthy `:7860` fallback daemon URL!
	healthyFallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/health") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"healthy":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer healthyFallback.Close()

	staleDaemonFile := filepath.Join(t.TempDir(), "daemon.json")
	t.Setenv("COLABHIVE_DAEMON_INFO_PATH", staleDaemonFile)
	_ = server.WriteDaemonInfoTo(staleDaemonFile, server.DaemonInfo{
		URL: "http://127.0.0.1:1", // Stale closed ephemeral port
	})
	discovered := discoverHealthyDaemonURLWithFallback(ctx, "", &server.DaemonInfo{URL: "http://127.0.0.1:1"}, healthyFallback.URL)
	if discovered != healthyFallback.URL {
		t.Fatalf("expected stale daemon.json discovery to fall back to %s, got %q", healthyFallback.URL, discovered)
	}
}
