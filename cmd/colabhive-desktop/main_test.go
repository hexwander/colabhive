package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDesktopFlags(t *testing.T) {
	t.Setenv("COLABHIVE_HOST", "")
	t.Setenv("COLABHIVE_PORT", "")
	t.Setenv("COLABHIVE_URL", "")
	t.Setenv("COLABHIVE_DATA_DIR", "")
	t.Setenv("COLABHIVE_DB_PATH", "")
	t.Setenv("COLABHIVE_WEB_DIR", "")
	t.Setenv("COLABHIVE_DEFAULT_STRATEGY", "")

	var errBuf bytes.Buffer
	cfg, showVer, err := parseDesktopFlags([]string{
		"--minimized",
		"--port", "17860",
		"--data-dir", `C:\ColabHive\data`,
		"--db", `C:\ColabHive\data\colabhive_prod.db`,
		"--web-dir", `C:\ColabHive\web\dist`,
		"--url", "http://127.0.0.1:17860",
	}, &errBuf)
	if err != nil {
		t.Fatalf("parseDesktopFlags failed: %v", err)
	}
	if showVer {
		t.Fatalf("expected showVersion=false")
	}
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("expected default Host=127.0.0.1, got %q", cfg.Host)
	}
	if !cfg.StartMinimized {
		t.Fatalf("expected StartMinimized=true when --minimized is passed")
	}
	if cfg.Port != 17860 {
		t.Fatalf("expected Port=17860, got %d", cfg.Port)
	}
	if cfg.DataDir != `C:\ColabHive\data` {
		t.Fatalf("unexpected DataDir: %q", cfg.DataDir)
	}
	if cfg.DBPath != `C:\ColabHive\data\colabhive_prod.db` {
		t.Fatalf("unexpected DBPath: %q", cfg.DBPath)
	}
	if cfg.WebDir != `C:\ColabHive\web\dist` {
		t.Fatalf("unexpected WebDir: %q", cfg.WebDir)
	}
	if cfg.AttachURL != "http://127.0.0.1:17860" {
		t.Fatalf("unexpected AttachURL: %q", cfg.AttachURL)
	}
	if cfg.DefaultStrategy != "" {
		t.Fatalf("expected omitted --strategy to remain empty (preserve SQLite policy), got %q", cfg.DefaultStrategy)
	}
}

func TestTrayOnlyAliasAndVersionFlag(t *testing.T) {
	var errBuf bytes.Buffer
	cfg, _, err := parseDesktopFlags([]string{"--tray-only"}, &errBuf)
	if err != nil {
		t.Fatalf("parseDesktopFlags(--tray-only) failed: %v", err)
	}
	if !cfg.StartMinimized {
		t.Fatalf("expected --tray-only to set StartMinimized=true")
	}

	var out bytes.Buffer
	code := runDesktopMain(context.Background(), []string{"--version"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("expected exit code 0 for --version, got %d", code)
	}
	if !strings.Contains(out.String(), "colabhive-desktop v") {
		t.Fatalf("expected version banner, got %q", out.String())
	}
}

func TestWriteICNSAndPNGUtilityFlags(t *testing.T) {
	tmp := t.TempDir()
	icnsPath := filepath.Join(tmp, "Resources", "AppIcon.icns")
	pngPath := filepath.Join(tmp, "preview", "icon-256.png")

	var out, errBuf bytes.Buffer
	if code := runDesktopMain(context.Background(), []string{"--write-icns", icnsPath}, &out, &errBuf); code != 0 {
		t.Fatalf("--write-icns failed with code %d: %s", code, errBuf.String())
	}
	icnsBytes, err := os.ReadFile(icnsPath)
	if err != nil {
		t.Fatalf("ReadFile(icns) failed: %v", err)
	}
	if len(icnsBytes) < 64 || string(icnsBytes[:4]) != "icns" {
		t.Fatalf("invalid icns header in %s (len=%d)", icnsPath, len(icnsBytes))
	}

	if code := runDesktopMain(context.Background(), []string{"--write-png", pngPath, "--icon-size", "256"}, &out, &errBuf); code != 0 {
		t.Fatalf("--write-png failed with code %d: %s", code, errBuf.String())
	}
	pngBytes, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatalf("ReadFile(png) failed: %v", err)
	}
	if len(pngBytes) < 32 || !bytes.Equal(pngBytes[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("invalid PNG header in %s (len=%d)", pngPath, len(pngBytes))
	}
}
