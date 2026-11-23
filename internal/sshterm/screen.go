package sshterm

import (
	"fmt"
	"image/color"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// What the panel draws and the model reads.
//
// A line is a list of runs of cells that share a style. Colors are ""
// for the terminal's default, "0" to "15" for the sixteen colors the
// panel's stylesheet maps to its theme, or "#rrggbb". A wide character
// (Hangul, CJK) is a run of its own with Wide set, so the panel can give
// it two cells and keep the columns aligned.

// Run is cells that share a style.
type Run struct {
	Text  string `json:"t"`
	Fg    string `json:"f,omitempty"`
	Bg    string `json:"b,omitempty"`
	Flags uint8  `json:"a,omitempty"`
	// Wide is a run of one character two cells wide.
	Wide bool `json:"w,omitempty"`
}

// Flags, as the panel reads them.
const (
	FlagBold uint8 = 1 << iota
	FlagFaint
	FlagItalic
	FlagUnderline
	FlagReverse
	FlagStrike
	FlagConceal
	FlagBlink
)

// Line is one row of the screen or of the scrollback.
type Line []Run

// Screen is a snapshot of a terminal's screen.
type Screen struct {
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	Lines   []Line `json:"lines"`
	CursorX int    `json:"cx"`
	CursorY int    `json:"cy"`
	Cursor  bool   `json:"cursor"`
	Alt     bool   `json:"alt"`
	Title   string `json:"title,omitempty"`
}

func colorString(c color.Color) string {
	switch v := c.(type) {
	case nil:
		return ""
	case ansi.BasicColor:
		return fmt.Sprint(uint8(v))
	case ansi.IndexedColor:
		if v < 16 {
			return fmt.Sprint(uint8(v))
		}
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

func flagsOf(s *uv.Style) uint8 {
	var f uint8
	if s.Attrs&uv.AttrBold != 0 {
		f |= FlagBold
	}
	if s.Attrs&uv.AttrFaint != 0 {
		f |= FlagFaint
	}
	if s.Attrs&uv.AttrItalic != 0 {
		f |= FlagItalic
	}
	if s.Attrs&uv.AttrReverse != 0 {
		f |= FlagReverse
	}
	if s.Attrs&uv.AttrStrikethrough != 0 {
		f |= FlagStrike
	}
	if s.Attrs&uv.AttrConceal != 0 {
		f |= FlagConceal
	}
	if s.Attrs&(uv.AttrBlink|uv.AttrRapidBlink) != 0 {
		f |= FlagBlink
	}
	if s.Underline != uv.UnderlineStyleNone {
		f |= FlagUnderline
	}
	return f
}

// lineOf turns width cells, read by at, into runs. Cells after the last
// one with content or a background are left out.
func lineOf(width int, at func(x int) *uv.Cell) Line {
	var out Line
	cur := -1 // the run a narrow cell of the same style extends
	end := 0
	for x := 0; x < width; x++ {
		c := at(x)
		if c != nil && (c.Content != "" && c.Content != " " || c.Style.Bg != nil || c.Style.Attrs&uv.AttrReverse != 0) {
			end = x + 1
		}
	}
	for x := 0; x < end; x++ {
		c := at(x)
		if c == nil {
			c = &uv.EmptyCell
		}
		if c.Width == 0 && c.Content == "" {
			// The second half of a wide character.
			continue
		}
		text := c.Content
		if text == "" {
			text = " "
		}
		fg, bg, flags := colorString(c.Style.Fg), colorString(c.Style.Bg), flagsOf(&c.Style)
		if c.Width > 1 {
			out = append(out, Run{Text: text, Fg: fg, Bg: bg, Flags: flags, Wide: true})
			cur = -1
			continue
		}
		if cur >= 0 && out[cur].Fg == fg && out[cur].Bg == bg && out[cur].Flags == flags {
			out[cur].Text += text
			continue
		}
		out = append(out, Run{Text: text, Fg: fg, Bg: bg, Flags: flags})
		cur = len(out) - 1
	}
	return out
}

// Text is a line as plain text, trailing spaces trimmed.
func (l Line) Text() string {
	var b strings.Builder
	for _, r := range l {
		if r.Flags&FlagConceal != 0 {
			b.WriteString(strings.Repeat(" ", len([]rune(r.Text))))
			continue
		}
		b.WriteString(r.Text)
	}
	return strings.TrimRight(b.String(), " ")
}
