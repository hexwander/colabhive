package models

import (
	"strings"
	"time"
)

// Official bundled Cloud SDK / colab-cli OAuth2 client configuration and endpoints.
// Note: client_id 764086051850-... is allowlisted by Google for REMOTE_REDIRECT_URI.
const (
	OfficialColabOAuthClientID     = "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com"
	OfficialColabOAuthClientSecret = "d-FL95Q19q7MQmFpd7hHD0Ty"
	OfficialGoogleAuthURI          = "https://accounts.google.com/o/oauth2/auth"
	OfficialGoogleTokenURI         = "https://oauth2.googleapis.com/token"
	OfficialGoogleTokenInfoURI     = "https://oauth2.googleapis.com/tokeninfo"
	OfficialGoogleRevokeURI        = "https://oauth2.googleapis.com/revoke"
	OfficialGoogleUserInfoURI      = "https://www.googleapis.com/oauth2/v2/userinfo"
	OfficialRemoteRedirectURI      = "https://sdk.cloud.google.com/applicationdefaultauthcode.html"

	ColabTunnelDomain    = "https://colab.research.google.com"
	ColabGAPIDomain      = "https://colab.pa.googleapis.com"
	ColabPublicAPIDomain = "https://colaboratory.googleapis.com"
	ColabXSSIPrefix      = ")]}'\n"
)

// Standard and least-privilege Contributor OAuth2 scope sets.
var (
	// ContributorOAuthScopes requests strictly Colaboratory + identity (ZERO Google Drive or GCP billing scopes).
	ContributorOAuthScopes = []string{
		"openid",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/colaboratory",
	}

	// OwnerOAuthScopes matches google-colab-cli v0.8.0 full owner scopes.
	OwnerOAuthScopes = []string{
		"openid",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/colaboratory",
		"https://www.googleapis.com/auth/drive.file",
	}
)

// SubscriptionTier represents a Google Colab subscription tier.
type SubscriptionTier string

const (
	TierFree    SubscriptionTier = "FREE"     // SUBSCRIPTION_TIER_NONE
	TierPAYG    SubscriptionTier = "PAYG"     // Pay-As-You-Go CU balance on non-Pro
	TierPro     SubscriptionTier = "PRO"      // SUBSCRIPTION_TIER_PRO
	TierProPlus SubscriptionTier = "PRO_PLUS" // SUBSCRIPTION_TIER_PRO_PLUS
	TierAIUltra SubscriptionTier = "AI_ULTRA" // Google One AI Ultra / High-Cap tier
)

// TierStandardCycleCU returns the official monthly cycle Compute Unit allocation for a subscription tier.
func TierStandardCycleCU(tier SubscriptionTier) float64 {
	switch tier {
	case TierAIUltra:
		return 2500.0
	case TierProPlus:
		return 500.0
	case TierPro:
		return 100.0
	case TierPAYG:
		return 100.0
	case TierFree:
		return 0
	default:
		return 0
	}
}

// NormalizeSubscriptionTier normalizes API/UI/wire subscription tier strings into canonical SubscriptionTier values.
func NormalizeSubscriptionTier(raw SubscriptionTier) (SubscriptionTier, bool) {
	switch strings.ToUpper(strings.TrimSpace(string(raw))) {
	case "AI_ULTRA", "SUBSCRIPTION_TIER_AI_ULTRA", "ULTRA":
		return TierAIUltra, true
	case "PRO_PLUS", "SUBSCRIPTION_TIER_PRO_PLUS", "PRO+":
		return TierProPlus, true
	case "PRO", "SUBSCRIPTION_TIER_PRO":
		return TierPro, true
	case "PAYG", "PAY_AS_YOU_GO":
		return TierPAYG, true
	case "FREE", "SUBSCRIPTION_TIER_NONE", "NONE":
		return TierFree, true
	default:
		return "", false
	}
}

// AccountHealth represents the 2D scheduling health state of a pooled Google account.
type AccountHealth string

const (
	HealthHealthy              AccountHealth = "HEALTHY"
	HealthActiveWorking        AccountHealth = "ACTIVE_WORKING"
	HealthMaxConcurrency       AccountHealth = "MAX_CONCURRENCY"
	HealthCooldownRateLimit    AccountHealth = "COOLDOWN_RATE_LIMIT"
	HealthQuotaLowFreeOnly     AccountHealth = "QUOTA_LOW_FREE_ONLY"
	HealthPausedCapReached     AccountHealth = "PAUSED_CAP_REACHED"
	HealthPausedScheduleWindow AccountHealth = "PAUSED_SCHEDULE_WINDOW"
	HealthPausedManual         AccountHealth = "PAUSED_MANUAL"
	HealthAuthExpired          AccountHealth = "AUTH_EXPIRED"
	HealthRevokedByUser        AccountHealth = "REVOKED_BY_USER"
	HealthQuarantinedDenylist  AccountHealth = "QUARANTINED_DENYLIST"
	HealthProxyError           AccountHealth = "PROXY_ERROR"
)

// IsSchedulable reports whether the account is generally eligible for new VM assignments.
func (h AccountHealth) IsSchedulable() bool {
	return h == HealthHealthy || h == HealthActiveWorking || h == HealthQuotaLowFreeOnly
}

// GPUEntitlementStatus tracks per-account, per-accelerator eligibility & HTTP 400 cooldowns.
type GPUEntitlementStatus string

const (
	EntitlementAvailable      GPUEntitlementStatus = "AVAILABLE"
	EntitlementTierRestricted GPUEntitlementStatus = "TIER_RESTRICTED" // HTTP 400 entitlement block
	EntitlementIneligible     GPUEntitlementStatus = "INELIGIBLE"      // Reported ineligible by v1/user-info
	EntitlementCooldown       GPUEntitlementStatus = "COOLDOWN"        // Transient 503 / capacity cooldown
	EntitlementUnknown        GPUEntitlementStatus = "UNKNOWN"
)

// GPUEntitlement stores an account's capability and calibrated burn rate for one accelerator.
type GPUEntitlement struct {
	Accelerator        AcceleratorType       `json:"accelerator"`
	Status             GPUEntitlementStatus  `json:"status"`
	Reason             string                `json:"reason,omitempty"`
	CooldownUntil      *time.Time            `json:"cooldown_until,omitempty"`
	CalibratedBurnRate float64               `json:"calibrated_burn_rate,omitempty"`
	CalibrationSource  RateCalibrationSource `json:"calibration_source,omitempty"`
	VerifiedAt         *time.Time            `json:"verified_at,omitempty"`
}

// ProxyProtocol enumerates supported per-account egress proxy protocols.
type ProxyProtocol string

const (
	ProxyDirect ProxyProtocol = "direct"
	ProxyHTTP   ProxyProtocol = "http"
	ProxyHTTPS  ProxyProtocol = "https"
	ProxySOCKS5 ProxyProtocol = "socks5"
)

// ProxyConfig defines a per-account egress proxy for strict IP/ASN isolation.
type ProxyConfig struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Protocol      ProxyProtocol `json:"protocol"`
	URL           string        `json:"url,omitempty"` // Stripped/masked in public views
	MaskedURL     string        `json:"masked_url"`
	Region        string        `json:"region,omitempty"`
	ExitIP        string        `json:"exit_ip,omitempty"`
	LatencyMs     int64         `json:"latency_ms"`
	Healthy       bool          `json:"healthy"`
	BoundAccounts int           `json:"bound_accounts"`
	LastCheckedAt *time.Time    `json:"last_checked_at,omitempty"`
	LastError     string        `json:"last_error,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
}

// ProxyProbeResult is returned by POST /api/v1/proxies/test.
type ProxyProbeResult struct {
	Healthy    bool      `json:"healthy"`
	ExitIP     string    `json:"exit_ip,omitempty"`
	Region     string    `json:"region,omitempty"`
	LatencyMs  int64     `json:"latency_ms"`
	StatusCode int       `json:"status_code,omitempty"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

// ScheduleWindow defines contributor time-of-day / day-of-week scheduling boundaries.
type ScheduleWindow struct {
	Enabled    bool   `json:"enabled"`
	Timezone   string `json:"timezone"`     // e.g. "Asia/Shanghai" or "UTC"
	DaysOfWeek []int  `json:"days_of_week"` // 0=Sunday .. 6=Saturday
	StartHour  string `json:"start_hour"`   // "22:00"
	EndHour    string `json:"end_hour"`     // "09:00"
}

// ContributorGuardrails defines non-negotiable compute limits and security firewalls
// enforced by ColabHive's scheduler and WebSocket proxy on contributed accounts.
type ContributorGuardrails struct {
	MinReserveCU          float64           `json:"min_reserve_cu"`
	MaxDonatedCUTotal     float64           `json:"max_donated_cu_total,omitempty"` // 0 = unlimited above floor
	MaxDonatedCUDaily     float64           `json:"max_donated_cu_daily,omitempty"` // 0 = unlimited above floor
	DonatedCUUsedTotal    float64           `json:"donated_cu_used_total"`
	DonatedCUUsedToday    float64           `json:"donated_cu_used_today"`
	DailyUsageDate        string            `json:"daily_usage_date,omitempty"`
	AllowPaidCUBurn       bool              `json:"allow_paid_cu_burn"`
	AllowedGPUs           []AcceleratorType `json:"allowed_gpus"`
	MaxConcurrentVMs      int               `json:"max_concurrent_vms"`
	ScheduleWindow        *ScheduleWindow   `json:"schedule_window,omitempty"`
	BlockDrivePropagation bool              `json:"block_drive_propagation"`
	MaskEmailInDashboard  bool              `json:"mask_email_in_dashboard"`
	Paused                bool              `json:"paused"`
	ExpiresAt             *time.Time        `json:"expires_at,omitempty"`
	ReceiptToken          string            `json:"receipt_token,omitempty"` // Only populated on initial issuance
}

// FreeQuotaInfo represents Google Colab's free dynamic milli-CCU quota window.
type FreeQuotaInfo struct {
	RemainingMilliCCU int64      `json:"remaining_milli_ccu"`
	RemainingFreeCU   float64    `json:"remaining_free_cu"`
	NextRefillAt      *time.Time `json:"next_refill_at,omitempty"`
}

// GPUHourEquivalence translates raw Compute Units into calibrated GPU runtime hours.
type GPUHourEquivalence struct {
	Accelerator       AcceleratorType       `json:"accelerator"`
	DisplayName       string                `json:"display_name"`
	ShortLabel        string                `json:"short_label"`
	VRAMGB            int                   `json:"vram_gb"`
	BurnRateCUHr      float64               `json:"burn_rate_cu_hr"`
	CalibrationSource RateCalibrationSource `json:"calibration_source"`
	AvailableHours    float64               `json:"available_hours"`
	SchedulableHours  float64               `json:"schedulable_hours"`
	EligibleAccounts  int                   `json:"eligible_accounts"`
	MaxParallelSlots  int                   `json:"max_parallel_slots"`
	FreeWindowsReady  int                   `json:"free_windows_ready"`
	Locked            bool                  `json:"locked"`
}

// Account represents a pooled Google Colab account in ColabHive.
// Secret OAuth tokens use json:"-" so JSON serialization NEVER leaks credentials.
type Account struct {
	ID                     string                                  `json:"id"`
	Email                  string                                  `json:"email"`
	DisplayName            string                                  `json:"display_name,omitempty"`
	AvatarURL              string                                  `json:"avatar_url,omitempty"`
	Alias                  string                                  `json:"alias,omitempty"`
	Notes                  string                                  `json:"notes,omitempty"`
	SubscriptionTier       SubscriptionTier                        `json:"subscription_tier"`
	Health                 AccountHealth                           `json:"health"`
	HealthReason           string                                  `json:"health_reason,omitempty"`
	CooldownUntil          *time.Time                              `json:"cooldown_until,omitempty"`
	Enabled                bool                                    `json:"enabled"`
	Priority               int                                     `json:"priority"`
	Groups                 []string                                `json:"groups"`
	Tags                   []string                                `json:"tags"`
	ProxyID                string                                  `json:"proxy_id,omitempty"`
	Proxy                  *ProxyConfig                            `json:"proxy,omitempty"`
	MaxConcurrentVMs         int                                     `json:"max_concurrent_vms"`
	ActiveAssignments        int                                     `json:"active_assignments"`
	UpstreamAssignmentsCount int                                     `json:"upstream_assignments_count,omitempty"`
	UpstreamUnadoptedCount   int                                     `json:"upstream_unadopted_count,omitempty"`
	UpstreamBurnRateCUHr     float64                                 `json:"upstream_burn_rate_cu_hr,omitempty"`
	StickyAgentCount         int                                     `json:"sticky_agent_count"`
	PaidCUBalance          float64                                 `json:"paid_cu_balance"`
	ReferenceCycleCU       float64                                 `json:"reference_cycle_cu"`
	CustomCycleCU          bool                                    `json:"custom_cycle_cu,omitempty"`
	CustomSubscriptionTier bool                                    `json:"custom_subscription_tier,omitempty"`
	SchedulableCU          float64                                 `json:"schedulable_cu"`
	LiveBurnRateCUHr       float64                                 `json:"live_burn_rate_cu_hr"`
	CycleResetsAt          *time.Time                              `json:"cycle_resets_at,omitempty"`
	EarliestBatchExpiresAt *time.Time                              `json:"earliest_batch_expires_at,omitempty"`
	ExpiringSoonCU         float64                                 `json:"expiring_soon_cu"`
	FreeQuota              *FreeQuotaInfo                          `json:"free_quota,omitempty"`
	GPUEntitlements        map[AcceleratorType]GPUEntitlement      `json:"gpu_entitlements"`
	GPUEquivalence         map[AcceleratorType]GPUHourEquivalence  `json:"gpu_equivalence,omitempty"`
	PublicAPISupported     bool                                    `json:"public_api_supported"`
	TunnelAPISupported     bool                                    `json:"tunnel_api_supported"`
	IsContributor          bool                                    `json:"is_contributor"`
	InviteCode             string                                  `json:"invite_code,omitempty"`
	Guardrails             ContributorGuardrails                   `json:"guardrails"`
	Scopes                 []string                                `json:"scopes,omitempty"`
	ActiveSessionSummary   string                                  `json:"active_session_summary,omitempty"`
	TokenExpiresAt         *time.Time                              `json:"token_expires_at,omitempty"`
	LastPolledAt           *time.Time                              `json:"last_polled_at,omitempty"`
	CreatedAt              time.Time                               `json:"created_at"`
	UpdatedAt              time.Time                               `json:"updated_at"`

	// Vault-encrypted credentials — NEVER serialized to API responses.
	EncryptedRefreshToken string `json:"-"`
	EncryptedAccessToken  string `json:"-"`
	OAuthClientID         string `json:"-"`
	OAuthClientSecret     string `json:"-"`
	ReceiptTokenHash      string `json:"-"`
}

// OAuthAuthMode enumerates supported Google OAuth2 onboarding flows.
type OAuthAuthMode string

const (
	OAuthModeRemoteCopyPaste OAuthAuthMode = "remote_copy_paste" // Mode A: sdk.cloud.google.com/applicationdefaultauthcode.html
	OAuthModeWebCallback     OAuthAuthMode = "web_callback"      // Mode B: Direct HTTPS callback
	OAuthModeCLIDonate       OAuthAuthMode = "cli_donate"        // Mode C: colabhive-sdk donate
)

// OAuthStartRequest initiates an owner or contributor OAuth2 PKCE session.
type OAuthStartRequest struct {
	Alias         string                 `json:"alias,omitempty"`
	Notes         string                 `json:"notes,omitempty"`
	Groups        []string               `json:"groups,omitempty"`
	Tags          []string               `json:"tags,omitempty"`
	ProxyID       string                 `json:"proxy_id,omitempty"`
	ProxyURL      string                 `json:"proxy_url,omitempty"`
	Mode          OAuthAuthMode          `json:"mode,omitempty"`
	IsContributor bool                   `json:"is_contributor,omitempty"`
	InviteCode    string                 `json:"invite_code,omitempty"`
	Guardrails    *ContributorGuardrails `json:"guardrails,omitempty"`
}

// OAuthPendingSession holds server-side PKCE state during a 10-minute OAuth handshake.
type OAuthPendingSession struct {
	State         string                 `json:"state"`
	CodeVerifier  string                 `json:"-"`
	CodeChallenge string                 `json:"code_challenge"`
	AuthURL       string                 `json:"auth_url"`
	RedirectURI   string                 `json:"redirect_uri"`
	Mode          OAuthAuthMode          `json:"mode"`
	Scopes        []string               `json:"scopes"`
	Alias         string                 `json:"alias,omitempty"`
	Notes         string                 `json:"notes,omitempty"`
	Groups        []string               `json:"groups,omitempty"`
	Tags          []string               `json:"tags,omitempty"`
	ProxyID       string                 `json:"proxy_id,omitempty"`
	ProxyURL      string                 `json:"proxy_url,omitempty"`
	IsContributor bool                   `json:"is_contributor"`
	InviteCode    string                 `json:"invite_code,omitempty"`
	Guardrails    *ContributorGuardrails `json:"guardrails,omitempty"`
	ExpiresAt     time.Time              `json:"expires_at"`
	CreatedAt     time.Time              `json:"created_at"`
}

// ContributorInvite represents a shareable Guest Portal invitation (`/contribute/:code`).
type ContributorInvite struct {
	Code              string                `json:"code"` // ch_inv_...
	PoolTitle         string                `json:"pool_title"`
	MaintainerName    string                `json:"maintainer_name"`
	WelcomeNote       string                `json:"welcome_note,omitempty"`
	TargetGroups      []string              `json:"target_groups"`
	DefaultProxyID    string                `json:"default_proxy_id,omitempty"`
	DefaultGuardrails ContributorGuardrails `json:"default_guardrails"`
	MaxUses           int                   `json:"max_uses"` // 0 = unlimited
	UsedCount         int                   `json:"used_count"`
	SupportedModes    []OAuthAuthMode       `json:"supported_modes"`
	RequiredScopes    []string              `json:"required_scopes"`
	ShareURL          string                `json:"share_url,omitempty"`
	Revoked           bool                  `json:"revoked"`
	ExpiresAt         *time.Time            `json:"expires_at,omitempty"`
	CreatedAt         time.Time             `json:"created_at"`
}

// CreateInviteRequest creates a new ContributorInvite.
type CreateInviteRequest struct {
	PoolTitle         string                `json:"pool_title"`
	MaintainerName    string                `json:"maintainer_name"`
	WelcomeNote       string                `json:"welcome_note,omitempty"`
	TargetGroups      []string              `json:"target_groups,omitempty"`
	DefaultProxyID    string                `json:"default_proxy_id,omitempty"`
	DefaultGuardrails ContributorGuardrails `json:"default_guardrails"`
	MaxUses           int                   `json:"max_uses,omitempty"`
	TTLHours          int                   `json:"ttl_hours,omitempty"`
}

// ContributionReceipt is returned once upon completing the Guest Contributor OAuth flow.
type ContributionReceipt struct {
	ReceiptToken string                   `json:"receipt_token"` // ch_rcpt_...
	ManageURL    string                   `json:"manage_url"`
	Status       ContributorReceiptStatus `json:"status"`
}

// ContributorActiveVM exposes safe hardware/uptime telemetry to a contributor without leaking user code.
type ContributorActiveVM struct {
	SessionID    string          `json:"session_id"`
	Accelerator  AcceleratorType `json:"accelerator"`
	BurnRateCUHr float64         `json:"burn_rate_cu_hr"`
	UptimeSec    int64           `json:"uptime_sec"`
	StartedAt    time.Time       `json:"started_at"`
}

// ContributorReceiptStatus powers the self-service `/contribute/manage/:receipt_token` portal.
type ContributorReceiptStatus struct {
	AccountID        string                `json:"account_id"`
	MaskedEmail      string                `json:"masked_email"`
	SubscriptionTier SubscriptionTier      `json:"subscription_tier"`
	Health           AccountHealth         `json:"health"`
	PaidCUBalance    float64               `json:"paid_cu_balance"`
	SchedulableCU    float64               `json:"schedulable_cu"`
	LiveBurnRateCUHr float64               `json:"live_burn_rate_cu_hr"`
	Guardrails       ContributorGuardrails `json:"guardrails"`
	ActiveVMs        []ContributorActiveVM `json:"active_vms"`
	LastPolledAt     *time.Time            `json:"last_polled_at,omitempty"`
}

// ContributorGuardrailsPatch allows contributors to update safety caps or pause/resume in real time.
type ContributorGuardrailsPatch struct {
	MinReserveCU         *float64          `json:"min_reserve_cu,omitempty"`
	MaxDonatedCUTotal    *float64          `json:"max_donated_cu_total,omitempty"`
	MaxDonatedCUDaily    *float64          `json:"max_donated_cu_daily,omitempty"`
	AllowPaidCUBurn      *bool             `json:"allow_paid_cu_burn,omitempty"`
	AllowedGPUs          []AcceleratorType `json:"allowed_gpus,omitempty"`
	MaxConcurrentVMs     *int              `json:"max_concurrent_vms,omitempty"`
	ScheduleWindow       *ScheduleWindow   `json:"schedule_window,omitempty"`
	MaskEmailInDashboard *bool             `json:"mask_email_in_dashboard,omitempty"`
	Paused               *bool             `json:"paused,omitempty"`
}

// QuotaSummary powers the Observatory Hero Telemetry Strip & Cloud GPU Equivalence Matrix.
type QuotaSummary struct {
	AccountsTotal          int                                    `json:"accounts_total"`
	AccountsHealthy        int                                    `json:"accounts_healthy"`
	AccountsWorking        int                                    `json:"accounts_working"`
	AccountsContributor    int                                    `json:"accounts_contributor"`
	TotalAvailableCU       float64                                `json:"total_available_cu"`
	SchedulableCU          float64                                `json:"schedulable_cu"`
	EstimatedValueUSD      float64                                `json:"estimated_value_usd"`
	ActiveBurnRateCUHr     float64                                `json:"active_burn_rate_cu_hr"`
	PoolDepletionETAHours  *float64                               `json:"pool_depletion_eta_hours,omitempty"`
	EarliestBatchExpiryCU  float64                                `json:"earliest_batch_expiry_cu"`
	EarliestBatchExpiresAt *time.Time                             `json:"earliest_batch_expires_at,omitempty"`
	ActiveVMs              int                                    `json:"active_vms"`
	TotalVMSlots           int                                    `json:"total_vm_slots"`
	TotalOnlineVRAMGB      int                                    `json:"total_online_vram_gb"`
	FreeT4WindowsReady     int                                    `json:"free_t4_windows_ready"`
	SavedIdleWasteCU       float64                                `json:"saved_idle_waste_cu"`
	AutoTeardownsCount     int                                    `json:"auto_teardowns_count"`
	ZombieAbortsCount      int                                    `json:"zombie_aborts_count"`
	GPUEquivalence         map[AcceleratorType]GPUHourEquivalence `json:"gpu_equivalence"`
	UpdatedAt              time.Time                              `json:"updated_at"`
}

// DiscoveredLocalCredential represents a read-only local/WSL2 credential candidate found on disk.
type DiscoveredLocalCredential struct {
	ID           string   `json:"id"`
	SourceType   string   `json:"source_type"` // "keyring_plaintext" | "colab_token_json" | "gcloud_adc"
	SourcePath   string   `json:"source_path"`
	Environment  string   `json:"environment"` // "host" | "wsl2"
	EmailPreview string   `json:"email_preview,omitempty"`
	TokenPreview string   `json:"token_preview"`
	Scopes       []string `json:"scopes,omitempty"`
}

// ImportConflictStrategy governs duplicate email resolution when importing `.chvault` / JSON bundles.
type ImportConflictStrategy string

const (
	ConflictSkip        ImportConflictStrategy = "skip"
	ConflictUpdate      ImportConflictStrategy = "update"
	ConflictMergeGroups ImportConflictStrategy = "merge_groups"
)

// PortableAccountOAuth holds portable OAuth credentials inside a CredentialBundle.
type PortableAccountOAuth struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	TokenURI     string   `json:"token_uri"`
	RefreshToken string   `json:"refresh_token"`
	AccessToken  string   `json:"access_token,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// PortableAccountEntry represents one account inside `colabhive.credential_bundle` v1.
type PortableAccountEntry struct {
	Email            string                `json:"email,omitempty"`
	Alias            string                `json:"alias,omitempty"`
	SubscriptionTier SubscriptionTier      `json:"subscription_tier,omitempty"`
	Groups           []string              `json:"groups,omitempty"`
	Tags             []string              `json:"tags,omitempty"`
	ProxyURL         string                `json:"proxy_url,omitempty"`
	OAuth            PortableAccountOAuth  `json:"oauth"`
	Guardrails       ContributorGuardrails `json:"guardrails"`
}

// VaultKDFParams describes PBKDF2-HMAC-SHA256 parameters for encrypted `.chvault` bundles.
type VaultKDFParams struct {
	Algorithm  string `json:"alg"`
	Iterations int    `json:"iterations"`
	SaltB64    string `json:"salt_b64"`
}

// VaultCipherPayload holds AES-256-GCM encrypted bytes inside `.chvault` bundles.
type VaultCipherPayload struct {
	Algorithm     string `json:"alg"`
	NonceB64      string `json:"nonce_b64"`
	CiphertextB64 string `json:"ciphertext_b64"`
}

// CredentialBundle implements `colabhive.credential_bundle` v1 (`.chvault` & plain admin JSON).
type CredentialBundle struct {
	Schema     string                 `json:"schema"` // "colabhive.credential_bundle"
	Version    string                 `json:"version"`
	ExportedAt time.Time              `json:"exported_at"`
	Encrypted  bool                   `json:"encrypted"`
	Accounts   []PortableAccountEntry `json:"accounts,omitempty"`
	Proxies    []ProxyConfig          `json:"proxies,omitempty"`
	KDF        *VaultKDFParams        `json:"kdf,omitempty"`
	Cipher     *VaultCipherPayload    `json:"cipher,omitempty"`
}

// CredentialImportRequest handles multi-format credential imports (`keyringrc.cfg`, ADC, JSON, `.chvault`).
type CredentialImportRequest struct {
	Format           string                 `json:"format"` // "discovered" | "keyring_ini" | "adc_json" | "raw_json" | "chvault"
	DiscoveredIDs    []string               `json:"discovered_ids,omitempty"`
	RawPayload       string                 `json:"raw_payload,omitempty"`
	Passphrase       string                 `json:"passphrase,omitempty"`
	DefaultGroups    []string               `json:"default_groups,omitempty"`
	DefaultProxyID   string                 `json:"default_proxy_id,omitempty"`
	ConflictStrategy ImportConflictStrategy `json:"conflict_strategy,omitempty"`
}

// CredentialExportRequest configures `.chvault` or plain JSON pool exports.
type CredentialExportRequest struct {
	AccountIDs     []string `json:"account_ids,omitempty"` // Empty = export all non-contributor accounts
	IncludeProxies bool     `json:"include_proxies"`
	Encrypt        bool     `json:"encrypt"`
	Passphrase     string   `json:"passphrase,omitempty"`
}

// AccountPatch updates mutable administrative fields on a pooled account.
type AccountPatch struct {
	Alias            *string                `json:"alias,omitempty"`
	Notes            *string                `json:"notes,omitempty"`
	Enabled          *bool                  `json:"enabled,omitempty"`
	Priority         *int                   `json:"priority,omitempty"`
	Groups           []string               `json:"groups,omitempty"`
	Tags             []string               `json:"tags,omitempty"`
	ProxyID          *string                `json:"proxy_id,omitempty"`
	MaxConcurrentVMs *int                   `json:"max_concurrent_vms,omitempty"`
	SubscriptionTier *SubscriptionTier      `json:"subscription_tier,omitempty"`
	ReferenceCycleCU *float64               `json:"reference_cycle_cu,omitempty"`
	Guardrails       *ContributorGuardrails `json:"guardrails,omitempty"`
}

// ============================================================================
// Account & System Diagnostic Self-Test DTOs
// ============================================================================

// VerifyStepStatus indicates the outcome of a single diagnostic check step.
type VerifyStepStatus string

const (
	VerifyStepOK      VerifyStepStatus = "ok"
	VerifyStepError   VerifyStepStatus = "error"
	VerifyStepSkipped VerifyStepStatus = "skipped"
)

// VerifyStepResult captures timing and human-readable telemetry for one diagnostic check.
type VerifyStepResult struct {
	ID         string           `json:"id"`
	TitleZH    string           `json:"title_zh"`
	TitleEN    string           `json:"title_en"`
	Status     VerifyStepStatus `json:"status"`
	DurationMs int64            `json:"duration_ms"`
	Summary    string           `json:"summary"`
	Detail     string           `json:"detail,omitempty"`
}

// AccountVerifyRequest configures POST /api/v1/accounts/{id}/verify.
type AccountVerifyRequest struct {
	RunKernelSmoke bool            `json:"run_kernel_smoke,omitempty"`
	Accelerator    AcceleratorType `json:"accelerator,omitempty"`
}

// AccountVerifyResult is returned by POST /api/v1/accounts/{id}/verify.
type AccountVerifyResult struct {
	AccountID       string             `json:"account_id"`
	Email           string             `json:"email"`
	OverallOK       bool               `json:"overall_ok"`
	CheckedAt       time.Time          `json:"checked_at"`
	TotalDurationMs int64              `json:"total_duration_ms"`
	Steps           []VerifyStepResult `json:"steps"`
	Account         *Account           `json:"account,omitempty"`
	KernelStdout    string             `json:"kernel_stdout,omitempty"`
}

// SystemSelfTestResult is returned by POST /api/v1/system/selftest.
type SystemSelfTestResult struct {
	OverallOK            bool               `json:"overall_ok"`
	Mode                 string             `json:"mode"` // "demo" | "production"
	CheckedAt            time.Time          `json:"checked_at"`
	Steps                []VerifyStepResult `json:"steps"`
	DiscoveredLocalCount int                `json:"discovered_local_count"`
}
