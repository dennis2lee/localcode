# Improvements

## Summary

Open and partial work remains in security, orchestration, and client behavior. The numbered records below retain completed fixes and their release versions.

| Area | Recorded remaining work |
|---|---|
| Security | Network destination controls and structural source provenance in summaries |
| Orchestration | Resume, loops, per-item pipelines, child-session retention, and permission feasibility |
| Agent selection | Direct selection of dynamic Smart Agent specialists |
| Client behavior | Rewind recovery and the UI items below |
| Coverage | No Linux or macOS CI; 23 tested packages never run on Windows; the fmt check cannot fail |

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

4. **Configurable hook timeout. Done in v0.118.0.**

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
   * The client control shipped: both clients ask for earlier events, over the `?since=` the daemon already supported. Verified against the code in the v0.120.0 sweep.

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

11. **Cross-session usage totals. Done in v0.118.0.**

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

16. **Hook timeout failure policy. Done in v0.118.0.**

    * A `pre_tool_use` hook terminated after 30 seconds does not block the tool.
    * Current documented behavior: fail open.
    * Required decisions: per-hook `timeout`, a `fail_closed` option, and the default failure policy.

17. **Sensitive content in compaction logs. Done in v0.118.0.**

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

23. **Distinguishing stalled and completed turns. Done in v0.118.0.**

    * v0.48.0 added bounded continuation for models that describe a next step without executing it.
    * Known model families receive an instruction and a `keep_going` budget.
    * Termination uses the budget and a two-consecutive-prose-response heuristic.
    * A completed task with a continuation budget can cost one extra turn.
    * Models outside the quirk table require manual `keep_going` configuration.
    * Remaining: an explicit completion signal. Tool-use history alone does not distinguish completion from abandonment.

24. **Cancelled queued messages retain a sent indication. Done in v0.118.0.**

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
    * Remaining: compression, and tracing that writes without Smart Agent. Note what that second half actually is: the writer is already independent — `cmd/localcode/wire.go` opens the trace writer and applies retention unconditionally, because a daemon started with Smart Agent off may have it turned on ten minutes later. What is still gated is the switch that decides what gets written: `Loop.tracer` returns nil unless Smart Agent is on for the turn (`internal/agent/observe.go`), deliberately, since the trace records which models answered and what they cost. Independence means ungating that switch, not building the writer.

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
    * The provenance machinery is used, not defined-and-unused: `prompt.FromGeneratedSummary` marks live content in three production sites — the carried system prompt and carried unknown-source blocks in a compaction call (`internal/agent/compact.go`), and the recalled memory index (`internal/agent/prompt_assets.go`) — and `internal/prompt/activation.go` flags any asset pairing it with instruction trust, since text a model wrote cannot be an instruction. What is genuinely not there is structural provenance *inside* a compaction summary, and that half is impossible downstream rather than merely undone: the summary arrives from the model as one opaque string and re-enters the conversation as a single text block behind a header that declares it machine-written, so no later layer can attribute spans within it. The header and the prompt's source discipline are the whole mechanism, by necessity rather than by deferral.
    * Trust labels are declarations, not enforcement. They do not guarantee that a model ignores instructions in external content.
    * Permission checks remain the enforcement mechanism.

29. **Network egress policy. Done in v0.118.0.**

    * Permission rules control tools and paths.
    * They do not restrict network destinations used by an executing shell command or MCP server.
    * Destination allowlists or deny-by-default egress require a separate mechanism.

30. **MCP server trust records. Mostly done in v0.55.0.**

    * Tool names, descriptions, and schemas are fingerprinted in `~/.localcode/mcp-pins.json` on first connection.
    * A changed fingerprint produces a startup warning naming the server, then updates the stored fingerprint.
    * The record includes first-seen and last-changed timestamps.
    * Remaining: a known-server registry and declared-version pinning.
    * Fingerprints describe the advertised interface. They cannot detect behavior changes that leave that interface unchanged.

31. **Debate review capabilities. Four planned parts completed in v0.69.0, each since grown past its bullet.**

    * Reviewers run the configured `verify_command` — and, beyond the bullet, they read too. The reviewer allowlist is the reading tools plus the check plus the verdict (`internal/agent/debate.go`): `read_file`, `glob`, `grep`, `check`. The shell is excluded deliberately, because a reviewer with a shell is a reviewer that can write; `check` is the narrow exception, running one person-written line no model can change or add an argument to.
    * Review briefs include `git diff HEAD` in a repository — and, beyond the bullet, the brief separates what the author says from what actually changed, so the reviewer checks the second against the first rather than reviewing a summary. Outside a repository the fallback is the watched file-tool list, labelled as what it is rather than passed off as a diff.
    * Up to three reviewers run independently — and, beyond the bullet, concurrently in child sessions of their own that persist across rounds, unable to see each other, because three models agreeing is worth something only if they arrived there separately. Ending still needs every reviewer that answered to approve, inside the round budget or a measured stall.
    * Entry points: command (`/debate`), natural-language tool (the Debate tool in `internal/agent/debate_tool.go`, which books the run for when the turn ends), and debate button (the Web UI dialog in `internal/daemon/static/js/debate.js`, which types the same `/debate` through the same guards rather than owning a path of its own).

    | Prior question, not a task | Settled position |
    |---|---|
    | One verification command | Tests and linters cannot be selected independently. Named checks would require a constrained selection interface. Open only if somebody designs that interface. |
    | No diff outside Git | The tool-call list misses shell changes. Workspace snapshots would require a tree hash per round. Open only with that design. |
    | Reviewer disagreement | The author resolves conflicting findings within the round budget. No additional model or automatic tie-breaking rule is configured. That is the decision, not a gap awaiting a rule. |

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
    * It runs `go test` on the Windows-specific and directory-resolution packages with `CGO_ENABLED=0`, so those are also built pure-Go. It does not run `go vet`, or a pure-Go build of every package. See item 41 for the scope.
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
    | Validate edited syntax | Language-independent bracket checks would reject valid strings and comments. A project with `verify_command` configured gets the `check` tool on the default path (`internal/tools/check.go`, registered in `cmd/localcode/wire.go` with no Smart Agent gate), so a validator does ship by default for such projects — but it checks the tree on demand, not an edit at write time, and a project without the key gets no tool at all. A `post_tool_use` hook still cannot block an edit. |
    | Reject stale edits | A per-session read register must distinguish external changes from formatter changes made by localcode hooks. Timestamp comparison alone is insufficient. |

36. **Repeatable orchestration. Partial.**

    * Implemented: plan, validator, runner, and structured results.
    * Completed follow-up: settings toggle and model instructions describing when to use the tool.
    * Since shipped but still listed as missing: each stage announces itself running and completed on the task channel (`internal/agent/orchestrate_run.go`), so progress events already fire; every unit's structured answer is recovered from its own child's durable log (`internal/agent/answer_tool.go`) rather than carried in memory, so per-unit results are already durable and reconstructable; stage children run in sessions of their own that no list shows, and deleting the parent takes them with it (`internal/session/session.go`) — the deletion machinery exists, and only the retention policy (when an old child goes) is missing.

    | Missing capability | Required design |
    |---|---|
    | Resume | Durable results keyed by stage, item, and copy. Write-step invalidation rules. An unchanged completed prefix could then be reused. Genuinely absent: nothing reuses a previous run today. |
    | Loop | `repeat_until` with mandatory `max_rounds` and a report of the stopping round. Genuinely absent. |
    | Pipeline | Per-item state tracking so a slow item does not block every later stage. This also requires phase visibility and a run ledger. Genuinely absent — and a settled exclusion rather than an oversight: stages run in order with every stage a barrier, stated as deliberate with the tradeoff at `internal/agent/orchestrate.go:66-69`. |
    | Child-session retention | A 32-agent run creates 32 stored sessions and 32 `/tasks` rows. The sessions delete with the parent; what does not exist is a policy for when anything else cleans them up. |
    | Concurrent permission display | The broker represents concurrent requests, but client presentation is unspecified. All four execution slots can wait while the user sees one request. |

37. **Direct selection of Smart Agent specialists. Done in v0.118.0.**

    * Specialists are available for delegation but absent from direct selection.
    * `GET /api/agents` returns only `config.Agents`. TUI Tab, the Web UI menu, and `localcode run --agent oracle` therefore cannot select the six dynamic specialists.
    * The gap became visible when one-shot delegation shipped in v0.80.0.
    * Specialists are derived each turn so the Smart Agent switch can change mid-session.
    * Profile routing currently has no specialist override for `--profile` or `--model`.
    * Adding a specialist to `config.Agents` marks it as user-defined in `smart.Agents`. That changes its prompt assignment to the orchestration prompt.
    * Required design: an override interface in `internal/smart` that preserves specialist identity.

38. **Orchestration permission feasibility. Done in v0.118.0.**

    * `Orchestrate` requires permission for every call.
    * A run can contain up to 32 agent turns and last half an hour.
    * Unattended turns can generate a complete plan before discovering that permission cannot be obtained.
    * `skip_all`, `skip_tools`, or an allow rule can authorize the call.
    * `hiddenTools` cannot currently query the permission resolver at turn preparation time.
    * Debate's structural refusal was handled by hiding its tool in v0.80.0. Orchestration needs a resolver-aware check instead.
    * Until then, the required flag is documented in USAGE and covered by a test.

39. **Rewind recovery and capture coverage. Mostly done in v0.117.0; the capture set stays narrow by decision.**

    * `/redo` shipped in v0.117.0 and exists end to end: `routeRedo` answers it (`internal/agent/redo.go`), the rewind copies each file's about-to-be-overwritten content through `keepPostImage` (`internal/agent/checkpoint.go`) in the one moment it is still on disk, and the copies ride on the rewound marker itself, so there is no second store to disagree with the first. Offered only while the rewind is still the last thing that happened, because putting an undone turn back underneath a conversation that has moved on interleaves two histories.
    * What is genuinely left is the capture set, and it is narrow on purpose rather than by omission. Only `write_file` and `edit` are captured — a fixed two-name set the registry enforces structurally, matching Claude Code's documented scope. A shell command's writes, a file too large to keep, and anything behind a symlink are never copied either way, and both replies say which paths they could not put back rather than silently skipping them. The rows below are that decision's consequences, not oversights to pick up.

    | Capture gap | Effect |
    |---|---|
    | Hard links | Not detected. `Nlink` is not portable to the Windows target. Restoring one path also affects its linked names. The original comparison noted that Claude Code documents skipping hard links. |
    | MCP filesystem writes | Tools outside the two-name capture set are not captured, even when registered through the same registry. |
    | Background-task events | Rewind can remove `task.spawned` while later `task.status` events remain. Refusing rewind while a child is live reduces but does not eliminate the inconsistency. |

40. **Multiline TUI completion. Done in v0.118.0.**

    * Both clients complete commands and references within a sentence.
    * The TUI disables completion when the input contains more than one line.
    * `SetCursorColumn` selects a column in the current line. The widget exposes no line setter.
    * `CursorDown` moves by visual row, which differs from logical lines after wrapping.
    * `cursorRune` returns `-1` for multiline input, disabling the completion scan.
    * The Web UI uses the textarea's absolute `selectionStart` offset and supports multiline completion.
    * Required change: an upstream line setter or local row tracking consistent with the widget.

41. **Test suites that run on Windows. Done in v0.118.0.**

    * The Windows CI job runs tests since v0.87.0, scoped to what passes there: `internal/update` and `internal/childproc` whole, the handoff test in `cmd/localcode`, and the handoff and update tests in `internal/daemon`.
    * Widened in v0.92.0: `internal/userdirs`, `internal/skills` and `internal/rules` run whole, and the `cmd/localcode` filter also covers the two agent-directory wiring tests. Path resolution is a claim that has to be executed on the platform it is claimed for.
    * The first full run showed 52 failures across `cmd/localcode`, `internal/daemon` and `internal/session`, none in the code under test. Three causes account for nearly all of them:
        * A `t.TempDir()` holding a store's session logs cannot be removed while the store has them open; Windows refuses to delete an open file. `session.Store.Close` exists now, and the fix is `t.Cleanup(store.Close)` wherever a test builds a store, or a shared helper that does.
        * `TestDaemonEndToEnd` pastes a Windows path into JSON unescaped: `invalid character 'U' in string escape code`.
        * Tests isolating `HOME` did not set `USERPROFILE`, which is what `os.UserHomeDir` reads on Windows. Fixed at the three sites in `cmd/localcode`; others may exist in packages the job does not run yet.
    * Once a package is clean, drop it from the `-run` filter in `.github/workflows/gui-windows.yml` so the whole package runs.

42. **Handoff for `/update` inside the desktop window. Done in v0.95.0.**

    * The window serves the daemon's handler in its own process and wires no `Handoff` hook (`runGUI` in `cmd/localcode/modes.go`), so `/update` typed in the window takes the installer path: the MSI on Windows, the bundle replacement on macOS, and a reply asking for the window to be reopened. Anything else running is a refusal, as it was in the terminal before v0.86.0.
    * v0.88.0 described the window fronting a startup successor through `successorProxy` and did not wire it: `runGUI` never called `startupHandoffBinary`, and `successorProxy` had no caller outside its test. The deadcode allowlist recorded it as live behind the gui tag, which hid that.
    * Done in v0.95.0, both halves. At startup the window spawns the successor on a fresh loopback listener and serves the proxy. On `/update` it does the same mid-session: `windowHandoff` spawns the successor, retires the in-process daemon, swaps the window's handler to the proxy, and reloads the page. The successor watches the same alive pipe the terminal's does, so it exits with the window.
    * The settings window's install button still runs the MSI, and the window now registers with the Restart Manager so Windows starts it again when the install finishes (`internal/gui/restart_windows.go`). Windows' own rules apply: the window must have been open a minute, and an install needing a reboot does not restart anything.

43. **Muse never receives the reasoning strength it asks for. Done in v0.95.0.**

    * The model's own card sets reasoning strength through the system prompt, as `Reasoning strength: <low|medium|high|xhigh>`, and asks for `high` or `xhigh` on coding and agentic work. localcode sends `reasoning_effort` on the request instead (`openAIEffort` in `internal/provider/openai.go`), which the model's chat template does not read, so every muse conversation runs at whatever the server defaults to. `/effort high` changes nothing on this family.
    * `/llm-doctor` writes the line into its own canaries (v0.90.0), so the probes run the model the way its publisher intends. Real conversations do not.
    * Done as described: `museReasoningLine` in `internal/agent/quirks.go` puts `Reasoning strength: <level>` into the system prompt on the model-quirk asset whenever an effort level is set on a muse profile or conversation, and `xhigh` is a level now, sent to the OpenAI wire as `high` and to Anthropic's as the high budget.

44. **No `top_p` or `top_k` on OpenAI-compatible requests. Done in v0.117.0.**

    * `oaRequest` carries `temperature` and nothing else from the sampling family, so a profile cannot ask for the `top_p` 0.95 and `top_k` 64 that muse's vLLM recipe specifies alongside temperature 1.0. `/llm-doctor` sets them directly on its own request bodies; a profile has no way to.
    * Adding them means a config field each and a decision about servers that reject `top_k`, which is not in the OpenAI schema and which vLLM accepts as an extension.
    * The decision taken: send it only when a profile asked, on the reasoning `reasoning_effort` is already sent on — most servers ignore an unknown field, and one that refuses it only ever sees it from somebody who set it deliberately.
    * Pointers rather than plain numbers, because zero is a value here: `top_k` 0 means "no limit" on vLLM, so "unset" had to be distinguishable from it.
    * Wired to all three backends rather than only the one the complaint named: a profile field that silently did nothing on Anthropic would be a new version of the same complaint. Both are dropped while a Claude model is reasoning, as temperature already was, so one profile can carry a sampling recipe and ask for reasoning without the two colliding.
    * Bounded in `Config.Validate`, because a sampling number outside its range is refused by the server in the middle of a turn — the worst place to read about a typo.


45. **Smart Agent's orchestration prompt told a muse to delegate to itself. Confirmed and fixed in v0.108.0.**

    * Reported: turning Smart Agent off noticeably reduced looping on Muse-Glimmer-30B. The bundle changes eleven things for the model (see `smartOn` and `smartAgent` sites), and the likeliest cause is the orchestration prompt `localVariant` in `internal/smart/prompt.go`, which every local family gets: "Searching the codebase: use Task with the explore agent. Do not grep the whole project yourself." and the same for review and verification. In a one-profile config every specialist is the same muse model, so the orchestrator delegates a search to itself, gets a summary it did not write, and re-delegates. Each Task is a full extra model call in a fresh context.
    * Second candidate: the paged `read_file` (800 lines with a `read on with offset=N` footer). A model that does not carry the offset re-reads the same window, which the repeat guard then ends.
    * How to confirm from what Smart Agent already records: with it on, every turn is in `~/.localcode/trace/localcode-<day>.jsonl`. Count `span == "tool"` records with `tool == "Task"` per `trace_id`, and count `span == "tool"` records per trace whose `parent_session_id` is set. A loop that is delegation shows as many Task spans under one trace; one that is re-reading shows as repeated `read_file` spans with the same session. The `stopped: N steps in a row only repeated` notice (v0.95.0) names the calls in the transcript either way.
    * If confirmed, the fix is a muse-specific orchestration variant that recommends delegation rather than ordering it, or that leaves the delegation tools out of a one-profile config where the specialist would be the orchestrator's own model. Not done unasked: it changes what every muse session is told.

    * **Confirmed against the reporter's own model**, a 30B muse served by LM Studio, through a shim that recorded every request. Same task, same workspace, one switch. With Smart Agent off: thirteen requests, two edits, both files correct. With it on: the orchestrator's first move was `Task(agent: "explore")`, and the sub-agent — the same muse — spent thirteen requests on `glob` and `grep` in a two-file project, found the answer at the eleventh, carried on, edited nothing, and was still running when it was cancelled. The two system prompts measured 4190 and 3547 characters, so the prefix cache could not hit either, exactly as predicted here.
    * **The second candidate is ruled out for this failure.** `read_file` was called twice with no repeat; the loop was delegation, not paging.
    * **Fixed by roster shape rather than by model id.** `smart.Solo` reports whether every routing category resolves to the same profile, and `OrchestrationPrompt`/`PlanPolicy` take it. The test is what the categories *resolve to*, not `len(Profiles) == 1`: `classify` reads a weight class out of a model id, so two local endpoints whose ids carry no size qualifier route everything to one profile — the population this exists for, and what a profile-count test would have let through.
    * The solo prompt states what delegation still buys — a context this conversation does not pay for — instead of ordering it, and says plainly that review, planning and running the build are not worth handing to the same model. Re-measured on the same model and task: no `Task` span at all in the trace, edits at request four, thirteen requests, both files correct. The penalty for having the switch on is gone.

46. **The window's installed copy stops updating after a startup handoff. Windows only, done in v0.117.0.**

   * On Windows an .exe cannot be replaced in place, so a startup update stages the new binary under `%LOCALAPPDATA%\localcode\bin` and the installed copy under Program Files — the one the shortcut starts — keeps whatever version the MSI put there.
   * The window then serves `successorProxy`, which forwards `GET /api/update` to the successor. The successor answers with its own version, which is the staged copy's and is current, so `available` is false and the settings panel says "localcode x.y.z is the latest release" with no install button.
   * The reply that creates the situation promises the opposite (`internal/update/install.go`: "the copy under … is still the old version, and the settings window's install button updates it"), and that button is now hidden by the same answer that hid the problem.
   * What still reaches the user: the daemon and the whole Web UI, because those are the staged copy. What never does: the window shell itself — the frameless chrome, the resize edges, `RegisterApplicationRestart`, the splash.
   * The decision taken: the window keeps one. `successorProxy` now takes the daemon this process built before it handed the listener over, and answers `GET /api/update` and `POST /api/update/install` from it while everything else goes to the successor. That daemon serves nothing and listens nowhere; it is kept precisely because it is the only thing in the process that knows how to run an installer, and because its version is the installed copy's rather than the staged one's.
   * The two routes join the two that were already kept for the same class of reason — the folder picker and the reveal — so the shape was there to follow rather than invent.
   * The mid-session handoff keeps nothing, and should not: that one happens *because* somebody just installed an update, and the daemon being retired is the one that did it. Asking it again would offer the release it has already applied.


47. **The four Web UI screenshots in `docs/img/` were of the old interface. Done in v0.106.0.**

   * `docs/where-localcode-differs.html` and its Korean twin embed `webui-full.png`, `webui-completion.png`, `webui-sessions.png` and `webui-outside.png`. All four had been taken before the interface study, so they showed the old palette, the old glyph icons, the full-width transcript and the permanent row of session buttons.
   * Recorded here as "needs a screen, and there is no path from this environment to a PNG on disk". That was wrong: Chrome over the DevTools Protocol both evaluates script and captures pixels, which is what the three interesting figures need — a completion walk mid-typing, the archive open, the permissions dialog scrolled to its boundary section. Retaken from a running v0.105.1 with three MCP servers, three named conversations and an archive.
   * Two interface faults surfaced in the taking and were fixed with them: every unclassed button was a filled lozenge of the accent, and a session row's controls reserved a blank band under every idle row. See the v0.106.0 entry.


48. **The carry-on nudge was measured on a task the model had already finished, and it went and worked again. Done in v0.109.0.**

   * `keep_going` sent one message — check whether the task is complete; if not, take the next step with the tools — and sent it **with the tools callable**. A model that had finished did what a compliant model does with tools in reach: it checked. Measured on the reporter's own 30B muse through a recording shim, a task finished correctly at request six cost seven more and the whole budget.
   * Both earlier fixes were patches over the same hole. v0.53.0 stopped the prompt asserting the work was unfinished; v0.107.0 narrowed what counted as work to a call that changes something. Neither touched the fact that the question and the means to answer it with more work arrived together.
   * The question is now its own request, sent with `tool_choice: none`. The tool definitions stay on the wire — a local server's prefix cache is keyed on the rendered prompt and the schemas are the front of it — and the model may answer but not act. A carry-on follows only an answer that says work remains.
   * LM Studio was checked directly for whether it honours the field on this model: with `tool_choice: none` the same request answers in prose where it otherwise calls a tool. A server that drops the field is handled rather than assumed away: a verdict reply carrying tool calls is counted as a carry-on, so the budget still bounds the turn.
   * Measured on the new build against the original repro, the same task and workspace the earlier entries used. Three runs, all three identical: seven provider requests, one question, **zero carry-ons**, both files correct. The same task cost thirteen requests under the shape v0.107.0 replaced and nine under v0.107.0 itself. On a second task — change a function's signature, then fix every caller until the build passes — two runs at eleven and fourteen requests, one question each, zero carry-ons, build passing; three control runs with the feature off took ten to twelve.
   * One measurement was thrown away rather than reported. A run came back as "no question asked", which would have meant the feature never fired on the repro; the request log showed the question going out and dying unanswered, because the trial script's poll cap had expired and it deleted the session mid-turn. The cap was raised, the script now reports whether it timed out, and the arm was re-run.
   * A second fault was found while reviewing the change and is fixed with it, having been in the old shape as well: pressing stop left the prompt in the history. A cancelled stream closes without a terminal event, which the loop reads as a model that stopped after running tools, so the carry-on was appended to a turn that was already over and the next thing the person typed arrived underneath it. The turn is now silent once its context is done, and a test that drives a real cancellation covers it.

49. **The transcript was a ribbon down the middle of a three-column window. Done in v0.109.0.**

   * `#transcript > *` was capped at a 37rem reading measure and centred, and the composer at that measure plus six rem. That is the right rule for a page of prose and the wrong one for this window: the middle column of three was a narrow band with empty ground either side, and a table, a diff or a file listing wrapped inside it while the window had room to spare.
   * Both now span and keep one `--gutter`, which is the only thing holding the text off the panel rules. The composer moved into the conversation column so that it is the transcript's width by construction — the panels either side are draggable and collapsible, so a padding would have had to be recomputed every time one of them moved.
   * Found in the same pass: two `#input` rules, the second of which reset `font: inherit` and had been silently discarding the document face the first asked for. Merged at the face the composer has actually been drawing, since changing it is not a change to how wide anything is.
   * And a second fault, in every build to date and nothing to do with width. `autoResizeInput` writes the measured height inline and nothing measures again, so a page laid out in a window with no size kept whatever `scrollHeight` returned there: measured at 996px on a page loaded into a hidden pane, drawn as a 240px empty box by the `max-height` cap, over a third of a small window. A box with no width is no longer measured, and a window that gains a size recomputes. Three tests, and the harness now models window-level events so a resize can be fired at all.

50. **A shelf that cost what the shelf being empty costs. Done in v0.110.0.**

   * Every session's event log was read and parsed at startup, archived or not, and the archive is the part nobody is looking at. Measured on a home shaped like the report's — 5 active and 100 archived conversations, 3,000 events each with two background tasks apiece, 708 MB on disk — the daemon answered its first request in 2.9s and sat at 1.8 GB resident. After: 0.18s and 109 MB.
   * Three things read it. `restoreOne` parsed every log into memory; `RehydrateAll` skipped an archived conversation but not the work that ran inside it; and `Scheduler.Restore` read every session's events looking for booked work.
   * The shelf is a subtree, not a flag. A background task's session is invisible and carries no `archived_at` of its own — `Store.Archive` refuses anything that is not a conversation — so a first attempt that gated on the flag alone still read every task log at startup, which on a busy conversation is the larger half of the events. `LoadAllFromDisk` now reads all the metadata first, computes the shelved set from the parent links that gives it, and reads only the logs outside it; `Store.ShelvedIDs` is that set, and the rehydrator and the scheduler ask for it rather than each doing their own one-hop parent check.
   * The log is read by the first request that wants it: `Append`, `Events` and `TailSince` ask before they take the store's lock. `Append` above all, since the sequence numbers live in the log and handing out seq 1 to a session whose file ends at 6,000 breaks `since=` replay and Last-Event-ID resume for it permanently.
   * Two faults were found reviewing the first attempt, and both are in it because of how the read was written rather than because of what it does. Reading the file **with the store's mutex held** stopped every other conversation's output for the length of the read — that mutex is taken by every append and every fan-out in the process — so the read now happens outside it and installs under a short lock. And **swallowing a read error** left the session present, empty, and with its sequence number at zero, which is exactly the duplicate-seq corruption above; a missing file is still an empty session, and every other error is returned so the caller refuses instead of writing.
   * The scheduler is a behaviour change, and a smaller one than the first attempt claimed. A booking has never run a turn in an archived conversation — `Scheduler.fire` looks at the parent and marks the row missed on sight — so the first attempt's justification, that archiving stopped a turn firing where nobody would see it, was false. What actually changes: a booking whose moment passes after a restart is no longer marked at that moment, because nothing under the shelf is armed; retrieval marks it, and says the conversation was archived rather than that localcode was not running. Archiving itself leaves the timers alone.
   * Two pre-existing scheduler faults surfaced in that review and are fixed with it: the per-conversation id counter was rebuilt only from surviving rows, so a cancelled booking's number was forgotten and reused, against what the comment beside it claimed; and a restore over an already-armed row left two timers on it.

51. **The splash named the version being replaced. Done in v0.110.0.**

   * Reported with a photograph: the window read `LocalCode v0.108.1` above a status line reading "starting localcode 0.109.0". Both were true. The label is the shell's own version, fixed when the window opens, and after a startup handoff the shell is the copy the shortcut points at rather than the one about to serve.
   * The version is what anybody looks at to decide whether an update took, so it is the wrong one to be stale. `Launch` now hands its start function a `setVersion`, the splash's version span has an id and a writer, and the handoff calls it as soon as it has read the successor's version — which is the same moment it already says "starting localcode x.y.z". It is put back if that successor will not start, since having just promised a version, leaving it up while the old binary serves is the same untruth the other way round.
   * Found in the same review: `internal/gui` is behind a build tag, so the race lane never compiled its tests and the check gate only built the package. Its suite had been failing since the icon was redrawn, on a test that pinned the old palette as its own premise — so a new test in that package could pass and be invisible. The gate now runs `go test -tags gui ./internal/gui/`, the premise asserts that there is a `#` to encode rather than which colour it is, and the two announcements the splash makes during a handoff are covered by tests in the default build.

52. **Reasoning effort was a word you had to know, and one answer for a conversation that can change model. Done in v0.111.0.**

   * Setting it meant typing `/effort xhigh` and knowing whether this model had that step. Four of the five words do nothing on an adaptive Claude, `xhigh` does nothing on anything but muse, and there was no way to find that out except by reading what `/effort` printed afterwards.
   * The control now offers the levels the model tells apart and refuses the rest. The list is computed from what the adapters actually put on the wire: `{off, high}` where the family decides the amount itself, `{off, low, medium, high}` where each level is a token budget or a `reasoning_effort` word, all five on muse, which reads the strength from its system prompt.
   * The list travels with the answer rather than living in each client. Two clients with their own copies of it is two places to update when a family is added, and one of them will be missed.
   * The answer is kept per model. One conversation can change model without being asked to — a fallback does it — and a level that suited a muse is a word a Claude has no step for. Keyed by model id, with the conversation-wide value that predates it still answering for a model with no entry of its own.
   * Eight faults came with the first cut and were caught by an adversarial review before release: the data race below, the level being the one request field re-read mid-turn, a level list that ignored the reply cap and so offered two steps that send the same number, "back to the profile's" doing nothing on a conversation that predates the per-model field, `/effort` storing a level the picker refuses, `/effort` changing the level without announcing it, a fork losing the level, and the terminal footer keeping the previous model's. Each is fixed with a test that fails without the fix.
   * The data race is the one worth naming. `Session` is copied by value everywhere it leaves the store, which was enough while every field was a value; the per-model levels are a map, and a value copy shares the map header. The daemon lists sessions on one request and sets a level on another, so that pair raced — and a caller could write into the store through a session it had been handed. Every path that hands one out detaches it now, with a regression test that fails both ways without the fix.
   * Found while building it: an agent switch changes the model and nothing told the control. It went on showing the level and the steps of the model it had left, which on a one-switch family is a dial that does nothing. The daemon announces from the switch handler, so both clients redraw from one place.

53. **The desktop window never installs an update at startup on macOS or Linux. Done in v0.117.0.**

   * Two of the three startup modes ask whether there is a newer release: `runDaemon` and `runEmbedded` both call `autoUpdateAtStartup`. `runGUI` never has — the call was added to the other two when the feature landed and the window was already in the tree.
   * It is not an oversight to copy the line into. The window's daemon is built inside the callback `gui.Launch` runs, which is after the window is on screen, and `autoUpdateAtStartup` ends in `exec`: replacing a process that is holding a native window is not the same operation as replacing a headless one. Doing it before `gui.Launch` would mean building enough of a daemon to read the config before the window appears, which is the several-second blank the splash exists to remove.
   * Two ways out, both decisions rather than patches. The window could take the successor path it already has on Windows — `startupHandoffBinary` keys off a platform constant, and a caller that says "I cannot exec" rather than a platform that cannot would put every window on the proxy — or the update check could run before `gui.Launch` on a cut-down daemon. The first reuses machinery that took several releases to make reliable; the second reintroduces the delay the splash was built for.
   * The first way out was taken, and it was one line of meaning rather than one of code: `startupHandoffBinary` now takes the caller's answer to "can you exec?" instead of reading `selfRestartAvailable`. A window says no on every platform, because what stops it is holding a native window rather than anything about the operating system. The headless daemon and the terminal still ask the platform, which is right for them.
   * `ApplyForHandoff` was already correct on Unix — it falls through to `Apply`, which replaces the binary in place — so nothing about the install half had to change. The machinery this reuses is the one that took several releases to make reliable, which is exactly why it was the way out to take.
   * Guarded by reading the source rather than by running it: the next step of that function asks GitHub for a release and installs it, so a test that drove it would be a test that downloads. What is worth protecting is which answer each caller gives, and that is what is written down.

54. **Stop did not reach a search. Done in v0.113.0.**

   * Reported as "clicking stop should stop a running tool too". Most of it already worked and was measured doing so: a `sleep` started by the bash tool dies with the turn, because `shell.Command` binds it to the context and kills the whole process group; MCP passes the context to the server; `check` goes through the same shell; a permission prompt has a `ctx.Done()` arm.
   * The gap was `internal/tools/search.go`. Its three `filepath.WalkDir` sites took no context, so a stop during a `grep` or `glob` ended the turn on paper — the model was never asked again — while the walk carried on to the end of the disk, holding the turn's goroutine and the session's busy flag.
   * Three shapes, three answers. The walks check between entries and return a sentinel the caller can tell from a read error. A scan inside one file checks every 4,096 lines, because the walk only looks between files and one log can be the whole search. A pattern with no `**` is `filepath.Glob`, which has no hook at all, so the wait sits beside it and the abandoned glob finishes unread — safe here in a way it would not be for most work, since it reads directory entries and returns a list.
   * The first cut of this fix missed the third shape and its own comment claimed otherwise. The review measured it: a cancelled non-`**` glob ran 339ms and returned its whole 2.2 MB answer. Two of the new tests were also vacuous, matching nothing because the pattern's depth did not match the fixture; the uncancelled test now asserts every pattern it stops actually matches something.

55. **A slash command that did not exist was handed to the model. Done in v0.114.0.**

   * Reported with the transcript. Somebody typed `/clean`; nothing here recognised it, so it fell through every route and was sent to the model as an ordinary prompt. The model's own reasoning is in the log: "The user typed /clean twice. Possibly they want to clean up the home directory?" — then `find /home/... -name "*.md" -o -name "*.txt" -o -name "*.log"`, `ls -lh`, `du -sh` across the home directory, and the line "Maybe they want to clean up the home directory by removing empty directories or temporary files." It was cancelled before it removed anything.
   * Nothing was deleted, and that is luck rather than design: the model was choosing between asking for clarification and removing files, with a shell and skipped permissions, because a typed command reached it as an instruction.
   * A message beginning with a slash is somebody addressing the program. It is now answered here — last in the route table, so every built-in, custom command and skill still wins first — and never sent on. The answer names the closest command when exactly one is a single edit away, which is the case that produced this: `/clean` for `/clear`.
   * A path is not a command. A first word carrying a second slash or a dot goes to the model as it always did, so "/etc/hosts needs a line" is still prose.
   * Two existing tests asserted the old behaviour — "unmatched slash text is sent as is" and "unknown slash command still goes to model". Their comments defended it only as "keeps its old behavior". Both were rewritten to the safe contract, keeping the property they were really protecting: a near miss must not load somebody else's skill body.
   * The same report showed a second fault. `/clear` typed while that stuck turn was running was refused with a 409, and both clients read every 409 as "a turn is running, queue this" — so it was queued in silence and delivered later. The daemon marks this refusal now, and both clients show it.


56. **A command-by-command comparison with opencode, and the twelve things it found. Done in v0.116.0.**

   * Every one of opencode's 33 slash commands was matched against localcode's by reading both implementations, not by comparing names. Sixteen pairs had been called "already covered" in an earlier pass; none of the sixteen turned out to be identical, five were not covered at all, and one of them was a regression this project had just shipped.
   * The regression first. `routeUnknownCommand`, added in v0.114.0, answered any slash nothing else claimed — without checking whether the name existed. `/init`, `/memory` and `/usage` accept no argument, so `/init focus on tests` fell through to it and was told "There is no /init in this build", directly above a list containing `/init`. A name that is known now gets a different answer, saying what was refused and why, and still does not reach the model.
   * `/init` takes what to focus on, the way opencode's does, and its prompt asks for the things a repository actually says about itself: lockfiles, CI workflows, pre-commit configuration, monorepo boundaries, and rules files another agent left behind.
   * `/status` is the one that mattered most. An MCP server that fails to connect said so in a Web UI indicator and nowhere else, so a terminal had no way to see that a server was down, let alone read the error. It now lists every server with its state and last error, plus the skills, custom commands, agents and workspace.
   * `/debug` is the block to paste into a bug report: version, platform, session, agent, model, effort, workspace, config path, and what is attached. opencode copies its equivalent to the clipboard; this build has no clipboard code at all, so it prints.
   * `/workspace [path]` closed a plain asymmetry: the directory a conversation works in was reachable only from the Web UI's button. The move waits for an idle session, for the reason `/clear` and `/rewind` do — a relative path resolved either side of it lands in a different project — and both clients now hear about it on a `workspace.changed` event, so one client's button cannot go on naming a directory the other has left.
   * Four session operations existed only as Web UI buttons: `/new`, `/rename`, `/fork` and `/delete`.
   * Leaving was three keystrokes short. `/exit`, `/quit` and `/q` did not exist, and the bare word `quit` reached the model as an ordinary prompt. The three places that each held their own copy of the exit check are now one function, which is what would have let `quit` be added to two of them.
   * Ctrl+C took a half-written prompt with it. The first one clears the line, as a shell prompt does; the second leaves.
   * Any picker can be narrowed by typing. Every key was already being swallowed by the picker and doing nothing, so this costs no keystroke that meant something else.
   * Ctrl+E steps the reasoning effort without opening a list, which is what makes dialling it up and down through a long job tedious.
   * `/rewind` gives the undone prompt back, into an empty box only. This is as much of opencode's `/redo` as is honest here: the conversational half is restored, and the file half cannot be, because the pre-images are kept and the post-images are not.
   * Six aliases for commands that already existed, because they are the words people arrive typing: `/agents`, `/models`, `/mo`, `/sessions`, `/resume`, `/continue`.
   * Left for their own cycle, and why. Picking a model independent of the agent (opencode's `/models`) is a session model override, and model resolution also decides the provider, the effort levels and the token ceiling — too much blast radius to attach to this. Detaching a synchronous sub-agent mid-flight into the background (opencode's Ctrl+B) is a redesign of how `SpawnSync` blocks. Re-applying an undone turn's file writes needs post-images that are not kept.
57. **The three a parity review left for their own cycle. Done in v0.117.0.**

   * Item 56 closed twelve findings and named three it would not attach to that change: picking a model independent of the agent, letting go of a synchronous sub-agent, and re-applying an undone turn's file writes. Each was a design decision rather than a patch, and each is here with the decision stated.
   * **Which model answers.** What is chosen is a profile, not a model id. A model does not travel alone — the provider that serves it, the token ceiling, the context window — and storing "answer on claude-opus-5" against an agent pointed at a local vLLM would name a model that provider cannot serve. The profile already knows all of it, and the agent keeps its prompt, its tools and its permissions, which is what separates choosing a model from switching agent. Kept per agent for the reason effort is kept per model: agents are pointed at models that suit them, and a choice made while talking to one should not follow you to another.
   * **Letting go of a sub-agent.** The obstacle was a context, not a policy: `spawnSync` ran the child on the caller's own goroutine under the caller's own context, and a context cannot change parents once it is built, so a child derived from the parent turn could never outlive it. The child now runs under a context of its own and the link to the parent is a goroutine — which reproduces the old behaviour exactly while attached, and simply stops watching once the child is let go. What the model is told matters as much as the mechanism: "still working, do not start it again" rather than "cancelled", because a model told the second one reasonably starts the same job over, which is two sub-agents doing one piece of work with one of them invisible.
   * **Redo.** Not a new store, a moment. Pre-images are kept as a matter of course, which is what makes undoing always possible; post-images are not, which is why redoing was not. During a rewind, immediately before each pre-image goes back, the turn's own result is still on disk — so that is when it is copied, into the same content-addressed store, where a file the turn left unchanged costs nothing new. The conversation half is the append-only shape the rewind already uses: a marker naming the rewind it cancels, and `applyRewinds` reading both. Offered only while the rewind is still the last thing that happened, because putting an undone turn back underneath a conversation that has moved on interleaves two histories.

58. **The release gate ran every test one way. Done in v0.117.0.**

   * `check.sh` ran `go test ./... -race` and nothing else. The race detector is five to ten times slower, so a test whose timing assumption only holds at that speed passes the gate and fails for anybody who types `go test ./...`.
   * One did, and it shipped: a search cancelled two milliseconds in, on a tree that walks in under two without the detector. It was green in v0.116.0's gate and red on a plain run of the same commit.
   * Two fixes, because either alone leaves the hole. The test now measures how long the search takes on the machine it is running on and cancels a tenth of the way in, so it asserts a property of the code rather than a number from one build mode; and the gate grew a plain lane beside the race one, in its own group so it runs alongside rather than after.



59. **The eleven that were still open. Done in v0.118.0.**

   * Closed together because they were the whole remaining list, and several turned out to share a shape: a control that existed and could not be seen, or a claim wider than what was enforced.
   * **Hooks (4, 16).** A timeout arrived as `signal: killed` in a warning, indistinguishable from a script that ran and failed, and the tool went ahead — so a `pre_tool_use` guard written to stop something dangerous was silently not stopping it. A hook that does not finish now says it never decided. The default failure policy stays permissive and that is the decision, not inertia: a hook that cannot run and blocks everything locks somebody out of their own tools mid-session, which is the commoner accident and the more damaging one. `fail_closed` is one line of config for the other case, and covers not-finishing only — a script that ran and exited non-zero has decided, and treating each of its bugs as a veto would be a lockout per bug.
   * **Session logs (17).** The review's answer: `0644` in a `0755` directory, which on a shared machine is every other account on it — and compaction records its summary in full, so one event holds a condensed copy of the whole conversation. The checkpoint blobs in the same package had been `0700` since they were added; the tighter answer was already here, applied to the copies of files and not to the conversations about them. Existing directories are narrowed on open, because a directory keeps the mode it was made with. Retention stays manual and documented rather than automatic: deleting somebody's conversations on a timer is not a default to introduce.
   * **Usage (11).** Read from the logs when asked rather than kept as a running total, so there is one place the truth lives. Archived conversations count — they happened, and a total quietly leaving them out is wrong in the direction nobody checks, which is the same reasoning that keeps a rewound turn's tokens in the per-session figure.
   * **Terminal (24, 40).** A stopped turn left its queued prompts saying they had been sent, about messages the daemon had already dropped; they are rewritten rather than removed, because taking the words away silently is the other half of the same fault. And completion works past the first newline: reading the offset is exact arithmetic over the widget's own logical lines, and writing the cursor back never names a row at all — the tail goes in first, the cursor goes to the one position that needs no arithmetic, and the head is typed in front of it.
   * **Agents (37, 38, 23).** The specialists were delegatable and not selectable, and nothing underneath was ever the obstacle: `profileFor` and `agentConfig` have always resolved one by name, so the gap was the listing and the check beside it. Orchestration is no longer offered to a turn that could never authorize it — the permission resolver is asked before the tools are advertised, which needed the session id to be on the context from the start of a turn rather than only around the tool calls. And `/keep-going` now says what the conversation it was typed in actually gets; the explicit completion signal that item asked for arrived with the keep_going redesign, measured at 13 requests down to 7 with one tool-less question and no carry-ons.
   * **Network (29).** The honest scope, and saying so is the point. localcode's own outbound connections all meet at `http.DefaultTransport` or a clone of it, so one checked dialer covers the providers, remote MCP and the update check. A shell command is a separate process with its own sockets and is not covered; refusing to *run* commands whose names look networked would be theatre, defeated by a script or a different binary name in seconds. What bounds those is the permission prompt on bash and the operating system, and the documentation says exactly that rather than implying a control that is not there.
   * **Windows (41).** The three named causes fixed rather than worked around: every test that builds a store closes it, the end-to-end test marshals its nested tool arguments instead of hand-escaping one of the two levels, and the tests isolating `HOME` set `USERPROFILE` — which is what `os.UserHomeDir` reads there. `internal/session` and `internal/daemon` run whole on Windows CI now; `cmd/localcode` keeps its filter, since what remains there is about driving real processes rather than about paths.
   * And a fourth cause, found by widening the filter and reading the log rather than the badge: **the Windows test step could not fail the job.** A multi-line `run:` under PowerShell reports only the last command's exit code, so every `go test` line but the last was advisory. The first widened run had 27 failures in `internal/session` and was reported green. The step is `shell: bash` now, where Actions sets `-e` and the first failure ends it.
   * The 27 were one bug and one blind spot in the fix for it. `Store.Close` was being registered by a pass that matched `session.NewStore(` — which is every call outside the package and none inside it, so `internal/session`'s own tests got nothing; and `LoadAllFromDisk` returns a store too, which nothing was closing anywhere. Both shapes are covered now, and the check that found them is the one worth keeping: grep for every construction and assert a `t.Cleanup` within a few lines of it.


60. **Two permission tables nothing walked. Fixed in v0.120.0.**

    * A sweep for remaining work looked for hand-maintained lists with no guard — the shape item 59's coverage guard had just found four untested commands in. Both permission tables in `internal/config` were that shape, and neither was merely untested.
    * `secretPatterns` denied the absolute spelling of seven credential files and allowed the relative one. The patterns read `*/.npmrc`, and `*` matching any run of characters still leaves the `/` a literal the subject must carry. The subject arrives as the model wrote it, so the guarded spelling was the one that almost never arrived: `.npmrc`, `.pypirc`, `.aws/credentials`, `.aws/config`, `.kube/config`, `.docker/config.json`, `.gnupg/secring.gpg` and `.ssh/config` were all readable with Smart Agent on. The existing test passed because its one `.npmrc` path was `/home/u/.npmrc`. Two entries — `.env` and `.netrc` — were listed bare beside their prefixed forms, so whoever wrote the list saw this twice and the other seven were not caught.
    * `wideProgramNames` never trimmed `.exe`, so on Windows an "always" on one narrow command became a wildcard: `rm.exe -rf build` persisted `rm.exe *`, and so did `python3.exe`, `bash.exe`, `node.exe` and `sudo.exe`. Two names were worked around by listing them twice; the other forty-five were not.
    * The guards state the requirement rather than the list: a table of credential files each checked relative, absolute and tilde, because keying the test on the patterns is what let the hole survive. Reverting either fix fails them with the symptom.

61. **What the v0.120.0 sweep found, and what it did not.**

    * Ninety-five candidates were raised from six angles — this file's own open items, the UI ideas table, the CI and gate lanes, source markers, hand-maintained lists with no guard, and documentation drift — and each was verified against the code by a separate reader, because a record saying "Open" two releases after the work shipped is the failure mode this file has.
    * Seventy-four are genuinely open or partial, twelve had already shipped without the record being updated, and nine were notes with no work in them. The stale twelve are corrected in place above.
    * The largest single gap is coverage, not features: there is no Linux or macOS CI job at all, so `vet`, `gofmt`, `-race`, `deadcode`, the doc-link check and the Web UI suite run only on the developer's machine, and 23 packages with tests — `internal/agent`, `internal/tui`, `internal/tools` and `internal/provider` among them — never run on Windows. Item 33 records the decision to defer a second workflow; that decision predates v0.118.0 shipping green with 27 failing Windows tests.
    * `scripts/check-fmt.sh` discarded `gofmt`'s stderr and forced success, so a Go file that would not parse passed the fmt check — the fifth check-that-cannot-fail found in three releases, and in a script whose own comment exists to explain why the naive version cannot fail. Fixed in v0.120.0.

62. **Six records describing shipped work as remaining. Corrected in this pass.**

    * Items 39, 31, 35, 26, 28, and 36 each described work as remaining that the code already did: `/redo` end to end, all four debate parts (each past its bullet), the `check` validator on the default path, the unconditional trace writer with retention, a provenance class used in three production sites, and orchestration's stage events, durable per-unit answers, and child-session deletion. It had already happened that week: somebody was sent to build something that exists.
    * The cause is structural, not carelessness. Nothing ties an item's status to the code it describes — a record goes stale the moment the work ships, and no check fails when it does. The v0.120.0 sweep found twelve of the same shape for the same reason. A file whose value is being the record cannot rely on being re-read.
    * What was done about it: each correction above cites the file that proves it, so the next reader can re-verify rather than re-trust; settled exclusions (the two-name capture set, per-item pipelining) are named as decisions with their locations instead of sitting in missing-capability tables where they read as oversights; and prior questions (item 31's table) are labelled as questions so nobody picks one up as a task. What was not done: no status is wired to any test, so this file will go stale again — that wiring is the actual remaining work, recorded here so it is not lost with the rest.

63. **Seven questions that were somebody's to answer, answered: none of them.**

    * A scoping pass over the eight partially-done items separated what the code could settle from what it could not, and the second list came back to the owner. All seven are declined. They are recorded here rather than left out, because a question nobody wrote down is a question the next sweep re-opens — twelve items came back that way in v0.120.0.
    * **Signing and notarization.** Who holds the Apple Developer ID and the Windows code-signing certificate, and where the private keys live during a release cut from one machine. Declined. The macOS `.app` stays unsigned and the `.msi` unsigned, which is a real cost to somebody installing either, and it is the cost being accepted rather than an oversight. Nothing in the tree references a key, and no build script asks for one.
    * **A known-server registry for MCP.** What the source of truth for "known" would be. Declined, and it is the one with no buildable answer either way: there is no list, no feed and nothing to fetch, so the choice was between inventing a curation source and not having the feature. Declared-version pinning, the half that needed no such source, shipped instead.
    * **Language-specific syntax validators.** Whether to ship Go validation through `go/parser`, which would be exact and small here, or stay language-agnostic. Declined. localcode stays agnostic and leans on the project's own `verify_command` and `post_tool_use` hook, which already reach the default path.
    * **Widening the rewind capture set to MCP filesystem writes.** Declined. The two-name set is a documented structural exclusion, not an omission: a restore that quietly puts back three of the five files a turn changed is worse than one that puts back none, and every reply says which files it could not cover.
    * **A retention default for orchestration child sessions.** How many to keep. Declined, which leaves the sessions on disk and the deletion machinery unused — the mechanism exists for a policy nobody has chosen, and that is the state, written down.
    * **Turn-log compression.** Declined, and it was declined once already: `docs/SMART_AGENT_DEFERRED_ITEMS_IMPLEMENTATION_2026_08_26.md` records under its design decisions that "compression is not implemented. The recorded decision favored bounded line-JSON retention over maintaining another file format." The bound it was meant to provide exists. Re-asking a settled question and getting the same answer is worth recording once so it stops being asked.
    * **Per-item pipelining in Orchestrate.** Declined, and likewise already settled in the code itself: `internal/agent/orchestrate.go:66-69` states the tradeoff and the decision — "pipelining an item through the remaining stages on its own buys wall clock and costs a per-item state machine, and it is deliberately not in this version."
    * The pattern in the last two is worth naming: both were listed as open work in this file while the decision against them sat in a source comment and a design document. A record that does not know what the code already decided will keep proposing it.

## UI ideas

### Web UI

| Idea | Status and scope |
|---|---|
| Markdown rendering | Done in v0.27.0. The dependency-free renderer supports headings, emphasis, code, lists, blockquotes, links, and rules in the Web UI and GUI. Code syntax highlighting remains open. |
| Collapsible tool-call cards | Done. `internal/daemon/static/js/transcript.js` expands tool input and output. The row said "Open" until the v0.120.0 sweep read the code. |
| Diff viewer | Open. Before-and-after views for `edit` and `write_file` results. |
| Persistent permission approval | Done in v0.20.0. Options: allow once, allow for session, and always allow. The last option writes a matching config rule. |
| Usage visualization | Open. Per-model token bars and context-use indicator. |
| Session completion indication | Done in v0.40.0. The session light identifies running work and unread completed replies. Cross-client activity sources were aligned in v0.42.0. Running indicators became amber in v0.52.0 across the session list, prompt status, and task panel. Green indicates an available session. |
| Session search and filter | Open. Search by title and workspace. Manual card ordering shipped in v0.43.0. |
| Scroll control | Done in TUI v0.31.0 and Web UI v0.51.0. Output follows only when the view was already at the bottom before an update. Web UI and background-task windows provide a jump-to-bottom control. |
| Workspace is process-wide | Done in v0.39.0. Relative paths resolve per session. `os.Chdir` was removed. File tools use the session directory and bash uses `cmd.Dir`. Only the session's own running turn blocks its workspace switch. |
| Per-session workspace | Added in v0.28.0; underlying isolation completed in v0.39.0. Switching one client's session no longer changes another client's workspace. |
| Dark/light theme and mobile layout | **Declined.** v0.52.0 introduced role-based color properties with a test for undefined properties, so a light theme is a second value set away — and the decision is not to add one, nor a mobile layout, nor `/themes`. |
| MCP server status | Partial. `/status` reports each server's connection state and its last error (v0.116.0), and `/mcps` turns one off for one conversation (v0.119.0). Remaining: a control in the Web UI panel. The cheap version sends `/reset-mcp`, which is a machine-wide reset rather than a per-row retry. |

### TUI

| Idea | Status and scope |
|---|---|
| Markdown and code rendering | Open. Consider a renderer such as glamour. |
| In-program session picker | Done in v0.59.0. `/session` selects a conversation without restart. `/model` provides agent selection. Session deletion remains in the startup picker, not the in-program picker. |
| Tool progress | Done in v0.25.0; extended in v0.32.11. Displays the running tool, queue depth, and background-task count. Transcript tool-call entries persist. Elapsed time remains open. |
| Context indicator | Done in v0.119.0. The terminal was ignoring usage events entirely; it warns at 70% of the window and again at 90%, matching the Web UI. The 85% in the original proposal was not adopted — the two clients agreeing matters more. Remaining: a graphical bar rather than a percentage. Note that auto-compaction fires at 50% by default, before either warning. |
| History search | Open. Search earlier output with the `/` key. |

### Both clients

| Idea | Status and scope |
|---|---|
| Daemon-provided help | Partial in v0.60.0. `GET /api/slash-commands` supplies command names and descriptions for completion. Help strings remain client-owned. Release-verification tests require both help strings to include every `SlashCommands()` entry. Initial tests found one missing TUI command and eight missing Web UI commands. A shared endpoint such as `GET /api/commands/help` remains a proposal. |
| English program output | Done in v0.13.0. Documentation followed in v0.19.0. |
