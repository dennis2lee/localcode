# Refactoring Plan

> **Finished.** All thirteen steps shipped in v0.31.0 and the commits just
> after it; the two items deliberately left undone are named in
> [Completion status](#completion-status) at the bottom. This is kept as the
> record of why the packages are shaped the way they are — the line counts and
> "currently" in the text below describe the codebase *before* the work, not
> now. Nothing here is outstanding.

This is an execution plan for a structural refactoring of the codebase, written
to be carried out phase by phase by a coding agent. The codebase grew feature
by feature over many releases; the goal is to restructure it so future changes
stop being risky, **without changing behavior** — except for the specific bugs
listed in [§1](#1-real-bugs-found-during-analysis), each of which gets its own
test.

## Ground rules (read first, apply to every phase)

1. **No behavior change** except the bugs in §1. Every phase must end with:
   ```
   gofmt -l .                      # must print nothing
   go vet ./...
   go test ./...
   GOOS=windows GOARCH=amd64 go build ./cmd/localcode   # pure-Go cross-compile must never break
   GOOS=linux   GOARCH=amd64 go build ./cmd/localcode
   ```
2. **One phase = one commit.** Never mix a mechanical file-move with a logic
   change in the same commit — a reviewer must be able to verify moves with
   `git diff --color-moved`.
3. Do **not** touch: `internal/gui` (CGo, build-tagged), `internal/childproc`
   (its `guard_test.go` walks the whole repo — new `exec.Command` call sites
   must keep using `childproc.Hide`), `build/` scripts, `.github/workflows/`.
4. No new dependencies. The Web UI stays build-step-free (native ES modules,
   embedded via the existing `//go:embed all:static`).
5. Preserve every doc comment when moving code. These comments carry design
   rationale (cache-prefix reasoning, race explanations) that must not be lost.
6. Phases are ordered by value/risk. A, B, C are independent of each other;
   D depends on A; E and F are independent of everything else.

---

## 1. Real bugs found during analysis

Fix each with a regression test **in the phase that touches that area** (noted
per item). These are the only intentional behavior changes in this plan.

| # | Bug | Where fixed |
|---|-----|-------------|
| B1 | `Config.merge` drops `MCPServers`, `Permissions`, `AutoDelegate`, `SkipPermissions` — a project-scope `.localcode/config.json` (e.g. written by `localcode mcp add --scope project`) is silently ignored whenever a global config exists | Phase A3 |
| B2 | `RemovePermissionRuleFromFile` is the only config writer missing `os.MkdirAll`; all five writers use non-atomic `os.WriteFile` at `0o644` on a file holding API keys and MCP auth headers | Phase A1 |
| B3 | TUI: an `error` event whose `Data["error"]` is not a string never clears `waiting` — the spinner hangs forever (`tui.go` ~line 941) | Phase E3 |
| B4 | TUI: while a turn is running, typing `/compact` (any slash command) or `exit` clears the input silently and does nothing — no queue, no message, and `exit` doesn't quit (`tui.go` ~line 699, `isPlainPrompt`) | Phase E3 |
| B5 | Web UI: `renderTasks` interpolates `t.agent` / `t.status` from SSE payloads into `innerHTML` without `escapeHtml` — the one unescaped sink in the file | Phase F2 |
| B6 | Web UI: `renderMarkdown`'s fenced-code placeholder is a bare ` N ` — model output containing ` 3 ` after a code block splices the wrong block (or the literal string `undefined`) into the transcript | Phase F2 |
| B7 | Web UI: `HELP_TEXT` mixes pre-escaped (`/&lt;skill name&gt;`) and raw (`/agent <name>`) entries, and is escaped again at render time, so some lines display double-escaped | Phase F4 |
| B8 | TUI: `agentsMsg`/`commandsMsg` fetch errors are swallowed with no `errMsg` and no transcript line — a failed `GET /api/agents` leaves Tab dead with no explanation | Phase E2 |
| B9 | CLI: the four hand-rolled `--scope` parsers disagree — `mcp add` rejects unknown flags, `mcp add-json`/`mcp remove` silently treat `--bogus` as a positional arg | Phase D2 |

---

## Phase A — `internal/config` (highest value, do first)

### A1. One shared read-modify-write primitive (fixes B2)

The surgical JSON read-modify-write skeleton is currently copy-pasted **five
times**: `UpdateMCPServersInFile` (config.go), `AddPermissionRuleToFile`,
`RemovePermissionRuleFromFile`, `SetSkipPermissionsInFile` (permission.go),
`updateAutoDelegateInFile` (permission.go). The copies have already drifted
(`RemovePermissionRuleFromFile` lacks `MkdirAll`; it alone has an extra
`default: return nil`). Replace all five bodies with one primitive in a new
`internal/config/rawfile.go`:

```go
// updateRawConfig rewrites path by parsing it as a raw JSON object, letting
// mutate change exactly the keys it cares about, and writing the result back
// atomically. Unknown keys survive untouched — that is the whole point: the
// config file belongs to the user, and a writer that round-trips it through
// the Config struct would silently drop every field the struct doesn't know.
//
// The write is temp-file + rename so a crash or full disk can never leave a
// truncated config.json, and the file is created 0o600 because it can hold
// provider API keys and MCP auth headers.
func updateRawConfig(path string, mutate func(raw map[string]json.RawMessage) error) error {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read config %s: %w", path, err)
	}

	if err := mutate(raw); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	out = append(out, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	return os.Rename(tmpName, path)
}
```

Then each public writer collapses to ~10 lines. Example — `SetSkipPermissionsInFile`:

```go
func SetSkipPermissionsInFile(path string, enabled bool) error {
	return updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		v, err := json.Marshal(enabled)
		if err != nil {
			return err
		}
		raw["skip_permissions"] = v
		return nil
	})
}
```

`updateAutoDelegateInFile` already has this shape one level down — rebase it
onto `updateRawConfig` instead of its own read/write code.

Note the `error` return in the mutate callback: it lets
`removeMCPServerFromFile` (cmd/localcode/mcp.go) abort with "not found"
**inside** the callback instead of parsing the file twice — change
`UpdateMCPServersInFile`'s signature from
`update func(map[string]MCPServerConfig)` to
`update func(map[string]MCPServerConfig) error` and update its three callers
(`mcpAdd`, `mcpAddJSON`, `removeMCPServerFromFile`, `mcpImportClaude`).

**Existing tests to keep green:** `auto_delegate_file_test.go` and
`permission_test.go` already pin exactly the survives-unknown-keys behavior —
they must pass unchanged. **Add:** a test that the written file has mode
`0o600` and that a mutate error leaves the original file byte-identical.

### A2. Deduplicate rule mutation between Runtime and InFile variants

The flat-to-rules promotion + dedupe + append logic exists twice
(`AddPermissionRuleRuntime` in config.go vs `AddPermissionRuleToFile` in
permission.go — the comment on the former literally says "mirrors
AddPermissionRuleToFile's rule-append logic"), and removal likewise. Extract
two pure functions both sides call:

```go
// addRule appends rule to m[tool], first promoting a legacy flat decision to
// an explicit "*" rule, and refusing exact duplicates so repeated approvals
// don't grow the list. Reports whether anything changed.
func addRule(m map[string]ToolPermission, tool string, rule PermissionRule) bool {
	tp := m[tool]
	if tp.Flat != "" {
		tp.Rules = []PermissionRule{{Match: "*", Decision: tp.Flat}}
		tp.Flat = ""
	}
	for _, existing := range tp.Rules {
		if existing.Match == rule.Match && existing.Decision == rule.Decision {
			return false
		}
	}
	tp.Rules = append(tp.Rules, rule)
	m[tool] = tp
	return true
}

func removeRule(m map[string]ToolPermission, tool string, rule PermissionRule) bool
```

In `removeRule`, build a **fresh slice** instead of the current
`kept := tp.Rules[:0]` in-place filter — the in-place version aliases the
caller's backing array, which is safe today only because `PermissionsSnapshot`
copies; don't leave that landmine.

### A3. Fix `Config.merge` (fixes B1)

`merge` is a hand-maintained field list that has already fallen behind the
struct: it copies Providers, Profiles, Agents, DefaultProfile,
MaxConcurrentTasks, AutoMemoryEnabled, AutoCompactEnabled, ShowTPS, Hooks —
and silently drops `MCPServers`, `Permissions`, `AutoDelegate`,
`SkipPermissions`.

1. Add the four missing fields (maps merge key-wise project-over-global, same
   as Providers; `AutoDelegate` and `SkipPermissions` are project-wins-if-set).
2. Replace the three near-identical map-copy blocks with one generic helper:
   ```go
   func mergeMap[K comparable, V any](dst *map[K]V, src map[K]V) {
   	if len(src) == 0 {
   		return
   	}
   	if *dst == nil {
   		*dst = map[K]V{}
   	}
   	for k, v := range src {
   		(*dst)[k] = v
   	}
   }
   ```
3. **Add the guard test** so the list can never fall behind again: a
   reflection test that iterates `reflect.TypeOf(Config{})`'s exported fields
   and fails for any field not present in a hand-maintained
   `mergedFields`/`intentionallyNotMerged` set in the test. Adding a Config
   field then forces a conscious decision.
4. Regression test for B1 itself: global config with providers + project
   config with only `mcp_servers` → `LoadMerged` must surface the project MCP
   server. (This is the `localcode mcp add --scope project` scenario.)

### A4. Split the package into focused files (mechanical)

`config.go` (678 lines) + `permission.go` (585 lines) become:

| New file | Contents (moved verbatim) |
|---|---|
| `config.go` | `Config` DTO, `Profile`, `AgentConfig`, `ProviderConfig`, `Validate`, `Resolve*` |
| `load.go` | `DefaultGlobalPath`, `Load`, `LoadMerged`, `LoadFile`, `loadOptional`, `merge` + `mergeMap` |
| `rawfile.go` | `updateRawConfig` + the five `*InFile` writers |
| `runtime.go` | everything guarded by `permMu`/`delegateMu`: `PermissionsSkipped`, `Set*Runtime`, `Add/RemovePermissionRuleRuntime`, `PermissionsSnapshot`, `AutoDelegateSnapshot`, `SetAutoDelegateRuntime` |
| `rules.go` | `addRule`, `removeRule` (from A2), `ToolPermission`, `PermissionRule`, `Decision`, `resolve` |
| `shell.go` | `resolveShellCommand`, `hasUnsafeShellConstruct`, `splitShellSegments`, `globMatch` |
| `mcpserver.go` | `MCPServerConfig`, `Transport()`, `IsRemote()`, `Validate()`, the `MCPTransport*` constants (~90 self-contained lines) |

### A5. Stop compiling a regexp per glob match

`globMatch` builds and compiles a regexp on **every call**, and it is on the
hot path: `resolveShellCommand` calls it per shell segment × per rule (a
5-segment command against 10 rules = 50+ `regexp.Compile` calls per tool
call), and `AutoDelegateConfig.MatchesPrompt` per pattern per prompt. Replace
with a direct two-pointer matcher (no regexp import needed for `*`/`?`
semantics):

```go
// globMatch reports whether subject matches pattern, where '*' matches any
// run of characters (including none) and '?' matches exactly one. Direct
// two-pointer walk with the classic star-backtrack — no regexp, no
// compilation on the hot path (this runs per shell segment × per rule on
// every tool call).
func globMatch(pattern, subject string) bool {
	p, s := 0, 0
	starP, starS := -1, 0
	for s < len(subject) {
		switch {
		case p < len(pattern) && (pattern[p] == subject[s] || pattern[p] == '?'):
			p++
			s++
		case p < len(pattern) && pattern[p] == '*':
			starP, starS = p, s
			p++
		case starP >= 0:
			starS++
			p, s = starP+1, starS
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
```

The existing `permission_test.go` glob cases pin the semantics; run them
against the new implementation before deleting the regexp version. Add cases
for `**`, a trailing `*`, `?` at string end, and empty pattern/subject.

---

## Phase B — split `internal/agent/loop.go` (1144 lines)

`loop.go` currently holds five separable concerns. This phase is mostly
mechanical moves plus one small dispatch-table refactor.

### B1. File split (mechanical)

| New file | Moves in |
|---|---|
| `loop.go` | package doc, constants, `Loop`, `New`, `ClearSessionState`, the settings getters/setters (`AutoCompactEnabled`…`SetProjectDir`), `appendHistory`/`history`/`setHistory` |
| `commands.go` | `SendMessage`'s command routing + all local command handlers: `parseSkillCommand`, `listSkills`, `showMemoryInfo`, `parseConfigCommand`, `handleConfigCommand`, `knownSetting`, `configSummary`, `onOff`, `parseCompactCommand`, `handleCompactCommand`, `handleCostCommand`, `matchCustomCommand`, `matchSkillName`, `skillModelText`, `findSkill`, `skillNames` |
| `turn.go` | `sendWithModelText`, `consumeStream`, `streamUsage`, `runTools`, `drainText` |
| `delegate.go` | `delegateTarget`, `delegatePrompt`, `sessionAgent`, `appendDelegatedTurn` |
| `compact.go` | `compactThresholdPercent`, `compactionPrompt`, `maybeAutoCompact`, `compactHistory` |
| `usage.go` | `sessionUsage`, `modelTotals`, `recordUsage`, `getUsage`, `clearUsage`, `addCumulativeUsage` |

The existing `rehydrate.go`, `permission.go`, `taskmanager.go`, `task_tool.go`
already follow this pattern — this just finishes the job.

### B2. Extract the repeated "local reply" triple

Every local command handler ends with the same three appends, written out
seven times (`listSkills`, `showMemoryInfo`, `handleConfigCommand`,
`handleCompactCommand`, `handleCostCommand`, plus the error paths in
`SendMessage`). Extract:

```go
// replyLocal records a command the user typed and the locally computed answer
// — no model call. The delta/end pair mirrors how a streamed model reply
// lands in the log, so clients render local answers with zero special cases.
func (l *Loop) replyLocal(sessionID, displayText, answer string) error {
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})
	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": answer})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": answer})
	return nil
}
```

(Handlers that append the user message early and compute afterwards — like
`handleCompactCommand` — get a second helper `replyText(sessionID, answer)`
for just the delta/end pair.)

### B3. Turn `SendMessage`'s if-ladder into a routing table

`SendMessage` is a 90-line ladder of `if parseX(text)` checks whose **order is
load-bearing** (built-ins beat custom commands beat skills beat delegation —
the comment at the skill-match site says exactly this). Make the order a data
structure so the next command can't be inserted in the wrong place:

```go
// commandRoutes is consulted in order; the first match wins. Order is the
// precedence contract: built-in commands, then custom commands, then skills,
// then auto-delegation, then an ordinary model turn — nothing user-facing can
// be shadowed by a later entry.
type commandRoute struct {
	match func(l *Loop, text string) (handler func(ctx context.Context, sessionID, agentName, text string) error, ok bool)
}
```

Keep it simple: this can be a `[]func(...) (handled bool, err error)` slice
walked in a loop — the shape matters less than making `SendMessage` itself a
~20-line "walk routes, else default turn" function. Existing tests
(`skill_command_test.go`, `custom_command_test.go`, `config_command_test.go`,
`memory_command_test.go`, `delegate_test.go`) pin the precedence; they must
pass unchanged.

### B4. Group the runtime settings

Three settings booleans + their six accessors clutter `Loop`. Move them into
one small mutex-guarded struct in `loop.go`:

```go
// liveSettings are the process-global toggles "/config" flips at runtime.
type liveSettings struct {
	mu           sync.Mutex
	autoCompact  bool
	showTPS      bool
	autoDelegate bool
}
```

`Loop` keeps thin forwarding methods (the daemon and TUI call them), but the
lock scope becomes obvious and `Loop.mu` stops guarding both history maps and
settings booleans (today one mutex covers unrelated state; after the split
`Loop.mu` guards only `messages`/`usage`/`cumulativeUsage`).

---

## Phase C — split `internal/daemon/daemon.go` (959 lines)

### C1. Extract the busy/cancel pair into a type

`busy map[string]bool` + `cancels map[string]context.CancelFunc` are guarded
by one mutex and must never disagree — that invariant currently lives in a
comment. Make it a type in a new `internal/daemon/turns.go`:

```go
// turnTracker records which sessions have a turn in flight and how to cancel
// each one. busy and cancel always change together under one lock, so the two
// can never disagree about whether a turn is running.
type turnTracker struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// begin registers a turn for id. It reports false (and registers nothing)
// when one is already running — the caller turns that into a 409.
func (t *turnTracker) begin(id string, cancel context.CancelFunc) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, running := t.cancels[id]; running {
		return false
	}
	t.cancels[id] = cancel
	return true
}

func (t *turnTracker) end(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.cancels, id)
}

// cancel stops id's turn if one is running, and reports whether it was.
func (t *turnTracker) cancel(id string) bool {
	t.mu.Lock()
	c, running := t.cancels[id]
	t.mu.Unlock()
	if running {
		c()
	}
	return running
}

func (t *turnTracker) busy(id string) bool
func (t *turnTracker) anyBusy(ids []string) []string   // for delete-all / workspace-switch guards
```

This removes the separate `busy` map entirely (a session is busy iff it has a
cancel registered) and shrinks `handleSendMessage`, `handleCancelTurn`,
`handleDeleteSession`, `handleDeleteAllSessions`, `handleSetWorkspace` to
calls on the tracker. **Preserve the two ordering comments** in
`handleSendMessage` (read `turnCtx.Err()` before `cancel()`; clear busy before
appending the terminal event) — move them onto the new call sites.

### C2. File split (mechanical)

| New file | Handlers |
|---|---|
| `daemon.go` | `Daemon`, `New`, `routes`, `Handler`, `writeJSON`, `writeError`, `WebFS`, `handleVersion` |
| `turns.go` | `turnTracker` (C1), `handleSendMessage`, `handleCancelTurn` |
| `sessions.go` | `handleCreateSession`, `handleListSessions`, `handleGetSession`, `handleDeleteSession`, `handleDeleteAllSessions`, `handleSwitchAgent`, `handleRenameSession`, `AgentInfo`, `handleListAgents`, `CommandInfo`, `handleListCommands` |
| `settings.go` | `handleGetSettings`, `handleSetAutoDelegate`, `handleSetSkipPermissions`, `handleAddPermissionRule`, `handleRemovePermissionRule`, `permissionRuleRequest` |
| `workspace.go` | `handleGetWorkspace`, `handleSetWorkspace`, `handleBrowseWorkspace` |
| `sse.go` | `handleEvents` |
| `uploads.go` | `maxUploadBytes`, `handleUploadFile` |
| `tasks.go` | `handleSpawnTask`, `handleListTasks`, `handleCancelTask`, `handleTaskOutput`, `handleResolvePermission` |

`daemon_test.go` (1357 lines) can be split along the same lines later; not
required in this phase.

---

## Phase D — `cmd/localcode` (depends on A1's signature change)

### D1. Subcommand dispatch table in `main.go`

Three copy-pasted `os.Args` blocks (plus `version` existing both as a
subcommand and as `--version`) become:

```go
var subcommands = map[string]func(args []string) error{
	"login":   runLogin,
	"mcp":     runMCP,
	"version": func([]string) error { fmt.Println(version); return nil },
}

func main() {
	if len(os.Args) > 1 {
		if cmd, ok := subcommands[os.Args[1]]; ok {
			if err := cmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		}
	}
	run()
}
```

Also rename the flag variable `gui` → `useGUI`: it currently shadows the
`localcode/internal/gui` package import for the rest of `run()`.

### D2. One shared flag parser for the `mcp` subcommands (fixes B9)

Four hand-rolled `--scope` loops (two byte-identical, two divergent) become
one helper in `cmd/localcode/flags.go`:

```go
// parseScope pulls -s/--scope out of args and returns the remaining
// positional arguments. Unknown flags are an error in every subcommand —
// previously mcp add rejected them while add-json/remove silently treated
// them as positionals.
func parseScope(args []string) (scope string, rest []string, err error) {
	scope = "global"
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-s" || a == "--scope":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--scope requires an argument (global|project)")
			}
			scope = args[i+1]
			i++
		case strings.HasPrefix(a, "-") && a != "-":
			return "", nil, fmt.Errorf("unknown flag %q", a)
		default:
			rest = append(rest, a)
		}
	}
	if scope != "global" && scope != "project" {
		return "", nil, fmt.Errorf("invalid scope %q (global|project)", scope)
	}
	return scope, rest, nil
}
```

(`mcp add` additionally needs `-e`/`--env`, `-H`/`--header`, `--transport` —
give it its own loop built on the same shape, or extend the helper with a
`map[string]*[]string` for repeatable flags. Either is fine; the requirement
is that *one* parser is shared by `add-json`/`remove`/`import-claude`, and all
of them reject unknown flags.)

### D3. Extract the shared save-and-report helper

`mcpAdd` and `mcpAddJSON` duplicate the same 10-line
overwrite-warn-update-print block, with the warning printed from **inside**
the mutate callback (so it fires even if the write then fails). Replace with:

```go
// saveMCPServer writes name->sc into the config at path and reports whether
// an existing entry was overwritten — decided inside the update so the check
// and the write are one atomic read-modify-write, but *printed* by the
// caller, after the write has actually succeeded.
func saveMCPServer(path, name string, sc config.MCPServerConfig) (overwrote bool, err error) {
	err = config.UpdateMCPServersInFile(path, func(servers map[string]config.MCPServerConfig) error {
		_, overwrote = servers[name]
		servers[name] = sc
		return nil
	})
	return overwrote, err
}
```

### D4. File split of `mcp.go` (744 lines, mechanical)

| New file | Contents |
|---|---|
| `mcp.go` | dispatch + usage text only |
| `mcp_add.go` | `mcpAdd`, `mcpAddJSON`, `saveMCPServer` |
| `mcp_list.go` | `mcpList`, `checkMCPServer`, `mcpGet` |
| `mcp_remove.go` | `mcpRemove`, `removeMCPServerFromFile` (rewritten on the error-returning callback from A1 — single file read, aborts with "not found" from inside the callback) |
| `mcp_import_claude.go` | `claudeMCPEntry`, `claudeUserConfig`, `claudeProjectMCPFile`, `collectClaudeMCPServers`, `mcpImportClaude` (matching `mcp_import_test.go` already exists) |
| `mcp_format.go` | `plural`, `oneLine`, `formatMCPCommand`, `sortedKeys`, `scopeLabel` |
| `flags.go` | `parseScope` (D2) |

In `mcpList`, separate computation from rendering: build
`[]serverRow{name, scope, status string}` first, then one render loop — the
column-width computation and `%-*s` formatting end up in one place.

### D5. Break up `buildDaemon` (123 lines) into `wire.go`

Resolve ambient inputs **once** — today `os.UserHomeDir` is called in two
places and `os.Getwd` in three, each with its own error handling:

```go
// env is the ambient machine state resolved exactly once at startup and
// passed down, instead of each builder re-deriving (and re-error-handling)
// it.
type env struct {
	home string
	cwd  string
}
```

Split `buildDaemon` into composable steps, each testable alone:

- `buildRegistry(cfg *config.Config, broker *agent.PermissionBroker) (*tools.Registry, error)` — the six `Register` calls
- `buildSystemPrompt(cfg, e env) (prompt string, sk []skills.Skill, cmds []commands.Command, err error)` — skills/commands/rules/memory loading + prompt concatenation
- `buildDaemon` composes them and stays under ~40 lines

Have the MCP branch return a cleanup func and finally call the currently
never-called `mcp.Manager.Close` on shutdown (the existing comment in main.go
acknowledges the gap):

```go
cleanup := func() {}
if mcpManager != nil {
	cleanup = func() { _ = mcpManager.Close() }
}
```

Move the four `runX` mode functions to `modes.go` (fixing the doc comment that
belongs to `runEmbedded` but is attached to `runGUI`), and
`pickOrCreateSession` (a 57-line stdin loop) to `session_picker.go` with an
injected `io.Reader`/`io.Writer` and a pure `parseChoice(line string, n int)`
so the `d<N>`/`da` mini-language gets unit tests.

---

## Phase E — `internal/tui/tui.go` (1016 lines)

The file is currently **not gofmt-clean** (misaligned `Model` fields) — run
`gofmt -w internal/tui/` as commit zero of this phase so later diffs are real.

### E1. Mechanical file split

`Update` is a 277-line type-switch whose `tea.KeyMsg` case contains a second
149-line switch whose `"enter"` case contains a third. Split by concern; all
line refs are to the current file:

| New file | Moves in |
|---|---|
| `tui.go` | styles, layout consts, `Model`, `New`, `Init` (~130 lines) |
| `messages.go` | the 10 `...Msg` types, `spinFrames`, `spinTick` |
| `update.go` | `Update` reduced to a dispatcher: `case tea.KeyMsg: return m.updateKey(msg)` etc. |
| `keys.go` | the key handlers (`ctrl+c`, `esc`, `y/n/s/a`, `up`, `down`, `tab`) |
| `submit.go` | the whole `"enter"` case → `handleSubmit`, plus `isPlainPrompt`, `dequeue` |
| `events.go` | `applyEvent` |
| `history.go` | `rememberPrompt`, `atInputTop/Bottom`, `setInputTo`, `historyPrev/Next` + the `history`/`historyIdx`/`draft` fields as a `promptHistory` struct |
| `permission.go` | `pendingPermission`, its key handling, `resolvePermission`, the `TypePermissionRequest` arm |
| `tasks.go` | `taskState`, `activeTasks`, `tasksSummary`, `fetchTaskOutput`, task event arms |
| `api.go` | `listenForEvent` + the eight `tea.Cmd` wrappers |
| `layout.go` | `resizeLayout`, `scrollInputToTop`, the `WindowSizeMsg` case |
| `view.go` | `inputBorder`, `View`, `busyLine` |

### E2. Collapse the eight identical `tea.Cmd` wrappers (fixes B8)

All eight wrappers are the same closure shape and all hardcode
`context.Background()` (a hung daemon hangs the goroutine forever). Replace
with one generic helper:

```go
// call runs one client call off the Update goroutine with a timeout — a hung
// daemon must never wedge a tea.Cmd goroutine forever — and wraps the result
// in the given message constructor.
func call[T any](fn func(context.Context) (T, error), wrap func(T, error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return wrap(fn(ctx))
	}
}
```

While here, fix B8: `agentsMsg`/`commandsMsg` error branches must set
`m.errMsg` (or append a transcript line) instead of silently dropping the
error. Also collapse the three byte-identical err-only message cases
(`permissionResolvedMsg`, `turnCancelledMsg`, `switchAgentMsg`) into one
`opErrMsg{op string; err error}`.

### E3. Single turn-teardown point (fixes B3, B4)

The `waiting=false; runningTool=""` pair is written three times in
`applyEvent`, and the `TypeError` arm only performs it **inside** the
string-assertion `if` — a non-string error hangs the spinner forever:

```go
// endTurn clears everything that means "a turn is running". Called
// unconditionally for every turn-terminating event — including an error
// event whose payload is malformed, which previously left the spinner
// running forever.
func (m *Model) endTurn() {
	m.waiting = false
	m.runningTool = ""
}
```

```go
case events.TypeError:
	m.endTurn()
	if msg, ok := ev.Data["error"].(string); ok {
		m.errMsg = msg
	} else {
		m.errMsg = "the daemon reported an error with a malformed payload"
	}
```

For B4, `handleSubmit`'s waiting branch gets an `else`:

```go
if m.waiting {
	if isPlainPrompt(text) {
		m.queue = append(m.queue, text)
		m.appendLocal(fmt.Sprintf("[queued] %s", text))
	} else {
		// Commands can't be queued — saying so beats silently eating them,
		// and `exit` still has to work while a turn is running.
		if lower := strings.ToLower(text); lower == "exit" || lower == ":q" {
			return m, tea.Quit
		}
		m.appendLocal(fmt.Sprintf("%s can't run while a turn is in progress — wait or press Esc to cancel.", text))
	}
	...
}
```

### E4. Slash-command table (replaces the switch + if-ladder)

Local command dispatch is currently a lowercased `switch` plus a
case-**sensitive** `CutPrefix` ladder (`/Agent foo` falls through to the model
as chat text), and the 23-line `helpText` const documents commands the table
doesn't implement — two sources of truth. Replace both with one table that
also generates the help text:

```go
// localCommand is a slash command the TUI answers without the daemon.
type localCommand struct {
	name     string // matched case-insensitively, with or without an argument
	takesArg bool
	help     string
	run      func(m *Model, arg string) (Model, tea.Cmd)
}

var localCommands = []localCommand{
	{name: "/help", help: "show this help", run: ...},
	{name: "/version", help: "show client and daemon versions", run: ...},
	{name: "/agent", takesArg: true, help: "list agents, or switch with /agent <name>", run: ...},
	{name: "/commands", help: "list custom commands", run: ...},
	{name: "/tasks", takesArg: true, help: "list background tasks, or show one's output", run: ...},
}
```

Dispatch lowercases the command word once; `helpText` becomes a function that
ranges over the table (plus a hardcoded section for daemon-side commands like
`/skill`, `/compact`, `/usage`). Also extract the thrice-duplicated
"render a name/description list" helper used by `/agent`, `/commands`, and
`tasksSummary`.

### E5. Structured transcript (last, riskiest TUI step — lands alone)

The transcript is a `string` of pre-rendered ANSI, which is why
`refreshViewport` re-wraps already-styled text and why every event triggers a
full re-render + `GotoBottom()` (the user can't scroll back during a stream).
Replace with:

```go
type entryKind int

const (
	entryUser entryKind = iota
	entryModel
	entryTool
	entryError
	entryLocal
)

type transcriptEntry struct {
	kind entryKind
	text string
}
```

All `lipgloss` styling moves into `view.go`, which renders `[]transcriptEntry`
at the current width. `refreshViewport` becomes conditional (only events that
change visible text re-render), and `GotoBottom` only fires if the viewport
was already at the bottom. This is the only E-step that changes `View` output
byte-for-byte, so it must be its own commit, manually verified against the
mock daemon (see §Verification).

Not in scope for this plan (record in docs/IMPROVEMENTS.md): making the TUI's
key handlers return an explicit `handled bool` (today some cases deliberately
fall through to the textarea and nothing marks which), Esc-dismiss for the
permission modal, and moving `pickOrCreateSession` into the TUI as a Bubble
Tea sub-model.

---

## Phase F — Web UI (`internal/daemon/static/index.html`, 1958 lines)

The daemon side needs **zero Go changes**: `//go:embed all:static` already
embeds new files, and `http.FileServerFS` serves `.js` as `text/javascript`,
so native ES modules work with no bundler. The offline, single-binary property
is preserved.

### F0. Mechanical extraction

```
internal/daemon/static/
  index.html      (~120 lines: markup + <link rel=stylesheet> + <script type=module>)
  style.css       (the 419-line <style> block, verbatim)
  js/main.js      (the 1417-line <script> body; drop the IIFE wrapper — modules are scoped)
```

Verify by loading the page against the mock daemon and diffing rendered
behavior. This alone takes the largest file in the repo from 1958 to ~120
lines.

### F1. Pure modules first: `js/markdown.js`, `js/format.js`

Move `escapeHtml`, `renderMarkdown`, `inline` into `js/markdown.js`;
`formatTime`, `shortenPath` into `js/format.js`. Zero DOM dependencies.

**Fix B6 here**: the fenced-code placeholder must be a sentinel the model
cannot type. Replace:

```js
// before: return ` ${blocks.length - 1} `;   ...   / (\d+) /g
return `\u0000${blocks.length - 1}\u0000`;
// ...
text = text.replace(/\u0000(\d+)\u0000/g, (_, i) => blocks[+i] ?? '');
```

### F2. Typed transcript constructors (fixes B5)

Replace `appendLine(htmlString)` — which makes all ~25 call sites individually
responsible for escaping — with constructors that only accept text:

```js
// js/transcript.js — the ONLY module allowed to touch transcriptEl.
// Everything goes through createElement/textContent; no call site ever
// builds HTML strings again, which is what closes the escape-a-string
// class of bug for good.
export function appendUser(text)   { appendDiv('msg-user', 'You: ' + text); }
export function appendTool(text)   { appendDiv('msg-tool', text); }
export function appendError(err)   { appendDiv('msg-error', 'Error: ' + String(err)); }

function appendDiv(cls, text) {
  const div = document.createElement('div');
  div.className = cls;
  div.textContent = text;
  transcriptEl.appendChild(div);
  transcriptEl.scrollTop = transcriptEl.scrollHeight;
}
```

(`appendModelText`/`endModelText` keep using `renderMarkdown` — that is the
one deliberate HTML sink, and it escapes internally.)

Rewrite `renderTasks` with `createElement`/`textContent` (B5) — it is the one
list renderer still interpolating SSE payload fields into `innerHTML`;
`renderMCPServers` and `renderSessionList` already do it safely.

### F3. `js/api.js` — one HTTP layer

Move `api()` there, add named endpoint wrappers (`sendMessage(id, text)`,
`listSessions()`, `setAutoDelegate(patch)`, …) so URL templates stop being
scattered across 20 template literals. Add:

```js
export class ApiError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}
export const isBusy = (err) => err instanceof ApiError && err.status === 409;
```

Rewrite `uploadFile` on the same wrapper (it currently reimplements fetch +
error handling because of `FormData` — give `api()` an option that skips the
JSON headers/body encoding instead). Collapse the ~14 copy-pasted
`catch { appendLine('<div class="msg-error">…') }` tails into
`appendError(err)` from F2, and unify the **two divergent 409-requeue paths**
(`sendMessage` vs `dequeueNext`) into one `enqueue/flush` pair in the composer
module so the POST to `/messages` is issued from exactly one place.

### F4. State object + SSE handler table (fixes B7 alongside)

Group the 27 top-level `let` bindings into two objects with explicit
ownership:

```js
// Everything here is per-session and MUST reset on session switch — the
// object being the reset unit is the point: a new field added here is
// automatically cleared by selectSession, instead of depending on someone
// remembering to add a line to a 12-field manual reset.
export function freshSessionState(id) {
  return {
    sessionID: id,
    waiting: false,
    tasks: new Map(),
    lastUsage: null,
    promptQueue: [],
    history: [], historyIdx: 0, historyDraft: '',
    runningTool: '',
    pendingPermissionID: null, pendingPermissionCanAlways: false,
    currentModelEl: null, currentModelBuffer: '',
  };
}
// app-scoped: agents, customCommands, sessions, mcpServers, settings,
// workspacePath, connected — lives across session switches.
```

`selectSession`'s hand-written 12-global reset becomes
`state = freshSessionState(id)`.

Replace the 105-line `applyEvent` switch with a handler table, one small
function per event type, each guarded against a missing `ev.data` (today
`ev.data.name`/`ev.data.id` are dereferenced unguarded and a malformed event
aborts the whole handler via the generic `console.error`):

```js
const handlers = {
  'message.part.delta': (d) => appendModelDelta(d.text ?? ''),
  'turn.done':          ()  => { setWaiting(false); },
  'permission.request': (d) => permissionModal.open(d),
  'config.changed':     (d) => applySettingsPatch(d),
  // ...
};

function applyEvent(ev) {
  const h = handlers[ev.type];
  if (!h) return;
  h(ev.data ?? {});
  renderStatusBar(); // one place, instead of 8 hand-picked call sites
}
```

Fix B7 here or in the same commit: `HELP_TEXT` becomes structured data
(`[{cmd, desc}]`) rendered through the F2 text-only constructors — the mixed
pre-escaping and the double-escape disappear together. `tryLocalCommand`'s
four near-identical if-blocks become entries in the same table pattern as the
TUI's E4.

### F5. Modals and renderers (last)

One module per modal (`permission`, `permissionSettings`, `delegate`,
`workspace`), each owning an explicit `isOpen` flag — removing the
`delegateModal.classList.contains('open')` DOM-as-state check — and one
render module per pane (`statusbar`, `sessions`, `tasks`, `mcp`, `agents`).
Merge the near-duplicate `saveWorkspace`/`applyWorkspace`. In `init()`, the
six sequential `await loadX()` calls become `Promise.all` once the loaders no
longer share the render fan-out.

**Manual verification is mandatory for F** (there are no JS tests): run the
`cmd/manualverify` mock-daemon harness, open the Web UI in the Browser pane,
and walk: send/stream a message, tool event rendering, markdown with fenced
code containing a bare number after a code block (B6), permission modal
allow/always, session switch (state reset), agent Tab-cycle, auto-delegate
settings save, workspace picker, task spawn rendering (B5 — verify with a
task agent name containing `<b>x</b>`), `/help` output (B7), drag-drop
upload, 409 requeue (send two prompts fast), SSE reconnect (restart daemon).

---

## Phase G — small items, any time

- `internal/mcp/mcp.go` is largely clean. Two touch-ups: sort server names
  before iterating in `Connect` so startup warnings and tool registration
  order are deterministic (matters for tests and log diffing), and extract
  `Manager.add(name, sc, session)` so `Connect` reads as a loop. Hook
  `Manager.Close` into the D5 cleanup func.
- `internal/tools`, `internal/session`, `internal/provider`,
  `internal/events`, `internal/skills`, `internal/hooks` need no structural
  work — do not churn them.

## Suggested execution order

| Step | Phase | Risk | Verification gate |
|---|---|---|---|
| 1 | A1+A2 (rawfile primitive, rule dedupe, B2) | low | config tests + new 0600/atomicity test |
| 2 | A3 (merge fix B1 + reflection guard test) | low | new merge tests |
| 3 | A4+A5 (file split, glob matcher) | low | full suite |
| 4 | B1–B4 (loop.go split + table + replyLocal) | medium | all agent tests unchanged |
| 5 | C1+C2 (turnTracker + daemon split) | medium | daemon_test.go unchanged |
| 6 | D1–D5 (cmd/localcode) | medium | mcp tests + new parseScope/parseChoice tests |
| 7 | E gofmt + E1 (TUI split) | low | tui_test.go unchanged |
| 8 | E2–E4 (call helper, endTurn, command table; B3/B4/B8) | medium | new regression tests |
| 9 | F0–F1 (extract css/js, markdown module; B6) | low | manual browser walk |
| 10 | F2–F3 (transcript constructors B5, api.js) | medium | manual browser walk |
| 11 | F4–F5 (state, SSE table, modals; B7) | medium | full manual browser walk (list above) |
| 12 | E5 (structured transcript) | high | manual TUI session against mock daemon |
| 13 | G | low | full suite |

Steps 1–8 are pure Go and fully covered by the existing test suite (~5,900
lines of tests across the touched packages) — trust it. Steps 9–12 change UI
rendering paths with no automated coverage — the manual walks are not
optional.

After the final step: update `docs/CHANGELOG.md` (an "Internal" section —
user-visible fixes B1–B9 get their own lines), and run
`scripts/release-preflight.sh` before any release build, as always.

---

## Completion status

All thirteen steps are done, and B1–B9 are all fixed with a regression test
each. Shipped in v0.31.0 (steps 1–13) and the follow-up commits after it
(the D5 and F5 leftovers listed below).

Two items were deliberately **not** carried out as written:

- **E5's `entryError` kind.** The enum in the plan lists five kinds, but the
  TUI has never put errors in the transcript — an error sets `errMsg`, which
  `View` renders in the footer and clears at the next turn. Adding the kind
  would have meant either an unused constant or moving errors into the
  transcript, and the latter is a behavior change ground rule 1 doesn't
  allow. The transcript has four kinds.
- **F5's one-module-per-modal split.** The four modals share ~180 lines of
  state and helpers and would have become four files of 40 lines each with
  cross-imports between them. The substantive half of the item — replacing
  the `classList.contains('open')` DOM-as-state reads with an explicit
  `isOpen` flag — is done, in `js/modal.js`, and `test/webui/modals.test.js`
  has a guard test that fails if a `contains('open')` check reappears
  anywhere under `static/js/`. The renderers likewise stayed in one
  `js/render.js` rather than one module per pane.

The mandatory manual walks in §F and §E5 were partly replaced by automated
coverage that did not exist when this plan was written: F0–F2 got the full
browser walk as specified, and F3–F5 and E5 are covered by the 90-test JS
suite (`make test-js`) and by Go tests driving the real `viewport.Model`
scroll API. Anything touching visual layout still wants an eye on it.
