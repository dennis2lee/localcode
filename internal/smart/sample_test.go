package smart

import (
	"os"
	"path/filepath"
	"testing"

	"localcode/internal/config"
)

// config.sample.json says, in as many words, that its "agents" section
// can be deleted because localcode supplies the same six with the same
// prompts. That is a claim about this package, and a sample that drifts
// from the code it documents is worse than no sample: it is a wrong
// answer somebody copied.
func TestTheSampleMatchesTheBuiltInRoster(t *testing.T) {
	path := filepath.Join("..", "..", "config.sample.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config.sample.json is missing: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.sample.json does not load: %v", err)
	}

	// The roster localcode would supply for these profiles, which is
	// what somebody who deleted the section would get. The agents are
	// cleared on the loaded config rather than on a copy of it, because
	// Config carries a lock and copying one is what vet objects to.
	declared := cfg.Agents
	cfg.Agents = nil
	supplied := Agents(cfg)
	cfg.Agents = declared
	if len(supplied) == 0 {
		t.Fatal("the sample's profiles produce no roster at all")
	}

	for name, want := range supplied {
		got, declared := cfg.Agents[name]
		if !declared {
			t.Errorf("localcode supplies %q and the sample does not list it", name)
			continue
		}
		if got.Prompt != want.Prompt {
			t.Errorf("%s: the sample's prompt is not the one localcode uses\n sample: %s\n  built: %s",
				name, got.Prompt, want.Prompt)
		}
		if got.Description != want.Description {
			t.Errorf("%s: description differs from the built-in one", name)
		}
		if len(got.Tools) != len(want.Tools) {
			t.Errorf("%s: tools = %v, built-in = %v", name, got.Tools, want.Tools)
			continue
		}
		for i := range want.Tools {
			if got.Tools[i] != want.Tools[i] {
				t.Errorf("%s: tools = %v, built-in = %v", name, got.Tools, want.Tools)
				break
			}
		}
	}
	for name := range cfg.Agents {
		if _, ok := supplied[name]; !ok {
			t.Errorf("the sample declares %q, which localcode does not supply: the section can no longer just be deleted", name)
		}
	}
}

// And the profiles the sample names have to be the ones the roster is
// routed to, or the file explains a mapping it does not produce.
func TestTheSampleProfilesRouteAsItSays(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "config.sample.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, tc := range []struct{ category, want string }{
		{CategoryQuick, "smart-quick"},
		{CategoryBalanced, "smart-balanced"},
		{CategoryDeep, "smart-deep"},
	} {
		if got := ProfileFor(cfg, tc.category); got != tc.want {
			t.Errorf("%s work routes to %q, and the sample says %q", tc.category, got, tc.want)
		}
	}
	// Every agent the sample declares points at a profile it declares.
	for name, a := range cfg.Agents {
		if _, ok := cfg.Profiles[a.Profile]; !ok {
			t.Errorf("%s points at profile %q, which the sample does not define", name, a.Profile)
		}
	}
}
