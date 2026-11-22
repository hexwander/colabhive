package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/colabhive/colabhive/internal/models"
)

// ============================================================================
// Jupyter REST Bootstrap & Proxy Token Refresh
// ============================================================================

func setProxyHeaders(req *http.Request, proxyToken string) {
	req.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
	req.Header.Set("Authorization", "Bearer "+proxyToken)
	req.Header.Set("X-Colab-Client-Agent", "colab-cli")
	req.Header.Set("Accept", "application/json")
}

func (e *Engine) bootstrapJupyterKernel(
	ctx context.Context,
	client *http.Client,
	proxyBaseURL string,
	proxyToken string,
) (kernelID string, jupyterSessionID string, err error) {
	// Prefer POST /api/sessions (`empty.ipynb`) so `colab url` Web UI attaches to the EXACT same live kernel!
	sessPayload := map[string]any{
		"path": "empty.ipynb",
		"name": "",
		"type": "notebook",
		"kernel": map[string]any{
			"name": "python3",
		},
	}
	bodyBytes, _ := json.Marshal(sessPayload)

	var lastErr error
	backoffs := []time.Duration{0, 150 * time.Millisecond, 400 * time.Millisecond}
	for _, wait := range backoffs {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(wait):
			}
		}

		reqSess, rErr := http.NewRequestWithContext(ctx, http.MethodPost, proxyBaseURL+"/api/sessions", bytes.NewReader(bodyBytes))
		if rErr != nil {
			return "", "", rErr
		}
		setProxyHeaders(reqSess, proxyToken)
		reqSess.Header.Set("Content-Type", "application/json")

		resp, doErr := client.Do(reqSess)
		if doErr == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var parsed struct {
					ID     string `json:"id"`
					Kernel struct {
						ID string `json:"id"`
					} `json:"kernel"`
				}
				if jErr := json.Unmarshal(stripXSSIPrefix(raw), &parsed); jErr == nil && parsed.Kernel.ID != "" {
					return parsed.Kernel.ID, parsed.ID, nil
				}
			}
			lastErr = fmt.Errorf("POST /api/sessions HTTP %d", resp.StatusCode)
		} else {
			lastErr = doErr
		}

		// Fallback to direct POST /api/kernels
		kernBytes, _ := json.Marshal(map[string]any{"name": "python3"})
		reqKern, kErr := http.NewRequestWithContext(ctx, http.MethodPost, proxyBaseURL+"/api/kernels", bytes.NewReader(kernBytes))
		if kErr != nil {
			return "", "", kErr
		}
		setProxyHeaders(reqKern, proxyToken)
		reqKern.Header.Set("Content-Type", "application/json")

		respK, doErrK := client.Do(reqKern)
		if doErrK == nil {
			rawK, _ := io.ReadAll(io.LimitReader(respK.Body, 1<<20))
			_ = respK.Body.Close()
			if respK.StatusCode >= 200 && respK.StatusCode < 300 {
				var parsedK struct {
					ID string `json:"id"`
				}
				if jErr := json.Unmarshal(stripXSSIPrefix(rawK), &parsedK); jErr == nil && parsedK.ID != "" {
					return parsedK.ID, "", nil
				}
			}
			lastErr = fmt.Errorf("POST /api/kernels HTTP %d", respK.StatusCode)
		} else {
			lastErr = doErrK
		}
	}
	return "", "", lastErr
}

func (e *Engine) postJupyterKernelAction(
	ctx context.Context,
	client *http.Client,
	proxyBaseURL string,
	proxyToken string,
	kernelID string,
	action string,
) error {
	u := fmt.Sprintf("%s/api/kernels/%s/%s", strings.TrimRight(proxyBaseURL, "/"), url.PathEscape(kernelID), action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	setProxyHeaders(req, proxyToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("POST /api/kernels/%s/%s HTTP %d", kernelID, action, resp.StatusCode)
}

func (e *Engine) deleteJupyterKernel(
	ctx context.Context,
	client *http.Client,
	proxyBaseURL string,
	proxyToken string,
	kernelID string,
) error {
	ctxShort, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	u := fmt.Sprintf("%s/api/kernels/%s", strings.TrimRight(proxyBaseURL, "/"), url.PathEscape(kernelID))
	req, err := http.NewRequestWithContext(ctxShort, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	setProxyHeaders(req, proxyToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return nil
}

// prepareSessionProxyClient ensures `ProxyToken` has >5 minutes of remaining TTL (refreshing via
// `GET /tun/m/assignments?authuser=0` if nearing expiry) and returns the account-pinned HTTP & WS clients.
func (e *Engine) prepareSessionProxyClient(
	ctx context.Context,
	acc *models.Account,
	sess *models.RuntimeSession,
	mem *sessionMemState,
) (proxyToken string, httpClient *http.Client, wsDialer *websocket.Dialer, err error) {
	accessTok, proxyURL, cErr := e.acquireCredentials(ctx, acc)
	if cErr != nil {
		return "", nil, nil, cErr
	}
	httpClient, wsDialer, err = e.buildClientAndDialer(proxyURL)
	if err != nil {
		return "", nil, nil, err
	}

	mem.mu.Lock()
	tok := firstNonEmpty(mem.proxyToken, sess.ProxyToken)
	mem.mu.Unlock()

	needsRefresh := tok == "" || (sess.ProxyTokenExpiresAt != nil && time.Until(*sess.ProxyTokenExpiresAt) <= 5*time.Minute)
	if needsRefresh && sess.Endpoint != "" {
		refreshedTok, refreshedURL, expAt, rErr := e.refreshEndpointToken(ctx, httpClient, accessTok, sess.Endpoint)
		if rErr == nil && refreshedTok != "" {
			tok = refreshedTok
			mem.mu.Lock()
			mem.proxyToken = refreshedTok
			mem.mu.Unlock()
			_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
				cur.ProxyToken = refreshedTok
				if refreshedURL != "" {
					cur.ProxyURL = strings.TrimRight(refreshedURL, "/")
				}
				cur.ProxyTokenExpiresAt = &expAt
				return true
			})
		} else if rErr != nil {
			tokenExpired := tok == "" || (sess.ProxyTokenExpiresAt != nil && time.Until(*sess.ProxyTokenExpiresAt) <= 0)
			if strings.Contains(strings.ToLower(rErr.Error()), "no longer present") || tokenExpired || isAuthFailureErr(rErr) {
				return "", nil, nil, rErr
			}
		}
	}

	return tok, httpClient, wsDialer, nil
}

func (e *Engine) forceRefreshSessionProxyToken(
	ctx context.Context,
	acc *models.Account,
	sess *models.RuntimeSession,
	mem *sessionMemState,
) (proxyToken string, wsDialer *websocket.Dialer, err error) {
	if sess == nil || sess.Endpoint == "" {
		return "", nil, errors.New("session missing endpoint for proxy token refresh")
	}
	accessTok, proxyURL, cErr := e.acquireCredentials(ctx, acc)
	if cErr != nil {
		return "", nil, cErr
	}
	httpClient, wsDialer, dErr := e.buildClientAndDialer(proxyURL)
	if dErr != nil {
		return "", nil, dErr
	}
	refreshedTok, refreshedURL, expAt, rErr := e.refreshEndpointToken(ctx, httpClient, accessTok, sess.Endpoint)
	if rErr != nil {
		return "", nil, rErr
	}
	mem.mu.Lock()
	mem.proxyToken = refreshedTok
	mem.mu.Unlock()
	_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
		cur.ProxyToken = refreshedTok
		if refreshedURL != "" {
			cur.ProxyURL = strings.TrimRight(refreshedURL, "/")
		}
		cur.ProxyTokenExpiresAt = &expAt
		return true
	})
	return refreshedTok, wsDialer, nil
}

func (e *Engine) confirmEndpointGoneBestEffort(ctx context.Context, acc *models.Account, sess *models.RuntimeSession) bool {
	if sess == nil || sess.Endpoint == "" || e.isDemoSession(acc, sess) {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	accessTok, proxyURL, cErr := e.acquireCredentials(probeCtx, acc)
	if cErr != nil {
		return false
	}
	httpClient, _, dErr := e.buildClientAndDialer(proxyURL)
	if dErr != nil {
		return false
	}
	_, _, _, rErr := e.refreshEndpointToken(probeCtx, httpClient, accessTok, sess.Endpoint)
	return rErr != nil && strings.Contains(strings.ToLower(rErr.Error()), "no longer present")
}

func (e *Engine) refreshEndpointToken(
	ctx context.Context,
	client *http.Client,
	accessToken string,
	endpoint string,
) (token string, proxyURL string, expiresAt time.Time, err error) {
	u := fmt.Sprintf("%s/tun/m/assignments?authuser=0", e.opts.TunnelBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Colab-Client-Agent", "colab-cli")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", time.Time{}, &httpStatusError{
			StatusCode: resp.StatusCode,
			Body:       fmt.Sprintf("assignments HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(stripXSSIPrefix(raw)))),
		}
	}

	var parsed struct {
		Assignments []assignResponseWire `json:"assignments"`
	}
	if err := json.Unmarshal(stripXSSIPrefix(raw), &parsed); err != nil {
		return "", "", time.Time{}, err
	}
	for _, a := range parsed.Assignments {
		if a.Endpoint == endpoint && a.RuntimeProxyInfo != nil {
			ttl := a.RuntimeProxyInfo.TokenExpiresInSeconds
			if ttl <= 0 {
				ttl = 3600
			}
			return a.RuntimeProxyInfo.Token, a.RuntimeProxyInfo.URL, time.Now().UTC().Add(time.Duration(ttl) * time.Second), nil
		}
	}
	return "", "", time.Time{}, fmt.Errorf("endpoint %s no longer present in /tun/m/assignments", endpoint)
}

func (e *Engine) reconcileAllAssignmentsBestEffort(ctx context.Context) {
	if e.st == nil {
		return
	}
	sessions, err := e.st.ListSessions(ctx)
	if err != nil {
		return
	}
	for _, s := range sessions {
		if s == nil || s.Status == models.SessionStopped || s.Status == models.SessionLost {
			continue
		}
		acc := e.lookupAccountBestEffort(ctx, s.AccountID)
		if e.isDemoSession(acc, s) {
			continue
		}
		accessTok, proxyURL, cErr := e.acquireCredentials(ctx, acc)
		if cErr != nil {
			continue
		}
		httpClient, _, dErr := e.buildClientAndDialer(proxyURL)
		if dErr != nil {
			continue
		}
		if _, _, _, rErr := e.refreshEndpointToken(ctx, httpClient, accessTok, s.Endpoint); rErr != nil &&
			strings.Contains(rErr.Error(), "no longer present") {
			now := time.Now().UTC()
			_, _ = e.mutateSessionState(ctx, s, false, func(cur *models.RuntimeSession) bool {
				cur.Status = models.SessionLost
				cur.StatusMessage = "VM idle-pruned or preempted upstream"
				cur.StoppedAt = &now
				cur.UpdatedAt = now
				return true
			})
			e.deleteMemState(s.ID)
		}
	}
}

// ============================================================================
// ZMQ-over-WebSocket v5.3 Persistent Kernel Execution & Contributor Firewall
// ============================================================================

type jupyterProxyAuthError struct {
	StatusCode int
	Endpoint   string
}

func (e *jupyterProxyAuthError) Error() string {
	return fmt.Sprintf("Jupyter runtime proxy rejected capability token on %s (HTTP %d)", e.Endpoint, e.StatusCode)
}

type postWriteExecError struct {
	err error
}

func (e *postWriteExecError) Error() string {
	return e.err.Error()
}

func (e *postWriteExecError) Unwrap() error {
	return e.err
}

func isPostWriteExecError(err error) bool {
	var pw *postWriteExecError
	return errors.As(err, &pw)
}

type jupyterWireMsg struct {
	Channel      string                 `json:"channel,omitempty"`
	Header       map[string]any         `json:"header"`
	ParentHeader map[string]any         `json:"parent_header"`
	Metadata     map[string]any         `json:"metadata"`
	Content      map[string]any         `json:"content"`
}

func (e *Engine) runSilentPrelude(
	ctx context.Context,
	wsDialer *websocket.Dialer,
	acc *models.Account,
	sess *models.RuntimeSession,
	mem *sessionMemState,
) error {
	mem.mu.Lock()
	if mem.preludeDone {
		mem.mu.Unlock()
		return nil
	}
	tok := firstNonEmpty(mem.proxyToken, sess.ProxyToken)
	mem.mu.Unlock()

	ctxShort, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := e.executeOverJupyterWS(ctxShort, wsDialer, acc, sess, tok, models.KernelExecRequest{
		SessionID:      sess.ID,
		Code:           "import os; os.makedirs('/content', exist_ok=True); os.chdir('/content')",
		Silent:         true,
		StoreHistory:   false,
		IdleTimeoutSec: 8,
	}, false)
	if err == nil {
		mem.mu.Lock()
		mem.preludeDone = true
		mem.mu.Unlock()
	}
	return err
}

// ExecuteCode executes Python/IPython (or a local `.py`/`.ipynb` file) inside a persistent Colab kernel,
// auto-provisioning or attaching to a sticky session when `SessionID` is omitted, and transparently
// failing over once onto a healthy backup account if an implicit/sticky VM was preempted or lost upstream.
func (e *Engine) ExecuteCode(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	code := req.Code
	execFileLabel := ""
	if strings.TrimSpace(req.LocalFilePath) != "" {
		loadedCode, label, lErr := loadLocalScriptOrNotebook(req.LocalFilePath)
		if lErr != nil {
			return nil, lErr
		}
		if code == "" {
			code = loadedCode
		} else {
			code = loadedCode + "\n" + code
		}
		execFileLabel = label
	}

	// Prepend deterministic environment variable injection when supplied.
	if len(req.Env) > 0 {
		var envBuf strings.Builder
		envBuf.WriteString("import os as __ch_os\n")
		keys := make([]string, 0, len(req.Env))
		for k := range req.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			kJSON, _ := json.Marshal(k)
			vJSON, _ := json.Marshal(req.Env[k])
			envBuf.WriteString(fmt.Sprintf("__ch_os.environ[%s] = %s\n", string(kJSON), string(vJSON)))
		}
		code = envBuf.String() + code
	}

	sess, freshlyProvisioned, err := e.resolveOrProvisionExecSessionWithFlag(ctx, req)
	if err != nil {
		return nil, err
	}
	if callerKey := strings.TrimSpace(req.APIKeyID); callerKey != "" {
		if sess.AdoptedExternal || sess.APIKeyID != callerKey {
			return nil, fmt.Errorf("permission denied: session %q does not belong to caller API key", sess.ID)
		}
	}

	res, execErr := e.executeOnSession(ctx, sess, req, code, execFileLabel)
	// NEVER auto-replay non-idempotent code on a second VM if execute_request was already written to the socket!
	if execErr == nil || ctx.Err() != nil || isPostWriteExecError(execErr) || !isDeadVMOrAuthExecError(execErr) {
		return res, execErr
	}

	// Transparent Mid-Call Failover: when an implicit/sticky execution (`SessionID == ""`)
	// hits a dead/preempted VM or auth failure and `AutoProvisionGPU != ""`, allocate a fresh
	// session on a healthy backup account and retry execution ONCE!
	if strings.TrimSpace(req.SessionID) == "" && req.AutoProvisionGPU != "" && !freshlyProvisioned {
		excluded := append([]string(nil), req.ExcludedAccountIDs...)
		if sess.AccountID != "" {
			excluded = appendUniqueStr(excluded, sess.AccountID)
		}
		allocReq := models.SessionAllocRequest{
			Accelerator:        req.AutoProvisionGPU,
			HighMem:            true,
			AutoFallback:       true,
			StickyKey:          req.StickyKey,
			CallerName:         req.CallerName,
			APIKeyID:           req.APIKeyID,
			Strategy:           req.Strategy,
			AccountSelector:    req.AccountSelector,
			ExcludedAccountIDs: excluded,
		}
		replacementSess, cErr := e.CreateSession(ctx, allocReq)
		if cErr != nil && sess.AccountID != "" && !isAuthFailureErr(execErr) && strings.Contains(strings.ToLower(cErr.Error()), "no eligible") {
			// Single-account pool where only the VM was idle-pruned upstream: allow re-allocating on the same healthy account.
			allocReq.ExcludedAccountIDs = req.ExcludedAccountIDs
			replacementSess, cErr = e.CreateSession(ctx, allocReq)
		}
		if cErr == nil && replacementSess != nil {
			return e.executeOnSession(ctx, replacementSess, req, code, execFileLabel)
		}
	}

	return res, execErr
}

func (e *Engine) executeOnSession(
	ctx context.Context,
	sess *models.RuntimeSession,
	req models.KernelExecRequest,
	code string,
	execFileLabel string,
) (*models.KernelExecResult, error) {
	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)

	now := time.Now().UTC()
	if !req.Silent {
		_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			if cur.Status != models.SessionZombieAlert {
				cur.Status = models.SessionBusy
				cur.UpdatedAt = now
				return true
			}
			return false
		})
	}

	var res *models.KernelExecResult
	var err error
	if e.isDemoSession(acc, sess) {
		res, err = e.executeDemoCode(ctx, acc, sess, mem, req, code)
	} else {
		proxyTok, _, wsDialer, pErr := e.prepareSessionProxyClient(ctx, acc, sess, mem)
		if pErr != nil {
			err = pErr
		} else {
			wsReq := models.KernelExecRequest{
				SessionID:      sess.ID,
				Code:           code,
				IdleTimeoutSec: req.IdleTimeoutSec,
				Silent:         req.Silent,
				StoreHistory:   req.StoreHistory,
				AllowStdin:     req.AllowStdin,
				InlineImages:   req.InlineImages,
			}
			res, err = e.executeOverJupyterWS(ctx, wsDialer, acc, sess, proxyTok, wsReq, true)
			// On Jupyter WS handshake 401/403 (expired runtimeProxyInfo token), refresh proxy token instead of marking Google account HealthAuthExpired!
			var proxyAuthErr *jupyterProxyAuthError
			if err != nil && ctx.Err() == nil && errors.As(err, &proxyAuthErr) {
				if refreshedTok, wsDialer2, rErr := e.forceRefreshSessionProxyToken(ctx, acc, sess, mem); rErr == nil && refreshedTok != "" {
					res, err = e.executeOverJupyterWS(ctx, wsDialer2, acc, sess, refreshedTok, wsReq, true)
				} else if rErr != nil {
					err = rErr
				}
			}
		}
	}

	doneAt := time.Now().UTC()
	if err != nil && ctx.Err() == nil && isPostWriteExecError(err) {
		// Post-write drop: only mark LOST if the VM actually disappeared upstream!
		if !e.confirmEndpointGoneBestEffort(ctx, acc, sess) {
			_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
				cur.ZMQReconnectCount++
				if !req.Silent && cur.Status != models.SessionZombieAlert {
					cur.Status = models.SessionIdle
				}
				cur.UpdatedAt = doneAt
				return true
			})
			if !req.Silent {
				e.publishEvent(models.WSEventSessionUpdated, sess)
			}
			return nil, err
		}
	}

	if err != nil && ctx.Err() == nil && isDeadVMOrAuthExecError(err) {
		// Dead/preempted VM or upstream Google OAuth failure:
		// NEVER overwrite Status=SessionIdle or KeepaliveHealthy=true!
		transitioned := false
		_, _ = e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			cur.Status = models.SessionLost
			cur.KeepaliveHealthy = false
			cur.StatusMessage = fmt.Sprintf("Runtime lost mid-execution: %v", err)
			cur.StoppedAt = &doneAt
			cur.UpdatedAt = doneAt
			cur.UptimeSec = computeUptimeSec(cur)
			transitioned = true
			return true
		})
		if transitioned {
			e.deleteMemState(sess.ID)
		}
		if sess.StickyKey != "" && e.rtr != nil {
			e.unbindStickyForCaller(ctx, sess.StickyKey, sess.APIKeyID)
		}
		if req.StickyKey != "" && req.StickyKey != sess.StickyKey && e.rtr != nil {
			e.unbindStickyForCaller(ctx, req.StickyKey, firstNonEmpty(req.APIKeyID, sess.APIKeyID))
		}
		if isAuthFailureErr(err) && sess.AccountID != "" {
			e.markAccountHealthSafe(ctx, sess.AccountID, models.HealthAuthExpired, err.Error(), 30*time.Minute)
		}
		if sess.AccountID != "" {
			e.syncAccountSessionRollup(ctx, sess.AccountID, doneAt)
		}
		e.publishEvent(models.WSEventSessionUpdated, sess)
		return nil, err
	}

	if !req.Silent {
		updated, _ := e.mutateSessionState(ctx, sess, false, func(cur *models.RuntimeSession) bool {
			if cur.Status != models.SessionZombieAlert {
				cur.Status = models.SessionIdle
			}
			if err == nil {
				cur.LastExecutionAt = &doneAt
				if execFileLabel != "" {
					cur.LastExecutionFile = execFileLabel
				} else if cur.LastExecutionFile == "" {
					cur.LastExecutionFile = "<cell>"
				}
				cur.KeepaliveHealthy = true
				cur.LastKeepaliveAt = &doneAt
			}
			cur.UpdatedAt = doneAt
			return true
		})
		if updated != nil && updated.Status != models.SessionStopped && updated.Status != models.SessionLost {
			e.publishEvent(models.WSEventSessionUpdated, updated)
		}
	}

	return res, err
}

func isDeadVMOrAuthExecError(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), "idle silence timeout exceeded") {
		return false
	}
	if isAuthFailureErr(err) {
		return true
	}
	var stErr *httpStatusError
	if errors.As(err, &stErr) {
		switch stErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusGone,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
	}
	lower := strings.ToLower(err.Error())
	for _, pat := range []string{
		"no longer present in /tun/m/assignments",
		"failed to connect jupyter websocket",
		"failed to write execute_request",
		"jupyter websocket read error",
		"jupyter kernel entered dead state",
		"jupyter kernel entered restarting state",
	} {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return isProxyOrNetworkFailure(err)
}

func (e *Engine) resolveOrProvisionExecSession(ctx context.Context, req models.KernelExecRequest) (*models.RuntimeSession, error) {
	sess, _, err := e.resolveOrProvisionExecSessionWithFlag(ctx, req)
	return sess, err
}

func (e *Engine) resolveOrProvisionExecSessionWithFlag(ctx context.Context, req models.KernelExecRequest) (*models.RuntimeSession, bool, error) {
	callerKey := strings.TrimSpace(req.APIKeyID)
	if strings.TrimSpace(req.SessionID) != "" {
		sess, err := e.GetSession(ctx, req.SessionID)
		if err == nil && sess != nil {
			if callerKey != "" && (sess.AdoptedExternal || sess.APIKeyID != callerKey) {
				return nil, false, fmt.Errorf("permission denied: session %q does not belong to caller API key", req.SessionID)
			}
			if sess.Status != models.SessionStopped && sess.Status != models.SessionLost {
				return sess, false, nil
			}
		}
		if req.AutoProvisionGPU == "" {
			if err != nil {
				return nil, false, err
			}
			return nil, false, fmt.Errorf("%w: session %q is %s", ErrSessionNotFound, req.SessionID, sess.Status)
		}
	}

	if req.StickyKey != "" && e.rtr != nil {
		if stickySess, ok := e.resolveStickyForCaller(ctx, req.StickyKey, req.APIKeyID); ok && stickySess != nil {
			if callerKey != "" && (stickySess.AdoptedExternal || stickySess.APIKeyID != callerKey) {
				// Never route a scoped API key onto an unowned or external session.
			} else if !stickySess.AdoptedExternal || stickySess.StickyKey == req.StickyKey {
				return stickySess, false, nil
			}
		}
	}

	gpu := req.AutoProvisionGPU
	if gpu == "" {
		gpu = models.AcceleratorG4
	}
	sess, err := e.CreateSession(ctx, models.SessionAllocRequest{
		Name:               req.SessionID,
		Accelerator:        gpu,
		HighMem:            true,
		AutoFallback:       true,
		StickyKey:          req.StickyKey,
		CallerName:         req.CallerName,
		APIKeyID:           req.APIKeyID,
		Strategy:           req.Strategy,
		AccountSelector:    req.AccountSelector,
		ExcludedAccountIDs: req.ExcludedAccountIDs,
	})
	if err != nil {
		return nil, false, err
	}
	return sess, true, nil
}

func loadLocalScriptOrNotebook(localPath string) (code string, label string, err error) {
	raw, err := os.ReadFile(localPath)
	if err != nil {
		return "", "", err
	}
	base := filepath.Base(localPath)
	if strings.HasSuffix(strings.ToLower(base), ".ipynb") {
		var nb struct {
			Cells []struct {
				CellType string `json:"cell_type"`
				Source   any    `json:"source"`
			} `json:"cells"`
		}
		if jErr := json.Unmarshal(raw, &nb); jErr != nil {
			return "", "", fmt.Errorf("invalid .ipynb JSON in %s: %w", localPath, jErr)
		}
		var combined strings.Builder
		for idx, c := range nb.Cells {
			if c.CellType != "code" {
				continue
			}
			src := flattenNotebookSource(c.Source)
			if strings.TrimSpace(src) == "" {
				continue
			}
			combined.WriteString(fmt.Sprintf("# --- Notebook Cell %d ---\n%s\n\n", idx, src))
		}
		return combined.String(), base, nil
	}
	return string(raw), base, nil
}

func flattenNotebookSource(src any) string {
	switch v := src.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, line := range v {
			if s, ok := line.(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	default:
		return ""
	}
}

func (e *Engine) executeOverJupyterWS(
	ctx context.Context,
	wsDialer *websocket.Dialer,
	acc *models.Account,
	sess *models.RuntimeSession,
	proxyToken string,
	req models.KernelExecRequest,
	broadcast bool,
) (*models.KernelExecResult, error) {
	start := time.Now()
	wsURL, err := buildJupyterChannelsWSURL(sess.ProxyURL, sess.KernelID, proxyToken)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	headers.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
	headers.Set("Authorization", "Bearer "+proxyToken)
	headers.Set("X-Colab-Client-Agent", "colab-cli")

	conn, resp, err := wsDialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
			_ = resp.Body.Close()
		}
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			return nil, &jupyterProxyAuthError{StatusCode: statusCode, Endpoint: sess.Endpoint}
		}
		if statusCode > 0 {
			return nil, fmt.Errorf(
				"failed to connect Jupyter WebSocket %s (HTTP %d): %w",
				sess.Endpoint,
				statusCode,
				&httpStatusError{StatusCode: statusCode, Body: fmt.Sprintf("Jupyter WebSocket HTTP %d", statusCode)},
			)
		}
		return nil, fmt.Errorf("failed to connect Jupyter WebSocket %s: %w", sess.Endpoint, err)
	}
	// _own_kernel = false: closing WebSocket leaves Python kernel & VRAM weights alive!
	defer conn.Close()
	stopCancelWatch := context.AfterFunc(ctx, func() {
		if nc := conn.UnderlyingConn(); nc != nil {
			_ = nc.SetDeadline(time.Now())
		}
		_ = conn.Close()
	})
	defer stopCancelWatch()

	msgID := uuid.NewString()
	clientSession := sess.JupyterSessionID
	if clientSession == "" {
		clientSession = uuid.NewString()
	}

	execReq := jupyterWireMsg{
		Channel: "shell",
		Header: map[string]any{
			"msg_id":   msgID,
			"session":  clientSession,
			"username": "colabhive",
			"date":     time.Now().UTC().Format(time.RFC3339Nano),
			"msg_type": "execute_request",
			"version":  "5.3",
		},
		ParentHeader: map[string]any{},
		Metadata:     map[string]any{},
		Content: map[string]any{
			"code":             req.Code,
			"silent":           req.Silent,
			"store_history":    req.StoreHistory,
			"user_expressions": map[string]any{},
			"allow_stdin":      req.AllowStdin,
			"stop_on_error":    true,
		},
	}

	writeDL := time.Now().Add(15 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(writeDL) {
		writeDL = dl
	}
	_ = conn.SetWriteDeadline(writeDL)
	if err := conn.WriteJSON(execReq); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("failed to write execute_request: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})

	idleTimeout := time.Duration(req.IdleTimeoutSec * float64(time.Second))
	if req.IdleTimeoutSec < 0 {
		idleTimeout = 0
	}

	var stdoutBuf, stderrBuf strings.Builder
	var resultStr string
	var execErr *models.KernelExecError
	var richOutputs []models.KernelRichOutput
	var idleDrainDeadline time.Time
	execCount := 0
	replyStatus := "ok"
	gotIdle := false
	gotReply := false

	blockDrive := acc != nil && (acc.IsContributor || acc.Guardrails.BlockDrivePropagation)

	for !(gotIdle && gotReply) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if gotIdle && !gotReply {
			if time.Now().After(idleDrainDeadline) {
				gotReply = true
				break
			}
			_ = conn.SetReadDeadline(idleDrainDeadline)
		} else if idleTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
		} else {
			_ = conn.SetReadDeadline(time.Time{})
		}

		msgType, rawFrame, rErr := conn.ReadMessage()
		if rErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if gotIdle {
				gotReply = true
				break
			}
			if netErr, ok := rErr.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				return nil, fmt.Errorf("kernel execution idle silence timeout exceeded (%.1fs)", req.IdleTimeoutSec)
			}
			return nil, &postWriteExecError{err: fmt.Errorf("jupyter websocket read error: %w", rErr)}
		}

		msg, parseErr := decodeJupyterFrame(msgType, rawFrame)
		if parseErr != nil {
			continue
		}

		wireType := asString(msg.Header["msg_type"])
		parentID := asString(msg.ParentHeader["msg_id"])

		// Unparented kernel death notification
		if wireType == "status" && parentID == "" {
			st := asString(msg.Content["execution_state"])
			if st == "dead" || st == "restarting" {
				return nil, &postWriteExecError{err: fmt.Errorf("jupyter kernel entered %s state", st)}
			}
		}

		// Contributor WebSocket Auth-Propagation Firewall (`colab_request` / `dfs_ephemeral` / `request_auth`)
		if wireType == "colab_request" {
			colabMsgID := asString(msg.Metadata["colab_msg_id"])
			if blockDrive {
				firewallMsg := "Blocked by ColabHive Contributor Firewall: Drive/GCP credential propagation is disabled on donated accounts."
				reply := jupyterWireMsg{
					Channel: "stdin",
					Header: map[string]any{
						"msg_id":   uuid.NewString(),
						"session":  clientSession,
						"username": "colabhive",
						"date":     time.Now().UTC().Format(time.RFC3339Nano),
						"msg_type": "input_reply",
						"version":  "5.3",
					},
					ParentHeader: msg.Header,
					Metadata:     map[string]any{},
					Content: map[string]any{
						"value": map[string]any{
							"type":         "colab_reply",
							"colab_msg_id": colabMsgID,
							"error":        firewallMsg,
						},
					},
				}
				_ = conn.WriteJSON(reply)
				stderrBuf.WriteString(firewallMsg + "\n")
				execErr = &models.KernelExecError{
					Name:      "PermissionError",
					Value:     firewallMsg,
					Traceback: []string{firewallMsg},
				}
				replyStatus = "error"
				e.publishEvent(models.WSEventGuardrailPreempt, map[string]any{
					"session_id": sess.ID,
					"account_id": sess.AccountID,
					"reason":     firewallMsg,
				})
				if broadcast {
					e.publishStreamChunk(sess, "stderr", firewallMsg+"\n", nil)
				}
				continue
			}
		}

		if parentID != msgID {
			continue
		}

		switch wireType {
		case "status":
			if asString(msg.Content["execution_state"]) == "idle" {
				gotIdle = true
				if !gotReply && idleDrainDeadline.IsZero() {
					idleDrainDeadline = time.Now().Add(250 * time.Millisecond)
				}
			}

		case "stream":
			streamName := firstNonEmpty(asString(msg.Content["name"]), "stdout")
			txt := asString(msg.Content["text"])
			if streamName == "stderr" {
				stderrBuf.WriteString(txt)
			} else {
				stdoutBuf.WriteString(txt)
			}
			if broadcast && txt != "" {
				e.publishStreamChunk(sess, streamName, txt, nil)
			}

		case "clear_output":
			stdoutBuf.Reset()
			stderrBuf.Reset()
			richOutputs = nil

		case "execute_result":
			if c := asInt(msg.Content["execution_count"]); c > 0 {
				execCount = c
			}
			if dataMap, ok := msg.Content["data"].(map[string]any); ok {
				resultStr = extractBestTextRepresentation(dataMap)
			}

		case "display_data", "update_display_data":
			if dataMap, ok := msg.Content["data"].(map[string]any); ok {
				ro := e.captureRichDisplayData(sess, execCount, len(richOutputs), dataMap, req.InlineImages)
				if ro != nil {
					richOutputs = append(richOutputs, *ro)
					if broadcast {
						e.publishStreamChunk(sess, "colab", fmt.Sprintf("[artifact] %s\n", firstNonEmpty(ro.ArtifactURL, ro.FilePath)), ro)
					}
				}
			}

		case "error":
			ename := asString(msg.Content["ename"])
			evalue := asString(msg.Content["evalue"])
			tb := asStringSlice(msg.Content["traceback"])
			ke := &models.KernelExecError{
				Name:      ename,
				Value:     evalue,
				Traceback: tb,
			}
			if ename == "SystemExit" {
				code := parseCPythonSystemExitCode(evalue)
				ke.ExitCode = &code
			}
			execErr = ke
			replyStatus = "error"

		case "execute_reply":
			gotReply = true
			if st := asString(msg.Content["status"]); st != "" && execErr == nil {
				replyStatus = st
			}
			if c := asInt(msg.Content["execution_count"]); c > 0 {
				execCount = c
			}
			if replyStatus == "error" && execErr == nil {
				ename := firstNonEmpty(asString(msg.Content["ename"]), "ExecutionError")
				evalue := asString(msg.Content["evalue"])
				ke := &models.KernelExecError{
					Name:      ename,
					Value:     evalue,
					Traceback: asStringSlice(msg.Content["traceback"]),
				}
				if ename == "SystemExit" {
					code := parseCPythonSystemExitCode(evalue)
					ke.ExitCode = &code
				}
				execErr = ke
			}
		}
	}

	const maxInlineStreamBytes = 512 * 1024
	stdoutStr := stdoutBuf.String()
	stderrStr := stderrBuf.String()
	truncated := false
	if len(stdoutStr) > maxInlineStreamBytes {
		stdoutStr = stdoutStr[:maxInlineStreamBytes]
		truncated = true
	}

	return &models.KernelExecResult{
		Session:         sess.Name,
		ExecutionID:     msgID,
		ExecutionCount:  execCount,
		Status:          replyStatus,
		Stdout:          stdoutStr,
		Stderr:          stderrStr,
		OutputTruncated: truncated,
		Result:          resultStr,
		ExecutionError:  execErr,
		RichOutputs:     richOutputs,
		DurationMs:      time.Since(start).Milliseconds(),
	}, nil
}

func buildJupyterChannelsWSURL(proxyBaseURL string, kernelID string, proxyToken string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(proxyBaseURL, "/"))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "ws", "wss":
	default:
		parsed.Scheme = "wss"
	}
	parsed.Path = path.Join(parsed.Path, "/api/kernels", kernelID, "channels")
	q := parsed.Query()
	q.Set("session_id", uuid.NewString())
	q.Set("token", proxyToken)
	q.Set("colab-runtime-proxy-token", proxyToken)
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

// decodeJupyterFrame supports BOTH Jupyter Default JSON text/binary frames AND `v1.kernel.websocket.jupyter.org`.
func decodeJupyterFrame(msgType int, raw []byte) (*jupyterWireMsg, error) {
	if msgType == websocket.TextMessage {
		var m jupyterWireMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	}
	if msgType == websocket.BinaryMessage && len(raw) >= 8 {
		// Default binary envelope: [nbufs u32 BE, offsets[0..nbufs] u32 BE, buffer0 JSON, *buffers]
		nbufs := int(binary.BigEndian.Uint32(raw[:4]))
		if nbufs >= 1 && nbufs < 256 && len(raw) >= 4*(nbufs+1) {
			off0 := int(binary.BigEndian.Uint32(raw[4:8]))
			end0 := len(raw)
			if nbufs >= 2 {
				end0 = int(binary.BigEndian.Uint32(raw[8:12]))
			}
			if off0 >= 4*(nbufs+1) && off0 < end0 && end0 <= len(raw) {
				var m jupyterWireMsg
				if err := json.Unmarshal(raw[off0:end0], &m); err == nil && m.Header != nil {
					return &m, nil
				}
			}
		}
	}
	return nil, errors.New("unrecognized jupyter frame")
}

func (e *Engine) captureRichDisplayData(
	sess *models.RuntimeSession,
	execCount int,
	idx int,
	dataMap map[string]any,
	inlineImages bool,
) *models.KernelRichOutput {
	mimes := make([]string, 0, len(dataMap))
	for k := range dataMap {
		mimes = append(mimes, k)
	}
	sort.Strings(mimes)

	for _, imgMime := range []string{"image/png", "image/jpeg"} {
		if b64Val, ok := dataMap[imgMime].(string); ok && strings.TrimSpace(b64Val) != "" {
			cleanB64 := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(b64Val), "\n", ""), "\r", "")
			decoded, err := base64.StdEncoding.DecodeString(cleanB64)
			if err != nil {
				continue
			}
			ext := ".png"
			if imgMime == "image/jpeg" {
				ext = ".jpg"
			}
			fileName := fmt.Sprintf("plot_%03d_%02d%s", execCount, idx, ext)
			artPath, pErr := e.ArtifactPath(sess.ID, fileName)
			if pErr == nil {
				_ = os.MkdirAll(filepath.Dir(artPath), 0o755)
				_ = os.WriteFile(artPath, decoded, 0o644)
			}
			ro := &models.KernelRichOutput{
				Index:       idx,
				MimeTypes:   mimes,
				FilePath:    fmt.Sprintf("/content/.colabhive_outputs/%s", fileName),
				ArtifactURL: fmt.Sprintf("/api/v1/artifacts/%s/%s", sess.ID, fileName),
				Title:       fileName,
				SizeBytes:   int64(len(decoded)),
			}
			if inlineImages {
				ro.InlineBase64 = cleanB64
			}
			return ro
		}
	}

	txt := extractBestTextRepresentation(dataMap)
	if txt != "" {
		return &models.KernelRichOutput{
			Index:     idx,
			MimeTypes: mimes,
			TextData:  txt,
			SizeBytes: int64(len(txt)),
		}
	}
	return nil
}

func extractBestTextRepresentation(dataMap map[string]any) string {
	for _, k := range []string{"text/markdown", "text/plain", "text/html"} {
		if v, ok := dataMap[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseCPythonSystemExitCode(evalue string) int {
	trimmed := strings.Trim(strings.TrimSpace(evalue), "'\"")
	if trimmed == "" || trimmed == "None" || trimmed == "0" {
		return 0
	}
	if n, err := strconv.Atoi(trimmed); err == nil {
		return n
	}
	return 1
}

// ============================================================================
// Jupyter `/api/contents` Remote File System & `uv pip install --system`
// ============================================================================

func normalizeRemoteContentsPath(remotePath string) string {
	cleaned := strings.TrimSpace(strings.ReplaceAll(remotePath, "\\", "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "content"
	}
	return cleaned
}

func escapeRemoteContentsURLPath(rel string) string {
	parts := strings.Split(rel, "/")
	escaped := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		escaped = append(escaped, url.PathEscape(p))
	}
	if len(escaped) == 0 {
		return "content"
	}
	return strings.Join(escaped, "/")
}

// ListFiles lists directory entries on a Colab VM under remotePath (default `/content`).
func (e *Engine) ListFiles(ctx context.Context, sessionID string, remotePath string) ([]models.RemoteFileEntry, error) {
	sess, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)

	if e.isDemoSession(acc, sess) {
		return e.listDemoFiles(mem, remotePath), nil
	}

	proxyTok, httpClient, _, pErr := e.prepareSessionProxyClient(ctx, acc, sess, mem)
	if pErr != nil {
		return nil, pErr
	}
	rel := normalizeRemoteContentsPath(remotePath)
	u := fmt.Sprintf(
		"%s/api/contents/%s?authuser=0&colab-runtime-proxy-token=%s",
		strings.TrimRight(sess.ProxyURL, "/"),
		escapeRemoteContentsURLPath(rel),
		url.QueryEscape(proxyTok),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	setProxyHeaders(req, proxyTok)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{StatusCode: resp.StatusCode, Body: string(stripXSSIPrefix(raw))}
	}

	var model struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Type    string `json:"type"`
		Size    int64  `json:"size"`
		Content []struct {
			Name         string     `json:"name"`
			Path         string     `json:"path"`
			Type         string     `json:"type"`
			Size         int64      `json:"size"`
			LastModified *time.Time `json:"last_modified"`
		} `json:"content"`
	}
	if err := json.Unmarshal(stripXSSIPrefix(raw), &model); err != nil {
		return nil, err
	}

	entries := make([]models.RemoteFileEntry, 0, len(model.Content))
	for _, item := range model.Content {
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
	sortRemoteEntries(entries)
	return entries, nil
}

// UploadFile writes `data` to `remotePath` on a Colab VM via `PUT /api/contents/{path}` (chunking >25MB payloads).
func (e *Engine) UploadFile(ctx context.Context, sessionID string, remotePath string, data []byte) error {
	sess, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)

	if e.isDemoSession(acc, sess) {
		e.writeDemoFile(mem, remotePath, data)
		return nil
	}

	proxyTok, httpClient, _, pErr := e.prepareSessionProxyClient(ctx, acc, sess, mem)
	if pErr != nil {
		return pErr
	}
	rel := normalizeRemoteContentsPath(remotePath)
	baseName := path.Base(rel)
	u := fmt.Sprintf(
		"%s/api/contents/%s?authuser=0&colab-runtime-proxy-token=%s",
		strings.TrimRight(sess.ProxyURL, "/"),
		escapeRemoteContentsURLPath(rel),
		url.QueryEscape(proxyTok),
	)

	const maxChunk = 24 * 1024 * 1024
	if len(data) <= maxChunk {
		return e.putContentsChunk(ctx, httpClient, u, proxyTok, baseName, rel, data, 0)
	}

	totalChunks := (len(data) + maxChunk - 1) / maxChunk
	for i := 0; i < totalChunks; i++ {
		end := (i + 1) * maxChunk
		if end > len(data) {
			end = len(data)
		}
		chunkNum := i + 1
		if i == totalChunks-1 {
			chunkNum = -1
		}
		if err := e.putContentsChunk(ctx, httpClient, u, proxyTok, baseName, rel, data[i*maxChunk:end], chunkNum); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) putContentsChunk(
	ctx context.Context,
	client *http.Client,
	targetURL string,
	proxyToken string,
	name string,
	relPath string,
	part []byte,
	chunkNum int,
) error {
	payload := map[string]any{
		"name":    name,
		"path":    relPath,
		"type":    "file",
		"format":  "base64",
		"content": base64.StdEncoding.EncodeToString(part),
	}
	if chunkNum != 0 {
		payload["chunk"] = chunkNum
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, targetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	setProxyHeaders(req, proxyToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{StatusCode: resp.StatusCode, Body: string(stripXSSIPrefix(raw))}
	}
	return nil
}

// DownloadFile fetches a remote file from `/api/contents/{path}?content=1`.
func (e *Engine) DownloadFile(ctx context.Context, sessionID string, remotePath string) ([]byte, error) {
	sess, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)

	if e.isDemoSession(acc, sess) {
		return e.readDemoFile(mem, remotePath)
	}

	proxyTok, httpClient, _, pErr := e.prepareSessionProxyClient(ctx, acc, sess, mem)
	if pErr != nil {
		return nil, pErr
	}
	rel := normalizeRemoteContentsPath(remotePath)
	u := fmt.Sprintf(
		"%s/api/contents/%s?content=1&authuser=0&colab-runtime-proxy-token=%s",
		strings.TrimRight(sess.ProxyURL, "/"),
		escapeRemoteContentsURLPath(rel),
		url.QueryEscape(proxyTok),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	setProxyHeaders(req, proxyTok)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{StatusCode: resp.StatusCode, Body: string(stripXSSIPrefix(raw))}
	}

	var model struct {
		Type    string `json:"type"`
		Format  string `json:"format"`
		Content any    `json:"content"`
	}
	if err := json.Unmarshal(stripXSSIPrefix(raw), &model); err != nil {
		return nil, err
	}
	if model.Type == "directory" {
		return nil, fmt.Errorf("%q is a directory, not a file", remotePath)
	}
	switch strings.ToLower(model.Format) {
	case "base64":
		s, _ := model.Content.(string)
		return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	case "json":
		return json.MarshalIndent(model.Content, "", "  ")
	default:
		if s, ok := model.Content.(string); ok {
			return []byte(s), nil
		}
		return json.Marshal(model.Content)
	}
}

// DeleteFile deletes a remote file on a Colab VM via `DELETE /api/contents/{path}`.
func (e *Engine) DeleteFile(ctx context.Context, sessionID string, remotePath string) error {
	sess, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)

	if e.isDemoSession(acc, sess) {
		e.deleteDemoFile(mem, remotePath)
		return nil
	}

	proxyTok, httpClient, _, pErr := e.prepareSessionProxyClient(ctx, acc, sess, mem)
	if pErr != nil {
		return pErr
	}
	rel := normalizeRemoteContentsPath(remotePath)
	u := fmt.Sprintf(
		"%s/api/contents/%s?authuser=0&colab-runtime-proxy-token=%s",
		strings.TrimRight(sess.ProxyURL, "/"),
		escapeRemoteContentsURLPath(rel),
		url.QueryEscape(proxyTok),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	setProxyHeaders(req, proxyTok)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return &httpStatusError{StatusCode: resp.StatusCode, Body: "DELETE /api/contents failed"}
}

// InstallPackages installs Python packages via `uv pip install --system` with `pip` fallback.
func (e *Engine) InstallPackages(
	ctx context.Context,
	sessionID string,
	packages []string,
	requirementsTxt string,
) (*models.KernelExecResult, error) {
	sess, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	cleanPkgs := make([]string, 0, len(packages))
	for _, p := range packages {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			cleanPkgs = append(cleanPkgs, trimmed)
		}
	}

	var argsList []string
	if strings.TrimSpace(requirementsTxt) != "" {
		// Support either inline requirements content or an already-uploaded `/content/...` path.
		trimmedReq := strings.TrimSpace(requirementsTxt)
		remoteReqPath := "/content/requirements.txt"
		if strings.HasPrefix(trimmedReq, "/content/") && !strings.Contains(trimmedReq, "\n") {
			remoteReqPath = trimmedReq
		} else {
			if uErr := e.UploadFile(ctx, sess.ID, remoteReqPath, []byte(trimmedReq+"\n")); uErr != nil {
				return nil, uErr
			}
		}
		argsList = append(argsList, "-r", remoteReqPath)
	}
	argsList = append(argsList, cleanPkgs...)
	if len(argsList) == 0 {
		return nil, errors.New("at least one package or requirements_txt must be supplied")
	}

	acc := e.lookupAccountBestEffort(ctx, sess.AccountID)
	mem := e.getOrCreateMemState(sess.ID, sess.ProxyToken)
	if e.isDemoSession(acc, sess) {
		mem.mu.Lock()
		mem.installedPkgs = append(mem.installedPkgs, cleanPkgs...)
		mem.execCount++
		count := mem.execCount
		mem.mu.Unlock()
		msg := fmt.Sprintf("Resolved %d packages in 184ms\nInstalled %s\nInstallation Complete (via uv)!\n", len(argsList), strings.Join(argsList, ", "))
		e.publishStreamChunk(sess, "stdout", msg, nil)
		return &models.KernelExecResult{
			Session:        sess.Name,
			ExecutionID:    uuid.NewString(),
			ExecutionCount: count,
			Status:         "ok",
			Stdout:         msg,
			DurationMs:     184,
		}, nil
	}

	argsJSON, _ := json.Marshal(argsList)
	snippet := fmt.Sprintf(
		"import subprocess, sys\n__ch_pkgs = %s\ntry:\n    subprocess.check_call(['uv', 'pip', 'install', '--system', *__ch_pkgs])\n    print('Installation Complete (via uv)!')\nexcept Exception:\n    subprocess.check_call([sys.executable, '-m', 'pip', 'install', *__ch_pkgs])\n    print('Installation Complete (via pip)!')\n",
		string(argsJSON),
	)
	return e.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID:      sess.ID,
		Code:           snippet,
		IdleTimeoutSec: 300,
		StoreHistory:   true,
	})
}

func sortRemoteEntries(entries []models.RemoteFileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "directory"
		}
		return entries[i].Name < entries[j].Name
	})
}

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	default:
		return ""
	}
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func asStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if s, ok2 := v.([]string); ok2 {
			return s
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
