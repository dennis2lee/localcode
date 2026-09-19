package config

import (
	"bytes"
	"strings"
	"testing"
)

// TestOpencodeRealisticConfigLoadsAndResolvesDialableBaseURL verifies that a realistic
// opencode.json — defining providers with npm and options.baseURL, root model, and an agent block —
// loads cleanly and produces profiles whose providers have dialable base_urls:
// - Anthropic's trailing /v1 is stripped so appending /v1/messages targets the host root;
// - OpenAI-compatible's /v1 is preserved verbatim so appending /chat/completions targets /v1/chat/completions.
func TestOpencodeRealisticConfigLoadsAndResolvesDialableBaseURL(t *testing.T) {
	t.Setenv("ANTHROPIC_KEY", "test-anthropic-key")
	t.Setenv("OPENAI_KEY", "test-openai-key")

	body := `{
		"provider": {
			"anthropic": {
				"npm": "@ai-sdk/anthropic",
				"options": {
					"baseURL": "https://api.anthropic.com/v1",
					"apiKey": "{env:ANTHROPIC_KEY}"
				}
			},
			"local": {
				"npm": "@ai-sdk/openai-compatible",
				"options": {
					"baseURL": "http://127.0.0.1:1234/v1",
					"apiKey": "{env:OPENAI_KEY}"
				}
			}
		},
		"model": "anthropic/claude-sonnet-4-5",
		"agent": {
			"reviewer": {
				"description": "Reviews code changes",
				"prompt": "You are a reviewer: check diffs for bugs.",
				"model": "local/google/gemma-3n-e4b"
			},
			"worker": {
				"description": "General worker"
			}
		}
	}`

	cfg, err := loadText(t, body)
	if err != nil {
		t.Fatalf("realistic opencode config failed to load: %v", err)
	}

	// 1. Verify root model synthesised opencode:default and default_profile was set.
	if cfg.DefaultProfile != "opencode:default" {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, "opencode:default")
	}
	defaultProf, ok := cfg.Profiles["opencode:default"]
	if !ok {
		t.Fatalf("opencode:default profile was not synthesised")
	}
	if defaultProf.Provider != "anthropic" || defaultProf.Model != "claude-sonnet-4-5" {
		t.Errorf("opencode:default = %+v, want Provider: anthropic, Model: claude-sonnet-4-5", defaultProf)
	}

	// Dialable base_url for Anthropic: must have stripped /v1 to avoid /v1/v1/messages 404.
	anthropicProv, err := cfg.ResolveProvider(defaultProf)
	if err != nil {
		t.Fatalf("ResolveProvider(defaultProf) failed: %v", err)
	}
	if anthropicProv.BaseURL != "https://api.anthropic.com" {
		t.Errorf("Anthropic BaseURL = %q, want %q (host root with /v1 stripped)", anthropicProv.BaseURL, "https://api.anthropic.com")
	}
	if anthropicProv.APIKey != "test-anthropic-key" {
		t.Errorf("Anthropic APIKey = %q, want test-anthropic-key", anthropicProv.APIKey)
	}

	// 2. Verify agent reviewer synthesised opencode:agent:reviewer pointing to local provider.
	reviewerAgent, ok := cfg.Agents["reviewer"]
	if !ok {
		t.Fatalf("agent reviewer missing from Agents")
	}
	if reviewerAgent.Profile != "opencode:agent:reviewer" {
		t.Errorf("reviewer Profile = %q, want %q", reviewerAgent.Profile, "opencode:agent:reviewer")
	}
	if reviewerAgent.Prompt != "You are a reviewer: check diffs for bugs." {
		t.Errorf("reviewer Prompt = %q", reviewerAgent.Prompt)
	}

	reviewerProf, ok := cfg.Profiles["opencode:agent:reviewer"]
	if !ok {
		t.Fatalf("opencode:agent:reviewer profile was not synthesised")
	}
	if reviewerProf.Provider != "local" || reviewerProf.Model != "google/gemma-3n-e4b" {
		t.Errorf("reviewerProf = %+v, want Provider: local, Model: google/gemma-3n-e4b", reviewerProf)
	}

	// Dialable base_url for OpenAI-compatible: must retain /v1 verbatim so /chat/completions is appended.
	localProv, err := cfg.ResolveProvider(reviewerProf)
	if err != nil {
		t.Fatalf("ResolveProvider(reviewerProf) failed: %v", err)
	}
	if localProv.BaseURL != "http://127.0.0.1:1234/v1" {
		t.Errorf("OpenAI-compatible BaseURL = %q, want %q (verbatim with /v1)", localProv.BaseURL, "http://127.0.0.1:1234/v1")
	}

	// 3. Verify agent worker without model/temp/top_p inherits default_profile.
	workerAgent, ok := cfg.Agents["worker"]
	if !ok {
		t.Fatalf("agent worker missing from Agents")
	}
	if workerAgent.Profile != "opencode:default" {
		t.Errorf("worker Profile = %q, want %q (inherited default_profile)", workerAgent.Profile, "opencode:default")
	}
}

// TestOpencodeModelUndefinedProviderRefusedWithTableSentence verifies that a root model
// whose left half names an undefined provider is refused with the exact sentence from the table.
func TestOpencodeModelUndefinedProviderRefusedWithTableSentence(t *testing.T) {
	_, err := loadText(t, `{"model":"openai/gpt-5.1-codex"}`)
	if err == nil {
		t.Fatal("model naming undefined provider loaded without error, want refusal")
	}
	wantSentence := `model is "openai/gpt-5.1-codex", and "openai" is not a provider this config defines. opencode resolves that name against its models.dev catalogue; localcode never reaches a catalogue, so it has no endpoint, no credential and no limits for it. Add a providers."openai" block naming its type, base_url and key, or write the model under a provider this file already defines.`
	if !strings.Contains(err.Error(), wantSentence) {
		t.Errorf("error = %q\nwant containing sentence:\n%q", err.Error(), wantSentence)
	}
}

// TestOpencodeNpmUnknownRefusedWithTableSentence verifies that an unknown npm package
// is refused with the exact sentence from the table.
func TestOpencodeNpmUnknownRefusedWithTableSentence(t *testing.T) {
	_, err := loadText(t, `{"provider":{"openai":{"npm":"@ai-sdk/openai"}}}`)
	if err == nil {
		t.Fatal("unknown npm package loaded without error, want refusal")
	}
	wantSentence := `provider "openai": npm is "@ai-sdk/openai", and localcode has no client for it — localcode speaks three protocols (Anthropic messages, Bedrock Converse, OpenAI chat/completions) and installs nothing at runtime. Use "@ai-sdk/openai-compatible" if the endpoint serves /v1/chat/completions.`
	if !strings.Contains(err.Error(), wantSentence) {
		t.Errorf("error = %q\nwant containing sentence:\n%q", err.Error(), wantSentence)
	}
}

// TestOpencodeModelFirstSlashSplitPreservesSlashes verifies that splitting root model
// at the first slash preserves slashes within model IDs such as "google/gemma-3n-e4b".
func TestOpencodeModelFirstSlashSplitPreservesSlashes(t *testing.T) {
	body := `{
		"provider": {
			"lmstudio": {
				"npm": "@ai-sdk/openai-compatible",
				"options": {
					"baseURL": "http://127.0.0.1:1234/v1"
				}
			}
		},
		"model": "lmstudio/google/gemma-3n-e4b"
	}`

	cfg, err := loadText(t, body)
	if err != nil {
		t.Fatalf("config with multi-slash model failed to load: %v", err)
	}
	prof, ok := cfg.Profiles["opencode:default"]
	if !ok {
		t.Fatalf("opencode:default profile missing")
	}
	if prof.Provider != "lmstudio" {
		t.Errorf("Provider = %q, want %q", prof.Provider, "lmstudio")
	}
	if prof.Model != "google/gemma-3n-e4b" {
		t.Errorf("Model = %q, want %q (second slash preserved)", prof.Model, "google/gemma-3n-e4b")
	}
}

// TestOpencodeAgentToolsAndPermissionRefused verifies that agent.tools and agent.permission
// are refused with their exact respective table sentences.
// Rationale: localcode permissions are daemon-wide, and per-agent tools is a closed allowlist
// rather than per-tool on/off switches; neither can be translated without changing config semantics.
func TestOpencodeAgentToolsAndPermissionRefused(t *testing.T) {
	// agent.tools
	_, errTools := loadText(t, `{
		"provider":{"anthropic":{"npm":"@ai-sdk/anthropic"}},
		"model":"anthropic/claude-sonnet-4-5",
		"agent":{
			"readonly":{"tools":{"edit":false}}
		}
	}`)
	if errTools == nil {
		t.Fatal("agent.tools loaded without error, want refusal")
	}
	wantToolsSentence := `agent "readonly": tools is a set of per-tool on/off switches, and localcode's per-agent tools is a closed allowlist — the two cannot be converted without changing what this file says. Write the restriction as a top-level "permission" map if it is meant for every agent; localcode has no per-agent permission.`
	if !strings.Contains(errTools.Error(), wantToolsSentence) {
		t.Errorf("error = %q\nwant sentence:\n%q", errTools.Error(), wantToolsSentence)
	}

	// agent.permission
	_, errPerm := loadText(t, `{
		"provider":{"anthropic":{"npm":"@ai-sdk/anthropic"}},
		"model":"anthropic/claude-sonnet-4-5",
		"agent":{
			"build":{"permission":{"bash":"allow"}}
		}
	}`)
	if errPerm == nil {
		t.Fatal("agent.permission loaded without error, want refusal")
	}
	wantPermSentence := `agent "build": permission sets rules for this agent alone, and localcode's permission rules are daemon-wide — folding them in would apply them to every agent. Move them to the top-level "permission", which localcode already reads, if that is what you mean.`
	if !strings.Contains(errPerm.Error(), wantPermSentence) {
		t.Errorf("error = %q\nwant sentence:\n%q", errPerm.Error(), wantPermSentence)
	}
}

// TestOpencodeHandwrittenOpencodeProfileRefusedByValidate verifies that Validate
// refuses a hand-written profile whose name begins with "opencode:", preventing
// silent collisions with synthesised profile names.
func TestOpencodeHandwrittenOpencodeProfileRefusedByValidate(t *testing.T) {
	_, err := loadText(t, `{
		"providers": {"anthropic": {"type": "anthropic", "api_key": "k"}},
		"profiles": {
			"opencode:whatever": {
				"provider": "anthropic",
				"model": "claude-sonnet-4-5"
			}
		},
		"default_profile": "opencode:whatever"
	}`)
	if err == nil {
		t.Fatal("hand-written profile named opencode:whatever loaded without error, want refusal")
	}
	want := `profile "opencode:whatever": a profile whose name begins with "opencode:" is reserved for keys read from an opencode file; rename yours`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q\nwant containing:\n%q", err.Error(), want)
	}
}

// TestOpencodeRegressionGuardLocalcodeConfigUntouched verifies that a working localcode config.json
// containing providers (including a bedrock block with its AWS profile field), profiles, agents,
// and default_profile loads byte-for-byte identically and resolves the same profile for the same agent.
func TestOpencodeRegressionGuardLocalcodeConfigUntouched(t *testing.T) {
	raw := []byte(`{` +
		`"providers":{"bedrock":{"type":"bedrock","region":"us-west-2","profile":"my-aws-profile"}},` +
		`"profiles":{"main":{"provider":"bedrock","model":"us.anthropic.claude-sonnet-4-6"}},` +
		`"default_profile":"main",` +
		`"agents":{"general-purpose":{"profile":"main"}}` +
		`}`)

	norm, err := NormalizeOpencode(raw)
	if err != nil {
		t.Fatalf("localcode config failed normalisation: %v", err)
	}
	if !bytes.Equal(norm.JSON, raw) {
		t.Errorf("regression guard failed: output is not byte-for-byte identical:\ngot:  %s\nwant: %s", string(norm.JSON), string(raw))
	}

	cfg, err := loadText(t, string(raw))
	if err != nil {
		t.Fatalf("localcode config failed to load: %v", err)
	}
	prof, err := cfg.ResolveProfile("general-purpose")
	if err != nil {
		t.Fatalf("ResolveProfile(general-purpose) failed: %v", err)
	}
	if prof.Provider != "bedrock" || prof.Model != "us.anthropic.claude-sonnet-4-6" {
		t.Errorf("resolved profile = %+v, want Provider: bedrock, Model: us.anthropic.claude-sonnet-4-6", prof)
	}
	prov, err := cfg.ResolveProvider(prof)
	if err != nil {
		t.Fatalf("ResolveProvider failed: %v", err)
	}
	if prov.Profile != "my-aws-profile" || prov.Region != "us-west-2" {
		t.Errorf("resolved provider = %+v, want Profile: my-aws-profile, Region: us-west-2", prov)
	}
}

// TestOpencodeAnthropicV1TrapAndInvalidPathRefused verifies that Anthropic options.baseURL
// strips trailing /v1, and refuses URLs whose path ends in a non-v1 segment.
func TestOpencodeAnthropicV1TrapAndInvalidPathRefused(t *testing.T) {
	// Valid: trailing /v1 is stripped to host root
	cfg, err := loadText(t, `{
		"provider": {
			"anthropic": {
				"npm": "@ai-sdk/anthropic",
				"options": {
					"baseURL": "https://proxy.example.com/v1"
				}
			}
		},
		"model": "anthropic/claude-sonnet-4-5"
	}`)
	if err != nil {
		t.Fatalf("valid Anthropic baseURL failed to load: %v", err)
	}
	if got := cfg.Providers["anthropic"].BaseURL; got != "https://proxy.example.com" {
		t.Errorf("BaseURL = %q, want https://proxy.example.com (trailing /v1 stripped)", got)
	}

	// Invalid: non-v1 path segment refuses
	_, errBad := loadText(t, `{
		"provider": {
			"anthropic": {
				"npm": "@ai-sdk/anthropic",
				"options": {
					"baseURL": "https://proxy.example.com/anthropic"
				}
			}
		},
		"model": "anthropic/claude-sonnet-4-5"
	}`)
	if errBad == nil {
		t.Fatal("Anthropic baseURL with non-v1 path loaded without error, want refusal")
	}
	wantSentence := `provider "anthropic": options.baseURL is "https://proxy.example.com/anthropic", and localcode's anthropic client always appends /v1/messages, so there is no base_url that reaches the path this names.`
	if !strings.Contains(errBad.Error(), wantSentence) {
		t.Errorf("error = %q\nwant sentence:\n%q", errBad.Error(), wantSentence)
	}
}

// TestOpencodeProviderBlacklistAndWhitelist verifies blacklist overlap and whitelist omission refusals.
func TestOpencodeProviderBlacklistAndWhitelist(t *testing.T) {
	// Blacklist overlap
	_, errBL := loadText(t, `{
		"provider": {
			"anthropic": {
				"npm": "@ai-sdk/anthropic",
				"blacklist": ["claude-opus-4-5"]
			}
		},
		"model": "anthropic/claude-opus-4-5"
	}`)
	if errBL == nil {
		t.Fatal("blacklisted model loaded without error, want refusal")
	}
	wantBL := `provider "anthropic".blacklist hides "claude-opus-4-5", and profile "opencode:default" is that model on that provider. localcode's /model lists profiles and cannot hide one — remove the profile, or the blacklist entry.`
	if !strings.Contains(errBL.Error(), wantBL) {
		t.Errorf("error = %q\nwant containing:\n%q", errBL.Error(), wantBL)
	}

	// Whitelist omission
	_, errWL := loadText(t, `{
		"provider": {
			"anthropic": {
				"npm": "@ai-sdk/anthropic",
				"whitelist": ["claude-haiku-4-5"]
			}
		},
		"model": "anthropic/claude-opus-4-5"
	}`)
	if errWL == nil {
		t.Fatal("whitelisted provider omitting model loaded without error, want refusal")
	}
	wantWL := `provider "anthropic".whitelist keeps only the models it lists, and profile "opencode:default" is "claude-opus-4-5" on that provider, which it does not list. localcode's /model lists profiles and cannot hide one — remove the profile, or add the model.`
	if !strings.Contains(errWL.Error(), wantWL) {
		t.Errorf("error = %q\nwant containing:\n%q", errWL.Error(), wantWL)
	}
}

// TestOpencodeEnabledAndDisabledProviders verifies enabled_providers and disabled_providers refusals.
func TestOpencodeEnabledAndDisabledProviders(t *testing.T) {
	// disabled_providers overlap
	_, errDP := loadText(t, `{
		"provider": {
			"openai": {
				"npm": "@ai-sdk/openai-compatible",
				"options": {"baseURL": "http://127.0.0.1:1234/v1"}
			}
		},
		"disabled_providers": ["openai"]
	}`)
	if errDP == nil {
		t.Fatal("disabled_providers overlap loaded without error, want refusal")
	}
	wantDP := `disabled_providers lists "openai", and this file also defines providers.openai — localcode has no automatic provider loading to switch off, so it would load that block and use it. Remove "openai" from disabled_providers, or remove the providers.openai block.`
	if !strings.Contains(errDP.Error(), wantDP) {
		t.Errorf("error = %q\nwant containing:\n%q", errDP.Error(), wantDP)
	}

	// enabled_providers omission
	_, errEP := loadText(t, `{
		"provider": {
			"openai": {
				"npm": "@ai-sdk/openai-compatible",
				"options": {"baseURL": "http://127.0.0.1:1234/v1"}
			}
		},
		"enabled_providers": ["anthropic"]
	}`)
	if errEP == nil {
		t.Fatal("enabled_providers omission loaded without error, want refusal")
	}
	wantEP := `enabled_providers does not list "openai", and this file defines providers.openai — enabled_providers means every other provider is ignored, but localcode would still load that block and use it. Add "openai" to enabled_providers, or remove the providers.openai block.`
	if !strings.Contains(errEP.Error(), wantEP) {
		t.Errorf("error = %q\nwant containing:\n%q", errEP.Error(), wantEP)
	}
}

// TestOpencodeModeHonouredAsAgentDeprecatedSpelling verifies that mode is honoured as the deprecated
// spelling of agent, subject to the same field-level rules.
func TestOpencodeModeHonouredAsAgentDeprecatedSpelling(t *testing.T) {
	cfg, err := loadText(t, `{
		"provider": {
			"anthropic": {
				"npm": "@ai-sdk/anthropic"
			}
		},
		"model": "anthropic/claude-sonnet-4-5",
		"mode": {
			"custom-agent": {
				"prompt": "You are a custom mode agent."
			}
		}
	}`)
	if err != nil {
		t.Fatalf("mode block failed to load: %v", err)
	}
	ag, ok := cfg.Agents["custom-agent"]
	if !ok {
		t.Fatalf("custom-agent from mode block missing in Agents")
	}
	if ag.Prompt != "You are a custom mode agent." {
		t.Errorf("Prompt = %q", ag.Prompt)
	}
	if ag.Profile != "opencode:default" {
		t.Errorf("Profile = %q, want inherited opencode:default", ag.Profile)
	}
}
