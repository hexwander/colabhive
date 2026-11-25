// Package pool implements models.AccountPoolService, managing Google OAuth2 PKCE S256
// onboarding, AES-256-GCM vault credential storage, dual-endpoint Colab quota polling,
// multi-format credential portability, per-account proxy isolation, and the External
// Contributor Guest Portal & scheduler safety guardrails.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/proxy"
	"github.com/colabhive/colabhive/internal/vault"
)

const (
	oauthPendingTTL    = 10 * time.Minute
	tokenRefreshMargin = 5 * time.Minute
)

// LocalScanCandidate describes a single candidate file path for ScanLocalCredentials.
type LocalScanCandidate struct {
	SourceType   string // "keyring_plaintext" | "colab_token_json" | "gcloud_adc"
	SourcePath   string
	KeyringRC    string // Optional companion keyringrc.cfg path
	Environment  string // "host" | "wsl2"
}

// Config configures pool.Service endpoints, master encryption secret, and optional test overrides.
type Config struct {
	MasterSecret   string
	StrictVaultKey bool
	PublicBaseURL  string

	// Optional custom GCP Web OAuth client for Mode B 1-click HTTPS callbacks.
	CustomOAuthClientID     string
	CustomOAuthClientSecret string
	WebCallbackRedirectURI  string

	// Optional Google & Colab endpoint overrides (default to official Google production URLs).
	GoogleAuthURI       string
	GoogleTokenURI      string
	GoogleTokenInfoURI  string
	GoogleRevokeURI     string
	GoogleUserInfoURI   string
	ColabTunnelDomain   string
	ColabGAPIDomain     string
	ColabPublicDomain   string
	ProxyProbeTargetURL string

	// Optional hermetic candidate list for ScanLocalCredentials tests.
	// Note: ScanLocalCredentials is NEVER invoked automatically on Service startup.
	LocalScanRoots []LocalScanCandidate

	NowFunc func() time.Time
}

func (c Config) withDefaults() Config {
	if c.PublicBaseURL == "" {
		c.PublicBaseURL = "http://127.0.0.1:7860"
	}
	c.PublicBaseURL = strings.TrimRight(c.PublicBaseURL, "/")
	if c.GoogleAuthURI == "" {
		c.GoogleAuthURI = models.OfficialGoogleAuthURI
	}
	if c.GoogleTokenURI == "" {
		c.GoogleTokenURI = models.OfficialGoogleTokenURI
	}
	if c.GoogleTokenInfoURI == "" {
		c.GoogleTokenInfoURI = models.OfficialGoogleTokenInfoURI
	}
	if c.GoogleRevokeURI == "" {
		c.GoogleRevokeURI = models.OfficialGoogleRevokeURI
	}
	if c.GoogleUserInfoURI == "" {
		c.GoogleUserInfoURI = models.OfficialGoogleUserInfoURI
	}
	if c.ColabTunnelDomain == "" {
		c.ColabTunnelDomain = models.ColabTunnelDomain
	}
	c.ColabTunnelDomain = strings.TrimRight(c.ColabTunnelDomain, "/")
	if c.ColabGAPIDomain == "" {
		c.ColabGAPIDomain = models.ColabGAPIDomain
	}
	c.ColabGAPIDomain = strings.TrimRight(c.ColabGAPIDomain, "/")
	if c.ColabPublicDomain == "" {
		c.ColabPublicDomain = models.ColabPublicAPIDomain
	}
	c.ColabPublicDomain = strings.TrimRight(c.ColabPublicDomain, "/")
	if c.NowFunc == nil {
		c.NowFunc = func() time.Time { return time.Now().UTC() }
	}
	return c
}

type discoveredRecord struct {
	meta        models.DiscoveredLocalCredential
	sourcePath  string
	sourceType  string
	resolvedEm  string
	resolvedNm  string
	accessToken string
	expiry      *time.Time
}

type cachedScanIdentity struct {
	email        string
	displayName  string
	accessToken  string
	expiry       *time.Time
	invalidGrant bool
	cachedAt     time.Time
}

// Service implements models.AccountPoolService.
type Service struct {
	st        models.Store
	hub       models.EventHub
	cfg       Config
	cipher    *vault.Cipher
	transport *proxy.TransportFactory

	oauthMu      sync.Mutex
	pendingOAuth map[string]*models.OAuthPendingSession

	emailUpsertMu sync.Mutex

	acctMu       sync.Mutex
	acctLocks    map[string]*sync.Mutex
	revokingAccs map[string]int

	drainerMu      sync.RWMutex
	sessionDrainer func(ctx context.Context, accountID string) error

	scanMu            sync.RWMutex
	discovered        map[string]discoveredRecord
	scanIdentityCache map[string]cachedScanIdentity
}

var _ models.AccountPoolService = (*Service)(nil)

// NewService constructs a new models.AccountPoolService.
// Strictly adheres to zero-touch startup safety: never reads local user credentials on initialization.
func NewService(st models.Store, hub models.EventHub, cfg Config) models.AccountPoolService {
	return NewConcreteService(st, hub, cfg)
}

// NewConcreteService constructs a concrete *Service instance.
func NewConcreteService(st models.Store, hub models.EventHub, cfg Config) *Service {
	norm := cfg.withDefaults()
	tf := proxy.NewTransportFactory()
	if norm.ProxyProbeTargetURL != "" {
		tf.SetProbeURL(norm.ProxyProbeTargetURL)
	}
	var vc *vault.Cipher
	if norm.StrictVaultKey {
		vc = vault.NewStrictCipher(norm.MasterSecret)
	} else {
		vc = vault.NewCipher(norm.MasterSecret)
	}
	return &Service{
		st:           st,
		hub:          hub,
		cfg:          norm,
		cipher:       vc,
		transport:    tf,
		pendingOAuth: make(map[string]*models.OAuthPendingSession),
		acctLocks:         make(map[string]*sync.Mutex),
		discovered:        make(map[string]discoveredRecord),
		scanIdentityCache: make(map[string]cachedScanIdentity),
	}
}

func (s *Service) now() time.Time {
	return s.cfg.NowFunc().UTC()
}

func (s *Service) publish(eventType models.WSEventType, payload any) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(models.WSEvent{
		Type:      eventType,
		Timestamp: s.now(),
		Payload:   payload,
	})
}

func (s *Service) accountMutex(accountID string) *sync.Mutex {
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	m, ok := s.acctLocks[accountID]
	if !ok {
		m = &sync.Mutex{}
		s.acctLocks[accountID] = m
	}
	return m
}

// SetSessionDrainer registers a callback (typically runtime.Engine.StopManagedSessionsForAccount)
// invoked before revoking contributor OAuth grants or deleting pooled accounts so active VMs are unassigned upstream.
func (s *Service) SetSessionDrainer(fn func(ctx context.Context, accountID string) error) {
	s.drainerMu.Lock()
	s.sessionDrainer = fn
	s.drainerMu.Unlock()
}

// EnsureSessionDrainer registers fn only when no sessionDrainer has been configured yet.
func (s *Service) EnsureSessionDrainer(fn func(ctx context.Context, accountID string) error) {
	s.drainerMu.Lock()
	if s.sessionDrainer == nil {
		s.sessionDrainer = fn
	}
	s.drainerMu.Unlock()
}

func (s *Service) getSessionDrainer() func(ctx context.Context, accountID string) error {
	s.drainerMu.RLock()
	fn := s.sessionDrainer
	s.drainerMu.RUnlock()
	return fn
}

func (s *Service) resolveAccountProxyURL(ctx context.Context, acc *models.Account) string {
	if acc == nil || acc.ProxyID == "" {
		return ""
	}
	p, err := s.st.GetProxy(ctx, acc.ProxyID)
	if err != nil || p == nil {
		return ""
	}
	return p.URL
}

func (s *Service) ensureProxyForURL(ctx context.Context, rawURL string, label string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || strings.EqualFold(trimmed, "direct") {
		return "", nil
	}
	proto, _, err := proxy.ParseProtocol(trimmed)
	if err != nil {
		return "", err
	}
	existing, err := s.st.ListProxies(ctx)
	if err == nil {
		for _, p := range existing {
			if p.URL == trimmed {
				return p.ID, nil
			}
		}
	}
	if label == "" {
		label = "Sticky Proxy (" + proxy.MaskProxyURL(trimmed) + ")"
	}
	cfg := &models.ProxyConfig{
		ID:        models.NewPrefixedID("prx_"),
		Name:      label,
		Protocol:  proto,
		URL:       trimmed,
		MaskedURL: proxy.MaskProxyURL(trimmed),
		Healthy:   true,
		CreatedAt: s.now(),
	}
	if err := s.st.SaveProxy(ctx, cfg); err != nil {
		return "", err
	}
	return cfg.ID, nil
}

// ============================================================================
// Account CRUD & Scheduler State Hooks
// ============================================================================

// ListAccounts returns all pooled accounts with updated SchedulableCU & GPU equivalence.
func (s *Service) ListAccounts(ctx context.Context) ([]*models.Account, error) {
	accts, err := s.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for i, acc := range accts {
		prevRef := acc.ReferenceCycleCU
		prevTier := acc.SubscriptionTier
		prevDate := acc.Guardrails.DailyUsageDate
		prevUsedToday := acc.Guardrails.DonatedCUUsedToday
		s.recomputeAccountDerivedFields(ctx, acc)
		if acc.ReferenceCycleCU != prevRef || acc.SubscriptionTier != prevTier ||
			acc.Guardrails.DailyUsageDate != prevDate || acc.Guardrails.DonatedCUUsedToday != prevUsedToday {
			if synced := s.persistDerivedRolloverLocked(ctx, acc.ID); synced != nil {
				accts[i] = synced
			}
		}
	}
	return accts, nil
}

// GetAccount retrieves a pooled account by ID or case-insensitive email address.
func (s *Service) GetAccount(ctx context.Context, idOrEmail string) (*models.Account, error) {
	key := strings.TrimSpace(idOrEmail)
	if key == "" {
		return nil, errors.New("pool: empty account id or email")
	}
	acc, err := s.st.GetAccount(ctx, key)
	if err != nil && strings.Contains(key, "@") {
		acc, err = s.st.GetAccountByEmail(ctx, key)
	}
	if err != nil {
		return nil, err
	}
	prevRef := acc.ReferenceCycleCU
	prevTier := acc.SubscriptionTier
	prevDate := acc.Guardrails.DailyUsageDate
	prevUsedToday := acc.Guardrails.DonatedCUUsedToday
	s.recomputeAccountDerivedFields(ctx, acc)
	if acc.ReferenceCycleCU != prevRef || acc.SubscriptionTier != prevTier ||
		acc.Guardrails.DailyUsageDate != prevDate || acc.Guardrails.DonatedCUUsedToday != prevUsedToday {
		if synced := s.persistDerivedRolloverLocked(ctx, acc.ID); synced != nil {
			return synced, nil
		}
	}
	return acc, nil
}

func (s *Service) persistDerivedRolloverLocked(ctx context.Context, accountID string) *models.Account {
	mu := s.accountMutex(accountID)
	mu.Lock()
	defer mu.Unlock()

	fresh, err := s.st.GetAccount(ctx, accountID)
	if err != nil || fresh == nil {
		return nil
	}
	s.recomputeAccountDerivedFields(ctx, fresh)
	_ = s.st.SaveAccount(ctx, fresh)
	return fresh
}

// UpdateAccount applies administrative patches to an existing pooled account.
func (s *Service) UpdateAccount(ctx context.Context, id string, patch models.AccountPatch) (*models.Account, error) {
	mu := s.accountMutex(id)
	mu.Lock()

	acc, err := s.st.GetAccount(ctx, id)
	if err != nil {
		mu.Unlock()
		return nil, err
	}

	if patch.Alias != nil {
		acc.Alias = strings.TrimSpace(*patch.Alias)
	}
	if patch.Notes != nil {
		acc.Notes = strings.TrimSpace(*patch.Notes)
	}
	if patch.Enabled != nil {
		acc.Enabled = *patch.Enabled
		if !acc.Enabled && acc.Health == models.HealthHealthy {
			acc.Health = models.HealthPausedManual
			acc.HealthReason = "Manually disabled by administrator"
		} else if acc.Enabled && acc.Health == models.HealthPausedManual && !acc.Guardrails.Paused {
			acc.Health = models.HealthHealthy
			acc.HealthReason = ""
		}
	}
	if patch.Priority != nil {
		acc.Priority = *patch.Priority
	}
	if patch.Groups != nil {
		acc.Groups = uniqueStrings(patch.Groups)
	}
	if patch.Tags != nil {
		acc.Tags = uniqueStrings(patch.Tags)
	}
	if patch.ProxyID != nil {
		acc.ProxyID = strings.TrimSpace(*patch.ProxyID)
	}
	if patch.SubscriptionTier != nil {
		normTier, ok := models.NormalizeSubscriptionTier(*patch.SubscriptionTier)
		if !ok {
			mu.Unlock()
			return nil, fmt.Errorf("pool: invalid subscription_tier %q", *patch.SubscriptionTier)
		}
		prevTier := acc.SubscriptionTier
		acc.SubscriptionTier = normTier
		acc.CustomSubscriptionTier = true
		if prevTier != normTier {
			acc.GPUEntitlements = mergeEntitlementsForManualTier(acc.GPUEntitlements, normTier)
			if !acc.IsContributor {
				acc.MaxConcurrentVMs = tierDefaultMaxVMs(normTier)
				if acc.ActiveAssignments > acc.MaxConcurrentVMs {
					acc.MaxConcurrentVMs = acc.ActiveAssignments
				}
			}
			if patch.ReferenceCycleCU == nil && !acc.CustomCycleCU {
				std := TierStandardCycleCU(normTier)
				if acc.PaidCUBalance > std {
					std = acc.PaidCUBalance
				}
				acc.ReferenceCycleCU = std
			}
		}
	}
	if patch.ReferenceCycleCU != nil {
		if *patch.ReferenceCycleCU > 0 {
			acc.ReferenceCycleCU = round2(*patch.ReferenceCycleCU)
			acc.CustomCycleCU = true
		} else {
			acc.CustomCycleCU = false
			acc.ReferenceCycleCU = 0
		}
	}
	if patch.MaxConcurrentVMs != nil && *patch.MaxConcurrentVMs > 0 {
		acc.MaxConcurrentVMs = *patch.MaxConcurrentVMs
	}
	if patch.Guardrails != nil {
		g := *patch.Guardrails
		if acc.IsContributor {
			g.BlockDrivePropagation = true
		}
		g.DonatedCUUsedTotal = acc.Guardrails.DonatedCUUsedTotal
		g.DonatedCUUsedToday = acc.Guardrails.DonatedCUUsedToday
		g.DailyUsageDate = acc.Guardrails.DailyUsageDate
		g.ReceiptToken = ""
		acc.Guardrails = g
	}

	s.recomputeAccountDerivedFields(ctx, acc)
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		mu.Unlock()
		return nil, err
	}
	mu.Unlock()

	s.publish(models.WSEventAccountUpdated, acc)
	if summary, sErr := s.GetQuotaSummary(ctx); sErr == nil {
		s.publish(models.WSEventQuotaUpdated, summary)
	}
	return acc, nil
}

func mergeEntitlementsForManualTier(existing map[models.AcceleratorType]models.GPUEntitlement, tier models.SubscriptionTier) map[models.AcceleratorType]models.GPUEntitlement {
	defaults := defaultEntitlementsForTier(tier)
	if len(existing) == 0 {
		return defaults
	}
	for gpu, defEnt := range defaults {
		if cur, ok := existing[gpu]; ok {
			cur.Status = defEnt.Status
			cur.Reason = ""
			cur.CooldownUntil = nil
			defaults[gpu] = cur
		}
	}
	return defaults
}

// DeleteAccount drains active managed sessions on Google (best-effort) and deletes the account from the pool.
func (s *Service) DeleteAccount(ctx context.Context, id string) error {
	targetID := strings.TrimSpace(id)
	if targetID == "" {
		return errors.New("pool: empty account id")
	}

	mu := s.accountMutex(targetID)
	mu.Lock()
	existing, err := s.st.GetAccount(ctx, targetID)
	if err != nil {
		mu.Unlock()
		return err
	}
	if existing.Enabled {
		existing.Enabled = false
		_ = s.st.SaveAccount(ctx, existing)
	}
	mu.Unlock()

	if drainer := s.getSessionDrainer(); drainer != nil {
		if dErr := drainer(ctx, targetID); dErr != nil {
			return fmt.Errorf("pool: failed to drain active VMs before deleting account: %w", dErr)
		}
	}

	mu.Lock()
	delErr := s.st.DeleteAccount(ctx, targetID)
	if delErr == nil {
		now := s.now()
		if sessions, sErr := s.st.ListSessions(ctx); sErr == nil {
			for _, sess := range sessions {
				if sess != nil && sess.AccountID == targetID && isActiveSessionStatus(sess.Status) {
					sess.Status = models.SessionStopped
					sess.StatusMessage = "Terminated due to account deletion"
					sess.StoppedAt = &now
					sess.ProxyToken = ""
					_ = s.st.SaveSession(ctx, sess)
					s.publish(models.WSEventSessionUpdated, sess)
				}
			}
		}
	}
	mu.Unlock()

	if delErr != nil {
		return delErr
	}
	if summary, err := s.GetQuotaSummary(ctx); err == nil {
		s.publish(models.WSEventQuotaUpdated, summary)
	}
	return nil
}

// MarkGPUEntitlement updates an account's per-accelerator eligibility & optional cooldown (e.g. HTTP 400/503).
func (s *Service) MarkGPUEntitlement(
	ctx context.Context,
	accountID string,
	gpu models.AcceleratorType,
	status models.GPUEntitlementStatus,
	reason string,
	cooldown time.Duration,
) error {
	mu := s.accountMutex(accountID)
	mu.Lock()
	defer mu.Unlock()

	acc, err := s.st.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if acc.GPUEntitlements == nil {
		acc.GPUEntitlements = make(map[models.AcceleratorType]models.GPUEntitlement)
	}
	now := s.now()
	ent := acc.GPUEntitlements[gpu]
	ent.Accelerator = gpu
	ent.Status = status
	ent.Reason = reason
	ent.VerifiedAt = &now
	if cooldown > 0 {
		until := now.Add(cooldown)
		ent.CooldownUntil = &until
	} else {
		ent.CooldownUntil = nil
	}
	acc.GPUEntitlements[gpu] = ent

	if err := s.st.SaveAccount(ctx, acc); err != nil {
		return err
	}
	s.publish(models.WSEventAccountUpdated, acc)
	return nil
}

// MarkAccountHealth updates an account's scheduling health state and optional cooldown window.
func (s *Service) MarkAccountHealth(
	ctx context.Context,
	accountID string,
	health models.AccountHealth,
	reason string,
	cooldown time.Duration,
) error {
	mu := s.accountMutex(accountID)
	mu.Lock()
	defer mu.Unlock()

	acc, err := s.st.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	now := s.now()
	acc.Health = health
	acc.HealthReason = reason
	if cooldown > 0 {
		until := now.Add(cooldown)
		acc.CooldownUntil = &until
	} else {
		acc.CooldownUntil = nil
	}
	s.recomputeAccountDerivedFields(ctx, acc)
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		return err
	}
	s.publish(models.WSEventAccountUpdated, acc)
	return nil
}

// RecordAccountCUBurn deducts deltaCU from an account's cached balance and updates contributor ledgers.
func (s *Service) RecordAccountCUBurn(ctx context.Context, accountID string, deltaCU float64) error {
	if deltaCU <= 0 {
		return nil
	}
	mu := s.accountMutex(accountID)
	mu.Lock()
	defer mu.Unlock()

	acc, err := s.st.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	acc.PaidCUBalance -= deltaCU
	if acc.PaidCUBalance < 0 {
		acc.PaidCUBalance = 0
	}
	if acc.IsContributor {
		normalizeGuardrailsDailyUsage(&acc.Guardrails, s.now())
		acc.Guardrails.DonatedCUUsedTotal += deltaCU
		acc.Guardrails.DonatedCUUsedToday += deltaCU
	}
	s.recomputeAccountDerivedFields(ctx, acc)
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		return err
	}

	// Emit guardrail preemption alert if contributor account approaches or hits its reserve floor/cap.
	if acc.IsContributor && acc.Health == models.HealthPausedCapReached {
		s.publish(models.WSEventGuardrailPreempt, map[string]any{
			"account_id":     acc.ID,
			"min_reserve_cu": acc.Guardrails.MinReserveCU,
			"paid_cu":        acc.PaidCUBalance,
			"reason":         acc.HealthReason,
		})
	}
	s.publish(models.WSEventAccountUpdated, acc)
	return nil
}

// ReconcileAccountSessionRollup safely updates an account's active session count, burn rate,
// and summary under the per-account mutex without clobbering Google's polled external assignments
// or live heterogeneous burn rates.
func (s *Service) ReconcileAccountSessionRollup(
	ctx context.Context,
	accountID string,
	reconciledUpstreamCount *int,
	now time.Time,
) (*models.Account, error) {
	if s.st == nil || strings.TrimSpace(accountID) == "" {
		return nil, nil
	}
	if now.IsZero() {
		now = s.now()
	}

	mu := s.accountMutex(accountID)
	mu.Lock()

	acc, err := s.st.GetAccount(ctx, accountID)
	if err != nil || acc == nil {
		mu.Unlock()
		return acc, err
	}

	allSessions, err := s.st.ListSessions(ctx)
	if err != nil {
		mu.Unlock()
		return nil, err
	}

	var active []*models.RuntimeSession
	localBurn := 0.0
	postPollManagedBurn := 0.0
	for _, sess := range allSessions {
		if sess == nil || sess.AccountID != accountID {
			continue
		}
		if sess.Status == models.SessionStopped || sess.Status == models.SessionLost || sess.Status == models.SessionZombieAlert {
			continue
		}
		active = append(active, sess)
		localBurn += sess.BurnRateCUHr
		if !sess.AdoptedExternal && (acc.LastPolledAt == nil || sess.CreatedAt.After(*acc.LastPolledAt)) {
			postPollManagedBurn += sess.BurnRateCUHr
		}
	}

	sort.Slice(active, func(i, j int) bool {
		return active[i].CreatedAt.Before(active[j].CreatedAt)
	})

	summaries := make([]string, 0, len(active))
	for i, sess := range active {
		if i >= 3 {
			summaries = append(summaries, fmt.Sprintf("+%d more", len(active)-3))
			break
		}
		summaries = append(summaries, fmt.Sprintf("%s (%s)", sess.Name, sess.Accelerator))
	}

	if reconciledUpstreamCount != nil {
		acc.UpstreamUnadoptedCount = 0
		if *reconciledUpstreamCount == 0 && len(active) == 0 {
			acc.UpstreamAssignmentsCount = 0
			acc.UpstreamBurnRateCUHr = 0
		} else if *reconciledUpstreamCount > acc.UpstreamAssignmentsCount {
			acc.UpstreamAssignmentsCount = *reconciledUpstreamCount
		}
	}

	effectiveActive := len(active) + acc.UpstreamUnadoptedCount
	var effectiveBurn float64
	switch {
	case effectiveActive == 0:
		effectiveBurn = 0
	case acc.UpstreamBurnRateCUHr > 0 && acc.UpstreamAssignmentsCount > 0:
		if effectiveActive == acc.UpstreamAssignmentsCount {
			effectiveBurn = acc.UpstreamBurnRateCUHr
		} else if acc.UpstreamUnadoptedCount > 0 {
			effectiveBurn = acc.UpstreamBurnRateCUHr + localBurn
		} else if effectiveActive > acc.UpstreamAssignmentsCount {
			effectiveBurn = acc.UpstreamBurnRateCUHr + postPollManagedBurn
		} else {
			effectiveBurn = localBurn
		}
	default:
		effectiveBurn = localBurn
	}

	prevActive := acc.ActiveAssignments
	acc.ActiveAssignments = effectiveActive
	acc.LiveBurnRateCUHr = round2(effectiveBurn)
	acc.ActiveSessionSummary = strings.Join(summaries, " + ")
	acc.UpdatedAt = now

	// When a live GPU session on this account was just freed (active count decreased),
	// immediately lift any HTTP 412 / MAX_CONCURRENCY cooldown so the user can schedule
	// into the newly freed slot right away instead of waiting out the 10-minute cooldown.
	if effectiveActive < prevActive && acc.Health == models.HealthMaxConcurrency {
		acc.CooldownUntil = nil
		acc.HealthReason = ""
		if effectiveActive == 0 {
			acc.Health = models.HealthHealthy
		} else {
			acc.Health = models.HealthActiveWorking
		}
	}

	s.recomputeAccountDerivedFields(ctx, acc)
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		mu.Unlock()
		return nil, err
	}
	mu.Unlock()

	s.publish(models.WSEventAccountUpdated, acc)
	return acc, nil
}

// SetAccountStickyCount safely updates Account.StickyAgentCount under the per-account mutex.
func (s *Service) SetAccountStickyCount(ctx context.Context, accountID string, count int) error {
	if s.st == nil || strings.TrimSpace(accountID) == "" {
		return nil
	}
	if count < 0 {
		count = 0
	}
	mu := s.accountMutex(accountID)
	mu.Lock()
	acc, err := s.st.GetAccount(ctx, accountID)
	if err != nil || acc == nil {
		mu.Unlock()
		return err
	}
	if acc.StickyAgentCount == count {
		mu.Unlock()
		return nil
	}
	acc.StickyAgentCount = count
	acc.UpdatedAt = s.now()
	saveErr := s.st.SaveAccount(ctx, acc)
	mu.Unlock()
	if saveErr == nil {
		s.publish(models.WSEventAccountUpdated, acc)
	}
	return saveErr
}

// ============================================================================
// Proxy Pool Management
// ============================================================================

// ListProxies returns all configured egress proxies with MaskedURL populated.
func (s *Service) ListProxies(ctx context.Context) ([]*models.ProxyConfig, error) {
	list, err := s.st.ListProxies(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.MaskedURL == "" {
			p.MaskedURL = proxy.MaskProxyURL(p.URL)
		}
	}
	return list, nil
}

// SaveProxy validates, masks, and persists a ProxyConfig.
func (s *Service) SaveProxy(ctx context.Context, cfg *models.ProxyConfig) (*models.ProxyConfig, error) {
	if cfg == nil {
		return nil, errors.New("pool: nil proxy config")
	}
	proto, _, err := proxy.ParseProtocol(cfg.URL)
	if err != nil {
		return nil, err
	}
	if cfg.ID == "" {
		cfg.ID = models.NewPrefixedID("prx_")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "Proxy " + proxy.MaskProxyURL(cfg.URL)
	}
	cfg.Protocol = proto
	cfg.MaskedURL = proxy.MaskProxyURL(cfg.URL)
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = s.now()
	}
	if cfg.LastCheckedAt == nil {
		cfg.Healthy = true
	}
	if err := s.st.SaveProxy(ctx, cfg); err != nil {
		return nil, err
	}
	return s.st.GetProxy(ctx, cfg.ID)
}

// DeleteProxy removes a proxy configuration and unbinds dependent accounts.
func (s *Service) DeleteProxy(ctx context.Context, id string) error {
	return s.st.DeleteProxy(ctx, id)
}

// TestProxy probes a raw proxy URL, measuring RTT latency and exit IP.
func (s *Service) TestProxy(ctx context.Context, rawURL string) (*models.ProxyProbeResult, error) {
	return s.transport.ProbeProxy(ctx, rawURL)
}

// SeedAccountCredentials encrypts OAuth tokens at rest and saves an Account into SQLite.
func (s *Service) SeedAccountCredentials(
	ctx context.Context,
	acc *models.Account,
	refreshToken string,
	accessToken string,
	expiresIn time.Duration,
) (*models.Account, error) {
	if acc == nil {
		return nil, errors.New("pool: nil account")
	}
	encRef, err := s.cipher.EncryptString(refreshToken)
	if err != nil {
		return nil, err
	}
	encAcc, err := s.cipher.EncryptString(accessToken)
	if err != nil {
		return nil, err
	}
	acc.EncryptedRefreshToken = encRef
	acc.EncryptedAccessToken = encAcc
	if acc.OAuthClientID == "" {
		acc.OAuthClientID = models.OfficialColabOAuthClientID
	}
	if acc.OAuthClientSecret == "" {
		acc.OAuthClientSecret = models.OfficialColabOAuthClientSecret
	}
	if accessToken != "" && expiresIn > 0 {
		exp := s.now().Add(expiresIn)
		acc.TokenExpiresAt = &exp
	}
	s.recomputeAccountDerivedFields(ctx, acc)
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		return nil, err
	}
	return acc, nil
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		t := strings.TrimSpace(it)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}
