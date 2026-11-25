package appstore

import (
	"strings"
	"testing"

	"github.com/colabhive/colabhive/internal/commtunnel"
)

func TestIsSameHostOrigin(t *testing.T) {
	cases := []struct {
		origin string
		inHost string
		want   bool
	}{
		{"http://127.35.74.101:8188", "127.35.74.101:8188", true},
		{"http://127.0.0.1:7860", "127.0.0.1:7860", true},
		{"https://studio.localhost:8890", "studio.localhost:8890", true},
		{"https://evil.example.com", "127.0.0.1:7860", false},
		{"http://127.0.0.1:7860/path", "127.0.0.1:7860", false},
		{"null", "127.0.0.1:7860", false},
		{"", "127.0.0.1:7860", false},
	}
	for _, tc := range cases {
		if got := isSameHostOrigin(tc.origin, tc.inHost); got != tc.want {
			t.Errorf("isSameHostOrigin(%q, %q) = %v, want %v", tc.origin, tc.inHost, got, tc.want)
		}
	}
}

func TestCommTunnelServerPy_SafetyInvariants(t *testing.T) {
	if !strings.Contains(commtunnel.ServerBootstrapPython, "s.settimeout(None)") {
		t.Fatalf("ServerBootstrapPython must call s.settimeout(None) after create_connection")
	}
	if !strings.Contains(commtunnel.ServerBootstrapPython, "_ch_close_all_tunnels()") {
		t.Fatalf("ServerBootstrapPython must close prior sockets on new comm registration")
	}
	if !strings.Contains(commtunnel.ServerBootstrapPython, "s.shutdown(socket.SHUT_RDWR)") {
		t.Fatalf("ServerBootstrapPython must call shutdown(SHUT_RDWR) to unblock Linux recv() promptly")
	}
	if !strings.Contains(commtunnel.ServerBootstrapPython, "@comm.on_close") {
		t.Fatalf("ServerBootstrapPython must register @comm.on_close handler")
	}
	if strings.Contains(commtunnel.ServerBootstrapPython, "sys.exit(") {
		t.Fatalf("ServerBootstrapPython must not contain sys.exit(")
	}
}
