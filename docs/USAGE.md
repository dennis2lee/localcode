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
| [7. Agents and automation](#part-7-agents-and-automation) | [Available tools](#available-tools), [Combining agents](#combining-agents), [Smart Agent](#smart-agent), [Plan mode](#plan-mode), [Auto delegation](#auto-delegation), [Background tasks](#background-tasks), [Switching models](#switching-models), [Local LLMs](#attaching-a-local-llm) |
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
| `--listen <host:port>` | `127.0.0.1:4096` | Address the daemon binds. The Web UI is served here too. See [When the port is already taken](#when-the-port-is-already-taken). |
| `--server <url>` | none | Do not start a local daemon. Attach the TUI to an already running daemon, which may be remote. |
| `--headless` | `false` | Run the daemon alone with no TUI, exposing the HTTP API and Web UI |
| `--gui` | on for a `-tags gui` build, off otherwise | Open the native desktop window instead of the TUI. `--gui=false` forces the TUI on a build that has the window. |
| `-version`, `--version` | `false` | Print the build version and exit |

`localcode version` works the same as `-version`.

Three useful combinations:

| Command | What it does |
|---|---|
| `localcode` | Starts a local daemon and attaches the TUI. Open `http://127.0.0.1:4096` in a browser to use the Web UI on the same sessions at the same time. |
| `localcode --headless --listen 0.0.0.0:4096` | Daemon only. Meant for a remote server. |

#### When the port is already taken

Running `localcode` starts a daemon and attaches the TUI to it, so it needs the address in `--listen`. When something already has it, what happens depends on what that something is:

| What holds the address | What happens |
|---|---|
| A localcode daemon working in **this** directory | The TUI attaches to it and says so. The daemon is a shared core and this is one of its clients, so a second terminal on the same project is the normal case, not a conflict. You get that daemon's sessions in the picker. |
| A localcode daemon working in **another** directory | Not attached to. It starts its own daemon on a free port and says which project the other one is in. |
| Anything else | The daemon binds a free port on the same host instead and prints where the Web UI went. The terminal still works, which is the point. |
| Anything else, and you typed `--listen` yourself | An error naming the address, because you asked for that one and serving somewhere else would answer a different request. Stop what is using it, or pass a different `--listen`. |

The directory matters because a daemon stamps its own onto every session created on it. Attaching from another project would open a conversation that edits the first project's files while the terminal sits in the second, so sharing a daemon is a convenience and working where you are is the promise. Two spellings of one directory are one directory: a symlink and its target match, which is why `/tmp` and `/private/tmp` on macOS do not start two daemons.

The daemon is asked what it is before the TUI attaches to it: a `GET /api/version` that has to answer as localcode, then a `GET /api/workspace` for where it is working. A web server on 4096 that is not localcode gets out of the way like anything else rather than being handed a client, and a localcode that will not say where it works is treated as working somewhere else, since the cost of that is one extra daemon and the cost of guessing the other way is a session in the wrong project.

This does not apply to the desktop window, which has never had the problem: nobody types its address, so it binds whatever port the OS gives it.
| `localcode --server http://host:4096` | TUI only, attached to a daemon that is already running. |
| `localcode-gui` | A native desktop window instead of the TUI. Experimental, built with `-tags gui`, opens by default on such a build (no `--gui` needed). See [Desktop window](#desktop-window-experimental). |

### Desktop window (experimental)

Instead of the TUI or a browser, localcode can open its Web UI in a native desktop window, so it is one app to launch rather than a server to start and a browser tab to open.

```bash
./localcode-gui
```

A `-tags gui` build defaults `--gui` to on, so no flag is needed — running the binary opens the window directly. Pass `--gui=false` to force the TUI on that same build instead.

It starts the daemon in-process on a private loopback port and shows the same Web UI in an OS native window (WKWebView on macOS, WebView2 on Windows). Nothing is exposed off the machine and there is no fixed port to collide with.

**Startup screen.** The window opens immediately, before the daemon exists, showing the app icon and a status line naming the step in progress: reading config, opening providers, loading sessions, connecting to each MCP server by name, restoring history. Starting up takes a few seconds with several MCP servers configured, and one that is slow or dead holds up everything behind it, so the line names the server being waited on rather than a generic "loading". Opening providers does not read AWS configuration. Bedrock opens the AWS config and credential chain only when the first request actually uses that provider, so a stale or unused Bedrock entry cannot stop a local-only daemon. The screen is replaced by the app as soon as it is ready.

If startup fails, the reason is shown on that screen and the window stays open. `localcode-gui.exe` has no console (see below), so this is the only place a startup error can be read.

**Not on Linux.** There is no desktop window there and none is planned: the webview links WebKitGTK through CGo, which is a build per distribution rather than a build flag. `--gui` on a Linux build says so and stops. What a Linux install has instead is the same interface in a browser tab: run `localcode` and open `http://127.0.0.1:4096` (or whatever `--listen` says). The Web UI and the window are the same page.

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

Two files in the repository are worth copying from:

| File | What it is |
|---|---|
| `config.example.json` | Every key this program reads, with a note on each. The reference. |
| `config.sample.json` | One worked setup for orchestration: providers, three profiles, the switch, and the six specialists with their prompts written out. Start here if what you want is the multi-agent behaviour. |

#### Comments: the file is JSONC

`//` to the end of a line, `/* */` across lines, and a trailing comma before a `}` or `]`. A config is a thing you write by hand, and a thing written by hand wants to say why.

**Comments survive the writes localcode makes.** Saving a permission rule, or typing `/smart-agent on`, rewrites config.json, and a rewrite that dropped your comments would eat the very thing they are for. So a file with comments in it is edited one key at a time, in place: the value changes and every other byte, including the comments and your own indentation and key order, stays exactly as it was.

One case is refused rather than guessed at: adding a key that is not already in a commented file. There is no span to replace, and choosing a position would mean deciding which side of one of your comments it belongs on. localcode says so, the change still applies for the run, and you can add the key by hand.

A `//` inside a string is not a comment, so a `base_url` of `https://example.com/v1` means what it says.

#### Values from the environment: `{env:NAME}`

Any string value in config.json may be `{env:NAME}`, and is replaced by that environment variable when the file is read. It is the same spelling opencode uses, so a config written for that works here.

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

* **Every string, not just keys.** A base_url, a model id, an MCP server's own environment, an `Authorization` header: anything that differs between machines can come from the environment instead of being edited per machine.
* **A placeholder may be part of a value**, and a value may contain several: `"Bearer {env:TOKEN}"`, `"https://{env:HOST}/mcp"`.
* **A missing variable is an error that names it**, along with the field that asked for it and the file it is in. An empty `api_key` would otherwise fail much later as a 401 that says nothing about config.json.
* **What is on disk stays a placeholder.** localcode rewrites config.json one key at a time from the file itself, so saving a setting from the UI ("always allow") or running `localcode mcp add` never turns `{env:ANTHROPIC_API_KEY}` into the key it stands for.
* Only a real variable name counts, so ordinary text with a brace in it (`{envelope}`, `{env: something}`) is left alone.

The point is a config.json that can be committed to a repository, copied between machines, or pasted into an issue without carrying a secret in it. For Anthropic and Bedrock specifically there is also [`localcode login`](#authenticating-with-login), which stores the credential outside config.json entirely.

#### Top level fields

| Field | Meaning |
|---|---|
| `providers` | Model backend connection details. `type` is `bedrock`, `anthropic`, or `openai-compat`. Bedrock AWS configuration is loaded lazily on its first request. |
| `profiles` | A named provider and model pairing. `max_tokens`, `temperature`, `context_window` and `keep_going` are optional. |
| `agents` | Maps an agent name to a profile. `--agent` resolves through this. An unknown name falls back to `default_profile`. |
| `max_concurrent_tasks` | Caps how many **background** tasks run at once. Unset means 1, so background tasks queue rather than run together. Synchronous `Task` delegation is not counted against it, because a caller that is blocked cannot start a second one and holding a slot while waiting for a nested child would deadlock against itself. |
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
| `max_tokens` | Cap on one reply, 4096 if unset. Unlike `context_window` this cannot be discovered: it is a choice about how long an answer you want, not a fact about the server. Reduced automatically when the conversation leaves less room than this in the window, so a generous ceiling is safe, and a reply that runs into it says so rather than just stopping. |
| `temperature` | Sampling temperature |
| `keep_going` | How many times one turn may be told to carry on after the model stops with the task unfinished. Only ever applies to muse models; on them `0` (default) means `3` out of the box and `-1` forces it off. See [below](#a-model-that-stops-mid-task). |
| `fallback` | Other profile names to try, in order, when a request to this one fails for a reason another model could survive. Read only with [Smart Agent](#smart-agent) on. See [Fallback chains](#fallback-chains-when-a-model-will-not-answer). |
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
N times per turn.

**It exists for muse models and reaches nothing else.** Any model whose
id contains `muse` (case does not matter, so `Muse-Glimmer-30B` and
`my-muse-variant` both count) gets a budget of 3 out of the box —
installing the release is the whole fix, no config key required. On any
other model the feature does not exist, whatever is configured: the
habit it compensates for is this family's, and a nudge sent to a model
without it is localcode second-guessing a finished answer. This used to
be keyed on `glimmer`, which was a live bug — a muse variant without
that word in its id got a budget of zero, and the feature built for
these models was off for most of them.

Two controls:

* **`/keep-going`** toggles it for the whole daemon (the GUI settings
  window has the same switch as a checkbox — one switch, two homes, kept
  in step). The choice is saved to config.json.
* A muse profile's own `keep_going` number adjusts the budget, and `-1`
  turns it off for that profile alone:

```json
{
  "profiles": {
    "eager": { "provider": "local", "model": "muse-glimmer-30b", "keep_going": 5 },
    "quiet": { "provider": "local", "model": "muse-small-7b", "keep_going": -1 }
  }
}
```

The rules it carries on under, each of which is a case where stopping
was right:

| Not carried on when | Because |
|---|---|
| No tool ran in this turn | It was a question and its answer, not a task |
| The last tool call was refused | The model stopped because someone said no |
| The reply ends in a question | The model is asking, not stalling |
| The reply hit `max_tokens` | It needs a bigger cap, not another turn — and that is already reported |
| The last carry-on produced no *new* work | The model is going over what it has already done, which is it saying it has finished |
| You have already typed something | Your message reaches the model as soon as this turn ends, and it beats an invented one |

That last rule is what keeps the setting cheap: a finished task costs one
extra turn, not `keep_going` of them. Each carry-on appears in the
transcript as a note saying what happened, so a turn that continues by
itself never looks like one that never stopped.

**"New" is doing real work in that rule**, and it was the fault reported
in v0.52.0: with muse, a task that was already finished got run again and
again. The rule used to count *any* tool call as work. But a model told it
has not finished does not argue — it goes and finds something to do,
re-reading the file it just wrote or re-running the build it just ran, and
every one of those bought another carry-on until the budget ran out. A
call now counts only if this turn has not already made it, arguments and
all: re-running a build after fixing the file that broke it is work,
because the fix is a call of its own; re-running it to admire the result
is not. The prompt was the other half — it used to open by telling the
model it had not finished, which localcode has no way of knowing. It now
asks, and names "it is already complete" as an answer.

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

#### The four switches

Four switches decide which prompts you see. **Each belongs to the conversation, not to the daemon**: two conversations on one daemon are two projects, and "do not ask me about this one" is a sentence about a project. Flipping one in a scratch experiment used to flip it in the window editing something that mattered.

| Switch | Command | What it allows without asking |
|---|---|---|
| `skip_all` | `/permission-skip-all` | Everything, the workspace boundary included |
| `skip_tools` | `/permission-skip-tools` | Every tool prompt, and **not** a path that leaves the workspace |
| `read_outside` | `/read-outside` | Reading outside the workspace (`read_file`, `grep`, `glob`) |
| `write_outside` | `/write-outside` | Writing outside the workspace (`write_file`, `edit`) |

`skip_tools` is the one most people want: work head-down in one repository without being interrupted, and still be asked before anything reaches another project. Before it existed the only way to stop the interruptions was to turn off the guard that matters most.

config.json holds the **defaults** for a conversation that has not answered for itself:

```json
{
  "skip_permissions": false,
  "skip_tool_permissions": false,
  "read_outside_workspace": false,
  "write_outside_workspace": false
}
```

The Permissions panel and the four commands set the conversation you are in and save the answer with the session, so reopening it reopens it configured the way you left it. A background task follows the conversation that started it, live: turning a switch off reaches work already running.

With `skip_all` on, the model writes files and runs shell commands with no confirmation at all, anywhere on the machine. Turn it on only where that is acceptable: a scratch repository, a container, a machine whose state you do not mind losing.

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

An ordinary permission prompt offers four answers. A prompt raised by the workspace boundary offers a different set, because it is a different question: see [Leaving the project](#leaving-the-project).

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

A pill under the prompt box (Web UI and the GUI window, same page) always shows the open conversation's permission state: `permissions: ask (N rules)`, `permissions: tools skipped`, or `permissions: skip` in warn color. Click it to open a panel that:

* shows the four switches for the conversation you are in, and says where each answer came from: this conversation, the one that started it, or config.json
* lists the directories this conversation has approved leaving the project for, each with a **forget** button (the same thing `/read-outside mem-clear` does)
* lists every rule currently in `permission`, with a remove button per rule
* adds a new rule by tool name, match pattern, and decision (allow/ask/deny)

Every change applies immediately (the running daemon's decisions reflect it on the very next tool call) and is written to config.json the same way "always allow" is — the daemon needs a config.json path to persist to (see `--config` above); if it doesn't have one, the panel still shows the current state but the controls are disabled with a note explaining why.

### Switching the workspace directory

The workspace is the directory every relative file path and bash command resolves against. A session starts in whichever directory the daemon was given at startup, and from then on the workspace belongs to the session. It is shown at the top of the GUI window and in the Web UI's header, and can be changed without restarting by clicking it.

What that click does depends on where you are:

| Where | Clicking the workspace |
|---|---|
| GUI window | Opens the operating system's own folder picker (macOS `choose folder`, Windows' folder browser, zenity/kdialog on Linux), starting in the current workspace. Choosing a folder applies it immediately; dismissing the dialog changes nothing. |
| Browser | Opens a box to type an absolute path into. The web platform gives no way to get a real filesystem path out of a file dialog — neither `<input webkitdirectory>` nor `showDirectoryPicker()` exposes one — and a daemon you reached over the network would open its dialog on the *server*, so the picker is deliberately offered only in the desktop window. |

The folder icon beside the path opens that directory in a file-manager window — Explorer on Windows, Finder (brought to the front) on macOS, whatever `xdg-open` is registered to elsewhere. Offered only in the desktop window, for the same reason as the picker: over the network the window would open on the daemon's machine.

**Each session has its own workspace.** Every relative file path and every bash command resolves against the directory of the session it belongs to, so two sessions can work in two different projects at the same time, on the same daemon, without disturbing each other. It is a property of the session, not of the localcode process, which is why reopening a conversation about another project puts you back in that project rather than wherever the daemon happens to have been started.

**Delegated work inherits it.** A sub agent started by the `Task` tool, by `TaskBackground`, or through the tasks API works in the directory of the session that launched it, and so does anything it delegates in turn. The directory is taken at the moment the task starts and stays with it: moving the parent afterwards moves the parent, and the task finishes the instructions it was given where it was given them. Custom commands (`@file`, `` !`cmd` ``) and hooks resolve against the same directory.

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
| `pre_model` | `session_id`, `agent`, `model`, `provider` | Yes, the request is never sent and the turn ends with an error |
| `post_model` | `session_id`, `agent`, `model`, `stop_reason`, `input_tokens`, `output_tokens`, `cache_read` | No, the reply has already arrived |
| `delegate` | `session_id`, `agent`, `prompt` | Yes, the sub agent is never created and the calling model gets a failed tool call |
| `compact` | `session_id`, `reason` (`automatic` or `overflow`) | No |
| `retry` | `session_id`, `from_model`, `to_model`, `error` | No, the switch has already been decided |

Every payload also carries **`cwd`**: the workspace of the session the event is about.

Other details:

* **A hook runs in that `cwd`.** Not in the directory localcode was started in, which is what it used to be: a `git status`, a `./scripts/check.sh`, a formatter looking for the project's own config all now see the project whose turn triggered them, including a delegated sub agent's, and including a session that was moved to another directory after it was created. `cwd` is on stdin as well as being the working directory, for a hook that shells out somewhere else or appends to a log shared by several projects.
* **`pre_model` can inject context.** Print `{"context":"..."}` on stdout and that text is appended to the system prompt for that one call. Useful for facts the model cannot look up: an incident in progress, a freeze window, who is on call. It costs the session its system prompt cache for every call the hook injects on, since the cached prefix is exactly the part being changed.
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
itself.

There is a second entry, for a different kind of habit. A model whose id
contains `glimmer` is asked to finish the task before ending its turn
rather than writing down what still has to happen and stopping — the
stall [`keep_going`](#a-model-that-stops-mid-task) recovers from, said in
words as well. Nothing is added for any other model.

The Web UI also unwraps the ones that arrive anyway, so a reply already in
a transcript reads properly. It handles symbols (`\rightarrow`, `\sim`,
Greek letters, relations) and the font commands that carry words rather
than maths (`\text`, `\mathbf`, `\mathrm`, `\boldsymbol` and the rest),
including when those are nested in each other: `$\mathbf{\text{ vs }}$`
is the word "vs". That half is deliberately narrow: a `$` span is only
touched when it contains a LaTeX command, so `$PATH`, `$5` and a real
formula are left alone.

The table is `modelQuirks` in `internal/agent/quirks.go`, matched as a
lowercased substring of the model id. An entry can also carry a default
`keep_going` budget, which is how the second note above comes with a
mechanism rather than only a request.

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

**Scrolling up stays scrolled up.** Both clients follow the newest output only while the view is already at the bottom of the transcript. Scroll up while a turn is running and the view stays exactly where you put it, however much the model writes underneath; scroll back down and following resumes on its own, since being at the bottom is the whole condition. There is nothing to turn on and nothing to remember. The Web UI shows a **&darr; latest** control in the corner of the transcript while the view is away from the bottom, which jumps back and resumes following. Sending a prompt or opening a session goes to the bottom whatever the view was doing, because that is what you just asked for. A background task's own window behaves the same way.

**Esc cancels whatever is running.** Press it while the model is answering (the status line says "esc to cancel") to stop that turn immediately. Cancelling also clears anything waiting in the prompt queue — the point of cancelling is to stop, so letting a queued message fire right after would defeat it. A `[cancelled]` line marks where it stopped; nothing about it is treated as an error. Pressing Esc with nothing running does nothing.

**Up and Down recall previous prompts**, the way a shell's history does. Up walks back through what you have already sent, newest first, and Down walks forward again. Stepping forward past the newest entry restores whatever you had half-typed before you started recalling, so reaching for history never costs you a draft.

Recall *starts* at the edges of the prompt box: the cursor has to already be on the first line before Up reaches for history, and on the last line before Down does. Inside a multi-line prompt the arrows just move the cursor as usual. Once a walk is under way both keys keep walking wherever the cursor has landed, so Up, Up, Up goes three prompts back; editing what was recalled ends the walk, and the next Up starts again from the newest entry. Repeating the same message twice in a row stores it once.

**The list belongs to the conversation.** Each session has its own, and switching away and back finds it as you left it. It is filled from two places: what you send, and the prompts in the transcript the daemon replays when the session opens — so reopening a session (or reattaching the TUI to one) recalls what was asked in it before, including prompts sent from another client. Nothing is stored on disk for this; the replayed transcript is the record.

Up to 200 entries per session are kept.

**Messages sent while a turn is still running are queued.** This covers the whole turn, tool execution included, not just while text is streaming. The prompt appears in the transcript immediately (the TUI marks it `[queued] <text>`) and the status line shows `(N queued)`. The first queued message sends automatically the moment the turn actually ends, and several stack up and go out in order. If a send does slip through while the daemon is busy (for example, a turn started from another client on the same session), it is queued and retried rather than shown as an error.

Commands starting with `/` are not queued, and since v0.37.0 they are not silently ignored either: both clients refuse them out loud — "can't run while a turn is in progress — wait for it to finish, or press Esc to cancel it." Queueing one would mean replaying it after the turn, by which time a second turn may have started, and it would reach the model as ordinary text instead of running. `exit` and `:q` still work mid-turn in the TUI: quitting is not something to make someone wait for.

### Running a skill

Type a skill's own name as a command. You do not have to wait for the model to decide to call the `Skill` tool.

| Command | Effect |
|---|---|
| `/skill` | Lists registered skill names and descriptions instantly, with no model call |
| `/<skill name>` | Runs that skill, for example `/pdf-tools` |
| `/<skill name> <request>` | Runs the skill with your request attached, for example `/pdf-tools merge a.pdf and b.pdf` |

The transcript keeps just the short command you typed. The full skill body goes only to the model.

**Completing a name.** Type part of one and press the right arrow. In both the TUI and the Web UI, a `/name` with nothing after it completes against the installed skills and the custom commands, and pressing the key again offers the next match:

```text
/p     ->  /pdf-tools  ->  /plan-review  ->  /pptx  ->  /p
```

The walk comes back round to what you typed, so it is never a cycle you cannot leave. The line under the prompt box shows the first match and how many there are, which is what tells you whether the key is worth pressing. Editing the text ends the walk and the next press starts a fresh one from what is now in the box.

The right arrow only completes at the very end of a one-word `/name`. Anywhere else it moves the cursor, and a prompt that already has arguments (`/pdf-tools merge a.pdf`) is past completing.

Built-in commands complete too, so `/sm` finishes to `/smart-agent` and `/perm` to `/permission-skip-all`. Four lists feed it: the installed skills, the custom commands, the commands the daemon answers, and the few each client answers itself. A name in more than one list is offered once.

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
| `/config auto_compact on\|off` | Automatic compaction; `/auto-compact` is the fuller command, with a threshold |
| `/config show_tps on\|off` | The tokens per second reading under the prompt |
| `/config auto_delegate on\|off` | Sending matching prompts to a cheaper sub agent, see [Auto delegation](#auto-delegation) |
| `/config smart_agent on\|off` | The built-in specialist roster and the orchestration prompt, see [Smart Agent](#smart-agent). Reports the roster it turned on, or says why it is empty. |

Each change records a `config.changed` event on that session and the Web UI updates its status bar right away. A newly opened client reads current values from `GET /api/settings`.

### `/compact`

Compacts the conversation immediately instead of waiting for the automatic threshold. See [Context window management](#context-window-management).

| Command | Effect |
|---|---|
| `/compact` | Compacts with the default summarization prompt |
| `/compact <instructions>` | Adds your instructions for that one summarization, for example `/compact keep only file paths` |

With nothing to compact, on an empty session, it records an error event and does nothing. On success it records a `compacted` event just like automatic compaction, marked `manual: true`, and shows a confirmation.

### `/usage`

Shows cumulative token counts per model for the current session, with no model call. **Token counts only, never dollar figures.**

Unlike the context percentage in the status bar, which is a snapshot of the most recent call, `/usage` sums every API call since the session started. Each call is billed for the entire history it resends, so a sum rather than a snapshot is the correct answer to "how much has this session used".

With no calls yet, it just says so.

### `/context`

Shows what is actually in the request this session would send next, and what is not, with no model call.

The prompt a turn sends is assembled from a declared inventory rather than concatenated: every piece has a stable id, a source, a trust class, a place in the request, and a condition saying when it applies. `/context` reports the assembly for the next turn, which is the one you are deciding whether to send:

* which assets are included, with the size of each and the reason it applies,
* a per category total, because "the prompt is 4,000 tokens" is not actionable and "the project's rules are 3,200 of it" is,
* whether the request carries external content, which is data and never instruction,
* warnings, such as an unstable asset sitting ahead of a stable one and spoiling the cache prefix behind it,
* and the rest of what fills a window: the tool definitions, counted separately for built-in tools and for each MCP server, the conversation so far, and the room reserved for the answer, against the window itself.

`/context all` also lists what was left out and why, which is the form that answers "why are my project's rules not in there". It is also the form that lists the conversation's own sources one by one: a request carries an entry for every tool result it is still sending, and past a dozen the short form folds them into a count rather than printing a transcript index.

Identities, sizes and reasons only: the bodies are never printed. The assets include the workspace's own instructions and anything a hook injected, and the transcript is a durable log.

Token figures are estimated from character counts. About right for English, and a floor for Korean and Japanese, which run several times denser.

The same assembly is recorded in the turn log as `prompt_manifest`, `prompt_assets` and `prompt_untrusted`, and the full record is kept beside the trace under `~/.localcode/manifests/`, aged by the same retention. `/context <id>` reads one back: the request that actually went out, with its inclusion and exclusion reasons, hashes, warnings and any provider lowering. That is the difference between the two forms, and it is worth stating plainly: bare `/context` describes the hypothetical next turn, `/context <id>` describes a call that happened.

A fallback that reports the same manifest id on a different model family reused a prompt written for the model that failed, which is what the id makes visible.

### The switches

These settings change how every turn behaves, and each has a command of its own. They are answered by the daemon rather than by a client, so the TUI and the Web UI both have them and both say the same thing about them.

**Daemon-wide**, saved to config.json:

| Command | Setting | What it does |
|---|---|---|
| `/smart-agent` | `smart_agent` | The specialist roster, the fallback chain, the trace, the prompt cache markers and the guards. See [Smart Agent](#smart-agent). |
| `/auto-delegate` | `auto_delegate` | Sends matching prompts to a cheaper agent. See [Auto delegation](#auto-delegation). |
| `/keep-going` | `keep_going` | The carry-on nudge for muse models. See [A model that stops mid-task](#a-model-that-stops-mid-task). The settings window has the same switch as a checkbox. |
| `/auto-compact` | `auto_compact_enabled`, `auto_compact_percent` | Auto-compaction. A number sets the threshold and turns it on: `/auto-compact 70` compacts past 70% of the context window. The default threshold is 50%. |

**Per conversation**, saved with the session. config.json holds their defaults. See [The four switches](#the-four-switches):

| Command | Switch | What it allows without asking |
|---|---|---|
| `/permission-skip-all` | `skip_all` | Everything, the workspace boundary included. |
| `/permission-skip-tools` | `skip_tools` | Every tool prompt, and not a path that leaves the workspace. |
| `/read-outside` | `read_outside` | Reading outside the workspace. `mem-clear` forgets the directories approved at a prompt. |

Two more commands book work for later. See [Scheduled tasks](#scheduled-tasks):

| Command | What it does |
|---|---|
| `/schedule` | `/schedule <when> <what to do>` books a prompt; `/schedule cancel <id>` removes one. |
| `/show-scheduled-task` | Lists this conversation's booked tasks. |
| `/write-outside` | `write_outside` | Writing outside the workspace. `mem-clear` likewise. |

With no argument each one flips: `/smart-agent` turns it on if it was off. `on` and `off` set it outright, which is what you want in a script or when you are not sure what it currently is.

**They save.** A daemon-wide choice is written to config.json and a per-conversation one is saved with the session, so either survives a restart, which is how the same switches have always behaved in the settings window and is not how `/config` behaved: `/config smart_agent on` applies for the run and forgets. When there is no config.json to write to, or the write fails, the change still applies and the reply says so rather than reporting a change that did happen as one that did not.

Each reply says what the switch did and what it did not. Turning Smart Agent on with no profiles configured is legal and inert, so it says so instead of reading as a change that took effect; the same for auto-delegation with no `auto_delegate` block. `/permission-skip-all on` says in as many words that shell commands and paths outside the workspace will no longer ask in this conversation, that rules which **deny** still deny, and that `/permission-skip-tools` is the same thing without the last part.

`/config` still works and still lists all four settings including `auto_compact`.

Two more commands apply an edited configuration without restarting:

| Command | What it does |
|---|---|
| `/reset-mcp` | Stops the MCP servers, re-reads their configuration from config.json, reconnects, and swaps the tools. A server removed from the file takes its tools with it; one added while the daemon runs connects. The status indicator follows. |
| `/reset-skills` | Reloads skills from disk, against the live workspace. A skill installed or edited mid-run applies immediately, and the name completes like any other. |

### Other local commands

These are typed into the message box but never reach the event log, so replaying a session does not bring them back.

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
| `/debate` | `/debate <reviewer>[,<reviewer>] [rounds] <what to do>` — this conversation's agent writes, the others review, round after round. Also started by asking for it in words, or with the ⚖️ button. See [Debate](#debate). |
| `/effort` | `/effort [off\|low\|medium\|high]` — how hard the model is asked to think in this conversation, and what that reaches on this model. `default` goes back to the profile's. See [Effort](#effort). |
| `exit`, `:q` | Quits the TUI, same as Ctrl+C. The Web UI only prints a note, since a browser cannot quit the program. Close the tab yourself. |

## Part 5. Sessions

### Switching sessions

A session is an append only event log that lives as long as the daemon, so reopening the TUI or a browser tab picks the conversation back up.

* **TUI**: at startup, if any session exists, the terminal lists them with session ID, agent, and creation time. Enter a number to resume, or `n` or an empty line to start fresh. From the same screen, `d<number>` such as `d1` deletes one session and reshows the list, and `da` deletes every session after you type `yes` to confirm.
* **TUI, once running**: `/session` opens the same choice without restarting. Arrow keys move, Enter switches, Esc cancels. Switching clears the screen and replays the chosen conversation from its own log; the prompt recall history goes with it, since it belonged to the conversation you left.
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
* **Deleting cascades to everything the session started.** A background task runs in a child session of its own, invisible and unlisted, and a task can delegate again, so the tree can be several levels deep. Deleting the conversation stops every running descendant, waits for it to unwind, and then removes all of their logs and metadata along with the parent's. Nothing is left running and nothing is left on disk.
* A task that was stopped this way reports a stopped outcome rather than an empty answer, so a collection in flight can tell "the session went away" from "the task had nothing to say".
* A new background task cannot be launched under a session while it is being deleted. Deletion and delegation share one claim: a delegation already under way is waited for and included in the stop, and one that starts afterwards is refused with an error saying the session is going away. There is no order in which a delete and a delegation can interleave that leaves work running or a session behind.
* Delete all is refused with 409 if **any** session has a turn running, and nothing is deleted, so a partial delete never leaves things in an unclear state. It stops running background work first, the same way a single delete does.
* While delete all is running, starting a turn, creating a session and forking one are all refused with 409. The refusal is decided and registered in one step rather than checked and then acted on, so a request that was already past its check when delete all began is waited for and included, rather than committing into a store that is being emptied. For a fork the window opens before the source conversation is read, not just before the copy is created, because a fork depends on state: a copy made from a snapshot taken outside the window would let a deleted conversation reappear. There is no order in which the two can interleave that leaves a session behind, removes a log from under a turn, or recreates a conversation from one that was just deleted.

### Context window management

At the end of every turn, the token usage the provider reports is recorded as a `usage` event. Bedrock, Anthropic, and any OpenAI compatible server asked for `stream_options.include_usage` all supply it.

Each event carries input and output token counts, the model's known maximum context from the best effort table in [internal/modelinfo](../internal/modelinfo/modelinfo.go) defaulting to 128000 for unknown models, the percentage filled, and tokens per second. Both clients drive their status bar from this.

**Automatic compaction.** Once context use passes the threshold (**50%** unless `/auto-compact <percent>` set another), and `auto_compact` is on, the next message triggers a one time summarization of the whole conversation. That summary replaces the history and the new message is sent after it. The transcript notes that compaction happened.

**When a request is too big anyway.** The threshold watches the history alone, and what a server refuses is the history *plus* the room the request reserves for the reply, so a large `max_tokens` against a small window can be refused while the meter reads half full. Four things guard against that.

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
| Title | The session's title, or its ID if it has never been renamed, behind a light: **grey** — nothing is running here; **blinking amber** — a turn is running; **steady green** — a reply arrived while you were in another session and you have not opened it since. Amber is the same colour a running background task carries, and green means one thing everywhere: this is up. Every card has one, so an idle session says so rather than looking like a panel that does not draw lights. |
| Workspace | The directory the session was created in, shortened from the front (the full path is in the tooltip). This is what distinguishes two sessions that would otherwise look identical because they belong to different projects. |
| Created | Local date and time |

**Clicking anywhere on a card switches to that session** — the transcript, agent, and status bar all follow. The `rename` and `delete` buttons on the card act on that session without switching to it. Above the list is **+ new**, below it **delete all sessions**; both destructive actions confirm first.

**Drag a card up or down to put it where you want it.** The order is saved on the daemon (`POST /api/sessions/order`, and in each session's metadata file), so it survives a restart and is the same in every client. Newest-first is only the starting arrangement: it sinks the conversation you have lived in all week below every throwaway one started since. A session created after you have arranged the panel appears at the top, where a new session has always appeared, rather than at the bottom of an arrangement it was never part of.

The workspace shown on a card is where that session currently is: recorded when it was created, and updated when the workspace is switched while that session is the open one. Switching in one session never relabels another. Sessions created before this field existed show `(workspace not recorded)`.

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
| Activity light | Three states: **grey** — no live connection to the model (the event stream to the daemon is down); **solid green** — connected and idle; **blinking amber** — a turn is running in this session. It recovers to green on its own when a stopped daemon comes back, since the browser reconnects the stream automatically. "Running" is the daemon's own answer, not this page's memory of having sent something, so it agrees with the light on the session's row in the left panel however the turn started — from another client, or before this page was loaded. |
| Stop button | Appears while a turn is running, whoever started it. Click it to cancel, the same as Esc — which is the faster route but depends on the key reaching the page. |
| Auto-delegate pill | `auto-delegate: on` / `off`. Click to open a panel setting which prompts are delegated and which agent answers them — see [Auto delegation](#auto-delegation). |
| Permission pill | `permissions: ask (N rules)` or `permissions: skip`. Click it to view or change permission settings — see [Viewing and changing permission settings](#viewing-and-changing-permission-settings-without-waiting-for-a-prompt). |
| Settings pill | `⚙ settings`. Opens the settings window, which holds the [Smart Agent](#smart-agent) switch and the update controls. See [Checking for updates](#checking-for-updates). |

### Switching agents with Tab

`Tab` switches to the next agent and `Shift+Tab` to the previous one, the same as in the TUI — the browser's normal "move focus to the next control" behavior is suppressed so the key means the same thing in both clients. Focus stays in the prompt box. Inside a modal (permissions, workspace) Tab still moves between fields, which is the only thing it could usefully mean there.

The header dropdown does the same thing and lists each agent with the model it resolves to, e.g. `explore (qwen3-1.7b)`; the agent's description is in the option's tooltip.

In the TUI, `/model` opens the same choice as a list: arrow keys to move, Enter to switch, Esc to cancel, with the model each agent resolves to beside its name. Tab still cycles, which is faster when there are two agents and blind when there are six.

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
- **Not for commands.** `/compact`, `/agent` and the rest don't go through the message endpoint. Typing one mid-turn is refused with a line saying so, rather than queued or ignored.

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
| `Task` | No | Delegate to another named agent and wait for its result. Offered only when there are 2 or more agents to delegate to, which [Smart Agent](#smart-agent) is one way to arrange. |
| `TaskBackground` | No | Start a sub agent and return its task id straight away. Offered only with [Smart Agent](#smart-agent) on. |
| `TaskCollect` | No | Wait for background sub agents and return what they found. Offered only with [Smart Agent](#smart-agent) on. |

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

1. A new `explore` session is created, recording `task.spawned` on the parent. It does not queue: `max_concurrent_tasks` bounds background fan out, and a synchronous delegation cannot fan out.
2. One turn runs **synchronously** with `explore`'s profile, prompt, and tools. Unlike [background tasks](#background-tasks), the delegating agent's turn waits for this.
3. `explore`'s final answer text is returned as the tool result, and the delegating agent continues from it.

Delegation deeper than 3 levels is refused automatically, so agents cannot recurse into each other forever. The depth travels with the delegation, including into a background task, so no mixture of `Task` and `TaskBackground` gets a fresh allowance.

### Smart Agent

Off by default. One switch, in the settings window (the gear pill under the prompt), with `/config smart_agent on|off` and `"smart_agent": true` in config.json as the other two ways to set it. The change applies on every session on the daemon, and is saved to config.json so it survives a restart.

**When the change takes effect.** Work already admitted keeps the setting it was admitted under, all the way through:

| Unit of work | Sees the change |
|---|---|
| A message not yet sent | Yes, immediately |
| A turn already running | No. Its agent roster, tool allowlist, the delegation roster its `Task` and `TaskBackground` schemas advertise, fallback chain, cache markers, credential guards and turn log all stay as they were when it started, even if it is in a long tool loop |
| A sub agent started by `Task` | No. It runs under the state its parent turn was admitted with |
| A background task launched with `TaskBackground` | No, including while it is waiting for a free slot. A specialist admitted with a read only tool list starts with that list whenever it eventually runs |

The alternative was rereading the switch at every point that consults it, which let a single turn run half enabled.

If the settings window cannot write config.json it says so beside the switch: the change is applied to the running daemon either way, and the warning is only about whether it will survive a restart.

What it turns on is a way of working rather than a preference, which is why it is opt in: one request can become several model calls against several contexts. That is the point of it and it is also a bill nobody agreed to by installing an update.

#### What it adds

| Added | Detail |
|---|---|
| Six specialist agents | `explore`, `librarian`, `oracle`, `plan`, `implement`, `verify`. They exist without being configured, and disappear again when the switch is turned off. See [What the roster needs from your config](#what-the-roster-needs-from-your-config). |
| An orchestration prompt | Appended to the system prompt of top level sessions only. It tells the model to work out what is being asked, send wide reading to a sub agent, do the narrow work itself, verify before reporting, and say what it checked. |
| `TaskBackground` and `TaskCollect` | Launch several specialists at once and pick up the answers together, instead of waiting for each in turn. |

| [Fallback chains](#fallback-chains-when-a-model-will-not-answer) | A turn survives a rate limit or an outage by retrying the same endpoint with a bounded backoff, then moving to the next profile, re-deriving the prompt for the model it moved to. |
| [A turn log](#the-turn-log) | One JSON line per thing that happened, correlated across sub agents by a trace id. |
| [Cache breakpoints](#prompt-cache-breakpoints) | The tool schemas, the system prompt and the tail of the conversation are marked, so the provider can serve the unchanged part of every request from cache. |
| [Two guards](#secrets-and-the-workspace-boundary) | Credential files are refused, and paths outside the session's workspace — symlinks resolved — are asked about. |
| [A trust boundary](#the-trust-boundary) | The system prompt states which sources are instructions and which are data, and MCP output arrives framed as data. |

#### What the roster needs from your config

Nothing, in the sense that matters: **you do not declare the specialists.** With `smart_agent` on, localcode creates all six and points each at whichever of your profiles suits the work it does. What it needs from you is profiles to choose between.

Three is the useful number, because the roster sorts into three kinds of work:

| Kind | Agents | Work |
|---|---|---|
| quick | `explore`, `verify` | Searching, running a build |
| balanced | `implement` | Making one self-contained change |
| deep | `plan`, `oracle`, `librarian` | Judgement: design, review, reading something long |

With one profile everything runs on it and orchestration still works; it just costs the same everywhere, which is most of the point of having a roster. With none at all there is no roster, and turning the switch on says so.

Name a profile `smart-quick`, `smart-balanced` or `smart-deep` to pin a category by hand; no heuristic gets a vote after that. Without those names localcode guesses from the model ids, which works and is worth not relying on.

**`config.sample.json` is this written out**, with the six prompts in full. A test in `internal/smart` holds that file to the built-in roster, so the prompts in it are the prompts localcode actually uses rather than a copy that drifted.

To replace one, declare an agent with the same name. A name you have declared is yours entirely — prompt, model and tools — and localcode does not supply its own version of it.

#### The specialists

| Agent | Runs on | Tools | For |
|---|---|---|---|
| `explore` | the quick profile | `read_file`, `glob`, `grep`, `Skill` | Finding where something lives. Paths and line numbers, not explanations. |
| `librarian` | the deep profile | `read_file`, `glob`, `grep`, `Skill` | Working through documentation, a long file, or an unfamiliar subsystem. |
| `oracle` | the deep profile | `read_file`, `glob`, `grep`, `Skill` | Reviewing a change or a design for what is actually wrong with it. |
| `plan` | the deep profile | `read_file`, `glob`, `grep`, `Skill` | Turning a large request into ordered, concrete steps. |
| `implement` | the balanced profile | `read_file`, `write_file`, `edit`, `bash`, `glob`, `grep`, `Skill` | One self contained change, carried out and checked. |
| `verify` | the quick profile | `read_file`, `bash`, `glob`, `grep` | Running the build or the tests and reporting what happened. |

Two properties of that table are enforced rather than requested:

* **No specialist has the delegation tools.** A sub agent that can delegate can spawn sub agents that can delegate. "Do not delegate" in a prompt is a request; leaving `Task` out of the allowlist is the answer. Delegation deeper than 3 levels is refused as well, background launches included.
* **The four investigating agents have no shell.** A read only agent with `bash` is not read only, and the value of sending investigation elsewhere is that its answer can be trusted not to have changed anything on the way.

Every specialist is also told to answer in under 300 words, with paths and line numbers rather than pasted output. That is the whole economy of the feature: a sub agent can read fifty thousand tokens in a context the main session never pays for, and hand back two hundred.

An agent you have defined yourself under one of those names is left alone completely, prompt, model and tools. The roster is a starting point, not something that overwrites a deliberate choice.

#### Which model each specialist runs on

Not named in the build, because the models named there would be the models on one developer's machine. Each specialist asks for a capability class instead, and the class is resolved against the profiles already in your config.json.

| Class | Wants | Matched from the model id by |
|---|---|---|
| `quick` | The cheap fast one | `haiku`, `mini`, `flash`, `nano`, `lite`, `small`, `turbo`, and small parameter counts such as `-8b` |
| `balanced` | Good enough to be trusted with an edit | `sonnet`, `coder`, `medium`, `gpt-4`, and mid parameter counts such as `-30b` |
| `deep` | The strongest one | `opus`, `gpt-5`, `pro`, `ultra`, `thinking`, `-r1`, and large parameter counts such as `-70b` |

The lightest class a model id matches wins, so `gpt-5-mini` is quick and not deep: the size qualifier is the specific half of the name and the family is the general one. A class with no match falls back to the nearest class, then to `default_profile`. With one profile configured every specialist runs on it, which is correct: the separate context is the benefit, and a different model is a bonus.

To settle it by hand, name a profile `smart-quick`, `smart-balanced` or `smart-deep`. That beats every heuristic outright.

```json
"profiles": {
  "smart-quick": { "provider": "local", "model": "whatever-my-server-loaded" },
  "smart-deep":  { "provider": "bedrock", "model": "us.anthropic.claude-opus-4-6-v1" }
}
```

#### Running several at once

`Task` waits. For one question that is right. For three independent questions it means waiting three times, so:

1. `TaskBackground({"agent":"explore","prompt":"..."})` three times. Each returns a task id straight away and the orchestrator keeps working.
2. `TaskCollect({})` once. It waits for all of them and returns the answers in the order they were launched, so the model can match each answer to what it asked. `TaskCollect({"task_id":"..."})` takes just one and leaves the rest outstanding.

Stopping a turn stops the collection, not the tasks. Anything that had not finished stays outstanding and can be collected again; the answer is not lost because the turn that asked for it went away. `TaskCollect` says how many were left running.

A session may have 8 launched and uncollected tasks at once. The ninth is refused, and says to collect first. It is a ceiling rather than a queue on purpose: every outstanding task is a model spending tokens in a session nobody is reading, so hitting it is a signal.

Background tasks appear in the Web UI's right panel while they run, the same as tasks started through the API.

#### Fallback chains: when a model will not answer

In a long agent session a model failure is an ordinary condition rather than an exception: a rate limit at the wrong moment, a provider having a bad hour, a local server that was restarted, a credential that expired overnight. Without somewhere else to go, every one of those ends the turn and loses whatever it was in the middle of.

Name the somewhere else on the profile.

```json
"profiles": {
  "strong":  { "provider": "bedrock", "model": "us.anthropic.claude-opus-4-6-v1", "fallback": ["balanced", "local"] },
  "balanced":{ "provider": "bedrock", "model": "us.anthropic.claude-sonnet-4-5-20250929-v1:0" },
  "local":   { "provider": "local", "model": "qwen3-30b-a3b" }
}
```

| Detail | Behaviour |
|---|---|
| Before it fires | A transient failure — a rate limit, a 5xx, a dropped connection — is retried on the same endpoint first: up to two attempts with a 1s then 2s backoff. The common rate limit is a blip, and the fallback is a different model with a different cache prefix, so the chain is spent only when waiting did not help. Credential and model-identity failures skip the retry, because a 401 will be a 401 in two seconds too. The retry is counted only when it is actually attempted: a turn cancelled during the backoff ends there, recording the cancellation rather than a retry or a fallback that never reached a provider |
| When it fires | Rate limits and quota that outlast the retries, 5xx and gateway errors, a connection that is refused or times out, a model or credential the endpoint does not have |
| When it does not | A conversation too long for the window, which is summarized and retried on the same model instead; a request another endpoint would refuse the same way, such as a bad parameter or a tool schema the API will not take; and a stream that had already written part of an answer, since falling back there would leave the conversation carrying both halves |
| How it decides | On what the error says the cause was, not on which exception class carried it. Bedrock raises `ValidationException` for a model id that does not exist in the region, for an account not entitled to the model, and for a request field the model will not accept: the first two move to the next profile and the third does not |
| What a refusal costs | Nothing. An error the chain does not cover ends the turn without contacting a fallback and without consuming a link, so a rate limit arriving later still gets the first fallback rather than the second |
| Order | The primary profile's own list, in order. The list is flat: a fallback's own `fallback` is not followed, so a chain cannot loop and its length is what it says |
| Visibility | Each retry and each switch is recorded in the transcript, naming what failed and what happens next. A session that quietly got worse, or quietly paused, is the failure mode this avoids |
| Validation | Names are checked when the config loads, not when something breaks. A chain is read exactly when something has already gone wrong |

**The model is not the only thing that changes.** Both the orchestration prompt and the per-model formatting note are written per model family, so falling back re-derives the whole request rather than resending the failed one with a different model id. A local open weight model that catches an overflow from a hosted flagship gets the prompt written for it. This is the reason `fallback` is part of Smart Agent rather than a standalone setting.

#### The turn log

Smart Agent writes a structured record of what each turn did to `~/.localcode/trace/localcode-<date>.jsonl`, one JSON object per line.

A multi agent turn cannot be debugged from a transcript. The transcript shows what the main conversation said. It does not show that the answer came from the second model in a fallback chain, that four fifths of the input was served from cache, that a sub agent spent ninety seconds in one grep, or that the turn compacted twice on the way.

| Field | Meaning |
|---|---|
| `trace_id` | One per top level turn, inherited by every sub agent it spawns. This is what makes a fan out to three specialists one story rather than four unrelated logs |
| `span` | `turn.start`, `model`, `tool`, `delegate`, `retry`, `fallback`, `compact`, `turn.end` |
| `session_id`, `parent_session_id` | Which session, and whose child it is |
| `agent`, `profile`, `model`, `provider` | Who answered, on what |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` | What it cost. The read count is how you tell a working cache breakpoint from one that is doing nothing |
| `tool`, `duration_ms` | Which tool and how long it took, timed around the whole call so a wait on a permission prompt reads as a wait |
| `finish_reason`, `fallbacks`, `retries`, `compactions` | On `turn.end`: did this turn have a bad time |
| `prompt_manifest`, `prompt_assets`, `prompt_untrusted` | Which prompt assembly this call was built from, which assets it selected, and which of them carried external content. Identities only, never the bodies. See [`/context`](#context) |

```bash
jq -c 'select(.trace_id=="a1b2c3d4e5f6a7b8")' ~/.localcode/trace/localcode-2026-08-25.jsonl
```

`GET /api/trace` returns the last records held in memory, for the question asked while a session is still open. `?limit=`, `?session=` and `?trace=` narrow it. Nothing is written with Smart Agent off, and the file is not created until the first record.

**Retention.** Files older than 30 days are removed, so a daemon left running for months does not accumulate one file per day forever. The removal happens once the configured bounds are installed at startup, and again at each day rotation — never before the configuration is read, so a longer configured retention protects its own files from the very first prune. Two config keys adjust it: `trace_max_age_days` changes the age (values at or below zero mean the default, not "keep forever"), and `trace_max_total_mb`, when set, additionally removes the oldest files until the directory fits under it. The file being written today is never removed.

Both keys bound the prompt-manifest directory too, which sits beside the trace and holds the assemblies its `prompt_manifest` ids refer to. The two are bounded separately rather than together, so a busy manifest directory cannot evict trace files or the other way round; the consequence worth knowing is that a trace line can outlive its manifest if the two fill at different rates, and `/context <id>` says so when it cannot resolve one. Each assembly is written once per day however many calls share it, since a manifest is immutable content addressed by its own id.

#### Prompt cache breakpoints

The stable half of an agent request is the tool schemas and the system prompt, and in a long session it is the same bytes every turn. Smart Agent marks the end of it so the provider can serve it from cache, at roughly a tenth of the price of reading it again.

| Backend | What is marked |
|---|---|
| Anthropic API | `cache_control` on the last tool, on the system prompt, and on the last block of each of the last two messages |
| Bedrock | A `cachePoint` after the tools, after the system prompt, and after each of the last two messages |
| openai-compatible | Nothing. Local servers do their own prefix caching with nothing to declare |

The conversation marks move with the history, and that is cheaper than it sounds: the history is append-only, so each request's marked prefix contains the previous one's, the provider serves the shared part at the cache rate, and only the new suffix is written at the premium. Two moving marks rather than one because a cache lookup only checks a bounded distance behind each breakpoint, and one long tool round can outrun it; the older mark keeps that miss from reaching back to the start of the conversation. Four marks total, which is the Anthropic API's limit.

A breakpoint the provider does not honour is harmless: prefixes below the provider's minimum (about 1024 tokens on most Claude models) are ignored and the request is priced as it was before.

Because each specialist runs in its own session, each has its own stable prefix and its own cache. That is a reason to delegate rather than a cost of it.

#### Secrets and the workspace boundary

Two shipped guards. The credential one is on with Smart Agent on; the workspace boundary is always on, because it is a safety property rather than an orchestration feature. Both are overridable.

**Secrets are refused outright.** `read_file`, `write_file` and `edit` are denied for paths that look like a credential store: `.env` and `.env.*`, private keys (`*.pem`, `*.key`, `id_rsa` and friends), `~/.ssh`, `~/.aws/credentials`, `~/.kube/config`, `~/.npmrc`, `*credentials.json`, `.netrc`. The threat does not need a malicious model: a summarized repository or a "what is in this directory" is enough, and once a credential is in a context it has been sent to a provider.

Denied rather than asked, because "may I read your SSH private key?" has one right answer and asking it teaches people to click yes. `skip_permissions` does not unlock them: it downgrades ask to allow and never touches deny. To allow one, write the rule:

```json
"permission": { "read_file": [{ "match": "*.env", "decision": "allow" }] }
```

`bash` is deliberately not covered. A shell command is not a path, and matching `cat .env` out of an arbitrary command line catches the honest case and misses every other one. The shell has its own rules.

### Leaving the project

**A path that lands outside the session's own directory is a question of its own.** Not a refusal: reading a system header or a file in another checkout is ordinary work. It is the difference between an agent that stays where it was pointed and one that is discovered to have been somewhere else. A `deny` stays a `deny`, and a rule that allows `write_file` everywhere is a statement about this project rather than a licence to edit the one next door.

**Which tools it covers**, and what counts as reading or writing:

| Class | Tools | Switch |
|---|---|---|
| Reading | `read_file`, `grep`, `glob` | `read_outside` |
| Writing | `write_file`, `edit` | `write_outside` |
| Not covered | `bash` | — |

`bash` is not covered and does not claim to be. A shell command is not a path, and `cd /etc && cat passwd` cannot be judged by looking at it. `bash` asks on its own terms, as it always has.

**The question is answered by place**, because a place is what it is about. A model told to read a sibling repository reads forty files in it, and forty prompts is one decision and thirty-nine keystrokes, which is how a permission prompt stops being read.

| Answer | TUI key | Effect |
|---|---|---|
| Allow once | `y` | This call only. |
| Deny | `n` | Refuses this call. |
| Allow this directory | `d` | That directory and everything under it, for the rest of this session. Nothing is written to disk. |
| Allow anywhere outside | `s` | Turns this conversation's `read_outside` or `write_outside` on, so it shows in the Permissions panel and can be turned off there. |

The prompt says which project the path is outside of, and names the directory `d` would cover, before you answer.

`/read-outside mem-clear` and `/write-outside mem-clear` forget the directories approved with `d`, without touching the switches: somebody who approved one directory by mistake should not have to change a setting to take it back. The Permissions panel lists them with a **forget** button each. A background task inherits its parent's approved directories, since it works in the parent's project on work the parent authorized.

`permission-skip-tools` does not silence this question. Only `permission-skip-all` does.

The comparison is between physical paths: symlinks are resolved on both sides before the question is decided, so a link inside the workspace pointing at `~/.aws` is outside, which is where it actually leads, and a workspace that is itself reached through a link (macOS's `/tmp`) still contains its own files. A path that cannot be resolved at all — a link loop, a permission failure — is treated as outside, which costs one question rather than one blind allow. A file about to be created is judged by its closest existing ancestor, so a new file under a link is judged by where the link leads — and a dangling symlink, whose own target does not exist yet, is followed to that target rather than judged by where it sits, because writing through it is what creates the file there.

#### The trust boundary

Everything a turn reads arrives as text, and not all of it deserves the same standing. With Smart Agent on, two things say so:

* **The system prompt states the ranking**, for the orchestrator and every specialist: instructions come from the user, the system prompt and the project's own rule files; tool results — file contents, command output, fetched pages, MCP output — are data, to be used but not obeyed. A tool result containing text addressed to the model is content to surface, not an instruction to follow.
* **MCP output is framed as data on arrival.** An MCP server's output is the least trusted text a turn reads — another process, possibly another machine, its words going straight into the model — so it is wrapped in a marker naming the server and saying not to follow instructions inside it, rather than handed over bare. Error results are wrapped too — the server controls both its text and its error flag, so an unlabelled error path would just be a way around the label.

This is labelling, not enforcement, and it is not claimed as more: a model can still be talked into something by a sufficiently crafted input. What it changes is the default — the model has been told which sources rank where before the crafted input arrives. The permission system stays the enforcement layer: every MCP tool call still asks, whatever its description says.

**MCP servers are pinned on first use.** Tool descriptions steer the model, and a server whose descriptions change between runs changes what the model is told it can do, silently. Each server's advertised surface — every tool's name, description and schema — is fingerprinted into `~/.localcode/mcp-pins.json` on first connect, and a change since the last run is reported as a startup warning naming the server. Warn once, not refuse: tools also change because somebody upgraded the server, and the person who did not upgrade it is the one the warning is for. The pin file works with Smart Agent on or off, since it guards startup rather than a turn.

#### The prompt is written for the model

Role stays constant, prompt does not. A policy that reads as a helpful summary to one model reads as a checklist to be performed by another, and the failure modes are opposite: one delegates nothing, the other delegates its own reasoning.

| Model family | Difference |
|---|---|
| Default | The full policy, for models that follow one stated once |
| GPT and o series | The same, plus a stopping rule: delegate a question once, and never delegate your own reasoning |
| Gemini | The same, plus a concrete threshold, since a long context model left alone reads everything itself |
| Local open weight models (qwen, glm, kimi, llama, mistral, gemma, deepseek and similar) | Shorter and flat, one rule per line. Background delegation is left out entirely: launching work and never collecting it is worse than not launching it. |

#### What it does not do

* Nothing is added to a session that is already a sub agent, so a specialist is never told to orchestrate.
* Nothing is added when there is nowhere to delegate to, which is the case when config.json has no profiles at all.
* It does not change permissions. Every tool a specialist calls goes through the same allow/ask/deny rules and the same prompt as any other tool call, asked in the session that spawned it.
* The specialists do not appear in the agent picker. They are delegation targets rather than conversation modes, so `Tab`, `/agent` and the header dropdown still cycle only the agents in config.json.

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

### Debate

Two agents on one piece of work, or four: this conversation's agent writes it, the reviewers read it and say what is wrong, it answers them, and that repeats.

**Just say so.** An ordinary message that asks for review-and-iterate starts one:

```
1부터 10까지 더하는 파이선 프로그램을 만들어라. 완료되면 @girl이 검토하고, 그 결과를 반영해라. 10번 반복해라.
write a retry wrapper for the upload call, then have the review agent check it and fix what it finds, 5 rounds
```

The model reads the sentence and separates the three things in it — who reviews, how many rounds, and the work. **localcode runs the loop**, and asks first: the confirmation names the reviewers, the rounds, the model turns it will cost, and the task as localcode read it. That echo is the point — a protocol sentence that leaked into the task is visible before the turns are spent.

**The ⚖️ button** (under the prompt box) asks for the same three things as three fields, and shows the command it is about to send as you fill it in.

**Or type it.** `/debate <reviewer>[,<reviewer>] [rounds] <what to do>`:

```
/debate girl 10 1부터 10까지 더하는 파이선 프로그램을 만들어라
/debate review write a retry wrapper around the upload call
/debate girl,tom 5 rewrite the parser
```

The reviewers are any configured agents other than the one already running; the rounds default to **3** and cannot exceed **10**. Rounds are positional and optional, and a count is a token that is *only* digits — `/debate girl 10페이지 문서를 써라` is three rounds writing a ten-page document, not ten rounds writing "페이지 문서를 써라".

**The protocol is not in the prompt.** Everything except the work itself lives in the command or the tool arguments: who reviews, how many times, when to stop. That is not brevity, it is correctness — a prompt that still says "review it ten times" gives the author a loop of its own to run inside the loop already running it, and a model told to repeat ten times inside one turn stops at three and reports that it is done. localcode counts the rounds; the models do the work.

**A round is one author turn plus one turn per reviewer**, plus whatever tools each of them runs. Ten rounds with two reviewers is thirty turns, which is why the numbers are shown before it starts.

**Who runs where.** The author runs in this conversation, with its history and its cached prefix intact, so its work appears exactly as it always does. Each reviewer runs in a sub-session of its own — switching this session's model mid-conversation would invalidate its tools, its system prompt and its cache at once — and **keeps that session for every round**, which is what lets it say "the second thing I raised is still not fixed". Its row appears in the right panel; click it to read the whole review session, tool calls included.

**A panel reviews independently.** Reviewers run at the same time, in separate sessions, and never see each other's findings. Three models agreeing is worth something only if they arrived there separately, and **all of them have to approve** — one holdout keeps the debate going, because taking the approval would be picking the answer that ends the work.

**The reviewer can read, and can run your check, and cannot write.**

| | |
|---|---|
| Reading | `read_file`, `glob`, `grep` |
| Checking | `check` — runs `verify_command` from config.json, exactly, with no arguments |
| Reporting | `Verdict` |
| Never | `write_file`, `edit`, `bash` |

`bash` is the one worth explaining: a shell command is not a path and cannot be judged by looking at it, so a reviewer with a shell is a second author editing the same files from the other side. `check` is the narrow way back — one line you wrote in your own config, fixed before any model saw it:

```json
"verify_command": "go test ./... && go vet ./..."
```

The model chooses whether to run the check, never what the check is. Unset, the tool is not registered at all rather than registered and always failing. An agent with its own `tools` list only gets what that list also permits, so add `"check"` to it.

**What the reviewer is given.** The task, the author's own account of what it did, what actually changed, and the workspace to read for itself. Not this conversation's transcript: a history full of another model's tool calls is rejected outright by some providers, and it would be re-sent every round. The account and the change report are deliberately separate — one is what the author says, the other is what happened.

The change report is a real diff where there is a repository to ask: `git diff HEAD`, plus the names of files not yet added to git. Outside a repository it falls back to the files localcode watched `write_file` and `edit` touch, **labelled as what it is** — a file the shell moved or generated is not in that list, and a reviewer that took it for a diff would review the wrong set without knowing.

**How it ends**, and it always says which:

| Reason | What happened |
|---|---|
| `approved` | Every reviewer approved. |
| `rounds` | The budget ran out with no approval. The work stands; read it before trusting it. |
| `stalled` | Two rounds running in which the author called no tool at all. That is a standoff, and the rounds left would only restate it. |
| `stopped` | You pressed Stop. What was done is kept. |

The approval is a **tool call**, not a sentence: the reviewer sets a boolean, because "did it approve" cannot be read reliably out of prose in two languages. A model that will not call tools can end its reply with a line that is exactly `APPROVED`. Anything else — silence, an unreadable call, a reply with the word inside a sentence — is *not approved*, because ending a debate a round early on a misread stops the work being looked at while the transcript says somebody signed it off. The verdict tool is hidden from every turn that is not a review, so no model is ever in a position to mark its own homework.

**Models agreeing is not evidence that the code is right.** It is two or three readings instead of one. The tests are the evidence, which is what `verify_command` is for.

A debate can only be started from a conversation somebody is having: not from a sub-agent, not from a scheduled run, and not from inside a debate. Nobody is watching those, and a tree of debates has no ceiling on it.

### Effort

How hard the model is asked to think, as one word:

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

The conversation wins over the profile. The question belongs to the work rather than to the model — the same model answering "which file is this in" and "why does this deadlock" wants different amounts of reasoning — and without a per-conversation answer the only way to have both would be two profiles pointing at one model.

**Unset is the default, and unset sends nothing.** A request from anybody who has not asked for a level is byte for byte what it was before this existed. `off` is not a level either: the wires' own vocabulary is low/medium/high, servers disagree about whether there is a word for "none", and asking for one nobody agrees on is a worse answer than saying nothing.

**One word, several wires, and they do not agree.** That is reported rather than smoothed over — a setting that claims three positions on a wire with two lies twice, once when it is set and again when somebody tries to tell "high" from "medium" by watching what it costs. `/effort` says what your level reaches on your model, every time it is asked.

| Provider | What is sent | What the levels mean |
|---|---|---|
| OpenAI-compatible | `reasoning_effort` | The three levels, as the server understands them. A server that does not support reasoning ignores the field and nothing changes — which is what makes this safe on a local muse or gemma. |
| Anthropic API, newest Claude families | extended thinking, `adaptive` | The model sizes its own reasoning, so low, medium and high all reach the same switch. Here the setting is **on or off, not a dial**. |
| Anthropic API, older Claude models | extended thinking, with a budget | Three levels, three token budgets. |
| Bedrock | extended thinking, through `additionalModelRequestFields` | The same shapes, chosen the same way from the model id. Merged with the million-token beta rather than overwriting it — that field is one document and the SDK gives no way to read one back, so both features have to be written at once or the last one wins. |

**Two things happen on their own, because otherwise they are a 400 rather than a worse answer.**

* **The budget is fitted to `max_tokens`.** On a model that takes a budget, that budget is spent out of the output cap: one larger than the cap is refused, and one equal to it leaves nothing to answer with. It is shrunk to fit, keeping 1024 tokens back for the answer, and a cap too small for any useful reasoning asks for none at all rather than for a reasoning model with no room to reason. This is not an exotic pairing — `config.example.json` puts `max_tokens: 8192` on the profiles these levels apply to, and `high` is 16384.
* **Temperature is dropped.** The API fixes the temperature while a model is reasoning and refuses a request that also sets one. Dropping it is what leaves both usable; the alternative is that a profile with a temperature on it cannot ask for reasoning at all.

**Reasoning is shown while it happens and never kept.** In the Web UI it is a muted block of its own, separate from the answer, because it is the working and not the conclusion; in the TUI the busy indicator says `thinking` instead of `working`. None of it is written to the session log: the API does not want it back on a later turn, and a transcript that kept it would double the scrolling for something nobody reads twice. A reload does not bring it back, which is the honest consequence.

### Scheduled tasks

**Just say when.** An ordinary message that asks for something later books it instead of doing it now:

```
내일 아침에 테스트 돌려줘
in 30 minutes check whether the build is still green
금요일 저녁에 배포 준비해줘
```

The model recognises the request and separates the time from the work; **localcode reads the time**, with the same parser the command and the button use, and the confirmation names the moment it read rather than the words the model passed. That division is the point: a local model asked for a timestamp gets the year wrong occasionally, and a scheduled task is exactly where an occasional wrong answer stays invisible until the day it matters.

Booking asks first, like any other side effect — it is a turn that will run later, unattended, with tools of its own. `/permission-skip-tools` silences that question along with the rest.

You can also book one by hand. `/schedule <when> <what to do>`:

```
/schedule 30분 뒤 run the tests and report the failures
/schedule 내일 아침 summarize yesterday's commits
/schedule in 2 hours check whether the build is still green
/schedule 2026-09-01 14:30 draft the release notes
```

**It runs only while localcode is running.** There is no service, no launchd job, and nothing wakes the machine. If localcode is closed or the machine is asleep at that moment, the task is reported as **missed** — in those words, with the time it was booked for — and is **not** run late. Running "summarize yesterday's commits" at four in the afternoon because the machine was asleep at nine would be doing something nobody asked for at a moment they did not choose. A booking whose moment has not yet come is re-armed when localcode starts again.

**The Schedule button** (⏰ under the prompt box, Web UI and the desktop window) asks for the two separately: a **When** field and a **What to do** field. That is the difference from typing the command — at a prompt the split between the moment and the request has to be found in one string, and in two fields there is nothing to find. The When field shows what the time was read as while you type it, before anything is booked.

**The time is read by localcode, not by the model.**

| Shape | Examples |
|---|---|
| Relative | `30분 뒤`, `3시간 후에`, `in 2 hours`, `in half an hour`, `한 시간 뒤` |
| Clock | `오후 3시`, `5시까지`, `at 3pm`, `18:00` |
| Named | `내일 아침`, `모레 점심`, `저녁 7시`, `tomorrow 9am`, `tonight`, `at noon` |
| Weekday | `금요일 저녁`, `다음주 월요일`, `next monday`, `friday evening` |
| Absolute | `2026-09-01 14:30`, `2026-09-01` |

The particle belongs to the time, not to the request: `내일 아침에 테스트 돌려줘` books tomorrow at nine and sends `테스트 돌려줘`, and `에러 로그 확인` is not mistaken for a particle because a word that merely starts the same way is not one.

**A bare hour means the nearest one.** `5시` said at half past four is the five half an hour away, not the one nineteen hours away that happens to be a morning. A named day pins the date, so `내일 9시` is nine that morning; `오전 9시` and `18:00` are unambiguous and are left alone.

**What it refuses, and why it says which.** A refusal names the kind of no, because "could not read a time" sends you looking for a spelling mistake when the answer is something else:

| Input | Answer |
|---|---|
| `나중에`, `곧`, `later today` | Not a time. Nobody has picked a moment, and a scheduler must not pick one for you. |
| `매일 9시`, `1시간마다`, `every day at 9am` | Repeats are not supported — one prompt, one moment. |
| Anything else it cannot read | Refused with the examples above. |

The reply echoes what was read, in full, because a misread time is worth catching before the work is booked rather than after it fails to happen.

**Where it runs.** In a session of its own, under the conversation's workspace and its four permission switches — the same shape a background task runs in, which is most of why this is small. The reply says which directory before you commit to it.

**Nobody is watching, so it cannot wait forever.** A permission request has no timeout; a scheduled turn that hits one waits five minutes (long enough for someone at the desk to answer the prompt, which appears in the conversation that booked the work) and then stops, saying which tool it wanted and why it did not run. Decide the posture in advance with `/permission-skip-tools`, `/read-outside` and `/write-outside` if the task needs to act without asking.

| Where | What you get |
|---|---|
| ⏰ Schedule button | Two fields — when, and what to do — with the time echoed back as you type it. |
| Right panel | One row per booked task, with a light: **blinking green** while it waits, **solid green** once there is an answer nobody has read, **grey** once it has been read. Click the row to read the result; **rename** names it and **delete** removes it along with the run's transcript, the same two buttons a session row carries. |
| `/show-scheduled-task` | The same list as text, for the TUI. |
| `/schedule cancel <id>` | Removes one. Ids are short and belong to the conversation: `s1`, `s2`. |
| `/schedule rename <id> <name>` | Labels one; an empty name clears it. |

**Naming a task.** A booked prompt is a paragraph and a row is one truncated line, so two rows that both start "run the tests and report the fail…" are two rows nobody can tell apart at the moment they need to. A name is a label for the row — cosmetic, like a session's title, and nothing resolves by it. The prompt stays visible underneath, so naming a task adds a label rather than hiding what it will actually run.

One prompt, one moment: there are no repeats. A repeating job needs a failure policy and a stop condition of its own, and shipping it without them is how an expired credential becomes five hundred identical failed sessions.

### Background tasks

Start another agent from a parent session and track its progress. The event types are the same as the `Task` tool, `task.spawned` and `task.status`, but this is **asynchronous**. The caller does not wait.

This is API only for now. Neither client has a "run in background" button, only the sidebar that shows status.

```bash
curl -X POST http://127.0.0.1:4096/api/sessions/<parent-id>/tasks \
  -d '{"agent":"explore","prompt":"find every TODO under src/"}'
```

`task.spawned` and `task.status` events, carrying running, completed, failed, or cancelled, flow into the parent session's stream and appear live in the Web UI sidebar and the TUI transcript. Background concurrency is capped by `max_concurrent_tasks`; a task cancelled while it is still queued reaches a terminal status like any other, and collecting it returns the cancellation rather than waiting.

#### Watching one

A task is a session, so it has a full conversation behind those three words. Click its row in the Web UI's right panel to open it. The window shows the whole of what is happening in it:

| What | Shown as |
|---|---|
| Tool calls | A line when the call starts and the same line completed with its result, `✓` or `✗`, and the output; click it for the full arguments. |
| A permission it is blocked on | `⏸ waiting for permission`, naming the tool and what it wants to do. Answer it in the session that spawned the task, which is where the prompt appears. |
| Work it delegates itself | A line per sub-task it spawns and per status that comes back. |
| The end | `— finished —` or `— cancelled —`, and any call the turn stopped mid-flight is marked as not finished rather than left spinning. |
| Errors and compaction | A line each. |

A task still running offers **Stop this task**. One that has finished offers **Delete this task** instead: its work is over and its transcript is all that is left, so this is how you are rid of a row that has served its purpose. Deleting removes the conversation and the row together, and the row stays gone across a reload, because the removal is recorded on the parent session's own log where the row comes from.

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

### Checking for updates

The settings window (⚙ under the prompt) has one section, **Updates**,
with two buttons, and neither does anything until it is clicked.

| Button | What it does |
| --- | --- |
| Check for updates | Asks GitHub for the latest release of `dennis2lee/localcode` and compares it against this build — or asks `update_url` instead, when config.json sets one. See [Updating from somewhere other than GitHub](#updating-from-somewhere-other-than-github). |
| Download and install | Downloads the file for this platform, verifies it, and either installs it and restarts localcode or starts the platform's installer — see [What installing does](#checking-for-updates) below. Appears only when there is a newer release *and* this localcode can install it. |

Nothing checks on a timer or on opening the panel. A check is an outbound
request that tells GitHub which version this machine is running, which is
a thing to ask for rather than assume, and an update replaces the program
while someone is using it.

#### Updating from somewhere other than GitHub

`update_url` in config.json replaces GitHub entirely: one **https** address at which the current installers are published, side by side, named the way localcode names them.

```json
{ "update_url": "https://bitbucket.org/acme/localcode-builds/downloads/" }
```

It exists for a machine that cannot reach github.com, or an organisation that would rather its own build were the one installed: an internal Bitbucket, an artifact server, a plain file share.

**The version is read out of the filenames**, because on a directory of files that is the only place it is written down. Publish the installers under the names localcode publishes them under — `localcode-1.2.3-darwin-universal.tar.gz`, `localcode-1.2.3-windows-amd64.msi`, `localcode-1.2.3-linux-amd64.deb` and the rest — and drop the new build in. The page is scanned for those names whatever it is: an Apache or nginx directory index, a Bitbucket downloads listing, an artifact server's JSON, or a direct link to a single file. If several versions are there, the highest wins, so a leftover from last month cannot offer a downgrade.

| Situation | What you get |
|---|---|
| The URL cannot be reached | `could not reach update_url <url>: ...`, naming the address |
| It answers 404, 403, ... | `update_url <url> answered 404 Not Found` |
| Nothing there looks like an installer | A message saying so, with an example filename |
| It is not https | Refused, with the reason |

**https only.** What this URL names is a file localcode will run as an installer, and a file share usually publishes nothing beside it to check the download against — so the connection is the only thing that says the file came from the host you meant. An `http://` URL is refused rather than allowed with a warning.

**Verifying the download.** GitHub publishes a SHA-256 for every asset and localcode checks it. A file share does not, so if a `<filename>.sha256` sits beside the installer (the shape `sha256sum` writes) it is used; if nothing is published, the install goes ahead and the panel says the download **could not be verified**, which is a true thing about a file that has just been run.

The panel names the source when it is not the public releases page, so an internal build is never reported as though it came from GitHub.

**Where the install button appears.** Where the daemon and the person
clicking share a machine: the desktop window, and a daemon listening on
loopback — which is the ordinary `localcode` run, TUI and Web UI both.
Over `--server`, or a daemon deliberately exposed with something like
`--listen 0.0.0.0:4096`, the check still works and the install does not:
it would replace the program on the *server*, at the request of a browser
somewhere else. The panel says so and offers the release page instead. It
is the same rule the folder picker follows.

**What is downloaded.** The file that matches how localcode was installed:

| Platform | Asset |
| --- | --- |
| Windows amd64 | The `.msi`, which upgrades in place |
| Windows arm64 | The `.zip` (no installer; the panel says where the file is) |
| macOS, from `LocalCode.app` | `LocalCode-x.y.z-darwin-universal-app.tar.gz` |
| macOS, command line | `localcode-x.y.z-darwin-universal.tar.gz` |
| Linux, installed from the `.deb` (`/usr/bin/localcode`) | `localcode-x.y.z-linux-<arch>.deb`, with the `apt install` line to run |
| Linux, installed under your home directory (`~/.local/bin`, or any tarball copy) | `localcode-x.y.z-linux-<arch>.tar.gz`, installed for you |

It is checked against the SHA-256 GitHub records for the asset before
anything is run, and refused if it does not match or if the size is
wrong — a connection dropped at 90% otherwise produces an installer that
opens, fails halfway, and leaves a broken install. Downloads go to the
user cache directory (`%LOCALAPPDATA%\localcode\updates` on Windows).

**What installing does.** It depends on who owns the copy being replaced.

*An install nobody else manages* — a binary in `~/.local/bin`, or
anywhere else you can write, which is what
[the no-root install](INSTALL.md#install-on-linux) produces — is replaced
by localcode itself. The new binary is unpacked beside the old one, asked
for its version to prove it runs on this machine at all, and renamed into
place. Rename is atomic, so the localcode running at that moment keeps
the file it started from and nothing is ever half-written.

**It then restarts itself onto the new binary**, which is the point: the
program in memory is still the old one until something replaces it, and
until v0.53.0 nothing did — the panel said "restart localcode to run the
new version" and left it there, so an update that had worked perfectly
showed the same version in the header afterwards and read as one that had
not happened. The restart is an `exec`, not a new process beside this one,
so it keeps the process id, the terminal, the standard streams and the
arguments it was started with: a TUI comes back in the terminal it was in
with the flags it was given, and the Web UI's browser tab reconnects on
its own once the new daemon has the address. That is the whole update on a
machine where you have no root.

The exception is a daemon you reached from somewhere else. It installs and
does not restart — that is not a browser's to order — and the panel says
to restart it instead.

*An install a package manager owns* is left to that package manager. On
Windows localcode runs `msiexec /i` on the downloaded package: Windows
asks for elevation, shows the installer, and localcode has to close for
its files to be replaced — the window offers to close itself a few
seconds after the installer starts. On Linux a `.deb` is downloaded and
verified and the panel gives you the one command that installs it
(`sudo apt install <path>`): installing needs root, and localcode does
not ask for a password or drive a package manager on your behalf. The
same for a copy in `/usr/bin` that only root can write, `LocalCode.app`
(which is signed as a whole bundle, not as the binary inside it), and a
Windows zip. In each of those the panel says where the file is *and why
it stopped there*, since "unpack it yourself" with no reason reads like
localcode never tried.

A build that is not a release — one from a working tree, which reports
its version as `dev` — is never offered an update, since there is no
version to compare and every release would look both newer and older.

## Known limitations

* Opening a session shows the end of it, not all of it. A long conversation loads its last few hundred events rather than replaying the whole log, so switching sessions costs the same whatever their length; there is no "load earlier" control yet, so the beginning of a very long conversation is only in its `.jsonl`.

* If an MCP server dies and the reconnect also fails, for example because the executable is gone, its tools return an error on every later call until the daemon restarts.
* There is no auth token. Anyone who can reach the `--listen` address gets the entire API, shell execution included. Expose it only over loopback plus an SSH tunnel.
* On Windows, shell execution resolves to `sh` on PATH, then Git for Windows' `bash.exe` at its usual install paths, then `cmd /c`. Under the `cmd` fallback, bash-only syntax does not work; the bash tool tells the model so in its description. Installing Git for Windows gives the full POSIX behavior.
* There is no desktop window on Linux. It links a native webview through CGo, which on Linux means WebKitGTK and a build per distribution; the daemon, the TUI, and the Web UI in a browser all work there. The `.deb` and the Linux tarballs carry the `localcode` binary only.
* `/compact` can still overlap a running turn on the same session. Ordinary messages are serialized (the daemon refuses a second turn, and the client queues and retries it), but compaction does not go through that path.
