package models

import (
	"context"
	"time"
)

// EventHub broadcasts real-time telemetry, terminal chunks, and trace events to WebSocket subscribers.
type EventHub interface {
	Publish(event WSEvent)
	Subscribe(ctx context.Context) (<-chan WSEvent, func())
	ClientCount() int
}

// SavedWasteStats tracks CUs saved via automatic teardowns and anti-zombie aborts.
type SavedWasteStats struct {
	SavedIdleWasteCU   float64 `json:"saved_idle_waste_cu"`
	AutoTeardownsCount int     `json:"auto_teardowns_count"`
	ZombieAbortsCount  int     `json:"zombie_aborts_count"`
}

// Store defines the persistence contract (implemented over pure-Go modernc.org/sqlite).
type Store interface {
	// Accounts
	SaveAccount(ctx context.Context, acc *Account) error
	GetAccount(ctx context.Context, id string) (*Account, error)
	GetAccountByEmail(ctx context.Context, email string) (*Account, error)
	GetAccountByReceiptHash(ctx context.Context, receiptHash string) (*Account, error)
	ListAccounts(ctx context.Context) ([]*Account, error)
	DeleteAccount(ctx context.Context, id string) error

	// Proxies
	SaveProxy(ctx context.Context, proxy *ProxyConfig) error
	GetProxy(ctx context.Context, id string) (*ProxyConfig, error)
	ListProxies(ctx context.Context) ([]*ProxyConfig, error)
	DeleteProxy(ctx context.Context, id string) error

	// Contributor Invites
	SaveInvite(ctx context.Context, invite *ContributorInvite) error
	GetInvite(ctx context.Context, code string) (*ContributorInvite, error)
	ListInvites(ctx context.Context) ([]*ContributorInvite, error)
	DeleteInvite(ctx context.Context, code string) error

	// Runtime Sessions
	SaveSession(ctx context.Context, sess *RuntimeSession) error
	GetSession(ctx context.Context, id string) (*RuntimeSession, error)
	GetSessionByName(ctx context.Context, name string) (*RuntimeSession, error)
	ListSessions(ctx context.Context) ([]*RuntimeSession, error)
	DeleteSession(ctx context.Context, id string) error

	// Sharded Experiments
	SaveExperiment(ctx context.Context, exp *Experiment) error
	GetExperiment(ctx context.Context, id string) (*Experiment, error)
	ListExperiments(ctx context.Context) ([]*Experiment, error)
	DeleteExperiment(ctx context.Context, id string) error

	// Scoped Agent API Keys (`ch_sk_...`)
	SaveAPIKey(ctx context.Context, key *AgentAPIKey) error
	GetAPIKey(ctx context.Context, id string) (*AgentAPIKey, error)
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*AgentAPIKey, error)
	ListAPIKeys(ctx context.Context) ([]*AgentAPIKey, error)
	DeleteAPIKey(ctx context.Context, id string) error

	// Router Policy & Call Traces
	GetRouterPolicy(ctx context.Context) (*RouterPolicy, error)
	SaveRouterPolicy(ctx context.Context, policy *RouterPolicy) error
	AppendToolTrace(ctx context.Context, trace *ToolTraceEvent) error
	ListToolTraces(ctx context.Context, limit int) ([]ToolTraceEvent, error)

	// Empirical Burn Calibration & Saved Waste Ledger
	RecordBurnSample(ctx context.Context, acc AcceleratorType, shape MachineShape, cuPerHour float64) error
	GetCalibratedBurnRate(ctx context.Context, acc AcceleratorType, shape MachineShape) (float64, bool, error)
	AddSavedWaste(ctx context.Context, savedCU float64, isZombieAbort bool) error
	GetSavedWasteStats(ctx context.Context) (SavedWasteStats, error)

	Close() error
}

// AccountPoolService manages Google OAuth2 onboarding, AES-256-GCM vault encryption,
// multi-format credential portability, per-account proxy isolation, `ccu-info` / `v1/user-info`
// quota telemetry, and the External Contributor Guest Portal & safety guardrails.
type AccountPoolService interface {
	// Owner OAuth2 PKCE Wizard
	StartOAuth(ctx context.Context, req OAuthStartRequest) (*OAuthPendingSession, error)
	CompleteOAuth(ctx context.Context, state string, rawCodeOrURL string) (*Account, error)

	// Account Pool & Quota Observatory
	ListAccounts(ctx context.Context) ([]*Account, error)
	GetAccount(ctx context.Context, idOrEmail string) (*Account, error)
	UpdateAccount(ctx context.Context, id string, patch AccountPatch) (*Account, error)
	DeleteAccount(ctx context.Context, id string) error
	RefreshAccountQuota(ctx context.Context, id string) (*Account, error)
	RefreshAllQuotas(ctx context.Context) (*QuotaSummary, error)
	GetQuotaSummary(ctx context.Context) (*QuotaSummary, error)

	// Scheduler & Runtime Integration Hooks (Zero-Leak Credential Access)
	AcquireAccessToken(ctx context.Context, accountID string) (accessToken string, proxyURL string, err error)
	CanScheduleAccount(acc *Account, requested AcceleratorType) (allowed bool, reason string)
	MarkGPUEntitlement(ctx context.Context, accountID string, gpu AcceleratorType, status GPUEntitlementStatus, reason string, cooldown time.Duration) error
	MarkAccountHealth(ctx context.Context, accountID string, health AccountHealth, reason string, cooldown time.Duration) error
	RecordAccountCUBurn(ctx context.Context, accountID string, deltaCU float64) error

	// Proxy Pool Management
	ListProxies(ctx context.Context) ([]*ProxyConfig, error)
	SaveProxy(ctx context.Context, cfg *ProxyConfig) (*ProxyConfig, error)
	DeleteProxy(ctx context.Context, id string) error
	TestProxy(ctx context.Context, rawURL string) (*ProxyProbeResult, error)

	// Credential Portability (`keyringrc.cfg`, `gcloud` ADC, JSON, `.chvault`)
	ScanLocalCredentials(ctx context.Context) ([]DiscoveredLocalCredential, error)
	ImportCredentials(ctx context.Context, req CredentialImportRequest) ([]*Account, error)
	ExportVaultBundle(ctx context.Context, req CredentialExportRequest) (*CredentialBundle, error)

	// External Contributor Guest Portal (`/contribute/:code` & `/contribute/manage/:receipt_token`)
	CreateInvite(ctx context.Context, req CreateInviteRequest) (*ContributorInvite, error)
	ListInvites(ctx context.Context) ([]*ContributorInvite, error)
	RevokeInvite(ctx context.Context, code string) error
	GetPublicInvite(ctx context.Context, code string) (*ContributorInvite, error)
	StartContribution(ctx context.Context, inviteCode string, guardrails ContributorGuardrails, mode OAuthAuthMode) (*OAuthPendingSession, error)
	CompleteContribution(ctx context.Context, inviteCode string, state string, rawCodeOrURL string) (*ContributionReceipt, error)
	GetContributionByReceipt(ctx context.Context, receiptToken string) (*ContributorReceiptStatus, error)
	UpdateContributionGuardrails(ctx context.Context, receiptToken string, patch ContributorGuardrailsPatch) (*ContributorReceiptStatus, error)
	RevokeContribution(ctx context.Context, receiptToken string) error
}

// RuntimeEngine manages Google Colab VM provisioning (Public API + Tunnel API fallback),
// Jupyter REST + ZMQ-over-WebSocket execution, 15m idle-reaper keepalive, `/proc/{pid}/stat`
// anti-zombie watchdogs, `/api/colab/resources` telemetry, and parallel sharded experiments.
type RuntimeEngine interface {
	// Session Lifecycle
	CreateSession(ctx context.Context, req SessionAllocRequest) (*RuntimeSession, error)
	GetSession(ctx context.Context, idOrName string) (*RuntimeSession, error)
	ListSessions(ctx context.Context, filter SessionFilter) ([]*RuntimeSession, error)
	SyncRemoteAssignments(ctx context.Context) (*SessionSyncResult, error)
	StopSession(ctx context.Context, idOrName string) error
	RestartKernel(ctx context.Context, idOrName string) error
	InterruptKernel(ctx context.Context, idOrName string) error
	HotRestartWorker(ctx context.Context, idOrName string) error
	GetColabWebURL(ctx context.Context, idOrName string) (string, error)

	// Execution & Virtual Notebook Cells
	ExecuteCode(ctx context.Context, req KernelExecRequest) (*KernelExecResult, error)
	GetCells(ctx context.Context, sessionID string, includeOutputs bool) ([]NotebookCell, error)
	MutateCell(ctx context.Context, sessionID string, req CellMutationRequest) (*NotebookCell, *KernelExecResult, error)

	// Remote File System & Package Installer (`uv pip install --system`)
	ListFiles(ctx context.Context, sessionID string, remotePath string) ([]RemoteFileEntry, error)
	UploadFile(ctx context.Context, sessionID string, remotePath string, data []byte) error
	DownloadFile(ctx context.Context, sessionID string, remotePath string) ([]byte, error)
	DeleteFile(ctx context.Context, sessionID string, remotePath string) error
	InstallPackages(ctx context.Context, sessionID string, packages []string, requirementsTxt string) (*KernelExecResult, error)

	// Multi-Account Sharded Experiments
	LaunchShardedExperiment(ctx context.Context, req ExperimentLaunchRequest) (*Experiment, error)
	GetExperiment(ctx context.Context, id string) (*Experiment, error)
	ListExperiments(ctx context.Context) ([]*Experiment, error)
	CollectAndTeardownExperiment(ctx context.Context, id string, forceKill bool) (*ExperimentTeardownResult, error)
}

// RouterService governs smart account/GPU scheduling (`waste-zero`, `max-balance`,
// `sticky-session`, `round-robin`), scoped `ch_sk_...` API keys, call trace logging,
// and surgical `cc-switch`-style 1-click Agent MCP configuration injection.
type RouterService interface {
	// Scheduling & Sticky Kernel Affinity
	SelectAccountAndTier(ctx context.Context, req SessionAllocRequest) (*Account, AcceleratorType, error)
	ResolveStickySession(ctx context.Context, stickyKey string) (*RuntimeSession, bool)
	BindStickySession(ctx context.Context, binding RouteBinding) error
	UnbindStickySession(ctx context.Context, stickyKey string) error
	ListStickyBindings(ctx context.Context) ([]RouteBinding, error)

	// Policy & Trace Inspector
	GetPolicy(ctx context.Context) (*RouterPolicy, error)
	UpdatePolicy(ctx context.Context, policy RouterPolicy) (*RouterPolicy, error)
	RecordTrace(ctx context.Context, event ToolTraceEvent) error
	ListTraces(ctx context.Context, limit int) ([]ToolTraceEvent, error)

	// Scoped Agent API Keys (`ch_sk_...`)
	CreateAPIKey(ctx context.Context, req CreateAPIKeyRequest) (*AgentAPIKey, error)
	AuthenticateAPIKey(ctx context.Context, rawKey string) (*AgentAPIKey, error)
	ListAPIKeys(ctx context.Context) ([]*AgentAPIKey, error)
	RevokeAPIKey(ctx context.Context, id string) error

	// 1-Click Agent Config Injector (Antigravity, Claude Code, Claude Desktop, Codex, Gemini CLI, Cursor, Windsurf, OpenCode)
	ListAgentClientStatuses(ctx context.Context, serverBaseURL string, rawAPIKey string) ([]AgentClientConfigStatus, error)
	InjectAgentConfig(ctx context.Context, req AgentInjectRequest) (*AgentClientConfigStatus, error)
	RemoveAgentConfig(ctx context.Context, clientID AgentClientID) (*AgentClientConfigStatus, error)
}
