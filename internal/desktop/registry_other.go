//go:build !windows && !darwin

package desktop

// IsAutoStartEnabled is a no-op stub on platforms other than Windows and macOS.
func IsAutoStartEnabled() bool {
	return false
}

// SetAutoStartEnabled is a no-op stub on platforms other than Windows and macOS.
func SetAutoStartEnabled(enabled bool, exePath string) error {
	_ = enabled
	_ = exePath
	return nil
}
