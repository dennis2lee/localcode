package config

import (
	"encoding/json"
	"testing"
)

// mouseEnabled reports the switch the TUI reads: on only when the key
// says true, so an absent key leaves the mouse with the terminal.
func mouseEnabled(c *Config) bool {
	return c.Mouse != nil && *c.Mouse
}

// The TUI's scrollbar is opt-in: a plain drag stops making a native text
// selection while it is on, so an absent key must leave the mouse with
// the terminal.
func TestMouseDefaultsToOffWhenTheKeyIsAbsent(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{}`), &cfg); err != nil {
		t.Fatalf("empty config does not parse: %v", err)
	}
	if mouseEnabled(&cfg) {
		t.Error("Mouse is on with no key in config.json: the terminal keeps the mouse unless the user opts in")
	}
}

func TestMouseTurnsOnWhenConfigSaysSo(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"mouse": true}`), &cfg); err != nil {
		t.Fatalf(`{"mouse": true} does not parse: %v`, err)
	}
	if !mouseEnabled(&cfg) {
		t.Error(`Mouse is off with "mouse": true in config.json: the switch does nothing`)
	}
}

func TestMouseExplicitFalseStaysOff(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"mouse": false}`), &cfg); err != nil {
		t.Fatalf(`{"mouse": false} does not parse: %v`, err)
	}
	if mouseEnabled(&cfg) {
		t.Error(`Mouse is on with "mouse": false in config.json`)
	}
}

// A project config must win in both directions: opting out where the
// user is working matters as much as opting in at home.
func TestMouseMergesFromProjectScope(t *testing.T) {
	on, off := true, false
	global := &Config{Mouse: &on}
	global.merge(&Config{})
	if !mouseEnabled(global) {
		t.Error("merging an empty project config turned the global opt-in off")
	}
	global.merge(&Config{Mouse: &off})
	if mouseEnabled(global) {
		t.Error("a project config saying false did not turn the global opt-in off")
	}
	project := &Config{}
	project.merge(&Config{Mouse: &on})
	if !mouseEnabled(project) {
		t.Error("a project config saying true did not turn the switch on")
	}
}
