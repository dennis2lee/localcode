# localcode

A coding agent that talks to three provider families: Amazon Bedrock, the Anthropic API directly, and any OpenAI-compatible endpoint (LM Studio, vLLM, and other local runtimes).

The model calls tools itself for file reads and writes, shell execution, MCP, and Skills. The core runs as a headless daemon. A TUI and a browser Web UI both attach to it as equal clients.

## Features

| Area | What you get |
|---|---|
| Providers | Bedrock, Anthropic API, OpenAI-compatible. Switch with one config file. |
| Auth | `localcode login bedrock` (AWS SSO device flow, no AWS CLI needed), `localcode login anthropic` (stores an API key) |
| Permissions | opencode style allow/deny/ask rules, plus allow once, allow for session, or always allow at the prompt. `git` runs without asking by default; a bash line is checked per command, so an allowed prefix cannot carry `&& rm -rf ~` along with it, and each command is matched both as written and unquoted so a deny rule is not escaped by spelling it `"curl"`. `skip_permissions` turns every confirmation off, and is off by default. Current status shows under the prompt; click it to toggle `skip_permissions` or add/remove rules, in the Web UI and the GUI window alike. |
| Hooks | Claude Code style shell hooks on `pre_tool_use`, `post_tool_use`, `user_prompt_submit`, `stop`, `session_start` |
| Multi agent | Per role model, prompt, and tool scope. Agents delegate through the `Task` tool. Tab or `/agent` switches agent without losing session context, in the TUI and the Web UI alike. Matching prompts can auto delegate to a cheaper agent, keeping the main session's prompt cache intact; which prompts and which agent are both set from a panel under the prompt bar, applied to the next prompt and saved to config.json. |
| Project context | `AGENTS.md` with `@path` imports and a `CLAUDE.md` fallback, `/init` to draft one, custom slash commands in `.localcode/commands/*.md`, auto memory the model writes for itself across sessions |
| Conversation | `/compact [instructions]` on demand, automatic compaction past 80% context, `/usage` for cumulative tokens per model (token counts only, no dollar figures) |
| Context limits | The window is read from the server (`GET /v1/models`, or `/props` on llama.cpp) rather than guessed from the model's name, which for a local server cannot be right: it serves whatever was loaded, usually with a smaller window than the model supports. A request refused for being too long does not end the turn: the conversation is summarized, and if that is not enough it is trimmed, shrinking each time until the server accepts it. Sized against the conversation's measured length rather than a window figure, because the character estimate runs about 4x low on Korean and Japanese. Tool output is capped at a quarter of the window as it arrives, so one large file cannot fill the context by itself. A reply cut off by `max_tokens` says so instead of just stopping. |
| Restart safety | Session list, conversation context, and `/usage` totals all restore from disk after a daemon restart |
| Workspace | Per session, not per process. Every relative path and bash command resolves against the directory of the session it belongs to, so two sessions can work in two projects at once on one daemon, and reopening a conversation puts you back in its project. Only that session's own turn blocks its own switch. |
| Web UI | Left panel listing every session with the workspace it belongs to — click one to switch, with a light per session: blinking green while the model is working, steady green for a reply that arrived while you were elsewhere, dark once you have read it. Drag and drop file attach, model output rendered as markdown, Tab to switch agent, live status bar under the prompt (agent, model, context use, tokens per second, activity light, auto-delegate and permission toggles), workspace directory shown in the header (click to type or paste a path, with a Browse button where the daemon can open a native folder picker, and a folder icon that opens the directory in a Finder/Explorer window), switching session moves the workspace to that session's project, right panel listing background tasks — each says which tool it is in, and opens its own live transcript when clicked — and connected MCP servers |
| MCP | All three transports: local `stdio`, remote `http` (streamable) and `sse`, with per-server auth headers, which are withheld if a redirect takes the request off the configured host. `localcode mcp add/list/get/remove` edits config.json for you (never printing header or environment values), the same way `claude mcp` does; `mcp list` connects to each server and reports one line of status per server. `localcode mcp import-claude` pulls an existing Claude Code setup's MCP servers (`./.mcp.json` and `~/.claude.json`) straight in, remote ones included. |
| Steering a turn | Messages typed while a turn is running are delivered to that turn and picked up at the model's next tool call, so a correction lands mid job rather than after it. They arrive in the order typed; stop discards what is still queued. A background task does not block the prompt, since it runs in its own session. |
| Running work | Each tool call gets a transcript line, written when it starts and completed with its result or a failure marker; click it for the full arguments and output. A line below the prompt also names the running tool and background tasks. `/tasks` inspects a background task mid run and `/tasks cancel <id>` stops one; a permission a task needs is asked in the session that spawned it, since nobody is watching the task's own log. |
| Editing | Up and Down recall previous prompts. Esc cancels a running turn, including one sitting in a shell command — the whole process tree is stopped, not just the shell. IME composition renders inside the prompt box. |
| Event delivery | A client that falls behind is told rather than silently skipped: the stream ends, and it reconnects and replays what it missed from the session log. Both clients resume at the right point, with no gap and no duplicate. |
| Dictation | Talk your prompt instead of typing it, by default entirely on your machine with no network. `localcode dictation install` fetches a whisper.cpp engine and model; the engine runs as a child process, so this works in every build rather than only the desktop one. Grey text while you speak, committed when you pause. Korean and English, or both at once. A settings window (⚙ under the prompt) picks the microphone, the spoken language, and — if this machine is the wrong one to run it on — the address of a speech server elsewhere, which need not be whisper.cpp: OpenAI-compatible, whisper.cpp and WhisperX servers are all understood, and which one it is is worked out on the first utterance. A transcription that fails says why instead of looking like a dead microphone. See [USAGE.md](docs/USAGE.md#dictating-a-prompt). |
| Windows | Shell execution resolves to `sh`, then Git for Windows' `bash.exe`, then `cmd /c`, so the bash tool works without Git Bash on PATH. |
| Desktop window | Experimental, opens the Web UI in a native OS window (no browser, no visible server). Built opt in with `-tags gui`; a build made that way opens the window by default (`--gui=false` forces the TUI instead). macOS ships a double-clickable `LocalCode.app` via `make dist-mac-gui`; the Windows MSI installs `localcode-gui.exe` plus a Start Menu shortcut and the WebView2 runtime bootstrapper. Launching it from `cmd` returns the prompt immediately rather than holding the console, and the workspace can be typed, browsed for, or opened in an Explorer window. See [USAGE.md](docs/USAGE.md#desktop-window-experimental). |

## Documentation

| Document | Contents |
|---|---|
| [INSTALL.md](docs/INSTALL.md) | Building from source, producing macOS and Windows packages |
| [USAGE.md](docs/USAGE.md) | config.json, commands, screen controls, session and agent management |
| [MODELS.md](docs/MODELS.md) | Real setup for Bedrock and Claude, local LLMs, and verified model IDs |
| [IMPROVEMENTS.md](docs/IMPROVEMENTS.md) | Known gaps and UI ideas |
| [CHANGELOG.md](docs/CHANGELOG.md) | Version history |
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

## Quick start

```bash
go build -o localcode ./cmd/localcode
mkdir -p ~/.localcode
cp config.example.json ~/.localcode/config.json
```

Edit `~/.localcode/config.json` and set your Bedrock region and model IDs, or the address of your local LLM. Then run:

```bash
./localcode --agent general-purpose
```

The default run starts a local daemon and attaches the TUI to it. Open the same address (`http://127.0.0.1:4096`) in a browser to use the Web UI at the same time.

To run the daemon on a remote machine with `--headless` and attach from your laptop with `--server`, see [USAGE.md](docs/USAGE.md#remote-daemon-over-an-ssh-tunnel).

## Tests

```bash
go test ./...
```

That includes the Web UI: `internal/daemon` shells out to the JavaScript suite
in [test/webui/](test/webui/), which loads the shipped `index.html` and
`app.js` into a hand-written DOM (Node's built-in test runner, no
dependencies). It skips itself when `node` isn't installed. To run only that
part while editing the page:

```bash
make test-js
```

## Not done yet

* macOS code signing and notarization, and Windows MSI code signing. Both install, but neither is signed.
* Windows arm64 MSI. Only amd64 ships an MSI today, arm64 ships a portable zip.

See [USAGE.md](docs/USAGE.md#known-limitations) for the full list of limitations.
