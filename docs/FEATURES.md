# localcode, feature by feature

The full reference: every capability localcode ships, one line each, grouped by area. The [README](../README.md) is the short version; this is the long one. Each line here is a shipped behaviour covered by the repository's test gate, and [USAGE.md](USAGE.md) is where the detail lives.

## What is not standard in a coding agent

| Capability | In one line |
|---|---|
| Debate | Review by up to three agents, each with its own model and session. |
| Scheduled tasks | One-time work parsed locally from Korean or English prompts. |
| Orchestrate | Structured multi-stage plans validated before model execution. |
| Cross-session reference | `#S2` resolves to an optional session read tool call. Session content is not inserted into the user message. |
| Provider mixing | Hosted and local models in the same conversation. |
| Prompt inventory | `/context` reports the next request by source and trust class without a model call. |
| Trace | JSON Lines records for model calls, tool calls, delegation, fallback, and compaction. One trace ID per turn. |
| Model-invocable commands | Optional slash command execution by the model. Explicit allowlist with no wildcard. |
| Three front ends | TUI, browser, and native clients for the same daemon. |
| Workspace boundary | Separate permission checks for physical paths outside the session workspace. |

## Features

### Models and providers

| Area | What you get |
|---|---|
| Providers | Bedrock, Anthropic API, and OpenAI-compatible providers in one config file. AWS configuration loads only on first Bedrock use. Unused Bedrock entries do not block local startup. |
| Auth | `localcode login bedrock` (AWS SSO device flow, no AWS CLI needed), `localcode login anthropic` (stores an API key). |
| Model switching | Hosted and local models in one conversation. Tab or `/agent` changes the model, prompt, and tool scope for the next message. `/model <profile>` changes only the model and keeps the agent's prompt, tools, and permissions. History remains unchanged. Each switch is an event. |
| Effort | `off`, `low`, `medium`, `high`, or `xhigh` per model, kept per model across switches. Only the levels a model tells apart are offered. See [USAGE.md](USAGE.md#effort). |
| Sampling | `temperature`, `top_p`, and `top_k` on a profile. All three reach OpenAI-compatible, Anthropic, and Bedrock (`top_k` through `additionalModelRequestFields`); all three are dropped while a Claude model is reasoning. Out-of-range values are refused when the config loads. |
| Reasoning display | Provider reasoning fields plus `<think>` splitting, never sent back to the model. muse reasoning folds and persists across reloads. See [USAGE.md](USAGE.md#per-model-formatting-notes). |
| Context window | Server supplied values from `GET /v1/models` or llama.cpp `/props`. Length errors cause summarization and trimming until acceptance. |
| Prompt cache | Breakpoints after the stable prefix (tool schemas and system prompt) and conversation tail. Anthropic uses `cache_control`; Bedrock uses `cachePoint`; providers with server-side prefix caching receive neither. |
| Fallback | Two endpoint retries after 1 and 2 seconds, then the profile `fallback`. A 401 or unknown model ID skips the delay. localcode derives a new request for the fallback model and records the switch. |
| Model specific handling | Additional prompt lines for required model families, automatic continuation for models that stop during tasks, `/keep-going off` to disable it, a folding reasoning block for muse, and LaTeX unwrapping for Gemma. The settings window's Muse tab holds the muse switches. |
| Repeated tool calls | `/repeat-limit` ends a turn after N steps that only repeat tool calls already made, naming them. Off by default; `on` is 3. Some local models think by re-reading, which the guard would cut short. |
| LLM doctor | `/llm-doctor` checks a local muse or gemma against canaries and a stored baseline. See [USAGE.md](USAGE.md#llm-doctor). |

### Agents, delegation, orchestration

| Area | What you get |
|---|---|
| Multiple agents | Per role model, prompt, and tool scope. Delegation through `Task`. Bare `/model` reports the model in force and every profile this config can reach; `/model <profile>` changes only the model, while `/model <agent>` switches agent. |
| Automatic delegation | Matching prompts can use a lower cost agent without changing the main session cache. Configuration in the panel below the prompt bar, stored in config.json. |
| Smart Agent | Disabled by default. Six specialists: `explore`, `librarian`, `oracle`, `plan`, `implement`, and `verify`. Separate session and context for each specialist. Models resolve from existing profiles or explicit `smart-quick`, `smart-balanced`, and `smart-deep` profiles. Specialist tool allowlists exclude delegation. |
| Debate | Up to three concurrent reviewers on separate models, unanimous approval, round limit 3 (max 10). Via plain language, the debate button, or `/debate`. See [USAGE.md](USAGE.md#debate). |
| Orchestrate | Disabled by default. Structured plans validated before execution: step, fanout, and barrier stages with `repeat_until` loops. Limits: 8 stages, 32 agent turns, 4 at a time. See [USAGE.md](USAGE.md#orchestration). |
| Scheduled tasks | One-time work via plain language, `/schedule`, or the schedule button, parsed locally in Korean or English. Missed times report as missed. See [USAGE.md](USAGE.md#scheduled-tasks). |
| Model-invocable commands | Disabled by default, explicit allowlist with no wildcard. Commands run as separate turns. See [USAGE.md](USAGE.md#model-invocable). |
| Concurrency | Optional provider limit through `max_concurrent_tasks`, acquired before the daemon limit. Work waiting for a local GPU does not consume a daemon slot. |

### Sessions

| Area | What you get |
|---|---|
| Event log | Append-only session events. Clients resume from a `since` sequence number without gaps or duplicates. A client outside the retained buffer receives an error and replays from the log. |
| Workspace | Per session directory. Relative paths and bash commands use that directory, which permits multiple projects on one daemon. Every turn derives the current directory again for the system prompt. |
| Cross-session reference | `#S2` resolves to a session read tool call, never spliced into the prompt. References are not transitive. See [USAGE.md](USAGE.md#referring-to-another-conversation-with-name). |
| Archive and retrieve | Archived conversations stay readable on disk without slowing startup. Booked work disarms while archived. See [USAGE.md](USAGE.md#archiving-a-conversation). |
| Undo a turn | `/rewind` removes the last exchange and restores files `write_file` or `edit` changed. `/redo` puts it back. See [USAGE.md](USAGE.md#rewind). |
| Start fresh | `/clear` appends a history barrier. Earlier events remain visible, persistent, and excluded from later model context. |
| Compaction | `/compact [instructions]` on demand, automatic past a threshold (`/auto-compact 70`, 50% by default), `/usage` for cumulative tokens per model, with `/usage all\|today\|week\|month` counting every conversation including archived ones. |
| Steering | Messages entered during a turn are delivered at the next tool call in entry order. Esc stops the process tree, including a running shell command. |
| Restart safety | Session list, conversation context, and `/usage` totals restore from disk. A second terminal in the same project attaches to the running daemon; one in another project starts its own. |

### Tools, permissions, guards

| Area | What you get |
|---|---|
| Permissions | Allow, deny, and ask rules, plus prompt choices for one use, the session, or always. Each bash command is checked in written and unquoted forms. Conversation switches: `/permission-skip-all`, `/permission-skip-tools`, `/read-outside`, and `/write-outside`. |
| Workspace boundary | Separate permission checks for paths outside the session workspace. Bash has its own permissions. See [USAGE.md](USAGE.md#leaving-the-project). |
| Credential guard | Smart Agent denies read, write, and edit access to `.env`, `*.pem`, `id_rsa`, `~/.ssh`, `~/.aws/credentials`, `.netrc`, and related paths. Skip switches cannot override deny rules. Explicit config.json tool rules can permit required project access. |
| Instructions and data | The system prompt labels instruction sources and data sources. MCP output includes a marker with the server name. Labels do not replace permission enforcement. Delegated work cannot change child permission switches, including a task that starts with `/permission-skip-all on`. |
| Search limits | `grep` reports files omitted because of the 200 match limit, lines over 1 MB, or read failures, in both modes. Reports up to three paths. With Smart Agent on, a single file contributes at most 30 matches. |
| File tools | With Smart Agent on: paged `read_file`, and `edit` failures diagnose whitespace, punctuation, and near matches. See [USAGE.md](USAGE.md#file-and-search-behavior). |
| Tool name repair | Unambiguous decorated names resolve only within the current agent tool list. The result reports the accepted spelling. Tool restrictions remain effective. |
| Hooks | Tool and lifecycle hooks with per-hook timeout and `fail_closed`. See [USAGE.md](USAGE.md#hooks). |
| MCP | `stdio`, streamable `http`, and `sse` transports. `localcode mcp add/list/get/remove` without printing secrets. See [USAGE.md](USAGE.md#managing-mcp-servers-with-localcode-mcp). |
| Egress | `network.egress` bounds localcode's own outbound connections when asked for. Shell and subprocess servers are out of scope. See [USAGE.md](USAGE.md#where-localcode-may-connect). |
| Debug log | `/debug-log` toggles writing every model request and response, byte for byte and binary included, to one file per prompt in the workspace. Credentials are redacted; nothing else is. Off at every start. |
| Trace | JSON Lines events per turn, inherited by child agents. See [USAGE.md](USAGE.md#the-turn-log). |
| Prompt inventory | `/context` reports the next request by source and trust class without a model call. See [USAGE.md](USAGE.md#context). |

### Interfaces

| Area | What you get |
|---|---|
| Web UI | Session panel with drag ordering, status colors, file drag and drop, Markdown, and task and MCP panels. See [USAGE.md](USAGE.md#part-6-web-ui). |
| TUI | Shared daemon and commands. `/session` switches conversations, `/agent` switches agents, `/model` changes the model, and `/show-scheduled-task` lists scheduled work. |
| Readouts | `/status` names every MCP server with its connection state and last error, then skills, commands, agents, and workspace. `/debug` prints one unstyled block for bug reports. `/workspace` shows the conversation directory and moves it. |
| Desktop window | Experimental native Web UI without a separate browser or visible server. Optional `-tags gui` build. `LocalCode.app` on macOS and `localcode-gui.exe` in the Windows MSI. On Windows, links open in the system's default browser. See [USAGE.md](USAGE.md#desktop-window-experimental). |
| Prompt box | Unsent drafts per conversation, `/name` completion, Alt+Up/Down turn jumps, IME composition. See [USAGE.md](USAGE.md#screen-controls). |
| Running work | A transcript entry per tool call with arguments and output. `/tasks` inspects background work. See [USAGE.md](USAGE.md#background-tasks). |
| Stable scroll | Manual upward scrolling disables automatic movement in both clients and task windows. User turns have a full height left border. |

### Running and packaging

| Area | What you get |
|---|---|
| Single request | `localcode run` answers in process and exits. Text, JSON, and stream-JSON output. See [USAGE.md](USAGE.md#one-prompt-no-window-localcode-run). |
| Config | `config.example.json` documents every key and includes a tested orchestration example. User config supports JSONC. localcode preserves comments during config writes. |
| Environment values | String fields support `{env:NAME}` and `{env:NAME:-fallback}` at load time, including API keys, `base_url`, model IDs, and MCP environments. Missing variables produce an error with the variable and field names. Files retain the placeholder. |
| Project context | Workspace rules from `AGENTS.md`/`CLAUDE.md` with `@path` imports. Skills and commands from project and home roots. `/init` drafts, auto memory persists. See [USAGE.md](USAGE.md#part-3-project-context). |
| Updating | Newer releases install at startup or on `/update`, SHA-256 verified. `auto_update: false` turns it off. See [USAGE.md](USAGE.md#checking-for-updates). |
| Windows | Shell selection order: `sh`, Git for Windows `bash.exe`, then `cmd /c`. Missing `python` or `python3`, including Microsoft Store stubs, produces a `winget install` command through the normal permission gate. Detection uses PATH lookup and does not depend on localized shell error text. |
| Linux | User install of one static binary in `~/.local/bin`, with no writes outside `$HOME`. System `.deb` packages for Ubuntu and Debian. Portable tarballs for other distributions. `CGO_ENABLED=0` and no `Depends:` entry. Linux supports the daemon, TUI, and Web UI, but not the desktop window. |

## Documentation

| Document | Contents |
|---|---|
| [INSTALL.md](INSTALL.md) | Release installation, source builds, and distribution packages |
| [USAGE.md](USAGE.md) | config.json, commands, screen controls, session and agent management |
| [MODELS.md](MODELS.md) | Provider setup, local LLMs, and verified model IDs |
| [IMPROVEMENTS.md](IMPROVEMENTS.md) | Known gaps and UI ideas |
| [DOCUMENTATION_STYLE.md](DOCUMENTATION_STYLE.md) | Structure, terminology, and editing checks |
| [CHANGELOG.md](CHANGELOG.md) | Version history |
| [Where localcode differs](https://dennis2lee.github.io/localcode/where-localcode-differs.html) | The capabilities that are not standard in coding agents, with the use case for each |
| [Korean translation of that page](https://dennis2lee.github.io/localcode/where-localcode-differs.ko.html) | Translation of the authoritative English page |
| [Coding agents on one model](https://dennis2lee.github.io/localcode/coding-agent-benchmark.html) | SWE-bench Verified, 25 instances, four agent configurations on one model |
| [LICENSE](../LICENSE) | MIT |

## Architecture

| Layer | Responsibilities |
|---|---|
| Core daemon | Sessions, agent loop, tools, MCP, Skills, providers, and task manager |
| HTTP API | Session creation, messages, permission responses, and background tasks |
| SSE | Tokens, tool lifecycle, permission requests, and task status |
| Clients | TUI and Web UI on the same API |

Sessions use append-only event logs. A TUI restart or new browser tab resumes from a `since` sequence number.

## Install

**Recommended for Linux and command line macOS. No root required:**

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
```

The script verifies the published SHA-256 and installs one static binary at `~/.local/bin/localcode`. It writes nothing outside `$HOME`, uses no package manager, and requests no password. Options after `-s --`: `--version x.y.z`, `--dir ~/bin`, and `--uninstall`. Run the installer again to upgrade.

`~/.local/bin` is the directory Ubuntu's own `~/.profile` puts on PATH when it exists; the script prints the line to add if this shell does not have it yet.

**Other ways:**

| Where | How |
|---|---|
| Ubuntu or Debian system install, with root | `sudo apt install ./localcode-x.y.z-linux-amd64.deb` from the [releases page](https://github.com/dennis2lee/localcode/releases) (`-arm64.deb` on ARM) |
| Windows | `localcode-x.y.z-windows-amd64.msi`, or the portable `.zip` on ARM64 |
| macOS, as an app | `LocalCode-x.y.z-darwin-universal-app.tar.gz`, unpacked into `/Applications` |
| From source | `go build -o localcode ./cmd/localcode` |

See [INSTALL.md](INSTALL.md) for all of them, including what to do when `apt` refuses a local file.

## Quick start

```bash
mkdir -p ~/.localcode
cp config.example.json ~/.localcode/config.json
```

Edit `~/.localcode/config.json`. Set the Bedrock region and model IDs, or the local LLM address. Any value can use `{env:NAME}` to keep API keys out of the file. Then run:

```bash
localcode --agent general-purpose
```

This starts the local daemon and TUI. Open `http://127.0.0.1:4096` for the Web UI.

For a daemon on a remote machine (`--headless`) attached from a laptop (`--server`), see [USAGE.md](USAGE.md#remote-daemon-over-an-ssh-tunnel).

## Tests

Run the full verification suite before committing:

```bash
make check
```

Go tests only:

```bash
go test ./...
```

This includes the Web UI suite. `internal/daemon` runs [test/webui/](../test/webui/), which loads the shipped `index.html` and actual `static/js/*.js` modules in a custom DOM. It uses the Node built-in test runner with no dependencies and skips when `node` is unavailable. Web UI only:

```bash
make test-js
```

## Not done yet

* No macOS code signing or notarization, Windows MSI signing, or `.deb` signing. All packages install unsigned.
* No Windows ARM64 MSI. AMD64 has an MSI; ARM64 has a portable zip.
* No apt repository. Install `.deb` files directly. `apt update` does not offer upgrades; localcode can check GitHub.
* No Linux desktop window. The daemon, TUI, and Web UI are supported.

See [USAGE.md](USAGE.md#known-limitations) for the full list of limitations.
