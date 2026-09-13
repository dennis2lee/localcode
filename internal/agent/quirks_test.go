package agent

import (
	"strings"
	"testing"

	"localcode/internal/config"
)

// The model quirk table (modelQuirks) is where per-family behavior overrides
// live. When a model family exhibits quirks that degrade localcode's
// interaction — such as Gemma emitting raw LaTeX markup or Muse stopping
// midway through multi-step coding tasks — the table provides prompt notes
// and carry-on continuation budgets.
//
// Because the table is evaluated sequentially by lowercase substring matching,
// every field declared on an entry must reach the code that consumes it.
// The failure this file exists to prevent is twofold:
//
// 1. Silent field abandonment: an entry declares a field (like keepGoing), but
//    the downstream consumer checks something else (such as a hardcoded substring
//    "muse") ahead of the table, silently discarding the table's value.
// 2. Substring shadowing: an earlier entry's match pattern is a substring of a
//    later entry's pattern, rendering the later entry completely unreachable.

// assertQuirkConsumers verifies that every field declared in a quirk entry
// actually reaches its respective consumer functions in the agent package.
func assertQuirkConsumers(t *testing.T, loop *Loop, match, note string, keepGoing int) {
	t.Helper()

	if match == "" {
		t.Fatal("quirk entry declared an empty match pattern")
	}
	if match != strings.ToLower(match) {
		t.Errorf("quirk entry %q has uppercase letters; model IDs are lowercased before matching so this will never match", match)
	}

	// 1. The match field must reach modelFamily for both lowercase and
	// uppercase variants of a model ID.
	sampleModel := "vendor-" + match + "-code-7b"
	if got := modelFamily(sampleModel); got != match {
		t.Errorf("modelFamily(%q) = %q, want %q — match pattern did not reach modelFamily", sampleModel, got, match)
	}
	upperModel := "VENDOR-" + strings.ToUpper(match) + "-CODE-7B"
	if got := modelFamily(upperModel); got != match {
		t.Errorf("modelFamily(%q) = %q, want %q — modelFamily is not case-insensitive", upperModel, got, match)
	}

	// 2. The note field must reach quirkNote and modelNoteFor.
	if note != "" {
		if got := quirkNote(sampleModel); got != note {
			t.Errorf("quirkNote(%q) did not return the declared note: got %q, want %q", sampleModel, got, note)
		}
		if got := modelNoteFor(sampleModel, ""); !strings.Contains(got, note) {
			t.Errorf("modelNoteFor(%q) does not contain the declared note: got %q", sampleModel, got)
		}
	}

	// 3. The keepGoing field must reach modelKeepGoing, keepGoingApplies,
	// and loop.effectiveKeepGoing.
	if keepGoing > 0 {
		if got := modelKeepGoing(sampleModel); got != keepGoing {
			t.Errorf("modelKeepGoing(%q) = %d, want %d", sampleModel, got, keepGoing)
		}
		if !keepGoingApplies(sampleModel) {
			t.Errorf("keepGoingApplies(%q) = false, want true — keepGoing budget declared in table did not activate feature", sampleModel)
		}
		// An unconfigured profile gets the family default.
		if got := loop.effectiveKeepGoing(config.Profile{Model: sampleModel}); got != keepGoing {
			t.Errorf("effectiveKeepGoing(%q, unconfigured) = %d, want %d", sampleModel, got, keepGoing)
		}
		// A profile override takes precedence over the family default.
		override := keepGoing + 2
		if got := loop.effectiveKeepGoing(config.Profile{Model: sampleModel, KeepGoing: override}); got != override {
			t.Errorf("effectiveKeepGoing(%q, keep_going=%d) = %d, want %d", sampleModel, override, got, override)
		}
		// A profile opt-out (-1) turns carrying-on off.
		if got := loop.effectiveKeepGoing(config.Profile{Model: sampleModel, KeepGoing: -1}); got != -1 {
			t.Errorf("effectiveKeepGoing(%q, keep_going=-1) = %d, want -1", sampleModel, got)
		}
		// Disabling the daemon-wide switch gates the budget to zero.
		loop.SetKeepGoingEnabled(false)
		if got := loop.effectiveKeepGoing(config.Profile{Model: sampleModel}); got != 0 {
			t.Errorf("effectiveKeepGoing(%q) with switch off = %d, want 0", sampleModel, got)
		}
		loop.SetKeepGoingEnabled(true)
	} else {
		// Families without a keepGoing budget must not be nudged, even if
		// a user attempts to configure keep_going on their profile.
		if got := modelKeepGoing(sampleModel); got != 0 {
			t.Errorf("modelKeepGoing(%q) = %d, want 0", sampleModel, got)
		}
		if keepGoingApplies(sampleModel) {
			t.Errorf("keepGoingApplies(%q) = true, want false — model without keepGoing must not have carry-on enabled", sampleModel)
		}
		if got := loop.effectiveKeepGoing(config.Profile{Model: sampleModel, KeepGoing: 5}); got != 0 {
			t.Errorf("effectiveKeepGoing(%q, keep_going=5) = %d, want 0 — non-stalling model was opted into nudges", sampleModel, got)
		}
	}
}

// TestEveryModelQuirkEntryReachesItsConsumers walks modelQuirks and verifies
// that every entry declared in the table actually reaches the consumers that
// depend on it.
//
// If a new field is added to modelQuirks or a new entry is placed in the table,
// this test enforces that the entry does not suffer from silent field abandonment
// or shadowing.
func TestEveryModelQuirkEntryReachesItsConsumers(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")

	for i, q := range modelQuirks {
		// Check for shadowing by any earlier entry.
		for j := 0; j < i; j++ {
			prev := modelQuirks[j]
			if strings.Contains(q.match, prev.match) {
				t.Fatalf("entry %d (%q) is shadowed by earlier entry %d (%q)", i, q.match, j, prev.match)
			}
			if q.match == prev.match {
				t.Fatalf("entry %d and %d have duplicate match pattern %q", i, j, q.match)
			}
		}

		assertQuirkConsumers(t, loop, q.match, q.note, q.keepGoing)
	}
}

// TestANewModelFamilyInQuirksHonoursItsKeepGoingBudget proves that the table
// is the true source of truth for the carry-on feature, rather than a hardcoded
// substring check.
//
// In the original defect, keepGoingApplies hardcoded strings.Contains(model, "muse").
// As a result, adding a third family to modelQuirks with keepGoing: 4 would have its
// budget silently discarded: effectiveKeepGoing returned 0 because keepGoingApplies
// was consulted ahead of the table and rejected any model without "muse" in its name.
//
// This test temporarily appends a non-muse family ("stubborn") to modelQuirks and
// verifies that its keepGoing budget reaches effectiveKeepGoing and keepGoingApplies.
func TestANewModelFamilyInQuirksHonoursItsKeepGoingBudget(t *testing.T) {
	orig := modelQuirks
	defer func() { modelQuirks = orig }()

	const newFamily = "stubborn"
	const newBudget = 4
	const newNote = "Working style: finish the task before you end your turn."

	modelQuirks = append(modelQuirks, struct {
		match     string
		note      string
		keepGoing int
	}{
		match:     newFamily,
		note:      newNote,
		keepGoing: newBudget,
	})

	loop := newSmartLoop(t, "http://127.0.0.1:1")
	assertQuirkConsumers(t, loop, newFamily, newNote, newBudget)

	// Specifically verify that effectiveKeepGoing honours the table value.
	profile := config.Profile{Model: "stubborn-coder-30b"}
	if got := loop.effectiveKeepGoing(profile); got != newBudget {
		t.Fatalf("effectiveKeepGoing(%q) = %d, want %d — table default budget was discarded", profile.Model, got, newBudget)
	}

	// And verify that profile override works on the new family.
	profileWithOverride := config.Profile{Model: "stubborn-coder-30b", KeepGoing: 6}
	if got := loop.effectiveKeepGoing(profileWithOverride); got != 6 {
		t.Fatalf("effectiveKeepGoing(%q, keep_going=6) = %d, want 6", profile.Model, got)
	}

	// And profile opt-out (-1) works.
	profileOptOut := config.Profile{Model: "stubborn-coder-30b", KeepGoing: -1}
	if got := loop.effectiveKeepGoing(profileOptOut); got != -1 {
		t.Fatalf("effectiveKeepGoing(%q, keep_going=-1) = %d, want -1", profile.Model, got)
	}
}

// TestShadowingBetweenQuirkEntriesIsChecked verifies the shadowing invariant.
//
// In a table scanned sequentially with strings.Contains(id, q.match), if an earlier
// entry's match is a substring of a later entry's match, the later entry will never
// be reached for any model ID.
//
// Today, the table has only "gemma" and "muse", neither of which is a substring of
// the other, so shadowing is trivially impossible between the current entries. We
// keep this test because the original bug that motivated this table was "glimmer" vs
// "muse" (where muse-glimmer-30b would match either). If an entry for "muse-glimmer"
// were ever added after "muse", it would be silently dead.
func TestShadowingBetweenQuirkEntriesIsChecked(t *testing.T) {
	for i := range modelQuirks {
		for j := 0; j < i; j++ {
			if strings.Contains(modelQuirks[i].match, modelQuirks[j].match) {
				t.Errorf("entry %q at index %d is shadowed by %q at index %d",
					modelQuirks[i].match, i, modelQuirks[j].match, j)
			}
		}
	}

	// Guard the shadowing check itself: confirm that a shadowed entry is detected.
	shadowed := []struct {
		match     string
		note      string
		keepGoing int
	}{
		{match: "muse"},
		{match: "muse-glimmer"},
	}
	foundShadow := false
	for i := range shadowed {
		for j := 0; j < i; j++ {
			if strings.Contains(shadowed[i].match, shadowed[j].match) {
				foundShadow = true
			}
		}
	}
	if !foundShadow {
		t.Error("shadowing check failed to detect that 'muse' shadows 'muse-glimmer'")
	}
}

// TestExistingModelFamiliesPreserveTheirBehavior ensures that making the table
// the source of truth for keep-going does not change the behavior of any existing
// family: muse continues to get carry-ons, gemma does not, and unlisted models
// remain unaffected.
func TestExistingModelFamiliesPreserveTheirBehavior(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")

	// Muse models must get budget=3 by default, and honor overrides.
	for _, museModelName := range []string{"muse-glimmer-30b", "MUSE-GLIMMER-30B", "my-muse-variant-7b"} {
		if !keepGoingApplies(museModelName) {
			t.Errorf("keepGoingApplies(%q) = false, want true", museModelName)
		}
		if got := loop.effectiveKeepGoing(config.Profile{Model: museModelName}); got != 3 {
			t.Errorf("effectiveKeepGoing(%q) = %d, want 3", museModelName, got)
		}
		if got := loop.effectiveKeepGoing(config.Profile{Model: museModelName, KeepGoing: 5}); got != 5 {
			t.Errorf("effectiveKeepGoing(%q, keep_going=5) = %d, want 5", museModelName, got)
		}
		if got := loop.effectiveKeepGoing(config.Profile{Model: museModelName, KeepGoing: -1}); got != -1 {
			t.Errorf("effectiveKeepGoing(%q, keep_going=-1) = %d, want -1", museModelName, got)
		}
	}

	// Gemma models have formatting quirks but no carry-on budget.
	for _, gemmaModelName := range []string{"gemma-3-27b-it", "GEMMA-2-9B"} {
		if keepGoingApplies(gemmaModelName) {
			t.Errorf("keepGoingApplies(%q) = true, want false", gemmaModelName)
		}
		if got := loop.effectiveKeepGoing(config.Profile{Model: gemmaModelName}); got != 0 {
			t.Errorf("effectiveKeepGoing(%q) = %d, want 0", gemmaModelName, got)
		}
		// A profile setting keep_going on Gemma is ignored.
		if got := loop.effectiveKeepGoing(config.Profile{Model: gemmaModelName, KeepGoing: 5}); got != 0 {
			t.Errorf("effectiveKeepGoing(%q, keep_going=5) = %d, want 0", gemmaModelName, got)
		}
	}

	// Unlisted models (Claude, Qwen, GPT) are completely outside the table.
	for _, unlistedModelName := range []string{"claude-sonnet-4-6", "qwen3-30b-a3b", "gpt-4o"} {
		if keepGoingApplies(unlistedModelName) {
			t.Errorf("keepGoingApplies(%q) = true, want false", unlistedModelName)
		}
		if got := loop.effectiveKeepGoing(config.Profile{Model: unlistedModelName}); got != 0 {
			t.Errorf("effectiveKeepGoing(%q) = %d, want 0", unlistedModelName, got)
		}
		if got := loop.effectiveKeepGoing(config.Profile{Model: unlistedModelName, KeepGoing: 3}); got != 0 {
			t.Errorf("effectiveKeepGoing(%q, keep_going=3) = %d, want 0", unlistedModelName, got)
		}
	}
}
