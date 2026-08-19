import { sessionListEl, sessionIdEl } from './dom.js';
import { app, session, resetSession, forgetHistory } from './state.js';
import * as apiClient from './api.js';
import { appendError, clearTranscript } from './transcript.js';
import { formatTime, shortenPath } from './format.js';
import { renderTasks, renderStatusBar, setCurrentAgent, renderWorkspace } from './render.js';
import { setWaiting, setInputLocked, renderCommDot } from './composer.js';
import { connectEvents } from './events.js';
import { loadWorkspace } from './loaders.js';
import { permissionRequest } from './modals.js';

export async function loadSessions() {
  try {
    app.sessions = await apiClient.getSessions();
  } catch (err) {
    app.sessions = [];
  }
  renderSessionList();
  // The listing carries each session's busy flag, which is also what the
  // light under the prompt reports for the one on screen — so a refresh
  // that changes it has to redraw that light too. This is the path that
  // corrects the dot after a reload into a session that is already
  // working: no activity event is coming, because nothing changed.
  renderCommDot();
  // The header names the current session too, and its name lives in the
  // listing that was just refetched — so it is re-rendered from the same
  // data, in the same place, rather than left to whoever caused the
  // change to remember to update it.
  renderSessionHeader();
}

// renderSessionHeader labels the current session in the header. It shows
// the title, not the id: the id is a timestamp nobody reads, and after
// naming a session the name is what you look for to confirm you are in
// it. The id stays in the tooltip, where a bug report can still find it.
export function renderSessionHeader() {
  if (!session.sessionID) return;
  const current = (app.sessions || []).find(x => x.id === session.sessionID);
  sessionIdEl.textContent = (current && current.title) || session.sessionID;
  sessionIdEl.title = session.sessionID;
}

export function renderSessionList() {
  sessionListEl.innerHTML = '';
  if (!app.sessions || app.sessions.length === 0) {
    sessionListEl.innerHTML = '<div style="color:var(--muted)">no sessions</div>';
    return;
  }
  for (const s of app.sessions) {
    const div = document.createElement('div');
    div.className = 'session-item' + (s.id === session.sessionID ? ' active' : '');
    // The whole card switches to the session — the old dedicated "switch"
    // button made the single most common action the smallest target on
    // the row. The rename/delete buttons below stop propagation so they
    // don't switch as a side effect of being clicked.
    div.title = `${s.id}\nclick to switch to this session, drag to move it up or down`;
    div.addEventListener('click', () => {
      if (s.id !== session.sessionID) selectSession(s.id, s.agent, s.workspace);
    });
    makeDraggable(div, s.id);

    const title = document.createElement('div');
    title.className = 'title';
    // A dot on every working session, not just the one on screen. The
    // status line under the prompt only ever spoke for the current
    // conversation, so a turn left running in another one was invisible
    // — including a turn stuck waiting on a permission request, which
    // blocks workspace switching for every session until it is answered.
    // Three states, one dot, and every session has one:
    //   running        green, blinking  — the model is working
    //   answer unread  green, steady    — it finished while you were elsewhere
    //   idle           grey, steady     — nothing is happening here
    //
    // The idle dot is drawn rather than omitted. A row with no light and a
    // row whose light has not been noticed look the same, so an absent dot
    // could mean "idle" or "this panel does not draw lights" — and the two
    // green states only mean something against a light that is reliably
    // there when nothing is going on.
    const unread = app.unreadSessions.has(s.id);
    const led = document.createElement('span');
    led.className = 'session-led ' + (s.busy ? 'running' : unread ? 'unread' : 'idle');
    led.title = s.busy
      ? 'a turn is running in this session'
      : unread
        ? 'this session has a reply you have not looked at'
        : 'nothing is running in this session';
    title.appendChild(led);
    title.appendChild(document.createTextNode(s.title ? s.title : s.id));
    div.appendChild(title);

    // Which project a conversation belongs to is the thing that
    // distinguishes otherwise identical sessions, so it's shown here
    // instead of the agent name (which the header dropdown and the
    // status line under the prompt both already carry).
    const workspace = document.createElement('div');
    workspace.className = 'workspace';
    workspace.textContent = s.workspace ? shortenPath(s.workspace) : '(workspace not recorded)';
    workspace.title = s.workspace || 'this session predates workspace tracking';
    div.appendChild(workspace);

    const meta = document.createElement('div');
    meta.className = 'meta';
    meta.textContent = formatTime(s.created_at);
    div.appendChild(meta);

    const actions = document.createElement('div');
    actions.className = 'actions';

    const forkBtn = document.createElement('button');
    forkBtn.textContent = 'fork';
    forkBtn.title = 'start a new session carrying a copy of this conversation';
    forkBtn.addEventListener('click', (e) => { e.stopPropagation(); forkSession(s); });
    actions.appendChild(forkBtn);

    const renameBtn = document.createElement('button');
    renameBtn.textContent = 'rename';
    renameBtn.addEventListener('click', (e) => { e.stopPropagation(); renameSessionPrompt(s); });
    actions.appendChild(renameBtn);

    const delBtn = document.createElement('button');
    delBtn.textContent = 'delete';
    delBtn.className = 'danger-btn';
    delBtn.addEventListener('click', (e) => { e.stopPropagation(); deleteSessionConfirm(s); });
    actions.appendChild(delBtn);

    div.appendChild(actions);
    sessionListEl.appendChild(div);
  }
}

// Dragging a session card up or down the panel.
//
// The panel is ordered newest-first, which is the right default and the
// wrong permanent arrangement: the conversation someone is living in for a
// week sinks below every throwaway one started since. So the order is
// theirs to set, and the daemon remembers it — an arrangement that had to
// be redone after every restart would not be worth making.
//
// draggingID is module state rather than something carried on the event,
// because the dataTransfer payload is not readable during dragover in every
// browser, and dragover is where a row has to decide whether it is a
// possible drop target at all.
let draggingID = null;

function makeDraggable(div, id) {
  div.draggable = true;
  div.setAttribute('draggable', 'true');

  div.addEventListener('dragstart', (e) => {
    draggingID = id;
    div.classList.add('dragging');
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      // Some browsers start no drag at all without data on the transfer.
      try { e.dataTransfer.setData('text/plain', id); } catch { /* not fatal */ }
    }
  });
  div.addEventListener('dragend', () => {
    draggingID = null;
    div.classList.remove('dragging');
    clearDropMarkers();
  });
  div.addEventListener('dragover', (e) => {
    if (!draggingID || draggingID === id) return;
    // preventDefault on dragover is what says "a drop is allowed here";
    // without it the browser refuses the drop and the card springs back.
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    div.classList.add('drop-target');
  });
  div.addEventListener('dragleave', () => div.classList.remove('drop-target'));
  div.addEventListener('drop', (e) => {
    e.preventDefault();
    e.stopPropagation();
    const from = draggingID;
    draggingID = null;
    clearDropMarkers();
    if (from && from !== id) dropSessionOn(from, id);
  });
}

function clearDropMarkers() {
  for (const el of sessionListEl.childNodes || []) {
    if (el.classList) {
      el.classList.remove('drop-target');
      el.classList.remove('dragging');
    }
  }
}

// reorderList moves fromID to where toID currently sits. Pure, and
// exported, because this is the part with an answer that can be wrong:
// dropping a card on the one below it and on the one above it are
// different moves, and both have to come out as the list looks after the
// mouse is released.
export function reorderList(sessions, fromID, toID) {
  const out = sessions.slice();
  const from = out.findIndex(s => s.id === fromID);
  const to = out.findIndex(s => s.id === toID);
  if (from < 0 || to < 0 || from === to) return sessions;
  const [moved] = out.splice(from, 1);
  out.splice(to, 0, moved);
  return out;
}

// dropSessionOn applies the move on screen first and tells the daemon
// after. The drop already happened as far as the person doing it is
// concerned; waiting a round trip to redraw would show the card snap back
// to where it was and then move again.
export async function dropSessionOn(fromID, toID) {
  const before = app.sessions;
  app.sessions = reorderList(before, fromID, toID);
  renderSessionList();
  try {
    await apiClient.reorderSessions(app.sessions.map(s => s.id));
  } catch (err) {
    appendError(`could not save the session order: ${err}`);
    // Back to what the daemon actually has, rather than leaving an order
    // on screen that will not survive the next reload.
    app.sessions = before;
    renderSessionList();
  }
}

// forkSession copies a conversation into a new session and switches to
// it. Switching is the point: forking to keep looking at the original
// would leave you to find the copy in the list yourself, and the reason
// to fork is to take this thread somewhere else *now*. The original is
// untouched and one click away in the panel.
export async function forkSession(s) {
  try {
    const forked = await apiClient.forkSession(s.id);
    await loadSessions();
    selectSession(forked.id, forked.agent, forked.workspace);
  } catch (err) {
    appendError(`failed to fork session: ${err}`);
  }
}

export async function renameSessionPrompt(s) {
  const newTitle = window.prompt('New session name:', s.title || '');
  if (newTitle === null) return;
  try {
    await apiClient.renameSession(s.id, newTitle);
    await loadSessions();
  } catch (err) {
    appendError(`failed to rename: ${err}`);
  }
}

export async function deleteSessionConfirm(s) {
  if (!window.confirm(`Delete session "${s.title || s.id}"? This cannot be undone.`)) return;
  try {
    await apiClient.deleteSession(s.id);
    // The conversation is gone; its recall list has nothing left to be
    // about.
    forgetHistory(s.id);
  } catch (err) {
    appendError(`failed to delete session: ${err}`);
    return;
  }
  if (s.id === session.sessionID) {
    await loadSessions();
    if (app.sessions.length > 0) {
      selectSession(app.sessions[0].id, app.sessions[0].agent, app.sessions[0].workspace);
    } else {
      await createNewSession();
    }
  } else {
    await loadSessions();
  }
}

// selectSession switches the UI to a session and, if that session was
// started somewhere else, moves the daemon's workspace to match — so
// opening a conversation about another project actually puts you back in
// that project rather than leaving its old transcript pointed at the
// current directory. workspace is that session's recorded directory;
// sessions from before the field existed have none, and those leave the
// workspace alone rather than guessing.
export function selectSession(id, agent, workspace) {
  // Opening it is reading it.
  app.unreadSessions.delete(id);
  resetSession(id);
  renderSessionHeader();
  clearTranscript();
  renderTasks();
  setWaiting(false);
  // Through the Modal object, not by reaching past it to the class list.
  // The class is an output of that object and never an input, so hiding
  // the element directly left isOpen stuck true for the life of the page
  // — and two keyboard handlers read it. Escape silently stopped
  // cancelling turns and Tab stopped cycling agents, permanently, with
  // nothing on screen to explain it.
  //
  // The request itself is not lost by closing it here: it stays
  // unanswered in that session's log, so coming back to the session
  // replays it and the modal reappears. Until then that session's turn is
  // blocked on it, which is why the session list marks it (see
  // renderSessionList) and why the workspace error names it.
  permissionRequest.close();
  setInputLocked(false);
  setCurrentAgent(agent);
  renderStatusBar(); // the new session's agent/model, before any event arrives
  renderSessionList();
  connectEvents();
  // The header names the directory this session works in. Painted from
  // what the listing already says, so it changes with the click rather
  // than a round trip later, and then confirmed against the daemon —
  // which is the authority, and which also answers for a session that has
  // no recorded workspace of its own.
  //
  // Confirmed rather than *set*: this used to POST the session's own path
  // back to the daemon, which is a no-op that can fail. The daemon refuses
  // a workspace change while that session has a turn running, so opening a
  // conversation that was working left the header on the previous
  // session's project, with an error in the transcript about a move nobody
  // had asked for.
  if (workspace) {
    app.workspacePath = workspace;
    renderWorkspace();
  }
  loadWorkspace();
}

export async function createNewSession() {
  try {
    const sess = await apiClient.createSession('general-purpose');
    await loadSessions();
    selectSession(sess.id, sess.agent, sess.workspace);
  } catch (err) {
    sessionIdEl.textContent = 'error';
    appendError(`failed to create session: ${err}`);
  }
}

export async function deleteAllSessions() {
  if (!window.confirm('Delete ALL sessions? This cannot be undone.')) return;
  try {
    await apiClient.deleteAllSessions();
  } catch (err) {
    appendError(`failed to delete all sessions: ${err}`);
    return;
  }
  await createNewSession();
}
