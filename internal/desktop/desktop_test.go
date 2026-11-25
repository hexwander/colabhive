package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/server"
)

func TestComputeFlyoutPositionTaskbarEdgesAndDPI(t *testing.T) {
	t.Run("bottom taskbar 100pct DPI centered and clamped", func(t *testing.T) {
		mon := Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
		work := Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1032} // 48px bottom taskbar
		anchor := Rect{Left: 1700, Top: 1036, Right: 1724, Bottom: 1060}

		got := ComputeFlyoutPosition(FlyoutLayoutInput{
			DPI:           96,
			AnchorRect:    &anchor,
			Cursor:        Point{X: 1712, Y: 1048},
			MonitorBounds: mon,
			WorkArea:      work,
		})

		if got.DockEdge != EdgeBottom {
			t.Fatalf("expected EdgeBottom, got %s", got.DockEdge)
		}
		if got.Width != 396 || got.Height != 580 {
			t.Fatalf("expected 396x580, got %dx%d", got.Width, got.Height)
		}
		if got.Y != 444 {
			t.Fatalf("expected Y=444, got %d", got.Y)
		}
		if got.X != 1514 {
			t.Fatalf("expected X=1514, got %d", got.X)
		}

		farRight := Rect{Left: 1896, Top: 1036, Right: 1916, Bottom: 1060}
		clamped := ComputeFlyoutPosition(FlyoutLayoutInput{
			DPI:           96,
			AnchorRect:    &farRight,
			MonitorBounds: mon,
			WorkArea:      work,
		})
		if clamped.X != 1516 {
			t.Fatalf("expected right-clamped X=1516, got %d", clamped.X)
		}
	})

	t.Run("top taskbar 125pct DPI", func(t *testing.T) {
		mon := Rect{Left: 0, Top: 0, Right: 2400, Bottom: 1350}
		work := Rect{Left: 0, Top: 60, Right: 2400, Bottom: 1350}
		anchor := Rect{Left: 2100, Top: 10, Right: 2130, Bottom: 40}

		got := ComputeFlyoutPosition(FlyoutLayoutInput{
			DPI:           120,
			AnchorRect:    &anchor,
			MonitorBounds: mon,
			WorkArea:      work,
		})

		if got.DockEdge != EdgeTop {
			t.Fatalf("expected EdgeTop, got %s", got.DockEdge)
		}
		if got.Width != 495 || got.Height != 725 {
			t.Fatalf("expected 495x725 at 120 DPI, got %dx%d", got.Width, got.Height)
		}
		if got.Y != 70 {
			t.Fatalf("expected Y=70 below top taskbar, got %d", got.Y)
		}
	})

	t.Run("left and right vertical taskbars 150pct DPI", func(t *testing.T) {
		mon := Rect{Left: 0, Top: 0, Right: 2880, Bottom: 1800}
		leftWork := Rect{Left: 96, Top: 0, Right: 2880, Bottom: 1800}

		gotLeft := ComputeFlyoutPosition(FlyoutLayoutInput{
			DPI:           144,
			Cursor:        Point{X: 40, Y: 1500},
			MonitorBounds: mon,
			WorkArea:      leftWork,
		})
		if gotLeft.DockEdge != EdgeLeft {
			t.Fatalf("expected EdgeLeft, got %s", gotLeft.DockEdge)
		}
		if gotLeft.Width != 594 || gotLeft.Height != 870 {
			t.Fatalf("expected 594x870 at 144 DPI, got %dx%d", gotLeft.Width, gotLeft.Height)
		}
		if gotLeft.X != 96+12 {
			t.Fatalf("expected X=108 next to left taskbar, got %d", gotLeft.X)
		}
		if gotLeft.Y != 918 {
			t.Fatalf("expected bottom-clamped Y=918, got %d", gotLeft.Y)
		}

		rightWork := Rect{Left: 0, Top: 0, Right: 2784, Bottom: 1800}
		gotRight := ComputeFlyoutPosition(FlyoutLayoutInput{
			DPI:           144,
			Cursor:        Point{X: 2830, Y: 900},
			MonitorBounds: mon,
			WorkArea:      rightWork,
		})
		if gotRight.DockEdge != EdgeRight {
			t.Fatalf("expected EdgeRight, got %s", gotRight.DockEdge)
		}
		if gotRight.X != 2784-594-12 {
			t.Fatalf("expected X=%d next to right taskbar, got %d", 2784-594-12, gotRight.X)
		}
	})

	t.Run("200pct 4K secondary monitor with negative coordinates", func(t *testing.T) {
		mon := Rect{Left: -3840, Top: 0, Right: 0, Bottom: 2160}
		work := Rect{Left: -3840, Top: 0, Right: 0, Bottom: 2064}
		anchor := Rect{Left: -320, Top: 2080, Right: -272, Bottom: 2128}

		got := ComputeFlyoutPosition(FlyoutLayoutInput{
			DPI:           192,
			AnchorRect:    &anchor,
			MonitorBounds: mon,
			WorkArea:      work,
		})
		if got.Width != 792 || got.Height != 1160 {
			t.Fatalf("expected 792x1160 at 192 DPI, got %dx%d", got.Width, got.Height)
		}
		if got.X < work.Left || got.X+got.Width > work.Right {
			t.Fatalf("flyout X=%d..%d escaped negative secondary monitor %d..%d", got.X, got.X+got.Width, work.Left, work.Right)
		}
		if got.Y != 2064-1160-16 {
			t.Fatalf("expected Y=%d, got %d", 2064-1160-16, got.Y)
		}
	})

	t.Run("compact laptop screen clamps main window cleanly", func(t *testing.T) {
		work := Rect{Left: 0, Top: 0, Right: 1280, Bottom: 680}
		rc := ComputeCenteredWindowRect(1380, 880, 96, work)
		if rc.Left < work.Left || rc.Right > work.Right || rc.Top < work.Top || rc.Bottom > work.Bottom {
			t.Fatalf("main window rect %+v overflowed compact workArea %+v", rc, work)
		}
	})
}

func TestFramelessHitTestAndMaximizedInset(t *testing.T) {
	bounds := Rect{Left: 100, Top: 200, Right: 1480, Bottom: 1080}

	cases := []struct {
		name string
		pt   Point
		want uintptr
	}{
		{"top-left corner", Point{102, 203}, HTTopLeft},
		{"top-right corner", Point{1477, 202}, HTTopRight},
		{"bottom-left corner", Point{103, 1078}, HTBottomLeft},
		{"bottom-right corner", Point{1478, 1077}, HTBottomRight},
		{"top edge", Point{700, 202}, HTTop},
		{"bottom edge", Point{700, 1077}, HTBottom},
		{"left edge", Point{102, 600}, HTLeft},
		{"right edge", Point{1478, 600}, HTRight},
		{"interior client", Point{700, 600}, HTClient},
		{"outside window", Point{50, 50}, HTNowhere},
	}
	for _, tc := range cases {
		if got := HitTestFramelessWindow(bounds, tc.pt, 6, 12, false); got != tc.want {
			t.Fatalf("%s: expected hit-test %d, got %d", tc.name, tc.want, got)
		}
	}

	// When maximized (`IsZoomed == true`), outer border must return HTClient rather than resize handles.
	if got := HitTestFramelessWindow(bounds, Point{101, 201}, 6, 12, true); got != HTClient {
		t.Fatalf("expected HTClient on outer border when maximized, got %d", got)
	}

	// Verify WM_NCCALCSIZE maximized frame inset removes 8px overhang with zero bleed.
	mon := Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	work := Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1032}
	rawMax := Rect{Left: -8, Top: -8, Right: 1928, Bottom: 1040}
	inset := InsetMaximizedClientRect(rawMax, 8, 8, mon, work)
	if inset != work {
		t.Fatalf("expected maximized inset %+v to match workArea %+v", inset, work)
	}

	// Auto-hide taskbar (`workArea == monBounds`) preserves 1px bottom hover strip.
	rawAutoHide := Rect{Left: -8, Top: -8, Right: 1928, Bottom: 1088}
	insetAuto := InsetMaximizedClientRect(rawAutoHide, 8, 8, mon, mon)
	if insetAuto.Bottom != 1079 || insetAuto.Left != 0 || insetAuto.Top != 0 || insetAuto.Right != 1920 {
		t.Fatalf("expected auto-hide maximized rect (0,0,1920,1079), got %+v", insetAuto)
	}

	if ht, ok := ParseResizeEdgeHitTest("/bottom-right"); !ok || ht != HTBottomRight {
		t.Fatalf("expected HTBottomRight for /bottom-right, got ok=%v ht=%d", ok, ht)
	}
}

func TestFormatAutoStartAndMacOSLaunchAgentPlist(t *testing.T) {
	got := FormatAutoStartCommand(`  "D:\git_proj\colabhive\bin\colabhive-desktop.exe"  `)
	want := `"` + filepath.Clean(`D:\git_proj\colabhive\bin\colabhive-desktop.exe`) + `" --minimized`
	if got != want {
		t.Fatalf("FormatAutoStartCommand mismatch:\n got: %s\nwant: %s", got, want)
	}
	if empty := FormatAutoStartCommand("   "); empty != "" {
		t.Fatalf("expected empty string for blank path, got %q", empty)
	}

	plist := FormatMacOSLaunchAgentPlist("/Applications/ColabHive.app/Contents/MacOS/colabhive-desktop", "/Users/alice/.colabhive/logs")
	for _, needle := range []string{
		"<string>com.colabhive.desktop</string>",
		"<string>/Applications/ColabHive.app/Contents/MacOS/colabhive-desktop</string>",
		"<string>--minimized</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
	} {
		if !strings.Contains(plist, needle) {
			t.Fatalf("LaunchAgent plist missing %q:\n%s", needle, plist)
		}
	}
}

func TestRenderColabHiveIconAndICNSContainer(t *testing.T) {
	for _, sz := range []int{16, 32, 64, 256} {
		bgra, mask := BuildIconAndMaskBits(sz)
		if len(bgra) != sz*sz*4 {
			t.Fatalf("size %d: expected %d BGRA bytes, got %d", sz, sz*sz*4, len(bgra))
		}
		expectedMaskBytes := (((sz + 31) / 32) * 4) * sz
		if len(mask) != expectedMaskBytes {
			t.Fatalf("size %d: expected %d mask bytes, got %d", sz, expectedMaskBytes, len(mask))
		}

		// Corner `(0, 0)` outside Apple superellipse squircle must be 100% transparent (`alpha == 0`).
		if bgra[3] != 0 {
			t.Fatalf("size %d: top-left corner expected alpha=0, got %d", sz, bgra[3])
		}
		if mask[0]&0x80 == 0 {
			t.Fatalf("size %d: expected top-left AND mask bit to be 1 (transparent)", sz)
		}

		_, cells := buildHoneycombTriad(sz)
		toPx := func(norm float64) int {
			p := int((norm + 1.0) * 0.5 * float64(sz))
			if p < 0 {
				return 0
			}
			if p >= sz {
				return sz - 1
			}
			return p
		}

		// 1. Top Crown Cell -> Colab Amber-Gold (`#FBBF24 -> #F59E0B`: high R, warm G, low B)
		topX, topY := toPx(cells[0].center.x), toPx(cells[0].center.y)
		topOff := (topY*sz + topX) * 4
		tb, tg, tr, ta := bgra[topOff], bgra[topOff+1], bgra[topOff+2], bgra[topOff+3]
		if ta != 255 || tr < 200 || tg < 120 || tb > 110 || tr <= tg {
			t.Fatalf("size %d: expected Colab Amber top crown cell at (%d,%d), got BGRA=(%d,%d,%d,%d)", sz, topX, topY, tb, tg, tr, ta)
		}

		// 2. Bottom-Left Cell -> Crisp Warm Alabaster (`#FAF9F5 -> #E5E3DC`: high R, G, B)
		blX, blY := toPx(cells[1].center.x), toPx(cells[1].center.y)
		blOff := (blY*sz + blX) * 4
		lb, lg, lr, la := bgra[blOff], bgra[blOff+1], bgra[blOff+2], bgra[blOff+3]
		if la != 255 || lr < 195 || lg < 195 || lb < 185 {
			t.Fatalf("size %d: expected Warm Alabaster bottom-left cell at (%d,%d), got BGRA=(%d,%d,%d,%d)", sz, blX, blY, lb, lg, lr, la)
		}

		// 3. Bottom-Right Cell -> Live Compute Emerald (`#34D399 -> #10B981`: dominant G)
		brX, brY := toPx(cells[2].center.x), toPx(cells[2].center.y)
		brOff := (brY*sz + brX) * 4
		eb, eg, er, ea := bgra[brOff], bgra[brOff+1], bgra[brOff+2], bgra[brOff+3]
		if ea != 255 || eg < 155 || eg <= er*2 || eg <= eb {
			t.Fatalf("size %d: expected Compute Emerald bottom-right cell at (%d,%d), got BGRA=(%d,%d,%d,%d)", sz, brX, brY, eb, eg, er, ea)
		}
	}

	// Verify PNG encoding round-trip
	pngData, err := EncodeColabHivePNG(64)
	if err != nil {
		t.Fatalf("EncodeColabHivePNG(64) failed: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatalf("png.Decode failed: %v", err)
	}
	if decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 64 {
		t.Fatalf("expected 64x64 PNG bounds, got %v", decoded.Bounds())
	}

	// Verify Apple `.icns` container structure (`ic07`, `ic08`, `ic09`, `ic10`)
	icnsData, err := EncodeColabHiveICNS()
	if err != nil {
		t.Fatalf("EncodeColabHiveICNS() failed: %v", err)
	}
	if len(icnsData) < 64 || string(icnsData[:4]) != "icns" {
		t.Fatalf("missing 'icns' magic header")
	}
	declaredLen := binary.BigEndian.Uint32(icnsData[4:8])
	if int(declaredLen) != len(icnsData) {
		t.Fatalf("icns header size %d != actual byte length %d", declaredLen, len(icnsData))
	}
	seenTypes := map[string]bool{}
	offset := 8
	for offset+8 <= len(icnsData) {
		osType := string(icnsData[offset : offset+4])
		chunkLen := int(binary.BigEndian.Uint32(icnsData[offset+4 : offset+8]))
		if chunkLen < 16 || offset+chunkLen > len(icnsData) {
			t.Fatalf("invalid icns chunk %q length %d at offset %d", osType, chunkLen, offset)
		}
		payload := icnsData[offset+8 : offset+chunkLen]
		if !bytes.Equal(payload[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
			t.Fatalf("icns chunk %q payload is not a valid PNG", osType)
		}
		seenTypes[osType] = true
		offset += chunkLen
	}
	for _, wantType := range []string{"ic07", "ic08", "ic09", "ic10"} {
		if !seenTypes[wantType] {
			t.Fatalf("icns container missing required %s chunk (got %v)", wantType, seenTypes)
		}
	}
}

func TestParseWebViewMessageWindowActionsAndBootstrapJS(t *testing.T) {
	// 1. Exact `TrayPopoverPage.tsx` object message
	req, extURL, ok := ParseWebViewMessage(`{"type":"colabhive:desktop-action","action":"open-route","route":"/router","path":"/router"}`)
	if !ok || extURL != "" || req.Action != "open-route" || req.Route != "/router" {
		t.Fatalf("unexpected ParseWebViewMessage result: ok=%v req=%+v ext=%q", ok, req, extURL)
	}

	// 2. Frameless window control actions
	for _, act := range []string{"window-drag", "start-drag", "window-minimize", "window-maximize", "window-toggle-maximize", "window-close"} {
		wReq, _, wOk := ParseWebViewMessage(`{"action":"` + act + `"}`)
		if !wOk || wReq.Action != act || !IsWindowControlAction(act) {
			t.Fatalf("expected %s to parse as valid window control action, got ok=%v req=%+v", act, wOk, wReq)
		}
	}
	resizeReq, _, resizeOk := ParseWebViewMessage(`{"type":"colabhive:desktop-action","action":"window-resize","edge":"top-right"}`)
	if !resizeOk || resizeReq.Action != "window-resize" || resizeReq.Route != "/top-right" {
		t.Fatalf("expected window-resize with edge top-right, got ok=%v req=%+v", resizeOk, resizeReq)
	}

	// 3. JSON-quoted string from GetWebMessageAsJSON
	req2, _, ok2 := ParseWebViewMessage(`"quit"`)
	if !ok2 || req2.Action != "quit" {
		t.Fatalf("expected quit action from JSON string, got ok=%v req=%+v", ok2, req2)
	}

	// 4. External URL open vs unsafe scheme rejection
	req3, ext3, ok3 := ParseWebViewMessage(`{"type":"colabhive:desktop-action","action":"open-external","url":"https://colab.research.google.com/drive/123"}`)
	if !ok3 || req3.Action != "open-external" || ext3 != "https://colab.research.google.com/drive/123" {
		t.Fatalf("expected valid open-external, got ok=%v req=%+v ext=%q", ok3, req3, ext3)
	}
	if _, _, badOk := ParseWebViewMessage(`{"action":"open-external","url":"javascript:alert(1)"}`); badOk {
		t.Fatalf("expected javascript: URL to be rejected")
	}

	// 5. Bootstrap JS for Windows Main vs macOS Main vs `/tray`
	winMainJS := BuildNativeWebViewBootstrapJS("windows", true, false)
	if !strings.Contains(winMainJS, `var PLATFORM = "windows"`) ||
		!strings.Contains(winMainJS, `var IS_TRAY_SURFACE = false`) ||
		!strings.Contains(winMainJS, `__COLABHIVE_WINDOW_CONTROLS__`) ||
		!strings.Contains(winMainJS, `[data-colabhive-drag]`) {
		t.Fatalf("Windows main bootstrap JS missing frameless window controls or drag selector")
	}

	macMainJS := BuildNativeWebViewBootstrapJS("macos", false, false)
	if !strings.Contains(macMainJS, `var PLATFORM = "macos"`) ||
		!strings.Contains(macMainJS, `trafficLightInset: 76`) ||
		!strings.Contains(macMainJS, `window.webkit.messageHandlers.colabhive`) {
		t.Fatalf("macOS main bootstrap JS missing platform=macos, trafficLightInset=76, or WKWebView bridge")
	}

	trayJS := BuildNativeWebViewBootstrapJS("windows", false, true)
	if !strings.Contains(trayJS, `var IS_TRAY_SURFACE = true`) {
		t.Fatalf("Tray bootstrap JS must mark IS_TRAY_SURFACE = true")
	}

	// 6. ToggleDebouncer
	deb := NewToggleDebouncer(400 * time.Millisecond)
	t0 := time.Now()
	if !deb.ShouldExecute("toggle-autostart", t0) {
		t.Fatalf("first toggle should execute")
	}
	if deb.ShouldExecute("toggle-autostart", t0.Add(35*time.Millisecond)) {
		t.Fatalf("duplicate toggle 35ms later must be suppressed")
	}
	deb.Rollback("toggle-autostart")
	if !deb.ShouldExecute("toggle-autostart", t0.Add(40*time.Millisecond)) {
		t.Fatalf("toggle after Rollback must execute immediately")
	}

	ackReq, _, reqID, ackOk := ParseWebViewMessageWithAck(`{"type":"colabhive:desktop-action","req_id":"wv_123","action":"toggle-autostart"}`)
	if !ackOk || reqID != "wv_123" || ackReq.Action != "toggle-autostart" {
		t.Fatalf("expected req_id=wv_123 and action=toggle-autostart, got ok=%v reqID=%q req=%+v", ackOk, reqID, ackReq)
	}
}

func TestJoinBaseAndRouteShortInputsNoPanic(t *testing.T) {
	cases := []struct {
		base  string
		route string
		want  string
	}{
		{"", "", "/"},
		{"", "/router", "/router"},
		{"http://", "/router", "/router"},
		{"https://", "pool", "/pool"},
		{"http://", "http://", "/"},
		{"https://", "https://", "/"},
		{"short", "/pool", "short/pool"},
		{"http://a", "/pool", "http://a/pool"},
		{"http://127.0.0.1:7860", "router", "http://127.0.0.1:7860/router"},
		{"http://127.0.0.1:7860/tray?native=1", "/pool", "http://127.0.0.1:7860/pool"},
		{"https://hive.example.com/sub/path/", "http://127.0.0.1:7860/settings", "https://hive.example.com/settings"},
	}
	for _, tc := range cases {
		got := joinBaseAndRoute(tc.base, tc.route)
		if got != tc.want {
			t.Fatalf("joinBaseAndRoute(%q, %q) = %q, want %q", tc.base, tc.route, got, tc.want)
		}
	}
}

func TestStaleDaemonInfoFallsBackToConfiguredPort(t *testing.T) {
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"healthy":true,"status":"ok","version":"1.0.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer fakeSrv.Close()

	u, err := url.Parse(fakeSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	livePort, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	deadSrv := httptest.NewServer(http.NotFoundHandler())
	deadURL := deadSrv.URL
	deadSrv.Close()

	tmpDir := t.TempDir()
	daemonFile := filepath.Join(tmpDir, "daemon.json")
	t.Setenv("COLABHIVE_DAEMON_INFO_PATH", daemonFile)
	_ = server.WriteDaemonInfo(server.DaemonInfo{
		URL:       deadURL,
		PID:       999999,
		DBPath:    filepath.Join(tmpDir, "missing.db"),
		StartedAt: time.Now().UTC(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cp, err := EnsureControlPlane(ctx, Config{
		Port: livePort,
	}, nil)
	if err != nil {
		t.Fatalf("EnsureControlPlane with stale daemon.json failed: %v", err)
	}
	defer cp.Close()

	if cp.Owned {
		t.Fatalf("expected EnsureControlPlane to attach (Owned=false) to live port %d despite stale daemon.json", livePort)
	}
	wantURL := NormalizeLoopbackBaseURL(fakeSrv.URL)
	if cp.BaseURL != wantURL {
		t.Fatalf("expected BaseURL=%q, got %q", wantURL, cp.BaseURL)
	}
}

func TestFormatTrayTooltipAndUTF16Safety(t *testing.T) {
	sum := &models.QuotaSummary{
		TotalAvailableCU: 633.24,
		ActiveVMs:        3,
	}
	tip := FormatTrayTooltip(sum, true)
	if tip != "ColabHive · 剩余 633.2 CU · 3 台实例运行中" {
		t.Fatalf("unexpected tooltip: %q", tip)
	}
	menu := FormatPoolMenuLabel(sum, true)
	if menu != "算力池: 剩余 633.2 CU · 3 台运行中" {
		t.Fatalf("unexpected menu label: %q", menu)
	}

	longStr := strings.Repeat("🐝算力池测试", 40)
	trunc := TruncateUTF16Units(longStr, 120)
	if units := len(utf16.Encode([]rune(trunc))); units > 120 {
		t.Fatalf("expected <= 120 UTF-16 code units, got %d", units)
	}
}

func TestResolveMacOSAppBundlePaths(t *testing.T) {
	tmpRoot := t.TempDir()
	appBundle := filepath.Join(tmpRoot, "ColabHive.app")
	macOSDir := filepath.Join(appBundle, "Contents", "MacOS")
	resWebDist := filepath.Join(appBundle, "Contents", "Resources", "web", "dist")
	if err := os.MkdirAll(macOSDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resWebDist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resWebDist, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved := ResolveRuntimePaths(
		Config{},
		filepath.Join(macOSDir, "colabhive-desktop"),
		"",
		nil,
	)
	if !strings.EqualFold(resolved.WebDir, resWebDist) {
		t.Fatalf("expected macOS .app bundle WebDir=%s, got %s", resWebDist, resolved.WebDir)
	}
	if strings.Contains(strings.ToLower(filepath.ToSlash(resolved.DBPath)), ".app/contents") {
		t.Fatalf("expected macOS .app bundle DBPath outside read-only .app/Contents, got %s", resolved.DBPath)
	}
}

func TestResolveRuntimePathsAndControlPlaneAttach(t *testing.T) {
	tmpRoot := t.TempDir()
	dataDir := filepath.Join(tmpRoot, "data")
	webDist := filepath.Join(tmpRoot, "web", "dist")
	binDir := filepath.Join(tmpRoot, "bin")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(webDist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	prodDB := filepath.Join(dataDir, "colabhive_prod.db")
	if err := os.WriteFile(prodDB, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDist, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved := ResolveRuntimePaths(
		Config{},
		filepath.Join(binDir, "colabhive-desktop.exe"),
		os.TempDir(),
		nil,
	)
	if !strings.EqualFold(resolved.DBPath, prodDB) {
		t.Fatalf("expected DBPath=%s, got %s", prodDB, resolved.DBPath)
	}
	if !strings.EqualFold(resolved.WebDir, webDist) {
		t.Fatalf("expected WebDir=%s, got %s", webDist, resolved.WebDir)
	}

	daemonFile := filepath.Join(tmpRoot, "daemon.json")
	t.Setenv("COLABHIVE_DAEMON_INFO_PATH", daemonFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var lastBridgedAction string
	cp1, err := EnsureControlPlane(ctx, Config{
		Host:   "127.0.0.1",
		Port:   0,
		DBPath: ":memory:",
		WebDir: webDist,
		Demo:   true,
	}, func(_ context.Context, req server.DesktopActionRequest) (server.DesktopState, error) {
		if req.Action != "state" {
			lastBridgedAction = req.Action
		}
		return server.DesktopState{
			DesktopShell:      true,
			AutoStartEnabled:  true,
			MainWindowVisible: true,
			TrayVisible:       false,
			LastAction:        req.Action,
		}, nil
	})
	if err != nil {
		t.Fatalf("EnsureControlPlane (in-process) failed: %v", err)
	}
	defer cp1.Close()

	if !cp1.Owned {
		t.Fatalf("expected cp1.Owned=true")
	}

	actResp, err := http.Post(
		cp1.BaseURL+"/api/v1/desktop/action",
		"application/json",
		bytes.NewBufferString(`{"action":"open-route","route":"/router"}`),
	)
	if err != nil {
		t.Fatalf("POST /api/v1/desktop/action failed: %v", err)
	}
	defer actResp.Body.Close()
	if actResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/v1/desktop/action, got %d", actResp.StatusCode)
	}
	var parsed map[string]any
	if err := json.NewDecoder(actResp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if handled, _ := parsed["handled"].(bool); !handled {
		t.Fatalf("expected handled=true, got %+v", parsed)
	}
	if lastBridgedAction != "open-route" {
		t.Fatalf("expected bridge to receive open-route, got %q", lastBridgedAction)
	}

	cp2, err := EnsureControlPlane(ctx, Config{
		Host: "127.0.0.1",
		Port: 0,
	}, nil)
	if err != nil {
		t.Fatalf("EnsureControlPlane (attach) failed: %v", err)
	}
	defer cp2.Close()

	if cp2.Owned {
		t.Fatalf("expected cp2.Owned=false when attaching to live daemon")
	}
	if cp2.BaseURL != cp1.BaseURL {
		t.Fatalf("expected cp2.BaseURL=%s, got %s", cp1.BaseURL, cp2.BaseURL)
	}
}

func TestRouterPolicyPersistsAcrossControlPlaneRestarts(t *testing.T) {
	tmpRoot := t.TempDir()
	dbPath := filepath.Join(tmpRoot, "colabhive.db")
	webDist := filepath.Join(tmpRoot, "web", "dist")
	if err := os.MkdirAll(webDist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDist, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COLABHIVE_DAEMON_INFO_PATH", filepath.Join(tmpRoot, "daemon.json"))

	ctx := context.Background()

	// 1. First boot with empty DefaultStrategy -> fresh DB defaults to waste-zero; user switches to max-balance.
	cp1, err := EnsureControlPlane(ctx, Config{
		Host:   "127.0.0.1",
		Port:   0,
		DBPath: dbPath,
		WebDir: webDist,
	}, nil)
	if err != nil {
		t.Fatalf("EnsureControlPlane boot 1 failed: %v", err)
	}
	putReq, err := http.NewRequest(
		http.MethodPut,
		cp1.BaseURL+"/api/v1/router/policy",
		strings.NewReader(`{"default_strategy":"max-balance","auto_failover":true,"allow_free_fallback":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT /api/v1/router/policy failed: %v", err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on PUT /api/v1/router/policy, got %d", putResp.StatusCode)
	}
	if err := cp1.Close(); err != nil {
		t.Fatalf("cp1.Close() failed: %v", err)
	}
	_ = os.Remove(filepath.Join(tmpRoot, "daemon.json"))

	// 2. Second boot (simulating app upgrade/restart without --strategy) -> MUST preserve max-balance!
	cp2, err := EnsureControlPlane(ctx, Config{
		Host:   "127.0.0.1",
		Port:   0,
		DBPath: dbPath,
		WebDir: webDist,
	}, nil)
	if err != nil {
		t.Fatalf("EnsureControlPlane boot 2 failed: %v", err)
	}
	defer cp2.Close()

	getResp, err := http.Get(cp2.BaseURL + "/api/v1/router/policy")
	if err != nil {
		t.Fatalf("GET /api/v1/router/policy failed: %v", err)
	}
	defer getResp.Body.Close()
	var pol models.RouterPolicy
	if err := json.NewDecoder(getResp.Body).Decode(&pol); err != nil {
		t.Fatal(err)
	}
	if pol.DefaultStrategy != models.StrategyMaxBalance {
		t.Fatalf("expected persisted strategy %q after restart, got %q", models.StrategyMaxBalance, pol.DefaultStrategy)
	}
}
