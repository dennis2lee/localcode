package browser

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Element is one thing on the page that can be clicked or typed into, as
// Read lists it. Ref is the number the model clicks it by.
type Element struct {
	Ref       int    `json:"ref"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Offscreen bool   `json:"offscreen,omitempty"`
}

// Snapshot is the page as Read sees it.
type Snapshot struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	Elements  []Element `json:"elements"`
	Truncated bool      `json:"truncated"`
}

// readScript marks every visible element a person could click or type
// into with data-lc-ref, numbering them in document order, and returns
// them with the page's text. Earlier marks are cleared first, so a number
// always means what the latest Read said. A password field's value is
// never read back.
const readScript = `(() => {
  const maxElements = 250, maxText = 15000;
  for (const e of document.querySelectorAll('[data-lc-ref]')) e.removeAttribute('data-lc-ref');
  const sel = 'a[href],button,input:not([type=hidden]),select,textarea,summary,label[for],' +
    '[role=button],[role=link],[role=checkbox],[role=radio],[role=tab],[role=menuitem],[role=option],[role=switch],[role=combobox],[role=textbox],' +
    '[contenteditable=""],[contenteditable=true],[onclick],[tabindex]:not([tabindex="-1"])';
  const clean = s => (s || '').replace(/\s+/g, ' ').trim().slice(0, 80);
  const out = [];
  let n = 0;
  for (const el of document.querySelectorAll(sel)) {
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) continue;
    const st = getComputedStyle(el);
    if (st.visibility === 'hidden' || st.display === 'none' || st.opacity === '0') continue;
    if (out.length >= maxElements) break;
    n++;
    el.setAttribute('data-lc-ref', String(n));
    const tag = el.tagName.toLowerCase();
    const role = el.getAttribute('role');
    let kind = role || tag;
    if (tag === 'input') kind = 'input[' + (el.type || 'text') + ']';
    const name = clean(el.getAttribute('aria-label') || el.innerText || el.getAttribute('placeholder') ||
      el.getAttribute('title') || el.getAttribute('alt') || el.getAttribute('name') || (tag === 'a' ? el.getAttribute('href') : ''));
    let value = '';
    if (tag === 'input' || tag === 'textarea' || tag === 'select') {
      value = el.type === 'password' ? (el.value ? '(filled)' : '') : clean(el.value);
      if (el.type === 'checkbox' || el.type === 'radio') value = el.checked ? 'checked' : 'unchecked';
    }
    const offscreen = r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth;
    out.push({ref: n, kind, name, value, x: Math.round(r.left + r.width / 2), y: Math.round(r.top + r.height / 2), offscreen});
  }
  const text = (document.body ? document.body.innerText : '') || '';
  return {url: location.href, title: document.title, text: text.slice(0, maxText),
    elements: out, truncated: text.length > maxText || out.length >= maxElements};
})()`

// Read returns the page's text and its clickable elements, numbered.
func (t *Tab) Read(ctx context.Context) (Snapshot, error) {
	var s Snapshot
	err := t.eval(ctx, readScript, &s)
	return s, err
}

// RefPoint scrolls the element Read numbered ref into view and returns its
// centre in viewport pixels.
func (t *Tab) RefPoint(ctx context.Context, ref int) (x, y float64, err error) {
	var p *struct{ X, Y float64 }
	script := `(() => {
  const el = document.querySelector('[data-lc-ref="` + strconv.Itoa(ref) + `"]');
  if (!el) return null;
  el.scrollIntoView({block: 'center', inline: 'center'});
  const r = el.getBoundingClientRect();
  return {X: r.left + r.width / 2, Y: r.top + r.height / 2};
})()`
	if err := t.eval(ctx, script, &p); err != nil {
		return 0, 0, err
	}
	if p == nil {
		return 0, 0, fmt.Errorf("element %d is not on the page any more; read the page again", ref)
	}
	return p.X, p.Y, nil
}

// ClickRef clicks the element Read numbered ref, the way a mouse would.
func (t *Tab) ClickRef(ctx context.Context, ref int) error {
	x, y, err := t.RefPoint(ctx, ref)
	if err != nil {
		return err
	}
	return t.Click(ctx, x, y, 1)
}

// Click moves the mouse to x, y and clicks count times.
func (t *Tab) Click(ctx context.Context, x, y float64, count int) error {
	if x < 0 || y < 0 || x > ViewportWidth || y > ViewportHeight {
		return fmt.Errorf("(%v, %v) is outside the %dx%d page", x, y, ViewportWidth, ViewportHeight)
	}
	if err := t.Mouse(ctx, MouseEvent{Type: "mouseMoved", X: x, Y: y}); err != nil {
		return err
	}
	for i := 1; i <= count; i++ {
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			if err := t.Mouse(ctx, MouseEvent{Type: typ, X: x, Y: y, Button: "left", ClickCount: i}); err != nil {
				return err
			}
		}
	}
	return nil
}

// MouseEvent is one mouse event, from the model's actions or the panel.
type MouseEvent struct {
	Type       string  `json:"type"` // mouseMoved, mousePressed, mouseReleased, mouseWheel
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Button     string  `json:"button,omitempty"` // left, middle, right
	ClickCount int     `json:"clickCount,omitempty"`
	DeltaX     float64 `json:"deltaX,omitempty"`
	DeltaY     float64 `json:"deltaY,omitempty"`
	Modifiers  int     `json:"modifiers,omitempty"`
	// Buttons are the buttons held, as a bit mask (left 1, right 2,
	// middle 4). A move with a button held is a drag.
	Buttons int `json:"buttons,omitempty"`
}

// Mouse dispatches one mouse event.
func (t *Tab) Mouse(ctx context.Context, e MouseEvent) error {
	switch e.Type {
	case "mouseMoved", "mousePressed", "mouseReleased", "mouseWheel":
	default:
		return fmt.Errorf("unknown mouse event %q", e.Type)
	}
	params := map[string]any{"type": e.Type, "x": e.X, "y": e.Y, "modifiers": e.Modifiers}
	if e.Button != "" {
		params["button"] = e.Button
	} else if e.Type == "mousePressed" || e.Type == "mouseReleased" {
		params["button"] = "left"
	}
	if e.ClickCount > 0 {
		params["clickCount"] = e.ClickCount
	}
	if e.Buttons != 0 {
		params["buttons"] = e.Buttons
	}
	if e.Type == "mouseWheel" {
		params["deltaX"], params["deltaY"] = e.DeltaX, e.DeltaY
	}
	return t.input(ctx, "Input.dispatchMouseEvent", params)
}

// inputAckWait is how long an input event waits for the browser to say
// the page took it.
const inputAckWait = 5 * time.Second

// input sends one input event and waits a while for the browser's
// answer, which comes once the page has handled the event. A click that
// opens a tab in a new window has, on a loaded machine, had that answer
// held back for over a minute although the event had been delivered and
// the new tab had loaded. The event is sent either way, and later
// commands reach the browser after it, so after the wait the call goes
// on as if answered.
func (t *Tab) input(ctx context.Context, method string, params map[string]any) error {
	done := t.conn.Go(ctx, t.session, method, params)
	timer := time.NewTimer(inputAckWait)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Scroll turns the wheel at x, y by dx, dy pixels.
func (t *Tab) Scroll(ctx context.Context, x, y, dx, dy float64) error {
	return t.Mouse(ctx, MouseEvent{Type: "mouseWheel", X: x, Y: y, DeltaX: dx, DeltaY: dy})
}

// Type puts text in at the focused element, as typing would, passwords
// included.
func (t *Tab) Type(ctx context.Context, text string) error {
	if !utf8.ValidString(text) {
		return errors.New("text is not valid UTF-8")
	}
	return t.conn.Call(ctx, t.session, "Input.insertText", map[string]any{"text": text}, nil)
}

// KeyEvent is one keyboard event, from the panel.
type KeyEvent struct {
	Type      string `json:"type"` // keyDown, keyUp, char
	Key       string `json:"key"`
	Code      string `json:"code"`
	Text      string `json:"text,omitempty"`
	KeyCode   int    `json:"keyCode,omitempty"`
	Modifiers int    `json:"modifiers,omitempty"`
	// Commands are editing commands the key stands for ("selectAll").
	// A browser with no window has no menu to turn Cmd+A into one, so on
	// macOS a shortcut is a key press that does nothing unless they come
	// with it.
	Commands []string `json:"commands,omitempty"`
}

// editingCommands is what a shortcut means as an editing command, keyed
// by the lower-case key, for Ctrl (or Cmd) and the key; "Z" is with Shift.
var editingCommands = map[string]string{"a": "selectAll", "c": "copy", "x": "cut", "z": "undo", "Z": "redo", "y": "redo"}

// panelCommands are the editing commands a key event may carry: the
// ones above, and paste, which the panel sends for the person's own
// Cmd+V or Ctrl+V so the page reads the clipboard itself.
var panelCommands = map[string]bool{"selectAll": true, "copy": true, "cut": true, "paste": true, "undo": true, "redo": true}

// commandsFor is the editing command a key with its modifiers stands for,
// if any: Ctrl or Cmd with one of the keys above and nothing else held,
// and with Shift held as well only Z, which is redo. Ctrl+Shift+C is not
// a copy in a browser, and sent as one it put the selection on the
// clipboard under a page that uses the keys for something of its own.
func commandsFor(key string, mods int) []string {
	if mods&(ModCtrl|ModMeta) == 0 || mods&ModAlt != 0 {
		return nil
	}
	k := strings.ToLower(key)
	if mods&ModShift != 0 {
		if k != "z" {
			return nil
		}
		k = "Z"
	}
	if c, ok := editingCommands[k]; ok {
		return []string{c}
	}
	return nil
}

// KeyEvent dispatches one key event as the panel saw it.
//
// The key goes to the page and no further. It carries the key's Windows
// code, which is what a page reads, and no native key code: with one, the
// browser takes the event for a key pressed on its own window, and runs
// its own shortcut for any key the page leaves unused. A browser with no
// window still has them all. Measured, one browser per key, with the
// native code set to the Windows one as it was at first:
//
//   - Windows and Linux, Chrome and Edge: F1 opened the vendor's help
//     site, Ctrl+T a new tab, Ctrl+H the history, Ctrl+J the downloads and
//     Ctrl+U the page's source, each in a tab in front of the
//     conversation's, whose page is then hidden and no longer drawn, so
//     the panel showed a still picture that took no click. Ctrl+W closed
//     the conversation's tab. Ctrl+N and Ctrl+Shift+N opened windows, F12
//     the developer tools. Ctrl+plus zoomed the page and the browser kept
//     that zoom for the site (see ownPreferences for what a zoomed page
//     does to a click).
//   - macOS, where the number is some other key's: Shift alone, Escape,
//     Page Down and every function key opened chrome://settings/help in
//     front of the conversation's tab.
//
// With no native code, none of 54 keys had any effect on the browser, on
// any of the four.
func (t *Tab) KeyEvent(ctx context.Context, e KeyEvent) error {
	switch e.Type {
	case "keyDown", "keyUp", "rawKeyDown", "char":
	default:
		return fmt.Errorf("unknown key event %q", e.Type)
	}
	for _, c := range e.Commands {
		if !panelCommands[c] {
			return fmt.Errorf("unknown editing command %q", c)
		}
	}
	params := map[string]any{"type": e.Type, "key": e.Key, "code": e.Code, "modifiers": e.Modifiers}
	if e.KeyCode != 0 {
		params["windowsVirtualKeyCode"] = e.KeyCode
	}
	if e.Text != "" {
		params["text"] = e.Text
		params["unmodifiedText"] = e.Text
	}
	if len(e.Commands) > 0 {
		params["commands"] = e.Commands
	}
	return t.input(ctx, "Input.dispatchKeyEvent", params)
}

// Modifier bits, as the DevTools protocol numbers them.
const (
	ModAlt   = 1
	ModCtrl  = 2
	ModMeta  = 4
	ModShift = 8
)

type keyDef struct {
	key, code string
	keyCode   int
	text      string
}

var namedKeys = map[string]keyDef{
	"enter": {"Enter", "Enter", 13, "\r"}, "return": {"Enter", "Enter", 13, "\r"},
	"tab": {"Tab", "Tab", 9, ""}, "escape": {"Escape", "Escape", 27, ""}, "esc": {"Escape", "Escape", 27, ""},
	"backspace": {"Backspace", "Backspace", 8, ""}, "delete": {"Delete", "Delete", 46, ""},
	"space":   {" ", "Space", 32, " "},
	"arrowup": {"ArrowUp", "ArrowUp", 38, ""}, "up": {"ArrowUp", "ArrowUp", 38, ""},
	"arrowdown": {"ArrowDown", "ArrowDown", 40, ""}, "down": {"ArrowDown", "ArrowDown", 40, ""},
	"arrowleft": {"ArrowLeft", "ArrowLeft", 37, ""}, "left": {"ArrowLeft", "ArrowLeft", 37, ""},
	"arrowright": {"ArrowRight", "ArrowRight", 39, ""}, "right": {"ArrowRight", "ArrowRight", 39, ""},
	"home": {"Home", "Home", 36, ""}, "end": {"End", "End", 35, ""},
	"pageup": {"PageUp", "PageUp", 33, ""}, "pagedown": {"PageDown", "PageDown", 34, ""},
	"plus": {"+", "Equal", 187, "+"}, "minus": {"-", "Minus", 189, "-"},
}

// keyPlace is where a key is on the keyboard: the code and the Windows
// key code a page reads to know which key was pressed.
type keyPlace struct {
	code    string
	keyCode int
}

// punctuation is where the keys that are neither letters nor digits are
// on a US keyboard, by the character each types alone or with Shift. A
// page that takes Ctrl+/ or Ctrl+plus as a shortcut often reads the key
// code, and a key sent with none was not one it knew.
var punctuation = map[rune]keyPlace{
	'=': {"Equal", 187}, '+': {"Equal", 187}, '-': {"Minus", 189}, '_': {"Minus", 189},
	',': {"Comma", 188}, '<': {"Comma", 188}, '.': {"Period", 190}, '>': {"Period", 190},
	'/': {"Slash", 191}, '?': {"Slash", 191}, ';': {"Semicolon", 186}, ':': {"Semicolon", 186},
	'\'': {"Quote", 222}, '"': {"Quote", 222}, '`': {"Backquote", 192}, '~': {"Backquote", 192},
	'[': {"BracketLeft", 219}, '{': {"BracketLeft", 219}, ']': {"BracketRight", 221}, '}': {"BracketRight", 221},
	'\\': {"Backslash", 220}, '|': {"Backslash", 220},
	')': {"Digit0", 48}, '!': {"Digit1", 49}, '@': {"Digit2", 50}, '#': {"Digit3", 51}, '$': {"Digit4", 52},
	'%': {"Digit5", 53}, '^': {"Digit6", 54}, '&': {"Digit7", 55}, '*': {"Digit8", 56}, '(': {"Digit9", 57},
}

// parseKeys reads a key combination such as "Enter", "ctrl+a",
// "shift+Tab" or "cmd+l". The key is what follows the last "+", and the
// plus key itself when the combination ends in one: "ctrl++", which is
// also "ctrl+plus".
func parseKeys(combo string) (keyDef, int, error) {
	last := combo
	var held []string
	switch i := strings.LastIndex(combo, "+"); {
	case combo == "+":
	case len(combo) > 2 && strings.HasSuffix(combo, "++"):
		last, held = "+", strings.Split(combo[:len(combo)-2], "+")
	case i >= 0:
		last, held = combo[i+1:], strings.Split(combo[:i], "+")
	}
	mods := 0
	for _, m := range held {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "ctrl", "control":
			mods |= ModCtrl
		case "alt", "option":
			mods |= ModAlt
		case "shift":
			mods |= ModShift
		case "cmd", "meta", "command", "super":
			mods |= ModMeta
		default:
			return keyDef{}, 0, fmt.Errorf("unknown modifier %q in %q", m, combo)
		}
	}
	// With Ctrl, Cmd or Alt held a key is a shortcut and types nothing:
	// Ctrl+Enter is a key press, not a line break, as it is from the panel.
	shortcut := mods&(ModCtrl|ModMeta|ModAlt) != 0
	last = strings.TrimSpace(last)
	if def, ok := namedKeys[strings.ToLower(last)]; ok {
		if shortcut {
			def.text = ""
		}
		return def, mods, nil
	}
	if len(last) >= 2 && (last[0] == 'f' || last[0] == 'F') {
		if n, err := strconv.Atoi(last[1:]); err == nil && n >= 1 && n <= 12 {
			name := "F" + strconv.Itoa(n)
			return keyDef{name, name, 111 + n, ""}, mods, nil
		}
	}
	if r, size := utf8.DecodeRuneInString(last); size == len(last) && size > 0 {
		upper := strings.ToUpper(last)
		def := keyDef{key: last, text: last}
		if r < 128 && (('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')) {
			def.code, def.keyCode = "Key"+upper, int(upper[0])
		} else if '0' <= r && r <= '9' {
			def.code, def.keyCode = "Digit"+last, int(r)
		} else if at, ok := punctuation[r]; ok {
			def.code, def.keyCode = at.code, at.keyCode
		}
		if shortcut {
			def.text = ""
		}
		return def, mods, nil
	}
	return keyDef{}, 0, fmt.Errorf("unknown key %q", last)
}

// Press presses and releases a key combination such as "Enter" or
// "ctrl+a".
func (t *Tab) Press(ctx context.Context, combo string) error {
	def, mods, err := parseKeys(combo)
	if err != nil {
		return err
	}
	down := KeyEvent{Type: "keyDown", Key: def.key, Code: def.code, KeyCode: def.keyCode, Modifiers: mods, Text: def.text, Commands: commandsFor(def.key, mods)}
	if def.text == "" {
		down.Type = "rawKeyDown"
	}
	if err := t.KeyEvent(ctx, down); err != nil {
		return err
	}
	return t.KeyEvent(ctx, KeyEvent{Type: "keyUp", Key: def.key, Code: def.code, KeyCode: def.keyCode, Modifiers: mods})
}
