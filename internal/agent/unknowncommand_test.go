package agent

import (
	"context"
	"strings"
	"testing"
)

// A slash command this build does not have is answered here, in English,
// and is never handed to the model.
//
// The report this is for: somebody typed a command that did not exist, it
// went to the model as an ordinary prompt, and the model — with a shell
// and skipped permissions — did what the word meant. Every untracked file
// in the project was gone.

func TestAnUnknownCommandIsAnsweredAndNotSent(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")

	if err := loop.SendMessage(context.Background(), sid, "boy", "/clean"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := len(bodies()); n != 0 {
		t.Fatalf("the unknown command reached the model (%d requests)", n)
	}

	reply := lastReply(t, loop, sid)
	for _, want := range []string{"no /clean", "Commands:", "/clear"} {
		if !strings.Contains(reply, want) {
			t.Errorf("the answer does not mention %q: %s", want, reply)
		}
	}
}

// One edit away and only one candidate: a suggestion. Two equally close
// candidates are a coin toss, so none is offered.
func TestTheSuggestionIsOfferedOnlyWhenItIsUnambiguous(t *testing.T) {
	known := []string{"clear", "compact", "context", "config"}
	for _, c := range []struct{ asked, want string }{
		{"clean", "clear"},  // one substitution
		{"clea", "clear"},   // one deletion
		{"cleart", "clear"}, // one insertion
		{"clear", "clear"},  // itself
		{"conte", ""},       // context and config are both two away, and conte is one from neither
		{"zzzzzz", ""},      // nothing close
	} {
		if got := nearestCommand(known, c.asked); got != c.want {
			t.Errorf("nearestCommand(%q) = %q, want %q", c.asked, got, c.want)
		}
	}
	// Two names each one edit from the same typo: a coin toss, so no
	// guess. ("clean" and "cleat" are both one substitution from
	// "clear"; "cle" is two away and would not be a second candidate.)
	if got := nearestCommand([]string{"clean", "cleat"}, "clear"); got != "" {
		t.Errorf("nearestCommand with two equally close names = %q, want none", got)
	}
}

// A path is prose. A second slash or a dot is what tells them apart.
func TestWhatCountsAsACommand(t *testing.T) {
	for _, c := range []struct {
		text string
		name string
		ok   bool
	}{
		{"/clear", "clear", true},
		{"/clean the build dir", "clean", true},
		{"/effort-set high", "effort-set", true},
		{"/etc/hosts needs a line", "", false},
		{"/tmp/build.log has it", "", false},
		{"/Users/someone/x.go", "", false},
		{"/1abc", "", false},
		{"/", "", false},
		{"not a command", "", false},
		{"", "", false},
	} {
		name, ok := looksLikeCommand(c.text)
		if ok != c.ok || name != c.name {
			t.Errorf("looksLikeCommand(%q) = (%q, %v), want (%q, %v)", c.text, name, ok, c.name, c.ok)
		}
	}
}
