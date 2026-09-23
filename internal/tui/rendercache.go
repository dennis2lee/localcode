package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// transcriptRenderCache remembers what each transcript entry rendered to,
// so an arriving event costs one entry's rendering instead of the whole
// conversation's.
//
// Rendering a transcript is not cheap. Every entry goes through lipgloss,
// which walks its text by grapheme cluster, and the width pass over the
// joined result walks all of it again — measured on a 200-turn transcript
// at width 80, 2.6ms for the entries and 6.0ms for the width pass. That
// happened inside refreshViewport, which runs on every event, including
// every streaming delta of every reply, so the cost of a session grew
// with the square of its length. It is also the whole of why twelve
// scrollbar tests took 8 to 50 seconds each under the race detector:
// their helper posts 200 events one at a time.
//
// What makes an entry cacheable is that its rendered text depends on
// nothing but the entry and the width. renderTranscript builds one part
// per entry with no look-back and no position, and the styles it uses are
// package-level values fixed at init.
//
// The width pass is the subtle half, and caching it per entry is wrong in
// the obvious form. It does two things at once: it wraps each line down
// to the width, and it pads every line out to the widest line in what it
// was handed. Wrapping is per line, so it splits across entries without
// trouble. Padding does not: a line with nowhere to break — a long path,
// a hash, a run of minified code — stays wider than the width, and the
// whole transcript is then padded out to it while the same entry,
// rendered alone, is padded out only to itself.
//
// So an entry is wrapped at the width, the widest line it ends up with is
// measured, and every entry is padded to the widest of those. That is the
// column the whole render would have used. A new widest line repads
// everything, which is the cost this type exists to avoid, but it happens
// when a line that will not fit arrives rather than on every event.
//
// release_render_cache_test.go checks all of it against the uncached
// expression rather than trusting any of it.
//
// Two widths are kept because refreshViewport asks for two: the full
// width, to decide whether the transcript overflows and has earned a
// scrollbar, and then one column narrower to leave room for it. With a
// single slot those two would evict each other on every event and the
// cache would never hit.
//
// Entries are compared by position, so removing one from the middle
// re-renders everything after it: each entry has shifted a place and
// mismatches the one it is now compared against, although none of its
// text changed. Deliberately not detected. The only removal in the
// package is resolvePendingUser dropping the echo of a prompt, and
// appendPendingUser put that echo at the end, so what shifts is the
// handful of entries that arrived while the daemon was answering. At
// about 50us an entry that is microseconds, once per prompt, against
// shift detection that would run on every event to find it. The output
// is right either way; this is only about work.
type transcriptRenderCache struct {
	slots [2]transcriptWidthSlot
	next  int
	// renders counts entries rendered, which is the whole point of this
	// type and the only thing about it that is not observable from its
	// output. release_render_cache_test.go asserts on it, because a
	// cache that quietly stopped caching would otherwise pass every
	// test it has and cost exactly what it was written to save.
	renders int
}

// transcriptWidthSlot is what one width has rendered to. entries,
// wrapped, owns and padded are parallel, one element per transcript
// entry.
type transcriptWidthSlot struct {
	live    bool
	width   int
	entries []transcriptEntry
	// wrapped[i] is entries[i] styled and wrapped to width, and "" for an
	// entry that shows nothing.
	wrapped []string
	// shows[i] is whether entries[i] takes a place in the transcript at
	// all, which is not the same as rendering to something: see
	// transcriptEntryShows.
	shows []bool
	// owns[i] is the widest line in wrapped[i]. It is at least width,
	// and more when the entry holds a run of characters with nowhere to
	// break: a long path, a hash, a line of minified code.
	owns []int
	// padded[i] is wrapped[i] with every line padded out to target.
	padded []string
	// target is the column every line is padded to: the width, or the
	// widest line in the transcript when some line could not be wrapped
	// down to the width.
	target int
	// blank is what this transcript renders to when nothing in it shows,
	// and is also the empty line standing between two entries.
	blank string
	sep   string
}

// content is the text the viewport is given for this transcript at this
// width, byte for byte what the uncached path produced.
func (c *transcriptRenderCache) content(entries []transcriptEntry, width int) string {
	slot := c.slotFor(width)

	// A transcript can lose entries as well as gain them: a pending
	// prompt is replaced by the real one, and /clear empties it.
	if len(slot.entries) > len(entries) {
		slot.entries = slot.entries[:len(entries)]
		slot.shows = slot.shows[:len(entries)]
		slot.wrapped = slot.wrapped[:len(entries)]
		slot.owns = slot.owns[:len(entries)]
		slot.padded = slot.padded[:len(entries)]
	}
	fresh := make([]int, 0, 8)
	for i, e := range entries {
		switch {
		case i >= len(slot.entries):
			slot.entries = append(slot.entries, e)
			slot.shows = append(slot.shows, false)
			slot.wrapped = append(slot.wrapped, "")
			slot.owns = append(slot.owns, 0)
			slot.padded = append(slot.padded, "")
		case slot.entries[i] == e:
			// Struct equality over every field: the kind, the text,
			// and a reasoning block's header and its live and open
			// flags. Those are the only things that decide what an
			// entry renders to, so a hit cannot be stale.
			continue
		}
		c.renders++
		slot.entries[i] = e
		slot.shows[i] = transcriptEntryShows(e)
		if !slot.shows[i] {
			slot.wrapped[i], slot.owns[i], slot.padded[i] = "", 0, ""
		} else {
			slot.wrapped[i] = lipgloss.NewStyle().Width(width).Render(renderTranscript([]transcriptEntry{e}, width))
			slot.owns[i] = lipgloss.Width(slot.wrapped[i])
		}
		fresh = append(fresh, i)
	}

	target := width
	for _, own := range slot.owns {
		if own > target {
			target = own
		}
	}
	if target != slot.target {
		// A line that would not wrap down to the width, wider than any
		// before it, or the widest one going away. One column holds for
		// the whole transcript, so all of it moves.
		slot.target = target
		slot.blank = lipgloss.NewStyle().Width(target).Render("")
		slot.sep = "\n" + slot.blank + "\n"
		for i := range slot.wrapped {
			slot.padded[i] = padTranscriptEntry(slot.wrapped[i], slot.owns[i], target)
		}
	} else {
		for _, i := range fresh {
			slot.padded[i] = padTranscriptEntry(slot.wrapped[i], slot.owns[i], target)
		}
	}

	shown := make([]string, 0, len(slot.padded))
	for i, p := range slot.padded {
		if !slot.shows[i] {
			continue
		}
		shown = append(shown, p)
	}
	if len(shown) == 0 {
		return slot.blank
	}
	return strings.Join(shown, slot.sep)
}

// slotFor returns the slot holding this width, emptying the oldest one
// when a third width turns up. A miss costs one full render, which is
// what every render cost before this existed.
func (c *transcriptRenderCache) slotFor(width int) *transcriptWidthSlot {
	for i := range c.slots {
		if c.slots[i].live && c.slots[i].width == width {
			return &c.slots[i]
		}
	}
	take := -1
	for i := range c.slots {
		if !c.slots[i].live {
			take = i
			break
		}
	}
	if take < 0 {
		take = c.next
		c.next = (c.next + 1) % len(c.slots)
	}
	// target starts at -1 rather than 0 so the first content() call
	// always takes the repad branch and fills blank and sep, including
	// for a width of 0.
	c.slots[take] = transcriptWidthSlot{live: true, width: width, target: -1}
	return &c.slots[take]
}

// padTranscriptEntry pads every line of one wrapped entry out to the
// column the transcript as a whole is padded to.
//
// An entry already that wide is returned untouched, which is every entry
// in the ordinary case where nothing outran the width: the padding a
// second pass would add is the padding the first one already added.
func padTranscriptEntry(wrapped string, own, target int) string {
	if wrapped == "" || own == target {
		return wrapped
	}
	return lipgloss.NewStyle().Width(target).Render(wrapped)
}
