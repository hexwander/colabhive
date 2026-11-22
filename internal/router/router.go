// Package router implements ColabHive's smart multi-account GPU scheduler,
// sticky kernel affinity manager, scoped ch_sk_... API key authenticator,
// tool-call trace inspector, and cc-switch-grade surgical 1-click Agent config injector.
package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/colabhive/colabhive/internal/models"
)

var (
	// ErrNoEligibleAccount indicates no pooled account can satisfy the requested compute spec.
	ErrNoEligibleAccount = errors.New("no eligible pooled account available for requested accelerator")
	// ErrInvalidAPIKey indicates an unrecognized, disabled, or expired ch_sk_... key.
	ErrInvalidAPIKey = errors.New("invalid, disabled, or expired API key")
	// ErrAPIKeyBudgetExceeded indicates an Agent API key has exhausted its CU budget or VM cap.
	ErrAPIKeyBudgetExceeded = errors.New("API key compute unit budget or concurrency limit exceeded")
)

var _ models.RouterService = (*Service)(nil)

// Service implements models.RouterService depending strictly on models interfaces.
type Service struct {
	st   models.Store
	pool models.AccountPoolService
	hub  models.EventHub

	mu             sync.RWMutex
	stickyBindings map[string]*models.RouteBinding

	allocMu          sync.Mutex
	pendingByAccount map[string]int
	pendingByAPIKey  map[string]int
	rrSeqByAccount   map[string]uint64
	rrTick           uint64

	keyMu sync.Mutex

	NowFunc func() time.Time

	homeDir string
	appData string
	wslRoot string
	goos    string
}

type stickyCountSyncer interface {
	SetAccountStickyCount(ctx context.Context, accountID string, count int) error
}

// NewService constructs a RouterService using the host's default home and WSL2 directories.
func NewService(st models.Store, pool models.AccountPoolService, hub models.EventHub) models.RouterService {
	home, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	wslRoot := `\\wsl.localhost`
	return NewServiceWithDirs(st, pool, hub, home, appData, wslRoot, runtimeGOOS())
}

// NewServiceWithDirs constructs a RouterService with explicit filesystem roots (ideal for hermetic unit tests).
func NewServiceWithDirs(
	st models.Store,
	pool models.AccountPoolService,
	hub models.EventHub,
	homeDir string,
	appData string,
	wslRoot string,
	goos string,
) *Service {
	return &Service{
		st:               st,
		pool:             pool,
		hub:              hub,
		stickyBindings:   make(map[string]*models.RouteBinding),
		pendingByAccount: make(map[string]int),
		pendingByAPIKey:  make(map[string]int),
		rrSeqByAccount:   make(map[string]uint64),
		homeDir:          homeDir,
		appData:          appData,
		wslRoot:          wslRoot,
		goos:             goos,
	}
}

func (s *Service) nowUTC() time.Time {
	if s.NowFunc != nil {
		return s.NowFunc().UTC()
	}
	return time.Now().UTC()
}

// NormalizeStrategy maps CLI/MCP strategy aliases into canonical models.RoutingStrategy values.
func NormalizeStrategy(raw models.RoutingStrategy, fallback models.RoutingStrategy) models.RoutingStrategy {
	s := strings.ToLower(strings.TrimSpace(string(raw)))
	switch s {
	case "":
		if fallback != "" {
			return fallback
		}
		return models.StrategyWasteZero
	case "waste-zero", "waste_zero", "smart":
		return models.StrategyWasteZero
	case "max-balance", "max_balance", "balance":
		return models.StrategyMaxBalance
	case "sticky-session", "sticky_session", "sticky":
		return models.StrategyStickySession
	case "round-robin", "round_robin", "rr":
		return models.StrategyRoundRobin
	case "free-opportunistic", "free_opportunistic", "free", "cheapest":
		return models.StrategyFreeOpportunistic
	default:
		if fallback != "" {
			return fallback
		}
		return models.StrategyWasteZero
	}
}

// AcquireAccountSlot atomically selects an account and accelerator tier AND reserves an
// in-flight concurrency slot (`pendingByAccount` + `pendingByAPIKey`) so concurrent shard
// allocations or parallel MCP calls never stampede the same account before SaveSession completes.
// Callers MUST invoke the returned release function once session persistence completes or provisioning fails.
func (s *Service) AcquireAccountSlot(
	ctx context.Context,
	req models.SessionAllocRequest,
) (*models.Account, models.AcceleratorType, func(), error) {
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	return s.selectAccountAndTierLocked(ctx, req, true)
}

// SelectAccountAndTier picks the optimal pooled Google account and hardware accelerator
// according to the active routing strategy, contributor guardrails, sticky affinity,
// scoped API key ACLs, in-flight reservations, and Auto-GPU-Tier-Fallback waterfall.
func (s *Service) SelectAccountAndTier(
	ctx context.Context,
	req models.SessionAllocRequest,
) (*models.Account, models.AcceleratorType, error) {
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	acc, tier, _, err := s.selectAccountAndTierLocked(ctx, req, false)
	return acc, tier, err
}

func (s *Service) ensureAllocMapsLocked() {
	if s.pendingByAccount == nil {
		s.pendingByAccount = make(map[string]int)
	}
	if s.pendingByAPIKey == nil {
		s.pendingByAPIKey = make(map[string]int)
	}
	if s.rrSeqByAccount == nil {
		s.rrSeqByAccount = make(map[string]uint64)
	}
}

func (s *Service) effectiveActiveForLocked(acc *models.Account) int {
	if acc == nil {
		return 0
	}
	return acc.ActiveAssignments + s.pendingByAccount[acc.ID]
}

func (s *Service) selectAccountAndTierLocked(
	ctx context.Context,
	req models.SessionAllocRequest,
	reserve bool,
) (*models.Account, models.AcceleratorType, func(), error) {
	s.ensureAllocMapsLocked()
	now := s.nowUTC()
	policy, err := s.GetPolicy(ctx)
	if err != nil || policy == nil {
		def := models.DefaultRouterPolicy()
		policy = &def
	}

	requestedTier := req.Accelerator
	if strings.TrimSpace(string(requestedTier)) == "" {
		requestedTier = models.AcceleratorCPU
	} else {
		norm, normErr := models.NormalizeAccelerator(string(requestedTier))
		if normErr != nil {
			return nil, "", nil, normErr
		}
		requestedTier = norm
	}

	allowedGroups := append([]string(nil), req.AllowedGroups...)
	var allowedGPUs []models.AcceleratorType
	effectiveStrategy := NormalizeStrategy(req.Strategy, policy.DefaultStrategy)

	// Enforce scoped API key permissions, CU budgets, and VM concurrency caps (including in-flight slots) if bound.
	if req.APIKeyID != "" && s.st != nil {
		key, keyErr := s.st.GetAPIKey(ctx, req.APIKeyID)
		if keyErr != nil || key == nil {
			return nil, "", nil, fmt.Errorf("%w: key %q not found", ErrInvalidAPIKey, req.APIKeyID)
		}
		if normalizeAPIKeyDailyUsage(key, now) {
			s.rolloverAPIKeyDailyUsageSafe(ctx, key.ID, now)
		}
		if err := validateAPIKeyLimits(key, now); err != nil {
			return nil, "", nil, err
		}
		if key.MaxConcurrentVMs > 0 {
			activeForKey := s.countActiveSessionsForAPIKey(ctx, key.ID) + s.pendingByAPIKey[key.ID]
			if activeForKey >= key.MaxConcurrentVMs {
				return nil, "", nil, fmt.Errorf(
					"%w: key %q has %d/%d active VMs",
					ErrAPIKeyBudgetExceeded, key.KeyPrefix, activeForKey, key.MaxConcurrentVMs,
				)
			}
		}
		if req.Strategy == "" && key.DefaultStrategy != "" {
			effectiveStrategy = NormalizeStrategy(key.DefaultStrategy, policy.DefaultStrategy)
		}
		if len(key.AllowedGroups) > 0 {
			allowedGroups = intersectOrCopyGroups(allowedGroups, key.AllowedGroups)
			if len(allowedGroups) == 0 {
				return nil, "", nil, fmt.Errorf(
					"%w: requested groups %v are not permitted by API key allowed_groups %v",
					ErrAPIKeyBudgetExceeded, req.AllowedGroups, key.AllowedGroups,
				)
			}
		}
		if len(key.AllowedGPUs) > 0 {
			allowedGPUs = append([]models.AcceleratorType(nil), key.AllowedGPUs...)
		}
	}

	// Build ordered candidate hardware waterfall.
	autoFallback := req.AutoFallback && policy.AutoFallbackEnabled
	if len(req.FallbackChain) > 0 && req.AutoFallback {
		autoFallback = true
	}
	chain := req.FallbackChain
	if len(chain) == 0 {
		if req.RequireLargeVRAM {
			chain = policy.LargeVRAMFallbackChain
		} else {
			chain = policy.FallbackChain
		}
	}
	candidateTiers := models.FallbackCandidatesFrom(requestedTier, autoFallback, chain)
	if autoFallback && len(req.FallbackChain) == 0 && !req.RequireLargeVRAM &&
		len(candidateTiers) == 1 && !containsAccelerator(chain, requestedTier) &&
		containsAccelerator(policy.LargeVRAMFallbackChain, requestedTier) {
		candidateTiers = models.FallbackCandidatesFrom(requestedTier, autoFallback, policy.LargeVRAMFallbackChain)
	}
	if len(allowedGPUs) > 0 {
		filtered := make([]models.AcceleratorType, 0, len(candidateTiers))
		for _, t := range candidateTiers {
			if containsAccelerator(allowedGPUs, t) {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			return nil, "", nil, fmt.Errorf(
				"%w: requested accelerator %s is not permitted by API key allowed_gpus %v",
				ErrAPIKeyBudgetExceeded, requestedTier, allowedGPUs,
			)
		}
		candidateTiers = filtered
	}

	accounts, err := s.loadAccounts(ctx)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to list pooled accounts: %w", err)
	}
	if len(accounts) == 0 {
		return nil, "", nil, fmt.Errorf("%w: account pool is empty", ErrNoEligibleAccount)
	}

	excludedAccs := make(map[string]bool, len(req.ExcludedAccountIDs))
	for _, ex := range req.ExcludedAccountIDs {
		trimmed := strings.TrimSpace(ex)
		if trimmed != "" {
			excludedAccs[trimmed] = true
			excludedAccs[strings.ToLower(trimmed)] = true
		}
	}
	excludedPairs := make(map[string]bool, len(req.ExcludedAccountTiers))
	for _, pair := range req.ExcludedAccountTiers {
		trimmed := strings.TrimSpace(pair)
		if idx := strings.LastIndex(trimmed, ":"); idx > 0 {
			accPart := strings.TrimSpace(trimmed[:idx])
			tierPart := strings.ToUpper(strings.TrimSpace(trimmed[idx+1:]))
			if normT, nErr := models.NormalizeAccelerator(tierPart); nErr == nil {
				tierPart = string(normT)
			}
			excludedPairs[accPart+":"+tierPart] = true
		}
	}

	// Filter accounts by administrative enablement, explicit selector, exclusions, and group ACLs.
	baseAccounts := make([]*models.Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil || !acc.Enabled {
			continue
		}
		if excludedAccs[acc.ID] || (acc.Email != "" && excludedAccs[strings.ToLower(acc.Email)]) {
			continue
		}
		if !matchesAccountSelector(acc, req.AccountSelector) {
			continue
		}
		if len(allowedGroups) > 0 && !hasAnyGroup(acc.Groups, allowedGroups) {
			continue
		}
		baseAccounts = append(baseAccounts, acc)
	}
	if len(baseAccounts) == 0 {
		return nil, "", nil, fmt.Errorf(
			"%w: zero enabled accounts match selector=%q groups=%v excluded=%v",
			ErrNoEligibleAccount, req.AccountSelector, allowedGroups, req.ExcludedAccountIDs,
		)
	}

	// Check sticky account affinity ONLY when caller strategy is default ("") or explicitly sticky-session,
	// and never when the pinned account has been excluded due to an upstream error.
	var pinnedAccountID string
	callerExplicitNonSticky := strings.TrimSpace(string(req.Strategy)) != "" &&
		NormalizeStrategy(req.Strategy, "") != models.StrategyStickySession
	if req.StickyKey != "" && !callerExplicitNonSticky &&
		(effectiveStrategy == models.StrategyStickySession || policy.StickySessionEnabled) {
		candPin := s.resolveStickyAccountIDForKey(ctx, req.StickyKey, req.APIKeyID, policy.StickyIdleTTLSec, now)
		if candPin != "" && !excludedAccs[candPin] {
			pinnedAccountID = candPin
		}
	}

	makeReservation := func(chosen *models.Account) func() {
		if !reserve || chosen == nil {
			return func() {}
		}
		accID := chosen.ID
		keyID := req.APIKeyID
		s.pendingByAccount[accID]++
		if keyID != "" {
			s.pendingByAPIKey[keyID]++
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				s.allocMu.Lock()
				if s.pendingByAccount[accID] <= 1 {
					delete(s.pendingByAccount, accID)
				} else {
					s.pendingByAccount[accID]--
				}
				if keyID != "" {
					if s.pendingByAPIKey[keyID] <= 1 {
						delete(s.pendingByAPIKey, keyID)
					} else {
						s.pendingByAPIKey[keyID]--
					}
				}
				s.allocMu.Unlock()
			})
		}
	}

	rejectionReasons := make([]string, 0, 6)
	for _, tier := range candidateTiers {
		eligible := make([]*models.Account, 0, len(baseAccounts))
		for idx, acc := range baseAccounts {
			candLabel := acc.Email
			if req.APIKeyID != "" {
				candLabel = fmt.Sprintf("pool-slot-%d", idx+1)
			}
			if excludedPairs[acc.ID+":"+string(tier)] {
				if len(rejectionReasons) < 6 {
					rejectionReasons = append(rejectionReasons, fmt.Sprintf("%s(%s): excluded after failed attempt", candLabel, tier))
				}
				continue
			}
			ok, reason := s.canScheduleAccountForTierLocked(acc, tier, now)
			if !ok {
				if len(rejectionReasons) < 6 && reason != "" {
					rejectionReasons = append(rejectionReasons, fmt.Sprintf("%s(%s): %s", candLabel, tier, reason))
				}
				continue
			}
			eligible = append(eligible, acc)
		}
		if len(eligible) == 0 {
			continue
		}

		// Sticky affinity hit: reuse pinned account if still healthy, eligible, and not tier-excluded.
		if pinnedAccountID != "" && !excludedPairs[pinnedAccountID+":"+string(tier)] {
			for _, acc := range eligible {
				if acc.ID == pinnedAccountID {
					return acc, tier, makeReservation(acc), nil
				}
			}
		}

		chosen := s.pickByStrategyLocked(eligible, tier, effectiveStrategy, now)
		if chosen != nil {
			return chosen, tier, makeReservation(chosen), nil
		}
	}

	detail := strings.Join(rejectionReasons, "; ")
	if detail == "" {
		detail = "all candidate tiers exhausted"
	}
	return nil, "", nil, fmt.Errorf("%w (%s)", ErrNoEligibleAccount, detail)
}

func (s *Service) loadAccounts(ctx context.Context) ([]*models.Account, error) {
	if s.pool != nil {
		return s.pool.ListAccounts(ctx)
	}
	if s.st != nil {
		return s.st.ListAccounts(ctx)
	}
	return nil, nil
}

func (s *Service) canScheduleAccountForTierLocked(
	acc *models.Account,
	tier models.AcceleratorType,
	now time.Time,
) (bool, string) {
	if !acc.Enabled {
		return false, "account disabled"
	}
	effectiveActive := s.effectiveActiveForLocked(acc)
	if !acc.Health.IsSchedulable() {
		// Allow expired transient cooldowns to be re-evaluated if pool hasn't refreshed yet.
		if !((acc.Health == models.HealthCooldownRateLimit || acc.Health == models.HealthMaxConcurrency) &&
			acc.CooldownUntil != nil && !now.Before(*acc.CooldownUntil)) {
			return false, fmt.Sprintf("health=%s", acc.Health)
		}
	}
	if acc.CooldownUntil != nil && now.Before(*acc.CooldownUntil) {
		return false, fmt.Sprintf("account in cooldown until %s", acc.CooldownUntil.Format(time.RFC3339))
	}
	if acc.MaxConcurrentVMs > 0 && effectiveActive >= acc.MaxConcurrentVMs {
		return false, fmt.Sprintf("max_concurrent_vms reached (%d/%d)", effectiveActive, acc.MaxConcurrentVMs)
	}

	// Check 2D per-accelerator entitlement matrix.
	if ent, ok := acc.GPUEntitlements[tier]; ok {
		switch ent.Status {
		case models.EntitlementTierRestricted, models.EntitlementIneligible:
			if ent.CooldownUntil == nil || now.Before(*ent.CooldownUntil) {
				return false, fmt.Sprintf("gpu %s %s", tier, ent.Status)
			}
		case models.EntitlementCooldown:
			if ent.CooldownUntil != nil && now.Before(*ent.CooldownUntil) {
				return false, fmt.Sprintf("gpu %s cooldown", tier)
			}
		}
	}

	spec, hasSpec := models.GetHardwareSpec(tier)
	if !hasSpec {
		return false, fmt.Errorf("unknown accelerator %s", tier).Error()
	}

	// Enforce hard Contributor Guardrails.
	if acc.IsContributor {
		g := acc.Guardrails
		if g.Paused {
			return false, "contributor account paused"
		}
		if g.ExpiresAt != nil && now.After(*g.ExpiresAt) {
			return false, "contribution grant expired"
		}
		if g.MaxConcurrentVMs > 0 && effectiveActive >= g.MaxConcurrentVMs {
			return false, "contributor max_concurrent_vms reached"
		}
		if len(g.AllowedGPUs) > 0 && !containsAccelerator(g.AllowedGPUs, tier) {
			return false, fmt.Sprintf("gpu %s not in contributor allowed_gpus", tier)
		}
		if g.ScheduleWindow != nil && g.ScheduleWindow.Enabled {
			if !withinScheduleWindow(g.ScheduleWindow, now) {
				return false, "outside contributor active schedule window"
			}
		}
		freeReady := isFreeWindowReady(acc, spec)
		if !g.AllowPaidCUBurn {
			if !freeReady && spec.DefaultBurnRateCUHr > 0 {
				return false, "contributor forbids paid CU burn"
			}
		} else if !freeReady || acc.PaidCUBalance > 0 {
			minRunway := math.Max(2.0, spec.DefaultBurnRateCUHr*0.25)
			donatable := effectiveAccountSchedulableCUAt(acc, now)
			if donatable < minRunway && !freeReady {
				return false, fmt.Sprintf("below contributor reserve floor (donatable=%.2f < %.2f CU)", donatable, minRunway)
			}
		}
	} else {
		// Standard owner account check: Free accounts without Paid CUs can only run Free-eligible tiers.
		freeReady := isFreeWindowReady(acc, spec) || (spec.FreeTierEligible && acc.SubscriptionTier == models.TierFree)
		availCU := effectiveAccountSchedulableCUAt(acc, now)
		if !freeReady && availCU <= 0.25 {
			return false, fmt.Sprintf("insufficient paid CUs (%.2f CU) for %s", availCU, tier)
		}
		if acc.Health == models.HealthQuotaLowFreeOnly && !spec.FreeTierEligible {
			return false, "account in QUOTA_LOW_FREE_ONLY state"
		}
	}

	// Delegate to AccountPoolService guardrail hook when wired, passing effectiveActive.
	if s.pool != nil {
		accWithPending := *acc
		accWithPending.ActiveAssignments = effectiveActive
		if allowed, reason := s.pool.CanScheduleAccount(&accWithPending, tier); !allowed {
			return false, reason
		}
	}

	return true, ""
}

func (s *Service) pickByStrategyLocked(
	eligible []*models.Account,
	tier models.AcceleratorType,
	strategy models.RoutingStrategy,
	now time.Time,
) *models.Account {
	if len(eligible) == 0 {
		return nil
	}
	if len(eligible) == 1 {
		chosen := eligible[0]
		if strategy == models.StrategyRoundRobin {
			s.rrTick++
			s.rrSeqByAccount[chosen.ID] = s.rrTick
		}
		return chosen
	}

	candidates := append([]*models.Account(nil), eligible...)
	spec, _ := models.GetHardwareSpec(tier)

	if strategy == models.StrategyRoundRobin {
		// Monotonic ring cursor across all eligible accounts: least-recently-picked account wins,
		// breaking initial ties on effective load, priority, and deterministic ID.
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			seqA := s.rrSeqByAccount[a.ID]
			seqB := s.rrSeqByAccount[b.ID]
			if seqA != seqB {
				return seqA < seqB
			}
			loadA := s.effectiveActiveForLocked(a)
			loadB := s.effectiveActiveForLocked(b)
			if loadA != loadB {
				return loadA < loadB
			}
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			return a.ID < b.ID
		})
		chosen := candidates[0]
		s.rrTick++
		s.rrSeqByAccount[chosen.ID] = s.rrTick
		return chosen
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		actA := s.effectiveActiveForLocked(a)
		actB := s.effectiveActiveForLocked(b)
		switch strategy {
		case models.StrategyFreeOpportunistic:
			aFree := isFreeWindowReady(a, spec) || (spec.FreeTierEligible && a.SubscriptionTier == models.TierFree)
			bFree := isFreeWindowReady(b, spec) || (spec.FreeTierEligible && b.SubscriptionTier == models.TierFree)
			if aFree != bFree {
				return aFree
			}
			aFreeCU := freeQuotaCU(a)
			bFreeCU := freeQuotaCU(b)
			if math.Abs(aFreeCU-bFreeCU) > 0.01 {
				return aFreeCU > bFreeCU
			}
			if actA != actB {
				return actA < actB
			}
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			return effectiveAccountSchedulableCUAt(a, now) > effectiveAccountSchedulableCUAt(b, now)

		case models.StrategyMaxBalance:
			aCU := effectiveAccountSchedulableCUAt(a, now)
			bCU := effectiveAccountSchedulableCUAt(b, now)
			if math.Abs(aCU-bCU) > 0.01 {
				return aCU > bCU
			}
			if actA != actB {
				return actA < actB
			}
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			return a.ID < b.ID

		default: // models.StrategyWasteZero (and Sticky fallback)
			scoreA := wasteZeroScore(a, spec, actA, now)
			scoreB := wasteZeroScore(b, spec, actB, now)
			if math.Abs(scoreA-scoreB) > 1e-6 {
				return scoreA > scoreB
			}
			if actA != actB {
				return actA < actB
			}
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			return a.ID < b.ID
		}
	})

	return candidates[0]
}

// wasteZeroScore assigns higher urgency to:
//  1. Free refilling windows / TierFree accounts on Free-eligible accelerators (0 paid burn),
//  2. Accounts with expiring 90-day CU batches (`ExpiringSoonCU` / `EarliestBatchExpiresAt`),
//  3. Contributed accounts with expiring grants or high idle donatable CUs above their floor,
//  4. Balanced schedulable CU vs steeper concurrency load penalty (`-350 * effectiveActive`) and `Priority * 10`
//     so single Ultra accounts are never stampeded while healthy Pro+/Ultra peers sit idle.
func wasteZeroScore(acc *models.Account, spec models.HardwareSpec, effectiveActive int, now time.Time) float64 {
	score := 0.0

	if isFreeWindowReady(acc, spec) && acc.PaidCUBalance == 0 {
		score += 5000.0 + freeQuotaCU(acc)*10.0
	}

	if acc.ExpiringSoonCU > 0 {
		score += 2000.0 + acc.ExpiringSoonCU*5.0
	}

	if acc.EarliestBatchExpiresAt != nil {
		hoursLeft := acc.EarliestBatchExpiresAt.Sub(now).Hours()
		if hoursLeft > 0 && hoursLeft <= 24*30 {
			// Closer to 90d cliff -> higher score.
			score += 1500.0 * (1.0 - hoursLeft/(24.0*30.0))
		}
	}

	if acc.IsContributor && acc.Guardrails.ExpiresAt != nil {
		hoursLeft := acc.Guardrails.ExpiresAt.Sub(now).Hours()
		if hoursLeft > 0 && hoursLeft <= 24*30 {
			score += 800.0 * (1.0 - hoursLeft/(24.0*30.0))
		}
	}

	schedCU := effectiveAccountSchedulableCUAt(acc, now)
	score += math.Min(schedCU, 500.0)*0.4 + math.Log1p(schedCU)*15.0
	score -= float64(effectiveActive) * 350.0
	score += float64(acc.Priority) * 10.0
	return score
}

func effectiveAccountSchedulableCU(acc *models.Account) float64 {
	return effectiveAccountSchedulableCUAt(acc, time.Now().UTC())
}

func effectiveAccountSchedulableCUAt(acc *models.Account, now time.Time) float64 {
	if acc == nil {
		return 0
	}
	avail := acc.PaidCUBalance
	if acc.SchedulableCU > 0 && acc.SchedulableCU < avail {
		avail = acc.SchedulableCU
	}
	if acc.Guardrails.MinReserveCU > 0 {
		afterFloor := acc.PaidCUBalance - acc.Guardrails.MinReserveCU
		if afterFloor < avail {
			avail = afterFloor
		}
	}
	if acc.IsContributor {
		if acc.Guardrails.MaxDonatedCUTotal > 0 {
			rem := acc.Guardrails.MaxDonatedCUTotal - acc.Guardrails.DonatedCUUsedTotal
			if rem < avail {
				avail = rem
			}
		}
		if acc.Guardrails.MaxDonatedCUDaily > 0 {
			usedToday := acc.Guardrails.DonatedCUUsedToday
			today := now.UTC().Format("2006-01-02")
			if acc.Guardrails.DailyUsageDate != "" && acc.Guardrails.DailyUsageDate != today {
				usedToday = 0
			}
			rem := acc.Guardrails.MaxDonatedCUDaily - usedToday
			if rem < avail {
				avail = rem
			}
		}
	}
	if avail < 0 {
		return 0
	}
	return avail
}

func isFreeWindowReady(acc *models.Account, spec models.HardwareSpec) bool {
	if acc == nil || !spec.FreeTierEligible {
		return false
	}
	if hasFreeWindow(acc) {
		return true
	}
	return acc.SubscriptionTier == models.TierFree && acc.PaidCUBalance == 0
}

func hasFreeWindow(acc *models.Account) bool {
	return acc != nil && acc.FreeQuota != nil && (acc.FreeQuota.RemainingMilliCCU > 0 || acc.FreeQuota.RemainingFreeCU > 0)
}

func freeQuotaCU(acc *models.Account) float64 {
	if acc == nil || acc.FreeQuota == nil {
		return 0
	}
	if acc.FreeQuota.RemainingFreeCU > 0 {
		return acc.FreeQuota.RemainingFreeCU
	}
	return float64(acc.FreeQuota.RemainingMilliCCU) / 1000.0
}

func withinScheduleWindow(win *models.ScheduleWindow, now time.Time) bool {
	if win == nil || !win.Enabled {
		return true
	}
	loc := resolveRouterLocation(win.Timezone)
	local := now.In(loc)

	startMin, ok1 := parseHHMM(win.StartHour)
	endMin, ok2 := parseHHMM(win.EndHour)
	curMin := local.Hour()*60 + local.Minute()

	dayAllowed := func(wd time.Weekday) bool {
		if len(win.DaysOfWeek) == 0 {
			return true
		}
		for _, d := range win.DaysOfWeek {
			if d == int(wd) {
				return true
			}
		}
		return false
	}

	if !ok1 || !ok2 || startMin == endMin {
		return dayAllowed(local.Weekday())
	}
	if startMin < endMin {
		return dayAllowed(local.Weekday()) && curMin >= startMin && curMin < endMin
	}
	// Overnight window (e.g. 22:00 -> 09:00 spanning midnight): check previous weekday after 00:00!
	if curMin >= startMin {
		return dayAllowed(local.Weekday())
	}
	if curMin < endMin {
		prevDay := (local.Weekday() + 6) % 7
		return dayAllowed(prevDay)
	}
	return false
}

func resolveRouterLocation(tz string) *time.Location {
	trimmed := strings.TrimSpace(tz)
	if trimmed == "" || strings.EqualFold(trimmed, "UTC") {
		return time.UTC
	}
	if loc, err := time.LoadLocation(trimmed); err == nil {
		return loc
	}
	switch trimmed {
	case "Asia/Shanghai", "Asia/Hong_Kong", "Asia/Taipei", "Asia/Singapore":
		return time.FixedZone(trimmed, 8*3600)
	case "Asia/Tokyo", "Asia/Seoul":
		return time.FixedZone(trimmed, 9*3600)
	case "America/Los_Angeles", "US/Pacific":
		return time.FixedZone(trimmed, -8*3600)
	case "America/New_York", "US/Eastern":
		return time.FixedZone(trimmed, -5*3600)
	case "Europe/Berlin", "Europe/Paris":
		return time.FixedZone(trimmed, 1*3600)
	default:
		return time.UTC
	}
}

func parseHHMM(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func matchesAccountSelector(acc *models.Account, selector string) bool {
	sel := strings.TrimSpace(selector)
	if sel == "" || strings.EqualFold(sel, "all") {
		return true
	}
	lower := strings.ToLower(sel)
	if strings.HasPrefix(lower, "tag:") {
		want := strings.TrimPrefix(lower, "tag:")
		for _, t := range acc.Tags {
			if strings.EqualFold(strings.TrimSpace(t), want) {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(lower, "group:") {
		want := strings.TrimPrefix(lower, "group:")
		for _, g := range acc.Groups {
			if strings.EqualFold(strings.TrimSpace(g), want) {
				return true
			}
		}
		return false
	}
	return acc.ID == sel ||
		strings.EqualFold(acc.Email, sel) ||
		(acc.Alias != "" && strings.EqualFold(acc.Alias, sel))
}

func hasAnyGroup(accGroups []string, allowed []string) bool {
	for _, want := range allowed {
		for _, g := range accGroups {
			if strings.EqualFold(strings.TrimSpace(g), strings.TrimSpace(want)) {
				return true
			}
		}
	}
	return false
}

func intersectOrCopyGroups(a, b []string) []string {
	if len(a) == 0 {
		return append([]string(nil), b...)
	}
	out := make([]string, 0, len(a))
	for _, item := range a {
		if hasAnyGroup(b, []string{item}) {
			out = append(out, item)
		}
	}
	return out
}

func containsAccelerator(list []models.AcceleratorType, target models.AcceleratorType) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

// ============================================================================
// Sticky-Agent-Session Affinity & Automatic Sticky Escape
// ============================================================================

func stickyStorageKey(apiKeyID, stickyKey string) string {
	return strings.TrimSpace(apiKeyID) + "\x00" + strings.TrimSpace(stickyKey)
}

func (s *Service) resolveStickyAccountIDForKey(ctx context.Context, stickyKey string, callerAPIKeyID string, ttlSec int, now time.Time) string {
	sess, ok := s.ResolveStickySessionForKey(ctx, stickyKey, callerAPIKeyID)
	if ok && sess != nil {
		return sess.AccountID
	}
	mapKey := stickyStorageKey(callerAPIKeyID, stickyKey)
	s.mu.RLock()
	b, exists := s.stickyBindings[mapKey]
	var accID string
	var boundKeyID string
	var lastActive time.Time
	if exists && b != nil {
		accID = b.AccountID
		boundKeyID = b.APIKeyID
		lastActive = b.LastActiveAt
	}
	s.mu.RUnlock()
	if accID == "" {
		return ""
	}
	if strings.TrimSpace(callerAPIKeyID) != strings.TrimSpace(boundKeyID) {
		return ""
	}
	ttl := time.Duration(ttlSec) * time.Second
	if ttl > 0 && now.Sub(lastActive) > ttl {
		_ = s.UnbindStickySessionForKey(ctx, stickyKey, callerAPIKeyID)
		return ""
	}
	return accID
}

// ResolveStickySession returns the warm RuntimeSession bound to stickyKey (admin/unscoped namespace)
// if and only if the binding has not expired AND the underlying VM/account is still alive and healthy.
func (s *Service) ResolveStickySession(ctx context.Context, stickyKey string) (*models.RuntimeSession, bool) {
	return s.ResolveStickySessionForKey(ctx, stickyKey, "")
}

// ResolveStickySessionForKey resolves a warm sticky RuntimeSession namespaced by callerAPIKeyID
// while enforcing APIKeyID ownership isolation and filtering out unintended AdoptedExternal sessions.
func (s *Service) ResolveStickySessionForKey(
	ctx context.Context,
	stickyKey string,
	callerAPIKeyID string,
) (*models.RuntimeSession, bool) {
	key := strings.TrimSpace(stickyKey)
	if key == "" {
		return nil, false
	}
	callerKey := strings.TrimSpace(callerAPIKeyID)
	if strings.Contains(key, "\x00") {
		parts := strings.SplitN(key, "\x00", 2)
		if callerKey == "" {
			callerKey = strings.TrimSpace(parts[0])
		}
		key = strings.TrimSpace(parts[1])
		if key == "" {
			return nil, false
		}
	}
	mapKey := stickyStorageKey(callerKey, key)

	policy, _ := s.GetPolicy(ctx)
	if policy != nil && !policy.StickySessionEnabled {
		return nil, false
	}
	ttlSec := 1200
	if policy != nil && policy.StickyIdleTTLSec > 0 {
		ttlSec = policy.StickyIdleTTLSec
	}

	now := s.nowUTC()
	s.mu.RLock()
	binding, exists := s.stickyBindings[mapKey]
	if !exists || binding == nil {
		s.mu.RUnlock()
		return nil, false
	}
	sessID := binding.SessionID
	sessName := binding.SessionName
	boundAPIKeyID := strings.TrimSpace(binding.APIKeyID)
	lastActive := binding.LastActiveAt
	s.mu.RUnlock()

	if boundAPIKeyID != callerKey {
		return nil, false
	}

	if ttlSec > 0 && now.Sub(lastActive) > time.Duration(ttlSec)*time.Second {
		_ = s.UnbindStickySessionForKey(ctx, key, callerKey)
		return nil, false
	}

	if s.st == nil {
		return nil, false
	}
	sess, err := s.st.GetSession(ctx, sessID)
	if (err != nil || sess == nil) && sessName != "" && callerKey == "" {
		sess, err = s.st.GetSessionByName(ctx, sessName)
	}
	if err != nil || sess == nil || !isSessionAliveForSticky(sess.Status) {
		// Sticky Escape: VM terminated, lost, or entered zombie state.
		_ = s.UnbindStickySessionForKey(ctx, key, callerKey)
		return nil, false
	}

	// Never hijack or route sticky traffic onto an externally started Colab notebook unless explicitly bound to it.
	if sess.AdoptedExternal && strings.TrimSpace(sess.StickyKey) != key {
		_ = s.UnbindStickySessionForKey(ctx, key, callerKey)
		return nil, false
	}

	// Verify API key consistency & live budget/enablement on the bound key.
	sessAPIKeyID := strings.TrimSpace(sess.APIKeyID)
	if sessAPIKeyID != callerKey {
		_ = s.UnbindStickySessionForKey(ctx, key, callerKey)
		return nil, false
	}
	effectiveKeyID := boundAPIKeyID
	if effectiveKeyID == "" {
		effectiveKeyID = sessAPIKeyID
	}
	if effectiveKeyID != "" {
		storedKey, kErr := s.st.GetAPIKey(ctx, effectiveKeyID)
		if kErr != nil || storedKey == nil {
			_ = s.UnbindStickySessionForKey(ctx, key, callerKey)
			return nil, false
		}
		if normalizeAPIKeyDailyUsage(storedKey, now) {
			s.rolloverAPIKeyDailyUsageSafe(ctx, storedKey.ID, now)
		}
		if validateAPIKeyLimits(storedKey, now) != nil {
			_ = s.UnbindStickySessionForKey(ctx, key, callerKey)
			return nil, false
		}
	}

	// Verify owning account hasn't been revoked or exhausted below floor.
	if acc, accErr := s.loadSingleAccount(ctx, sess.AccountID); accErr == nil && acc != nil {
		if !acc.Enabled || !acc.Health.IsSchedulable() {
			_ = s.UnbindStickySessionForKey(ctx, key, callerKey)
			return nil, false
		}
		if acc.IsContributor && acc.Guardrails.Paused {
			_ = s.UnbindStickySessionForKey(ctx, key, callerKey)
			return nil, false
		}
	}

	s.mu.Lock()
	if cur, ok := s.stickyBindings[mapKey]; ok && cur != nil {
		cur.LastActiveAt = now
		cur.ActiveCalls++
		cur.IdleTTLRemainingSec = ttlSec
	}
	s.mu.Unlock()

	return sess, true
}

func (s *Service) loadSingleAccount(ctx context.Context, accountID string) (*models.Account, error) {
	if s.pool != nil {
		return s.pool.GetAccount(ctx, accountID)
	}
	if s.st != nil {
		return s.st.GetAccount(ctx, accountID)
	}
	return nil, nil
}

func isSessionAliveForSticky(st models.SessionStatus) bool {
	switch st {
	case models.SessionIdle, models.SessionBusy, models.SessionProvisioning, models.SessionReconnecting:
		return true
	default:
		return false
	}
}

// BindStickySession pins an Agent/MCP session key to a live Colab RuntimeSession.
func (s *Service) BindStickySession(ctx context.Context, binding models.RouteBinding) error {
	key := strings.TrimSpace(binding.StickyKey)
	if key == "" {
		return errors.New("sticky_key is required")
	}
	now := s.nowUTC()
	if binding.PinnedAt.IsZero() {
		binding.PinnedAt = now
	}
	if binding.LastActiveAt.IsZero() {
		binding.LastActiveAt = now
	}
	if binding.ClientName == "" {
		binding.ClientName = "agent"
	}
	if binding.Strategy == "" {
		binding.Strategy = models.StrategyStickySession
	}
	policy, _ := s.GetPolicy(ctx)
	ttlSec := 1200
	if policy != nil && policy.StickyIdleTTLSec > 0 {
		ttlSec = policy.StickyIdleTTLSec
	}
	binding.IdleTTLRemainingSec = ttlSec
	if binding.ActiveCalls <= 0 {
		binding.ActiveCalls = 1
	}

	apiKeyID := strings.TrimSpace(binding.APIKeyID)
	if strings.Contains(key, "\x00") {
		parts := strings.SplitN(key, "\x00", 2)
		if apiKeyID == "" {
			apiKeyID = strings.TrimSpace(parts[0])
		}
		key = strings.TrimSpace(parts[1])
		if key == "" {
			return errors.New("sticky_key is required")
		}
	}

	cp := binding
	cp.StickyKey = key
	cp.APIKeyID = apiKeyID
	mapKey := stickyStorageKey(apiKeyID, key)

	var prevAccID string
	s.mu.Lock()
	if prev := s.stickyBindings[mapKey]; prev != nil {
		prevAccID = prev.AccountID
	}
	s.stickyBindings[mapKey] = &cp
	s.mu.Unlock()

	s.syncStickyCounts(ctx, prevAccID, cp.AccountID)
	return nil
}

// UnbindStickySessionForKey removes a sticky session affinity link strictly within apiKeyID's namespace.
func (s *Service) UnbindStickySessionForKey(ctx context.Context, stickyKey string, apiKeyID string) error {
	key := strings.TrimSpace(stickyKey)
	if key == "" {
		return nil
	}
	if strings.Contains(key, "\x00") {
		parts := strings.SplitN(key, "\x00", 2)
		if strings.TrimSpace(apiKeyID) == "" {
			apiKeyID = strings.TrimSpace(parts[0])
		}
		key = strings.TrimSpace(parts[1])
	}
	mapKey := stickyStorageKey(apiKeyID, key)
	var prevAccID string
	s.mu.Lock()
	if prev := s.stickyBindings[mapKey]; prev != nil {
		prevAccID = prev.AccountID
	}
	delete(s.stickyBindings, mapKey)
	s.mu.Unlock()

	if prevAccID != "" {
		s.syncStickyCounts(ctx, prevAccID)
	}
	return nil
}

// UnbindStickySession removes an unscoped/admin sticky session affinity link (or an explicit `apiKeyID+"\x00"+stickyKey`).
// Scoped tenant bindings are isolated under `apiKeyID+"\x00"+stickyKey` so unscoped calls never clobber tenant bindings.
func (s *Service) UnbindStickySession(ctx context.Context, stickyKey string) error {
	return s.UnbindStickySessionForKey(ctx, stickyKey, "")
}

// UnbindStickySessionAdmin allows admin `/api/v1/router/sticky/{stickyKey}` to unbind either an explicit
// `?api_key_id=` binding or any matching binding when invoked from the admin control plane.
func (s *Service) UnbindStickySessionAdmin(ctx context.Context, stickyKey string, apiKeyID string) error {
	key := strings.TrimSpace(stickyKey)
	if key == "" {
		return nil
	}
	if strings.TrimSpace(apiKeyID) != "" || strings.Contains(key, "\x00") {
		return s.UnbindStickySessionForKey(ctx, key, apiKeyID)
	}
	var affected []string
	s.mu.Lock()
	for mapKey, b := range s.stickyBindings {
		if b != nil && b.StickyKey == key {
			if b.AccountID != "" {
				affected = append(affected, b.AccountID)
			}
			delete(s.stickyBindings, mapKey)
		}
	}
	s.mu.Unlock()
	if len(affected) > 0 {
		s.syncStickyCounts(ctx, affected...)
	}
	return nil
}

func (s *Service) syncStickyCounts(ctx context.Context, accountIDs ...string) {
	if ctx == nil {
		ctx = context.Background()
	}
	policy, _ := s.GetPolicy(ctx)
	ttlSec := 1200
	if policy != nil && policy.StickyIdleTTLSec > 0 {
		ttlSec = policy.StickyIdleTTLSec
	}
	ttl := time.Duration(ttlSec) * time.Second
	now := s.nowUTC()

	counts := make(map[string]int)
	targets := make(map[string]bool)
	for _, id := range accountIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			targets[trimmed] = true
		}
	}

	s.mu.RLock()
	for _, b := range s.stickyBindings {
		if b == nil || b.AccountID == "" {
			continue
		}
		if ttl > 0 && now.Sub(b.LastActiveAt) > ttl {
			continue
		}
		counts[b.AccountID]++
		targets[b.AccountID] = true
	}
	s.mu.RUnlock()

	for accID := range targets {
		cnt := counts[accID]
		if syncer, ok := s.pool.(stickyCountSyncer); ok {
			_ = syncer.SetAccountStickyCount(ctx, accID, cnt)
		} else if s.st != nil {
			if acc, err := s.st.GetAccount(ctx, accID); err == nil && acc != nil && acc.StickyAgentCount != cnt {
				acc.StickyAgentCount = cnt
				_ = s.st.SaveAccount(ctx, acc)
			}
		}
	}
}

// ListStickyBindings returns all active sticky session bindings with live TTL countdowns.
func (s *Service) ListStickyBindings(ctx context.Context) ([]models.RouteBinding, error) {
	policy, _ := s.GetPolicy(ctx)
	ttlSec := 1200
	if policy != nil && policy.StickyIdleTTLSec > 0 {
		ttlSec = policy.StickyIdleTTLSec
	}
	ttl := time.Duration(ttlSec) * time.Second
	now := s.nowUTC()

	var prunedAccIDs []string
	s.mu.Lock()
	out := make([]models.RouteBinding, 0, len(s.stickyBindings))
	for k, b := range s.stickyBindings {
		if b == nil {
			delete(s.stickyBindings, k)
			continue
		}
		elapsed := now.Sub(b.LastActiveAt)
		if ttl > 0 && elapsed > ttl {
			if b.AccountID != "" {
				prunedAccIDs = append(prunedAccIDs, b.AccountID)
			}
			delete(s.stickyBindings, k)
			continue
		}
		if s.st != nil && b.SessionID != "" {
			if sess, err := s.st.GetSession(ctx, b.SessionID); err == nil && sess != nil {
				if !isSessionAliveForSticky(sess.Status) {
					if b.AccountID != "" {
						prunedAccIDs = append(prunedAccIDs, b.AccountID)
					}
					delete(s.stickyBindings, k)
					continue
				}
				if b.KernelID == "" {
					b.KernelID = sess.KernelID
				}
			}
		}
		cp := *b
		rem := int((ttl - elapsed).Seconds())
		if rem < 0 {
			rem = 0
		}
		cp.IdleTTLRemainingSec = rem
		out = append(out, cp)
	}
	s.mu.Unlock()

	if len(prunedAccIDs) > 0 {
		s.syncStickyCounts(ctx, prunedAccIDs...)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].LastActiveAt.After(out[j].LastActiveAt)
	})
	return out, nil
}

// ============================================================================
// Router Policy & Tool-Call Trace Inspector
// ============================================================================

// GetPolicy loads the global RouterPolicy from SQLite or falls back to DefaultRouterPolicy.
func (s *Service) GetPolicy(ctx context.Context) (*models.RouterPolicy, error) {
	def := models.DefaultRouterPolicy()
	if s.st == nil {
		return &def, nil
	}
	pol, err := s.st.GetRouterPolicy(ctx)
	if err != nil || pol == nil {
		return &def, nil
	}
	if pol.DefaultStrategy == "" {
		pol.DefaultStrategy = def.DefaultStrategy
	}
	if pol.StickyIdleTTLSec <= 0 {
		pol.StickyIdleTTLSec = def.StickyIdleTTLSec
	}
	if len(pol.FallbackChain) == 0 {
		pol.FallbackChain = def.FallbackChain
	}
	if len(pol.LargeVRAMFallbackChain) == 0 {
		pol.LargeVRAMFallbackChain = def.LargeVRAMFallbackChain
	}
	if pol.KeepaliveIntervalSec <= 0 {
		pol.KeepaliveIntervalSec = def.KeepaliveIntervalSec
	}
	if pol.ResourcePollIntervalSec <= 0 {
		pol.ResourcePollIntervalSec = def.ResourcePollIntervalSec
	}
	return pol, nil
}

// UpdatePolicy validates and persists a new RouterPolicy.
func (s *Service) UpdatePolicy(ctx context.Context, policy models.RouterPolicy) (*models.RouterPolicy, error) {
	def := models.DefaultRouterPolicy()
	policy.DefaultStrategy = NormalizeStrategy(policy.DefaultStrategy, def.DefaultStrategy)
	if policy.StickyIdleTTLSec <= 0 {
		policy.StickyIdleTTLSec = def.StickyIdleTTLSec
	}
	if len(policy.FallbackChain) == 0 {
		policy.FallbackChain = def.FallbackChain
	}
	if len(policy.LargeVRAMFallbackChain) == 0 {
		policy.LargeVRAMFallbackChain = def.LargeVRAMFallbackChain
	}
	if policy.KeepaliveIntervalSec <= 0 {
		policy.KeepaliveIntervalSec = def.KeepaliveIntervalSec
	}
	if policy.ResourcePollIntervalSec <= 0 {
		policy.ResourcePollIntervalSec = def.ResourcePollIntervalSec
	}
	policy.UpdatedAt = time.Now().UTC()

	if s.st != nil {
		if err := s.st.SaveRouterPolicy(ctx, &policy); err != nil {
			return nil, err
		}
	}
	return &policy, nil
}

// RecordTrace appends a ToolTraceEvent to persistent storage and broadcasts it over WebSocket.
func (s *Service) RecordTrace(ctx context.Context, event models.ToolTraceEvent) error {
	if event.ID == "" {
		event.ID = models.NewPrefixedID("trc_")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.Status == "" {
		event.Status = "ok"
	}
	if event.StatusCode == 0 {
		if event.Status == "error" {
			event.StatusCode = 500
		} else {
			event.StatusCode = 200
		}
	}
	if len(event.RequestPreview) > 512 {
		event.RequestPreview = event.RequestPreview[:512] + "..."
	}
	if len(event.ResponsePreview) > 512 {
		event.ResponsePreview = event.ResponsePreview[:512] + "..."
	}

	if s.st != nil {
		if err := s.st.AppendToolTrace(ctx, &event); err != nil {
			return err
		}
		if event.CUDelta > 0 && event.APIKeyPrefix != "" {
			s.incrementKeyUsageByPrefix(ctx, event.APIKeyPrefix, event.CUDelta)
		}
	}

	if s.hub != nil {
		s.hub.Publish(models.WSEvent{
			Type:      models.WSEventTraceEvent,
			Timestamp: event.Timestamp,
			Payload:   event,
		})
	}
	return nil
}

// ListTraces returns the most recent tool-call traces (newest first).
func (s *Service) ListTraces(ctx context.Context, limit int) ([]models.ToolTraceEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if s.st == nil {
		return []models.ToolTraceEvent{}, nil
	}
	traces, err := s.st.ListToolTraces(ctx, limit)
	if err != nil {
		return nil, err
	}
	if traces == nil {
		return []models.ToolTraceEvent{}, nil
	}
	return traces, nil
}

// ============================================================================
// Scoped Agent API Keys (`ch_sk_...`)
// ============================================================================

// HashAPIKey computes the hex-encoded SHA-256 digest of a raw ch_sk_... secret.
func HashAPIKey(rawKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawKey)))
	return hex.EncodeToString(sum[:])
}

// CreateAPIKey mints a new scoped ch_sk_live_... secret, stores only its SHA-256 hash,
// and returns the raw key once in the creation response.
func (s *Service) CreateAPIKey(ctx context.Context, req models.CreateAPIKeyRequest) (*models.AgentAPIKey, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Agent Key"
	}

	randBytes := make([]byte, 18)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random API key: %w", err)
	}
	rawSecret := "ch_sk_live_" + hex.EncodeToString(randBytes)
	keyHash := HashAPIKey(rawSecret)
	prefix := rawSecret[:19] + "..."

	now := s.nowUTC()
	var expiresAt *time.Time
	if req.ExpiresInDays > 0 {
		exp := now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &exp
	}

	allowedGroups := req.AllowedGroups
	if allowedGroups == nil {
		allowedGroups = []string{}
	}
	allowedGPUs := req.AllowedGPUs
	if allowedGPUs == nil {
		allowedGPUs = []models.AcceleratorType{}
	}

	stored := &models.AgentAPIKey{
		ID:               models.NewPrefixedID("key_"),
		Name:             name,
		KeyPrefix:        prefix,
		KeyHash:          keyHash,
		AllowedGroups:    allowedGroups,
		AllowedGPUs:      allowedGPUs,
		DefaultStrategy:  req.DefaultStrategy,
		MaxBudgetCUTotal: req.MaxBudgetCUTotal,
		MaxBudgetCUDaily: req.MaxBudgetCUDaily,
		DailyUsageDate:   now.Format("2006-01-02"),
		MaxConcurrentVMs: req.MaxConcurrentVMs,
		IdleAutoKillMin:  req.IdleAutoKillMin,
		Enabled:          true,
		ExpiresAt:        expiresAt,
		CreatedAt:        now,
	}

	if s.st != nil {
		s.keyMu.Lock()
		err := s.st.SaveAPIKey(ctx, stored)
		s.keyMu.Unlock()
		if err != nil {
			return nil, err
		}
	}

	withRaw := *stored
	withRaw.RawKey = rawSecret
	return &withRaw, nil
}

// AuthenticateAPIKey verifies a raw ch_sk_... key by SHA-256 hash and enforces CU budgets.
func (s *Service) AuthenticateAPIKey(ctx context.Context, rawKey string) (*models.AgentAPIKey, error) {
	cleaned := strings.TrimSpace(rawKey)
	if strings.HasPrefix(strings.ToLower(cleaned), "bearer ") {
		cleaned = strings.TrimSpace(cleaned[7:])
	}
	if cleaned == "" || !strings.HasPrefix(cleaned, "ch_sk_") {
		return nil, ErrInvalidAPIKey
	}
	if s.st == nil {
		return nil, ErrInvalidAPIKey
	}

	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	key, err := s.st.GetAPIKeyByHash(ctx, HashAPIKey(cleaned))
	if err != nil || key == nil {
		return nil, ErrInvalidAPIKey
	}

	now := s.nowUTC()
	rolled := normalizeAPIKeyDailyUsage(key, now)
	if err := validateAPIKeyLimits(key, now); err != nil {
		if rolled {
			key.RawKey = ""
			_ = s.st.SaveAPIKey(ctx, key)
		}
		return nil, err
	}

	key.LastUsedAt = &now
	key.RawKey = ""
	_ = s.st.SaveAPIKey(ctx, key)
	return key, nil
}

// ListAPIKeys returns all issued Agent API keys (never exposing RawKey).
func (s *Service) ListAPIKeys(ctx context.Context) ([]*models.AgentAPIKey, error) {
	if s.st == nil {
		return []*models.AgentAPIKey{}, nil
	}
	keys, err := s.st.ListAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	if keys == nil {
		return []*models.AgentAPIKey{}, nil
	}
	now := s.nowUTC()
	for _, k := range keys {
		if k != nil {
			if normalizeAPIKeyDailyUsage(k, now) {
				s.rolloverAPIKeyDailyUsageSafe(ctx, k.ID, now)
			}
			k.RawKey = ""
		}
	}
	return keys, nil
}

// RevokeAPIKey permanently deletes/revokes an Agent API key.
func (s *Service) RevokeAPIKey(ctx context.Context, id string) error {
	if s.st == nil {
		return nil
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	return s.st.DeleteAPIKey(ctx, strings.TrimSpace(id))
}

// RecordAPIKeyCUBurn atomically charges deltaCU against apiKeyID's daily and total CU ledgers
// under s.keyMu (preventing races with RevokeAPIKey / AuthenticateAPIKey) and returns exceeded=true
// if the key was revoked, disabled, expired, or exceeded MaxBudgetCUDaily / MaxBudgetCUTotal.
func (s *Service) RecordAPIKeyCUBurn(ctx context.Context, apiKeyID string, deltaCU float64) (exceeded bool, reason string) {
	id := strings.TrimSpace(apiKeyID)
	if id == "" || s.st == nil {
		return false, ""
	}

	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	key, err := s.st.GetAPIKey(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, "API key has been revoked"
		}
		return false, ""
	}
	if key == nil {
		return false, ""
	}

	now := s.nowUTC()
	dirty := normalizeAPIKeyDailyUsage(key, now)
	if deltaCU > 0 {
		key.UsedCUTotal = math.Round((key.UsedCUTotal+deltaCU)*10000) / 10000
		key.UsedCUToday = math.Round((key.UsedCUToday+deltaCU)*10000) / 10000
		key.LastUsedAt = &now
		dirty = true
	}
	if dirty {
		key.RawKey = ""
		_ = s.st.SaveAPIKey(ctx, key)
	}
	if limitErr := validateAPIKeyLimits(key, now); limitErr != nil {
		return true, limitErr.Error()
	}
	return false, ""
}

func (s *Service) rolloverAPIKeyDailyUsageSafe(ctx context.Context, keyID string, now time.Time) {
	if s.st == nil || strings.TrimSpace(keyID) == "" {
		return
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	fresh, err := s.st.GetAPIKey(ctx, keyID)
	if err != nil || fresh == nil {
		return
	}
	if normalizeAPIKeyDailyUsage(fresh, now) {
		fresh.RawKey = ""
		_ = s.st.SaveAPIKey(ctx, fresh)
	}
}

func normalizeAPIKeyDailyUsage(key *models.AgentAPIKey, now time.Time) bool {
	if key == nil {
		return false
	}
	today := now.UTC().Format("2006-01-02")
	if key.DailyUsageDate == "" {
		key.DailyUsageDate = today
		return true
	}
	if key.DailyUsageDate != today {
		key.UsedCUToday = 0
		key.DailyUsageDate = today
		return true
	}
	return false
}

func validateAPIKeyLimits(key *models.AgentAPIKey, now time.Time) error {
	if !key.Enabled {
		return fmt.Errorf("%w: API key %q is disabled", ErrInvalidAPIKey, key.KeyPrefix)
	}
	if key.ExpiresAt != nil && now.After(*key.ExpiresAt) {
		return fmt.Errorf("%w: API key %q expired at %s", ErrInvalidAPIKey, key.KeyPrefix, key.ExpiresAt.Format(time.RFC3339))
	}
	if key.MaxBudgetCUTotal > 0 && key.UsedCUTotal >= key.MaxBudgetCUTotal {
		return fmt.Errorf(
			"%w: total CU budget exhausted (%.2f / %.2f CU)",
			ErrAPIKeyBudgetExceeded, key.UsedCUTotal, key.MaxBudgetCUTotal,
		)
	}
	usedToday := key.UsedCUToday
	today := now.UTC().Format("2006-01-02")
	if key.DailyUsageDate != "" && key.DailyUsageDate != today {
		usedToday = 0
	}
	if key.MaxBudgetCUDaily > 0 && usedToday >= key.MaxBudgetCUDaily {
		return fmt.Errorf(
			"%w: daily CU budget exhausted (%.2f / %.2f CU)",
			ErrAPIKeyBudgetExceeded, usedToday, key.MaxBudgetCUDaily,
		)
	}
	return nil
}

func (s *Service) countActiveSessionsForAPIKey(ctx context.Context, keyID string) int {
	if s.st == nil || keyID == "" {
		return 0
	}
	sessions, err := s.st.ListSessions(ctx)
	if err != nil {
		return 0
	}
	count := 0
	for _, sess := range sessions {
		if sess != nil && sess.APIKeyID == keyID && isSessionAliveForSticky(sess.Status) {
			count++
		}
	}
	return count
}

func (s *Service) incrementKeyUsageByPrefix(ctx context.Context, prefix string, cuDelta float64) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	keys, err := s.st.ListAPIKeys(ctx)
	if err != nil {
		return
	}
	now := s.nowUTC()
	for _, k := range keys {
		if k != nil && k.KeyPrefix == prefix {
			fresh, gErr := s.st.GetAPIKey(ctx, k.ID)
			if gErr != nil || fresh == nil {
				return
			}
			normalizeAPIKeyDailyUsage(fresh, now)
			fresh.UsedCUTotal = math.Round((fresh.UsedCUTotal+cuDelta)*10000) / 10000
			fresh.UsedCUToday = math.Round((fresh.UsedCUToday+cuDelta)*10000) / 10000
			fresh.RawKey = ""
			_ = s.st.SaveAPIKey(ctx, fresh)
			return
		}
	}
}
