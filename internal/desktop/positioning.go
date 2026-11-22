package desktop

import (
	"math"
	"strings"
)

// Standard Win32 Non-Client Hit-Test Codes (`WM_NCHITTEST` & `WM_NCLBUTTONDOWN`).
const (
	HTNowhere     uintptr = 0
	HTClient      uintptr = 1
	HTCaption     uintptr = 2
	HTLeft        uintptr = 10
	HTRight       uintptr = 11
	HTTop         uintptr = 12
	HTTopLeft     uintptr = 13
	HTTopRight    uintptr = 14
	HTBottom      uintptr = 15
	HTBottomLeft  uintptr = 16
	HTBottomRight uintptr = 17
)

// Point represents a 2D physical screen coordinate in pixels.
type Point struct {
	X int32
	Y int32
}

// Rect represents a Win32 screen rectangle (`Left`, `Top`, `Right`, `Bottom`) in physical pixels.
type Rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// Width returns the horizontal span of `r`.
func (r Rect) Width() int32 {
	if r.Right <= r.Left {
		return 0
	}
	return r.Right - r.Left
}

// Height returns the vertical span of `r`.
func (r Rect) Height() int32 {
	if r.Bottom <= r.Top {
		return 0
	}
	return r.Bottom - r.Top
}

// Empty reports whether `r` has zero or negative area.
func (r Rect) Empty() bool {
	return r.Width() <= 0 || r.Height() <= 0
}

// Contains reports whether point `p` lies inside `r`.
func (r Rect) Contains(p Point) bool {
	return p.X >= r.Left && p.X <= r.Right && p.Y >= r.Top && p.Y <= r.Bottom
}

// Center returns the midpoint of `r`.
func (r Rect) Center() Point {
	return Point{
		X: r.Left + r.Width()/2,
		Y: r.Top + r.Height()/2,
	}
}

// HitTestFramelessWindow maps the outer `borderPx` edges (`cornerPx` corners) of a frameless
// client-extended Win32 window to `HTTOPLEFT`..`HTBOTTOMRIGHT` when `!maximized`, and `HTCLIENT` inside.
func HitTestFramelessWindow(winBounds Rect, cursor Point, borderPx int32, cornerPx int32, maximized bool) uintptr {
	if winBounds.Empty() || !winBounds.Contains(cursor) {
		return HTNowhere
	}
	if maximized {
		return HTClient
	}
	if borderPx <= 0 {
		borderPx = 6
	}
	if cornerPx < borderPx {
		cornerPx = borderPx * 2
	}

	nearLeft := cursor.X < winBounds.Left+borderPx
	nearRight := cursor.X >= winBounds.Right-borderPx
	nearTop := cursor.Y < winBounds.Top+borderPx
	nearBottom := cursor.Y >= winBounds.Bottom-borderPx

	if !nearLeft && !nearRight && !nearTop && !nearBottom {
		return HTClient
	}

	inCornerLeft := cursor.X < winBounds.Left+cornerPx
	inCornerRight := cursor.X >= winBounds.Right-cornerPx
	inCornerTop := cursor.Y < winBounds.Top+cornerPx
	inCornerBottom := cursor.Y >= winBounds.Bottom-cornerPx

	if (nearTop && inCornerLeft) || (nearLeft && inCornerTop) {
		return HTTopLeft
	}
	if (nearTop && inCornerRight) || (nearRight && inCornerTop) {
		return HTTopRight
	}
	if (nearBottom && inCornerLeft) || (nearLeft && inCornerBottom) {
		return HTBottomLeft
	}
	if (nearBottom && inCornerRight) || (nearRight && inCornerBottom) {
		return HTBottomRight
	}
	if nearTop {
		return HTTop
	}
	if nearBottom {
		return HTBottom
	}
	if nearLeft {
		return HTLeft
	}
	if nearRight {
		return HTRight
	}
	return HTClient
}

// InsetMaximizedClientRect insets Win32's oversized maximized window rectangle (`WM_NCCALCSIZE` when `IsZoomed`)
// by `frameX`/`frameY` (`SM_CXSIZEFRAME + SM_CXPADDEDBORDER`) and clamps cleanly to `workArea` with zero bleed.
func InsetMaximizedClientRect(raw Rect, frameX, frameY int32, monBounds, workArea Rect) Rect {
	out := Rect{
		Left:   raw.Left + maxInt32(frameX, 0),
		Top:    raw.Top + maxInt32(frameY, 0),
		Right:  raw.Right - maxInt32(frameX, 0),
		Bottom: raw.Bottom - maxInt32(frameY, 0),
	}
	if !workArea.Empty() {
		if out.Left < workArea.Left {
			out.Left = workArea.Left
		}
		if out.Top < workArea.Top {
			out.Top = workArea.Top
		}
		if out.Right > workArea.Right {
			out.Right = workArea.Right
		}
		if out.Bottom > workArea.Bottom {
			out.Bottom = workArea.Bottom
		}
		// Preserve a 1px bottom trigger strip when Windows Taskbar is set to auto-hide (`workArea == monBounds`).
		if !monBounds.Empty() && workArea == monBounds && out.Bottom > out.Top+100 {
			out.Bottom--
		}
	}
	return out
}

// ParseResizeEdgeHitTest maps a WebView edge identifier (`"top-left"`, `"right"`, etc.) to its Win32 `HT*` constant.
func ParseResizeEdgeHitTest(edge string) (uintptr, bool) {
	norm := strings.ToLower(strings.Trim(strings.TrimSpace(edge), "/"))
	switch norm {
	case "top", "n":
		return HTTop, true
	case "bottom", "s":
		return HTBottom, true
	case "left", "w":
		return HTLeft, true
	case "right", "e":
		return HTRight, true
	case "top-left", "topleft", "nw":
		return HTTopLeft, true
	case "top-right", "topright", "ne":
		return HTTopRight, true
	case "bottom-left", "bottomleft", "sw":
		return HTBottomLeft, true
	case "bottom-right", "bottomright", "se":
		return HTBottomRight, true
	default:
		return HTNowhere, false
	}
}

// TaskbarEdge identifies which monitor edge hosts the Windows Taskbar.
type TaskbarEdge string

const (
	EdgeBottom TaskbarEdge = "bottom"
	EdgeTop    TaskbarEdge = "top"
	EdgeLeft   TaskbarEdge = "left"
	EdgeRight  TaskbarEdge = "right"
)

// FlyoutLayoutInput holds the geometry inputs required to position the Magpie-style `/tray` popover.
type FlyoutLayoutInput struct {
	LogicalWidth  int    // Default 396
	LogicalHeight int    // Default 580
	GapLogicalPx  int    // Default 8
	DPI           uint32 // Default 96 (100% scale)
	AnchorRect    *Rect  // Optional tray icon rect from Shell_NotifyIconGetRect
	Cursor        Point  // Fallback cursor position from GetCursorPos
	MonitorBounds Rect   // Full monitor bounds (MONITORINFO.rcMonitor)
	WorkArea      Rect   // Usable monitor work area excluding taskbar (MONITORINFO.rcWork)
}

// FlyoutPlacement describes the computed physical screen rectangle for the Magpie tray flyout.
type FlyoutPlacement struct {
	X        int32
	Y        int32
	Width    int32
	Height   int32
	DockEdge TaskbarEdge
	Scale    float64
}

// ScaleForDPI scales a 96-DPI logical pixel dimension to physical pixels for `dpi`.
func ScaleForDPI(logicalPx int, dpi uint32) int32 {
	if dpi == 0 {
		dpi = 96
	}
	scale := float64(dpi) / 96.0
	return int32(math.Round(float64(logicalPx) * scale))
}

// DetectTaskbarEdge infers the docked taskbar edge by comparing `monitorBounds` and `workArea`.
// When taskbar auto-hide is enabled (`monitorBounds == workArea`), it falls back to anchor/cursor proximity.
func DetectTaskbarEdge(monitorBounds, workArea Rect, anchor Point) TaskbarEdge {
	if monitorBounds.Empty() || workArea.Empty() {
		return EdgeBottom
	}

	topInset := workArea.Top - monitorBounds.Top
	bottomInset := monitorBounds.Bottom - workArea.Bottom
	leftInset := workArea.Left - monitorBounds.Left
	rightInset := monitorBounds.Right - workArea.Right

	maxInset := maxInt32(maxInt32(topInset, bottomInset), maxInt32(leftInset, rightInset))
	if maxInset > 0 {
		switch maxInset {
		case bottomInset:
			return EdgeBottom
		case topInset:
			return EdgeTop
		case leftInset:
			return EdgeLeft
		case rightInset:
			return EdgeRight
		}
	}

	// Auto-hidden taskbar: infer edge from whichever monitor border is closest to `anchor`.
	distBottom := absInt32(monitorBounds.Bottom - anchor.Y)
	distTop := absInt32(anchor.Y - monitorBounds.Top)
	distLeft := absInt32(anchor.X - monitorBounds.Left)
	distRight := absInt32(monitorBounds.Right - anchor.X)

	minDist := minInt32(minInt32(distBottom, distTop), minInt32(distLeft, distRight))
	switch minDist {
	case distTop:
		return EdgeTop
	case distLeft:
		return EdgeLeft
	case distRight:
		return EdgeRight
	default:
		return EdgeBottom
	}
}

// ComputeFlyoutPosition calculates the DPI-scaled physical window coordinates for the `396x580`
// Magpie-style tray flyout, anchored cleanly `8px` from the taskbar and clamped inside `WorkArea`.
func ComputeFlyoutPosition(in FlyoutLayoutInput) FlyoutPlacement {
	logicalW := in.LogicalWidth
	if logicalW <= 0 {
		logicalW = DefaultFlyoutLogicalWidth
	}
	logicalH := in.LogicalHeight
	if logicalH <= 0 {
		logicalH = DefaultFlyoutLogicalHeight
	}
	gapLogical := in.GapLogicalPx
	if gapLogical <= 0 {
		gapLogical = DefaultFlyoutGapPx
	}
	dpi := in.DPI
	if dpi == 0 {
		dpi = 96
	}
	scale := float64(dpi) / 96.0

	work := in.WorkArea
	mon := in.MonitorBounds
	if work.Empty() {
		if !mon.Empty() {
			work = mon
		} else {
			work = Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1032}
			mon = Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
		}
	}
	if mon.Empty() {
		mon = work
	}

	gap := ScaleForDPI(gapLogical, dpi)
	edgeMargin := gap

	w := ScaleForDPI(logicalW, dpi)
	h := ScaleForDPI(logicalH, dpi)

	maxW := work.Width() - 2*edgeMargin
	if maxW > 120 && w > maxW {
		w = maxW
	}
	maxH := work.Height() - 2*edgeMargin
	if maxH > 120 && h > maxH {
		h = maxH
	}

	anchor := in.Cursor
	hasAnchorRect := in.AnchorRect != nil && !in.AnchorRect.Empty()
	if hasAnchorRect {
		anchor = in.AnchorRect.Center()
	}

	edge := DetectTaskbarEdge(mon, work, anchor)

	var x, y int32
	switch edge {
	case EdgeTop:
		x = clampInt32(anchor.X-w/2, work.Left+edgeMargin, work.Right-w-edgeMargin)
		y = work.Top + gap
		if hasAnchorRect && in.AnchorRect.Bottom > work.Top && in.AnchorRect.Bottom+gap+h <= work.Bottom-edgeMargin {
			y = in.AnchorRect.Bottom + gap
		}

	case EdgeLeft:
		x = work.Left + gap
		if hasAnchorRect && in.AnchorRect.Right > work.Left && in.AnchorRect.Right+gap+w <= work.Right-edgeMargin {
			x = in.AnchorRect.Right + gap
		}
		y = clampInt32(anchor.Y-h/2, work.Top+edgeMargin, work.Bottom-h-edgeMargin)

	case EdgeRight:
		x = work.Right - w - gap
		if hasAnchorRect && in.AnchorRect.Left < work.Right && in.AnchorRect.Left-gap-w >= work.Left+edgeMargin {
			x = in.AnchorRect.Left - w - gap
		}
		y = clampInt32(anchor.Y-h/2, work.Top+edgeMargin, work.Bottom-h-edgeMargin)

	case EdgeBottom:
		fallthrough
	default:
		x = clampInt32(anchor.X-w/2, work.Left+edgeMargin, work.Right-w-edgeMargin)
		y = work.Bottom - h - gap
		// If the icon lives inside the Windows 11 tray overflow popup above the taskbar,
		// place the flyout `8px` above the overflow icon when vertical space allows.
		if hasAnchorRect && in.AnchorRect.Top < work.Bottom && in.AnchorRect.Top-gap-h >= work.Top+edgeMargin {
			y = in.AnchorRect.Top - h - gap
		}
	}

	x = clampInt32(x, work.Left+edgeMargin, work.Right-w-edgeMargin)
	y = clampInt32(y, work.Top+edgeMargin, work.Bottom-h-edgeMargin)

	return FlyoutPlacement{
		X:        x,
		Y:        y,
		Width:    w,
		Height:   h,
		DockEdge: edge,
		Scale:    scale,
	}
}

// ComputeCenteredWindowRect returns a centered physical screen rectangle for the Main Window,
// clamped cleanly inside `workArea` so smaller or high-DPI laptop screens never clip window edges.
func ComputeCenteredWindowRect(logicalW, logicalH int, dpi uint32, workArea Rect) Rect {
	if logicalW <= 0 {
		logicalW = DefaultMainLogicalWidth
	}
	if logicalH <= 0 {
		logicalH = DefaultMainLogicalHeight
	}
	if workArea.Empty() {
		workArea = Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1032}
	}

	margin := ScaleForDPI(20, dpi)
	w := ScaleForDPI(logicalW, dpi)
	h := ScaleForDPI(logicalH, dpi)

	if availW := workArea.Width() - 2*margin; availW > 320 && w > availW {
		w = availW
	}
	if availH := workArea.Height() - 2*margin; availH > 240 && h > availH {
		h = availH
	}

	x := workArea.Left + (workArea.Width()-w)/2
	y := workArea.Top + (workArea.Height()-h)/2

	return Rect{
		Left:   x,
		Top:    y,
		Right:  x + w,
		Bottom: y + h,
	}
}

func clampInt32(v, lo, hi int32) int32 {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minInt32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func absInt32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
