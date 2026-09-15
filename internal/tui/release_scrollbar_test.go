package tui

import (
	"math"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// Release verification for the mouse-driven scrollbar behind the
// "mouse" switch. Everything here drives real messages through Update
// and asserts what the reader would see: where the transcript sits,
// what the frame draws, and whether the frame asks the terminal for
// the mouse at all.

// scrollbarConversation returns a model whose transcript overflows its
// viewport, with the switch as given. The transcript is 200 user turns,
// so the thumb parks at the bottom of the track on arrival.
func scrollbarConversation(t *testing.T, mouse bool) Model {
	t.Helper()
	m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), mouse)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	for i := 0; i < 200; i++ {
		m.applyEvent(events.Event{
			Type: events.TypeUserMessage,
			Data: map[string]any{"text": "message number " + string(rune('a'+i%26))},
		})
	}
	m.refreshViewport()
	if m.viewport.TotalLineCount() <= m.viewport.Height() {
		t.Fatalf("transcript is %d lines in a %d-line viewport: nothing to scroll",
			m.viewport.TotalLineCount(), m.viewport.Height())
	}
	if !m.viewport.AtBottom() {
		t.Fatal("a fresh transcript should be at the bottom")
	}
	return m
}

// scrollbarThumbRow reads which screen row the thumb is on out of the
// frame itself, the way a reader would point at it, rather than asking
// the geometry for an index.
func scrollbarThumbRow(t *testing.T, m Model) int {
	t.Helper()
	for i, line := range strings.Split(m.View().Content, "\n") {
		if strings.Contains(line, "█") {
			return i
		}
	}
	t.Fatal("the frame draws no thumb, so there is nothing to press")
	return -1
}

func clickAt(m Model, x, y int) Model {
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return updated.(Model)
}

// scrollbarShortConversation returns a model with a short transcript,
// so the thumb is several rows long and there is an inside to grab.
// scrollbarConversation's 200 turns round the thumb to one row.
func scrollbarShortConversation(t *testing.T, mouse bool, turns int) Model {
	t.Helper()
	m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), mouse)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	for i := 0; i < turns; i++ {
		m.applyEvent(events.Event{
			Type: events.TypeUserMessage,
			Data: map[string]any{"text": "message number"},
		})
	}
	m.refreshViewport()
	if m.viewport.TotalLineCount() <= m.viewport.Height() {
		t.Fatalf("transcript is %d lines in a %d-line viewport: nothing to scroll",
			m.viewport.TotalLineCount(), m.viewport.Height())
	}
	return m
}

// Clicking the up arrow moves the transcript up one line, and the down
// arrow back down again.
func TestClickingTheScrollbarArrowsMovesOneLine(t *testing.T) {
	m := scrollbarConversation(t, true)
	h := m.viewport.Height()
	bottom := m.viewport.YOffset()

	m = clickAt(m, 79, 0)
	if got := m.viewport.YOffset(); got != bottom-1 {
		t.Errorf("clicking the up arrow sits at offset %d, want one line above the bottom %d", got, bottom)
	}

	m = clickAt(m, 79, h-1)
	if got := m.viewport.YOffset(); got != bottom {
		t.Errorf("clicking the down arrow sits at offset %d, want back at the bottom %d", got, bottom)
	}
}

// Clicking the track above the thumb pages up a screen, and below it
// pages back down.
func TestClickingTheTrackPagesAScreen(t *testing.T) {
	m := scrollbarConversation(t, true)
	h := m.viewport.Height()
	bottom := m.viewport.YOffset()
	thumb := scrollbarThumbRow(t, m)
	if thumb <= 1 {
		t.Fatalf("thumb is on row %d: no track above it to click", thumb)
	}

	m = clickAt(m, 79, thumb-1)
	if got := bottom - m.viewport.YOffset(); got != h {
		t.Errorf("clicking the track above the thumb moved %d lines, want one screen of %d", got, h)
	}

	// Page up until there is track below the thumb to click on, then
	// one click below it must come back down exactly one screen.
	for i := 0; i < h; i++ {
		thumb = scrollbarThumbRow(t, m)
		if thumb+1 <= h-2 {
			break
		}
		m = clickAt(m, 79, thumb-1)
	}
	thumb = scrollbarThumbRow(t, m)
	if thumb+1 > h-2 {
		t.Fatalf("thumb is on row %d: no track below it to click", thumb)
	}
	before := m.viewport.YOffset()
	m = clickAt(m, 79, thumb+1)
	if got := m.viewport.YOffset() - before; got != h {
		t.Errorf("clicking the track below the thumb moved %d lines, want one screen of %d", got, h)
	}
}

// Pressing the thumb away from its top row and moving a little keeps
// the thumb under the pointer: the content follows the pointer's
// movement, not its absolute position.
func TestDraggingTheThumbKeepsItUnderThePointer(t *testing.T) {
	m := scrollbarShortConversation(t, true, 10)
	maxOffset := m.viewport.TotalLineCount() - m.viewport.Height()
	m.viewport.SetYOffset(maxOffset / 3)

	g, ok := m.scrollbarLayout()
	if !ok {
		t.Fatal("the scrollbar is not drawn, so there is nothing to press")
	}
	track := scrollbarTrackLen(g.height)
	length, start := scrollbarThumb(g)
	if length < 4 {
		t.Fatalf("thumb is %d rows, want several rows so there is an inside to grab", length)
	}
	travel := track - length
	const grab = 2
	const delta = 2
	pressY := g.top + 1 + start + grab
	moveY := pressY + delta
	if start+delta > travel {
		t.Fatalf("thumb starts at track row %d with %d rows of travel: moving %d rows clamps", start, travel, delta)
	}

	m = clickAt(m, 79, pressY)
	if !m.scrollbarDragging {
		t.Fatal("pressing the thumb did not start a drag")
	}
	before := m.viewport.YOffset()
	updated, _ := m.Update(tea.MouseMotionMsg{Y: moveY})
	m = updated.(Model)
	want := int(math.Round(float64(start+delta) * float64(maxOffset) / float64(travel)))
	if got := m.viewport.YOffset(); got != want {
		t.Errorf("moving the pointer down %d rows moved %d lines (offset %d to %d), want %d lines to offset %d",
			delta, got-before, before, got, want-before, want)
	}

	after, ok := m.scrollbarLayout()
	if !ok {
		t.Fatal("the scrollbar is not drawn after the move, so there is no thumb to check")
	}
	afterLength, afterStart := scrollbarThumb(after)
	pointer := moveY - (after.top + 1)
	if pointer < afterStart || pointer >= afterStart+afterLength {
		t.Errorf("the thumb came out from under the pointer: pointer track row %d, thumb rows [%d, %d)",
			pointer, afterStart, afterStart+afterLength)
	}

	updated, _ = m.Update(tea.MouseReleaseMsg{})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMotionMsg{Y: 1})
	m = updated.(Model)
	if m.viewport.YOffset() != want {
		t.Errorf("moving the mouse after release sits at offset %d, want the held offset %d: the drag was still held",
			m.viewport.YOffset(), want)
	}
}

// Dragging past either end of the track parks at the transcript's end
// rather than wrapping or running off it.
func TestDraggingTheThumbClampsAtBothEnds(t *testing.T) {
	m := scrollbarConversation(t, true)
	thumb := scrollbarThumbRow(t, m)

	m = clickAt(m, 79, thumb)
	updated, _ := m.Update(tea.MouseMotionMsg{Y: -100})
	m = updated.(Model)
	if m.viewport.YOffset() != 0 {
		t.Errorf("dragging the thumb far above the track sits at offset %d, want the top 0", m.viewport.YOffset())
	}

	updated, _ = m.Update(tea.MouseMotionMsg{Y: 1000})
	m = updated.(Model)
	if !m.viewport.AtBottom() {
		t.Errorf("dragging the thumb far below the track sits at offset %d, want the bottom", m.viewport.YOffset())
	}

	updated, _ = m.Update(tea.MouseReleaseMsg{})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMotionMsg{Y: 1})
	m = updated.(Model)
	if !m.viewport.AtBottom() {
		t.Errorf("moving the mouse after release sits at offset %d: the drag was still held", m.viewport.YOffset())
	}
}

// With the switch off, none of it happens: clicks and drags leave the
// transcript where it was, the frame draws no scrollbar, and the frame
// does not ask the terminal for the mouse.
func TestTheScrollbarStaysOffUnlessAsked(t *testing.T) {
	m := scrollbarConversation(t, false)
	bottom := m.viewport.YOffset()

	m = clickAt(m, 79, 0)
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = updated.(Model)
	if m.viewport.YOffset() != bottom {
		t.Errorf("with the switch off, clicking and wheeling moved the transcript to offset %d from %d",
			m.viewport.YOffset(), bottom)
	}

	frame := m.View()
	if strings.Contains(frame.Content, "▲") || strings.Contains(frame.Content, "▼") {
		t.Error("with the switch off, the frame draws scrollbar arrows it should not draw")
	}
	if frame.MouseMode != tea.MouseModeNone {
		t.Error("with the switch off, the frame requests the mouse, taking drag-to-select away for nothing")
	}
}

// With the switch on and a transcript that fits, there is no scrollbar,
// no mouse reporting, and the transcript keeps its full width: the
// frame is byte-identical to the switch-off frame.
func TestNoScrollbarWhenTheTranscriptFits(t *testing.T) {
	on := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), true)
	updated, _ := on.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	on = updated.(Model)
	on.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "one short line"}})
	on.refreshViewport()

	off := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), false)
	updated, _ = off.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	off = updated.(Model)
	off.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "one short line"}})
	off.refreshViewport()

	if on.View().Content != off.View().Content {
		t.Error("with a transcript that fits, the switch-on frame differs from the switch-off frame: the transcript did not keep its full width")
	}
	if on.View().MouseMode != tea.MouseModeNone {
		t.Error("with a transcript that fits, the frame requests the mouse although there is nothing to scroll")
	}
	if on.viewport.Width() != 80 {
		t.Errorf("with a transcript that fits, the transcript is %d columns wide, want the full 80", on.viewport.Width())
	}
}

// While the scrollbar is drawn, the frame asks the terminal for the
// mouse; that is the request that costs drag-to-select, so it must be
// there exactly when scrolling exists and nowhere else.
func TestTheFrameAsksForTheMouseOnlyWhileScrolling(t *testing.T) {
	m := scrollbarConversation(t, true)
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Error("with an overflowing transcript and the switch on, the frame does not request the mouse, so no gesture can arrive")
	}
}

// The keyboard bindings that already scroll keep working with the
// switch on or off.
func TestScrollKeysWorkWithTheSwitchOnOrOff(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		m := scrollbarConversation(t, mouse)
		bottom := m.viewport.YOffset()

		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
		m = updated.(Model)
		if m.viewport.YOffset() >= bottom {
			t.Errorf("mouse=%v: PgUp left the offset at %d (was %d)", mouse, m.viewport.YOffset(), bottom)
		}
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = updated.(Model)
		if m.viewport.YOffset() != bottom {
			t.Errorf("mouse=%v: PgDn left the offset at %d, want back at %d", mouse, m.viewport.YOffset(), bottom)
		}

		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
		m = updated.(Model)
		if m.viewport.YOffset() != bottom-1 {
			t.Errorf("mouse=%v: shift+up sits at offset %d, want one line above %d", mouse, m.viewport.YOffset(), bottom)
		}
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
		m = updated.(Model)
		if m.viewport.YOffset() != bottom {
			t.Errorf("mouse=%v: shift+down sits at offset %d, want back at %d", mouse, m.viewport.YOffset(), bottom)
		}
	}
}

// Once reporting is on, the terminal sends wheel events whether or not
// anything wants them, so the wheel scrolls the transcript a few lines
// either way rather than doing nothing.
func TestTheWheelScrollsWhileReportingIsOn(t *testing.T) {
	m := scrollbarConversation(t, true)
	bottom := m.viewport.YOffset()

	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = updated.(Model)
	if got := bottom - m.viewport.YOffset(); got != scrollbarWheelDelta {
		t.Errorf("wheeling up moved %d lines, want %d", got, scrollbarWheelDelta)
	}
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = updated.(Model)
	if m.viewport.YOffset() != bottom {
		t.Errorf("wheeling down sits at offset %d, want back at the bottom %d", m.viewport.YOffset(), bottom)
	}
}

// A resize ends a drag in progress: every row means something else
// afterwards, so holding on would land somewhere never pointed at.
func TestAResizeEndsADragInProgress(t *testing.T) {
	m := scrollbarConversation(t, true)
	h := m.viewport.Height()
	thumb := scrollbarThumbRow(t, m)
	m = clickAt(m, 79, thumb)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(tea.MouseMotionMsg{Y: 1})
	m = updated.(Model)
	_ = h
	if m.viewport.YOffset() == 0 {
		t.Error("moving the mouse after a resize scrolled to the top: the drag survived the resize")
	}
}
