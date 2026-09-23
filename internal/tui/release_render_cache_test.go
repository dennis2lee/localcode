package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// Release verification for the transcript render cache. Two things have
// to hold and neither implies the other: the cache must produce exactly
// what rendering everything produced, and it must actually skip the work
// it exists to skip.

// uncachedTranscript is the expression refreshViewport used before the
// cache, kept here verbatim as the thing the cache has to agree with.
func uncachedTranscript(entries []transcriptEntry, width int) string {
	return lipgloss.NewStyle().Width(width).Render(renderTranscript(entries, width))
}

// cacheTestEntries covers every kind, the shapes that render to nothing,
// and the content that makes rendering width-sensitive: wide characters,
// a fenced code block, a table, and text long enough to wrap.
func cacheTestEntries() []transcriptEntry {
	return []transcriptEntry{
		{kind: entryUser, text: "hello world"},
		{kind: entryModel, text: "a **bold** reply\n\n```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\nafter the fence"},
		{kind: entryTool, text: "[read] internal/tui/view.go"},
		{kind: entryLocal, text: "/help"},
		{kind: entrySent, text: "[sent — the turn had already started] carry on"},
		{kind: entryPending, text: "waiting on the daemon"},
		{kind: entryUser, text: strings.Repeat("넓은 한글 텍스트 and some latin ", 6)},
		{kind: entryModel, text: "- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |"},
		{kind: entryUser, text: ""},        // renders to nothing, and is skipped
		{kind: entryModel, text: "\n\n\n"}, // trims to nothing, and is skipped
		// The third shape, and the one that is not the other two: it
		// survives the newline trim, so it keeps its place between the
		// entries either side, and then renders to nothing through
		// markdown. Deciding "does this show" from the rendered text
		// rather than from the entry dropped it and lost two blank
		// lines.
		{kind: entryModel, text: "   "},
		{kind: entryTool, text: "  "},
		{kind: entryUser, text: " \t "},
		// The fourth shape, and not a whitespace one: a fence with
		// nothing in it is ordinary characters, so it keeps its place,
		// and the markdown renderer returns nothing for it.
		{kind: entryModel, text: "```\n```"},
		{kind: entryModel, text: "```go\n```"},
		// A code block is never wrapped, so this line is wider than any
		// terminal the transcript is laid out for, and the width pass
		// pads the whole transcript out to it rather than to the width.
		{kind: entryModel, text: "```go\n" + "x := []string{" + strings.Repeat(`"a", `, 40) + "}\n```"},
		{kind: entryUser, text: "last"},
	}
}

// The cache must agree with rendering everything, after every shape of
// change a session actually makes: a turn arriving, a reply streaming
// into the entry already open, a pending echo being replaced by the real
// prompt, a stopped turn rewriting the lines it abandoned, and /clear.
func TestTheRenderCacheSaysWhatRenderingItAllSays(t *testing.T) {
	// Widths the viewport can hold it at. 1 and 2 are degenerate and
	// unreachable in practice (refreshViewport floors the width), and
	// they are here because a cache that only works on ordinary numbers
	// is a cache that breaks on the first odd terminal.
	for _, width := range []int{80, 79, 40, 120, 3, 2, 1} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			full := cacheTestEntries()
			cache := &transcriptRenderCache{}
			var entries []transcriptEntry

			check := func(what string) {
				t.Helper()
				want := uncachedTranscript(entries, width)
				if got := cache.content(entries, width); got != want {
					t.Fatalf("after %s the cache and a full render disagree:\n cached %q\n  fresh %q", what, got, want)
				}
			}

			check("an empty transcript")
			for i, e := range full {
				entries = append(entries, e)
				check(fmt.Sprintf("appending entry %d", i))
			}

			// A reply streaming in: appendModelDelta grows the entry
			// already open rather than starting a new one, which is the
			// case the cache is busiest on.
			entries = append(entries, transcriptEntry{kind: entryModel, text: ""})
			for _, chunk := range []string{"Here", " is", " a **streamed**", " reply\n\n```\ncode\n```", " and the end."} {
				entries[len(entries)-1].text += chunk
				check("a streamed delta")
			}

			// resolvePendingUser drops the echo from the middle.
			for i, e := range entries {
				if e.kind == entryPending {
					entries = append(entries[:i:i], entries[i+1:]...)
					break
				}
			}
			check("dropping a pending echo from the middle")

			// abandonPendingUsers rewrites several entries in place.
			for i, e := range entries {
				if e.kind == entrySent {
					entries[i].kind = entryTool
					entries[i].text = "[not sent — the turn was stopped before the model saw this] " + e.text
				}
			}
			check("abandoning the queue")

			// An entry becoming empty, and an empty one gaining text:
			// both change which entries are shown at all.
			entries[0].text = ""
			check("the first entry losing its text")
			entries[0].text = "back again"
			check("the first entry getting text back")

			entries = entries[:3]
			check("truncating")
			entries = nil
			check("/clear")
			entries = append(entries, transcriptEntry{kind: entryUser, text: "after the clear"})
			check("the first turn after /clear")
		})
	}
}

// The saving itself. Appending N entries must render N entries, not the
// N(N+1)/2 that rendering the whole transcript every time costs — that
// square is what made the scrollbar tests take a minute each.
func TestTheRenderCacheRendersAnEntryOnce(t *testing.T) {
	const turns = 200
	cache := &transcriptRenderCache{}
	var entries []transcriptEntry
	for i := 0; i < turns; i++ {
		entries = append(entries, transcriptEntry{kind: entryUser, text: fmt.Sprintf("message number %d", i)})
		cache.content(entries, 80)
	}
	if cache.renders != turns {
		t.Errorf("%d appends rendered %d entries, want %d", turns, cache.renders, turns)
	}

	// Asking again without changing anything renders nothing at all.
	before := cache.renders
	cache.content(entries, 80)
	if cache.renders != before {
		t.Errorf("re-rendering an unchanged transcript rendered %d more entries, want 0", cache.renders-before)
	}

	// A streamed delta touches only the entry it grows.
	entries[len(entries)-1].text += " and more"
	cache.content(entries, 80)
	if got := cache.renders - before; got != 1 {
		t.Errorf("a delta on the last entry rendered %d entries, want 1", got)
	}
}

// The rule itself, not just that both paths follow it.
//
// The corpus checks the cache against the full render, and both call
// transcriptEntryShows, so they move together: changing the rule to
// TrimSpace leaves every one of those tests green while quietly taking
// away the place a whitespace-only reply holds. What the rule has to be
// is its own question and belongs in its own test.
func TestWhatCountsAsAnEntryThatShows(t *testing.T) {
	cases := []struct {
		text  string
		shows bool
		why   string
	}{
		{"", false, "nothing at all"},
		{"\n", false, "one newline"},
		{"\n\n\n", false, "only newlines, which the separator owns"},
		{"   ", true, "spaces are not newlines: the reply was there and rendered to nothing"},
		{"\t", true, "a tab likewise"},
		{"\r\n", true, "a carriage return is content to everything but the trim"},
		{"\n  \n", true, "spaces between newlines survive the trim"},
		{"```\n```", true, "an empty fence is ordinary characters"},
		{"hello", true, "a reply"},
		{"\nhello\n", true, "a reply with the newlines the separator owns"},
		{"    indented code\n", true, "leading whitespace is content, never trimmed"},
	}
	for _, c := range cases {
		for _, kind := range []entryKind{entryUser, entryModel, entryTool, entryLocal, entryPending, entrySent} {
			e := transcriptEntry{kind: kind, text: c.text}
			if got := transcriptEntryShows(e); got != c.shows {
				t.Errorf("transcriptEntryShows(%d, %q) = %v, want %v: %s", kind, c.text, got, c.shows, c.why)
			}
		}
	}
}

// What a removal costs, pinned rather than left to be discovered.
//
// The cache compares entries by position, so an entry removed from the
// middle shifts every entry after it out of line and they re-render
// although their text is identical. The cost is exactly the number of
// entries after the hole, which for the only removal this package makes
// is small: resolvePendingUser drops an echo that appendPendingUser put
// at the end. A test saying so is the difference between a known cost
// and one somebody measures again in a year.
func TestRemovingAnEntryCostsTheEntriesAfterIt(t *testing.T) {
	build := func() (*transcriptRenderCache, []transcriptEntry) {
		cache := &transcriptRenderCache{}
		var entries []transcriptEntry
		for i := 0; i < 50; i++ {
			entries = append(entries, transcriptEntry{kind: entryUser, text: fmt.Sprintf("turn %d", i)})
		}
		cache.content(entries, 80)
		return cache, entries
	}

	for _, after := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("%d entries after it", after), func(t *testing.T) {
			cache, entries := build()
			before := cache.renders
			at := len(entries) - 1 - after
			shortened := append(entries[:at:at], entries[at+1:]...)

			got := cache.content(shortened, 80)
			if want := uncachedTranscript(shortened, 80); got != want {
				t.Fatalf("a removal produced the wrong text")
			}
			if spent := cache.renders - before; spent != after {
				t.Errorf("removing an entry with %d after it rendered %d, want %d", after, spent, after)
			}
		})
	}

	// The echo a prompt actually drops is the last entry, and that is the
	// case that must cost nothing.
	cache, entries := build()
	before := cache.renders
	cache.content(entries[:len(entries)-1], 80)
	if spent := cache.renders - before; spent != 0 {
		t.Errorf("dropping the last entry rendered %d entries, want 0", spent)
	}
}

// refreshViewport asks for two widths on every event: the full one to
// decide whether the scrollbar is earned, then one column narrower to
// leave it room. One slot would let those evict each other and the cache
// would never hit, so this pins that both are kept.
func TestTheRenderCacheKeepsTheTwoWidthsAViewportAsksFor(t *testing.T) {
	cache := &transcriptRenderCache{}
	var entries []transcriptEntry
	for i := 0; i < 20; i++ {
		entries = append(entries, transcriptEntry{kind: entryUser, text: fmt.Sprintf("turn %d", i)})
		cache.content(entries, 80)
		cache.content(entries, 79)
	}
	// One render per width per new entry, and nothing else.
	if want := 2 * len(entries); cache.renders != want {
		t.Errorf("20 turns at two widths rendered %d entries, want %d", cache.renders, want)
	}

	// A third width costs a full render, and is still correct. Two is
	// what the viewport asks for; a resize is not a hot path.
	before := cache.renders
	if got, want := cache.content(entries, 100), uncachedTranscript(entries, 100); got != want {
		t.Errorf("a third width disagrees with a full render:\n cached %q\n  fresh %q", got, want)
	}
	if got := cache.renders - before; got != len(entries) {
		t.Errorf("a third width rendered %d entries, want all %d", got, len(entries))
	}
	// Whichever width it evicted, both answers are still right.
	for _, w := range []int{80, 79, 100} {
		if got, want := cache.content(entries, w), uncachedTranscript(entries, w); got != want {
			t.Errorf("width %d disagrees after eviction:\n cached %q\n  fresh %q", w, got, want)
		}
	}
}

// The same thing through the model, so the wiring is covered and not
// just the type: what the viewport is actually given after real events
// must be what rendering the transcript from scratch gives.
func TestTheViewportShowsWhatAFullRenderWouldShow(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		t.Run(fmt.Sprintf("mouse %v", mouse), func(t *testing.T) {
			m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), mouse)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m = updated.(Model)

			for i := 0; i < 40; i++ {
				m.applyEvent(events.Event{
					Type: events.TypeUserMessage,
					Data: map[string]any{"text": fmt.Sprintf("message number %d", i)},
				})
			}
			m.appendModelDelta("a reply that ")
			m.appendModelDelta("streams in **parts**")
			m.refreshViewport()

			// The width the viewport settled on is the one to compare at:
			// with the switch on and the transcript overflowing, the
			// scrollbar has taken a column.
			want := uncachedTranscript(m.transcript, m.viewport.Width())
			if got := m.renderCache.content(m.transcript, m.viewport.Width()); got != want {
				t.Fatalf("the cached transcript differs from a full render at width %d", m.viewport.Width())
			}
			if m.renderCache.renders == 0 {
				t.Error("the model rendered nothing through the cache, so refreshViewport is not using it")
			}
		})
	}
}
