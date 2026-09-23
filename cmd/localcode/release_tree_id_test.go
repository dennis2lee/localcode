package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tree identity is what lets `make dist` trust a gate that already
// ran, so the one thing it must never do is stop noticing a change while
// still printing a plausible hash.
//
// It did. `git ls-files --others` reports a nested git repository as a
// single directory entry, `shasum < <dir>` fails with "Is a directory",
// and the loop carried on: the entry contributed a constant, the digest
// stayed the same size, and the guard against a short walk never fired
// because only one line was lost. Measured before the fix, with a
// repository inside the checkout, the identity did not move when a file
// under it was rewritten or when a new one was added. The agent harness
// puts worktrees at .claude/worktrees, which is exactly that shape, and
// the only thing hiding them was .git/info/exclude, which is not
// committed.

// treeIDRepo builds a throwaway git repository with a copy of the script
// where the script expects to find it, and returns the repository path.
func treeIDRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "tree-id.sh"))
	if err != nil {
		t.Fatalf("read tree-id.sh: %v", err)
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("git", "init", "-q", ".")
	run("git", "config", "user.email", "t@example.invalid")
	run("git", "config", "user.name", "t")

	// Over the hundred-line floor the script refuses to go below, so the
	// failure being tested is the silent one and not that guard. Each
	// file is two lines of digest, and the script spawns a shasum per
	// file, so this is also what these tests cost: sixty is comfortably
	// over the floor and half the runtime of a rounder number.
	for i := 0; i < 60; i++ {
		name := filepath.Join(dir, "f"+strings.Repeat("0", 3-len(itoa(i)))+itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("file "+itoa(i)+"\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "tree-id.sh"), src, 0o755); err != nil {
		t.Fatalf("write tree-id.sh: %v", err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-qm", "init")
	return dir
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// treeID runs the script and returns its output and whether it succeeded.
func treeID(t *testing.T, dir string) (string, string, bool) {
	t.Helper()
	cmd := exec.Command(filepath.Join(dir, "scripts", "tree-id.sh"))
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), stderr.String(), err == nil
}

func TestTheTreeIdentityRefusesWhatItCannotSeeInto(t *testing.T) {
	dir := treeIDRepo(t)

	clean, _, ok := treeID(t, dir)
	if !ok || clean == "" {
		t.Fatalf("precondition: the script failed on an ordinary tree")
	}

	// The property the whole stamp rests on: an edit moves the identity.
	if err := os.WriteFile(filepath.Join(dir, "f001.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	edited, _, ok := treeID(t, dir)
	if !ok {
		t.Fatal("the script failed after an ordinary edit")
	}
	if edited == clean {
		t.Fatal("an edited file did not move the tree identity")
	}

	// A repository inside the checkout, which is what the agent harness
	// leaves at .claude/worktrees and what git reports as one directory.
	nested := filepath.Join(dir, "inner")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, args := range [][]string{{"git", "init", "-q", "."}, {"git", "config", "user.email", "t@example.invalid"}, {"git", "config", "user.name", "t"}} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = nested
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(nested, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, stderr, ok := treeID(t, dir)
	if ok {
		t.Errorf("the script printed an identity (%s) for a tree holding a repository it cannot read, so the stamp would claim to cover files nothing hashed", out)
	}
	if !strings.Contains(stderr, "inner") {
		t.Errorf("the refusal does not name what it could not hash:\n%s", stderr)
	}

	// And it is usable again once the directory is ignored, which is the
	// fix the message asks for.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("inner/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if _, stderr, ok := treeID(t, dir); !ok {
		t.Errorf("the script still refuses after the directory was ignored:\n%s", stderr)
	}
}

// A directory was one shape. A file this user cannot read is another,
// and it corrupted the digest the same silent way: the hash failed, the
// mode was already printed without its hash, and the next path was
// joined onto that half-written line.
func TestTheTreeIdentityRefusesAFileItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can read a file with no permissions")
	}
	dir := treeIDRepo(t)
	if _, _, ok := treeID(t, dir); !ok {
		t.Fatal("precondition: the script failed on an ordinary tree")
	}

	locked := filepath.Join(dir, "locked.txt")
	if err := os.WriteFile(locked, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	out, stderr, ok := treeID(t, dir)
	if ok {
		t.Errorf("the script printed an identity (%s) for a tree holding a file it could not read", out)
	}
	if !strings.Contains(stderr, "locked.txt") {
		t.Errorf("the refusal does not name the file it could not read:\n%s", stderr)
	}
}

// The things the identity must still notice, so a refusal that was made
// too eager would be caught here rather than by a release that never
// re-ran the gate.
func TestTheTreeIdentityStillSeesOrdinaryChanges(t *testing.T) {
	dir := treeIDRepo(t)
	seen := map[string]string{}
	take := func(what string) string {
		t.Helper()
		out, stderr, ok := treeID(t, dir)
		if !ok {
			t.Fatalf("%s: the script refused:\n%s", what, stderr)
		}
		if prev, dup := seen[out]; dup {
			t.Errorf("%s gives the same identity as %s", what, prev)
		}
		seen[out] = what
		return out
	}

	take("the tree as committed")
	if err := os.WriteFile(filepath.Join(dir, "f002.txt"), []byte("rewritten\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	take("a file rewritten")
	if err := os.WriteFile(filepath.Join(dir, "brand-new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	take("an untracked file added")
	if err := os.Chmod(filepath.Join(dir, "f003.txt"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	take("a file made executable")
	if err := os.Symlink("f004.txt", filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	take("a symlink added")
	if err := os.Remove(filepath.Join(dir, "f005.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	take("a tracked file deleted")
}

// The harness's own worktrees are ignored by the committed .gitignore and
// not by .git/info/exclude, which no clone carries.
func TestTheHarnessWorktreesAreIgnoredByTheCommittedFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, want := range []string{".claude/worktrees/", "notes.md"} {
		found := false
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is not in the committed .gitignore, so it enters the tree identity: a worktree makes the gate refuse to stamp, and notes.md invalidates the stamp the gate just wrote", want)
		}
	}
}
