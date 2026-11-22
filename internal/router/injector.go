package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/colabhive/colabhive/internal/models"
)

const defaultServerBaseURL = "http://127.0.0.1:7860"

type clientTargetSpec struct {
	id               models.AgentClientID
	displayName      string
	format           string // "json" | "toml" | "jsonc"
	defaultTransport string // "http" | "stdio"
	hotReloadNote    string
}

var clientSpecs = []clientTargetSpec{
	{
		id:               models.AgentAntigravity,
		displayName:      "Antigravity (AGY)",
		format:           "json",
		defaultTransport: "http",
		hotReloadNote:    "Auto-discovered on next session or MCP refresh",
	},
	{
		id:               models.AgentClaudeCode,
		displayName:      "Claude Code",
		format:           "json",
		defaultTransport: "http",
		hotReloadNote:    "Immediate (run `claude mcp list` to verify)",
	},
	{
		id:               models.AgentClaudeDesktop,
		displayName:      "Claude Desktop",
		format:           "json",
		defaultTransport: "stdio",
		hotReloadNote:    "Restart Claude Desktop to load updated stdio MCP bridge",
	},
	{
		id:               models.AgentCodex,
		displayName:      "OpenAI Codex",
		format:           "toml",
		defaultTransport: "http",
		hotReloadNote:    "Restart `codex` CLI or reload Codex Desktop session",
	},
	{
		id:               models.AgentGeminiCLI,
		displayName:      "Gemini CLI",
		format:           "json",
		defaultTransport: "http",
		hotReloadNote:    "Restart `gemini` CLI to reload settings.json",
	},
	{
		id:               models.AgentCursor,
		displayName:      "Cursor",
		format:           "json",
		defaultTransport: "http",
		hotReloadNote:    "Immediate in Cursor Settings -> MCP panel",
	},
	{
		id:               models.AgentWindsurf,
		displayName:      "Windsurf",
		format:           "json",
		defaultTransport: "http",
		hotReloadNote:    "Click Refresh in Windsurf Cascade MCP panel",
	},
	{
		id:               models.AgentOpenCode,
		displayName:      "OpenCode",
		format:           "jsonc",
		defaultTransport: "http",
		hotReloadNote:    "Preserves // comments; restart `opencode` session",
	},
}

func runtimeGOOS() string {
	return runtime.GOOS
}

func getClientSpec(id models.AgentClientID) (clientTargetSpec, error) {
	for _, sp := range clientSpecs {
		if sp.id == id {
			return sp, nil
		}
	}
	return clientTargetSpec{}, fmt.Errorf("unsupported agent client %q", id)
}

func normalizeServerURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return defaultServerBaseURL
	}
	return trimmed
}

// SanitizeInjectorAPIKey strips known UI placeholder tokens (`ch_sk_live_default`,
// masked prefixes containing `...` or `xxxx`, or synthesized `*_live_token` values)
// so broken fake tokens are NEVER written into user MCP configs.
func SanitizeInjectorAPIKey(raw string) string {
	k := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(k), "bearer ") {
		k = strings.TrimSpace(k[7:])
	}
	if k == "" {
		return ""
	}
	lower := strings.ToLower(k)
	if lower == "none" || lower == "null" || lower == "open" ||
		lower == "ch_sk_live_default" || lower == "ch_sk_test_default" ||
		strings.Contains(k, "...") || strings.Contains(k, "…") ||
		strings.Contains(lower, "xxxx") ||
		strings.HasSuffix(lower, "_live_token") {
		return ""
	}
	return k
}

// ResolveStdioCommand resolves the real absolute `colabhive` executable path for `stdio` MCP configs,
// checking a sibling `colabhive.exe` / `colabhive` binary when invoked from `colabhive-desktop`,
// and falling back to `"colabhive"` only for WSL2 targets or `go test` binaries.
func ResolveStdioCommand(exePath string, isWSL bool) string {
	if isWSL {
		return "colabhive"
	}
	cleaned := strings.TrimSpace(exePath)
	if cleaned == "" {
		return "colabhive"
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && strings.TrimSpace(resolved) != "" {
		cleaned = resolved
	}
	baseLower := strings.ToLower(filepath.Base(cleaned))
	lowerFull := strings.ToLower(cleaned)
	if strings.HasSuffix(baseLower, ".test") ||
		strings.HasSuffix(baseLower, ".test.exe") ||
		strings.HasPrefix(baseLower, "__debug_bin") ||
		strings.Contains(lowerFull, "go-build") {
		return "colabhive"
	}

	// If invoked from `colabhive-desktop.exe` (or another wrapper), prefer sibling `colabhive(.exe)` if present.
	if baseLower != "colabhive" && baseLower != "colabhive.exe" {
		dir := filepath.Dir(cleaned)
		sibCandidates := []string{"colabhive"}
		if strings.HasSuffix(baseLower, ".exe") || runtime.GOOS == "windows" {
			sibCandidates = []string{"colabhive.exe", "colabhive"}
		}
		for _, sib := range sibCandidates {
			cand := filepath.Join(dir, sib)
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				return cand
			}
		}
	}
	return cleaned
}

func resolveStdioExecutable(isWSL bool) string {
	if isWSL {
		return "colabhive"
	}
	if envBin := strings.TrimSpace(os.Getenv("COLABHIVE_BIN_PATH")); envBin != "" {
		return ResolveStdioCommand(envBin, false)
	}
	exePath, err := os.Executable()
	if err != nil {
		return "colabhive"
	}
	return ResolveStdioCommand(exePath, false)
}

func (s *Service) resolveClientPaths(clientID models.AgentClientID) (hostPath string, wslPath string) {
	home := s.homeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	goos := s.goos
	if goos == "" {
		goos = runtime.GOOS
	}

	var wslCandidates []string
	var wslFallback string
	switch clientID {
	case models.AgentAntigravity:
		agy2Path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
		agy1Path := filepath.Join(home, ".gemini", "antigravity", "mcp_config.json")
		if info, err := os.Stat(agy2Path); err == nil && !info.IsDir() {
			hostPath = agy2Path
		} else if info1, err1 := os.Stat(agy1Path); err1 == nil && !info1.IsDir() {
			hostPath = agy1Path
		} else {
			hostPath = agy2Path
		}
		wslCandidates = []string{".gemini/config/mcp_config.json", ".gemini/antigravity/mcp_config.json"}
		wslFallback = ".gemini/config/mcp_config.json"
	case models.AgentClaudeCode:
		hostPath = filepath.Join(home, ".claude.json")
		wslCandidates = []string{".claude.json"}
		wslFallback = ".claude.json"
	case models.AgentClaudeDesktop:
		switch goos {
		case "windows":
			appData := s.appData
			if appData == "" {
				appData = filepath.Join(home, "AppData", "Roaming")
			}
			hostPath = filepath.Join(appData, "Claude", "claude_desktop_config.json")
		case "darwin":
			hostPath = filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
		default:
			hostPath = filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
		}
	case models.AgentCodex:
		hostPath = filepath.Join(home, ".codex", "config.toml")
		wslCandidates = []string{".codex/config.toml"}
		wslFallback = ".codex/config.toml"
	case models.AgentGeminiCLI:
		hostPath = filepath.Join(home, ".gemini", "settings.json")
		wslCandidates = []string{".gemini/settings.json"}
		wslFallback = ".gemini/settings.json"
	case models.AgentCursor:
		hostPath = filepath.Join(home, ".cursor", "mcp.json")
		wslCandidates = []string{".cursor/mcp.json"}
		wslFallback = ".cursor/mcp.json"
	case models.AgentWindsurf:
		hostPath = filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
		wslCandidates = []string{".codeium/windsurf/mcp_config.json"}
		wslFallback = ".codeium/windsurf/mcp_config.json"
	case models.AgentOpenCode:
		jsoncPath := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		jsonPath := filepath.Join(home, ".config", "opencode", "opencode.json")
		if info, err := os.Stat(jsoncPath); err == nil && !info.IsDir() {
			hostPath = jsoncPath
		} else if infoJ, errJ := os.Stat(jsonPath); errJ == nil && !infoJ.IsDir() {
			hostPath = jsonPath
		} else {
			hostPath = jsonPath
		}
		wslCandidates = []string{".config/opencode/opencode.jsonc", ".config/opencode/opencode.json"}
		wslFallback = ".config/opencode/opencode.json"
	}

	if goos == "windows" && len(wslCandidates) > 0 && s.wslRoot != "" {
		wslPath = discoverWSLUserFileCandidates(s.wslRoot, wslCandidates, wslFallback)
	}
	return hostPath, wslPath
}

func discoverWSLUserFileCandidates(wslRoot string, relSlashPaths []string, defaultRelSlash string) string {
	entries, err := os.ReadDir(wslRoot)
	if err != nil {
		return ""
	}
	var fallbackCandidate string
	defaultNative := filepath.FromSlash(defaultRelSlash)
	for _, distro := range entries {
		if !distro.IsDir() {
			continue
		}
		homeRoot := filepath.Join(wslRoot, distro.Name(), "home")
		users, uErr := os.ReadDir(homeRoot)
		if uErr != nil {
			continue
		}
		for _, u := range users {
			if !u.IsDir() {
				continue
			}
			for _, rel := range relSlashPaths {
				candidate := filepath.Join(homeRoot, u.Name(), filepath.FromSlash(rel))
				if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
					return candidate
				}
			}
			if fallbackCandidate == "" && defaultNative != "" {
				defCand := filepath.Join(homeRoot, u.Name(), defaultNative)
				if info, pErr := os.Stat(filepath.Dir(defCand)); pErr == nil && info.IsDir() {
					fallbackCandidate = defCand
				}
			}
		}
	}
	return fallbackCandidate
}

func (s *Service) backupPaths(clientID models.AgentClientID, format string) (firstWritePath string, timestampedPath string) {
	home := s.homeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	baseDir := filepath.Join(home, ".colabhive", "backups")
	firstDir := filepath.Join(baseDir, "first-write")
	firstWritePath = filepath.Join(firstDir, fmt.Sprintf("%s.%s.bak", clientID, format))
	ts := time.Now().UTC().Format("20060102T150405Z")
	timestampedPath = filepath.Join(baseDir, fmt.Sprintf("%s-%s.%s.bak", clientID, ts, format))
	return firstWritePath, timestampedPath
}

// ListAgentClientStatuses inspects all 8 supported AI coding tools on the host (and WSL2 on Windows),
// reporting detection status, backup state, surgical injection status, and ready-to-copy snippets.
func (s *Service) ListAgentClientStatuses(
	_ context.Context,
	serverBaseURL string,
	rawAPIKey string,
) ([]models.AgentClientConfigStatus, error) {
	baseURL := normalizeServerURL(serverBaseURL)
	key := SanitizeInjectorAPIKey(rawAPIKey)

	statuses := make([]models.AgentClientConfigStatus, 0, len(clientSpecs))
	for _, spec := range clientSpecs {
		st := s.inspectClientStatus(spec, baseURL, key, spec.defaultTransport)
		statuses = append(statuses, st)
	}
	return statuses, nil
}

func (s *Service) inspectClientStatus(
	spec clientTargetSpec,
	baseURL string,
	apiKey string,
	transport string,
) models.AgentClientConfigStatus {
	apiKey = SanitizeInjectorAPIKey(apiKey)
	if transport != "http" && transport != "stdio" {
		transport = spec.defaultTransport
	}
	hostPath, wslPath := s.resolveClientPaths(spec.id)
	firstBackup, _ := s.backupPaths(spec.id, spec.format)

	detected := false
	injected := false
	var lastSynced *time.Time

	if info, err := os.Stat(hostPath); err == nil && !info.IsDir() {
		detected = true
		if raw, rErr := os.ReadFile(hostPath); rErr == nil {
			injected = isColabHiveInjected(spec, raw)
			if injected {
				mod := info.ModTime().UTC()
				lastSynced = &mod
			}
		}
	} else if dirInfo, dErr := os.Stat(filepath.Dir(hostPath)); dErr == nil && dirInfo.IsDir() {
		detected = true
	}

	backupExists := false
	backupPath := ""
	if _, bErr := os.Stat(firstBackup); bErr == nil {
		backupExists = true
		backupPath = firstBackup
	}

	// Only populate DeepLinkURI when generating a real OS-registered scheme (never unregistered `colabhive://`).
	deepLink := ""
	if spec.id == models.AgentCursor && transport == "http" {
		if entryJSON, err := json.Marshal(buildColabHiveJSONEntry(models.AgentCursor, baseURL, apiKey, "http", false)); err == nil {
			deepLink = "cursor://anysphere.cursor-deeplink/mcp/install?name=colabhive&config=" +
				url.QueryEscape(base64.StdEncoding.EncodeToString(entryJSON))
		}
	}

	return models.AgentClientConfigStatus{
		ClientID:      spec.id,
		DisplayName:   spec.displayName,
		ConfigPath:    hostPath,
		WSLConfigPath: wslPath,
		Format:        spec.format,
		Transport:     transport,
		Detected:      detected,
		Injected:      injected,
		BackupExists:  backupExists,
		BackupPath:    backupPath,
		HotReloadNote: spec.hotReloadNote,
		Snippet:       buildConfigSnippet(spec, baseURL, apiKey, transport),
		CLICommand:    buildCLICommand(spec.id, baseURL, apiKey),
		DeepLinkURI:   deepLink,
		LastSyncedAt:  lastSynced,
	}
}

// InjectAgentConfig performs an atomic, backup-first surgical injection of `colabhive`
// into the target AI coding agent's configuration file without touching unrelated keys or comments.
func (s *Service) InjectAgentConfig(_ context.Context, req models.AgentInjectRequest) (*models.AgentClientConfigStatus, error) {
	spec, err := getClientSpec(req.ClientID)
	if err != nil {
		return nil, err
	}

	baseURL := normalizeServerURL(req.ServerBaseURL)
	apiKey := SanitizeInjectorAPIKey(req.APIKey)
	transport := strings.ToLower(strings.TrimSpace(req.Transport))
	if transport != "http" && transport != "stdio" {
		transport = spec.defaultTransport
	}

	hostPath, wslPath := s.resolveClientPaths(spec.id)
	if err := s.surgicallyWriteClientFile(spec, hostPath, baseURL, apiKey, transport, false); err != nil {
		return nil, err
	}

	if req.IncludeWSL2 && wslPath != "" {
		_ = s.surgicallyWriteClientFile(spec, wslPath, baseURL, apiKey, transport, true)
	}

	status := s.inspectClientStatus(spec, baseURL, apiKey, transport)
	now := time.Now().UTC()
	status.LastSyncedAt = &now
	return &status, nil
}

// RemoveAgentConfig removes ONLY ColabHive's MCP entry from the target agent's config file,
// preserving all other MCP servers, hooks, settings, and user comments.
func (s *Service) RemoveAgentConfig(_ context.Context, clientID models.AgentClientID) (*models.AgentClientConfigStatus, error) {
	spec, err := getClientSpec(clientID)
	if err != nil {
		return nil, err
	}

	hostPath, wslPath := s.resolveClientPaths(spec.id)
	if _, statErr := os.Stat(hostPath); statErr == nil {
		if err := s.surgicallyRemoveFromFile(spec, hostPath); err != nil {
			return nil, err
		}
	}
	if wslPath != "" {
		if _, statErr := os.Stat(wslPath); statErr == nil {
			_ = s.surgicallyRemoveFromFile(spec, wslPath)
		}
	}

	status := s.inspectClientStatus(spec, defaultServerBaseURL, "", spec.defaultTransport)
	return &status, nil
}

func (s *Service) surgicallyWriteClientFile(
	spec clientTargetSpec,
	targetPath string,
	baseURL string,
	apiKey string,
	transport string,
	isWSL bool,
) error {
	apiKey = SanitizeInjectorAPIKey(apiKey)
	var existing []byte
	fileExisted := false
	if raw, err := os.ReadFile(targetPath); err == nil {
		existing = raw
		fileExisted = true
	}

	// Backup-first invariant: preserve untouched original in ~/.colabhive/backups/first-write/ before first write.
	if fileExisted {
		suffix := ""
		if isWSL {
			suffix = "-wsl2"
		}
		firstBackup, tsBackup := s.backupPaths(models.AgentClientID(string(spec.id)+suffix), spec.format)
		if err := os.MkdirAll(filepath.Dir(firstBackup), 0o700); err != nil {
			return fmt.Errorf("failed to create backup directory: %w", err)
		}
		if _, err := os.Stat(firstBackup); os.IsNotExist(err) {
			if err := atomicWriteBytes(firstBackup, existing, 0o600); err != nil {
				return fmt.Errorf("failed to write first-write backup: %w", err)
			}
		}
		_ = atomicWriteBytes(tsBackup, existing, 0o600)
	}

	var updated []byte
	var err error
	switch spec.format {
	case "toml":
		updated, err = injectCodexTOML(existing, baseURL, apiKey, transport, isWSL)
	case "jsonc":
		updated, err = injectOpenCodeJSONC(existing, baseURL, apiKey, transport, isWSL)
	default:
		updated, err = injectStandardJSON(spec.id, existing, baseURL, apiKey, transport, isWSL)
	}
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory for %s: %w", spec.id, err)
	}
	return atomicWriteBytes(targetPath, updated, 0o600)
}

func (s *Service) surgicallyRemoveFromFile(spec clientTargetSpec, targetPath string) error {
	existing, err := os.ReadFile(targetPath)
	if err != nil {
		return err
	}

	var updated []byte
	switch spec.format {
	case "toml":
		updated, err = removeCodexTOML(existing)
	case "jsonc":
		updated, err = removeOpenCodeJSONC(existing)
	default:
		updated, err = removeStandardJSON(existing)
	}
	if err != nil {
		return err
	}
	return atomicWriteBytes(targetPath, updated, 0o600)
}

func atomicWriteBytes(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".colabhive-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, wErr := tmp.Write(data); wErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return wErr
	}
	if cErr := tmp.Close(); cErr != nil {
		_ = os.Remove(tmpName)
		return cErr
	}
	_ = os.Chmod(tmpName, perm)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*20) * time.Millisecond)
		}
		if rErr := os.Rename(tmpName, path); rErr == nil {
			return nil
		} else {
			lastErr = rErr
		}
	}

	// Non-destructive swap fallback: NEVER delete `path` before securing a restorable backup!
	bakPath := fmt.Sprintf("%s.colabhive-swap-%d", path, time.Now().UnixNano())
	if bErr := os.Rename(path, bakPath); bErr == nil {
		if rErr2 := os.Rename(tmpName, path); rErr2 == nil {
			_ = os.Remove(bakPath)
			return nil
		} else {
			_ = os.Rename(bakPath, path)
			_ = os.Remove(tmpName)
			return rErr2
		}
	}

	_ = os.Remove(tmpName)
	return lastErr
}

// ============================================================================
// Format-Specific Surgical Codecs (JSON, Comment-Preserving JSONC, TOML)
// ============================================================================

func buildColabHiveJSONEntry(
	clientID models.AgentClientID,
	baseURL string,
	apiKey string,
	transport string,
	isWSL bool,
) map[string]any {
	apiKey = SanitizeInjectorAPIKey(apiKey)
	if transport == "stdio" {
		cmdBin := resolveStdioExecutable(isWSL)
		env := map[string]string{
			"COLABHIVE_URL": baseURL,
		}
		if apiKey != "" {
			env["COLABHIVE_API_KEY"] = apiKey
		}
		entry := map[string]any{
			"command": cmdBin,
			"args":    []string{"mcp", "stdio"},
			"env":     env,
		}
		if clientID == models.AgentClaudeCode {
			entry["type"] = "stdio"
		}
		return entry
	}

	mcpURL := baseURL + "/mcp"

	switch clientID {
	case models.AgentClaudeCode:
		entry := map[string]any{
			"type": "http",
			"url":  mcpURL,
		}
		if apiKey != "" {
			entry["headers"] = map[string]string{
				"Authorization": "Bearer " + apiKey,
			}
		}
		return entry

	case models.AgentGeminiCLI:
		entry := map[string]any{
			"httpUrl": mcpURL,
			"timeout": 1800000,
		}
		if apiKey != "" {
			entry["headers"] = map[string]string{
				"Authorization": "Bearer " + apiKey,
			}
		}
		return entry

	case models.AgentWindsurf:
		entry := map[string]any{
			"serverUrl": mcpURL,
		}
		if apiKey != "" {
			entry["headers"] = map[string]string{
				"Authorization": "Bearer " + apiKey,
			}
		}
		return entry

	case models.AgentOpenCode:
		entry := map[string]any{
			"type":    "remote",
			"url":     mcpURL,
			"enabled": true,
		}
		if apiKey != "" {
			entry["headers"] = map[string]string{
				"Authorization": "Bearer " + apiKey,
			}
		}
		return entry

	default: // Antigravity, Cursor, etc.
		entry := map[string]any{
			"url": mcpURL,
		}
		if apiKey != "" {
			entry["headers"] = map[string]string{
				"Authorization": "Bearer " + apiKey,
			}
		}
		return entry
	}
}

func injectStandardJSON(
	clientID models.AgentClientID,
	existing []byte,
	baseURL string,
	apiKey string,
	transport string,
	isWSL bool,
) ([]byte, error) {
	apiKey = SanitizeInjectorAPIKey(apiKey)
	root := make(map[string]any)
	trimmed := bytes.TrimSpace(existing)
	if len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &root); err != nil {
			return nil, fmt.Errorf("existing JSON config in %s is malformed: %w", clientID, err)
		}
	}

	servers, ok := root["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		servers = make(map[string]any)
	}
	servers["colabhive"] = buildColabHiveJSONEntry(clientID, baseURL, apiKey, transport, isWSL)
	root["mcpServers"] = servers

	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func removeStandardJSON(existing []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(existing)
	if len(trimmed) == 0 {
		return existing, nil
	}
	root := make(map[string]any)
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return nil, fmt.Errorf("malformed JSON config: %w", err)
	}
	if servers, ok := root["mcpServers"].(map[string]any); ok && servers != nil {
		delete(servers, "colabhive")
		root["mcpServers"] = servers
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// injectCodexTOML surgically updates or appends `[mcp_servers.colabhive]` inside `~/.codex/config.toml`
// while preserving all surrounding comments, ordering, and unrelated tables, and validates the output with go-toml/v2.
func injectCodexTOML(existing []byte, baseURL string, apiKey string, transport string, isWSL bool) ([]byte, error) {
	apiKey = SanitizeInjectorAPIKey(apiKey)
	cleaned, err := removeCodexTOML(existing)
	if err != nil {
		return nil, err
	}

	var block strings.Builder
	block.WriteString("[mcp_servers.colabhive]\n")
	if transport == "stdio" {
		cmdBin := resolveStdioExecutable(isWSL)
		block.WriteString("command = " + strconvQuoteTOML(cmdBin) + "\n")
		block.WriteString("args = [\"mcp\", \"stdio\"]\n")
		block.WriteString("tool_timeout_sec = 1800\n")
		if apiKey != "" {
			block.WriteString("env = { COLABHIVE_URL = " + strconvQuoteTOML(baseURL) + ", COLABHIVE_API_KEY = " + strconvQuoteTOML(apiKey) + " }\n")
		} else {
			block.WriteString("env = { COLABHIVE_URL = " + strconvQuoteTOML(baseURL) + " }\n")
		}
	} else {
		mcpURL := baseURL + "/mcp"
		block.WriteString("url = " + strconvQuoteTOML(mcpURL) + "\n")
		block.WriteString("tool_timeout_sec = 1800\n")
		if apiKey != "" {
			block.WriteString("http_headers = { Authorization = " + strconvQuoteTOML("Bearer "+apiKey) + " }\n")
		}
	}

	baseText := strings.TrimRight(string(cleaned), " \t\r\n")
	var finalText string
	if baseText == "" {
		finalText = block.String()
	} else {
		finalText = baseText + "\n\n" + block.String()
	}

	// Verify resulting TOML parses cleanly via go-toml/v2.
	var check map[string]any
	if err := toml.Unmarshal([]byte(finalText), &check); err != nil {
		return nil, fmt.Errorf("generated Codex TOML failed validation: %w", err)
	}
	return []byte(finalText), nil
}

func removeCodexTOML(existing []byte) ([]byte, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return []byte{}, nil
	}
	// Verify input is valid TOML before mutating.
	var check map[string]any
	if err := toml.Unmarshal(existing, &check); err != nil {
		return nil, fmt.Errorf("existing Codex config.toml is invalid TOML: %w", err)
	}

	lines := strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	var st tomlScanState

	for _, line := range lines {
		wasInMultilineOrContainer := st.inMultiBasic || st.inMultiLiteral || st.bracketDepth > 0 || st.braceDepth > 0
		if !wasInMultilineOrContainer {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				if segs, _, ok := parseTOMLHeaderSegments(trimmed); ok {
					if len(segs) >= 2 && segs[0] == "mcp_servers" && segs[1] == "colabhive" {
						skipping = true
						st.advanceLine(line)
						continue
					}
					skipping = false
				}
			}
		}
		st.advanceLine(line)
		if !skipping {
			out = append(out, line)
		}
	}

	joined := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if joined == "" {
		return []byte{}, nil
	}
	candidate := []byte(joined + "\n")

	// Fallback if `mcp_servers.colabhive` was defined via inline table or dotted root keys rather than a `[table]` header.
	var postCheck map[string]any
	if err := toml.Unmarshal(candidate, &postCheck); err == nil {
		if servers, ok := postCheck["mcp_servers"].(map[string]any); ok {
			if _, stillHas := servers["colabhive"]; stillHas {
				delete(servers, "colabhive")
				if len(servers) == 0 {
					delete(postCheck, "mcp_servers")
				} else {
					postCheck["mcp_servers"] = servers
				}
				if marshaled, mErr := toml.Marshal(postCheck); mErr == nil {
					return marshaled, nil
				}
			}
		}
	}
	return candidate, nil
}

type tomlScanState struct {
	inMultiBasic   bool
	inMultiLiteral bool
	bracketDepth   int
	braceDepth     int
}

func (st *tomlScanState) advanceLine(line string) {
	i := 0
	n := len(line)
	for i < n {
		if st.inMultiBasic {
			if line[i] == '\\' {
				i += 2
				continue
			}
			if i+2 < n && line[i] == '"' && line[i+1] == '"' && line[i+2] == '"' {
				// Up to 2 extra quotes are allowed immediately before closing `"""` in TOML.
				i += 3
				for i < n && line[i] == '"' {
					i++
				}
				st.inMultiBasic = false
				continue
			}
			i++
			continue
		}
		if st.inMultiLiteral {
			if i+2 < n && line[i] == '\'' && line[i+1] == '\'' && line[i+2] == '\'' {
				i += 3
				for i < n && line[i] == '\'' {
					i++
				}
				st.inMultiLiteral = false
				continue
			}
			i++
			continue
		}

		ch := line[i]
		if ch == '#' {
			// Comment runs to end of line.
			return
		}
		if ch == '"' {
			if i+2 < n && line[i+1] == '"' && line[i+2] == '"' {
				st.inMultiBasic = true
				i += 3
				continue
			}
			// Single-line basic string
			i++
			for i < n {
				if line[i] == '\\' {
					i += 2
					continue
				}
				if line[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}
		if ch == '\'' {
			if i+2 < n && line[i+1] == '\'' && line[i+2] == '\'' {
				st.inMultiLiteral = true
				i += 3
				continue
			}
			// Single-line literal string
			i++
			for i < n {
				if line[i] == '\'' {
					i++
					break
				}
				i++
			}
			continue
		}
		switch ch {
		case '[':
			st.bracketDepth++
		case ']':
			if st.bracketDepth > 0 {
				st.bracketDepth--
			}
		case '{':
			st.braceDepth++
		case '}':
			if st.braceDepth > 0 {
				st.braceDepth--
			}
		}
		i++
	}
}

func parseTOMLHeaderSegments(trimmed string) (segments []string, isArray bool, ok bool) {
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false, false
	}
	i := 1
	if strings.HasPrefix(trimmed, "[[") {
		isArray = true
		i = 2
	}
	n := len(trimmed)
	for i < n {
		for i < n && (trimmed[i] == ' ' || trimmed[i] == '\t') {
			i++
		}
		if i >= n {
			return nil, false, false
		}
		var seg string
		switch trimmed[i] {
		case '"':
			end := i + 1
			for end < n {
				if trimmed[end] == '\\' {
					end += 2
					continue
				}
				if trimmed[end] == '"' {
					break
				}
				end++
			}
			if end >= n || trimmed[end] != '"' {
				return nil, false, false
			}
			var unq string
			if err := json.Unmarshal([]byte(trimmed[i:end+1]), &unq); err == nil {
				seg = unq
			} else {
				seg = trimmed[i+1 : end]
			}
			i = end + 1
		case '\'':
			end := strings.IndexByte(trimmed[i+1:], '\'')
			if end < 0 {
				return nil, false, false
			}
			seg = trimmed[i+1 : i+1+end]
			i = i + 1 + end + 1
		default:
			start := i
			for i < n && trimmed[i] != '.' && trimmed[i] != ']' && trimmed[i] != ' ' && trimmed[i] != '\t' && trimmed[i] != '#' {
				i++
			}
			if i == start {
				return nil, false, false
			}
			seg = trimmed[start:i]
		}
		segments = append(segments, seg)

		for i < n && (trimmed[i] == ' ' || trimmed[i] == '\t') {
			i++
		}
		if i < n && trimmed[i] == '.' {
			i++
			continue
		}
		if isArray {
			if i+1 >= n || trimmed[i] != ']' || trimmed[i+1] != ']' {
				return nil, false, false
			}
			i += 2
		} else {
			if i >= n || trimmed[i] != ']' {
				return nil, false, false
			}
			i++
		}
		for i < n && (trimmed[i] == ' ' || trimmed[i] == '\t') {
			i++
		}
		if i < n && trimmed[i] != '#' {
			return nil, false, false
		}
		return segments, isArray, len(segments) > 0
	}
	return nil, false, false
}

func strconvQuoteTOML(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// injectOpenCodeJSONC updates `mcp.colabhive` inside `opencode.json` / `opencode.jsonc` while preserving leading `//` and `/* */` comments.
func injectOpenCodeJSONC(existing []byte, baseURL string, apiKey string, transport string, isWSL bool) ([]byte, error) {
	apiKey = SanitizeInjectorAPIKey(apiKey)
	headerComments, stripped := splitLeadingJSONCComments(existing)
	root := make(map[string]any)
	if len(bytes.TrimSpace(stripped)) > 0 {
		cleanJSON := stripJSONCCommentsAndTrailingCommas(stripped)
		if err := json.Unmarshal(cleanJSON, &root); err != nil {
			return nil, fmt.Errorf("existing OpenCode config is malformed: %w", err)
		}
	}

	mcpObj, ok := root["mcp"].(map[string]any)
	if !ok || mcpObj == nil {
		mcpObj = make(map[string]any)
	}
	if transport == "stdio" {
		cmdBin := resolveStdioExecutable(isWSL)
		cmd := []string{cmdBin, "mcp", "stdio"}
		envMap := map[string]string{
			"COLABHIVE_URL": baseURL,
		}
		if apiKey != "" {
			envMap["COLABHIVE_API_KEY"] = apiKey
		}
		entry := map[string]any{
			"type":        "local",
			"command":     cmd,
			"environment": envMap,
			"enabled":     true,
		}
		mcpObj["colabhive"] = entry
	} else {
		mcpObj["colabhive"] = buildColabHiveJSONEntry(models.AgentOpenCode, baseURL, apiKey, "http", isWSL)
	}
	root["mcp"] = mcpObj

	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if len(headerComments) > 0 {
		buf.Write(headerComments)
		if !bytes.HasSuffix(headerComments, []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	buf.Write(encoded)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func removeOpenCodeJSONC(existing []byte) ([]byte, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return existing, nil
	}
	headerComments, stripped := splitLeadingJSONCComments(existing)
	root := make(map[string]any)
	if len(bytes.TrimSpace(stripped)) > 0 {
		cleanJSON := stripJSONCCommentsAndTrailingCommas(stripped)
		if err := json.Unmarshal(cleanJSON, &root); err != nil {
			return nil, fmt.Errorf("existing opencode.jsonc is malformed: %w", err)
		}
	}
	if mcpObj, ok := root["mcp"].(map[string]any); ok && mcpObj != nil {
		delete(mcpObj, "colabhive")
		root["mcp"] = mcpObj
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if len(headerComments) > 0 {
		buf.Write(headerComments)
		if !bytes.HasSuffix(headerComments, []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	buf.Write(encoded)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func splitLeadingJSONCComments(src []byte) (leading []byte, rest []byte) {
	text := string(src)
	i := 0
	n := len(text)
	for i < n {
		start := i
		for i < n && (text[i] == ' ' || text[i] == '\t' || text[i] == '\r' || text[i] == '\n') {
			i++
		}
		if i+1 < n && text[i] == '/' && text[i+1] == '/' {
			for i < n && text[i] != '\n' {
				i++
			}
			if i < n && text[i] == '\n' {
				i++
			}
			continue
		}
		if i+1 < n && text[i] == '/' && text[i+1] == '*' {
			i += 2
			for i+1 < n && !(text[i] == '*' && text[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			}
			continue
		}
		return []byte(text[:start]), []byte(text[start:])
	}
	return src, nil
}

func stripJSONCCommentsAndTrailingCommas(src []byte) []byte {
	var noComments bytes.Buffer
	inStr := false
	escaped := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inStr {
			noComments.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			noComments.WriteByte(ch)
			continue
		}
		if ch == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				noComments.WriteByte('\n')
			}
			continue
		}
		if ch == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		noComments.WriteByte(ch)
	}

	// Second pass: remove trailing commas before `}` or `]` outside strings.
	b := noComments.Bytes()
	var out bytes.Buffer
	inStr = false
	escaped = false
	for i := 0; i < len(b); i++ {
		ch := b[i]
		if inStr {
			out.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			out.WriteByte(ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\r' || b[j] == '\n') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
		}
		out.WriteByte(ch)
	}
	return out.Bytes()
}

func isColabHiveInjected(spec clientTargetSpec, raw []byte) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false
	}
	switch spec.format {
	case "toml":
		var doc map[string]any
		if err := toml.Unmarshal(raw, &doc); err != nil {
			return false
		}
		if servers, ok := doc["mcp_servers"].(map[string]any); ok {
			_, has := servers["colabhive"]
			return has
		}
		return false
	case "jsonc":
		var doc map[string]any
		if err := json.Unmarshal(stripJSONCCommentsAndTrailingCommas(raw), &doc); err != nil {
			return false
		}
		if mcpObj, ok := doc["mcp"].(map[string]any); ok {
			_, has := mcpObj["colabhive"]
			return has
		}
		return false
	default:
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return false
		}
		if servers, ok := doc["mcpServers"].(map[string]any); ok {
			_, has := servers["colabhive"]
			return has
		}
		return false
	}
}

func buildConfigSnippet(spec clientTargetSpec, baseURL string, apiKey string, transport string) string {
	apiKey = SanitizeInjectorAPIKey(apiKey)
	switch spec.format {
	case "toml":
		b, _ := injectCodexTOML(nil, baseURL, apiKey, transport, false)
		return strings.TrimSpace(string(b))
	case "jsonc":
		b, _ := injectOpenCodeJSONC(nil, baseURL, apiKey, transport, false)
		return strings.TrimSpace(string(b))
	default:
		b, _ := injectStandardJSON(spec.id, nil, baseURL, apiKey, transport, false)
		return strings.TrimSpace(string(b))
	}
}

func buildCLICommand(clientID models.AgentClientID, baseURL string, apiKey string) string {
	apiKey = SanitizeInjectorAPIKey(apiKey)
	mcpURL := baseURL + "/mcp"
	switch clientID {
	case models.AgentClaudeCode:
		if apiKey != "" {
			return fmt.Sprintf(`claude mcp add --transport http colabhive %s --header "Authorization: Bearer %s"`, mcpURL, apiKey)
		}
		return fmt.Sprintf(`claude mcp add --transport http colabhive %s`, mcpURL)
	case models.AgentCodex:
		return fmt.Sprintf(`colabhive connect codex --server %s`, baseURL)
	default:
		return fmt.Sprintf(`colabhive connect %s --server %s`, clientID, baseURL)
	}
}
