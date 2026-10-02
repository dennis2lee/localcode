//go:build gui && windows

package gui

/*
// webview.h lives inside the webview_go module and is not on this
// package's include path, so the one function needed from it is declared
// here. Its definition is linked in from that module's object.
extern void *webview_get_native_handle(void *w, int kind);

static void *nativeHandle(void *w, int kind) {
	return webview_get_native_handle(w, kind);
}
*/
import "C"

import "unsafe"

// browserController is the ICoreWebView2Controller behind a webview_t.
func browserController(w unsafe.Pointer) uintptr {
	return uintptr(C.nativeHandle(w, C.int(nativeHandleBrowserController)))
}
