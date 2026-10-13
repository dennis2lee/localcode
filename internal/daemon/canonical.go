package daemon

import (
	"fmt"
	"net/http"
	"path"
	"strings"
)

// canonicalWrites refuses a request that changes something when its path
// is not in canonical form, rather than letting the mux redirect it.
//
// The mux answers a path with "." or ".." segments, or a doubled slash,
// with a redirect to the cleaned path, and an HTTP client follows that
// redirect with the method it started with. So
// DELETE /api/sessions/{id}/tasks/.. arrived as DELETE /api/sessions/{id}
// and deleted the conversation, not a task: a request aimed at one route
// was carried to another, more destructive one. A read redirected is the
// same read; a write redirected is a different write, so writes are
// answered 400 instead.
func canonicalWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if p := r.URL.EscapedPath(); cleanPath(p) != p {
				writeError(w, http.StatusBadRequest, fmt.Errorf("%s %s: the path is not in canonical form", r.Method, p))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// cleanPath is the form the mux redirects to: net/http's own rule, a
// cleaned path that keeps its trailing slash. The mux applies it to the
// escaped path, so "%2e%2e" is a segment and not a step up.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	if strings.HasSuffix(p, "/") && np != "/" {
		np += "/"
	}
	return np
}
