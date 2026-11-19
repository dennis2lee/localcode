package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/browser"
)

func browserDaemon(t *testing.T) (*Daemon, *httptest.Server, string) {
	t.Helper()
	d := newTestDaemon(t, "http://127.0.0.1:1")
	dir := t.TempDir()
	// A profile name already chosen, so nothing is copied from the
	// person's own browser.
	if err := os.WriteFile(filepath.Join(dir, "localcode-profile"), []byte("Default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.Browser = browser.NewManager(browser.Options{DataDir: dir})
	t.Cleanup(d.Browser.Close)
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	if _, err := d.Loop.Store.CreateSession("b1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	return d, srv, "b1"
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

	resp := postBrowserJSON(t, base+"/navigate", map[string]string{"url": site.URL})
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
	if d.Browser.Existing(id) != nil {
		t.Error("the deleted conversation's tab is still open")
	}
}

func TestTheSettingTurnsTheBrowserOnAndOff(t *testing.T) {
	d, srv, _ := browserDaemon(t)
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
	if s["browser"] != true || s["browser_available"] != true {
		t.Errorf("settings report browser=%v available=%v", s["browser"], s["browser_available"])
	}
	resp = postBrowserJSON(t, srv.URL+"/api/settings/browser", map[string]bool{"enabled": false})
	resp.Body.Close()
	if d.Loop.Config.BrowserLive() {
		t.Error("still on after turning it off")
	}
}
