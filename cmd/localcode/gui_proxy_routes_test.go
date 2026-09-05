package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Every route the Web UI calls, against a window that has handed its
// daemon to another process.
//
// This is the test the folder button needed and did not have. After an
// update the window serves the page's requests through successorProxy,
// and that mode had never been exercised end to end: it only became
// reachable at all in v0.101.0, when the successor stopped dying seconds
// after it started. The first thing anybody clicked in it was broken,
// because the proxy's copy of a route had drifted from the daemon's.
//
// So: a real daemon behind a real proxy, and every URL in
// internal/daemon/static/js/api.js driven through it. The point is not
// what each one returns but that none of them reaches a handler that no
// longer matches what the page sends. The coverage guard below keeps the
// two lists together, so a route added to the page cannot quietly go
// untested here.

// probe is one request the page makes. ok lists the answers that mean it
// reached a handler which understood it; anything else is drift.
//
// 404 is never an ok answer for a route that exists: through this proxy
// it means the window swallowed the path or never forwarded it. Where an
// id is invented, "the daemon says no such thing" IS the pass — the
// request got to the daemon — so those carry 400/404 deliberately, with
// a note saying which.
type probe struct {
	method, path, body string
	ok                 []int
	note               string
}

func TestEveryRouteThePageCallsWorksThroughTheWindowProxy(t *testing.T) {
	front, sid, schedID, work, dialogs := windowOnAProxy(t)

	call := func(method, path, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, front+path, strings.NewReader(body))
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

	only := func(codes ...int) []int { return codes }
	probes := []probe{
		{method: "GET", path: "/api/agents", ok: only(200)},
		{method: "GET", path: "/api/commands", ok: only(200)},
		{method: "GET", path: "/api/skills", ok: only(200)},
		{method: "GET", path: "/api/slash-commands", ok: only(200)},
		{method: "GET", path: "/api/settings", ok: only(200)},
		{method: "GET", path: "/api/version", ok: only(200)},
		{method: "GET", path: "/api/mcp-servers", ok: only(200)},
		{method: "GET", path: "/api/sessions", ok: only(200)},
		{method: "GET", path: "/api/sessions?archived=1", ok: only(200)},
		{method: "GET", path: "/api/sessions/" + sid + "/schedules", ok: only(200)},
		{method: "GET", path: "/api/sessions/" + sid + "/permissions", ok: only(200)},

		// The three the window keeps for itself, because they open a
		// native dialog and a dialog belongs to the window somebody is
		// looking at rather than to a daemon with no window.
		{method: "GET", path: "/api/workspace?session=" + sid, ok: only(200)},
		{method: "POST", path: "/api/workspace/browse", body: `{"start":"` + jsonPath(work) + `"}`, ok: only(200, 204)},
		{method: "POST", path: "/api/workspace/reveal?session=" + sid, body: `{}`, ok: only(200)},
		{method: "POST", path: "/api/workspace", body: `{"path":"` + jsonPath(work) + `","session_id":"` + sid + `"}`, ok: only(200)},

		{method: "POST", path: "/api/settings/keep-going", body: `{"enabled":false}`, ok: only(200, 204)},
		{method: "POST", path: "/api/settings/repeat-limit", body: `{"limit":0}`, ok: only(200, 204)},
		{method: "POST", path: "/api/settings/smart-agent", body: `{"enabled":false}`, ok: only(200, 204)},
		{method: "POST", path: "/api/settings/orchestrate", body: `{"enabled":false}`, ok: only(200, 204)},
		{method: "POST", path: "/api/settings/model-invocable", body: `{"enabled":false}`, ok: only(200, 204)},
		{method: "POST", path: "/api/settings/auto-delegate", body: `{"enabled":false}`, ok: only(200, 204)},
		{method: "POST", path: "/api/permissions/skip", body: `{"enabled":false}`, ok: only(200, 204)},
		{method: "POST", path: "/api/permissions/rules", body: `{"tool":"bash","match":"echo *","decision":"allow"}`, ok: only(200, 204)},
		{method: "POST", path: "/api/permissions/rules/remove", body: `{"tool":"bash","match":"echo *","decision":"allow"}`, ok: only(200, 204)},

		{method: "POST", path: "/api/schedules/preview", body: `{"when":"tomorrow 9am"}`, ok: only(200)},
		{method: "POST", path: "/api/sessions/" + sid + "/permissions", body: `{"switch":"skip_tools","enabled":false}`, ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/permissions/forget", body: `{"class":"read"}`, ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/agent", body: `{"agent":"general-purpose"}`, ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/rename", body: `{"title":"renamed"}`, ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/cancel", body: `{}`, ok: only(200, 204, 409)},
		{method: "POST", path: "/api/sessions/order", body: `{"ids":["` + sid + `"]}`, ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/messages", body: `{"text":"hello"}`,
			ok:   only(200, 202),
			note: "the turn fails later against a provider that is not there; being accepted is the route working"},

		{method: "POST", path: "/api/sessions/" + sid + "/schedules/" + schedID + "/rename", body: `{"name":"nightly"}`, ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/schedules/" + schedID + "/seen", body: `{}`, ok: only(200, 204)},

		// Invented ids. Reaching the daemon and being told there is no
		// such request, task or answer is the pass: a 502, or a 404 from
		// the proxy's own mux, would not be.
		{method: "POST", path: "/api/sessions/" + sid + "/permissions/p-nope", body: `{"allow":false,"scope":"once"}`,
			ok: only(200, 204, 400, 404, 409), note: "no such pending request"},
		{method: "POST", path: "/api/sessions/" + sid + "/input/a-nope", body: `{"answer":"x"}`,
			ok: only(200, 204, 400, 404, 409), note: "no such question"},
		{method: "POST", path: "/api/tasks/t-nope/cancel", body: `{}`,
			ok: only(200, 204, 400, 404), note: "no such task"},

		// Reachable and refused is a pass: it proves the route is the
		// daemon's rather than something the window ate. This daemon is
		// not one a client may install an update on.
		{method: "GET", path: "/api/update", ok: only(200, 500, 502, 503), note: "asks GitHub; offline is fine"},
		{method: "POST", path: "/api/update/install", ok: only(403)},

		// Destructive, so last, and in this order: the schedule belongs
		// to the session, and the session to the list.
		{method: "DELETE", path: "/api/sessions/" + sid + "/schedules/" + schedID, ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/archive", ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/retrieve", ok: only(200, 204)},
		{method: "POST", path: "/api/sessions/" + sid + "/fork", ok: only(200, 201)},
		{method: "DELETE", path: "/api/sessions/" + sid, ok: only(200, 204)},
		{method: "DELETE", path: "/api/sessions", ok: only(200, 204)},
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
			where := p.method + " " + p.path
			if p.note != "" {
				where += " (" + p.note + ")"
			}
			t.Errorf("%s: %d %s (want one of %v)", where, status, strings.TrimSpace(body), p.ok)
		}
	}

	// The two dialogs really were the window's, and the folder button
	// asked for the session's own workspace rather than anything a caller
	// put in the body.
	if *dialogs.picked == "" {
		t.Error("the folder picker was never opened by the window")
	}
	if *dialogs.revealed != work {
		t.Errorf("the folder button opened %q, want the session's workspace %q", *dialogs.revealed, work)
	}

	assertCoversAPIJS(t, probes, sid, schedID, "p-nope", "a-nope", "t-nope")
}

// assertCoversAPIJS keeps the table above and the page's own list of URLs
// from drifting apart.
//
// api.js is the only place the Web UI builds an /api/... string, which is
// what makes this checkable: a route added there and not probed here is a
// route that goes through the window untested, and that is exactly the
// gap the folder button fell into.
func assertCoversAPIJS(t *testing.T, probes []probe, ids ...string) {
	t.Helper()
	// From this file's own path, not the working directory: the test
	// moves into a temporary workspace, which is the whole point of it.
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(here)))
	src, err := os.ReadFile(filepath.Join(root, "internal", "daemon", "static", "js", "api.js"))
	if err != nil {
		t.Fatalf("read api.js: %v", err)
	}
	call := regexp.MustCompile("api\\(\\s*'(GET|POST|DELETE|PUT|PATCH)'\\s*,\\s*(`[^`]*`|'[^']*')")
	interp := regexp.MustCompile(`\$\{[^}]*\}`)

	probed := map[string]bool{}
	for _, p := range probes {
		probed[p.method+" "+pattern(p.path, ids)] = true
	}
	// Driven in windowOnAProxy rather than in the table, because the rest
	// of the table needs what they return.
	probed["POST /api/sessions"] = true
	probed["POST /api/sessions/{}/schedules"] = true

	var missing []string
	for _, m := range call.FindAllStringSubmatch(string(src), -1) {
		method, raw := m[1], strings.Trim(m[2], "`'")
		want := method + " " + pattern(interp.ReplaceAllString(raw, "{}"), nil)
		if !probed[want] {
			missing = append(missing, want)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the page calls %d route(s) this test never drives through the window proxy:\n  %s\n"+
			"Add a probe for each, or the window can break them the way it broke the folder button.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// pattern reduces a concrete URL to the shape api.js writes: no query,
// and each id this test is holding replaced by the placeholder an
// interpolation leaves behind.
//
// The ids are passed in rather than recognised by shape. A session is
// "s-1788…" and a schedule is "s1", which no single rule separates from a
// path segment the page writes literally — and a rule that guessed wrong
// would either hide a gap or invent one.
func pattern(p string, ids []string) string {
	p = strings.SplitN(p, "?", 2)[0]
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		for _, id := range ids {
			if seg == id {
				parts[i] = "{}"
				break
			}
		}
	}
	return strings.Join(parts, "/")
}

// jsonPath makes a filesystem path safe to paste into a JSON string,
// which on Windows means the separators.
func jsonPath(p string) string {
	b, _ := json.Marshal(p)
	return strings.Trim(string(b), `"`)
}

type openedDialogs struct{ picked, revealed *string }

// windowOnAProxy builds the situation under test: a real daemon in the
// position of the successor, and a real successorProxy in the position of
// the window in front of it. Returns the front's URL, a session and a
// schedule to address the per-id routes to, the workspace, and what the
// native dialogs were asked to open.
func windowOnAProxy(t *testing.T) (front, sessionID, scheduleID, workspace string, dialogs openedDialogs) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := t.TempDir()
	t.Chdir(work)

	// A provider that is configured and never answers: nothing here needs
	// a model, and a turn that cannot connect is still a turn the daemon
	// accepted.
	cfgDir := filepath.Join(home, ".localcode")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
	  "providers": {"local": {"type": "openai-compat", "base_url": "http://127.0.0.1:1/v1", "api_key": "x"}},
	  "profiles": {"local": {"provider": "local", "model": "m", "max_tokens": 256}},
	  "default_profile": "local",
	  "auto_update": false,
	  "agents": {"general-purpose": {"profile": "local", "description": "the one this test switches to"}}
	}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	d, cleanup, err := buildDaemon(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("build the daemon: %v", err)
	}
	t.Cleanup(cleanup)

	// The successor: a daemon in another process, which is why it has no
	// dialogs of its own. Leaving these nil is the point — the proxy is
	// what makes the page believe the window still has them.
	backend := httptest.NewServer(d.Handler())
	t.Cleanup(backend.Close)

	// No window opens on the machine running the suite.
	var picked, revealed string
	restorePick, restoreReveal := pickDirectory, revealDirectory
	pickDirectory = func(_ context.Context, _, start string) (string, error) { picked = start; return work, nil }
	revealDirectory = func(_ context.Context, dir string) error { revealed = dir; return nil }
	t.Cleanup(func() { pickDirectory, revealDirectory = restorePick, restoreReveal })

	f := httptest.NewServer(successorProxy(strings.TrimPrefix(backend.URL, "http://")))
	t.Cleanup(f.Close)

	// The two answers have the id in different places: a session is the
	// object, a booking is wrapped in one.
	post := func(path, body string, at ...string) string {
		t.Helper()
		resp, err := http.Post(f.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var got map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("POST %s: %s: %v", path, resp.Status, err)
		}
		for _, key := range at {
			nested, ok := got[key].(map[string]any)
			if !ok {
				t.Fatalf("POST %s: %s has no %q object: %v", path, resp.Status, key, got)
			}
			got = nested
		}
		id, _ := got["id"].(string)
		if id == "" {
			t.Fatalf("POST %s: %s gave no id: %v", path, resp.Status, got)
		}
		return id
	}

	sid := post("/api/sessions", `{"agent":"general-purpose"}`)
	schedID := post("/api/sessions/"+sid+"/schedules", `{"when":"tomorrow 9am","prompt":"run make check"}`, "schedule")

	return f.URL, sid, schedID, work, openedDialogs{picked: &picked, revealed: &revealed}
}
