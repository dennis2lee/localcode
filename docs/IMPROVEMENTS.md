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
2. **Partially done: turn serialization.** The daemon does hold a per session busy flag and returns 409 while a turn is running, and since v0.24.0 a client that gets that 409 queues the message and retries on `turn.done` instead of erroring, so two clients on one session no longer interleave or lose a message. What is still missing is the same treatment for `/compact`, which can overlap a running turn.
3. ~~**Bash permission globs are too coarse.**~~ Done in v0.20.0. A bash command is split on `&&`, `||`, `;`, `|`, and newlines (quote aware), every segment has to earn `allow` on its own, any `deny` anywhere denies the whole line, and command substitution or output redirection never auto-allows.
4. **Hook timeout is not configurable.** The timeout is fixed at 30 seconds. A per hook `timeout` field would help, and killing the process group would make sure children spawned by the shell get cleaned up. (Which shell runs hooks is resolved per OS as of v0.23.0; only the timeout is still fixed.)
5. ~~**MCP is stdio only.**~~ Done in the Unreleased build. All three transports connect: `stdio`, `http` (streamable), and `sse`, chosen by an entry's `type` or inferred from whether it carries a url. `localcode mcp add --transport http|sse <name> <url>` registers one, `-H "Key: Value"` attaches an auth header, and `import-claude` brings Claude Code's remote servers across too. What's still missing is OAuth: a server that wants an interactive authorization flow rather than a static token can't be set up from here (the SDK exposes a hook for it, `StreamableClientTransport.OAuthHandler`).
6. ~~**`localcode mcp list` shows a static list.**~~ Done in the Unreleased build. `mcp list` now starts each registered server, handshakes, lists its tools, and reports `connection: OK (N tools)` or `connection: FAILED — <reason>` per entry, bounded by a 20s timeout; `--no-test` keeps the old instant listing. Querying a *running* daemon's `GET /api/mcp-servers` instead of starting a throwaway process is still an opening, and would be faster when a daemon happens to be up.
7. **Compaction can fail when history already exceeds the context.** If the history is right at the model limit, the summarization request itself can fail. A truncation fallback that drops the oldest turns would make auto compaction more robust.
8. **Config key order is not preserved.** When `localcode mcp` rewrites the file, top level keys come back alphabetically sorted. No data is lost, but diffs get noisy. Minor.
9. **`/usage` has no cross session or daily totals.** It reports one session. Daily or weekly reporting across sessions needs separate aggregation.
10. **Dictation has no engine-side VAD.** Endpointing is energy over a 30ms frame: loud enough for long enough is speech, and about a second of quiet ends the utterance. That is enough to decide when grey text settles, and the cost of being wrong is a sentence committed a beat early rather than a wrong transcript. A model like Silero would be a better detector, at the price of another model to ship and load. Worth doing only once this one is observed failing.
11. **Whisper partials re-read the whole utterance.** The model takes a window at a time, so provisional text is a fresh transcription about once a second rather than words arriving one by one, and more of it can be revised before it settles. A long utterance also means a longer window each pass. Capping the window, or keeping already-settled text and only re-reading the tail, would bound both.
12. **Compaction summaries sit in the event log as plain text.** v0.12.0 started storing the full summary in the `compacted` event so restarts can restore it. If a session contained sensitive material, the summary of it now lives in the log file too. Worth reviewing log file permissions and retention against that.

## UI ideas

### Web UI

| Idea | Why |
|---|---|
| ~~Markdown rendering~~ | Done in the Unreleased build. A small dependency-free renderer handles headers, bold/italic, inline code, fenced code blocks, lists, blockquotes, links, and rules, in both the Web UI and the GUI window. No syntax highlighting inside code blocks yet — still an opening if that matters more than the current plain monospace block. |
| Collapsible tool call cards | Show tool input and output as a folded card that expands on click. Long sessions become much easier to follow. |
| Diff viewer | Render `edit` and `write_file` results as a before and after diff. |
| ~~"Always allow" on permission prompts~~ | Done in v0.20.0. Prompts now offer allow once, allow for session, and always allow, the last of which writes the matching rule into config.json. |
| `/usage` visualization | Bars for tokens per model, a gauge for context use. |
| Session search and filter | The session list in the left panel needs title (and workspace) search once it gets long. |
| ~~Scroll control~~ | Half done in v0.31.0, on the TUI side: the viewport only auto-follows the bottom when it was already there before an update, instead of forcing it on every event. The Web UI equivalent and a jump-to-bottom button are still open. |
| ~~Per-session workspace~~ | Done in the Unreleased build. Selecting a session switches the daemon's working directory to the one that session was created in, announced with a `[workspace]` line. Still process-wide underneath: two clients on one daemon share a workspace, so one of them switching sessions moves the other's too. |
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
