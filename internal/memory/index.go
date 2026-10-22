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
	start := strings.Index(content, blockStartPrefix)
	if start < 0 {
		return "", content
	}
	end := strings.Index(content[start:], blockEnd)
	if end < 0 {
		// A start with no end: somebody cut the block in half. What
		// follows the marker is kept as the person's text rather than
		// thrown away as half a block.
		return "", content[:start] + content[start+len(blockStartPrefix):]
	}
	end += start + len(blockEnd)
	return content[start:end], content[:start] + content[end:]
}

// indexFile is MEMORY.md as read: its text with a byte-order mark taken
// off and line endings made "\n", which is what everything here works
// on, and how it was written, so a rewrite gives the person's part back
// the way they wrote it: CRLF stays CRLF, and a BOM stays at the start of
// the file rather than ending up behind the generated block.
type indexFile struct {
	text string
	crlf bool
	bom  bool
}

// readIndex reads MEMORY.md, refusing anything but a regular file for the
// same reason a note is refused: a link planted at the index would put
// whatever it points at into every session's prompt.
func readIndex(dir string) indexFile {
	data, _, err := readRegular(IndexPath(dir), maxIndexFileBytes)
	if err != nil {
		return indexFile{}
	}
	f := indexFile{crlf: strings.Contains(data, "\r\n")}
	if strings.HasPrefix(data, "\ufeff") {
		f.bom = true
		data = strings.TrimPrefix(data, "\ufeff")
	}
	f.text = strings.ReplaceAll(data, "\r\n", "\n")
	return f
}

// encode is text written the way the file was.
func (f indexFile) encode(text string) []byte {
	if f.crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if f.bom {
		text = "\ufeff" + text
	}
	return []byte(text)
}

func readIndexFile(dir string) string { return readIndex(dir).text }

// writeIndex replaces MEMORY.md with text, encoded as f was, unless that
// is what it already holds: a rewrite that changes nothing should not
// touch the file, and BeginSession regenerates every time it runs.
func writeIndex(dir string, f indexFile, text string) error {
	if text == f.text {
		if _, err := os.Lstat(IndexPath(dir)); err == nil {
			return nil
		}
	}
	return writeAtomic(IndexPath(dir), f.encode(text))
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
	_, rest := splitIndex(f.text)
	rest = strings.Trim(rest, "\n")
	lines := blockLines(entries, u, rest)
	var b strings.Builder
	if len(lines) > 0 {
		b.WriteString(blockStart + "\n")
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n" + blockEnd + "\n")
	}
	if rest != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(rest + "\n")
	}
	if b.Len() == 0 {
		if err := os.Remove(IndexPath(s.Dir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return IndexStats{}, err
		}
	} else if err := writeIndex(s.Dir, f, b.String()); err != nil {
		return IndexStats{}, err
	}
	_, stats := s.load(entries, u)
	return stats, nil
}

// legacyLink matches a hand-written index line that is nothing but a link
// to one file, with or without a hook after it.
func legacyLink(file string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*[-*][ \t]+\[[^\]\n]*\]\(` + regexp.QuoteMeta(file) + `\)[^\n]*\n?`)
}

// dropLegacyLine takes out of the person's part of MEMORY.md the line that
// only pointed at file, once the tool manages that file or it has been
// forgotten. Lines that say more than a link, or link elsewhere, stay.
func dropLegacyLine(dir, file string) error {
	if err := indexTooLarge(dir); err != nil {
		return err
	}
	f := readIndex(dir)
	content := f.text
	if content == "" {
		return nil
	}
	block, rest := splitIndex(content)
	re := legacyLink(file)
	if !re.MatchString(rest) {
		return nil
	}
	start := strings.Index(content, blockStartPrefix)
	var out string
	if block == "" || start < 0 {
		out = re.ReplaceAllString(content, "")
	} else {
		before, after := content[:start], content[start+len(block):]
		out = re.ReplaceAllString(before, "") + block + re.ReplaceAllString(after, "")
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
	kept, _ := capLines(all, s.Limits)
	if len(kept) < len(all) {
		// Room for the line that says so, inside the same limits: the
		// limits are what the prompt pays, and a note about the budget
		// should not be what breaks it.
		lim := s.Limits
		lim.IndexLines = max(lim.IndexLines-1, 0)
		lim.IndexBytes = max(lim.IndexBytes-omittedNoteReserve, 0)
		kept, _ = capLines(all, lim)
		omitted := len(all) - len(kept)
		stats.Omitted = omitted
		kept = append(kept, fmt.Sprintf("(%d more index lines did not fit and are not shown. memory_search reaches every note, and with no query lists them all.)", omitted))
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
func capLines(lines []string, lim Limits) ([]string, int) {
	var kept []string
	size := 0
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
				cut := l[:lim.IndexBytes]
				for len(cut) > 0 && !utf8.ValidString(cut) {
					cut = cut[:len(cut)-1]
				}
				kept = append(kept, cut)
				size = len(cut)
			}
			break
		}
		kept = append(kept, l)
		size += need
	}
	return kept, size
}
