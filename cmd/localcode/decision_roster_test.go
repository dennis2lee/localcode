package main

import (
	"testing"

	"localcode/internal/config"
	"localcode/internal/tools"
)

// The two permission-decision rosters are joined by a bare string
// conversion, and nothing in the type system notices when they stop
// agreeing.
//
// buildRegistry hands the registry a resolver that converts the config's
// answer with tools.Decision(cfg.ResolvePermissionFor(...)). A decision
// added to one package and not the other therefore survives compilation
// and arrives at Registry.Call as a value it has never heard of. That is
// fail-closed — the call is refused — but a refusal is the wrong answer
// for a decision somebody deliberately added, and the only place the
// mismatch could have been caught is here, in the one package that
// imports both.
func TestTheTwoDecisionRostersStillAgree(t *testing.T) {
	if len(config.AllDecisions) != len(tools.AllDecisions) {
		t.Fatalf("roster sizes differ: config has %d (%v), tools has %d (%v); teach the new decision to both",
			len(config.AllDecisions), config.AllDecisions, len(tools.AllDecisions), tools.AllDecisions)
	}
	for i, want := range config.AllDecisions {
		if got := tools.AllDecisions[i]; string(got) != string(want) {
			t.Errorf("roster entry %d: config has %q, tools has %q — the conversion at the wiring would produce a decision the registry refuses",
				i, want, got)
		}
	}
	// Every config decision must survive the conversion as one the
	// registry acts on, which is the property the conversion relies on
	// and the reason the rosters have to match at all.
	for _, d := range config.AllDecisions {
		if converted := tools.Decision(d); !containsDecision(tools.AllDecisions, converted) {
			t.Errorf("config decision %q converts to %q, which the registry does not know", d, converted)
		}
	}
}

func containsDecision(roster []tools.Decision, want tools.Decision) bool {
	for _, d := range roster {
		if d == want {
			return true
		}
	}
	return false
}
