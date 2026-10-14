package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README's First run section is what a new install is pointed to: the
// installer, the no-config error and `localcode login anthropic` all name
// it. Each config there has to load and give the default agent a profile,
// or the first thing a new user copies stops localcode, which is what
// copying config.example.json used to do for anyone without all three of
// its providers.
func TestTheREADMEFirstRunConfigsLoadAndAnswer(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "### First run")
	if start < 0 {
		t.Fatal("README.md has no First run section; the installer, the no-config error and the login command point to it")
	}
	section := text[start:]
	if end := strings.Index(section[len("### First run"):], "\n## "); end >= 0 {
		section = section[:len("### First run")+end]
	}
	blocks := regexp.MustCompile("(?s)```json\\s*\\n(.*?)```").FindAllStringSubmatch(section, -1)
	if len(blocks) < 3 {
		t.Fatalf("found %d configs in First run, want one each for Anthropic, Bedrock and a local server", len(blocks))
	}
	kinds := map[ProviderType]bool{}
	for i, b := range blocks {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(b[1]), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := Load(path)
		if err != nil {
			t.Errorf("config %d does not load: %v\n%s", i+1, err, b[1])
			continue
		}
		p, err := cfg.ResolveProfile("general-purpose")
		if err != nil {
			t.Errorf("config %d gives the default agent no profile: %v", i+1, err)
			continue
		}
		kinds[cfg.Providers[p.Provider].Type] = true
	}
	for _, k := range []ProviderType{ProviderAnthropic, ProviderBedrock, ProviderOpenAICompat} {
		if !kinds[k] {
			t.Errorf("no First run config for a %s provider", k)
		}
	}
}
