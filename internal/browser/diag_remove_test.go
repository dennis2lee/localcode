package browser

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9]+`)

// profileDir is a directory for a test's browser profile. Diagnostic
// branch only: its removal is measured, with who holds what is refused.
func profileDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", unsafeName.ReplaceAllString(t.Name(), "_")+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		start := time.Now()
		last := ""
		asked := false
		for {
			err := os.RemoveAll(dir)
			if err == nil {
				if last != "" {
					diagf("REMOVED test=%s after=%v dir=%s", t.Name(), time.Since(start).Round(time.Millisecond), dir)
				}
				return
			}
			if msg := err.Error(); msg != last {
				last = msg
				file := dir
				var pe *fs.PathError
				if errors.As(err, &pe) {
					file = pe.Path
					if !filepath.IsAbs(file) {
						file = filepath.Join(filepath.Dir(dir), file)
					}
				}
				diagf("REMOVE-REFUSED test=%s at=%v err=%q holders=%s browsers=%s", t.Name(), time.Since(start).Round(time.Millisecond), msg, holders(file), processTable())
				if !asked {
					asked = true
					go func() { diagf("COMMANDS test=%s %s", t.Name(), commandLines()) }()
				}
			}
			if time.Since(start) > 90*time.Second {
				t.Errorf("the profile is still not removed after %v: %v", time.Since(start).Round(time.Second), err)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return dir
}
