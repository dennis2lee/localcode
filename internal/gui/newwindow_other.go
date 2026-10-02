//go:build gui && !windows

package gui

import webview "github.com/webview/webview_go"

// routeNewWindows is nothing off Windows. The popup this exists to redirect
// is WebView2's: it is the one backend that opens a window of its own for a
// request nobody answered. What WKWebView and WebKitGTK do with the same
// request is a separate question, and not one this change touches.
func routeNewWindows(w webview.WebView) {}
