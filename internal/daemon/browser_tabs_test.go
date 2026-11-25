package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/browser"
)

// The panel's tab strip: "+" opens an empty tab and makes it the active
// one, a click on a tab switches to it, and its "x" closes it. The
// location answer lists every tab.
func TestThePanelOpensSwitchesAndClosesTabs(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := browser.Find(""); err != nil {
		t.Skip("no Chrome or Edge here")
	}
	_, srv, id := browserDaemon(t)
	base := srv.URL + "/api/sessions/" + id + "/browser"
	type tab struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	tabsOf := func(resp *http.Response) []tab {
		t.Helper()
		defer resp.Body.Close()
		var out struct {
			HasTab bool  `json:"has_tab"`
			Tabs   []tab `json:"tabs"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Tabs
	}
	call := func(method, url string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// The first new tab starts the browser.
	if resp := whenStarted(t, func() *http.Response { return call(http.MethodPost, base+"/tabs") }); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("new tab = %d: %s", resp.StatusCode, body)
	} else if got := tabsOf(resp); len(got) != 1 || !got[0].Active {
		t.Fatalf("after the first new tab: %+v", got)
	}
	resp := call(http.MethodPost, base+"/tabs")
	two := tabsOf(resp)
	if len(two) != 2 || two[0].Active || !two[1].Active {
		t.Fatalf("after the second new tab: %+v", two)
	}
	state := tabsOf(call(http.MethodGet, base))
	if len(state) != 2 || state[1].ID != two[1].ID {
		t.Fatalf("the location answer lists %+v", state)
	}

	if got := tabsOf(call(http.MethodPost, base+"/tabs/"+two[0].ID+"/activate")); len(got) != 2 || !got[0].Active || got[1].Active {
		t.Errorf("after switching to the first: %+v", got)
	}
	if resp := call(http.MethodPost, base+"/tabs/no-such-tab/activate"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("switching to a tab that is not there = %d", resp.StatusCode)
	}
	if got := tabsOf(call(http.MethodDelete, base+"/tabs/"+two[0].ID)); len(got) != 1 || got[0].ID != two[1].ID || !got[0].Active {
		t.Errorf("after closing the active one: %+v", got)
	}
	if resp := call(http.MethodDelete, base+"/tabs/"+two[0].ID); resp.StatusCode != http.StatusNotFound {
		t.Errorf("closing it again = %d", resp.StatusCode)
	}
	// The last one closed: an empty list, which the panel draws, not null,
	// which it would skip and leave the closed tab drawn until a poll.
	last := call(http.MethodDelete, base+"/tabs/"+two[1].ID)
	body, _ := io.ReadAll(last.Body)
	last.Body.Close()
	if !strings.Contains(string(body), `"tabs":[]`) {
		t.Errorf("closing the last tab answered %s", body)
	}
}

// Archiving a conversation closes its browser tabs, none opens for it on
// the shelf, and retrieving it does not bring them back: a page left
// running on the shelf goes on loading and holding its logins for a
// conversation nobody is in.
func TestArchivingAConversationClosesItsTabs(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := browser.Find(""); err != nil {
		t.Skip("no Chrome or Edge here")
	}
	d, srv, id := browserDaemon(t)
	base := srv.URL + "/api/sessions/" + id
	post := func(url string) int {
		t.Helper()
		resp, err := http.Post(url, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// The first new tab starts the browser.
	first := whenStarted(t, func() *http.Response {
		resp, err := http.Post(base+"/browser/tabs", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	})
	data, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("new tab = %d: %s", first.StatusCode, data)
	}
	if d.Browser.Existing(id) == nil {
		t.Fatal("no tab to archive with")
	}
	if code := post(base + "/archive"); code != http.StatusOK {
		t.Fatalf("archive = %d", code)
	}
	// The close runs in the conversation's turn, after the archive has
	// answered.
	deadline := time.Now().Add(5 * time.Second)
	for d.Browser.Existing(id) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the archived conversation's tab is still open")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code := post(base + "/browser/tabs"); code != http.StatusForbidden {
		t.Errorf("a new tab for the archived conversation = %d, want 403", code)
	}
	if d.Browser.Existing(id) != nil {
		t.Error("a tab opened for the archived conversation")
	}
	if code := post(base + "/retrieve"); code != http.StatusOK {
		t.Fatalf("retrieve = %d", code)
	}
	if d.Browser.Existing(id) != nil {
		t.Error("retrieving the conversation brought its tab back")
	}
}

// Every route that starts or drives the browser refuses an archived
// conversation, as the rest of the daemon does: archiving closed its tabs,
// and none opens for it on the shelf.
func TestTheBrowserRefusesAnArchivedConversation(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Browser = browser.NewManager(browser.Options{ChromePath: "/no/such/chrome", DataDir: t.TempDir()})
	d.Loop.Config.SetBrowserRuntime(true)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	if _, err := d.Loop.Store.CreateSessionIn("b1", "", "general-purpose", t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/sessions/b1"
	do := func(method, url, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := do(http.MethodPost, base+"/archive", "{}"); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d", resp.StatusCode)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/browser/tabs", "{}"},
		{http.MethodPost, "/browser/tabs/T1/activate", "{}"},
		{http.MethodDelete, "/browser/tabs/T1", ""},
		{http.MethodPost, "/browser/navigate", `{"url":"https://example.com/"}`},
		{http.MethodPost, "/browser/input", `{"text":"x"}`},
	} {
		if resp := do(c.method, base+c.path, c.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s on an archived conversation = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
}

// A tab opened for a conversation that was archived or deleted while it
// opened is closed, and the request refused as it would have been had it
// come after: 403 for an archived conversation, 404 for a deleted one.
func TestATabOpenedTooLateIsClosed(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Browser = browser.NewManager(browser.Options{ChromePath: "/no/such/chrome", DataDir: t.TempDir()})
	for _, id := range []string{"live", "shelved"} {
		if _, err := d.Loop.Store.CreateSessionIn(id, "", "general-purpose", t.TempDir(), true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Loop.Store.Archive("shelved"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   string
		late bool
		code int
	}{
		{"live", false, http.StatusOK},
		{"shelved", true, http.StatusForbidden},
		{"deleted", true, http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		if late := d.openedTooLate(w, c.id); late != c.late || (late && w.Code != c.code) {
			t.Errorf("%s: too late %v with %d, want %v with %d", c.id, late, w.Code, c.late, c.code)
		}
	}
}
