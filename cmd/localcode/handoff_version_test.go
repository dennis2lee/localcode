package main

import (
	"strings"
	"testing"
)

// What the splash is told while a startup handoff happens.
//
// Reported with a photograph: the window read "LocalCode v0.108.1" above
// a status line reading "starting localcode 0.109.0". Both were true.
// The label is the shell's own version, fixed when the window opens, and
// after an update the shell is the copy the shortcut points at rather
// than the one about to serve — which makes the version, the one thing
// anybody reads to decide whether an update took, the stale one.
func TestTheSplashIsToldWhichVersionIsComingUp(t *testing.T) {
	var lines, versions []string
	progress := func(s string) { lines = append(lines, s) }
	setVersion := func(s string) { versions = append(versions, s) }

	handoffComing("0.109.0", progress, setVersion)

	if len(versions) != 1 || versions[0] != "0.109.0" {
		t.Errorf("the version label was told %v, want [0.109.0]", versions)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "0.109.0") {
		t.Errorf("the status line said %v", lines)
	}
}

// And put back if that version will not start. Having promised one, the
// window then comes up on the other, and leaving the label on the
// promise is the same untruth the other way round.
func TestASuccessorThatWillNotStartPutsTheVersionBack(t *testing.T) {
	var lines, versions []string
	progress := func(s string) { lines = append(lines, s) }
	setVersion := func(s string) { versions = append(versions, s) }

	handoffFailed("0.109.0", "0.108.1", progress, setVersion)

	if len(versions) != 1 || versions[0] != "0.108.1" {
		t.Errorf("the version label was told %v, want [0.108.1] — it still names a version that did not start", versions)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "would not start") {
		t.Errorf("the status line said %v", lines)
	}
}

// Nothing is said when the successor's version could not be read: the
// window is coming up on itself, which is what the label already says.
func TestNothingIsSaidWhenThereIsNoSuccessorVersion(t *testing.T) {
	var lines, versions []string
	handoffFailed("", "0.108.1",
		func(s string) { lines = append(lines, s) },
		func(s string) { versions = append(versions, s) })
	if len(lines) != 0 || len(versions) != 0 {
		t.Errorf("said %v / %v about a successor whose version was never read", lines, versions)
	}
}
