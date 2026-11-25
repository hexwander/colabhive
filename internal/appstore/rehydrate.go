package appstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

type persistedAppInstance struct {
	ID                 string                 `json:"id"`
	TemplateID         string                 `json:"template_id"`
	Name               string                 `json:"name,omitempty"`
	NameZh             string                 `json:"name_zh,omitempty"`
	SessionID          string                 `json:"session_id"`
	RemotePort         int                    `json:"remote_port,omitempty"`
	PreferredLocalPort int                    `json:"preferred_local_port,omitempty"`
	IdleAutoStopMin    int                    `json:"idle_auto_stop_min,omitempty"`
	AutoResumeEnabled  bool                   `json:"auto_resume_enabled"`
	SelectedModelID    string                 `json:"selected_model_id,omitempty"`
	SelectedModelName  string                 `json:"selected_model_name,omitempty"`
	ModelDownloadURL   string                 `json:"model_download_url,omitempty"`
	CheckpointDir      string                 `json:"checkpoint_dir,omitempty"`
	CustomRepoURL      string                 `json:"custom_repo_url,omitempty"`
	CustomCommand      string                 `json:"custom_command,omitempty"`
	AccountSelector    string                 `json:"account_selector,omitempty"`
	PreferredGPU       models.AcceleratorType `json:"preferred_gpu,omitempty"`
	ExplicitGPU        bool                   `json:"explicit_gpu,omitempty"`
	LaunchMode         string                 `json:"launch_mode,omitempty"`
	Deploying          bool                   `json:"deploying,omitempty"`
	StartedAt          time.Time              `json:"started_at"`
}

func (m *Manager) stateFilePath() string {
	dir := strings.TrimSpace(m.stateDataDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "appstore_state.json")
}

func (m *Manager) persistInstancesLocked() {
	path := m.stateFilePath()
	if path == "" {
		return
	}
	records := make([]persistedAppInstance, 0, len(m.instances))
	for _, mi := range m.instances {
		if mi == nil || mi.inst == nil || strings.TrimSpace(mi.inst.SessionID) == "" {
			continue
		}
		switch mi.inst.State {
		case models.AppStateRunning, models.AppStateStarting, models.AppStateFailover:
		default:
			continue
		}
		prefLocal := mi.inst.LocalPort
		if prefLocal <= 0 {
			prefLocal = mi.tpl.DefaultLocalPort
		}
		records = append(records, persistedAppInstance{
			ID:                 mi.inst.ID,
			TemplateID:         mi.inst.TemplateID,
			Name:               mi.inst.Name,
			NameZh:             mi.inst.NameZh,
			SessionID:          mi.inst.SessionID,
			RemotePort:         mi.inst.RemotePort,
			PreferredLocalPort: prefLocal,
			IdleAutoStopMin:    mi.inst.IdleAutoStopMin,
			AutoResumeEnabled:  mi.inst.AutoResumeEnabled,
			SelectedModelID:    mi.inst.SelectedModelID,
			SelectedModelName:  mi.inst.SelectedModelName,
			ModelDownloadURL:   mi.inst.ModelDownloadURL,
			CheckpointDir:      mi.inst.CheckpointDir,
			CustomRepoURL:      mi.inst.CustomRepoURL,
			CustomCommand:      mi.inst.CustomCommand,
			AccountSelector:    mi.accountSelector,
			PreferredGPU:       mi.preferredGPU,
			ExplicitGPU:        mi.explicitGPU,
			LaunchMode:         mi.inst.LaunchMode,
			Deploying:          mi.inst.State == models.AppStateStarting || mi.inst.State == models.AppStateFailover,
			StartedAt:          mi.inst.StartedAt,
		})
	}
	if len(records) == 0 {
		_ = os.Remove(path)
		return
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].StartedAt.Before(records[j].StartedAt)
	})
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func (m *Manager) loadPersistedInstances() []persistedAppInstance {
	path := m.stateFilePath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var records []persistedAppInstance
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil
	}
	return records
}

// extractTemplateIDFromSessionName parses `app-<templateID>-<6hex>` (including hyphenated template IDs
// like `app-unsloth-studio-ab12cd` and `app-wangp-6e5fa3`).
func extractTemplateIDFromSessionName(sessName string) (tplID string, suffix string, ok bool) {
	lower := strings.ToLower(strings.TrimSpace(sessName))
	if !strings.HasPrefix(lower, "app-") {
		return "", "", false
	}
	rest := strings.TrimPrefix(lower, "app-")
	lastDash := strings.LastIndexByte(rest, '-')
	if lastDash <= 0 || lastDash >= len(rest)-1 {
		return "", "", false
	}
	tplID = strings.TrimSpace(rest[:lastDash])
	suffix = strings.TrimSpace(rest[lastDash+1:])
	if tplID == "" || suffix == "" {
		return "", "", false
	}
	return tplID, suffix, true
}

// rehydrateActiveInstances automatically re-attaches to still-running cloud `app-<tpl>-*` GPU sessions
// across desktop/server restarts, restoring their private localhost port bridges and resuming any
// in-progress background installations seamlessly.
func (m *Manager) rehydrateActiveInstances() {
	if m == nil || m.rt == nil {
		return
	}
	defer func() { _ = recover() }()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	sessions, err := m.rt.ListSessions(ctx, models.SessionFilter{})
	if err != nil {
		return
	}

	liveByID := make(map[string]*models.RuntimeSession, len(sessions))
	var liveAppSessions []*models.RuntimeSession
	for _, sess := range sessions {
		if sess == nil || strings.TrimSpace(sess.ID) == "" || !isSessionHealthyActive(sess.Status) {
			continue
		}
		liveByID[sess.ID] = sess
		if _, _, ok := extractTemplateIDFromSessionName(sess.Name); ok {
			liveAppSessions = append(liveAppSessions, sess)
		}
	}

	matchedSessionIDs := make(map[string]struct{})
	persisted := m.loadPersistedInstances()
	dirtyPersisted := false

	type rehydrateTarget struct {
		instID     string
		tpl        models.AppTemplate
		healthPath string
		sess       *models.RuntimeSession
		rec        *persistedAppInstance
		wasDeploy  bool
		prefLocal  int
		remotePort int
	}
	var targets []rehydrateTarget

	for i := range persisted {
		rec := persisted[i]
		sess, ok := liveByID[rec.SessionID]
		if !ok {
			dirtyPersisted = true
			continue
		}
		tpl, pluginMan, found := m.resolveTemplateAndPlugin(rec.TemplateID)
		if !found {
			dirtyPersisted = true
			continue
		}
		hp := "/"
		if pluginMan != nil && pluginMan.HealthPath != "" {
			hp = pluginMan.HealthPath
		}
		matchedSessionIDs[sess.ID] = struct{}{}
		remPort := rec.RemotePort
		if remPort <= 0 {
			remPort = tpl.DefaultRemotePort
		}
		prefLocal := rec.PreferredLocalPort
		if prefLocal <= 0 {
			prefLocal = tpl.DefaultLocalPort
		}
		recCopy := rec
		targets = append(targets, rehydrateTarget{
			instID:     firstStr(rec.ID, models.NewPrefixedID("app_")),
			tpl:        tpl,
			healthPath: hp,
			sess:       sess,
			rec:        &recCopy,
			wasDeploy:  rec.Deploying,
			prefLocal:  prefLocal,
			remotePort: remPort,
		})
	}

	// Also adopt any live `app-<tpl>-<hex>` sessions that predate appstore_state.json!
	sort.Slice(liveAppSessions, func(i, j int) bool {
		return liveAppSessions[i].CreatedAt.After(liveAppSessions[j].CreatedAt)
	})
	for _, sess := range liveAppSessions {
		if _, already := matchedSessionIDs[sess.ID]; already {
			continue
		}
		tplID, suffix, ok := extractTemplateIDFromSessionName(sess.Name)
		if !ok || tplID == "llm-chat" {
			continue
		}
		tpl, pluginMan, found := m.resolveTemplateAndPlugin(tplID)
		if !found {
			continue
		}
		hp := "/"
		if pluginMan != nil && pluginMan.HealthPath != "" {
			hp = pluginMan.HealthPath
		}
		matchedSessionIDs[sess.ID] = struct{}{}
		targets = append(targets, rehydrateTarget{
			instID:     "app_" + suffix + "_" + strings.TrimPrefix(sess.ID, "sess_")[:minInt(6, len(strings.TrimPrefix(sess.ID, "sess_")))],
			tpl:        tpl,
			healthPath: hp,
			sess:       sess,
			rec:        nil,
			wasDeploy:  false,
			prefLocal:  tpl.DefaultLocalPort,
			remotePort: tpl.DefaultRemotePort,
		})
	}

	if len(targets) == 0 {
		if dirtyPersisted {
			m.mu.Lock()
			m.persistInstancesLocked()
			m.mu.Unlock()
		}
		return
	}

	isolatedOrigin := strings.TrimSpace(m.cfg.IsolatedProxyOrigin)
	now := time.Now().UTC()

	for _, tg := range targets {
		_, provider, freeActive, accLabel := m.resolveSessionAccountMeta(ctx, tg.sess)

		webProxyURL := fmt.Sprintf("/apps/proxy/%s/", tg.instID)
		if isolatedOrigin != "" {
			webProxyURL = strings.TrimRight(isolatedOrigin, "/") + webProxyURL
		}

		name := tg.tpl.Name
		nameZh := tg.tpl.NameZh
		idleMin := m.cfg.DefaultIdleAutoStopMin
		autoResume := true
		modelCfg := resolvedModelConfig{}
		ckptDir := appCheckpointRemoteDir(tg.tpl.ID)
		cleanRepo := ""
		cleanCmd := ""
		accSelector := ""
		prefGPU := tg.sess.Accelerator
		explicitGPU := false
		startedAt := tg.sess.CreatedAt.UTC()
		if startedAt.IsZero() {
			startedAt = now
		}
		if tg.rec != nil {
			name = firstStr(tg.rec.Name, name)
			nameZh = firstStr(tg.rec.NameZh, nameZh)
			if tg.rec.IdleAutoStopMin >= minAppIdleAutoStopMin {
				idleMin = tg.rec.IdleAutoStopMin
			}
			autoResume = tg.rec.AutoResumeEnabled
			modelCfg.id = tg.rec.SelectedModelID
			modelCfg.name = tg.rec.SelectedModelName
			modelCfg.downloadURL = tg.rec.ModelDownloadURL
			ckptDir = firstStr(tg.rec.CheckpointDir, ckptDir)
			cleanRepo = tg.rec.CustomRepoURL
			cleanCmd = tg.rec.CustomCommand
			accSelector = tg.rec.AccountSelector
			if tg.rec.PreferredGPU != "" {
				prefGPU = tg.rec.PreferredGPU
			}
			explicitGPU = tg.rec.ExplicitGPU
			if !tg.rec.StartedAt.IsZero() {
				startedAt = tg.rec.StartedAt.UTC()
			}
		} else if len(tg.tpl.ModelOptions) > 0 {
			modelCfg.id = tg.tpl.ModelOptions[0].ID
			modelCfg.name = tg.tpl.ModelOptions[0].NameZh
		}

		launchMode := ""
		if tg.rec != nil {
			launchMode = strings.TrimSpace(tg.rec.LaunchMode)
		}
		if launchMode == "" {
			if tg.tpl.MediaAPIBase != "" {
				launchMode = "headless"
			} else {
				launchMode = "native_webui"
			}
		}
		mediaAPIBase := tg.tpl.MediaAPIBase
		if launchMode == "native_webui" && tg.tpl.ID == "wangp" {
			mediaAPIBase = ""
		}
		studioRoute := ""
		if tg.tpl.UI != nil && tg.tpl.UI.Mode == "builtin_route" && tg.tpl.UI.Route != "" {
			studioRoute = tg.tpl.UI.Route
		}

		inst := &models.AppInstance{
			ID:                tg.instID,
			TemplateID:        tg.tpl.ID,
			Name:              name,
			NameZh:            nameZh,
			Category:          tg.tpl.Category,
			State:             models.AppStateRunning,
			ProgressPct:       100,
			SessionID:         tg.sess.ID,
			AccountID:         tg.sess.AccountID,
			AccountLabel:      accLabel,
			Provider:          provider,
			AssignedGPU:       tg.sess.Accelerator,
			FreeTierActive:    freeActive,
			RemotePort:        tg.remotePort,
			WebProxyURL:       webProxyURL,
			IdleAutoStopMin:   idleMin,
			AutoResumeEnabled: autoResume,
			SelectedModelID:   modelCfg.id,
			SelectedModelName: modelCfg.name,
			ModelDownloadURL:  modelCfg.downloadURL,
			CheckpointDir:     ckptDir,
			CustomRepoURL:     cleanRepo,
			CustomCommand:     cleanCmd,
			Provides:          append([]string(nil), tg.tpl.Provides...),
			MediaAPIBase:      mediaAPIBase,
			StudioRoute:       studioRoute,
			LaunchMode:        launchMode,
			StartedAt:         startedAt,
			LastActiveAt:      now,
		}
		mi := &managedInstance{
			inst:               inst,
			tpl:                tg.tpl,
			cleanRepo:          cleanRepo,
			cleanCmd:           cleanCmd,
			checkpointDir:      ckptDir,
			healthPath:         firstStr(tg.healthPath, "/"),
			accountSelector:    accSelector,
			preferredGPU:       prefGPU,
			explicitGPU:        explicitGPU,
			model:              modelCfg,
			lastCheckpointSync: now,
		}

		// Register instance first so bridge handler can find it immediately.
		m.mu.Lock()
		m.instances[tg.instID] = mi
		m.mu.Unlock()

		if tg.wasDeploy && !m.cfg.DemoMode {
			m.mu.Lock()
			inst.State = models.AppStateStarting
			inst.ProgressPct = 50
			inst.StatusStep = fmt.Sprintf("Resuming in-progress setup of %s on %s...", name, tg.sess.Accelerator)
			inst.StatusStepZh = fmt.Sprintf("重启后已接管进行中的「%s」部署，正在同步实时进度...", nameZh)
			appendInstanceDeployLogLocked(inst, inst.StatusStepZh)
			m.persistInstancesLocked()
			m.mu.Unlock()
		} else {
			var bridge *localBridge
			decoratedURL := ""
			if !m.cfg.CloudMode && tg.tpl.SupportsPortBridge {
				if b, bErr := startLocalPortBridge(tg.instID, tg.prefLocal, m.newBridgeHTTPHandler(tg.instID)); bErr == nil {
					bridge = b
					decoratedURL = m.decorateLocalBridgeURL(ctx, tg.tpl.ID, tg.sess.ID, b.url)
				}
			}
			m.mu.Lock()
			mi.bridge = bridge
			if bridge != nil {
				inst.LocalPort = bridge.port
				inst.LocalBridgeActive = true
				inst.LocalURL = decoratedURL
				inst.StatusStep = "Running · Reattached private localhost bridge ready"
				inst.StatusStepZh = fmt.Sprintf("运行中 · 本机私有直连 %s 已就绪（重启自动接管）", bridge.url)
			} else {
				inst.StatusStep = "Running · Reattached to active cloud GPU session"
				inst.StatusStepZh = "运行中 · 已自动接管云端运行中的应用实例"
			}
			appendInstanceDeployLogLocked(inst, inst.StatusStepZh)
			m.persistInstancesLocked()
			m.mu.Unlock()
		}

		if !m.cfg.DemoMode {
			sessCopy := *tg.sess
			m.wg.Add(1)
			go m.reconcileRehydratedAppBootstrap(tg.instID, &sessCopy, tg.tpl, tg.prefLocal, tg.wasDeploy)
		}
	}
}

func (m *Manager) reconcileRehydratedAppBootstrap(
	instanceID string,
	sess *models.RuntimeSession,
	tpl models.AppTemplate,
	prefLocalPort int,
	wasDeploying bool,
) {
	defer m.wg.Done()
	defer func() { _ = recover() }()

	if m.rt == nil || sess == nil || strings.TrimSpace(sess.ID) == "" {
		return
	}
	safeID := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, strings.TrimSpace(tpl.ID))
	if safeID == "" {
		safeID = "app"
	}
	statusPath := "/tmp/ch_app_boot_" + safeID + ".json"

	probeScript := fmt.Sprintf(`import json, os, sys
p = %s
if not os.path.exists(p):
    sys.stdout.write("COLABHIVE_APP_REHYDRATE:{\"state\":\"absent\"}\n")
else:
    with open(p, "r", encoding="utf-8", errors="replace") as fh:
        raw = fh.read().strip()
    data = json.loads(raw) if raw else {"state": "absent"}
    pid = int(data.get("pid") or 0)
    if data.get("state") == "running" and pid > 0:
        try:
            os.kill(pid, 0)
        except OSError:
            with open(p, "r", encoding="utf-8", errors="replace") as fh2:
                raw2 = fh2.read().strip()
            data = json.loads(raw2) if raw2 else data
            if data.get("state") == "running":
                data = {"state": "error", "error": "background app bootstrap process exited unexpectedly"}
    sys.stdout.write("COLABHIVE_APP_REHYDRATE:" + json.dumps(data, ensure_ascii=False) + "\n")
`, strconvQuote(statusPath))

	probeCtx, probeCancel := context.WithTimeout(m.stopCtx, 12*time.Second)
	res, err := m.rt.ExecuteCode(probeCtx, models.KernelExecRequest{
		SessionID:               sess.ID,
		Code:                    probeScript,
		Silent:                  false,
		StoreHistory:            false,
		IdleTimeoutSec:          15,
		CallerName:              "appstore-rehydrate-probe",
		SuppressOutputBroadcast: true,
	})
	probeCancel()

	if err != nil || res == nil || !strings.Contains(res.Stdout, "COLABHIVE_APP_REHYDRATE:") {
		return
	}
	idx := strings.Index(res.Stdout, "COLABHIVE_APP_REHYDRATE:")
	line := res.Stdout[idx+len("COLABHIVE_APP_REHYDRATE:"):]
	if nl := strings.IndexAny(line, "\r\n"); nl >= 0 {
		line = line[:nl]
	}
	var st struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &st) != nil {
		return
	}

	finishReady := func() {
		m.mu.RLock()
		mi := m.instances[instanceID]
		m.mu.RUnlock()
		if mi == nil || mi.inst == nil {
			return
		}
		var bridge *localBridge
		if !m.cfg.CloudMode && tpl.SupportsPortBridge {
			m.mu.RLock()
			hasBridge := mi.bridge != nil
			m.mu.RUnlock()
			if !hasBridge {
				if b, bErr := startLocalPortBridge(instanceID, prefLocalPort, m.newBridgeHTTPHandler(instanceID)); bErr == nil {
					bridge = b
				}
			}
		}
		m.mu.Lock()
		if m.closed || mi.inst.SessionID != sess.ID {
			m.mu.Unlock()
			if bridge != nil {
				_ = bridge.Close()
			}
			return
		}
		if bridge != nil {
			mi.bridge = bridge
			mi.inst.LocalPort = bridge.port
			mi.inst.LocalBridgeActive = true
			mi.inst.LocalURL = bridge.url
		}
		mi.inst.State = models.AppStateRunning
		mi.inst.ProgressPct = 100
		if mi.inst.LocalURL != "" {
			mi.inst.StatusStep = "Running · Private localhost bridge ready"
			mi.inst.StatusStepZh = fmt.Sprintf("运行中 · 本机私有直连 %s 已就绪", mi.inst.LocalURL)
		} else {
			mi.inst.StatusStep = "Running · Cloud studio ready"
			mi.inst.StatusStepZh = "运行中 · 云端应用已就绪"
		}
		appendInstanceDeployLogLocked(mi.inst, "部署完成 · "+mi.inst.StatusStepZh)
		m.persistInstancesLocked()
		snap := cloneAppInstance(mi.inst)
		m.mu.Unlock()
		m.publishInstanceEvent(snap)
	}

	handleFail := func(reason string) {
		cleanMsg := sanitizeAppStoreError(errors.New(firstStr(reason, "remote app bootstrap failed")))
		stopErr := m.stopSessionBounded(m.stopCtx, sess.ID)
		m.mu.Lock()
		mi := m.instances[instanceID]
		if mi == nil || mi.inst == nil || mi.inst.SessionID != sess.ID {
			m.mu.Unlock()
			return
		}
		oldBridge := mi.bridge
		mi.bridge = nil
		mi.inst.LocalBridgeActive = false
		mi.inst.LocalURL = ""
		mi.inst.State = models.AppStateError
		mi.inst.ErrorMessage = cleanMsg
		appendInstanceDeployLogLocked(mi.inst, "部署失败："+cleanMsg)
		if stopErr == nil {
			mi.inst.SessionID = ""
			mi.inst.StatusStep = "Bootstrap failed · cloud GPU automatically released (0 burn)"
			mi.inst.StatusStepZh = "启动失败 · 已自动释放云端显卡（当前 0 消耗）"
			appendInstanceDeployLogLocked(mi.inst, "已自动释放云端显卡会话，当前 0 额度消耗")
		}
		m.persistInstancesLocked()
		snap := cloneAppInstance(mi.inst)
		m.mu.Unlock()
		if oldBridge != nil {
			_ = oldBridge.Close()
		}
		m.publishInstanceEvent(snap)
	}

	switch st.State {
	case "ready":
		if wasDeploying {
			finishReady()
		}
	case "error":
		handleFail(st.Error)
	case "running":
		opCtx, opCancel := context.WithCancel(m.stopCtx)
		m.mu.Lock()
		mi := m.instances[instanceID]
		if mi == nil || mi.inst == nil || mi.inst.SessionID != sess.ID {
			m.mu.Unlock()
			opCancel()
			return
		}
		mi.inst.State = models.AppStateStarting
		if mi.inst.ProgressPct >= 100 || mi.inst.ProgressPct < 45 {
			mi.inst.ProgressPct = 50
		}
		mi.launchCancel = opCancel
		m.persistInstancesLocked()
		snap := cloneAppInstance(mi.inst)
		m.mu.Unlock()
		m.publishInstanceEvent(snap)

		defer func() {
			opCancel()
			m.mu.Lock()
			if cur := m.instances[instanceID]; cur != nil {
				cur.launchCancel = nil
			}
			m.mu.Unlock()
		}()

		if pErr := m.pollDetachedAppBootstrapWithInstance(opCtx, instanceID, sess.ID, tpl.ID, statusPath, 15*time.Minute); pErr != nil {
			if errors.Is(pErr, context.Canceled) {
				return
			}
			handleFail(pErr.Error())
			return
		}
		finishReady()
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
