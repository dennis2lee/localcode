# localcode

A coding agent that talks to three provider families: Amazon Bedrock, the Anthropic API directly, and any OpenAI-compatible endpoint (LM Studio, vLLM, and other local runtimes).

The model calls tools itself for file reads and writes, shell execution, MCP, and Skills. The core runs as a headless daemon. A TUI and a browser Web UI both attach to it as equal clients.

## Features

| Area | What you get |
|---|---|
| Providers | Bedrock, Anthropic API, OpenAI-compatible. Switch with one config file. |
| Auth | `localcode login bedrock` (AWS SSO device flow, no AWS CLI needed), `localcode login anthropic` (stores an API key) |
| Permissions | opencode style allow/deny/ask rules. Auto allow `git *`, auto block `rm *`, and so on. |
| Hooks | Claude Code style shell hooks on `pre_tool_use`, `post_tool_use`, `user_prompt_submit`, `stop`, `session_start` |
| Multi agent | Per role model, prompt, and tool scope. Agents delegate through the `Task` tool. Tab or `/agent` switches agent without losing session context. |
| Project context | `AGENTS.md` with `@path` imports and a `CLAUDE.md` fallback, `/init` to draft one, custom slash commands in `.localcode/commands/*.md`, auto memory the model writes for itself across sessions |
| Conversation | `/compact [instructions]` on demand, automatic compaction past 80% context, `/usage` for cumulative tokens per model (token counts only, no dollar figures) |
| Restart safety | Session list, conversation context, and `/usage` totals all restore from disk after a daemon restart |
| Web UI | Drag and drop file attach, live status bar under the prompt (agent, model, context use, TPS, activity light), right panel with session rename and delete plus connected MCP servers |
| MCP | `localcode mcp add/list/get/remove` edits config.json for you, the same way `claude mcp` does |
| Prompt queue | Messages typed while the model is still answering queue up and send in order as soon as the turn ends |

## Documentation

| Document | Contents |
|---|---|
| [INSTALL.md](INSTALL.md) | Building from source, producing macOS and Windows packages |
| [USAGE.md](USAGE.md) | config.json, commands, screen controls, session and agent management |
| [MODELS.md](MODELS.md) | Real setup for Bedrock and Claude, local LLMs, and verified model IDs |
| [IMPROVEMENTS.md](IMPROVEMENTS.md) | Known gaps and UI ideas |
| [CHANGELOG.md](CHANGELOG.md) | Version history |
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

To run the daemon on a remote machine with `--headless` and attach from your laptop with `--server`, see [USAGE.md](USAGE.md#remote-daemon-over-an-ssh-tunnel).

## Tests

```bash
go test ./...
```

## Not done yet

* macOS code signing and notarization, and Windows MSI code signing. Both install, but neither is signed.
* Windows arm64 MSI. Only amd64 ships an MSI today, arm64 ships a portable zip.

See [USAGE.md](USAGE.md#known-limitations) for the full list of limitations.
