package shell

import (
	"errors"
	"strings"
	"testing"
)

// Two ways "python" goes wrong on Windows, and one way localcode's own
// detector went wrong looking for the first.

// The live one, measured before it was fixed: the segment splitter did not
// know about quotes, so a semicolon inside a commit message started a new
// "command" whose first word was python3. On a machine where the Store
// alias is enabled that resolves, so the git commit was refused, unrun,
// and answered with instructions for installing an interpreter it was
// never going to use.
func TestAQuotedSemicolonDoesNotStartACommand(t *testing.T) {
	storeEverywhere := func(name string) (string, error) {
		return `C:\Users\u\AppData\Local\Microsoft\WindowsApps\` + name + `.exe`, nil
	}
	for _, command := range []string{
		`git commit -m "fix; python3 helper"`,
		`git commit -m 'refactor: drop the python shim; keep the tests'`,
		`echo "a && python b"`,
		`grep -r "python3 -m venv" docs/`,
		`git commit -m "use python | not python3"`,
	} {
		if msg, is := storeStub("windows", command, storeEverywhere); is {
			t.Errorf("refused a command that runs no interpreter:\n  %s\n  %s", command, msg)
		}
	}
}

// And it still splits where a shell would.
func TestTheSplitterStillFindsARealSecondCommand(t *testing.T) {
	storeEverywhere := func(name string) (string, error) {
		return `C:\Users\u\AppData\Local\Microsoft\WindowsApps\` + name + `.exe`, nil
	}
	for _, command := range []string{
		`cd /c/x && python3 setup.py`,
		`git pull; python -m pytest`,
		`cat f.txt | python3 -c "print(1)"`,
		`make build || python3 fallback.py`,
		"cd /c/x\npython3 run.py",
		`PYTHON3.EXE -c "print(1)"`,
	} {
		if _, is := storeStub("windows", command, storeEverywhere); !is {
			t.Errorf("did not notice the interpreter in: %s", command)
		}
	}
}

// An unbalanced quote is the safe direction: the rest of the line becomes
// one segment, so a split is missed rather than invented.
func TestAnUnbalancedQuoteDoesNotInventASplit(t *testing.T) {
	got := splitSegments(`echo "oops; python3 x`)
	if len(got) != 1 {
		t.Errorf("splitSegments(%q) = %q, want one segment", `echo "oops; python3 x`, got)
	}
}

// The other failure: the name is not on PATH at all. LookPath fails, so
// storeStub skips it, and the model used to get a bare shell error whose
// plain reading ("Python is missing") is false on a machine that has a
// perfectly good python.exe in a conda root.
func TestAnInterpreterNotOnPathIsExplained(t *testing.T) {
	nothing := func(string) (string, error) { return "", errors.New("not found") }

	got := missingInterpreter("windows", `cd /c/x && python3 - <<'PY'`, true, nothing, true)
	if got == "" {
		t.Fatal("a python3 that is not on PATH was not explained")
	}
	for _, want := range []string{
		"python3 is not on PATH",
		"does not provide python3.exe",
		"fresh shell",
		"absolute path",
		"miniforge3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the message is missing %q:\n%s", want, got)
		}
	}
}

// It is the complement of the Store check, not a second one: exactly one
// of them can answer for a given command.
func TestTheTwoWindowsPythonChecksDoNotOverlap(t *testing.T) {
	command := "python3 x.py"

	onPathAsStub := func(name string) (string, error) {
		return `C:\Users\u\AppData\Local\Microsoft\WindowsApps\` + name + `.exe`, nil
	}
	if _, is := storeStub("windows", command, onPathAsStub); !is {
		t.Error("the stub check did not fire for a stub")
	}
	if got := missingInterpreter("windows", command, true, onPathAsStub, true); got != "" {
		t.Errorf("both checks answered the same command:\n%s", got)
	}

	notOnPath := func(string) (string, error) { return "", errors.New("nope") }
	if _, is := storeStub("windows", command, notOnPath); is {
		t.Error("the stub check fired for something not on PATH")
	}
	if got := missingInterpreter("windows", command, true, notOnPath, true); got == "" {
		t.Error("neither check answered a command that failed for a name not on PATH")
	}
}

// Only after a failure, only on Windows, and only for a name it knows.
func TestTheExplanationIsNarrow(t *testing.T) {
	nothing := func(string) (string, error) { return "", errors.New("not found") }

	if got := missingInterpreter("windows", "python3 x.py", false, nothing, true); got != "" {
		t.Error("a command that succeeded was explained anyway")
	}
	if got := missingInterpreter("darwin", "python3 x.py", true, nothing, true); got != "" {
		t.Error("fired on a platform where python3 is the right name and the error means what it says")
	}
	if got := missingInterpreter("windows", "node x.js", true, nothing, true); got != "" {
		t.Error("fired for a command that is not an interpreter this knows")
	}
	// The named miss: a wrapper puts something else in the leading
	// position. Deliberate, because widening the matcher is what creates
	// false positives.
	if got := missingInterpreter("windows", "env python3 x.py", true, nothing, true); got != "" {
		t.Error("fired for a wrapped invocation, which is a case it deliberately does not cover")
	}
}

// When PATH does resolve a python, say so as a machine fact and not as a
// recommendation. Naming a conda base that cannot import the project turns
// one failed call into a wrong hypothesis to debug.
func TestAPythonOnPathIsReportedAsAFactNotAChoice(t *testing.T) {
	onlyPython := func(name string) (string, error) {
		if name == "python" {
			return `C:\Users\u\AppData\Local\miniforge3\python.exe`, nil
		}
		return "", errors.New("not found")
	}
	got := missingInterpreter("windows", "python3 x.py", true, onlyPython, true)
	if !strings.Contains(got, "/c/Users/u/AppData/Local/miniforge3/python.exe") {
		t.Errorf("the path was not rendered for the shell that will run it:\n%s", got)
	}
	if !strings.Contains(got, "separate question") {
		t.Errorf("the message reads as a recommendation rather than an observation:\n%s", got)
	}

	// A Store stub on PATH is not a python to point at.
	stubbed := func(name string) (string, error) {
		if name == "python" {
			return `C:\Users\u\AppData\Local\Microsoft\WindowsApps\python.exe`, nil
		}
		return "", errors.New("not found")
	}
	if got := missingInterpreter("windows", "python3 x.py", true, stubbed, true); strings.Contains(got, "There is a python on PATH") {
		t.Errorf("pointed at a Store stub:\n%s", got)
	}
}

// Whatever is printed has to be pasteable into the shell that will run it.
// A bare backslash path inside a bash -c string is eaten by bash before
// MSYS ever sees it.
func TestPathsAreRenderedForTheShellThatWillRunThem(t *testing.T) {
	if got := forShell(`C:\Users\u\python.exe`, true); got != "/c/Users/u/python.exe" {
		t.Errorf("posix form = %q", got)
	}
	if got := forShell(`C:\Users\u\python.exe`, false); got != `C:\Users\u\python.exe` {
		t.Errorf("cmd form = %q", got)
	}
	if !strings.Contains(huntCommand(false), "%UserProfile%") {
		t.Error("the cmd hunt does not use cmd's own variable form")
	}
	if strings.Contains(huntCommand(true), `\`) {
		t.Errorf("the posix hunt contains a backslash bash would eat:\n%s", huntCommand(true))
	}
}
