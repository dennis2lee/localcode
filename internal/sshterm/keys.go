package sshterm

import (
	"fmt"
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
)

// Keys, from the model's grammar and from the panel's key events, as the
// emulator's key events. The emulator encodes them with the terminal's
// modes: an arrow key in a full-screen program is not the arrow key at a
// prompt.

var namedKeys = map[string]rune{
	"enter": uv.KeyEnter, "return": uv.KeyEnter,
	"tab":    uv.KeyTab,
	"escape": uv.KeyEscape, "esc": uv.KeyEscape,
	"backspace": uv.KeyBackspace,
	"delete":    uv.KeyDelete, "del": uv.KeyDelete,
	"insert": uv.KeyInsert,
	"space":  uv.KeySpace,
	"up":     uv.KeyUp, "arrowup": uv.KeyUp,
	"down": uv.KeyDown, "arrowdown": uv.KeyDown,
	"left": uv.KeyLeft, "arrowleft": uv.KeyLeft,
	"right": uv.KeyRight, "arrowright": uv.KeyRight,
	"home":   uv.KeyHome,
	"end":    uv.KeyEnd,
	"pageup": uv.KeyPgUp, "pgup": uv.KeyPgUp,
	"pagedown": uv.KeyPgDown, "pgdown": uv.KeyPgDown,
	"f1": uv.KeyF1, "f2": uv.KeyF2, "f3": uv.KeyF3, "f4": uv.KeyF4,
	"f5": uv.KeyF5, "f6": uv.KeyF6, "f7": uv.KeyF7, "f8": uv.KeyF8,
	"f9": uv.KeyF9, "f10": uv.KeyF10, "f11": uv.KeyF11, "f12": uv.KeyF12,
}

// ParseKeys reads keys as the model writes them: one combination, or
// several separated by spaces: "ctrl+c", "Escape", "Up Up Enter" (three
// presses). A single character with no modifier types that character.
func ParseKeys(s string) ([]uv.KeyPressEvent, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("no keys")
	}
	var out []uv.KeyPressEvent
	for _, f := range fields {
		ev, err := parseCombo(f)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func parseCombo(combo string) (uv.KeyPressEvent, error) {
	last := combo
	var held []string
	switch i := strings.LastIndex(combo, "+"); {
	case combo == "+":
	case len(combo) > 2 && strings.HasSuffix(combo, "++"):
		last, held = "+", strings.Split(combo[:len(combo)-2], "+")
	case i >= 0:
		last, held = combo[i+1:], strings.Split(combo[:i], "+")
	}
	var mod uv.KeyMod
	for _, m := range held {
		switch strings.ToLower(m) {
		case "ctrl", "control":
			mod |= uv.ModCtrl
		case "alt", "option", "meta":
			mod |= uv.ModAlt
		case "shift":
			mod |= uv.ModShift
		default:
			return uv.KeyPressEvent{}, fmt.Errorf("unknown modifier %q in %q", m, combo)
		}
	}
	if code, ok := namedKeys[strings.ToLower(last)]; ok {
		return uv.KeyPressEvent{Code: code, Mod: mod}, nil
	}
	if strings.EqualFold(last, "plus") {
		last = "+"
	}
	r, size := utf8.DecodeRuneInString(last)
	if size != len(last) || size == 0 {
		return uv.KeyPressEvent{}, fmt.Errorf("unknown key %q", last)
	}
	// A control character is a key by its name (ctrl+o, Enter), never as
	// itself: as itself it would pass for text.
	if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
		return uv.KeyPressEvent{}, fmt.Errorf("control character %U: name the key (ctrl+o, Enter, Escape)", r)
	}
	if mod == 0 || mod == uv.ModShift {
		return uv.KeyPressEvent{Code: r, Text: last}, nil
	}
	if mod&uv.ModCtrl != 0 && 'A' <= r && r <= 'Z' {
		r += 'a' - 'A'
	}
	return uv.KeyPressEvent{Code: r, Mod: mod}, nil
}

// personKeyEvent is a panel key as an emulator key event. A key that
// types text (no Ctrl, Alt or Cmd held) carries its text. ok is false for
// a key the terminal has no use for (a lone modifier).
func personKeyEvent(k PersonKey) (uv.KeyPressEvent, bool) {
	var mod uv.KeyMod
	if k.Ctrl {
		mod |= uv.ModCtrl
	}
	if k.Alt {
		mod |= uv.ModAlt
	}
	if k.Shift {
		mod |= uv.ModShift
	}
	if k.Meta {
		// Cmd on macOS is the window's, not the terminal's.
		return uv.KeyPressEvent{}, false
	}
	switch k.Key {
	case "Shift", "Control", "Alt", "Meta", "CapsLock", "Dead", "Unidentified", "Process", "":
		return uv.KeyPressEvent{}, false
	}
	if code, ok := namedKeys[strings.ToLower(k.Key)]; ok {
		if code == uv.KeySpace && mod&(uv.ModCtrl|uv.ModAlt) == 0 {
			return uv.KeyPressEvent{Code: ' ', Text: " "}, true
		}
		// Shift+Tab is a key of its own; Shift on the others is
		// carried as a modifier.
		return uv.KeyPressEvent{Code: code, Mod: mod}, true
	}
	r, size := utf8.DecodeRuneInString(k.Key)
	if size != len(k.Key) || size == 0 {
		return uv.KeyPressEvent{}, false
	}
	if mod&(uv.ModCtrl|uv.ModAlt) == 0 {
		return uv.KeyPressEvent{Code: r, Text: k.Key}, true
	}
	if 'A' <= r && r <= 'Z' {
		r += 'a' - 'A'
	}
	return uv.KeyPressEvent{Code: r, Mod: mod &^ uv.ModShift}, true
}

// xterm's numbers for the keys sent as CSI n ~, and the final letters of
// those sent as CSI 1;m X.
var (
	tildeKeys = map[rune]int{
		uv.KeyInsert: 2, uv.KeyDelete: 3, uv.KeyPgUp: 5, uv.KeyPgDown: 6,
		uv.KeyF5: 15, uv.KeyF6: 17, uv.KeyF7: 18, uv.KeyF8: 19,
		uv.KeyF9: 20, uv.KeyF10: 21, uv.KeyF11: 23, uv.KeyF12: 24,
	}
	letterKeys = map[rune]byte{
		uv.KeyUp: 'A', uv.KeyDown: 'B', uv.KeyRight: 'C', uv.KeyLeft: 'D',
		uv.KeyEnd: 'F', uv.KeyHome: 'H',
		uv.KeyF1: 'P', uv.KeyF2: 'Q', uv.KeyF3: 'R', uv.KeyF4: 'S',
	}
	// ctrlPunct are the control characters Ctrl with a punctuation key
	// makes.
	ctrlPunct = map[rune]string{
		'@': "\x00", '[': "\x1b", '\\': "\x1c", ']': "\x1d", '^': "\x1e", '_': "\x1f", '/': "\x1f", '?': "\x7f",
		'2': "\x00", '3': "\x1b", '4': "\x1c", '5': "\x1d", '6': "\x1e", '7': "\x1f", '8': "\x7f",
	}
)

// encodeSpecial encodes the keys the emulator does not: a special key
// with a modifier held, in xterm's form (Ctrl+Left is ESC[1;5D, Ctrl+Delete
// ESC[3;5~), Shift+Tab, Enter and Backspace with a modifier, and Ctrl with
// punctuation. ok is false for every other key, which the emulator
// encodes with the terminal's modes.
func encodeSpecial(ev uv.KeyPressEvent) (string, bool) {
	mod := ev.Mod & (uv.ModShift | uv.ModAlt | uv.ModCtrl)
	switch ev.Code {
	case uv.KeyTab:
		if mod == uv.ModShift {
			return "\x1b[Z", true
		}
		return "", false
	case uv.KeyEnter:
		if mod == 0 {
			return "", false
		}
		if mod&uv.ModAlt != 0 {
			return "\x1b\r", true
		}
		return "\r", true
	case uv.KeyBackspace:
		switch {
		case mod == 0:
			return "", false
		case mod&uv.ModCtrl != 0:
			return "\x08", true
		case mod&uv.ModAlt != 0:
			return "\x1b\x7f", true
		}
		return "\x7f", true
	}
	if mod == 0 {
		return "", false
	}
	m := 1
	if mod&uv.ModShift != 0 {
		m += 1
	}
	if mod&uv.ModAlt != 0 {
		m += 2
	}
	if mod&uv.ModCtrl != 0 {
		m += 4
	}
	if n, ok := tildeKeys[ev.Code]; ok {
		return fmt.Sprintf("\x1b[%d;%d~", n, m), true
	}
	if c, ok := letterKeys[ev.Code]; ok {
		return fmt.Sprintf("\x1b[1;%d%c", m, c), true
	}
	if mod&uv.ModCtrl != 0 {
		if s, ok := ctrlPunct[ev.Code]; ok {
			if mod&uv.ModAlt != 0 {
				return "\x1b" + s, true
			}
			return s, true
		}
	}
	return "", false
}
