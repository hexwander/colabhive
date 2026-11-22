//go:build windows

package desktop

import (
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	modOle32    = windows.NewLazySystemDLL("ole32.dll")
	modDwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	modShcore   = windows.NewLazySystemDLL("shcore.dll")

	procSetProcessDpiAwarenessContext = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = modUser32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwareness        = modShcore.NewProc("SetProcessDpiAwareness")
	procGetDpiForMonitor              = modShcore.NewProc("GetDpiForMonitor")
	procGetDpiForWindow               = modUser32.NewProc("GetDpiForWindow")
	procGetSystemMetrics              = modUser32.NewProc("GetSystemMetrics")
	procGetSystemMetricsForDpi        = modUser32.NewProc("GetSystemMetricsForDpi")

	procRegisterClassExW       = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW        = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow          = modUser32.NewProc("DestroyWindow")
	procDefWindowProcW         = modUser32.NewProc("DefWindowProcW")
	procShowWindow             = modUser32.NewProc("ShowWindow")
	procUpdateWindow           = modUser32.NewProc("UpdateWindow")
	procSetWindowPos           = modUser32.NewProc("SetWindowPos")
	procSetForegroundWindow    = modUser32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWin  = modUser32.NewProc("AllowSetForegroundWindow")
	procGetWindowThreadProcID  = modUser32.NewProc("GetWindowThreadProcessId")
	procIsIconic               = modUser32.NewProc("IsIconic")
	procIsZoomed               = modUser32.NewProc("IsZoomed")
	procIsWindowVisible        = modUser32.NewProc("IsWindowVisible")
	procReleaseCapture         = modUser32.NewProc("ReleaseCapture")
	procFindWindowW            = modUser32.NewProc("FindWindowW")
	procPostMessageW           = modUser32.NewProc("PostMessageW")
	procSendMessageW           = modUser32.NewProc("SendMessageW")
	procRegisterWindowMessageW = modUser32.NewProc("RegisterWindowMessageW")
	procGetMessageW            = modUser32.NewProc("GetMessageW")
	procTranslateMessage       = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW       = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage        = modUser32.NewProc("PostQuitMessage")
	procLoadCursorW            = modUser32.NewProc("LoadCursorW")
	procGetClientRect          = modUser32.NewProc("GetClientRect")
	procGetWindowRect          = modUser32.NewProc("GetWindowRect")
	procGetCursorPos           = modUser32.NewProc("GetCursorPos")
	procMonitorFromPoint       = modUser32.NewProc("MonitorFromPoint")
	procMonitorFromWindow      = modUser32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW        = modUser32.NewProc("GetMonitorInfoW")
	procDestroyIcon            = modUser32.NewProc("DestroyIcon")
	procCreateIconIndirect     = modUser32.NewProc("CreateIconIndirect")

	procCreatePopupMenu    = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW        = modUser32.NewProc("AppendMenuW")
	procSetMenuDefaultItem = modUser32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu     = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu        = modUser32.NewProc("DestroyMenu")

	procOpenClipboard    = modUser32.NewProc("OpenClipboard")
	procCloseClipboard   = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard   = modUser32.NewProc("EmptyClipboard")
	procSetClipboardData = modUser32.NewProc("SetClipboardData")

	procGlobalAlloc   = modKernel32.NewProc("GlobalAlloc")
	procGlobalLock    = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock  = modKernel32.NewProc("GlobalUnlock")
	procGlobalFree    = modKernel32.NewProc("GlobalFree")
	procRtlMoveMemory = modKernel32.NewProc("RtlMoveMemory")

	procCreateDIBSection = modGdi32.NewProc("CreateDIBSection")
	procCreateBitmap     = modGdi32.NewProc("CreateBitmap")
	procCreateSolidBrush = modGdi32.NewProc("CreateSolidBrush")
	procDeleteObject     = modGdi32.NewProc("DeleteObject")

	procShellNotifyIconW       = modShell32.NewProc("Shell_NotifyIconW")
	procShellNotifyIconGetRect = modShell32.NewProc("Shell_NotifyIconGetRect")
	procShellExecuteW          = modShell32.NewProc("ShellExecuteW")

	procCoInitializeEx               = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize               = modOle32.NewProc("CoUninitialize")
	procDwmSetWindowAttr             = modDwmapi.NewProc("DwmSetWindowAttribute")
	procDwmExtendFrameIntoClientArea = modDwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

const (
	// Custom WM_APP messages
	wmAppShowMainWindow = 0x8000 + 42 // Single-instance IPC wakeup (`cmdShowMainWindow`)
	wmAppDispatchQueue  = 0x8000 + 43 // Thread-safe UI closure dispatch
	wmAppTrayCallback   = 0x8000 + 44 // Shell_NotifyIconW callback message

	// Standard Win32 Window Messages
	wmNull          = 0x0000
	wmDestroy       = 0x0002
	wmMove          = 0x0003
	wmActivate      = 0x0006
	wmSize          = 0x0005
	wmClose         = 0x0010
	wmQuit          = 0x0012
	wmGetMinMaxInfo = 0x0024
	wmSetIcon       = 0x0080
	wmNcCalcSize    = 0x0083
	wmNcHitTest     = 0x0084
	wmNcLButtonDown = 0x00A1
	wmCommand       = 0x0111
	wmMoving        = 0x0216
	wmDpiChanged    = 0x02E0

	sizeMinimized = 1

	smCxSizeFrame    = 32
	smCySizeFrame    = 33
	smCxPaddedBorder = 92

	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B
	ninSelect       = 0x0400
	ninKeySelect    = 0x0401

	waInactive = 0

	// Window Styles
	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsClipChildren     = 0x02000000

	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080

	// ShowWindow Commands
	swHide           = 0
	swShowNormal     = 1
	swMaximize       = 3
	swShowNoActivate = 4
	swShow           = 5
	swMinimize       = 6
	swRestore        = 9

	// SetWindowPos Flags
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpShowWindow   = 0x0040
	swpFrameChanged = 0x0020

	hwndTopmost = ^uintptr(0) // (HWND)-1

	// Shell_NotifyIconW Constants
	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010
	nifShowTip = 0x00000080

	niifNone    = 0x00000000
	niifNoSound = 0x00000010

	// Menu Constants
	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfChecked   = 0x00000008
	mfUnchecked = 0x00000000

	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020

	// Context Menu Command IDs
	cmdMenuOpenTray      = 1001
	cmdMenuOpenMain      = 1002
	cmdMenuOpenPool      = 1003
	cmdMenuSyncNow       = 1004
	cmdMenuCopyMCPURL    = 1005
	cmdMenuToggleStartup = 1006
	cmdMenuQuit          = 1007

	// Clipboard & Memory Constants
	cfUnicodeText = 13
	gmemMoveable  = 0x0002

	// Monitor Constants
	monitorDefaultToPrimary = 1
	monitorDefaultToNearest = 2

	// DWM Attributes (Windows 11)
	dwmwaWindowCornerPreference = 33
	dwmwaBorderColor            = 34
	dwmwcpRound                 = 2

	// Icon & Cursor Constants
	iconSmall = 0
	iconBig   = 1
	idcArrow  = 32512

	coinitApartmentThreaded = 0x2
)

type winPoint struct {
	X int32
	Y int32
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

func (r winRect) toRect() Rect {
	return Rect{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: r.Bottom}
}

type winMsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
}

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type minMaxInfo struct {
	PtReserved     winPoint
	PtMaxSize      winPoint
	PtMaxPosition  winPoint
	PtMinTrackSize winPoint
	PtMaxTrackSize winPoint
}

type dwmMargins struct {
	CxLeftWidth    int32
	CxRightWidth   int32
	CyTopHeight    int32
	CyBottomHeight int32
}

type ncCalcSizeParams struct {
	Rgrc  [3]winRect
	Lppos uintptr
}

type monitorInfo struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
}

type notifyIconDataW struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type notifyIconIdentifier struct {
	CbSize   uint32
	HWnd     uintptr
	UID      uint32
	GuidItem windows.GUID
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

func enablePerMonitorDPIV2() {
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == (DPI_AWARENESS_CONTEXT)-4
	if err := procSetProcessDpiAwarenessContext.Find(); err == nil {
		r, _, _ := procSetProcessDpiAwarenessContext.Call(^uintptr(3))
		if r != 0 {
			return
		}
	}
	if err := procSetProcessDpiAwareness.Find(); err == nil {
		// PROCESS_PER_MONITOR_DPI_AWARE == 2
		r, _, _ := procSetProcessDpiAwareness.Call(2)
		if r == 0 {
			return
		}
	}
	if err := procSetProcessDPIAware.Find(); err == nil {
		_, _, _ = procSetProcessDPIAware.Call()
	}
}

func createBrandHIcon(size int) uintptr {
	bgra, mask := BuildIconAndMaskBits(size)
	if len(bgra) == 0 || len(mask) == 0 {
		return 0
	}

	bmi := bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       int32(size),
		BiHeight:      -int32(size), // Top-down DIB
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: 0, // BI_RGB
		BiSizeImage:   uint32(len(bgra)),
	}

	var ppvBits unsafe.Pointer
	hbmColor, _, _ := procCreateDIBSection.Call(
		0,
		uintptr(unsafe.Pointer(&bmi)),
		0,
		uintptr(unsafe.Pointer(&ppvBits)),
		0,
		0,
	)
	if hbmColor == 0 || ppvBits == nil {
		return 0
	}
	defer procDeleteObject.Call(hbmColor)

	dst := unsafe.Slice((*byte)(ppvBits), len(bgra))
	copy(dst, bgra)

	hbmMask, _, _ := procCreateBitmap.Call(
		uintptr(size),
		uintptr(size),
		1,
		1,
		uintptr(unsafe.Pointer(&mask[0])),
	)
	if hbmMask == 0 {
		return 0
	}
	defer procDeleteObject.Call(hbmMask)

	ii := iconInfo{
		FIcon:    1,
		HbmMask:  hbmMask,
		HbmColor: hbmColor,
	}
	hIcon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return hIcon
}

func applyWin11WindowChrome(hwnd uintptr, subtleFlyoutBorder bool) {
	if hwnd == 0 || procDwmSetWindowAttr.Find() != nil {
		return
	}
	cornerPref := uint32(dwmwcpRound)
	_, _, _ = procDwmSetWindowAttr.Call(
		hwnd,
		dwmwaWindowCornerPreference,
		uintptr(unsafe.Pointer(&cornerPref)),
		unsafe.Sizeof(cornerPref),
	)
	if subtleFlyoutBorder {
		// COLORREF is 0x00BBGGRR: crisp Warm-Stone border `#DCDCD8` (R:220, G:220, B:216 -> 0x00D8DCDC)
		borderColor := uint32(0x00D8DCDC)
		_, _, _ = procDwmSetWindowAttr.Call(
			hwnd,
			dwmwaBorderColor,
			uintptr(unsafe.Pointer(&borderColor)),
			unsafe.Sizeof(borderColor),
		)
	}
}

func applyFramelessMainWindowChrome(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	applyWin11WindowChrome(hwnd, false)
	if procDwmExtendFrameIntoClientArea.Find() == nil {
		// Extend DWM frame 1px into client area so Windows 11/10 renders native drop shadow & rounded corners
		// while `WM_NCCALCSIZE` removes 100% of the ugly default system caption bar!
		margins := dwmMargins{
			CxLeftWidth:    0,
			CxRightWidth:   0,
			CyTopHeight:    1,
			CyBottomHeight: 0,
		}
		_, _, _ = procDwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
	}
	_, _, _ = procSetWindowPos.Call(
		hwnd,
		0,
		0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged,
	)
}

func isWindowMaximized(hwnd uintptr) bool {
	if hwnd == 0 || procIsZoomed.Find() != nil {
		return false
	}
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

func getSystemMetricsForDPI(index int32, dpi uint32) int32 {
	if dpi == 0 {
		dpi = 96
	}
	if procGetSystemMetricsForDpi.Find() == nil {
		v, _, _ := procGetSystemMetricsForDpi.Call(uintptr(index), uintptr(dpi))
		return int32(v)
	}
	if procGetSystemMetrics.Find() == nil {
		v, _, _ := procGetSystemMetrics.Call(uintptr(index))
		return ScaleForDPI(int(int32(v)), dpi)
	}
	return 0
}

func queryMaximizedFrameThickness(hwnd uintptr) (frameX, frameY int32) {
	dpi := queryWindowDPI(hwnd)
	padded := getSystemMetricsForDPI(smCxPaddedBorder, dpi)
	frameX = getSystemMetricsForDPI(smCxSizeFrame, dpi) + padded
	frameY = getSystemMetricsForDPI(smCySizeFrame, dpi) + padded
	return frameX, frameY
}

func queryMonitorForWindow(hwnd uintptr) (monBounds Rect, workArea Rect, dpi uint32) {
	if hwnd != 0 && procMonitorFromWindow.Find() == nil {
		hMon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
		if hMon != 0 {
			dpi = queryMonitorDPI(hMon, hwnd)
			mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
			ok, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi)))
			if ok != 0 {
				return mi.RcMonitor.toRect(), mi.RcWork.toRect(), dpi
			}
		}
	}
	return queryPrimaryMonitorWorkArea()
}

func queryWindowDPI(hwnd uintptr) uint32 {
	if hwnd != 0 && procGetDpiForWindow.Find() == nil {
		dpi, _, _ := procGetDpiForWindow.Call(hwnd)
		if dpi >= 72 && dpi <= 576 {
			return uint32(dpi)
		}
	}
	return 96
}

func queryMonitorDPI(hMon uintptr, fallbackHwnd uintptr) uint32 {
	if hMon != 0 && procGetDpiForMonitor.Find() == nil {
		var dpiX, dpiY uint32
		// MDT_EFFECTIVE_DPI == 0
		hr, _, _ := procGetDpiForMonitor.Call(
			hMon,
			0,
			uintptr(unsafe.Pointer(&dpiX)),
			uintptr(unsafe.Pointer(&dpiY)),
		)
		if hr == 0 && dpiX >= 72 && dpiX <= 576 {
			return dpiX
		}
	}
	return queryWindowDPI(fallbackHwnd)
}

func callMonitorFromPoint(pt winPoint, flags uintptr) uintptr {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		packedPt := *(*uint64)(unsafe.Pointer(&pt))
		hMon, _, _ := procMonitorFromPoint.Call(uintptr(packedPt), flags)
		return hMon
	}
	hMon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(pt.X)), uintptr(uint32(pt.Y)), flags)
	return hMon
}

func queryPrimaryMonitorWorkArea() (monBounds Rect, workArea Rect, dpi uint32) {
	hMon := callMonitorFromPoint(winPoint{X: 0, Y: 0}, monitorDefaultToPrimary)
	dpi = queryMonitorDPI(hMon, 0)
	if hMon != 0 {
		mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
		ok, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi)))
		if ok != 0 {
			return mi.RcMonitor.toRect(), mi.RcWork.toRect(), dpi
		}
	}
	return Rect{0, 0, 1920, 1080}, Rect{0, 0, 1920, 1032}, dpi
}

func queryMonitorAtPoint(p Point, fallbackHwnd uintptr) (monBounds Rect, workArea Rect, dpi uint32) {
	hMon := callMonitorFromPoint(winPoint{X: p.X, Y: p.Y}, monitorDefaultToNearest)
	dpi = queryMonitorDPI(hMon, fallbackHwnd)
	if hMon != 0 {
		mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
		ok, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi)))
		if ok != 0 {
			return mi.RcMonitor.toRect(), mi.RcWork.toRect(), dpi
		}
	}
	return queryPrimaryMonitorWorkArea()
}

func copyUTF16Buffer(dst []uint16, text string) {
	for i := range dst {
		dst[i] = 0
	}
	if len(dst) <= 1 || text == "" {
		return
	}
	safe := TruncateUTF16Units(text, len(dst)-1)
	u16, err := windows.UTF16FromString(safe)
	if err != nil {
		return
	}
	copy(dst, u16)
	dst[len(dst)-1] = 0
}

func writeClipboardUnicodeText(ownerHwnd uintptr, text string) error {
	u16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	byteLen := uintptr(len(u16) * 2)

	hMem, _, allocErr := procGlobalAlloc.Call(gmemMoveable, byteLen)
	if hMem == 0 {
		return allocErr
	}

	ptr, _, lockErr := procGlobalLock.Call(hMem)
	if ptr == 0 {
		_, _, _ = procGlobalFree.Call(hMem)
		return lockErr
	}
	if len(u16) > 0 {
		_, _, _ = procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&u16[0])), byteLen)
	}
	_, _, _ = procGlobalUnlock.Call(hMem)

	opened, _, openErr := procOpenClipboard.Call(ownerHwnd)
	if opened == 0 {
		_, _, _ = procGlobalFree.Call(hMem)
		return openErr
	}
	defer procCloseClipboard.Call()

	_, _, _ = procEmptyClipboard.Call()
	res, _, setErr := procSetClipboardData.Call(cfUnicodeText, hMem)
	if res == 0 {
		_, _, _ = procGlobalFree.Call(hMem)
		return setErr
	}
	// Once SetClipboardData succeeds, the system owns hMem; do NOT call GlobalFree.
	return nil
}

func openExternalBrowserURL(rawURL string) {
	u := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(strings.ToLower(u), "http://") && !strings.HasPrefix(strings.ToLower(u), "https://") {
		return
	}
	verb, _ := windows.UTF16PtrFromString("open")
	target, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return
	}
	_, _, _ = procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0,
		0,
		swShowNormal,
	)
}

// launchAppModeBrowserFallback opens Edge or Chrome in chromeless `--app=<url>` mode ONLY when
// the Edge WebView2 Evergreen Runtime is missing on the machine.
func launchAppModeBrowserFallback(targetURL string) {
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, bin := range candidates {
		if isRegularFile(bin) {
			cmd := exec.Command(bin, "--app="+targetURL)
			if cmd.Start() == nil {
				return
			}
		}
	}
	openExternalBrowserURL(targetURL)
}
