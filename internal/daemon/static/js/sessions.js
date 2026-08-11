import { sessionListEl, sessionIdEl } from './dom.js';
import { app, session, resetSession } from './state.js';
import * as apiClient from './api.js';
import { appendError, clearTranscript } from './transcript.js';
import { formatTime, shortenPath } from './format.js';
import { renderTasks, renderStatusBar, setCurrentAgent } from './render.js';
import { setWaiting, setInputLocked } from './composer.js';
import { connectEvents } from './events.js';
import { applyWorkspace, permissionRequest } from './modals.js';

export async function loadSessions() {
  try {
    app.sessions = await apiClient.getSessions();
  } catch (err) {
    app.sessions = [];
  }
  renderSessionList();
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
    div.title = `${s.id}\nclick to switch to this session`;
    div.addEventListener('click', () => {
      if (s.id !== session.sessionID) selectSession(s.id, s.agent, s.workspace);
    });

    const title = document.createElement('div');
    title.className = 'title';
    // A dot on every working session, not just the one on screen. The
    // status line under the prompt only ever spoke for the current
    // conversation, so a turn left running in another one was invisible
    // — including a turn stuck waiting on a permission request, which
    // blocks workspace switching for every session until it is answered.
    if (s.busy) {
      const led = document.createElement('span');
      led.className = 'session-led';
      led.title = 'a turn is running in this session';
      title.appendChild(led);
    }
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
  // Deliberately not awaited: the session is already switched and its
  // transcript already replaying. The workspace change lands a moment
  // later and announces itself in the transcript, and if it's refused
  // (another session mid-turn) that's reported without having blocked
  // the switch the user actually asked for.
  if (workspace) applyWorkspace(workspace);
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
