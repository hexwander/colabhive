package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

const usdPerComputeUnit = 0.10 // Official Colab PAYG rate ($10 / 100 CU)

// StripColabXSSI strips Google Colab's 5-byte `)]}'\n` XSSI guard prefix from JSON payloads.
func StripColabXSSI(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte(")]}'")) {
		trimmed = bytes.TrimLeft(trimmed[4:], "\r\n\t ")
	}
	return trimmed
}

type tunnelCCUInfoWire struct {
	CurrentBalance        float64   `json:"currentBalance"`
	ConsumptionRateHourly float64   `json:"consumptionRateHourly"`
	AssignmentsCount      int       `json:"assignmentsCount"`
	SubscriptionTier      string    `json:"subscriptionTier"`
	Tier                  string    `json:"tier"`
	ProTier               any       `json:"proTier"`
	EligibleGPUs          []string  `json:"eligibleGpus"`
	EligibleTPUs          []string  `json:"eligibleTpus"`
	FreeCCUQuotaInfo      *freeWire `json:"freeCcuQuotaInfo"`
}

type publicSubscriptionWire struct {
	Tier             string `json:"tier"`
	SubscriptionTier string `json:"subscriptionTier"`
	ProTier          any    `json:"proTier"`
}

// TierStandardCycleCU returns the standard monthly cycle Compute Unit pack for a subscription tier.
func TierStandardCycleCU(tier models.SubscriptionTier) float64 {
	return models.TierStandardCycleCU(tier)
}

type gapiUserInfoWire struct {
	SubscriptionTier        string            `json:"subscriptionTier"`
	PaidComputeUnitsBalance float64           `json:"paidComputeUnitsBalance"`
	ConsumptionRateHourly   float64           `json:"consumptionRateHourly"`
	AssignmentsCount        int               `json:"assignmentsCount"`
	EligibleAccelerators    []accelModelsWire `json:"eligibleAccelerators"`
	IneligibleAccelerators  []accelModelsWire `json:"ineligibleAccelerators"`
	FreeCCUQuotaInfo        *freeWire         `json:"freeCcuQuotaInfo"`
}

type accelModelsWire struct {
	Variant string   `json:"variant"`
	Models  []string `json:"models"`
}

type freeWire struct {
	RemainingTokens        any   `json:"remainingTokens"`
	NextRefillTimestampSec int64 `json:"nextRefillTimestampSec"`
}

func parseFreeQuotaWire(w *freeWire) *models.FreeQuotaInfo {
	if w == nil {
		return nil
	}
	var milli int64
	switch v := w.RemainingTokens.(type) {
	case string:
		milli, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	case float64:
		milli = int64(v)
	case json.Number:
		milli, _ = v.Int64()
	}
	var refill *time.Time
	if w.NextRefillTimestampSec > 0 {
		t := time.Unix(w.NextRefillTimestampSec, 0).UTC()
		refill = &t
	}
	return &models.FreeQuotaInfo{
		RemainingMilliCCU: milli,
		RemainingFreeCU:   round2(float64(milli) / 1000.0),
		NextRefillAt:      refill,
	}
}

// RefreshAccountQuota polls Google's dual quota endpoints (`/tun/m/ccu-info?authuser=0`
// and `colab.pa.googleapis.com/v1/user-info?get_ccu_consumption_info=true`), updates
// EWMA empirical burn calibration when `assignmentsCount == 1`, and persists updated telemetry.
func (s *Service) RefreshAccountQuota(ctx context.Context, id string) (*models.Account, error) {
	accessToken, proxyURL, err := s.AcquireAccessToken(ctx, id)
	if err != nil {
		return nil, err
	}

	httpClient, err := s.transport.HTTPClient(proxyURL, 15*time.Second)
	if err != nil {
		return nil, err
	}

	// 1. Primary Tunnel CCU Endpoint: GET /tun/m/ccu-info?authuser=0
	var tunData *tunnelCCUInfoWire
	tunURL := s.cfg.ColabTunnelDomain + "/tun/m/ccu-info?authuser=0"
	if req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, tunURL, nil); rErr == nil {
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Colab-Client-Agent", "colab-cli")
		if resp, doErr := httpClient.Do(req); doErr == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var parsed tunnelCCUInfoWire
				if json.Unmarshal(StripColabXSSI(body), &parsed) == nil {
					tunData = &parsed
				}
			}
		}
	}

	// 2. Rich GAPI Endpoint: GET /v1/user-info?get_ccu_consumption_info=true
	var gapiData *gapiUserInfoWire
	gapiURL := s.cfg.ColabGAPIDomain + "/v1/user-info?get_ccu_consumption_info=true"
	if req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, gapiURL, nil); rErr == nil {
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Colab-Client-Agent", "vscode")
		if resp, doErr := httpClient.Do(req); doErr == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var parsed gapiUserInfoWire
				if json.Unmarshal(StripColabXSSI(body), &parsed) == nil {
					gapiData = &parsed
				}
			}
		}
	}

	if tunData == nil && gapiData == nil {
		return nil, fmt.Errorf("pool: failed to fetch Colab quota from both tunnel and GAPI endpoints for account %s", id)
	}

	// 3. Official Public API subscription probe (`GET /v1beta/subscription`) on initial check
	//    or whenever GAPI `/v1/user-info` is unavailable and Public API is supported.
	mu := s.accountMutex(id)
	mu.Lock()
	acc, err := s.st.GetAccount(ctx, id)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	needPublicProbe := acc.LastPolledAt == nil || (acc.PublicAPISupported && (gapiData == nil || strings.TrimSpace(gapiData.SubscriptionTier) == ""))
	mu.Unlock()

	publicSupported := acc.PublicAPISupported
	var publicSubTier string
	if needPublicProbe {
		pCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		pubURL := s.cfg.ColabPublicDomain + "/v1beta/subscription"
		if pReq, pErr := http.NewRequestWithContext(pCtx, http.MethodGet, pubURL, nil); pErr == nil {
			pReq.Header.Set("Authorization", "Bearer "+accessToken)
			pReq.Header.Set("Accept", "application/json")
			if pResp, doErr := httpClient.Do(pReq); doErr == nil {
				pubBody, _ := io.ReadAll(io.LimitReader(pResp.Body, 65536))
				pResp.Body.Close()
				publicSupported = pResp.StatusCode == http.StatusOK
				if pResp.StatusCode == http.StatusOK {
					var pubSub publicSubscriptionWire
					if json.Unmarshal(StripColabXSSI(pubBody), &pubSub) == nil {
						publicSubTier = extractWireTierString(pubSub.SubscriptionTier, pubSub.Tier, pubSub.ProTier)
					}
				}
			}
		}
		cancel()
	}

	now := s.now()

	mu.Lock()
	defer mu.Unlock()

	acc, err = s.st.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}

	if tunData != nil {
		acc.TunnelAPISupported = true
		acc.PaidCUBalance = tunData.CurrentBalance
		acc.LiveBurnRateCUHr = tunData.ConsumptionRateHourly
		acc.ActiveAssignments = tunData.AssignmentsCount
		if fq := parseFreeQuotaWire(tunData.FreeCCUQuotaInfo); fq != nil {
			acc.FreeQuota = fq
		}
		if !acc.CustomSubscriptionTier && (gapiData == nil || strings.TrimSpace(gapiData.SubscriptionTier) == "") {
			if publicSubTier != "" {
				acc.SubscriptionTier = mapSubscriptionTier(publicSubTier, acc.PaidCUBalance, acc.SubscriptionTier)
			} else if tunWireTier := extractWireTierString(tunData.SubscriptionTier, tunData.Tier, tunData.ProTier); tunWireTier != "" {
				acc.SubscriptionTier = mapSubscriptionTier(tunWireTier, acc.PaidCUBalance, acc.SubscriptionTier)
			} else {
				acc.SubscriptionTier = inferTierFromTunnel(acc, tunData)
			}
		}
		applyEligibleStrings(acc, append(tunData.EligibleGPUs, tunData.EligibleTPUs...), nil, now)
	}

	if gapiData != nil {
		if gapiData.PaidComputeUnitsBalance > 0 || tunData == nil {
			acc.PaidCUBalance = gapiData.PaidComputeUnitsBalance
		}
		if gapiData.ConsumptionRateHourly > 0 || tunData == nil {
			acc.LiveBurnRateCUHr = gapiData.ConsumptionRateHourly
		}
		if gapiData.AssignmentsCount > 0 || tunData == nil {
			acc.ActiveAssignments = gapiData.AssignmentsCount
		}
		if fq := parseFreeQuotaWire(gapiData.FreeCCUQuotaInfo); fq != nil {
			acc.FreeQuota = fq
		}
		if !acc.CustomSubscriptionTier {
			if strings.TrimSpace(gapiData.SubscriptionTier) != "" {
				acc.SubscriptionTier = mapSubscriptionTier(gapiData.SubscriptionTier, acc.PaidCUBalance, acc.SubscriptionTier)
			} else if publicSubTier != "" {
				acc.SubscriptionTier = mapSubscriptionTier(publicSubTier, acc.PaidCUBalance, acc.SubscriptionTier)
			} else if tunData == nil {
				acc.SubscriptionTier = mapSubscriptionTier(gapiData.SubscriptionTier, acc.PaidCUBalance, acc.SubscriptionTier)
			}
		}

		var elig, inelig []string
		for _, grp := range gapiData.EligibleAccelerators {
			elig = append(elig, grp.Models...)
		}
		for _, grp := range gapiData.IneligibleAccelerators {
			inelig = append(inelig, grp.Models...)
		}
		applyEligibleStrings(acc, elig, inelig, now)
	}

	acc.PublicAPISupported = publicSupported
	acc.LastPolledAt = &now
	acc.UpstreamAssignmentsCount = acc.ActiveAssignments
	acc.UpstreamBurnRateCUHr = acc.LiveBurnRateCUHr

	var activeForAcc []*models.RuntimeSession
	if sessions, sErr := s.st.ListSessions(ctx); sErr == nil {
		for _, sess := range sessions {
			if sess != nil && sess.AccountID == acc.ID && isActiveSessionStatus(sess.Status) {
				activeForAcc = append(activeForAcc, sess)
			}
		}
	}
	unadopted := acc.UpstreamAssignmentsCount - len(activeForAcc)
	if unadopted < 0 {
		unadopted = 0
	}
	acc.UpstreamUnadoptedCount = unadopted
	if len(activeForAcc) > acc.ActiveAssignments {
		acc.ActiveAssignments = len(activeForAcc)
	}

	// Empirical EWMA Burn Calibration: when exactly 1 runtime is assigned on this account,
	// attribute its live `consumptionRateHourly` to that active session's accelerator & shape.
	if acc.UpstreamAssignmentsCount == 1 && acc.LiveBurnRateCUHr > 0 && len(activeForAcc) == 1 {
		sess := activeForAcc[0]
		_ = s.st.RecordBurnSample(ctx, sess.Accelerator, sess.Shape, acc.LiveBurnRateCUHr)
		if acc.GPUEntitlements == nil {
			acc.GPUEntitlements = make(map[models.AcceleratorType]models.GPUEntitlement)
		}
		ent := acc.GPUEntitlements[sess.Accelerator]
		ent.Accelerator = sess.Accelerator
		if ent.Status == "" {
			ent.Status = models.EntitlementAvailable
		}
		ent.CalibratedBurnRate = acc.LiveBurnRateCUHr
		ent.CalibrationSource = models.CalibrationLive
		ent.VerifiedAt = &now
		acc.GPUEntitlements[sess.Accelerator] = ent
	}

	s.recomputeAccountDerivedFields(ctx, acc)
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		return nil, err
	}

	s.publish(models.WSEventAccountUpdated, acc)
	if summary, sErr := s.GetQuotaSummary(ctx); sErr == nil {
		s.publish(models.WSEventQuotaUpdated, summary)
	}
	return acc, nil
}

// RefreshAllQuotas concurrently refreshes quota telemetry across all enabled accounts
// and returns the updated aggregate QuotaSummary.
func (s *Service) RefreshAllQuotas(ctx context.Context) (*models.QuotaSummary, error) {
	accts, err := s.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, acc := range accts {
		if !acc.Enabled || acc.Health == models.HealthRevokedByUser {
			continue
		}
		if acc.EncryptedRefreshToken == "" && acc.EncryptedAccessToken == "" {
			continue
		}
		wg.Add(1)
		go func(accountID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, _ = s.RefreshAccountQuota(ctx, accountID)
		}(acc.ID)
	}
	wg.Wait()

	summary, err := s.GetQuotaSummary(ctx)
	if err != nil {
		return nil, err
	}
	s.publish(models.WSEventQuotaUpdated, summary)
	return summary, nil
}

// GetQuotaSummary computes the real-time multi-account QuotaSummary and 8-tier Cloud GPU
// Equivalent Runtime Matrix (`G4`, `H100`, `A100`, `L4`, `T4`, `V6E1`, `V5E1`, `CPU`).
func (s *Service) GetQuotaSummary(ctx context.Context) (*models.QuotaSummary, error) {
	accts, err := s.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	sessions, _ := s.st.ListSessions(ctx)
	waste, _ := s.st.GetSavedWasteStats(ctx)

	now := s.now()
	summary := &models.QuotaSummary{
		SavedIdleWasteCU:   round2(waste.SavedIdleWasteCU),
		AutoTeardownsCount: waste.AutoTeardownsCount,
		ZombieAbortsCount:  waste.ZombieAbortsCount,
		GPUEquivalence:     make(map[models.AcceleratorType]models.GPUHourEquivalence, len(models.HardwareCatalog)),
		UpdatedAt:          now,
	}

	// Resolve effective burn rate & calibration source per hardware tier.
	type resolvedRate struct {
		rate   float64
		source models.RateCalibrationSource
	}
	rates := make(map[models.AcceleratorType]resolvedRate, len(models.HardwareCatalog))
	for _, spec := range models.HardwareCatalog {
		r := resolvedRate{
			rate:   spec.DefaultBurnRateCUHr,
			source: models.CalibrationReference,
		}
		if calRate, ok, _ := s.st.GetCalibratedBurnRate(ctx, spec.Accelerator, models.EffectiveMachineShape(spec.Accelerator, true)); ok && calRate > 0 {
			r.rate = calRate
			r.source = models.CalibrationEmpirical
		}
		rates[spec.Accelerator] = r
	}

	// Count online VRAM from active tracked RuntimeSessions.
	activeSessionsByAcc := make(map[string]int)
	for _, sess := range sessions {
		if isActiveSessionStatus(sess.Status) {
			activeSessionsByAcc[sess.AccountID]++
			if spec, ok := models.GetHardwareSpec(sess.Accelerator); ok {
				summary.TotalOnlineVRAMGB += spec.VRAMGB
			}
			if sess.BurnRateCUHr > 0 {
				rates[sess.Accelerator] = resolvedRate{
					rate:   sess.BurnRateCUHr,
					source: models.CalibrationLive,
				}
			}
		}
	}

	for _, acc := range accts {
		s.recomputeAccountDerivedFields(ctx, acc)
		if acc.Health == models.HealthRevokedByUser {
			continue
		}
		summary.AccountsTotal++
		if acc.IsContributor {
			summary.AccountsContributor++
		}
		if acc.Enabled && acc.Health.IsSchedulable() {
			summary.AccountsHealthy++
		}
		activeCount := acc.ActiveAssignments
		if tracked := activeSessionsByAcc[acc.ID]; tracked > activeCount {
			activeCount = tracked
		}
		if activeCount > 0 {
			summary.AccountsWorking++
		}
		summary.ActiveVMs += activeCount
		if acc.Enabled && acc.Health.IsSchedulable() {
			summary.TotalVMSlots += effectiveMaxVMs(acc)
		}

		summary.TotalAvailableCU += acc.PaidCUBalance
		if acc.Enabled && acc.Health.IsSchedulable() {
			summary.SchedulableCU += acc.SchedulableCU
		}
		summary.ActiveBurnRateCUHr += acc.LiveBurnRateCUHr

		if acc.EarliestBatchExpiresAt != nil && acc.ExpiringSoonCU > 0 {
			if summary.EarliestBatchExpiresAt == nil || acc.EarliestBatchExpiresAt.Before(*summary.EarliestBatchExpiresAt) {
				expCopy := *acc.EarliestBatchExpiresAt
				summary.EarliestBatchExpiresAt = &expCopy
				summary.EarliestBatchExpiryCU = acc.ExpiringSoonCU
			} else if acc.EarliestBatchExpiresAt.Equal(*summary.EarliestBatchExpiresAt) {
				summary.EarliestBatchExpiryCU += acc.ExpiringSoonCU
			}
		}

		if acc.Enabled && acc.FreeQuota != nil && acc.FreeQuota.RemainingMilliCCU > 0 {
			summary.FreeT4WindowsReady++
		}
	}

	summary.TotalAvailableCU = round2(summary.TotalAvailableCU)
	summary.SchedulableCU = round2(summary.SchedulableCU)
	summary.ActiveBurnRateCUHr = round2(summary.ActiveBurnRateCUHr)
	summary.EstimatedValueUSD = round2(summary.TotalAvailableCU * usdPerComputeUnit)
	summary.EarliestBatchExpiryCU = round2(summary.EarliestBatchExpiryCU)

	if summary.ActiveBurnRateCUHr > 0 {
		eta := round2(summary.SchedulableCU / summary.ActiveBurnRateCUHr)
		summary.PoolDepletionETAHours = &eta
	}

	// Build 8-tier GPUHourEquivalence across eligible accounts.
	for _, spec := range models.HardwareCatalog {
		rr := rates[spec.Accelerator]
		burn := rr.rate
		if burn <= 0 {
			burn = spec.DefaultBurnRateCUHr
		}

		var (
			availCU          float64
			schedCU          float64
			eligibleAccounts int
			maxSlots         int
			freeWindows      int
		)

		for _, acc := range accts {
			if !acc.Enabled || acc.Health == models.HealthRevokedByUser {
				continue
			}
			if !isAccountEntitledToGPU(acc, spec.Accelerator, now) {
				continue
			}
			// Contributor GPU allowlist check
			if len(acc.Guardrails.AllowedGPUs) > 0 && !containsAccelerator(acc.Guardrails.AllowedGPUs, spec.Accelerator) {
				continue
			}

			freeCU := 0.0
			if spec.FreeTierEligible && acc.FreeQuota != nil && acc.FreeQuota.RemainingFreeCU > 0 {
				freeCU = acc.FreeQuota.RemainingFreeCU
				freeWindows++
			}

			// Free-tier accounts without paid balance are only eligible for FreeTierEligible accelerators.
			if acc.SubscriptionTier == models.TierFree && acc.PaidCUBalance <= 0 && !spec.FreeTierEligible {
				continue
			}

			eligibleAccounts++
			availCU += acc.PaidCUBalance + freeCU

			if ok, _ := s.CanScheduleAccount(acc, spec.Accelerator); ok {
				schedCU += acc.SchedulableCU + freeCU
				freeSlots := effectiveMaxVMs(acc) - acc.ActiveAssignments
				if freeSlots > 0 {
					maxSlots += freeSlots
				}
			}
		}

		var availHrs, schedHrs float64
		if burn > 0 {
			availHrs = round2(availCU / burn)
			schedHrs = round2(schedCU / burn)
		}

		summary.GPUEquivalence[spec.Accelerator] = models.GPUHourEquivalence{
			Accelerator:       spec.Accelerator,
			DisplayName:       spec.DisplayName,
			ShortLabel:        spec.ShortLabel,
			VRAMGB:            spec.VRAMGB,
			BurnRateCUHr:      round2(burn),
			CalibrationSource: rr.source,
			AvailableHours:    availHrs,
			SchedulableHours:  schedHrs,
			EligibleAccounts:  eligibleAccounts,
			MaxParallelSlots:  maxSlots,
			FreeWindowsReady:  freeWindows,
			Locked:            eligibleAccounts == 0,
		}
	}

	return summary, nil
}

func (s *Service) recomputeAccountDerivedFields(ctx context.Context, acc *models.Account) {
	if acc == nil {
		return
	}
	now := s.now()

	// Heal tunnel-polled accounts persisted earlier with FREE tier before RefreshAccountQuota re-runs.
	if !acc.CustomSubscriptionTier &&
		(acc.SubscriptionTier == "" || acc.SubscriptionTier == models.TierFree) &&
		acc.TunnelAPISupported && (acc.PaidCUBalance > 0 || acc.ActiveAssignments >= 2 || acc.LiveBurnRateCUHr > 0) {
		var eligGPUs []string
		for gpu, ent := range acc.GPUEntitlements {
			if ent.Status == models.EntitlementAvailable {
				eligGPUs = append(eligGPUs, string(gpu))
			}
		}
		acc.SubscriptionTier = inferTierFromTunnel(acc, &tunnelCCUInfoWire{
			CurrentBalance:        acc.PaidCUBalance,
			ConsumptionRateHourly: acc.LiveBurnRateCUHr,
			AssignmentsCount:      acc.ActiveAssignments,
			EligibleGPUs:          eligGPUs,
		})
	}

	// Ensure ReferenceCycleCU reflects standard cycle quota (2,500 CU for AI Ultra, 500 CU for Pro+, 100 CU for Pro/PAYG)
	// unless the user manually customized ReferenceCycleCU via PATCH /api/v1/accounts/{id}.
	if !acc.CustomCycleCU {
		std := TierStandardCycleCU(acc.SubscriptionTier)
		if acc.ReferenceCycleCU < std {
			acc.ReferenceCycleCU = std
		}
		if acc.PaidCUBalance > acc.ReferenceCycleCU {
			acc.ReferenceCycleCU = acc.PaidCUBalance
		}
	}

	tierDefault := tierDefaultMaxVMs(acc.SubscriptionTier)
	if !acc.IsContributor {
		if acc.MaxConcurrentVMs <= 0 || (acc.MaxConcurrentVMs == 1 && tierDefault > 1) {
			acc.MaxConcurrentVMs = tierDefault
		}
		if acc.ActiveAssignments > acc.MaxConcurrentVMs {
			acc.MaxConcurrentVMs = acc.ActiveAssignments
		}
	} else {
		if acc.MaxConcurrentVMs <= 0 {
			acc.MaxConcurrentVMs = tierDefault
		}
		if acc.Guardrails.MaxConcurrentVMs > 0 && acc.Guardrails.MaxConcurrentVMs < acc.MaxConcurrentVMs {
			acc.MaxConcurrentVMs = acc.Guardrails.MaxConcurrentVMs
		}
	}

	if acc.IsContributor {
		normalizeGuardrailsDailyUsage(&acc.Guardrails, now)
	}

	// Compute SchedulableCU respecting MinReserveCU and contributor total/daily donation caps.
	sched := acc.PaidCUBalance - acc.Guardrails.MinReserveCU
	if acc.IsContributor {
		if !acc.Guardrails.AllowPaidCUBurn {
			sched = 0
		} else {
			if acc.Guardrails.MaxDonatedCUTotal > 0 {
				remTotal := acc.Guardrails.MaxDonatedCUTotal - acc.Guardrails.DonatedCUUsedTotal
				if remTotal < sched {
					sched = remTotal
				}
			}
			if acc.Guardrails.MaxDonatedCUDaily > 0 {
				remDaily := acc.Guardrails.MaxDonatedCUDaily - acc.Guardrails.DonatedCUUsedToday
				if remDaily < sched {
					sched = remDaily
				}
			}
		}
	}
	if sched < 0 || acc.Health == models.HealthRevokedByUser {
		sched = 0
	}
	acc.SchedulableCU = round2(sched)

	// Automatically lift expired account-level cooldowns.
	if acc.CooldownUntil != nil && !now.Before(*acc.CooldownUntil) {
		acc.CooldownUntil = nil
		switch acc.Health {
		case models.HealthCooldownRateLimit, models.HealthMaxConcurrency, models.HealthProxyError:
			acc.Health = models.HealthHealthy
			acc.HealthReason = ""
		case models.HealthAuthExpired:
			if acc.EncryptedRefreshToken != "" || acc.EncryptedAccessToken != "" {
				acc.Health = models.HealthHealthy
				acc.HealthReason = ""
			}
		}
	}

	// Synchronize non-terminal Health state with live telemetry & guardrails.
	hasActiveCooldown := acc.CooldownUntil != nil && now.Before(*acc.CooldownUntil)
	switch {
	case acc.Health == models.HealthAuthExpired ||
		acc.Health == models.HealthRevokedByUser ||
		acc.Health == models.HealthQuarantinedDenylist ||
		acc.Health == models.HealthProxyError ||
		acc.Health == models.HealthCooldownRateLimit:
		// Preserve terminal/error state until refreshed or cleared.
	case acc.Health == models.HealthMaxConcurrency && hasActiveCooldown:
		// Preserve HTTP 412 upstream concurrency cooldown while CooldownUntil is in the future.
	default:
		isFreeNoPaid := acc.SubscriptionTier == models.TierFree && acc.PaidCUBalance == 0
		if !acc.Enabled || acc.Guardrails.Paused {
			acc.Health = models.HealthPausedManual
			if acc.HealthReason == "" {
				acc.HealthReason = "Paused"
			}
		} else if acc.IsContributor && acc.Guardrails.ExpiresAt != nil && !now.Before(*acc.Guardrails.ExpiresAt) {
			acc.Health = models.HealthPausedCapReached
			acc.HealthReason = "Contribution window expired"
		} else if acc.Guardrails.ScheduleWindow != nil && acc.Guardrails.ScheduleWindow.Enabled && !isWithinScheduleWindow(acc.Guardrails.ScheduleWindow, now) {
			acc.Health = models.HealthPausedScheduleWindow
			acc.HealthReason = "Outside contributor active schedule window"
		} else if acc.IsContributor && acc.Guardrails.AllowPaidCUBurn && acc.SchedulableCU <= 0 && (acc.FreeQuota == nil || acc.FreeQuota.RemainingMilliCCU <= 0) && !isFreeNoPaid {
			acc.Health = models.HealthPausedCapReached
			acc.HealthReason = "Reached contributor CU reserve floor or donation cap"
		} else if acc.ActiveAssignments >= effectiveMaxVMs(acc) && acc.ActiveAssignments > 0 {
			acc.Health = models.HealthMaxConcurrency
			acc.HealthReason = "At maximum concurrent VM assignments"
		} else if acc.ActiveAssignments > 0 {
			acc.Health = models.HealthActiveWorking
			acc.HealthReason = ""
		} else if acc.SchedulableCU <= 0 {
			acc.Health = models.HealthQuotaLowFreeOnly
			acc.HealthReason = "Free dynamic quota window only"
		} else {
			acc.Health = models.HealthHealthy
			acc.HealthReason = ""
		}
	}

	// Ensure default GPU entitlements map exists.
	if len(acc.GPUEntitlements) == 0 {
		acc.GPUEntitlements = defaultEntitlementsForTier(acc.SubscriptionTier)
	}

	// Populate per-account GPUEquivalence map.
	acc.GPUEquivalence = make(map[models.AcceleratorType]models.GPUHourEquivalence, len(models.HardwareCatalog))
	for _, spec := range models.HardwareCatalog {
		burn := spec.DefaultBurnRateCUHr
		src := models.CalibrationReference
		if ent, ok := acc.GPUEntitlements[spec.Accelerator]; ok && ent.CalibratedBurnRate > 0 {
			burn = ent.CalibratedBurnRate
			src = ent.CalibrationSource
			if src == "" {
				src = models.CalibrationEmpirical
			}
		} else if ctx != nil && s.st != nil {
			if cal, ok, _ := s.st.GetCalibratedBurnRate(ctx, spec.Accelerator, models.EffectiveMachineShape(spec.Accelerator, true)); ok && cal > 0 {
				burn = cal
				src = models.CalibrationEmpirical
			}
		}
		entitled := isAccountEntitledToGPU(acc, spec.Accelerator, now)
		if len(acc.Guardrails.AllowedGPUs) > 0 && !containsAccelerator(acc.Guardrails.AllowedGPUs, spec.Accelerator) {
			entitled = false
		}
		var availHrs, schedHrs float64
		if entitled && burn > 0 {
			freeCU := 0.0
			if spec.FreeTierEligible && acc.FreeQuota != nil {
				freeCU = acc.FreeQuota.RemainingFreeCU
			}
			availHrs = round2((acc.PaidCUBalance + freeCU) / burn)
			schedHrs = round2((acc.SchedulableCU + freeCU) / burn)
		}
		acc.GPUEquivalence[spec.Accelerator] = models.GPUHourEquivalence{
			Accelerator:       spec.Accelerator,
			DisplayName:       spec.DisplayName,
			ShortLabel:        spec.ShortLabel,
			VRAMGB:            spec.VRAMGB,
			BurnRateCUHr:      round2(burn),
			CalibrationSource: src,
			AvailableHours:    availHrs,
			SchedulableHours:  schedHrs,
			EligibleAccounts:  boolCount(entitled),
			Locked:            !entitled,
		}
	}
}

func tierDefaultMaxVMs(tier models.SubscriptionTier) int {
	switch tier {
	case models.TierAIUltra, models.TierProPlus:
		return 5
	case models.TierPro, models.TierPAYG:
		return 2
	default:
		return 1
	}
}

func extractWireTierString(subTier string, altTier string, proTier any) string {
	if s := strings.TrimSpace(subTier); s != "" {
		return s
	}
	if s := strings.TrimSpace(altTier); s != "" {
		return s
	}
	switch v := proTier.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		switch int(v) {
		case 3:
			return "AI_ULTRA"
		case 2:
			return "PRO_PLUS"
		case 1:
			return "PRO"
		}
	case int:
		switch v {
		case 3:
			return "AI_ULTRA"
		case 2:
			return "PRO_PLUS"
		case 1:
			return "PRO"
		}
	case map[string]any:
		for _, k := range []string{"subscriptionTier", "tier", "name"} {
			if raw, ok := v[k].(string); ok && strings.TrimSpace(raw) != "" {
				return strings.TrimSpace(raw)
			}
		}
	}
	return ""
}

func inferTierFromTunnel(acc *models.Account, tun *tunnelCCUInfoWire) models.SubscriptionTier {
	if tun == nil {
		if acc != nil && acc.SubscriptionTier != "" {
			return acc.SubscriptionTier
		}
		return models.TierFree
	}

	var paidBalance float64
	var burnRate float64
	var current models.SubscriptionTier
	if acc != nil {
		paidBalance = acc.PaidCUBalance
		burnRate = acc.LiveBurnRateCUHr
		current = acc.SubscriptionTier
	}
	if paidBalance <= 0 && tun.CurrentBalance > 0 {
		paidBalance = tun.CurrentBalance
	}
	if tun.ConsumptionRateHourly > 0 {
		burnRate = tun.ConsumptionRateHourly
	}

	if wireTier := extractWireTierString(tun.SubscriptionTier, tun.Tier, tun.ProTier); wireTier != "" {
		return mapSubscriptionTier(wireTier, paidBalance, current)
	}

	hasFlagshipGPU := false
	for _, raw := range tun.EligibleGPUs {
		if norm, err := models.NormalizeAccelerator(raw); err == nil {
			if norm == models.AcceleratorG4 || norm == models.AcceleratorH100 {
				hasFlagshipGPU = true
				break
			}
		}
	}
	// Live burn rate signal: G4/A100/H100 burn >= 6.7 CU/hr per VM (e.g. 3 x G4 == ~28.98 CU/hr).
	if burnRate >= 6.5 || (tun.AssignmentsCount > 0 && burnRate/float64(tun.AssignmentsCount) >= 6.5) {
		hasFlagshipGPU = true
	}

	switch {
	case paidBalance > 500 ||
		(hasFlagshipGPU && paidBalance >= 250 && tun.AssignmentsCount >= 3) ||
		(burnRate >= 25.0 && (paidBalance >= 200 || tun.AssignmentsCount >= 3)):
		return models.TierAIUltra
	case paidBalance >= 120 || tun.AssignmentsCount >= 3 || burnRate >= 6.5 || (hasFlagshipGPU && paidBalance > 0):
		if current == models.TierAIUltra {
			return models.TierAIUltra
		}
		return models.TierProPlus
	case paidBalance > 0 || tun.AssignmentsCount >= 2 || burnRate > 0:
		if current == models.TierAIUltra || current == models.TierProPlus {
			return current
		}
		return models.TierPro
	default:
		return models.TierFree
	}
}

func mapSubscriptionTier(wireTier string, paidBalance float64, current models.SubscriptionTier) models.SubscriptionTier {
	switch strings.ToUpper(strings.TrimSpace(wireTier)) {
	case "SUBSCRIPTION_TIER_AI_ULTRA", "AI_ULTRA":
		return models.TierAIUltra
	case "SUBSCRIPTION_TIER_PRO_PLUS", "PRO_PLUS":
		if current == models.TierAIUltra || paidBalance >= 1000 {
			return models.TierAIUltra
		}
		return models.TierProPlus
	case "SUBSCRIPTION_TIER_PRO", "PRO":
		return models.TierPro
	case "SUBSCRIPTION_TIER_NONE", "FREE", "":
		if paidBalance > 0 {
			return models.TierPAYG
		}
		return models.TierFree
	default:
		if current != "" {
			return current
		}
		return models.TierFree
	}
}

func defaultEntitlementsForTier(tier models.SubscriptionTier) map[models.AcceleratorType]models.GPUEntitlement {
	m := make(map[models.AcceleratorType]models.GPUEntitlement, len(models.AllAccelerators))
	for _, acc := range models.AllAccelerators {
		status := models.EntitlementAvailable
		if tier == models.TierFree && acc != models.AcceleratorT4 && acc != models.AcceleratorCPU {
			status = models.EntitlementTierRestricted
		}
		if tier == models.TierPro && acc == models.AcceleratorH100 {
			status = models.EntitlementTierRestricted
		}
		m[acc] = models.GPUEntitlement{
			Accelerator: acc,
			Status:      status,
		}
	}
	return m
}

func applyEligibleStrings(acc *models.Account, eligible []string, ineligible []string, now time.Time) {
	if len(eligible) == 0 && len(ineligible) == 0 {
		return
	}
	if acc.GPUEntitlements == nil {
		acc.GPUEntitlements = make(map[models.AcceleratorType]models.GPUEntitlement)
	}
	eligSet := make(map[models.AcceleratorType]bool)
	hasAnyGPU := false
	hasAnyTPU := false
	for _, raw := range eligible {
		if norm, err := models.NormalizeAccelerator(raw); err == nil {
			eligSet[norm] = true
			if spec, ok := models.GetHardwareSpec(norm); ok {
				switch spec.Variant {
				case models.VariantGPU:
					hasAnyGPU = true
				case models.VariantTPU:
					hasAnyTPU = true
				}
			}
		}
	}
	ineligSet := make(map[models.AcceleratorType]bool)
	for _, raw := range ineligible {
		if norm, err := models.NormalizeAccelerator(raw); err == nil {
			ineligSet[norm] = true
		}
	}

	// CPU is always available on every Colab account.
	eligSet[models.AcceleratorCPU] = true

	for _, tier := range models.AllAccelerators {
		prev := acc.GPUEntitlements[tier]
		prev.Accelerator = tier
		// Keep active temporary cooldowns if still in the future.
		if (prev.Status == models.EntitlementCooldown || prev.Status == models.EntitlementTierRestricted) &&
			prev.CooldownUntil != nil && now.Before(*prev.CooldownUntil) {
			acc.GPUEntitlements[tier] = prev
			continue
		}
		spec, _ := models.GetHardwareSpec(tier)
		if ineligSet[tier] {
			prev.Status = models.EntitlementIneligible
			prev.VerifiedAt = &now
		} else if eligSet[tier] {
			prev.Status = models.EntitlementAvailable
			prev.CooldownUntil = nil
			prev.VerifiedAt = &now
		} else if spec.Variant == models.VariantGPU && hasAnyGPU {
			prev.Status = models.EntitlementIneligible
			prev.VerifiedAt = &now
		} else if spec.Variant == models.VariantTPU && (hasAnyTPU || acc.SubscriptionTier == models.TierFree) {
			if acc.SubscriptionTier == models.TierFree {
				prev.Status = models.EntitlementTierRestricted
			} else {
				prev.Status = models.EntitlementIneligible
			}
			prev.VerifiedAt = &now
		} else if prev.Status == "" {
			if acc.SubscriptionTier == models.TierFree && !spec.FreeTierEligible {
				prev.Status = models.EntitlementTierRestricted
			} else {
				prev.Status = models.EntitlementAvailable
			}
		}
		acc.GPUEntitlements[tier] = prev
	}
}

func isAccountEntitledToGPU(acc *models.Account, gpu models.AcceleratorType, now time.Time) bool {
	if gpu == models.AcceleratorCPU {
		return true
	}
	if acc.GPUEntitlements == nil {
		return true
	}
	ent, ok := acc.GPUEntitlements[gpu]
	if !ok {
		return true
	}
	switch ent.Status {
	case models.EntitlementAvailable, models.EntitlementUnknown, "":
		return true
	case models.EntitlementCooldown, models.EntitlementTierRestricted:
		if ent.CooldownUntil != nil && !now.Before(*ent.CooldownUntil) {
			return true
		}
		return false
	case models.EntitlementIneligible:
		return false
	default:
		return true
	}
}

func isActiveSessionStatus(st models.SessionStatus) bool {
	switch st {
	case models.SessionProvisioning, models.SessionIdle, models.SessionBusy, models.SessionReconnecting, models.SessionDraining:
		return true
	default:
		return false
	}
}

func effectiveMaxVMs(acc *models.Account) int {
	maxVMs := acc.MaxConcurrentVMs
	if maxVMs <= 0 {
		maxVMs = tierDefaultMaxVMs(acc.SubscriptionTier)
	}
	if acc.IsContributor && acc.Guardrails.MaxConcurrentVMs > 0 && acc.Guardrails.MaxConcurrentVMs < maxVMs {
		maxVMs = acc.Guardrails.MaxConcurrentVMs
	}
	return maxVMs
}

func containsAccelerator(list []models.AcceleratorType, target models.AcceleratorType) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}
