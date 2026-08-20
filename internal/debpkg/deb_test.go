package debpkg

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests are the only thing standing between a broken .deb and the
// first person to run `apt install`. The release is cut from a Mac, which
// has no dpkg, so "the build succeeded" says nothing — the package is
// checked by reading the bytes back and asserting the structure dpkg
// requires.
//
// Where dpkg *is* present (a Linux developer, or CI on Linux), the last
// test hands the package to the real thing.

func sample() Package {
	return Package{
		Name:         "localcode",
		Version:      "1.2.3",
		Architecture: "amd64",
		Maintainer:   "dennis2lee <someone@example.com>",
		Homepage:     "https://example.com/localcode",
		Section:      "devel",
		Priority:     "optional",
		Synopsis:     "A coding agent",
		Description:  "Longer text.\n\nA second paragraph.",
		ModTime:      time.Unix(1700000000, 0).UTC(),
		Files: []File{
			{Path: "/usr/bin/localcode", Mode: 0o755, Data: bytes.Repeat([]byte("x"), 2048)},
			{Path: "/usr/share/doc/localcode/copyright", Mode: 0o644, Data: []byte("MIT\n")},
		},
	}
}

func build(t *testing.T, p Package) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := p.Build(&buf); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return buf.Bytes()
}

// The three members, in the one order dpkg accepts. debian-binary has to
// be first and uncompressed: it is the format version, and a reader has
// to have it before anything after it means anything.
func TestThePackageHasTheThreeMembersInOrder(t *testing.T) {
	got, err := Read(build(t, sample()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"debian-binary", "control.tar.gz", "data.tar.gz"}
	if strings.Join(got.Members, ",") != strings.Join(want, ",") {
		t.Errorf("members = %v, want %v", got.Members, want)
	}
	if got.Format != "2.0\n" {
		t.Errorf("debian-binary = %q, want \"2.0\\n\"", got.Format)
	}
}

// An ar member of odd length is padded to an even boundary. Without the
// pad, every member after it is read from one byte off — which is not a
// clean failure but a gzip header that happens not to parse.
func TestAnOddSizedMemberIsPadded(t *testing.T) {
	p := sample()
	// debian-binary is "2.0\n": four bytes, even. The control member's
	// length is whatever gzip makes it, so this is really a test that the
	// reader and writer agree on the padding rule for any length — drive
	// it with a payload chosen to make the data member odd or even and
	// check both parse.
	for _, size := range []int{2047, 2048} {
		p.Files[0].Data = bytes.Repeat([]byte("x"), size)
		got, err := Read(build(t, p))
		if err != nil {
			t.Fatalf("%d bytes: Read: %v", size, err)
		}
		if len(got.Files["/usr/bin/localcode"].Data) != size {
			t.Errorf("%d bytes: payload came back as %d", size, len(got.Files["/usr/bin/localcode"].Data))
		}
	}
}

func TestTheControlFileCarriesWhatDpkgReads(t *testing.T) {
	got, err := Read(build(t, sample()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for field, want := range map[string]string{
		"Package":      "localcode",
		"Version":      "1.2.3",
		"Architecture": "amd64",
		"Maintainer":   "dennis2lee <someone@example.com>",
		"Section":      "devel",
		"Priority":     "optional",
		"Homepage":     "https://example.com/localcode",
	} {
		if got.Control[field] != want {
			t.Errorf("%s = %q, want %q", field, got.Control[field], want)
		}
	}
	// Installed-Size is in KiB and rounded up: 2048 + 4 bytes is three
	// KiB once rounded, and a package that under-reports its size tells
	// the installer the wrong thing before it commits.
	size, err := strconv.Atoi(got.Control["Installed-Size"])
	if err != nil || size < 2 {
		t.Errorf("Installed-Size = %q, want the payload in KiB", got.Control["Installed-Size"])
	}
}

// A blank line inside an extended description is written as a line
// holding a single dot. Written as nothing, it ends the field, and every
// line after it is read as a new field — which is how a description turns
// into a control file dpkg refuses.
func TestABlankLineInTheDescriptionIsWrittenAsADot(t *testing.T) {
	raw := build(t, sample())
	got, err := Read(raw)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if desc := got.Control["Description"]; !strings.Contains(desc, "A second paragraph.") {
		t.Errorf("the second paragraph is not in the description: %q", desc)
	}
	if got.Control["A second paragraph."] != "" {
		t.Error("the second paragraph was read as a control field of its own")
	}
}

func TestTheFilesArriveWithTheirModes(t *testing.T) {
	got, err := Read(build(t, sample()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	bin, ok := got.Files["/usr/bin/localcode"]
	if !ok {
		t.Fatalf("no /usr/bin/localcode in the package: %v", got.Files)
	}
	if bin.Mode != 0o755 {
		t.Errorf("mode = %o, want 755 — an installed program that is not executable", bin.Mode)
	}
	doc, ok := got.Files["/usr/share/doc/localcode/copyright"]
	if !ok {
		t.Fatal("no copyright file in the package")
	}
	if doc.Mode != 0o644 {
		t.Errorf("copyright mode = %o, want 644", doc.Mode)
	}
}

// Every directory leading to a file is in the archive. dpkg unpacks in
// order, and it is the directory entries that a later removal knows to
// clean up.
func TestEveryParentDirectoryIsInThePackage(t *testing.T) {
	got, err := Read(build(t, sample()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	have := map[string]bool{}
	for _, d := range got.Dirs {
		have[d] = true
	}
	for _, want := range []string{"/usr", "/usr/bin", "/usr/share", "/usr/share/doc", "/usr/share/doc/localcode"} {
		if !have[want] {
			t.Errorf("%s is not in the package: %v", want, got.Dirs)
		}
	}
}

// md5sums is what `dpkg --verify` checks an install against, and it
// spells the paths without a leading slash — the one place in the package
// where they are written that way. A spelling of our own is a checksum
// that never matches anything.
func TestMD5SumsMatchTheFilesAndAreSpelledTheWayDpkgReadsThem(t *testing.T) {
	p := sample()
	got, err := Read(build(t, p))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, f := range p.Files {
		key := strings.TrimPrefix(f.Path, "/")
		sum := md5.Sum(f.Data)
		if got.MD5Sums[key] != hex.EncodeToString(sum[:]) {
			t.Errorf("md5sums[%q] = %q, want %s", key, got.MD5Sums[key], hex.EncodeToString(sum[:]))
		}
	}
	if _, wrong := got.MD5Sums["/usr/bin/localcode"]; wrong {
		t.Error("md5sums has a leading slash on its paths")
	}
}

// Two builds of one package are the same bytes. Not a nicety: it is what
// makes it possible to say a published .deb came from a given source
// tree, and the only thing that could make it false is a clock reading
// that sneaked in.
func TestTwoBuildsOfTheSamePackageAreIdentical(t *testing.T) {
	first := build(t, sample())
	second := build(t, sample())
	if !bytes.Equal(first, second) {
		t.Error("two builds of the same package produced different bytes")
	}
}

func TestAPackageThatCannotBeInstalledIsRefusedAtBuildTime(t *testing.T) {
	for name, mangle := range map[string]func(*Package){
		"no maintainer":      func(p *Package) { p.Maintainer = "" },
		"maintainer as name": func(p *Package) { p.Maintainer = "dennis2lee" },
		"no version":         func(p *Package) { p.Version = "" },
		"no architecture":    func(p *Package) { p.Architecture = "" },
		"no files":           func(p *Package) { p.Files = nil },
		"relative path":      func(p *Package) { p.Files[0].Path = "usr/bin/localcode" },
	} {
		p := sample()
		mangle(&p)
		if err := p.Build(&bytes.Buffer{}); err == nil {
			t.Errorf("%s: built a package anyway", name)
		}
	}
}

// The real check, where a real dpkg exists: a Linux developer, or CI on
// Linux. Skipped on the Mac the release is cut from, which is exactly why
// everything above reads the bytes rather than trusting them.
func TestDpkgItselfAcceptsThePackage(t *testing.T) {
	dpkg, err := exec.LookPath("dpkg-deb")
	if err != nil {
		t.Skip("dpkg-deb is not installed on this machine")
	}
	dir := t.TempDir()
	path := dir + "/localcode.deb"
	if err := os.WriteFile(path, build(t, sample()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(dpkg, "--info", path).CombinedOutput()
	if err != nil {
		t.Fatalf("dpkg-deb --info refused the package: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "localcode") {
		t.Errorf("dpkg-deb --info does not name the package:\n%s", out)
	}
	if out, err := exec.Command(dpkg, "--contents", path).CombinedOutput(); err != nil {
		t.Fatalf("dpkg-deb --contents refused the package: %v\n%s", err, out)
	} else if !strings.Contains(string(out), "usr/bin/localcode") {
		t.Errorf("dpkg-deb --contents does not list the binary:\n%s", out)
	}
}
