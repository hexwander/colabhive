//go:build windows

package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"

	"github.com/colabhive/colabhive/internal/server"
)

// comProc wraps a COM virtual method table function pointer.
type comProc uintptr

//go:uintptrescapes
func (p comProc) Call(a ...uintptr) (r1, r2 uintptr, lastErr error) {
	return syscall.SyscallN(uintptr(p), a...)
}

type iUnknownVtbl struct {
	QueryInterface comProc
	AddRef         comProc
	Release        comProc
}

type eventRegistrationToken struct {
	Value int64
}

type coreWebView2Color struct {
	A byte
	R byte
	G byte
	B byte
}

type coreWebView2PhysicalKeyStatus struct {
	RepeatCount   uint32
	ScanCode      uint32
	IsExtendedKey int32
	IsMenuKeyDown int32
	WasKeyDown    int32
	IsKeyReleased int32
}

// ICoreWebView2Environment ({b96d755e-0319-4e92-a296-23436f46a1fc})
type iCoreWebView2EnvironmentVtbl struct {
	iUnknownVtbl
	CreateCoreWebView2Controller     comProc
	CreateWebResourceResponse        comProc
	GetBrowserVersionString          comProc
	AddNewBrowserVersionAvailable    comProc
	RemoveNewBrowserVersionAvailable comProc
}

type iCoreWebView2Environment struct {
	vtbl *iCoreWebView2EnvironmentVtbl
}

// ICoreWebView2Controller ({4d00c0d1-9434-4eb6-8078-8697a560334f})
type iCoreWebView2ControllerVtbl struct {
	iUnknownVtbl
	GetIsVisible                      comProc
	PutIsVisible                      comProc
	GetBounds                         comProc
	PutBounds                         comProc
	GetZoomFactor                     comProc
	PutZoomFactor                     comProc
	AddZoomFactorChanged              comProc
	RemoveZoomFactorChanged           comProc
	SetBoundsAndZoomFactor            comProc
	MoveFocus                         comProc
	AddMoveFocusRequested             comProc
	RemoveMoveFocusRequested          comProc
	AddGotFocus                       comProc
	RemoveGotFocus                    comProc
	AddLostFocus                      comProc
	RemoveLostFocus                   comProc
	AddAcceleratorKeyPressed          comProc
	RemoveAcceleratorKeyPressed       comProc
	GetParentWindow                   comProc
	PutParentWindow                   comProc
	NotifyParentWindowPositionChanged comProc
	Close                             comProc
	GetCoreWebView2                   comProc
}

type iCoreWebView2Controller struct {
	vtbl *iCoreWebView2ControllerVtbl
}

// ICoreWebView2Controller2 ({c979903e-d4ca-4228-92eb-47ee3fa96eab})
type iCoreWebView2Controller2Vtbl struct {
	iCoreWebView2ControllerVtbl
	GetDefaultBackgroundColor comProc
	PutDefaultBackgroundColor comProc
}

type iCoreWebView2Controller2 struct {
	vtbl *iCoreWebView2Controller2Vtbl
}

var iidICoreWebView2Controller2 = windows.GUID{
	Data1: 0xc979903e,
	Data2: 0xd4ca,
	Data3: 0x4228,
	Data4: [8]byte{0x92, 0xeb, 0x47, 0xee, 0x3f, 0xa9, 0x6e, 0xab},
}

// ICoreWebView2 ({76eceacb-0462-4d94-ac83-423a6793775e})
type iCoreWebView2Vtbl struct {
	iUnknownVtbl
	GetSettings                            comProc
	GetSource                              comProc
	Navigate                               comProc
	NavigateToString                       comProc
	AddNavigationStarting                  comProc
	RemoveNavigationStarting               comProc
	AddContentLoading                      comProc
	RemoveContentLoading                   comProc
	AddSourceChanged                       comProc
	RemoveSourceChanged                    comProc
	AddHistoryChanged                      comProc
	RemoveHistoryChanged                   comProc
	AddNavigationCompleted                 comProc
	RemoveNavigationCompleted              comProc
	AddFrameNavigationStarting             comProc
	RemoveFrameNavigationStarting          comProc
	AddFrameNavigationCompleted            comProc
	RemoveFrameNavigationCompleted         comProc
	AddScriptDialogOpening                 comProc
	RemoveScriptDialogOpening              comProc
	AddPermissionRequested                 comProc
	RemovePermissionRequested              comProc
	AddProcessFailed                       comProc
	RemoveProcessFailed                    comProc
	AddScriptToExecuteOnDocumentCreated    comProc
	RemoveScriptToExecuteOnDocumentCreated comProc
	ExecuteScript                          comProc
	CapturePreview                         comProc
	Reload                                 comProc
	PostWebMessageAsJSON                   comProc
	PostWebMessageAsString                 comProc
	AddWebMessageReceived                  comProc
	RemoveWebMessageReceived               comProc
	CallDevToolsProtocolMethod             comProc
	GetBrowserProcessID                    comProc
	GetCanGoBack                           comProc
	GetCanGoForward                        comProc
	GoBack                                 comProc
	GoForward                              comProc
	GetDevToolsProtocolEventReceiver       comProc
	Stop                                   comProc
	AddNewWindowRequested                  comProc
	RemoveNewWindowRequested               comProc
	AddDocumentTitleChanged                comProc
	RemoveDocumentTitleChanged             comProc
	GetDocumentTitle                       comProc
	AddHostObjectToScript                  comProc
	RemoveHostObjectFromScript             comProc
	OpenDevToolsWindow                     comProc
	AddContainsFullScreenElementChanged    comProc
	RemoveContainsFullScreenElementChanged comProc
	GetContainsFullScreenElement           comProc
	AddWebResourceRequested                comProc
	RemoveWebResourceRequested             comProc
	AddWebResourceRequestedFilter          comProc
	RemoveWebResourceRequestedFilter       comProc
	AddWindowCloseRequested                comProc
	RemoveWindowCloseRequested             comProc
}

type iCoreWebView2 struct {
	vtbl *iCoreWebView2Vtbl
}

// ICoreWebView2Settings ({e562e4f0-d7fa-43ac-8d71-c05150499f00})
type iCoreWebView2SettingsVtbl struct {
	iUnknownVtbl
	GetIsScriptEnabled                  comProc
	PutIsScriptEnabled                  comProc
	GetIsWebMessageEnabled              comProc
	PutIsWebMessageEnabled              comProc
	GetAreDefaultScriptDialogsEnabled   comProc
	PutAreDefaultScriptDialogsEnabled   comProc
	GetIsStatusBarEnabled               comProc
	PutIsStatusBarEnabled               comProc
	GetAreDevToolsEnabled               comProc
	PutAreDevToolsEnabled               comProc
	GetAreDefaultContextMenusEnabled    comProc
	PutAreDefaultContextMenusEnabled    comProc
	GetAreHostObjectsAllowed            comProc
	PutAreHostObjectsAllowed            comProc
	GetIsZoomControlEnabled             comProc
	PutIsZoomControlEnabled             comProc
	GetIsBuiltInErrorPageEnabled        comProc
	PutIsBuiltInErrorPageEnabled        comProc
	GetUserAgent                        comProc
	PutUserAgent                        comProc
	GetAreBrowserAcceleratorKeysEnabled comProc
	PutAreBrowserAcceleratorKeysEnabled comProc
}

type iCoreWebView2Settings struct {
	vtbl *iCoreWebView2SettingsVtbl
}

// ICoreWebView2WebMessageReceivedEventArgs
type iCoreWebView2WebMessageReceivedEventArgsVtbl struct {
	iUnknownVtbl
	GetSource                comProc
	GetWebMessageAsJSON      comProc
	TryGetWebMessageAsString comProc
}

type iCoreWebView2WebMessageReceivedEventArgs struct {
	vtbl *iCoreWebView2WebMessageReceivedEventArgsVtbl
}

// ICoreWebView2NavigationCompletedEventArgs
type iCoreWebView2NavigationCompletedEventArgsVtbl struct {
	iUnknownVtbl
	GetIsSuccess      comProc
	GetWebErrorStatus comProc
	GetNavigationID   comProc
}

type iCoreWebView2NavigationCompletedEventArgs struct {
	vtbl *iCoreWebView2NavigationCompletedEventArgsVtbl
}

// ICoreWebView2ProcessFailedEventArgs
type iCoreWebView2ProcessFailedEventArgsVtbl struct {
	iUnknownVtbl
	GetProcessFailedKind comProc
}

type iCoreWebView2ProcessFailedEventArgs struct {
	vtbl *iCoreWebView2ProcessFailedEventArgsVtbl
}

const (
	wv2ProcessFailedBrowserExited       = 0
	wv2ProcessFailedRenderExited        = 1
	wv2ProcessFailedRenderUnresponsive  = 2
	wv2ProcessFailedFrameRenderExited   = 3
)

// ICoreWebView2AcceleratorKeyPressedEventArgs
type iCoreWebView2AcceleratorKeyPressedEventArgsVtbl struct {
	iUnknownVtbl
	GetKeyEventKind      comProc
	GetVirtualKey        comProc
	GetKeyEventLParam    comProc
	GetPhysicalKeyStatus comProc
	GetHandled           comProc
	PutHandled           comProc
}

type iCoreWebView2AcceleratorKeyPressedEventArgs struct {
	vtbl *iCoreWebView2AcceleratorKeyPressedEventArgsVtbl
}

// ============================================================================
// COM Event Callback Handlers (Singleton Vtbls)
// ============================================================================

const eNoInterface = uintptr(0x80004002)

type envCompletedHandlerVtbl struct {
	iUnknownVtbl
	Invoke comProc
}

type envCompletedHandler struct {
	vtbl *envCompletedHandlerVtbl
	mgr  *webviewManager
}

var envCompletedVtblSingleton *envCompletedHandlerVtbl

type ctrlCompletedHandlerVtbl struct {
	iUnknownVtbl
	Invoke comProc
}

type ctrlCompletedHandler struct {
	vtbl *ctrlCompletedHandlerVtbl
	surf *webviewSurface
}

var ctrlCompletedVtblSingleton *ctrlCompletedHandlerVtbl

type webMsgHandlerVtbl struct {
	iUnknownVtbl
	Invoke comProc
}

type webMsgHandler struct {
	vtbl *webMsgHandlerVtbl
	surf *webviewSurface
}

var webMsgVtblSingleton *webMsgHandlerVtbl

type navCompletedHandlerVtbl struct {
	iUnknownVtbl
	Invoke comProc
}

type navCompletedHandler struct {
	vtbl *navCompletedHandlerVtbl
	surf *webviewSurface
}

var navCompletedVtblSingleton *navCompletedHandlerVtbl

type processFailedHandlerVtbl struct {
	iUnknownVtbl
	Invoke comProc
}

type processFailedHandler struct {
	vtbl *processFailedHandlerVtbl
	surf *webviewSurface
}

var processFailedVtblSingleton *processFailedHandlerVtbl

type accelKeyHandlerVtbl struct {
	iUnknownVtbl
	Invoke comProc
}

type accelKeyHandler struct {
	vtbl *accelKeyHandlerVtbl
	surf *webviewSurface
}

var accelKeyVtblSingleton *accelKeyHandlerVtbl

var initComCallbacksOnce sync.Once

func ensureComCallbackVtbls() {
	initComCallbacksOnce.Do(func() {
		noopQI := comProc(windows.NewCallback(func(this, riid uintptr, ppv *uintptr) uintptr {
			if ppv != nil {
				*ppv = 0
			}
			return eNoInterface
		}))
		noopAddRef := comProc(windows.NewCallback(func(this uintptr) uintptr { return 1 }))
		noopRelease := comProc(windows.NewCallback(func(this uintptr) uintptr { return 1 }))
		baseUnk := iUnknownVtbl{
			QueryInterface: noopQI,
			AddRef:         noopAddRef,
			Release:        noopRelease,
		}

		envCompletedVtblSingleton = &envCompletedHandlerVtbl{
			iUnknownVtbl: baseUnk,
			Invoke: comProc(windows.NewCallback(func(this *envCompletedHandler, errCode uintptr, env *iCoreWebView2Environment) uintptr {
				if this != nil && this.mgr != nil {
					this.mgr.onEnvironmentReady(errCode, env)
				}
				return 0
			})),
		}

		ctrlCompletedVtblSingleton = &ctrlCompletedHandlerVtbl{
			iUnknownVtbl: baseUnk,
			Invoke: comProc(windows.NewCallback(func(this *ctrlCompletedHandler, errCode uintptr, ctrl *iCoreWebView2Controller) uintptr {
				if this != nil && this.surf != nil {
					this.surf.onControllerReady(errCode, ctrl)
				}
				return 0
			})),
		}

		webMsgVtblSingleton = &webMsgHandlerVtbl{
			iUnknownVtbl: baseUnk,
			Invoke: comProc(windows.NewCallback(func(this *webMsgHandler, sender *iCoreWebView2, args *iCoreWebView2WebMessageReceivedEventArgs) uintptr {
				if this != nil && this.surf != nil {
					this.surf.onWebMessageReceived(args)
				}
				return 0
			})),
		}

		navCompletedVtblSingleton = &navCompletedHandlerVtbl{
			iUnknownVtbl: baseUnk,
			Invoke: comProc(windows.NewCallback(func(this *navCompletedHandler, sender *iCoreWebView2, args *iCoreWebView2NavigationCompletedEventArgs) uintptr {
				if this != nil && this.surf != nil {
					this.surf.onNavigationCompleted(args)
				}
				return 0
			})),
		}

		processFailedVtblSingleton = &processFailedHandlerVtbl{
			iUnknownVtbl: baseUnk,
			Invoke: comProc(windows.NewCallback(func(this *processFailedHandler, sender *iCoreWebView2, args *iCoreWebView2ProcessFailedEventArgs) uintptr {
				if this != nil && this.surf != nil {
					this.surf.onProcessFailed(args)
				}
				return 0
			})),
		}

		accelKeyVtblSingleton = &accelKeyHandlerVtbl{
			iUnknownVtbl: baseUnk,
			Invoke: comProc(windows.NewCallback(func(this *accelKeyHandler, sender *iCoreWebView2Controller, args *iCoreWebView2AcceleratorKeyPressedEventArgs) uintptr {
				if this != nil && this.surf != nil {
					this.surf.onAcceleratorKey(args)
				}
				return 0
			})),
		}
	})
}

// ============================================================================
// Shared WebView2 Environment & Per-Window Surface Host
// ============================================================================

type webviewManager struct {
	env        *iCoreWebView2Environment
	envHandler *envCompletedHandler
	envFailed  bool
	pending    []*webviewSurface
	surfaces   []*webviewSurface
}

func isWebView2RuntimeInstalled() bool {
	ver, err := webviewloader.GetInstalledVersion()
	return err == nil && strings.TrimSpace(ver) != ""
}

func newWebviewManager() (*webviewManager, error) {
	if !isWebView2RuntimeInstalled() {
		return nil, fmt.Errorf("Edge WebView2 Evergreen Runtime is not installed")
	}
	ensureComCallbackVtbls()

	userDataDir := resolveWebView2UserDataDir()
	_ = os.MkdirAll(userDataDir, 0o700)
	userDataUTF16, err := windows.UTF16PtrFromString(userDataDir)
	if err != nil {
		return nil, err
	}

	mgr := &webviewManager{}
	mgr.envHandler = &envCompletedHandler{
		vtbl: envCompletedVtblSingleton,
		mgr:  mgr,
	}

	hr, callErr := webviewloader.CreateCoreWebView2EnvironmentWithOptions(
		nil,
		userDataUTF16,
		0,
		uintptr(unsafe.Pointer(mgr.envHandler)),
	)
	if callErr != nil {
		return nil, callErr
	}
	if int32(hr) < 0 {
		return nil, fmt.Errorf("CreateCoreWebView2EnvironmentWithOptions failed: HRESULT 0x%08X", uint32(hr))
	}
	return mgr, nil
}

func resolveWebView2UserDataDir() string {
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		return filepath.Join(localAppData, "ColabHive", "WebView2")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".colabhive", "webview2")
	}
	return filepath.Join(os.TempDir(), "ColabHive-WebView2")
}

func (m *webviewManager) isFailed() bool {
	return m == nil || m.envFailed
}

func (m *webviewManager) onEnvironmentReady(errCode uintptr, env *iCoreWebView2Environment) {
	if int32(errCode) < 0 || env == nil {
		m.envFailed = true
		m.pending = nil
		for _, s := range m.surfaces {
			if s != nil {
				s.markFailed()
			}
		}
		return
	}
	_, _, _ = env.vtbl.AddRef.Call(uintptr(unsafe.Pointer(env)))
	m.env = env

	pending := m.pending
	m.pending = nil
	for _, s := range pending {
		m.startController(s)
	}
}

func (m *webviewManager) attachSurface(s *webviewSurface) {
	m.surfaces = append(m.surfaces, s)
	if m.envFailed {
		if s != nil {
			s.markFailed()
		}
		return
	}
	if m.env != nil {
		m.startController(s)
		return
	}
	m.pending = append(m.pending, s)
}

func (m *webviewManager) startController(s *webviewSurface) {
	if m.env == nil || s == nil || s.hwnd == 0 {
		return
	}
	hr, _, _ := m.env.vtbl.CreateCoreWebView2Controller.Call(
		uintptr(unsafe.Pointer(m.env)),
		s.hwnd,
		uintptr(unsafe.Pointer(s.ctrlHandler)),
	)
	if int32(hr) < 0 {
		s.markFailed()
	}
}

func (m *webviewManager) closeAll() {
	for _, s := range m.surfaces {
		s.close()
	}
	if m.env != nil {
		_, _, _ = m.env.vtbl.Release.Call(uintptr(unsafe.Pointer(m.env)))
		m.env = nil
	}
}

type webviewSurface struct {
	hwnd             uintptr
	isTray           bool
	initialURL       string
	pendingRoute     string
	visible          bool
	ready            bool
	failed           bool
	navigated        bool
	navFailed        bool
	navRetries       int
	recoverCount     int
	lastRecoverAt    time.Time
	autostart        bool

	ctrl          *iCoreWebView2Controller
	webview       *iCoreWebView2
	ctrlHandler   *ctrlCompletedHandler
	msgHandler    *webMsgHandler
	navHandler    *navCompletedHandler
	procHandler   *processFailedHandler
	keyHandler    *accelKeyHandler

	onMessage     func(raw string)
	onEscape      func()
	onFail        func()
	onNavRetryReq func(delay time.Duration)
}

func newWebviewSurface(
	hwnd uintptr,
	initialURL string,
	isTray bool,
	initialVisible bool,
	autostart bool,
	onMessage func(raw string),
	onEscape func(),
	onFail func(),
	onNavRetryReq func(delay time.Duration),
) *webviewSurface {
	ensureComCallbackVtbls()
	s := &webviewSurface{
		hwnd:          hwnd,
		isTray:        isTray,
		initialURL:    initialURL,
		visible:       initialVisible,
		autostart:     autostart,
		onMessage:     onMessage,
		onEscape:      onEscape,
		onFail:        onFail,
		onNavRetryReq: onNavRetryReq,
	}
	s.ctrlHandler = &ctrlCompletedHandler{vtbl: ctrlCompletedVtblSingleton, surf: s}
	s.msgHandler = &webMsgHandler{vtbl: webMsgVtblSingleton, surf: s}
	s.navHandler = &navCompletedHandler{vtbl: navCompletedVtblSingleton, surf: s}
	s.procHandler = &processFailedHandler{vtbl: processFailedVtblSingleton, surf: s}
	s.keyHandler = &accelKeyHandler{vtbl: accelKeyVtblSingleton, surf: s}
	return s
}

func (s *webviewSurface) isFailed() bool {
	return s == nil || s.failed
}

func (s *webviewSurface) markFailed() {
	if s == nil || s.failed {
		return
	}
	s.failed = true
	if s.onFail != nil {
		s.onFail()
	}
}

func (s *webviewSurface) onControllerReady(errCode uintptr, ctrl *iCoreWebView2Controller) {
	if int32(errCode) < 0 || ctrl == nil {
		s.markFailed()
		return
	}
	_, _, _ = ctrl.vtbl.AddRef.Call(uintptr(unsafe.Pointer(ctrl)))
	s.ctrl = ctrl

	// Paint Warm Stone `#F7F7F5` before HTML loads so neither Main Window nor `/tray` flashes pure white.
	var ctrl2 *iCoreWebView2Controller2
	hr, _, _ := ctrl.vtbl.QueryInterface.Call(
		uintptr(unsafe.Pointer(ctrl)),
		uintptr(unsafe.Pointer(&iidICoreWebView2Controller2)),
		uintptr(unsafe.Pointer(&ctrl2)),
	)
	if int32(hr) >= 0 && ctrl2 != nil {
		warmStone := coreWebView2Color{A: 255, R: 247, G: 247, B: 245}
		packedColor := *(*uint32)(unsafe.Pointer(&warmStone))
		_, _, _ = ctrl2.vtbl.PutDefaultBackgroundColor.Call(
			uintptr(unsafe.Pointer(ctrl2)),
			uintptr(packedColor),
		)
		_, _, _ = ctrl2.vtbl.Release.Call(uintptr(unsafe.Pointer(ctrl2)))
	}

	var wv *iCoreWebView2
	wvHr, _, _ := ctrl.vtbl.GetCoreWebView2.Call(
		uintptr(unsafe.Pointer(ctrl)),
		uintptr(unsafe.Pointer(&wv)),
	)
	if int32(wvHr) < 0 || wv == nil {
		_, _, _ = ctrl.vtbl.Close.Call(uintptr(unsafe.Pointer(ctrl)))
		_, _, _ = ctrl.vtbl.Release.Call(uintptr(unsafe.Pointer(ctrl)))
		s.ctrl = nil
		s.markFailed()
		return
	}
	// COM [out, retval] GetCoreWebView2 already returns an AddRef'd interface pointer;
	// do not call AddRef a second time (s.close() releases it once).
	s.webview = wv

	// Configure native desktop settings
	var settings *iCoreWebView2Settings
	if shr, _, _ := wv.vtbl.GetSettings.Call(uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(&settings))); int32(shr) >= 0 && settings != nil {
		_, _, _ = settings.vtbl.PutIsScriptEnabled.Call(uintptr(unsafe.Pointer(settings)), 1)
		_, _, _ = settings.vtbl.PutIsWebMessageEnabled.Call(uintptr(unsafe.Pointer(settings)), 1)
		_, _, _ = settings.vtbl.PutIsStatusBarEnabled.Call(uintptr(unsafe.Pointer(settings)), 0)
		if s.isTray {
			_, _, _ = settings.vtbl.PutAreDefaultContextMenusEnabled.Call(uintptr(unsafe.Pointer(settings)), 0)
			_, _, _ = settings.vtbl.PutIsZoomControlEnabled.Call(uintptr(unsafe.Pointer(settings)), 0)
		}
		_, _, _ = settings.vtbl.Release.Call(uintptr(unsafe.Pointer(settings)))
	}

	var tok eventRegistrationToken
	_, _, _ = wv.vtbl.AddWebMessageReceived.Call(
		uintptr(unsafe.Pointer(wv)),
		uintptr(unsafe.Pointer(s.msgHandler)),
		uintptr(unsafe.Pointer(&tok)),
	)
	_, _, _ = wv.vtbl.AddNavigationCompleted.Call(
		uintptr(unsafe.Pointer(wv)),
		uintptr(unsafe.Pointer(s.navHandler)),
		uintptr(unsafe.Pointer(&tok)),
	)
	_, _, _ = wv.vtbl.AddProcessFailed.Call(
		uintptr(unsafe.Pointer(wv)),
		uintptr(unsafe.Pointer(s.procHandler)),
		uintptr(unsafe.Pointer(&tok)),
	)
	_, _, _ = ctrl.vtbl.AddAcceleratorKeyPressed.Call(
		uintptr(unsafe.Pointer(ctrl)),
		uintptr(unsafe.Pointer(s.keyHandler)),
		uintptr(unsafe.Pointer(&tok)),
	)

	// Inject bridge bootstrap before first document loads so `/tray` and Main Window
	// have `window.__COLABHIVE_DESKTOP__`, external link routing, and attached-daemon state parity!
	s.injectBootstrapScript()

	s.ready = true
	s.resizeToClientRect()
	s.setVisible(s.visible)

	target := s.initialURL
	if s.pendingRoute != "" {
		target = joinBaseAndRoute(s.initialURL, s.pendingRoute)
		s.pendingRoute = ""
	}
	s.navigateRaw(target)
}

func (s *webviewSurface) injectBootstrapScript() {
	if s.webview == nil {
		return
	}
	script := BuildNativeWebViewBootstrapJS("windows", s.autostart, s.isTray)
	ptr, err := windows.UTF16PtrFromString(script)
	if err != nil {
		return
	}
	_, _, _ = s.webview.vtbl.AddScriptToExecuteOnDocumentCreated.Call(
		uintptr(unsafe.Pointer(s.webview)),
		uintptr(unsafe.Pointer(ptr)),
		0,
	)
}

func buildWebViewBootstrapJS(autostart bool) string {
	return BuildNativeWebViewBootstrapJS("windows", autostart, false)
}

func (s *webviewSurface) onWebMessageReceived(args *iCoreWebView2WebMessageReceivedEventArgs) {
	if args == nil || s.onMessage == nil {
		return
	}
	// Prefer GetWebMessageAsJSON so JS object payloads (`{type:'colabhive:desktop-action',...}`) decode cleanly!
	var jsonPtr *uint16
	hr, _, _ := args.vtbl.GetWebMessageAsJSON.Call(
		uintptr(unsafe.Pointer(args)),
		uintptr(unsafe.Pointer(&jsonPtr)),
	)
	if int32(hr) >= 0 && jsonPtr != nil {
		raw := windows.UTF16PtrToString(jsonPtr)
		windows.CoTaskMemFree(unsafe.Pointer(jsonPtr))
		s.onMessage(raw)
		return
	}

	var strPtr *uint16
	hr2, _, _ := args.vtbl.TryGetWebMessageAsString.Call(
		uintptr(unsafe.Pointer(args)),
		uintptr(unsafe.Pointer(&strPtr)),
	)
	if int32(hr2) >= 0 && strPtr != nil {
		raw := windows.UTF16PtrToString(strPtr)
		windows.CoTaskMemFree(unsafe.Pointer(strPtr))
		s.onMessage(raw)
	}
}

func (s *webviewSurface) onNavigationCompleted(args *iCoreWebView2NavigationCompletedEventArgs) {
	isSuccess := int32(1)
	if args != nil {
		hr, _, _ := args.vtbl.GetIsSuccess.Call(
			uintptr(unsafe.Pointer(args)),
			uintptr(unsafe.Pointer(&isSuccess)),
		)
		if int32(hr) < 0 {
			isSuccess = 1
		}
	}
	if isSuccess == 0 {
		s.navFailed = true
		if !s.navigated && s.onNavRetryReq != nil && s.navRetries < 3 {
			s.navRetries++
			backoff := time.Duration(s.navRetries*700) * time.Millisecond
			s.onNavRetryReq(backoff)
		}
		return
	}
	s.navigated = true
	s.navFailed = false
	s.navRetries = 0
	s.syncAutostartState(s.autostart)
	if !s.isTray && s.hwnd != 0 {
		s.syncWindowState(isWindowMaximized(s.hwnd))
	}
	if s.pendingRoute != "" {
		r := s.pendingRoute
		s.pendingRoute = ""
		s.openSPARoute(r)
	}
}

func (s *webviewSurface) onProcessFailed(args *iCoreWebView2ProcessFailedEventArgs) {
	kind := uint32(wv2ProcessFailedRenderExited)
	if args != nil {
		_, _, _ = args.vtbl.GetProcessFailedKind.Call(
			uintptr(unsafe.Pointer(args)),
			uintptr(unsafe.Pointer(&kind)),
		)
	}
	if kind == wv2ProcessFailedBrowserExited {
		s.markFailed()
		return
	}
	if kind == wv2ProcessFailedRenderExited ||
		kind == wv2ProcessFailedRenderUnresponsive ||
		kind == wv2ProcessFailedFrameRenderExited {
		s.navigated = false
		s.navFailed = true
		now := time.Now()
		if s.recoverCount < 4 && (s.lastRecoverAt.IsZero() || now.Sub(s.lastRecoverAt) >= 1500*time.Millisecond) {
			s.recoverCount++
			s.lastRecoverAt = now
			if s.ready && s.webview != nil {
				target := s.initialURL
				if s.pendingRoute != "" {
					target = joinBaseAndRoute(s.initialURL, s.pendingRoute)
				}
				s.navigateRaw(target)
			}
		}
	}
}

func (s *webviewSurface) retryInitialNavigation() {
	if s == nil || s.failed || !s.ready || s.webview == nil || s.navigated {
		return
	}
	target := s.initialURL
	if s.pendingRoute != "" {
		target = joinBaseAndRoute(s.initialURL, s.pendingRoute)
	}
	s.navigateRaw(target)
}

func (s *webviewSurface) syncWindowState(maximized bool) {
	if s == nil || s.isTray || !s.ready || s.webview == nil {
		return
	}
	lit := "false"
	if maximized {
		lit = "true"
	}
	s.evalJS(fmt.Sprintf(`(function(m){
  if (window.__COLABHIVE_WINDOW_CONTROLS__) {
    window.__COLABHIVE_WINDOW_CONTROLS__.maximized = m;
  }
  window.dispatchEvent(new CustomEvent('colabhive:window-state', {
    detail: { maximized: m, platform: 'windows', frameless: true }
  }));
})(%s);`, lit))
}

func (s *webviewSurface) onAcceleratorKey(args *iCoreWebView2AcceleratorKeyPressedEventArgs) {
	if args == nil || s.onEscape == nil {
		return
	}
	var kind uint32
	_, _, _ = args.vtbl.GetKeyEventKind.Call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&kind)))
	if kind != 0 { // COREWEBVIEW2_KEY_EVENT_KIND_KEY_DOWN == 0
		return
	}
	var vk uint32
	_, _, _ = args.vtbl.GetVirtualKey.Call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&vk)))
	if vk == 0x1B { // VK_ESCAPE
		var status coreWebView2PhysicalKeyStatus
		_, _, _ = args.vtbl.GetPhysicalKeyStatus.Call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&status)))
		if status.WasKeyDown == 0 {
			_, _, _ = args.vtbl.PutHandled.Call(uintptr(unsafe.Pointer(args)), 1)
			s.onEscape()
		}
	}
}

func (s *webviewSurface) resizeToClientRect() {
	if s.ctrl == nil || s.hwnd == 0 {
		return
	}
	var rc winRect
	_, _, _ = procGetClientRect.Call(s.hwnd, uintptr(unsafe.Pointer(&rc)))
	if rc.Right <= rc.Left || rc.Bottom <= rc.Top {
		return
	}
	s.putBounds(rc)
}

func (s *webviewSurface) putBounds(rc winRect) {
	if s.ctrl == nil {
		return
	}
	switch runtime.GOARCH {
	case "arm64":
		words := (*[2]uintptr)(unsafe.Pointer(&rc))
		_, _, _ = s.ctrl.vtbl.PutBounds.Call(uintptr(unsafe.Pointer(s.ctrl)), words[0], words[1])
	case "386":
		_, _, _ = s.ctrl.vtbl.PutBounds.Call(
			uintptr(unsafe.Pointer(s.ctrl)),
			uintptr(rc.Left),
			uintptr(rc.Top),
			uintptr(rc.Right),
			uintptr(rc.Bottom),
		)
	default: // amd64 x64 ABI passes >8-byte structs by pointer
		_, _, _ = s.ctrl.vtbl.PutBounds.Call(uintptr(unsafe.Pointer(s.ctrl)), uintptr(unsafe.Pointer(&rc)))
	}
}

func (s *webviewSurface) notifyPositionChanged() {
	if s.ctrl == nil {
		return
	}
	_, _, _ = s.ctrl.vtbl.NotifyParentWindowPositionChanged.Call(uintptr(unsafe.Pointer(s.ctrl)))
}

func (s *webviewSurface) setVisible(vis bool) {
	s.visible = vis
	if s.ctrl == nil {
		return
	}
	v := uintptr(0)
	if vis {
		v = 1
	}
	_, _, _ = s.ctrl.vtbl.PutIsVisible.Call(uintptr(unsafe.Pointer(s.ctrl)), v)
}

func (s *webviewSurface) focus() {
	if s.ctrl == nil {
		return
	}
	// COREWEBVIEW2_MOVE_FOCUS_REASON_PROGRAMMATIC == 0
	_, _, _ = s.ctrl.vtbl.MoveFocus.Call(uintptr(unsafe.Pointer(s.ctrl)), 0)
}

func (s *webviewSurface) navigateRaw(targetURL string) {
	if s.webview == nil || strings.TrimSpace(targetURL) == "" {
		return
	}
	ptr, err := windows.UTF16PtrFromString(targetURL)
	if err != nil {
		return
	}
	_, _, _ = s.webview.vtbl.Navigate.Call(uintptr(unsafe.Pointer(s.webview)), uintptr(unsafe.Pointer(ptr)))
}

func (s *webviewSurface) openSPARoute(route string) {
	cleanRoute := NormalizeRoute(route)
	if !s.ready || s.webview == nil {
		s.pendingRoute = cleanRoute
		return
	}
	if !s.navigated {
		s.pendingRoute = cleanRoute
		return
	}
	routeJSON, _ := json.Marshal(cleanRoute)
	js := fmt.Sprintf(`(function(to){
  try {
    if (window.location.pathname.indexOf('/tray') !== 0) {
      var cur = window.location.pathname + window.location.search;
      if (cur !== to) {
        window.history.pushState(null, '', to);
        window.dispatchEvent(new CustomEvent('colabhive:navigate', { detail: { to: to } }));
      }
      return;
    }
  } catch (_) {}
  window.location.href = to;
})(%s);`, string(routeJSON))
	s.evalJS(js)
}

func (s *webviewSurface) syncAutostartState(enabled bool) {
	s.autostart = enabled
	if !s.ready || s.webview == nil {
		return
	}
	lit := "false"
	if enabled {
		lit = "true"
	}
	s.evalJS(fmt.Sprintf(`(function(v){
  window.__COLABHIVE_DESKTOP__ = window.__COLABHIVE_DESKTOP__ || { shell: true };
  window.__COLABHIVE_DESKTOP__.autostart = v;
  window.dispatchEvent(new CustomEvent('colabhive:autostart-state', { detail: { autostart: v } }));
})(%s);`, lit))
}

func (s *webviewSurface) setTrayMode(mode string) {
	if s == nil || !s.isTray || !s.ready || s.webview == nil {
		return
	}
	m := "panel"
	if strings.EqualFold(strings.TrimSpace(mode), "menu") {
		m = "menu"
	}
	modeJSON, err := json.Marshal(m)
	if err != nil {
		return
	}
	s.evalJS(fmt.Sprintf(`(function(mode){
  window.__COLABHIVE_TRAY_MODE__ = mode;
  if (typeof window.__COLABHIVE_SET_TRAY_MODE__ === 'function') {
    window.__COLABHIVE_SET_TRAY_MODE__(mode);
  } else {
    window.dispatchEvent(new CustomEvent('colabhive:tray-mode', { detail: { mode: mode } }));
  }
})(%s);`, string(modeJSON)))
}

func (s *webviewSurface) evalJS(script string) {
	if s.webview == nil || strings.TrimSpace(script) == "" {
		return
	}
	ptr, err := windows.UTF16PtrFromString(script)
	if err != nil {
		return
	}
	_, _, _ = s.webview.vtbl.ExecuteScript.Call(
		uintptr(unsafe.Pointer(s.webview)),
		uintptr(unsafe.Pointer(ptr)),
		0,
	)
}

func (s *webviewSurface) ackWebViewAction(reqID string, state server.DesktopState, execErr error) {
	if s == nil || s.webview == nil || strings.TrimSpace(reqID) == "" {
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
	s.evalJS(js)
}

func (s *webviewSurface) close() {
	if s.ctrl != nil {
		_, _, _ = s.ctrl.vtbl.Close.Call(uintptr(unsafe.Pointer(s.ctrl)))
		_, _, _ = s.ctrl.vtbl.Release.Call(uintptr(unsafe.Pointer(s.ctrl)))
		s.ctrl = nil
	}
	if s.webview != nil {
		_, _, _ = s.webview.vtbl.Release.Call(uintptr(unsafe.Pointer(s.webview)))
		s.webview = nil
	}
}
