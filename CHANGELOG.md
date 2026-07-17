# Changelog

## v0.5.0

- Multi-agent Task delegation: `AgentConfig` gains `description`/`prompt`/`tools`, per-agent tool scoping enforced in both the specs the model sees and at call time, `Task` tool (registered once 2+ agents are configured) for synchronous delegation with a depth guard against infinite recursion
- Plan mode: mid-conversation agent switching (`Store.SetAgent`, `agent.switched` event, `GET /api/agents`, `POST /api/sessions/{id}/agent`) — TUI Tab-key cycling + `/agent` command, Web UI header dropdown + `/agent` command
- AGENTS.md/CLAUDE.md project + global rules files, auto-loaded into the system prompt (opencode/Claude Code convention) — project file found by climbing from cwd to the git root, global file at `~/.localcode/AGENTS.md` (falls back to `~/.claude/CLAUDE.md`), both included when present
- `/init` command: scans the repo and has the model generate or improve `AGENTS.md`
- Custom slash commands: `.localcode/commands/*.md` (project) and `~/.localcode/commands/*.md` (global), with YAML frontmatter (`description`/`agent`/`model`) and template expansion (`$ARGUMENTS`, `$1`-`$9`, `` !`shell` ``, `@file`); `GET /api/commands` + `/commands` local listing in TUI/Web UI

## v0.4.0

- `-version`/`--version` inline flag (alongside the existing `localcode version` subcommand)
- Prompt input auto-grows as the message gets longer (TUI: `bubbles/textarea`, Enter to send / Ctrl+J for newline; Web UI: `<textarea>` with JS auto-resize, Shift+Enter for newline)
- `/skill` slash command: `/skill` lists registered skills with no model call, `/skill <name>` splices that skill's body into the model's turn immediately
- `/help`, `/version`, `exit`/`:q` local commands in both TUI and Web UI (handled client-side; `/version` reports the attached daemon's build version via `GET /api/version`)
- [MODELS.md](MODELS.md): verified Bedrock/Claude model IDs and setup steps, plus local-LLM (LM Studio/vLLM) setup — flags that the newest Claude models (Opus 4.7/4.8, Sonnet 5, Fable 5) aren't reachable through this project's Bedrock provider yet
- Broader unit test coverage (Bedrock/OpenAI-compat translation functions, built-in tools, config validation, session store) so the implementation can be self-verified without a real Bedrock or local-LLM connection

## v0.3.0

- Session list/resume: `GET /api/sessions`, TUI startup picker, Web UI session-switch modal
- `message.user` event so a resumed session replays the user's own prompts, not just the model's answers
- MCP server connection failures are non-fatal (one bad server no longer blocks daemon startup) and a dropped session reconnects automatically on the next call
- Expanded unit test coverage across the daemon, MCP client, and agent packages

## v0.2.0

- Core daemon: HTTP + SSE API over `agent.Loop`, with the TUI rewritten as a client of it
- Web UI served by the daemon (same HTTP/SSE API as the TUI)
- MCP client integration (stdio servers, `mcp__<server>__<tool>` namespacing, permission-gated)
- Skills (progressive disclosure via SKILL.md + the `Skill` tool)
- Background Task Manager (concurrent sub-agents, `task.spawned`/`task.status` events)

## v0.1.0

- Initial MVP: Bedrock + OpenAI-compat providers, built-in tools (read/write/edit/bash/glob/grep), Bubble Tea TUI, macOS/Windows packaging
