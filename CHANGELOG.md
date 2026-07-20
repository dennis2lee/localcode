# Changelog

## v0.16.0

- Bedrock: support the `[1m]` 1M-context suffix on `profiles.<name>.model` (e.g. `"us.anthropic.claude-sonnet-4-6[1m]"`), matching the shorthand Claude Code's own model settings use. localcode strips the suffix before sending the real model ID and passes Anthropic's 1M-context beta flag via Bedrock's `AdditionalModelRequestFields`. **Not verified against a real Bedrock account** — the beta flag name is carried over from Anthropic's direct-API convention, not confirmed against AWS's own docs; see MODELS.md if it doesn't work for you.

## v0.15.0

- TUI status line: replace the "(tab to switch: a → b → c)" hint next to `agent: <name>` with the model ID the current agent's profile actually resolves to (`agent: explore  ·  model: qwen3-30b-a3b`) — `GET /api/agents` now reports each agent's resolved model, so this needed no client-side config access.
- Docs (MODELS.md): add a troubleshooting entry for `ValidationException: ... Your account is not authorized to invoke this API operation` on Bedrock — usually a malformed model ID (a `[1m]` "1M context" suffix copied from a UI label isn't part of the real model ID) or a model not yet supported by the Converse API.

## v0.14.0

- **Fix a TUI crash on repeated Tab presses**: `strings: illegal use of non-zero Builder copied by value`. Root cause — `Model.transcript` was a `strings.Builder`, but `Model.Update` has a value receiver (bubbletea's Program copies the whole model on every call), and `strings.Builder` embeds a self-referential pointer it uses to detect illegal copies. Once the transcript had any content, the next copy-then-write panicked. `transcript` is now a plain string, which has no such restriction. Added a regression test that drives 50 rapid Tab presses after seeding the transcript.
- **Delete sessions, one or all at once.** Previously the TUI had no session-management UI at all (only resume-by-number or start-new), and the Web UI could only delete one session at a time.
  - New `DELETE /api/sessions` bulk-delete endpoint (`session.Store.DeleteAll`, `client.DeleteAllSessions`) — refuses (409) if *any* session has a turn in progress, so a bulk delete never partially succeeds.
  - TUI: the startup session picker now supports `d<N>` (delete session N, e.g. `d1`) and `da` (delete every session, with a typed "yes" confirmation).
  - Web UI: a **delete all** button next to **+ new session** in the Sessions panel.

## v0.13.0

- **All program-facing output is now in English** — TUI (`internal/tui`), Web UI (`internal/daemon/static/index.html`), CLI (`localcode login`/`localcode mcp`/session picker), and agent-loop local command replies (`/skill`, `/memory`, `/config`, `/compact`, `/usage`) no longer mix in Korean strings. Documentation files (README/USAGE/MODELS/CHANGELOG/IMPROVEMENTS) are unaffected — this covers only what the running program prints or renders.
- Fix: switching agents (Tab in the TUI, the dropdown in the Web UI, or `/agent <name>`) no longer writes a permanent "switched to X" line into the transcript on every switch. Both clients already show the current agent in a persistent status line; the transcript write was pure duplication, and on the TUI (where Tab is the primary way to switch) it meant the screen filled up with one throwaway line per keypress.
- TUI layout: the current-agent status line now lives directly below the prompt input box instead of above the transcript, and the input box is now visually bordered top and bottom so its boundary is unambiguous.
- Bedrock: when the AWS SDK's default credential chain finds nothing and falls all the way through to an EC2 instance-metadata lookup (the exact failure you get on a non-EC2 machine when `providers.<name>.profile` isn't set to a working SSO/login profile), the error now includes an actionable hint pointing at `providers.<name>.profile` and `AWS_PROFILE` instead of just the raw SDK error dump. See [MODELS.md](MODELS.md) for the full troubleshooting entry.
- Docs: added a concrete `config.json` example for pointing an `openai-compat` provider at a remote API-key-gated proxy (the `api_key` field already existed and works exactly like opencode's `@ai-sdk/openai-compatible` `apiKey` — this was a documentation gap, not a missing feature).

## v0.12.5

- Change the startup banner's tagline from "Local & cloud LLM coding agent" to "Multi LLM coding agent".

## v0.12.4

- Move the startup banner's tagline onto the same line as the last row of stars (next to the third `✦`), matching the original layout, instead of a separate line below the wordmark.

## v0.12.3

- Redesign the startup banner: a small star-scattered "localcode" wordmark instead of the block-letter version, with an English tagline ("Local & cloud LLM coding agent").

## v0.12.2

- Restructure README.md and USAGE.md for readability — no content removed, only reorganized:
  - README.md's single wall-of-text feature paragraph is now a scannable "핵심 기능" bullet list grouped by theme (providers/auth, safety, multi-agent, project context, conversation management, Web UI, MCP management), plus a proper "문서" links section.
  - USAGE.md's 22 flat, ungrouped `##` sections are now organized into 8 numbered parts (시작하기/설정/프로젝트 컨텍스트/명령어/세션 관리/Web UI/에이전트와 자동화/알려진 제약), with a table of contents at the top. Verified every one of USAGE.md's 45 internal links and every cross-file link from README.md/MODELS.md/INSTALL.md still resolves to a valid anchor — heading text (and therefore its anchor) was never changed, only its nesting level.
- No functional/behavioral changes. Tab-key agent cycling, the `plan`/`build`/`explore`/`review` agent presets (opencode's Plan/Build mode equivalent), and the startup ASCII banner were all already implemented in earlier releases (v0.9.0–v0.12.0) — verified still working, nothing new to ship there.

## v0.12.1

- Rename `/cost` to `/usage` — the command only ever showed raw token counts (deliberately no dollar figures), so "cost" was misleading terminology; the underlying behavior is unchanged.

## v0.12.0

- **Sessions survive a daemon restart.** Previously a restart wiped the session list and all conversation context — the event log was persisted, but nothing restored it. Session metadata (agent/title/visible/parent) is now written to a `<id>.meta.json` sidecar alongside each session's `<id>.jsonl`, `session.LoadAllFromDisk` restores every session pair at startup, and a new `agent.Loop.RehydrateAll()` replays each session's event log back into the in-memory conversation history and `/cost` token totals the model needs to actually remember anything, not just the transcript a client re-renders. A session that fails to restore (corrupt metadata) is skipped with a logged warning rather than blocking every other session's restore.
- Fix (caught during this work): rehydrating history from the event log could surface a local-only command's confirmation text (e.g. "대화가 압축되었습니다" from `/compact`, or `/cost`'s own answer) as if the model had said it, since those commands log their reply through the same event types as a real turn. `message.user` events are now tagged `"local": true` when they never reach the model, and rehydration skips both the command and its paired reply.
- `localcode` now prints a "LOCALCODE" startup banner (opencode-style) before the interactive TUI takes the screen, in both the default embedded mode and `--server`-attached mode. `--headless` skips it, since that's meant to run unattended.

## v0.11.1

- Fix: `localcode mcp add/add-json/remove` no longer drops config.json keys it doesn't recognize. The previous implementation round-tripped the entire file through the Config struct, so any field outside the known schema (a typo, a future version's field) was silently deleted on save. Now only the `mcp_servers` key is rewritten (`config.UpdateMCPServersInFile`); everything else is preserved as raw JSON. `remove` also no longer rewrites/reformats the file when the name isn't found.
- Fix: hook `matcher` regexes are now anchored to the full tool name. `"bash"` previously matched any tool whose name merely contained "bash" (e.g. `mcp__server__run_bash`); it now matches exactly the `bash` tool. Alternation (`"bash|edit"`) and explicit patterns (`"mcp__github__.*"`) work as before.
- Fix: the compaction summarization call's own token usage now counts toward `/cost` (it's a billed API call like any other, previously invisible in the totals).
- Fix: compaction summaries were capped at 1,024 output tokens, which could truncate the summary of a long session mid-sentence — the cap is now 4,096 (the default turn budget).

## v0.11.0

- `localcode mcp` CLI subcommand (Claude Code's `claude mcp` equivalent): `add [-e KEY=VALUE]... [-s global|project] <name> -- <command> [args...]`, `add-json`, `list`, `get <name>`, and `remove [-s global|project] <name>` manage `mcp_servers` entries in `~/.localcode/config.json` (default) or `./.localcode/config.json` (`-s project`) without hand-editing JSON. `list`/`get` read both scopes and report which one a server actually lives in (project overrides global on name collision, matching runtime merge semantics); `remove` requires an explicit `-s` when a name exists in both scopes rather than guessing. Runs standalone like `localcode login`, no daemon required — edits take effect on the daemon's next start. Added `config.LoadFile`/`config.SaveFile` to read/write a single config file for editing, and `omitempty` on `providers`/`profiles`/`agents`/`default_profile`/`max_concurrent_tasks` so a freshly-created config (e.g. one that only has `mcp_servers` so far) doesn't get cluttered with `null`/`0` entries.

## v0.10.0

- Hooks (`hooks` in config.json): Claude Code-style lifecycle hooks — `pre_tool_use`, `post_tool_use`, `user_prompt_submit`, `stop`, `session_start` — each a list of `{"matcher": regex-on-tool-name, "command": shell-command}` run with the event payload as JSON on stdin. `pre_tool_use` and `user_prompt_submit` can block (exit code 2, reason on stderr, or `{"decision":"block","reason":"..."}` on stdout); `post_tool_use`/`stop`/`session_start` are fire-and-forget/informational since they run after the fact. Independent of and layered before the existing `permission` allow/ask/deny system — a `pre_tool_use` hook that allows a call still goes through `permission` afterward. Per-event config, not additive across project/global merge (matches how other config sections merge).
- `/compact [instructions]`: on-demand conversation compaction, without waiting for the 80% auto-compact threshold. An optional trailing instruction string overrides the default summarization prompt for that one compaction. Shares its core logic with auto-compaction but surfaces failures to the user instead of silently skipping.
- `/cost`: shows cumulative token usage broken down by model for the current session — input/output tokens and call count per model, plus a grand total. Deliberately token-counts only, no dollar figures. Distinct from the existing context-window status line, which reflects only the most recent call's usage (a snapshot for auto-compact triggering), whereas `/cost` sums every API call made in the session (since each call is billed for its full resent history).

## v0.9.0

- Token usage tracking: all three providers (Bedrock, Anthropic direct, OpenAI-compat) now report input/output token usage per turn (`provider.EventUsage`), surfaced as a new `usage` session event with context-window fill percentage (via a new `internal/modelinfo` best-effort max-context lookup) and tokens-per-second.
- Auto-compaction: once a session's context usage crosses 80%, the conversation history is summarized by the model in place and replaced with the summary before the next turn — toggle with `config.json`'s `auto_compact_enabled` or live via `/config auto_compact on|off`. A new `compacted` event marks when it happens.
- `/config` command: view or toggle `auto_compact` and `show_tps` (tokens-per-second display) live, process-wide, without restarting; `GET /api/settings` reports current values for clients that just connected.
- Session rename (`Session.Title`, `Store.SetTitle`, `POST /api/sessions/{id}/rename`) and delete (`Store.Delete`, `DELETE /api/sessions/{id}`, refuses while a turn is in-flight) — both exposed as buttons in the Web UI's session list.
- `mcp.Manager.Servers()` + `GET /api/mcp-servers`: lists currently-connected MCP server names, shown in the Web UI's right panel.
- File uploads (`POST /api/sessions/{id}/uploads`) for the Web UI's new drag-and-drop attachment support — saves to `~/.localcode/uploads/<session-id>/<filename>` and inserts a path reference into the prompt for the model to read with its own file tools.
- Web UI: persistent right-panel session list (switch/rename/delete) replacing the old modal picker, a connected-MCP-servers list below it, drag-and-drop file attachment on the prompt box, and a status line under the prompt showing current agent/model, context-window fill %, TPS, and a pulsing "communicating" indicator that flashes on completion.

## v0.8.0

- Fine-grained permission rules (opencode-style): `config.json`'s new `permission` field maps a tool name (or `"*"` fallback) to either a flat `"allow"/"ask"/"deny"` or an ordered array of `{"match": glob, "decision": ...}` rules matched against the call's subject (the bash command string, or a file path for `write_file`/`edit`) — last match wins. Lets safe commands (`git *`) run without a prompt while dangerous ones (`rm *`) are blocked outright, and can even restrict tools that previously never asked (e.g. deny reading `*.env`). No `permission` config at all preserves exactly today's behavior.

## v0.7.0

- `localcode login bedrock`: native AWS IAM Identity Center (SSO) device-authorization flow — no AWS CLI required. Writes the same artifacts the AWS CLI does (`~/.aws/sso/cache/<sha1(start-url)>.json`, a `[profile ...]` block in `~/.aws/config`), so the default AWS credential chain (which `provider.Bedrock` already relies on) picks it up automatically. `config.json`'s `providers.<name>.profile` selects the named profile.
- `localcode login anthropic`: saves a personal Anthropic API key (from console.anthropic.com) to `~/.localcode/credentials.json` (mode 0600), and a new `anthropic` provider type talks directly to `api.anthropic.com` — usage-billed separately from a claude.ai Pro/Max subscription, not a substitute for one. Unlocks the newest Claude models (Opus 4.7/4.8, Sonnet 5, Fable 5) that Bedrock's Converse API doesn't yet expose.
- Explicitly does **not** implement reusing a claude.ai Pro/Max subscription itself (what Claude Code's own login does) — that requires Anthropic's undocumented, Claude-Code-only OAuth client, and reimplementing it in a third-party tool would risk violating Anthropic's terms of service.

## v0.6.1

- Fix: custom-command expansion no longer re-scans substituted content for further directives — a `!`shell`` command's output or an argument value containing an `@path` (e.g. `@/etc/passwd`) is now left literal instead of being read and inlined. Expansion is a single left-to-right pass; `$1`/`$ARGUMENTS` still substitute into the shell command itself.
- Fix: auto-memory `MEMORY.md` index no longer emits invalid UTF-8 when the 25KB byte cap lands in the middle of a multi-byte rune (CJK/emoji); the incomplete trailing bytes are trimmed.

## v0.6.0

- Claude Code-style auto memory: model-written notes persisted across sessions under `~/.localcode/projects/<slug>/memory/` (slug derived from the git repo root, shared across worktrees), with `MEMORY.md` as the index (loaded into the system prompt every session, capped at 200 lines/25KB matching Claude Code's own limit) and topic files read on demand via the model's existing file tools — no dedicated Memory tool needed. Toggle with `"auto_memory_enabled": false` in config.json (default on). `/memory` local command shows the directory and current index.
- `AGENTS.md`/`CLAUDE.md` rules files now support Claude Code's `@path/to/import` syntax: recursive imports up to 4 hops, relative paths resolved against the importing file's directory, `~/` for home-relative, references inside fenced code blocks or inline code spans left literal.

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
