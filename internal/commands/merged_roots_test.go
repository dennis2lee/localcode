package commands

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// Every root is read now, so a root that used to be ignored is read too,
// and what is in it may be a leftover: a file where the commands directory
// should be. That costs the root its commands, with one line saying why,
// and does not cost the load the rest.
func TestAnUnreadableCommandsDirectoryIsSkippedNotFatal(t *testing.T) {
	logs := captureLog(t)
	leftover := filepath.Join(t.TempDir(), "commands")
	if err := os.WriteFile(leftover, []byte("a file, not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := t.TempDir()
	writeCommand(t, good, "ship.md", "---\ndescription: ship it\n---\n\nship\n")

	list, err := LoadAll(leftover, good)
	if err != nil {
		t.Fatalf("a file where a commands directory should be stopped the load: %v", err)
	}
	if len(list) != 1 || list[0].Name != "ship" {
		t.Errorf("commands = %+v, want ship from the directory that can be read", list)
	}
	if !strings.Contains(logs.String(), leftover) || !strings.Contains(logs.String(), "cannot be read") {
		t.Errorf("nothing said why %s was skipped: %q", leftover, logs.String())
	}
}

// A command that does not parse does not hold its name: it is skipped with a
// line naming its file, and a lower one of the same name loads.
func TestABrokenHigherCommandLetsALowerOneLoad(t *testing.T) {
	logs := captureLog(t)
	higher, lower := t.TempDir(), t.TempDir()
	writeCommand(t, higher, "ship.md", "---\ndescription: [unclosed\n---\n\nhigher\n")
	writeCommand(t, lower, "ship.md", "---\ndescription: lower\n---\n\nlower\n")

	list, err := LoadAll(higher, lower)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Description != "lower" {
		t.Errorf("commands = %+v, want the lower ship", list)
	}
	broken := filepath.Join(higher, "ship.md")
	if n := strings.Count(logs.String(), broken); n != 1 {
		t.Errorf("the broken file is named %d times in the log, want once: %q", n, logs.String())
	}
}

// One directory under two names, ~/.opencode/command linked to
// ~/.claude/commands, is read once: its broken file is reported once, not
// once per name.
func TestALinkedCommandsDirectoryIsReadOnce(t *testing.T) {
	logs := captureLog(t)
	real := t.TempDir()
	writeCommand(t, real, "bad.md", "---\ndescription: [unclosed\n---\n\nbad\n")
	link := filepath.Join(t.TempDir(), "command")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symbolic link can be made here: %v", err)
	}
	if _, err := LoadAll(real, link); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "bad.md"); n != 1 {
		t.Errorf("a directory linked under a second name was read %d times: %q", n, logs.String())
	}
}
