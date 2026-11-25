package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/runtime"
)

// SeedDemoCluster populates rich, realistic showcase state into Store and RouterService
// when `--demo` mode is active, while remaining idempotent across restarts.
func SeedDemoCluster(
	ctx context.Context,
	st models.Store,
	rtr models.RouterService,
	artifactsDir string,
	publicBaseURL string,
) error {
	if st == nil {
		return nil
	}
	if publicBaseURL == "" {
		publicBaseURL = "http://localhost:7860"
	}
	publicBaseURL = trimRightSlash(publicBaseURL)

	existingAccs, _ := st.ListAccounts(ctx)
	alreadySeeded := len(existingAccs) > 0

	now := time.Now().UTC()
	in9Days := now.Add(9 * 24 * time.Hour)
	in18Days := now.Add(18 * 24 * time.Hour)
	in3Hours := now.Add(3 * time.Hour)
	in6Hours := now.Add(6 * time.Hour)
	started20mAgo := now.Add(-20 * time.Minute)

	// 1. Seed 3 Isolated Egress Proxies
	prxLAX := &models.ProxyConfig{
		ID:            "prx_us_lax_01",
		Name:          "US-California · Residential SOCKS5",
		Protocol:      models.ProxySOCKS5,
		URL:           "socks5://demo:pass@38.142.19.84:1080",
		MaskedURL:     "socks5://38.142.*.*:1080",
		Region:        "US-LAX",
		ExitIP:        "38.142.19.84",
		LatencyMs:     52,
		Healthy:       true,
		BoundAccounts: 2,
		LastCheckedAt: &now,
		CreatedAt:     started20mAgo,
	}
	prxPDX := &models.ProxyConfig{
		ID:            "prx_us_pdx_02",
		Name:          "US-Oregon · Dedicated SOCKS5",
		Protocol:      models.ProxySOCKS5,
		URL:           "socks5://demo:pass@64.88.201.17:1080",
		MaskedURL:     "socks5://64.88.*.*:1080",
		Region:        "US-PDX",
		ExitIP:        "64.88.201.17",
		LatencyMs:     61,
		Healthy:       true,
		BoundAccounts: 1,
		LastCheckedAt: &now,
		CreatedAt:     started20mAgo,
	}
	prxNRT := &models.ProxyConfig{
		ID:            "prx_jp_nrt_03",
		Name:          "Tokyo · Clean Lab HTTP",
		Protocol:      models.ProxyHTTP,
		URL:           "http://demo:pass@45.76.108.22:8080",
		MaskedURL:     "http://45.76.*.*:8080",
		Region:        "JP-NRT",
		ExitIP:        "45.76.108.22",
		LatencyMs:     39,
		Healthy:       true,
		BoundAccounts: 1,
		LastCheckedAt: &now,
		CreatedAt:     started20mAgo,
	}

	if !alreadySeeded {
		_ = st.SaveProxy(ctx, prxLAX)
		_ = st.SaveProxy(ctx, prxPDX)
		_ = st.SaveProxy(ctx, prxNRT)

		// 2. Seed 4 Pooled Accounts (AI Ultra, Pro+, Pro Expiring, Contributed Pro+)
		demoAccounts := []*models.Account{
			{
				ID:                   "acc_demo_ultra01",
				Email:                "lab-ultra-01@colabhive.dev",
				DisplayName:          "Lab AI Ultra #1",
				Alias:                "Lab Ultra #1 (Blackwell G4)",
				Notes:                "Primary high-VRAM node · Renews 15th monthly",
				SubscriptionTier:     models.TierAIUltra,
				Health:               models.HealthActiveWorking,
				Enabled:              true,
				Priority:             100,
				Groups:               []string{"research-core", "high-vram"},
				Tags:                 []string{"demo", "g4", "h100", "lab-shared"},
				ProxyID:              prxLAX.ID,
				Proxy:                prxLAX,
				MaxConcurrentVMs:     5,
				ActiveAssignments:    2,
				StickyAgentCount:     2,
				PaidCUBalance:        482.50,
				ReferenceCycleCU:     2500.00,
				SchedulableCU:        482.50,
				LiveBurnRateCUHr:     18.54, // 1x G4 (9.66) + 1x A100 (8.88)
				CycleResetsAt:        &in18Days,
				PublicAPISupported:   true,
				TunnelAPISupported:   true,
				ActiveSessionSummary: "qwen27b_mmlu_s0 (G4) + wb_interactive_a100 (A100)",
				LastPolledAt:         &now,
				CreatedAt:            started20mAgo,
				UpdatedAt:            now,
				GPUEntitlements: map[models.AcceleratorType]models.GPUEntitlement{
					models.AcceleratorG4:   {Accelerator: models.AcceleratorG4, Status: models.EntitlementAvailable, CalibratedBurnRate: 9.66, CalibrationSource: models.CalibrationLive},
					models.AcceleratorH100: {Accelerator: models.AcceleratorH100, Status: models.EntitlementAvailable, CalibratedBurnRate: 12.50, CalibrationSource: models.CalibrationEmpirical},
					models.AcceleratorA100: {Accelerator: models.AcceleratorA100, Status: models.EntitlementAvailable, CalibratedBurnRate: 8.88, CalibrationSource: models.CalibrationLive},
					models.AcceleratorL4:   {Accelerator: models.AcceleratorL4, Status: models.EntitlementAvailable},
					models.AcceleratorT4:   {Accelerator: models.AcceleratorT4, Status: models.EntitlementAvailable},
					models.AcceleratorV6E1: {Accelerator: models.AcceleratorV6E1, Status: models.EntitlementAvailable},
				},
			},
			{
				ID:                   "acc_demo_proplus02",
				Email:                "ml-proplus-02@colabhive.dev",
				DisplayName:          "Research Pro+ #2",
				Alias:                "Research Pro+ #2",
				SubscriptionTier:     models.TierProPlus,
				Health:               models.HealthActiveWorking,
				Enabled:              true,
				Priority:             80,
				Groups:               []string{"research-core"},
				Tags:                 []string{"demo", "g4", "a100"},
				ProxyID:              prxPDX.ID,
				Proxy:                prxPDX,
				MaxConcurrentVMs:     4,
				ActiveAssignments:    1,
				PaidCUBalance:        418.20,
				ReferenceCycleCU:     500.00,
				SchedulableCU:        418.20,
				LiveBurnRateCUHr:     9.66,
				CycleResetsAt:        &in18Days,
				TunnelAPISupported:   true,
				ActiveSessionSummary: "qwen27b_mmlu_s1 (G4 · 51.8/96GB)",
				LastPolledAt:         &now,
				CreatedAt:            started20mAgo,
				UpdatedAt:            now,
				GPUEntitlements: map[models.AcceleratorType]models.GPUEntitlement{
					models.AcceleratorH100: {Accelerator: models.AcceleratorH100, Status: models.EntitlementTierRestricted, Reason: "HTTP 400 regional/tier block", CooldownUntil: &in6Hours},
					models.AcceleratorG4:   {Accelerator: models.AcceleratorG4, Status: models.EntitlementAvailable, CalibratedBurnRate: 9.66, CalibrationSource: models.CalibrationLive},
					models.AcceleratorA100: {Accelerator: models.AcceleratorA100, Status: models.EntitlementAvailable},
					models.AcceleratorL4:   {Accelerator: models.AcceleratorL4, Status: models.EntitlementAvailable},
					models.AcceleratorT4:   {Accelerator: models.AcceleratorT4, Status: models.EntitlementAvailable},
				},
			},
			{
				ID:                     "acc_demo_pro03",
				Email:                  "indie-pro-03@colabhive.dev",
				DisplayName:            "Eval Runner Pro #3",
				Alias:                  "Eval Pro #3 (Expiring Batch)",
				SubscriptionTier:       models.TierPro,
				Health:                 models.HealthActiveWorking,
				Enabled:                true,
				Priority:               90,
				Groups:                 []string{"research-core", "sweep"},
				Tags:                   []string{"demo", "expiring-soon"},
				ProxyID:                prxNRT.ID,
				Proxy:                  prxNRT,
				MaxConcurrentVMs:       2,
				ActiveAssignments:      1,
				PaidCUBalance:          142.80,
				ReferenceCycleCU:       200.00,
				SchedulableCU:          142.80,
				LiveBurnRateCUHr:       9.66,
				EarliestBatchExpiresAt: &in9Days,
				ExpiringSoonCU:         38.40,
				FreeQuota: &models.FreeQuotaInfo{
					RemainingMilliCCU: 14500,
					RemainingFreeCU:   14.5,
					NextRefillAt:      &in3Hours,
				},
				TunnelAPISupported:   true,
				ActiveSessionSummary: "qwen27b_mmlu_s2 (G4 · Waste-Zero Priority)",
				LastPolledAt:         &now,
				CreatedAt:            started20mAgo,
				UpdatedAt:            now,
				GPUEntitlements: map[models.AcceleratorType]models.GPUEntitlement{
					models.AcceleratorH100: {Accelerator: models.AcceleratorH100, Status: models.EntitlementIneligible},
					models.AcceleratorG4:   {Accelerator: models.AcceleratorG4, Status: models.EntitlementAvailable},
					models.AcceleratorL4:   {Accelerator: models.AcceleratorL4, Status: models.EntitlementAvailable},
					models.AcceleratorT4:   {Accelerator: models.AcceleratorT4, Status: models.EntitlementAvailable},
				},
			},
			{
				ID:                 "acc_demo_contrib04",
				Email:              "research-proplus-04@colabhive.dev",
				DisplayName:        "Secondary Pro+ #4",
				Alias:              "Secondary Pro+ #4 (Reserve >= 80 CU)",
				SubscriptionTier:   models.TierProPlus,
				Health:             models.HealthActiveWorking,
				Enabled:            true,
				Priority:           75,
				Groups:             []string{"research-core"},
				Tags:               []string{"demo"},
				ProxyID:            prxLAX.ID,
				Proxy:              prxLAX,
				MaxConcurrentVMs:   2,
				ActiveAssignments:  1,
				PaidCUBalance:      384.92,
				ReferenceCycleCU:   500.00,
				SchedulableCU:      304.92, // 384.92 - 80.0 floor
				LiveBurnRateCUHr:   9.66,
				IsContributor:      false,
				TunnelAPISupported: true,
				Guardrails: models.ContributorGuardrails{
					MinReserveCU:     80.0,
					AllowPaidCUBurn:  true,
					AllowedGPUs:      []models.AcceleratorType{models.AcceleratorT4, models.AcceleratorL4, models.AcceleratorG4, models.AcceleratorA100},
					MaxConcurrentVMs: 2,
				},
				ActiveSessionSummary: "qwen27b_mmlu_s3 (G4)",
				LastPolledAt:         &now,
				CreatedAt:            started20mAgo,
				UpdatedAt:            now,
				GPUEntitlements: map[models.AcceleratorType]models.GPUEntitlement{
					models.AcceleratorG4:   {Accelerator: models.AcceleratorG4, Status: models.EntitlementAvailable},
					models.AcceleratorA100: {Accelerator: models.AcceleratorA100, Status: models.EntitlementAvailable},
					models.AcceleratorL4:   {Accelerator: models.AcceleratorL4, Status: models.EntitlementAvailable},
					models.AcceleratorT4:   {Accelerator: models.AcceleratorT4, Status: models.EntitlementAvailable},
				},
			},
		}

		for _, a := range demoAccounts {
			_ = st.SaveAccount(ctx, a)
		}

		// 3. Seed Saved Waste Stats (+312.6 CU across 19 auto-teardowns + 4 zombie aborts)
		for i := 0; i < 19; i++ {
			_ = st.AddSavedWaste(ctx, 12.2, false)
		}
		for i := 0; i < 4; i++ {
			_ = st.AddSavedWaste(ctx, 20.2, true)
		}

		// 4. Seed Active 4x G4 Sharded Experiment (`exp_qwen27b_mmlu`) + 1 Interactive A100 Session
		pngBytes := runtime.GenerateDemoPlotPNG()
		shardAccounts := []struct {
			id    string
			email string
		}{
			{"acc_demo_ultra01", "lab-ultra-01@colabhive.dev"},
			{"acc_demo_proplus02", "ml-proplus-02@colabhive.dev"},
			{"acc_demo_pro03", "indie-pro-03@colabhive.dev"},
			{"acc_demo_contrib04", "alice.contrib@stanford.edu"},
		}
		syncedCounts := []int{88, 86, 83, 85} // Total 342 / 500
		shards := make([]models.ExperimentShard, 4)

		for idx := 0; idx < 4; idx++ {
			shIdx := idx
			sessID := fmt.Sprintf("sess_demo_qwen_s%d", idx)
			sessName := fmt.Sprintf("qwen27b_mmlu_s%d", idx)
			ep := fmt.Sprintf("demo-g4-s-qwen0%d", idx)
			pid := 4812 + idx*23

			plotFile := fmt.Sprintf("shard_%d_curve.png", idx)
			if artifactsDir != "" {
				p := filepath.Join(artifactsDir, sessID, plotFile)
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				_ = os.WriteFile(p, pngBytes, 0o644)
			}

			const gb = int64(1024 * 1024 * 1024)
			sess := &models.RuntimeSession{
				ID:                   sessID,
				Name:                 sessName,
				AccountID:            shardAccounts[idx].id,
				AccountEmail:         shardAccounts[idx].email,
				Backend:              models.BackendTunnelAPI,
				Endpoint:             ep,
				NotebookHash:         models.NewNotebookHash(),
				ProxyURL:             "demo://" + ep,
				Variant:              models.VariantGPU,
				RequestedAccelerator: models.AcceleratorG4,
				Accelerator:          models.AcceleratorG4,
				Shape:                models.ShapeHighRAM,
				Status:               models.SessionBusy,
				KernelID:             fmt.Sprintf("kern-g4-demo-%d", idx),
				BurnRateCUHr:         9.66,
				AccumulatedCU:        3.22,
				ColabWebURL:          models.BuildColabWebAttachURL(ep),
				ExperimentID:         "exp_qwen27b_mmlu",
				ShardIndex:           &shIdx,
				KeepaliveHealthy:     true,
				LastKeepaliveAt:      &now,
				Resources: &models.VMResourceTelemetry{
					MemoryTotalBytes: 89 * gb,
					MemoryFreeBytes:  58 * gb,
					MemoryUsedBytes:  31 * gb,
					DiskTotalBytes:   253 * gb,
					DiskUsedBytes:    62 * gb,
					HFCacheBytes:     54 * gb,
					GPUs: []models.VMGPUTelemetry{
						{
							Name:              "NVIDIA RTX PRO 6000 Blackwell",
							MemoryUsedBytes:   51*gb + int64(idx)*512*1024*1024,
							MemoryTotalBytes:  96 * gb,
							GPUUtilization:    0.94,
							MemoryUtilization: 0.54,
							TemperatureC:      64 + idx,
							PowerWatts:        288,
							EverUsed:          true,
						},
					},
					SampledAt: now,
				},
				Worker: &models.WorkerLiveness{
					PID:           pid,
					ProcState:     "R",
					Alive:         true,
					LastCheckedAt: &now,
				},
				Timeline: []models.ExecutionStep{
					{ID: "st1", Label: fmt.Sprintf("Assign G4 96GB VM (%s)", shardAccounts[idx].email), Status: "completed", DurationMs: 4120, StartedAt: started20mAgo, CompletedAt: &started20mAgo},
					{ID: "st2", Label: "uv pip install --system vllm transformers datasets", Status: "completed", DurationMs: 14200, StartedAt: started20mAgo, CompletedAt: &started20mAgo},
					{ID: "st3", Label: "8s Startup Self-Check & /proc/stat != Z Verified", Status: "completed", DurationMs: 8010, StartedAt: started20mAgo, CompletedAt: &started20mAgo},
					{ID: "st4", Label: fmt.Sprintf("Shard #%d Evaluation Loop (PID %d)", idx, pid), Status: "running", DurationMs: 1180000, StartedAt: started20mAgo},
				},
				Cells: []models.NotebookCell{
					{
						CellID:         fmt.Sprintf("cell_s%d_1", idx),
						Index:          0,
						CellType:       "code",
						Source:         fmt.Sprintf("!nvidia-smi\n# Qwen3-27B BF16 Shard %d/4 Worker", idx),
						ExecutionCount: 1,
						Outputs: []models.KernelRichOutput{
							{
								Index:       0,
								MimeTypes:   []string{"image/png"},
								ArtifactURL: fmt.Sprintf("/api/v1/artifacts/%s/%s", sessID, plotFile),
								Title:       fmt.Sprintf("Shard #%d Throughput & Loss", idx),
								SizeBytes:   int64(len(pngBytes)),
							},
						},
					},
				},
				LastExecutionFile: "eval_qwen27b_shard.py",
				LastExecutionAt:   &now,
				CreatedAt:         started20mAgo,
				UpdatedAt:         now,
			}
			_ = st.SaveSession(ctx, sess)

			shards[idx] = models.ExperimentShard{
				ShardID:          idx,
				SessionID:        sessID,
				SessionName:      sessName,
				AccountID:        shardAccounts[idx].id,
				AccountEmail:     shardAccounts[idx].email,
				Accelerator:      models.AcceleratorG4,
				Status:           "RUNNING",
				PID:              pid,
				ProcState:        "R",
				ProgressPct:      float64(syncedCounts[idx]) / 125.0 * 100.0,
				SyncedRecords:    syncedCounts[idx],
				TargetRecords:    125,
				VRAMUsedGB:       51.4 + float64(idx)*0.5,
				VRAMTotalGB:      96.0,
				SMUtilPct:        94,
				TempC:            64 + idx,
				CUBurned:         3.22,
				StartupSelfCheck: "PASSED",
				LogTail:          fmt.Sprintf("[%d/125] mmlu_pro_acc=0.864 | 51.4GB/96GB VRAM | 2.91s/sample", syncedCounts[idx]),
				StartedAt:        &started20mAgo,
			}
		}

		// Interactive warm A100 session bound to Claude Code
		a100ID := "sess_demo_interactive_a100"
		a100Plot := "a100_attention_curve.png"
		if artifactsDir != "" {
			p := filepath.Join(artifactsDir, a100ID, a100Plot)
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, pngBytes, 0o644)
		}
		const gb = int64(1024 * 1024 * 1024)
		_ = st.SaveSession(ctx, &models.RuntimeSession{
			ID:                   a100ID,
			Name:                 "wb_interactive_a100",
			AccountID:            "acc_demo_ultra01",
			AccountEmail:         "lab-ultra-01@colabhive.dev",
			Backend:              models.BackendTunnelAPI,
			Endpoint:             "demo-a100-s-wb01",
			NotebookHash:         models.NewNotebookHash(),
			ProxyURL:             "demo://demo-a100-s-wb01",
			Variant:              models.VariantGPU,
			RequestedAccelerator: models.AcceleratorH100,
			Accelerator:          models.AcceleratorA100,
			FallbackApplied:      true,
			Shape:                models.ShapeHighRAM,
			Status:               models.SessionIdle,
			KernelID:             "kern-a100-demo-wb",
			BurnRateCUHr:         8.88,
			AccumulatedCU:        1.85,
			ColabWebURL:          models.BuildColabWebAttachURL("demo-a100-s-wb01"),
			StickyKey:            "mcp-http-claude-code-01",
			KeepaliveHealthy:     true,
			LastKeepaliveAt:      &now,
			Resources: &models.VMResourceTelemetry{
				MemoryTotalBytes: 89 * gb,
				MemoryFreeBytes:  64 * gb,
				MemoryUsedBytes:  25 * gb,
				DiskTotalBytes:   253 * gb,
				DiskUsedBytes:    48 * gb,
				HFCacheBytes:     28 * gb,
				GPUs: []models.VMGPUTelemetry{
					{
						Name:              "NVIDIA A100-SXM4-40GB",
						MemoryUsedBytes:   24 * gb,
						MemoryTotalBytes:  40 * gb,
						GPUUtilization:    0.38,
						MemoryUtilization: 0.60,
						TemperatureC:      56,
						PowerWatts:        142,
						EverUsed:          true,
					},
				},
				SampledAt: now,
			},
			Cells: []models.NotebookCell{
				{
					CellID:         "cell_wb_1",
					Index:          0,
					CellType:       "code",
					Source:         "import torch\nprint(torch.cuda.get_device_name(0))",
					ExecutionCount: 1,
					Outputs: []models.KernelRichOutput{
						{Index: 0, MimeTypes: []string{"text/plain"}, TextData: "NVIDIA A100-SXM4-40GB"},
					},
				},
				{
					CellID:         "cell_wb_2",
					Index:          1,
					CellType:       "code",
					Source:         "import matplotlib.pyplot as plt\nplt.plot([0.92, 0.61, 0.43, 0.31])",
					ExecutionCount: 2,
					Outputs: []models.KernelRichOutput{
						{
							Index:       0,
							MimeTypes:   []string{"image/png"},
							ArtifactURL: fmt.Sprintf("/api/v1/artifacts/%s/%s", a100ID, a100Plot),
							Title:       "Validation Loss Curve",
							SizeBytes:   int64(len(pngBytes)),
						},
					},
				},
			},
			CreatedAt: started20mAgo,
			UpdatedAt: now,
		})

		_ = st.SaveExperiment(ctx, &models.Experiment{
			ID:                    "exp_qwen27b_mmlu",
			Title:                 "Qwen3-27B MMLU-Pro Eval · 4× G4 96GB Shards",
			Status:                models.ExperimentRunning,
			ScriptPath:            "eval_qwen27b_shard.py",
			Accelerator:           models.AcceleratorG4,
			HighMem:               true,
			NumShards:             4,
			Strategy:              models.StrategyWasteZero,
			CompletedShards:       0,
			RunningShards:         4,
			FailedShards:          0,
			SyncedRecordsTotal:    342,
			TargetRecordsTotal:    500,
			AggregateBurnRateCUHr: 38.64,
			CUBurnedSoFar:         12.88,
			EstimatedTotalCU:      18.80,
			AutoStopOnComplete:    true,
			Artifacts: []models.KernelRichOutput{
				{
					Index:       0,
					MimeTypes:   []string{"image/png"},
					ArtifactURL: fmt.Sprintf("/api/v1/artifacts/%s/%s", "sess_demo_qwen_s0", "shard_0_curve.png"),
					Title:       "Aggregate Shard Loss & Accuracy",
					SizeBytes:   int64(len(pngBytes)),
				},
			},
			Shards:    shards,
			CreatedAt: started20mAgo,
			UpdatedAt: now,
		})

		doneAt := now.Add(-35 * time.Minute)
		zeroExit := 0
		_ = st.SaveExperiment(ctx, &models.Experiment{
			ID:                    "exp_lora_r1",
			Title:                 "Llama-3.3-8B LoRA Sweep · 2× L4 Completed",
			Status:                models.ExperimentCompleted,
			ScriptPath:            "train_lora_sweep.py",
			Accelerator:           models.AcceleratorL4,
			HighMem:               true,
			NumShards:             2,
			Strategy:              models.StrategyRoundRobin,
			CompletedShards:       2,
			RunningShards:         0,
			SyncedRecordsTotal:    200,
			TargetRecordsTotal:    200,
			AggregateBurnRateCUHr: 0,
			CUBurnedSoFar:         4.18,
			AutoStopOnComplete:    true,
			MergedJSONLPath:       "./runs/exp_lora_r1/merged.jsonl",
			Shards: []models.ExperimentShard{
				{ShardID: 0, SessionName: "exp_lora_r1_s0", AccountID: "acc_demo_proplus02", AccountEmail: "ml-proplus-02@colabhive.dev", Accelerator: models.AcceleratorL4, Status: "COMPLETED_AND_UNASSIGNED", ExitCode: &zeroExit, ProgressPct: 100, SyncedRecords: 100, TargetRecords: 100, CUBurned: 2.09, CompletedAt: &doneAt},
				{ShardID: 1, SessionName: "exp_lora_r1_s1", AccountID: "acc_demo_pro03", AccountEmail: "indie-pro-03@colabhive.dev", Accelerator: models.AcceleratorL4, Status: "COMPLETED_AND_UNASSIGNED", ExitCode: &zeroExit, ProgressPct: 100, SyncedRecords: 100, TargetRecords: 100, CUBurned: 2.09, CompletedAt: &doneAt},
			},
			CreatedAt:   now.Add(-65 * time.Minute),
			UpdatedAt:   doneAt,
			CompletedAt: &doneAt,
		})
	}

	// 6. Ensure a second completed experiment (`exp_lora_r1`) & PNG plot artifacts exist even when
	//    `mockcolab.SeedDemoCluster` seeded the primary 4x G4 cluster first!
	if exps, _ := st.ListExperiments(ctx); len(exps) == 1 && exps[0] != nil {
		pngBytes := runtime.GenerateDemoPlotPNG()
		if len(exps[0].Shards) > 0 && artifactsDir != "" {
			s0ID := exps[0].Shards[0].SessionID
			plotName := "shard_0_curve.png"
			p := filepath.Join(artifactsDir, s0ID, plotName)
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, pngBytes, 0o644)
			exps[0].Artifacts = []models.KernelRichOutput{
				{
					Index:       0,
					MimeTypes:   []string{"image/png"},
					ArtifactURL: fmt.Sprintf("/api/v1/artifacts/%s/%s", s0ID, plotName),
					Title:       "Aggregate Shard Loss & Accuracy",
					SizeBytes:   int64(len(pngBytes)),
				},
			}
			_ = st.SaveExperiment(ctx, exps[0])
		}
		doneAt := now.Add(-35 * time.Minute)
		zeroExit := 0
		acc0ID, acc0Email := "acc_demo_01", "lab-ultra-01@colabhive.dev"
		acc1ID, acc1Email := "acc_demo_02", "ml-proplus-02@colabhive.dev"
		if len(existingAccs) > 0 && existingAccs[0] != nil {
			acc0ID, acc0Email = existingAccs[0].ID, existingAccs[0].Email
		}
		if len(existingAccs) > 1 && existingAccs[1] != nil {
			acc1ID, acc1Email = existingAccs[1].ID, existingAccs[1].Email
		}
		_ = st.SaveExperiment(ctx, &models.Experiment{
			ID:                 "exp_lora_r1",
			Title:              "Llama-3.3-8B LoRA Sweep · 2× L4 Completed",
			Status:             models.ExperimentCompleted,
			ScriptPath:         "train_lora_sweep.py",
			Accelerator:        models.AcceleratorL4,
			HighMem:            true,
			NumShards:          2,
			Strategy:           models.StrategyRoundRobin,
			CompletedShards:    2,
			SyncedRecordsTotal: 200,
			TargetRecordsTotal: 200,
			CUBurnedSoFar:      4.18,
			AutoStopOnComplete: true,
			MergedJSONLPath:    "./runs/exp_lora_r1/merged.jsonl",
			Shards: []models.ExperimentShard{
				{ShardID: 0, SessionName: "exp_lora_r1_s0", AccountID: acc0ID, AccountEmail: acc0Email, Accelerator: models.AcceleratorL4, Status: "COMPLETED_AND_UNASSIGNED", ExitCode: &zeroExit, ProgressPct: 100, SyncedRecords: 100, TargetRecords: 100, CUBurned: 2.09, CompletedAt: &doneAt},
				{ShardID: 1, SessionName: "exp_lora_r1_s1", AccountID: acc1ID, AccountEmail: acc1Email, Accelerator: models.AcceleratorL4, Status: "COMPLETED_AND_UNASSIGNED", ExitCode: &zeroExit, ProgressPct: 100, SyncedRecords: 100, TargetRecords: 100, CUBurned: 2.09, CompletedAt: &doneAt},
			},
			CreatedAt:   now.Add(-65 * time.Minute),
			UpdatedAt:   doneAt,
			CompletedAt: &doneAt,
		})
	}

	// 7. Always ensure sticky bindings & router traces are hydrated in memory/DB for `/router` topology,
	//    referencing real SessionIDs & AccountIDs in Store!
	if rtr != nil {
		allSessions, _ := st.ListSessions(ctx)
		sess0ID, sess0Name, sess0Kern, sess0AccID, sess0Email := "sess_demo_qwen_s0", "qwen27b_mmlu_s0", "kern-g4-demo-0", "acc_demo_ultra01", "lab-ultra-01@colabhive.dev"
		var sess0Acc models.AcceleratorType = models.AcceleratorG4
		sess1ID, sess1Name, sess1Kern, sess1AccID, sess1Email := "sess_demo_interactive_a100", "wb_interactive_a100", "kern-a100-demo-wb", "acc_demo_ultra01", "lab-ultra-01@colabhive.dev"
		var sess1Acc models.AcceleratorType = models.AcceleratorA100

		if len(allSessions) > 0 && allSessions[0] != nil {
			s0 := allSessions[0]
			sess0ID, sess0Name, sess0Kern, sess0AccID, sess0Email, sess0Acc = s0.ID, s0.Name, s0.KernelID, s0.AccountID, s0.AccountEmail, s0.Accelerator
		}
		if len(allSessions) > 1 && allSessions[1] != nil {
			s1 := allSessions[1]
			sess1ID, sess1Name, sess1Kern, sess1AccID, sess1Email, sess1Acc = s1.ID, s1.Name, s1.KernelID, s1.AccountID, s1.AccountEmail, s1.Accelerator
		}

		existingKeys, _ := rtr.ListAPIKeys(ctx)
		var agyKeyID, ccKeyID string
		var agyPrefix, ccPrefix string
		if len(existingKeys) == 0 {
			k1, err1 := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
				Name:             "Antigravity IDE (Primary)",
				DefaultStrategy:  models.StrategyWasteZero,
				MaxBudgetCUTotal: 250.0,
				MaxConcurrentVMs: 4,
			})
			if err1 == nil && k1 != nil {
				agyKeyID = k1.ID
				agyPrefix = k1.KeyPrefix
			}
			k2, err2 := rtr.CreateAPIKey(ctx, models.CreateAPIKeyRequest{
				Name:             "Claude Code Evaluation Bot",
				DefaultStrategy:  models.StrategyStickySession,
				MaxBudgetCUTotal: 120.0,
				MaxConcurrentVMs: 2,
			})
			if err2 == nil && k2 != nil {
				ccKeyID = k2.ID
				ccPrefix = k2.KeyPrefix
			}
		} else {
			agyKeyID = existingKeys[0].ID
			agyPrefix = existingKeys[0].KeyPrefix
			if len(existingKeys) > 1 {
				ccKeyID = existingKeys[1].ID
				ccPrefix = existingKeys[1].KeyPrefix
			}
		}

		_ = rtr.BindStickySession(ctx, models.RouteBinding{
			StickyKey:    "mcp-http-antigravity-01",
			ClientName:   "Antigravity IDE",
			APIKeyID:     agyKeyID,
			APIKeyPrefix: agyPrefix,
			Strategy:     models.StrategyWasteZero,
			SessionID:    sess0ID,
			SessionName:  sess0Name,
			KernelID:     sess0Kern,
			AccountID:    sess0AccID,
			AccountEmail: sess0Email,
			ProxySummary: "socks5://38.12.*.*:1080",
			Accelerator:  sess0Acc,
			ActiveCalls:  14,
		})

		_ = rtr.BindStickySession(ctx, models.RouteBinding{
			StickyKey:    "mcp-http-claude-code-01",
			ClientName:   "Claude Code",
			APIKeyID:     ccKeyID,
			APIKeyPrefix: ccPrefix,
			Strategy:     models.StrategyStickySession,
			SessionID:    sess1ID,
			SessionName:  sess1Name,
			KernelID:     sess1Kern,
			AccountID:    sess1AccID,
			AccountEmail: sess1Email,
			ProxySummary: "socks5://38.12.*.*:1080",
			Accelerator:  sess1Acc,
			ActiveCalls:  7,
		})

		existingTraces, _ := rtr.ListTraces(ctx, 5)
		if len(existingTraces) == 0 {
			seedTraces := []models.ToolTraceEvent{
				{
					Timestamp:       now.Add(-9 * time.Minute),
					Caller:          "Antigravity IDE",
					APIKeyPrefix:    agyPrefix,
					Transport:       models.TransportMCPHTTP,
					ToolOrEndpoint:  "pool_quota_summary",
					DurationMs:      38,
					Status:          "ok",
					StatusCode:      200,
					RequestPreview:  `{"refresh_live":true}`,
					ResponsePreview: `{"total_available_cu":1428.42,"active_vms":4}`,
				},
				{
					Timestamp:       now.Add(-8 * time.Minute),
					Caller:          "Claude Code",
					APIKeyPrefix:    ccPrefix,
					Transport:       models.TransportMCPHTTP,
					ToolOrEndpoint:  "create_runtime",
					SessionID:       sess1ID,
					AccountID:       sess1AccID,
					AccountEmail:    sess1Email,
					RequestedGPU:    models.AcceleratorH100,
					AssignedGPU:     sess1Acc,
					FallbackTrace:   fmt.Sprintf("HTTP 400 H100 -> Fallback %s", sess1Acc),
					DurationMs:      1420,
					Status:          "fallback",
					StatusCode:      200,
					RequestPreview:  `{"gpu":"H100","high_mem":true,"auto_fallback":true}`,
					ResponsePreview: fmt.Sprintf(`{"assigned_accelerator":%q,"fallback_applied":true}`, sess1Acc),
				},
				{
					Timestamp:       now.Add(-6 * time.Minute),
					Caller:          "Antigravity IDE",
					APIKeyPrefix:    agyPrefix,
					Transport:       models.TransportMCPHTTP,
					ToolOrEndpoint:  "experiment_launch_sharded",
					AccountID:       sess0AccID,
					AccountEmail:    sess0Email,
					RequestedGPU:    models.AcceleratorG4,
					AssignedGPU:     models.AcceleratorG4,
					DurationMs:      2840,
					CUDelta:         0.42,
					Status:          "ok",
					StatusCode:      200,
					RequestPreview:  `{"experiment_id":"exp_qwen3_27b_eval","gpu":"G4","num_shards":4,"strategy":"waste-zero"}`,
					ResponsePreview: `{"status":"RUNNING","num_shards":4,"aggregate_burn_rate_cu_hr":38.64}`,
				},
				{
					Timestamp:       now.Add(-2 * time.Minute),
					Caller:          "Claude Code",
					APIKeyPrefix:    ccPrefix,
					Transport:       models.TransportMCPHTTP,
					ToolOrEndpoint:  "execute_code",
					SessionID:       sess1ID,
					AccountID:       sess1AccID,
					AccountEmail:    sess1Email,
					RequestedGPU:    sess1Acc,
					AssignedGPU:     sess1Acc,
					DurationMs:      312,
					CUDelta:         0.08,
					Status:          "ok",
					StatusCode:      200,
					RequestPreview:  `{"code":"import torch; print(torch.cuda.get_device_name(0))"}`,
					ResponsePreview: `{"stdout":"NVIDIA RTX PRO 6000 Blackwell\n","status":"ok"}`,
				},
				{
					Timestamp:       now.Add(-45 * time.Second),
					Caller:          "colabhive-cli",
					Transport:       models.TransportCLI,
					ToolOrEndpoint:  "experiment_status",
					AssignedGPU:     models.AcceleratorG4,
					DurationMs:      19,
					Status:          "ok",
					StatusCode:      200,
					RequestPreview:  `{"experiment_id":"exp_qwen3_27b_eval"}`,
					ResponsePreview: `{"synced_records_total":756,"running_shards":4}`,
				},
			}
			for _, tr := range seedTraces {
				_ = rtr.RecordTrace(ctx, tr)
			}
		}
	}

	return nil
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
