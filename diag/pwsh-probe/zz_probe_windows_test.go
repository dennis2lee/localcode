//go:build windows

package sshterm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Measurements for the review of the PowerShell panel, not assertions:
// each logs what the real console did.

// What a key sent while a command runs does to text typed ahead of it:
// is the command stopped, and is the text on the next prompt's line?
func TestProbeTypeaheadAfterAKey(t *testing.T) {
	for _, shell := range realShells(t) {
		for _, key := range []string{"ctrl+c", "ctrl+\\", "ctrl+z"} {
			t.Run(shell+"/"+key, func(t *testing.T) {
				_, term := realTerminal(t, shell, t.TempDir())
				res, err := term.Run(context.Background(), "Start-Sleep -Seconds 4", 300*time.Millisecond, term.PersonGen())
				if err != nil || !res.TimedOut {
					t.Fatalf("sleep: %+v %v", res, err)
				}
				if err := term.Type("Write-Output buffered", false, term.PersonGen()); err != nil {
					t.Fatalf("type: %v", err)
				}
				time.Sleep(300 * time.Millisecond)
				start := time.Now()
				if err := term.Keys(key, term.PersonGen()); err != nil {
					t.Fatalf("key: %v", err)
				}
				for time.Since(start) < 8*time.Second && term.Info().Running {
					time.Sleep(50 * time.Millisecond)
				}
				ended := time.Since(start)
				time.Sleep(2 * time.Second)
				r, _ := term.Read(0, false)
				tail := r.Screen
				for len(tail) > 0 && strings.TrimSpace(tail[len(tail)-1]) == "" {
					tail = tail[:len(tail)-1]
				}
				if len(tail) > 6 {
					tail = tail[len(tail)-6:]
				}
				t.Logf("PROBE %s %s: running ended after %s (still running: %v); cursor line %q; buffered text on the screen after: %v\n%s",
					shell, key, ended.Round(10*time.Millisecond), term.Info().Running, term.cursorLineLocked(), strings.Contains(strings.Join(r.Screen, "\n"), "buffered"), strings.Join(tail, "\n"))
			})
		}
	}
}

// A native program exiting with the same nonzero code twice.
func TestProbeRepeatedNativeExitCode(t *testing.T) {
	for _, shell := range realShells(t) {
		t.Run(shell, func(t *testing.T) {
			_, term := realTerminal(t, shell, t.TempDir())
			for _, cmd := range []string{"cmd /c exit 7", "cmd /c exit 7", "Get-Item C:\\nope-probe", "cmd /c exit 0", "cmd /c exit 7"} {
				res := realRun(t, term, cmd)
				t.Logf("PROBE %s %q: status=%q finished=%v", shell, cmd, res.Status, res.Finished)
			}
		})
	}
}

// Output with no line end before the prompt.
func TestProbeNoNewlineBeforeThePrompt(t *testing.T) {
	for _, shell := range realShells(t) {
		t.Run(shell, func(t *testing.T) {
			_, term := realTerminal(t, shell, t.TempDir())
			start := time.Now()
			res := realRun(t, term, "Write-Host -NoNewline hello-probe")
			r, _ := term.Read(0, false)
			t.Logf("PROBE %s: output=%q status=%q took=%s; cursor line %q", shell, res.Output, res.Status, time.Since(start).Round(10*time.Millisecond), term.cursorLineLocked())
			_ = r
		})
	}
}

func (t *Terminal) cursorLineLocked() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cursorLine()
}
