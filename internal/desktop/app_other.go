//go:build !windows && !darwin

package desktop

import (
	"context"
	"fmt"
	"runtime"
)

// Run is a clean stub on non-Windows/non-macOS platforms so `go build`, `go vet`, and `go test` succeed across all OSes.
// On Linux, use `colabhive serve` to run the ColabHive control plane and web UI.
func Run(ctx context.Context, cfg Config) error {
	_ = ctx
	_ = cfg
	return fmt.Errorf("colabhive-desktop native tray shell requires Windows or macOS (current GOOS=%s); run 'colabhive serve' instead", runtime.GOOS)
}
