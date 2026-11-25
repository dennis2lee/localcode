// Package browsertest is what the tests that start a real browser share:
// the profile directory the browser runs on, and its removal.
//
// It imports nothing of localcode's, so the browser package's own tests
// can use it as the daemon's and the agent's do.
package browsertest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ProfileNameFile is the file in a profile directory that names the
// profile the browser runs on. With it there the browser package copies
// nothing in from the person's own browser. It is that package's
// profileNameFile, and a test there holds the two to one name.
const ProfileNameFile = "localcode-profile"

// removeBound is how long a profile directory is given to come free.
const removeBound = 60 * time.Second // diag: twice the bound, to see how long a hold lasts

// ProfileDir makes the profile directory for a browser the test starts,
// and removes it when the test ends.
//
// It is never the person's own profile, nor a copy of it: a new directory
// with the profile's name already chosen, which is what stops the import.
//
// Call it before the browser is made, so the cleanup that closes the
// browser, registered after this one, runs before it.
//
// The directory is not t.TempDir(), because of how that one goes. On
// Windows a file another process has open cannot be removed, and
// t.TempDir fails the test when its directory is still held two seconds
// after the test's own cleanups. Closing the browser waits for the one
// process that was started. A browser is several, and the rest end when
// they notice it has gone, each in its own time. So the removal here
// waits for them, for a time no browser that is going needs, and fails
// only when the directory is still held at the end of it: that is a
// browser, or a part of one, the test left running, and the failure names
// the processes holding it where the system can tell.
func ProfileDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "localcode-browser-profile-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		diagTest = t.Name()
		r, err := removeWithin(dir, removeBound, os.RemoveAll, holders)
		diagf("removed dir=%s test=%s waited=%v ms=%d err=%v", dir, diagTest, r.waited.Round(time.Millisecond), r.waited.Milliseconds(), err)
		switch {
		case err != nil:
			t.Errorf("the browser's profile directory was not removed: %v", err)
		case r.waited > 0:
			t.Logf("the browser's profile directory came free %v after the first try at removing it (%v); held then by: %s",
				r.waited.Round(time.Millisecond), r.first, orNobody(r.held))
		}
	})
	if err := os.WriteFile(filepath.Join(dir, ProfileNameFile), []byte("Default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// removal is how removing a directory went.
type removal struct {
	waited time.Duration // from the first try to the one that removed it; zero when the first did
	first  error         // what the first try said, when it failed
	held   []string      // the processes holding a file in it then, where the system can tell
}

// removeWithin removes dir, trying again for as long as bound while
// something still holds it. The error after that says what is left in
// the directory and who holds it.
//
// remove and holders are os.RemoveAll and this platform's holders, and a
// test's own.
func removeWithin(dir string, bound time.Duration, remove func(string) error, holders func([]string) []string) (removal, error) {
	var r removal
	start := time.Now()
	pause := 5 * time.Millisecond
	for {
		err := remove(dir)
		if err == nil {
			if r.first != nil {
				r.waited = time.Since(start)
			}
			return r, nil
		}
		// diag: who holds it at every try, not only the first.
		who := holders(left(dir))
		diagf("held dir=%s test=%s t=%v err=%v holders=%q", dir, diagTest, time.Since(start).Round(time.Millisecond), err, who)
		if r.first == nil {
			r.first = err
			r.held = who
		}
		if time.Since(start) >= bound {
			files := left(dir)
			return r, fmt.Errorf("%w, still after %v; left in it: %s; held by: %s; held at the first try by: %s",
				err, bound, orNothing(relative(dir, files)), orNobody(holders(files)), orNobody(r.held))
		}
		time.Sleep(pause)
		if pause < 200*time.Millisecond {
			pause *= 2
		}
	}
}

// maxLeft bounds how many of the files left in a directory are listed and
// asked about: enough to name every holder of a profile, without a
// message a screen long.
const maxLeft = 40

// left is the files still in dir, at most maxLeft of them.
func left(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // going as it is walked
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		if len(files) == maxLeft {
			return fs.SkipAll
		}
		return nil
	})
	return files
}

func relative(dir string, files []string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f
		if rel, err := filepath.Rel(dir, f); err == nil {
			out[i] = rel
		}
	}
	return out
}

func orNobody(who []string) string {
	if len(who) == 0 {
		return "no process the system names"
	}
	return strings.Join(who, "; ")
}

func orNothing(files []string) string {
	if len(files) == 0 {
		return "no file"
	}
	return strings.Join(files, ", ")
}

// Measurement only: lives on diag/profile-hold, never on main.
var diagTest string

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
