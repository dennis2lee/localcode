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
