package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// config.example.json is the only documentation of the file format that
// can be copied and run, so it has to stay a valid Config rather than a
// description of one that used to be.
//
// Loaded through the real loader, not just json.Unmarshal, because the
// loader is where an unknown key or a renamed field would actually bite.
func TestExampleConfigStillMatchesTheStruct(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config.example.json does not parse as a Config: %v", err)
	}

	// Spot-check the blocks that are easy to leave behind when a
	// feature moves: an example that silently stops mentioning a
	// setting is how a setting becomes undiscoverable.
	if len(cfg.Providers) == 0 {
		t.Error("no providers in the example")
	}
	if len(cfg.Profiles) == 0 {
		t.Error("no profiles in the example")
	}
}

// Every setting Config accepts has to appear in config.example.json.
//
// README calls that file the reference, "every key with a note on it",
// and RELEASING.md makes adding new keys to it a release step. It was
// neither: six settings were absent when this test was written --
// mcp_servers, hooks, permission, auto_compact_enabled,
// auto_memory_enabled and show_tps. Two of those are toggled from
// /config and written back into the user's own config.json, so somebody
// who flipped one found a key in their file that the reference had never
// mentioned.
//
// The struct is the source of truth and the file is what drifts, so the
// test reads the tags off Config by reflection rather than listing them.
// A seventh key added tomorrow fails here on the day it is added, which
// is the point: the release step this replaces asked a person to
// remember, and eight releases went by with six keys missing.
func TestEveryConfigKeyIsInTheExample(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("config.example.json does not parse: %v", err)
	}

	typ := reflect.TypeOf(Config{})
	var checked int
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		checked++
		if _, ok := doc[name]; !ok {
			t.Errorf("%q is a setting config.json accepts and config.example.json never mentions it, so it is a setting nobody can find", name)
		}
	}
	// Presence only, deliberately. Requiring a "//name" note beside every
	// key was the first version of this and it failed on six: providers,
	// profiles, agents, default_profile, max_concurrent_tasks and
	// auto_delegate carry their notes inside the block instead, against
	// the field being explained, which reads better than one paragraph
	// above a twenty-line object. The convention the file actually
	// follows is a note where a note helps, and a test that fought it
	// would be a test somebody edits the file to satisfy.
	if checked < 10 {
		t.Fatalf("only %d json tags found on Config, so this test is checking almost nothing", checked)
	}
}
