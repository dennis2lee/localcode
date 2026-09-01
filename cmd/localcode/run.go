package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"localcode/internal/agent"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
)

// One prompt, one answer, no window.
//
// Every other way into localcode ends in something that stays: a TUI, a
// desktop window, or a daemon serving an HTTP API. None of those can be
// put in a pipe, which is what a benchmark harness, a git hook and a
// shell script all want — and the absence showed up as a row of "no
// one-shot CLI mode" in somebody's comparison table.
//
// In-process rather than through the daemon. The daemon is the right
// answer for a thing you attach clients to, and the wrong one here: it
// binds a port, it outlives the answer, and two of them sharing a session
// directory is a hazard this project has already had to write down. A
// one-shot run has one caller, needs no port, and should leave nothing
// behind.

// runOptions is what the flags say.
type runOptions struct {
	format  string
	agent   string
	profile string
	model   string
	bare    bool
	skip    bool
	timeout time.Duration
	config  string
}

// Output formats. Three, because the three callers want different things:
// a person reading, a script parsing one answer, and a harness watching a
// turn happen.
const (
	formatText       = "text"
	formatJSON       = "json"
	formatStreamJSON = "stream-json"
)

func runOneShot(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var o runOptions
	fs.StringVar(&o.format, "format", formatText, "output format: text, json, or stream-json")
	fs.StringVar(&o.agent, "agent", "general-purpose", "which agent from config to answer as")
	fs.StringVar(&o.profile, "profile", "", "model profile from config to use, overriding the agent's own")
	fs.StringVar(&o.model, "model", "", "model id to use, overriding the profile's own")
	fs.BoolVar(&o.bare, "bare", false, "load nothing but the base prompt: no AGENTS.md/CLAUDE.md, no skills, no memory, no hooks, no MCP, no custom commands")
	fs.BoolVar(&o.skip, "skip-permissions", false, "run tools without asking; without this a tool that needs permission is refused, since nobody is watching")
	fs.DurationVar(&o.timeout, "timeout", 0, "give up after this long (e.g. 90s); zero waits indefinitely")
	fs.StringVar(&o.config, "config", "", "path to a single config.json")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: localcode run [flags] \"prompt\"\n\n"+
			"Answers one prompt and exits. The prompt may also come from stdin:\n"+
			"  echo \"what does this repo do?\" | localcode run\n\n"+
			"Flags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch o.format {
	case formatText, formatJSON, formatStreamJSON:
	default:
		return fmt.Errorf("unknown --format %q: use text, json or stream-json", o.format)
	}

	prompt, err := readPrompt(fs.Args())
	if err != nil {
		return err
	}
	return oneShot(context.Background(), o, prompt, os.Stdout)
}

// readPrompt takes the prompt from the command line or from stdin.
//
// Stdin matters more than it looks: a prompt long enough to be worth
// scripting is a prompt with newlines and quotes in it, and getting one of
// those through a shell argument intact is its own small ordeal.
func readPrompt(args []string) (string, error) {
	joined := strings.TrimSpace(strings.Join(args, " "))
	if joined != "" && joined != "-" {
		return joined, nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read prompt from stdin: %w", err)
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", errors.New("no prompt: pass one as an argument or on stdin")
	}
	return prompt, nil
}

func oneShot(ctx context.Context, o runOptions, prompt string, out io.Writer) error {
	if o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}

	loop, agentName, cleanup, err := buildOneShot(ctx, o)
	if err != nil {
		return err
	}
	defer cleanup()

	// An in-memory store. A one-shot leaves nothing behind: it does not
	// belong in the session list, it must not collide with a daemon's
	// session directory, and a benchmark that ran a thousand prompts
	// would otherwise leave a thousand conversations to scroll past.
	const sid = "run"
	if _, err := loop.Store.CreateSession(sid, "", agentName, false); err != nil {
		return fmt.Errorf("start a session to run in: %w", err)
	}
	if o.skip {
		// The session's own switch, the same one "/permission-skip-all"
		// sets — rather than a second way to mean the same thing.
		yes := true
		if _, err := loop.Store.SetPermission(sid, session.SwitchSkipAll, &yes); err != nil {
			return fmt.Errorf("--skip-permissions: %w", err)
		}
	}

	live, lost, unsub, err := loop.Store.Subscribe(sid)
	if err != nil {
		return err
	}
	defer unsub()

	// Nobody is watching, which is the same fact a scheduled run states —
	// so a tool that needs permission is refused rather than left waiting
	// on an answer that is never coming. Zero rather than the scheduler's
	// five minutes: a scheduled run fires while somebody is usually at the
	// desk, and a pipe has no desk.
	restore := agent.SetUnattendedWait(0)
	defer restore()
	turnCtx := agent.WithUnattended(ctx)

	w := newRunWriter(o.format, out)
	done := make(chan error, 1)
	go func() { done <- loop.SendMessage(turnCtx, sid, agentName, prompt) }()

	for {
		select {
		case ev := <-live:
			w.event(ev)
		case <-lost:
			// The subscriber fell behind and events were dropped. Said
			// rather than swallowed: a truncated answer that looks whole
			// is the one outcome worth refusing.
			return errors.New("fell behind the event stream; the answer would be incomplete")
		case err := <-done:
			// Whatever is already in the channel belongs to this turn.
			for {
				select {
				case ev := <-live:
					w.event(ev)
					continue
				default:
				}
				break
			}
			return w.finish(sid, err)
		case <-ctx.Done():
			return fmt.Errorf("gave up after %s", o.timeout)
		}
	}
}

// buildOneShot assembles the parts buildDaemon assembles, minus everything
// that only makes sense for something that stays running.
//
// The same helpers, deliberately: loadConfig, buildProviders,
// buildRegistry and buildSystemPrompt are where the rules live, and a
// second copy of any of them would drift. What differs is the
// composition — no session directory, no rehydration, no scheduler, no
// task manager, no trace.
func buildOneShot(ctx context.Context, o runOptions) (*agent.Loop, string, func(), error) {
	e, err := resolveEnv()
	if err != nil {
		return nil, "", nil, err
	}
	cfg, err := loadConfig(o.config, e)
	if err != nil {
		return nil, "", nil, err
	}
	agentName, err := applyModelChoice(cfg, o)
	if err != nil {
		return nil, "", nil, err
	}
	if o.bare {
		// Hooks are somebody's shell commands running around every turn,
		// which is exactly the kind of ambient difference that makes two
		// tools incomparable.
		cfg.Hooks = nil
	}

	providers, err := buildProviders(ctx, cfg, e)
	if err != nil {
		return nil, "", nil, err
	}
	store, err := session.NewStore("")
	if err != nil {
		return nil, "", nil, err
	}
	broker := agent.NewPermissionBroker(store)
	registry, err := buildRegistry(cfg, broker, store)
	if err != nil {
		return nil, "", nil, err
	}

	loop := agent.New(store, registry, providers, cfg)
	loop.ProjectDir = e.cwd
	if !o.bare {
		// The workspace speaking, and the two indexes a session normally
		// opens with. All three are what --bare exists to silence: they
		// are real context in ordinary use and an unfair advantage in a
		// comparison, and either way the reader should know which they
		// are getting.
		loop.WorkspaceRules = workspaceRules(e)
		skillsSection, memoryPolicy, memorySection, skillList, cmdList, memDir, err := buildSystemPrompt(cfg, registry, e)
		if err != nil {
			return nil, "", nil, err
		}
		loop.SkillsSection = skillsSection
		loop.MemoryPolicy = memoryPolicy
		loop.MemorySection = memorySection
		loop.Skills = skillList
		loop.Commands = cmdList
		loop.MemoryDir = memDir
	}
	return loop, agentName, func() {}, nil
}

// applyModelChoice resolves --agent, --profile and --model against the
// config, and reports which agent to answer as.
//
// The config is loaded fresh for this process and never written back, so
// pointing an agent at another profile and overriding that profile's model
// is a local edit to a value nothing else will ever see. That is a great
// deal simpler than threading an override through the turn.
func applyModelChoice(cfg *config.Config, o runOptions) (string, error) {
	agentName := o.agent
	agentCfg, ok := cfg.Agents[agentName]
	if !ok {
		return "", fmt.Errorf("no agent %q in config. Configured: %s", agentName, strings.Join(sortedNames(cfg.Agents), ", "))
	}
	profileName := agentCfg.Profile
	if o.profile != "" {
		if _, ok := cfg.Profiles[o.profile]; !ok {
			return "", fmt.Errorf("no profile %q in config. Configured: %s", o.profile, strings.Join(sortedProfiles(cfg.Profiles), ", "))
		}
		profileName = o.profile
		agentCfg.Profile = profileName
		cfg.Agents[agentName] = agentCfg
	}
	if profileName == "" {
		profileName = cfg.DefaultProfile
	}
	if o.model != "" {
		p, ok := cfg.Profiles[profileName]
		if !ok {
			return "", fmt.Errorf("--model needs a profile to apply to, and %q resolves to none", agentName)
		}
		p.Model = o.model
		cfg.Profiles[profileName] = p
	}
	return agentName, nil
}

// runWriter turns the event stream into whatever the caller asked for.
type runWriter struct {
	format string
	out    io.Writer
	enc    *json.Encoder

	text    strings.Builder
	toolLog []map[string]any
	usage   map[string]any
	started time.Time
	errs    []string
}

func newRunWriter(format string, out io.Writer) *runWriter {
	w := &runWriter{format: format, out: out, started: time.Now()}
	if format == formatStreamJSON {
		w.enc = json.NewEncoder(out)
	}
	return w
}

func (w *runWriter) event(ev events.Event) {
	switch w.format {
	case formatStreamJSON:
		// The event stream itself, one object per line. Not a new schema:
		// this is what every client already reads, so a harness written
		// against it is written against the thing rather than a summary
		// of it that could drift.
		_ = w.enc.Encode(map[string]any{"type": string(ev.Type), "data": ev.Data})
	case formatText:
		// Streamed as it arrives, because a one-shot that prints nothing
		// for ninety seconds is indistinguishable from one that hung.
		if ev.Type == events.TypeMessagePartDelta {
			if s, _ := ev.Data["text"].(string); s != "" {
				fmt.Fprint(w.out, s)
			}
		}
	}
	w.record(ev)
}

// record keeps what the json format reports at the end, whatever the
// format is — the two are separate questions and a run that streams still
// has to be able to say what its usage was.
func (w *runWriter) record(ev events.Event) {
	switch ev.Type {
	case events.TypeMessagePartEnd:
		// The daemon's whole copy of the reply, which is authoritative:
		// it is written over whatever the fragments said.
		if s, _ := ev.Data["text"].(string); s != "" {
			w.text.Reset()
			w.text.WriteString(s)
		}
	case events.TypeToolStart:
		name, _ := ev.Data["name"].(string)
		w.toolLog = append(w.toolLog, map[string]any{"name": name, "input": ev.Data["input"]})
	case events.TypeToolEnd:
		if n := len(w.toolLog); n > 0 {
			isErr, _ := ev.Data["is_error"].(bool)
			w.toolLog[n-1]["is_error"] = isErr
		}
	case events.TypeUsage:
		// Only the settled figures. The live ones broadcast during a
		// stream are estimates and carry a flag saying so; reporting one
		// as the answer's cost would be reporting a guess as a fact.
		if estimated, _ := ev.Data["estimated"].(bool); !estimated {
			w.usage = ev.Data
		}
	case events.TypeError:
		if s, _ := ev.Data["error"].(string); s != "" {
			w.errs = append(w.errs, s)
		}
	}
}

// finish writes whatever the format owes at the end and reports whether
// the run failed, so the process can exit non-zero on one.
func (w *runWriter) finish(sessionID string, turnErr error) error {
	switch w.format {
	case formatText:
		if w.text.Len() > 0 || turnErr == nil {
			fmt.Fprintln(w.out)
		}
	case formatJSON:
		out := map[string]any{
			"session_id":  sessionID,
			"result":      w.text.String(),
			"duration_ms": time.Since(w.started).Milliseconds(),
		}
		if len(w.toolLog) > 0 {
			out["tools"] = w.toolLog
		}
		if w.usage != nil {
			out["usage"] = map[string]any{
				"input_tokens":  w.usage["input_tokens"],
				"output_tokens": w.usage["output_tokens"],
			}
		}
		if turnErr != nil {
			out["error"] = turnErr.Error()
		} else if len(w.errs) > 0 {
			out["error"] = strings.Join(w.errs, "; ")
		}
		enc := json.NewEncoder(w.out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	}
	return turnErr
}

// sortedNames and sortedProfiles name what is configured, for the two
// refusals above. A refusal that says only "no such agent" leaves the
// reader guessing at spelling.
func sortedNames(m map[string]config.AgentConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return sortStrings(out)
}

func sortedProfiles(m map[string]config.Profile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return sortStrings(out)
}

func sortStrings(in []string) []string {
	sort.Strings(in)
	return in
}
