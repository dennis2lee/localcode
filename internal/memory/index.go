package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The block of MEMORY.md the memory tool owns. Everything outside it is
// the person's, or an earlier version's, and is kept byte for byte.
const (
	blockStart = "<!-- localcode:memory-index start: generated from the note files; edit a note, not this block -->"
	blockEnd   = "<!-- localcode:memory-index end -->"
	// blockStartPrefix finds a start marker whatever its trailing words,
	// so the explanation in it can be reworded without orphaning the
	// blocks already written.
	blockStartPrefix = "<!-- localcode:memory-index start"
)

// typeHeadings are the index's group headings.
var typeHeadings = map[Type]string{
	TypeUser:      "## User",
	TypeFeedback:  "## Feedback",
	TypeProject:   "## Project",
	TypeReference: "## Reference",
	TypeLegacy:    "## Other notes",
}

// IndexStats describes an index as loaded.
type IndexStats struct {
	// Notes is how many note files the directory holds.
	Notes int
	// Lines and Bytes are what was loaded.
	Lines int
	Bytes int
	// Omitted is how many lines did not fit the limits.
	Omitted int
}

// splitIndex separates MEMORY.md into the generated block and the rest.
func splitIndex(content string) (block, rest string) {
	block, _, rest = findBlock(content)
	return block, rest
}

// findBlock finds the generated block: a start marker that begins a
// line, the first end marker after it that begins a line, and nothing
// between them but what the generator writes (group headings, note
// lines, blank lines). Anything else is not the block, however it
// looks: the person quoting a marker in their own notes, above the real
// block, would otherwise pair with the real end and everything between
// would be taken for the block and rewritten away. start is where the
// block begins, or -1.
func findBlock(content string) (block string, start int, rest string) {
	for from := 0; ; {
		i := strings.Index(content[from:], blockStartPrefix)
		if i < 0 {
			return "", -1, content
		}
		at := from + i
		from = at + len(blockStartPrefix)
		if at > 0 && content[at-1] != '\n' {
			continue
		}
		j := strings.Index(content[at:], "\n"+blockEnd)
		if j < 0 {
			// A start with no end: somebody cut the block in half. All
			// of it is kept as the person's text, the marker line
			// included, rather than thrown away as half a block or left
			// as half a comment; a new block goes above it.
			continue
		}
		end := at + j + 1 + len(blockEnd)
		if !generatedBody(content[at:end]) {
			continue
		}
		return content[at:end], at, content[:at] + content[end:]
	}
}

// generatedBody reports whether a candidate block holds only lines the
// generator writes.
func generatedBody(block string) bool {
	lines := strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n")
	if len(lines) < 2 || !strings.HasSuffix(strings.TrimSpace(lines[0]), "-->") {
		return false
	}
	// The generator never writes an empty block: no notes, no block. So
	// a group heading is required, and a pair of markers with nothing
	// between them is somebody quoting the format.
	heading := false
	for _, l := range lines[1 : len(lines)-1] {
		l = strings.TrimSpace(l)
		switch {
		case l == "", strings.HasPrefix(l, "- ["):
		case strings.HasPrefix(l, "## "):
			heading = true
		default:
			return false
		}
	}
	return heading
}

// indexFile is MEMORY.md as read: its bytes after a byte-order mark,
// untouched, and whether it had a mark or any CRLF line ending. The
// person's part of the file is spliced back from raw exactly as it was,
// whatever its line endings, and the generated block takes CRLF when the
// file has any, so it does not stand out in a Windows editor. text is
// raw with CRLF made LF, which is what reading and matching work on.
type indexFile struct {
	raw    string
	bom    bool
	crlf   bool
	exists bool
}

func (f indexFile) text() string { return strings.ReplaceAll(f.raw, "\r\n", "\n") }

func (f indexFile) eol() string {
	if f.crlf {
		return "\r\n"
	}
	return "\n"
}

// readIndex reads MEMORY.md, refusing anything but a regular file for the
// same reason a note is refused: a link planted at the index would put
// whatever it points at into every session's prompt.
func readIndex(dir string) indexFile {
	data, _, err := readRegular(IndexPath(dir), maxIndexFileBytes)
	if err != nil {
		return indexFile{}
	}
	f := indexFile{exists: true, crlf: strings.Contains(data, "\r\n")}
	if strings.HasPrefix(data, "\ufeff") {
		f.bom = true
		data = strings.TrimPrefix(data, "\ufeff")
	}
	f.raw = data
	return f
}

func readIndexFile(dir string) string { return readIndex(dir).text() }

// writeIndex replaces MEMORY.md with raw, the mark put back in front,
// unless that is what it already holds: a rewrite that changes nothing
// should not touch the file, and BeginSession regenerates every time it
// runs.
func writeIndex(dir string, f indexFile, raw string) error {
	if f.exists && raw == f.raw {
		return nil
	}
	if f.bom {
		raw = "\ufeff" + raw
	}
	return writeAtomic(IndexPath(dir), []byte(raw))
}

// trimEOLs takes up to n line endings off the start of s, and nothing
// else.
func trimEOLs(s string, n int) string {
	for ; n > 0; n-- {
		switch {
		case strings.HasPrefix(s, "\r\n"):
			s = s[2:]
		case strings.HasPrefix(s, "\n"):
			s = s[1:]
		default:
			return s
		}
	}
	return s
}

// indexTooLarge reports a MEMORY.md the tool will not rewrite, because it
// could not keep all of it.
func indexTooLarge(dir string) error {
	if _, cut, err := readRegular(IndexPath(dir), maxIndexFileBytes); err == nil && cut {
		return fmt.Errorf("%s is larger than %dMB; shorten it by hand before the memory tools change it", IndexPath(dir), maxIndexFileBytes>>20)
	}
	return nil
}

// blockLines renders the typed notes, grouped by type in the order of
// Types and newest-used first within a group, plus the untyped files the
// person's own text does not link, so every note in the directory is
// reachable from the index.
func blockLines(entries []Entry, u *usage, rest string) []string {
	byType := map[Type][]Entry{}
	for _, e := range entries {
		if e.Type == TypeLegacy && strings.Contains(rest, "("+e.File()+")") {
			continue
		}
		byType[e.Type] = append(byType[e.Type], e)
	}
	var lines []string
	for _, t := range append(append([]Type{}, Types...), TypeLegacy) {
		list := byType[t]
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool {
			a, b := u.Entries[list[i].File()], u.Entries[list[j].File()]
			if a != nil && b != nil && !a.LastUsed.Equal(b.LastUsed) {
				return a.LastUsed.After(b.LastUsed)
			}
			if (a != nil) != (b != nil) {
				return a != nil
			}
			return list[i].Name < list[j].Name
		})
		lines = append(lines, typeHeadings[t])
		for _, e := range list {
			desc := e.Description
			if desc == "" {
				desc = DescribeFrom(e.Body)
			}
			lines = append(lines, fmt.Sprintf("- [%s](%s): %s", e.Name, e.File(), desc))
		}
	}
	return lines
}

// regenerate rewrites the block of MEMORY.md from the note files. Called
// with the directory locked, after every change the tool makes.
func (s Store) regenerate(u *usage) (IndexStats, error) {
	entries, err := readEntries(s.Dir)
	if err != nil {
		return IndexStats{}, err
	}
	if err := indexTooLarge(s.Dir); err != nil {
		return IndexStats{}, err
	}
	f := readIndex(s.Dir)
	block, start, rest := findBlock(f.raw)
	if block != "" {
		// The block is followed by its own line ending and the blank
		// line this function wrote after it: two line endings, taken off
		// wherever the block is, so a block the person moved into the
		// middle of their text leaves no blank lines behind when it
		// moves back to the top or goes.
		rest = f.raw[:start] + trimEOLs(f.raw[start+len(block):], 2)
	}
	restText := strings.Trim(strings.ReplaceAll(rest, "\r\n", "\n"), "\n")
	lines := blockLines(entries, u, restText)
	eol := f.eol()
	var b strings.Builder
	if len(lines) > 0 {
		b.WriteString(blockStart + eol)
		b.WriteString(strings.Join(lines, eol))
		b.WriteString(eol + blockEnd + eol)
	}
	if strings.TrimSpace(restText) != "" {
		if b.Len() > 0 {
			b.WriteString(eol)
		}
		b.WriteString(rest)
	}
	switch {
	case b.Len() > 0:
		if err := writeIndex(s.Dir, f, b.String()); err != nil {
			return IndexStats{}, err
		}
	case block != "":
		// The block was all the file held, and its last note is gone.
		if err := os.Remove(IndexPath(s.Dir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return IndexStats{}, err
		}
	}
	// Otherwise the file held no block and nothing but blank space, or
	// nothing at all: it is the person's, and stays as it is.
	_, stats := s.load(entries, u)
	return stats, nil
}

// legacyLink matches a hand-written index line that starts with a link
// to one file. Whether the line is nothing but that link and its hook is
// onlyLinks's to say.
func legacyLink(file string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*[-*][ \t]+\[[^\]\n]*\]\(` + regexp.QuoteMeta(file) + `\)([^\n]*)\n?`)
}

// maxHook is the longest text after the link that is still taken for the
// note's one-line hook, which the generated block's description replaces.
const maxHook = 150

// dropOnlyLinks removes from text the lines legacyLink finds that say
// nothing but the link and a short hook: no other link, nothing long
// enough to be a fact of its own. A line that says more is the person's
// and stays, a duplicate of the generated line rather than a loss.
func dropOnlyLinks(re *regexp.Regexp, text string) string {
	return re.ReplaceAllStringFunc(text, func(line string) string {
		m := re.FindStringSubmatch(line)
		after := strings.TrimRight(m[1], "\r")
		// Any other link, in any of Markdown's forms (inline, reference
		// [text][ref] or [ref], autolink <https://...>, a bare URL), makes
		// the line the person's: keeping a line that only looked like
		// more costs a duplicate, deleting one that was more costs what
		// it said.
		if strings.ContainsAny(after, "[<") || strings.Contains(after, "://") || len([]rune(after)) > maxHook {
			return line
		}
		return ""
	})
}

// dropLegacyLine takes out of the person's part of MEMORY.md the line that
// only pointed at file, once the tool manages that file or it has been
// forgotten. Lines that say more than a link and its hook, or link
// elsewhere too, stay.
func dropLegacyLine(dir, file string) error {
	if err := indexTooLarge(dir); err != nil {
		return err
	}
	f := readIndex(dir)
	content := f.raw
	if content == "" {
		return nil
	}
	// On the raw text: the person's lines go back exactly as they were,
	// whatever ends them, and the pattern's [^\n]* takes a "\r" with
	// the rest of the line.
	block, start, rest := findBlock(content)
	re := legacyLink(file)
	if !re.MatchString(rest) {
		return nil
	}
	var out string
	if block == "" || start < 0 {
		out = dropOnlyLinks(re, content)
	} else {
		before, after := content[:start], content[start+len(block):]
		out = dropOnlyLinks(re, before) + block + dropOnlyLinks(re, after)
	}
	return writeIndex(dir, f, out)
}

// Load is the index as the prompt receives it: the generated block, then
// the person's own text, neutralized, within the directory's limits. When
// something does not fit, the last line says how much was left out and
// how to reach it.
func (s Store) Load() (string, IndexStats) {
	entries, err := readEntries(s.Dir)
	if err != nil {
		entries = nil
	}
	return s.load(entries, readUsage(s.Dir))
}

func (s Store) load(entries []Entry, u *usage) (string, IndexStats) {
	_, rest := splitIndex(readIndexFile(s.Dir))
	rest = strings.Trim(rest, "\n")
	lines := blockLines(entries, u, rest)
	if rest != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Split(rest, "\n")...)
	}
	text := Neutralize(strings.Join(lines, "\n"))
	stats := IndexStats{Notes: len(entries)}
	if strings.TrimSpace(text) == "" {
		return "", stats
	}
	all := strings.Split(text, "\n")
	kept, _, cut := capLines(all, s.Limits)
	if len(kept) < len(all) || cut {
		// Room for the line that says so, inside the same limits: the
		// limits are what the prompt pays, and a note about the budget
		// should not be what breaks it.
		lim := s.Limits
		lim.IndexLines = max(lim.IndexLines-1, 0)
		lim.IndexBytes = max(lim.IndexBytes-omittedNoteReserve, 0)
		kept, _, cut = capLines(all, lim)
		omitted := len(all) - len(kept)
		var notice string
		switch {
		case cut && omitted == 0:
			// One line longer than the whole budget: a pasted paragraph
			// or record. What was cut is a line's end, not whole lines,
			// and it is said all the same.
			notice = "(the line above is longer than the index's budget and was cut short. memory_search reaches every note, and with no query lists them all.)"
			omitted = 1
		case cut:
			notice = fmt.Sprintf("(the line above was cut short, and %d more index lines did not fit and are not shown. memory_search reaches every note, and with no query lists them all.)", omitted)
		default:
			notice = fmt.Sprintf("(%d more index lines did not fit and are not shown. memory_search reaches every note, and with no query lists them all.)", omitted)
		}
		stats.Omitted = omitted
		kept = append(kept, notice)
	}
	out := strings.Join(kept, "\n")
	stats.Lines, stats.Bytes = len(kept), len(out)
	return out, stats
}

// omittedNoteReserve is the room kept for the line saying how much did
// not fit.
const omittedNoteReserve = 160

// capLines keeps whole lines from the top while they fit both limits. A
// single line longer than the byte budget is cut at a rune boundary
// rather than dropped, so an index that is one long paragraph still loads
// its beginning, as it always has.
func capLines(lines []string, lim Limits) (kept []string, size int, cut bool) {
	for _, l := range lines {
		if len(kept) >= lim.IndexLines {
			break
		}
		need := len(l)
		if len(kept) > 0 {
			need++
		}
		if size+need > lim.IndexBytes {
			if len(kept) == 0 && lim.IndexBytes > 0 {
				part := l[:lim.IndexBytes]
				for len(part) > 0 && !utf8.ValidString(part) {
					part = part[:len(part)-1]
				}
				kept = append(kept, part)
				size = len(part)
				cut = true
			}
			break
		}
		kept = append(kept, l)
		size += need
	}
	return kept, size, cut
}
