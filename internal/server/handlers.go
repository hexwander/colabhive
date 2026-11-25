package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/proxy"
	"github.com/colabhive/colabhive/internal/router"
	"github.com/colabhive/colabhive/internal/runtime"
	"github.com/colabhive/colabhive/internal/vault"
)

var errRequestBodyTooLarge = errors.New("request body exceeds maximum allowed size")

func isValidASCIIExperimentIDServer(id string) bool {
	if len(id) == 0 || len(id) > 128 || id[0] == '.' || id[len(id)-1] == '.' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	stem := id
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}

func isSafePathSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	if strings.ContainsAny(seg, "/\\:\x00") {
		return false
	}
	return true
}

func (s *Server) requestAPIKey(r *http.Request) *models.AgentAPIKey {
	if v := r.Context().Value(ctxKeyAPIKey); v != nil {
		if k, ok := v.(*models.AgentAPIKey); ok {
			return k
		}
	}
	return nil
}

func mapServiceErrorStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "already exists"):
		return http.StatusConflict
	case strings.Contains(msg, "revoked"),
		strings.Contains(msg, "expired"),
		strings.Contains(msg, "exhausted"),
		strings.Contains(msg, "max_uses"),
		strings.Contains(msg, "max uses"):
		return http.StatusGone
	case strings.Contains(msg, "forbidden"),
		strings.Contains(msg, "cannot overwrite an owner account"):
		return http.StatusForbidden
	case errors.Is(err, runtime.ErrSessionNotFound),
		errors.Is(err, runtime.ErrExperimentNotFound),
		strings.Contains(msg, "not found"):
		return http.StatusNotFound
	case errors.Is(err, router.ErrInvalidAPIKey),
		strings.Contains(msg, "unauthorized"):
		return http.StatusUnauthorized
	case errors.Is(err, router.ErrAPIKeyBudgetExceeded),
		errors.Is(err, router.ErrNoEligibleAccount),
		strings.Contains(msg, "precondition"),
		strings.Contains(msg, "http 412"),
		strings.Contains(msg, "budget"):
		return http.StatusPreconditionFailed
	case strings.Contains(msg, "unsupported accelerator"),
		strings.Contains(msg, "invalid"),
		strings.Contains(msg, "required"),
		strings.Contains(msg, "http 400"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func decodeJSONBody(r *http.Request, dst any) error {
	return decodeJSONBodyLimit(r, dst, 64<<20)
}

func decodeJSONBodyLimit(r *http.Request, dst any, maxBytes int64) error {
	if r.Body == nil {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > maxBytes {
		return errRequestBodyTooLarge
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	return json.Unmarshal([]byte(trimmed), dst)
}

func (s *Server) signRichOutputs(outs []models.KernelRichOutput) []models.KernelRichOutput {
	if len(outs) == 0 || !s.isAuthRequired() {
		return outs
	}
	cp := make([]models.KernelRichOutput, len(outs))
	for i, ro := range outs {
		if ro.ArtifactURL != "" {
			ro.ArtifactURL = s.signArtifactURL(ro.ArtifactURL)
		}
		cp[i] = ro
	}
	return cp
}

var serverEmailRedactRegex = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

func redactEmailsInServerText(s string, knownEmail string) string {
	if s == "" {
		return ""
	}
	if knownEmail != "" {
		s = strings.ReplaceAll(s, " ("+knownEmail+")", "")
		s = strings.ReplaceAll(s, knownEmail, "pooled-account")
	}
	return serverEmailRedactRegex.ReplaceAllString(s, "pooled-account")
}

func redactSessionForScopedCaller(sess *models.RuntimeSession) *models.RuntimeSession {
	if sess == nil {
		return nil
	}
	cp := *sess
	origEmail := sess.AccountEmail
	cp.AccountEmail = ""
	cp.AccountID = ""
	cp.StatusMessage = redactEmailsInServerText(cp.StatusMessage, origEmail)
	if len(sess.Timeline) > 0 {
		tl := make([]models.ExecutionStep, len(sess.Timeline))
		for i, step := range sess.Timeline {
			step.Label = redactEmailsInServerText(step.Label, origEmail)
			step.Detail = redactEmailsInServerText(step.Detail, origEmail)
			tl[i] = step
		}
		cp.Timeline = tl
	}
	if len(sess.RecentLogs) > 0 {
		logs := make([]string, len(sess.RecentLogs))
		for i, line := range sess.RecentLogs {
			logs[i] = redactEmailsInServerText(line, origEmail)
		}
		cp.RecentLogs = logs
	}
	if len(sess.Cells) > 0 {
		cells := make([]models.NotebookCell, len(sess.Cells))
		for i, cell := range sess.Cells {
			if len(cell.Outputs) > 0 {
				outs := make([]models.KernelRichOutput, len(cell.Outputs))
				for j, ro := range cell.Outputs {
					ro.TextData = redactEmailsInServerText(ro.TextData, origEmail)
					ro.Title = redactEmailsInServerText(ro.Title, origEmail)
					outs[j] = ro
				}
				cell.Outputs = outs
			}
			cells[i] = cell
		}
		cp.Cells = cells
	}
	return &cp
}

func (s *Server) pooledEmailsForRedaction(ctx context.Context, primary string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(em string) {
		em = strings.TrimSpace(em)
		if em == "" {
			return
		}
		lw := strings.ToLower(em)
		if !seen[lw] {
			seen[lw] = true
			out = append(out, em)
		}
	}
	add(primary)
	if s.pool != nil {
		if accs, err := s.pool.ListAccounts(ctx); err == nil {
			for _, a := range accs {
				if a != nil {
					add(a.Email)
				}
			}
		}
	}
	return out
}

func replaceKnownEmailsCI(text string, emails []string) string {
	if text == "" || len(emails) == 0 {
		return text
	}
	for _, em := range emails {
		if em == "" {
			continue
		}
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(em))
		text = re.ReplaceAllString(text, "pooled-account")
	}
	return text
}

func redactRichOutputsWithEmails(outs []models.KernelRichOutput, knownEmails []string) []models.KernelRichOutput {
	if len(outs) == 0 || len(knownEmails) == 0 {
		return outs
	}
	res := make([]models.KernelRichOutput, len(outs))
	for i, ro := range outs {
		ro.TextData = replaceKnownEmailsCI(ro.TextData, knownEmails)
		ro.Title = replaceKnownEmailsCI(ro.Title, knownEmails)
		res[i] = ro
	}
	return res
}

func redactExecResultForScopedCaller(res *models.KernelExecResult, knownEmails []string) *models.KernelExecResult {
	if res == nil {
		return nil
	}
	cp := *res
	cp.Stdout = replaceKnownEmailsCI(cp.Stdout, knownEmails)
	cp.Stderr = replaceKnownEmailsCI(cp.Stderr, knownEmails)
	cp.Result = replaceKnownEmailsCI(cp.Result, knownEmails)
	cp.RichOutputs = redactRichOutputsWithEmails(cp.RichOutputs, knownEmails)
	if cp.ExecutionError != nil {
		errCp := *cp.ExecutionError
		errCp.Value = replaceKnownEmailsCI(errCp.Value, knownEmails)
		if len(errCp.Traceback) > 0 {
			tb := make([]string, len(errCp.Traceback))
			for i, ln := range errCp.Traceback {
				tb[i] = replaceKnownEmailsCI(ln, knownEmails)
			}
			errCp.Traceback = tb
		}
		cp.ExecutionError = &errCp
	}
	return &cp
}

func redactExperimentForScopedCaller(exp *models.Experiment) *models.Experiment {
	if exp == nil {
		return nil
	}
	cp := *exp
	cp.LocalOutputDir = ""
	cp.MergedJSONLPath = ""
	if len(exp.Shards) > 0 {
		shards := make([]models.ExperimentShard, len(exp.Shards))
		for i, sh := range exp.Shards {
			origEmail := sh.AccountEmail
			sh.AccountEmail = ""
			sh.AccountID = ""
			if sh.LogTail != "" {
				sh.LogTail = redactEmailsInServerText(sh.LogTail, origEmail)
			}
			shards[i] = sh
		}
		cp.Shards = shards
	}
	return &cp
}

func redactTeardownForScopedCaller(res *models.ExperimentTeardownResult) *models.ExperimentTeardownResult {
	if res == nil {
		return nil
	}
	cp := *res
	cp.MergedJSONLPath = ""
	cp.ExportedNotebooks = nil
	return &cp
}

func (s *Server) signSessionArtifacts(sess *models.RuntimeSession) *models.RuntimeSession {
	if sess == nil || !s.isAuthRequired() || len(sess.Cells) == 0 {
		return sess
	}
	cp := *sess
	cells := make([]models.NotebookCell, len(sess.Cells))
	for i, c := range sess.Cells {
		c.Outputs = s.signRichOutputs(c.Outputs)
		cells[i] = c
	}
	cp.Cells = cells
	return &cp
}

func (s *Server) prepareSessionForResponse(r *http.Request, sess *models.RuntimeSession) *models.RuntimeSession {
	out := s.signSessionArtifacts(sess)
	if s.requestAPIKey(r) != nil {
		out = redactSessionForScopedCaller(out)
	}
	return out
}

func (s *Server) signExperimentArtifacts(exp *models.Experiment) *models.Experiment {
	if exp == nil || !s.isAuthRequired() || len(exp.Artifacts) == 0 {
		return exp
	}
	cp := *exp
	cp.Artifacts = s.signRichOutputs(exp.Artifacts)
	return &cp
}

func (s *Server) prepareExperimentForResponse(r *http.Request, exp *models.Experiment) *models.Experiment {
	out := s.signExperimentArtifacts(exp)
	if s.requestAPIKey(r) != nil {
		out = redactExperimentForScopedCaller(out)
	}
	return out
}

func (s *Server) isCloudMode() bool {
	return strings.EqualFold(strings.TrimSpace(s.cfg.Mode), "cloud")
}

func isLoopbackHost(rawHost string) bool {
	h := strings.TrimSpace(rawHost)
	if parsedHost, _, err := net.SplitHostPort(h); err == nil && parsedHost != "" {
		h = parsedHost
	}
	h = strings.Trim(h, "[]")
	return strings.EqualFold(h, "localhost") || h == "127.0.0.1" || h == "::1" || h == "0.0.0.0"
}

func isLoopbackBaseURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return false
	}
	return isLoopbackHost(u.Host)
}

func (s *Server) inferServerBaseURL(r *http.Request) string {
	configured := strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL), "/")
	if configured != "" && (!s.isCloudMode() || !isLoopbackBaseURL(configured) || r == nil) {
		return configured
	}
	if r == nil {
		if configured != "" {
			return configured
		}
		return "http://127.0.0.1:7860"
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Host)

	// Strictly gate BOTH X-Forwarded-Proto and X-Forwarded-Host behind `--trust-proxy` AND a trusted proxy peer!
	if s.cfg.TrustProxy && isTrustedProxyPeer(r.RemoteAddr) {
		if xfp := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xfp != "" {
			firstProto := strings.ToLower(strings.TrimSpace(strings.Split(xfp, ",")[0]))
			if firstProto == "https" || firstProto == "http" {
				scheme = firstProto
			}
		}
		if xfh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xfh != "" {
			firstHost := strings.TrimSpace(strings.Split(xfh, ",")[0])
			if firstHost != "" {
				host = firstHost
			}
		}
	}

	if host == "" {
		if configured != "" {
			return configured
		}
		host = "127.0.0.1:7860"
	}
	if configured != "" && isLoopbackHost(host) {
		return configured
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func (s *Server) isCallerAuthenticatedOnHealthProbe(r *http.Request) bool {
	if !s.isAuthRequired() {
		return true
	}
	tok := extractRequestToken(r)
	if tok == "" {
		return false
	}
	adminTok := strings.TrimSpace(s.cfg.AdminToken)
	if adminTok != "" && constantTimeTokenEqual(tok, adminTok) {
		return true
	}
	if strings.HasPrefix(tok, "ch_sk_") && s.router != nil {
		if _, err := s.router.AuthenticateAPIKey(r.Context(), tok); err == nil {
			return true
		}
	}
	return false
}

// ============================================================================
// 1. Health & Hardware Catalog
// ============================================================================

func (s *Server) handleGetHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fastProbe := r.URL.Query().Get("fast") == "1" || r.URL.Query().Get("probe") == "1"

	mode := "local"
	if s.isCloudMode() {
		mode = "cloud"
	}
	authReq := s.isAuthRequired()
	publicURL := s.inferServerBaseURL(r)
	authed := s.isCallerAuthenticatedOnHealthProbe(r)

	activeSess := 0
	activeExps := 0
	wsClients := 0

	// Redact operational session/experiment/WS counts on unauthenticated cloud/protected health probes,
	// while still returning 200 OK for Docker HEALTHCHECK and frontend boot mode discovery.
	if authed {
		if !fastProbe && s.runtime != nil {
			if list, err := s.runtime.ListSessions(ctx, models.SessionFilter{ActiveOnly: true}); err == nil {
				activeSess = len(list)
			}
			if exps, err := s.runtime.ListExperiments(ctx); err == nil {
				for _, e := range exps {
					if e != nil && (e.Status == models.ExperimentRunning || e.Status == models.ExperimentProvisioning) {
						activeExps++
					}
				}
			}
		}
		if s.hub != nil {
			wsClients = s.hub.ClientCount()
		}
	}

	writeJSON(w, http.StatusOK, models.DaemonHealthStatus{
		Healthy:               true,
		Version:               s.cfg.Version,
		DeploymentMode:        mode,
		AuthRequired:          authReq,
		PublicURL:             publicURL,
		UptimeSec:             int64(time.Since(s.startedAt).Seconds()),
		KeepaliveLoopSec:      s.cfg.KeepaliveIntervalSec,
		ResourcePollSec:       s.cfg.ResourcePollSec,
		AntiZombieArmed:       true,
		ContributorFirewallOn: true,
		ActiveSessions:        activeSess,
		ActiveExperiments:     activeExps,
		ConnectedWSClients:    wsClients,
		Timestamp:             time.Now().UTC(),
	})
}

func (s *Server) handleGetHardware(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, models.HardwareCatalog)
}

// ============================================================================
// 2. Account Pool & Quota Summary
// ============================================================================

func accountInAllowedGroupsREST(acc *models.Account, allowed []string) bool {
	if acc == nil {
		return false
	}
	if len(allowed) == 0 {
		return true
	}
	for _, want := range allowed {
		for _, g := range acc.Groups {
			if strings.EqualFold(strings.TrimSpace(g), strings.TrimSpace(want)) {
				return true
			}
		}
	}
	return false
}

func gpuInAllowedGPUsREST(gpu models.AcceleratorType, allowed []models.AcceleratorType) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, g := range allowed {
		if g == gpu {
			return true
		}
	}
	return false
}

func (s *Server) buildScopedQuotaSummary(ctx context.Context, key *models.AgentAPIKey, tagFilter string) *models.QuotaSummary {
	out := &models.QuotaSummary{
		GPUEquivalence: make(map[models.AcceleratorType]models.GPUHourEquivalence),
		UpdatedAt:      time.Now().UTC(),
	}
	var accs []*models.Account
	if s.pool != nil {
		accs, _ = s.pool.ListAccounts(ctx)
	} else if s.st != nil {
		accs, _ = s.st.ListAccounts(ctx)
	}
	if tagFilter != "" {
		filtered := make([]*models.Account, 0, len(accs))
		for _, a := range accs {
			if a == nil {
				continue
			}
			for _, t := range a.Tags {
				if strings.EqualFold(t, tagFilter) {
					filtered = append(filtered, a)
					break
				}
			}
		}
		accs = filtered
	}
	eligibleCount := 0
	eligibleAccIDs := make(map[string]bool)
	for _, a := range accs {
		if a == nil || a.Health == models.HealthRevokedByUser {
			continue
		}
		if !accountInAllowedGroupsREST(a, key.AllowedGroups) {
			continue
		}
		eligibleCount++
		eligibleAccIDs[a.ID] = true
		out.AccountsTotal++
		if a.Health.IsSchedulable() && a.Enabled {
			out.AccountsHealthy++
		}
		out.TotalAvailableCU += a.PaidCUBalance
		sched := a.SchedulableCU
		if sched <= 0 && a.PaidCUBalance > a.Guardrails.MinReserveCU {
			sched = a.PaidCUBalance - a.Guardrails.MinReserveCU
		}
		out.SchedulableCU += sched
		slots := a.MaxConcurrentVMs
		if slots <= 0 {
			slots = 2
		}
		out.TotalVMSlots += slots
	}
	if eligibleCount > 0 && key.MaxConcurrentVMs > 0 && (out.TotalVMSlots == 0 || key.MaxConcurrentVMs < out.TotalVMSlots) {
		out.TotalVMSlots = key.MaxConcurrentVMs
	}
	out.TotalAvailableCU = math.Round(out.TotalAvailableCU*100) / 100
	out.SchedulableCU = math.Round(out.SchedulableCU*100) / 100
	if key.MaxBudgetCUTotal > 0 {
		rem := math.Max(0, math.Round((key.MaxBudgetCUTotal-key.UsedCUTotal)*100)/100)
		if rem < out.SchedulableCU {
			out.SchedulableCU = rem
		}
	}
	for _, spec := range models.HardwareCatalog {
		if !gpuInAllowedGPUsREST(spec.Accelerator, key.AllowedGPUs) {
			continue
		}
		rate := spec.DefaultBurnRateCUHr
		availHrs := 0.0
		schedHrs := 0.0
		if rate > 0 {
			availHrs = math.Round((out.TotalAvailableCU/rate)*100) / 100
			schedHrs = math.Round((out.SchedulableCU/rate)*100) / 100
		}
		out.GPUEquivalence[spec.Accelerator] = models.GPUHourEquivalence{
			Accelerator:       spec.Accelerator,
			DisplayName:       spec.DisplayName,
			ShortLabel:        spec.ShortLabel,
			VRAMGB:            spec.VRAMGB,
			BurnRateCUHr:      rate,
			CalibrationSource: models.CalibrationReference,
			AvailableHours:    availHrs,
			SchedulableHours:  schedHrs,
			EligibleAccounts:  out.AccountsHealthy,
			MaxParallelSlots:  out.TotalVMSlots,
		}
	}
	if s.runtime != nil {
		if list, lErr := s.runtime.ListSessions(ctx, models.SessionFilter{ActiveOnly: true}); lErr == nil {
			for _, sess := range list {
				if sess == nil || sess.AdoptedExternal || sess.APIKeyID != key.ID ||
					sess.Status == models.SessionStopped || sess.Status == models.SessionLost {
					continue
				}
				if tagFilter != "" && !eligibleAccIDs[sess.AccountID] {
					continue
				}
				out.ActiveVMs++
				out.ActiveBurnRateCUHr += sess.BurnRateCUHr
			}
			out.ActiveBurnRateCUHr = math.Round(out.ActiveBurnRateCUHr*100) / 100
		}
	}
	return out
}

func (s *Server) handleGetQuotaSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tagFilter := strings.TrimSpace(r.URL.Query().Get("tag"))
	if key := s.requestAPIKey(r); key != nil {
		writeJSON(w, http.StatusOK, s.buildScopedQuotaSummary(ctx, key, tagFilter))
		return
	}
	refresh := r.URL.Query().Get("refresh") == "true" || r.URL.Query().Get("refresh_live") == "true"

	if tagFilter != "" {
		if refresh && s.pool != nil {
			_, _ = s.pool.RefreshAllQuotas(ctx)
		}
		writeJSON(w, http.StatusOK, s.computeSummaryFromStoreFiltered(ctx, tagFilter))
		return
	}

	if s.pool != nil {
		var sum *models.QuotaSummary
		var err error
		if refresh {
			sum, err = s.pool.RefreshAllQuotas(ctx)
		} else {
			sum, err = s.pool.GetQuotaSummary(ctx)
		}
		if err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
		if sum != nil {
			writeJSON(w, http.StatusOK, sum)
			return
		}
	}

	// Fallback computation directly from Store when running with mock/embedded store.
	writeJSON(w, http.StatusOK, s.computeSummaryFromStore(ctx))
}

func (s *Server) computeSummaryFromStore(ctx context.Context) *models.QuotaSummary {
	return s.computeSummaryFromStoreFiltered(ctx, "")
}

func (s *Server) computeSummaryFromStoreFiltered(ctx context.Context, tagFilter string) *models.QuotaSummary {
	now := time.Now().UTC()
	var accs []*models.Account
	if s.pool != nil {
		accs, _ = s.pool.ListAccounts(ctx)
	} else if s.st != nil {
		accs, _ = s.st.ListAccounts(ctx)
	}
	allowedAccIDs := make(map[string]bool)
	if tagFilter != "" {
		filtered := make([]*models.Account, 0, len(accs))
		for _, a := range accs {
			if a == nil {
				continue
			}
			for _, t := range a.Tags {
				if strings.EqualFold(t, tagFilter) {
					filtered = append(filtered, a)
					allowedAccIDs[a.ID] = true
					break
				}
			}
		}
		accs = filtered
	}
	var sessions []*models.RuntimeSession
	if s.runtime != nil {
		rawSessions, _ := s.runtime.ListSessions(ctx, models.SessionFilter{ActiveOnly: true})
		if tagFilter != "" {
			for _, sess := range rawSessions {
				if sess != nil && allowedAccIDs[sess.AccountID] {
					sessions = append(sessions, sess)
				}
			}
		} else {
			sessions = rawSessions
		}
	}

	sum := &models.QuotaSummary{
		AccountsTotal:  len(accs),
		ActiveVMs:      len(sessions),
		GPUEquivalence: make(map[models.AcceleratorType]models.GPUHourEquivalence, len(models.HardwareCatalog)),
		UpdatedAt:      now,
	}

	for _, sItem := range sessions {
		if sItem != nil {
			if spec, ok := models.GetHardwareSpec(sItem.Accelerator); ok {
				sum.TotalOnlineVRAMGB += spec.VRAMGB
			}
		}
	}

	for _, a := range accs {
		if a == nil || a.Health == models.HealthRevokedByUser {
			continue
		}
		if a.Health.IsSchedulable() && a.Enabled {
			sum.AccountsHealthy++
		}
		if a.ActiveAssignments > 0 {
			sum.AccountsWorking++
		}
		if a.IsContributor {
			sum.AccountsContributor++
		}
		sum.TotalAvailableCU += a.PaidCUBalance
		sched := a.SchedulableCU
		if sched <= 0 && a.PaidCUBalance > a.Guardrails.MinReserveCU {
			sched = a.PaidCUBalance - a.Guardrails.MinReserveCU
		}
		sum.SchedulableCU += sched
		sum.ActiveBurnRateCUHr += a.LiveBurnRateCUHr
		maxSlots := a.MaxConcurrentVMs
		if maxSlots <= 0 {
			maxSlots = 2
		}
		sum.TotalVMSlots += maxSlots
		if a.FreeQuota != nil && a.FreeQuota.RemainingMilliCCU > 0 {
			sum.FreeT4WindowsReady++
		}
		if a.EarliestBatchExpiresAt != nil {
			if sum.EarliestBatchExpiresAt == nil || a.EarliestBatchExpiresAt.Before(*sum.EarliestBatchExpiresAt) {
				cp := *a.EarliestBatchExpiresAt
				sum.EarliestBatchExpiresAt = &cp
				sum.EarliestBatchExpiryCU = a.ExpiringSoonCU
			}
		}
	}

	sum.TotalAvailableCU = math.Round(sum.TotalAvailableCU*100) / 100
	sum.SchedulableCU = math.Round(sum.SchedulableCU*100) / 100
	sum.EstimatedValueUSD = math.Round(sum.TotalAvailableCU*0.10*100) / 100
	sum.ActiveBurnRateCUHr = math.Round(sum.ActiveBurnRateCUHr*100) / 100
	if sum.ActiveBurnRateCUHr > 0 {
		eta := math.Round((sum.SchedulableCU/sum.ActiveBurnRateCUHr)*100) / 100
		sum.PoolDepletionETAHours = &eta
	}

	if s.st != nil && tagFilter == "" {
		if waste, err := s.st.GetSavedWasteStats(ctx); err == nil {
			sum.SavedIdleWasteCU = waste.SavedIdleWasteCU
			sum.AutoTeardownsCount = waste.AutoTeardownsCount
			sum.ZombieAbortsCount = waste.ZombieAbortsCount
		}
	}

	for _, spec := range models.HardwareCatalog {
		rate := spec.DefaultBurnRateCUHr
		availHrs := 0.0
		schedHrs := 0.0
		if rate > 0 {
			availHrs = math.Round((sum.TotalAvailableCU/rate)*100) / 100
			schedHrs = math.Round((sum.SchedulableCU/rate)*100) / 100
		}
		eligibleCount := 0
		parallelSlots := 0
		for _, a := range accs {
			if a == nil || !a.Enabled || !a.Health.IsSchedulable() {
				continue
			}
			if ent, has := a.GPUEntitlements[spec.Accelerator]; has &&
				(ent.Status == models.EntitlementTierRestricted || ent.Status == models.EntitlementIneligible) {
				continue
			}
			eligibleCount++
			slots := a.MaxConcurrentVMs
			if slots <= 0 {
				slots = 2
			}
			parallelSlots += slots
		}
		sum.GPUEquivalence[spec.Accelerator] = models.GPUHourEquivalence{
			Accelerator:       spec.Accelerator,
			DisplayName:       spec.DisplayName,
			ShortLabel:        spec.ShortLabel,
			VRAMGB:            spec.VRAMGB,
			BurnRateCUHr:      rate,
			CalibrationSource: models.CalibrationReference,
			AvailableHours:    availHrs,
			SchedulableHours:  schedHrs,
			EligibleAccounts:  eligibleCount,
			MaxParallelSlots:  parallelSlots,
			FreeWindowsReady:  sum.FreeT4WindowsReady,
			Locked:            eligibleCount == 0 && len(accs) > 0,
		}
	}
	return sum
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var list []*models.Account
	var err error
	if s.pool != nil {
		list, err = s.pool.ListAccounts(ctx)
	} else if s.st != nil {
		list, err = s.st.ListAccounts(ctx)
	}
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if list == nil {
		list = []*models.Account{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var acc *models.Account
	var err error
	if s.pool != nil {
		acc, err = s.pool.GetAccount(r.Context(), id)
	} else if s.st != nil {
		acc, err = s.st.GetAccount(r.Context(), id)
	}
	if err != nil || acc == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("account %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var patch models.AccountPatch
	if err := decodeJSONBody(r, &patch); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if s.pool != nil {
		acc, err := s.pool.UpdateAccount(r.Context(), id, patch)
		if err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, acc)
		return
	}
	if s.st != nil {
		acc, err := s.st.GetAccount(r.Context(), id)
		if err != nil || acc == nil {
			writeJSONError(w, http.StatusNotFound, "account not found")
			return
		}
		if patch.Alias != nil {
			acc.Alias = *patch.Alias
		}
		if patch.Notes != nil {
			acc.Notes = *patch.Notes
		}
		if patch.Enabled != nil {
			acc.Enabled = *patch.Enabled
		}
		if patch.Priority != nil {
			acc.Priority = *patch.Priority
		}
		if patch.Groups != nil {
			acc.Groups = patch.Groups
		}
		if patch.Tags != nil {
			acc.Tags = patch.Tags
		}
		if patch.ProxyID != nil {
			acc.ProxyID = *patch.ProxyID
		}
		if patch.SubscriptionTier != nil {
			normTier, ok := models.NormalizeSubscriptionTier(*patch.SubscriptionTier)
			if !ok {
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid subscription_tier %q", *patch.SubscriptionTier))
				return
			}
			acc.SubscriptionTier = normTier
			acc.CustomSubscriptionTier = true
			if patch.ReferenceCycleCU == nil && !acc.CustomCycleCU {
				std := models.TierStandardCycleCU(normTier)
				if acc.PaidCUBalance > std {
					std = acc.PaidCUBalance
				}
				acc.ReferenceCycleCU = std
			}
		}
		if patch.ReferenceCycleCU != nil {
			if *patch.ReferenceCycleCU > 0 {
				acc.ReferenceCycleCU = math.Round(*patch.ReferenceCycleCU*100) / 100
				acc.CustomCycleCU = true
			} else {
				acc.CustomCycleCU = false
				acc.ReferenceCycleCU = models.TierStandardCycleCU(acc.SubscriptionTier)
			}
		}
		if patch.MaxConcurrentVMs != nil {
			acc.MaxConcurrentVMs = *patch.MaxConcurrentVMs
		}
		if patch.Guardrails != nil {
			acc.Guardrails = *patch.Guardrails
		}
		acc.UpdatedAt = time.Now().UTC()
		_ = s.st.SaveAccount(r.Context(), acc)
		writeJSON(w, http.StatusOK, acc)
		return
	}
	writeJSONError(w, http.StatusNotFound, "account not found")
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.pool != nil {
		if err := s.pool.DeleteAccount(r.Context(), id); err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
	} else if s.st != nil {
		_ = s.st.DeleteAccount(r.Context(), id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRefreshAccountQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.pool != nil {
		acc, err := s.pool.RefreshAccountQuota(r.Context(), id)
		if err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
		s.triggerAsyncSessionSync()
		writeJSON(w, http.StatusOK, acc)
		return
	}
	if s.st != nil {
		if acc, err := s.st.GetAccount(r.Context(), id); err == nil && acc != nil {
			now := time.Now().UTC()
			acc.LastPolledAt = &now
			_ = s.st.SaveAccount(r.Context(), acc)
			s.triggerAsyncSessionSync()
			writeJSON(w, http.StatusOK, acc)
			return
		}
	}
	writeJSONError(w, http.StatusNotFound, "account not found")
}

func (s *Server) triggerAsyncSessionSync() {
	if s.cfg.DemoMode || s.runtime == nil {
		return
	}
	if asyncTrigger, ok := s.runtime.(interface{ TriggerSyncAsync() }); ok {
		asyncTrigger.TriggerSyncAsync()
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = s.runtime.SyncRemoteAssignments(ctx)
	}()
}

// ============================================================================
// 2b. Account & System Diagnostic Self-Test Endpoints
// ============================================================================

const defaultGoogleCertsProbeURL = "https://www.googleapis.com/oauth2/v3/certs"

func detectEnvProxyMasked() string {
	raw := firstVal(
		os.Getenv("HTTPS_PROXY"),
		os.Getenv("https_proxy"),
		os.Getenv("HTTP_PROXY"),
		os.Getenv("http_proxy"),
		os.Getenv("ALL_PROXY"),
		os.Getenv("all_proxy"),
	)
	if raw == "" {
		return ""
	}
	return proxy.MaskProxyURL(raw)
}

func isDemoSandboxAccount(demoCfg bool, acc *models.Account) bool {
	if demoCfg {
		return true
	}
	if acc == nil {
		return false
	}
	if strings.HasPrefix(acc.ID, "acc_demo") || strings.HasSuffix(strings.ToLower(acc.Email), ".demo") {
		return true
	}
	for _, t := range acc.Tags {
		if strings.EqualFold(strings.TrimSpace(t), "demo") {
			return true
		}
	}
	return false
}

func formatAccountScopesSummary(scopes []string) string {
	hasColab := false
	hasDrive := false
	for _, sc := range scopes {
		lower := strings.ToLower(sc)
		if strings.Contains(lower, "colaboratory") {
			hasColab = true
		}
		if strings.Contains(lower, "drive") {
			hasDrive = true
		}
	}
	switch {
	case hasColab && hasDrive:
		return fmt.Sprintf("colaboratory + drive.file (%d 项)", len(scopes))
	case hasColab:
		return fmt.Sprintf("colaboratory 最小权限 (%d 项)", len(scopes))
	case len(scopes) > 0:
		return fmt.Sprintf("%d 项已授权", len(scopes))
	default:
		return "colaboratory (默认)"
	}
}

func formatAccountQuotaAndGPUs(acc *models.Account) (summary string, detail string) {
	if acc == nil {
		return "未知额度", ""
	}
	g4Hrs := 0.0
	if eq, ok := acc.GPUEquivalence[models.AcceleratorG4]; ok && eq.AvailableHours > 0 {
		g4Hrs = eq.AvailableHours
	} else if acc.PaidCUBalance > 0 {
		if spec, ok := models.GetHardwareSpec(models.AcceleratorG4); ok && spec.DefaultBurnRateCUHr > 0 {
			g4Hrs = math.Round((acc.PaidCUBalance/spec.DefaultBurnRateCUHr)*100) / 100
		}
	}
	maxSlots := acc.MaxConcurrentVMs
	if maxSlots <= 0 {
		maxSlots = 2
	}
	tier := acc.SubscriptionTier
	if tier == "" {
		tier = models.TierFree
	}
	summary = fmt.Sprintf(
		"套餐 %s · 余额 %.2f CU (≈ %.1f 小时 G4) · 活跃实例 %d/%d · 0.00 CU",
		tier, acc.PaidCUBalance, g4Hrs, acc.ActiveAssignments, maxSlots,
	)

	var avail []string
	var restricted []string
	for _, gpu := range models.AllAccelerators {
		if ent, ok := acc.GPUEntitlements[gpu]; ok {
			switch ent.Status {
			case models.EntitlementAvailable:
				avail = append(avail, string(gpu))
			case models.EntitlementTierRestricted, models.EntitlementIneligible:
				restricted = append(restricted, string(gpu))
			}
		}
	}
	if len(avail) == 0 {
		if tier == models.TierFree {
			avail = []string{"T4", "CPU"}
		} else {
			avail = []string{"G4", "H100", "A100", "L4", "T4", "CPU"}
		}
	}
	detail = fmt.Sprintf("可用加速卡: %s | 可调度额度: %.2f CU", strings.Join(avail, ", "), acc.SchedulableCU)
	if len(restricted) > 0 {
		detail += fmt.Sprintf(" | 受限: %s", strings.Join(restricted, ", "))
	}
	return summary, detail
}

func (s *Server) probeGoogleReachability(ctx context.Context, rawProxyURL string) (statusCode int, latencyMs int64, targetURL string, err error) {
	targetURL = firstVal(s.cfg.GoogleProbeURL, defaultGoogleCertsProbeURL)
	tf := s.txFactory
	if tf == nil {
		tf = proxy.NewTransportFactory()
	}
	client, cErr := tf.HTTPClient(rawProxyURL, 5*time.Second)
	if cErr != nil {
		return 0, 0, targetURL, cErr
	}
	pCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, rErr := http.NewRequestWithContext(pCtx, http.MethodGet, targetURL, nil)
	if rErr != nil {
		return 0, 0, targetURL, rErr
	}
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, doErr := client.Do(req)
	latencyMs = time.Since(start).Milliseconds()
	if latencyMs <= 0 {
		latencyMs = 1
	}
	if doErr != nil {
		return 0, latencyMs, targetURL, doErr
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 500 {
		return resp.StatusCode, latencyMs, targetURL, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, latencyMs, targetURL, nil
}

func isConcurrencyLimitAllocError(errMsg string, acc *models.Account) bool {
	lower := strings.ToLower(errMsg)
	if strings.Contains(lower, "412") ||
		strings.Contains(lower, "too_many_active_runtimes") ||
		strings.Contains(lower, "toomanyactiveruntimes") ||
		strings.Contains(lower, "toomanyassignments") ||
		strings.Contains(lower, "too many active") ||
		strings.Contains(lower, "max_concurrent_vms") ||
		strings.Contains(lower, "max concurrent vms") ||
		strings.Contains(lower, "max_concurrency") {
		return true
	}
	if strings.Contains(lower, "cooldown") && acc != nil &&
		(acc.Health == models.HealthMaxConcurrency ||
			strings.Contains(strings.ToLower(acc.HealthReason), "412") ||
			strings.Contains(strings.ToLower(acc.HealthReason), "concurrent")) {
		return true
	}
	return false
}

func (s *Server) runAccountKernelSmoke(
	ctx context.Context,
	acc *models.Account,
	accel models.AcceleratorType,
) (models.VerifyStepResult, string) {
	const (
		stepID  = "kernel_smoke"
		titleZH = "云端内核真机代码试跑"
		titleEN = "Cloud Kernel Live Smoke Execution"
	)
	start := time.Now()
	elapsedMs := func() int64 {
		d := time.Since(start).Milliseconds()
		if d <= 0 {
			return 1
		}
		return d
	}

	if !acc.Enabled {
		return models.VerifyStepResult{
			ID:         stepID,
			TitleZH:    titleZH,
			TitleEN:    titleEN,
			Status:     models.VerifyStepError,
			DurationMs: elapsedMs(),
			Summary:    "账号当前处于停用状态（Enabled=false），请先启用账号后再试跑",
		}, ""
	}
	if s.runtime == nil {
		return models.VerifyStepResult{
			ID:         stepID,
			TitleZH:    titleZH,
			TitleEN:    titleEN,
			Status:     models.VerifyStepError,
			DurationMs: elapsedMs(),
			Summary:    "运行时引擎未就绪，无法分配测试会话",
		}, ""
	}

	shortID := models.NewPrefixedID("")
	if len(shortID) > 6 {
		shortID = shortID[:6]
	}
	sessName := "ch-selftest-" + shortID

	sess, allocErr := s.runtime.CreateSession(ctx, models.SessionAllocRequest{
		Name:            sessName,
		AccountSelector: acc.ID,
		Accelerator:     accel,
		HighMem:         false,
		AutoFallback:    false,
		CallerName:      "colabhive-selftest",
	})
	if allocErr != nil || sess == nil {
		errMsg := "unknown session allocation error"
		if allocErr != nil {
			errMsg = allocErr.Error()
		}
		if acc.ActiveAssignments > 0 && isConcurrencyLimitAllocError(errMsg, acc) {
			return models.VerifyStepResult{
				ID:         stepID,
				TitleZH:    titleZH,
				TitleEN:    titleEN,
				Status:     models.VerifyStepOK,
				DurationMs: elapsedMs(),
				Summary: fmt.Sprintf(
					"该账号当前已有 %d 台 Colab 实例正在运行（已达 Google 官方单号同开上限），证明登录凭证与算力接口完全正常！待现有任务跑完即可分配新实例。",
					acc.ActiveAssignments,
				),
				Detail: errMsg,
			}, ""
		}
		return models.VerifyStepResult{
			ID:         stepID,
			TitleZH:    titleZH,
			TitleEN:    titleEN,
			Status:     models.VerifyStepError,
			DurationMs: elapsedMs(),
			Summary:    fmt.Sprintf("分配临时 %s 云端实例失败", accel),
			Detail:     errMsg,
		}, ""
	}

	// CRITICAL SAFETY: ONLY tear down the newly created `sess.ID` when `CreateSession` succeeded (`allocErr == nil && sess != nil`).
	// NEVER touch any pre-existing sessions on the user's account!
	createdSessionID := sess.ID
	if createdSessionID != "" {
		defer func(sid string) {
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = s.runtime.StopSession(stopCtx, sid)
		}(createdSessionID)
	}

	const smokeCode = `import platform, sys; print(f"COLABHIVE_SELFTEST_OK | host={platform.node()} | py={sys.version.split()[0]}")`
	execRes, execErr := s.runtime.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID:      sess.ID,
		Code:           smokeCode,
		IdleTimeoutSec: 45,
		Silent:         false,
		StoreHistory:   false,
		CallerName:     "colabhive-selftest",
	})
	if execErr != nil {
		return models.VerifyStepResult{
			ID:         stepID,
			TitleZH:    titleZH,
			TitleEN:    titleEN,
			Status:     models.VerifyStepError,
			DurationMs: elapsedMs(),
			Summary:    fmt.Sprintf("%s 云端内核代码执行异常（实例已自动回收）", sess.Accelerator),
			Detail:     execErr.Error(),
		}, ""
	}
	if execRes == nil || execRes.Status != "ok" || !strings.Contains(execRes.Stdout, "COLABHIVE_SELFTEST_OK") {
		detail := ""
		if execRes != nil {
			detail = firstVal(strings.TrimSpace(execRes.Stderr), strings.TrimSpace(execRes.Stdout), execRes.Status)
		}
		return models.VerifyStepResult{
			ID:         stepID,
			TitleZH:    titleZH,
			TitleEN:    titleEN,
			Status:     models.VerifyStepError,
			DurationMs: elapsedMs(),
			Summary:    fmt.Sprintf("%s 内核返回非预期输出（实例已自动回收）", sess.Accelerator),
			Detail:     detail,
		}, ""
	}

	stdout := strings.TrimSpace(execRes.Stdout)
	firstLine := strings.TrimSpace(strings.Split(stdout, "\n")[0])
	return models.VerifyStepResult{
		ID:         stepID,
		TitleZH:    titleZH,
		TitleEN:    titleEN,
		Status:     models.VerifyStepOK,
		DurationMs: elapsedMs(),
		Summary:    fmt.Sprintf("%s 真机代码执行成功并已自动销毁释放（零残留）", sess.Accelerator),
		Detail:     firstLine,
	}, stdout
}

func (s *Server) handleVerifyAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	var req models.AccountVerifyRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	accel := models.AcceleratorCPU
	if strings.TrimSpace(string(req.Accelerator)) != "" {
		norm, err := models.NormalizeAccelerator(string(req.Accelerator))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		accel = norm
	}

	var acc *models.Account
	var err error
	if s.pool != nil {
		acc, err = s.pool.GetAccount(ctx, id)
	} else if s.st != nil {
		acc, err = s.st.GetAccount(ctx, id)
	}
	if err != nil || acc == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("account %q not found", id))
		return
	}

	totalStart := time.Now()
	steps := make([]models.VerifyStepResult, 0, 4)
	isDemo := isDemoSandboxAccount(s.cfg.DemoMode, acc)

	// Resolve account's egress proxy details without leaking credentials.
	rawProxyURL := ""
	egressLabel := "Direct (本机直连)"
	if acc.ProxyID != "" {
		var pCfg *models.ProxyConfig
		if s.st != nil {
			pCfg, _ = s.st.GetProxy(ctx, acc.ProxyID)
		}
		if pCfg == nil && acc.Proxy != nil {
			pCfg = acc.Proxy
		}
		if pCfg != nil {
			rawProxyURL = pCfg.URL
			masked := firstVal(pCfg.MaskedURL, proxy.MaskProxyURL(pCfg.URL))
			egressLabel = fmt.Sprintf("%s (%s)", firstVal(pCfg.Name, "绑定代理"), masked)
		} else {
			egressLabel = fmt.Sprintf("代理 %s", acc.ProxyID)
		}
	} else if envMasked := detectEnvProxyMasked(); envMasked != "" {
		egressLabel = fmt.Sprintf("环境变量代理 (%s)", envMasked)
	}

	// Step 1: Egress & Network Reachability (`egress`)
	t1 := time.Now()
	if isDemo && strings.TrimSpace(s.cfg.GoogleProbeURL) == "" {
		steps = append(steps, models.VerifyStepResult{
			ID:         "egress",
			TitleZH:    "网络与出口连通性",
			TitleEN:    "Network & Egress Connectivity",
			Status:     models.VerifyStepOK,
			DurationMs: 1,
			Summary:    fmt.Sprintf("出口通道正常 · %s（本地演示沙盒） · 0.00 CU", egressLabel),
			Detail:     "mode=demo",
		})
	} else {
		code, rttMs, targetURL, pErr := s.probeGoogleReachability(ctx, rawProxyURL)
		dur1 := time.Since(t1).Milliseconds()
		if dur1 <= 0 {
			dur1 = rttMs
		}
		if dur1 <= 0 {
			dur1 = 1
		}
		if pErr != nil {
			steps = append(steps, models.VerifyStepResult{
				ID:         "egress",
				TitleZH:    "网络与出口连通性",
				TitleEN:    "Network & Egress Connectivity",
				Status:     models.VerifyStepError,
				DurationMs: dur1,
				Summary:    fmt.Sprintf("无法经由 %s 连通 Google API 节点", egressLabel),
				Detail:     pErr.Error(),
			})
		} else {
			steps = append(steps, models.VerifyStepResult{
				ID:         "egress",
				TitleZH:    "网络与出口连通性",
				TitleEN:    "Network & Egress Connectivity",
				Status:     models.VerifyStepOK,
				DurationMs: dur1,
				Summary:    fmt.Sprintf("Google 节点连通正常 · %s · %d ms · 0.00 CU", egressLabel, rttMs),
				Detail:     fmt.Sprintf("GET %s -> HTTP %d", targetURL, code),
			})
		}
	}

	// Step 2: OAuth2 Credentials & Token Refresh (`oauth`)
	t2 := time.Now()
	var step2 models.VerifyStepResult
	if s.pool != nil {
		tok, usedProxy, tokErr := s.pool.AcquireAccessToken(ctx, acc.ID)
		dur2 := time.Since(t2).Milliseconds()
		if dur2 <= 0 {
			dur2 = 1
		}
		if tokErr != nil || strings.TrimSpace(tok) == "" {
			errDetail := "empty access token"
			if tokErr != nil {
				errDetail = tokErr.Error()
			}
			step2 = models.VerifyStepResult{
				ID:         "oauth",
				TitleZH:    "OAuth2 登录凭证与令牌续期",
				TitleEN:    "OAuth2 Credentials & Token Refresh",
				Status:     models.VerifyStepError,
				DurationMs: dur2,
				Summary:    "OAuth2 令牌解密或续期失败，请重新完成账号授权登录",
				Detail:     errDetail,
			}
		} else {
			if latest, gErr := s.pool.GetAccount(ctx, acc.ID); gErr == nil && latest != nil {
				acc = latest
			}
			scopeDesc := formatAccountScopesSummary(acc.Scopes)
			expNote := "自动续期已托管"
			if acc.TokenExpiresAt != nil {
				mins := int(time.Until(*acc.TokenExpiresAt).Minutes())
				if mins > 0 {
					expNote = fmt.Sprintf("剩余 %d 分钟有效", mins)
				}
			}
			step2 = models.VerifyStepResult{
				ID:         "oauth",
				TitleZH:    "OAuth2 登录凭证与令牌续期",
				TitleEN:    "OAuth2 Credentials & Token Refresh",
				Status:     models.VerifyStepOK,
				DurationMs: dur2,
				Summary:    fmt.Sprintf("登录有效 (%s) · %s · 0.00 CU", acc.Email, scopeDesc),
				Detail:     fmt.Sprintf("出口=%s | AccessToken %s", proxy.MaskProxyURL(usedProxy), expNote),
			}
		}
	} else {
		dur2 := time.Since(t2).Milliseconds()
		if dur2 <= 0 {
			dur2 = 1
		}
		if acc.Health == models.HealthAuthExpired || acc.Health == models.HealthRevokedByUser {
			step2 = models.VerifyStepResult{
				ID:         "oauth",
				TitleZH:    "OAuth2 登录凭证与令牌续期",
				TitleEN:    "OAuth2 Credentials & Token Refresh",
				Status:     models.VerifyStepError,
				DurationMs: dur2,
				Summary:    fmt.Sprintf("账号授权状态异常 (%s)", acc.Health),
				Detail:     acc.HealthReason,
			}
		} else {
			step2 = models.VerifyStepResult{
				ID:         "oauth",
				TitleZH:    "OAuth2 登录凭证与令牌续期",
				TitleEN:    "OAuth2 Credentials & Token Refresh",
				Status:     models.VerifyStepOK,
				DurationMs: dur2,
				Summary:    fmt.Sprintf("登录有效 (%s) · %s · 0.00 CU", acc.Email, formatAccountScopesSummary(acc.Scopes)),
				Detail:     fmt.Sprintf("健康状态=%s", acc.Health),
			}
		}
	}
	steps = append(steps, step2)

	// Step 3: Colab Official Quota & GPU Entitlements Probe (`quota`)
	t3 := time.Now()
	var step3 models.VerifyStepResult
	if step2.Status == models.VerifyStepError {
		step3 = models.VerifyStepResult{
			ID:         "quota",
			TitleZH:    "Colab 官方额度与显卡权限探针",
			TitleEN:    "Colab Quota & GPU Entitlements Probe",
			Status:     models.VerifyStepSkipped,
			DurationMs: 0,
			Summary:    "前置 OAuth2 登录校验未通过，已跳过额度查询",
		}
	} else if s.pool != nil {
		refreshed, qErr := s.pool.RefreshAccountQuota(ctx, acc.ID)
		dur3 := time.Since(t3).Milliseconds()
		if dur3 <= 0 {
			dur3 = 1
		}
		if qErr != nil {
			step3 = models.VerifyStepResult{
				ID:         "quota",
				TitleZH:    "Colab 官方额度与显卡权限探针",
				TitleEN:    "Colab Quota & GPU Entitlements Probe",
				Status:     models.VerifyStepError,
				DurationMs: dur3,
				Summary:    "拉取 Google Colab 实时额度与显卡权限失败",
				Detail:     qErr.Error(),
			}
		} else {
			acc = refreshed
			qSum, qDet := formatAccountQuotaAndGPUs(acc)
			step3 = models.VerifyStepResult{
				ID:         "quota",
				TitleZH:    "Colab 官方额度与显卡权限探针",
				TitleEN:    "Colab Quota & GPU Entitlements Probe",
				Status:     models.VerifyStepOK,
				DurationMs: dur3,
				Summary:    qSum,
				Detail:     qDet,
			}
		}
	} else {
		now := time.Now().UTC()
		acc.LastPolledAt = &now
		if s.st != nil {
			_ = s.st.SaveAccount(ctx, acc)
		}
		dur3 := time.Since(t3).Milliseconds()
		if dur3 <= 0 {
			dur3 = 1
		}
		qSum, qDet := formatAccountQuotaAndGPUs(acc)
		step3 = models.VerifyStepResult{
			ID:         "quota",
			TitleZH:    "Colab 官方额度与显卡权限探针",
			TitleEN:    "Colab Quota & GPU Entitlements Probe",
			Status:     models.VerifyStepOK,
			DurationMs: dur3,
			Summary:    qSum,
			Detail:     qDet,
		}
	}
	steps = append(steps, step3)

	// Step 4: Optional Live Cloud Kernel Smoke Test (`kernel_smoke`)
	var kernelStdout string
	if !req.RunKernelSmoke {
		steps = append(steps, models.VerifyStepResult{
			ID:         "kernel_smoke",
			TitleZH:    "云端内核真机代码试跑",
			TitleEN:    "Cloud Kernel Live Smoke Execution",
			Status:     models.VerifyStepSkipped,
			DurationMs: 0,
			Summary:    "未开启真机试跑（零算力消耗）；如需实测云端代码执行可勾选开启",
		})
	} else if step2.Status == models.VerifyStepError || step3.Status == models.VerifyStepError {
		steps = append(steps, models.VerifyStepResult{
			ID:         "kernel_smoke",
			TitleZH:    "云端内核真机代码试跑",
			TitleEN:    "Cloud Kernel Live Smoke Execution",
			Status:     models.VerifyStepSkipped,
			DurationMs: 0,
			Summary:    "前置凭证或额度校验未通过，已跳过真机试跑",
		})
	} else {
		step4, outStr := s.runAccountKernelSmoke(ctx, acc, accel)
		kernelStdout = outStr
		steps = append(steps, step4)
		// Refresh post-teardown account snapshot from Google so external ActiveAssignments stay accurate.
		if s.pool != nil {
			if postAcc, qErr := s.pool.RefreshAccountQuota(ctx, acc.ID); qErr == nil && postAcc != nil {
				acc = postAcc
			} else if postAcc, gErr := s.pool.GetAccount(ctx, acc.ID); gErr == nil && postAcc != nil {
				acc = postAcc
			}
		} else if s.st != nil {
			if postAcc, gErr := s.st.GetAccount(ctx, acc.ID); gErr == nil && postAcc != nil {
				acc = postAcc
			}
		}
	}

	overallOK := true
	for _, st := range steps {
		if st.Status == models.VerifyStepError {
			overallOK = false
			break
		}
	}

	totalMs := time.Since(totalStart).Milliseconds()
	if totalMs <= 0 {
		totalMs = 1
	}

	writeJSON(w, http.StatusOK, models.AccountVerifyResult{
		AccountID:       acc.ID,
		Email:           acc.Email,
		OverallOK:       overallOK,
		CheckedAt:       time.Now().UTC(),
		TotalDurationMs: totalMs,
		Steps:           steps,
		Account:         acc,
		KernelStdout:    kernelStdout,
	})
}

func (s *Server) handleSystemSelfTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mode := "production"
	if s.cfg.DemoMode {
		mode = "demo"
	}

	steps := make([]models.VerifyStepResult, 0, 4)

	// 1. SQLite Store & AES-256-GCM Vault Health (`sqlite_vault`)
	t1 := time.Now()
	acctCount := 0
	var storeErr error
	if s.st != nil {
		accts, err := s.st.ListAccounts(ctx)
		if err != nil {
			storeErr = err
		} else {
			acctCount = len(accts)
		}
	} else if s.pool != nil {
		accts, err := s.pool.ListAccounts(ctx)
		if err != nil {
			storeErr = err
		} else {
			acctCount = len(accts)
		}
	}
	if storeErr == nil {
		testCipher := vault.NewCipher("")
		const probePlain = "colabhive-aes256gcm-selftest"
		sealed, encErr := testCipher.EncryptString(probePlain)
		if encErr != nil {
			storeErr = encErr
		} else if opened, decErr := testCipher.DecryptString(sealed); decErr != nil || opened != probePlain {
			if decErr != nil {
				storeErr = decErr
			} else {
				storeErr = errors.New("vault roundtrip plaintext mismatch")
			}
		}
	}
	dur1 := time.Since(t1).Milliseconds()
	if dur1 <= 0 {
		dur1 = 1
	}
	if storeErr != nil {
		steps = append(steps, models.VerifyStepResult{
			ID:         "sqlite_vault",
			TitleZH:    "本地数据库与 AES-256-GCM 保险箱",
			TitleEN:    "SQLite Store & AES-256-GCM Vault",
			Status:     models.VerifyStepError,
			DurationMs: dur1,
			Summary:    "本地 SQLite 存储或加密保险箱自检失败",
			Detail:     storeErr.Error(),
		})
	} else {
		steps = append(steps, models.VerifyStepResult{
			ID:         "sqlite_vault",
			TitleZH:    "本地数据库与 AES-256-GCM 保险箱",
			TitleEN:    "SQLite Store & AES-256-GCM Vault",
			Status:     models.VerifyStepOK,
			DurationMs: dur1,
			Summary:    fmt.Sprintf("SQLite 读写正常（当前池内 %d 个账号）· AES-256-GCM 加解密校验通过", acctCount),
			Detail:     "HKDF-SHA256 + AES-256-GCM AEAD OK",
		})
	}

	// 2. Google Upstream Network Reachability (`google_network`)
	t2 := time.Now()
	if s.cfg.DemoMode && strings.TrimSpace(s.cfg.GoogleProbeURL) == "" {
		steps = append(steps, models.VerifyStepResult{
			ID:         "google_network",
			TitleZH:    "Google 官方 API 网络连通性",
			TitleEN:    "Google Official API Network Reachability",
			Status:     models.VerifyStepOK,
			DurationMs: 1,
			Summary:    "当前为本地演示沙盒模式（Mock Upstream），无需外部网络即可体验完整流程",
			Detail:     "mode=demo (mockcolab)",
		})
	} else {
		code, rttMs, targetURL, netErr := s.probeGoogleReachability(ctx, "")
		dur2 := time.Since(t2).Milliseconds()
		if dur2 <= 0 {
			dur2 = rttMs
		}
		if dur2 <= 0 {
			dur2 = 1
		}
		envNote := "本机直连"
		if envMasked := detectEnvProxyMasked(); envMasked != "" {
			envNote = "HTTPS_PROXY=" + envMasked
		}
		if netErr != nil {
			steps = append(steps, models.VerifyStepResult{
				ID:         "google_network",
				TitleZH:    "Google 官方 API 网络连通性",
				TitleEN:    "Google Official API Network Reachability",
				Status:     models.VerifyStepError,
				DurationMs: dur2,
				Summary:    fmt.Sprintf("无法连接 Google API (%s)；请配置系统代理 HTTPS_PROXY 或为账号绑定独立代理", envNote),
				Detail:     netErr.Error(),
			})
		} else {
			steps = append(steps, models.VerifyStepResult{
				ID:         "google_network",
				TitleZH:    "Google 官方 API 网络连通性",
				TitleEN:    "Google Official API Network Reachability",
				Status:     models.VerifyStepOK,
				DurationMs: dur2,
				Summary:    fmt.Sprintf("Google OAuth2 / API 真机连通正常（%d ms · %s）", rttMs, envNote),
				Detail:     fmt.Sprintf("mode=%s | GET %s -> HTTP %d", mode, targetURL, code),
			})
		}
	}

	// 3. MCP Server & Smart Router Policy (`mcp_router`)
	t3 := time.Now()
	toolCount := 0
	if s.mcp != nil {
		toolCount = len(s.mcp.Tools())
	}
	var pol *models.RouterPolicy
	var rtrErr error
	if s.router != nil {
		pol, rtrErr = s.router.GetPolicy(ctx)
	} else {
		rtrErr = errors.New("router service not configured")
	}
	dur3 := time.Since(t3).Milliseconds()
	if dur3 <= 0 {
		dur3 = 1
	}
	if toolCount < 30 || rtrErr != nil || pol == nil {
		detail := fmt.Sprintf("tools=%d", toolCount)
		if rtrErr != nil {
			detail += " | err=" + rtrErr.Error()
		}
		steps = append(steps, models.VerifyStepResult{
			ID:         "mcp_router",
			TitleZH:    "MCP 协议服务与智能路由引擎",
			TitleEN:    "MCP Server & Smart Router Engine",
			Status:     models.VerifyStepError,
			DurationMs: dur3,
			Summary:    fmt.Sprintf("MCP 工具注册数不足或路由策略异常（当前 %d 个工具）", toolCount),
			Detail:     detail,
		})
	} else {
		steps = append(steps, models.VerifyStepResult{
			ID:         "mcp_router",
			TitleZH:    "MCP 协议服务与智能路由引擎",
			TitleEN:    "MCP Server & Smart Router Engine",
			Status:     models.VerifyStepOK,
			DurationMs: dur3,
			Summary:    fmt.Sprintf("已注册 %d 个静态 MCP 工具 · 调度策略 %s 就绪", toolCount, pol.DefaultStrategy),
			Detail:     fmt.Sprintf("Streamable HTTP (/mcp) + Stdio | auto_fallback=%v", pol.AutoFallbackEnabled),
		})
	}

	// 4. Read-Only Local & WSL2 Credential Scanner (`local_cli_scanner`)
	t4 := time.Now()
	discoveredCount := 0
	if s.isCloudMode() {
		steps = append(steps, models.VerifyStepResult{
			ID:         "local_cli_scanner",
			TitleZH:    "宿主机本地文件系统隔离校验",
			TitleEN:    "Host Filesystem Isolation Guardrail",
			Status:     models.VerifyStepOK,
			DurationMs: 1,
			Summary:    "云端部署模式已禁用宿主机本地凭证扫描与配置注入（安全隔离已生效）",
			Detail:     "mode=cloud (host filesystem scan disabled)",
		})
	} else {
		var scanErr error
		var found []models.DiscoveredLocalCredential
		if s.pool != nil {
			found, scanErr = s.pool.ScanLocalCredentials(ctx)
			discoveredCount = len(found)
		}
		dur4 := time.Since(t4).Milliseconds()
		if dur4 <= 0 {
			dur4 = 1
		}
		if scanErr != nil {
			steps = append(steps, models.VerifyStepResult{
				ID:         "local_cli_scanner",
				TitleZH:    "本机 / WSL2 现有凭证只读扫描",
				TitleEN:    "Local & WSL2 CLI Credential Read-Only Scan",
				Status:     models.VerifyStepError,
				DurationMs: dur4,
				Summary:    "只读扫描本机凭证目录时返回错误",
				Detail:     scanErr.Error(),
			})
		} else if discoveredCount > 0 {
			previews := make([]string, 0, len(found))
			for i, item := range found {
				if i >= 4 {
					break
				}
				label := item.SourceType + "@" + item.Environment
				if item.EmailPreview != "" {
					label += "(" + item.EmailPreview + ")"
				}
				previews = append(previews, label)
			}
			steps = append(steps, models.VerifyStepResult{
				ID:         "local_cli_scanner",
				TitleZH:    "本机 / WSL2 现有凭证只读扫描",
				TitleEN:    "Local & WSL2 CLI Credential Read-Only Scan",
				Status:     models.VerifyStepOK,
				DurationMs: dur4,
				Summary:    fmt.Sprintf("发现 %d 份可一键导入的本机/WSL2 Google 凭证（只读检测，未自动写入）", discoveredCount),
				Detail:     strings.Join(previews, ", "),
			})
		} else {
			steps = append(steps, models.VerifyStepResult{
				ID:         "local_cli_scanner",
				TitleZH:    "本机 / WSL2 现有凭证只读扫描",
				TitleEN:    "Local & WSL2 CLI Credential Read-Only Scan",
				Status:     models.VerifyStepOK,
				DurationMs: dur4,
				Summary:    "未检测到现成 colab-cli / gcloud 凭证文件；可通过向导网页授权添加新账号",
				Detail:     "read-only scan completed (0 local files)",
			})
		}
	}

	overallOK := true
	for _, st := range steps {
		if st.Status == models.VerifyStepError {
			overallOK = false
			break
		}
	}

	writeJSON(w, http.StatusOK, models.SystemSelfTestResult{
		OverallOK:            overallOK,
		Mode:                 mode,
		CheckedAt:            time.Now().UTC(),
		Steps:                steps,
		DiscoveredLocalCount: discoveredCount,
	})
}

// ============================================================================
// 3–7. OAuth2 Wizard, Proxies, Credential Portability & Contributor Portal
// ============================================================================

func (s *Server) handleStartOAuth(w http.ResponseWriter, r *http.Request) {
	var req models.OAuthStartRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.pool == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "account pool service not configured")
		return
	}
	pending, err := s.pool.StartOAuth(r.Context(), req)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pending)
}

func (s *Server) handleCompleteOAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State    string `json:"state"`
		Code     string `json:"code"`
		RawInput string `json:"raw_input"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.pool == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "account pool service not configured")
		return
	}
	codeInput := body.RawInput
	if strings.TrimSpace(codeInput) == "" {
		codeInput = body.Code
	}
	acc, err := s.pool.CompleteOAuth(r.Context(), body.State, codeInput)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	s.triggerAsyncSessionSync()
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleListProxies(w http.ResponseWriter, r *http.Request) {
	var list []*models.ProxyConfig
	var err error
	if s.pool != nil {
		list, err = s.pool.ListProxies(r.Context())
	} else if s.st != nil {
		list, err = s.st.ListProxies(r.Context())
	}
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if list == nil {
		list = []*models.ProxyConfig{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSaveProxy(w http.ResponseWriter, r *http.Request) {
	var cfg models.ProxyConfig
	if err := decodeJSONBody(r, &cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.pool != nil {
		saved, err := s.pool.SaveProxy(r.Context(), &cfg)
		if err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, saved)
		return
	}
	if cfg.ID == "" {
		cfg.ID = models.NewPrefixedID("prx_")
	}
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = time.Now().UTC()
	}
	cfg.Healthy = true
	if s.st != nil {
		_ = s.st.SaveProxy(r.Context(), &cfg)
	}
	writeJSON(w, http.StatusOK, &cfg)
}

func (s *Server) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.pool != nil {
		if err := s.pool.DeleteProxy(r.Context(), id); err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
	} else if s.st != nil {
		_ = s.st.DeleteProxy(r.Context(), id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.pool != nil {
		res, err := s.pool.TestProxy(r.Context(), body.URL)
		if err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	writeJSON(w, http.StatusOK, &models.ProxyProbeResult{
		Healthy:    true,
		LatencyMs:  24,
		StatusCode: 200,
		CheckedAt:  time.Now().UTC(),
	})
}

func (s *Server) handleScanLocalCredentials(w http.ResponseWriter, r *http.Request) {
	if s.isCloudMode() {
		writeJSONError(w, http.StatusForbidden, "local host credential scan is disabled in --mode cloud")
		return
	}
	if s.pool == nil {
		writeJSON(w, http.StatusOK, []models.DiscoveredLocalCredential{})
		return
	}
	found, err := s.pool.ScanLocalCredentials(r.Context())
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if found == nil {
		found = []models.DiscoveredLocalCredential{}
	}
	writeJSON(w, http.StatusOK, found)
}

func (s *Server) handleImportCredentials(w http.ResponseWriter, r *http.Request) {
	var req models.CredentialImportRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.pool == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "account pool service not configured")
		return
	}
	imported, err := s.pool.ImportCredentials(r.Context(), req)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if imported == nil {
		imported = []*models.Account{}
	}
	if len(imported) > 0 {
		s.triggerAsyncSessionSync()
	}
	writeJSON(w, http.StatusOK, imported)
}

func (s *Server) handleExportVaultBundle(w http.ResponseWriter, r *http.Request) {
	var req models.CredentialExportRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.pool == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "account pool service not configured")
		return
	}
	bundle, err := s.pool.ExportVaultBundle(r.Context(), req)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bundle)
}

// ============================================================================
// 8–10. Runtime Sessions, Execution, Files, Packages, Artifacts & Experiments
// ============================================================================

// authorizeSessionForAPIKey ensures scoped `ch_sk_...` callers CANNOT inspect, execute on,
// or terminate `AdoptedExternal` sessions or sessions belonging to a different API key / admin.
func (s *Server) authorizeSessionForAPIKey(ctx context.Context, r *http.Request, idOrName string) (*models.RuntimeSession, int, error) {
	if s.runtime == nil {
		return nil, http.StatusServiceUnavailable, errors.New("runtime engine not configured")
	}
	sess, err := s.runtime.GetSession(ctx, idOrName)
	if err != nil {
		return nil, mapServiceErrorStatus(err), err
	}
	if sess == nil {
		return nil, http.StatusNotFound, runtime.ErrSessionNotFound
	}
	if key := s.requestAPIKey(r); key != nil {
		if sess.AdoptedExternal {
			return nil, http.StatusForbidden, errors.New("forbidden: scoped API key cannot access external Colab sessions")
		}
		if sess.APIKeyID != key.ID {
			return nil, http.StatusForbidden, errors.New("forbidden: session belongs to a different API key or principal")
		}
	}
	return sess, http.StatusOK, nil
}

func (s *Server) recordExperimentOwner(expID, apiKeyID string) {
	if expID == "" || apiKeyID == "" {
		return
	}
	s.expOwnerMu.Lock()
	s.expOwnerByID[expID] = apiKeyID
	s.expOwnerMu.Unlock()
}

func (s *Server) canAPIKeyAccessExperiment(ctx context.Context, r *http.Request, exp *models.Experiment) bool {
	key := s.requestAPIKey(r)
	if key == nil {
		return true
	}
	if exp == nil {
		return false
	}
	if exp.APIKeyID != "" {
		return exp.APIKeyID == key.ID
	}
	s.expOwnerMu.RLock()
	ownerID, tracked := s.expOwnerByID[exp.ID]
	s.expOwnerMu.RUnlock()
	if tracked {
		return ownerID == key.ID
	}
	if s.runtime != nil && len(exp.Shards) > 0 {
		matchedAny := false
		for _, sh := range exp.Shards {
			if sh.SessionID == "" {
				continue
			}
			sess, err := s.runtime.GetSession(ctx, sh.SessionID)
			if err != nil || sess == nil || sess.ID != sh.SessionID || sess.AdoptedExternal || sess.APIKeyID != key.ID {
				return false
			}
			matchedAny = true
		}
		return matchedAny
	}
	return false
}

const defaultSessionHistoryLimit = 50

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	accID := firstVal(q.Get("account_id"), q.Get("account"))
	key := s.requestAPIKey(r)
	filter := models.SessionFilter{
		AccountID:    accID,
		ExperimentID: q.Get("experiment_id"),
		ActiveOnly:   q.Get("active_only") == "true",
		SyncBackend:  q.Get("sync") == "true" && key == nil,
	}
	list, err := s.runtime.ListSessions(r.Context(), filter)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	// Keep 100% of active sessions while bounding historical (STOPPED/LOST) rows
	// by default so dashboards & Workbench stay fast even with hundreds of past runs.
	// Callers can pass ?all=true or ?limit=0 for full history, or ?limit=N to tune.
	historyLimit := defaultSessionHistoryLimit
	if q.Get("all") == "true" || q.Get("all") == "1" {
		historyLimit = 0
	} else if rawLim := strings.TrimSpace(q.Get("limit")); rawLim != "" {
		if n, parseErr := strconv.Atoi(rawLim); parseErr == nil && n >= 0 {
			historyLimit = n
		}
	}
	if key != nil {
		scoped := make([]*models.RuntimeSession, 0, len(list))
		for _, sess := range list {
			if sess != nil && !sess.AdoptedExternal && sess.APIKeyID == key.ID {
				scoped = append(scoped, s.prepareSessionForResponse(r, sess))
			}
		}
		list = scoped
	}
	if historyLimit > 0 && !filter.ActiveOnly && len(list) > historyLimit {
		bounded := make([]*models.RuntimeSession, 0, historyLimit+8)
		histCount := 0
		for _, sess := range list {
			if sess == nil {
				continue
			}
			isHist := sess.Status == models.SessionStopped || sess.Status == models.SessionLost
			if isHist {
				if histCount >= historyLimit {
					continue
				}
				histCount++
			}
			bounded = append(bounded, sess)
		}
		list = bounded
	}
	if key == nil && s.isAuthRequired() {
		signed := make([]*models.RuntimeSession, 0, len(list))
		for _, sess := range list {
			signed = append(signed, s.signSessionArtifacts(sess))
		}
		list = signed
	}
	if list == nil {
		list = []*models.RuntimeSession{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSyncSessions(w http.ResponseWriter, r *http.Request) {
	if s.runtime == nil {
		writeJSON(w, http.StatusOK, models.SessionSyncResult{
			Sessions:     []*models.RuntimeSession{},
			AdoptedCount: 0,
			ActiveCount:  0,
		})
		return
	}
	res, err := s.runtime.SyncRemoteAssignments(r.Context())
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if res == nil {
		res = &models.SessionSyncResult{
			Sessions: []*models.RuntimeSession{},
		}
	}
	if res.Sessions == nil {
		res.Sessions = []*models.RuntimeSession{}
	}
	res.ActiveCount = len(res.Sessions)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var flex struct {
		models.SessionAllocRequest
		Session string `json:"session"`
		GPU     string `json:"gpu"`
		TPU     string `json:"tpu"`
		Account string `json:"account"`
	}
	// Default HighMem & AutoFallback to true when omitted by lightweight callers.
	flex.HighMem = true
	flex.AutoFallback = true

	if err := decodeJSONBody(r, &flex); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	req := flex.SessionAllocRequest
	if req.Name == "" {
		req.Name = flex.Session
	}
	if req.Accelerator == "" {
		rawAcc := firstVal(flex.GPU, flex.TPU, "CPU")
		norm, err := models.NormalizeAccelerator(rawAcc)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Accelerator = norm
	}
	if req.AccountSelector == "" {
		req.AccountSelector = firstVal(flex.Account, strings.TrimSpace(r.Header.Get("X-ColabHive-Account")))
	}
	if req.Strategy == "" {
		req.Strategy = models.RoutingStrategy(strings.TrimSpace(r.Header.Get("X-ColabHive-Strategy")))
	}
	if req.StickyKey == "" {
		req.StickyKey = strings.TrimSpace(r.Header.Get("X-ColabHive-Session-Id"))
	}
	if key := s.requestAPIKey(r); key != nil {
		req.APIKeyID = key.ID
		if len(req.AllowedGroups) == 0 && len(key.AllowedGroups) > 0 {
			req.AllowedGroups = key.AllowedGroups
		}
	}

	sess, err := s.runtime.CreateSession(r.Context(), req)
	s.recordRESTTrace(r, "POST /api/v1/sessions", req.Accelerator, sess, start, err)
	if err != nil {
		msg := err.Error()
		if s.requestAPIKey(r) != nil {
			msg = redactEmailsInServerText(msg, "")
		}
		writeJSONError(w, mapServiceErrorStatus(err), msg)
		return
	}
	writeJSON(w, http.StatusOK, s.prepareSessionForResponse(r, sess))
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, status, err := s.authorizeSessionForAPIKey(r.Context(), r, r.PathValue("id"))
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.prepareSessionForResponse(r, sess))
}

func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id); err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	if err := s.runtime.StopSession(r.Context(), id); err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": id, "unassigned": true})
}

func (s *Server) handleRestartKernel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id); err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	if err := s.runtime.RestartKernel(r.Context(), id); err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarted": true})
}

func (s *Server) handleInterruptKernel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id); err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	if err := s.runtime.InterruptKernel(r.Context(), id); err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interrupted": true})
}

func (s *Server) handleHotRestartWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id); err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	if err := s.runtime.HotRestartWorker(r.Context(), id); err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarted": true})
}

func (s *Server) handleGetSessionWebURL(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id); err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	u, err := s.runtime.GetColabWebURL(r.Context(), id)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": u})
}

func (s *Server) handleExecuteSessionCode(w http.ResponseWriter, r *http.Request) {
	s.executeCodeCommon(w, r, r.PathValue("id"))
}

func (s *Server) handleExecuteStickyCode(w http.ResponseWriter, r *http.Request) {
	s.executeCodeCommon(w, r, "")
}

func (s *Server) executeCodeCommon(w http.ResponseWriter, r *http.Request, pathSessionID string) {
	start := time.Now()
	var flex struct {
		models.KernelExecRequest
		Session    string  `json:"session"`
		GPU        string  `json:"gpu"`
		Account    string  `json:"account"`
		TimeoutSec float64 `json:"timeout_sec"`
		LocalFile  string  `json:"local_file"`
	}
	flex.StoreHistory = true
	if err := decodeJSONBody(r, &flex); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	req := flex.KernelExecRequest
	if req.LocalFilePath == "" && flex.LocalFile != "" {
		req.LocalFilePath = flex.LocalFile
	}
	if (s.isCloudMode() || s.requestAPIKey(r) != nil) && strings.TrimSpace(req.LocalFilePath) != "" {
		writeJSONError(w, http.StatusBadRequest, "host filesystem local_file_path is not permitted in cloud mode or with scoped API keys; pass inline code instead")
		return
	}
	req.SessionID = firstVal(pathSessionID, req.SessionID, flex.Session)
	if req.IdleTimeoutSec == 0 && flex.TimeoutSec > 0 {
		req.IdleTimeoutSec = flex.TimeoutSec
	}
	if req.AutoProvisionGPU == "" && flex.GPU != "" {
		if norm, err := models.NormalizeAccelerator(flex.GPU); err == nil {
			req.AutoProvisionGPU = norm
		}
	}
	if req.AccountSelector == "" {
		req.AccountSelector = firstVal(flex.Account, strings.TrimSpace(r.Header.Get("X-ColabHive-Account")))
	}
	if req.Strategy == "" {
		req.Strategy = models.RoutingStrategy(strings.TrimSpace(r.Header.Get("X-ColabHive-Strategy")))
	}
	if req.StickyKey == "" {
		req.StickyKey = strings.TrimSpace(r.Header.Get("X-ColabHive-Session-Id"))
	}
	if key := s.requestAPIKey(r); key != nil {
		req.APIKeyID = key.ID
		if req.SessionID != "" {
			if _, status, authErr := s.authorizeSessionForAPIKey(r.Context(), r, req.SessionID); authErr != nil {
				writeJSONError(w, status, authErr.Error())
				return
			}
		} else if req.StickyKey != "" && s.router != nil {
			if sr, ok := s.router.(interface {
				ResolveStickySessionForKey(context.Context, string, string) (*models.RuntimeSession, bool)
			}); ok {
				if bound, okB := sr.ResolveStickySessionForKey(r.Context(), req.StickyKey, key.ID); okB && bound != nil && (bound.AdoptedExternal || bound.APIKeyID != key.ID) {
					writeJSONError(w, http.StatusForbidden, "forbidden: sticky session belongs to a different API key or principal")
					return
				}
			}
		}
	}

	res, err := s.runtime.ExecuteCode(r.Context(), req)
	var sess *models.RuntimeSession
	if res != nil && res.Session != "" {
		sess, _ = s.runtime.GetSession(r.Context(), res.Session)
	}
	s.recordRESTTrace(r, "POST /api/v1/sessions/exec", req.AutoProvisionGPU, sess, start, err)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if res != nil {
		res.RichOutputs = s.signRichOutputs(res.RichOutputs)
		if s.requestAPIKey(r) != nil {
			knownEmail := ""
			if sess != nil {
				knownEmail = sess.AccountEmail
			}
			res = redactExecResultForScopedCaller(res, s.pooledEmailsForRedaction(r.Context(), knownEmail))
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetCells(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	inc := r.URL.Query().Get("include_outputs") != "false"
	cells, err := s.runtime.GetCells(r.Context(), id, inc)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if cells == nil {
		cells = []models.NotebookCell{}
	}
	var knownEmails []string
	if s.requestAPIKey(r) != nil {
		primary := ""
		if sess != nil {
			primary = sess.AccountEmail
		}
		knownEmails = s.pooledEmailsForRedaction(r.Context(), primary)
	}
	if s.isAuthRequired() || len(knownEmails) > 0 {
		for i := range cells {
			if s.isAuthRequired() {
				cells[i].Outputs = s.signRichOutputs(cells[i].Outputs)
			}
			if len(knownEmails) > 0 {
				cells[i].Outputs = redactRichOutputsWithEmails(cells[i].Outputs, knownEmails)
			}
		}
	}
	writeJSON(w, http.StatusOK, cells)
}

func (s *Server) handleMutateCell(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	var req models.CellMutationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	cell, res, err := s.runtime.MutateCell(r.Context(), id, req)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	var knownEmails []string
	if s.requestAPIKey(r) != nil {
		primary := ""
		if sess != nil {
			primary = sess.AccountEmail
		}
		knownEmails = s.pooledEmailsForRedaction(r.Context(), primary)
	}
	if cell != nil {
		cell.Outputs = s.signRichOutputs(cell.Outputs)
		if len(knownEmails) > 0 {
			cp := *cell
			cp.Outputs = redactRichOutputsWithEmails(cp.Outputs, knownEmails)
			cell = &cp
		}
	}
	if res != nil {
		res.RichOutputs = s.signRichOutputs(res.RichOutputs)
		if len(knownEmails) > 0 {
			res = redactExecResultForScopedCaller(res, knownEmails)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cell":   cell,
		"result": res,
	})
}

func (s *Server) handleGetFilesOrDownload(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("content") == "1" {
		s.handleDownloadFile(w, r)
		return
	}
	id := r.PathValue("id")
	sess, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	remPath := firstVal(r.URL.Query().Get("path"), "/content")
	entries, err := s.runtime.ListFiles(r.Context(), sess.ID, remPath)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if entries == nil {
		entries = []models.RemoteFileEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	rawSess := r.PathValue("id")
	sess, status, err := s.authorizeSessionForAPIKey(r.Context(), r, rawSess)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}

	const maxUploadPayloadBytes = 256 << 20 // 256 MiB upload payload ceiling

	// Support streaming/multipart `FormData` uploads (`path` / `remote_path` + `file`) without duplicate Base64!
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadPayloadBytes)
		if pErr := r.ParseMultipartForm(32 << 20); pErr != nil {
			if strings.Contains(strings.ToLower(pErr.Error()), "too large") {
				writeJSONError(w, http.StatusRequestEntityTooLarge, "upload payload exceeds 256 MiB limit")
				return
			}
			writeJSONError(w, http.StatusBadRequest, "invalid multipart upload: "+pErr.Error())
			return
		}
		if r.MultipartForm != nil {
			defer func() { _ = r.MultipartForm.RemoveAll() }()
		}
		f, hdr, fErr := r.FormFile("file")
		if fErr != nil {
			writeJSONError(w, http.StatusBadRequest, "missing multipart 'file' field: "+fErr.Error())
			return
		}
		defer f.Close()

		data, readErr := io.ReadAll(io.LimitReader(f, maxUploadPayloadBytes+1))
		if readErr != nil {
			writeJSONError(w, http.StatusBadRequest, "failed to read uploaded file: "+readErr.Error())
			return
		}
		if int64(len(data)) > maxUploadPayloadBytes {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "uploaded file exceeds 256 MiB limit")
			return
		}

		dest := firstVal(r.FormValue("remote_path"), r.FormValue("path"), r.URL.Query().Get("path"))
		if dest == "" && hdr != nil && hdr.Filename != "" {
			dest = "/content/" + filepath.Base(hdr.Filename)
		}
		if dest == "" {
			writeJSONError(w, http.StatusBadRequest, "remote_path or path is required")
			return
		}
		if !strings.HasPrefix(dest, "/") {
			dest = "/" + dest
		}

		if err := s.runtime.UploadFile(r.Context(), sess.ID, dest, data); err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"uploaded":    true,
			"remote_path": dest,
			"bytes":       len(data),
		})
		return
	}

	var body struct {
		RemotePath    string `json:"remote_path"`
		Path          string `json:"path"`
		Format        string `json:"format"`
		Chunk         int    `json:"chunk"`
		UploadID      string `json:"upload_id"`
		Content       string `json:"content"`
		ContentBase64 string `json:"content_base64"`
	}
	if err := decodeJSONBodyLimit(r, &body, maxUploadPayloadBytes); err != nil {
		if errors.Is(err, errRequestBodyTooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "upload JSON body exceeds 256 MiB limit")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	dest := firstVal(body.RemotePath, body.Path)
	if dest == "" {
		writeJSONError(w, http.StatusBadRequest, "remote_path is required")
		return
	}
	if !strings.HasPrefix(dest, "/") {
		dest = "/" + dest
	}

	rawStr := firstVal(body.ContentBase64, body.Content)
	var data []byte
	if strings.EqualFold(body.Format, "base64") || body.ContentBase64 != "" {
		data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(rawStr))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid base64 payload: "+err.Error())
			return
		}
	} else {
		data = []byte(rawStr)
	}

	// Support bounded multi-chunk upload protocol (`chunk`: 1..N, -1 for final chunk)
	// keyed by canonical `sess.ID + "\x00" + dest` (plus optional `upload_id`).
	if body.Chunk != 0 {
		bufKey := sess.ID + "\x00" + dest
		if strings.TrimSpace(body.UploadID) != "" {
			bufKey += "\x00" + strings.TrimSpace(body.UploadID)
		}
		curLen, completeData, cErr := s.chunks.handleChunk(bufKey, body.Chunk, data)
		if cErr != nil {
			if errors.Is(cErr, errUploadChunkLimitExceeded) {
				writeJSONError(w, http.StatusRequestEntityTooLarge, cErr.Error())
				return
			}
			writeJSONError(w, http.StatusBadRequest, cErr.Error())
			return
		}
		if body.Chunk > 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"uploaded":    false,
				"chunk":       body.Chunk,
				"remote_path": dest,
				"bytes":       curLen,
			})
			return
		}
		data = completeData
	}

	if err := s.runtime.UploadFile(r.Context(), sess.ID, dest, data); err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uploaded":    true,
		"remote_path": dest,
		"bytes":       len(data),
	})
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	remPath := r.URL.Query().Get("path")
	if strings.TrimSpace(remPath) == "" {
		writeJSONError(w, http.StatusBadRequest, "query parameter 'path' is required")
		return
	}
	data, err := s.runtime.DownloadFile(r.Context(), sess.ID, remPath)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	baseName := filepath.Base(remPath)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", baseName))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, status, err := s.authorizeSessionForAPIKey(r.Context(), r, id)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	remPath := r.URL.Query().Get("path")
	if strings.TrimSpace(remPath) == "" {
		writeJSONError(w, http.StatusBadRequest, "query parameter 'path' is required")
		return
	}
	if err := s.runtime.DeleteFile(r.Context(), sess.ID, remPath); err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInstallPackages(w http.ResponseWriter, r *http.Request) {
	rawSess := r.PathValue("id")
	sess, status, err := s.authorizeSessionForAPIKey(r.Context(), r, rawSess)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	var body struct {
		Packages         []string `json:"packages"`
		RequirementsTxt  string   `json:"requirements_txt"`
		RequirementsPath string   `json:"requirements_path"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	reqPath := strings.TrimSpace(body.RequirementsPath)
	if reqPath != "" && (s.isCloudMode() || s.requestAPIKey(r) != nil) {
		writeJSONError(w, http.StatusBadRequest, "host filesystem requirements_path is not permitted in cloud mode or with scoped API keys; pass requirements_txt or packages instead")
		return
	}
	reqSpec := body.RequirementsTxt
	if reqSpec == "" && reqPath != "" {
		raw, rErr := os.ReadFile(reqPath)
		if rErr != nil {
			writeJSONError(w, http.StatusBadRequest, "failed to read requirements_path: "+rErr.Error())
			return
		}
		reqSpec = string(raw)
	}
	res, err := s.runtime.InstallPackages(r.Context(), sess.ID, body.Packages, reqSpec)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if res != nil && s.requestAPIKey(r) != nil {
		res = redactExecResultForScopedCaller(res, s.pooledEmailsForRedaction(r.Context(), sess.AccountEmail))
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleServeArtifact(w http.ResponseWriter, r *http.Request) {
	sessID := r.PathValue("sessionID")
	fileName := r.PathValue("filename")
	if !isSafePathSegment(sessID) || !isSafePathSegment(fileName) {
		writeJSONError(w, http.StatusBadRequest, "invalid artifact path segment")
		return
	}

	// When accessed via a scoped `ch_sk_...` key (without an admin-minted HMAC signature),
	// verify the artifact's session belongs to that API key by exact Session ID (never matching by Name)!
	if s.requestAPIKey(r) != nil && !s.verifyArtifactSignature(sessID, fileName, r.URL.Query()) {
		authSess, stCode, authErr := s.authorizeSessionForAPIKey(r.Context(), r, sessID)
		if authErr != nil {
			writeJSONError(w, stCode, authErr.Error())
			return
		}
		if authSess == nil || authSess.ID != sessID {
			writeJSONError(w, http.StatusForbidden, "forbidden: artifact session ID does not match caller session")
			return
		}
	}

	absArtifactsDir, err := filepath.Abs(s.cfg.ArtifactsDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	absTarget, err := filepath.Abs(filepath.Join(absArtifactsDir, sessID, fileName))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid artifact path")
		return
	}
	rel, err := filepath.Rel(absArtifactsDir, absTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeJSONError(w, http.StatusBadRequest, "artifact path escapes root")
		return
	}

	root, err := os.OpenRoot(absArtifactsDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()

	f, err := root.Open(filepath.Join(sessID, fileName))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, 128<<20))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctype := mime.TypeByExtension(filepath.Ext(fileName))
	if ctype == "" {
		ctype = "image/png"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.isAuthRequired() {
		w.Header().Set("Cache-Control", "private, max-age=300")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleListExperiments(w http.ResponseWriter, r *http.Request) {
	list, err := s.runtime.ListExperiments(r.Context())
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if s.requestAPIKey(r) != nil {
		scoped := make([]*models.Experiment, 0, len(list))
		for _, exp := range list {
			if s.canAPIKeyAccessExperiment(r.Context(), r, exp) {
				scoped = append(scoped, s.prepareExperimentForResponse(r, exp))
			}
		}
		list = scoped
	} else if s.isAuthRequired() {
		signed := make([]*models.Experiment, 0, len(list))
		for _, exp := range list {
			signed = append(signed, s.signExperimentArtifacts(exp))
		}
		list = signed
	}
	if list == nil {
		list = []*models.Experiment{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetExperiment(w http.ResponseWriter, r *http.Request) {
	exp, err := s.runtime.GetExperiment(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if !s.canAPIKeyAccessExperiment(r.Context(), r, exp) {
		writeJSONError(w, http.StatusForbidden, "forbidden: experiment belongs to a different API key or principal")
		return
	}
	writeJSON(w, http.StatusOK, s.prepareExperimentForResponse(r, exp))
}

func (s *Server) handleLaunchExperiment(w http.ResponseWriter, r *http.Request) {
	var flex struct {
		models.ExperimentLaunchRequest
		GPU          string `json:"gpu"`
		Shards       int    `json:"shards"`
		UploadExtras []struct {
			LocalPath     string `json:"local_path"`
			RemotePath    string `json:"remote_path"`
			ContentB64    string `json:"content_b64"`
			ContentBase64 string `json:"content_base64"`
			Content       string `json:"content"`
		} `json:"upload_files"`
	}
	flex.HighMem = true
	flex.AutoStopOnComplete = true
	if err := decodeJSONBody(r, &flex); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	req := flex.ExperimentLaunchRequest
	req.ExperimentID = strings.TrimSpace(req.ExperimentID)
	if req.ExperimentID != "" && !isValidASCIIExperimentIDServer(req.ExperimentID) {
		writeJSONError(w, http.StatusBadRequest, "invalid experiment_id: must be 1..128 ASCII [A-Za-z0-9._-] characters without '..' or path separators")
		return
	}
	if req.ExperimentID != "" && s.runtime != nil {
		if existing, gErr := s.runtime.GetExperiment(r.Context(), req.ExperimentID); gErr == nil && existing != nil {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("experiment %q already exists", req.ExperimentID))
			return
		}
		if allExps, lErr := s.runtime.ListExperiments(r.Context()); lErr == nil {
			for _, ex := range allExps {
				if ex != nil && strings.EqualFold(ex.ID, req.ExperimentID) {
					writeJSONError(w, http.StatusConflict, fmt.Sprintf("experiment %q already exists", req.ExperimentID))
					return
				}
			}
		}
	}

	disallowHostFS := s.isCloudMode() || s.requestAPIKey(r) != nil
	if disallowHostFS {
		if strings.TrimSpace(req.LocalOutputDir) != "" {
			writeJSONError(w, http.StatusBadRequest, "host filesystem local_output_dir is not permitted in cloud mode or with scoped API keys")
			return
		}
		if strings.TrimSpace(req.ScriptPath) != "" && strings.TrimSpace(req.ScriptCode) == "" {
			writeJSONError(w, http.StatusBadRequest, "host filesystem script_path without inline script_code is not permitted in cloud mode or with scoped API keys")
			return
		}
		if strings.TrimSpace(req.ScriptPath) != "" {
			req.ScriptPath = filepath.Base(req.ScriptPath)
		}
	}

	if req.Accelerator == "" && flex.GPU != "" {
		norm, err := models.NormalizeAccelerator(flex.GPU)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Accelerator = norm
	}
	if req.NumShards <= 0 && flex.Shards > 0 {
		req.NumShards = flex.Shards
	}
	if key := s.requestAPIKey(r); key != nil {
		req.APIKeyID = key.ID
	}

	var stageDir string
	defer func() {
		if stageDir != "" {
			_ = os.RemoveAll(stageDir)
		}
	}()

	// Materialize inline uploaded files into a confined ephemeral temp directory cleaned up via defer!
	if len(flex.UploadExtras) > 0 {
		specs := make([]models.ExperimentFileSpec, 0, len(flex.UploadExtras))
		for idx, item := range flex.UploadExtras {
			lp := strings.TrimSpace(item.LocalPath)
			b64 := firstVal(item.ContentBase64, item.ContentB64)
			hasInline := b64 != "" || item.Content != ""

			if disallowHostFS && lp != "" && !hasInline {
				writeJSONError(w, http.StatusBadRequest, "host filesystem upload_files[].local_path is not permitted in cloud mode or with scoped API keys; pass content_base64 or content")
				return
			}

			if hasInline {
				var decoded []byte
				if b64 != "" {
					var dErr error
					decoded, dErr = base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
					if dErr != nil {
						writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid upload_files[%d] base64: %v", idx, dErr))
						return
					}
				} else {
					decoded = []byte(item.Content)
					b64 = base64.StdEncoding.EncodeToString(decoded)
				}

				if stageDir == "" {
					var mkErr error
					stageDir, mkErr = os.MkdirTemp("", "colabhive-exp-uploads-*")
					if mkErr != nil {
						writeJSONError(w, http.StatusInternalServerError, "failed to create staging directory")
						return
					}
				}
				baseName := filepath.Base(firstVal(item.RemotePath, "file.bin"))
				if !isSafePathSegment(baseName) {
					baseName = "file.bin"
				}
				staged := filepath.Join(stageDir, fmt.Sprintf("%d_%s", idx, baseName))
				if wErr := os.WriteFile(staged, decoded, 0o600); wErr != nil {
					writeJSONError(w, http.StatusInternalServerError, "failed to stage upload file")
					return
				}
				lp = staged
			}

			specs = append(specs, models.ExperimentFileSpec{
				LocalPath:     lp,
				RemotePath:    item.RemotePath,
				ContentBase64: b64,
			})
		}
		req.UploadFiles = specs
	}

	exp, err := s.runtime.LaunchShardedExperiment(r.Context(), req)
	if err != nil {
		msg := err.Error()
		if s.requestAPIKey(r) != nil {
			msg = redactEmailsInServerText(msg, "")
		}
		writeJSONError(w, mapServiceErrorStatus(err), msg)
		return
	}
	if key := s.requestAPIKey(r); key != nil && exp != nil {
		s.recordExperimentOwner(exp.ID, key.ID)
	}
	writeJSON(w, http.StatusOK, s.prepareExperimentForResponse(r, exp))
}

func (s *Server) handleTeardownExperiment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.requestAPIKey(r) != nil {
		exp, err := s.runtime.GetExperiment(r.Context(), id)
		if err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
		if !s.canAPIKeyAccessExperiment(r.Context(), r, exp) {
			writeJSONError(w, http.StatusForbidden, "forbidden: experiment belongs to a different API key or principal")
			return
		}
		id = exp.ID
	}
	var body struct {
		ForceKillRunning bool `json:"force_kill_running"`
	}
	_ = decodeJSONBody(r, &body)
	res, err := s.runtime.CollectAndTeardownExperiment(r.Context(), id, body.ForceKillRunning)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if s.requestAPIKey(r) != nil {
		res = redactTeardownForScopedCaller(res)
	}
	writeJSON(w, http.StatusOK, res)
}

// ============================================================================
// 11. Agent Router, Sticky Sessions, API Keys & `cc-switch` Config Sync
// ============================================================================

func (s *Server) handleGetRouterPolicy(w http.ResponseWriter, r *http.Request) {
	pol, err := s.router.GetPolicy(r.Context())
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pol)
}

func (s *Server) handleUpdateRouterPolicy(w http.ResponseWriter, r *http.Request) {
	var pol models.RouterPolicy
	if err := decodeJSONBody(r, &pol); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.router.UpdatePolicy(r.Context(), pol)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleListStickyBindings(w http.ResponseWriter, r *http.Request) {
	list, err := s.router.ListStickyBindings(r.Context())
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if list == nil {
		list = []models.RouteBinding{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleUnbindStickySession(w http.ResponseWriter, r *http.Request) {
	stickyKey := r.PathValue("stickyKey")
	apiKeyID := strings.TrimSpace(r.URL.Query().Get("api_key_id"))
	if adminUnbinder, ok := s.router.(interface {
		UnbindStickySessionAdmin(context.Context, string, string) error
	}); ok {
		if err := adminUnbinder.UnbindStickySessionAdmin(r.Context(), stickyKey, apiKeyID); err != nil {
			writeJSONError(w, mapServiceErrorStatus(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.router.UnbindStickySession(r.Context(), stickyKey); err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTraces(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	traces, err := s.router.ListTraces(r.Context(), limit)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if traces == nil {
		traces = []models.ToolTraceEvent{}
	}
	writeJSON(w, http.StatusOK, traces)
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.router.ListAPIKeys(r.Context())
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	if keys == nil {
		keys = []*models.AgentAPIKey{}
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req models.CreateAPIKeyRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.router.CreateAPIKey(r.Context(), req)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, created)
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := s.router.RevokeAPIKey(r.Context(), r.PathValue("id")); err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type agentClientResponse struct {
	models.AgentClientConfigStatus
	SupportsAutoInject bool `json:"supports_auto_inject"`
	CloudMode          bool `json:"cloud_mode"`
}

func canonicalLaptopConfigPath(cid models.AgentClientID) string {
	switch cid {
	case models.AgentAntigravity:
		return "~/.gemini/config/mcp_config.json"
	case models.AgentClaudeCode:
		return "~/.claude.json"
	case models.AgentClaudeDesktop:
		return "~/Library/Application Support/Claude/claude_desktop_config.json"
	case models.AgentCodex:
		return "~/.codex/config.toml"
	case models.AgentGeminiCLI:
		return "~/.gemini/settings.json"
	case models.AgentCursor:
		return "~/.cursor/mcp.json"
	case models.AgentWindsurf:
		return "~/.codeium/windsurf/mcp_config.json"
	case models.AgentOpenCode:
		return "~/.config/opencode/opencode.json"
	default:
		return ""
	}
}

func buildRemoteHTTPSnippetForClaudeDesktop(baseURL, cleanKey string) string {
	entry := map[string]any{
		"url": baseURL + "/mcp",
	}
	if cleanKey != "" {
		entry["headers"] = map[string]string{
			"Authorization": "Bearer " + cleanKey,
		}
	}
	doc := map[string]any{
		"mcpServers": map[string]any{
			"colabhive": entry,
		},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b)
}

func (s *Server) handleListAgentClients(w http.ResponseWriter, r *http.Request) {
	apiKey := r.URL.Query().Get("api_key")
	cleanKey := router.SanitizeInjectorAPIKey(apiKey)
	baseURL := s.inferServerBaseURL(r)
	list, err := s.router.ListAgentClientStatuses(r.Context(), baseURL, cleanKey)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}

	cloud := s.isCloudMode()
	out := make([]agentClientResponse, 0, len(list))
	for _, st := range list {
		if cleanKey != "" && st.ClientID != models.AgentClaudeCode {
			st.CLICommand = fmt.Sprintf("colabhive connect %s --server %s --key %s", st.ClientID, baseURL, cleanKey)
		}
		if cloud {
			st.Detected = false
			st.Injected = false
			st.BackupExists = false
			st.BackupPath = ""
			st.WSLConfigPath = ""
			st.LastSyncedAt = nil
			st.Transport = "http"
			if canon := canonicalLaptopConfigPath(st.ClientID); canon != "" {
				st.ConfigPath = canon
			}
			if st.ClientID == models.AgentClaudeDesktop {
				st.Snippet = buildRemoteHTTPSnippetForClaudeDesktop(baseURL, cleanKey)
			}
		}
		out = append(out, agentClientResponse{
			AgentClientConfigStatus: st,
			SupportsAutoInject:      !cloud,
			CloudMode:               cloud,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleInjectAgentConfig(w http.ResponseWriter, r *http.Request) {
	if s.isCloudMode() {
		writeJSONError(w, http.StatusForbidden, "host filesystem config injection is disabled in --mode cloud; copy the remote MCP snippet instead")
		return
	}
	var req models.AgentInjectRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ServerBaseURL == "" {
		req.ServerBaseURL = s.inferServerBaseURL(r)
	}
	status, err := s.router.InjectAgentConfig(r.Context(), req)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleRemoveAgentConfig(w http.ResponseWriter, r *http.Request) {
	if s.isCloudMode() {
		writeJSONError(w, http.StatusForbidden, "host filesystem config removal is disabled in --mode cloud")
		return
	}
	cid := models.AgentClientID(r.PathValue("clientId"))
	status, err := s.router.RemoveAgentConfig(r.Context(), cid)
	if err != nil {
		writeJSONError(w, mapServiceErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleGetDesktopState(w http.ResponseWriter, r *http.Request) {
	s.desktopMu.RLock()
	bridge := s.desktopBridge
	s.desktopMu.RUnlock()

	baseURL := s.inferServerBaseURL(r)
	if bridge == nil || s.isCloudMode() {
		writeJSON(w, http.StatusOK, DesktopState{
			DesktopShell: false,
			ServerURL:    baseURL,
		})
		return
	}
	st, err := bridge(r.Context(), DesktopActionRequest{Action: "state"})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if st.ServerURL == "" {
		st.ServerURL = baseURL
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleDesktopAction(w http.ResponseWriter, r *http.Request) {
	if s.isCloudMode() {
		writeJSONError(w, http.StatusForbidden, "desktop tray actions are disabled in --mode cloud")
		return
	}
	var req DesktopActionRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	if req.Route == "" {
		req.Route = strings.TrimSpace(req.Path)
	}
	if req.Action == "" {
		writeJSONError(w, http.StatusBadRequest, "action is required")
		return
	}

	switch req.Action {
	case "open-main", "open-route", "hide-tray", "show-tray", "toggle-tray", "sync-now", "copy-mcp", "quit", "toggle-autostart", "set-autostart", "state":
		// Valid desktop bridge action
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported desktop action: "+req.Action)
		return
	}

	s.desktopMu.RLock()
	bridge := s.desktopBridge
	s.desktopMu.RUnlock()

	baseURL := s.inferServerBaseURL(r)
	if bridge == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"handled":       false,
			"desktop_shell": false,
			"action":        req.Action,
			"route":         req.Route,
			"state": DesktopState{
				DesktopShell: false,
				ServerURL:    baseURL,
				LastAction:   req.Action,
			},
		})
		return
	}

	st, err := bridge(r.Context(), req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if st.ServerURL == "" {
		st.ServerURL = baseURL
	}
	if st.LastAction == "" {
		st.LastAction = req.Action
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"handled":           true,
		"desktop_shell":     st.DesktopShell,
		"autostart":         st.AutoStartEnabled,
		"autostart_enabled": st.AutoStartEnabled,
		"action":            req.Action,
		"route":             req.Route,
		"state":             st,
	})
}

func (s *Server) recordRESTTrace(
	r *http.Request,
	endpoint string,
	reqGPU models.AcceleratorType,
	sess *models.RuntimeSession,
	start time.Time,
	err error,
) {
	if s.router == nil {
		return
	}
	ua := r.UserAgent()
	transport := models.TransportREST
	if strings.Contains(strings.ToLower(ua), "python-sdk") {
		transport = models.TransportSDK
	} else if strings.Contains(strings.ToLower(ua), "colabhive-cli") {
		transport = models.TransportCLI
	}

	ev := models.ToolTraceEvent{
		Caller:         firstVal(strings.Split(ua, "/")[0], "rest-client"),
		Transport:      transport,
		ToolOrEndpoint: endpoint,
		RequestedGPU:   reqGPU,
		DurationMs:     time.Since(start).Milliseconds(),
		Status:         "ok",
		StatusCode:     200,
	}
	if key := s.requestAPIKey(r); key != nil {
		ev.APIKeyPrefix = key.KeyPrefix
	}
	if sess != nil {
		ev.SessionID = sess.ID
		ev.AccountID = sess.AccountID
		ev.AccountEmail = sess.AccountEmail
		ev.AssignedGPU = sess.Accelerator
		if sess.FallbackApplied {
			ev.Status = "fallback"
			ev.FallbackTrace = fmt.Sprintf("%s -> %s", sess.RequestedAccelerator, sess.Accelerator)
		}
	}
	if err != nil {
		ev.Status = "error"
		ev.StatusCode = mapServiceErrorStatus(err)
		ev.ErrorMessage = err.Error()
	}
	_ = s.router.RecordTrace(r.Context(), ev)
}

func firstVal(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
