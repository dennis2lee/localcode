package browser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"localcode/internal/cdp"
	"localcode/internal/childproc"
)

// Viewport is the page size every tab renders at. Fixed, so a screenshot,
// the coordinates the model clicks at and the frames the panel shows are
// all in one frame of reference.
const (
	ViewportWidth  = 1280
	ViewportHeight = 800
)

// process is one running browser and the connection to it.
type process struct {
	cmd     *exec.Cmd
	conn    *cdp.Conn
	dataDir string
	exited  chan struct{}
}

// launchArgs are the browser's command line. No window (the panel is the
// window), the profile localcode keeps, and a debugging port the browser
// picks and writes to DevToolsActivePort in the profile directory.
func launchArgs(dataDir, profile string) []string {
	return []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + dataDir,
		"--profile-directory=" + profile,
		"--headless=new",
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-crash-restore-bubble",
		fmt.Sprintf("--window-size=%d,%d", ViewportWidth, ViewportHeight),
		"about:blank",
	}
}

// start launches the browser at path on dataDir and connects to it.
func start(ctx context.Context, path, dataDir, profile string) (*process, error) {
	portFile := filepath.Join(dataDir, "DevToolsActivePort")
	closeStale(portFile)
	// Left by an earlier run, it names a port nobody is listening on.
	_ = os.Remove(portFile)

	cmd := exec.Command(path, launchArgs(dataDir, profile)...)
	childproc.Hide(cmd)
	childproc.NewGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", filepath.Base(path), err)
	}
	p := &process{cmd: cmd, dataDir: dataDir, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()

	wsURL, err := waitForPort(ctx, portFile, p.exited)
	if err != nil {
		p.kill()
		return nil, err
	}
	conn, err := cdp.Dial(wsURL)
	if err != nil {
		p.kill()
		return nil, fmt.Errorf("connect to the browser: %w", err)
	}
	p.conn = conn
	return p, nil
}

// waitForPort waits for the browser to write DevToolsActivePort: the port
// on its first line, the browser's endpoint path on its second.
func waitForPort(ctx context.Context, portFile string, exited <-chan struct{}) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if url, ok := readPortFile(portFile); ok {
			return url, nil
		}
		select {
		case <-exited:
			// The usual reason: a browser from an earlier run still has
			// this profile open, and the new one handed itself over to it.
			return "", errors.New("the browser exited at once; if another browser is running on ~/.localcode/browser, close it and try again")
		case <-ctx.Done():
			return "", errors.New("the browser did not open its debugging port within 30s")
		case <-tick.C:
		}
	}
}

// closeStale asks a browser an earlier run left on this profile to quit.
//
// A daemon that was killed, or crashed, never closed its browser: the
// browser runs in a process group of its own and does not die with it.
// It keeps the profile locked, and a new one started on the same profile
// hands itself over to it and exits, so nothing would work until somebody
// found and killed it by hand. Its DevToolsActivePort is still in the
// profile directory, and the browser listening there is the one to close:
// over its own protocol, which is the same call on every platform.
func closeStale(portFile string) {
	url, ok := readPortFile(portFile)
	if !ok {
		return
	}
	conn, err := cdp.Dial(url)
	if err != nil {
		return // nobody there: the file is all that is left
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = conn.Call(ctx, "", "Browser.close", nil, nil)
	cancel()
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
	}
	_ = conn.Close()
	// The profile lock goes with the process; give it a moment.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := cdp.Dial(url); err != nil {
			return
		} else {
			c.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func readPortFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		lines = append(lines, strings.TrimSpace(sc.Text()))
	}
	if len(lines) < 2 || lines[0] == "" || !strings.HasPrefix(lines[1], "/") {
		return "", false
	}
	return "ws://127.0.0.1:" + lines[0] + lines[1], true
}

// close asks the browser to quit and kills it if it has not within a few
// seconds.
func (p *process) close() {
	if p.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.conn.Call(ctx, "", "Browser.close", nil, nil)
		cancel()
		_ = p.conn.Close()
	}
	select {
	case <-p.exited:
	case <-time.After(3 * time.Second):
		p.kill()
	}
}

func (p *process) kill() {
	_ = childproc.KillGroup(p.cmd)
	select {
	case <-p.exited:
	case <-time.After(3 * time.Second):
	}
}
