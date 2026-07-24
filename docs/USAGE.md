# Usage

## Contents

| Part | Sections |
|---|---|
| [1. Getting started](#part-1-getting-started) | [Run modes](#run-modes), [Remote daemon over an SSH tunnel](#remote-daemon-over-an-ssh-tunnel) |
| [2. Configuration](#part-2-configuration) | [Config file (config.json)](#config-file-configjson), [Managing MCP servers](#managing-mcp-servers-with-localcode-mcp), [Permission rules](#fine-grained-permission-rules), [Hooks](#hooks), [Authenticating with /login](#authenticating-with-login) |
| [3. Project context](#part-3-project-context) | [Skills](#skills), [AGENTS.md](#agentsmd-project-rules), [Auto memory](#auto-memory) |
| [4. Commands and screen controls](#part-4-commands-and-screen-controls) | [Screen controls](#screen-controls), [Running a skill](#running-a-skill), [/init](#init), [Custom commands](#custom-commands), [/tasks](#tasks), [/memory](#memory), [/config](#config), [/compact](#compact), [/usage](#usage), [Other local commands](#other-local-commands) |
| [5. Sessions](#part-5-sessions) | [Switching sessions](#switching-sessions), [Rename and delete](#renaming-and-deleting-sessions), [Context window](#context-window-management), [Session logs](#session-logs), [Restart recovery](#daemon-restart-and-session-recovery) |
| [6. Web UI](#part-6-web-ui) | [Right panel](#right-panel), [Drag and drop attach](#drag-and-drop-file-attach), [Status bar](#status-bar-under-the-prompt) |
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
| `localcode-gui --gui` | A native desktop window instead of the TUI. Experimental, opt in, built with `-tags gui`. See [Desktop window](#desktop-window-experimental). |

### Desktop window (experimental)

Instead of the TUI or a browser, localcode can open its Web UI in a native desktop window, so it is one app to launch rather than a server to start and a browser tab to open.

```bash
./localcode-gui --gui
```

It starts the daemon in-process on a private loopback port and shows the same Web UI in an OS native window (WKWebView on macOS, WebView2 on Windows). Nothing is exposed off the machine and there is no fixed port to collide with.

This is opt in, because the window links a native webview through CGo, which cannot be cross compiled the way the pure Go daemon and TUI are. It is built per OS:

* macOS: `make dist-mac-gui` produces a double-clickable `LocalCode.app` (universal, arm64 + amd64). `make gui-mac` builds just the bare `localcode-gui` binary. macOS always has WKWebView.
* Windows: built in CI by `.github/workflows/gui-windows.yml` on a Windows runner (CGo cannot cross compile from macOS), which uploads `localcode-gui.exe` as an artifact. The Windows MSI (`make dist-msi VERSION=x.y.z GUI_EXE=path/to/localcode-gui.exe`) installs it alongside the TUI binary with its own Start Menu shortcut ("LocalCode (Desktop)"), and runs Microsoft's WebView2 Evergreen Bootstrapper silently during install so the runtime is there even on older Windows 10 systems that do not ship it already. That install step is skipped quietly (not a failed install) if there is no network access at install time or the runtime is already present.

The macOS `.app` is unsigned, so Gatekeeper needs a right click then Open the first time, same as the TUI app.

A build made without `-tags gui` still accepts `--gui` but returns an error saying so, rather than failing to build. The daemon, TUI, and browser modes are unchanged.

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
    "cheap":    { "provider": "local", "model": "qwen3-30b-a3b", "max_tokens": 4096 }
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
| `profiles` | A named provider and model pairing. `max_tokens` and `temperature` are optional. |
| `agents` | Maps an agent name to a profile. `--agent` resolves through this. An unknown name falls back to `default_profile`. |
| `max_concurrent_tasks` | Caps how many background tasks run at once |
| `mcp_servers` | Same shape as Claude Code's `.mcp.json`, so existing entries copy over directly |

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

* Each server is launched over stdio, and its tools appear as `mcp__<server>__<tool>`.
* **MCP tools always require permission confirmation.** A server claiming its own tool is read only through annotations is not trusted.
* If one server fails to connect, from a bad command or a crash, only that server is skipped. The rest register normally and the daemon still starts, logging a warning.
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

# copy an existing .mcp.json entry across as raw JSON
localcode mcp add-json weather '{"command":"node","args":["weather-server.js"],"env":{"API_KEY":"xyz"}}'

localcode mcp list          # shows whether each came from global or project
localcode mcp get github    # full command, args, and env for one server
localcode mcp remove github # unregister
```

| Detail | Behavior |
|---|---|
| `-e`, `--env KEY=VALUE` | Repeatable |
| `-s`, `--scope` | `global`, the default, or `project` |
| `--` | Everything after it is the command and its arguments. Always use it so flags meant for the server, such as `-y`, do not get read as flags for `localcode mcp` itself. |
| `remove` without `-s` | If the same name exists in both global and project, nothing is deleted and you get an ambiguity error. Say which with `-s global` or `-s project`. |

These commands only read and write the `mcp_servers` map, so editing config.json by hand works exactly the same. The CLI is a convenience.

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

The same convention opencode and Claude Code use. Put an `AGENTS.md` at the project root, or any parent directory up to the git repository root, and it is appended to the system prompt at startup.

`CLAUDE.md` is recognized as a fallback in the same places, so an existing Claude Code file is reused as is.

A `~/.localcode/AGENTS.md`, falling back to `~/.claude/CLAUDE.md`, applies your personal rules to every project. Project rules and global rules are combined, not overwritten.

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
| Quit the TUI | **Ctrl+C**, or type `exit` or `:q` |

Other behavior:

* The input box grows as you type, up to about 10 lines, then scrolls internally.
* A permission prompt appears whenever the model wants `write_file`, `edit`, a non-git `bash` command, or an MCP tool. Any client attached to the session can answer it, and answering closes the prompt on every other client.
* The TUI draws a rule above and below the input box, with a status line directly underneath showing `agent: <name>  ·  model: <model id>`. Switching with Tab updates only that line and adds nothing to the transcript.
* The TUI places the real terminal cursor at the insertion point inside the prompt box, so IME composition for Korean, Japanese, and Chinese renders in the box while you type rather than below it.
* **Running work shows below the prompt box, not in the conversation.** While a turn is in flight the TUI animates a line naming what it is doing (the running tool's name, or `working`), the queue depth, and how many background tasks are going. It disappears the moment the turn ends. The Web UI shows the same information in its status bar. Tool starts and finishes no longer write `[tool] ...` lines into the transcript.

**Esc cancels whatever is running.** Press it while the model is answering (the status line says "esc to cancel") to stop that turn immediately. Cancelling also clears anything waiting in the prompt queue — the point of cancelling is to stop, so letting a queued message fire right after would defeat it. A `[cancelled]` line marks where it stopped; nothing about it is treated as an error. Pressing Esc with nothing running does nothing.

**Up and Down recall previous prompts**, the way a shell's history does. Up walks back through what you have already sent, newest first, and Down walks forward again. Stepping forward past the newest entry restores whatever you had half-typed before you started recalling, so reaching for history never costs you a draft.

Recall only kicks in at the edges of the prompt box: the cursor has to already be on the first line before Up reaches for history, and on the last line before Down does. Inside a multi-line prompt the arrows just move the cursor as usual. Repeating the same message twice in a row stores it once.

The list lives in the client, in memory, and is not part of the session. It starts empty each time you launch the TUI, and the Web UI clears it when you switch sessions.

**Messages sent while a turn is still running are queued.** This covers the whole turn, tool execution included, not just while text is streaming. The transcript shows `[queued] <text>` immediately and the status line shows `(N queued)`. The first queued message sends automatically the moment the turn actually ends, and several stack up and go out in order. If a send does slip through while the daemon is busy (for example, a turn started from another client on the same session), it is queued and retried rather than shown as an error.

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

A running task also appears in the indicator below the prompt box, and in the Web UI's right panel.

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
* **Web UI**: the right panel always shows the session list. Switching clears the screen and replays that session's whole event log, including user messages, model replies, and tool runs.

`GET /api/sessions` returns the same list. Background tasks are `visible:false` and do not appear there. Use `GET /api/sessions/{id}/tasks` for those.

### Renaming and deleting sessions

Sessions are identified and resumed by ID, so a `title` is purely for display.

| Action | API | Also available from |
|---|---|---|
| Rename | `POST /api/sessions/{id}/rename` with `{"title": "..."}` | The rename button in the Web UI right panel |
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

If summarization fails, for example on a network error, compaction is skipped and the original history is used. It never blocks the conversation.

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

### Right panel

Three sections, top to bottom:

| Section | Contents |
|---|---|
| Sessions | Every session on the daemon with its title or ID, agent, and creation time. Each has switch, rename, and delete. Plus **+ new session** and **delete all**, both of which confirm first. |
| Background tasks | Live status of subtasks started by the `Task` tool or the background task API |
| MCP servers | Names of currently connected servers, from `GET /api/mcp-servers`. Configured servers that failed to connect are absent here and logged as a daemon warning. |

### Drag and drop file attach

Dropping a file on the input box uploads it through `POST /api/sessions/{id}/uploads` to `~/.localcode/uploads/<session id>/<filename>`, and inserts the absolute path into the input box.

The file contents are not shown to the model. The model reads that path itself with `read_file` or `bash` when it needs to. This works well for text files. For images and other binaries the model cannot read them as text, so only the path is useful.

### Status bar under the prompt

One line directly below the input box:

| Element | Behavior |
|---|---|
| Agent and model | Which agent answers the next message, and the model its profile resolves to |
| Context use | Yellow past 70%, red past 90% |
| TPS | Shown when `show_tps` is on |
| Activity dot | Pulses yellow while talking to the model, flashes green briefly when the reply finishes, then goes dark |

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
| `enabled` | The starting value. `/config auto_delegate on\|off` changes it while running. |
| `agent` | Which entry in `agents` handles delegated prompts. Must exist, or startup fails with an error. |
| `match` | Globs (`*` for any run of characters, `?` for one) tried case insensitively against the whole trimmed prompt. Any one matching delegates the prompt. |

Behavior:

* The delegated turn shows `[delegated to <agent>]` in the transcript, so a cheaper model answering is visible rather than silent.
* The prompt and the sub agent's answer both enter the main session's history, so the main model has the exchange as context on its next turn even though it never ran for this one.
* Commands are never delegated. `/`-prefixed commands, skills, and `exit`/`:q` are all handled before the delegation check, so a pattern of `*` still leaves them working.
* An empty `match` list delegates nothing. A half written config is inert rather than quietly routing everything to the cheap model.
* Delegation never recurses. A session that already has a parent (any sub agent session) does not delegate again, and an agent never delegates to itself.
* Turning it on with no `auto_delegate` block in config.json tells you so rather than silently doing nothing.

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

## Known limitations

* If an MCP server dies and the reconnect also fails, for example because the executable is gone, its tools return an error on every later call until the daemon restarts.
* There is no auth token. Anyone who can reach the `--listen` address gets the entire API, shell execution included. Expose it only over loopback plus an SSH tunnel.
* On Windows, shell execution resolves to `sh` on PATH, then Git for Windows' `bash.exe` at its usual install paths, then `cmd /c`. Under the `cmd` fallback, bash-only syntax does not work; the bash tool tells the model so in its description. Installing Git for Windows gives the full POSIX behavior.
* `/compact` can still overlap a running turn on the same session. Ordinary messages are serialized (the daemon refuses a second turn, and the client queues and retries it), but compaction does not go through that path.
