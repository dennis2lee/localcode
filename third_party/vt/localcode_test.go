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
