package config

import (
	"bytes"
	"encoding/json"
	"sort"
)

// A config file is a thing people write by hand, and a thing written by
// hand wants to say why.
//
// JSON has no comments, so this one has been carrying pseudo-comments
// instead: "//base_url" beside "base_url", a key with a string value that
// happens to be prose. That works and it reads badly, it cannot be
// attached to anything but an object member, and it shows up in the
// parsed config as a field nothing reads.
//
// So the file is read as JSONC: // to end of line, /* */ across lines,
// and a trailing comma before } or ]. Nothing else changes. It is still
// JSON, still written by the same writers, and a file with no comments in
// it parses exactly as it did.
//
// The hard half is not reading. It is that this program rewrites
// config.json when a switch is toggled or a permission rule is added, and
// a rewrite that dropped the comments would eat the very thing this
// feature is for, silently, the first time somebody typed /smart-agent.
// stripComments is therefore length-preserving: every byte it removes is
// replaced by a space, so an offset into the stripped text is the same
// offset into the original, and a writer can find the exact span of the
// value it is replacing and leave every other byte of the file alone.
// See rawfile.go.

// stripComments blanks out JSONC comments, leaving valid JSON of exactly
// the same length.
//
// Comments inside strings are not comments: a base_url of
// "http://example.com" contains "//" and means nothing by it, which is
// the bug every naive version of this has. So the scan tracks strings and
// their escapes, and only looks for a comment when it is not in one.
//
// A trailing comma is removed the same way, by blanking the comma itself
// once the next non-space, non-comment character turns out to be a
// closing brace or bracket. Written as a second pass over the blanked
// text, because deciding it needs the comments already gone.
func stripComments(data []byte) []byte {
	out := make([]byte, len(data))
	copy(out, data)

	inString := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			switch c {
			case '\\':
				// Skip the escaped byte, whatever it is: a \" must not
				// end the string, and a \\ must not escape the quote
				// after it.
				i++
			case '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
			// The newline itself stays: it is whitespace JSON allows,
			// and keeping it means a line comment cannot swallow the
			// line after it.
			i--
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			out[i], out[i+1] = ' ', ' '
			i += 2
			for i < len(out) {
				if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
					out[i], out[i+1] = ' ', ' '
					i++
					break
				}
				// Newlines inside a block comment are kept, so a line
				// number in a parse error still means what it says.
				if out[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
		}
	}
	return stripTrailingCommas(out)
}

// stripTrailingCommas blanks a comma that is followed only by whitespace
// and then a closing brace or bracket.
//
// Run on already-blanked text, so a comma followed by a comment and then
// a brace is caught too, which is exactly the shape a commented config
// ends up in: a last entry, a comma somebody left behind, and a note
// about the section under it.
func stripTrailingCommas(data []byte) []byte {
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c != ',' {
			continue
		}
		for j := i + 1; j < len(data); j++ {
			switch data[j] {
			case ' ', '\t', '\r', '\n':
				continue
			case '}', ']':
				data[i] = ' '
			}
			break
		}
	}
	return data
}

// hasComments reports whether stripping changed anything, which is how a
// writer knows it has a file worth being careful with.
func hasComments(data []byte) bool {
	stripped := stripComments(append([]byte(nil), data...))
	if len(stripped) != len(data) {
		return true
	}
	for i := range data {
		if data[i] != stripped[i] {
			return true
		}
	}
	return false
}

// spliceKeys rewrites only the top-level keys whose values changed,
// leaving every other byte of the file exactly as it was.
//
// This is what keeps a commented config commented. A value is replaced in
// place, in the original text, so the comments around it, the
// indentation somebody chose, and the ordering they put the keys in all
// survive a toggle written by this program.
//
// A key the change adds goes after everything else in the object: after
// the last member and after whatever comments follow it, on a line of
// its own before the closing brace. Nothing that was there moves, so no
// comment ends up beside a key it was not written for. Adding used to be
// refused outright, on the reasoning that any position chooses a side of
// somebody's comment, and the cost was a switch in the settings window
// that did not survive a restart for anybody whose config had a note in
// it. The end of the object is the one position that chooses no side.
//
// It reports false rather than guessing when it meets something it
// cannot do safely: a key that was removed, or a file whose top level
// will not scan. The caller then refuses the write and says so, which is
// the honest outcome: the alternative is rewriting the file and taking
// the comments with it.
func spliceKeys(data []byte, before, after map[string]json.RawMessage) ([]byte, bool) {
	spans, ok := topLevelSpans(data)
	if !ok {
		return nil, false
	}

	// The file's own line ends, in everything written into it: a value
	// that takes several lines is marshalled with bare newlines, and a
	// file written on Windows would come back with two kinds.
	nl := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		nl = "\r\n"
	}
	lines := func(b []byte) []byte {
		if nl == "\n" {
			return b
		}
		return bytes.ReplaceAll(b, []byte("\n"), []byte(nl))
	}

	type edit struct {
		from, to int
		value    []byte
	}
	var edits []edit
	var added []string
	for key, want := range after {
		had, existed := before[key]
		if existed && bytes.Equal(canonical(had), canonical(want)) {
			continue
		}
		span, found := spans[key]
		if !found {
			added = append(added, key)
			continue
		}
		// Indented to sit where the old value sat, so a nested object
		// replacing another reads like the rest of the file.
		pretty, err := json.MarshalIndent(json.RawMessage(want), span.indent, "  ")
		if err != nil {
			return nil, false
		}
		edits = append(edits, edit{from: span.from, to: span.to, value: lines(pretty)})
	}
	for key := range before {
		if _, still := after[key]; !still {
			return nil, false // a removal; same reasoning as an addition
		}
	}
	if len(added) > 0 {
		// In a fixed order, so the same change writes the same file.
		sort.Strings(added)
		tail, ok := objectTail(data, spans)
		if !ok {
			return nil, false
		}
		var b bytes.Buffer
		for _, key := range added {
			name, err := json.Marshal(key)
			if err != nil {
				return nil, false
			}
			pretty, err := json.MarshalIndent(after[key], tail.indent, "  ")
			if err != nil {
				return nil, false
			}
			if tail.members {
				b.WriteByte(',')
			}
			tail.members = true
			b.WriteString(nl)
			b.WriteString(tail.indent)
			b.Write(name)
			b.WriteString(": ")
			b.Write(lines(pretty))
		}
		text := b.Bytes()
		if tail.comma >= 0 {
			// The last member already ends in a comma, which a JSONC
			// file may: the new members go after it and take over the
			// trailing one.
			text = append(text[1:], ',')
		} else if tail.lastEnd >= 0 && tail.lastEnd != tail.at {
			// A comment sits between the last member and the end of
			// the object. The comma belongs to the member, before the
			// comment, and the new lines go after it.
			edits = append(edits, edit{from: tail.lastEnd, to: tail.lastEnd, value: []byte{','}})
			text = text[1:]
		}
		if tail.at < len(data) && data[tail.at] == '}' {
			// Nothing between the last thing and the brace, as in an
			// object written on one line: the brace goes on its own.
			text = append(text, nl...)
		}
		edits = append(edits, edit{from: tail.at, to: tail.at, value: text})
	}
	if len(edits) == 0 {
		return data, true
	}
	// Back to front, so an earlier edit does not move a later one. Two
	// edits at one offset keep the order they were made in.
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].from > edits[j].from })

	out := append([]byte(nil), data...)
	for _, e := range edits {
		out = append(out[:e.from], append(append([]byte(nil), e.value...), out[e.to:]...)...)
	}
	return out, true
}

// canonical re-encodes a value so two spellings of the same thing compare
// equal, which is what stops a splice rewriting a key nothing changed
// just because the marshaller spaces it differently.
func canonical(v json.RawMessage) []byte {
	// Numbers as written, not as floats: two whole numbers past 2^53
	// that differ are one float, and a change from one to the other
	// would compare equal here and not be written.
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var any1 any
	if err := dec.Decode(&any1); err != nil {
		return v
	}
	out, err := json.Marshal(any1)
	if err != nil {
		return v
	}
	return out
}

// valueSpan is where a top-level key's value sits in the original text,
// and the indentation of the line its key is on, so a replacement lines
// up with what is around it.
type valueSpan struct {
	from, to int
	indent   string
}

// topLevelSpans finds each top-level key's value span by scanning the
// comment-blanked copy, whose offsets are the original's.
func topLevelSpans(data []byte) (map[string]valueSpan, bool) {
	clean := stripComments(append([]byte(nil), data...))
	dec := json.NewDecoder(bytes.NewReader(clean))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, false
	}
	out := map[string]valueSpan{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, false
		}
		// The decoder reports the offset after the key token; the value
		// begins at the next thing that is not a space or a colon.
		from := int(dec.InputOffset())
		for from < len(clean) && (clean[from] == ' ' || clean[from] == '\t' ||
			clean[from] == '\n' || clean[from] == '\r' || clean[from] == ':') {
			from++
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		out[key] = valueSpan{from: from, to: int(dec.InputOffset()), indent: indentOfLine(clean, from)}
	}
	return out, true
}

// indentOfLine is the whitespace at the start of the line an offset is
// on, so a re-marshalled object is indented like the one it replaces.
func indentOfLine(data []byte, off int) string {
	start := off
	for start > 0 && data[start-1] != '\n' {
		start--
	}
	end := start
	for end < len(data) && (data[end] == ' ' || data[end] == '\t') {
		end++
	}
	return string(data[start:end])
}

// objectEnd is where a new member of the top-level object goes.
type objectEnd struct {
	// at is the offset to write at: after the last thing in the object
	// that is not whitespace, a comment included.
	at int
	// lastEnd is the end of the last member's value, or -1 in an object
	// with no members.
	lastEnd int
	// comma is the offset of a comma already written after the last
	// member, or -1.
	comma int
	// members is whether the object has any.
	members bool
	// indent is what the new member's line starts with.
	indent string
}

// objectTail finds the end of the top-level object in the original text:
// everything from the last member to the closing brace is whitespace,
// comments and at most one comma, and anything else means the file is
// not the shape this can edit.
func objectTail(data []byte, spans map[string]valueSpan) (objectEnd, bool) {
	end := objectEnd{lastEnd: -1, comma: -1, indent: "  "}
	for _, span := range spans {
		if span.to > end.lastEnd {
			end.lastEnd = span.to
			end.indent = span.indent
			end.members = true
		}
	}
	i := end.lastEnd
	if i < 0 {
		// No members: start after the opening brace, which is the first
		// thing in the file that is not whitespace or a comment.
		clean := stripComments(append([]byte(nil), data...))
		i = bytes.IndexByte(clean, '{')
		if i < 0 {
			return end, false
		}
		i++
	}
	end.at = i
	for i < len(data) {
		c := data[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			// A line comment ends at a carriage return too, when the
			// file has Windows line ends: the new line goes before it.
			end.at = i
			if i > 0 && data[i-1] == '\r' {
				end.at = i - 1
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			close := bytes.Index(data[i+2:], []byte("*/"))
			if close < 0 {
				return end, false
			}
			i += 2 + close + 2
			end.at = i
		case c == ',' && end.comma < 0 && end.members:
			end.comma = i
			i++
			end.at = i
		case c == '}':
			return end, true
		default:
			return end, false
		}
	}
	return end, false
}
