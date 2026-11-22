package models

import "time"

// RoutingStrategy defines the pool scheduler's account selection algorithm.
type RoutingStrategy string

const (
	StrategyWasteZero         RoutingStrategy = "waste-zero"         // Prioritize expiring 90d CUs & refilling Free milli-CCUs
	StrategyMaxBalance        RoutingStrategy = "max-balance"        // Route heavy runs to deepest CU reservoir
	StrategyStickySession     RoutingStrategy = "sticky-session"     // Reuse warm kernel bound to agent/session
	StrategyRoundRobin        RoutingStrategy = "round-robin"        // Spread concurrent VMs evenly across healthy accounts
	StrategyFreeOpportunistic RoutingStrategy = "free-opportunistic" // Try Free T4/CPU windows first before touching Paid CUs
)

// RouterPolicy configures global account routing, sticky affinity, and hardware fallback waterfalls.
type RouterPolicy struct {
	DefaultStrategy         RoutingStrategy   `json:"default_strategy"`
	StickySessionEnabled    bool              `json:"sticky_session_enabled"`
	StickyIdleTTLSec        int               `json:"sticky_idle_ttl_sec"`
	AutoFallbackEnabled     bool              `json:"auto_fallback_enabled"`
	FallbackChain           []AcceleratorType `json:"fallback_chain"`
	LargeVRAMFallbackChain  []AcceleratorType `json:"large_vram_fallback_chain"`
	AutoReleaseOnZombie     bool              `json:"auto_release_on_zombie"`
	KeepaliveIntervalSec    int               `json:"keepalive_interval_sec"`
	ResourcePollIntervalSec int               `json:"resource_poll_interval_sec"`
	UpdatedAt               time.Time         `json:"updated_at"`
}

// DefaultRouterPolicy returns ColabHive's recommended out-of-the-box router configuration.
func DefaultRouterPolicy() RouterPolicy {
	return RouterPolicy{
		DefaultStrategy:         StrategyWasteZero,
		StickySessionEnabled:    true,
		StickyIdleTTLSec:        1200, // 20 minutes
		AutoFallbackEnabled:     true,
		FallbackChain:           append([]AcceleratorType(nil), DefaultGPUFallbackChain...),
		LargeVRAMFallbackChain:  append([]AcceleratorType(nil), LargeVRAMFallbackChain...),
		AutoReleaseOnZombie:     true,
		KeepaliveIntervalSec:    8,
		ResourcePollIntervalSec: 10,
		UpdatedAt:               time.Now().UTC(),
	}
}

// AgentAPIKey represents a scoped `ch_sk_...` credential issued to AI coding agents, CLIs, or SDKs.
type AgentAPIKey struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	KeyPrefix        string            `json:"key_prefix"`        // e.g. "ch_sk_live_8f9a..."
	RawKey           string            `json:"raw_key,omitempty"` // Returned ONLY once upon creation
	KeyHash          string            `json:"-"`                 // SHA-256 digest stored at rest
	AllowedGroups    []string          `json:"allowed_groups"`    // Empty = all non-restricted groups
	AllowedGPUs      []AcceleratorType `json:"allowed_gpus"`      // Empty = all tiers
	DefaultStrategy  RoutingStrategy   `json:"default_strategy,omitempty"`
	MaxBudgetCUTotal float64           `json:"max_budget_cu_total"` // 0 = unlimited
	MaxBudgetCUDaily float64           `json:"max_budget_cu_daily"` // 0 = unlimited
	UsedCUTotal      float64           `json:"used_cu_total"`
	UsedCUToday      float64           `json:"used_cu_today"`
	DailyUsageDate   string            `json:"daily_usage_date,omitempty"`
	MaxConcurrentVMs int               `json:"max_concurrent_vms"`
	IdleAutoKillMin  int               `json:"idle_auto_kill_min"`
	Enabled          bool              `json:"enabled"`
	LastUsedAt       *time.Time        `json:"last_used_at,omitempty"`
	ExpiresAt        *time.Time        `json:"expires_at,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

// CreateAPIKeyRequest defines inputs for minting a new `ch_sk_...` Agent API key.
type CreateAPIKeyRequest struct {
	Name             string            `json:"name"`
	AllowedGroups    []string          `json:"allowed_groups,omitempty"`
	AllowedGPUs      []AcceleratorType `json:"allowed_gpus,omitempty"`
	DefaultStrategy  RoutingStrategy   `json:"default_strategy,omitempty"`
	MaxBudgetCUTotal float64           `json:"max_budget_cu_total,omitempty"`
	MaxBudgetCUDaily float64           `json:"max_budget_cu_daily,omitempty"`
	MaxConcurrentVMs int               `json:"max_concurrent_vms,omitempty"`
	IdleAutoKillMin  int               `json:"idle_auto_kill_min,omitempty"`
	ExpiresInDays    int               `json:"expires_in_days,omitempty"`
}

// RouteBinding represents an active Sticky-Agent-Session affinity link (`Agent -> Session -> Account -> GPU`).
type RouteBinding struct {
	StickyKey           string          `json:"sticky_key"`
	ClientName          string          `json:"client_name"`
	APIKeyID            string          `json:"api_key_id,omitempty"`
	APIKeyPrefix        string          `json:"api_key_prefix,omitempty"`
	Strategy            RoutingStrategy `json:"strategy"`
	SessionID           string          `json:"session_id"`
	SessionName         string          `json:"session_name"`
	KernelID            string          `json:"kernel_id,omitempty"`
	AccountID           string          `json:"account_id"`
	AccountEmail        string          `json:"account_email"`
	ProxySummary        string          `json:"proxy_summary,omitempty"`
	Accelerator         AcceleratorType `json:"accelerator"`
	ActiveCalls         int             `json:"active_calls"`
	IdleTTLRemainingSec int             `json:"idle_ttl_remaining_sec"`
	PinnedAt            time.Time       `json:"pinned_at"`
	LastActiveAt        time.Time       `json:"last_active_at"`
}

// TransportType identifies how an Agent or CLI invoked ColabHive.
type TransportType string

const (
	TransportMCPHTTP  TransportType = "mcp-http"
	TransportMCPSSE   TransportType = "mcp-sse"
	TransportMCPStdio TransportType = "mcp-stdio"
	TransportREST     TransportType = "rest"
	TransportCLI      TransportType = "cli"
	TransportSDK      TransportType = "sdk"
)

// ToolTraceEvent records a live MCP tool call or REST execution in the Agent Router Trace Inspector.
type ToolTraceEvent struct {
	ID              string          `json:"id"`
	Timestamp       time.Time       `json:"timestamp"`
	Caller          string          `json:"caller"`
	APIKeyPrefix    string          `json:"api_key_prefix,omitempty"`
	Transport       TransportType   `json:"transport"`
	ToolOrEndpoint  string          `json:"tool_or_endpoint"`
	SessionID       string          `json:"session_id,omitempty"`
	AccountID       string          `json:"account_id,omitempty"`
	AccountEmail    string          `json:"account_email,omitempty"`
	RequestedGPU    AcceleratorType `json:"requested_gpu,omitempty"`
	AssignedGPU     AcceleratorType `json:"assigned_gpu,omitempty"`
	FallbackTrace   string          `json:"fallback_trace,omitempty"`
	DurationMs      int64           `json:"duration_ms"`
	CUDelta         float64         `json:"cu_delta"`
	Status          string          `json:"status"` // "ok" | "fallback" | "error"
	StatusCode      int             `json:"status_code"`
	RequestPreview  string          `json:"request_preview,omitempty"`
	ResponsePreview string          `json:"response_preview,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
}

// AgentClientID enumerates AI coding clients supported by ColabHive's `cc-switch`-style 1-click injector.
type AgentClientID string

const (
	AgentAntigravity   AgentClientID = "antigravity"
	AgentClaudeCode    AgentClientID = "claude-code"
	AgentClaudeDesktop AgentClientID = "claude-desktop"
	AgentCodex         AgentClientID = "codex"
	AgentGeminiCLI     AgentClientID = "gemini-cli"
	AgentCursor        AgentClientID = "cursor"
	AgentWindsurf      AgentClientID = "windsurf"
	AgentOpenCode      AgentClientID = "opencode"
)

// AllSupportedAgentClients lists all supported 1-click MCP target clients in UI display order.
var AllSupportedAgentClients = []AgentClientID{
	AgentAntigravity,
	AgentClaudeCode,
	AgentClaudeDesktop,
	AgentCodex,
	AgentGeminiCLI,
	AgentCursor,
	AgentWindsurf,
	AgentOpenCode,
}

// AgentClientConfigStatus reports detection, backup, surgical injection state, and copy-paste snippets
// for each local/WSL2 AI coding agent.
type AgentClientConfigStatus struct {
	ClientID      AgentClientID `json:"client_id"`
	DisplayName   string        `json:"display_name"`
	ConfigPath    string        `json:"config_path"`
	WSLConfigPath string        `json:"wsl_config_path,omitempty"`
	Format        string        `json:"format"`    // "json" | "toml" | "jsonc"
	Transport     string        `json:"transport"` // "http" | "stdio"
	Detected      bool          `json:"detected"`
	Injected      bool          `json:"injected"`
	BackupExists  bool          `json:"backup_exists"`
	BackupPath    string        `json:"backup_path,omitempty"`
	HotReloadNote string        `json:"hot_reload_note,omitempty"`
	Snippet       string        `json:"snippet"`
	CLICommand    string        `json:"cli_command,omitempty"`
	DeepLinkURI   string        `json:"deep_link_uri,omitempty"`
	LastSyncedAt  *time.Time    `json:"last_synced_at,omitempty"`
}

// AgentInjectRequest requests surgical injection or restoration of a local AI client's MCP config.
type AgentInjectRequest struct {
	ClientID      AgentClientID `json:"client_id"`
	ServerBaseURL string        `json:"server_base_url,omitempty"`
	APIKey        string        `json:"api_key,omitempty"`
	Transport     string        `json:"transport,omitempty"` // "http" | "stdio"
	IncludeWSL2   bool          `json:"include_wsl2,omitempty"`
}

// WSEventType enumerates real-time messages pushed over `/ws/events`.
type WSEventType string

const (
	WSEventQuotaUpdated      WSEventType = "quota_updated"
	WSEventAccountUpdated    WSEventType = "account_updated"
	WSEventSessionUpdated    WSEventType = "session_updated"
	WSEventSessionOutput     WSEventType = "session_output"
	WSEventSessionStream     WSEventType = WSEventSessionOutput
	WSEventExperimentUpdated WSEventType = "experiment_updated"
	WSEventTraceEvent        WSEventType = "trace_event"
	WSEventGuardrailPreempt  WSEventType = "guardrail_preempt"
	WSEventZombieAlert       WSEventType = "zombie_alert"
	WSEventHeartbeat         WSEventType = "heartbeat"
)

// SessionStreamChunk streams live stdout/stderr/system lines for `/exp` WebSocket terminals.
type SessionStreamChunk struct {
	SessionID    string             `json:"session_id"`
	ExperimentID string             `json:"experiment_id,omitempty"`
	ShardIndex   *int               `json:"shard_index,omitempty"`
	Stream       string             `json:"stream"` // "stdout" | "stderr" | "colab"
	Text         string             `json:"text"`
	RichOutput   *KernelRichOutput  `json:"rich_output,omitempty"`
	Timestamp    time.Time          `json:"timestamp"`
}

// WSEvent is the unified envelope broadcast over `/ws/events`.
type WSEvent struct {
	Type      WSEventType `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Payload   any         `json:"payload"`
}

// DaemonHealthStatus powers the bottom Observatory Status Footer and `/api/v1/health`.
type DaemonHealthStatus struct {
	Healthy               bool      `json:"healthy"`
	Version               string    `json:"version"`
	DeploymentMode        string    `json:"deployment_mode"`
	AuthRequired          bool      `json:"auth_required"`
	PublicURL             string    `json:"public_url,omitempty"`
	UptimeSec             int64     `json:"uptime_sec"`
	KeepaliveLoopSec      int       `json:"keepalive_loop_sec"`
	ResourcePollSec       int       `json:"resource_poll_sec"`
	AntiZombieArmed       bool      `json:"anti_zombie_armed"`
	ContributorFirewallOn bool      `json:"contributor_firewall_on"`
	ActiveSessions        int       `json:"active_sessions"`
	ActiveExperiments     int       `json:"active_experiments"`
	ConnectedWSClients    int       `json:"connected_ws_clients"`
	Timestamp             time.Time `json:"timestamp"`
}
