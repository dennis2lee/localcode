package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Every profile the project ships, crossed with every effort level,
// has to produce a request the API will accept.
//
// v0.70.0 shipped two hard 400s, and neither needed an unusual
// configuration to reach: the example config below pairs max_tokens 8192
// with its Claude profiles, "high" is a 16384-token budget, and a budget
// larger than the output cap is refused outright; separately, a profile
// with a temperature on it that also asked to reason sent both, which the
// API refuses together. Both were fixed in v0.71.0, and effort_test.go
// pins each fix at the point it was made -- one budget, one model, one
// level at a time.
//
// What was missing is this: nobody had walked the shipped profiles
// against the levels a user can actually select. The two defects were
// found by scouts reading the adapter, not by a test, and the next one
// of this shape would be found the same way. The precedent for closing
// it is v0.61.0's sample-versus-roster test, which caught two drifts the
// first time it ran.
//
// config.example.json only. config.sample.json is JSONC, and reproducing
// the comment stripper here to read it would be a second implementation
// of internal/config's parser inside a test -- and internal/config
// imports this package, so it cannot be borrowed. The example file is the
// one the README calls the reference and the one both 400s came from.
func TestEveryShippedProfileSurvivesEveryEffortLevel(t *testing.T) {
	profiles := shippedProfiles(t)
	if len(profiles) < 2 {
		t.Fatalf("parsed %d profiles from config.example.json, so this test is checking almost nothing", len(profiles))
	}

	// Each profile is run with its own temperature and again with one set.
	// No shipped profile sets a temperature today, which would make the
	// second of the two 400s vacuous here -- dropping a temperature that
	// was never there proves nothing. The person the defect actually
	// reached is the one who put a temperature on their profile and then
	// turned effort up, and the config has always allowed that.
	temperatures := []float64{0, 0.7}

	for _, p := range profiles {
		for _, level := range Levels() {
			for _, temp := range temperatures {
				runShippedProfileCase(t, p, level, temp)
			}
		}
	}
}

func runShippedProfileCase(t *testing.T, p shippedProfile, level Effort, temp float64) {
	name := p.name + "/" + string(level)
	if temp != 0 {
		name += "/with-temperature"
	}
	t.Run(name, func(t *testing.T) {
		req := ChatRequest{
			Model:       p.Model,
			MaxTokens:   p.MaxTokens,
			Temperature: temp,
			Effort:      level,
		}

		switch p.providerType {
		case "anthropic", "bedrock":
			// Both adapters build the thinking field from this
			// one function, which is why testing it covers the
			// pair. Bedrock carries the result through
			// additionalModelRequestFields.
			th := anthropicThinking(p.Model, level, p.MaxTokens)
			if th == nil {
				// Nothing asked for, so nothing can be refused.
				// The temperature rule still has to hold in the
				// other direction: a request with no thinking
				// keeps the temperature it was configured with.
				if got := temperatureFor(req); got != temp {
					t.Errorf("no thinking asked for, yet the temperature was changed from %v to %v", temp, got)
				}
				return
			}

			// The 400 of the second kind: temperature alongside
			// reasoning.
			if got := temperatureFor(req); got != 0 {
				t.Errorf("thinking is %+v and the request still carries temperature %v; the API refuses both together", th, got)
			}

			if th.Type == "adaptive" {
				// The newest families size their own reasoning
				// and reject a budget, so there is no arithmetic
				// left to get wrong.
				if th.BudgetTokens != 0 {
					t.Errorf("adaptive thinking carries a budget of %d, which this family rejects", th.BudgetTokens)
				}
				return
			}

			// The 400 of the first kind: a budget the output cap
			// cannot pay for. The budget is spent out of
			// max_tokens, so it must leave room for an answer as
			// well as fit.
			if th.BudgetTokens < minThinkingBudget {
				t.Errorf("thinking budget %d is below the %d minimum, which is a reasoning request with no room to reason", th.BudgetTokens, minThinkingBudget)
			}
			if p.MaxTokens > 0 && th.BudgetTokens+answerReserve > p.MaxTokens {
				t.Errorf("thinking budget %d plus the %d reserved for the answer exceeds max_tokens %d: the API refuses a budget it cannot pay for", th.BudgetTokens, answerReserve, p.MaxTokens)
			}

		case "openai-compat":
			// One string on the wire, no arithmetic. What matters
			// is that a level a user can select is a level this
			// adapter has a spelling for, and that "unset" stays
			// absent rather than becoming a word.
			if level == EffortUnset {
				return
			}
			if !ValidEffort(string(level)) {
				t.Errorf("%q is offered as a level and is not a valid one", level)
			}

		default:
			t.Fatalf("profile %q names provider type %q, which this test has no rule for -- add one rather than leaving the profile unchecked", p.name, p.providerType)
		}
	})
}

type shippedProfile struct {
	name         string
	providerType string
	Model        string  `json:"model"`
	MaxTokens    int     `json:"max_tokens"`
	Temperature  float64 `json:"temperature"`
	Provider     string  `json:"provider"`
}

// shippedProfiles reads config.example.json, resolving each profile's
// provider key to its declared type.
//
// It reads the file rather than restating its contents: a copy of these
// profiles living in a test would agree with itself forever while the
// shipped file drifted, which is the failure this test exists to catch.
func shippedProfiles(t *testing.T) []shippedProfile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatalf("read config.example.json: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			Type string `json:"type"`
		} `json:"providers"`
		Profiles map[string]shippedProfile `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse config.example.json: %v", err)
	}

	var out []shippedProfile
	for name, p := range doc.Profiles {
		prov, ok := doc.Providers[p.Provider]
		if !ok {
			t.Errorf("profile %q names provider %q, which config.example.json does not declare", name, p.Provider)
			continue
		}
		p.name, p.providerType = name, prov.Type
		out = append(out, p)
	}
	return out
}
