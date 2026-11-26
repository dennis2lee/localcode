package sshterm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// A terminal: one SSH connection and the interactive shell in it, kept
// for as long as its conversation exists, and shared by the model and
// the person.
//
// The emulator holds the screen. Everything that reads or changes it
// takes mu: the shell's output, the person's keys, the model's actions,
// a snapshot for the panel. The emulator's replies to the shell (a device
// attributes answer, the bytes of a key) go out through a pump that never
// waits on mu, into a bounded buffer a separate writer sends on, so a
// shell that stops reading cannot stall the screen.

// State is where a terminal's connection is.
type State string

const (
	StateConnecting State = "connecting"
	StateConnected  State = "connected"
	StateEnded      State = "ended"
)

// Opener is who opened a terminal.
type Opener string

const (
	ByModel  Opener = "model"
	ByPerson Opener = "person"
)

// Refusals of the model's actions, said to the model as they are.
var (
	ErrHeld         = errors.New("the person is holding this terminal (Hold in its panel): its screen and input are theirs until they release it; ask them in the conversation, or wait")
	ErrPersonActive = errors.New("the person typed in this terminal in the last few seconds; wait for them, or ask them in the conversation")
	ErrBusy         = errors.New("the shell has not reported the end of the last command: it is still running, or a nested shell (sudo -i, su, ssh, docker exec) is at its own prompt, which reports nothing. Use wait or read to see which; key ctrl+c stops a command; in a nested shell, use type with enter")
	ErrLineNotEmpty = errors.New("the person has typed part of a command line in this terminal and not run it; ask them to finish or clear it")
	ErrNotConnected = errors.New("this terminal is not connected")
	ErrChanged      = errors.New("the person typed in this terminal while the question was open, so what was approved may no longer fit; read the screen and decide again")
	ErrModelLine    = errors.New("the command line has text you typed (at this prompt, or ahead while the last command ran) and did not run; clear it first with key ctrl+c")
	ErrTypeahead    = errors.New("a command is running and nothing on the screen is waiting for input: a line sent now would be read by the shell as its next command once this one ends. Wait for it to end and use run, or answer it when it asks (read shows when)")
	ErrFullScreen   = errors.New("a full-screen program has the screen, or left the terminal on its alternate screen: run types nothing there. Use read to see what is on it, and key or type to work it or leave it")
	ErrUseRun       = errors.New("at the shell's prompt, commands go through run, which the rules for commands see: type takes text without a line end there, and key takes only line-editing keys (arrows, Home, End, Backspace, Delete, ctrl+a, ctrl+e, ctrl+b, ctrl+f, ctrl+k, ctrl+u, ctrl+w, ctrl+l, ctrl+c)")
)

// Waits a test shortens.
var (
	// personYield is how long after the person's last key the model's
	// input is refused.
	personYield = 10 * time.Second
	// lostAfter is how long a "running" shell has to be quiet at a line
	// that looks like a prompt for its integration to count as lost (a
	// nested shell, which prints no marks).
	lostAfter = 3 * time.Second
	// inputAfter is how long a command has to be quiet at a line that
	// looks like a question for run to return.
	inputAfter = 3 * time.Second
	// promptWait is how long a shell that has drawn nothing since a command
	// ended is waited on for its prompt before the cursor is taken as where
	// an empty prompt leaves it.
	promptWait = 2 * time.Second
	// interruptWait is how long after a Ctrl+C at the prompt, the model's
	// or the person's, a prompt that answers it is waited for.
	interruptWait = 10 * time.Second
)

const (
	// questionWait is how long a question waits for the person.
	questionWait = 5 * time.Minute
	// scrollbackLines is how much scrollback a terminal keeps.
	scrollbackLines = 5000
	// promptSettle is how long output has to be quiet, once the prompt has
	// been drawn after a command, for the cursor to be taken as where the
	// prompt leaves it.
	promptSettle = 150 * time.Millisecond
	// inputLimit bounds input queued for a shell that is not reading.
	inputLimit = 1 << 20
	// captureLimit bounds what run keeps of a command's output.
	captureLimit = 1 << 20

	MinCols, MaxCols = 40, 300
	MinRows, MaxRows = 10, 120
	DefaultCols      = 100
	DefaultRows      = 30
)

// Terminal is one connection and its shell.
type Terminal struct {
	ID     string
	Conv   string
	Spec   string
	Opener Opener
	// approved is the subject a permission question approved for this
	// connection, "" when none was asked.
	approved string
	// Epoch tells this terminal's scrollback numbering from another's
	// under the same id (a reconnect, a restarted daemon).
	Epoch string

	m      *Manager
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	state    State
	reason   string
	target   Target
	server   string
	shell    string
	question *Question
	answers  chan Answer
	qseq     int

	emu   *vt.Emulator
	cols  int
	rows  int
	sizes map[string][2]int // viewer → cols, rows
	title string

	scroll      []Line
	scrollFirst int

	marks       markScanner
	integrated  bool
	running     bool
	runStarted  time.Time
	promptX     int
	promptY     int
	promptKnown bool
	// markAt is when the shell last said a command ended, and markX and
	// markY where that left the cursor (not yet known while markPending: the
	// mark has been read and the output before it is still to be drawn).
	// The mark comes before the prompt, which a shell can take a while to
	// work out (a status line from git, a prompt command of the person's):
	// while the cursor is where the mark left it, no prompt has been drawn.
	markAt      time.Time
	markX       int
	markY       int
	markPending bool
	// markAbs is markY counted from the terminal's first line: a prompt
	// drawn on the row after the mark, with the screen scrolled by one,
	// can leave the cursor at markX and markY.
	markAbs int
	// A local terminal's screen clears (see clearEnds): how many there
	// have been, the row the screen started at after the last, the tail of
	// the output before, in which a clear's sequence may have begun, and
	// the prompt the last D mark carried.
	clears     int
	clearTop   int
	clearTail  []byte
	lastPrompt string
	// personTyped and modelTyped are the person, or the model, having
	// typed on the prompt's line since the prompt: what they typed may
	// still be there, unsent. promptLeft, promptRest and promptNext are the
	// prompt as it left the line (the cursor's row left of the cursor, what
	// is right of it, the row under it), so a line typed on and erased,
	// or a prompt redrawn elsewhere (Ctrl+L), reads as empty again.
	personTyped bool
	modelTyped  bool
	// typedAhead is input the model sent while a command ran and nothing
	// on the screen waited for it: the terminal keeps it, and it is on the
	// next prompt's line when the command ends.
	typedAhead bool
	// typedAfter and personAfter are the model, or the person, having
	// typed since the last key that drops the line or sends it: a Ctrl+C at
	// the prompt (either's), the person's Enter, a run's own line. A mark
	// says the shell is at a new prompt, and clears what was typed before
	// that key. What was typed after it is on the new prompt's line, however
	// late the mark arrives: the mark is not what clears it.
	typedAfter  bool
	personAfter bool
	// personAhead is the person having typed while a command ran, since
	// the last line end, or key that signals the command (Ctrl+C, and in a
	// remote shell's terminal Ctrl+\ and Ctrl+Z), which drop what was
	// typed before them. What the command did not read waits in the
	// terminal, and the shell draws it right after its next prompt as it
	// reads it: bash and zsh, measured, in the same write as the prompt.
	// What the command read (q to a pager) is not drawn. aheadText is the
	// text they typed then, edited as the terminal's own line editing
	// edits it. aheadMixed is anything else among it (a key, a Tab, a paste
	// with a line end kept in it), or text after a line
	// sent ahead (aheadSent): the screen cannot say what the shell made of
	// that, and the line at the next prompt is theirs until their Enter or
	// Ctrl+C. aheadJudge is the line at the new prompt being theirs for
	// text alone, until the screen is read for it (judgeAhead). A signal
	// to a command drops what waits in the terminal, and not a line the
	// shell keeps (text drawn on it, the line Ctrl+O brings back): the
	// line stays theirs.
	personAhead bool
	aheadText   string
	aheadMixed  bool
	aheadSent   bool
	aheadJudge  bool
	// aheadPasted is a bracketed paste among it, its marks sent: read as
	// text only where the shell has bracketed paste on at its prompt. A
	// shell that has not (bash 3.2) reads the marks as keys and draws some
	// of them.
	aheadPasted bool
	// interrupts are the Ctrl+C presses at the prompt that no prompt has
	// answered yet, and modelInterrupt when the model last pressed one,
	// until a prompt: a run waits for the answer, and says when none came.
	interrupts     interrupts
	modelInterrupt time.Time
	promptLeft     string
	promptRest     string
	promptNext     string
	personGen      uint64
	integrating    bool
	lastPerson     time.Time
	hold           bool
	lastOutput     time.Time
	// outputs counts the reads of the shell's output, so a run can tell
	// output that came after it typed its command from output before,
	// whatever the clock's resolution.
	outputs   uint64
	settle    *time.Timer
	run       *runWait
	endedAt   time.Time
	endedFlag atomic.Bool
	// summary is the terminal's state for readers that must not wait on
	// mu (the session list, the settings window): written on every change.
	summary atomic.Pointer[Summary]

	changed chan struct{}
	version uint64

	client  *ssh.Client
	session *ssh.Session
	closers []io.Closer

	inMu    sync.Mutex
	inBuf   []byte
	inReady chan struct{}
	inLost  bool

	// resized wakes the sender of window-change requests, which sends
	// the size current when it runs: requests sent one per goroutine
	// arrived out of order.
	resized chan struct{}
}

// interrupts counts Ctrl+C presses at the prompt that no prompt has
// answered yet. Each press the shell takes brings one prompt, so with two
// on their way the first prompt is not the last word: a run waits for the
// rest, and no prompt is recorded as where the line starts. What is on
// the line is not decided by the count (a shell can answer two presses
// with one prompt, or none), and a press is waited on for interruptWait at
// most.
type interrupts struct {
	n    int
	last time.Time
}

// press notes one. Those before it that no prompt answered in time were
// not interrupts, or will not be waited for: the wait is for this one.
func (i *interrupts) press() {
	if i.pending() == 0 {
		i.n = 0
	}
	i.n++
	i.last = time.Now()
}

// pending is how long a prompt may still be on its way for a press: zero
// when none is.
func (i *interrupts) pending() time.Duration {
	if i.n == 0 {
		return 0
	}
	return max(0, interruptWait-time.Since(i.last))
}

// answered takes a prompt as the answer to one press.
func (i *interrupts) answered() {
	if i.n > 0 {
		i.n--
	}
}

// runWait is the model's run, waiting for its command to finish.
type runWait struct {
	out       capture
	started   bool // a C mark said where the output starts
	done      chan struct{}
	status    string
	ended     bool // a D mark ended it
	alt       bool
	held      bool   // the person held the terminal: what came is theirs
	sentAt    uint64 // outputs when the command was typed
	personGen uint64
	// local is a run in a local terminal, whose output is read off the
	// screen rather than from the stream (see localOutput), from the row
	// after the command's echo. startAbs and startX are where the
	// command was typed (startAbs counting every line the terminal has
	// had), cols the screen's width then.
	local    bool
	startAbs int
	startX   int
	cols     int
	line     string // the command, as typed
	clears   int    // the terminal's clears when it was typed
	// prompt is the prompt the D mark said follows the command, "" when
	// it said none.
	prompt string
	// output is a local run's output, read off the screen as the run
	// ends, before a size held back for it is applied: a smaller screen
	// cuts off what is wider than it.
	output   string
	captured bool
}

func newNonce() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newTerminal(m *Manager, conv, id, spec string, by Opener) *Terminal {
	ctx, cancel := context.WithCancel(context.Background())
	t := &Terminal{
		ID: id, Conv: conv, Spec: spec, Opener: by, Epoch: newNonce(),
		m: m, ctx: ctx, cancel: cancel,
		state:   StateConnecting,
		answers: make(chan Answer, 1),
		cols:    DefaultCols, rows: DefaultRows,
		sizes:   map[string][2]int{},
		changed: make(chan struct{}),
		inReady: make(chan struct{}, 1),
		resized: make(chan struct{}, 1),
	}
	t.marks.nonce = newNonce()
	t.summary.Store(&Summary{Target: spec, State: StateConnecting})
	t.emu = vt.NewEmulator(t.cols, t.rows)
	t.emu.SetScrollbackSize(scrollbackLines)
	t.emu.SetCallbacks(vt.Callbacks{
		Title: func(s string) {
			if r := []rune(s); len(r) > 200 {
				s = string(r[:200])
			}
			t.title = s
		},
	})
	go t.pump()
	return t
}

// notify tells watchers the terminal changed. Called with mu held.
func (t *Terminal) notify() {
	target := t.Spec
	if t.target.Host != "" {
		target = t.target.String()
	}
	t.summary.Store(&Summary{Target: target, State: t.state, Asking: t.question != nil})
	t.version++
	close(t.changed)
	t.changed = make(chan struct{})
	t.m.touched(t.Conv)
}

// Summary is a terminal's state as read without its lock.
type Summary struct {
	Target string
	State  State
	Asking bool
}

// Summarize is the terminal's state as of its last change. It takes no
// lock: a terminal busy drawing does not hold up the session list.
func (t *Terminal) Summarize() Summary {
	if s := t.summary.Load(); s != nil {
		return *s
	}
	return Summary{Target: t.Spec, State: StateConnecting}
}

// Changed is closed the next time the terminal changes.
func (t *Terminal) Changed() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.changed
}

// Version counts the terminal's changes.
func (t *Terminal) Version() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.version
}

// pump moves the emulator's input (keys, replies to queries) to the
// writer's buffer. It never takes mu, and never waits on the shell.
func (t *Terminal) pump() {
	buf := make([]byte, 4096)
	for {
		n, err := t.emu.Read(buf)
		if n > 0 {
			t.inMu.Lock()
			if len(t.inBuf)+n > inputLimit {
				t.inLost = true
			} else {
				t.inBuf = append(t.inBuf, buf[:n]...)
			}
			t.inMu.Unlock()
			select {
			case t.inReady <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

// writer sends queued input to the shell's stdin.
func (t *Terminal) writer(stdin io.Writer) {
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.inReady:
		}
		t.inMu.Lock()
		b := t.inBuf
		t.inBuf = nil
		t.inMu.Unlock()
		if len(b) == 0 {
			continue
		}
		if _, err := stdin.Write(b); err != nil {
			return
		}
	}
}

// reader feeds the shell's output to the screen and the tracker.
func (t *Terminal) reader(r io.Reader) {
	// A panic here would end the program, every conversation with it.
	defer func() {
		if rec := recover(); rec != nil {
			go t.end(fmt.Sprintf("the terminal stopped: %v", rec))
		}
	}()
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			t.output(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (t *Terminal) output(p []byte) {
	if err := t.feed(p); err != nil {
		t.end("the remote sent output the terminal could not draw (" + err.Error() + ")")
	}
}

// screenChunk is how much output is drawn under one hold of the lock:
// the panel, the model and the session list wait for it between chunks.
const screenChunk = 1024

// feed takes a read of the shell's output: the marks and the waiting
// run's capture first, then the screen, a chunk at a time. A panic in the
// emulator is returned as an error, so a remote that finds one ends its
// own terminal rather than the program.
func (t *Terminal) feed(p []byte) error {
	ok, mark := t.track(p)
	if !ok {
		return nil
	}
	if mark >= 0 {
		// Where the end of the command leaves the cursor: the prompt is
		// what moves it from there.
		if err := t.show(p[:mark]); err != nil {
			return err
		}
		t.mu.Lock()
		t.markHere()
		t.mu.Unlock()
		p = p[mark:]
	}
	if err := t.show(p); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if w := t.run; w != nil && t.emu.IsAltScreen() {
		w.alt = true
		t.finishRun(w)
	}
	t.lastOutput = time.Now()
	if !t.running {
		t.armSettle(promptSettle)
	}
	t.notify()
	return nil
}

// markHere records the cursor as the end of a command leaves it. Called
// with mu held.
func (t *Terminal) markHere() {
	pos := t.emu.CursorPosition()
	t.markX, t.markY, t.markPending = pos.X, pos.Y, false
	t.markAbs = t.absRow(pos.Y)
}

// atMark reports the cursor where the end of the last command left it:
// nothing has been drawn since. Called with mu held.
func (t *Terminal) atMark() bool {
	pos := t.emu.CursorPosition()
	return !t.markPending && pos.X == t.markX && pos.Y == t.markY && t.absRow(pos.Y) == t.markAbs
}

// show draws output on the screen, a chunk at a time.
func (t *Terminal) show(p []byte) error {
	// Clearing the screen with its scrollback (ED 3, which clear sends)
	// clears the terminal's own scrollback too, where it comes in the
	// output: what was cleared is not there for the model to read, and
	// what came after it is.
	for len(p) > 0 {
		upto := len(p)
		clears := false
		if i := bytes.Index(p, ed3); i >= 0 {
			upto, clears = i+len(ed3), true
		}
		part := p[:upto]
		for len(part) > 0 {
			n := min(len(part), screenChunk)
			if err := t.draw(part[:n]); err != nil {
				return err
			}
			part = part[n:]
		}
		if clears {
			t.mu.Lock()
			t.scrollFirst += len(t.scroll)
			t.scroll = nil
			t.mu.Unlock()
		}
		p = p[upto:]
	}
	return nil
}

var ed3 = []byte("\x1b[3J")

// track reads the marks in a read of the shell's output and feeds the
// waiting run's capture. It reports false for a terminal that has ended,
// and where in the read the last end of a command is marked, -1 when none
// is.
func (t *Terminal) track(p []byte) (ok bool, mark int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == StateEnded {
		return false, -1
	}
	mark = -1
	t.outputs++
	from := 0
	for _, ev := range t.marks.feed(p) {
		w := t.run
		switch ev.kind {
		case 'C':
			if w != nil {
				// The output starts here: what came before was the
				// shell echoing the command.
				w.out = capture{max: captureLimit}
				w.started = true
			}
			t.running = true
		case 'D':
			if w != nil {
				w.out.write(p[from:ev.end])
				w.status, w.ended = ev.status, true
				if w.local {
					// Finished once the screen shows the prompt the mark
					// names (promptDrawn): a pseudo console can send the
					// mark ahead of the output before it.
					w.prompt = ev.prompt
				} else {
					t.finishRun(w)
				}
			}
			t.running = false
			t.promptKnown = false
			t.markAt, t.markPending = time.Now(), true
			t.lastPrompt = ev.prompt
			mark = ev.end
			t.integrated = true
			// The shell is at a new prompt. What was typed before the key
			// that brought it went with the old line. What was typed since
			// that key is on this one, and so is what the model typed ahead
			// while the command ran.
			// What the person typed while the command ran is on this line
			// if the command did not read it. Text alone, the screen says
			// which once the prompt is drawn (judgeAhead). Anything else, or
			// text the last prompt was never read for (a command the shell
			// started from a line sent ahead came between), and the line is
			// theirs until their Enter or Ctrl+C at a prompt.
			if t.personAhead && t.aheadMixed || t.aheadJudge {
				t.personAfter = true
			}
			t.aheadJudge = t.personAhead && !t.personAfter
			t.personTyped = t.personAfter || t.aheadJudge
			t.modelTyped = t.typedAhead || t.typedAfter
			t.typedAhead = false
			t.personAhead, t.aheadSent = false, false
			t.interrupts.answered()
			t.modelInterrupt = time.Time{}
		}
		from = ev.end
	}
	if w := t.run; w != nil && from < len(p) {
		w.out.write(p[from:])
	}
	return true, mark
}

// draw writes output to the emulator and moves what scrolled off into the
// scrollback, under the lock. A panic in the emulator is returned as an
// error, with the lock released.
func (t *Terminal) draw(b []byte) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	if t.isLocal() {
		// Each clear is written on its own, and where the screen starts
		// after it is kept: what a run prints after a clear starts there
		// (localOutput), and the prompt after it is drawn there.
		ends := clearEnds(t.clearTail, b)
		t.clearTail = clearTailOf(t.clearTail, b)
		done := 0
		for _, end := range ends {
			_, _ = t.emu.Write(b[done:end])
			t.drainScrollback()
			// A clear of the alternate screen (a full-screen program's)
			// leaves the main screen as it was.
			if !t.emu.IsAltScreen() {
				t.clears++
				t.clearTop = t.absRow(0)
			}
			done = end
		}
		b = b[done:]
	}
	_, _ = t.emu.Write(b)
	t.drainScrollback()
	return nil
}

// finishRun ends the model's wait. Called with mu held.
func (t *Terminal) finishRun(w *runWait) {
	if t.run == w {
		t.run = nil
		t.captureRun(w)
		close(w.done)
		t.applySize()
	}
}

// captureRun reads a local run's output off the screen while it is still
// the size the run saw. Called with mu held, before applySize.
func (t *Terminal) captureRun(w *runWait) {
	if w.local && !w.captured {
		w.output, w.captured = t.localOutput(w), true
	}
}

// armSettle has the prompt looked for after d with no output: where it
// left the cursor, once it has been drawn and output has been quiet for
// promptSettle. Called with mu held.
func (t *Terminal) armSettle(d time.Duration) {
	if t.settle != nil {
		t.settle.Stop()
	}
	t.settle = time.AfterFunc(d, t.settled)
}

// settled records where the prompt left the cursor, when the shell is at
// one it has finished drawing.
func (t *Terminal) settled() {
	defer func() {
		if r := recover(); r != nil {
			go t.end(fmt.Sprintf("the terminal stopped: %v", r))
		}
	}()
	t.mu.Lock()
	defer t.mu.Unlock()
	// A line that is the person's only for what they typed while the
	// command before it ran is read off the screen when the prompt is
	// drawn: the prompt is recorded, or not, by what of it is there.
	judge := t.personTyped && t.aheadJudge
	if t.state == StateEnded || t.running || (t.personTyped && !judge) || t.modelTyped || !t.integrated || time.Since(t.lastOutput) < promptSettle {
		return
	}
	// With a Ctrl+C still to be answered, this prompt is not the one the
	// line will be on: what is typed now shows up after the next.
	if left := t.interrupts.pending(); left > 0 {
		t.armSettle(left)
		return
	}
	// Quiet after the end of a command is not a prompt yet. The mark comes
	// first and the prompt after whatever the shell does to work it out,
	// which was taken for the prompt when it took longer than promptSettle:
	// a line the person then typed on and erased never read as empty again,
	// and a run typed its command before the prompt it was meant for. A
	// prompt moves the cursor. One that does not (an empty one) is taken as
	// drawn after promptWait.
	if left := promptWait - time.Since(t.markAt); left > 0 && (t.markPending || t.atMark()) {
		t.armSettle(left)
		return
	}
	if judge {
		t.aheadJudge = false
		if !t.judgeAhead() {
			// The screen cannot say: the line is theirs until their Enter
			// or Ctrl+C.
			t.personAfter = true
			t.notify()
			return
		}
	} else {
		t.recordPrompt(t.emu.CursorPosition().X)
	}
	if w := t.run; w != nil && w.local && w.ended && t.promptDrawn(w) {
		t.finishRun(w)
	}
	t.notify()
}

// recordPrompt takes the prompt as ending at column x of the cursor's
// row, with the person's text between x and the cursor: the line reads as
// empty with the cursor at x, the row the same left of it, blank up to
// where the cursor is now, and what is right of that (a right prompt) and
// under the row as it is now. Called with mu held.
func (t *Terminal) recordPrompt(x int) {
	pos := t.emu.CursorPosition()
	_, rest, next := t.aroundCursor()
	rest = strings.TrimRight(strings.Repeat(" ", pos.X-x)+rest, " ")
	t.promptX, t.promptY, t.promptKnown = x, pos.Y, true
	t.promptLeft, t.promptRest, t.promptNext = t.cellText(pos.Y, 0, x), rest, next
}

// judgeAhead records the prompt after a command the person typed text
// during, where the screen says it ends, and reports whether it could.
// What the shell drew of the text is the end of what was typed, right
// before the cursor, and the prompt ends where that starts
// (aheadShownFrom). None of it there, or only ASCII spaces (which change
// nothing a command line runs) not reaching the left edge, and the command
// read it. Matched to the left edge, the text can go on in the row above:
// it is theirs, and no prompt is recorded, so the last prompt's end stays
// for the next reading. It cannot say with the
// cursor where the mark left it (no prompt drawn, however long that has
// been), at the left edge (nothing left of the cursor to read: a line that
// filled the row above, or a prompt that leaves nothing on its row), in
// the last column (the cursor sits there on the last character written or
// over a blank cell, and the screen does not say which), or after a
// bracketed paste where the shell has bracketed paste off at its prompt. Called with mu held, where a
// prompt would be recorded.
func (t *Terminal) judgeAhead() bool {
	pos := t.emu.CursorPosition()
	if t.atMark() || pos.X == 0 || pos.X == t.emu.Width()-1 || t.aheadPasted && !t.emu.BracketedPaste() {
		return false
	}
	from, spaces := t.aheadShownFrom(pos.X, pos.Y)
	if from == pos.X || (spaces && from > 0) {
		// None of it there. That is the command having read it only where
		// the row ends as the last prompt did (promptEnd): otherwise
		// something else was drawn since the mark (a background job's
		// output), over the text or before the prompt, and the screen
		// cannot say.
		end := promptEnd(t.promptLeft)
		if end == "" || !strings.HasSuffix(strings.TrimRight(t.cellText(pos.Y, 0, pos.X), " "), end) {
			return false
		}
		t.recordPrompt(pos.X)
	} else if from == 0 {
		// To the left edge: it can go on in the row above, and nothing
		// left of it on the row is the prompt. Theirs, with no prompt
		// recorded, and the last one's end kept for the next reading.
		return false
	} else {
		// Theirs on the line, as if typed at it: until their Enter or
		// Ctrl+C, past a command the shell starts on its own too.
		t.recordPrompt(from)
		t.personAfter = true
	}
	return true
}

// promptEnd is the last two characters of s before its ASCII spaces, or
// as many as there are: what a prompt ends with stays the same from one
// prompt to the next where a clock or a directory at its start changes,
// and is unlike output (zsh's prompt ends in " %", a progress line in
// "0%").
func promptEnd(s string) string {
	r := []rune(strings.TrimRight(s, " "))
	return string(r[max(len(r)-2, 0):])
}

// aheadShownFrom is the column where the end of aheadText starts, as much
// of it as is drawn right before column x of row y, or x when none is,
// and whether that is ASCII spaces alone. The longest end that matches: a
// shorter one would take part of the text for the prompt, and the line
// would read as empty with that part still on it. A wide character is one
// cell's content, and the cell after it is skipped. Called with mu held.
func (t *Terminal) aheadShownFrom(x, y int) (from int, spaces bool) {
	from, spaces = x, true
	end := ""
	for c := x - 1; c >= 0; c-- {
		cell := t.emu.CellAt(c, y)
		if cell == nil {
			break
		}
		if cell.Content == "" && cell.Width == 0 {
			continue // the second half of a wide character
		}
		s := cell.Content
		if s == "" {
			s = " "
		}
		end = s + end
		if !strings.HasSuffix(t.aheadText, end) {
			break
		}
		from, spaces = c, spaces && s == " "
	}
	return from, spaces
}

// drainScrollback moves the lines the emulator scrolled off the top into
// the terminal's own scrollback, so every line has a number a viewer can
// ask from. Called with mu held.
func (t *Terminal) drainScrollback() {
	n := t.emu.ScrollbackLen()
	if n == 0 {
		return
	}
	width := t.emu.Width()
	for y := 0; y < n; y++ {
		yy := y
		t.scroll = append(t.scroll, lineOf(width, func(x int) *uv.Cell { return t.emu.ScrollbackCellAt(x, yy) }))
	}
	t.emu.ClearScrollback()
	if over := len(t.scroll) - scrollbackLines; over > 0 {
		t.scroll = append(t.scroll[:0:0], t.scroll[over:]...)
		t.scrollFirst += over
	}
}

// aroundCursor is the cursor's row left of the cursor, what is right of
// it, and every row under it to the bottom of the screen, as the cells
// hold them (trailing blanks trimmed, nothing else). Called with mu held.
func (t *Terminal) aroundCursor() (left, rest, below string) {
	pos := t.emu.CursorPosition()
	w, h := t.emu.Width(), t.emu.Height()
	var rows []string
	for y := pos.Y + 1; y < h; y++ {
		rows = append(rows, strings.TrimRight(t.cellText(y, 0, w), " "))
	}
	return t.cellText(pos.Y, 0, pos.X), strings.TrimRight(t.cellText(pos.Y, pos.X, w), " "), strings.TrimRight(strings.Join(rows, "\n"), "\n")
}

// cellText is the text of screen row y from column from up to to.
// Called with mu held.
func (t *Terminal) cellText(y, from, to int) string {
	var b strings.Builder
	for x := from; x < to && x < t.emu.Width(); x++ {
		if c := t.emu.CellAt(x, y); c != nil {
			if c.Content == "" && c.Width == 0 {
				continue // the second half of a wide character
			}
			if c.Content == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(c.Content)
			}
		}
	}
	return b.String()
}

// cutsLine reports a screen of cols by rows cutting into the line being
// typed: the cursor is outside it, or something on the cursor's row or a
// row under it is. Called with mu held.
func (t *Terminal) cutsLine(cols, rows int) bool {
	pos := t.emu.CursorPosition()
	if pos.X >= cols || pos.Y >= rows {
		return true
	}
	w, h := t.emu.Width(), t.emu.Height()
	for y := pos.Y; y < h; y++ {
		from := cols
		if y >= rows {
			from = 0
		}
		for x := from; x < w; x++ {
			if c := t.emu.CellAt(x, y); c != nil && c.Content != "" && c.Content != " " {
				return true
			}
		}
	}
	return false
}

// cursorLine is the text of the line the cursor is on. Called with mu
// held.
func (t *Terminal) cursorLine() string {
	pos := t.emu.CursorPosition()
	return lineOf(t.emu.Width(), func(x int) *uv.Cell { return t.emu.CellAt(x, pos.Y) }).Text()
}

// looksLikePrompt is a line ending the way shell prompts end.
func looksLikePrompt(line string) bool {
	line = strings.TrimRight(line, " ")
	for _, end := range []string{"$", "#", "%", ">", "❯", "➜", "»", "λ"} {
		if strings.HasSuffix(line, end) {
			return true
		}
	}
	return false
}

// lostIntegration reports a shell said to be running a command that has
// been quiet at what looks like a prompt: a nested shell (sudo -i, ssh,
// docker exec) prints no marks. Called with mu held.
func (t *Terminal) lostIntegration() bool {
	return t.integrated && t.running && time.Since(t.lastOutput) >= lostAfter && looksLikePrompt(t.cursorLine())
}

// ask puts a question to the person and waits for the answer.
func (t *Terminal) ask(ctx context.Context, q Question) (Answer, error) {
	t.mu.Lock()
	t.qseq++
	q.ID = t.qseq
	t.question = &q
	select {
	case <-t.answers:
	default:
	}
	t.notify()
	t.mu.Unlock()
	t.m.asking(t)
	defer func() {
		t.mu.Lock()
		if t.question != nil && t.question.ID == q.ID {
			t.question = nil
			t.notify()
		}
		t.mu.Unlock()
		t.m.asked(t)
	}()
	timer := time.NewTimer(questionWait)
	defer timer.Stop()
	select {
	case a := <-t.answers:
		if !a.Accept {
			return a, errNotAnswered
		}
		return a, nil
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	case <-t.ctx.Done():
		return Answer{}, errors.New("the terminal was closed")
	case <-timer.C:
		return Answer{}, errors.New("nobody answered in the SSH panel")
	}
}

// Answer answers the question with id. Only the panel's route calls
// this; no tool action does. A host key is accepted only with the
// fingerprint the question showed.
func (t *Terminal) Answer(id int, a Answer, fingerprint string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	q := t.question
	if q == nil || q.ID != id {
		return errors.New("that question is no longer asked")
	}
	if a.Accept {
		switch q.Kind {
		case AskHostKey:
			if q.HostKey == nil || fingerprint != q.HostKey.Fingerprint {
				return errors.New("the fingerprint does not match the one asked about")
			}
		case AskPrompts:
			if len(a.Values) != len(q.Prompts) {
				return fmt.Errorf("want %d answers", len(q.Prompts))
			}
		default:
			if len(a.Values) != 1 {
				return errors.New("want one answer")
			}
		}
	}
	select {
	case t.answers <- a:
	default:
		return errors.New("that question has been answered")
	}
	t.question = nil
	t.notify()
	return nil
}

// connect makes the connection and starts the shell, in its own
// goroutine: the terminal is in the panel from the start.
func (t *Terminal) connect(ctx context.Context) {
	if t.isLocal() {
		if err := t.startLocal(ctx); err != nil {
			t.end("could not start: " + err.Error())
		}
		return
	}
	if err := t.dialAndStart(ctx); err != nil {
		t.end("could not connect: " + err.Error())
		return
	}
	t.integrate()
}

func (t *Terminal) dialAndStart(ctx context.Context) error {
	spec, err := ParseSpec(t.Spec)
	if err != nil {
		return err
	}
	o := t.m.opts
	cfg, err := loadConfig(o.Home, o.SystemDir)
	if err != nil {
		return fmt.Errorf("reading ~/.ssh/config: %w", err)
	}
	target := cfg.resolve(spec, o.LocalUser, o.Home)
	t.mu.Lock()
	t.target = target
	t.notify()
	t.mu.Unlock()
	if target.ProxyCommand != "" {
		return fmt.Errorf("~/.ssh/config gives %s a ProxyCommand; localcode does not run local commands for SSH. Use ProxyJump instead, or connect to a host it can reach directly", spec.Host)
	}
	var hops []Target
	for _, hop := range target.ProxyJump {
		hs, err := ParseSpec(hop)
		if err != nil {
			return fmt.Errorf("ProxyJump %q: %w", hop, err)
		}
		hops = append(hops, cfg.resolve(hs, o.LocalUser, o.Home))
	}
	if o.CheckTarget != nil {
		if err := o.CheckTarget(t.Conv, t.Opener, Route{Asked: spec, Target: target, Hops: hops}, t.approved); err != nil {
			return err
		}
	}
	var via *ssh.Client
	for _, ht := range hops {
		c, err := t.handshake(ctx, via, ht)
		if err != nil {
			return fmt.Errorf("jump host %s: %w", ht, err)
		}
		t.mu.Lock()
		if t.state == StateEnded {
			t.mu.Unlock()
			c.Close()
			return errors.New("closed while connecting")
		}
		t.closers = append(t.closers, c)
		t.mu.Unlock()
		via = c
	}
	client, err := t.handshake(ctx, via, target)
	if err != nil {
		return err
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return err
	}
	for _, kv := range o.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			_ = sess.Setenv(k, v)
		}
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	t.mu.Lock()
	cols, rows := t.cols, t.rows
	t.mu.Unlock()
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		client.Close()
		return fmt.Errorf("the server refused a terminal: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		client.Close()
		return err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		client.Close()
		return err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		client.Close()
		return err
	}
	if err := sess.Shell(); err != nil {
		client.Close()
		return fmt.Errorf("the server refused a shell: %w", err)
	}
	t.mu.Lock()
	if t.state == StateEnded {
		t.mu.Unlock()
		client.Close()
		return errors.New("closed while connecting")
	}
	t.client, t.session = client, sess
	t.server = string(client.ServerVersion())
	t.state = StateConnected
	t.notify()
	t.mu.Unlock()

	go t.writer(stdin)
	go t.sizer(sess)
	go t.reader(stdout)
	go t.reader(stderr)
	go t.keepalive(client, target)
	go func() {
		err := sess.Wait()
		reason := "the shell exited"
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			reason = fmt.Sprintf("the shell exited with status %d", exit.ExitStatus())
		} else if err != nil {
			reason = "the connection ended: " + err.Error()
		}
		t.end(reason)
	}()
	return nil
}

// Route is where a connection goes: what was asked for, where it
// resolved to, and the jump hosts on the way.
type Route struct {
	Asked  Spec
	Target Target
	Hops   []Target
}

// watchdog closes a connection whose handshake takes longer than d,
// counting only the time nobody is being asked something.
type watchdog struct {
	mu    sync.Mutex
	timer *time.Timer
	d     time.Duration
	done  bool
}

func newWatchdog(d time.Duration, fire func()) *watchdog {
	w := &watchdog{d: d}
	w.timer = time.AfterFunc(d, fire)
	return w
}

func (w *watchdog) pause() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		w.timer.Stop()
	}
}

func (w *watchdog) resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		w.timer.Reset(w.d)
	}
}

func (w *watchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	w.timer.Stop()
}

// handshake connects to target, through via when it is not nil.
func (t *Terminal) handshake(ctx context.Context, via *ssh.Client, target Target) (*ssh.Client, error) {
	o := t.m.opts
	if via == nil && o.Permits != nil && !o.Permits(target.Host) {
		return nil, fmt.Errorf("network.egress does not allow %s", target.Host)
	}
	dctx, cancel := context.WithTimeout(ctx, target.ConnectTimeout)
	defer cancel()
	var conn net.Conn
	var err error
	if via != nil {
		conn, err = via.DialContext(dctx, "tcp", target.Address())
	} else {
		conn, err = t.m.dial(dctx, "tcp", target.Address())
	}
	if err != nil {
		return nil, err
	}
	var timedOut bool
	var tmu sync.Mutex
	dog := newWatchdog(target.ConnectTimeout, func() {
		tmu.Lock()
		timedOut = true
		tmu.Unlock()
		conn.Close()
	})
	defer dog.stop()
	// The handshake has no context of its own: closing the connection is
	// how a stopped turn or a closed terminal ends it.
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-t.ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()
	defer close(stop)
	ask := func(qctx context.Context, q Question) (Answer, error) {
		dog.pause()
		defer dog.resume()
		return t.ask(qctx, q)
	}
	hostAsk := func(qctx context.Context, hq HostKeyQuestion) (bool, error) {
		a, err := ask(qctx, Question{Kind: AskHostKey, For: target.String(), HostKey: &hq})
		if err != nil {
			if errors.Is(err, errNotAnswered) {
				return false, nil
			}
			return false, err
		}
		return a.Accept, nil
	}
	agentPath := target.IdentityAgent
	if agentPath == "" || agentPath == "SSH_AUTH_SOCK" {
		agentPath = o.Agent
	}
	ag, closeAgent := openAgent(agentPath)
	defer closeAgent()
	keys := newHostKeys(o, target)
	config := &ssh.ClientConfig{
		User:              target.User,
		Auth:              authMethods(ctx, target, o.Home, ag, ask),
		HostKeyCallback:   keys.callback(ctx, target.KnownName(), hostAsk),
		HostKeyAlgorithms: keys.algorithms(target.KnownName()),
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, target.Address(), config)
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		tmu.Lock()
		to := timedOut
		tmu.Unlock()
		if to {
			return nil, fmt.Errorf("%s did not finish the SSH handshake within %s", target.Address(), target.ConnectTimeout)
		}
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// keepalive asks the server for a reply every interval, and ends the
// terminal after count intervals with none.
func (t *Terminal) keepalive(c *ssh.Client, target Target) {
	tick := time.NewTicker(target.AliveInterval)
	defer tick.Stop()
	done := make(chan error, 1)
	inflight := false
	missed := 0
	for {
		select {
		case <-t.ctx.Done():
			return
		case err := <-done:
			inflight = false
			if err == nil {
				missed = 0
			}
		case <-tick.C:
			if inflight {
				missed++
				if missed >= target.AliveCountMax {
					t.end(fmt.Sprintf("the server did not answer %d keepalives, %s apart", missed, target.AliveInterval))
					return
				}
				continue
			}
			inflight = true
			go func() {
				_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
		}
	}
}

// end closes the connection, once, and keeps the screen.
func (t *Terminal) end(reason string) {
	t.mu.Lock()
	if t.state == StateEnded {
		t.mu.Unlock()
		return
	}
	t.state = StateEnded
	t.reason = reason
	t.endedAt = time.Now()
	t.endedFlag.Store(true)
	t.running = false
	if w := t.run; w != nil {
		t.finishRun(w)
	}
	if t.settle != nil {
		t.settle.Stop()
	}
	client, closers := t.client, t.closers
	t.notify()
	t.mu.Unlock()
	t.cancel()
	if client != nil {
		client.Close()
	}
	for i := len(closers) - 1; i >= 0; i-- {
		closers[i].Close()
	}
	if pw, ok := t.emu.InputPipe().(*io.PipeWriter); ok {
		pw.Close()
	}
	t.m.ended(t)
}

// Info is a terminal's state, for the panel and the model.
type Info struct {
	ID          string    `json:"id"`
	Epoch       string    `json:"epoch"`
	Spec        string    `json:"spec"`
	Target      string    `json:"target,omitempty"`
	State       State     `json:"state"`
	Reason      string    `json:"reason,omitempty"`
	Server      string    `json:"server,omitempty"`
	Shell       string    `json:"shell,omitempty"`
	Integrated  bool      `json:"integrated"`
	Running     bool      `json:"running"`
	Alt         bool      `json:"alt"`
	Held        bool      `json:"held"`
	Opener      Opener    `json:"opener"`
	Question    *Question `json:"question,omitempty"`
	Title       string    `json:"title,omitempty"`
	Cols        int       `json:"cols"`
	Rows        int       `json:"rows"`
	InputLost   bool      `json:"input_lost,omitempty"`
	ScrollFirst int       `json:"scroll_first"`
	ScrollNext  int       `json:"scroll_next"`
}

// Info reads the terminal's state.
func (t *Terminal) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.infoLocked()
}

func (t *Terminal) infoLocked() Info {
	in := Info{
		ID: t.ID, Epoch: t.Epoch, Spec: t.Spec, State: t.state, Reason: t.reason, Server: t.server, Shell: t.shell,
		Integrated: t.integrated, Running: t.running, Alt: t.emu.IsAltScreen(), Held: t.hold,
		Opener: t.Opener, Title: t.title, Cols: t.cols, Rows: t.rows,
		ScrollFirst: t.scrollFirst, ScrollNext: t.scrollFirst + len(t.scroll),
	}
	if t.target.Host != "" {
		in.Target = t.target.String()
	}
	if t.question != nil {
		q := *t.question
		in.Question = &q
	}
	t.inMu.Lock()
	in.InputLost = t.inLost
	t.inMu.Unlock()
	return in
}

// Target is where the terminal connected, once it is known.
func (t *Terminal) Target() Target {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.target
}

// Snapshot is the screen now.
func (t *Terminal) Snapshot() Screen {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.screenLocked()
}

func (t *Terminal) screenLocked() Screen {
	w, h := t.emu.Width(), t.emu.Height()
	s := Screen{Cols: w, Rows: h, Alt: t.emu.IsAltScreen(), Title: t.title}
	s.Lines = make([]Line, h)
	for y := 0; y < h; y++ {
		yy := y
		s.Lines[y] = lineOf(w, func(x int) *uv.Cell { return t.emu.CellAt(x, yy) })
	}
	pos := t.emu.CursorPosition()
	s.CursorX, s.CursorY, s.Cursor = pos.X, pos.Y, !t.emu.CursorHidden()
	return s
}

// Scrollback returns up to max scrollback lines from number from on,
// and the number of the first line returned (later than from when those
// lines were dropped, or when max cut the front).
func (t *Terminal) Scrollback(from, max int) ([]Line, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if from < t.scrollFirst {
		from = t.scrollFirst
	}
	i := from - t.scrollFirst
	if i >= len(t.scroll) {
		return nil, t.scrollFirst + len(t.scroll)
	}
	lines := t.scroll[i:]
	if max > 0 && len(lines) > max {
		from += len(lines) - max
		lines = lines[len(lines)-max:]
	}
	return append([]Line(nil), lines...), from
}

// modelMayAct refuses the model while the person has the terminal.
// Called with mu held.
func (t *Terminal) modelMayAct() error {
	if t.hold {
		return ErrHeld
	}
	if t.state != StateConnected {
		if t.state == StateEnded {
			return fmt.Errorf("%w: it ended (%s); connect again", ErrNotConnected, t.reason)
		}
		return fmt.Errorf("%w yet", ErrNotConnected)
	}
	if !t.lastPerson.IsZero() && time.Since(t.lastPerson) < personYield {
		return ErrPersonActive
	}
	return nil
}

// lineEmpty reports the prompt's line as the prompt left it: the cursor
// at the prompt's column, the prompt's text left of it, and nothing new
// right of it or under it. Where the prompt is on the screen does not
// matter (Ctrl+L draws it at the top). Called with mu held.
func (t *Terminal) lineEmpty() bool {
	if !t.promptKnown {
		return false
	}
	// A prompt that leaves nothing on its line (PS1 empty, or ending in a
	// line break) cannot be told from an empty row further down: a line
	// that wrapped, or one with a line break in it, leaves the cursor on
	// one. A line typed on at such a prompt is not judged by the screen.
	if t.promptX == 0 && t.promptLeft == "" {
		return false
	}
	left, rest, next := t.aroundCursor()
	return t.emu.CursorPosition().X == t.promptX && left == t.promptLeft && rest == t.promptRest && next == t.promptNext
}

// atPrompt reports a shell with integration waiting at its prompt: no
// command running, no full-screen program. Called with mu held.
func (t *Terminal) atPrompt() bool {
	return t.integrated && !t.running && !t.emu.IsAltScreen()
}

// lineHasPersonText reports text the person typed on the prompt's line
// and has not sent, which the model's input would be added to: from their
// first key until their Enter or Ctrl+C, or the next prompt, unless the
// line is as the prompt left it. Where the cursor is does not matter:
// after Ctrl+A, or a Ctrl+U at the start of the line, the text is still
// there. Without integration nothing says where a prompt is, and only the
// 10 second yield holds. Called with mu held.
func (t *Terminal) lineHasPersonText() bool {
	return t.personTyped && t.atPrompt() && !t.lineEmpty()
}

// lineHasModelText reports text the model typed at the prompt and did
// not run. Not judged by the screen: what the screen shows can hide it (a
// line of spaces pushes the rest out of sight). It holds until the next
// prompt, or the model's own Ctrl+C, which discards the whole line in
// bash and zsh. Called with mu held.
func (t *Terminal) lineHasModelText() bool {
	return t.modelTyped && t.atPrompt()
}

// WaitPrompt waits up to d for the shell to be at a prompt it has
// finished drawing, and reports whether it is. Without integration it
// cannot tell, and waits for output to be quiet instead.
func (t *Terminal) WaitPrompt(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		t.mu.Lock()
		ready := t.state == StateConnected && ((t.integrated && !t.running && t.promptKnown) ||
			(!t.integrated && !t.lastOutput.IsZero() && time.Since(t.lastOutput) > 500*time.Millisecond))
		ended := t.state == StateEnded
		ch := t.changed
		t.mu.Unlock()
		if ready {
			return true
		}
		if ended {
			return false
		}
		select {
		case <-ch:
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return false
		}
	}
	return false
}

// PersonGen counts the person's inputs, so a caller can tell whether the
// person typed between two moments.
func (t *Terminal) PersonGen() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.personGen
}

// RunResult is what run saw.
type RunResult struct {
	Output   string
	Status   string // the exit status, "" when not known
	Finished bool   // the shell said the command finished
	Alt      bool   // a full-screen program took the screen
	Asking   bool   // output stopped at what looks like a question
	TimedOut bool
	Lost     bool   // the shell stopped saying when commands end
	Ended    string // the terminal ended while waiting
	Person   bool   // the person typed in the terminal during the command
	Took     time.Duration
	// Integrated is whether the shell says when a command ends.
	Integrated bool
}

var (
	askingSecret = regexp.MustCompile(`(?i)(password|passphrase|passcode|verification code|비밀번호|암호)[^\n]{0,80}[:：]\s*$`)
	askingInput  = regexp.MustCompile(`(\?|\]|:|\))\s*$`)
)

// CheckCommand refuses a command run cannot type as written: more than
// one line, control characters, or a Tab. History expansion is the
// shell's other way of running a line that is not the one approved: it is
// off in a shell with integration, and checkHistory refuses it in one
// without.
func CheckCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("no command")
	}
	for _, r := range command {
		if r == '\n' || r == '\r' {
			return errors.New("run takes one line; join commands with ; or &&, or use type for text over several lines")
		}
		if r == '\t' {
			return errors.New("a Tab would complete rather than type; use spaces")
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return fmt.Errorf("control character %U in the command; use key for keys", r)
		}
	}
	return nil
}

// checkHistory refuses a line a shell's history expansion could rewrite:
// "^" starting it, or "!" that is not in single quotes (plain or $'...')
// and not "$!" or "${!". Conservative: a "!" this cannot place is refused.
func checkHistory(command string) error {
	if strings.HasPrefix(strings.TrimLeft(command, " "), "^") {
		return errors.New(`a line starting with "^" is the shell's history substitution; quote the "^" or start the line otherwise`)
	}
	const (
		plain = iota
		single
		ansi // $'...', where a backslash escapes
		double
	)
	quote := plain
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch quote {
		case single:
			if r == '\'' {
				quote = plain
			}
			continue
		case ansi:
			switch r {
			case '\\':
				i++
			case '\'':
				quote = plain
			}
			continue
		case double:
			switch r {
			case '\\':
				i++
				continue
			case '"':
				quote = plain
				continue
			}
		case plain:
			switch r {
			case '\\':
				i++
				continue
			case '\'':
				if i > 0 && rs[i-1] == '$' {
					quote = ansi
				} else {
					quote = single
				}
				continue
			case '"':
				quote = double
				continue
			}
		}
		if r != '!' {
			continue
		}
		// $! and ${!name} are parameters, not history.
		if i > 0 && (rs[i-1] == '$' || (rs[i-1] == '{' && i > 1 && rs[i-2] == '$')) {
			continue
		}
		if i+1 >= len(rs) {
			continue
		}
		switch rs[i+1] {
		case ' ', '=', '(':
			continue
		}
		return errors.New(`the shell would expand "!" from its history here; put that part in single quotes`)
	}
	return nil
}

// Run types command and Enter, and waits until the shell says the
// command finished, for at most timeout. gen is PersonGen when the call
// was approved: a person who typed since gets ErrChanged.
func (t *Terminal) Run(ctx context.Context, command string, timeout time.Duration, gen uint64) (RunResult, error) {
	if err := CheckCommand(command); err != nil {
		return RunResult{}, err
	}
	// The integration line being typed, or a prompt still being drawn
	// after the last command: typing then would mix with it, or be echoed
	// twice.
	integrateBy := time.Now().Add(8 * time.Second)
	var settleBy time.Time
	for {
		t.mu.Lock()
		if t.integrating && time.Now().Before(integrateBy) {
			ch := t.changed
			t.mu.Unlock()
			select {
			case <-ch:
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return RunResult{}, ctx.Err()
			}
			continue
		}
		// A Ctrl+C at the prompt: the prompt that answers it, which says
		// the line was dropped, is on its way. A slow remote takes a while
		// over it, and a run that gave up after two seconds was refused for
		// text that was no longer on the line. Not waited for once the
		// answer cannot matter: the terminal ended or is held, the person
		// typed since the run was approved, or either typed after the key,
		// which no prompt clears.
		if t.state == StateConnected && !t.hold && t.personGen == gen &&
			t.interrupts.pending() > 0 && !t.typedAfter && !t.personAfter {
			ch := t.changed
			t.mu.Unlock()
			select {
			case <-ch:
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return RunResult{}, ctx.Err()
			}
			continue
		}
		// Three seconds for the prompt to be drawn, counted from here:
		// the prompt that answers a Ctrl+C is as much to wait for as any,
		// and the wait for its mark may have used up more than that. None
		// when the line has typed text on it, which is refused below
		// whatever the prompt is.
		if settleBy.IsZero() {
			settleBy = time.Now().Add(3 * time.Second)
		}
		if !t.integrated || t.running || t.promptKnown || (t.personTyped && !t.aheadJudge) || t.modelTyped || time.Now().After(settleBy) {
			break
		}
		ch := t.changed
		t.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(promptSettle):
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		}
	}
	// mu is held here.
	if err := t.modelMayAct(); err != nil {
		t.mu.Unlock()
		return RunResult{}, err
	}
	if t.personGen != gen {
		t.mu.Unlock()
		return RunResult{}, ErrChanged
	}
	// Never typed into a command that has not ended: what looks like a
	// nested shell's prompt can be a progress line of a command that is
	// still reading its input.
	if t.integrated && t.running {
		t.mu.Unlock()
		return RunResult{}, ErrBusy
	}
	// Nor onto a full-screen program's screen. With no command running
	// that is a program that quit without giving the screen back (killed),
	// or one the terminal did not see start. What is typed there the
	// terminal does not follow as a command line.
	if t.integrated && t.emu.IsAltScreen() {
		t.mu.Unlock()
		return RunResult{}, ErrFullScreen
	}
	if t.lineHasPersonText() {
		t.mu.Unlock()
		return RunResult{}, ErrLineNotEmpty
	}
	if t.lineHasModelText() {
		err := ErrModelLine
		if !t.modelInterrupt.IsZero() && !t.typedAfter {
			// Pressing the key again is what the refusal says to do, and
			// the model just did. (With text typed after the key, pressing
			// it again is the thing to do.)
			err = fmt.Errorf("%w. No prompt has followed the ctrl+c you pressed %s ago: the remote may be slow (wait, then run again), or the shell did not take the key (press it again; if no prompt follows that either, ask the person to clear the line)",
				ErrModelLine, time.Since(t.modelInterrupt).Round(time.Second))
		}
		t.mu.Unlock()
		return RunResult{}, err
	}
	// History expansion is off in a shell with integration (integrate.go);
	// in one without, the line is refused where the shell would expand it.
	// A local shell is a PowerShell, which has none.
	if !t.integrated && !t.isLocal() {
		if err := checkHistory(command); err != nil {
			t.mu.Unlock()
			return RunResult{}, err
		}
	}
	w := &runWait{out: capture{max: captureLimit}, done: make(chan struct{}), personGen: t.personGen, sentAt: t.outputs}
	if t.isLocal() {
		pos := t.emu.CursorPosition()
		w.local = true
		w.startAbs, w.startX = t.absRow(pos.Y), pos.X
		w.cols, w.line, w.clears = t.emu.Width(), command, t.clears
		// A prompt as wide as the screen leaves the cursor on its last
		// cell, and the next character goes to the next row. A space or a
		// wide character's second half there is not told from an empty
		// cell by the screen; the prompt's width tells it.
		if pos.X == w.cols-1 && t.pendingWrap(pos) {
			w.startAbs, w.startX = w.startAbs+1, 0
		}
	}
	t.run = w
	integrated := t.integrated
	t.modelTyped = false
	t.sent()
	if t.integrated {
		t.running = true
	}
	t.runStarted = time.Now()
	t.emu.SendText(command + "\r")
	t.notify()
	t.mu.Unlock()
	t.m.used(t)

	start := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	res := RunResult{Integrated: integrated}
wait:
	for {
		select {
		case <-w.done:
			break wait
		case <-ctx.Done():
			t.mu.Lock()
			if t.run == w {
				t.run = nil
				// Stop the command this call started, unless the person
				// has stepped in since.
				if t.state == StateConnected && t.personGen == w.personGen && (t.running || !t.integrated) {
					t.sendKey(uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl})
				}
				t.applySize()
			}
			t.mu.Unlock()
			return RunResult{}, ctx.Err()
		case <-deadline.C:
			res.TimedOut = true
			break wait
		case <-poll.C:
			t.mu.Lock()
			if t.run == w {
				quiet := time.Since(t.lastOutput)
				line := t.cursorLine()
				// The screen says something about this command only once
				// the command has printed: before its echo arrives, the
				// line under the cursor is still the prompt it was typed at.
				// Counted, not timed: a fast server answers within the
				// clock's resolution, or before the wait's clock starts.
				spoke := t.outputs > w.sentAt
				switch {
				case w.local && w.ended && quiet >= 2*promptSettle && (t.promptDrawn(w) || time.Since(t.markAt) >= promptWait):
					// The prompt was not taken as settled (somebody typed
					// on it, or it looks like none): the screen is quiet.
					t.finishRun(w)
				case spoke && quiet > 700*time.Millisecond && askingSecret.MatchString(line):
					res.Asking = true
					t.finishRun(w)
				case spoke && quiet > inputAfter && t.emu.CursorPosition().X > 0 && askingInput.MatchString(strings.TrimRight(line, " ")+" ") && !looksLikePrompt(line):
					res.Asking = true
					t.finishRun(w)
				case !integrated && quiet > 2*time.Second && time.Since(start) > 2*time.Second:
					t.finishRun(w)
				case integrated && spoke && t.lostIntegration():
					res.Lost = true
					t.finishRun(w)
				}
			}
			t.mu.Unlock()
		}
	}
	t.mu.Lock()
	if t.run == w {
		t.run = nil
		t.captureRun(w)
		t.applySize()
	}
	if w.held {
		t.mu.Unlock()
		return RunResult{}, ErrHeld
	}
	if w.local {
		t.captureRun(w)
		res.Output = w.output
	} else {
		res.Output = w.out.text(!w.started)
	}
	res.Status = w.status
	res.Finished = w.ended
	res.Alt = w.alt
	res.Person = t.personGen != w.personGen
	if t.state == StateEnded {
		res.Ended = t.reason
	}
	t.mu.Unlock()
	if res.Asking {
		t.m.asking(t)
	}
	res.Took = time.Since(start).Round(100 * time.Millisecond)
	return res, nil
}

// cleanText removes what would end a bracketed paste early or act as a
// key: the paste brackets, and control characters other than line ends
// and tabs.
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\x1b[200~", "")
	s = strings.ReplaceAll(s, "\x1b[201~", "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// Type types text into whatever runs in the terminal, then Enter when
// enter. Line ends in text are typed as Enter; control characters are
// left out (key sends keys).
func (t *Terminal) Type(text string, enter bool, gen uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.modelMayAct(); err != nil {
		return err
	}
	if t.personGen != gen {
		return ErrChanged
	}
	if t.lineHasPersonText() {
		return ErrLineNotEmpty
	}
	text = cleanText(text)
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\r"), "\n", "\r")
	if enter {
		text += "\r"
	}
	if t.atPrompt() {
		// A command at the prompt goes through run.
		if strings.ContainsAny(text, "\r\t") {
			return ErrUseRun
		}
		t.modelTyped, t.typedAfter = true, true
		// Not refused above: the line read as empty of the person's. It
		// is the model's now, and nothing of theirs is on it to keep the
		// model from clearing what it types.
		t.personTyped, t.personAfter = false, false
	} else if t.typeaheadOnly() {
		if strings.Contains(text, "\r") {
			return ErrTypeahead
		}
		t.typedAhead = true
	}
	if t.integrated && t.running && t.personAhead && text != "" {
		// After what the person typed ahead: the end of what waits is no
		// longer theirs, and the screen cannot say what of theirs is left.
		t.aheadMixed = true
	}
	t.emu.SendText(text)
	t.notify()
	go t.m.used(t)
	return nil
}

// Keys presses keys, written in the tool's key grammar.
func (t *Terminal) Keys(keys string, gen uint64) error {
	evs, err := ParseKeys(keys)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.modelMayAct(); err != nil {
		return err
	}
	if t.personGen != gen {
		return ErrChanged
	}
	if t.lineHasPersonText() {
		return ErrLineNotEmpty
	}
	if t.atPrompt() {
		for _, ev := range evs {
			if !lineEditingKey(ev) {
				return ErrUseRun
			}
		}
		t.modelTyped = true
		t.personTyped, t.personAfter = false, false
		// Ctrl+C discards the line where the shell takes it as an
		// interrupt; the prompt that follows says so. Nothing is taken for
		// granted before it: with "stty intr undef" the key is only a
		// character. The key drops what was typed before it and nothing
		// after: a key pressed after it, in this call or a later one, is on
		// the line of the prompt it brings, where "ctrl+c r m" used to
		// leave "rm" for the next run to finish.
		for _, ev := range evs {
			if ev.Mod == uv.ModCtrl && ev.Code == 'c' {
				t.interrupt()
				t.modelInterrupt = time.Now()
			} else {
				t.typedAfter = true
			}
		}
	} else if t.typeaheadOnly() {
		for _, ev := range evs {
			if endsLine(ev) {
				return ErrTypeahead
			}
		}
		// A key that signals the running command is no text typed ahead
		// of the next prompt: the terminal takes it, and drops whatever
		// was typed ahead before it. Counted as text, a Ctrl+C that
		// stopped a command (what a run that timed out says to press) left
		// the next run refused for a line nothing was on.
		for _, ev := range evs {
			if t.signalKey(ev) {
				t.typedAhead = false
			} else {
				t.typedAhead = true
			}
		}
	}
	if t.integrated && t.running {
		// A signal drops what the person typed ahead too. Any other key
		// goes after it, and the screen cannot say what of theirs is left.
		for _, ev := range evs {
			if t.signalKey(ev) && !t.emu.IsAltScreen() {
				t.signalled()
			} else if t.personAhead {
				t.aheadMixed = true
			}
		}
	}
	for _, ev := range evs {
		t.sendKey(ev)
	}
	t.notify()
	go t.m.used(t)
	return nil
}

// interrupt notes a Ctrl+C sent to the shell at its prompt. Where the
// shell takes it, the line as it is goes, and a new prompt comes: what
// either typed before the key is dropped with it, and where the prompt was
// is no longer where it is. Called with mu held.
func (t *Terminal) interrupt() {
	t.interrupts.press()
	t.typedAfter, t.personAfter = false, false
	// The line is no longer as the command left it: a shell that reads the
	// key as a character puts it there, and no prompt follows.
	t.aheadJudge = false
	t.promptKnown = false
}

// sent notes the line going to the shell to be run: the person's Enter, or
// a run's own line. Whatever was on it went with it, and no Ctrl+C before
// it is still to be answered at this prompt. Called with mu held.
func (t *Terminal) sent() {
	t.typedAfter, t.personAfter = false, false
	t.dropAhead()
	t.aheadSent, t.aheadJudge = false, false
	t.interrupts = interrupts{}
}

// dropAhead forgets what the person typed while a command ran: a line end
// or a signal took it, a line was sent, or a key is the first typed while
// this command runs and what was typed while an earlier one ran is that
// one's, read or held by now. Called with mu held.
func (t *Terminal) dropAhead() {
	t.personAhead, t.aheadText, t.aheadMixed, t.aheadPasted = false, "", false, false
}

// signalled notes a key the terminal turns into a signal for the command
// that runs (either's), on the main screen: on the alternate screen a
// full-screen program reads keys raw, and no signal comes of it. The
// terminal drops what waits in it, what was typed while the command ran,
// and what is typed after the key starts on an empty line. A line the
// shell keeps the signal does not drop, and a hold stays: the terminal
// cannot say whether text typed after a line sent ahead waited behind
// that line or went with a line a command read. Where the key is no
// signal (`stty intr undef`), nothing is dropped and the terminal does
// not follow it. Called with mu held.
func (t *Terminal) signalled() {
	t.dropAhead()
	t.aheadSent = false
}

// aheadKey notes a key the person pressed while a command ran. Called
// with mu held.
func (t *Terminal) aheadKey(ev uv.KeyPressEvent) {
	if !t.personAhead {
		t.dropAhead()
	}
	ctrl := ev.Mod == uv.ModCtrl
	switch {
	case t.signalKey(ev) && !t.emu.IsAltScreen():
		t.signalled()
	case endsLine(ev) && ev.Mod&uv.ModAlt == 0 && !(ctrl && ev.Code == 'o'):
		// The line goes to whatever reads it, the shell too: nothing typed
		// before it is left waiting, and what is typed after it can be for
		// the prompt after the shell runs it. (Ctrl+O ends no line in the
		// terminal. With Alt, the shell reads Escape and the key as one:
		// bash does nothing with it, zsh puts a line break in the line.)
		// On the alternate screen the program reads it, as any key there.
		t.dropAhead()
		t.aheadSent = !t.emu.IsAltScreen()
	case ev.Text != "" && ev.Mod&^uv.ModShift == 0:
		t.aheadTyped(ev.Text, false)
	case t.emu.IsAltScreen():
		// A full-screen program's: it reads every key. (Text is read off
		// the screen at the prompt, as anywhere: what it read is not there.)
	case !t.aheadMixed && !t.aheadPasted && ev.Mod == 0 && ev.Code == uv.KeyBackspace && aheadErasable(t.aheadText):
		// The terminal's line editing, where a command reads whole lines,
		// and the shell's: one character less. A program that reads keys
		// took it all. (After a bracketed paste the terminal erases the
		// paste's closing mark, not the text.)
		_, n := utf8.DecodeLastRuneInString(t.aheadText)
		t.aheadText = t.aheadText[:len(t.aheadText)-n]
	case !t.aheadMixed && ctrl && ev.Code == 'u' && !t.isLocal():
		// Not in a local terminal: PowerShell leaves the line as it is.
		// After a bracketed paste too: the terminal kills the paste with
		// its marks.
		t.aheadText, t.aheadPasted = "", false
	case ctrl && ev.Code == 'd' && t.aheadText == "" && !t.aheadMixed && !t.aheadPasted:
		// The end of input, at the start of a line: to whatever reads it
		// (Ctrl+D to leave a REPL, or to end what cat reads). Read by the
		// shell, it ends the shell, or the shell says it will not. After
		// text, the terminal passes the text on as it is, beyond Backspace
		// and Ctrl+U: that is a key like any other.
	default:
		// What the shell does with any other key (Up recalls a command,
		// Escape waits for the next key, Ctrl+W ends a word differently in
		// zsh) the screen cannot be read for.
		t.personAhead, t.aheadMixed = true, true
	}
}

// aheadErasable is text whose last character Backspace takes off as the
// terminal's line editing does: one byte, unless the terminal counts
// UTF-8, which the shell side does not say. Called on what the person
// typed while a command ran.
func aheadErasable(s string) bool {
	return s == "" || s[len(s)-1] < utf8.RuneSelf
}

// aheadTyped notes text the person typed or pasted while a command ran.
// A line end in it sends the line (unless the paste is bracketed, which
// keeps it in the line): what came before it is not left waiting, and
// what comes after can be for the prompt after the shell runs it. Called
// with mu held.
func (t *Terminal) aheadTyped(s string, bracketed bool) {
	if !t.personAhead {
		t.dropAhead()
	}
	if i := strings.LastIndex(s, "\r"); i >= 0 && !bracketed {
		t.dropAhead()
		t.aheadSent = !t.emu.IsAltScreen()
		s = s[i+1:]
	}
	if s == "" {
		return
	}
	t.personAhead = true
	t.aheadPasted = t.aheadPasted || bracketed
	if t.aheadSent || !drawnAsTyped(s) {
		// After a line sent ahead, the shell runs a command between this
		// prompt and the one the text is drawn at. A Tab completes, and a
		// line end kept in a bracketed paste is on several rows: neither
		// is drawn as typed.
		t.aheadMixed = true
	}
	t.aheadText += s
}

// drawnAsTyped is text a shell draws as it was typed, each character in a
// cell of its own (two for a wide one), so the screen can be read for it.
// A character that is not graphic (U+200B, U+FEFF, a soft hyphen, private
// use) is not: bash draws it as it is, in no cell of its own, and zsh
// draws markup (<200b>), measured. Nor is one the terminal puts in one
// cell with the character before it (joinsTheOneBefore): a combining
// accent (zsh draws <0301>), an emoji's variation selector or skin tone,
// Thai SARA AM.
func drawnAsTyped(s string) bool {
	for _, r := range s {
		if !unicode.IsGraphic(r) || joinsTheOneBefore(r) {
			return false
		}
	}
	return true
}

// joinsTheOneBefore is a character the terminal puts in one cell with the
// character before it, as one grapheme: an emoji's skin tone, Thai SARA
// AM, a half-width voiced mark. Drawn first after a prompt, it is in the
// cell of the prompt's last space, and the prompt read off the row would
// have it.
func joinsTheOneBefore(r rune) bool {
	c, _ := ansi.FirstGraphemeCluster(" "+string(r), ansi.GraphemeWidth)
	return c != " "
}

// lineEditingKey is a key the model may press at the shell's prompt:
// one that moves, deletes, clears or interrupts, and none that runs the
// line (Enter, ctrl+j, ctrl+m, ctrl+o), completes it (Tab), or starts a
// sequence another key finishes (Escape, ctrl+x).
func lineEditingKey(ev uv.KeyPressEvent) bool {
	if ev.Text != "" {
		return printable(ev.Text)
	}
	if ev.Mod == 0 {
		switch ev.Code {
		case uv.KeyLeft, uv.KeyRight, uv.KeyUp, uv.KeyDown, uv.KeyHome, uv.KeyEnd, uv.KeyBackspace, uv.KeyDelete:
			return true
		}
		return false
	}
	if ev.Mod == uv.ModCtrl {
		switch ev.Code {
		case 'a', 'e', 'b', 'f', 'k', 'u', 'w', 'l', 'c':
			return true
		}
	}
	return false
}

// printable reports text with no control character in it.
func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// endsLine is a key that sends a line on: Enter, and the control
// characters a terminal reads as one.
func endsLine(ev uv.KeyPressEvent) bool {
	if ev.Code == uv.KeyEnter {
		return true
	}
	if ev.Mod&uv.ModCtrl != 0 {
		switch ev.Code {
		case 'j', 'm', 'o':
			return true
		}
	}
	return false
}

// signalKey is a key the terminal turns into a signal for the command in
// the foreground, rather than into input, and that drops the input typed
// ahead of it. On a remote shell's terminal: interrupt, quit and suspend
// (Ctrl+C, Ctrl+\\, Ctrl+Z), which flush the terminal's input queue. On a
// Windows console only Ctrl+C. Measured under ConPTY on Windows Server
// 2022 and 2025, both PowerShells, with a line typed while Start-Sleep
// ran: Ctrl+C stopped it and the line was gone; Ctrl+\\ and Ctrl+Z stopped
// nothing, and the line was on the next prompt, with "^\\" after it or its
// last character undone. Called with mu held.
func (t *Terminal) signalKey(ev uv.KeyPressEvent) bool {
	if ev.Mod != uv.ModCtrl {
		return false
	}
	switch ev.Code {
	case 'c':
		return true
	case '\\', 'z':
		return !t.isLocal()
	}
	return false
}

// typeaheadOnly reports a command running in a shell with integration,
// with nothing on the screen waiting for input: no full-screen program,
// and the cursor's line neither a prompt (a nested shell, a REPL) nor a
// question. A line sent then waits in the terminal and is read by the
// shell as its next command once this one ends. Called with mu held.
func (t *Terminal) typeaheadOnly() bool {
	if !t.integrated || !t.running || t.emu.IsAltScreen() {
		return false
	}
	line := strings.TrimRight(t.cursorLine(), " ")
	if line == "" {
		return true
	}
	return !looksLikePrompt(line) && !askingInput.MatchString(line+" ") && !askingSecret.MatchString(line+" ")
}

// sendKey sends one key. Called with mu held.
func (t *Terminal) sendKey(ev uv.KeyPressEvent) {
	if seq, ok := encodeSpecial(ev); ok {
		t.emu.SendText(seq)
		return
	}
	if ev.Text != "" {
		t.emu.SendText(ev.Text)
		return
	}
	t.emu.SendKeys(ev)
}

// PersonKey is a key the person pressed in the panel.
type PersonKey struct {
	Key   string `json:"key"`
	Ctrl  bool   `json:"ctrl"`
	Alt   bool   `json:"alt"`
	Meta  bool   `json:"meta"`
	Shift bool   `json:"shift"`
}

// PersonInput is the person typing: a key, text (typed or composed by an
// input method), or a paste.
func (t *Terminal) PersonInput(k *PersonKey, text, paste string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != StateConnected {
		return ErrNotConnected
	}
	t.lastPerson = time.Now()
	t.personGen++
	// What the input does to the command line, where the shell is at its
	// prompt: enter sends it, clears is Ctrl+C. after is something on the
	// line that comes after the line end of the same input.
	enter, clears, after := false, false, false
	// rows is a line break put in the line without sending it: the line
	// is over several rows after that, and the row the cursor is on does
	// not say what is on it. The last row of a paste can read as the
	// prompt does, when what was copied had a prompt in it.
	rows := false
	atPrompt := !(t.integrated && t.running)
	switch {
	case k != nil:
		ev, ok := personKeyEvent(*k)
		if !ok {
			return nil
		}
		t.sendKey(ev)
		if !atPrompt {
			t.aheadKey(ev)
		}
		// Enter, and the keys a shell reads as one: not with Alt, which
		// makes each of them another key (zsh puts a line break in the
		// line, bash does nothing). Ctrl+O sends the line too, and bash
		// then puts the next line of its history on the prompt: nobody
		// typed it, and it is there.
		enter = endsLine(ev) && ev.Mod&uv.ModAlt == 0
		after = enter && ev.Mod&uv.ModCtrl != 0 && ev.Code == 'o'
		rows = endsLine(ev) && !enter
		// Ctrl+C drops the line, where the shell takes it. Ctrl+U does
		// not, at the start of the line: whether it is empty is lineEmpty's
		// to say.
		clears = ev.Mod == uv.ModCtrl && ev.Code == 'c'
	case text != "":
		text = cleanText(text)
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\r"), "\n", "\r")
		t.emu.SendText(text)
		if !atPrompt {
			t.aheadTyped(text, false)
		}
		enter = strings.Contains(text, "\r")
		after = enter && !strings.HasSuffix(text, "\r")
	case paste != "":
		paste = cleanText(paste)
		if paste == "" {
			// Nothing to paste: no bracketed paste marks for nothing, which
			// a shell with bracketed paste off draws.
			break
		}
		paste = strings.ReplaceAll(strings.ReplaceAll(paste, "\r\n", "\r"), "\n", "\r")
		// A shell that has not turned bracketed paste on reads a paste as
		// typed, and a line end in it sends the line. One that has keeps
		// the paste on the line, line ends and all.
		enter = strings.Contains(paste, "\r") && !t.emu.BracketedPaste()
		after = enter && !strings.HasSuffix(paste, "\r")
		rows = strings.Contains(paste, "\r") && !enter
		if !atPrompt {
			t.aheadTyped(paste, t.emu.BracketedPaste())
		}
		t.emu.Paste(paste)
	}
	switch {
	case enter && !atPrompt:
		// Into a command that is running: that command's to read, and
		// nothing to do with the line the shell will be at when it ends.
	case enter:
		// The line is sent, with whatever either typed on it. What came
		// after the line end is on the next line.
		t.personTyped = false
		t.sent()
		t.personAfter = after
		if t.integrated {
			t.running = true
			t.runStarted = time.Now()
		}
	case clears:
		// At the prompt the key is the line's: it drops what either typed
		// before it, where the shell takes it as an interrupt. The prompt it
		// brings says so, and until then the key counts as typed ("stty intr
		// undef" makes it a character on the line). On the alternate screen
		// too, where a prompt is when a program left the screen that way:
		// what they type there counts, so the key that drops it does.
		if t.integrated && !t.running {
			t.interrupt()
			t.personTyped = true
		} else {
			t.personTyped = false
		}
	case atPrompt:
		t.personTyped, t.personAfter = true, true
		t.aheadJudge = false
		if rows {
			t.promptKnown = false
		}
	}
	t.notify()
	return nil
}

// ViewerSize records the size a viewer's visible panel fits; zero cols
// forgets the viewer (its panel was hidden, or it went away). The
// terminal takes the smallest size among the viewers, so no viewer
// cuts off what another shows, and keeps its size with none.
func (t *Terminal) ViewerSize(viewer string, cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cols <= 0 || rows <= 0 {
		delete(t.sizes, viewer)
	} else {
		t.sizes[viewer] = [2]int{max(MinCols, min(MaxCols, cols)), max(MinRows, min(MaxRows, rows))}
	}
	t.applySize()
}

// applySize resizes to the smallest viewer size, unless a run is
// waiting: a resize makes the shell redraw its line, which would land in
// the command's output. Called with mu held.
func (t *Terminal) applySize() {
	if t.run != nil || len(t.sizes) == 0 {
		return
	}
	cols, rows := MaxCols, MaxRows
	for _, s := range t.sizes {
		cols, rows = min(cols, s[0]), min(rows, s[1])
	}
	if cols == t.cols && rows == t.rows {
		return
	}
	t.cols, t.rows = cols, rows
	// A new size can move the cursor and what is around it. Where the
	// cursor was still as the end of the last command left it, that is no
	// prompt drawn: the mark moves with it.
	atMark := t.atMark()
	// A smaller screen cuts off what is past its edges, and puts a cursor
	// that was past them back inside. With text of the person's cut off,
	// or the cursor moved to another row, what is left can look like the
	// prompt alone: the screen says nothing about the line after that. A
	// line that reads as empty now has nothing of theirs to cut.
	if t.personTyped && !t.lineEmpty() && t.cutsLine(cols, rows) {
		t.promptKnown = false
		// Not read for what was typed while the command ran either: what
		// the cut took is not on the screen to be read.
		if t.aheadJudge {
			t.aheadJudge, t.personAfter = false, true
		}
	}
	t.emu.Resize(cols, rows)
	if atMark {
		t.markHere()
	}
	select {
	case t.resized <- struct{}{}:
	default:
	}
	t.notify()
}

// sizer sends the terminal's size to the server whenever it changes.
func (t *Terminal) sizer(sess *ssh.Session) {
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.resized:
		}
		t.mu.Lock()
		cols, rows := t.cols, t.rows
		t.mu.Unlock()
		if err := sess.WindowChange(rows, cols); err != nil {
			return
		}
	}
}

// SetHold holds the terminal for the person, or lets go of it.
func (t *Terminal) SetHold(on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hold = on
	// A run waiting when the person takes the terminal gets nothing of
	// what comes after: it ends now, refused, its capture dropped.
	if w := t.run; on && w != nil {
		w.held = true
		w.out = capture{}
		t.finishRun(w)
	}
	t.notify()
}

// ReadResult is the screen as the model reads it.
type ReadResult struct {
	Info       Info
	Screen     []string
	Scrollback []string
	CursorX    int
	CursorY    int
	RunningFor time.Duration
}

// Read is the screen as text, with up to scrollback lines before it.
// For the model it is refused while the person holds the terminal.
func (t *Terminal) Read(scrollback int, forModel bool) (ReadResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if forModel && t.hold {
		return ReadResult{}, ErrHeld
	}
	r := ReadResult{Info: t.infoLocked()}
	s := t.screenLocked()
	for _, l := range s.Lines {
		r.Screen = append(r.Screen, l.Text())
	}
	for len(r.Screen) > 0 && r.Screen[len(r.Screen)-1] == "" && len(r.Screen)-1 > s.CursorY {
		r.Screen = r.Screen[:len(r.Screen)-1]
	}
	if scrollback > 0 {
		from := max(0, len(t.scroll)-scrollback)
		for _, l := range t.scroll[from:] {
			r.Scrollback = append(r.Scrollback, l.Text())
		}
	}
	r.CursorX, r.CursorY = s.CursorX, s.CursorY
	if t.running {
		r.RunningFor = time.Since(t.runStarted).Round(time.Second)
	}
	return r, nil
}

// Wait waits until text is on the screen (when text is not empty), or
// the running command finishes, or d passes. Refused while the person
// holds the terminal.
func (t *Terminal) Wait(ctx context.Context, d time.Duration, text string) (bool, error) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		t.mu.Lock()
		if t.hold {
			t.mu.Unlock()
			return false, ErrHeld
		}
		ch := t.changed
		ended := t.state == StateEnded
		done := false
		if text != "" {
			for y := 0; y < t.emu.Height() && !done; y++ {
				yy := y
				if strings.Contains(lineOf(t.emu.Width(), func(x int) *uv.Cell { return t.emu.CellAt(x, yy) }).Text(), text) {
					done = true
				}
			}
		} else if t.integrated && !t.running {
			done = true
		}
		t.mu.Unlock()
		if done {
			return true, nil
		}
		if ended {
			return false, nil
		}
		select {
		case <-ch:
		case <-deadline.C:
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// Close ends the connection.
func (t *Terminal) Close(reason string) {
	t.end(reason)
}

// openAgent connects to the agent: path names one, "none" none, and ""
// the one SSH_AUTH_SOCK names. Nil when there is none.
func openAgent(path string) (agent.ExtendedAgent, func()) {
	if strings.EqualFold(path, "none") {
		return nil, func() {}
	}
	conn, err := dialAgent(path)
	if err != nil || conn == nil {
		return nil, func() {}
	}
	return agent.NewClient(conn), func() { conn.Close() }
}
