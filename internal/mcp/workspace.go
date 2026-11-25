package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/colabhive/colabhive/internal/models"
)

type hostFSAccessMode int

const (
	hostFSBlocked hostFSAccessMode = iota
	hostFSTrustedWorkspace
	hostFSFullLocal
)

// isDirectLoopbackHTTPRequest returns true ONLY when the TCP peer (`r.RemoteAddr`) is a loopback
// address (`127.0.0.0/8` or `::1`), the `Host` header is loopback, AND zero reverse-proxy
// forwarding headers are present (preventing local reverse proxies / Cloudflare Tunnels from
// forwarding remote internet callers into the local trusted workspace).
func isDirectLoopbackHTTPRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !isAllowedLoopbackHostMCP(r.Host) {
		return false
	}
	for _, proxyHdr := range []string{
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"Forwarded",
		"X-Real-IP",
		"CF-Connecting-IP",
		"True-Client-IP",
	} {
		if strings.TrimSpace(r.Header.Get(proxyHdr)) != "" {
			return false
		}
	}
	rawPeer := strings.TrimSpace(r.RemoteAddr)
	if rawPeer == "" {
		return false
	}
	host := rawPeer
	if parsedHost, _, err := net.SplitHostPort(rawPeer); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	// Strip IPv6 zone identifier if any.
	if zoneIdx := strings.IndexByte(host, '%'); zoneIdx >= 0 {
		host = host[:zoneIdx]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SetTrustedWorkspaceDir enables an explicit opt-in local host directory confined via `os.OpenRoot`
// for direct `127.0.0.1`/`::1` loopback callers in non-cloud mode. Passing an empty string disables it.
func (s *Server) SetTrustedWorkspaceDir(dir string) {
	trimmed := strings.TrimSpace(dir)
	var rawAbs, canonAbs string
	if trimmed != "" {
		if abs, err := filepath.Abs(trimmed); err == nil {
			rawAbs = filepath.Clean(abs)
		} else {
			rawAbs = filepath.Clean(trimmed)
		}
		canonAbs = rawAbs
		if eval, err := filepath.EvalSymlinks(rawAbs); err == nil && strings.TrimSpace(eval) != "" {
			canonAbs = filepath.Clean(eval)
		}
	}
	s.mu.Lock()
	s.trustedWorkspaceRaw = rawAbs
	s.trustedWorkspaceCanon = canonAbs
	s.mu.Unlock()
}

func (s *Server) trustedWorkspaceRoots() (rawAbs string, canonAbs string) {
	s.mu.RLock()
	rawAbs = s.trustedWorkspaceRaw
	canonAbs = s.trustedWorkspaceCanon
	s.mu.RUnlock()
	return rawAbs, canonAbs
}

func (s *Server) hostFSAccessMode(cctx callContext) hostFSAccessMode {
	if s.isCloudMode() {
		return hostFSBlocked
	}
	if cctx.transport == models.TransportMCPStdio && cctx.apiKey == nil {
		return hostFSFullLocal
	}
	_, canonRoot := s.trustedWorkspaceRoots()
	if canonRoot == "" {
		return hostFSBlocked
	}
	if cctx.transport == models.TransportMCPStdio {
		return hostFSTrustedWorkspace
	}
	if (cctx.transport == models.TransportMCPHTTP || cctx.transport == models.TransportMCPSSE) && cctx.directLoopback {
		return hostFSTrustedWorkspace
	}
	return hostFSBlocked
}

func isRelativeSubpathInsideRoot(rel string) bool {
	if rel == "" || rel == ".." {
		return false
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return false
	}
	slash := filepath.ToSlash(rel)
	if slash == ".." || strings.HasPrefix(slash, "../") || strings.Contains(slash, "/../") || strings.HasSuffix(slash, "/..") {
		return false
	}
	return true
}

func (s *Server) resolveTrustedWorkspacePath(rawPath string, allowRootDir bool) (rootDir string, relSlash string, absDisplay string, err error) {
	rawRoot, canonRoot := s.trustedWorkspaceRoots()
	if canonRoot == "" {
		return "", "", "", errors.New("trusted local workspace is not enabled")
	}
	cleanedInput := strings.TrimSpace(rawPath)
	if cleanedInput == "" {
		return "", "", "", errors.New("empty host path")
	}
	if strings.ContainsRune(cleanedInput, '\x00') {
		return "", "", "", errors.New("host path contains invalid NUL byte")
	}
	// Reject Windows alternate data stream syntax (`foo.txt:stream`) beyond the drive volume prefix.
	vol := filepath.VolumeName(cleanedInput)
	if strings.Contains(cleanedInput[len(vol):], ":") {
		return "", "", "", errors.New("host path contains invalid colon separator")
	}

	var rel string
	if filepath.IsAbs(cleanedInput) {
		absTarget := filepath.Clean(cleanedInput)
		candRel, rErr := filepath.Rel(canonRoot, absTarget)
		if rErr != nil || !isRelativeSubpathInsideRoot(candRel) {
			if rawRoot != "" && rawRoot != canonRoot {
				if altRel, aErr := filepath.Rel(rawRoot, absTarget); aErr == nil && isRelativeSubpathInsideRoot(altRel) {
					candRel = altRel
					rErr = nil
				}
			}
		}
		if rErr != nil || !isRelativeSubpathInsideRoot(candRel) {
			return "", "", "", fmt.Errorf("host path %q is outside trusted workspace %q", rawPath, canonRoot)
		}
		rel = candRel
	} else {
		// Cross-platform defense-in-depth: on POSIX, `\` is not a path separator so
		// `..\secret.txt` would otherwise pass `filepath.Clean` as a literal filename.
		// Normalize backslashes before traversal checks so Windows-style escapes are
		// rejected uniformly across Linux, macOS, and Windows.
		if strings.Contains(cleanedInput, `\`) {
			slashCleaned := path.Clean(strings.ReplaceAll(cleanedInput, `\`, "/"))
			if slashCleaned == ".." || strings.HasPrefix(slashCleaned, "../") || strings.HasPrefix(slashCleaned, "/") {
				return "", "", "", fmt.Errorf("host path %q escapes trusted workspace %q", rawPath, canonRoot)
			}
		}
		candRel := filepath.Clean(cleanedInput)
		if !isRelativeSubpathInsideRoot(candRel) {
			return "", "", "", fmt.Errorf("host path %q escapes trusted workspace %q", rawPath, canonRoot)
		}
		rel = candRel
	}

	relSlash = filepath.ToSlash(rel)
	relSlash = strings.TrimPrefix(relSlash, "./")
	if relSlash == "" {
		relSlash = "."
	}
	if !allowRootDir && relSlash == "." {
		return "", "", "", fmt.Errorf("host file path %q must name a file inside trusted workspace %q", rawPath, canonRoot)
	}
	absDisplay = canonRoot
	if relSlash != "." {
		absDisplay = filepath.Join(canonRoot, filepath.FromSlash(relSlash))
	}
	return canonRoot, relSlash, absDisplay, nil
}

func (s *Server) validateHostPathAccess(cctx callContext, rawPath string, allowRootDir bool, blockedErrMsg string) error {
	switch s.hostFSAccessMode(cctx) {
	case hostFSBlocked:
		return errors.New(blockedErrMsg)
	case hostFSFullLocal:
		return nil
	case hostFSTrustedWorkspace:
		_, _, _, err := s.resolveTrustedWorkspacePath(rawPath, allowRootDir)
		return err
	default:
		return errors.New(blockedErrMsg)
	}
}

func (s *Server) readHostFile(cctx callContext, rawPath string, blockedErrMsg string) ([]byte, string, error) {
	switch s.hostFSAccessMode(cctx) {
	case hostFSBlocked:
		return nil, "", errors.New(blockedErrMsg)
	case hostFSFullLocal:
		raw, err := os.ReadFile(rawPath)
		if err != nil {
			return nil, "", err
		}
		return raw, rawPath, nil
	case hostFSTrustedWorkspace:
		rootDir, relSlash, absDisplay, err := s.resolveTrustedWorkspacePath(rawPath, false)
		if err != nil {
			return nil, "", err
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			return nil, "", fmt.Errorf("open trusted workspace %q: %w", rootDir, err)
		}
		defer root.Close()
		raw, err := root.ReadFile(relSlash)
		if err != nil {
			return nil, "", err
		}
		return raw, absDisplay, nil
	default:
		return nil, "", errors.New(blockedErrMsg)
	}
}

func (s *Server) writeHostFile(cctx callContext, rawPath string, data []byte, blockedErrMsg string) (string, error) {
	switch s.hostFSAccessMode(cctx) {
	case hostFSBlocked:
		return "", errors.New(blockedErrMsg)
	case hostFSFullLocal:
		if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(rawPath, data, 0o644); err != nil {
			return "", err
		}
		return rawPath, nil
	case hostFSTrustedWorkspace:
		rootDir, relSlash, absDisplay, err := s.resolveTrustedWorkspacePath(rawPath, false)
		if err != nil {
			return "", err
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			return "", fmt.Errorf("open trusted workspace %q: %w", rootDir, err)
		}
		defer root.Close()
		if parentDir := path.Dir(relSlash); parentDir != "" && parentDir != "." {
			if err := root.MkdirAll(parentDir, 0o755); err != nil {
				return "", err
			}
		}
		if err := root.WriteFile(relSlash, data, 0o644); err != nil {
			return "", err
		}
		return absDisplay, nil
	default:
		return "", errors.New(blockedErrMsg)
	}
}

func (s *Server) prepareHostOutputDir(cctx callContext, rawPath string, blockedErrMsg string) (string, error) {
	switch s.hostFSAccessMode(cctx) {
	case hostFSBlocked:
		return "", errors.New(blockedErrMsg)
	case hostFSFullLocal:
		return rawPath, nil
	case hostFSTrustedWorkspace:
		rootDir, relSlash, absDisplay, err := s.resolveTrustedWorkspacePath(rawPath, true)
		if err != nil {
			return "", err
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			return "", fmt.Errorf("open trusted workspace %q: %w", rootDir, err)
		}
		defer root.Close()
		if relSlash != "." {
			if err := root.MkdirAll(relSlash, 0o755); err != nil {
				return "", err
			}
		}
		info, err := root.Stat(relSlash)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("trusted workspace output path %q is not a directory", rawPath)
		}
		// Double-check symlink resolution after creation so downstream writers cannot escape via a symlinked directory.
		evalOut, err := filepath.EvalSymlinks(absDisplay)
		if err != nil {
			return "", err
		}
		relCheck, rErr := filepath.Rel(rootDir, evalOut)
		if rErr != nil || !isRelativeSubpathInsideRoot(relCheck) {
			return "", fmt.Errorf("output directory %q resolves outside trusted workspace %q", rawPath, rootDir)
		}
		return evalOut, nil
	default:
		return "", errors.New(blockedErrMsg)
	}
}

func parseScriptOrNotebookBytes(raw []byte, fileLabel string) (string, error) {
	base := filepath.Base(fileLabel)
	if !strings.HasSuffix(strings.ToLower(base), ".ipynb") {
		return string(raw), nil
	}
	var nb struct {
		Cells []struct {
			CellType string `json:"cell_type"`
			Source   any    `json:"source"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(raw, &nb); err != nil {
		return "", fmt.Errorf("invalid .ipynb JSON in %s: %w", base, err)
	}
	var combined strings.Builder
	for idx, c := range nb.Cells {
		if c.CellType != "code" {
			continue
		}
		var src string
		switch v := c.Source.(type) {
		case string:
			src = v
		case []any:
			var b strings.Builder
			for _, line := range v {
				if s, ok := line.(string); ok {
					b.WriteString(s)
				}
			}
			src = b.String()
		}
		if strings.TrimSpace(src) == "" {
			continue
		}
		combined.WriteString(fmt.Sprintf("# --- Notebook Cell %d ---\n%s\n\n", idx, src))
	}
	return combined.String(), nil
}
