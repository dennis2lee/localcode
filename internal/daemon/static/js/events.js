import {
  permissionTextEl, permissionAllowAlwaysBtn, permissionAllowSessionBtn,
  permissionOutsideEl, permissionAllowDirBtn, permissionAllowOutsideBtn,
} from './dom.js';
import { app, session } from './state.js';
import {
  appendUser, appendTool, appendError, appendModelText, endModelText,
  appendToolCall, finishToolCall, resolvePendingUser, abandonRunningToolCalls,
  appendReview, appendThinking, endThinking, clearTranscript, showEarlierBanner,
} from './transcript.js';
import { renderStatusBar, renderTasks, setCurrentAgent, renderAutoDelegate, renderMCPServers, renderPermissionStatus } from './render.js';
import { setWaiting, setConnected, setInputLocked, renderCommDot, recordHistoryEntry } from './composer.js';
import {
  refreshDelegatePanelIfOpen, refreshPermissionSettingsIfOpen, permissionRequest,
  applySessionPermissions,
} from './modals.js';
import { applyScheduleEvent } from './schedules.js';
import { refreshSmartAgentIfOpen, refreshOrchestrateIfOpen, refreshKeepGoingIfOpen } from './settings.js';
import { refreshTaskViewStatus } from './taskview.js';
// events.js and sessions.js import each other (session.renamed reloads the
// session list; selectSession opens the event stream). Both references are
// only ever called from inside a function body, never read at module-
// evaluation time, so the cycle is safe — see MDN's notes on circular ES
// module imports.
import { loadSessions, renderSessionList, loadArchived, selectSession } from './sessions.js';

let eventSource = null;

// Each handler receives ev.data ?? {}, so a malformed event (missing data)
// degrades to "nothing to read" instead of throwing out of the whole
// dispatch — before this table, `ev.data.name`/`ev.data.id` were dereferenced
// unguarded and a malformed frame from the daemon could abort the handler.
const handlers = {
  // A daemon that handed its address to a newer version of itself, and
  // is about to end this stream. The reconnect that follows lands on the
  // new one on its own; what it cannot do is swap the JavaScript already
  // running here, so the page says what a reload would get.
  'daemon.replaced': (d) => {
    appendTool(`localcode ${d.version || ''} took over this address. This page keeps working against it; reload for its interface.`);
  },
  'message.user': (d) => {
    if (typeof d.text !== 'string') return;
    // A message localcode sent on the user's behalf — keep_going telling a
    // stalled model to carry on. It is in the log so the model's history
    // survives a restart, and it is announced by its own note; painting it
    // as a typed line would put words in the user's mouth, and it has no
    // business in Up/Down recall.
    if (d.auto) return;
    // A message this client sent already has a placeholder standing in for
    // it; this is that message finally reaching the model, so the
    // placeholder goes rather than sitting above a duplicate. Unconditional
    // because every prompt gets one now, not just the mid-turn ones — and
    // it is a no-op for text no placeholder was made for (another client's
    // message, or a replayed one).
    resolvePendingUser(d.text);
    // Every prompt this session has seen goes into Up/Down recall, whoever
    // typed it and whenever. On the replay that opens a session this is
    // what rebuilds the list, so recall survives a reload and a switch
    // through another conversation.
    recordHistoryEntry(d.text);
    appendUser(d.text);
  },
  // Reasoning, live. Never replayed, so a reload does not bring it back
  // and is not meant to: the answer is what the transcript keeps.
  'thinking.delta': (d) => {
    if (typeof d.text === 'string') appendThinking(d.text);
  },
  'thinking.end': () => endThinking(),
  'message.part.delta': (d) => {
    if (typeof d.text === 'string') appendModelText(d.text);
  },
  // One model message ended, NOT the turn — a turn with tool calls streams
  // several of these. Ending the wait here is what used to make a prompt
  // typed during tool execution skip the queue and bounce off the daemon's
  // busy flag with a 409.
  'message.part.end': (d) => {
    endModelText(typeof d.text === 'string' ? d.text : '');
  },
  // The daemon's real turn boundary, emitted after its busy flag is
  // cleared — safe to stop waiting and let the queue drain.
  'turn.done': () => {
    session.runningTool = '';
    setWaiting(false);
  },
  // Tool activity gets a transcript line of its own, not just the status
  // bar: the status bar only says what is running now and clears when it
  // stops, so a long turn spent in tools left no trace of itself either
  // during or after. See appendToolCall.
  'tool.start': (d) => {
    session.runningTool = d.name || '';
    appendToolCall(d.tool_use_id, d.name || '', d.input || '');
  },
  'tool.end': (d) => {
    session.runningTool = '';
    finishToolCall(d.tool_use_id, d.content, d.is_error);
  },
  'permission.request': (d) => {
    session.pendingPermissionID = d.id;
    session.pendingPermissionCanAlways = !!d.can_always;
    // The light says what the modal says. Without this the dot went on
    // blinking green — "working" — behind a dialog that had stopped the
    // work to ask a question.
    renderCommDot();
    permissionTextEl.textContent = `[${d.tool}] ${d.description || '(no description given)'}`;
    // A boundary question is a different question, so it gets different
    // buttons: a place is answered at one of two sizes, and "always
    // allow" would write a tool rule that outlives the reason for it.
    const outside = d.outside === 'read' || d.outside === 'write' ? d.outside : '';
    permissionOutsideEl.hidden = !outside;
    if (outside) {
      permissionOutsideEl.textContent =
        `This path is outside the project this conversation is working in (${d.workspace || 'unknown'}).`;
      permissionAllowDirBtn.hidden = false;
      permissionAllowDirBtn.textContent = `Allow ${outside} under ${d.outside_dir || 'this directory'}`;
      permissionAllowDirBtn.title = 'for the rest of this session; /'
        + outside + '-outside mem-clear forgets it';
      permissionAllowOutsideBtn.hidden = false;
      permissionAllowOutsideBtn.textContent = `Allow ${outside} anywhere outside`;
      permissionAllowOutsideBtn.title = `turns ${outside}-outside on for this conversation`;
    } else {
      permissionAllowDirBtn.hidden = true;
      permissionAllowOutsideBtn.hidden = true;
    }
    const offerAlways = session.pendingPermissionCanAlways && !outside;
    permissionAllowAlwaysBtn.style.display = offerAlways ? '' : 'none';
    permissionAllowSessionBtn.style.display = outside ? 'none' : '';
    if (offerAlways) {
      permissionAllowAlwaysBtn.title = `don't ask again — writes "${d.rule}" to config.json`;
    }
    permissionRequest.open();
    setInputLocked(true, 'Resolve the permission request above to continue.');
  },
  // The four switches for this conversation moved: at its own prompt, in
  // another window, or by somebody answering "allow anywhere" above.
  'permissions.changed': (d) => {
    applySessionPermissions(d);
  },
  // Work booked for later. The panel's rows are built from these, which
  // is what makes them survive a reload.
  'schedule.created': (d) => applyScheduleEvent('schedule.created', d),
  'schedule.status': (d) => applyScheduleEvent('schedule.status', d),
  'schedule.seen': (d) => applyScheduleEvent('schedule.seen', d),
  'schedule.renamed': (d) => applyScheduleEvent('schedule.renamed', d),
  'schedule.removed': (d) => applyScheduleEvent('schedule.removed', d),
  // A debate: this session's agent writes, another one reviews, round
  // after round. The banner goes up before the first turn so the page
  // says what is about to happen rather than explaining it afterwards.
  'debate.started': (d) => {
    const model = d.model ? ` (${d.model})` : '';
    appendTool(`[debate: ${d.author} writes, ${d.reviewer}${model} reviews, up to ${d.rounds} rounds]`);
  },
  'debate.review': (d) => appendReview(d),
  // The note is composed by the daemon and travels on the event, so both
  // clients say the same thing and neither has to reconstruct why the
  // debate ended from a reason code.
  'debate.ended': (d) => {
    if (d.note) appendTool(`[${d.note}]`);
  },
  'permission.resolved': () => {
    permissionRequest.close();
    setInputLocked(false);
  },
  // Sidebar + status bar carry task activity; no transcript line.
  'task.spawned': (d) => {
    session.tasks.set(d.task_id, { agent: d.agent, status: 'spawned' });
    renderTasks();
  },
  'task.status': (d) => {
    // "deleted" is the daemon saying this task's conversation is gone,
    // recorded on this session's own log so the row stays gone across a
    // reload rather than being rebuilt from the task.spawned above it.
    if (d.status === 'deleted') {
      session.tasks.delete(d.task_id);
      renderTasks();
      return;
    }
    if (session.tasks.has(d.task_id)) session.tasks.get(d.task_id).status = d.status;
    else session.tasks.set(d.task_id, { agent: '', status: d.status });
    renderTasks();
    refreshTaskViewStatus(d.task_id, d.status);
  },
  // What a task is doing right now, mirrored into the parent as it
  // happens. "running" for twenty minutes says nothing about whether
  // anything is happening; the name of the tool it is in says a lot.
  'task.progress': (d) => {
    const t = session.tasks.get(d.task_id);
    if (!t) return;
    t.doing = d.doing || '';
    renderTasks();
  },
  // Just update the state the status bar already renders every time — no
  // transcript line here. A line on every single switch would leave a
  // permanent "switched to X" entry for something already visible in the
  // header dropdown and the status line.
  'agent.switched': (d) => {
    setCurrentAgent(d.agent);
    // The last usage report came from the agent that just stopped being
    // current, so its model is now stale — the status bar names what will
    // answer the *next* message. Only the model is dropped: the token
    // counts and context percentage belong to the conversation, which the
    // switch doesn't reset. The next turn's usage event refills it.
    if (session.lastUsage) session.lastUsage = { ...session.lastUsage, model: '' };
  },
  // Merged, not replaced: the live tokens-per-second estimate broadcast
  // during a generation carries only the rate, and overwriting would blank
  // the context percentage and model name every second while a model is
  // talking.
  usage: (d) => {
    session.lastUsage = { ...(session.lastUsage || {}), ...d };
  },
  cleared: () => {
    appendTool('[system] cleared: the model starts fresh from here. Everything above is still in this conversation.');
  },
  rewound: (d) => {
    const files = [];
    if (d.restored) files.push(`${d.restored} file(s) restored`);
    if (d.created) files.push(`${d.created} removed`);
    if (d.skipped) files.push(`${d.skipped} left alone`);
    const what = d.turn_text ? `: ${d.turn_text}` : '';
    appendTool(`[system] rewound one turn${what}${files.length ? ' — ' + files.join(', ') : ''}.`);
  },
  compacted: (d) => {
    appendTool(`[system] conversation compacted to save context (summary: ${d.summary_length || 0} chars).`);
  },
  'config.changed': (d) => {
    if (typeof d.auto_compact_enabled === 'boolean') app.autoCompactEnabled = d.auto_compact_enabled;
    if (typeof d.show_tps === 'boolean') app.showTPS = d.show_tps;
    if (typeof d.auto_delegate === 'boolean') {
      app.autoDelegate = d.auto_delegate;
      renderAutoDelegate();
      refreshDelegatePanelIfOpen();
    }
    // "/config smart_agent on" from another client, or from the TUI. The
    // panel is only redrawn if it happens to be open — there is no status
    // bar pill for this one, deliberately: it is a way of working that is
    // chosen once, not a thing to flip between messages.
    if (typeof d.orchestrate === 'boolean') {
      app.orchestrate = d.orchestrate;
    }
    if (typeof d.smart_agent === 'boolean') {
      app.smartAgent = d.smart_agent;
      refreshSmartAgentIfOpen();
      refreshOrchestrateIfOpen();
    }
  },
  // Daemon-wide, and the live half of every switch: a toggle typed at
  // any prompt, the settings window in this tab or another one. It
  // carries all of them, so this applies a snapshot rather than merging
  // a sequence and cannot leave the page half-updated by a missed event.
  //
  // The session-scoped config.changed above still arrives for the four
  // settings it has always carried, and applying the same state twice is
  // harmless. This one is the only half that reaches a window looking at
  // another session, which is where the old state used to sit.
  'settings.changed': (d) => {
    if (typeof d.auto_compact_enabled === 'boolean') app.autoCompactEnabled = d.auto_compact_enabled;
    if (typeof d.show_tps === 'boolean') app.showTPS = d.show_tps;
    if (typeof d.auto_delegate === 'boolean') {
      app.autoDelegate = d.auto_delegate;
      renderAutoDelegate();
      refreshDelegatePanelIfOpen();
    }
    if (typeof d.orchestrate === 'boolean') {
      app.orchestrate = d.orchestrate;
    }
    if (typeof d.smart_agent === 'boolean') {
      app.smartAgent = d.smart_agent;
      refreshSmartAgentIfOpen();
      refreshOrchestrateIfOpen();
    }
    if (typeof d.keep_going === 'boolean') {
      app.keepGoing = d.keep_going;
      refreshKeepGoingIfOpen();
    }
    if (typeof d.auto_compact_percent === 'number') app.autoCompactPercent = d.auto_compact_percent;
    if (typeof d.skip_permissions === 'boolean') {
      // The daemon default, which is what a conversation that has not
      // answered for itself follows. The pill and the checkboxes read
      // the open conversation's own answer instead and are moved by
      // permissions.changed, which is the event that carries it.
      app.skipPermissions = d.skip_permissions;
    }
    renderStatusBar();
  },
  // A fork is a verbatim copy of a conversation, so its transcript is
  // indistinguishable from the original's. This line is the only thing
  // that says which one you are looking at.
  'session.forked': (d) => {
    appendTool(`[this is a fork of "${d.from_title || d.from || 'another session'}" — the original is untouched]`);
  },
  // Opened on its own, a run session is a conversation that starts with an
  // instruction nobody in it typed, at a moment nobody was there for. The
  // same job session.forked does, for the same reason.
  'session.scheduled': (d) => {
    const who = d.name || `scheduled task ${d.schedule || ''}`.trim();
    let line = `created by ${who}`;
    if (d.run > 0) {
      // "run 3" and "run 3 of 5" are different amounts of reassurance
      // when you are looking at a series.
      if (d.run_total > 0) line += `, run ${d.run} of ${d.run_total}`;
      else if (d.repeat) line += `, run ${d.run} (${d.repeat})`;
    }
    const at = d.at ? new Date(d.at) : null;
    if (at && !isNaN(at)) line += `, ${at.toLocaleString()}`;
    appendTool(`[${line}]`);
  },
    'session.archived': async (d) => {
      // The list this client is showing has changed, whoever changed it.
      await loadSessions();
      // Unconditionally: the header's count is about the archive, and
      // fetching it only while the section is open makes the number a
      // claim about a list the page did not load.
      await loadArchived();
      if (d && d.session === session.sessionID && d.archived) {
        // Move first, then say so. selectSession clears the transcript on
        // its way in, so a notice appended before the switch is wiped by
        // it and the page changes conversation with no explanation.
        if (app.sessions.length > 0) {
          const next = app.sessions[0];
          selectSession(next.id, next.agent, next.workspace);
        }
        appendError('That conversation was archived elsewhere. Retrieve it to work in it again.');
      }
    },
  'session.renamed': () => {
    loadSessions();
  },
  // Daemon-wide, not part of this conversation: it arrives on the same
  // stream but carries the whole server list every time, so the handler
  // replaces rather than merges.
  // Daemon-wide, like mcp.status: which session is working right now.
  // The list's own `busy` fields are the load-time answer; these keep it
  // current without polling.
  'session.activity': (d) => {
    const s = (app.sessions || []).find(x => x.id === d.session);
    if (!s || s.busy === !!d.busy) return;
    const finished = s.busy && !d.busy;
    s.busy = !!d.busy;
    // A turn that ended somewhere you were not looking leaves an answer
    // behind, and the light says so until you go and read it. A turn that
    // ended in the session on screen does not: you watched it arrive, so
    // the light goes straight back to idle rather than asking you to
    // acknowledge something you have already seen.
    if (finished && d.session !== session.sessionID) {
      app.unreadSessions.add(d.session);
    }
    renderSessionList();
    // The light under the prompt reads this same flag for the session on
    // screen (see turnInFlight), so it is redrawn from the event that
    // changed it rather than waiting for something else to happen.
    if (d.session === session.sessionID) renderCommDot();
  },
  'mcp.status': (d) => {
    app.mcpServers = d.servers || [];
    renderMCPServers();
  },
  delegated: (d) => {
    appendTool(`[delegated to ${d.agent || ''}]`);
  },
  'turn.cancelled': () => {
    session.promptQueue = [];
    session.runningTool = '';
    setWaiting(false);
    abandonRunningToolCalls('stopped');
    appendTool('[cancelled]');
  },
  error: (d) => {
    // "recovered" means the loop already handled it and the turn is still
    // going — most often the context-window overflow, which is summarized
    // and retried rather than surfaced. Clearing the spinner and painting
    // it red would say the turn is over when the reply is still coming.
    if (d.recovered) {
      appendTool(`[${d.error || ''}]`);
      return;
    }
    session.runningTool = '';
    setWaiting(false);
    appendError(d.error || '');
  },
};

export function applyEvent(ev) {
  const h = handlers[ev.type];
  if (!h) return;
  h(ev.data ?? {});
  // One place, instead of a hand-picked call site per case. The light
  // reads the task list as well as the turn now, so every event that can
  // move a task's status has to be able to move it.
  renderStatusBar();
  renderCommDot();
}

// How much of a long conversation to load when opening it. Enough that
// the visible end of a transcript is there with room to scroll back a
// little, and small enough that the cost of a session switch does not
// depend on how long the session is. Measured before choosing: 7,680
// events left the daemon in 47ms but cost the client 751ms to render in
// a headless DOM, and more in a real one.
const TRANSCRIPT_TAIL = 400;

// wantWholeTranscript is set by the banner below and cleared whenever a
// session is opened, so a conversation still opens at its end and stays
// whole only for as long as somebody is reading back through it.
let wantWholeTranscript = false;

// sawFirstSeq guards the banner to the first persisted event of a
// connection: that event's sequence is what says whether anything was cut
// off, and every later one would answer the same question again.
let sawFirstSeq = false;

// noticeTruncation says so when the transcript on screen does not start at
// the start of the conversation.
//
// The first persisted event of a connection carries the answer: sequences
// begin at 1, so anything higher means the daemon cut to a tail and the
// events before it were never sent. They are still in the log — nothing
// deletes them but deleting the session — and the banner is the request
// that goes and gets them.
//
// Transient events (Store.Broadcast) carry no sequence and say nothing
// about position, so they are skipped rather than counted as the first.
function noticeTruncation(ev) {
  if (sawFirstSeq || wantWholeTranscript) return;
  const seq = Number(ev && ev.seq);
  if (!Number.isFinite(seq) || seq <= 0) return;
  sawFirstSeq = true;
  if (seq > 1) showEarlierBanner(openWholeTranscript);
}

export function openWholeTranscript() {
  wantWholeTranscript = true;
  clearTranscript();
  connectEvents();
}

// resetTranscriptWindow puts the next connection back to opening at the
// end. Called when the session changes, because "show me all of it" was
// said about a particular conversation.
export function resetTranscriptWindow() {
  wantWholeTranscript = false;
  sawFirstSeq = false;
}

export function connectEvents() {
  if (eventSource) eventSource.close();
  setConnected(false);
  sawFirstSeq = false;
  // ?tail= so opening a long conversation shows its end straight away
  // rather than rebuilding the whole thing first. The daemon moves the
  // cut back to a turn boundary, and a reconnect ignores it in favour of
  // Last-Event-ID, so nothing is skipped after the first load.
  //
  // Omitting it asks from the beginning of the log. That is what the
  // banner does, and it is the only way the browser has ever had to see
  // past the cut: the record is complete on disk and this is the request
  // that fetches all of it.
  const url = wantWholeTranscript
    ? `/api/sessions/${session.sessionID}/events`
    : `/api/sessions/${session.sessionID}/events?tail=${TRANSCRIPT_TAIL}`;
  eventSource = new EventSource(url);
  eventSource.onopen = () => setConnected(true);
  eventSource.onmessage = (e) => {
    // An event arriving is itself proof the stream is up, which matters
    // because onopen doesn't fire again after an auto-reconnect in every
    // browser.
    setConnected(true);
    try {
      const ev = JSON.parse(e.data);
      noticeTruncation(ev);
      applyEvent(ev);
    } catch (err) { console.error('bad event', err); }
  };
  eventSource.onerror = () => {
    // EventSource auto-reconnects using Last-Event-ID, so there's nothing to
    // do but show the light as down until it comes back.
    setConnected(false);
  };
}
