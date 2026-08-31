package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Edit struct{}

func (Edit) Name() string { return "edit" }
func (Edit) Description() string {
	return "Replace an exact substring in a file with new text. old_string must match exactly once unless replace_all is set."
}
func (Edit) smartDescription() string {
	return "Replace an exact substring in a file with new text. old_string must match exactly once " +
		"unless replace_all is set, and it is matched byte for byte — indentation, trailing spaces " +
		"and line endings all count. When it does not match, the result says what is actually in the " +
		"file at the closest place, so you can correct the string rather than guess. A successful " +
		"edit comes back with the changed lines, numbered, as they now stand on disk."
}
func (Edit) InputSchema() json.RawMessage {
	return schema(`{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}}`, "path", "old_string", "new_string")
}
func (e Edit) DescriptionFor(ctx context.Context) string {
	if smartAgent(ctx) {
		return e.smartDescription()
	}
	return e.Description()
}
func (e Edit) InputSchemaFor(ctx context.Context) json.RawMessage { return e.InputSchema() }
func (Edit) RequiresPermission(json.RawMessage) bool              { return true }

// OutsideClass: writing (see WriteFile.OutsideClass).
func (Edit) OutsideClass() OutsideClass { return OutsideWrite }

// Subject exposes the target file path as the permission-rule pattern
// subject (see WriteFile.Subject).
func (Edit) Subject(input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(input, &args)
	return args.Path
}

func (Edit) Execute(ctx context.Context, input json.RawMessage) Result {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	path := resolve(ctx, args.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Content: fmt.Sprintf("read %s: %v", args.Path, err), IsError: true}
	}
	content := string(data)
	smart := smartAgent(ctx)

	count := strings.Count(content, args.OldString)
	if count == 0 {
		if smart {
			return Result{Content: diagnoseMiss(content, args.OldString, args.Path), IsError: true}
		}
		return Result{Content: "old_string not found in file", IsError: true}
	}
	if count > 1 && !args.ReplaceAll {
		if smart {
			return Result{
				Content: fmt.Sprintf("old_string is not unique in %s: %d matches, at line(s) %s. "+
					"Add surrounding lines to old_string so it identifies one of them, or pass "+
					"replace_all to change every one.",
					args.Path, count, matchLines(content, args.OldString)),
				IsError: true,
			}
		}
		return Result{Content: fmt.Sprintf("old_string is not unique (%d matches); pass replace_all or add more context", count), IsError: true}
	}

	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(content, args.OldString, args.NewString)
	} else {
		updated = strings.Replace(content, args.OldString, args.NewString, 1)
	}

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return Result{Content: fmt.Sprintf("write %s: %v", args.Path, err), IsError: true}
	}
	if smart {
		return Result{Content: fmt.Sprintf("replaced %d occurrence(s) in %s\n\n%s",
			count, args.Path, editedRegion(content, updated, args.OldString))}
	}
	return Result{Content: fmt.Sprintf("replaced %d occurrence(s) in %s", count, args.Path)}
}

// Diagnosing a miss.
//
// "old_string not found in file" is a true sentence that helps nobody. It
// is also the most common way an edit fails in every harness that has
// published numbers on it, which is why the edit format a coding agent uses
// is a research topic of its own. A model given nothing but "not found"
// does one of two things: it retries with a string just as wrong, or it
// gives up on edit and rewrites the whole file with write_file — and a
// whole-file rewrite of a file it has only partly read is how an agent
// deletes work.
//
// Almost every real miss is one of a handful of things, and all of them are
// answerable by looking: the indentation is spaces where the file has a
// tab, a trailing space was dropped, the file is CRLF, or the model is
// quoting a line that has since changed. So look, and say which.
//
// What this deliberately does not do is fix it. Matching "close enough"
// and applying the edit anyway would raise the success rate and would be
// wrong in exactly the languages where whitespace is syntax: an edit that
// silently re-indents a Python block, a Makefile recipe or a YAML document
// has changed the program. The tool reports; the model corrects.
func diagnoseMiss(content, old, display string) string {
	msg := fmt.Sprintf("old_string not found in %s.", display)

	if strings.Contains(content, "\r\n") && !strings.Contains(old, "\r\n") {
		return msg + " The file has Windows line endings (CRLF) and old_string has plain newlines, " +
			"so a string spanning more than one line cannot match. Edit one line at a time, or " +
			"include the carriage returns."
	}

	// A whitespace-insensitive comparison of the whole string. When that
	// finds it, the difference is nothing but spacing, and saying so with
	// the exact bytes is enough for the next attempt to land.
	if lines := looseMatchLines(content, old); len(lines) > 0 {
		where := lines[0]
		return msg + fmt.Sprintf(" The same text is at line %d, differing only in whitespace. "+
			"The file has:\n%s\nCopy that exactly — indentation and trailing spaces included.",
			where.line, numbered(where.text, where.line))
	}

	// Failing that, anchor on the first line of old_string alone. This is
	// the case where the model is quoting something real but the rest of
	// its excerpt has drifted, and the line numbers are what it needs.
	first := firstMeaningfulLine(old)
	if first != "" {
		if hits := lineHits(content, first); len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "%s Its first line does appear, at line(s) %s. The file there reads:\n",
				msg, joinInts(hits, 5))
			b.WriteString(numbered(contextLines(content, hits[0], 2), max(hits[0]-2, 1)))
			b.WriteString("Re-read the file and quote it exactly.")
			return b.String()
		}
	}

	return msg + " Nothing resembling it is in the file — re-read the file before editing; " +
		"it may not be the one you meant, or it may have changed since you last read it."
}

// looseWhitespace collapses every run of whitespace to a single space and
// trims the ends, so two texts that differ only in spacing compare equal.
func looseWhitespace(s string) string { return strings.Join(strings.Fields(s), " ") }

type looseHit struct {
	line int
	text []string
}

// looseMatchLines finds windows of the file that equal old once whitespace
// is normalised away.
func looseMatchLines(content, old string) []looseHit {
	want := looseWhitespace(old)
	if want == "" {
		return nil
	}
	lines := splitLines(content)
	span := len(splitLines(old))
	if span == 0 || span > len(lines) {
		return nil
	}
	var out []looseHit
	for i := 0; i+span <= len(lines); i++ {
		window := lines[i : i+span]
		if looseWhitespace(strings.Join(window, "\n")) == want {
			out = append(out, looseHit{line: i + 1, text: window})
			if len(out) == 3 {
				break
			}
		}
	}
	return out
}

// firstMeaningfulLine is the first non-blank line of old_string, trimmed —
// the anchor to look for when the whole thing cannot be found.
func firstMeaningfulLine(old string) string {
	for _, l := range splitLines(old) {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// lineHits is the 1-indexed lines of content whose trimmed text equals want.
func lineHits(content, want string) []int {
	var out []int
	for i, l := range splitLines(content) {
		if strings.TrimSpace(l) == want {
			out = append(out, i+1)
		}
	}
	return out
}

// contextLines is the lines around a 1-indexed line, for quoting back.
func contextLines(content string, at, around int) []string {
	lines := splitLines(content)
	from := max(at-around-1, 0)
	to := min(at+around, len(lines))
	if from >= to {
		return nil
	}
	return lines[from:to]
}

// matchLines is where a substring occurs, as a readable list of line
// numbers — what "3 matches" should have said in the first place.
func matchLines(content, old string) string {
	if old == "" {
		return "unknown"
	}
	var out []int
	offset := 0
	for {
		i := strings.Index(content[offset:], old)
		if i < 0 {
			break
		}
		abs := offset + i
		out = append(out, strings.Count(content[:abs], "\n")+1)
		offset = abs + len(old)
	}
	return joinInts(out, 10)
}

// joinInts renders at most n numbers, saying how many it left off.
func joinInts(in []int, n int) string {
	if len(in) == 0 {
		return "none"
	}
	shown := in
	rest := 0
	if len(in) > n {
		shown, rest = in[:n], len(in)-n
	}
	parts := make([]string, len(shown))
	for i, v := range shown {
		parts[i] = fmt.Sprint(v)
	}
	s := strings.Join(parts, ", ")
	if rest > 0 {
		s += fmt.Sprintf(" and %d more", rest)
	}
	return s
}

// editedRegionContext is how many unchanged lines are shown either side of
// a change.
const editedRegionContext = 3

// editedRegion is the changed part of the file as it now stands, numbered.
//
// The cheapest possible verification step, and the one the orchestration
// prompt asks for in words: "re-read the file you edited". A model that is
// told only "replaced 1 occurrence" has no way to notice that its
// new_string landed at the wrong indentation or that it matched the
// declaration it meant to keep rather than the one below it. Handing the
// lines back closes that loop inside the call that opened it, for the price
// of a few lines instead of a whole second read.
func editedRegion(before, after, old string) string {
	oldLines, newLines := splitLines(before), splitLines(after)

	// The first and last lines that differ, from each end.
	head := 0
	for head < len(oldLines) && head < len(newLines) && oldLines[head] == newLines[head] {
		head++
	}
	if head == len(oldLines) && head == len(newLines) {
		return "(the file is unchanged: new_string is identical to old_string)"
	}
	tail := 0
	for tail < len(oldLines)-head && tail < len(newLines)-head &&
		oldLines[len(oldLines)-1-tail] == newLines[len(newLines)-1-tail] {
		tail++
	}

	from := max(head-editedRegionContext, 0)
	to := min(len(newLines)-tail+editedRegionContext, len(newLines))
	if from >= to {
		return ""
	}
	body := numbered(newLines[from:to], from+1)
	if to-from > 60 {
		body = numbered(newLines[from:from+60], from+1) +
			fmt.Sprintf("… (%d more changed lines, not shown)\n", to-from-60)
	}
	return "The file now reads:\n" + body
}
