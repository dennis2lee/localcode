package main

import (
	"fmt"
	"io"
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
	buf  []byte
	path string
	f    *os.File
}

// newSuccessorOutput opens the file the successor's output is copied to.
// A file that cannot be opened is not an error: the buffer still works,
// and the handoff is not the place to fail over logging.
func newSuccessorOutput() *successorOutput {
	o := &successorOutput{}
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

func (o *successorOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf = append(o.buf, p...)
	if len(o.buf) > 64<<10 {
		o.buf = o.buf[len(o.buf)-(64<<10):]
	}
	if o.f != nil {
		_, _ = o.f.Write(p)
	}
	return len(p), nil
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
	text := strings.TrimSpace(string(o.buf))
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

// passthrough copies to w and swallows what w does with it.
//
// A GUI-subsystem process has no console, so its os.Stderr is a handle
// that refuses writes. io.MultiWriter gives up on the first error, which
// would mean the console's refusal also threw away the copy being kept —
// the one that exists precisely because there is no console.
type passthrough struct{ w io.Writer }

func (p passthrough) Write(b []byte) (int, error) {
	if p.w != nil {
		_, _ = p.w.Write(b)
	}
	return len(b), nil
}
