package pool

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/colabhive/colabhive/internal/models"
)

// CanScheduleAccount enforces account health, cooldowns, concurrency caps, GPU entitlements,
// reserve CU floors (requiring >= max(2.0 CU, 15m of burn) above floor), allowed GPUs, and schedule windows.
func (s *Service) CanScheduleAccount(acc *models.Account, requested models.AcceleratorType) (allowed bool, reason string) {
	if acc == nil {
		return false, "nil account"
	}
	if acc.EffectiveProvider() != models.ProviderColab {
		return false, fmt.Sprintf("provider %s is not supported for interactive Colab sessions", acc.EffectiveProvider())
	}
	now := s.now()

	if !acc.Enabled {
		return false, "account is disabled"
	}
	if acc.Guardrails.Paused {
		return false, "account scheduling is paused by guardrails"
	}
	if acc.CooldownUntil != nil && now.Before(*acc.CooldownUntil) {
		return false, fmt.Sprintf("account in rate-limit cooldown until %s", acc.CooldownUntil.Format(time.RFC3339))
	}

	// Reject non-schedulable terminal/error states (allowing expired cooldowns to pass).
	switch acc.Health {
	case models.HealthAuthExpired, models.HealthRevokedByUser, models.HealthQuarantinedDenylist, models.HealthProxyError, models.HealthPausedManual:
		return false, fmt.Sprintf("account health is %s", acc.Health)
	case models.HealthCooldownRateLimit, models.HealthMaxConcurrency:
		if acc.CooldownUntil != nil && now.Before(*acc.CooldownUntil) {
			return false, fmt.Sprintf("account in %s cooldown", acc.Health)
		}
		if acc.Health == models.HealthCooldownRateLimit && acc.CooldownUntil == nil {
			return false, "account in rate-limit cooldown"
		}
	}

	if acc.Guardrails.ExpiresAt != nil && !now.Before(*acc.Guardrails.ExpiresAt) {
		return false, "account authorization has expired"
	}

	if acc.Guardrails.ScheduleWindow != nil && acc.Guardrails.ScheduleWindow.Enabled {
		if !isWithinScheduleWindow(acc.Guardrails.ScheduleWindow, now) {
			return false, "outside account active schedule window"
		}
	}

	if len(acc.Guardrails.AllowedGPUs) > 0 && !containsAccelerator(acc.Guardrails.AllowedGPUs, requested) {
		return false, fmt.Sprintf("accelerator %s is not permitted by account guardrails", requested)
	}

	maxVMs := effectiveMaxVMs(acc)
	if acc.ActiveAssignments >= maxVMs {
		return false, fmt.Sprintf("max concurrent VMs reached (%d/%d)", acc.ActiveAssignments, maxVMs)
	}

	if !isAccountEntitledToGPU(acc, requested, now) {
		return false, fmt.Sprintf("account lacks entitlement or is in cooldown for %s", requested)
	}

	spec, ok := models.GetHardwareSpec(requested)
	if !ok {
		return false, fmt.Sprintf("unknown accelerator %s", requested)
	}

	burnRate := spec.DefaultBurnRateCUHr
	if ent, eOK := acc.GPUEntitlements[requested]; eOK && ent.CalibratedBurnRate > 0 {
		burnRate = ent.CalibratedBurnRate
	}

	hasFreeWindow := spec.FreeTierEligible &&
		((acc.FreeQuota != nil && (acc.FreeQuota.RemainingMilliCCU > 0 || acc.FreeQuota.RemainingFreeCU > 0)) ||
			(acc.SubscriptionTier == models.TierFree && acc.PaidCUBalance == 0))

	availCU := acc.PaidCUBalance - acc.Guardrails.MinReserveCU

	// Enforce at least max(2.0 CU, 15 minutes of burn) runway above MinReserveCU.
	minRunway := math.Max(2.0, burnRate*0.25)
	if burnRate == 0 {
		minRunway = 0
	}

	if availCU < minRunway {
		if hasFreeWindow {
			return true, "ok (free quota window)"
		}
		return false, fmt.Sprintf("insufficient schedulable CU above reserve floor (available %.2f CU < required %.2f CU)", availCU, minRunway)
	}

	return true, "ok"
}

func effectiveGuardrailsDailyUsed(g models.ContributorGuardrails, now time.Time) float64 {
	today := now.UTC().Format("2006-01-02")
	if g.DailyUsageDate != "" && g.DailyUsageDate != today {
		return 0
	}
	return g.DonatedCUUsedToday
}

func normalizeGuardrailsDailyUsage(g *models.ContributorGuardrails, now time.Time) bool {
	if g == nil {
		return false
	}
	today := now.UTC().Format("2006-01-02")
	if g.DailyUsageDate == "" {
		g.DailyUsageDate = today
		return true
	}
	if g.DailyUsageDate != today {
		g.DonatedCUUsedToday = 0
		g.DailyUsageDate = today
		return true
	}
	return false
}

func isWithinScheduleWindow(win *models.ScheduleWindow, utcNow time.Time) bool {
	if win == nil || !win.Enabled {
		return true
	}
	loc := resolveLocationWithFallback(win.Timezone)
	local := utcNow.In(loc)

	startMin, okStart := parseHHMM(win.StartHour)
	endMin, okEnd := parseHHMM(win.EndHour)
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

	if !okStart || !okEnd || startMin == endMin {
		return dayAllowed(local.Weekday())
	}

	if startMin < endMin {
		return dayAllowed(local.Weekday()) && curMin >= startMin && curMin < endMin
	}

	if curMin >= startMin {
		return dayAllowed(local.Weekday())
	}
	if curMin < endMin {
		prevDay := (local.Weekday() + 6) % 7
		return dayAllowed(prevDay)
	}
	return false
}

func parseHHMM(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func resolveLocationWithFallback(tz string) *time.Location {
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
