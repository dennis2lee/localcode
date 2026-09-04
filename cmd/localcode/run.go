package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"localcode/internal/agent"
	"localcode/internal/client"
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
	// session keeps the conversation instead of throwing it away, so it
	// can be picked up in the TUI or the Web UI afterwards.
	session bool
	// server is the daemon to route a kept conversation through, when
	// there is one. Empty means "look at the default address".
	server string
	// listen is the address to look for a daemon at. A flag rather than a
	// constant because somebody running one on another port would
	// otherwise get a second writer on their session directory, which is
	// the one outcome this whole path exists to avoid.
	listen string
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
	fs.BoolVar(&o.session, "session", false, "keep the conversation so it can be continued in the TUI or Web UI; prints its id")
	fs.StringVar(&o.server, "server", "", "daemon to run a kept conversation through, e.g. http://localhost:4096")
	fs.StringVar(&o.listen, "listen", defaultAddr, "address to look for a running daemon at, for --session")
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

	if o.server != "" && !o.session {
		return errors.New("--server is for --session: a conversation that is thrown away has no reason to go through a daemon")
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
	// A kept conversation goes through a daemon when there is one.
	//
	// Not for convenience: two processes appending to one session
	// directory is how a log ends up with sequences that repeat and go
	// backwards, which this project has already had to fix the reading
	// side of. Routing through the daemon leaves exactly one writer, and
	// it is also the only way the conversation shows up without
	// restarting anything — a daemon reads the session directory once, at
	// startup, and never looks again.
	if o.session {
		url := runDaemonURL(o)
		switch {
		case url != "" && !o.shapesTheTurn():
			return throughDaemon(ctx, o, url, prompt, out)
		case url != "":
			// The shaping flags describe a turn this process builds and a
			// daemon builds its own, so the two cannot both be honoured.
			// Honouring the flags and saying what it costs beats refusing:
			// a script that works on a machine with no daemon and fails on
			// one with a daemon is the worse surprise, and the cost here is
			// only that a daemon reads the session directory at startup and
			// never looks again.
			fmt.Fprintf(os.Stderr,
				"note: --bare/--profile/--model/--skip-permissions shape a turn this process builds, "+
					"so this runs here rather than on the daemon at %s. "+
					"The conversation is written to disk and appears there when it next starts.\n", url)
		}
	}

	loop, agentName, cleanup, err := buildOneShot(ctx, o)
	if err != nil {
		return err
	}
	defer cleanup()

	// Visible in the session list only when it is being kept. A thrown-away
	// run has no business in a list of conversations, and a benchmark that
	// ran a thousand of them would otherwise leave a thousand to scroll
	// past.
	sid := oneShotSessionID(o)
	if _, err := loop.Store.CreateSession(sid, "", agentName, o.session); err != nil {
		return fmt.Errorf("start a session to run in: %w", err)
	}
	if o.session {
		// Said on stderr, so it does not land in the answer a script is
		// parsing. It is the one thing a person needs from this run that
		// is not the answer.
		fmt.Fprintf(os.Stderr, "session %s\n", sid)
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
	// In memory unless the conversation is being kept. A run that is
	// thrown away must not touch the session directory at all: that is
	// what makes it safe to run a thousand times, and safe to run beside
	// a daemon.
	//
	// When it is kept, this path is only reached because no daemon
	// answered — so this process is the only writer, which is the
	// property that matters.
	storeDir := ""
	if o.session {
		storeDir = filepath.Join(e.home, ".localcode", "sessions")
	}
	store, err := session.NewStore(storeDir)
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

	// Somewhere to delegate to.
	//
	// Smart Agent is a roster of sub-agents plus a prompt telling the model
	// to use them, and the prompt was already being sent from here: a run
	// with smart_agent on was told to send its wide reading to a Task tool
	// this process had never registered. That is worse than not shipping
	// the feature at all — a policy describing a tool the model was not
	// given is a turn spent looking for it, which is exactly what it looked
	// like from outside (a model grepping in a loop, delegating nothing).
	//
	// Not everything the daemon registers, and the line is the mode's own.
	// Three are left out because a run cannot honour them, and each would
	// otherwise be a tool that can only refuse — a turn the model spends
	// discovering it. Booking work for later needs something to still be
	// here when the time comes. Reading another conversation needs another
	// conversation, and a one-shot's store holds its own session and
	// nothing else. And a debate can only be started from a conversation
	// somebody is having, which a pipe is definitionally not: every turn
	// here is unattended, and that is the first thing debateRefusal checks.
	//
	// Background delegation is the near case and it is in, because it earns
	// its place inside a single turn: three independent questions launched
	// at once cost what one costs, which is most of what Smart Agent is for
	// and the whole of what a harness measures. What it needs is for the
	// run not to exit while a sub-agent is still writing — see the cleanup
	// below.
	tasks := agent.NewTaskManager(ctx, loop, cfg.MaxConcurrentTasks)
	registry.Register(agent.NewTaskTool(tasks, loop.DelegatableAgents))
	registry.Register(agent.NewTaskBackgroundTool(tasks, loop.DelegatableAgents))
	registry.Register(agent.NewTaskCollectTool(tasks))
	// Orchestrate and the tool a stage answers with. Both run inline, in
	// the tool call that asked for them (see orchestrate_run.go), so a plan
	// is finished before the turn is. Its policy text is sent from here
	// already too, which makes leaving the tool out the same defect again.
	registry.Register(agent.NewOrchestrateTool(loop))
	registry.Register(agent.NewAnswerTool())
	// A checklist is worth keeping even where nobody is watching it live:
	// the run's transcript is read afterwards, and the model gets the
	// same next-action discipline either way.
	registry.Register(agent.NewUpdatePlanTool(loop))
	// The model running a command. A run honours it for the same reason
	// it honours anything else in the config: the booked command becomes
	// the next turn through the same SendMessage, and a run has one. What
	// it may run is the list the person wrote, which this process reads
	// from the same file the daemon does.
	registry.Register(agent.NewCommandTool(loop))

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
		loop.Version = version
	}
	return loop, agentName, func() {
		// The run does not end before the work it started does. A background
		// sub-agent was told to keep going after the turn that launched it;
		// in a daemon there is a process for it to keep going in, and here
		// the only one is this. Returning now would kill it mid-edit, having
		// already reported to the model that it was under way.
		//
		// Said before waiting rather than after, on stderr so it stays out
		// of the answer a script is parsing: a pipe that goes quiet is
		// indistinguishable from one that hung.
		if n := tasks.Outstanding(); n > 0 {
			fmt.Fprintf(os.Stderr, "waiting for %d background sub-agent(s) to finish\n", n)
		}
		tasks.Drain(ctx)
	}, nil
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

// shapesTheTurn reports whether any flag describes how this process would
// build the turn, as opposed to what to ask. Those cannot travel to a
// daemon, which builds its own.
func (o runOptions) shapesTheTurn() bool {
	return o.bare || o.profile != "" || o.model != "" || o.skip
}

// oneShotSessionID names the conversation.
//
// A kept one needs an id nothing else will ever pick, since it is going
// into a directory the daemon also writes to; a thrown-away one lives and
// dies in this process and can be called anything.
func oneShotSessionID(o runOptions) string {
	if !o.session {
		return "run"
	}
	return fmt.Sprintf("run-%d", time.Now().UnixNano())
}

// runDaemonURL is the daemon to route a kept conversation through, or ""
// when there is nobody to route to.
//
// --server names one outright. Otherwise the default address is asked
// whether a localcode daemon is there, using the same probe that decides
// whether starting up should attach instead of binding — so the two
// cannot disagree about what counts as a daemon being present.
func runDaemonURL(o runOptions) string {
	if o.server != "" {
		return strings.TrimRight(o.server, "/")
	}
	if _, ok := daemonWorkspace(o.listen); ok {
		return "http://" + o.listen
	}
	return ""
}

// throughDaemon runs the prompt in a session the daemon owns.
//
// The answer comes back over the same event stream every other client
// reads, so the three formats print exactly what they print for a run of
// this process's own.
func throughDaemon(ctx context.Context, o runOptions, url, prompt string, out io.Writer) error {
	c := client.New(url)
	sess, err := c.CreateSession(ctx, o.agent)
	if err != nil {
		return fmt.Errorf("create a session on %s: %w", url, err)
	}
	fmt.Fprintf(os.Stderr, "session %s on %s\n", sess.ID, url)

	// Subscribed from the start of the log, so nothing between creating
	// the session and sending the prompt is missed.
	stream := c.StreamEvents(ctx, sess.ID, 0)
	w := newRunWriter(o.format, out)
	done := make(chan error, 1)
	go func() { done <- c.SendMessage(ctx, sess.ID, prompt) }()

	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				return w.finish(sess.ID, <-done)
			}
			w.event(ev)
		case err := <-done:
			// Drain what the daemon has already sent for this turn.
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for {
				select {
				case ev, ok := <-stream:
					if !ok {
						return w.finish(sess.ID, err)
					}
					w.event(ev)
					if ev.Type == events.TypeTurnDone {
						return w.finish(sess.ID, err)
					}
					continue
				case <-deadline.C:
					return w.finish(sess.ID, err)
				}
			}
		case <-ctx.Done():
			return fmt.Errorf("gave up after %s", o.timeout)
		}
	}
}
