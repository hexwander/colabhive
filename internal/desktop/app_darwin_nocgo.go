//go:build darwin && !cgo

package desktop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/server"
)

type darwinNoCgoApp struct {
	cfg     Config
	exePath string
	cp      *ControlPlaneHandle

	mu                sync.RWMutex
	autoStartEnabled  bool
	mainWindowVisible bool
	trayVisible       bool
	lastAction        string

	quitOnce sync.Once
	quitCh   chan struct{}
	debouncer *ToggleDebouncer
}

// Run boots the ColabHive control plane on macOS when cross-compiled with `CGO_ENABLED=0`,
// writes `~/.colabhive/daemon.json`, supports `~/Library/LaunchAgents/com.colabhive.desktop.plist`
// login autostart, and opens the Observatory UI in app-mode browser unless `--minimized` is set.
func Run(ctx context.Context, cfg Config) error {
	exePath, _ := os.Executable()
	app := &darwinNoCgoApp{
		cfg:              cfg,
		exePath:          exePath,
		autoStartEnabled: IsAutoStartEnabled(),
		quitCh:           make(chan struct{}),
		debouncer:        NewToggleDebouncer(400 * time.Millisecond),
	}

	cp, err := EnsureControlPlane(ctx, cfg, app.handleDesktopBridge)
	if err != nil {
		return err
	}
	app.cp = cp
	defer cp.Close()

	if !cfg.StartMinimized {
		app.openAppBrowser("/")
	}

	select {
	case <-ctx.Done():
	case <-app.quitCh:
	}
	return nil
}

func (a *darwinNoCgoApp) snapshotState() server.DesktopState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	baseURL := ""
	if a.cp != nil {
		baseURL = a.cp.BaseURL
	}
	return server.DesktopState{
		DesktopShell:      true,
		AutoStartEnabled:  a.autoStartEnabled,
		MainWindowVisible: a.mainWindowVisible,
		TrayVisible:       a.trayVisible,
		ServerURL:         baseURL,
		LastAction:        a.lastAction,
	}
}

func (a *darwinNoCgoApp) handleDesktopBridge(_ context.Context, req server.DesktopActionRequest) (server.DesktopState, error) {
	act := strings.ToLower(strings.TrimSpace(req.Action))
	if act == "" || act == "state" {
		return a.snapshotState(), nil
	}

	route := req.Route
	if strings.TrimSpace(route) == "" {
		route = req.Path
	}

	a.mu.Lock()
	a.lastAction = act
	a.mu.Unlock()

	switch act {
	case "open-main":
		a.openAppBrowser("/")
	case "open-route":
		a.openAppBrowser(NormalizeRoute(route))
	case "show-tray", "toggle-tray":
		a.openAppBrowser("/tray")
	case "hide-tray", "window-close":
		a.mu.Lock()
		a.trayVisible = false
		if act == "window-close" {
			a.mainWindowVisible = false
		}
		a.mu.Unlock()
	case "toggle-autostart":
		if a.debouncer.ShouldExecute("toggle-autostart", time.Now()) {
			next := !IsAutoStartEnabled()
			if err := SetAutoStartEnabled(next, a.exePath); err != nil {
				a.debouncer.Rollback("toggle-autostart")
				return a.snapshotState(), err
			}
			a.mu.Lock()
			a.autoStartEnabled = next
			a.mu.Unlock()
		}
	case "set-autostart":
		target := !IsAutoStartEnabled()
		if req.Enabled != nil {
			target = *req.Enabled
		}
		if err := SetAutoStartEnabled(target, a.exePath); err != nil {
			return a.snapshotState(), err
		}
		a.mu.Lock()
		a.autoStartEnabled = target
		a.mu.Unlock()
	case "quit":
		a.quitOnce.Do(func() { close(a.quitCh) })
	}

	return a.snapshotState(), nil
}

func (a *darwinNoCgoApp) openAppBrowser(route string) {
	if a.cp == nil || a.cp.BaseURL == "" {
		return
	}
	targetURL := strings.TrimRight(a.cp.BaseURL, "/") + NormalizeRoute(route)

	a.mu.Lock()
	a.mainWindowVisible = true
	a.mu.Unlock()

	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates,
			filepath.Join(home, "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"),
			filepath.Join(home, "Applications", "Brave Browser.app", "Contents", "MacOS", "Brave Browser"),
			filepath.Join(home, "Applications", "Microsoft Edge.app", "Contents", "MacOS", "Microsoft Edge"),
		)
	}
	candidates = append(candidates,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	)

	for _, bin := range candidates {
		if isRegularFile(bin) {
			if exec.Command(bin, "--app="+targetURL).Start() == nil {
				return
			}
		}
	}
	_ = exec.Command("open", targetURL).Start()
}
