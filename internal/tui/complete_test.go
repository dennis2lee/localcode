package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
)

func withSkills(m Model, names ...string) Model {
	for _, n := range names {
		m.skillsList = append(m.skillsList, client.SkillInfo{Name: n})
	}
	return m
}

// pressRight is the completion key.
func pressRight(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	return updated.(Model)
}

// typing part of a skill's name and pressing Right finishes it.
func TestRightArrowCompletesASkillName(t *testing.T) {
	m := withSkills(newTestModel(), "pdf-tools")
	m.setInputTo("/pdf")

	m = pressRight(t, m)
	if m.input.Value() != "/pdf-tools" {
		t.Errorf("input = %q, want the completed skill name", m.input.Value())
	}
}

// Several candidates for one prefix, so the same key walks them. The
// common-prefix answer a shell gives is no answer here: skills are named
// for what they do, so the shared prefix of "plan" and "pdf-tools" is
// one letter.
func TestRightArrowCyclesThroughEveryCandidate(t *testing.T) {
	m := withSkills(newTestModel(), "pdf-tools", "plan-review", "pptx")
	m.setInputTo("/p")

	seen := []string{}
	for i := 0; i < 4; i++ {
		m = pressRight(t, m)
		seen = append(seen, m.input.Value())
	}
	want := []string{"/pdf-tools", "/plan-review", "/pptx", "/p"}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("press %d gave %q, want %q (full walk: %v)", i+1, seen[i], want[i], seen)
		}
	}
	// And round again, so the walk is a ring rather than a dead end.
	m = pressRight(t, m)
	if m.input.Value() != "/pdf-tools" {
		t.Errorf("the walk did not come round: %q", m.input.Value())
	}
}

// Editing ends the walk: what is in the box is a new prompt, and the
// next press should start over from it rather than carry on from where
// the last walk had got to.
func TestEditingRestartsTheWalk(t *testing.T) {
	m := withSkills(newTestModel(), "pdf-tools", "plan-review")
	m.setInputTo("/p")
	m = pressRight(t, m)
	if m.input.Value() != "/pdf-tools" {
		t.Fatalf("first completion = %q", m.input.Value())
	}

	m.setInputTo("/pl")
	m = pressRight(t, m)
	if m.input.Value() != "/plan-review" {
		t.Errorf("after editing, completion = %q, want the match for the new prefix", m.input.Value())
	}
}

// Custom commands complete too: they are invoked the same way, and
// somebody typing "/re" is not thinking about which list it is in.
func TestCommandsCompleteAlongsideSkills(t *testing.T) {
	m := withSkills(newTestModel(), "review-diff")
	m.commandsList = []client.CommandInfo{{Name: "release"}}
	m.setInputTo("/re")

	m = pressRight(t, m)
	first := m.input.Value()
	m = pressRight(t, m)
	second := m.input.Value()
	if first != "/review-diff" || second != "/release" {
		t.Errorf("walk gave %q then %q, want both the skill and the command", first, second)
	}
}

// Right still moves the cursor when it has something to move over. Only
// at the end of a one-word "/name" does it mean completion.
func TestRightArrowStillMovesTheCursor(t *testing.T) {
	m := withSkills(newTestModel(), "pdf-tools")
	m.setInputTo("/pdf")
	m.input.CursorStart()

	m = pressRight(t, m)
	if m.input.Value() != "/pdf" {
		t.Errorf("Right completed from the middle of the prompt: %q", m.input.Value())
	}

	// And a prompt that is no longer one word is no longer completable:
	// the skill has been chosen and what follows is the request.
	m.setInputTo("/pdf split this")
	m = pressRight(t, m)
	if m.input.Value() != "/pdf split this" {
		t.Errorf("Right completed a prompt with arguments: %q", m.input.Value())
	}
}

// Nothing to complete leaves the key alone rather than swallowing it.
func TestNoCandidatesLeavesTheKeyAlone(t *testing.T) {
	m := withSkills(newTestModel(), "pdf-tools")
	m.setInputTo("/zz")
	m = pressRight(t, m)
	if m.input.Value() != "/zz" {
		t.Errorf("input = %q, want it untouched", m.input.Value())
	}
}

// The footer says how many candidates there are, so an ambiguous prefix
// looks ambiguous before the key is pressed.
func TestTheFooterCountsTheCandidates(t *testing.T) {
	m := withSkills(newTestModel(), "pdf-tools", "plan-review")
	m.setInputTo("/p")
	if hint := m.completionHint(); hint == "" {
		t.Fatal("no completion hint for an ambiguous prefix")
	} else if want := "2 matches"; !contains(hint, want) {
		t.Errorf("hint = %q, want it to mention %q", hint, want)
	}

	m.setInputTo("/pd")
	if hint := m.completionHint(); hint != "→ /pdf-tools" {
		t.Errorf("hint for a single match = %q", hint)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// The three switches got a command each so they could be reached from a
// prompt, and being completable is half of what that is for:
// "/permission-skip-all" is not a name anybody types twice from memory.
func TestTheDaemonsOwnCommandsComplete(t *testing.T) {
	m := newTestModel()
	m.slashList = []client.SlashCommandInfo{
		{Name: "smart-agent"},
		{Name: "skill"},
	}
	m.setInputTo("/sm")

	m = pressRight(t, m)
	if m.input.Value() != "/smart-agent" {
		t.Errorf("input = %q, want the daemon's own command completed", m.input.Value())
	}
}

// This client's own commands come from the table that dispatches them,
// so one added there is completable without a second list to remember.
func TestThisClientsOwnCommandsComplete(t *testing.T) {
	m := newTestModel()
	m.setInputTo("/vers")

	m = pressRight(t, m)
	if m.input.Value() != "/version" {
		t.Errorf("input = %q, want a local command completed", m.input.Value())
	}
}

// A skill and a custom command can share a name. Offering it twice in a
// walk looks like the key stopped working.
func TestASharedNameIsOfferedOnce(t *testing.T) {
	m := withSkills(newTestModel(), "review")
	m.commandsList = []client.CommandInfo{{Name: "review"}}
	m.setInputTo("/rev")

	m = pressRight(t, m)
	first := m.input.Value()
	m = pressRight(t, m)
	if first != "/review" || m.input.Value() != "/rev" {
		t.Errorf("walk gave %q then %q, want the name once and then what was typed", first, m.input.Value())
	}
}
