package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Smart Agent agent-computer interface.
//
// Each of these pins one way the plain tools answered a question wrongly
// rather than incompletely — a search that reported "no matches" for a file
// containing the pattern, a cap that returned a short list shaped exactly
// like a complete one, an edit failure that named no cause. The point of
// every assertion here is the same: the model is told what it was not
// shown.

func smartCtx(dir string) context.Context {
	return WithSmartAgent(WithWorkingDir(context.Background(), dir), true)
}

func plainCtx(dir string) context.Context {
	return WithWorkingDir(context.Background(), dir)
}

func write(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return full
}

func grep(ctx context.Context, pattern string) Result {
	in, _ := json.Marshal(map[string]string{"pattern": pattern})
	return Grep{}.Execute(ctx, in)
}

// The measured failure, restated as a test: one generated file with five
// hundred hits used to take 199 of the 200 available slots, a .git pack
// object took the other, and three real source files under the same root
// were never reached — with nothing in the answer to say so.
func TestOneCrowdedFileNoLongerHidesTheRestOfTheRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".git/objects/pack", "needle\x00binary needle\n")
	write(t, dir, "generated.txt", strings.Repeat("needle\n", 500))
	write(t, dir, "real.go", "package p // needle\n")
	write(t, dir, "node_modules/left-pad/index.js", "needle\n")

	plain := grep(plainCtx(dir), "needle")
	if strings.Contains(plain.Content, "real.go") {
		t.Fatalf("the plain grep was supposed to lose real.go; it did not:\n%s", plain.Content)
	}

	res := grep(smartCtx(dir), "needle")
	if !strings.Contains(res.Content, "real.go:1:") {
		t.Errorf("real.go is still missing from the results:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "500 needle") && !strings.Contains(res.Content, "more match(es) in this file") {
		t.Errorf("the crowded file did not say how much it held back:\n%s", res.Content)
	}
	if strings.Contains(res.Content, ".git/objects") {
		t.Errorf(".git was searched:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "node_modules") && !strings.Contains(res.Content, "did not descend") {
		t.Errorf("node_modules was searched:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "did not descend into .git, node_modules") {
		t.Errorf("the skipped directories were not named:\n%s", res.Content)
	}
}

// A single long line used to end the scan of its file with no error the
// caller checked, so a pattern present twice came back as "no matches".
// A false negative is the one answer a search must never give: the model
// concludes the symbol does not exist and writes it again.
//
// Not behind the switch. This is not a better answer than the old one, it
// is a true one instead of a false one, and nobody opts into that.
func TestALongLineNoLongerHidesEverythingBelowIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "minified.js", strings.Repeat("x", 200_000)+"\nneedle late\n")

	for name, ctx := range map[string]context.Context{"off": plainCtx(dir), "on": smartCtx(dir)} {
		if got := grep(ctx, "needle").Content; !strings.Contains(got, "minified.js:2:needle late") {
			t.Errorf("with the switch %s, the match after the long line was not found:\n%s", name, got)
		}
	}
}

// Past a megabyte the line is not searched either. The difference is that
// the answer names the file it could not finish instead of reporting it
// clean — which is the same defect one size up, so it is unconditional
// too.
func TestAFileTooLongToSearchIsNamedRatherThanCalledClean(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "huge.txt", strings.Repeat("x", 2<<20)+"\nneedle\n")

	for name, ctx := range map[string]context.Context{"off": plainCtx(dir), "on": smartCtx(dir)} {
		got := grep(ctx, "needle").Content
		if !strings.Contains(got, "huge.txt") || !strings.Contains(got, "longer than") {
			t.Errorf("with the switch %s, the unsearchable file was not reported:\n%s", name, got)
		}
	}
}

func TestBinaryFilesAreSkippedAndCounted(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "image.png", "\x89PNG\x00\x00needle\n")
	write(t, dir, "notes.txt", "needle\n")

	res := grep(smartCtx(dir), "needle")
	if strings.Contains(res.Content, "image.png:") {
		t.Errorf("a binary file was quoted back:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "skipped 1 binary file") {
		t.Errorf("the skipped binary was not counted:\n%s", res.Content)
	}
}

// The total cap is the one that used to be entirely silent: 200 lines,
// shaped exactly like a complete answer, which is a claim that the tree
// holds 200 matches. Unconditional, for the same reason as the two above.
func TestTheSearchBudgetSaysWhenItRanOut(t *testing.T) {
	dir := t.TempDir()
	for i := range 20 {
		write(t, dir, fmt.Sprintf("f%02d.txt", i), strings.Repeat("needle\n", 25))
	}
	for name, ctx := range map[string]context.Context{"off": plainCtx(dir), "on": smartCtx(dir)} {
		got := grep(ctx, "needle").Content
		if !strings.Contains(got, "stopped at the 200-result limit") {
			t.Errorf("with the switch %s, the budget ran out without saying so:\n%s", name, lastLines(got, 2))
		}
	}
}

// A file nothing could open is not a file with no matches. Same shape as
// the two above, rarer, and unconditional for the same reason.
//
// Named rather than counted: "could not open 1 file(s)" on every search of
// a project with one dangling symlink is a line that repeats forever and
// cannot be acted on. The name is a thing to go and delete.
func TestAFileThatCouldNotBeOpenedIsNamed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ok.txt", "needle\n")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	for name, ctx := range map[string]context.Context{"off": plainCtx(dir), "on": smartCtx(dir)} {
		got := grep(ctx, "needle").Content
		if !strings.Contains(got, "ok.txt:1:needle") {
			t.Errorf("with the switch %s, the readable file was lost:\n%s", name, got)
		}
		if !strings.Contains(got, "could not open dangling") {
			t.Errorf("with the switch %s, the unopenable file was not named:\n%s", name, got)
		}
	}
}

// Past a few names the list stops being a name and becomes a wall.
func TestALongListOfNamesIsCutShortRatherThanPastedWhole(t *testing.T) {
	got := walkNotice{unreadable: []string{"e", "d", "c", "b", "a"}}.String()
	if got != "[could not open a, b, c and 2 more]" {
		t.Errorf("notice = %q", got)
	}
	if dup := (walkNotice{skipped: []string{"x", "x", "y"}}).String(); dup != "[did not descend into x, y]" {
		t.Errorf("repeats were not folded: %q", dup)
	}
}

// And a search that withheld nothing says nothing. A notice on every
// result is a notice nobody reads, and it would also mean no plain grep
// ever returns bare "file:line:text" again.
func TestASearchThatWithheldNothingSaysNothing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "hello\nneedle\n")

	for name, ctx := range map[string]context.Context{"off": plainCtx(dir), "on": smartCtx(dir)} {
		if got := grep(ctx, "needle").Content; got != "a.txt:2:needle\n" {
			t.Errorf("with the switch %s, a complete answer carried something extra: %q", name, got)
		}
		if got := grep(ctx, "NOPE").Content; got != "no matches" {
			t.Errorf("with the switch %s, an empty answer carried something extra: %q", name, got)
		}
	}
}

// A match inside a minified line is one line of two hundred thousand
// characters. Quoting it whole spends a quarter of the window on one hit.
func TestAVeryLongMatchingLineIsClippedOnARuneBoundary(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "cjk.txt", "needle "+strings.Repeat("한글", 2000)+"\n")

	res := grep(smartCtx(dir), "needle")
	if !strings.Contains(res.Content, "bytes)") {
		t.Errorf("the long line was not clipped:\n%s", res.Content)
	}
	if strings.ContainsRune(res.Content, '�') {
		t.Errorf("the clip split a rune:\n%s", res.Content)
	}
}

// "**/cmd/*.go" used to be compared against "main.go" alone and match
// nothing — which reads exactly like a project that has no such files.
func TestADoubleStarPatternCanNameDirectoriesAfterTheStars(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "cmd/localcode/main.go", "package main")
	write(t, dir, "internal/agent/loop.go", "package agent")

	in, _ := json.Marshal(map[string]string{"pattern": "**/localcode/*.go"})
	if plain := (Glob{}).Execute(plainCtx(dir), in); strings.Contains(plain.Content, "main.go") {
		t.Fatalf("the plain glob was supposed to miss this pattern; it found %q", plain.Content)
	}

	res := Glob{}.Execute(smartCtx(dir), in)
	if !strings.Contains(res.Content, filepath.Join("cmd", "localcode", "main.go")) {
		t.Errorf("the pattern did not find the file:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "loop.go") {
		t.Errorf("the pattern matched something it should not have:\n%s", res.Content)
	}
}

func TestGlobDoesNotListPackageCaches(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a")
	write(t, dir, "node_modules/x/b.go", "package b")

	in, _ := json.Marshal(map[string]string{"pattern": "**/*.go"})
	res := Glob{}.Execute(smartCtx(dir), in)
	if strings.Contains(res.Content, "node_modules") && !strings.Contains(res.Content, "did not descend") {
		t.Errorf("node_modules was listed:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("the real file is missing:\n%s", res.Content)
	}
}

func readFile(ctx context.Context, args map[string]any) Result {
	in, _ := json.Marshal(args)
	return ReadFile{}.Execute(ctx, in)
}

func TestALongFileArrivesOneWindowAtATimeAndSaysHowMuchIsLeft(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	write(t, dir, "long.txt", b.String())

	res := readFile(smartCtx(dir), map[string]any{"path": "long.txt"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "     1\tline 1\n") {
		t.Errorf("the window did not start at the top:\n%s", firstLines(res.Content, 3))
	}
	if strings.Contains(res.Content, "line 801\n") {
		t.Errorf("the window did not stop at the default size")
	}
	if !strings.Contains(res.Content, "[lines 1-800 of 2000 in long.txt; read on with offset=801]") {
		t.Errorf("the footer did not say what was left:\n%s", lastLines(res.Content, 2))
	}
}

func TestAWindowedReadCanBeAskedForAnyPartOfTheFile(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	write(t, dir, "short.txt", b.String())

	res := readFile(smartCtx(dir), map[string]any{"path": "short.txt", "offset": 50, "limit": 3})
	if !strings.Contains(res.Content, "    50\tline 50\n") || !strings.Contains(res.Content, "    52\tline 52\n") {
		t.Errorf("the requested window is wrong:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "line 53\n") {
		t.Errorf("the limit was not honoured:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "[lines 50-52 of 100 in short.txt; read on with offset=53]") {
		t.Errorf("the footer is wrong:\n%s", res.Content)
	}

	past := readFile(smartCtx(dir), map[string]any{"path": "short.txt", "offset": 500})
	if !past.IsError || !strings.Contains(past.Content, "past the end") {
		t.Errorf("an offset past the end was not reported: %q", past.Content)
	}
}

// A short file has nothing withheld, so it gets no footer — the footer is
// there to distinguish a partial answer from a whole one, and one on every
// read would train the model to stop reading it.
func TestAFileThatFitsComesBackWithNoFooter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "small.txt", "one\ntwo\n")
	res := readFile(smartCtx(dir), map[string]any{"path": "small.txt"})
	if strings.Contains(res.Content, "[lines") {
		t.Errorf("a complete read was footnoted:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "     3\t") {
		t.Errorf("the trailing newline was numbered as a third line:\n%q", res.Content)
	}
}

func TestABinaryFileIsDescribedRatherThanRenderedAsText(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	res := readFile(smartCtx(dir), map[string]any{"path": "logo.png"})
	if !res.IsError || !strings.Contains(res.Content, "looks like a binary file") {
		t.Errorf("a binary file was read as text: %q", res.Content)
	}
}

func edit(ctx context.Context, args map[string]any) Result {
	in, _ := json.Marshal(args)
	return Edit{}.Execute(ctx, in)
}

// The most common way an edit fails, and the reason the edit format a
// coding agent uses is a research topic of its own. "not found" sends the
// model either to the same wrong string again or to write_file, which
// discards the rest of the file.
func TestAWhitespaceOnlyMissSaysWhereAndWhat(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package p\n\nfunc f() {\n\treturn 1\n}\n")

	// Four spaces where the file has a tab.
	res := edit(smartCtx(dir), map[string]any{
		"path": "main.go", "old_string": "    return 1", "new_string": "    return 2",
	})
	if !res.IsError {
		t.Fatal("the edit was applied; a whitespace-loose match must not be")
	}
	if !strings.Contains(res.Content, "line 4") || !strings.Contains(res.Content, "differing only in whitespace") {
		t.Errorf("the miss was not diagnosed:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "\treturn 1") {
		t.Errorf("the actual bytes were not shown:\n%s", res.Content)
	}

	// And the file is untouched: reporting is not repairing. In Python, a
	// Makefile or a YAML document, re-indenting to make an edit apply
	// changes the program.
	after, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(after), "\treturn 1") {
		t.Errorf("the file was changed by a failed edit: %q", after)
	}
}

func TestACrlfFileSaysSoWhenAMultiLineEditCannotMatch(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "win.txt", "alpha\r\nbeta\r\ngamma\r\n")
	res := edit(smartCtx(dir), map[string]any{
		"path": "win.txt", "old_string": "alpha\nbeta", "new_string": "alpha\ndelta",
	})
	if !res.IsError || !strings.Contains(res.Content, "CRLF") {
		t.Errorf("the line endings were not named:\n%s", res.Content)
	}
}

func TestANonUniqueEditIsToldWhereTheMatchesAre(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.txt", "a\nsame\nb\nsame\nc\nsame\n")
	res := edit(smartCtx(dir), map[string]any{
		"path": "x.txt", "old_string": "same", "new_string": "other",
	})
	if !res.IsError {
		t.Fatal("a non-unique edit was applied")
	}
	if !strings.Contains(res.Content, "line(s) 2, 4, 6") {
		t.Errorf("the match positions were not given:\n%s", res.Content)
	}
}

func TestAnEditWithNothingLikeItSaysToReadTheFileAgain(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.txt", "nothing resembling the request\n")
	res := edit(smartCtx(dir), map[string]any{
		"path": "x.txt", "old_string": "func Whatever() {", "new_string": "x",
	})
	if !res.IsError || !strings.Contains(res.Content, "re-read the file") {
		t.Errorf("no recovery was suggested:\n%s", res.Content)
	}
}

// The cheapest verification there is: the model sees what its edit
// produced, in the call that produced it, instead of being told a count.
func TestASuccessfulEditHandsBackTheLinesItChanged(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package p\n\nfunc f() int {\n\treturn 1\n}\n\nfunc g() {}\n")
	res := edit(smartCtx(dir), map[string]any{
		"path": "main.go", "old_string": "\treturn 1", "new_string": "\treturn 2",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "     4\t\treturn 2") {
		t.Errorf("the changed line was not shown as it now stands:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "     3\tfunc f() int {") {
		t.Errorf("no surrounding context was shown:\n%s", res.Content)
	}
}

func TestWriteFileSaysWhatItDestroyed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "big.go", strings.Repeat("// a line\n", 340))

	in, _ := json.Marshal(map[string]string{"path": "big.go", "content": "package p\n"})
	res := WriteFile{}.Execute(smartCtx(dir), in)
	if !strings.Contains(res.Content, "it had 340 line(s), it now has 1") {
		t.Errorf("the overwrite did not report what it replaced: %q", res.Content)
	}

	in2, _ := json.Marshal(map[string]string{"path": "new.go", "content": "package p\n"})
	res2 := WriteFile{}.Execute(smartCtx(dir), in2)
	if !strings.Contains(res2.Content, "created new.go") {
		t.Errorf("a new file was not reported as created: %q", res2.Content)
	}
}

// Off, these answer exactly as they did the day before any of this was
// written. That is the contract of an opt-in, and it is asserted string
// for string rather than described.
//
// grep is the deliberate exception and is not in this list: two of its
// answers were wrong rather than merely plain, and those are fixed with
// the switch off. See TestTheSearchBudgetSaysWhenItRanOut and the two
// above it.
func TestWithSmartAgentOffTheRestAnswerExactlyAsTheyDidBefore(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f.txt", "one\ntwo\n")
	write(t, dir, "dup.txt", "same\nsame\n")

	ctx := plainCtx(dir)

	if got := readFile(ctx, map[string]any{"path": "f.txt"}).Content; got != "     1\tone\n     2\ttwo\n     3\t\n" {
		t.Errorf("read_file changed off-switch: %q", got)
	}
	miss := edit(ctx, map[string]any{"path": "f.txt", "old_string": "  one", "new_string": "x"})
	if miss.Content != "old_string not found in file" {
		t.Errorf("edit's miss changed off-switch: %q", miss.Content)
	}
	dup := edit(ctx, map[string]any{"path": "dup.txt", "old_string": "same", "new_string": "x"})
	if !strings.HasPrefix(dup.Content, "old_string is not unique (2 matches)") {
		t.Errorf("edit's ambiguity changed off-switch: %q", dup.Content)
	}
	ok := edit(ctx, map[string]any{"path": "f.txt", "old_string": "one", "new_string": "uno"})
	if ok.Content != "replaced 1 occurrence(s) in f.txt" {
		t.Errorf("edit's success changed off-switch: %q", ok.Content)
	}
	in, _ := json.Marshal(map[string]string{"path": "w.txt", "content": "hi"})
	if got := (WriteFile{}).Execute(ctx, in).Content; got != "wrote 2 bytes to w.txt" {
		t.Errorf("write_file changed off-switch: %q", got)
	}
}

// The schemas and descriptions follow the same switch as the behaviour.
// They have to: a turn shown a read_file that takes offset and limit, and
// then given one that ignores them, is a turn whose tool calls fail for a
// reason nothing in the conversation explains.
func TestTheAdvertisedInterfaceFollowsTheSwitch(t *testing.T) {
	off, on := context.Background(), WithSmartAgent(context.Background(), true)

	if s := string(ReadFile{}.InputSchemaFor(off)); strings.Contains(s, "offset") {
		t.Errorf("read_file advertised paging with the switch off: %s", s)
	}
	if s := string(ReadFile{}.InputSchemaFor(on)); !strings.Contains(s, "offset") || !strings.Contains(s, "limit") {
		t.Errorf("read_file did not advertise paging with the switch on: %s", s)
	}
	for _, tc := range []struct {
		name string
		got  func(context.Context) string
	}{
		{"read_file", ReadFile{}.DescriptionFor},
		{"edit", Edit{}.DescriptionFor},
		{"grep", Grep{}.DescriptionFor},
		{"glob", Glob{}.DescriptionFor},
		{"write_file", WriteFile{}.DescriptionFor},
	} {
		if tc.got(off) == tc.got(on) {
			t.Errorf("%s describes itself identically either side of the switch", tc.name)
		}
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	return strings.Join(parts[:min(n, len(parts))], "\n")
}

func lastLines(s string, n int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return strings.Join(parts, "\n")
}
