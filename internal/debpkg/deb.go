// Package debpkg writes a Debian binary package (.deb).
//
// It exists because localcode is released from a Mac, and `dpkg-deb` is
// not on a Mac. The alternatives were to require another Homebrew formula
// on the release machine (the MSI already needs msitools, and that
// dependency is felt every release), or to build the package on a Linux
// runner purely to run one tool over a binary that was cross-compiled
// anyway. A .deb is an `ar` archive holding three members, two of which
// are ordinary tarballs, and Go has readers and writers for tar and gzip
// in its standard library — so this is about two hundred lines instead of
// a build machine.
//
// Written to be checkable rather than trusted: Read parses a package back
// out of the bytes, and the tests use it to assert the exact structure
// dpkg requires. Nothing here needs root, and nothing here shells out.
package debpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// arMagic opens every ar archive.
const arMagic = "!<arch>\n"

// Members of a .deb, in the order dpkg requires them.
//
// debian-binary must come first and must not be compressed: it is the
// format version, and a reader has to know that before it can make sense
// of anything after it.
const (
	memberVersion = "debian-binary"
	memberControl = "control.tar.gz"
	memberData    = "data.tar.gz"
)

// formatVersion is the .deb format this writes. 2.0 has been the format
// since 1995 and is what every dpkg in existence reads.
const formatVersion = "2.0\n"

// File is one file to install.
type File struct {
	// Path is where it lands on the target system, absolute:
	// "/usr/bin/localcode".
	Path string
	// Mode is the permission bits. 0o755 for a program, 0o644 for a
	// document.
	Mode int64
	Data []byte
}

// Package is the package to write.
type Package struct {
	Name         string
	Version      string
	Architecture string // "amd64", "arm64" — Debian's names, not Go's
	Maintainer   string // "Name <email>", which dpkg requires in that shape
	Homepage     string
	Section      string
	Priority     string
	// Synopsis is the one-line description. Description is the longer
	// text under it, in ordinary paragraphs — the continuation-line
	// formatting Debian wants is applied here rather than by the caller.
	Synopsis    string
	Description string
	Files       []File
	// ModTime is stamped on every entry. A fixed value makes the output
	// byte-for-byte reproducible, which is why the caller passes one
	// rather than this reaching for the clock: two builds of the same
	// release should produce the same package.
	ModTime time.Time
}

// Build writes the package to w.
func (p Package) Build(w io.Writer) error {
	if err := p.validate(); err != nil {
		return err
	}

	data, sums, installed, err := p.dataTar()
	if err != nil {
		return fmt.Errorf("data.tar.gz: %w", err)
	}
	control, err := p.controlTar(sums, installed)
	if err != nil {
		return fmt.Errorf("control.tar.gz: %w", err)
	}

	if _, err := io.WriteString(w, arMagic); err != nil {
		return err
	}
	for _, m := range []struct {
		name string
		body []byte
	}{
		{memberVersion, []byte(formatVersion)},
		{memberControl, control},
		{memberData, data},
	} {
		if err := writeArMember(w, m.name, m.body, p.ModTime); err != nil {
			return err
		}
	}
	return nil
}

func (p Package) validate() error {
	switch {
	case p.Name == "":
		return fmt.Errorf("no package name")
	case p.Version == "":
		return fmt.Errorf("no version")
	case p.Architecture == "":
		return fmt.Errorf("no architecture")
	case p.Synopsis == "":
		return fmt.Errorf("no description")
	case len(p.Files) == 0:
		return fmt.Errorf("no files to install")
	}
	// A Maintainer that is not "Name <email>" is rejected by dpkg at
	// build time in the ordinary toolchain; here nothing would notice
	// until someone tried to install the package.
	if !strings.Contains(p.Maintainer, "<") || !strings.HasSuffix(strings.TrimSpace(p.Maintainer), ">") {
		return fmt.Errorf("maintainer %q is not in the form \"Name <email>\"", p.Maintainer)
	}
	for _, f := range p.Files {
		if !strings.HasPrefix(f.Path, "/") {
			return fmt.Errorf("file path %q is not absolute", f.Path)
		}
	}
	return nil
}

// dataTar builds the payload: the files, and every directory leading to
// them. Returns the compressed tar, the md5sums text, and the installed
// size in KiB.
func (p Package) dataTar() (payload []byte, sums string, installedKiB int64, err error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	// Directories first, and every one of them: dpkg unpacks entries in
	// order and a file whose parent is not in the archive is an unpack
	// failure on some paths — and, more practically, the directories are
	// what a package removal knows to clean up.
	dirs := map[string]bool{}
	var order []string
	for _, f := range p.Files {
		parts := strings.Split(strings.Trim(f.Path, "/"), "/")
		for i := 1; i < len(parts); i++ {
			dir := "./" + strings.Join(parts[:i], "/") + "/"
			if !dirs[dir] {
				dirs[dir] = true
				order = append(order, dir)
			}
		}
	}
	sort.Strings(order)
	for _, dir := range order {
		if err := tw.WriteHeader(&tar.Header{
			Name:     dir,
			Typeflag: tar.TypeDir,
			Mode:     0o755,
			ModTime:  p.ModTime,
			Uname:    "root",
			Gname:    "root",
			Format:   tar.FormatGNU,
		}); err != nil {
			return nil, "", 0, err
		}
	}

	var md5s strings.Builder
	var total int64
	files := append([]File(nil), p.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		name := "." + f.Path
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     f.Mode,
			Size:     int64(len(f.Data)),
			ModTime:  p.ModTime,
			Uname:    "root",
			Gname:    "root",
			Format:   tar.FormatGNU,
		}); err != nil {
			return nil, "", 0, err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, "", 0, err
		}
		sum := md5.Sum(f.Data)
		// The path in md5sums has no leading "./" and no leading slash,
		// which is the one place in the package where it is spelled that
		// way. dpkg matches these against the files it unpacked, so a
		// spelling of its own is a checksum that never matches anything.
		fmt.Fprintf(&md5s, "%s  %s\n", hex.EncodeToString(sum[:]), strings.TrimPrefix(f.Path, "/"))
		total += int64(len(f.Data))
	}

	if err := tw.Close(); err != nil {
		return nil, "", 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, "", 0, err
	}
	// Installed-Size is in KiB, rounded up: it is what an installer shows
	// before it commits, so under-reporting it is the wrong direction.
	return buf.Bytes(), md5s.String(), (total + 1023) / 1024, nil
}

// controlTar builds the metadata member: the control file and md5sums.
func (p Package) controlTar(sums string, installedKiB int64) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	if err := tw.WriteHeader(&tar.Header{
		Name:     "./",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
		ModTime:  p.ModTime,
		Uname:    "root",
		Gname:    "root",
		Format:   tar.FormatGNU,
	}); err != nil {
		return nil, err
	}

	for _, f := range []struct {
		name string
		body string
	}{
		{"./control", p.controlFile(installedKiB)},
		{"./md5sums", sums},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name:     f.name,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(f.body)),
			ModTime:  p.ModTime,
			Uname:    "root",
			Gname:    "root",
			Format:   tar.FormatGNU,
		}); err != nil {
			return nil, err
		}
		if _, err := io.WriteString(tw, f.body); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// controlFile renders debian/control for a binary package.
func (p Package) controlFile(installedKiB int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Package: %s\n", p.Name)
	fmt.Fprintf(&b, "Version: %s\n", p.Version)
	fmt.Fprintf(&b, "Architecture: %s\n", p.Architecture)
	fmt.Fprintf(&b, "Maintainer: %s\n", p.Maintainer)
	fmt.Fprintf(&b, "Installed-Size: %d\n", installedKiB)
	if p.Section != "" {
		fmt.Fprintf(&b, "Section: %s\n", p.Section)
	}
	if p.Priority != "" {
		fmt.Fprintf(&b, "Priority: %s\n", p.Priority)
	}
	if p.Homepage != "" {
		fmt.Fprintf(&b, "Homepage: %s\n", p.Homepage)
	}
	// No Depends field, and that is a statement rather than an omission:
	// the binary is built with CGO_ENABLED=0, so it needs no libc, no
	// runtime and nothing else from the system.
	fmt.Fprintf(&b, "Description: %s\n", p.Synopsis)
	for _, line := range strings.Split(strings.TrimRight(p.Description, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			// A blank line inside an extended description is written as a
			// line holding a single dot. Written as nothing, it ends the
			// field and everything after it is read as a new one.
			b.WriteString(" .\n")
			continue
		}
		fmt.Fprintf(&b, " %s\n", line)
	}
	return b.String()
}

// writeArMember writes one member with its 60-byte header.
//
// The format is fixed-width text, which is why this is a series of
// Fprintf's rather than a struct: every field is space-padded to its own
// width and the whole thing is padded to an even length.
func writeArMember(w io.Writer, name string, body []byte, mod time.Time) error {
	if len(name) > 16 {
		// Longer names need one of the two incompatible extensions (BSD's
		// "#1/" or GNU's string table), and a .deb has three members whose
		// names are fixed and short.
		return fmt.Errorf("ar member name %q is too long", name)
	}
	header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n",
		name, mod.Unix(), 0, 0, 0o100644, len(body))
	if len(header) != 60 {
		return fmt.Errorf("ar header for %q came out %d bytes, want 60", name, len(header))
	}
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if len(body)%2 == 1 {
		_, err := io.WriteString(w, "\n")
		return err
	}
	return nil
}
