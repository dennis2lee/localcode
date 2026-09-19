package config

import "strings"

// A config file travels. A repository's .localcode/config.json is
// committed and checked out on Windows and on Linux alike, and a home
// config is copied between machines, so a rule about the shape of a path
// in it has to give the same answer wherever it is read.
//
// filepath.IsAbs does not: it answers for the host. "/srv/mcp" is
// absolute on Linux and relative on Windows, and "C:\srv" is the other
// way around — so a path this package refuses on one machine it accepts
// and then joins onto the project on the other. filepath.Clean and
// filepath.Separator have the same problem with "..\\escape".
//
// So these two read both conventions, always. They are about what a
// person wrote in a file rather than about what this machine can open,
// which is a different question and one the file system answers later.

// absolutePath reports whether p is absolute in either convention: a
// leading slash of either kind, a drive letter, or a UNC share.
func absolutePath(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	switch {
	case strings.HasPrefix(p, "//"):
		// \\server\share, which is absolute and names another machine.
		return true
	case strings.HasPrefix(p, "/"):
		return true
	case len(p) >= 2 && p[1] == ':' && isDriveLetter(p[0]):
		// "C:" with or without a slash after it. "C:sub" is relative to
		// that drive's current directory, which is not a directory anybody
		// here can name, so it counts as absolute for the purpose of
		// refusing it.
		return true
	}
	return false
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// escapesUpward reports whether a relative path climbs out of the
// directory it is relative to, reading both separators and without
// consulting the file system.
func escapesUpward(p string) bool {
	depth := 0
	for _, part := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		switch part {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}
