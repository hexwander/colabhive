//go:build darwin

package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func resolveMacOSLaunchAgentPath() (plistPath string, logDir string, err error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", "", errors.New("cannot resolve user home directory for LaunchAgents")
	}
	plistPath = filepath.Join(home, "Library", "LaunchAgents", MacOSLaunchAgentFileName)
	logDir = filepath.Join(home, ".colabhive", "logs")
	return plistPath, logDir, nil
}

// IsAutoStartEnabled reports whether `~/Library/LaunchAgents/com.colabhive.desktop.plist` is installed.
func IsAutoStartEnabled() bool {
	plistPath, _, err := resolveMacOSLaunchAgentPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(plistPath)
	if err != nil {
		return false
	}
	content := string(raw)
	return strings.Contains(content, MacOSLaunchAgentLabel) && strings.Contains(content, "<true/>")
}

// SetAutoStartEnabled writes or deletes `~/Library/LaunchAgents/com.colabhive.desktop.plist`.
func SetAutoStartEnabled(enabled bool, exePath string) error {
	plistPath, logDir, err := resolveMacOSLaunchAgentPath()
	if err != nil {
		return err
	}
	if !enabled {
		if rmErr := os.Remove(plistPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return rmErr
		}
		return nil
	}

	if strings.TrimSpace(exePath) == "" {
		resolved, execErr := os.Executable()
		if execErr != nil {
			return execErr
		}
		exePath = resolved
	}
	if realExe, symErr := filepath.EvalSymlinks(exePath); symErr == nil && strings.TrimSpace(realExe) != "" {
		exePath = realExe
	}

	if logDir != "" {
		_ = os.MkdirAll(logDir, 0o700)
	}
	plistXML := FormatMacOSLaunchAgentPlist(exePath, logDir)
	if plistXML == "" {
		return errors.New("empty executable path for macOS LaunchAgent")
	}
	if mkErr := os.MkdirAll(filepath.Dir(plistPath), 0o755); mkErr != nil {
		return mkErr
	}
	return os.WriteFile(plistPath, []byte(plistXML), 0o644)
}
