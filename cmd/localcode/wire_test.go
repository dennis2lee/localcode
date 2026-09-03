package main

import (
	"context"
	"testing"

	"localcode/internal/config"
)

// A provider declaration can survive a global plus project config merge even
// when the active profiles are entirely local. Startup must not read that
// unused provider's credentials. Before Bedrock initialization became lazy,
// the deliberately missing profile below made buildProviders fail.
func TestBuildProvidersDoesNotLoadAWSConfigAtStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {
				Type:    config.ProviderOpenAICompat,
				BaseURL: "http://127.0.0.1:1234/v1",
			},
			"old-bedrock": {
				Type:    config.ProviderBedrock,
				Region:  "us-west-2",
				Profile: "profile-that-does-not-exist",
			},
		},
		Profiles: map[string]config.Profile{
			"local": {Provider: "local", Model: "local-model"},
		},
		DefaultProfile: "local",
	}

	providers, err := buildProviders(context.Background(), cfg, env{home: t.TempDir()})
	if err != nil {
		t.Fatalf("buildProviders loaded unused AWS configuration: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %d, want both configured clients available lazily", len(providers))
	}
}
