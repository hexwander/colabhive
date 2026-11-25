package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/appstore"
	"github.com/colabhive/colabhive/internal/models"
)

func TestAssetVault_SaveListServeAndPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	vault := NewAssetVault(dir)

	pngBytes := []byte("\x89PNG\r\n\x1a\nfake-png-body-0123456789")
	saved, err := vault.Save(MediaAsset{
		Prompt:    "astronaut bunny in orbital station",
		ModelID:   "z_image_turbo",
		ModelName: "Z-Image Turbo",
		Mode:      "t2i",
		EngineID:  "wangp",
	}, "bunny.png", bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("Save asset failed: %v", err)
	}
	if !strings.HasPrefix(saved.ID, "ast_") || saved.Kind != AssetKindImage || saved.MimeType != "image/png" {
		t.Fatalf("unexpected saved asset metadata: %+v", saved)
	}

	// Simulate restart: new vault instance reading same dataDir.
	vault2 := NewAssetVault(dir)
	list := vault2.List(AssetFilter{Kind: AssetKindImage, Query: "astronaut"})
	if len(list) != 1 || list[0].ID != saved.ID {
		t.Fatalf("expected persisted asset after restart, got %+v", list)
	}

	// Serve file with Range support.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, saved.FileURL, nil)
	vault2.ServeFile(rec, req, saved.ID)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Fatalf("ServeFile mismatch: status=%d body=%q", rec.Code, rec.Body.String())
	}

	if err := vault2.Delete(saved.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := vault2.Get(saved.ID); err == nil {
		t.Fatalf("expected ErrAssetNotFound after delete")
	}
}

func TestUISandbox_IsolatedOriginSDKInjectionAndScopedPermissions(t *testing.T) {
	dataDir := t.TempDir()
	pluginRoot := filepath.Join(dataDir, "plugins", "cinema-ui")
	distDir := filepath.Join(pluginRoot, "ui", "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte(`<!doctype html><script src="/__colabhive_sdk.js"></script><h1>Cinema UI</h1>`), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	manifestJSON := `{
  "schema_version": "2",
  "id": "cinema-ui",
  "kind": "ui",
  "name": "Cinema UI",
  "name_zh": "电影分镜 UI",
  "category": "video",
  "consumes": ["media.image.v1", "media.video.v1"],
  "permissions": ["assets:read", "media:invoke"],
  "ui": { "mode": "local_bundle", "bundle_dir": "ui/dist", "entry": "index.html" }
}`
	if err := os.WriteFile(filepath.Join(pluginRoot, "manifest.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatalf("write manifest.json: %v", err)
	}

	appMgr := appstore.NewManager(nil, nil, nil, nil, appstore.Config{
		DataDir:                dataDir,
		DisableUserHomePlugins: true,
		DemoMode:               true,
	})
	defer appMgr.Close()

	// Pure UI plugins MUST NOT appear as cloud GPU launch templates, but MUST appear in ListUIPlugins!
	for _, tpl := range appMgr.ListTemplates() {
		if tpl.ID == "cinema-ui" {
			t.Fatalf("pure kind=ui plugin should not be listed in cloud launch templates")
		}
	}
	uiPlugins := appMgr.ListUIPlugins()
	if len(uiPlugins) != 1 || uiPlugins[0].ID != "cinema-ui" || uiPlugins[0].RootDir == "" {
		t.Fatalf("expected cinema-ui in ListUIPlugins with RootDir, got %+v", uiPlugins)
	}

	host := NewHost(appMgr, nil, Config{DataDir: dataDir, HostBaseURL: "http://127.0.0.1:7860"})
	defer host.Close()

	// Switch user default UI preference and verify persistence across reload.
	if err := host.Sandbox().SetUIPreference("media.v1", "cinema-ui"); err != nil {
		t.Fatalf("SetUIPreference failed: %v", err)
	}
	caps := host.GetCapabilities()
	if caps.UIPreferences["media.v1"] != "cinema-ui" {
		t.Fatalf("expected media.v1 preference cinema-ui, got %+v", caps.UIPreferences)
	}

	sess, err := host.OpenUIPlugin("cinema-ui")
	if err != nil {
		t.Fatalf("OpenUIPlugin failed: %v", err)
	}
	if !host.Sandbox().IsRegisteredOrigin(sess.Origin) {
		t.Fatalf("expected sandbox origin %s to be registered", sess.Origin)
	}

	// Verify index.html & /__colabhive_sdk.js served cleanly on isolated origin.
	resp, err := http.Get(sess.URL)
	if err != nil {
		t.Fatalf("GET sandbox index: %v", err)
	}
	htmlBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(htmlBody), "Cinema UI") {
		t.Fatalf("unexpected sandbox HTML: status=%d body=%q", resp.StatusCode, string(htmlBody))
	}

	sdkResp, err := http.Get(sess.Origin + "/__colabhive_sdk.js")
	if err != nil {
		t.Fatalf("GET __colabhive_sdk.js: %v", err)
	}
	sdkBody, _ := io.ReadAll(sdkResp.Body)
	_ = sdkResp.Body.Close()
	if sdkResp.StatusCode != 200 || !strings.Contains(string(sdkBody), "window.ColabHiveSDK") {
		t.Fatalf("unexpected SDK JS response: status=%d", sdkResp.StatusCode)
	}

	// Enforce least-privilege permissions & strict origin binding.
	if _, ok := host.Sandbox().AuthenticateToken(sess.token, sess.Origin, "assets:read"); !ok {
		t.Fatalf("expected declared permission assets:read to succeed")
	}
	if _, ok := host.Sandbox().AuthenticateToken(sess.token, sess.Origin, "assets:write"); ok {
		t.Fatalf("undeclared permission assets:write MUST be rejected")
	}
	if _, ok := host.Sandbox().AuthenticateToken(sess.token, "http://evil.example.com", "assets:read"); ok {
		t.Fatalf("mismatched Origin MUST be rejected")
	}
}

type mockMediaRuntime struct {
	models.RuntimeEngine
	sess *models.RuntimeSession
}

func (m *mockMediaRuntime) CreateSession(_ context.Context, _ models.SessionAllocRequest) (*models.RuntimeSession, error) {
	return m.sess, nil
}
func (m *mockMediaRuntime) GetSession(_ context.Context, _ string) (*models.RuntimeSession, error) {
	return m.sess, nil
}
func (m *mockMediaRuntime) ListSessions(_ context.Context, _ models.SessionFilter) ([]*models.RuntimeSession, error) {
	return []*models.RuntimeSession{m.sess}, nil
}
func (m *mockMediaRuntime) StopSession(_ context.Context, _ string) error { return nil }
func (m *mockMediaRuntime) ExecuteCode(_ context.Context, _ models.KernelExecRequest) (*models.KernelExecResult, error) {
	return &models.KernelExecResult{Status: "ok"}, nil
}

func TestHost_EndToEndMediaJobStagesInputStreamsPreviewAndSavesOutputToVault(t *testing.T) {
	var sawUpload atomic.Bool
	outputPNG := []byte("\x89PNG\r\n\x1a\nGENERATED-VIDEO-FRAME-OUTPUT")

	// Mock remote headless `media.v1` engine worker.
	workerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/colabhive/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []MediaModelInfo{
					{ID: "z_image_turbo", Name: "Z-Image Turbo", MainOutput: []string{"image"}, Cached: true, Featured: true},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/colabhive/v1/uploads":
			sawUpload.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"path": "/tmp/staged_ref.png"})
		case r.Method == http.MethodPost && r.URL.Path == "/colabhive/v1/jobs":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["input_path"] != "/tmp/staged_ref.png" {
				http.Error(w, "missing staged input_path", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "rjob_1"})
		case r.Method == http.MethodGet && r.URL.Path == "/colabhive/v1/jobs/rjob_1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			send := func(obj any) {
				b, _ := json.Marshal(obj)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", string(b))
				if fl != nil {
					fl.Flush()
				}
			}
			send(map[string]any{"type": "progress", "phase": "inference", "status": "Step 2/4", "progress": 50, "current_step": 2, "total_steps": 4})
			send(map[string]any{"type": "preview", "phase": "inference", "progress": 75, "preview_data_url": "data:image/jpeg;base64,/9j/4AAQ"})
			send(map[string]any{"type": "log", "text": "Saved output png"})
			send(map[string]any{
				"type": "done",
				"files": []map[string]any{
					{"file_id": "f1", "filename": "out_001.png", "mime_type": "image/png", "kind": "image"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/colabhive/v1/files/f1":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(outputPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	defer workerSrv.Close()

	dataDir := t.TempDir()
	rt := &mockMediaRuntime{
		sess: &models.RuntimeSession{
			ID:          "sess_media_1",
			Name:        "app-wangp-abc123",
			Status:      models.SessionIdle,
			Accelerator: models.AcceleratorA100,
			ProxyURL:    workerSrv.URL,
			CreatedAt:   time.Now().UTC(),
		},
	}
	appMgr := appstore.NewManager(nil, rt, nil, nil, appstore.Config{
		DataDir:                dataDir,
		DisableUserHomePlugins: true,
		DemoMode:               true,
	})
	defer appMgr.Close()

	host := NewHost(appMgr, nil, Config{DataDir: dataDir, HostBaseURL: "http://127.0.0.1:7860"})
	defer host.Close()

	engines := host.ListMediaEngines()
	if len(engines) != 1 || !engines[0].MediaAPIReady {
		t.Fatalf("expected 1 ready media engine, got %+v", engines)
	}
	instID := engines[0].InstanceID

	modelsList, err := host.ListEngineModels(context.Background(), instID, false)
	if err != nil || len(modelsList) != 1 || modelsList[0].ID != "z_image_turbo" {
		t.Fatalf("ListEngineModels failed: err=%v list=%+v", err, modelsList)
	}

	// Save a source reference image in Asset Vault, then run an I2I job referencing it!
	refAsset, err := host.Assets().Save(MediaAsset{Kind: AssetKindImage, Prompt: "source"}, "ref.png", bytes.NewReader([]byte("ref-img")))
	if err != nil {
		t.Fatalf("save ref asset: %v", err)
	}

	job, err := host.CreateMediaJob(context.Background(), MediaJobRequest{
		InstanceID:   instID,
		ModelID:      "z_image_turbo",
		ModelName:    "Z-Image Turbo",
		Mode:         "i2i",
		Prompt:       "cyberpunk neon style",
		InputAssetID: refAsset.ID,
	})
	if err != nil {
		t.Fatalf("CreateMediaJob failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var finalJob *MediaJob
	for time.Now().Before(deadline) {
		j, _ := host.GetMediaJob(job.ID)
		if j != nil && (j.State == MediaJobDone || j.State == MediaJobError) {
			finalJob = j
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalJob == nil || finalJob.State != MediaJobDone {
		t.Fatalf("expected MediaJobDone, got %+v", finalJob)
	}
	if !sawUpload.Load() {
		t.Fatalf("expected input_asset_id to be automatically staged via POST /colabhive/v1/uploads")
	}
	if len(finalJob.Assets) != 1 || finalJob.Assets[0].ParentAssetID != refAsset.ID {
		t.Fatalf("expected 1 output asset linked to parent %s, got %+v", refAsset.ID, finalJob.Assets)
	}
	if finalJob.PreviewDataURL == "" {
		t.Fatalf("expected live preview data URL to be captured")
	}
}
