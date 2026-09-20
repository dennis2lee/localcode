import { sessionListEl, sessionIdEl, archiveToggleEl, archiveListEl, inputEl } from './dom.js';
import { app, session, resetSession, forgetHistory, stashDraft, draftFor, forgetDraft } from './state.js';
import * as apiClient from './api.js';
import { appendError, appendTool, clearTranscript } from './transcript.js';
import { formatTime, shortenPath } from './format.js';
import { renderTasks, renderStatusBar, setCurrentAgent, renderWorkspace } from './render.js';
import { setWaiting, setInputLocked, renderCommDot, autoResizeInput } from './composer.js';
import { connectEvents, resetTranscriptWindow } from './events.js';
import { loadWorkspace } from './loaders.js';
import { loadSessionPermissions, loadEffort } from './modals.js';
import { loadSchedules } from './schedules.js';
import { permissionRequest } from './modals.js';

export async function loadSessions() {
  try {
    app.sessions = await apiClient.getSessions();
  } catch (err) {
    app.sessions = [];
  }
  try {
    const res = await apiClient.getGroups();
    if (res && Array.isArray(res.names)) app.sessionGroups = res.names;
  } catch {
    // A daemon that does not know about groups, or a request that failed:
    // leave whatever we had. An empty list is the flat panel, which is the
    // right thing to show when we cannot find out.
    if (!app.sessionGroups) app.sessionGroups = [];
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

// sessionMatchesFilter decides whether one session row survives the
// panel filter. Pure, and exported, for the same reason reorderList is:
// the answer it gives is easy to get wrong in a way the eye forgives —
// matching the shortened card text instead of the full workspace path
// would pass every glance at the panel and fail every real search.
export function sessionMatchesFilter(s, q) {
  const query = (q || '').trim().toLowerCase();
  if (!query) return true;
  // The FULL workspace path, not the shortened text the card shows. The
  // card keeps the tail (see shortenPath) and drops the leading
  // directories, which are exactly what somebody typing a directory name
  // is looking for.
  return (s.title || '').toLowerCase().includes(query) ||
    (s.workspace || '').toLowerCase().includes(query);
}

// Which groups are folded shut is the one piece of group state that stays
// in the browser. Everything else about a group — that it exists, what it
// is called, what is in it, what order they are drawn in — is the same for
// the window and the desktop build and has to survive a restart, so it
// lives on the daemon. Whether a group is folded is not about the group,
// it is about the panel in front of you, and a second window folding one
// shut should not fold it in the first. localStorage is per browser
// profile, which is exactly that scope.
//
// Every read and write is wrapped: a private window, cleared site data or
// a storage quota all throw here, and none of them is a reason for the
// session panel not to draw.
const GROUP_COLLAPSE_KEY = 'localcode.collapsedGroups';

export function readCollapsedGroups() {
  try {
    const raw = localStorage.getItem(GROUP_COLLAPSE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

export function writeCollapsedGroups(collapsed) {
  try {
    localStorage.setItem(GROUP_COLLAPSE_KEY, JSON.stringify(collapsed));
  } catch {
    /* a private window, or storage turned off: the panel still draws */
  }
}

function renderSessionCard(s, filtering) {
  const div = document.createElement('div');
  div.className = 'session-item' + (s.id === session.sessionID ? ' active' : '');
  // The whole card switches to the session — the old dedicated "switch"
  // button made the single most common action the smallest target on
  // the row. The rename/delete buttons below stop propagation so they
  // don't switch as a side effect of being clicked.
  div.title = filtering
    ? `${s.id}\nclick to switch to this session`
    : `${s.id}\nclick to switch to this session, drag to move it up or down`;
  div.addEventListener('click', () => {
    if (s.id !== session.sessionID) selectSession(s.id, s.agent, s.workspace);
  });
  if (filtering) {
    div.draggable = false;
  } else {
    makeDraggable(div, s.id);
  }

  const title = document.createElement('div');
  title.className = 'title';
  // A dot on every working session, not just the one on screen. The
  // status line under the prompt only ever spoke for the current
  // conversation, so a turn left running in another one was invisible
  // — including a turn stuck waiting on a permission request, which
  // blocks workspace switching for every session until it is answered.
  // Four states, one dot, and every session has one:
  //   waiting        amber, steady    — stopped, and only you can restart it
  //   running        green, blinking  — the model is working
  //   answer unread  green, steady    — it finished while you were elsewhere
  //   idle           grey, steady     — nothing is happening here
  //
  // Waiting is tested first because it is almost always true *alongside*
  // running: a permission request is raised from inside a turn, and the
  // turn stays open while the question sits there. Drawing the turn
  // would be drawing the less useful of two true things — "busy" is
  // something to wait out, "waiting for you" is something to do.
  //
  // Amber is reserved for it. Every other light in the product is green
  // when something is happening and grey when nothing is, so amber
  // means one thing everywhere: this one is yours.
  //
  // The idle dot is drawn rather than omitted. A row with no light and a
  // row whose light has not been noticed look the same, so an absent dot
  // could mean "idle" or "this panel does not draw lights" — and the two
  // green states only mean something against a light that is reliably
  // there when nothing is going on.
  const unread = app.unreadSessions.has(s.id);
  const state = s.asking ? 'asking' : s.busy ? 'running' : unread ? 'unread' : 'idle';
  const led = document.createElement('span');
  led.className = 'session-led ' + state;
  led.title = {
    asking: 'this session is waiting for you to answer a permission request',
    running: 'a turn is running in this session',
    unread: 'this session has a reply you have not looked at',
    idle: 'nothing is running in this session',
  }[state];
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

  const archiveBtn = document.createElement('button');
  archiveBtn.textContent = 'archive';
  archiveBtn.title = 'put this conversation away; it keeps everything and can be retrieved';
  // Not a danger-btn. The outlined red is reserved for the one action
  // that cannot be undone, and using it here would say the opposite of
  // what this does. No confirm either, for the same reason: a confirm on
  // a reversible action teaches people to click through confirms.
  archiveBtn.addEventListener('click', (e) => { e.stopPropagation(); archiveSessionNow(s); });
  actions.appendChild(archiveBtn);

  const delBtn = document.createElement('button');
  delBtn.textContent = 'delete';
  delBtn.className = 'danger-btn';
  delBtn.addEventListener('click', (e) => { e.stopPropagation(); deleteSessionConfirm(s); });
  actions.appendChild(delBtn);

  div.appendChild(actions);
  return div;
}

export function renderSessionList() {
  sessionListEl.innerHTML = '';
  if (!app.sessions || app.sessions.length === 0) {
    sessionListEl.innerHTML = '<div style="color:var(--muted)">no sessions</div>';
    return;
  }
  const query = (app.sessionFilter || '').trim();
  const visible = app.sessions.filter(s => sessionMatchesFilter(s, query));
  if (visible.length === 0) {
    sessionListEl.innerHTML = '<div style="color:var(--muted)">no sessions match this filter</div>';
    return;
  }
  // While a filter is on, dragging is off. The rows on screen are a
  // subset of app.sessions, and dropSessionOn/reorderList count positions
  // in the full array — so a drop among filtered rows would move a
  // session nobody pointed at. Mapping the index back through the filter
  // would keep the gesture but land the card somewhere the filtered view
  // never showed, which is a stranger surprise than a gesture that waits
  // until the filter is cleared. The tooltip drops the drag half for the
  // same reason: offering a move that will not happen is worse than not
  // offering it.
  const filtering = query !== '';
  const groups = app.sessionGroups || [];

  // When no groups have been created, render flat rows exactly as today.
  // This is the regression guard: a person with no groups sees the existing panel.
  if (groups.length === 0) {
    for (const s of visible) {
      sessionListEl.appendChild(renderSessionCard(s, filtering));
    }
    return;
  }

  const collapsed = readCollapsedGroups();
  const groupSet = new Set(groups);

  // 1. Ungrouped sessions render first, with no header, above every group.
  const ungrouped = visible.filter(s => !s.group || !groupSet.has(s.group));
  for (const s of ungrouped) {
    sessionListEl.appendChild(renderSessionCard(s, filtering));
  }

  // If there are no ungrouped sessions shown, render a drop target at the top
  // so dragging a session to the top takes it out of its group.
  if (ungrouped.length === 0 && !filtering) {
    const topDrop = document.createElement('div');
    topDrop.className = 'session-group-ungrouped-drop';
    topDrop.title = 'drag here to remove from group';
    topDrop.addEventListener('dragover', (e) => {
      if (!draggingID) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
      topDrop.classList.add('drop-target');
    });
    topDrop.addEventListener('dragleave', () => topDrop.classList.remove('drop-target'));
    topDrop.addEventListener('drop', (e) => {
      e.preventDefault();
      e.stopPropagation();
      topDrop.classList.remove('drop-target');
      const from = draggingID;
      draggingID = null;
      clearDropMarkers();
      if (from) dropSessionToUngroupedTop(from);
    });
    sessionListEl.appendChild(topDrop);
  }

  // 2. Groups render in list order, each with a header showing the name and how many sessions are in it.
  for (const groupName of groups) {
    // The count is how many sessions are in the group, not how many of
    // them the filter left on screen. A group that says (2) while showing
    // one row is telling you the filter is hiding something, which is the
    // useful half; a count that shrank to match the rows on screen would
    // only repeat what you can already see.
    const inGroupAll = app.sessions.filter(s => s.group === groupName);
    const inGroupVisible = visible.filter(s => s.group === groupName);

    // If filtering and this group has no visible rows, hide its header too rather than showing an empty group.
    if (filtering && inGroupVisible.length === 0) {
      continue;
    }

    // A filter opens every group it matched. Folding a group shut says
    // "not now"; typing into the filter says "find this", and answering
    // that with a shut group holding the only row that matched would be
    // the panel refusing to show what it just found. The fold is not
    // forgotten, only overruled — clearing the filter shuts it again.
    const isCollapsed = !!collapsed[groupName] && !filtering;
    const header = document.createElement('div');
    header.className = 'session-group-header' + (isCollapsed ? ' collapsed' : '');

    const toggle = document.createElement('span');
    toggle.className = 'group-toggle';
    toggle.textContent = isCollapsed ? '▸' : '▾';
    header.appendChild(toggle);

    const nameSpan = document.createElement('span');
    nameSpan.className = 'group-name';
    nameSpan.textContent = groupName;
    header.appendChild(nameSpan);

    const countSpan = document.createElement('span');
    countSpan.className = 'group-count';
    countSpan.textContent = ` (${inGroupAll.length})`;
    header.appendChild(countSpan);

    const actions = document.createElement('span');
    actions.className = 'group-actions';

    const renameBtn = document.createElement('button');
    renameBtn.className = 'icon-btn group-rename-btn';
    renameBtn.textContent = 'rename';
    renameBtn.title = 'rename group';
    renameBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      promptRenameGroup(groupName);
    });
    actions.appendChild(renameBtn);

    // A danger-btn, unlike the archive button on a session card. Archiving
    // keeps everything and hands it back; deleting a group does not — the
    // sessions survive, but how they were arranged is gone, and no click
    // puts it back. That is what the outlined red is for.
    const deleteBtn = document.createElement('button');
    deleteBtn.className = 'icon-btn danger-btn group-delete-btn';
    deleteBtn.textContent = 'delete';
    deleteBtn.title = 'delete group';
    deleteBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      promptDeleteGroup(groupName);
    });
    actions.appendChild(deleteBtn);

    header.appendChild(actions);

    header.addEventListener('click', () => {
      const cur = readCollapsedGroups();
      if (cur[groupName]) delete cur[groupName];
      else cur[groupName] = true;
      // Only the groups that are shut are kept, and only the ones that
      // still exist. A group that was folded and then renamed or deleted
      // would otherwise leave its name here for good, and the entry would
      // fold the group shut again if that name ever came back.
      writeCollapsedGroups(Object.fromEntries(
        Object.keys(cur).filter(name => cur[name] && groupSet.has(name)).map(name => [name, true]),
      ));
      renderSessionList();
    });

    if (!filtering) {
      wireGroupHeaderDrop(header, groupName);
    }

    sessionListEl.appendChild(header);

    if (!isCollapsed) {
      for (const s of inGroupVisible) {
        sessionListEl.appendChild(renderSessionCard(s, filtering));
      }
    }
  }
}

function wireGroupHeaderDrop(header, groupName) {
  header.addEventListener('dragover', (e) => {
    if (!draggingID) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    header.classList.add('drop-target');
  });
  header.addEventListener('dragleave', () => header.classList.remove('drop-target'));
  header.addEventListener('drop', (e) => {
    e.preventDefault();
    e.stopPropagation();
    header.classList.remove('drop-target');
    const from = draggingID;
    draggingID = null;
    clearDropMarkers();
    if (from) dropSessionOnGroupHeader(from, groupName);
  });
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
  const fromIndex = app.sessions.findIndex(s => s.id === fromID);
  const toIndex = app.sessions.findIndex(s => s.id === toID);
  if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex) return;

  const targetSession = app.sessions[toIndex];
  const targetGroup = targetSession.group || '';
  const fromSession = app.sessions[fromIndex];
  const oldGroup = fromSession.group || '';

  const beforeSessions = app.sessions.map(s => ({ ...s }));
  const groupChanged = oldGroup !== targetGroup;

  app.sessions = reorderList(beforeSessions, fromID, toID);
  const moved = app.sessions.find(s => s.id === fromID);
  if (moved) {
    moved.group = targetGroup;
  }
  renderSessionList();

  try {
    if (groupChanged) {
      await apiClient.setSessionGroup(fromID, targetGroup);
    }
    await apiClient.reorderSessions(app.sessions.map(s => s.id));
  } catch (err) {
    appendError(`could not save the session order: ${err}`);
    app.sessions = beforeSessions;
    renderSessionList();
  }
}

// Dropping a card on a group's header puts it in that group, at the top.
// The header is the one drop target a collapsed group still offers, and
// "the top" is the only position it can mean — the rows it would be placed
// among are not on screen.
export async function dropSessionOnGroupHeader(fromID, groupName) {
  const fromIndex = app.sessions.findIndex(s => s.id === fromID);
  if (fromIndex < 0) return;
  const fromSession = app.sessions[fromIndex];

  const beforeSessions = app.sessions.map(s => ({ ...s }));
  fromSession.group = groupName;

  const firstInGroup = app.sessions.findIndex(s => s.id !== fromID && s.group === groupName);
  if (firstInGroup >= 0) {
    app.sessions = reorderList(app.sessions, fromID, app.sessions[firstInGroup].id);
  }
  renderSessionList();

  try {
    await apiClient.setSessionGroup(fromID, groupName);
    await apiClient.reorderSessions(app.sessions.map(s => s.id));
  } catch (err) {
    appendError(`could not move session into group: ${err}`);
    app.sessions = beforeSessions;
    renderSessionList();
  }
}

// Dropping a card on the strip above the first group takes it out of
// whatever group it was in. The strip is only drawn when every session is
// in a group: with ungrouped rows on screen there is already somewhere to
// drop a card to ungroup it, and an empty target above them would be a
// second way to do the same thing.
export async function dropSessionToUngroupedTop(fromID) {
  const fromIndex = app.sessions.findIndex(s => s.id === fromID);
  if (fromIndex < 0) return;
  const fromSession = app.sessions[fromIndex];
  const oldGroup = fromSession.group || '';

  const beforeSessions = app.sessions.map(s => ({ ...s }));
  const [moved] = app.sessions.splice(fromIndex, 1);
  moved.group = '';
  app.sessions.unshift(moved);
  renderSessionList();

  try {
    if (oldGroup !== '') {
      await apiClient.setSessionGroup(fromID, '');
    }
    await apiClient.reorderSessions(app.sessions.map(s => s.id));
  } catch (err) {
    appendError(`could not remove session from group: ${err}`);
    app.sessions = beforeSessions;
    renderSessionList();
  }
}

export async function promptCreateGroup() {
  const name = window.prompt('New group name:');
  if (name === null) return;
  const trimmed = name.trim();
  if (!trimmed) {
    appendError('group name cannot be empty');
    return;
  }
  if ((app.sessionGroups || []).includes(trimmed)) {
    appendError(`duplicate group name "${trimmed}"`);
    return;
  }
  const next = [...(app.sessionGroups || []), trimmed];
  try {
    await apiClient.setGroups(next);
    app.sessionGroups = next;
    renderSessionList();
  } catch (err) {
    appendError(`could not create group: ${err}`);
  }
}

export async function promptRenameGroup(oldName) {
  const newName = window.prompt('Rename group:', oldName);
  if (newName === null || newName === oldName) return;
  const trimmed = newName.trim();
  if (!trimmed) {
    appendError('group name cannot be empty');
    return;
  }
  const next = (app.sessionGroups || []).map(g => g === oldName ? trimmed : g);
  try {
    // Say that this is a rename. The daemon will not work it out from the
    // two lists, because the same difference is produced by deleting one
    // group and making another — and it has to know which, to decide
    // whether the sessions come along.
    await apiClient.setGroups(next, { from: oldName, to: trimmed });
    await loadSessions();
  } catch (err) {
    appendError(`could not rename group: ${err}`);
  }
}

export async function promptDeleteGroup(groupName) {
  if (!window.confirm(`Delete group "${groupName}"? Sessions in this group will become ungrouped.`)) return;
  const next = (app.sessionGroups || []).filter(g => g !== groupName);
  try {
    await apiClient.setGroups(next);
    await loadSessions();
  } catch (err) {
    appendError(`could not delete group: ${err}`);
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
    forgetDraft(s.id);
  } catch (err) {
    appendError(`failed to delete session: ${err}`);
    return;
  }
  // Both lists, because this one button serves both. The archive rows
  // carry a delete of their own (renderArchiveList), so deleting an
  // archived conversation used to leave it on screen with the count
  // unchanged: the same staleness the count fix was for, on the one path
  // that fix did not reach.
  await loadArchived();
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
  rememberOpenSession(id);
  // What is in the box was typed into the conversation being left, so it
  // goes with it. Read before resetSession, which is where sessionID stops
  // naming that conversation.
  stashDraft(session.sessionID, inputEl.value);
  resetSession(id);
  // And the one being opened gets back whatever it was holding, which is
  // usually nothing. Set unconditionally: an empty draft has to clear the
  // box, or the stash would only ever add text and never take it away.
  inputEl.value = draftFor(id);
  autoResizeInput();
  // A conversation opens at its end, including one opened again after
  // somebody asked to see all of a different one.
  resetTranscriptWindow();
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
  // The four permission switches belong to the conversation, so the
  // panel and the pill would otherwise go on showing the last one's.
  loadSessionPermissions(id);
  loadEffort(id);
  // Booked work belongs to the conversation too, so the panel would
  // otherwise go on showing the last one's.
  loadSchedules(id);
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
  // The archive is named because delete all empties it too, and a shelf
  // is where things go precisely so they are not lost. Somebody who put
  // ten conversations away and then cleared the list is entitled to know
  // that before the click, not after it.
  if (!window.confirm('Delete ALL sessions, including archived ones? This cannot be undone.')) return;
  try {
    await apiClient.deleteAllSessions();
  } catch (err) {
    appendError(`failed to delete all sessions: ${err}`);
    return;
  }
  // Delete all removes the archived conversations too, so the header that
  // counts them is describing a list that is now empty.
  await loadArchived();
  await createNewSession();
}

// The archive.
//
// Archiving is not deleting and the panel says so in every way it can: no
// confirm, no red, and the conversation stays one click from coming back.
// What it is for is a list that has grown past the point of being scannable
// without anybody wanting to lose anything.

export async function archiveSessionNow(s) {
  try {
    await apiClient.archiveSession(s.id);
  } catch (err) {
    // 409 with a body naming the tasks or schedules still going. Shown as
    // it came: "wait for them or cancel them first" is the useful part and
    // it is already in the message.
    appendError(`failed to archive: ${err}`);
    return;
  }
  // An unread mark on a session no longer in the list can never be
  // cleared by opening it, so it would sit there forever.
  app.unreadSessions.delete(s.id);
  await loadSessions();
  // Unconditionally, not only when the section is open. The header shows
  // a count, and a count read out of a list the page only fetches while
  // the section is expanded is a claim about data it has not loaded: the
  // conversation moved and the header went on showing the number it last
  // happened to see.
  await loadArchived();
  if (s.id === session.sessionID) await moveOffArchivedSession();
}

export async function retrieveSessionNow(s) {
  try {
    await apiClient.retrieveSession(s.id);
  } catch (err) {
    appendError(`failed to retrieve: ${err}`);
    return;
  }
  await loadSessions();
  await loadArchived();
}

// Where to go when the conversation on screen has just left the list.
// Fetched after the move, never before it: counting sessions and then
// archiving is a window another client can archive or delete the fallback
// inside.
async function moveOffArchivedSession() {
  // Both branches clear the transcript on their way in, so anything worth
  // saying has to be said after the switch, not before it.
  if (app.sessions.length > 0) {
    const next = app.sessions[0];
    selectSession(next.id, next.agent, next.workspace);
  } else {
    await createNewSession();
    appendTool('[that was the only conversation, so a new one was started]');
  }
}

export async function loadArchived() {
  try {
    app.archivedSessions = await apiClient.getArchivedSessions();
  } catch {
    app.archivedSessions = [];
  }
  renderArchiveList();
}

function renderArchiveList() {
  // The number, whenever there is one. An empty archive is "Archive": a
  // zero is a count of nothing and reads as a fault.
  const n = (app.archivedSessions || []).length;
  archiveToggleEl.textContent = n > 0 ? `Archive (${n})` : 'Archive';
  archiveToggleEl.setAttribute('aria-expanded', app.archiveOpen ? 'true' : 'false');
  archiveListEl.hidden = !app.archiveOpen;
  archiveListEl.innerHTML = '';
  if (!app.archiveOpen) return;

  if (n === 0) {
    const empty = document.createElement('div');
    empty.className = 'meta';
    empty.textContent = 'Nothing archived. Drag a conversation here, or use its archive button.';
    archiveListEl.appendChild(empty);
    return;
  }

  for (const s of app.archivedSessions) {
    const div = document.createElement('div');
    // No session-led, no click handler and no draggable: an archived
    // conversation is not one you can be in, and offering the gestures
    // that open one would be a promise the daemon then refuses.
    div.className = 'session-item archived';

    const title = document.createElement('div');
    title.className = 'title';
    title.textContent = s.title || s.id;
    div.appendChild(title);

    // Which project it belonged to, the same line the live rows carry.
    // A shelf is where conversations go to stop being distinguishable —
    // twenty of them titled after the thing they were about, with
    // nothing saying which of four projects that was — and the
    // workspace is what tells two of them apart before you retrieve one
    // to find out.
    const workspace = document.createElement('div');
    workspace.className = 'workspace';
    workspace.textContent = s.workspace ? shortenPath(s.workspace) : '(workspace not recorded)';
    workspace.title = s.workspace || 'this session predates workspace tracking';
    div.appendChild(workspace);

    // When it was put away. The line itself is hidden on an archived row
    // — the two buttons stand in its place, since they are the only
    // things you can do here — so the row carries it as a tooltip
    // instead of rendering text nothing will ever show.
    const meta = document.createElement('div');
    meta.className = 'meta';
    meta.textContent = `archived ${formatTime(s.archived_at)}`;
    div.title = meta.textContent;
    div.appendChild(meta);

    const actions = document.createElement('div');
    actions.className = 'actions';

    const retrieveBtn = document.createElement('button');
    retrieveBtn.textContent = 'retrieve';
    retrieveBtn.title = 'bring this conversation back into the session list';
    retrieveBtn.addEventListener('click', (e) => { e.stopPropagation(); retrieveSessionNow(s); });
    actions.appendChild(retrieveBtn);

    const delBtn = document.createElement('button');
    delBtn.textContent = 'delete';
    delBtn.className = 'danger-btn';
    delBtn.addEventListener('click', (e) => { e.stopPropagation(); deleteSessionConfirm(s); });
    actions.appendChild(delBtn);

    div.appendChild(actions);
    archiveListEl.appendChild(div);
  }
}

export async function toggleArchive() {
  app.archiveOpen = !app.archiveOpen;
  try { localStorage.setItem('archiveOpen', app.archiveOpen ? '1' : ''); } catch { /* private window */ }
  if (app.archiveOpen) {
    await loadArchived();
  } else {
    renderArchiveList();
  }
}

// Dragging a session onto the archive.
//
// The header is itself the drop zone, rather than the section around it: a
// zone with element children needs the drop to bubble, and this page's own
// test DOM has no bubbling, so a container zone would look right in a
// browser and be untestable here.
//
// One way only. Retrieve is a button, not a drag back out, which keeps
// draggingID the single drag-state variable: a second one would need every
// active row's dragover to reject the wrong kind, and the row drop handler
// calls stopPropagation unconditionally.
export function wireArchiveDrop() {
  archiveToggleEl.addEventListener('click', toggleArchive);
  // The collapsed state is drawn rather than assumed from the markup, so
  // the label and aria-expanded have one owner and cannot disagree with
  // what the section is actually doing.
  renderArchiveList();

  archiveToggleEl.addEventListener('dragover', (e) => {
    if (!draggingID) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    archiveToggleEl.classList.add('drag-over');
    // The label changes as well as the border, so the live state is
    // readable without having to perceive a colour.
    archiveToggleEl.textContent = 'drop to archive';
  });
  archiveToggleEl.addEventListener('dragleave', () => {
    archiveToggleEl.classList.remove('drag-over');
    renderArchiveList();
  });
  archiveToggleEl.addEventListener('drop', (e) => {
    e.preventDefault();
    e.stopPropagation();
    const id = draggingID;
    draggingID = null;
    archiveToggleEl.classList.remove('drag-over');
    clearDropMarkers();
    const s = (app.sessions || []).find((x) => x.id === id);
    if (s) archiveSessionNow(s);
    else renderArchiveList();
  });
}

// Which conversation this window is looking at, kept across a reload.
//
// sessionStorage rather than localStorage, and the difference is the
// point: it is per tab and per window, so two windows on one daemon each
// come back to their own conversation instead of fighting over one key.
// It survives a reload and does not survive the window closing, which is
// exactly the lifetime wanted — a fresh window starting on the newest
// conversation is the old behaviour and is right.
//
// Best-effort throughout. A WebView with storage disabled costs the
// person a remembered conversation, not a page that fails to start.
const OPEN_SESSION_KEY = 'localcode.openSession';

export function rememberOpenSession(id) {
  try {
    sessionStorage.setItem(OPEN_SESSION_KEY, id || '');
  } catch { /* storage refused: the next reload starts at the top */ }
}

export function rememberedOpenSession() {
  try {
    return sessionStorage.getItem(OPEN_SESSION_KEY) || '';
  } catch {
    return '';
  }
}
