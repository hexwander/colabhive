// Command colabhive-desktop launches the Native Desktop Application (Windows Frameless
// Win32/WebView2 & macOS Cocoa/WKWebView), Single-Instance Guard, Magpie-Style `/tray`
// System Tray / Menu Bar Popover, and Native Context Menu for ColabHive.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/colabhive/colabhive/internal/desktop"
)

var (
	version   = "1.0.0"
	commit    = "dev"
	buildDate = "unknown"
)

type cliToolFlags struct {
	showVersion bool
	writeICNS   string
	writePNG    string
	iconSize    int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code := runDesktopMain(ctx, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

func runDesktopMain(ctx context.Context, args []string, out io.Writer, errOut io.Writer) int {
	cfg, toolFlags, err := parseDesktopFlagsWithTools(args, errOut)
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if toolFlags.showVersion {
		_, _ = fmt.Fprintf(out, "colabhive-desktop v%s (%s @ %s)\n", cfg.Version, cfg.Commit, cfg.BuildDate)
		return 0
	}
	if strings.TrimSpace(toolFlags.writeICNS) != "" {
		if err := writeICNSFile(toolFlags.writeICNS); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colabhive-desktop] write-icns failed: %v\n", err)
			return 1
		}
		return 0
	}
	if strings.TrimSpace(toolFlags.writePNG) != "" {
		if err := writePNGFile(toolFlags.writePNG, toolFlags.iconSize); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colabhive-desktop] write-png failed: %v\n", err)
			return 1
		}
		return 0
	}

	if err := desktop.Run(ctx, cfg); err != nil && err != context.Canceled {
		_, _ = fmt.Fprintf(errOut, "[colabhive-desktop] %v\n", err)
		return 1
	}
	return 0
}

func writeICNSFile(destPath string) error {
	data, err := desktop.EncodeColabHiveICNS()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(destPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(destPath, data, 0o644)
}

func writePNGFile(destPath string, size int) error {
	if size <= 0 {
		size = 512
	}
	data, err := desktop.EncodeColabHivePNG(size)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(destPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(destPath, data, 0o644)
}

func parseDesktopFlags(args []string, errOut io.Writer) (desktop.Config, bool, error) {
	cfg, toolFlags, err := parseDesktopFlagsWithTools(args, errOut)
	return cfg, toolFlags.showVersion, err
}

func parseDesktopFlagsWithTools(args []string, errOut io.Writer) (desktop.Config, cliToolFlags, error) {
	fs := flag.NewFlagSet("colabhive-desktop", flag.ContinueOnError)
	fs.SetOutput(errOut)

	cfg := desktop.Config{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
	}

	var minimized, trayOnly bool
	var tools cliToolFlags
	defaultDemo := strings.EqualFold(envOrDefault("COLABHIVE_DEMO", ""), "true") || envOrDefault("COLABHIVE_DEMO", "") == "1"

	fs.BoolVar(&minimized, "minimized", false, "Start hidden in the System Tray / Menu Bar without opening the Main Window")
	fs.BoolVar(&trayOnly, "tray-only", false, "Alias for --minimized (start hidden in System Tray / Menu Bar only)")
	fs.StringVar(&cfg.Host, "host", envOrDefault("COLABHIVE_HOST", "127.0.0.1"), "Bind host when launching control plane in-process")
	fs.IntVar(&cfg.Port, "port", envIntOrDefault("COLABHIVE_PORT", 7860), "Control plane HTTP/WS/MCP port")
	fs.StringVar(&cfg.DataDir, "data-dir", envOrDefault("COLABHIVE_DATA_DIR", ""), "Directory for SQLite vault and artifacts")
	fs.StringVar(&cfg.DBPath, "db", envOrDefault("COLABHIVE_DB_PATH", ""), "Explicit SQLite database file path")
	fs.StringVar(&cfg.WebDir, "web-dir", envOrDefault("COLABHIVE_WEB_DIR", ""), "Frontend SPA dist directory (auto-detects web/dist)")
	fs.StringVar(&cfg.AttachURL, "url", envOrDefault("COLABHIVE_URL", ""), "Optional running ColabHive daemon URL to attach to")
	fs.StringVar(&cfg.Mode, "mode", envOrDefault("COLABHIVE_MODE", "local"), "Control plane mode (local)")
	fs.StringVar(&cfg.AdminToken, "admin-token", envOrDefault("COLABHIVE_ADMIN_TOKEN", ""), "Optional admin Bearer token")
	fs.StringVar(&cfg.MasterKey, "master-key", envOrDefault("COLABHIVE_MASTER_KEY", ""), "Optional AES-256-GCM master secret for OAuth vault")
	fs.StringVar(&cfg.DefaultStrategy, "strategy", envOrDefault("COLABHIVE_DEFAULT_STRATEGY", "waste-zero"), "Default pool routing strategy")
	fs.IntVar(&cfg.KeepaliveSec, "keepalive-sec", envIntOrDefault("COLABHIVE_KEEPALIVE_INTERVAL_SEC", 8), "Keepalive loop cadence in seconds")
	fs.BoolVar(&cfg.Demo, "demo", defaultDemo, "Launch interactive in-memory Demo Cluster")
	fs.BoolVar(&tools.showVersion, "version", false, "Print version metadata and exit")
	fs.StringVar(&tools.writeICNS, "write-icns", "", "Generate macOS AppIcon.icns bundle icon at path and exit")
	fs.StringVar(&tools.writePNG, "write-png", "", "Generate PNG app icon at path and exit")
	fs.IntVar(&tools.iconSize, "icon-size", 512, "PNG dimension when --write-png is used")

	if err := fs.Parse(args); err != nil {
		return cfg, tools, err
	}

	cfg.StartMinimized = minimized || trayOnly
	return cfg, tools, nil
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
