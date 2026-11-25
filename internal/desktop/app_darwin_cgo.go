//go:build darwin && cgo

package desktop

/*
#cgo CFLAGS: -fobjc-arc -mmacosx-version-min=11.0 -Wno-deprecated-declarations
#cgo LDFLAGS: -mmacosx-version-min=11.0 -framework Cocoa -framework WebKit

#include <stdlib.h>
#include <stdint.h>

#ifndef COLABHIVE_CH_CONST_CHAR
#define COLABHIVE_CH_CONST_CHAR
typedef const char ch_const_char;
#endif

void ch_cocoa_run(
	const char *baseURL,
	const char *mainBootstrapJS,
	const char *trayBootstrapJS,
	const void *iconPNG,
	int iconPNGLen,
	int startMinimized,
	int autostartOn,
	const char *initialTooltip,
	const char *initialPoolLabel
);
void ch_cocoa_show_main(const char *route);
void ch_cocoa_hide_main(void);
void ch_cocoa_minimize_main(void);
void ch_cocoa_zoom_main(void);
void ch_cocoa_show_tray(void);
void ch_cocoa_hide_tray(void);
void ch_cocoa_toggle_tray(void);
void ch_cocoa_eval_main_js(const char *js);
void ch_cocoa_eval_tray_js(const char *js);
void ch_cocoa_update_telemetry(const char *tooltip, const char *poolLabel, int autostartOn);
void ch_cocoa_copy_clipboard(const char *text);
void ch_cocoa_open_external(const char *urlStr);
void ch_cocoa_quit(void);
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/server"
)

const (
	darwinCmdOpenTray      = 1001
	darwinCmdOpenMain      = 1002
	darwinCmdOpenPool      = 1003
	darwinCmdSyncNow       = 1004
	darwinCmdCopyMCPURL    = 1005
	darwinCmdToggleStartup = 1006
	darwinCmdQuit          = 1007
)

var (
	darwinActiveMu  sync.RWMutex
	darwinActiveApp *darwinDesktopApp
)

func currentDarwinApp() *darwinDesktopApp {
	darwinActiveMu.RLock()
	a := darwinActiveApp
	darwinActiveMu.RUnlock()
	return a
}

type darwinDesktopApp struct {
	cfg     Config
	exePath string
	cp      atomic.Pointer[ControlPlaneHandle]

	quitting   atomic.Bool
	quitDoneCh chan struct{}

	stateMu           sync.RWMutex
	autoStartEnabled  bool
	mainWindowVisible bool
	trayVisible       bool
	lastAction        string
	cachedSummary     *models.QuotaSummary
	summaryConnected  bool
	currentTooltip    string
	currentPoolLabel  string

	debouncer *ToggleDebouncer
}

// Run launches the Native macOS Cocoa + `WKWebView` Frameless Traffic-Light Desktop App,
// `NSStatusBar` Menu Bar Icon, Magpie `396x580` `NSPopover` (`/tray`), and Right-Click `NSMenu`.
func Run(ctx context.Context, cfg Config) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	exePath, _ := os.Executable()
	autoOn := IsAutoStartEnabled()
	initTip := FormatTrayTooltip(nil, false)
	initPool := FormatPoolMenuLabel(nil, false)

	app := &darwinDesktopApp{
		cfg:               cfg,
		exePath:           exePath,
		quitDoneCh:        make(chan struct{}),
		autoStartEnabled:  autoOn,
		mainWindowVisible: !cfg.StartMinimized,
		currentTooltip:    initTip,
		currentPoolLabel:  initPool,
		debouncer:         NewToggleDebouncer(400 * time.Millisecond),
	}

	darwinActiveMu.Lock()
	darwinActiveApp = app
	darwinActiveMu.Unlock()
	defer func() {
		darwinActiveMu.Lock()
		if darwinActiveApp == app {
			darwinActiveApp = nil
		}
		darwinActiveMu.Unlock()
	}()

	cp, err := EnsureControlPlane(ctx, cfg, app.handleDesktopBridge)
	if err != nil {
		return err
	}
	app.cp.Store(cp)
	defer cp.Close()

	iconPNG, _ := EncodeColabHivePNG(64)
	mainJS := BuildNativeWebViewBootstrapJS("macos", autoOn, false)
	trayJS := BuildNativeWebViewBootstrapJS("macos", autoOn, true)

	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()

	go func() {
		select {
		case <-ctx.Done():
			app.quitApplication()
		case <-app.quitDoneCh:
		}
	}()

	go app.runTelemetryPoller(pollCtx)

	cBaseURL := C.CString(cp.BaseURL)
	cMainJS := C.CString(mainJS)
	cTrayJS := C.CString(trayJS)
	cTip := C.CString(initTip)
	cPool := C.CString(initPool)
	defer func() {
		C.free(unsafe.Pointer(cBaseURL))
		C.free(unsafe.Pointer(cMainJS))
		C.free(unsafe.Pointer(cTrayJS))
		C.free(unsafe.Pointer(cTip))
		C.free(unsafe.Pointer(cPool))
	}()

	var iconPtr unsafe.Pointer
	iconLen := C.int(0)
	if len(iconPNG) > 0 {
		iconPtr = C.CBytes(iconPNG)
		iconLen = C.int(len(iconPNG))
		defer C.free(iconPtr)
	}

	startMin := C.int(0)
	if cfg.StartMinimized {
		startMin = 1
	}
	autoInt := C.int(0)
	if autoOn {
		autoInt = 1
	}

	C.ch_cocoa_run(cBaseURL, cMainJS, cTrayJS, iconPtr, iconLen, startMin, autoInt, cTip, cPool)
	close(app.quitDoneCh)
	return nil
}

func (a *darwinDesktopApp) snapshotState() server.DesktopState {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	baseURL := ""
	if cp := a.cp.Load(); cp != nil {
		baseURL = cp.BaseURL
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

func (a *darwinDesktopApp) handleDesktopBridge(_ context.Context, req server.DesktopActionRequest) (server.DesktopState, error) {
	act := strings.ToLower(strings.TrimSpace(req.Action))
	if act == "" || act == "state" {
		return a.snapshotState(), nil
	}
	route := req.Route
	if strings.TrimSpace(route) == "" {
		route = req.Path
	}
	if act == "quit" {
		a.stateMu.Lock()
		a.lastAction = "quit"
		a.stateMu.Unlock()
		st := a.snapshotState()
		a.quitApplication()
		return st, nil
	}
	err := a.executeAction(act, route, req.Enabled)
	return a.snapshotState(), err
}

func (a *darwinDesktopApp) executeAction(action string, route string, explicitEnabled *bool) error {
	act := strings.ToLower(strings.TrimSpace(action))

	a.stateMu.Lock()
	a.lastAction = act
	a.stateMu.Unlock()

	switch act {
	case "open-main":
		C.ch_cocoa_hide_tray()
		a.showMain("/")
	case "open-route":
		C.ch_cocoa_hide_tray()
		a.showMain(NormalizeRoute(route))
	case "hide-tray":
		a.stateMu.Lock()
		a.trayVisible = false
		a.stateMu.Unlock()
		C.ch_cocoa_hide_tray()
	case "show-tray":
		a.stateMu.Lock()
		a.trayVisible = true
		a.stateMu.Unlock()
		C.ch_cocoa_show_tray()
	case "toggle-tray":
		if a.debouncer.ShouldExecute("toggle-tray", time.Now()) {
			C.ch_cocoa_toggle_tray()
		}
	case "sync-now":
		if a.debouncer.ShouldExecute("sync-now", time.Now()) {
			C.ch_cocoa_hide_tray()
			a.triggerManualCloudSync()
		}
	case "copy-mcp":
		if a.debouncer.ShouldExecute("copy-mcp", time.Now()) {
			C.ch_cocoa_hide_tray()
			if cp := a.cp.Load(); cp != nil && cp.BaseURL != "" {
				mcpURL := strings.TrimRight(cp.BaseURL, "/") + "/mcp"
				cText := C.CString(mcpURL)
				C.ch_cocoa_copy_clipboard(cText)
				C.free(unsafe.Pointer(cText))
			}
		}
	case "toggle-autostart":
		if a.debouncer.ShouldExecute("toggle-autostart", time.Now()) {
			next := !IsAutoStartEnabled()
			if err := SetAutoStartEnabled(next, a.exePath); err != nil {
				a.debouncer.Rollback("toggle-autostart")
				return err
			}
			a.applyAutoStartState(next)
		}
	case "set-autostart":
		target := !IsAutoStartEnabled()
		if explicitEnabled != nil {
			target = *explicitEnabled
		}
		if err := SetAutoStartEnabled(target, a.exePath); err != nil {
			return err
		}
		a.applyAutoStartState(target)
	case "window-minimize":
		C.ch_cocoa_minimize_main()
	case "window-maximize", "window-toggle-maximize":
		if a.debouncer.ShouldExecuteWithin("window-toggle-maximize", time.Now(), 220*time.Millisecond) {
			C.ch_cocoa_zoom_main()
		}
	case "window-close":
		a.stateMu.Lock()
		a.mainWindowVisible = false
		a.stateMu.Unlock()
		C.ch_cocoa_hide_main()
	case "window-drag", "start-drag", "window-resize":
		// Native drag is performed synchronously in Objective-C's script message handler.
	case "quit":
		a.quitApplication()
	}
	return nil
}

func (a *darwinDesktopApp) showMain(route string) {
	cleanRoute := NormalizeRoute(route)
	a.stateMu.Lock()
	a.mainWindowVisible = true
	a.trayVisible = false
	a.stateMu.Unlock()

	cRoute := C.CString(cleanRoute)
	defer C.free(unsafe.Pointer(cRoute))
	C.ch_cocoa_show_main(cRoute)
}

func (a *darwinDesktopApp) applyAutoStartState(enabled bool) {
	a.stateMu.Lock()
	a.autoStartEnabled = enabled
	tip := a.currentTooltip
	pool := a.currentPoolLabel
	a.stateMu.Unlock()

	a.pushTelemetryToCocoa(tip, pool, enabled)

	lit := "false"
	if enabled {
		lit = "true"
	}
	js := fmt.Sprintf(`(function(v){
  window.__COLABHIVE_DESKTOP__ = window.__COLABHIVE_DESKTOP__ || { shell: true, platform: 'macos' };
  window.__COLABHIVE_DESKTOP__.autostart = v;
})(%s);`, lit)
	a.evalJS(false, js)
	a.evalJS(true, js)
}

func (a *darwinDesktopApp) pushTelemetryToCocoa(tip, poolLabel string, autostart bool) {
	cTip := C.CString(tip)
	cPool := C.CString(poolLabel)
	defer func() {
		C.free(unsafe.Pointer(cTip))
		C.free(unsafe.Pointer(cPool))
	}()
	autoInt := C.int(0)
	if autostart {
		autoInt = 1
	}
	C.ch_cocoa_update_telemetry(cTip, cPool, autoInt)
}

func (a *darwinDesktopApp) evalJS(isTray bool, script string) {
	if strings.TrimSpace(script) == "" {
		return
	}
	cJS := C.CString(script)
	defer C.free(unsafe.Pointer(cJS))
	if isTray {
		C.ch_cocoa_eval_tray_js(cJS)
	} else {
		C.ch_cocoa_eval_main_js(cJS)
	}
}

func (a *darwinDesktopApp) ackWebViewAction(isTray bool, reqID string, state server.DesktopState, execErr error) {
	if strings.TrimSpace(reqID) == "" {
		return
	}
	payload := map[string]any{
		"ok":    execErr == nil,
		"state": state,
	}
	if execErr != nil {
		payload["error"] = execErr.Error()
	}
	idJSON, err1 := json.Marshal(reqID)
	payloadJSON, err2 := json.Marshal(payload)
	if err1 != nil || err2 != nil {
		return
	}
	js := fmt.Sprintf(
		`if (typeof window.__COLABHIVE_DESKTOP_RESOLVE__ === 'function') { window.__COLABHIVE_DESKTOP_RESOLVE__(%s, %s); }`,
		string(idJSON),
		string(payloadJSON),
	)
	a.evalJS(isTray, js)
}

func (a *darwinDesktopApp) quitApplication() {
	if !a.quitting.CompareAndSwap(false, true) {
		return
	}
	C.ch_cocoa_quit()
}

func (a *darwinDesktopApp) runTelemetryPoller(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(300 * time.Millisecond):
	}
	a.pollSummaryOnce(ctx)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pollSummaryOnce(ctx)
		}
	}
}

func (a *darwinDesktopApp) pollSummaryOnce(ctx context.Context) {
	cp := a.cp.Load()
	if cp == nil || a.quitting.Load() {
		return
	}
	sum, err := cp.FetchQuotaSummary(ctx, false)
	if err != nil || sum == nil {
		return
	}
	a.applySummaryUpdate(sum)
}

func (a *darwinDesktopApp) applySummaryUpdate(sum *models.QuotaSummary) {
	if sum == nil {
		return
	}
	nextTip := FormatTrayTooltip(sum, true)
	nextPool := FormatPoolMenuLabel(sum, true)

	a.stateMu.Lock()
	a.cachedSummary = sum
	a.summaryConnected = true
	a.currentTooltip = nextTip
	a.currentPoolLabel = nextPool
	autoOn := a.autoStartEnabled
	a.stateMu.Unlock()

	a.pushTelemetryToCocoa(nextTip, nextPool, autoOn)
}

func (a *darwinDesktopApp) triggerManualCloudSync() {
	cp := a.cp.Load()
	if cp == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if sum, err := cp.SyncSessionsAndAccounts(ctx); err == nil && sum != nil {
			a.applySummaryUpdate(sum)
		}
	}()
}

//export colabhiveDarwinOnWebMessage
func colabhiveDarwinOnWebMessage(isTray C.int, rawJSON *C.ch_const_char) {
	app := currentDarwinApp()
	if app == nil || rawJSON == nil {
		return
	}
	traySurface := isTray != 0
	raw := C.GoString((*C.char)(rawJSON))

	req, extURL, reqID, ok := ParseWebViewMessageWithAck(raw)
	if !ok {
		if reqID != "" {
			app.ackWebViewAction(traySurface, reqID, app.snapshotState(), errors.New("unsupported desktop action"))
		}
		return
	}
	if req.Action == "state" {
		if reqID != "" {
			app.ackWebViewAction(traySurface, reqID, app.snapshotState(), nil)
		}
		return
	}
	if req.Action == "open-external" {
		cURL := C.CString(extURL)
		C.ch_cocoa_open_external(cURL)
		C.free(unsafe.Pointer(cURL))
		if reqID != "" {
			app.ackWebViewAction(traySurface, reqID, app.snapshotState(), nil)
		}
		return
	}
	if req.Action == "quit" {
		app.stateMu.Lock()
		app.lastAction = "quit"
		app.stateMu.Unlock()
		if reqID != "" {
			app.ackWebViewAction(traySurface, reqID, app.snapshotState(), nil)
		}
		app.quitApplication()
		return
	}
	execErr := app.executeAction(req.Action, req.Route, req.Enabled)
	if reqID != "" {
		app.ackWebViewAction(traySurface, reqID, app.snapshotState(), execErr)
	}
}

//export colabhiveDarwinOnMenuCommand
func colabhiveDarwinOnMenuCommand(cmdID C.int) {
	app := currentDarwinApp()
	if app == nil {
		return
	}
	switch int(cmdID) {
	case darwinCmdOpenTray:
		_ = app.executeAction("show-tray", "/tray", nil)
	case darwinCmdOpenMain:
		_ = app.executeAction("open-main", "/", nil)
	case darwinCmdOpenPool:
		_ = app.executeAction("open-route", "/pool", nil)
	case darwinCmdSyncNow:
		app.triggerManualCloudSync()
	case darwinCmdCopyMCPURL:
		if cp := app.cp.Load(); cp != nil && cp.BaseURL != "" {
			mcpURL := strings.TrimRight(cp.BaseURL, "/") + "/mcp"
			cText := C.CString(mcpURL)
			C.ch_cocoa_copy_clipboard(cText)
			C.free(unsafe.Pointer(cText))
		}
	case darwinCmdToggleStartup:
		next := !IsAutoStartEnabled()
		if err := SetAutoStartEnabled(next, app.exePath); err == nil {
			app.applyAutoStartState(next)
		}
	case darwinCmdQuit:
		app.quitApplication()
	}
}

//export colabhiveDarwinOnStateChanged
func colabhiveDarwinOnStateChanged(mainVisible C.int, trayVisible C.int) {
	app := currentDarwinApp()
	if app == nil {
		return
	}
	app.stateMu.Lock()
	app.mainWindowVisible = mainVisible != 0
	app.trayVisible = trayVisible != 0
	app.stateMu.Unlock()
}
