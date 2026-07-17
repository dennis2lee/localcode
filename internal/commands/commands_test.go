package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCommand(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}
}

func TestLoadAllParsesFrontmatterAndBody(t *testing.T) {
	dir := t.TempDir()
	writeCommand(t, dir, "test.md", "---\ndescription: Run the test suite\nagent: build\nmodel: strong-model\n---\nRun tests for: $ARGUMENTS\n")

	cmds, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	c := cmds[0]
	if c.Name != "test" {
		t.Errorf("Name = %q, want \"test\"", c.Name)
	}
	if c.Description != "Run the test suite" {
		t.Errorf("Description = %q", c.Description)
	}
	if c.Agent != "build" {
		t.Errorf("Agent = %q, want \"build\"", c.Agent)
	}
	if c.Model != "strong-model" {
		t.Errorf("Model = %q, want \"strong-model\"", c.Model)
	}
	if !strings.Contains(c.Body, "$ARGUMENTS") {
		t.Errorf("Body = %q, want it to retain the $ARGUMENTS placeholder", c.Body)
	}
}

func TestLoadAllWithoutFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeCommand(t, dir, "plain.md", "Just a plain prompt template, no frontmatter.\n")

	cmds, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Name != "plain" {
		t.Fatalf("expected 1 command named \"plain\", got %+v", cmds)
	}
	if cmds[0].Description != "" {
		t.Errorf("expected empty description, got %q", cmds[0].Description)
	}
}

func TestLoadAllProjectOverridesGlobal(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()

	writeCommand(t, global, "review.md", "---\ndescription: GLOBAL\n---\nglobal body")
	writeCommand(t, project, "review.md", "---\ndescription: PROJECT\n---\nproject body")
	writeCommand(t, global, "onlyglobal.md", "---\ndescription: only in global\n---\nbody")

	cmds, err := LoadAll(project, global)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands (review overridden + onlyglobal), got %d: %+v", len(cmds), cmds)
	}
	byName := map[string]Command{}
	for _, c := range cmds {
		byName[c.Name] = c
	}
	if byName["review"].Description != "PROJECT" {
		t.Errorf("expected project-local review command to win, got %q", byName["review"].Description)
	}
	if _, ok := byName["onlyglobal"]; !ok {
		t.Error("expected onlyglobal command to still be loaded")
	}
}

func TestLoadAllIgnoresNonMarkdownAndMissingDirs(t *testing.T) {
	dir := t.TempDir()
	writeCommand(t, dir, "notacommand.txt", "ignored")
	writeCommand(t, dir, "real.md", "body")

	cmds, err := LoadAll(dir, filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Name != "real" {
		t.Fatalf("expected only \"real\" command, got %+v", cmds)
	}
}

func TestExpandArgumentsAndPositional(t *testing.T) {
	cmd := Command{Body: "all=[$ARGUMENTS] first=[$1] second=[$2] third=[$3]"}
	got, err := Expand(cmd, "alpha beta", t.TempDir())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := "all=[alpha beta] first=[alpha] second=[beta] third=[]"
	if got != want {
		t.Errorf("Expand() = %q, want %q", got, want)
	}
}

func TestExpandShellInjection(t *testing.T) {
	cmd := Command{Body: "output: !`echo hello`"}
	got, err := Expand(cmd, "", t.TempDir())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "output: hello" {
		t.Errorf("Expand() = %q, want %q", got, "output: hello")
	}
}

func TestExpandShellRunsInGivenCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("here"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := Command{Body: "!`cat marker.txt`"}
	got, err := Expand(cmd, "", dir)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "here" {
		t.Errorf("Expand() = %q, want %q", got, "here")
	}
}

func TestExpandFileInclusion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("important context"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := Command{Body: "See @notes.txt for background."}
	got, err := Expand(cmd, "", dir)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !strings.Contains(got, "important context") {
		t.Errorf("Expand() = %q, want it to contain the included file's content", got)
	}
	if !strings.Contains(got, "notes.txt") {
		t.Errorf("Expand() = %q, want it to reference the included file's path", got)
	}
}

func TestExpandFileInclusionMissingFileErrors(t *testing.T) {
	cmd := Command{Body: "See @missing.txt"}
	if _, err := Expand(cmd, "", t.TempDir()); err == nil {
		t.Error("expected an error for a missing @file reference")
	}
}

func TestExpandShellFailureErrors(t *testing.T) {
	cmd := Command{Body: "!`exit 1`"}
	if _, err := Expand(cmd, "", t.TempDir()); err == nil {
		t.Error("expected an error for a failing shell command")
	}
}

// TestExpandShellOutputNotReScannedForImports guards against a
// directive-injection bug: a shell command whose stdout happens to contain
// an "@path" reference must NOT cause that path to be read and inlined.
func TestExpandShellOutputNotReScannedForImports(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The shell prints an @-reference to the secret file; it must survive
	// as literal text, not be expanded into the file's contents.
	cmd := Command{Body: "!`echo @" + secret + "`"}
	got, err := Expand(cmd, "", dir)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if strings.Contains(got, "TOP SECRET") {
		t.Errorf("shell output was re-scanned and the file got inlined: %q", got)
	}
	if !strings.Contains(got, "@"+secret) {
		t.Errorf("expected the @-reference to remain literal in the output, got %q", got)
	}
}

// TestExpandArgumentNotReScannedForImports guards the same injection via an
// argument: a "$ARGUMENTS" value that contains an "@path" must not trigger
// a file read.
func TestExpandArgumentNotReScannedForImports(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := Command{Body: "user said: $ARGUMENTS"}
	got, err := Expand(cmd, "@"+secret, dir)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if strings.Contains(got, "TOP SECRET") {
		t.Errorf("argument was re-scanned and the file got inlined: %q", got)
	}
}

// TestExpandShellReceivesSubstitutedArgs confirms $N still reaches the
// shell command itself (a legitimate, preserved feature).
func TestExpandShellReceivesSubstitutedArgs(t *testing.T) {
	cmd := Command{Body: "!`echo got:$1`"}
	got, err := Expand(cmd, "hello", t.TempDir())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "got:hello" {
		t.Errorf("Expand() = %q, want %q (arg substituted into the shell command)", got, "got:hello")
	}
}
