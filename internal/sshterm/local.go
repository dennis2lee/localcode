package sshterm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Local terminals: a shell on this computer, for the PowerShell panel.
//
// A manager whose Options.Local is set opens shells on this computer
// instead of SSH connections. Everything the model and the person do in
// a terminal is the same code either way: the screen, run, type, key,
// hold, the questions a screen asks. What differs is how the shell is
// started and how it says a command ended.
//
// It is started under a pseudo console (ConPTY, local_windows.go), with
// the integration given on its own command line rather than typed into
// it: PowerShell runs -EncodedCommand before its first prompt, after the
// person's profile, so nothing appears on the screen and nothing waits
// for a prompt to look like a prompt. Measured on Windows Server 2022 and
// 2025 (diag/pwsh), Windows PowerShell 5.1 and PowerShell 7 both: the
// system's pseudo console passes OSC 133 through, the status of a native
// command, a cmdlet that failed and one that did not each arrive right,
// and Korean output comes back as UTF-8.

// LocalShell is the shell a local manager starts.
type LocalShell struct {
	// Path is the shell: powershell.exe (Windows PowerShell), pwsh.exe
	// (PowerShell 7), or a full path to either.
	Path string
	// Dir answers the directory a conversation's shell starts in. Nil,
	// or "", starts it where localcode runs.
	Dir func(conv string) string
	// Start runs argv under a pseudo console of cols by rows, in dir. Nil
	// uses the platform's own, which only Windows has.
	Start func(argv []string, dir string, cols, rows int) (LocalProcess, error)
}

// LocalProcess is a shell running under a pseudo console.
type LocalProcess interface {
	// Read is what the console draws.
	Read(p []byte) (int, error)
	// Write is what is typed.
	Write(p []byte) (int, error)
	// Resize gives the console a new size.
	Resize(cols, rows int) error
	// Wait returns when the shell exits, with its exit code.
	Wait() (int, error)
	// Close ends the shell and the console. Safe to call more than once.
	Close() error
}

// ErrNoLocalShell is a local shell asked for where there is none.
var ErrNoLocalShell = errors.New("a PowerShell terminal needs Windows")

// localWait bounds how long a starting shell has to draw its first
// prompt before it is taken to have no integration. Windows PowerShell
// with a profile that loads modules takes seconds.
var localWait = 20 * time.Second

// ShellName is a local shell's name as the panel and the model see it.
func ShellName(path string) string {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "pwsh":
		return "pwsh"
	case "powershell":
		return "powershell"
	}
	return base
}

// powershellIntegration is the script a PowerShell runs before its first
// prompt.
//
//   - Its prompt prints the D mark, with the last command's status, ahead
//     of whatever prompt was there (the default, or the person's profile's).
//     A native program's own exit code when it set one, 1 for a command
//     that failed otherwise, 0 for one that did not.
//   - PSReadLine's PSConsoleHostReadLine, where it is loaded, prints the C
//     mark once the line is read, which is where a command's output starts.
//     Without PSReadLine there is no C mark, and the output is what follows
//     the command's line.
//   - [char] rather than `e, which Windows PowerShell 5.1 does not have.
//   - Every mark carries the nonce, as the bash and zsh marks do.
//   - The D mark carries the prompt's text too (base64, up to 600
//     characters of it), which is how the end of a command's output is
//     found on the screen (localOutput).
//
// It prints nothing itself.
func powershellIntegration(nonce string) string {
	return `$global:__lc_n='` + nonce + `'
$global:__lc_e=[char]27
$global:__lc_b=[char]7
$global:__lc_prompt=$function:prompt
$global:__lc_le=$global:LASTEXITCODE
function global:prompt {
  $ok=$global:?
  $s = if ($ok) {0} elseif ($global:LASTEXITCODE -ne $global:__lc_le -and $global:LASTEXITCODE) {$global:LASTEXITCODE} else {1}
  $global:__lc_le=$global:LASTEXITCODE
  $p = "$(& $global:__lc_prompt)"
  $t = if ($p.Length -le 600) {';p=' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($p))} else {''}
  "$($global:__lc_e)]133;D;$s$t;lc=$($global:__lc_n)$($global:__lc_b)" + $p
}
if (Test-Path Function:\PSConsoleHostReadLine) {
  $global:__lc_rl=$function:PSConsoleHostReadLine
  function global:PSConsoleHostReadLine {
    $l = & $global:__lc_rl
    [Console]::Write("$($global:__lc_e)]133;C;lc=$($global:__lc_n)$($global:__lc_b)")
    $global:__lc_le=$global:LASTEXITCODE
    $l
  }
}
`
}

// powershellArgs is the command line a local terminal starts: the shell
// with its profile, no banner, and the integration.
func powershellArgs(path, nonce string) []string {
	return []string{path, "-NoLogo", "-NoExit", "-EncodedCommand", encodePowerShell(powershellIntegration(nonce))}
}

// encodePowerShell is -EncodedCommand's form: UTF-16LE, then base64.
func encodePowerShell(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// isLocal reports a terminal of a local manager.
func (t *Terminal) isLocal() bool { return t.m.opts.Local != nil }

// startLocal starts the shell, and the goroutines that carry its input,
// output and size.
func (t *Terminal) startLocal(ctx context.Context) error {
	l := t.m.opts.Local
	if l.Path == "" {
		return errors.New("no shell is set")
	}
	start := l.Start
	if start == nil {
		start = startPseudoConsole
	}
	dir := ""
	if l.Dir != nil {
		dir = l.Dir(t.Conv)
	}
	t.mu.Lock()
	cols, rows := t.cols, t.rows
	t.mu.Unlock()
	p, err := start(powershellArgs(l.Path, t.marks.nonce), dir, cols, rows)
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.state == StateEnded || ctx.Err() != nil {
		t.mu.Unlock()
		p.Close()
		return errors.New("closed while starting")
	}
	t.closers = append(t.closers, p)
	t.shell = ShellName(l.Path)
	t.state = StateConnected
	// Until its first prompt, which carries the first mark: a run waits
	// for it rather than typing into a shell that is still loading.
	t.integrating = true
	t.notify()
	t.mu.Unlock()

	go t.writer(p)
	go t.reader(p)
	go t.localSizer(p)
	go func() {
		code, err := p.Wait()
		reason := fmt.Sprintf("the shell exited with status %d", code)
		if err != nil {
			reason = "the shell stopped: " + err.Error()
		}
		t.end(reason)
	}()
	go t.integrateLocal()
	return nil
}

// integrateLocal waits for the first mark. Without one in time the shell
// is taken to have no integration: its profile replaced the prompt after
// the integration ran, or it is not a PowerShell.
func (t *Terminal) integrateLocal() {
	defer func() {
		t.mu.Lock()
		t.integrating = false
		t.notify()
		t.mu.Unlock()
	}()
	deadline := time.Now().Add(localWait)
	for time.Now().Before(deadline) {
		t.mu.Lock()
		on, state, ch := t.integrated, t.state, t.changed
		t.mu.Unlock()
		if on || state != StateConnected {
			return
		}
		select {
		case <-t.ctx.Done():
			return
		case <-ch:
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// localSizer gives the console the terminal's size whenever it changes.
func (t *Terminal) localSizer(p LocalProcess) {
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.resized:
		}
		t.mu.Lock()
		cols, rows := t.cols, t.rows
		t.mu.Unlock()
		if err := p.Resize(cols, rows); err != nil {
			return
		}
	}
}

// A command's output in a local terminal, read off the screen.
//
// A pseudo console is a renderer, not a pipe: it draws the console's
// buffer as it changes, on a frame of its own, and sends a mark the shell
// prints straight away. Windows Server 2025's sends the text before a
// mark first. Windows Server 2022's does not (diag/pwsh): a command's C
// and D marks arrived before its echo, or in the middle of its output, so
// neither the stream between the marks nor where the D mark landed on
// the screen says where the output is. What the marks carry is right:
// the status, and the prompt that follows. So a local run ends once the
// screen shows that prompt at the cursor, and its output is the rows
// between the command's echo and the prompt's first row.

// absRow numbers screen row y among every line the terminal has had:
// scrollback, dropped lines included, then the screen. Called with mu
// held.
func (t *Terminal) absRow(y int) int { return t.scrollFirst + len(t.scroll) + y }

// echoEnd is the row the command's echo ends on: it starts after the
// prompt, at startX, and wraps at the screen's width.
func (w *runWait) echoEnd() int {
	return w.startAbs + (w.startX+w.width-1)/max(1, w.cols)
}

// promptRows is how many rows prompt takes on a screen cols wide, the
// cursor on the last of them.
func promptRows(prompt string, cols int) int {
	cols = max(1, cols)
	n := 0
	for _, line := range strings.Split(prompt, "\n") {
		w := ansi.StringWidth(line)
		n += max(1, (w+cols-1)/cols)
	}
	return n
}

// promptDrawn reports the screen showing the prompt the run's D mark
// named, ending at the cursor. Without one named, the settled prompt is
// all there is to go by. Called with mu held.
func (t *Terminal) promptDrawn(w *runWait) bool {
	if w.prompt == "" {
		return true
	}
	lines := strings.Split(w.prompt, "\n")
	last := lines[len(lines)-1]
	cols := t.emu.Width()
	if n := ansi.StringWidth(last); n > cols {
		// The part of it on the cursor's row.
		cut := n - n%cols
		if n%cols == 0 {
			cut = n - cols
		}
		last = ansi.TruncateLeft(last, cut, "")
	}
	left, _, _ := t.aroundCursor()
	return strings.TrimRight(left, " ") == strings.TrimRight(last, " ")
}

// rowText is the text of row abs, and false for a row the terminal no
// longer has (dropped from the scrollback) or does not have yet. Called
// with mu held.
func (t *Terminal) rowText(abs int) (string, bool) {
	if abs < t.scrollFirst {
		return "", false
	}
	if i := abs - t.scrollFirst; i < len(t.scroll) {
		return strings.TrimRight(t.scroll[i].Text(), " "), true
	}
	y := abs - t.absRow(0)
	if y >= t.emu.Height() {
		return "", false
	}
	return strings.TrimRight(lineOf(t.emu.Width(), func(x int) *uv.Cell { return t.emu.CellAt(x, y) }).Text(), " "), true
}

// localOutput is what the run's command printed: the rows after its echo,
// up to the row before the prompt when it ended, and up to the cursor's
// row (the line still being written) when it did not. Called with mu held.
func (t *Terminal) localOutput(w *runWait) string {
	from := w.echoEnd() + 1
	cur := t.absRow(t.emu.CursorPosition().Y)
	last := cur
	if w.ended {
		rows := 1
		if w.prompt != "" {
			rows = promptRows(w.prompt, t.emu.Width())
		}
		last = cur - rows
	}
	// A row filled to the last column is taken to go on in the next one:
	// a line wider than the screen, wrapped there. A pseudo console sends
	// nothing that says which line ends were the program's, and a path a
	// model copies out of the output must not have a line end in it. A
	// line the program ended exactly at the screen's width is joined to
	// the next too; that is rarer, and docs/USAGE.md says so.
	cols := t.emu.Width()
	var b strings.Builder
	dropped := 0
	joined := true
	for abs := from; abs <= last; abs++ {
		s, ok := t.rowText(abs)
		if !ok {
			if abs < t.scrollFirst {
				dropped++
			}
			continue
		}
		if !joined {
			b.WriteByte('\n')
		}
		b.WriteString(s)
		joined = ansi.StringWidth(s) >= cols
	}
	out := strings.TrimRight(b.String(), " \n")
	if dropped > 0 {
		out = "[... " + itoa(dropped) + " earlier lines left the scrollback ...]\n" + out
	}
	return out
}
