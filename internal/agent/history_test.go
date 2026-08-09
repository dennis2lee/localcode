package agent

import (
	"testing"

	"localcode/internal/provider"
)

func user(texts ...string) provider.Message {
	m := provider.Message{Role: provider.RoleUser}
	for _, t := range texts {
		m.Content = append(m.Content, provider.TextBlock(t))
	}
	return m
}

func assistant(texts ...string) provider.Message {
	m := provider.Message{Role: provider.RoleAssistant}
	for _, t := range texts {
		m.Content = append(m.Content, provider.TextBlock(t))
	}
	return m
}

func texts(m provider.Message) []string {
	var out []string
	for _, b := range m.Content {
		out = append(out, b.Text)
	}
	return out
}

func TestSendableHistory(t *testing.T) {
	tests := []struct {
		name  string
		in    []provider.Message
		want  []provider.Message
		notes string
	}{{
		name: "alternating history is unchanged",
		in:   []provider.Message{user("hi"), assistant("hello"), user("more")},
		want: []provider.Message{user("hi"), assistant("hello"), user("more")},
	}, {
		// The shape a provider error leaves behind: the user message is
		// appended before the request, the request fails, and the retry
		// appends another.
		name: "two user messages merge into one",
		in:   []provider.Message{user("first try"), user("retry")},
		want: []provider.Message{user("first try", "retry")},
	}, {
		// The shape every successful compaction produced.
		name: "compaction summary merges with the prompt that follows",
		in:   []provider.Message{user("[summary]"), user("next question")},
		want: []provider.Message{user("[summary]", "next question")},
	}, {
		// The shape a cancel-before-first-token produced.
		name: "an empty assistant message is dropped",
		in:   []provider.Message{user("hi"), {Role: provider.RoleAssistant}, user("again")},
		want: []provider.Message{user("hi", "again")},
	}, {
		name: "three in a row all merge",
		in:   []provider.Message{user("a"), user("b"), user("c")},
		want: []provider.Message{user("a", "b", "c")},
	}, {
		name: "consecutive assistant messages merge too",
		in:   []provider.Message{user("q"), assistant("part one"), assistant("part two")},
		want: []provider.Message{user("q"), assistant("part one", "part two")},
	}, {
		name: "empty history stays empty",
		in:   nil,
		want: []provider.Message{},
	}, {
		name: "a history of nothing but empty messages comes back empty",
		in:   []provider.Message{{Role: provider.RoleUser}, {Role: provider.RoleAssistant}},
		want: []provider.Message{},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sendableHistory(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d messages, want %d\n got: %v\nwant: %v", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i].Role != tc.want[i].Role {
					t.Errorf("message %d role = %v, want %v", i, got[i].Role, tc.want[i].Role)
				}
				g, w := texts(got[i]), texts(tc.want[i])
				if len(g) != len(w) {
					t.Fatalf("message %d has %d blocks %q, want %d %q", i, len(g), g, len(w), w)
				}
				for j := range g {
					if g[j] != w[j] {
						t.Errorf("message %d block %d = %q, want %q", i, j, g[j], w[j])
					}
				}
			}
		})
	}
}

// The output must never alias the caller's history — merging writes a
// combined block slice, and doing that in place would corrupt the live
// in-memory history this is called on.
func TestSendableHistoryDoesNotMutateItsInput(t *testing.T) {
	in := []provider.Message{user("a"), user("b")}
	before := len(in[0].Content)

	out := sendableHistory(in)
	if len(out) != 1 || len(out[0].Content) != 2 {
		t.Fatalf("merge did not happen: %v", out)
	}
	if len(in[0].Content) != before {
		t.Errorf("input message grew from %d to %d blocks", before, len(in[0].Content))
	}
	if len(in) != 2 {
		t.Errorf("input slice length changed to %d", len(in))
	}

	// Mutating the result must not reach back into the input.
	out[0].Content[0] = provider.TextBlock("clobbered")
	if in[0].Content[0].Text != "a" {
		t.Errorf("input block was aliased and got clobbered: %q", in[0].Content[0].Text)
	}
}

// Whatever comes out is a shape a strict provider accepts: that is the
// whole contract, so assert it directly rather than only per-case.
func TestSendableHistoryAlwaysAlternatesAndIsNonEmpty(t *testing.T) {
	messy := []provider.Message{
		user("a"), user("b"), {Role: provider.RoleAssistant}, assistant("c"),
		assistant("d"), user("e"), {Role: provider.RoleUser}, user("f"),
	}
	out := sendableHistory(messy)
	for i, m := range out {
		if len(m.Content) == 0 {
			t.Errorf("message %d is empty", i)
		}
		if i > 0 && out[i-1].Role == m.Role {
			t.Errorf("messages %d and %d are both %v", i-1, i, m.Role)
		}
	}
}
