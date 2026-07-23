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

1. **`sh -c` dependency on Windows.** The bash tool, hooks, and `` !`shell` `` expansion in custom commands all run through `sh -c`. On the Windows MSI build they all fail unless something like Git Bash is on PATH. Needs a `cmd /c` or PowerShell fallback when `runtime.GOOS == "windows"`.
2. **No server side turn lock.** Two messages arriving for the same session at nearly the same time can interleave the history. v0.18.0 added a client side prompt queue in both the TUI and Web UI, which covers the common case of one person typing ahead, but two different clients on the same session can still race. A per session lock that returns 409 or queues while a turn is running would close it. `/compact` overlapping a running turn has the same problem.
3. **Bash permission globs are too coarse.** An allow rule of `"git *"` also lets `git status && rm -rf ~` through. The command string should be split on shell syntax so each segment matches on its own. At minimum, fall back to ask when `&&`, `;`, or `|` appears.
4. **Hook timeout and shell are not configurable.** The timeout is fixed at 30 seconds. A per hook `timeout` field would help, and killing the process group would make sure children spawned by `sh -c` get cleaned up.
5. **MCP is stdio only.** HTTP and SSE transport servers cannot be attached yet, unlike Claude Code. Room for something like `localcode mcp add --transport http <name> <url>`.
6. **`localcode mcp list` shows a static list.** It prints what is registered in config, but whether a server actually starts is only known once the daemon runs. When a daemon is up, querying `GET /api/mcp-servers` and showing connection state alongside would be more useful.
7. **Compaction can fail when history already exceeds the context.** If the history is right at the model limit, the summarization request itself can fail. A truncation fallback that drops the oldest turns would make auto compaction more robust.
8. **Config key order is not preserved.** When `localcode mcp` rewrites the file, top level keys come back alphabetically sorted. No data is lost, but diffs get noisy. Minor.
9. **`/usage` has no cross session or daily totals.** It reports one session. Daily or weekly reporting across sessions needs separate aggregation.
10. **Compaction summaries sit in the event log as plain text.** v0.12.0 started storing the full summary in the `compacted` event so restarts can restore it. If a session contained sensitive material, the summary of it now lives in the log file too. Worth reviewing log file permissions and retention against that.

## UI ideas

### Web UI

| Idea | Why |
|---|---|
| Markdown rendering with code highlighting | Model replies render as plain text today. For a coding tool, code block highlighting is the single biggest readability win. Prefer a light library that bundles without an external CDN. |
| Collapsible tool call cards | Show tool input and output as a folded card that expands on click. Long sessions become much easier to follow. |
| Diff viewer | Render `edit` and `write_file` results as a before and after diff. |
| "Always allow" on permission prompts | Only one time allow and deny exist today. An always allow button that writes the matching `permission` rule into config would save a lot of clicking. |
| `/usage` visualization | Bars for tokens per model, a gauge for context use. |
| Session search and filter | The session list in the right panel needs title search once it gets long. |
| Scroll control | Stop auto scrolling when the user scrolls up mid stream, and show a jump to bottom button. |
| Dark and light theme toggle | Plus a responsive layout for mobile. |
| MCP server status | A connected or failed dot next to each MCP server in the right panel, with a reconnect button. |

### TUI

| Idea | Why |
|---|---|
| Markdown and code block rendering | A renderer such as glamour would make replies far easier to read. |
| Session picker inside the TUI | Today you type a number at a plain terminal prompt before the TUI starts. A Bubble Tea list with arrow key selection would feel native. |
| Tool progress display | Running tool name with a spinner, and elapsed time on completion. |
| Context gauge | Turn the percentage in the status line into a colored bar, yellow at 70%, red at 85%. |
| History scroll and search | Search earlier output in long sessions with the `/` key. |

### Both clients

| Idea | Why |
|---|---|
| Serve `/help` from the daemon | The TUI and Web UI each hardcode their own help string, so adding a command means editing two places. A single source such as `GET /api/commands/help` would keep them in sync. |
| ~~Mixed Korean and English error messages~~ | Done in v0.13.0. All program output is English now, and the documentation followed in v0.19.0. |
