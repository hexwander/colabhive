package pluginhost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/appstore"
	"github.com/colabhive/colabhive/internal/llmgw"
	"github.com/colabhive/colabhive/internal/models"
)

// Config configures the ColabHive Plugin Platform v2 Host SDK service.
type Config struct {
	DataDir     string
	HostBaseURL string
	DemoMode    bool
}

// CapabilityProvider describes one live runtime or engine providing a standard capability.
type CapabilityProvider struct {
	Capability   string `json:"capability"`
	PluginID     string `json:"plugin_id"`
	InstanceID   string `json:"instance_id,omitempty"`
	Name         string `json:"name"`
	NameZh       string `json:"name_zh"`
	State        string `json:"state"`
	GPU          string `json:"gpu,omitempty"`
	AccountLabel string `json:"account_label,omitempty"`
	MediaAPIBase string `json:"media_api_base,omitempty"`
	NativeURL    string `json:"native_url,omitempty"`
}

// UIPluginDescriptor describes an installed or built-in UI option selectable by the user.
type UIPluginDescriptor struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	NameZh               string   `json:"name_zh"`
	Author               string   `json:"author,omitempty"`
	Version              string   `json:"version,omitempty"`
	Description          string   `json:"description,omitempty"`
	DescriptionZh        string   `json:"description_zh,omitempty"`
	Mode                 string   `json:"mode"` // "builtin_route" | "local_bundle" | "remote_proxy"
	Route                string   `json:"route,omitempty"`
	Consumes             []string `json:"consumes,omitempty"`
	OptionalCapabilities []string `json:"optional_capabilities,omitempty"`
	Permissions          []string `json:"permissions,omitempty"`
	Builtin              bool     `json:"builtin"`
}

// CapabilitiesSnapshot is returned by `GET /sdk/v1/capabilities`.
type CapabilitiesSnapshot struct {
	SDKVersion    string                 `json:"sdk_version"`
	Providers     []CapabilityProvider   `json:"providers"`
	UIPlugins     []UIPluginDescriptor   `json:"ui_plugins"`
	UIPreferences map[string]string      `json:"ui_preferences"`
	LLMReady      bool                   `json:"llm_ready"`
	LLMModelName  string                 `json:"llm_model_name,omitempty"`
}

// MediaEngineSummary describes a running cloud GPU media engine usable via `media.v1`.
type MediaEngineSummary struct {
	InstanceID        string   `json:"instance_id"`
	TemplateID        string   `json:"template_id"`
	Name              string   `json:"name"`
	NameZh            string   `json:"name_zh"`
	State             string   `json:"state"`
	LaunchMode        string   `json:"launch_mode"`
	MediaAPIReady     bool     `json:"media_api_ready"`
	Provides          []string `json:"provides"`
	AssignedGPU       string   `json:"assigned_gpu"`
	AccountLabel      string   `json:"account_label"`
	FreeTierActive    bool     `json:"free_tier_active"`
	IdleAutoStopMin   int      `json:"idle_auto_stop_min"`
	NativeURL         string   `json:"native_url,omitempty"`
	StudioRoute       string   `json:"studio_route,omitempty"`
	PoweredBy         string   `json:"powered_by,omitempty"`
	StartedAt         time.Time `json:"started_at"`
}

// MediaModelInfo describes a model returned by a `media.v1` cloud engine.
type MediaModelInfo struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Family       string          `json:"family,omitempty"`
	FamilyLabel  string          `json:"family_label,omitempty"`
	Description  string          `json:"description,omitempty"`
	MainOutput   []string        `json:"main_output,omitempty"`
	Outputs      []string        `json:"outputs,omitempty"`
	Inputs       []string        `json:"inputs,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
	Cached       bool            `json:"cached"`
	Featured     bool            `json:"featured,omitempty"`
	DefaultRes   string          `json:"default_resolution,omitempty"`
	DefaultSteps int             `json:"default_steps,omitempty"`
}

// MediaJobRequest is the standardized `POST /sdk/v1/media/jobs` request payload.
type MediaJobRequest struct {
	InstanceID     string         `json:"instance_id"`
	ModelID        string         `json:"model_id"`
	ModelName      string         `json:"model_name,omitempty"`
	Mode           string         `json:"mode,omitempty"` // "t2i" | "i2i" | "t2v" | "i2v" | "audio"
	Prompt         string         `json:"prompt"`
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	Steps          int            `json:"steps,omitempty"`
	Seed           int64          `json:"seed,omitempty"`
	GuidanceScale  *float64       `json:"guidance_scale,omitempty"`
	VideoLength    int            `json:"video_length,omitempty"`
	DurationSec    float64        `json:"duration_sec,omitempty"`
	FPS            int            `json:"fps,omitempty"`
	InputAssetID   string         `json:"input_asset_id,omitempty"`
	SourcePluginID string         `json:"source_plugin_id,omitempty"`
	ExtraSettings  map[string]any `json:"extra_settings,omitempty"`
}

// MediaJobState tracks the lifecycle of one local-orchestrated cloud media generation job.
type MediaJobState string

const (
	MediaJobQueued    MediaJobState = "queued"
	MediaJobRunning   MediaJobState = "running"
	MediaJobDone      MediaJobState = "done"
	MediaJobError     MediaJobState = "error"
	MediaJobCanceled  MediaJobState = "canceled"
)

// MediaJobEvent is streamed over SSE (`GET /sdk/v1/media/jobs/{id}/events`).
type MediaJobEvent struct {
	Type           string        `json:"type"` // "snapshot" | "progress" | "preview" | "log" | "done" | "error" | "canceled"
	JobID          string        `json:"job_id"`
	State          MediaJobState `json:"state,omitempty"`
	Phase          string        `json:"phase,omitempty"`
	Status         string        `json:"status,omitempty"`
	Progress       int           `json:"progress,omitempty"`
	CurrentStep    int           `json:"current_step,omitempty"`
	TotalSteps     int           `json:"total_steps,omitempty"`
	PreviewDataURL string        `json:"preview_data_url,omitempty"`
	LogLine        string        `json:"log_line,omitempty"`
	Error          string        `json:"error,omitempty"`
	Assets         []*MediaAsset `json:"assets,omitempty"`
	Timestamp      time.Time     `json:"timestamp"`
}

// MediaJob represents a tracked generation job and its local asset outputs.
type MediaJob struct {
	ID             string        `json:"id"`
	RemoteJobID    string        `json:"remote_job_id,omitempty"`
	InstanceID     string        `json:"instance_id"`
	EngineID       string        `json:"engine_id"`
	ModelID        string        `json:"model_id"`
	ModelName      string        `json:"model_name,omitempty"`
	Mode           string        `json:"mode"`
	Prompt         string        `json:"prompt"`
	NegativePrompt string        `json:"negative_prompt,omitempty"`
	Resolution     string        `json:"resolution,omitempty"`
	Steps          int           `json:"steps,omitempty"`
	Seed           int64         `json:"seed,omitempty"`
	InputAssetID   string        `json:"input_asset_id,omitempty"`
	SourcePluginID string        `json:"source_plugin_id,omitempty"`
	State          MediaJobState `json:"state"`
	Phase          string        `json:"phase,omitempty"`
	StatusText     string        `json:"status_text,omitempty"`
	Progress       int           `json:"progress"`
	CurrentStep    int           `json:"current_step,omitempty"`
	TotalSteps     int           `json:"total_steps,omitempty"`
	PreviewDataURL string        `json:"preview_data_url,omitempty"`
	Logs           []string      `json:"logs,omitempty"`
	Error          string        `json:"error,omitempty"`
	AssetIDs       []string      `json:"asset_ids,omitempty"`
	Assets         []*MediaAsset `json:"assets,omitempty"`
	DurationMS     int64         `json:"duration_ms,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	CompletedAt    *time.Time    `json:"completed_at,omitempty"`
}

// EnhancePromptRequest asks ColabHive's live LLM Gateway to enrich/translate a media prompt.
type EnhancePromptRequest struct {
	Prompt   string `json:"prompt"`
	Mode     string `json:"mode,omitempty"`     // "image" | "video"
	Language string `json:"language,omitempty"` // "en" (default) | "zh"
	Style    string `json:"style,omitempty"`
}

// EnhancePromptResponse returns the enriched prompt and which LLM model produced it.
type EnhancePromptResponse struct {
	EnhancedPrompt string `json:"enhanced_prompt"`
	ModelID        string `json:"model_id"`
	ModelName      string `json:"model_name"`
}

type managedJob struct {
	job       *MediaJob
	cancel    context.CancelFunc
	subs      map[chan MediaJobEvent]struct{}
}

type cachedModelsEntry struct {
	fetchedAt time.Time
	models    []MediaModelInfo
}

// Host orchestrates Plugin Platform v2 capabilities, local Asset Vault, sandboxed UI plugins,
// and headless `media.v1` job execution across ColabHive's pooled cloud GPUs.
type Host struct {
	apps    *appstore.Manager
	llm     *llmgw.Service
	assets  *AssetVault
	sandbox *UISandbox
	cfg     Config

	mu          sync.RWMutex
	jobs        map[string]*managedJob
	modelsCache map[string]cachedModelsEntry
}

// NewHost constructs a Plugin Platform v2 Host SDK controller.
func NewHost(apps *appstore.Manager, llm *llmgw.Service, cfg Config) *Host {
	return &Host{
		apps:        apps,
		llm:         llm,
		assets:      NewAssetVault(cfg.DataDir),
		sandbox:     NewUISandbox(cfg.DataDir, cfg.HostBaseURL),
		cfg:         cfg,
		jobs:        make(map[string]*managedJob),
		modelsCache: make(map[string]cachedModelsEntry),
	}
}

// Assets returns the persistent local Asset Vault.
func (h *Host) Assets() *AssetVault { return h.assets }

// Sandbox returns the isolated local UI plugin sandbox host.
func (h *Host) Sandbox() *UISandbox { return h.sandbox }

// Close cleanly shuts down active plugin UI sandboxes and cancels running job watchers.
func (h *Host) Close() error {
	h.mu.Lock()
	for _, mj := range h.jobs {
		if mj != nil && mj.cancel != nil {
			mj.cancel()
		}
	}
	h.mu.Unlock()
	if h.sandbox != nil {
		return h.sandbox.Close()
	}
	return nil
}

// GetCapabilities returns all live capability providers, selectable UI plugins, and preferences.
func (h *Host) GetCapabilities() CapabilitiesSnapshot {
	snap := CapabilitiesSnapshot{
		SDKVersion:    "2",
		Providers:     make([]CapabilityProvider, 0),
		UIPlugins:     make([]UIPluginDescriptor, 0),
		UIPreferences: h.sandbox.GetUIPreferences(),
	}

	// Built-in reference UI plugin (dogfoods ONLY public /sdk/v1/* endpoints!)
	snap.UIPlugins = append(snap.UIPlugins, UIPluginDescriptor{
		ID:                   "builtin:media-studio",
		Name:                 "ColabHive Native Media Studio",
		NameZh:               "ColabHive 原生创作台（默认 · 0ms 秒开）",
		Author:               "ColabHive",
		Version:              "2.0.0",
		Description:          "Instant local image & video creation studio with persistent gallery and AI prompt enhancement.",
		DescriptionZh:        "极速本地文生图 / 图生视频创作台，自带本地永久画廊与 AI 提示词扩写，完全基于开放 Host SDK 构建。",
		Mode:                 "builtin_route",
		Route:                "/studio/media",
		Consumes:             []string{"media.image.v1", "media.video.v1", "media.audio.v1"},
		OptionalCapabilities: []string{"llm.openai.v1"},
		Permissions:          []string{"assets:read", "assets:write", "media:invoke", "engines:read", "jobs:manage", "cap:llm.openai.v1"},
		Builtin:              true,
	})

	// Community / third-party installed v2 UI plugins
	if h.apps != nil {
		for _, man := range h.apps.ListUIPlugins() {
			mode := "local_bundle"
			route := ""
			if man.UI != nil {
				if man.UI.Mode != "" {
					mode = man.UI.Mode
				}
				route = man.UI.Route
			}
			snap.UIPlugins = append(snap.UIPlugins, UIPluginDescriptor{
				ID:                   man.ID,
				Name:                 man.Name,
				NameZh:               man.NameZh,
				Author:               man.Author,
				Version:              man.Version,
				Description:          man.Description,
				DescriptionZh:        man.DescriptionZh,
				Mode:                 mode,
				Route:                route,
				Consumes:             append([]string(nil), man.Consumes...),
				OptionalCapabilities: append([]string(nil), man.OptionalCapabilities...),
				Permissions:          append([]string(nil), man.Permissions...),
				Builtin:              false,
			})
		}

		// Live engine instances
		for _, inst := range h.apps.ListInstances() {
			if inst == nil || inst.State != models.AppStateRunning {
				continue
			}
			for _, capName := range inst.Provides {
				snap.Providers = append(snap.Providers, CapabilityProvider{
					Capability:   capName,
					PluginID:     inst.TemplateID,
					InstanceID:   inst.ID,
					Name:         inst.Name,
					NameZh:       inst.NameZh,
					State:        string(inst.State),
					GPU:          string(inst.AssignedGPU),
					AccountLabel: inst.AccountLabel,
					MediaAPIBase: inst.MediaAPIBase,
					NativeURL:    inst.LocalURL,
				})
			}
		}
	}

	// Fallback classic remote web UI option
	snap.UIPlugins = append(snap.UIPlugins, UIPluginDescriptor{
		ID:            "remote:native",
		Name:          "Engine Native WebUI (Expert Mode)",
		NameZh:        "引擎原生网页界面（专家兜底模式）",
		Author:        "Upstream Engine",
		Version:       "1.0.0",
		Description:   "Opens the upstream engine's original remote Gradio / ComfyUI web interface directly.",
		DescriptionZh: "直接打开云端引擎自带的原生 Gradio / ComfyUI 网页界面。",
		Mode:          "remote_proxy",
		Consumes:      []string{"media.image.v1", "media.video.v1", "media.audio.v1"},
		Builtin:       true,
	})

	// Live LLM Gateway capability (`llm.openai.v1`)
	if h.llm != nil {
		st := h.llm.Status()
		if st.Running {
			snap.LLMReady = true
			snap.LLMModelName = st.ActiveModelName
			if snap.LLMModelName == "" {
				snap.LLMModelName = st.ActiveModelID
			}
			snap.Providers = append(snap.Providers, CapabilityProvider{
				Capability:   "llm.openai.v1",
				PluginID:     "llm-chat",
				InstanceID:   st.SessionID,
				Name:         snap.LLMModelName,
				NameZh:       snap.LLMModelName,
				State:        "running",
				GPU:          string(st.AssignedGPU),
				AccountLabel: st.AccountLabel,
			})
		}
	}

	return snap
}

// OpenUIPlugin starts (or reuses) an isolated loopback sandbox listener for a third-party UI plugin.
func (h *Host) OpenUIPlugin(pluginID string) (*SandboxSession, error) {
	if h.apps == nil {
		return nil, errors.New("app store manager unavailable")
	}
	man, ok := h.apps.GetPluginManifest(pluginID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", appstore.ErrAppPluginNotFound, pluginID)
	}
	return h.sandbox.EnsureOpen(man)
}

// EnhancePrompt reuses ColabHive's live LLM Gateway (`llm.openai.v1`) for cross-plugin AI prompt enrichment.
func (h *Host) EnhancePrompt(ctx context.Context, req EnhancePromptRequest) (*EnhancePromptResponse, error) {
	raw := strings.TrimSpace(req.Prompt)
	if raw == "" {
		return nil, errors.New("prompt is required")
	}
	if h.llm == nil {
		return nil, errors.New("LLM gateway service is unavailable")
	}
	st := h.llm.Status()
	if !st.Running {
		return nil, errors.New("请先在「无审查大模型」页面启动一个云端模型，即可跨插件一键调用 AI 提示词扩写")
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "image"
	}
	lang := strings.ToLower(strings.TrimSpace(req.Language))
	if lang == "" {
		lang = "en"
	}
	styleHint := strings.TrimSpace(req.Style)
	targetLangDesc := "vivid, natural English"
	if lang == "zh" {
		targetLangDesc = "精炼具象的中文"
	}
	mediaDesc := "a photorealistic / high-craft still image"
	if strings.Contains(mode, "video") || mode == "t2v" || mode == "i2v" {
		mediaDesc = "a cinematic AI video clip (explicitly describing subject motion, camera movement, lighting, and atmosphere)"
	}
	sysPrompt := fmt.Sprintf(
		"You are an expert prompt engineer for state-of-the-art AI media models (Z-Image, Flux, Wan 2.2, LTX-2, MiniMax H3). "+
			"Rewrite and enrich the user's idea into ONE concise, vivid prompt in %s optimized for %s. "+
			"Include composition, lighting, texture, lens/motion details. Output ONLY the final prompt text with no quotes, markdown, or explanation.",
		targetLangDesc, mediaDesc,
	)
	userContent := raw
	if styleHint != "" {
		userContent += "\nPreferred style: " + styleHint
	}
	temp := 0.7
	thinkOff := false
	resp, err := h.llm.Chat(ctx, llmgw.ChatInferRequest{
		Model: st.ActiveModelID,
		Messages: []llmgw.ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userContent},
		},
		Temperature: &temp,
		MaxTokens:   320,
		Think:       &thinkOff,
	}, nil)
	if err != nil {
		return nil, err
	}
	out := strings.TrimSpace(resp.Content)
	out = strings.Trim(out, "\"'`")
	if out == "" {
		out = raw
	}
	return &EnhancePromptResponse{
		EnhancedPrompt: out,
		ModelID:        resp.ModelID,
		ModelName:      st.ActiveModelName,
	}, nil
}

// ListMediaEngines returns all active running media-capable cloud instances.
func (h *Host) ListMediaEngines() []MediaEngineSummary {
	if h.apps == nil {
		return []MediaEngineSummary{}
	}
	insts := h.apps.ListInstances()
	out := make([]MediaEngineSummary, 0)
	for _, inst := range insts {
		if inst == nil || inst.State != models.AppStateRunning {
			continue
		}
		isMedia := inst.MediaAPIBase != ""
		for _, capName := range inst.Provides {
			if strings.HasPrefix(capName, "media.") {
				isMedia = true
				break
			}
		}
		if !isMedia && inst.TemplateID != "wangp" {
			continue
		}
		poweredBy := ""
		if inst.TemplateID == "wangp" {
			poweredBy = "WanGP (DeepBeepMeep)"
		}
		out = append(out, MediaEngineSummary{
			InstanceID:      inst.ID,
			TemplateID:      inst.TemplateID,
			Name:            inst.Name,
			NameZh:          inst.NameZh,
			State:           string(inst.State),
			LaunchMode:      inst.LaunchMode,
			MediaAPIReady:   inst.MediaAPIBase != "",
			Provides:        append([]string(nil), inst.Provides...),
			AssignedGPU:     string(inst.AssignedGPU),
			AccountLabel:    inst.AccountLabel,
			FreeTierActive:  inst.FreeTierActive,
			IdleAutoStopMin: inst.IdleAutoStopMin,
			NativeURL:       inst.LocalURL,
			StudioRoute:     inst.StudioRoute,
			PoweredBy:       poweredBy,
			StartedAt:       inst.StartedAt,
		})
	}
	return out
}

// ListEngineModels fetches the live model catalog from a running `media.v1` engine instance
// (with a 60s local memory cache for instant UI switching).
func (h *Host) ListEngineModels(ctx context.Context, instanceID string, forceRefresh bool) ([]MediaModelInfo, error) {
	if !forceRefresh {
		h.mu.RLock()
		if cached, ok := h.modelsCache[instanceID]; ok && time.Since(cached.fetchedAt) < 60*time.Second && len(cached.models) > 0 {
			out := append([]MediaModelInfo(nil), cached.models...)
			h.mu.RUnlock()
			return out, nil
		}
		h.mu.RUnlock()
	}

	if h.apps == nil {
		return nil, errors.New("app store manager unavailable")
	}
	client, baseURL, _, err := h.apps.GetInstanceMediaClient(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("query engine models: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("engine /models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Models []MediaModelInfo `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		// Also accept bare JSON array.
		var arr []MediaModelInfo
		if err2 := json.Unmarshal(body, &arr); err2 != nil {
			return nil, fmt.Errorf("decode engine /models: %w", err)
		}
		payload.Models = arr
	}
	h.mu.Lock()
	h.modelsCache[instanceID] = cachedModelsEntry{
		fetchedAt: time.Now(),
		models:    append([]MediaModelInfo(nil), payload.Models...),
	}
	h.mu.Unlock()
	return payload.Models, nil
}

// GetModelDefaults retrieves pristine default generation settings & schema for a model.
func (h *Host) GetModelDefaults(ctx context.Context, instanceID, modelID string) (map[string]any, error) {
	if h.apps == nil {
		return nil, errors.New("app store manager unavailable")
	}
	client, baseURL, _, err := h.apps.GetInstanceMediaClient(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	target := baseURL + "/models/" + url.PathEscape(strings.TrimSpace(modelID)) + "/defaults"
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("engine defaults HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func newMediaJobID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "mjob_" + hex.EncodeToString(b[:])
}

// CreateMediaJob validates the request, registers a local tracked job, and runs the remote
// `media.v1` generation asynchronously (streaming progress/previews and pulling outputs to Asset Vault).
func (h *Host) CreateMediaJob(ctx context.Context, req MediaJobRequest) (*MediaJob, error) {
	if h.apps == nil {
		return nil, errors.New("app store manager unavailable")
	}
	req.InstanceID = strings.TrimSpace(req.InstanceID)
	req.ModelID = strings.TrimSpace(req.ModelID)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.InstanceID == "" {
		return nil, errors.New("instance_id is required")
	}
	if req.ModelID == "" {
		return nil, errors.New("model_id is required")
	}
	if req.Prompt == "" {
		return nil, errors.New("prompt is required")
	}
	if req.Mode == "" {
		req.Mode = "t2i"
	}

	client, baseURL, inst, err := h.apps.GetInstanceMediaClient(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	jobID := newMediaJobID()
	job := &MediaJob{
		ID:             jobID,
		InstanceID:     inst.ID,
		EngineID:       inst.TemplateID,
		ModelID:        req.ModelID,
		ModelName:      req.ModelName,
		Mode:           req.Mode,
		Prompt:         req.Prompt,
		NegativePrompt: req.NegativePrompt,
		Resolution:     req.Resolution,
		Steps:          req.Steps,
		Seed:           req.Seed,
		InputAssetID:   strings.TrimSpace(req.InputAssetID),
		SourcePluginID: strings.TrimSpace(req.SourcePluginID),
		State:          MediaJobQueued,
		Phase:          "queued",
		StatusText:     "正在提交生成任务到云端显卡...",
		Progress:       2,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	mj := &managedJob{
		job:    job,
		cancel: runCancel,
		subs:   make(map[chan MediaJobEvent]struct{}),
	}
	h.mu.Lock()
	h.jobs[jobID] = mj
	snap := cloneMediaJob(job)
	h.mu.Unlock()

	go h.executeMediaJob(runCtx, mj, client, baseURL, req)
	return snap, nil
}

// ListMediaJobs returns recent and active media jobs newest-first.
func (h *Host) ListMediaJobs() []*MediaJob {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*MediaJob, 0, len(h.jobs))
	for _, mj := range h.jobs {
		if mj != nil && mj.job != nil {
			out = append(out, cloneMediaJob(mj.job))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// GetMediaJob returns a snapshot of a single job.
func (h *Host) GetMediaJob(jobID string) (*MediaJob, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	mj, ok := h.jobs[strings.TrimSpace(jobID)]
	if !ok || mj == nil || mj.job == nil {
		return nil, false
	}
	return cloneMediaJob(mj.job), true
}

// SubscribeJobEvents registers a live SSE subscriber for jobID and returns an initial snapshot.
func (h *Host) SubscribeJobEvents(jobID string) (*MediaJob, <-chan MediaJobEvent, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	mj, ok := h.jobs[strings.TrimSpace(jobID)]
	if !ok || mj == nil || mj.job == nil {
		return nil, nil, nil, false
	}
	ch := make(chan MediaJobEvent, 32)
	mj.subs[ch] = struct{}{}
	snap := cloneMediaJob(mj.job)
	unsub := func() {
		h.mu.Lock()
		delete(mj.subs, ch)
		h.mu.Unlock()
	}
	return snap, ch, unsub, true
}

// CancelMediaJob requests cancellation both locally and on the remote `media.v1` worker.
func (h *Host) CancelMediaJob(ctx context.Context, jobID string) (*MediaJob, error) {
	h.mu.Lock()
	mj, ok := h.jobs[strings.TrimSpace(jobID)]
	if !ok || mj == nil || mj.job == nil {
		h.mu.Unlock()
		return nil, errors.New("media job not found")
	}
	remoteID := mj.job.RemoteJobID
	instID := mj.job.InstanceID
	cancelFn := mj.cancel
	h.mu.Unlock()

	if remoteID != "" && h.apps != nil {
		if client, baseURL, _, err := h.apps.GetInstanceMediaClient(ctx, instID); err == nil {
			cCtx, cCancel := context.WithTimeout(ctx, 8*time.Second)
			req, _ := http.NewRequestWithContext(cCtx, http.MethodPost, baseURL+"/jobs/"+url.PathEscape(remoteID)+"/cancel", nil)
			if resp, err := client.Do(req); err == nil && resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			cCancel()
		}
	}
	if cancelFn != nil {
		cancelFn()
	}

	now := time.Now().UTC()
	h.mu.Lock()
	if mj.job.State == MediaJobQueued || mj.job.State == MediaJobRunning {
		mj.job.State = MediaJobCanceled
		mj.job.Phase = "canceled"
		mj.job.StatusText = "已取消生成"
		mj.job.UpdatedAt = now
		mj.job.CompletedAt = &now
		h.broadcastJobEventLocked(mj, MediaJobEvent{
			Type:      "canceled",
			JobID:     mj.job.ID,
			State:     MediaJobCanceled,
			Phase:     "canceled",
			Status:    "已取消生成",
			Timestamp: now,
		})
	}
	snap := cloneMediaJob(mj.job)
	h.mu.Unlock()
	return snap, nil
}

func (h *Host) broadcastJobEventLocked(mj *managedJob, ev MediaJobEvent) {
	for ch := range mj.subs {
		select {
		case ch <- ev:
		default:
			// Drop oldest event for slow consumer to prevent blocking job loop.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func (h *Host) failJob(mj *managedJob, errMsg string) {
	now := time.Now().UTC()
	h.mu.Lock()
	if mj.job.State == MediaJobCanceled {
		h.mu.Unlock()
		return
	}
	mj.job.State = MediaJobError
	mj.job.Phase = "error"
	mj.job.Error = errMsg
	mj.job.StatusText = "生成失败：" + errMsg
	mj.job.UpdatedAt = now
	mj.job.CompletedAt = &now
	h.broadcastJobEventLocked(mj, MediaJobEvent{
		Type:      "error",
		JobID:     mj.job.ID,
		State:     MediaJobError,
		Phase:     "error",
		Status:    mj.job.StatusText,
		Error:     errMsg,
		Timestamp: now,
	})
	h.mu.Unlock()
}

func (h *Host) executeMediaJob(
	ctx context.Context,
	mj *managedJob,
	client *http.Client,
	baseURL string,
	req MediaJobRequest,
) {
	started := time.Now()
	emitProgress := func(phase, status string, pct, curStep, totStep int) {
		now := time.Now().UTC()
		h.mu.Lock()
		if mj.job.State == MediaJobCanceled {
			h.mu.Unlock()
			return
		}
		mj.job.State = MediaJobRunning
		if phase != "" {
			mj.job.Phase = phase
		}
		if status != "" {
			mj.job.StatusText = status
		}
		if pct > 0 {
			mj.job.Progress = pct
		}
		if curStep > 0 {
			mj.job.CurrentStep = curStep
		}
		if totStep > 0 {
			mj.job.TotalSteps = totStep
		}
		mj.job.UpdatedAt = now
		h.broadcastJobEventLocked(mj, MediaJobEvent{
			Type:        "progress",
			JobID:       mj.job.ID,
			State:       MediaJobRunning,
			Phase:       mj.job.Phase,
			Status:      mj.job.StatusText,
			Progress:    mj.job.Progress,
			CurrentStep: mj.job.CurrentStep,
			TotalSteps:  mj.job.TotalSteps,
			Timestamp:   now,
		})
		h.mu.Unlock()
	}

	// 1. If an input_asset_id was provided (e.g. I2I / I2V cross-plugin pipeline), stage it on remote VM.
	remoteInputPath := ""
	if strings.TrimSpace(req.InputAssetID) != "" {
		emitProgress("uploading_input", "正在上传参考素材到云端显卡...", 6, 0, 0)
		upPath, uErr := h.uploadAssetToRemoteWorker(ctx, client, baseURL, req.InputAssetID)
		if uErr != nil {
			h.failJob(mj, "上传参考图/素材失败: "+uErr.Error())
			return
		}
		remoteInputPath = upPath
	}

	// 2. Submit job to remote `media.v1` engine.
	submitBody := map[string]any{
		"model_id":        req.ModelID,
		"mode":            req.Mode,
		"prompt":          req.Prompt,
		"negative_prompt": req.NegativePrompt,
		"resolution":      req.Resolution,
		"steps":           req.Steps,
		"seed":            req.Seed,
		"video_length":    req.VideoLength,
		"duration_sec":    req.DurationSec,
		"fps":             req.FPS,
		"input_path":      remoteInputPath,
		"extra_settings":  req.ExtraSettings,
	}
	if req.GuidanceScale != nil {
		submitBody["guidance_scale"] = *req.GuidanceScale
	}
	rawBody, _ := json.Marshal(submitBody)
	subCtx, subCancel := context.WithTimeout(ctx, 30*time.Second)
	httpReq, err := http.NewRequestWithContext(subCtx, http.MethodPost, baseURL+"/jobs", bytes.NewReader(rawBody))
	if err != nil {
		subCancel()
		h.failJob(mj, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	subCancel()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		h.failJob(mj, "提交远端生成任务失败: "+err.Error())
		return
	}
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.failJob(mj, fmt.Sprintf("远端引擎拒绝任务 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(respBytes))))
		return
	}
	var subResp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(respBytes, &subResp); err != nil || subResp.JobID == "" {
		snippet := strings.TrimSpace(string(respBytes))
		if len(snippet) > 160 {
			snippet = snippet[:160]
		}
		h.failJob(mj, fmt.Sprintf("远端引擎未返回有效 job_id (HTTP %d: %s)", resp.StatusCode, snippet))
		return
	}
	h.mu.Lock()
	mj.job.RemoteJobID = subResp.JobID
	mj.job.State = MediaJobRunning
	h.mu.Unlock()

	// 3. Stream SSE events from remote `GET /colabhive/v1/jobs/{remoteJobID}/events`.
	evReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/jobs/"+url.PathEscape(subResp.JobID)+"/events", nil)
	if err != nil {
		h.failJob(mj, err.Error())
		return
	}
	evReq.Header.Set("Accept", "text/event-stream")
	evResp, err := client.Do(evReq)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		h.failJob(mj, "连接远端实时进度流失败: "+err.Error())
		return
	}
	defer evResp.Body.Close()
	if evResp.StatusCode < 200 || evResp.StatusCode >= 300 {
		errTxt, _ := io.ReadAll(io.LimitReader(evResp.Body, 64<<10))
		h.failJob(mj, fmt.Sprintf("远端进度流异常 (HTTP %d): %s", evResp.StatusCode, strings.TrimSpace(string(errTxt))))
		return
	}

	type remoteFileRef struct {
		FileID   string `json:"file_id"`
		Filename string `json:"filename"`
		MimeType string `json:"mime_type"`
		Kind     string `json:"kind"`
	}
	var completedFiles []remoteFileRef
	var remoteErr string
	sawDone := false

	scanner := bufio.NewScanner(evResp.Body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev struct {
			Type           string          `json:"type"`
			Phase          string          `json:"phase"`
			Status         string          `json:"status"`
			Progress       int             `json:"progress"`
			CurrentStep    int             `json:"current_step"`
			TotalSteps     int             `json:"total_steps"`
			PreviewDataURL string          `json:"preview_data_url"`
			Text           string          `json:"text"`
			Error          string          `json:"error"`
			Files          []remoteFileRef `json:"files"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}

		switch ev.Type {
		case "progress":
			emitProgress(ev.Phase, ev.Status, ev.Progress, ev.CurrentStep, ev.TotalSteps)
		case "preview":
			now := time.Now().UTC()
			h.mu.Lock()
			mj.job.PreviewDataURL = ev.PreviewDataURL
			if ev.Progress > 0 {
				mj.job.Progress = ev.Progress
			}
			if ev.Status != "" {
				mj.job.StatusText = ev.Status
			}
			mj.job.UpdatedAt = now
			h.broadcastJobEventLocked(mj, MediaJobEvent{
				Type:           "preview",
				JobID:          mj.job.ID,
				State:          MediaJobRunning,
				Phase:          ev.Phase,
				Status:         ev.Status,
				Progress:       mj.job.Progress,
				CurrentStep:    ev.CurrentStep,
				TotalSteps:     ev.TotalSteps,
				PreviewDataURL: ev.PreviewDataURL,
				Timestamp:      now,
			})
			h.mu.Unlock()
		case "log":
			ln := strings.TrimSpace(ev.Text)
			if ln != "" {
				now := time.Now().UTC()
				h.mu.Lock()
				mj.job.Logs = append(mj.job.Logs, ln)
				if len(mj.job.Logs) > 120 {
					mj.job.Logs = append([]string(nil), mj.job.Logs[len(mj.job.Logs)-120:]...)
				}
				mj.job.UpdatedAt = now
				h.broadcastJobEventLocked(mj, MediaJobEvent{
					Type:      "log",
					JobID:     mj.job.ID,
					State:     MediaJobRunning,
					LogLine:   ln,
					Timestamp: now,
				})
				h.mu.Unlock()
			}
		case "done":
			completedFiles = ev.Files
			sawDone = true
		case "error":
			remoteErr = ev.Error
			if remoteErr == "" {
				remoteErr = "远端生成任务失败"
			}
		case "canceled":
			return
		}
	}

	if ctx.Err() != nil {
		return
	}
	if remoteErr != "" {
		h.failJob(mj, remoteErr)
		return
	}
	if !sawDone || len(completedFiles) == 0 {
		h.failJob(mj, "生成任务已结束，但未收到成品文件")
		return
	}

	// 4. Download every generated artifact over Comm Tunnel v3 straight into the Local Asset Vault!
	emitProgress("downloading_output", "云端渲染完成，正在同步成品到本地永久画廊...", 96, 0, 0)
	genMS := time.Since(started).Milliseconds()
	savedAssets := make([]*MediaAsset, 0, len(completedFiles))
	savedIDs := make([]string, 0, len(completedFiles))
	for _, rf := range completedFiles {
		fURL := baseURL + "/files/" + url.PathEscape(rf.FileID)
		dlCtx, dlCancel := context.WithTimeout(ctx, 5*time.Minute)
		dlReq, _ := http.NewRequestWithContext(dlCtx, http.MethodGet, fURL, nil)
		dlResp, dlErr := client.Do(dlReq)
		if dlErr != nil {
			dlCancel()
			h.failJob(mj, "拉取成品文件失败: "+dlErr.Error())
			return
		}
		if dlResp.StatusCode < 200 || dlResp.StatusCode >= 300 {
			_ = dlResp.Body.Close()
			dlCancel()
			h.failJob(mj, fmt.Sprintf("拉取成品文件失败 (HTTP %d)", dlResp.StatusCode))
			return
		}
		meta := MediaAsset{
			Kind:           AssetKind(rf.Kind),
			MimeType:       rf.MimeType,
			Prompt:         req.Prompt,
			NegativePrompt: req.NegativePrompt,
			ModelID:        req.ModelID,
			ModelName:      req.ModelName,
			Mode:           req.Mode,
			Resolution:     req.Resolution,
			Steps:          req.Steps,
			Seed:           req.Seed,
			DurationSec:    req.DurationSec,
			GenerationMS:   genMS,
			EngineID:       mj.job.EngineID,
			InstanceID:     mj.job.InstanceID,
			JobID:          mj.job.ID,
			SourcePluginID: req.SourcePluginID,
			ParentAssetID:  strings.TrimSpace(req.InputAssetID),
		}
		asset, sErr := h.assets.Save(meta, rf.Filename, dlResp.Body)
		_ = dlResp.Body.Close()
		dlCancel()
		if sErr != nil {
			h.failJob(mj, "保存成品到本地资产库失败: "+sErr.Error())
			return
		}
		savedAssets = append(savedAssets, asset)
		savedIDs = append(savedIDs, asset.ID)
	}

	now := time.Now().UTC()
	h.mu.Lock()
	mj.job.State = MediaJobDone
	mj.job.Phase = "done"
	mj.job.Progress = 100
	mj.job.StatusText = fmt.Sprintf("生成完成（耗时 %.1fs · 已保存至本地永久画廊）", float64(genMS)/1000.0)
	mj.job.AssetIDs = savedIDs
	mj.job.Assets = savedAssets
	mj.job.DurationMS = genMS
	mj.job.UpdatedAt = now
	mj.job.CompletedAt = &now
	h.broadcastJobEventLocked(mj, MediaJobEvent{
		Type:      "done",
		JobID:     mj.job.ID,
		State:     MediaJobDone,
		Phase:     "done",
		Status:    mj.job.StatusText,
		Progress:  100,
		Assets:    savedAssets,
		Timestamp: now,
	})
	h.mu.Unlock()
}

func (h *Host) uploadAssetToRemoteWorker(ctx context.Context, client *http.Client, baseURL, assetID string) (string, error) {
	asset, f, err := h.assets.OpenBlob(assetID)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(asset.Filename))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, io.LimitReader(f, 64<<20)); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	upCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(upCtx, http.MethodPost, baseURL+"/uploads", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("remote upload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || strings.TrimSpace(out.Path) == "" {
		return "", errors.New("remote worker returned empty upload path")
	}
	return out.Path, nil
}

func cloneMediaJob(j *MediaJob) *MediaJob {
	if j == nil {
		return nil
	}
	cp := *j
	cp.Logs = append([]string(nil), j.Logs...)
	cp.AssetIDs = append([]string(nil), j.AssetIDs...)
	if len(j.Assets) > 0 {
		cp.Assets = make([]*MediaAsset, len(j.Assets))
		for i, a := range j.Assets {
			cp.Assets[i] = cloneAsset(a)
		}
	}
	if j.CompletedAt != nil {
		t := *j.CompletedAt
		cp.CompletedAt = &t
	}
	return &cp
}
