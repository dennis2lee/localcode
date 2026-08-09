# Code review — August 2026

A full-repository bug hunt across the Go backend, the Web UI, and the
build/CI surface. Six reviewers worked in parallel, one per subsystem;
findings below are consolidated and de-duplicated. Several defects were
found independently by more than one reviewer — those cross-confirmations
are marked, and carry the most weight.

**Status:** C1 and C2–C4 are **fixed** (see the FIXED markers below).
Everything else stands as reported.

## Coverage

| Area | Status |
|---|---|
| agent / session / events / memory | reviewed |
| dictation (whisper + sherpa) | reviewed |
| tools / shell / rules / hooks / mcp | reviewed |
| config / provider / credentials / awssso / gui / cmd | reviewed |
| markdown renderer (XSS surface) | reviewed (author, direct) |
| uploads path handling, client reconnect | spot-checked (author, direct) |
| **daemon HTTP / SSE / client / TUI (full)** | **not completed** — reviewer hit the account session limit |
| **Web UI JS (full) / build scripts / MSI / workflows** | **not completed** — reviewer hit the account session limit |

The two incomplete areas are not "clean" — they are unreviewed. The
highest-risk items in them (upload path traversal, SSE reconnect tight-loop,
markdown XSS) were checked directly and are noted as cleared; everything
else in those two areas still wants a pass.

Severity: **critical** = data loss, security, or a session that breaks and
stays broken. **major** = wrong behavior a normal user will hit. **minor**
= narrow, cosmetic, or needs an unusual setup.

---

## Critical

### ~~C1~~ FIXED. Shell permission bypass: an escaped quote hides a chained command from the allow/deny check — arbitrary commands auto-run with no prompt
`internal/config/shell.go:76-124` (`splitShellSegments`) — *cross-confirmed by two reviewers; verified directly by the author against both the real function and real bash.*

The segment splitter opens a quote context on a `"` that appears **outside**
quotes, but bash treats that same `"`, when backslash-escaped (`\"`), as a
literal character that does **not** open a quote. The two disagree, and the
permission check trusts the splitter.

Verified:

```
input:  git status \" && rm -rf ~
split:  ["git status \" && rm -rf ~"]   (ONE segment)   unsafe=false
```

That one segment matches the built-in `git *` auto-allow rule, and
`hasUnsafeShellConstruct` sees no `$(`, backtick, or `>` — so it resolves to
**allow, no prompt**. Real bash then runs it as a chain (confirmed: the part
after `&&` executed). `\'` does the same via the single-quote path.

Impact: any allowed command prefix (`git`, or any user allow rule) becomes a
launch point for arbitrary code. The model — or a prompt-injection payload in
a file the model reads — can run `rm -rf ~`, exfiltrate, or install
persistence, all without the permission prompt the whole system exists to
show. This defeats the exact "anything appended to an allowed prefix" attack
the file's own header says the splitter was written to stop.

**Fixed.** `splitShellSegments` now models bash's backslash rule outside
quotes: an unquoted `\` makes the next character literal, so it can neither
open a quote nor act as a separator. The payload above now splits into
`["git status \"", "rm -rf ~"]`, the `rm` segment matches no allow rule, and
the line falls back to `ask`. Covered by
`TestSplitShellSegmentsHonorsBackslashOutsideQuotes` (7 cases, including
Windows paths and a trailing backslash) and
`TestEscapedQuoteCannotSmuggleACommandPastAnAllowRule`.

### ~~C2~~ FIXED. A cancelled turn (Esc before the first token) writes an empty assistant message that permanently breaks the session
`internal/agent/turn.go:97-105`; provider `send` sites in `bedrock.go:249-256`, `anthropic.go:223-230`, `openai.go` — *cross-confirmed by two reviewers.*

On context cancellation every provider's stream goroutine returns and closes
its channel **without** emitting a terminal `EventMessageStop`/`EventError`.
`consumeStream` reads the closed channel as a normal end and unconditionally
appends `provider.Message{Role: Assistant, Content: assistantBlocks}` — with
zero blocks when the user cancelled before the first delta (common: Esc during
prefill of a long prompt, or the `POST /sessions/{id}/cancel` button).

Anthropic and Bedrock both reject a message with empty content, so **every
later turn in that session 400s**. History is rehydrated from the event log,
so it survives a restart — except `rehydrateHistory` happens to drop the empty
message (`rehydrate.go:108`), which means a daemon restart silently repairs
what the live process broke. That divergence is itself a smell, and confirms
the live path is the buggy one.

### ~~C3~~ FIXED. A provider error mid-turn leaves history ending in a user message; Bedrock then rejects every retry, forever
`internal/agent/turn.go:74-105` + `internal/provider/bedrock.go:73-120` (`toBedrockMessages`, no consecutive-role merge) — *cross-confirmed by two reviewers.*

The user message is appended to history **before** the provider call. If
`p.Chat` errors synchronously (e.g. a Bedrock `ThrottlingException` — no
adapter has retry logic) or the stream returns an `EventError`, the function
returns before the assistant message is appended. History now ends with a user
message; the user's retry appends another → `[…, user, user]` → Bedrock's
Converse API rejects non-alternating roles with a `ValidationException`, on
every retry. The code already knows Bedrock rejects back-to-back user messages
(the `takeInjected` comment guards exactly that) but only on the injected-input
path, not the error path. A defensive merge in `toBedrockMessages` would
contain the whole family (C2, C3, C4).

### ~~C4~~ FIXED. Auto-compaction always produces two consecutive user messages on the next turn — breaks Bedrock the moment it fires
`internal/agent/compact.go:88-91` + `turn.go:61-77`.

`compactHistory` collapses history to a single `RoleUser` summary. The next
turn appends the new user message → `[user(summary), user(prompt)]`. Anthropic
and OpenAI accept it; **Bedrock rejects it** — so on Bedrock, the instant
auto-compaction fires at 80% context (or the user runs `/compact`), the next
turn errors, and per C3 the session never recovers. Nothing merges the summary
into the following user turn.

### Fix for C2–C4

One guard plus two source fixes, in `internal/agent`:

* **`history.go` (new) — `sendableHistory`.** Applied to every outgoing
  request (`turn.go`, and the compaction call in `compact.go`). Drops empty
  messages and merges consecutive same-role messages, so both texts still
  reach the model, in order, in one message. A guard rather than a design:
  history should already alternate, but the cost of it not doing so was out of
  all proportion to the mistake.
* **`turn.go` — no empty assistant message.** A turn that produced no blocks
  records nothing, matching what `rehydrateHistory` already reconstructed from
  the log.
* **`compact.go` — the compaction request is normalized too.** History
  routinely ends with a user-role message (tool results are one), so appending
  the instructions after it made two in a row — meaning on Bedrock the
  compaction call itself failed, which is the one call that must not, since it
  is what rescues a session that has run out of context. This instance was not
  in the original report.

Verified end-to-end through the real `Loop` and a real HTTP provider, asserting
on the JSON that actually goes out:
`TestRetryAfterProviderErrorSendsAValidConversation` and
`TestTurnCancelledBeforeFirstTokenLeavesAUsableSession`. **Both were confirmed
to fail with the fix reverted** (`retry request has two "user" messages in a
row`) and pass with it.

`TestAutoCompactTriggersAboveThresholdAndResetsHistory` was asserting the old
shape — "system + summary + new turn = 3 messages", i.e. the two-consecutive-user
bug as expected behavior. It now asserts 2 messages, that no two adjacent
messages share a role, and that both the summary and the new prompt reach the
model.

---

## Major

### M1. One slow transcription freezes the entire dictation API for up to a minute
`internal/dictation/manager.go:112-121` + `dictation.go:88-113`.

`Session.Write`/`Stop` hold the per-session lock across `Final()` →
`transcribe`, which blocks up to 60s on a wedged whisper-server. The reaper
holds the **manager** lock while calling `s.Idle()`, which needs that same
per-session lock. So one stuck transcription blocks the reaper, which blocks
`Manager.Get`/`Start`/`Stop` — every other client's audio POSTs, new
dictations, and mic-off clicks hang. `Idle` should not need the session lock
(snapshot `lastActivity` atomically), and `Final` should not be called under a
lock the reaper can wait on.

### M2. `acquireWhisper` kills an engine other sessions are actively using
`internal/dictation/whisper_process.go:100-103`.

On a key mismatch the shared process is killed **without consulting its
refcount**. A second recognizer opened with a different `WhisperModel`/`Threads`
(or a CLI path alongside the daemon) kills the engine out from under a live
`Final()` — the in-flight request resets, and the victim session silently
degrades to stale partials with no error. Compounding it: the key embeds raw
`cfg.Threads`, but `startWhisper` normalizes `0` to `defaultThreads()`, so
`Threads:0` and `Threads:4` (when the default is 4) start identical servers
under different keys and kill-restart each other for no semantic difference.

### M3. Interrupted event log lines are silently truncated, then seq numbers get reused
`internal/session/session.go:539-551` (`restoreOne`).

`restoreOne` uses a 1MB-capped `bufio.Scanner` and never checks `scanner.Err()`.
No tool truncates its output (bash/read/search have no size cap), so a single
`tool.end` carrying a big file read or chatty build log exceeds 1MB. `Scan`
stops, every later event is dropped, and `nextSeq` restores **below** the true
max — so post-restart appends reuse seq numbers already in the file. That
breaks `Events(since)` / Last-Event-ID replay and silently drops the tail of
the rehydrated conversation.

### M4. Concurrent appends to one session persist out of order and can race `Delete`'s file close
`internal/session/session.go:369-377`.

The JSONL write happens **outside** the store lock. A background `TaskManager`
task appends `task.status` to the *parent* session while the parent's turn
appends deltas — two appends take seq N and N+1 under the lock, then write to
the file in reverse. Worse, `turnTracker.busy` tracks daemon turns but **not**
TaskManager tasks, so `DeleteAll` can `Close` a running task's log file
mid-write; with fd reuse the stray write can land in a *different* session's
log.

### M5. MCP auth token leaks across a cross-host redirect
`internal/mcp/mcp.go:479-488` (`headerTransport.RoundTrip`).

Headers (e.g. `Authorization: Bearer …`) are injected at the RoundTripper
level on every hop. Go's client strips sensitive headers on a cross-host
redirect only for headers set on the *initial request* — not for ones a custom
RoundTripper re-adds each call. A remote MCP endpoint (or an on-path attacker)
answering `307 Location: https://evil/…` gets the bearer token attached to the
request to `evil`. Fix: a `CheckRedirect` that drops the injected headers on
host change.

### M6. Deny rules are defeated by trivial shell quoting (the milder sibling of C1)
`internal/config/shell.go:20-124`.

A deny rule `curl *` is not matched by `c\url …`, `"curl" …`, or `cu''rl …`,
all of which run `curl`. Without `skip_permissions` the hard deny silently
weakens to a click-through `ask`; **with** `skip_permissions` (`rules.go:198-201`)
that `ask` downgrades to `allow`, so an explicitly denied command runs with no
prompt. Same root cause as C1 (string-glob matching of shell syntax); worth
fixing together.

### M7. Delegation failure records the prompt in the log but not in history → rehydration divergence
`internal/agent/delegate.go:58-67`.

`delegatePrompt` writes the `message.user` event before `SpawnSync`; on failure
it writes only an error event and returns, so live history has no trace. After
a restart, `rehydrateHistory` replays that user message with no assistant reply
after it — a dangling user turn, which on the next real turn produces
consecutive user messages (→ Bedrock rejects) and on all providers shows the
model a prompt it never answered.

### M8. Empty-content assistant message rejected on cancel — provider contract
`internal/provider/{bedrock,anthropic,openai}.go` `send` — see C2. Listed
again here because the fix belongs partly in the adapters: on `ctx.Done` they
should emit a terminal event so `consumeStream` can distinguish "cancelled,
nothing produced" from "clean finish", rather than the consumer guessing.

### M9. `mcp get` prints stdio env values in plaintext
`cmd/localcode/mcp_list.go:154-156`.

`printMCPServerDetail` does `env: KEY=VALUE`, while the same file's
`formatMCPCommand` documents that env/header *values* are never printed. A
server added with `-e GITHUB_TOKEN=ghp_…` leaks the token into scrollback or a
pasted bug report. Header values are correctly hidden; env values are not.

### M10. OpenAI-compat usage dropped when the backend attaches it to a content chunk
`internal/provider/openai.go:247-256`.

The `chunk.Usage != nil` read is nested inside `if len(chunk.Choices) == 0`.
vLLM and several OpenAI-compat proxies put `usage` on a chunk that still
carries a choice, so their token accounting is silently lost and the
context-window meter reads nothing.

---

## Minor

- **`internal/dictation/manager.go:49-61,131-149`** — no `closed` state: `Start`
  racing `Close` can spawn a fresh engine *after* `Shutdown`, orphaning a
  whisper-server the exact way the recent fix was meant to prevent. Needs a
  `closed` flag checked under `m.mu`, re-checked at insert.
- **`internal/dictation/whisper_install.go:120-136`** — zip extraction is
  non-atomic and readiness is a bare existence check, so a Ctrl-C mid-extract
  leaves a truncated engine that permanently satisfies "installed" and can
  never self-repair (re-running install is a no-op). The sherpa path got
  stage-and-rename for this reason; the whisper path didn't.
- **`internal/dictation/whisper_process.go:153-188`** — data race on the shared
  `bytes.Buffer`: the exec output-copier goroutine writes while `waitReady`'s
  timeout path calls `tail()` → `log.String()`. `go test -race` on a slow
  startup flags it. (The `alive()==false` branch is safe; the timeout branch is
  not.)
- **`internal/dictation/whisper_process.go:347-354`** — `freePort` TOCTOU:
  the probe port can be taken between listener-close and the child's bind, and
  `waitReady`'s bare TCP dial can bless a *different* process's server. Re-check
  `alive()` after a successful dial.
- **`internal/agent/taskmanager.go:72-87`** — `Spawn` leaks one
  `context.WithCancel` per task (deleted from the map but never called on normal
  completion); grows unbounded over a long daemon run.
- **`internal/agent/taskmanager.go:105-113`** — a task cancelled mid-run is
  reported `status:"failed"` with a context-cancelled error rather than
  `"cancelled"`, so a deliberate stop shows a scary failure.
- **`internal/agent/permission.go:133-137`** — a cancelled turn removes the
  pending permission but never writes a `permission.resolved` event, so a client
  replaying the log renders a prompt that can never be answered.
- **`internal/session/session.go:239-262`** — `Delete` never notifies or closes
  the deleted session's live SSE subscribers; they block until the HTTP client
  itself disconnects. Also `Append`'s marshal error is swallowed (in memory but
  never on disk → replay divergence).
- **`internal/hooks/hooks.go:103-131`** — a `pre_tool_use` hook that hangs is
  killed at 30s and the tool then **proceeds** (fail-open). Documented as
  intentional, but a hook whose job is to *block* should be able to fail closed;
  worth a config knob.
- **`internal/commands/commands.go:185-195`** — slash-command `!…` shell
  expansion runs with `context.Background()` and no timeout, unlike the bash
  tool (2m) and hooks (30s). A blocking embedded command hangs the turn.
- **`internal/tools/{write,edit,read}.go`** — file tools follow symlinks and the
  permission subject is the literal, un-normalized path. An in-workspace allow
  glob is satisfied by a path that is a symlink out of the workspace, or by one
  with `../` components matched verbatim. There is no workspace-root confinement
  at the tool layer — only whatever the permission glob expresses.
- **`internal/mcp/mcp.go:516-552`** — `reconnect` runs `connectOne` outside the
  lock; two concurrent `Execute` calls to the same server can both redial and
  both store into `e.session`, leaking the loser's session (fd/pipe/process).
- **`internal/awssso/awssso.go:107`** — registration expiry hardcoded to
  issued-at + 90d instead of AWS's returned `ClientSecretExpiresAt`; a shorter
  real expiry gets written too-late into the shared cache, so refresh fails
  opaquely instead of prompting re-login.
- **`internal/gui/gui.go:52-81`** — data race on `startErr`/`cleanup` if the
  window closes before `start` finishes; minor (process exits immediately).
- **`internal/modelinfo/modelinfo.go:23-31`** — Claude 3.x IDs
  (`claude-3-5-sonnet-…`, `claude-3-opus-…`) don't match the 4.x substring
  checks, so they fall back to 128k instead of 200k and the context meter
  over-reports ~1.6x. UI-only.
- **`internal/provider/openai.go:297-304`** — `ToolUseEnd` events are emitted by
  ranging a map, so multiple tool calls surface (and execute) in random order
  rather than the order the model issued them.
- **`internal/dictation/download.go:98-160`** — SIGKILL/power-loss strands
  `.download-*` / `.unpack-*` temp files (190–400MB) that no later run sweeps;
  they accumulate invisibly beside the binary. A cheap sweep at the top of
  `install` fixes it.
- **`internal/dictation/engine.go:93-98`** — `Available()` ignores a config
  `whisper_bin` (reports "no recognizer" when `Ready()`/`Open()` would succeed),
  and it gates the sherpa-only `dictation test`, so on a non-gui build with
  whisper installed the friendly guard is bypassed and `Diagnose` returns raw
  `ErrUnavailable`.
- **`internal/daemon/dictation.go:63-79`** — an audio POST racing the reaper
  gets a 400 ("session already stopped") instead of the 404 the client's
  session-expiry handling keys off; wrong signal, harmless server-side.

---

## Checked and cleared

These were suspected and traced to be **safe** — recorded so they aren't
re-investigated:

- **Markdown renderer XSS** (`static/js/markdown.js`) — verified directly with
  the real renderer. `<img onerror>`, `<script>`, `javascript:` links, and
  attribute-breakout via a `"` in a link URL are all escaped (raw text is
  escaped in step 2 before any markup is introduced; the link regex only
  accepts `https?://` and the `"` is already `&quot;` by the time `inline`
  runs). Code spans and fenced blocks round-trip correctly.
- **Upload path traversal** (`internal/daemon/uploads.go`) — filename is
  `filepath.Base`'d and `.`/`..` rejected; the session-`id` path segment is
  gated by `Store.Get(id)` succeeding, so a traversal id isn't a real session
  and 404s before any path is built.
- **Client SSE reconnect** (`internal/client/client.go:320-342`) — has a 1s
  backoff (`time.After(time.Second)`), not a tight loop on a dead daemon.
- **Session subscriber fan-out** (`Append`/`Broadcast`/`Subscribe`) — send
  under lock, non-blocking, `markLost` via `sync.Once`; no send-on-closed
  window. (The file-write races M3/M4 are separate.)
- **Usage/TPS accounting** — internally consistent; `rehydrateUsage` matches the
  live path one-for-one including compaction billing.
- **`takeInjected` / injected-message rehydration** — round-trips in both event
  orderings.
- **config load/merge/rawfile** — every field merged (guarded by
  `TestMergeFieldsGuard`), unknown keys preserved with atomic temp+rename at
  0600, runtime-mutated fields consistently behind their mutexes.
- **credentials / awssso device flow** — 0600/0700, nothing logged (except the
  expiry item above).
- **`childproc.Hide`** — correct (`CREATE_NO_WINDOW` + `HideWindow`, nil-guarded,
  no-op off Windows).
- **`rules.go` `@import`** — skipped inside code spans, depth-capped, missing
  file degrades to literal; no attacker-controlled expansion.
- **`globMatch`** — two-pointer wildcard walk verified correct.
- **VAD `feed(nil)`, `forced` reset, utterance-generation, partial-frame
  carryover** — all traced sound.
- **Download / engine checksums and archive traversal** (`safeJoin`,
  base-name-only zip) — pinned, verified before placement, traversal-safe.

---

## Recommended order

1. ~~**C1**~~ and ~~**C2 / C3 / C4**~~ — done.
2. **M6** — shares C1's root cause and is *not* fixed by it: a deny rule is
   still evadable by quoting (`"curl" …`, `cu''rl …`), because matching is a
   glob against text that still contains the quotes. Normally that weakens a
   deny to an `ask`; **with `skip_permissions` on it becomes an `allow`**, so
   an explicitly denied command runs unprompted. Needs unquoting/normalization
   before matching, which C1's parse fix does not provide.
3. **M1** (dictation lock-up) and **M5** (MCP token leak).
4. **M3 / M4** (log truncation + unordered/racy writes) — data integrity.
5. The rest as capacity allows.

## Note on completeness

The two subsystems marked *not completed* above (full daemon HTTP/SSE/TUI, and
the full Web UI JS + build/MSI/CI surface) were cut short by an account session
limit, not by being clean. Re-run those two reviews after the limit resets to
close the gap. Everything else here was traced end-to-end by its reviewer;
the items labelled *verified directly* were additionally reproduced by the
author against the real code.
