package dialog

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestAppleScriptString(t *testing.T) {
	cases := map[string]string{
		`/tmp/plain`:         `"/tmp/plain"`,
		`/tmp/with "quote"`:  `"/tmp/with \"quote\""`,
		`/tmp/back\slash`:    `"/tmp/back\\slash"`,
		`/tmp/both\ and "q"`: `"/tmp/both\\ and \"q\""`,
	}
	for in, want := range cases {
		if got := appleScriptString(in); got != want {
			t.Errorf("appleScriptString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestCleanPath(t *testing.T) {
	// AppleScript's "POSIX path of" always appends a slash for a directory;
	// every caller here expects paths without one.
	got, err := cleanPath("/Users/me/projects/app/\n")
	if err != nil {
		t.Fatalf("cleanPath: %v", err)
	}
	if got != "/Users/me/projects/app" {
		t.Errorf("cleanPath = %q, want the trailing slash and newline gone", got)
	}

	// Root is the one path whose trailing slash is the path.
	if got, err := cleanPath("/"); err != nil || got != "/" {
		t.Errorf("cleanPath(\"/\") = %q, %v; want \"/\", nil", got, err)
	}

	// A helper that prints nothing means nothing was chosen.
	if _, err := cleanPath("  \n"); !errors.Is(err, ErrCancelled) {
		t.Errorf("cleanPath on empty output = %v, want ErrCancelled", err)
	}
}

// TestPickDirectoryContextCancel opens a real folder picker and tears it
// down through the context, which is the path the daemon relies on when a
// client disconnects mid-dialog. It puts a window on screen, so it only
// runs when explicitly asked for:
//
//	LOCALCODE_DIALOG_TEST=1 go test ./internal/dialog/
func TestPickDirectoryContextCancel(t *testing.T) {
	if os.Getenv("LOCALCODE_DIALOG_TEST") == "" {
		t.Skip("opens a real dialog window; set LOCALCODE_DIALOG_TEST=1 to run")
	}
	if !Available() {
		t.Skipf("no folder picker available on %s", runtime.GOOS)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	if _, err := PickDirectory(ctx, "test: this should close itself", ""); err == nil {
		t.Fatal("PickDirectory returned a path, but nobody chose one")
	}
	// The point is that it came back at all: a picker that ignored the
	// context would block here until a human closed the window.
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("PickDirectory took %v to honour a 2s context", elapsed)
	}
}
