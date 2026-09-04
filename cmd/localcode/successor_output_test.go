package main

import (
	"os"
	"strings"
	"testing"
)

// A handoff that fails used to say only that the successor did not begin
// serving. In the desktop window the successor's own account went to a
// console that does not exist, so a timeout arrived with no cause and no
// way to find one.
func TestAFailedHandoffQuotesWhatTheSuccessorSaid(t *testing.T) {
	out := newSuccessorOutput()
	defer out.Close()
	out.Write([]byte("panic: config.json: unexpected end of JSON input\n"))

	note := out.note()
	if !strings.Contains(note, "unexpected end of JSON input") {
		t.Errorf("note does not quote the successor: %q", note)
	}
	if out.path != "" && !strings.Contains(note, out.path) {
		t.Errorf("note does not say where the rest is: %q", note)
	}
	if out.path != "" {
		body, err := os.ReadFile(out.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "unexpected end of JSON input") {
			t.Errorf("the file does not hold the output: %s", body)
		}
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
	out.Write([]byte(strings.Repeat("noise\n", 2000)))
	out.Write([]byte("the actual reason\n"))

	note := out.note()
	if !strings.Contains(note, "the actual reason") {
		t.Error("the tail was dropped, which is where the reason is")
	}
	if len(note) > successorTail+400 {
		t.Errorf("the note is %d bytes; it goes in a transcript", len(note))
	}
}

// A console that refuses writes must not take the copy with it. That is
// the whole reason the passthrough exists: io.MultiWriter gives up on the
// first error, and in a GUI process the console is the first writer and
// it always errors.
func TestARefusingConsoleDoesNotSwallowTheCopy(t *testing.T) {
	out := newSuccessorOutput()
	defer out.Close()
	n, err := passthrough{refusingWriter{}}.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Errorf("passthrough reported n=%d err=%v; it must absorb the refusal", n, err)
	}
}

type refusingWriter struct{}

func (refusingWriter) Write(p []byte) (int, error) { return 0, os.ErrInvalid }
