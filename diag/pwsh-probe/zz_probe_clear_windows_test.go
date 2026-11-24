//go:build windows

package sshterm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// What a real pseudo console sends for Clear-Host: logged, not asserted.

type teeProc struct {
	LocalProcess
	mu  sync.Mutex
	buf []byte
}

func (p *teeProc) Read(b []byte) (int, error) {
	n, err := p.LocalProcess.Read(b)
	p.mu.Lock()
	p.buf = append(p.buf, b[:n]...)
	p.mu.Unlock()
	return n, err
}

func (p *teeProc) take() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.buf
	p.buf = nil
	return b
}

func TestProbeWhatClearHostSends(t *testing.T) {
	for _, shell := range realShells(t) {
		for _, cmd := range []string{"Clear-Host; Write-Output after-clear", "cls", "[Console]::Clear(); Write-Output after-console-clear"} {
			t.Run(shell+"/"+cmd, func(t *testing.T) {
				var tee *teeProc
				dir := t.TempDir()
				m := NewManager(Options{Local: &LocalShell{Path: shell, Dir: func(string) string { return dir }, Start: func(argv []string, dir string, cols, rows int) (LocalProcess, error) {
					p, err := startPseudoConsole(argv, dir, cols, rows)
					if err != nil {
						return nil, err
					}
					tee = &teeProc{LocalProcess: p}
					return tee, nil
				}}})
				t.Cleanup(func() { m.CloseAll("probe over", true) })
				term, err := m.Open("c1", "PowerShell", ByModel)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				term.WaitConnected(ctx)
				for i := 0; i < 600 && !term.Info().Integrated; i++ {
					time.Sleep(100 * time.Millisecond)
				}
				realRun(t, term, "1..5 | ForEach-Object { \"before $_\" }")
				time.Sleep(500 * time.Millisecond)
				tee.take()
				res, _ := term.Run(context.Background(), cmd, 10*time.Second, term.PersonGen())
				time.Sleep(500 * time.Millisecond)
				raw := tee.take()
				r, _ := term.Read(0, false)
				t.Logf("PROBE %s %q: output=%q took=%s\nRAW %q\nSCREEN\n%s", shell, cmd, res.Output, res.Took, raw, strings.Join(r.Screen[:8], "\n"))
			})
		}
	}
}
