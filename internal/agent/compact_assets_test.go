package agent

import (
	"strings"
	"testing"

	"localcode/internal/provider"
)

func msgWithSources(ids ...string) provider.Message {
	b := provider.TextBlock("x")
	for _, id := range ids {
		b.Sources = append(b.Sources, provider.BlockSource{ID: id})
	}
	return provider.Message{Role: provider.RoleUser, Content: []provider.Block{b}}
}

// A skill body and a command expansion live in the conversation, so
// compaction takes them. The model is told which ones, most recent
// first, so it can reload what it is still working from.
func TestCompactionNamesWhatItsSummaryReplaced(t *testing.T) {
	history := []provider.Message{
		msgWithSources("file.README.md"),
		msgWithSources("command.deploy"),
		msgWithSources("skill.body.release"),
	}
	got := droppedCarriedAssets(history)
	if len(got) != 3 || got[0] != "skill release" {
		t.Fatalf("got %v, want the most recent first", got)
	}

	note := carriedAssetNote(got)
	for _, want := range []string{"skill release", "command deploy", "spliced file README.md", "Load again"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
}

// localcode's own framing and notices are rebuilt on the next request,
// so naming them would be noise.
func TestOnlyAuthoredContentIsNamed(t *testing.T) {
	history := []provider.Message{msgWithSources("skill.frame.release", "reminder.time", "conversation")}
	if got := droppedCarriedAssets(history); len(got) != 0 {
		t.Errorf("got %v, want nothing: those are rebuilt every request", got)
	}
}

// The person's own mid-turn instruction has no name, and saying so once
// reads better than a bare id.
func TestATypedInstructionIsNamedInWords(t *testing.T) {
	got := droppedCarriedAssets([]provider.Message{msgWithSources("injected.user")})
	if len(got) != 1 || !strings.Contains(got[0], "you typed") {
		t.Errorf("got %v", got)
	}
}

// A session that invoked forty skills must not spend its summary listing
// them.
func TestTheListIsCapped(t *testing.T) {
	var names []string
	for i := 0; i < 20; i++ {
		names = append(names, "skill s")
	}
	note := carriedAssetNote(names)
	if !strings.Contains(note, "and 14 more") {
		t.Errorf("note = %q", note)
	}
}

// Nothing carried means nothing said.
func TestNoNoteWhenNothingWasCarried(t *testing.T) {
	if got := carriedAssetNote(nil); got != "" {
		t.Errorf("note = %q", got)
	}
}
