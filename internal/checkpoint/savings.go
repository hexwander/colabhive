package checkpoint

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

const (
	benchmarkLargeGPUUSDPerHr = 2.20 // 80-96GB GPU (G4 / H100 / A100) Vast.ai / RunPod 2026 market avg
	benchmarkMidGPUUSDPerHr   = 0.60 // 24-32GB GPU (L4 / TPU v6e-1) Vast.ai / RunPod 2026 market avg
	benchmarkT4GPUUSDPerHr    = 0.25 // 16GB T4 Vast.ai / RunPod 2026 market avg
	usdToCNYRate              = 7.20
	g4ReferenceBurnCUHr       = 9.66
	t4ReferenceBurnCUHr       = 1.96
)

// ComputeSavingsReceipt aggregates real telemetry across the account pool, saved waste ledger,
// sessions, sharded experiments, and checkpoint relays to build a 100% PII-redacted SavingsReceiptStats.
func ComputeSavingsReceipt(
	ctx context.Context,
	st models.Store,
	pool models.AccountPoolService,
	rt models.RuntimeEngine,
	ckpt *Manager,
) (*models.SavingsReceiptStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	var accounts []*models.Account
	if pool != nil {
		accts, err := pool.ListAccounts(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute savings receipt: list pool accounts: %w", err)
		}
		accounts = accts
	} else if st != nil {
		accts, err := st.ListAccounts(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute savings receipt: list store accounts: %w", err)
		}
		accounts = accts
	}

	var summary *models.QuotaSummary
	if pool != nil {
		s, err := pool.GetQuotaSummary(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute savings receipt: get quota summary: %w", err)
		}
		summary = s
	}

	var wasteStats models.SavedWasteStats
	if st != nil {
		ws, err := st.GetSavedWasteStats(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute savings receipt: get saved waste stats: %w", err)
		}
		wasteStats = ws
	}
	if wasteStats.SavedIdleWasteCU == 0 && summary != nil {
		wasteStats.SavedIdleWasteCU = summary.SavedIdleWasteCU
		wasteStats.AutoTeardownsCount = summary.AutoTeardownsCount
		wasteStats.ZombieAbortsCount = summary.ZombieAbortsCount
	}

	var sessions []*models.RuntimeSession
	if rt != nil {
		list, err := rt.ListSessions(ctx, models.SessionFilter{})
		if err != nil {
			return nil, fmt.Errorf("compute savings receipt: list runtime sessions: %w", err)
		}
		sessions = list
	}
	if st != nil {
		stList, err := st.ListSessions(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute savings receipt: list store sessions: %w", err)
		}
		if len(sessions) == 0 {
			sessions = stList
		} else if len(stList) > 0 {
			seenSess := make(map[string]struct{}, len(sessions))
			for _, s := range sessions {
				if s != nil {
					seenSess[s.ID] = struct{}{}
				}
			}
			for _, s := range stList {
				if s != nil {
					if _, dup := seenSess[s.ID]; !dup {
						seenSess[s.ID] = struct{}{}
						sessions = append(sessions, s)
					}
				}
			}
		}
	}

	var experiments []*models.Experiment
	if rt != nil {
		exps, err := rt.ListExperiments(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute savings receipt: list runtime experiments: %w", err)
		}
		experiments = exps
	}
	if st != nil {
		stExps, err := st.ListExperiments(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute savings receipt: list store experiments: %w", err)
		}
		if len(experiments) == 0 {
			experiments = stExps
		} else if len(stExps) > 0 {
			seenExp := make(map[string]struct{}, len(experiments))
			for _, e := range experiments {
				if e != nil {
					seenExp[e.ID] = struct{}{}
				}
			}
			for _, e := range stExps {
				if e != nil {
					if _, dup := seenExp[e.ID]; !dup {
						seenExp[e.ID] = struct{}{}
						experiments = append(experiments, e)
					}
				}
			}
		}
	}

	poolTitle := "ColabHive 私人聚合算力池"

	accountByID := make(map[string]*models.Account, len(accounts))
	providerSeen := make(map[models.ProviderType]bool)
	var cycleBurnedCU float64
	var donatedFreeCU float64
	gpuSeenWeight := make(map[models.AcceleratorType]float64)
	colabAccountCount := 0

	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		accountByID[acc.ID] = acc
		prov := acc.EffectiveProvider()
		if prov != models.ProviderColab {
			continue
		}
		colabAccountCount++
		providerSeen[prov] = true

		if acc.ReferenceCycleCU > acc.PaidCUBalance && acc.ReferenceCycleCU > 0 {
			cycleBurnedCU += acc.ReferenceCycleCU - acc.PaidCUBalance
		}
		if acc.IsContributor && acc.Guardrails.DonatedCUUsedTotal > 0 {
			donatedFreeCU += acc.Guardrails.DonatedCUUsedTotal
		}
		if acc.SubscriptionTier == models.TierAIUltra || acc.SubscriptionTier == models.TierProPlus {
			gpuSeenWeight[models.AcceleratorG4] += 0.25
		} else if acc.SubscriptionTier == models.TierFree {
			gpuSeenWeight[models.AcceleratorT4] += 0.25
		}
	}

	hoursByGPU := make(map[models.AcceleratorType]float64)
	var freeSessionHours float64
	var sessionBurnedCU float64
	countedExpSessions := make(map[string]bool)

	now := time.Now().UTC()
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		if sess.Accelerator == models.AcceleratorCPU {
			// CPU-only sessions MUST NOT be counted toward GPU savings hours or free GPU harvest.
			continue
		}
		hrs := float64(sess.UptimeSec) / 3600.0
		if hrs <= 0 && sess.AccumulatedCU > 0 && sess.BurnRateCUHr > 0 {
			hrs = sess.AccumulatedCU / sess.BurnRateCUHr
		}
		if hrs <= 0 && !sess.CreatedAt.IsZero() && (sess.Status == models.SessionIdle || sess.Status == models.SessionBusy) {
			elapsed := now.Sub(sess.CreatedAt).Hours()
			if elapsed > 0 && elapsed < 72 {
				hrs = elapsed
			}
		}
		if hrs < 0 {
			hrs = 0
		}

		gpu := sess.Accelerator
		if gpu == "" {
			gpu = models.AcceleratorT4
		}
		hoursByGPU[gpu] += hrs
		gpuSeenWeight[gpu] += hrs + 1.0
		sessionBurnedCU += sess.AccumulatedCU
		if sess.ExperimentID != "" {
			countedExpSessions[sess.ID] = true
		}

		acc := accountByID[sess.AccountID]
		isFree := sess.BurnRateCUHr == 0 ||
			(acc != nil && (acc.SubscriptionTier == models.TierFree || acc.IsContributor))
		if isFree {
			freeSessionHours += hrs
		}
	}

	var expExtraBurnedCU float64
	for _, exp := range experiments {
		if exp == nil {
			continue
		}
		gpu := exp.Accelerator
		if gpu == "" {
			gpu = models.AcceleratorG4
		}
		if gpu != models.AcceleratorCPU {
			gpuSeenWeight[gpu] += 2.0
		}

		var shardUnseenCU float64
		for _, sh := range exp.Shards {
			sGPU := sh.Accelerator
			if sGPU == "" {
				sGPU = gpu
			}
			if sGPU == models.AcceleratorCPU {
				continue
			}
			gpuSeenWeight[sGPU] += 1.0
			if sh.SessionID != "" && countedExpSessions[sh.SessionID] {
				continue
			}
			shardUnseenCU += sh.CUBurned
			burnRef := referenceBurnRate(sGPU)
			if sh.CUBurned > 0 && burnRef > 0 {
				shHrs := sh.CUBurned / burnRef
				hoursByGPU[sGPU] += shHrs
				acc := accountByID[sh.AccountID]
				if acc != nil && (acc.SubscriptionTier == models.TierFree || acc.IsContributor) {
					freeSessionHours += shHrs
				}
			}
		}
		if len(exp.Shards) == 0 && exp.CUBurnedSoFar > 0 && gpu != models.AcceleratorCPU {
			shardUnseenCU += exp.CUBurnedSoFar
			hoursByGPU[gpu] += exp.CUBurnedSoFar / referenceBurnRate(gpu)
		}
		expExtraBurnedCU += shardUnseenCU
	}

	ledgerBurnedCU := sessionBurnedCU + expExtraBurnedCU
	totalCUBurned := math.Max(cycleBurnedCU, ledgerBurnedCU)

	// Attribute any historical cycle CU burned prior to current in-memory sessions to GPU hours.
	if totalCUBurned > ledgerBurnedCU {
		historicalCU := totalCUBurned - ledgerBurnedCU
		hasLargeTier := false
		for _, acc := range accounts {
			if acc != nil && (acc.SubscriptionTier == models.TierAIUltra || acc.SubscriptionTier == models.TierProPlus) {
				hasLargeTier = true
				break
			}
		}
		if hasLargeTier {
			hoursByGPU[models.AcceleratorG4] += historicalCU / g4ReferenceBurnCUHr
			gpuSeenWeight[models.AcceleratorG4] += historicalCU / g4ReferenceBurnCUHr
		} else {
			hoursByGPU[models.AcceleratorT4] += historicalCU / t4ReferenceBurnCUHr
			gpuSeenWeight[models.AcceleratorT4] += historicalCU / t4ReferenceBurnCUHr
		}
	}

	donatedFreeHours := donatedFreeCU / g4ReferenceBurnCUHr
	freeGPUHoursHarvested := freeSessionHours
	if donatedFreeHours > freeSessionHours {
		freeGPUHoursHarvested += (donatedFreeHours - freeSessionHours)
	}

	largeVRAMHours := hoursByGPU[models.AcceleratorG4] + hoursByGPU[models.AcceleratorH100] + hoursByGPU[models.AcceleratorA100]
	midVRAMHours := hoursByGPU[models.AcceleratorL4] + hoursByGPU[models.AcceleratorV6E1]
	// Strictly exclude CPU sessions from GPU savings hours.
	t4AndOtherHours := hoursByGPU[models.AcceleratorT4] + hoursByGPU[models.AcceleratorV5E1]

	totalGPUHours := largeVRAMHours + midVRAMHours + t4AndOtherHours
	if freeGPUHoursHarvested > totalGPUHours {
		totalGPUHours = freeGPUHoursHarvested
	}

	savedWasteG4Hours := wasteStats.SavedIdleWasteCU / g4ReferenceBurnCUHr
	g4EquivalentHours := largeVRAMHours +
		(midVRAMHours * (benchmarkMidGPUUSDPerHr / benchmarkLargeGPUUSDPerHr)) +
		(freeGPUHoursHarvested * (benchmarkT4GPUUSDPerHr / benchmarkLargeGPUUSDPerHr)) +
		savedWasteG4Hours

	var autoResumeRelays int
	if ckpt != nil {
		if lErr := ckpt.LedgerError(); lErr != nil {
			return nil, fmt.Errorf("load checkpoint ledger for savings receipt: %w", lErr)
		}
		for _, rec := range ckpt.ListCheckpoints() {
			if rec != nil && rec.RelayCount > 0 {
				autoResumeRelays += rec.RelayCount
			}
		}
	}

	// Equivalent cloud GPU rental value (Vast.ai / RunPod 2026 average benchmark):
	// - $2.20/hr for 80-96GB GPUs (G4 / H100 / A100)
	// - $0.60/hr for 24-32GB GPUs (L4 / TPU v6e-1)
	// - $0.25/hr for 16GB T4
	// - Plus rescued idle waste CU & preemption checkpoint relays
	paidT4Hours := math.Max(0, t4AndOtherHours-freeSessionHours)
	estimatedSavedUSD := (freeGPUHoursHarvested * benchmarkT4GPUUSDPerHr) +
		(largeVRAMHours * benchmarkLargeGPUUSDPerHr) +
		(midVRAMHours * benchmarkMidGPUUSDPerHr) +
		(paidT4Hours * benchmarkT4GPUUSDPerHr) +
		(savedWasteG4Hours * benchmarkLargeGPUUSDPerHr) +
		(float64(autoResumeRelays) * 1.5 * benchmarkT4GPUUSDPerHr)

	estimatedSavedUSD = round2(estimatedSavedUSD)
	estimatedSavedCNY := round2(estimatedSavedUSD * usdToCNYRate)

	// Honest efficiency computed strictly from measured values (0 when no compute usage yet;
	// no synthetic floor and no fabricated default percentage).
	var wasteZeroEfficiency float64
	measuredSavedUnits := wasteStats.SavedIdleWasteCU + (freeGPUHoursHarvested * t4ReferenceBurnCUHr)
	totalManagedUnits := totalCUBurned + measuredSavedUnits
	if totalManagedUnits > 0 {
		rawPct := (measuredSavedUnits / totalManagedUnits) * 100.0
		wasteZeroEfficiency = round1(math.Min(100.0, math.Max(0.0, rawPct)))
	}

	providersConnected := buildProviderDisplayList(providerSeen)
	topGPUs := buildTopGPUDisplayList(gpuSeenWeight)

	var rankBadgeZh, rankBadgeEn string
	if freeGPUHoursHarvested >= 5.0 {
		rankBadgeZh = "🏆 算力白嫖宗师"
		rankBadgeEn = "🏆 Free Compute Grandmaster"
	} else if totalGPUHours >= 10.0 || estimatedSavedUSD >= 40.0 {
		rankBadgeZh = "⚡ 多号算力达人"
		rankBadgeEn = "⚡ Multi-GPU Power User"
	} else {
		rankBadgeZh = "⚡ 多号调度极客"
		rankBadgeEn = "⚡ Multi-Pool Dispatch Geek"
	}

	providersLabelZh := "Google Colab"
	providersLabelEn := "Google Colab"
	if len(providersConnected) > 0 {
		providersLabelZh = strings.Join(providersConnected, " + ")
		providersLabelEn = providersLabelZh
	}

	var shareCopyZh, shareCopyEn string
	if freeGPUHoursHarvested > 0 {
		shareCopyZh = fmt.Sprintf(
			"🐝 用 ColabHive 把 %d 个账号（%s）聚合成私人云算力池！累计运行 %.1f 小时显卡（含 %.1f 小时免费卡），等效云端租卡价值约 ¥%.0f（$%.0f，按 Vast.ai/RunPod 均价）！ %s · github.com/hexwander/colabhive",
			colabAccountCount,
			providersLabelZh,
			round1(totalGPUHours),
			round1(freeGPUHoursHarvested),
			math.Round(estimatedSavedCNY),
			math.Round(estimatedSavedUSD),
			rankBadgeZh,
		)
		shareCopyEn = fmt.Sprintf(
			"🐝 Pooled %d accounts (%s) into a private GPU cluster with ColabHive! %.1fh total GPU runtime (%.1fh free tier), equivalent to ~$%.0f (¥%.0f) at Vast.ai/RunPod avg rates! %s · github.com/hexwander/colabhive",
			colabAccountCount,
			providersLabelEn,
			round1(totalGPUHours),
			round1(freeGPUHoursHarvested),
			math.Round(estimatedSavedUSD),
			math.Round(estimatedSavedCNY),
			rankBadgeEn,
		)
	} else {
		shareCopyZh = fmt.Sprintf(
			"🐝 用 ColabHive 把 %d 个账号（%s）聚合成私人云算力池！累计运行 %.1f 小时云端显卡，自动闲置关机防浪费 %d 次，等效云端租卡价值约 ¥%.0f（$%.0f，按 Vast.ai/RunPod 均价）！ %s · github.com/hexwander/colabhive",
			colabAccountCount,
			providersLabelZh,
			round1(totalGPUHours),
			wasteStats.AutoTeardownsCount,
			math.Round(estimatedSavedCNY),
			math.Round(estimatedSavedUSD),
			rankBadgeZh,
		)
		shareCopyEn = fmt.Sprintf(
			"🐝 Pooled %d accounts (%s) into a private GPU cluster with ColabHive! %.1fh total GPU runtime with %d idle auto-stops — ~$%.0f (¥%.0f) equivalent cloud GPU value (Vast.ai/RunPod avg)! %s · github.com/hexwander/colabhive",
			colabAccountCount,
			providersLabelEn,
			round1(totalGPUHours),
			wasteStats.AutoTeardownsCount,
			math.Round(estimatedSavedUSD),
			math.Round(estimatedSavedCNY),
			rankBadgeEn,
		)
	}

	out := &models.SavingsReceiptStats{
		GeneratedAt:            now,
		PoolTitle:              RedactEmailStrings(poolTitle),
		TotalAccounts:          colabAccountCount,
		ProvidersConnected:     scrubStringSlice(providersConnected),
		TotalGPUHours:          round2(totalGPUHours),
		FreeGPUHoursHarvested:  round2(freeGPUHoursHarvested),
		G4EquivalentHours:      round2(g4EquivalentHours),
		TotalCUBurned:          round2(totalCUBurned),
		SavedIdleWasteCU:       round2(wasteStats.SavedIdleWasteCU),
		WasteZeroEfficiencyPct: wasteZeroEfficiency,
		AutoResumeRelays:       autoResumeRelays,
		AutoTeardowns:          wasteStats.AutoTeardownsCount,
		ZombieAborts:           wasteStats.ZombieAbortsCount,
		EstimatedSavedUSD:      estimatedSavedUSD,
		EstimatedSavedCNY:      estimatedSavedCNY,
		TopGPUsUsed:            scrubStringSlice(topGPUs),
		RankBadgeEn:            RedactEmailStrings(rankBadgeEn),
		RankBadgeZh:            RedactEmailStrings(rankBadgeZh),
		ShareCopyEn:            RedactEmailStrings(shareCopyEn),
		ShareCopyZh:            RedactEmailStrings(shareCopyZh),
	}
	return out, nil
}

func referenceBurnRate(gpu models.AcceleratorType) float64 {
	if spec, ok := models.GetHardwareSpec(gpu); ok && spec.DefaultBurnRateCUHr > 0 {
		return spec.DefaultBurnRateCUHr
	}
	return t4ReferenceBurnCUHr
}

func buildProviderDisplayList(seen map[models.ProviderType]bool) []string {
	if seen[models.ProviderColab] {
		return []string{"Google Colab"}
	}
	return []string{}
}

func buildTopGPUDisplayList(weights map[models.AcceleratorType]float64) []string {
	type gpuScore struct {
		gpu   models.AcceleratorType
		score float64
	}
	list := make([]gpuScore, 0, len(weights))
	for g, w := range weights {
		if w > 0 {
			list = append(list, gpuScore{gpu: g, score: w})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return string(list[i].gpu) < string(list[j].gpu)
	})
	out := make([]string, 0, 3)
	for _, item := range list {
		if len(out) >= 3 {
			break
		}
		if spec, ok := models.GetHardwareSpec(item.gpu); ok {
			out = append(out, spec.ShortLabel)
		} else {
			out = append(out, string(item.gpu))
		}
	}
	return out
}

func scrubStringSlice(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = RedactEmailStrings(s)
	}
	return out
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
