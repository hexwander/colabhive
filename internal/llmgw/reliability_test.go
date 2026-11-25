package llmgw

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

type stubLLMExecRuntime struct {
	models.RuntimeEngine
	execFn func(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error)
}

func (s *stubLLMExecRuntime) ExecuteCode(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	return s.execFn(ctx, req)
}

func TestSanitizeUpstreamHTMLNotice(t *testing.T) {
	raw := "assign POST HTTP 503: <!DOCTYPE html><html><head><title>Error 503</title></head><body>Busy</body></html>"
	got := sanitizeUpstreamHTMLNotice(raw)
	if strings.Contains(strings.ToLower(got), "<html") || strings.Contains(strings.ToLower(got), "<!doctype") {
		t.Fatalf("expected HTML to be stripped, got %q", got)
	}
	if !strings.Contains(got, "503") {
		t.Fatalf("expected 503 hint to remain, got %q", got)
	}
}

func TestPollDetachedOllamaBootstrapSurvivesTransientDropsAndPublishesSteps(t *testing.T) {
	origInterval := ollamaBootstrapPollInterval
	ollamaBootstrapPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { ollamaBootstrapPollInterval = origInterval })

	calls := 0
	rt := &stubLLMExecRuntime{
		execFn: func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
			calls++
			switch calls {
			case 1:
				return &models.KernelExecResult{
					Status: "ok",
					Stdout: `COLABHIVE_LLM_POLL:{"state":"running","step":"Installing Ollama...","step_zh":"正在云端显卡安装 Ollama 推理引擎..."}` + "\n",
				}, nil
			case 2, 3, 4:
				return nil, errors.New("jupyter websocket read error: websocket: close 1006 (abnormal closure): unexpected EOF")
			case 5:
				return &models.KernelExecResult{
					Status: "ok",
					Stdout: `COLABHIVE_LLM_POLL:{"state":"running","step":"Pulling weights...","step_zh":"正在拉取 Hugging Face 无审查模型权重..."}` + "\n",
				}, nil
			default:
				return &models.KernelExecResult{
					Status: "ok",
					Stdout: `COLABHIVE_LLM_POLL:{"state":"ready","step":"Ready","step_zh":"模型加载完成"}` + "\n",
				}, nil
			}
		},
	}

	svc := &Service{rt: rt}
	spec := models.LLMModelSpec{ID: "qwen3.5-9b-heretic", OllamaTag: "hf.co/mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF:Q4_K_M"}

	live, err := svc.pollDetachedOllamaBootstrap(context.Background(), "sess_1", spec, "/tmp/ch_ollama_boot.json")
	if err != nil {
		t.Fatalf("expected detached Ollama poll to survive transient WS drops, got error: %v", err)
	}
	if !live {
		t.Fatalf("expected live=true on ready state")
	}
	if calls != 6 {
		t.Fatalf("expected 6 poll calls, got %d", calls)
	}
	svc.stateMu.RLock()
	gotStep := svc.status.StatusStepZh
	svc.stateMu.RUnlock()
	if !strings.Contains(gotStep, "拉取") {
		t.Fatalf("expected live StatusStepZh update during polling, got %q", gotStep)
	}
}

type stubProgressRuntime struct {
	models.RuntimeEngine
	createFn func(ctx context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error)
	stopFn   func(ctx context.Context, sessionID string) error
	execFn   func(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error)
}

func (s *stubProgressRuntime) CreateSession(ctx context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error) {
	return s.createFn(ctx, req)
}

func (s *stubProgressRuntime) StopSession(ctx context.Context, sessionID string) error {
	if s.stopFn != nil {
		return s.stopFn(ctx, sessionID)
	}
	return nil
}

func (s *stubProgressRuntime) ExecuteCode(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	if s.execFn != nil {
		return s.execFn(ctx, req)
	}
	return &models.KernelExecResult{Status: "ok"}, nil
}

type captureHub struct {
	models.EventHub
	events []models.WSEvent
}

func (h *captureHub) Publish(evt models.WSEvent) {
	h.events = append(h.events, evt)
}

func TestFormatAllocationErrorMaxConcurrencyFriendlyChinese(t *testing.T) {
	raw := errors.New(`no eligible pooled account available for requested accelerator (h***@gmail.com(T4): health=MAX_CONCURRENCY; j***@gmail.com(T4): health=MAX_CONCURRENCY)`)
	got := formatAllocationError(raw).Error()
	if !strings.Contains(got, "并发实例上限") || !strings.Contains(got, "实验工作台") {
		t.Fatalf("expected honest MAX_CONCURRENCY diagnosis, got %q", got)
	}
	if strings.Contains(got, "切换至支持 T4") {
		t.Fatalf("should NOT tell user to switch to T4 when all accounts are MAX_CONCURRENCY, got %q", got)
	}
}

func TestFormatAllocationErrorCapacityTimeoutFriendlyChinese(t *testing.T) {
	raw := errors.New(`no eligible pooled account available for requested accelerator (t***@gmail.com(L4): excluded after failed attempt) (prior attempt: colab /tun/m/assign G4 probe timed out after 28s (capacity unavailable): context deadline exceeded)`)
	got := formatAllocationError(raw).Error()
	if !strings.Contains(got, "暂无库存") || !strings.Contains(got, "实验工作台") {
		t.Fatalf("expected honest Colab stockout/concurrency diagnosis, got %q", got)
	}
	if strings.Contains(got, "添加新的 Google Colab 账号") {
		t.Fatalf("should NOT tell user to add a new account on Colab capacity timeout, got %q", got)
	}
}

func TestBuildOllamaBootstrapPythonStreamsHTTPPullProgress(t *testing.T) {
	script := buildOllamaBootstrapPythonWithID("hf.co/example/model:Q4_K_M", "boot_test1", 8192, 2)
	if strings.Contains(script, "%!(") {
		t.Fatalf("unexpected fmt.Sprintf formatting error inside bootstrap python script:\n%s", script)
	}
	for _, want := range []string{
		"http://127.0.0.1:11434/api/pull",
		"正在下载模型权重 {}%",
		"Downloading weights {}%",
		"MB/s",
		"正在校验模型权重完整性",
		"正在将模型权重载入显存并预热 CUDA 内核",
		"__ch_num_ctx = 8192",
		"num_predict",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected streaming Ollama pull+warmup script to contain %q", want)
		}
	}
}

func TestThinkingStreamSplitterAcrossChunkBoundaries(t *testing.T) {
	var thinkBuf, textBuf strings.Builder
	sp := newThinkingStreamSplitter(
		func(s string) error { thinkBuf.WriteString(s); return nil },
		func(s string) error { textBuf.WriteString(s); return nil },
	)
	for _, chunk := range []string{"<thi", "nk>\nStep 1: ", "verify\n</th", "ink>\n\nFinal ", "Answer!"} {
		if err := sp.Push(chunk); err != nil {
			t.Fatalf("Push(%q): %v", chunk, err)
		}
	}
	if err := sp.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := strings.TrimSpace(thinkBuf.String()); got != "Step 1: verify" {
		t.Fatalf("unexpected thinking output: %q", got)
	}
	if got := textBuf.String(); got != "Final Answer!" {
		t.Fatalf("unexpected answer output: %q", got)
	}
}

func TestResolveThinkFlag(t *testing.T) {
	tr, fa := true, false
	if v := resolveThinkFlag(&fa, &tr, "high"); v == nil || *v {
		t.Fatalf("expected explicit think=false to win, got %v", v)
	}
	if v := resolveThinkFlag(nil, &fa, "high"); v == nil || *v {
		t.Fatalf("expected enable_thinking=false to win over effort, got %v", v)
	}
	if v := resolveThinkFlag(nil, nil, "none"); v == nil || *v {
		t.Fatalf("expected reasoning_effort=none -> false, got %v", v)
	}
	if v := resolveThinkFlag(nil, nil, "disabled"); v == nil || *v {
		t.Fatalf("expected thinking.type=disabled -> false, got %v", v)
	}
	if v := resolveThinkFlag(nil, nil, "enabled"); v == nil || !*v {
		t.Fatalf("expected thinking.type=enabled -> true, got %v", v)
	}
	if v := resolveThinkFlag(nil, nil, ""); v != nil {
		t.Fatalf("expected empty inputs -> nil, got %v", *v)
	}
}

type stubListRuntime struct {
	models.RuntimeEngine
	sessions []*models.RuntimeSession
}

func (s *stubListRuntime) ListSessions(_ context.Context, _ models.SessionFilter) ([]*models.RuntimeSession, error) {
	return s.sessions, nil
}

func TestRehydrateActiveSessionAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// Case 1: Upgrade path with NO llmgw_state.json yet — adopts live `llmgw-qwen3-5-9b-heretic` session!
	rt1 := &stubListRuntime{
		sessions: []*models.RuntimeSession{
			{
				ID:          "sess_stopped_old",
				Name:        "llmgw-gemma4-12b-heretic",
				AccountID:   "acc_1",
				Accelerator: models.AcceleratorT4,
				Status:      models.SessionStopped,
				CreatedAt:   now.Add(-20 * time.Minute),
			},
			{
				ID:           "sess_live_qwen",
				Name:         "llmgw-qwen3-5-9b-heretic",
				AccountID:    "acc_1",
				AccountEmail: "alice@gmail.com",
				Accelerator:  models.AcceleratorT4,
				Status:       models.SessionIdle,
				CreatedAt:    now.Add(-5 * time.Minute),
			},
		},
	}

	svc1 := NewService(nil, rt1, nil, nil, Config{DataDir: dir, DemoMode: false})
	t.Cleanup(func() { _ = svc1.Close() })

	st1 := svc1.Status()
	if !st1.Running || st1.State != models.LLMStateReady || st1.SessionID != "sess_live_qwen" || st1.ActiveModelID != "qwen3.5-9b-heretic" {
		t.Fatalf("expected live llmgw-qwen3-5-9b-heretic session rehydrated on restart, got %+v", st1)
	}

	// Case 2: Persisted custom Hugging Face model rehydrates exact custom model ID across restart.
	customTag := "hf.co/unsloth/gemma-3-27b-it-GGUF:Q4_K_M"
	rt2 := &stubListRuntime{
		sessions: []*models.RuntimeSession{
			{
				ID:          "sess_live_custom",
				Name:        "llmgw-" + safeSessionSlug(customTag),
				AccountID:   "acc_2",
				Accelerator: models.AcceleratorL4,
				Status:      models.SessionIdle,
				CreatedAt:   now.Add(-2 * time.Minute),
			},
		},
	}
	dir2 := t.TempDir()
	svcSeed := NewService(nil, &stubListRuntime{}, nil, nil, Config{DataDir: dir2, DemoMode: false})
	specCustom, err := svcSeed.ResolveModel(customTag)
	if err != nil {
		t.Fatalf("ResolveModel custom: %v", err)
	}
	svcSeed.stateMu.Lock()
	svcSeed.activeSpec = specCustom
	svcSeed.status.Running = true
	svcSeed.status.SessionID = "sess_live_custom"
	svcSeed.status.IdleAutoStopMin = 60
	svcSeed.persistStateLocked()
	svcSeed.stateMu.Unlock()
	_ = svcSeed.Close()

	svc2 := NewService(nil, rt2, nil, nil, Config{DataDir: dir2, DemoMode: false})
	t.Cleanup(func() { _ = svc2.Close() })
	st2 := svc2.Status()
	if !st2.Running || st2.ActiveModelID != customTag || st2.IdleAutoStopMin != 60 || st2.AssignedGPU != models.AcceleratorL4 {
		t.Fatalf("expected custom HF model rehydrated from llmgw_state.json, got %+v", st2)
	}

	// Case 3: When no live sessions exist, stale state file is cleaned up and gateway stays stopped.
	svc3 := NewService(nil, &stubListRuntime{}, nil, nil, Config{DataDir: dir2, DemoMode: false})
	t.Cleanup(func() { _ = svc3.Close() })
	if st3 := svc3.Status(); st3.Running || st3.State != models.LLMStateStopped {
		t.Fatalf("expected stopped state when no live session exists, got %+v", st3)
	}
}

func TestLiveOllamaInferenceQueueSerializesConcurrentRequests(t *testing.T) {
	var inExec atomic.Int32
	var maxConcurrent atomic.Int32
	reqAStarted := make(chan struct{})
	releaseReqA := make(chan struct{})
	var callNum atomic.Int32

	rt := &stubLLMExecRuntime{
		execFn: func(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
			cur := inExec.Add(1)
			defer inExec.Add(-1)
			for {
				prev := maxConcurrent.Load()
				if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
					break
				}
			}
			n := callNum.Add(1)
			if n == 1 {
				close(reqAStarted)
				select {
				case <-releaseReqA:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if req.OnStdout != nil {
				req.OnStdout(`{"message":{"role":"assistant","content":"ok"},"done":false}` + "\n")
				req.OnStdout(`{"message":{"role":"assistant","content":""},"done":true,"eval_count":2,"prompt_eval_count":2}` + "\n")
			}
			return &models.KernelExecResult{Status: "ok"}, nil
		},
	}

	svc := NewService(nil, rt, nil, nil, Config{DemoMode: false})
	t.Cleanup(func() { _ = svc.Close() })

	spec, _ := svc.ResolveModel(DefaultModelID)
	snap := activeRuntimeSnap{
		status:    models.LLMGatewayStatus{Running: true, State: models.LLMStateReady, SessionID: "sess_live_q", AssignedGPU: models.AcceleratorT4},
		spec:      spec,
		sessionID: "sess_live_q",
	}

	errA := make(chan error, 1)
	go func() {
		_, _, err := svc.runLiveOllamaChat(context.Background(), snap, ChatInferRequest{Messages: []ChatMessage{{Role: "user", Content: "A"}}}, nil)
		errA <- err
	}()
	<-reqAStarted

	// Request C waits briefly then cancels — must exit cleanly without interrupting Request A.
	ctxC, cancelC := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelC()
	if _, _, errC := svc.runLiveOllamaChat(ctxC, snap, ChatInferRequest{Messages: []ChatMessage{{Role: "user", Content: "C"}}}, nil); !errors.Is(errC, context.DeadlineExceeded) {
		t.Fatalf("expected queued canceled request to return DeadlineExceeded, got %v", errC)
	}

	// Request B queues behind Request A, then runs immediately after Request A finishes.
	errB := make(chan error, 1)
	go func() {
		_, _, err := svc.runLiveOllamaChat(context.Background(), snap, ChatInferRequest{Messages: []ChatMessage{{Role: "user", Content: "B"}}}, nil)
		errB <- err
	}()
	time.Sleep(30 * time.Millisecond)
	close(releaseReqA)

	if err := <-errA; err != nil {
		t.Fatalf("Request A failed: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("Request B failed: %v", err)
	}
	if got := maxConcurrent.Load(); got != 1 {
		t.Fatalf("expected strictly serialized live Ollama/Jupyter turns (maxConcurrent=1), got %d", got)
	}
}

func TestStartPublishesLiveOnProgressAndStopCancelsInFlightStart(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	hub := &captureHub{}

	rt := &stubProgressRuntime{
		createFn: func(ctx context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error) {
			if req.OnProgress == nil {
				t.Errorf("expected OnProgress callback to be wired on CreateSession")
			} else {
				req.OnProgress(
					"Requesting L4 GPU on alice@gmail.com (attempt 1)...",
					"正在向 Google Colab 申请 L4 显卡（账号 alice@gmail.com · 第 1 次探测）...",
				)
			}
			close(started)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		},
	}

	svc := NewService(nil, rt, nil, hub, Config{DemoMode: true})
	t.Cleanup(func() { _ = svc.Close() })

	startDone := make(chan error, 1)
	go func() {
		_, err := svc.Start(context.Background(), models.LLMStartRequest{ModelID: DefaultModelID})
		startDone <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("Start did not reach CreateSession in time")
	}

	// Verify live progress was published and raw email was scrubbed!
	st := svc.Status()
	if !strings.Contains(st.StatusStepZh, "正在向 Google Colab 申请 L4 显卡") {
		t.Fatalf("expected live OnProgress step in Status(), got %q", st.StatusStepZh)
	}
	if strings.Contains(st.StatusStepZh, "alice@gmail.com") || strings.Contains(st.StatusStep, "alice@gmail.com") {
		t.Fatalf("expected raw email to be scrubbed from live progress, got en=%q zh=%q", st.StatusStep, st.StatusStepZh)
	}

	// Now invoke Stop() while Start() is still blocked inside CreateSession — must cancel immediately!
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	stoppedStatus, err := svc.Stop(stopCtx)
	if err != nil {
		t.Fatalf("Stop() during in-flight Start failed: %v", err)
	}
	if stoppedStatus.State != models.LLMStateStopped {
		t.Fatalf("expected stopped state after canceling Start, got %s", stoppedStatus.State)
	}

	select {
	case <-canceled:
	case <-time.After(1 * time.Second):
		t.Fatalf("expected CreateSession ctx to be canceled by Stop()")
	}

	select {
	case startErr := <-startDone:
		if !errors.Is(startErr, context.Canceled) {
			t.Fatalf("expected Start() to return context.Canceled, got %v", startErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("Start() did not return promptly after Stop()")
	}
}

type rehydrateTestRuntime struct {
	models.RuntimeEngine
	sessions    []*models.RuntimeSession
	execFn      func(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error)
	stopCalled  atomic.Int32
	lastStopped atomic.Value
}

func (r *rehydrateTestRuntime) ListSessions(_ context.Context, _ models.SessionFilter) ([]*models.RuntimeSession, error) {
	return r.sessions, nil
}

func (r *rehydrateTestRuntime) ExecuteCode(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	if r.execFn != nil {
		return r.execFn(ctx, req)
	}
	return &models.KernelExecResult{Status: "ok"}, nil
}

func (r *rehydrateTestRuntime) StopSession(_ context.Context, id string) error {
	r.stopCalled.Add(1)
	r.lastStopped.Store(id)
	return nil
}

func TestRehydrateResumesInProgressDeploymentAndSyncsLogsAfterCrash(t *testing.T) {
	origInterval := ollamaBootstrapPollInterval
	ollamaBootstrapPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { ollamaBootstrapPollInterval = origInterval })

	dataDir := t.TempDir()
	// Simulate persisted state saved right after GPU allocation before ColabHive crashed mid-pull.
	ps := persistedGatewayState{
		SessionID:    "sess_crash_resume_1",
		ModelID:      "qwen3.8-27b-abliterated",
		Engine:       models.LLMEngineOllama,
		PreferredGPU: models.AcceleratorL4,
		Deploying:    true,
	}
	raw, _ := json.Marshal(ps)
	if err := os.WriteFile(filepath.Join(dataDir, "llmgw_state.json"), raw, 0o600); err != nil {
		t.Fatalf("write persisted state: %v", err)
	}

	var pollCount atomic.Int32
	rt := &rehydrateTestRuntime{
		sessions: []*models.RuntimeSession{
			{
				ID:          "sess_crash_resume_1",
				Name:        "llmgw-qwen3-8-27b-abliterated",
				Status:      models.SessionIdle,
				Accelerator: models.AcceleratorL4,
				CreatedAt:   time.Now().UTC(),
			},
		},
		execFn: func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
			if req.CallerName == "llmgw-rehydrate-probe" {
				return &models.KernelExecResult{
					Status: "ok",
					Stdout: `COLABHIVE_LLM_REHYDRATE:{"state":"running","step":"Downloading weights 42% (6.5 / 15.4 GB · 280 MB/s)","step_zh":"正在下载模型权重 42% (6.5 / 15.4 GB · 280 MB/s)"}` + "\n",
				}, nil
			}
			n := pollCount.Add(1)
			if n == 1 {
				return &models.KernelExecResult{
					Status: "ok",
					Stdout: `COLABHIVE_LLM_POLL:{"state":"running","step":"Downloading weights 85%","step_zh":"正在下载模型权重 85% (13.1 / 15.4 GB · 310 MB/s)","ollama_log_tail":["offloaded 49/49 layers to GPU"]}` + "\n",
				}, nil
			}
			return &models.KernelExecResult{
				Status: "ok",
				Stdout: `COLABHIVE_LLM_POLL:{"state":"ready","step":"Model ready","step_zh":"模型已载入显存并预热完成"}` + "\n",
			}, nil
		},
	}

	svc := NewService(nil, rt, nil, nil, Config{DataDir: dataDir, DemoMode: false})
	t.Cleanup(func() { _ = svc.Close() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.Status()
		if st.Running && st.State == models.LLMStateReady {
			joinedLogs := strings.Join(st.DeployLogs, "\n")
			if !strings.Contains(joinedLogs, "85%") || !strings.Contains(joinedLogs, "[Ollama] offloaded 49/49 layers to GPU") || !strings.Contains(joinedLogs, "部署完成") {
				t.Fatalf("expected resumed progress & Ollama log lines in DeployLogs, got:\n%s", joinedLogs)
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("expected resumed deployment to reach Ready state, got %+v", svc.Status())
}

func TestRehydrateAutoReleasesOrphanedGPUWhenDetachedBootstrapFailed(t *testing.T) {
	dataDir := t.TempDir()
	ps := persistedGatewayState{
		SessionID:    "sess_crash_failed_1",
		ModelID:      "qwen3.8-27b-abliterated",
		Engine:       models.LLMEngineOllama,
		PreferredGPU: models.AcceleratorL4,
		Deploying:    true,
	}
	raw, _ := json.Marshal(ps)
	if err := os.WriteFile(filepath.Join(dataDir, "llmgw_state.json"), raw, 0o600); err != nil {
		t.Fatalf("write persisted state: %v", err)
	}

	rt := &rehydrateTestRuntime{
		sessions: []*models.RuntimeSession{
			{
				ID:          "sess_crash_failed_1",
				Name:        "llmgw-qwen3-8-27b-abliterated",
				Status:      models.SessionIdle,
				Accelerator: models.AcceleratorL4,
				CreatedAt:   time.Now().UTC(),
			},
		},
		execFn: func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
			if req.CallerName == "llmgw-rehydrate-probe" {
				return &models.KernelExecResult{
					Status: "ok",
					Stdout: `COLABHIVE_LLM_REHYDRATE:{"state":"error","error":"ollama pull failed: HTTP 400"}` + "\n",
				}, nil
			}
			return &models.KernelExecResult{Status: "ok"}, nil
		},
	}

	svc := NewService(nil, rt, nil, nil, Config{DataDir: dataDir, DemoMode: false})
	t.Cleanup(func() { _ = svc.Close() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.Status()
		if st.State == models.LLMStateError && st.SessionID == "" && rt.stopCalled.Load() >= 1 {
			joinedLogs := strings.Join(st.DeployLogs, "\n")
			if !strings.Contains(joinedLogs, "已自动释放云端显卡") || !strings.Contains(st.StatusStepZh, "已自动释放云端显卡") {
				t.Fatalf("expected explicit auto-release log & status message, got step=%q logs:\n%s", st.StatusStepZh, joinedLogs)
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("expected orphaned failed GPU to be auto-released on restart, stopCalled=%d status=%+v", rt.stopCalled.Load(), svc.Status())
}
