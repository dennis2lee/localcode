//go:build gui

package gui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// logoSVG is the application icon, the same artwork the installer puts on
// the desktop, shown on the splash screen so the window that opens is
// recognisably the thing that was clicked. logo_test.go fails the build
// if this copy drifts from build/icon/icon.svg.
//
//go:embed logo.svg
var logoSVG string

// splashHTML is the first thing the window shows, before there is a
// daemon to point it at.
//
// It exists because the window used to take several seconds to appear.
// Starting a daemon means reading config, opening providers, loading
// every session from disk and — the slow part — spawning and handshaking
// with each configured MCP server. All of that happened before the
// window was created, so from outside the app there was nothing at all:
// no window, no icon bouncing, no error. The rational response to that
// is to click the icon again, and now two of them are starting.
//
// So the window comes up immediately with this, and the work reports
// into it. The status line matters more than the logo: "starting" that
// sits unchanged for five seconds is only marginally better than
// nothing, whereas naming the MCP server currently being connected
// makes a slow start legible rather than suspect.
//
// It is a self-contained document with no external references. There is
// no server yet — that is the whole point — so there is nothing to fetch
// a stylesheet or an image from.
func splashHTML(version string) string {
	v := ""
	if version != "" && version != "dev" {
		v = "v" + version
	}
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>LocalCode</title>
<style>
  :root { color-scheme: dark; }
  html, body { height: 100%; margin: 0; }
  body {
    background: #181818;
    color: #e6edf3;
    font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 18px;
    /* The splash is replaced by a navigation, not dismissed by the
       user, so nothing here is interactive and nothing should look
       like it is. */
    user-select: none;
    cursor: default;
  }
  .logo { width: 112px; height: 112px; flex: none; }
  /* The icon is authored at 256px for the installer, and carries width
     and height attributes to say so. Inlined here those attributes win
     over the box it is sitting in, so it drew at full size and overlapped
     the name and status line underneath. Sizing the element itself is
     what overrides them. */
  .logo svg { display: block; width: 100%; height: 100%; }
  h1 { font-size: 22px; font-weight: 600; margin: 0; letter-spacing: 0.01em; }
  .version { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; color: #8b949e; }
  .row { display: flex; align-items: baseline; gap: 9px; }
  /* Fixed height and a fixed width so a longer message does not shift
     the logo above it; a splash that jitters while it loads looks less
     finished than one that does not. */
  #status {
    color: #8b949e;
    font-size: 12px;
    height: 16px;
    max-width: 420px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    text-align: center;
  }
  /* An indeterminate bar, not a percentage: the steps take wildly
     different amounts of time (one unreachable MCP server outlasts
     everything else put together), so any number would be a lie. This
     only claims that something is still happening. */
  .track { width: 220px; height: 3px; background: #2a2a2a; border-radius: 2px; overflow: hidden; }
  .track i { display: block; width: 40%; height: 100%; background: #58a6ff; border-radius: 2px; animation: slide 1.3s ease-in-out infinite; }
  @keyframes slide { 0% { transform: translateX(-100%); } 100% { transform: translateX(250%); } }
  @media (prefers-reduced-motion: reduce) {
    .track i { animation: none; width: 100%; opacity: 0.5; }
  }
  /* Failure is shown here rather than thrown away. This binary is
     linked -H windowsgui and has no console, so a startup error printed
     to stderr goes nowhere at all — the window would simply never
     appear, which is exactly the symptom the splash exists to remove. */
  body.failed .track { display: none; }
  body.failed #status { color: #f85149; white-space: pre-wrap; height: auto; text-align: left; }
</style>
</head>
<body>
  <div class="logo">` + logoSVG + `</div>
  <div class="row"><h1>LocalCode</h1><span class="version">` + v + `</span></div>
  <div id="status">starting</div>
  <div class="track"><i></i></div>
<script>
  // Called from Go, over the webview's Eval channel, as each step
  // begins. Guarded because the first message can race the document:
  // Eval on a page that has not finished parsing would otherwise throw
  // into a console nobody is reading.
  window.lcStatus = (text) => {
    const el = document.getElementById('status');
    if (el) el.textContent = text;
  };
  window.lcFailed = (text) => {
    document.body.classList.add('failed');
    window.lcStatus(text);
  };
</script>
</body>
</html>`
}

// dataURL wraps a document so Navigate can show it without a server.
// SetHtml would be the obvious call, but on WebView2 it lands before the
// controller is ready often enough to matter, and a navigation is the
// path every other page here takes.
func dataURL(html string) string {
	// Percent-encoding rather than base64: it survives being read in a
	// log or a debugger, and the document is small enough that the size
	// difference is irrelevant.
	var b strings.Builder
	b.WriteString("data:text/html;charset=utf-8,")
	for i := 0; i < len(html); i++ {
		c := html[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// jsCall builds a JavaScript call with a properly quoted argument. The
// strings reaching it include file paths and error text from arbitrary
// tools, and a raw quote or newline in one of those would otherwise turn
// a status update into a syntax error — losing every later update too,
// because the failure is silent.
func jsCall(fn, arg string) string {
	q, err := json.Marshal(arg)
	if err != nil {
		return ""
	}
	return "window." + fn + " && " + fn + "(" + string(q) + ")"
}
