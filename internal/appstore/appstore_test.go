package appstore_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/appstore"
	"github.com/colabhive/colabhive/internal/checkpoint"
	"github.com/colabhive/colabhive/internal/events"
	"github.com/colabhive/colabhive/internal/models"
)

type mockRuntime struct {
	mu                 sync.Mutex
	accounts           []*models.Account
	sessions           map[string]*models.RuntimeSession
	filesBySess        map[string]map[string][]byte
	realRemoteMode     bool
	customProxyURL     string
	customProxyToken   string
	remoteReplyBody    string
	remoteStatusCode   int
	remoteHeaders      [][2]string
	stopSessionErr     error
	stopCalls          []string
	createCalls        []models.SessionAllocRequest
	lastBootstrapReq   *models.KernelExecRequest
	lastBridgeExecReq  *models.KernelExecRequest
	bridgeExecErr      error
	bridgeTruncated    bool
	bridgeOversize     bool
	bridgeReturnNotOK  bool
	listFilesErr       error
	listFilesCallCount int
}

func newMockRuntime(accounts []*models.Account) *mockRuntime {
	return &mockRuntime{
		accounts:    accounts,
		sessions:    make(map[string]*models.RuntimeSession),
		filesBySess: make(map[string]map[string][]byte),
	}
}

func (m *mockRuntime) CreateSession(_ context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls = append(m.createCalls, req)

	excluded := make(map[string]bool)
	for _, id := range req.ExcludedAccountIDs {
		excluded[id] = true
	}

	sel := strings.TrimSpace(req.AccountSelector)
	wantID := ""
	if strings.HasPrefix(strings.ToLower(sel), "id:") {
		wantID = strings.TrimSpace(sel[3:])
	}

	var chosen *models.Account
	for _, acc := range m.accounts {
		if acc == nil || !acc.Enabled || !acc.Health.IsSchedulable() || excluded[acc.ID] {
			continue
		}
		if wantID != "" && acc.ID != wantID {
			continue
		}
		chosen = acc
		break
	}
	if chosen == nil {
		return nil, errors.New("no eligible account")
	}

	sessID := models.NewPrefixedID("sess_")
	gpu := req.Accelerator
	if gpu == "" {
		gpu = models.AcceleratorT4
	}

	endpoint := "demo-gpu-s-app"
	proxyURL := "demo://app-endpoint"
	proxyTok := ""
	if m.realRemoteMode {
		endpoint = "gpu-g4-s-realvm123"
		proxyURL = "https://8080-gpu-g4-s-realvm123.us-west1.colab.googleusercontent.com"
	}
	if m.customProxyURL != "" {
		proxyURL = m.customProxyURL
	}
	if m.customProxyToken != "" {
		proxyTok = m.customProxyToken
	}

	now := time.Now().UTC()
	sess := &models.RuntimeSession{
		ID:           sessID,
		Name:         req.Name,
		AccountID:    chosen.ID,
		AccountEmail: chosen.Email,
		Endpoint:     endpoint,
		ProxyURL:     proxyURL,
		ProxyToken:   proxyTok,
		Accelerator:  gpu,
		Status:       models.SessionIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m.sessions[sessID] = sess
	m.filesBySess[sessID] = make(map[string][]byte)
	return sess, nil
}

func (m *mockRuntime) GetSession(_ context.Context, id string) (*models.RuntimeSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, errors.New("session not found")
	}
	cp := *s
	return &cp, nil
}

func (m *mockRuntime) ListSessions(context.Context, models.SessionFilter) ([]*models.RuntimeSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*models.RuntimeSession
	for _, s := range m.sessions {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

func (m *mockRuntime) SyncRemoteAssignments(context.Context) (*models.SessionSyncResult, error) {
	return &models.SessionSyncResult{}, nil
}

func (m *mockRuntime) StopSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalls = append(m.stopCalls, id)
	if m.stopSessionErr != nil {
		return m.stopSessionErr
	}
	if s, ok := m.sessions[id]; ok {
		s.Status = models.SessionStopped
	}
	return nil
}

func (m *mockRuntime) RestartKernel(context.Context, string) error    { return nil }
func (m *mockRuntime) InterruptKernel(context.Context, string) error  { return nil }
func (m *mockRuntime) HotRestartWorker(context.Context, string) error { return nil }
func (m *mockRuntime) GetColabWebURL(context.Context, string) (string, error) {
	return "https://colab.research.google.com", nil
}

func (m *mockRuntime) ExecuteCode(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	m.mu.Lock()
	realMode := m.realRemoteMode
	replyBody := m.remoteReplyBody
	statusCode := m.remoteStatusCode
	customHdrs := append([][2]string(nil), m.remoteHeaders...)
	execErr := m.bridgeExecErr
	truncated := m.bridgeTruncated
	oversize := m.bridgeOversize
	notOK := m.bridgeReturnNotOK
	if strings.Contains(req.Code, "__COLABHIVE_BRIDGE_JSON__:") {
		cpReq := req
		m.lastBridgeExecReq = &cpReq
	} else if req.CallerName == "appstore-bootstrap" {
		cpReq := req
		cpEnv := make(map[string]string, len(req.Env))
		for k, v := range req.Env {
			cpEnv[k] = v
		}
		cpReq.Env = cpEnv
		m.lastBootstrapReq = &cpReq
	}
	m.mu.Unlock()

	if strings.Contains(req.Code, "__COLABHIVE_BRIDGE_JSON__:") {
		if execErr != nil {
			return nil, execErr
		}
		if truncated {
			return &models.KernelExecResult{
				Session:         req.SessionID,
				Status:          "ok",
				OutputTruncated: true,
				Stdout:          "__COLABHIVE_BRIDGE_JSON__:{\"ok\":true",
			}, nil
		}
		if oversize {
			return &models.KernelExecResult{
				Session: req.SessionID,
				Status:  "ok",
				Stdout:  "__COLABHIVE_BRIDGE_JSON__:" + strings.Repeat("A", (384<<10)+1024),
			}, nil
		}
		if notOK {
			raw, _ := json.Marshal(map[string]any{"ok": false, "status": 502, "error": "connection refused"})
			return &models.KernelExecResult{
				Session: req.SessionID,
				Status:  "ok",
				Stdout:  "__COLABHIVE_BRIDGE_JSON__:" + string(raw) + "\n",
			}, nil
		}
		if realMode {
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			var hdrs any = map[string]string{"Content-Type": "application/json", "X-Remote-VM": "comfyui-live"}
			if len(customHdrs) > 0 {
				pairs := make([][]string, len(customHdrs))
				for i, kv := range customHdrs {
					pairs[i] = []string{kv[0], kv[1]}
				}
				hdrs = pairs
			}
			env := map[string]any{
				"ok":       true,
				"status":   statusCode,
				"headers":  hdrs,
				"body_b64": base64.StdEncoding.EncodeToString([]byte(replyBody)),
			}
			raw, _ := json.Marshal(env)
			return &models.KernelExecResult{
				Session: req.SessionID,
				Status:  "ok",
				Stdout:  "__COLABHIVE_BRIDGE_JSON__:" + string(raw) + "\n",
			}, nil
		}
	}

	return &models.KernelExecResult{
		Session: req.SessionID,
		Status:  "ok",
		Stdout:  "[colabhive-appstore] ready\n",
	}, nil
}

func (m *mockRuntime) GetCells(context.Context, string, bool) ([]models.NotebookCell, error) {
	return nil, nil
}
func (m *mockRuntime) MutateCell(context.Context, string, models.CellMutationRequest) (*models.NotebookCell, *models.KernelExecResult, error) {
	return nil, nil, nil
}
func (m *mockRuntime) ListFiles(_ context.Context, sessionID string, remotePath string) ([]models.RemoteFileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listFilesCallCount++
	if m.listFilesErr != nil {
		return nil, m.listFilesErr
	}
	var out []models.RemoteFileEntry
	prefix := path.Clean(remotePath) + "/"
	for p, b := range m.filesBySess[sessionID] {
		if strings.HasPrefix(p, prefix) {
			name := strings.TrimPrefix(p, prefix)
			if !strings.Contains(name, "/") {
				out = append(out, models.RemoteFileEntry{Name: name, Path: p, Type: "file", SizeBytes: int64(len(b))})
			}
		}
	}
	return out, nil
}
func (m *mockRuntime) UploadFile(_ context.Context, sessionID string, remotePath string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.filesBySess[sessionID] == nil {
		m.filesBySess[sessionID] = make(map[string][]byte)
	}
	m.filesBySess[sessionID][path.Clean(remotePath)] = append([]byte(nil), data...)
	return nil
}
func (m *mockRuntime) DownloadFile(_ context.Context, sessionID string, remotePath string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.filesBySess[sessionID][path.Clean(remotePath)]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), b...), nil
}
func (m *mockRuntime) DeleteFile(context.Context, string, string) error { return nil }
func (m *mockRuntime) InstallPackages(context.Context, string, []string, string) (*models.KernelExecResult, error) {
	return &models.KernelExecResult{Status: "ok"}, nil
}
func (m *mockRuntime) LaunchShardedExperiment(context.Context, models.ExperimentLaunchRequest) (*models.Experiment, error) {
	return nil, nil
}
func (m *mockRuntime) GetExperiment(context.Context, string) (*models.Experiment, error) {
	return nil, nil
}
func (m *mockRuntime) ListExperiments(context.Context) ([]*models.Experiment, error) {
	return nil, nil
}
func (m *mockRuntime) CollectAndTeardownExperiment(context.Context, string, bool) (*models.ExperimentTeardownResult, error) {
	return nil, nil
}

type mockPool struct {
	accounts []*models.Account
}

func (p *mockPool) StartOAuth(context.Context, models.OAuthStartRequest) (*models.OAuthPendingSession, error) {
	return nil, nil
}
func (p *mockPool) CompleteOAuth(context.Context, string, string) (*models.Account, error) {
	return nil, nil
}
func (p *mockPool) ListAccounts(context.Context) ([]*models.Account, error) {
	return p.accounts, nil
}
func (p *mockPool) GetAccount(_ context.Context, id string) (*models.Account, error) {
	for _, a := range p.accounts {
		if a.ID == id || a.Email == id {
			return a, nil
		}
	}
	return nil, errors.New("not found")
}
func (p *mockPool) UpdateAccount(context.Context, string, models.AccountPatch) (*models.Account, error) {
	return nil, nil
}
func (p *mockPool) DeleteAccount(context.Context, string) error { return nil }
func (p *mockPool) RefreshAccountQuota(context.Context, string) (*models.Account, error) {
	return nil, nil
}
func (p *mockPool) RefreshAllQuotas(context.Context) (*models.QuotaSummary, error) { return nil, nil }
func (p *mockPool) GetQuotaSummary(context.Context) (*models.QuotaSummary, error) {
	return &models.QuotaSummary{AccountsTotal: len(p.accounts)}, nil
}
func (p *mockPool) AcquireAccessToken(context.Context, string) (string, string, error) {
	return "tok", "", nil
}
func (p *mockPool) CanScheduleAccount(*models.Account, models.AcceleratorType) (bool, string) {
	return true, ""
}
func (p *mockPool) MarkGPUEntitlement(context.Context, string, models.AcceleratorType, models.GPUEntitlementStatus, string, time.Duration) error {
	return nil
}
func (p *mockPool) MarkAccountHealth(context.Context, string, models.AccountHealth, string, time.Duration) error {
	return nil
}
func (p *mockPool) RecordAccountCUBurn(context.Context, string, float64) error { return nil }
func (p *mockPool) ListProxies(context.Context) ([]*models.ProxyConfig, error) { return nil, nil }
func (p *mockPool) SaveProxy(context.Context, *models.ProxyConfig) (*models.ProxyConfig, error) {
	return nil, nil
}
func (p *mockPool) DeleteProxy(context.Context, string) error { return nil }
func (p *mockPool) TestProxy(context.Context, string) (*models.ProxyProbeResult, error) {
	return nil, nil
}
func (p *mockPool) ScanLocalCredentials(context.Context) ([]models.DiscoveredLocalCredential, error) {
	return nil, nil
}
func (p *mockPool) ImportCredentials(context.Context, models.CredentialImportRequest) ([]*models.Account, error) {
	return nil, nil
}
func (p *mockPool) ExportVaultBundle(context.Context, models.CredentialExportRequest) (*models.CredentialBundle, error) {
	return nil, nil
}

func createDirSymlinkOrJunction(t *testing.T, target, linkPath string) bool {
	t.Helper()
	if err := os.Symlink(target, linkPath); err == nil {
		return true
	}
	if goruntime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "mklink", "/J", linkPath, target)
		if out, err := cmd.CombinedOutput(); err == nil {
			return true
		} else {
			t.Logf("mklink /J failed: %v (%s)", err, string(out))
		}
	}
	return false
}

// ============================================================================
// Tests
// ============================================================================

func TestCuratedTemplatesCatalogThreeCoreRealAppsAndValidation(t *testing.T) {
	templates := appstore.CuratedTemplates()
	if len(templates) != 5 {
		t.Fatalf("expected EXACTLY 5 real core built-in templates (llm-chat, wangp, comfyui, unsloth-studio, jupyterlab), got %d", len(templates))
	}

	// Verify old stub built-ins are completely gone!
	for _, removed := range []string{"wan-video", "flux-paint", "voice-studio", "open-webui", "custom-github"} {
		if appstore.IsBuiltinTemplateID(removed) {
			t.Fatalf("stub built-in %q must be removed from built-in catalog", removed)
		}
		if _, ok := appstore.FindTemplate(removed); ok {
			t.Fatalf("FindTemplate(%q) must return false after removing stub built-ins", removed)
		}
	}

	// 1. Flagship llm-chat (Uncensored LLM /v1 Deployer) at index 0.
	if templates[0].ID != "llm-chat" ||
		!strings.Contains(templates[0].NameZh, "无审查") ||
		templates[0].Category != models.AppCategoryWorkspace ||
		templates[0].DefaultRemotePort != 11434 ||
		templates[0].DefaultLocalPort != 7860 {
		t.Fatalf("index 0 must be flagship llm-chat card, got %+v", templates[0])
	}

	// 2. WanGP at index 1 with MiniMax H3, LTX-2.5, Wan 2.2, and 2026 Image Pack profiles.
	wangp := templates[1]
	if wangp.ID != "wangp" ||
		wangp.Category != models.AppCategoryVideo ||
		wangp.DefaultRemotePort != 7861 ||
		wangp.DefaultLocalPort != 7861 ||
		!wangp.SupportsCustomModelURL ||
		len(wangp.ModelOptions) != 4 ||
		wangp.ModelOptions[0].ID != "minimax-h3" {
		t.Fatalf("index 1 must be real WanGP studio with 4 profiles starting with minimax-h3, got %+v", wangp)
	}

	// 3. ComfyUI at index 2 with 2026 Comfy-Org model options & real HF download URLs.
	comfy := templates[2]
	if comfy.ID != "comfyui" ||
		comfy.Category != models.AppCategoryImage ||
		comfy.Icon != "🎨" ||
		comfy.DefaultRemotePort != 8188 ||
		comfy.DefaultLocalPort != 8188 ||
		!comfy.SupportsCustomModelURL ||
		len(comfy.ModelOptions) != 4 {
		t.Fatalf("index 2 must be real comfyui card with 4 model options, got %+v", comfy)
	}
	if comfy.ModelOptions[0].ID != "blank" || comfy.ModelOptions[0].DownloadURL != "" {
		t.Fatalf("comfyui option 0 must be blank (0 GB preload), got %+v", comfy.ModelOptions[0])
	}
	if comfy.ModelOptions[1].ID != "z-image-turbo" ||
		comfy.ModelOptions[1].DownloadURL != "https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/diffusion_models/z_image_turbo_bf16.safetensors" ||
		comfy.ModelOptions[1].TargetSubdir != "models/diffusion_models" ||
		comfy.ModelOptions[1].Filename != "z_image_turbo_bf16.safetensors" {
		t.Fatalf("unexpected z-image-turbo model option: %+v", comfy.ModelOptions[1])
	}
	if comfy.ModelOptions[2].ID != "qwen-image-fp8" ||
		comfy.ModelOptions[2].RecommendedGPU != models.AcceleratorL4 ||
		comfy.ModelOptions[2].DownloadURL != "https://huggingface.co/Comfy-Org/Qwen-Image/resolve/main/split_files/diffusion_models/qwen_image_2512_fp8_e4m3fn.safetensors" {
		t.Fatalf("unexpected qwen-image-fp8 model option: %+v", comfy.ModelOptions[2])
	}
	if comfy.ModelOptions[3].ID != "flux2-dev-fp8" ||
		comfy.ModelOptions[3].RecommendedGPU != models.AcceleratorG4 ||
		comfy.ModelOptions[3].DownloadURL != "https://huggingface.co/Comfy-Org/flux2-dev/resolve/main/split_files/diffusion_models/flux2_dev_fp8mixed.safetensors" {
		t.Fatalf("unexpected flux2-dev-fp8 model option: %+v", comfy.ModelOptions[3])
	}

	// 4. Unsloth Studio at index 3.
	if templates[3].ID != "unsloth-studio" ||
		templates[3].Category != models.AppCategoryWorkspace ||
		templates[3].DefaultRemotePort != 8890 ||
		templates[3].DefaultLocalPort != 8890 {
		t.Fatalf("index 3 must be unsloth-studio, got %+v", templates[3])
	}

	// 5. JupyterLab at index 4 with real PyTorch+CUDA option & token/XSRF hardening.
	if templates[4].ID != "jupyterlab" ||
		templates[4].Category != models.AppCategoryWorkspace ||
		templates[4].DefaultRemotePort != 8888 ||
		templates[4].DefaultLocalPort != 8889 ||
		len(templates[4].ModelOptions) != 1 ||
		templates[4].ModelOptions[0].ID != "default" {
		t.Fatalf("index 4 must be jupyterlab with default option, got %+v", templates[4])
	}

	for _, tpl := range templates {
		fullTpl, ok := appstore.FindTemplate(tpl.ID)
		if !ok || strings.TrimSpace(fullTpl.BootstrapScript) == "" {
			t.Fatalf("template %s missing BootstrapScript", tpl.ID)
		}
		if strings.Contains(fullTpl.BootstrapScript, "check=False") {
			t.Fatalf("template %s MUST NOT swallow subprocess failures with check=False", tpl.ID)
		}
		if !strings.Contains(fullTpl.BootstrapScript, "_wait_http_ready") {
			t.Fatalf("template %s MUST verify HTTP readiness on 127.0.0.1:<port>", tpl.ID)
		}
	}

	// Verify ComfyUI bootstrap actually downloads model weights, checks HTTP 200 & non-zero size!
	comfyFull, _ := appstore.FindTemplate("comfyui")
	for _, requiredSnippet := range []string{
		"COLABHIVE_MODEL_DOWNLOAD_URL",
		"_provision_model_weight",
		"status_code != 200",
		"total_written <= 0",
		"os.replace(tmp_path, dest_path)",
	} {
		if !strings.Contains(comfyFull.BootstrapScript, requiredSnippet) {
			t.Fatalf("comfyui bootstrap missing real model provisioning check %q", requiredSnippet)
		}
	}

	// Verify JupyterLab retains XSRF protection and per-instance random token auth!
	jup, _ := appstore.FindTemplate("jupyterlab")
	for _, forbidden := range []string{"disable_check_xsrf", `--ServerApp.token=",`, "--ServerApp.token=''", `--ServerApp.password=",`} {
		if strings.Contains(jup.BootstrapScript, forbidden) {
			t.Fatalf("jupyterlab bootstrap MUST NOT contain insecure flag %q", forbidden)
		}
	}
	if !strings.Contains(jup.BootstrapScript, "secrets.token_urlsafe(24)") {
		t.Fatalf("jupyterlab bootstrap MUST generate per-instance random token via secrets.token_urlsafe(24)")
	}

	// Verify ModelDownloadURL strict validation (only huggingface.co / civitai.com, zero shell metacharacters).
	cleanHF, hfFile, err := appstore.ValidateAndSanitizeModelDownloadURL("https://huggingface.co/stabilityai/sdxl-turbo/resolve/main/sd_xl_turbo_1.0_fp16.safetensors")
	if err != nil || cleanHF == "" || hfFile != "sd_xl_turbo_1.0_fp16.safetensors" {
		t.Fatalf("expected valid HF URL, got url=%q file=%q err=%v", cleanHF, hfFile, err)
	}
	cleanCiv, civFile, err := appstore.ValidateAndSanitizeModelDownloadURL("https://civitai.com/api/download/models/123456")
	if err != nil || cleanCiv == "" || !strings.HasSuffix(civFile, ".safetensors") {
		t.Fatalf("expected valid Civitai URL, got url=%q file=%q err=%v", cleanCiv, civFile, err)
	}
	badModelURLs := []string{
		"",
		"http://huggingface.co/a/b/c.safetensors",
		"https://evil.example.com/model.safetensors",
		"https://huggingface.co.evil.com/a/b.safetensors",
		"https://user:pass@huggingface.co/a/b.safetensors",
		"https://huggingface.co/a/b.safetensors;rm -rf /",
		"https://huggingface.co/a/b.safetensors $(id)",
		"https://huggingface.co/a/../etc/passwd",
		"https://huggingface.co:8443/a/b.safetensors",
		"https://civitai.com/api/download/models/1#frag",
		"https://civitai.com/api/download/models/1?a=1&b=2",
	}
	for _, bad := range badModelURLs {
		if _, _, mErr := appstore.ValidateAndSanitizeModelDownloadURL(bad); mErr == nil {
			t.Fatalf("expected error for invalid/unsafe model_download_url %q", bad)
		}
	}

	// Verify custom GitHub URL & command injection sanitization.
	cleanURL, quoted, err := appstore.ValidateAndSanitizeGitHubRepoURL("https://github.com/comfyanonymous/ComfyUI")
	if err != nil || cleanURL != "https://github.com/comfyanonymous/ComfyUI.git" || quoted != "'https://github.com/comfyanonymous/ComfyUI.git'" {
		t.Fatalf("unexpected sanitized repo result: url=%q quoted=%q err=%v", cleanURL, quoted, err)
	}
	for _, bad := range []string{
		"", "http://github.com/owner/repo", "https://gitlab.com/owner/repo",
		"https://github.com/owner/repo;rm -rf /", "https://github.com/owner/../etc/passwd",
	} {
		if _, _, bErr := appstore.ValidateAndSanitizeGitHubRepoURL(bad); bErr == nil {
			t.Fatalf("expected error for invalid repo URL %q", bad)
		}
	}
	for _, badCmd := range []string{"python3 app.py; id", "python3 app.py && id", "bash -c whoami"} {
		if _, cErr := appstore.ValidateCustomStartCommand(badCmd); cErr == nil {
			t.Fatalf("expected error for unsafe command %q", badCmd)
		}
	}
}

func TestExplicitGPUAccountAndModelSelectionLaunch(t *testing.T) {
	ctx := context.Background()
	accounts := []*models.Account{
		{ID: "acc_t4", Email: "t4@example.com", Alias: "Free T4", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
		{ID: "acc_g4", Email: "g4@example.com", Alias: "Ultra G4", SubscriptionTier: models.TierAIUltra, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	mgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               false,
	})
	defer mgr.Close()

	// 1. Explicit PreferredGPU=G4 + AccountID="acc_g4" + SelectedModelID="qwen-image-fp8":
	//    MUST honor G4 (NOT override to L4), pass AccountSelector="id:acc_g4", download exact HF URL,
	//    and record SelectedModelID, SelectedModelName, ModelDownloadURL, CheckpointDir on AppInstance!
	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		PreferredGPU:       models.AcceleratorG4,
		AccountID:          "acc_g4",
		SelectedModelID:    "qwen-image-fp8",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(comfyui qwen-image-fp8 on G4/acc_g4): %v", err)
	}
	if inst.AssignedGPU != models.AcceleratorG4 || inst.AccountID != "acc_g4" {
		t.Fatalf("expected explicit G4 & acc_g4, got gpu=%s acc=%s", inst.AssignedGPU, inst.AccountID)
	}
	if inst.SelectedModelID != "qwen-image-fp8" ||
		!strings.Contains(inst.SelectedModelName, "Qwen-Image FP8") ||
		inst.ModelDownloadURL != "https://huggingface.co/Comfy-Org/Qwen-Image/resolve/main/split_files/diffusion_models/qwen_image_2512_fp8_e4m3fn.safetensors" ||
		inst.CheckpointDir != "/content/ComfyUI/output" {
		t.Fatalf("unexpected model/checkpoint metadata on AppInstance: %+v", inst)
	}
	rt.mu.Lock()
	alloc1 := rt.createCalls[len(rt.createCalls)-1]
	boot1 := rt.lastBootstrapReq
	rt.mu.Unlock()
	if alloc1.Accelerator != models.AcceleratorG4 || alloc1.AccountSelector != "id:acc_g4" {
		t.Fatalf("expected CreateSession Accelerator=G4 AccountSelector='id:acc_g4', got %+v", alloc1)
	}
	if boot1 == nil ||
		boot1.Env["COLABHIVE_MODEL_DOWNLOAD_URL"] != inst.ModelDownloadURL ||
		boot1.Env["COLABHIVE_MODEL_SUBDIR"] != "models/diffusion_models" ||
		boot1.Env["COLABHIVE_MODEL_FILENAME"] != "qwen_image_2512_fp8_e4m3fn.safetensors" ||
		boot1.IdleTimeoutSec < 900 {
		t.Fatalf("unexpected comfyui bootstrap env/timeout: %+v", boot1)
	}

	// 2. ComfyUI without PreferredGPU but with SelectedModelID="qwen-image-fp8" falls back to model's RecommendedGPU (L4).
	instL4, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		SelectedModelID:    "qwen-image-fp8",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(comfyui qwen-image default GPU): %v", err)
	}
	if instL4.AssignedGPU != models.AcceleratorL4 {
		t.Fatalf("expected fallback to model RecommendedGPU=L4, got %s", instL4.AssignedGPU)
	}

	// 2b. Verify WanGP & Unsloth Studio launch with their dedicated checkpoint dirs!
	wangpInst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "wangp",
		SelectedModelID:    "minimax-h3",
		PreferredGPU:       models.AcceleratorG4,
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(wangp minimax-h3): %v", err)
	}
	if wangpInst.CheckpointDir != "/content/WanGP/outputs" || wangpInst.SelectedModelID != "minimax-h3" {
		t.Fatalf("unexpected WanGP instance metadata: %+v", wangpInst)
	}
	unslothInst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "unsloth-studio",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(unsloth-studio): %v", err)
	}
	if unslothInst.CheckpointDir != "/content/unsloth_outputs" {
		t.Fatalf("unexpected Unsloth Studio checkpoint dir: %+v", unslothInst)
	}

	// 3. ComfyUI blank launch passes empty COLABHIVE_MODEL_DOWNLOAD_URL (honest blank mode!).
	instBlank, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		SelectedModelID:    "blank",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(comfyui blank): %v", err)
	}
	if instBlank.SelectedModelID != "blank" || instBlank.ModelDownloadURL != "" {
		t.Fatalf("expected blank comfyui with empty ModelDownloadURL, got %+v", instBlank)
	}
	rt.mu.Lock()
	bootBlank := rt.lastBootstrapReq
	rt.mu.Unlock()
	if bootBlank.Env["COLABHIVE_MODEL_DOWNLOAD_URL"] != "" {
		t.Fatalf("blank comfyui must NOT pass COLABHIVE_MODEL_DOWNLOAD_URL, got %q", bootBlank.Env["COLABHIVE_MODEL_DOWNLOAD_URL"])
	}

	// 4. Custom Civitai URL works; invalid URL fails BEFORE allocating any GPU session!
	instCustom, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		ModelDownloadURL:   "https://civitai.com/api/download/models/987654",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(comfyui custom civitai): %v", err)
	}
	if instCustom.ModelDownloadURL != "https://civitai.com/api/download/models/987654" {
		t.Fatalf("expected custom model URL recorded, got %+v", instCustom)
	}

	rt.mu.Lock()
	beforeBad := len(rt.createCalls)
	rt.mu.Unlock()
	if _, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:       "comfyui",
		ModelDownloadURL: "https://evil.attacker.com/backdoor.safetensors;id",
	}); err == nil {
		t.Fatalf("expected invalid model_download_url to be rejected")
	}
	rt.mu.Lock()
	afterBad := len(rt.createCalls)
	rt.mu.Unlock()
	if afterBad != beforeBad {
		t.Fatalf("invalid model_download_url must fail BEFORE allocating any GPU session")
	}

	// 5. JupyterLab records real CheckpointDir="/content".
	jupInst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "jupyterlab",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(jupyterlab): %v", err)
	}
	if jupInst.CheckpointDir != "/content" || jupInst.SelectedModelID != "default" {
		t.Fatalf("expected jupyterlab CheckpointDir=/content and SelectedModelID=default, got %+v", jupInst)
	}
}

func TestPluginRegistryLoadInstallSymlinkConfinementAnd256KiBCap(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	homeDir := t.TempDir()
	t.Setenv("COLABHIVE_HOME", homeDir)

	homePluginsDir := filepath.Join(homeDir, "plugins")
	if err := os.MkdirAll(homePluginsDir, 0o700); err != nil {
		t.Fatalf("mkdir home plugins: %v", err)
	}
	homePluginJSON := `{
		"id": "home-asr-studio",
		"name": "Home ASR Studio",
		"name_zh": "家目录语音识别插件",
		"category": "audio",
		"repo_url": "https://github.com/openai/whisper",
		"start_command": "python3 app.py --port 7870",
		"remote_port": 7870
	}`
	if err := os.WriteFile(filepath.Join(homePluginsDir, "home-asr-studio.json"), []byte(homePluginJSON), 0o600); err != nil {
		t.Fatalf("write home plugin: %v", err)
	}

	accounts := []*models.Account{
		{ID: "acc_p1", Email: "p1@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	mgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:   dataDir,
		CloudMode: false,
		DemoMode:  true,
	})
	defer mgr.Close()

	// 1. Runtime loading from COLABHIVE_HOME/plugins/*.json alongside 5 core apps.
	tpls := mgr.ListTemplates()
	if len(tpls) < 5 || tpls[0].ID != "llm-chat" || tpls[1].ID != "wangp" || tpls[2].ID != "comfyui" || tpls[3].ID != "unsloth-studio" || tpls[4].ID != "jupyterlab" {
		t.Fatalf("first 5 templates must be core built-ins, got %+v", tpls)
	}
	if _, ok := mgr.GetTemplate("home-asr-studio"); !ok {
		t.Fatalf("expected home-asr-studio plugin to load at runtime")
	}

	// 2. Reject invalid manifests & built-in collisions.
	badManifests := []appstore.AppPluginManifest{
		{ID: "Bad_Upper", Name: "X", Category: models.AppCategoryAudio, RemotePort: 7865},
		{ID: "../escape", Name: "X", Category: models.AppCategoryAudio, RemotePort: 7865},
		{ID: "llm-chat", Name: "Hijack Builtin", Category: models.AppCategoryWorkspace, RemotePort: 7865},
		{ID: "comfyui", Name: "Hijack ComfyUI", Category: models.AppCategoryImage, RemotePort: 8188},
		{ID: "jupyterlab", Name: "Hijack Jupyter", Category: models.AppCategoryWorkspace, RemotePort: 8888},
		{ID: "valid-id", Name: "Privileged Port", Category: models.AppCategoryImage, RemotePort: 80},
		{ID: "valid-id", Name: "Shell Inject", Category: models.AppCategoryImage, RemotePort: 7865, StartCommand: "python3 app.py; id"},
		{ID: "valid-id", Name: "Bad Ckpt Dir", Category: models.AppCategoryImage, RemotePort: 7865, CheckpointDir: "/etc/passwd"},
	}
	for _, bad := range badManifests {
		if _, err := mgr.InstallPlugin(bad); err == nil {
			t.Fatalf("expected InstallPlugin rejection for %+v", bad)
		}
	}

	// 3. Enforce aligned 256 KiB cap on serialized JSON before writing to disk!
	hugeHighlights := make([]string, 12)
	for i := range hugeHighlights {
		hugeHighlights[i] = strings.Repeat("H", 25000) // ~300 KiB total > 256 KiB!
	}
	if _, err := mgr.InstallPlugin(appstore.AppPluginManifest{
		ID:         "oversized-plugin",
		Name:       "Oversized Plugin",
		Category:   models.AppCategoryCustom,
		RemotePort: 7890,
		Highlights: hugeHighlights,
	}); !errors.Is(err, appstore.ErrPluginManifestTooLarge) {
		t.Fatalf("expected ErrPluginManifestTooLarge for >256KiB serialized manifest, got %v", err)
	}

	// 4. Install valid community plugin, launch, verify checkpoint_dir recorded, then delete.
	installed, err := mgr.InstallPlugin(appstore.AppPluginManifest{
		ID:            "kokoro-tts",
		Name:          "Kokoro 82M TTS",
		NameZh:        "Kokoro 语音合成",
		Category:      models.AppCategoryAudio,
		RepoURL:       "https://github.com/hexgrad/kokoro",
		StartCommand:  "python3 app.py",
		RemotePort:    7864,
		CheckpointDir: "/content/checkpoints/kokoro-tts",
	})
	if err != nil {
		t.Fatalf("InstallPlugin(kokoro-tts) failed: %v", err)
	}
	if installed.ID != "kokoro-tts" || installed.DefaultRemotePort != 7864 {
		t.Fatalf("unexpected installed template: %+v", installed)
	}
	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "kokoro-tts",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(kokoro-tts) failed: %v", err)
	}
	if inst.CheckpointDir != "/content/checkpoints/kokoro-tts" || inst.CustomRepoURL != "https://github.com/hexgrad/kokoro.git" {
		t.Fatalf("unexpected launched plugin instance: %+v", inst)
	}

	if err := mgr.DeletePlugin("comfyui"); err == nil {
		t.Fatalf("expected error deleting built-in comfyui")
	}
	if err := mgr.DeletePlugin("kokoro-tts"); err != nil {
		t.Fatalf("DeletePlugin(kokoro-tts) failed: %v", err)
	}
	if err := mgr.DeletePlugin("kokoro-tts"); !errors.Is(err, appstore.ErrAppPluginNotFound) {
		t.Fatalf("expected ErrAppPluginNotFound on second delete, got %v", err)
	}

	// 5. P1 Security: Symlinked `plugins` directory itself MUST be rejected on read, install, AND delete!
	symDataDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideManifest := `{"id":"escaped-plugin","name":"Escaped","category":"custom","remote_port":7899}`
	if err := os.WriteFile(filepath.Join(outsideDir, "escaped-plugin.json"), []byte(outsideManifest), 0o600); err != nil {
		t.Fatalf("write outside plugin: %v", err)
	}
	symPluginsDir := filepath.Join(symDataDir, "plugins")
	if !createDirSymlinkOrJunction(t, outsideDir, symPluginsDir) {
		t.Fatalf("failed to create symlink/junction to verify plugins directory confinement")
	}
	symMgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                symDataDir,
		DisableUserHomePlugins: true,
		DemoMode:               true,
	})
	defer symMgr.Close()

	// Read MUST refuse symlinked plugins dir.
	if _, ok := symMgr.GetTemplate("escaped-plugin"); ok {
		t.Fatalf("SECURITY FAILURE: loaded plugin from symlinked plugins directory!")
	}
	// Install MUST refuse symlinked plugins dir.
	if _, err := symMgr.InstallPlugin(appstore.AppPluginManifest{
		ID:         "write-escape",
		Name:       "Write Escape",
		Category:   models.AppCategoryCustom,
		RemotePort: 7898,
	}); err == nil {
		t.Fatalf("SECURITY FAILURE: InstallPlugin wrote through symlinked plugins directory!")
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, "write-escape.json")); !os.IsNotExist(statErr) {
		t.Fatalf("SECURITY FAILURE: file created inside symlink target dir!")
	}
	// Delete MUST refuse symlinked plugins dir (leaving outside file untouched).
	if err := symMgr.DeletePlugin("escaped-plugin"); err == nil {
		t.Fatalf("SECURITY FAILURE: DeletePlugin operated through symlinked plugins directory!")
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, "escaped-plugin.json")); statErr != nil {
		t.Fatalf("SECURITY FAILURE: outside file was removed through symlinked plugins dir!")
	}
}

func TestAppLaunchLocalPortBridgeProxyAndCleanShutdown(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	hub := events.NewHub()

	accounts := []*models.Account{
		{
			ID:               "acc_1",
			Email:            "creator@private-mail.com",
			Alias:            "创作免费号 #1",
			Provider:         models.ProviderColab,
			SubscriptionTier: models.TierFree,
			Health:           models.HealthHealthy,
			Enabled:          true,
		},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	ckpt := checkpoint.NewManagerWithConfig(checkpoint.Config{DataDir: dataDir, DemoMode: true}, pool, rt, hub)
	mgr := appstore.NewManager(pool, rt, ckpt, hub, appstore.Config{
		DataDir:                dataDir,
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               true,
	})
	defer mgr.Close()

	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		SelectedModelID:    "z-image-turbo",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(comfyui) failed: %v", err)
	}
	if inst.State != models.AppStateRunning || !inst.LocalBridgeActive || inst.LocalPort <= 0 {
		t.Fatalf("expected running instance with active isolated local bridge, got %+v", inst)
	}
	parsedA, pErr := url.Parse(inst.LocalURL)
	if pErr != nil || parsedA.Hostname() == "" || parsedA.Hostname() == "127.0.0.1" {
		t.Fatalf("expected per-instance isolated loopback host distinct from 127.0.0.1, got LocalURL=%q", inst.LocalURL)
	}

	// Verify sibling Studio gets a DIFFERENT isolated loopback host (true browser origin & cookie jar isolation)!
	instB, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "jupyterlab",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(jupyterlab) failed: %v", err)
	}
	parsedB, pErrB := url.Parse(instB.LocalURL)
	if pErrB != nil || parsedB.Hostname() == "" || parsedB.Hostname() == parsedA.Hostname() || parsedB.Hostname() == "127.0.0.1" {
		t.Fatalf("expected sibling Studio on distinct isolated loopback host: A=%q B=%q", inst.LocalURL, instB.LocalURL)
	}
	_, _ = mgr.Stop(ctx, instB.ID)

	// Verify HTTP 200 through isolated LocalURL!
	resp, err := http.Get(inst.LocalURL + "/")
	if err != nil {
		t.Fatalf("GET %s failed: %v", inst.LocalURL, err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(bodyBytes), "ComfyUI") {
		t.Fatalf("expected interactive ComfyUI studio HTML, got status=%d", resp.StatusCode)
	}

	// Stop instance & verify its isolated TCP listener is closed cleanly!
	boundAddr := parsedA.Host
	stopped, err := mgr.Stop(ctx, inst.ID)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if stopped.State != models.AppStateStopped || stopped.LocalBridgeActive || stopped.LocalPort != 0 {
		t.Fatalf("expected stopped state with inactive bridge, got %+v", stopped)
	}

	conn, dialErr := net.DialTimeout("tcp", boundAddr, 250*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatalf("expected isolated localhost bridge %s to be closed after Stop()", boundAddr)
	}
}

func TestCloudModeLiveLaunchFailClosed422AndSameOriginProxyIsolation(t *testing.T) {
	ctx := context.Background()
	accounts := []*models.Account{
		{
			ID:               "acc_cloud",
			Email:            "cloud@example.com",
			SubscriptionTier: models.TierPro,
			Health:           models.HealthHealthy,
			Enabled:          true,
		},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)

	// 1. P1 Cloud Mode Fail-Closed: live cloud mode without IsolatedProxyOrigin MUST reject Launch()
	//    with ErrIsolatedOriginRequired BEFORE allocating ANY billing GPU VM!
	liveCloudMgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              true,
		DemoMode:               false,
	})
	defer liveCloudMgr.Close()

	if _, err := liveCloudMgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui"}); !errors.Is(err, appstore.ErrIsolatedOriginRequired) {
		t.Fatalf("expected ErrIsolatedOriginRequired on live cloud Launch without isolated origin, got %v", err)
	}
	rt.mu.Lock()
	allocCount := len(rt.createCalls)
	rt.mu.Unlock()
	if allocCount != 0 {
		t.Fatalf("live cloud fail-closed MUST NOT allocate any GPU VM, got %d CreateSession calls", allocCount)
	}

	// 2. Live cloud mode WITH IsolatedProxyOrigin configured permits Launch and emits isolated WebProxyURL.
	isoCloudMgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              true,
		DemoMode:               false,
		IsolatedProxyOrigin:    "https://studio.isolated.example.com",
	})
	defer isoCloudMgr.Close()
	isoInst, err := isoCloudMgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui"})
	if err != nil {
		t.Fatalf("Launch with IsolatedProxyOrigin failed: %v", err)
	}
	if isoInst.LocalBridgeActive || !strings.HasPrefix(isoInst.WebProxyURL, "https://studio.isolated.example.com/apps/proxy/") {
		t.Fatalf("unexpected isolated cloud instance: %+v", isoInst)
	}

	// 3. Same-origin `/apps/proxy/{id}/` returns HTTP 422 in cloud demo mode, protected mode, and auth-header requests.
	cloudDemoMgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              true,
		DemoMode:               true,
	})
	defer cloudDemoMgr.Close()
	demoInst, err := cloudDemoMgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui"})
	if err != nil {
		t.Fatalf("Launch(cloud demo): %v", err)
	}
	rec := httptest.NewRecorder()
	cloudDemoMgr.ServeProxyHTTP(rec, httptest.NewRequest(http.MethodGet, demoInst.WebProxyURL, nil), demoInst.ID, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected HTTP 422 on cloud WebProxyURL, got %d", rec.Code)
	}

	protMgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               false,
		ProtectedMode:          true,
	})
	defer protMgr.Close()
	protInst, err := protMgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui", PreferredLocalPort: 0})
	if err != nil {
		t.Fatalf("Launch(protected): %v", err)
	}
	rec2 := httptest.NewRecorder()
	protMgr.ServeProxyHTTP(rec2, httptest.NewRequest(http.MethodGet, protInst.WebProxyURL, nil), protInst.ID, "")
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected HTTP 422 on protected WebProxyURL, got %d", rec2.Code)
	}
}

func TestLocalBridgeGuardCrossSiteCookiesAndControlPlaneScrubbing(t *testing.T) {
	ctx := context.Background()
	accounts := []*models.Account{
		{ID: "acc_guard", Email: "guard@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	rt.realRemoteMode = true
	rt.remoteStatusCode = http.StatusFound
	rt.remoteReplyBody = "redirecting"
	rt.remoteHeaders = [][2]string{
		{"Location", "/studio/login"},
		{"Set-Cookie", "sid=alpha; Path=/"},
		{"Set-Cookie", "_xsrf=beta; HttpOnly"},
		{"Connection", "keep-alive, X-Hop-Secret"},
		{"X-Hop-Secret", "must-strip"},
		{"X-App-Custom", "preserved"},
	}

	mgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               false,
	})
	defer mgr.Close()

	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "jupyterlab", PreferredLocalPort: 0})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	noRedirectClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	parsedLocal, pErr := url.Parse(inst.LocalURL)
	if pErr != nil || parsedLocal.Host == "" {
		t.Fatalf("parse inst.LocalURL %q: %v", inst.LocalURL, pErr)
	}

	// 1. DNS rebinding, cross-origin, wrong-scheme Origin, and Sec-Fetch-Site: cross-site MUST return 403!
	//    Notice Origin & Sec-Fetch-Site test cases pass the VALID isolated `parsedLocal.Host` so they
	//    genuinely exercise the Origin & Sec-Fetch-Site guards (rather than failing early on Host!).
	badCases := []struct {
		name      string
		host      string
		origin    string
		fetchSite string
	}{
		{"evil host", fmt.Sprintf("evil.attacker.com:%d", inst.LocalPort), "", ""},
		{"wrong loopback port host", "127.0.0.1:7860", "", ""},
		{"cross-origin header", parsedLocal.Host, "https://evil.attacker.com", ""},
		{"wrong-port loopback origin", parsedLocal.Host, "http://127.0.0.1:7860", ""},
		{"https origin on http bridge", parsedLocal.Host, "https://" + parsedLocal.Host, ""},
		{"null origin", parsedLocal.Host, "null", ""},
		{"cross-site sec-fetch-site", parsedLocal.Host, "", "cross-site"},
	}
	for _, tc := range badCases {
		req, _ := http.NewRequest(http.MethodGet, inst.LocalURL+"/api/test", nil)
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
		}
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatalf("%s request failed: %v", tc.name, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: expected 403 Forbidden, got %d", tc.name, resp.StatusCode)
		}
	}

	// 2. Valid same-origin request on this instance's isolated loopback host (`127.a.b.c:<port>`)
	//    preserves native unmodified `_xsrf` and app cookies (no cookie renaming!) while rejecting
	//    shared `127.0.0.1:<port>` & `localhost:<port>` Hosts, preserving Jupyter's `?token=<random>`,
	//    and stripping ONLY ColabHive control-plane credentials!
	// Shared 127.0.0.1 / localhost Host MUST be rejected with 403 Forbidden!
	for _, sharedHost := range []string{fmt.Sprintf("127.0.0.1:%d", inst.LocalPort), fmt.Sprintf("localhost:%d", inst.LocalPort)} {
		badHostReq, _ := http.NewRequest(http.MethodGet, inst.LocalURL+"/api/test", nil)
		badHostReq.Host = sharedHost
		badResp, bErr := noRedirectClient.Do(badHostReq)
		if bErr != nil {
			t.Fatalf("sharedHost %s request failed: %v", sharedHost, bErr)
		}
		_ = badResp.Body.Close()
		if badResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 for shared host %s on isolated bridge %s, got %d", sharedHost, parsedLocal.Host, badResp.StatusCode)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, inst.LocalURL+"/files/my%20doc%2Freport.png?token=ch_adm_1&key=ch_sk_2&api_key=k3&sig=s4&exp=999&keep=ok", nil)
	req.Host = parsedLocal.Host
	req.Header.Set("Origin", "http://"+parsedLocal.Host)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Authorization", "Bearer ch_adm_supersecret")
	req.Header.Set("Cookie", "_xsrf=jup_xsrf_ok; app_session=app_sid_123")
	req.Header.Set("X-ColabHive-Key", "ch_sk_secret")
	req.Header.Set("x-api-key", "ch_sk_secret2")
	req.Header.Set("Range", "bytes=0-127")

	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("valid isolated loopback request failed: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/studio/login" {
		t.Fatalf("expected verbatim 302 Location=/studio/login, got status=%d loc=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if len(resp.Header.Values("Set-Cookie")) != 2 {
		t.Fatalf("expected 2 repeated Set-Cookie headers preserved, got %v", resp.Header.Values("Set-Cookie"))
	}
	if resp.Header.Get("X-Hop-Secret") != "" {
		t.Fatalf("hop-by-hop Connection-nominated header X-Hop-Secret was not stripped")
	}
	if resp.Header.Get("X-App-Custom") != "preserved" {
		t.Fatalf("expected X-App-Custom header preserved, got %q", resp.Header.Get("X-App-Custom"))
	}

	rt.mu.Lock()
	execReq := rt.lastBridgeExecReq
	rt.mu.Unlock()
	if execReq == nil {
		t.Fatalf("expected bridge ExecuteCode call")
	}
	if gotPath := execReq.Env["CH_BRIDGE_PATH"]; gotPath != "/files/my%20doc%2Freport.png?keep=ok" {
		t.Fatalf("expected escaped path + scrubbed query, got %q", gotPath)
	}
	hdrJSON := execReq.Env["CH_BRIDGE_HEADERS_JSON"]
	for _, leaked := range []string{"ch_adm_supersecret", "X-ColabHive-Key", "ch_sk_secret", "x-api-key"} {
		if strings.Contains(strings.ToLower(hdrJSON), strings.ToLower(leaked)) {
			t.Fatalf("control-plane credential leaked into proxied app headers: found %q in %s", leaked, hdrJSON)
		}
	}
	if !strings.Contains(hdrJSON, "_xsrf=jup_xsrf_ok") || !strings.Contains(hdrJSON, "app_sid_123") {
		t.Fatalf("expected native unmodified app Cookie header preserved on isolated host, got %s", hdrJSON)
	}
	if !strings.Contains(hdrJSON, "bytes=0-127") {
		t.Fatalf("expected Range header preserved, got %s", hdrJSON)
	}

	// Verify Jupyter's own random `?token=` query parameter IS preserved on the isolated local bridge!
	if got := appstore.SanitizeLocalBridgeRawQuery("token=jupRandToken_AbC123&key=ch_sk_drop&keep=1"); got != "token=jupRandToken_AbC123&keep=1" {
		t.Fatalf("expected Jupyter ?token= preserved on local bridge, got %q", got)
	}
	if got := appstore.SanitizeLocalBridgeRawQuery("token=ch_adm_must_strip&keep=1"); got != "keep=1" {
		t.Fatalf("expected ColabHive ch_adm_ token stripped on local bridge, got %q", got)
	}
}

func TestDirectRuntimeProxyWebSocketCookiesAndUnmappedPortFallback(t *testing.T) {
	ctx := context.Background()

	// 1. Real upstream HTTP + WebSocket server simulating a port-mapped runtime proxy.
	largePayload := bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 128*1024) // 512 KiB
	var mu sync.Mutex
	var sawToken, sawEscapedPath, sawQuery, sawCookie, sawAppAuth, sawColabKey, sawRange string
	var sawWSUpgrade, sawWSCookie, sawWSKey string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			mu.Lock()
			sawWSUpgrade = r.Header.Get("Upgrade")
			sawWSCookie = r.Header.Get("Cookie")
			sawWSKey = r.Header.Get("Sec-WebSocket-Key")
			mu.Unlock()

			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack unsupported", http.StatusInternalServerError)
				return
			}
			conn, rw, err := hj.Hijack()
			if err != nil {
				return
			}
			defer conn.Close()

			sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			accept := base64.StdEncoding.EncodeToString(sum[:])
			_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
			_ = rw.Flush()

			// Echo one line over the upgraded raw WebSocket TCP stream!
			line, _ := rw.ReadString('\n')
			_, _ = rw.WriteString("ECHO:" + line)
			_ = rw.Flush()
			return
		}

		mu.Lock()
		sawToken = r.Header.Get("X-Colab-Runtime-Proxy-Token")
		sawEscapedPath = r.URL.EscapedPath()
		sawQuery = r.URL.RawQuery
		sawCookie = r.Header.Get("Cookie")
		sawAppAuth = r.Header.Get("Authorization")
		sawColabKey = r.Header.Get("X-ColabHive-Key")
		sawRange = r.Header.Get("Range")
		mu.Unlock()

		w.Header().Add("Set-Cookie", "comfy_sid=xyz; Path=/")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(largePayload)
	}))
	defer upstream.Close()
	upstreamPort := upstream.Listener.Addr().(*net.TCPAddr).Port

	accounts := []*models.Account{
		{ID: "acc_direct", Email: "direct@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	rt.realRemoteMode = true
	rt.customProxyURL = upstream.URL
	rt.customProxyToken = "colab_proxy_cap_tok_123"
	rt.remoteReplyBody = `{"via":"python-bridge-fallback"}`

	mgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               false,
	})
	defer mgr.Close()

	// Part A: Exact port-mapped direct proxy streams 512 KiB, preserves app Cookies & app Authorization,
	//         strips ColabHive control-plane keys, AND upgrades live WebSockets cleanly!
	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		CustomRemotePort:   upstreamPort,
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	parsedDirect, pErrD := url.Parse(inst.LocalURL)
	if pErrD != nil || parsedDirect.Host == "" {
		t.Fatalf("parse direct inst.LocalURL %q: %v", inst.LocalURL, pErrD)
	}
	req, _ := http.NewRequest(http.MethodGet, inst.LocalURL+"/assets/my%20img%2F4k.bin?token=ch_adm_drop&key=k&api_key=ak&sig=s&exp=1&v=2", nil)
	req.Header.Set("Cookie", "comfy_sid=keep_me; _xsrf=tok123")
	req.Header.Set("Authorization", "token jupyter_app_token_ok")
	req.Header.Set("X-ColabHive-Key", "ch_sk_must_strip")
	req.Header.Set("Range", "bytes=0-1023")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("direct proxy GET failed: %v", err)
	}
	gotBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(gotBody) != len(largePayload) {
		t.Fatalf("expected 200 with %d bytes via direct ReverseProxy, got status=%d len=%d", len(largePayload), resp.StatusCode, len(gotBody))
	}
	if resp.Header.Get("Set-Cookie") != "comfy_sid=xyz; Path=/" {
		t.Fatalf("expected native unmodified Set-Cookie on isolated host, got %q", resp.Header.Get("Set-Cookie"))
	}
	mu.Lock()
	if sawToken != "colab_proxy_cap_tok_123" || sawEscapedPath != "/assets/my%20img%2F4k.bin" || sawQuery != "v=2" {
		t.Fatalf("direct proxy path/token mismatch: tok=%q path=%q q=%q", sawToken, sawEscapedPath, sawQuery)
	}
	if sawCookie != "comfy_sid=keep_me; _xsrf=tok123" || sawAppAuth != "token jupyter_app_token_ok" || sawColabKey != "" || sawRange != "bytes=0-1023" {
		t.Fatalf("direct proxy header contract mismatch: cookie=%q auth=%q colabKey=%q range=%q", sawCookie, sawAppAuth, sawColabKey, sawRange)
	}
	mu.Unlock()

	// Real WebSocket 101 Switching Protocols upgrade through isolated localBridge -> direct ReverseProxy!
	wsConn, err := net.DialTimeout("tcp", parsedDirect.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("dial local bridge for WebSocket: %v", err)
	}
	defer wsConn.Close()
	_ = wsConn.SetDeadline(time.Now().Add(3 * time.Second))
	wsReq := fmt.Sprintf(
		"GET /ws?clientId=comfy1 HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Origin: http://%s\r\n"+
			"Connection: Upgrade\r\n"+
			"Upgrade: websocket\r\n"+
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"Cookie: ws_session=live_ws_cookie\r\n"+
			"\r\n",
		parsedDirect.Host, parsedDirect.Host,
	)
	if _, err := io.WriteString(wsConn, wsReq); err != nil {
		t.Fatalf("write WS upgrade request: %v", err)
	}
	br := bufio.NewReader(wsConn)
	statusLine, err := br.ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "101") {
		t.Fatalf("expected HTTP 101 Switching Protocols over localBridge, got %q (err=%v)", statusLine, err)
	}
	for {
		line, rErr := br.ReadString('\n')
		if rErr != nil {
			t.Fatalf("read WS upgrade headers: %v", rErr)
		}
		if line == "\r\n" {
			break
		}
	}
	_, _ = io.WriteString(wsConn, "ping-comfy\n")
	echoLine, err := br.ReadString('\n')
	if err != nil || echoLine != "ECHO:ping-comfy\n" {
		t.Fatalf("expected bidirectional WS stream echo 'ECHO:ping-comfy\\n', got %q (err=%v)", echoLine, err)
	}
	mu.Lock()
	if !strings.EqualFold(sawWSUpgrade, "websocket") || sawWSCookie != "ws_session=live_ws_cookie" || sawWSKey != "dGhlIHNhbXBsZSBub25jZQ==" {
		t.Fatalf("WebSocket upgrade headers not preserved: upgrade=%q cookie=%q key=%q", sawWSUpgrade, sawWSCookie, sawWSKey)
	}
	mu.Unlock()

	// Part B: Unmapped direct proxy URL (upstream is on `upstreamPort`, app runs on DIFFERENT port 8188):
	//         resolveDirectRuntimeProxyBase MUST decline direct proxy and fall back cleanly to Python bridge!
	unmappedInst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		CustomRemotePort:   8188, // does NOT match upstreamPort!
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(unmapped): %v", err)
	}
	fbResp, err := http.Get(unmappedInst.LocalURL + "/queue")
	if err != nil {
		t.Fatalf("unmapped fallback GET failed: %v", err)
	}
	fbBody, _ := io.ReadAll(fbResp.Body)
	_ = fbResp.Body.Close()
	if fbResp.StatusCode != http.StatusOK || !strings.Contains(string(fbBody), "python-bridge-fallback") {
		t.Fatalf("unmapped direct proxy URL MUST decline direct route and fall back to Python bridge, got status=%d body=%s", fbResp.StatusCode, string(fbBody))
	}

	// Also verify unprefixed Colab URL (`https://gpu-t4-s-abc.us-west1.colab.googleusercontent.com`) declines direct proxy!
	rt.mu.Lock()
	rt.customProxyURL = "https://gpu-t4-s-abc.us-west1.colab.googleusercontent.com"
	rt.mu.Unlock()
	unprefInst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(unprefixed colab): %v", err)
	}
	fbResp2, err := http.Get(unprefInst.LocalURL + "/system_stats")
	if err != nil {
		t.Fatalf("unprefixed colab fallback GET failed: %v", err)
	}
	fbBody2, _ := io.ReadAll(fbResp2.Body)
	_ = fbResp2.Body.Close()
	if fbResp2.StatusCode != http.StatusOK || !strings.Contains(string(fbBody2), "python-bridge-fallback") {
		t.Fatalf("unprefixed Colab URL MUST decline direct route and fall back cleanly, got status=%d body=%s", fbResp2.StatusCode, string(fbBody2))
	}
}

func TestLiveModeBridgeFailuresReturn502NeverFakePreview(t *testing.T) {
	ctx := context.Background()
	accounts := []*models.Account{
		{ID: "acc_live", Email: "live@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	rt.realRemoteMode = true

	mgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               false,
	})
	defer mgr.Close()

	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui", PreferredLocalPort: 0})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	cases := []struct {
		name  string
		setup func()
	}{
		{"bridge execution error", func() { rt.bridgeExecErr = errors.New("kernel timeout") }},
		{"truncated stdout frame", func() { rt.bridgeExecErr = nil; rt.bridgeTruncated = true }},
		{"oversize >384KiB stdout frame", func() { rt.bridgeTruncated = false; rt.bridgeOversize = true }},
		{"upstream not ok envelope", func() { rt.bridgeOversize = false; rt.bridgeReturnNotOK = true }},
	}

	for _, tc := range cases {
		rt.mu.Lock()
		tc.setup()
		rt.mu.Unlock()

		resp, err := http.Get(inst.LocalURL + "/api/health")
		if err != nil {
			t.Fatalf("%s: GET failed: %v", tc.name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("%s: expected HTTP 502 Bad Gateway in live mode, got %d body=%s", tc.name, resp.StatusCode, string(body))
		}
		if strings.Contains(string(body), "<!DOCTYPE html>") {
			t.Fatalf("%s: live mode MUST NEVER fall back to fake preview HTML!", tc.name)
		}
	}
}

func TestStopPreSyncFailureProtectsVMAndForceOverrides(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	accounts := []*models.Account{
		{ID: "acc_stop_sync", Email: "sync@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	ckpt := checkpoint.NewManager(dataDir, pool, rt, nil)
	mgr := appstore.NewManager(pool, rt, ckpt, nil, appstore.Config{
		DataDir:                dataDir,
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               false,
	})
	defer mgr.Close()

	autoResume := true
	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		AutoResumeEnabled:  &autoResume,
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Inject remote ListFiles failure so pre-stop checkpoint sync fails!
	rt.mu.Lock()
	rt.listFilesErr = errors.New("network disconnect during final checkpoint")
	rt.mu.Unlock()

	// 1. Default Stop (force=false) MUST return ErrPreStopCheckpointSyncFailed WITHOUT tearing down VM or closing bridge!
	abortedInst, stopErr := mgr.Stop(ctx, inst.ID)
	if !errors.Is(stopErr, appstore.ErrPreStopCheckpointSyncFailed) {
		t.Fatalf("expected ErrPreStopCheckpointSyncFailed when pre-stop sync fails, got %v", stopErr)
	}
	if abortedInst == nil || abortedInst.State != models.AppStateRunning || abortedInst.SessionID != inst.SessionID || !abortedInst.LocalBridgeActive {
		t.Fatalf("expected running VM & active bridge kept intact on failed pre-stop sync, got %+v", abortedInst)
	}
	rt.mu.Lock()
	if len(rt.stopCalls) != 0 {
		t.Fatalf("Stop(force=false) MUST NOT tear down VM when pre-stop sync fails, got stopCalls=%v", rt.stopCalls)
	}
	rt.mu.Unlock()

	// 2. StopWithOptions(force=true) overrides the sync failure and cleanly shuts down the VM & bridge!
	forcedInst, forceErr := mgr.StopWithOptions(ctx, inst.ID, true)
	if forceErr != nil {
		t.Fatalf("StopWithOptions(force=true) failed: %v", forceErr)
	}
	if forcedInst.State != models.AppStateStopped || forcedInst.SessionID != "" || forcedInst.LocalBridgeActive {
		t.Fatalf("expected clean Stopped state after force stop, got %+v", forcedInst)
	}
	rt.mu.Lock()
	if len(rt.stopCalls) != 1 || rt.stopCalls[0] != inst.SessionID {
		t.Fatalf("expected StopSession called once on force stop, got %v", rt.stopCalls)
	}
	rt.mu.Unlock()
}

func TestStopTeardownErrorPreservesSessionForRetryAndUpdateConfig(t *testing.T) {
	ctx := context.Background()
	accounts := []*models.Account{
		{ID: "acc_stop", Email: "stop@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	mgr := appstore.NewManager(pool, rt, nil, nil, appstore.Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               true,
	})
	defer mgr.Close()

	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui", PreferredLocalPort: 0})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	origSessID := inst.SessionID

	badMin := 4
	if _, err := mgr.UpdateConfig(ctx, inst.ID, appstore.AppInstanceConfigPatch{IdleAutoStopMin: &badMin}); err == nil {
		t.Fatalf("expected error for idle_auto_stop_min=4 (< 5)")
	}
	goodMin := 60
	updated, err := mgr.UpdateConfig(ctx, inst.ID, appstore.AppInstanceConfigPatch{IdleAutoStopMin: &goodMin})
	if err != nil || updated.IdleAutoStopMin != 60 {
		t.Fatalf("expected persisted IdleAutoStopMin=60, got %+v err=%v", updated, err)
	}

	rt.mu.Lock()
	rt.stopSessionErr = errors.New("upstream Colab API 503 during unassign")
	rt.mu.Unlock()

	failedInst, stopErr := mgr.Stop(ctx, inst.ID)
	if stopErr == nil {
		t.Fatalf("expected Stop() to surface rt.StopSession error, got nil")
	}
	if failedInst == nil || failedInst.State != models.AppStateError || failedInst.SessionID != origSessID {
		t.Fatalf("expected AppStateError with intact SessionID=%q for retry, got %+v", origSessID, failedInst)
	}

	rt.mu.Lock()
	rt.stopSessionErr = nil
	rt.mu.Unlock()

	stoppedInst, retryErr := mgr.Stop(ctx, inst.ID)
	if retryErr != nil {
		t.Fatalf("retry Stop() failed: %v", retryErr)
	}
	if stoppedInst.State != models.AppStateStopped || stoppedInst.SessionID != "" {
		t.Fatalf("expected clean Stopped state after retry, got %+v", stoppedInst)
	}
}

func TestWatchdogPeriodicCheckpointRespectsWatchPathsAndFailureBackoff(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	accounts := []*models.Account{
		{ID: "acc_wd", Email: "wd@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	ckpt := checkpoint.NewManager(dataDir, pool, rt, nil)

	// Configure custom WatchPaths separate from ComfyUI's default `/content/ComfyUI/output`!
	if _, err := ckpt.UpdatePolicy(models.CheckpointPolicy{
		AutoResumeEnabled:  true,
		SyncIntervalSec:    60,
		WatchPaths:         []string{"/content/custom_watch"},
		MaxSnapshotsPerJob: 3,
	}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	// Start with ListFiles error so initial periodic sync attempts fail!
	rt.mu.Lock()
	rt.listFilesErr = errors.New("temporary VM busy")
	rt.mu.Unlock()

	mgr := appstore.NewManager(pool, rt, ckpt, nil, appstore.Config{
		DataDir:                        dataDir,
		DisableUserHomePlugins:         true,
		CloudMode:                      false,
		DemoMode:                       false,
		WatchdogInterval:               20 * time.Millisecond,
		CheckpointSyncIntervalOverride: 40 * time.Millisecond,
	})
	defer mgr.Close()

	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	initialSyncTime, ok := mgr.LastCheckpointSyncTime(inst.ID)
	if !ok {
		t.Fatalf("LastCheckpointSyncTime missing")
	}

	// Wait across multiple failing ticks and verify lastCheckpointSync NEVER advanced on failure!
	time.Sleep(150 * time.Millisecond)
	afterFailSyncTime, _ := mgr.LastCheckpointSyncTime(inst.ID)
	if !afterFailSyncTime.Equal(initialSyncTime) {
		t.Fatalf("lastCheckpointSync MUST NOT advance on failed sync: before=%v after=%v", initialSyncTime, afterFailSyncTime)
	}
	rt.mu.Lock()
	if rt.listFilesCallCount == 0 {
		t.Fatalf("expected watchdog to attempt periodic checkpoint sync")
	}
	rt.listFilesErr = nil
	rt.mu.Unlock()

	// Now upload a file inside configured `policy.WatchPaths` (`/content/custom_watch`), NOT `/content/ComfyUI/output`!
	_ = rt.UploadFile(ctx, inst.SessionID, "/content/custom_watch/weights_step_50.safetensors", []byte("REAL_WEIGHTS"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec, found := ckpt.LatestCheckpointForJob(inst.ID); found && rec != nil && rec.RemotePath == "/content/custom_watch" && rec.FileCount == 1 {
			afterSuccessSyncTime, _ := mgr.LastCheckpointSyncTime(inst.ID)
			if !afterSuccessSyncTime.After(initialSyncTime) {
				t.Fatalf("expected lastCheckpointSync to advance after successful sync")
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected watchdog periodic checkpoint sync to honor configured WatchPaths (/content/custom_watch)")
}

func TestHonestFailoverReportingWithAndWithoutVerifiedSnapshot(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	hub := events.NewHub()

	accounts := []*models.Account{
		{ID: "acc_a", Email: "acct_a@gmail.com", Alias: "免费号 A", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
		{ID: "acc_b", Email: "acct_b@gmail.com", Alias: "免费号 B", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}

	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	rt.realRemoteMode = true
	rt.remoteStatusCode = http.StatusOK
	rt.remoteReplyBody = `{"queue_remaining":0,"comfy_ready":true}`

	ckpt := checkpoint.NewManager(dataDir, pool, rt, hub)
	mgr := appstore.NewManager(pool, rt, ckpt, hub, appstore.Config{
		DataDir:                dataDir,
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               false,
		WatchdogInterval:       25 * time.Millisecond,
		IdleTimeoutOverride:    300 * time.Millisecond,
	})
	defer mgr.Close()

	// Case 1: Failover WITHOUT any synced snapshot -> MUST honestly report clean restart (NO false snapshot-restored claim!).
	inst1, err := mgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui", PreferredLocalPort: 0})
	if err != nil {
		t.Fatalf("Launch inst1: %v", err)
	}
	origSess1 := inst1.SessionID
	origPort1 := inst1.LocalPort

	rt.mu.Lock()
	rt.sessions[origSess1].Status = models.SessionLost
	rt.mu.Unlock()

	resp, err := http.Get(inst1.LocalURL + "/queue")
	if err != nil {
		t.Fatalf("GET after preemption failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 after failover, got %d", resp.StatusCode)
	}

	afterNoSnap, err := mgr.GetInstance(inst1.ID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if afterNoSnap.AccountID != "acc_b" || afterNoSnap.LocalPort != origPort1 || afterNoSnap.FailoverCount < 1 {
		t.Fatalf("expected failover onto acc_b keeping port %d, got %+v", origPort1, afterNoSnap)
	}
	if !strings.Contains(afterNoSnap.LastFailoverNote, "已切换至新账号并全新重启（未找到/未能恢复历史快照") {
		t.Fatalf("failover without snapshot MUST report clean restart honestly, got %q", afterNoSnap.LastFailoverNote)
	}
	if strings.Contains(afterNoSnap.LastFailoverNote, "已从快照恢复") {
		t.Fatalf("failover without snapshot MUST NEVER claim snapshot was restored: %q", afterNoSnap.LastFailoverNote)
	}

	// Case 2: Snapshot exists on record, but local snapshot dir is missing -> ResumeFromCheckpoint fails ->
	//         MUST still fall back to fresh VM AND report honestly that snapshot could NOT be restored!
	instBrokenSnap, err := mgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui", PreferredLocalPort: 0})
	if err != nil {
		t.Fatalf("Launch instBrokenSnap: %v", err)
	}
	_ = rt.UploadFile(ctx, instBrokenSnap.SessionID, "/content/ComfyUI/output/lost.png", []byte("PNG"))
	brokenRec, sErr := ckpt.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID:  instBrokenSnap.SessionID,
		JobID:      instBrokenSnap.ID,
		JobKind:    "app",
		RemotePath: "/content/ComfyUI/output",
	})
	if sErr != nil {
		t.Fatalf("SyncSessionCheckpoint for instBrokenSnap: %v", sErr)
	}
	// Remove local snapshot directory to force ResumeFromCheckpoint failure.
	_ = os.RemoveAll(brokenRec.LocalSnapshotDir)
	rt.mu.Lock()
	rt.sessions[instBrokenSnap.SessionID].Status = models.SessionLost
	rt.mu.Unlock()

	healedBroken, err := mgr.TriggerFailover(ctx, instBrokenSnap.ID)
	if err != nil {
		t.Fatalf("TriggerFailover(instBrokenSnap): %v", err)
	}
	if !strings.Contains(healedBroken.LastFailoverNote, "已切换至新账号并全新重启（未找到/未能恢复历史快照") ||
		strings.Contains(healedBroken.LastFailoverNote, "已从快照恢复") {
		t.Fatalf("failed snapshot restore MUST report honestly without claiming restore, got %q", healedBroken.LastFailoverNote)
	}

	// Case 3: Failover WITH a verified synced snapshot -> MUST restore snapshot and report "已从快照恢复并切换至..."!
	inst2, err := mgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "comfyui", PreferredLocalPort: 0})
	if err != nil {
		t.Fatalf("Launch inst2: %v", err)
	}
	_ = rt.UploadFile(ctx, inst2.SessionID, "/content/ComfyUI/output/render_001.png", []byte("PNG_BYTES"))
	if _, sErr := ckpt.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID:  inst2.SessionID,
		JobID:      inst2.ID,
		JobKind:    "app",
		RemotePath: "/content/ComfyUI/output",
	}); sErr != nil {
		t.Fatalf("SyncSessionCheckpoint for inst2: %v", sErr)
	}

	rt.mu.Lock()
	rt.sessions[inst2.SessionID].Status = models.SessionLost
	rt.mu.Unlock()

	healed2, err := mgr.TriggerFailover(ctx, inst2.ID)
	if err != nil {
		t.Fatalf("TriggerFailover(inst2): %v", err)
	}
	if !strings.Contains(healed2.LastFailoverNote, "已从快照恢复并切换至") {
		t.Fatalf("failover with verified snapshot expected restored note, got %q", healed2.LastFailoverNote)
	}

	// Wait for idle auto-shutdown to cleanly stop inst1 (empty dir -> stops idle VM).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cur, gErr := mgr.GetInstance(inst1.ID)
		if gErr == nil && cur.State == models.AppStateStopped {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	cur, _ := mgr.GetInstance(inst1.ID)
	t.Fatalf("expected instance to auto-stop after idle timeout, got state=%q", cur.State)
}

func TestConcurrentLaunchRestartAndZeroGoroutineLeak(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	accounts := []*models.Account{
		{ID: "acc_conc_1", Email: "conc1@gmail.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
		{ID: "acc_conc_2", Email: "conc2@gmail.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}

	goruntime.GC()
	time.Sleep(30 * time.Millisecond)
	baseGoroutines := goruntime.NumGoroutine()

	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	ckpt := checkpoint.NewManagerWithConfig(checkpoint.Config{DataDir: dataDir, DemoMode: true}, pool, rt, events.NewHub())
	mgr := appstore.NewManager(pool, rt, ckpt, events.NewHub(), appstore.Config{
		DataDir:                dataDir,
		DisableUserHomePlugins: true,
		CloudMode:              false,
		DemoMode:               true,
		WatchdogInterval:       50 * time.Millisecond,
	})

	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		SelectedModelID:    "z-image-turbo",
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("comfyui Launch failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, gErr := http.Get(inst.LocalURL + "/system_stats")
			if gErr == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			mVal := 30 + (idx%3)*15
			_, _ = mgr.UpdateConfig(ctx, inst.ID, appstore.AppInstanceConfigPatch{IdleAutoStopMin: &mVal})
			_ = mgr.ListInstances()
		}(i)
	}
	wg.Wait()

	restarted, err := mgr.Restart(ctx, inst.ID)
	if err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	if restarted.State != models.AppStateRunning || !restarted.LocalBridgeActive || restarted.SelectedModelID != "z-image-turbo" {
		t.Fatalf("expected running comfyui retaining z-image-turbo after Restart, got %+v", restarted)
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("mgr.Close() failed: %v", err)
	}

	time.Sleep(80 * time.Millisecond)
	goruntime.GC()
	afterGoroutines := goruntime.NumGoroutine()
	if afterGoroutines > baseGoroutines+4 {
		t.Fatalf("potential goroutine leak: baseline=%d afterClose=%d", baseGoroutines, afterGoroutines)
	}
}

func TestAuditClosureKeylessProxy422LLMChatRejectComfyOrderAndRestartCheckpoints(t *testing.T) {
	ctx := context.Background()
	accounts := []*models.Account{
		{ID: "acc_c1", Email: "c1@example.com", SubscriptionTier: models.TierPro, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &mockPool{accounts: accounts}
	rt := newMockRuntime(accounts)
	dataDir := t.TempDir()
	ckpt := checkpoint.NewManagerWithConfig(checkpoint.Config{DataDir: dataDir, DemoMode: true}, pool, rt, nil)

	// 1. Live keyless local mode MUST STILL block same-origin `/apps/proxy/{id}/` with HTTP 422!
	mgr := appstore.NewManager(pool, rt, ckpt, nil, appstore.Config{
		DataDir:                dataDir,
		DisableUserHomePlugins: true,
		CloudMode:              false,
		ProtectedMode:          false,
		DemoMode:               false,
	})
	defer mgr.Close()

	// 2. Direct Launch of `llm-chat` MUST be rejected (single source of truth in /api/v1/llm/start).
	if _, err := mgr.Launch(ctx, models.AppLaunchRequest{TemplateID: "llm-chat"}); err == nil {
		t.Fatalf("expected Launch(llm-chat) to fail so /api/v1/llm/start remains single owner")
	}

	// 3. ComfyUI bootstrap script MUST clone BEFORE makedirs(ckpt_dir) and MUST strip Authorization on redirects.
	comfyTpl, ok := appstore.FindTemplate("comfyui")
	if !ok {
		t.Fatalf("comfyui built-in template missing")
	}
	cloneIdx := strings.Index(comfyTpl.BootstrapScript, "git\", \"clone\"")
	mkdirIdx := strings.Index(comfyTpl.BootstrapScript, "os.makedirs(ckpt_dir, exist_ok=True)")
	if cloneIdx < 0 || mkdirIdx < 0 || cloneIdx > mkdirIdx {
		t.Fatalf("comfyui bootstrap must git clone BEFORE os.makedirs(ckpt_dir): cloneIdx=%d mkdirIdx=%d", cloneIdx, mkdirIdx)
	}
	if !strings.Contains(comfyTpl.BootstrapScript, "_SafeModelRedirectHandler") || !strings.Contains(comfyTpl.BootstrapScript, `next_req.remove_header("Authorization")`) {
		t.Fatalf("comfyui bootstrap must strip HF_TOKEN Authorization header on cross-host redirects")
	}

	// 4. Launch comfyui with explicit L4 GPU -> verify keyless same-origin proxy returns 422
	//    AND Restart preserves AutoFallback == false!
	inst, err := mgr.Launch(ctx, models.AppLaunchRequest{
		TemplateID:         "comfyui",
		PreferredGPU:       models.AcceleratorL4,
		PreferredLocalPort: 0,
	})
	if err != nil {
		t.Fatalf("Launch(comfyui): %v", err)
	}
	rec := httptest.NewRecorder()
	mgr.ServeProxyHTTP(rec, httptest.NewRequest(http.MethodGet, inst.WebProxyURL, nil), inst.ID, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected HTTP 422 on live keyless same-origin /apps/proxy/{id}/, got %d", rec.Code)
	}

	if _, err := mgr.Restart(ctx, inst.ID); err != nil {
		t.Fatalf("Restart(comfyui): %v", err)
	}
	rt.mu.Lock()
	lastAlloc := rt.createCalls[len(rt.createCalls)-1]
	rt.mu.Unlock()
	if lastAlloc.AutoFallback {
		t.Fatalf("expected Restart to preserve AutoFallback=false for explicit GPU selection, got %+v", lastAlloc)
	}
}
