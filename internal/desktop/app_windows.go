//go:build windows

package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/server"
)

const trayIconUID = 1

var (
	activeAppMu sync.RWMutex
	activeApp   *desktopApp

	trayHostWndProcPtr   uintptr
	mainWndProcPtr       uintptr
	trayFlyoutWndProcPtr uintptr
	wndProcsOnce         sync.Once
)

func ensureWndProcCallbacks() {
	wndProcsOnce.Do(func() {
		trayHostWndProcPtr = windows.NewCallback(trayHostWndProc)
		mainWndProcPtr = windows.NewCallback(mainWndProc)
		trayFlyoutWndProcPtr = windows.NewCallback(trayFlyoutWndProc)
	})
}

func currentApp() *desktopApp {
	activeAppMu.RLock()
	a := activeApp
	activeAppMu.RUnlock()
	return a
}

type desktopApp struct {
	cfg               Config
	exePath           string
	cp                atomic.Pointer[ControlPlaneHandle]
	browserFallbackFn func(targetURL string)

	hInstance       uintptr
	hIconBig        uintptr
	hIconSmall      uintptr
	hBrushWarmStone uintptr

	hwndTrayHost   uintptr
	hwndMain       uintptr
	hwndTrayFlyout uintptr

	wmTaskbarCreated uint32

	wvMgr       *webviewManager
	mainSurface *webviewSurface
	traySurface *webviewSurface

	quitting         atomic.Bool
	minimizeHintSent atomic.Bool
	quitDoneCh       chan struct{}

	dispatchMu sync.Mutex
	dispatchQ  []func()

	stateMu           sync.RWMutex
	autoStartEnabled  bool
	mainWindowVisible bool
	trayVisible       bool
	lastAction        string
	cachedSummary     *models.QuotaSummary
	summaryConnected  bool
	currentTooltip    string

	// Tray click & auto-dismiss coordination
	trayWasOpenOnLDown       bool
	lastTrayAutoHideAt       time.Time
	ignoreNextLButtonUpUntil time.Time

	debouncer *ToggleDebouncer
}

func (a *desktopApp) getCP() *ControlPlaneHandle {
	if a == nil {
		return nil
	}
	return a.cp.Load()
}

func (a *desktopApp) setCP(cp *ControlPlaneHandle) {
	if a != nil {
		a.cp.Store(cp)
	}
}

func (a *desktopApp) invokeBrowserFallback(routeOrPath string) {
	cp := a.getCP()
	if cp == nil || cp.BaseURL == "" {
		return
	}
	fn := a.browserFallbackFn
	if fn == nil {
		fn = launchAppModeBrowserFallback
	}
	fn(cp.BaseURL + NormalizeRoute(routeOrPath))
}

// Run launches the Native Windows Desktop Application, Single-Instance Guard,
// In-Process/Attached Control Plane, System Tray Icon, and Magpie `/tray` Flyout Popover.
func Run(ctx context.Context, cfg Config) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	enablePerMonitorDPIV2()

	// 1. Single-Instance Guard & IPC Wakeup (`Local\ColabHiveDesktopSingleton`)
	mutexName, err := windows.UTF16PtrFromString(SingletonMutexName)
	if err != nil {
		return err
	}
	hMutex, mutexErr := windows.CreateMutex(nil, false, mutexName)
	if errors.Is(mutexErr, windows.ERROR_ALREADY_EXISTS) {
		if hMutex != 0 {
			_ = windows.CloseHandle(hMutex)
		}
		wakeExistingInstance(cfg.StartMinimized)
		return nil
	}
	if mutexErr != nil {
		return fmt.Errorf("failed to create singleton mutex: %w", mutexErr)
	}
	defer windows.CloseHandle(hMutex)

	// 2. Initialize STA COM for Edge WebView2
	coRes, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if int32(coRes) >= 0 {
		defer procCoUninitialize.Call()
	}

	exePath, _ := os.Executable()
	app := &desktopApp{
		cfg:               cfg,
		exePath:           exePath,
		browserFallbackFn: launchAppModeBrowserFallback,
		quitDoneCh:        make(chan struct{}),
		autoStartEnabled:  IsAutoStartEnabled(),
		currentTooltip:    FormatTrayTooltip(nil, false),
		debouncer:         NewToggleDebouncer(400 * time.Millisecond),
	}

	activeAppMu.Lock()
	activeApp = app
	activeAppMu.Unlock()
	defer func() {
		activeAppMu.Lock()
		if activeApp == app {
			activeApp = nil
		}
		activeAppMu.Unlock()
	}()

	// 3. Create programmatic multi-resolution brand icons & hidden HWNDs BEFORE starting HTTP
	//    so `ColabHiveDesktopTrayHost` is immediately discoverable by second-instance wakeups
	//    and ready to dispatch `/api/v1/desktop/*` bridge actions with zero startup race!
	if err := app.initWin32ResourcesAndWindows(); err != nil {
		return err
	}
	defer app.cleanupWin32Resources()

	// 4. Attach to running ColabHive daemon on `:7860` OR start full control-plane stack in-process!
	cp, err := EnsureControlPlane(ctx, cfg, app.handleDesktopBridge)
	if err != nil {
		return err
	}
	app.setCP(cp)
	defer cp.Close()

	// 5. Add System Tray icon immediately
	app.addOrRestoreTrayIcon(true)
	defer app.deleteTrayIcon()

	// 6. Initialize Shared Edge WebView2 Environment & Both Surfaces (`/` + `/tray?native=1`)
	if mgr, wvErr := newWebviewManager(); wvErr == nil {
		app.wvMgr = mgr

		if app.hwndMain != 0 {
			var mainSurf *webviewSurface
			mainSurf = newWebviewSurface(
				app.hwndMain,
				cp.BaseURL+"/",
				false,
				!cfg.StartMinimized,
				app.autoStartEnabled,
				func(raw string) {
					app.dispatch(func() { app.handleWebViewRawMessage(mainSurf, raw) })
				},
				nil,
				func() {
					app.dispatch(func() { app.onMainWebViewFailed() })
				},
			)
			app.mainSurface = mainSurf
			mgr.attachSurface(app.mainSurface)
		}

		if app.hwndTrayFlyout != 0 {
			// Keep initialVisible=true while HWND is SW_HIDE so `/tray` pre-renders DOM & telemetry
			// in background for instant 0ms first left-click pop-up!
			var traySurf *webviewSurface
			traySurf = newWebviewSurface(
				app.hwndTrayFlyout,
				cp.BaseURL+"/tray?native=1",
				true,
				true,
				app.autoStartEnabled,
				func(raw string) {
					app.dispatch(func() { app.handleWebViewRawMessage(traySurf, raw) })
				},
				func() {
					app.dispatch(func() { app.hideTrayFlyout() })
				},
				func() {
					app.dispatch(func() { app.onTrayWebViewFailed() })
				},
			)
			app.traySurface = traySurf
			mgr.attachSurface(app.traySurface)
		}
	}

	// 7. Show Main Window on startup unless `--minimized` / `--tray-only` was requested
	if !cfg.StartMinimized {
		app.showMainWindow("/")
	}

	// 8. Start context cancellation watcher & 15-second background `/api/v1/pool/summary` poller
	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()

	go func() {
		select {
		case <-ctx.Done():
			app.dispatch(func() { app.quitApplication() })
		case <-app.quitDoneCh:
		}
	}()

	go app.runTelemetryPoller(pollCtx)

	// 9. Run canonical Win32 STA message pump until `PostQuitMessage(0)`
	var msg winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	close(app.quitDoneCh)
	return nil
}

const secondInstanceWakeTimeout = 3500 * time.Millisecond

func wakeExistingInstance(minimized bool) {
	_ = wakeExistingInstanceWithTimeout(minimized, secondInstanceWakeTimeout)
}

func wakeExistingInstanceWithTimeout(minimized bool, maxWait time.Duration) bool {
	className, err := windows.UTF16PtrFromString(TrayHostClassName)
	if err != nil {
		return false
	}
	wParam := uintptr(0)
	if minimized {
		wParam = 1
	}
	if maxWait <= 0 {
		maxWait = secondInstanceWakeTimeout
	}
	deadline := time.Now().Add(maxWait)
	for {
		hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
		if hwnd != 0 {
			if !minimized {
				var pid uint32
				_, _, _ = procGetWindowThreadProcID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
				if pid != 0 {
					_, _, _ = procAllowSetForegroundWin.Call(uintptr(pid))
				}
			}
			_, _, _ = procPostMessageW.Call(hwnd, wmAppShowMainWindow, wParam, 0)
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (a *desktopApp) initWin32ResourcesAndWindows() error {
	ensureWndProcCallbacks()

	var hMod windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &hMod); err == nil {
		a.hInstance = uintptr(hMod)
	}

	a.hIconBig = createBrandHIcon(64)
	a.hIconSmall = createBrandHIcon(32)
	// Warm Stone `#F7F7F5` -> COLORREF 0x00BBGGRR = 0x00F5F7F7
	a.hBrushWarmStone, _, _ = procCreateSolidBrush.Call(0x00F5F7F7)

	hCursor, _, _ := procLoadCursorW.Call(0, idcArrow)

	if taskbarMsgName, err := windows.UTF16PtrFromString("TaskbarCreated"); err == nil {
		msgID, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(taskbarMsgName)))
		a.wmTaskbarCreated = uint32(msgID)
	}

	// 1. Register & Create Hidden Tray Host Window (`ColabHiveDesktopTrayHost`)
	trayHostCls, _ := windows.UTF16PtrFromString(TrayHostClassName)
	wcTrayHost := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   trayHostWndProcPtr,
		HInstance:     a.hInstance,
		HCursor:       hCursor,
		LpszClassName: trayHostCls,
	}
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wcTrayHost)))

	trayHostTitle, _ := windows.UTF16PtrFromString("ColabHive Tray Host")
	hwndTray, _, createErr := procCreateWindowExW.Call(
		wsExToolWindow,
		uintptr(unsafe.Pointer(trayHostCls)),
		uintptr(unsafe.Pointer(trayHostTitle)),
		wsPopup,
		0, 0, 0, 0,
		0, 0, a.hInstance, 0,
	)
	if hwndTray == 0 {
		return fmt.Errorf("CreateWindowExW(ColabHiveDesktopTrayHost) failed: %w", createErr)
	}
	a.setTrayHostHWND(hwndTray)

	// 2. Register & Create Resizable Main Desktop Window (`1380x880` logical px)
	mainCls, _ := windows.UTF16PtrFromString(MainWindowClassName)
	wcMain := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   mainWndProcPtr,
		HInstance:     a.hInstance,
		HIcon:         a.hIconBig,
		HIconSm:       a.hIconSmall,
		HCursor:       hCursor,
		HbrBackground: a.hBrushWarmStone,
		LpszClassName: mainCls,
	}
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wcMain)))

	_, primaryWork, primaryDPI := queryPrimaryMonitorWorkArea()
	mainRect := ComputeCenteredWindowRect(DefaultMainLogicalWidth, DefaultMainLogicalHeight, primaryDPI, primaryWork)
	mainTitle, _ := windows.UTF16PtrFromString("ColabHive")

	hwndMain, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(mainCls)),
		uintptr(unsafe.Pointer(mainTitle)),
		wsOverlappedWindow|wsClipChildren,
		uintptr(int(mainRect.Left)),
		uintptr(int(mainRect.Top)),
		uintptr(int(mainRect.Width())),
		uintptr(int(mainRect.Height())),
		0, 0, a.hInstance, 0,
	)
	if hwndMain != 0 {
		a.hwndMain = hwndMain
		applyFramelessMainWindowChrome(hwndMain)
		if a.hIconBig != 0 {
			_, _, _ = procSendMessageW.Call(hwndMain, wmSetIcon, iconBig, a.hIconBig)
		}
		if a.hIconSmall != 0 {
			_, _, _ = procSendMessageW.Call(hwndMain, wmSetIcon, iconSmall, a.hIconSmall)
		}
	}

	// 3. Register & Create Borderless Magpie Tray Flyout Window (`396x580` logical px)
	flyoutCls, _ := windows.UTF16PtrFromString(TrayFlyoutClassName)
	wcFlyout := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   trayFlyoutWndProcPtr,
		HInstance:     a.hInstance,
		HIcon:         a.hIconBig,
		HIconSm:       a.hIconSmall,
		HCursor:       hCursor,
		HbrBackground: a.hBrushWarmStone,
		LpszClassName: flyoutCls,
	}
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wcFlyout)))

	flyW := ScaleForDPI(DefaultFlyoutLogicalWidth, primaryDPI)
	flyH := ScaleForDPI(DefaultFlyoutLogicalHeight, primaryDPI)
	flyoutTitle, _ := windows.UTF16PtrFromString("ColabHive Quick Panel")

	hwndFlyout, _, _ := procCreateWindowExW.Call(
		wsExToolWindow|wsExTopmost,
		uintptr(unsafe.Pointer(flyoutCls)),
		uintptr(unsafe.Pointer(flyoutTitle)),
		wsPopup|wsClipChildren,
		uintptr(int(primaryWork.Right-flyW-8)),
		uintptr(int(primaryWork.Bottom-flyH-8)),
		uintptr(int(flyW)),
		uintptr(int(flyH)),
		0, 0, a.hInstance, 0,
	)
	if hwndFlyout != 0 {
		a.hwndTrayFlyout = hwndFlyout
		applyWin11WindowChrome(hwndFlyout, true)
	}

	return nil
}

func (a *desktopApp) cleanupWin32Resources() {
	if a.wvMgr != nil {
		a.wvMgr.closeAll()
		a.wvMgr = nil
	}
	if a.hwndTrayFlyout != 0 {
		_, _, _ = procDestroyWindow.Call(a.hwndTrayFlyout)
		a.hwndTrayFlyout = 0
	}
	if a.hwndMain != 0 {
		_, _, _ = procDestroyWindow.Call(a.hwndMain)
		a.hwndMain = 0
	}
	if hwnd := a.getTrayHostHWND(); hwnd != 0 {
		a.setTrayHostHWND(0)
		_, _, _ = procDestroyWindow.Call(hwnd)
	}
	if a.hIconBig != 0 {
		_, _, _ = procDestroyIcon.Call(a.hIconBig)
		a.hIconBig = 0
	}
	if a.hIconSmall != 0 {
		_, _, _ = procDestroyIcon.Call(a.hIconSmall)
		a.hIconSmall = 0
	}
	if a.hBrushWarmStone != 0 {
		_, _, _ = procDeleteObject.Call(a.hBrushWarmStone)
		a.hBrushWarmStone = 0
	}
}

// ============================================================================
// Thread-Safe UI Dispatch & Desktop Bridge Implementation
// ============================================================================

func (a *desktopApp) setTrayHostHWND(hwnd uintptr) {
	a.dispatchMu.Lock()
	a.hwndTrayHost = hwnd
	hasQueued := hwnd != 0 && len(a.dispatchQ) > 0
	a.dispatchMu.Unlock()

	if hasQueued {
		_, _, _ = procPostMessageW.Call(hwnd, wmAppDispatchQueue, 0, 0)
	}
}

func (a *desktopApp) getTrayHostHWND() uintptr {
	a.dispatchMu.Lock()
	hwnd := a.hwndTrayHost
	a.dispatchMu.Unlock()
	return hwnd
}

func (a *desktopApp) dispatch(fn func()) {
	if fn == nil || a.quitting.Load() {
		return
	}
	a.dispatchMu.Lock()
	a.dispatchQ = append(a.dispatchQ, fn)
	hwnd := a.hwndTrayHost
	a.dispatchMu.Unlock()

	if hwnd != 0 {
		_, _, _ = procPostMessageW.Call(hwnd, wmAppDispatchQueue, 0, 0)
	}
}

func (a *desktopApp) drainDispatchQueue() {
	a.dispatchMu.Lock()
	tasks := a.dispatchQ
	a.dispatchQ = nil
	a.dispatchMu.Unlock()

	for _, fn := range tasks {
		if fn != nil {
			fn()
		}
	}
}

func (a *desktopApp) snapshotState() server.DesktopState {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	baseURL := ""
	if cp := a.getCP(); cp != nil {
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

func (a *desktopApp) handleDesktopBridge(ctx context.Context, req server.DesktopActionRequest) (server.DesktopState, error) {
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
		a.dispatch(func() { a.quitApplication() })
		return st, nil
	}

	done := make(chan struct{})
	var execErr error
	a.dispatch(func() {
		execErr = a.executeActionOnUIThread(act, route, req.Enabled)
		close(done)
	})

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()

	select {
	case <-done:
		return a.snapshotState(), execErr
	case <-ctx.Done():
		return a.snapshotState(), ctx.Err()
	case <-a.quitDoneCh:
		return a.snapshotState(), nil
	case <-timer.C:
		return a.snapshotState(), errors.New("desktop UI thread dispatch timed out")
	}
}

func (a *desktopApp) handleWebViewRawMessage(surf *webviewSurface, raw string) {
	req, extURL, reqID, ok := ParseWebViewMessageWithAck(raw)
	if !ok {
		if reqID != "" && surf != nil {
			surf.ackWebViewAction(reqID, a.snapshotState(), errors.New("unsupported desktop action"))
		}
		return
	}
	if req.Action == "state" {
		if reqID != "" && surf != nil {
			surf.ackWebViewAction(reqID, a.snapshotState(), nil)
		}
		return
	}
	if req.Action == "open-external" {
		openExternalBrowserURL(extURL)
		if reqID != "" && surf != nil {
			surf.ackWebViewAction(reqID, a.snapshotState(), nil)
		}
		return
	}
	if req.Action == "quit" {
		a.stateMu.Lock()
		a.lastAction = "quit"
		a.stateMu.Unlock()
		if reqID != "" && surf != nil {
			surf.ackWebViewAction(reqID, a.snapshotState(), nil)
		}
		a.quitApplication()
		return
	}
	execErr := a.executeActionOnUIThread(req.Action, req.Route, req.Enabled)
	if reqID != "" && surf != nil {
		surf.ackWebViewAction(reqID, a.snapshotState(), execErr)
	}
}

func (a *desktopApp) executeActionOnUIThread(action string, route string, explicitEnabled *bool) error {
	act := strings.ToLower(strings.TrimSpace(action))

	a.stateMu.Lock()
	a.lastAction = act
	a.stateMu.Unlock()

	switch act {
	case "open-main":
		a.hideTrayFlyout()
		a.showMainWindow("/")
	case "open-route":
		a.hideTrayFlyout()
		a.showMainWindow(NormalizeRoute(route))
	case "hide-tray":
		a.hideTrayFlyout()
	case "show-tray":
		a.showTrayFlyout()
	case "toggle-tray":
		if a.debouncer.ShouldExecute("toggle-tray", time.Now()) {
			a.toggleTrayFlyout()
		}
	case "toggle-autostart":
		if a.debouncer.ShouldExecute("toggle-autostart", time.Now()) {
			next := !IsAutoStartEnabled()
			if err := SetAutoStartEnabled(next, a.exePath); err != nil {
				a.debouncer.Rollback("toggle-autostart")
				return err
			}
			a.setAutoStartState(next)
		}
	case "set-autostart":
		target := !IsAutoStartEnabled()
		if explicitEnabled != nil {
			target = *explicitEnabled
		}
		if err := SetAutoStartEnabled(target, a.exePath); err != nil {
			return err
		}
		a.setAutoStartState(target)
	case "window-drag", "start-drag":
		if a.hwndMain != 0 && a.debouncer.ShouldExecuteWithin("window-drag", time.Now(), 150*time.Millisecond) {
			_, _, _ = procReleaseCapture.Call()
			_, _, _ = procPostMessageW.Call(a.hwndMain, wmNcLButtonDown, HTCaption, 0)
		}
	case "window-resize":
		if a.hwndMain != 0 && !isWindowMaximized(a.hwndMain) && a.debouncer.ShouldExecuteWithin("window-resize", time.Now(), 150*time.Millisecond) {
			if ht, ok := ParseResizeEdgeHitTest(route); ok {
				_, _, _ = procReleaseCapture.Call()
				_, _, _ = procPostMessageW.Call(a.hwndMain, wmNcLButtonDown, ht, 0)
			}
		}
	case "window-minimize":
		if a.hwndMain != 0 {
			_, _, _ = procShowWindow.Call(a.hwndMain, swMinimize)
		}
	case "window-maximize", "window-toggle-maximize":
		if a.hwndMain != 0 && a.debouncer.ShouldExecuteWithin("window-toggle-maximize", time.Now(), 220*time.Millisecond) {
			if isWindowMaximized(a.hwndMain) {
				_, _, _ = procShowWindow.Call(a.hwndMain, swRestore)
			} else {
				_, _, _ = procShowWindow.Call(a.hwndMain, swMaximize)
			}
			if a.mainSurface != nil {
				a.mainSurface.resizeToClientRect()
				a.mainSurface.syncWindowState(isWindowMaximized(a.hwndMain))
			}
		}
	case "window-close":
		a.minimizeMainWindowToTray()
	case "quit":
		a.quitApplication()
	}
	return nil
}

func (a *desktopApp) setAutoStartState(enabled bool) {
	a.stateMu.Lock()
	a.autoStartEnabled = enabled
	a.stateMu.Unlock()

	if a.mainSurface != nil {
		a.mainSurface.syncAutostartState(enabled)
	}
	if a.traySurface != nil {
		a.traySurface.syncAutostartState(enabled)
	}
}

// ============================================================================
// Main Window & Magpie Tray Flyout Window Actions
// ============================================================================

func (a *desktopApp) onMainWebViewFailed() {
	a.stateMu.RLock()
	wasVis := a.mainWindowVisible
	a.stateMu.RUnlock()

	if a.hwndMain != 0 {
		if winVis, _, _ := procIsWindowVisible.Call(a.hwndMain); winVis != 0 {
			wasVis = true
			_, _, _ = procShowWindow.Call(a.hwndMain, swHide)
		}
	}
	a.stateMu.Lock()
	a.mainWindowVisible = false
	a.stateMu.Unlock()

	if wasVis {
		route := "/"
		if a.mainSurface != nil && strings.TrimSpace(a.mainSurface.pendingRoute) != "" {
			route = a.mainSurface.pendingRoute
		}
		a.invokeBrowserFallback(route)
	}
}

func (a *desktopApp) onTrayWebViewFailed() {
	a.stateMu.RLock()
	wasVis := a.trayVisible
	a.stateMu.RUnlock()

	a.hideTrayFlyout()
	if wasVis {
		a.invokeBrowserFallback("/tray")
	}
}

func (a *desktopApp) showMainWindow(route string) {
	cleanRoute := NormalizeRoute(route)

	// If WebView2 Evergreen Runtime is absent or failed asynchronously on this Windows host,
	// hide any blank HWND and fall back cleanly to Edge/Chrome `--app=`.
	if a.wvMgr == nil || a.wvMgr.isFailed() || a.hwndMain == 0 || a.mainSurface == nil || a.mainSurface.isFailed() {
		if a.hwndMain != 0 {
			_, _, _ = procShowWindow.Call(a.hwndMain, swHide)
		}
		a.stateMu.Lock()
		a.mainWindowVisible = false
		a.stateMu.Unlock()
		a.invokeBrowserFallback(cleanRoute)
		return
	}

	if a.mainSurface != nil {
		a.mainSurface.setVisible(true)
		if cleanRoute != "" && cleanRoute != "/" {
			a.mainSurface.openSPARoute(cleanRoute)
		} else if !a.mainSurface.navigated && a.mainSurface.ready {
			if cp := a.getCP(); cp != nil && cp.BaseURL != "" {
				a.mainSurface.navigateRaw(cp.BaseURL + "/")
			}
		}
	}

	iconic, _, _ := procIsIconic.Call(a.hwndMain)
	if iconic != 0 {
		_, _, _ = procShowWindow.Call(a.hwndMain, swRestore)
	} else {
		_, _, _ = procShowWindow.Call(a.hwndMain, swShow)
	}
	_, _, _ = procUpdateWindow.Call(a.hwndMain)
	_, _, _ = procSetForegroundWindow.Call(a.hwndMain)

	if a.mainSurface != nil {
		a.mainSurface.resizeToClientRect()
		a.mainSurface.syncWindowState(isWindowMaximized(a.hwndMain))
		a.mainSurface.focus()
	}

	a.stateMu.Lock()
	a.mainWindowVisible = true
	a.stateMu.Unlock()
}

func (a *desktopApp) minimizeMainWindowToTray() {
	if a.hwndMain == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(a.hwndMain, swHide)
	if a.mainSurface != nil {
		a.mainSurface.setVisible(false)
	}

	a.stateMu.Lock()
	a.mainWindowVisible = false
	a.stateMu.Unlock()

	// Show a subtle one-time tray notification on the first close-to-tray action.
	if a.minimizeHintSent.CompareAndSwap(false, true) {
		a.showTrayBalloon("ColabHive", "ColabHive 已最小化到系统托盘，点击托盘图标可随时呼出")
	}
}

func (a *desktopApp) queryTrayIconRect() (*Rect, Point) {
	var cur winPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
	cursorPt := Point{X: cur.X, Y: cur.Y}

	if trayHWND := a.getTrayHostHWND(); trayHWND != 0 && procShellNotifyIconGetRect.Find() == nil {
		ident := notifyIconIdentifier{
			CbSize: uint32(unsafe.Sizeof(notifyIconIdentifier{})),
			HWnd:   trayHWND,
			UID:    trayIconUID,
		}
		var iconRc winRect
		hr, _, _ := procShellNotifyIconGetRect.Call(
			uintptr(unsafe.Pointer(&ident)),
			uintptr(unsafe.Pointer(&iconRc)),
		)
		if int32(hr) >= 0 {
			r := iconRc.toRect()
			if !r.Empty() {
				return &r, cursorPt
			}
		}
	}
	return nil, cursorPt
}

func (a *desktopApp) showTrayFlyout() {
	if a.wvMgr == nil || a.wvMgr.isFailed() || a.hwndTrayFlyout == 0 || a.traySurface == nil || a.traySurface.isFailed() {
		// If WebView2 is absent or failed asynchronously, hide any blank HWND and open `/tray` in `--app=` fallback.
		if a.hwndTrayFlyout != 0 {
			_, _, _ = procShowWindow.Call(a.hwndTrayFlyout, swHide)
		}
		a.stateMu.Lock()
		a.trayVisible = false
		a.stateMu.Unlock()
		a.invokeBrowserFallback("/tray")
		return
	}

	anchorRect, cursorPt := a.queryTrayIconRect()
	probePt := cursorPt
	if anchorRect != nil && !anchorRect.Empty() {
		probePt = anchorRect.Center()
	}
	monBounds, workArea, monDPI := queryMonitorAtPoint(probePt, a.hwndTrayFlyout)

	placement := ComputeFlyoutPosition(FlyoutLayoutInput{
		LogicalWidth:  DefaultFlyoutLogicalWidth,
		LogicalHeight: DefaultFlyoutLogicalHeight,
		GapLogicalPx:  DefaultFlyoutGapPx,
		DPI:           monDPI,
		AnchorRect:    anchorRect,
		Cursor:        cursorPt,
		MonitorBounds: monBounds,
		WorkArea:      workArea,
	})

	_, _, _ = procSetWindowPos.Call(
		a.hwndTrayFlyout,
		hwndTopmost,
		uintptr(int(placement.X)),
		uintptr(int(placement.Y)),
		uintptr(int(placement.Width)),
		uintptr(int(placement.Height)),
		swpNoActivate|swpFrameChanged,
	)

	if a.traySurface != nil {
		a.traySurface.setVisible(true)
		a.traySurface.resizeToClientRect()
		a.traySurface.notifyPositionChanged()
		a.traySurface.syncAutostartState(IsAutoStartEnabled())
	}

	_, _, _ = procShowWindow.Call(a.hwndTrayFlyout, swShowNoActivate)
	_, _, _ = procSetForegroundWindow.Call(a.hwndTrayFlyout)
	if a.traySurface != nil {
		a.traySurface.focus()
	}

	a.stateMu.Lock()
	a.trayVisible = true
	a.stateMu.Unlock()
}

func (a *desktopApp) hideTrayFlyout() {
	if a.hwndTrayFlyout == 0 {
		return
	}
	vis, _, _ := procIsWindowVisible.Call(a.hwndTrayFlyout)
	if vis != 0 {
		_, _, _ = procShowWindow.Call(a.hwndTrayFlyout, swHide)
	}
	a.stateMu.Lock()
	a.trayVisible = false
	a.stateMu.Unlock()
}

func (a *desktopApp) toggleTrayFlyout() {
	a.stateMu.RLock()
	currentlyVisible := a.trayVisible
	a.stateMu.RUnlock()

	if currentlyVisible {
		a.hideTrayFlyout()
		return
	}
	a.showTrayFlyout()
}

func (a *desktopApp) quitApplication() {
	if !a.quitting.CompareAndSwap(false, true) {
		return
	}
	a.deleteTrayIcon()
	if a.wvMgr != nil {
		a.wvMgr.closeAll()
		a.wvMgr = nil
	}
	if a.hwndTrayFlyout != 0 {
		_, _, _ = procDestroyWindow.Call(a.hwndTrayFlyout)
		a.hwndTrayFlyout = 0
	}
	if a.hwndMain != 0 {
		_, _, _ = procDestroyWindow.Call(a.hwndMain)
		a.hwndMain = 0
	}
	if hwnd := a.getTrayHostHWND(); hwnd != 0 {
		a.setTrayHostHWND(0)
		_, _, _ = procDestroyWindow.Call(hwnd)
	}
	_, _, _ = procPostQuitMessage.Call(0)
}

// ============================================================================
// System Tray Icon, Telemetry Polling & Native Win32 Context Menu
// ============================================================================

func (a *desktopApp) addOrRestoreTrayIcon(initialAdd bool) {
	trayHWND := a.getTrayHostHWND()
	if trayHWND == 0 || a.quitting.Load() {
		return
	}
	a.stateMu.RLock()
	tip := a.currentTooltip
	a.stateMu.RUnlock()

	nid := notifyIconDataW{
		CbSize:           uint32(unsafe.Sizeof(notifyIconDataW{})),
		HWnd:             trayHWND,
		UID:              trayIconUID,
		UFlags:           nifMessage | nifIcon | nifTip | nifShowTip,
		UCallbackMessage: wmAppTrayCallback,
		HIcon:            a.hIconSmall,
	}
	copyUTF16Buffer(nid.SzTip[:], tip)

	if !initialAdd {
		ok, _, _ := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
		if ok != 0 {
			return
		}
	}
	_, _, _ = procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
}

func (a *desktopApp) updateTrayTooltip(tip string) {
	trayHWND := a.getTrayHostHWND()
	if trayHWND == 0 || a.quitting.Load() {
		return
	}
	nid := notifyIconDataW{
		CbSize: uint32(unsafe.Sizeof(notifyIconDataW{})),
		HWnd:   trayHWND,
		UID:    trayIconUID,
		UFlags: nifTip | nifShowTip,
	}
	copyUTF16Buffer(nid.SzTip[:], tip)
	_, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func (a *desktopApp) showTrayBalloon(title, message string) {
	trayHWND := a.getTrayHostHWND()
	if trayHWND == 0 || a.quitting.Load() {
		return
	}
	a.stateMu.RLock()
	tip := a.currentTooltip
	a.stateMu.RUnlock()

	nid := notifyIconDataW{
		CbSize:      uint32(unsafe.Sizeof(notifyIconDataW{})),
		HWnd:        trayHWND,
		UID:         trayIconUID,
		UFlags:      nifInfo | nifTip | nifShowTip,
		DwInfoFlags: niifNone | niifNoSound,
	}
	copyUTF16Buffer(nid.SzTip[:], tip)
	copyUTF16Buffer(nid.SzInfoTitle[:], title)
	copyUTF16Buffer(nid.SzInfo[:], message)
	_, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func (a *desktopApp) deleteTrayIcon() {
	trayHWND := a.getTrayHostHWND()
	if trayHWND == 0 {
		return
	}
	nid := notifyIconDataW{
		CbSize: uint32(unsafe.Sizeof(notifyIconDataW{})),
		HWnd:   trayHWND,
		UID:    trayIconUID,
	}
	_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func (a *desktopApp) runTelemetryPoller(ctx context.Context) {
	// Initial quick poll so tooltip populates immediately after startup.
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

func (a *desktopApp) pollSummaryOnce(ctx context.Context) {
	cp := a.getCP()
	if cp == nil || a.quitting.Load() {
		return
	}
	sum, err := cp.FetchQuotaSummary(ctx, false)
	if err != nil {
		return
	}
	a.applySummaryUpdate(sum)
}

func (a *desktopApp) applySummaryUpdate(sum *models.QuotaSummary) {
	if sum == nil {
		return
	}
	nextTip := FormatTrayTooltip(sum, true)

	a.stateMu.Lock()
	a.cachedSummary = sum
	a.summaryConnected = true
	changed := a.currentTooltip != nextTip
	a.currentTooltip = nextTip
	a.stateMu.Unlock()

	if changed {
		a.dispatch(func() {
			a.updateTrayTooltip(nextTip)
		})
	}
}

func (a *desktopApp) triggerManualCloudSync() {
	cp := a.getCP()
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

func (a *desktopApp) showNativeTrayContextMenu() {
	a.hideTrayFlyout()

	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	a.stateMu.RLock()
	poolLabel := FormatPoolMenuLabel(a.cachedSummary, a.summaryConnected)
	a.stateMu.RUnlock()

	autostartOn := IsAutoStartEnabled()
	a.stateMu.Lock()
	a.autoStartEnabled = autostartOn
	a.stateMu.Unlock()

	appendTextItem := func(flags uintptr, id uintptr, label string) {
		u16, err := windows.UTF16PtrFromString(label)
		if err != nil {
			return
		}
		_, _, _ = procAppendMenuW.Call(hMenu, flags, id, uintptr(unsafe.Pointer(u16)))
	}
	appendSeparator := func() {
		_, _, _ = procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
	}

	// 1. 打开快捷悬浮面板 (Magpie 视图) (bold default item)
	appendTextItem(mfString, cmdMenuOpenTray, "打开快捷悬浮面板 (Magpie 视图)")
	_, _, _ = procSetMenuDefaultItem.Call(hMenu, cmdMenuOpenTray, 0)

	// 2. 打开 ColabHive 主窗口
	appendTextItem(mfString, cmdMenuOpenMain, "打开 ColabHive 主窗口")

	// 3. ---
	appendSeparator()

	// 4. 算力池: 剩余 X CU · N 台运行中
	appendTextItem(mfString, cmdMenuOpenPool, poolLabel)

	// 5. 立即同步云端算力与实例
	appendTextItem(mfString, cmdMenuSyncNow, "立即同步云端算力与实例")

	// 6. 复制本机 MCP 接口地址
	appendTextItem(mfString, cmdMenuCopyMCPURL, "复制本机 MCP 接口地址")

	// 7. ---
	appendSeparator()

	// 8. 开机自动启动 (MF_CHECKED when HKCU\...\Run\ColabHive is present)
	startupFlags := uintptr(mfString | mfUnchecked)
	if autostartOn {
		startupFlags = uintptr(mfString | mfChecked)
	}
	appendTextItem(startupFlags, cmdMenuToggleStartup, "开机自动启动")

	// 9. ---
	appendSeparator()

	// 10. 退出 ColabHive
	appendTextItem(mfString, cmdMenuQuit, "退出 ColabHive")

	var pt winPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	trayHWND := a.getTrayHostHWND()
	if trayHWND == 0 {
		return
	}
	// Required Win32 pattern: SetForegroundWindow -> TrackPopupMenu -> PostMessage(WM_NULL)
	// so clicking outside cleanly dismisses the native popup menu.
	_, _, _ = procSetForegroundWindow.Call(trayHWND)
	_, _, _ = procTrackPopupMenu.Call(
		hMenu,
		tpmRightButton|tpmBottomAlign,
		uintptr(int(pt.X)),
		uintptr(int(pt.Y)),
		0,
		trayHWND,
		0,
	)
	_, _, _ = procPostMessageW.Call(trayHWND, wmNull, 0, 0)
}

func (a *desktopApp) handleMenuCommand(cmdID uint16) {
	switch cmdID {
	case cmdMenuOpenTray:
		a.showTrayFlyout()
	case cmdMenuOpenMain:
		a.showMainWindow("/")
	case cmdMenuOpenPool:
		a.showMainWindow("/pool")
	case cmdMenuSyncNow:
		a.triggerManualCloudSync()
	case cmdMenuCopyMCPURL:
		if cp := a.getCP(); cp != nil && cp.BaseURL != "" {
			mcpURL := strings.TrimRight(cp.BaseURL, "/") + "/mcp"
			_ = writeClipboardUnicodeText(a.getTrayHostHWND(), mcpURL)
		}
	case cmdMenuToggleStartup:
		next := !IsAutoStartEnabled()
		if err := SetAutoStartEnabled(next, a.exePath); err == nil {
			a.setAutoStartState(next)
		}
	case cmdMenuQuit:
		a.quitApplication()
	}
}

func (a *desktopApp) shouldSuppressTrayClickReopen(now time.Time) bool {
	a.stateMu.RLock()
	currentlyVisible := a.trayVisible
	lastAutoHide := a.lastTrayAutoHideAt
	a.stateMu.RUnlock()

	if currentlyVisible {
		return false
	}
	return a.trayWasOpenOnLDown || (!lastAutoHide.IsZero() && now.Sub(lastAutoHide) < 220*time.Millisecond)
}

func (a *desktopApp) handleTrayCallback(lParam uintptr) {
	evt := uint32(lParam) & 0xFFFF
	switch evt {
	case wmLButtonDown:
		a.stateMu.RLock()
		a.trayWasOpenOnLDown = a.trayVisible
		a.stateMu.RUnlock()

	case wmLButtonDblClk:
		a.trayWasOpenOnLDown = false
		a.ignoreNextLButtonUpUntil = time.Now().Add(320 * time.Millisecond)
		a.hideTrayFlyout()
		a.showMainWindow("/")

	case wmLButtonUp:
		now := time.Now()
		if now.Before(a.ignoreNextLButtonUpUntil) {
			a.trayWasOpenOnLDown = false
			return
		}
		// Scope focus-loss auto-hide debounce strictly to tray-icon pointer left-click (`WM_LBUTTONUP`)
		// so explicit bridge `show-tray`/`toggle-tray` and keyboard toggles are never suppressed.
		if a.shouldSuppressTrayClickReopen(now) {
			a.trayWasOpenOnLDown = false
			return
		}
		a.trayWasOpenOnLDown = false
		a.toggleTrayFlyout()

	case ninSelect, ninKeySelect:
		if time.Now().Before(a.ignoreNextLButtonUpUntil) {
			a.trayWasOpenOnLDown = false
			return
		}
		a.trayWasOpenOnLDown = false
		a.toggleTrayFlyout()

	case wmRButtonUp, wmContextMenu:
		a.trayWasOpenOnLDown = false
		a.showNativeTrayContextMenu()
	}
}

// ============================================================================
// Win32 Window Procedures (`WndProc`)
// ============================================================================

func trayHostWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	app := currentApp()
	if app != nil {
		if app.wmTaskbarCreated != 0 && uint32(msg) == app.wmTaskbarCreated {
			app.addOrRestoreTrayIcon(false)
			return 0
		}
		switch uint32(msg) {
		case wmAppShowMainWindow:
			if wParam == 1 {
				app.stateMu.Lock()
				app.lastAction = "ipc-minimized"
				app.stateMu.Unlock()
				return 0
			}
			app.hideTrayFlyout()
			app.showMainWindow("/")
			return 0
		case wmAppDispatchQueue:
			app.drainDispatchQueue()
			return 0
		case wmAppTrayCallback:
			app.handleTrayCallback(lParam)
			return 0
		case wmCommand:
			app.handleMenuCommand(uint16(wParam & 0xFFFF))
			return 0
		}
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return ret
}

func mainWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	app := currentApp()
	if app != nil {
		switch uint32(msg) {
		case wmNcCalcSize:
			// Seamless frameless client-extended window: remove 100% of the ugly default Win32 caption bar
			// while keeping WS_OVERLAPPEDWINDOW DWM rounded corners, drop shadow, and Win+Arrow Aero Snap!
			if wParam != 0 && lParam != 0 && isWindowMaximized(hwnd) {
				var params ncCalcSizeParams
				_, _, _ = procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&params)), lParam, unsafe.Sizeof(params))
				frameX, frameY := queryMaximizedFrameThickness(hwnd)
				monBounds, workArea, _ := queryMonitorForWindow(hwnd)
				inset := InsetMaximizedClientRect(params.Rgrc[0].toRect(), frameX, frameY, monBounds, workArea)
				params.Rgrc[0] = winRect{
					Left:   inset.Left,
					Top:    inset.Top,
					Right:  inset.Right,
					Bottom: inset.Bottom,
				}
				_, _, _ = procRtlMoveMemory.Call(lParam, uintptr(unsafe.Pointer(&params)), unsafe.Sizeof(params))
			}
			return 0
		case wmNcHitTest:
			var rc winRect
			_, _, _ = procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
			dpi := queryWindowDPI(hwnd)
			borderPx := ScaleForDPI(6, dpi)
			cornerPx := ScaleForDPI(12, dpi)
			pt := Point{
				X: int32(int16(uint16(lParam & 0xFFFF))),
				Y: int32(int16(uint16((lParam >> 16) & 0xFFFF))),
			}
			return HitTestFramelessWindow(rc.toRect(), pt, borderPx, cornerPx, isWindowMaximized(hwnd))
		case wmClose:
			if !app.quitting.Load() {
				app.minimizeMainWindowToTray()
				return 0
			}
		case wmSize:
			if app.mainSurface != nil {
				app.mainSurface.resizeToClientRect()
				if wParam != sizeMinimized {
					app.mainSurface.syncWindowState(isWindowMaximized(hwnd))
				}
			}
		case wmMove, wmMoving:
			if app.mainSurface != nil {
				app.mainSurface.notifyPositionChanged()
			}
		case wmDpiChanged:
			if lParam != 0 {
				var suggested winRect
				_, _, _ = procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&suggested)), lParam, unsafe.Sizeof(suggested))
				_, _, _ = procSetWindowPos.Call(
					hwnd,
					0,
					uintptr(int(suggested.Left)),
					uintptr(int(suggested.Top)),
					uintptr(int(suggested.Right-suggested.Left)),
					uintptr(int(suggested.Bottom-suggested.Top)),
					swpNoZOrder|swpNoActivate,
				)
			}
			if app.mainSurface != nil {
				app.mainSurface.resizeToClientRect()
			}
			return 0
		case wmGetMinMaxInfo:
			if lParam != 0 {
				dpi := queryWindowDPI(hwnd)
				var mmi minMaxInfo
				_, _, _ = procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&mmi)), lParam, unsafe.Sizeof(mmi))
				mmi.PtMinTrackSize.X = ScaleForDPI(860, dpi)
				mmi.PtMinTrackSize.Y = ScaleForDPI(560, dpi)
				_, _, _ = procRtlMoveMemory.Call(lParam, uintptr(unsafe.Pointer(&mmi)), unsafe.Sizeof(mmi))
				return 0
			}
		case wmDestroy:
			if app.quitting.Load() {
				return 0
			}
		}
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return ret
}

func trayFlyoutWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	app := currentApp()
	if app != nil {
		switch uint32(msg) {
		case wmActivate:
			// Instant Magpie / Windows 11 Quick Settings dismissal when clicking anywhere outside!
			if uint16(wParam&0xFFFF) == waInactive && !app.quitting.Load() {
				app.stateMu.Lock()
				wasVis := app.trayVisible
				if wasVis {
					app.lastTrayAutoHideAt = time.Now()
				}
				app.stateMu.Unlock()
				if wasVis {
					app.hideTrayFlyout()
				}
				return 0
			}
		case wmClose:
			if !app.quitting.Load() {
				app.hideTrayFlyout()
				return 0
			}
		case wmSize:
			if app.traySurface != nil {
				app.traySurface.resizeToClientRect()
			}
		case wmMove, wmMoving:
			if app.traySurface != nil {
				app.traySurface.notifyPositionChanged()
			}
		case wmDpiChanged:
			if lParam != 0 {
				var suggested winRect
				_, _, _ = procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&suggested)), lParam, unsafe.Sizeof(suggested))
				_, _, _ = procSetWindowPos.Call(
					hwnd,
					hwndTopmost,
					uintptr(int(suggested.Left)),
					uintptr(int(suggested.Top)),
					uintptr(int(suggested.Right-suggested.Left)),
					uintptr(int(suggested.Bottom-suggested.Top)),
					swpNoActivate,
				)
			}
			if app.traySurface != nil {
				app.traySurface.resizeToClientRect()
			}
			return 0
		}
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return ret
}
