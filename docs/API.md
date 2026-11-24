# HTTP API reference

Source of truth: the 67 `d.mux.HandleFunc` registrations in
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
  `internal/daemon/daemon.go`). A message's body is capped higher, to
  carry 10 MB of base64 images, and its text keeps the 1 MB cap. Uploads
  are multipart, at most 32 MB per file: a larger file is refused with
  `413`, not truncated.
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
* A write whose path is not canonical (a `.` or `..` segment, or a
  doubled slash) is answered `400` rather than redirected
  (`CanonicalWrites` in `internal/daemon/canonical.go`, in front of the
  daemon and of the desktop window's proxy). A client follows
  a redirect with the method it started with, so
  `DELETE /api/sessions/{id}/tasks/..` reached `DELETE /api/sessions/{id}`.
  Reads are still redirected to the cleaned path.
* Writes to `/api/sessions/{id}/...` pass through the ownership gate
  (`ownershipGate` in `internal/daemon/handoff.go`): while the daemon this
  one replaced is still finishing a session, writes to it get `409` and
  reads pass. `GET` and `HEAD` never block on it.

## Which parts are load-bearing for the two clients

The two clients are the TUI (an HTTP client even when it shares a process
with the daemon, via `internal/client`) and the Web UI (via
`internal/daemon/static/js/api.js`, where every request URL template lives;
the event stream URLs are built in `events.js` and `taskview.js`).

* Core turn loop, used by both: sessions list/create, `POST
  /api/sessions/{id}/messages`, `GET /api/sessions/{id}/events`,
  permission and input resolution, `POST /api/sessions/{id}/cancel`.
  Break one and both clients stop working.
* Used by one client: the model picker endpoints (TUI; the page learns
  the model from `model.changed` events), the update check and install
  (page; the TUI updates through `/update`), task spawn/list/output
  (TUI commands and the model's own tools; the page reads task state
  from `task.spawned`/`task.status` events, cancels through `POST
  /api/tasks/{taskId}/cancel`, and removes rows through `DELETE
  /api/sessions/{id}/tasks[/{taskId}]`), `GET /api/usage` (page: the usage
  window; the TUI prints `/usage all` instead).
* Used by `localcode run`: `GET /api/sessions/{id}/usage`, for the usage
  a run through a daemon reports.
* Incidental: `GET /api/trace` has no in-tree caller (the turn log is
  read with `jq` from `~/.localcode/trace/`); `GET /api/sessions/{id}`
  has no in-tree caller (both clients use the list plus the event
  stream); `POST /api/daemon/shutdown` is update machinery, called by the
  replacing process, not by either client.

## The desktop window and its proxy

The window serves the daemon built in its own process. Every route below
behaves as documented there.

After an update hands that daemon to a newer binary, at startup or through
`/update`, the window serves a reverse proxy onto the new daemon
(`cmd/localcode/gui_proxy.go`). Every route below stays reachable. Five
routes behave differently behind the proxy, marked `(window: local)` or
`(window: rewritten)` in the tables:

* `POST /api/workspace/browse` and `POST /api/workspace/reveal` are
  answered by the window process itself with native dialogs. The daemon
  behind the proxy would answer `404` (no picker on a network daemon).
* `GET /api/update` and `POST /api/update/install` are answered by the
  window's own older daemon when one is kept, because the successor is
  current by construction and would always answer "no update". One is
  kept after a startup update. After `/update` the successor answers.
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
| `POST /api/update/install` (window: local) | nothing | `200 {"version", "source", "source_unverified", "verified", "started", "replaced", "restarting", "path", "detail"}`. `source_unverified: true` means the source is plain http. `verified: false` is reported, not hidden | `403` installing is not allowed from here; download/install failures carry their own status |
| `GET /api/trace` | `?limit=` (1-500, default 100), `?session=`, `?trace=` | `200 {"enabled", "records"}`. `enabled` is false with Smart Agent off, which is a setting, not a failure | none |
| `GET /api/usage` | `?window=` `all` (default), `today`, `week` or `month` | `200 {"scope", "models", "sessions", "unread", "note"?}`: what `/usage all` and its windows print, as data. `scope` is the window as the heading names it (`every conversation`, `today`, `the last 7 days`, `the last 30 days`). `models` maps each model to `{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cache_read_or_write_tokens", "calls"}`, and is `{}` when nothing was recorded. `sessions` counts the sessions that recorded a call, `unread` the logs that could not be read. `note` is present when a cache figure is: the text `/usage` prints under its report to say what the cache figures are. Every session counts, archived ones and sub-agents' included, and a fork's copy of another log is not counted twice. The Web UI's usage window draws this | `400` unknown window |

### Settings (daemon-wide)

`GET /api/settings` answers the whole snapshot, so a client applies
state instead of merging events: `auto_compact_enabled`,
`auto_compact_percent`, `keep_going`, `fold_thinking`, `muse_profiles`,
`repeat_limit`, `smart_agent`, `browser`, `browser_available`,
`browser_import`, `ssh`, `ssh_available`, `orchestrate`, `model_invocable`,
`model_commands`, `orchestrate_roster`, `show_tps`, `show_thinking`,
`show_timestamps`, `auto_delegate`, `auto_delegate_agent`,
`auto_delegate_match`, `skip_permissions`, `permission_rules`,
`can_edit_permissions`. `muse_profiles` lists the profiles whose model ID
contains `muse`, sorted, and is `[]` when none does. `browser_available`
is whether a Chrome or Edge was found to run. `ssh_available` is whether
the daemon has an SSH manager. `browser_import` is
`{"from", "profile", "skipped"}` for the last copy of the person's
profile, or `null` before the first. Changes are announced
on the broadcast as `settings.changed`; a client that was not connected
re-reads this endpoint on load.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/settings` | nothing | `200` the snapshot above | none |
| `POST /api/settings/auto-delegate` | `{"enabled"?, "agent"?, "match"?}`; pointers, so one field can move alone | `204` | `400` unknown agent or fewer than two agents configured; `400` bad body (text error); `500` applied for this run but not persisted (text error) |
| `POST /api/settings/smart-agent` | `{"enabled"}` | `200 {"smart_agent", "applied": true, "persisted", "error"?}`; `applied` is always true, `persisted` is the part that can fail | `400` bad body (text error) |
| `POST /api/settings/browser` | `{"enabled"}`; off also quits the browser | `200 {"browser", "applied": true, "persisted", "error"?}` | `400` bad body (text error) |
| `POST /api/settings/ssh` | `{"enabled"}`; off also closes every SSH connection | `200 {"ssh", "applied": true, "persisted", "error"?}` | `400` bad body (text error); `403` refused by the SSH request guard (see SSH below) |
| `GET /api/ssh` | nothing | `200 [{"session", "title", "id", "target", "state"}]`: every SSH connection open now, in any conversation | `403` refused by the SSH request guard (see SSH below) |
| `POST /api/browser/import` | nothing | `200 {"from", "profile", "skipped"}`: copies the person's Chrome or Edge profile in again and restarts the browser, closing every tab | `404` no browser in this daemon; `409` the browser setting is off; `400` no profile to copy |
| `POST /api/settings/orchestrate` | `{"enabled"}` | `200 {"orchestrate", "applied": true, "persisted", "error"?}` | `400` bad body (text error) |
| `POST /api/settings/model-invocable` | `{"enabled"}` | `200 {"model_invocable", "applied": true, "persisted", "error"?}` | `400` bad body (text error) |
| `POST /api/settings/keep-going` | `{"enabled"}` | `204` | `400` bad body (text error); `500` applied but not persisted (text error) |
| `POST /api/settings/fold-thinking` | `{"enabled"}` | `200 {"fold_thinking", "applied": true, "persisted", "error"?}` | `400` bad body (text error) |
| `POST /api/settings/repeat-limit` | `{"limit"}`; 0 turns the guard off | `204` | `400` outside 0..max (text error); `500` applied but not persisted (text error) |

### The desktop window's own settings

What the page keeps for itself (faces, text sizes, zoom, panel widths,
which panels a conversation shows, folded groups) lives in the browser's
`localStorage`. The desktop window's page also writes each change here,
and reads the set back before it draws, because the window's
`localStorage` is tied to a port the window does not always get back.
The file is `~/.localcode/window-state.json`. A browser tab calls neither
route.

Three names hold several items each: `localcode.panelWidths`,
`localcode.panelViews` and `localcode.collapsedGroups`. The page writes
each whole. It calls `GET` again before it changes one item, and starts
from the value the file has then, so the items a second open window wrote
are sent back unchanged. The read and the write are two requests, and
each window has its own daemon: two windows that change the same name
within those two requests keep the value written last.

Both routes pass the SSH request guard (see SSH below) and answer `403`
when it refuses.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/window/state` | nothing | `200 {"available", "kept"}`. `kept` is an object of name to string, or `null` when nothing has been written yet (no file, or a file that is not an object of settings). `available` is false for a daemon started without a path | `403` refused by the request guard; `500` the file is there and could not be read |
| `PUT /api/window/state` | an object of name to string or `null`: a string keeps the name, `null` forgets it, names not in the body are left alone. Names are `archiveOpen` or `localcode.` followed by 1 to 64 letters, digits, `.`, `_` or `-`, starting with a letter or digit. At most 64 names, 256 KiB a value, 1 MiB in all | `204`, with the file on disk | `400` a body that is not one object (`null`, or anything after the object), a name outside the rule, a value that is not a string or `null`, or more than 64 names; `403` refused by the request guard; `404` a daemon started without a path; `413` a body, a value or the resulting file too large; `500` the file could not be written, or is there and could not be read (nothing is written then) |

### Daemon-wide permission defaults

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `POST /api/permissions/skip` | `{"enabled"}` | `204` | `400` bad body (text error); `500` applied but not persisted (text error) |
| `POST /api/permissions/rules` | `{"tool", "match", "decision"}`; all three required | `204` | `400` a field missing, or a `decision` that is not a known one (text error); `500` applied but not persisted (text error) |
| `POST /api/permissions/rules/remove` | `{"tool", "match", "decision"}`; the exact rule to remove | `204` | `400` a field missing, or a `decision` that is not a known one (text error); `500` applied but not persisted (text error) |

### Workspace

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/workspace` (window: rewritten) | `?session=`; pass the session, the workspace is per-session and the bare answer is only the default new sessions inherit | `200 {"path", "can_browse", "can_reveal"}` | none |
| `POST /api/workspace` | `{"path", "session_id"?}`; empty `session_id` moves the daemon default for sessions with none of their own | `200 {"path"}` | `400` no path (text error), or not a usable directory; `404` unknown session; `409` a turn is running in this session, with `"busy"` naming it |
| `POST /api/workspace/browse` (window: local) | `{"start"?}`; absent means no starting directory | `200 {"path"}`; `204` the picker was dismissed, which is normal | `404` this daemon has no picker (text error); `500` the picker failed |
| `POST /api/workspace/reveal` (window: local) | nothing; `?session=` selects whose workspace. A caller-supplied path is refused by design | `200 {"path"}` | `404` this daemon cannot open a file manager (text error); `500` opening failed |

### Listings (pickers and completion)

Bodies are names and descriptions only, never prompt bodies or tool
implementations.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/mcp-servers` | nothing | `200 [{"name", "status", "detail"}]`; broken servers appear as disconnected, and no servers means `[]`. Live changes arrive as `mcp.status` on the stream | none |
| `GET /api/agents` | nothing | `200 [{"name", "description"?, "model"?, "builtin"?}]`, sorted; declared agents plus the built-in specialists while `orchestrate` is on | none |
| `GET /api/commands` | nothing | `200 [{"name", "description"?}]`, sorted; project plus global custom commands. Running one still goes through `POST /api/sessions/{id}/messages` as `/<name>` | none |
| `GET /api/skills` | nothing | `200 [{"name", "description"?}]`, sorted | none |
| `GET /api/slash-commands` | nothing | `200` the daemon's own commands for client completion | none |

### Sessions

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `POST /api/sessions` | `{"agent"?}`, default `general-purpose` | `201` the session, stamped with the live workspace | `400` bad body; `409` every session is being deleted; `500` creation failed |
| `GET /api/sessions` | `?archived=1` for the other list | `200` visible sessions in panel order: sessions never dragged come first, newest first, then the dragged ones in their saved order. Each has `"busy"` (a turn is running here, or the daemon this one took over from is still finishing a turn or a background task in it, which is also why a write to it answers `409` until then; a `session.activity` with `"busy": false` says when it lets go), `"tasks"` (how many tasks and scheduled runs it started are still running, at any depth, background or not) and `"asking"` (it waits for a person: a permission request or a question the model asked) | none |
| `GET /api/sessions/{id}` | nothing | `200` the session. No in-tree caller; both clients use the list plus the event stream | `404` unknown session |
| `DELETE /api/sessions/{id}` | nothing | `204`. Removes the session, the background work it started, and all their logs; the parent's task row is marked deleted | `404` unknown session; `409` a turn is running |
| `DELETE /api/sessions` | nothing | `204`. Refuses while any session has a turn in flight | `409` naming the busy sessions |
| `POST /api/sessions/order` | `{"ids"}` the dragged panel order, persisted | `204` | `400` bad body or unknown id |
| `GET /api/sessions/groups` | nothing | `200 {"names"}` the panel's groups, in the order they are drawn | none |
| `POST /api/sessions/groups` | `{"names"}` the whole list — **required**, since a missing field would read as an empty list — and `{"rename": {"from", "to"}}` when one of them is an old group under a new name | `200 {"names"}`. A name that is gone and not renamed is deleted; the sessions that were in it are left ungrouped | `400` bad body, no `names`, a name that is empty, padded, over 100 characters, carries a control character or is listed twice, or a rename whose `from` is not a group or whose `to` is not in the list; `500` the list could not be written |
| `POST /api/sessions/{id}/group` | `{"group"}`, or `""` for ungrouped; must already be in the group list, and is matched exactly rather than trimmed | `200` the session | `400` no such group or the session is archived; `404` unknown session; `500` the session could not be written |
| `POST /api/sessions/{id}/agent` | `{"agent"}`; must be one the listing offers | `200` the session; an `agent.switched` event follows | `400` unknown agent or bad body; `403` archived; `404` unknown session |
| `POST /api/sessions/{id}/rename` | `{"title"}`; display only, resolution stays by id | `200` the session; a `session.renamed` event follows | `400` bad body; `404` unknown session |
| `GET /api/sessions/{id}/usage` | nothing | `200` the same shape as `GET /api/usage`, for this session and every session below it. `localcode run --format json` reports its total as a run's usage | `404` unknown session |
| `POST /api/sessions/{id}/fork` | nothing | `201` the new session: a verbatim event-log copy (minus `session.renamed`), same effort levels and model choice, titled `fork of X` | `404` unknown source; `409` a turn is running in the source, or delete-all is |
| `POST /api/sessions/{id}/archive` | nothing | `200` the session. Refuses rather than stops running work | `404` unknown session; `400` store refusal; `409` a turn is running, background tasks are running (names them), scheduled runs are in progress, or delete-all is |
| `POST /api/sessions/{id}/retrieve` | nothing | `200` the session, history rebuilt from the log; shelved schedule rows come back, missed ones marked missed | `404` unknown session; `409` busy, or delete-all is |
| `POST /api/sessions/{id}/messages` | `{"text", "images"?}`. `images` is `[{"media_type", "data"}]` with base64 `data`. Text is required unless images are present. `/clear`, `/rewind`, `/redo`, `/compact`, `/workspace <path>` and a `!` command sent while a turn runs are refused with `409 {"held": ...}` rather than queued | `202 {"status": "accepted"}` (a turn starts; the reply arrives on the stream) or `202 {"status": "injected"}` (handed to the running turn). A message with images is never handed to a running turn | `400` bad body, empty text with no images, text over 1 MB, an image type other than PNG, JPEG, GIF or WEBP, or images over 10 MB in total; `403` archived; `404` unknown session; `409` with `{"held": "/clear"...}` when the text must wait for idle, plain `409` when a turn is running and the message carries images, when the begin race is lost repeatedly, when the old daemon still owns the session, or while delete-all runs |
| `POST /api/sessions/{id}/uploads` | multipart form, `file` field; name is sanitized to its base, max 32 MB | `200 {"path"}` under `~/.localcode/uploads/<id>/`; splice the path into the next message, the model reads it with file tools | `400` unparseable form, missing field, bad filename; `403` archived; `404` unknown session; `413` file over 32 MB, nothing kept and an earlier file of that name left as it was; `500` disk failure |

### Event stream

`GET /api/sessions/{id}/events` is Server-Sent Events, `Content-Type:
text/event-stream`, with a `: ping` comment every 20 seconds so silent
turns do not look dead. Resume with the `Last-Event-ID` header a
reconnecting `EventSource` sends back (wins), `?since=<seq>`, or `?tail=N`
to open near the end at a turn boundary. The header wins because a
browser reconnects to the same URL, whose `?since=` or `?tail=` says
where the stream started rather than where it got to. Invalid `since`/`tail`
is `400`, an unknown session `404`. The backlog collapses finished
replies (deltas before the last `message.part.end` are skipped; a reply
still streaming keeps its deltas). Daemon-wide events (`mcp.status`,
`session.activity`, `session.archived`, `session.deleted`,
`settings.changed`, `daemon.replaced`, and `error` for update notices) ride the same connection with no
`id:` line and never move the resume point. See [Event types](#event-types).

### Schedules (work booked for later)

The list is read once when a conversation opens; after that the client
follows the `schedule.*` events on that conversation's own stream.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/sessions/{id}/schedules` | nothing | `200 {"schedules": [...]}` | `404` unknown session |
| `POST /api/sessions/{id}/schedules` | `{"when", "prompt", "times"?, "until"?, "keep"?}`; `when` is parsed by the same parser as `/schedule`, `keep: 0` keeps no transcripts | `201 {"schedule", "human", "repeat", "workspace"}` | `400` unparseable time, empty prompt, bad repeat options (the parser's own sentence); `403` archived; `404` no scheduler or unknown session; `409` the booking conflicts |
| `POST /api/schedules/preview` | `{"when"}` | Always `200`: `{"ok": true, "at", "human", "repeat"}` or `{"ok": false, "detail"}`. A half-typed time is not an HTTP error | `400` bad body (text error) |
| `POST /api/sessions/{id}/schedules/{sid}/seen` | nothing | `204`; the third state of the row's light. An unknown booking is still `204` | `404` no scheduler |
| `POST /api/sessions/{id}/schedules/{sid}/rename` | `{"name"}`; empty clears it | `200` the entry | `400` bad body (text error); `404` no scheduler or no such booking |
| `DELETE /api/sessions/{id}/schedules/{sid}` | nothing | `204`; also removes the run session it left behind, best effort | `404` no scheduler or no such booking |

### Browser (per session)

The desktop window's browser panel. The frame stream is also how the
daemon knows a window is showing the conversation: the model is offered
the `browser` tool only while one is open. Every route but the snapshot
route answers `404` when the daemon has no browser, and every route
answers `404` when the session is unknown. The stream, navigate and
input routes answer `409` while the browser setting is off, so nothing
starts the browser and no stream counts as watching.

The daemon answers `403` to any request, on every route, whose
User-Agent contains `LocalcodeBrowser`. That is every request the
model's browser sends, so a page it opens cannot drive the API.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/sessions/{id}/browser` | nothing | `200 {"has_tab", "url"?, "title"?, "tabs"?, "width", "height"}`; `url` and `title` are the active tab's; `tabs` is every tab in the order opened, each `{"id", "url", "title", "active"}`; `width` and `height` are the page size every coordinate is in | `404` |
| `GET /api/sessions/{id}/browser/stream` | nothing | `200 multipart/x-mixed-replace`, one JPEG per part, as long as the client holds it open. Waits while the conversation has no tab, and starts no browser | `404`; `409` the setting is off |
| `POST /api/sessions/{id}/browser/navigate` | `{"url"}` (`http`, `https`, or `about:blank`), or `{"action": "back"\|"forward"\|"reload"}`; starts the browser and opens the tab when there is none | `200 {"url", "title"}` | `400` the page could not load, an address of another scheme, or an unknown action; `403` a deny rule for `browser` covers the page or one a redirect led to, and it was not loaded; `409` the setting is off; `503` the browser could not start |
| `POST /api/sessions/{id}/browser/tabs` | nothing | `200 {"tabs"}`: opens an empty tab and makes it the active one, starting the browser if it is not running | `409` the setting is off; `503` the browser could not start |
| `POST /api/sessions/{id}/browser/tabs/{tab}/activate` | nothing | `200 {"tabs"}`: the tab is the one the model's browser tool acts on and the stream shows | `404` no such tab; `409` the setting is off |
| `DELETE /api/sessions/{id}/browser/tabs/{tab}` | nothing | `200 {"tabs"}`: the tab is closed; when it was the active one, the one before it is | `404` no such tab; `409` the setting is off |
| `POST /api/sessions/{id}/browser/input` | one of `{"mouse": {"type", "x", "y", "button"?, "buttons"?, "clickCount"?, "deltaX"?, "deltaY"?, "modifiers"?}}`, `{"key": {"type", "key", "code", "text"?, "keyCode"?, "modifiers"?, "commands"?}}`, `{"text"}`, in page pixels. `buttons` is the held buttons as a mask (left 1, right 2, middle 4); a move with one held is a drag. `commands` is editing commands for the key: `selectAll`, `copy`, `cut`, `paste`, `undo`, `redo` | `204` | `400` bad event or another command; `409` the setting is off, or no tab |
| `GET /api/sessions/{id}/snapshots/{name}` | nothing | `200 image/png`: a snapshot the `browser` tool saved, from `.localcode/snapshots/` under the conversation's working directory, or under the home directory for a conversation with none. A snapshot saved before the conversation moved to another working directory is found where its `tool.end` says it was saved. `name` is the file name `tool.end` gave in `snapshot` | `404` no such conversation, a name not in the tool's own shape (`YYYYMMDD-HHMMSS-NNN.png`), no such file, or a file that is not a regular one |

### SSH (per session)

The SSH panel. Every route answers `404` when the daemon has no SSH
manager or the session is unknown, and `404` for an unknown terminal.
Connect, input and reconnect answer `409` while the ssh setting is off
and `403` for an archived conversation.

Every SSH route, `GET /api/ssh` and `POST /api/settings/ssh` pass a request guard first and
answers `403` when it refuses: a `POST` or `DELETE` whose
`Content-Type` is not `application/json`; a `Sec-Fetch-Site` other than
`same-origin` or `none`; an `Origin` whose host is not the request's
`Host`; or a `Host` that is a name other than `localhost` and the name
the daemon was started to listen on (an address is accepted). The guard
refuses what another website could send. It cannot tell the panel from a
local program: the daemon has no authentication.

A terminal's `Info` is `{"id", "epoch", "spec", "target"?, "state":
"connecting"|"connected"|"ended", "reason"?, "server"?, "shell"?,
"integrated", "running", "alt", "held", "opener": "model"|"person",
"question"?, "title"?, "cols", "rows", "input_lost"?, "scroll_first",
"scroll_next"}`. `question` is `{"id", "kind":
"host_key"|"password"|"passphrase"|"prompts", "for", "host_key"?:
{"host", "key_type", "fingerprint"}, "key"?, "name"?, "instruction"?,
"prompts"?: [{"text", "echo"}]}`.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `GET /api/sessions/{id}/ssh` | nothing | `200 {"enabled", "terminals": [Info], "wants"}`; `wants` counts the times a terminal wanted the person (a question, a command at a password prompt) | `403`; `404` |
| `GET /api/sessions/{id}/ssh/stream` | query `viewer`, `terminal`, `epoch`, `from` | `200 text/event-stream`. Every frame is an unnamed event `{"kind", "data"}`: `state` (as the route above, sent when it changes), `screen` `{"terminal", "epoch", "screen": {"cols", "rows", "lines", "cx", "cy", "cursor", "alt", "title"?}}` for the terminal named, and `scroll` `{"terminal", "epoch", "from", "lines"}`, the lines that scrolled off its top, numbered from `from`. A line is runs `{"t", "f"?, "b"?, "a"?, "w"?}`: text, colours (`"0"` to `"15"` or `#rrggbb`), flags (bold 1, faint 2, italic 4, underline 8, reverse 16, strike 32, conceal 64, blink 128), and a wide character. With `epoch` and `from` the scrollback resumes there; otherwise the last 1000 lines come first. Ending the stream forgets the viewer's size | `403`; `404` |
| `POST /api/sessions/{id}/ssh/connect` | `{"target"}`: `[user@]host[:port]` or a `Host` from `~/.ssh/config` | `200 {"terminal"}`: the terminal, connecting; questions arrive as its `question` | `400` a target that does not parse, or the limit; `403`; `409` the setting is off |
| `POST /api/sessions/{id}/ssh/{term}/input` | one of `{"key": {"key", "ctrl"?, "alt"?, "shift"?, "meta"?}}`, `{"text"}`, `{"paste"}`. `key` is a DOM key name; a key with `meta` is the window's and is dropped. A paste is bracketed when the shell asked for it | `204` | `403`; `409` the terminal is not connected or the setting is off |
| `POST /api/sessions/{id}/ssh/{term}/size` | `{"viewer", "cols", "rows"}`; `cols` 0 forgets the viewer. The terminal takes the smallest size among its viewers, 40 to 300 columns and 10 to 120 rows | `204` | `400` no viewer; `403` |
| `POST /api/sessions/{id}/ssh/{term}/hold` | `{"on"}` | `204`: while held, the model's every action on the terminal is refused, `read` and `wait` included | `403` |
| `POST /api/sessions/{id}/ssh/{term}/answer` | `{"question", "accept", "values"?, "fingerprint"?}`: `values` for a password, a passphrase or prompts, one per prompt; `fingerprint`, the one the question showed, to accept a host key | `204` | `403`; `409` the question is no longer asked, the fingerprint does not match, or the wrong number of values |
| `POST /api/sessions/{id}/ssh/{term}/reconnect` | `{}` | `200 {"terminal"}`: a new connection to the same place under the same id and a new `epoch` | `403`; `409` the terminal is still connected, or the setting is off |
| `DELETE /api/sessions/{id}/ssh/{term}` | `{}` | `204`: the connection is closed and the terminal removed | `403`; `404` |

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
parent's log are the panel rows, which is why they survive a reload. A
`task.status` of `"deleted"` removes a row. A row can outlive its
session, so the two `DELETE` routes work from the rows: a row whose
session is gone, or belongs to another conversation, gets the `"deleted"`
record and no session is touched. A child session with no row, such as a
scheduled run, is not a task here and is never deleted by them.

| Method and path | Takes | Answers | Errors |
|---|---|---|---|
| `POST /api/sessions/{id}/tasks` | `{"agent", "prompt"}`; both required | `201 {"task_id"}` | `400` bad body or a field missing; `403` archived; `500` spawn failed |
| `GET /api/sessions/{id}/tasks` | nothing | `200` the session's tasks | none (unknown session reads as no tasks) |
| `DELETE /api/sessions/{id}/tasks/{taskId}` | nothing | `204`; stops the task if it runs, deletes its session with everything under it, and appends `task.status` `"deleted"` to `{id}` | `404` unknown session, or `taskId` is not a row of its panel; `409` a turn is running in the task's own session; `500` the session could not be deleted |
| `DELETE /api/sessions/{id}/tasks` | nothing | `200 {"deleted"}`: the number of rows removed, running tasks included; `{id}` itself stays | `404` unknown session; `409` a turn is running in one of the task sessions, and nothing is deleted; `500` a session could not be deleted, and its row stays |
| `POST /api/sessions/{id}/cancel` | nothing | `200 {"cancelled"}`; false when nothing was running, which is not an error | none |
| `POST /api/sessions/{id}/detach` | nothing | `200 {"detached", "task_id"}`; lets go of the sub-agent blocking the turn so both carry on | none (`detached: false` with no task manager) |
| `POST /api/tasks/{taskId}/cancel` | nothing | `200 {"cancelled"}` | none |
| `GET /api/tasks/{taskId}/output` | nothing | `200 {"output"}`: the task's reply text so far, rebuilt from its deltas, works mid-run | `404` unknown task, or the id is a conversation rather than a task |

## Event types

Every constant in `internal/events/events.go` a client can receive on the
stream, with the payload fields the comments there promise. Per-session
log events carry `seq`; transient broadcast events (`task.progress`,
`thinking.delta`, `thinking.end`) carry none and are missed when missed.
`thinking.block` is logged, so a reconnect or a reload replays it.

| Type | Payload and scope |
|---|---|
| `message.user` | `{"text", "model_text"?, "local"?, "auto"?, "images"?}`. `local: true` was answered without a model call and is skipped rebuilding history. `auto: true` marks a message localcode sent for the user: the keep_going nudge, or a debate's reviews handed to the author. Clients do not draw it as typed. `images` is `[{"media_type", "data"}]`, the images sent with the message |
| `message.part.delta` | `{"text"}` fragment of a streaming reply |
| `message.part.end` | `{"text", "failed"?, "shell_command"?}` the finished reply text; one turn has several when tools ran. `failed: true` closes a reply whose stream died before it finished: the text is on the record and not in the history, which is rebuilt without it. `text` is empty when the stream died after a tool call and before any text; the calls a failed reply started never ran. A log written before this key existed rebuilds such a reply into the history as finished. `shell_command` names the `!` command whose output `text` is |
| `tool.start`, `tool.end` | a tool call and its result. `tool.end` carries `snapshot`, the saved file's path, when the call was a `browser` snapshot; a client shows it from the snapshot route |
| `permission.request` | a tool approval question; answered at `POST /api/sessions/{id}/permissions/{permId}` |
| `permission.resolved` | how it was answered |
| `permission.forgotten` | `{"class": "read" or "write"}`; remembered directories dropped |
| `permissions.changed` | the session switches view, per session |
| `task.spawned`, `task.status` | the panel rows; a `deleted` status removes one |
| `task.progress` | `{"task_id", "doing"}`; transient, mirrored into the parent |
| `agent.switched` | `{"agent"}` |
| `thinking.delta` | `{"text", "fold", "show_thinking"}` while reasoning streams; transient, never logged. `fold` is true when the block is drawn labelled and folded once the answer starts: `fold_thinking` is on and the model ID contains `muse`. `show_thinking` is the switch's current value; a delta is never replayed, so it is always current |
| `thinking.end` | `{"fold", "elapsed_ms"?}` when a reasoning block ends; transient. `elapsed_ms` is the time from the block's first delta to its end. For a fold block it follows `thinking.block`, which has already folded it |
| `thinking.block` | `{"text", "elapsed_ms"}`; logged. A fold block as the record keeps it: the whole reasoning and its time, written when the block ends, or when the stream stops with it open. A client folds the live block on it and takes its text as authoritative; with no live block (a replay) it draws the block folded. Only fold blocks are logged. Never rebuilt into the history the model is sent |
| `error` | `{"error", "recovered"?, "history_replaced"?, "fallback"?}` a turn failure; distinct from `turn.cancelled`, which is a person stopping it on purpose. `recovered: true` is a notice from a turn that carries on. `fallback` names the model the turn moved to, on the notice that reports the switch. `history_replaced: true` marks the trim that dropped the oldest messages to fit the window: the context usage count went with them, as after a compaction. Also sent daemon-wide, with no `seq`, for update notices: a failed handoff, an MSI install staged from a terminal, or a failed MSI install |
| `mcp.status` | `{"servers": [{"name", "status", "detail"}]}`; daemon-wide, complete list every time |
| `session.activity` | `{"session", "busy", "tasks", "asking"}`, the fields as `GET /api/sessions` has them; daemon-wide. The session's whole state, sent whenever its turn, its running tasks and scheduled runs, or a question waiting for a person changes. Also sent for a task's or a scheduled run's own session, which is not in the list; the Web UI draws a scheduled run's waiting there. A daemon before v0.166.0 sends `{"session", "busy"}` alone |
| `session.archived` | `{"session", "archived"}`; either direction, daemon-wide |
| `session.deleted` | `{"session"}`; daemon-wide, since the log it would ride in is gone |
| `session.renamed` | `{"title"}` |
| `session.forked` | `{"from", "from_title", "copied"}`; reader note, ignored rebuilding history. `copied` is how many events after it are the copy of the source's log; usage totals skip them. Absent in logs written before it existed |
| `session.scheduled` | `{"schedule", "name", "run", "run_total", "at", "repeat", "from"}`; reader note, ignored rebuilding history |
| `settings.changed` | `{"auto_compact_enabled", "show_tps", "auto_delegate", "smart_agent", "skip_permissions", "keep_going", "fold_thinking", "repeat_limit", "auto_compact_percent", "show_thinking", "show_timestamps", "orchestrate", "model_invocable"}` as a snapshot, daemon-wide. The auto-delegate target and patterns and the permission rules are not in it: re-read `GET /api/settings` for those |
| `config.changed` | `{"auto_compact_enabled", "show_tps", "auto_delegate", "smart_agent", "orchestrate"?}`; per session. Written by `/config` and by the toggle commands; `orchestrate` comes only from the toggle commands |
| `workspace.changed` | `{"path"}`; per session |
| `usage` | `{"input_tokens", "output_tokens", "cached_input_tokens", "cache_read_tokens", "cache_write_tokens", "measured", "measured_images", "max_context", "percent", "tps", "show_tps", "model", "estimated"}`; from reported usage. The daemon also broadcasts live estimates during a stream, `{"tps", "show_tps", "estimated": true}`, which are not logged; the settled event carries `estimated: false`. `percent` is `input_tokens` plus `cached_input_tokens` plus `output_tokens` over `max_context`: the whole prompt the provider read and the reply it wrote, which the next request carries. `input_tokens` alone is what was billed at the full rate. `cache_read_tokens` and `cache_write_tokens` split `cached_input_tokens` the way it is billed: served from the prompt cache, or written to it. Logs written before the split carry `cached_input_tokens` alone. `measured` is the daemon's own character estimate of the same messages, for sizing after a restart, and `measured_images` the number of images among them. Draw a gauge from `percent`, not from `input_tokens` |
| `compacted` | `{"summary_length", "manual", "summary", "model"?, "input_tokens"?, "output_tokens"?, "cache_read_tokens"?, "cache_write_tokens"?, "replaced_assets"?}`. The token figures are the summarizing call's own, present when it reported usage. `replaced_assets` names the assets the replaced messages carried, with Smart Agent on |
| `cleared` | no payload; `/clear`, a barrier rebuilding history |
| `rewound` | `{"from_seq", "turn_text", "restored", "skipped", "created"}` |
| `redone` | `{"rewind_seq", "turn_text", "written", "skipped"}` |
| `checkpoint` | `{"tool", "path", "sha256", "mode", "existed", "too_large"}`; bytes live content-addressed beside the log, not here |
| `schedule.created` | `{"id", "at", "prompt", "agent", "by"?}`. `by` is `"person"` or `"model"`: whose words the prompt holds, which decides whether a leading `/` or `!` in it runs as a command when it fires (a person's does, a model's is sent as text). Absent in logs written before it existed, and read as a person's |
| `schedule.status` | `{"id", "status", "run_session"?, "error"?}` |
| `schedule.seen` | `{"id"}` |
| `schedule.renamed` | `{"id", "name"}` |
| `schedule.removed` | `{"id"}` |
| `input.request` | `{"id", "question", "options"?}`; answered at `POST /api/sessions/{id}/input/{askId}` |
| `input.resolved` | `{"id", "answer"?}` or `{"id", "cancelled": true}` |
| `plan.updated` | `{"plan": [{"step", "status"}], "explanation"?}`; logged, unlike `task.progress` |
| `memory.session` | `{"name", "content"}` when the model saves a session note, `{"name", "forgotten": true}` when it forgets one; logged. The daemon rebuilds the conversation's notes from these on restart, rewind, redo and fork; `cleared` starts them over. The Web UI and the TUI draw the `memory_save` and `memory_forget` tool calls instead, and ignore this event. |
| `debate.started` | `{"author", "reviewer", "reviewers", "model", "models", "rounds", "task"}`. `reviewer` holds the reviewers joined and `model` the first reviewer's. Clients prefer the plurals and fall back to the singulars for logs written before them |
| `debate.review` | `{"round", "rounds", "reviewer", "model", "text", "approved", "session"}` |
| `debate.ended` | `{"reason" ("approved", "rounds", "stalled", "stopped", "failed"), "rounds", "approved", "note", "collapsed"}`. `collapsed: true` means the rounds were taken out of the history and the context usage count went with them, as after a compaction. Absent in logs written before it existed |
| `delegated` | `{"agent", "prompt"}`; a sub-agent answered on its own model |
| `effort.changed` | `{"model", "agent", "level", "source", "levels", "note"}` |
| `model.changed` | `{"agent", "profile", "model", "provider", "source", "choices"}` |
| `turn.done` | no payload; the turn boundary. Gate "waiting" state and queue drain on this, not on `message.part.end` |
| `turn.cancelled` | no payload; stopped on purpose via `POST /api/sessions/{id}/cancel` |
| `daemon.replaced` | `{"version", "pid"}`; reconnect at once rather than a second later |
