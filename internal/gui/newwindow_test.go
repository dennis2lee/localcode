//go:build gui

package gui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unsafe"
)

// What may be handed to the shell. The refusals matter more than the
// accepts: the shell opens whatever an address names.
func TestExternalURLAllowsOnlyWebAddresses(t *testing.T) {
	// address -> what is handed to the shell. Almost always the address
	// itself.
	accepted := map[string]string{
		"https://example.com/":                  "https://example.com/",
		"http://example.com/a/b?x=1&y=2#frag":   "http://example.com/a/b?x=1&y=2#frag",
		"HTTPS://EXAMPLE.COM/Path":              "HTTPS://EXAMPLE.COM/Path",
		"https://example.com:8443/x":            "https://example.com:8443/x",
		"http://127.0.0.1:4096/":                "http://127.0.0.1:4096/",
		"http://[::1]:8080/index.html":          "http://[::1]:8080/index.html",
		"https://xn--bcher-kva.example/":        "https://xn--bcher-kva.example/",
		"https://user:pw@example.com/":          "https://user:pw@example.com/",
		"https://example.com/a%20b?q=%E2%9C%93": "https://example.com/a%20b?q=%E2%9C%93",
		"https://example.com/50%25off":          "https://example.com/50%25off",
		// Characters a model-written link has in it all the time.
		"https://en.wikipedia.org/wiki/Function_(mathematics)": "https://en.wikipedia.org/wiki/Function_(mathematics)",
		"http://localhost:3000/":                               "http://localhost:3000/",
		"https://example.com/it's":                             "https://example.com/it's",
		"https://example.com/~user/a,b;c$d+e!f*g":              "https://example.com/~user/a,b;c$d+e!f*g",
		"https://example.com/caf\u00e9":                        "https://example.com/caf\u00e9",
		// A '%' that starts no escape is kept as written by a browser, so it
		// reaches the hook as it is, and the shell cannot open it that way.
		// It is handed over spelled "%25".
		"https://example.com/deals/50%off": "https://example.com/deals/50%25off",
		"https://example.com/docs#100%":    "https://example.com/docs#100%25",
		"https://user:p%w@example.com/":    "https://user:p%25w@example.com/",
		"https://u%x@example.com/":         "https://u%25x@example.com/",
		"https://example.com/a%":           "https://example.com/a%25",
		"https://example.com/a%4":          "https://example.com/a%254",
		"https://example.com/a%zz":         "https://example.com/a%25zz",
		"https://example.com/a%%41":        "https://example.com/a%25%41",
		"https://example.com/100%_pure":    "https://example.com/100%25_pure",
		"https://example.com/sale-50%-off": "https://example.com/sale-50%25-off",
		"https://example.com/page#50%":     "https://example.com/page#50%25",
		"https://example.com#50%":          "https://example.com#50%25",
		"https://example.com/?q=100%":      "https://example.com/?q=100%25",
	}
	for raw, want := range accepted {
		got, reason := externalURL(raw)
		if reason != "" || got != want {
			t.Errorf("externalURL(%q) = %q, %q; want it accepted as %q", raw, got, reason, want)
		}
	}

	refused := map[string]string{
		"empty":                "",
		"javascript":           "javascript:alert(1)",
		"file":                 "file:///C:/Windows/System32/calc.exe",
		"file with host":       "file://host/share/a.exe",
		"mailto":               "mailto:someone@example.com",
		"ftp":                  "ftp://example.com/a",
		"data":                 "data:text/html,<b>x</b>",
		"ms-msdt":              "ms-msdt:/id PCWDiagnostic /skip force /param \"IT_RebrowseForFile=?\"",
		"search-ms":            "search-ms:query=x&crumb=location:\\\\evil\\share",
		"store":                "ms-windows-store://pdp/?productid=9NBLGGH4NNS1",
		"unc path":             `\\evil\share\a.exe`,
		"drive path":           `C:\Windows\System32\calc.exe`,
		"relative":             "/docs/page",
		"scheme relative":      "//example.com/a",
		"no scheme":            "example.com/a",
		"no host":              "http:///a",
		"empty host":           "https://:443/",
		"opaque":               "https:example.com",
		"space inside":         "http://example.com/a b",
		"leading space":        " http://example.com/",
		"trailing space":       "http://example.com/ ",
		"newline":              "http://example.com/\nmalicious",
		"tab":                  "http://example.com/\ta",
		"nul":                  "http://example.com/\x00",
		"delete":               "http://example.com/\x7f",
		"newline in fragment":  "http://example.com/#\n",
		"nul in fragment":      "http://example.com/#\x00",
		"delete in fragment":   "http://example.com/#\x7f",
		"tab in fragment":      "http://example.com/#a\tb",
		"unparseable":          "http://exa mple.com%zz/",
		"scheme only":          "https:",
		"too long":             "https://example.com/" + strings.Repeat("a", maxExternalURL),
		"unicode scheme trick": "\uff48ttps://example.com/",
		"double quote":         `https://example.com/a"--flag=1`,
		"quote in query":       `https://example.com/?q="x"`,
		"quote in fragment":    `https://example.com/#"`,
		"less than":            "https://example.com/<script>",
		"lone less than":       "https://example.com/a<b",
		"greater than":         "https://example.com/a>b",
		// The host and port are still judged as written: a '%' there is not
		// an escape in a path to be forgiven.
		"stray escape in host":          "https://exa%zzmple.com/",
		"stray escape in host and user": "https://user%zz@exa%zzmple.com/",
		// A scheme the shell would act on, written with an authority so that
		// the host check cannot be what refuses it.
		"javascript with host": "javascript://example.com/%0Aalert(1)",
		"data with host":       "data://example.com/x",
		"mailto with host":     "mailto://example.com/x",
		"ms-msdt with host":    "ms-msdt://example.com/id",
		"search-ms with host":  "search-ms://example.com/?query=x",
		"vbscript with host":   "vbscript://example.com/",
		"ws with host":         "ws://example.com/",
	}
	for name, raw := range refused {
		got, reason := externalURL(raw)
		if reason == "" {
			t.Errorf("externalURL(%s: %q) = %q, accepted; want it refused", name, raw, got)
			continue
		}
		if got != "" {
			t.Errorf("externalURL(%s: %q) refused with %q but still returned %q", name, raw, reason, got)
		}
	}
}

// A refused address says why, in a phrase that names the rule. gui-frame.log
// is the only place anybody learns that a click did nothing, and it used to
// call every refusal a scheme refusal.
func TestExternalURLSaysWhyItRefused(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"empty", "", refusedEmpty},
		{"too long", "https://example.com/" + strings.Repeat("a", maxExternalURL), refusedTooLong},
		{"space", "http://example.com/a b", refusedBlank},
		{"newline", "http://example.com/\n", refusedBlank},
		{"newline in fragment", "http://example.com/#\n", refusedBlank},
		{"double quote", `https://example.com/a"b`, refusedQuote},
		{"less than", "https://example.com/a<b", refusedQuote},
		{"greater than", "https://example.com/a>b", refusedQuote},
		{"bad host escape", "https://exa%zzmple.com/", refusedMalformed},
		{"ftp", "ftp://example.com/a", refusedNotWeb},
		{"javascript", "javascript:alert(1)", refusedNotWeb},
		{"store", "ms-windows-store://pdp/?productid=9NBLGGH4NNS1", refusedNotWeb},
		{"no scheme", "example.com/a", refusedNotWeb},
		{"opaque", "https:example.com", refusedNoHost},
		{"empty host", "https://:443/", refusedNoHost},
	}
	for _, c := range cases {
		if _, got := externalURL(c.raw); got != c.want {
			t.Errorf("externalURL(%s: %q) refused with %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

// A '%' that starts no escape is written "%25" outside the host and port,
// and nothing else is touched. The split is the one url.Parse makes: the
// userinfo ends at the last '@' of the authority, the authority at the
// first '/', '?' or '#'.
func TestRepairStrayPercents(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com/50%off", "https://example.com/50%25off"},
		{"https://u:p%w@example.com/", "https://u:p%25w@example.com/"},
		{"https://exa%zzmple.com/x%", "https://exa%zzmple.com/x%25"},
		{"https://exa%zzmple.com:80%/x", "https://exa%zzmple.com:80%/x"},
		{"https://example.com#50%", "https://example.com#50%25"},
		{"https://example.com?q=%", "https://example.com?q=%25"},
		{"https://a@b%@exa%zz.com/%", "https://a@b%25@exa%zz.com/%25"},
		{"https://u%@host/p@q%", "https://u%25@host/p@q%25"},
		// Escapes stay as they are, in either case, and a percent sign that
		// is already spelled %25 is not spelled again.
		{"https://example.com/a%41b", "https://example.com/a%41b"},
		{"https://example.com/a%4fb", "https://example.com/a%4fb"},
		{"https://example.com/a%4Fb", "https://example.com/a%4Fb"},
		{"https://example.com/a%20b?q=%E2%9C%93", "https://example.com/a%20b?q=%E2%9C%93"},
		{"https://example.com/50%25off", "https://example.com/50%25off"},
		// A '%' that is followed by one hex digit and then the end, or by a
		// '%', starts no escape.
		{"https://example.com/%4", "https://example.com/%254"},
		{"https://example.com/%%41", "https://example.com/%25%41"},
		{"https://example.com/%g1", "https://example.com/%25g1"},
		{"https://example.com/%1g", "https://example.com/%251g"},
		{"https://", "https://"},
		{"https:%", "https:%"},
		{"no-scheme%", "no-scheme%"},
		{"", ""},
	}
	for _, c := range cases {
		if got := repairStrayPercents(c.in); got != c.want {
			t.Errorf("repairStrayPercents(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// What gui-frame.log gets for a refusal: the reason and the scheme, and
// nothing else of the address, which may carry a token.
func TestRefusalNoteNamesTheSchemeAndNothingElse(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"store link", "ms-windows-store://pdp/?productid=9NBLGGH4NNS1", "scheme ms-windows-store"},
		{"sixteen characters", "abcdefghijklmnop:rest", "scheme abcdefghijklmnop"},
		{"seventeen characters", "abcdefghijklmnopq:rest", "scheme abcdefghijklmnopq"},
		{"web address", "https://example.com/?token=abc123", "scheme https"},
		{"upper case", "HTTPS://example.com/", "scheme HTTPS"},
		{"every scheme character", "a1+b-c.d:rest", "scheme a1+b-c.d"},
		{"single letter", `C:\Windows\System32\calc.exe`, "scheme C"},
		{"capped", strings.Repeat("a", maxSchemeLogged+10) + "://h/", "scheme " + strings.Repeat("a", maxSchemeLogged) + "..."},
		{"at the cap", strings.Repeat("a", maxSchemeLogged) + "://h/", "scheme " + strings.Repeat("a", maxSchemeLogged)},
		{"empty", "", "no scheme"},
		{"no colon", "example.com/a", "no scheme"},
		{"leading colon", ":rest", "no scheme"},
		{"starts with a digit", "1http://example.com/", "no scheme"},
		{"starts with a plus", "+http://example.com/", "no scheme"},
		{"space before the colon", "ht tp://example.com/", "no scheme"},
		{"unc path", `\\evil\share\a.exe`, "no scheme"},
		{"non-ASCII", "\uff48ttps://example.com/", "no scheme"},
	}
	for _, c := range cases {
		got := refusalNote(c.raw, "why")
		want := "why (" + c.want + ")"
		if got != want {
			t.Errorf("refusalNote(%s: %q) = %q, want %q", c.name, c.raw, got, want)
		}
	}
	for _, secret := range []string{"productid", "token", "abc123", "evil"} {
		for _, c := range cases {
			if strings.Contains(refusalNote(c.raw, "why"), secret) {
				t.Errorf("refusalNote(%q) quotes %q from the address", c.raw, secret)
			}
		}
	}
}

// An HRESULT fails when bit 31 of its low 32 bits is set, whatever the
// upper half of the register holds.
func TestFailedReadsTheLow32BitsOfAnHRESULT(t *testing.T) {
	var high uintptr = 1
	high <<= 32 // zero where uintptr is 32 bits wide, which is fine
	cases := []struct {
		name string
		hr   uintptr
		want bool
	}{
		{"S_OK", 0, false},
		{"S_FALSE", 1, false},
		{"largest success", 0x7fffffff, false},
		{"smallest failure", 0x80000000, true},
		{"E_NOINTERFACE", 0x80004002, true},
		{"E_INVALIDARG", 0x80070057, true},
		{"all bits", ^uintptr(0), true},
		{"success with garbage above", high | 1, false},
		{"failure with garbage above", high | 0x80004002, true},
	}
	// The two the handler answers with are the SDK's values, and failures.
	if eNoInterface != 0x80004002 || eInvalidArg != 0x80070057 {
		t.Errorf("eNoInterface = %#x, eInvalidArg = %#x; winerror.h has 0x80004002 and 0x80070057", eNoInterface, eInvalidArg)
	}
	for _, c := range cases {
		if got := failed(c.hr); got != c.want {
			t.Errorf("failed(%s = %#x) = %v, want %v", c.name, c.hr, got, c.want)
		}
	}
}

// The boundary of the length bound: exactly the bound is allowed, one more
// is not.
func TestExternalURLLengthBound(t *testing.T) {
	base := "https://example.com/"
	atBound := base + strings.Repeat("a", maxExternalURL-len(base))
	if len(atBound) != maxExternalURL {
		t.Fatalf("fixture is %d bytes, want %d", len(atBound), maxExternalURL)
	}
	if _, reason := externalURL(atBound); reason != "" {
		t.Errorf("an address exactly at the bound was refused: %s", reason)
	}
	if _, reason := externalURL(atBound + "a"); reason == "" {
		t.Error("an address one past the bound was accepted")
	}
}

type fakeHandleOwner struct{ w unsafe.Pointer }

// The handle comes out of a pointer to a one-field struct holding a
// pointer, and nothing else. A library that changed shape must make the
// hook decline, not read some other word.
func TestWebviewHandleReadsOnlyTheExpectedShape(t *testing.T) {
	x := 7
	good := &fakeHandleOwner{w: unsafe.Pointer(&x)}
	p, ok := webviewHandle(good)
	if !ok || p != unsafe.Pointer(&x) {
		t.Errorf("webviewHandle(good) = %v, %v; want the stored pointer", p, ok)
	}

	type twoFields struct {
		w unsafe.Pointer
		n int
	}
	type wrongKind struct{ w int }
	var up unsafe.Pointer
	str := "webview"
	cases := map[string]any{
		"nil":             nil,
		"nil pointer":     (*fakeHandleOwner)(nil),
		"not a pointer":   fakeHandleOwner{w: unsafe.Pointer(&x)},
		"two fields":      &twoFields{w: unsafe.Pointer(&x)},
		"field not a ptr": &wrongKind{w: 3},
		"null handle":     &fakeHandleOwner{},
		"a string":        "webview",
		// A pointer is not enough: what it points at must be the struct.
		// Reading a field of an int is a panic inside Launch, where the
		// contract is to decline and leave the popups as they were.
		"pointer to int":     &x,
		"pointer to pointer": &up,
		"pointer to string":  &str,
	}
	for name, v := range cases {
		if p, ok := webviewHandle(v); ok {
			t.Errorf("webviewHandle(%s) = %v, true; want it declined", name, p)
		}
	}
}

// webviewModuleDir is where the pinned webview_go is on this machine.
func webviewModuleDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/webview/webview_go").Output()
	if err != nil {
		t.Skipf("cannot locate the webview_go module (%v)", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Skip("go list gave no directory for webview_go")
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// vtableMembers lists a COM interface's methods in slot order, as
// WebView2.h declares them in the interface's C vtable.
func vtableMembers(t *testing.T, header, iface string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?s)typedef struct ` + iface + `Vtbl\s*\{(.*?)\}\s*` + iface + `Vtbl;`)
	m := re.FindStringSubmatch(header)
	if m == nil {
		t.Fatalf("WebView2.h has no vtable for %s", iface)
	}
	members := regexp.MustCompile(`\(\s*STDMETHODCALLTYPE\s*\*\s*(\w+)\s*\)`).FindAllStringSubmatch(m[1], -1)
	out := make([]string, 0, len(members))
	for _, x := range members {
		out = append(out, x[1])
	}
	return out
}

// Every number the Windows hook calls COM by, against the header the pinned
// webview_go ships. A wrong slot calls a different method with the same
// arguments, which on a user's desktop is a crash or a popup that was meant
// to be gone.
func TestComSlotsMatchTheShippedHeader(t *testing.T) {
	dir := webviewModuleDir(t)
	header := readFile(t, filepath.Join(dir, "libs", "mswebview2", "include", "WebView2.h"))

	check := func(iface, method string, want int) {
		t.Helper()
		members := vtableMembers(t, header, iface)
		if want >= len(members) || members[want] != method {
			got := "(out of range)"
			if want < len(members) {
				got = members[want]
			}
			t.Errorf("%s slot %d is %s in WebView2.h, want %s", iface, want, got, method)
		}
	}
	check("ICoreWebView2Controller", "get_CoreWebView2", slotControllerGetCoreWebView2)
	check("ICoreWebView2", "add_NewWindowRequested", slotWebViewAddNewWindowRequested)
	check("ICoreWebView2NewWindowRequestedEventArgs", "get_Uri", slotNewWindowArgsGetURI)
	check("ICoreWebView2NewWindowRequestedEventArgs", "put_Handled", slotNewWindowArgsPutHandled)
	check("ICoreWebView2NewWindowRequestedEventHandler", "Invoke", slotHandlerInvoke)

	iid := regexp.MustCompile(`IID_ICoreWebView2NewWindowRequestedEventHandler = \{0x([0-9a-f]+),0x([0-9a-f]+),0x([0-9a-f]+),\{((?:0x[0-9a-f]+,?)+)\}\}`).FindStringSubmatch(header)
	if iid == nil {
		t.Fatal("WebView2.h does not define IID_ICoreWebView2NewWindowRequestedEventHandler")
	}
	tail := strings.NewReplacer("0x", "", ",", "").Replace(iid[4])
	got := strings.ToUpper("{" + pad(iid[1], 8) + "-" + pad(iid[2], 4) + "-" + pad(iid[3], 4) + "-" + tail[:4] + "-" + tail[4:] + "}")
	if got != newWindowHandlerIID {
		t.Errorf("handler IID in WebView2.h is %s, newWindowHandlerIID is %s", got, newWindowHandlerIID)
	}
}

func pad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}

// The value passed to webview_get_native_handle for the browser controller,
// and the shape webviewHandle depends on, against the library source.
func TestWebviewLibraryMatchesWhatTheHookAssumes(t *testing.T) {
	dir := webviewModuleDir(t)

	h := readFile(t, filepath.Join(dir, "libs", "webview", "include", "webview.h"))
	enum := regexp.MustCompile(`(?s)typedef enum \{(.*?)\} webview_native_handle_kind_t;`).FindStringSubmatch(h)
	if enum == nil {
		t.Fatal("webview.h has no webview_native_handle_kind_t")
	}
	kinds := regexp.MustCompile(`WEBVIEW_NATIVE_HANDLE_KIND_\w+`).FindAllString(enum[1], -1)
	if nativeHandleBrowserController >= len(kinds) || kinds[nativeHandleBrowserController] != "WEBVIEW_NATIVE_HANDLE_KIND_BROWSER_CONTROLLER" {
		t.Errorf("handle kind %d is not the browser controller in webview.h: %v", nativeHandleBrowserController, kinds)
	}

	goSrc := readFile(t, filepath.Join(dir, "webview.go"))
	if !regexp.MustCompile(`(?s)type webview struct \{\s*w C\.webview_t\s*\}`).MatchString(goSrc) {
		t.Error("webview_go's webview struct is no longer a single C.webview_t field, so webviewHandle reads the wrong thing")
	}
	if !regexp.MustCompile(`void \*webview_get_native_handle\(`).MatchString(h) {
		t.Error("webview.h no longer declares webview_get_native_handle")
	}
}

// The hook reads an unexported field of a library whose private shape is
// not promised. When this fails, the module was bumped: reread webview.go
// and WebView2.h (the two tests above do the mechanical part), then run the
// click check on a Windows machine, which is the only thing that exercises
// the real window, and only then change the pin here.
func TestWebviewVersionIsTheOneTheHookWasCheckedAgainst(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/webview/webview_go").Output()
	if err != nil {
		t.Skipf("cannot read the webview_go version (%v)", err)
	}
	const checked = "v0.0.0-20240831120633-6173450d4dd6"
	if got := strings.TrimSpace(string(out)); got != checked {
		t.Errorf("webview_go is %s, the new-window hook was checked against %s", got, checked)
	}
}

// parseSource parses one of this package's files as text. The files behind
// a build tag are parsed the same: the tag only decides whether the compiler
// sees them.
func parseSource(t *testing.T, name string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return fset, f
}

func funcDecl(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// The hook does nothing until Launch asks for it, and nothing off Windows
// notices when it stops asking: the call is an empty function there, and no
// test opens a window. This is the one place that says the call is made, to
// the window, unconditionally, and before the first page is loaded (a page
// that asks for a new window before the handler is in is answered by the
// popup).
func TestLaunchInstallsTheNewWindowHookBeforeNavigating(t *testing.T) {
	fset, f := parseSource(t, "gui.go")
	launch := funcDecl(f, "Launch")
	if launch == nil || launch.Body == nil {
		t.Fatal("gui.go has no Launch; this test no longer reads what it thinks it reads")
	}

	var hook token.Pos
	for _, stmt := range launch.Body.List {
		es, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := es.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "routeNewWindows" {
			continue
		}
		if len(call.Args) != 1 {
			t.Fatalf("Launch calls routeNewWindows with %d arguments, want the window", len(call.Args))
		}
		if arg, ok := call.Args[0].(*ast.Ident); !ok || arg.Name != "w" {
			t.Fatalf("Launch passes %T to routeNewWindows, want the window w", call.Args[0])
		}
		hook = call.Pos()
		break
	}
	if !hook.IsValid() {
		t.Fatal("Launch does not call routeNewWindows(w) as one of its own statements, so the window opens popups again on Windows")
	}

	var navigate token.Pos
	ast.Inspect(launch.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Navigate" {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "w" && (!navigate.IsValid() || call.Pos() < navigate) {
			navigate = call.Pos()
		}
		return true
	})
	if !navigate.IsValid() {
		t.Fatal("Launch has no w.Navigate; this test no longer reads what it thinks it reads")
	}
	if hook > navigate {
		t.Errorf("Launch calls routeNewWindows (%s) after w.Navigate (%s)", fset.Position(hook), fset.Position(navigate))
	}
}

// comCall is handed the address of a local as an integer. The compiler keeps
// such a local on the heap, and alive across the call, only when the
// function taking it says so with //go:uintptrescapes. Without that the
// local stays in the goroutine's stack, the stack can be moved before the
// COM method writes through the integer, and the write is lost: no
// controller, or no address, and nothing says so but a line in a log. The
// compiler cannot be run for Windows here, so this reads the source.
func TestComCallPinsItsPointerArguments(t *testing.T) {
	_, f := parseSource(t, "newwindow_windows.go")

	hasDirective := func(fn *ast.FuncDecl) bool {
		if fn == nil || fn.Doc == nil {
			return false
		}
		for _, c := range fn.Doc.List {
			if c.Text == "//go:uintptrescapes" {
				return true
			}
		}
		return false
	}
	// uintptr(unsafe.Pointer(&x)): an address converted to an integer.
	isAddressAsInteger := func(e ast.Expr) bool {
		conv, ok := e.(*ast.CallExpr)
		if !ok || len(conv.Args) != 1 {
			return false
		}
		if id, ok := conv.Fun.(*ast.Ident); !ok || id.Name != "uintptr" {
			return false
		}
		inner, ok := conv.Args[0].(*ast.CallExpr)
		if !ok || len(inner.Args) != 1 {
			return false
		}
		if sel, ok := inner.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Pointer" {
			return false
		}
		u, ok := inner.Args[0].(*ast.UnaryExpr)
		return ok && u.Op == token.AND
	}

	sites := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range call.Args {
			if !isAddressAsInteger(arg) {
				continue
			}
			sites++
			callee, ok := call.Fun.(*ast.Ident)
			if !ok {
				t.Errorf("an address is passed as an integer to %T; only a function of this file with //go:uintptrescapes keeps it valid", call.Fun)
				break
			}
			if !hasDirective(funcDecl(f, callee.Name)) {
				t.Errorf("%s is passed the address of a local as an integer and lacks //go:uintptrescapes, so a moved stack loses the write", callee.Name)
			}
			break
		}
		return true
	})
	if sites < 3 {
		t.Errorf("found %d calls passing an address as an integer, want at least 3 (get_CoreWebView2, add_NewWindowRequested, get_Uri); this test no longer reads what it thinks it reads", sites)
	}
}
