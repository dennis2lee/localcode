package memory

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Type is what kind of thing a note records. The four are Claude Code's,
// kept identical so a person who has read one tool's memory reads the
// other's, and so the descriptions the model is given mean the same thing
// everywhere.
type Type string

const (
	// TypeUser is a fact about the person: role, language, how they like
	// to work.
	TypeUser Type = "user"
	// TypeFeedback is a correction the person gave, or a way of working
	// they confirmed.
	TypeFeedback Type = "feedback"
	// TypeProject is a fact about the work that the code, the history and
	// AGENTS.md do not record: a decision and its reason, a quirk.
	TypeProject Type = "project"
	// TypeReference is where something outside the repository lives.
	TypeReference Type = "reference"
	// TypeLegacy is a file written before notes had a type: a topic file
	// with no frontmatter, linked from a hand-written index. Read and
	// listed, never retired, never rewritten.
	TypeLegacy Type = "legacy"
)

// Types is the order the index groups notes in: the two that shape how
// the work is done first, because when the index is over its budget it is
// the end that is not loaded.
var Types = []Type{TypeUser, TypeFeedback, TypeProject, TypeReference}

// ParseType reads a type name, case-insensitively. Legacy is not a type a
// note can be saved as.
func ParseType(s string) (Type, bool) {
	t := Type(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range Types {
		if t == known {
			return t, true
		}
	}
	return "", false
}

// retires reports whether a note of this type is retired when unused.
// A user or feedback note's index line is usually the whole of it: "reply
// in the language of the prompt" is applied every turn without the file
// ever being opened, so an unopened file says nothing about whether it is
// still in use. Only the person, or the model on their behalf, retires
// those.
func (t Type) retires() bool { return t == TypeProject || t == TypeReference }

// Limits on one note.
const (
	maxNameLen        = 64
	maxDescriptionLen = 150
	// MaxBodyBytes is the most a note's text may hold. A note is a fact
	// and the reason for it; anything longer is a document, and a
	// document belongs in the repository, where it is reviewed.
	MaxBodyBytes = 4 * 1024
)

// Entry is one note of project or user scope: a file <Name>.md with a
// frontmatter header.
type Entry struct {
	Name        string
	Description string
	Type        Type
	Body        string
	// Keep exempts a note from retirement: one the person asked to be
	// remembered, whose index line may be all that is ever read of it.
	Keep bool
	// Created and Modified are when the note was first saved and last
	// changed. Written into the note rather than the usage record because
	// they change only when the note does, and because they are what a
	// person reading the file wants to know about it.
	Created  time.Time
	Modified time.Time
}

// File is the note's file name inside its directory.
func (e Entry) File() string { return e.Name + ".md" }

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Type is read from either place: Claude Code writes it under
	// metadata, and a hand-written note is as likely to put it at the
	// top level.
	Type     string   `yaml:"type,omitempty"`
	Metadata metadata `yaml:"metadata"`
}

type metadata struct {
	Type     string `yaml:"type"`
	Keep     bool   `yaml:"keep,omitempty"`
	Created  string `yaml:"created,omitempty"`
	Modified string `yaml:"modified,omitempty"`
}

// ParseEntry reads a note file. A file without frontmatter is a legacy
// topic file: its whole content is the body and its type is TypeLegacy.
// name is the file's base name without ".md", which is the note's name
// whatever the frontmatter says, because the file name is what the index
// links to.
func ParseEntry(name, content string) Entry {
	content = strings.TrimPrefix(content, "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	legacy := Entry{Name: name, Type: TypeLegacy, Body: strings.TrimSpace(content)}
	if !strings.HasPrefix(content, "---\n") {
		return legacy
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return legacy
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return legacy
	}
	typ, ok := ParseType(fm.Metadata.Type)
	if !ok {
		typ, ok = ParseType(fm.Type)
	}
	if !ok {
		// Frontmatter, but not ours: a note somebody started by hand.
		// Readable, and left alone.
		typ = TypeLegacy
	}
	body := rest[end+len("\n---"):]
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	e := Entry{
		Name:        name,
		Description: oneLine(fm.Description),
		Type:        typ,
		Body:        strings.TrimSpace(body),
		Keep:        fm.Metadata.Keep,
	}
	e.Created, _ = time.Parse(time.RFC3339, fm.Metadata.Created)
	e.Modified, _ = time.Parse(time.RFC3339, fm.Metadata.Modified)
	return e
}

// Format renders a note as its file content.
func (e Entry) Format() string {
	var fm struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Metadata    metadata `yaml:"metadata"`
	}
	fm.Name = e.Name
	fm.Description = e.Description
	fm.Metadata.Type = string(e.Type)
	fm.Metadata.Keep = e.Keep
	if !e.Created.IsZero() {
		fm.Metadata.Created = e.Created.UTC().Format(time.RFC3339)
	}
	if !e.Modified.IsZero() {
		fm.Metadata.Modified = e.Modified.UTC().Format(time.RFC3339)
	}
	out, err := yaml.Marshal(fm)
	if err != nil {
		// Three strings cannot fail to marshal; if they somehow did, a
		// note with no header is still a readable note.
		return e.Body + "\n"
	}
	return "---\n" + string(out) + "---\n\n" + strings.TrimSpace(e.Body) + "\n"
}

// reservedNames are names a note cannot take: the index itself (the same
// file as "memory.md" on a case-insensitive disk) and the device names
// Windows will not create a file for.
var reservedNames = map[string]bool{
	"memory": true, "con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ErrBadName is returned for a name that leaves nothing usable once
// normalized.
var ErrBadName = errors.New("a note's name needs at least one ASCII letter or digit, in kebab-case, such as build-needs-cgo")

// NormalizeName turns what the model offered as a name into a file-safe
// one: lower case ASCII letters and digits, words joined by single
// hyphens, at most 64 characters. Lenient on purpose: "Build Commands" and
// "build_commands" are the same note, and a model that guessed the format
// wrong should not lose the note over it.
//
// ASCII only, because a name is a file name: macOS has stored the same
// Korean name in two Unicode forms depending on the disk, and a note
// whose name does not compare equal to its own file cannot be found
// again. What cannot reach the result is a path: no separator, no dot,
// no "..".
func NormalizeName(s string) (string, error) {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".md")) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > maxNameLen {
		name = strings.TrimRight(name[:maxNameLen], "-")
	}
	if name == "" {
		return "", ErrBadName
	}
	if reservedNames[name] {
		return "", fmt.Errorf("%q is reserved; choose another name", name)
	}
	return name, nil
}

// oneLine makes a description fit an index line: one line, no markup that
// could end the generated block it is written into, and at most
// maxDescriptionLen characters.
func oneLine(s string) string {
	s = Sanitize(s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "<!--", "<! --")
	s = strings.ReplaceAll(s, "-->", "-- >")
	if utf8.RuneCountInString(s) > maxDescriptionLen {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:maxDescriptionLen-1])) + "…"
	}
	return s
}

// DescribeFrom is the description a note gets when the model gave none:
// its first line, cut to fit.
func DescribeFrom(body string) string {
	body = strings.TrimSpace(body)
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	return oneLine(strings.TrimLeft(body, "#*-> "))
}
