// Package credentials stores secrets `/login` collects (currently just an
// Anthropic API key) outside of config.json, so a project-local
// .localcode/config.json can be safely committed to version control
// without leaking a key. It lives at ~/.localcode/credentials.json,
// written with mode 0600.
package credentials

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Credentials struct {
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
}

func path(home string) string {
	return filepath.Join(home, ".localcode", "credentials.json")
}

// Load reads ~/.localcode/credentials.json. A missing file is not an
// error — it just means nothing has been saved yet.
func Load(home string) (Credentials, error) {
	data, err := os.ReadFile(path(home))
	if err != nil {
		if os.IsNotExist(err) {
			return Credentials{}, nil
		}
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return Credentials{}, fmt.Errorf("parse credentials: %w", err)
	}
	return c, nil
}

// SaveAnthropicAPIKey writes apiKey into ~/.localcode/credentials.json,
// preserving any other fields already saved there.
func SaveAnthropicAPIKey(home, apiKey string) error {
	c, err := Load(home)
	if err != nil {
		return err
	}
	c.AnthropicAPIKey = apiKey

	p := path(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	// Written beside and renamed into place, so a reader never sees half a
	// file: a running daemon reads this on a request that needs the key,
	// and a login in another terminal can be writing it at that moment.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".credentials-*.json")
	if err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write credentials: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}
