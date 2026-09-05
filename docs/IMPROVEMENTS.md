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

44. **No `top_p` or `top_k` on OpenAI-compatible requests. Open.**

    * `oaRequest` carries `temperature` and nothing else from the sampling family, so a profile cannot ask for the `top_p` 0.95 and `top_k` 64 that muse's vLLM recipe specifies alongside temperature 1.0. `/llm-doctor` sets them directly on its own request bodies; a profile has no way to.
    * Adding them means a config field each and a decision about servers that reject `top_k`, which is not in the OpenAI schema and which vLLM accepts as an extension.


45. **Smart Agent's orchestration prompt told a muse to delegate to itself. Confirmed and fixed in v0.108.0.**

    * Reported: turning Smart Agent off noticeably reduced looping on Muse-Glimmer-30B. The bundle changes eleven things for the model (see `smartOn` and `smartAgent` sites), and the likeliest cause is the orchestration prompt `localVariant` in `internal/smart/prompt.go`, which every local family gets: "Searching the codebase: use Task with the explore agent. Do not grep the whole project yourself." and the same for review and verification. In a one-profile config every specialist is the same muse model, so the orchestrator delegates a search to itself, gets a summary it did not write, and re-delegates. Each Task is a full extra model call in a fresh context.
    * Second candidate: the paged `read_file` (800 lines with a `read on with offset=N` footer). A model that does not carry the offset re-reads the same window, which the repeat guard then ends.
    * How to confirm from what Smart Agent already records: with it on, every turn is in `~/.localcode/trace/localcode-<day>.jsonl`. Count `span == "tool"` records with `tool == "Task"` per `trace_id`, and count `span == "tool"` records per trace whose `parent_session_id` is set. A loop that is delegation shows as many Task spans under one trace; one that is re-reading shows as repeated `read_file` spans with the same session. The `stopped: N steps in a row only repeated` notice (v0.95.0) names the calls in the transcript either way.
    * If confirmed, the fix is a muse-specific orchestration variant that recommends delegation rather than ordering it, or that leaves the delegation tools out of a one-profile config where the specialist would be the orchestrator's own model. Not done unasked: it changes what every muse session is told.

    * **Confirmed against the reporter's own model**, a 30B muse served by LM Studio, through a shim that recorded every request. Same task, same workspace, one switch. With Smart Agent off: thirteen requests, two edits, both files correct. With it on: the orchestrator's first move was `Task(agent: "explore")`, and the sub-agent — the same muse — spent thirteen requests on `glob` and `grep` in a two-file project, found the answer at the eleventh, carried on, edited nothing, and was still running when it was cancelled. The two system prompts measured 4190 and 3547 characters, so the prefix cache could not hit either, exactly as predicted here.
    * **The second candidate is ruled out for this failure.** `read_file` was called twice with no repeat; the loop was delegation, not paging.
    * **Fixed by roster shape rather than by model id.** `smart.Solo` reports whether every routing category resolves to the same profile, and `OrchestrationPrompt`/`PlanPolicy` take it. The test is what the categories *resolve to*, not `len(Profiles) == 1`: `classify` reads a weight class out of a model id, so two local endpoints whose ids carry no size qualifier route everything to one profile — the population this exists for, and what a profile-count test would have let through.
    * The solo prompt states what delegation still buys — a context this conversation does not pay for — instead of ordering it, and says plainly that review, planning and running the build are not worth handing to the same model. Re-measured on the same model and task: no `Task` span at all in the trace, edits at request four, thirteen requests, both files correct. The penalty for having the switch on is gone.

46. **The window's installed copy stops updating after a startup handoff. Windows only, open.**

   * On Windows an .exe cannot be replaced in place, so a startup update stages the new binary under `%LOCALAPPDATA%\localcode\bin` and the installed copy under Program Files — the one the shortcut starts — keeps whatever version the MSI put there.
   * The window then serves `successorProxy`, which forwards `GET /api/update` to the successor. The successor answers with its own version, which is the staged copy's and is current, so `available` is false and the settings panel says "localcode x.y.z is the latest release" with no install button.
   * The reply that creates the situation promises the opposite (`internal/update/install.go`: "the copy under … is still the old version, and the settings window's install button updates it"), and that button is now hidden by the same answer that hid the problem.
   * What still reaches the user: the daemon and the whole Web UI, because those are the staged copy. What never does: the window shell itself — the frameless chrome, the resize edges, `RegisterApplicationRestart`, the splash.
   * Not fixed here because the fix is a decision rather than a patch. Answering `GET /api/update` from the window's own version is easy; making `POST /api/update/install` mean "replace the installed copy" requires the *window* process to run the install, and after a startup handoff the window has already discarded its daemon. Either the window keeps enough of one to install with, or the successor is told which file to replace.


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
