package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/credentials"
	"localcode/internal/provider"
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

// An anthropic provider with no key is a provider nobody can use yet, not
// a reason to stop. It stopped startup for everyone whose config named
// one, including people who only use a local model and copied
// config.example.json, which names all three providers. The missing key
// is said when a request needs it, and a key saved by
// `localcode login anthropic` after startup is used without a restart.
func TestAnAnthropicProviderWithNoKeyWaitsForTheFirstRequest(t *testing.T) {
	home := t.TempDir()
	// Under a lock: the server writes it on its own goroutine, and the race
	// detector does not count a TCP round trip as an ordering.
	var mu sync.Mutex
	var gotKey string
	keySeen := func() string { mu.Lock(); defer mu.Unlock(); return gotKey }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.Header.Get("x-api-key")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {Type: config.ProviderAnthropic, BaseURL: srv.URL},
			"local":     {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1234/v1"},
		},
		Profiles:       map[string]config.Profile{"local": {Provider: "local", Model: "m"}},
		DefaultProfile: "local",
	}
	providers, err := buildProviders(context.Background(), cfg, env{home: home})
	if err != nil {
		t.Fatalf("a provider with no key stopped startup: %v", err)
	}
	req := provider.ChatRequest{Model: "claude-sonnet-5-5", MaxTokens: 16,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("hi")}}}}

	_, err = providers["anthropic"].Chat(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "localcode login anthropic") {
		t.Fatalf("the first request said %v, want how to give it a key", err)
	}
	if keySeen() != "" {
		t.Fatal("a request went out with no key")
	}

	if err := credentials.SaveAnthropicAPIKey(home, "sk-ant-later"); err != nil {
		t.Fatal(err)
	}
	stream, err := providers["anthropic"].Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("the key saved after startup was not used: %v", err)
	}
	for range stream {
	}
	if got := keySeen(); got != "sk-ant-later" {
		t.Errorf("the request carried key %q, want the one saved after startup", got)
	}
}

// A key in the config is used as given.
func TestAnAnthropicProviderWithAKeyIsBuiltAtOnce(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"anthropic": {Type: config.ProviderAnthropic, APIKey: "sk-ant-config", BaseURL: "http://127.0.0.1:1"},
	}}
	providers, err := buildProviders(context.Background(), cfg, env{home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ad, ok := providers["anthropic"].(*provider.AnthropicDirect)
	if !ok || ad.APIKey != "sk-ant-config" || ad.BaseURL != "http://127.0.0.1:1" {
		t.Errorf("provider = %#v, want the key and base URL from the config", providers["anthropic"])
	}
}
