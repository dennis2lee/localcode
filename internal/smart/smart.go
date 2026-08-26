// Package smart holds the Smart Agent bundle: the built-in specialist
// sub-agents, the orchestration prompt that knows how to use them, and the
// category routing that decides which of the user's models each specialist
// runs on.
//
// It exists as its own package because none of it is a property of the
// agent loop. The loop already knows how to run a named agent in its own
// session with its own prompt, model and tool allowlist — that machinery
// arrived with the Task tool and per-agent config. What was missing is
// anybody to run: a config with one agent in it has nowhere to delegate,
// so the whole multi-agent half of localcode only worked for people who
// had already sat down and written six agent blocks by hand.
//
// Smart Agent is that roster, shipped. It is off by default and changes
// nothing until it is turned on, because it spends money the ordinary
// single-agent path does not: every delegation is a second model call
// against a second context.
package smart

import (
	"sort"

	"localcode/internal/config"
)

// Agent is one built-in specialist.
//
// Deliberately not config.AgentConfig: an AgentConfig names a profile,
// and which profile a specialist should run on is not knowable here. It
// depends on what the person running localcode actually has configured,
// which is what Category and routing.go are for.
type Agent struct {
	// Name is what the orchestrator calls it by, and what appears in the
	// Task tool's enum. Lowercase and plain, because the model reads it.
	Name string

	// Description is the one line the orchestrator sees when choosing.
	// It is the most load-bearing string in this file: delegation is only
	// as good as the model's ability to tell these apart at a glance.
	Description string

	// Category is the capability class this agent needs, resolved to one
	// of the user's profiles at load time. See routing.go.
	Category string

	// Prompt is the specialist's system prompt, appended to the base one
	// for turns run as this agent.
	Prompt string

	// Tools is the allowlist. Every entry here is enforced twice — once
	// in what the model is shown, once when it calls anyway — and every
	// specialist has one. An agent with no restriction would get the
	// delegation tools too, which is the recursive-explosion problem: a
	// sub-agent that can spawn sub-agents, told to be thorough, will.
	// Saying "do not delegate" in the prompt is a request; leaving Task
	// out of this list is the answer.
	Tools []string
}

// Tool names, spelled once. These are the names the registry registers
// under (see cmd/localcode/wire.go); a typo here is not an error, it is a
// specialist quietly missing a tool, which is worse.
const (
	ToolRead    = "read_file"
	ToolWrite   = "write_file"
	ToolEdit    = "edit"
	ToolBash    = "bash"
	ToolGlob    = "glob"
	ToolGrep    = "grep"
	ToolSkill   = "Skill"
	ToolTask    = "Task"
	ToolSpawn   = "TaskBackground"
	ToolCollect = "TaskCollect"
)

// DelegationTools are the tools that only mean anything when there is
// somewhere to delegate to. Named as a set so the loop can hide them in
// the one case where they are noise (a config with a single agent and
// Smart Agent off) and so no specialist can be given one by accident.
var DelegationTools = []string{ToolTask, ToolSpawn, ToolCollect}

// readOnly is the tool set for the agents that investigate rather than
// change: everything needed to find and read, and nothing that writes.
//
// Bash is absent on purpose, and it is the line that matters most in this
// file. A "read-only" agent with a shell is not read-only — `sh -c` is a
// write tool wearing a different name — and the whole value of sending
// investigation to a separate agent is that its answer can be trusted not
// to have changed anything on the way.
var readOnly = []string{ToolRead, ToolGlob, ToolGrep, ToolSkill}

// reportRule is appended to every specialist's prompt.
//
// This is context isolation stated as an instruction. The point of a
// sub-agent is that it can read fifty thousand tokens of grep output in a
// context the main session never pays for, and hand back two hundred. A
// specialist that returns its whole transcript has cost more than doing
// the work inline.
const reportRule = "\n\nReport back in under 300 words. You are answering a question for another agent " +
	"that cannot see anything you saw: give it the findings, the exact file paths and line numbers, and the " +
	"conclusion. Do not narrate what you did, do not paste long file contents or command output, and do not " +
	"pad the answer to look thorough. If you could not find the answer, say that plainly and say where you looked."

// builtins is the roster.
//
// The shape is borrowed from the multi-agent harnesses this is modelled
// on: a small set of sharply separated roles rather than a large set of
// overlapping ones. Six is already at the edge of what a model reliably
// picks between; the failure mode of adding a seventh is not that it goes
// unused, it is that the orchestrator starts choosing wrongly among the
// six that were working.
var builtins = []Agent{
	{
		Name:        "explore",
		Category:    CategoryQuick,
		Description: "Find things. Locates files, symbols, call sites and configuration across the project and reports where they are. Cheap and fast; use it instead of grepping the codebase yourself.",
		Tools:       readOnly,
		Prompt: "You are the explore agent. Your job is to find where something lives in this project and " +
			"report back, not to explain it in depth and not to change it. Search broadly first (Glob, Grep), " +
			"then read only the files that matter. Prefer several cheap searches over one expensive read. " +
			"Answer with paths and line numbers.",
	},
	{
		Name:        "librarian",
		Category:    CategoryDeep,
		Description: "Read and digest. Works through documentation, long files, logs or unfamiliar subsystems and returns a condensed account of how they work.",
		Tools:       readOnly,
		Prompt: "You are the librarian agent. You are given something long — documentation, a subsystem, a " +
			"log — and you return the part of it that answers the question. Read the whole of what you are " +
			"pointed at before answering. Quote exactly when the wording matters, summarise when it does not, " +
			"and say explicitly when the source does not cover what was asked.",
	},
	{
		Name:        "oracle",
		Category:    CategoryDeep,
		Description: "Review and critique. Reads a change or a design and looks for what is wrong with it: bugs, missed cases, bad assumptions. Never edits anything.",
		Tools:       readOnly,
		Prompt: "You are the oracle agent: a reviewer, not an implementer. Read what you are given and look " +
			"for what is actually wrong with it — a case it does not handle, an assumption that does not hold, " +
			"a failure it cannot recover from. Say what would have to be true for each problem to bite, and " +
			"where in the code it is. Do not list style preferences, do not restate what the code does, and do " +
			"not approve something you have not read. If it looks correct, say so and say what you checked.",
	},
	{
		Name:        "plan",
		Category:    CategoryDeep,
		Description: "Decompose. Turns a large or vague request into an ordered list of concrete steps, naming the files each one touches. Reads code but never edits it.",
		Tools:       readOnly,
		Prompt: "You are the plan agent. Turn the request into an ordered list of steps that someone else " +
			"could carry out without asking you anything. Read enough of the code first that each step names " +
			"the actual files and functions involved. Say which steps depend on which. Call out the decisions " +
			"that are the user's to make rather than deciding them. Do not write the code.",
	},
	{
		Name:        "implement",
		Category:    CategoryBalanced,
		Description: "Make a self-contained change. Edits files and runs commands to carry out one clearly specified piece of work, then reports what changed.",
		Tools:       []string{ToolRead, ToolWrite, ToolEdit, ToolBash, ToolGlob, ToolGrep, ToolSkill},
		Prompt: "You are the implement agent. You are given one self-contained piece of work and you finish " +
			"it: read what you need, make the change, and check that it builds or runs. Stay inside what you " +
			"were asked for — if you find a second problem, report it rather than fixing it too. Report which " +
			"files you changed and what you verified.",
	},
	{
		Name:        "verify",
		Category:    CategoryQuick,
		Description: "Check that it works. Runs the build, the tests or the command and reports what actually happened, without fixing anything.",
		Tools:       []string{ToolRead, ToolBash, ToolGlob, ToolGrep},
		Prompt: "You are the verify agent. Run what you were asked to run and report exactly what happened: " +
			"the command, whether it passed, and the failing output if it did not. Do not fix anything, do not " +
			"edit files, and do not report success you did not observe — if the command could not be run, say " +
			"that instead of guessing at the outcome.",
	},
}

// Agents returns the built-in specialists as agent configs, each pointed
// at whichever of cfg's profiles best fits its category.
//
// User config wins: a name already in cfg.Agents is left alone entirely,
// so someone who has written their own "explore" agent keeps it, prompt,
// model, tools and all. That is the only override mechanism this needs —
// the built-ins are a starting roster, not a thing to configure.
//
// Returns nil when cfg has no profile to route to at all, which is the
// only way this can fail: an agent whose profile does not resolve is a
// turn that errors out the moment it is delegated to.
func Agents(cfg *config.Config) map[string]config.AgentConfig {
	if cfg == nil || len(cfg.Profiles) == 0 {
		return nil
	}
	out := map[string]config.AgentConfig{}
	for _, a := range builtins {
		if _, taken := cfg.Agents[a.Name]; taken {
			continue
		}
		profile := ProfileFor(cfg, a.Category)
		if profile == "" {
			continue
		}
		out[a.Name] = config.AgentConfig{
			Profile:     profile,
			Description: a.Description,
			Prompt:      a.Prompt + reportRule,
			Tools:       append([]string(nil), a.Tools...),
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Names lists the built-in agent names, sorted — for docs, tests, and
// anything that needs to say what the roster is without resolving it
// against a config.
func Names() []string {
	names := make([]string, 0, len(builtins))
	for _, a := range builtins {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	return names
}

// Builtins returns a copy of the roster, for tests and for the docs
// generator. A copy because Agent contains a slice.
func Builtins() []Agent {
	out := make([]Agent, len(builtins))
	for i, a := range builtins {
		out[i] = a
		out[i].Tools = append([]string(nil), a.Tools...)
	}
	return out
}
