package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Unset is the default, zero is off, and nothing above the cap comes out
// of the accessor: past it the guard is off in all but name, and off has
// its own spelling.
func TestRepeatLimitDefaultsAndBounds(t *testing.T) {
	n := func(v int) *int { return &v }
	for _, tc := range []struct {
		name string
		in   *int
		want int
	}{
		{"unset is off", nil, 0},
		{"off", n(0), 0},
		{"six", n(6), 6},
		{"negative reads as off", n(-3), 0},
		{"capped", n(1000), MaxRepeatLimit},
	} {
		if got := (&Config{RepeatLimitSteps: tc.in}).RepeatLimit(); got != tc.want {
			t.Errorf("%s: RepeatLimit() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Zero is written as zero, not removed: an absent key means the default,
// and off is not the default.
func TestSetRepeatLimitInFileWritesZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"providers":{},"profiles":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetRepeatLimitInFile(path, 0); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["repeat_limit"]) != "0" {
		t.Errorf("repeat_limit = %s, want 0 written explicitly", raw["repeat_limit"])
	}
}
