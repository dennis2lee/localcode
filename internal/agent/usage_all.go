package agent

import (
	"fmt"
	"sort"
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
// in the per-session total.
func (l *Loop) usageAcross(w usageWindow) (totals map[string]modelTotals, sessions int, unread int) {
	totals = map[string]modelTotals{}
	if l.Store == nil {
		return totals, 0, 0
	}
	for _, sess := range l.Store.AllSessions() {
		evs, err := l.Store.Events(sess.ID, 0)
		if err != nil {
			// A log that cannot be read is counted and named rather than
			// skipped in silence: a total quietly missing a conversation
			// is the failure this whole command would be judged on.
			unread++
			continue
		}
		counted := false
		for _, ev := range evs {
			if !w.since.IsZero() && ev.Timestamp.Before(w.since) {
				continue
			}
			switch ev.Type {
			case events.TypeUsage:
				addModelTotals(totals, dataString(ev.Data, "model"),
					dataInt(ev.Data, "input_tokens"), dataInt(ev.Data, "output_tokens"))
				counted = true
			case events.TypeCompacted:
				// The compaction call is billed too, when it reported
				// usage. Cleared and rewound markers carry none.
				if model := dataString(ev.Data, "model"); model != "" {
					addModelTotals(totals, model,
						dataInt(ev.Data, "input_tokens"), dataInt(ev.Data, "output_tokens"))
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

// usageAcrossReport is what "/usage all" and its windows print.
func usageAcrossReport(w usageWindow, totals map[string]modelTotals, sessions, unread int) string {
	if len(totals) == 0 {
		return fmt.Sprintf("No usage recorded for %s.", w.name)
	}
	models := make([]string, 0, len(totals))
	for m := range totals {
		models = append(models, m)
	}
	sort.Strings(models)

	var b strings.Builder
	fmt.Fprintf(&b, "Token usage across %s (%d conversation", w.name, sessions)
	if sessions != 1 {
		b.WriteString("s")
	}
	b.WriteString("):\n")

	var grandInput, grandOutput, grandCalls int
	for _, m := range models {
		t := totals[m]
		name := m
		if name == "" {
			name = "(model not recorded)"
		}
		fmt.Fprintf(&b, "- %s: input %d · output %d · total %d (%d calls)\n",
			name, t.InputTokens, t.OutputTokens, t.InputTokens+t.OutputTokens, t.Calls)
		grandInput += t.InputTokens
		grandOutput += t.OutputTokens
		grandCalls += t.Calls
	}
	fmt.Fprintf(&b, "\nGrand total: input %d · output %d · total %d (%d calls)",
		grandInput, grandOutput, grandInput+grandOutput, grandCalls)
	if unread > 0 {
		fmt.Fprintf(&b, "\n\n%d conversation(s) could not be read and are not in this total.", unread)
	}
	b.WriteString("\n\nCounted from the conversations' own logs, archived ones included. " +
		"A turn that was later undone still cost what it cost, so it is still counted.")
	return b.String()
}
