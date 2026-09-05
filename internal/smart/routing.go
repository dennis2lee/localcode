package smart

import (
	"sort"
	"strings"

	"localcode/internal/config"
)

// Category routing.
//
// A specialist needs a model, and the obvious way to give it one is to
// name the model in the agent definition. That is wrong for a reason
// worth stating: this file ships in a binary, and the models it would
// name are not the models the person running it has. Somebody is on two
// local servers, somebody else on Bedrock, somebody else on one model for
// everything. A hard-coded "claude-opus-5" is a roster that works on the
// developer's machine and nowhere else.
//
// So a specialist names a capability class instead — "this wants the
// cheap fast one", "this wants the strongest one" — and the class is
// resolved against whatever profiles are actually configured, at load
// time. The orchestration stays the same when the models change, which is
// the whole point: the roster outlives the model lineup.
const (
	// CategoryQuick is for work that is mechanical and high-volume:
	// finding files, running a build, reading a directory. Speed and
	// price matter more than judgement.
	CategoryQuick = "quick"

	// CategoryBalanced is for work that changes things. It wants a model
	// good enough to be trusted with an edit, without spending the
	// strongest one on it.
	CategoryBalanced = "balanced"

	// CategoryDeep is for judgement: review, planning, making sense of an
	// unfamiliar subsystem. The expensive call, made rarely.
	CategoryDeep = "deep"
)

// Categories lists the routing categories, lightest first. The order is
// load-bearing: classify walks it and takes the first class a model id
// matches, which is what makes a size qualifier beat a family name.
var Categories = []string{CategoryQuick, CategoryBalanced, CategoryDeep}

// markers are the substrings that identify a model's weight class from
// its id, checked lowercased.
//
// This is a heuristic and it is allowed to be one. Getting it wrong costs
// a specialist running on a model heavier or lighter than ideal, which is
// a price and a latency difference; getting it wrong is not a correctness
// failure, and the escape hatch below (a profile named "smart-<category>")
// settles it exactly for anyone who cares.
//
// Parameter counts are included because local model ids carry them and
// nothing else in the id says how big the thing is. "8b" is a different
// model class from "70b" no matter whose it is.
var markers = map[string][]string{
	CategoryQuick: {
		"haiku", "mini", "flash", "nano", "lite", "tiny", "turbo", "small",
		"-1b", "-2b", "-3b", "-4b", "-7b", "-8b", "-9b", "1.5b",
	},
	CategoryBalanced: {
		"sonnet", "coder", "medium", "gpt-4", "gpt-oss",
		"-12b", "-13b", "-14b", "-22b", "-24b", "-27b", "-30b", "-32b",
	},
	CategoryDeep: {
		"opus", "gpt-5", "-o3", "-o4", "pro", "ultra", "max", "large",
		"thinking", "reason", "-r1", "-70b", "-72b", "-120b", "-235b", "-405b",
	},
}

// fallbacks is the chain tried when no profile matches a category
// directly: the nearest class first, then the rest.
//
// Nearest rather than strongest, because "no quick model configured"
// should not silently route every grep to the most expensive model
// available. Somebody with one model configured gets that model for
// everything, which is correct and is what happens at the end of the
// chain anyway.
var fallbacks = map[string][]string{
	CategoryQuick:    {CategoryBalanced, CategoryDeep},
	CategoryBalanced: {CategoryDeep, CategoryQuick},
	CategoryDeep:     {CategoryBalanced, CategoryQuick},
}

// ProfileFor picks the profile a category should run on, or "" when cfg
// has no profiles at all.
//
// The order is: an explicit override, then a model whose id says it is
// this class, then the neighbouring classes, then the configured default,
// then the first profile by name. Every step is deterministic — two runs
// against the same config route the same way, which matters because the
// routing decides which prompt prefix gets cached.
func ProfileFor(cfg *config.Config, category string) string {
	if cfg == nil || len(cfg.Profiles) == 0 {
		return ""
	}

	// The escape hatch, and the documented way to pin this. A profile
	// named "smart-deep" is somebody saying which model they want the
	// deep specialists on, and no heuristic should get a vote after that.
	if _, ok := cfg.Profiles["smart-"+category]; ok {
		return "smart-" + category
	}

	if name := bestMatch(cfg.Profiles, category); name != "" {
		return name
	}
	for _, next := range fallbacks[category] {
		if name := bestMatch(cfg.Profiles, next); name != "" {
			return name
		}
	}
	if cfg.DefaultProfile != "" {
		if _, ok := cfg.Profiles[cfg.DefaultProfile]; ok {
			return cfg.DefaultProfile
		}
	}
	return firstByName(cfg.Profiles)
}

// bestMatch returns the first profile, by name, whose model belongs to
// category — or "" if none does.
// Solo reports whether every routing category lands on the same profile,
// which is to say that delegating is the model handing work to itself.
//
// Measured, on the model this matters for. With one profile configured,
// all six specialists resolve to it; the orchestration prompt then orders
// the model to delegate a search rather than grep — and its first move
// was Task(explore), after which the sub-agent, the same 30B muse, spent
// thirteen requests on glob and grep in a two-file project, found the
// answer at the eleventh, kept going, and edited nothing. The same task
// with the switch off finished in thirteen requests with both files
// correct.
//
// The test is what every category RESOLVES to, not how many profiles are
// written down, and the difference is the population this exists for.
// classify reads a weight class out of a model id (routing_test.go has
// two profiles named "model" and "another" that it cannot classify at
// all), so two local endpoints whose ids carry no size qualifier route
// every category to the same profile — exactly the setup this is meant to
// catch, and exactly what a "len(Profiles) == 1" test would let through.
func Solo(cfg *config.Config) bool {
	if cfg == nil || len(cfg.Profiles) == 0 {
		return false
	}
	first := ""
	for _, category := range Categories {
		got := ProfileFor(cfg, category)
		if got == "" {
			return false
		}
		if first == "" {
			first = got
		} else if got != first {
			return false
		}
	}
	return first != ""
}

func bestMatch(profiles map[string]config.Profile, category string) string {
	for _, name := range sortedNames(profiles) {
		if class, ok := classify(profiles[name].Model); ok && class == category {
			return name
		}
	}
	return ""
}

// classify decides which weight class a model id belongs to, and reports
// false for an id that says nothing about its size.
//
// The order of the search is the whole rule: the lightest class that
// matches wins. Model ids are built by gluing a size qualifier onto a
// family name, and the qualifier is the specific half — "gpt-5-mini"
// carries "gpt-5", which is a flagship family, and "mini", which is what
// this particular model actually is. Scoring the two against each other
// makes the cheapest model in a config the one doing the reviewing, which
// is the exact opposite of the intent.
func classify(model string) (string, bool) {
	id := strings.ToLower(model)
	for _, category := range Categories {
		for _, m := range markers[category] {
			if strings.Contains(id, m) {
				return category, true
			}
		}
	}
	return "", false
}

func sortedNames(profiles map[string]config.Profile) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func firstByName(profiles map[string]config.Profile) string {
	names := sortedNames(profiles)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}
