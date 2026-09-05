package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/dialog"
)

// Every route the Web UI calls, against a window that has handed its
// daemon to another process.
//
// This is the test the folder button needed and did not have. The window
// serves the page's requests through successorProxy after an update, and
// that mode had never been exercised end to end: it only became reachable
// at all in v0.101.0, when the successor stopped dying seconds after it
// started. The first thing anybody clicked in it was broken, because the
// proxy's copy of a route had drifted from the daemon's.
//
// So: a real daemon behind a real proxy, and every URL in
// internal/daemon/static/js/api.js driven through it. The point is not
// what each one returns but that none of them reaches a handler that no
// longer matches what the page sends.
func TestEveryRouteThePageCallsWorksThroughTheWindowProxy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := t.TempDir()
	t.Chdir(work)

	// A provider that is configured and never reached: nothing here sends
	// a prompt anywhere, and a turn that fails to connect is still a turn
	// the daemon accepted.
	cfgDir := filepath.Join(home, ".localcode")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
	  "providers": {"local": {"type": "openai-compat", "base_url": "http://127.0.0.1:1/v1", "api_key": "x"}},
	  "profiles": {"local": {"provider": "local", "model": "m", "max_tokens": 256}},
	  "default_profile": "local",
	  "agents": {"general-purpose": {"profile": "local", "description": "the one this test switches to"}}
	}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	d, cleanup, err := buildDaemon(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("build the daemon: %v", err)
	}
	defer cleanup()

	// The successor: a daemon in another process, which is why it has no
	// dialogs of its own. Leaving these nil is the point — the proxy is
	// what makes the page believe the window still has them.
	backend := httptest.NewServer(d.Handler())
	defer backend.Close()

	// No window opens on the machine running the suite.
	var picked, revealed string
	restorePick, restoreReveal := pickDirectory, revealDirectory
	pickDirectory = func(_ context.Context, _, start string) (string, error) { picked = start; return work, nil }
	revealDirectory = func(_ context.Context, dir string) error { revealed = dir; return nil }
	defer func() { pickDirectory, revealDirectory = restorePick, restoreReveal }()

	front := httptest.NewServer(successorProxy(strings.TrimPrefix(backend.URL, "http://")))
	defer front.Close()

	call := func(method, path, body string) (int, string) {
		t.Helper()
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req, err := http.NewRequest(method, front.URL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	// One conversation to address the per-session routes to.
	status, body := call("POST", "/api/sessions", `{"agent":"general-purpose"}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create a session: %d %s", status, body)
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &made); err != nil || made.ID == "" {
		t.Fatalf("the new session had no id: %s", body)
	}
	sid := made.ID

	// Whatever agents this config actually has: the roster is built from
	// config.json, so hard-coding a name here would test the fixture
	// rather than the route.
	firstAgent := "general-purpose"
	if _, list := call("GET", "/api/agents", ""); list != "" {
		var agents []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(list), &agents) == nil && len(agents) > 0 {
			firstAgent = agents[0].Name
		}
	}

	// Every URL api.js builds. ok lists the answers that mean the request
	// reached a handler that understood it; anything else is the drift
	// this test exists to catch. 404 is never in an ok list: through this
	// proxy it means a route the window swallowed or never forwarded.
	type probe struct {
		method, path, body string
		ok                 []int
	}
	only := func(codes ...int) []int { return codes }
	probes := []probe{
		{"GET", "/api/agents", "", only(200)},
		{"GET", "/api/commands", "", only(200)},
		{"GET", "/api/skills", "", only(200)},
		{"GET", "/api/slash-commands", "", only(200)},
		{"GET", "/api/settings", "", only(200)},
		{"GET", "/api/version", "", only(200)},
		{"GET", "/api/mcp-servers", "", only(200)},
		{"GET", "/api/sessions", "", only(200)},
		{"GET", "/api/sessions?archived=1", "", only(200)},
		{"GET", "/api/sessions/" + sid, "", only(200)},
		{"GET", "/api/sessions/" + sid + "/tasks", "", only(200)},
		{"GET", "/api/sessions/" + sid + "/schedules", "", only(200)},
		{"GET", "/api/sessions/" + sid + "/permissions", "", only(200)},
		{"GET", "/api/trace", "", only(200)},

		// The three the window keeps for itself.
		{"GET", "/api/workspace?session=" + sid, "", only(200)},
		{"POST", "/api/workspace/browse", `{"start":"` + jsonPath(work) + `"}`, only(200, 204)},
		{"POST", "/api/workspace/reveal?session=" + sid, `{}`, only(200)},
		{"POST", "/api/workspace", `{"path":"` + jsonPath(work) + `","session_id":"` + sid + `"}`, only(200)},

		{"POST", "/api/settings/keep-going", `{"enabled":false}`, only(200, 204)},
		{"POST", "/api/settings/repeat-limit", `{"limit":0}`, only(200, 204)},
		{"POST", "/api/settings/smart-agent", `{"enabled":false}`, only(200)},
		{"POST", "/api/settings/orchestrate", `{"enabled":false}`, only(200)},
		{"POST", "/api/settings/model-invocable", `{"enabled":false}`, only(200)},
		{"POST", "/api/settings/auto-delegate", `{"enabled":false}`, only(200, 204)},
		{"POST", "/api/permissions/skip", `{"enabled":false}`, only(200, 204)},
		{"POST", "/api/permissions/rules", `{"tool":"bash","match":"echo *","decision":"allow"}`, only(200, 204)},
		{"POST", "/api/permissions/rules/remove", `{"tool":"bash","match":"echo *","decision":"allow"}`, only(200, 204)},

		{"POST", "/api/schedules/preview", `{"when":"tomorrow 9am"}`, only(200)},
		{"POST", "/api/sessions/" + sid + "/permissions", `{"switch":"skip_tools","enabled":false}`, only(200, 204)},
		{"POST", "/api/sessions/" + sid + "/permissions/forget", `{"class":"read"}`, only(200, 204)},
		{"POST", "/api/sessions/" + sid + "/agent", `{"agent":"` + firstAgent + `"}`, only(200, 204)},
		{"POST", "/api/sessions/" + sid + "/rename", `{"title":"renamed"}`, only(200, 204)},
		{"POST", "/api/sessions/" + sid + "/cancel", `{}`, only(200, 409)},
		{"POST", "/api/sessions/order", `{"ids":["` + sid + `"]}`, only(200, 204)},

		// Reachable and refused is a pass: it proves the route is the
		// daemon's rather than something the proxy ate. This daemon is
		// not one a client may install an update on.
		{"GET", "/api/update", "", only(200, 500, 502, 503)},
		{"POST", "/api/update/install", "", only(403)},
	}

	for _, p := range probes {
		status, body := call(p.method, p.path, p.body)
		okd := false
		for _, c := range p.ok {
			if status == c {
				okd = true
			}
		}
		if !okd {
			t.Errorf("%s %s: %d %s (want one of %v)", p.method, p.path, status, strings.TrimSpace(body), p.ok)
		}
	}

	// The two dialogs really were the window's, and the folder button
	// asked for the session's own workspace rather than anything a caller
	// put in the body.
	if picked == "" {
		t.Error("the folder picker was never opened by the window")
	}
	if revealed != work {
		t.Errorf("the folder button opened %q, want the session's workspace %q", revealed, work)
	}

	// Destructive last, so everything above still had a session.
	for _, p := range []probe{
		{"POST", "/api/sessions/" + sid + "/archive", "", only(200, 204)},
		{"POST", "/api/sessions/" + sid + "/retrieve", "", only(200, 204)},
		{"POST", "/api/sessions/" + sid + "/fork", "", only(200, 201)},
		{"DELETE", "/api/sessions/" + sid, "", only(200, 204)},
		{"DELETE", "/api/sessions", "", only(200, 204)},
	} {
		status, body := call(p.method, p.path, p.body)
		okd := false
		for _, c := range p.ok {
			if status == c {
				okd = true
			}
		}
		if !okd {
			t.Errorf("%s %s: %d %s (want one of %v)", p.method, p.path, status, strings.TrimSpace(body), p.ok)
		}
	}
}

// jsonPath makes a filesystem path safe to paste into a JSON string,
// which on Windows means the separators.
func jsonPath(p string) string {
	b, _ := json.Marshal(p)
	return strings.Trim(string(b), `"`)
}

// Keep the import used on every platform.
var _ = fmt.Sprintf
var _ = dialog.Available
