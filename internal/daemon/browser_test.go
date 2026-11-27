package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/browser"
	"localcode/internal/browser/browsertest"
	"localcode/internal/config"
	"localcode/internal/events"
)

func browserDaemon(t *testing.T) (*Daemon, *httptest.Server, string) {
	t.Helper()
	d := newTestDaemon(t, "http://127.0.0.1:1")
	// A profile of the test's own, its name already chosen, so nothing is
	// copied from the person's own browser. Made before the browser's
	// close is registered, so it is removed after the browser has closed.
	dir := browsertest.ProfileDir(t)
	// The guard as cmd/localcode wires it.
	d.Browser = browser.NewManager(browser.Options{DataDir: dir, Block: agent.BrowserBlock(d.Loop)})
	t.Cleanup(d.Browser.Close)
	d.Loop.Config.SetBrowserRuntime(true)
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	// A workspace of its own, which is where its snapshots are saved.
	if _, err := d.Loop.Store.CreateSessionIn("b1", "", "general-purpose", t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	return d, srv, "b1"
}

// whenStarted sends a test's first request that starts the browser again
// while the answer is that the browser is still starting, as a person
// would click again, and returns the first other answer with its body
// still to read. The first start of the browser on a machine took up to
// 73 seconds on a CI runner (see browser.startFor), and a request waits
// for at most 45 of them.
func whenStarted(t *testing.T, send func() *http.Response) *http.Response {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for {
		resp := send()
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		starting := resp.StatusCode == http.StatusServiceUnavailable && strings.Contains(string(body), browser.ErrStarting.Error())
		if !starting || time.Now().After(deadline) {
			return resp
		}
		t.Logf("the browser is still starting; asking again")
	}
}

func postBrowserJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A daemon without a browser says so, rather than pretending.
func TestNoBrowserIsA404(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	d.Loop.Store.CreateSession("b1", "", "general-purpose", true)
	for _, path := range []string{"/browser", "/browser/stream"} {
		resp, err := http.Get(srv.URL + "/api/sessions/b1" + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
	if d.BrowserViewing("b1") {
		t.Error("a daemon with no browser says a GUI is watching it")
	}
}

// The frame stream is the GUI window watching: open, the conversation is
// watched; closed, it is not. It starts no browser while there is no tab.
func TestTheStreamIsWhatWatchingMeans(t *testing.T) {
	d, srv, id := browserDaemon(t)
	if d.BrowserViewing(id) {
		t.Fatal("watched before anything opened the stream")
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+id+"/browser/stream", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin") // as the panel's <img> sends it
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/x-mixed-replace") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !d.BrowserViewing(id) {
		t.Error("not watched while the stream is open")
	}
	if d.BrowserViewing("someone-else") {
		t.Error("another conversation counts as watched")
	}
	if d.Browser.Existing(id) != nil {
		t.Error("opening the stream opened a tab")
	}
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for d.BrowserViewing(id) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if d.BrowserViewing(id) {
		t.Error("still watched after the stream closed")
	}
}

// The person drives the page from the panel: the address bar opens it, a
// click lands on it, and the stream shows it.
func TestThePanelDrivesThePage(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := browser.Find(""); err != nil {
		t.Skip("no Chrome or Edge here")
	}
	d, srv, id := browserDaemon(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Panel</title><style>body{margin:0}</style>`+
			`<button style="width:200px;height:100px" onclick="document.title='pressed'">Press</button>`)
	}))
	defer site.Close()
	base := srv.URL + "/api/sessions/" + id + "/browser"

	resp := whenStarted(t, func() *http.Response {
		return postBrowserJSON(t, base+"/navigate", map[string]string{"url": site.URL})
	})
	var nav struct{ URL, Title string }
	json.NewDecoder(resp.Body).Decode(&nav)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || nav.Title != "Panel" {
		t.Fatalf("navigate: %d %+v", resp.StatusCode, nav)
	}

	// The stream sends a frame of the page.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/stream", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin") // as the panel's <img> sends it
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	gotFrame := make(chan bool, 1)
	go func() {
		r := bufio.NewReader(stream.Body)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "Content-Type: image/jpeg") {
				gotFrame <- true
				return
			}
		}
	}()

	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		resp := postBrowserJSON(t, base+"/input", map[string]any{"mouse": map[string]any{"type": typ, "x": 50, "y": 50, "button": "left", "clickCount": 1}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("input %s = %d", typ, resp.StatusCode)
		}
	}
	select {
	case <-gotFrame:
	case <-ctx.Done():
		t.Fatal("no frame on the stream")
	}

	state, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		HasTab bool   `json:"has_tab"`
		Title  string `json:"title"`
	}
	json.NewDecoder(state.Body).Decode(&st)
	state.Body.Close()
	if !st.HasTab || st.Title != "pressed" {
		t.Errorf("after the panel's click the state is %+v; want the button pressed", st)
	}

	// Deleting the conversation closes its tab.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/sessions/"+id, nil)
	del, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	// Closed in the conversation's turn, after the delete has answered.
	deadline := time.Now().Add(5 * time.Second)
	for d.Browser.Existing(id) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the deleted conversation's tab is still open")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The panel's address bar is held to the deny rules like the model: a
// denied page is never loaded, so it is never sent the profile's cookies.
func TestThePanelCannotOpenADeniedPage(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := browser.Find(""); err != nil {
		t.Skip("no Chrome or Edge here")
	}
	d, srv, id := browserDaemon(t)
	var hits []string
	var mu sync.Mutex
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Page</title>`)
	}))
	defer site.Close()
	d.Loop.Config.Permissions = config.Permissions{agent.BrowserToolName: config.ToolPermission{Rules: []config.PermissionRule{
		{Match: "*/blocked*", Decision: config.DecisionDeny},
	}}}
	resp := whenStarted(t, func() *http.Response {
		return postBrowserJSON(t, srv.URL+"/api/sessions/"+id+"/browser/navigate", map[string]string{"url": site.URL + "/blocked"})
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("the panel's navigate to a denied page = %d, want 403", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range hits {
		if h == "/blocked" {
			t.Errorf("the denied page was requested: %v", hits)
		}
	}
}

// With the setting off the panel's routes refuse, so nothing opens the
// browser and no stream makes a conversation count as watched.
func TestThePanelRoutesRefuseWithTheSettingOff(t *testing.T) {
	d, srv, id := browserDaemon(t)
	d.Loop.Config.SetBrowserRuntime(false)
	base := srv.URL + "/api/sessions/" + id + "/browser"
	stream, err := http.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	stream.Body.Close()
	if stream.StatusCode != http.StatusConflict {
		t.Errorf("stream with the setting off = %d, want 409", stream.StatusCode)
	}
	for _, c := range []struct {
		path string
		body any
	}{
		{"/navigate", map[string]string{"url": "https://example.com/"}},
		{"/input", map[string]any{"text": "x"}},
	} {
		resp := postBrowserJSON(t, base+c.path, c.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("POST %s with the setting off = %d, want 409", c.path, resp.StatusCode)
		}
	}
	if d.BrowserViewing(id) || d.Browser.Existing(id) != nil {
		t.Error("a refused request left the conversation watched or with a tab")
	}
}

// The panel's frame sets the height of the conversation's pages: the
// answer and the panel's poll say it, and it starts no browser for a
// conversation with no tab. A size that is not one is refused.
func TestThePanelsFrameSetsThePagesHeight(t *testing.T) {
	d, srv, id := browserDaemon(t)
	base := srv.URL + "/api/sessions/" + id + "/browser"
	state := func() (st struct {
		HasTab        bool `json:"has_tab"`
		Width, Height int
	}) {
		t.Helper()
		resp, err := http.Get(base)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := state(); st.Width != browser.ViewportWidth || st.Height != browser.ViewportHeight {
		t.Fatalf("before the panel says: %+v", st)
	}
	resp := postBrowserJSON(t, base+"/size", map[string]float64{"width": 574, "height": 780})
	var got struct{ Width, Height int }
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || got.Width != browser.ViewportWidth || got.Height != 1739 {
		t.Fatalf("size: %d %+v, want 200 and 1280x1739", resp.StatusCode, got)
	}
	if st := state(); st.HasTab || st.Height != 1739 {
		t.Errorf("after the panel says: %+v, want no tab and 1739", st)
	}
	if d.Browser.Existing(id) != nil {
		t.Error("a size opened a tab")
	}
	for _, body := range []any{
		map[string]float64{"width": 0, "height": 780},
		map[string]float64{"width": 574},
		map[string]string{"width": "wide"},
	} {
		resp := postBrowserJSON(t, base+"/size", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("size %v = %d, want 400", body, resp.StatusCode)
		}
	}
	if h := d.Browser.Height(id); h != 1739 {
		t.Errorf("a refused size changed the height to %d", h)
	}
	resp = postBrowserJSON(t, srv.URL+"/api/sessions/nope/browser/size", map[string]float64{"width": 574, "height": 780})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("size for no such conversation = %d, want 404", resp.StatusCode)
	}
}

// Nothing the model's browser sends gets an answer: not a page it opened,
// not a script on one, on any route.
func TestTheDaemonRefusesItsOwnBrowser(t *testing.T) {
	_, srv, id := browserDaemon(t)
	ua := "Mozilla/5.0 (Macintosh) Chrome/142.0.0.0 Safari/537.36 " + browser.UserAgentMark
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/settings"},
		{http.MethodPost, "/api/settings/skip_permissions"},
		{http.MethodGet, "/api/sessions/" + id + "/browser"},
		{http.MethodGet, "/"},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader(`{"enabled":true}`))
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s from the model's browser = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	// The same request without the mark is answered.
	resp, err := http.Get(srv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/settings = %d", resp.StatusCode)
	}
}

// The snapshot route serves a capture the tool saved for the conversation
// and nothing else: only a name in the tool's own shape, only from the
// conversation's snapshot directory, never through a link.
func TestTheSnapshotRouteServesOnlyCaptures(t *testing.T) {
	d, srv, id := browserDaemon(t)
	dir := filepath.Join(d.Loop.SessionDir(id), ".localcode", "snapshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.WriteFile(filepath.Join(dir, "20261119-101500-001.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(secret, []byte("secret"), 0o600)
	if err := os.Symlink(secret, filepath.Join(dir, "20261119-101500-002.png")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "notes.png"), []byte("not a capture"), 0o644)

	get := func(path string) (int, string, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), b.String()
	}
	base := "/api/sessions/" + id + "/snapshots/"
	if code, ct, body := get(base + "20261119-101500-001.png"); code != http.StatusOK || ct != "image/png" || body != string(png) {
		t.Errorf("a saved capture: %d %q %q", code, ct, body)
	}
	for _, name := range []string{
		"20261119-101500-002.png", // a link out of the directory
		"20261119-101500-003.png", // not there
		"notes.png",               // not a capture's name
		"..%2F..%2Fsecret.txt",
		"20261119-101500-001.png.txt",
	} {
		if code, _, body := get(base + name); code != http.StatusNotFound || strings.Contains(body, "secret") {
			t.Errorf("GET %s = %d %q, want 404", name, code, body)
		}
	}
	if code, _, _ := get("/api/sessions/nobody/snapshots/20261119-101500-001.png"); code != http.StatusNotFound {
		t.Errorf("a conversation that does not exist = %d, want 404", code)
	}
}

// A conversation with no working directory (no workspace, no project)
// has its captures under the home directory, and the route finds them
// there: the tool and the route ask the same function.
func TestTheSnapshotRouteFindsACaptureWithNoWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	d, srv, _ := browserDaemon(t)
	if _, err := d.Loop.Store.CreateSession("nowhere", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	d.Loop.ProjectDir = ""
	if dir := d.Loop.SessionDir("nowhere"); dir != "" {
		t.Fatalf("the conversation has a working directory, %q; the test is not testing anything", dir)
	}
	dir, err := agent.SnapshotDir("")
	if err != nil || !strings.HasPrefix(dir, home) {
		t.Fatalf("SnapshotDir(\"\") = %q, %v; want under %s", dir, err, home)
	}
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "20261119-101500-001.png"), []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/api/sessions/nowhere/snapshots/20261119-101500-001.png")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a capture saved with no working directory = %d, want 200", resp.StatusCode)
	}
}

// A snapshot taken before the conversation moved to another working
// directory still shows: the route finds it where its tool.end says it
// was saved, and only in a .localcode snapshots directory.
func TestASnapshotStillShowsAfterTheWorkspaceMoves(t *testing.T) {
	d, srv, id := browserDaemon(t)
	dir, _ := agent.SnapshotDir(d.Loop.SessionDir(id))
	os.MkdirAll(dir, 0o755)
	const name = "20261119-101500-001.png"
	saved := filepath.Join(dir, name)
	if err := os.WriteFile(saved, []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.Loop.Store.Append(id, events.TypeToolEnd, map[string]any{"tool_use_id": "t1", "content": "saved", "snapshot": saved})
	// A tool.end naming a file of the same name outside a snapshots
	// directory is not followed.
	stray := filepath.Join(t.TempDir(), "20261119-101500-002.png")
	os.WriteFile(stray, []byte("\x89PNG"), 0o644)
	d.Loop.Store.Append(id, events.TypeToolEnd, map[string]any{"tool_use_id": "t2", "content": "x", "snapshot": stray})

	moved := postBrowserJSON(t, srv.URL+"/api/workspace", map[string]string{"path": t.TempDir(), "session_id": id})
	moved.Body.Close()
	if moved.StatusCode != http.StatusOK {
		t.Fatalf("workspace move: %d", moved.StatusCode)
	}
	if now, _ := agent.SnapshotDir(d.Loop.SessionDir(id)); now == dir {
		t.Fatal("the conversation did not move; the test is not testing anything")
	}
	get := func(name string) int {
		resp, err := http.Get(srv.URL + "/api/sessions/" + id + "/snapshots/" + name)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(name); code != http.StatusOK {
		t.Errorf("a snapshot from before the move = %d, want 200", code)
	}
	if code := get("20261119-101500-002.png"); code != http.StatusNotFound {
		t.Errorf("a file outside a snapshots directory = %d, want 404", code)
	}
}

func TestTheSettingTurnsTheBrowserOnAndOff(t *testing.T) {
	d, srv, _ := browserDaemon(t)
	d.Loop.Config.SetBrowserRuntime(false)
	resp := postBrowserJSON(t, srv.URL+"/api/settings/browser", map[string]bool{"enabled": true})
	resp.Body.Close()
	if !d.Loop.Config.BrowserLive() {
		t.Fatal("not on after the settings call")
	}
	get, err := http.Get(srv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	json.NewDecoder(get.Body).Decode(&s)
	get.Body.Close()
	_, findErr := browser.Find("")
	if s["browser"] != true || s["browser_available"] != (findErr == nil) {
		t.Errorf("settings report browser=%v available=%v; a browser is installed here: %v", s["browser"], s["browser_available"], findErr == nil)
	}
	resp = postBrowserJSON(t, srv.URL+"/api/settings/browser", map[string]bool{"enabled": false})
	resp.Body.Close()
	if d.Loop.Config.BrowserLive() {
		t.Error("still on after turning it off")
	}
	// Copying somebody's profile is not something a request does while
	// the feature is off.
	imp := postBrowserJSON(t, srv.URL+"/api/browser/import", map[string]any{})
	imp.Body.Close()
	if imp.StatusCode != http.StatusConflict {
		t.Errorf("import with the browser off = %d, want 409", imp.StatusCode)
	}
}
