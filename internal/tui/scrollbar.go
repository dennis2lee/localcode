package tui

import "math"

// A scrollbar down the transcript's right edge, behind the "mouse"
// switch. Which cell is an arrow, which rows are track, where the thumb
// sits and how long it is are a pure function of the layout below, so a
// test can feed coordinates in and read the action out with no terminal,
// no rendering, and no Update loop.
//
// The scrollbar owns the transcript's last column: the row the viewport
// starts on holds an up arrow, its last row a down arrow, and the rows
// between are the track the thumb moves in.
type scrollbarGeometry struct {
	width  int // terminal width in cells; the scrollbar is column width-1
	top    int // screen row the viewport starts on
	height int // viewport height in rows
	total  int // transcript lines at the width they are laid out in
	offset int // current first-visible-line offset into those lines
}

// scrollbarMinThumb is the shortest a thumb ever gets. A transcript far
// longer than the screen would round its thumb to zero rows, which would
// leave nothing to grab, so one row is the floor rather than nothing.
const scrollbarMinThumb = 1

// scrollbarHitKind names what a click on the scrollbar means, in the
// words the transcript answers to rather than as a row number.
type scrollbarHitKind int

const (
	scrollbarHitNone scrollbarHitKind = iota
	scrollbarHitLineUp
	scrollbarHitLineDown
	scrollbarHitPageUp
	scrollbarHitPageDown
	scrollbarHitThumb
)

// scrollbarHit is what a click at one cell resolves to. A thumb hit
// starts a drag; the motion handler keeps the pressed row inside the
// thumb from the grab offset stored at press time.
type scrollbarHit struct {
	kind scrollbarHitKind
}

// scrollbarTrackLen is how many rows between the two arrows belong to
// the track. Two are always arrows, so a viewport shorter than three
// rows has no track at all.
func scrollbarTrackLen(height int) int {
	return height - 2
}

// scrollbarVisible reports whether the layout earns a scrollbar: the
// switch is checked by the caller, and here a transcript that fits its
// viewport has none, whatever else is true.
func scrollbarVisible(g scrollbarGeometry) bool {
	return g.width > 1 && g.height > 0 && g.total > g.height
}

// scrollbarThumb returns the thumb's length in track rows and its start
// as a track-relative row. Length is proportional to the share of the
// transcript on screen, clamped so a transcript one line taller than the
// viewport still leaves somewhere to drag to: the thumb never fills the
// whole track while there is anywhere to go.
func scrollbarThumb(g scrollbarGeometry) (length, start int) {
	track := scrollbarTrackLen(g.height)
	if track <= 0 {
		return 0, 0
	}
	maxOffset := g.total - g.height
	if maxOffset <= 0 {
		return 0, 0
	}
	proportional := int(math.Round(float64(g.height*g.height) / float64(g.total)))
	ceiling := track - 1
	if ceiling < scrollbarMinThumb {
		// No room for both a thumb and somewhere to drag it: the
		// thumb fills the track and the arrows do the moving.
		ceiling = track
	}
	length = min(max(proportional, scrollbarMinThumb), ceiling)
	offset := min(max(g.offset, 0), maxOffset)
	travel := track - length
	if travel <= 0 {
		return length, 0
	}
	// The full travel is reached exactly at the bottom, so the thumb
	// parks against the down arrow rather than stopping one row short.
	start = int(math.Round(float64(offset) * float64(travel) / float64(maxOffset)))
	return length, start
}

// scrollbarFractionForRow maps a screen row to a content fraction, 0 at
// the top of the track and 1 at the bottom. Rows past either end clamp
// rather than wrap, so dragging past the track parks at the transcript's
// end instead of jumping somewhere else.
func scrollbarFractionForRow(y, trackTop, trackLen int) float64 {
	if trackLen <= 1 {
		return 0
	}
	f := float64(y-trackTop) / float64(trackLen-1)
	return min(max(f, 0), 1)
}

// scrollbarOffsetForFraction maps a content fraction back to a
// first-visible-line offset, clamped into what the viewport can show.
func scrollbarOffsetForFraction(f float64, total, height int) int {
	maxOffset := total - height
	if maxOffset <= 0 {
		return 0
	}
	f = min(max(f, 0), 1)
	return int(math.Round(f * float64(maxOffset)))
}

// resolveScrollbarClick reads what a click at terminal cell (x, y) asks
// the transcript to do. Anything that is not an arrow, the track, or the
// thumb resolves to none, and none means the click is not the
// scrollbar's: the caller must leave it alone rather than scrolling
// somewhere it never pointed.
func resolveScrollbarClick(x, y int, g scrollbarGeometry) scrollbarHit {
	if !scrollbarVisible(g) {
		return scrollbarHit{kind: scrollbarHitNone}
	}
	if x != g.width-1 {
		return scrollbarHit{kind: scrollbarHitNone}
	}
	row := y - g.top
	if row < 0 || row >= g.height {
		return scrollbarHit{kind: scrollbarHitNone}
	}
	if row == 0 {
		return scrollbarHit{kind: scrollbarHitLineUp}
	}
	if row == g.height-1 {
		return scrollbarHit{kind: scrollbarHitLineDown}
	}
	trackTop := g.top + 1
	length, start := scrollbarThumb(g)
	if thumbRow := y - trackTop; thumbRow >= start && thumbRow < start+length {
		return scrollbarHit{kind: scrollbarHitThumb}
	}
	if y-trackTop < start {
		return scrollbarHit{kind: scrollbarHitPageUp}
	}
	return scrollbarHit{kind: scrollbarHitPageDown}
}

// scrollbarWheelDelta is how many lines one wheel notch moves. The same
// number bubbles scrolls by default, so the wheel answers the way the
// toolkit's own widgets do.
const scrollbarWheelDelta = 3

// fullWidth is the terminal width the scrollbar column is measured
// against. Before the first resize there is no terminal width yet, and
// the viewport's own (unset, defaulted at construction) is the stand-in.
func (m Model) fullWidth() int {
	if m.termWidth > 0 {
		return m.termWidth
	}
	if m.viewport.Width() > 0 {
		return m.viewport.Width()
	}
	return fallbackWidth
}

// scrollbarLayout builds the click geometry for the current frame and
// reports whether the scrollbar is drawn at all: the switch on, a
// transcript taller than its viewport, and no picker covering the room
// the scrollbar would stand in.
func (m Model) scrollbarLayout() (scrollbarGeometry, bool) {
	g := scrollbarGeometry{
		width:  m.fullWidth(),
		top:    0,
		height: m.viewport.Height(),
		total:  m.viewport.TotalLineCount(),
		offset: m.viewport.YOffset(),
	}
	if !m.mouseEnabled || m.picker != nil {
		return g, false
	}
	if !scrollbarVisible(g) {
		return g, false
	}
	return g, true
}

// overlayScrollbar draws the arrows, track, and thumb down the
// transcript's last column. lines starts at the viewport's first row, so
// viewport row i is lines[i] whatever screen row the viewport starts on.
func overlayScrollbar(lines []string, g scrollbarGeometry) []string {
	length, start := scrollbarThumb(g)
	for i := 0; i < g.height && i < len(lines); i++ {
		var cell string
		switch {
		case i == 0:
			cell = "▲"
		case i == g.height-1:
			cell = "▼"
		case i-1 >= start && i-1 < start+length:
			cell = "█"
		default:
			cell = "│"
		}
		lines[i] += cell
	}
	return lines
}

// handleScrollbarClick answers a mouse press. Clicks that are not the
// scrollbar's resolve to none and are left alone; anything else moves
// the transcript, or starts a drag the motion handler below continues.
func (m Model) handleScrollbarClick(x, y int, left bool) Model {
	g, ok := m.scrollbarLayout()
	if !ok || !left {
		return m
	}
	switch hit := resolveScrollbarClick(x, y, g); hit.kind {
	case scrollbarHitLineUp:
		m.viewport.ScrollUp(1)
	case scrollbarHitLineDown:
		m.viewport.ScrollDown(1)
	case scrollbarHitPageUp:
		m.viewport.PageUp()
	case scrollbarHitPageDown:
		m.viewport.PageDown()
	case scrollbarHitThumb:
		_, start := scrollbarThumb(g)
		m.scrollbarDragGrab = (y - (g.top + 1)) - start
		m.scrollbarDragging = true
	default:
		return m
	}
	return m
}

// handleScrollbarMotion moves the transcript while a thumb drag is held.
// The drag is relative: the thumb keeps the grab offset the press
// stored, so the pressed row stays inside the thumb while it moves and
// the content follows the pointer's movement rather than its absolute
// position. Rows past either end of the track clamp at the transcript's
// ends.
func (m Model) handleScrollbarMotion(y int) Model {
	if !m.scrollbarDragging {
		return m
	}
	g, ok := m.scrollbarLayout()
	if !ok {
		m.scrollbarDragging = false
		m.scrollbarDragGrab = 0
		return m
	}
	track := scrollbarTrackLen(g.height)
	length, _ := scrollbarThumb(g)
	travel := track - length
	if travel <= 0 {
		return m
	}
	grab := m.scrollbarDragGrab
	if grab < 0 {
		grab = 0
	}
	if grab > length-1 {
		grab = length - 1
	}
	// The thumb's top row travels travel rows, so the desired top maps
	// back through the same helpers over travel+1 slots. The helpers
	// clamp, which parks drags past either end at the transcript's ends.
	desired := (y - (g.top + 1)) - grab
	m.viewport.SetYOffset(scrollbarOffsetForFraction(
		scrollbarFractionForRow(desired+g.top+1, g.top+1, travel+1), g.total, g.height))
	return m
}

// handleScrollbarWheel answers the wheel while reporting is on. The
// terminal sends wheel events whether or not anything wants them once
// reporting is enabled, and a wheel that does nothing reads as broken,
// so it scrolls the transcript a few lines either way.
func (m Model) handleScrollbarWheel(up bool) Model {
	if _, ok := m.scrollbarLayout(); !ok {
		return m
	}
	if up {
		m.viewport.ScrollUp(scrollbarWheelDelta)
	} else {
		m.viewport.ScrollDown(scrollbarWheelDelta)
	}
	return m
}
