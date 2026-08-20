# Usage

## Contents

| Part | Sections |
|---|---|
| [1. Getting started](#part-1-getting-started) | [Run modes](#run-modes), [Remote daemon over an SSH tunnel](#remote-daemon-over-an-ssh-tunnel) |
| [2. Configuration](#part-2-configuration) | [Config file (config.json)](#config-file-configjson), [Managing MCP servers](#managing-mcp-servers-with-localcode-mcp), [Permission rules](#fine-grained-permission-rules), [Permission settings panel](#viewing-and-changing-permission-settings-without-waiting-for-a-prompt), [Switching the workspace directory](#switching-the-workspace-directory), [Hooks](#hooks), [Authenticating with /login](#authenticating-with-login) |
| [3. Project context](#part-3-project-context) | [Skills](#skills), [AGENTS.md](#agentsmd-project-rules), [Auto memory](#auto-memory) |
| [4. Commands and screen controls](#part-4-commands-and-screen-controls) | [Screen controls](#screen-controls), [Running a skill](#running-a-skill), [/init](#init), [Custom commands](#custom-commands), [/tasks](#tasks), [/memory](#memory), [/config](#config), [/compact](#compact), [/usage](#usage), [Other local commands](#other-local-commands) |
| [5. Sessions](#part-5-sessions) | [Switching sessions](#switching-sessions), [Rename and delete](#renaming-and-deleting-sessions), [Context window](#context-window-management), [Session logs](#session-logs), [Restart recovery](#daemon-restart-and-session-recovery) |
| [6. Web UI](#part-6-web-ui) | [Resizing and hiding the panels](#resizing-and-hiding-the-side-panels), [Left panel: sessions](#left-panel-sessions), [Right panel](#right-panel), [Drag and drop attach](#drag-and-drop-file-attach), [Status bar](#status-bar-under-the-prompt), [Switching agents with Tab](#switching-agents-with-tab), [Markdown rendering](#model-output-renders-as-markdown), [Watching a long turn](#watching-a-long-turn), [Redirecting a turn](#redirecting-a-turn-while-it-runs) |
| [7. Agents and automation](#part-7-agents-and-automation) | [Available tools](#available-tools), [Combining agents](#combining-agents), [Plan mode](#plan-mode), [Auto delegation](#auto-delegation), [Background tasks](#background-tasks), [Switching models](#switching-models), [Local LLMs](#attaching-a-local-llm) |
| [Known limitations](#known-limitations) | |

## Part 1. Getting started

### Run modes

```bash
localcode --agent general-purpose
```

| Flag | Default | Meaning |
|---|---|---|
| `--config <path>` | none | Use only this file as config. Without it, `~/.localcode/config.json` and `./.localcode/config.json` are merged, with the project file winning. |
| `--agent <name>` | `general-purpose` | Which model profile to use, resolved through the `agents` map in config |
| `--listen <host:port>` | `127.0.0.1:4096` | Address the daemon binds. The Web UI is served here too. |
| `--server <url>` | none | Do not start a local daemon. Attach the TUI to an already running daemon, which may be remote. |
| `--headless` | `false` | Run the daemon alone with no TUI, exposing the HTTP API and Web UI |
| `-version`, `--version` | `false` | Print the build version and exit |

`localcode version` works the same as `-version`.

Three useful combinations:

| Command | What it does |
|---|---|
| `localcode` | Starts a local daemon and attaches the TUI. Open `http://127.0.0.1:4096` in a browser to use the Web UI on the same sessions at the same time. |
| `localcode --headless --listen 0.0.0.0:4096` | Daemon only. Meant for a remote server. |
| `localcode --server http://host:4096` | TUI only, attached to a daemon that is already running. |
| `localcode-gui` | A native desktop window instead of the TUI. Experimental, built with `-tags gui`, opens by default on such a build (no `--gui` needed). See [Desktop window](#desktop-window-experimental). |

### Desktop window (experimental)

Instead of the TUI or a browser, localcode can open its Web UI in a native desktop window, so it is one app to launch rather than a server to start and a browser tab to open.

```bash
./localcode-gui
```

A `-tags gui` build defaults `--gui` to on, so no flag is needed — running the binary opens the window directly. Pass `--gui=false` to force the TUI on that same build instead.

It starts the daemon in-process on a private loopback port and shows the same Web UI in an OS native window (WKWebView on macOS, WebView2 on Windows). Nothing is exposed off the machine and there is no fixed port to collide with.

**Startup screen.** The window opens immediately, before the daemon exists, showing the app icon and a status line naming the step in progress — reading config, opening providers, loading sessions, connecting to each MCP server by name, restoring history. Starting up takes a few seconds with several MCP servers configured, and one that is slow or dead holds up everything behind it, so the line names the server being waited on rather than a generic "loading". The screen is replaced by the app as soon as it is ready.

If startup fails, the reason is shown on that screen and the window stays open. `localcode-gui.exe` has no console (see below), so this is the only place a startup error can be read.

The window links a native webview through CGo, which cannot be cross compiled the way the pure Go daemon and TUI are, so it's built per OS:

* macOS: `make dist-mac-gui` produces a double-clickable `LocalCode.app` (universal, arm64 + amd64). `make gui-mac` builds just the bare `localcode-gui` binary. macOS always has WKWebView.
* Windows: built in CI by `.github/workflows/gui-windows.yml` on a Windows runner (CGo cannot cross compile from macOS), which uploads `localcode-gui.exe` as an artifact. It is linked with `-H windowsgui`, so starting it from `cmd` opens the window and **gives the prompt straight back** instead of tying up the console until the window closes (and launching it from Explorer doesn't flash an empty console box). The trade-off is that a GUI-subsystem process has no console of its own, so the console-only subcommands — `version`, `mcp`, `login` — print nowhere useful from `localcode-gui.exe`; run those from `localcode.exe`, which the same MSI installs. The Windows MSI (`make dist-msi VERSION=x.y.z GUI_EXE=path/to/localcode-gui.exe`) installs it alongside the TUI binary with its own Start Menu shortcut ("LocalCode (Desktop)"), and runs Microsoft's WebView2 Evergreen Bootstrapper silently during install so the runtime is there even on older Windows 10 systems that do not ship it already. That install step is skipped quietly (not a failed install) if there is no network access at install time, if the runtime is already present, or if the bootstrapper itself isn't being reinstalled (which is the normal case when upgrading over an existing version).

**Title bar.** There isn't one on either platform, by two different routes.

* **macOS**: the bar is drawn transparent over the window's own background and the title text is hidden, so the app is one surface from the top down with the close/minimise/zoom buttons floating on it. The bar itself is still there, which is what still lets the window be dragged and closed.
* **Windows** (v0.44.0, working in v0.45.1): the frame is genuinely removed, and everything it did is put back by hand. A 28px strip across the top drags the window and maximises it on a double-click, six-pixel edges around the window resize it, and the page draws its own minimise/maximise/close buttons at the right-hand end of the strip. Those are all page controls that ask the window to do the work — WebView2 puts the page in a child window, so the window's own hit test never sees the mouse, which is why v0.44.0 shipped a bar that could not be dragged. Alt+F4 and the taskbar's own Close work as always, whatever else does not.

Every launch writes `%LOCALAPPDATA%\localcode\gui-frame.log` saying whether the frame was actually removed, what the window style was before and after, and whether the messages the drag and resize regions depend on arrived. It is a handful of lines, replaced each time, and it is the thing to look at (or send) when the window does not behave.

If the Windows window misbehaves — it cannot be moved, an edge will not resize, the buttons do nothing — start it with `LOCALCODE_TITLEBAR=1` and it keeps the ordinary Windows frame instead, with nothing else about the app changed:

```
set LOCALCODE_TITLEBAR=1
localcode-gui.exe
```

The macOS `.app` is unsigned, so Gatekeeper needs a right click then Open the first time, same as the TUI app.

A build made without `-tags gui` still accepts `--gui` but returns an error saying so, rather than failing to build (and `--gui` defaults to off on such a build, so the TUI still starts normally with no flags at all). The daemon, TUI, and browser modes are unchanged.

The window shows the current workspace directory at the top; clicking it opens the OS folder picker — see [Switching the workspace directory](#switching-the-workspace-directory).

### Remote daemon over an SSH tunnel

```bash
# on the Linux server
localcode --headless --listen 127.0.0.1:4096

# on your laptop
ssh -L 4096:127.0.0.1:4096 linux-box
localcode --server http://localhost:4096   # terminal
# or open http://localhost:4096 in a browser
```

> Binding `0.0.0.0` exposes an arbitrary code execution API, since the bash tool is part of it. There is no auth token yet. Always reach the daemon over loopback plus an SSH tunnel, and never bind it directly to an untrusted network.

## Part 2. Configuration

### Config file (config.json)

Either `~/.localcode/config.json` for global settings, or `<project>/.localcode/config.json` for a project override that wins over the global file.

```json
{
  "providers": {
    "bedrock": { "type": "bedrock", "region": "us-west-2" },
    "local":   { "type": "openai-compat", "base_url": "http://localhost:1234/v1" }
  },
  "profiles": {
    "strong":   { "provider": "bedrock", "model": "us.anthropic.claude-opus-4-5-20251101-v1:0", "max_tokens": 8192 },
    "balanced": { "provider": "bedrock", "model": "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "max_tokens": 8192 },
    "cheap":    { "provider": "local", "model": "qwen3-30b-a3b", "max_tokens": 4096, "context_window": 32768 }
  },
  "agents": {
    "general-purpose": { "profile": "balanced" },
    "explore":         { "profile": "cheap" }
  },
  "default_profile": "balanced",
  "max_concurrent_tasks": 5,
  "mcp_servers": {
    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": { "GITHUB_TOKEN": "..." } }
  }
}
```

#### Top level fields

| Field | Meaning |
|---|---|
| `providers` | Model backend connection details. `type` is `bedrock`, `anthropic`, or `openai-compat`. |
| `profiles` | A named provider and model pairing. `max_tokens`, `temperature` and `context_window` are optional. |
| `agents` | Maps an agent name to a profile. `--agent` resolves through this. An unknown name falls back to `default_profile`. |
| `max_concurrent_tasks` | Caps how many background tasks run at once |
| `mcp_servers` | Same shape as Claude Code's `.mcp.json`, so existing entries copy over directly |
| `dictation` | Speech engine settings. Every field optional; with none set, an installed engine beside the binary is found and used. See [Dictating a prompt](#dictating-a-prompt). |
| `dictation_model_dir` | Path to an unpacked sherpa-onnx model, for the older sherpa engine only. See [Engines](#engines). |

#### Profile fields

| Field | Meaning |
|---|---|
| `provider` | Key into `providers` |
| `model` | Model id, as the provider names it |
| `max_tokens` | Cap on one reply, 4096 if unset. Unlike `context_window` this cannot be discovered: it is a choice about how long an answer you want, not a fact about the server. Reduced automatically when the conversation leaves less room than this in the window, so a generous ceiling is safe, and a reply that runs into it says so rather than just stopping. |
| `temperature` | Sampling temperature |
| `keep_going` | How many times one turn may be told to carry on after the model stops with the task unfinished. `0` (default) defers to the model: families known to stall get `3` out of the box, everything else gets none. `-1` forces it off. See [below](#a-model-that-stops-mid-task). |
| `context_window` | The model's total input+output token limit. Usually unnecessary: an openai-compatible server is asked directly (`GET /v1/models`, or `/props` on llama.cpp), and only when it does not answer is the limit guessed from the model name, falling back to 128k for anything unrecognised. Set it when the server reports nothing and the name gives no clue, or to override both. Guessing high is the harmful direction, since this number is what keeps a request inside the real limit. |

#### A model that stops mid-task

Some models — local ones, mostly — end a turn by writing down what still
has to happen rather than doing it: a build fails, the model reads the
error, says "`global_init.cpp` also has to be updated", and stops.
Typing "carry on" makes it pick up and finish that step, and then it
stops again. The person is being used as the model's own loop.

Two things address it, and they are separate on purpose.

**A note in the system prompt**, for the models this has been reported
against, telling them to take the next step in the same turn. It costs a
paragraph, applies to nothing else, and needs no configuration. It is
also not a guarantee, which is why there is a second thing.

**`keep_going`**, which is localcode typing "carry on" for you, at most
N times per turn. Models known to stall get a budget of 3 out of the box
— installing the release is the whole fix, no config key required. For
any other model it stays off, and the profile can override either way:

```json
{
  "profiles": {
    "local": { "provider": "local", "model": "some-other-model", "keep_going": 3 },
    "quiet": { "provider": "local", "model": "muse-glimmer-30b", "keep_going": -1 }
  }
}
```

`0` (or leaving it out) means the model's own default; `-1` means never.
The default for unrecognised models has to stay zero: a turn that ends
after tool use looks exactly the same whether the model finished or gave
up, so on a model that stops when it is done this would spend a turn
asking "anything else?" after every task.

The rules it carries on under, each of which is a case where stopping
was right:

| Not carried on when | Because |
|---|---|
| No tool ran in this turn | It was a question and its answer, not a task |
| The last tool call was refused | The model stopped because someone said no |
| The reply ends in a question | The model is asking, not stalling |
| The reply hit `max_tokens` | It needs a bigger cap, not another turn — and that is already reported |
| The last carry-on produced no work | Prose twice over is the model saying it has finished |
| You have already typed something | Your message reaches the model as soon as this turn ends, and it beats an invented one |

That last rule is what keeps the setting cheap: a finished task costs one
extra turn, not `keep_going` of them. Each carry-on appears in the
transcript as a note saying what happened, so a turn that continues by
itself never looks like one that never stopped.

#### Provider fields

| Field | Meaning |
|---|---|
| `bedrock.region` | AWS region, for example `us-west-2` |
| `bedrock.profile` | AWS named profile to use, such as one created by `localcode login bedrock`. Omit it to use the default AWS credential chain. |
| `anthropic.api_key` | Optional. Omit it and the key stored by `localcode login anthropic` in `~/.localcode/credentials.json` is used. |
| `anthropic.base_url` | Defaults to `api.anthropic.com`. Override it to go through a corporate proxy. |
| `openai-compat.base_url` | The URL prefix in front of `/chat/completions` |
| `openai-compat.api_key` | Optional, usually unnecessary for a local server. Sent as `Authorization: Bearer <key>`. |

See [MODELS.md](MODELS.md) for real model IDs, region prefixes, and Bedrock troubleshooting.

#### Agent fields

| Field | Meaning |
|---|---|
| `profile` | Which provider and model this agent uses. Required. |
| `description` | One line shown to other agents choosing a delegate through the `Task` tool |
| `prompt` | Extra system prompt appended when running as this agent |
| `tools` | Restricts this agent to the listed tools. Leave it out for full access. |

See [Combining agents](#combining-agents) for the full picture.

#### MCP notes

* Three transports are supported, the same ones Claude Code writes: `stdio` (a local child process), `http` (streamable HTTP), and `sse` (the older HTTP+SSE). Tools appear as `mcp__<server>__<tool>` whichever transport they came over.

```json
{
  "mcp_servers": {
    "github":  { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": { "GITHUB_TOKEN": "..." } },
    "hosted":  { "type": "http", "url": "https://example.com/mcp", "headers": { "Authorization": "Bearer ..." } },
    "legacy":  { "type": "sse", "url": "https://example.com/sse" }
  }
}
```

* `type` may be omitted: an entry with a `url` is treated as `http`, anything else as `stdio`. Set `"type": "sse"` explicitly for a server that still speaks the older protocol — a bare url will not infer it.
* `headers` is where a remote server's API token goes. Its **values are never printed** by `mcp list` or `mcp get`, only the key names, since that output routinely lands in a scrollback or a pasted bug report. They are also only ever *added* to a request, never overriding a header the protocol itself sets.
* A remote server has no request timeout (a tool call may legitimately run long), but a server that accepts a connection and then never answers is given up on after 60 seconds.
* **MCP tools always require permission confirmation.** A server claiming its own tool is read only through annotations is not trusted.
* If one server fails to connect — a bad command, a crash, an unreachable endpoint, a rejected token — only that server is skipped. The rest register normally and the daemon still starts, logging a warning.
* If a connected server's session dies, the next call retries the connection once.

A broken config, such as a profile pointing at a provider that does not exist, fails at startup with an error instead of running.

### Managing MCP servers with `localcode mcp`

The same role `claude mcp` plays in Claude Code. It registers, lists, and removes servers without hand editing the `mcp_servers` JSON.

This is a plain CLI subcommand that runs immediately without starting the daemon or TUI, the same way `localcode login` does. It edits config.json only, so **a running daemon picks up changes at its next start or reconnect.**

```bash
# register a stdio MCP server, global by default in ~/.localcode/config.json
localcode mcp add github -e GITHUB_TOKEN=ghp_xxx -- npx -y @modelcontextprotocol/server-github

# register for this project only, in ./.localcode/config.json
localcode mcp add local-fs -s project -- npx -y @modelcontextprotocol/server-filesystem .

# register a remote server (streamable HTTP), with an auth header
localcode mcp add --transport http -H "Authorization: Bearer xyz" hosted https://example.com/mcp

# a server that still speaks the older HTTP+SSE protocol
localcode mcp add --transport sse legacy https://example.com/sse

# copy an existing .mcp.json entry across as raw JSON (either shape)
localcode mcp add-json weather '{"command":"node","args":["weather-server.js"],"env":{"API_KEY":"xyz"}}'

localcode mcp list          # scope, command, and a live connection test per server
localcode mcp list --no-test # just the listing, without starting anything
localcode mcp get github    # full command, args, and env for one server
localcode mcp remove github # unregister

# already using Claude Code? pull its MCP servers in instead of retyping them
localcode mcp import-claude
```

| Detail | Behavior |
|---|---|
| `-t`, `--transport` | `stdio` (the default), `http`, or `sse`. With `http`/`sse` the single positional argument after the name is the server's url instead of a command. |
| `-e`, `--env KEY=VALUE` | Repeatable. stdio servers only. |
| `-H`, `--header "Key: Value"` | Repeatable. Remote servers only — this is where an auth token goes. |
| `-s`, `--scope` | `global`, the default, or `project` |
| `--` | Everything after it is the command and its arguments. Always use it so flags meant for the server, such as `-y`, do not get read as flags for `localcode mcp` itself. |
| `remove` without `-s` | If the same name exists in both global and project, nothing is deleted and you get an ambiguity error. Say which with `-s global` or `-s project`. |

These commands only read and write the `mcp_servers` map, so editing config.json by hand works exactly the same. The CLI is a convenience.

#### `mcp list` tests each connection

Being registered in config.json says nothing about whether a server's command exists, its endpoint is reachable, or either speaks MCP. So `localcode mcp list` brings each one up for real — starting the process or dialing the URL, completing the MCP handshake, listing its tools — and prints **one line per server**: its name, which scope it is registered in, and whether it answered.

```
github         global    ok (26 tools)
hosted         global    ok (4 tools)
local-fs       project   ok (11 tools)
weather        project   failed: connect (node): fork/exec node: no such file or directory

1 of 4 server(s) failed to connect.
```

That is the whole output — no config file paths, no command lines or urls, no env or header keys. This is a status view; `localcode mcp get <name>` is where a server's full definition lives.

A failure is reported, not returned as an error — the listing itself succeeded, and a server being down is information about that server. Each check is bounded by a 20 second timeout, so an unresponsive server delays the listing rather than hanging it. Servers with expensive startup (an `npx` package that isn't cached yet) make this noticeably slower than a plain listing; `--no-test` skips the connecting entirely and just names what is registered.

#### Importing from Claude Code

`localcode mcp import-claude` reads every MCP server a Claude Code install already knows about for the current directory — this project's checked-in `./.mcp.json`, plus `~/.claude.json`'s global servers and its per-project block — and registers them the same way `mcp add` would. Project-scoped Claude entries win over global ones on a name collision, matching Claude Code's own precedence.

Local and remote servers both import: an entry's `type`, `url`, and `headers` carry across unchanged alongside `command`/`args`/`env`, because localcode's own config uses the same field names.

```bash
localcode mcp import-claude                  # into ~/.localcode/config.json (global, the default)
localcode mcp import-claude -s project       # into ./.localcode/config.json instead
localcode mcp import-claude --skip-existing  # leave servers that already exist under that name alone
```

Re-running it is the common case — a Claude Code setup changed, or the first run only partially matched what's local — so by default it **overwrites** a server already registered under the same name with the freshly imported definition, the same way `mcp add` does. Pass `--skip-existing` to leave those alone instead.

An entry that says neither how to start a process nor where to connect can't be imported by any build, and is listed by name with the reason rather than silently dropped.

### Fine grained permission rules

Default behavior without any rules:

| Tool | Confirmation |
|---|---|
| `read_file`, `glob`, `grep` | Runs immediately |
| `bash` running a `git` command | Runs immediately (built in default, see below) |
| `write_file`, `edit`, `bash` running anything else, MCP tools | Always asks |

**git runs without asking by default.** Any `git` subcommand through the `bash` tool is auto-allowed out of the box, no config needed, because an agent that has to ask before every `git status` is unusable and git is close to always either read only or recoverable through the reflog. Add your own `bash` rule for `git` in `permission` (see below) to turn this back into ask or deny.

Adding a `permission` block gives you opencode style per tool and per pattern control, so safe commands run automatically, dangerous ones are blocked outright, and only the rest prompt. A rule you write for a tool always overrides that tool's built in default, including the git one.

```json
{
  "permission": {
    "bash": [
      { "match": "*",       "decision": "ask" },
      { "match": "git *",   "decision": "allow" },
      { "match": "rm *",    "decision": "deny" }
    ],
    "read_file": "allow",
    "*": "ask"
  }
}
```

| Rule form | Behavior |
|---|---|
| String value (`"allow"`, `"ask"`, `"deny"`) | Applies to every call of that tool |
| Array value | A list of `{"match": pattern, "decision": ...}` checked in order, where **the last matching rule wins** |
| `"*"` key | Fallback for any tool with no exactly named rule. An exact tool name always beats `"*"`. |
| No rule matches | Falls back to that tool's built in default from the table above |

In the example, `git status` matches both `*` (ask) and `git *` (allow), and allow wins because it comes later. `rm -rf` matches `*` and `rm *`, so deny wins.

What each pattern matches:

| Tool | Match target |
|---|---|
| `bash` | The full command string |
| `write_file`, `edit` | The target file path |
| Anything else, including MCP tools | No pattern, only the `"*"` rule applies |

Patterns are globs where `*` is zero or more characters and `?` is exactly one.

#### Skipping confirmations entirely

`"skip_permissions": true` turns every `ask` into `allow`, the equivalent of Claude Code's `--dangerously-skip-permissions`. It defaults to **off** and has to be opted into deliberately.

```json
{ "skip_permissions": true }
```

With it on, the model writes files and runs shell commands with no confirmation at all. Turn it on only where that is acceptable: a scratch repository, a container, a machine whose state you do not mind losing.

`deny` rules still deny. Skipping confirmations is a convenience; overriding a rule written specifically to forbid something would be a different and much worse promise. Pairing the two is a reasonable middle ground:

```json
{
  "skip_permissions": true,
  "permission": { "bash": [{ "match": "rm *", "decision": "deny" }] }
}
```

`deny` can block tools that never needed confirmation. For example, this blocks reading `.env` files while leaving every other read alone:

```json
{ "read_file": [{ "match": "*.env", "decision": "deny" }, { "match": "*", "decision": "allow" }] }
```

**A `bash` rule matches per command, not per line.** `git status && rm -rf ~` is not treated as one string matched against `git *` — the line is split on `&&`, `||`, `;`, `|`, and newlines (quoted separators, like one inside a commit message, are left alone), and every resulting command has to earn `allow` on its own. Any `deny` anywhere in the line denies the whole thing. This is why allowing `git *` (or relying on the built in git default) is safe: it cannot be used to smuggle an unrelated command through alongside a git one.

Command substitution and output redirection (`$(...)`, `` `...` ``, `<(...)`, `>(...)`, `>`, `>>`) never auto-allow, even inside an otherwise-allowed command, since they can run a nested command or write to an arbitrary file that the per-segment check never sees. Those always fall back to asking, unless an explicit `deny` rule matches, in which case deny still wins.

### Answering a permission prompt: once, this session, or always

A permission prompt offers four answers:

| Answer | TUI key | Effect |
|---|---|---|
| Allow once | `y` | Approves exactly this call. Asks again next time. |
| Deny | `n` | Refuses this call. Asks again next time. |
| Allow for session | `s` | Approves this call and every later call in the current session that matches the same rule pattern (e.g. any `npm *` command), without asking again — until the session ends or the daemon restarts. Nothing is written to disk. |
| Always allow | `a` (only shown when available) | Everything "allow for session" does, plus writes a matching rule to config.json, so the same pattern is auto-allowed in every future session too. |

The Web UI shows the same four as buttons: Deny, Allow for session, Always allow, Allow once.

For a `bash` call, the rule an "allow for session" or "always allow" grants is generalized to the command's first word — approving `npm test` grants `npm *`, not just that exact command — since approving a shell command usually means approving that program. Every other tool (file paths, MCP tools) grants the exact subject rather than widening it, so approving one file doesn't silently approve a whole directory. The prompt always shows the exact pattern before you answer, so nothing is granted invisibly.

"Always allow" writes to whichever config.json the daemon loaded: the file passed via `--config`, or the global `~/.localcode/config.json` if none was given — never the project-local override, so an approval survives switching projects. It edits only the `permission` key, preserving every other key and value in the file byte for byte, the same careful merge `localcode mcp` uses. If the daemon has no writable config.json to target, "always allow" isn't offered — only once, session, and deny are.

Session grants are forgotten when a session is deleted, and when the daemon restarts. Permanent ("always") grants live in config.json and survive both.

**A permission modal locks the prompt box while it's open**, in the Web UI, with a placeholder pointing back at it — typing an answer into the chat box instead of clicking a button no longer silently queues as a follow-up message. The TUI equivalent doesn't lock (its permission line is already a separate prompt), but prints a one-time hint if Enter is pressed while a request is pending, rather than doing nothing with no explanation.

### Viewing and changing permission settings without waiting for a prompt

A pill under the prompt box (Web UI and the GUI window, same page) always shows the current permission state: `permissions: ask (N rules)`, or `permissions: skip` in warn color when `skip_permissions` is on. Click it to open a panel that:

* toggles `skip_permissions` on or off
* lists every rule currently in `permission`, with a remove button per rule
* adds a new rule by tool name, match pattern, and decision (allow/ask/deny)

Every change applies immediately (the running daemon's decisions reflect it on the very next tool call) and is written to config.json the same way "always allow" is — the daemon needs a config.json path to persist to (see `--config` above); if it doesn't have one, the panel still shows the current state but the controls are disabled with a note explaining why.

### Switching the workspace directory

The workspace is the directory every relative file path and bash command resolves against — set once at startup from wherever you ran `localcode`. It's shown at the top of the GUI window, and in the Web UI's header, and can be changed without restarting by clicking it.

What that click does depends on where you are:

| Where | Clicking the workspace |
|---|---|
| GUI window | Opens the operating system's own folder picker (macOS `choose folder`, Windows' folder browser, zenity/kdialog on Linux), starting in the current workspace. Choosing a folder applies it immediately; dismissing the dialog changes nothing. |
| Browser | Opens a box to type an absolute path into. The web platform gives no way to get a real filesystem path out of a file dialog — neither `<input webkitdirectory>` nor `showDirectoryPicker()` exposes one — and a daemon you reached over the network would open its dialog on the *server*, so the picker is deliberately offered only in the desktop window. |

The folder icon beside the path opens that directory in a file-manager window — Explorer on Windows, Finder (brought to the front) on macOS, whatever `xdg-open` is registered to elsewhere. Offered only in the desktop window, for the same reason as the picker: over the network the window would open on the daemon's machine.

**Each session has its own workspace.** Every relative file path and every bash command resolves against the directory of the session it belongs to, so two sessions can work in two different projects at the same time, on the same daemon, without disturbing each other. It is a property of the session, not of the localcode process, which is why reopening a conversation about another project puts you back in that project rather than wherever the daemon happens to have been started.

Switching is refused only while *this* session has a turn in flight, since redirecting it mid-tool-call would change what an already-running command is operating on. A turn in some other session is not your business and no longer blocks anything.

Omitting the session (a client that does not track them) sets the default instead: what a newly created session starts in, and what a session with no recorded workspace of its own falls back to.

Before v0.39.0 this was process-wide — one `os.Chdir` for the whole daemon — which is why a workspace change used to be refused whenever *any* session was busy, including one nobody was watching and one parked forever on an unanswered permission request.

### Hooks

The same concept as Claude Code hooks. Run a shell command at a specific point, and block that point if you want.

Hooks are a separate layer from `permission`. Permission decides whether a tool call is allowed, denied, or confirmed. Hooks splice an arbitrary shell command in around it, or at points that have nothing to do with tools.

With both enabled, `pre_tool_use` runs first, and if it does not block, the `permission` check follows. A `pre_tool_use` hook allowing something does **not** skip an ask or deny from `permission`.

```json
{
  "hooks": {
    "pre_tool_use": [
      { "matcher": "bash", "command": "echo \"$STDIN\" | jq -e '.tool_input.command | test(\"rm -rf\") | not' >/dev/null || (echo blocked >&2; exit 2)" }
    ],
    "post_tool_use": [
      { "command": "cat >> /tmp/tool-log.jsonl" }
    ],
    "user_prompt_submit": [{ "command": "..." }],
    "stop": [{ "command": "..." }],
    "session_start": [{ "command": "..." }]
  }
}
```

| Event | Payload on stdin | Can block |
|---|---|---|
| `pre_tool_use` | `tool_name`, `tool_input` | Yes, stops the tool before the permission check |
| `post_tool_use` | `tool_name`, `tool_input`, `tool_output`, `is_error` | No, it runs after the fact |
| `user_prompt_submit` | `session_id`, `prompt` | Yes, the message reaches neither a command nor the model, and an error event is recorded |
| `stop` | `session_id` | No |
| `session_start` | `session_id`, `agent` | No |

Other details:

* **matcher** only means something for `pre_tool_use` and `post_tool_use`. It is a regex against the tool name, **anchored to the whole name**, so `"bash"` hits only the `bash` tool. Alternation such as `"bash|edit"` and patterns such as `"mcp__github__.*"` both work. Omit it to run on every tool call.
* **To block**, print `{"decision":"block","reason":"..."}` on stdout, or exit with code **2**, in which case stderr becomes the reason. Any other non zero exit is treated as a warning and execution continues.
* Each hook has a 30 second timeout. Multiple hooks on one event run in registration order and stop at the first block.
* A project config's `hooks` replaces the global config per event rather than merging, matching how the rest of the config merges.

### Authenticating with `/login`

`localcode login <bedrock|anthropic>` walks through cloud provider authentication interactively. Run it in a terminal before starting the daemon or TUI. It removes the need to paste an `api_key` into config.json or to install the AWS CLI first.

> **Signing in with a claude.ai Pro or Max subscription is not supported.** Claude Code can do that because it uses a private OAuth client Anthropic issued specifically for it, and those credentials and scopes are not public. A third party tool imitating them would risk violating the Anthropic terms of service, so it is not implemented. Both methods below use only the official published flows from AWS and Anthropic.

#### `localcode login bedrock`

Implements the AWS IAM Identity Center (SSO) **device authorization flow** directly, so it works without the AWS CLI installed.

```bash
localcode login bedrock
```

* Prompts for the SSO start URL and SSO region. You can supply them up front with `--start-url`, `--sso-region`, `--region`, `--profile`, `--account`, and `--role`.
* Prints an authorization URL, opens a browser when it can, and waits for approval. **This is a device code URL, so any device can open it.** It does not have to be the machine running the command.
* If more than one AWS account or role is reachable, it lists them to pick from. A single option is chosen automatically.
* Saves results **where the AWS CLI keeps them**: the token cache at `~/.aws/sso/cache/<sha1 of start url>.json`, and a `[profile <name>]` entry in `~/.aws/config`, named `localcode-bedrock` by default. An existing profile with that name is left untouched.
* Those credentials are picked up by the standard AWS credential chain, so config.json only needs `"providers": {"bedrock": {"type":"bedrock","region":"...","profile":"localcode-bedrock"}}`. The command prints the exact values when it finishes.

#### `localcode login anthropic`

```bash
localcode login anthropic
```

Reads an API key from `console.anthropic.com`, hidden from the screen on a real terminal, and stores it in `~/.localcode/credentials.json` with mode 0600.

config.json then needs only `"providers": {"anthropic": {"type":"anthropic"}}`. Leaving out `api_key` uses the stored key.

## Part 3. Project context

### Skills

Put a skill at `~/.localcode/skills/<name>/SKILL.md` for global scope, or `<project>/.localcode/skills/<name>/SKILL.md` for a project scoped one that wins on a name collision.

```markdown
---
name: pdf-tools
description: Merge, split, and watermark PDF files
---
# PDF Tools

Write the real instructions here. This whole body is what gets
returned when the model calls the `Skill` tool with this name.
```

Only each skill's `name` and `description` go into the system prompt at startup, costing a few dozen tokens per skill. The body loads only when the skill is actually invoked, so unused skills are nearly free.

To reference other files such as `scripts/*.py` from the body, write relative paths and let the model read them with `read_file` or `bash`.

Run a skill directly by its own name with `/<skill name>`. See [Running a skill](#running-a-skill).

### AGENTS.md project rules

The same convention opencode and Claude Code use. Put an `AGENTS.md` at the project root, or any parent directory up to the git repository root, and it is appended to the system prompt.

`CLAUDE.md` is recognized as a fallback in the same places, so an existing Claude Code file is reused as is.

`~/.localcode/AGENTS.md` and `~/.claude/CLAUDE.md` both apply your personal rules to every project — both are loaded when both exist, so setting localcode up after Claude Code does not silently retire the file you already wrote. Project rules and global rules are combined, not overwritten.

Which project's rules apply is decided per session, from the workspace that session is in, and re-read at the start of every turn. So two sessions in two projects each get their own `AGENTS.md`, moving a session's workspace moves the rules with it, and editing the file takes effect on your next message rather than on the next restart. (Before v0.43.0 they were read once at startup from the directory the daemon happened to be launched in — which for a desktop build opened from Finder or the Start Menu is not a project at all.)

```markdown
# AGENTS.md
Build: `make build`
Test: `go test ./...`
Architecture: core daemon over HTTP and SSE, with TUI and Web UI clients. internal/agent drives a turn.
Conventions: comments explain why only. Handle errors that can actually happen.
```

Use [`/init`](#init) to have the model scan the repository and draft one for you.

Inside `AGENTS.md` or `CLAUDE.md`, `@path` splices another file in at that spot, matching Claude Code's import syntax.

| Form | Resolves against |
|---|---|
| `@relative/path` | The directory of the file doing the importing |
| `@~/path` | Your home directory |

An imported file can import further files, followed up to 4 levels deep. Anything inside a fenced code block or inline code such as `` `@path` `` is left alone.

```markdown
# AGENTS.md
Read @README.md for a project overview.
Personal workflow: @~/.localcode/my-workflow.md
```

### Per-model formatting notes

A model's habits are not always right for this window, and a few are wrong
in a way the user pays for. Gemma writes symbols and names as LaTeX
(`$\rightarrow$`, `$\text{Bla-Bla}$`), which a client with MathJax renders
as an arrow and a name, and this one shows as the dollars and backslashes
they are.

So a model whose id contains `gemma` gets one extra paragraph in its
system prompt: this interface renders Markdown only, write the character
itself. Nothing is added for any other model.

The Web UI also unwraps the ones that arrive anyway, so a reply already in
a transcript reads properly. It handles symbols (`\rightarrow`, `\sim`,
Greek letters, relations) and the font commands that carry words rather
than maths (`\text`, `\mathbf`, `\mathrm`, `\boldsymbol` and the rest),
including when those are nested in each other: `$\mathbf{\text{ vs }}$`
is the word "vs". That half is deliberately narrow: a `$` span is only
touched when it contains a LaTeX command, so `$PATH`, `$5` and a real
formula are left alone.

The table is `modelQuirks` in `internal/agent/quirks.go`, matched as a
lowercased substring of the model id.

### Auto memory

The same idea as Claude Code's auto memory. Where `AGENTS.md` is written by a person, auto memory is **written by the model as it works**, so build commands, facts discovered while debugging, and stated preferences such as "use pnpm" survive into the next session.

* Each project gets `~/.localcode/projects/<slug>/memory/` automatically. The slug comes from the git repository root path, so multiple worktrees or subdirectories of one repository share a single memory directory. Outside a git repository, the working directory is used instead.
* `MEMORY.md` in that directory is the index, loaded into the system prompt at the start of every session, capped at 200 lines or 25KB, the same limits Claude Code uses. Anything past that is not loaded.
* The system prompt tells the model to split details into separate topic files such as `debugging.md`, which it reads with `read_file` when needed.
* There is no dedicated memory tool. The model uses the `read_file`, `write_file`, and `edit` tools it already has. The directory path and current index are given to it each session.
* [`/memory`](#memory) prints the directory path and index contents instantly, with no model call.

Turn it off with:

```json
{ "auto_memory_enabled": false }
```

## Part 4. Commands and screen controls

### Screen controls

Common to the TUI and Web UI:

| Action | How |
|---|---|
| Send a message | **Enter**. The Web UI also has a Send button. |
| Insert a newline | **Ctrl+J** in the TUI, **Shift+Enter** in the Web UI |
| Answer a permission prompt | `y`, `n`, `s`, or `a` in the TUI; buttons in the Web UI — see [answering a permission prompt](#answering-a-permission-prompt-once-this-session-or-always) |
| Cancel the running turn | **Esc**, in either client |
| Recall a previous prompt | **Up** and **Down**, in either client |
| Switch agent | **Tab** for the next, **Shift+Tab** for the previous, in either client |
| Quit the TUI | **Ctrl+C**, or type `exit` or `:q` |

Other behavior:

* The input box grows as you type, up to about 10 lines, then scrolls internally.
* A permission prompt appears whenever the model wants `write_file`, `edit`, a non-git `bash` command, or an MCP tool. Any client attached to the session can answer it, and answering closes the prompt on every other client.
* The TUI draws a rule above and below the input box, with a status line directly underneath showing `agent: <name>  ·  model: <model id>`. Switching with Tab updates only that line and adds nothing to the transcript.
* The TUI places the real terminal cursor at the insertion point inside the prompt box, so IME composition for Korean, Japanese, and Chinese renders in the box while you type rather than below it.
* **Running work shows below the prompt box, not in the conversation.** While a turn is in flight the TUI animates a line naming what it is doing (the running tool's name, or `working`), the queue depth, and how many background tasks are going. It disappears the moment the turn ends. The Web UI shows the same information in its status bar. Tool starts and finishes no longer write `[tool] ...` lines into the transcript.

**Esc cancels whatever is running.** Press it while the model is answering (the status line says "esc to cancel") to stop that turn immediately. Cancelling also clears anything waiting in the prompt queue — the point of cancelling is to stop, so letting a queued message fire right after would defeat it. A `[cancelled]` line marks where it stopped; nothing about it is treated as an error. Pressing Esc with nothing running does nothing.

**Up and Down recall previous prompts**, the way a shell's history does. Up walks back through what you have already sent, newest first, and Down walks forward again. Stepping forward past the newest entry restores whatever you had half-typed before you started recalling, so reaching for history never costs you a draft.

Recall *starts* at the edges of the prompt box: the cursor has to already be on the first line before Up reaches for history, and on the last line before Down does. Inside a multi-line prompt the arrows just move the cursor as usual. Once a walk is under way both keys keep walking wherever the cursor has landed, so Up, Up, Up goes three prompts back; editing what was recalled ends the walk, and the next Up starts again from the newest entry. Repeating the same message twice in a row stores it once.

**The list belongs to the conversation.** Each session has its own, and switching away and back finds it as you left it. It is filled from two places: what you send, and the prompts in the transcript the daemon replays when the session opens — so reopening a session (or reattaching the TUI to one) recalls what was asked in it before, including prompts sent from another client. Nothing is stored on disk for this; the replayed transcript is the record.

Up to 200 entries per session are kept.

**Messages sent while a turn is still running are queued.** This covers the whole turn, tool execution included, not just while text is streaming. The prompt appears in the transcript immediately (the TUI marks it `[queued] <text>`) and the status line shows `(N queued)`. The first queued message sends automatically the moment the turn actually ends, and several stack up and go out in order. If a send does slip through while the daemon is busy (for example, a turn started from another client on the same session), it is queued and retried rather than shown as an error.

Commands starting with `/`, along with `exit` and `:q`, are not queued. They keep the old behavior of being ignored until the turn finishes, because replaying them later would send them to the model as ordinary text instead of running them.

### Running a skill

Type a skill's own name as a command. You do not have to wait for the model to decide to call the `Skill` tool.

| Command | Effect |
|---|---|
| `/skill` | Lists registered skill names and descriptions instantly, with no model call |
| `/<skill name>` | Runs that skill, for example `/pdf-tools` |
| `/<skill name> <request>` | Runs the skill with your request attached, for example `/pdf-tools merge a.pdf and b.pdf` |

The transcript keeps just the short command you typed. The full skill body goes only to the model.

When two things share a name, precedence is:

1. Built in commands such as `/init` and `/compact`
2. [Custom commands](#custom-commands)
3. Skills

A `/name` matching none of them is sent to the model as ordinary text.

> `/skill <name>` still works as the older spelling.

### `/init`

The same as opencode's `/init`. Scans the repository with `Glob`, `Grep`, and `Read`, then creates or improves an `AGENTS.md` at the project root covering build, lint, and test commands, an architecture overview, and code conventions.

The transcript shows only `/init`. Because it writes a file, expect a `write_file` or `edit` permission prompt the first time.

### Custom commands

Put a markdown file at `.localcode/commands/<name>.md` for the project, or `~/.localcode/commands/<name>.md` for global scope where the project file wins on a collision. Call it with `/<name>`. The format matches opencode's custom commands.

```markdown
---
description: Run only the tests matching a pattern
agent: build
model: my-strong-model-id
---
Find the tests matching `$ARGUMENTS` and analyze the results.
Relevant source: @internal/agent/loop.go
Currently failing: !`go test ./... 2>&1 | grep FAIL`
```

| Frontmatter | Meaning |
|---|---|
| `description` | One line shown by `/commands`. Optional. |
| `agent` | Run this one turn as that agent, using its profile, system prompt, and tool restrictions. The session's own agent is unchanged. Optional. |
| `model` | Force a different model ID for this one turn, ignoring the profile. Optional. |

Body substitutions:

| Token | Replaced with |
|---|---|
| `$ARGUMENTS` | The whole argument string |
| `$1` through `$9` | Positional arguments split on whitespace |
| `` !`shell command` `` | That command's stdout |
| `@path` | That file's contents, resolved relative to the working directory rather than the command file |

For example `/hello World` sends the body with `$1` and `$ARGUMENTS` both replaced by `World`, while the transcript shows only `/hello World`. Use `/commands` to list what is registered.

### `/tasks`

Background tasks produce no transcript lines. Inspect them here instead.

| Command | Effect |
|---|---|
| `/tasks` | Lists every background task in this session with its status, agent, and prompt. Answered from client state, no model call. |
| `/tasks <id>` | Shows everything that task has produced so far. Works while it is still running, so it doubles as a progress view. |
| `/tasks cancel <id>` | Stops a running task. The status line turns to `cancelled` once the stop reaches the task's next step. |

A running task also appears in the indicator below the prompt box, and in the Web UI's right panel.

**A permission request raised inside a task is asked in this session.** Nothing streams a task's own log, so a request written only there could never be answered, and the task waited on it forever while holding one of the concurrency slots. The request now appears here, prefixed with the task it came from, and answering it releases the task.

**A background task does not block the prompt.** Tasks run in their own child sessions, so the session you are typing in stays free: a new prompt goes out immediately rather than queuing. Only a turn in *this* session queues what you type.

### `/memory`

Prints the current project's auto memory directory path and `MEMORY.md` index contents instantly, with no model call. See [Auto memory](#auto-memory).

### `/config`

Settings that can be toggled while running. They apply daemon wide rather than per session, so changing one from any session takes effect everywhere immediately.

| Command | Effect |
|---|---|
| `/config` | Shows current values, no model call |
| `/config auto_compact on\|off` | Automatic compaction past 80% context |
| `/config show_tps on\|off` | The tokens per second reading under the prompt |
| `/config auto_delegate on\|off` | Sending matching prompts to a cheaper sub agent, see [Auto delegation](#auto-delegation) |

Each change records a `config.changed` event on that session and the Web UI updates its status bar right away. A newly opened client reads current values from `GET /api/settings`.

### `/compact`

Compacts the conversation immediately instead of waiting for the 80% threshold. See [Context window management](#context-window-management).

| Command | Effect |
|---|---|
| `/compact` | Compacts with the default summarization prompt |
| `/compact <instructions>` | Adds your instructions for that one summarization, for example `/compact keep only file paths` |

With nothing to compact, on an empty session, it records an error event and does nothing. On success it records a `compacted` event just like automatic compaction, marked `manual: true`, and shows a confirmation.

### `/usage`

Shows cumulative token counts per model for the current session, with no model call. **Token counts only, never dollar figures.**

Unlike the context percentage in the status bar, which is a snapshot of the most recent call, `/usage` sums every API call since the session started. Each call is billed for the entire history it resends, so a sum rather than a snapshot is the correct answer to "how much has this session used".

With no calls yet, it just says so.

### Other local commands

These are typed into the message box but never reach the event log, so replaying a session does not bring them back.

| Command | Effect |
|---|---|
| `/help` | Lists available commands instantly, no model call |
| `/version` | Shows the version of the **daemon** you are attached to, from `GET /api/version`. With `--server` against a remote daemon this is that daemon's version, which can differ from your local binary. |
| `exit`, `:q` | Quits the TUI, same as Ctrl+C. The Web UI only prints a note, since a browser cannot quit the program. Close the tab yourself. |

## Part 5. Sessions

### Switching sessions

A session is an append only event log that lives as long as the daemon, so reopening the TUI or a browser tab picks the conversation back up.

* **TUI**: at startup, if any session exists, the terminal lists them with session ID, agent, and creation time. Enter a number to resume, or `n` or an empty line to start fresh. From the same screen, `d<number>` such as `d1` deletes one session and reshows the list, and `da` deletes every session after you type `yes` to confirm.
* **Web UI**: the left panel always shows the session list, each entry labelled with the workspace directory it was created in. Click a session to switch to it — the screen clears and that session's whole event log replays, including user messages, model replies, and tool runs. See [Left panel: sessions](#left-panel-sessions).

`GET /api/sessions` returns the same list. Background tasks are `visible:false` and do not appear there. Use `GET /api/sessions/{id}/tasks` for those.

### Renaming and deleting sessions

Sessions are identified and resumed by ID, so a `title` is purely for display.

| Action | API | Also available from |
|---|---|---|
| Rename | `POST /api/sessions/{id}/rename` with `{"title": "..."}` | The rename button on the session card in the Web UI left panel |
| Delete one | `DELETE /api/sessions/{id}` | The delete button per session in the Web UI, or `d<number>` in the TUI startup picker |
| Delete all | `DELETE /api/sessions` | The delete all button in the Web UI, or `da` in the TUI startup picker |

* A rename records a `session.renamed` event.
* Deleting removes the session and its JSONL and metadata files permanently. It cannot be undone.
* Deleting is refused with 409 if that session has a turn in progress.
* Deleting a parent does not cascade to child sessions created by background tasks. They stay, invisible in the list.
* Delete all is refused with 409 if **any** session has a turn running, and nothing is deleted, so a partial delete never leaves things in an unclear state.

### Context window management

At the end of every turn, the token usage the provider reports is recorded as a `usage` event. Bedrock, Anthropic, and any OpenAI compatible server asked for `stream_options.include_usage` all supply it.

Each event carries input and output token counts, the model's known maximum context from the best effort table in [internal/modelinfo](../internal/modelinfo/modelinfo.go) defaulting to 128000 for unknown models, the percentage filled, and tokens per second. Both clients drive their status bar from this.

**Automatic compaction.** Once context use passes **80%**, and `auto_compact` is on, the next message triggers a one time summarization of the whole conversation. That summary replaces the history and the new message is sent after it. The transcript notes that compaction happened.

**When a request is too big anyway.** The 80% figure watches the history alone, and what a server refuses is the history *plus* the room the request reserves for the reply, so a large `max_tokens` against a small window can be refused while the meter reads half full. Four things guard against that.

| Guard | What it does |
|---|---|
| `max_tokens` is clamped | Every request asks for only what is left of the window, so the reply cap cannot be what pushes a request over. |
| Tool output is capped | One tool result may take at most a quarter of the window. The start and the end are kept and the gap is described, so the model knows to read a file in ranges rather than believing it saw all of it. Without this, `read_file` on a large file or `bash` running `cat` could exceed the whole window inside a single message, which no summarizing or dropping can undo. |
| A refused turn is summarized and retried | Not the end of the turn. The transcript says it happened, and the turn carries on. |
| Still refused, it is trimmed | Whole messages go from the oldest end, and the remaining text is cut if dropping is not enough. Each attempt aims at two thirds of what the conversation *measures*, not of the window, so it converges on a size the server accepts. This matters most for Korean and Japanese, where the character-count estimate runs about 4x low and a request the server refuses can measure as comfortably fitting. |

`/compact` shrinks its own request the same way, rather than failing for being too long, which is the one failure it cannot have.

Set `context_window` on the profile if the model is one whose name gives no clue to its real limit.

If summarization fails for some other reason, a network error say, compaction is skipped and the original history is used. It never blocks the conversation.

| Setting | Default | Change it |
|---|---|---|
| Automatic compaction | on | `"auto_compact_enabled": false` in config, or `/config auto_compact on\|off` while running |
| TPS display | on | `"show_tps": false` in config, or `/config show_tps on\|off` while running |

### Session logs

Session events append to `~/.localcode/sessions/<session-id>.jsonl`, useful for debugging and replay.

### Daemon restart and session recovery

Session metadata at `~/.localcode/sessions/<id>.meta.json` and the event log at `<id>.jsonl` both persist, so **restarting the daemon keeps** the session list, the conversation context, and `/usage` totals.

At startup, `session.LoadAllFromDisk` reads every pair to restore the list, and `agent.Loop.RehydrateAll()` replays each event log to rebuild the model facing history, including tool calls, tool results, and compaction summaries, along with token usage. The previous conversation is not only visible again, the model itself still remembers the context for the next message.

If one session fails to restore, for example a corrupt `.meta.json`, the rest still restore and the daemon logs a warning.

## Part 6. Web UI

### Resizing and hiding the side panels

| Action | How |
|---|---|
| Resize a panel | Drag the hairline between it and the transcript |
| Back to the default width | Double-click that same handle |
| Hide or show a panel | The toggle button in the header, at the far left for the session panel and the far right for the tasks/MCP panel |

Both the widths and the hidden/shown state are remembered in the browser (or the desktop window) and restored next time.

A drag stops at 160px, because a panel narrower than the session titles and server names it exists to show is worse than no panel at all. The toggle is what removes one entirely.

### Left panel: sessions

Every session on the daemon, newest first. Each card shows:

| Line | Contents |
|---|---|
| Title | The session's title, or its ID if it has never been renamed, behind a light: **grey** — nothing is running here; **blinking green** — a turn is running; **steady green** — a reply arrived while you were in another session and you have not opened it since. Every card has one, so an idle session says so rather than looking like a panel that does not draw lights. |
| Workspace | The directory the session was created in, shortened from the front (the full path is in the tooltip). This is what distinguishes two sessions that would otherwise look identical because they belong to different projects. |
| Created | Local date and time |

**Clicking anywhere on a card switches to that session** — the transcript, agent, and status bar all follow. The `rename` and `delete` buttons on the card act on that session without switching to it. Above the list is **+ new**, below it **delete all sessions**; both destructive actions confirm first.

**Drag a card up or down to put it where you want it.** The order is saved on the daemon (`POST /api/sessions/order`, and in each session's metadata file), so it survives a restart and is the same in every client. Newest-first is only the starting arrangement: it sinks the conversation you have lived in all week below every throwaway one started since. A session created after you have arranged the panel appears at the top, where a new session has always appeared, rather than at the bottom of an arrangement it was never part of.

The workspace is recorded when the session is created and never rewritten afterwards, so [switching the workspace](#switching-the-workspace-directory) changes where *new* sessions start without relabelling old ones. Sessions created before this field existed show `(workspace not recorded)`.

**Switching to a session also switches the workspace to its directory** — see [Switching the workspace directory](#switching-the-workspace-directory).

The agent isn't shown here — the header dropdown and the [status bar](#status-bar-under-the-prompt) both already carry the current agent, and it's a property of the next message rather than of the session's identity.

### Right panel

| Section | Contents |
|---|---|
| Background tasks | Live status of subtasks started by the `Task` tool or the background task API |
| MCP servers | Names of currently connected servers, from `GET /api/mcp-servers`. Configured servers that failed to connect are absent here and logged as a daemon warning. |

### Drag and drop file attach

Dropping a file on the input box uploads it through `POST /api/sessions/{id}/uploads` to `~/.localcode/uploads/<session id>/<filename>`, and inserts the absolute path into the input box.

The file contents are not shown to the model. The model reads that path itself with `read_file` or `bash` when it needs to. This works well for text files. For images and other binaries the model cannot read them as text, so only the path is useful.

### Status bar under the prompt

One line directly below the input box:

| Element | Behavior |
|---|---|
| Agent and model | Which agent answers the next message, and the model its profile resolves to. The model shows from the moment a session opens, taken from the agent's profile, and switches to whatever the provider actually reports once the first reply arrives. |
| Context use | Yellow past 70%, red past 90% |
| TPS | Shown when `show_tps` is on. It is a **generation** rate: the clock runs from the first token to the last, so the wait before a model starts answering (prefill, and on a shared local server the queue in front of you) is not divided into the output. It covers the whole turn, not the last model call — a turn that uses tools is several calls, and the last is often a handful of tokens. A `~` prefix marks a live estimate made while the model is still talking, counted from stream chunks because the real token count only arrives when the stream ends; it is replaced by the exact figure at that point. A reply that arrives in a single chunk shows nothing at all: there is no interval to measure across. |
| Activity light | Three states: **gray** — no live connection to the model (the event stream to the daemon is down); **solid green** — connected and idle; **blinking green** — a turn is running in this session. It recovers to green on its own when a stopped daemon comes back, since the browser reconnects the stream automatically. "Running" is the daemon's own answer, not this page's memory of having sent something, so it agrees with the light on the session's row in the left panel however the turn started — from another client, or before this page was loaded. |
| Stop button | Appears while a turn is running, whoever started it. Click it to cancel, the same as Esc — which is the faster route but depends on the key reaching the page. |
| Dictation pill | `dictation: off`. Click to talk your prompt instead of typing it; the pill turns red and blinks while it listens. Reads `dictation: unavailable`, disabled with the reason in its tooltip, when no speech model is configured or the build has no recognizer. See [Dictating a prompt](#dictating-a-prompt). |
| Auto-delegate pill | `auto-delegate: on` / `off`. Click to open a panel setting which prompts are delegated and which agent answers them — see [Auto delegation](#auto-delegation). |
| Permission pill | `permissions: ask (N rules)` or `permissions: skip`. Click it to view or change permission settings — see [Viewing and changing permission settings](#viewing-and-changing-permission-settings-without-waiting-for-a-prompt). |

### Switching agents with Tab

`Tab` switches to the next agent and `Shift+Tab` to the previous one, the same as in the TUI — the browser's normal "move focus to the next control" behavior is suppressed so the key means the same thing in both clients. Focus stays in the prompt box. Inside a modal (permissions, workspace) Tab still moves between fields, which is the only thing it could usefully mean there.

The header dropdown does the same thing and lists each agent with the model it resolves to, e.g. `explore (qwen3-1.7b)`; the agent's description is in the option's tooltip.

### Model output renders as markdown

Headers, bold/italic, inline code, fenced code blocks, lists, blockquotes, links, and horizontal rules render as formatted HTML instead of raw text, in both the Web UI and the native GUI window. It's a small built-in renderer with no external dependency, since this stays a fully offline app; anything it doesn't recognize as markdown (including any raw HTML the model writes) shows as plain escaped text rather than being interpreted, so nothing the model outputs can inject markup.

### Watching a long turn

Each tool the model runs gets its own line in the transcript, written the moment the call starts and completed when it ends:

```
▸ bash  go test ./...                                    running…
✓ bash  go test ./...                                    14 lines
```

The marker pulses while the call is running, and turns into `✓` or a red `✗`. Only the tool's name and its main argument are shown, so a file read does not bury the conversation — **click the line** to expand the full arguments and the full result, and click again to collapse it.

This matters most on the turns where the model spends minutes in tools and says nothing: without those lines, the screen shows a blinking light and no other sign that anything is happening. The status bar still names the tool currently running; the transcript is what remains afterwards.

The TUI writes the same one-line entries, without the expandable detail.

A submitted prompt appears immediately, dimmed, and turns into an ordinary `You:` line once the daemon confirms it. The gap between the two is everything that happens before the model is handed the text — hooks, the auto-delegation decision, the first request — which on a remote or loaded model is seconds. Both clients do this; the transcript still holds one entry per message, the one the daemon recorded.

### Redirecting a turn while it runs

You can keep typing while the model is working. A prompt sent during a turn is handed to that turn and picked up at its **next tool call** — so "actually, skip the tests" reaches the model mid-job instead of after it has finished the wrong thing.

Until the model is handed it, the line reads `[sent — the model will pick this up at its next step]`. That is replaced by the normal `You: …` line at the moment the model actually receives it, which is where it appears in the transcript from then on.

Several messages can stack up, and they arrive in the order you typed them. The model is told the text came from you mid-task, so it is not mistaken for tool output.

Two things it is not:

- **Not an interrupt.** A tool already running finishes first; nothing is killed. To stop the work outright, press the stop button or Esc — which also discards anything you had typed while waiting, since the point of stopping is to end the job, not to have it act on your queue afterwards.
- **Not for commands.** `/compact`, `/agent` and the rest don't go through the message endpoint, so they still wait for the turn to end.

If the turn happens to finish in the instant between your pressing Enter and the daemon accepting the message, it is answered as an ordinary next message instead. Nothing is dropped either way.

## Part 7. Agents and automation

### Available tools

| Tool | Needs permission | Purpose |
|---|---|---|
| `read_file` | No | Read a file with line numbers |
| `glob` | No | Find files by pattern, `**` supported |
| `grep` | No | Search file contents by regex |
| `write_file` | Yes | Create or overwrite a file |
| `edit` | Yes | Replace a specific string in a file |
| `bash` | Yes | Run a shell command, 2 minute default timeout |
| `Skill` | No | Load a skill body by name. Registered only when skills exist. |
| `mcp__<server>__<tool>` | Yes, always | Tools from each configured MCP server |
| `Task` | No | Delegate to another named agent and wait for its result. Registered only when 2 or more agents are configured. |

### Combining agents

An `agents` entry with only a `profile` is just routing, meaning "run under this name and get this model". Adding `description`, `prompt`, and `tools` turns it into a genuinely **separate role** that other agents can delegate to through the `Task` tool.

The idea comes from opencode's subagent and model matching, such as `oh-my-opencode` attaching different models to orchestrator, explore, and review roles.

```json
"agents": {
  "build": {
    "profile": "strong",
    "description": "Implements features and fixes bugs.",
    "prompt": "You are the build agent. Delegate research to the explore agent via the Task tool instead of doing it yourself."
  },
  "explore": {
    "profile": "cheap",
    "description": "Fast, read-only codebase search.",
    "prompt": "You are the explore agent. Locate relevant files and summarize quickly.",
    "tools": ["read_file", "glob", "grep"]
  }
}
```

| Field | Meaning |
|---|---|
| `profile` | Which provider and model. Required. |
| `description` | The one line another agent reads when picking a delegate |
| `prompt` | Appended after the base system prompt when running as this agent. Use it to narrow the role, such as "do not modify files" or "be fast and terse". |
| `tools` | The only tools this agent may use. Leave it out for everything, including `Task`. When set, the model sees only those tools, and a call to anything outside the list is refused before it runs. |

**The `Task` tool** registers automatically once `agents` has 2 or more entries. When the model calls `Task({"agent":"explore","prompt":"..."})`:

1. A new `explore` session is created, recording `task.spawned` on the parent, and waits on the `max_concurrent_tasks` semaphore.
2. One turn runs **synchronously** with `explore`'s profile, prompt, and tools. Unlike [background tasks](#background-tasks), the delegating agent's turn waits for this.
3. `explore`'s final answer text is returned as the tool result, and the delegating agent continues from it.

Delegation deeper than 3 levels is refused automatically, so agents cannot recurse into each other forever.

### Plan mode

The same concept as opencode's Plan and Build modes on the Tab key.

The `plan` agent in `config.example.json` allows only `tools: ["read_file","glob","grep"]`, so `write_file`, `edit`, and `bash` are never exposed or executed.

opencode implements Plan mode through an ask permission instead, which has produced reported escapes such as [bash running anyway in plan mode](https://github.com/anomalyco/opencode/issues/20938) and [subagents bypassing the read only restriction](https://github.com/anomalyco/opencode/issues/26514). localcode never shows the tool to the model in the first place and refuses it again just before execution, so that class of bypass is structurally impossible.

**Switching keeps the session's conversation context and changes only which agent answers next.** It does not start a new session.

| Client | How to switch |
|---|---|
| TUI | **Tab** cycles through configured agents. The status line under the input box always shows `agent: <name>  ·  model: <model id>`. |
| Web UI | The header dropdown |
| Both | `/agent` to list, `/agent <name>` to switch |

Switching posts to `POST /api/sessions/{id}/agent`. On success an `agent.switched` event goes to the session, so every attached client, including a TUI and Web UI open at once, updates together.

The usual flow: let `plan` analyze and design with no ability to change files, then Tab to `build` and carry straight on with the context intact.

### Auto delegation

Small, mechanical prompts can be answered by a cheaper agent automatically, without you switching models and without the main model running at all.

**This is off unless you configure it.** With no `auto_delegate` block in config.json, nothing changes from before, so there is nothing you have to do to keep current behavior.

The reason to do this is prompt cache economics, not just the per token price difference. A cache entry is keyed by model as well as by prompt bytes, so changing the session's model part way through throws away the whole cached prefix (tools, system prompt, and every prior turn) and rewrites it at the write premium. On a long session that prefix is the expensive part.

| Operation | Cost against base input price |
|---|---|
| Cache read | about 0.1x |
| Cache write, 5 minute TTL | 1.25x |
| Cache write, 1 hour TTL | 2x |

Delegating sidesteps that entirely. The sub agent runs in its own session with its own history, so the main session's model and prefix never change and its cache survives.

Turn it on in config.json:

```json
{
  "auto_delegate": {
    "enabled": true,
    "agent": "explore",
    "match": ["find *", "where is *", "which file *"]
  }
}
```

Start with a narrow list like this one and widen it once you have seen the answers. `config.example.json` carries a longer list (`search *`, `list *`, `grep *`) you can copy from.

| Field | Meaning |
|---|---|
| `enabled` | The starting value. `/config auto_delegate on\|off`, or the auto-delegate panel under the prompt box, changes it while running. |
| `agent` | Which entry in `agents` handles delegated prompts. Must exist, or startup fails with an error. Changeable at runtime from the [auto-delegate panel](#configuring-it-from-the-web-ui-or-gui-window). |
| `match` | Globs (`*` for any run of characters, `?` for one) tried case insensitively against the whole trimmed prompt. Any one matching delegates the prompt. Editable at runtime from the same panel. |

Behavior:

* The delegated turn shows `[delegated to <agent>]` in the transcript, so a cheaper model answering is visible rather than silent.
* The prompt and the sub agent's answer both enter the main session's history, so the main model has the exchange as context on its next turn even though it never ran for this one.
* Commands are never delegated. `/`-prefixed commands, skills, and `exit`/`:q` are all handled before the delegation check, so a pattern of `*` still leaves them working.
* An empty `match` list delegates nothing. A half written config is inert rather than quietly routing everything to the cheap model.
* Delegation never recurses. A session that already has a parent (any sub agent session) does not delegate again, and an agent never delegates to itself.
* Turning it on with no `auto_delegate` block in config.json tells you so rather than silently doing nothing.

#### Configuring it from the Web UI or GUI window

Clicking the `auto-delegate: on|off` pill under the prompt box opens a panel with all three settings:

| Control | Effect |
|---|---|
| The checkbox | Turns delegation on or off — the same setting as `/config auto_delegate on|off` |
| **Answer them with** | Which agent handles delegated prompts, listed with the model each resolves to (e.g. `explore (qwen3-1.7b)`), so the cost trade-off is visible at the point of choosing |
| **Patterns** | Add or remove match globs one at a time |

Every change **takes effect on the very next prompt** — no restart — and is written back to config.json. Each control rewrites only its own field, so turning delegation off and on again cannot lose the agent and patterns, and editing patterns cannot flip the switch.

You can configure the whole thing from here with no `auto_delegate` block in config.json to begin with; one is created as needed. Until an agent *and* at least one pattern are set, the panel says so explicitly rather than showing an "on" that quietly delegates nothing, and the pill reads `auto-delegate: on (unconfigured)`.

An agent that isn't in the `agents` map is refused with an error, and refused before anything is applied — a typo can't leave the setting half-changed.

#### Choosing what to delegate

There is really only one decision to make here, and the rest of this feature follows from it:

> Which questions are you happy to have answered by the cheaper model?

A delegated prompt is answered at that model's quality. Widen the patterns and you save more but get weaker answers on whatever you swept in.

| Good candidates | Keep on the main model |
|---|---|
| Finding a file or a symbol | Writing or changing code |
| Searching, listing, counting | Design and refactoring |
| "Where is this function?" | "Why is this failing?" (debugging) |

The short version: questions that only **read** are safe to delegate; anything that **produces or changes** something is not.

Practical loop:

1. Start with two or three narrow patterns.
2. Watch for `[delegated to <agent>]` in the transcript and read those answers.
3. Widen the list if they hold up, drop a pattern if they do not.
4. `/config auto_delegate off` turns it off mid-conversation with no restart, and `/config` shows the current state and target agent.

How much this saves depends on how often you ask lookup questions. If you mostly ask for code to be written, little will match and the effect will be small. That is a fine reason to leave it off.

### Background tasks

Start another agent from a parent session and track its progress. The event types are the same as the `Task` tool, `task.spawned` and `task.status`, but this is **asynchronous**. The caller does not wait.

This is API only for now. Neither client has a "run in background" button, only the sidebar that shows status.

```bash
curl -X POST http://127.0.0.1:4096/api/sessions/<parent-id>/tasks \
  -d '{"agent":"explore","prompt":"find every TODO under src/"}'
```

`task.spawned` and `task.status` events, carrying running, completed, failed, or cancelled, flow into the parent session's stream and appear live in the Web UI sidebar and the TUI transcript. Concurrency is capped by `max_concurrent_tasks`.

### Switching models

Changing model inside one conversation is not supported yet. Add a new name to the `agents` map and restart with `--agent <name>`.

```json
"agents": {
  "quick-search": { "profile": "cheap" }
}
```

```bash
localcode --agent quick-search
```

### Attaching a local LLM

1. Load a model in LM Studio and start its local server, by default `http://localhost:1234/v1`.
2. Point `providers.local.base_url` at that address.
3. Set the profile's `model` to exactly the model name LM Studio shows.
4. Point an `agents` entry at that profile and run with `--agent`.

See [MODELS.md](MODELS.md#local-llms-over-an-openai-compatible-endpoint) for more, including remote proxies that need an API key.

### Dictating a prompt

localcode can take a prompt by voice, in the desktop window and in the
Web UI. Click the `dictation: off` pill in the status row under the
prompt box and talk; the text appears as you speak, grey while a
sentence is still in progress and ordinary text once you pause. Click
the pill again to stop. By default everything runs on your machine: no
audio leaves it, and it works with no network at all. Pointing it at
[another machine](#using-a-speech-server-on-another-machine) is the one
thing that changes that, and it says so where you set it.

It is off until an engine and a model are on disk, because there is no
sensible one to guess and guessing would mean a silent several-hundred-
megabyte download the first time anyone clicked the button.

```bash
localcode dictation install
```

On Windows the installer already ran this unless you passed
`DICTATION=0`. It downloads the engine and a Whisper model, checks both
against pinned checksums, and puts them in a `models` directory beside
the binary. About 200MB. No config change is needed: that directory is
where localcode looks when nothing is configured. `localcode dictation
status` reports whether it worked, and `localcode dictation remove`
deletes it again.

localcode publishes a whisper build for Windows only. On macOS and Linux
that command still installs the model — the same file everywhere, and the
larger half of the download — and then tells you where to get the engine.
If one is already on your PATH it says so and gives you the setting to
use it:

```json
{ "dictation": { "whisper_bin": "/opt/homebrew/bin/whisper-server" } }
```

`brew install whisper-cpp` provides that binary. localcode names an engine
it finds on PATH but never runs one without being told to: a build it has
not been tested against is a choice to make deliberately, not one to have
made quietly on your behalf.

#### Engines

| | whisper | sherpa |
|---|---|---|
| Runs as | a child process | linked into localcode |
| Available in | every build | the desktop build only |
| Korean accuracy | good, correctly spaced | poor, and see the note below |
| Text while you speak | re-read about once a second | word by word |
| Download | ~200MB | ~400MB, ~130MB kept |

**whisper is the default and the one to use.** It is
[whisper.cpp](https://github.com/ggml-org/whisper.cpp) running as a
separate program that localcode feeds audio to, which is what makes it
work everywhere: the recognizer is a file beside the binary rather than
something compiled in, so the TUI and the headless daemon can dictate
too.

sherpa is the original engine, kept because it still works where it is
already installed. It is not being developed further. On the Korean
model it ships with, transcripts came out unspaced and inaccurate; the
spacing is fixed as of v0.32.13, the accuracy is not. Install it with
`localcode dictation install --engine sherpa`.

#### Settings window

The ⚙ button in the status row under the prompt box opens a settings
window. Dictation's live there: which microphone to record from, the
spoken language, the speech engine's address and port, and which API
that engine speaks.

The address is two boxes because it is two decisions — the machine
(`192.168.1.50`, or `https://speech.example.com` to use TLS) and the port
(`8080` if you leave it empty, which is what whisper.cpp's server uses
when started without `--port`). Leave the address empty to run the engine
on this machine. Setting it changes nothing else about dictation: the
same grey-then-committed text, the same pause detection, the same
errors — only where the audio is transcribed.

The API dropdown names the dialect the server speaks
(`openai` / `whispercpp` / `whisperx`); left on "work it out on the first
utterance", localcode tries each in turn, which is right for almost
everything and cannot succeed against a server that answers every path.

Two kinds of setting sit in it, and the window says which is which
because they do not behave the same way:

| | Where it lives | Who it applies to |
|---|---|---|
| Microphone | this browser (`localStorage`) | you, on this machine |
| Language, engine address and port, engine API | the daemon's `config.json` | every client attached to it |

A microphone cannot be a daemon setting: a device id means nothing on
another machine, and the daemon never sees audio hardware at all, only
the PCM a page sends it. Device *names* stay hidden until the page has
been allowed microphone access once — and several browsers go further and
report a single anonymous input until then, so a headset or USB
microphone plugged in since is not in the list at all. **Show the
microphones on this machine** appears when that is the case: it asks for
access, stops the recording immediately, and lists what is really there.
Opening the panel asks for nothing on its own. A device plugged in or
unplugged while the panel is open lands in the list without being asked
for.

A daemon started without a `config.json` can still be configured — the
change applies for as long as it runs — and the window says that too,
rather than appearing to save and forgetting.

The spoken language applies as soon as it is chosen — the dropdown is a
finished decision the moment it changes, and closing the panel used to
discard it silently. The engine address keeps the Save button, since a
half-typed host is not something to apply as it is typed.

Getting the language wrong is not a small loss of accuracy. Whisper
writes the audio **in the language it is told**, so Korean speech with
English selected comes back as an English translation, and English speech
with Korean selected comes back spelled out in Hangul — "I'm a boy" as
아이엠어보이. If you dictate in more than one language, leave it on
auto-detect.

**Spoken language applies to whisper only.** Whisper takes it per
request. Sherpa is one model per language and the model `localcode
dictation install --engine sherpa` fetches is Korean, so the setting does
nothing for it — English dictated into that engine is not mistranscribed
or dropped, it is written out in Hangul: "I'm a boy" as 아이엠어보이. The
window says so next to the control when that is the engine in force, and
so does `localcode dictation status`. The fix is to install whisper,
which is multilingual and is used in preference wherever it is
installed:

```
localcode dictation install
```

#### Configuration

The settings window covers the common cases; everything below is the
same thing in the file. None of it is required — set it only to override
what `dictation install` arranged:

```json
{
  "dictation": {
    "engine": "whisper",
    "language": "ko",
    "whisper_model": "/Users/you/.localcode/models/ggml-medium-q5_0.bin"
  }
}
```

| Key | Meaning |
|---|---|
| `engine` | `whisper` or `sherpa`. Empty prefers whisper. |
| `language` | ISO 639-1 code, `ko` or `en`. Empty auto-detects, which is what mixed speech wants and slightly slower for speech that is only ever one language. |
| `whisper_bin` | Path to the engine executable. |
| `whisper_model` | Path to a `ggml-*.bin`, or a directory holding one. When several are installed the largest is used. |
| `whisper_url` | A speech server on another machine. Set, nothing runs locally. See [below](#using-a-speech-server-on-another-machine). |
| `whisper_api` | Which dialect that server speaks: `whisperlive` (streaming), `openai`, `whispercpp` or `whisperx`. Omit to work it out when dictation starts. |
| `threads` | CPU cap. 0 picks a modest default, since this runs beside a language model doing the actual work. |

`dictation_model_dir` still points sherpa at its model directory, which
has to hold the unpacked archive contents — `encoder-*.onnx`,
`decoder-*.onnx`, `joiner-*.onnx`, `tokens.txt` — not the archive.

On Windows **every backslash in a JSON path has to be doubled**, because
a single one starts an escape. `"C:\Users\..."` is not a valid string
at all: the config fails to parse with

```
invalid character 'U' in string escape code
```

and localcode refuses to start, which reads as a broken install rather
than a mistyped path. Forward slashes work on Windows too and avoid the
whole problem.

#### Using a speech server on another machine

A laptop with no GPU and a workstation with one is the usual reason:
point the laptop at the workstation and the transcription happens there.
Nothing is installed on this side — no engine, no model, no child
process — and the microphone, the voice detection and the prompt box all
work exactly as before.

It does not have to be whisper.cpp. Four server dialects are understood,
and which one is in front of you is worked out when dictation starts and
remembered:

| Dialect | Endpoint | Servers |
|---|---|---|
| `whisperlive` | `WS /` | [WhisperLive](https://github.com/collabora/WhisperLive), streaming |
| `openai` | `POST /v1/audio/transcriptions` | anything OpenAI-compatible, including WhisperX's compatibility layer |
| `whispercpp` | `POST /inference` | whisper.cpp's own server, and what localcode runs locally |
| `whisperx` | `POST /asr` | WhisperX's native API |

The first of those is a WebSocket and the other three are uploads, and
the difference is what the text feels like. See
[below](#streaming-from-a-server-that-supports-it).

Discovery costs up to three extra requests once per engine and none after
that, and it is run against half a second of silence rather than against
anything you said. That matters on a slow server: the search used to send
the real utterance to each candidate, sharing that sentence's deadline
between them, so an engine that takes a few seconds had the endpoint that
would have worked cut off part-way through and was reported as having none
of them.

An endpoint that answers a probe wins. One that answers *badly* — a
missing field, a model it does not have — is remembered but not chosen
while another might work, since a WhisperX server offers an
OpenAI-shaped endpoint as well as its own and the first refusing must not
hide the second. If nothing works, that refusal is what gets reported.

Set `whisper_api` to one of the names above to skip discovery. Only an
actual HTTP response settles the choice: a connection that fails, or that
the server closes mid-upload, says nothing about which endpoint is there,
so the search carries on, nothing is remembered, and it is reported as a
server that did not answer rather than one that lacks the endpoints.

The dialect decides where the spoken language is sent, which is why
getting it wrong is quiet: `openai` and `whispercpp` take it as a form
field, and `whisperx` reads it from the query string and ignores form
fields it does not know — so a language chosen in the panel simply had no
effect there, and every utterance was auto-detected.

#### Streaming from a server that supports it

Whisper is not a streaming model: it reads a window of audio and returns
the whole of it. So the upload dialects show text while someone is still
talking by re-sending the utterance so far every 900ms and throwing the
previous answer away — wasted work by construction, and over a network
wasted upload too, since ten seconds into a sentence every preview ships
those ten seconds again.

A streaming server removes both halves of that:

| | Upload dialects | `whisperlive` |
|---|---|---|
| Audio sent | The whole utterance, again, every 900ms | Once, as it is recorded |
| Text appears | Up to a second behind the words | As the server produces it |
| Cost per sentence | Grows with the length of the sentence | Flat |

Nothing else changes. The pause still decides when grey text becomes
committed text, and it is still decided here from the audio rather than
by the server, so dictation behaves the same way on every engine.

localcode tries the streaming dialect first whenever `whisper_url` is
set and `whisper_api` is empty. A server that does not have it answers
the upgrade with an ordinary HTTP status, which costs one request and is
not an error: the upload dialects are tried next, exactly as before.
Setting `whisper_api` to `whisperlive` pins it, and then a server that
does not answer is reported instead of quietly falling back to the
slower path. Setting it to any of the other three skips the streaming
attempt entirely.

Two details of that protocol are worth knowing:

* The spoken language travels in the handshake, not per request. It is
  sent as `null` when the language is left on auto-detect.
* The server is asked for the model named in `whisper_model` when that
  is a plain name (`small`, `large-v3`). A path to a local `ggml-*.bin`
  is not a name a streaming server can load, so `small` is asked for
  instead of passing the path along.

On the other machine:

```bash
python3 run_server.py --port 9090 --backend faster_whisper
```

Then set `whisper_url` to `192.168.1.50:9090`.

When nothing comes out and the reason is not obvious, ask the server
directly:

```
localcode dictation probe
```

It reports whether TCP connects, whether a plain `GET` is answered,
whether the streaming endpoint is there, and what each upload endpoint
says. A server that answers `GET` and resets
every upload is not a wrong endpoint — the address and the paths are
fine, and something is rejecting the audio itself.

If the server wants an OpenAI-style `model` field, set `whisper_model` —
it is sent only when configured, since a server hosting a single model
rejects a name it does not recognise.

```json
{
  "dictation": {
    "whisper_url": "http://192.168.1.50:8080",
    "language": "ko"
  }
}
```

`https://` is honoured. A TLS port answers a plaintext request by closing
the connection with no status and no message, which looks exactly like a
server refusing the request, so an https address quietly downgraded is a
failure with no evidence in it. `localcode dictation probe` detects that
case and says so.

The address is read the way people write one: with or without the
scheme, with or without a trailing slash, and with or without the port
(8080 is assumed, which is what whisper.cpp's server uses when started
without `--port`). `whisper_bin` and `whisper_model` are ignored while it
is set. The [settings window](#settings-window) takes the same address as
two boxes, one for the machine and one for the port, and writes this key.

A remote engine is not a different feature: the recording, the pause
detection, the grey provisional text and the committed text all work
exactly as they do locally, and the only difference is which machine runs
the transcription.

Two things about the decode do not travel, because they are command line
options on an engine localcode starts and a remote one was started by
somebody else. Both are now handled from this side instead:

| Local engine gets | A remote server gets |
|---|---|
| Decoding fallback on (localcode simply does not pass `--no-fallback`) | `temperature_inc=0.2` on every request, which is whisper.cpp's own default and overrides a server started with `--no-fallback`. The OpenAI and WhisperX shapes have no equivalent field; a server behind those that drops unconfident segments has to be restarted without the flag. |
| `--no-timestamps` | Timestamps are removed from the answer, in both shapes whisper emits them (`[00:00:00.000 --> …]` and `<\|0.00\|>`), so a server configured to include them does not put them in the prompt box. |

`threads` is the remote machine's own business and is not sent.

On the other machine, run whisper.cpp's server bound to an address this
one can reach — its default is loopback only, so it needs telling:

```bash
whisper-server --model ggml-small-q5_1.bin --host 0.0.0.0 --port 8080
```

**Recorded audio leaves this machine when you set this.** That is the
point of it, but it reverses what dictation otherwise guarantees, and
the wire is plain HTTP with no authentication — whisper.cpp's server has
none to offer. Treat the address as you would any other unauthenticated
service on your network, and only point it somewhere you would be
willing to send what you say out loud. `localcode dictation status`
prints which engine is in use and says plainly when it is a remote one.

A wrong address fails when dictation starts, naming the address, rather
than as silence the first time you speak — which is indistinguishable
from a microphone that is not working.

#### If nothing appears at all and the microphone will not switch off

That shape is a speech server that accepts the connection and then never
answers, and before v0.43.1 it had no symptom of any kind: every upload
stayed open, no text arrived, no error arrived, and clicking the pill did
nothing, because stopping waited for those uploads to finish.

What happens now:

| After | What you see |
|---|---|
| 20s on one preview, 45s on one committed sentence | The daemon gives up on that transcription and the transcript says so. These are the limits that normally fire. |
| 60s on one upload | The browser gives up on the request itself. A backstop above the daemon's own limits, not the usual path — set below them, it used to abort commits that were about to succeed. |
| 3 failures in a row | Dictation stops itself and says why. |
| A click on the pill | The microphone goes off immediately. It waits up to 1.5s for already-recorded audio to finish uploading and no longer. |

A slow engine is also not asked for a new preview until at least as long as the last one took, and audio queued behind it is uploaded in pieces of at most 512KB rather than as one ever-growing body — which the daemon used to refuse outright (`request body too large`), stopping dictation a few seconds into every attempt.

As of v0.45.1 the request carrying audio never waits for the engine at all: a finished sentence is transcribed on its own and arrives with a later chunk. That is what makes a slow engine slow rather than broken — before it, the engine's time was time the browser spent holding an open request, and everything else followed from that.

When dictation still produces nothing, ask the server directly rather than guessing:

```
localcode dictation probe
```

Run it from `localcode.exe` on Windows — `localcode-gui.exe` has no console to print to. It reports whether TCP connects, whether a plain `GET` is answered, and what each upload endpoint says, which separates a wrong address from a wrong endpoint from a server that takes the audio and never replies.

The daemon end matches: a transcription can be cancelled, so switching the
microphone off no longer queues behind a request that will never land, and
a browser that gives up on a chunk takes that work with it instead of
leaving the session locked.

If the server answers some paths and hangs on others, the dialect search
now gives each candidate its own share of the time, so the endpoint that
works is still reached. `localcode dictation probe` reports which is
which.

#### If words go missing when you speak quickly

Two things caused this before v0.43.0 and both are fixed; they are worth
knowing because the symptoms are still what to look for.

The audio was resampled from the microphone's rate (48kHz, usually) to
the 16kHz whisper takes by keeping every third sample and discarding the
rest. That does not remove the energy above 8kHz, it folds it back down
into the middle of the speech band as noise — and fast speech puts more
energy up there, because sibilants and plosives arrive closer together.
The conversion now low-passes before it decimates.

The engine was also started with `--no-fallback`, which drops any segment
whose decode looks unconfident instead of retrying it. Words running
together is exactly what fails that check, so speaking quickly could
produce no text at all. A remote whisper.cpp server started with that
flag is covered too, as of v0.43.1 — see the table above. Silence is handled on localcode's side instead —
audio is only sent once the microphone has actually heard speech, and
whisper's `[BLANK_AUDIO]`-style annotations are stripped from the reply.

If it still struggles, the model is the next thing to change.

#### Choosing a Whisper model

`dictation install` fetches `ggml-small-q5_1.bin` (190MB). Quantised, so
it holds a correspondingly smaller amount of memory while resident. On
the Korean reference set it transcribed every sentence with correct
spacing, and one 6.6s recording took about 290ms on Apple Silicon.

To use a different one, download it from
<https://huggingface.co/ggerganov/whisper.cpp> into the same `models`
directory. The largest installed file wins, so no config change is
needed. `medium` is more accurate on unclear or noisy speech at roughly
twice the compute; `large-v3-turbo` is a similar size to medium but
optimised for inference and worth benchmarking if small ever struggles.
For a real-time feature, latency is felt far more than the last two
points of accuracy, which is why the default is the small one.

#### macOS and Linux

Upstream publishes a ready-made engine for Windows only, so `dictation
install` can fetch it there and not elsewhere. On macOS and Linux it
says so, and the engine has to be put in place by hand. The model still
downloads normally either way.

For macOS, the `whisper-macos` workflow builds a universal
`whisper-server` and uploads it as an artifact:

```bash
gh workflow run whisper-macos.yml --ref main
gh run download <run-id> -n whisper-server-darwin-universal -D models
```

To build it yourself, on either platform:

```bash
git clone --depth 1 https://github.com/ggml-org/whisper.cpp
cmake -B build -DCMAKE_BUILD_TYPE=Release -DGGML_NATIVE=OFF whisper.cpp
cmake --build build --config Release --target whisper-server
```

`GGML_NATIVE=OFF` matters if the binary will run anywhere but the
machine that built it: ggml otherwise tunes for the local processor and
the result dies with an illegal instruction on an older one.

Put the resulting `whisper-server` in the `models` directory beside the
localcode binary, or name it with `whisper_bin`.

#### Two things worth knowing

- **Text you have already seen can change.** Whisper reads a window of
  audio as a unit and re-reads the whole utterance about once a second,
  so a word can be revised as later words give it context. That is what
  the grey means: it is not settled yet. It stops changing when you
  pause.
- **Mixed Korean and English is the hard case.** Whisper is multilingual
  and handles it far better than a Korean-only model, but
  "useState 훅을 async 함수로 바꿔줘" is still where you will see the
  limits. Leave `language` unset for mixed speech; set it when you only
  ever dictate one language.
- **An utterance commits itself after 30 seconds.** Normally a pause of
  about a second ends one. In a room with no quiet in it — a fan, a
  conversation nearby — that pause never arrives, so a 30 second cap
  ends the utterance anyway rather than letting it grow for as long as
  the session lives.

When dictation cannot run, the pill reads `dictation: unavailable` and
is disabled, with the daemon's own explanation in its tooltip. It stays
on screen rather than disappearing: the usual reason is that nothing has
been installed yet, which is worth knowing about.

#### When a transcript comes out wrong

"The transcript is wrong" is two different faults that need opposite
fixes, and in the finished sentence they look identical:

* The model misheard.
* The model heard correctly and the text was assembled wrongly.

Only the tokens behind the text tell them apart:

```bash
localcode dictation test recording.wav
```

Record a 16 kHz mono 16-bit WAV of a sentence you know, and run it. The
command prints the text, the raw token list, how many tokens mark the
start of a word, how many decoded to nothing, and a one-paragraph reading
of what that combination means. When the text had to be rebuilt from the
tokens to recover its spacing, it says so and shows what the recognizer
itself returned. Desktop build only; on Windows that is
`localcode-gui.exe dictation test`.

Correct tokens with no spaces in the text is a joining fault. Tokens that
decoded to nothing mean pieces are being lost between the model and the
text. Tokens that do not match what you said is the model, and no
decoding change will help.

Without producing a WAV at all, start the desktop window with
`LC_DICTATION_DEBUG=1` and dictate as usual. Every finished sentence is
logged with the same token detail. On Windows:

```bash
cmd /c "set LC_DICTATION_DEBUG=1 && localcode-gui.exe > dictation.log 2>&1"
```

Off by default, because each line contains what was just said out loud.

#### Word boundaries

Spacing is taken from the tokens, not from the recognizer's finished
string. Where the tokens mark word boundaries and the string does not,
localcode reassembles the text from the tokens.

That indirection is there because the two disagree. Measured on Windows
with the Korean model above:

| | |
|---|---|
| tokens | `["는" " 구" "체" "적인" " 돈을" " 남" "겼" "어" "."]` |
| recognizer text | `는구체적인돈을남겼어.` |
| what localcode types | `는 구체적인 돈을 남겼어.` |

The model had the spacing right. Only the joining step lost it, so the
tokens are the better source. Both spellings of a boundary are honoured:
the sentencepiece `▁` prefix, and a plain leading space.

A rebuild only happens when the tokens carry boundaries and the text has
none, which is the exact shape of that fault. A recognizer that spaced
its own text is left alone, and a vocabulary with no boundary marks at
all does not gain spaces between every character.

Two related settings, both about how the model is loaded rather than how
its output is joined. A model whose archive contains a `bpe.model` is
decoded as sentencepiece BPE; one without keeps sherpa's default.
`LC_SHERPA_MODELING_UNIT` forces either way, taking one of sherpa's own
values (`cjkchar`, `bpe`, `cjkchar+bpe`). `LC_SHERPA_DEBUG=1` makes
sherpa print what it loaded and which unit it settled on, on stderr.

On Windows the speech runtime is three DLLs that the MSI installs beside
`localcode-gui.exe`. They are not optional extras: Windows resolves a
program's imports before any of its own code runs, so a copy of
`localcode-gui.exe` moved elsewhere on its own will not start at all.

### Checking for updates

The settings window (⚙ under the prompt) has an **Updates** section with
two buttons, and neither does anything until it is clicked.

| Button | What it does |
| --- | --- |
| Check for updates | Asks GitHub for the latest release of `dennis2lee/localcode` and compares it against this build. |
| Download and install | Downloads the file for this platform, verifies it, and starts the installer. Appears only when there is a newer release *and* this localcode can install it. |

Nothing checks on a timer or on opening the panel. A check is an outbound
request that tells GitHub which version this machine is running, which is
a thing to ask for rather than assume, and an update replaces the program
while someone is using it.

**Where the install button appears.** Only in the desktop window, where
the daemon and the person clicking share a machine. Over `--server` the
check still works and the install does not: it would replace the program
on the *server*, at the request of a browser somewhere else. The panel
says so and offers the release page instead. It is the same rule the
folder picker follows.

**What is downloaded.** The file that matches how localcode was installed:

| Platform | Asset |
| --- | --- |
| Windows amd64 | The `.msi`, which upgrades in place |
| Windows arm64 | The `.zip` (no installer; the panel says where the file is) |
| macOS, from `LocalCode.app` | `LocalCode-x.y.z-darwin-universal-app.tar.gz` |
| macOS, command line | `localcode-x.y.z-darwin-universal.tar.gz` |

It is checked against the SHA-256 GitHub records for the asset before
anything is run, and refused if it does not match or if the size is
wrong — a connection dropped at 90% otherwise produces an installer that
opens, fails halfway, and leaves a broken install. Downloads go to the
user cache directory (`%LOCALAPPDATA%\localcode\updates` on Windows).

**What installing does.** On Windows it runs `msiexec /i` on the
downloaded package: Windows asks for elevation, shows the installer, and
localcode has to close for its files to be replaced — the window offers
to close itself a few seconds after the installer starts. Everywhere else
localcode does not unpack anything over a running install; it says where
the file is and leaves it to you. Unpacking an archive over the program
that is running it is how an update leaves half of two versions on a
machine.

A build that is not a release — one from a working tree, which reports
its version as `dev` — is never offered an update, since there is no
version to compare and every release would look both newer and older.

## Known limitations

* Opening a session shows the end of it, not all of it. A long conversation loads its last few hundred events rather than replaying the whole log, so switching sessions costs the same whatever their length; there is no "load earlier" control yet, so the beginning of a very long conversation is only in its `.jsonl`.

* If an MCP server dies and the reconnect also fails, for example because the executable is gone, its tools return an error on every later call until the daemon restarts.
* There is no auth token. Anyone who can reach the `--listen` address gets the entire API, shell execution included. Expose it only over loopback plus an SSH tunnel.
* On Windows, shell execution resolves to `sh` on PATH, then Git for Windows' `bash.exe` at its usual install paths, then `cmd /c`. Under the `cmd` fallback, bash-only syntax does not work; the bash tool tells the model so in its description. Installing Git for Windows gives the full POSIX behavior.
* `/compact` can still overlap a running turn on the same session. Ordinary messages are serialized (the daemon refuses a second turn, and the client queues and retries it), but compaction does not go through that path.
