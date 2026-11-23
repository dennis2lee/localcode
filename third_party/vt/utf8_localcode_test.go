package vt

import (
	"io"
	"testing"
)

// localcode: see LOCALCODE.md.
func TestWideCharactersWrapAtTheMargin(t *testing.T) {
	row := func(e *Emulator, y int) string {
		s := ""
		for x := 0; x < e.Width(); x++ {
			if c := e.CellAt(x, y); c != nil {
				s += c.Content
			}
		}
		return s
	}
	for _, c := range []struct {
		name, in string
		want     [2]string
	}{
		{"a wide character ending at the margin, then a space and a return", "abcd안 \r\x1b[Kxy", [2]string{"abcd안", "xy    "}},
		{"a wide character ending at the margin, then another character", "abcd안x", [2]string{"abcd안", "x     "}},
		{"a wide character with one column left", "abcde안", [2]string{"abcde ", "안    "}},
		{"narrow characters, as before", "abcdefg", [2]string{"abcdef", "g     "}},
	} {
		e := NewEmulator(6, 3)
		go io.Copy(io.Discard, e)
		e.WriteString(c.in)
		got := [2]string{row(e, 0), row(e, 1)}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
