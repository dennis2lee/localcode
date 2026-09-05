package main

import (
	"os"
	"strings"
	"testing"
)

// write puts bytes where the successor would put them: straight into the
// file it was given. The parent no longer sees them go past — that is the
// point of the change these tests came with — so a test stands in for the
// child by writing to the same file.
func writeAsSuccessor(t *testing.T, out *successorOutput, s string) {
	t.Helper()
	stdout, stderr := out.streams()
	if stdout != stderr {
		t.Fatalf("the successor was given two different streams: %v and %v", stdout, stderr)
	}
	if _, err := stdout.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

// A handoff that fails used to say only that the successor did not begin
// serving. In the desktop window the successor's own account went to a
// console that does not exist, so a timeout arrived with no cause and no
// way to find one.
func TestAFailedHandoffQuotesWhatTheSuccessorSaid(t *testing.T) {
	out := newSuccessorOutput()
	defer out.Close()
	if out.path == "" {
		t.Skip("no cache directory to write the log in")
	}
	writeAsSuccessor(t, out, "panic: config.json: unexpected end of JSON input\n")

	note := out.note()
	if !strings.Contains(note, "unexpected end of JSON input") {
		t.Errorf("note does not quote the successor: %q", note)
	}
	if !strings.Contains(note, out.path) {
		t.Errorf("note does not say where the rest is: %q", note)
	}
	body, err := os.ReadFile(out.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "unexpected end of JSON input") {
		t.Errorf("the file does not hold the output: %s", body)
	}
}

// Silence is a finding too, and it has to read as one rather than as an
// empty message.
func TestASilentSuccessorSaysSo(t *testing.T) {
	out := newSuccessorOutput()
	defer out.Close()
	if note := out.note(); !strings.Contains(note, "printed nothing") {
		t.Errorf("note = %q", note)
	}
}

// Only the tail is quoted: a Go panic is longer than a transcript line,
// and the last frames are the ones that name the cause.
func TestOnlyTheTailIsQuoted(t *testing.T) {
	out := newSuccessorOutput()
	defer out.Close()
	if out.path == "" {
		t.Skip("no cache directory to write the log in")
	}
	writeAsSuccessor(t, out, strings.Repeat("noise\n", 2000))
	writeAsSuccessor(t, out, "the actual reason\n")

	note := out.note()
	if !strings.Contains(note, "the actual reason") {
		t.Error("the tail was dropped, which is where the reason is")
	}
	if len(note) > successorTail+400 {
		t.Errorf("the note is %d bytes; it goes in a transcript", len(note))
	}
}

// What the successor writes to has to be a real file.
//
// os/exec inherits a *os.File straight through to the child; hand it any
// other writer and it builds an os.Pipe whose read end lives in THIS
// process. A handoff makes a chain — generation 1 spawns generation 2 and
// then exits — so generation 2's stderr was a pipe belonging to a process
// that had gone, and the next line it printed raised SIGPIPE. Go makes
// SIGPIPE on descriptor 1 or 2 fatal, so the new daemon was killed by its
// own first diagnostic.
func TestTheSuccessorIsGivenRealFilesAndNeverAPipe(t *testing.T) {
	out := newSuccessorOutput()
	defer out.Close()

	stdout, stderr := out.streams()
	if stdout == nil || stderr == nil {
		t.Fatal("the successor was given no streams at all")
	}
	// *os.File is the type; that it is one is the whole assertion, and
	// the signature already says so. What a test can still check is that
	// they are usable and that neither is a pipe this process made.
	for name, f := range map[string]*os.File{"stdout": stdout, "stderr": stderr} {
		info, err := f.Stat()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode()&os.ModeNamedPipe != 0 {
			t.Errorf("%s is a pipe; the successor will die of SIGPIPE when this process goes", name)
		}
	}
}
