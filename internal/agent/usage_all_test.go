package agent

import (
	"strings"
	"testing"
	"time"
)

// What every conversation has cost, rather than only the one you are in.
//
// "/usage" answered for its own session, which is the right default and
// was the only answer: a daily figure meant adding up by hand across
// however many conversations a day had.

func TestTheUsageWindowsAreReadFromTheWord(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		arg   string
		since time.Time
		name  string
	}{
		{"all", time.Time{}, "every conversation"},
		{"today", time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), "today"},
		{"week", now.AddDate(0, 0, -7), "the last 7 days"},
		{"month", now.AddDate(0, 0, -30), "the last 30 days"},
		// Case is not the question being asked.
		{"ALL", time.Time{}, "every conversation"},
	} {
		w, ok := parseUsageWindow(c.arg, now)
		if !ok {
			t.Errorf("%q was not recognised", c.arg)
			continue
		}
		if !w.since.Equal(c.since) {
			t.Errorf("%q reaches back to %v, want %v", c.arg, w.since, c.since)
		}
		if w.name != c.name {
			t.Errorf("%q is called %q, want %q", c.arg, w.name, c.name)
		}
	}
	// A word this does not know is not guessed at: answering a question
	// about a different period than the one asked is worse than saying
	// which words there are.
	if _, ok := parseUsageWindow("fortnight", now); ok {
		t.Error("an unknown window was accepted")
	}
}

// Every conversation, archived ones included, summed from the logs.
func TestUsageAcrossCountsEveryConversation(t *testing.T) {
	loop, first, _ := effortLoop(t, "")
	if _, err := loop.Store.CreateSession("second", "", "boy", true); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Store.CreateSession("put-away", "", "boy", true); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		session string
		model   string
		in, out int
	}{
		{first, "muse", 100, 10},
		{"second", "muse", 200, 20},
		{"second", "opus", 5, 1},
		{"put-away", "muse", 7, 3},
	} {
		if _, err := loop.Store.Append(c.session, "usage", map[string]any{
			"model": c.model, "input_tokens": c.in, "output_tokens": c.out,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Archiving is not deleting, and a total that quietly left an
	// archived conversation out would be wrong in the direction nobody
	// checks.
	if _, err := loop.Store.Archive("put-away"); err != nil {
		t.Fatal(err)
	}

	totals, sessions, unread := loop.usageAcross(usageWindow{name: "every conversation"})
	if unread != 0 {
		t.Errorf("%d conversation(s) could not be read", unread)
	}
	if sessions != 3 {
		t.Errorf("counted %d conversations, want all three", sessions)
	}
	if got := totals["muse"]; got.InputTokens != 307 || got.OutputTokens != 33 || got.Calls != 3 {
		t.Errorf("muse totals = %+v, want 307 in, 33 out, 3 calls", got)
	}
	if got := totals["opus"]; got.InputTokens != 5 || got.Calls != 1 {
		t.Errorf("opus totals = %+v, want 5 in over 1 call", got)
	}

	report := usageAcrossReport(usageWindow{name: "every conversation"}, totals, sessions, unread)
	for _, want := range []string{"muse", "opus", "Grand total", "3 conversations"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q: %s", want, report)
		}
	}
}

// A window leaves out what happened before it.
func TestAWindowLeavesOutWhatCameBefore(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	if _, err := loop.Store.Append(sid, "usage", map[string]any{
		"model": "muse", "input_tokens": 100, "output_tokens": 10,
	}); err != nil {
		t.Fatal(err)
	}

	// A window that starts after everything in the log.
	ahead := usageWindow{name: "today", since: time.Now().Add(time.Hour)}
	totals, sessions, _ := loop.usageAcross(ahead)
	if len(totals) != 0 || sessions != 0 {
		t.Errorf("totals = %+v over %d conversations, want nothing from before the window", totals, sessions)
	}
	if report := usageAcrossReport(ahead, totals, sessions, 0); !strings.Contains(report, "No usage recorded for today") {
		t.Errorf("an empty window does not say so: %s", report)
	}

	// And one that starts before it.
	behind := usageWindow{name: "the last 7 days", since: time.Now().Add(-time.Hour)}
	if totals, _, _ := loop.usageAcross(behind); totals["muse"].InputTokens != 100 {
		t.Errorf("totals = %+v, want the call inside the window", totals)
	}
}
