package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// This file is the package's one "surgical config.json writer" primitive
// (updateRawConfig/updateRawSection) plus every public function built on
// top of it. Each of these rewrites exactly one top-level key (or one field
// inside one top-level key) and leaves everything else in the file — keys
// this build doesn't even know about included — byte-for-byte alone.

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

// UpdateMCPServersInFile rewrites only the "mcp_servers" key of the JSON
// config at path, leaving every other top-level key intact — including
// keys this version of localcode doesn't know about, which a full
// Config-struct round-trip would silently drop. Used by `localcode mcp
// add/remove`. A missing file starts from an empty object; update receives
// the current entries (never nil) and mutates them in place. update may
// return an error (e.g. "server not found") to abort the whole operation —
// nothing is written in that case.
func UpdateMCPServersInFile(path string, update func(servers map[string]MCPServerConfig) error) error {
	return updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		servers := map[string]MCPServerConfig{}
		if rawServers, ok := raw["mcp_servers"]; ok {
			if err := json.Unmarshal(rawServers, &servers); err != nil {
				return fmt.Errorf("parse mcp_servers in %s: %w", path, err)
			}
		}

		if err := update(servers); err != nil {
			return err
		}

		if len(servers) == 0 {
			delete(raw, "mcp_servers")
			return nil
		}
		encoded, err := json.Marshal(servers)
		if err != nil {
			return fmt.Errorf("marshal mcp_servers: %w", err)
		}
		raw["mcp_servers"] = encoded
		return nil
	})
}

// AddPermissionRuleToFile appends one rule to path's "permission" map for
// toolName, creating the file and the map as needed. It rewrites only the
// "permission" key and leaves every other key in the file byte-for-byte
// alone, so a field this build doesn't know about (a typo, a newer
// version's setting) isn't silently dropped when the user picks "always
// allow".
//
// The rule is appended rather than inserted because ToolPermission
// resolves with last-match-wins: a later rule is what overrides an earlier
// broader one.
func AddPermissionRuleToFile(path, toolName string, rule PermissionRule) error {
	return updateRawSection(path, "permission", func(block map[string]json.RawMessage) error {
		perms, err := decodePermissions(block)
		if err != nil {
			return fmt.Errorf("parse permission in %s: %w", path, err)
		}
		addRule(perms, toolName, rule)
		return encodePermissions(block, perms)
	})
}

// RemovePermissionRuleFromFile removes one rule from path's "permission"
// map for toolName, matched by exact (Match, Decision). It leaves every
// other key untouched, the same surgical approach as AddPermissionRuleToFile.
// Removing the last rule for a tool drops that tool's key entirely rather
// than leaving an empty array behind.
func RemovePermissionRuleFromFile(path, toolName string, rule PermissionRule) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil // no file, nothing to remove
	}
	return updateRawSection(path, "permission", func(block map[string]json.RawMessage) error {
		perms, err := decodePermissions(block)
		if err != nil {
			return fmt.Errorf("parse permission in %s: %w", path, err)
		}
		removeRule(perms, toolName, rule)
		return encodePermissions(block, perms)
	})
}

// SetSkipPermissionsInFile writes the top-level "skip_permissions" key,
// leaving every other key untouched. See Config.SkipPermissions.
func SetSkipPermissionsInFile(path string, enabled bool) error {
	return updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		encoded, err := json.Marshal(enabled)
		if err != nil {
			return fmt.Errorf("marshal skip_permissions: %w", err)
		}
		raw["skip_permissions"] = encoded
		return nil
	})
}

// SetAutoDelegateEnabledInFile flips only the "enabled" field inside the
// top-level "auto_delegate" block at path, leaving the rest of that block
// (agent, match) and every other key in the file untouched.
//
// Enabling when no auto_delegate block exists yet writes one with just
// {"enabled": true} and no agent. That config is inert (Validate rejects an
// agent-less block, and MatchesPrompt with no patterns delegates nothing),
// which is why callers are expected to tell the user that a block still has
// to be filled in — writing a guessed agent name here would be worse.
func SetAutoDelegateEnabledInFile(path string, enabled bool) error {
	return updateAutoDelegateInFile(path, func(block map[string]json.RawMessage) error {
		encoded, err := json.Marshal(enabled)
		if err != nil {
			return fmt.Errorf("marshal auto_delegate.enabled: %w", err)
		}
		block["enabled"] = encoded
		return nil
	})
}

// SetAutoDelegateTargetInFile writes which agent handles delegated prompts
// and which prompts qualify, leaving "enabled" — and any key this build
// doesn't know about — exactly as it was. The counterpart to
// SetAutoDelegateEnabledInFile, which changes only the switch.
//
// An empty match list is written as an empty array rather than omitted: it
// means "delegate nothing", which is a real choice someone can make from a
// settings panel, and dropping the key would instead read as "unset".
func SetAutoDelegateTargetInFile(path, agent string, match []string) error {
	return updateAutoDelegateInFile(path, func(block map[string]json.RawMessage) error {
		encodedAgent, err := json.Marshal(agent)
		if err != nil {
			return fmt.Errorf("marshal auto_delegate.agent: %w", err)
		}
		block["agent"] = encodedAgent

		if match == nil {
			match = []string{}
		}
		encodedMatch, err := json.Marshal(match)
		if err != nil {
			return fmt.Errorf("marshal auto_delegate.match: %w", err)
		}
		block["match"] = encodedMatch
		return nil
	})
}

// updateAutoDelegateInFile rewrites only the named keys inside the top-level
// "auto_delegate" object, leaving the rest of that block and every other key
// in the file untouched.
func updateAutoDelegateInFile(path string, update func(block map[string]json.RawMessage) error) error {
	return updateRawSection(path, "auto_delegate", update)
}

// SetDictationInFile writes the speech settings a settings panel can
// change, leaving every other key in the "dictation" block — and in the
// file — exactly as it was.
//
// Written even when empty, rather than omitted. "" is a real answer for
// both of these and not the same as "unset": an empty language means
// auto-detect, and an empty URL means run the engine locally. Dropping
// the key would leave the previous value in the file and the panel would
// appear not to have saved.
func SetDictationInFile(path, language, whisperURL, whisperAPI string) error {
	return updateRawSection(path, "dictation", func(block map[string]json.RawMessage) error {
		for key, value := range map[string]string{
			"language":    language,
			"whisper_url": whisperURL,
			"whisper_api": whisperAPI,
		} {
			encoded, err := json.Marshal(value)
			if err != nil {
				return fmt.Errorf("marshal dictation.%s: %w", key, err)
			}
			block[key] = encoded
		}
		return nil
	})
}
