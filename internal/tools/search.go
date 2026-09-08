package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type Glob struct{}

func (Glob) Name() string        { return "glob" }
func (Glob) Description() string { return "List files matching a glob pattern, e.g. \"src/**/*.go\"." }
func (Glob) smartDescription() string {
	return fmt.Sprintf("List files matching a glob pattern, e.g. \"src/**/*.go\". \"**\" matches any "+
		"number of directories, and the part after it may name directories as well as a filename "+
		"(\"**/cmd/*.go\"). Version-control and package-cache directories are not walked, and the "+
		"listing stops at %d paths — it says so when either happens.", maxGlobResults)
}
func (Glob) InputSchema() json.RawMessage {
	return schema(`{"pattern":{"type":"string"}}`, "pattern")
}
func (g Glob) DescriptionFor(ctx context.Context) string {
	if smartAgent(ctx) {
		return g.smartDescription()
	}
	return g.Description()
}
func (g Glob) InputSchemaFor(ctx context.Context) json.RawMessage { return g.InputSchema() }
func (Glob) RequiresPermission(json.RawMessage) bool              { return false }

// OutsideClass: reading. Listing another project's files is reading it,
// even though nothing is opened.
func (Glob) OutsideClass() OutsideClass { return OutsideRead }

// Subject is the fixed part of the pattern — the directory being listed.
// See globSubject.
func (Glob) Subject(input json.RawMessage) string {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return ""
	}
	return globSubject(args.Pattern)
}

// maxGlobResults bounds one listing. "**/*" at the root of a repository
// with a node_modules in it is six figures of paths, and the model is
// charged for all of them.
const maxGlobResults = 500

func (Glob) Execute(ctx context.Context, input json.RawMessage) Result {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	// Matched inside the session's directory, but reported relative to it.
	// The model asked about "**/*.go" in its workspace and the answer
	// reads best in the same terms — and a relative path it hands to
	// read_file next resolves back to the same file, since every tool
	// resolves against the same directory.
	pattern := resolve(ctx, args.Pattern)
	if !smartAgent(ctx) {
		matches, err := doubleStarGlob(ctx, pattern)
		if walkStopped(err) {
			return stoppedResult()
		}
		if err != nil {
			return Result{Content: fmt.Sprintf("glob %s: %v", args.Pattern, err), IsError: true}
		}
		sort.Strings(matches)
		return Result{Content: strings.Join(relativeTo(WorkingDir(ctx), matches), "\n")}
	}

	matches, notice, err := smartGlob(ctx, pattern)
	if walkStopped(err) {
		return stoppedResult()
	}
	if err != nil {
		return Result{Content: fmt.Sprintf("glob %s: %v", args.Pattern, err), IsError: true}
	}
	sort.Strings(matches)
	out := strings.Join(relativeTo(WorkingDir(ctx), matches), "\n")
	if out == "" {
		out = "no files match " + args.Pattern
	}
	if n := notice.String(); n != "" {
		out += "\n" + n
	}
	return Result{Content: out}
}

// stopWalk is what a directory walk returns when the turn it belongs to
// has been stopped.
//
// Every walk below checks the context — on each directory entry, and
// every few thousand lines inside a file large enough to be a search of
// its own — and hands this back, which filepath.WalkDir treats as "stop
// now". A pattern with no "**" has no walk of ours to check in and is
// handled separately; see globOrStop. Without any of it, pressing stop
// during a search over a large tree ended the turn on paper — the model
// was never asked again — while the walk carried on to the end of the
// disk, holding the turn's goroutine and the session's busy flag with
// it. What somebody who presses stop is asking for is that the work
// ends, and a search is work.
//
// A sentinel rather than ctx.Err() itself, so the caller can tell "the
// person stopped this" from a read error on some directory: the first is
// not worth a message, and the second is.
var stopWalk = errors.New("the search was stopped")

// walkStopped reports whether an error from a walk is that sentinel.
func walkStopped(err error) bool { return errors.Is(err, stopWalk) }

// globOrStop is filepath.Glob, abandoned rather than waited for when the
// turn is stopped.
//
// A pattern with no "**" does not go through a walk of ours: it goes to
// filepath.Glob, which offers nothing to check a context at. That is not
// the cheap case it sounds like — "*/*/*/*.go" over a project measured
// at 950ms, and it went on running after a stop and returned its whole
// answer, which is the fault this file exists to have fixed.
//
// So the glob runs on its own goroutine and this waits on whichever
// comes first. Abandoning it is safe in a way abandoning most work is
// not: it reads directory entries and returns a list, touching nothing
// and holding nothing, so a goroutine left behind finishes on its own
// and its answer is dropped. The channel is buffered so that goroutine
// never blocks on a send nobody is waiting for.
func globOrStop(ctx context.Context, pattern string) ([]string, error) {
	type answer struct {
		out []string
		err error
	}
	done := make(chan answer, 1)
	go func() {
		out, err := filepath.Glob(pattern)
		done <- answer{out, err}
	}()
	select {
	case a := <-done:
		return a.out, a.err
	case <-ctx.Done():
		return nil, stopWalk
	}
}

// stoppedResult is what a search returns when it was stopped part way:
// the same words the agent loop writes for a tool it did not start, so a
// transcript reads the same whether the stop landed before a tool or
// inside one. See tools.CancelledResult.
func stoppedResult() Result { return CancelledResult() }

// doubleStarGlob supports "**" (recursive) in addition to filepath.Glob's
// single-level "*", since that's the pattern models reach for by default.
func doubleStarGlob(ctx context.Context, pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return globOrStop(ctx, pattern)
	}

	parts := strings.SplitN(pattern, "**", 2)
	root := strings.TrimSuffix(parts[0], "/")
	if root == "" {
		root = "."
	}
	suffix := strings.TrimPrefix(parts[1], "/")

	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return stopWalk
		}
		if err != nil || d.IsDir() {
			return nil
		}
		if suffix == "" {
			out = append(out, path)
			return nil
		}
		ok, _ := filepath.Match(suffix, filepath.Base(path))
		if ok {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// smartGlob is doubleStarGlob with three differences, each of them a case
// the plain one got wrong quietly.
//
// It does not walk version-control internals or package caches, and says
// which it left out. It stops at maxGlobResults and says that too. And it
// matches the part after "**" against the path below the walk root as well
// as against the filename, which is what makes "**/cmd/*.go" find
// anything — before, that pattern was compared to "main.go" alone and
// returned nothing at all, which reads exactly like a project that has no
// such files.
func smartGlob(ctx context.Context, pattern string) ([]string, walkNotice, error) {
	var notice walkNotice
	notice.limit = maxGlobResults

	if !strings.Contains(pattern, "**") {
		out, err := globOrStop(ctx, pattern)
		if len(out) > maxGlobResults {
			out, notice.capped = out[:maxGlobResults], true
		}
		return out, notice, err
	}

	parts := strings.SplitN(pattern, "**", 2)
	root := strings.TrimSuffix(parts[0], "/")
	if root == "" {
		root = "."
	}
	suffix := strings.TrimPrefix(parts[1], "/")

	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return stopWalk
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir(root, path, d.Name()) {
				notice.skipped = append(notice.skipped, d.Name())
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= maxGlobResults {
			notice.capped = true
			return filepath.SkipAll
		}
		if suffix == "" || globSuffixMatch(suffix, root, path) {
			out = append(out, path)
		}
		return nil
	})
	return out, notice, err
}

// globSuffixMatch matches the part of a "**" pattern after the stars
// against a file.
//
// "**" stands for any number of directories, so what has to match is a
// trailing run of segments, not the whole path below the walk root: for
// "**/localcode/*.go" and cmd/localcode/main.go the part that matches is
// the last two segments. Every suffix is tried, shortest last, and the
// shortest is the filename alone — which is the only comparison the plain
// glob ever made, and why a pattern naming a directory after the stars
// used to match nothing at all.
func globSuffixMatch(suffix, root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	for i := range segs {
		if ok, _ := filepath.Match(suffix, strings.Join(segs[i:], "/")); ok {
			return true
		}
	}
	return false
}

type Grep struct{}

func (Grep) Name() string { return "grep" }
func (Grep) Description() string {
	return fmt.Sprintf("Search file contents for a regex pattern under a path (recursive). "+
		"Results are \"file:line:text\". The search stops after %d matches, and says so when it "+
		"does, along with any file it could not finish reading — a short answer is never left "+
		"looking like a complete one.", maxMatches)
}
func (Grep) smartDescription() string {
	return fmt.Sprintf("Search file contents for a regex pattern under a path (recursive). "+
		"Results are \"file:line:text\". Binary files and version-control or package-cache "+
		"directories are skipped, no single file may contribute more than %d matches, and the "+
		"search stops after %d — whenever any of that happens the result says so, so a short "+
		"answer is never mistaken for a complete one.", maxPerFileMatches, maxMatches)
}
func (Grep) InputSchema() json.RawMessage {
	return schema(`{"pattern":{"type":"string"},"path":{"type":"string","description":"file or directory to search; defaults to \".\""}}`, "pattern")
}
func (g Grep) DescriptionFor(ctx context.Context) string {
	if smartAgent(ctx) {
		return g.smartDescription()
	}
	return g.Description()
}
func (g Grep) InputSchemaFor(ctx context.Context) json.RawMessage { return g.InputSchema() }
func (Grep) RequiresPermission(json.RawMessage) bool              { return false }

// OutsideClass: reading. A grep reads every file under its path, which
// is more of somebody else's project than a read_file ever is.
func (Grep) OutsideClass() OutsideClass { return OutsideRead }

// Subject is the path being searched, defaulting to the workspace itself
// the same way Execute does.
func (Grep) Subject(input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return ""
	}
	if args.Path == "" {
		return "."
	}
	return args.Path
}

const maxMatches = 200

// maxPerFileMatches stops one file from spending the whole budget.
//
// The failure it fixes was measured, not imagined: a directory holding a
// generated table with five hundred hits returned 199 of them and one
// match from a .git pack object, and the three real source files under the
// same root were never reached. The answer had the same shape a complete
// one has.
const maxPerFileMatches = 30

// maxMatchLine caps how much of a matching line comes back. A match inside
// a minified bundle is one line of two hundred thousand characters, and
// returning it whole spends a quarter of the context window on a single
// result.
const maxMatchLine = 400

func (Grep) Execute(ctx context.Context, input json.RawMessage) Result {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}
	if args.Path == "" {
		args.Path = "."
	}

	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return Result{Content: fmt.Sprintf("invalid regex: %v", err), IsError: true}
	}

	body, notice, walkErr := grepWalk(ctx, re, resolve(ctx, args.Path), WorkingDir(ctx), smartAgent(ctx))
	if walkStopped(walkErr) {
		return stoppedResult()
	}
	if walkErr != nil {
		return Result{Content: fmt.Sprintf("grep %s: %v", args.Path, walkErr), IsError: true}
	}
	out := body
	if out == "" {
		out = "no matches"
	}
	// The notice goes out even when there were no matches at all, and that
	// is the case where it matters most. "no matches" from a search that
	// could not read a third of what it walked is the answer that sends a
	// model off to write code which already exists.
	if n := notice.String(); n != "" {
		out += "\n" + n
	}
	return Result{Content: out}
}

// ctxCheckLines is how often the scan of a single file looks at whether
// the turn is still wanted. Every 4,096 lines is under a millisecond of
// scanning between checks and is not measurable against the regex.
const ctxCheckLines = 4096

// grepWalk is the search, in one function for both settings.
//
// Three of the things it does are not Smart Agent's and never were,
// because they are not improvements to an answer. Each is a place the
// search did not look and did not say so:
//
//   - It says when the match budget ran out. Returning 200 lines with the
//     shape of a complete result is a claim that the tree holds 200
//     matches.
//   - It reads lines up to maxLineBytes and reports a file it could not
//     finish. A file with one very long line used to end its own scan with
//     no error anybody checked, and the file came back clean.
//   - It names a file it could not open. Rare, and the same shape: a file
//     nothing looked inside is not a file with no matches.
//
// Everything else here is behind smart, and each of those is a judgement
// about what is worth looking at rather than a correction: not walking
// version-control internals, skipping binaries, bounding one file's share
// of the budget, clipping an enormous matching line. Those change which
// answer is best; the three above change whether the answer is true.
func grepWalk(ctx context.Context, re *regexp.Regexp, root, base string, smart bool) (string, walkNotice, error) {
	var b strings.Builder
	var notice walkNotice
	notice.limit = maxMatches
	total := 0

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return stopWalk
		}
		if err != nil {
			notice.unreadable = append(notice.unreadable, relName(base, path))
			return nil
		}
		if d.IsDir() {
			if smart && skipDir(root, path, d.Name()) {
				notice.skipped = append(notice.skipped, d.Name())
				return filepath.SkipDir
			}
			return nil
		}
		if total >= maxMatches {
			notice.capped = true
			return filepath.SkipAll
		}

		f, err := os.Open(path)
		if err != nil {
			// Skipped, and named. A permission-denied file or a dangling
			// symlink is rare enough that this is quiet in a source tree,
			// and when it does fire the name is the whole value of it:
			// a count repeats forever and cannot be acted on.
			notice.unreadable = append(notice.unreadable, relName(base, path))
			return nil
		}
		defer f.Close()

		if smart {
			head := make([]byte, binarySniff)
			n, readErr := f.Read(head)
			if readErr != nil && readErr != io.EOF {
				notice.unreadable = append(notice.unreadable, relName(base, path))
				return nil
			}
			if looksBinary(head[:n]) {
				notice.binary++
				return nil
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				notice.unreadable = append(notice.unreadable, relName(base, path))
				return nil
			}
		}

		name := relName(base, path)
		scanner := lineScanner(f)
		lineNo, inFile := 0, 0
		for scanner.Scan() {
			lineNo++
			// One file can be most of a search: the walk checks the
			// context between files, and a log of a few hundred megabytes
			// is a single entry. Checked every so many lines rather than
			// every line, because ctx.Err() takes a lock and the inner
			// loop here runs once per line of every file in the tree.
			if lineNo%ctxCheckLines == 0 && ctx.Err() != nil {
				return stopWalk
			}
			if !re.MatchString(scanner.Text()) {
				continue
			}
			inFile++
			if smart && inFile > maxPerFileMatches {
				continue
			}
			line := scanner.Text()
			if smart {
				line = clipLine(line)
			}
			fmt.Fprintf(&b, "%s:%d:%s\n", name, lineNo, line)
			total++
			if total >= maxMatches {
				notice.capped = true
				break
			}
		}
		if scanner.Err() != nil {
			// bufio.ErrTooLong, in practice. Scan stopped, the rest of the
			// file was never searched, and without this the caller is told
			// the file is clean.
			notice.tooLong = append(notice.tooLong, name)
		}
		if smart && inFile > maxPerFileMatches {
			fmt.Fprintf(&b, "%s: %d more match(es) in this file, not shown\n", name, inFile-maxPerFileMatches)
		}
		return nil
	})
	return b.String(), notice, walkErr
}

// clipLine cuts one matching line down to something worth quoting, on a
// rune boundary — cutting mid-rune would put a replacement character into
// the model's context and, worse, into any path or identifier it copies
// back out of the result.
func clipLine(s string) string {
	if len(s) <= maxMatchLine {
		return s
	}
	cut := maxMatchLine
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf(" … (+%d bytes)", len(s)-cut)
}
