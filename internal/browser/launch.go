package browser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"localcode/internal/cdp"
	"localcode/internal/childproc"
)

// The page size tabs render at. The width is fixed, so a page is laid out
// the same at any panel size, and a point the model read off a screenshot
// is where it was after the panel is resized. The height follows the
// panel: a conversation's tabs are as tall as fills its panel's frame at
// that width (Manager.SetHeight), from MinViewportHeight to
// MaxViewportHeight, and ViewportHeight until a panel says. A screenshot,
// the coordinates the model clicks at and the frames the panel shows are
// all in the page's own CSS pixels.
const (
	ViewportWidth     = 1280
	ViewportHeight    = 800
	MinViewportHeight = 320
	MaxViewportHeight = 2560
)

// HeightFor is the page height that fills a box w wide and h tall (in
// any one unit) at ViewportWidth, held to MinViewportHeight and
// MaxViewportHeight. ViewportHeight for a box with no size.
func HeightFor(w, h float64) int {
	if !(w > 0) || !(h > 0) || math.IsInf(w, 0) || math.IsInf(h, 0) {
		return ViewportHeight
	}
	return clampHeight(math.Round(ViewportWidth * h / w))
}

func clampHeight(h float64) int {
	return int(math.Max(MinViewportHeight, math.Min(MaxViewportHeight, h)))
}

// process is one running browser and the connection to it.
type process struct {
	cmd       *exec.Cmd
	conn      *cdp.Conn
	dataDir   string
	exited    chan struct{}
	userAgent string // what every target is told to send; see guard

	// guarded is, for each page, the session the guard set up: see
	// Manager.attached.
	guardMu sync.Mutex
	guarded map[string]string
}

// launchArgs are the browser's command line. No window (the panel is the
// window), the profile localcode keeps, and a debugging port the browser
// picks and writes to DevToolsActivePort in the profile directory.
//
// imported is whether the profile came from the person's own browser. One
// that did needs the system's secret store, where the key its cookies and
// passwords are encrypted with lives. One that did not is better off
// without it: on macOS a browser on a new profile asks the keychain for a
// key it does not have, and waits on a dialog nobody may be there to
// answer, with every page load behind it. Such a profile keeps its own
// key in a store of Chrome's own instead.
func launchArgs(dataDir, profile string, imported bool) []string {
	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + dataDir,
		"--profile-directory=" + profile,
		"--headless=new",
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-crash-restore-bubble",
		// Both in one flag: the browser takes the last one it is given.
		//
		// BackgroundFetch: a page can have the browser download a URL in
		// the background with no page to watch: the browser makes that
		// request itself, outside the guard and without its user agent.
		//
		// MacAppCodeSignClone: on macOS a browser makes a copy of its own
		// application at every start, a hard link to its program in a
		// folder under the system's temporary files, and removes it when
		// it quits. A browser that starts while another is making or
		// removing its copy can be given the copy's path as its own: it
		// finds no framework beside it and is aborted before it listens
		// ("Library not loaded", by the system's crash report). Measured
		// with Chrome 158, the package's tests run twice side by side as
		// the gate runs them: a browser was lost that way in three runs
		// of four, and with the copy off in none of four. A browser that
		// is killed, not quit, also left its copy there for good.
		"--disable-features=BackgroundFetch,MacAppCodeSignClone",
		fmt.Sprintf("--window-size=%d,%d", ViewportWidth, ViewportHeight),
		// Every conversation's tab is in a window of its own, and all but
		// one are behind another. A browser slows, and under memory
		// pressure stops, a page whose window is hidden: on a loaded
		// machine a click whose new tab covered its page never finished,
		// and a page opened beside another stopped answering. Every tab
		// is one the model or the person may be working in.
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-background-timer-throttling",
		// The copied profile may be signed in to the person's browser
		// account. The model's browsing is not synced to it.
		"--disable-sync",
	}
	if !imported {
		switch runtime.GOOS {
		case "darwin":
			args = append(args, "--use-mock-keychain")
		case "linux":
			args = append(args, "--password-store=basic")
		}
	}
	return append(args, "about:blank")
}

// ownerFile names the process whose browser runs on the profile.
const ownerFile = "localcode-owner"

// ErrInUse is a browser on this profile that a live localcode is using:
// another desktop window's, which every window's daemon shares the
// profile with.
type ErrInUse struct{ PID int }

func (e ErrInUse) Error() string {
	return fmt.Sprintf("the browser is in use by another localcode (process %d); close that window, or use the browser there", e.PID)
}

// errStartStopped is a start called off by a close or a profile copy.
var errStartStopped = errors.New("the browser was closed while it started")

// start launches the browser at path on dataDir and connects to it, by
// ctx's deadline (see startFor).
func start(ctx context.Context, path, dataDir, profile string, imported bool) (*process, error) {
	portFile := filepath.Join(dataDir, "DevToolsActivePort")
	// A browser on this profile that a live localcode is using is that
	// localcode's: closing it would take its conversations' tabs. Only
	// one whose owner is gone, or that nobody claimed, is left over.
	if data, err := os.ReadFile(filepath.Join(dataDir, ownerFile)); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid != os.Getpid() && pidAlive(pid) {
			if _, ok := readPortFile(portFile); ok {
				return nil, ErrInUse{PID: pid}
			}
		}
	}
	closeStale(portFile)
	// Left by an earlier run, it names a port nobody is listening on.
	_ = os.Remove(portFile)
	if err := ownPreferences(filepath.Join(dataDir, profile)); err != nil {
		return nil, fmt.Errorf("set up the profile: %w", err)
	}

	cmd := exec.Command(path, launchArgs(dataDir, profile, imported)...)
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
		select {
		case <-p.exited:
			// How it ended is the only word there is on why: the browser
			// says nothing else of an exit before it listens.
			err = fmt.Errorf("%w (%v)", err, cmd.ProcessState)
		default:
		}
		p.kill()
		return nil, err
	}
	// By the start's own deadline: a first start measured up to 7.9s
	// here, against the 10s an ordinary dial allows.
	within := cdp.DialTimeoutDefault
	if deadline, ok := ctx.Deadline(); ok {
		within = max(time.Until(deadline), time.Second)
	}
	conn, err := cdp.DialTimeout(wsURL, within)
	if err != nil {
		p.kill()
		return nil, fmt.Errorf("connect to the browser: %w", err)
	}
	p.conn = conn
	_ = os.WriteFile(filepath.Join(dataDir, ownerFile), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	return p, nil
}

// waitForPort waits for the browser to write DevToolsActivePort: the port
// on its first line, the browser's endpoint path on its second. Until
// ctx ends, which is the start's own bound (startFor), not a caller's.
func waitForPort(ctx context.Context, portFile string, exited <-chan struct{}) (string, error) {
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
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("the browser did not open its debugging port within %s", startFor)
			}
			return "", errStartStopped
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

// ownPreferences sets, in the profile's Preferences before the browser
// reads them, the two settings this browser cannot take from the person's
// own. It runs at every start, so a profile copied by an earlier version
// is put right too.
//
// "Preload pages" is turned off. With it on, a page can have the browser
// fetch another page ahead of a click (link prefetch, speculation rules,
// prerender). The browser makes those requests itself, outside what the
// guard can see, so a page could have a denied site fetched with the
// profile's cookies.
//
// The page zoom is taken out: the default one, and each site's own. The
// panel and the model address a page by a point in the 1280x800 picture
// of it, and the browser takes a point in the page's own layout pixels.
// The two are the same only at 100%. A profile copied from a browser set
// to 125% laid every page out 1024 pixels wide, drawn at 1280, and every
// click landed a quarter further from the corner than it was aimed: a
// click on a field reached the empty page beside it, and the typing after
// it went nowhere (measured, with Chrome and Edge, on macOS and Windows).
func ownPreferences(profileDir string) error {
	path := filepath.Join(profileDir, "Preferences")
	prefs := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		// Numbers kept as written: the file holds times in microseconds,
		// past what a float64 carries exactly.
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		// A file that is not an object, "null" included, is started
		// over: the browser rewrites the rest on its first run.
		if dec.Decode(&prefs) != nil || prefs == nil {
			prefs = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	net, _ := prefs["net"].(map[string]any)
	if net == nil {
		net = map[string]any{}
	}
	// 2 is "never" (chrome::NetworkPredictionOptions).
	net["network_prediction_options"] = 2
	prefs["net"] = net
	// Zoom levels are kept by storage partition under these two keys;
	// with neither, every page is at 100%.
	if partition, _ := prefs["partition"].(map[string]any); partition != nil {
		delete(partition, "default_zoom_level")
		delete(partition, "per_host_zoom_levels")
	}
	data, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
