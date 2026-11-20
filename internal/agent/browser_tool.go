package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"localcode/internal/browser"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// The model working a web page, in a browser shown in the GUI's right
// panel.
//
// Offered only while the browser setting is on and the conversation is
// open in a GUI window (see hiddenTools): the person asked for a browser
// they can watch and take over, and a conversation in the TUI or a
// headless run has nobody watching. It asks nothing: the person chose a
// browser that runs without questions, and a deny rule for "browser"
// matched against the page's URL is how a site is kept out of reach.

// BrowserToolName is the tool's name, and the key permission rules use.
const BrowserToolName = "browser"

// browserOffered reports whether this turn is offered the browser: the
// setting is on and a GUI window is showing the conversation.
func (l *Loop) browserOffered(ctx context.Context) bool {
	if l.Config == nil || !l.Config.BrowserLive() || l.BrowserViewing == nil || Unattended(ctx) {
		return false
	}
	sessionID, ok := SessionIDFromContext(ctx)
	return ok && l.BrowserViewing(sessionID)
}

// BrowserTool drives the conversation's tab.
type BrowserTool struct {
	loop    *Loop
	manager *browser.Manager
}

// NewBrowserTool returns the tool, driving tabs in manager.
func NewBrowserTool(loop *Loop, manager *browser.Manager) BrowserTool {
	return BrowserTool{loop: loop, manager: manager}
}

func (BrowserTool) Name() string { return BrowserToolName }

func (BrowserTool) Description() string {
	return fmt.Sprintf(`Use a web browser the way a person does. It is shown to the user in a panel of the app beside the conversation, and they can click and type in it too. The page is %dx%d pixels.

Actions:
- navigate {url}: open a URL.
- back, forward, reload.
- read: the page's URL, title, text, and every visible element that can be clicked or typed into, numbered [n]. Numbers change when the page does; read again after anything that changes it.
- screenshot {full_page?, ref?}: a snapshot of the page, saved as a PNG file in the workspace and shown to the user in the conversation; the result names the file. The visible part by default, the whole page with full_page, or only element [ref]. A model that is sent images also gets the image.
- click {ref} or {x, y}: click element [ref] from the last read, or the point (x, y) on the page.
- type {text, ref?}: type text, passwords included, into element [ref] (clicked first) or wherever the focus is.
- key {keys}: press a key or a combination: "Enter", "Tab", "Escape", "shift+Tab", "ctrl+a" (select all, on every platform).
- scroll {direction, amount?, ref?}: scroll up, down, left or right by amount steps (default 3), over element [ref] or the middle of the page.
- wait {seconds}: wait up to 10 seconds for the page to change.

Work in small steps and look again after each one: read (or screenshot) to see the result before deciding the next action.`, browser.ViewportWidth, browser.ViewportHeight)
}

func (BrowserTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":["navigate","back","forward","reload","read","screenshot","click","type","key","scroll","wait"]},
"url":{"type":"string"},
"ref":{"type":"integer","description":"element number from the last read"},
"x":{"type":"number"},"y":{"type":"number"},
"text":{"type":"string"},
"keys":{"type":"string"},
"direction":{"type":"string","enum":["up","down","left","right"]},
"amount":{"type":"integer"},
"seconds":{"type":"number"},
"full_page":{"type":"boolean","description":"screenshot: the whole page, not only the visible part"}
},"required":["action"]}`)
}

// RequiresPermission is false: the person chose a browser that runs
// without questions. A deny rule still refuses (see Execute and Subject).
func (BrowserTool) RequiresPermission(json.RawMessage) bool { return false }

// Subject is the URL a navigate is going to, so a deny rule for "browser"
// such as "*bank*" keeps the model from opening it. For every other action
// the page it would act on is checked in Execute.
func (BrowserTool) Subject(input json.RawMessage) string {
	var in struct {
		Action string `json:"action"`
		URL    string `json:"url"`
	}
	if json.Unmarshal(input, &in) != nil || in.Action != "navigate" {
		return ""
	}
	// As Navigate will open it, so a rule written for "https://bank*"
	// matches the bare "bank.com" the model may have typed.
	if u, err := browser.NormalizeURL(in.URL); err == nil {
		return u
	}
	return in.URL
}

func (t BrowserTool) Describe(input json.RawMessage) string {
	var in browserInput
	_ = json.Unmarshal(input, &in)
	switch in.Action {
	case "navigate":
		return "browser: open " + in.URL
	case "type":
		return "browser: type into the page"
	default:
		return "browser: " + in.Action
	}
}

type browserInput struct {
	Action    string   `json:"action"`
	URL       string   `json:"url"`
	Ref       int      `json:"ref"`
	X         *float64 `json:"x"`
	Y         *float64 `json:"y"`
	Text      string   `json:"text"`
	Keys      string   `json:"keys"`
	Direction string   `json:"direction"`
	Amount    int      `json:"amount"`
	Seconds   float64  `json:"seconds"`
	FullPage  bool     `json:"full_page"`
}

func browserError(format string, args ...any) tools.Result {
	return tools.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

func (t BrowserTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var in browserInput
	if err := json.Unmarshal(input, &in); err != nil {
		return browserError("invalid input: %v", err)
	}
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return browserError("the browser has no conversation to belong to")
	}
	// Asked again on every call, not only when the turn's tools were
	// chosen: the setting can go off and the window close in the middle
	// of a turn, and an agent whose own tools list names "browser" is
	// handed the tool without the offering rule being asked at all.
	if t.loop == nil || !t.loop.browserOffered(ctx) {
		return tools.Result{Content: "the browser is not available: it is off, or this conversation is not open in the desktop window", IsError: true, Refused: true}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tab, err := t.manager.Tab(ctx, sessionID)
	if err != nil {
		return browserError("could not open the browser: %v", err)
	}
	if in.Action != "navigate" {
		if refusal := t.denied(ctx, tab); refusal != "" {
			return tools.Result{Content: refusal, IsError: true, Refused: true}
		}
	}

	res, err := t.act(ctx, tab, in)
	if err != nil {
		// The guard failed a load a deny rule covers: a redirect, or a
		// link the page followed. Said as the rule's refusal, which it is.
		if errors.Is(err, browser.ErrBlocked) {
			return tools.Result{Content: fmt.Sprintf("%s: it led to a page a permission rule for %q denies, and that page was not loaded; navigate somewhere else", in.Action, BrowserToolName), IsError: true, Refused: true}
		}
		return browserError("%s: %v", in.Action, err)
	}
	// An action that led to a denied page (a click on a link, a script
	// that moved the page) found it not loaded. Its result would name
	// the page's full address, which the rule keeps from the model too.
	if refusal := t.denied(ctx, tab); refusal != "" {
		return tools.Result{Content: fmt.Sprintf("%s: it led to a page that was not loaded: %s", in.Action, refusal), IsError: true, Refused: true}
	}
	if dialogs := tab.TakeDialogs(); len(dialogs) > 0 {
		res.Content += "\nThe page showed a dialog, answered OK: " + strings.Join(dialogs, "; ")
	}
	return res
}

// denied refuses an action on a page a deny rule for the browser covers,
// such as one a click led to.
func (t BrowserTool) denied(ctx context.Context, tab *browser.Tab) string {
	if t.loop == nil || t.loop.Config == nil {
		return ""
	}
	loc, err := tab.Location(ctx)
	if err != nil || loc.URL == "" {
		return ""
	}
	// The browser's own rule, not the running agent's: every agent shares
	// the browser, so what any rule denies is denied here as it is to the
	// guard, and an agent's allow does not lift a top-level deny.
	if t.loop.Config.DeniedAnywhere(BrowserToolName, loc.URL) {
		// The site, not the URL: a query string can carry what the rule
		// was written to keep from the model.
		return fmt.Sprintf("the page on %s is denied by a permission rule for %q; navigate somewhere else", siteOf(loc.URL), BrowserToolName)
	}
	return ""
}

// siteOf is a URL's scheme and host.
func siteOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return "this page"
}

func (t BrowserTool) act(ctx context.Context, tab *browser.Tab, in browserInput) (tools.Result, error) {
	switch in.Action {
	case "navigate":
		if strings.TrimSpace(in.URL) == "" {
			return tools.Result{}, fmt.Errorf("a url is needed")
		}
		if err := tab.Navigate(ctx, in.URL); err != nil {
			return tools.Result{}, err
		}
		return t.where(ctx, tab, "Opened")
	case "back", "forward":
		delta := -1
		if in.Action == "forward" {
			delta = 1
		}
		if err := tab.History(ctx, delta); err != nil {
			return tools.Result{}, err
		}
		return t.where(ctx, tab, "Now at")
	case "reload":
		if err := tab.Reload(ctx); err != nil {
			return tools.Result{}, err
		}
		return t.where(ctx, tab, "Reloaded")
	case "read":
		snap, err := tab.Read(ctx)
		if err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Content: formatSnapshot(snap)}, nil
	case "screenshot":
		return t.snapshot(ctx, tab, in)
	case "click":
		switch {
		case in.Ref > 0:
			if err := tab.ClickRef(ctx, in.Ref); err != nil {
				return tools.Result{}, err
			}
		case in.X != nil && in.Y != nil:
			if err := tab.Click(ctx, *in.X, *in.Y, 1); err != nil {
				return tools.Result{}, err
			}
		default:
			return tools.Result{}, fmt.Errorf("give ref, or x and y")
		}
		tab.Settle(ctx)
		return t.where(ctx, tab, "Clicked. Now at")
	case "type":
		if in.Ref > 0 {
			if err := tab.ClickRef(ctx, in.Ref); err != nil {
				return tools.Result{}, err
			}
		}
		if err := tab.Type(ctx, in.Text); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Content: fmt.Sprintf("Typed %d characters.", len([]rune(in.Text)))}, nil
	case "key":
		if strings.TrimSpace(in.Keys) == "" {
			return tools.Result{}, fmt.Errorf("keys are needed")
		}
		if err := tab.Press(ctx, in.Keys); err != nil {
			return tools.Result{}, err
		}
		tab.Settle(ctx)
		return t.where(ctx, tab, "Pressed "+in.Keys+". Now at")
	case "scroll":
		amount := in.Amount
		if amount <= 0 {
			amount = 3
		}
		step := float64(amount) * 100
		var dx, dy float64
		switch in.Direction {
		case "up":
			dy = -step
		case "down", "":
			dy = step
		case "left":
			dx = -step
		case "right":
			dx = step
		default:
			return tools.Result{}, fmt.Errorf("direction is up, down, left or right")
		}
		x, y := float64(browser.ViewportWidth)/2, float64(browser.ViewportHeight)/2
		if in.Ref > 0 {
			px, py, err := tab.RefPoint(ctx, in.Ref)
			if err != nil {
				return tools.Result{}, err
			}
			x, y = px, py
		}
		if err := tab.Scroll(ctx, x, y, dx, dy); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Content: "Scrolled " + in.Direction + "."}, nil
	case "wait":
		d := time.Duration(in.Seconds * float64(time.Second))
		if d <= 0 {
			d = time.Second
		}
		if d > 10*time.Second {
			d = 10 * time.Second
		}
		select {
		case <-ctx.Done():
			return tools.Result{}, ctx.Err()
		case <-time.After(d):
		}
		return t.where(ctx, tab, "Waited. Now at")
	}
	return tools.Result{}, fmt.Errorf("unknown action %q", in.Action)
}

// where reports the page's URL and title after an action that may have
// moved it.
func (t BrowserTool) where(ctx context.Context, tab *browser.Tab, verb string) (tools.Result, error) {
	loc, err := tab.Location(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: fmt.Sprintf("%s %s (%s).", verb, loc.URL, loc.Title)}, nil
}

// formatSnapshot is a read, as text for the model.
func formatSnapshot(s browser.Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\nTitle: %s\n\nElements:\n", s.URL, s.Title)
	if len(s.Elements) == 0 {
		b.WriteString("(none)\n")
	}
	for _, e := range s.Elements {
		fmt.Fprintf(&b, "[%d] %s %q", e.Ref, e.Kind, e.Name)
		if e.Value != "" {
			fmt.Fprintf(&b, " value=%q", e.Value)
		}
		if e.Offscreen {
			b.WriteString(" (scrolled out of view)")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nText:\n")
	b.WriteString(s.Text)
	if s.Truncated {
		b.WriteString("\n(cut short; scroll and read again for more)")
	}
	return b.String()
}

// BrowserBlock is the browser's guard as the deny rules for "browser"
// say it: true for a URL a rule denies, at the top level or in any agent's
// own block, since every conversation and agent shares the one browser.
// The browser asks it on every request, so a rule added while the browser
// runs applies to the next one.
func BrowserBlock(l *Loop) func(url string) bool {
	return func(url string) bool {
		return l.Config.DeniedAnywhere(BrowserToolName, url)
	}
}

// SnapshotDir is where the browser tool saves the captures of a
// conversation whose working directory is dir, and where the daemon
// serves them from: dir's .localcode/snapshots, or ~/.localcode/snapshots
// for a conversation with no working directory.
func SnapshotDir(dir string) (string, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = home
	}
	return filepath.Join(dir, ".localcode", "snapshots"), nil
}

// snapshotName is the shape of a capture's file name, which is also what
// the daemon's route for showing one accepts.
func snapshotName(now time.Time, seq int) string {
	return fmt.Sprintf("%s-%03d.png", now.Format("20060102-150405"), seq)
}

// snapshot captures the page, saves it, shows it in the conversation, and
// gives it to a model that is sent images.
func (t BrowserTool) snapshot(ctx context.Context, tab *browser.Tab, in browserInput) (tools.Result, error) {
	shot, err := tab.Capture(ctx, browser.Shot{FullPage: in.FullPage, Ref: in.Ref})
	if err != nil {
		return tools.Result{}, err
	}
	dir, err := SnapshotDir(tools.WorkingDir(ctx))
	if err != nil {
		return tools.Result{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tools.Result{}, err
	}
	// A project's .localcode is often committed, and a snapshot of a
	// signed-in page is not something to commit by accident.
	if f, err := os.OpenFile(filepath.Join(dir, ".gitignore"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.WriteString("# Browser snapshots localcode saved; never committed.\n*\n")
		_ = f.Close()
	}
	var path string
	now := time.Now()
	for seq := 1; ; seq++ {
		path = filepath.Join(dir, snapshotName(now, seq))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return tools.Result{}, err
		}
		_, werr := f.Write(shot.PNG)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return tools.Result{}, werr
		}
		break
	}
	loc, _ := tab.Location(ctx)
	what := "the visible page"
	switch {
	case in.Ref > 0:
		what = fmt.Sprintf("element [%d]", in.Ref)
	case in.FullPage:
		what = "the whole page"
	}
	text := fmt.Sprintf("Snapshot of %s of %s (%s), %dx%d, saved to %s.", what, loc.URL, loc.Title, shot.Width, shot.Height, path)
	if shot.Cut {
		text += fmt.Sprintf(" The page is taller than %d pixels; only its top was taken.", browser.MaxShotHeight)
	}
	res := tools.Result{Content: text, Snapshot: path}
	switch {
	case !viewsImages(ctx):
		res.Content += " This model is not sent images, so it has the file and not the picture; read describes the page in text."
	case len(shot.PNG) > provider.MaxImageBytes || shot.Width > provider.MaxImageSide || shot.Height > provider.MaxImageSide:
		// A model's server refuses an image past these, and an image it
		// refuses stays in the history, failing every turn after.
		res.Content += fmt.Sprintf(" It is too big to send to the model (%d bytes; the limits are %d bytes and %d pixels a side), so it has the file and not the picture; the visible page or one element is smaller.", len(shot.PNG), provider.MaxImageBytes, provider.MaxImageSide)
	default:
		res.Images = []provider.Block{provider.ImageBlock("image/png", shot.PNG)}
	}
	return res, nil
}
