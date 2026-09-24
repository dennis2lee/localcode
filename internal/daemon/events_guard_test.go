package daemon

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Allowlist of event types deliberately not handled in internal/tui/events.go.
//
// The TUI attaches to a single session in a local terminal window. It has no
// multi-session sidebar, no scheduled tasks panel, no MCP server monitor,
// and runs inside the current directory without multi-workspace switching.
//
// 17 of the 50 event types are intentionally unhandled here:
//   - checkpoint: filesystem snapshot for /rewind restoration; read only by
//     the daemon during rewind, not rendered in the terminal transcript.
//   - permission.forgotten: daemon log marker for /read-outside and
//     /write-outside mem-clear to rebuild outside rules across restarts;
//     never displayed in the TUI.
//   - mcp.status: daemon-wide status of configured MCP servers; displayed in
//     the Web UI status bar, but TUI has no MCP server status panel.
//   - session.activity: daemon-wide session busy indicator; consumed by Web
//     UI multi-session sidebar, but TUI attaches to one session.
//   - session.archived: daemon-wide session archive notice; consumed by Web
//     UI sidebar, whereas TUI operates on the current session without an
//     archive drawer.
//   - session.deleted: daemon-wide session deletion broadcast; consumed by
//     Web UI sidebar to remove list items, whereas TUI attaches to one session.
//   - session.renamed: session title update; consumed by Web UI sidebar,
//     whereas TUI status line does not display session titles.
//   - schedule.created: scheduled task booking created; consumed by Web UI
//     schedules panel, whereas TUI has no scheduled task UI.
//   - schedule.status: scheduled task run execution status; consumed by Web
//     UI schedules panel, whereas TUI has no schedule UI.
//   - schedule.seen: scheduled task result acknowledged by user; consumed
//     by Web UI schedules panel indicator, whereas TUI has no schedule UI.
//   - schedule.renamed: scheduled task title changed; consumed by Web UI
//     schedules panel, whereas TUI has no schedule UI.
//   - schedule.removed: scheduled task deleted; consumed by Web UI schedules
//     panel, whereas TUI has no schedule UI.
//   - task.progress: transient tool progress ("doing") of background tasks;
//     displayed in Web UI task indicator, whereas TUI task status only tracks
//     spawned/status lifecycle.
//   - workspace.changed: session directory switch; consumed by Web UI
//     workspace picker, whereas TUI runs within the current terminal directory.
//   - compacted: history compaction summary event; TUI compaction runs locally
//     or transparently without inserting a transcript system note.
//   - config.changed: session configuration change; TUI handles /config
//     commands directly in the client rather than consuming broadcast config changes.
//   - permissions.changed: session permission flags snapshot; consumed by
//     Web UI permission modal, whereas TUI handles permissions via request
//     modals and local commands.
var tuiIgnoredEventTypes = map[string]string{
	"checkpoint":           "filesystem snapshot for /rewind restoration; daemon-internal and has no display representation in the terminal",
	"permission.forgotten": "daemon log marker for /read-outside mem-clear to rebuild outside rules across restarts; not displayed in TUI",
	"mcp.status":           "daemon-wide MCP server status; displayed in Web UI status bar, but TUI has no MCP server status panel",
	"session.activity":     "daemon-wide session busy indicator; consumed by Web UI multi-session sidebar, but TUI attaches to a single session",
	"session.archived":     "daemon-wide session archive notice; consumed by Web UI sidebar, whereas TUI operates on the current session without an archive drawer",
	"session.deleted":      "daemon-wide session deletion broadcast; consumed by Web UI sidebar to remove list items, whereas TUI attaches to one session",
	"session.renamed":      "session title update; consumed by Web UI sidebar and header, whereas TUI status line does not display session titles",
	"schedule.created":     "scheduled task booking created; consumed by Web UI schedules panel, whereas TUI has no scheduled task UI",
	"schedule.status":      "scheduled task run execution status; consumed by Web UI schedules panel, whereas TUI has no schedule UI",
	"schedule.seen":        "scheduled task result acknowledged; consumed by Web UI schedules panel indicator, whereas TUI has no schedule UI",
	"schedule.renamed":     "scheduled task title changed; consumed by Web UI schedules panel, whereas TUI has no schedule UI",
	"schedule.removed":     "scheduled task deleted; consumed by Web UI schedules panel, whereas TUI has no schedule UI",
	"task.progress":        "transient tool progress of background tasks; displayed in Web UI task indicator, whereas TUI task status only tracks spawned/status lifecycle",
	"workspace.changed":    "session directory switch; consumed by Web UI workspace picker, whereas TUI runs within the current terminal directory",
	"config.changed":       "session configuration change; TUI handles /config commands directly in the client rather than consuming broadcast config changes",
	"permissions.changed":  "session permission flags snapshot; consumed by Web UI permission modal, whereas TUI handles permissions via request modals and local commands",
}

// Allowlist of event types deliberately not handled in internal/daemon/static/js/events.js.
//
// The Web UI handles almost all event types across its transcript, status bar,
// modals, schedules panel, and multi-session sidebar. Only 2 event types are
// deliberately unhandled because they are purely server-side persistence records:
//   - checkpoint: filesystem snapshot recorded before file modifications for
//     /rewind restore; read only by the backend, with no UI representation.
//   - permission.forgotten: server-side log entry recorded when outside
//     permissions are forgotten (/read-outside mem-clear); outside paths are
//     rebuilt by the daemon and active permissions arrive via permissions.changed.
var webUIIgnoredEventTypes = map[string]string{
	"checkpoint":           "filesystem snapshot recorded before write/edit for /rewind restoration; read only by the daemon on rewind and never displayed in the Web UI",
	"permission.forgotten": "daemon persistence marker recording when outside permissions are cleared; outside paths are rebuilt by the daemon and active permissions arrive via permissions.changed",
}

// Allowlist of event types deliberately not handled in internal/daemon/static/js/taskview.js.
//
// taskview.js is a read-only modal window monitoring a single background task's
// event stream (/api/sessions/{taskID}/events?tail=200). It renders transcript lines
// and tool executions for that subtask.
//
// 33 of the 50 event types are deliberately not handled in taskview:
//   - checkpoint: daemon-internal file rewind snapshot; never displayed in subtask transcript modal.
//   - permission.forgotten: daemon-internal permission reset marker; subtasks do not run interactive permission resets.
//   - mcp.status: daemon-wide MCP server status; not relevant to a single background task view.
//   - session.activity: daemon-wide session activity indicator; task view renders only the selected task transcript.
//   - session.archived: daemon-wide archive broadcast; task modal views an active task session.
//   - session.deleted: daemon-wide session deletion broadcast; task modal lifecycle is tied to the parent task controls.
//   - session.renamed: daemon-wide session title update; task modal displays the task id and agent name from spawn metadata.
//   - session.forked: subtask conversations are spawned fresh and cannot be created by forking conversations.
//   - session.scheduled: subtasks are spawned by agents, not by scheduled task bookings.
//   - schedule.created: scheduled task booking creation; subtasks do not book schedules.
//   - schedule.status: scheduled task run execution status; not rendered in subtask modal.
//   - schedule.seen: scheduled task acknowledgment; not rendered in subtask modal.
//   - schedule.renamed: scheduled task rename; not rendered in subtask modal.
//   - schedule.removed: scheduled task deletion; not rendered in subtask modal.
//   - settings.changed: daemon-wide settings update; task modal renders only task transcript lines.
//   - config.changed: session config changes; subtasks run under static agent configuration without interactive /config.
//   - workspace.changed: session workspace change; subtasks run in the parent task workspace.
//   - permissions.changed: session permission flags; subtasks inherit permissions or pause on permission.request.
//   - effort.changed: model effort level; task modal renders output stream without effort indicators.
//   - model.changed: model switch; task modal renders model output directly without a model picker.
//   - usage: token usage metrics; task modal does not render a token context fill meter.
//   - task.progress: subtask tool progress is already rendered in real-time by tool.start and tool.end in the modal.
//   - thinking.delta: reasoning streaming chunk; suppressed in task modal to keep background task output concise.
//   - thinking.end: reasoning end marker; suppressed in task modal along with thinking.delta.
//   - thinking.block: a muse reasoning block as the log keeps it; suppressed in task modal along with thinking.delta and thinking.end.
//   - input.request: interactive mid-turn user prompt; subtasks run unattended and cannot prompt the user interactively.
//   - input.resolved: interactive mid-turn response; subtasks run unattended without input prompts.
//   - plan.updated: checklist plan; subtask transcript shows tool calls directly rather than plan checklists.
//   - debate.started: multi-agent debate banner; subtasks do not initiate debate sessions.
//   - debate.review: debate review turn; subtasks do not engage in multi-agent debate reviews.
//   - debate.ended: debate conclusion banner; subtasks do not engage in debate sessions.
//   - daemon.replaced: daemon binary takeover notice; handled globally by the main events stream.
//   - redone: interactive /redo turn restoration; subtasks run unattended without interactive undo/redo.
var taskViewIgnoredEventTypes = map[string]string{
	"checkpoint":           "daemon-internal file rewind snapshot; never displayed in subtask transcript modal",
	"permission.forgotten": "daemon-internal permission reset marker; subtasks do not run interactive permission resets",
	"mcp.status":           "daemon-wide MCP server status; not relevant to a single background task view",
	"session.activity":     "daemon-wide session activity indicator; task view renders only the selected task transcript",
	"session.archived":     "daemon-wide archive broadcast; task modal views an active task session",
	"session.deleted":      "daemon-wide session deletion broadcast; task modal lifecycle is tied to the parent task controls",
	"session.renamed":      "daemon-wide session title update; task modal displays the task id and agent name from spawn metadata",
	"session.forked":       "subtask conversations are spawned fresh and cannot be created by forking conversations",
	"session.scheduled":    "subtasks are spawned by agents, not by scheduled task bookings",
	"schedule.created":     "scheduled task booking creation; subtasks do not book schedules",
	"schedule.status":      "scheduled task run execution status; not rendered in subtask modal",
	"schedule.seen":        "scheduled task acknowledgment; not rendered in subtask modal",
	"schedule.renamed":     "scheduled task rename; not rendered in subtask modal",
	"schedule.removed":     "scheduled task deletion; not rendered in subtask modal",
	"settings.changed":     "daemon-wide settings update; task modal renders only task transcript lines",
	"config.changed":       "session config changes; subtasks run under static agent configuration without interactive /config",
	"workspace.changed":    "session workspace change; subtasks run in the parent task workspace",
	"permissions.changed":  "session permission flags; subtasks inherit permissions or pause on permission.request",
	"effort.changed":       "model effort level; task modal renders output stream without effort indicators",
	"model.changed":        "model switch; task modal renders model output directly without a model picker",
	"usage":                "token usage metrics; task modal does not render a token context fill meter",
	"task.progress":        "subtask tool progress is already rendered in real-time by tool.start and tool.end in the modal",
	"thinking.delta":       "reasoning streaming chunk; suppressed in task modal to keep background task output concise",
	"thinking.end":         "reasoning end marker; suppressed in task modal along with thinking.delta",
	"thinking.block":       "logged muse reasoning block; suppressed in task modal along with thinking.delta and thinking.end, to keep background task output concise",
	"input.request":        "interactive mid-turn user prompt; subtasks run unattended and cannot prompt the user interactively",
	"input.resolved":       "interactive mid-turn response; subtasks run unattended without input prompts",
	"plan.updated":         "checklist plan; subtask transcript shows tool calls directly rather than plan checklists",
	"debate.started":       "multi-agent debate banner; subtasks do not initiate debate sessions",
	"debate.review":        "debate review turn; subtasks do not engage in multi-agent debate reviews",
	"debate.ended":         "debate conclusion banner; subtasks do not engage in debate sessions",
	"daemon.replaced":      "daemon binary takeover notice; handled globally by the main events stream",
	"redone":               "interactive /redo turn restoration; subtasks run unattended without interactive undo/redo",
}

// Allowlist of event types deliberately not handled in internal/agent/rehydrate.go.
//
// rehydrateHistory reconstructs the LLM conversation history ([]provider.Message)
// to send to the model on subsequent turns. Only messages actually seen by the
// model (user messages, completed assistant text, tool calls, tool results,
// compaction/cleared boundaries, and collapsed debate blocks) belong in this history.
//
// 42 of the 50 event types are intentionally ignored here:
//   - UI/display events (thinking, effort, delegated, plan, error, input.request/resolved)
//   - Intermediate stream chunks (message.part.delta)
//   - Daemon/session lifecycle events (session.*, mcp.status, daemon.replaced)
//   - Task management and progress (task.*, schedule.*)
//   - Configuration and settings (settings.changed, config.changed, workspace.changed, permissions.*)
//   - Metrics and file snapshots (usage, checkpoint, rewound, redone)
var rehydrateIgnoredEventTypes = map[string]string{
	"message.part.delta":   "streaming text delta; final assistant message content is captured at message.part.end",
	"thinking.delta":       "model reasoning tokens; transient broadcast never sent back to LLM providers",
	"thinking.end":         "reasoning stream boundary; transient broadcast never sent back to LLM providers",
	"thinking.block":       "muse reasoning logged so the clients draw it folded on replay; never rebuilt into provider history, because the OpenAI-compatible wire takes no reasoning back and a restored history has to size like the live one",
	"permission.request":   "UI authorization dialog; not part of LLM conversation history",
	"permission.resolved":  "UI authorization outcome; not part of LLM conversation history",
	"permission.forgotten": "daemon permission cache eviction; not part of LLM conversation history",
	"permissions.changed":  "session permission flags; not part of LLM conversation history",
	"task.spawned":         "background task spawned notice; subtasks execute in their own sessions and are not in parent model history",
	"task.status":          "background task status change; not part of LLM conversation history",
	"task.progress":        "transient background task tool progress; not part of LLM conversation history",
	"agent.switched":       "agent persona switch; system prompt changes are supplied separately from conversation history",
	"model.changed":        "model selection update; model configuration is supplied per request rather than in message history",
	"effort.changed":       "thinking effort configuration; supplied as request parameter rather than conversation message",
	"mcp.status":           "daemon-wide MCP server connection status; not part of LLM conversation history",
	"session.activity":     "daemon-wide session activity indicator; not part of LLM conversation history",
	"session.archived":     "session archive lifecycle event; not part of LLM conversation history",
	"session.deleted":      "session deletion lifecycle event; not part of LLM conversation history",
	"session.renamed":      "session title update; not part of LLM conversation history",
	"session.forked":       "session fork provenance; deliberately ignored during rehydration so model is not told it is a copy",
	"session.scheduled":    "scheduled run provenance; deliberately ignored during rehydration so model is not told about schedule origin",
	"settings.changed":     "daemon-wide settings snapshot; not part of LLM conversation history",
	"config.changed":       "session config change; not part of LLM conversation history",
	"workspace.changed":    "workspace directory change; passed to model via environment/system prompt rather than history",
	"usage":                "token usage statistics; tracked separately by rehydrateUsage rather than reconstructed into message history",
	"rewound":              "turn rewind marker; handled prior to history reconstruction by applyRewinds",
	"redone":               "turn redo marker; handled prior to history reconstruction by applyRewinds",
	"checkpoint":           "file checkpoint hash for undo; used by file restore logic, not part of conversation history",
	"schedule.created":     "scheduled task booking; not part of LLM conversation history",
	"schedule.status":      "scheduled task status; not part of LLM conversation history",
	"schedule.seen":        "scheduled task acknowledgment; not part of LLM conversation history",
	"schedule.renamed":     "scheduled task rename; not part of LLM conversation history",
	"schedule.removed":     "scheduled task deletion; not part of LLM conversation history",
	"input.request":        "mid-turn question to user; answered via subsequent message, request itself is not a history message",
	"input.resolved":       "mid-turn question resolved; resolution answer is integrated via message flow, not standalone history turn",
	"plan.updated":         "model plan checklist; transient execution state not passed back to model as history messages",
	"debate.review":        "individual debate review turns; collapsed into a single summary block between debate.started and debate.ended",
	"daemon.replaced":      "daemon process handoff notification; not part of LLM conversation history",
	"delegated":            "subagent delegation note; UI display marker not included in LLM history",
	"turn.done":            "turn completion marker; not part of LLM conversation history",
	"turn.cancelled":       "turn cancellation marker; not part of LLM conversation history",
}

// Every event type declared in internal/events/events.go must be explicitly
// accounted for in internal/tui/events.go: either handled in applyEvent or
// recorded in tuiIgnoredEventTypes with a documented reason.
//
// A new event type added to events.go without an entry in either will fail this
// test, preventing silent drops in the terminal interface.
func TestEveryEventTypeIsHandledOrExplicitlyIgnoredInTUI(t *testing.T) {
	root := repoRoot(t)
	declared := loadDeclaredEventTypes(t, filepath.Join(root, "internal", "events", "events.go"))
	handled := loadTUIHandledEventTypes(t, filepath.Join(root, "internal", "tui", "events.go"), declared)

	verifyCoverage(t, "TUI (internal/tui/events.go)", declared, handled, tuiIgnoredEventTypes)
}

// Every event type declared in internal/events/events.go must be explicitly
// accounted for in internal/daemon/static/js/events.js: either handled in
// the handlers map or recorded in webUIIgnoredEventTypes with a documented reason.
//
// The Web UI is the primary interface for multi-session interaction, and an
// unhandled event type is a silent gap where state or transcript rows fail to update.
func TestEveryEventTypeIsHandledOrExplicitlyIgnoredInWebUI(t *testing.T) {
	root := repoRoot(t)
	declared := loadDeclaredEventTypes(t, filepath.Join(root, "internal", "events", "events.go"))
	handled := loadWebUIHandledEventTypes(t, filepath.Join(root, "internal", "daemon", "static", "js", "events.js"))

	verifyCoverage(t, "Web UI (events.js)", declared, handled, webUIIgnoredEventTypes)
}

// Every event type declared in internal/events/events.go must be explicitly
// accounted for in internal/daemon/static/js/taskview.js: either handled in
// applyTaskEvent or recorded in taskViewIgnoredEventTypes with a documented reason.
//
// The background task window streams subtask events; omitting an event without
// a documented decision risks subtasks freezing or failing silently without visual feedback.
func TestEveryEventTypeIsHandledOrExplicitlyIgnoredInTaskView(t *testing.T) {
	root := repoRoot(t)
	declared := loadDeclaredEventTypes(t, filepath.Join(root, "internal", "events", "events.go"))
	handled := loadTaskViewHandledEventTypes(t, filepath.Join(root, "internal", "daemon", "static", "js", "taskview.js"))

	verifyCoverage(t, "TaskView (taskview.js)", declared, handled, taskViewIgnoredEventTypes)
}

// Every event type declared in internal/events/events.go must be explicitly
// accounted for in internal/agent/rehydrate.go: either handled in rehydrateHistory
// or recorded in rehydrateIgnoredEventTypes with a documented reason.
//
// History rehydration reconstructs what the model is sent across process restarts.
// An unhandled event type here could either drop critical context or corrupt
// provider message sequences.
func TestEveryEventTypeIsHandledOrExplicitlyIgnoredInRehydrate(t *testing.T) {
	root := repoRoot(t)
	declared := loadDeclaredEventTypes(t, filepath.Join(root, "internal", "events", "events.go"))
	handled := loadRehydrateHandledEventTypes(t, filepath.Join(root, "internal", "agent", "rehydrate.go"), declared)

	verifyCoverage(t, "Rehydrate (rehydrate.go)", declared, handled, rehydrateIgnoredEventTypes)
}

// verifyCoverage ensures:
// 1. Every declared event type is in either handled or ignored.
// 2. No declared event type is in both handled and ignored (redundant allowlist entry).
// 3. Every entry in the ignore list corresponds to a real declared event type (stale allowlist entry).
func verifyCoverage(t *testing.T, consumerName string, declared map[string]string, handled map[string]bool, ignored map[string]string) {
	t.Helper()

	// Check for stale entries in the allowlist
	for evType := range ignored {
		if _, exists := declared[evType]; !exists {
			t.Errorf("%s allowlist contains %q, which is not a declared event type in internal/events/events.go; remove stale allowlist entry", consumerName, evType)
		}
	}

	var unhandled []string
	var redundant []string

	for evType, constName := range declared {
		isHandled := handled[evType]
		_, isIgnored := ignored[evType]

		if !isHandled && !isIgnored {
			unhandled = append(unhandled, fmt.Sprintf("%s (%s)", evType, constName))
		}
		if isHandled && isIgnored {
			redundant = append(redundant, fmt.Sprintf("%s (%s)", evType, constName))
		}
	}

	sort.Strings(unhandled)
	sort.Strings(redundant)

	if len(unhandled) > 0 {
		t.Errorf("%s has %d unhandled event types not accounted for in its handlers or allowlist:\n  - %s\n"+
			"Every event type must either be handled or documented in the allowlist with why it is ignored.",
			consumerName, len(unhandled), strings.Join(unhandled, "\n  - "))
	}

	if len(redundant) > 0 {
		t.Errorf("%s has %d event types in BOTH handlers and allowlist:\n  - %s\n"+
			"Remove these from the allowlist because they are already handled.",
			consumerName, len(redundant), strings.Join(redundant, "\n  - "))
	}
}

// repoRoot locates the repository root by searching upward for go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate repository root (go.mod) by walking up from current working directory")
	return ""
}

// loadDeclaredEventTypes parses internal/events/events.go using go/parser
// to extract all constants of type Type. Returns map[eventTypeValue]constName.
func loadDeclaredEventTypes(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	out := make(map[string]string)
	for _, decl := range node.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		var lastType string
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				if ident, ok := vs.Type.(*ast.Ident); ok {
					lastType = ident.Name
				} else {
					lastType = ""
				}
			}
			if lastType != "Type" {
				continue
			}
			for i, name := range vs.Names {
				if len(vs.Values) > i {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						val, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatalf("unquote %s: %v", lit.Value, err)
						}
						out[val] = name.Name
					}
				}
			}
		}
	}

	if len(out) < 45 {
		t.Fatalf("parsed only %d event types from %s; parser is broken or file format changed", len(out), path)
	}
	return out
}

// loadTUIHandledEventTypes parses internal/tui/events.go using go/parser
// to extract all event types handled in func (m *Model) applyEvent(ev events.Event).
func loadTUIHandledEventTypes(t *testing.T, path string, declared map[string]string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// Invert declared: constName -> eventTypeValue
	constToVal := make(map[string]string)
	for val, constName := range declared {
		constToVal[constName] = val
	}

	handled := make(map[string]bool)
	var applyEventFunc *ast.FuncDecl
	for _, decl := range node.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "applyEvent" {
			applyEventFunc = fn
			break
		}
	}

	if applyEventFunc == nil {
		t.Fatalf("applyEvent function not found in %s", path)
	}

	ast.Inspect(applyEventFunc.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		// Match switch ev.Type
		if sel, ok := sw.Tag.(*ast.SelectorExpr); ok {
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "ev" && sel.Sel.Name == "Type" {
				for _, stmt := range sw.Body.List {
					cc, ok := stmt.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, expr := range cc.List {
						// Case events.TypeFoo
						if s, ok := expr.(*ast.SelectorExpr); ok {
							if pkg, ok := s.X.(*ast.Ident); ok && pkg.Name == "events" {
								if val, found := constToVal[s.Sel.Name]; found {
									handled[val] = true
								}
							}
						}
					}
				}
			}
		}
		return true
	})

	if len(handled) < 25 {
		t.Fatalf("parsed only %d handled event types in %s; applyEvent switch structure may have changed", len(handled), path)
	}
	return handled
}

// loadRehydrateHandledEventTypes parses internal/agent/rehydrate.go using go/parser
// to extract all event types handled in func rehydrateHistory(evs []events.Event).
func loadRehydrateHandledEventTypes(t *testing.T, path string, declared map[string]string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	constToVal := make(map[string]string)
	for val, constName := range declared {
		constToVal[constName] = val
	}

	handled := make(map[string]bool)
	var rehydrateFunc *ast.FuncDecl
	for _, decl := range node.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "rehydrateHistory" {
			rehydrateFunc = fn
			break
		}
	}

	if rehydrateFunc == nil {
		t.Fatalf("rehydrateHistory function not found in %s", path)
	}

	ast.Inspect(rehydrateFunc.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if sel, ok := sw.Tag.(*ast.SelectorExpr); ok {
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "ev" && sel.Sel.Name == "Type" {
				for _, stmt := range sw.Body.List {
					cc, ok := stmt.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, expr := range cc.List {
						if s, ok := expr.(*ast.SelectorExpr); ok {
							if pkg, ok := s.X.(*ast.Ident); ok && pkg.Name == "events" {
								if val, found := constToVal[s.Sel.Name]; found {
									handled[val] = true
								}
							}
						}
					}
				}
			}
		}
		return true
	})

	if len(handled) < 5 {
		t.Fatalf("parsed only %d handled event types in %s; rehydrateHistory switch structure may have changed", len(handled), path)
	}
	return handled
}

// loadWebUIHandledEventTypes parses static/js/events.js to extract all keys
// in the top-level const handlers = { ... } object literal.
func loadWebUIHandledEventTypes(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := stripJSComments(string(src))

	opener := "const handlers = {"
	idx := strings.Index(text, opener)
	if idx < 0 {
		t.Fatalf("no `%s` in %s", opener, path)
	}
	body := text[idx+len(opener):]

	keys, err := parseJSObjectLiteralTopKeys(body)
	if err != nil {
		t.Fatalf("parse handlers in %s: %v", path, err)
	}

	if len(keys) < 40 {
		t.Fatalf("parsed only %d keys from handlers in %s; structure may have changed", len(keys), path)
	}
	return keys
}

// loadTaskViewHandledEventTypes parses static/js/taskview.js to extract all
// case labels in applyTaskEvent(ev)'s switch (ev.type) statement.
func loadTaskViewHandledEventTypes(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := stripJSComments(string(src))

	funcOpener := "function applyTaskEvent(ev)"
	idx := strings.Index(text, funcOpener)
	if idx < 0 {
		t.Fatalf("no `%s` in %s", funcOpener, path)
	}
	body := text[idx+len(funcOpener):]

	switchOpener := "switch (ev.type)"
	sidx := strings.Index(body, switchOpener)
	if sidx < 0 {
		t.Fatalf("no `%s` in %s", switchOpener, path)
	}
	switchBody := body[sidx+len(switchOpener):]

	casePattern := regexp.MustCompile(`^\s*case\s+['"]([^'"]+)['"]\s*:`)
	keys := make(map[string]bool)
	depth := 0
	for _, line := range strings.Split(switchBody, "\n") {
		for _, r := range line {
			if r == '{' {
				depth++
			} else if r == '}' {
				depth--
				if depth <= 0 {
					break
				}
			}
		}
		if m := casePattern.FindStringSubmatch(line); m != nil {
			keys[m[1]] = true
		}
		if depth <= 0 && strings.Contains(line, "}") {
			break
		}
	}

	if len(keys) < 15 {
		t.Fatalf("parsed only %d case labels from applyTaskEvent in %s; structure may have changed", len(keys), path)
	}
	return keys
}

// parseJSObjectLiteralTopKeys extracts keys defined at depth 0 inside an object literal.
// Keys may be quoted ('foo', "bar") or bare identifiers (usage, compacted).
func parseJSObjectLiteralTopKeys(body string) (map[string]bool, error) {
	keys := make(map[string]bool)
	keyPattern := regexp.MustCompile(`^\s*(?:'([^']+)'|"([^"]+)"|([A-Za-z_$][\w$]*))\s*:`)

	depth := 0
	for _, line := range strings.Split(body, "\n") {
		if depth == 0 {
			if m := keyPattern.FindStringSubmatch(line); m != nil {
				key := m[1]
				if key == "" {
					key = m[2]
				}
				if key == "" {
					key = m[3]
				}
				if key != "" {
					keys[key] = true
				}
			}
		}
		for _, r := range line {
			switch r {
			case '{', '[', '(':
				depth++
			case '}', ']', ')':
				if depth == 0 && r == '}' {
					return keys, nil
				}
				depth--
			}
		}
	}
	return nil, errors.New("unterminated object literal body")
}
