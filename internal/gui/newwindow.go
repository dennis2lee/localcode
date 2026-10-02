//go:build gui

package gui

import (
	"net/url"
	"reflect"
	"strings"
	"unsafe"
)

// Links in the window go to the system's default browser.
//
// A link a model writes is drawn with target="_blank", and the window had
// nothing to say about a page asking for a new window, so WebView2 did what
// it does with no answer: it opened a window of its own, an Edge-engine
// popup with an address bar. It looked like Edge, it ignored whichever
// browser the person had chosen, and it left a second window of this
// program's runtime on screen. Only Windows ever did this: WebView2 is the
// one backend whose unanswered new-window request makes a window.
//
// The answer is given where WebView2 asks, so it covers every way a new
// window is requested and not only a left click: a middle click, ctrl or
// shift with a click, window.open, and "Open link in new window" in the
// right-click menu. See newwindow_windows.go.

// What the Windows side reads out of WebView2's COM interfaces, by slot.
//
// COM calls go through a table of function pointers, and a slot number is
// the whole of how a method is named, so a wrong one calls some other
// method with these arguments. The numbers below are the positions in
// WebView2.h's vtables, and newwindow_test.go checks every one against the
// header that ships with the pinned webview_go, so a typo or a dependency
// that moved a method fails a test on every machine rather than a window on
// somebody's desktop.
const (
	// ICoreWebView2Controller::get_CoreWebView2
	slotControllerGetCoreWebView2 = 25
	// ICoreWebView2::add_NewWindowRequested
	slotWebViewAddNewWindowRequested = 44
	// ICoreWebView2NewWindowRequestedEventArgs::get_Uri and ::put_Handled
	slotNewWindowArgsGetURI     = 3
	slotNewWindowArgsPutHandled = 6
	// ICoreWebView2NewWindowRequestedEventHandler::Invoke
	slotHandlerInvoke = 3

	// WEBVIEW_NATIVE_HANDLE_KIND_BROWSER_CONTROLLER in webview.h, which for
	// the Edge backend is the ICoreWebView2Controller.
	nativeHandleBrowserController = 2

	// IID_ICoreWebView2NewWindowRequestedEventHandler
	newWindowHandlerIID = "{D4C185FE-C81C-4989-97AF-2D3FA7AB5651}"
)

// maxExternalURL bounds what is handed to the shell. A link longer than this
// is not one anybody clicked on purpose.
const maxExternalURL = 8192

// Why externalURL refused an address. Each is a fixed phrase that names the
// rule and never quotes the address, so it can go in gui-frame.log: an
// address may carry a token in its query.
const (
	refusedEmpty     = "empty address"
	refusedTooLong   = "address too long"
	refusedBlank     = "space or control character"
	refusedQuote     = "double quote, less-than or greater-than character"
	refusedMalformed = "not a well-formed address"
	refusedNotWeb    = "scheme is not http or https"
	refusedNoHost    = "no host"
	maxSchemeLogged  = 32
)

// externalURL says whether raw is an address the window may hand to the
// system. It returns the form to hand over and an empty reason when it is,
// and an empty form and the reason (one of the refused* phrases) when it is
// not.
//
// Only http and https. The shell opens whatever an address names, so a
// file: URL runs a program, a UNC path reaches out to a share, and an
// application protocol (ms-msdt:, search-ms:, a store link) starts an
// application with attacker-chosen arguments. The links that matter here
// are written by a model from text it has read, and none of them is
// something the shell should be asked to run.
//
// Almost nothing is repaired. WebView2 hands over a normalised address, so
// one with a space, a control character or a trailing blank was not written
// by a browser and is refused rather than trimmed into something else.
//
// The one repair is a '%' that does not start an escape, which becomes
// "%25". A browser keeps such a '%' as written ("50%off", "#100%") and a
// server reads it as a literal percent sign either way, but the Windows
// shell does not: handed "https://example.com/50%off" it fails with "the
// system cannot find the file specified" and opens nothing, for Edge as for
// any other browser (measured on a runner). Spelled "%25" the address means
// the same and opens. See repairStrayPercents.
func externalURL(raw string) (target, reason string) {
	if raw == "" {
		return "", refusedEmpty
	}
	if len(raw) > maxExternalURL {
		return "", refusedTooLong
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f {
			return "", refusedBlank
		}
		// A browser's registered command wraps the address in quotes, so a
		// quote inside it would end that argument and start another. A
		// browser percent-encodes all three in every part of an address, so
		// none of them is in a link anybody wrote.
		if r == '"' || r == '<' || r == '>' {
			return "", refusedQuote
		}
	}
	raw = repairStrayPercents(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", refusedMalformed
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", refusedNotWeb
	}
	// An opaque address ("https:example.com") has no host either, so this is
	// also what refuses it.
	if u.Hostname() == "" {
		return "", refusedNoHost
	}
	return raw, ""
}

// repairStrayPercents returns raw with every '%' that starts no escape
// written as "%25", outside the host and port. A '%' followed by two hex
// digits is an escape and stays as it is.
//
// The host and port are left as written, so Go's rules for them still refuse
// a malformed host such as "exa%zzmple.com". The userinfo ends at the last
// '@' of the authority, and the authority at the first '/', '?' or '#', the
// same split url.Parse makes.
func repairStrayPercents(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	authStart := i + len("://")
	authEnd := len(raw)
	if j := strings.IndexAny(raw[authStart:], "/?#"); j >= 0 {
		authEnd = authStart + j
	}
	hostStart := authStart
	if at := strings.LastIndexByte(raw[authStart:authEnd], '@'); at >= 0 {
		hostStart = authStart + at + 1
	}
	return escapeStrayPercents(raw[:hostStart]) + raw[hostStart:authEnd] + escapeStrayPercents(raw[authEnd:])
}

func escapeStrayPercents(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && !(i+2 < len(s) && isHexDigit(s[i+1]) && isHexDigit(s[i+2])) {
			b.WriteString("%25")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// refusalNote is the line gui-frame.log gets for a refused address: the
// reason externalURL gave and, when the address starts with a scheme, the
// scheme. The rest of the address is left out, since it is the part that may
// be private.
func refusalNote(raw, reason string) string {
	scheme := schemeOf(raw)
	if scheme == "" {
		return reason + " (no scheme)"
	}
	if len(scheme) > maxSchemeLogged {
		scheme = scheme[:maxSchemeLogged] + "..."
	}
	return reason + " (scheme " + scheme + ")"
}

// schemeOf returns the scheme at the start of raw, or "" when raw does not
// start with one. A scheme is a letter followed by letters, digits, '+', '-'
// or '.', and ends at the first ':'.
func schemeOf(raw string) string {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == ':':
			return raw[:i]
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return ""
		}
	}
	return ""
}

// failed is the FAILED() macro: an HRESULT is failed when its high bit is
// set, and only its low 32 bits are the result.
func failed(hr uintptr) bool { return int32(hr) < 0 }

// The two HRESULTs the handler's QueryInterface answers with, as winerror.h
// has them.
const (
	eNoInterface = 0x80004002 // E_NOINTERFACE
	eInvalidArg  = 0x80070057 // E_INVALIDARG
)

// webviewHandle reads the C handle out of a webview.WebView.
//
// webview_go keeps it in an unexported field and offers no way to ask for
// it, and the handle is the only way to reach the browser control behind
// the window. The type is a pointer to a struct whose single field is the
// handle, and this refuses anything else instead of guessing: a library
// that changed shape makes the hook go quietly missing, which is the
// window going back to opening popups, and the version pin in
// newwindow_test.go is what makes that a test failure when it is bumped.
func webviewHandle(w any) (unsafe.Pointer, bool) {
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, false
	}
	s := v.Elem()
	if s.Kind() != reflect.Struct || s.NumField() != 1 {
		return nil, false
	}
	f := s.Field(0)
	if f.Kind() != reflect.UnsafePointer {
		return nil, false
	}
	p := f.UnsafePointer()
	return p, p != nil
}
