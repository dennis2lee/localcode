package agent

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
)

// TestClassificationPhraseListInvariantsArePreserved audits the four error-classification
// lists (deterministicPhrases, fallbackPhrases, retryPhrases, and overflowPhrases)
// against the structural rules required for reliable error handling.
//
// A broken invariant fails silently in production: an uppercase character causes
// the phrase to never match because the matcher lowercases the subject first;
// a collision across unrelated lists causes request defects to be retried; and
// accidental substrings within a list introduce redundant or shadowing matches.
func TestClassificationPhraseListInvariantsArePreserved(t *testing.T) {
	lists := map[string][]string{
		"deterministicPhrases": deterministicPhrases,
		"fallbackPhrases":      fallbackPhrases,
		"retryPhrases":         retryPhrases,
		"overflowPhrases":      overflowPhrases,
	}

	// Invariant 1: Every entry in all four lists must be strictly lowercase.
	// The classifier runs strings.ToLower on err.Error(); an uppercase phrase
	// could never match on the wire and would silently cost a recovery.
	for name, list := range lists {
		for _, entry := range list {
			for _, r := range entry {
				if unicode.IsUpper(r) {
					t.Errorf("%s contains non-lowercase entry %q; will never match lowercased error text", name, entry)
				}
			}
		}
	}

	// Invariant 2: No duplicates within any list.
	for name, list := range lists {
		seen := make(map[string]bool)
		for _, entry := range list {
			if seen[entry] {
				t.Errorf("%s contains duplicate entry %q", name, entry)
			}
			seen[entry] = true
		}
	}

	// Invariant 3: Cross-list relationships.
	// deterministicPhrases and overflowPhrases must be strictly disjoint from all
	// other lists. A phrase that is both deterministic and fallback-eligible creates
	// an ambiguity where request bugs get sent across model chains.
	// retryPhrases, by deliberate design (documented in fallback.go:211), is a strict
	// subset of fallbackPhrases: anything transient enough to retry in place must
	// also be eligible for fallback once retries are spent.
	for _, entry := range retryPhrases {
		found := false
		for _, fb := range fallbackPhrases {
			if fb == entry {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("retry phrase %q is missing from fallbackPhrases; transient failures must fall back once retries are spent", entry)
		}
	}

	// Ensure deterministic and overflow phrases do not collide with any other list.
	disjointPairs := [][2]string{
		{"deterministicPhrases", "fallbackPhrases"},
		{"deterministicPhrases", "retryPhrases"},
		{"deterministicPhrases", "overflowPhrases"},
		{"overflowPhrases", "fallbackPhrases"},
		{"overflowPhrases", "retryPhrases"},
	}
	for _, pair := range disjointPairs {
		setB := make(map[string]bool)
		for _, entry := range lists[pair[1]] {
			setB[entry] = true
		}
		for _, entry := range lists[pair[0]] {
			if setB[entry] {
				t.Errorf("entry %q appears in both %s and %s; request defects and overflows must not overlap recovery lists", entry, pair[0], pair[1])
			}
		}
	}

	// Invariant 4: No entry is a substring of another entry in the same list.
	// A substring entry shadows the longer entry or renders it dead code.
	// Exception: "server error" and "internal server error" in fallbackPhrases and
	// retryPhrases. This pair is deliberate: "internal server error" explicitly names
	// the canonical RFC 9110 HTTP 500 reason phrase, while "server error" catches
	// generic provider variations (e.g. "server error occurred").
	for name, list := range lists {
		for i, a := range list {
			for j, b := range list {
				if i == j {
					continue
				}
				if (a == "server error" && b == "internal server error") ||
					(b == "server error" && a == "internal server error") {
					continue
				}
				if strings.Contains(b, a) {
					t.Errorf("%s: entry %q is a substring of %q in the same list", name, a, b)
				}
			}
		}
	}
}

// TestEveryClassificationPhraseMatchesItsIntendedClassAndRejectsPlausibleNonMatches
// walks all entries across all four lists, asserting that each entry actually
// classifies a representative error, and asserting that plausible non-matching
// errors (such as embedded digits or incidental prose) are rejected.
func TestEveryClassificationPhraseMatchesItsIntendedClassAndRejectsPlausibleNonMatches(t *testing.T) {
	// 1. deterministicPhrases: each entry must classify request defects as non-retryable
	// and non-fallback, even when an error also happens to mention a 500 or 429 status.
	for _, p := range deterministicPhrases {
		err := fmt.Errorf("provider error: %s", p)
		if retryableInPlace(err) {
			t.Errorf("deterministic phrase %q was classified as retryable in place", p)
		}
		if worthFallingBackOver(err) {
			t.Errorf("deterministic phrase %q was classified as fallback-eligible", p)
		}

		// A deterministic phrase must trump an endpoint status code in the same error.
		errWithStatus := fmt.Errorf("openai-compat endpoint returned 500: %s", p)
		if worthFallingBackOver(errWithStatus) {
			t.Errorf("deterministic phrase %q failed to override HTTP 500 in fallback check", p)
		}
		if retryableInPlace(errWithStatus) {
			t.Errorf("deterministic phrase %q failed to override HTTP 500 in retry check", p)
		}
	}

	// 2. overflowPhrases: each entry must be recognized as context overflow, and must
	// never be offered a fallback or same-endpoint retry (overflow requires compaction).
	for _, p := range overflowPhrases {
		err := fmt.Errorf("openai-compat endpoint returned 400: %s", p)
		if !isContextOverflow(err) {
			t.Errorf("overflow phrase %q was not recognized by isContextOverflow", p)
		}
		if retryableInPlace(err) {
			t.Errorf("overflow phrase %q was classified as retryable in place", p)
		}
		if worthFallingBackOver(err) {
			t.Errorf("overflow phrase %q was classified as fallback-eligible", p)
		}
	}

	// 3. retryPhrases: each entry must classify a representative error as retryable in place.
	for _, p := range retryPhrases {
		var repErr error
		if isStatusNumber(p) {
			repErr = fmt.Errorf("openai-compat endpoint returned %s: endpoint failure", p)
		} else {
			repErr = fmt.Errorf("endpoint failure: %s", p)
		}
		if !retryableInPlace(repErr) {
			t.Errorf("retry phrase %q failed to classify representative error %v as retryable", p, repErr)
		}
		if !worthFallingBackOver(repErr) {
			t.Errorf("retry phrase %q failed to classify representative error %v as fallback-eligible", p, repErr)
		}
	}

	// 4. fallbackPhrases: each entry must classify a representative error as fallback-eligible.
	for _, p := range fallbackPhrases {
		var repErr error
		if isStatusNumber(p) {
			repErr = fmt.Errorf("openai-compat endpoint returned %s: endpoint failure", p)
		} else {
			repErr = fmt.Errorf("endpoint failure: %s", p)
		}
		if !worthFallingBackOver(repErr) {
			t.Errorf("fallback phrase %q failed to classify representative error %v as fallback-eligible", p, repErr)
		}
	}

	// 5. Plausible non-matching errors:
	// For every status number (e.g. "500", "502", "503", "504", "429", "401", "403", "404"),
	// digits embedded in larger numbers ("15000", "15040") or duration units ("500ms")
	// must NOT trigger retry or fallback.
	statusCodes := []string{"500", "502", "503", "504", "429", "401", "403", "404"}
	for _, code := range statusCodes {
		// Embedded digits: 15000, 15040, 14290, etc.
		errEmbedded := fmt.Errorf("openai-compat endpoint returned 400: input length (1%s0 tokens) exceeds maximum", code)
		if retryableInPlace(errEmbedded) {
			t.Errorf("status code %q matched embedded digits in %v for retry", code, errEmbedded)
		}
		if worthFallingBackOver(errEmbedded) {
			t.Errorf("status code %q matched embedded digits in %v for fallback", code, errEmbedded)
		}

		// Unit suffix: "500ms timeout"
		errSuffix := fmt.Errorf("openai-compat endpoint returned 400: %sms timeout", code)
		if retryableInPlace(errSuffix) {
			t.Errorf("status code %q matched unit-suffixed digits in %v for retry", code, errSuffix)
		}
		if worthFallingBackOver(errSuffix) {
			t.Errorf("status code %q matched unit-suffixed digits in %v for fallback", code, errSuffix)
		}
	}

	// Plausible non-matching prose errors must not trigger overflow, retry, or fallback.
	errUnrelated := fmt.Errorf("openai-compat endpoint returned 400: syntax error on line 42")
	if isContextOverflow(errUnrelated) {
		t.Errorf("unrelated error %v was classified as overflow", errUnrelated)
	}
	if retryableInPlace(errUnrelated) {
		t.Errorf("unrelated error %v was classified as retryable in place", errUnrelated)
	}
	if worthFallingBackOver(errUnrelated) {
		t.Errorf("unrelated error %v was classified as fallback-eligible", errUnrelated)
	}
}

// TestDigitsInProseAreNotClassifiedAsStatusFailures verifies that status numbers
// embedded in longer numbers (such as token counts or request IDs) do not cause
// client request defects to be misclassified as retryable server errors.
//
// These three shapes represent the defects that initiated the status boundary rules:
//   - "15000 tokens": status "500" is preceded by "1"
//   - "142900 characters": status "429" is followed by "0"
//   - "req_50231": status "502" is followed by "3"
func TestDigitsInProseAreNotClassifiedAsStatusFailures(t *testing.T) {
	embeddedCases := []struct {
		desc   string
		errStr string
	}{
		{
			desc:   "token count with preceded digit (15000 embeds 500)",
			errStr: "openai-compat endpoint returned 400: input length (15000 tokens) exceeds maximum",
		},
		{
			desc:   "character count with followed digit (142900 embeds 429)",
			errStr: "openai-compat endpoint returned 400: input length (142900 characters) exceeds maximum",
		},
		{
			desc:   "request ID with followed digit (req_50231 embeds 502)",
			errStr: "openai-compat endpoint returned 400: request failed (id: req_50231)",
		},
	}

	for _, tc := range embeddedCases {
		err := fmt.Errorf("%s", tc.errStr)
		if retryableInPlace(err) {
			t.Errorf("%s: classified as retryable in place: %v", tc.desc, err)
		}
		if worthFallingBackOver(err) {
			t.Errorf("%s: classified as fallback-eligible: %v", tc.desc, err)
		}
	}
}

// TestStatusNumbersMatchAcrossDiverseProviderFormatsWithoutPrefixAllowlist
// verifies that HTTP status codes from diverse local model servers (such as
// llama.cpp, vllm, ollama) and upstream reverse proxies are recognized as
// retryable and fallback-eligible without requiring specific prefix phrases.
//
// An allowlist of prefixes (e.g. requiring "returned" or "http/1.1") fails in
// the costly direction: local servers under load return status numbers in
// diverse shapes ("vllm: 429", "llama.cpp server: 503", "ollama: 502 from proxy").
// If unrecognised, these transient server errors fail the turn instead of
// recovering through same-endpoint retry or profile fallback.
func TestStatusNumbersMatchAcrossDiverseProviderFormatsWithoutPrefixAllowlist(t *testing.T) {
	cases := []string{
		`openai-compat endpoint returned 503: {"detail":"try later"}`,
		`provider responded with 502`,
		`{"status_code": 503, "detail": "upstream"}`,
		`vllm: 429`,
		`request failed, 500`,
		`HTTP/1.1 503`,
		`llama.cpp server: 503 {"error":"slot unavailable"}`,
		`ollama: 502 from proxy`,
		`upstream 504`,
		`got status=503 body=`,
	}

	for _, s := range cases {
		err := fmt.Errorf("%s", s)
		if !retryableInPlace(err) {
			t.Errorf("expected retry=true for provider error %q, got false", s)
		}
		if !worthFallingBackOver(err) {
			t.Errorf("expected fallback=true for provider error %q, got false", s)
		}
	}
}
