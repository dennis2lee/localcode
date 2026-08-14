//go:build gui && windows

package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A window with no title bar on Windows.
//
// Windows has no equivalent of macOS's "draw the title bar transparent and
// leave it there". The caption is the non-client area, and the non-client
// area is where moving, resizing from an edge, double-click-to-maximize
// and the close button all live — delete it and, without replacing every
// one of those, the window cannot be moved or closed at all.
//
// So it is replaced rather than deleted:
//
//   - WM_NCCALCSIZE says the client area is the whole window, which is
//     what removes the bar.
//   - WM_NCHITTEST puts the resize edges back, and makes a strip across
//     the top behave as the caption did: drag to move, double-click to
//     maximize, right-click for the system menu (which is also where
//     "Close" still is, alongside Alt+F4 and the taskbar).
//   - The strip's right-hand end is left as client area, where the page
//     draws minimise/maximise/close buttons and calls back through
//     lcWindowCommand. See windowBarWidth and the #window-bar block in
//     the Web UI.
//
// None of this can be tried from the machine it was written on, which is
// why LOCALCODE_TITLEBAR=1 exists: set it and the window keeps the
// ordinary Windows frame, with nothing else about the app changed.

// gwlpWndProc is a var rather than a const because it is negative and is
// passed as a uintptr: the conversion wraps at runtime, which is what the
// API expects, and a constant conversion would not compile at all.
var (
	gwlpWndProc = -4
	gwlStyle    = -16
)

const (
	wmNCCalcSize  = 0x0083
	wmNCHitTest   = 0x0084
	wmNCDestroy   = 0x0082
	wmClose       = 0x0010
	wmSysCommand  = 0x0112
	scMinimize    = 0xF020
	scMaximize    = 0xF030
	scRestore     = 0xF120
	swpFrameChang = 0x0020
	swpNoMove     = 0x0002
	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	wsCaption     = 0x00C00000
	wsThickFrame  = 0x00040000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsMaximizeBox = 0x00010000

	smCXFrame        = 32
	smCYFrame        = 33
	smCXPaddedBorder = 92

	htClient      = 1
	htCaption     = 2
	htLeft        = 10
	htRight       = 11
	htTop         = 12
	htTopLeft     = 13
	htTopRight    = 14
	htBottom      = 15
	htBottomLeft  = 16
	htBottomRight = 17
)

// The in-page title bar, in the layout units the stylesheet uses. The two
// have to agree — the buttons only receive clicks because the hit test
// hands that rectangle back to the page — so chrome_test.go reads these
// numbers out of style.css and fails the build if they drift.
const (
	windowBarHeight = 28
	windowBarWidth  = 138 // three 46px buttons
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	procCallWindowProc   = user32.NewProc("CallWindowProcW")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procIsZoomed         = user32.NewProc("IsZoomed")
	procPostMessage      = user32.NewProc("PostMessageW")
	// Windows 10 1607 and later. Absent on older systems, where the
	// fallback is the system-wide metric and a 100% scale assumption.
	procGetDpiForWindow = user32.NewProc("GetDpiForWindow")
)

type rect struct{ left, top, right, bottom int32 }

// nccalcsizeParams is NCCALCSIZE_PARAMS. Only the first rectangle — the
// proposed window rectangle — is read or written here.
type nccalcsizeParams struct {
	rgrc  [3]rect
	lppos uintptr
}

var (
	frameLogMu     sync.Mutex
	frameLogOpened bool
	// One line each, the first time they happen: enough to tell "the
	// subclass is not being called" from "it is, and the caption is coming
	// from somewhere else".
	firstNCCalcSize sync.Once
	firstHitTest    sync.Once

	framedMu sync.Mutex
	// frameRemoved records that the subclass actually took. The page's own
	// title bar is only offered when it did: drawing buttons over a window
	// that still has the system's would be two title bars, one of them
	// fake.
	frameRemoved bool
	// framed maps a window to the procedure it had before this one, which
	// is both how messages reach the default handling and how the
	// subclass is removed when the window goes away.
	framed = map[uintptr]uintptr{}
)

// hideTitleBar takes the frame off the window, unless asked not to.
//
// Two mechanisms, because the first one shipped alone in v0.44.0 and the
// caption was still there on the machine it was tried on:
//
//   - the subclass below, which answers WM_NCCALCSIZE, and
//   - WS_CAPTION cleared from the window style outright.
//
// The style change is the blunt one, and on its own it would take the
// move/resize/close behaviour with it — which is why the styles that carry
// resizing, the system menu and the minimise/maximise commands are kept,
// and why the hit test exists. Doing both means the caption cannot be
// drawn whichever of them Windows was ignoring.
func hideTitleBar(hwnd uintptr) {
	if hwnd == 0 || os.Getenv("LOCALCODE_TITLEBAR") != "" {
		return
	}
	prev, _, callErr := procSetWindowLongPtr.Call(hwnd, uintptr(gwlpWndProc), syscall.NewCallback(framelessProc))
	if prev == 0 {
		// Nothing was replaced. Leave the window exactly as it was, and
		// leave a note saying so, because from the outside this is
		// indistinguishable from the whole feature being absent.
		frameLog("subclass failed: hwnd=%#x err=%v", hwnd, callErr)
		return
	}
	framedMu.Lock()
	framed[hwnd] = prev
	frameRemoved = true
	framedMu.Unlock()

	before, _, _ := procGetWindowLongPtr.Call(hwnd, uintptr(gwlStyle))
	after := before &^ uintptr(wsCaption)
	// Kept: the thick frame is what Windows sizes the window by (and what
	// Aero Snap uses), the system menu is Alt+Space and the taskbar's
	// right-click Close, and the two box styles are the minimise and
	// maximise commands the page's buttons post.
	after |= uintptr(wsThickFrame | wsSysMenu | wsMinimizeBox | wsMaximizeBox)
	procSetWindowLongPtr.Call(hwnd, uintptr(gwlStyle), after)

	// The frame only changes when Windows is told to recalculate it. Without
	// this the caption stays on screen until something else resizes the
	// window.
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		uintptr(swpFrameChang|swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate))

	frameLog("subclass installed: hwnd=%#x style=%#x -> %#x", hwnd, before, after)
}

// frameLog records what the frame removal did, one file per launch.
//
// The desktop build has no console — that is deliberate, so starting it
// from cmd returns the prompt — so a failure here has nowhere to go and no
// symptom beyond "the title bar is still there", which is also what a
// build without the feature looks like. This is the difference between
// those two, and it is the only thing that can be asked for from a machine
// the developer does not have.
func frameLog(format string, args ...any) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "localcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, "gui-frame.log")

	frameLogMu.Lock()
	defer frameLogMu.Unlock()
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if !frameLogOpened {
		// Truncated once per launch: this is a handful of lines about the
		// last start, not a history.
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		frameLogOpened = true
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}

func previousProc(hwnd uintptr) uintptr {
	framedMu.Lock()
	defer framedMu.Unlock()
	return framed[hwnd]
}

func framelessProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	prev := previousProc(hwnd)
	if prev == 0 {
		// Should not happen; if it does, do nothing clever.
		return 0
	}

	switch msg {
	case wmNCCalcSize:
		if wparam != 0 {
			firstNCCalcSize.Do(func() { frameLog("first WM_NCCALCSIZE handled") })
			// The client area becomes the whole window, which is what
			// removes the caption and the frame.
			//
			// Maximized is the exception: Windows proposes a rectangle
			// that is larger than the work area by exactly the frame it
			// expects to draw, so leaving it alone puts the edges of the
			// page off-screen and over the taskbar.
			if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
				p := (*nccalcsizeParams)(unsafe.Pointer(lparam))
				x, y := frameThickness(hwnd)
				p.rgrc[0].left += x
				p.rgrc[0].top += y
				p.rgrc[0].right -= x
				p.rgrc[0].bottom -= y
			}
			return 0
		}

	case wmNCHitTest:
		if where, ok := hitTest(hwnd, lparam); ok {
			firstHitTest.Do(func() { frameLog("first WM_NCHITTEST answered with %d", where) })
			return uintptr(where)
		}

	case wmNCDestroy:
		// Put the original procedure back before the window goes, so
		// nothing is left pointing at a callback for a dead window.
		procSetWindowLongPtr.Call(hwnd, uintptr(gwlpWndProc), prev)
		framedMu.Lock()
		delete(framed, hwnd)
		framedMu.Unlock()
	}

	ret, _, _ := procCallWindowProc.Call(prev, hwnd, msg, wparam, lparam)
	return ret
}

// frameThickness is how wide Windows would have drawn the resize frame,
// which is what the edges are still worth being, in pixels for this
// window's DPI.
func frameThickness(hwnd uintptr) (x, y int32) {
	scale := dpiScale(hwnd)
	cx, _, _ := procGetSystemMetrics.Call(uintptr(smCXFrame))
	cy, _, _ := procGetSystemMetrics.Call(uintptr(smCYFrame))
	pad, _, _ := procGetSystemMetrics.Call(uintptr(smCXPaddedBorder))
	x = int32(float64(int32(cx)+int32(pad)) * scale)
	y = int32(float64(int32(cy)+int32(pad)) * scale)
	if x < 1 {
		x = 1
	}
	if y < 1 {
		y = 1
	}
	return x, y
}

// dpiScale is this window's scaling factor relative to the 96 DPI the
// system metrics are reported at, or 1 where it cannot be asked.
func dpiScale(hwnd uintptr) float64 {
	if err := procGetDpiForWindow.Find(); err != nil {
		return 1
	}
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// hitTest answers what is under the cursor, in place of the non-client
// area that is no longer there. Returns false for anything it has no
// opinion about, which falls through to the default handling.
func hitTest(hwnd, lparam uintptr) (int32, bool) {
	var r rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return 0, false
	}
	// Screen coordinates, signed: a window dragged off the left edge of
	// the primary monitor has negative x, and reading these as unsigned
	// puts the cursor at 65,000 pixels.
	x := int32(int16(lparam & 0xffff))
	y := int32(int16((lparam >> 16) & 0xffff))

	bx, by := frameThickness(hwnd)
	// The grab area is deliberately a little wider than the frame Windows
	// would have drawn: with no visible border to aim at, an exact 1px
	// edge is a target nobody can hit.
	if bx < 6 {
		bx = 6
	}
	if by < 6 {
		by = 6
	}

	zoomed, _, _ := procIsZoomed.Call(hwnd)
	if zoomed == 0 { // a maximized window has no edges to drag
		left, right := x < r.left+bx, x >= r.right-bx
		top, bottom := y < r.top+by, y >= r.bottom-by
		switch {
		case top && left:
			return htTopLeft, true
		case top && right:
			return htTopRight, true
		case bottom && left:
			return htBottomLeft, true
		case bottom && right:
			return htBottomRight, true
		case left:
			return htLeft, true
		case right:
			return htRight, true
		case top:
			return htTop, true
		case bottom:
			return htBottom, true
		}
	}

	scale := dpiScale(hwnd)
	if y < r.top+int32(float64(windowBarHeight)*scale) {
		// The buttons the page draws are its own to click.
		if x >= r.right-int32(float64(windowBarWidth)*scale) {
			return htClient, true
		}
		return htCaption, true
	}
	return 0, false
}

// windowCommand is what the page's minimise/maximise/close buttons do.
// Kept here rather than in the page's own reach: these are window
// operations, and the page has no window.
func windowCommand(hwnd uintptr, cmd string) {
	switch cmd {
	case "minimize":
		procPostMessage.Call(hwnd, uintptr(wmSysCommand), uintptr(scMinimize), 0)
	case "maximize":
		if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
			procPostMessage.Call(hwnd, uintptr(wmSysCommand), uintptr(scRestore), 0)
			return
		}
		procPostMessage.Call(hwnd, uintptr(wmSysCommand), uintptr(scMaximize), 0)
	case "close":
		procPostMessage.Call(hwnd, uintptr(wmClose), 0, 0)
	}
}

// windowControls is the function the page calls to work its own title bar
// buttons, or nil when this window kept the system's frame and has no need
// of them. Returning a plain func keeps the webview binding in gui.go and
// this file free of anything that cannot be compiled for Windows alone.
func windowControls(hwnd uintptr) func(string) {
	framedMu.Lock()
	removed := frameRemoved
	framedMu.Unlock()
	if hwnd == 0 || !removed {
		return nil
	}
	return func(cmd string) { windowCommand(hwnd, cmd) }
}
