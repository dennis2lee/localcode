package config

import (
	"encoding/json"
	"testing"
)

// A provider entry merges whole, so a project config that redeclares a
// provider and omits max_concurrent_tasks drops the lane the global config
// gave it.
//
// That is how every provider field already behaves, base_url and api_key
// included; pinned here so the new field cannot make it a surprise, and so
// that anyone who later writes a per-field provider merge has to decide
// this deliberately rather than by accident.
func TestAProjectProviderEntryReplacesTheGlobalOneWholesale(t *testing.T) {
	var global, project Config
	if err := json.Unmarshal([]byte(`{"providers":{"local":{"type":"openai-compat","base_url":"http://a","max_concurrent_tasks":2}}}`), &global); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"providers":{"local":{"type":"openai-compat","base_url":"http://b"}}}`), &project); err != nil {
		t.Fatal(err)
	}
	global.merge(&project)

	got := global.Providers["local"]
	if got.BaseURL != "http://b" {
		t.Errorf("base_url = %q, want the project's", got.BaseURL)
	}
	if got.MaxConcurrentTasks != 0 {
		t.Errorf("max_concurrent_tasks = %d; a redeclared provider entry replaces the global one whole", got.MaxConcurrentTasks)
	}
}
