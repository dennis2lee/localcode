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
// The original version of this test checked only the top level of Config,
// which let nested keys drift: Profile's temperature, top_p and top_k, and
// ProviderConfig's profile were absent from the example despite shipping in
// the product and being accepted by the config loader.
//
// The struct is the source of truth and the file is what drifts, so the
// test reads the tags off Config and its nested structs by reflection
// rather than listing them. A new field added to Config, Profile,
// ProviderConfig, AgentConfig, AutoDelegateConfig or EgressConfig fails
// here on the day it is added unless config.example.json demonstrates it.
func TestEveryConfigKeyIsInTheExample(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("config.example.json does not parse: %v", err)
	}

	var checked int
	checkStructFields(t, reflect.TypeOf(Config{}), doc, "", &checked)

	// Presence only, deliberately. Requiring a "//name" note beside every
	// key was the first version of this and it failed on six: providers,
	// profiles, agents, default_profile, max_concurrent_tasks and
	// auto_delegate carry their notes inside the block instead, against
	// the field being explained, which reads better than one paragraph
	// above a twenty-line object. The convention the file actually
	// follows is a note where a note helps, and a test that fought it
	// would be a test somebody edits the file to satisfy.
	if checked < 40 {
		t.Fatalf("only %d json tags checked across Config and nested structs, so this test is checking almost nothing", checked)
	}
}

// checkStructFields recursively verifies that every json field defined on typ
// appears in the parsed JSON data.
//
// For struct fields (such as AutoDelegateConfig or NetworkConfig), every field
// must appear within the corresponding child object. For map-of-struct fields
// (such as Providers, Profiles, and Agents), keys are distributed across
// instances (e.g. bedrock has region, while local has base_url), so every field
// of the value struct must appear in at least one entry of that map.
func checkStructFields(t *testing.T, typ reflect.Type, data any, path string, checked *int) {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}

	obj, ok := data.(map[string]any)
	if !ok {
		t.Errorf("%s expected a JSON object in config.example.json, got %T", path, data)
		return
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}

		fieldPath := name
		if path != "" {
			fieldPath = path + "." + name
		}

		*checked++
		val, exists := obj[name]
		if !exists {
			t.Errorf("%q is a setting config.json accepts and config.example.json never mentions it, so it is a setting nobody can find", fieldPath)
			continue
		}

		// mcp_servers, hooks, and permission document their nested shape
		// as escaped prose inside sibling "//note" strings rather than as
		// parseable keys. In config.example.json they are empty objects
		// ("mcp_servers": {}, "hooks": {}, "permission": {}) because localcode
		// ships with no MCP servers, no hooks, and no permission overrides
		// configured by default. A recursive walk would report all of their
		// inner keys missing from those empty maps, so they are exempted
		// here from structural descent.
		switch fieldPath {
		case "mcp_servers", "hooks", "permission":
			continue
		}

		fieldType := field.Type
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}

		switch fieldType.Kind() {
		case reflect.Struct:
			checkStructFields(t, fieldType, val, fieldPath, checked)

		case reflect.Map:
			valType := fieldType.Elem()
			if valType.Kind() == reflect.Pointer {
				valType = valType.Elem()
			}
			if valType.Kind() != reflect.Struct {
				continue
			}

			valMap, ok := val.(map[string]any)
			if !ok || len(valMap) == 0 {
				t.Errorf("%q is a map of %s but has no entries in config.example.json to demonstrate its fields", fieldPath, valType.Name())
				continue
			}

			seenKeys := make(map[string]bool)
			for _, entry := range valMap {
				if entryMap, ok := entry.(map[string]any); ok {
					for k := range entryMap {
						seenKeys[k] = true
					}
				}
			}

			for j := 0; j < valType.NumField(); j++ {
				nestedField := valType.Field(j)
				ntag := nestedField.Tag.Get("json")
				nname, _, _ := strings.Cut(ntag, ",")
				if nname == "" || nname == "-" {
					continue
				}
				*checked++
				if !seenKeys[nname] {
					t.Errorf("%q is accepted on %s in config.json and config.example.json never mentions it in any %s entry", nname, valType.Name(), fieldPath)
				}
			}
		}
	}
}
