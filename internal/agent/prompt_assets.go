package agent

import (
	"localcode/internal/prompt"
	"localcode/internal/smart"
)

// This file is localcode's prompt surface, declared.
//
// Every one of these assets used to be a `system = system + "\n\n" + x`
// in buildRun, in an order fixed by the order the lines were written in
// and with the conditions spread across three packages. The text has not
// changed and neither has the order; what changed is that both are now
// facts the program can be asked about. The assembled request records
// which of these were in it and which were not and why, so "was the
// orchestration policy in that specialist's request" stops being a
// question you answer by re-reading buildRun.
//
// Asset IDs are the stable names. They appear in manifests, in the turn
// log, and in tests, so renaming one is a visible change rather than a
// refactor: pick the name for what the asset is, not for where the code
// currently lives.
const (
	// AssetBaseSystem is the product's own description of itself.
	AssetBaseSystem = "system.base"
	// AssetWorkspace is where this conversation is working, stated to the
	// model rather than left to be inferred.
	//
	// It was left to be inferred, and that was a defect with a clear
	// shape. Nothing in the prompt named the directory, so a model learned
	// it from tool output: a pwd, a glob result, the path in an earlier
	// answer. That knowledge then lived in the conversation history, and
	// moving the workspace does not rewrite history. The model went on
	// prefixing every shell command with "cd <the old path> &&" and went
	// on writing to absolute paths under it, while the daemon answered
	// every question about the workspace with the new one. Files appeared
	// in the project the person had just left.
	//
	// Bash is why nothing caught it: the workspace boundary is a check on
	// paths, and a shell command is not a path, so "cd /old && touch x"
	// is outside what that guard can see and always was.
	//
	// Re-derived every turn from the session's own directory, so it cannot
	// go stale the way a remembered path does.
	AssetWorkspace = "session.workspace"
	// AssetProjectRules is the workspace speaking: AGENTS.md, CLAUDE.md
	// and their imports, read from the session's own directory.
	AssetProjectRules = "project.rules"
	// AssetAgentPrompt is the role's own instructions from config.
	AssetAgentPrompt = "agent.prompt"
	// AssetOrchestration is the Smart Agent policy, for the turn that is
	// doing the orchestrating and no other.
	AssetOrchestration = "smart.orchestration"

	// AssetPlanPolicy is what a turn is told about the Orchestrate tool:
	// when a plan is worth its cost, and when Task is the answer instead.
	// Behind its own switch, hence its own asset rather than a paragraph
	// folded into the orchestration policy: the two can be on separately,
	// and a manifest that showed one string for two switches could not say
	// which of them a request carried.
	AssetPlanPolicy = "smart.plan_policy"
	// AssetTrustBoundary names which sources are instructions and which
	// are data, for every agent in the bundle.
	AssetTrustBoundary = "smart.trust_boundary"
	// AssetModelQuirk is the per-family note about how to write for this
	// model, last so it is not buried under the project's rules.
	AssetModelQuirk = "model.quirk"
	// AssetSkillsIndex lists the installed skills, loaded at startup.
	AssetSkillsIndex = "skills.index"
	// AssetMemoryPolicy is the product's description of the auto-memory
	// convention: where notes live and what belongs in them.
	AssetMemoryPolicy = "memory.policy"
	// AssetMemoryIndex is the model's own cross-session notes: text the
	// model wrote for itself, reloaded each start. Generated content,
	// never instruction.
	AssetMemoryIndex = "memory.index"
	// AssetCompactPrompt is the summarizing call's instruction. A
	// utility asset: it belongs to a different call than the
	// conversation, and listing it here is what keeps the inventory the
	// whole prompt surface rather than the conversational half.
	AssetCompactPrompt = "utility.compact"
)

// Value keys are how per-session text reaches an asset's Render without
// the registry having to be rebuilt every turn. Keeping the assets pure
// functions of the activation context is what makes the registry a static
// inventory that a test can walk, rather than a pile of closures over
// whichever Loop happened to build them.
const (
	valBaseSystem   = "base_system"
	valProjectRules = "project_rules"
	valAgentPrompt  = "agent_prompt"
	valOrchestrator = "orchestration"
	valPlanPolicy   = "plan_policy"
	valModelQuirk   = "model_quirk"
	valSkillsIndex  = "skills_index"
	valMemoryPolicy = "memory_policy"
	valMemoryIndex  = "memory_index"
)

// promptRegistry is the inventory, built once. The Order values are the
// order the old concatenation used, and the reason each one has the
// number it has is written beside it: this is the one place where the
// shape of a localcode request is decided, so it is the one place worth
// arguing about it.
//
// Cache classes descend down the list, which is not a coincidence and is
// checked by the assembler: the stable product text first so a provider
// can cache the prefix, the session's own material next, the per-model
// note last because it is the one a fallback changes.
func promptRegistry() *prompt.Registry {
	r := prompt.NewRegistry()

	r.Add(prompt.Asset{
		ID:         AssetBaseSystem,
		Kind:       prompt.KindBaseSystem,
		Provenance: prompt.FromProduct,
		Trust:      prompt.TrustSystem,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheStable,
		Order:      10,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Value(valBaseSystem) == "" {
				return false, "no base system prompt is configured"
			}
			return true, "the product's own prompt, in every request"
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valBaseSystem) },
	})

	// The skills index and the memory index used to be folded into the
	// base prompt at startup, which made three assets look like one.
	// They are separate because the questions about them are separate: a
	// skill index is user-installed procedure, and a memory index is
	// text the model wrote for itself in an earlier session — worth
	// knowing when reading a request, because it is the one part of the
	// system block no person authored.
	r.Add(prompt.Asset{
		ID:         AssetSkillsIndex,
		Kind:       prompt.KindSkill,
		Provenance: prompt.FromSkill,
		Trust:      prompt.TrustUser,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheDurableReload,
		Order:      13,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Value(valSkillsIndex) == "" {
				return false, "no skills are installed"
			}
			return true, "the installed skills, listed so the model can invoke them"
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valSkillsIndex) },
	})
	// The auto-memory convention, and the notes themselves, are two
	// assets because they are two trust classes. They were one string
	// once, and the consequence was that text the model wrote in an
	// earlier session arrived in a system block declared as project
	// instruction: a laundering path one restart long, since a tool
	// result or a hostile repository can influence what gets saved.
	r.Add(prompt.Asset{
		ID:         AssetMemoryPolicy,
		Kind:       prompt.KindModeInstruction,
		Provenance: prompt.FromProduct,
		Trust:      prompt.TrustSystem,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheDurableReload,
		Order:      15,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Value(valMemoryPolicy) == "" {
				return false, "auto memory is off"
			}
			return true, "how to keep notes across sessions"
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valMemoryPolicy) },
	})
	r.Add(prompt.Asset{
		ID:         AssetMemoryIndex,
		Kind:       prompt.KindExternalContent,
		Provenance: prompt.FromGeneratedSummary,
		Trust:      prompt.TrustGenerated,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheDurableReload,
		Order:      16,
		Version:    "2",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Value(valMemoryIndex) == "" {
				return false, "auto memory is off or this project has none"
			}
			return true, "notes the model wrote in earlier sessions, recalled as a record"
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valMemoryIndex) },
	})

	// The project's rules are session-dynamic: fixed for a session,
	// different in the next directory along. They are TrustProject
	// rather than TrustUser because whoever controls the repository
	// wrote them, which is usually but not always the person running
	// localcode, and that difference is exactly what a trust class is
	// for.
	// Before the project's own rules, which are read out of this
	// directory: the place, then what the place says.
	r.Add(prompt.Asset{
		ID:         AssetWorkspace,
		Kind:       prompt.KindModeInstruction,
		Provenance: prompt.FromProduct,
		Trust:      prompt.TrustSystem,
		Placement:  prompt.PlaceSystem,
		// Session-dynamic, not stable: the directory is a property of the
		// conversation and it can move within one. A stable marker here
		// would put a moved workspace behind a cache prefix that says it
		// never moved.
		Cache:   prompt.CacheSessionDynamic,
		Order:   15,
		Version: "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Workspace == "" {
				return false, "this session has no recorded directory"
			}
			return true, "the directory this conversation works in"
		},
		Render: func(a prompt.ActivationContext) string {
			return "You are working in " + a.Workspace + ". Relative paths in your tool calls " +
				"resolve there, and every shell command starts there, so a bare relative path is " +
				"the right way to name a file in this project.\n\n" +
				"This can change during a conversation. If it does, this line changes with it and " +
				"is the only thing that is current: absolute paths and \"cd\" prefixes from earlier " +
				"in this conversation point at wherever the work was then, and following them " +
				"writes to the project you have left. Do not carry a directory forward from an " +
				"earlier message; read it here."
		},
	})

	r.Add(prompt.Asset{
		ID:         AssetProjectRules,
		Kind:       prompt.KindProjectInstruction,
		Provenance: prompt.FromWorkspace,
		Trust:      prompt.TrustProject,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheSessionDynamic,
		Order:      20,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Value(valProjectRules) == "" {
				return false, "this workspace ships no project instructions"
			}
			return true, "the workspace's own rules, from " + string(a.WorkspaceClass)
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valProjectRules) },
	})

	r.Add(prompt.Asset{
		ID:         AssetAgentPrompt,
		Kind:       prompt.KindAgentPrompt,
		Provenance: prompt.FromUser,
		Trust:      prompt.TrustUser,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheSessionDynamic,
		Order:      30,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Value(valAgentPrompt) == "" {
				return false, "agent " + a.Agent + " has no prompt of its own"
			}
			return true, "the configured prompt for agent " + a.Agent
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valAgentPrompt) },
	})

	// After the agent's own prompt, deliberately: a specialist's
	// instructions are never overridden by the orchestration policy, and
	// a specialist does not receive it at all.
	r.Add(prompt.Asset{
		ID:         AssetOrchestration,
		Kind:       prompt.KindModeInstruction,
		Provenance: prompt.FromProduct,
		Trust:      prompt.TrustSystem,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheSessionDynamic,
		Order:      40,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if !a.SmartAgent {
				return false, "smart agent is off for this turn"
			}
			if a.Role != prompt.RoleOrchestrator {
				return false, "this turn is a " + string(a.Role) + ", which does not orchestrate"
			}
			if a.Value(valOrchestrator) == "" {
				return false, "there is nobody to delegate to"
			}
			return true, "this turn orchestrates"
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valOrchestrator) },
	})

	// After the orchestration policy, and gated separately. A turn can
	// orchestrate without being allowed to plan, which is the ordinary
	// case: Smart Agent on, orchestrate off.
	r.Add(prompt.Asset{
		ID:         AssetPlanPolicy,
		Kind:       prompt.KindModeInstruction,
		Provenance: prompt.FromProduct,
		Trust:      prompt.TrustSystem,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheSessionDynamic,
		Order:      41,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Value(valPlanPolicy) == "" {
				return false, "orchestration is off, or this turn has nobody to delegate a stage to"
			}
			return true, "this turn may run a plan"
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valPlanPolicy) },
	})

	// The boundary rides with the whole bundle, orchestrator and
	// specialist alike: the orchestrator gets it as part of deciding,
	// and the specialist gets it because the specialist is the one
	// actually reading the tool output it is about.
	r.Add(prompt.Asset{
		ID:         AssetTrustBoundary,
		Kind:       prompt.KindSafetyPolicy,
		Provenance: prompt.FromProduct,
		Trust:      prompt.TrustSystem,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheSessionDynamic,
		Order:      50,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if !a.SmartAgent {
				return false, "smart agent is off for this turn"
			}
			if a.Lifecycle != "" && a.Lifecycle != prompt.LifecycleTurn {
				// A utility call carries the conversation's own folded
				// prompt, boundary included; selecting it again here
				// would say it twice.
				return false, "a " + string(a.Lifecycle) + " call carries the conversation's prompt instead"
			}
			return true, "every agent in the bundle is told what counts as an instruction"
		},
		Render: func(prompt.ActivationContext) string { return smart.TrustBoundary },
	})

	// Last, so a note about how to write for this window is not buried
	// under the project's rules, and re-derived on every fallback
	// because it is written per family: sending the note for a hosted
	// flagship to the local 8B that caught the overflow produces an
	// answer worse than the failure it replaced.
	r.Add(prompt.Asset{
		ID:         AssetModelQuirk,
		Kind:       prompt.KindModeInstruction,
		Provenance: prompt.FromProduct,
		Trust:      prompt.TrustSystem,
		Placement:  prompt.PlaceSystem,
		Cache:      prompt.CacheSessionDynamic,
		Order:      60,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Value(valModelQuirk) == "" {
				return false, "no note is written for " + a.Model
			}
			return true, "the note written for " + a.Model
		},
		Render: func(a prompt.ActivationContext) string { return a.Value(valModelQuirk) },
	})

	// The summarizing call's instruction. PlaceUtilityCall: it is not
	// part of any conversational request, and recording it here is what
	// makes CU-07's separation checkable — a utility prompt is in the
	// inventory and never in a turn's system block. Renders the same
	// constant compactHistory sends, so the inventory and the call
	// cannot drift apart.
	r.Add(prompt.Asset{
		ID:         AssetCompactPrompt,
		Kind:       prompt.KindUtilityPrompt,
		Provenance: prompt.FromProduct,
		Trust:      prompt.TrustSystem,
		Placement:  prompt.PlaceUtilityCall,
		Cache:      prompt.CacheStable,
		Order:      10,
		Version:    "1",
		Active: func(a prompt.ActivationContext) (bool, string) {
			if a.Lifecycle != prompt.LifecycleCompaction {
				return false, "this is not the compaction call"
			}
			return true, "the summarizing call's instruction"
		},
		Render: func(prompt.ActivationContext) string { return compactionPrompt },
	})

	return r
}
