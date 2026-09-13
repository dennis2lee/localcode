package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The two comparison pages name the release they describe, and it has to be
// the newest one.
//
// Nothing guarded them, and they drifted sixteen releases: both were stamped
// v0.105.1 while the build was on v0.121.0, so a page whose entire job is
// saying how this build differs was describing a build nobody runs. The pages
// are hand-written prose and no test can know whether their content is
// current — but the stamp is a fact, and a stale stamp is the signal that the
// prose behind it is stale too.
//
// Deliberately not a check that the content changed. A release that touches
// nothing these pages discuss should still restamp them, because the claim
// the stamp makes is "this describes v0.121.0", and leaving it at an older
// version to avoid a lie makes a different one.
func TestTheComparisonPagesNameTheCurrentRelease(t *testing.T) {
	root := repoRootFromDaemon(t)
	want := newestChangelogVersion(t, filepath.Join(root, "docs", "CHANGELOG.md"))

	for _, name := range []string{
		"where-localcode-differs.html",
		"where-localcode-differs.ko.html",
	} {
		path := filepath.Join(root, "docs", name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// The stamp in the page header, and only that.
		//
		// Each page names the version in five places — the header, two
		// sentences about what was checked against what, a caveat, and
		// the footer — and it also names older versions in prose, as in
		// "shipped in v0.83.0", which are history and must not be
		// rewritten. An alt attribute names whatever version was on
		// screen when the screenshot was taken, which would be wrong to
		// change for the same reason. The header stamp is the one field
		// whose whole meaning is "this describes that release", so it is
		// the one worth pinning.
		m := regexp.MustCompile(`class="meta">\s*version[^0-9]*([0-9]+\.[0-9]+\.[0-9]+)`).FindStringSubmatch(string(body))
		if m == nil {
			t.Errorf("%s has no version in its header stamp — the header changed shape, and this guard has to be taught the new one rather than quietly passing", name)
			continue
		}
		if m[1] != want {
			t.Errorf("%s is stamped %s and the newest release is %s — restamp it, and read what it says while you are there",
				name, m[1], want)
		}
	}
}

// newestChangelogVersion is the version in the first "## vX.Y.Z" heading.
func newestChangelogVersion(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read changelog: %v", err)
	}
	m := regexp.MustCompile(`(?m)^## v([0-9]+\.[0-9]+\.[0-9]+)`).FindStringSubmatch(string(body))
	if m == nil {
		t.Fatal("no version heading in docs/CHANGELOG.md")
	}
	return m[1]
}

// repoRootFromDaemon walks up to the directory holding go.mod.
func repoRootFromDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
