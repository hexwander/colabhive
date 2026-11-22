// Command colab is the 100% drop-in `google-colab-cli` (v0.8.0) compatible binary entrypoint
// backed by ColabHive's multi-account compute pool, smart router, and anti-zombie watchdogs.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/colabhive/colabhive/internal/server"
)

var (
	version   = "1.0.0"
	commit    = "dev"
	buildDate = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code := server.RunDaemonCLI(
		ctx,
		"colab",
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
		server.VersionInfo{
			Version:   version,
			Commit:    commit,
			BuildDate: buildDate,
		},
	)
	os.Exit(code)
}
