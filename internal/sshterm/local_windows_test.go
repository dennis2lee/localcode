//go:build windows

package sshterm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The real thing: Windows PowerShell 5.1, and PowerShell 7 where it is
// installed, under the system's pseudo console. Only CI's Windows job
// runs these (the local gate cross-builds Windows and runs no tests there).

// realShells are the shells this machine has.
func realShells(t *testing.T) []string {
	var out []string
	for _, name := range []string{"powershell.exe", "pwsh.exe"} {
		if _, err := exec.LookPath(name); err == nil {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		t.Skip("no PowerShell on this machine")
	}
	return out
}

// realTerminal starts shell in dir and waits for its first prompt.
func realTerminal(t *testing.T, shell, dir string) (*Manager, *Terminal) {
	t.Helper()
	m := NewManager(Options{Local: &LocalShell{Path: shell, Dir: func(string) string { return dir }}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	term, err := m.Open("c1", "PowerShell", ByModel)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := term.WaitConnected(ctx); err != nil {
		t.Fatalf("start %s: %v", shell, err)
	}
	if !eventually(60*time.Second, func() bool { return term.Info().Integrated }) {
		r, _ := term.Read(0, false)
		t.Fatalf("%s: no mark within a minute; info %+v, screen:\n%s", shell, term.Info(), strings.Join(r.Screen, "\n"))
	}
	settled(t, term)
	return m, term
}

func realRun(t *testing.T, term *Terminal, cmd string) RunResult {
	t.Helper()
	res, err := term.Run(context.Background(), cmd, 60*time.Second, term.PersonGen())
	if err != nil {
		r, _ := term.Read(0, false)
		t.Fatalf("run %q: %v; screen:\n%s", cmd, err, strings.Join(r.Screen, "\n"))
	}
	return res
}

func TestARealPowerShellRunsCommandsAndReportsTheirStatus(t *testing.T) {
	for _, shell := range realShells(t) {
		t.Run(shell, func(t *testing.T) {
			dir := t.TempDir()
			_, term := realTerminal(t, shell, dir)
			if got, want := term.Info().Shell, ShellName(shell); got != want {
				t.Errorf("shell %q, want %q", got, want)
			}
			for _, c := range []struct {
				cmd, out, status string
			}{
				{"echo hello", "hello", "0"},
				{"cmd /c exit 3", "", "3"},
				{"'한글 출력'", "한글 출력", "0"},
				{`1..3 | ForEach-Object { "line $_" }`, "line 1\nline 2\nline 3", "0"},
			} {
				res := realRun(t, term, c.cmd)
				if !res.Finished || res.Status != c.status || strings.TrimSpace(res.Output) != c.out {
					t.Errorf("run %q: %+v, want output %q and status %s", c.cmd, res, c.out, c.status)
				}
			}
			// Where it started: the same directory, whichever of its
			// names (a temp directory has an 8.3 one) either side uses.
			// A path wider than the screen comes back as one line.
			res := realRun(t, term, "(Get-Location).Path")
			if strings.Contains(res.Output, "\n") {
				t.Errorf("the path came back on more than one line: %q", res.Output)
			}
			got, err1 := os.Stat(strings.TrimSpace(res.Output))
			want, err2 := os.Stat(dir)
			if err1 != nil || err2 != nil || !os.SameFile(got, want) {
				t.Errorf("started in %q, want %q (%v, %v)", res.Output, dir, err1, err2)
			}
			res = realRun(t, term, `Get-Item C:\no\such\thing`)
			if !res.Finished || res.Status != "1" || !strings.Contains(res.Output, "Cannot find path") {
				t.Errorf("a cmdlet that failed: %+v", res)
			}
			// A cmdlet that succeeds after a native command that failed:
			// the earlier exit code is not this command's.
			realRun(t, term, "cmd /c exit 4")
			if res := realRun(t, term, "Get-Date | Out-Null"); res.Status != "0" {
				t.Errorf("a cmdlet after a failed native command: %+v", res)
			}
			// A native program exiting as the one before it did leaves
			// $LASTEXITCODE as it was, and was reported as 1; a cmdlet
			// failing after it adds to $Error and is 1.
			for _, c := range []struct{ cmd, status string }{
				{"cmd /c exit 7", "7"},
				{"cmd /c exit 7", "7"},
				{`Get-Item C:\no\such\thing`, "1"},
				{"cmd /c exit 7", "7"},
				{"cmd /c exit 0", "0"},
			} {
				if res := realRun(t, term, c.cmd); res.Status != c.status {
					t.Errorf("run %q: status %q, want %s", c.cmd, res.Status, c.status)
				}
			}
		})
	}
}

func TestARealPowerShellIsResizedAndInterrupted(t *testing.T) {
	// As a localcode started from Git Bash is: told to ignore Ctrl+C,
	// which the shells it starts would inherit (local_windows.go).
	procSetConsoleCtrlHandler.Call(0, 1)
	allowCtrlC = sync.Once{}
	shell := realShells(t)[0]
	_, term := realTerminal(t, shell, t.TempDir())
	term.ViewerSize("v1", 80, 24)
	if !eventually(10*time.Second, func() bool {
		res := realRun(t, term, "$Host.UI.RawUI.WindowSize.Width")
		return strings.TrimSpace(res.Output) == "80"
	}) {
		t.Fatal("the console never saw 80 columns")
	}

	res, err := term.Run(context.Background(), "Start-Sleep -Seconds 60", 2*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("a sleep: %+v", res)
	}
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !term.WaitPrompt(ctx, 15*time.Second) {
		r, _ := term.Read(0, false)
		t.Fatalf("no prompt after ctrl+c; info %+v, screen:\n%s", term.Info(), strings.Join(r.Screen, "\n"))
	}
	if res := realRun(t, term, "echo after"); res.Output != "after" || res.Status != "0" {
		t.Fatalf("after ctrl+c: %+v", res)
	}
}

func TestARealPowerShellEndsWithItsExitAndWithClose(t *testing.T) {
	shell := realShells(t)[0]
	m, term := realTerminal(t, shell, t.TempDir())
	res, err := term.Run(context.Background(), "exit 7", 30*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if res.Ended == "" {
		t.Errorf("exit: %+v", res)
	}
	if !eventually(20*time.Second, func() bool { return term.Info().State == StateEnded }) {
		t.Fatal("not ended")
	}
	if r := term.Info().Reason; r != "the shell exited with status 7" {
		t.Errorf("reason %q", r)
	}

	again, err := m.Reconnect("c1", term.ID, ByPerson)
	if err != nil {
		t.Fatal(err)
	}
	if !eventually(60*time.Second, func() bool { return again.Info().Integrated }) {
		t.Fatal("no mark after starting again")
	}
	if !m.Remove("c1", again.ID, "closed in the panel") {
		t.Fatal("not removed")
	}
	if !eventually(20*time.Second, func() bool { return again.Info().State == StateEnded }) {
		t.Fatal("not ended by close")
	}
}

func TestAShellThatIsNotThereSaysWhy(t *testing.T) {
	m := NewManager(Options{Local: &LocalShell{Path: filepath.Join(t.TempDir(), "no-such-shell.exe")}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	term, err := m.Open("c1", "PowerShell", ByPerson)
	if err != nil {
		t.Fatal(err)
	}
	if !eventually(10*time.Second, func() bool { return term.Info().State == StateEnded }) {
		t.Fatal("not ended")
	}
	if r := term.Info().Reason; !strings.Contains(r, "could not start") {
		t.Errorf("reason %q", r)
	}
}

// Clear-Host under a real pseudo console: the clear is found in what the
// console sends, the run ends when the prompt after it is drawn, and its
// output is what came after the clear.
func TestARealClearKeepsWhatCameAfter(t *testing.T) {
	for _, shell := range realShells(t) {
		t.Run(shell, func(t *testing.T) {
			_, term := realTerminal(t, shell, t.TempDir())
			realRun(t, term, "1..5 | ForEach-Object { \"before $_\" }")
			start := time.Now()
			res := realRun(t, term, "Clear-Host; Write-Output after-clear; Write-Output second")
			if !res.Finished || res.Status != "0" || res.Output != "after-clear\nsecond" {
				r, _ := term.Read(0, false)
				t.Errorf("%+v; screen:\n%s", res, strings.Join(r.Screen, "\n"))
			}
			if took := time.Since(start); took >= promptWait {
				t.Errorf("the run after a clear took %s, the wait for a prompt not seen", took)
			}
		})
	}
}

// What the person types while a command runs: PowerShell puts what the
// command did not read on the next prompt's line (measured, see
// signalKey), and a run is not typed onto it. A key the command read
// leaves the line empty.
func TestARealPowerShellKeepsWhatWasTypedWhileACommandRan(t *testing.T) {
	setWait(t, &personYield, 300*time.Millisecond)
	for _, shell := range realShells(t) {
		t.Run(shell, func(t *testing.T) {
			_, term := realTerminal(t, shell, t.TempDir())
			prompt := strings.TrimRight(lastRows(readScreen(term), 1)[0], " ")
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			res, err := term.Run(ctx, "Start-Sleep -Seconds 3", time.Second, term.PersonGen())
			if err != nil || !res.TimedOut {
				t.Fatalf("a sleep: %+v %v", res, err)
			}
			term.PersonInput(nil, "echo typed", "")
			if !term.WaitPrompt(ctx, 20*time.Second) {
				t.Fatalf("no prompt after the sleep; info %+v, screen:\n%s", term.Info(), strings.Join(readScreen(term), "\n"))
			}
			typedLine(t, term, "echo typed")
			time.Sleep(2 * personYield)
			if _, err := term.Run(ctx, "echo hi", 10*time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
				t.Fatalf("run onto what was typed while the command ran: %v; screen:\n%s", err, strings.Join(readScreen(term), "\n"))
			}
			for range "echo typed" {
				term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
			}
			typedLine(t, term, prompt)
			if res := runWhenFree(t, term, "echo hi"); strings.TrimSpace(res.Output) != "hi" {
				t.Fatalf("run once the person erased it: %+v", res)
			}

			res, err = term.Run(ctx, "[Console]::ReadKey($true) | Out-Null", time.Second, term.PersonGen())
			if err != nil || !res.TimedOut {
				t.Fatalf("a read of one key: %+v %v", res, err)
			}
			term.PersonInput(nil, "q", "")
			if !term.WaitPrompt(ctx, 20*time.Second) {
				t.Fatalf("no prompt after the key was read; screen:\n%s", strings.Join(readScreen(term), "\n"))
			}
			if res := runWhenFree(t, term, "echo after"); strings.TrimSpace(res.Output) != "after" {
				t.Fatalf("run after a key the command read: %+v", res)
			}
		})
	}
}
