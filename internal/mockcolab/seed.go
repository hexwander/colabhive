package mockcolab

import (
	"context"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/vault"
)

// DemoClusterSummary reports the seeded entities created by SeedDemoCluster for `--demo` mode and tests.
type DemoClusterSummary struct {
	Accounts           []*models.Account
	Proxies            []*models.ProxyConfig
	ActiveExperimentID string
	QuotaSummary       *models.QuotaSummary
}

// SeedDemoCluster idempotently populates Store (and optional AccountPoolService + mockcolab.Server)
// with a realistic 4-account multi-tier research cluster, sticky proxies, contributor invite & receipt,
// empirical EWMA burn-rate calibrations, saved waste stats, active sessions, and a 4x G4 sharded experiment.
func SeedDemoCluster(
	ctx context.Context,
	st models.Store,
	poolSvc models.AccountPoolService,
	srv *Server,
) (*DemoClusterSummary, error) {
	now := time.Now().UTC()

	// Check idempotency: if accounts already exist, return summary of existing cluster.
	existingAccts, err := st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	if len(existingAccts) > 0 {
		var summary *models.QuotaSummary
		if poolSvc != nil {
			summary, _ = poolSvc.GetQuotaSummary(ctx)
		}
		return &DemoClusterSummary{
			Accounts:           existingAccts,
			ActiveExperimentID: "exp_qwen3_27b_eval",
			QuotaSummary:       summary,
		}, nil
	}

	cipher := vault.NewCipher("")

	// 1. Seed 2 Sticky Egress Proxies
	checkedAt := now.Add(-2 * time.Minute)
	proxies := []*models.ProxyConfig{
		{
			ID:            "prx_us_sjc_01",
			Name:          "US-West SJC Residential #1",
			Protocol:      models.ProxySOCKS5,
			URL:           "", // Direct in local demo so local mockcolab calls succeed cleanly
			MaskedURL:     "socks5://38.12.*.*:1080",
			Region:        "US-West (San Jose)",
			ExitIP:        "38.12.90.42",
			LatencyMs:     18,
			Healthy:       true,
			LastCheckedAt: &checkedAt,
			CreatedAt:     now.Add(-72 * time.Hour),
		},
		{
			ID:            "prx_us_iad_02",
			Name:          "US-East IAD Academic #2",
			Protocol:      models.ProxyHTTP,
			URL:           "",
			MaskedURL:     "http://198.51.*.*:8080",
			Region:        "US-East (Virginia)",
			ExitIP:        "198.51.100.19",
			LatencyMs:     31,
			Healthy:       true,
			LastCheckedAt: &checkedAt,
			CreatedAt:     now.Add(-48 * time.Hour),
		},
	}
	for _, p := range proxies {
		if err := st.SaveProxy(ctx, p); err != nil {
			return nil, err
		}
	}

	// 2. Seed 4 Realistic Accounts (AI Ultra, Pro+, Pro, Pro+ Secondary)
	expSoon := now.Add(5 * 24 * time.Hour)
	cycleReset := now.Add(18 * 24 * time.Hour)
	refillSoon := now.Add(3*time.Hour + 15*time.Minute)
	tokenExp := now.Add(55 * time.Minute)

	type acctSeed struct {
		acc     *models.Account
		refresh string
		access  string
	}

	seeds := []acctSeed{
		{
			refresh: "1//0ultra_refresh_01",
			access:  "ya29.ultra_access_01",
			acc: &models.Account{
				ID:                "acc_demo_ultra_01",
				Email:             "lab-ultra-01@stanford-ai.org",
				DisplayName:       "Stanford AI Lab Ultra #1",
				Alias:             "Lab Ultra #1 (Blackwell G4)",
				SubscriptionTier:  models.TierAIUltra,
				Health:            models.HealthActiveWorking,
				Enabled:           true,
				Priority:          100,
				Groups:            []string{"core-research", "eval-sweep"},
				Tags:              []string{"g4", "h100", "demo"},
				ProxyID:           "prx_us_sjc_01",
				MaxConcurrentVMs:  5,
				ActiveAssignments: 2,
				StickyAgentCount:  1,
				PaidCUBalance:     820.50,
				ReferenceCycleCU:  2500.0,
				SchedulableCU:     820.50,
				LiveBurnRateCUHr:  19.32,
				CycleResetsAt:     &cycleReset,
				FreeQuota: &models.FreeQuotaInfo{
					RemainingMilliCCU: 18500,
					RemainingFreeCU:   18.50,
					NextRefillAt:      &refillSoon,
				},
				PublicAPISupported:   true,
				TunnelAPISupported:   true,
				Scopes:               append([]string(nil), models.OwnerOAuthScopes...),
				ActiveSessionSummary: "2× G4 96GB (qwen3_s0, qwen3_s1)",
				TokenExpiresAt:       &tokenExp,
				LastPolledAt:         &now,
				CreatedAt:            now.Add(-30 * 24 * time.Hour),
			},
		},
		{
			refresh: "1//0proplus_refresh_02",
			access:  "ya29.proplus_access_02",
			acc: &models.Account{
				ID:                     "acc_demo_proplus_02",
				Email:                  "research-proplus-02@berkeley.edu",
				DisplayName:            "Berkeley Systems Pro+ #2",
				Alias:                  "Pro+ Expiring Batch #2",
				SubscriptionTier:       models.TierProPlus,
				Health:                 models.HealthActiveWorking,
				Enabled:                true,
				Priority:               90,
				Groups:                 []string{"core-research", "eval-sweep"},
				Tags:                   []string{"g4", "a100", "expiring-soon", "demo"},
				ProxyID:                "prx_us_sjc_01",
				MaxConcurrentVMs:       4,
				ActiveAssignments:      1,
				StickyAgentCount:       1,
				PaidCUBalance:          412.80,
				ReferenceCycleCU:       500.0,
				SchedulableCU:          412.80,
				LiveBurnRateCUHr:       9.66,
				EarliestBatchExpiresAt: &expSoon,
				ExpiringSoonCU:         118.40,
				FreeQuota: &models.FreeQuotaInfo{
					RemainingMilliCCU: 14000,
					RemainingFreeCU:   14.00,
					NextRefillAt:      &refillSoon,
				},
				PublicAPISupported:   true,
				TunnelAPISupported:   true,
				Scopes:               append([]string(nil), models.OwnerOAuthScopes...),
				ActiveSessionSummary: "1× G4 96GB (qwen3_s2)",
				TokenExpiresAt:       &tokenExp,
				LastPolledAt:         &now,
				CreatedAt:            now.Add(-84 * 24 * time.Hour),
			},
		},
		{
			refresh: "1//0pro_refresh_03",
			access:  "ya29.pro_access_03",
			acc: &models.Account{
				ID:                "acc_demo_pro_03",
				Email:             "sweeps-pro-03@gmail.com",
				DisplayName:       "Eval Sweeps Pro #3",
				Alias:             "Sweeps Pro #3 (A100/L4/T4)",
				SubscriptionTier:  models.TierPro,
				Health:            models.HealthHealthy,
				Enabled:           true,
				Priority:          60,
				Groups:            []string{"eval-sweep"},
				Tags:              []string{"a100", "l4", "t4", "demo"},
				ProxyID:           "prx_us_iad_02",
				MaxConcurrentVMs:  2,
				ActiveAssignments: 0,
				PaidCUBalance:     94.20,
				ReferenceCycleCU:  100.0,
				SchedulableCU:     94.20,
				LiveBurnRateCUHr:  0,
				FreeQuota: &models.FreeQuotaInfo{
					RemainingMilliCCU: 12000,
					RemainingFreeCU:   12.00,
					NextRefillAt:      &refillSoon,
				},
				TunnelAPISupported: true,
				Scopes:             append([]string(nil), models.OwnerOAuthScopes...),
				TokenExpiresAt:     &tokenExp,
				LastPolledAt:       &now,
				CreatedAt:          now.Add(-15 * 24 * time.Hour),
			},
		},
		{
			refresh: "1//0contrib_refresh_alice",
			access:  "ya29.contrib_access_alice",
			acc: &models.Account{
				ID:                "acc_demo_contrib_04",
				Email:             "alice.donated@mit.edu",
				DisplayName:       "Alice Chen (Contributed)",
				Alias:             "Alice Donated Pro+ (Reserve ≥50 CU)",
				SubscriptionTier:  models.TierProPlus,
				Health:            models.HealthActiveWorking,
				Enabled:           true,
				Priority:          75,
				Groups:            []string{"contributors", "eval-sweep"},
				Tags:              []string{"contributed", "g4", "demo"},
				ProxyID:           "prx_us_iad_02",
				MaxConcurrentVMs:  1,
				ActiveAssignments: 1,
				PaidCUBalance:     310.00,
				ReferenceCycleCU:  500.0,
				SchedulableCU:     185.60,
				LiveBurnRateCUHr:   9.66,
				TunnelAPISupported: true,
				IsContributor:      false,
				Guardrails: models.ContributorGuardrails{
					MinReserveCU:     50.0,
					AllowPaidCUBurn:  true,
					AllowedGPUs:      []models.AcceleratorType{models.AcceleratorG4, models.AcceleratorA100, models.AcceleratorL4, models.AcceleratorT4},
					MaxConcurrentVMs: 1,
				},
				Scopes:               append([]string(nil), models.OwnerOAuthScopes...),
				ActiveSessionSummary: "1× G4 96GB (qwen3_s3)",
				TokenExpiresAt:       &tokenExp,
				LastPolledAt:         &now,
				CreatedAt:            now.Add(-20 * time.Hour),
			},
		},
	}

	concretePool, _ := poolSvc.(*pool.Service)
	savedAccts := make([]*models.Account, 0, len(seeds))

	for _, item := range seeds {
		if concretePool != nil {
			saved, err := concretePool.SeedAccountCredentials(ctx, item.acc, item.refresh, item.access, 55*time.Minute)
			if err != nil {
				return nil, err
			}
			savedAccts = append(savedAccts, saved)
		} else {
			item.acc.EncryptedRefreshToken, _ = cipher.EncryptString(item.refresh)
			item.acc.EncryptedAccessToken, _ = cipher.EncryptString(item.access)
			item.acc.OAuthClientID = models.OfficialColabOAuthClientID
			item.acc.OAuthClientSecret = models.OfficialColabOAuthClientSecret
			if err := st.SaveAccount(ctx, item.acc); err != nil {
				return nil, err
			}
			savedAccts = append(savedAccts, item.acc)
		}
	}

	// 4. Seed Empirical Burn Rate Calibration EWMA & Saved Idle-Waste Stats
	_ = st.RecordBurnSample(ctx, models.AcceleratorG4, models.ShapeHighRAM, 9.66)
	_ = st.RecordBurnSample(ctx, models.AcceleratorA100, models.ShapeHighRAM, 8.88)
	_ = st.RecordBurnSample(ctx, models.AcceleratorL4, models.ShapeHighRAM, 4.52)
	_ = st.RecordBurnSample(ctx, models.AcceleratorT4, models.ShapeStandard, 1.96)

	for i := 0; i < 3; i++ {
		_ = st.AddSavedWaste(ctx, 28.40, true)
	}
	for i := 0; i < 9; i++ {
		_ = st.AddSavedWaste(ctx, 7.05, false)
	}

	// 5. Seed 4 Active G4 Sessions + 1 Flagship Sharded Experiment (`Qwen3-27B Eval · 4× G4 96GB Shards`)
	expID := "exp_qwen3_27b_eval"
	shardAccounts := []*models.Account{savedAccts[0], savedAccts[0], savedAccts[1], savedAccts[3]}
	shards := make([]models.ExperimentShard, 0, 4)

	for idx := 0; idx < 4; idx++ {
		shardIdx := idx
		acc := shardAccounts[idx]
		ep := "gpu-g4-s-demo0" + string(rune('0'+idx))
		proxyTok := "ChAKDm_demo_shard_tok_" + string(rune('0'+idx))
		kernID := "kern-demo-0" + string(rune('0'+idx))
		nbh := models.NewNotebookHash()
		proxyURL := "https://8080-" + ep + "-c.us-central1-a.prod.colab.dev"
		startedAt := now.Add(-42 * time.Minute)
		if srv != nil {
			proxyURL = srv.URL + "/runtime/" + ep
			srv.mu.Lock()
			vm := &mockVM{
				Endpoint:        ep,
				NotebookHash:    nbh,
				AccountEmail:    acc.Email,
				Variant:         1,
				MachineShape:    1,
				Accelerator:     models.AcceleratorG4,
				BurnRateCUHr:    9.66,
				ProxyToken:      proxyTok,
				CreatedAt:       startedAt,
				LastKeepaliveAt: now,
				kernels: map[string]*mockKernel{
					kernID: {
						ID:             kernID,
						Name:           "python3",
						ExecutionState: "idle",
						ExecutionCount: 3,
						LastActivity:   now,
					},
				},
				sessions: make(map[string]map[string]any),
				files: map[string][]byte{
					"content/sample_data/README.md": []byte("ColabHive Demo Runtime /content/sample_data\n"),
					"content/shard_progress.jsonl":  []byte("{\"step\": 180, \"acc\": 0.8472}\n"),
				},
			}
			srv.vmsByEndpoint[ep] = vm
			srv.nbhToEndpoint[nbh] = ep
			srv.proxyTokenToVM[proxyTok] = vm
			srv.mu.Unlock()
		}
		sess := &models.RuntimeSession{
			ID:                   "sess_qwen3_s" + string(rune('0'+idx)),
			Name:                 "qwen3_27b_s" + string(rune('0'+idx)),
			AccountID:            acc.ID,
			AccountEmail:         acc.Email,
			Backend:              models.BackendTunnelAPI,
			Endpoint:             ep,
			NotebookHash:         nbh,
			ProxyURL:             proxyURL,
			ProxyToken:           proxyTok,
			ProxyTokenExpiresAt:  &tokenExp,
			Variant:              models.VariantGPU,
			RequestedAccelerator: models.AcceleratorG4,
			Accelerator:          models.AcceleratorG4,
			Shape:                models.ShapeHighRAM,
			Status:               models.SessionBusy,
			KernelID:             kernID,
			BurnRateCUHr:         9.66,
			AccumulatedCU:        6.76,
			ColabWebURL:          models.BuildColabWebAttachURL(ep),
			ExperimentID:         expID,
			ShardIndex:           &shardIdx,
			KeepaliveHealthy:     true,
			LastKeepaliveAt:      &now,
			Resources: &models.VMResourceTelemetry{
				MemoryTotalBytes: 89612345344,
				MemoryUsedBytes:  31457280000,
				MemoryFreeBytes:  58155065344,
				DiskTotalBytes:   253055090688,
				DiskUsedBytes:    62277025792,
				GPUs: []models.VMGPUTelemetry{
					{
						Name:              "NVIDIA RTX PRO 6000 Blackwell",
						MemoryUsedBytes:   54975581388,
						MemoryTotalBytes:  103079215104,
						GPUUtilization:    0.94,
						MemoryUtilization: 0.53,
						TemperatureC:      67,
						EverUsed:          true,
					},
				},
				SampledAt: now,
			},
			Worker: &models.WorkerLiveness{
				PID:           4810 + idx,
				ProcState:     "R",
				Alive:         true,
				LastCheckedAt: &now,
			},
			UptimeSec: 2520,
			CreatedAt: startedAt,
			UpdatedAt: now,
		}
		if err := st.SaveSession(ctx, sess); err != nil {
			return nil, err
		}

		synced := 180 + idx*12
		shards = append(shards, models.ExperimentShard{
			ShardID:          idx,
			SessionID:        sess.ID,
			SessionName:      sess.Name,
			AccountID:        acc.ID,
			AccountEmail:     acc.Email,
			Accelerator:      models.AcceleratorG4,
			Status:           "RUNNING",
			PID:              4810 + idx,
			ProcState:        "R",
			ProgressPct:      float64(synced) / 2.5,
			SyncedRecords:    synced,
			TargetRecords:    250,
			VRAMUsedGB:       51.2,
			VRAMTotalGB:      96.0,
			SMUtilPct:        94.0,
			TempC:            67,
			CUBurned:         6.76,
			StartupSelfCheck: "PASSED",
			LogTail:          "[eval] Qwen3-27B BF16 MMLU-Pro acc=0.8472 | 2.94s/it",
			StartedAt:        &startedAt,
		})
	}

	exp := &models.Experiment{
		ID:                    expID,
		Title:                 "Qwen3-27B Eval · 4× G4 96GB Shards",
		Status:                models.ExperimentRunning,
		ScriptPath:            "benchmarks/eval_qwen3_27b_mmlu.py",
		Accelerator:           models.AcceleratorG4,
		HighMem:               true,
		NumShards:             4,
		Strategy:              models.StrategyWasteZero,
		CompletedShards:       0,
		RunningShards:         4,
		SyncedRecordsTotal:    756,
		TargetRecordsTotal:    1000,
		AggregateBurnRateCUHr: 38.64,
		CUBurnedSoFar:         27.04,
		EstimatedTotalCU:      35.80,
		AutoStopOnComplete:    true,
		Shards:                shards,
		CreatedAt:             now.Add(-42 * time.Minute),
		UpdatedAt:             now,
	}
	if err := st.SaveExperiment(ctx, exp); err != nil {
		return nil, err
	}

	var quotaSummary *models.QuotaSummary
	if poolSvc != nil {
		quotaSummary, _ = poolSvc.GetQuotaSummary(ctx)
	}

	return &DemoClusterSummary{
		Accounts:           savedAccts,
		Proxies:            proxies,
		ActiveExperimentID: expID,
		QuotaSummary:       quotaSummary,
	}, nil
}
