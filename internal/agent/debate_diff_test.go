package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func commit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// The reason this exists: a list of write_file calls is not a diff. A
// file changed by the shell is invisible to the tool log and has to be
// in the report, and a file written back unchanged has to not be.
func TestTheDiffIsWhatChangedNotWhatWasWatched(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "sum.py", "print(1)\n")
	commit(t, dir)

	// Changed without any file tool: exactly the case the tool log cannot
	// see.
	cmd := exec.Command("sh", "-c", "printf 'print(sum(range(1,11)))\\n' > sum.py")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shell edit: %v\n%s", err, out)
	}

	diff, ok := workspaceDiff(dir)
	if !ok {
		t.Fatal("a git repository was not recognised as one")
	}
	if !strings.Contains(diff, "sum.py") || !strings.Contains(diff, "range(1,11)") {
		t.Errorf("diff = %q, want the shell's change to sum.py in it", diff)
	}
}

// A new file is named but not pasted: its whole contents would be the
// largest thing in the brief, and the reviewer has read tools.
func TestUntrackedFilesAreNamedNotPasted(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "kept.txt", "x\n")
	commit(t, dir)
	write(t, dir, "brand_new.py", "SECRET_LOOKING_CONTENT = 1\n")

	diff, ok := workspaceDiff(dir)
	if !ok {
		t.Fatal("not recognised as a repository")
	}
	if !strings.Contains(diff, "brand_new.py") {
		t.Errorf("diff = %q, want the new file named", diff)
	}
	if strings.Contains(diff, "SECRET_LOOKING_CONTENT") {
		t.Errorf("the new file's contents were pasted into the brief: %q", diff)
	}
}

// Nothing changed is an answer the reviewer needs, and it is not the
// same as "there is no repository here".
func TestACleanTreeIsAnAnswer(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.txt", "x\n")
	commit(t, dir)

	diff, ok := workspaceDiff(dir)
	if !ok {
		t.Fatal("not recognised as a repository")
	}
	if diff != "" {
		t.Errorf("diff = %q, want empty for a clean tree", diff)
	}
}

// Outside a repository there is no honest diff, and the caller has to be
// able to tell that from a clean one so it can fall back.
func TestOutsideARepositoryThereIsNoDiff(t *testing.T) {
	if _, ok := workspaceDiff(t.TempDir()); ok {
		t.Error("a plain directory was reported as a repository")
	}
	if _, ok := workspaceDiff(""); ok {
		t.Error("an empty path was reported as a repository")
	}
}

// A generated file or a lockfile can be a megabyte on its own, and a
// reviewer handed that has spent its context before reading the change.
func TestAnOversizedDiffIsCutAndSaysSo(t *testing.T) {
	huge := strings.Repeat("+a line of diff\n", diffLimit/8)
	got := clampDiff(huge)
	if len(got) > diffLimit+200 {
		t.Errorf("clamped diff is %d characters, want about %d", len(got), diffLimit)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("a truncated diff does not say it was truncated, so it reads as the whole change")
	}
}

// The report says which kind of answer it is. A reviewer that reads a
// list of watched tool calls as a diff reviews the wrong set of files
// and has no way to know.
func TestTheChangeReportSaysWhichKindOfAnswerItIs(t *testing.T) {
	loop, sid := changeReportLoop(t, gitRepo(t))
	if got := loop.changeReport(sid, nil); !strings.Contains(got, "AS GIT SEES IT") {
		t.Errorf("report in a repository = %q, want it to say the diff is git's", got)
	}

	loop, sid = changeReportLoop(t, t.TempDir())
	got := loop.changeReport(sid, []string{"sum.py"})
	if !strings.Contains(got, "not a diff") || !strings.Contains(got, "sum.py") {
		t.Errorf("report outside a repository = %q, want the file list, labelled as not a diff", got)
	}
}

// changeReportLoop is a loop with one session stamped to dir, which is
// all changeReport reads.
func changeReportLoop(t *testing.T, dir string) (*Loop, string) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), nil, &config.Config{})
	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "boy", dir, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return loop, sid
}
