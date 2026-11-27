package daemon

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// RefuseOtherSites turns away, on every route, a request that a page from
// another site could have made, and one sent through a name that is not
// this machine's.
//
// The daemon has no authentication, and it listens where the person's
// everyday browser can reach it. Without this, any page they opened could
// send it a request: a form POST, or a fetch in no-cors mode, needs no
// preflight, so a page could turn every permission prompt off or start a
// turn, and it would run. A page whose name the attacker points at
// 127.0.0.1 (DNS rebinding) could also read the answers, because the
// browser takes it for the daemon's own origin. The browser tells the
// three cases apart, and this reads what it says:
//
//   - the host the request was sent to must be an address, localhost or a
//     name under .localhost, this machine's own name, or the name the
//     daemon was started to listen on (listenHost). A rebinding page's
//     requests carry the attacker's name.
//   - Sec-Fetch-Site, when the browser sends it, must be same-origin or
//     none. One exception: opening a page of the Web UI itself in a tab
//     from a link on another site, which reads nothing and changes
//     nothing. The API and a frame are refused.
//   - Origin, when present, must name the host the request was sent to.
//     The scheme is not compared, so a TLS proxy in front still works.
//     "null" (a sandboxed frame, a file: or data: page) names none.
//
// A client that is not a browser (the TUI, `localcode run`, curl) sends
// neither header and is let through on the first rule alone. The desktop
// window's page is the daemon's own origin, and its proxy after an update
// passes the window's Host and Origin through unchanged.
//
// Exported for that proxy: its own routes (the native folder dialogs) are
// answered in front of the daemon and need the same refusal.
func RefuseOtherSites(listenHost func() string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listen := ""
		if listenHost != nil {
			listen = listenHost()
		}
		if why := otherSite(r, listen); why != "" {
			writeError(w, http.StatusForbidden, errors.New("refused: "+why))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// otherSite says why r is one RefuseOtherSites turns away, or "".
func otherSite(r *http.Request, listenHost string) string {
	if host := requestHost(r); !ownHost(host, listenHost) {
		return "a request sent to " + host + ", a name this daemon was not started on"
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" && !openingAPage(r) {
		return "a request from another site"
	}
	if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r) {
		return "a request from another origin"
	}
	return ""
}

// openingAPage is a tab opened on a page of the Web UI: a top-level
// navigation that reads a static file. Another site's link may do that;
// the page it opens then asks the API as its own origin.
func openingAPage(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		r.Header.Get("Sec-Fetch-Mode") == "navigate" &&
		r.Header.Get("Sec-Fetch-Dest") == "document" &&
		!strings.HasPrefix(r.URL.Path, "/api/")
}

// sameOrigin is whether the Origin header o names the host r was sent to.
func sameOrigin(o string, r *http.Request) bool {
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// requestHost is the host r was sent to, without its port or brackets.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// machineName is this machine's own name; a variable for tests.
var machineName = os.Hostname

// ownHost is whether host names this machine: an address, localhost or a
// name under it, the machine's own name (with or without .local), or the
// name the daemon was started to listen on. An empty host (HTTP/1.0) is
// one no browser sends.
func ownHost(host, listenHost string) bool {
	if host == "" || net.ParseIP(host) != nil {
		return true
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if l := strings.ToLower(strings.TrimSuffix(listenHost, ".")); l != "" && h == l {
		return true
	}
	if name, err := machineName(); err == nil && name != "" {
		name = strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(name, "."), ".local"))
		if h == name || h == name+".local" {
			return true
		}
	}
	return false
}
