package vt

import (
	"io"
	"testing"
	"time"
)

// localcode: see LOCALCODE.md.

func TestACursorSavedPastANarrowerScreenDoesNotPanic(t *testing.T) {
	for name, c := range map[string][2]string{
		"restore, then set a tab":                 {"\x1b[100G\x1b7", "\x1b8\x1bH"},
		"restore, then clear a tab":               {"\x1b[100G\x1b7", "\x1b8\x1b[0g"},
		"full screen opened wide, quit narrow":    {"\x1b[100G\x1b[?1049h", "\x1b[?1049l\x1bH"},
		"full screen opened wide, then a tab":     {"\x1b[100G\x1b[?1049h", "\x1b[?1049l\tx"},
		"full screen opened wide, then text":      {"\x1b[100G\x1b[?1049h", "\x1b[?1049lhello\r\n"},
		"full screen opened wide, then clear tab": {"\x1b[100G\x1b[?1049h", "\x1b[?1049l\x1b[0g"},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panic %v", name, r)
				}
			}()
			e := NewEmulator(100, 30)
			go io.Copy(io.Discard, e)
			e.WriteString(c[0])
			e.Resize(40, 10)
			e.WriteString(c[1])
			if p := e.CursorPosition(); p.X >= 40 || p.Y >= 10 {
				t.Errorf("%s: cursor at %v on a 40x10 screen", name, p)
			}
		}()
	}
}

func TestARepeatCountDoesNotOutlastTheScreen(t *testing.T) {
	e := NewEmulator(80, 24)
	go io.Copy(io.Discard, e)
	start := time.Now()
	e.WriteString("a\x1b[99999999b\x1b[99999999S\x1b[99999999T")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestMarginsPastTheScreenKeepTheCursorOnIt(t *testing.T) {
	for _, in := range []string{
		"\x1b[5;99999r\x1b[S",
		"\x1b[5;99999r\x1b[10T",
		"\x1b[5;99999r\x1b[99H\n",
		"\x1b[5;99999r\x1bM\x1b[L\x1b[M",
		"\x1b[?69h\x1b[2;999s\x1b[999C",
		"\x1b[?69h\x1b[2;999s\x1b[?6h\x1b[1;999H",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%q: panic %v", in, r)
				}
			}()
			e := NewEmulator(80, 24)
			go io.Copy(io.Discard, e)
			e.WriteString(in)
			if p := e.CursorPosition(); p.X < 0 || p.X >= 80 || p.Y < 0 || p.Y >= 24 {
				t.Errorf("%q: cursor at %v on an 80x24 screen", in, p)
			}
		}()
	}
}

func TestBracketedPasteSaysWhatPasteGoesBy(t *testing.T) {
	e := NewEmulator(80, 24)
	go io.Copy(io.Discard, e)
	if e.BracketedPaste() {
		t.Fatal("on before the application asked")
	}
	e.Write([]byte("\x1b[?2004h"))
	if !e.BracketedPaste() {
		t.Fatal("off after the application turned it on")
	}
	e.Write([]byte("\x1b[?2004l"))
	if e.BracketedPaste() {
		t.Fatal("on after the application turned it off")
	}
}

// A character written in the last column leaves a wrap pending; a cursor
// moved to the last column does not, and neither does the next character
// once it wrapped.
func TestPendingWrapSaysWhetherTheLastColumnHoldsACharacter(t *testing.T) {
	e := NewEmulator(10, 3)
	go io.Copy(io.Discard, e)
	e.Write([]byte("\x1b[10G"))
	if e.PendingWrap() {
		t.Fatal("pending after the cursor was moved to the last column")
	}
	e.Write([]byte("\r123456789"))
	if e.PendingWrap() {
		t.Fatal("pending with the last column still blank")
	}
	e.Write([]byte("0"))
	if !e.PendingWrap() {
		t.Fatal("not pending after a character in the last column")
	}
	if x := e.CursorPosition().X; x != 9 {
		t.Fatalf("cursor at %d, want on the last column", x)
	}
	e.Write([]byte("a"))
	if e.PendingWrap() {
		t.Fatal("still pending after the next character wrapped")
	}
}
