package appstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/checkpoint"
	"github.com/colabhive/colabhive/internal/commtunnel"
	"github.com/colabhive/colabhive/internal/models"
)

var (
	// ErrAppInstanceNotFound is returned when an AppInstance ID does not exist.
	ErrAppInstanceNotFound = errors.New("app instance not found")

	// ErrIsolatedOriginRequired is returned (HTTP 422) when same-origin interactive HTML proxying
	// or a live cloud-mode Studio launch is requested without an isolated proxy origin.
	ErrIsolatedOriginRequired = errors.New("same-origin interactive HTML proxy is disabled: Studio UI requires an isolated origin separate from the control plane so untrusted app HTML never shares the admin bearer origin")

	// ErrPreStopCheckpointSyncFailed is returned (HTTP 409) when AutoResumeEnabled is true,
	// force is false, and the final pre-stop checkpoint backup fails.
	ErrPreStopCheckpointSyncFailed = errors.New("pre-stop checkpoint sync failed")
)

const (
	defaultAppIdleAutoStopMin = 45
	minAppIdleAutoStopMin     = 5
	maxAppIdleAutoStopMin     = 720
	sessionCleanupTimeout     = 15 * time.Second
)

// Config configures the 1-Click AI App Store Manager.
type Config struct {
	DataDir                string
	DisableUserHomePlugins bool
	CloudMode              bool
	DemoMode               bool
	ProtectedMode          bool
	// IsolatedProxyOrigin specifies an optional dedicated origin (distinct from the control plane)
	// for serving Studio UIs in live cloud deployments. When empty in live CloudMode, Launch fails closed (422).
	IsolatedProxyOrigin    string
	DefaultIdleAutoStopMin int
	WatchdogInterval       time.Duration
	// IdleTimeoutOverride allows tests to verify sub-second idle auto-shutdown without waiting minutes.
	IdleTimeoutOverride time.Duration
	// CheckpointSyncIntervalOverride allows tests to verify sub-second periodic checkpoint sync.
	CheckpointSyncIntervalOverride time.Duration
}

// AppInstanceConfigPatch updates mutable runtime settings on an AppInstance (`PATCH /api/v1/apps/instances/{id}`).
type AppInstanceConfigPatch struct {
	IdleAutoStopMin   *int  `json:"idle_auto_stop_min,omitempty"`
	AutoResumeEnabled *bool `json:"auto_resume_enabled,omitempty"`
}

type resolvedModelConfig struct {
	id             string
	name           string
	downloadURL    string
	targetSubdir   string
	filename       string
	recommendedGPU models.AcceleratorType
}

type managedInstance struct {
	opMu                  sync.Mutex
	inst                  *models.AppInstance
	tpl                   models.AppTemplate
	bridge                *localBridge
	cleanRepo             string
	cleanCmd              string
	checkpointDir         string
	healthPath            string
	accountSelector       string
	preferredGPU          models.AcceleratorType
	explicitGPU           bool
	model                 resolvedModelConfig
	lastCheckpointSync    time.Time
	lastCheckpointAttempt time.Time
	checkpointFailCount   int
	launchCancel          context.CancelFunc
}

// Manager orchestrates 1-Click AI App instances, declarative community plugins,
// private localhost port bridges, idle auto-shutdown timers, periodic checkpointing,
// and cross-account checkpoint failover.
type Manager struct {
	mu              sync.RWMutex
	closed          bool
	cfg             Config
	stateDataDir    string
	pluginsDir      string
	extraPluginDirs []string
	pluginWriteMu   sync.Mutex
	plugins         map[string]*loadedPluginEntry
	pool            models.AccountPoolService
	rt              models.RuntimeEngine
	ckpt            *checkpoint.Manager
	hub             models.EventHub
	instances       map[string]*managedInstance
	tunnelsMu       sync.Mutex
	tunnels         map[string]*commtunnel.Tunnel
	stopCtx         context.Context
	stopCancel      context.CancelFunc
	wg              sync.WaitGroup
}

// NewManager creates and starts a new App Store Manager.
func NewManager(
	pool models.AccountPoolService,
	rt models.RuntimeEngine,
	ckpt *checkpoint.Manager,
	hub models.EventHub,
	cfg Config,
) *Manager {
	if cfg.DefaultIdleAutoStopMin <= 0 {
		cfg.DefaultIdleAutoStopMin = defaultAppIdleAutoStopMin
	}
	if cfg.WatchdogInterval <= 0 {
		cfg.WatchdogInterval = 15 * time.Second
	}
	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" && ckpt != nil {
		dataDir = strings.TrimSpace(ckpt.DataDir())
	}
	primaryPluginsDir, extraPluginsDirs := resolvePluginsDirs(dataDir, cfg.DisableUserHomePlugins)

	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cfg:             cfg,
		stateDataDir:    dataDir,
		pluginsDir:      primaryPluginsDir,
		extraPluginDirs: extraPluginsDirs,
		plugins:         make(map[string]*loadedPluginEntry),
		pool:            pool,
		rt:              rt,
		ckpt:            ckpt,
		hub:             hub,
		instances:       make(map[string]*managedInstance),
		tunnels:         make(map[string]*commtunnel.Tunnel),
		stopCtx:         ctx,
		stopCancel:      cancel,
	}
	m.ReloadPlugins()
	m.rehydrateActiveInstances()
	m.wg.Add(1)
	go m.watchdogLoop()
	return m
}

// Close shuts down the background watchdog and closes all localhost port bridge listeners cleanly.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	m.stopCancel()
	m.wg.Wait()

	m.mu.RLock()
	items := make([]*managedInstance, 0, len(m.instances))
	for _, mi := range m.instances {
		if mi != nil {
			items = append(items, mi)
		}
	}
	m.mu.RUnlock()

	for _, mi := range items {
		mi.opMu.Lock()
		m.mu.Lock()
		b := mi.bridge
		mi.bridge = nil
		if mi.inst != nil {
			mi.inst.LocalBridgeActive = false
			mi.inst.LocalPort = 0
			mi.inst.LocalURL = ""
		}
		m.mu.Unlock()
		mi.opMu.Unlock()
		if b != nil {
			_ = b.Close()
		}
	}
	m.closeAllCommTunnels()
	return nil
}

// IsCloudMode reports whether host port-bridge listeners and same-origin proxy HTML are disabled.
func (m *Manager) IsCloudMode() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.CloudMode
}

// SetCloudMode updates CloudMode dynamically.
func (m *Manager) SetCloudMode(cloudMode bool) {
	m.mu.Lock()
	m.cfg.CloudMode = cloudMode
	m.mu.Unlock()
}

// CheckSameOriginProxyAllowed enforces P0 Same-Origin XSS isolation on `/apps/proxy/{id}/`.
// In ALL live modes (`!demoMode`, including keyless local mode AND cloud/protected deployments),
// untrusted Studio HTML MUST NOT be served on the control-plane origin (`127.0.0.1:7860` etc.):
// local apps use their dedicated isolated loopback port (`127.0.0.1:<localPort>`), and cloud
// deployments require an explicit separate `IsolatedProxyOrigin` matching the request Host!
func (m *Manager) CheckSameOriginProxyAllowed(r *http.Request) error {
	m.mu.RLock()
	cloudMode := m.cfg.CloudMode
	demoMode := m.cfg.DemoMode
	isolatedOrigin := strings.TrimSpace(m.cfg.IsolatedProxyOrigin)
	m.mu.RUnlock()

	if cloudMode {
		if isolatedOrigin != "" && r != nil {
			if u, err := url.Parse(isolatedOrigin); err == nil && u.Host != "" && strings.EqualFold(strings.TrimSpace(r.Host), u.Host) {
				return nil
			}
		}
		return ErrIsolatedOriginRequired
	}
	if demoMode {
		return nil
	}
	if isolatedOrigin != "" && r != nil {
		if u, err := url.Parse(isolatedOrigin); err == nil && u.Host != "" && strings.EqualFold(strings.TrimSpace(r.Host), u.Host) {
			return nil
		}
	}
	return ErrIsolatedOriginRequired
}

// ListTemplates returns the 5 real built-in core templates (`llm-chat`, `wangp`, `comfyui`, `unsloth-studio`, `jupyterlab`)
// followed by all validated community/third-party plugins loaded from `<dataDir>/plugins/*.json` and `~/.colabhive/plugins/*.json`.
func (m *Manager) ListTemplates() []models.AppTemplate {
	m.ReloadPlugins()
	out := CuratedTemplates()

	m.mu.RLock()
	pluginList := make([]models.AppTemplate, 0, len(m.plugins))
	for _, ent := range m.plugins {
		if ent != nil && ent.manifest.Kind != models.PluginKindUI {
			pluginList = append(pluginList, cloneAppTemplate(ent.template))
		}
	}
	m.mu.RUnlock()

	sort.Slice(pluginList, func(i, j int) bool {
		return pluginList[i].ID < pluginList[j].ID
	})
	return append(out, pluginList...)
}

// GetTemplate looks up a built-in template or loaded third-party plugin template by ID.
func (m *Manager) GetTemplate(id string) (models.AppTemplate, bool) {
	if tpl, ok := FindTemplate(id); ok {
		return tpl, true
	}
	m.ReloadPlugins()
	target := strings.ToLower(strings.TrimSpace(id))
	m.mu.RLock()
	defer m.mu.RUnlock()
	if ent, ok := m.plugins[target]; ok && ent != nil {
		return cloneAppTemplate(ent.template), true
	}
	return models.AppTemplate{}, false
}

func (m *Manager) resolveTemplateAndPlugin(id string) (models.AppTemplate, *AppPluginManifest, bool) {
	if tpl, ok := FindTemplate(id); ok {
		return tpl, nil, true
	}
	m.ReloadPlugins()
	target := strings.ToLower(strings.TrimSpace(id))
	m.mu.RLock()
	defer m.mu.RUnlock()
	if ent, ok := m.plugins[target]; ok && ent != nil {
		manCopy := clonePluginManifest(ent.manifest)
		return cloneAppTemplate(ent.template), &manCopy, true
	}
	return models.AppTemplate{}, nil, false
}

// ListInstances returns deep copies of all AppInstances ordered newest StartedAt first.
func (m *Manager) ListInstances() []*models.AppInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*models.AppInstance, 0, len(m.instances))
	for _, mi := range m.instances {
		if mi != nil && mi.inst != nil {
			out = append(out, cloneAppInstance(mi.inst))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// GetInstance returns a deep copy of a single AppInstance by ID.
func (m *Manager) GetInstance(id string) (*models.AppInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mi, ok := m.instances[strings.TrimSpace(id)]
	if !ok || mi == nil || mi.inst == nil {
		return nil, ErrAppInstanceNotFound
	}
	return cloneAppInstance(mi.inst), nil
}

// LastCheckpointSyncTime returns the timestamp of the last successful periodic checkpoint sync
// for an instance (never advanced on failed syncs).
func (m *Manager) LastCheckpointSyncTime(id string) (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mi, ok := m.instances[strings.TrimSpace(id)]
	if !ok || mi == nil {
		return time.Time{}, false
	}
	return mi.lastCheckpointSync, true
}

// UpdateConfig validates and persists mutable configuration (`idle_auto_stop_min`, `auto_resume_enabled`)
// directly onto an existing AppInstance, serialized under `mi.opMu`.
func (m *Manager) UpdateConfig(_ context.Context, id string, patch AppInstanceConfigPatch) (*models.AppInstance, error) {
	if patch.IdleAutoStopMin == nil && patch.AutoResumeEnabled == nil {
		return nil, errors.New("invalid instance config patch: at least one field is required")
	}
	if patch.IdleAutoStopMin != nil {
		v := *patch.IdleAutoStopMin
		if v < minAppIdleAutoStopMin || v > maxAppIdleAutoStopMin {
			return nil, fmt.Errorf("invalid idle_auto_stop_min %d: must be between %d and %d minutes", v, minAppIdleAutoStopMin, maxAppIdleAutoStopMin)
		}
	}

	targetID := strings.TrimSpace(id)
	m.mu.RLock()
	mi, ok := m.instances[targetID]
	m.mu.RUnlock()
	if !ok || mi == nil || mi.inst == nil {
		return nil, ErrAppInstanceNotFound
	}

	mi.opMu.Lock()
	defer mi.opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("app store manager is closed")
	}
	if patch.IdleAutoStopMin != nil {
		mi.inst.IdleAutoStopMin = *patch.IdleAutoStopMin
	}
	if patch.AutoResumeEnabled != nil {
		mi.inst.AutoResumeEnabled = *patch.AutoResumeEnabled
	}
	outCopy := cloneAppInstance(mi.inst)
	m.mu.Unlock()

	m.publishInstanceEvent(outCopy)
	return outCopy, nil
}

func resolveLaunchModelConfig(tpl models.AppTemplate, req models.AppLaunchRequest) (resolvedModelConfig, error) {
	selectedID := strings.TrimSpace(req.SelectedModelID)
	customURL := strings.TrimSpace(req.ModelDownloadURL)

	if customURL != "" {
		if !tpl.SupportsCustomModelURL {
			return resolvedModelConfig{}, fmt.Errorf("invalid model_download_url: template %q does not support custom model downloads", tpl.ID)
		}
		cleanURL, derivedFile, err := ValidateAndSanitizeModelDownloadURL(customURL)
		if err != nil {
			return resolvedModelConfig{}, err
		}
		// If the URL exactly matches one of the template's curated model options, reuse its metadata.
		for _, opt := range tpl.ModelOptions {
			if opt.DownloadURL != "" && strings.EqualFold(strings.TrimSpace(opt.DownloadURL), cleanURL) {
				sub := opt.TargetSubdir
				if sub == "" {
					sub = "models/checkpoints"
				}
				fn := opt.Filename
				if fn == "" {
					fn = derivedFile
				}
				return resolvedModelConfig{
					id:             opt.ID,
					name:           firstStr(opt.NameZh, opt.Name, opt.ID),
					downloadURL:    cleanURL,
					targetSubdir:   sub,
					filename:       fn,
					recommendedGPU: opt.RecommendedGPU,
				}, nil
			}
		}
		if selectedID == "" {
			selectedID = "custom"
		}
		return resolvedModelConfig{
			id:           selectedID,
			name:         "Custom (" + derivedFile + ")",
			downloadURL:  cleanURL,
			targetSubdir: "models/checkpoints",
			filename:     derivedFile,
		}, nil
	}

	if selectedID != "" {
		for _, opt := range tpl.ModelOptions {
			if strings.EqualFold(opt.ID, selectedID) {
				cfg := resolvedModelConfig{
					id:             opt.ID,
					name:           firstStr(opt.NameZh, opt.Name, opt.ID),
					recommendedGPU: opt.RecommendedGPU,
				}
				if strings.TrimSpace(opt.DownloadURL) != "" {
					cleanURL, derivedFile, err := ValidateAndSanitizeModelDownloadURL(opt.DownloadURL)
					if err != nil {
						return resolvedModelConfig{}, err
					}
					sub, err := ValidateModelTargetSubdir(opt.TargetSubdir)
					if err != nil {
						return resolvedModelConfig{}, err
					}
					fn := derivedFile
					if strings.TrimSpace(opt.Filename) != "" {
						fn, err = ValidateModelFilename(opt.Filename)
						if err != nil {
							return resolvedModelConfig{}, err
						}
					}
					cfg.downloadURL = cleanURL
					cfg.targetSubdir = sub
					cfg.filename = fn
				}
				return cfg, nil
			}
		}
		if len(tpl.ModelOptions) > 0 {
			return resolvedModelConfig{}, fmt.Errorf("invalid selected_model_id %q: unknown model option for template %q", req.SelectedModelID, tpl.ID)
		}
		return resolvedModelConfig{
			id:   selectedID,
			name: selectedID,
		}, nil
	}

	if len(tpl.ModelOptions) > 0 {
		opt := tpl.ModelOptions[0]
		cfg := resolvedModelConfig{
			id:             opt.ID,
			name:           firstStr(opt.NameZh, opt.Name, opt.ID),
			recommendedGPU: opt.RecommendedGPU,
		}
		if strings.TrimSpace(opt.DownloadURL) != "" {
			cleanURL, derivedFile, err := ValidateAndSanitizeModelDownloadURL(opt.DownloadURL)
			if err != nil {
				return resolvedModelConfig{}, err
			}
			sub, err := ValidateModelTargetSubdir(opt.TargetSubdir)
			if err != nil {
				return resolvedModelConfig{}, err
			}
			fn := derivedFile
			if strings.TrimSpace(opt.Filename) != "" {
				fn, err = ValidateModelFilename(opt.Filename)
				if err != nil {
					return resolvedModelConfig{}, err
				}
			}
			cfg.downloadURL = cleanURL
			cfg.targetSubdir = sub
			cfg.filename = fn
		}
		return cfg, nil
	}

	return resolvedModelConfig{}, nil
}

// Launch allocates a pooled GPU session with explicit GPU, account, and model selection,
// runs the core template or community plugin bootstrap script, binds a private `127.0.0.1`
// port bridge (when `CloudMode == false`), and arms auto-checkpointing.
// In live CloudMode (`CloudMode && !DemoMode`) without an `IsolatedProxyOrigin`, Launch fails closed
// with `ErrIsolatedOriginRequired` (HTTP 422) BEFORE allocating any billing GPU VM.
func (m *Manager) Launch(ctx context.Context, req models.AppLaunchRequest) (*models.AppInstance, error) {
	if m.rt == nil {
		return nil, errors.New("runtime engine is required to launch app")
	}

	m.mu.RLock()
	cloudMode := m.cfg.CloudMode
	demoMode := m.cfg.DemoMode
	isolatedOrigin := strings.TrimSpace(m.cfg.IsolatedProxyOrigin)
	defaultIdleMin := m.cfg.DefaultIdleAutoStopMin
	m.mu.RUnlock()

	if cloudMode && !demoMode && isolatedOrigin == "" {
		return nil, ErrIsolatedOriginRequired
	}

	if strings.EqualFold(strings.TrimSpace(req.TemplateID), "llm-chat") {
		return nil, errors.New(`invalid template_id "llm-chat": Uncensored LLM Deployer uses the dedicated LLM Gateway (/api/v1/llm/start and /apps/llm) so the selected model and GPU session stay synchronized`)
	}

	tpl, pluginMan, ok := m.resolveTemplateAndPlugin(req.TemplateID)
	if !ok {
		return nil, fmt.Errorf("invalid template_id %q: unknown app template", req.TemplateID)
	}
	if tpl.Kind == models.PluginKindUI {
		return nil, fmt.Errorf("plugin %q is a local UI plugin (kind=ui) and runs locally via /sdk/v1/ui-plugins/%s/open rather than allocating a cloud GPU", tpl.ID, tpl.ID)
	}

	var cleanRepoURL, cleanCmd string
	checkpointDir := appCheckpointRemoteDir(tpl.ID)
	healthPath := "/"
	if pluginMan != nil {
		cleanRepoURL = pluginMan.RepoURL
		cleanCmd = pluginMan.StartCommand
		if pluginMan.CheckpointDir != "" {
			checkpointDir = pluginMan.CheckpointDir
		}
		if pluginMan.HealthPath != "" {
			healthPath = pluginMan.HealthPath
		}
	}

	if strings.TrimSpace(req.CustomRepoURL) != "" {
		var err error
		cleanRepoURL, _, err = ValidateAndSanitizeGitHubRepoURL(req.CustomRepoURL)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(req.CustomCommand) != "" {
		var err error
		cleanCmd, err = ValidateCustomStartCommand(req.CustomCommand)
		if err != nil {
			return nil, err
		}
	}

	modelCfg, mErr := resolveLaunchModelConfig(tpl, req)
	if mErr != nil {
		return nil, mErr
	}

	remotePort := tpl.DefaultRemotePort
	if req.CustomRemotePort > 0 && req.CustomRemotePort <= 65535 {
		remotePort = req.CustomRemotePort
	}
	if remotePort <= 0 {
		remotePort = 8188
	}

	idleMin := req.IdleAutoStopMin
	if idleMin == 0 {
		idleMin = defaultIdleMin
	} else if idleMin < minAppIdleAutoStopMin || idleMin > maxAppIdleAutoStopMin {
		return nil, fmt.Errorf("invalid idle_auto_stop_min %d: must be between %d and %d minutes", idleMin, minAppIdleAutoStopMin, maxAppIdleAutoStopMin)
	}

	autoResume := true
	if m.ckpt != nil {
		autoResume = m.ckpt.GetPolicy().AutoResumeEnabled
	}
	if req.AutoResumeEnabled != nil {
		autoResume = *req.AutoResumeEnabled
	}

	// Explicit GPU selection: respect user's req.PreferredGPU (NEVER override with T4!),
	// falling back to selected model's RecommendedGPU -> template's RecommendedGPU -> T4.
	explicitGPU := strings.TrimSpace(string(req.PreferredGPU)) != ""
	var gpu models.AcceleratorType
	if explicitGPU {
		normGPU, validGPU := normalizeKnownAccelerator(req.PreferredGPU)
		if !validGPU {
			return nil, fmt.Errorf("invalid preferred_gpu %q", req.PreferredGPU)
		}
		gpu = normGPU
	} else if modelCfg.recommendedGPU != "" {
		gpu = modelCfg.recommendedGPU
	} else if tpl.RecommendedGPU != "" {
		gpu = tpl.RecommendedGPU
	} else {
		gpu = models.AcceleratorT4
	}

	// Explicit account selection: pin via `AccountSelector: "id:<accountID>"`.
	accountSelector := ""
	if rawAcc := strings.TrimSpace(req.AccountID); rawAcc != "" {
		if strings.HasPrefix(strings.ToLower(rawAcc), "id:") {
			rawAcc = strings.TrimSpace(rawAcc[3:])
		}
		if rawAcc == "" {
			return nil, errors.New("invalid account_id: cannot be empty")
		}
		accountSelector = "id:" + rawAcc
	}

	strategy := req.Strategy
	if strategy == "" {
		if gpu == models.AcceleratorT4 && tpl.FreeTierCompatible {
			strategy = models.StrategyFreeFirst
		} else {
			strategy = models.StrategyWasteZero
		}
	}
	autoFallback := !explicitGPU
	if req.AutoFallback != nil {
		autoFallback = *req.AutoFallback
	}
	queueRetry := req.QueueRetryEnabled
	queueMaxWaitSec := req.QueueMaxWaitSec
	if queueRetry {
		if queueMaxWaitSec <= 0 {
			queueMaxWaitSec = 600 // Default 10 min safe polite-backoff waitlist
		} else if queueMaxWaitSec < 60 {
			queueMaxWaitSec = 60
		} else if queueMaxWaitSec > 1800 {
			queueMaxWaitSec = 1800
		}
	}

	instanceID := models.NewPrefixedID("app_")
	appName := strings.TrimSpace(checkpoint.RedactEmailStrings(req.Name))
	appNameZh := tpl.NameZh
	if appName == "" {
		appName = tpl.Name
	} else {
		appNameZh = appName
	}

	webProxyURL := fmt.Sprintf("/apps/proxy/%s/", instanceID)
	if isolatedOrigin != "" {
		webProxyURL = strings.TrimRight(isolatedOrigin, "/") + webProxyURL
	}

	launchMode := strings.ToLower(strings.TrimSpace(req.LaunchMode))
	if launchMode == "" {
		if tpl.MediaAPIBase != "" {
			launchMode = "headless"
		} else {
			launchMode = "native_webui"
		}
	} else if launchMode != "headless" && launchMode != "native_webui" {
		return nil, fmt.Errorf("invalid launch_mode %q: must be headless or native_webui", req.LaunchMode)
	}
	studioRoute := ""
	if tpl.UI != nil && tpl.UI.Mode == "builtin_route" && tpl.UI.Route != "" {
		studioRoute = tpl.UI.Route
	}
	if launchMode == "native_webui" && tpl.ID == "wangp" {
		// In explicit classic Gradio mode, do not advertise headless media API.
		tpl.MediaAPIBase = ""
	}

	now := time.Now().UTC()
	allocStepEn := fmt.Sprintf("Allocating %s GPU from pool...", gpu)
	allocStepZh := fmt.Sprintf("正在从算力池申请 %s 显卡...", gpu)
	inst := &models.AppInstance{
		ID:                instanceID,
		TemplateID:        tpl.ID,
		Name:              appName,
		NameZh:            appNameZh,
		Category:          tpl.Category,
		State:             models.AppStateStarting,
		StatusStep:        allocStepEn,
		StatusStepZh:      allocStepZh,
		ProgressPct:       10,
		AssignedGPU:       gpu,
		RemotePort:        remotePort,
		LocalBridgeActive: false,
		WebProxyURL:       webProxyURL,
		IdleAutoStopMin:   idleMin,
		AutoResumeEnabled: autoResume,
		SelectedModelID:   modelCfg.id,
		SelectedModelName: modelCfg.name,
		ModelDownloadURL:  modelCfg.downloadURL,
		CheckpointDir:     checkpointDir,
		CustomRepoURL:     cleanRepoURL,
		CustomCommand:     cleanCmd,
		Provides:          append([]string(nil), tpl.Provides...),
		MediaAPIBase:      tpl.MediaAPIBase,
		StudioRoute:       studioRoute,
		LaunchMode:        launchMode,
		StartedAt:         now,
		LastActiveAt:      now,
	}
	appendInstanceDeployLogLocked(inst, fmt.Sprintf("开始启动「%s」（目标显卡：%s，方案：%s）", appNameZh, gpu, firstStr(modelCfg.name, "默认环境")))
	appendInstanceDeployLogLocked(inst, allocStepZh)

	opCtx, opCancel := context.WithCancel(ctx)
	mi := &managedInstance{
		inst:               inst,
		tpl:                tpl,
		cleanRepo:          cleanRepoURL,
		cleanCmd:           cleanCmd,
		checkpointDir:      checkpointDir,
		healthPath:         healthPath,
		accountSelector:    accountSelector,
		preferredGPU:       gpu,
		explicitGPU:        explicitGPU,
		model:              modelCfg,
		lastCheckpointSync: now,
		launchCancel:       opCancel,
	}

	mi.opMu.Lock()
	defer mi.opMu.Unlock()
	defer func() {
		opCancel()
		m.mu.Lock()
		mi.launchCancel = nil
		m.mu.Unlock()
	}()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("app store manager is closed")
	}
	m.instances[instanceID] = mi
	preAllocSnap := cloneAppInstance(inst)
	m.mu.Unlock()
	m.publishInstanceEvent(preAllocSnap)

	allocReq := models.SessionAllocRequest{
		Name:            fmt.Sprintf("app-%s-%s", tpl.ID, instanceID[len("app_"):len("app_")+6]),
		Accelerator:     gpu,
		Strategy:        strategy,
		AccountSelector: accountSelector,
		AutoFallback:    autoFallback,
		CallerName:      "appstore:" + tpl.ID,
		OnProgress: func(stepEn, stepZh string) {
			m.mu.Lock()
			if inst.State == models.AppStateStarting {
				inst.StatusStep = checkpoint.RedactEmailStrings(stepEn)
				inst.StatusStepZh = checkpoint.RedactEmailStrings(stepZh)
				if inst.ProgressPct < 38 {
					inst.ProgressPct += 4
					if inst.ProgressPct > 38 {
						inst.ProgressPct = 38
					}
				}
				appendInstanceDeployLogLocked(inst, firstStr(stepZh, stepEn))
				snap := cloneAppInstance(inst)
				m.mu.Unlock()
				m.publishInstanceEvent(snap)
				return
			}
			m.mu.Unlock()
		},
	}
	sess, err := m.allocateSessionWithWaitlist(opCtx, inst, allocReq, queueRetry, time.Duration(queueMaxWaitSec)*time.Second)
	if err != nil {
		m.mu.Lock()
		if errors.Is(err, context.Canceled) {
			inst.State = models.AppStateStopped
			inst.SessionID = ""
			inst.StatusStep = "Launch canceled · zero GPU quota burning"
			inst.StatusStepZh = "已取消启动 · 当前 0 消耗"
			appendInstanceDeployLogLocked(inst, "已取消启动（未占用显卡，当前 0 消耗）")
			snap := cloneAppInstance(inst)
			m.mu.Unlock()
			m.publishInstanceEvent(snap)
			return nil, context.Canceled
		}
		delete(m.instances, instanceID)
		m.mu.Unlock()
		return nil, fmt.Errorf("allocate GPU session for %s: %s", tpl.ID, sanitizeAppStoreError(err))
	}

	_, provider, freeActive, accLabel := m.resolveSessionAccountMeta(opCtx, sess)

	bootStep := "Bootstrapping GPU environment..."
	bootStepZh := "正在云端显卡初始化应用环境..."
	if modelCfg.downloadURL != "" {
		bootStep = fmt.Sprintf("Downloading model weights (%s) & starting %s...", modelCfg.filename, tpl.Name)
		bootStepZh = fmt.Sprintf("正在真实下载模型权重 (%s) 并启动 %s...", modelCfg.filename, tpl.NameZh)
	}

	m.mu.Lock()
	if m.closed {
		delete(m.instances, instanceID)
		m.mu.Unlock()
		m.stopSessionBounded(ctx, sess.ID)
		return nil, errors.New("app store manager is closed")
	}
	inst.StatusStep = bootStep
	inst.StatusStepZh = bootStepZh
	inst.ProgressPct = 45
	inst.SessionID = sess.ID
	inst.AccountID = sess.AccountID
	inst.AccountLabel = accLabel
	inst.Provider = provider
	inst.AssignedGPU = sess.Accelerator
	inst.FreeTierActive = freeActive
	appendInstanceDeployLogLocked(inst, fmt.Sprintf("已分配云端显卡：%s · %s（会话 %s）", sess.Accelerator, accLabel, sess.ID))
	appendInstanceDeployLogLocked(inst, bootStepZh)
	m.persistInstancesLocked()
	startSnap := cloneAppInstance(inst)
	m.mu.Unlock()
	m.publishInstanceEvent(startSnap)

	if err := m.runBootstrapOnSessionForInstance(opCtx, instanceID, sess.ID, tpl, remotePort, cleanRepoURL, cleanCmd, checkpointDir, healthPath, modelCfg); err != nil {
		canceled := errors.Is(err, context.Canceled) || opCtx.Err() != nil
		cleanMsg := sanitizeAppStoreError(err)
		m.mu.Lock()
		if canceled {
			appendInstanceDeployLogLocked(inst, "收到取消指令，正在自动释放云端显卡...")
		} else {
			appendInstanceDeployLogLocked(inst, "部署失败："+cleanMsg)
			appendInstanceDeployLogLocked(inst, fmt.Sprintf("正在自动释放云端显卡（%s · %s）以避免继续计费...", sess.Accelerator, sess.ID))
		}
		m.mu.Unlock()

		stopErr := m.stopSessionBounded(ctx, sess.ID)
		m.mu.Lock()
		if canceled {
			inst.State = models.AppStateStopped
			if stopErr == nil {
				inst.SessionID = ""
				inst.StatusStep = "Launch canceled · cloud GPU released (0 burn)"
				inst.StatusStepZh = "已取消部署 · 已自动释放云端显卡（当前 0 消耗）"
				appendInstanceDeployLogLocked(inst, "已取消部署并释放云端显卡，当前 0 消耗")
			} else {
				inst.State = models.AppStateError
				inst.ErrorMessage = "cancel teardown failed: " + sanitizeAppStoreError(stopErr)
				inst.StatusStep = "Cancel teardown failed — retry Stop"
				inst.StatusStepZh = "取消部署时释放显卡失败，请点击停止释放重试"
			}
			cCopy := cloneAppInstance(inst)
			m.persistInstancesLocked()
			m.mu.Unlock()
			m.publishInstanceEvent(cCopy)
			return nil, context.Canceled
		}
		inst.State = models.AppStateError
		if stopErr == nil {
			inst.SessionID = ""
			inst.StatusStep = "Bootstrap failed · cloud GPU automatically released (0 burn)"
			inst.StatusStepZh = "启动失败 · 已自动释放云端显卡（当前 0 消耗）"
			appendInstanceDeployLogLocked(inst, "已自动释放云端显卡会话，当前 0 额度消耗")
		} else {
			inst.StatusStep = "Bootstrap failed & GPU teardown failed — retry Stop"
			inst.StatusStepZh = "启动失败且显卡释放失败 — 请点击停止释放重试"
			appendInstanceDeployLogLocked(inst, "自动释放显卡失败："+sanitizeAppStoreError(stopErr))
		}
		inst.ErrorMessage = cleanMsg
		m.persistInstancesLocked()
		errCopy := cloneAppInstance(inst)
		m.mu.Unlock()
		m.publishInstanceEvent(errCopy)
		return nil, errors.New(cleanMsg)
	}

	var bridge *localBridge
	if !cloudMode && tpl.SupportsPortBridge {
		prefPort := req.PreferredLocalPort
		if prefPort <= 0 {
			prefPort = tpl.DefaultLocalPort
		}
		b, bErr := startLocalPortBridge(instanceID, prefPort, m.newBridgeHTTPHandler(instanceID))
		if bErr != nil {
			stopErr := m.stopSessionBounded(ctx, sess.ID)
			m.mu.Lock()
			inst.State = models.AppStateError
			if stopErr == nil {
				inst.SessionID = ""
			}
			inst.ErrorMessage = bErr.Error()
			m.mu.Unlock()
			return nil, bErr
		}
		bridge = b
	}

	decoratedLocalURL := ""
	if bridge != nil {
		decoratedLocalURL = m.decorateLocalBridgeURL(opCtx, tpl.ID, sess.ID, bridge.url)
	}

	readyAt := time.Now().UTC()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		if bridge != nil {
			_ = bridge.Close()
		}
		m.stopSessionBounded(ctx, sess.ID)
		return nil, errors.New("app store manager is closed")
	}
	oldBridge := mi.bridge
	mi.bridge = bridge
	mi.lastCheckpointSync = readyAt
	if bridge != nil {
		inst.LocalPort = bridge.port
		inst.LocalBridgeActive = true
		inst.LocalURL = decoratedLocalURL
		inst.StatusStep = "Running · Private localhost bridge ready"
		inst.StatusStepZh = fmt.Sprintf("运行中 · 本机私有直连 %s 已就绪", bridge.url)
	} else {
		inst.LocalPort = 0
		inst.LocalBridgeActive = false
		inst.LocalURL = ""
		inst.StatusStep = "Running · Isolated cloud runtime ready"
		inst.StatusStepZh = "运行中 · 云端隔离运行环境已就绪"
	}
	inst.State = models.AppStateRunning
	inst.ProgressPct = 100
	inst.LastActiveAt = readyAt
	appendInstanceDeployLogLocked(inst, "部署完成 · "+inst.StatusStepZh)
	m.persistInstancesLocked()
	outCopy := cloneAppInstance(inst)
	m.mu.Unlock()

	if oldBridge != nil && oldBridge != bridge {
		_ = oldBridge.Close()
	}

	if autoResume && m.ckpt != nil {
		_, _ = m.ckpt.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
			SessionID:   sess.ID,
			JobID:       instanceID,
			JobTitle:    appNameZh,
			JobKind:     "app",
			RemotePath:  checkpointDir,
			ProgressPct: 100,
		})
	}

	m.publishInstanceEvent(outCopy)
	return outCopy, nil
}

// Stop closes the localhost port bridge listener cleanly and releases the underlying GPU session
// with `force=false` (protecting against silent loss of unsaved data if final pre-stop checkpoint sync fails).
func (m *Manager) Stop(ctx context.Context, id string) (*models.AppInstance, error) {
	return m.StopWithOptions(ctx, id, false)
}

// StopWithOptions stops the app instance.
// When `AutoResumeEnabled` is true and `!force`, it runs final pre-stop checkpoint sync FIRST:
// if that sync fails, it returns `ErrPreStopCheckpointSyncFailed` WITHOUT closing the bridge or
// tearing down the GPU VM (keeping instance state intact so the user can retry or pass `force=true`).
// NEVER swallows `rt.StopSession` errors: if teardown fails, `SessionID` stays intact and State becomes `AppStateError`.
func (m *Manager) StopWithOptions(ctx context.Context, id string, force bool) (*models.AppInstance, error) {
	targetID := strings.TrimSpace(id)
	m.mu.RLock()
	mi, ok := m.instances[targetID]
	var cancelLaunch context.CancelFunc
	if ok && mi != nil {
		cancelLaunch = mi.launchCancel
	}
	m.mu.RUnlock()
	if !ok || mi == nil || mi.inst == nil {
		return nil, ErrAppInstanceNotFound
	}
	// If the instance is currently mid-launch (GPU alloc or pip/bootstrap polling), abort it immediately
	// BEFORE waiting on mi.opMu so user cancel takes effect in milliseconds!
	if cancelLaunch != nil {
		cancelLaunch()
	}

	mi.opMu.Lock()
	defer mi.opMu.Unlock()

	m.mu.Lock()
	if mi.inst.State == models.AppStateStopped && mi.inst.SessionID == "" && mi.bridge == nil {
		cp := cloneAppInstance(mi.inst)
		m.mu.Unlock()
		return cp, nil
	}
	prevState := mi.inst.State
	sessID := mi.inst.SessionID
	autoResume := mi.inst.AutoResumeEnabled
	ckptDir := mi.checkpointDir
	if ckptDir == "" {
		ckptDir = appCheckpointRemoteDir(mi.inst.TemplateID)
	}
	titleZh := mi.inst.NameZh
	mi.inst.State = models.AppStateStopping
	mi.inst.StatusStep = "Saving final checkpoint before stopping..."
	mi.inst.StatusStepZh = "正在安全保存进度并准备释放显卡..."
	m.mu.Unlock()

	if autoResume && m.ckpt != nil && sessID != "" {
		pol := m.ckpt.GetPolicy()
		_, syncErr := m.syncInstanceCheckpoints(ctx, sessID, targetID, titleZh, ckptDir, pol.WatchPaths)
		if syncErr != nil && !force {
			now := time.Now().UTC()
			m.mu.Lock()
			// Restore previous state intact without closing bridge or tearing down the VM!
			mi.inst.State = prevState
			mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(fmt.Sprintf("pre-stop checkpoint sync failed: %v", syncErr))
			mi.inst.StatusStep = "Stop aborted: final checkpoint backup failed (retry Stop or Force Stop)"
			mi.inst.StatusStepZh = "关机前进度备份失败，已保留云端实例（可重试备份关机或强制关机）"
			mi.inst.LastActiveAt = now
			abortedCopy := cloneAppInstance(mi.inst)
			m.mu.Unlock()

			m.publishInstanceEvent(abortedCopy)
			return abortedCopy, fmt.Errorf("%w (retry or pass force=true): %w", ErrPreStopCheckpointSyncFailed, syncErr)
		}
	}

	m.mu.Lock()
	bridge := mi.bridge
	mi.bridge = nil
	mi.inst.LocalBridgeActive = false
	mi.inst.LocalPort = 0
	mi.inst.LocalURL = ""
	mi.inst.StatusStep = "Stopping GPU session..."
	mi.inst.StatusStepZh = "正在释放云端显卡会话..."
	m.mu.Unlock()

	if bridge != nil {
		_ = bridge.Close()
	}
	m.closeCommTunnelsForSession(sessID)

	if sessID != "" && m.rt != nil {
		if stopErr := m.rt.StopSession(ctx, sessID); stopErr != nil {
			now := time.Now().UTC()
			m.mu.Lock()
			// Preserve SessionID intact so retry Stop() targets the still-billing GPU VM!
			mi.inst.SessionID = sessID
			mi.inst.State = models.AppStateError
			mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(fmt.Sprintf("stop GPU session %s failed: %v", sessID, stopErr))
			mi.inst.StatusStep = "Stop failed — GPU session may still be active; retry Stop"
			mi.inst.StatusStepZh = "停止云端显卡会话失败，请重试停止以防继续计费"
			mi.inst.LastActiveAt = now
			errCopy := cloneAppInstance(mi.inst)
			m.mu.Unlock()

			m.publishInstanceEvent(errCopy)
			return errCopy, fmt.Errorf("stop GPU session %s: %w", sessID, stopErr)
		}
	}

	now := time.Now().UTC()
	m.mu.Lock()
	mi.inst.SessionID = ""
	mi.inst.State = models.AppStateStopped
	mi.inst.ErrorMessage = ""
	mi.inst.LocalBridgeActive = false
	mi.inst.LocalPort = 0
	mi.inst.LocalURL = ""
	mi.inst.StatusStep = "Stopped"
	mi.inst.StatusStepZh = "已关机 · 算力额度已停止计费"
	mi.inst.LastActiveAt = now
	m.persistInstancesLocked()
	outCopy := cloneAppInstance(mi.inst)
	m.mu.Unlock()

	m.publishInstanceEvent(outCopy)
	return outCopy, nil
}

// Restart stops any previous VM/bridge for the instance and provisions a fresh GPU runtime & bridge,
// preserving the instance's GPU, account pin, and selected model config.
func (m *Manager) Restart(ctx context.Context, id string) (*models.AppInstance, error) {
	targetID := strings.TrimSpace(id)
	m.mu.RLock()
	mi, ok := m.instances[targetID]
	cloudMode := m.cfg.CloudMode
	demoMode := m.cfg.DemoMode
	isolatedOrigin := strings.TrimSpace(m.cfg.IsolatedProxyOrigin)
	m.mu.RUnlock()
	if !ok || mi == nil || mi.inst == nil {
		return nil, ErrAppInstanceNotFound
	}
	if cloudMode && !demoMode && isolatedOrigin == "" {
		return nil, ErrIsolatedOriginRequired
	}

	mi.opMu.Lock()
	defer mi.opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("app store manager is closed")
	}
	prevState := mi.inst.State
	oldBridge := mi.bridge
	mi.bridge = nil
	oldSessID := mi.inst.SessionID
	autoResume := mi.inst.AutoResumeEnabled
	jobTitle := mi.inst.NameZh
	if jobTitle == "" {
		jobTitle = mi.inst.Name
	}
	prefPort := mi.inst.LocalPort
	if prefPort <= 0 {
		prefPort = mi.tpl.DefaultLocalPort
	}
	tpl := mi.tpl
	remotePort := mi.inst.RemotePort
	cleanRepo := mi.cleanRepo
	cleanCmd := mi.cleanCmd
	ckptDir := mi.checkpointDir
	if ckptDir == "" {
		ckptDir = appCheckpointRemoteDir(tpl.ID)
	}
	healthPath := mi.healthPath
	if healthPath == "" {
		healthPath = "/"
	}
	accountSelector := mi.accountSelector
	explicitGPU := mi.explicitGPU
	modelCfg := mi.model
	gpu := mi.preferredGPU
	if gpu == "" {
		gpu = mi.inst.AssignedGPU
	}
	if gpu == "" {
		gpu = tpl.RecommendedGPU
	}
	// Quiesce inbound writes BEFORE final checkpoint sync so NO browser save can be acknowledged
	// after files are copied and before the old VM is replaced!
	mi.inst.State = models.AppStateStopping
	mi.inst.LocalBridgeActive = false
	mi.inst.LocalPort = 0
	mi.inst.LocalURL = ""
	mi.inst.StatusStep = "Saving final checkpoint before restart..."
	mi.inst.StatusStepZh = "正在冻结写入并备份进度，准备重启..."
	m.mu.Unlock()

	if oldBridge != nil {
		_ = oldBridge.Close()
	}
	m.closeCommTunnelsForSession(oldSessID)

	// P0 Checkpoint Protection on Restart: sync all workspace/checkpoint paths BEFORE destroying the old VM!
	if autoResume && m.ckpt != nil && oldSessID != "" {
		pol := m.ckpt.GetPolicy()
		if _, syncErr := m.syncInstanceCheckpoints(ctx, oldSessID, targetID, jobTitle, ckptDir, pol.WatchPaths); syncErr != nil && !errors.Is(syncErr, checkpoint.ErrCheckpointEmpty) {
			// Restore bridge & previous running state intact on the preserved VM!
			var restoredBridge *localBridge
			if !cloudMode && tpl.SupportsPortBridge && prevState == models.AppStateRunning {
				restoredBridge, _ = startLocalPortBridge(targetID, prefPort, m.newBridgeHTTPHandler(targetID))
			}
			m.mu.Lock()
			mi.bridge = restoredBridge
			mi.inst.State = prevState
			if restoredBridge != nil {
				mi.inst.LocalPort = restoredBridge.port
				mi.inst.LocalBridgeActive = true
				mi.inst.LocalURL = restoredBridge.url
			}
			mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(fmt.Sprintf("pre-restart checkpoint sync failed: %v", syncErr))
			mi.inst.StatusStep = "Restart aborted: final checkpoint backup failed (VM preserved)"
			mi.inst.StatusStepZh = "重启前进度备份失败，已保留原实例"
			abortedCopy := cloneAppInstance(mi.inst)
			m.mu.Unlock()
			m.publishInstanceEvent(abortedCopy)
			return nil, fmt.Errorf("%w: pre-restart checkpoint sync failed (%v); running VM preserved", ErrPreStopCheckpointSyncFailed, syncErr)
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("app store manager is closed")
	}
	mi.inst.State = models.AppStateStarting
	mi.inst.ProgressPct = 35
	mi.inst.ErrorMessage = ""
	mi.inst.LocalBridgeActive = false
	mi.inst.LocalPort = 0
	mi.inst.LocalURL = ""
	mi.inst.StatusStep = "Restarting..."
	mi.inst.StatusStepZh = "正在重新分配显卡并启动应用..."
	m.mu.Unlock()
	if oldSessID != "" && m.rt != nil {
		if stopErr := m.rt.StopSession(ctx, oldSessID); stopErr != nil {
			// Only treat the canonical models.ErrSessionNotFound sentinel as already gone; never ignore storage/account errors!
			if !errors.Is(stopErr, models.ErrSessionNotFound) {
				m.mu.Lock()
				mi.inst.SessionID = oldSessID
				mi.inst.State = models.AppStateError
				mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(fmt.Sprintf("stop previous GPU session %s failed: %v", oldSessID, stopErr))
				errCopy := cloneAppInstance(mi.inst)
				m.mu.Unlock()
				m.publishInstanceEvent(errCopy)
				return nil, fmt.Errorf("stop previous GPU session %s before restart: %w", oldSessID, stopErr)
			}
		}
	}

	strategy := models.StrategyFreeFirst
	if gpu != models.AcceleratorT4 || !tpl.FreeTierCompatible {
		strategy = models.StrategyWasteZero
	}
	newSess, err := m.rt.CreateSession(ctx, models.SessionAllocRequest{
		Name:            fmt.Sprintf("app-%s-restart", tpl.ID),
		Accelerator:     gpu,
		Strategy:        strategy,
		AccountSelector: accountSelector,
		AutoFallback:    !explicitGPU,
		CallerName:      "appstore-restart:" + tpl.ID,
	})
	if err != nil {
		m.mu.Lock()
		mi.inst.SessionID = ""
		mi.inst.State = models.AppStateError
		mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(err.Error())
		errCopy := cloneAppInstance(mi.inst)
		m.mu.Unlock()
		m.publishInstanceEvent(errCopy)
		return nil, err
	}

	// Restore ALL latest verified path snapshots onto the fresh VM BEFORE starting the Studio so notebooks/outputs survive Restart!
	// Pass countAsAutoRelay=false: a manual Restart MUST NOT inflate automatic preemption-relay savings counters.
	if autoResume && m.ckpt != nil {
		snapshotsToRestore := m.collectLatestCheckpointsByPath(targetID, oldSessID)
		for _, rec := range snapshotsToRestore {
			if _, rErr := m.ckpt.RestoreSnapshotIntoSession(ctx, rec.ID, newSess.ID, false); rErr != nil {
				stopErr := m.stopSessionBounded(ctx, newSess.ID)
				m.mu.Lock()
				if stopErr == nil {
					mi.inst.SessionID = ""
				} else {
					mi.inst.SessionID = newSess.ID
				}
				mi.inst.State = models.AppStateError
				mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(fmt.Sprintf("restore checkpoint %s (%s) after restart failed: %v", rec.ID, rec.RemotePath, rErr))
				errCopy := cloneAppInstance(mi.inst)
				m.mu.Unlock()
				m.publishInstanceEvent(errCopy)
				return nil, fmt.Errorf("restore checkpoint %s (%s) after restart: %w", rec.ID, rec.RemotePath, rErr)
			}
		}
	}

	if bErr := m.runBootstrapOnSession(ctx, newSess.ID, tpl, remotePort, cleanRepo, cleanCmd, ckptDir, healthPath, modelCfg); bErr != nil {
		stopErr := m.stopSessionBounded(ctx, newSess.ID)
		m.mu.Lock()
		if stopErr == nil {
			mi.inst.SessionID = ""
		} else {
			mi.inst.SessionID = newSess.ID
		}
		mi.inst.State = models.AppStateError
		mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(bErr.Error())
		errCopy := cloneAppInstance(mi.inst)
		m.mu.Unlock()
		m.publishInstanceEvent(errCopy)
		return nil, bErr
	}

	var newBridge *localBridge
	if !cloudMode && tpl.SupportsPortBridge {
		b, lErr := startLocalPortBridge(targetID, prefPort, m.newBridgeHTTPHandler(targetID))
		if lErr != nil {
			stopErr := m.stopSessionBounded(ctx, newSess.ID)
			m.mu.Lock()
			if stopErr == nil {
				mi.inst.SessionID = ""
			} else {
				mi.inst.SessionID = newSess.ID
			}
			mi.inst.State = models.AppStateError
			mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(lErr.Error())
			errCopy := cloneAppInstance(mi.inst)
			m.mu.Unlock()
			m.publishInstanceEvent(errCopy)
			return nil, lErr
		}
		newBridge = b
	}

	_, provider, freeActive, accLabel := m.resolveSessionAccountMeta(ctx, newSess)
	decoratedRestartURL := ""
	if newBridge != nil {
		decoratedRestartURL = m.decorateLocalBridgeURL(ctx, tpl.ID, newSess.ID, newBridge.url)
	}
	now := time.Now().UTC()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		if newBridge != nil {
			_ = newBridge.Close()
		}
		m.stopSessionBounded(ctx, newSess.ID)
		return nil, errors.New("app store manager is closed")
	}
	supersededBridge := mi.bridge
	mi.bridge = newBridge
	mi.lastCheckpointSync = now
	mi.lastCheckpointAttempt = time.Time{}
	mi.checkpointFailCount = 0
	mi.inst.SessionID = newSess.ID
	mi.inst.AccountID = newSess.AccountID
	mi.inst.AccountLabel = accLabel
	mi.inst.Provider = provider
	mi.inst.AssignedGPU = newSess.Accelerator
	mi.inst.FreeTierActive = freeActive
	mi.inst.State = models.AppStateRunning
	mi.inst.ProgressPct = 100
	mi.inst.ErrorMessage = ""
	mi.inst.LastActiveAt = now
	if newBridge != nil {
		mi.inst.LocalPort = newBridge.port
		mi.inst.LocalBridgeActive = true
		mi.inst.LocalURL = decoratedRestartURL
		mi.inst.StatusStep = "Running · Private localhost bridge ready"
		mi.inst.StatusStepZh = fmt.Sprintf("运行中 · 本机私有直连 %s 已就绪", newBridge.url)
	} else {
		mi.inst.LocalPort = 0
		mi.inst.LocalBridgeActive = false
		mi.inst.LocalURL = ""
		mi.inst.StatusStep = "Running · Isolated cloud runtime ready"
		mi.inst.StatusStepZh = "运行中 · 云端隔离运行环境已就绪"
	}
	m.persistInstancesLocked()
	outCopy := cloneAppInstance(mi.inst)
	m.mu.Unlock()

	if supersededBridge != nil && supersededBridge != newBridge {
		_ = supersededBridge.Close()
	}

	m.publishInstanceEvent(outCopy)
	return outCopy, nil
}

// TriggerFailover relays an interrupted running AppInstance onto the next healthy account
// while keeping its localhost port bridge uninterrupted. Reports HONESTLY whether data was
// actually restored from a verified snapshot (`restoredFromSnapshot`) vs cleanly restarted.
func (m *Manager) TriggerFailover(ctx context.Context, id string) (*models.AppInstance, error) {
	targetID := strings.TrimSpace(id)
	m.mu.RLock()
	mi, ok := m.instances[targetID]
	m.mu.RUnlock()
	if !ok || mi == nil || mi.inst == nil {
		return nil, ErrAppInstanceNotFound
	}

	mi.opMu.Lock()
	defer mi.opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("app store manager is closed")
	}
	if mi.inst.State == models.AppStateStopped || mi.inst.State == models.AppStateStopping {
		cp := cloneAppInstance(mi.inst)
		m.mu.Unlock()
		return cp, nil
	}
	oldSessID := mi.inst.SessionID
	oldAccID := mi.inst.AccountID
	explicitGPU := mi.explicitGPU
	gpu := mi.inst.AssignedGPU
	if gpu == "" {
		gpu = mi.preferredGPU
	}
	tpl := mi.tpl
	remotePort := mi.inst.RemotePort
	cleanRepo := mi.cleanRepo
	cleanCmd := mi.cleanCmd
	modelCfg := mi.model
	ckptDir := mi.checkpointDir
	if ckptDir == "" {
		ckptDir = appCheckpointRemoteDir(tpl.ID)
	}
	healthPath := mi.healthPath
	if healthPath == "" {
		healthPath = "/"
	}
	mi.inst.State = models.AppStateFailover
	mi.inst.StatusStep = "Preempted · Switching to next healthy account..."
	mi.inst.StatusStepZh = "检测到原账号掉线/额度耗尽 · 正在切换新账号..."
	failoverSnap := cloneAppInstance(mi.inst)
	m.mu.Unlock()
	m.publishInstanceEvent(failoverSnap)

	// Check if another concurrent caller already healed the session.
	if oldSessID != "" && m.rt != nil {
		if curSess, err := m.rt.GetSession(ctx, oldSessID); err == nil && curSess != nil && isSessionHealthyActive(curSess.Status) {
			m.mu.Lock()
			mi.inst.State = models.AppStateRunning
			cp := cloneAppInstance(mi.inst)
			m.mu.Unlock()
			return cp, nil
		}
	}
	m.closeCommTunnelsForSession(oldSessID)

	var newSess *models.RuntimeSession
	restoredFromSnapshot := false
	var restoreFailureReason string

	if m.ckpt != nil {
		verifiedByPath := m.collectLatestCheckpointsByPath(targetID, oldSessID)
		if len(verifiedByPath) > 0 {
			primary := verifiedByPath[0]
			resumedRec, rErr := m.ckpt.ResumeFromCheckpoint(ctx, primary.ID, models.CheckpointResumeRequest{
				PreferredGPU: gpu,
				Strategy:     models.StrategyFreeFirst,
			})
			if rErr == nil && resumedRec != nil && resumedRec.ResumedSessionID != "" {
				if fetchedSess, gErr := m.rt.GetSession(ctx, resumedRec.ResumedSessionID); gErr == nil && fetchedSess != nil {
					newSess = fetchedSess
					allRestored := true
					for _, extra := range verifiedByPath[1:] {
						// Secondary paths share the SAME failover event (primary ResumeFromCheckpoint already counted 1 relay).
						if _, exErr := m.ckpt.RestoreSnapshotIntoSession(ctx, extra.ID, newSess.ID, false); exErr != nil {
							allRestored = false
							restoreFailureReason = checkpoint.RedactEmailStrings(fmt.Sprintf("%s: %v", extra.RemotePath, exErr))
							break
						}
					}
					if allRestored && checkpointPathsCoverDir(verifiedByPath, ckptDir) {
						restoredFromSnapshot = true
					} else if allRestored {
						restoreFailureReason = fmt.Sprintf("仅恢复了部分监听目录，未找到应用主目录 %s 快照", ckptDir)
					}
				} else {
					restoreFailureReason = "resumed session lookup failed"
				}
			} else if rErr != nil {
				restoreFailureReason = checkpoint.RedactEmailStrings(rErr.Error())
			}
		}
	}

	if newSess == nil {
		allocReq := models.SessionAllocRequest{
			Name:         fmt.Sprintf("app-%s-failover", tpl.ID),
			Accelerator:  gpu,
			Strategy:     models.StrategyFreeFirst,
			AutoFallback: !explicitGPU,
			CallerName:   "appstore-failover:" + tpl.ID,
		}
		if oldAccID != "" {
			withExcl := allocReq
			withExcl.ExcludedAccountIDs = []string{oldAccID}
			newSess, _ = m.rt.CreateSession(ctx, withExcl)
		}
		if newSess == nil {
			var cErr error
			newSess, cErr = m.rt.CreateSession(ctx, allocReq)
			if cErr != nil {
				m.mu.Lock()
				mi.inst.State = models.AppStateError
				mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(cErr.Error())
				errCopy := cloneAppInstance(mi.inst)
				m.mu.Unlock()
				m.publishInstanceEvent(errCopy)
				return nil, cErr
			}
		}
	}

	if bErr := m.runBootstrapOnSession(ctx, newSess.ID, tpl, remotePort, cleanRepo, cleanCmd, ckptDir, healthPath, modelCfg); bErr != nil {
		m.stopSessionBounded(ctx, newSess.ID)
		m.mu.Lock()
		mi.inst.State = models.AppStateError
		mi.inst.ErrorMessage = checkpoint.RedactEmailStrings(bErr.Error())
		errCopy := cloneAppInstance(mi.inst)
		m.mu.Unlock()
		m.publishInstanceEvent(errCopy)
		return nil, bErr
	}

	_, provider, freeActive, accLabel := m.resolveSessionAccountMeta(ctx, newSess)
	now := time.Now().UTC()

	var relayNoteZh string
	var statusStepEn string
	if restoredFromSnapshot {
		relayNoteZh = fmt.Sprintf("已从快照恢复并切换至 %s", accLabel)
		statusStepEn = fmt.Sprintf("Running · Restored from snapshot on %s", accLabel)
	} else {
		if restoreFailureReason != "" {
			relayNoteZh = fmt.Sprintf("已切换至新账号并全新重启（未找到/未能恢复历史快照：%s）", restoreFailureReason)
		} else {
			relayNoteZh = "已切换至新账号并全新重启（未找到/未能恢复历史快照）"
		}
		statusStepEn = fmt.Sprintf("Running · Clean restart on %s (no snapshot restored)", accLabel)
	}

	m.mu.Lock()
	if m.closed || mi.inst.State == models.AppStateStopped || mi.inst.State == models.AppStateStopping {
		m.mu.Unlock()
		m.stopSessionBounded(ctx, newSess.ID)
		return nil, errors.New("app instance stopped or manager closed during failover")
	}
	mi.inst.SessionID = newSess.ID
	mi.inst.AccountID = newSess.AccountID
	mi.inst.AccountLabel = accLabel
	mi.inst.Provider = provider
	mi.inst.AssignedGPU = newSess.Accelerator
	mi.inst.FreeTierActive = freeActive
	mi.inst.FailoverCount++
	mi.inst.LastFailoverNote = relayNoteZh
	mi.inst.State = models.AppStateRunning
	mi.inst.ProgressPct = 100
	mi.inst.ErrorMessage = ""
	mi.inst.StatusStep = statusStepEn
	mi.inst.StatusStepZh = relayNoteZh
	mi.inst.LastActiveAt = now
	mi.lastCheckpointSync = now
	mi.lastCheckpointAttempt = time.Time{}
	mi.checkpointFailCount = 0
	m.persistInstancesLocked()
	outCopy := cloneAppInstance(mi.inst)
	m.mu.Unlock()

	m.publishInstanceEvent(outCopy)
	return outCopy, nil
}

// ServeProxyHTTP serves `/apps/proxy/{id}/` and `/apps/proxy/{id}/{rest...}` requests.
// In cloudMode or non-demo protected deployments, returns HTTP 422 Unprocessable Entity JSON
// so untrusted app HTML is NEVER served on the control-plane origin.
func (m *Manager) ServeProxyHTTP(w http.ResponseWriter, r *http.Request, id string, subPath string) {
	if err := m.CheckSameOriginProxyAllowed(r); err != nil {
		writeBridgeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	targetID := strings.TrimSpace(id)
	m.mu.RLock()
	mi, ok := m.instances[targetID]
	m.mu.RUnlock()

	if !ok || mi == nil || mi.inst == nil {
		writeBridgeJSONError(w, http.StatusNotFound, "app instance not found")
		return
	}

	escapedSub := extractEscapedSubPath(r, targetID, subPath)
	m.serveInstanceRequest(w, r, mi, escapedSub, false)
}

func (m *Manager) newBridgeHTTPHandler(instanceID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.RLock()
		mi, ok := m.instances[instanceID]
		m.mu.RUnlock()
		if !ok || mi == nil {
			http.NotFound(w, r)
			return
		}
		escapedPath := "/"
		if r.URL != nil {
			if ep := r.URL.EscapedPath(); ep != "" {
				escapedPath = ep
			}
		}
		m.serveInstanceRequest(w, r, mi, escapedPath, true)
	})
}

func (m *Manager) serveInstanceRequest(
	w http.ResponseWriter,
	r *http.Request,
	mi *managedInstance,
	escapedSubPath string,
	isLocalBridge bool,
) {
	now := time.Now().UTC()
	m.mu.Lock()
	mi.inst.LastActiveAt = now
	state := mi.inst.State
	sessID := mi.inst.SessionID
	remotePort := mi.inst.RemotePort
	autoResume := mi.inst.AutoResumeEnabled
	instSnap := cloneAppInstance(mi.inst)
	tpl := mi.tpl
	demoMode := m.cfg.DemoMode
	m.mu.Unlock()

	if state == models.AppStateStopped || state == models.AppStateStopping {
		writeBridgeJSONError(w, http.StatusGone, "app instance is stopped")
		return
	}

	var sess *models.RuntimeSession
	if m.rt != nil && sessID != "" {
		s, sErr := m.rt.GetSession(r.Context(), sessID)
		if sErr != nil || s == nil || !isSessionHealthyActive(s.Status) {
			if autoResume {
				if healed, fErr := m.TriggerFailover(r.Context(), instSnap.ID); fErr == nil && healed != nil {
					instSnap = healed
					sessID = healed.SessionID
					s, _ = m.rt.GetSession(r.Context(), sessID)
				}
			}
		}
		sess = s
	}

	// Synthetic Studio preview HTML is strictly reserved for explicit DemoMode == true!
	if demoMode {
		serveInteractiveStudioPreview(w, r, instSnap, tpl, escapedSubPath)
		return
	}

	// Live mode (DemoMode == false): NEVER serve fake preview HTML.
	if sess == nil || !isSessionHealthyActive(sess.Status) || sessID == "" {
		writeBridgeJSONError(w, http.StatusBadGateway, "upstream GPU runtime session is unavailable")
		return
	}

	if escapedSubPath == "" {
		escapedSubPath = "/"
	}
	if !strings.HasPrefix(escapedSubPath, "/") {
		escapedSubPath = "/" + escapedSubPath
	}
	sanitizedQuery := ""
	if r.URL != nil && r.URL.RawQuery != "" {
		if isLocalBridge {
			// Local isolated bridge preserves app-native `?token=` (such as JupyterLab's own random token)
			// while still stripping ColabHive control-plane tokens (`ch_adm_*` / `ch_sk_*` / `key` / `sig` / `exp`).
			sanitizedQuery = SanitizeLocalBridgeRawQuery(r.URL.RawQuery)
		} else {
			sanitizedQuery = SanitizeProxyRawQuery(r.URL.RawQuery)
		}
	}

	// Isolated local Studio bridges run on their own per-instance loopback host (`127.a.b.c:<port>` or
	// `<slug>.localhost:<port>`), providing true browser-enforced origin & cookie-jar isolation with
	// native unmodified cookie names (so Jupyter `_xsrf` and app JS cookies work natively!).
	cookiePrefix := ""
	if !isLocalBridge {
		cookiePrefix = bridgeCookiePrefix(instSnap.ID)
	}

	// High-Performance Path: when direct Colab VM runtime proxy credentials can be mapped
	// explicitly to `remotePort`, stream directly via httputil.ReverseProxy (supporting WebSockets,
	// per-instance scoped app cookies, large binary assets, Range, repeated Set-Cookie, and verbatim redirects).
	if tryDirectRuntimeReverseProxy(w, r, sess, remotePort, escapedSubPath, sanitizedQuery, cookiePrefix) {
		return
	}

	// Real Google Colab Port-8080 Comm TCP Tunnel: multiplexes full-duplex HTTP/1.1, large binary
	// downloads, Range requests, and HTTP 101 WebSocket upgrades over Colab's authenticated port-8080
	// Jupyter WebSocket directly to 127.0.0.1:<remotePort> on the remote GPU VM!
	if m.tryCommTunnelReverseProxy(w, r, sess, remotePort, escapedSubPath, sanitizedQuery, cookiePrefix) {
		return
	}

	if isWebSocketUpgradeRequest(r.Header) {
		writeBridgeJSONError(w, http.StatusNotImplemented, "WebSocket upgrade requires a mapped direct runtime proxy transport")
		return
	}

	targetPath := escapedSubPath
	if sanitizedQuery != "" {
		targetPath += "?" + sanitizedQuery
	}

	env, bErr := forwardRequestViaRuntimeBridge(r.Context(), m.rt, sessID, remotePort, targetPath, r, cookiePrefix)
	if bErr != nil {
		if errors.Is(bErr, errBridgeBodyTooLarge) {
			writeBridgeJSONError(w, http.StatusRequestEntityTooLarge, bErr.Error())
			return
		}
		writeBridgeJSONError(w, http.StatusBadGateway, "upstream app bridge error: "+checkpoint.RedactEmailStrings(bErr.Error()))
		return
	}
	if wErr := writeBridgeEnvelopeResponse(w, env, cookiePrefix); wErr != nil {
		writeBridgeJSONError(w, http.StatusBadGateway, "invalid upstream bridge payload: "+wErr.Error())
		return
	}
}

// checkpointPathsCoverDir returns true when at least one restored snapshot covers `targetDir`
// (either exact match or an ancestor directory containing `targetDir`).
func checkpointPathsCoverDir(recs []*models.CheckpointRecord, targetDir string) bool {
	want := strings.TrimRight(strings.TrimSpace(targetDir), "/")
	if want == "" {
		return len(recs) > 0
	}
	for _, r := range recs {
		if r == nil {
			continue
		}
		rp := strings.TrimRight(strings.TrimSpace(r.RemotePath), "/")
		if rp == want || (rp != "" && strings.HasPrefix(want, rp+"/")) {
			return true
		}
	}
	return false
}

// collectLatestCheckpointsByPath returns the newest verified (`synced`/`resumed`) snapshot for EVERY
// distinct RemotePath belonging to `targetID` (or `oldSessID`), ordered parent-before-child (path depth
// ascending, then timestamp ascending) so restoring a parent directory (e.g. `/content`) NEVER overwrites
// newer files restored from a deeper child directory (e.g. `/content/outputs`)!
func (m *Manager) collectLatestCheckpointsByPath(targetID, oldSessID string) []*models.CheckpointRecord {
	if m.ckpt == nil {
		return nil
	}
	byPath := make(map[string]*models.CheckpointRecord)
	for _, rec := range m.ckpt.ListCheckpoints() {
		if rec == nil || (rec.Status != "synced" && rec.Status != "resumed") {
			continue
		}
		if rec.JobID != targetID && !(oldSessID != "" && (rec.SourceSessionID == oldSessID || rec.ResumedSessionID == oldSessID)) {
			continue
		}
		rp := strings.TrimRight(strings.TrimSpace(rec.RemotePath), "/")
		if rp == "" {
			continue
		}
		if _, seen := byPath[rp]; !seen {
			byPath[rp] = rec
		}
	}
	out := make([]*models.CheckpointRecord, 0, len(byPath))
	for _, rec := range byPath {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		pi := strings.TrimRight(strings.TrimSpace(out[i].RemotePath), "/")
		pj := strings.TrimRight(strings.TrimSpace(out[j].RemotePath), "/")
		di := strings.Count(pi, "/")
		dj := strings.Count(pj, "/")
		if di != dj {
			return di < dj // Parent directory first, deeper child directory second so newer child files win!
		}
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.Before(out[j].UpdatedAt)
		}
		return pi < pj
	})
	return out
}

func (m *Manager) syncInstanceCheckpoints(
	ctx context.Context,
	sessID string,
	jobID string,
	jobTitle string,
	instanceCkptDir string,
	policyWatchPaths []string,
) (synced bool, err error) {
	if m.ckpt == nil {
		return false, nil
	}

	// ALWAYS include the instance's own CheckpointDir (e.g. JupyterLab `/content`, ComfyUI `/content/ComfyUI/output`)
	// PLUS all deduplicated policy WatchPaths — NEVER skip the instance directory when another watch path succeeds!
	targetPaths := make([]string, 0, len(policyWatchPaths)+1)
	seen := make(map[string]struct{}, len(policyWatchPaths)+1)
	addPath := func(raw string) {
		p := strings.TrimSpace(raw)
		if p == "" {
			return
		}
		if _, dup := seen[p]; !dup {
			seen[p] = struct{}{}
			targetPaths = append(targetPaths, p)
		}
	}
	addPath(instanceCkptDir)
	for _, raw := range policyWatchPaths {
		addPath(raw)
	}

	var firstRealErr error
	var sawEmpty bool

	for _, wp := range targetPaths {
		_, sErr := m.ckpt.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
			SessionID:   sessID,
			JobID:       jobID,
			JobTitle:    jobTitle,
			JobKind:     "app",
			RemotePath:  wp,
			ProgressPct: 100,
		})
		if sErr == nil {
			synced = true
		} else if errors.Is(sErr, checkpoint.ErrCheckpointEmpty) {
			sawEmpty = true
		} else if firstRealErr == nil {
			firstRealErr = sErr
		}
	}

	if firstRealErr != nil {
		return false, firstRealErr
	}
	if synced {
		return true, nil
	}
	if sawEmpty {
		return false, checkpoint.ErrCheckpointEmpty
	}
	return false, errors.New("no checkpoint watch paths configured")
}

func computeCheckpointRetryBackoff(syncInterval time.Duration, failCount int, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if failCount <= 0 {
		return 0
	}
	base := 15 * time.Second
	if syncInterval > 0 && syncInterval < base {
		base = syncInterval
	}
	shift := failCount - 1
	if shift > 4 {
		shift = 4
	}
	backoff := base << shift
	capDur := syncInterval
	if capDur < 5*time.Minute {
		capDur = 5 * time.Minute
	}
	if backoff > capDur {
		backoff = capDur
	}
	return backoff
}

func (m *Manager) watchdogLoop() {
	defer m.wg.Done()

	m.mu.RLock()
	interval := m.cfg.WatchdogInterval
	m.mu.RUnlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCtx.Done():
			return
		case <-ticker.C:
			m.watchdogTick()
		}
	}
}

func (m *Manager) watchdogTick() {
	now := time.Now().UTC()

	m.mu.RLock()
	ids := make([]string, 0, len(m.instances))
	for id, mi := range m.instances {
		if mi != nil && mi.inst != nil && mi.inst.State == models.AppStateRunning {
			ids = append(ids, id)
		}
	}
	idleOverride := m.cfg.IdleTimeoutOverride
	ckptIntervalOverride := m.cfg.CheckpointSyncIntervalOverride
	m.mu.RUnlock()

	var ckptPolicy models.CheckpointPolicy
	if m.ckpt != nil {
		ckptPolicy = m.ckpt.GetPolicy()
	}

	for _, id := range ids {
		if m.stopCtx.Err() != nil {
			return
		}
		m.mu.RLock()
		mi, ok := m.instances[id]
		if !ok || mi == nil || mi.inst == nil || mi.inst.State != models.AppStateRunning {
			m.mu.RUnlock()
			continue
		}
		idleMin := mi.inst.IdleAutoStopMin
		lastActive := mi.inst.LastActiveAt
		sessID := mi.inst.SessionID
		autoResume := mi.inst.AutoResumeEnabled
		lastCkptSync := mi.lastCheckpointSync
		lastCkptAttempt := mi.lastCheckpointAttempt
		failCount := mi.checkpointFailCount
		titleZh := mi.inst.NameZh
		ckptDir := mi.checkpointDir
		if ckptDir == "" {
			ckptDir = appCheckpointRemoteDir(mi.inst.TemplateID)
		}
		m.mu.RUnlock()

		var idleLimit time.Duration
		if idleOverride > 0 {
			idleLimit = idleOverride
		} else if idleMin > 0 {
			idleLimit = time.Duration(idleMin) * time.Minute
		}
		if idleLimit > 0 && now.Sub(lastActive) >= idleLimit {
			stopped, stopErr := m.StopWithOptions(m.stopCtx, id, false)
			// Only force-stop an abandoned idle VM if there are literally zero files to back up;
			// NEVER silently swallow real sync errors (I/O, network, depth limit).
			if stopErr != nil && errors.Is(stopErr, checkpoint.ErrCheckpointEmpty) {
				stopped, stopErr = m.StopWithOptions(m.stopCtx, id, true)
			}
			if stopErr == nil && stopped != nil && stopped.State == models.AppStateStopped {
				m.mu.Lock()
				if cur, exists := m.instances[id]; exists && cur != nil && cur.inst != nil && cur.inst.State == models.AppStateStopped {
					cur.inst.StatusStep = "Auto-stopped after idle timeout to save quota"
					cur.inst.StatusStepZh = "空闲超时已自动关机省额度"
					snap := cloneAppInstance(cur.inst)
					m.mu.Unlock()
					m.publishInstanceEvent(snap)
				} else {
					m.mu.Unlock()
				}
			}
			continue
		}

		if autoResume && sessID != "" && m.rt != nil {
			sess, err := m.rt.GetSession(m.stopCtx, sessID)
			if err != nil || sess == nil || !isSessionHealthyActive(sess.Status) {
				_, _ = m.TriggerFailover(m.stopCtx, id)
				continue
			}
		}

		// Periodic checkpoint sync: honors `ckptPolicy.WatchPaths` (falling back to instance `CheckpointDir`)
		// and NEVER advances `lastCheckpointSync` on failure (uses separate `lastCheckpointAttempt` backoff).
		if m.ckpt != nil && autoResume && ckptPolicy.AutoResumeEnabled && sessID != "" {
			syncInterval := time.Duration(ckptPolicy.SyncIntervalSec) * time.Second
			if syncInterval <= 0 {
				syncInterval = 60 * time.Second
			}
			if ckptIntervalOverride > 0 {
				syncInterval = ckptIntervalOverride
			}
			retryBackoff := computeCheckpointRetryBackoff(syncInterval, failCount, ckptIntervalOverride)
			dueForSync := now.Sub(lastCkptSync) >= syncInterval
			backoffElapsed := lastCkptAttempt.IsZero() || now.Sub(lastCkptAttempt) >= retryBackoff
			if dueForSync && backoffElapsed {
				// Non-blocking TryLock prevents a long-running Stop/Restart on one instance from
				// stalling the shared watchdog loop; leaving lastCheckpointSync/Attempt untouched
				// keeps dueForSync armed so sync retries automatically on the very next tick.
				if mi.opMu.TryLock() {
					m.mu.RLock()
					stillRunning := mi.inst != nil && mi.inst.State == models.AppStateRunning && mi.inst.AutoResumeEnabled && mi.inst.SessionID == sessID
					m.mu.RUnlock()
					if stillRunning {
						syncCtx, cancel := context.WithTimeout(m.stopCtx, 30*time.Second)
						syncedOK, syncErr := m.syncInstanceCheckpoints(syncCtx, sessID, id, titleZh, ckptDir, ckptPolicy.WatchPaths)
						cancel()
						attemptTime := time.Now().UTC()
						m.mu.Lock()
						if syncedOK && syncErr == nil {
							mi.lastCheckpointSync = attemptTime
							mi.lastCheckpointAttempt = time.Time{}
							mi.checkpointFailCount = 0
						} else {
							// NEVER advance lastCheckpointSync on failed sync; arm backoff timer instead.
							mi.lastCheckpointAttempt = attemptTime
							mi.checkpointFailCount++
						}
						m.mu.Unlock()
					}
					mi.opMu.Unlock()
				}
			}
		}
	}
}

func (m *Manager) stopSessionBounded(ctx context.Context, sessionID string) error {
	if m.rt == nil || sessionID == "" {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCleanupTimeout)
	defer cancel()
	return m.rt.StopSession(stopCtx, sessionID)
}

func (m *Manager) runBootstrapOnSession(
	ctx context.Context,
	sessionID string,
	tpl models.AppTemplate,
	remotePort int,
	cleanRepo string,
	cleanCmd string,
	checkpointDir string,
	healthPath string,
	modelCfg resolvedModelConfig,
) error {
	return m.runBootstrapOnSessionForInstance(ctx, "", sessionID, tpl, remotePort, cleanRepo, cleanCmd, checkpointDir, healthPath, modelCfg)
}

func (m *Manager) runBootstrapOnSessionForInstance(
	ctx context.Context,
	instanceID string,
	sessionID string,
	tpl models.AppTemplate,
	remotePort int,
	cleanRepo string,
	cleanCmd string,
	checkpointDir string,
	healthPath string,
	modelCfg resolvedModelConfig,
) error {
	if strings.TrimSpace(tpl.BootstrapScript) == "" {
		return nil
	}
	env := map[string]string{
		"COLABHIVE_REMOTE_PORT": strconv.Itoa(remotePort),
		"COLABHIVE_TEMPLATE_ID": tpl.ID,
	}
	if tpl.MediaAPIBase != "" {
		env["COLABHIVE_LAUNCH_MODE"] = "headless"
	} else {
		env["COLABHIVE_LAUNCH_MODE"] = "native_webui"
	}
	if checkpointDir != "" {
		env["COLABHIVE_CHECKPOINT_DIR"] = checkpointDir
	}
	if healthPath != "" {
		env["COLABHIVE_HEALTH_PATH"] = healthPath
	}
	if cleanRepo != "" {
		env["COLABHIVE_CUSTOM_REPO_URL"] = cleanRepo
		env["COLABHIVE_CUSTOM_REPO_QUOTED"] = ShellQuotePOSIX(cleanRepo)
	}
	if cleanCmd != "" {
		env["COLABHIVE_CUSTOM_CMD"] = cleanCmd
	}
	if modelCfg.id != "" {
		env["COLABHIVE_MODEL_ID"] = modelCfg.id
	}
	if modelCfg.downloadURL != "" {
		env["COLABHIVE_MODEL_DOWNLOAD_URL"] = modelCfg.downloadURL
		env["COLABHIVE_MODEL_SUBDIR"] = firstStr(modelCfg.targetSubdir, "models/checkpoints")
		env["COLABHIVE_MODEL_FILENAME"] = modelCfg.filename
	}

	idleSec := 600.0
	if modelCfg.downloadURL != "" {
		idleSec = 900.0
	}
	bootID := models.NewPrefixedID("appboot_")
	launcherCode := buildDetachedAppBootstrapPython(tpl.ID, tpl.BootstrapScript, bootID)
	var (
		res *models.KernelExecResult
		err error
	)
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 1500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		res, err = m.rt.ExecuteCode(ctx, models.KernelExecRequest{
			SessionID:      sessionID,
			Code:           launcherCode,
			Env:            env,
			Silent:         false,
			StoreHistory:   false,
			IdleTimeoutSec: idleSec,
			CallerName:     "appstore-bootstrap",
		})
		if err == nil {
			break
		}
		if ctx.Err() != nil || !isTransientAppExecErr(err) {
			return fmt.Errorf("bootstrap %s: %w", tpl.ID, err)
		}
	}
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w", tpl.ID, err)
	}
	if res != nil && strings.EqualFold(res.Status, "error") {
		if res.ExecutionError != nil {
			return fmt.Errorf("bootstrap %s execution error: %s: %s", tpl.ID, res.ExecutionError.Name, res.ExecutionError.Value)
		}
		return fmt.Errorf("bootstrap %s execution failed: %s", tpl.ID, strings.TrimSpace(res.Stderr))
	}
	if res != nil && strings.Contains(res.Stdout, "COLABHIVE_APP_BG:") {
		idx := strings.Index(res.Stdout, "COLABHIVE_APP_BG:")
		rest := res.Stdout[idx+len("COLABHIVE_APP_BG:"):]
		if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
			rest = rest[:nl]
		}
		statusPath := strings.TrimSpace(rest)
		if statusPath == "" {
			statusPath = "/tmp/ch_app_boot.json"
		}
		maxWait := 12 * time.Minute
		if modelCfg.downloadURL != "" {
			maxWait = 20 * time.Minute
		}
		return m.pollDetachedAppBootstrapWithInstance(ctx, instanceID, sessionID, tpl.ID, statusPath, maxWait)
	}
	return nil
}

func sanitizeAppStoreError(err error) string {
	if err == nil {
		return ""
	}
	raw := checkpoint.RedactEmailStrings(err.Error())
	lower := strings.ToLower(raw)
	idx := strings.Index(lower, "<!doctype html")
	if idx < 0 {
		idx = strings.Index(lower, "<html")
	}
	if idx >= 0 {
		prefix := strings.TrimRight(strings.TrimSpace(raw[:idx]), ": ")
		if strings.Contains(lower, "503") {
			return prefix + " (Google Colab 后端临时 503 繁忙，当前档位资源紧张，请稍候重试或切换显卡档位)"
		}
		return prefix + " (Google Colab 后端临时返回错误页，请稍候重试)"
	}
	return raw
}

func isTransientAppExecErr(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, pat := range []string{
		"close 1006",
		"abnormal closure",
		"unexpected eof",
		"i/o timeout",
		"connection reset",
		"broken pipe",
		"http 404",
		"http 502",
		"http 503",
		"http 504",
	} {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

func buildDetachedAppBootstrapPython(templateID, innerScript, bootID string) string {
	safeID := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, strings.TrimSpace(templateID))
	if safeID == "" {
		safeID = "app"
	}
	scriptPath := "/tmp/ch_app_inner_" + safeID + ".py"
	runnerPath := "/tmp/ch_app_runner_" + safeID + ".py"
	statusPath := "/tmp/ch_app_boot_" + safeID + ".json"
	logPath := "/tmp/ch_app_boot_" + safeID + ".log"

	runnerPy := fmt.Sprintf(`import json, os, subprocess, sys
script_path = %s
status_path = %s
log_path = %s
boot_id = %s
pid = os.getpid()

def write_status(state, error=""):
    tmp = status_path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump({"boot_id": boot_id, "state": state, "pid": pid, "error": error}, fh, ensure_ascii=False)
    os.replace(tmp, status_path)

write_status("running")
try:
    with open(log_path, "wb") as lfh:
        proc = subprocess.run(
            [sys.executable, script_path],
            stdin=subprocess.DEVNULL,
            stdout=lfh,
            stderr=subprocess.STDOUT,
            check=False,
        )
    if proc.returncode != 0:
        tail = ""
        try:
            with open(log_path, "r", encoding="utf-8", errors="replace") as rfh:
                tail = rfh.read()[-600:].strip()
        except Exception:
            pass
        write_status("error", tail or ("bootstrap exited with code " + str(proc.returncode)))
    else:
        write_status("ready")
except Exception as exc:
    write_status("error", str(exc))
`, strconv.Quote(scriptPath), strconv.Quote(statusPath), strconv.Quote(logPath), strconv.Quote(bootID))

	return fmt.Sprintf(`import json, os, subprocess, sys
__inner = %s
__runner = %s
__boot_id = %s
__script_path = %s
__runner_path = %s
__status_path = %s
__already_started = False
if __boot_id and os.path.exists(__status_path):
    try:
        with open(__status_path, "r", encoding="utf-8", errors="replace") as __rfh:
            __cur = json.loads(__rfh.read().strip() or "{}")
        if __cur.get("boot_id") == __boot_id:
            __already_started = True
    except Exception:
        pass
if not __already_started:
    if os.path.exists(__status_path):
        try:
            os.remove(__status_path)
        except OSError:
            pass
    with open(__script_path, "w", encoding="utf-8") as __fh:
        __fh.write(__inner)
    with open(__runner_path, "w", encoding="utf-8") as __fh:
        __fh.write(__runner)
    __spawner = subprocess.Popen(
        [
            sys.executable,
            "-c",
            "import os, subprocess, sys; p = subprocess.Popen([sys.executable, sys.argv[1]], env=dict(os.environ), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True); print(p.pid)",
            __runner_path,
        ],
        env=dict(os.environ),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    __out, _ = __spawner.communicate(timeout=15)
    __pid = int((__out or "0").strip().splitlines()[-1] or "0") if (__out or "").strip() else 0
    try:
        with open(__status_path, "x", encoding="utf-8") as __sfh:
            json.dump({"boot_id": __boot_id, "state": "running", "pid": __pid, "error": ""}, __sfh)
    except FileExistsError:
        pass
sys.stdout.write("COLABHIVE_APP_BG:" + __status_path + "\n")
`, strconv.Quote(innerScript), strconv.Quote(runnerPy), strconv.Quote(bootID), strconv.Quote(scriptPath), strconv.Quote(runnerPath), strconv.Quote(statusPath))
}

var appBootstrapPollInterval = 3 * time.Second

func appRuntimeLogPath(templateID string) string {
	switch strings.ToLower(strings.TrimSpace(templateID)) {
	case "wangp":
		return "/content/wangp.log"
	case "comfyui":
		return "/content/comfyui.log"
	case "unsloth-studio":
		return "/content/unsloth_studio.log"
	case "jupyterlab":
		return "/content/jupyterlab_app.log"
	case "llm-chat":
		return "/content/llm_chat.log"
	default:
		return "/content/custom_app.log"
	}
}

func (m *Manager) pollDetachedAppBootstrap(
	ctx context.Context,
	sessionID string,
	templateID string,
	statusPath string,
	maxWait time.Duration,
) error {
	return m.pollDetachedAppBootstrapWithInstance(ctx, "", sessionID, templateID, statusPath, maxWait)
}

func (m *Manager) pollDetachedAppBootstrapWithInstance(
	ctx context.Context,
	instanceID string,
	sessionID string,
	templateID string,
	statusPath string,
	maxWait time.Duration,
) error {
	bootLogPath := strings.TrimSuffix(statusPath, ".json") + ".log"
	appLogPath := appRuntimeLogPath(templateID)
	pollScript := fmt.Sprintf(`import json, os, sys
p = %s
boot_log = %s
app_log = %s
def _tail_lines(path, max_bytes=12288, max_lines=20):
    try:
        if not path or not os.path.exists(path):
            return []
        with open(path, "r", encoding="utf-8", errors="replace") as lf:
            lf.seek(0, os.SEEK_END)
            sz = lf.tell()
            lf.seek(max(0, sz - max_bytes))
            lines = [ln.strip() for ln in lf.read().splitlines() if ln.strip()]
        return lines[-max_lines:]
    except Exception:
        return []

if not os.path.exists(p):
    data = {"state": "running"}
else:
    with open(p, "r", encoding="utf-8", errors="replace") as fh:
        raw = fh.read().strip()
    data = json.loads(raw) if raw else {"state": "running"}
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
data["boot_log_tail"] = _tail_lines(boot_log, 16384, 24)
data["app_log_tail"] = _tail_lines(app_log, 8192, 10)
sys.stdout.write("COLABHIVE_APP_POLL:" + json.dumps(data, ensure_ascii=False) + "\n")
`, strconv.Quote(statusPath), strconv.Quote(bootLogPath), strconv.Quote(appLogPath))

	deadline := time.Now().Add(maxWait)
	consecutiveErrs := 0
	seenLogs := make(map[string]struct{})

	applyRemoteLogs := func(bootTail, appTail []string) {
		if strings.TrimSpace(instanceID) == "" {
			return
		}
		var (
			latestStep   string
			latestStepZh string
			latestPct    int
			freshLines   []string
		)
		for _, ln := range bootTail {
			trimmed := strings.TrimSpace(ln)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "[CH_STEP]") {
				rawJSON := strings.TrimSpace(strings.TrimPrefix(trimmed, "[CH_STEP]"))
				var stepObj struct {
					Pct    int    `json:"pct"`
					Step   string `json:"step"`
					StepZh string `json:"step_zh"`
				}
				if json.Unmarshal([]byte(rawJSON), &stepObj) == nil {
					if stepObj.Pct > latestPct {
						latestPct = stepObj.Pct
					}
					if stepObj.Step != "" {
						latestStep = stepObj.Step
					}
					if stepObj.StepZh != "" {
						latestStepZh = stepObj.StepZh
					}
					msg := firstStr(stepObj.StepZh, stepObj.Step)
					if msg != "" {
						if _, dup := seenLogs["step:"+msg]; !dup {
							seenLogs["step:"+msg] = struct{}{}
							freshLines = append(freshLines, msg)
						}
					}
				}
				continue
			}
			if len(trimmed) > 220 {
				trimmed = trimmed[:220] + "..."
			}
			if _, dup := seenLogs["boot:"+trimmed]; !dup {
				seenLogs["boot:"+trimmed] = struct{}{}
				freshLines = append(freshLines, trimmed)
			}
		}
		for _, ln := range appTail {
			trimmed := strings.TrimSpace(ln)
			if trimmed == "" {
				continue
			}
			if len(trimmed) > 220 {
				trimmed = trimmed[:220] + "..."
			}
			if _, dup := seenLogs["app:"+trimmed]; !dup {
				seenLogs["app:"+trimmed] = struct{}{}
				freshLines = append(freshLines, "["+templateID+"] "+trimmed)
			}
		}
		if latestPct == 0 && latestStepZh == "" && len(freshLines) == 0 {
			return
		}

		m.mu.Lock()
		mi := m.instances[instanceID]
		if mi == nil || mi.inst == nil {
			m.mu.Unlock()
			return
		}
		changed := false
		if latestPct > mi.inst.ProgressPct && latestPct < 100 {
			mi.inst.ProgressPct = latestPct
			changed = true
		}
		if latestStep != "" && latestStep != mi.inst.StatusStep {
			mi.inst.StatusStep = checkpoint.RedactEmailStrings(latestStep)
			changed = true
		}
		if latestStepZh != "" && latestStepZh != mi.inst.StatusStepZh {
			mi.inst.StatusStepZh = checkpoint.RedactEmailStrings(latestStepZh)
			changed = true
		}
		for _, fl := range freshLines {
			appendInstanceDeployLogLocked(mi.inst, fl)
			changed = true
		}
		var snap *models.AppInstance
		if changed {
			snap = cloneAppInstance(mi.inst)
		}
		m.mu.Unlock()
		if snap != nil {
			m.publishInstanceEvent(snap)
		}
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("bootstrap %s timed out after %s", templateID, maxWait)
		}

		res, err := m.rt.ExecuteCode(ctx, models.KernelExecRequest{
			SessionID:               sessionID,
			Code:                    pollScript,
			Silent:                  false,
			StoreHistory:            false,
			IdleTimeoutSec:          30,
			CallerName:              "appstore-bootstrap-poll",
			SuppressOutputBroadcast: true,
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			consecutiveErrs++
			if consecutiveErrs >= 12 {
				return fmt.Errorf("bootstrap %s lost connection polling remote progress after %d retries: %w", templateID, consecutiveErrs, err)
			}
		} else if res != nil && strings.Contains(res.Stdout, "COLABHIVE_APP_POLL:") {
			consecutiveErrs = 0
			idx := strings.Index(res.Stdout, "COLABHIVE_APP_POLL:")
			line := res.Stdout[idx+len("COLABHIVE_APP_POLL:"):]
			if nl := strings.IndexAny(line, "\r\n"); nl >= 0 {
				line = line[:nl]
			}
			var st struct {
				State       string   `json:"state"`
				Error       string   `json:"error"`
				BootLogTail []string `json:"boot_log_tail,omitempty"`
				AppLogTail  []string `json:"app_log_tail,omitempty"`
			}
			if jErr := json.Unmarshal([]byte(strings.TrimSpace(line)), &st); jErr == nil {
				applyRemoteLogs(st.BootLogTail, st.AppLogTail)
				switch st.State {
				case "ready":
					return nil
				case "error":
					msg := strings.TrimSpace(st.Error)
					if msg == "" {
						msg = "remote app bootstrap failed"
					}
					return fmt.Errorf("bootstrap %s failed: %s", templateID, msg)
				}
			}
		} else if res != nil && strings.EqualFold(res.Status, "error") {
			consecutiveErrs++
			if consecutiveErrs >= 6 {
				return fmt.Errorf("bootstrap %s poll failed: %s", templateID, strings.TrimSpace(res.Stderr))
			}
		}

		timer := time.NewTimer(appBootstrapPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) resolveSessionAccountMeta(
	ctx context.Context,
	sess *models.RuntimeSession,
) (acc *models.Account, provider models.ProviderType, freeTier bool, label string) {
	provider = models.ProviderColab
	if sess == nil {
		return nil, provider, false, "算力池健康账号"
	}
	if m.pool != nil && sess.AccountID != "" {
		acc, _ = m.pool.GetAccount(ctx, sess.AccountID)
	}
	if acc != nil {
		provider = acc.EffectiveProvider()
		freeTier = acc.SubscriptionTier == models.TierFree
	}
	if sess.BurnRateCUHr == 0 && sess.Accelerator == models.AcceleratorT4 {
		freeTier = true
	}
	label = checkpoint.FormatSafeAccountLabel(acc, sess.AccountID)
	return acc, provider, freeTier, label
}

func (m *Manager) publishInstanceEvent(inst *models.AppInstance) {
	if m.hub == nil || inst == nil {
		return
	}
	m.hub.Publish(models.WSEvent{
		Type:      models.WSEventAppInstance,
		Timestamp: time.Now().UTC(),
		Payload:   cloneAppInstance(inst),
	})
}

func isSessionHealthyActive(st models.SessionStatus) bool {
	switch st {
	case models.SessionStopped, models.SessionLost, models.SessionError, models.SessionZombieAlert:
		return false
	default:
		return true
	}
}

func appCheckpointRemoteDir(templateID string) string {
	switch strings.ToLower(strings.TrimSpace(templateID)) {
	case "wangp":
		return "/content/WanGP/outputs"
	case "comfyui":
		return "/content/ComfyUI/output"
	case "unsloth-studio":
		return "/content/unsloth_outputs"
	case "jupyterlab":
		return "/content"
	case "llm-chat":
		return "/content/checkpoints/llm-chat"
	default:
		cleanID := strings.TrimSpace(templateID)
		if cleanID != "" && pluginIDRe.MatchString(cleanID) {
			return "/content/checkpoints/" + cleanID
		}
		return "/content/checkpoints"
	}
}

const maxAppDeployLogLines = 80

func appendInstanceDeployLogLocked(inst *models.AppInstance, msg string) {
	if inst == nil {
		return
	}
	cleaned := strings.TrimSpace(checkpoint.RedactEmailStrings(msg))
	if cleaned == "" {
		return
	}
	ts := time.Now().Format("15:04:05")
	if n := len(inst.DeployLogs); n > 0 {
		last := inst.DeployLogs[n-1]
		if idx := strings.Index(last, "] "); idx >= 0 && last[idx+2:] == cleaned {
			return
		}
	}
	inst.DeployLogs = append(inst.DeployLogs, "["+ts+"] "+cleaned)
	if len(inst.DeployLogs) > maxAppDeployLogLines {
		inst.DeployLogs = append([]string(nil), inst.DeployLogs[len(inst.DeployLogs)-maxAppDeployLogLines:]...)
	}
}

func (m *Manager) decorateLocalBridgeURL(ctx context.Context, tplID, sessionID, rawURL string) string {
	base := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if base == "" || strings.ToLower(strings.TrimSpace(tplID)) != "jupyterlab" || m.rt == nil || strings.TrimSpace(sessionID) == "" || m.cfg.DemoMode {
		return base
	}
	tCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	res, err := m.rt.ExecuteCode(tCtx, models.KernelExecRequest{
		SessionID:               sessionID,
		Code:                    `import os; p="/content/.colabhive_jupyter_token"; print("CH_JUP_TOK:" + open(p).read().strip()) if os.path.isfile(p) else None`,
		Silent:                  false,
		StoreHistory:            false,
		IdleTimeoutSec:          10,
		CallerName:              "appstore-jupyter-token",
		SuppressOutputBroadcast: true,
	})
	if err != nil || res == nil {
		return base
	}
	if idx := strings.Index(res.Stdout, "CH_JUP_TOK:"); idx >= 0 {
		tok := strings.TrimSpace(res.Stdout[idx+len("CH_JUP_TOK:"):])
		if nl := strings.IndexAny(tok, "\r\n \t"); nl >= 0 {
			tok = tok[:nl]
		}
		if tok != "" {
			return base + "/lab?token=" + url.QueryEscape(tok)
		}
	}
	return base
}

func cloneAppInstance(in *models.AppInstance) *models.AppInstance {
	if in == nil {
		return nil
	}
	cp := *in
	cp.Name = checkpoint.RedactEmailStrings(cp.Name)
	cp.NameZh = checkpoint.RedactEmailStrings(cp.NameZh)
	cp.AccountLabel = checkpoint.RedactEmailStrings(cp.AccountLabel)
	cp.LastFailoverNote = checkpoint.RedactEmailStrings(cp.LastFailoverNote)
	cp.ErrorMessage = checkpoint.RedactEmailStrings(cp.ErrorMessage)
	if len(in.Provides) > 0 {
		cp.Provides = append([]string(nil), in.Provides...)
	}
	if len(in.DeployLogs) > 0 {
		cp.DeployLogs = append([]string(nil), in.DeployLogs...)
	}
	return &cp
}

// queueRetryScheduleOverride allows unit tests to shorten polite-backoff intervals.
var queueRetryScheduleOverride []time.Duration

var defaultQueueRetrySchedule = []time.Duration{
	15 * time.Second,
	20 * time.Second,
	25 * time.Second,
	30 * time.Second,
	35 * time.Second,
	45 * time.Second,
}

func queueRetryDelayForAttempt(attempt int) time.Duration {
	sched := defaultQueueRetrySchedule
	jitterCap := 4 * time.Second
	if len(queueRetryScheduleOverride) > 0 {
		sched = queueRetryScheduleOverride
		jitterCap = 0
	}
	idx := attempt - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sched) {
		idx = len(sched) - 1
	}
	d := sched[idx]
	if jitterCap > 0 {
		jitter := time.Duration(time.Now().UnixNano() % int64(jitterCap))
		if jitter < 0 {
			jitter = -jitter
		}
		d += jitter
	}
	return d
}

// isTransientGPUShortageError returns true when CreateSession failed due to
// temporary Google Colab capacity shortage (412/503/429), per-GPU cooldown, or
// temporary concurrency saturation across pooled accounts — safe for polite waitlist retries.
func isTransientGPUShortageError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "invalid") || strings.Contains(low, "unauthorized") || strings.Contains(low, "not found") {
		return false
	}
	for _, kw := range []string{
		"no eligible pooled account",
		"http 503",
		"http 412",
		"http 429",
		"cooldown",
		"max_concurrency",
		"capacity",
		"temporarily unavailable",
		"resource exhausted",
		"繁忙",
		"缺货",
		"资源紧张",
		"请稍候重试",
	} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

func (m *Manager) allocateSessionWithWaitlist(
	ctx context.Context,
	inst *models.AppInstance,
	allocReq models.SessionAllocRequest,
	queueRetry bool,
	maxWait time.Duration,
) (*models.RuntimeSession, error) {
	if !queueRetry || maxWait <= 0 {
		return m.rt.CreateSession(ctx, allocReq)
	}

	start := time.Now()
	deadline := start.Add(maxWait)
	attempt := 0
	var lastErr error

	for {
		attempt++
		if attempt > 1 {
			m.mu.Lock()
			if inst.State == models.AppStateStarting {
				inst.StatusStep = fmt.Sprintf("GPU waitlist attempt #%d (%s)...", attempt, allocReq.Accelerator)
				inst.StatusStepZh = fmt.Sprintf("安全候补抢卡第 %d 次尝试：正在向 Google Colab 申请 %s...", attempt, allocReq.Accelerator)
				appendInstanceDeployLogLocked(inst, inst.StatusStepZh)
				snap := cloneAppInstance(inst)
				m.mu.Unlock()
				m.publishInstanceEvent(snap)
			} else {
				m.mu.Unlock()
			}
		}

		sess, err := m.rt.CreateSession(ctx, allocReq)
		if err == nil {
			if attempt > 1 {
				m.mu.Lock()
				if inst.State == models.AppStateStarting {
					msgZh := fmt.Sprintf("🎉 第 %d 次候补抢卡成功！已拿到 %s 显卡", attempt, sess.Accelerator)
					inst.StatusStep = fmt.Sprintf("GPU acquired on waitlist attempt #%d (%s)", attempt, sess.Accelerator)
					inst.StatusStepZh = msgZh
					appendInstanceDeployLogLocked(inst, msgZh)
					snap := cloneAppInstance(inst)
					m.mu.Unlock()
					m.publishInstanceEvent(snap)
				} else {
					m.mu.Unlock()
				}
			}
			return sess, nil
		}
		lastErr = err

		if ctx.Err() != nil || !isTransientGPUShortageError(err) {
			return nil, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("GPU waitlist timed out after %d attempts (%s): %w", attempt, maxWait.Round(time.Second), lastErr)
		}

		delay := queueRetryDelayForAttempt(attempt)
		if delay > remaining {
			delay = remaining
		}
		if delay <= 0 {
			return nil, fmt.Errorf("GPU waitlist timed out after %d attempts (%s): %w", attempt, maxWait.Round(time.Second), lastErr)
		}

		delaySec := int(delay.Round(time.Second) / time.Second)
		if delaySec < 1 {
			delaySec = 1
		}
		m.mu.Lock()
		if inst.State == models.AppStateStarting {
			inst.ProgressPct = 14
			inst.StatusStep = fmt.Sprintf("Waiting for %s capacity (attempt #%d · next polite retry in %ds · cancel anytime)", allocReq.Accelerator, attempt, delaySec)
			inst.StatusStepZh = fmt.Sprintf("安全候补抢卡中（第 %d 次无卡）：Google Colab 当前 %s 紧缺，%d 秒后礼貌重试 · 随时可取消", attempt, allocReq.Accelerator, delaySec)
			appendInstanceDeployLogLocked(inst, fmt.Sprintf("第 %d 次申请 %s 暂未放卡（已安全退避 %ds，防触发风控，预计总等待上限 %d 分钟）", attempt, allocReq.Accelerator, delaySec, int(maxWait/time.Minute)))
			snap := cloneAppInstance(inst)
			m.mu.Unlock()
			m.publishInstanceEvent(snap)
		} else {
			m.mu.Unlock()
		}

		// Sleep with periodic 5s countdown refresh while remaining instantly cancellable.
		sleepEnd := time.Now().Add(delay)
		timer := time.NewTimer(delay)
		var tick *time.Ticker
		if delay >= 6*time.Second {
			tick = time.NewTicker(5 * time.Second)
		}
		waitDone := false
		for !waitDone {
			if tick != nil {
				select {
				case <-ctx.Done():
					timer.Stop()
					tick.Stop()
					return nil, context.Canceled
				case <-timer.C:
					tick.Stop()
					waitDone = true
				case <-tick.C:
					remSec := int(time.Until(sleepEnd).Round(time.Second) / time.Second)
					if remSec < 1 {
						continue
					}
					m.mu.Lock()
					if inst.State == models.AppStateStarting {
						inst.StatusStep = fmt.Sprintf("Waiting for %s capacity (attempt #%d · retrying in %ds)", allocReq.Accelerator, attempt, remSec)
						inst.StatusStepZh = fmt.Sprintf("安全候补抢卡中（第 %d 次无卡）：%d 秒后自动重试 %s · 随时可取消", attempt, remSec, allocReq.Accelerator)
						snap := cloneAppInstance(inst)
						m.mu.Unlock()
						m.publishInstanceEvent(snap)
					} else {
						m.mu.Unlock()
					}
				}
			} else {
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, context.Canceled
				case <-timer.C:
					waitDone = true
				}
			}
		}
	}
}
