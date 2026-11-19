package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"localcode/internal/browser"
	"localcode/internal/config"
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
	return fmt.Sprintf(`Use a web browser the way a person does. It is shown to the user in the app's right panel, and they can click and type in it too. The page is %dx%d pixels.

Actions:
- navigate {url}: open a URL.
- back, forward, reload.
- read: the page's URL, title, text, and every visible element that can be clicked or typed into, numbered [n]. Numbers change when the page does; read again after anything that changes it.
- screenshot: an image of what is visible.
- click {ref} or {x, y}: click element [ref] from the last read, or the point (x, y) on the page.
- type {text, ref?}: type text, passwords included, into element [ref] (clicked first) or wherever the focus is.
- key {keys}: press a key or a combination: "Enter", "Tab", "Escape", "ctrl+a", "shift+Tab".
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
"seconds":{"type":"number"}
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
		return browserError("%s: %v", in.Action, err)
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
	if t.loop.Config.ResolvePermissionFor(ctx, BrowserToolName, loc.URL, false) == config.DecisionDeny {
		return fmt.Sprintf("the page at %s is denied by a permission rule for %q; navigate somewhere else", loc.URL, BrowserToolName)
	}
	return ""
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
		if !viewsImages(ctx) {
			return tools.Result{}, fmt.Errorf("this model is not sent images; use read to see the page")
		}
		png, err := tab.Screenshot(ctx)
		if err != nil {
			return tools.Result{}, err
		}
		loc, _ := tab.Location(ctx)
		return tools.Result{
			Content: fmt.Sprintf("Screenshot of %s (%s), %dx%d.", loc.URL, loc.Title, browser.ViewportWidth, browser.ViewportHeight),
			Images:  []provider.Block{provider.ImageBlock("image/png", png)},
		}, nil
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
