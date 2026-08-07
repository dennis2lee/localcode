import { sessionListEl, sessionIdEl, modalEl } from './dom.js';
import { app, session, resetSession } from './state.js';
import * as apiClient from './api.js';
import { appendError, clearTranscript } from './transcript.js';
import { formatTime, shortenPath } from './format.js';
import { renderTasks, renderStatusBar, setCurrentAgent } from './render.js';
import { setWaiting, setInputLocked } from './composer.js';
import { connectEvents } from './events.js';
import { applyWorkspace } from './modals.js';

export async function loadSessions() {
  try {
    app.sessions = await apiClient.getSessions();
  } catch (err) {
    app.sessions = [];
  }
  renderSessionList();
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
    title.textContent = s.title ? s.title : s.id;
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
  sessionIdEl.textContent = session.sessionID;
  clearTranscript();
  renderTasks();
  setWaiting(false);
  modalEl.classList.remove('open');
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
