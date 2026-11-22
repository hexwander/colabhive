//go:build windows

package desktop

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// IsAutoStartEnabled reports whether `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\ColabHive` is set.
func IsAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, AutoStartRegistryKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	val, _, err := k.GetStringValue(AutoStartRegistryValueName)
	return err == nil && strings.TrimSpace(val) != ""
}

// SetAutoStartEnabled writes or deletes `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\ColabHive`.
func SetAutoStartEnabled(enabled bool, exePath string) error {
	if enabled {
		if strings.TrimSpace(exePath) == "" {
			resolved, err := os.Executable()
			if err != nil {
				return err
			}
			exePath = resolved
		}
		cmd := FormatAutoStartCommand(exePath)
		if cmd == "" {
			return errors.New("empty executable path for autostart")
		}
		k, _, err := registry.CreateKey(registry.CURRENT_USER, AutoStartRegistryKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		return k.SetStringValue(AutoStartRegistryValueName, cmd)
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, AutoStartRegistryKeyPath, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()

	if dErr := k.DeleteValue(AutoStartRegistryValueName); dErr != nil && !errors.Is(dErr, registry.ErrNotExist) {
		return dErr
	}
	return nil
}
