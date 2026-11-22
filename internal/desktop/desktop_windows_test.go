//go:build windows

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/colabhive/colabhive/internal/server"
)

func TestWin32IconAndStructSizes(t *testing.T) {
	for _, sz := range []int{16, 32, 64} {
		hIcon := createBrandHIcon(sz)
		if hIcon == 0 {
			t.Fatalf("createBrandHIcon(%d) returned NULL HICON", sz)
		}
		_, _, _ = procDestroyIcon.Call(hIcon)
	}

	// Verify NOTIFYICONDATAW exact SDK size (976 bytes on 64-bit Windows, 956 bytes on 32-bit Windows)
	gotNID := unsafe.Sizeof(notifyIconDataW{})
	if unsafe.Sizeof(uintptr(0)) == 8 && gotNID != 976 {
		t.Fatalf("expected sizeof(notifyIconDataW)=976 on 64-bit Windows, got %d", gotNID)
	}

	_ = isWebView2RuntimeInstalled()

	js := buildWebViewBootstrapJS(true)
	if !strings.Contains(js, "origFetch(input, init)") {
		t.Fatalf("expected buildWebViewBootstrapJS to delegate /api/v1/desktop/action to origFetch")
	}
	if !strings.Contains(js, "data.handled === false && hasNativeWebView()") {
		t.Fatalf("expected buildWebViewBootstrapJS to fall back to WebView2 postMessage when handled === false")
	}
	if !strings.Contains(js, "__COLABHIVE_DESKTOP_RESOLVE__") {
		t.Fatalf("expected buildWebViewBootstrapJS to wire native req_id ack resolver")
	}
	if !strings.Contains(js, "__COLABHIVE_WINDOW_CONTROLS__") {
		t.Fatalf("expected buildWebViewBootstrapJS to expose frameless window controls on main surface")
	}
}

func TestWin32FramelessMainWindowChromeAndHitTest(t *testing.T) {
	ensureWndProcCallbacks()

	app := &desktopApp{
		debouncer: NewToggleDebouncer(400 * time.Millisecond),
	}
	activeAppMu.Lock()
	prev := activeApp
	activeApp = app
	activeAppMu.Unlock()
	defer func() {
		activeAppMu.Lock()
		activeApp = prev
		activeAppMu.Unlock()
	}()

	var hMod windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hMod)
	clsName, _ := windows.UTF16PtrFromString("ColabHiveFramelessTestWnd")
	wc := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   mainWndProcPtr,
		HInstance:     uintptr(hMod),
		LpszClassName: clsName,
	}
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	title, _ := windows.UTF16PtrFromString("Frameless Test")
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(clsName)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsClipChildren,
		120, 140, 960, 640,
		0, 0, uintptr(hMod), 0,
	)
	if hwnd == 0 {
		t.Fatalf("CreateWindowExW failed: %v", err)
	}
	defer procDestroyWindow.Call(hwnd)
	app.hwndMain = hwnd
	applyFramelessMainWindowChrome(hwnd)

	var wrc, crc winRect
	_, _, _ = procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wrc)))
	_, _, _ = procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&crc)))

	winW, winH := wrc.Right-wrc.Left, wrc.Bottom-wrc.Top
	cliW, cliH := crc.Right-crc.Left, crc.Bottom-crc.Top
	if cliW != winW || cliH != winH {
		t.Fatalf("expected frameless WM_NCCALCSIZE client (%dx%d) == window (%dx%d) with zero system titlebar",
			cliW, cliH, winW, winH)
	}

	packPt := func(x, y int32) uintptr {
		return uintptr(uint16(int16(x))) | (uintptr(uint16(int16(y))) << 16)
	}
	if ht, _, _ := procSendMessageW.Call(hwnd, wmNcHitTest, 0, packPt(wrc.Left+2, wrc.Top+2)); ht != HTTopLeft {
		t.Fatalf("expected HTTopLeft(%d), got %d", HTTopLeft, ht)
	}
	if ht, _, _ := procSendMessageW.Call(hwnd, wmNcHitTest, 0, packPt(wrc.Right-2, wrc.Bottom-2)); ht != HTBottomRight {
		t.Fatalf("expected HTBottomRight(%d), got %d", HTBottomRight, ht)
	}
	if ht, _, _ := procSendMessageW.Call(hwnd, wmNcHitTest, 0, packPt(wrc.Left+winW/2, wrc.Top+2)); ht != HTTop {
		t.Fatalf("expected HTTop(%d), got %d", HTTop, ht)
	}
	if ht, _, _ := procSendMessageW.Call(hwnd, wmNcHitTest, 0, packPt(wrc.Left+winW/2, wrc.Top+winH/2)); ht != HTClient {
		t.Fatalf("expected HTClient(%d), got %d", HTClient, ht)
	}
}

func TestWebViewAsyncFailureFallbackAndFocusLossDebounceScope(t *testing.T) {
	var fallbackURLs []string
	app := &desktopApp{
		browserFallbackFn: func(u string) {
			fallbackURLs = append(fallbackURLs, u)
		},
		debouncer: NewToggleDebouncer(400 * time.Millisecond),
	}
	app.setCP(&ControlPlaneHandle{BaseURL: "http://127.0.0.1:17860"})

	// 1. Simulate WebView2 async environment failure after main window was requested
	mgr := &webviewManager{}
	app.wvMgr = mgr
	mainSurf := newWebviewSurface(
		0,
		"http://127.0.0.1:17860/",
		false,
		true,
		false,
		nil,
		nil,
		func() { app.onMainWebViewFailed() },
	)
	mainSurf.pendingRoute = "/pool"
	app.mainSurface = mainSurf
	app.mainWindowVisible = true
	mgr.attachSurface(mainSurf)

	traySurf := newWebviewSurface(
		0,
		"http://127.0.0.1:17860/tray?native=1",
		true,
		true,
		false,
		nil,
		nil,
		func() { app.onTrayWebViewFailed() },
	)
	app.traySurface = traySurf
	mgr.attachSurface(traySurf)

	// Trigger async E_FAIL (0x80004005)
	mgr.onEnvironmentReady(0x80004005, nil)
	if !mgr.isFailed() || !mainSurf.isFailed() || !traySurf.isFailed() {
		t.Fatalf("expected manager and attached surfaces to be marked failed")
	}
	// Main window was visible -> fallback launched for /pool; tray was hidden -> no spurious /tray popup!
	if len(fallbackURLs) != 1 || fallbackURLs[0] != "http://127.0.0.1:17860/pool" {
		t.Fatalf("expected single main fallback to /pool, got %v", fallbackURLs)
	}
	// Subsequent explicit tray open falls back cleanly to /tray
	app.showTrayFlyout()
	if len(fallbackURLs) != 2 || fallbackURLs[1] != "http://127.0.0.1:17860/tray" {
		t.Fatalf("expected showTrayFlyout to invoke /tray fallback after async failure, got %v", fallbackURLs)
	}

	// 2. Verify 220ms focus-loss debounce ONLY suppresses WM_LBUTTONUP, NOT explicit toggleTrayFlyout/bridge/ninKeySelect!
	fallbackURLs = nil
	now := time.Now()
	app.lastTrayAutoHideAt = now
	if !app.shouldSuppressTrayClickReopen(now.Add(50 * time.Millisecond)) {
		t.Fatalf("expected WM_LBUTTONUP within 220ms of focus-loss auto-hide to be suppressed")
	}
	// Calling wmLButtonUp within 220ms must NOT reopen (zero fallback invocations)
	app.handleTrayCallback(wmLButtonUp)
	if len(fallbackURLs) != 0 {
		t.Fatalf("expected wmLButtonUp within 220ms to stay closed, got %v", fallbackURLs)
	}
	// Calling explicit bridge toggle-tray within the exact same 220ms window MUST NOT be suppressed!
	if err := app.executeActionOnUIThread("toggle-tray", "/tray", nil); err != nil {
		t.Fatalf("executeActionOnUIThread(toggle-tray) failed: %v", err)
	}
	if len(fallbackURLs) != 1 || fallbackURLs[0] != "http://127.0.0.1:17860/tray" {
		t.Fatalf("expected explicit bridge toggle-tray not to be suppressed by lastTrayAutoHideAt, got %v", fallbackURLs)
	}
}

func TestWin32MinimizedLifecycleSingletonAndQuit(t *testing.T) {
	// Skip only if a user-launched ColabHiveDesktopTrayHost or singleton mutex is already active in this session.
	clsPtr, _ := windows.UTF16PtrFromString(TrayHostClassName)
	if existing, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(clsPtr)), 0); existing != 0 {
		t.Skip("ColabHiveDesktopTrayHost is already running in session; skipping singleton lifecycle test")
	}
	if mPtr, mErr := windows.UTF16PtrFromString(SingletonMutexName); mErr == nil {
		hProbe, cErr := windows.CreateMutex(nil, false, mPtr)
		if hProbe != 0 {
			_ = windows.CloseHandle(hProbe)
		}
		if errors.Is(cErr, windows.ERROR_ALREADY_EXISTS) {
			t.Skip("ColabHiveDesktopSingleton mutex is already held by a running desktop instance; skipping singleton lifecycle test")
		}
	}

	tmpDir := t.TempDir()
	daemonPath := filepath.Join(tmpDir, "daemon.json")
	t.Setenv("COLABHIVE_DAEMON_INFO_PATH", daemonPath)
	t.Setenv("LOCALAPPDATA", filepath.Join(tmpDir, "localappdata"))

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Config{
			Host:           "127.0.0.1",
			Port:           0,
			DBPath:         ":memory:",
			Demo:           true,
			StartMinimized: true,
		})
	}()

	// Wait for `ColabHiveDesktopTrayHost` window and `daemon.json` to come online.
	var baseURL string
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(clsPtr)), 0)
		info, err := server.ReadDaemonInfoFrom(daemonPath)
		if hwnd != 0 && err == nil && info != nil && info.URL != "" {
			baseURL = info.URL
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if baseURL == "" {
		cancel()
		t.Fatalf("timed out waiting for minimized desktop host and daemon.json")
	}

	// Verify `/api/v1/desktop/state` reports `desktop_shell: true` and `main_window_visible: false`
	resp, err := http.Get(baseURL + "/api/v1/desktop/state")
	if err != nil {
		cancel()
		t.Fatalf("GET /api/v1/desktop/state failed: %v", err)
	}
	var st server.DesktopState
	_ = json.NewDecoder(resp.Body).Decode(&st)
	_ = resp.Body.Close()
	if !st.DesktopShell || st.MainWindowVisible {
		cancel()
		t.Fatalf("unexpected desktop state for --minimized launch: %+v", st)
	}

	// Verify second `Run` hits single-instance mutex (`Local\ColabHiveDesktopSingleton`), posts `WM_APP+42`, and returns nil!
	if dupErr := Run(ctx, Config{StartMinimized: true}); dupErr != nil {
		cancel()
		t.Fatalf("expected duplicate Run() to exit cleanly via singleton mutex, got: %v", dupErr)
	}

	// Verify frameless Win32 main window (`WM_NCCALCSIZE` removes ugly system caption bar -> ClientRect == WindowRect!)
	if app := currentApp(); app != nil {
		type frameCheck struct {
			winW, winH       int32
			clientW, clientH int32
			htCorner         uintptr
			htCenter         uintptr
		}
		fcCh := make(chan frameCheck, 1)
		app.dispatch(func() {
			var wrc, crc winRect
			if app.hwndMain != 0 {
				_, _, _ = procGetWindowRect.Call(app.hwndMain, uintptr(unsafe.Pointer(&wrc)))
				_, _, _ = procGetClientRect.Call(app.hwndMain, uintptr(unsafe.Pointer(&crc)))
				cornerLParam := uintptr(uint16(int16(wrc.Left+2))) | (uintptr(uint16(int16(wrc.Top+2))) << 16)
				cx := wrc.Left + (wrc.Right-wrc.Left)/2
				cy := wrc.Top + (wrc.Bottom-wrc.Top)/2
				centerLParam := uintptr(uint16(int16(cx))) | (uintptr(uint16(int16(cy))) << 16)
				htCorner, _, _ := procSendMessageW.Call(app.hwndMain, wmNcHitTest, 0, cornerLParam)
				htCenter, _, _ := procSendMessageW.Call(app.hwndMain, wmNcHitTest, 0, centerLParam)
				fcCh <- frameCheck{
					winW:     wrc.Right - wrc.Left,
					winH:     wrc.Bottom - wrc.Top,
					clientW:  crc.Right - crc.Left,
					clientH:  crc.Bottom - crc.Top,
					htCorner: htCorner,
					htCenter: htCenter,
				}
				return
			}
			fcCh <- frameCheck{}
		})
		select {
		case fc := <-fcCh:
			if fc.winW <= 0 || fc.clientW != fc.winW || fc.clientH != fc.winH {
				cancel()
				t.Fatalf("expected frameless WM_NCCALCSIZE client rect (%dx%d) to equal window rect (%dx%d)",
					fc.clientW, fc.clientH, fc.winW, fc.winH)
			}
			if fc.htCorner != HTTopLeft || fc.htCenter != HTClient {
				cancel()
				t.Fatalf("expected WM_NCHITTEST corner=HTTopLeft(%d), center=HTClient(%d), got corner=%d center=%d",
					HTTopLeft, HTClient, fc.htCorner, fc.htCenter)
			}
		case <-time.After(1 * time.Second):
		}
	}

	// If Edge WebView2 Evergreen Runtime is installed on this host, verify real Chromium controller
	// initialization + `window.chrome.webview.postMessage({type:'colabhive:desktop-action',...})` round-trip!
	if isWebView2RuntimeInstalled() {
		wvDeadline := time.Now().Add(6 * time.Second)
		wvReady := false
		for time.Now().Before(wvDeadline) {
			app := currentApp()
			if app != nil {
				readyCh := make(chan bool, 1)
				app.dispatch(func() {
					readyCh <- (app.traySurface != nil && app.traySurface.navigated)
				})
				select {
				case ok := <-readyCh:
					if ok {
						wvReady = true
					}
				case <-time.After(250 * time.Millisecond):
				}
			}
			if wvReady {
				break
			}
			time.Sleep(60 * time.Millisecond)
		}
		if !wvReady {
			cancel()
			t.Fatalf("timed out waiting for hidden /tray WebView2 surface NavigationCompleted")
		}

		// Fire real JS object postMessage inside Chromium -> verify Go receives GetWebMessageAsJSON!
		if app := currentApp(); app != nil {
			app.dispatch(func() {
				if app.traySurface != nil {
					app.traySurface.evalJS(`window.chrome.webview.postMessage({type:'colabhive:desktop-action',action:'hide-tray',route:'/tray'});`)
				}
			})
		}

		msgDeadline := time.Now().Add(3 * time.Second)
		gotBridgeMsg := false
		for time.Now().Before(msgDeadline) {
			r2, gErr := http.Get(baseURL + "/api/v1/desktop/state")
			if gErr == nil {
				var st2 server.DesktopState
				_ = json.NewDecoder(r2.Body).Decode(&st2)
				_ = r2.Body.Close()
				if st2.LastAction == "hide-tray" {
					gotBridgeMsg = true
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
		}
		if !gotBridgeMsg {
			cancel()
			t.Fatalf("did not receive real WebView2 window.chrome.webview.postMessage object payload in Go")
		}

		// Also verify real `fetch('/api/v1/desktop/action')` inside WebView2 passes through `origFetch` to Go's bridge!
		if app := currentApp(); app != nil {
			app.dispatch(func() {
				if app.traySurface != nil {
					app.traySurface.evalJS(`fetch('/api/v1/desktop/action', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action: 'hide-tray', route: '/tray' }) });`)
				}
			})
		}
	}

	// Trigger graceful native `quit` via `/api/v1/desktop/action` and verify clean `WM_QUIT` teardown!
	qResp, err := http.Post(baseURL+"/api/v1/desktop/action", "application/json", bytes.NewBufferString(`{"action":"quit"}`))
	if err != nil {
		cancel()
		t.Fatalf("POST /api/v1/desktop/action quit failed: %v", err)
	}
	_ = qResp.Body.Close()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("Run() returned error on quit: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatalf("timed out waiting for Run() message pump to terminate after quit")
	}
}
