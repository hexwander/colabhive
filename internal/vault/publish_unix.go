//go:build !windows

package vault

import "os"

// publishSyncedTempFileNoOverwrite publishes an already-written, Sync()ed, and closed
// temporary file to `dstPath` strictly via `os.Link` (`link(2)`), which atomically fails
// with `os.ErrExist` (`EEXIST`) if `dstPath` already exists — never overwriting a
// concurrently published key and never creating a 0-byte file.
func publishSyncedTempFileNoOverwrite(tmpPath string, dstPath string) error {
	err := os.Link(tmpPath, dstPath)
	_ = os.Remove(tmpPath)
	return err
}
