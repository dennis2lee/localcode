package debpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Reading a .deb back out again.
//
// This is here so the tests can check the package that was written rather
// than the intention behind it. Nothing on this machine can install a
// .deb — the release is cut from a Mac — so "it builds" would otherwise
// mean nothing at all, and the failure would land on whoever ran
// `apt install` first.
//
// It reads only what a check needs: the member names and their order, the
// control fields, and the files with their modes. It is not a general
// dpkg.

// Contents is a parsed package.
type Contents struct {
	// Members are the ar member names, in the order they appear. dpkg
	// requires debian-binary first; the tests assert on this directly.
	Members []string
	// Format is the debian-binary member, normally "2.0\n".
	Format string
	// Control are the fields of the control file, keyed as written
	// ("Package", "Installed-Size", ...). The extended description is
	// under "Description" with its continuation lines joined.
	Control map[string]string
	// MD5Sums maps the path (as md5sums spells it, with no leading
	// slash) to its checksum.
	MD5Sums map[string]string
	// Files are the regular files in data.tar.gz, keyed by their absolute
	// installed path.
	Files map[string]File
	// Dirs are the directories in data.tar.gz, as absolute paths.
	Dirs []string
}

// Read parses a .deb.
func Read(b []byte) (Contents, error) {
	out := Contents{
		Control: map[string]string{},
		MD5Sums: map[string]string{},
		Files:   map[string]File{},
	}
	if !bytes.HasPrefix(b, []byte(arMagic)) {
		return out, fmt.Errorf("not an ar archive")
	}
	rest := b[len(arMagic):]

	members := map[string][]byte{}
	for len(rest) > 0 {
		if len(rest) < 60 {
			return out, fmt.Errorf("truncated ar header")
		}
		name := strings.TrimSpace(string(rest[0:16]))
		sizeField := strings.TrimSpace(string(rest[48:58]))
		if magic := string(rest[58:60]); magic != "`\n" {
			return out, fmt.Errorf("member %q has a bad header terminator %q", name, magic)
		}
		size, err := strconv.Atoi(sizeField)
		if err != nil {
			return out, fmt.Errorf("member %q has an unreadable size %q", name, sizeField)
		}
		if 60+size > len(rest) {
			return out, fmt.Errorf("member %q claims %d bytes, more than the archive holds", name, size)
		}
		body := rest[60 : 60+size]
		members[name] = body
		out.Members = append(out.Members, name)
		advance := 60 + size
		if size%2 == 1 {
			advance++ // members are padded to an even boundary
		}
		if advance > len(rest) {
			return out, fmt.Errorf("member %q runs past the end of the archive", name)
		}
		rest = rest[advance:]
	}

	out.Format = string(members[memberVersion])

	control, err := untarGz(members[memberControl])
	if err != nil {
		return out, fmt.Errorf("control.tar.gz: %w", err)
	}
	out.Control = parseControl(string(control.files["./control"]))
	for _, line := range strings.Split(string(control.files["./md5sums"]), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			out.MD5Sums[fields[1]] = fields[0]
		}
	}

	data, err := untarGz(members[memberData])
	if err != nil {
		return out, fmt.Errorf("data.tar.gz: %w", err)
	}
	for name, body := range data.files {
		out.Files[strings.TrimPrefix(name, ".")] = File{
			Path: strings.TrimPrefix(name, "."),
			Mode: data.modes[name],
			Data: body,
		}
	}
	for _, dir := range data.dirs {
		out.Dirs = append(out.Dirs, strings.TrimSuffix(strings.TrimPrefix(dir, "."), "/"))
	}
	return out, nil
}

type tarball struct {
	files map[string][]byte
	modes map[string]int64
	dirs  []string
}

func untarGz(b []byte) (tarball, error) {
	out := tarball{files: map[string][]byte{}, modes: map[string]int64{}}
	if len(b) == 0 {
		return out, fmt.Errorf("member is missing")
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			out.dirs = append(out.dirs, h.Name)
		case tar.TypeReg:
			body, err := io.ReadAll(tr)
			if err != nil {
				return out, err
			}
			out.files[h.Name] = body
			out.modes[h.Name] = h.Mode
		}
		if h.Uname != "root" || h.Gname != "root" {
			// Everything in a .deb is owned by root: dpkg installs as
			// root and the ownership in the archive is what the installed
			// files get.
			return out, fmt.Errorf("%s is owned by %q:%q, not root", h.Name, h.Uname, h.Gname)
		}
	}
	return out, nil
}

// parseControl reads RFC822-ish control fields, joining the continuation
// lines of a folded field into one value separated by newlines.
func parseControl(s string) map[string]string {
	out := map[string]string{}
	field := ""
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if field != "" {
				out[field] += "\n" + strings.TrimSpace(line)
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field = name
		out[field] = strings.TrimSpace(value)
	}
	return out
}
