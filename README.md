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
| Config from the environment | Any string in config.json may be `{env:NAME}`, or `{env:NAME:-fallback}` when it is optional, and is read from the environment as the file loads — API keys, a base_url, a model id, an MCP server's own environment, anything that differs per machine. The same spelling opencode uses. A variable that is missing is an error naming it and the field that asked for it, not an empty string that fails later as a 401. What is on disk stays a placeholder: every write rewrites one key of the file itself, so saving a setting never turns it into the secret it stands for. |
| Project context | `AGENTS.md` with `@path` imports and a `CLAUDE.md` fallback, read from the workspace of the session that is asking — so two sessions in two projects each get their own, and editing the file takes effect on the next message. `~/.localcode/AGENTS.md` and `~/.claude/CLAUDE.md` both apply everywhere, so an existing Claude Code setup is reused rather than replaced. `/init` to draft one, custom slash commands in `.localcode/commands/*.md`, auto memory the model writes for itself across sessions |
| Model quirks | A model whose id says it needs it gets one extra line of system prompt, and nothing else is told anything. Gemma writes `$\rightarrow$` and `$\text{name}$`, which needs MathJax to read as anything; it is asked for the character instead, and the Web UI unwraps the ones that arrive anyway. A model that ends its turn describing the next step instead of taking it is asked to take it, and is told to carry on up to three times per turn when it does it anyway — not when nothing ran, when a tool was refused, when it asked you a question, or when you have already typed the answer. `keep_going` on the profile sets the number, or `-1` turns it off. |
| Conversation | `/compact [instructions]` on demand, automatic compaction past 80% context, `/usage` for cumulative tokens per model (token counts only, no dollar figures) |
| Context limits | The window is read from the server (`GET /v1/models`, or `/props` on llama.cpp) rather than guessed from the model's name, which for a local server cannot be right: it serves whatever was loaded, usually with a smaller window than the model supports. A request refused for being too long does not end the turn: the conversation is summarized, and if that is not enough it is trimmed, shrinking each time until the server accepts it. Sized against the conversation's measured length rather than a window figure, because the character estimate runs about 4x low on Korean and Japanese. Tool output is capped at a quarter of the window as it arrives, so one large file cannot fill the context by itself. A reply cut off by `max_tokens` says so instead of just stopping. |
| Restart safety | Session list, conversation context, and `/usage` totals all restore from disk after a daemon restart |
| Workspace | Per session, not per process. Every relative path and bash command resolves against the directory of the session it belongs to, so two sessions can work in two projects at once on one daemon, and reopening a conversation puts you back in its project. Only that session's own turn blocks its own switch. |
| Web UI | Left panel listing every session with the workspace it belongs to — click one to switch, drag one to move it up or down (the order is saved on the daemon), with a light per session: blinking amber while the model is working, steady green for a reply that arrived while you were elsewhere, grey when nothing is running. Drag and drop file attach, model output rendered as markdown, Tab to switch agent, live status bar under the prompt (agent, model, context use, tokens per second, activity light, auto-delegate and permission toggles), workspace directory shown in the header (click to type or paste a path, with a Browse button where the daemon can open a native folder picker, and a folder icon that opens the directory in a Finder/Explorer window), switching session moves the workspace to that session's project, right panel listing background tasks — each says which tool it is in, and opens its own live transcript when clicked — and connected MCP servers |
| MCP | All three transports: local `stdio`, remote `http` (streamable) and `sse`, with per-server auth headers, which are withheld if a redirect takes the request off the configured host. `localcode mcp add/list/get/remove` edits config.json for you (never printing header or environment values), the same way `claude mcp` does; `mcp list` connects to each server and reports one line of status per server. `localcode mcp import-claude` pulls an existing Claude Code setup's MCP servers (`./.mcp.json` and `~/.claude.json`) straight in, remote ones included. |
| Steering a turn | Messages typed while a turn is running are delivered to that turn and picked up at the model's next tool call, so a correction lands mid job rather than after it. They arrive in the order typed; stop discards what is still queued. A background task does not block the prompt, since it runs in its own session. |
| Running work | Each tool call gets a transcript line, written when it starts and completed with its result or a failure marker; click it for the full arguments and output. A line below the prompt also names the running tool and background tasks. `/tasks` inspects a background task mid run and `/tasks cancel <id>` stops one; a permission a task needs is asked in the session that spawned it, since nobody is watching the task's own log. |
| Reading while it writes | Scrolling up holds the view there, however much the model writes underneath it, in the Web UI and the TUI alike — and in a background task's own window. Following the newest output resumes by scrolling back to the bottom, since being at the bottom is the whole condition; the Web UI also offers a jump-to-latest control while the view is away from it. Sending a prompt or opening a session goes to the bottom, because that is what was just asked for. |
| Editing | Up and Down recall previous prompts, per conversation: each session keeps its own list, refilled from the transcript when the session opens, so reopening one (or attaching the TUI to it) recalls what was asked in it before. Esc cancels a running turn, including one sitting in a shell command — the whole process tree is stopped, not just the shell. IME composition renders inside the prompt box. |
| Event delivery | A client that falls behind is told rather than silently skipped: the stream ends, and it reconnects and replays what it missed from the session log. Both clients resume at the right point, with no gap and no duplicate. |
| Dictation | Talk your prompt instead of typing it, by default entirely on your machine with no network. `localcode dictation install` fetches a whisper.cpp engine and model; the engine runs as a child process, so this works in every build rather than only the desktop one. Grey text while you speak, committed when you pause. Korean and English, or both at once. A settings window (⚙ under the prompt) picks the microphone — asking the browser for the device list, since it hides one until it has been allowed to record — the spoken language, which applies as it is chosen and says so when the engine in force cannot honour it, and — if this machine is the wrong one to run it on — the address and port of a speech server elsewhere, which need not be whisper.cpp: WhisperLive, OpenAI-compatible, whisper.cpp and WhisperX servers are all understood, worked out when dictation starts or named outright in the same window. A WhisperLive server is streamed to over a WebSocket — the audio goes out as it is recorded and the text comes back mid-sentence, instead of the whole utterance being re-sent four times a second. Everything else about dictation is identical wherever the engine runs — including the decoding options localcode would pass to an engine it started, which travel as request fields instead, and timestamps, which are stripped from whatever the server sends. A server that stops answering is given up on and said out loud rather than leaving the microphone stuck on. A transcription that fails says why instead of looking like a dead microphone. See [USAGE.md](docs/USAGE.md#dictating-a-prompt). |
| Updates | The settings window checks GitHub for a newer release when asked, and — where the daemon and the person clicking share a machine, meaning the desktop window or a daemon on loopback — downloads it and installs it after a confirmation: the platform's installer where there is one, and the binary itself where localcode owns the file it runs from. Nothing runs on a timer and nothing downloads unasked: a check is an outbound request that says which version this machine runs. The download is verified against the release's SHA-256 before anything is run. Over `--server` the check still works and the install button does not, since it would replace the program on the server. |
| Windows | Shell execution resolves to `sh`, then Git for Windows' `bash.exe`, then `cmd /c`, so the bash tool works without Git Bash on PATH. |
| Linux | Installs without root: one command puts a static binary in `~/.local/bin` and writes nothing outside `$HOME` (`curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh \| sh`), which is the case on a managed or shared machine. A `.deb` for Ubuntu and Debian (`sudo apt install ./localcode-x.y.z-linux-amd64.deb`, amd64 and arm64) when localcode is for everyone on the box, and a portable tarball for everything else. Static: built with `CGO_ENABLED=0`, so there is no `Depends:` line and nothing to install alongside it. The update check offers whichever matches how localcode was installed, and a copy you own it replaces for you — unpacked beside the old one, checked that it runs, renamed into place, so nothing needs root and nothing is half-written. No desktop window there — the daemon, the TUI, and the Web UI in a browser are what a Linux install is. |
| Desktop window | Experimental, opens the Web UI in a native OS window (no browser, no visible server). Built opt in with `-tags gui`; a build made that way opens the window by default (`--gui=false` forces the TUI instead). macOS ships a double-clickable `LocalCode.app` via `make dist-mac-gui`; the Windows MSI installs `localcode-gui.exe` plus a Start Menu shortcut and the WebView2 runtime bootstrapper. Launching it from `cmd` returns the prompt immediately rather than holding the console, and the workspace can be typed, browsed for, or opened in an Explorer window. Neither platform shows a title bar: macOS draws it into the page's own background, Windows replaces the frame outright and the page draws the window buttons (`LOCALCODE_TITLEBAR=1` puts the ordinary frame back). See [USAGE.md](docs/USAGE.md#desktop-window-experimental). |

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

## Install

**Ubuntu, Debian, any Linux, and macOS from the command line. No root:**

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
```

One static binary in `~/.local/bin/localcode`. Nothing is written outside `$HOME`, no package manager is involved, and you are never asked for a password, which is what you need on a machine you do not administer. The download is checked against the SHA-256 GitHub publishes for it. Options go after `-s --`: `--version x.y.z`, `--dir ~/bin`, `--uninstall`. Running it again is the upgrade.

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

Edit `~/.localcode/config.json` and set your Bedrock region and model IDs, or the address of your local LLM. An API key can stay out of the file entirely: any value may be `{env:NAME}`, read from the environment as the file loads. Then run:

```bash
localcode --agent general-purpose
```

The default run starts a local daemon and attaches the TUI to it. Open the same address (`http://127.0.0.1:4096`) in a browser to use the Web UI at the same time.

To run the daemon on a remote machine with `--headless` and attach from your laptop with `--server`, see [USAGE.md](docs/USAGE.md#remote-daemon-over-an-ssh-tunnel).

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

* macOS code signing and notarization, Windows MSI code signing, and a signed `.deb`. All three install; none of them is signed.
* Windows arm64 MSI. Only amd64 ships an MSI today, arm64 ships a portable zip.
* An apt repository. The `.deb` installs from the file, so `apt update` never offers an upgrade; localcode checks GitHub itself.
* A desktop window on Linux. The daemon, TUI, and Web UI all run there.

See [USAGE.md](docs/USAGE.md#known-limitations) for the full list of limitations.
