# Improvements

Findings from a code review on 2026-07-18. Items marked done were fixed on the spot and shipped in the version noted. The rest are candidates for later work.

## Shipped in v0.12.0

| Item | What changed |
|---|---|
| Conversation context lost on daemon restart | Session metadata now saves to a separate `<id>.meta.json` via `session.LoadAllFromDisk`, and `agent.Loop.RehydrateAll()` rebuilds model history and token usage from the event log at daemon start. |
| Local command replies leaking into history | Found during live verification of the restart work. Confirmation text from commands that never call the model (`/compact`, `/usage`) was being replayed as if the model had said it. `message.user` events now carry `"local": true` so rehydration skips them. |
| Startup logo | The TUI prints a "LOCALCODE" block banner at startup, the way opencode does. `--headless` skips it. |

## Shipped in v0.11.1

| Item | What changed |
|---|---|
| `localcode mcp add/remove` dropped unknown config.json fields | The whole file was being round tripped through the Config struct, so any key not in the struct (a typo, a field from a future version) silently vanished. Only the `mcp_servers` key is rewritten now, everything else stays as raw JSON. `remove` also no longer reformats the file when the name is not found. |
| Hook matcher matched partial names | A `"bash"` matcher also caught tools that merely contained bash, such as `mcp__server__run_bash`. Matchers are anchored to the full tool name now. Patterns like `"bash\|edit"` and `"mcp__github__.*"` work as before. |
| Compaction tokens missing from `/usage` | The summarization call is a billed API call, but it was not counted. |
| Compaction summary truncated at 1,024 tokens | Summaries of long sessions could get cut off mid sentence. Raised to 4,096, the default turn budget. |

## Remaining work, highest value first

1. ~~**`sh -c` dependency on Windows.**~~ Done in v0.23.0. Shell execution now resolves per OS in `internal/shell`: `sh` on PATH, then Git for Windows' `bash.exe` at known install paths, then `cmd /c`, and the bash tool's description warns the model when it is talking to `cmd`.
2. **Partially done: turn serialization.** The daemon does hold a per session busy flag and returns 409 while a turn is running, and since v0.24.0 a client that gets that 409 queues the message and retries on `turn.done` instead of erroring, so two clients on one session no longer interleave or lose a message. What is still missing is the same treatment for `/compact`, which can overlap a running turn. As of v0.37.0 both clients at least say so instead of doing nothing: a command typed mid-turn is refused out loud, since handing it to the running turn would deliver it to the model as chat.
3. ~~**Bash permission globs are too coarse.**~~ Done in v0.20.0. A bash command is split on `&&`, `||`, `;`, `|`, and newlines (quote aware), every segment has to earn `allow` on its own, any `deny` anywhere denies the whole line, and command substitution or output redirection never auto-allows.
4. **Hook timeout is not configurable.** The timeout is fixed at 30 seconds. A per hook `timeout` field would help. (Which shell runs hooks is resolved per OS as of v0.23.0, and as of v0.37.0 every shell command runs in a process group that is killed whole, so children no longer outlive it; only the timeout is still fixed.)
5. ~~**MCP is stdio only.**~~ Done in the Unreleased build. All three transports connect: `stdio`, `http` (streamable), and `sse`, chosen by an entry's `type` or inferred from whether it carries a url. `localcode mcp add --transport http|sse <name> <url>` registers one, `-H "Key: Value"` attaches an auth header, and `import-claude` brings Claude Code's remote servers across too. What's still missing is OAuth: a server that wants an interactive authorization flow rather than a static token can't be set up from here (the SDK exposes a hook for it, `StreamableClientTransport.OAuthHandler`).
6. **Partially done: switching to a long session shows only its tail.** A conversation opens at its end rather than replaying the whole log — measured at 1.63MB/751ms of client work for a 7,680-event session, against 0.08MB/4ms for the tail. v0.37.0 fixed what made that window far smaller than it looked: it counted streamed text fragments, so one long reply used it up and the conversation above it vanished. Replies that have finished now replay whole. What is still open is scrolling *back*: there is no "load earlier" control, so the beginning of a long conversation is only reachable from the session's `.jsonl`. The daemon already takes `?since=`, so the missing part is a client one.
7. ~~**`localcode mcp list` shows a static list.**~~ Done in the Unreleased build. `mcp list` now starts each registered server, handshakes, lists its tools, and reports `connection: OK (N tools)` or `connection: FAILED — <reason>` per entry, bounded by a 20s timeout; `--no-test` keeps the old instant listing. Querying a *running* daemon's `GET /api/mcp-servers` instead of starting a throwaway process is still an opening, and would be faster when a daemon happens to be up.
8. ~~**Compaction can fail when history already exceeds the context.**~~ Done in v0.37.0. The summarization request is trimmed to fit before it is sent, dropping whole messages from the oldest end (never opening on a tool result whose tool call went with them), and the prompt says what was left out. If the server refuses it anyway, it shrinks and re-sends rather than giving up. A turn refused for being too long summarizes and retries, then forces a trim, shrinking each round until it is accepted. Each round aims at two thirds of what the conversation *measures* rather than of the window, because the character estimate runs about 4x low on Korean and Japanese, so a budget taken from the window alone would decide a just-refused request already fits and cut nothing. Tool results are capped at a quarter of the window as they arrive, which closes the one case none of the above could reach: a single message larger than the window.
9. ~~**A local model's context window had to be configured by hand.**~~ Done in v0.38.0. The server is asked (`GET /v1/models`, `/props` on llama.cpp) instead of the window being guessed from the model's name, which for a local server cannot be right in principle: it serves whatever was loaded and is nearly always started with a smaller window than the model supports. Config still overrides, and a server that does not answer falls back to the guess. What is still open is the *output* cap: `max_tokens` cannot be discovered the same way, because no server reports how long an answer you want, so it stays a config value with a 4096 default.
10. **Config key order is not preserved.** When `localcode mcp` rewrites the file, top level keys come back alphabetically sorted. No data is lost, but diffs get noisy. Minor.
11. **`/usage` has no cross session or daily totals.** It reports one session. Daily or weekly reporting across sessions needs separate aggregation.
12. ~~**Deny rules can be evaded by shell quoting.**~~ Done in v0.33.4. Each command is matched both as written and with its shell quoting removed, and the stricter of the two answers wins — so `"curl" …`, `cu''rl …` and `c\url …` all meet a `curl *` deny, while unquoting can never widen an allow (that needs both readings to match).
13. **Dictation has no engine-side VAD.** Endpointing is energy over a 30ms frame: loud enough for long enough is speech, and about a second of quiet ends the utterance. That is enough to decide when grey text settles, and the cost of being wrong is a sentence committed a beat early rather than a wrong transcript. A model like Silero would be a better detector, at the price of another model to ship and load. Worth doing only once this one is observed failing.
14. **Partially done: Whisper partials re-read the whole utterance.** The model takes a window at a time, so provisional text is a fresh transcription about once a second rather than words arriving one by one, and more of it can be revised before it settles. v0.33.1 caps an utterance at 30 seconds, so the cost per pass is now bounded rather than growing with the time spent talking. What is still open is the waste itself: keeping already-settled text and re-reading only the tail would cut most of it.
15. **File tools are not confined to the workspace.** `read`, `write` and `edit` follow symlinks, and the permission subject is the path exactly as the model wrote it — so an allow rule meant to cover the workspace is satisfied by a symlink pointing out of it, or by a path containing `../` matched verbatim. There is no workspace-root check at the tool layer; the only confinement is whatever the permission glob happens to express. Fixing it means resolving the path first and refusing anything that lands outside the workspace, which is a behaviour change (some setups legitimately read outside it) rather than a patch, so it wants deciding rather than doing. Largest item left from the 2026-08 review.
16. **A `pre_tool_use` hook that hangs is killed at 30s and the tool then runs.** Fail-open is the current, documented behaviour, and it is the wrong default for a hook whose job is to block something. Needs a per-hook `timeout` and a `fail_closed` flag, plus a decision about which way the default goes.
17. **Compaction summaries sit in the event log as plain text.** v0.12.0 started storing the full summary in the `compacted` event so restarts can restore it. If a session contained sensitive material, the summary of it now lives in the log file too. Worth reviewing log file permissions and retention against that.

## UI ideas

### Web UI

| Idea | Why |
|---|---|
| ~~Markdown rendering~~ | Done in the Unreleased build. A small dependency-free renderer handles headers, bold/italic, inline code, fenced code blocks, lists, blockquotes, links, and rules, in both the Web UI and the GUI window. No syntax highlighting inside code blocks yet — still an opening if that matters more than the current plain monospace block. |
| Collapsible tool call cards | Show tool input and output as a folded card that expands on click. Long sessions become much easier to follow. |
| Diff viewer | Render `edit` and `write_file` results as a before and after diff. |
| ~~"Always allow" on permission prompts~~ | Done in v0.20.0. Prompts now offer allow once, allow for session, and always allow, the last of which writes the matching rule into config.json. |
| `/usage` visualization | Bars for tokens per model, a gauge for context use. |
| ~~No sign that another session finished~~ | Done in v0.40.0. The per-session light is green now (matching the status light under the prompt), blinks while the model is working, and goes steady for a reply that arrived while you were elsewhere, clearing when you open the session. |
| Session search and filter | The session list in the left panel needs title (and workspace) search once it gets long. |
| ~~Scroll control~~ | Half done in v0.31.0, on the TUI side: the viewport only auto-follows the bottom when it was already there before an update, instead of forcing it on every event. The Web UI equivalent and a jump-to-bottom button are still open. |
| ~~Workspace is process-wide~~ | Done in v0.39.0, the way this entry said it would have to be: relative paths resolve per session at the tool layer and `os.Chdir` is gone. Each turn carries its session's directory on the context; read/write/edit join onto it, glob and grep search inside it, bash gets it as `cmd.Dir`. Two clients on one daemon can now work in two projects, and only *this* session's own turn blocks its own switch. |
| ~~Per-session workspace~~ | Done in the Unreleased build, and per-session for real as of v0.39.0. Selecting a session works in the directory that session belongs to; the workspace is no longer shared underneath, so one client switching sessions no longer moves another's. |
| Dark and light theme toggle | Plus a responsive layout for mobile. |
| MCP server status | A connected or failed dot next to each MCP server in the right panel, with a reconnect button. |

### TUI

| Idea | Why |
|---|---|
| Markdown and code block rendering | A renderer such as glamour would make replies far easier to read. |
| Session picker inside the TUI | Today you type a number at a plain terminal prompt before the TUI starts. A Bubble Tea list with arrow key selection would feel native. |
| ~~Tool progress display~~ | Done in v0.25.0, extended in v0.32.11. An animated line below the prompt box names the running tool, the queue depth, and the background-task count, and clears at the turn boundary; each tool call also gets a transcript line that survives the turn. Elapsed time is still not shown. |
| Context gauge | Turn the percentage in the status line into a colored bar, yellow at 70%, red at 85%. |
| History scroll and search | Search earlier output in long sessions with the `/` key. |

### Both clients

| Idea | Why |
|---|---|
| Serve `/help` from the daemon | The TUI and Web UI each hardcode their own help string, so adding a command means editing two places. A single source such as `GET /api/commands/help` would keep them in sync. |
| ~~Mixed Korean and English error messages~~ | Done in v0.13.0. All program output is English now, and the documentation followed in v0.19.0. |
