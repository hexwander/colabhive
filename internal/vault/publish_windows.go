//go:build windows

package vault

import (
	"os"
	"syscall"
)

// publishSyncedTempFileNoOverwrite publishes an already-written, Sync()ed, and closed
// temporary file to `dstPath` without overwriting any existing file and without ever
// exposing a 0-byte file at `dstPath`.
func publishSyncedTempFileNoOverwrite(tmpPath string, dstPath string) error {
	if err := os.Link(tmpPath, dstPath); err == nil {
		_ = os.Remove(tmpPath)
		return nil
	}
	fromPtr, err := syscall.UTF16PtrFromString(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	toPtr, err := syscall.UTF16PtrFromString(dstPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	// Win32 MoveFileW (without MOVEFILE_REPLACE_EXISTING) atomically renames the synced temp file
	// and fails with ERROR_ALREADY_EXISTS if `dstPath` was created concurrently.
	mvErr := syscall.MoveFile(fromPtr, toPtr)
	if mvErr != nil {
		_ = os.Remove(tmpPath)
		return mvErr
	}
	return nil
}
