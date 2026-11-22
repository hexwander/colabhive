package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/colabhive/colabhive/internal/server"
)

func TestColabBinaryVersionAndHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	code := server.RunDaemonCLI(context.Background(), "colab", []string{"version"}, nil, &out, &errOut, server.VersionInfo{Version: "1.0.0"})
	if code != 0 || !strings.Contains(out.String(), "Version: 1.0.0") {
		t.Fatalf("unexpected colab version output: code=%d out=%q", code, out.String())
	}
}
