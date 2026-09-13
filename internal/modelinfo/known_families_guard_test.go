package modelinfo

import (
	"strings"
	"testing"
)

// TestNoEarlierFamilyKeyShadowsALaterKey guards against ordering hazards in
// knownFamilies.
//
// MaxContextTokens walks knownFamilies in declaration order and returns on the
// first substring match. If an earlier entry's key is a substring of a later
// entry's key, any model ID containing the later key will always match the
// earlier one first. The later entry becomes unreachable dead code.
//
// This failure is completely silent at compile time. It only surfaces when a
// model receives the wrong context window, causing early auto-compaction or an
// artificially throttled reply limit. This test asserts that for every entry in
// knownFamilies, no preceding entry is a substring of it.
func TestNoEarlierFamilyKeyShadowsALaterKey(t *testing.T) {
	for j, later := range knownFamilies {
		laterSub := strings.ToLower(later.substr)
		for i := 0; i < j; i++ {
			earlier := knownFamilies[i]
			earlierSub := strings.ToLower(earlier.substr)
			if strings.Contains(laterSub, earlierSub) {
				t.Errorf("knownFamilies[%d] (%q, %d tokens) is unreachable: earlier entry [%d] (%q, %d tokens) is contained within it and shadows every match",
					j, later.substr, later.tokens, i, earlier.substr, earlier.tokens)
			}
		}
	}
}

// familyRequirement pairs a family key with a verified real-world model ID and
// its required context window size.
//
// The requirement is defined independently from knownFamilies. Keying a test
// on the table's own numbers would allow a table full of wrong numbers to pass
// as long as it agreed with itself.
type familyRequirement struct {
	modelID    string
	wantTokens int
}

var familyRequirements = map[string]familyRequirement{
	"claude-opus-5":     {modelID: "claude-opus-5-20251101", wantTokens: 1000000},
	"claude-opus-4-8":   {modelID: "global.anthropic.claude-opus-4-8", wantTokens: 1000000},
	"claude-opus-4-7":   {modelID: "claude-opus-4-7-20251101", wantTokens: 1000000},
	"claude-opus-4-6":   {modelID: "us.anthropic.claude-opus-4-6-v1", wantTokens: 1000000},
	"claude-sonnet-5":   {modelID: "us.anthropic.claude-sonnet-5", wantTokens: 1000000},
	"claude-sonnet-4-6": {modelID: "claude-sonnet-4-6-20251001", wantTokens: 1000000},
	"claude-fable":      {modelID: "claude-fable-5-20251101", wantTokens: 1000000},
	"claude-mythos":     {modelID: "claude-mythos-5-20251101", wantTokens: 1000000},
	"claude-3":          {modelID: "claude-3-5-sonnet-20241022", wantTokens: 200000},
	"claude-opus":       {modelID: "claude-opus-4-5-20251001", wantTokens: 200000},
	"claude-sonnet":     {modelID: "anthropic.claude-sonnet-4-5-20250929-v1:0", wantTokens: 200000},
	"claude-haiku":      {modelID: "global.anthropic.claude-haiku-4-5-20251001-v1:0", wantTokens: 200000},
	"gpt-4o":            {modelID: "gpt-4o-2024-08-06", wantTokens: 128000},
	"gpt-4-turbo":       {modelID: "gpt-4-turbo-2024-04-09", wantTokens: 128000},
	"gpt-4":             {modelID: "gpt-4-0613", wantTokens: 8192},
	"gpt-3.5":           {modelID: "gpt-3.5-turbo-0125", wantTokens: 16385},
	"qwen3":             {modelID: "qwen3-72b-instruct", wantTokens: 32768},
	"qwen2.5":           {modelID: "qwen2.5-coder-32b-instruct", wantTokens: 32768},
	"llama-3.1":         {modelID: "llama-3.1-70b-instruct", wantTokens: 128000},
	"llama-3.2":         {modelID: "llama-3.2-3b-instruct", wantTokens: 128000},
	"llama-3":           {modelID: "llama-3-8b-instruct", wantTokens: 8192},
	"mixtral":           {modelID: "mixtral-8x7b-instruct-v0.1", wantTokens: 32768},
}

// TestEveryKnownFamilyEntryResolvesRepresentativeModel walks knownFamilies and
// validates that every entry resolves a realistic model identifier to its
// claimed window size.
//
// A containment check proves only that entries are reachable. It cannot detect
// a table populated with incorrect token counts. This test enforces that every
// table entry is backed by a verified requirement, and fails if any entry is
// added to knownFamilies without being guarded here.
func TestEveryKnownFamilyEntryResolvesRepresentativeModel(t *testing.T) {
	seen := make(map[string]bool)

	for i, entry := range knownFamilies {
		req, ok := familyRequirements[entry.substr]
		if !ok {
			t.Errorf("knownFamilies[%d] (%q) has no corresponding entry in familyRequirements; every family must have a guarded representative real model ID",
				i, entry.substr)
			continue
		}
		seen[entry.substr] = true

		if entry.tokens != req.wantTokens {
			t.Errorf("knownFamilies[%d] (%q) claims %d tokens, but requirement is %d tokens",
				i, entry.substr, entry.tokens, req.wantTokens)
		}

		if !strings.Contains(strings.ToLower(req.modelID), strings.ToLower(entry.substr)) {
			t.Errorf("representative model ID %q does not contain family substring %q",
				req.modelID, entry.substr)
		}

		got := MaxContextTokens(req.modelID)
		if got != req.wantTokens {
			t.Errorf("MaxContextTokens(%q) = %d, want %d (matched family %q)",
				req.modelID, got, req.wantTokens, entry.substr)
		}
	}

	for substr := range familyRequirements {
		if !seen[substr] {
			t.Errorf("familyRequirements contains %q, but it is missing from knownFamilies", substr)
		}
	}
}
