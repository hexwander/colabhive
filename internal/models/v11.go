package models

import (
	"strings"
	"time"
)

// ============================================================================
// Compute Provider Identity (Google Colab primary; legacy DB field compatible)
// ============================================================================

// ProviderType identifies the upstream GPU compute platform backing an account.
type ProviderType string

const (
	ProviderColab ProviderType = "colab" // Google Colab (Free T4 + Pro/Pro+/Ultra G4/A100/H100)
)

// NormalizeProviderType normalizes raw provider strings, defaulting empty values to ProviderColab.
// Any legacy non-Colab string preserved in an existing SQLite DB is returned lowercased so callers
// checking `!= ProviderColab` safely skip non-Colab rows without misrouting them to Colab.
func NormalizeProviderType(raw string) ProviderType {
	clean := strings.ToLower(strings.TrimSpace(raw))
	switch clean {
	case "", "colab", "google", "google-colab":
		return ProviderColab
	default:
		return ProviderType(clean)
	}
}

// ProviderAccountMeta stores legacy optional platform metadata on an Account row
// (kept for backwards compatibility when reading existing SQLite databases).
type ProviderAccountMeta struct {
	Username         string  `json:"username,omitempty"`
	WeeklyQuotaHours float64 `json:"weekly_quota_hours,omitempty"`
	WeeklyUsedHours  float64 `json:"weekly_used_hours,omitempty"`
	WeeklyResetLabel string  `json:"weekly_reset_label,omitempty"`
	MaxBidPriceUSDHr float64 `json:"max_bid_price_usd_hr,omitempty"`
	BalanceUSD       float64 `json:"balance_usd,omitempty"`
	CostUSDPerHr     float64 `json:"cost_usd_per_hr,omitempty"`
	PreferredRegion  string  `json:"preferred_region,omitempty"`
	DefaultGPUName   string  `json:"default_gpu_name,omitempty"`
	APIEndpoint      string  `json:"api_endpoint,omitempty"`
}

// ============================================================================
// v1.1 Pillar 1: Private AI Chat & Zero-Tunnel Local `/v1` + Ollama Gateway
// ============================================================================

// LLMEngineType identifies the inference engine booted inside a pooled GPU VM.
type LLMEngineType string

const (
	LLMEngineOllama   LLMEngineType = "ollama"
	LLMEngineVLLM     LLMEngineType = "vllm"
	LLMEngineLlamaCpp LLMEngineType = "llamacpp"
)

// LLMGatewayState represents the lifecycle state of the local `/v1` & Ollama gateway.
type LLMGatewayState string

const (
	LLMStateStopped  LLMGatewayState = "stopped"
	LLMStateStarting LLMGatewayState = "starting"
	LLMStatePulling  LLMGatewayState = "pulling"
	LLMStateReady    LLMGatewayState = "ready"
	LLMStateFailover LLMGatewayState = "failover"
	LLMStateError    LLMGatewayState = "error"
)

// LLMModelSpec defines a curated open-source LLM in the 1-click Private AI Chat catalog.
type LLMModelSpec struct {
	ID              string          `json:"id"`
	DisplayName     string          `json:"display_name"`
	DisplayNameZh   string          `json:"display_name_zh"`
	ProviderFamily  string          `json:"provider_family"` // e.g. "Qwen", "DeepSeek", "Gemma", "Llama"
	Summary         string          `json:"summary"`
	SummaryZh       string          `json:"summary_zh"`
	ParameterSize   string          `json:"parameter_size"` // e.g. "8B", "14B", "32B", "72B"
	Quantization    string          `json:"quantization"`   // e.g. "Q4_K_M", "BF16", "AWQ"
	ContextLength   int             `json:"context_length"`
	MinVRAMGB       int             `json:"min_vram_gb"`
	RecommendedGPU  AcceleratorType `json:"recommended_gpu"`
	FreeTierReady   bool            `json:"free_tier_ready"` // True if runs smoothly on 16GB Free Colab T4!
	DefaultEngine   LLMEngineType   `json:"default_engine"`
	OllamaTag       string          `json:"ollama_tag"`
	HuggingFaceRepo string          `json:"huggingface_repo,omitempty"`
	Tags            []string        `json:"tags"`
}

// LLMGatewayStatus reports live state of the Private AI Chat & `/v1` Gateway.
type LLMGatewayStatus struct {
	Running          bool            `json:"running"`
	State            LLMGatewayState `json:"state"`
	StatusStep       string          `json:"status_step,omitempty"`
	StatusStepZh     string          `json:"status_step_zh,omitempty"`
	ActiveModelID    string          `json:"active_model_id,omitempty"`
	ActiveModelName  string          `json:"active_model_name,omitempty"`
	Engine           LLMEngineType   `json:"engine,omitempty"`
	Strategy         RoutingStrategy `json:"strategy,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	AccountID        string          `json:"account_id,omitempty"`
	AccountLabel     string          `json:"account_label,omitempty"`
	Provider         ProviderType    `json:"provider,omitempty"`
	AssignedGPU      AcceleratorType `json:"assigned_gpu,omitempty"`
	FreeTierActive   bool            `json:"free_tier_active"`
	AutoFailover     bool            `json:"auto_failover"`
	FailoverCount    int             `json:"failover_count"`
	LastFailoverNote string          `json:"last_failover_note,omitempty"`
	OpenAIBaseURL    string          `json:"openai_base_url"`
	AnthropicBaseURL string          `json:"anthropic_base_url"`
	OllamaBaseURL    string          `json:"ollama_base_url"`
	IdleAutoStopMin  int             `json:"idle_auto_stop_min"`
	RequestsServed   int64           `json:"requests_served"`
	TokensGenerated  int64           `json:"tokens_generated"`
	AvgTokensPerSec  float64         `json:"avg_tokens_per_sec"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	LastActiveAt     *time.Time      `json:"last_active_at,omitempty"`
	ErrorMessage     string          `json:"error_message,omitempty"`
	DeployLogs       []string        `json:"deploy_logs,omitempty"`
}

// LLMStartRequest configures 1-click startup or hot-switching of the Private AI Chat gateway.
type LLMStartRequest struct {
	ModelID         string          `json:"model_id"`
	PreferredGPU    AcceleratorType `json:"preferred_gpu,omitempty"`
	AccountID       string          `json:"account_id,omitempty"`
	Strategy        RoutingStrategy `json:"strategy,omitempty"`
	Engine          LLMEngineType   `json:"engine,omitempty"`
	AutoFailover    *bool           `json:"auto_failover,omitempty"`
	IdleAutoStopMin int             `json:"idle_auto_stop_min,omitempty"`
}

// ============================================================================
// v1.1 Pillar 2: 1-Click AI App Store & Zero-Tunnel Private Local Port Bridge
// ============================================================================

// AppCategory classifies 1-Click AI App templates for non-technical users.
type AppCategory string

const (
	AppCategoryVideo     AppCategory = "video"
	AppCategoryImage     AppCategory = "image"
	AppCategoryAudio     AppCategory = "audio"
	AppCategoryWorkspace AppCategory = "workspace"
	AppCategoryCustom    AppCategory = "custom"
)

// AppModelOption declares an explicit selectable model/weight preset for an App Store template or plugin.
type AppModelOption struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	NameZh         string          `json:"name_zh"`
	Summary        string          `json:"summary,omitempty"`
	SummaryZh      string          `json:"summary_zh,omitempty"`
	MinVRAMGB      int             `json:"min_vram_gb"`
	RecommendedGPU AcceleratorType `json:"recommended_gpu"`
	DownloadURL    string          `json:"download_url,omitempty"`
	TargetSubdir   string          `json:"target_subdir,omitempty"`
	Filename       string          `json:"filename,omitempty"`
}

// PluginKind classifies orthogonal Plugin Platform v2 plugins:
// - "engine": runs on a cloud GPU VM and implements capability protocols (e.g. media.image.v1)
// - "ui": runs locally (0ms instant load in an isolated loopback origin) consuming capabilities
// - "bundle": ships both a cloud engine and a companion local UI bundle
type PluginKind string

const (
	PluginKindEngine PluginKind = "engine"
	PluginKindUI     PluginKind = "ui"
	PluginKindBundle PluginKind = "bundle"
)

// PluginUIConfig declares how a plugin's user interface is rendered by ColabHive.
type PluginUIConfig struct {
	Mode      string `json:"mode,omitempty"`       // "remote_proxy" | "local_bundle" | "builtin_route"
	BundleDir string `json:"bundle_dir,omitempty"` // Relative dir inside plugin folder (e.g. "ui/dist")
	Entry     string `json:"entry,omitempty"`      // Default "index.html"
	Route     string `json:"route,omitempty"`      // Built-in SPA route (e.g. "/studio/media")
}

// AppTemplate defines a 1-click cloud GPU application card (built-in core app or JSON plugin).
type AppTemplate struct {
	ID                     string           `json:"id"`
	Name                   string           `json:"name"`
	NameZh                 string           `json:"name_zh"`
	Tagline                string           `json:"tagline"`
	TaglineZh              string           `json:"tagline_zh"`
	Description            string           `json:"description"`
	DescriptionZh          string           `json:"description_zh"`
	Category               AppCategory      `json:"category"`
	Icon                   string           `json:"icon"`
	DefaultRemotePort      int              `json:"default_remote_port"`
	DefaultLocalPort       int              `json:"default_local_port"`
	MinVRAMGB              int              `json:"min_vram_gb"`
	RecommendedGPU         AcceleratorType  `json:"recommended_gpu"`
	FreeTierCompatible     bool             `json:"free_tier_compatible"`
	EstBootSec             int              `json:"est_boot_sec"`
	Highlights             []string         `json:"highlights"`
	HighlightsZh           []string         `json:"highlights_zh"`
	SupportsPortBridge     bool             `json:"supports_port_bridge"`
	PluginIntegrationTipEn string           `json:"plugin_integration_tip_en,omitempty"`
	PluginIntegrationTipZh string           `json:"plugin_integration_tip_zh,omitempty"`
	IsPlugin               bool             `json:"is_plugin,omitempty"`
	Author                 string           `json:"author,omitempty"`
	Version                string           `json:"version,omitempty"`
	Kind                   PluginKind       `json:"kind,omitempty"`
	Provides               []string         `json:"provides,omitempty"`
	Consumes               []string         `json:"consumes,omitempty"`
	OptionalCapabilities   []string         `json:"optional_capabilities,omitempty"`
	Permissions            []string         `json:"permissions,omitempty"`
	MediaAPIBase           string           `json:"media_api_base,omitempty"`
	UI                     *PluginUIConfig  `json:"ui,omitempty"`
	ModelOptions           []AppModelOption `json:"model_options,omitempty"`
	SupportsCustomModelURL bool             `json:"supports_custom_model_url,omitempty"`
	BootstrapScript        string           `json:"-"`
}

// AppInstanceState tracks the live status of a launched 1-Click AI App.
type AppInstanceState string

const (
	AppStateStarting AppInstanceState = "starting"
	AppStateRunning  AppInstanceState = "running"
	AppStateFailover AppInstanceState = "failover"
	AppStateStopping AppInstanceState = "stopping"
	AppStateStopped  AppInstanceState = "stopped"
	AppStateError    AppInstanceState = "error"
)

// AppInstance represents a running or recently launched 1-Click AI App backed by a pooled GPU VM.
type AppInstance struct {
	ID                string           `json:"id"`
	TemplateID        string           `json:"template_id"`
	Name              string           `json:"name"`
	NameZh            string           `json:"name_zh"`
	Category          AppCategory      `json:"category"`
	State             AppInstanceState `json:"state"`
	StatusStep        string           `json:"status_step,omitempty"`
	StatusStepZh      string           `json:"status_step_zh,omitempty"`
	ProgressPct       int              `json:"progress_pct"`
	SessionID         string           `json:"session_id,omitempty"`
	AccountID         string           `json:"account_id,omitempty"`
	AccountLabel      string           `json:"account_label,omitempty"`
	Provider          ProviderType     `json:"provider,omitempty"`
	AssignedGPU       AcceleratorType  `json:"assigned_gpu,omitempty"`
	FreeTierActive    bool             `json:"free_tier_active"`
	RemotePort        int              `json:"remote_port"`
	LocalPort         int              `json:"local_port,omitempty"`
	LocalBridgeActive bool             `json:"local_bridge_active"`
	LocalURL          string           `json:"local_url,omitempty"`
	WebProxyURL       string           `json:"web_proxy_url"`
	IdleAutoStopMin   int              `json:"idle_auto_stop_min"`
	AutoResumeEnabled bool             `json:"auto_resume_enabled"`
	FailoverCount     int              `json:"failover_count"`
	LastFailoverNote  string           `json:"last_failover_note,omitempty"`
	SelectedModelID   string           `json:"selected_model_id,omitempty"`
	SelectedModelName string           `json:"selected_model_name,omitempty"`
	ModelDownloadURL  string           `json:"model_download_url,omitempty"`
	CheckpointDir     string           `json:"checkpoint_dir,omitempty"`
	CustomRepoURL     string           `json:"custom_repo_url,omitempty"`
	CustomCommand     string           `json:"custom_command,omitempty"`
	Provides          []string         `json:"provides,omitempty"`
	MediaAPIBase      string           `json:"media_api_base,omitempty"`
	StudioRoute       string           `json:"studio_route,omitempty"`
	LaunchMode        string           `json:"launch_mode,omitempty"`
	ErrorMessage      string           `json:"error_message,omitempty"`
	DeployLogs        []string         `json:"deploy_logs,omitempty"`
	StartedAt         time.Time        `json:"started_at"`
	LastActiveAt      time.Time        `json:"last_active_at"`
}

// AppLaunchRequest launches a curated AI App template or community JSON plugin with explicit GPU & model config.
type AppLaunchRequest struct {
	TemplateID         string          `json:"template_id"`
	Name               string          `json:"name,omitempty"`
	PreferredGPU       AcceleratorType `json:"preferred_gpu,omitempty"`
	AccountID          string          `json:"account_id,omitempty"`
	Strategy           RoutingStrategy `json:"strategy,omitempty"`
	SelectedModelID    string          `json:"selected_model_id,omitempty"`
	ModelDownloadURL   string          `json:"model_download_url,omitempty"`
	PreferredLocalPort int             `json:"preferred_local_port,omitempty"`
	IdleAutoStopMin    int             `json:"idle_auto_stop_min,omitempty"`
	AutoResumeEnabled  *bool           `json:"auto_resume_enabled,omitempty"`
	CustomRepoURL      string          `json:"custom_repo_url,omitempty"`
	CustomCommand      string          `json:"custom_command,omitempty"`
	CustomRemotePort   int             `json:"custom_remote_port,omitempty"`
	LaunchMode         string          `json:"launch_mode,omitempty"`        // "headless" (default for media.v1 engines) | "native_webui"
	AutoFallback       *bool           `json:"auto_fallback,omitempty"`      // Allow falling back to another GPU tier if requested tier is out of stock
	QueueRetryEnabled  bool            `json:"queue_retry_enabled,omitempty"` // Enable safe polite-backoff GPU waitlist when Colab returns 412/503 or pool is busy
	QueueMaxWaitSec    int             `json:"queue_max_wait_sec,omitempty"` // Waitlist deadline in seconds (default 600, max 1800)
}

// ============================================================================
// v1.1 Pillar 4: Auto-Checkpoint Preemption Relay & Shareable Savings Receipt
// ============================================================================

// CheckpointPolicy governs automatic progress snapshots and cross-account preemption resume.
type CheckpointPolicy struct {
	AutoResumeEnabled  bool      `json:"auto_resume_enabled"`
	SyncIntervalSec    int       `json:"sync_interval_sec"`
	WatchPaths         []string  `json:"watch_paths"`
	MaxSnapshotsPerJob int       `json:"max_snapshots_per_job"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// DefaultCheckpointPolicy returns default checkpoint & auto-resume settings.
func DefaultCheckpointPolicy() CheckpointPolicy {
	return CheckpointPolicy{
		AutoResumeEnabled:  true,
		SyncIntervalSec:    60,
		WatchPaths:         []string{"/content/checkpoints", "/content/outputs"},
		MaxSnapshotsPerJob: 3,
		UpdatedAt:          time.Now().UTC(),
	}
}

// CheckpointRecord tracks a synced workspace snapshot and any automatic cross-account relay.
type CheckpointRecord struct {
	ID                  string          `json:"id"`
	JobID               string          `json:"job_id"`
	JobTitle            string          `json:"job_title"`
	JobKind             string          `json:"job_kind"` // "app" | "experiment" | "session" | "llm"
	SourceSessionID     string          `json:"source_session_id"`
	SourceAccountLabel  string          `json:"source_account_label"`
	ResumedSessionID    string          `json:"resumed_session_id,omitempty"`
	ResumedAccountLabel string          `json:"resumed_account_label,omitempty"`
	AssignedGPU         AcceleratorType `json:"assigned_gpu,omitempty"`
	RemotePath          string          `json:"remote_path"`
	LocalSnapshotDir    string          `json:"local_snapshot_dir,omitempty"`
	FileCount           int             `json:"file_count"`
	TotalBytes          int64           `json:"total_bytes"`
	ProgressPct         float64         `json:"progress_pct"`
	RelayCount          int             `json:"relay_count"`
	Status              string          `json:"status"` // "synced" | "relaying" | "resumed" | "failed"
	LastRelayNoteEn     string          `json:"last_relay_note_en,omitempty"`
	LastRelayNoteZh     string          `json:"last_relay_note_zh,omitempty"`
	ResumeCommand       string          `json:"resume_command,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// CheckpointSyncRequest triggers an immediate progress snapshot from a running session.
type CheckpointSyncRequest struct {
	SessionID     string  `json:"session_id"`
	JobID         string  `json:"job_id,omitempty"`
	JobTitle      string  `json:"job_title,omitempty"`
	JobKind       string  `json:"job_kind,omitempty"`
	RemotePath    string  `json:"remote_path,omitempty"`
	ProgressPct   float64 `json:"progress_pct,omitempty"`
	ResumeCommand string  `json:"resume_command,omitempty"`
}

// CheckpointResumeRequest triggers cross-account restoration & continuation on a fresh GPU VM.
type CheckpointResumeRequest struct {
	PreferredGPU  AcceleratorType `json:"preferred_gpu,omitempty"`
	Strategy      RoutingStrategy `json:"strategy,omitempty"`
	ResumeCommand string          `json:"resume_command,omitempty"`
}

// SavingsReceiptStats powers the 1-click Shareable Compute Savings Poster (100% PII-redacted).
type SavingsReceiptStats struct {
	GeneratedAt              time.Time `json:"generated_at"`
	PoolTitle                string    `json:"pool_title"`
	TotalAccounts            int       `json:"total_accounts"`
	ProvidersConnected       []string  `json:"providers_connected"`
	TotalGPUHours            float64   `json:"total_gpu_hours"`
	FreeGPUHoursHarvested    float64   `json:"free_gpu_hours_harvested"`
	G4EquivalentHours        float64   `json:"g4_equivalent_hours"`
	TotalCUBurned            float64   `json:"total_cu_burned"`
	SavedIdleWasteCU         float64   `json:"saved_idle_waste_cu"`
	WasteZeroEfficiencyPct   float64   `json:"waste_zero_efficiency_pct"`
	AutoResumeRelays         int       `json:"auto_resume_relays"`
	AutoTeardowns            int       `json:"auto_teardowns"`
	ZombieAborts             int       `json:"zombie_aborts"`
	EstimatedSavedUSD        float64   `json:"estimated_saved_usd"`
	EstimatedSavedCNY        float64   `json:"estimated_saved_cny"`
	TopGPUsUsed              []string  `json:"top_gpus_used"`
	RankBadgeEn              string    `json:"rank_badge_en"`
	RankBadgeZh              string    `json:"rank_badge_zh"`
	ShareCopyEn              string    `json:"share_copy_en"`
	ShareCopyZh              string    `json:"share_copy_zh"`
}

// v1.1 Real-Time WebSocket Event Types.
const (
	WSEventLLMGateway      WSEventType = "llm_gateway_updated"
	WSEventAppInstance     WSEventType = "app_instance_updated"
	WSEventCheckpointRelay WSEventType = "checkpoint_relay"
)
