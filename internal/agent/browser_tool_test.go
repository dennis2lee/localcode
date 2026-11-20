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
	"sync/atomic"
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
	var blockedHits atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/blocked" {
			blockedHits.Add(1)
			fmt.Fprint(w, `<!doctype html><title>Blocked</title><p>secret</p>`)
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/blocked?token=abc", http.StatusFound)
			return
		}
		if r.URL.Path == "/long" {
			fmt.Fprint(w, `<!doctype html><title>Long</title><style>body{margin:0}</style><div style="height:12000px;background:linear-gradient(red,blue)"></div>`)
			return
		}
		if r.URL.Path == "/busy" {
			// Noise, which a PNG cannot make small: over 5 MB at 1280x1400.
			fmt.Fprint(w, `<!doctype html><title>Busy</title><style>body{margin:0}</style><canvas id=c width=1280 height=1400 style="display:block"></canvas><script>
const c = document.getElementById('c').getContext('2d'); const d = c.createImageData(1280, 1400);
for (let i = 0; i < d.data.length; i++) d.data[i] = (i % 4 === 3) ? 255 : (Math.random() * 256) | 0;
c.putImageData(d, 0, 0);</script>`)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Start</title><body style="margin:0">`+
			`<button style="width:120px;height:40px" onclick="document.title='clicked'">Press</button>`+
			`<a href="/blocked?token=abc">go</a><div style="height:3000px"></div></body>`)
	}))
	defer site.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "localcode-profile"), []byte("Default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	loop.Config.Permissions = config.Permissions{BrowserToolName: config.ToolPermission{Rules: []config.PermissionRule{
		{Match: "*/blocked*", Decision: config.DecisionDeny},
	}}}
	// The guard as cmd/localcode wires it.
	manager := browser.NewManager(browser.Options{DataDir: dir, Block: BrowserBlock(loop)})
	defer manager.Close()
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
	if ignore, _ := os.ReadFile(filepath.Join(work, ".localcode", "snapshots", ".gitignore")); !strings.Contains(string(ignore), "\n*\n") {
		t.Errorf("the snapshot directory does not keep itself out of git: %q", ignore)
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

	// A snapshot past what a model's server takes (8000 pixels a side, or
	// view_image's byte limit) is saved, and the model gets the file and
	// not the picture.
	for _, page := range []string{"/long", "/busy"} {
		call(map[string]any{"action": "navigate", "url": site.URL + page})
		big := tool.Execute(withViewsImages(ctx, true), json.RawMessage(`{"action":"screenshot","full_page":true}`))
		if big.IsError || len(big.Images) != 0 || big.Snapshot == "" || !strings.Contains(big.Content, "too big") {
			t.Errorf("full page of %s for a model that is sent images: images=%d %q", page, len(big.Images), big.Content)
		}
		if _, err := os.Stat(big.Snapshot); err != nil {
			t.Errorf("full page of %s was not saved: %v", page, err)
		}
	}
	call(map[string]any{"action": "navigate", "url": site.URL})
	call(map[string]any{"action": "read"})

	// The deny rule's subject for navigate is the URL.
	if got := tool.Subject(json.RawMessage(`{"action":"navigate","url":"` + site.URL + `/blocked"}`)); got != site.URL+"/blocked" {
		t.Errorf("navigate subject = %q", got)
	}
	if loop.Config.ResolvePermission(BrowserToolName, site.URL+"/blocked", false) != config.DecisionDeny {
		t.Fatal("the rule does not deny the blocked URL; the test is not testing anything")
	}
	// A click on a link there, and a navigate that redirects there, are
	// refused as the rule's refusals, the page is never requested, and
	// the result does not give its address.
	call(map[string]any{"action": "read"})
	for _, c := range []map[string]any{
		{"action": "click", "ref": 2},
		{"action": "navigate", "url": site.URL + "/redirect"},
	} {
		res := call(c)
		if !res.Refused || strings.Contains(res.Content, "token=abc") || strings.Contains(res.Content, "/blocked") {
			t.Errorf("%v: %+v", c, res)
		}
		// Back to an allowed page.
		call(map[string]any{"action": "navigate", "url": site.URL})
		call(map[string]any{"action": "read"})
	}
	if n := blockedHits.Load(); n != 0 {
		t.Errorf("the denied page was requested %d time(s)", n)
	}
	// A page loaded before a rule denied it is refused from then on: the
	// check behind the guard, for a page already open.
	if res := call(map[string]any{"action": "navigate", "url": site.URL + "/later"}); res.IsError {
		t.Fatalf("navigate /later: %+v", res)
	}
	// Through the runtime setters from here on, as the daemon changes
	// them: the browser's request guard resolves the rules on its own
	// goroutine for every request the open page makes.
	later := config.PermissionRule{Match: "*/later*", Decision: config.DecisionDeny}
	loop.Config.AddPermissionRuleRuntime(BrowserToolName, later)
	res := call(map[string]any{"action": "read"})
	if !res.Refused || strings.Contains(res.Content, "Press") {
		t.Errorf("read on a page a rule denied after it loaded: %+v", res)
	}
	// The same refusal whoever runs the turn: an agent's allow does not
	// lift the top-level deny, and another agent's deny counts.
	loop.Config.SetAgentsRuntime(map[string]config.AgentConfig{
		"web": {Permission: map[string]config.ToolPermission{BrowserToolName: {Rules: []config.PermissionRule{{Match: "*/later*", Decision: config.DecisionAllow}}}}},
	})
	asWeb := config.WithAgent(ctx, "web")
	if res := tool.Execute(asWeb, json.RawMessage(`{"action":"read"}`)); !res.Refused {
		t.Errorf("an agent's allow lifted the top-level deny on an open page: %+v", res)
	}
	loop.Config.RemovePermissionRuleRuntime(BrowserToolName, config.PermissionRule{Match: "*/blocked*", Decision: config.DecisionDeny})
	loop.Config.RemovePermissionRuleRuntime(BrowserToolName, later)
	loop.Config.SetAgentsRuntime(map[string]config.AgentConfig{
		"other": {Permission: map[string]config.ToolPermission{BrowserToolName: {Rules: []config.PermissionRule{{Match: "*/later*", Decision: config.DecisionDeny}}}}},
	})
	if res := call(map[string]any{"action": "read"}); !res.Refused {
		t.Errorf("another agent's deny did not count on an open page: %+v", res)
	}

	// The setting going off in the middle of a turn takes the tool away
	// at the next call.
	loop.Config.SetBrowserRuntime(false)
	if res := call(map[string]any{"action": "read"}); !res.Refused {
		t.Errorf("a call after the setting went off: %+v", res)
	}
}

func TestBrowserBlockIsTheDenyRules(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	block := BrowserBlock(loop)
	if block("https://bank.example/") {
		t.Error("blocked with no rule")
	}
	// Read on every call, so a rule added later applies.
	loop.Config.Permissions = config.Permissions{BrowserToolName: config.ToolPermission{Rules: []config.PermissionRule{
		{Match: "*bank*", Decision: config.DecisionDeny},
		{Match: "*ask*", Decision: config.DecisionAsk},
	}}}
	if !block("https://bank.example/") {
		t.Error("a denied URL is not blocked")
	}
	if block("https://ask.example/") || block("https://example.com/") {
		t.Error("a URL no deny rule matches is blocked")
	}
	// An agent's own deny reaches the shared browser, and an agent's
	// allow does not lift a top-level deny there.
	loop.Config.Agents = map[string]config.AgentConfig{
		"web":   {Permission: map[string]config.ToolPermission{BrowserToolName: {Rules: []config.PermissionRule{{Match: "*shop*", Decision: config.DecisionDeny}}}}},
		"agent": {Permission: map[string]config.ToolPermission{BrowserToolName: {Rules: []config.PermissionRule{{Match: "*bank*", Decision: config.DecisionAllow}}}}},
	}
	if !block("https://shop.example/") {
		t.Error("an agent's deny rule does not reach the guard")
	}
	if !block("https://bank.example/") {
		t.Error("an agent's allow lifted a top-level deny in the shared browser")
	}
	// A persona shut down with a catch-all deny keeps only itself away.
	loop.Config.Permissions = config.Permissions{}
	loop.Config.Agents = map[string]config.AgentConfig{
		"quiet": {Permission: map[string]config.ToolPermission{"*": {Rules: []config.PermissionRule{{Match: "*", Decision: config.DecisionDeny}}}}},
	}
	if block("https://example.com/") {
		t.Error("one agent's catch-all deny closed the shared browser")
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
	if got, _ := SnapshotDir("/w"); got != filepath.Join("/w", ".localcode", "snapshots") {
		t.Errorf("SnapshotDir(/w) = %q", got)
	}
	home, _ := os.UserHomeDir()
	if got, _ := SnapshotDir(""); got != filepath.Join(home, ".localcode", "snapshots") {
		t.Errorf("SnapshotDir with no working directory = %q, want under the home directory", got)
	}
}
