package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"localcode/internal/browser"
	"localcode/internal/config"
	"localcode/internal/tools"
)

// The browser is offered to a turn only with the setting on and a GUI
// window showing the conversation, and never to an unattended one.
func TestTheBrowserIsOfferedOnlyWhereSomebodyIsWatching(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	ctx := WithSessionID(context.Background(), "s1")
	watching := map[string]bool{}
	loop.BrowserViewing = func(id string) bool { return watching[id] }

	offered := func(ctx context.Context) bool { return !loop.hiddenTools(ctx)[BrowserToolName] }

	loop.Config.SetBrowserRuntime(false)
	watching["s1"] = true
	if offered(ctx) {
		t.Error("offered with the setting off")
	}
	loop.Config.SetBrowserRuntime(true)
	if !offered(ctx) {
		t.Error("not offered with the setting on and a GUI window watching")
	}
	watching["s1"] = false
	if offered(ctx) {
		t.Error("offered with no GUI window showing the conversation")
	}
	watching["s1"] = true
	if offered(WithSessionID(context.Background(), "s2")) {
		t.Error("offered to a conversation nobody is watching because another one is watched")
	}
	if offered(WithUnattended(ctx)) {
		t.Error("offered to an unattended turn")
	}
	loop.BrowserViewing = nil
	if offered(ctx) {
		t.Error("offered with no daemon to say who is watching")
	}
}

// An agent's own tools list does not offer the browser by naming it, and
// an agent whose only tool is the browser gets no tools when it is not
// offered, not every tool.
func TestAnAgentsOwnListDoesNotOfferTheBrowser(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	loop.Tools.Register(NewBrowserTool(loop, nil))
	ctx := WithSessionID(context.Background(), "s1")
	watching := false
	loop.BrowserViewing = func(string) bool { return watching }
	loop.Config.SetBrowserRuntime(true)

	names := func(tools ...string) []string {
		return loop.Tools.NamesFor(ctx, loop.toolsForTurn(ctx, config.AgentConfig{Tools: tools}))
	}
	if got := names(BrowserToolName); len(got) != 0 {
		t.Errorf("an agent whose only tool is the browser, not offered, was given %v", got)
	}
	if got := names(BrowserToolName, "read_file"); len(got) != 1 || got[0] != "read_file" {
		t.Errorf("not offered: %v, want [read_file]", got)
	}
	watching = true
	if got := names(BrowserToolName); len(got) != 1 || got[0] != BrowserToolName {
		t.Errorf("offered: %v, want [browser]", got)
	}
}

func TestTheBrowserCommandTurnsItOnAndOff(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	closed := 0
	loop.CloseBrowser = func() { closed++ }
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	loop.Config.SetBrowserRuntime(false)
	if out := replyTo(t, loop, sid, "/browser on"); !strings.Contains(out, "browser: on") {
		t.Errorf("/browser on said %q", out)
	}
	if !loop.Config.BrowserLive() {
		t.Error("the setting did not turn on")
	}
	if out := replyTo(t, loop, sid, "/browser"); !strings.Contains(out, "browser: off") {
		t.Errorf("/browser said %q", out)
	}
	if loop.Config.BrowserLive() || closed != 1 {
		t.Errorf("off left the setting %v and closed the browser %d time(s); want off, once", loop.Config.BrowserLive(), closed)
	}
	if out := replyTo(t, loop, sid, "/browser maybe"); !strings.Contains(out, "usage") {
		t.Errorf("a bad argument said %q", out)
	}
}

func TestASnapshotReadsAsAList(t *testing.T) {
	out := formatSnapshot(browser.Snapshot{
		URL: "https://example.com/", Title: "Example", Text: "Hello",
		Elements: []browser.Element{
			{Ref: 1, Kind: "a", Name: "More"},
			{Ref: 2, Kind: "input[password]", Name: "Password", Value: "(filled)"},
			{Ref: 3, Kind: "button", Name: "Later", Offscreen: true},
		},
		Truncated: true,
	})
	for _, want := range []string{
		"URL: https://example.com/", `[1] a "More"`, `[2] input[password] "Password" value="(filled)"`,
		`[3] button "Later" (scrolled out of view)`, "Text:\nHello", "cut short",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot text lacks %q:\n%s", want, out)
		}
	}
}

// The tool against a real browser: it opens a page, reads it, clicks by
// number, takes snapshots, and a deny rule for the browser keeps a site
// out of reach both for navigate (through the permission pipeline's
// subject) and for any action on a page a click led to.
func TestTheBrowserToolWorksAPageAndHonoursADeny(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := browser.Find(""); err != nil {
		t.Skip("no Chrome or Edge here")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/blocked" {
			fmt.Fprint(w, `<!doctype html><title>Blocked</title><p>secret</p>`)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Start</title><body style="margin:0">`+
			`<button style="width:120px;height:40px" onclick="document.title='clicked'">Press</button>`+
			`<a href="/blocked">go</a><div style="height:3000px"></div></body>`)
	}))
	defer site.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "localcode-profile"), []byte("Default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := browser.NewManager(browser.Options{DataDir: dir})
	defer manager.Close()

	loop := newSmartLoop(t, "http://127.0.0.1:1")
	loop.Config.Permissions = config.Permissions{BrowserToolName: config.ToolPermission{Rules: []config.PermissionRule{
		{Match: "*/blocked*", Decision: config.DecisionDeny},
	}}}
	watching := false
	loop.BrowserViewing = func(string) bool { return watching }
	loop.Config.SetBrowserRuntime(true)
	tool := NewBrowserTool(loop, manager)
	work := t.TempDir()
	ctx := tools.WithWorkingDir(WithSessionID(context.Background(), "s1"), work)
	call := func(in map[string]any) tools.Result {
		t.Helper()
		data, _ := json.Marshal(in)
		return tool.Execute(ctx, data)
	}

	// Nobody watching: refused before anything starts.
	if res := call(map[string]any{"action": "navigate", "url": site.URL}); !res.Refused {
		t.Fatalf("navigate with no GUI window watching: %+v", res)
	}
	if manager.Existing("s1") != nil {
		t.Fatal("a refused call opened a tab")
	}
	watching = true

	if res := call(map[string]any{"action": "navigate", "url": site.URL}); res.IsError || !strings.Contains(res.Content, "(Start)") {
		t.Fatalf("navigate: %+v", res)
	}
	read := call(map[string]any{"action": "read"})
	if read.IsError || !strings.Contains(read.Content, `button "Press"`) {
		t.Fatalf("read: %+v", read)
	}
	if res := call(map[string]any{"action": "click", "ref": 1}); res.IsError || !strings.Contains(res.Content, "(clicked)") {
		t.Errorf("click: %+v", res)
	}

	// A model that is not sent images still gets the file, and is told
	// where it is and what read is for.
	shot := call(map[string]any{"action": "screenshot"})
	if shot.IsError || len(shot.Images) != 0 || shot.Snapshot == "" {
		t.Fatalf("screenshot for a model that is not sent images: %+v", shot)
	}
	if !strings.Contains(shot.Content, shot.Snapshot) || !strings.Contains(shot.Content, "read") {
		t.Errorf("the text does not give the file and point at read: %q", shot.Content)
	}
	if filepath.Dir(shot.Snapshot) != filepath.Join(work, ".localcode", "snapshots") {
		t.Errorf("saved to %s, not the conversation's working directory", shot.Snapshot)
	}
	assertPNG := func(path string) (w, h int) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 24 || string(data[1:4]) != "PNG" {
			t.Fatalf("%s is not a PNG", path)
		}
		be := func(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }
		return be(data[16:20]), be(data[20:24])
	}
	if w, h := assertPNG(shot.Snapshot); w != browser.ViewportWidth || h != browser.ViewportHeight {
		t.Errorf("the visible page is %dx%d", w, h)
	}

	seen := tool.Execute(withViewsImages(ctx, true), json.RawMessage(`{"action":"screenshot"}`))
	if seen.IsError || len(seen.Images) != 1 || seen.Snapshot == "" || seen.Snapshot == shot.Snapshot {
		t.Errorf("screenshot for a model that is sent images: %+v", seen)
	}
	whole := call(map[string]any{"action": "screenshot", "full_page": true})
	if _, h := assertPNG(whole.Snapshot); h < 3000 {
		t.Errorf("the whole page is %d pixels tall; the page is over 3000", h)
	}
	elem := call(map[string]any{"action": "screenshot", "ref": 1})
	if w, h := assertPNG(elem.Snapshot); w != 120 || h != 40 {
		t.Errorf("element [1] is %dx%d; the button is 120x40", w, h)
	}
	if res := call(map[string]any{"action": "screenshot", "ref": 99}); !res.IsError {
		t.Errorf("a snapshot of an element that is not there: %+v", res)
	}

	// The deny rule's subject for navigate is the URL.
	if got := tool.Subject(json.RawMessage(`{"action":"navigate","url":"` + site.URL + `/blocked"}`)); got != site.URL+"/blocked" {
		t.Errorf("navigate subject = %q", got)
	}
	if loop.Config.ResolvePermission(BrowserToolName, site.URL+"/blocked", false) != config.DecisionDeny {
		t.Fatal("the rule does not deny the blocked URL; the test is not testing anything")
	}
	// A click that leads there is not a navigate, so the action after it
	// is what is refused.
	call(map[string]any{"action": "read"})
	tab := manager.Existing("s1")
	if err := tab.Navigate(ctx, site.URL+"/blocked"); err != nil {
		t.Fatal(err)
	}
	res := call(map[string]any{"action": "read"})
	if !res.Refused || strings.Contains(res.Content, "secret") {
		t.Errorf("read on a denied page: %+v", res)
	}

	// The setting going off in the middle of a turn takes the tool away
	// at the next call.
	loop.Config.SetBrowserRuntime(false)
	if res := call(map[string]any{"action": "read"}); !res.Refused {
		t.Errorf("a call after the setting went off: %+v", res)
	}
}

func TestSnapshotNamesAreWhatTheDaemonServes(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 15, 0, 0, time.UTC)
	for _, seq := range []int{1, 42, 1000} {
		name := snapshotName(now, seq)
		if !regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9]{3,}\.png$`).MatchString(name) {
			t.Errorf("%q is not a name the daemon's snapshot route serves", name)
		}
	}
	if got := SnapshotDir("/w"); got != filepath.Join("/w", ".localcode", "snapshots") {
		t.Errorf("SnapshotDir = %q", got)
	}
}
