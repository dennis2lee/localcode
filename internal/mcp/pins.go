package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Trust-on-first-use pinning for MCP servers.
//
// Configuring a server used to be the whole of the trust decision: once
// an entry was in config.json, whatever the server advertised was what
// the model was told it could do, every run, with no record of what it
// had said the run before. That matters because tool descriptions are
// instructions to the model — a server whose descriptions change between
// runs changes the model's behaviour, silently, without a byte of
// localcode's own configuration moving.
//
// The pin is a fingerprint of the server's advertised surface: every
// tool's name, description and input schema, in a canonical order. The
// first connection records it; every later connection compares against
// it. A change is reported as a startup warning naming the server, and
// the pin is then updated — warn-once rather than refuse, because a
// server's tools also change for the ordinary reason that somebody
// upgraded it, and refusing would turn every legitimate update into an
// outage. The warning is the audit trail the decision was missing: the
// person who upgraded expects it, and the person who did not has been
// told the one fact that should make them look.

// pinEntry is what is remembered about one server between runs.
type pinEntry struct {
	Fingerprint string `json:"fingerprint"`
	FirstSeen   string `json:"first_seen"`
	// LastChanged is set each time the fingerprint moves, so the file
	// itself answers "when did this server last change what it offers".
	LastChanged string `json:"last_changed,omitempty"`
}

type pinFileData struct {
	Servers map[string]pinEntry `json:"servers"`
}

// fingerprintTools reduces a server's advertised surface to one hash.
// Sorted by tool name so the fingerprint is a fact about the surface,
// not about the order the server happened to list it in.
func fingerprintTools(list []*mcpsdk.Tool) string {
	sorted := append([]*mcpsdk.Tool(nil), list...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	for _, t := range sorted {
		h.Write([]byte(t.Name))
		h.Write([]byte{0})
		h.Write([]byte(t.Description))
		h.Write([]byte{0})
		if schema, err := json.Marshal(t.InputSchema); err == nil {
			h.Write(schema)
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkPin compares one server's fingerprint against the pin file at
// path, records the new state, and reports whether the surface changed
// since the last run. A missing file or a server seen for the first time
// is the trust-on-first-use case: recorded, not reported.
//
// Read-modify-write without locking, because one localcode process owns
// its home directory's pin file the same way it owns config.json. A
// failure to read or write is returned so the caller can say the audit
// is not happening, which is different from silently not doing it.
func checkPin(path, server, fingerprint string) (changed bool, err error) {
	var data pinFileData
	raw, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		if uerr := json.Unmarshal(raw, &data); uerr != nil {
			// A corrupt pin file starts trust over rather than killing
			// startup: the pins are an audit record, and an unreadable
			// record's honest replacement is a fresh one.
			data = pinFileData{}
		}
	case !os.IsNotExist(rerr):
		return false, fmt.Errorf("read %s: %w", path, rerr)
	}
	if data.Servers == nil {
		data.Servers = map[string]pinEntry{}
	}

	now := time.Now().Format(time.RFC3339)
	entry, seen := data.Servers[server]
	switch {
	case !seen:
		entry = pinEntry{Fingerprint: fingerprint, FirstSeen: now}
	case entry.Fingerprint != fingerprint:
		changed = true
		entry.Fingerprint = fingerprint
		entry.LastChanged = now
	default:
		return false, nil // unchanged, nothing to write
	}
	data.Servers[server] = entry

	out, merr := json.MarshalIndent(data, "", "  ")
	if merr != nil {
		return changed, merr
	}
	if werr := os.WriteFile(path, append(out, '\n'), 0o600); werr != nil {
		return changed, fmt.Errorf("write %s: %w", path, werr)
	}
	return changed, nil
}
