package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func storedManifest(id, secret string) Manifest {
	return Manifest{
		ID: id, At: time.Now(), Agent: "general-purpose", Role: RoleOrchestrator,
		Model: "claude-opus-5", Provider: "anthropic", Family: "opus",
		Selected: []Entry{{ID: "project.rules", Kind: KindProjectInstruction,
			Provenance: FromWorkspace, Trust: TrustProject, Placement: PlaceSystem,
			Reason: "the workspace's own rules", Hash: hashOf(secret), Tokens: estimateTokens(secret)}},
		Excluded: []Exclusion{{ID: "smart.orchestration", Reason: "smart agent is off"}},
		Lowering: []string{"3 system blocks folded into one system string"},
		Warnings: []string{"memory.index is durable_reload but comes after a less stable asset"},
	}
}

// R12N3. A manifest that only exists during the call it describes
// answers the wrong question: the trace records an id, and the id has to
// lead somewhere. It leads to the full record, including the halves the
// trace never carried — exclusions with reasons, warnings, and the
// adapter lowering.
func TestAManifestIsRetrievableByItsIDAfterARestart(t *testing.T) {
	dir := t.TempDir()
	const secret = "the project's private instructions"

	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Put(storedManifest("abc123", secret))

	// A different process, with nothing in memory: the file is the
	// record that lasts.
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get("abc123")
	if !ok {
		t.Fatal("the manifest could not be resolved after a restart")
	}
	if got.Model != "claude-opus-5" || got.Role != RoleOrchestrator {
		t.Errorf("the activation facts did not survive: %+v", got)
	}
	if len(got.Selected) != 1 || got.Selected[0].Reason == "" || got.Selected[0].Hash == "" {
		t.Errorf("selected entries lost their reasons or hashes: %+v", got.Selected)
	}
	if len(got.Excluded) != 1 || !strings.Contains(got.Excluded[0].Reason, "smart agent is off") {
		t.Errorf("exclusions did not survive: %+v", got.Excluded)
	}
	if len(got.Lowering) != 1 || len(got.Warnings) != 1 {
		t.Errorf("lowering or warnings did not survive: %+v %+v", got.Lowering, got.Warnings)
	}

	// Redacted by construction: a manifest never held a body, which is
	// the only reason this is safe to write to disk at all.
	raw, rerr := os.ReadFile(filepath.Join(dir, "manifests-"+time.Now().Format("2006-01-02")+".jsonl"))
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("the stored manifest contains the rendered body")
	}

	if _, ok := reopened.Get("nothing-here"); ok {
		t.Error("an id that was never stored resolved anyway")
	}
}

// The store ages with the trace it sits beside, and for the same reason:
// a manifest whose trace line has been pruned is not worth keeping, and
// one whose trace line survives must still resolve.
func TestManifestRetentionMatchesTheTrace(t *testing.T) {
	dir := t.TempDir()
	day := func(daysAgo int) string {
		return filepath.Join(dir, "manifests-"+time.Now().AddDate(0, 0, -daysAgo).Format("2006-01-02")+".jsonl")
	}
	for _, p := range []string{day(40), day(100), day(0)} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Open deletes nothing, exactly as the trace writer does not: a
	// longer configured retention has to be installed before anything
	// irreversible happens.
	if _, err := os.Stat(day(100)); err != nil {
		t.Error("open pruned before any retention was configured")
	}
	s.SetRetention(90, 0)
	if _, err := os.Stat(day(40)); err != nil {
		t.Error("a 40-day manifest file was deleted under a 90-day retention")
	}
	if _, err := os.Stat(day(100)); !os.IsNotExist(err) {
		t.Error("a 100-day manifest file survived a 90-day retention")
	}
	if _, err := os.Stat(day(0)); err != nil {
		t.Error("today's file was pruned")
	}
}

// Nil is a store that records nothing, so a Loop without one does not
// have to guard every call site.
func TestANilManifestStoreIsSafe(t *testing.T) {
	var s *Store
	s.Put(storedManifest("x", "y"))
	s.SetRetention(10, 0)
	if _, ok := s.Get("x"); ok {
		t.Error("a nil store returned a manifest")
	}
}

// Two assemblies that differ only in adapter lowering are different
// requests, and conflating them would defeat the record.
func TestLoweringChangesTheManifestID(t *testing.T) {
	base := Manifest{Agent: "a", Model: "m", Selected: []Entry{{ID: "x", Hash: "h"}}}
	base.ID = manifestID(base)
	lowered := base.WithLowering("3 system blocks folded into one system string")
	if lowered.ID == base.ID {
		t.Error("a lowering left the manifest id unchanged, so two different requests share one identity")
	}
	if len(base.Lowering) != 0 {
		t.Error("WithLowering mutated the manifest it was called on")
	}
}

// R13N5. The trace beside this store has had a size bound since v0.55.0;
// the store took only the age bound, which leaves a busy fortnight
// filling a disk while every file is comfortably inside its age window.
func TestManifestRetentionAppliesTheSizeBoundToo(t *testing.T) {
	dir := t.TempDir()
	day := func(daysAgo int) string {
		return filepath.Join(dir, "manifests-"+time.Now().AddDate(0, 0, -daysAgo).Format("2006-01-02")+".jsonl")
	}
	big := make([]byte, 700*1024)
	for i := range big {
		big[i] = 'x'
	}
	for _, p := range []string{day(3), day(2), day(1), day(0)} {
		if err := os.WriteFile(p, big, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Every file is one day old at most, so age alone removes nothing.
	s.SetRetention(30, 1)

	if _, err := os.Stat(day(3)); !os.IsNotExist(err) {
		t.Error("the oldest file survived a 1MB total bound")
	}
	if _, err := os.Stat(day(0)); err != nil {
		t.Error("today's file was removed, and it is the one being written")
	}
}

// A manifest is immutable content addressed by its own id. Recording the
// same assembly on every turn of a long session used to cost one full
// record per turn, and it is also what made the two lookups disagree.
func TestTheSameAssemblyIsWrittenOnce(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 5; i++ {
		s.Put(storedManifest("dup1", "the project's private instructions"))
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifests-"+time.Now().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; n != 1 {
		t.Errorf("five identical Puts wrote %d records, want 1", n)
	}
}

// The memory ring keeps the first manifest recorded under an id. The file
// lookup used to return the last matching record, so "/context <id>"
// could answer differently before and after a restart while describing an
// assembly that had not changed.
func TestTheMemoryAndFileLookupsAgree(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	first := storedManifest("same", "one")
	first.Warnings = []string{"the first record"}
	s.Put(first)

	// A second process appending under the same id, which is what a
	// restart inside the same day produces.
	second := storedManifest("same", "one")
	second.Warnings = []string{"a later record"}
	line, _ := json.Marshal(second)
	f, err := os.OpenFile(filepath.Join(dir, "manifests-"+time.Now().Format("2006-01-02")+".jsonl"),
		os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Write(append(line, '\n'))
	f.Close()

	inMemory, ok := s.Get("same")
	if !ok {
		t.Fatal("the manifest is not in memory")
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	fromDisk, ok := reopened.Get("same")
	if !ok {
		t.Fatal("the manifest did not survive the restart")
	}
	if inMemory.Warnings[0] != fromDisk.Warnings[0] {
		t.Errorf("memory answered %q and the file answered %q for the same id",
			inMemory.Warnings[0], fromDisk.Warnings[0])
	}
}

// The id has to be immutable under every fact a diagnostic is read for,
// not only under the wire text. Two calls whose bytes match but whose
// declared trust differs are two different records, and an id that
// conflated them would answer "/context <id>" with the other call's
// classification.
func TestChangedTrustMetadataChangesTheManifestID(t *testing.T) {
	base := Manifest{Agent: "a", Model: "m", Selected: []Entry{
		{ID: "memory.index", Placement: PlaceSystem, Hash: "h",
			Kind: KindExternalContent, Provenance: FromGeneratedSummary, Trust: TrustGenerated},
	}}
	base.ID = manifestID(base)

	promoted := base
	promoted.Selected = []Entry{{ID: "memory.index", Placement: PlaceSystem, Hash: "h",
		Kind: KindExternalContent, Provenance: FromGeneratedSummary, Trust: TrustProject}}
	if manifestID(promoted) == base.ID {
		t.Error("the same text declared with a different trust class shares a manifest id")
	}

	warned := base
	warned.Warnings = []string{"memory.index is durable_reload but comes after a less stable asset"}
	if manifestID(warned) == base.ID {
		t.Error("a warning left the manifest id unchanged")
	}
}
