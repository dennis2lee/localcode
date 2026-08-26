package config

import "strings"

import "testing"

func chainConfig(fallback ...string) *Config {
	return &Config{
		Providers: map[string]ProviderConfig{"p": {Type: ProviderOpenAICompat, BaseURL: "http://x/v1"}},
		Profiles: map[string]Profile{
			"primary": {Provider: "p", Model: "a", Fallback: fallback},
			"backup":  {Provider: "p", Model: "b"},
		},
		DefaultProfile: "primary",
	}
}

func TestAValidChainLoads(t *testing.T) {
	if err := chainConfig("backup").Validate(); err != nil {
		t.Errorf("a chain naming a real profile was rejected: %v", err)
	}
}

// Checked at load rather than at the moment of a failure. A fallback chain
// is read exactly when something has already gone wrong, which is the
// worst possible time to discover a typo in it.
func TestAChainNamingNothingIsRejected(t *testing.T) {
	err := chainConfig("typo").Validate()
	if err == nil {
		t.Fatal("a fallback naming no profile was accepted")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("the error does not name the offending entry: %v", err)
	}
}

func TestAProfileCannotFallBackToItself(t *testing.T) {
	err := chainConfig("primary").Validate()
	if err == nil {
		t.Fatal("a profile listing itself was accepted")
	}
	if !strings.Contains(err.Error(), "itself") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
}

func TestNoChainIsStillValid(t *testing.T) {
	if err := chainConfig().Validate(); err != nil {
		t.Errorf("a profile with no fallback was rejected: %v", err)
	}
}
