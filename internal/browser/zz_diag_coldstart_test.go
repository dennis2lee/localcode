package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"localcode/internal/cdp"
	"localcode/internal/childproc"
)

// Measurement only, on diag/cold-start: how long the browser takes to open
// its debugging port and to make its first page, the first time it is
// started on a machine and the times after. start gives up on the port
// after 30 seconds; this waits far longer, to tell a slow start from one
// that never comes.
func TestZZColdStart(t *testing.T) {
	if os.Getenv("LC_PROBE") == "" {
		t.Skip("a probe: set LC_PROBE")
	}
	install, err := Find("")
	if err != nil {
		t.Skip("no Chrome or Edge here")
	}
	fmt.Printf("RESULT browser=%q\n", install.Path)
	for i := 0; i < 6; i++ {
		// Not t.TempDir: a profile the browser still holds fails its
		// removal on Windows, which is another question.
		dir, err := os.MkdirTemp("", "lc-cold-")
		if err != nil {
			t.Fatal(err)
		}
		portFile := filepath.Join(dir, "DevToolsActivePort")
		if err := ownPreferences(filepath.Join(dir, "Default")); err != nil {
			t.Fatal(err)
		}
		// The browser's own log, in the profile directory: what it was
		// doing, by its own clock, if the start turns out slow.
		args := append([]string{"--enable-logging"}, launchArgs(dir, "Default", false)...)
		cmd := exec.Command(install.Path, args...)
		childproc.Hide(cmd)
		childproc.NewGroup(cmd)
		t0 := time.Now()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		portMS, wsURL, gone := int64(-1), "", false
		looked := false
		for time.Since(t0) < 170*time.Second && !gone {
			if url, ok := readPortFile(portFile); ok {
				portMS, wsURL = time.Since(t0).Milliseconds(), url
				break
			}
			if !looked && time.Since(t0) > 12*time.Second {
				// Slow: who has the processor, while it still is.
				looked = true
				busy := exec.Command("sh", "-c", "ps aux --sort=-%cpu | head -20")
				if runtime.GOOS == "windows" {
					busy = exec.Command("powershell", "-NoProfile", "-Command",
						"Get-Process | Sort-Object CPU -Descending | Select-Object -First 20 Name,Id,CPU,WS,StartTime | Format-Table -AutoSize | Out-String -Width 220")
				}
				out, _ := busy.CombinedOutput()
				fmt.Printf("RESULT i=%d slow: no port after 12s; the busiest processes:\n%s\n", i, out)
			}
			select {
			case <-exited:
				gone = true
			case <-time.After(20 * time.Millisecond):
			}
		}
		dialMS, targetMS := int64(-1), int64(-1)
		var conn *cdp.Conn
		if wsURL != "" {
			t1 := time.Now()
			conn, err = cdp.Dial(wsURL)
			if err == nil {
				dialMS = time.Since(t1).Milliseconds()
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				var created struct {
					TargetID string `json:"targetId"`
				}
				t2 := time.Now()
				if err := conn.Call(ctx, "", "Target.createTarget", map[string]any{"url": blankPage, "newWindow": true}, &created); err == nil {
					targetMS = time.Since(t2).Milliseconds()
				} else {
					fmt.Printf("RESULT i=%d createTarget: %v after %dms\n", i, err, time.Since(t2).Milliseconds())
				}
				cancel()
			} else {
				fmt.Printf("RESULT i=%d dial: %v\n", i, err)
			}
		}
		fmt.Printf("RESULT i=%d port_ms=%d dial_ms=%d target_ms=%d exited_early=%v total_ms=%d started_at=%s\n",
			i, portMS, dialMS, targetMS, gone, time.Since(t0).Milliseconds(), t0.UTC().Format("15:04:05.000"))
		if portMS < 0 || portMS > 12000 {
			log, _ := os.ReadFile(filepath.Join(dir, "chrome_debug.log"))
			if len(log) > 20000 {
				log = log[:20000]
			}
			fmt.Printf("RESULT i=%d slow: the browser's log:\n%s\n", i, log)
		}
		if conn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = conn.Call(ctx, "", "Browser.close", nil, nil)
			cancel()
			_ = conn.Close()
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = childproc.KillGroup(cmd)
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
			}
		}
		_ = os.RemoveAll(dir)
	}
}
