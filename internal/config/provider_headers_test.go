package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// teamKey is a made-up header value, distinctive enough that a test can
// say it appears nowhere it should not.
const teamKey = "made-up-team-key-0001"

// A headers block on an openai-compat or anthropic provider loads, and is
// sent as api_key is: each value trimmed, and an empty one left out, so an
// optional {env:} placeholder whose variable is unset sends nothing.
// Header values take {env:NAME} like every other string in the file, and
// a setting written back from the UI keeps the placeholder on disk.
func TestProviderHeadersLoadAndAreSentTrimmed(t *testing.T) {
	t.Setenv("LC_TEST_TEAM_KEY", teamKey)
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"providers": {
		"gw": {"type": "openai-compat", "base_url": "https://gateway.example.com/v1", "api_key": "k",
			"headers": {"X-Team-Key": "{env:LC_TEST_TEAM_KEY}", "X-Region": " eu-1\n", "X-Optional": "{env:LC_TEST_UNSET_HEADER:-}", "x-api-key": "allowed here"}},
		"claude": {"type": "anthropic", "headers": {"X-Team-Key": "abc"}}},
	  "profiles": {"main": {"provider": "gw", "model": "CodeA"}}, "default_profile": "main"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("a config with provider headers was refused: %v", err)
	}
	got := cfg.Providers["gw"].SentHeaders()
	want := map[string]string{"X-Team-Key": teamKey, "X-Region": "eu-1", "x-api-key": "allowed here"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SentHeaders = %q, want %q", got, want)
	}
	if got := cfg.Providers["claude"].SentHeaders(); got["X-Team-Key"] != "abc" {
		t.Errorf("anthropic SentHeaders = %q", got)
	}
	if got := (ProviderConfig{Headers: map[string]string{"X-A": " "}}).SentHeaders(); got != nil {
		t.Errorf("only empty values: %q, want nil", got)
	}

	if err := AddPermissionRuleToFile(path, "bash", PermissionRule{Match: "git status", Decision: DecisionAllow}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "{env:LC_TEST_TEAM_KEY}") || strings.Contains(string(after), teamKey) {
		t.Errorf("writing the file back replaced the header's placeholder with its value:\n%s", after)
	}
}

// What cannot work is refused at load, with an error that names the
// provider and the header and never quotes the value: a header bedrock
// would never send, a name HTTP cannot carry, two spellings of one name, a
// header localcode or the HTTP client sets itself, and a value with a line
// break or a control character inside it.
func TestProviderHeadersThatCannotWorkAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   ProviderType
		hdrs  map[string]string
		wants []string
	}{
		{"bedrock", ProviderBedrock, map[string]string{"X-Team-Key": teamKey}, []string{"not supported on a bedrock provider", "AWS SDK"}},
		{"empty name", ProviderOpenAICompat, map[string]string{"": teamKey}, []string{"empty name"}},
		{"not a token", ProviderOpenAICompat, map[string]string{"X Team Key": teamKey}, []string{`"X Team Key" is not a valid HTTP header name`}},
		{"twice", ProviderOpenAICompat, map[string]string{"X-Team-Key": teamKey, "x-team-key": teamKey}, []string{`"X-Team-Key" and "x-team-key", which are the same header`}},
		{"authorization", ProviderOpenAICompat, map[string]string{"authorization": teamKey}, []string{`"authorization" is one localcode sets`, "put the token in api_key instead"}},
		{"content type", ProviderOpenAICompat, map[string]string{"Content-Type": teamKey}, []string{`"Content-Type" is one localcode sets`}},
		{"anthropic authorization", ProviderAnthropic, map[string]string{"Authorization": teamKey}, []string{"put the key in api_key instead"}},
		{"anthropic key", ProviderAnthropic, map[string]string{"X-Api-Key": teamKey}, []string{`"X-Api-Key" is one localcode sets`, "api_key"}},
		{"anthropic version", ProviderAnthropic, map[string]string{"Anthropic-Version": teamKey}, []string{`"Anthropic-Version" is one localcode sets`}},
		{"host", ProviderOpenAICompat, map[string]string{"Host": teamKey}, []string{`"Host" is written by the HTTP client`}},
		{"connection", ProviderAnthropic, map[string]string{"Connection": teamKey}, []string{`"Connection" is written by the HTTP client`}},
		{"line break", ProviderOpenAICompat, map[string]string{"X-Team-Key": teamKey + "\nX-Other: y"}, []string{`header "X-Team-Key" has a line break in it`}},
		{"carriage return", ProviderOpenAICompat, map[string]string{"X-Team-Key": "a\rb" + teamKey}, []string{"line break"}},
		{"control character", ProviderOpenAICompat, map[string]string{"X-Team-Key": teamKey + "\x00"}, []string{"control character"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Providers: map[string]ProviderConfig{"gw": {Type: tc.typ, BaseURL: "https://gateway.example.com", Headers: tc.hdrs}}}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("loaded")
			}
			for _, want := range append(tc.wants, `provider "gw"`) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not say %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), teamKey) {
				t.Errorf("the error quotes the header's value: %v", err)
			}
		})
	}

	// And what is fine is not: a tab is a header character, and a newline
	// at the end of a pasted value is trimmed rather than refused.
	ok := &Config{Providers: map[string]ProviderConfig{"gw": {Type: ProviderOpenAICompat, Headers: map[string]string{
		"X-Team-Key": teamKey + "\n", "X-Note": "a\tb", "X-Api-Key": teamKey, "Anthropic-Version": "x",
	}}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid headers on openai-compat were refused: %v", err)
	}
}

// opencode's options.headers is localcode's headers one level down, and is
// read as them. On bedrock it is refused, in opencode's terms, as before:
// the AWS SDK builds those requests. Two spellings that disagree are
// refused. A model's own headers stay refused, since localcode sends
// headers per provider.
func TestOpencodeOptionsHeadersBecomeProviderHeaders(t *testing.T) {
	cfg, err := loadText(t, `{"provider": {"gw": {"npm": "@ai-sdk/openai-compatible",
		"options": {"baseURL": "https://gateway.example.com/v1", "apiKey": "k", "headers": {"X-Team-Key": "`+teamKey+`"}}}},
		"model": "gw/CodeA"}`)
	if err != nil {
		t.Fatalf("options.headers was refused: %v", err)
	}
	if got := cfg.Providers["gw"].Headers["X-Team-Key"]; got != teamKey {
		t.Errorf("headers = %q, want options.headers carried across", cfg.Providers["gw"].Headers)
	}

	for name, tc := range map[string]struct{ body, want string }{
		"bedrock": {`{"provider": {"aws": {"npm": "@ai-sdk/amazon-bedrock", "options": {"region": "us-west-2", "headers": {"X-Team-Key": "v"}}}}}`,
			"localcode's bedrock requests are built by the AWS SDK"},
		"disagree": {`{"providers": {"gw": {"type": "openai-compat", "base_url": "https://gateway.example.com/v1", "headers": {"X-Team-Key": "a"},
			"options": {"headers": {"X-Team-Key": "b"}}}}}`, "headers and options.headers are two spellings of the same block"},
		"per model": {`{"provider": {"gw": {"npm": "@ai-sdk/openai-compatible", "options": {"baseURL": "https://gateway.example.com/v1"},
			"models": {"CodeA": {"headers": {"X-Team-Key": "v"}}}}}}`, "localcode sends headers per provider, not per model"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadText(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}

	// The same block under both spellings is one block, not a conflict.
	cfg, err = loadText(t, `{"providers": {"gw": {"type": "openai-compat", "base_url": "https://gateway.example.com/v1",
		"headers": {"X-Team-Key": "a"}, "options": {"headers": {"X-Team-Key": "a"}}}}}`)
	if err != nil || cfg.Providers["gw"].Headers["X-Team-Key"] != "a" {
		t.Errorf("the same headers under both spellings: %v, %+v", err, cfg)
	}
}
