package browser

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	if e.Type == "mouseWheel" {
		params["deltaX"], params["deltaY"] = e.DeltaX, e.DeltaY
	}
	return t.conn.Call(ctx, t.session, "Input.dispatchMouseEvent", params, nil)
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
}

// KeyEvent dispatches one key event as the panel saw it.
func (t *Tab) KeyEvent(ctx context.Context, e KeyEvent) error {
	switch e.Type {
	case "keyDown", "keyUp", "rawKeyDown", "char":
	default:
		return fmt.Errorf("unknown key event %q", e.Type)
	}
	params := map[string]any{"type": e.Type, "key": e.Key, "code": e.Code, "modifiers": e.Modifiers}
	if e.KeyCode != 0 {
		params["windowsVirtualKeyCode"] = e.KeyCode
		params["nativeVirtualKeyCode"] = e.KeyCode
	}
	if e.Text != "" {
		params["text"] = e.Text
		params["unmodifiedText"] = e.Text
	}
	return t.conn.Call(ctx, t.session, "Input.dispatchKeyEvent", params, nil)
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
}

// parseKeys reads a key combination such as "Enter", "ctrl+a",
// "shift+Tab" or "cmd+l".
func parseKeys(combo string) (keyDef, int, error) {
	parts := strings.Split(combo, "+")
	mods := 0
	for _, m := range parts[:len(parts)-1] {
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
	last := strings.TrimSpace(parts[len(parts)-1])
	if def, ok := namedKeys[strings.ToLower(last)]; ok {
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
		}
		if mods&(ModCtrl|ModMeta|ModAlt) != 0 {
			def.text = "" // a shortcut, not a character
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
	down := KeyEvent{Type: "keyDown", Key: def.key, Code: def.code, KeyCode: def.keyCode, Modifiers: mods, Text: def.text}
	if def.text == "" {
		down.Type = "rawKeyDown"
	}
	if err := t.KeyEvent(ctx, down); err != nil {
		return err
	}
	return t.KeyEvent(ctx, KeyEvent{Type: "keyUp", Key: def.key, Code: def.code, KeyCode: def.keyCode, Modifiers: mods})
}
