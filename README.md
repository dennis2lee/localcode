# localcode

A coding agent that runs on your own machine against three provider families: **Amazon Bedrock**, the **Anthropic API**, and any **OpenAI-compatible endpoint** (LM Studio, vLLM, llama.cpp, Ollama). The model calls tools itself for file reads and writes, shell execution, MCP, and Skills.

The core is a headless daemon. A TUI, a browser Web UI, and an optional desktop window attach to it as equal clients on one HTTP/SSE API.

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
```

## What is not standard in a coding agent

| Capability | In one line |
|---|---|
| Debate | Adversarial review by up to three other agents, each on its own model, over real rounds with real sub-sessions. |
| Scheduled tasks | A time in the prompt books work instead of doing it now. Parsed by localcode, in Korean and English. |
| Orchestrate | A multi-stage plan the model authors as data and localcode validates before a token is spent. |
| Cross-session reference | `#S2` in a prompt, resolved to a tool the model may call, never spliced into the message. |
| Provider crossing | A hosted model and a local one in the same conversation. |
| Prompt inventory | `/context`: what the next request would carry, per source, with trust classes, and no model call. |
| Trace | One JSON line per model call, tool call, delegation, fallback, and compaction, under one trace id per turn. |
| Model-invocable commands | The model may run a slash command as its own turn, off by default, allowlisted with no wildcard. |
| Three front ends | TUI, browser, and native window, all first-class clients of one daemon. |
| Workspace boundary | Any path leaving the session's workspace is its own question, judged on physical paths. |

Use case and screenshots for each: [Where localcode differs](https://dennis2lee.github.io/localcode/where-localcode-differs.html).

## Features

### Models and providers

| Area | What you get |
|---|---|
| Providers | Bedrock, Anthropic API, OpenAI-compatible, chosen in one config file. AWS configuration is read on first Bedrock use only, so an unused entry cannot break local-only startup. |
| Auth | `localcode login bedrock` (AWS SSO device flow, no AWS CLI needed), `localcode login anthropic` (stores an API key). |
| Model switching | A hosted model and a local one in one conversation. Tab or `/agent` changes the model, prompt, and tool scope for the *next* message; the history is untouched and the switch is recorded as an event. |
| Effort | `off`, `low`, `medium`, `high`, on a profile or per conversation with `/effort`. Sent as `reasoning_effort` to an OpenAI-compatible server, as extended thinking to the Anthropic API, and through `additionalModelRequestFields` on Bedrock. Unset by default, and unset sends nothing. |
| Reasoning display | Both spellings read: `reasoning_content` (DeepSeek, vLLM, SGLang, LM Studio, llama.cpp, Ollama) and `reasoning` (OpenRouter). Shown live, never written to the log, never sent back. |
| Context window | Read from the server (`GET /v1/models`, or `/props` on llama.cpp) rather than guessed from the model name. A request refused for length is summarized, then trimmed, until the server accepts it. |
| Prompt cache | Breakpoints on the stable prefix (tool schemas, system prompt) and on the conversation tail. `cache_control` on Anthropic, `cachePoint` on Bedrock, nothing where the server does its own prefix caching. |
| Fallback | Two retries on the failing endpoint (1s, then 2s), then the profile's `fallback`. A 401 or an unknown model id skips the wait. The request is re-derived rather than resent with a new model id, and the switch is recorded in the transcript. |
| Model quirks | One extra prompt line for the families that need it, keep-going for models that stop mid-task (`/keep-going` off), LaTeX unwrapping for Gemma. |

### Agents, delegation, orchestration

| Area | What you get |
|---|---|
| Multi agent | Per-role model, prompt, and tool scope. Agents delegate through `Task`. `/model` lists every agent with the model it resolves to. |
| Auto delegate | Matching prompts routed to a cheaper agent, leaving the main session's cache intact. Set from a panel under the prompt bar, saved to config.json. |
| Smart Agent | Off by default. Turns the session model into an orchestrator with six specialists: `explore`, `librarian`, `oracle`, `plan`, `implement`, `verify`. Each runs in its own session and context, so fifty files read for one answer cost the main conversation a paragraph. Models resolved from existing profiles, or pinned with `smart-quick`, `smart-balanced`, `smart-deep`. No specialist may delegate, as a tool allowlist rather than a line of prompt. |
| Debate | Up to three reviewers, each a separate agent on its own model profile, reading the author's work and saying what is wrong, over rounds. Reviewers run at the same time and cannot see each other. Read-only tools plus this project's own `verify_command`, no arguments. Approval is a boolean every reviewer must set. Ends on approval, on the round budget (3 by default, 10 at most), or on a two-round standoff, and the closing line says which. Entry: plain language, the ⚖️ button, or `/debate <agents> <rounds> <work>`. |
| Orchestrate | Off by default. The plan is data: the model fills in the tool's own input schema, so every agent, reference, and count is checked before a token is spent, and an unworkable plan is refused with the reason. Three stage kinds (one agent once, fan-out, barrier), with `keep` as the adversarial filter and no expression language. Ceilings of 8 stages, 32 agent turns, 10 minutes a stage, 30 a run, all refusals rather than truncations. The report is written by localcode from what happened. |
| Scheduled tasks | A time in the prompt books the work instead of running it. Also `/schedule`, or the ⏰ button, which takes the moment and the request in two fields. The time is parsed by localcode, not by the model: relative, clock, named, weekday, and absolute forms in Korean and English. A vague time or a repeat is refused by name rather than guessed at. Each run gets its own session under the conversation's workspace and permission switches, and a row with a status light. **It fires only while localcode is running**; a moment that passed while it was closed is reported as missed, not run late. |
| Model-invocable commands | Off by default (`/model-invocable`, `model_invocable`, or the settings window). Two levels: the switch, and the list, written separately so turning the switch off keeps the choices. A built-in is named in `model_commands` with its slash; a custom command or skill opts itself in with `model_invocable: true`. No wildcard. A command containing a `` !`shell command` `` splice can never be model-invocable, and the refusal is named. |
| Concurrency | A provider entry may carry its own `max_concurrent_tasks`, taken before the daemon-wide slot, so a task queued on a busy local GPU is not holding a slot a hosted task could use. |

### Sessions

| Area | What you get |
|---|---|
| Event log | A session is an append-only event log, not an array of messages. Clients resume from a `since` sequence number with no gap and no duplicate; one that falls behind is told rather than silently skipped, and replays from the log. |
| Workspace | Per session, not per process. Every relative path and bash command resolves against its own session's directory, so two projects run on one daemon. The system prompt names that directory, re-derived every turn so it follows a workspace move instead of going stale. |
| Cross-session reference | `#S2 has the final report. Check it against the file here.` Resolves by id, exact title, or unambiguous title prefix, archive included. Fetches nothing: the reference becomes localcode's own line saying the transcript can be read with a tool, so it arrives as a tool result, the least trusted class, and never re-enters the message path. Non-transitive in both halves. Never refused, in either direction. |
| Archive and retrieve | A shelf, not a bin. An archived conversation keeps everything including its log, stays readable, and still records a background task that outlives the archive. What it refuses is *starting* work, as one store-level invariant rather than a check per caller, and the refusal is 403 and never 409. |
| Undo a turn | `/rewind` puts back the last turn: the exchange leaves the model's history, and every file `write_file` or `edit` changed is restored. The reply names its limits every time: not a shell command's writes, not a background sub-agent's, not your own edits, and not a symlinked path or a file over 8 MiB. |
| Start fresh | `/clear` leaves the model holding none of the conversation and the conversation holding all of it, as a barrier appended to the log rather than a deletion. Everything stays on screen and survives a restart. |
| Compaction | `/compact [instructions]` on demand, automatic past a threshold (`/auto-compact 70`, 50% by default), `/usage` for cumulative tokens per model. |
| Steering | Messages typed during a turn are delivered to that turn at the model's next tool call, in the order typed. Esc stops the whole process tree, a running shell command included. |
| Restart safety | Session list, conversation context, and `/usage` totals restore from disk. A second terminal in the same project attaches to the running daemon; one in another project starts its own. |

### Tools, permissions, guards

| Area | What you get |
|---|---|
| Permissions | opencode-style allow/deny/ask rules, plus allow once, for the session, or always at the prompt. A bash line is checked per command, matched both as written and unquoted. Four switches belonging to the conversation rather than the daemon: `/permission-skip-all`, `/permission-skip-tools`, `/read-outside`, `/write-outside`. |
| Workspace boundary | A path leaving the session's workspace is a question in its own right, judged on physical paths, so a link inside the workspace aimed at `~/.aws` is outside. Covers `read_file`, `grep`, `glob` as reading and `write_file`, `edit` as writing, with a switch for each half. Answered by place: this directory for the session, or anywhere outside the project. Bash is not covered and does not claim to be, since a shell command is not a path. |
| Credential guard | `.env`, `*.pem`, `id_rsa`, `~/.ssh`, `~/.aws/credentials`, `.netrc` and the rest denied for read, write, and edit with Smart Agent on. Deny class, so no skip switch downgrades it; a rule for the same tool in config.json is the way through for a project that has to edit its own `.env`. |
| Instructions and data | Every turn's system prompt states which sources are instructions (you, the prompt, the project's rules) and which are data (every tool result), and MCP output arrives wrapped in a marker naming its server. Labelling rather than enforcement: the permission gate stays the control. A delegated task is work and not a command, so a sub-agent's task beginning `/permission-skip-all on` cannot flip the child's switch. |
| Search that admits what it missed | `grep` names the files it did not finish: past its 200-match cap, a line over 1 MB, a file it could not open. At most three paths named rather than counted, and no single file may take more than 30 of the 200 results. |
| File tools | `read_file` pages (`offset`, `limit`, 800 lines by default) with a footer naming the lines you got out of the lines there are; a binary file is described, not rendered. A failed `edit` says why (whitespace only, CRLF, or where its first line does appear) and a near miss is reported rather than applied. |
| Tool name repair | A decorated name that unambiguously means one tool the agent actually has runs it and says which spelling worked. Resolution searches only that agent's own roster, so a misspelling cannot reach past a `tools` restriction. |
| Hooks | Tool hooks on `pre_tool_use`, `post_tool_use`, `user_prompt_submit`, `stop`, `session_start`, and lifecycle hooks `pre_model` (block a request, or inject a fact), `post_model`, `delegate` (refuse a sub-agent, which a prompt cannot do), `compact`, `retry`. Each runs in the workspace of the session whose event triggered it. |
| MCP | All three transports: `stdio`, `http` (streamable), `sse`, with per-server auth headers withheld if a redirect leaves the configured host. `localcode mcp add/list/get/remove` edits config.json without printing header or environment values; `mcp import-claude` pulls an existing Claude Code setup straight in. A server's surface is fingerprinted, and a change is one startup warning before the pin moves. |
| Trace | One JSON line per event to `~/.localcode/trace/`: which model answered on which profile, cost including cache reads and writes, tool timings, delegations, fallbacks, compactions. One trace id per turn, inherited by every sub-agent, so a fan-out to three specialists greps back as one story. `GET /api/trace` tails it live. Retention: `trace_max_age_days` (30), optional `trace_max_total_mb`. |
| Prompt inventory | Every piece of the system prompt has a stable id, a source, a trust class saying whether it may be followed as instruction, a place in the request, and a condition. `/context` reports the assembly the next turn would send with no model call: what is included and why, per-category token estimates, tool definitions, and whether the request carries content that may not be followed as instruction. `/context all` adds what was left out; `/context <id>` reads back a call that happened. Identities, hashes, and sizes only, never bodies. |

### Interfaces

| Area | What you get |
|---|---|
| Web UI | Left panel of every session with its workspace, click to switch and drag to reorder, each with a status light (blinking green working, amber waiting on a permission, steady green for an unread reply, grey idle). Drag-and-drop file attach, markdown output, live status bar, workspace directory in the header with a native folder picker, right panel of background tasks and connected MCP servers. |
| TUI | The same daemon, the same commands. `/session` switches conversation in place, `/model` and `/agent` switch agent, `/show-scheduled-task` lists what is booked. |
| Desktop window | Experimental. The Web UI in a native OS window, no browser and no visible server. Built opt-in with `-tags gui`; `LocalCode.app` on macOS, `localcode-gui.exe` in the Windows MSI. See [USAGE.md](docs/USAGE.md#desktop-window-experimental). |
| Prompt box | Up and Down recall previous prompts, per conversation, refilled from the transcript when a session opens. The right arrow completes a `/name` against installed skills and commands, cycling back to what you typed, and completes mid-sentence rather than replacing the box, so `read the mail, then run /tid` finishes in place. A slash that does not open a word is a path and is left alone. Alt+Up and Alt+Down move the *view* between your own turns (Web UI). IME composition renders inside the box. |
| Running work | Every tool call gets a transcript line, written when it starts and completed with its result; click it for arguments and output. `/tasks` inspects a background task mid-run, `/tasks cancel <id>` stops one, and a permission it needs is asked in the session that spawned it. A task's own window shows its whole conversation, delegations included. |
| Reading while it writes | Scrolling up holds the view there however much is written underneath, in both clients and in a task's own window. Your turns are marked at their edges, with a rule down the whole left side, so a ten-line paste is marked on every line. |

### Running and packaging

| Area | What you get |
|---|---|
| One prompt, no window | `localcode run "what does this repo do?"` answers and exits, in process: no port bound and nothing written to the session list. `--format text` for a person, `json` for a script, `stream-json` for the event stream every other client reads. `--bare` loads nothing but the base prompt, which is what makes a comparison against another tool fair. `--session` keeps the conversation and routes it through a running daemon. Smart Agent works here, and a run does not exit while a sub-agent it launched is still working. |
| Config | `config.example.json` is the reference: every key with a note on it, plus a worked orchestration setup that a test holds to the roster localcode actually builds. Your own config may be JSONC, and comments survive the writes localcode makes to it. |
| Config from the environment | Any string may be `{env:NAME}` or `{env:NAME:-fallback}`, read as the file loads: API keys, a base_url, a model id, an MCP server's environment. A missing variable is an error naming it and the field that asked, not an empty string that fails later as a 401. What is on disk stays a placeholder. |
| Project context | `AGENTS.md` with `@path` imports and a `CLAUDE.md` fallback, read from the asking session's workspace. `~/.localcode/AGENTS.md` and `~/.claude/CLAUDE.md` apply everywhere, so an existing Claude Code setup is reused. `/init` to draft one, custom commands in `.localcode/commands/*.md`, auto memory across sessions. |
| Updating | The settings window checks GitHub when asked, verifies the download against the release SHA-256, and installs: the platform's installer where there is one, the binary in place where localcode owns it, then `exec` back into the same terminal. Nothing runs on a timer and nothing downloads unasked. Not available over `--server`, and no restart on Windows, which the reply says rather than promises. |
| Windows | Shell execution resolves to `sh`, then Git for Windows' `bash.exe`, then `cmd /c`. A `python`/`python3` that resolves to a Microsoft Store stub, or is absent from PATH entirely, is answered with the `winget install` line through the ordinary permission gate rather than left as a bare shell error. Keyed on PATH lookup, not on the shell's error text, which is translated on every non-English Windows. |
| Linux | Installs without root: a static binary in `~/.local/bin`, nothing written outside `$HOME`. A `.deb` for Ubuntu and Debian when localcode is for everyone on the box, and a portable tarball for everything else. `CGO_ENABLED=0`, so there is no `Depends:` line. No desktop window there; the daemon, TUI, and Web UI are what a Linux install is. |

## Documentation

| Document | Contents |
|---|---|
| [INSTALL.md](docs/INSTALL.md) | Building from source, producing macOS and Windows packages |
| [USAGE.md](docs/USAGE.md) | config.json, commands, screen controls, session and agent management |
| [MODELS.md](docs/MODELS.md) | Real setup for Bedrock and Claude, local LLMs, and verified model IDs |
| [IMPROVEMENTS.md](docs/IMPROVEMENTS.md) | Known gaps and UI ideas |
| [CHANGELOG.md](docs/CHANGELOG.md) | Version history |
| [Where localcode differs](https://dennis2lee.github.io/localcode/where-localcode-differs.html) | The capabilities that are not standard in coding agents, with the use case for each |
| [Korean translation of that page](https://dennis2lee.github.io/localcode/where-localcode-differs.ko.html) | The English page is the source of truth |
| [Coding agents on one model](https://dennis2lee.github.io/localcode/coding-agent-benchmark.html) | SWE-bench Verified, 25 instances, four agent configurations on one model |
| [LICENSE](LICENSE) | MIT |

## Architecture

```
[core daemon]  sessions, agent loop, tools, MCP, Skills, providers, task manager
   |- HTTP API   create session, send message, answer permission, spawn background task
   |_ SSE        token stream, tool start and end, permission requests, task status
        ^              ^
     [TUI]         [Web UI]   both are first class clients on the same API
```

A session is an append only event log, not an array of messages. Close the TUI or open a new browser tab and it resumes from a single `since` sequence number.

## Install

**Linux and macOS from the command line. No root:**

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
```

One static binary in `~/.local/bin/localcode`, checked against the SHA-256 GitHub publishes for it. Nothing is written outside `$HOME`, no package manager is involved, and no password is asked for, which is what a machine you do not administer needs. Options go after `-s --`: `--version x.y.z`, `--dir ~/bin`, `--uninstall`. Running it again is the upgrade.

`~/.local/bin` is the directory Ubuntu's own `~/.profile` puts on PATH when it exists; the script prints the line to add if this shell does not have it yet.

**Other ways:**

| Where | How |
|---|---|
| Ubuntu or Debian, for every user on the machine (needs root) | `sudo apt install ./localcode-x.y.z-linux-amd64.deb` from the [releases page](https://github.com/dennis2lee/localcode/releases) (`-arm64.deb` on ARM) |
| Windows | `localcode-x.y.z-windows-amd64.msi`, or the portable `.zip` on ARM64 |
| macOS, as an app | `LocalCode-x.y.z-darwin-universal-app.tar.gz`, unpacked into `/Applications` |
| From source | `go build -o localcode ./cmd/localcode` |

See [INSTALL.md](docs/INSTALL.md) for all of them, including what to do when `apt` refuses a local file.

## Quick start

```bash
mkdir -p ~/.localcode
cp config.example.json ~/.localcode/config.json
```

Edit `~/.localcode/config.json` and set your Bedrock region and model IDs, or the address of your local LLM. An API key can stay out of the file entirely: any value may be `{env:NAME}`, read from the environment as the file loads. Then:

```bash
localcode --agent general-purpose
```

That starts a local daemon and attaches the TUI to it. Open `http://127.0.0.1:4096` in a browser for the Web UI at the same time.

For a daemon on a remote machine (`--headless`) attached from a laptop (`--server`), see [USAGE.md](docs/USAGE.md#remote-daemon-over-an-ssh-tunnel).

## Tests

```bash
go test ./...
```

That includes the Web UI: `internal/daemon` shells out to the JavaScript suite
in [test/webui/](test/webui/), which loads the shipped `index.html` and the
real `static/js/*.js` ES modules into a hand-written DOM (Node's built-in test
runner, no dependencies). It skips itself when `node` isn't installed. To run
only that part while editing the page:

```bash
make test-js
```

## Not done yet

* macOS code signing and notarization, Windows MSI code signing, and a signed `.deb`. All three install; none is signed.
* Windows arm64 MSI. Only amd64 ships an MSI today, arm64 ships a portable zip.
* An apt repository. The `.deb` installs from the file, so `apt update` never offers an upgrade; localcode checks GitHub itself.
* A desktop window on Linux. The daemon, TUI, and Web UI all run there.

See [USAGE.md](docs/USAGE.md#known-limitations) for the full list of limitations.
