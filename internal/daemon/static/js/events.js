import { inputEl } from './dom.js';
import { app, session } from './state.js';
import {
  appendUser, appendTool, appendError, appendModelText, endModelText,
  appendToolCall, finishToolCall, resolvePendingUser, abandonRunningToolCalls,
  appendReview, appendThinking, endThinking, foldThinking, clearTranscript, showEarlierBanner,
  abandonPendingUsers, settleThinkingBlock, hasLiveThinking,
} from './transcript.js';
import { findRefresh } from './find.js';
import { renderStatusBar, renderTasks, setCurrentAgent, renderAutoDelegate, renderMCPServers, renderPermissionStatus, renderWorkspace } from './render.js';
import { setWaiting, setConnected, setInputLocked, renderCommDot, recordHistoryEntry } from './composer.js';
import {
  refreshDelegatePanelIfOpen, refreshPermissionSettingsIfOpen,
  enqueuePermissionRequest, settlePermissionRequest,
  applySessionPermissions,
  applyEffort,
} from './modals.js';
import { applyScheduleEvent } from './schedules.js';
import { refreshSmartAgentIfOpen, refreshOrchestrateIfOpen, refreshKeepGoingIfOpen, refreshRepeatLimitIfOpen, refreshFoldThinkingIfOpen } from './settings.js';
import { refreshTaskViewStatus } from './taskview.js';
// events.js and sessions.js import each other (session.renamed reloads the
// session list; selectSession opens the event stream). Both references are
// only ever called from inside a function body, never read at module-
// evaluation time, so the cycle is safe — see MDN's notes on circular ES
// module imports.
import { loadSessions, renderSessionList, loadArchived, selectSession } from './sessions.js';
import { loadAgents } from './loaders.js';

let eventSource = null;

// isOrchestrateStage reports whether a task id is an orchestration stage
// progress report rather than a task. The daemon reports stage progress
// on the task.status channel with task_id "orchestrate:<stage>", and
// that id names a report, not a session: there is no conversation
// behind it to open, inspect, or cancel.
function isOrchestrateStage(taskID) {
  return typeof taskID === 'string' && taskID.startsWith('orchestrate:');
}

// stageLine renders one orchestration stage report as the transcript
// line it is: a running stage names how many agents it launched, a
// finished one how many answers it kept. Anything else falls back to the
// status word itself, so a future stage state is still legible rather
// than silently dropped.
function stageLine(d) {
  const stage = d.stage || String(d.task_id || '').replace(/^orchestrate:/, '');
  if (d.status === 'running') {
    const n = Number(d.agents) || 0;
    return `[orchestrate: ${stage} running (${n} agent${n === 1 ? '' : 's'})]`;
  }
  if (d.status === 'completed') {
    return `[orchestrate: ${stage} finished (${Number(d.kept) || 0} kept)]`;
  }
  if (!d.status) return `[orchestrate: ${stage}]`;
  return `[orchestrate: ${stage} ${d.status}]`;
}

// Each handler receives ev.data ?? {}, so a malformed event (missing data)
// degrades to "nothing to read" instead of throwing out of the whole
// dispatch — before this table, `ev.data.name`/`ev.data.id` were dereferenced
// unguarded and a malformed frame from the daemon could abort the handler.
// The context percentage is a reading of the history the daemon holds,
// and every one of these events replaces that history: the daemon drops
// its own count with it and says nothing until the next turn reports. A
// gauge that went on showing the old fill was wrong at the one moment
// somebody was looking at it. The model name and the rate stay: they are
// about the session, not about the conversation that was replaced.
function forgetContextFill() {
  if (session.lastUsage) session.lastUsage = { ...session.lastUsage, percent: null };
}

const handlers = {
  // A daemon that handed its address to a newer version of itself, and
  // is about to end this stream. The reconnect that follows lands on the
  // new one on its own; what it cannot do is swap the JavaScript already
  // running here, so the page says what a reload would get.
  'daemon.replaced': (d) => {
    appendTool(`localcode ${d.version || ''} took over this address; loading its interface.`);
    // Noted, not acted on yet.
    //
    // The window reloads the page itself when the update went through its
    // own handoff, and that was the only path that did: a window that
    // updated at startup serves a fixed proxy and never captured the
    // reload, and every later update is performed by the successor in
    // another process, which cannot reach up to ask. So the page has to
    // do it — but not now. This event is sent at the *start* of the
    // retirement, before the drain and before anything has been pointed
    // at the new daemon, so reloading here would fetch the old interface
    // again and nothing would say so a second time.
    //
    // The right moment is when the stream comes back, because that is
    // when there is a new daemon behind it. See resyncAfterReconnect.
    session.daemonReplaced = true;
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
    // A new turn's prompt: any reasoning block still open belongs to a
    // turn that ended without saying so, and the next reasoning must not
    // be written into it above this prompt.
    foldThinking(0);
    // Every prompt this session has seen goes into Up/Down recall, whoever
    // typed it and whenever. On the replay that opens a session this is
    // what rebuilds the list, so recall survives a reload and a switch
    // through another conversation.
    recordHistoryEntry(d.text);
    appendUser(d.text, d.images);
  },
  // Reasoning, live. The deltas are never replayed; a muse model's block
  // comes back on a reload as the thinking.block the daemon logged.
  // "fold" marks a muse model's stream, which is drawn as a labelled
  // block that folds when the answer starts; see appendThinking.
  'thinking.delta': (d) => {
    if (typeof d.text === 'string') appendThinking(d.text, d.fold === true);
  },
  'thinking.end': (d) => endThinking(typeof d.elapsed_ms === 'number' ? d.elapsed_ms : 0),
  // A muse model's reasoning block as the log keeps it: live, it folds
  // the block the deltas drew, with the whole text; on a reload or
  // reconnect it is the only sign of the block, and draws it folded.
  'thinking.block': (d) => settleThinkingBlock(d.text, typeof d.elapsed_ms === 'number' ? d.elapsed_ms : 0),
  'message.part.delta': (d) => {
    if (typeof d.text !== 'string') return;
    // The answer has started, so the reasoning before it is over
    // whether or not its end arrived first.
    foldThinking(0);
    appendModelText(d.text);
  },
  // One model message ended, NOT the turn — a turn with tool calls streams
  // several of these. Ending the wait here is what used to make a prompt
  // typed during tool execution skip the queue and bounce off the daemon's
  // busy flag with a 409.
  'message.part.end': (d) => {
    // The message is over, so its reasoning is too. After a reconnect
    // this can be the only sign of the answer: the daemon replays a
    // finished reply as its end alone, and the reasoning's broadcast end
    // is not replayed.
    foldThinking(0);
    // Command output the person ran is not something the model said, so
    // it draws as a tool line rather than a model message. The header
    // names the command; the user message above it already shows the
    // line as typed.
    if (typeof d.shell_command === 'string' && d.shell_command) {
      appendTool('$ ' + d.shell_command + '\n' + (typeof d.text === 'string' ? d.text : ''));
      return;
    }
    endModelText(typeof d.text === 'string' ? d.text : '');
  },
  // How hard the model is asked to think, changed here or in another
  // client watching the same conversation. Carried in the event rather
  // than fetched, so a client that opens the session later replays it.
  'effort.changed': (d) => {
    applyEffort({
      model: d.model, agent: d.agent, level: d.level,
      source: d.source, levels: d.levels || [], note: d.note,
    });
  },
  // The daemon's real turn boundary, emitted after its busy flag is
  // cleared — safe to stop waiting and let the queue drain.
  'turn.done': () => {
    session.runningTool = '';
    setWaiting(false);
    // A reasoning block still open is one whose end never came.
    foldThinking(0);
    // A row still running at the turn's end is a call that never ran:
    // the stream died after the model asked for it, and no tool.end is
    // coming. It sat spinning under the error line for the life of the
    // page, as a cancelled one did before turn.cancelled closed it.
    abandonRunningToolCalls('not run');
    // The one refresh the transcript cannot ask for itself.
    //
    // Every line it *draws* asks — see appendDiv — but a reply is not
    // drawn, it is written into an element that is rewritten on every
    // fragment, and asking per fragment would re-search the whole
    // conversation per token. So a finished reply is announced here, at
    // the turn's end, which is the first moment it is worth searching
    // again. A cancelled or failed turn needs no announcement of its own:
    // both of them draw a line, and drawing it is what asks.
    findRefresh();
  },
  // Tool activity gets a transcript line of its own, not just the status
  // bar: the status bar only says what is running now and clears when it
  // stops, so a long turn spent in tools left no trace of itself either
  // during or after. See appendToolCall.
  'tool.start': (d) => {
    session.runningTool = d.name || '';
    foldThinking(0);
    appendToolCall(d.tool_use_id, d.name || '', d.input || '');
  },
  'tool.end': (d) => {
    session.runningTool = '';
    finishToolCall(d.tool_use_id, d.content, d.is_error);
  },
  // The screen always holds the oldest unresolved request: a newer
  // arrival queues behind it, and a resolution for any other id leaves
  // the modal alone. Answering the second of two requests used to close
  // the first without answering it, and a replayed answer from days ago
  // closed a live modal — see the TUI's events.go, which states the rule.
  'permission.request': (d) => {
    enqueuePermissionRequest(d);
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
  // A question the model asked mid-turn. Rendered in the transcript
  // rather than as a modal: the options are a shortcut, not the whole
  // answer space, and the box below is already where a person types the
  // one the model did not think of.
  'input.request': (d) => {
    session.pendingAsk = d.id;
    const opts = Array.isArray(d.options) ? d.options : [];
    appendTool([`[the model is asking] ${d.question}`,
      ...opts.map((o, i) => `  ${i + 1}. ${o}`),
      '  (reply in the box below, in your own words or with a number)'].join('\n'));
  },
  'input.resolved': (d) => {
    if (session.pendingAsk === d.id) session.pendingAsk = null;
    if (d.answer) appendTool(`[answered] ${d.answer}`);
  },
  // The model's own checklist. Whole list every time, for the same
  // reason the TUI prints it whole: the person is watching to see
  // whether the work is the right work, not to diff step statuses.
  'plan.updated': (d) => {
    const steps = Array.isArray(d.plan) ? d.plan : [];
    const mark = (s) => (s === 'completed' ? 'x' : s === 'in_progress' ? '>' : ' ');
    const head = d.explanation ? `[plan] ${d.explanation}` : '[plan]';
    appendTool([head, ...steps.map((s) => `  [${mark(s.status)}] ${s.step}`)].join('\n'));
  },
  // The panel arrives as reviewers/models; the singulars stay as the
  // fallback, because a log written before the plural fields existed
  // replays through this same renderer and must still name its reviewer
  // and model.
  'debate.started': (d) => {
    const reviewers = Array.isArray(d.reviewers) && d.reviewers.length
      ? d.reviewers
      : (d.reviewer ? [d.reviewer] : []);
    const models = (d.models && typeof d.models === 'object' && !Array.isArray(d.models)) ? d.models : {};
    const named = Object.keys(models).length === 0 && d.model
      ? reviewers.map((r) => `${r} (${d.model})`)
      : reviewers.map((r) => (models[r] ? `${r} (${models[r]})` : r));
    appendTool(`[debate: ${d.author} writes, ${named.join(', ')} reviews, up to ${d.rounds} rounds]`);
  },
  'debate.review': (d) => appendReview(d),
  // The note is composed by the daemon and travels on the event, so both
  // clients say the same thing and neither has to reconstruct why the
  // debate ended from a reason code.
  'debate.ended': (d) => {
    if (d.note) appendTool(`[${d.note}]`);
    // A collapse replaces the history, as a compaction does. Only a
    // debate that says it had nothing to collapse keeps the reading; one
    // from an older daemon that does not say is let go of, since a blank
    // gauge refills on the next turn and a stale one misleads.
    if (d.collapsed !== false) forgetContextFill();
  },
  // The question is gone, however it went: answered from these buttons,
  // answered in another window, given up on unattended, or cancelled
  // along with the turn that asked it. Matched on id: a resolution for
  // anything but the request on screen drops just that queued request,
  // and a stale one matches nothing at all.
  'permission.resolved': (d) => {
    settlePermissionRequest(d.id);
  },
  // Sidebar + status bar carry task activity; no transcript line.
  'task.spawned': (d) => {
    // An orchestration stage is a progress report, not a task: the
    // daemon sends task_id "orchestrate:<stage>" for stage progress,
    // and there is no session behind that id to ever open.
    if (isOrchestrateStage(d.task_id)) return;
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
    // Stage progress is a transcript line, not a task row. Filing it as
    // a task drew a clickable row out of a zero-value task — empty
    // agent — and the click opened a task view for a session id that
    // does not exist.
    if (isOrchestrateStage(d.task_id)) {
      appendTool(stageLine(d));
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
    // The conversation's own choice is kept per agent on the server, so
    // the cached one belonged to the agent just left. Dropped with the
    // usage model above: the daemon announces the new agent's view right
    // after this event, which refills it when that agent has its own.
    session.chosenModel = '';
    session.chosenModelAgent = '';
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
    forgetContextFill();
  },
  rewound: (d) => {
    const files = [];
    if (d.restored) files.push(`${d.restored} file(s) restored`);
    if (d.created) files.push(`${d.created} removed`);
    if (d.skipped) files.push(`${d.skipped} left alone`);
    const what = d.turn_text ? `: ${d.turn_text}` : '';
    appendTool(`[system] rewound one turn${what}${files.length ? ' — ' + files.join(', ') : ''}.`);
    // The undone prompt goes back in the box so the turn can be retyped
    // from where it went wrong. Only into an empty box: whatever is
    // already typed is a newer intention than the one being undone.
    if (d.prompt && !inputEl.value.trim()) {
      inputEl.value = d.prompt;
      // So the box grows to fit it, the way it does while typing.
      inputEl.dispatchEvent(new Event('input', { bubbles: true }));
    }
    forgetContextFill();
  },
  // Which model answers here, chosen apart from which agent does — here
  // or in another client. Carried in the event rather than fetched, so a
  // client that opens the session later replays it.
  'model.changed': (d) => {
    // Only a choice this conversation made. Back on the agent's own, the
    // status bar goes back to reading it from the agent, which is where
    // it will stay correct when the agent is switched.
    session.chosenModel = d.source === 'conversation' ? (d.model || '') : '';
    // The agent the choice belongs to, so the status bar can tell a
    // cached choice from the one in force after a switch.
    session.chosenModelAgent = d.source === 'conversation' ? (d.agent || '') : '';
    renderStatusBar();
  },
  // A conversation moved to another directory, here or in the terminal.
  // The button names the workspace, and a button naming the old one is
  // how a file lands in the wrong project.
  'workspace.changed': (d) => {
    if (typeof d.path === 'string' && d.path) {
      app.workspacePath = d.path;
      renderWorkspace();
    }
  },
  redone: (d) => {
    const files = [];
    if (d.written) files.push(`${d.written} file(s) written again`);
    if (d.skipped) files.push(`${d.skipped} left alone`);
    const what = d.turn_text ? `: ${d.turn_text}` : '';
    appendTool(`[system] put the turn back${what}${files.length ? ' — ' + files.join(', ') : ''}.`);
    forgetContextFill();
  },
  compacted: (d) => {
    appendTool(`[system] conversation compacted to save context (summary: ${d.summary_length || 0} chars).`);
    forgetContextFill();
  },
  'config.changed': (d) => {
    // Turning Smart Agent on or off changes which agents exist, so a
    // flip here means the dropdown is stale. Compared before applying,
    // because the snapshot carries every switch on every change.
    const rosterMayHaveChanged = typeof d.smart_agent === 'boolean' && d.smart_agent !== app.smartAgent;
    if (typeof d.auto_compact_enabled === 'boolean') app.autoCompactEnabled = d.auto_compact_enabled;
    if (typeof d.show_tps === 'boolean') app.showTPS = d.show_tps;
    if (typeof d.show_thinking === 'boolean') app.showThinking = d.show_thinking;
    if (typeof d.show_timestamps === 'boolean') app.showTimestamps = d.show_timestamps;
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
    if (rosterMayHaveChanged) loadAgents();
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
    // Same roster rule as config.changed above: the snapshot always
    // carries smart_agent, so only an actual flip refetches.
    const rosterMayHaveChanged = typeof d.smart_agent === 'boolean' && d.smart_agent !== app.smartAgent;
    if (typeof d.auto_compact_enabled === 'boolean') app.autoCompactEnabled = d.auto_compact_enabled;
    if (typeof d.show_tps === 'boolean') app.showTPS = d.show_tps;
    if (typeof d.show_thinking === 'boolean') app.showThinking = d.show_thinking;
    if (typeof d.show_timestamps === 'boolean') app.showTimestamps = d.show_timestamps;
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
    if (typeof d.fold_thinking === 'boolean') app.foldThinking = d.fold_thinking;
    // Redrawn on either, since the note says when show_thinking is
    // hiding the reasoning this switch would label.
    if (typeof d.fold_thinking === 'boolean' || typeof d.show_thinking === 'boolean') refreshFoldThinkingIfOpen();
    if (typeof d.repeat_limit === 'number') {
      app.repeatLimit = d.repeat_limit;
      refreshRepeatLimitIfOpen();
    }
    if (typeof d.auto_compact_percent === 'number') app.autoCompactPercent = d.auto_compact_percent;
    if (typeof d.skip_permissions === 'boolean') {
      // The daemon default, which is what a conversation that has not
      // answered for itself follows. The pill and the checkboxes read
      // the open conversation's own answer instead and are moved by
      // permissions.changed, which is the event that carries it.
      app.skipPermissions = d.skip_permissions;
    }
    if (rosterMayHaveChanged) loadAgents();
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
  // A conversation is gone, deleted from wherever. Daemon-wide, like
  // session.archived: without it a delete in another window, in the TUI,
  // or by this page's own "delete all" left the row in every other
  // client's sidebar and its id in their header until something else
  // happened to reload the list.
  'session.deleted': (d) => {
    const gone = d.session;
    if (!gone) return;
    loadSessions().then(() => {
      if (gone !== session.sessionID) return;
      const next = (app.sessions || [])[0];
      if (next) selectSession(next.id);
    });
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
    // A cancelled turn is not waiting on an answer. The daemon does send
    // permission.resolved for the question it was holding, and that is
    // the event that clears this; saying it here too costs nothing and
    // means the light does not depend on two events arriving in order.
    // Settled rather than just cleared, so a second request queued behind
    // the cancelled one comes up instead of being stranded with the
    // composer unlocked and no modal to answer it in. The resolved event
    // for the cancelled question then matches nothing and is ignored.
    settlePermissionRequest(session.pendingPermissionID);
    setWaiting(false);
    if (!session.pendingPermissionID) setInputLocked(false);
    abandonRunningToolCalls('stopped');
    foldThinking(0);
    // The queue went with the turn: the daemon drops it in
    // turnTracker.cancel, so anything still showing as sent was never
    // handed to anybody.
    abandonPendingUsers();
    appendTool('[cancelled]');
    renderCommDot();
  },
  error: (d) => {
    // "recovered" means the loop already handled it and the turn is still
    // going — most often the context-window overflow, which is summarized
    // and retried rather than surfaced. Clearing the spinner and painting
    // it red would say the turn is over when the reply is still coming.
    if (d.recovered) {
      appendTool(`[${d.error || ''}]`);
      // A trim that dropped the oldest messages replaced the history the
      // gauge was a reading of.
      if (d.history_replaced === true) forgetContextFill();
      return;
    }
    session.runningTool = '';
    setWaiting(false);
    foldThinking(0);
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

// resyncAfterReconnect asks the daemon whether the turn this client
// thinks is running still is.
//
// session.waiting is this page's own memory, set when it sent a prompt and
// cleared by the turn.end that answers it. If the daemon serving that turn
// goes away — killed, crashed, or replaced by an update that did not
// finish — that event never comes, and the memory has no expiry. The
// stream drops, the light goes grey, and when the stream comes back the
// page is still saying "working…" about a turn that no longer exists, with
// a stop button that answers 502. That is exactly what a window reported
// after its daemon vanished mid-turn.
//
// A stream that has been down is a gap in what this client knows, so the
// reconnect is the moment to stop trusting the memory and ask. The daemon
// is the authority: it says whether the session is busy. Three cases and
// only one of them clears anything — the turn is still running (a blip,
// and the memory was right), the turn ended and the replay is about to say
// so, or the turn is gone and nobody will ever say so.
//
// Nothing is cleared unless the answer actually arrived: a listing that
// does not contain this session is a failed fetch or a deleted
// conversation, and neither is evidence that a turn finished.
async function resyncAfterReconnect() {
  // The stream is back, and the daemon behind it said it was handing
  // over: whatever is answering now is the new version, so this is the
  // moment to go and get its interface. See the daemon.replaced handler.
  if (session.daemonReplaced) {
    session.daemonReplaced = false;
    try {
      location.reload();
    } catch { /* nothing else to try */ }
    return;
  }
  // The roster this page shows is the roster the daemon had when the
  // stream dropped: an update handoff or a restart under an open window
  // can change which agents exist and which models their labels name, so
  // the dropdown is refetched on every reconnect rather than trusted.
  // Unconditional on the turn state below: staleness does not depend on
  // whether a turn was running when the stream went away.
  await loadAgents();
  // A turn to check: one this page is waiting on, or a reasoning block
  // still streaming in one it is only watching.
  const inProgress = () => !!session.sessionID && (session.waiting || hasLiveThinking());
  if (!inProgress()) return;
  // Each await gives the stream a chance to deliver the backlog the
  // reconnect brought, and a turn.done in it ends the turn by itself.
  // Nothing is declared unless the page is still where it was: the same
  // conversation, no prompt sent since, and the turn still in progress.
  const id = session.sessionID;
  const epoch = session.turnEpoch;
  const still = () => session.sessionID === id && session.turnEpoch === epoch && inProgress();
  await loadSessions();
  if (!still()) return;
  const mine = (app.sessions || []).find(s => s.id === id);
  if (!mine || mine.busy) return;
  // The daemon clears a session's busy flag just before it writes
  // turn.done, so an idle answer can overtake the end of a turn that did
  // finish. One more wait before declaring anything.
  await new Promise((resolve) => setTimeout(resolve, lostTurnGraceMs));
  if (!still()) return;
  if (!session.waiting) {
    // A turn this page was only watching: its block folds, and nothing
    // is said, since it was not this page's turn.
    foldThinking(0);
    return;
  }
  // What a cancelled turn gets, since nothing will ever end this one:
  // the question it held is put away, its running tool rows stop, and
  // the prompts sent into it are marked as never handed to anybody.
  settlePermissionRequest(session.pendingPermissionID);
  setWaiting(false);
  if (!session.pendingPermissionID) setInputLocked(false);
  foldThinking(0);
  abandonRunningToolCalls('did not finish');
  abandonPendingUsers();
  appendTool('[the localcode running this turn is no longer running it; the turn did not finish]');
  renderCommDot();
}

// lostTurnGraceMs is the wait resyncAfterReconnect takes before it
// declares a turn lost; see there.
const lostTurnGraceMs = 1000;

// EventSource.CLOSED, spelled out: the constant is on the constructor,
// which a test double does not have to provide.
const CLOSED = 2;

// Reopening a stream the browser has given up on.
//
// Backoff because the reason it closed is usually still true a moment
// later — a 404 for a session that is gone stays a 404 — and a tight loop
// there is a request per frame. Capped so a daemon that comes back after
// an hour is still picked up without a reload.
const reconnectFirstDelay = 1000;
const reconnectMaxDelay = 15000;
let reconnectDelay = reconnectFirstDelay;
let reconnectTimer = null;

function cancelReconnect() {
  if (reconnectTimer !== null) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
}

function scheduleReconnect() {
  if (reconnectTimer !== null) return;
  const wait = reconnectDelay;
  reconnectDelay = Math.min(reconnectDelay * 2, reconnectMaxDelay);
  const id = session.sessionID;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    // Not if the page has moved on to another conversation in the
    // meantime: that switch opened a stream of its own. And resuming
    // from where this page got to, not from the tail: the transcript
    // already holds the tail, and asking for it again drew all of it a
    // second time.
    if (session.sessionID && session.sessionID === id) connectEvents({ resume: true });
  }, wait);
}

// lastSeenSeq is the newest logged event this page has drawn from the
// stream it has open, for a stream rebuilt after the browser gave up on
// it to resume from. A browser that reconnects on its own sends the
// same thing as Last-Event-ID; a new EventSource cannot, so the rebuilt
// one asks with ?since=.
let lastSeenSeq = 0;

export function connectEvents(opts = {}) {
  const resume = opts.resume === true && lastSeenSeq > 0;
  if (eventSource) eventSource.close();
  cancelReconnect();
  setConnected(false);
  if (!resume) {
    lastSeenSeq = 0;
    sawFirstSeq = false;
  }
  // ?tail= so opening a long conversation shows its end straight away
  // rather than rebuilding the whole thing first. The daemon moves the
  // cut back to a turn boundary, and a reconnect ignores it in favour of
  // Last-Event-ID, so nothing is skipped after the first load.
  //
  // Omitting it asks from the beginning of the log. That is what the
  // banner does, and it is the only way the browser has ever had to see
  // past the cut: the record is complete on disk and this is the request
  // that fetches all of it.
  //
  // A resumed stream asks with ?since=. When the browser later reconnects
  // that stream on its own it resends the same URL with a Last-Event-ID,
  // and the daemon prefers the Last-Event-ID, which is where the stream
  // got to rather than where it started.
  const url = resume
    ? `/api/sessions/${session.sessionID}/events?since=${lastSeenSeq}`
    : wantWholeTranscript
      ? `/api/sessions/${session.sessionID}/events`
      : `/api/sessions/${session.sessionID}/events?tail=${TRANSCRIPT_TAIL}`;
  eventSource = new EventSource(url);
  eventSource.onopen = () => {
    // Up again: the next failure starts its own backoff from the bottom.
    reconnectDelay = reconnectFirstDelay;
    if (setConnected(true)) resyncAfterReconnect();
  };
  eventSource.onmessage = (e) => {
    // An event arriving is itself proof the stream is up, which matters
    // because onopen doesn't fire again after an auto-reconnect in every
    // browser.
    reconnectDelay = reconnectFirstDelay;
    if (setConnected(true)) resyncAfterReconnect();
    try {
      const ev = JSON.parse(e.data);
      if (typeof ev.seq === 'number' && ev.seq > lastSeenSeq) lastSeenSeq = ev.seq;
      noticeTruncation(ev);
      applyEvent(ev);
    } catch (err) { console.error('bad event', err); }
  };
  eventSource.onerror = () => {
    setConnected(false);
    // Two different failures arrive here and only one of them retries.
    //
    // A transport-level drop — the daemon restarting, a cable — leaves
    // the stream CONNECTING, and the browser reopens it on its own with
    // Last-Event-ID, which is what the comment here used to say and is
    // still true. But a reply whose status is not 200, or whose
    // Content-Type is not text/event-stream, *fails the connection* per
    // the spec: readyState goes to CLOSED and the browser never tries
    // again. The daemon answers 404 from the SSE handler for a session it
    // does not know, and a window whose successor has gone answers 502 to
    // everything — so one such reply left the page permanently deaf while
    // looking alive: prompts posted, turns ran, and nothing was ever
    // painted.
    //
    // The page knows how to build the stream; it just never asked again.
    if (eventSource && eventSource.readyState === CLOSED) scheduleReconnect();
  };
}
