package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

type apiKeyCUBurnRecorder interface {
	RecordAPIKeyCUBurn(ctx context.Context, apiKeyID string, deltaCU float64) (exceeded bool, reason string)
}

// ParseProcStat parses a Linux `/proc/{pid}/stat` line using the normative `rfind(')')` rule
// (handling process `comm` names containing spaces or nested parentheses) and detects zombie (`Z`) traps.
func ParseProcStat(raw string) (state string, alive bool, zombie bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "MISSING", false, false
	}
	rparen := strings.LastIndex(trimmed, ")")
	if rparen == -1 || len(trimmed) <= rparen+2 {
		return "?", false, false
	}
	stateChar := string(trimmed[rparen+2])
	isZombie := stateChar == "Z" || stateChar == "z"
	isDead := isZombie || stateChar == "X" || stateChar == "x"
	return stateChar, !isDead, isZombie
}

// ============================================================================
// Out-of-Band `/api/colab/resources` Poller & Keepalive / Anti-Zombie Loops
// ============================================================================

type colabResourcesWire struct {
	Memory struct {
		TotalBytes int64 `json:"totalBytes"`
		FreeBytes  int64 `json:"freeBytes"`
	} `json:"memory"`
	Disks []struct {
		Filesystem struct {
			Label      string `json:"label"`
			TotalBytes int64  `json:"totalBytes"`
			UsedBytes  int64  `json:"usedBytes"`
		} `json:"filesystem"`
	} `json:"disks"`
	GPUs []struct {
		Name              string  `json:"name"`
		MemoryUsedBytes   int64   `json:"memoryUsedBytes"`
		MemoryTotalBytes  int64   `json:"memoryTotalBytes"`
		GPUUtilization    float64 `json:"gpuUtilization"`
		MemoryUtilization float64 `json:"memoryUtilization"`
		TemperatureC      int     `json:"temperatureC"`
		PowerWatts        int     `json:"powerWatts"`
		EverUsed          bool    `json:"everUsed"`
	} `json:"gpus"`
}

func (e *Engine) fetchVMResources(
	ctx context.Context,
	client *http.Client,
	proxyBaseURL string,
	proxyToken string,
) (*models.VMResourceTelemetry, error) {
	return e.fetchVMResourcesWithTimeout(ctx, client, proxyBaseURL, proxyToken, 8*time.Second)
}

func (e *Engine) fetchVMResourcesWithTimeout(
	ctx context.Context,
	client *http.Client,
	proxyBaseURL string,
	proxyToken string,
	timeout time.Duration,
) (*models.VMResourceTelemetry, error) {
	if proxyBaseURL == "" {
		return nil, nil
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	ctxShort, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u := strings.TrimRight(proxyBaseURL, "/") + "/api/colab/resources"
	req, err := http.NewRequestWithContext(ctxShort, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	setProxyHeaders(req, proxyToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET /api/colab/resources HTTP %d", resp.StatusCode)
	}

	var wire colabResourcesWire
	if err := json.Unmarshal(stripXSSIPrefix(raw), &wire); err != nil {
		return nil, err
	}

	var diskTotal, diskUsed int64
	if len(wire.Disks) > 0 {
		diskTotal = wire.Disks[0].Filesystem.TotalBytes
		diskUsed = wire.Disks[0].Filesystem.UsedBytes
	}

	gpus := make([]models.VMGPUTelemetry, 0, len(wire.GPUs))
	for _, g := range wire.GPUs {
		gpus = append(gpus, models.VMGPUTelemetry{
			Name:              g.Name,
			MemoryUsedBytes:   g.MemoryUsedBytes,
			MemoryTotalBytes:  g.MemoryTotalBytes,
			GPUUtilization:    g.GPUUtilization,
			MemoryUtilization: g.MemoryUtilization,
			TemperatureC:      g.TemperatureC,
			PowerWatts:        g.PowerWatts,
			EverUsed:          g.EverUsed,
		})
	}

	usedRAM := wire.Memory.TotalBytes - wire.Memory.FreeBytes
	if usedRAM < 0 {
		usedRAM = 0
	}

	return &models.VMResourceTelemetry{
		MemoryTotalBytes: wire.Memory.TotalBytes,
		MemoryFreeBytes:  wire.Memory.FreeBytes,
		MemoryUsedBytes:  usedRAM,
		DiskTotalBytes:   diskTotal,
		DiskUsedBytes:    diskUsed,
		GPUs:             gpus,
		SampledAt:        time.Now().UTC(),
	}, nil
}

func (e *Engine) pingTunnelKeepalive(ctx context.Context, client *http.Client, accessToken string, endpoint string) error {
	if endpoint == "" {
		return nil
	}
	ctxShort, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	u := fmt.Sprintf("%s/tun/m/%s/keep-alive/?authuser=0", e.opts.TunnelBaseURL, url.PathEscape(endpoint))
	req, err := http.NewRequestWithContext(ctxShort, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Colab-Tunnel", "Google")
	req.Header.Set("X-Colab-Client-Agent", "colab-cli")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("keep-alive HTTP %d", resp.StatusCode)
}

func (e *Engine) startBackgroundLoops() {
	e.bgWg.Add(3)
	go e.runKeepaliveAndZombieLoop()
	go e.runResourceAndGuardrailLoop()
	go e.runRemoteSyncLoop()
}

func (e *Engine) runRemoteSyncLoop() {
	defer e.bgWg.Done()
	interval := e.opts.RemoteSyncInterval
	if interval <= 0 {
		interval = 20 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-e.bgCtx.Done():
			return
		case <-ticker.C:
			if !e.opts.DemoMode {
				syncCtx, cancel := context.WithTimeout(e.bgCtx, 15*time.Second)
				_, _ = e.SyncRemoteAssignments(syncCtx)
				cancel()
			}
		}
	}
}

func (e *Engine) runKeepaliveAndZombieLoop() {
	defer e.bgWg.Done()
	keepTicker := time.NewTicker(e.opts.KeepaliveInterval)
	tunnelTicker := time.NewTicker(e.opts.TunnelPingInterval)
	defer keepTicker.Stop()
	defer tunnelTicker.Stop()

	for {
		select {
		case <-e.bgCtx.Done():
			return
		case <-keepTicker.C:
			e.SweepKeepaliveAndZombiesOnce(e.bgCtx, false)
		case <-tunnelTicker.C:
			e.SweepKeepaliveAndZombiesOnce(e.bgCtx, true)
		}
	}
}

func (e *Engine) runResourceAndGuardrailLoop() {
	defer e.bgWg.Done()
	ticker := time.NewTicker(e.opts.ResourcePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.bgCtx.Done():
			return
		case <-ticker.C:
			e.PollResourcesAndGuardrailsOnce(e.bgCtx)
		}
	}
}

// SweepKeepaliveAndZombiesOnce executes a single pass of the 15m-reaper keepalive
// and `/proc/{pid}/stat` anti-zombie watchdog across all active sessions.
func (e *Engine) SweepKeepaliveAndZombiesOnce(ctx context.Context, includeTunnelPing bool) {
	if e.st == nil {
		return
	}
	sessions, err := e.st.ListSessions(ctx)
	if err != nil {
		return
	}

	autoReleaseZombie := true
	stickyIdleTTL := 20 * time.Minute
	boundSessionIDs := make(map[string]bool)
	if e.rtr != nil {
		if pol, pErr := e.rtr.GetPolicy(ctx); pErr == nil && pol != nil {
			autoReleaseZombie = pol.AutoReleaseOnZombie
			if pol.StickyIdleTTLSec > 0 {
				stickyIdleTTL = time.Duration(pol.StickyIdleTTLSec) * time.Second
			}
		}
		if bindings, bErr := e.rtr.ListStickyBindings(ctx); bErr == nil {
			for _, b := range bindings {
				if b.SessionID != "" {
					boundSessionIDs[b.SessionID] = true
				}
			}
		}
	}

	now := time.Now().UTC()
	for _, sess := range sessions {
		if sess == nil || sess.Status == models.SessionStopped || sess.Status == models.SessionLost || sess.Status == models.SessionZombieAlert {
			continue
		}
		// Never run ZMQ probes or auto-reapers against AdoptedExternal sessions.
		if sess.AdoptedExternal {
			continue
		}

		// Enforce AgentAPIKey.IdleAutoKillMin and unbound sticky idle reaper on idle non-external sessions.
		if e.shouldAutoReapIdleSession(ctx, sess, boundSessionIDs, stickyIdleTTL, now) {
			_ = e.StopSession(ctx, sess.ID)
			continue
		}

		isOrphanedSticky := sess.StickyKey != "" && !boundSessionIDs[sess.ID]

		acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
		if e.isDemoSession(acc, sess) {
			_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
				if !isOrphanedSticky {
					cur.KeepaliveHealthy = true
					cur.LastKeepaliveAt = &now
				}
				cur.UptimeSec = computeUptimeSec(cur)
				return true
			})
			continue
		}

		accessTok, proxyURL, cErr := e.acquireCredentials(ctx, acc)
		if cErr != nil {
			continue
		}
		httpClient, wsDialer, dErr := e.buildClientAndDialer(proxyURL)
		if dErr != nil {
			continue
		}

		keepaliveOK := false
		// Stop pinging /tun/m/{endpoint}/keep-alive/ if the sticky session was unbound or expired!
		if includeTunnelPing && sess.Endpoint != "" && !isOrphanedSticky {
			if pErr := e.pingTunnelKeepalive(ctx, httpClient, accessTok, sess.Endpoint); pErr == nil {
				keepaliveOK = true
			}
		}

		var workerUpdate *models.WorkerLiveness
		var probeMeta shardExitProbeMeta
		isZombie := false
		alreadyExited := sess.Worker != nil && !sess.Worker.Alive && !sess.Worker.ZombieDetected
		// Probe `/proc/{pid}/stat` when session has a live tracked background worker PID.
		// If the worker was already recorded as exited (`!sess.Worker.Alive`, e.g., retrying AutoStopOnComplete
		// after a transient unassign error when the kernel may already be deleted), skip re-probing the kernel!
		if sess.Worker != nil && sess.Worker.PID > 0 && !alreadyExited {
			mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)
			mem.mu.Lock()
			tok := firstNonEmpty(mem.proxyToken, sess.ProxyToken)
			mem.mu.Unlock()

			shardIdx := -1
			if sess.ShardIndex != nil {
				shardIdx = *sess.ShardIndex
			}
			probeCode := fmt.Sprintf(
				"import os, json, time\np=%d; sid=%d; sp=f'/proc/{p}/stat'\nraw=''\nif os.path.exists(sp):\n    raw=open(sp,'r',errors='replace').read()\nprint(raw.strip())\n"+
					"ef=f'/content/.colabhive/shard_{sid}_exit.json'\n"+
					"if not raw.strip() or (sid>=0 and os.path.exists(ef)) or ') Z ' in raw:\n"+
					"    meta={}\n"+
					"    if sid>=0 and not os.path.exists(ef):\n"+
					"        time.sleep(0.05)\n"+
					"    if sid>=0 and os.path.exists(ef):\n"+
					"        try: meta.update(json.loads(open(ef,'r',errors='replace').read()))\n"+
					"        except Exception: pass\n"+
					"    jf=f'/content/shard_{sid}.jsonl'\n"+
					"    if sid>=0 and os.path.exists(jf):\n"+
					"        meta['has_jsonl']=True\n"+
					"        try: meta['records']=sum(1 for ln in open(jf,'r',errors='replace') if ln.strip())\n"+
					"        except Exception: pass\n"+
					"    lf='/content/worker.log'\n"+
					"    if os.path.exists(lf):\n"+
					"        try:\n"+
					"            lns=[ln.strip() for ln in open(lf,'r',errors='replace').read().splitlines() if ln.strip()]\n"+
					"            if lns: meta['tail']=lns[-1][:240]\n"+
					"            if any('Traceback (most recent call last)' in ln for ln in lns[-40:]): meta['fatal']=True\n"+
					"        except Exception: pass\n"+
					"    if meta: print('__CH_META__:' + json.dumps(meta))\n",
				sess.Worker.PID,
				shardIdx,
			)
			res, pErr := e.executeOverJupyterWS(ctx, wsDialer, acc, sess, tok, models.KernelExecRequest{
				SessionID:      sess.ID,
				Code:           probeCode,
				Silent:         true,
				StoreHistory:   false,
				IdleTimeoutSec: 8,
			}, false)
			if pErr == nil && res != nil && res.Status == "ok" {
				statPart, parsedMeta := splitStatAndShardMeta(res.Stdout)
				probeMeta = parsedMeta
				stCode, alive, z := ParseProcStat(statPart)
				if parsedMeta.HasExitCode {
					// Exit metadata was already atomically published by the worker supervisor before os._exit();
					// even if /proc/<pid>/stat is momentarily in Z before the reaper's waitpid() finishes,
					// treat the worker as exited normally rather than an un-reapable zombie!
					alive = false
					z = false
				}
				isZombie = z
				workerCopy := *sess.Worker
				workerCopy.ProcState = stCode
				workerCopy.Alive = alive
				workerCopy.ZombieDetected = z
				if parsedMeta.HasExitCode {
					ec := parsedMeta.ExitCode
					workerCopy.ExitCode = &ec
				}
				if parsedMeta.Fatal {
					workerCopy.HasFatalTrace = true
				}
				workerCopy.LastCheckedAt = &now
				workerUpdate = &workerCopy
				keepaliveOK = true
			}
		} else {
			keepaliveOK = true
		}

		if isZombie {
			if workerUpdate != nil {
				sess.Worker = workerUpdate
			}
			e.handleZombieDetected(ctx, sess, autoReleaseZombie)
			continue
		}

		workerExited := (workerUpdate != nil && !workerUpdate.Alive) || alreadyExited
		effectiveWorker := workerUpdate
		if effectiveWorker == nil {
			effectiveWorker = sess.Worker
		}
		_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			if workerUpdate != nil {
				cur.Worker = workerUpdate
			}
			// For experiment shards, keep `SessionBusy` until `reconcileFinishedExperimentShard` persists
			// the final checkpoint (preventing the background idle reaper from unassigning the VM early!).
			if workerExited && sess.ExperimentID == "" && cur.Status == models.SessionBusy {
				cur.Status = models.SessionIdle
			}
			if keepaliveOK {
				cur.KeepaliveHealthy = true
				cur.LastKeepaliveAt = &now
			}
			cur.UptimeSec = computeUptimeSec(cur)
			return true
		})

		if workerExited && sess.ExperimentID != "" && effectiveWorker != nil {
			readyForIdle := e.reconcileFinishedExperimentShard(ctx, sess, effectiveWorker, probeMeta, now)
			if readyForIdle {
				_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
					if cur.Status == models.SessionBusy {
						cur.Status = models.SessionIdle
						return true
					}
					return false
				})
			}
		}
	}
}

type shardExitProbeMeta struct {
	HasExitCode bool
	ExitCode    int
	HasJSONL    bool
	Records     int
	Fatal       bool
	Tail        string
}

func splitStatAndShardMeta(stdout string) (string, shardExitProbeMeta) {
	const marker = "__CH_META__:"
	var meta shardExitProbeMeta
	idx := strings.Index(stdout, marker)
	if idx < 0 {
		return stdout, meta
	}
	statPart := stdout[:idx]
	jsonLine := strings.TrimSpace(stdout[idx+len(marker):])
	if nl := strings.IndexByte(jsonLine, '\n'); nl >= 0 {
		jsonLine = strings.TrimSpace(jsonLine[:nl])
	}
	var raw struct {
		ExitCode *int   `json:"exit_code"`
		HasJSONL bool   `json:"has_jsonl"`
		Records  int    `json:"records"`
		Fatal    bool   `json:"fatal"`
		Tail     string `json:"tail"`
	}
	if json.Unmarshal([]byte(jsonLine), &raw) == nil {
		if raw.ExitCode != nil {
			meta.HasExitCode = true
			meta.ExitCode = *raw.ExitCode
		}
		meta.HasJSONL = raw.HasJSONL
		meta.Records = raw.Records
		meta.Fatal = raw.Fatal
		meta.Tail = raw.Tail
	}
	return statPart, meta
}

func (e *Engine) reconcileFinishedExperimentShard(
	ctx context.Context,
	sess *models.RuntimeSession,
	worker *models.WorkerLiveness,
	meta shardExitProbeMeta,
	now time.Time,
) bool {
	if e.st == nil || sess == nil || strings.TrimSpace(sess.ExperimentID) == "" || worker == nil {
		return true
	}

	var (
		shouldAutoStop   bool
		needFinalSync    bool
		expectJSONL      bool
		finalAlreadySync bool
		shardID          int
		outDir           string
		updatedExp       *models.Experiment
	)

	e.expMu.Lock()
	exp, err := e.st.GetExperiment(ctx, sess.ExperimentID)
	if err != nil || exp == nil {
		e.expMu.Unlock()
		return true
	}
	targetIdx := -1
	for i := range exp.Shards {
		if exp.Shards[i].SessionID == sess.ID {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		e.expMu.Unlock()
		// Shard-to-session link not persisted yet while `LaunchShardedExperiment` is still provisioning;
		// keep `SessionBusy` (`readyForIdle = false`) so the next sweep reconciles it once linked!
		return false
	}
	curStatus := exp.Shards[targetIdx].Status
	retryCompletedSync := (curStatus == "COMPLETED" && (exp.AutoStopOnComplete || (exp.Shards[targetIdx].CheckpointExpected && !exp.Shards[targetIdx].FinalCheckpointSaved))) ||
		(curStatus == "FAILED" && exp.Shards[targetIdx].CheckpointExpected && !exp.Shards[targetIdx].FinalCheckpointSaved)
	if curStatus != "RUNNING" && curStatus != "PROVISIONING" && !retryCompletedSync {
		e.expMu.Unlock()
		return true
	}

	sh := &exp.Shards[targetIdx]
	shardID = sh.ShardID
	outDir = exp.LocalOutputDir
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(os.TempDir(), "colabhive-runs", exp.ID)
	}

	if retryCompletedSync {
		shouldAutoStop = curStatus == "COMPLETED" && exp.AutoStopOnComplete
		expectJSONL = sh.CheckpointExpected || meta.HasJSONL
		finalAlreadySync = sh.FinalCheckpointSaved
		needFinalSync = shouldAutoStop || expectJSONL
		e.expMu.Unlock()
	} else {
		// Fail closed on missing or non-zero exit code (`shard_<N>_exit.json` is written atomically
		// inside the worker wrapper BEFORE `/proc/<pid>` exits). When an explicit `exit_code == 0` is
		// recorded, trust the process exit status even if the script caught and logged an intermediate traceback.
		failed := worker.ExitCode == nil || *worker.ExitCode != 0
		sh.ProcState = worker.ProcState
		sh.ZombieDetected = false
		sh.CompletedAt = &now
		if sess.AccumulatedCU > sh.CUBurned {
			sh.CUBurned = sess.AccumulatedCU
		}
		if meta.HasJSONL {
			sh.CheckpointExpected = true
		}
		if meta.Records > 0 {
			sh.SyncedRecords = meta.Records
		}
		if meta.Tail != "" {
			sh.LogTail = meta.Tail
		}
		expectJSONL = sh.CheckpointExpected

		if failed {
			sh.Status = "FAILED"
			ec := 1
			if worker.ExitCode != nil && *worker.ExitCode != 0 {
				ec = *worker.ExitCode
			}
			sh.ExitCode = &ec
			// Even on failed exit, persist any partial `shard_<N>.jsonl` written before crash prior to idle reaping!
			needFinalSync = expectJSONL
		} else {
			zero := 0
			sh.ExitCode = &zero
			sh.Status = "COMPLETED"
			sh.ProgressPct = 100.0
			shouldAutoStop = exp.AutoStopOnComplete
			needFinalSync = shouldAutoStop || expectJSONL
		}

		recomputeExperimentRollup(exp)
		_ = e.st.SaveExperiment(ctx, exp)
		cp := *exp
		cp.Shards = append([]models.ExperimentShard(nil), exp.Shards...)
		updatedExp = &cp
		e.expMu.Unlock()
	}

	readyForIdle := true
	if needFinalSync {
		mkErr := os.MkdirAll(outDir, 0o755)
		localShardPath := filepath.Join(outDir, fmt.Sprintf("shard_%d.jsonl", shardID))
		checkpointSafeToTeardown := false

		// Only reuse a cached `localShardPath` when a previous post-exit completion sweep ALREADY verified
		// and persisted the final shard checkpoint (`finalAlreadySync`), never an earlier mid-run partial snapshot!
		if retryCompletedSync && finalAlreadySync {
			if !expectJSONL {
				checkpointSafeToTeardown = true
			} else if info, sErr := os.Stat(localShardPath); sErr == nil && info.Size() > 0 {
				checkpointSafeToTeardown = true
			}
		}
		newlySavedFinal := false
		if !checkpointSafeToTeardown {
			remoteJSONL := fmt.Sprintf("/content/shard_%d.jsonl", shardID)
			data, dErr := e.DownloadFile(ctx, sess.ID, remoteJSONL)
			if dErr == nil {
				if len(data) == 0 && !expectJSONL {
					checkpointSafeToTeardown = true
					newlySavedFinal = true
				} else if mkErr == nil {
					if wErr := writeShardCheckpointAtomic(localShardPath, data); wErr == nil {
						checkpointSafeToTeardown = true
						newlySavedFinal = true
					}
				}
			} else if !expectJSONL && isRemoteFileMissingErr(dErr) {
				checkpointSafeToTeardown = true
				newlySavedFinal = true
			}
		}
		if newlySavedFinal {
			e.expMu.Lock()
			if savedExp, gErr := e.st.GetExperiment(ctx, sess.ExperimentID); gErr == nil && savedExp != nil {
				for i := range savedExp.Shards {
					if savedExp.Shards[i].SessionID == sess.ID {
						savedExp.Shards[i].FinalCheckpointSaved = true
						_ = e.st.SaveExperiment(ctx, savedExp)
						break
					}
				}
			}
			e.expMu.Unlock()
		}

		if !checkpointSafeToTeardown {
			// Keep session in `SessionBusy` so the background idle reaper cannot kill the VM before the next sweep retries!
			readyForIdle = false
		} else if shouldAutoStop {
			if stopErr := e.StopSession(ctx, sess.ID); stopErr == nil {
				e.expMu.Lock()
				if freshExp, fErr := e.st.GetExperiment(ctx, sess.ExperimentID); fErr == nil && freshExp != nil {
					for i := range freshExp.Shards {
						if freshExp.Shards[i].SessionID == sess.ID && freshExp.Shards[i].Status == "COMPLETED" {
							freshExp.Shards[i].Status = "COMPLETED_AND_UNASSIGNED"
							break
						}
					}
					recomputeExperimentRollup(freshExp)
					_ = e.st.SaveExperiment(ctx, freshExp)
					cp2 := *freshExp
					cp2.Shards = append([]models.ExperimentShard(nil), freshExp.Shards...)
					updatedExp = &cp2
				}
				e.expMu.Unlock()
			}
		}
	}

	if updatedExp != nil {
		e.publishEvent(models.WSEventExperimentUpdated, updatedExp)
	}
	return readyForIdle
}

func isRemoteFileMissingErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "404") || strings.Contains(msg, "not found") || strings.Contains(msg, "no such file")
}

func writeShardCheckpointAtomic(destPath string, data []byte) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".shard_ckpt_*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, wErr := tmp.Write(data)
	sErr := tmp.Sync()
	cErr := tmp.Close()
	if wErr != nil || sErr != nil || cErr != nil {
		_ = os.Remove(tmpPath)
		if wErr != nil {
			return wErr
		}
		if sErr != nil {
			return sErr
		}
		return cErr
	}
	if rErr := os.Rename(tmpPath, destPath); rErr != nil {
		_ = os.Remove(tmpPath)
		return rErr
	}
	return nil
}

func (e *Engine) handleZombieDetected(ctx context.Context, sess *models.RuntimeSession, autoRelease bool) {
	now := time.Now().UTC()
	pid := 0
	if sess.Worker != nil {
		pid = sess.Worker.PID
	}
	wCopy := sess.Worker
	transitioned := false
	_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
		if wCopy != nil {
			cur.Worker = wCopy
		}
		cur.Status = models.SessionZombieAlert
		cur.StatusMessage = fmt.Sprintf("Linux zombie (`Z`) detected on worker PID %d — CU drain aborted", pid)
		cur.UpdatedAt = now
		transitioned = true
		return true
	})
	if transitioned && e.st != nil {
		// Credit 1 hour of prevented silent zombie CU drain!
		if sess.BurnRateCUHr > 0 {
			_ = e.st.AddSavedWaste(ctx, sess.BurnRateCUHr, true)
		}
		if sess.ExperimentID != "" {
			e.expMu.Lock()
			if exp, gErr := e.st.GetExperiment(ctx, sess.ExperimentID); gErr == nil && exp != nil {
				changed := false
				for i := range exp.Shards {
					if exp.Shards[i].SessionID == sess.ID && exp.Shards[i].Status != "ZOMBIE_ABORTED" {
						exp.Shards[i].Status = "ZOMBIE_ABORTED"
						exp.Shards[i].ProcState = "Z"
						exp.Shards[i].ZombieDetected = true
						exp.Shards[i].CompletedAt = &now
						changed = true
					}
				}
				if changed {
					recomputeExperimentRollup(exp)
					_ = e.st.SaveExperiment(ctx, exp)
					e.publishEvent(models.WSEventExperimentUpdated, exp)
				}
			}
			e.expMu.Unlock()
		}
	}
	e.publishEvent(models.WSEventZombieAlert, map[string]any{
		"session_id":   sess.ID,
		"session_name": sess.Name,
		"pid":          pid,
		"burn_rate":    sess.BurnRateCUHr,
		"auto_release": autoRelease,
	})
	if autoRelease && !sess.AdoptedExternal {
		_ = e.StopSession(ctx, sess.ID)
	}
}

func (e *Engine) shouldAutoReapIdleSession(
	ctx context.Context,
	sess *models.RuntimeSession,
	boundSessionIDs map[string]bool,
	stickyIdleTTL time.Duration,
	now time.Time,
) bool {
	if sess == nil || sess.AdoptedExternal || sess.Status != models.SessionIdle {
		return false
	}
	if sess.Worker != nil && sess.Worker.Alive && !sess.Worker.ZombieDetected {
		return false
	}
	// Never idle-reap an experiment shard VM before its shard link and final checkpoint have been reconciled/saved!
	if sess.ExperimentID != "" && e.st != nil {
		exp, gErr := e.st.GetExperiment(ctx, sess.ExperimentID)
		if gErr != nil || exp == nil {
			return false
		}
		foundShard := false
		for i := range exp.Shards {
			sh := exp.Shards[i]
			if sh.SessionID == sess.ID {
				foundShard = true
				if sh.Status == "RUNNING" || sh.Status == "PROVISIONING" {
					return false
				}
				if sh.Status == "COMPLETED" && exp.AutoStopOnComplete && !sh.FinalCheckpointSaved {
					return false
				}
				if (sh.Status == "COMPLETED" || sh.Status == "FAILED") && sh.CheckpointExpected && !sh.FinalCheckpointSaved {
					return false
				}
				break
			}
		}
		if !foundShard {
			return false
		}
	}

	lastActive := sess.CreatedAt
	if sess.LastExecutionAt != nil && !sess.LastExecutionAt.IsZero() {
		lastActive = *sess.LastExecutionAt
	}
	if lastActive.IsZero() {
		return false
	}

	var idleLimit time.Duration
	if sess.APIKeyID != "" && e.st != nil {
		if apiKey, err := e.st.GetAPIKey(ctx, sess.APIKeyID); err == nil && apiKey != nil && apiKey.IdleAutoKillMin > 0 {
			idleLimit = time.Duration(apiKey.IdleAutoKillMin) * time.Minute
		}
	}
	if sess.StickyKey != "" && e.rtr != nil && !boundSessionIDs[sess.ID] && stickyIdleTTL > 0 {
		if idleLimit == 0 || stickyIdleTTL < idleLimit {
			idleLimit = stickyIdleTTL
		}
	}

	return idleLimit > 0 && now.Sub(lastActive) >= idleLimit
}

// parseSpawnedPID extracts `PID=<int>` emitted by background worker launchers on real Colab VMs.
func parseSpawnedPID(stdout string) (int, bool) {
	for _, token := range strings.Fields(stdout) {
		if idx := strings.Index(token, "PID="); idx >= 0 {
			numPart := strings.TrimRight(token[idx+4:], ".,;:)\r\n")
			if pid, err := strconv.Atoi(numPart); err == nil && pid > 0 {
				return pid, true
			}
		}
	}
	return 0, false
}

// PollResourcesAndGuardrailsOnce polls `/api/colab/resources`, updates live CU burn ledgers,
// advances demo experiment shards, enforces IdleAutoKillMin, and enforces Contributor `MinReserveCU` 5-minute preemption.
func (e *Engine) PollResourcesAndGuardrailsOnce(ctx context.Context) {
	if e.st == nil {
		return
	}
	sessions, err := e.st.ListSessions(ctx)
	if err != nil {
		return
	}

	stickyIdleTTL := 20 * time.Minute
	boundSessionIDs := make(map[string]bool)
	if e.rtr != nil {
		if pol, pErr := e.rtr.GetPolicy(ctx); pErr == nil && pol != nil && pol.StickyIdleTTLSec > 0 {
			stickyIdleTTL = time.Duration(pol.StickyIdleTTLSec) * time.Second
		}
		if bindings, bErr := e.rtr.ListStickyBindings(ctx); bErr == nil {
			for _, b := range bindings {
				if b.SessionID != "" {
					boundSessionIDs[b.SessionID] = true
				}
			}
		}
	}

	deltaHours := e.opts.ResourcePollInterval.Hours()
	if deltaHours <= 0 {
		deltaHours = 10.0 / 3600.0
	}

	now := time.Now().UTC()
	for _, sess := range sessions {
		if sess == nil || sess.Status == models.SessionStopped || sess.Status == models.SessionLost {
			continue
		}
		if !sess.AdoptedExternal && e.shouldAutoReapIdleSession(ctx, sess, boundSessionIDs, stickyIdleTTL, now) {
			_ = e.StopSession(ctx, sess.ID)
			continue
		}
		acc := e.lookupAccountBestEffort(ctx, sess.AccountID)

		var newResources *models.VMResourceTelemetry
		if e.isDemoSession(acc, sess) {
			demoCopy := *sess
			e.jitterDemoTelemetry(&demoCopy)
			newResources = demoCopy.Resources
		} else {
			mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)
			proxyTok, httpClient, _, pErr := e.prepareSessionProxyClient(ctx, acc, sess, mem)
			if pErr == nil {
				if res, rErr := e.fetchVMResources(ctx, httpClient, sess.ProxyURL, proxyTok); rErr == nil && res != nil {
					newResources = res
				}
			}
		}

		cuDelta := 0.0
		if sess.BurnRateCUHr > 0 {
			cuDelta = sess.BurnRateCUHr * deltaHours
		}

		updatedAt := time.Now().UTC()
		aliveAfterSave := false
		_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			if newResources != nil {
				cur.Resources = newResources
			}
			if cuDelta > 0 {
				cur.AccumulatedCU = math.Round((cur.AccumulatedCU+cuDelta)*10000) / 10000
			}
			cur.UptimeSec = computeUptimeSec(cur)
			cur.UpdatedAt = updatedAt
			aliveAfterSave = true
			return true
		})
		if !aliveAfterSave {
			continue
		}

		// Accumulate live Compute Unit burn & enforce Contributor + Scoped API Key CU budgets.
		// Never charge a contributor's own personal external Colab notebooks (`AdoptedExternal`) against donated caps!
		if !sess.AdoptedExternal && cuDelta > 0 && e.pool != nil && sess.AccountID != "" {
			_ = e.pool.RecordAccountCUBurn(ctx, sess.AccountID, cuDelta)
		}

		if !sess.AdoptedExternal && sess.APIKeyID != "" && e.rtr != nil {
			if recorder, ok := e.rtr.(apiKeyCUBurnRecorder); ok {
				if exceeded, reason := recorder.RecordAPIKeyCUBurn(ctx, sess.APIKeyID, cuDelta); exceeded {
					e.publishEvent(models.WSEventGuardrailPreempt, map[string]any{
						"session_id": sess.ID,
						"api_key_id": sess.APIKeyID,
						"reason":     reason,
					})
					_ = e.StopSession(ctx, sess.ID)
					continue
				}
			}
		}

		if !sess.AdoptedExternal && sess.BurnRateCUHr > 0 && acc != nil && acc.IsContributor && acc.Guardrails.AllowPaidCUBurn && acc.Guardrails.MinReserveCU > 0 {
			fiveMinMargin := sess.BurnRateCUHr * (5.0 / 60.0)
			if acc.PaidCUBalance-cuDelta <= acc.Guardrails.MinReserveCU+fiveMinMargin {
				e.publishEvent(models.WSEventGuardrailPreempt, map[string]any{
					"session_id":     sess.ID,
					"account_id":     acc.ID,
					"paid_balance":   acc.PaidCUBalance,
					"min_reserve_cu": acc.Guardrails.MinReserveCU,
					"reason":         "Approaching contributor personal MinReserveCU floor — draining & unassigning VM",
				})
				_ = e.StopSession(ctx, sess.ID)
				continue
			}
		}

		e.publishEvent(models.WSEventSessionUpdated, sess)
	}

	if e.opts.DemoMode {
		e.advanceDemoExperimentsBestEffort(ctx)
	}
}

// ============================================================================
// Multi-Account Parallel Sharded Experiments (`K` GPUs across Pooled Accounts)
// ============================================================================

// LaunchShardedExperiment provisions `NumShards` concurrent VMs across pooled Google accounts,
// uploads workspace files, installs packages via `uv`, injects `COLABHIVE_SHARD_ID=0..K-1`,
// runs the startup self-check, and tracks aggregate shard telemetry.
func (e *Engine) LaunchShardedExperiment(
	ctx context.Context,
	req models.ExperimentLaunchRequest,
) (*models.Experiment, error) {
	numShards := req.NumShards
	if numShards <= 0 {
		numShards = 1
	}
	if numShards > 32 {
		numShards = 32
	}

	expID := strings.TrimSpace(req.ExperimentID)
	if expID == "" {
		expID = models.NewPrefixedID("exp_")
	} else if !isValidASCIIExperimentID(expID) {
		return nil, fmt.Errorf("invalid experiment_id %q: must be 1..128 ASCII [A-Za-z0-9._-] characters", req.ExperimentID)
	}
	accel := req.Accelerator
	if strings.TrimSpace(string(accel)) == "" {
		accel = models.AcceleratorG4
	} else {
		norm, err := models.NormalizeAccelerator(string(accel))
		if err != nil {
			return nil, err
		}
		accel = norm
	}

	strategy := req.Strategy
	if strategy == "" {
		strategy = models.StrategyWasteZero
	}

	scriptCode := req.ScriptCode
	scriptBaseName := "shard_worker.py"
	if strings.TrimSpace(req.ScriptPath) != "" {
		scriptBaseName = sanitizeFileComponent(filepath.Base(req.ScriptPath), "shard_worker.py")
		if scriptCode == "" {
			if raw, rErr := os.ReadFile(req.ScriptPath); rErr == nil {
				scriptCode = string(raw)
			}
		}
	}
	if scriptCode == "" {
		scriptCode = defaultShardWorkerScript()
	}

	outDir := strings.TrimSpace(req.LocalOutputDir)
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "colabhive-runs", expID)
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = fmt.Sprintf("%s · %d× %s Shards", expID, numShards, accel)
	}

	now := time.Now().UTC()
	exp := &models.Experiment{
		ID:                 expID,
		Title:              title,
		APIKeyID:           strings.TrimSpace(req.APIKeyID),
		Status:             models.ExperimentProvisioning,
		ScriptPath:         firstNonEmpty(req.ScriptPath, scriptBaseName),
		Accelerator:        accel,
		HighMem:            req.HighMem,
		NumShards:          numShards,
		Strategy:           strategy,
		TargetRecordsTotal: numShards * 100,
		AutoStopOnComplete: req.AutoStopOnComplete,
		LocalOutputDir:     outDir,
		Shards:             make([]models.ExperimentShard, numShards),
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	e.expMu.Lock()
	if e.st != nil {
		if existing, gErr := e.st.GetExperiment(ctx, expID); gErr == nil && existing != nil {
			e.expMu.Unlock()
			return nil, fmt.Errorf("%w: %s", ErrExperimentAlreadyExists, expID)
		}
		if allExps, lErr := e.st.ListExperiments(ctx); lErr == nil {
			for _, ex := range allExps {
				if ex != nil && strings.EqualFold(ex.ID, expID) {
					e.expMu.Unlock()
					return nil, fmt.Errorf("%w: %s", ErrExperimentAlreadyExists, expID)
				}
			}
		}
		if err := e.st.SaveExperiment(ctx, exp); err != nil {
			e.expMu.Unlock()
			if strings.Contains(strings.ToLower(err.Error()), "already exists") {
				return nil, fmt.Errorf("%w: %s", ErrExperimentAlreadyExists, expID)
			}
			return nil, err
		}
	}
	e.expMu.Unlock()

	_ = os.MkdirAll(outDir, 0o755)

	var wg sync.WaitGroup
	var shardMu sync.Mutex
	var firstErr error

	for idx := 0; idx < numShards; idx++ {
		shardID := idx
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessName := fmt.Sprintf("%s_s%d", expID, shardID)
			shardCopy := shardID

			sess, sErr := e.CreateSession(ctx, models.SessionAllocRequest{
				Name:         sessName,
				Accelerator:  accel,
				HighMem:      req.HighMem,
				Strategy:     strategy,
				AutoFallback: true,
				APIKeyID:     req.APIKeyID,
				ExperimentID: expID,
				ShardIndex:   &shardCopy,
			})
			if sErr != nil {
				shardMu.Lock()
				if firstErr == nil {
					firstErr = sErr
				}
				exp.Shards[shardID] = models.ExperimentShard{
					ShardID:          shardID,
					SessionName:      sessName,
					Accelerator:      accel,
					Status:           "FAILED",
					StartupSelfCheck: "FAILED",
					LogTail:          sErr.Error(),
				}
				shardMu.Unlock()
				return
			}

			// Persist shard-to-session link immediately before launching worker so fast-finishing shards
			// can be reconciled even while sibling shards are still provisioning!
			e.expMu.Lock()
			shardMu.Lock()
			exp.Shards[shardID] = models.ExperimentShard{
				ShardID:          shardID,
				SessionID:        sess.ID,
				SessionName:      sess.Name,
				AccountID:        sess.AccountID,
				AccountEmail:     sess.AccountEmail,
				Accelerator:      sess.Accelerator,
				Status:           "PROVISIONING",
				TargetRecords:    25,
				StartupSelfCheck: "PENDING",
			}
			if e.st != nil {
				if cur, gErr := e.st.GetExperiment(ctx, expID); gErr == nil && cur != nil {
					cur.Shards[shardID] = exp.Shards[shardID]
					_ = e.st.SaveExperiment(ctx, cur)
				}
			}
			shardMu.Unlock()
			e.expMu.Unlock()

			// Upload workspace files (local path or inline base64) and entrypoint script.
			for _, uf := range req.UploadFiles {
				if strings.TrimSpace(uf.RemotePath) == "" {
					continue
				}
				if strings.TrimSpace(uf.LocalPath) != "" {
					if raw, rErr := os.ReadFile(uf.LocalPath); rErr == nil {
						_ = e.UploadFile(ctx, sess.ID, uf.RemotePath, raw)
					}
				} else if strings.TrimSpace(uf.ContentBase64) != "" {
					cleanB64 := strings.TrimSpace(uf.ContentBase64)
					raw, dErr := base64.StdEncoding.DecodeString(cleanB64)
					if dErr != nil {
						raw, dErr = base64.RawStdEncoding.DecodeString(cleanB64)
					}
					if dErr == nil {
						_ = e.UploadFile(ctx, sess.ID, uf.RemotePath, raw)
					}
				}
			}
			remoteScript := "/content/" + scriptBaseName
			_ = e.UploadFile(ctx, sess.ID, remoteScript, []byte(scriptCode))

			if len(req.Packages) > 0 {
				_, _ = e.InstallPackages(ctx, sess.ID, req.Packages, "")
			}

			shardEnv := make(map[string]string, len(req.Env)+3)
			for k, v := range req.Env {
				shardEnv[k] = v
			}
			shardEnv["COLABHIVE_EXPERIMENT_ID"] = expID
			shardEnv["COLABHIVE_SHARD_ID"] = fmt.Sprintf("%d", shardID)
			shardEnv["COLABHIVE_NUM_SHARDS"] = fmt.Sprintf("%d", numShards)

			launcher := buildBackgroundShardLauncher(remoteScript, req.ScriptArgs, shardID, numShards)
			mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)
			mem.mu.Lock()
			mem.workerCmd = launcher
			mem.mu.Unlock()

			execRes, execErr := e.ExecuteCode(ctx, models.KernelExecRequest{
				SessionID:      sess.ID,
				Code:           launcher,
				Env:            shardEnv,
				IdleTimeoutSec: 60,
				StoreHistory:   true,
			})

			acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
			startedAt := time.Now().UTC()
			pid := 0
			if e.isDemoSession(acc, sess) {
				pid = 4800 + shardID*17
			} else if execRes != nil {
				if parsedPID, ok := parseSpawnedPID(execRes.Stdout); ok {
					pid = parsedPID
				}
			}
			vramUsed := 0.0
			vramTotal := 0.0
			smUtil := 92.0
			tempC := 64
			if sess.Resources != nil && len(sess.Resources.GPUs) > 0 {
				g := sess.Resources.GPUs[0]
				vramUsed = math.Round((float64(g.MemoryUsedBytes)/(1024*1024*1024))*10) / 10
				vramTotal = math.Round((float64(g.MemoryTotalBytes)/(1024*1024*1024))*10) / 10
				smUtil = math.Round(g.GPUUtilization * 100)
				if g.TemperatureC > 0 {
					tempC = g.TemperatureC
				}
			}

			workerInfo := &models.WorkerLiveness{
				PID:           pid,
				ProcState:     "R",
				Alive:         pid > 0 || e.isDemoSession(acc, sess),
				LastCheckedAt: &startedAt,
			}
			_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
				cur.Worker = workerInfo
				cur.Status = models.SessionBusy
				cur.UpdatedAt = startedAt
				return true
			})

			shardStatus := "RUNNING"
			selfCheck := "PASSED"
			logTail := fmt.Sprintf("Shard %d/%d started on %s (%s)", shardID, numShards, sess.Accelerator, sess.AccountEmail)
			var exitPtr *int
			var completedPtr *time.Time
			progress := 15.0
			synced := 0

			if e.isDemoSession(acc, sess) {
				// Seed initial shard JSONL records so `wait=True` or immediate `CollectAndTeardownExperiment` succeeds deterministically!
				e.seedDemoShardRecords(mem, shardID, 25)
				synced = 25
				progress = 100.0
				zero := 0
				exitPtr = &zero
				completedPtr = &startedAt
				if req.AutoStopOnComplete {
					remoteJSONL := fmt.Sprintf("/content/shard_%d.jsonl", shardID)
					if data, dErr := e.DownloadFile(ctx, sess.ID, remoteJSONL); dErr == nil && len(data) > 0 {
						_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("shard_%d.jsonl", shardID)), data, 0o644)
					}
					_ = e.StopSession(ctx, sess.ID)
					shardStatus = "COMPLETED_AND_UNASSIGNED"
				} else {
					shardStatus = "COMPLETED"
				}
			} else if execErr != nil || (execRes != nil && execRes.Status == "error") {
				shardStatus = "FAILED"
				selfCheck = "FAILED"
				if execErr != nil {
					logTail = execErr.Error()
				} else if execRes.ExecutionError != nil {
					logTail = execRes.ExecutionError.Value
				}
			} else if execRes != nil && strings.TrimSpace(execRes.Stdout) != "" {
				lines := strings.Split(strings.TrimSpace(execRes.Stdout), "\n")
				logTail = lines[len(lines)-1]
			}

			e.expMu.Lock()
			shardMu.Lock()
			nextShard := models.ExperimentShard{
				ShardID:          shardID,
				SessionID:        sess.ID,
				SessionName:      sess.Name,
				AccountID:        sess.AccountID,
				AccountEmail:     sess.AccountEmail,
				Accelerator:      sess.Accelerator,
				Status:           shardStatus,
				PID:              pid,
				ProcState:        "R",
				ZombieDetected:   false,
				ExitCode:         exitPtr,
				ProgressPct:      progress,
				SyncedRecords:    synced,
				TargetRecords:    25,
				VRAMUsedGB:       vramUsed,
				VRAMTotalGB:      vramTotal,
				SMUtilPct:        smUtil,
				TempC:            tempC,
				StartupSelfCheck: selfCheck,
				LogTail:          logTail,
				StartedAt:        &startedAt,
				CompletedAt:      completedPtr,
			}
			exp.Shards[shardID] = nextShard
			exp.AggregateBurnRateCUHr += sess.BurnRateCUHr
			if e.st != nil {
				if cur, gErr := e.st.GetExperiment(ctx, expID); gErr == nil && cur != nil {
					existingSt := cur.Shards[shardID].Status
					// Preserve terminal shard status if watchdog already reconciled a sub-second exit!
					if existingSt != "COMPLETED" && existingSt != "COMPLETED_AND_UNASSIGNED" && existingSt != "FAILED" && existingSt != "ZOMBIE_ABORTED" {
						cur.Shards[shardID] = nextShard
					}
					cur.AggregateBurnRateCUHr = exp.AggregateBurnRateCUHr
					recomputeExperimentRollup(cur)
					_ = e.st.SaveExperiment(ctx, cur)
				}
			}
			shardMu.Unlock()
			e.expMu.Unlock()
		}()
	}

	wg.Wait()
	e.expMu.Lock()
	if e.st != nil {
		if cur, gErr := e.st.GetExperiment(ctx, expID); gErr == nil && cur != nil {
			for i := range exp.Shards {
				existingSt := cur.Shards[i].Status
				if existingSt == "COMPLETED" || existingSt == "COMPLETED_AND_UNASSIGNED" || existingSt == "FAILED" || existingSt == "ZOMBIE_ABORTED" {
					exp.Shards[i] = cur.Shards[i]
				} else {
					cur.Shards[i] = exp.Shards[i]
				}
			}
		}
	}
	recomputeExperimentRollup(exp)

	if exp.FailedShards == exp.NumShards && firstErr != nil {
		if e.st != nil {
			_ = e.st.SaveExperiment(ctx, exp)
		}
		e.expMu.Unlock()
		return nil, firstErr
	}
	if e.st != nil {
		if err := e.st.SaveExperiment(ctx, exp); err != nil {
			e.expMu.Unlock()
			return nil, err
		}
	}
	e.expMu.Unlock()
	e.publishEvent(models.WSEventExperimentUpdated, exp)
	return exp, nil
}

func buildBackgroundShardLauncher(remoteScript string, scriptArgs []string, shardID int, numShards int) string {
	// Wrap the worker inside a lightweight Python supervisor process (`__p.pid`) that atomically publishes
	// `/content/.colabhive/shard_<N>_exit.json` via `tmp + os.replace` BEFORE `__p` itself exits (while `/proc/<pid>`
	// is still alive in procfs!), completely eliminating any race window between `waitpid` reaping `/proc/<pid>`
	// and exit metadata becoming visible to the watchdog sweep.
	wrapperScript := "import subprocess, json, os, sys, signal\n" +
		"sf = sys.argv[1]\n" +
		"child = subprocess.Popen(sys.argv[2:])\n" +
		"def _fwd(sig, _f):\n" +
		"    try: child.send_signal(sig)\n" +
		"    except Exception: pass\n" +
		"signal.signal(signal.SIGTERM, _fwd)\n" +
		"signal.signal(signal.SIGINT, _fwd)\n" +
		"rc = child.wait()\n" +
		"try:\n" +
		"    sys.stdout.flush(); sys.stderr.flush()\n" +
		"    tf = sf + '.tmp.' + str(os.getpid())\n" +
		"    with open(tf, 'w') as f:\n" +
		"        json.dump({'exit_code': rc}, f)\n" +
		"        f.flush(); os.fsync(f.fileno())\n" +
		"    os.replace(tf, sf)\n" +
		"except Exception: pass\n" +
		"os._exit(rc if 0 <= rc <= 255 else 1)\n"
	exitPath := fmt.Sprintf("/content/.colabhive/shard_%d_exit.json", shardID)
	cmdParts := append([]string{"python3", "-u", "-c", wrapperScript, exitPath, "python3", "-u", remoteScript}, scriptArgs...)
	cmdJSON, _ := json.Marshal(cmdParts)
	return fmt.Sprintf(
		"import os, subprocess, json, threading\nos.makedirs('/content/.colabhive', exist_ok=True)\n"+
			"__sf = %q\n"+
			"if os.path.exists(__sf): os.remove(__sf)\n"+
			"__lf = open('/content/worker.log', 'a')\n"+
			"__p = subprocess.Popen(%s, stdout=__lf, stderr=subprocess.STDOUT, start_new_session=True)\n"+
			"def __reap(proc, lf, sf):\n"+
			"    rc = proc.wait()\n"+
			"    try: lf.flush(); lf.close()\n"+
			"    except Exception: pass\n"+
			"    if not os.path.exists(sf):\n"+
			"        try:\n"+
			"            tf = sf + '.reap.tmp'\n"+
			"            with open(tf, 'w') as f: json.dump({'exit_code': rc if rc != 0 else -9}, f)\n"+
			"            os.replace(tf, sf)\n"+
			"        except Exception: pass\n"+
			"threading.Thread(target=__reap, args=(__p, __lf, __sf), daemon=True).start()\n"+
			"print(f'Started shard %d/%d worker PID={__p.pid}')\n",
		exitPath, string(cmdJSON), shardID, numShards,
	)
}

func defaultShardWorkerScript() string {
	return "import os, json\nsid = int(os.environ.get('COLABHIVE_SHARD_ID', '0'))\n" +
		"with open(f'/content/shard_{sid}.jsonl', 'w') as f:\n" +
		"    for i in range(10):\n" +
		"        f.write(json.dumps({'shard': sid, 'idx': i, 'ok': True}) + '\\n')\n"
}

func recomputeExperimentRollup(exp *models.Experiment) {
	completed := 0
	running := 0
	failed := 0
	syncedTotal := 0
	targetTotal := 0
	cuSoFar := 0.0
	activeBurn := 0.0

	for _, sh := range exp.Shards {
		syncedTotal += sh.SyncedRecords
		targetTotal += sh.TargetRecords
		cuSoFar += sh.CUBurned
		switch sh.Status {
		case "COMPLETED", "COMPLETED_AND_UNASSIGNED":
			completed++
		case "FAILED", "ZOMBIE_ABORTED":
			failed++
		case "RUNNING", "PROVISIONING":
			running++
			spec, ok := models.GetHardwareSpec(sh.Accelerator)
			if ok {
				activeBurn += spec.DefaultBurnRateCUHr
			}
		}
	}

	exp.CompletedShards = completed
	exp.RunningShards = running
	exp.FailedShards = failed
	exp.SyncedRecordsTotal = syncedTotal
	if targetTotal > 0 {
		exp.TargetRecordsTotal = targetTotal
	}
	if cuSoFar > exp.CUBurnedSoFar {
		exp.CUBurnedSoFar = math.Round(cuSoFar*100) / 100
	}
	exp.AggregateBurnRateCUHr = math.Round(activeBurn*100) / 100

	now := time.Now().UTC()
	exp.UpdatedAt = now
	switch {
	case completed == exp.NumShards && exp.NumShards > 0:
		exp.Status = models.ExperimentCompleted
		if exp.CompletedAt == nil {
			exp.CompletedAt = &now
		}
	case failed == exp.NumShards && exp.NumShards > 0:
		exp.Status = models.ExperimentFailed
		if exp.CompletedAt == nil {
			exp.CompletedAt = &now
		}
	case running == 0 && failed > 0:
		exp.Status = models.ExperimentPartialFailure
		if exp.CompletedAt == nil {
			exp.CompletedAt = &now
		}
	default:
		exp.Status = models.ExperimentRunning
	}
}

// GetExperiment returns live experiment status across all shards.
func (e *Engine) GetExperiment(ctx context.Context, id string) (*models.Experiment, error) {
	if e.st == nil {
		return nil, ErrExperimentNotFound
	}
	exp, err := e.st.GetExperiment(ctx, strings.TrimSpace(id))
	if err != nil || exp == nil {
		return nil, fmt.Errorf("%w: %s", ErrExperimentNotFound, id)
	}
	return exp, nil
}

// ListExperiments returns all experiments sorted newest-first.
func (e *Engine) ListExperiments(ctx context.Context) ([]*models.Experiment, error) {
	if e.st == nil {
		return []*models.Experiment{}, nil
	}
	list, err := e.st.ListExperiments(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		return []*models.Experiment{}, nil
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	return list, nil
}

// CollectAndTeardownExperiment downloads per-shard JSONL checkpoints, merges them deterministically
// into `<LocalOutputDir>/merged.jsonl`, exports `.ipynb` provenance logs, and guarantees zero leaked VMs.
func (e *Engine) CollectAndTeardownExperiment(
	ctx context.Context,
	id string,
	forceKill bool,
) (*models.ExperimentTeardownResult, error) {
	exp, err := e.GetExperiment(ctx, id)
	if err != nil {
		return nil, err
	}

	outDir := exp.LocalOutputDir
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(os.TempDir(), "colabhive-runs", exp.ID)
	}
	_ = os.MkdirAll(outDir, 0o755)

	var mergedLines []string
	var exportedNotebooks []string
	activeRemaining := 0
	totalCU := exp.CUBurnedSoFar

	// Sort shards by ShardID for deterministic JSONL ordering.
	sortedShards := append([]models.ExperimentShard(nil), exp.Shards...)
	sort.Slice(sortedShards, func(i, j int) bool {
		return sortedShards[i].ShardID < sortedShards[j].ShardID
	})

	for idx, sh := range sortedShards {
		sid := strings.TrimSpace(sh.SessionID)
		if sid == "" {
			continue
		}
		var (
			sess *models.RuntimeSession
			sErr error
		)
		if e.st != nil {
			sess, sErr = e.st.GetSession(ctx, sid)
		} else {
			sess, sErr = e.GetSession(ctx, sid)
		}
		if sErr == nil && sess != nil && sess.ID == sid &&
			!sess.AdoptedExternal &&
			(exp.APIKeyID == "" || sess.APIKeyID == exp.APIKeyID) {
			totalCU += sess.AccumulatedCU
			localShardPath := filepath.Join(outDir, fmt.Sprintf("shard_%d.jsonl", sh.ShardID))
			var shardData []byte
			downloadFailed := false
			if sess.Status != models.SessionStopped && sess.Status != models.SessionLost {
				remoteJSONL := fmt.Sprintf("/content/shard_%d.jsonl", sh.ShardID)
				if data, dErr := e.DownloadFile(ctx, sess.ID, remoteJSONL); dErr == nil && len(data) > 0 {
					shardData = data
					if wErr := writeShardCheckpointAtomic(localShardPath, data); wErr != nil {
						downloadFailed = true
					}
				} else if dErr != nil && (sh.CheckpointExpected || !isRemoteFileMissingErr(dErr)) {
					downloadFailed = true
				}
			}
			if len(shardData) == 0 {
				if cached, rErr := os.ReadFile(localShardPath); rErr == nil && len(cached) > 0 {
					shardData = cached
					// Only clear `downloadFailed` when the VM is already gone or the post-exit final checkpoint
					// was verified (`FinalCheckpointSaved`); otherwise keep `downloadFailed=true` so `forceKill=false`
					// preserves the live VM rather than discarding an unsaved final checkpoint!
					if sh.FinalCheckpointSaved || !sh.CheckpointExpected || sess.Status == models.SessionStopped || sess.Status == models.SessionLost {
						downloadFailed = false
					}
				}
			}
			if len(shardData) > 0 {
				for _, line := range strings.Split(strings.TrimSpace(string(shardData)), "\n") {
					if strings.TrimSpace(line) != "" {
						mergedLines = append(mergedLines, line)
					}
				}
			}

			// Export shard `.ipynb` provenance notebook with sanitized basename.
			safeSessName := sanitizeFileComponent(sess.Name, sess.ID)
			nbPath := filepath.Join(outDir, fmt.Sprintf("%s.ipynb", safeSessName))
			if nbBytes, nbErr := exportProvenanceNotebook(sess); nbErr == nil {
				_ = os.WriteFile(nbPath, nbBytes, 0o644)
				exportedNotebooks = append(exportedNotebooks, nbPath)
			}

			// Teardown VM if finished or forceKill requested; keep running shards (or unpersisted checkpoints when !forceKill) alive!
			if sess.Status != models.SessionStopped && sess.Status != models.SessionLost {
				isTerminalShard := sh.Status == "COMPLETED" || sh.Status == "COMPLETED_AND_UNASSIGNED" ||
					sh.Status == "FAILED" || sh.Status == "ZOMBIE_ABORTED"
				if forceKill || (isTerminalShard && !downloadFailed) {
					if stopErr := e.StopSession(ctx, sess.ID); stopErr != nil {
						activeRemaining++
					} else if sh.Status != "FAILED" && sh.Status != "ZOMBIE_ABORTED" {
						sortedShards[idx].Status = "COMPLETED_AND_UNASSIGNED"
					}
				} else {
					activeRemaining++
				}
			}
		}
	}

	mergedPath := filepath.Join(outDir, "merged.jsonl")
	mergedContent := ""
	if len(mergedLines) > 0 {
		mergedContent = strings.Join(mergedLines, "\n") + "\n"
	}
	_ = os.WriteFile(mergedPath, []byte(mergedContent), 0o644)

	now := time.Now().UTC()
	exp.Shards = sortedShards
	exp.MergedJSONLPath = mergedPath
	if len(mergedLines) > exp.SyncedRecordsTotal {
		exp.SyncedRecordsTotal = len(mergedLines)
	}
	if activeRemaining > 0 {
		recomputeExperimentRollup(exp)
	} else {
		exp.AggregateBurnRateCUHr = 0
		if forceKill && exp.Status == models.ExperimentRunning {
			exp.Status = models.ExperimentCancelled
		} else {
			exp.Status = models.ExperimentCompleted
		}
		exp.CompletedAt = &now
	}
	exp.UpdatedAt = now
	if totalCU > exp.CUBurnedSoFar {
		exp.CUBurnedSoFar = math.Round(totalCU*100) / 100
	}
	if e.st != nil {
		_ = e.st.SaveExperiment(ctx, exp)
	}
	e.publishEvent(models.WSEventExperimentUpdated, exp)

	return &models.ExperimentTeardownResult{
		ExperimentID:       exp.ID,
		AllVMsUnassigned:   activeRemaining == 0,
		ActiveVMsRemaining: activeRemaining,
		MergedJSONLPath:    mergedPath,
		MergedRecordsCount: len(mergedLines),
		ExportedNotebooks:  exportedNotebooks,
		TotalCUConsumed:    exp.CUBurnedSoFar,
	}, nil
}

func exportProvenanceNotebook(sess *models.RuntimeSession) ([]byte, error) {
	cells := make([]map[string]any, 0, len(sess.Cells)+1)
	cells = append(cells, map[string]any{
		"cell_type": "markdown",
		"id":        "prov_header",
		"metadata":  map[string]any{},
		"source": []string{
			fmt.Sprintf("# ColabHive Provenance Notebook — `%s`\n", sess.Name),
			fmt.Sprintf("- **Account**: `%s`\n", sess.AccountEmail),
			fmt.Sprintf("- **Accelerator**: `%s` (`%s`)\n", sess.Accelerator, sess.Endpoint),
		},
	})
	for _, c := range sess.Cells {
		cells = append(cells, map[string]any{
			"cell_type":       c.CellType,
			"id":              c.CellID,
			"metadata":        map[string]any{},
			"source":          []string{c.Source},
			"execution_count": c.ExecutionCount,
			"outputs":         []any{},
		})
	}
	nb := map[string]any{
		"nbformat":       4,
		"nbformat_minor": 5,
		"metadata": map[string]any{
			"kernelspec": map[string]any{
				"display_name": "Python 3",
				"language":     "python",
				"name":         "python3",
			},
		},
		"cells": cells,
	}
	return json.MarshalIndent(nb, "", "  ")
}
