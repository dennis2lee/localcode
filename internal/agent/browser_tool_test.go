package agent

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
// number, and a deny rule for the browser keeps a site out of reach both
// for navigate (through the permission pipeline's subject) and for any
// action on a page a click led to.
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
		fmt.Fprint(w, `<!doctype html><title>Start</title><button onclick="document.title='clicked'">Press</button><a href="/blocked">go</a>`)
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
	tool := NewBrowserTool(loop, manager)
	ctx := WithSessionID(context.Background(), "s1")
	call := func(in map[string]any) tools.Result {
		t.Helper()
		data, _ := json.Marshal(in)
		return tool.Execute(ctx, data)
	}

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
	if res := call(map[string]any{"action": "screenshot"}); !res.IsError {
		t.Error("a screenshot for a model that is not sent images was taken")
	}
	if res := call(map[string]any{"action": "screenshot"}); !strings.Contains(res.Content, "read") {
		t.Errorf("the refusal does not point at read: %q", res.Content)
	}
	shot := tool.Execute(withViewsImages(ctx, true), json.RawMessage(`{"action":"screenshot"}`))
	if shot.IsError || len(shot.Images) != 1 {
		t.Errorf("screenshot for a model that is sent images: %+v", shot)
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
}
