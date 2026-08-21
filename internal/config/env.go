package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// {env:NAME} in config.json, substituted from the environment as the file
// is read.
//
// The point is a config file that can be committed, copied between
// machines, or pasted into an issue without carrying an API key in it —
// and, past keys, any value that differs per machine: a base_url, a model
// id, the address of a speech server. It is the same spelling opencode
// uses, so a config written for that works here.
//
// Substitution happens on the way in and nowhere else. Every writer in
// this package goes through updateRawConfig, which re-reads the file from
// disk and rewrites one key at a time, so "always allow" or `localcode
// mcp add` cannot turn a placeholder into the secret it stands for. That
// is the property that makes this safe to use for keys at all, and
// TestWritingBackKeepsThePlaceholder is there to keep it true.
//
// Two forms:
//
//	{env:NAME}            required: the variable must be set and non-empty
//	{env:NAME:-fallback}  optional: the fallback is used when it is not
//
// A missing required variable is an error naming the variable and the
// field it was written in, rather than an empty string. An empty api_key
// fails later, somewhere else, as a 401 that says nothing about the
// config file — which is the failure this whole feature exists to avoid.
var envRef = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)(:-[^}]*)?\}`)

// expandEnv substitutes every {env:NAME} in the string values of a JSON
// document, leaving keys, numbers, and structure alone.
//
// It works on the decoded document rather than on the file's bytes: an
// API key containing a quote or a backslash would otherwise be spliced
// into the text and produce a config file that no longer parses, or worse
// one that parses differently.
//
// Numbers are decoded as json.Number so that re-encoding writes them back
// exactly as they were written. Through float64 an integer past 2^53 comes
// back as a different number, silently — no field here holds one today,
// and the guarantee worth having is that this rewrites whatever it was
// given rather than the fields this version happens to know about.
func expandEnv(data []byte, lookup func(string) (string, bool)) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		// Not this function's error to report: the caller parses the same
		// bytes into a Config and says what is wrong with them there.
		return data, nil
	}

	out, err := expandValue(doc, "", lookup)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func expandValue(v any, path string, lookup func(string) (string, bool)) (any, error) {
	switch t := v.(type) {
	case string:
		return expandString(t, path, lookup)
	case map[string]any:
		// Sorted so a file with two bad placeholders reports the same one
		// every time. An error that moves around between runs is one
		// people learn to distrust.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			nv, err := expandValue(t[k], join(path, k), lookup)
			if err != nil {
				return nil, err
			}
			t[k] = nv
		}
		return t, nil
	case []any:
		for i, item := range t {
			nv, err := expandValue(item, fmt.Sprintf("%s[%d]", path, i), lookup)
			if err != nil {
				return nil, err
			}
			t[i] = nv
		}
		return t, nil
	default:
		return v, nil
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func expandString(s, path string, lookup func(string) (string, bool)) (string, error) {
	var bad error
	out := envRef.ReplaceAllStringFunc(s, func(ref string) string {
		m := envRef.FindStringSubmatch(ref)
		name, fallback := m[1], m[2]
		value, ok := lookup(name)
		if ok && value != "" {
			return value
		}
		if fallback != "" {
			return strings.TrimPrefix(fallback, ":-")
		}
		if bad == nil {
			// The variable's name, never its value: this error goes to a
			// terminal, into a log, and into an issue report.
			bad = fmt.Errorf("%s is not set in the environment, and %s asks for it as {env:%s} "+
				"(use {env:%s:-fallback} if it is meant to be optional)", name, fieldName(path), name, name)
		}
		return ref
	})
	if bad != nil {
		return "", bad
	}
	return out, nil
}

// fieldName describes where in the file a placeholder was, since "NAME is
// not set" on its own leaves someone grepping a config they may not have
// written.
func fieldName(path string) string {
	if path == "" {
		return "the config file"
	}
	return path
}

// osLookup is expandEnv's lookup in a real run. A test passes its own
// rather than setting variables on the process, which no parallel test
// can do safely.
func osLookup(name string) (string, bool) { return os.LookupEnv(name) }
