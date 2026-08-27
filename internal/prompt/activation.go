package prompt

import (
	"sort"
	"strings"
)

// ActivationContext is everything one model call knows about itself, in a
// form an asset's Active function can be a pure function of.
//
// It is a snapshot on purpose. The setting it records most carefully is
// SmartAgent, which is read once when work is admitted and pinned for the
// life of that work: a turn that started with the bundle on keeps it for
// every round, including the rounds after somebody flipped the switch.
// Passing the live setting in here instead would reintroduce exactly the
// split that pinning exists to prevent, where a turn's prompt says one
// thing and its tools say another.
//
// Everything else is here for the same reason: an asset that needs to
// know the model family, the fallback position or which tools are
// actually advertised should read it from the request's own description
// rather than reaching back into the loop, because an Active function
// that can see mutable state is an Active function whose answer cannot be
// reproduced from the manifest.
type ActivationContext struct {
	// SmartAgent is the pinned answer for this unit of work, not the
	// live setting.
	SmartAgent bool

	// Agent is the resolved agent name, Role is what it is doing in the
	// bundle. Role is separate because the same agent name can be the
	// orchestrator in one turn and a specialist in another, and the
	// orchestration policy belongs only to the first.
	Agent string
	Role  Role

	// Profile, Model, Provider describe the endpoint this request is
	// going to. Family is the model family the per-model notes are
	// written against.
	Profile  string
	Model    string
	Provider string
	Family   string

	// FallbackIndex is 0 for the profile the turn started on and
	// increments down the chain. An asset that must be re-derived for
	// the model that caught the overflow reads this.
	FallbackIndex int

	// UtilityAttempt numbers the tries one utility operation has made,
	// starting at zero. A compaction whose request did not fit shrinks
	// its budget and asks the same model again, which is a different
	// event from moving to the next endpoint in a fallback chain.
	//
	// Its own field because it was once FallbackIndex, and a diagnostic
	// reading that field cannot tell a retry from a fallback: a third
	// compaction attempt in a session that never fell back rendered as
	// "fallback position 2". It is part of the manifest identity for
	// the same reason the chain position is: two attempts of one
	// compaction are two different requests, and in the degenerate case
	// where nothing else about them differs, this is what says so.
	UtilityAttempt int

	// Tools are the names actually advertised to the model for this
	// call. A tool description asset whose tool is not here does not
	// belong in the request: the model would be told how to use
	// something it cannot call.
	Tools []string

	// Workspace is the session's directory, WorkspaceClass says what
	// sort of place it is (a project with rules, a bare directory, none
	// at all) for assets that vary on that rather than on the path.
	Workspace      string
	WorkspaceClass WorkspaceClass

	// Lifecycle is where in the turn this call happens: the ordinary
	// conversation, a compaction, a title. Utility calls have their own
	// prompt and must not inherit the conversation's.
	Lifecycle Lifecycle

	// Flags are named feature switches an asset may condition on,
	// without this struct growing a field per experiment.
	Flags map[string]bool

	// Values carries the per-session text an asset renders from: the
	// workspace's rules, the agent's configured prompt. Assets stay pure
	// functions of the context this way, which is what makes the
	// registry a static inventory that can be listed and tested rather
	// than a pile of closures built fresh every turn.
	Values map[string]string
}

// Role is what this call is doing, as distinct from which agent is doing
// it.
type Role string

const (
	// RoleOrchestrator is the session's own model, deciding what to do
	// and what to delegate.
	RoleOrchestrator Role = "orchestrator"
	// RoleSpecialist is a sub-agent doing one scoped piece of work.
	RoleSpecialist Role = "specialist"
	// RoleUtility is a call that is not part of the conversation.
	RoleUtility Role = "utility"
)

// WorkspaceClass describes the kind of place a session is working in, for
// assets that care about the kind rather than the path.
type WorkspaceClass string

const (
	// WorkspaceNone is a session with no directory on its context.
	WorkspaceNone WorkspaceClass = "none"
	// WorkspacePlain is a directory with no project instructions in it.
	WorkspacePlain WorkspaceClass = "plain"
	// WorkspaceProject is a directory that ships rules of its own.
	WorkspaceProject WorkspaceClass = "project"
)

// Lifecycle is which call this is.
type Lifecycle string

const (
	// LifecycleTurn is the ordinary conversation.
	LifecycleTurn Lifecycle = "turn"
	// LifecycleCompaction is the summarizing call.
	LifecycleCompaction Lifecycle = "compaction"
	// LifecycleTitle is the naming call.
	LifecycleTitle Lifecycle = "title"
	// LifecycleMemory is the call that writes the model's own notes.
	LifecycleMemory Lifecycle = "memory"
)

// Flag reports a named feature switch, false when unset, so an asset does
// not have to nil-check the map.
func (a ActivationContext) Flag(name string) bool { return a.Flags[name] }

// Value returns the per-session text registered under name, or "".
func (a ActivationContext) Value(name string) string { return a.Values[name] }

// HasTool reports whether name is advertised to the model for this call.
func (a ActivationContext) HasTool(name string) bool {
	for _, t := range a.Tools {
		if t == name {
			return true
		}
	}
	return false
}

// Registry is the declared inventory of the prompt surface.
//
// It is a list rather than a map because order of registration is not the
// order of assembly — Order and ID decide that — and because being able
// to walk the whole inventory in a stable sequence is what makes it
// answerable: a test can assert that every registered asset has an ID, a
// trust class and a reason for being where it is, which is a property of
// the inventory rather than of any one request.
type Registry struct {
	assets []Asset
	byID   map[string]int
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byID: make(map[string]int)}
}

// Add registers an asset. A duplicate ID replaces the earlier entry,
// which is what a caller overriding a built-in asset means, and is
// reported so a caller doing it by accident can be told.
func (r *Registry) Add(a Asset) (replaced bool) {
	if i, ok := r.byID[a.ID]; ok {
		r.assets[i] = a
		return true
	}
	r.byID[a.ID] = len(r.assets)
	r.assets = append(r.assets, a)
	return false
}

// Remove drops an asset by ID and reports whether it was there. This is
// how a complete prompt replacement removes the presets it is replacing,
// leaving the safety assets that a replacement is not allowed to turn
// off to be re-added by the caller.
func (r *Registry) Remove(id string) bool {
	i, ok := r.byID[id]
	if !ok {
		return false
	}
	r.assets = append(r.assets[:i], r.assets[i+1:]...)
	delete(r.byID, id)
	for j := i; j < len(r.assets); j++ {
		r.byID[r.assets[j].ID] = j
	}
	return true
}

// Get returns a registered asset by ID.
func (r *Registry) Get(id string) (Asset, bool) {
	i, ok := r.byID[id]
	if !ok {
		return Asset{}, false
	}
	return r.assets[i], true
}

// Len is how many assets are registered.
func (r *Registry) Len() int { return len(r.assets) }

// All returns the inventory in assembly order: by placement, then by
// Order, then by ID. Sorting by ID last is what makes the whole thing
// deterministic — two assets that forgot to disagree about Order still
// come out in the same sequence on every machine and every run, rather
// than in whatever order they happened to be registered.
func (r *Registry) All() []Asset {
	out := make([]Asset, len(r.assets))
	copy(out, r.assets)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Placement != out[j].Placement {
			return placementRank(out[i].Placement) < placementRank(out[j].Placement)
		}
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// placementRank fixes the order placements are emitted in, so a walk of
// the whole inventory reads system first and utility last rather than in
// the order the constants happen to be spelled.
func placementRank(p Placement) int {
	switch p {
	case PlaceSystem:
		return 0
	case PlaceToolDefinition:
		return 1
	case PlaceMessage:
		return 2
	case PlaceChildContext:
		return 3
	case PlaceUtilityCall:
		return 4
	}
	return 5
}

// Validate checks the inventory itself, as opposed to any one request
// built from it. It is meant to run in a test over the real registry, so
// that an asset added later cannot quietly skip the fields the manifest
// and the trust model depend on.
func (r *Registry) Validate() []string {
	var problems []string
	seen := map[string]bool{}
	for _, a := range r.assets {
		switch {
		case a.ID == "":
			problems = append(problems, "an asset has no ID")
			continue
		case seen[a.ID]:
			problems = append(problems, a.ID+": registered twice")
		}
		seen[a.ID] = true
		if a.Kind == "" {
			problems = append(problems, a.ID+": no kind")
		}
		if a.Provenance == "" {
			problems = append(problems, a.ID+": no provenance")
		}
		if a.Trust == "" {
			problems = append(problems, a.ID+": no trust class")
		}
		if a.Placement == "" {
			problems = append(problems, a.ID+": no placement")
		}
		if a.Cache == "" {
			problems = append(problems, a.ID+": no cache class")
		}
		if a.Render == nil {
			problems = append(problems, a.ID+": no Render")
		}
		if a.Active == nil {
			problems = append(problems, a.ID+": no Active, so it can never be excluded with a reason")
		}
		// External content that claims instruction authority is the one
		// combination that is always a mistake: it is the exact
		// confusion the trust class exists to prevent.
		if a.Provenance == FromMCPServer || a.Provenance == FromToolResult {
			if a.Trust != TrustExternal {
				problems = append(problems, a.ID+": arrives from outside but is not TrustExternal")
			}
		}
		// Text a model wrote cannot be an instruction, whatever the
		// asset's author believed. The laundering path is one restart
		// long: untrusted content influences what gets saved, and the
		// save is read back as a system block.
		if a.Provenance == FromGeneratedSummary && a.Trust.Instruction() {
			problems = append(problems, a.ID+": is model-generated but claims instruction authority")
		}
	}
	sort.Strings(problems)
	return problems
}

// Inventory renders the registry as one line per asset, for the
// documentation check and for a person asking what the prompt surface
// actually consists of.
func (r *Registry) Inventory() string {
	var b strings.Builder
	for _, a := range r.All() {
		b.WriteString(a.ID)
		b.WriteString("\t")
		b.WriteString(string(a.Kind))
		b.WriteString("\t")
		b.WriteString(string(a.Provenance))
		b.WriteString("\t")
		b.WriteString(string(a.Trust))
		b.WriteString("\t")
		b.WriteString(string(a.Placement))
		b.WriteString("\t")
		b.WriteString(string(a.Cache))
		b.WriteString("\n")
	}
	return b.String()
}
