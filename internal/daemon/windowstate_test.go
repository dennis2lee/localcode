package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// windowStateDaemon is a daemon that keeps the window's settings in a file
// under the test's own directory, and a server in front of it.
func windowStateDaemon(t *testing.T) (*Daemon, *httptest.Server, string) {
	t.Helper()
	d := newTestDaemon(t, "http://127.0.0.1:9/v1")
	path := filepath.Join(t.TempDir(), "sub", "window-state.json")
	d.WindowStatePath = path
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	return d, srv, path
}

func windowStateRequest(t *testing.T, method, url, body string, header map[string]string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

type windowStateAnswer struct {
	Available bool               `json:"available"`
	Kept      *map[string]string `json:"kept"`
}

func getWindowState(t *testing.T, srv *httptest.Server) windowStateAnswer {
	t.Helper()
	status, body := windowStateRequest(t, "GET", srv.URL+"/api/window/state", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/window/state: %d %s", status, body)
	}
	var a windowStateAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatalf("GET /api/window/state answered %q: %v", body, err)
	}
	return a
}

// The whole point: what the page writes is in a file the moment the
// request is answered, and a daemon started afterwards, on whatever port,
// reads the same thing back.
func TestWhatTheWindowWritesIsThereForTheNextDaemon(t *testing.T) {
	_, srv, path := windowStateDaemon(t)

	// Before anything is written the answer is "nothing kept", which is
	// not the same as "kept, and empty": the page hands over what it has
	// only for the first.
	if a := getWindowState(t, srv); !a.Available || a.Kept != nil {
		t.Fatalf("before any write: available=%v kept=%v, want available and null", a.Available, a.Kept)
	}

	status, body := windowStateRequest(t, "PUT", srv.URL+"/api/window/state",
		`{"localcode.textScale":"1.125","localcode.zoom":"1.25","archiveOpen":"1"}`, nil)
	if status != http.StatusNoContent {
		t.Fatalf("PUT: %d %s", status, body)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file is not there when the request has been answered: %v", err)
	}
	var onDisk map[string]string
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("the file is not an object of strings: %v\n%s", err, raw)
	}
	if onDisk["localcode.textScale"] != "1.125" || onDisk["localcode.zoom"] != "1.25" || onDisk["archiveOpen"] != "1" {
		t.Errorf("file = %s", raw)
	}

	// Another daemon, another address, the same file.
	other := newTestDaemon(t, "http://127.0.0.1:9/v1")
	other.WindowStatePath = path
	srv2 := httptest.NewServer(other.Handler())
	defer srv2.Close()
	a := getWindowState(t, srv2)
	if a.Kept == nil || (*a.Kept)["localcode.textScale"] != "1.125" || (*a.Kept)["localcode.zoom"] != "1.25" {
		t.Fatalf("the next daemon reads %v", a.Kept)
	}
}

// A write names the keys it changes and leaves the rest, and null forgets
// one: two windows writing different settings do not undo each other, and
// a setting put back to its default is gone rather than stored as one.
func TestAWriteChangesOnlyTheKeysItNames(t *testing.T) {
	_, srv, _ := windowStateDaemon(t)
	put := func(body string) {
		t.Helper()
		if status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", body, nil); status != http.StatusNoContent {
			t.Fatalf("PUT %s: %d %s", body, status, out)
		}
	}
	put(`{"localcode.textScale":"1.125","localcode.face.doc":"Georgia, serif"}`)
	put(`{"localcode.zoom":"1.5"}`)
	put(`{"localcode.textScale":null}`)
	// Forgetting a key that is not there is not an error.
	put(`{"localcode.textScale.read":null}`)

	a := getWindowState(t, srv)
	if a.Kept == nil {
		t.Fatal("nothing kept after three writes")
	}
	want := map[string]string{"localcode.face.doc": "Georgia, serif", "localcode.zoom": "1.5"}
	if len(*a.Kept) != len(want) {
		t.Errorf("kept = %v, want %v", *a.Kept, want)
	}
	for k, v := range want {
		if (*a.Kept)[k] != v {
			t.Errorf("kept[%q] = %q, want %q", k, (*a.Kept)[k], v)
		}
	}

	// An empty object writes the file too, so "kept, and empty" can be
	// told from "nothing kept yet".
	put(`{"localcode.face.doc":null,"localcode.zoom":null}`)
	if a := getWindowState(t, srv); a.Kept == nil || len(*a.Kept) != 0 {
		t.Errorf("after forgetting everything: kept = %v, want an empty object", a.Kept)
	}
}

// Only the page's own names, of a size the page writes: this is one small
// file, and not a way to put anything else on the disk.
func TestTheWindowStateRefusesWhatThePageWouldNotWrite(t *testing.T) {
	_, srv, path := windowStateDaemon(t)
	big := strings.Repeat("x", maxWindowStateValue+1)
	many := map[string]string{}
	for i := 0; i < maxWindowStateKeys+1; i++ {
		many[fmt.Sprintf("localcode.k%d", i)] = "v"
	}
	manyJSON, _ := json.Marshal(many)
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"a name that is not the page's":   {`{"smart_agent":"true"}`, http.StatusBadRequest},
		"a path for a name":               {`{"localcode.../../x":"1"}`, http.StatusBadRequest},
		"an object's own inherited name":  {`{"__proto__":"1"}`, http.StatusBadRequest},
		"a name with the prefix and more": {`{"localcode.":"1"}`, http.StatusBadRequest},
		"a number for a value":            {`{"localcode.zoom":1.5}`, http.StatusBadRequest},
		"an array":                        {`["localcode.zoom"]`, http.StatusBadRequest},
		"a value too large":               {`{"localcode.zoom":"` + big + `"}`, http.StatusRequestEntityTooLarge},
		"too many names":                  {string(manyJSON), http.StatusBadRequest},
	} {
		status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", c.body, nil)
		if status != c.want {
			t.Errorf("%s: %d %s, want %d", name, status, out, c.want)
		}
		// Refused for the reason the case is about, and not for another
		// rule the body also happens to break.
		if name == "too many names" && !strings.Contains(out, "at most") {
			t.Errorf("%s: refused with %s, which is not the limit on how many", name, out)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a refused write left a file behind (stat: %v)", err)
	}

	// One refused name refuses the whole request: nothing of it is kept.
	windowStateRequest(t, "PUT", srv.URL+"/api/window/state", `{"localcode.zoom":"1.5","other":"1"}`, nil)
	if a := getWindowState(t, srv); a.Kept != nil {
		t.Errorf("half of a refused request was kept: %v", *a.Kept)
	}
}

// The whole file has a size it does not pass, whatever the single values
// are, and a write that would pass it changes nothing.
func TestTheWindowStateFileHasACeiling(t *testing.T) {
	_, srv, path := windowStateDaemon(t)
	value := strings.Repeat("x", maxWindowStateValue-1024)
	last := 0
	for i := 0; i < 6; i++ {
		body, _ := json.Marshal(map[string]string{"localcode.big" + string(rune('a'+i)): value})
		last, _ = windowStateRequest(t, "PUT", srv.URL+"/api/window/state", string(body), nil)
		if last != http.StatusNoContent {
			break
		}
	}
	if last != http.StatusRequestEntityTooLarge {
		t.Fatalf("six values of %d bytes were all taken (last answer %d); the file has no ceiling", len(value), last)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxWindowStateFile {
		t.Errorf("the file is %d bytes, over the %d it is held to", info.Size(), maxWindowStateFile)
	}
	// What was there before the refused write is still readable.
	if a := getWindowState(t, srv); a.Kept == nil || len(*a.Kept) == 0 {
		t.Errorf("the refused write lost what was kept")
	}
}

// The ceiling counts the line end the file is written with: a write that
// would land exactly on the limit before it is one byte over on disk.
func TestTheWindowStateCeilingCountsTheWholeFile(t *testing.T) {
	_, srv, path := windowStateDaemon(t)
	chunk := strings.Repeat("x", 250*1024)
	for _, k := range []string{"localcode.a", "localcode.b", "localcode.c", "localcode.d"} {
		b, _ := json.Marshal(map[string]string{k: chunk})
		if status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", string(b), nil); status != http.StatusNoContent {
			t.Fatalf("PUT %s: %d %s", k, status, out)
		}
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var held map[string]string
	if err := json.Unmarshal(cur, &held); err != nil {
		t.Fatal(err)
	}
	// One more value, sized so the object is exactly the limit before
	// its line end.
	held["localcode.e"] = ""
	base, _ := json.MarshalIndent(held, "", "  ")
	need := maxWindowStateFile - len(base)
	if need < 0 || need > maxWindowStateValue {
		t.Fatalf("the last value would have to be %d bytes; this test no longer fills the file the way it means to", need)
	}
	b, _ := json.Marshal(map[string]string{"localcode.e": strings.Repeat("y", need)})
	status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", string(b), nil)
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("a write that makes the file one byte over: %d %s, want 413", status, out)
	}
	if info, err := os.Stat(path); err != nil || info.Size() > maxWindowStateFile {
		t.Errorf("the file is over the %d bytes it is held to (stat: %v, %v)", maxWindowStateFile, info, err)
	}
	// One byte less fits, and lands exactly on the limit.
	b, _ = json.Marshal(map[string]string{"localcode.e": strings.Repeat("y", need-1)})
	if status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", string(b), nil); status != http.StatusNoContent {
		t.Errorf("a write that makes the file exactly the limit: %d %s, want 204", status, out)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != maxWindowStateFile {
		t.Errorf("the file is %v bytes, want exactly %d (stat error: %v)", info.Size(), maxWindowStateFile, err)
	}
}

// The body is one object and nothing else. A null decodes without an
// error and names nothing; taken, it wrote an empty file, and a file that
// exists is the record, so the next page cleared its own settings to
// match it instead of handing them over.
func TestTheWindowStateBodyIsOneObject(t *testing.T) {
	_, srv, path := windowStateDaemon(t)
	over := `{"localcode.a":"` + strings.Repeat("x", maxWindowStateFile) + `"}`
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"null":                        {`null`, http.StatusBadRequest},
		"an object and more after it": {`{"localcode.zoom":"1.5"} and more`, http.StatusBadRequest},
		"two objects":                 {`{"localcode.zoom":"1.5"}{"localcode.zoom":"2"}`, http.StatusBadRequest},
		"a body over the limit":       {over, http.StatusRequestEntityTooLarge},
	} {
		status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", c.body, nil)
		if status != c.want {
			t.Errorf("%s: %d %s, want %d", name, status, out, c.want)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a refused body left a file behind (stat: %v)", err)
	}
	if a := getWindowState(t, srv); a.Kept != nil {
		t.Errorf("after only refused bodies something is kept: %v", *a.Kept)
	}
	// Space after the object is not "more".
	if status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", "{\"localcode.zoom\":\"1.5\"}\n  \n", nil); status != http.StatusNoContent {
		t.Errorf("an object followed by blank lines: %d %s, want 204", status, out)
	}
}

// A file that is there and cannot be read is still the record. Read as
// "nothing kept", the next write put the one setting it carried into an
// empty file and answered that it was done, and every other setting was
// gone. On Windows that takes no more than another process holding the
// file open for a moment.
func TestAWindowStateFileThatCannotBeReadIsNotReplaced(t *testing.T) {
	t.Run("a directory where the file should be", func(t *testing.T) {
		// Refuses a read on every platform, and not as "no such file".
		_, srv, path := windowStateDaemon(t)
		if err := os.MkdirAll(filepath.Join(path, "inside"), 0o755); err != nil {
			t.Fatal(err)
		}
		if status, out := windowStateRequest(t, "GET", srv.URL+"/api/window/state", "", nil); status != http.StatusInternalServerError {
			t.Errorf("GET: %d %s, want 500: a page told \"nothing kept\" hands over its own storage in place of the file", status, out)
		}
		if status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", `{"localcode.zoom":"1.5"}`, nil); status != http.StatusInternalServerError {
			t.Errorf("PUT: %d %s, want 500", status, out)
		}
		if _, err := os.Stat(filepath.Join(path, "inside")); err != nil {
			t.Errorf("what was at the path was replaced: %v", err)
		}
	})
	t.Run("a file that may not be read", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("a mode does not make a file unreadable on Windows; the directory case above runs there")
		}
		if os.Geteuid() == 0 {
			t.Skip("root reads a file of any mode")
		}
		_, srv, path := windowStateDaemon(t)
		const settings = `{"localcode.textScale":"1.25","localcode.face.doc":"Georgia, serif"}`
		if status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", settings, nil); status != http.StatusNoContent {
			t.Fatalf("PUT: %d %s", status, out)
		}
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", `{"localcode.zoom":"1.5"}`, nil)
		if status != http.StatusInternalServerError {
			t.Errorf("PUT over a file that could not be read: %d %s, want 500", status, out)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		a := getWindowState(t, srv)
		if a.Kept == nil || (*a.Kept)["localcode.textScale"] != "1.25" || (*a.Kept)["localcode.face.doc"] != "Georgia, serif" {
			t.Errorf("the settings that were in the file are gone: %v", a.Kept)
		}
		if a.Kept != nil && (*a.Kept)["localcode.zoom"] != "" {
			t.Errorf("a write answered 500 was made: %v", *a.Kept)
		}
	})
}

// The answer is never one a browser may keep and show again: the file
// changes under it.
func TestTheWindowStateIsNotCached(t *testing.T) {
	_, srv, _ := windowStateDaemon(t)
	resp, err := http.Get(srv.URL + "/api/window/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// The file is read by whatever page asks, so a page from another site, or
// one that reached this machine under another name, is refused both ways.
func TestTheWindowStateRefusesAnotherSite(t *testing.T) {
	_, srv, _ := windowStateDaemon(t)
	url := srv.URL + "/api/window/state"
	if status, out := windowStateRequest(t, "PUT", url, `{"localcode.face.doc":"Georgia, serif"}`, nil); status != http.StatusNoContent {
		t.Fatalf("PUT: %d %s", status, out)
	}
	for name, header := range map[string]map[string]string{
		"another site's fetch":        {"Sec-Fetch-Site": "cross-site"},
		"another origin":              {"Origin": "http://evil.example"},
		"a name pointed at this host": {"Host": "rebind.example:4096"},
	} {
		if status, out := windowStateRequest(t, "GET", url, "", header); status != http.StatusForbidden {
			t.Errorf("GET from %s: %d %s, want 403", name, status, out)
		} else if strings.Contains(out, "Georgia") {
			t.Errorf("GET from %s was refused and still carried the settings: %s", name, out)
		}
		if status, _ := windowStateRequest(t, "PUT", url, `{"localcode.zoom":"3"}`, header); status != http.StatusForbidden {
			t.Errorf("PUT from %s: %d, want 403", name, status)
		}
	}
	// A form, which another site can post without asking first.
	req, _ := http.NewRequest("PUT", url, strings.NewReader(`{"localcode.zoom":"3"}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a PUT that is not JSON: %d, want 403", resp.StatusCode)
	}
	// The page's own requests go through, with what a browser sends.
	own := map[string]string{"Origin": srv.URL, "Sec-Fetch-Site": "same-origin"}
	if status, out := windowStateRequest(t, "GET", url, "", own); status != http.StatusOK || !strings.Contains(out, "Georgia") {
		t.Errorf("the page's own GET: %d %s", status, out)
	}
	if a := getWindowState(t, srv); a.Kept == nil || (*a.Kept)["localcode.zoom"] != "" {
		t.Errorf("a refused write changed the file: %v", a.Kept)
	}
}

// A daemon with no path keeps nothing and says so, and the page then
// leaves its own storage alone.
func TestADaemonWithNoPathKeepsNoWindowState(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:9/v1")
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	if a := getWindowState(t, srv); a.Available || a.Kept != nil {
		t.Errorf("available=%v kept=%v, want neither", a.Available, a.Kept)
	}
	if status, _ := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", `{"localcode.zoom":"2"}`, nil); status != http.StatusNotFound {
		t.Errorf("PUT: %d, want 404", status)
	}
}

// A file that is not what this writes is read as nothing kept, so the
// page fills it in again, and a file somebody edited is read for the
// entries that are still the page's.
func TestAWindowStateFileThatIsNotOursIsReadForWhatIs(t *testing.T) {
	_, srv, path := windowStateDaemon(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := getWindowState(t, srv); !a.Available || a.Kept != nil {
		t.Errorf("a file that does not parse: available=%v kept=%v, want available and null", a.Available, a.Kept)
	}
	// And the next write replaces it rather than failing on it.
	if status, out := windowStateRequest(t, "PUT", srv.URL+"/api/window/state", `{"localcode.zoom":"1.1"}`, nil); status != http.StatusNoContent {
		t.Fatalf("PUT over a file that does not parse: %d %s", status, out)
	}

	// A file that is null is not an object of settings either.
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := getWindowState(t, srv); !a.Available || a.Kept != nil {
		t.Errorf("a file that is null: available=%v kept=%v, want available and null", a.Available, a.Kept)
	}

	edited := `{"localcode.zoom":"1.5","other":"x","localcode.textScale":1.25,"__proto__":"y","localcode.face.ui":"Verdana, sans-serif","localcode.face.doc":null}`
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	a := getWindowState(t, srv)
	if a.Kept == nil {
		t.Fatal("an edited file read as nothing kept")
	}
	if len(*a.Kept) != 2 || (*a.Kept)["localcode.zoom"] != "1.5" || (*a.Kept)["localcode.face.ui"] != "Verdana, sans-serif" {
		t.Errorf("kept = %v, want only the two entries that are the page's", *a.Kept)
	}
}

// Writes from several requests at once all land: each reads the file and
// writes it back, and one must not do that between another's two halves.
func TestWindowStateWritesAtOnceAllLand(t *testing.T) {
	_, srv, path := windowStateDaemon(t)
	names := []string{"localcode.a", "localcode.b", "localcode.c", "localcode.d", "localcode.e", "localcode.f", "localcode.g", "localcode.h"}
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{name: "1"})
			req, _ := http.NewRequest("PUT", srv.URL+"/api/window/state", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("PUT %s: %v", name, err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Errorf("PUT %s: %d", name, resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	a := getWindowState(t, srv)
	if a.Kept == nil || len(*a.Kept) != len(names) {
		t.Fatalf("kept = %v, want all %d", a.Kept, len(names))
	}
	// No temporary file is left beside it.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("left behind beside the file: %s", e.Name())
		}
	}
}
