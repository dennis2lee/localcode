# Improvements

## Summary

Open and partial work remains in security, orchestration, and client behavior. The numbered records below retain completed fixes and their release versions.

| Area | Recorded remaining work |
|---|---|
| Security | Network destination controls and structural source provenance in summaries |
| Orchestration | Resume, loops, per-item pipelines, child-session retention, and permission feasibility |
| Agent selection | Direct selection of dynamic Smart Agent specialists |
| Client behavior | Rewind recovery, multiline TUI completion, and the UI items below |

Original review: 2026-07-18. Item numbers and recorded version statuses are preserved.

## Shipped in v0.12.0

| Item | Change |
|---|---|
| Conversation context lost on daemon restart | Session metadata persists in `<id>.meta.json`. `session.LoadAllFromDisk` loads it at startup. `agent.Loop.RehydrateAll()` rebuilds model history and token usage from the event log. |
| Local command replies included in model history | Replies from `/compact`, `/usage`, and other commands that do not call the model are excluded from replay. Their `message.user` events carry `"local": true`. The defect was found during live restart verification. |
| Startup logo | TUI startup prints the LOCALCODE banner. `--headless` suppresses it. |

## Shipped in v0.11.1

| Item | Change |
|---|---|
| `localcode mcp add/remove` dropped unknown config fields | Only `mcp_servers` is rewritten. Other values remain raw JSON. Removing a nonexistent entry leaves formatting unchanged. |
| Hook matcher accepted partial names | Matchers now apply to the full tool name. A `"bash"` matcher no longer matches `mcp__server__run_bash`. Patterns such as `"bash\|edit"` and `"mcp__github__.*"` remain supported. |
| Compaction tokens missing from `/usage` | Token accounting now includes the summarization API call. |
| Compaction summary truncated at 1,024 tokens | Summary output limit raised to 4,096 tokens, the default turn budget. |

## Remaining work, highest value first

Completed findings remain in this list to preserve item numbers and release history.

1. **Windows shell execution. Done in v0.23.0.**

   * `internal/shell` selects `sh` on PATH, then Git for Windows' `bash.exe` at known paths, then `cmd /c`.
   * The bash tool description identifies use of `cmd`.

2. **Turn serialization. Partially done.**

   * The daemon's per-session busy flag rejects concurrent turns with HTTP 409.
   * Since v0.24.0, clients queue a rejected message and retry on `turn.done`.
   * Remaining: `/compact` can overlap a running turn.
   * Since v0.37.0, both clients explicitly refuse commands entered during a turn. Commands are not queued as model input.

3. **Bash permission matching. Done in v0.20.0.**

   * Quote-aware splitting on `&&`, `||`, `;`, `|`, and newlines.
   * Each segment requires an allow decision.
   * Any deny decision rejects the complete command.
   * Command substitution and output redirection never receive automatic approval.

4. **Configurable hook timeout. Open.**

   * Fixed timeout: 30 seconds.
   * Proposed setting: per-hook `timeout`.
   * Completed prerequisites: OS-specific shell selection in v0.23.0, process-group termination in v0.37.0, and session-workspace execution in v0.62.1.

5. **Remote MCP transports. Done in v0.29.0; OAuth remains open.**

   * Supported transports: `stdio`, streamable `http`, and `sse`.
   * Selection: explicit `type`, or URL-based inference.
   * Registration: `localcode mcp add --transport http|sse <name> <url>`.
   * Static authentication headers: `-H "Key: Value"`.
   * `import-claude` imports remote server entries.
   * Remaining: interactive OAuth setup. The SDK exposes `StreamableClientTransport.OAuthHandler`.

6. **Long-session replay. Partially done.**

   * Sessions open with recent events rather than the complete log.
   * Recorded measurement for 7,680 events: 1.63 MB and 751 ms for full replay; 0.08 MB and 4 ms for recent events.
   * v0.37.0 changed replay accounting so completed replies remain whole instead of consuming the window as individual streamed fragments.
   * Remaining: a client control to load earlier events. Earlier content is otherwise available only in the session's `.jsonl`.
   * The daemon already supports `?since=`.

7. **MCP connection checks. Done in v0.28.0.**

   * `localcode mcp list` starts each server, performs the handshake, lists tools, and reports success with a tool count or failure with a reason.
   * Per-server timeout: 20 seconds.
   * `--no-test` retains the static listing.
   * Remaining optimization: query a running daemon's `GET /api/mcp-servers` instead of starting a temporary server process.

8. **Compaction above the context limit. Done in v0.37.0.**

   * Summarization input is trimmed before submission.
   * Trimming removes complete oldest messages and avoids an initial tool result without its call.
   * The prompt identifies omitted content.
   * A rejected summarization request is reduced and retried.
   * A normal turn rejected for context length is summarized and retried, then forcibly trimmed if needed.
   * Each reduction targets two thirds of the measured conversation size. The character estimate was approximately four times too low for Korean and Japanese.
   * Incoming tool results are capped at one quarter of the context window.

9. **Local-model context discovery. Done in v0.38.0.**

   * Discovery uses `GET /v1/models` and llama.cpp's `/props`.
   * Explicit configuration takes precedence.
   * A server that does not report its window uses the model-name estimate.
   * Remaining: `max_tokens` stays configurable with a 4,096 default. Servers do not report the desired answer length.

10. **Config key ordering. Open; minor.**

    * `localcode mcp` sorts top-level keys when rewriting configuration.
    * Values are preserved, but diffs include ordering changes.

11. **Cross-session usage totals. Open.**

    * `/usage` reports one session.
    * Daily or weekly totals require separate aggregation.

12. **Quoted shell commands bypassing deny rules. Done in v0.33.4.**

    * Commands are matched both as written and after shell quoting is removed.
    * The stricter decision applies.
    * A `curl *` deny matches `"curl"`, `cu''rl`, and `c\url` spellings.
    * Both representations must match before quoting removal can preserve an allow decision.

13. **Engine-side dictation VAD. Closed by removal in v0.53.0.**

    * Dictation, the speech engine, and the Windows installer model download were removed.

14. **Whisper partials rereading complete utterances. Closed by removal in v0.53.0.**

    * Dictation, the speech engine, and the Windows installer model download were removed.

15. **Workspace boundary checks for file tools. Done in v0.55.0; extended through v0.63.0.**

    | Version or review | Change |
    |---|---|
    | v0.54.0 | With `smart_agent` enabled, paths outside the session workspace changed from allow to ask. The check included `..`. `read_file` gained a permission subject. |
    | v0.55.0 | Both sides of the comparison use resolved physical paths. New paths use their closest existing ancestor. Unresolvable paths are treated as outside the workspace. |
    | Round 10 | Missing components are checked with `Lstat`. Dangling symlinks are followed instead of being classified by their containing directory. A bounded hop count handles cycles. |
    | v0.63.0 | Boundary checks became independent of `smart_agent`. `grep` and `glob` gained permission subjects. External reads and writes gained separate switches. Approvals can cover a directory for the session or all locations. |

    * `permission-skip-tools` provides a way to reduce repeated prompts without disabling the other permission controls.

16. **Hook timeout failure policy. Open.**

    * A `pre_tool_use` hook terminated after 30 seconds does not block the tool.
    * Current documented behavior: fail open.
    * Required decisions: per-hook `timeout`, a `fail_closed` option, and the default failure policy.

17. **Sensitive content in compaction logs. Open.**

    * Since v0.12.0, the `compacted` event stores the full summary for restart recovery.
    * A summary may retain sensitive session content.
    * Review needed: log permissions and retention.

18. **Windows desktop caption. Done in v0.44.0; working behavior confirmed in v0.45.1.**

    * Custom framing uses `WM_NCCALCSIZE`, `WM_NCHITTEST`, and page-rendered controls.
    * Follow-up fixes cleared `WS_CAPTION` and routed move/resize requests from the page because WebView2's child window receives mouse input.
    * `LOCALCODE_TITLEBAR=1` restores the system frame.
    * `internal/gui/chrome_test.go` checks shared geometry.
    * Limitation: the development machine is a Mac. CI builds the Windows binary without opening a window. Hit testing still requires Windows runtime verification.

19. **Whisper hallucination filtering dependent on local VAD. Closed by removal in v0.53.0.**

    * Dictation, the speech engine, and the Windows installer model download were removed.

20. **Update rollback and installer verification. Mostly closed in v0.50.0; handoff since v0.86.0; remaining gaps open.**

    * Writable binary installations are unpacked beside the existing binary, execution-checked, and renamed into place.
    * v0.53.0 restarted into the replacement. Since v0.86.0 the terminal hands the daemon over instead: `/update` starts the new binary on the listening socket and the old daemon finishes its turns. Since v0.85.0 the update is also applied at startup (`auto_update` turns it off), and since v0.88.0 that includes Windows, by a handoff before anything is served.
    * `/usr/bin` installations receive an `apt install` command.
    * `LocalCode.app` is replaced as a bundle.
    * Windows: `/update` and the startup update install from the zip on both architectures, without elevation. A writable install renames the running `.exe` to `.old` and takes its name; an install under Program Files is staged under `%LocalAppData%\localcode\bin` and the successor runs from there. The MSI runs only from the settings window's button. Windows arm64 has no MSI.
    * No automatic rollback. On Windows the `.old` copy beside a writable install is a manual one; it is removed by the next update, never at startup. Elsewhere recovery requires a previous release installer or archive.
    * The staged copy under `%LocalAppData%` is written by localcode, not the installer, and the MSI's uninstall does not remove it.
    * Download integrity uses the asset SHA-256 recorded by GitHub. This is not a signature. Release trust depends on the repository and TLS.
    * The `msiexec` path has not been run from the Mac development machine. `internal/update` tests use a controlled server. The startup handoff on Windows is verified in parts on the Windows CI runner (two-process handoff, rename install, window proxy); the assembled path needs a release server and has not been run end to end.

21. **Published speech engines for macOS and Linux. Closed by removal in v0.53.0.**

    * Dictation, the speech engine, and the Windows installer model download were removed.

22. **Partial output from a slow remote speech engine. Closed by removal in v0.53.0.**

    * Dictation, the speech engine, and the Windows installer model download were removed.

23. **Distinguishing stalled and completed turns. Open.**

    * v0.48.0 added bounded continuation for models that describe a next step without executing it.
    * Known model families receive an instruction and a `keep_going` budget.
    * Termination uses the budget and a two-consecutive-prose-response heuristic.
    * A completed task with a continuation budget can cost one extra turn.
    * Models outside the quirk table require manual `keep_going` configuration.
    * Remaining: an explicit completion signal. Tool-use history alone does not distinguish completion from abandonment.

24. **Cancelled queued messages retain a sent indication. Open.**

    * Source: August 2026 review, M19. Rechecked against v0.48.0.
    * Mid-turn input is displayed as sent with a promise of delivery at the next model step.
    * `turnTracker.cancel` discards the queue.
    * The Web UI's `turn.cancelled` handler clears queue and activity state but leaves the sent placeholder.
    * Required behavior decision: replace the placeholder with a discarded status or remove it.

25. **Conversation prompt caching. Done in v0.55.0; provider coverage remains partial.**

    * v0.54.0 added cache breakpoints for tool schemas and the system prompt on Anthropic and Bedrock.
    * v0.55.0 added breakpoints on the final block of each of the last two conversation messages.
    * Append-only history permits reuse of the previous prefix and a cache write for the new suffix.
    * Two moving conversation markers limit cache misses when a long tool round exceeds the lookup window.
    * Total: four markers, the API limit.
    * OpenAI-compatible requests receive no explicit cache controls. Local servers may cache prefixes themselves. Hosted providers with explicit controls remain unsupported.

26. **Turn-log retention. Done in v0.55.0; compression and independent tracing remain open.**

    * Pruning runs after configured limits are applied at startup and at daily rotation.
    * Round 10 found pruning under the default 30-day limit before `trace_max_age_days` was applied. `Open` now deletes nothing.
    * `trace_max_age_days`: zero or negative values select the default, not unlimited retention.
    * `trace_max_total_mb`: optional total-size limit, deleting oldest files first.
    * Today's file is never deleted.
    * Retention uses rotation-generated filenames rather than mtime.
    * Remaining: compression and enabling tracing independently of Smart Agent.

27. **Retrying an endpoint before fallback. Done in v0.55.0.**

    | Failure | Behavior |
    |---|---|
    | HTTP 429, HTTP 5xx, dropped connection | Up to two same-endpoint retries with 1-second and 2-second waits |
    | HTTP 401, model not found, DNS failure | Immediate fallback |
    | Request defect | No retry |

    * Each fallback endpoint has its own retry allowance.
    * Retries appear in the transcript and turn log: `retry` spans and `retries` on `turn.end`.
    * Turn cancellation interrupts the wait.

28. **Instruction-source trust classification. Partially done.**

    | Version | Change |
    |---|---|
    | v0.56.0 | Prompt pieces carry declared trust classes separate from source and message role. |
    | v0.57.0 | Each request identifies dynamic sources from all included messages. Labels survive later turns and restarts. Delegated tasks have their own source class. |
    | v0.58.0 | Delegated authority is scoped to the receiving request. Custom-command `@path` and shell expansions no longer appear as direct user instructions. |
    | v0.76.0 | `#<name>` conversation references resolve to metadata. Transcript content is retrieved only as a tool result. |

    * With Smart Agent enabled, orchestrator and specialist prompts distinguish instructions from tool-result data.
    * MCP results identify the external server, including error results.
    * Compaction summaries identify machine-generated content and state that quoted content retains its original authority.
    * Conversation tool results do not reenter the user-message processing path. Embedded references and slash commands therefore do not execute through that path.
    * Remaining: structural provenance inside compaction summaries.
    * Trust labels are declarations, not enforcement. They do not guarantee that a model ignores instructions in external content.
    * Permission checks remain the enforcement mechanism.

29. **Network egress policy. Open.**

    * Permission rules control tools and paths.
    * They do not restrict network destinations used by an executing shell command or MCP server.
    * Destination allowlists or deny-by-default egress require a separate mechanism.

30. **MCP server trust records. Mostly done in v0.55.0.**

    * Tool names, descriptions, and schemas are fingerprinted in `~/.localcode/mcp-pins.json` on first connection.
    * A changed fingerprint produces a startup warning naming the server, then updates the stored fingerprint.
    * The record includes first-seen and last-changed timestamps.
    * Remaining: a known-server registry and declared-version pinning.
    * Fingerprints describe the advertised interface. They cannot detect behavior changes that leave that interface unchanged.

31. **Debate review capabilities. Four planned parts completed in v0.69.0.**

    * Reviewers can run the configured `verify_command` without arguments. The model cannot choose the command contents.
    * Review briefs include `git diff HEAD` in a repository. Other workspaces use an explicitly labelled tool-call list.
    * Up to three reviewers run independently. All must approve.
    * Entry points: command, natural-language tool, and debate button.

    | Remaining gap | Constraint |
    |---|---|
    | One verification command | Tests and linters cannot be selected independently. Named checks would require a constrained selection interface. |
    | No diff outside Git | The tool-call list misses shell changes. Workspace snapshots would require a tree hash per round. |
    | Reviewer disagreement | The author resolves conflicting findings within the round budget. No additional model or automatic tie-breaking rule is configured. |

32. **Bedrock reasoning effort. Done in v0.71.0; API compatibility remains unverified in part.**

    * Reasoning configuration uses `additionalModelRequestFields` and merges with the million-token beta field.
    * Returned reasoning blocks are sent back on the assistant message required for continuation.
    * The change also fixed two v0.70.0 defects in other adapters: reasoning budgets above `max_tokens` and temperature supplied with thinking. Both caused HTTP 400 responses.
    * Unverified: the accepted Bedrock parameter name. The implementation uses Anthropic's name based on working `anthropic_beta` passthrough behavior.
    * Rejection messages identify the setting and how to disable it. The parameter name is defined by one constant.
    * Unverified: Bedrock support for `adaptive`, which newer families accept through the direct API.
    * Reasoning is streamed but not stored. Reload loses it, and `/context` does not account for its cost.

33. **CI test enforcement. Open for commits; enforced for releases.**

    * `gui-windows.yml` performs checkout, version resolution, build, smoke check, and upload.
    * It does not run `go test`, `go vet`, or a pure-Go build.
    * `make check` records a local verification stamp. Release preflight requires a matching stamp.
    * The stamp does not enforce checks on commits or verify another machine.
    * A push-triggered workflow could provide both checks.
    * The recorded decision was to defer that workflow because development and releases use one machine and another workflow adds maintenance.

34. **Unused functions. Resolved; test-environment gaps remain.**

    * Five functions were removed after their replacement paths were identified.
    * `turnTracker.anyRunning` and `whileIdle`: obsolete process-wide guard after per-session workspaces in v0.39.0.
    * `turnTracker.busy`: duplicate of `anyBusy`; its test caller now uses `anyBusy`.
    * `Loop.systemPromptFor`: replaced by typed prompt assets in `internal/agent/prompt_assets.go`.
    * `dataStrings`: replaced by `dataSources` in v0.57.0.
    * `turnTracker.running` remains required by `handleListSessions`. It was restored after an initial removal caused `go build` to fail.
    * The obsolete `memory.SystemPromptSection` wrapper was also removed. Its two tests now exercise `PolicySection` and `IndexSection`, which `cmd/localcode/wire.go` calls.
    * A duplicate memory assertion in `internal/agent` was removed.
    * `scripts/deadcode.allow` contains seven entries in the accepted categories: test-only reachability and build-tag variants.

    | Remaining verification gap | Evidence or constraint |
    |---|---|
    | Debian package acceptance | The test skips without `dpkg-deb`, which is absent from the Mac release machine. |
    | Conditional Web UI suite | Tests skip without `node` and under `-short`. Release preflight requires `node`. |
    | No browser execution in the verification suite | Tests use `test/webui/dom.js`, a handwritten DOM. |
    | DOM fidelity | In v0.72.0, all 272 tests passed while browser startup failed because the test double returned an Array from `querySelectorAll`. It now returns a NodeList. `previousElementSibling` was also added. |
    | Layout coverage | `offsetTop` and `scrollTop` are fixture values, not browser measurements. |

35. **Tool-interface validation. Partial; most enhancements require Smart Agent.**

    * Smart Agent adds paging, omission accounting, and edit-failure diagnostics.
    * Four unconditional fixes address silent match-budget exhaustion, long-line scan termination, unreadable-file omission, and unhelpful unknown-tool errors.
    * The unknown-tool fix shipped in v0.76.0 after a transcript showed five repeated `bash.command` calls with no recovery guidance.
    * A related OpenAI streaming defect retained only the first function-name delta. A split `read_file` name could become `read_`.

    | Missing capability | Constraint |
    |---|---|
    | Validate edited syntax | Language-independent bracket checks would reject valid strings and comments. A project can use `post_tool_use` for validation, but no validator ships by default and the hook cannot block an edit. |
    | Reject stale edits | A per-session read register must distinguish external changes from formatter changes made by localcode hooks. Timestamp comparison alone is insufficient. |

36. **Repeatable orchestration. Partial.**

    * Implemented: plan, validator, runner, and structured results.
    * Completed follow-up: settings toggle and model instructions describing when to use the tool.

    | Missing capability | Required design |
    |---|---|
    | Resume | Durable results keyed by stage, item, and copy. Write-step invalidation rules. An unchanged completed prefix could then be reused. |
    | Loop | `repeat_until` with mandatory `max_rounds` and a report of the stopping round. |
    | Pipeline | Per-item state tracking so a slow item does not block every later stage. This also requires phase visibility and a run ledger. |
    | Child-session retention | A 32-agent run creates 32 stored sessions and 32 `/tasks` rows. No cleanup policy exists. |
    | Concurrent permission display | The broker represents concurrent requests, but client presentation is unspecified. All four execution slots can wait while the user sees one request. |

37. **Direct selection of Smart Agent specialists. Open.**

    * Specialists are available for delegation but absent from direct selection.
    * `GET /api/agents` returns only `config.Agents`. TUI Tab, the Web UI menu, and `localcode run --agent oracle` therefore cannot select the six dynamic specialists.
    * The gap became visible when one-shot delegation shipped in v0.80.0.
    * Specialists are derived each turn so the Smart Agent switch can change mid-session.
    * Profile routing currently has no specialist override for `--profile` or `--model`.
    * Adding a specialist to `config.Agents` marks it as user-defined in `smart.Agents`. That changes its prompt assignment to the orchestration prompt.
    * Required design: an override interface in `internal/smart` that preserves specialist identity.

38. **Orchestration permission feasibility. Open.**

    * `Orchestrate` requires permission for every call.
    * A run can contain up to 32 agent turns and last half an hour.
    * Unattended turns can generate a complete plan before discovering that permission cannot be obtained.
    * `skip_all`, `skip_tools`, or an allow rule can authorize the call.
    * `hiddenTools` cannot currently query the permission resolver at turn preparation time.
    * Debate's structural refusal was handled by hiding its tool in v0.80.0. Orchestration needs a resolver-aware check instead.
    * Until then, the required flag is documented in USAGE and covered by a test.

39. **Rewind recovery and capture coverage. Partial.**

    * `/rewind` has no `/redo`.
    * Restore does not capture the content it overwrites. The reply therefore lists every affected path.

    | Capture gap | Effect |
    |---|---|
    | Hard links | Not detected. `Nlink` is not portable to the Windows target. Restoring one path also affects its linked names. The original comparison noted that Claude Code documents skipping hard links. |
    | MCP filesystem writes | Tools outside the two-name capture set are not captured, even when registered through the same registry. |
    | Background-task events | Rewind can remove `task.spawned` while later `task.status` events remain. Refusing rewind while a child is live reduces but does not eliminate the inconsistency. |

40. **Multiline TUI completion. Open.**

    * Both clients complete commands and references within a sentence.
    * The TUI disables completion when the input contains more than one line.
    * `SetCursorColumn` selects a column in the current line. The widget exposes no line setter.
    * `CursorDown` moves by visual row, which differs from logical lines after wrapping.
    * `cursorRune` returns `-1` for multiline input, disabling the completion scan.
    * The Web UI uses the textarea's absolute `selectionStart` offset and supports multiline completion.
    * Required change: an upstream line setter or local row tracking consistent with the widget.

41. **Test suites that run on Windows. Open.**

    * The Windows CI job runs tests since v0.87.0, scoped to what passes there: `internal/update`, `internal/childproc`, the handoff test in `cmd/localcode`, and the handoff and update tests in `internal/daemon`.
    * The first full run showed 52 failures across `cmd/localcode`, `internal/daemon` and `internal/session`, none in the code under test. Three causes account for nearly all of them:
        * A `t.TempDir()` holding a store's session logs cannot be removed while the store has them open; Windows refuses to delete an open file. `session.Store.Close` exists now, and the fix is `t.Cleanup(store.Close)` wherever a test builds a store, or a shared helper that does.
        * `TestDaemonEndToEnd` pastes a Windows path into JSON unescaped: `invalid character 'U' in string escape code`.
        * Tests isolating `HOME` did not set `USERPROFILE`, which is what `os.UserHomeDir` reads on Windows. Fixed at the three sites in `cmd/localcode`; others may exist in packages the job does not run yet.
    * Once a package is clean, drop it from the `-run` filter in `.github/workflows/gui-windows.yml` so the whole package runs.

42. **Handoff for `/update` inside the desktop window. Open.**

    * The window serves the daemon's handler in its own process and wires no `Handoff` hook (`runGUI` in `cmd/localcode/modes.go`), so `/update` typed in the window takes the installer path: the MSI on Windows, the bundle replacement on macOS, and a reply asking for the window to be reopened. Anything else running is a refusal, as it was in the terminal before v0.86.0.
    * The update at startup already runs the window against a successor through `successorProxy` (v0.88.0). The same proxy makes a mid-session handoff possible: bind a fresh loopback listener, spawn the successor on it, retire the in-process daemon, and swap the window's handler to the proxy. The browse and reveal routes stay in the window process as they do at startup.
    * Until then USAGE says so in "Handing over".

## UI ideas

### Web UI

| Idea | Status and scope |
|---|---|
| Markdown rendering | Done in v0.27.0. The dependency-free renderer supports headings, emphasis, code, lists, blockquotes, links, and rules in the Web UI and GUI. Code syntax highlighting remains open. |
| Collapsible tool-call cards | Open. Expandable tool input and output. |
| Diff viewer | Open. Before-and-after views for `edit` and `write_file` results. |
| Persistent permission approval | Done in v0.20.0. Options: allow once, allow for session, and always allow. The last option writes a matching config rule. |
| Usage visualization | Open. Per-model token bars and context-use indicator. |
| Session completion indication | Done in v0.40.0. The session light identifies running work and unread completed replies. Cross-client activity sources were aligned in v0.42.0. Running indicators became amber in v0.52.0 across the session list, prompt status, and task panel. Green indicates an available session. |
| Session search and filter | Open. Search by title and workspace. Manual card ordering shipped in v0.43.0. |
| Scroll control | Done in TUI v0.31.0 and Web UI v0.51.0. Output follows only when the view was already at the bottom before an update. Web UI and background-task windows provide a jump-to-bottom control. |
| Workspace is process-wide | Done in v0.39.0. Relative paths resolve per session. `os.Chdir` was removed. File tools use the session directory and bash uses `cmd.Dir`. Only the session's own running turn blocks its workspace switch. |
| Per-session workspace | Added in v0.28.0; underlying isolation completed in v0.39.0. Switching one client's session no longer changes another client's workspace. |
| Dark/light theme and mobile layout | Open. v0.52.0 introduced role-based color properties with a test for undefined properties. A light theme still requires another value set. |
| MCP server status | Open. Connection status and reconnect controls. |

### TUI

| Idea | Status and scope |
|---|---|
| Markdown and code rendering | Open. Consider a renderer such as glamour. |
| In-program session picker | Done in v0.59.0. `/session` selects a conversation without restart. `/model` provides agent selection. Session deletion remains in the startup picker, not the in-program picker. |
| Tool progress | Done in v0.25.0; extended in v0.32.11. Displays the running tool, queue depth, and background-task count. Transcript tool-call entries persist. Elapsed time remains open. |
| Context indicator | Open. Proposed bar thresholds: yellow at 70%, red at 85%. |
| History search | Open. Search earlier output with the `/` key. |

### Both clients

| Idea | Status and scope |
|---|---|
| Daemon-provided help | Partial in v0.60.0. `GET /api/slash-commands` supplies command names and descriptions for completion. Help strings remain client-owned. Release-verification tests require both help strings to include every `SlashCommands()` entry. Initial tests found one missing TUI command and eight missing Web UI commands. A shared endpoint such as `GET /api/commands/help` remains a proposal. |
| English program output | Done in v0.13.0. Documentation followed in v0.19.0. |
