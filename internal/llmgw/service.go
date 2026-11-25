package llmgw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/colabhive/colabhive/internal/commtunnel"
	"github.com/colabhive/colabhive/internal/models"
)

var (
	// ErrNoAccountsAvailable is returned when the account pool has zero configured or schedulable GPU accounts.
	ErrNoAccountsAvailable = errors.New(
		"算力池中暂无可用显卡账号：请先在【账号池】页面添加 Google Colab 账号后再使用 AI 聊天 (no GPU accounts available in pool: please connect a Google Colab account in Account Pool first)",
	)
	// ErrModelMismatch is returned (HTTP 409 Conflict) when the gateway is warm on model A and a chat request explicitly requests a different model B.
	ErrModelMismatch = errors.New("requested model does not match the currently loaded model")
	// ErrGatewayStopped is returned when an explicit Stop() or lifecycle switch preempted an in-flight inference turn.
	ErrGatewayStopped = errors.New("LLM gateway was stopped before inference completed")
)

type gatewayClientError struct {
	statusCode int
	msg        string
}

func (e *gatewayClientError) Error() string {
	return e.msg
}

var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// Config configures the Uncensored Open-Weight LLM Deployer & `/v1` + Ollama Gateway service.
type Config struct {
	BaseURL                string
	DataDir                string
	CatalogPath            string
	DisableUserHomeCatalog bool
	DemoMode               bool
	DefaultIdleStopMin     int
	IdleCheckInterval      time.Duration
	SSEKeepaliveInterval   time.Duration
	NowFunc                func() time.Time
}

// ChatMessage represents a normalized multi-turn conversation message across OpenAI, Anthropic, and Ollama.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatInferRequest encapsulates a normalized inference request routed through the active GPU session.
type ChatInferRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	Think       *bool         `json:"think,omitempty"`
}

// ChatInferResponse holds the completed text, token chunks, and usage metrics from an inference turn.
type ChatInferResponse struct {
	ModelID          string
	OllamaTag        string
	Content          string
	Chunks           []string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Duration         time.Duration
	FailoverOccurred bool
}

type activeRuntimeSnap struct {
	status       models.LLMGatewayStatus
	spec         models.LLMModelSpec
	sessionID    string
	liveOllama   bool
	lifecycleGen uint64
}

// Service manages the 1-Click Private AI Chat lifecycle, remote Ollama kernel bridge,
// idle auto-shutdown reaper, and automatic cross-account preemption failover.
type Service struct {
	pool models.AccountPoolService
	rt   models.RuntimeEngine
	rtr  models.RouterService
	hub  models.EventHub
	cfg  Config

	lifecycleMu   sync.Mutex
	startCancelMu sync.Mutex
	startCancel   context.CancelFunc

	stateMu         sync.RWMutex
	status          models.LLMGatewayStatus
	activeSpec      models.LLMModelSpec
	preferredGPU    models.AcceleratorType
	explicitGPU     bool
	pinnedAccountID string
	liveOllama      bool
	lifecycleGen    uint64
	totalGenTimeNs  int64

	inFlight atomic.Int64
	inferSem chan struct{} // 1-slot FIFO fallback queue when Jupyter execute_request bridge is used instead of multiplexed Comm TCP tunnel

	tunnelMu      sync.Mutex
	ollamaTunnels map[string]*commtunnel.Tunnel
	ollamaPort    int // default 11434; overridable in unit tests

	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWg     sync.WaitGroup
	closed   atomic.Bool
}

// NewService constructs a new LLM Gateway Service and starts its idle auto-stop watchdog.
func NewService(
	pool models.AccountPoolService,
	rt models.RuntimeEngine,
	rtr models.RouterService,
	hub models.EventHub,
	cfg Config,
) *Service {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:7860"
	}
	cfg.BaseURL = baseURL
	if cfg.DefaultIdleStopMin <= 0 {
		cfg.DefaultIdleStopMin = 30
	}
	if cfg.IdleCheckInterval <= 0 {
		cfg.IdleCheckInterval = 15 * time.Second
	}
	if cfg.SSEKeepaliveInterval <= 0 {
		cfg.SSEKeepaliveInterval = 15 * time.Second
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())
	svc := &Service{
		pool: pool,
		rt:   rt,
		rtr:  rtr,
		hub:  hub,
		cfg:  cfg,
		status: models.LLMGatewayStatus{
			Running:          false,
			State:            models.LLMStateStopped,
			StatusStep:       "Stopped · Pick a 2026 uncensored model below or send a /v1 request to auto-wake a pooled GPU",
			StatusStepZh:     "待机中 · 选择下方无审查模型点击一键部署，或外部客户端直连 /v1 自动唤醒显卡",
			AutoFailover:     true,
			OpenAIBaseURL:    baseURL + "/v1",
			AnthropicBaseURL: baseURL,
			OllamaBaseURL:    baseURL,
			IdleAutoStopMin:  cfg.DefaultIdleStopMin,
		},
		bgCtx:         bgCtx,
		bgCancel:      bgCancel,
		inferSem:      make(chan struct{}, 1),
		ollamaTunnels: make(map[string]*commtunnel.Tunnel),
	}

	svc.rehydrateActiveSession()

	svc.bgWg.Add(1)
	go svc.idleWatchdogLoop()
	return svc
}

type persistedGatewayState struct {
	SessionID       string                 `json:"session_id"`
	ModelID         string                 `json:"model_id"`
	ModelName       string                 `json:"model_name,omitempty"`
	OllamaTag       string                 `json:"ollama_tag,omitempty"`
	Engine          models.LLMEngineType   `json:"engine,omitempty"`
	Strategy        models.RoutingStrategy `json:"strategy,omitempty"`
	IdleAutoStopMin int                    `json:"idle_auto_stop_min,omitempty"`
	AutoFailover    bool                   `json:"auto_failover"`
	PreferredGPU    models.AcceleratorType `json:"preferred_gpu,omitempty"`
	ExplicitGPU     bool                   `json:"explicit_gpu,omitempty"`
	PinnedAccountID string                 `json:"pinned_account_id,omitempty"`
	Deploying       bool                   `json:"deploying,omitempty"`
	StartedAt       *time.Time             `json:"started_at,omitempty"`
}

func (s *Service) stateFilePath() string {
	dir := strings.TrimSpace(s.cfg.DataDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "llmgw_state.json")
}

func (s *Service) persistStateLocked() {
	path := s.stateFilePath()
	isDeploying := s.status.State == models.LLMStatePulling || s.status.State == models.LLMStateFailover
	if path == "" || (!s.status.Running && !isDeploying) || strings.TrimSpace(s.status.SessionID) == "" {
		return
	}
	ps := persistedGatewayState{
		SessionID:       s.status.SessionID,
		ModelID:         s.activeSpec.ID,
		ModelName:       s.activeSpec.DisplayNameZh,
		OllamaTag:       s.activeSpec.OllamaTag,
		Engine:          s.status.Engine,
		Strategy:        s.status.Strategy,
		IdleAutoStopMin: s.status.IdleAutoStopMin,
		AutoFailover:    s.status.AutoFailover,
		PreferredGPU:    s.preferredGPU,
		ExplicitGPU:     s.explicitGPU,
		PinnedAccountID: s.pinnedAccountID,
		Deploying:       !s.status.Running && isDeploying,
		StartedAt:       s.status.StartedAt,
	}
	raw, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func (s *Service) clearPersistedState() {
	if path := s.stateFilePath(); path != "" {
		_ = os.Remove(path)
	}
}

func (s *Service) loadPersistedState() *persistedGatewayState {
	path := s.stateFilePath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var ps persistedGatewayState
	if err := json.Unmarshal(raw, &ps); err != nil || strings.TrimSpace(ps.SessionID) == "" {
		return nil
	}
	return &ps
}

func isLiveGatewayRuntimeSession(sess *models.RuntimeSession) bool {
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return false
	}
	switch sess.Status {
	case models.SessionIdle, models.SessionBusy, models.SessionReconnecting:
		return true
	default:
		return false
	}
}

func (s *Service) resolveSpecFromSessionSlug(slug string) (models.LLMModelSpec, bool) {
	norm := strings.ToLower(strings.TrimSpace(slug))
	if norm == "" {
		return models.LLMModelSpec{}, false
	}
	for _, sp := range s.Catalog() {
		if safeSessionSlug(sp.ID) == norm || strings.ToLower(sp.ID) == norm {
			return sp, true
		}
	}
	if sp, err := s.ResolveModel(slug); err == nil && sp.ID != "" {
		return sp, true
	}
	return models.LLMModelSpec{}, false
}

// rehydrateActiveSession automatically re-attaches to a still-running cloud `llmgw-*` GPU
// session after a desktop/server restart so the UI, /v1/models, and Stop & Reclaim GPU
// continue controlling the live GPU seamlessly with zero cold restart.
func (s *Service) rehydrateActiveSession() {
	if s == nil || s.rt == nil {
		return
	}
	defer func() { _ = recover() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sessions, err := s.rt.ListSessions(ctx, models.SessionFilter{})
	if err != nil || len(sessions) == 0 {
		return
	}

	persisted := s.loadPersistedState()
	liveByID := make(map[string]*models.RuntimeSession, len(sessions))
	var liveLLMSessions []*models.RuntimeSession
	for _, sess := range sessions {
		if !isLiveGatewayRuntimeSession(sess) {
			continue
		}
		liveByID[sess.ID] = sess
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(sess.Name)), "llmgw-") {
			liveLLMSessions = append(liveLLMSessions, sess)
		}
	}

	var matchedSess *models.RuntimeSession
	var spec models.LLMModelSpec
	var usedPersisted *persistedGatewayState

	if persisted != nil {
		if liveSess, ok := liveByID[persisted.SessionID]; ok {
			matchedSess = liveSess
			usedPersisted = persisted
			if persisted.ModelID != "" {
				if resolved, rErr := s.ResolveModel(persisted.ModelID); rErr == nil {
					spec = resolved
				}
			}
			if spec.ID == "" && persisted.OllamaTag != "" {
				if resolved, rErr := s.ResolveModel(persisted.OllamaTag); rErr == nil {
					spec = resolved
				}
			}
		} else {
			// Persisted session is no longer live; clean up stale state file.
			s.clearPersistedState()
		}
	}

	if matchedSess == nil && len(liveLLMSessions) > 0 {
		sort.Slice(liveLLMSessions, func(i, j int) bool {
			return liveLLMSessions[i].CreatedAt.After(liveLLMSessions[j].CreatedAt)
		})
		for _, cand := range liveLLMSessions {
			slug := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cand.Name)), "llmgw-")
			if resolved, ok := s.resolveSpecFromSessionSlug(slug); ok {
				matchedSess = cand
				spec = resolved
				break
			}
		}
	}

	if matchedSess == nil || spec.ID == "" {
		return
	}

	var acc *models.Account
	if s.pool != nil && matchedSess.AccountID != "" {
		acc, _ = s.pool.GetAccount(ctx, matchedSess.AccountID)
	}
	accLabel := FormatAccountLabel(acc, matchedSess.AccountEmail, matchedSess.AccountID)
	provider := models.ProviderColab
	if acc != nil {
		provider = acc.EffectiveProvider()
	}
	freeTierActive := isSessionFreeTier(acc, matchedSess, spec)

	now := s.nowUTC()
	startedAt := matchedSess.CreatedAt.UTC()
	if usedPersisted != nil && usedPersisted.StartedAt != nil && !usedPersisted.StartedAt.IsZero() {
		startedAt = usedPersisted.StartedAt.UTC()
	}
	if startedAt.IsZero() {
		startedAt = now
	}

	engine := spec.DefaultEngine
	if engine == "" {
		engine = models.LLMEngineOllama
	}
	strategy := models.StrategyWasteZero
	if spec.FreeTierReady {
		strategy = models.StrategyFreeFirst
	}
	idleMin := s.cfg.DefaultIdleStopMin
	autoFailover := true
	prefGPU := matchedSess.Accelerator
	explicitGPU := false
	pinnedAcc := ""
	if usedPersisted != nil {
		if usedPersisted.Engine != "" {
			engine = usedPersisted.Engine
		}
		if usedPersisted.Strategy != "" {
			strategy = usedPersisted.Strategy
		}
		if usedPersisted.IdleAutoStopMin >= 5 {
			idleMin = usedPersisted.IdleAutoStopMin
		}
		autoFailover = usedPersisted.AutoFailover
		if usedPersisted.PreferredGPU != "" {
			prefGPU = usedPersisted.PreferredGPU
		}
		explicitGPU = usedPersisted.ExplicitGPU
		pinnedAcc = usedPersisted.PinnedAccountID
	}

	wasDeploying := usedPersisted != nil && usedPersisted.Deploying

	s.stateMu.Lock()
	s.activeSpec = spec
	s.liveOllama = !s.cfg.DemoMode
	s.preferredGPU = prefGPU
	s.explicitGPU = explicitGPU
	s.pinnedAccountID = pinnedAcc
	s.status.ActiveModelID = spec.ID
	s.status.ActiveModelName = spec.DisplayNameZh
	s.status.Engine = engine
	s.status.Strategy = strategy
	s.status.SessionID = matchedSess.ID
	s.status.AccountID = matchedSess.AccountID
	s.status.AccountLabel = accLabel
	s.status.Provider = provider
	s.status.AssignedGPU = matchedSess.Accelerator
	s.status.FreeTierActive = freeTierActive
	s.status.AutoFailover = autoFailover
	s.status.IdleAutoStopMin = idleMin
	s.status.StartedAt = &startedAt
	s.status.LastActiveAt = &now
	s.status.ErrorMessage = ""
	gen := s.lifecycleGen
	if wasDeploying && !s.cfg.DemoMode {
		s.status.Running = false
		s.status.State = models.LLMStatePulling
		s.status.StatusStep = fmt.Sprintf("Resuming in-progress deployment of %s on %s (%s)...", spec.DisplayName, matchedSess.Accelerator, accLabel)
		s.status.StatusStepZh = fmt.Sprintf("重启后已接管进行中的部署任务，正在同步 %s (%s) 实时进度...", accLabel, matchedSess.Accelerator)
		s.appendDeployLogLocked("软件重启后已重新连接到云端部署任务，继续同步实时进度...")
	} else {
		s.status.Running = true
		s.status.State = models.LLMStateReady
		s.status.StatusStep = fmt.Sprintf("Ready · Reattached to %s on %s (%s)", spec.DisplayName, matchedSess.Accelerator, accLabel)
		s.status.StatusStepZh = fmt.Sprintf("运行中 · 当前由 %s (%s) 驱动（已自动接管运行中显卡）", accLabel, matchedSess.Accelerator)
	}
	s.persistStateLocked()
	s.stateMu.Unlock()

	if !s.cfg.DemoMode {
		sessCopy := *matchedSess
		s.bgWg.Add(1)
		go s.reconcileRehydratedBootstrap(&sessCopy, spec, accLabel, gen, wasDeploying)
	}
}

// reconcileRehydratedBootstrap checks `/tmp/ch_ollama_boot.json` on a reattached live GPU VM after
// a restart/crash. If the detached cloud worker is still pulling/warming weights, it seamlessly resumes
// live progress & log polling until ready; if it failed while ColabHive was closed, it automatically
// releases the orphaned GPU VM so zero compute units leak.
func (s *Service) reconcileRehydratedBootstrap(
	sess *models.RuntimeSession,
	spec models.LLMModelSpec,
	accLabel string,
	expectedGen uint64,
	wasDeploying bool,
) {
	defer s.bgWg.Done()
	defer func() { _ = recover() }()

	if s.rt == nil || sess == nil || strings.TrimSpace(sess.ID) == "" {
		return
	}

	probeScript := `import json, os, sys
p = "/tmp/ch_ollama_boot.json"
if not os.path.exists(p):
    sys.stdout.write("COLABHIVE_LLM_REHYDRATE:{\"state\":\"absent\"}\n")
else:
    with open(p, "r", encoding="utf-8", errors="replace") as fh:
        raw = fh.read().strip()
    data = json.loads(raw) if raw else {"state": "absent"}
    pid = int(data.get("pid") or 0)
    if data.get("state") == "running" and pid > 0:
        try:
            os.kill(pid, 0)
        except OSError:
            with open(p, "r", encoding="utf-8", errors="replace") as fh2:
                raw2 = fh2.read().strip()
            data = json.loads(raw2) if raw2 else data
            if data.get("state") == "running":
                data = {"state": "error", "error": "background Ollama installer exited unexpectedly"}
    sys.stdout.write("COLABHIVE_LLM_REHYDRATE:" + json.dumps(data) + "\n")
`
	probeCtx, probeCancel := context.WithTimeout(s.bgCtx, 12*time.Second)
	res, err := s.rt.ExecuteCode(probeCtx, models.KernelExecRequest{
		SessionID:               sess.ID,
		Code:                    probeScript,
		Silent:                  false,
		StoreHistory:            false,
		IdleTimeoutSec:          15,
		CallerName:              "llmgw-rehydrate-probe",
		SuppressOutputBroadcast: true,
	})
	probeCancel()

	markReady := func() {
		now := s.nowUTC()
		numParallel, _ := safeOllamaConcurrency(sess.Accelerator, spec)
		s.stateMu.Lock()
		if s.lifecycleGen != expectedGen || s.status.SessionID != sess.ID {
			s.stateMu.Unlock()
			return
		}
		s.status.Running = true
		s.status.State = models.LLMStateReady
		s.status.ErrorMessage = ""
		s.status.LastActiveAt = &now
		if numParallel > 1 {
			s.status.StatusStep = fmt.Sprintf("Ready · %s on %s (%s · %d-way parallel)", spec.DisplayName, sess.Accelerator, accLabel, numParallel)
			s.status.StatusStepZh = fmt.Sprintf("运行中 · 当前由 %s (%s · %d 路并行) 驱动", accLabel, sess.Accelerator, numParallel)
		} else {
			s.status.StatusStep = fmt.Sprintf("Ready · %s on %s (%s)", spec.DisplayName, sess.Accelerator, accLabel)
			s.status.StatusStepZh = fmt.Sprintf("运行中 · 当前由 %s (%s) 驱动", accLabel, sess.Accelerator)
		}
		s.appendDeployLogLocked("部署完成 · " + s.status.StatusStepZh)
		s.persistStateLocked()
		snap := cloneGatewayStatus(s.status)
		s.stateMu.Unlock()
		s.publishStatus(snap)
	}

	handleFailed := func(reason string) {
		redacted := ScrubRawEmails(strings.TrimSpace(reason))
		if redacted == "" {
			redacted = "remote Ollama bootstrap failed"
		}
		s.stateMu.Lock()
		if s.lifecycleGen != expectedGen || s.status.SessionID != sess.ID {
			s.stateMu.Unlock()
			return
		}
		s.appendDeployLogLocked("部署失败：" + redacted)
		s.appendDeployLogLocked(fmt.Sprintf("正在自动释放云端显卡（%s · %s）以避免继续计费...", sess.Accelerator, sess.ID))
		s.stateMu.Unlock()

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
		stopErr := s.rt.StopSession(stopCtx, sess.ID)
		stopCancel()

		s.stateMu.Lock()
		if s.lifecycleGen != expectedGen {
			s.stateMu.Unlock()
			return
		}
		s.status.Running = false
		s.status.State = models.LLMStateError
		s.liveOllama = false
		if stopErr != nil && !isNotFoundError(stopErr) {
			redStop := ScrubRawEmails(stopErr.Error())
			s.status.SessionID = sess.ID
			s.status.StatusStep = "Engine bootstrap failed and GPU teardown failed — retry Stop to prevent idle billing"
			s.status.StatusStepZh = "远程推理引擎启动失败且显卡释放失败 — 请重试停止，避免云端显卡继续计费"
			s.status.ErrorMessage = redacted + " (rollback teardown failed: " + redStop + ")"
			s.appendDeployLogLocked("自动释放显卡失败：" + redStop)
		} else {
			s.status.SessionID = ""
			s.status.AccountID = ""
			s.status.AccountLabel = ""
			s.status.Provider = ""
			s.status.AssignedGPU = ""
			s.status.FreeTierActive = false
			s.status.StatusStep = "Engine bootstrap failed · cloud GPU automatically released (0 burn)"
			s.status.StatusStepZh = "远程推理引擎启动失败 · 已自动释放云端显卡（当前 0 消耗）"
			s.status.ErrorMessage = redacted
			s.appendDeployLogLocked("已自动释放云端显卡会话，当前 0 额度消耗")
			s.clearPersistedState()
		}
		snap := cloneGatewayStatus(s.status)
		s.stateMu.Unlock()
		s.publishStatus(snap)
	}

	if err != nil || res == nil || !strings.Contains(res.Stdout, "COLABHIVE_LLM_REHYDRATE:") {
		// Mock/non-bootstrap runtime or transient probe error: do not break already-ready reattachment.
		return
	}

	idx := strings.Index(res.Stdout, "COLABHIVE_LLM_REHYDRATE:")
	line := res.Stdout[idx+len("COLABHIVE_LLM_REHYDRATE:"):]
	if nl := strings.IndexAny(line, "\r\n"); nl >= 0 {
		line = line[:nl]
	}
	var st struct {
		State  string `json:"state"`
		Step   string `json:"step"`
		StepZh string `json:"step_zh"`
		Error  string `json:"error"`
	}
	if jErr := json.Unmarshal([]byte(strings.TrimSpace(line)), &st); jErr != nil {
		return
	}

	switch st.State {
	case "ready":
		if wasDeploying {
			markReady()
		}
	case "error":
		handleFailed(st.Error)
	case "running":
		// Cloud VM is actively downloading weights or warming CUDA kernels in the background!
		s.stateMu.Lock()
		if s.lifecycleGen != expectedGen || s.status.SessionID != sess.ID {
			s.stateMu.Unlock()
			return
		}
		s.status.Running = false
		s.status.State = models.LLMStatePulling
		if st.Step != "" {
			s.status.StatusStep = st.Step
		}
		if st.StepZh != "" {
			s.status.StatusStepZh = st.StepZh
			s.appendDeployLogLocked(st.StepZh)
		}
		s.persistStateLocked()
		snap := cloneGatewayStatus(s.status)
		s.stateMu.Unlock()
		s.publishStatus(snap)

		opCtx, opCancel := context.WithCancel(s.bgCtx)
		s.startCancelMu.Lock()
		s.startCancel = opCancel
		s.startCancelMu.Unlock()
		defer func() {
			opCancel()
			s.startCancelMu.Lock()
			s.startCancel = nil
			s.startCancelMu.Unlock()
		}()

		_, pollErr := s.pollDetachedOllamaBootstrap(opCtx, sess.ID, spec, "/tmp/ch_ollama_boot.json")
		if pollErr != nil {
			if errors.Is(pollErr, context.Canceled) {
				return
			}
			handleFailed(pollErr.Error())
			return
		}
		markReady()
	}
}

func (s *Service) nowUTC() time.Time {
	if s.cfg.NowFunc != nil {
		return s.cfg.NowFunc().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) sseKeepaliveInterval() time.Duration {
	if s != nil && s.cfg.SSEKeepaliveInterval > 0 {
		return s.cfg.SSEKeepaliveInterval
	}
	return 15 * time.Second
}

// Close terminates the background idle-stop goroutine and all open Comm TCP tunnels cleanly.
func (s *Service) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		s.closeAllOllamaTunnels()
		if s.bgCancel != nil {
			s.bgCancel()
		}
		s.bgWg.Wait()
	}
	return nil
}

// Catalog returns the active curated (or hot-reloaded JSON override) uncensored open-weight LLM specs.
func (s *Service) Catalog() []models.LLMModelSpec {
	if s == nil {
		return DefaultCatalog()
	}
	return LoadCatalogWithOverride(s.cfg.DataDir, s.cfg.CatalogPath, s.cfg.DisableUserHomeCatalog)
}

// ResolveModel resolves a model ID or custom Hugging Face / Ollama tag against the active (hot-reloadable) catalog.
func (s *Service) ResolveModel(raw string) (models.LLMModelSpec, error) {
	return ResolveModelSpecInCatalog(raw, s.Catalog())
}

// ActiveModelSpec returns the currently loaded model spec (including custom `hf.co/...` GGUF specs) when running.
func (s *Service) ActiveModelSpec() (models.LLMModelSpec, bool) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if !s.status.Running || s.activeSpec.ID == "" {
		return models.LLMModelSpec{}, false
	}
	cp := s.activeSpec
	cp.Tags = append([]string(nil), s.activeSpec.Tags...)
	return cp, true
}

// Status returns a thread-safe snapshot of the current LLM gateway status.
func (s *Service) Status() models.LLMGatewayStatus {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return cloneGatewayStatus(s.status)
}

func cloneGatewayStatus(in models.LLMGatewayStatus) models.LLMGatewayStatus {
	out := in
	if in.StartedAt != nil {
		t := *in.StartedAt
		out.StartedAt = &t
	}
	if in.LastActiveAt != nil {
		t := *in.LastActiveAt
		out.LastActiveAt = &t
	}
	if len(in.DeployLogs) > 0 {
		out.DeployLogs = append([]string(nil), in.DeployLogs...)
	}
	return out
}

const maxDeployLogLines = 80

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func (s *Service) appendDeployLogLocked(msg string) {
	cleaned := strings.TrimSpace(ScrubRawEmails(msg))
	if cleaned == "" {
		return
	}
	ts := s.nowUTC().Local().Format("15:04:05")
	// Deduplicate identical consecutive message bodies so fast polling never spams identical lines.
	if n := len(s.status.DeployLogs); n > 0 {
		last := s.status.DeployLogs[n-1]
		if idx := strings.Index(last, "] "); idx >= 0 && last[idx+2:] == cleaned {
			return
		}
	}
	entry := "[" + ts + "] " + cleaned
	s.status.DeployLogs = append(s.status.DeployLogs, entry)
	if len(s.status.DeployLogs) > maxDeployLogLines {
		s.status.DeployLogs = append([]string(nil), s.status.DeployLogs[len(s.status.DeployLogs)-maxDeployLogLines:]...)
	}
}

func (s *Service) publishStatus(st models.LLMGatewayStatus) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(models.WSEvent{
		Type:      models.WSEventLLMGateway,
		Timestamp: s.nowUTC(),
		Payload:   cloneGatewayStatus(st),
	})
}

// Start allocates a pooled GPU VM (or hot-switches models), bootstraps Ollama, and marks the gateway ready.
func (s *Service) Start(ctx context.Context, req models.LLMStartRequest) (*models.LLMGatewayStatus, error) {
	if s.rt == nil {
		return nil, errors.New("runtime engine is unavailable")
	}

	spec, err := s.ResolveModel(req.ModelID)
	if err != nil {
		return nil, err
	}

	engine := req.Engine
	if engine == "" {
		engine = spec.DefaultEngine
	}
	if engine == "" {
		engine = models.LLMEngineOllama
	}

	strategy := req.Strategy
	if strategy == "" {
		if spec.FreeTierReady {
			strategy = models.StrategyFreeFirst
		} else {
			strategy = models.StrategyWasteZero
		}
	}

	explicitGPU := strings.TrimSpace(string(req.PreferredGPU)) != ""
	preferredGPU := req.PreferredGPU
	if !explicitGPU {
		preferredGPU = spec.RecommendedGPU
	} else {
		normGPU, err := models.NormalizeAccelerator(string(preferredGPU))
		if err != nil {
			return nil, err
		}
		preferredGPU = normGPU
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	opCtx, opCancel := context.WithCancel(ctx)
	s.startCancelMu.Lock()
	s.startCancel = opCancel
	s.startCancelMu.Unlock()
	defer func() {
		opCancel()
		s.startCancelMu.Lock()
		s.startCancel = nil
		s.startCancelMu.Unlock()
	}()

	s.stateMu.RLock()
	curStatus := cloneGatewayStatus(s.status)
	curSessID := s.status.SessionID
	s.stateMu.RUnlock()

	autoFailover := curStatus.AutoFailover
	if req.AutoFailover != nil {
		autoFailover = *req.AutoFailover
	}
	idleMin := curStatus.IdleAutoStopMin
	if idleMin <= 0 {
		idleMin = s.cfg.DefaultIdleStopMin
	}
	if req.IdleAutoStopMin > 0 {
		idleMin = req.IdleAutoStopMin
	}

	accountID := strings.TrimSpace(req.AccountID)

	// Idempotent fast-path if the exact model (and optional pinned account/GPU) is already running on a healthy session.
	if curStatus.Running &&
		curStatus.State == models.LLMStateReady &&
		curStatus.ActiveModelID == spec.ID &&
		(req.PreferredGPU == "" || curStatus.AssignedGPU == preferredGPU) &&
		(accountID == "" || curStatus.AccountID == accountID) &&
		curSessID != "" &&
		s.isSessionHealthy(ctx, curSessID) {
		now := s.nowUTC()
		s.stateMu.Lock()
		s.status.AutoFailover = autoFailover
		s.status.IdleAutoStopMin = idleMin
		s.status.Strategy = strategy
		s.status.LastActiveAt = &now
		// Always bind newly requested GPU/account pins even on warm fast-path reuse so future failovers honor them!
		if explicitGPU {
			s.preferredGPU = preferredGPU
			s.explicitGPU = true
		}
		if accountID != "" {
			s.pinnedAccountID = accountID
		}
		snap := cloneGatewayStatus(s.status)
		s.stateMu.Unlock()
		s.publishStatus(snap)
		return &snap, nil
	}

	// Bump monotonic lifecycle generation BEFORE stopping the prior session so no in-flight failover
	// from the old generation can race with this Start() / hot-switch.
	s.stateMu.Lock()
	s.lifecycleGen++
	s.status.Running = false
	s.stateMu.Unlock()

	// If switching models or restarting from a stale session, release the previous session cleanly.
	// If teardown fails (not "not found"), preserve SessionID & mark LLMStateError so no billing GPU leaks!
	if curSessID != "" {
		s.closeOllamaTunnel(curSessID)
		if stopErr := s.rt.StopSession(ctx, curSessID); stopErr != nil && !isNotFoundError(stopErr) {
			redacted := ScrubRawEmails(stopErr.Error())
			s.stateMu.Lock()
			s.status.Running = false
			s.status.State = models.LLMStateError
			s.status.SessionID = curSessID
			s.status.StatusStep = "Failed to stop previous GPU session before switch — retry Stop to avoid idle billing"
			s.status.StatusStepZh = "切换模型前释放旧显卡失败 — 请先重试停止，避免旧显卡继续计费"
			s.status.ErrorMessage = redacted
			s.liveOllama = false
			errSnap := cloneGatewayStatus(s.status)
			s.stateMu.Unlock()
			s.publishStatus(errSnap)
			return nil, errors.New(redacted)
		}
		s.stateMu.Lock()
		if s.status.SessionID == curSessID {
			s.status.SessionID = ""
		}
		s.stateMu.Unlock()
	}

	return s.provisionAndBootstrapLocked(opCtx, spec, engine, strategy, preferredGPU, explicitGPU, accountID, autoFailover, idleMin, nil, false, "")
}

// Stop tears down the active GPU session (or cancels an in-flight Start / failover) so zero idle compute units or free GPU hours are wasted.
func (s *Service) Stop(ctx context.Context) (*models.LLMGatewayStatus, error) {
	// Cancel any in-flight Start/failover before waiting on lifecycleMu so the user can abort slow probes or pulls immediately.
	s.startCancelMu.Lock()
	cancelFn := s.startCancel
	s.startCancelMu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.stopLocked(ctx, "Gateway stopped · zero GPU quota burning", "已停止 · 当前不消耗任何显卡时长")
}

func (s *Service) stopLocked(ctx context.Context, stepEn, stepZh string) (*models.LLMGatewayStatus, error) {
	// Bump lifecycleGen and mark Running=false BEFORE invoking rt.StopSession so any in-flight
	// inference turn that fails due to session termination immediately observes the generation bump
	// and NEVER resurrects the gateway via failover!
	s.stateMu.Lock()
	s.lifecycleGen++
	oldSessID := s.status.SessionID
	s.status.Running = false
	s.stateMu.Unlock()

	var stopErr error
	if oldSessID != "" {
		s.closeOllamaTunnel(oldSessID)
		if s.rt != nil {
			stopErr = s.rt.StopSession(ctx, oldSessID)
		}
	}

	if stopErr != nil && !isNotFoundError(stopErr) {
		redacted := ScrubRawEmails(stopErr.Error())
		s.stateMu.Lock()
		s.status.Running = false
		s.status.State = models.LLMStateError
		s.status.SessionID = oldSessID // Keep SessionID tracked so user can retry teardown without leaking a billing GPU!
		s.status.StatusStep = "Failed to tear down GPU session — retry Stop to prevent idle billing"
		s.status.StatusStepZh = "停止显卡会话失败 — 请重试停止，避免云端显卡继续计费"
		s.status.ErrorMessage = redacted
		s.liveOllama = false
		snap := cloneGatewayStatus(s.status)
		s.stateMu.Unlock()

		s.publishStatus(snap)
		return &snap, errors.New(redacted)
	}

	s.stateMu.Lock()
	s.status.Running = false
	s.status.State = models.LLMStateStopped
	s.status.StatusStep = stepEn
	s.status.StatusStepZh = stepZh
	s.status.SessionID = ""
	s.status.AccountID = ""
	s.status.AccountLabel = ""
	s.status.Provider = ""
	s.status.AssignedGPU = ""
	s.status.FreeTierActive = false
	s.status.StartedAt = nil
	s.status.ErrorMessage = ""
	s.liveOllama = false
	snap := cloneGatewayStatus(s.status)
	s.stateMu.Unlock()

	s.clearPersistedState()
	s.publishStatus(snap)
	return &snap, nil
}

func (s *Service) provisionAndBootstrapLocked(
	ctx context.Context,
	spec models.LLMModelSpec,
	engine models.LLMEngineType,
	strategy models.RoutingStrategy,
	preferredGPU models.AcceleratorType,
	explicitGPU bool,
	accountID string,
	autoFailover bool,
	idleMin int,
	excludedAccountIDs []string,
	isFailover bool,
	prevAccountLabel string,
) (*models.LLMGatewayStatus, error) {
	if s.pool != nil {
		accs, err := s.pool.ListAccounts(ctx)
		if err == nil && len(accs) == 0 {
			s.stateMu.Lock()
			s.status.Running = false
			s.status.State = models.LLMStateError
			s.status.StatusStep = "No GPU accounts in pool"
			s.status.StatusStepZh = "算力池中暂无可用显卡账号"
			s.status.ErrorMessage = ErrNoAccountsAvailable.Error()
			errSnap := cloneGatewayStatus(s.status)
			s.stateMu.Unlock()
			s.publishStatus(errSnap)
			return nil, ErrNoAccountsAvailable
		}
	}

	initialState := models.LLMStateStarting
	stepEn := fmt.Sprintf("Allocating %s GPU from pool (%s)...", preferredGPU, strategy)
	stepZh := fmt.Sprintf("正在从算力池分配 %s 显卡（策略：%s）...", preferredGPU, friendlyStrategyZh(strategy))
	if isFailover {
		initialState = models.LLMStateFailover
		stepEn = "Active session interrupted — failing over to next healthy GPU account..."
		stepZh = "检测到原账号时长耗尽或掉线，正在自动无感切换至下一健康账号..."
	}

	s.stateMu.Lock()
	s.status.Running = false
	s.status.State = initialState
	s.status.StatusStep = stepEn
	s.status.StatusStepZh = stepZh
	s.status.ActiveModelID = spec.ID
	s.status.ActiveModelName = spec.DisplayNameZh
	s.status.Engine = engine
	s.status.Strategy = strategy
	s.status.AutoFailover = autoFailover
	s.status.IdleAutoStopMin = idleMin
	s.status.ErrorMessage = ""
	if !isFailover {
		s.status.DeployLogs = nil
	}
	s.appendDeployLogLocked(fmt.Sprintf("开始部署 %s（目标标签：%s）", firstNonEmpty(spec.DisplayNameZh, spec.ID), spec.OllamaTag))
	s.appendDeployLogLocked(stepZh)
	s.preferredGPU = preferredGPU
	s.explicitGPU = explicitGPU
	s.pinnedAccountID = strings.TrimSpace(accountID)
	startSnap := cloneGatewayStatus(s.status)
	s.stateMu.Unlock()
	s.publishStatus(startSnap)

	accountSelector := ""
	if trimmedID := strings.TrimSpace(accountID); trimmedID != "" {
		accountSelector = "id:" + trimmedID
	}

	var fallbackChain []models.AcceleratorType
	if !explicitGPU {
		fallbackChain = buildGPUFallbackChain(preferredGPU, spec.MinVRAMGB)
	}
	allocReq := models.SessionAllocRequest{
		Name:               "llmgw-" + safeSessionSlug(spec.ID),
		Accelerator:        preferredGPU,
		HighMem:            spec.MinVRAMGB >= 22,
		RequireLargeVRAM:   spec.MinVRAMGB >= 40,
		Strategy:           strategy,
		AccountSelector:    accountSelector,
		ExcludedAccountIDs: excludedAccountIDs,
		AutoFallback:       !explicitGPU,
		FallbackChain:      fallbackChain,
		CallerName:         "llmgw",
		OnProgress: func(stepEn, stepZh string) {
			s.stateMu.Lock()
			if s.status.State == models.LLMStateStarting || s.status.State == models.LLMStateFailover {
				s.status.StatusStep = ScrubRawEmails(stepEn)
				s.status.StatusStepZh = ScrubRawEmails(stepZh)
				s.appendDeployLogLocked(firstNonEmpty(stepZh, stepEn))
				snap := cloneGatewayStatus(s.status)
				s.stateMu.Unlock()
				s.publishStatus(snap)
				return
			}
			s.stateMu.Unlock()
		},
	}

	sess, err := s.rt.CreateSession(ctx, allocReq)
	if err != nil && isFailover && len(excludedAccountIDs) > 0 && ctx.Err() == nil {
		// Single-account pool resilience: if no other account exists, allow re-provisioning on the same account.
		allocReq.ExcludedAccountIDs = nil
		sess, err = s.rt.CreateSession(ctx, allocReq)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.stateMu.Lock()
			s.status.Running = false
			s.status.State = models.LLMStateStopped
			s.status.StatusStep = "Deployment canceled · zero GPU quota burning"
			s.status.StatusStepZh = "已取消部署 · 当前不消耗任何显卡时长"
			s.status.ErrorMessage = ""
			s.appendDeployLogLocked("已取消部署（未占用显卡，当前 0 消耗）")
			cancelSnap := cloneGatewayStatus(s.status)
			s.stateMu.Unlock()
			s.publishStatus(cancelSnap)
			return nil, context.Canceled
		}
		friendlyErr := formatAllocationError(err)
		s.stateMu.Lock()
		s.status.Running = false
		s.status.State = models.LLMStateError
		s.status.StatusStep = "Failed to allocate GPU session"
		s.status.StatusStepZh = "无法从算力池分配可用显卡"
		s.status.ErrorMessage = friendlyErr.Error()
		s.appendDeployLogLocked("显卡分配失败：" + friendlyErr.Error())
		failSnap := cloneGatewayStatus(s.status)
		s.stateMu.Unlock()
		s.publishStatus(failSnap)
		return nil, friendlyErr
	}

	var acc *models.Account
	if s.pool != nil && sess.AccountID != "" {
		acc, _ = s.pool.GetAccount(ctx, sess.AccountID)
	}
	accLabel := FormatAccountLabel(acc, sess.AccountEmail, sess.AccountID)
	provider := models.ProviderColab
	if acc != nil {
		provider = acc.EffectiveProvider()
	}
	freeTierActive := isSessionFreeTier(acc, sess, spec)

	pullState := models.LLMStatePulling
	if isFailover {
		pullState = models.LLMStateFailover
	}
	s.stateMu.Lock()
	s.activeSpec = spec
	s.status.State = pullState
	s.status.SessionID = sess.ID
	s.status.AccountID = sess.AccountID
	s.status.AccountLabel = accLabel
	s.status.Provider = provider
	s.status.AssignedGPU = sess.Accelerator
	s.status.FreeTierActive = freeTierActive
	s.status.StatusStep = fmt.Sprintf("Bootstrapping %s (%s) on %s...", spec.DisplayName, spec.OllamaTag, sess.Accelerator)
	s.status.StatusStepZh = fmt.Sprintf("正在 %s (%s) 上加载模型 %s...", accLabel, sess.Accelerator, spec.DisplayNameZh)
	s.appendDeployLogLocked(fmt.Sprintf("已分配云端显卡：%s · %s（会话 %s）", sess.Accelerator, accLabel, sess.ID))
	s.appendDeployLogLocked(s.status.StatusStepZh)
	s.persistStateLocked()
	pullSnap := cloneGatewayStatus(s.status)
	s.stateMu.Unlock()
	s.publishStatus(pullSnap)

	liveMode, bootErr := s.bootstrapRemoteOllama(ctx, sess, spec)
	if bootErr != nil {
		redactedBoot := ScrubRawEmails(bootErr.Error())
		s.stateMu.Lock()
		s.appendDeployLogLocked("部署失败：" + redactedBoot)
		s.appendDeployLogLocked(fmt.Sprintf("正在自动释放云端显卡（%s · %s）以避免继续计费...", sess.Accelerator, sess.ID))
		rollingSnap := cloneGatewayStatus(s.status)
		s.stateMu.Unlock()
		s.publishStatus(rollingSnap)

		stopCtx := ctx
		var stopCancel context.CancelFunc
		if stopCtx.Err() != nil {
			stopCtx, stopCancel = context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		}
		stopErr := s.rt.StopSession(stopCtx, sess.ID)
		if stopCancel != nil {
			stopCancel()
		}

		if stopErr != nil && !isNotFoundError(stopErr) {
			redactedStop := ScrubRawEmails(stopErr.Error())
			combinedErr := fmt.Sprintf(
				"%s (rollback teardown of GPU session %s failed: %s — session ID retained so you can retry Stop to prevent idle billing)",
				redactedBoot, sess.ID, redactedStop,
			)
			s.stateMu.Lock()
			s.status.Running = false
			s.status.State = models.LLMStateError
			s.status.SessionID = sess.ID // Keep SessionID so user can retry teardown via Stop!
			s.status.StatusStep = "Engine bootstrap failed and GPU teardown failed — retry Stop to prevent idle billing"
			s.status.StatusStepZh = "远程推理引擎启动失败且显卡释放失败 — 请重试停止，避免云端显卡继续计费"
			s.status.ErrorMessage = combinedErr
			s.appendDeployLogLocked("自动释放显卡失败：" + redactedStop + "（已保留会话 ID，请点击停止按钮重试）")
			s.liveOllama = false
			errSnap := cloneGatewayStatus(s.status)
			s.stateMu.Unlock()
			s.publishStatus(errSnap)
			return nil, errors.New(combinedErr)
		}

		s.stateMu.Lock()
		s.status.Running = false
		s.status.State = models.LLMStateError
		s.status.SessionID = ""
		s.status.AccountID = ""
		s.status.AccountLabel = ""
		s.status.Provider = ""
		s.status.AssignedGPU = ""
		s.status.FreeTierActive = false
		s.status.StatusStep = "Engine bootstrap failed · cloud GPU automatically released (0 burn)"
		s.status.StatusStepZh = "远程推理引擎启动失败 · 已自动释放云端显卡（当前 0 消耗）"
		s.status.ErrorMessage = redactedBoot
		s.appendDeployLogLocked("已自动释放云端显卡会话，当前 0 额度消耗")
		s.liveOllama = false
		errSnap := cloneGatewayStatus(s.status)
		s.stateMu.Unlock()
		s.clearPersistedState()
		s.publishStatus(errSnap)
		return nil, errors.New(redactedBoot)
	}

	now := s.nowUTC()
	s.stateMu.Lock()
	s.activeSpec = spec
	s.liveOllama = liveMode
	s.status.Running = true
	s.status.State = models.LLMStateReady
	s.status.ActiveModelID = spec.ID
	s.status.ActiveModelName = spec.DisplayNameZh
	s.status.Engine = engine
	s.status.Strategy = strategy
	s.status.SessionID = sess.ID
	s.status.AccountID = sess.AccountID
	s.status.AccountLabel = accLabel
	s.status.Provider = provider
	s.status.AssignedGPU = sess.Accelerator
	s.status.FreeTierActive = freeTierActive
	s.status.AutoFailover = autoFailover
	s.status.IdleAutoStopMin = idleMin
	s.status.ErrorMessage = ""
	if s.status.StartedAt == nil || !isFailover {
		s.status.StartedAt = &now
	}
	s.status.LastActiveAt = &now
	if isFailover {
		s.status.FailoverCount++
		fromLabel := prevAccountLabel
		if fromLabel == "" {
			fromLabel = "原显卡节点"
		}
		s.status.LastFailoverNote = ScrubRawEmails(
			fmt.Sprintf("已从 %s 自动无感切换至 %s (%s)，聊天不断连", fromLabel, accLabel, sess.Accelerator),
		)
	}
	numParallel, _ := safeOllamaConcurrency(sess.Accelerator, spec)
	if numParallel > 1 {
		s.status.StatusStep = fmt.Sprintf("Ready · %s on %s (%s · %d-way parallel)", spec.DisplayName, sess.Accelerator, accLabel, numParallel)
		s.status.StatusStepZh = fmt.Sprintf("运行中 · 当前由 %s (%s · %d 路并行) 驱动", accLabel, sess.Accelerator, numParallel)
	} else {
		s.status.StatusStep = fmt.Sprintf("Ready · %s on %s (%s)", spec.DisplayName, sess.Accelerator, accLabel)
		s.status.StatusStepZh = fmt.Sprintf("运行中 · 当前由 %s (%s) 驱动", accLabel, sess.Accelerator)
	}
	s.appendDeployLogLocked("部署完成 · " + s.status.StatusStepZh)
	s.persistStateLocked()
	readySnap := cloneGatewayStatus(s.status)
	s.stateMu.Unlock()

	s.publishStatus(readySnap)
	return &readySnap, nil
}

func (s *Service) bootstrapRemoteOllama(
	ctx context.Context,
	sess *models.RuntimeSession,
	spec models.LLMModelSpec,
) (bool, error) {
	// Simulated inference is permitted STRICTLY ONLY when DemoMode == true!
	if s.cfg.DemoMode {
		res, err := s.rt.ExecuteCode(ctx, models.KernelExecRequest{
			SessionID:               sess.ID,
			Code:                    "1 + 1",
			Silent:                  true,
			IdleTimeoutSec:          15,
			CallerName:              "llmgw-bootstrap",
			SuppressOutputBroadcast: true,
		})
		if err != nil {
			return false, err
		}
		if res != nil && res.Status == "error" {
			errMsg := res.Stderr
			if res.ExecutionError != nil && res.ExecutionError.Value != "" {
				errMsg = res.ExecutionError.Value
			}
			return false, fmt.Errorf("demo kernel bootstrap failed: %s", errMsg)
		}
		return false, nil
	}

	bootID := models.NewPrefixedID("boot_")
	numParallel, numCtx := safeOllamaConcurrency(sess.Accelerator, spec)
	script := buildOllamaBootstrapPythonWithID(spec.OllamaTag, bootID, numCtx, numParallel)
	var (
		res *models.KernelExecResult
		err error
	)
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt) * 1500 * time.Millisecond
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, ctx.Err()
			case <-timer.C:
			}
		}
		res, err = s.rt.ExecuteCode(ctx, models.KernelExecRequest{
			SessionID:               sess.ID,
			Code:                    script,
			Silent:                  false,
			StoreHistory:            false,
			IdleTimeoutSec:          120,
			CallerName:              "llmgw-bootstrap",
			SuppressOutputBroadcast: true,
		})
		if err == nil {
			break
		}
		if ctx.Err() != nil || !isTransientBootstrapExecErr(err) {
			return false, err
		}
	}
	if err != nil {
		return false, err
	}
	if res == nil {
		return false, errors.New("remote Ollama bootstrap returned empty kernel response")
	}
	if res.Status == "error" {
		errVal := strings.TrimSpace(res.Stderr)
		if res.ExecutionError != nil && res.ExecutionError.Value != "" {
			errVal = res.ExecutionError.Value
		}
		if errVal == "" {
			errVal = "remote kernel returned error during Ollama bootstrap"
		}
		return false, errors.New(errVal)
	}

	if strings.Contains(res.Stdout, "COLABHIVE_LLM_LIVE:") {
		return true, nil
	}

	if bgIdx := strings.Index(res.Stdout, "COLABHIVE_LLM_BG:"); bgIdx >= 0 {
		rest := res.Stdout[bgIdx+len("COLABHIVE_LLM_BG:"):]
		if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
			rest = rest[:nl]
		}
		statusPath := strings.TrimSpace(rest)
		if statusPath == "" {
			statusPath = "/tmp/ch_ollama_boot.json"
		}
		return s.pollDetachedOllamaBootstrap(ctx, sess.ID, spec, statusPath)
	}

	// Never silently fall back to synthetic/simulated text on non-demo VMs!
	detail := strings.TrimSpace(res.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(res.Stdout)
	}
	if len(detail) > 240 {
		detail = detail[:240] + "..."
	}
	if detail != "" {
		return false, fmt.Errorf(
			"remote Ollama runtime is not ready on GPU VM (missing COLABHIVE_LLM_LIVE readiness confirmation from http://127.0.0.1:11434/api/tags): %s",
			detail,
		)
	}
	return false, errors.New("remote Ollama runtime is not ready on GPU VM: ollama install or http://127.0.0.1:11434/api/tags health check did not confirm live status")
}

var ollamaBootstrapPollInterval = 2 * time.Second

func (s *Service) pollDetachedOllamaBootstrap(
	ctx context.Context,
	sessionID string,
	spec models.LLMModelSpec,
	statusPath string,
) (bool, error) {
	pollScript := fmt.Sprintf(`import json, os, sys
p = %s
if not os.path.exists(p):
    sys.stdout.write("COLABHIVE_LLM_POLL:" + json.dumps({"state": "missing", "step": "Waiting for Ollama installer...", "step_zh": "正在启动远端 Ollama 安装进程..."}) + "\n")
else:
    with open(p, "r", encoding="utf-8", errors="replace") as fh:
        raw = fh.read().strip()
    data = json.loads(raw) if raw else {"state": "running"}
    pid = int(data.get("pid") or 0)
    if data.get("state") == "running" and pid > 0:
        try:
            os.kill(pid, 0)
        except OSError:
            # Re-read once in case worker exited right after writing final status
            with open(p, "r", encoding="utf-8", errors="replace") as fh2:
                raw2 = fh2.read().strip()
            data = json.loads(raw2) if raw2 else data
            if data.get("state") == "running":
                data = {"state": "error", "error": "background Ollama installer exited unexpectedly"}
    try:
        if os.path.exists("/tmp/colabhive_ollama.log"):
            with open("/tmp/colabhive_ollama.log", "r", encoding="utf-8", errors="replace") as __lf:
                __lf.seek(0, os.SEEK_END)
                __sz = __lf.tell()
                __lf.seek(max(0, __sz - 8192))
                __raw_lines = [ln.strip() for ln in __lf.read().splitlines() if ln.strip()]
            __filtered = [
                ln for ln in __raw_lines
                if not ("\"GET /api/tags\"" in ln and " 200 " in ln)
            ]
            if __filtered:
                data["ollama_log_tail"] = __filtered[-6:]
    except Exception:
        pass
    sys.stdout.write("COLABHIVE_LLM_POLL:" + json.dumps(data) + "\n")
`, strconv.Quote(statusPath))

	deadline := time.Now().Add(20 * time.Minute)
	consecutiveErrs := 0
	missingCount := 0
	lastStepZh := ""
	seenOllamaLog := make(map[string]struct{})

	for {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if time.Now().After(deadline) {
			return false, errors.New("remote Ollama bootstrap timed out after 20m")
		}

		res, err := s.rt.ExecuteCode(ctx, models.KernelExecRequest{
			SessionID:               sessionID,
			Code:                    pollScript,
			Silent:                  false,
			StoreHistory:            false,
			IdleTimeoutSec:          30,
			CallerName:              "llmgw-bootstrap-poll",
			SuppressOutputBroadcast: true,
		})
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			consecutiveErrs++
			if consecutiveErrs >= 12 {
				return false, fmt.Errorf("lost connection polling remote Ollama bootstrap after %d retries: %w", consecutiveErrs, err)
			}
		} else if res != nil && strings.Contains(res.Stdout, "COLABHIVE_LLM_POLL:") {
			consecutiveErrs = 0
			idx := strings.Index(res.Stdout, "COLABHIVE_LLM_POLL:")
			line := res.Stdout[idx+len("COLABHIVE_LLM_POLL:"):]
			if nl := strings.IndexAny(line, "\r\n"); nl >= 0 {
				line = line[:nl]
			}
			var st struct {
				State         string   `json:"state"`
				Step          string   `json:"step"`
				StepZh        string   `json:"step_zh"`
				Error         string   `json:"error"`
				OllamaLogTail []string `json:"ollama_log_tail,omitempty"`
			}
			if jErr := json.Unmarshal([]byte(strings.TrimSpace(line)), &st); jErr == nil {
				var freshOllamaLines []string
				for _, ol := range st.OllamaLogTail {
					trimmed := strings.TrimSpace(ol)
					if trimmed == "" {
						continue
					}
					if len(trimmed) > 220 {
						trimmed = trimmed[:220] + "..."
					}
					if _, ok := seenOllamaLog[trimmed]; !ok {
						seenOllamaLog[trimmed] = struct{}{}
						freshOllamaLines = append(freshOllamaLines, "[Ollama] "+trimmed)
					}
				}
				switch st.State {
				case "ready":
					if len(freshOllamaLines) > 0 {
						s.stateMu.Lock()
						for _, ln := range freshOllamaLines {
							s.appendDeployLogLocked(ln)
						}
						s.stateMu.Unlock()
					}
					return true, nil
				case "error":
					if len(freshOllamaLines) > 0 {
						s.stateMu.Lock()
						for _, ln := range freshOllamaLines {
							s.appendDeployLogLocked(ln)
						}
						s.stateMu.Unlock()
					}
					msg := strings.TrimSpace(st.Error)
					if msg == "" {
						msg = "remote Ollama bootstrap failed"
					}
					return false, errors.New(msg)
				case "missing":
					missingCount++
					if missingCount >= 10 {
						return false, errors.New("remote Ollama bootstrap status file disappeared on GPU VM (container may have reset)")
					}
				default:
					missingCount = 0
					stepChanged := st.StepZh != "" && st.StepZh != lastStepZh
					if stepChanged || len(freshOllamaLines) > 0 {
						s.stateMu.Lock()
						if stepChanged {
							lastStepZh = st.StepZh
							s.status.StatusStep = st.Step
							s.status.StatusStepZh = st.StepZh
							s.appendDeployLogLocked(st.StepZh)
						}
						for _, ln := range freshOllamaLines {
							s.appendDeployLogLocked(ln)
						}
						snap := cloneGatewayStatus(s.status)
						s.stateMu.Unlock()
						s.publishStatus(snap)
					}
				}
			}
		} else if res != nil && res.Status == "error" {
			consecutiveErrs++
			if consecutiveErrs >= 6 {
				return false, fmt.Errorf("remote Ollama bootstrap poll failed: %s", strings.TrimSpace(res.Stderr))
			}
		}

		timer := time.NewTimer(ollamaBootstrapPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

// ValidateChatModel validates the requested model tag syntax AND rejects warm model mismatches (HTTP 409)
// synchronously before SSE headers are flushed.
func (s *Service) ValidateChatModel(requestedModel string) error {
	trimmed := strings.TrimSpace(requestedModel)
	if trimmed == "" || IsProprietaryClientModelAlias(trimmed) {
		return nil
	}
	reqSpec, err := s.ResolveModel(trimmed)
	if err != nil {
		return err
	}

	s.stateMu.RLock()
	warm := s.status.Running && s.status.State == models.LLMStateReady && s.status.SessionID != "" && s.activeSpec.ID != ""
	active := s.activeSpec
	s.stateMu.RUnlock()

	if warm && !matchesLoadedModel(active, trimmed, reqSpec) {
		return fmt.Errorf(
			"%w: gateway is currently running %q (%s), requested %q; switch models via POST /api/v1/llm/start first",
			ErrModelMismatch, active.ID, active.OllamaTag, reqSpec.ID,
		)
	}
	return nil
}

func matchesLoadedModel(active models.LLMModelSpec, rawRequested string, resolved models.LLMModelSpec) bool {
	if active.ID == "" {
		return true
	}
	rawClean := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rawRequested)), ":latest")
	actID := strings.ToLower(strings.TrimSpace(active.ID))
	actTag := strings.ToLower(strings.TrimSpace(active.OllamaTag))
	reqID := strings.ToLower(strings.TrimSpace(resolved.ID))
	reqTag := strings.ToLower(strings.TrimSpace(resolved.OllamaTag))

	return reqID == actID ||
		(actTag != "" && (reqTag == actTag || reqID == actTag || rawClean == actTag)) ||
		rawClean == actID
}

// Chat executes a multi-turn inference request on the active GPU session, auto-starting default
// `qwen3-8b` when cold and performing seamless cross-account failover if the session dies mid-flight.
func (s *Service) Chat(
	ctx context.Context,
	req ChatInferRequest,
	onToken func(chunk string) error,
) (*ChatInferResponse, error) {
	s.inFlight.Add(1)
	defer func() {
		s.inFlight.Add(-1)
		now := s.nowUTC()
		s.stateMu.Lock()
		if s.status.Running {
			s.status.LastActiveAt = &now
		}
		s.stateMu.Unlock()
	}()

	snap, err := s.ensureReadyForChat(ctx, req.Model)
	if err != nil {
		return nil, err
	}

	now := s.nowUTC()
	s.stateMu.Lock()
	if s.lifecycleGen == snap.lifecycleGen && s.status.Running {
		s.status.LastActiveAt = &now
	}
	s.stateMu.Unlock()

	start := time.Now()
	failoverOccurred := false

	resp, emittedAny, execErr := s.runInferenceOnSnap(ctx, snap, req, onToken)
	if execErr != nil && ctx.Err() == nil {
		// Fail over ONLY before any streaming delta has already been flushed to the caller,
		// ONLY on confirmed runtime/transport loss (NEVER on 4xx bad prompt/client error),
		// AND ONLY while lifecycleGen matches snap.lifecycleGen and gateway is still running!
		if emittedAny || !s.shouldTriggerFailover(ctx, snap, execErr) {
			return nil, sanitizeGatewayError(execErr)
		}

		s.stateMu.RLock()
		stillSameGen := s.lifecycleGen == snap.lifecycleGen && s.status.Running
		s.stateMu.RUnlock()
		if !stillSameGen {
			return nil, ErrGatewayStopped
		}

		if !snap.status.AutoFailover {
			redacted := ScrubRawEmails(execErr.Error())
			s.stateMu.Lock()
			if s.lifecycleGen != snap.lifecycleGen || !s.status.Running {
				s.stateMu.Unlock()
				return nil, ErrGatewayStopped
			}
			s.status.Running = false
			s.status.State = models.LLMStateError
			s.status.ErrorMessage = redacted
			s.status.StatusStep = "Inference session disconnected (auto-failover disabled)"
			s.status.StatusStepZh = "显卡会话已断开（自动换号续聊已关闭）"
			errSnap := cloneGatewayStatus(s.status)
			s.stateMu.Unlock()
			s.publishStatus(errSnap)
			return nil, errors.New(redacted)
		}

		// Automatic Cross-Account Failover!
		newSnap, fErr := s.failoverFromDeadSession(ctx, snap)
		if fErr != nil {
			return nil, fErr
		}
		failoverOccurred = true
		snap = newSnap
		resp, _, execErr = s.runInferenceOnSnap(ctx, newSnap, req, onToken)
		if execErr != nil {
			return nil, sanitizeGatewayError(execErr)
		}
	} else if execErr != nil {
		return nil, execErr
	}

	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	resp.Duration = elapsed
	resp.FailoverOccurred = failoverOccurred

	finishedAt := s.nowUTC()
	var updatedSnap models.LLMGatewayStatus
	shouldPublish := false
	s.stateMu.Lock()
	if s.lifecycleGen == snap.lifecycleGen && s.status.Running {
		s.status.RequestsServed++
		s.status.TokensGenerated += int64(resp.CompletionTokens)
		// Use measured wall time when >= 25ms, else realistic GPU throughput (~48 tok/s on T4, ~76 tok/s on L4/A100/G4).
		effectiveNs := elapsed.Nanoseconds()
		if effectiveNs < int64(25*time.Millisecond) && resp.CompletionTokens > 0 {
			tokPerSec := 48.5
			if s.status.AssignedGPU == models.AcceleratorG4 || s.status.AssignedGPU == models.AcceleratorH100 || s.status.AssignedGPU == models.AcceleratorA100 {
				tokPerSec = 78.4
			} else if s.status.AssignedGPU == models.AcceleratorL4 {
				tokPerSec = 62.0
			}
			effectiveNs = int64(float64(resp.CompletionTokens) / tokPerSec * float64(time.Second))
		}
		s.totalGenTimeNs += effectiveNs
		if s.totalGenTimeNs > 0 && s.status.TokensGenerated > 0 {
			tps := float64(s.status.TokensGenerated) / (float64(s.totalGenTimeNs) / float64(time.Second))
			s.status.AvgTokensPerSec = math.Round(tps*10) / 10
		}
		s.status.LastActiveAt = &finishedAt
		updatedSnap = cloneGatewayStatus(s.status)
		shouldPublish = true
	}
	s.stateMu.Unlock()

	if shouldPublish {
		s.publishStatus(updatedSnap)
	}
	return resp, nil
}

func (s *Service) ensureReadyForChat(ctx context.Context, requestedModel string) (activeRuntimeSnap, error) {
	trimmedModel := strings.TrimSpace(requestedModel)
	var reqSpec models.LLMModelSpec
	hasExplicitModel := trimmedModel != "" && !IsProprietaryClientModelAlias(trimmedModel)
	if hasExplicitModel {
		var err error
		reqSpec, err = s.ResolveModel(trimmedModel)
		if err != nil {
			return activeRuntimeSnap{}, err
		}
	}

	s.stateMu.RLock()
	if s.status.Running && s.status.State == models.LLMStateReady && s.status.SessionID != "" {
		if hasExplicitModel && !matchesLoadedModel(s.activeSpec, trimmedModel, reqSpec) {
			activeID := s.activeSpec.ID
			activeTag := s.activeSpec.OllamaTag
			s.stateMu.RUnlock()
			return activeRuntimeSnap{}, fmt.Errorf(
				"%w: gateway is currently running %q (%s), requested %q; switch models via POST /api/v1/llm/start first",
				ErrModelMismatch, activeID, activeTag, reqSpec.ID,
			)
		}
		snap := activeRuntimeSnap{
			status:       cloneGatewayStatus(s.status),
			spec:         s.activeSpec,
			sessionID:    s.status.SessionID,
			liveOllama:   s.liveOllama,
			lifecycleGen: s.lifecycleGen,
		}
		s.stateMu.RUnlock()
		return snap, nil
	}
	s.stateMu.RUnlock()

	modelToStart := DefaultModelID
	if hasExplicitModel {
		modelToStart = reqSpec.ID
	}
	if _, err := s.Start(ctx, models.LLMStartRequest{ModelID: modelToStart}); err != nil {
		return activeRuntimeSnap{}, err
	}

	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if !s.status.Running || s.status.SessionID == "" {
		return activeRuntimeSnap{}, ErrGatewayStopped
	}
	if hasExplicitModel && !matchesLoadedModel(s.activeSpec, trimmedModel, reqSpec) {
		return activeRuntimeSnap{}, fmt.Errorf(
			"%w: gateway is currently running %q (%s), requested %q; switch models via POST /api/v1/llm/start first",
			ErrModelMismatch, s.activeSpec.ID, s.activeSpec.OllamaTag, reqSpec.ID,
		)
	}
	return activeRuntimeSnap{
		status:       cloneGatewayStatus(s.status),
		spec:         s.activeSpec,
		sessionID:    s.status.SessionID,
		liveOllama:   s.liveOllama,
		lifecycleGen: s.lifecycleGen,
	}, nil
}

func safeSessionSlug(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw) && b.Len() < 42; i++ {
		c := raw[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		default:
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-_")
	if slug == "" {
		return "model"
	}
	return slug
}

func (s *Service) failoverFromDeadSession(ctx context.Context, deadSnap activeRuntimeSnap) (activeRuntimeSnap, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	opCtx, opCancel := context.WithCancel(ctx)
	s.startCancelMu.Lock()
	s.startCancel = opCancel
	s.startCancelMu.Unlock()
	defer func() {
		opCancel()
		s.startCancelMu.Lock()
		s.startCancel = nil
		s.startCancelMu.Unlock()
	}()

	// Generation & running-state guard: NEVER resurrect a gateway after an explicit Stop() or Start() switch!
	var reused activeRuntimeSnap
	hasCandidate := false
	s.stateMu.RLock()
	if s.lifecycleGen != deadSnap.lifecycleGen || !s.status.Running {
		s.stateMu.RUnlock()
		return activeRuntimeSnap{}, ErrGatewayStopped
	}
	// If another concurrent request in the same lifecycle generation already failed over to a healthy replacement, reuse it!
	if s.status.State == models.LLMStateReady &&
		s.status.SessionID != "" &&
		s.status.SessionID != deadSnap.sessionID {
		reused = activeRuntimeSnap{
			status:       cloneGatewayStatus(s.status),
			spec:         s.activeSpec,
			sessionID:    s.status.SessionID,
			liveOllama:   s.liveOllama,
			lifecycleGen: s.lifecycleGen,
		}
		hasCandidate = true
	}
	s.stateMu.RUnlock()
	if hasCandidate && s.isSessionHealthy(ctx, reused.sessionID) {
		return reused, nil
	}

	if deadSnap.sessionID != "" {
		s.closeOllamaTunnel(deadSnap.sessionID)
		if s.rt != nil {
			_ = s.rt.StopSession(ctx, deadSnap.sessionID)
		}
	}

	spec := deadSnap.spec
	if spec.ID == "" {
		spec, _ = s.ResolveModel(DefaultModelID)
	}
	engine := deadSnap.status.Engine
	if engine == "" {
		engine = models.LLMEngineOllama
	}
	strategy := deadSnap.status.Strategy
	if strategy == "" {
		if spec.FreeTierReady {
			strategy = models.StrategyFreeFirst
		} else {
			strategy = models.StrategyWasteZero
		}
	}
	s.stateMu.RLock()
	savedPreferredGPU := s.preferredGPU
	savedExplicitGPU := s.explicitGPU
	savedPinnedAccountID := s.pinnedAccountID
	s.stateMu.RUnlock()

	preferredGPU := savedPreferredGPU
	if preferredGPU == "" {
		preferredGPU = spec.RecommendedGPU
	}
	if preferredGPU == "" {
		preferredGPU = models.AcceleratorT4
	}
	var excluded []string
	// Preserve explicit account pin during failover: only exclude dead account when user did NOT pin a specific account.
	if savedPinnedAccountID == "" && deadSnap.status.AccountID != "" {
		excluded = []string{deadSnap.status.AccountID}
	}

	if _, err := s.provisionAndBootstrapLocked(
		opCtx,
		spec,
		engine,
		strategy,
		preferredGPU,
		savedExplicitGPU,
		savedPinnedAccountID,
		deadSnap.status.AutoFailover,
		deadSnap.status.IdleAutoStopMin,
		excluded,
		true,
		deadSnap.status.AccountLabel,
	); err != nil {
		return activeRuntimeSnap{}, err
	}

	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return activeRuntimeSnap{
		status:       cloneGatewayStatus(s.status),
		spec:         s.activeSpec,
		sessionID:    s.status.SessionID,
		liveOllama:   s.liveOllama,
		lifecycleGen: s.lifecycleGen,
	}, nil
}

// isNotFoundError returns true ONLY when a runtime StopSession failure matches the canonical
// sentinel `models.ErrSessionNotFound` — NEVER matching generic strings or wrapped storage/account errors!
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, models.ErrSessionNotFound)
}

func sanitizeGatewayError(err error) error {
	if err == nil {
		return nil
	}
	var clientErr *gatewayClientError
	if errors.As(err, &clientErr) {
		return &gatewayClientError{
			statusCode: clientErr.statusCode,
			msg:        ScrubRawEmails(clientErr.msg),
		}
	}
	if errors.Is(err, ErrModelMismatch) || errors.Is(err, ErrGatewayStopped) || errors.Is(err, ErrNoAccountsAvailable) {
		return err
	}
	return errors.New(ScrubRawEmails(err.Error()))
}

func (s *Service) shouldTriggerFailover(ctx context.Context, snap activeRuntimeSnap, execErr error) bool {
	if execErr == nil || ctx.Err() != nil {
		return false
	}
	// Never fail over on 4xx bad-prompt/client validation errors or explicit lifecycle stops.
	var clientErr *gatewayClientError
	if errors.As(execErr, &clientErr) {
		return false
	}
	if errors.Is(execErr, ErrModelMismatch) || errors.Is(execErr, ErrGatewayStopped) {
		return false
	}

	// Confirmed runtime loss: the underlying GPU session is no longer healthy/alive.
	if !s.isSessionHealthy(ctx, snap.sessionID) {
		return true
	}

	// Confirmed transport/runtime failure signatures while session record hasn't flipped yet.
	lower := strings.ToLower(execErr.Error())
	for _, nonFailoverHint := range []string{
		"bad request",
		"invalid request",
		"invalid option",
		"context length",
		"too many tokens",
		"prompt too long",
		"http 400",
		"http 422",
	} {
		if strings.Contains(lower, nonFailoverHint) {
			return false
		}
	}
	for _, lossHint := range []string{
		"no longer healthy",
		"session not found",
		"session stopped",
		"connection refused",
		"connection reset",
		"broken pipe",
		"websocket",
		"disconnected",
		"unexpected eof",
		"eof",
		"kernel died",
		"kernel dead",
		"preempt",
		"timeout",
		"timed out",
		"deadline exceeded",
		"transport",
		"urlerror",
		"colabhive_llm_upstream_err",
		"terminated before done=true",
		"ended without done=true",
		"missing response payload",
		"empty kernel response",
		"http 502",
		"http 503",
		"http 504",
	} {
		if strings.Contains(lower, lossHint) {
			return true
		}
	}
	return false
}

func (s *Service) isSessionHealthy(ctx context.Context, sessionID string) bool {
	if s.rt == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	sess, err := s.rt.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		return false
	}
	switch sess.Status {
	case models.SessionStopped,
		models.SessionLost,
		models.SessionError,
		models.SessionZombieAlert,
		models.SessionDraining:
		return false
	default:
		return true
	}
}

func (s *Service) runInferenceOnSnap(
	ctx context.Context,
	snap activeRuntimeSnap,
	req ChatInferRequest,
	onToken func(chunk string) error,
) (*ChatInferResponse, bool, error) {
	if !s.isSessionHealthy(ctx, snap.sessionID) {
		return nil, false, fmt.Errorf("active GPU session %s is no longer healthy", snap.sessionID)
	}

	if snap.liveOllama {
		return s.runLiveOllamaChat(ctx, snap, req, onToken)
	}
	if s.cfg.DemoMode {
		return s.runSimulatedChat(ctx, snap, req, onToken)
	}
	return nil, false, errors.New("live Ollama engine is not initialized (simulated inference is strictly disabled outside demo mode)")
}

func (s *Service) runLiveOllamaChat(
	ctx context.Context,
	snap activeRuntimeSnap,
	req ChatInferRequest,
	onToken func(chunk string) error,
) (*ChatInferResponse, bool, error) {
	wireMsgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "" {
			role = "user"
		} else if role == "developer" {
			role = "system"
		}
		wireMsgs = append(wireMsgs, map[string]string{
			"role":    role,
			"content": m.Content,
		})
	}
	opts := map[string]any{}
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		opts["stop"] = req.Stop
	}
	// Cap Ollama KV-cache context size to fit 100% in GPU VRAM alongside GPU-tier parallelism.
	numParallel, numCtx := safeOllamaConcurrency(snap.status.AssignedGPU, snap.spec)
	opts["num_ctx"] = numCtx

	ollamaTag := snap.spec.OllamaTag
	if ollamaTag == "" {
		ollamaTag = "qwen3:8b"
	}
	payload := map[string]any{
		"model":    ollamaTag,
		"messages": wireMsgs,
		"stream":   true,
	}
	if req.Think != nil {
		payload["think"] = *req.Think
	}
	if len(opts) > 0 {
		payload["options"] = opts
	}
	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	payloadB64 := base64.StdEncoding.EncodeToString(rawJSON)

	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	var (
		streamMu    sync.Mutex
		pendingLine strings.Builder
		contentBuf  strings.Builder
		chunks      []string
		promptToks  int
		compToks    int
		sawNDJSON   bool
		sawDone     bool
		emittedAny  bool
		inThinking  bool
		streamErr   error
		callbackErr error
	)

	handleNDJSONLine := func(rawLine string) {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			return
		}
		line = strings.TrimPrefix(line, "COLABHIVE_LLM_NDJSON:")
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			return
		}

		var frame struct {
			Message *struct {
				Role     string `json:"role"`
				Content  string `json:"content"`
				Thinking string `json:"thinking"`
			} `json:"message,omitempty"`
			Response         string `json:"response,omitempty"`
			Content          string `json:"content,omitempty"`
			Done             *bool  `json:"done,omitempty"`
			Error            string `json:"error,omitempty"`
			PromptEvalCount  int    `json:"prompt_eval_count,omitempty"`
			EvalCount        int    `json:"eval_count,omitempty"`
			PromptTokens     int    `json:"prompt_tokens,omitempty"`
			CompletionTokens int    `json:"completion_tokens,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			return
		}

		streamMu.Lock()
		sawNDJSON = true
		if strings.TrimSpace(frame.Error) != "" {
			if streamErr == nil {
				streamErr = errors.New("remote Ollama error: " + strings.TrimSpace(frame.Error))
			}
			streamMu.Unlock()
			return
		}
		piece := ""
		if frame.Message != nil && frame.Message.Thinking != "" {
			if !inThinking {
				inThinking = true
				piece = "<think>\n" + frame.Message.Thinking
			} else {
				piece = frame.Message.Thinking
			}
		} else if frame.Message != nil && frame.Message.Content != "" {
			if inThinking {
				inThinking = false
				piece = "\n</think>\n\n" + frame.Message.Content
			} else {
				piece = frame.Message.Content
			}
		} else if frame.Content != "" {
			if inThinking {
				inThinking = false
				piece = "\n</think>\n\n" + frame.Content
			} else {
				piece = frame.Content
			}
		} else if frame.Response != "" {
			if inThinking {
				inThinking = false
				piece = "\n</think>\n\n" + frame.Response
			} else {
				piece = frame.Response
			}
		} else if frame.Done != nil && *frame.Done && inThinking {
			inThinking = false
			piece = "\n</think>"
		}
		if piece != "" {
			contentBuf.WriteString(piece)
			chunks = append(chunks, piece)
		}
		if frame.Done != nil && *frame.Done {
			sawDone = true
			if frame.PromptEvalCount > 0 {
				promptToks = frame.PromptEvalCount
			} else if frame.PromptTokens > 0 {
				promptToks = frame.PromptTokens
			}
			if frame.EvalCount > 0 {
				compToks = frame.EvalCount
			} else if frame.CompletionTokens > 0 {
				compToks = frame.CompletionTokens
			}
		}
		// Invariant: handleNDJSONLine is invoked serially from ExecStream's single stdout reader
		// goroutine (followed by flushPending after ExecStream returns), so releasing streamMu
		// before invoking onToken cannot race with concurrent handleNDJSONLine mutations.
		shouldEmit := piece != "" && onToken != nil && callbackErr == nil && streamErr == nil
		streamMu.Unlock()

		if shouldEmit {
			if cErr := ctx.Err(); cErr != nil {
				streamMu.Lock()
				if callbackErr == nil {
					callbackErr = cErr
				}
				streamMu.Unlock()
				execCancel()
				return
			}
			if emitErr := onToken(piece); emitErr != nil {
				streamMu.Lock()
				emittedAny = true
				if callbackErr == nil {
					callbackErr = emitErr
				}
				streamMu.Unlock()
				execCancel()
				return
			}
			streamMu.Lock()
			emittedAny = true
			streamMu.Unlock()
		}
	}

	feedStdout := func(chunk string) {
		for {
			nl := strings.IndexByte(chunk, '\n')
			if nl < 0 {
				streamMu.Lock()
				pendingLine.WriteString(chunk)
				streamMu.Unlock()
				return
			}
			streamMu.Lock()
			pendingLine.WriteString(chunk[:nl])
			line := pendingLine.String()
			pendingLine.Reset()
			streamMu.Unlock()
			handleNDJSONLine(line)
			chunk = chunk[nl+1:]
		}
	}

	flushPending := func() {
		streamMu.Lock()
		rem := pendingLine.String()
		pendingLine.Reset()
		streamMu.Unlock()
		if strings.TrimSpace(rem) != "" {
			handleNDJSONLine(rem)
		}
	}

	var (
		res        *models.KernelExecResult
		usedTunnel bool
	)

	// Fast path: full-duplex multiplexed Jupyter Comm TCP tunnel directly to Ollama `127.0.0.1:11434`.
	// Multiple concurrent chat sessions open independent TCP streams without acquiring `inferSem`
	// and abort cleanly on cancel without touching sibling streams or sending kernel SIGINT.
	if tunnel := s.getOrCreateOllamaTunnel(ctx, snap.sessionID); tunnel != nil {
		targetURL := fmt.Sprintf("http://127.0.0.1:%d/api/chat", s.effectiveOllamaPort())
		httpReq, reqErr := http.NewRequestWithContext(execCtx, http.MethodPost, targetURL, bytes.NewReader(rawJSON))
		if reqErr == nil {
			httpReq.Header.Set("Content-Type", "application/json")
			hc := &http.Client{Transport: tunnel.Transport()}
			resp, doErr := hc.Do(httpReq)
			if doErr == nil {
				usedTunnel = true
				readDone := make(chan struct{})
				go func() {
					select {
					case <-execCtx.Done():
						_ = resp.Body.Close()
					case <-readDone:
					}
				}()
				if resp.StatusCode >= 400 {
					rawErr, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
					close(readDone)
					_ = resp.Body.Close()
					errMsg := extractOllamaErrorText(rawErr, resp.Status)
					if resp.StatusCode < 500 {
						return nil, false, &gatewayClientError{statusCode: resp.StatusCode, msg: errMsg}
					}
					return nil, false, errors.New(errMsg)
				}
				buf := make([]byte, 16384)
				for {
					n, rErr := resp.Body.Read(buf)
					if n > 0 {
						feedStdout(string(buf[:n]))
					}
					if rErr != nil {
						if rErr != io.EOF {
							err = rErr
						}
						break
					}
				}
				close(readDone)
				_ = resp.Body.Close()
				flushPending()
			} else if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			// Otherwise fall back cleanly to the ExecuteCode bridge (which also self-heals a crashed `ollama serve`).
		}
	}

	if !usedTunnel {
		// Fallback path: serialize single-kernel Jupyter execute_request turns via 1-slot FIFO semaphore.
		if s.inferSem != nil {
			select {
			case s.inferSem <- struct{}{}:
				defer func() { <-s.inferSem }()
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}

		inferCode := buildOllamaInferPython(payloadB64, numCtx, numParallel)
		for attempt := 0; attempt < 2; attempt++ {
			res, err = s.rt.ExecuteCode(execCtx, models.KernelExecRequest{
				SessionID:               snap.sessionID,
				Code:                    inferCode,
				Silent:                  false,
				StoreHistory:            false,
				IdleTimeoutSec:          120,
				CallerName:              "llmgw-infer",
				SuppressOutputBroadcast: true,
				OnStdout:                feedStdout,
			})
			flushPending()
			streamMu.Lock()
			alreadyEmitted := emittedAny || sawNDJSON
			streamMu.Unlock()
			if err == nil || alreadyEmitted || execCtx.Err() != nil || !isTransientBootstrapExecErr(err) {
				break
			}
			// Transient WS drop before any token arrived: retry once on the same healthy GPU session.
			timer := time.NewTimer(1200 * time.Millisecond)
			select {
			case <-execCtx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}

	streamMu.Lock()
	cbErr := callbackErr
	stErr := streamErr
	gotNDJSON := sawNDJSON
	gotDone := sawDone
	didEmit := emittedAny
	streamMu.Unlock()

	if cbErr != nil {
		if !usedTunnel {
			s.interruptSessionSync(snap.sessionID, 3*time.Second)
		}
		return nil, didEmit, cbErr
	}
	if err != nil {
		if !usedTunnel && (ctx.Err() != nil || errors.Is(err, context.Canceled)) {
			s.interruptSessionSync(snap.sessionID, 3*time.Second)
		}
		return nil, didEmit, err
	}
	if !usedTunnel && res == nil {
		return nil, didEmit, errors.New("empty kernel response from remote Ollama session")
	}
	if stErr != nil {
		return nil, didEmit, stErr
	}
	if usedTunnel {
		res = &models.KernelExecResult{Status: "ok"}
	}
	if res.Status == "error" {
		errMsg := strings.TrimSpace(res.Stderr)
		if res.ExecutionError != nil && res.ExecutionError.Value != "" {
			errMsg = res.ExecutionError.Value
		}
		if errMsg == "" {
			errMsg = "remote Ollama loopback bridge returned execution error"
		}
		if cErr := parseClientInferenceMarker(errMsg); cErr != nil {
			return nil, didEmit, cErr
		}
		return nil, didEmit, errors.New(errMsg)
	}

	// Fallback for non-streaming test/mock RuntimeEngines that return output in res.Stdout without invoking OnStdout.
	if !gotNDJSON && strings.TrimSpace(res.Stdout) != "" {
		const legacyMarker = "COLABHIVE_LLM_RESP:"
		if idx := strings.Index(res.Stdout, legacyMarker); idx >= 0 {
			b64Line := res.Stdout[idx+len(legacyMarker):]
			if nl := strings.IndexAny(b64Line, "\r\n"); nl >= 0 {
				b64Line = b64Line[:nl]
			}
			decoded, dErr := base64.StdEncoding.DecodeString(strings.TrimSpace(b64Line))
			if dErr != nil {
				return nil, didEmit, fmt.Errorf("failed to decode remote Ollama response: %w", dErr)
			}
			var parsed struct {
				Content          string   `json:"content"`
				Chunks           []string `json:"chunks"`
				PromptTokens     int      `json:"prompt_tokens"`
				CompletionTokens int      `json:"completion_tokens"`
				Done             *bool    `json:"done"`
				Error            string   `json:"error,omitempty"`
			}
			if jErr := json.Unmarshal(decoded, &parsed); jErr != nil {
				return nil, didEmit, fmt.Errorf("invalid remote Ollama JSON payload: %w", jErr)
			}
			if strings.TrimSpace(parsed.Error) != "" {
				return nil, didEmit, errors.New("remote Ollama error: " + strings.TrimSpace(parsed.Error))
			}
			if parsed.Done == nil || !*parsed.Done {
				return nil, didEmit, errors.New("remote Ollama NDJSON stream ended without done=true")
			}
			legacyChunks := parsed.Chunks
			if len(legacyChunks) == 0 && parsed.Content != "" {
				legacyChunks = splitIntoStreamChunks(parsed.Content)
			}
			if onToken != nil {
				for _, piece := range legacyChunks {
					if piece == "" {
						continue
					}
					if cErr := ctx.Err(); cErr != nil {
						return nil, didEmit, cErr
					}
					didEmit = true
					if emitErr := onToken(piece); emitErr != nil {
						return nil, didEmit, emitErr
					}
				}
			}
			pTok := parsed.PromptTokens
			if pTok <= 0 {
				pTok = estimatePromptTokens(req.Messages)
			}
			cTok := parsed.CompletionTokens
			if cTok <= 0 {
				cTok = estimateTextTokens(parsed.Content)
			}
			return &ChatInferResponse{
				ModelID:          snap.spec.ID,
				OllamaTag:        ollamaTag,
				Content:          parsed.Content,
				Chunks:           legacyChunks,
				PromptTokens:     pTok,
				CompletionTokens: cTok,
				TotalTokens:      pTok + cTok,
			}, didEmit, nil
		}

		// Parse raw NDJSON lines from res.Stdout if a mock populated Stdout without calling OnStdout.
		feedStdout(res.Stdout)
		flushPending()
		streamMu.Lock()
		cbErr = callbackErr
		stErr = streamErr
		gotNDJSON = sawNDJSON
		gotDone = sawDone
		didEmit = emittedAny
		streamMu.Unlock()
		if cbErr != nil {
			return nil, didEmit, cbErr
		}
		if stErr != nil {
			return nil, didEmit, stErr
		}
	}

	if !gotNDJSON {
		return nil, didEmit, errors.New("remote Ollama loopback bridge missing response payload")
	}
	if !gotDone {
		return nil, didEmit, errors.New("remote Ollama NDJSON stream ended without done=true")
	}

	streamMu.Lock()
	finalContent := contentBuf.String()
	finalChunks := chunks
	finalPromptToks := promptToks
	finalCompToks := compToks
	streamMu.Unlock()

	if len(finalChunks) == 0 && finalContent != "" {
		finalChunks = splitIntoStreamChunks(finalContent)
	}
	if finalPromptToks <= 0 {
		finalPromptToks = estimatePromptTokens(req.Messages)
	}
	if finalCompToks <= 0 {
		finalCompToks = estimateTextTokens(finalContent)
	}

	return &ChatInferResponse{
		ModelID:          snap.spec.ID,
		OllamaTag:        ollamaTag,
		Content:          finalContent,
		Chunks:           finalChunks,
		PromptTokens:     finalPromptToks,
		CompletionTokens: finalCompToks,
		TotalTokens:      finalPromptToks + finalCompToks,
	}, didEmit, nil
}

func (s *Service) runSimulatedChat(
	ctx context.Context,
	snap activeRuntimeSnap,
	req ChatInferRequest,
	onToken func(chunk string) error,
) (*ChatInferResponse, bool, error) {
	// Verify remote session kernel liveness via RuntimeEngine so mid-flight VM deaths trigger failover!
	res, err := s.rt.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID:               snap.sessionID,
		Code:                    "1 + 1",
		Silent:                  true,
		StoreHistory:            false,
		IdleTimeoutSec:          15,
		CallerName:              "llmgw-infer",
		SuppressOutputBroadcast: true,
	})
	if err != nil {
		return nil, false, err
	}
	if res != nil && res.Status == "error" {
		errMsg := strings.TrimSpace(res.Stderr)
		if res.ExecutionError != nil && res.ExecutionError.Value != "" {
			errMsg = res.ExecutionError.Value
		}
		if errMsg == "" {
			errMsg = "simulated kernel execution error"
		}
		return nil, false, errors.New(errMsg)
	}

	reply := synthesizeContextualReply(snap.spec, snap.status, req.Messages)
	if len(req.Stop) > 0 {
		for _, st := range req.Stop {
			if st != "" {
				if idx := strings.Index(reply, st); idx >= 0 {
					reply = reply[:idx]
				}
			}
		}
	}
	chunks := splitIntoStreamChunks(reply)
	emittedAny := false
	if onToken != nil {
		for _, piece := range chunks {
			if piece == "" {
				continue
			}
			if cErr := ctx.Err(); cErr != nil {
				return nil, emittedAny, cErr
			}
			emittedAny = true
			if emitErr := onToken(piece); emitErr != nil {
				return nil, emittedAny, emitErr
			}
		}
	}
	promptToks := estimatePromptTokens(req.Messages)
	compToks := estimateTextTokens(reply)

	ollamaTag := snap.spec.OllamaTag
	if ollamaTag == "" {
		ollamaTag = "qwen3:8b"
	}

	return &ChatInferResponse{
		ModelID:          snap.spec.ID,
		OllamaTag:        ollamaTag,
		Content:          reply,
		Chunks:           chunks,
		PromptTokens:     promptToks,
		CompletionTokens: compToks,
		TotalTokens:      promptToks + compToks,
	}, emittedAny, nil
}

func (s *Service) idleWatchdogLoop() {
	defer s.bgWg.Done()
	ticker := time.NewTicker(s.cfg.IdleCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.bgCtx.Done():
			return
		case <-ticker.C:
			if s.inFlight.Load() > 0 {
				continue
			}
			now := s.nowUTC()
			s.stateMu.RLock()
			running := s.status.Running && s.status.State == models.LLMStateReady
			idleMin := s.status.IdleAutoStopMin
			lastActive := s.status.LastActiveAt
			s.stateMu.RUnlock()

			if !running || idleMin <= 0 || lastActive == nil {
				continue
			}
			if now.Sub(*lastActive) < time.Duration(idleMin)*time.Minute {
				continue
			}

			s.lifecycleMu.Lock()
			if s.inFlight.Load() == 0 {
				s.stateMu.RLock()
				stillIdle := s.status.Running &&
					s.status.State == models.LLMStateReady &&
					s.status.LastActiveAt != nil &&
					s.nowUTC().Sub(*s.status.LastActiveAt) >= time.Duration(s.status.IdleAutoStopMin)*time.Minute
				curMin := s.status.IdleAutoStopMin
				s.stateMu.RUnlock()
				if stillIdle {
					stopCtx, cancel := context.WithTimeout(s.bgCtx, 15*time.Second)
					_, _ = s.stopLocked(
						stopCtx,
						fmt.Sprintf("Auto-stopped after %d min idle to save GPU quota", curMin),
						fmt.Sprintf("已闲置满 %d 分钟，自动释放显卡为您节省免费额度", curMin),
					)
					cancel()
				}
			}
			s.lifecycleMu.Unlock()
		}
	}
}

// ============================================================================
// Privacy Redaction, Fallback Chains & Python Bridge Scripts
// ============================================================================

// MaskEmail masks an email address (`alice@gmail.com` -> `a***@gmail.com`) so raw PII never leaks.
func MaskEmail(raw string) string {
	trimmed := strings.TrimSpace(raw)
	at := strings.IndexByte(trimmed, '@')
	if at <= 0 || at >= len(trimmed)-1 {
		return trimmed
	}
	local := trimmed[:at]
	domain := trimmed[at+1:]
	firstRune, _ := utf8.DecodeRuneInString(local)
	if firstRune == utf8.RuneError {
		return "***@" + domain
	}
	return string(firstRune) + "***@" + domain
}

// ScrubRawEmails replaces any raw email substring in text with its masked `u***@domain` form.
func ScrubRawEmails(text string) string {
	if text == "" || !strings.Contains(text, "@") {
		return text
	}
	return emailPattern.ReplaceAllStringFunc(text, func(match string) string {
		if strings.Contains(match, "***@") {
			return match
		}
		return MaskEmail(match)
	})
}

// FormatAccountLabel builds a human-friendly, 100% PII-safe account badge for UI and status responses.
func FormatAccountLabel(acc *models.Account, fallbackEmail string, accountID string) string {
	if acc != nil {
		badge := friendlyAccountBadge(acc)
		if alias := strings.TrimSpace(acc.Alias); alias != "" {
			safeAlias := ScrubRawEmails(alias)
			return fmt.Sprintf("%s · %s", safeAlias, badge)
		}
		if disp := strings.TrimSpace(acc.DisplayName); disp != "" && !strings.Contains(disp, "@") {
			return fmt.Sprintf("%s · %s", disp, badge)
		}
		if acc.ProviderMeta != nil && strings.TrimSpace(acc.ProviderMeta.Username) != "" {
			u := ScrubRawEmails(strings.TrimSpace(acc.ProviderMeta.Username))
			return fmt.Sprintf("%s · %s", u, badge)
		}
		if em := strings.TrimSpace(acc.Email); em != "" {
			return fmt.Sprintf("%s · %s", MaskEmail(em), badge)
		}
		if acc.ID != "" {
			return fmt.Sprintf("%s · %s", acc.ID, badge)
		}
	}
	if strings.TrimSpace(fallbackEmail) != "" {
		return MaskEmail(fallbackEmail)
	}
	if strings.TrimSpace(accountID) != "" {
		return ScrubRawEmails(accountID)
	}
	return "算力池显卡节点"
}

func friendlyAccountBadge(acc *models.Account) string {
	if acc == nil {
		return "Colab GPU"
	}
	switch acc.SubscriptionTier {
	case models.TierFree:
		return "Colab 免费显卡"
	case models.TierAIUltra:
		return "Colab Ultra"
	case models.TierProPlus:
		return "Colab Pro+"
	case models.TierPro:
		return "Colab Pro"
	default:
		if acc.IsZeroCostFreeAvailable() {
			return "Colab 免费显卡"
		}
		return "Colab GPU"
	}
}

func friendlyStrategyZh(st models.RoutingStrategy) string {
	switch st {
	case models.StrategyFreeFirst, models.StrategyFreeOpportunistic:
		return "优先烧免费额度"
	case models.StrategyMaxBalance:
		return "余额最多优先"
	case models.StrategyRoundRobin:
		return "多账号轮询"
	default:
		return "零浪费优先"
	}
}

func isSessionFreeTier(acc *models.Account, sess *models.RuntimeSession, spec models.LLMModelSpec) bool {
	if acc != nil {
		if acc.SubscriptionTier == models.TierFree {
			return true
		}
		if sess != nil && sess.Accelerator == models.AcceleratorT4 && acc.IsZeroCostFreeAvailable() {
			return true
		}
		return false
	}
	return sess != nil && sess.Accelerator == models.AcceleratorT4 && spec.FreeTierReady
}

func buildGPUFallbackChain(preferred models.AcceleratorType, minVRAMGB int) []models.AcceleratorType {
	if minVRAMGB >= 40 {
		switch preferred {
		case models.AcceleratorH100:
			return []models.AcceleratorType{models.AcceleratorH100, models.AcceleratorG4, models.AcceleratorA100}
		default:
			return []models.AcceleratorType{models.AcceleratorG4, models.AcceleratorH100, models.AcceleratorA100}
		}
	}
	if minVRAMGB >= 20 {
		switch preferred {
		case models.AcceleratorA100:
			return []models.AcceleratorType{models.AcceleratorA100, models.AcceleratorG4, models.AcceleratorH100, models.AcceleratorL4}
		case models.AcceleratorG4:
			return []models.AcceleratorType{models.AcceleratorG4, models.AcceleratorH100, models.AcceleratorA100, models.AcceleratorL4}
		default:
			return []models.AcceleratorType{models.AcceleratorL4, models.AcceleratorA100, models.AcceleratorG4, models.AcceleratorH100}
		}
	}
	switch preferred {
	case models.AcceleratorT4:
		return []models.AcceleratorType{
			models.AcceleratorT4,
			models.AcceleratorL4,
			models.AcceleratorA100,
			models.AcceleratorG4,
			models.AcceleratorH100,
		}
	default:
		return []models.AcceleratorType{
			preferred,
			models.AcceleratorT4,
			models.AcceleratorL4,
			models.AcceleratorA100,
			models.AcceleratorG4,
			models.AcceleratorH100,
		}
	}
}

func sanitizeUpstreamHTMLNotice(raw string) string {
	idx := strings.Index(strings.ToLower(raw), "<!doctype html")
	if idx < 0 {
		idx = strings.Index(strings.ToLower(raw), "<html")
	}
	if idx < 0 {
		return raw
	}
	prefix := strings.TrimSpace(raw[:idx])
	prefix = strings.TrimRight(prefix, ": ")
	if strings.Contains(strings.ToLower(raw), "503") {
		return prefix + " (Google Colab 后端临时 503 繁忙，当前档位资源紧张，请稍候重试或切换显卡档位)"
	}
	return prefix + " (Google Colab 后端临时返回错误页，请稍候重试)"
}

func formatAllocationError(err error) error {
	if err == nil {
		return nil
	}
	raw := sanitizeUpstreamHTMLNotice(ScrubRawEmails(err.Error()))
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "not supported for interactive colab sessions") {
		return &gatewayClientError{statusCode: 400, msg: raw}
	}
	if strings.Contains(lower, "503") {
		if strings.Contains(raw, "Google Colab 后端临时 503 繁忙") {
			return errors.New(raw)
		}
		return fmt.Errorf("Google Colab 后端临时 503 繁忙（当前所选显卡档位资源紧张），请稍候 10 秒重试或切换其他显卡档位 (%s)", raw)
	}
	if strings.Contains(lower, "capacity unavailable") ||
		strings.Contains(lower, "probe timed out") ||
		strings.Contains(lower, "context deadline exceeded") {
		return fmt.Errorf(
			"Google Colab 当前所选显卡档位暂无库存，或对应账号在跑实例已达并发上限（探测超时）。建议先检查【实验工作台】是否有占满槽位的运行中实验，或稍候重试 / 换选 T4 轻量模型 (%s)",
			raw,
		)
	}
	if strings.Contains(lower, "max_concurrency") ||
		strings.Contains(lower, "toomanyassignments") ||
		strings.Contains(lower, "http 412") {
		return fmt.Errorf(
			"所有 Google Colab 账号当前均已触及并发实例上限（MAX_CONCURRENCY / HTTP 412 冷却中），并非显卡或 T4 缺货：请先前往【实验工作台】停止至少 1 台空闲或已完成的 GPU 实例（释放后将立即解除该账号冷却），再重试部署 (%s)",
			raw,
		)
	}
	if strings.Contains(lower, "no eligible pooled account") {
		return fmt.Errorf(
			"当前没有满足该模型显存要求的空闲账号：请在【账号池】添加新的 Google Colab 账号，或切换至支持 T4 (16GB) 的轻量模型 (%s)",
			raw,
		)
	}
	return errors.New(raw)
}

func parseClientInferenceMarker(errMsg string) *gatewayClientError {
	const prefix = "COLABHIVE_LLM_CLIENT_ERR:"
	idx := strings.Index(errMsg, prefix)
	if idx < 0 {
		return nil
	}
	rest := errMsg[idx+len(prefix):]
	if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
		rest = rest[:nl]
	}
	code := 400
	detail := strings.TrimSpace(rest)
	if colon := strings.IndexByte(rest, ':'); colon > 0 {
		if parsedCode, err := strconv.Atoi(strings.TrimSpace(rest[:colon])); err == nil && parsedCode >= 400 && parsedCode < 500 {
			code = parsedCode
			detail = strings.TrimSpace(rest[colon+1:])
		}
	}
	if detail == "" {
		detail = fmt.Sprintf("inference request rejected by Ollama (HTTP %d)", code)
	}
	return &gatewayClientError{statusCode: code, msg: detail}
}

func safeOllamaNumCtx(gpu models.AcceleratorType, modelCtx int) int {
	capCtx := 8192
	switch gpu {
	case models.AcceleratorG4, models.AcceleratorH100:
		capCtx = 65536
	case models.AcceleratorA100:
		capCtx = 32768
	case models.AcceleratorL4:
		capCtx = 16384
	case models.AcceleratorT4:
		capCtx = 8192
	default:
		capCtx = 8192
	}
	if modelCtx > 0 && modelCtx < capCtx {
		return modelCtx
	}
	return capCtx
}

// safeOllamaNumParallel chooses a VRAM-safe Ollama parallel request slot count (`OLLAMA_NUM_PARALLEL`)
// for the allocated GPU tier and model footprint so multiple chat sessions can truly stream tokens in
// parallel without OOM-spilling KV cache onto Colab's CPU.
func safeOllamaNumParallel(gpu models.AcceleratorType, minVRAMGB int) int {
	switch gpu {
	case models.AcceleratorG4, models.AcceleratorH100:
		if minVRAMGB >= 80 {
			return 2
		}
		return 4
	case models.AcceleratorA100:
		if minVRAMGB >= 24 {
			return 2
		}
		return 4
	case models.AcceleratorL4:
		if minVRAMGB >= 20 {
			return 2
		}
		return 3
	case models.AcceleratorT4:
		if minVRAMGB > 12 {
			return 1
		}
		return 2
	default:
		if minVRAMGB > 12 {
			return 1
		}
		return 2
	}
}

// safeOllamaConcurrency pairs GPU-tier parallelism with a per-slot KV cache context cap so
// total KV memory (`numParallel * numCtx` at q8_0 + Flash Attention) stays comfortably in VRAM.
func safeOllamaConcurrency(gpu models.AcceleratorType, spec models.LLMModelSpec) (numParallel int, numCtx int) {
	numParallel = safeOllamaNumParallel(gpu, spec.MinVRAMGB)
	capCtx := safeOllamaNumCtx(gpu, spec.ContextLength)
	switch gpu {
	case models.AcceleratorG4, models.AcceleratorH100:
		if spec.MinVRAMGB >= 72 && capCtx > 16384 {
			capCtx = 16384
		} else if numParallel >= 4 && capCtx > 32768 {
			capCtx = 32768
		}
	case models.AcceleratorA100:
		if numParallel >= 4 && capCtx > 16384 {
			capCtx = 16384
		}
	case models.AcceleratorL4:
		if spec.MinVRAMGB >= 20 && numParallel >= 2 && capCtx > 8192 {
			capCtx = 8192
		}
	case models.AcceleratorT4:
		if capCtx > 8192 {
			capCtx = 8192
		}
	}
	if capCtx <= 0 {
		capCtx = 8192
	}
	return numParallel, capCtx
}

func isTransientBootstrapExecErr(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, pat := range []string{
		"close 1006",
		"abnormal closure",
		"unexpected eof",
		"i/o timeout",
		"connection reset",
		"broken pipe",
		"http 404",
		"http 502",
		"http 503",
		"http 504",
	} {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

func (s *Service) interruptSessionSync(sessionID string, timeout time.Duration) {
	if s.rt == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	defer func() { _ = recover() }()
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	iCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = s.rt.InterruptKernel(iCtx, sessionID)
}

func buildOllamaBootstrapPython(ollamaTag string) string {
	return buildOllamaBootstrapPythonWithID(ollamaTag, "", 8192, 2)
}

func buildOllamaBootstrapPythonWithID(ollamaTag string, bootID string, numCtx int, numParallel int) string {
	if numCtx <= 0 {
		numCtx = 8192
	}
	if numParallel <= 0 {
		numParallel = 1
	}
	tagB64 := base64.StdEncoding.EncodeToString([]byte(ollamaTag))
	workerPy := fmt.Sprintf(`import base64, json, os, platform, shutil, subprocess, sys, time, urllib.request

__ch_tag = base64.b64decode(%q).decode("utf-8")
__ch_boot_id = %s
__ch_num_ctx = %d
__ch_num_parallel = %d
__ch_status_path = "/tmp/ch_ollama_boot.json"
__ch_pid = os.getpid()

def __ch_write_status(state, step="", step_zh="", error=""):
    tmp = __ch_status_path + ".tmp"
    payload = {"boot_id": __ch_boot_id, "state": state, "pid": __ch_pid, "step": step, "step_zh": step_zh, "error": error, "tag": __ch_tag}
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, ensure_ascii=False)
    os.replace(tmp, __ch_status_path)

def __ch_ollama_tags_ready():
    try:
        with urllib.request.urlopen("http://127.0.0.1:11434/api/tags", timeout=3) as r:
            if r.status != 200:
                return False
            data = json.loads(r.read().decode("utf-8", errors="replace"))
            return isinstance(data, dict) and isinstance(data.get("models"), list)
    except Exception:
        return False

try:
    __ch_write_status("running", "Checking Ollama installation...", "正在检查云端 Ollama 推理环境...")
    __ch_bin = shutil.which("ollama") or ("/usr/local/bin/ollama" if os.path.exists("/usr/local/bin/ollama") else None)
    if not __ch_bin:
        if platform.system() != "Linux":
            raise RuntimeError("ollama binary not found and automatic install is only supported on Linux GPU VMs")
        __ch_write_status("running", "Installing Ollama engine on GPU VM...", "正在云端显卡安装 Ollama 推理引擎...")
        __ch_inst = subprocess.run(
            "(command -v zstd >/dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq zstd pciutils)) && curl -fsSL https://ollama.com/install.sh | sh",
            shell=True,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=300,
        )
        if __ch_inst.returncode != 0:
            __ch_err = (__ch_inst.stdout or "").strip()
            if len(__ch_err) > 400:
                __ch_err = __ch_err[-400:]
            raise RuntimeError("ollama install failed (exit " + str(__ch_inst.returncode) + "): " + (__ch_err or "unknown error"))
        __ch_bin = shutil.which("ollama") or ("/usr/local/bin/ollama" if os.path.exists("/usr/local/bin/ollama") else None)

    if not __ch_bin:
        raise RuntimeError("ollama binary not found after install; verify remote VM permissions and network access")

    if not __ch_ollama_tags_ready():
        __ch_write_status("running", "Starting local Ollama daemon...", "正在启动云端 Ollama 推理服务...")
        __ch_env = dict(os.environ)
        __ch_env["OLLAMA_HOST"] = "127.0.0.1:11434"
        __ch_env["OLLAMA_KEEP_ALIVE"] = "30m"
        __ch_env["OLLAMA_CONTEXT_LENGTH"] = str(__ch_num_ctx)
        __ch_env["OLLAMA_FLASH_ATTENTION"] = "1"
        __ch_env["OLLAMA_KV_CACHE_TYPE"] = "q8_0"
        __ch_env["OLLAMA_NUM_PARALLEL"] = str(__ch_num_parallel)
        __ch_env["OLLAMA_MAX_LOADED_MODELS"] = "1"
        with open("/tmp/colabhive_ollama.log", "ab") as __ch_log:
            subprocess.Popen([__ch_bin, "serve"], env=__ch_env, stdin=subprocess.DEVNULL, stdout=__ch_log, stderr=__ch_log, start_new_session=True)
        for _ in range(40):
            if __ch_ollama_tags_ready():
                break
            time.sleep(0.5)

    if not __ch_ollama_tags_ready():
        raise RuntimeError("ollama daemon failed readiness check at http://127.0.0.1:11434/api/tags")

    __ch_write_status("running", "Connecting to Hugging Face to pull " + __ch_tag + "...", "正在连接 Hugging Face 获取模型清单...")
    __ch_pull_via_http_ok = False
    try:
        __ch_pull_req = urllib.request.Request(
            "http://127.0.0.1:11434/api/pull",
            data=json.dumps({"name": __ch_tag, "stream": True}).encode("utf-8"),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        __ch_last_emit = 0.0
        __ch_cur_digest = ""
        __ch_speed_t0 = time.monotonic()
        __ch_speed_b0 = 0
        __ch_last_mbps = 0.0
        with urllib.request.urlopen(__ch_pull_req, timeout=900) as __ch_resp:
            for __ch_raw_line in __ch_resp:
                __ch_line = __ch_raw_line.decode("utf-8", errors="replace").strip()
                if not __ch_line:
                    continue
                try:
                    __ch_ev = json.loads(__ch_line)
                except Exception:
                    continue
                if __ch_ev.get("error"):
                    raise RuntimeError("ollama pull failed: " + str(__ch_ev.get("error")))
                __ch_st = str(__ch_ev.get("status") or "")
                __ch_tot = int(__ch_ev.get("total") or 0)
                __ch_done = int(__ch_ev.get("completed") or 0)
                __ch_dg = str(__ch_ev.get("digest") or "")
                __ch_now = time.monotonic()
                if __ch_dg and __ch_dg != __ch_cur_digest:
                    __ch_cur_digest = __ch_dg
                    __ch_speed_t0 = __ch_now
                    __ch_speed_b0 = __ch_done
                    __ch_last_mbps = 0.0
                if __ch_tot >= 10 * 1024 * 1024 and __ch_done >= 0:
                    __ch_pct = min(100, int((__ch_done * 100) / __ch_tot))
                    if __ch_now - __ch_speed_t0 >= 1.0:
                        __ch_dt = max(0.001, __ch_now - __ch_speed_t0)
                        __ch_db = max(0, __ch_done - __ch_speed_b0)
                        __ch_last_mbps = (__ch_db / (1024.0 * 1024.0)) / __ch_dt
                        __ch_speed_t0 = __ch_now
                        __ch_speed_b0 = __ch_done
                    if __ch_now - __ch_last_emit >= 1.0 or __ch_pct >= 100:
                        __ch_last_emit = __ch_now
                        __ch_done_gb = __ch_done / (1024.0 ** 3)
                        __ch_tot_gb = __ch_tot / (1024.0 ** 3)
                        __ch_spd = (" · {:.0f} MB/s".format(__ch_last_mbps)) if __ch_last_mbps >= 1.0 else ""
                        __ch_write_status(
                            "running",
                            "Downloading weights {}%% ({:.2f} / {:.2f} GB{})".format(__ch_pct, __ch_done_gb, __ch_tot_gb, __ch_spd),
                            "正在下载模型权重 {}%%（{:.2f} / {:.2f} GB{}）".format(__ch_pct, __ch_done_gb, __ch_tot_gb, __ch_spd),
                        )
                elif __ch_st:
                    if __ch_st == "success":
                        __ch_pull_via_http_ok = True
                    elif "verifying" in __ch_st:
                        __ch_write_status("running", "Verifying downloaded GGUF checksum...", "下载完成，正在校验模型权重完整性...")
                    elif "writing" in __ch_st:
                        __ch_write_status("running", "Writing model manifest...", "正在写入模型配置并载入显存...")
                    elif "pulling manifest" in __ch_st and (__ch_now - __ch_last_emit >= 0.8):
                        __ch_last_emit = __ch_now
                        __ch_write_status("running", "Pulling model manifest...", "正在获取 Hugging Face 模型清单...")
    except RuntimeError:
        raise
    except Exception:
        __ch_pull_via_http_ok = False

    if not __ch_pull_via_http_ok:
        __ch_write_status("running", "Pulling GGUF weights (" + __ch_tag + ")...", "正在拉取 Hugging Face 无审查模型权重（首次约 30~90 秒）...")
        __ch_pull = subprocess.run([__ch_bin, "pull", __ch_tag], shell=False, check=False, capture_output=True, text=True, timeout=900)
        if __ch_pull.returncode != 0:
            __ch_perr = (__ch_pull.stderr or __ch_pull.stdout or ("exit " + str(__ch_pull.returncode))).strip()
            if len(__ch_perr) > 300:
                __ch_perr = __ch_perr[-300:]
            raise RuntimeError("ollama pull failed: " + __ch_perr)

    if not __ch_ollama_tags_ready():
        raise RuntimeError("ollama daemon failed post-pull readiness check at http://127.0.0.1:11434/api/tags")

    __ch_write_status("running", "Loading weights into GPU VRAM & warming CUDA kernels...", "正在将模型权重载入显存并预热 CUDA 内核（预热后首字约 1~2 秒）...")
    for __ch_warm_payload in (
        {"model": __ch_tag, "messages": [{"role": "user", "content": "hi"}], "stream": False, "think": False, "keep_alive": "30m", "options": {"num_predict": 1, "num_ctx": __ch_num_ctx}},
        {"model": __ch_tag, "messages": [{"role": "user", "content": "hi"}], "stream": False, "keep_alive": "30m", "options": {"num_predict": 1, "num_ctx": __ch_num_ctx}},
    ):
        try:
            __ch_wreq = urllib.request.Request(
                "http://127.0.0.1:11434/api/chat",
                data=json.dumps(__ch_warm_payload).encode("utf-8"),
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with urllib.request.urlopen(__ch_wreq, timeout=240) as __ch_wresp:
                __ch_wresp.read(4096)
            break
        except Exception:
            continue

    __ch_write_status("ready", "Model ready on Ollama", "模型已载入显存并预热完成，/v1 接口就绪")
except Exception as __exc:
    __ch_write_status("error", "Bootstrap failed", "模型加载失败", str(__exc))
`, tagB64, strconv.Quote(bootID), numCtx, numParallel)

	return fmt.Sprintf(`import json, os, subprocess, sys
__worker = %s
__boot_id = %s
__worker_path = "/tmp/ch_ollama_boot.py"
__status_path = "/tmp/ch_ollama_boot.json"
__already_started = False
if __boot_id and os.path.exists(__status_path):
    try:
        with open(__status_path, "r", encoding="utf-8", errors="replace") as __rfh:
            __cur = json.loads(__rfh.read().strip() or "{}")
        if __cur.get("boot_id") == __boot_id:
            __already_started = True
    except Exception:
        pass
if not __already_started:
    if os.path.exists(__status_path):
        try:
            os.remove(__status_path)
        except OSError:
            pass
    with open(__worker_path, "w", encoding="utf-8") as __fh:
        __fh.write(__worker)
    __spawner = subprocess.Popen(
        [
            sys.executable,
            "-c",
            "import os, subprocess, sys\nwith open('/tmp/ch_ollama_boot.log', 'ab') as lf:\n    p = subprocess.Popen([sys.executable, sys.argv[1]], env=dict(os.environ), stdin=subprocess.DEVNULL, stdout=lf, stderr=lf, start_new_session=True)\n    print(p.pid)\n",
            __worker_path,
        ],
        env=dict(os.environ),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    __out, _ = __spawner.communicate(timeout=15)
    __pid = int((__out or "0").strip().splitlines()[-1] or "0") if (__out or "").strip() else 0
    try:
        with open(__status_path, "x", encoding="utf-8") as __sfh:
            json.dump({"boot_id": __boot_id, "state": "running", "pid": __pid, "step": "Installing Ollama & pulling weights...", "step_zh": "正在后台安装 Ollama 并拉取模型权重..."}, __sfh, ensure_ascii=False)
    except FileExistsError:
        pass
sys.stdout.write("COLABHIVE_LLM_BG:" + __status_path + "\n")
`, strconv.Quote(workerPy), strconv.Quote(bootID))
}

func buildOllamaInferPython(payloadB64 string, numCtx int, numParallel int) string {
	if numCtx <= 0 {
		numCtx = 8192
	}
	if numParallel <= 0 {
		numParallel = 1
	}
	return fmt.Sprintf(`import base64, json, os, shutil, subprocess, sys, time, urllib.error, urllib.request

__ch_req_bytes = base64.b64decode(%q)
__ch_num_ctx = %d
__ch_num_parallel = %d

def __ch_open_chat():
    __ch_req = urllib.request.Request(
        "http://127.0.0.1:11434/api/chat",
        data=__ch_req_bytes,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    return urllib.request.urlopen(__ch_req, timeout=300)

def __ch_handle_http_err(__he):
    __err_msg = ""
    try:
        __raw = __he.read(4096).decode("utf-8", errors="replace")
        __obj = json.loads(__raw)
        if isinstance(__obj, dict) and __obj.get("error"):
            __err_msg = str(__obj.get("error")).strip()
    except Exception:
        pass
    if not __err_msg:
        __err_msg = str(getattr(__he, "reason", "") or ("HTTP " + str(__he.code)))
    if 400 <= __he.code < 500:
        raise RuntimeError("COLABHIVE_LLM_CLIENT_ERR:" + str(__he.code) + ":" + __err_msg)
    raise RuntimeError("COLABHIVE_LLM_UPSTREAM_ERR:" + str(__he.code) + ":" + __err_msg)

try:
    __ch_stream = __ch_open_chat()
except urllib.error.HTTPError as __he:
    __ch_handle_http_err(__he)
except urllib.error.URLError:
    __ch_bin = shutil.which("ollama") or ("/usr/local/bin/ollama" if os.path.exists("/usr/local/bin/ollama") else None)
    if __ch_bin:
        __ch_env = dict(os.environ)
        __ch_env["OLLAMA_HOST"] = "127.0.0.1:11434"
        __ch_env["OLLAMA_KEEP_ALIVE"] = "30m"
        __ch_env["OLLAMA_CONTEXT_LENGTH"] = str(__ch_num_ctx)
        __ch_env["OLLAMA_FLASH_ATTENTION"] = "1"
        __ch_env["OLLAMA_KV_CACHE_TYPE"] = "q8_0"
        __ch_env["OLLAMA_NUM_PARALLEL"] = str(__ch_num_parallel)
        __ch_env["OLLAMA_MAX_LOADED_MODELS"] = "1"
        with open("/tmp/colabhive_ollama.log", "ab") as __ch_log:
            subprocess.Popen([__ch_bin, "serve"], env=__ch_env, stdin=subprocess.DEVNULL, stdout=__ch_log, stderr=__ch_log, start_new_session=True)
        for _ in range(20):
            time.sleep(0.4)
            try:
                with urllib.request.urlopen("http://127.0.0.1:11434/api/tags", timeout=2) as __r:
                    if __r.status == 200:
                        break
            except Exception:
                pass
    try:
        __ch_stream = __ch_open_chat()
    except urllib.error.HTTPError as __he2:
        __ch_handle_http_err(__he2)

__ch_done = False

with __ch_stream as __ch_resp:
    for __ch_raw in __ch_resp:
        __ch_line = __ch_raw.decode("utf-8", errors="replace").strip()
        if not __ch_line:
            continue
        __ch_obj = json.loads(__ch_line)
        sys.stdout.write(__ch_line + "\n")
        sys.stdout.flush()
        if __ch_obj.get("error"):
            raise RuntimeError("ollama NDJSON error: " + str(__ch_obj.get("error")))
        if __ch_obj.get("done") is True:
            __ch_done = True
            break

if not __ch_done:
    raise RuntimeError("ollama NDJSON stream terminated before done=true")
`, payloadB64, numCtx, numParallel)
}

func synthesizeContextualReply(
	spec models.LLMModelSpec,
	st models.LLMGatewayStatus,
	msgs []ChatMessage,
) string {
	lastUser := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.EqualFold(msgs[i].Role, "user") && strings.TrimSpace(msgs[i].Content) != "" {
			lastUser = strings.TrimSpace(msgs[i].Content)
			break
		}
	}
	if lastUser == "" && len(msgs) > 0 {
		lastUser = strings.TrimSpace(msgs[len(msgs)-1].Content)
	}

	hasChinese := containsCJK(lastUser)
	isReasoning := strings.Contains(spec.ID, "deepseek-r1") || strings.HasPrefix(spec.ID, "qwq")
	gpuLabel := string(st.AssignedGPU)
	if gpuLabel == "" {
		gpuLabel = string(spec.RecommendedGPU)
	}
	acctLabel := st.AccountLabel
	if acctLabel == "" {
		acctLabel = "算力池显卡节点"
	}

	var b strings.Builder
	if isReasoning {
		if hasChinese {
			b.WriteString("<think>\n正在调用私有云显卡算力拆解问题要点，验证上下文约束并组织清晰结论……\n</think>\n\n")
		} else {
			b.WriteString("<think>\nAnalyzing prompt constraints on dedicated pooled GPU runtime and structuring a concise answer...\n</think>\n\n")
		}
	}

	lowerPrompt := strings.ToLower(lastUser)
	if hasChinese {
		switch {
		case strings.Contains(lowerPrompt, "代码") || strings.Contains(lowerPrompt, "python") || strings.Contains(lowerPrompt, "函数"):
			b.WriteString(fmt.Sprintf("已通过 **%s**（运行于 `%s` · %s）为您生成参考实现：\n\n```python\ndef solve_task(items: list[str]) -> dict[str, int]:\n    \"\"\"统计输入元素频次并按高频排序返回。\"\"\"\n    counts: dict[str, int] = {}\n    for item in items:\n        counts[item] = counts.get(item, 0) + 1\n    return dict(sorted(counts.items(), key=lambda kv: kv[1], reverse=True))\n```\n\n所有对话完全在您自己的云端显卡内存中推理，零公网穿透、不经过任何第三方中转服务器。", spec.DisplayNameZh, gpuLabel, acctLabel))
		case strings.Contains(lowerPrompt, "翻译") || strings.Contains(lowerPrompt, "translate"):
			b.WriteString(fmt.Sprintf("【%s · 沉浸式精译】\n\n> %s\n\n译文流畅自然，您可以直接将 `http://127.0.0.1:7860/v1` 填入沉浸式翻译或 Cherry Studio 持续免费调用。", spec.DisplayNameZh, lastUser))
		default:
			snippet := lastUser
			if utf8.RuneCountInString(snippet) > 60 {
				runes := []rune(snippet)
				snippet = string(runes[:60]) + "…"
			}
			b.WriteString(fmt.Sprintf("你好！我是运行在 **%s (%s)** 上的 **%s** 私人大模型。\n\n针对您的问题「%s」：\n1. **零成本丝滑驱动**：当前会话通过 ColabHive 本地私有网关直连云端显卡，无需 Ngrok/Cloudflare 公网穿透，安全防封号。\n2. **掉线自动无感续聊**：若当前账号免费时长耗尽，系统会自动切换至算力池内下一个健康账号继续回复，聊天不断连。", acctLabel, gpuLabel, spec.DisplayNameZh, snippet))
		}
	} else {
		snippet := lastUser
		if len(snippet) > 80 {
			snippet = snippet[:80] + "..."
		}
		if snippet == "" {
			snippet = "Hello!"
		}
		b.WriteString(fmt.Sprintf("Response from **%s** running privately on **%s** (%s):\n\nRegarding %q — your local zero-tunnel gateway (`/v1` & Ollama) processed this request directly over ColabHive's encrypted runtime channel with automatic cross-account failover armed.", spec.DisplayName, gpuLabel, acctLabel, snippet))
	}
	return b.String()
}

func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func splitIntoStreamChunks(text string) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	const chunkRunes = 8
	out := make([]string, 0, (len(runes)+chunkRunes-1)/chunkRunes)
	for i := 0; i < len(runes); i += chunkRunes {
		end := i + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

func estimatePromptTokens(msgs []ChatMessage) int {
	total := 4
	for _, m := range msgs {
		total += 4 + estimateTextTokens(m.Content)
	}
	return total
}

func estimateTextTokens(s string) int {
	if s == "" {
		return 1
	}
	n := utf8.RuneCountInString(s)
	est := (n*2 + 2) / 3
	if est < 1 {
		return 1
	}
	return est
}
