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
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

const (
	remoteIntrospectTimeout = 4 * time.Second
	maxIntrospectLogBytes   = 5 * 1024 * 1024
	recentLogTailLineLimit  = 25
)

type listedAssignmentWire struct {
	Accelerator      string `json:"accelerator"`
	Endpoint         string `json:"endpoint"`
	Variant          any    `json:"variant"`
	MachineShape     any    `json:"machineShape"`
	RuntimeProxyInfo *struct {
		Token                 string `json:"token"`
		TokenExpiresInSeconds any    `json:"tokenExpiresInSeconds"`
		URL                   string `json:"url"`
	} `json:"runtimeProxyInfo"`
}

type listedAssignmentsEnvelope struct {
	Assignments []listedAssignmentWire `json:"assignments"`
}

type parsedRemoteAssignment struct {
	Endpoint     string
	Accelerator  models.AcceleratorType
	Variant      models.RuntimeVariant
	Shape        models.MachineShape
	ProxyURL     string
	ProxyToken   string
	TokenTTLSecs int
}

// SyncRemoteAssignments polls `GET {tunnelDomain}/tun/m/assignments?authuser=0` across all enabled
// non-revoked accounts, adopts any externally started Colab runtimes into `RuntimeSession` (`AdoptedExternal: true`),
// refreshes live Tornado `/api/colab/resources`, `/api/sessions`, and `/content/*.log` tails without touching ZMQ,
// and transitions disappeared external sessions to `STOPPED`.
func (e *Engine) SyncRemoteAssignments(ctx context.Context) (*models.SessionSyncResult, error) {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	if e.st == nil {
		return &models.SessionSyncResult{
			Sessions: []*models.RuntimeSession{},
		}, nil
	}

	var accounts []*models.Account
	var err error
	if e.pool != nil {
		accounts, err = e.pool.ListAccounts(ctx)
	} else {
		accounts, err = e.st.ListAccounts(ctx)
	}
	if err != nil {
		return nil, err
	}

	adoptedCount := 0
	anyStateChanged := false

	for _, acc := range accounts {
		if ctx.Err() != nil {
			break
		}
		if acc == nil || !acc.Enabled || acc.Health == models.HealthRevokedByUser {
			continue
		}
		if e.isDemoSession(acc, nil) {
			continue
		}
		if e.pool == nil && (e.opts.TunnelBaseURL == "" || e.opts.TunnelBaseURL == models.ColabTunnelDomain) {
			continue
		}

		accessTok, proxyURL, cErr := e.acquireCredentials(ctx, acc)
		if cErr != nil || strings.TrimSpace(accessTok) == "" {
			continue
		}
		httpClient, _, dErr := e.buildClientAndDialer(proxyURL)
		if dErr != nil {
			continue
		}

		syncFetchStartedAt := time.Now().UTC()
		remoteList, fErr := e.fetchAccountAssignments(ctx, httpClient, accessTok)
		if fErr != nil {
			// Do not disturb existing sessions if upstream assignments poll failed transiently.
			continue
		}

		adoptedForAcc, changedForAcc := e.reconcileAccountAssignments(ctx, acc, httpClient, remoteList, syncFetchStartedAt)
		adoptedCount += adoptedForAcc
		if changedForAcc || adoptedForAcc > 0 {
			anyStateChanged = true
		}
	}

	activeSessions, err := e.listActiveSessionsSnapshot(ctx)
	if err != nil {
		return nil, err
	}

	if anyStateChanged && e.pool != nil {
		if summary, sErr := e.pool.GetQuotaSummary(ctx); sErr == nil && summary != nil {
			e.publishEvent(models.WSEventQuotaUpdated, summary)
		}
	}

	return &models.SessionSyncResult{
		Sessions:     activeSessions,
		AdoptedCount: adoptedCount,
		ActiveCount:  len(activeSessions),
	}, nil
}

func (e *Engine) listActiveSessionsSnapshot(ctx context.Context) ([]*models.RuntimeSession, error) {
	all, err := e.st.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	active := make([]*models.RuntimeSession, 0, len(all))
	for _, s := range all {
		if s == nil || s.Status == models.SessionStopped || s.Status == models.SessionLost {
			continue
		}
		s.UptimeSec = computeUptimeSec(s)
		active = append(active, s)
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].CreatedAt.After(active[j].CreatedAt)
	})
	return active, nil
}

func (e *Engine) fetchAccountAssignments(
	ctx context.Context,
	client *http.Client,
	accessToken string,
) ([]parsedRemoteAssignment, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	u := fmt.Sprintf("%s/tun/m/assignments?authuser=0", e.opts.TunnelBaseURL)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Colab-Client-Agent", "colab-cli")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET /tun/m/assignments HTTP %d", resp.StatusCode)
	}

	clean := stripXSSIPrefix(raw)
	if len(strings.TrimSpace(string(clean))) == 0 {
		return []parsedRemoteAssignment{}, nil
	}

	var env listedAssignmentsEnvelope
	if err := json.Unmarshal(clean, &env); err != nil {
		// Also tolerate bare array response if upstream ever returns `[...]` directly.
		var arr []listedAssignmentWire
		if arrErr := json.Unmarshal(clean, &arr); arrErr != nil {
			return nil, fmt.Errorf("decode /tun/m/assignments JSON: %w", err)
		}
		env.Assignments = arr
	}

	out := make([]parsedRemoteAssignment, 0, len(env.Assignments))
	for _, w := range env.Assignments {
		ep := strings.TrimSpace(w.Endpoint)
		if ep == "" {
			continue
		}
		accel, variant := parseAssignmentAcceleratorAndVariant(w.Accelerator, w.Variant, ep)
		shape := parseAssignmentMachineShape(w.MachineShape, accel)

		var proxyURL, proxyTok string
		ttlSec := 3600
		if w.RuntimeProxyInfo != nil {
			proxyURL = strings.TrimRight(strings.TrimSpace(w.RuntimeProxyInfo.URL), "/")
			proxyTok = strings.TrimSpace(w.RuntimeProxyInfo.Token)
			if parsedTTL := parseFlexiblePositiveInt(w.RuntimeProxyInfo.TokenExpiresInSeconds); parsedTTL > 0 {
				ttlSec = parsedTTL
			}
		}

		out = append(out, parsedRemoteAssignment{
			Endpoint:     ep,
			Accelerator:  accel,
			Variant:      variant,
			Shape:        shape,
			ProxyURL:     proxyURL,
			ProxyToken:   proxyTok,
			TokenTTLSecs: ttlSec,
		})
	}
	return out, nil
}

func (e *Engine) reconcileAccountAssignments(
	ctx context.Context,
	acc *models.Account,
	httpClient *http.Client,
	assignments []parsedRemoteAssignment,
	syncFetchStartedAt time.Time,
) (adoptedCount int, stateChanged bool) {
	allSessions, err := e.st.ListSessions(ctx)
	if err != nil {
		return 0, false
	}

	activeByEndpoint := make(map[string]*models.RuntimeSession)
	for _, s := range allSessions {
		if s == nil || s.Status == models.SessionStopped || s.Status == models.SessionLost {
			continue
		}
		if s.Endpoint != "" {
			activeByEndpoint[s.Endpoint] = s
		}
	}

	homogeneous := len(assignments) > 0
	if homogeneous {
		firstAcc := assignments[0].Accelerator
		for _, a := range assignments[1:] {
			if a.Accelerator != firstAcc {
				homogeneous = false
				break
			}
		}
	}

	now := time.Now().UTC()
	seenEndpoints := make(map[string]bool, len(assignments))

	for _, a := range assignments {
		seenEndpoints[a.Endpoint] = true
		sess, exists := activeByEndpoint[a.Endpoint]
		if !exists && e.isEndpointInFlight(acc.ID, a.Endpoint) {
			// Skip endpoints currently being bootstrapped by provisionOnAccount!
			continue
		}

		burnRate := e.resolveAdoptedBurnRate(ctx, acc, a.Accelerator, a.Shape, len(assignments), homogeneous)

		var tokExpPtr *time.Time
		if a.ProxyToken != "" {
			exp := now.Add(time.Duration(a.TokenTTLSecs) * time.Second)
			tokExpPtr = &exp
		}

		baseName := buildAdoptedSessionBaseName(a.Accelerator, a.Endpoint)

		if exists && sess != nil {
			probeCopy := *sess
			if a.ProxyURL != "" {
				probeCopy.ProxyURL = a.ProxyURL
			}
			if a.ProxyToken != "" {
				probeCopy.ProxyToken = a.ProxyToken
				probeCopy.ProxyTokenExpiresAt = tokExpPtr
			}
			if probeCopy.ColabWebURL == "" {
				probeCopy.ColabWebURL = models.BuildColabWebAttachURL(a.Endpoint)
			}
			if probeCopy.AdoptedExternal && homogeneous && acc.LiveBurnRateCUHr > 0 && burnRate > 0 {
				probeCopy.BurnRateCUHr = burnRate
			}
			probeCopy.KeepaliveHealthy = true
			probeCopy.LastKeepaliveAt = &now

			mem := e.getOrCreateMemState(probeCopy.ID, probeCopy.ProxyToken)
			mem.mu.Lock()
			if probeCopy.ProxyToken != "" {
				mem.proxyToken = probeCopy.ProxyToken
			}
			if probeCopy.AdoptedExternal {
				mem.preludeDone = true
			}
			proxyTok := firstNonEmpty(mem.proxyToken, probeCopy.ProxyToken)
			mem.mu.Unlock()

			if probeCopy.ProxyURL != "" && proxyTok != "" {
				e.introspectLiveRuntimeREST(ctx, httpClient, &probeCopy, mem, proxyTok, baseName)
			}

			updatedNow := time.Now().UTC()
			updatedSess, _ := e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
				if probeCopy.ProxyURL != "" {
					cur.ProxyURL = probeCopy.ProxyURL
				}
				if probeCopy.ProxyToken != "" {
					cur.ProxyToken = probeCopy.ProxyToken
					cur.ProxyTokenExpiresAt = probeCopy.ProxyTokenExpiresAt
				}
				if cur.ColabWebURL == "" {
					cur.ColabWebURL = probeCopy.ColabWebURL
				}
				if cur.AdoptedExternal && homogeneous && acc.LiveBurnRateCUHr > 0 && burnRate > 0 {
					cur.BurnRateCUHr = burnRate
				}
				if probeCopy.Resources != nil {
					cur.Resources = probeCopy.Resources
				}
				if probeCopy.KernelID != "" {
					cur.KernelID = probeCopy.KernelID
				}
				if probeCopy.JupyterSessionID != "" {
					cur.JupyterSessionID = probeCopy.JupyterSessionID
				}
				if probeCopy.NotebookName != "" {
					cur.NotebookName = probeCopy.NotebookName
				}
				if cur.AdoptedExternal && probeCopy.Name != "" {
					cur.Name = probeCopy.Name
				}
				if len(probeCopy.RecentLogs) > 0 {
					cur.RecentLogs = probeCopy.RecentLogs
				}
				if cur.Status != models.SessionZombieAlert && cur.Status != models.SessionDraining {
					// Do not downgrade an active managed ExecuteCode call that just entered BUSY!
					if !(cur.Status == models.SessionBusy && !cur.AdoptedExternal && updatedNow.Sub(cur.UpdatedAt) < 10*time.Second) {
						cur.Status = probeCopy.Status
					}
				}
				cur.KeepaliveHealthy = true
				cur.LastKeepaliveAt = &now
				cur.UptimeSec = computeUptimeSec(cur)
				cur.UpdatedAt = updatedNow
				return true
			})
			if updatedSess != nil && updatedSess.Status != models.SessionStopped && updatedSess.Status != models.SessionLost {
				e.publishEvent(models.WSEventSessionUpdated, updatedSess)
			}
			continue
		}

		sess = &models.RuntimeSession{
			ID:                   models.NewPrefixedID("sess_"),
			Name:                 baseName,
			AccountID:            acc.ID,
			AccountEmail:         acc.Email,
			Backend:              models.BackendTunnelAPI,
			Endpoint:             a.Endpoint,
			ProxyURL:             a.ProxyURL,
			ProxyToken:           a.ProxyToken,
			ProxyTokenExpiresAt:  tokExpPtr,
			Variant:              a.Variant,
			RequestedAccelerator: a.Accelerator,
			Accelerator:          a.Accelerator,
			Shape:                a.Shape,
			Status:               models.SessionIdle,
			BurnRateCUHr:         burnRate,
			ColabWebURL:          models.BuildColabWebAttachURL(a.Endpoint),
			AdoptedExternal:      true,
			KeepaliveHealthy:     true,
			LastKeepaliveAt:      &now,
			Timeline: []models.ExecutionStep{
				{
					ID:          "step_sync_adopt",
					Label:       fmt.Sprintf("Synced External %s Runtime (%s)", a.Accelerator, acc.Email),
					Status:      "completed",
					DurationMs:  1,
					StartedAt:   now,
					CompletedAt: &now,
					Detail:      a.Endpoint,
				},
			},
			Cells:     []models.NotebookCell{},
			CreatedAt: now,
			UpdatedAt: now,
		}
		adoptedCount++
		stateChanged = true

		mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)
		mem.mu.Lock()
		if sess.ProxyToken != "" {
			mem.proxyToken = sess.ProxyToken
		}
		mem.preludeDone = true
		proxyTok := firstNonEmpty(mem.proxyToken, sess.ProxyToken)
		mem.mu.Unlock()

		if sess.ProxyURL != "" && proxyTok != "" {
			e.introspectLiveRuntimeREST(ctx, httpClient, sess, mem, proxyTok, baseName)
		}

		sess.UptimeSec = computeUptimeSec(sess)
		sess.UpdatedAt = time.Now().UTC()
		_ = e.st.SaveSession(ctx, sess)
		e.publishEvent(models.WSEventSessionUpdated, sess)
	}

	// Transition any active sessions on this account whose Endpoint disappeared from `/tun/m/assignments`.
	for _, s := range allSessions {
		if s == nil || s.AccountID != acc.ID {
			continue
		}
		if s.Status == models.SessionStopped || s.Status == models.SessionLost {
			continue
		}
		if e.isDemoSession(acc, s) || s.Endpoint == "" || seenEndpoints[s.Endpoint] {
			continue
		}
		// Skip endpoints currently in-flight OR sessions saved after our `/tun/m/assignments` snapshot started!
		if e.isEndpointInFlight(acc.ID, s.Endpoint) || (!syncFetchStartedAt.IsZero() && !s.CreatedAt.Before(syncFetchStartedAt)) {
			continue
		}

		stopNow := time.Now().UTC()
		transitioned := false
		_, _ = e.mutateSessionState(ctx, s, false, func(cur *models.RuntimeSession) bool {
			if cur.AdoptedExternal {
				cur.Status = models.SessionStopped
				cur.StatusMessage = "External runtime unassigned upstream"
			} else {
				cur.Status = models.SessionLost
				cur.StatusMessage = "VM idle-pruned or preempted upstream"
			}
			cur.StoppedAt = &stopNow
			cur.UpdatedAt = stopNow
			cur.UptimeSec = computeUptimeSec(cur)
			transitioned = true
			return true
		})
		if transitioned {
			e.deleteMemState(s.ID)
			if s.StickyKey != "" && e.rtr != nil {
				e.unbindStickyForCaller(ctx, s.StickyKey, s.APIKeyID)
			}
			e.publishEvent(models.WSEventSessionUpdated, s)
			stateChanged = true
		}
	}

	upstreamCount := len(assignments)
	e.syncAccountSessionRollupWithUpstream(ctx, acc.ID, &upstreamCount, time.Now().UTC())
	return adoptedCount, stateChanged
}

func (e *Engine) resolveAdoptedBurnRate(
	ctx context.Context,
	acc *models.Account,
	accel models.AcceleratorType,
	shape models.MachineShape,
	totalAssignments int,
	homogeneous bool,
) float64 {
	if homogeneous && totalAssignments > 0 && acc != nil && acc.LiveBurnRateCUHr > 0 {
		return math.Round((acc.LiveBurnRateCUHr/float64(totalAssignments))*100) / 100
	}
	if e.st != nil {
		if cal, ok, _ := e.st.GetCalibratedBurnRate(ctx, accel, shape); ok && cal > 0 {
			return math.Round(cal*100) / 100
		}
	}
	if spec, ok := models.GetHardwareSpec(accel); ok {
		return spec.DefaultBurnRateCUHr
	}
	return 0
}

// introspectLiveRuntimeREST performs non-intrusive Tornado REST introspection (`4s` timeout per request,
// ZERO ZMQ WebSocket connections) to populate hardware telemetry, active kernel/notebook metadata,
// `/content` task naming, and live `.log` tail lines.
func (e *Engine) introspectLiveRuntimeREST(
	ctx context.Context,
	client *http.Client,
	sess *models.RuntimeSession,
	mem *sessionMemState,
	proxyToken string,
	baseAdoptedName string,
) {
	// A. Hardware Telemetry: GET {ProxyURL}/api/colab/resources (4s timeout)
	if res, err := e.fetchVMResourcesWithTimeout(ctx, client, sess.ProxyURL, proxyToken, remoteIntrospectTimeout); err == nil && res != nil {
		sess.Resources = res
	}

	gpuUtilPct := maxGPUUtilizationPct(sess.Resources)

	// B. Jupyter Sessions & Kernels: GET {ProxyURL}/api/sessions (fallback GET /api/kernels)
	kernelID, jupSessID, execState, nbName := e.probeJupyterSessionsAndKernels(ctx, client, sess.ProxyURL, proxyToken, sess.KernelID)
	if kernelID != "" {
		sess.KernelID = kernelID
	}
	if jupSessID != "" {
		sess.JupyterSessionID = jupSessID
	}
	if nbName != "" && !strings.EqualFold(nbName, "empty.ipynb") {
		sess.NotebookName = nbName
	}

	hasActiveManagedWorker := !sess.AdoptedExternal && sess.Worker != nil && sess.Worker.Alive && !sess.Worker.ZombieDetected
	if strings.EqualFold(execState, "busy") || gpuUtilPct > 5.0 || hasActiveManagedWorker {
		sess.Status = models.SessionBusy
	} else if sess.Status != models.SessionZombieAlert && sess.Status != models.SessionDraining {
		sess.Status = models.SessionIdle
	}

	// C. `/content` Active Task & Log Tail Discovery: GET {ProxyURL}/api/contents[/content]
	entries, listErr := e.probeContentDirectoryEntries(ctx, client, sess.ProxyURL, proxyToken)
	if listErr != nil {
		return
	}

	hint, discoveredNB, chosenLog := analyzeContentEntries(entries)
	if sess.NotebookName == "" && discoveredNB != "" {
		sess.NotebookName = discoveredNB
	}
	if sess.AdoptedExternal {
		if hint == "" && sess.NotebookName != "" {
			hint = path.Base(sess.NotebookName)
		}
		if hint != "" && (sess.Name == "" || sess.Name == baseAdoptedName) {
			sess.Name = fmt.Sprintf("%s (%s)", baseAdoptedName, hint)
		} else if sess.Name == "" {
			sess.Name = baseAdoptedName
		}
	}

	if chosenLog != nil {
		if rawLog, fErr := e.fetchRemoteLogContent(ctx, client, sess.ProxyURL, proxyToken, chosenLog.Path, chosenLog.Name); fErr == nil && len(rawLog) > 0 {
			tailLines := extractTailLines(string(rawLog), recentLogTailLineLimit)
			if len(tailLines) > 0 {
				sess.RecentLogs = tailLines
			}
			e.publishLogDeltaIfNew(sess, mem, chosenLog.Path, rawLog, tailLines)
		}
	}
}

func (e *Engine) probeJupyterSessionsAndKernels(
	ctx context.Context,
	client *http.Client,
	proxyBaseURL string,
	proxyToken string,
	currentKernelID string,
) (kernelID string, jupyterSessionID string, executionState string, notebookName string) {
	base := strings.TrimRight(proxyBaseURL, "/")

	// 1. Try GET /api/sessions
	if rawSess, err := doShortProxyGET(ctx, client, base+"/api/sessions", proxyToken, remoteIntrospectTimeout, 1<<20); err == nil {
		type sessionWire struct {
			ID       string `json:"id"`
			Path     string `json:"path"`
			Name     string `json:"name"`
			Notebook struct {
				Path string `json:"path"`
				Name string `json:"name"`
			} `json:"notebook"`
			Kernel struct {
				ID             string `json:"id"`
				Name           string `json:"name"`
				ExecutionState string `json:"execution_state"`
			} `json:"kernel"`
		}
		var list []sessionWire
		if json.Unmarshal(stripXSSIPrefix(rawSess), &list) == nil && len(list) > 0 {
			bestIdx := -1
			for i, item := range list {
				if strings.TrimSpace(item.Kernel.ID) == "" {
					continue
				}
				if strings.EqualFold(strings.TrimSpace(item.Kernel.ExecutionState), "busy") {
					bestIdx = i
					break
				}
				if currentKernelID != "" && item.Kernel.ID == currentKernelID {
					bestIdx = i
				} else if bestIdx == -1 {
					bestIdx = i
				}
			}
			if bestIdx >= 0 {
				picked := list[bestIdx]
				nb := cleanNotebookLabel(firstNonEmpty(picked.Name, picked.Notebook.Name, picked.Path, picked.Notebook.Path))
				return strings.TrimSpace(picked.Kernel.ID),
					strings.TrimSpace(picked.ID),
					strings.ToLower(strings.TrimSpace(picked.Kernel.ExecutionState)),
					nb
			}
		}
	}

	// 2. Fallback GET /api/kernels
	if rawKern, err := doShortProxyGET(ctx, client, base+"/api/kernels", proxyToken, remoteIntrospectTimeout, 1<<20); err == nil {
		type kernelWire struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			ExecutionState string `json:"execution_state"`
		}
		var klist []kernelWire
		if json.Unmarshal(stripXSSIPrefix(rawKern), &klist) == nil && len(klist) > 0 {
			bestIdx := -1
			for i, k := range klist {
				if strings.TrimSpace(k.ID) == "" {
					continue
				}
				if strings.EqualFold(strings.TrimSpace(k.ExecutionState), "busy") {
					bestIdx = i
					break
				}
				if currentKernelID != "" && k.ID == currentKernelID {
					bestIdx = i
				} else if bestIdx == -1 {
					bestIdx = i
				}
			}
			if bestIdx >= 0 {
				picked := klist[bestIdx]
				return strings.TrimSpace(picked.ID), "", strings.ToLower(strings.TrimSpace(picked.ExecutionState)), ""
			}
		}
	}

	return "", "", "", ""
}

func (e *Engine) probeContentDirectoryEntries(
	ctx context.Context,
	client *http.Client,
	proxyBaseURL string,
	proxyToken string,
) ([]models.RemoteFileEntry, error) {
	base := strings.TrimRight(proxyBaseURL, "/")
	q := fmt.Sprintf("authuser=0&colab-runtime-proxy-token=%s", url.QueryEscape(proxyToken))

	// Real Colab Tornado mounts filesystem `/` as ContentsManager root (`/api/contents/content` == `/content`),
	// while `mockcolab` maps both `/api/contents/content` and `/api/contents` to `/content`.
	primaryURL := fmt.Sprintf("%s/api/contents/content?%s", base, q)
	if entries, err := fetchContentsListingURL(ctx, client, primaryURL, proxyToken); err == nil {
		return entries, nil
	}

	fallbackURL := fmt.Sprintf("%s/api/contents?%s", base, q)
	return fetchContentsListingURL(ctx, client, fallbackURL, proxyToken)
}

func fetchContentsListingURL(
	ctx context.Context,
	client *http.Client,
	targetURL string,
	proxyToken string,
) ([]models.RemoteFileEntry, error) {
	raw, err := doShortProxyGET(ctx, client, targetURL, proxyToken, remoteIntrospectTimeout, 2<<20)
	if err != nil {
		return nil, err
	}

	var dirModel struct {
		Type    string `json:"type"`
		Content []struct {
			Name         string     `json:"name"`
			Path         string     `json:"path"`
			Type         string     `json:"type"`
			Size         int64      `json:"size"`
			LastModified *time.Time `json:"last_modified"`
		} `json:"content"`
	}
	if err := json.Unmarshal(stripXSSIPrefix(raw), &dirModel); err != nil {
		return nil, err
	}

	entries := make([]models.RemoteFileEntry, 0, len(dirModel.Content))
	for _, item := range dirModel.Content {
		t := item.Type
		if t != "directory" {
			t = "file"
		}
		p := "/" + strings.TrimPrefix(item.Path, "/")
		entries = append(entries, models.RemoteFileEntry{
			Name:         item.Name,
			Path:         p,
			Type:         t,
			SizeBytes:    item.Size,
			LastModified: item.LastModified,
		})
	}
	return entries, nil
}

func (e *Engine) fetchRemoteLogContent(
	ctx context.Context,
	client *http.Client,
	proxyBaseURL string,
	proxyToken string,
	entryPath string,
	entryName string,
) ([]byte, error) {
	base := strings.TrimRight(proxyBaseURL, "/")
	rel := normalizeRemoteContentsPath(entryPath)
	candidates := []string{
		fmt.Sprintf("%s/api/contents/%s?content=1&authuser=0&colab-runtime-proxy-token=%s", base, escapeRemoteContentsURLPath(rel), url.QueryEscape(proxyToken)),
	}
	if entryName != "" && rel != entryName {
		candidates = append(candidates, fmt.Sprintf("%s/api/contents/%s?content=1&authuser=0&colab-runtime-proxy-token=%s", base, url.PathEscape(entryName), url.QueryEscape(proxyToken)))
	}

	var lastErr error
	for _, u := range candidates {
		raw, err := doShortProxyGET(ctx, client, u, proxyToken, remoteIntrospectTimeout, 8<<20)
		if err != nil {
			lastErr = err
			continue
		}
		var fileModel struct {
			Type    string `json:"type"`
			Format  string `json:"format"`
			Content any    `json:"content"`
		}
		if jErr := json.Unmarshal(stripXSSIPrefix(raw), &fileModel); jErr != nil {
			lastErr = jErr
			continue
		}
		if fileModel.Type == "directory" {
			lastErr = fmt.Errorf("%s is a directory", entryPath)
			continue
		}
		switch strings.ToLower(fileModel.Format) {
		case "base64":
			s, _ := fileModel.Content.(string)
			clean := strings.TrimSpace(s)
			decoded, dErr := base64.StdEncoding.DecodeString(clean)
			if dErr != nil {
				decoded, dErr = base64.RawStdEncoding.DecodeString(clean)
			}
			if dErr != nil {
				lastErr = dErr
				continue
			}
			if len(decoded) > maxIntrospectLogBytes {
				decoded = decoded[len(decoded)-maxIntrospectLogBytes:]
			}
			return decoded, nil
		default:
			if s, ok := fileModel.Content.(string); ok {
				b := []byte(s)
				if len(b) > maxIntrospectLogBytes {
					b = b[len(b)-maxIntrospectLogBytes:]
				}
				return b, nil
			}
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("empty log content for %s", entryPath)
}

func (e *Engine) publishLogDeltaIfNew(
	sess *models.RuntimeSession,
	mem *sessionMemState,
	logPath string,
	rawBytes []byte,
	tailLines []string,
) {
	if sess == nil || mem == nil || len(rawBytes) == 0 {
		return
	}

	var delta string
	mem.mu.Lock()
	switch {
	case mem.lastLogFile == "" && mem.lastLogByteLen == 0:
		if len(tailLines) > 0 {
			delta = strings.Join(tailLines, "\n") + "\n"
		}
		mem.lastLogFile = logPath
		mem.lastLogByteLen = len(rawBytes)

	case mem.lastLogFile == logPath && len(rawBytes) > mem.lastLogByteLen:
		appended := string(rawBytes[mem.lastLogByteLen:])
		appendedLines := extractTailLines(appended, recentLogTailLineLimit)
		if len(appendedLines) > 0 {
			delta = strings.Join(appendedLines, "\n") + "\n"
		}
		mem.lastLogByteLen = len(rawBytes)

	case mem.lastLogFile != logPath || len(rawBytes) < mem.lastLogByteLen:
		if len(tailLines) > 0 {
			delta = strings.Join(tailLines, "\n") + "\n"
		}
		mem.lastLogFile = logPath
		mem.lastLogByteLen = len(rawBytes)
	}
	mem.mu.Unlock()

	if delta != "" {
		e.publishStreamChunk(sess, "stdout", delta, nil)
	}
}

func analyzeContentEntries(entries []models.RemoteFileEntry) (taskHint string, notebookName string, bestLog *models.RemoteFileEntry) {
	var scriptCand string
	var jsonlCand string
	var logCandidates []models.RemoteFileEntry

	for _, e := range entries {
		if e.Type == "directory" {
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, ".ipynb"):
			if lower != "empty.ipynb" {
				if notebookName == "" {
					notebookName = name
				}
				if scriptCand == "" {
					scriptCand = name
				}
			}
		case strings.HasSuffix(lower, ".py"):
			if scriptCand == "" {
				scriptCand = name
			}
		case strings.HasSuffix(lower, ".jsonl"):
			if jsonlCand == "" || strings.HasPrefix(lower, "shard_") {
				jsonlCand = name
			}
		case strings.HasSuffix(lower, ".log"):
			if e.SizeBytes <= maxIntrospectLogBytes {
				logCandidates = append(logCandidates, e)
			}
		}
	}

	if len(logCandidates) > 0 {
		sort.Slice(logCandidates, func(i, j int) bool {
			ri := logFilePriority(logCandidates[i].Name)
			rj := logFilePriority(logCandidates[j].Name)
			if ri != rj {
				return ri < rj
			}
			ti := logCandidates[i].LastModified
			tj := logCandidates[j].LastModified
			if ti != nil && tj != nil && !ti.Equal(*tj) {
				return ti.After(*tj)
			}
			if ti != nil && tj == nil {
				return true
			}
			if ti == nil && tj != nil {
				return false
			}
			return logCandidates[i].Name < logCandidates[j].Name
		})
		picked := logCandidates[0]
		bestLog = &picked
	}

	switch {
	case scriptCand != "":
		taskHint = scriptCand
	case jsonlCand != "":
		taskHint = jsonlCand
	case bestLog != nil:
		taskHint = bestLog.Name
	}
	return taskHint, notebookName, bestLog
}

func logFilePriority(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "worker.log":
		return 0
	case "train.log", "eval.log", "run.log":
		return 1
	default:
		return 2
	}
}

func extractTailLines(raw string, maxLines int) []string {
	if maxLines <= 0 {
		maxLines = recentLogTailLineLimit
	}
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if strings.TrimSpace(normalized) == "" {
		return nil
	}
	lines := strings.Split(normalized, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l)
	}
	return out
}

func doShortProxyGET(
	ctx context.Context,
	client *http.Client,
	targetURL string,
	proxyToken string,
	timeout time.Duration,
	maxBytes int64,
) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	setProxyHeaders(req, proxyToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(stripXSSIPrefix(raw))),
		}
	}
	return raw, nil
}

func maxGPUUtilizationPct(res *models.VMResourceTelemetry) float64 {
	if res == nil {
		return 0
	}
	maxPct := 0.0
	for _, g := range res.GPUs {
		u := g.GPUUtilization
		if u > 0 && u <= 1.0 {
			u *= 100.0
		}
		if u > maxPct {
			maxPct = u
		}
	}
	return maxPct
}

func cleanNotebookLabel(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "/content/")
	s = strings.TrimPrefix(s, "content/")
	s = strings.TrimPrefix(s, "/")
	return s
}

func buildAdoptedSessionBaseName(accel models.AcceleratorType, endpoint string) string {
	accLower := strings.ToLower(string(accel))
	if accLower == "" {
		accLower = "cpu"
	}
	short := shortEndpointSlug(endpoint, accLower)
	return fmt.Sprintf("colab-%s-%s", accLower, short)
}

func shortEndpointSlug(endpoint string, accLower string) string {
	ep := strings.TrimSpace(endpoint)
	lower := strings.ToLower(ep)
	for _, pfx := range []string{
		"gpu-" + accLower + "-s-",
		"tpu-" + accLower + "-s-",
		"gpu-s-",
		"tpu-s-",
		"rn-",
	} {
		if strings.HasPrefix(lower, pfx) && len(ep) > len(pfx) {
			ep = ep[len(pfx):]
			break
		}
	}
	ep = strings.Trim(ep, "-_")
	var clean strings.Builder
	for _, ch := range ep {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			clean.WriteRune(ch)
			if clean.Len() >= 8 {
				break
			}
		}
	}
	if clean.Len() == 0 {
		return "vm"
	}
	return strings.ToLower(clean.String())
}

func parseAssignmentAcceleratorAndVariant(
	rawAcc string,
	rawVariant any,
	endpoint string,
) (models.AcceleratorType, models.RuntimeVariant) {
	trimmed := strings.TrimSpace(rawAcc)
	accel := models.AcceleratorCPU
	if norm, err := models.NormalizeAccelerator(trimmed); err == nil {
		accel = norm
	} else if trimmed != "" {
		// Try stripping prefixes like "NVIDIA_" or "ACCELERATOR_"
		upper := strings.ToUpper(trimmed)
		for _, cand := range models.AllAccelerators {
			if strings.Contains(upper, string(cand)) {
				accel = cand
				break
			}
		}
	}

	// Infer accelerator from endpoint slug (e.g. "gpu-g4-s-...") if wire accelerator was empty.
	if accel == models.AcceleratorCPU && trimmed == "" {
		epParts := strings.Split(strings.ToLower(endpoint), "-")
		if len(epParts) >= 2 && (epParts[0] == "gpu" || epParts[0] == "tpu") {
			if norm, err := models.NormalizeAccelerator(epParts[1]); err == nil {
				accel = norm
			}
		}
	}

	if spec, ok := models.GetHardwareSpec(accel); ok && accel != models.AcceleratorCPU {
		return accel, spec.Variant
	}

	switch v := rawVariant.(type) {
	case float64:
		return accel, models.VariantFromWireCode(int(v))
	case int:
		return accel, models.VariantFromWireCode(v)
	case json.Number:
		i, _ := v.Int64()
		return accel, models.VariantFromWireCode(int(i))
	case string:
		switch strings.ToUpper(strings.TrimSpace(v)) {
		case "1", "GPU", "VARIANT_GPU":
			return accel, models.VariantGPU
		case "2", "TPU", "VARIANT_TPU":
			return accel, models.VariantTPU
		default:
			return accel, models.VariantDefault
		}
	default:
		return accel, models.VariantDefault
	}
}

func parseAssignmentMachineShape(rawShape any, accel models.AcceleratorType) models.MachineShape {
	if spec, ok := models.GetHardwareSpec(accel); ok && spec.AlwaysHighMem {
		return models.ShapeHighRAM
	}
	switch v := rawShape.(type) {
	case float64:
		return models.ShapeFromWireCode(int(v))
	case int:
		return models.ShapeFromWireCode(v)
	case json.Number:
		i, _ := v.Int64()
		return models.ShapeFromWireCode(int(i))
	case string:
		switch strings.ToUpper(strings.TrimSpace(v)) {
		case "1", "HM", "HIGH_RAM", "HIGHMEM", "SHAPE_HIGHMEM":
			return models.ShapeHighRAM
		default:
			return models.ShapeStandard
		}
	default:
		return models.ShapeStandard
	}
}

func parseFlexiblePositiveInt(raw any) int {
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case string:
		s := strings.TrimSuffix(strings.TrimSpace(v), "s")
		n, _ := strconv.Atoi(s)
		return n
	default:
		return 0
	}
}
