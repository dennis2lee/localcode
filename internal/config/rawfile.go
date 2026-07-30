package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// updateRawConfig rewrites path by parsing it as a raw JSON object, letting
// mutate change exactly the keys it cares about, and writing the result back
// atomically. Unknown keys survive untouched — every writer in this package
// is built on this primitive specifically so a field a newer (or older)
// version added isn't silently dropped by a full Config-struct round-trip.
//
// The write is temp-file + rename so a crash or full disk can never leave a
// truncated config.json, and the file is created 0o600 because it can hold
// provider API keys and MCP auth headers.
func updateRawConfig(path string, mutate func(raw map[string]json.RawMessage) error) error {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read config %s: %w", path, err)
	}

	if err := mutate(raw); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	out = append(out, '\n')

	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	return os.Rename(tmpName, path)
}

// updateRawSection rewrites only the named top-level key of the config at
// path, decoding it into a map[string]json.RawMessage (an empty map if the
// key was absent), letting update mutate that block, then re-encoding it
// back under the same key. This is the shape every "surgical" writer in this
// package needs — object in, mutate, object out — without each one hand
// re-implementing the marshal/unmarshal boilerplate.
func updateRawSection(path, key string, update func(block map[string]json.RawMessage) error) error {
	return updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		block := map[string]json.RawMessage{}
		if rawBlock, ok := raw[key]; ok {
			if err := json.Unmarshal(rawBlock, &block); err != nil {
				return fmt.Errorf("parse %s in %s: %w", key, path, err)
			}
		}
		if err := update(block); err != nil {
			return err
		}
		encoded, err := json.Marshal(block)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", key, err)
		}
		raw[key] = encoded
		return nil
	})
}
