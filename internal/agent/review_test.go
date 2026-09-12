package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"localcode/internal/commands"
)

// "/review" came first on a parity review's candidate list and then was
// not built: the cycle that followed implemented the verification of
// that list rather than the list.

func TestReviewNamesTheRangeItIsAbout(t *testing.T) {
	for _, c := range []struct {
		arg       string
		what, how string
	}{
		{"", "uncommitted", "diff HEAD"},
		{"staged", "staged", "--cached"},
		{"head", "most recent commit", "show HEAD"},
		{"last", "most recent commit", "show HEAD"},
		// Case is not the question.
		{"STAGED", "staged", "--cached"},
		// Anything else goes to git as typed: a revision, a range, a
		// branch and a path are the same characters here, and git is the
		// thing that knows them apart.
		{"main..HEAD", "main..HEAD", "diff main..HEAD"},
		{"internal/agent", "internal/agent", "diff internal/agent"},
	} {
		what, how := reviewRange(c.arg)
		if !strings.Contains(what, c.what) {
			t.Errorf("reviewRange(%q) describes %q, want it to mention %q", c.arg, what, c.what)
		}
		if !strings.Contains(how, c.how) {
			t.Errorf("reviewRange(%q) runs %q, want it to contain %q", c.arg, how, c.how)
		}
	}
}

// The prompt that reaches the model names the range, asks for concrete
// findings, and says not to change anything.
func TestReviewSendsAReviewPrompt(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")

	if err := loop.SendMessage(context.Background(), sid, "boy", "/review staged"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := bodies()
	if len(reqs) == 0 {
		t.Fatal("/review never reached the model")
	}
	raw, err := json.Marshal(reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	sent := string(raw)
	for _, want := range []string{
		"the staged changes",
		"--cached",
		// A range that turns up empty is reported rather than quietly
		// replaced with something else: a review of the wrong diff reads
		// exactly like a review of the right one.
		"say so and stop rather than reviewing something else",
		"Do not change any file",
		// Concrete findings, ranked. A review that manufactures findings
		// to look thorough is worse than a short one.
		"Rank the findings",
		"Say so plainly when the change is fine",
	} {
		if !strings.Contains(sent, want) {
			t.Errorf("the review prompt does not say %q", want)
		}
	}
}

// The one built-in that yields to a command somebody wrote themselves.
//
// The name was in people's .localcode/commands before the built-in
// existed — there was none, the documentation said to write one for
// exactly this, and they did. Taking it now would silently replace a
// tuned file with a template that knows nothing about their repository.
func TestAReviewCommandOfYourOwnWins(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")
	loop.Commands = append(loop.Commands, commands.Command{Name: "review", Body: "my own review, for this repository"})

	if err := loop.SendMessage(context.Background(), sid, "boy", "/review"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := bodies()
	if len(reqs) == 0 {
		t.Fatal("nothing reached the model")
	}
	raw, _ := json.Marshal(reqs[0])
	sent := string(raw)
	if !strings.Contains(sent, "my own review, for this repository") {
		t.Errorf("the custom command was shadowed by the built-in: %s", firstBytes(sent))
	}
	if strings.Contains(sent, "Rank the findings") {
		t.Errorf("the built-in template was sent over somebody's own review command: %s", firstBytes(sent))
	}
}
