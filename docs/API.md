# HTTP API reference

Source of truth: the 59 `d.mux.HandleFunc` registrations in
`internal/daemon/daemon.go` (function `routes`). Anything here that
disagrees with that list is wrong; change this page, not the list. A test
(`internal/daemon/api_doc_test.go`) fails when a registered route is not
named on this page.

Base URL: the `--listen` address (default `127.0.0.1:4096`). The Web UI is
served from `/` on the same port; every route below lives under `/api/`.

## Read this first: no auth, no stability

There is no auth token. Anyone who can reach the `--listen` address gets
the whole API, shell execution included: sending a message starts a turn,
and a turn runs the bash tool. `USAGE.md` says the same under [Remote
daemon over an SSH tunnel](USAGE.md#remote-daemon-over-an-ssh-tunnel).
Bind loopback, or reach a remote daemon over an SSH tunnel. Never bind
`0.0.0.0` on an untrusted network.

There is no stated API stability. The surface has changed release to
release (per-session workspaces in v0.39.0, per-model effort levels,
schedule rows), and no version is negotiated: the only version signal is
`GET /api/version`, which names the daemon build. Pin the daemon and the
client to the same release when both are yours.

## Conventions

* JSON both ways, except where noted. Error answers are usually
  `{"error": "..."}`; a few handlers use plain-text `http.Error` instead,
  which the tables call out as `text error`.
* JSON request bodies are capped at 1 MB (`maxJSONBody` in
  `internal/daemon/daemon.go`). Uploads are multipart and capped
  separately at 32 MB per file.
* Status codes and what each client does with them:
  * `200` read or applied change, `201` created (session, task, booking),
    `202` message accepted for a turn (`{"status": "accepted"}`) or queued
    into a running turn (`{"status": "injected"}`), `204` applied with
    nothing to return (also: folder picker dismissed, and cancelling a
    turn or task that was not running is still `200` with a false flag).
  * `400` the request is bad (missing field, unknown name, unparseable
    time, repeat options that do not fit). `403` the session is archived
    and must be retrieved first, or the daemon refuses this operation
    over the network. `404` unknown session, task, schedule, or pending
    prompt. `409` a turn (or delete-all, or the old daemon still draining
    a session after an update) holds the session; both shipped clients
    queue on it. `500` the change applied but did not persist, or the
    operation failed mid-way. `501` shutdown is not wired in this daemon.
* Writes to `/api/sessions/{id}/...` pass through the ownership gate
  (`ownershipGate` in `internal/daemon/handoff.go`): while the daemon this
  one replaced is still finishing a session, writes to it get `409` and
  reads pass. `GET` and `HEAD` never block on it.

## Which parts are load-bearing for the two clients

The two clients are the TUI (an HTTP client even when it shares a process
with the daemon, via `internal/client`) and the Web UI (via
`internal/daemon/static/js/api.js`, where every page URL template lives).

* Core turn loop, used by both: sessions list/create, `POST
  /api/sessions/{id}/messages`, `GET /api/sessions/{id}/events`,
  permission and input resolution, `POST /api/sessions/{id}/cancel`.
  Break one and both clients stop working.
* Used by one client: the model picker endpoints (TUI; the page learns
  the model from `model.changed` events), the update check and install
  (page; the TUI updates through `/update`), task spawn/list/output
  (TUI commands and the model's own tools; the page reads task state
  from `task.spawned`/`task.status` events and cancels through `POST
  /api/tasks/{taskId}/cancel`).
* Incidental: `GET /api/trace` has no in-tree caller (the turn log is
  read with `jq` from `~/.localcode/trace/`); `GET /api/sessions/{id}`
  has no in-tree caller (both clients use the list plus the event
  stream); `POST /api/daemon/shutdown` is update machinery, called by the
  replacing process, not by either client.

## The desktop window proxies everything

The window (`cmd/localcode/gui_proxy.go`) serves a reverse proxy onto the
current daemon, so every route below is reachable from the window. Four
routes behave differently there, marked `(window: local)` in the tables:

* `POST /api/workspace/browse` and `POST /api/workspace/reveal` are
  answered by the window process itself with native dialogs. The daemon
  behind the proxy would answer `404` (no picker on a network daemon).
* `GET /api/update` and `POST /api/update/install` are answered by the
  window's own older daemon when one is kept, because the successor is
  current by construction and would always answer "no update".
* `GET /api/workspace` passes through, then the window rewrites
  `can_browse` and `can_reveal` to true.

## Routes

A session object in answers is `{"id", "agent", "title", "workspace",
"order", "created_at", "archived_at?", "parent_id?", "visible",
"permissions?", "effort?", "efforts?", ...}` (see
`internal/session/session.go`). List answers add `"busy"` and `"asking"`.

### Daemon, update, trace

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/version` | nothing | `200 {"version", "pid"}`; the pid tells one daemon from its replacement on the same address | none |
| `POST /api/daemon/shutdown` | nothing | `200 {"stopping": true}`; the process stops after the reply is out. Update machinery, not a client button | `403` this daemon is not one a client may stop; `501` no shutdown wired |
| `GET /api/update` (window: local) | nothing | Always `200`. On failure: `{"current", "checked": false, "detail"}`. On success: `{"current", "checked": true, "source", "latest", "tag", "page_url", "notes", "available", "can_install", ...}` plus `"asset"` and `"size"` when an installable asset fits this platform | none (failures are `200` with `checked: false`) |
| `POST /api/update/install` (window: local) | nothing | `200 {"version", "source", "verified", "started", "replaced", "restarting", "path", "detail"}`. `verified: false` is reported, not hidden | `403` installing is not allowed from here; download/install failures carry their own status |
| `GET /api/trace` | `?limit=` (1-500, default 100), `?session=`, `?trace=` | `200 {"enabled", "records"}`. `enabled` is false with Smart Agent off, which is a setting, not a failure | none |

### Settings (daemon-wide)

`GET /api/settings` answers the whole snapshot, so a client applies
state instead of merging events: `auto_compact_enabled`,
`auto_compact_percent`, `keep_going`, `repeat_limit`, `smart_agent`,
`orchestrate`, `model_invocable`, `model_commands`, `smart_agent_roster`,
`show_tps`, `show_thinking`, `show_timestamps`, `auto_delegate`,
`auto_delegate_agent`, `auto_delegate_match`, `skip_permissions`,
`permission_rules`, `can_edit_permissions`. Changes are announced on the
broadcast as `settings.changed`; a client that was not connected re-reads
this endpoint on load.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/settings` | nothing | `200` the snapshot above | none |
| `POST /api/settings/auto-delegate` | `{"enabled"?, "agent"?, "match"?}`; pointers, so one field can move alone | `204` | `400` unknown agent, fewer than two agents configured, bad body (text error); `500` applied for this run but not persisted (text error) |
| `POST /api/settings/smart-agent` | `{"enabled"}` | `200 {"smart_agent", "applied": true, "persisted", "error"?}`; `applied` is always true, `persisted` is the part that can fail | `400` bad body (text error) |
| `POST /api/settings/orchestrate` | `{"enabled"}` | `200 {"orchestrate", "applied": true, "persisted", "error"?}` | `400` bad body (text error) |
| `POST /api/settings/model-invocable` | `{"enabled"}` | `200 {"model_invocable", "applied": true, "persisted", "error"?}` | `400` bad body (text error) |
| `POST /api/settings/keep-going` | `{"enabled"}` | `204` | `400` bad body (text error); `500` applied but not persisted (text error) |
| `POST /api/settings/repeat-limit` | `{"limit"}`; 0 turns the guard off | `204` | `400` outside 0..max (text error); `500` applied but not persisted (text error) |

### Daemon-wide permission defaults

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `POST /api/permissions/skip` | `{"enabled"}` | `204` | `400` bad body (text error); `500` applied but not persisted (text error) |
| `POST /api/permissions/rules` | `{"tool", "match", "decision"}`; all three required | `204` | `400` a field missing (text error); `500` applied but not persisted (text error) |
| `POST /api/permissions/rules/remove` | `{"tool", "match", "decision"}`; the exact rule to remove | `204` | `400` a field missing (text error); `500` applied but not persisted (text error) |

### Workspace

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/workspace` (window: rewritten) | `?session=`; pass the session, the workspace is per-session and the bare answer is only the default new sessions inherit | `200 {"path", "can_browse", "can_reveal"}` | none |
| `POST /api/workspace` | `{"path", "session_id"?}`; empty `session_id` moves the daemon default for sessions with none of their own | `200 {"path"}` | `400` no path or not a usable directory (text error); `404` unknown session; `409` a turn is running in this session, with `"busy"` naming it |
| `POST /api/workspace/browse` (window: local) | `{"start"?}`; absent means no starting directory | `200 {"path"}`; `204` the picker was dismissed, which is normal | `404` this daemon has no picker (text error); `500` the picker failed |
| `POST /api/workspace/reveal` (window: local) | nothing; `?session=` selects whose workspace. A caller-supplied path is refused by design | `200 {"path"}` | `404` this daemon cannot open a file manager (text error); `500` opening failed |

### Listings (pickers and completion)

Bodies are names and descriptions only, never prompt bodies or tool
implementations.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/mcp-servers` | nothing | `200 [{"name", "status", "detail"}]`; broken servers appear as disconnected, and no servers means `[]`. Live changes arrive as `mcp.status` on the stream | none |
| `GET /api/agents` | nothing | `200 [{"name", "description"?, "model"?, "builtin"?}]`, sorted; declared agents plus the Smart Agent specialists while that switch is on | none |
| `GET /api/commands` | nothing | `200 [{"name", "description"?}]`, sorted; project plus global custom commands. Running one still goes through `POST /api/sessions/{id}/messages` as `/<name>` | none |
| `GET /api/skills` | nothing | `200 [{"name", "description"?}]`, sorted | none |
| `GET /api/slash-commands` | nothing | `200` the daemon's own commands for client completion | none |

### Sessions

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `POST /api/sessions` | `{"agent"?}`, default `general-purpose` | `201` the session, stamped with the live workspace | `400` bad body; `409` every session is being deleted; `500` creation failed |
| `GET /api/sessions` | `?archived=1` for the other list | `200` visible sessions newest first, each with `"busy"` (a turn is running) and `"asking"` (it waits for a person) | none |
| `GET /api/sessions/{id}` | nothing | `200` the session. No in-tree caller; both clients use the list plus the event stream | `404` unknown session |
| `DELETE /api/sessions/{id}` | nothing | `204`. Removes the session, the background work it started, and all their logs; the parent's task row is marked deleted | `404` unknown session; `409` a turn is running |
| `DELETE /api/sessions` | nothing | `204`. Refuses while any session has a turn in flight | `409` naming the busy sessions |
| `POST /api/sessions/order` | `{"ids"}` the dragged panel order, persisted | `204` | `400` bad body or unknown id |
| `GET /api/sessions/groups` | nothing | `200 {"names"}` the panel's groups, in the order they are drawn | none |
| `POST /api/sessions/groups` | `{"names"}` the whole list — **required**, since a missing field would read as an empty list — and `{"rename": {"from", "to"}}` when one of them is an old group under a new name | `200 {"names"}`. A name that is gone and not renamed is deleted; the sessions that were in it are left ungrouped | `400` bad body, no `names`, a name that is empty, padded, over 100 characters, carries a control character or is listed twice, or a rename whose `from` is not a group or whose `to` is not in the list; `500` the list could not be written |
| `POST /api/sessions/{id}/group` | `{"group"}`, or `""` for ungrouped; must already be in the group list, and is matched exactly rather than trimmed | `200` the session | `400` no such group or the session is archived; `404` unknown session; `500` the session could not be written |
| `POST /api/sessions/{id}/agent` | `{"agent"}`; must be one the listing offers | `200` the session; an `agent.switched` event follows | `400` unknown agent or bad body; `403` archived; `404` unknown session |
| `POST /api/sessions/{id}/rename` | `{"title"}`; display only, resolution stays by id | `200` the session; a `session.renamed` event follows | `400` bad body; `404` unknown session |
| `POST /api/sessions/{id}/fork` | nothing | `201` the new session: a verbatim event-log copy (minus `session.renamed`), same effort levels and model choice, titled `fork of X` | `404` unknown source; `409` a turn is running in the source, or delete-all is |
| `POST /api/sessions/{id}/archive` | nothing | `200` the session. Refuses rather than stops running work | `404` unknown session; `400` store refusal; `409` a turn is running, background tasks are running (names them), scheduled runs are in progress, or delete-all is |
| `POST /api/sessions/{id}/retrieve` | nothing | `200` the session, history rebuilt from the log; shelved schedule rows come back, missed ones marked missed | `404` unknown session; `409` busy, or delete-all is |
| `POST /api/sessions/{id}/messages` | `{"text"}`; required. `/clear`, `/rewind`, `/redo`, and `/workspace <path>` sent while a turn runs are refused with `409 {"held": ...}` rather than queued | `202 {"status": "accepted"}` (a turn starts; the reply arrives on the stream) or `202 {"status": "injected"}` (handed to the running turn) | `400` bad body or empty text; `403` archived; `404` unknown session; `409` with `{"held": "/clear"...}` when the text must wait for idle, plain `409` when the begin race is lost repeatedly or the old daemon still owns the session |
| `POST /api/sessions/{id}/uploads` | multipart form, `file` field; name is sanitized to its base, max 32 MB | `200 {"path"}` under `~/.localcode/uploads/<id>/`; splice the path into the next message, the model reads it with file tools | `400` unparseable form, missing field, bad filename; `403` archived; `404` unknown session; `500` disk failure |

### Event stream

`GET /api/sessions/{id}/events` is Server-Sent Events, `Content-Type:
text/event-stream`, with a `: ping` comment every 20 seconds so silent
turns do not look dead. Resume with `?since=<seq>` (wins), the
`Last-Event-ID` header a reconnecting `EventSource` sends back, or
`?tail=N` to open near the end at a turn boundary. Invalid `since`/`tail`
is `400`, an unknown session `404`. The backlog collapses finished
replies (deltas before the last `message.part.end` are skipped; a reply
still streaming keeps its deltas). Daemon-wide events (`mcp.status`,
`session.activity`, `session.archived`, `session.deleted`,
`settings.changed`, `daemon.replaced`) ride the same connection with no
`id:` line and never move the resume point. See [Event types](#event-types).

### Schedules (work booked for later)

The list is read once when a conversation opens; after that the client
follows the `schedule.*` events on that conversation's own stream.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/sessions/{id}/schedules` | nothing | `200 {"schedules": [...]}` | `404` unknown session |
| `POST /api/sessions/{id}/schedules` | `{"when", "prompt", "times"?, "until"?, "keep"?}`; `when` is parsed by the same parser as `/schedule`, `keep: 0` keeps no transcripts | `201 {"schedule", "human", "repeat", "workspace"}` | `400` unparseable time, empty prompt, bad repeat options (the parser's own sentence); `403` archived; `404` no scheduler or unknown session; `409` the booking conflicts |
| `POST /api/schedules/preview` | `{"when"}` | Always `200`: `{"ok": true, "at", "human", "repeat"}` or `{"ok": false, "detail"}`. A half-typed time is not an HTTP error | `400` bad body (text error) |
| `POST /api/sessions/{id}/schedules/{sid}/seen` | nothing | `204`; the third state of the row's light | `404` no scheduler or no such booking |
| `POST /api/sessions/{id}/schedules/{sid}/rename` | `{"name"}`; empty clears it | `200` the entry | `400` bad body (text error); `404` no scheduler or no such booking |
| `DELETE /api/sessions/{id}/schedules/{sid}` | nothing | `204`; also removes the run session it left behind, best effort | `404` no scheduler or no such booking |

### Effort and model (per session)

Both are GET-the-view plus POST-the-change, and both announce on the
session log (`effort.changed`, `model.changed`) so a second client
redraws and a late one replays. Neither is refused while a turn runs; the
change lands on the next turn.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/sessions/{id}/effort` | nothing | `200 {"model", "agent", "level", "source", "levels", "note"}`; the level list belongs to the model in hand, so re-read it after a model switch | `404` unknown session |
| `POST /api/sessions/{id}/effort` | `{"level"}`; empty clears the session answer so the profile's applies again | `200` the view | `400` the level is not a word, or not one this model tells apart; `404` unknown session |
| `GET /api/sessions/{id}/model` | nothing | `200 {"agent", "profile", "model", "provider", "source", "choices"}`; every profile this config can reach, the live one marked | `404` unknown session |
| `POST /api/sessions/{id}/model` | `{"profile"}`; empty clears the choice so the agent's own applies again | `200` the view | `400` no such profile, or bad body (text error); `404` unknown session |

### Session permissions

The four switches are per session (a scratch experiment must not silence
the conversation doing real work). The view is `{"session_id",
"effective", "source", "remembered": {"read": [...], "write": [...]}}`,
where `source` says per switch whether this conversation, its parent, or
config.json answered. Changes are announced as `permissions.changed` in
the session log.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/sessions/{id}/permissions` | nothing | `200` the view | `404` unknown session |
| `POST /api/sessions/{id}/permissions` | `{"switch", "enabled"}`; `enabled: null` clears the session answer so the daemon default applies again | `200` the view | `400` unknown switch or bad body (text error); `404` unknown session |
| `POST /api/sessions/{id}/permissions/forget` | `{"class": "read" or "write"}`; drops the remembered outside-workspace directories | `200` the view | `400` unknown class or bad body (text error); `404` unknown session |
| `POST /api/sessions/{id}/permissions/{permId}` | `{"allow", "scope"?}`; scope is `once` (default), `session`, or `always` | `200 {"status": "resolved"}` | `400` bad body; `404` nothing is waiting on this id (already answered, or its turn ended) |
| `POST /api/sessions/{id}/input/{askId}` | `{"answer"}`; a reply to one mid-turn question, not a policy decision | `200 {"status": "resolved"}` | `400` bad body or empty answer; `404` the question is not pending |

### Background tasks

A task is a child session: `task.spawned` and `task.status` in the
parent's log are the panel rows, which is why they survive a reload.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `POST /api/sessions/{id}/tasks` | `{"agent", "prompt"}`; both required | `201 {"task_id"}` | `400` bad body or a field missing; `403` archived; `500` spawn failed |
| `GET /api/sessions/{id}/tasks` | nothing | `200` the session's tasks | none (unknown session reads as no tasks) |
| `POST /api/sessions/{id}/cancel` | nothing | `200 {"cancelled"}`; false when nothing was running, which is not an error | none |
| `POST /api/sessions/{id}/detach` | nothing | `200 {"detached", "task_id"}`; lets go of the sub-agent blocking the turn so both carry on | none (`detached: false` with no task manager) |
| `POST /api/tasks/{taskId}/cancel` | nothing | `200 {"cancelled"}` | none |
| `GET /api/tasks/{taskId}/output` | nothing | `200 {"output"}`: the task's reply text so far, rebuilt from its deltas, works mid-run | `404` unknown task, or the id is a conversation rather than a task |

## Event types

Every constant in `internal/events/events.go` a client can receive on the
stream, with the payload fields the comments there promise. Per-session
log events carry `seq`; transient broadcast events (`task.progress`,
`thinking.delta`, `thinking.end`) carry none and are missed when missed.

| Type | Payload and scope |
|---|---|
| `message.user` | `{"text", "model_text"?, "local"?}`. `local: true` was answered without a model call and is skipped rebuilding history |
| `message.part.delta` | `{"text"}` fragment of a streaming reply |
| `message.part.end` | `{"text", "failed"?}` the finished reply text; one turn has several when tools ran. `failed: true` closes a reply whose stream died before it finished: the text is on the record and not in the history, which is rebuilt without it. A log written before this key was recorded replays such a reply as finished |
| `tool.start`, `tool.end` | a tool call and its result |
| `permission.request` | a tool approval question; answered at `POST /api/sessions/{id}/permissions/{permId}` |
| `permission.resolved` | how it was answered |
| `permission.forgotten` | `{"class": "read" or "write"}`; remembered directories dropped |
| `permissions.changed` | the session switches view, per session |
| `task.spawned`, `task.status` | the panel rows; a `deleted` status removes one |
| `task.progress` | `{"task_id", "doing"}`; transient, mirrored into the parent |
| `agent.switched` | `{"agent"}` |
| `thinking.delta` | `{"text"}` while reasoning streams; transient, never logged |
| `thinking.end` | reasoning block ended; transient, empty payload |
| `error` | a turn failure; distinct from `turn.cancelled`, which is a person stopping it on purpose |
| `mcp.status` | `{"servers": [{"name", "status", "detail"}]}`; daemon-wide, complete list every time |
| `session.activity` | `{"session", "busy"}`; daemon-wide turn indicator |
| `session.archived` | `{"session", "archived"}`; either direction, daemon-wide |
| `session.deleted` | `{"session"}`; daemon-wide, since the log it would ride in is gone |
| `session.renamed` | `{"title"}` |
| `session.forked` | `{"from", "from_title"}`; reader note, ignored rebuilding history |
| `session.scheduled` | `{"schedule", "name", "run", "run_total", "at", "repeat", "from"}`; reader note, ignored rebuilding history |
| `settings.changed` | every daemon switch as a snapshot, daemon-wide |
| `config.changed` | `{"auto_compact_enabled", "show_tps"}` from `/config` |
| `workspace.changed` | `{"path"}`; per session |
| `usage` | `{"input_tokens", "output_tokens", "cached_input_tokens", "measured", "max_context", "percent", "tps", "show_tps", "model"}`; from reported usage, never estimated. `percent` is of the whole prompt the provider read, `input_tokens` plus `cached_input_tokens`; `input_tokens` alone is what was billed at the full rate. `measured` is the daemon's own character estimate of the same messages, for sizing after a restart. Draw a gauge from `percent`, not from `input_tokens` |
| `compacted` | `{"summary_length", "manual", "summary", "model"?, "input_tokens"?, "output_tokens"?}` |
| `cleared` | no payload; `/clear`, a barrier rebuilding history |
| `rewound` | `{"from_seq", "turn_text", "restored", "skipped", "created"}` |
| `redone` | `{"rewind_seq", "turn_text", "written", "skipped"}` |
| `checkpoint` | `{"tool", "path", "sha256", "mode", "existed", "too_large"}`; bytes live content-addressed beside the log, not here |
| `schedule.created` | `{"id", "at", "prompt", "agent"}` |
| `schedule.status` | `{"id", "status", "run_session"?, "error"?}` |
| `schedule.seen` | `{"id"}` |
| `schedule.renamed` | `{"id", "name"}` |
| `schedule.removed` | `{"id"}` |
| `input.request` | `{"id", "question", "options"?}`; answered at `POST /api/sessions/{id}/input/{askId}` |
| `input.resolved` | `{"id", "answer"?}` or `{"id", "cancelled": true}` |
| `plan.updated` | `{"plan": [{"step", "status"}], "explanation"?}`; logged, unlike `task.progress` |
| `debate.started` | `{"author", "reviewer", "reviewers", "model", "models", "rounds", "task"}`. `reviewer` holds the reviewers joined and `model` the first reviewer's. Clients prefer the plurals and fall back to the singulars for logs written before them |
| `debate.review` | `{"round", "rounds", "reviewer", "model", "text", "approved", "session"}` |
| `debate.ended` | `{"reason" ("approved", "rounds", "stalled", "stopped", "failed"), "rounds", "approved", "note"}` |
| `delegated` | `{"agent", "prompt"}`; a sub-agent answered on its own model |
| `effort.changed` | `{"model", "agent", "level", "source", "levels", "note"}` |
| `model.changed` | `{"agent", "profile", "model", "provider", "source", "choices"}` |
| `turn.done` | no payload; the turn boundary. Gate "waiting" state and queue drain on this, not on `message.part.end` |
| `turn.cancelled` | no payload; stopped on purpose via `POST /api/sessions/{id}/cancel` |
| `daemon.replaced` | `{"version", "pid"}`; reconnect at once rather than a second later |
