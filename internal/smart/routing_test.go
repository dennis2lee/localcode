package smart

import (
	"testing"

	"localcode/internal/config"
)

func profiles(models map[string]string) *config.Config {
	p := map[string]config.Profile{}
	for name, model := range models {
		p[name] = config.Profile{Provider: "x", Model: model}
	}
	return &config.Config{Profiles: p}
}

func TestRoutingPicksByWeightClass(t *testing.T) {
	cfg := profiles(map[string]string{
		"flagship": "claude-opus-5",
		"mid":      "claude-sonnet-5",
		"cheap":    "claude-haiku-4-5",
	})
	for _, tc := range []struct{ category, want string }{
		{CategoryQuick, "cheap"},
		{CategoryBalanced, "mid"},
		{CategoryDeep, "flagship"},
	} {
		if got := ProfileFor(cfg, tc.category); got != tc.want {
			t.Errorf("%s routed to %q, want %q", tc.category, got, tc.want)
		}
	}
}

// The case the naive "first marker wins" version got wrong. "gpt-5-mini"
// contains "gpt-5", which is a deep marker, so on a config that also has a
// real flagship it would take the deep category and the cheapest model
// would be doing the reviewing.
func TestASmallModelDoesNotWinTheDeepCategory(t *testing.T) {
	cfg := profiles(map[string]string{
		"small": "gpt-5-mini",
		"full":  "gpt-5",
	})
	if got := ProfileFor(cfg, CategoryDeep); got != "full" {
		t.Errorf("deep routed to %q, want the full model", got)
	}
	if got := ProfileFor(cfg, CategoryQuick); got != "small" {
		t.Errorf("quick routed to %q, want the mini", got)
	}
}

// The documented way to settle it by hand. A heuristic over model names is
// a guess, and somebody who knows which model they want their reviews on
// needs a way to say so that no guess can outvote.
func TestAnExplicitlyNamedProfileWinsOutright(t *testing.T) {
	cfg := profiles(map[string]string{
		"smart-deep": "some-local-build-nobody-can-classify",
		"flagship":   "claude-opus-5",
	})
	if got := ProfileFor(cfg, CategoryDeep); got != "smart-deep" {
		t.Errorf("deep routed to %q, want the profile named for it", got)
	}
}

// One model configured is the common case for a local setup, and every
// specialist running on it is the correct answer: the value is still the
// separate context, not the different model.
func TestOneProfileTakesEverything(t *testing.T) {
	cfg := profiles(map[string]string{"only": "qwen3-coder-30b"})
	for _, category := range Categories {
		if got := ProfileFor(cfg, category); got != "only" {
			t.Errorf("%s routed to %q, want the only profile there is", category, got)
		}
	}
}

// No model in the config says what class it is — a local server serving
// "model" — so nothing matches and the chain has to end somewhere.
func TestUnclassifiableModelsFallBackToTheDefault(t *testing.T) {
	cfg := profiles(map[string]string{"a": "model", "b": "another"})
	cfg.DefaultProfile = "b"
	for _, category := range Categories {
		if got := ProfileFor(cfg, category); got != "b" {
			t.Errorf("%s routed to %q, want the default profile", category, got)
		}
	}
	cfg.DefaultProfile = ""
	if got := ProfileFor(cfg, CategoryDeep); got != "a" {
		t.Errorf("with no default, routed to %q, want the first profile by name", got)
	}
}

func TestNoProfilesRoutesNowhere(t *testing.T) {
	if got := ProfileFor(&config.Config{}, CategoryDeep); got != "" {
		t.Errorf("got %q, want no answer at all", got)
	}
}

// Routing decides which prompt prefix gets cached, so the same config has
// to route the same way every time. Map iteration order is the thing that
// would break this quietly.
func TestRoutingIsStable(t *testing.T) {
	cfg := profiles(map[string]string{
		"a": "claude-opus-5", "b": "claude-opus-5", "c": "claude-opus-5",
	})
	first := ProfileFor(cfg, CategoryDeep)
	for i := 0; i < 50; i++ {
		if got := ProfileFor(cfg, CategoryDeep); got != first {
			t.Fatalf("routed to %q then %q for the same config", first, got)
		}
	}
}
