package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		cmd := exec.Command(install.Path, launchArgs(dir, "Default", false)...)
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
		for time.Since(t0) < 170*time.Second && !gone {
			if url, ok := readPortFile(portFile); ok {
				portMS, wsURL = time.Since(t0).Milliseconds(), url
				break
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
