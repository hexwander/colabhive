package appstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

type stubAppExecRuntime struct {
	models.RuntimeEngine
	execFn func(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error)
}

func (s *stubAppExecRuntime) ExecuteCode(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	return s.execFn(ctx, req)
}

func TestResolveDirectRuntimeProxyBaseRealColabDev(t *testing.T) {
	// Real Google Colab *.prod.r.colab.dev JWTs are scoped strictly to port 8080:
	// direct per-port subdomain mapping must decline so traffic routes through our
	// full-duplex Jupyter Comm TCP tunnel over port 8080!
	sess := &models.RuntimeSession{
		ID:         "sess_live_colab",
		ProxyURL:   "https://8080-gpu-l4-s-ass1a0-1swksy0mhm0m4-b.asia-southeast1.prod.r.colab.dev",
		ProxyToken: "ChAKD_real_proxy_token",
	}

	if _, ok := resolveDirectRuntimeProxyBase(sess, 8188); ok {
		t.Fatalf("expected real .prod.r.colab.dev hostname to decline port-8188 subdomain rewrite (JWT is port-8080 scoped)")
	}
	if !isColabPort8080RuntimeProxy(sess) {
		t.Fatalf("expected real .prod.r.colab.dev session to match Comm TCP tunnel")
	}

	// Legacy .prod.colab.dev test hostnames still resolve per-port subdomains cleanly.
	legacy := &models.RuntimeSession{
		ID:         "sess_legacy_colab",
		ProxyURL:   "https://8080-gpu-l4-s-abc.prod.colab.dev",
		ProxyToken: "ChAKD_tok",
	}
	got, ok := resolveDirectRuntimeProxyBase(legacy, 8188)
	if !ok || got.String() != "https://8188-gpu-l4-s-abc.prod.colab.dev" {
		t.Fatalf("expected legacy .prod.colab.dev to map to port 8188, got ok=%v url=%v", ok, got)
	}
}

func TestSanitizeAppStoreErrorStripsHTMLAndRedactsEmail(t *testing.T) {
	raw := errors.New("allocate GPU failed on alice.secret@gmail.com: assign HTTP 503: <!DOCTYPE html><html><style>body{}</style><body>503 Service Unavailable</body></html>")
	got := sanitizeAppStoreError(raw)
	if strings.Contains(got, "alice.secret@gmail.com") {
		t.Fatalf("raw email leaked in sanitized error: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "<html") || strings.Contains(strings.ToLower(got), "<style") {
		t.Fatalf("raw HTML leaked in sanitized error: %q", got)
	}
	if !strings.Contains(got, "503") {
		t.Fatalf("expected sanitized error to preserve 503 hint: %q", got)
	}
}

func TestWangpTemplateUsesValidServerPortFlag(t *testing.T) {
	tpl, ok := FindTemplate("wangp")
	if !ok {
		t.Fatalf("wangp template missing")
	}
	if strings.Contains(tpl.BootstrapScript, "--server-name") {
		t.Fatalf("wangp bootstrap must NOT pass unsupported --server-name flag to wgp.py")
	}
	if !strings.Contains(tpl.BootstrapScript, "--server-port") {
		t.Fatalf("wangp bootstrap must pass --server-port flag to wgp.py")
	}
}

func TestPollDetachedAppBootstrapSurvivesTransientDrops(t *testing.T) {
	origInterval := appBootstrapPollInterval
	appBootstrapPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { appBootstrapPollInterval = origInterval })

	calls := 0
	rt := &stubAppExecRuntime{
		execFn: func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
			calls++
			switch calls {
			case 1:
				return &models.KernelExecResult{Status: "ok", Stdout: `COLABHIVE_APP_POLL:{"state":"running"}` + "\n"}, nil
			case 2, 3, 4:
				return nil, errors.New("jupyter websocket read error: websocket: close 1006 (abnormal closure): unexpected EOF")
			default:
				return &models.KernelExecResult{Status: "ok", Stdout: `COLABHIVE_APP_POLL:{"state":"ready"}` + "\n"}, nil
			}
		},
	}

	mgr := &Manager{rt: rt}
	if err := mgr.pollDetachedAppBootstrap(context.Background(), "sess_1", "comfyui", "/tmp/ch_app_boot_comfyui.json", 5*time.Second); err != nil {
		t.Fatalf("expected detached app poll to survive transient close 1006 drops, got error: %v", err)
	}
	if calls != 5 {
		t.Fatalf("expected 5 poll calls, got %d", calls)
	}
}

func TestPollDetachedAppBootstrapStreamsStepProgressAndLogs(t *testing.T) {
	origInterval := appBootstrapPollInterval
	appBootstrapPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { appBootstrapPollInterval = origInterval })

	calls := 0
	rt := &stubAppExecRuntime{
		execFn: func(_ context.Context, _ models.KernelExecRequest) (*models.KernelExecResult, error) {
			calls++
			if calls == 1 {
				return &models.KernelExecResult{
					Status: "ok",
					Stdout: `COLABHIVE_APP_POLL:{"state":"running","boot_log_tail":["[CH_STEP] {\"pct\":62,\"step\":\"Installing WanGP deps\",\"step_zh\":\"正在安装 WanGP Python 依赖包...\"}","Collecting mmgp>=3.0.0"],"app_log_tail":["Running on local URL: http://127.0.0.1:7861"]}` + "\n",
				}, nil
			}
			return &models.KernelExecResult{
				Status: "ok",
				Stdout: `COLABHIVE_APP_POLL:{"state":"ready","boot_log_tail":["[CH_STEP] {\"pct\":98,\"step\":\"WanGP ready\",\"step_zh\":\"WanGP 服务已就绪\"}"]}` + "\n",
			}, nil
		},
	}

	inst := &models.AppInstance{ID: "app_log_test", TemplateID: "wangp", State: models.AppStateStarting, ProgressPct: 45}
	mgr := &Manager{
		rt: rt,
		instances: map[string]*managedInstance{
			inst.ID: {inst: inst},
		},
	}

	if err := mgr.pollDetachedAppBootstrapWithInstance(context.Background(), inst.ID, "sess_1", "wangp", "/tmp/ch_app_boot_wangp.json", 5*time.Second); err != nil {
		t.Fatalf("pollDetachedAppBootstrapWithInstance failed: %v", err)
	}
	if inst.ProgressPct != 98 {
		t.Fatalf("expected live ProgressPct=98 from [CH_STEP], got %d", inst.ProgressPct)
	}
	if inst.StatusStepZh != "WanGP 服务已就绪" {
		t.Fatalf("unexpected StatusStepZh: %q", inst.StatusStepZh)
	}
	joined := strings.Join(inst.DeployLogs, "\n")
	for _, want := range []string{"正在安装 WanGP Python 依赖包", "Collecting mmgp>=3.0.0", "[wangp] Running on local URL: http://127.0.0.1:7861"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in DeployLogs, got:\n%s", want, joined)
		}
	}
}

func TestIsTrustedLoopbackHostAndExtractTemplateName(t *testing.T) {
	hostCases := []struct {
		in   string
		want bool
	}{
		{"127.146.99.4:7861", true},
		{"127.0.0.1:8188", true},
		{"app123.localhost:8888", true},
		{"localhost:7860", true},
		{"[::1]:7861", true},
		{"evil.example.com:7861", false},
		{"10.0.0.1:7861", false},
		{"", false},
	}
	for _, tc := range hostCases {
		if got := isTrustedLoopbackHost(tc.in); got != tc.want {
			t.Errorf("isTrustedLoopbackHost(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	nameCases := []struct {
		sessName string
		wantTpl  string
		wantOK   bool
	}{
		{"app-wangp-6e5fa3", "wangp", true},
		{"app-unsloth-studio-ab12cd", "unsloth-studio", true},
		{"app-comfyui-99aabb", "comfyui", true},
		{"llmgw-qwen3-5-9b-heretic", "", false},
	}
	for _, tc := range nameCases {
		gotTpl, _, ok := extractTemplateIDFromSessionName(tc.sessName)
		if ok != tc.wantOK || gotTpl != tc.wantTpl {
			t.Errorf("extractTemplateIDFromSessionName(%q) = (%q, %v), want (%q, %v)", tc.sessName, gotTpl, ok, tc.wantTpl, tc.wantOK)
		}
	}
}

type rehydrateAppRuntime struct {
	models.RuntimeEngine
	sessions []*models.RuntimeSession
}

func (r *rehydrateAppRuntime) ListSessions(_ context.Context, _ models.SessionFilter) ([]*models.RuntimeSession, error) {
	return r.sessions, nil
}

func (r *rehydrateAppRuntime) ExecuteCode(_ context.Context, _ models.KernelExecRequest) (*models.KernelExecResult, error) {
	return &models.KernelExecResult{Status: "ok", Stdout: `COLABHIVE_APP_REHYDRATE:{"state":"ready"}` + "\n"}, nil
}

func TestRehydrateActiveInstancesRestoresBridgeAndAdoptsLiveAppSession(t *testing.T) {
	rt := &rehydrateAppRuntime{
		sessions: []*models.RuntimeSession{
			{
				ID:          "sess_wangp_live_1",
				Name:        "app-wangp-6e5fa3",
				Status:      models.SessionIdle,
				Accelerator: models.AcceleratorA100,
				CreatedAt:   time.Now().UTC(),
			},
		},
	}
	mgr := NewManager(nil, rt, nil, nil, Config{
		DataDir:                t.TempDir(),
		DisableUserHomePlugins: true,
		DemoMode:               false,
	})
	t.Cleanup(func() { _ = mgr.Close() })

	list := mgr.ListInstances()
	if len(list) != 1 {
		t.Fatalf("expected 1 rehydrated app instance, got %d", len(list))
	}
	got := list[0]
	if got.TemplateID != "wangp" || got.SessionID != "sess_wangp_live_1" || got.State != models.AppStateRunning {
		t.Fatalf("unexpected rehydrated instance: %+v", got)
	}
	if !got.LocalBridgeActive || !strings.HasPrefix(got.LocalURL, "http://127.") {
		t.Fatalf("expected restored private loopback bridge URL, got active=%v url=%q", got.LocalBridgeActive, got.LocalURL)
	}
}
