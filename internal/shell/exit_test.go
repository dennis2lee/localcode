package shell

import "testing"

// The distinction the whole table exists for: grep says "I looked and
// found nothing" with 1 and "I could not look" with 2, and only the first
// is an answer. Collapsing them is how a missing file comes to read like
// an empty one.
func TestGrepsNoMatchIsAnAnswerAndItsRealErrorIsNot(t *testing.T) {
	if got := ExitAnswer(`grep -n "sym" file.h`, 1); got != "no matches" {
		t.Errorf("status 1 = %q, want %q", got, "no matches")
	}
	if got := ExitAnswer(`grep -n "sym" file.h`, 2); got != "" {
		t.Errorf("status 2 = %q, want no claim", got)
	}
}

func TestTheUtilitiesThatAnswerWithAStatus(t *testing.T) {
	for _, tc := range []struct {
		command string
		status  int
		want    string
	}{
		{`grep x f`, 1, "no matches"},
		{`egrep x f`, 1, "no matches"},
		{`fgrep x f`, 1, "no matches"},
		{`rg x`, 1, "no matches"},
		{`diff a b`, 1, "the inputs differ"},
		{`cmp a b`, 1, "the inputs differ"},
		{`test -f x`, 1, "the condition is false"},
		{`[ -f x ]`, 1, "the condition is false"},
		// Not in the table, and the common case: a build that failed is
		// a build that failed.
		{`go build ./...`, 1, ""},
		{`make check`, 2, ""},
	} {
		if got := ExitAnswer(tc.command, tc.status); got != tc.want {
			t.Errorf("ExitAnswer(%q, %d) = %q, want %q", tc.command, tc.status, got, tc.want)
		}
	}
}

// The program can be spelled several ways and is the same program each
// time. An absolute path is what a model writes when it has just run
// `which`, and the .exe is what Windows gives it.
func TestTheProgramIsFoundThroughItsSpelling(t *testing.T) {
	for _, command := range []string{
		`/usr/bin/grep x f`,
		`GREP.EXE x f`,
		`LC_ALL=C grep x f`,
		`LC_ALL=C GREP_OPTIONS= /usr/bin/grep x f`,
	} {
		if got := ExitAnswer(command, 1); got != "no matches" {
			t.Errorf("ExitAnswer(%q, 1) = %q, want %q", command, got, "no matches")
		}
	}
}

// An "=" does not make a field an assignment. If it did, the program
// would be swallowed and whatever came after it would be read as the
// program instead.
func TestAnOptionWithAnEqualsIsNotAnAssignment(t *testing.T) {
	// The program here is diff, not --unified=3.
	if got := ExitAnswer(`diff --unified=3 a b`, 1); got != "the inputs differ" {
		t.Errorf("got %q, want %q", got, "the inputs differ")
	}
	// And a leading option must not be mistaken for the program either.
	if got := ExitAnswer(`--color=always x f`, 1); got != "" {
		t.Errorf("got %q, want no claim", got)
	}
}

// Where more than one command ran, the status belongs to whichever
// finished last, and naming the first would be a guess. A wrong guess is
// worse than the bare number: it would report a failed build as a search
// that found nothing.
func TestNothingIsClaimedWhenAnotherCommandCouldHaveExited(t *testing.T) {
	for _, command := range []string{
		`grep x f | head -1`,
		`grep x f && echo yes`,
		`grep x f || true`,
		`grep x f; go build ./...`,
		"grep x f\ngo build ./...",
		`go build ./... | grep error`,
	} {
		if got := ExitAnswer(command, 1); got != "" {
			t.Errorf("ExitAnswer(%q, 1) = %q, want no claim", command, got)
		}
	}
}

// The segment scanner is quote-aware, and it has to be here for the same
// reason it had to be for StoreStub: a pattern is a string the person
// searching for it chose, and it may contain anything.
func TestASeparatorInsideThePatternDoesNotCountAsASecondCommand(t *testing.T) {
	for _, command := range []string{
		`grep -n "a|b" file.h`,
		`grep -n 'foo; bar' file.h`,
		`grep -n "x && y" file.h`,
	} {
		if got := ExitAnswer(command, 1); got != "no matches" {
			t.Errorf("ExitAnswer(%q, 1) = %q, want %q", command, got, "no matches")
		}
	}
}

// A redirection does not run a second program, so it does not change
// whose status this is.
func TestARedirectionLeavesTheStatusWithTheCommand(t *testing.T) {
	if got := ExitAnswer(`grep -n x file.h > out.txt`, 1); got != "no matches" {
		t.Errorf("got %q, want %q", got, "no matches")
	}
}

func TestAnEmptyCommandClaimsNothing(t *testing.T) {
	for _, command := range []string{"", "   ", "LC_ALL=C"} {
		if got := ExitAnswer(command, 1); got != "" {
			t.Errorf("ExitAnswer(%q, 1) = %q, want no claim", command, got)
		}
	}
}
