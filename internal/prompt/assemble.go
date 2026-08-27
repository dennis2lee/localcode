package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Block is one rendered asset on its way to a provider, with the facts
// about it that the adapter must not throw away.
//
// The adapter's job is to lower these into whatever the provider's wire
// format is, and the fields beyond Text are what make that lowering
// checkable: a provider that has no separate system field and has to fold
// the system blocks into the first message can say so in the manifest,
// rather than the distinction quietly disappearing at the boundary.
type Block struct {
	AssetID    string
	Kind       Kind
	Provenance Provenance
	Trust      Trust
	Cache      CacheClass
	Text       string
}

// Envelope is one assembled request, provider-neutral.
//
// System, Messages and ToolNotes are separate because they are separate
// in every provider that has them, and folding them into one string is a
// lowering decision that belongs to the adapter and belongs in the
// manifest when it happens. The old design made that decision once, in
// the loop, for every provider at once.
type Envelope struct {
	System    []Block
	Messages  []Block
	ToolNotes []Block
	Manifest  Manifest
}

// SystemText renders the system blocks into the single string a provider
// with one system field takes.
//
// This is the compatibility lowering, kept in one place and named for
// what it is, so the manifest can record that it happened. Callers that
// can do better than one string should not use it.
func (e Envelope) SystemText() string {
	parts := make([]string, 0, len(e.System))
	for _, b := range e.System {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Manifest is the record of what one assembly decided and why.
//
// It holds identities, hashes and sizes, never bodies. That is not an
// oversight: a manifest is written into the turn log and shown in
// diagnostics, and the assets it describes include the project's own
// instructions and whatever a hook chose to inject. Storing the digest
// keeps "did this change between the two requests" answerable without
// copying that content anywhere it was not already.
type Manifest struct {
	// ID identifies this assembly, so a trace record and a diagnostic
	// can refer to the same one. Derived from the content and from the
	// activation facts that change what was sent, so two identical
	// assemblies get the same ID and a changed one does not.
	ID string `json:"id"`

	// At is when this assembly was recorded, for retention and for
	// reading a stored manifest back beside its trace line.
	At time.Time `json:"at,omitempty"`

	// Activation is the description of the call this was built for.
	Agent         string `json:"agent,omitempty"`
	Role          Role   `json:"role,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Model         string `json:"model,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Family        string `json:"family,omitempty"`
	FallbackIndex int    `json:"fallback_index,omitempty"`
	// UtilityAttempt is which try of a utility operation this was. See
	// ActivationContext.UtilityAttempt: a compaction retry is not a
	// fallback position, and conflating them made a diagnostic say the
	// session had moved down a chain it never touched.
	UtilityAttempt int       `json:"utility_attempt,omitempty"`
	SmartAgent     bool      `json:"smart_agent,omitempty"`
	Lifecycle      Lifecycle `json:"lifecycle,omitempty"`

	Selected []Entry     `json:"selected,omitempty"`
	Excluded []Exclusion `json:"excluded,omitempty"`

	// Lowering records compatibility decisions an adapter made, such as
	// folding system blocks into one system string.
	Lowering []string `json:"lowering,omitempty"`

	// Warnings are assembly problems that did not stop the request:
	// a cache class out of order, a duplicate that was dropped.
	Warnings []string `json:"warnings,omitempty"`

	// TotalTokens is the estimate over everything selected.
	TotalTokens int `json:"total_tokens,omitempty"`
}

// Entry is one asset that made it into the request.
type Entry struct {
	ID         string     `json:"id"`
	Kind       Kind       `json:"kind,omitempty"`
	Provenance Provenance `json:"provenance,omitempty"`
	Trust      Trust      `json:"trust,omitempty"`
	Placement  Placement  `json:"placement,omitempty"`
	Cache      CacheClass `json:"cache,omitempty"`
	Order      int        `json:"order,omitempty"`
	Version    string     `json:"version,omitempty"`
	// Reason is why it was included, from the asset's own Active.
	Reason string `json:"reason,omitempty"`
	// Hash is of the rendered text; Tokens is the estimate.
	Hash   string `json:"hash,omitempty"`
	Tokens int    `json:"tokens,omitempty"`
}

// Exclusion is one asset that did not, and why not. This half is the
// point: "the project's rules were not in that request" is only useful
// with the reason attached.
type Exclusion struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

// Assemble selects the assets that apply to actx, renders them, and
// returns the request along with the record of how it was built.
//
// The order of operations is the contract. Selection happens first and
// consults only the activation context, so what is in the request is
// explainable without looking at any text. Rendering happens second, so
// an asset cannot influence whether it is selected by what it says.
// Deduplication happens third, on the rendered text, because two assets
// that arrive at the same words are a repetition the model pays for
// twice, and the one that catches it is the one that has both.
func Assemble(r *Registry, actx ActivationContext) Envelope {
	env := Envelope{Manifest: Manifest{
		Agent: actx.Agent, Role: actx.Role, Profile: actx.Profile,
		Model: actx.Model, Provider: actx.Provider, Family: actx.Family,
		FallbackIndex: actx.FallbackIndex, UtilityAttempt: actx.UtilityAttempt,
		SmartAgent: actx.SmartAgent, Lifecycle: actx.Lifecycle,
	}}

	// 1. Select. Every registered asset is asked, so the ones left out
	//    are recorded with a reason rather than being invisible.
	type chosen struct {
		asset  Asset
		reason string
	}
	var picked []chosen
	for _, a := range r.All() {
		ok, reason := a.Active(actx)
		if reason == "" {
			// An asset that does not say why is a hole in the record,
			// and the assembly is where it shows up rather than in a
			// diagnostic three weeks later.
			reason = "no reason given"
			env.Manifest.Warnings = append(env.Manifest.Warnings,
				a.ID+": Active returned no reason")
		}
		if !ok {
			env.Manifest.Excluded = append(env.Manifest.Excluded, Exclusion{ID: a.ID, Reason: reason})
			continue
		}
		picked = append(picked, chosen{asset: a, reason: reason})
	}

	// 2. Render, and drop what turned out to be empty. A source that
	//    exists but has nothing in it is an honest exclusion, not a
	//    blank paragraph in the prompt.
	// 3. Deduplicate on the rendered text, per placement: the same words
	//    twice is the model paying twice for one instruction.
	seen := map[string]string{} // hash -> the ID that got there first
	lastRank := map[Placement]int{}
	for _, c := range picked {
		text := c.asset.Render(actx)
		if strings.TrimSpace(text) == "" {
			env.Manifest.Excluded = append(env.Manifest.Excluded, Exclusion{
				ID: c.asset.ID, Reason: "selected but rendered empty",
			})
			continue
		}
		key := string(c.asset.Placement) + "\x00" + hashOf(text)
		if first, dup := seen[key]; dup {
			env.Manifest.Excluded = append(env.Manifest.Excluded, Exclusion{
				ID: c.asset.ID, Reason: "identical to " + first + ", dropped as a duplicate",
			})
			continue
		}
		seen[key] = c.asset.ID

		// A less stable asset placed before a more stable one
		// invalidates the cached prefix behind it every time it changes.
		// Worth a warning rather than a reordering: the fix is to give
		// the asset the Order its cache class deserves, and silently
		// moving it would hide the mistake from whoever added it.
		if rank := cacheRank(c.asset.Cache); rank < lastRank[c.asset.Placement] {
			env.Manifest.Warnings = append(env.Manifest.Warnings, fmt.Sprintf(
				"%s is %s but comes after a less stable asset, which spoils the cache prefix behind it",
				c.asset.ID, c.asset.Cache))
		} else {
			lastRank[c.asset.Placement] = rank
		}

		block := Block{
			AssetID: c.asset.ID, Kind: c.asset.Kind, Provenance: c.asset.Provenance,
			Trust: c.asset.Trust, Cache: c.asset.Cache, Text: text,
		}
		switch c.asset.Placement {
		case PlaceSystem:
			env.System = append(env.System, block)
		case PlaceMessage:
			env.Messages = append(env.Messages, block)
		case PlaceToolDefinition:
			env.ToolNotes = append(env.ToolNotes, block)
		default:
			// Child and utility placements are not part of this
			// request; they are recorded as selected so a diagnostic can
			// show that they exist and where they went.
		}

		tokens := estimateTokens(text)
		env.Manifest.TotalTokens += tokens
		env.Manifest.Selected = append(env.Manifest.Selected, Entry{
			ID: c.asset.ID, Kind: c.asset.Kind, Provenance: c.asset.Provenance,
			Trust: c.asset.Trust, Placement: c.asset.Placement, Cache: c.asset.Cache,
			Order: c.asset.Order, Version: c.asset.Version, Reason: c.reason,
			Hash: hashOf(text), Tokens: tokens,
		})
	}

	env.Manifest.ID = manifestID(env.Manifest)
	return env
}

// manifestID derives a stable identifier from what the assembly selected.
//
// Content-derived rather than random, so that two requests built the same
// way share an ID and a changed prompt surface announces itself as a
// different one. That is the property a fallback needs: if the manifest
// ID on the second attempt matches the first, the prompt was not
// re-derived for the new model, which is a bug this makes visible.
func manifestID(m Manifest) string {
	// The whole record, minus the two fields that are not facts about
	// the request: the ID itself, and when it happened to be recorded.
	//
	// Hashing an enumerated list of fields was the first version, and
	// the trouble with an enumeration is that it is a second definition
	// of what a manifest is. It said identity, placement and body hash,
	// which left two records with the same bytes and different declared
	// trust sharing an id, so "/context <id>" could answer with the
	// classification the other call had. Widening the list would have
	// fixed that case and left the next one.
	//
	// Hashing the record makes the invariant total instead: the stored
	// manifest is a pure function of its id. That is what makes the
	// disk dedup lossless rather than merely tidy, because two records
	// under one id cannot differ, and it means a field added later is
	// covered on the day it is added rather than the day somebody
	// remembers this function.
	//
	// At is excluded deliberately. An id names an assembly, not a call:
	// the same prompt sent on every turn of a session is one record,
	// and when each call happened is the trace's question, answered by
	// the line that carries this id.
	copy := m
	copy.ID = ""
	copy.At = time.Time{}
	body, err := json.Marshal(copy)
	if err != nil {
		// Unreachable for this struct, and a manifest with no id is
		// simply not recorded, which is better than a panic in a
		// diagnostic.
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:8])
}

// WithLowering records an adapter compatibility decision and recomputes
// the ID, because a lowering changes the request. Appending to Lowering
// without this leaves the ID describing an assembly that was not what
// went out.
func (m Manifest) WithLowering(what string) Manifest {
	out := m
	out.Lowering = append(append([]string{}, m.Lowering...), what)
	out.ID = manifestID(out)
	return out
}

// Explain answers "why was this asset present, absent, or dropped" for
// one ID, which is the question a diagnostic exists to serve.
func (m Manifest) Explain(id string) (string, bool) {
	for _, e := range m.Selected {
		if e.ID == id {
			return fmt.Sprintf("included in %s (%s, %s, %s): %s",
				e.Placement, e.Kind, e.Provenance, e.Trust, e.Reason), true
		}
	}
	for _, x := range m.Excluded {
		if x.ID == id {
			return "excluded: " + x.Reason, true
		}
	}
	return "", false
}

// SelectedIDs lists what made it in, in order. The compact form a trace
// record carries.
func (m Manifest) SelectedIDs() []string {
	out := make([]string, len(m.Selected))
	for i, e := range m.Selected {
		out[i] = e.ID
	}
	return out
}

// UntrustedIDs lists the selected assets carrying external content. A
// turn where this is non-empty is a turn whose request contains text
// nobody in the conversation wrote, which is worth being able to ask
// about directly.
func (m Manifest) UntrustedIDs() []string {
	var out []string
	for _, e := range m.Selected {
		if !e.Trust.Instruction() {
			out = append(out, e.ID)
		}
	}
	return out
}

// RuntimeEntry describes text that joins a request after assembly, from
// a source that only exists at request time. Hook-injected context is
// the canonical case: a pre_model hook runs per request, so its text
// cannot be a registered asset, and leaving it off the manifest made the
// one part of the prompt no inventory covered also the one part nobody
// could see. The entry carries the same facts a registered asset's entry
// does, hash and size included, and never the text.
func RuntimeEntry(id string, kind Kind, prov Provenance, trust Trust, place Placement, text, reason string) Entry {
	return Entry{
		ID: id, Kind: kind, Provenance: prov, Trust: trust, Placement: place,
		Cache: CacheOneShot, Reason: reason, Hash: hashOf(text), Tokens: estimateTokens(text),
	}
}

// WithRuntimeEntries returns a copy of m with entries recorded as
// selected and the ID recomputed, leaving m untouched — the base
// manifest belongs to the run and is shared across a turn's iterations,
// while runtime additions belong to one request.
//
// The recomputed ID is the point: two requests that differ only in what
// a hook injected are different requests, and a trace where they shared
// an ID would say the prompt was identical when it was not.
func (m Manifest) WithRuntimeEntries(entries ...Entry) Manifest {
	if len(entries) == 0 {
		return m
	}
	out := m
	out.Selected = append(append([]Entry{}, m.Selected...), entries...)
	for _, e := range entries {
		out.TotalTokens += e.Tokens
	}
	out.ID = manifestID(out)
	return out
}
