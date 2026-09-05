package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Whatever the successor said, kept where somebody can read it.
//
// A handoff that fails says the new localcode did not begin serving, and
// until now that was the whole of what anybody got. The successor's own
// account of why went to its stderr, which is inherited from the process
// that started it — and in the desktop window that is a GUI-subsystem
// binary with no console, so it went nowhere at all. A window reported a
// timeout with no cause and no way to find one.
//
// So the output is teed: on through to the parent's stderr, where there
// is a terminal to show it, and into a buffer whose tail goes into the
// error message. It is also written to a file, because an error line in
// a transcript is one screenshot wide and a stack of Go panics is not.

// successorTail is how much of the successor's output is quoted in the
// error. Enough for a panic's first frames or a config error, short
// enough to read in a transcript.
const successorTail = 1200

type successorOutput struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// newSuccessorOutput opens the file the successor's output is copied to.
// A file that cannot be opened is not an error: the buffer still works,
// and the handoff is not the place to fail over logging.
func newSuccessorOutput() *successorOutput {
	o := &successorOutput{}
	rememberSuccessor(o)
	base, err := os.UserCacheDir()
	if err != nil {
		return o
	}
	dir := filepath.Join(base, "localcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return o
	}
	path := filepath.Join(dir, "handoff.log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return o
	}
	o.path, o.f = path, f
	return o
}

// streams are what the successor is given for its stdout and stderr, and
// they are always real *os.File values.
//
// That is not a detail. os/exec inherits a *os.File straight through to
// the child; hand it anything else and it builds an os.Pipe instead, with
// the read end and a copy goroutine living in THIS process. v0.101.0 did
// exactly that, to tee the output into the buffer below — and a handoff
// makes a chain, where generation 1 spawns generation 2 and then exits.
// Generation 2's stderr was a pipe generation 1 owned; when generation 1
// went, the read end went with it, and the next line generation 2 printed
// raised SIGPIPE. Go makes SIGPIPE on descriptor 1 or 2 fatal, so the new
// daemon was killed by its own first diagnostic.
//
// The file is the tee now: the child writes to it directly, and note()
// reads the tail back off it. Nothing in the parent stands between them.
func (o *successorOutput) streams() (stdout, stderr *os.File) {
	if o != nil && o.f != nil {
		return o.f, o.f
	}
	// No file to write to — this process's own, which are also real
	// files, so still never a pipe. In a window they refuse writes and
	// the output is lost, which is the situation that existed before
	// there was a file at all.
	return os.Stdout, os.Stderr
}

func (o *successorOutput) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.f != nil {
		_ = o.f.Close()
		o.f = nil
	}
}

// note is what the failure message appends: the tail of what the
// successor said, and where the rest of it is.
func (o *successorOutput) note() string {
	if o == nil {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	text := strings.TrimSpace(string(o.tail()))
	if len(text) > successorTail {
		text = "…" + text[len(text)-successorTail:]
	}
	switch {
	case text == "" && o.path == "":
		return " It printed nothing."
	case text == "":
		return fmt.Sprintf(" It printed nothing; anything it writes later goes to %s.", o.path)
	case o.path == "":
		return " It said:\n" + text
	}
	return fmt.Sprintf(" It said (full output in %s):\n%s", o.path, text)
}

// tail is the end of what the successor has written, read back off the
// file it is writing. Called with o.mu held.
//
// From the file rather than from a buffer this process keeps, because
// this process no longer sees the bytes: the child holds the descriptor.
func (o *successorOutput) tail() []byte {
	if o.path == "" {
		return nil
	}
	data, err := os.ReadFile(o.path)
	if err != nil {
		return nil
	}
	if len(data) > 64<<10 {
		data = data[len(data)-(64<<10):]
	}
	return data
}

// handoffLogPath is where a successor's output is kept, for the messages
// that point at it. Empty when there is no cache directory to write in.
func handoffLogPath() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return "the user cache directory"
	}
	return filepath.Join(base, "localcode", "handoff.log")
}

// noteSuccessorExit appends why the daemon behind the window stopped.
//
// Written rather than only returned, because by the time it happens
// there is nobody to return it to: the window is showing a page served
// through a proxy onto a process that has just gone, and the next thing
// that happens is a 502 with no context.
func noteSuccessorExit(pid int, err error) {
	path := handoffLogPath()
	if path == "" {
		return
	}
	f, ferr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	if err == nil {
		fmt.Fprintf(f, "\n[the localcode behind this window (pid %d) exited cleanly]\n", pid)
		return
	}
	fmt.Fprintf(f, "\n[the localcode behind this window (pid %d) exited: %v]\n", pid, err)
}

// The successor this window last started, and how it ended.
//
// A package-level slot because the place that has this is not the place
// that needs it. The output is teed by whoever spawned the process; the
// question it answers — "why is every request 502" — is asked much later,
// in a proxy handler that never saw the spawn and has no way back to it.
// Pointing that handler at a log file was one step short: the person
// reading the error is looking at a transcript, not a filesystem, and
// twice now the file has been asked for and not arrived. So the words go
// in the error.
//
// One slot, because a window runs one daemon at a time.
var lastSuccessor struct {
	mu     sync.Mutex
	out    *successorOutput
	pid    int
	exited bool
	err    error
}

func rememberSuccessor(o *successorOutput) {
	lastSuccessor.mu.Lock()
	defer lastSuccessor.mu.Unlock()
	lastSuccessor.out, lastSuccessor.pid, lastSuccessor.exited, lastSuccessor.err = o, 0, false, nil
}

func rememberSuccessorExit(pid int, err error) {
	lastSuccessor.mu.Lock()
	defer lastSuccessor.mu.Unlock()
	lastSuccessor.pid, lastSuccessor.exited, lastSuccessor.err = pid, true, err
}

// successorEpitaph is what the daemon behind this window said before it
// stopped, or "" while it is still running — in which case a 502 is about
// something other than the process being gone, and saying it died would
// be a guess dressed as a fact.
func successorEpitaph() string {
	lastSuccessor.mu.Lock()
	out, pid, exited, err := lastSuccessor.out, lastSuccessor.pid, lastSuccessor.exited, lastSuccessor.err
	lastSuccessor.mu.Unlock()
	if !exited {
		return ""
	}
	how := "exited cleanly"
	if err != nil {
		how = "exited with " + err.Error()
	}
	return fmt.Sprintf(" It (pid %d) %s.%s", pid, how, out.note())
}
