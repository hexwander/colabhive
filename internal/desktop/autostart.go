package desktop

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// AutoStartRegistryKeyPath is the standard per-user Windows Startup registry key.
	AutoStartRegistryKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

	// AutoStartRegistryValueName is the registry value written for ColabHive launch-at-login.
	AutoStartRegistryValueName = "ColabHive"

	// MacOSLaunchAgentLabel is the per-user LaunchAgent bundle identifier on macOS.
	MacOSLaunchAgentLabel = "com.colabhive.desktop"

	// MacOSLaunchAgentFileName is the plist filename under `~/Library/LaunchAgents/`.
	MacOSLaunchAgentFileName = "com.colabhive.desktop.plist"
)

// FormatAutoStartCommand builds the quoted Windows Run command (`"\"<exePath>\" --minimized"`)
// so Windows Startup launches ColabHive silently into the System Tray without popping the main window.
func FormatAutoStartCommand(exePath string) string {
	cleaned := strings.Trim(strings.TrimSpace(exePath), `"`)
	if cleaned == "" {
		return ""
	}
	cleaned = filepath.Clean(cleaned)
	return fmt.Sprintf(`"%s" --minimized`, cleaned)
}

// FormatMacOSLaunchAgentPlist renders a valid Apple `launchd` user LaunchAgent XML property list
// that launches `colabhive-desktop --minimized` into the macOS Menu Bar at login.
func FormatMacOSLaunchAgentPlist(exePath string, logDir string) string {
	cleaned := strings.Trim(strings.TrimSpace(exePath), `"`)
	if cleaned == "" {
		return ""
	}
	escExe := xmlEscapeText(cleaned)

	var logEntries string
	if trimmedLog := strings.TrimSpace(logDir); trimmedLog != "" {
		outLog := xmlEscapeText(strings.TrimRight(trimmedLog, "/") + "/desktop.out.log")
		errLog := xmlEscapeText(strings.TrimRight(trimmedLog, "/") + "/desktop.err.log")
		logEntries = fmt.Sprintf(`  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
`, outLog, errLog)
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>--minimized</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <false/>
  <key>ProcessType</key>
  <string>Interactive</string>
%s</dict>
</plist>
`, MacOSLaunchAgentLabel, escExe, logEntries)
}

func xmlEscapeText(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}
