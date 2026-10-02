//go:build gui && windows

package gui

import (
	"syscall"
	"unsafe"

	webview "github.com/webview/webview_go"
	"golang.org/x/sys/windows"
)

// routeNewWindows answers every request for a new window with the system's
// default browser.
//
// ICoreWebView2::NewWindowRequested is where WebView2 asks the host what to
// do with a link that wants its own window. With no handler it opens one
// itself: an Edge-engine popup with an address bar, whatever browser the
// person has set as their default. A handler that sets Handled stops that,
// and gets the address in the same call.
//
// The address is handed to ShellExecute, which resolves the default the way
// every other program on the machine does: the person's choice for the
// http or https protocol in Default apps. Reading that choice out of the
// registry here would reimplement it, and badly: it is a hashed per-user
// value naming a class, whose command line differs between browsers and
// sometimes is not a command line at all.
//
// Best effort, and loud about failing: a window that cannot install the
// hook is the window as it was, popups included, and frameLog says why.
func routeNewWindows(w webview.WebView) {
	handle, ok := webviewHandle(w)
	if !ok {
		frameLog("new-window hook: cannot reach the webview handle (%T), popups stay", w)
		return
	}
	controller := browserController(handle)
	if controller == 0 {
		frameLog("new-window hook: the webview has no browser controller, popups stay")
		return
	}
	var core uintptr
	if hr := comCall(controller, slotControllerGetCoreWebView2, uintptr(unsafe.Pointer(&core))); failed(hr) || core == 0 {
		frameLog("new-window hook: get_CoreWebView2 failed (hr=%#x), popups stay", uint32(hr))
		return
	}
	// The interface comes back with a reference taken for us. It is kept for
	// as long as the process lives, which is as long as the handler is.
	var token int64
	if hr := comCall(core, slotWebViewAddNewWindowRequested, uintptr(unsafe.Pointer(&newWindowHandler)), uintptr(unsafe.Pointer(&token))); failed(hr) {
		frameLog("new-window hook: add_NewWindowRequested failed (hr=%#x), popups stay", uint32(hr))
		return
	}
	frameLog("new-window hook installed")
}

// comCall calls method slot of a COM object. The first word of the object
// is the table, and the slot is an index into it.
//
// An argument may be the address of a local the method writes through
// (uintptr(unsafe.Pointer(&core))), and the directive is what makes that
// safe. Without it the compiler sees an integer, keeps the local in the
// goroutine's stack, and Go may move that stack (it grows it at a function
// prologue and shrinks it during a collection) between taking the address
// and the method writing to it. The integer is not updated when that
// happens, so the method writes into memory the runtime has already
// released and the local keeps its zero: no controller, or no address, and
// the window is back to opening popups. With the directive the compiler
// moves such a local to the heap and keeps it alive for the whole call, as
// syscall.Syscall and (*LazyProc).Call do for theirs.
// TestComCallPinsItsPointerArguments holds this in place, because nothing
// that runs off Windows would notice it going missing.
//
//go:uintptrescapes
func comCall(obj uintptr, slot int, args ...uintptr) uintptr {
	table := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(table + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

// The handler is a COM object of our own: a table of four functions and a
// pointer to it as its first word.
//
// Both live in package-level variables on purpose. WebView2 keeps the
// pointer it was given for as long as the window exists, outside anything
// the Go runtime can see, so the object must be somewhere that neither
// moves nor goes away. It is never freed, so AddRef and Release have
// nothing to count.
type comObject struct{ table *[4]uintptr }

var (
	newWindowTable   [4]uintptr
	newWindowHandler = comObject{table: &newWindowTable}

	iidIUnknown      = windows.GUID{Data1: 0, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidNewWindowSink = mustGUID(newWindowHandlerIID)
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic("gui: bad GUID constant " + s + ": " + err.Error())
	}
	return g
}

func init() {
	newWindowTable[0] = syscall.NewCallback(sinkQueryInterface)
	newWindowTable[1] = syscall.NewCallback(sinkAddRef)
	newWindowTable[2] = syscall.NewCallback(sinkRelease)
	newWindowTable[slotHandlerInvoke] = syscall.NewCallback(sinkInvoke)
}

func sinkQueryInterface(this, riid, out uintptr) uintptr {
	if out == 0 {
		return eInvalidArg
	}
	id := (*windows.GUID)(unsafe.Pointer(riid))
	if riid != 0 && (*id == iidIUnknown || *id == iidNewWindowSink) {
		*(*uintptr)(unsafe.Pointer(out)) = this
		return 0
	}
	*(*uintptr)(unsafe.Pointer(out)) = 0
	return eNoInterface
}

func sinkAddRef(this uintptr) uintptr  { return 1 }
func sinkRelease(this uintptr) uintptr { return 1 }

// sinkInvoke is NewWindowRequested. It runs on the thread that pumps the
// window's messages, which is also the one allowed to start another program
// on the person's behalf.
func sinkInvoke(this, sender, args uintptr) uintptr {
	var uri *uint16
	hr := comCall(args, slotNewWindowArgsGetURI, uintptr(unsafe.Pointer(&uri)))
	// Handled first, and whatever happened above. Leaving it false is what
	// makes WebView2 open the popup, and a link that could not be read or
	// was refused must not become one.
	comCall(args, slotNewWindowArgsPutHandled, 1)
	if failed(hr) || uri == nil {
		frameLog("new-window request: could not read the address (hr=%#x)", uint32(hr))
		return 0
	}
	raw := windows.UTF16PtrToString(uri)
	windows.CoTaskMemFree(unsafe.Pointer(uri))

	target, reason := externalURL(raw)
	if reason != "" {
		// Said without the address: it may carry a token in its query.
		frameLog("new-window request refused: %s", refusalNote(raw, reason))
		return 0
	}
	if err := openInDefaultBrowser(target); err != nil {
		frameLog("new-window request: the default browser did not open: %v", err)
	}
	return 0
}

var verbOpen = windows.StringToUTF16Ptr("open")

// openInDefaultBrowser hands an address to the shell, which opens it with
// the browser the person has chosen for its protocol.
func openInDefaultBrowser(target string) error {
	p, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verbOpen, p, nil, nil, windows.SW_SHOWNORMAL)
}
