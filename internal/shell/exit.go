package shell

import (
	"path/filepath"
	"strings"
)

// A non-zero exit status is not always a fault.
//
// A handful of utilities answer a question with it. grep exits 1 when it
// found nothing, diff exits 1 when the inputs differ, test exits 1 when
// the condition is false. Reporting those as errors tells the model its
// command broke when the command in fact worked and gave an answer — and
// the model's recovery for a broken command is to run it again.
//
// That is the report this exists for, measured rather than imagined: a
// sweep of `grep -n` calls over a file list, a third of them marked
// failed, the whole sweep restarted from the top twice. Every one of the
// "failures" was a file that did not contain the symbol. The tool had
// been asked a question, had answered it correctly, and had labelled the
// answer an error.
//
// This is the same kind of knowledge StoreStub and MissingInterpreter
// hold — localcode knowing something specific about a command shape and
// saying it — and it is bounded the same way. The status is interpreted
// only when it is certain which program produced it.

// exitAnswers maps a utility to the statuses it uses as answers, and to
// what each one says. A utility absent from the table, or a status not
// listed under it, means no claim: grep's 2 really is an error and stays
// one, which is what keeps a missing file from being read as an empty
// one.
//
// A fixed table rather than something that needs maintaining. These are
// POSIX contracts and have not moved in forty years. rg is not POSIX but
// documents and keeps the same one, and is what a model reaches for
// first on a machine that has it.
var exitAnswers = map[string]map[int]string{
	"grep":  {1: "no matches"},
	"egrep": {1: "no matches"},
	"fgrep": {1: "no matches"},
	"rg":    {1: "no matches"},
	"diff":  {1: "the inputs differ"},
	"cmp":   {1: "the inputs differ"},
	"test":  {1: "the condition is false"},
	"[":     {1: "the condition is false"},
}

// ExitAnswer reports what status means for command, when that status is
// an answer rather than a fault. An empty string is "nothing is known",
// which is the honest reply for most commands and for every status this
// table does not list.
func ExitAnswer(command string, status int) string {
	// One command, or nothing is claimed. `grep x f | head` exits with
	// head's status and `a && b` with whichever of them ran last, so in
	// either case the utility named first did not necessarily produce
	// the number being explained. A wrong explanation is worse than the
	// plain status: it would tell the model a build failure was a file
	// with no matches in it.
	segments := splitSegments(command)
	if len(segments) != 1 {
		return ""
	}
	return exitAnswers[utilityName(segments[0])][status]
}

// utilityName is the program one command segment runs: lowercased, with
// any directory and any .exe stripped, so that /usr/bin/grep and
// GREP.EXE both arrive as "grep". Empty when there is no program to
// name.
//
// Leading environment assignments are skipped, because `LC_ALL=C grep`
// runs grep. Everything after the program name is left alone — the first
// field that is not an assignment is the answer, whatever it looks like.
func utilityName(segment string) string {
	for _, field := range strings.Fields(segment) {
		if isAssignment(field) {
			continue
		}
		return strings.TrimSuffix(strings.ToLower(filepath.Base(field)), ".exe")
	}
	return ""
}

// isAssignment reports whether a field is a VAR=value prefix rather than
// the program being run.
//
// The name has to look like a name: "LC_ALL=C" assigns, while "--opt=x"
// and "=x" do not and would otherwise swallow the program that follows
// them. Anything is allowed on the right of the "=", including nothing.
func isAssignment(field string) bool {
	eq := strings.Index(field, "=")
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := field[i]
		alnum := c == '_' ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9')
		if !alnum {
			return false
		}
	}
	return true
}
