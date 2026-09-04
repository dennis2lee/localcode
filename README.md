# localcode

localcode is a local coding agent with three provider families: **Amazon Bedrock**, the **Anthropic API**, and **OpenAI-compatible endpoints** such as LM Studio, vLLM, llama.cpp, and Ollama. Models can use file, shell, MCP, and Skill tools.

| Component | Role |
|---|---|
| Core daemon | Sessions, agents, tools, providers, and one HTTP/SSE API |
| TUI | Terminal client |
| Web UI | Browser client |
| Desktop window | Optional native client |

Start with [Install](#install) and [Quick start](#quick-start). See [Where localcode differs](https://dennis2lee.github.io/localcode/where-localcode-differs.html) for capability examples and screenshots.

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
| Model switching | Hosted and local models in one conversation. Tab or `/agent` changes the model, prompt, and tool scope for the next message. History remains unchanged. Each switch is an event. |
| Effort | `off`, `low`, `medium`, `high`, or `xhigh`, set by profile or `/effort`. OpenAI-compatible providers receive `reasoning_effort`, capped at `high`; muse models read the level from a system prompt line instead, which is where their model card puts it; Anthropic receives extended thinking; Bedrock receives `additionalModelRequestFields`. An unset value sends no field. |
| Reasoning display | Reads `reasoning_content` from DeepSeek, vLLM, SGLang, LM Studio, llama.cpp, and Ollama. Reads `reasoning` from OpenRouter. Live display only, with no log storage or later model input. |
| Context window | Server supplied values from `GET /v1/models` or llama.cpp `/props`. Length errors cause summarization and trimming until acceptance. |
| Prompt cache | Breakpoints after the stable prefix (tool schemas and system prompt) and conversation tail. Anthropic uses `cache_control`; Bedrock uses `cachePoint`; providers with server-side prefix caching receive neither. |
| Fallback | Two endpoint retries after 1 and 2 seconds, then the profile `fallback`. A 401 or unknown model ID skips the delay. localcode derives a new request for the fallback model and records the switch. |
| Model specific handling | Additional prompt lines for required model families, automatic continuation for models that stop during tasks, `/keep-going off` to disable it, and LaTeX unwrapping for Gemma. |
| Repeated tool calls | `/repeat-limit` ends a turn after N steps that only repeat tool calls already made, naming them. Off by default; `on` is 3. Some local models think by re-reading, which the guard would cut short. |
| LLM doctor | `/llm-doctor` for a local muse or gemma: the server's reported facts including the `system_fingerprint` off the answer, four canaries sent twice at the sampling the model's own recipe asks for (muse at temperature 1.0 / top_p 0.95 / top_k 64, gemma at temperature 0 with a seed), each passing, failing or inconclusive, and what differs from a baseline kept on a good day. See [USAGE.md](docs/USAGE.md#llm-doctor). |

### Agents, delegation, orchestration

| Area | What you get |
|---|---|
| Multiple agents | Per role model, prompt, and tool scope. Delegation through `Task`. `/model` lists each agent and its resolved model. |
| Automatic delegation | Matching prompts can use a lower cost agent without changing the main session cache. Configuration in the panel below the prompt bar, stored in config.json. |
| Smart Agent | Disabled by default. Six specialists: `explore`, `librarian`, `oracle`, `plan`, `implement`, and `verify`. Separate session and context for each specialist. Models resolve from existing profiles or explicit `smart-quick`, `smart-balanced`, and `smart-deep` profiles. Specialist tool allowlists exclude delegation. |
| Debate | Up to three concurrent reviewers with separate model profiles and no access to other reviews. Read-only tools plus the project `verify_command`, without arguments. Unanimous boolean approval. Stops on approval, the round limit (3 by default, 10 maximum), or two consecutive rejected rounds with no author tool call. Later model context retains the task and result; all rounds remain in the transcript. Entry through plain language, the ⚖️ button, or `/debate <agents> <rounds> <work>`. |
| Orchestrate | Disabled by default. The model submits a structured plan. localcode validates agents, references, and counts before execution. Stage types: one agent, concurrent agents, and barrier. The `keep` field provides review filtering; no expression language. Limits: 8 stages, 32 agent turns, 10 minutes per stage, and 30 minutes per run. Limit violations are rejected, not truncated. localcode creates the execution report. |
| Scheduled tasks | One-time work through plain language, `/schedule`, or the ⏰ button with separate time and request fields. Local parsing for relative, clock, named, weekday, and absolute time forms in Korean and English. Vague or repeating times are rejected. Each run has a status row and separate session with the source workspace and permissions. **Execution requires a running localcode process.** A time missed while localcode is stopped is reported as missed and is not run late. |
| Model-invocable commands | Disabled by default through `/model-invocable`, `model_invocable`, or settings. Commands run as separate turns. Disabling the feature preserves its allowlist. Built-in commands use `model_commands` with the slash. Custom commands and skills use `model_invocable: true`. No wildcard. Commands with a `` !`shell command` `` insertion are always rejected. |
| Concurrency | Optional provider limit through `max_concurrent_tasks`, acquired before the daemon limit. Work waiting for a local GPU does not consume a daemon slot. |

### Sessions

| Area | What you get |
|---|---|
| Event log | Append-only session events. Clients resume from a `since` sequence number without gaps or duplicates. A client outside the retained buffer receives an error and replays from the log. |
| Workspace | Per session directory. Relative paths and bash commands use that directory, which permits multiple projects on one daemon. Every turn derives the current directory again for the system prompt. |
| Cross-session reference | `#S2 has the final report. Check it against the file here.` Resolution by ID, exact title, or unambiguous title prefix, including archived sessions. The reference supplies only a notice that a tool can read the transcript. Any retrieved content returns as an external tool result. References are not transitive in either direction and are not rejected. |
| Archive and retrieve | Archived conversations retain their event logs and remain readable. Existing background tasks can still record results. New work receives HTTP 403, enforced by the store. |
| Undo a turn | `/rewind` removes the last exchange from model history and restores files changed by `write_file` or `edit`. Exclusions: shell writes, background agent writes, user edits, symbolic link paths, and files over 8 MiB. |
| Start fresh | `/clear` appends a history barrier. Earlier events remain visible, persistent, and excluded from later model context. |
| Compaction | `/compact [instructions]` on demand, automatic past a threshold (`/auto-compact 70`, 50% by default), `/usage` for cumulative tokens per model. |
| Steering | Messages entered during a turn are delivered at the next tool call in entry order. Esc stops the process tree, including a running shell command. |
| Restart safety | Session list, conversation context, and `/usage` totals restore from disk. A second terminal in the same project attaches to the running daemon; one in another project starts its own. |

### Tools, permissions, guards

| Area | What you get |
|---|---|
| Permissions | Allow, deny, and ask rules, plus prompt choices for one use, the session, or always. Each bash command is checked in written and unquoted forms. Conversation switches: `/permission-skip-all`, `/permission-skip-tools`, `/read-outside`, and `/write-outside`. |
| Workspace boundary | Physical path checks for access outside the session workspace. A workspace symbolic link to `~/.aws` is outside. Read checks cover `read_file`, `grep`, and `glob`; write checks cover `write_file` and `edit`. Approval scope is the current external directory or any external path. Bash has separate command permissions and is not covered by path checks. |
| Credential guard | Smart Agent denies read, write, and edit access to `.env`, `*.pem`, `id_rsa`, `~/.ssh`, `~/.aws/credentials`, `.netrc`, and related paths. Skip switches cannot override deny rules. Explicit config.json tool rules can permit required project access. |
| Instructions and data | The system prompt labels instruction sources and data sources. MCP output includes a marker with the server name. Labels do not replace permission enforcement. Delegated work cannot change child permission switches, including a task that starts with `/permission-skip-all on`. |
| Search limits | `grep` reports files omitted because of the 200 match limit, lines over 1 MB, or read failures, in both modes. Reports up to three paths. With Smart Agent on, a single file contributes at most 30 matches. |
| File tools | With Smart Agent on: `read_file` supports `offset` and `limit`, with 800 lines by default and a range footer, and binary files receive a description; failed `edit` calls report whitespace differences, punctuation differences, CRLF differences, the location of the first line, or a near match. Near matches are never applied. With it off, `read_file` returns the whole file and a failed `edit` reports only that `old_string` was not found. |
| Tool name repair | Unambiguous decorated names resolve only within the current agent tool list. The result reports the accepted spelling. Tool restrictions remain effective. |
| Hooks | Tool hooks: `pre_tool_use`, `post_tool_use`, `user_prompt_submit`, `stop`, and `session_start`. Lifecycle hooks: `pre_model`, `post_model`, `delegate`, `compact`, and `retry`. `pre_model` can block a request or inject context. `delegate` can reject a child task. Each hook uses the workspace of the source session. |
| MCP | `stdio`, streamable `http`, and `sse` transports. Authentication headers are withheld after redirects to another host. `localcode mcp add/list/get/remove` updates config.json without printing header or environment values. `mcp import-claude` imports Claude Code settings. Tool surface changes produce one startup warning before the stored fingerprint is updated. |
| Debug log | `/debug-log` toggles writing every model request and response, byte for byte and binary included, to one file per prompt in the workspace. Credentials are redacted; nothing else is. Off at every start. |
| Trace | JSON Lines events in `~/.localcode/trace/` for models, profiles, costs, cache use, tool timing, delegation, fallback, and compaction. One trace ID per turn, inherited by child agents. Live tail through `GET /api/trace`. Retention settings: `trace_max_age_days` (30 by default) and optional `trace_max_total_mb`. |
| Prompt inventory | Stable ID, source, trust class, placement, and activation condition for each system prompt component. `/context` reports the next request without a model call, including inclusion reasons, token estimates by category, tool definitions, and untrusted content indicators. `/context all` includes omitted entries. `/context <id>` reports a previous call. Manifests contain identities, hashes, and sizes, never content bodies. |

### Interfaces

| Area | What you get |
|---|---|
| Web UI | Session panel with workspace, switching, ordering, and status. Status colors: blinking green for active work, amber for permission, steady green for unread output, and grey for idle. File drag and drop, Markdown output, live status, native workspace picker, background task panel, and MCP server panel. |
| TUI | Shared daemon and commands. `/session` switches conversations, `/model` and `/agent` switch agents, and `/show-scheduled-task` lists scheduled work. |
| Desktop window | Experimental native Web UI without a separate browser or visible server. Optional `-tags gui` build. `LocalCode.app` on macOS and `localcode-gui.exe` in the Windows MSI. See [USAGE.md](docs/USAGE.md#desktop-window-experimental). |
| Prompt box | Separate unsent drafts per conversation. Up and Down recall prompts, restored from the transcript. Right Arrow completes `/name` entries from installed skills and commands, including names within a sentence such as `read the mail, then run /tid`, then cycles to the original text. Slashes outside a command name remain unchanged as paths. Alt+Up and Alt+Down move the Web UI view between user turns. Inline IME composition. |
| Running work | A transcript entry for each tool call, created at start and completed with the result. Expand it for arguments and output. `/tasks` inspects background work. `/tasks cancel <id>` stops it. Permission requests return to the source session. Task windows show the complete task conversation and delegation. |
| Stable scroll | Manual upward scrolling disables automatic movement in both clients and task windows. User turns have a full height left border. |

### Running and packaging

| Area | What you get |
|---|---|
| Single request | `localcode run "what does this repo do?"` answers in process and exits. No port and no session list entry. Output options: `--format text`, `--format json`, and `--format stream-json`. `--bare` loads only the base prompt. `--session` retains the conversation through a running daemon. Smart Agent is supported. The process waits for child agents before exit. |
| Config | `config.example.json` documents every key and includes a tested orchestration example. User config supports JSONC. localcode preserves comments during config writes. |
| Environment values | String fields support `{env:NAME}` and `{env:NAME:-fallback}` at load time, including API keys, `base_url`, model IDs, and MCP environments. Missing variables produce an error with the variable and field names. Files retain the placeholder. |
| Project context | Session workspace rules from `AGENTS.md`, including `@path` imports, with `CLAUDE.md` as an equal second name. Skills and custom commands come from one root under the project and one under your home, each the first of `.claude`, `.opencode`, `.localcode` that exists, resolved independently, with the project's winning on a name collision. The user-level `AGENTS.md`/`CLAUDE.md` comes from the home root alone. `/init` creates a draft. Automatic memory works across sessions. |
| Updating | A newer release is installed at startup and localcode comes back on it, before anything is served, on every platform: by `exec` where a process can replace its own image, and on Windows by starting the new binary beside the old one on the listener it already holds. `auto_update: false` turns it off. `/update` does the same on demand without restarting what you are looking at: the new daemon takes the socket, the old one finishes its turns, the TUI and the browser reconnect where they were. Two daemons never write one session at once. On Windows the running `.exe` is renamed aside, or staged under the user's cache directory where Program Files is not writable; nothing runs `msiexec` unasked. SHA-256 verified. |
| Windows | Shell selection order: `sh`, Git for Windows `bash.exe`, then `cmd /c`. Missing `python` or `python3`, including Microsoft Store stubs, produces a `winget install` command through the normal permission gate. Detection uses PATH lookup and does not depend on localized shell error text. |
| Linux | User install of one static binary in `~/.local/bin`, with no writes outside `$HOME`. System `.deb` packages for Ubuntu and Debian. Portable tarballs for other distributions. `CGO_ENABLED=0` and no `Depends:` entry. Linux supports the daemon, TUI, and Web UI, but not the desktop window. |

## Documentation

| Document | Contents |
|---|---|
| [INSTALL.md](docs/INSTALL.md) | Release installation, source builds, and distribution packages |
| [USAGE.md](docs/USAGE.md) | config.json, commands, screen controls, session and agent management |
| [MODELS.md](docs/MODELS.md) | Provider setup, local LLMs, and verified model IDs |
| [IMPROVEMENTS.md](docs/IMPROVEMENTS.md) | Known gaps and UI ideas |
| [DOCUMENTATION_STYLE.md](docs/DOCUMENTATION_STYLE.md) | Structure, terminology, and editing checks |
| [CHANGELOG.md](docs/CHANGELOG.md) | Version history |
| [Where localcode differs](https://dennis2lee.github.io/localcode/where-localcode-differs.html) | The capabilities that are not standard in coding agents, with the use case for each |
| [Korean translation of that page](https://dennis2lee.github.io/localcode/where-localcode-differs.ko.html) | Translation of the authoritative English page |
| [Coding agents on one model](https://dennis2lee.github.io/localcode/coding-agent-benchmark.html) | SWE-bench Verified, 25 instances, four agent configurations on one model |
| [LICENSE](LICENSE) | MIT |

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

See [INSTALL.md](docs/INSTALL.md) for all of them, including what to do when `apt` refuses a local file.

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

For a daemon on a remote machine (`--headless`) attached from a laptop (`--server`), see [USAGE.md](docs/USAGE.md#remote-daemon-over-an-ssh-tunnel).

## Tests

Run the full verification suite before committing:

```bash
make check
```

Go tests only:

```bash
go test ./...
```

This includes the Web UI suite. `internal/daemon` runs [test/webui/](test/webui/), which loads the shipped `index.html` and actual `static/js/*.js` modules in a custom DOM. It uses the Node built-in test runner with no dependencies and skips when `node` is unavailable. Web UI only:

```bash
make test-js
```

## Not done yet

* No macOS code signing or notarization, Windows MSI signing, or `.deb` signing. All packages install unsigned.
* No Windows ARM64 MSI. AMD64 has an MSI; ARM64 has a portable zip.
* No apt repository. Install `.deb` files directly. `apt update` does not offer upgrades; localcode can check GitHub.
* No Linux desktop window. The daemon, TUI, and Web UI are supported.

See [USAGE.md](docs/USAGE.md#known-limitations) for the full list of limitations.
