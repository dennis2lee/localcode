package tui

import (
	"strings"
	"testing"

	"localcode/internal/events"
)

// The daemon sends the debate panel as reviewers/models and keeps the
// singulars beside them. The banner must name every reviewer with its
// own model — and a log written before the plural fields existed must
// still render through the singular fallback.
func TestDebateBannerNamesEveryReviewerWithItsModel(t *testing.T) {
	cases := []struct {
		name string
		data map[string]any
		// want holds the banner line; matched exactly, so a dropped
		// reviewer or a swapped model fails rather than hiding in a
		// substring.
		want string
	}{
		{
			name: "a panel of two reviewers names both with their models",
			data: map[string]any{
				"author": "boy", "reviewer": "girl, tom",
				"reviewers": []any{"girl", "tom"},
				"model":     "review-model",
				"models":    map[string]any{"girl": "review-model", "tom": "third-model"},
				"rounds":    4, "task": "sum 1..10",
			},
			want: "[debate: boy writes, girl (review-model), tom (third-model) reviews, up to 4 rounds]",
		},
		{
			name: "a single reviewer reads as before",
			data: map[string]any{
				"author": "boy", "reviewer": "girl",
				"reviewers": []any{"girl"},
				"model":     "review-model",
				"models":    map[string]any{"girl": "review-model"},
				"rounds":    10, "task": "sum 1..10",
			},
			want: "[debate: boy writes, girl (review-model) reviews, up to 10 rounds]",
		},
		{
			// A log written before the plural fields existed carries
			// only the singulars; replay must still name them.
			name: "a log without plurals falls back to the singulars",
			data: map[string]any{
				"author": "boy", "reviewer": "girl",
				"model": "review-model", "rounds": 3, "task": "sum 1..10",
			},
			want: "[debate: boy writes, girl (review-model) reviews, up to 3 rounds]",
		},
		{
			name: "a store-shaped payload reads the same as the wire",
			data: map[string]any{
				"author": "boy", "reviewer": "girl, tom",
				"reviewers": []string{"girl", "tom"},
				"model":     "review-model",
				"models":    map[string]string{"girl": "review-model", "tom": "third-model"},
				"rounds":    4, "task": "sum 1..10",
			},
			want: "[debate: boy writes, girl (review-model), tom (third-model) reviews, up to 4 rounds]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.applyEvent(events.Event{Type: events.TypeDebateStarted, Data: tc.data})
			if len(m.transcript) != 1 {
				t.Fatalf("the banner wrote %d lines: %v", len(m.transcript), m.transcript)
			}
			if got := m.transcript[0].text; got != tc.want {
				t.Errorf("banner is %q, want %q", got, tc.want)
			}
		})
	}
}

// The plural reviewer list wins over a stale singular: the daemon keeps
// sending the joined string beside the list, and the list is the one
// that names each reviewer separately.
func TestDebateBannerPrefersTheReviewerListOverTheJoinedString(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeDebateStarted, Data: map[string]any{
		"author": "boy", "reviewer": "someone-else",
		"reviewers": []any{"girl", "tom"},
		"models":    map[string]any{"girl": "review-model", "tom": "third-model"},
		"rounds":    4, "task": "sum 1..10",
	}})
	got := m.transcript[len(m.transcript)-1].text
	if strings.Contains(got, "someone-else") {
		t.Errorf("the banner used the joined string instead of the list: %q", got)
	}
	if !strings.Contains(got, "girl (review-model)") || !strings.Contains(got, "tom (third-model)") {
		t.Errorf("the banner lost a reviewer or a model: %q", got)
	}
}
