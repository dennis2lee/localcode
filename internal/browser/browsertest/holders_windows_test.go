//go:build windows

package browsertest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openIn is a file in a new directory under the temporary directory,
// spelled as the system spells it (short names and all), and held open by
// this process: what a browser's database is to a test removing its
// profile.
func openIn(t *testing.T) (dir string, f *os.File) {
	t.Helper()
	dir, err := os.MkdirTemp("", "localcode-browser-profile-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err = os.Create(filepath.Join(dir, "Default", "DIPS"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return dir, f
}

// me is how holders names this process.
func me() string { return fmt.Sprintf("process %d (", os.Getpid()) }

// The process that has a file open is named, with the command line it was
// started with. This is the premise of every name a failure gives.
func TestTheProcessHoldingAFileIsNamed(t *testing.T) {
	dir, f := openIn(t)
	if err := os.RemoveAll(dir); err == nil {
		t.Fatal("a directory with an open file in it was removed; the test is not testing a held file")
	}
	who := holders([]string{f.Name()})
	found := false
	for _, w := range who {
		if strings.HasPrefix(w, me()) {
			found = true
			// After the name Windows has for the program, the command line.
			_, cmd, _ := strings.Cut(w, "): ")
			if exe := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"); !strings.Contains(cmd, exe) {
				t.Errorf("this process is named without its command line (%s): %s", exe, w)
			}
		}
	}
	if !found {
		t.Fatalf("holders = %q, want this process (%d) among them", who, os.Getpid())
	}
	f.Close()
	if who := holders([]string{f.Name()}); len(who) != 0 {
		t.Errorf("a file nobody has open is held by %q", who)
	}
}

// A file that is let go while the removal waits: the directory goes, and
// the holder at the first try is known.
func TestAFileLetGoLateIsWaitedForOnWindows(t *testing.T) {
	dir, f := openIn(t)
	go func() {
		time.Sleep(400 * time.Millisecond)
		f.Close()
	}()
	r, err := removeWithin(dir, 30*time.Second, os.RemoveAll, holders)
	if err != nil {
		t.Fatalf("the directory was not removed once its file was closed: %v", err)
	}
	if r.waited < 300*time.Millisecond || r.first == nil {
		t.Errorf("removal = %+v, want a wait for the file to close", r)
	}
	if len(r.held) == 0 || !strings.HasPrefix(r.held[0], me()) {
		t.Errorf("the holder at the first try = %q, want this process", r.held)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory is still there: %v", err)
	}
}

// A file never let go: the failure names the file and this process.
func TestAFileNeverLetGoFailsAndNamesItsHolderOnWindows(t *testing.T) {
	dir, _ := openIn(t)
	_, err := removeWithin(dir, 300*time.Millisecond, os.RemoveAll, holders)
	if err == nil {
		t.Fatal("a directory with an open file in it was reported removed")
	}
	for _, want := range []string{"DIPS", "held by: " + me()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure lacks %q: %v", want, err)
		}
	}
}
