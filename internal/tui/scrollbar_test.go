package tui

import (
	"math"
	"testing"
)

// The click geometry is a pure function of the layout: every region of
// the scrollbar, the edges around it, and the two degenerate transcript
// sizes, all without a terminal in the room.
func TestEveryScrollbarRegionResolvesToItsAction(t *testing.T) {
	// A transcript far longer than the screen: height 20, 200 lines.
	// Thumb: round(20*20/200) = 2 rows, parked at the top while the
	// offset is 0, so rows 1-2 of the viewport are the thumb.
	far := scrollbarGeometry{width: 80, top: 0, height: 20, total: 200, offset: 0}
	// Barely longer: height 20, 21 lines. Thumb: round(400/21) = 19,
	// clamped to a track of 18 minus one row to drag in: 17.
	barely := scrollbarGeometry{width: 80, top: 0, height: 20, total: 21, offset: 0}

	cases := []struct {
		name string
		geom scrollbarGeometry
		x, y int
		want scrollbarHitKind
	}{
		{"far: up arrow scrolls up one line", far, 79, 0, scrollbarHitLineUp},
		{"far: down arrow scrolls down one line", far, 79, 19, scrollbarHitLineDown},
		{"far: thumb grabs instead of paging", far, 79, 1, scrollbarHitThumb},
		{"far: thumb's second row still grabs", far, 79, 2, scrollbarHitThumb},
		{"far: track below the thumb pages down", far, 79, 3, scrollbarHitPageDown},
		{"far: bottom of the track pages down", far, 79, 18, scrollbarHitPageDown},
		{"barely: up arrow scrolls up one line", barely, 79, 0, scrollbarHitLineUp},
		{"barely: down arrow scrolls down one line", barely, 79, 19, scrollbarHitLineDown},
		{"barely: thumb fills all but one track row", barely, 79, 17, scrollbarHitThumb},
		{"barely: the one free row pages down", barely, 79, 18, scrollbarHitPageDown},
		// The frame beside the scrollbar is not the scrollbar.
		{"far: one column left of the bar does nothing", far, 78, 5, scrollbarHitNone},
		{"far: far from the bar does nothing", far, 10, 5, scrollbarHitNone},
		{"far: above the viewport does nothing", far, 79, -1, scrollbarHitNone},
		{"far: below the viewport does nothing", far, 79, 20, scrollbarHitNone},
		// A transcript that fits has no scrollbar anywhere, arrows included.
		{"fits: top row does nothing", scrollbarGeometry{width: 80, top: 0, height: 20, total: 20, offset: 0}, 79, 0, scrollbarHitNone},
		{"fits: middle does nothing", scrollbarGeometry{width: 80, top: 0, height: 20, total: 10, offset: 0}, 79, 10, scrollbarHitNone},
		{"fits: bottom row does nothing", scrollbarGeometry{width: 80, top: 0, height: 20, total: 20, offset: 0}, 79, 19, scrollbarHitNone},
		// A viewport offset in screen rows moves every region with it.
		{"far with a screen offset: arrow follows", scrollbarGeometry{width: 80, top: 5, height: 20, total: 200, offset: 0}, 79, 5, scrollbarHitLineUp},
		{"far with a screen offset: row above follows", scrollbarGeometry{width: 80, top: 5, height: 20, total: 200, offset: 0}, 79, 4, scrollbarHitNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveScrollbarClick(tc.x, tc.y, tc.geom)
			if got.kind != tc.want {
				t.Errorf("click at (%d, %d) resolves to %v, want %v", tc.x, tc.y, got.kind, tc.want)
			}
		})
	}
}

// The thumb tracks the offset: full travel at the bottom, none at the
// top, and proportionally between.
func TestTheThumbSitsWhereTheOffsetSays(t *testing.T) {
	g := scrollbarGeometry{width: 80, top: 0, height: 20, total: 200}
	maxOffset := g.total - g.height

	length, start := scrollbarThumb(scrollbarGeometry{width: 80, top: 0, height: 20, total: 200, offset: 0})
	if start != 0 {
		t.Errorf("thumb starts at track row %d with offset 0, want the very top", start)
	}
	if length != 2 {
		t.Errorf("thumb is %d rows in a 200-line transcript on 20 rows, want 2", length)
	}

	length, start = scrollbarThumb(scrollbarGeometry{width: 80, top: 0, height: 20, total: 200, offset: maxOffset})
	if length != 2 {
		t.Errorf("thumb is %d rows at the bottom, want the same 2 as at the top", length)
	}
	if start != 18-2 {
		t.Errorf("thumb starts at track row %d at the bottom, want the last travel row %d", start, 18-2)
	}

	mid := maxOffset / 2
	_, start = scrollbarThumb(scrollbarGeometry{width: 80, top: 0, height: 20, total: 200, offset: mid})
	want := int(math.Round(float64(mid) * float64(18-2) / float64(maxOffset)))
	if start != want {
		t.Errorf("thumb starts at track row %d halfway down, want %d", start, want)
	}
}

// A thumb that would round to zero rows stays visible and grabbable:
// one row is the floor, not nothing.
func TestATinyThumbStillShowsOneRow(t *testing.T) {
	length, _ := scrollbarThumb(scrollbarGeometry{width: 80, top: 0, height: 10, total: 1000, offset: 0})
	if length != scrollbarMinThumb {
		t.Errorf("thumb is %d rows in a 1000-line transcript on 10 rows, want the %d-row minimum", length, scrollbarMinThumb)
	}
}

// A transcript exactly one line taller than the viewport: the thumb must
// not fill the whole track, or there is nowhere to drag it to.
func TestOneExtraLineLeavesRoomToDrag(t *testing.T) {
	for _, height := range []int{5, 10, 20} {
		g := scrollbarGeometry{width: 80, top: 0, height: height, total: height + 1, offset: 0}
		length, _ := scrollbarThumb(g)
		if length >= scrollbarTrackLen(height) {
			t.Errorf("height %d: thumb is %d rows of a %d-row track, leaving nowhere to drag",
				height, length, scrollbarTrackLen(height))
		}
	}
}

// Dragging past either end of the track parks at the transcript's end
// rather than wrapping or running off it.
func TestDraggingPastTheEndsClamps(t *testing.T) {
	const total, height = 200, 20
	maxOffset := total - height
	track := scrollbarTrackLen(height)

	if got := scrollbarOffsetForFraction(scrollbarFractionForRow(-100, 1, track), total, height); got != 0 {
		t.Errorf("dragging far above the track lands at offset %d, want the top 0", got)
	}
	if got := scrollbarOffsetForFraction(scrollbarFractionForRow(1000, 1, track), total, height); got != maxOffset {
		t.Errorf("dragging far below the track lands at offset %d, want the bottom %d", got, maxOffset)
	}
	if got := scrollbarOffsetForFraction(scrollbarFractionForRow(1, 1, track), total, height); got != 0 {
		t.Errorf("dragging to the first track row lands at offset %d, want the top 0", got)
	}
	if got := scrollbarOffsetForFraction(scrollbarFractionForRow(1+track-1, 1, track), total, height); got != maxOffset {
		t.Errorf("dragging to the last track row lands at offset %d, want the bottom %d", got, maxOffset)
	}
}

// A press inside the thumb resolves to a drag wherever in the thumb it
// lands, not only on its first row: there is an inside to grab.
func TestAPressInsideTheThumbStillGrabs(t *testing.T) {
	g := scrollbarGeometry{width: 80, top: 0, height: 20, total: 21, offset: 0}
	length, start := scrollbarThumb(g)
	if length < 3 {
		t.Fatalf("thumb is %d rows, want several rows so there is an inside to grab", length)
	}
	middle := 1 + start + 1
	if got := resolveScrollbarClick(79, middle, g); got.kind != scrollbarHitThumb {
		t.Errorf("press inside the thumb resolves to %v, want a drag", got.kind)
	}
}
