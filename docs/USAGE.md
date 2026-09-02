# Usage

LocalCode supports interactive TUI, Web UI, desktop, daemon, and one-shot CLI workflows. Start with `localcode` for local interactive use. Use `localcode run` for scripts and `--server` for an existing daemon.

| Goal | Command |
|---|---|
| Start a local daemon and TUI | `localcode` |
| Open the Web UI | Run `localcode`, then open `http://127.0.0.1:4096` |
| Run one prompt and exit | `localcode run "<prompt>"` |
| Start a daemon without a TUI | `localcode --headless` |
| Connect a TUI to an existing daemon | `localcode --server <url>` |
| Open the desktop window | `localcode-gui` |

## Contents

| Part | Sections |
|---|---|
| [1. Getting started](#part-1-getting-started) | [Run modes](#run-modes), [Remote daemon over an SSH tunnel](#remote-daemon-over-an-ssh-tunnel) |
| [2. Configuration](#part-2-configuration) | [Config file (config.json)](#config-file-configjson), [Managing MCP servers](#managing-mcp-servers-with-localcode-mcp), [Permission rules](#fine-grained-permission-rules), [Permission settings panel](#viewing-and-changing-permission-settings-without-waiting-for-a-prompt), [Switching the workspace directory](#switching-the-workspace-directory), [Hooks](#hooks), [Authenticating with /login](#authenticating-with-login) |
| [3. Project context](#part-3-project-context) | [Skills](#skills), [AGENTS.md](#agentsmd-project-rules), [Auto memory](#auto-memory) |
| [4. Commands and screen controls](#part-4-commands-and-screen-controls) | [Screen controls](#screen-controls), [Running a skill](#running-a-skill), [/init](#init), [Custom commands](#custom-commands), [/tasks](#tasks), [/memory](#memory), [/config](#config), [/compact](#compact), [/usage](#usage), [Other local commands](#other-local-commands) |
| [5. Sessions](#part-5-sessions) | [Switching sessions](#switching-sessions), [Archive](#archiving-a-conversation), [Referring to another conversation](#referring-to-another-conversation-with-name), [Rename and delete](#renaming-and-deleting-sessions), [Context window](#context-window-management), [Session logs](#session-logs), [Restart recovery](#daemon-restart-and-session-recovery) |
| [6. Web UI](#part-6-web-ui) | [Resizing and hiding the panels](#resizing-and-hiding-the-side-panels), [Left panel: sessions](#left-panel-sessions), [Right panel](#right-panel), [Drag and drop attach](#drag-and-drop-file-attach), [Status bar](#status-bar-under-the-prompt), [Switching agents with Tab](#switching-agents-with-tab), [Markdown rendering](#model-output-renders-as-markdown), [Watching a long turn](#watching-a-long-turn), [Redirecting a turn](#redirecting-a-turn-while-it-runs) |
| [7. Agents and automation](#part-7-agents-and-automation) | [Available tools](#available-tools), [Combining agents](#combining-agents), [Orchestration](#orchestration), [Smart Agent](#smart-agent), [Plan mode](#plan-mode), [Auto delegation](#auto-delegation), [Background tasks](#background-tasks), [Switching models](#switching-models), [Python on Windows](#python-on-windows), [Local LLMs](#attaching-a-local-llm) |
| [Known limitations](#known-limitations) | |

## Part 1. Getting started

Default operation: one local daemon, one attached TUI, and a Web UI on the same sessions. Other modes cover scripts, remote daemons, and the experimental desktop window.

### Run modes

```bash
localcode --agent general-purpose
```

| Flag | Default | Meaning |
|---|---|---|
| `--config <path>` | none | Use only this file as config. Without it, `~/.localcode/config.json` and `./.localcode/config.json` are merged, with the project file winning. |
| `--agent <name>` | `general-purpose` | Which model profile to use, resolved through the `agents` map in config |
| `--listen <host:port>` | `127.0.0.1:4096` | Address the daemon binds. The Web UI is served here too. See [When the port is already taken](#when-the-port-is-already-taken). |
| `--server <url>` | none | Do not start a local daemon. Attach the TUI to an already running daemon, which may be remote. |
| `--headless` | `false` | Run the daemon alone with no TUI, exposing the HTTP API and Web UI |
| `--gui` | on for a `-tags gui` build, off otherwise | Open the native desktop window instead of the TUI. `--gui=false` forces the TUI on a build that has the window. |
| `-version`, `--version` | `false` | Print the build version and exit |

Subcommands: `run`, `login`, `mcp`, `version`. `localcode version` is equivalent to `-version`.

### One prompt, no window: `localcode run`

`localcode run` answers one prompt and exits. It is intended for scripts, pipes, and benchmarks.

```
localcode run "what does this repo do?"
echo "summarise the last commit" | localcode run
localcode run --format json --bare --model qwen3:32b "fix the failing test"
```

Default execution characteristics:

* In-process execution with no bound port
* In-memory conversation removed at process exit
* No files under `~/.localcode/sessions`
* No session-list entry

| Flag | Default | What it does |
|---|---|---|
| `--format` | `text` | `text` streams the answer as it arrives; `json` prints one object at the end; `stream-json` prints one event per line, the same events every other client reads |
| `--agent <name>` | `general-purpose` | Which agent from config answers |
| `--profile <name>` | the agent's own | Which model profile to use |
| `--model <id>` | the profile's own | Override the model id inside that profile |
| `--bare` | `false` | Load nothing but the base prompt |
| `--skip-permissions` | `false` | Run tools without asking |
| `--timeout <duration>` | none | Give up after this long, e.g. `90s` |
| `--config <path>` | none | Same as the top-level flag |
| `--session` | `false` | Keep the conversation so it can be continued in the TUI or Web UI. Prints its id on stderr |
| `--server <url>` | none | Daemon to run a kept conversation through. Only meaningful with `--session` |
| `--listen <host:port>` | `127.0.0.1:4096` | Where to look for a running daemon, for `--session` |

The prompt may be an argument or stdin. Use stdin for prompts that contain newlines or quotes.

| Behavior | Detail |
|---|---|
| `--bare` | Loads only the base system prompt, workspace, and tools. Excludes `AGENTS.md`, `CLAUDE.md`, skills, memory, custom commands, and hooks. Creates no memory-index directory. Suitable for controlled comparisons. |
| Permission request | Refused immediately because no interactive client can answer it. `--skip-permissions` is equivalent to `/permission-skip-all` for the run. |
| Failure | Non-zero exit status. JSON output includes an `error` field. |
| Smart Agent | Supports the six specialists plus `Task`, `TaskBackground`, and `TaskCollect` when `smart_agent` is enabled. |
| Orchestration | Available when `orchestrate` is enabled, but every plan requires permission. Use one of the following settings for unattended execution. |

```
localcode run --skip-permissions "..."
```

```json
"permission": { "Orchestrate": "allow" }
```

`Task` delegation remains available. Each delegated session applies its own tool permissions.

Unavailable tools:

* `Schedule`: the process exits before scheduled execution
* `session_read`: no other conversation is available
* Debate: requires an interactive conversation

The process waits for outstanding background tasks before exit. The wait is reported on stderr and remains subject to `--timeout`.

`--session` preserves the conversation:

| Daemon state | Where it runs | When it appears |
|---|---|---|
| A daemon is listening | On the daemon | Immediately, in the TUI and Web UI |
| Nothing is listening | Here, written to disk | Next time a daemon starts |

The daemon reads the session directory only at startup. Routing a kept session through a running daemon provides immediate visibility and avoids concurrent writers.

`--bare`, `--profile`, `--model`, and `--skip-permissions` apply to the local `run` process. When any of these flags is combined with `--session`, the turn runs locally even if a daemon is available. The command reports this choice.

Common commands:

| Command | What it does |
|---|---|
| `localcode` | Starts a local daemon and attaches the TUI. Open `http://127.0.0.1:4096` in a browser to use the Web UI on the same sessions at the same time. |
| `localcode --headless --listen 0.0.0.0:4096` | Daemon only. Meant for a remote server. |
| `localcode --server http://host:4096` | TUI only, attached to an existing daemon. |
| `localcode-gui` | Experimental native desktop window. Built with `-tags gui`. See [Desktop window](#desktop-window-experimental). |

#### When the port is already taken

`localcode` needs the `--listen` address for its daemon. Address conflicts are handled as follows:

| What holds the address | What happens |
|---|---|
| A localcode daemon in the same directory | Attach the TUI and use that daemon's sessions. |
| A localcode daemon in another directory | Start a new daemon on a free port and report both workspaces. |
| Another process, implicit default address | Bind a free port and report the Web UI address. |
| Another process, explicit `--listen` | Return an error. Stop the process or choose another address. |

Workspace matching uses resolved physical paths. A symlink and its target count as the same directory, including `/tmp` and `/private/tmp` on macOS.

Before attachment, the TUI verifies `GET /api/version` and `GET /api/workspace`. A non-localcode server is treated as another process. A localcode server that does not report its workspace is treated as a different workspace.

The desktop window uses an OS-assigned private port and does not have this conflict.

### Desktop window (experimental)

The desktop build runs the Web UI in a native window on a private loopback port.

```bash
./localcode-gui
```

A `-tags gui` build enables GUI mode by default. Use `--gui=false` for the TUI. The native views are WKWebView on macOS and WebView2 on Windows.

Startup behavior:

* Immediate startup screen with current phase and MCP server name
* Lazy AWS configuration and credential loading on the first Bedrock request
* Persistent error screen on startup failure
* No console output requirement for `localcode-gui.exe`

Platform support:

| Platform | Support and build |
|---|---|
| macOS | `make dist-mac-gui` builds universal `LocalCode.app`. `make gui-mac` builds `localcode-gui`. WKWebView is part of macOS. |
| Windows | `.github/workflows/gui-windows.yml` builds `localcode-gui.exe` on Windows. The MSI installs it with `localcode.exe`, adds a **LocalCode (Desktop)** shortcut, and runs the WebView2 Evergreen Bootstrapper when needed. Missing network access, an existing runtime, or a non-reinstalled bootstrapper does not fail installation. |
| Linux | No desktop build. `--gui` returns an error. Use the browser Web UI. WebKitGTK would require a distribution-specific CGo build. |

`localcode-gui.exe` is linked with `-H windowsgui`. Launching it from `cmd` returns the prompt immediately and Explorer shows no console window. Run console-only subcommands such as `version`, `mcp`, and `login` through `localcode.exe`.

Window chrome:

| Platform | Behavior |
|---|---|
| macOS | Transparent native title bar with hidden title text. Native drag and window controls remain available. |
| Windows | Frameless window with a 28px drag area, six-pixel resize edges, double-click maximize, and page-provided window buttons. Alt+F4 and taskbar Close remain available. |

Build an MSI containing the GUI with `make dist-msi VERSION=x.y.z GUI_EXE=path/to/localcode-gui.exe`.

Windows writes `%LOCALAPPDATA%\localcode\gui-frame.log` on every launch. The file records frame removal, window-style changes, and drag or resize messages. It is replaced on each launch.

For Windows frame, drag, resize, or button problems, enable the standard title bar:

```
set LOCALCODE_TITLEBAR=1
localcode-gui.exe
```

The unsigned macOS `.app` requires right-click and **Open** on first launch. A non-GUI build accepts `--gui` and returns a clear error; GUI mode remains disabled by default. Other run modes are unchanged.

Click the workspace path at the top of the window to open the OS folder picker. See [Switching the workspace directory](#switching-the-workspace-directory).

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

Configuration uses a global file plus an optional project override. Use `config.example.json` as the complete reference. Runtime controls can change selected settings without a restart.

### Config file (config.json)

| Scope | Path | Precedence |
|---|---|---|
| Global | `~/.localcode/config.json` | Base settings |
| Project | `<project>/.localcode/config.json` | Overrides global settings |

```json
{
  "providers": {
    "bedrock": { "type": "bedrock", "region": "us-west-2" },
    "local":   { "type": "openai-compat", "base_url": "http://localhost:1234/v1" }
  },
  "profiles": {
    "strong":   { "provider": "bedrock", "model": "us.anthropic.claude-opus-4-6-v1", "max_tokens": 8192 },
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

| File | What it is |
|---|---|
| `config.example.json` | Complete key reference plus a working orchestration setup with three providers, three `smart-*` profiles, Smart Agent, and six specialist definitions. |

#### Comments: the file is JSONC

Supported JSONC syntax:

* `//` line comments
* `/* */` block comments
* Trailing commas before `}` or `]`
* Literal `//` inside strings, including `https://example.com/v1`

LocalCode preserves comments, indentation, key order, and unrelated bytes when updating an existing key. It refuses to add a missing key to a commented file because there is no unambiguous insertion point. The runtime change still applies, and the key can be added manually.

#### Values from the environment: `{env:NAME}`

Any string value may include an environment placeholder. The syntax is compatible with opencode.

```json
{
  "providers": {
    "anthropic": { "type": "anthropic", "api_key": "{env:ANTHROPIC_API_KEY}" },
    "local":     { "type": "openai-compat", "base_url": "{env:LLM_URL:-http://localhost:1234/v1}" }
  },
  "profiles": { "main": { "provider": "anthropic", "model": "{env:LOCALCODE_MODEL:-claude-opus-4-6-v1}" } },
  "mcp_servers": {
    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": { "GITHUB_TOKEN": "{env:GITHUB_TOKEN}" } },
    "remote": { "url": "https://{env:MCP_HOST}/mcp", "headers": { "Authorization": "Bearer {env:MCP_TOKEN}" } }
  }
}
```

| Form | Meaning |
|---|---|
| `{env:NAME}` | Required. The variable must be set and non-empty, or the config does not load. |
| `{env:NAME:-fallback}` | Optional. The text after `:-` is used when the variable is unset or empty. |

Rules:

* Valid in every string, including URLs, model IDs, MCP environments, and headers
* Multiple placeholders and embedded placeholders supported
* Missing required variable reported with its field and source file
* Placeholder retained on disk during LocalCode updates
* Invalid placeholder-like text such as `{envelope}` or `{env: something}` left unchanged

Use placeholders for portable configuration without embedded secrets. [`localcode login`](#authenticating-with-login) stores Anthropic and Bedrock credentials outside config.json.

#### Top level fields

| Field | Meaning |
|---|---|
| `providers` | Model backend connection details. `type` is `bedrock`, `anthropic`, or `openai-compat`. Bedrock AWS configuration is loaded lazily on its first request. |
| `profiles` | A named provider and model pairing. `max_tokens`, `temperature`, `context_window` and `keep_going` are optional. |
| `agents` | Maps an agent name to a profile. `--agent` resolves through this. An unknown name falls back to `default_profile`. |
| `max_concurrent_tasks` | Maximum concurrent background tasks. Default: 1. Synchronous `Task` calls do not consume slots. Provider-specific limits are acquired first so waiting on one endpoint does not occupy a daemon-wide slot. |
| `mcp_servers` | Same shape as Claude Code's `.mcp.json`, so existing entries copy over directly |
| `permission` | Fine grained allow/ask/deny rules per tool. See [Permission rules](#fine-grained-permission-rules). |
| `update_url` | Where the update button looks instead of GitHub: an https address at which the current installers are published. Unset means GitHub. See [Updating from somewhere other than GitHub](#updating-from-somewhere-other-than-github). |
| `skip_permissions` | The daemon default for `skip_all`: turns every "ask" into "allow", the workspace boundary included. Off unless set; explicit deny rules still deny. See [The four switches](#the-four-switches). |
| `skip_tool_permissions` | The daemon default for `skip_tools`: every tool prompt allowed, and a path that leaves the workspace still asked about. |
| `read_outside_workspace` | The daemon default for `read_outside`: reading outside the session's workspace without being asked. |
| `write_outside_workspace` | The daemon default for `write_outside`. |
| `hooks` | Shell commands run at lifecycle points. See [Hooks](#hooks). |
| `auto_delegate` | Sends matching prompts to a cheaper agent. See [Auto delegation](#auto-delegation). |
| `smart_agent` | Turns on the built-in specialist roster and the orchestration prompt. Off unless set to true; also `/config smart_agent` and the settings window. See [Smart Agent](#smart-agent). |
| `auto_compact_enabled` | Automatic compaction past the threshold. On unless set to false; `/auto-compact` toggles it. |
| `auto_compact_percent` | The threshold, as a percent of the context window. `50` unless set; `/auto-compact <percent>` changes it live and saves it. |
| `auto_memory_enabled` | The notes the model keeps for itself across sessions. On unless set to false. See [Auto memory](#auto-memory). |
| `show_tps` | The tokens per second reading under the prompt. On unless set to false; also `/config show_tps`. |
| `trace_max_age_days` | How long a day of the Smart Agent turn log is kept. 30 when unset; zero or below means that default, not "keep forever". See [What it did](#the-turn-log). |
| `trace_max_total_mb` | Optional cap on the trace directory, and separately on the prompt-manifest directory beside it. When set, each is bounded on its own: the oldest files go until it fits, and today's file is never removed. See [What it did](#the-turn-log). |
| `default_profile` | The profile used when an agent name resolves to nothing. |

#### Profile fields

| Field | Meaning |
|---|---|
| `provider` | Key into `providers` |
| `model` | Model id, as the provider names it |
| `max_tokens` | Maximum output tokens per reply. Default: 4096. Reduced to fit remaining context space. Reaching the cap is reported. This is a configured limit, not a discovered model property. |
| `temperature` | Sampling temperature |
| `keep_going` | Maximum automatic continuations for Muse models. Zero or unset: 3. `-1`: disabled. Ignored for other model families. See [Continuation behavior](#a-model-that-stops-mid-task). |
| `fallback` | Other profile names to try, in order, when a request to this one fails for a reason another model could survive. Read only with [Smart Agent](#smart-agent) on. See [Fallback chains](#fallback-chains-when-a-model-will-not-answer). |
| `context_window` | Total input and output limit. Discovery uses `GET /v1/models` or llama.cpp `/props`, then model-ID matching, then 128k. An explicit value overrides discovery. Do not exceed the model's actual limit. |

#### A model that stops mid-task

`keep_going` continues an incomplete Muse-model turn by submitting `carry on`. It does not apply to other model families.

| Control | Scope | Behavior |
|---|---|---|
| `/keep-going` or GUI checkbox | Daemon | Enables or disables the feature and saves the value to config.json |
| Profile `keep_going` | Profile | Maximum continuations per turn |
| Unset or `0` on a Muse model | Profile | Default budget of 3 |
| `-1` on a Muse model | Profile | Disabled for that profile |

Model matching is case-insensitive and requires `muse` anywhere in the model ID, including `Muse-Glimmer-30B` and `my-muse-variant`.

```json
{
  "profiles": {
    "eager": { "provider": "local", "model": "muse-glimmer-30b", "keep_going": 5 },
    "quiet": { "provider": "local", "model": "muse-small-7b", "keep_going": -1 }
  }
}
```

No continuation occurs in the following cases:

| Not carried on when | Because |
|---|---|
| No tool ran | The turn may be a completed answer |
| Last tool call refused | Further work lacks approval |
| Reply ends with a question | User input is required |
| Reply reached `max_tokens` | The output cap must be increased |
| Previous continuation produced no new tool call | The task is complete or repeating work |
| User message already queued | The user message takes precedence |

A tool call counts as new only when the same arguments have not already appeared in the turn. A build repeated after a file edit counts as new work because the intervening edit is new. Each automatic continuation is recorded in the transcript.

#### Provider fields

| Field | Meaning |
|---|---|
| `bedrock.region` | AWS region, for example `us-west-2` |
| `bedrock.profile` | AWS named profile to use, such as one created by `localcode login bedrock`. Omit it to use the default AWS credential chain. |
| `anthropic.api_key` | Optional. Omit it and the key stored by `localcode login anthropic` in `~/.localcode/credentials.json` is used. |
| `anthropic.base_url` | Defaults to `api.anthropic.com`. Override it to go through a corporate proxy. |
| `openai-compat.base_url` | The URL prefix in front of `/chat/completions` |
| `openai-compat.api_key` | Optional, usually unnecessary for a local server. Sent as `Authorization: Bearer <key>`. |
| `<type>.max_concurrent_tasks` | Concurrent background-task limit for one provider endpoint. Acquired before the global limit. Zero or unset: unlimited. Maximum: 64. Validated at startup. |

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

Supported transports: `stdio` for a local child process, `http` for streamable HTTP, and `sse` for legacy HTTP+SSE. All tools use the name `mcp__<server>__<tool>`.

```json
{
  "mcp_servers": {
    "github":  { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": { "GITHUB_TOKEN": "..." } },
    "hosted":  { "type": "http", "url": "https://example.com/mcp", "headers": { "Authorization": "Bearer ..." } },
    "legacy":  { "type": "sse", "url": "https://example.com/sse" }
  }
}
```

* Omitted `type`: `http` when `url` exists, otherwise `stdio`. Legacy SSE requires explicit `"type": "sse"`.
* Remote credentials: `headers`. `mcp list` and `mcp get` print header names but never values. User headers cannot replace protocol headers.
* Timeout: no general request timeout; 60-second limit when a connected server never answers.
* Permission: every MCP tool call requires confirmation, regardless of server annotations.
* Connection failure: only the affected server is skipped. The daemon starts and logs a warning.
* If a connected server's session dies, the next call retries the connection once.

Invalid configuration, including a profile with a missing provider, stops startup with an error.

### Managing MCP servers with `localcode mcp`

`localcode mcp` manages the `mcp_servers` map without starting the daemon or TUI. A running daemon applies changes after restart or reconnect.

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
| `-H`, `--header "Key: Value"` | Repeatable. Remote servers only. Use for authentication headers. |
| `-s`, `--scope` | `global`, the default, or `project` |
| `--` | Everything after it is the command and its arguments. Always use it so flags meant for the server, such as `-y`, do not get read as flags for `localcode mcp` itself. |
| `remove` without `-s` | If the same name exists in both global and project, nothing is deleted and you get an ambiguity error. Say which with `-s global` or `-s project`. |

The commands modify only `mcp_servers`. Manual JSON edits remain supported.

#### `mcp list` tests each connection

`localcode mcp list` starts or connects to every server, completes the MCP handshake, lists tools, and prints one status line per server.

```
github         global    ok (26 tools)
hosted         global    ok (4 tools)
local-fs       project   ok (11 tools)
weather        project   failed: connect (node): fork/exec node: no such file or directory

1 of 4 server(s) failed to connect.
```

Output excludes config paths, commands, URLs, environment keys, and header keys. Use `localcode mcp get <name>` for the full definition.

Connection failures are status results, not command failures. Each check has a 20-second timeout. Use `--no-test` to list registrations without starting or connecting to servers.

#### Importing from Claude Code

`localcode mcp import-claude` imports servers from project `./.mcp.json`, global `~/.claude.json`, and its current-project block. Project entries override global entries with the same name. Local and remote fields are copied without conversion: `type`, `url`, `headers`, `command`, `args`, and `env`.

```bash
localcode mcp import-claude                  # into ~/.localcode/config.json (global, the default)
localcode mcp import-claude -s project       # into ./.localcode/config.json instead
localcode mcp import-claude --skip-existing  # leave servers that already exist under that name alone
```

Default behavior overwrites existing names. `--skip-existing` preserves them. Entries without a command or URL are reported by name and skipped.

### Fine grained permission rules

Default behavior without any rules:

| Tool | Confirmation |
|---|---|
| `read_file`, `glob`, `grep` | Runs immediately |
| `bash` running a `git` command | Runs immediately (built in default, see below) |
| `write_file`, `edit`, `bash` running anything else, MCP tools | Always asks |

Git commands run without confirmation by default. An explicit `bash` rule for Git can change the decision to `ask` or `deny`. Any explicit tool rule overrides its built-in default.

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
| `read_file`, `write_file`, `edit` | Target file path |
| `grep`, `glob` | Search directory |
| `check` | Configured verification command |
| Other tools, including MCP tools | No subject; use a flat decision or `*` pattern |

Patterns are globs where `*` is zero or more characters and `?` is exactly one.

#### The four switches

Permission switches belong to each conversation. Configuration values provide the initial defaults.

| Switch | Command | What it allows without asking |
|---|---|---|
| `skip_all` | `/permission-skip-all` | Everything, the workspace boundary included |
| `skip_tools` | `/permission-skip-tools` | Every tool prompt, and **not** a path that leaves the workspace |
| `read_outside` | `/read-outside` | Reading outside the workspace (`read_file`, `grep`, `glob`) |
| `write_outside` | `/write-outside` | Writing outside the workspace (`write_file`, `edit`) |

Use `skip_tools` to suppress tool prompts while retaining workspace-boundary prompts.

config.json holds the **defaults** for a conversation that has not answered for itself:

```json
{
  "skip_permissions": false,
  "skip_tool_permissions": false,
  "read_outside_workspace": false,
  "write_outside_workspace": false
}
```

The panel and commands save changes with the session. Background tasks follow their parent conversation's current settings.

> `skip_all` allows file writes and shell commands anywhere on the machine without confirmation. Use it only in an acceptable isolation boundary. Explicit `deny` rules remain effective.

```json
{
  "skip_permissions": true,
  "permission": { "bash": [{ "match": "rm *", "decision": "deny" }] }
}
```

`deny` can block tools that never needed confirmation. For example, this blocks reading `.env` files while leaving every other read alone:

```json
{ "read_file": [{ "match": "*", "decision": "allow" }, { "match": "*.env", "decision": "deny" }] }
```

`bash` rules apply to each command segment. LocalCode splits unquoted `&&`, `||`, `;`, `|`, and newlines. Every segment must resolve to `allow`; any `deny` rejects the full line. Quoted separators remain part of their command.

Command substitution and output redirection never auto-allow: `$(...)`, `` `...` ``, `<(...)`, `>(...)`, `>`, and `>>`. These forms require confirmation unless an explicit rule denies them.

### Answering a permission prompt: once, this session, or always

Ordinary tool prompts provide the following choices. Workspace-boundary prompts use the choices in [Leaving the project](#leaving-the-project).

| Answer | TUI key | Effect |
|---|---|---|
| Allow once | `y` | Approves exactly this call. Asks again next time. |
| Deny | `n` | Refuses this call. Asks again next time. |
| Allow for session | `s` | Approves this call and later matching calls until session deletion or daemon restart. Writes nothing to disk. |
| Always allow | `a` (only shown when available) | Everything "allow for session" does, plus writes a matching rule to config.json, so the same pattern is auto-allowed in every future session too. |

The Web UI shows the same four as buttons: Deny, Allow for session, Always allow, Allow once.

For `bash`, session and permanent grants generalize the first word. Approving `npm test` grants `npm *`. File and MCP grants use the exact subject. The prompt shows the resulting pattern.

"Always allow" updates the file passed through `--config`, or global `~/.localcode/config.json` when no explicit file is set. It never updates the project override. Only the `permission` key changes. Without a writable config target, this option is unavailable.

Session grants are forgotten when a session is deleted, and when the daemon restarts. Permanent ("always") grants live in config.json and survive both.

The Web UI locks the prompt box while the permission modal is open. The TUI keeps its separate permission line and shows a one-time hint if Enter is pressed while a request is pending.

### Viewing and changing permission settings without waiting for a prompt

The permission pill shows the current conversation state: `permissions: ask (N rules)`, `permissions: tools skipped`, or `permissions: skip`. Click it to open controls for:

* shows the four switches for the conversation you are in, and says where each answer came from: this conversation, the one that started it, or config.json
* lists the directories this conversation has approved leaving the project for, each with a **forget** button (the same thing `/read-outside mem-clear` does)
* lists every rule currently in `permission`, with a remove button per rule
* adds a new rule by tool name, match pattern, and decision (allow/ask/deny)

Changes apply on the next tool call. Rule changes use the same config target as "Always allow." Without a config path, persistent controls are disabled and the panel explains why.

### Switching the workspace directory

Each session has its own workspace. Relative file paths and shell commands resolve from that directory. Click the workspace path in the GUI or Web UI to change it without restarting.

What that click does depends on where you are:

| Where | Clicking the workspace |
|---|---|
| GUI window | Opens the native folder picker at the current workspace. Selection applies immediately; cancel leaves the workspace unchanged. |
| Browser | Accepts an absolute server path. Browser directory APIs do not expose a usable server path. |

The folder icon opens the current directory in Explorer, Finder, or the configured `xdg-open` handler. This action is available only in the desktop window because it operates on the daemon host.

Session workspaces are independent. Reopening a session restores its recorded directory.

Delegated tasks inherit the parent's workspace at task start. This applies to `Task`, `TaskBackground`, tasks API calls, and nested delegation. Later parent moves do not affect running tasks. Custom-command `@file`, command substitutions, and hooks use the session workspace.

The system prompt is rebuilt each turn with the current workspace. Absolute paths and `cd` prefixes from earlier turns remain historical data.

A workspace move does not rewrite conversation history. Check commands that contain an earlier absolute path before approving them.

The workspace boundary checks file-tool paths, not shell semantics. A `write_file` call to an old workspace prompts. A shell command such as `cd /old && touch x` is governed only by shell permission rules.

Workspace changes are refused while that session has a turn in progress. Work in other sessions does not block the change.

A request without a session changes the default for new sessions and for sessions without a recorded workspace.

### Hooks

Hooks run shell commands at lifecycle events. Blocking hooks can prevent the associated action.

Hooks and permissions are separate checks. `pre_tool_use` runs first. A successful hook does not bypass an `ask` or `deny` permission decision.

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
| `pre_model` | `session_id`, `agent`, `model`, `provider` | Yes, the request is never sent and the turn ends with an error |
| `post_model` | `session_id`, `agent`, `model`, `stop_reason`, `input_tokens`, `output_tokens`, `cache_read` | No, the reply has already arrived |
| `delegate` | `session_id`, `agent`, `prompt` | Yes, the sub agent is never created and the calling model gets a failed tool call |
| `compact` | `session_id`, `reason` (`automatic` or `overflow`) | No |
| `retry` | `session_id`, `from_model`, `to_model`, `error` | No, the switch has already been decided |

Every payload also carries **`cwd`**: the workspace of the session the event is about.

Other details:

* Hooks run in the event session's `cwd`, including delegated and moved sessions.
* `pre_model` may print `{"context":"..."}` to append context to one model call. This changes the cached system-prompt prefix.
* `matcher` applies only to `pre_tool_use` and `post_tool_use`. It is a full-name regular expression. Examples: `bash|edit`, `mcp__github__.*`. Omit it for every tool.
* Blocking result: `{"decision":"block","reason":"..."}` on stdout, or exit 2 with the reason on stderr.
* Other non-zero exits produce a warning and allow execution to continue.
* Timeout: 30 seconds per hook. Multiple hooks run in registration order and stop at the first block.
* Project hooks replace global hooks per event.

### Authenticating with `/login`

Run `localcode login <bedrock|anthropic>` in a terminal before starting the daemon. Credentials remain outside config.json. Bedrock login does not require the AWS CLI.

> claude.ai Pro and Max subscription login is not supported. LocalCode uses published AWS and Anthropic authentication flows, not Claude Code's private OAuth client.

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

Project rules, skills, and auto memory supply reusable context. Rules load each turn. Skill bodies load on demand. Auto memory stores project notes across sessions.

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

Only skill names and descriptions enter the initial system prompt. The full body loads when the skill is invoked.

To reference other files such as `scripts/*.py` from the body, write relative paths and let the model read them with `read_file` or `bash`.

Run a skill directly by its own name with `/<skill name>`. See [Running a skill](#running-a-skill).

### AGENTS.md project rules

Project rules in `AGENTS.md` are appended to the system prompt. LocalCode searches the workspace and parent directories up to the Git repository root.

`CLAUDE.md` is recognized as a fallback in the same places, so an existing Claude Code file is reused as is.

Both `~/.localcode/AGENTS.md` and `~/.claude/CLAUDE.md` apply globally when present. Global and project rules are combined.

Rules are resolved from the session workspace and reread at each turn. Workspace moves and rule edits apply on the next message.

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

Per-model prompt notes address known output-format and continuation behavior.

* Model IDs containing `gemma`: request Markdown and literal Unicode characters instead of LaTeX commands.

* Model IDs containing `muse`: request completion of the task within the current turn. See [`keep_going`](#a-model-that-stops-mid-task).

The Web UI converts supported LaTeX symbols and text-format commands to readable text. Supported forms include `\rightarrow`, `\sim`, Greek letters, relations, `\text`, `\mathbf`, `\mathrm`, and `\boldsymbol`, including nested commands. Only dollar spans containing a LaTeX command are converted. `$PATH`, `$5`, and ordinary formulas remain unchanged.

Matching uses lowercase model-ID substrings in `modelQuirks`, defined in `internal/agent/quirks.go`. Entries may also set a default `keep_going` budget.

### Auto memory

Auto memory stores model-written project notes across sessions. Typical contents include build commands, debugging findings, and user preferences.

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

Use commands for explicit session actions and settings. Use Esc to cancel a turn. Ordinary messages can redirect the model at a tool boundary.

### Screen controls

Common to the TUI and Web UI:

| Action | How |
|---|---|
| Send a message | **Enter**. The Web UI also has a Send button. |
| Insert a newline | **Ctrl+J** in the TUI, **Shift+Enter** in the Web UI |
| Answer a permission prompt | `y`, `n`, `s`, or `a` in the TUI; buttons in the Web UI. See [answering a permission prompt](#answering-a-permission-prompt-once-this-session-or-always) |
| Cancel the running turn | **Esc**, in either client |
| Recall a previous prompt | **Up** and **Down**, in either client |
| Jump between your own prompts | **Alt+Up** and **Alt+Down**, Web UI only. Moves the view to the previous or next turn of yours and marks where it landed; it does not touch what is in the prompt box, which is what plain Up and Down are for. The TUI marks turns the same way but has no key for this. |
| Switch agent | **Tab** for the next, **Shift+Tab** for the previous, in either client |
| Quit the TUI | **Ctrl+C**, or type `exit` or `:q` |

Other behavior:

* The input box grows as you type, up to about 10 lines, then scrolls internally.
* A permission prompt appears whenever the model wants `write_file`, `edit`, a non-git `bash` command, or an MCP tool. Any client attached to the session can answer it, and answering closes the prompt on every other client.
* The TUI draws a rule above and below the input box, with a status line directly underneath showing `agent: <name>  ·  model: <model id>`. Switching with Tab updates only that line and adds nothing to the transcript.
* The TUI places the real terminal cursor at the insertion point inside the prompt box, so IME composition for Korean, Japanese, and Chinese renders in the box while you type rather than below it.
* **Running work shows below the prompt box, not in the conversation.** While a turn is in flight the TUI animates a line naming what it is doing (the running tool's name, or `working`), the queue depth, and how many background tasks are going. It disappears the moment the turn ends. The Web UI shows the same information in its status bar. Tool starts and finishes no longer write `[tool] ...` lines into the transcript.

Both clients follow new output only while the transcript is at the bottom. Scrolling up pauses following. Returning to the bottom resumes it. In the Web UI, **↓ latest** jumps to the bottom. Sending a prompt or opening a session also moves to the bottom. Background-task windows use the same behavior.

TUI transcript scrolling: `PgUp` and `PgDn` by screen, `Shift+Up` and `Shift+Down` by line. Plain arrows move within the prompt or recall history. Resizing the prompt box preserves transcript following.

The Web UI initially loads the most recent events. If earlier events are omitted, **Load the whole conversation** reloads from the first event. Switching sessions returns to the default recent-event view.

Compaction changes model context, not the append-only session log. The full transcript remains available until the session is deleted.

Esc cancels the current turn and clears queued messages. The transcript records `[cancelled]`. Esc has no effect while idle.

Up recalls older prompts and Down recalls newer prompts. Moving beyond the newest entry restores the draft that was present before recall.

Recall starts with Up on the first prompt line or Down on the last line. Inside a multiline prompt, arrows move the cursor. Once recall starts, arrows continue through history. Editing a recalled prompt ends recall. Consecutive duplicate prompts are stored once.

Prompt history belongs to the session. It combines newly sent prompts with prompts replayed from the session log, including messages from other clients. No separate history file is written.

Up to 200 entries per session are kept.

Unsent drafts also belong to the conversation. Switching sessions in either client restores the selected conversation's draft.

Messages sent during a turn appear immediately and are delivered at the next tool boundary. Multiple messages retain their order. If the turn ends before delivery, the message starts the next turn. See [Redirecting a turn](#redirecting-a-turn-while-it-runs).

Slash commands are refused while a turn is running. Wait for completion or press Esc to cancel. TUI `exit` and `:q` remain available.

### Running a skill

Type a skill's own name as a command. You do not have to wait for the model to decide to call the `Skill` tool.

| Command | Effect |
|---|---|
| `/skill` | Lists registered skill names and descriptions instantly, with no model call |
| `/<skill name>` | Runs that skill, for example `/pdf-tools` |
| `/<skill name> <request>` | Runs the skill with your request attached, for example `/pdf-tools merge a.pdf and b.pdf` |

The transcript keeps just the short command you typed. The full skill body goes only to the model.

**Completing a name.** Type part of one and press the right arrow. In both the TUI and the Web UI, a `/name` completes against the installed skills and the custom commands, and pressing the key again offers the next match:

```text
/p     ->  /pdf-tools  ->  /plan-review  ->  /pptx  ->  /p
```

Completion cycles through matches and then restores the original prefix. The hint below the prompt shows the first match and match count. Editing the text restarts completion.

Completion applies to the word at the cursor, including command names inside a sentence. The rest of the prompt remains unchanged. Example: `read the mail, then run /tidy-context`.

Completion limits:

* Inside a word, Right moves the cursor. Completion requires the cursor at the word's end.
* Complete a command before adding a Korean particle. For example, the cursor inside `/tidy-context를` does not trigger completion.

Path components such as `internal/tui/co` and `/Users/me/co` do not trigger command completion. An ambiguous prefix such as `read /u` may match a command. The final completion candidate restores the original text.

Completion includes skills, custom commands, daemon commands, and client commands. Duplicate names appear once. Examples: `/sm` → `/smart-agent`, `/perm` → `/permission-skip-all`.

When two things share a name, precedence is:

1. Built in commands such as `/init` and `/compact`
2. [Custom commands](#custom-commands)
3. Skills

A `/name` matching none of them is sent to the model as ordinary text.

> `/skill <name>` still works as the older spelling.

### `/init`

`/init` scans the repository with `Glob`, `Grep`, and `Read`, then creates or updates root `AGENTS.md`. The result covers build, lint, tests, architecture, and code conventions.

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
| `model_invocable` | Let the model run this command itself, not only you. Off unless set, and refused for a command containing a `` !`shell command` ``. Needs [`/model-invocable`](#model-invocable) on as well. Optional. |

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

Task permission requests appear in the parent session with the task identifier. Answering the request allows the task to continue.

Background tasks run in child sessions and do not occupy the parent's turn. A new parent prompt can run while tasks continue.

### `/memory`

Prints the current project's auto memory directory path and `MEMORY.md` index contents instantly, with no model call. See [Auto memory](#auto-memory).

### `/config`

Settings that can be toggled while running. They apply daemon wide rather than per session, so changing one from any session takes effect everywhere immediately.

| Command | Effect |
|---|---|
| `/config` | Shows current values, no model call |
| `/config auto_compact on\|off` | Automatic compaction; `/auto-compact` is the fuller command, with a threshold |
| `/config show_tps on\|off` | The tokens per second reading under the prompt |
| `/config auto_delegate on\|off` | Sending matching prompts to a cheaper sub agent, see [Auto delegation](#auto-delegation) |
| `/config smart_agent on\|off` | The built-in specialist roster and the orchestration prompt, see [Smart Agent](#smart-agent). Reports the roster it turned on, or says why it is empty. |
| `/orchestrate on\|off` | Enable or disable validated stage plans. Default: off. Requires delegation targets. See [Orchestration](#orchestration). |

Each change records a `config.changed` event on that session and the Web UI updates its status bar right away. A newly opened client reads current values from `GET /api/settings`.

### `/compact`

Compacts the conversation immediately instead of waiting for the automatic threshold. See [Context window management](#context-window-management).

| Command | Effect |
|---|---|
| `/compact` | Compacts with the default summarization prompt |
| `/compact <instructions>` | Adds your instructions for that one summarization, for example `/compact keep only file paths` |

An empty session records an error and remains unchanged. Success records a `compacted` event with `manual: true`. The confirmation reports the previous and current context message counts. Full transcript content remains visible and logged. Compaction token usage is included in `/usage`.

Example confirmation:

```text
Compacted. The model opens the next message with a summary of this conversation rather than the whole of it.
12 message(s) in its context replaced by 1.
Everything above stays in this conversation and in its log: scroll up, or reopen it later, and it is all still there. Summarizing is itself a model call, so the compaction's own tokens are in /usage.
```

For OpenAI-compatible providers, a blank line separates the summary and next user prompt when adjacent messages with the same role are merged.

### `/clear`

`/clear` removes the model's conversation history without deleting the transcript or session log. It does not call a model.

Use `/compact` to retain a summary. Use `/clear` to start the next request without earlier conversation context.

| Effect | `/compact` | `/clear` |
|---|---|---|
| What the model gets next | a summary of the conversation | nothing |
| What the transcript shows | everything, plus a marker | everything, plus a marker |
| Costs a model call | yes | no |

Cumulative `/usage` totals remain unchanged. The context gauge resets.

LocalCode keeps the same session and visible transcript after `/clear`.

### `/rewind`

`/rewind` removes the last exchange from model context and restores files changed through `write_file` or `edit` during that turn.

| Target | Result |
|---|---|
| The conversation | the model no longer has that exchange; it stays in the transcript and the log |
| Files | every path `write_file` or `edit` changed is put back as it was before the turn's first write; a file the turn created is removed |
| Again | run it again to go back another turn |

Coverage follows [Claude Code's checkpointing scope](https://code.claude.com/docs/en/checkpointing). The command reports excluded changes:

* Shell changes: files written, moved, deleted, or generated by `bash`
* Background-agent changes: checkpointed in the child session
* Later user edits: overwritten if the same path is restored; the response lists affected paths
* Symlinks: skipped
* Files over 8 MiB: recorded but not copied; reported as unchanged

Use version control for recovery beyond this tool-level checkpoint scope.

Rewind is refused in scheduled runs and `localcode run` pipes. It is also refused while a background child of the conversation is running.

Both `/clear` and `/rewind` are refused while the session has a turn in progress. They are not delivered to the model as text.

Built-in commands take precedence over custom commands and skills with the same name. Rename a conflicting `clear` or `rewind` command.

### `/model-invocable`

Whether the model may run this session's commands itself. **Off by default.**

```
/model-invocable on
/model-invocable off
```

When enabled, the model may request an opted-in built-in command, custom command, or skill. The command runs as a separate turn in the same conversation after the requesting turn ends.

Both the session switch and a per-command opt-in are required:

| Kind | How it opts in |
|---|---|
| Built-in (`/compact`, `/usage` …) | named in config.json's `model_commands`, with the slash |
| Custom command | `model_invocable: true` in its own frontmatter |
| Skill | `model_invocable: true` in its own frontmatter |

Wildcards are not supported. Sensitive commands such as `/permission-skip-all` require an explicit entry.

Names must include the leading slash, such as `/tidy-context`. A name without the slash is refused.

Commands containing `` !`shell command` `` cannot be model-invocable. The substitution executes during rendering, outside the `bash` permission check. Manual invocation remains available. Enabling `/model-invocable` lists refused commands and reasons.

> The model can select commands based on untrusted file contents, command output, or MCP results. Enable only commands whose automatic execution is acceptable. The switch and allowlist are not a prompt-injection guarantee.

A model-invoked command cannot invoke another command.

### `/usage`

Shows cumulative token counts per model for the current session, with no model call. **Token counts only, never dollar figures.**

`/usage` sums every API call since session creation, including repeatedly sent history. The status-bar context percentage describes the latest request instead.

With no calls yet, it just says so.

### `/context`

Shows what is actually in the request this session would send next, and what is not, with no model call.

Each prompt asset has a stable ID, source, trust class, request position, and inclusion condition. `/context` reports:

* Included assets, sizes, and inclusion reasons
* Totals by category
* External content classified as data
* Cache-order warnings
* Tool schemas by built-in group and MCP server
* Conversation size, reserved answer space, and context-window limit

`/context all` adds excluded assets and reasons. It lists individual conversation sources. The short form groups tool-result sources into a count after twelve entries.

Only identities, sizes, and reasons are printed. Asset bodies, project instructions, and hook-injected content are not printed.

Token counts are character-based estimates. Korean and Japanese may use substantially more tokens than the estimate.

The turn log records `prompt_manifest`, `prompt_assets`, and `prompt_untrusted`. Full records are stored in `~/.localcode/manifests/` with trace retention. `/context <id>` reads a past request's inclusion reasons, exclusions, hashes, warnings, and provider lowering. Bare `/context` describes the next request; `/context <id>` describes a recorded request.

The same manifest ID on a different model family indicates reuse of the same prompt assembly.

### The switches

The following daemon commands work in both clients.

**Daemon-wide**, saved to config.json:

| Command | Setting | What it does |
|---|---|---|
| `/smart-agent` | `smart_agent` | The specialist roster, the fallback chain, the trace, the prompt cache markers and the guards. See [Smart Agent](#smart-agent). |
| `/orchestrate` | `orchestrate` | The Orchestrate tool. Needs at least two agents to delegate to, so in practice Smart Agent as well. See [Orchestration](#orchestration). |
| `/auto-delegate` | `auto_delegate` | Sends matching prompts to a cheaper agent. See [Auto delegation](#auto-delegation). |
| `/keep-going` | `keep_going` | Automatic Muse-model continuation. See [A model that stops mid-task](#a-model-that-stops-mid-task). Also available in settings. |
| `/auto-compact` | `auto_compact_enabled`, `auto_compact_percent` | Auto-compaction. A number sets the threshold and turns it on: `/auto-compact 70` compacts past 70% of the context window. The default threshold is 50%. |

**Per conversation**, saved with the session. config.json holds their defaults. See [The four switches](#the-four-switches):

| Command | Switch | What it allows without asking |
|---|---|---|
| `/permission-skip-all` | `skip_all` | Everything, the workspace boundary included. |
| `/permission-skip-tools` | `skip_tools` | Every tool prompt, and not a path that leaves the workspace. |
| `/read-outside` | `read_outside` | Reading outside the workspace. `mem-clear` forgets approved directories. |
| `/write-outside` | `write_outside` | Writing outside the workspace. `mem-clear` forgets approved directories. |

Two more commands book work for later. See [Scheduled tasks](#scheduled-tasks):

| Command | What it does |
|---|---|
| `/schedule` | `/schedule <when> <what to do>` books a prompt; `/schedule cancel <id>` removes one. A repeating time works too, with `--times`, `--until` and `--keep` after the request. |
| `/show-scheduled-task` | Lists this conversation's booked tasks. |

Toggle commands accept no argument to invert the current value, or `on` and `off` to set it explicitly.

Dedicated daemon-wide commands save to config.json. Per-conversation commands save with the session. `/config` changes apply only to the running daemon. If persistence fails, the runtime change still applies and the response reports the failure.

Replies report inactive configurations, such as Smart Agent without profiles or auto-delegation without a configured target. `/permission-skip-all on` warns that shell commands and external paths no longer prompt. Explicit `deny` rules still apply.

`/config` still works and still lists all four settings including `auto_compact`.

Two more commands apply an edited configuration without restarting:

| Command | What it does |
|---|---|
| `/reset-mcp` | Stops the MCP servers, re-reads their configuration from config.json, reconnects, and swaps the tools. A server removed from the file takes its tools with it; one added while the daemon runs connects. The status indicator follows. |
| `/reset-skills` | Reloads skills from disk, against the live workspace. A skill installed or edited mid-run applies immediately, and the name completes like any other. |

### Other local commands

These commands are handled locally or by the daemon without a model call. Client-only commands do not enter the event log.

| Command | Effect |
|---|---|
| `/help` | Lists available commands instantly, no model call |
| `/version` | Shows the version of the **daemon** you are attached to, from `GET /api/version`. With `--server` against a remote daemon this is that daemon's version, which can differ from your local binary. |
| `/agent` | Lists registered agents; `/agent <name>` switches. See [Switching agents](#switching-agents-with-tab). |
| `/model` | **TUI.** Opens a list of agents to choose from, with the model each one resolves to, so you can switch without knowing the name first. `/model <name>` switches directly. The Web UI has the header dropdown instead. |
| `/session` | **TUI.** Opens a list of conversations to switch to. `/session <id>` switches directly. The Web UI has the left panel instead. See [Switching sessions](#switching-sessions). |
| `/skill` | Lists registered skills. See [Running a skill](#running-a-skill). |
| `/commands` | Lists the custom commands registered from `.localcode/commands/*.md`. See [Custom commands](#custom-commands). |
| `/tasks` | Lists background tasks in this session. See [`/tasks`](#tasks). |
| `/smart-agent` | Toggles the Smart Agent bundle and saves the choice. `/smart-agent on\|off` sets it outright. Answered by the daemon, so both clients have it. See [The switches](#the-switches). |
| `/auto-delegate` | Toggles auto-delegation the same way. |
| `/permission-skip-all` | Allows every prompt in this conversation, the workspace boundary included. |
| `/permission-skip-tools` | Allows every tool prompt in this conversation, and still asks before a path leaves the workspace. |
| `/read-outside` | Reading outside the workspace: `on`, `off`, or `mem-clear` to forget the directories approved at a prompt. See [Leaving the project](#leaving-the-project). |
| `/write-outside` | Writing outside the workspace, the same three arguments. |
| `/schedule` | Books a prompt for later: `/schedule <when> <what to do>`. Runs only while localcode is running. See [Scheduled tasks](#scheduled-tasks). |
| `/show-scheduled-task` | Lists the prompts booked for later in this conversation. |
| `/debate` | `/debate <reviewer>[,<reviewer>] [rounds] <task>`. Author and reviewer iterations. Also available through natural language or the ⚖️ button. See [Debate](#debate). |
| `/effort` | `/effort [off\|low\|medium\|high]`. Conversation reasoning level. `default` restores the profile setting. See [Effort](#effort). |
| `exit`, `:q` | Quits the TUI, same as Ctrl+C. The Web UI only prints a note, since a browser cannot quit the program. Close the tab yourself. |

## Part 5. Sessions

Sessions retain conversations across client and daemon restarts. Archive to hide a session without deleting it. Delete permanently removes the session and its descendants.

### Switching sessions

Sessions persist as append-only event logs. Reopening a client or restarting the daemon restores the conversation.

* **TUI**: at startup, if any session exists, the terminal lists them with session ID, agent, and creation time. Enter a number to resume, or `n` or an empty line to start fresh. From the same screen, `d<number>` such as `d1` deletes one session and reshows the list, and `da` deletes every session after you type `yes` to confirm.
* **TUI, once running**: `/session` opens the same choice without restarting. Arrow keys move, Enter switches, Esc cancels. Switching clears the screen and replays the chosen conversation from its own log; the prompt recall history goes with it, since it belonged to the conversation you left.
* **Web UI**: click a session in the left panel. The transcript and workspace switch together. See [Left panel: sessions](#left-panel-sessions).

`GET /api/sessions` returns the same list. Background tasks are `visible:false` and do not appear there. Use `GET /api/sessions/{id}/tasks` for those.

### Archiving a conversation

Archiving removes a conversation from the active list without deleting its contents. Retrieve it to resume work.

| Where | Archive | Retrieve |
|---|---|---|
| Web UI | The `archive` button on the card, or drag the card onto the Archive header at the bottom of the panel | Open the Archive section and press `retrieve` |
| TUI | `/archive` puts the conversation you are in away | `/retrieve` offers a picker of the archived ones; `/retrieve <id>` takes one directly |
| API | `POST /api/sessions/{id}/archive` | `POST /api/sessions/{id}/retrieve` |

The Archive count refreshes on initial load and after every archive or retrieval, including changes from other clients.

`GET /api/sessions?archived=1` returns archived sessions in the same row format as the active-session endpoint.

#### What archiving keeps

Archiving preserves the title, workspace, permissions, effort, list rank, and event log. `GET /api/sessions/{id}` and its event stream remain readable.

The log continues accepting completion and missed-schedule events. Archived sessions cannot start new work.

#### What it refuses, and with what

| Request | Answer |
|---|---|
| Send a message | 403 |
| Start a background task | 403 |
| Book a scheduled prompt | 403 |
| Switch its agent | 403 |
| Upload a file to it | 403 |
| Name it in a reorder | 400 |

Archived-session work requests return 403. Clients reserve 409 for a running turn and would otherwise queue a request that cannot run.

#### When archiving is refused

| Going on in it | What happens |
|---|---|
| A turn | 409. Answer or cancel it first, including a permission prompt it may be waiting on. |
| A background task | 409, naming the tasks. Wait for them or cancel them. |
| A scheduled run | 409, naming the entries. |

Archiving does not cancel active work. Finish or cancel it before archiving.

A scheduled run that starts between the active-work check and the archive flag may finish in the archived conversation. Its results remain in the log.

#### The other edges

| Detail | Behavior |
|---|---|
| Order | Retrieval restores the previous relative rank. Reordering while archived may change the exact position. |
| Future scheduled prompt | Marked `missed` if due while archived. Retrieval does not run it late. |
| Background task sessions | Cannot be archived independently. |
| Restart | Archived stays archived. A session file written before this feature existed has no flag and loads as active. |
| Memory | Archiving releases in-memory conversation history. Retrieval reloads the log. Uncollected task results remain available. |
| Delete all sessions | Removes archived conversations too. The confirmation says so. |

### Referring to another conversation with `#<name>`

Use `#<name>` to refer to another conversation on the same daemon. The model receives its identity and may read it with `session_read`.

```
#S2 has the final report. Check it against the file in this workspace.
```

Reference forms:

| Form | Matches |
|---|---|
| `#S2` | a session id, or a title with no spaces |
| `#"the parser rewrite"` | any title |

Resolution order: exact ID, exact title, then unique title prefix. Ambiguous matches are listed for the user to choose.

A reference adds the session identity, workspace, and `session_read` instructions, not its transcript. The model may read it as a tool result. Referenced transcript text is data: nested `#` references do not resolve and slash commands do not execute.

Reference limits:

* Unmatched, ambiguous, and self-references produce a notice without blocking the turn. Numeric issue references such as `#42` and Markdown `# ` headings are ignored.
* Archived conversations remain readable.
* Cross-project references identify both workspaces and warn that paths belong to the referenced project.
* Maximum five references per message. Additional references are counted in a notice.
* Background sessions cannot be referenced; use `/tasks`.
* Delegated prompts do not resolve references, and sub-agents do not receive `session_read`.
* Auto-delegation skips prompts containing session references.
* Right-arrow completion includes active and archived conversations and replaces only the word at the cursor.

### Renaming and deleting sessions

Sessions are identified and resumed by ID, so a `title` is purely for display.

| Action | API | Also available from |
|---|---|---|
| Rename | `POST /api/sessions/{id}/rename` with `{"title": "..."}` | The rename button on the session card in the Web UI left panel |
| Delete one | `DELETE /api/sessions/{id}` | The delete button per session in the Web UI, or `d<number>` in the TUI startup picker |
| Delete all | `DELETE /api/sessions` | The delete all button in the Web UI, or `da` in the TUI startup picker |

* Rename records `session.renamed`.
* Delete permanently removes JSONL and metadata files.
* Delete returns 409 when the target has an active turn.
* Deleting a parent stops all descendants, waits for them to finish stopping, and removes their logs and metadata.
* A stopped task returns a stopped outcome, not an empty answer.
* New delegation is refused once deletion begins. Delegation already admitted is included in deletion.
* Delete all returns 409 without deleting anything if any session has an active turn. Running background work is stopped first.
* While delete all runs, new turns, sessions, and forks return 409. Already admitted operations are awaited and included. Fork admission covers source-history reads as well as creation.

### Context window management

Provider token usage is recorded as a `usage` event at turn end. Bedrock, Anthropic, and OpenAI-compatible servers with `stream_options.include_usage` supply these values.

The event includes input and output tokens, context limit, percentage used, and tokens per second. The context limit uses [internal/modelinfo](../internal/modelinfo/modelinfo.go), with a 128000-token default for unknown models. Both clients use this event for their status bars.

Automatic compaction runs on the next message after context use exceeds the threshold. The default is 50%. `/auto-compact <percent>` changes it. When enabled, one summary replaces the model history before the new message is sent. The transcript retains the original history and records the compaction.

The context gauge does not include all reserved output space. The following controls handle oversized requests:

| Guard | What it does |
|---|---|
| `max_tokens` is clamped | Every request asks for only what is left of the window, so the reply cap cannot be what pushes a request over. |
| Tool output is capped | One tool result may take at most a quarter of the window. The start and the end are kept and the gap is described, so the model knows to read a file in ranges rather than believing it saw all of it. Without this, `read_file` on a large file or `bash` running `cat` could exceed the whole window inside a single message, which no summarizing or dropping can undo. |
| A refused turn is summarized and retried | Not the end of the turn. The transcript says it happened, and the turn carries on. |
| Still refused, it is trimmed | Whole messages go from the oldest end, and the remaining text is cut if dropping is not enough. Each attempt aims at two thirds of what the conversation *measures*, not of the window, so it converges on a size the server accepts. This matters most for Korean and Japanese, where the character-count estimate runs about 4x low and a request the server refuses can measure as comfortably fitting. |

`/compact` also reduces its own request size when needed.

Set `context_window` on the profile if the model is one whose name gives no clue to its real limit.

If summarization fails for another reason, such as a network error, LocalCode uses the original history and continues.

| Setting | Default | Change it |
|---|---|---|
| Automatic compaction | on | `"auto_compact_enabled": false` in config, or `/config auto_compact on\|off` while running |
| TPS display | on | `"show_tps": false` in config, or `/config show_tps on\|off` while running |

### Session logs

Session events append to `~/.localcode/sessions/<session-id>.jsonl`, useful for debugging and replay.

### Daemon restart and session recovery

Session metadata at `~/.localcode/sessions/<id>.meta.json` and the event log at `<id>.jsonl` both persist, so **restarting the daemon keeps** the session list, the conversation context, and `/usage` totals.

At startup, `session.LoadAllFromDisk` restores metadata and `agent.Loop.RehydrateAll()` rebuilds model history from event logs. Restoration includes tool calls, tool results, compaction summaries, and token usage.

If one session fails to restore, for example a corrupt `.meta.json`, the rest still restore and the daemon logs a warning.

## Part 6. Web UI

The Web UI shares daemon sessions with the TUI and desktop window. The left panel manages sessions. The right panel shows tasks and MCP servers. Status and permission controls are below the prompt.

### Resizing and hiding the side panels

| Action | How |
|---|---|
| Resize a panel | Drag the hairline between it and the transcript |
| Back to the default width | Double-click that same handle |
| Hide or show a panel | The toggle button in the header, at the far left for the session panel and the far right for the tasks/MCP panel |

Panel width and visibility persist in the browser or desktop window.

Minimum panel width: 160px. Use the toggle to hide a panel.

### Left panel: sessions

Every session on the daemon, newest first. Each card shows:

| Line | Contents |
|---|---|
| Title | Title or session ID. Indicator: grey for idle, blinking green for running, steady green for unread reply, steady amber for pending permission. Permission state takes precedence. |
| Workspace | Current directory, shortened from the front. Tooltip shows the full path. |
| Created | Local date and time |

Click a card to switch its transcript, agent, workspace, and status bar. Rename and delete act on the card without switching. **+ new** creates a session. Deletion controls ask for confirmation.

Drag cards to reorder sessions. The daemon saves order through `POST /api/sessions/order` and session metadata. All clients use the same order. New sessions appear at the top.

Cards show the session's current workspace. Sessions without a recorded workspace show `(workspace not recorded)`.

Switching sessions restores the selected session's workspace. See [Switching the workspace directory](#switching-the-workspace-directory).

The current agent appears in the header selector and [status bar](#status-bar-under-the-prompt), not on session cards.

### Right panel

| Section | Contents |
|---|---|
| Background tasks | Live status of subtasks started by the `Task` tool or the background task API |
| MCP servers | Names of currently connected servers, from `GET /api/mcp-servers`. Configured servers that failed to connect are absent here and logged as a daemon warning. |

### Drag and drop file attach

Dropping a file on the input box uploads it through `POST /api/sessions/{id}/uploads` to `~/.localcode/uploads/<session id>/<filename>`, and inserts the absolute path into the input box.

Uploads insert a path, not file contents. The model must read the path with `read_file` or `bash`. Text files are supported; image and binary content is not decoded.

### Status bar under the prompt

One line directly below the input box:

| Element | Behavior |
|---|---|
| Agent and model | Agent for the next message. Initial model ID comes from its profile; after a reply, the provider-reported ID is used. |
| Context use | Yellow past 70%, red past 90% |
| TPS | Generation rate across the full turn, excluding prefill and queue time. `~` marks a live stream estimate; the final count replaces it. Single-chunk replies show no rate. Controlled by `show_tps`. |
| Activity light | Grey: disconnected from daemon. Solid green: connected and idle. Blinking green: turn or background work active. Steady amber: permission required. Uses daemon state and reconnects automatically. Tooltip distinguishes turns from tasks. |
| Stop button | Cancels the active turn, including a turn started by another client. Equivalent to Esc. |
| Auto-delegate pill | Opens target-agent and pattern settings. See [Auto delegation](#auto-delegation). |
| Permission pill | Shows permission state and opens its controls. See [Permission settings](#viewing-and-changing-permission-settings-without-waiting-for-a-prompt). |
| Settings pill | `⚙ settings`. Opens the settings window, which holds the [Smart Agent](#smart-agent) switch and the update controls. See [Checking for updates](#checking-for-updates). |

### Switching agents with Tab

Tab selects the next agent and Shift+Tab the previous agent. Focus remains in the prompt box. Inside a modal, Tab moves between fields.

The header dropdown does the same thing and lists each agent with the model it resolves to, e.g. `explore (qwen3-1.7b)`; the agent's description is in the option's tooltip.

In the TUI, `/model` opens an agent list with model IDs. Use arrows to select, Enter to switch, and Esc to cancel.

### Model output renders as markdown

The built-in offline renderer supports headings, emphasis, code, lists, blockquotes, links, and horizontal rules. Unsupported Markdown and raw HTML are escaped as text. Model output cannot inject HTML.

### Watching a long turn

Each tool the model runs gets its own line in the transcript, written the moment the call starts and completed when it ends:

```
▸ bash  go test ./...                                    running…
✓ bash  go test ./...                                    14 lines
```

The marker pulses during execution and becomes `✓` or a red `✗` at completion. Click a tool line to expand or collapse its full arguments and result.

The status bar shows the current tool. Completed tool lines remain in the transcript.

The TUI writes the same one-line entries, without the expandable detail.

Submitted prompts appear immediately in a dimmed state. Daemon acknowledgement confirms the entry. Each message has one recorded transcript event.

User turns start with a `You` boundary row and a left border across the full prompt. The TUI uses a `You ────────` row and `▌` gutter. Neither client adds an inline `You:` prefix.

### Redirecting a turn while it runs

Messages sent during a turn reach the model at its next tool boundary. A running tool finishes before the message is delivered.

Pending messages show a delivery notice. Once the model receives the message, it becomes a confirmed user turn at that point in the transcript.

Several messages can stack up, and they arrive in the order you typed them. The model is told the text came from you mid-task, so it is not mistaken for tool output.

Limits:

* A new message does not interrupt a running tool. Stop or Esc cancels the turn and discards pending messages.
* Slash commands are refused during an active turn. They are not queued or delivered as model text.

If the turn happens to finish in the instant between your pressing Enter and the daemon accepting the message, it is answered as an ordinary next message instead. Nothing is dropped either way.

## Part 7. Agents and automation

Agents select models and tool scopes. Smart Agent adds built-in specialists. Orchestration runs explicit stage plans. Scheduled and repeating tasks require the daemon to remain running.

### Available tools

| Tool | Needs permission | Purpose |
|---|---|---|
| `read_file` | No | Read a file with line numbers |
| `glob` | No | Find files by pattern, `**` supported |
| `grep` | No | Search file contents by regex. Stops at 200 matches and says so; names any file it could not open or could not finish reading. See [Grep completeness reporting](#grep-completeness-reporting) |
| `write_file` | Yes | Create or overwrite a file |
| `edit` | Yes | Replace a specific string in a file |
| `bash` | Yes, except default Git allowance | Shell command with a two-minute default timeout. Reports exit status, empty output, timeout, and cancellation separately. `grep` 1, `diff` 1, and `test` 1 are ordinary results; `grep` 2 is an error. See [Python on Windows](#python-on-windows). |
| `Skill` | No | Load a skill body by name. Registered only when skills exist. |
| `check` | No | Run this project's own `verify_command` and report its output and exit status. Registered only when that key is set. One run at a time per directory, so a concurrent panel of reviewers does not start several copies of your test suite in one tree; a call that queued says so. |
| `session_read` | No | Read another conversation on this daemon: `mode=summary` for what it concluded and which files it touched, `mode=transcript` for its messages a page at a time. Not offered to a delegated sub agent. See [Referring to another conversation](#referring-to-another-conversation-with-name) |
| `mcp__<server>__<tool>` | Yes, always | Tools from each configured MCP server |
| `Task` | No | Delegate to another named agent and wait for its result. Offered only when there are 2 or more agents to delegate to, which [Smart Agent](#smart-agent) is one way to arrange. |
| `TaskBackground` | No | Start a sub agent and return its task id straight away. Offered only with [Smart Agent](#smart-agent) on. |
| `TaskCollect` | No | Wait for background sub agents and return what they found. Offered only with [Smart Agent](#smart-agent) on. |
| `Orchestrate` | Yes, always | Run a validated plan of delegated stages. Offered only with [`/orchestrate`](#orchestration) on and at least two agents to delegate to. |
| `Answer` | No | Report a stage's result in the shape its plan declared. Offered only inside an orchestration stage that declared one. |

The loop ends when the model stops requesting tools. `Verdict` and `Answer` also end the turn after a valid completion result. Three consecutive steps containing only previously repeated tool calls end the turn with a transcript notice. A new edit between repeated checks counts as progress.

Unambiguous tool-name variants such as `bash.command`, `functions.bash`, or `readFile` resolve only against tools available to that agent. The result reports the resolved name. An unresolved name returns the allowed tool list. Resolution cannot bypass tool restrictions.

### Combining agents

Agents map names to profiles. Add `description`, `prompt`, and `tools` to define specialized delegation roles.

The profile names below are the ones `config.example.json` ships, so this block can be pasted onto a copy of it. A profile an agent names has to exist, or the daemon refuses to start.

```json
"agents": {
  "build": {
    "profile": "smart-deep",
    "description": "Implements features and fixes bugs.",
    "prompt": "You are the build agent. Delegate research to the explore agent via the Task tool instead of doing it yourself."
  },
  "explore": {
    "profile": "smart-quick",
    "description": "Fast, read-only codebase search.",
    "prompt": "You are the explore agent. Locate relevant files and summarize quickly.",
    "tools": ["read_file", "glob", "grep"]
  }
}
```

An explicit agent definition replaces the built-in specialist with the same name, including its prompt, profile, and tools. Other names add agents.

| Field | Meaning |
|---|---|
| `profile` | Which provider and model. Required. |
| `description` | The one line another agent reads when picking a delegate |
| `prompt` | Appended after the base system prompt when running as this agent. Use it to narrow the role, such as "do not modify files" or "be fast and terse". |
| `tools` | The only tools this agent may use. Leave it out for everything, including `Task`. When set, the model sees only those tools, and a call to anything outside the list is refused before it runs. |

**The `Task` tool** registers automatically once `agents` has 2 or more entries. When the model calls `Task({"agent":"explore","prompt":"..."})`:

1. A new `explore` session is created, recording `task.spawned` on the parent. It does not queue: `max_concurrent_tasks` bounds background fan out, and a synchronous delegation cannot fan out.
2. One turn runs **synchronously** with `explore`'s profile, prompt, and tools. Unlike [background tasks](#background-tasks), the delegating agent's turn waits for this.
3. `explore`'s final answer text is returned as the tool result, and the delegating agent continues from it.

Delegation deeper than 3 levels is refused automatically, so agents cannot recurse into each other forever. The depth travels with the delegation, including into a background task, so no mixture of `Task` and `TaskBackground` gets a fresh allowance.

### Orchestration

Orchestration runs a validated multi-stage plan. It is disabled by default and has three equivalent controls:

| Control | Location or effect |
|---|---|
| `/orchestrate on\|off` | Answered by the daemon, so both clients have it. Saved to config.json. |
| Settings window | The Orchestration section, under Smart Agent. |
| `"orchestrate": true` | In config.json. |

At least two delegation targets are required. Enabling orchestration without them is allowed but has no effect. The UI and command response report this condition.

`Task` delegates one request at a time. `Orchestrate` declares stage order, fanout, result types, and limits before execution.

The model submits a plan through `Orchestrate`. LocalCode validates agent names, stage references, and counts before starting work. Invalid plans return a reason and run no stages.

#### What the model is told

An orchestration policy is added to top-level turns when the feature is enabled.

Use orchestration for independent review dimensions, per-item checks, or structured surveys. Use `Task` for one delegated question.

The policy varies by model family:

| Family | What is added |
|---|---|
| Default | The policy as above. |
| gpt, o3, o4 | Adds an explicit stopping rule. |
| gemini | Adds a concrete delegation threshold. |
| Local open weight models | Short policy focused on a single list fanout. |

Stage agents receive neither the orchestration policy nor the `Orchestrate` tool.

#### A plan

| Field | Meaning |
|---|---|
| `goal` | Run objective supplied to every stage agent. Stage agents do not receive the parent conversation. |
| `stages` | Ordered stages. Each completes before the next starts. |

A stage is one of three kinds:

| Kind | What it does |
|---|---|
| `step` | Its agent runs once. |
| `fanout` | Its agent runs once per item in `over`, times `copies`, all at once. |
| `barrier` | Its agent runs once, handed everything the stages before it kept. |

| Stage field | Meaning |
|---|---|
| `name` | Lowercase, no spaces. How a later stage refers to this one. |
| `agent` | Which delegatable agent runs it. Checked against the roster the turn was admitted with. |
| `role` | Tool role: `readonly` (default), `builder`, or `runner`. Intersected with agent restrictions. Plans cannot enumerate tools or widen an agent's permissions. |
| `prompt` | Stands on its own. Three substitutions and no others: `{{task}}`, `{{item}}` in a fanout, `{{input}}` for what earlier stages kept. |
| `over` | Fanout only. A list you write, or one entry of the form `$stage.field` naming an earlier stage and one of its `strings` fields. |
| `copies` | Fanout only. Independent agents per item. |
| `returns` | Field name to type (`string`, `bool`, `number`, `strings`). A stage that declares this gets an `Answer` tool in exactly that shape. |
| `keep` | One returned field. Results where it is false or empty are dropped. |
| `unanswered` | What to do when an agent does not answer in the declared shape: `skip` (default), `keep`, `fail`. |

`keep` filters results by one returned field. It supports no expression language. For example, `{"survives":"bool"}` with `keep: "survives"` retains true results.

```json
{
  "goal": "Find real problems in the change on this branch.",
  "stages": [
    { "name": "find", "kind": "fanout", "agent": "oracle", "role": "readonly",
      "over": ["correctness", "error handling", "concurrency"],
      "prompt": "Review the change for problems of one kind only: {{item}}.",
      "returns": { "findings": "strings" } },
    { "name": "kill", "kind": "fanout", "agent": "oracle", "copies": 2,
      "over": ["$find.findings"],
      "prompt": "Try to refute this finding: {{item}}. Default to refuted if unsure.",
      "returns": { "survives": "bool", "why": "string" },
      "keep": "survives", "unanswered": "skip" },
    { "name": "report", "kind": "barrier", "agent": "plan",
      "prompt": "Write up what survived:\n{{input}}" }
  ]
}
```

#### What it does and does not promise

| Constraint | Behavior |
|---|---|
| Ceilings | 8 stages, 16 items per fanout, 8 copies, 32 agent turns per run, 5 declared fields per stage, 4 agents at once. Every one is a refusal at validation, not a truncation while running. |
| Timeouts | 10 minutes per stage; 30 minutes per run. |
| Cancellation | Every stage is a synchronous child, so Esc stops the whole run including the stage in flight. |
| Permission | Confirmation before each run. Shows maximum limits because referenced fanout sizes are unknown until earlier stages complete. |
| Repeats | Fanout items from prior results are deduplicated after case and whitespace normalization. The report includes the duplicate count. |
| The report | Generated from execution records by LocalCode, not model summarization. |
| Nesting | Nested orchestration is refused. |

Unsupported: per-item pipelining between stages, `repeat_until`, resuming runs, and saved plans.

### Smart Agent

Smart Agent enables specialist delegation, enhanced tools, fallback, tracing, cache markers, and credential-path checks. It is disabled by default. Use the settings window, `/smart-agent on|off`, `/config smart_agent on|off`, or `"smart_agent": true`.

Each admitted turn retains its initial Smart Agent setting:

| Unit of work | Sees the change |
|---|---|
| A message not yet sent | Yes, immediately |
| A turn already running | No. Its agent roster, tool allowlist, the delegation roster its `Task` and `TaskBackground` schemas advertise, fallback chain, cache markers, credential guards and turn log all stay as they were when it started, even if it is in a long tool loop |
| A sub agent started by `Task` | No. It runs under the state its parent turn was admitted with |
| A background task launched with `TaskBackground` | No, including while it is waiting for a free slot. A specialist admitted with a read only tool list starts with that list whenever it eventually runs |

If the settings window cannot write config.json it says so beside the switch: the change is applied to the running daemon either way, and the warning is only about whether it will survive a restart.

Specialist delegation can add model calls and token usage. Enable it explicitly for workloads that benefit from separate contexts.

#### What it adds

| Added | Detail |
|---|---|
| Six specialist agents | `explore`, `librarian`, `oracle`, `plan`, `implement`, `verify`. They exist without being configured, and disappear again when the switch is turned off. See [What the roster needs from your config](#what-the-roster-needs-from-your-config). |
| An orchestration prompt | Appended to the system prompt of top level sessions only. It tells the model to work out what is being asked, send wide reading to a sub agent, do the narrow work itself, verify before reporting, and say what it checked. |
| `TaskBackground` and `TaskCollect` | Launch several specialists at once and pick up the answers together, instead of waiting for each in turn. |
| [File and search behavior](#file-and-search-behavior) | Paged reads, bounded search with explicit notices, and edit diagnostics. Grep completeness fixes apply in both modes. |
| [Fallback chains](#fallback-chains-when-a-model-will-not-answer) | A turn survives a rate limit or an outage by retrying the same endpoint with a bounded backoff, then moving to the next profile, re-deriving the prompt for the model it moved to. |
| [A turn log](#the-turn-log) | One JSON line per thing that happened, correlated across sub agents by a trace id. |
| [Cache breakpoints](#prompt-cache-breakpoints) | The tool schemas, the system prompt and the tail of the conversation are marked, so the provider can serve the unchanged part of every request from cache. |
| [File-access checks](#secrets-and-the-workspace-boundary) | Credential-path denial and resolved workspace-boundary checks. |
| [A trust boundary](#the-trust-boundary) | The system prompt states which sources are instructions and which are data, and MCP output arrives framed as data. |

#### What the roster needs from your config

Smart Agent creates six specialists from available profiles. Explicit specialist declarations are optional.

The roster uses three profile categories:

| Kind | Agents | Work |
|---|---|---|
| quick | `explore`, `verify` | Searching, running a build |
| balanced | `implement` | Making one self-contained change |
| deep | `plan`, `oracle`, `librarian` | Judgement: design, review, reading something long |

With one profile, all specialists use that profile. With no profiles, no roster is created and the enable response reports it.

Profiles named `smart-quick`, `smart-balanced`, and `smart-deep` override automatic category matching. Otherwise, LocalCode classifies model IDs.

`config.example.json` includes the six built-in prompts. A test in `internal/smart` checks that these definitions match the roster.

Declare the same agent name to replace its prompt, model, and tools. LocalCode does not merge the built-in definition into an explicit definition.

#### The specialists

| Agent | Runs on | Tools | For |
|---|---|---|---|
| `explore` | the quick profile | `read_file`, `glob`, `grep`, `Skill` | Finding where something lives. Paths and line numbers, not explanations. |
| `librarian` | the deep profile | `read_file`, `glob`, `grep`, `Skill` | Working through documentation, a long file, or an unfamiliar subsystem. |
| `oracle` | the deep profile | `read_file`, `glob`, `grep`, `Skill` | Reviewing a change or a design for what is actually wrong with it. |
| `plan` | the deep profile | `read_file`, `glob`, `grep`, `Skill` | Turning a large request into ordered, concrete steps. |
| `implement` | the balanced profile | `read_file`, `write_file`, `edit`, `bash`, `glob`, `grep`, `Skill` | One self contained change, carried out and checked. |
| `verify` | the quick profile | `read_file`, `bash`, `glob`, `grep` | Running the build or the tests and reporting what happened. |

Enforced restrictions:

* Specialists do not receive delegation tools.
* Delegation depth is limited to three levels, including background tasks.
* `explore`, `librarian`, `oracle`, and `plan` have no shell access.

Specialists are instructed to return fewer than 300 words, with file paths and line numbers instead of copied output. Their source-reading context remains separate from the parent context.

Explicit definitions under specialist names remain unchanged.

<a id="the-tools-get-sharper-too"></a>

#### File and search behavior

Smart Agent changes file-tool schemas and execution behavior together. Both use the setting captured at turn admission.

The table below applies only with Smart Agent enabled. [Grep completeness reporting](#grep-completeness-reporting) applies in both modes.

| Tool | With Smart Agent on |
|---|---|
| `read_file` | Supports `offset` and `limit`. Default: 800 lines. Footer reports range, total lines, and continuation offset. Binary files are identified rather than rendered. |
| `grep` | Skips binary files, version-control internals, and package caches. Maximum 200 matches, 30 per file. Matching lines are clipped at 400 bytes on a rune boundary. Limits and skipped content are reported. |
| `glob` | Same reported directory exclusions as grep. Maximum 500 paths. Directory components after `**` are supported, including `**/cmd/*.go`. |
| `edit` | Reports whitespace differences, exact line bytes, CRLF, candidate line numbers, and duplicate matches. Successful edits return numbered changed lines. |
| `write_file` | Says whether it created the file or replaced one, and how many lines the replaced one had. |

Tool limits:

* `edit` reports near matches but never applies them automatically.
* Search skips version-control internals and package caches. It does not exclude `vendor`, `build`, `dist`, or `target` by default.
* Every output limit is reported.

<a id="three-grep-answers-that-were-wrong-not-short"></a>

#### Grep completeness reporting

These grep behaviors apply with Smart Agent enabled or disabled.

| Condition | Behavior |
|---|---|
| Result limit | Reports `[stopped at the 200-result limit ...]` and suggests narrowing the search. |
| Long lines | Reads lines up to 1MB. Reports files whose scan could not finish. |
| Unreadable files | Reports affected paths, including permission failures and dangling symlinks. |

Notices name up to three affected paths, then report the remaining count.

Complete searches retain the ordinary `file:line:text` format.

#### Which model each specialist runs on

Specialists select a capability class from configured profiles, not a hard-coded model.

| Class | Wants | Matched from the model id by |
|---|---|---|
| `quick` | Low-latency lookup | `haiku`, `mini`, `flash`, `nano`, `lite`, `small`, `turbo`, and small parameter counts such as `-8b` |
| `balanced` | Implementation | `sonnet`, `coder`, `medium`, `gpt-4`, and mid parameter counts such as `-30b` |
| `deep` | Complex analysis | `opus`, `gpt-5`, `pro`, `ultra`, `thinking`, `-r1`, and large parameter counts such as `-70b` |

The lightest matching class wins. For example, `gpt-5-mini` is `quick`. Missing classes fall back to the nearest class, then `default_profile`. A single configured profile serves all specialists.

Use explicit profile names to override classification:

```json
"profiles": {
  "smart-quick": { "provider": "local", "model": "whatever-my-server-loaded" },
  "smart-deep":  { "provider": "bedrock", "model": "us.anthropic.claude-opus-4-6-v1" }
}
```

#### Running several at once

Use `TaskBackground` and `TaskCollect` for independent work:

1. `TaskBackground({"agent":"explore","prompt":"..."})` three times. Each returns a task id straight away and the orchestrator keeps working.
2. `TaskCollect({})` once. It waits for all of them and returns the answers in the order they were launched, so the model can match each answer to what it asked. `TaskCollect({"task_id":"..."})` takes just one and leaves the rest outstanding.

Stopping collection does not cancel tasks. Unfinished tasks remain available for later collection. `TaskCollect` reports how many remain running.

Maximum eight uncollected tasks per session. Further launches are refused until results are collected.

Background tasks appear in the Web UI's right panel while they run, the same as tasks started through the API.

#### Fallback chains: when a model will not answer

Fallback profiles allow a turn to continue after eligible provider failures. Smart Agent must be enabled.

Set `fallback` on the primary profile:

```json
"profiles": {
  "strong":  { "provider": "bedrock", "model": "us.anthropic.claude-opus-4-6-v1", "fallback": ["balanced", "local"] },
  "balanced":{ "provider": "bedrock", "model": "us.anthropic.claude-sonnet-4-5-20250929-v1:0" },
  "local":   { "provider": "local", "model": "qwen3-30b-a3b" }
}
```

| Detail | Behaviour |
|---|---|
| Before fallback | Transient failures retry the same endpoint twice, after 1s and 2s. Credential and model-identity failures skip retry. Cancellation during backoff records cancellation, not an unattempted retry. |
| Eligible failures | Exhausted rate-limit or quota retries, 5xx and gateway errors, refused or timed-out connections, unavailable model, or invalid credentials. |
| Excluded failures | Context overflow uses same-model compaction. Invalid parameters and schemas end the request. A partially emitted answer does not switch models. |
| Classification | Uses the reported cause, not only the exception type. For Bedrock `ValidationException`, missing models and access entitlement may trigger fallback; invalid fields do not. |
| Ineligible error | Ends the turn without contacting or consuming a fallback entry. |
| Order | Primary profile's flat list, in order. Fallback profiles' own lists are not followed. |
| Visibility | Transcript records each retry and model switch with the failure reason. |
| Validation | Profile names checked at config load. |

Fallback rebuilds the request for the selected model family, including orchestration policy and formatting notes.

#### The turn log

Smart Agent writes a structured record of what each turn did to `~/.localcode/trace/localcode-<date>.jsonl`, one JSON object per line.

Trace records supplement the transcript with provider selection, cache usage, tool duration, delegation, retries, and compaction.

| Field | Meaning |
|---|---|
| `trace_id` | Top-level turn ID inherited by all delegated work. |
| `span` | `turn.start`, `model`, `tool`, `delegate`, `retry`, `fallback`, `compact`, `turn.end` |
| `session_id`, `parent_session_id` | Which session, and whose child it is |
| `agent`, `profile`, `model`, `provider` | Who answered, on what |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` | Token usage and cache accounting. |
| `tool`, `duration_ms` | Which tool and how long it took, timed around the whole call so a wait on a permission prompt reads as a wait |
| `finish_reason`, `fallbacks`, `retries`, `compactions` | Completion reason and recovery counts on `turn.end`. |
| `prompt_manifest`, `prompt_assets`, `prompt_untrusted` | Which prompt assembly this call was built from, which assets it selected, and which of them carried external content. Identities only, never the bodies. See [`/context`](#context) |

```bash
jq -c 'select(.trace_id=="a1b2c3d4e5f6a7b8")' ~/.localcode/trace/localcode-2026-08-25.jsonl
```

`GET /api/trace` returns recent in-memory records. Filters: `?limit=`, `?session=`, and `?trace=`. Smart Agent must be enabled. Files are created on the first record.

Trace retention defaults to 30 days. Cleanup runs after configuration loads and at daily rotation. `trace_max_age_days` changes the age limit; zero or negative values use the default. `trace_max_total_mb` additionally removes oldest files to meet a size limit. Today's active file is never removed.

The same limits apply separately to the manifest directory. A trace may outlive its manifest; `/context <id>` reports missing manifests. Each immutable assembly is written once per day, regardless of reuse.

#### Prompt cache breakpoints

Smart Agent marks stable request prefixes for provider prompt caching.

| Backend | What is marked |
|---|---|
| Anthropic API | `cache_control` on the last tool, on the system prompt, and on the last block of each of the last two messages |
| Bedrock | A `cachePoint` after the tools, after the system prompt, and after each of the last two messages |
| openai-compatible | Nothing. Local servers do their own prefix caching with nothing to declare |

Conversation markers move as history grows. Two message markers preserve cache lookup coverage after long tool rounds. Tool, system, and message markers use four cache points in total.

Providers ignore unsupported or undersized cache prefixes. Minimum sizes depend on the provider and model.

Each specialist has its own session prefix and cache.

#### Secrets and the workspace boundary

The credential-path check requires Smart Agent. Explicit tool rules can override it. The workspace boundary is always enabled and requires separate outside-access approval.

Smart Agent denies `read_file`, `write_file`, and `edit` for credential-like paths: `.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa` and related keys, `~/.ssh`, `~/.aws/credentials`, `~/.kube/config`, `~/.npmrc`, `*credentials.json`, and `.netrc`.

`skip_permissions` does not override a denial. To permit a credential path, add an explicit rule:

```json
"permission": { "read_file": [{ "match": "*.env", "decision": "allow" }] }
```

The credential-path check does not parse shell commands. `bash` remains subject to its own permission rules.

### Leaving the project

File-tool access outside the session workspace requires a separate permission decision. An ordinary tool `allow` rule does not bypass this boundary. Explicit `deny` rules still apply.

**Which tools it covers**, and what counts as reading or writing:

| Class | Tools | Switch |
|---|---|---|
| Reading | `read_file`, `grep`, `glob` | `read_outside` |
| Writing | `write_file`, `edit` | `write_outside` |
| Not covered | `bash` | None |

The workspace boundary does not inspect shell semantics such as `cd /etc && cat passwd`. Shell commands use `bash` permission rules.

Workspace-boundary prompts can approve one call, one directory, or all outside access of that read/write class:

| Answer | TUI key | Effect |
|---|---|---|
| Allow once | `y` | This call only. |
| Deny | `n` | Refuses this call. |
| Allow this directory | `d` | That directory and everything under it, for the rest of this session. Nothing is written to disk. |
| Allow anywhere outside | `s` | Turns this conversation's `read_outside` or `write_outside` on, so it shows in the Permissions panel and can be turned off there. |

The prompt says which project the path is outside of, and names the directory `d` would cover, before you answer.

`/read-outside mem-clear` and `/write-outside mem-clear` remove directory approvals without changing switches. The Permissions panel provides a **forget** button for each directory. Background tasks inherit parent directory approvals.

`permission-skip-tools` does not silence this question. Only `permission-skip-all` does.

The boundary compares resolved physical paths, including symlink targets. Unresolvable paths require confirmation. New files use the closest existing ancestor. Dangling symlinks are evaluated at their target path.

#### The trust boundary

Smart Agent labels instruction sources and external data:

* The orchestrator and specialists treat user instructions, system prompts, and project rules as instructions.
* Tool results, files, command output, fetched pages, and MCP output are data, not instructions.
* MCP results include the server name and an untrusted-data marker. Error results receive the same marker.

Labels do not prevent prompt injection. Permissions remain the enforcement mechanism. MCP tool calls still require confirmation regardless of tool descriptions.

Delegated task text is not parsed as a slash command and is not rerouted through auto-delegation. A user may still open the child conversation and run a command there.

At first connection, LocalCode fingerprints MCP tool names, descriptions, and schemas in `~/.localcode/mcp-pins.json`. Later changes produce a startup warning naming the server. Changed tools remain available. Pinning operates with Smart Agent enabled or disabled.

#### The prompt is written for the model

Orchestration policy wording varies by model family:

| Model family | Difference |
|---|---|
| Default | The full policy, for models that follow one stated once |
| GPT and o series | Adds a stopping rule: delegate a question once; do not delegate the agent's own reasoning. |
| Gemini | Adds a concrete delegation threshold. |
| Local open weight models (qwen, glm, kimi, llama, mistral, gemma, deepseek and similar) | Short policy with one rule per line. Background delegation omitted. |

#### What it does not do

* Nothing is added to a session that is already a sub agent, so a specialist is never told to orchestrate.
* Nothing is added when there is nowhere to delegate to, which is the case when config.json has no profiles at all.
* It does not change permissions. Every tool a specialist calls goes through the same allow/ask/deny rules and the same prompt as any other tool call, asked in the session that spawned it.
* The specialists do not appear in the agent picker. They are delegation targets rather than conversation modes, so `Tab`, `/agent` and the header dropdown still cycle only the agents in config.json.

### Plan mode

Plan mode restricts an agent to inspection tools while preserving the conversation for a later implementation turn.

The `plan` agent in `config.example.json` allows only `tools: ["read_file","glob","grep","Skill"]`, so `write_file`, `edit`, and `bash` are never exposed or executed.

Tool restrictions are enforced both when schemas are exposed and before execution. Related external reports: [shell access in plan mode](https://github.com/anomalyco/opencode/issues/20938), [sub-agent restriction bypass](https://github.com/anomalyco/opencode/issues/26514).

**Switching keeps the session's conversation context and changes only which agent answers next.** It does not start a new session.

| Client | How to switch |
|---|---|
| TUI | **Tab** cycles through configured agents. The status line under the input box always shows `agent: <name>  ·  model: <model id>`. |
| Web UI | The header dropdown |
| Both | `/agent` to list, `/agent <name>` to switch |

Switching posts to `POST /api/sessions/{id}/agent`. On success an `agent.switched` event goes to the session, so every attached client, including a TUI and Web UI open at once, updates together.

Use `plan` for analysis, then switch to a writable agent. `config.example.json` defines `implement` for this purpose. Only names in the configured `agents` map are selectable; unknown names are refused.

### Auto delegation

Small, mechanical prompts can be answered by a cheaper agent automatically, without you switching models and without the main model running at all.

Auto-delegation is inactive without an `auto_delegate` configuration.

Auto-delegation preserves the main session's model and prompt cache. Switching the main model invalidates its cached prefix.

The delegated agent runs in its own session and history.

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
| The checkbox | Enables or disables delegation. Equivalent to `/config auto_delegate on\|off`. |
| **Answer them with** | Target agent, shown with its model ID, such as `explore (qwen3-1.7b)` |
| **Patterns** | Add or remove match globs one at a time |

Panel changes apply on the next prompt and persist to config.json. Each control changes only its own field.

You can configure the whole thing from here with no `auto_delegate` block in config.json to begin with; one is created as needed. Until an agent *and* at least one pattern are set, the panel says so explicitly rather than showing an "on" that quietly delegates nothing, and the pill reads `auto-delegate: on (unconfigured)`.

Unknown agent names are rejected before any setting changes.

#### Choosing what to delegate

Delegate only tasks that the selected agent can answer reliably.

Broader match patterns route more work to the delegated model. Review the quality of those answers before expanding the patterns.

| Good candidates | Keep on the main model |
|---|---|
| Finding a file or a symbol | Writing or changing code |
| Searching, listing, counting | Design and refactoring |
| "Where is this function?" | "Why is this failing?" (debugging) |

Read-only lookup tasks are suitable initial candidates. Keep design, debugging, and edits on the main model unless the delegated agent is configured and validated for them.

Practical loop:

1. Start with two or three narrow patterns.
2. Watch for `[delegated to <agent>]` in the transcript and read those answers.
3. Widen the list if they hold up, drop a pattern if they do not.
4. `/config auto_delegate off` turns it off mid-conversation with no restart, and `/config` shows the current state and target agent.

Savings depend on the proportion of matching prompts. Workloads dominated by implementation may benefit little.

### Debate

Debate alternates author work with independent reviews. The current conversation's agent is the author. Configured reviewer agents inspect the result and request revisions.

Start a debate with a natural-language request:

```
1부터 10까지 더하는 파이선 프로그램을 만들어라. 완료되면 @girl이 검토하고, 그 결과를 반영해라. 10번 반복해라.
write a retry wrapper for the upload call, then have the review agent check it and fix what it finds, 5 rounds
```

The model extracts reviewers, round count, and task. LocalCode runs the loop. Confirmation shows the parsed task, reviewers, rounds, and maximum model turns before execution.

The ⚖️ button under the prompt provides reviewer, round-count, and task fields. It previews the command before submission.

Command syntax: `/debate <reviewer>[,<reviewer>] [rounds] <what to do>`.

```
/debate girl 10 1부터 10까지 더하는 파이선 프로그램을 만들어라
/debate review write a retry wrapper around the upload call
/debate girl,tom 5 rewrite the parser
```

Reviewers may be any configured agents except the current author. Default: 3 rounds. Maximum: 10 rounds. The optional count must be a digits-only token. For example, `/debate girl 10페이지 문서를 써라` uses three rounds because `10페이지` is task text.

Reviewer selection, round count, and stopping rules are tool or command arguments. Only the task is sent as the author's work request.

One round includes one author turn and one turn per reviewer, plus their tool calls. Ten rounds with two reviewers require up to thirty model turns.

During debate rounds, the author keeps the current conversation history and cache. Each reviewer keeps a separate session across rounds. Reviewer sessions appear in the right panel and include their tool calls.

Reviewers run concurrently and do not see one another's findings. All reviewers must approve to end the debate as approved.

Reviewer permissions:

| Capability | Tools |
|---|---|
| Reading | `read_file`, `glob`, `grep` |
| Checking | `check`: exact `verify_command` from config.json, without arguments |
| Reporting | `Verdict` |
| Never | `write_file`, `edit`, `bash` |

Reviewers cannot use `bash`. The `check` tool runs only the exact project-defined `verify_command`:

```json
"verify_command": "go test ./... && go vet ./..."
```

The model decides whether to run `check`, not its command or arguments. The tool is unavailable when `verify_command` is unset. An explicit agent tool list must also include `check`.

Reviewers receive the task, author summary, change report, and workspace access. They do not receive the author's full conversation history.

In Git repositories, the change report includes `git diff HEAD` and untracked filenames. Elsewhere, it lists paths observed through `write_file` and `edit` and labels that limited scope. Shell-generated changes are absent from the non-Git list.

Debate outcomes:

| Reason | What happened |
|---|---|
| `approved` | Every reviewer approved. |
| `rounds` | The budget ran out with no approval. The work stands; read it before trusting it. |
| `stalled` | Two consecutive rounds without an author tool call. |
| `stopped` | You pressed Stop. What was done is kept. |

At debate completion, model context replaces debate instructions, reviews, and intermediate answers with the task and final work state. The closing message reports this change. All rounds remain visible in the conversation and event log. Expired debate instructions do not apply to the next message.

A valid recorded verdict ends the reviewer turn. A verdict lacking required findings requests another call instead.

Approval normally uses the `Verdict` tool's boolean result. A model that cannot call tools may finish with an exact `APPROVED` line. Silence, malformed calls, and embedded uses of that word do not approve. The tool is available only in review turns.

Reviewer agreement does not prove correctness. Use the configured checks and inspect the resulting changes.

Debate is available only in interactive top-level conversations. It is unavailable to sub-agents, scheduled runs, `localcode run` pipes, and nested debates.

### Effort

Effort sets the requested reasoning level for the conversation:

```
/effort high
/effort off
/effort            # what is in force, and what it reaches on this model
/effort default    # back to what the profile says
```

Or on the profile, for every conversation that uses it:

```json
"balanced": { "provider": "anthropic", "model": "claude-sonnet-5", "effort": "medium" }
```

A conversation effort setting overrides its profile. `default` removes the override.

Unset and `off` send no reasoning fields. `/effort` reports the active setting and its provider-specific effect.

Provider mappings differ:

| Provider | What is sent | What the levels mean |
|---|---|---|
| OpenAI-compatible | `reasoning_effort` | Sends `low`, `medium`, or `high`. Effect depends on server support. |
| Anthropic API, newest Claude families | Extended thinking, `adaptive` | All enabled effort levels select adaptive thinking. No distinct low/medium/high budget. |
| Anthropic API, older Claude models | extended thinking, with a budget | Three levels, three token budgets. |
| Bedrock | Extended thinking in `additionalModelRequestFields` | Model-dependent adaptive or budgeted form. Merged with the million-token beta field when enabled. |

Automatic compatibility adjustments:

* Reasoning budgets are reduced to fit `max_tokens`, reserving 1024 tokens for the answer. A cap too small for useful reasoning disables the budget. The high budget is 16384 tokens before adjustment.
* Temperature is omitted when the provider's reasoning mode requires a fixed temperature.

Reasoning appears as a separate muted block in the Web UI. The TUI status reads `thinking`. Reasoning-stream text is not written to session logs or replayed after reload.

### Scheduled tasks

Scheduled tasks run a prompt at a parsed future time. LocalCode must be running at that time.

```
내일 아침에 테스트 돌려줘
in 30 minutes check whether the build is still green
금요일 저녁에 배포 준비해줘
```

For natural-language scheduling, the model separates time and task. LocalCode parses the time and shows the resolved timestamp in confirmation.

Scheduling requires permission. `/permission-skip-tools` suppresses this confirmation.

Command syntax: `/schedule <when> <what to do>`.

```
/schedule 30분 뒤 run the tests and report the failures
/schedule 내일 아침 summarize yesterday's commits
/schedule in 2 hours check whether the build is still green
/schedule 2026-09-01 14:30 draft the release notes
```

LocalCode does not install a service or wake the machine. If it is closed or the machine is asleep at the scheduled time, the occurrence is marked `missed` and does not run late. Future occurrences are restored at startup.

The ⏰ Schedule button provides **When** and **What to do** fields. The time field previews the parsed timestamp before scheduling.

**The time is read by localcode, not by the model.**

| Shape | Examples |
|---|---|
| Relative | `30분 뒤`, `3시간 후에`, `in 2 hours`, `in half an hour`, `한 시간 뒤` |
| Clock | `오후 3시`, `5시까지`, `at 3pm`, `18:00` |
| Named | `내일 아침`, `모레 점심`, `저녁 7시`, `tomorrow 9am`, `tonight`, `at noon` |
| Weekday | `금요일 저녁`, `다음주 월요일`, `next monday`, `friday evening` |
| Absolute | `2026-09-01 14:30`, `2026-09-01` |

Korean time particles are removed from the task. For example, `내일 아침에 테스트 돌려줘` schedules tomorrow at 09:00 with task `테스트 돌려줘`. Words such as `에러` are not treated as particles.

A bare hour selects its nearest future occurrence. At 04:30, `5시` means 05:00. A named day fixes the date: `내일 9시` means tomorrow at 09:00. Explicit forms such as `오전 9시` and `18:00` retain their stated time.

Unsupported time inputs return a specific reason:

| Input | Answer |
|---|---|
| `나중에`, `곧`, `later today` | No specific time. |
| `매달 1일`, `monthly` | Monthly recurrence unsupported. |
| `10초마다`, `every 5 seconds` | Interval shorter than the one-minute minimum. |
| Anything else it cannot read | Refused with the examples above. |

Review the full parsed time and task in the confirmation.

Each scheduled run uses a separate session with the booking conversation's workspace and four permission switches. Confirmation names the workspace.

Scheduled permission requests appear in the booking conversation and expire after five minutes. Expiry stops the run and names the blocked tool. Configure `/permission-skip-tools`, `/read-outside`, and `/write-outside` in advance if unattended access is required.

| Where | What you get |
|---|---|
| ⏰ Schedule button | Separate time and task fields with parsed-time preview. |
| Right panel | Task rows: blinking green while waiting, solid green for unread results, grey after reading. Click for results; double-click for booking details. Rename changes the label. Delete removes the booking and run transcript. |
| `/show-scheduled-task` | The same list as text, for the TUI. |
| `/schedule cancel <id>` | Removes one. Ids are short and belong to the conversation: `s1`, `s2`. |
| `/schedule rename <id> <name>` | Labels one; an empty name clears it. |

Task names are display labels only. The underlying prompt remains visible and names are not used for lookup.

Double-click a task row for read-only booking details: time, status, name, full prompt, agent, and error. Finished tasks provide **Open the run**. Rename and delete remain on the row. A single click on a finished task waits briefly to distinguish a double-click; an unrun task opens its booking immediately.

### Repeating tasks

Repeating schedules support forms such as `매일 9시`, `every 2 hours`, `1시간마다`, `매주 월요일 저녁`, and `hourly`. A stated time sets the first occurrence. Without a time, the first occurrence is one interval from now. Confirmation shows both the recurrence and first run.

Every repeating schedule has a stopping policy and a consecutive-failure policy.

Stopping conditions:

| Say | It runs |
|---|---|
| `--times 10`, or **Stop after** in the dialog | ten times, then it is done |
| `--until 2026-12-01`, or **or stop on** | until that moment passes |
| neither | until you delete it |

Three consecutive failed runs stop the schedule. The row turns steady amber and shows the failure count and last error. A successful run clears the consecutive-failure count.

Each occurrence has a separate session. Its opening event identifies the booking, run number, and scheduled time.

`--keep` controls retained run transcripts: `-1` keeps all, `0` keeps none, and a positive number keeps that many recent runs. Default: 10. The parent log retains run outcomes even when transcripts are deleted.

Missed occurrences are not replayed. A repeating schedule advances to the next future occurrence.

Monthly schedules and intervals shorter than one minute are unsupported. Use weekly rules or an explicit day interval where appropriate.

### Background tasks

Start another agent from a parent session and track its progress. The event types are the same as the `Task` tool, `task.spawned` and `task.status`, but this is **asynchronous**. The caller does not wait.

This is API only for now. Neither client has a "run in background" button, only the sidebar that shows status.

```bash
curl -X POST http://127.0.0.1:4096/api/sessions/<parent-id>/tasks \
  -d '{"agent":"explore","prompt":"find every TODO under src/"}'
```

`task.spawned` and `task.status` events, carrying running, completed, failed, or cancelled, flow into the parent session's stream and appear live in the Web UI sidebar and the TUI transcript. Background concurrency is capped by `max_concurrent_tasks`; a task cancelled while it is still queued reaches a terminal status like any other, and collecting it returns the cancellation rather than waiting.

#### Watching one

Click a task in the right panel to view its complete session:

| What | Shown as |
|---|---|
| Tool calls | A line when the call starts and the same line completed with its result, `✓` or `✗`, and the output; click it for the full arguments. |
| A permission it is blocked on | `⏸ waiting for permission`, naming the tool and what it wants to do. Answer it in the session that spawned the task, which is where the prompt appears. |
| Work it delegates itself | A line per sub-task it spawns and per status that comes back. |
| The end | Finished or cancelled marker. Incomplete tool calls are marked as unfinished. |
| Errors and compaction | A line each. |

Running tasks provide **Stop this task**. Finished tasks provide **Delete this task**, which removes the child session and its row. The parent log records deletion so the row stays removed after reload.

### Switching models

Switching agents changes the profile, provider, model, system prompt, and tool scope for the next message. Conversation history remains unchanged.

```json
"agents": {
  "general-purpose": { "profile": "smart-deep" },
  "on-the-laptop":   { "profile": "local-qwen" }
}
```

| Where | How |
|---|---|
| TUI | `Tab` cycles, `/agent <name>` switches, `/model` lists every agent with the model it resolves to |
| Web UI | The selector in the header, or `/agent <name>` |
| Either | The switch is appended to the session log, so it survives a restart |

Agents may use different providers. The selected model receives the preceding conversation, including earlier questions and answers.

Switching agents invalidates the previous agent's cached system prompt and tool-schema prefix.

`--agent <name>` still picks the agent a **new** session starts on.

### Python on Windows

After a failed Python command on Windows, LocalCode provides interpreter-specific diagnostics:

| What happened | What the model is told |
|---|---|
| The name resolves to a Microsoft Store app-execution-alias stub, which opens the Store instead of running Python | That every spelling hits the same stub, and the `winget` line that installs a real one. The install is handed back as a command rather than run, so it goes through the ordinary permission gate. |
| The name is not on PATH at all | That this is a common way for it to fail rather than a sign Python is missing, and where to look. |

Windows interpreter differences:

* conda, miniforge and miniconda ship **no `python3.exe` at all**, only `python.exe`.
* python.org's classic installers ship none either, and do not add themselves to PATH by default.
* Each tool call gets a fresh shell, so `conda activate` or a venv activation does not carry over. An interpreter has to be called by absolute path.

Diagnostic limits:

* A resolved PATH interpreter is reported as a machine fact, not a verified project interpreter.
* LocalCode suggests search or install commands but does not run them automatically.
* Diagnostics run after failure, not as a preflight block.
* Detection uses PATH lookup rather than localized error text.

Wrapped invocations such as `env python3 x.py` and `xargs python3` are not covered.

### Attaching a local LLM

1. Load a model in LM Studio and start its local server, by default `http://localhost:1234/v1`.
2. Point `providers.local.base_url` at that address.
3. Set the profile's `model` to exactly the model name LM Studio shows.
4. Point an `agents` entry at that profile and run with `--agent`.

See [MODELS.md](MODELS.md#local-llms-over-an-openai-compatible-endpoint) for more, including remote proxies that need an API key.

LocalCode reads local-provider `reasoning_content` and `reasoning` stream fields. The TUI shows `thinking`; the Web UI displays reasoning above the answer.

Separate reasoning-stream text is neither logged nor returned to the model. Reloading removes it.

Reasoning written into the answer itself, between `<think>` tags, is not separated out: it arrives as content, so it is shown and kept as content.

### Checking for updates

The settings window's **Updates** section checks or installs only when a button is clicked.

| Button | What it does |
| --- | --- |
| Check for updates | Compares the current build with the latest GitHub release or configured `update_url`. See [Custom update sources](#updating-from-somewhere-other-than-github). |
| Download and install | Downloads, verifies, and installs a supported update. Shown only for a newer version that this daemon can install. See [Platform behavior](#what-installing-does-per-platform). |

No update check runs on a timer or when the panel opens.

#### What installing does, per platform

Update behavior depends on the install format:

| Install shape | What happens | Comes back on its own |
|---|---|---|
| A binary somewhere you can write (`~/.local/bin`, and the same on macOS and Linux tarballs) | localcode writes the new binary over the running one | Yes, in the terminal. It re-executes itself, so the same terminal, the same process id and the same arguments come back. |
| Windows `.msi` | The installer runs at basic UI. Windows asks the Restart Manager which processes hold the files, its built-in dialog offers to close them, localcode is closed cleanly with the terminal restored, and the install completes | No. Start localcode again. |
| Windows `.zip`, macOS `.app`, Linux `.deb` | Downloaded, with what to do next | No. |

Windows MSI updates do not restart LocalCode in the original terminal. Start it again after installation.

The MSI uses basic UI so Windows Installer can offer its built-in files-in-use dialog. Full UI requires a package-authored dialog that this package does not contain.

#### Updating from somewhere other than GitHub

`update_url` in config.json replaces GitHub entirely: one **https** address at which the current installers are published, side by side, named the way localcode names them.

```json
{ "update_url": "https://bitbucket.org/acme/localcode-builds/downloads/" }
```

Use an internal source when GitHub is unavailable or when distributing an organization build.

Versions are parsed from standard asset names, including `localcode-1.2.3-darwin-universal.tar.gz`, `localcode-1.2.3-windows-amd64.msi`, and `localcode-1.2.3-linux-amd64.deb`. Supported source formats include directory indexes, Bitbucket download listings, artifact-server JSON, and direct file URLs. When multiple versions exist, the highest version is selected.

| Situation | What you get |
|---|---|
| The URL cannot be reached | `could not reach update_url <url>: ...`, naming the address |
| It answers 404, 403, ... | `update_url <url> answered 404 Not Found` |
| Nothing there looks like an installer | A message saying so, with an example filename |
| It is not https | Refused, with the reason |

`update_url` requires HTTPS. HTTP URLs are refused.

GitHub assets are checked against their published SHA-256. Other sources may provide a sibling `<filename>.sha256` in `sha256sum` format. Without a checksum, installation continues with a **could not be verified** warning.

The panel names the source when it is not the public releases page, so an internal build is never reported as though it came from GitHub.

Installation is offered for the desktop window and a daemon listening on loopback. Remote connections and non-loopback listeners support checks only and link to the release page.

**What is downloaded.** The file that matches how localcode was installed:

| Platform | Asset |
| --- | --- |
| Windows amd64 | The `.msi`, which upgrades in place |
| Windows arm64 | The `.zip` (no installer; the panel says where the file is) |
| macOS, from `LocalCode.app` | `LocalCode-x.y.z-darwin-universal-app.tar.gz` |
| macOS, command line | `localcode-x.y.z-darwin-universal.tar.gz` |
| Linux, installed from the `.deb` (`/usr/bin/localcode`) | `localcode-x.y.z-linux-<arch>.deb`, with the `apt install` line to run |
| Linux, installed under your home directory (`~/.local/bin`, or any tarball copy) | `localcode-x.y.z-linux-<arch>.tar.gz`, installed for you |

Downloads are rejected if their published checksum or expected size does not match. Files are stored in the user cache directory, including `%LOCALAPPDATA%\localcode\updates` on Windows.

Writable standalone binaries are replaced directly. Package-managed installs require the corresponding installer or package manager.

For a writable standalone binary, LocalCode unpacks the update beside the existing binary, checks its version by running it, and atomically renames it into place. This includes [the Linux user install](INSTALL.md#install-on-linux).

After replacement, local Unix execution restarts through `exec`. The process ID, terminal, standard streams, and arguments are retained. Browser clients reconnect when the daemon returns.

A remote daemon is not automatically restarted.

Package-managed and bundle installs:

* Windows MSI: run `msiexec /i`; Windows handles elevation and in-use files. LocalCode must close. The desktop window offers to close after the installer starts.
* Linux `.deb` or a root-owned `/usr/bin` copy: download and verify, then show `sudo apt install <path>`. LocalCode does not request a password or run the package manager.
* macOS `LocalCode.app` and Windows ZIP: download the complete distribution and show manual instructions.

The panel reports the downloaded path and required next action.

Development builds reporting `dev` are not offered updates.

## Known limitations

* Long sessions initially load recent events. Use **Load the whole conversation** for the full transcript.
* If an MCP server dies and reconnection fails, later tool calls fail until daemon restart.
* There is no auth token. Anyone who can reach the `--listen` address gets the entire API, shell execution included. Expose it only over loopback plus an SSH tunnel.
* On Windows, shell execution resolves to `sh` on PATH, then Git for Windows' `bash.exe` at its usual install paths, then `cmd /c`. Under the `cmd` fallback, bash-only syntax does not work; the bash tool tells the model so in its description. Installing Git for Windows gives the full POSIX behavior.
* There is no desktop window on Linux. It links a native webview through CGo, which on Linux means WebKitGTK and a build per distribution; the daemon, the TUI, and the Web UI in a browser all work there. The `.deb` and the Linux tarballs carry the `localcode` binary only.
* Manual compaction does not use ordinary turn serialization and can overlap an active turn on the same session.
