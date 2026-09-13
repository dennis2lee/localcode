package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// config.example.json drifts in the other direction too. The sibling test
// walks the struct and requires every setting to appear in the example;
// this one walks the example and requires every key there to be a setting
// the struct actually reads. A renamed field caught on one side and missed
// on the other leaves a key in the reference that config.json silently
// ignores, which reads as a setting that does nothing.
//
// The struct is still the source of truth, so the walk resolves each
// object in the file to the struct it is parsed into and checks the keys
// against that struct's tags by reflection, the same way the sibling does.
// A key that belongs to no struct fails here on the day it is written.
func TestEveryKeyInTheExampleIsOneTheStructReads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("config.example.json does not parse: %v", err)
	}

	var checked int
	checkDocKeys(t, reflect.TypeOf(Config{}), doc, "", &checked)

	// A floor, so the test cannot pass vacuously on an empty file. The
	// count covers the top level and every nested block the file
	// demonstrates; adding a block to the example raises it.
	if checked < 60 {
		t.Fatalf("only %d keys checked in config.example.json, so this test is checking almost nothing", checked)
	}
}

// checkDocKeys verifies that every non-comment key in data is a json tag
// on typ, descending into the nested objects the file demonstrates.
//
// Map-of-struct fields (providers, profiles, agents) are checked entry by
// entry against the element struct, because the keys are distributed: one
// provider entry never carries every field, the same union rule the
// sibling test applies from the other side.
func checkDocKeys(t *testing.T, typ reflect.Type, data any, path string, checked *int) {
	t.Helper()
	typ = deref(typ)
	if typ == nil || typ.Kind() != reflect.Struct {
		return
	}

	obj, ok := data.(map[string]any)
	if !ok {
		t.Errorf("%s expected a JSON object in config.example.json, got %T", path, data)
		return
	}

	for key, val := range obj {
		// Every note in the file is a "//name" key beside the key it
		// explains. They are prose, not settings, and no struct reads
		// them.
		if strings.HasPrefix(key, "//") {
			continue
		}
		fieldPath := key
		if path != "" {
			fieldPath = path + "." + key
		}
		*checked++
		field, found := fieldByJSONName(typ, key)
		if !found {
			t.Errorf("%q is in config.example.json and no struct reads it, so it is a setting that does nothing", fieldPath)
			continue
		}

		// mcp_servers, hooks, and permission document their nested shape
		// as escaped prose inside sibling "//note" strings rather than
		// as parseable keys, and ship as empty objects in the file. The
		// sibling test exempts them from descent for the same reason;
		// there is nothing inside to check against.
		if fieldPath == "mcp_servers" || fieldPath == "hooks" || fieldPath == "permission" {
			continue
		}

		child, isMap := childStructOf(field.Type)
		if child == nil {
			continue
		}
		sub, isObj := val.(map[string]any)
		if !isObj {
			continue
		}
		if !isMap {
			checkDocKeys(t, child, sub, fieldPath, checked)
			continue
		}
		for entryName, entry := range sub {
			if strings.HasPrefix(entryName, "//") {
				continue
			}
			*checked++
			checkDocKeys(t, child, entry, fieldPath+"."+entryName, checked)
		}
	}
}

// deref follows one pointer, so *AutoDelegateConfig and AutoDelegateConfig
// answer the same way.
func deref(typ reflect.Type) reflect.Type {
	if typ == nil {
		return nil
	}
	if typ.Kind() == reflect.Pointer {
		return typ.Elem()
	}
	return typ
}

// fieldByJSONName finds the struct field parsed from the named key.
func fieldByJSONName(typ reflect.Type, name string) (reflect.StructField, bool) {
	typ = deref(typ)
	for i := 0; i < typ.NumField(); i++ {
		sf := typ.Field(i)
		tagName, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if tagName == name {
			return sf, true
		}
	}
	return reflect.StructField{}, false
}

// childStructOf resolves what an object value under a field is checked
// against: the struct itself for a struct field (auto_delegate, network,
// egress), the element struct for a map of structs (providers, profiles,
// agents), and nil for everything else (strings, numbers, arrays), which
// have no keys to check. A second return reports the map case, whose
// entries are each a whole object rather than one shared one.
func childStructOf(ft reflect.Type) (child reflect.Type, isMap bool) {
	ft = deref(ft)
	if ft == nil {
		return nil, false
	}
	if ft.Kind() == reflect.Struct {
		return ft, false
	}
	if ft.Kind() == reflect.Map {
		if et := deref(ft.Elem()); et != nil && et.Kind() == reflect.Struct {
			// hooks.Config is a map too, but its entries are not
			// structs with json tags the file demonstrates; the caller
			// exempts hooks before this is ever asked.
			return et, true
		}
	}
	return nil, false
}

// USAGE.md is the reference a setting has to be named in to be found.
// The keys this task covers each reached the build without reaching that
// file's config reference: show_thinking and show_timestamps are
// daemon-wide settings with their own commands, top_p and top_k ride
// three different provider wires, effort's profile form was described
// only for the conversation, model_invocable's switch was described only
// where commands opt in, and the trace size cap's second directory was
// named nowhere in the example's own note.
//
// This names the keys, not the sentences around them. A test keyed on
// the prose would fail on a reword and pass on a paragraph that names
// the key while saying the opposite; the requirement is that each key
// is named in backticks, as a setting, in the reference.
func TestTheDocumentedConfigurationKeysAreNamedInUSAGE(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "USAGE.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, key := range []string{
		"show_thinking",
		"show_timestamps",
		"top_p",
		"top_k",
		"effort",
		"model_invocable",
		"trace_max_total_mb",
	} {
		if !strings.Contains(doc, "`"+key+"`") {
			t.Errorf("docs/USAGE.md never names `%s`, so it is a setting nobody can find", key)
		}
	}
}
