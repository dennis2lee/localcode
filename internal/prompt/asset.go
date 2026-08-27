// Package prompt is the inventory of everything localcode says to a model
// before the conversation starts, and the machinery that decides which of
// it applies to one particular request.
//
// It exists because the alternative had stopped scaling. A prompt used to
// be built by concatenating six strings in buildRun: the base prompt, the
// workspace's rules, the agent's own instructions, an orchestration
// policy, a trust boundary, a per-model note. That works until somebody
// asks a question about it. Which of those was in the request that just
// went out? Why was the orchestration policy there for a specialist? Did
// the fallback to another family re-derive the per-model note or reuse
// the one written for the model that failed? Is the project's AGENTS.md
// being treated as instructions or as data? Every one of those questions
// has an answer, and none of them could be read off a string.
//
// So the text is not the unit here. An Asset is: a piece of prompt with
// an identity that survives refactoring, a declared source, a declared
// trust class, a declared place in the request, and a condition saying
// when it applies. Assembling a request selects assets against an
// ActivationContext and records what it did in a Manifest, so the
// questions above are answered by reading one record rather than by
// re-deriving the concatenation by hand.
//
// Two things this deliberately is not. It is not a security boundary:
// labelling a source as external is a statement to the model, and the
// permission layer remains the thing that actually stops a tool call. And
// it is not a template engine: rendering happens after selection and is
// the last step, so an asset's identity and trust cannot be lost inside
// string interpolation.
package prompt

import (
	"crypto/sha256"
	"encoding/hex"
)

// Kind is what a piece of prompt is, which is not the same question as
// where it came from. A safety policy written by the product and a safety
// policy written by the user are the same kind and different provenance;
// keeping the two axes apart is what lets a diagnostic answer "what kind
// of instruction is this" and "who is allowed to have written it"
// separately.
type Kind string

const (
	// KindBaseSystem is the product's own description of what localcode
	// is and how it works. Exactly one is expected per request.
	KindBaseSystem Kind = "base_system"
	// KindSafetyPolicy is a rule about what must not happen, stated to
	// the model. The trust boundary is one.
	KindSafetyPolicy Kind = "safety_policy"
	// KindModeInstruction turns on a way of working: the orchestration
	// policy, a per-model note about how to write for this window.
	KindModeInstruction Kind = "mode_instruction"
	// KindToolDescription is what a tool says about itself. It reaches
	// the provider as part of a tool definition rather than as prose,
	// which is why placement is a separate field.
	KindToolDescription Kind = "tool_description"
	// KindRuntimeReminder is a fact about right now: the working
	// directory, the date, what is already running.
	KindRuntimeReminder Kind = "runtime_reminder"
	// KindAgentPrompt is a role's own instructions, from config or from
	// the built-in specialist roster.
	KindAgentPrompt Kind = "agent_prompt"
	// KindUtilityPrompt belongs to a call that is not the conversation:
	// compaction, titling, memory.
	KindUtilityPrompt Kind = "utility_prompt"
	// KindProjectInstruction is the workspace speaking: AGENTS.md,
	// CLAUDE.md, the rules a repository ships for whoever works in it.
	KindProjectInstruction Kind = "project_instruction"
	// KindExternalContent is data that came from outside and must never
	// be read as instruction: MCP output, a fetched page, a tool result.
	KindExternalContent Kind = "external_content"
	// KindSkill is a packaged procedure the model may follow.
	KindSkill Kind = "skill"
)

// Provenance is who wrote it. The distinction that matters most is the
// one between text the product controls and text that arrived from
// somewhere else, because everything downstream — trust, placement,
// whether a diagnostic may print the body — follows from it.
type Provenance string

const (
	// FromProduct is localcode's own text, compiled into the binary.
	FromProduct Provenance = "product"
	// FromUser is the person using it: config.json, a slash command.
	FromUser Provenance = "user"
	// FromOrganization is a policy applied above the individual.
	FromOrganization Provenance = "organization"
	// FromWorkspace is the project on disk: AGENTS.md and its imports.
	FromWorkspace Provenance = "workspace"
	// FromPlugin is a third-party extension.
	FromPlugin Provenance = "plugin"
	// FromSkill is a skill's own text.
	FromSkill Provenance = "skill"
	// FromHook is text a hook printed for the model to read.
	FromHook Provenance = "hook"
	// FromMCPServer is another process, possibly another machine.
	FromMCPServer Provenance = "mcp_server"
	// FromToolResult is the output of a tool this turn ran.
	FromToolResult Provenance = "tool_result"
	// FromChildResult is what a sub-agent handed back.
	FromChildResult Provenance = "child_result"
	// FromParentAgent is text an orchestrating model wrote as the task
	// for a sub-agent. Neither the product's nor the person's: the
	// person asked for an outcome and the parent model decided what to
	// say to get it, and recording it as either of the other two would
	// name an author who did not write it.
	FromParentAgent Provenance = "parent_agent"
	// FromGeneratedSummary is text a model wrote about earlier text,
	// which is worth distinguishing because a summary of untrusted
	// content is still untrusted.
	FromGeneratedSummary Provenance = "generated_summary"
)

// Trust is what authority the content carries, and it is deliberately a
// field of its own rather than something inferred from Provenance or from
// the message role it ends up in.
//
// Inferring it was the bug this replaces. A tool result placed in a user
// message is not the user speaking; a summary of a web page is not the
// product speaking; an MCP server's tool description sits in the system
// block and is still written by a stranger. Role and position are about
// where text goes, and trust is about whether it may tell the model what
// to do, so they have to be recorded separately or the second question
// gets answered by accident.
type Trust string

const (
	// TrustSystem is the product's own instruction. The highest, and the
	// only one that may be assumed rather than checked.
	TrustSystem Trust = "system"
	// TrustUser is the person's instruction, directly given.
	TrustUser Trust = "user"
	// TrustProject is the workspace's instruction: authored by whoever
	// controls the repository, which is usually but not always the
	// person running localcode. Instruction, but attributable.
	TrustProject Trust = "project"
	// TrustGenerated is text a model wrote: an auto-memory note, a
	// summary, anything a previous turn produced and a later turn reads
	// back. It may be recalled and reasoned about and it may not be
	// followed as an instruction.
	//
	// The reason is the laundering path, and it is short: a tool result,
	// a fetched page or a hostile repository can influence what the
	// model chooses to save, and the next process start loads that text
	// into a system block. If generated text could instruct, that path
	// turns any untrusted content into a standing instruction with one
	// restart in between. Distinct from TrustExternal because the
	// provenance genuinely differs, and identical to it in the one
	// respect that matters.
	TrustGenerated Trust = "generated"
	// TrustDelegated is the task a parent agent handed to a sub-agent.
	//
	// It instructs, and it has to: it is the entire reason the child
	// context exists, and a child that treated its own task as data
	// would do nothing. What it must not do is claim to be the person's
	// words or the product's, because it is neither, and because the
	// difference is exactly what a reader of a child's transcript needs
	// in order to ask where an instruction came from.
	//
	// It is instruction-authoritative only where it belongs, which is
	// the child's own request. It is never selected into the parent's,
	// where the text does not appear at all.
	//
	// The exposure this carries is stated rather than implied: a parent
	// that has read a hostile tool result can put that influence into a
	// task, and this class does not stop that. What stops it is the
	// child's own tool gating and the permission gate, which are
	// runtime controls; a trust label is a declaration, and declaring
	// this one honestly is what it can do.
	TrustDelegated Trust = "delegated"
	// TrustExternal is data. It may be read, quoted and reasoned about,
	// and it may never be followed as an instruction, however it is
	// phrased and whatever it claims to be.
	TrustExternal Trust = "external"
)

// Instruction reports whether content at this trust class may be followed
// as an instruction at all.
//
// Four classes may: the product's own text, the person's, the
// workspace's, and a task an orchestrator handed to the sub-agent that
// is running. Everything else may not, and that includes a class this
// function has never heard of.
//
// Failing closed on the unknown is the point. The first version of this
// asked "is it external?" and returned true for everything else, so
// TrustGenerated was instruction-authoritative the moment it was
// declared and before anyone thought about it. An allowlist cannot make
// that mistake: a class added later is data until someone comes here and
// decides otherwise, which is the decision that should be deliberate.
func (t Trust) Instruction() bool {
	switch t {
	case TrustSystem, TrustUser, TrustProject, TrustDelegated:
		return true
	}
	return false
}

// Placement is where an asset belongs in the request. It is a field
// because the old design's answer was always "the system string", and
// that is wrong for most of the inventory: a tool's description belongs
// in a tool definition where the provider can use it natively, a runtime
// reminder belongs next to the turn it describes, a compaction prompt
// belongs to a different call entirely.
type Placement string

const (
	// PlaceSystem is the provider's system block.
	PlaceSystem Placement = "system"
	// PlaceMessage is an ordinary message in the conversation.
	PlaceMessage Placement = "message"
	// PlaceToolDefinition travels with a tool's schema.
	PlaceToolDefinition Placement = "tool_definition"
	// PlaceChildContext is handed to a sub-agent rather than used here.
	PlaceChildContext Placement = "child_context"
	// PlaceUtilityCall belongs to compaction, titling or memory, which
	// are their own calls with their own contracts.
	PlaceUtilityCall Placement = "utility_call"
)

// CacheClass is how long a rendering stays valid, which is what decides
// whether it may sit inside a cached prefix.
//
// The rule it encodes: anything that changes per turn must come after
// everything that does not, or it invalidates the prefix behind it and
// the cache stops paying for itself. Recording the class per asset is
// what lets the assembler check that rather than trusting the author of
// each new asset to have thought about it.
type CacheClass string

const (
	// CacheStable is identical for every request of this shape: the base
	// prompt, the tool descriptions, the safety policies.
	CacheStable CacheClass = "stable"
	// CacheSessionDynamic is fixed for a session but not across
	// sessions: the workspace's rules, the agent's own prompt.
	CacheSessionDynamic CacheClass = "session_dynamic"
	// CacheTurnDynamic changes every turn: the time, what is running.
	CacheTurnDynamic CacheClass = "turn_dynamic"
	// CacheOneShot is used once and not repeated.
	CacheOneShot CacheClass = "one_shot"
	// CacheDurableReload survives a restart and is re-read rather than
	// remembered.
	CacheDurableReload CacheClass = "durable_reload"
)

// cacheRank orders the classes from most to least stable, so the
// assembler can check that a request never puts a less stable asset
// before a more stable one inside the same placement.
func cacheRank(c CacheClass) int {
	switch c {
	case CacheStable:
		return 0
	case CacheDurableReload:
		return 1
	case CacheSessionDynamic:
		return 2
	case CacheTurnDynamic:
		return 3
	case CacheOneShot:
		return 4
	}
	return 5
}

// Asset is one piece of the prompt surface, declared rather than
// concatenated.
//
// The two functions are deliberately separate. Active answers "does this
// belong in this request", and is consulted for every registered asset so
// that the ones left out can be recorded with a reason. Render answers
// "what does it say", and is called only for the ones that got in, so an
// asset that is expensive to render costs nothing when it does not apply
// and so that rendering can never influence selection.
type Asset struct {
	// ID is stable across refactors and is what a manifest, a trace
	// record and a test all refer to. Renaming the Go symbol that holds
	// an asset must not change it.
	ID string

	Kind       Kind
	Provenance Provenance
	Trust      Trust
	Placement  Placement
	Cache      CacheClass

	// Order is precedence within a placement. Lower comes first. Assets
	// that share an order are broken apart by ID, so equal inputs always
	// produce equal output.
	Order int

	// Version marks a meaningful change to what this asset says, for
	// drift detection across releases. Free-form; compared, not parsed.
	Version string

	// Active reports whether the asset applies, and returns the reason
	// either way. The reason is not decoration: it is what a diagnostic
	// shows when somebody asks why their project's rules were not in the
	// request, and an empty one makes the manifest useless.
	Active func(ActivationContext) (bool, string)

	// Render produces the text, after selection. Returning "" is
	// allowed and drops the asset with that recorded, which is the
	// honest outcome for a source that turned out to be empty.
	Render func(ActivationContext) string
}

// hashOf is the manifest's stand-in for content it must not store.
//
// A manifest travels into trace files and diagnostics, and the bodies it
// describes include the project's own instructions and anything a hook
// chose to inject. Recording a truncated digest keeps drift detection and
// "did this change between the two requests" working without writing the
// content anywhere it was not already written.
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// estimateTokens is the same rough measure used elsewhere in the
// codebase: a character count divided down. It is for budgeting and for
// telling a reader which asset is the large one, not for anything that
// has to agree with a provider's own count.
//
// Four characters per token is right for English prose and runs about
// four times low on Korean and Japanese, so it is a floor rather than an
// estimate for those. Callers that need to be safe treat it as one.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return len([]rune(s))/4 + 1
}
