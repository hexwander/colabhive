package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/colabhive/colabhive/internal/pluginhost"
)

func TestPluginSDK_SandboxCORSAndLeastPrivilegeIsolation(t *testing.T) {
	dataDir := t.TempDir()
	pluginDir := filepath.Join(dataDir, "plugins", "custom-media-ui")
	distDir := filepath.Join(pluginDir, "ui", "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<!doctype html><h1>Custom UI</h1>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	manifest := `{
  "schema_version": "2",
  "id": "custom-media-ui",
  "kind": "ui",
  "name": "Custom Media UI",
  "name_zh": "第三方极速生图 UI",
  "category": "image",
  "consumes": ["media.image.v1"],
  "permissions": ["assets:read", "engines:read"],
  "ui": { "mode": "local_bundle", "bundle_dir": "ui/dist", "entry": "index.html" }
}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	srv := New(nil, nil, nil, nil, nil, nil, Config{
		DataDir:    dataDir,
		AdminToken: "adm_secret_123",
		DemoMode:   true,
	})
	defer srv.Close()
	handler := srv.Handler()

	// 1. Admin control-plane opens the isolated third-party UI sandbox.
	openReq := httptest.NewRequest(http.MethodPost, "/sdk/v1/ui-plugins/custom-media-ui/open", nil)
	openReq.Header.Set("Authorization", "Bearer adm_secret_123")
	openRec := httptest.NewRecorder()
	handler.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("expected 200 opening UI plugin, got %d: %s", openRec.Code, openRec.Body.String())
	}
	var openResp pluginhost.SandboxSession
	if err := json.Unmarshal(openRec.Body.Bytes(), &openResp); err != nil || openResp.Origin == "" {
		t.Fatalf("invalid open response: %s", openRec.Body.String())
	}

	// Fetch generated SDK script from sandbox origin to extract scoped plugin token.
	sdkResp, err := http.Get(openResp.Origin + "/__colabhive_sdk.js")
	if err != nil {
		t.Fatalf("GET __colabhive_sdk.js: %v", err)
	}
	defer sdkResp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(sdkResp.Body)
	sdkSrc := buf.String()
	idx := bytes.Index([]byte(sdkSrc), []byte("ch_plug_"))
	if idx < 0 {
		t.Fatalf("expected scoped ch_plug_ token in __colabhive_sdk.js")
	}
	plugTok := sdkSrc[idx : idx+len("ch_plug_")+48]

	// 2. Declared permission (assets:read) succeeds with scoped token even without AdminToken!
	listReq := httptest.NewRequest(http.MethodGet, "/sdk/v1/assets", nil)
	listReq.Header.Set("Origin", openResp.Origin)
	listReq.Header.Set("X-ColabHive-Plugin-Token", plugTok)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on permitted GET /sdk/v1/assets, got %d: %s", listRec.Code, listRec.Body.String())
	}

	// 3. Undeclared permission (assets:write) MUST be rejected with 403!
	writeReq := httptest.NewRequest(http.MethodPost, "/sdk/v1/assets", bytes.NewReader([]byte(`{"filename":"a.png","data_base64":"AAAA"}`)))
	writeReq.Header.Set("Origin", openResp.Origin)
	writeReq.Header.Set("Content-Type", "application/json")
	writeReq.Header.Set("X-ColabHive-Plugin-Token", plugTok)
	writeRec := httptest.NewRecorder()
	handler.ServeHTTP(writeRec, writeReq)
	if writeRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on undeclared assets:write, got %d: %s", writeRec.Code, writeRec.Body.String())
	}

	// 4. Control-plane route (/api/v1/accounts) MUST reject plugin token with 403!
	accReq := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	accReq.Header.Set("Origin", openResp.Origin)
	accReq.Header.Set("X-ColabHive-Plugin-Token", plugTok)
	accRec := httptest.NewRecorder()
	handler.ServeHTTP(accRec, accReq)
	if accRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when plugin token tries /api/v1/accounts, got %d: %s", accRec.Code, accRec.Body.String())
	}

	// 5. Control-plane-only preference endpoint MUST reject plugin token with 403!
	prefReq := httptest.NewRequest(http.MethodPut, "/sdk/v1/ui-preferences/media.v1", bytes.NewReader([]byte(`{"ui_plugin_id":"custom-media-ui"}`)))
	prefReq.Header.Set("Origin", openResp.Origin)
	prefReq.Header.Set("Content-Type", "application/json")
	prefReq.Header.Set("X-ColabHive-Plugin-Token", plugTok)
	prefRec := httptest.NewRecorder()
	handler.ServeHTTP(prefRec, prefReq)
	if prefRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when plugin tries to hijack UI preferences, got %d: %s", prefRec.Code, prefRec.Body.String())
	}
}
