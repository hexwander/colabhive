package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/router"
)

// VersionInfo carries build metadata injected via `-ldflags`.
type VersionInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

type cliGlobalOpts struct {
	ServerURL string
	APIKey    string
	Account   string
	Strategy  string
	Fallback  string
	Shards    int
	ExpID     string
	JSON      bool
}

type daemonHTTPClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newDaemonClient(opts cliGlobalOpts) *daemonHTTPClient {
	base := strings.TrimRight(strings.TrimSpace(opts.ServerURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("COLABHIVE_URL")), "/")
	}
	if base == "" {
		if info, err := ReadDaemonInfo(); err == nil && info != nil && info.URL != "" {
			base = info.URL
		}
	}
	if base == "" {
		base = "http://127.0.0.1:7860"
	}
	key := strings.TrimSpace(opts.APIKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("COLABHIVE_API_KEY"))
	}
	if key == "" {
		key = strings.TrimSpace(os.Getenv("COLABHIVE_ADMIN_TOKEN"))
	}
	return &daemonHTTPClient{
		baseURL: base,
		apiKey:  key,
		http:    &http.Client{Timeout: 30 * time.Minute},
	}
}

func (c *daemonHTTPClient) doJSON(ctx context.Context, method string, path string, reqBody any, respDst any) (int, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, err
		}
		bodyReader = bytes.NewReader(b)
	}
	target := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "colabhive-cli/1.0.0")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cannot reach ColabHive daemon at %s (is `colabhive serve` running?): %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errObj struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &errObj)
		msg := firstVal(errObj.Error, errObj.Message, strings.TrimSpace(string(raw)), resp.Status)
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if respDst != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, respDst); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *daemonHTTPClient) doRawGet(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "colabhive-cli/1.0.0")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// RunDaemonCLI executes any `colabhive` / `colab` client command (`new`, `exec`, `run`, `sessions`,
// `status`, `stop`, `restart-kernel`, `upload`, `download`, `ls`, `rm`, `install`, `usage`, `log`,
// `url`, `auth`, `donate`, `connect`, `disconnect`, `pool`, `version`, `help`).
func RunDaemonCLI(
	ctx context.Context,
	progName string,
	args []string,
	in io.Reader,
	out io.Writer,
	errOut io.Writer,
	ver VersionInfo,
) int {
	globals, rest := extractGlobalCLIFlags(args)
	if len(rest) == 0 {
		printCLIUsage(progName, out, ver)
		return 0
	}

	sub := rest[0]
	subArgs := rest[1:]
	client := newDaemonClient(globals)

	switch sub {
	case "version", "--version", "-v":
		_, _ = fmt.Fprintf(out, "Version: %s (commit %s, built %s)\n", firstVal(ver.Version, "1.0.0"), firstVal(ver.Commit, "dev"), firstVal(ver.BuildDate, "unknown"))
		return 0
	case "help", "--help", "-h":
		printCLIUsage(progName, out, ver)
		return 0
	case "new":
		return cliCmdNew(ctx, client, globals, subArgs, out, errOut)
	case "exec":
		return cliCmdExec(ctx, client, globals, subArgs, in, out, errOut)
	case "run":
		return cliCmdRun(ctx, client, globals, subArgs, out, errOut)
	case "sessions":
		return cliCmdSessions(ctx, client, globals, subArgs, out, errOut)
	case "status":
		return cliCmdStatus(ctx, client, globals, subArgs, out, errOut)
	case "stop":
		return cliCmdStop(ctx, client, globals, subArgs, out, errOut)
	case "restart-kernel":
		return cliCmdRestartKernel(ctx, client, subArgs, errOut)
	case "upload":
		return cliCmdUpload(ctx, client, subArgs, out, errOut)
	case "download":
		return cliCmdDownload(ctx, client, subArgs, out, errOut)
	case "ls":
		return cliCmdLS(ctx, client, subArgs, out, errOut)
	case "rm":
		return cliCmdRM(ctx, client, subArgs, out, errOut)
	case "install":
		return cliCmdInstall(ctx, client, subArgs, out, errOut)
	case "usage":
		return cliCmdUsage(ctx, client, globals, subArgs, out, errOut)
	case "log":
		return cliCmdLog(ctx, client, subArgs, out, errOut)
	case "url":
		return cliCmdURL(ctx, client, subArgs, out, errOut)
	case "auth", "whoami":
		return cliCmdAuth(ctx, client, sub, subArgs, in, out, errOut)
	case "connect":
		return cliCmdConnect(ctx, globals, subArgs, out, errOut, false)
	case "disconnect":
		return cliCmdConnect(ctx, globals, subArgs, out, errOut, true)
	case "pool":
		return cliCmdPool(ctx, client, subArgs, out, errOut)
	default:
		_, _ = fmt.Fprintf(errOut, "[%s] Unknown subcommand %q. Run '%s help' for usage.\n", progName, sub, progName)
		return 2
	}
}

func extractGlobalCLIFlags(args []string) (cliGlobalOpts, []string) {
	var g cliGlobalOpts
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		nextVal := func() string {
			if idx := strings.IndexByte(a, '='); idx >= 0 {
				return a[idx+1:]
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--server" || strings.HasPrefix(a, "--server="):
			g.ServerURL = nextVal()
		case a == "--api-key" || strings.HasPrefix(a, "--api-key="),
			a == "--key" || strings.HasPrefix(a, "--key="):
			g.APIKey = nextVal()
		case a == "--account" || strings.HasPrefix(a, "--account="):
			g.Account = nextVal()
		case a == "--strategy" || strings.HasPrefix(a, "--strategy="):
			g.Strategy = nextVal()
		case a == "--fallback" || strings.HasPrefix(a, "--fallback="):
			g.Fallback = nextVal()
		case a == "--shards" || strings.HasPrefix(a, "--shards="):
			g.Shards, _ = strconv.Atoi(nextVal())
		case a == "--experiment" || strings.HasPrefix(a, "--experiment="):
			g.ExpID = nextVal()
		case a == "--json":
			g.JSON = true
		case a == "--logtostderr":
			// Accepted for colab v0.8.0 flag compatibility
		case a == "--config" || strings.HasPrefix(a, "--config="),
			a == "--auth" || strings.HasPrefix(a, "--auth="),
			a == "-c" || a == "--client-oauth-config" || strings.HasPrefix(a, "--client-oauth-config="):
			_ = nextVal()
		default:
			rest = append(rest, a)
		}
	}
	return g, rest
}

// ============================================================================
// Subcommand Implementations (`colab v0.8.0` + Pool Extensions)
// ============================================================================

func cliCmdNew(ctx context.Context, client *daemonHTTPClient, g cliGlobalOpts, args []string, out, errOut io.Writer) int {
	var sessName, gpuFlag, tpuFlag string
	highMem := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-s", "--session":
			if i+1 < len(args) {
				i++
				sessName = args[i]
			}
		case "--gpu":
			if i+1 < len(args) {
				i++
				gpuFlag = args[i]
			}
		case "--tpu":
			if i+1 < len(args) {
				i++
				tpuFlag = args[i]
			}
		case "--high-mem":
			highMem = true
		}
	}

	rawAcc := firstVal(gpuFlag, tpuFlag, "CPU")
	accel, err := models.NormalizeAccelerator(rawAcc)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 2
	}
	if highMem {
		if spec, ok := models.GetHardwareSpec(accel); ok && !spec.SupportsHighMemFlag {
			_, _ = fmt.Fprintf(errOut, "[colab] Note: --high-mem ignored for %s (single High-RAM shape).\n", accel)
		}
	}

	if sessName != "" {
		_, _ = fmt.Fprintf(out, "[colab] Creating session '%s'...\n", sessName)
	} else {
		_, _ = fmt.Fprintln(out, "[colab] Creating session...")
	}

	autoFallback := g.Fallback != "none" && g.Fallback != "false"
	var created models.RuntimeSession
	_, err = client.doJSON(ctx, http.MethodPost, "/api/v1/sessions", models.SessionAllocRequest{
		Name:            sessName,
		Accelerator:     accel,
		HighMem:         highMem,
		Strategy:        models.RoutingStrategy(g.Strategy),
		AccountSelector: g.Account,
		AutoFallback:    autoFallback,
		ExperimentID:    g.ExpID,
	}, &created)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Failed to create session: %v\n", err)
		return 1
	}

	if g.JSON {
		enc, _ := json.MarshalIndent(created, "", "  ")
		_, _ = fmt.Fprintln(out, string(enc))
		return 0
	}
	_, _ = fmt.Fprintf(out, "[colab] Session READY. (%s · %s · %s)\n", created.Name, created.Accelerator, created.AccountEmail)
	return 0
}

func cliCmdExec(ctx context.Context, client *daemonHTTPClient, _ cliGlobalOpts, args []string, in io.Reader, out, errOut io.Writer) int {
	var sessName, filePath, inlineCode, outImgPath string
	timeoutSec := 30.0
	envMap := make(map[string]string)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-s", "--session":
			if i+1 < len(args) {
				i++
				sessName = args[i]
			}
		case "-f", "--file":
			if i+1 < len(args) {
				i++
				filePath = args[i]
			}
		case "-c", "--code":
			if i+1 < len(args) {
				i++
				inlineCode = args[i]
			}
		case "--output-image":
			if i+1 < len(args) {
				i++
				outImgPath = args[i]
			}
		case "--timeout":
			if i+1 < len(args) {
				i++
				timeoutSec, _ = strconv.ParseFloat(args[i], 64)
			}
		case "--env":
			if i+1 < len(args) {
				i++
				k, v, ok := strings.Cut(args[i], "=")
				if !ok || strings.TrimSpace(k) == "" {
					_, _ = fmt.Fprintf(errOut, "[colab] Invalid --env %q (expected KEY=VALUE)\n", args[i])
					return 2
				}
				envMap[k] = v
			}
		default:
			if inlineCode == "" && !strings.HasPrefix(args[i], "-") {
				inlineCode = args[i]
			}
		}
	}

	code := inlineCode
	if filePath != "" {
		raw, err := os.ReadFile(filePath)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "[colab] Cannot read %s: %v\n", filePath, err)
			return 1
		}
		code = string(raw)
	} else if code == "" && in != nil {
		piped, _ := io.ReadAll(in)
		code = string(piped)
	}
	if strings.TrimSpace(code) == "" {
		_, _ = fmt.Fprintln(errOut, "[colab] No code supplied (pass -f script.py, code argument, or piped stdin).")
		return 1
	}

	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}

	var res models.KernelExecResult
	_, err = client.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/sessions/%s/exec", url.PathEscape(sid)), models.KernelExecRequest{
		SessionID:      sid,
		Code:           code,
		Env:            envMap,
		IdleTimeoutSec: timeoutSec,
		StoreHistory:   true,
		InlineImages:   outImgPath != "",
	}, &res)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Execution transport failed: %v\n", err)
		return 1
	}

	if res.Stdout != "" {
		_, _ = io.WriteString(out, res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			_, _ = io.WriteString(out, "\n")
		}
	}
	if res.Result != "" {
		_, _ = fmt.Fprintln(out, res.Result)
	}
	if res.Stderr != "" {
		_, _ = io.WriteString(errOut, res.Stderr)
	}
	if outImgPath != "" && len(res.RichOutputs) > 0 && res.RichOutputs[0].InlineBase64 != "" {
		if imgBytes, dErr := base64.StdEncoding.DecodeString(res.RichOutputs[0].InlineBase64); dErr == nil {
			_ = os.WriteFile(outImgPath, imgBytes, 0o644)
			_, _ = fmt.Fprintf(out, "[Image saved to: %s]\n", outImgPath)
		}
	}
	return 0
}

func isCLIExperimentTerminal(exp *models.Experiment) bool {
	if exp == nil {
		return false
	}
	switch exp.Status {
	case models.ExperimentCompleted, models.ExperimentPartialFailure, models.ExperimentFailed, models.ExperimentCancelled:
		return true
	}
	if len(exp.Shards) > 0 {
		allDone := true
		for _, sh := range exp.Shards {
			switch sh.Status {
			case "COMPLETED", "COMPLETED_AND_UNASSIGNED", "FAILED", "ZOMBIE_ABORTED":
			default:
				allDone = false
			}
		}
		if allDone {
			return true
		}
	}
	return false
}

func cliCmdRun(ctx context.Context, client *daemonHTTPClient, g cliGlobalOpts, args []string, out, errOut io.Writer) int {
	var sessName, gpuFlag, tpuFlag, scriptPath string
	var scriptArgs []string
	highMem := false
	keepVM := false
	timeoutSec := 120.0
	explicitTimeout := false
	envMap := make(map[string]string)

	for i := 0; i < len(args); i++ {
		if scriptPath != "" {
			// Support `--env` after script_path too per colab v0.8.0 spec!
			if args[i] == "--env" && i+1 < len(args) {
				i++
				k, v, ok := strings.Cut(args[i], "=")
				if !ok || strings.TrimSpace(k) == "" {
					_, _ = fmt.Fprintf(errOut, "[colab] Invalid --env %q\n", args[i])
					return 2
				}
				envMap[k] = v
				continue
			}
			scriptArgs = append(scriptArgs, args[i])
			continue
		}
		switch args[i] {
		case "-s", "--session":
			if i+1 < len(args) {
				i++
				sessName = args[i]
			}
		case "--gpu":
			if i+1 < len(args) {
				i++
				gpuFlag = args[i]
			}
		case "--tpu":
			if i+1 < len(args) {
				i++
				tpuFlag = args[i]
			}
		case "--high-mem":
			highMem = true
		case "--keep":
			keepVM = true
		case "--timeout":
			if i+1 < len(args) {
				i++
				timeoutSec, _ = strconv.ParseFloat(args[i], 64)
				explicitTimeout = true
			}
		case "--env":
			if i+1 < len(args) {
				i++
				k, v, ok := strings.Cut(args[i], "=")
				if !ok || strings.TrimSpace(k) == "" {
					_, _ = fmt.Fprintf(errOut, "[colab] Invalid --env %q\n", args[i])
					return 2
				}
				envMap[k] = v
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				_, _ = fmt.Fprintf(errOut, "[colab] Unknown flag %s\n", args[i])
				return 2
			}
			scriptPath = args[i]
		}
	}

	if scriptPath == "" {
		_, _ = fmt.Fprintln(errOut, "[colab] Missing script.py argument for 'run'.")
		return 2
	}

	// MANDATORY pre-flight check BEFORE allocating any cloud VM (exits code 2 on missing file!).
	scriptBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Script file not found: %s (%v)\n", scriptPath, err)
		return 2
	}

	accel, err := models.NormalizeAccelerator(firstVal(gpuFlag, tpuFlag, "G4"))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 2
	}

	// Multi-Account Parallel Sharded Sweep (`--shards K`, K > 1)
	if g.Shards > 1 {
		expID := firstVal(g.ExpID, models.NewPrefixedID("exp_"))
		_, _ = fmt.Fprintf(errOut, "[colab] Launching %d-shard %s experiment '%s'...\n", g.Shards, accel, expID)
		var exp models.Experiment
		_, lErr := client.doJSON(ctx, http.MethodPost, "/api/v1/experiments", models.ExperimentLaunchRequest{
			ExperimentID:       expID,
			ScriptPath:         filepath.Base(scriptPath),
			ScriptCode:         string(scriptBytes),
			ScriptArgs:         scriptArgs,
			Accelerator:        accel,
			HighMem:            highMem || true,
			NumShards:          g.Shards,
			Strategy:           models.RoutingStrategy(g.Strategy),
			AutoStopOnComplete: !keepVM,
			Env:                envMap,
		}, &exp)
		if lErr != nil {
			_, _ = fmt.Fprintf(errOut, "[colab] Sharded launch failed: %v\n", lErr)
			return 1
		}
		if g.JSON {
			enc, _ := json.MarshalIndent(exp, "", "  ")
			_, _ = fmt.Fprintln(out, string(enc))
			return 0
		}

		// Poll shard status until all shards reach a terminal state BEFORE collecting & tearing down!
		pollCtx := ctx
		var pollCancel context.CancelFunc
		if explicitTimeout && timeoutSec > 0 {
			pollCtx, pollCancel = context.WithTimeout(ctx, time.Duration(timeoutSec*float64(time.Second)))
			defer pollCancel()
		}

		for !isCLIExperimentTerminal(&exp) {
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-pollCtx.Done():
				timer.Stop()
				if !keepVM {
					cleanCtx, cCancel := context.WithTimeout(context.Background(), 15*time.Second)
					_, _ = client.doJSON(cleanCtx, http.MethodPost, fmt.Sprintf("/api/v1/experiments/%s/teardown", url.PathEscape(exp.ID)), map[string]any{
						"force_kill_running": true,
					}, nil)
					cCancel()
				}
				_, _ = fmt.Fprintf(errOut, "[colab] Experiment '%s' interrupted or timed out: %v\n", exp.ID, pollCtx.Err())
				return 1
			case <-timer.C:
			}
			var refreshed models.Experiment
			if _, gErr := client.doJSON(pollCtx, http.MethodGet, fmt.Sprintf("/api/v1/experiments/%s", url.PathEscape(exp.ID)), nil, &refreshed); gErr == nil && refreshed.ID != "" {
				exp = refreshed
			}
		}

		var td models.ExperimentTeardownResult
		_, _ = client.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/experiments/%s/teardown", url.PathEscape(exp.ID)), map[string]any{
			"force_kill_running": false,
		}, &td)
		_, _ = fmt.Fprintf(errOut, "[colab] Experiment '%s' completed [%s] (%d merged records -> %s).\n", exp.ID, exp.Status, td.MergedRecordsCount, td.MergedJSONLPath)
		if td.MergedJSONLPath != "" {
			if mergedRaw, rErr := os.ReadFile(td.MergedJSONLPath); rErr == nil {
				_, _ = out.Write(mergedRaw)
			}
		}
		if exp.Status == models.ExperimentFailed || exp.Status == models.ExperimentPartialFailure || exp.Status == models.ExperimentCancelled {
			return 1
		}
		return 0
	}

	// Single-VM `colab run` — ALL `[colab]` chatter goes strictly to `stderr`; `stdout` stays 100% pure!
	_, _ = fmt.Fprintf(errOut, "[colab] Creating session (%s)...\n", accel)
	var sess models.RuntimeSession
	_, err = client.doJSON(ctx, http.MethodPost, "/api/v1/sessions", models.SessionAllocRequest{
		Name:            sessName,
		Accelerator:     accel,
		HighMem:         highMem,
		Strategy:        models.RoutingStrategy(g.Strategy),
		AccountSelector: g.Account,
		AutoFallback:    g.Fallback != "none",
		ExperimentID:    g.ExpID,
	}, &sess)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Allocation failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(errOut, "[colab] Session READY (%s on %s).\n", sess.Name, sess.AccountEmail)

	if !keepVM {
		defer func() {
			_, _ = fmt.Fprintf(errOut, "[colab] Stopping session '%s'...\n", sess.Name)
			_, _ = client.doJSON(context.Background(), http.MethodDelete, fmt.Sprintf("/api/v1/sessions/%s", url.PathEscape(sess.ID)), nil, nil)
			_, _ = fmt.Fprintln(errOut, "[colab] Session terminated.")
		}()
	}

	codeToRun := string(scriptBytes)
	if len(scriptArgs) > 0 {
		argvJSON, _ := json.Marshal(append([]string{filepath.Base(scriptPath)}, scriptArgs...))
		codeToRun = fmt.Sprintf("import sys as __ch_sys; __ch_sys.argv = %s\n", string(argvJSON)) + codeToRun
	}

	var execRes models.KernelExecResult
	_, err = client.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/sessions/%s/exec", url.PathEscape(sess.ID)), models.KernelExecRequest{
		SessionID:      sess.ID,
		Code:           codeToRun,
		Env:            envMap,
		IdleTimeoutSec: timeoutSec,
		StoreHistory:   true,
	}, &execRes)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Execution failed: %v\n", err)
		return 1
	}

	// Pure script stdout ONLY on `out`!
	if execRes.Stdout != "" {
		_, _ = io.WriteString(out, execRes.Stdout)
	}
	if execRes.Stderr != "" {
		_, _ = io.WriteString(errOut, execRes.Stderr)
	}

	if execRes.ExecutionError != nil {
		if execRes.ExecutionError.Name == "SystemExit" && execRes.ExecutionError.ExitCode != nil {
			return *execRes.ExecutionError.ExitCode
		}
		for _, tb := range execRes.ExecutionError.Traceback {
			_, _ = fmt.Fprintln(errOut, tb)
		}
		return 1
	}
	return 0
}

func cliCmdSessions(ctx context.Context, client *daemonHTTPClient, g cliGlobalOpts, _ []string, out, errOut io.Writer) int {
	qs := url.Values{}
	qs.Set("active_only", "true")
	if g.Account != "" && g.Account != "all" {
		qs.Set("account_id", g.Account)
	}
	path := "/api/v1/sessions?" + qs.Encode()
	var list []models.RuntimeSession
	if _, err := client.doJSON(ctx, http.MethodGet, path, nil, &list); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	active := make([]models.RuntimeSession, 0, len(list))
	for _, s := range list {
		if s.Status != models.SessionStopped && s.Status != models.SessionLost {
			active = append(active, s)
		}
	}
	if g.JSON {
		enc, _ := json.MarshalIndent(active, "", "  ")
		_, _ = fmt.Fprintln(out, string(enc))
		return 0
	}
	if len(active) == 0 {
		_, _ = fmt.Fprintln(out, "[colab] No active sessions found on server.")
		return 0
	}
	for _, s := range active {
		shapeStr := "Standard"
		if s.Shape == models.ShapeHighRAM {
			shapeStr = "High-RAM"
		}
		_, _ = fmt.Fprintf(out, "[%s] %s | Hardware: %s | Shape: %s | Variant: %s | Account: %s\n",
			s.Name, s.Endpoint, s.Accelerator, shapeStr, s.Variant, s.AccountEmail)
	}
	return 0
}

func cliCmdStatus(ctx context.Context, client *daemonHTTPClient, g cliGlobalOpts, args []string, out, errOut io.Writer) int {
	var sessName string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-s" || args[i] == "--session") && i+1 < len(args) {
			i++
			sessName = args[i]
		}
	}
	if g.ExpID != "" {
		var exp models.Experiment
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/v1/experiments/"+url.PathEscape(g.ExpID), nil, &exp); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
			return 1
		}
		if g.JSON {
			enc, _ := json.MarshalIndent(exp, "", "  ")
			_, _ = fmt.Fprintln(out, string(enc))
			return 0
		}
		_, _ = fmt.Fprintf(out, "Experiment %s [%s] | Shards: %d/%d running | Synced: %d | Burn: %.2f CU/hr\n",
			exp.ID, exp.Status, exp.RunningShards, exp.NumShards, exp.SyncedRecordsTotal, exp.AggregateBurnRateCUHr)
		return 0
	}

	var list []models.RuntimeSession
	if sessName != "" {
		var single models.RuntimeSession
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/v1/sessions/"+url.PathEscape(sessName), nil, &single); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
			return 1
		}
		list = []models.RuntimeSession{single}
	} else {
		_, _ = client.doJSON(ctx, http.MethodGet, "/api/v1/sessions?active_only=true", nil, &list)
	}
	if g.JSON {
		enc, _ := json.MarshalIndent(list, "", "  ")
		_, _ = fmt.Fprintln(out, string(enc))
		return 0
	}
	for _, s := range list {
		shapeStr := "Standard"
		if s.Shape == models.ShapeHighRAM {
			shapeStr = "High-RAM"
		}
		_, _ = fmt.Fprintf(out, "[%s] %s | Hardware: %s | Shape: %s | Variant: %s | Status: %s\n",
			s.Name, s.Endpoint, s.Accelerator, shapeStr, s.Variant, s.Status)
		if s.LastExecutionAt != nil {
			_, _ = fmt.Fprintf(out, "  Last Execution: %s at %s\n",
				firstVal(s.LastExecutionFile, "<cell>"), s.LastExecutionAt.Format("2006-01-02 15:04:05"))
		}
	}
	return 0
}

func cliCmdStop(ctx context.Context, client *daemonHTTPClient, g cliGlobalOpts, args []string, out, errOut io.Writer) int {
	var sessName string
	stopAll := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-s", "--session":
			if i+1 < len(args) {
				i++
				sessName = args[i]
			}
		case "--all":
			stopAll = true
		}
	}

	if g.ExpID != "" {
		_, _ = fmt.Fprintf(out, "[colab] Stopping experiment '%s'...\n", g.ExpID)
		if _, err := client.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/experiments/%s/teardown", url.PathEscape(g.ExpID)), map[string]any{"force_kill_running": true}, nil); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintln(out, "[colab] Session terminated.")
		return 0
	}

	if stopAll {
		var list []models.RuntimeSession
		_, _ = client.doJSON(ctx, http.MethodGet, "/api/v1/sessions", nil, &list)
		for _, s := range list {
			if s.Status != models.SessionStopped && !s.AdoptedExternal {
				_, _ = fmt.Fprintf(out, "[colab] Stopping session '%s'...\n", s.Name)
				_, _ = client.doJSON(ctx, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(s.ID), nil, nil)
				_, _ = fmt.Fprintln(out, "[colab] Session terminated.")
			}
		}
		return 0
	}

	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "[colab] Stopping session '%s'...\n", sid)
	if _, err := client.doJSON(ctx, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(sid), nil, nil); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(out, "[colab] Session terminated.")
	return 0
}

func cliCmdRestartKernel(ctx context.Context, client *daemonHTTPClient, args []string, errOut io.Writer) int {
	var sessName string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-s" || args[i] == "--session") && i+1 < len(args) {
			i++
			sessName = args[i]
		}
	}
	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	if _, err := client.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/sessions/%s/restart-kernel", url.PathEscape(sid)), map[string]any{}, nil); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	return 0
}

func cliCmdUpload(ctx context.Context, client *daemonHTTPClient, args []string, out, errOut io.Writer) int {
	var sessName string
	var pos []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-s" || args[i] == "--session") && i+1 < len(args) {
			i++
			sessName = args[i]
		} else {
			pos = append(pos, args[i])
		}
	}
	if len(pos) < 2 {
		_, _ = fmt.Fprintln(errOut, "[colab] Usage: upload [-s SESSION] <LOCAL_PATH> <REMOTE_PATH>")
		return 1
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	if _, err := client.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/sessions/%s/files", url.PathEscape(sid)), map[string]any{
		"remote_path":    pos[1],
		"format":         "base64",
		"content_base64": b64,
	}, nil); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "[colab] Uploaded '%s' to '%s'\n", pos[0], pos[1])
	return 0
}

func cliCmdDownload(ctx context.Context, client *daemonHTTPClient, args []string, out, errOut io.Writer) int {
	var sessName string
	var pos []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-s" || args[i] == "--session") && i+1 < len(args) {
			i++
			sessName = args[i]
		} else {
			pos = append(pos, args[i])
		}
	}
	if len(pos) < 2 {
		_, _ = fmt.Fprintln(errOut, "[colab] Usage: download [-s SESSION] <REMOTE_PATH> <LOCAL_PATH>")
		return 1
	}
	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	raw, err := client.doRawGet(ctx, fmt.Sprintf("/api/v1/sessions/%s/files/download?path=%s", url.PathEscape(sid), url.QueryEscape(pos[0])))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	_ = os.MkdirAll(filepath.Dir(pos[1]), 0o755)
	if err := os.WriteFile(pos[1], raw, 0o644); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "[colab] Downloaded '%s' to '%s'\n", pos[0], pos[1])
	return 0
}

func cliCmdLS(ctx context.Context, client *daemonHTTPClient, args []string, out, errOut io.Writer) int {
	var sessName string
	remPath := "/content"
	for i := 0; i < len(args); i++ {
		if (args[i] == "-s" || args[i] == "--session") && i+1 < len(args) {
			i++
			sessName = args[i]
		} else if !strings.HasPrefix(args[i], "-") {
			remPath = args[i]
		}
	}
	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	var entries []models.RemoteFileEntry
	if _, err := client.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/files?path=%s", url.PathEscape(sid), url.QueryEscape(remPath)), nil, &entries); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	for _, e := range entries {
		if e.Type == "directory" {
			_, _ = fmt.Fprintf(out, "%s/\n", e.Name)
		} else {
			_, _ = fmt.Fprintf(out, "%s\n", e.Name)
		}
	}
	return 0
}

func cliCmdRM(ctx context.Context, client *daemonHTTPClient, args []string, out, errOut io.Writer) int {
	var sessName, remPath string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-s" || args[i] == "--session") && i+1 < len(args) {
			i++
			sessName = args[i]
		} else if !strings.HasPrefix(args[i], "-") {
			remPath = args[i]
		}
	}
	if remPath == "" {
		_, _ = fmt.Fprintln(errOut, "[colab] Missing REMOTE_PATH")
		return 1
	}
	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	if _, err := client.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/sessions/%s/files?path=%s", url.PathEscape(sid), url.QueryEscape(remPath)), nil, nil); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "[colab] Deleted %s\n", remPath)
	return 0
}

func cliCmdInstall(ctx context.Context, client *daemonHTTPClient, args []string, out, errOut io.Writer) int {
	var sessName, reqFile string
	var pkgs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-s", "--session":
			if i+1 < len(args) {
				i++
				sessName = args[i]
			}
		case "-r", "--requirement":
			if i+1 < len(args) {
				i++
				reqFile = args[i]
			}
		default:
			pkgs = append(pkgs, args[i])
		}
	}
	var reqTxt string
	if reqFile != "" {
		raw, err := os.ReadFile(reqFile)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
			return 1
		}
		reqTxt = string(raw)
	}
	if len(pkgs) == 0 && reqTxt == "" {
		_, _ = fmt.Fprintln(errOut, "[colab] Supply package names or -r requirements.txt")
		return 1
	}
	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "[colab] Installing packages on %s (preferring uv)...\n", sid)
	var res models.KernelExecResult
	if _, err := client.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/sessions/%s/install", url.PathEscape(sid)), map[string]any{
		"packages":         pkgs,
		"requirements_txt": reqTxt,
	}, &res); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	if res.Stdout != "" {
		_, _ = io.WriteString(out, res.Stdout)
	}
	if res.Stderr != "" {
		_, _ = io.WriteString(errOut, res.Stderr)
	}
	return 0
}

func cliCmdUsage(ctx context.Context, client *daemonHTTPClient, g cliGlobalOpts, args []string, out, errOut io.Writer) int {
	showGPUHours := false
	for _, a := range args {
		if a == "--gpu-hours" {
			showGPUHours = true
		}
	}

	var sum models.QuotaSummary
	if _, err := client.doJSON(ctx, http.MethodGet, "/api/v1/pool/summary?refresh=true", nil, &sum); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Warning: failed to fetch compute-unit info: %v\n", err)
		return 1
	}
	if g.JSON {
		enc, _ := json.MarshalIndent(sum, "", "  ")
		_, _ = fmt.Fprintln(out, string(enc))
		return 0
	}

	// Exact 3-line colab v0.8.0 stdout contract:
	_, _ = fmt.Fprintf(out, "Current balance: %.2f compute units\n", sum.TotalAvailableCU)
	_, _ = fmt.Fprintf(out, "Usage rate: %.2f/hr\n", sum.ActiveBurnRateCUHr)
	_, _ = fmt.Fprintf(out, "Active assignments: %d\n", sum.ActiveVMs)

	if showGPUHours && len(sum.GPUEquivalence) > 0 {
		_, _ = fmt.Fprintln(out, "\nCloud GPU Equivalent Remaining Runtime (Schedulable):")
		for _, tier := range models.AllAccelerators {
			if eq, ok := sum.GPUEquivalence[tier]; ok {
				_, _ = fmt.Fprintf(out, "  %-14s | %7.2f hrs @ %5.2f CU/hr (%d eligible accounts)\n",
					eq.ShortLabel, eq.SchedulableHours, eq.BurnRateCUHr, eq.EligibleAccounts)
			}
		}
	}
	return 0
}

func cliCmdLog(ctx context.Context, client *daemonHTTPClient, args []string, out, errOut io.Writer) int {
	limit := 50
	var outPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n", "--lines":
			if i+1 < len(args) {
				i++
				limit, _ = strconv.Atoi(args[i])
			}
		case "-o", "--output":
			if i+1 < len(args) {
				i++
				outPath = args[i]
			}
		}
	}
	var traces []models.ToolTraceEvent
	if _, err := client.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v1/router/traces?limit=%d", limit), nil, &traces); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
		return 1
	}
	if outPath != "" {
		var buf strings.Builder
		for _, tr := range traces {
			b, _ := json.Marshal(tr)
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if err := os.WriteFile(outPath, []byte(buf.String()), 0o644); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colab] %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(out, "[colab] Exported history to '%s'.\n", outPath)
		return 0
	}
	for _, tr := range traces {
		_, _ = fmt.Fprintf(out, "[%s] %s (%s) -> %s [%dms]\n",
			tr.Timestamp.Format("2006-01-02 15:04:05"), tr.ToolOrEndpoint, tr.Caller, tr.Status, tr.DurationMs)
	}
	return 0
}

func cliCmdURL(ctx context.Context, client *daemonHTTPClient, args []string, out, errOut io.Writer) int {
	var sessName string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-s" || args[i] == "--session") && i+1 < len(args) {
			i++
			sessName = args[i]
		}
	}
	sid, err := resolveDefaultCLISession(ctx, client, sessName)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Session '%s' not found.\n", sessName)
		return 1
	}
	var res struct {
		URL string `json:"url"`
	}
	if _, err := client.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/open-url", url.PathEscape(sid)), nil, &res); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Session '%s' not found.\n", sid)
		return 1
	}
	// Exact single raw pipeable line (NO `[colab]` prefix)
	_, _ = fmt.Fprintln(out, res.URL)
	return 0
}

func cliCmdAuth(ctx context.Context, client *daemonHTTPClient, sub string, args []string, in io.Reader, out, errOut io.Writer) int {
	if sub == "whoami" || (len(args) > 0 && args[0] == "status") || len(args) == 0 {
		var accs []models.Account
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/v1/accounts", nil, &accs); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colab] auth check failed: %v\n", err)
			return 1
		}
		if len(accs) == 0 {
			_, _ = fmt.Fprintln(out, "No Google accounts pooled yet. Run 'colabhive auth login' or open http://localhost:7860/pool.")
			return 0
		}
		for _, a := range accs {
			_, _ = fmt.Fprintf(out, "Account: %s | Tier: %s | Health: %s | Balance: %.2f CU\n",
				a.Email, a.SubscriptionTier, a.Health, a.PaidCUBalance)
		}
		return 0
	}

	// `colabhive auth login` -> interactive PKCE copy-paste flow
	var pending models.OAuthPendingSession
	if _, err := client.doJSON(ctx, http.MethodPost, "/api/v1/accounts/oauth/start", models.OAuthStartRequest{
		Mode: models.OAuthModeRemoteCopyPaste,
	}, &pending); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] Failed to start OAuth: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "Open this Google authorization URL in your browser:\n\n  %s\n\nPaste authorization code or redirect URL: ", pending.AuthURL)
	reader := bufio.NewReader(in)
	pasted, _ := reader.ReadString('\n')
	var acc models.Account
	if _, err := client.doJSON(ctx, http.MethodPost, "/api/v1/accounts/oauth/exchange", map[string]any{
		"state":     pending.State,
		"raw_input": strings.TrimSpace(pasted),
		"code":      strings.TrimSpace(pasted),
	}, &acc); err != nil {
		_, _ = fmt.Fprintf(errOut, "[colab] OAuth exchange failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "[OK] Added %s (%s · %.2f CU)\n", acc.Email, acc.SubscriptionTier, acc.PaidCUBalance)
	return 0
}

func isUnderSystemTempDir(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	absP, err1 := filepath.Abs(p)
	absTmp, err2 := filepath.Abs(os.TempDir())
	if err1 != nil || err2 != nil {
		return false
	}
	rel, rErr := filepath.Rel(absTmp, absP)
	if rErr != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func newLocalAgentInjector(explicitHome string, includeWSL2 bool) *router.Service {
	customHome := firstVal(explicitHome, os.Getenv("COLABHIVE_HOME"))
	homeDir := customHome
	if homeDir == "" {
		if h, err := os.UserHomeDir(); err == nil && strings.TrimSpace(h) != "" {
			homeDir = h
		} else {
			homeDir = firstVal(os.Getenv("HOME"), os.Getenv("USERPROFILE"))
		}
	}

	appDataDir := ""
	if customHome != "" {
		appDataDir = filepath.Join(homeDir, "AppData", "Roaming")
	} else {
		appDataDir = firstVal(os.Getenv("APPDATA"), filepath.Join(homeDir, "AppData", "Roaming"))
	}

	wslRoot := ""
	if includeWSL2 && customHome == "" && !isUnderSystemTempDir(homeDir) {
		wslRoot = firstVal(os.Getenv("COLABHIVE_WSL_ROOT"), `\\wsl.localhost`)
	}

	return router.NewServiceWithDirs(nil, nil, nil, homeDir, appDataDir, wslRoot, goruntime.GOOS)
}

func resolveConnectServerURL(explicit string) string {
	base := strings.TrimRight(strings.TrimSpace(explicit), "/")
	if base != "" {
		return base
	}
	if envURL := strings.TrimRight(strings.TrimSpace(os.Getenv("COLABHIVE_URL")), "/"); envURL != "" {
		return envURL
	}
	if info, err := ReadDaemonInfo(); err == nil && info != nil && strings.TrimSpace(info.URL) != "" {
		return strings.TrimRight(strings.TrimSpace(info.URL), "/")
	}
	return "http://127.0.0.1:7860"
}

func cliCmdConnect(ctx context.Context, g cliGlobalOpts, args []string, out, errOut io.Writer, remove bool) int {
	all := false
	includeWSL2 := true
	var targets []models.AgentClientID
	transport := ""
	serverOverride := g.ServerURL
	keyOverride := g.APIKey
	homeOverride := ""

	for i := 0; i < len(args); i++ {
		a := args[i]
		nextVal := func() string {
			if idx := strings.IndexByte(a, '='); idx >= 0 {
				return a[idx+1:]
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--all":
			all = true
		case a == "--wsl":
			includeWSL2 = true
		case a == "--no-wsl":
			includeWSL2 = false
		case a == "--transport" || strings.HasPrefix(a, "--transport="):
			transport = strings.ToLower(strings.TrimSpace(nextVal()))
		case a == "--server" || strings.HasPrefix(a, "--server="):
			serverOverride = nextVal()
		case a == "--key" || strings.HasPrefix(a, "--key="),
			a == "--api-key" || strings.HasPrefix(a, "--api-key="):
			keyOverride = nextVal()
		case a == "--home" || strings.HasPrefix(a, "--home="):
			homeOverride = nextVal()
		default:
			if strings.HasPrefix(a, "-") {
				_, _ = fmt.Fprintf(errOut, "[colabhive] Unknown flag %s for %s\n", a, map[bool]string{false: "connect", true: "disconnect"}[remove])
				return 2
			}
			targets = append(targets, models.AgentClientID(strings.ToLower(strings.TrimSpace(a))))
		}
	}

	if transport != "" && transport != "http" && transport != "stdio" {
		_, _ = fmt.Fprintf(errOut, "[colabhive] Invalid --transport %q (expected 'http' or 'stdio')\n", transport)
		return 2
	}

	if all || len(targets) == 0 {
		targets = models.AllSupportedAgentClients
	}

	baseURL := resolveConnectServerURL(serverOverride)
	apiKey := router.SanitizeInjectorAPIKey(firstVal(keyOverride, os.Getenv("COLABHIVE_API_KEY")))
	localInjector := newLocalAgentInjector(homeOverride, includeWSL2)

	var results []models.AgentClientConfigStatus
	hadErr := false

	for _, cid := range targets {
		if remove {
			st, err := localInjector.RemoveAgentConfig(ctx, cid)
			if err != nil {
				_, _ = fmt.Fprintf(errOut, "[colabhive] %s: %v\n", cid, err)
				hadErr = true
				continue
			}
			results = append(results, *st)
			if !g.JSON {
				_, _ = fmt.Fprintf(out, "[removed] %-16s (%s)\n", st.DisplayName, st.ConfigPath)
			}
		} else {
			effTransport := transport
			// When connecting to a remote non-loopback cloud server without explicit `--transport`,
			// default all clients (including claude-desktop if HTTP requested, or standard defaults) cleanly.
			if effTransport == "" && cid != models.AgentClaudeDesktop {
				effTransport = "http"
			}
			st, err := localInjector.InjectAgentConfig(ctx, models.AgentInjectRequest{
				ClientID:      cid,
				ServerBaseURL: baseURL,
				APIKey:        apiKey,
				Transport:     effTransport,
				IncludeWSL2:   includeWSL2,
			})
			if err != nil {
				_, _ = fmt.Fprintf(errOut, "[colabhive] %s: %v\n", cid, err)
				hadErr = true
				continue
			}
			results = append(results, *st)
			if !g.JSON {
				_, _ = fmt.Fprintf(out, "[injected] %-16s -> %s (%s · %s/mcp)\n", st.DisplayName, st.ConfigPath, st.Transport, baseURL)
			}
		}
	}

	if g.JSON && len(results) > 0 {
		enc, _ := json.MarshalIndent(results, "", "  ")
		_, _ = fmt.Fprintln(out, string(enc))
	}

	if hadErr {
		return 1
	}
	return 0
}

func cliCmdPool(ctx context.Context, client *daemonHTTPClient, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(errOut, "Usage: colabhive pool <export|import> [options]")
		return 2
	}
	switch args[0] {
	case "export":
		outFile := "pool.chvault"
		encrypt := false
		pass := ""
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "-o", "--output":
				if i+1 < len(args) {
					i++
					outFile = args[i]
				}
			case "--encrypt":
				encrypt = true
			case "--passphrase":
				if i+1 < len(args) {
					i++
					pass = args[i]
				}
			}
		}
		var bundle models.CredentialBundle
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/v1/pool/export", models.CredentialExportRequest{
			IncludeProxies: true,
			Encrypt:        encrypt,
			Passphrase:     pass,
		}, &bundle); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colabhive] export failed: %v\n", err)
			return 1
		}
		enc, _ := json.MarshalIndent(bundle, "", "  ")
		if err := os.WriteFile(outFile, append(enc, '\n'), 0o600); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colabhive] %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(out, "[colabhive] Exported credential bundle to %s\n", outFile)
		return 0

	case "import":
		if len(args) < 2 {
			_, _ = fmt.Fprintln(errOut, "Usage: colabhive pool import <file> [--passphrase PASS]")
			return 2
		}
		raw, err := os.ReadFile(args[1])
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "[colabhive] %v\n", err)
			return 1
		}
		pass := ""
		for i := 2; i < len(args); i++ {
			if args[i] == "--passphrase" && i+1 < len(args) {
				i++
				pass = args[i]
			}
		}
		var imported []models.Account
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/v1/pool/import", models.CredentialImportRequest{
			Format:     "chvault",
			RawPayload: string(raw),
			Passphrase: pass,
		}, &imported); err != nil {
			_, _ = fmt.Fprintf(errOut, "[colabhive] import failed: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(out, "[colabhive] Imported %d account(s).\n", len(imported))
		return 0
	default:
		_, _ = fmt.Fprintf(errOut, "[colabhive] Unknown pool subcommand %q\n", args[0])
		return 2
	}
}

func resolveDefaultCLISession(ctx context.Context, client *daemonHTTPClient, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	var list []models.RuntimeSession
	if _, err := client.doJSON(ctx, http.MethodGet, "/api/v1/sessions", nil, &list); err != nil {
		return "", err
	}
	var active []models.RuntimeSession
	for _, s := range list {
		if s.Status != models.SessionStopped && s.Status != models.SessionLost && !s.AdoptedExternal {
			active = append(active, s)
		}
	}
	if len(active) == 1 {
		return active[0].Name, nil
	}
	if len(active) == 0 {
		return "", fmt.Errorf("no active sessions found; create one with 'new' or pass -s <session>")
	}
	return "", fmt.Errorf("multiple active sessions (%d); specify target with -s <session>", len(active))
}

func printCLIUsage(prog string, out io.Writer, ver VersionInfo) {
	_, _ = fmt.Fprintf(out, `ColabHive (%s %s) — Multi-Account Google Colab GPU Observatory, MCP Server & Drop-In CLI

Control Plane & Agent Commands:
  %s serve [--mode local|cloud] [--port 7860] [--admin-token TOK] [--trust-proxy]
  %s mcp [stdio] [--strategy waste-zero] [--account <id>]
  %s connect [<agent>|--all] [--server URL] [--key ch_sk_...] [--transport http|stdio]
  %s disconnect [<agent>|--all]       Remove ColabHive MCP entry & preserve sibling configs
  %s pool <export|import> [file]      Portable AES-256-GCM .chvault pool migration

Drop-In Google Colab CLI Commands (100%% colab v0.8.0 compatible + Pool flags):
  %s new [-s NAME] [--gpu G4|H100|A100|L4|T4] [--tpu v6e1|v5e1] [--high-mem]
  %s run [--gpu G4] [--shards K] [--strategy waste-zero] <script.py> [args...]
  %s exec [-s NAME] [-f script.py|nb.ipynb] [--timeout 120] [--env K=V] [code]
  %s sessions [--account <id|all>] [--json]
  %s status [-s NAME] [--experiment EXP] [--json]
  %s stop [-s NAME | --all | --experiment EXP]
  %s restart-kernel [-s NAME]
  %s upload [-s NAME] <LOCAL_PATH> <REMOTE_PATH>
  %s download [-s NAME] <REMOTE_PATH> <LOCAL_PATH>
  %s ls [-s NAME] [REMOTE_PATH]
  %s rm [-s NAME] <REMOTE_PATH>
  %s install [-s NAME] [-r requirements.txt] [PACKAGES...]
  %s usage [--gpu-hours] [--json]
  %s url [-s NAME]
  %s log [-n LINES] [-o history.jsonl]
  %s auth [login|status]

Pool Routing Flags (accepted on any command):
  --server <URL>   --api-key <ch_sk_...>   --account <id|email|tag:X>
  --strategy <free-first|waste-zero|max-balance|sticky-session|round-robin|free-opportunistic>
  --shards <K>     --experiment <EXP_ID>   --fallback <auto|none>
`,
		prog, firstVal(ver.Version, "1.0.0"),
		prog, prog, prog, prog, prog,
		prog, prog, prog, prog, prog, prog, prog, prog, prog, prog, prog, prog, prog, prog, prog, prog,
	)
}
