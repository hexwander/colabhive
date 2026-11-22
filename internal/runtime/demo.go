package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/colabhive/colabhive/internal/models"
)

func defaultDemoFiles() map[string][]byte {
	return map[string][]byte{
		"/content/sample_data/README.md": []byte("ColabHive Virtual /content Workspace\n"),
		"/content/sample_data/california_housing_train.csv": []byte(
			"longitude,latitude,housing_median_age,total_rooms,median_house_value\n-114.31,34.19,15.0,5612.0,66900.0\n-114.47,34.40,19.0,7650.0,80100.0\n",
		),
		"/content/worker.log": []byte(
			"[15:14:02] [colabhive] Attached persistent Python 3.12 kernel on /content\n" +
				"[15:14:05] [watchdog] Verified /proc/4812/stat != Z (Keepalive 8s active)\n",
		),
	}
}

func (e *Engine) provisionDemoSession(
	ctx context.Context,
	acc *models.Account,
	tier models.AcceleratorType,
	requestedTier models.AcceleratorType,
	shape models.MachineShape,
	spec models.HardwareSpec,
	sessName string,
	nbh string,
	req models.SessionAllocRequest,
) (*models.RuntimeSession, error) {
	now := time.Now().UTC()
	shortHex := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	endpoint := fmt.Sprintf("demo-%s-s-%s", strings.ToLower(string(tier)), shortHex)
	tokExp := now.Add(1 * time.Hour)

	res := buildDemoTelemetry(tier, spec, 0.54, 0.91, 64)
	sess := &models.RuntimeSession{
		ID:                   models.NewPrefixedID("sess_"),
		Name:                 sessName,
		AccountID:            acc.ID,
		AccountEmail:         acc.Email,
		Backend:              models.BackendTunnelAPI,
		Endpoint:             endpoint,
		NotebookHash:         nbh,
		ProxyURL:             "demo://" + endpoint,
		ProxyToken:           "demo-proxy-token-" + shortHex,
		ProxyTokenExpiresAt:  &tokExp,
		Variant:              spec.Variant,
		RequestedAccelerator: requestedTier,
		Accelerator:          tier,
		FallbackApplied:      tier != requestedTier,
		Shape:                shape,
		Status:               models.SessionIdle,
		KernelID:             uuid.NewString(),
		JupyterSessionID:     uuid.NewString(),
		BurnRateCUHr:         spec.DefaultBurnRateCUHr,
		ColabWebURL:          models.BuildColabWebAttachURL(endpoint),
		ExperimentID:         req.ExperimentID,
		ShardIndex:           req.ShardIndex,
		StickyKey:            req.StickyKey,
		APIKeyID:             req.APIKeyID,
		KeepaliveHealthy:     true,
		LastKeepaliveAt:      &now,
		Resources:            res,
		Timeline: []models.ExecutionStep{
			{
				ID:          "step_assign",
				Label:       fmt.Sprintf("Assign %s VM (%s)", tier, acc.Email),
				Status:      "completed",
				DurationMs:  420,
				StartedAt:   now,
				CompletedAt: &now,
				Detail:      endpoint,
			},
		},
		Cells:     []models.NotebookCell{},
		CreatedAt: now,
		UpdatedAt: now,
	}

	_ = e.getOrCreateMemState(sess.ID, sess.ProxyToken)
	if err := e.persistNewSessionAndBind(ctx, acc, sess, req); err != nil {
		return nil, err
	}
	return sess, nil
}

func buildDemoTelemetry(
	tier models.AcceleratorType,
	spec models.HardwareSpec,
	vramRatio float64,
	smUtil float64,
	tempC int,
) *models.VMResourceTelemetry {
	const gb = int64(1024 * 1024 * 1024)
	ramTotal := 89 * gb
	ramUsed := 28 * gb
	diskTotal := 253 * gb
	diskUsed := 44 * gb

	var gpus []models.VMGPUTelemetry
	if spec.VRAMGB > 0 {
		vramTotal := int64(spec.VRAMGB) * gb
		vramUsed := int64(float64(vramTotal) * vramRatio)
		gpus = append(gpus, models.VMGPUTelemetry{
			Name:              spec.DisplayName,
			MemoryUsedBytes:   vramUsed,
			MemoryTotalBytes:  vramTotal,
			GPUUtilization:    smUtil,
			MemoryUtilization: vramRatio,
			TemperatureC:      tempC,
			PowerWatts:        285,
			EverUsed:          true,
		})
	}

	return &models.VMResourceTelemetry{
		MemoryTotalBytes: ramTotal,
		MemoryFreeBytes:  ramTotal - ramUsed,
		MemoryUsedBytes:  ramUsed,
		DiskTotalBytes:   diskTotal,
		DiskUsedBytes:    diskUsed,
		HFCacheBytes:     54 * gb,
		GPUs:             gpus,
		SampledAt:        time.Now().UTC(),
	}
}

var (
	rePrintCall  = regexp.MustCompile(`(?m)print\(([^\n]+)\)`)
	reSysExit    = regexp.MustCompile(`sys\.exit\(([^)]*)\)`)
	reRaiseError = regexp.MustCompile(`raise\s+([A-Za-z0-9_]+Error)\(([^)]*)\)`)
)

func (e *Engine) executeDemoCode(
	_ context.Context,
	acc *models.Account,
	sess *models.RuntimeSession,
	mem *sessionMemState,
	req models.KernelExecRequest,
	code string,
) (*models.KernelExecResult, error) {
	start := time.Now()
	mem.mu.Lock()
	mem.execCount++
	count := mem.execCount
	mem.mu.Unlock()

	spec, _ := models.GetHardwareSpec(sess.Accelerator)
	lowerCode := strings.ToLower(code)

	// 1. Contributor Drive/GCP Credential-Propagation Firewall check!
	if (strings.Contains(lowerCode, "drive.mount") || strings.Contains(lowerCode, "authenticate_user")) &&
		acc != nil && (acc.IsContributor || acc.Guardrails.BlockDrivePropagation) {
		msg := "Blocked by ColabHive Contributor Firewall: Drive/GCP credential propagation is disabled on donated accounts."
		e.publishEvent(models.WSEventGuardrailPreempt, map[string]any{
			"session_id": sess.ID,
			"account_id": acc.ID,
			"reason":     msg,
		})
		e.publishStreamChunk(sess, "stderr", msg+"\n", nil)
		return &models.KernelExecResult{
			Session:        sess.Name,
			ExecutionID:    uuid.NewString(),
			ExecutionCount: count,
			Status:         "error",
			Stderr:         msg + "\n",
			ExecutionError: &models.KernelExecError{
				Name:      "PermissionError",
				Value:     msg,
				Traceback: []string{"Traceback (most recent call last):", "  PermissionError: " + msg},
			},
			DurationMs: time.Since(start).Milliseconds() + 12,
		}, nil
	}

	// 2. CPython SystemExit handling (`colab run` exit-code propagation).
	if m := reSysExit.FindStringSubmatch(code); len(m) == 2 {
		exitCode := parseCPythonSystemExitCode(m[1])
		return &models.KernelExecResult{
			Session:        sess.Name,
			ExecutionID:    uuid.NewString(),
			ExecutionCount: count,
			Status:         "error",
			ExecutionError: &models.KernelExecError{
				Name:      "SystemExit",
				Value:     strings.TrimSpace(m[1]),
				Traceback: []string{},
				ExitCode:  &exitCode,
			},
			DurationMs: 8,
		}, nil
	}

	// 3. Explicit exception / division-by-zero simulation.
	if strings.Contains(code, "1/0") || strings.Contains(code, "1 / 0") {
		tb := "Traceback (most recent call last):\n  File \"<ipython-input>\", line 1, in <module>\nZeroDivisionError: division by zero"
		e.publishStreamChunk(sess, "stderr", tb+"\n", nil)
		return &models.KernelExecResult{
			Session:        sess.Name,
			ExecutionID:    uuid.NewString(),
			ExecutionCount: count,
			Status:         "error",
			Stderr:         tb + "\n",
			ExecutionError: &models.KernelExecError{
				Name:      "ZeroDivisionError",
				Value:     "division by zero",
				Traceback: strings.Split(tb, "\n"),
			},
			DurationMs: 9,
		}, nil
	}
	if m := reRaiseError.FindStringSubmatch(code); len(m) == 3 {
		errName := m[1]
		errVal := strings.Trim(strings.TrimSpace(m[2]), "'\"")
		tb := fmt.Sprintf("Traceback (most recent call last):\n  %s: %s", errName, errVal)
		e.publishStreamChunk(sess, "stderr", tb+"\n", nil)
		return &models.KernelExecResult{
			Session:        sess.Name,
			ExecutionID:    uuid.NewString(),
			ExecutionCount: count,
			Status:         "error",
			Stderr:         tb + "\n",
			ExecutionError: &models.KernelExecError{
				Name:      errName,
				Value:     errVal,
				Traceback: strings.Split(tb, "\n"),
			},
			DurationMs: 9,
		}, nil
	}

	var stdoutBuf strings.Builder
	var resultVal string
	var richOutputs []models.KernelRichOutput

	// 4. `!nvidia-smi` hardware inspection.
	if strings.Contains(lowerCode, "nvidia-smi") {
		gpuName := spec.DisplayName
		if spec.VRAMGB == 0 {
			stdoutBuf.WriteString("NVIDIA-SMI has failed because this session is allocated on a CPU VM (NONE).\n")
		} else {
			usedMiB := spec.VRAMGB * 1024 * 54 / 100
			totMiB := spec.VRAMGB * 1024
			table := fmt.Sprintf(
				"+-----------------------------------------------------------------------------------------+\n"+
					"| NVIDIA-SMI 570.124.04             Driver Version: 570.124.04     CUDA Version: 12.8     |\n"+
					"|-----------------------------------------+------------------------+----------------------+\n"+
					"| GPU  Name                 Persistence-M | Bus-Id          Disp.A | Volatile Uncorr. ECC |\n"+
					"|   0  %-34s |   00000000:00:04.0 Off |                    0 |\n"+
					"| 64C    P0              285W /  350W     |   %5dMiB / %6dMiB |     92%%      Default |\n"+
					"+-----------------------------------------+------------------------+----------------------+\n",
				truncateStr(gpuName, 34), usedMiB, totMiB,
			)
			stdoutBuf.WriteString(table)
		}
	}

	// 5. `torch.cuda` inspection.
	if strings.Contains(code, "torch.cuda.get_device_name") {
		if spec.VRAMGB > 0 {
			stdoutBuf.WriteString(spec.DisplayName + "\n")
		} else {
			stdoutBuf.WriteString("CPU\n")
		}
	} else if strings.Contains(code, "torch.cuda.is_available") && !strings.Contains(code, "get_device_name") {
		if spec.VRAMGB > 0 {
			resultVal = "True"
		} else {
			resultVal = "False"
		}
	}

	// 6. Matplotlib / Rich Plot generation -> real valid PNG image!
	if strings.Contains(lowerCode, "matplotlib") || strings.Contains(lowerCode, "plt.") || strings.Contains(lowerCode, "display_data") {
		pngBytes := GenerateDemoPlotPNG()
		fileName := fmt.Sprintf("plot_%03d_00.png", count)
		if artPath, err := e.ArtifactPath(sess.ID, fileName); err == nil {
			_ = os.MkdirAll(filepath.Dir(artPath), 0o755)
			_ = os.WriteFile(artPath, pngBytes, 0o644)
		}
		e.writeDemoFile(mem, "/content/.colabhive_outputs/"+fileName, pngBytes)
		ro := models.KernelRichOutput{
			Index:       0,
			MimeTypes:   []string{"image/png", "text/plain"},
			FilePath:    "/content/.colabhive_outputs/" + fileName,
			ArtifactURL: fmt.Sprintf("/api/v1/artifacts/%s/%s", sess.ID, fileName),
			Title:       "Training Loss & Eval Accuracy Curve",
			SizeBytes:   int64(len(pngBytes)),
		}
		if req.InlineImages {
			ro.InlineBase64 = base64.StdEncoding.EncodeToString(pngBytes)
		}
		richOutputs = append(richOutputs, ro)
		e.publishStreamChunk(sess, "colab", fmt.Sprintf("[artifact] %s\n", ro.ArtifactURL), &ro)
	}

	// 7. Print statements & shard launcher output.
	if stdoutBuf.Len() == 0 {
		matches := rePrintCall.FindAllStringSubmatch(code, -1)
		for _, m := range matches {
			arg := strings.TrimSpace(m[1])
			arg = strings.TrimPrefix(arg, "f")
			arg = strings.Trim(arg, "'\"")
			if strings.Contains(arg, "{__p.pid}") {
				arg = strings.ReplaceAll(arg, "{__p.pid}", "4812")
			}
			stdoutBuf.WriteString(arg + "\n")
		}
	}

	// Fallback expression summary if silent==false and nothing printed yet.
	if stdoutBuf.Len() == 0 && resultVal == "" && len(richOutputs) == 0 && !req.Silent {
		trimmed := strings.TrimSpace(code)
		lines := strings.Split(trimmed, "\n")
		lastLine := strings.TrimSpace(lines[len(lines)-1])
		if !strings.Contains(lastLine, "=") && !strings.HasPrefix(lastLine, "import ") && !strings.HasPrefix(lastLine, "#") {
			resultVal = lastLine
		} else {
			stdoutBuf.WriteString(fmt.Sprintf("[ok] Executed %d line(s) on %s (%s)\n", len(lines), sess.Accelerator, sess.Name))
		}
	}

	outStr := stdoutBuf.String()
	if !req.Silent && outStr != "" {
		e.publishStreamChunk(sess, "stdout", outStr, nil)
	}

	return &models.KernelExecResult{
		Session:        sess.Name,
		ExecutionID:    uuid.NewString(),
		ExecutionCount: count,
		Status:         "ok",
		Stdout:         outStr,
		Result:         resultVal,
		RichOutputs:    richOutputs,
		DurationMs:     time.Since(start).Milliseconds() + 14,
	}, nil
}

// GenerateDemoPlotPNG renders a crisp 480x260 Obsidian-themed training curve PNG using Go's stdlib image/png.
func GenerateDemoPlotPNG() []byte {
	const w, h = 480, 260
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	bg := color.RGBA{R: 13, G: 17, B: 23, A: 255}
	grid := color.RGBA{R: 32, G: 41, B: 54, A: 255}
	amber := color.RGBA{R: 245, G: 158, B: 11, A: 255}
	emerald := color.RGBA{R: 16, G: 185, B: 129, A: 255}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := bg
			if x%48 == 0 || y%40 == 0 || x == 36 || y == h-28 {
				c = grid
			}
			img.SetRGBA(x, y, c)
		}
	}

	// Exponential loss decay curve (Amber) & rising eval accuracy curve (Emerald).
	for x := 40; x < w-20; x++ {
		t := float64(x-40) / float64(w-60)
		lossY := int(float64(h-36) - (1.0-math.Exp(-3.4*t))*0.0 + math.Exp(-3.2*t)*float64(h-72))
		lossY = h - 32 - int((1.0-math.Exp(-3.2*t))*float64(h-75))
		// Invert so loss starts top-left and decays toward bottom-right:
		decayY := 36 + int((1.0-math.Exp(-3.1*t))*float64(h-76))
		accY := h - 36 - int((1.0-math.Exp(-2.6*t))*float64(h-84))
		_ = lossY
		for dy := -1; dy <= 1; dy++ {
			if decayY+dy >= 0 && decayY+dy < h {
				img.SetRGBA(x, decayY+dy, amber)
			}
			if accY+dy >= 0 && accY+dy < h {
				img.SetRGBA(x, accY+dy, emerald)
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func (e *Engine) listDemoFiles(mem *sessionMemState, remotePath string) []models.RemoteFileEntry {
	dir := "/" + strings.TrimPrefix(strings.TrimRight(strings.ReplaceAll(remotePath, "\\", "/"), "/"), "/")
	if dir == "/" || dir == "" {
		dir = "/content"
	}

	mem.mu.Lock()
	defer mem.mu.Unlock()

	now := time.Now().UTC()
	seenDirs := make(map[string]bool)
	var out []models.RemoteFileEntry

	prefix := dir + "/"
	for fullPath, data := range mem.virtualFiles {
		if !strings.HasPrefix(fullPath, prefix) {
			continue
		}
		rest := strings.TrimPrefix(fullPath, prefix)
		if rest == "" {
			continue
		}
		if slashIdx := strings.IndexByte(rest, '/'); slashIdx >= 0 {
			subDir := rest[:slashIdx]
			if !seenDirs[subDir] {
				seenDirs[subDir] = true
				out = append(out, models.RemoteFileEntry{
					Name:         subDir,
					Path:         path.Join(dir, subDir),
					Type:         "directory",
					LastModified: &now,
				})
			}
		} else {
			out = append(out, models.RemoteFileEntry{
				Name:         rest,
				Path:         fullPath,
				Type:         "file",
				SizeBytes:    int64(len(data)),
				LastModified: &now,
				Synced:       strings.HasSuffix(rest, ".jsonl"),
			})
		}
	}
	sortRemoteEntries(out)
	return out
}

func (e *Engine) writeDemoFile(mem *sessionMemState, remotePath string, data []byte) {
	p := "/" + strings.TrimPrefix(strings.ReplaceAll(remotePath, "\\", "/"), "/")
	cp := make([]byte, len(data))
	copy(cp, data)
	mem.mu.Lock()
	mem.virtualFiles[p] = cp
	mem.mu.Unlock()
}

func (e *Engine) readDemoFile(mem *sessionMemState, remotePath string) ([]byte, error) {
	p := "/" + strings.TrimPrefix(strings.ReplaceAll(remotePath, "\\", "/"), "/")
	mem.mu.Lock()
	data, ok := mem.virtualFiles[p]
	mem.mu.Unlock()
	if !ok {
		return nil, &httpStatusError{StatusCode: 404, Body: fmt.Sprintf("remote file not found: %s", p)}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (e *Engine) deleteDemoFile(mem *sessionMemState, remotePath string) {
	p := "/" + strings.TrimPrefix(strings.ReplaceAll(remotePath, "\\", "/"), "/")
	mem.mu.Lock()
	delete(mem.virtualFiles, p)
	mem.mu.Unlock()
}

func (e *Engine) seedDemoShardRecords(mem *sessionMemState, shardID int, count int) {
	var buf strings.Builder
	for i := 0; i < count; i++ {
		rec, _ := json.Marshal(map[string]any{
			"shard_id": shardID,
			"item_id":  shardID*count + i,
			"score":    math.Round((0.82+float64(i%7)*0.02)*10000) / 10000,
			"ok":       true,
		})
		buf.Write(rec)
		buf.WriteByte('\n')
	}
	e.writeDemoFile(mem, fmt.Sprintf("/content/shard_%d.jsonl", shardID), []byte(buf.String()))
}

func (e *Engine) jitterDemoTelemetry(sess *models.RuntimeSession) {
	if sess.Resources == nil || len(sess.Resources.GPUs) == 0 {
		return
	}
	tick := float64(time.Now().Unix()%11 - 5) // -5 .. +5
	g := &sess.Resources.GPUs[0]
	util := 0.91 + tick*0.01
	if util > 0.99 {
		util = 0.99
	}
	if util < 0.78 {
		util = 0.78
	}
	g.GPUUtilization = math.Round(util*100) / 100
	g.TemperatureC = 64 + int(tick/2)
	sess.Resources.SampledAt = time.Now().UTC()
}

func (e *Engine) advanceDemoExperimentsBestEffort(ctx context.Context) {
	if e.st == nil {
		return
	}
	exps, err := e.st.ListExperiments(ctx)
	if err != nil {
		return
	}
	for _, exp := range exps {
		if exp == nil || exp.Status != models.ExperimentRunning {
			continue
		}
		changed := false
		for i := range exp.Shards {
			sh := &exp.Shards[i]
			if sh.Status != "RUNNING" {
				continue
			}
			target := sh.TargetRecords
			if target <= 0 {
				target = 125
			}
			if sh.SyncedRecords < target {
				sh.SyncedRecords += 2
				if sh.SyncedRecords > target {
					sh.SyncedRecords = target
				}
				sh.ProgressPct = math.Round((float64(sh.SyncedRecords)/float64(target))*1000) / 10
				sh.LogTail = fmt.Sprintf("[%d/%d] acc=0.8642 | loss=0.381 | 2.94s/it", sh.SyncedRecords, target)
				changed = true
				if sh.SessionID != "" {
					if sess, sErr := e.st.GetSession(ctx, sh.SessionID); sErr == nil && sess != nil {
						e.publishStreamChunk(sess, "stdout", sh.LogTail+"\n", nil)
					}
				}
			}
		}
		if changed {
			recomputeExperimentRollup(exp)
			_ = e.st.SaveExperiment(ctx, exp)
			e.publishEvent(models.WSEventExperimentUpdated, exp)
		}
	}
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
