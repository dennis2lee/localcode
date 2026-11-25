package browser

import (
	"fmt"
	"os"
	"regexp"
	"time"
)

// Measurement only: lives on diag/profile-hold, never on main.

// diagf appends a line to the file LOCALCODE_DIAG_LOG names.
func diagf(format string, args ...any) {
	path := os.Getenv("LOCALCODE_DIAG_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s test-pid=%d %s\n", time.Now().Format("15:04:05.000"), os.Getpid(), fmt.Sprintf(format, args...))
}

var diagType = regexp.MustCompile(`--type=(\S+)`)
var diagSub = regexp.MustCompile(`--utility-sub-type=(\S+)`)

// diagKind is which of a browser's processes a command line is.
func diagKind(cmd string) string {
	kind := "browser"
	if m := diagType.FindStringSubmatch(cmd); m != nil {
		kind = m[1]
	}
	if m := diagSub.FindStringSubmatch(cmd); m != nil {
		kind += "/" + m[1]
	}
	if cmd == "" {
		kind = "?"
	}
	return kind
}

// diagWatch logs when the processes pid started have all gone, and each
// time fewer are left, for up to a minute after the close returned.
func diagWatch(pid int, dir string, closed time.Time, last []string) {
	deadline := closed.Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		now := diagChildren(pid)
		if len(now) != len(last) {
			diagf("children pid=%d dir=%s after-close=%v left=%d %v", pid, dir, time.Since(closed).Round(time.Millisecond), len(now), now)
			last = now
		}
		if len(now) == 0 {
			return
		}
	}
	diagf("children pid=%d dir=%s STILL-THERE after 60s: %v", pid, dir, last)
}
