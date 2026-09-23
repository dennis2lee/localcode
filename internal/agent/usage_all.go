package agent

import (
	"fmt"
	"strings"
	"time"

	"localcode/internal/events"
)

// What every conversation has cost, not just this one.
//
// "/usage" answers for the session it is typed in, which is the right
// default and was the only answer available: a daily or weekly figure
// meant adding up by hand across however many conversations a day had.
// The totals are already in each session's log — rehydrateUsage is the
// function that reads one — so the work here is walking the logs and
// bucketing by day rather than counting anything new.
//
// Deliberately read on demand rather than kept. A running cross-session
// total would be a second place the truth lives, and the log is the
// place it already lives; the cost is one pass over the logs when
// somebody asks, which is a thing they do occasionally and never in the
// middle of a turn.

// usageWindow is how far back a total reaches.
type usageWindow struct {
	name  string
	since time.Time
}

// parseUsageWindow reads the argument to "/usage".
//
// now is passed rather than read, so "today" means the same thing to the
// test as it does to the caller.
func parseUsageWindow(arg string, now time.Time) (usageWindow, bool) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "all":
		return usageWindow{name: "every conversation"}, true
	case "today":
		y, m, d := now.Date()
		return usageWindow{name: "today", since: time.Date(y, m, d, 0, 0, 0, 0, now.Location())}, true
	case "week":
		return usageWindow{name: "the last 7 days", since: now.AddDate(0, 0, -7)}, true
	case "month":
		return usageWindow{name: "the last 30 days", since: now.AddDate(0, 0, -30)}, true
	}
	return usageWindow{}, false
}

// usageAcross sums every conversation this daemon holds, within a window.
//
// Archived conversations count. They are conversations that happened, and
// a total that quietly left them out would be wrong in the direction
// nobody checks — the same reasoning that keeps a rewound turn's tokens
// in the per-session total. So do the sessions no list shows: a
// sub-agent's, a scheduled run's, a debate reviewer's. Each made its own
// model calls.
func (l *Loop) usageAcross(w usageWindow) (totals map[string]modelTotals, sessions int, unread int) {
	if l.Store == nil {
		return map[string]modelTotals{}, 0, 0
	}
	var ids []string
	for _, sess := range l.Store.AllSessions() {
		ids = append(ids, sess.ID)
	}
	return l.usageOfSessions(ids, w.since)
}

// usageOfSessions sums the model calls the given sessions' logs record,
// from since on (all of them when since is zero).
//
// A fork's log opens with a copy of the log it was forked from, and those
// calls were made once, by the original: counted in both, every fork
// doubled what its original had spent, and the copy is stamped with the
// moment of the fork, so "/usage today" counted an old conversation's
// spend the day it was forked. The session.forked event that opens a
// fork says how many events follow it as the copy, and those are skipped.
// A fork of a fork copies its own opening with the rest, inside the
// count, so the skip covers it.
func (l *Loop) usageOfSessions(ids []string, since time.Time) (totals map[string]modelTotals, sessions int, unread int) {
	totals = map[string]modelTotals{}
	for _, id := range ids {
		evs, err := l.Store.Events(id, 0)
		if err != nil {
			// A log that cannot be read is counted and named rather than
			// skipped in silence: a total quietly missing a conversation
			// is the failure this whole command would be judged on.
			unread++
			continue
		}
		counted := false
		copied := 0
		for _, ev := range evs {
			if copied > 0 {
				copied--
				continue
			}
			if ev.Type == events.TypeSessionForked {
				copied = dataInt(ev.Data, "copied")
				continue
			}
			if !since.IsZero() && ev.Timestamp.Before(since) {
				continue
			}
			switch ev.Type {
			case events.TypeUsage:
				addModelTotals(totals, dataString(ev.Data, "model"), callTokensOf(ev.Data))
				counted = true
			case events.TypeCompacted:
				// The compaction call is billed too, when it reported
				// usage. Cleared and rewound markers carry none.
				if model := dataString(ev.Data, "model"); model != "" {
					addModelTotals(totals, model, callTokensOf(ev.Data))
					counted = true
				}
			}
		}
		if counted {
			sessions++
		}
	}
	return totals, sessions, unread
}

// UsageFigures is one model's spend, or a whole summary's, as the API and
// "localcode run --format json" report it.
type UsageFigures struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	// Cached prompt a log recorded without the read and the write apart.
	CacheReadOrWriteTokens int `json:"cache_read_or_write_tokens"`
	Calls                  int `json:"calls"`
}

// UsageOfEvent is what one logged model call reported, read the way
// every total here reads it; the zero value for an event that is not a
// usage or a model-carrying compacted event.
func UsageOfEvent(ev events.Event) UsageFigures {
	switch ev.Type {
	case events.TypeUsage:
	case events.TypeCompacted:
		if dataString(ev.Data, "model") == "" {
			return UsageFigures{}
		}
	default:
		return UsageFigures{}
	}
	return figuresOf(modelTotals{}.add(callTokensOf(ev.Data)))
}

// Add folds g into f.
func (f *UsageFigures) Add(g UsageFigures) {
	f.InputTokens += g.InputTokens
	f.OutputTokens += g.OutputTokens
	f.CacheReadTokens += g.CacheReadTokens
	f.CacheWriteTokens += g.CacheWriteTokens
	f.CacheReadOrWriteTokens += g.CacheReadOrWriteTokens
	f.Calls += g.Calls
}

func figuresOf(t modelTotals) UsageFigures {
	return UsageFigures{
		InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
		CacheReadTokens: t.CacheReadTokens, CacheWriteTokens: t.CacheWriteTokens,
		CacheReadOrWriteTokens: t.CacheUnsplitTokens, Calls: t.Calls,
	}
}

// UsageSummary is what a set of conversations spent, per model: the
// figures "/usage all" prints, for a client to draw.
type UsageSummary struct {
	Scope    string                  `json:"scope"`
	Models   map[string]UsageFigures `json:"models"`
	Sessions int                     `json:"sessions"`
	Unread   int                     `json:"unread"`
	// Note says what the cache figures are, in the words /usage prints
	// under its report, when there are any to explain: one text, so a
	// client drawing the figures cannot explain them differently.
	Note string `json:"note,omitempty"`
}

// Total is the summary's figures summed over its models.
func (s UsageSummary) Total() UsageFigures {
	var t UsageFigures
	for _, m := range s.Models {
		t.Add(m)
	}
	return t
}

func usageSummaryOf(scope string, totals map[string]modelTotals, sessions, unread int) UsageSummary {
	out := UsageSummary{Scope: scope, Models: map[string]UsageFigures{}, Sessions: sessions, Unread: unread}
	var grand modelTotals
	for m, t := range totals {
		out.Models[m] = figuresOf(t)
		grand.CacheReadTokens += t.CacheReadTokens
		grand.CacheWriteTokens += t.CacheWriteTokens
		grand.CacheUnsplitTokens += t.CacheUnsplitTokens
	}
	if grand.cached() {
		out.Note = cacheNote
		if grand.CacheUnsplitTokens > 0 {
			out.Note += " " + cacheUnsplitNote
		}
	}
	return out
}

// UsageAcross is "/usage all|today|week|month" as data: the same
// conversations, read from the same logs, so the usage window and the
// command cannot disagree. ok is false for a word the command does not
// know.
func (l *Loop) UsageAcross(word string, now time.Time) (UsageSummary, bool) {
	w, ok := parseUsageWindow(word, now)
	if !ok {
		return UsageSummary{}, false
	}
	totals, sessions, unread := l.usageAcross(w)
	return usageSummaryOf(w.name, totals, sessions, unread), true
}

// UsageOfTree is what one conversation and every session below it spent:
// the sub-agents it delegated to, the tasks it spawned and their own. It
// is what a run cost, since a run's sub-agents make their calls in
// sessions of their own.
func (l *Loop) UsageOfTree(sessionID string) UsageSummary {
	ids := append([]string{sessionID}, l.Store.Descendants(sessionID)...)
	totals, sessions, unread := l.usageOfSessions(ids, time.Time{})
	return usageSummaryOf("this conversation and the sessions under it", totals, sessions, unread)
}

// usageAcrossReport is what "/usage all" and its windows print.
func usageAcrossReport(w usageWindow, totals map[string]modelTotals, sessions, unread int) string {
	if len(totals) == 0 {
		return fmt.Sprintf("No usage recorded for %s.", w.name)
	}
	// Sessions, not conversations: a sub-agent's, a scheduled run's and a
	// debate reviewer's are counted, and none of them is a conversation in
	// the list, so "4 conversations" for one listed conversation that
	// delegated three times named things nobody could find.
	heading := fmt.Sprintf("Token usage across %s (%d session", w.name, sessions)
	if sessions != 1 {
		heading += "s"
	}
	var b strings.Builder
	b.WriteString(usageReport(heading+"):\n", totals))
	if unread > 0 {
		fmt.Fprintf(&b, "\n\n%d session log(s) could not be read and are not in this total.", unread)
	}
	b.WriteString("\n\nCounted from the sessions' own logs, archived conversations and sub-agents included. " +
		"A turn that was later undone still cost what it cost, so it is still counted.")
	return b.String()
}
