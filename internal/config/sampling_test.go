package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func topP(v float64) *float64 { return &v }
func topK(v int) *int         { return &v }

func withProfile(p Profile) *Config {
	return &Config{
		Providers: map[string]ProviderConfig{"local": {Type: ProviderOpenAICompat, BaseURL: "http://x"}},
		Profiles:  map[string]Profile{"only": p},
	}
}

// A sampling number outside its range is refused by the server in the
// middle of a turn, which is a worse place to read about a typo than the
// moment of load.
func TestSamplingIsBoundedAtLoad(t *testing.T) {
	for _, c := range []struct {
		name    string
		profile Profile
		want    string
	}{
		{"top_p above one", Profile{Provider: "local", Model: "m", TopP: topP(1.5)}, "top_p"},
		{"top_p at zero", Profile{Provider: "local", Model: "m", TopP: topP(0)}, "top_p"},
		{"negative top_k", Profile{Provider: "local", Model: "m", TopK: topK(-1)}, "top_k"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := withProfile(c.profile).Validate()
			if err == nil {
				t.Fatalf("%v was accepted", c.profile)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error does not name %s: %v", c.want, err)
			}
		})
	}
}

// muse's own recipe, which is the case this exists for.
func TestTheMuseRecipeLoads(t *testing.T) {
	cfg := withProfile(Profile{Provider: "local", Model: "muse-glimmer-30b", Temperature: 1.0, TopP: topP(0.95), TopK: topK(64)})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("muse's own sampling recipe was refused: %v", err)
	}
	// top_k 0 is "no limit" on vLLM, so it is a value rather than an
	// absence and has to pass.
	if err := withProfile(Profile{Provider: "local", Model: "m", TopK: topK(0)}).Validate(); err != nil {
		t.Errorf("top_k 0 was refused: %v", err)
	}
}

// A profile written before these existed carries neither, and reads back
// carrying neither.
func TestAProfileWithoutThemStaysWithoutThem(t *testing.T) {
	var p Profile
	if err := json.Unmarshal([]byte(`{"provider":"local","model":"m"}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.TopP != nil || p.TopK != nil {
		t.Errorf("an old profile came back with top_p=%v top_k=%v, want neither", p.TopP, p.TopK)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "top_p") || strings.Contains(string(raw), "top_k") {
		t.Errorf("writing it back added a field it never had: %s", raw)
	}
}

// The egress allow list is bounded at load, because a rule with a scheme
// or a port in it looks like it works and silently matches nothing —
// the worst shape a security control can take.
func TestEgressRulesAreBoundedAtLoad(t *testing.T) {
	with := func(e *EgressConfig) *Config {
		c := withProfile(Profile{Provider: "local", Model: "m"})
		c.Network = &NetworkConfig{Egress: e}
		return c
	}
	for _, c := range []struct {
		name string
		cfg  *EgressConfig
		want string
	}{
		{"a URL", &EgressConfig{Allow: []string{"https://api.example.com"}}, "host names"},
		{"a port", &EgressConfig{Allow: []string{"api.example.com:443"}}, "host names"},
		{"a wildcard in the middle", &EgressConfig{Allow: []string{"api.*.com"}}, "leading"},
		{"an empty entry", &EgressConfig{Allow: []string{""}}, "empty entry"},
		// Enforcing an empty list would refuse every provider, which is
		// not what anybody means by turning it on.
		{"enforced with nothing allowed", &EgressConfig{Enforced: true}, "empty allow list"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := with(c.cfg).Validate()
			if err == nil {
				t.Fatalf("%+v was accepted", c.cfg)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the error does not say %q: %v", c.want, err)
			}
		})
	}

	// And a real one loads, and reaches the enforcing package unchanged.
	good := with(&EgressConfig{Enforced: true, Allow: []string{"api.anthropic.com", "*.internal.example"}})
	if err := good.Validate(); err != nil {
		t.Fatalf("a reasonable allow list was refused: %v", err)
	}
	p := good.EgressPolicy()
	if !p.Enforced || len(p.Allow) != 2 {
		t.Errorf("EgressPolicy = %+v, want the configured list, enforced", p)
	}
	// Nothing configured permits everything, which is what every build
	// before this did.
	if (&Config{}).EgressPolicy().Enforced {
		t.Error("a config with no network block came back enforcing something")
	}
}
