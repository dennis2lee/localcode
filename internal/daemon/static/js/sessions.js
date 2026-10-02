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
  // A listing that was asked for before a drop was saved describes the
  // panel as it was before the drop, and painting it would undo the drop
  // on screen. So wait for the saves in flight (see trackSave) and, if one
  // began while the answer was on its way, ask again. It ends when the
  // drops do: a person cannot drop faster than the daemon answers.
  let groupsAreCurrent;
  let started;
  do {
    while (savesInFlight.size) await Promise.allSettled([...savesInFlight]);
    started = savesStarted;
    groupsAreCurrent = false;
    let listing;
    try {
      listing = await apiClient.getSessions();
    } catch (err) {
      listing = [];
    }
    // Held in a local until it is known to be current: a drop that began
    // meanwhile has already rewritten app.sessions, and its request sends
    // that list, so overwriting it here would save the old order.
    if (started === savesStarted) app.sessions = listing;
    try {
      const res = await apiClient.getGroups();
      if (res && Array.isArray(res.names)) {
        // Held back for the reason the listing is: a group drop that began
        // meanwhile has already rewritten this list.
        if (started === savesStarted) app.sessionGroups = res.names;
        groupsAreCurrent = true;
      }
    } catch {
      // A daemon that does not know about groups, or a request that failed:
      // leave whatever we had. An empty list is the flat panel, which is the
      // right thing to show when we cannot find out.
      if (!app.sessionGroups) app.sessionGroups = [];
    }
  } while (started !== savesStarted);
  // Both halves are in hand here and nowhere else, so this is where the
  // list is put into the order it will be drawn in, and where folds for
  // groups that are gone are dropped.
  //
  // Only when the daemon actually answered. A group can be deleted from
  // the other window or the desktop build, and this is the only place
  // this window would hear about it — but pruning against a list that
  // failed to arrive would throw away every fold in the browser on one
  // bad request.
  app.sessions = panelOrder(app.sessions, app.sessionGroups);
  if (groupsAreCurrent) forgetCollapsedGroups(app.sessionGroups);
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

// A Set, not an object keyed by name. A group is called whatever somebody
// typed, and an object answers `collapsed['constructor']` with a function
// it inherited — so a group named constructor, __proto__, toString or
// valueOf would draw folded shut on the day it was made and never open
// again, with its sessions unreachable from the panel. A Set has no
// inherited members to collide with.
export function readCollapsedGroups() {
  try {
    const raw = localStorage.getItem(GROUP_COLLAPSE_KEY);
    if (!raw) return new Set();
    const parsed = JSON.parse(raw);
    return new Set(Array.isArray(parsed) ? parsed.filter(n => typeof n === 'string') : []);
  } catch {
    return new Set();
  }
}

export function writeCollapsedGroups(collapsed) {
  try {
    localStorage.setItem(GROUP_COLLAPSE_KEY, JSON.stringify([...collapsed]));
  } catch {
    /* a private window, or storage turned off: the panel still draws */
  }
}

// forgetCollapsedGroups drops folds for groups that are gone. Nothing on
// the server prunes this, so a group that was folded and then deleted
// would leave its name here for good — and fold a brand-new group of the
// same name shut on the day it was made.
export function forgetCollapsedGroups(groups) {
  const live = new Set(groups || []);
  const collapsed = readCollapsedGroups();
  let dropped = false;
  for (const name of [...collapsed]) {
    if (!live.has(name)) {
      collapsed.delete(name);
      dropped = true;
    }
  }
  if (dropped) writeCollapsedGroups(collapsed);
  return collapsed;
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
  // Not while a card is being carried. The panel is rebuilt from scratch,
  // so a redraw mid-drag takes the dragged row out of the document: some
  // engines then never send it a dragend, and the drag state below is
  // left set. The data is already current (the callers update app.sessions
  // before they get here), so only the drawing waits, and endDrag draws it
  // the moment the drag is over.
  if (dragging()) {
    redrawWanted = true;
    return;
  }
  redrawWanted = false;
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
    topDrop.addEventListener('dragleave', (e) => {
      if (leftElement(topDrop, e)) topDrop.classList.remove('drop-target');
    });
    topDrop.addEventListener('drop', (e) => {
      e.preventDefault();
      e.stopPropagation();
      topDrop.classList.remove('drop-target');
      const from = draggingID;
      endDrag();
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
    const isCollapsed = collapsed.has(groupName) && !filtering;
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

    // A folded group hides its rows, and with them every session's light.
    // The whole point of that light is that a turn left running in another
    // conversation — or worse, one stopped waiting for a permission answer
    // — is visible without going and looking. Folding a group must not be
    // a way to lose that, so the header carries the strongest state of
    // anything it is covering, by the same order of urgency as a row.
    if (isCollapsed) {
      const hidden = inGroupAll.some(g => g.asking) ? 'asking'
        : inGroupAll.some(g => g.busy) ? 'running'
          : inGroupAll.some(g => app.unreadSessions.has(g.id)) ? 'unread'
            : '';
      if (hidden) {
        const led = document.createElement('span');
        led.className = 'session-led ' + hidden;
        led.title = {
          asking: 'a session in this group is waiting for you to answer a permission request',
          running: 'a turn is running in a session in this group',
          unread: 'a session in this group has a reply you have not looked at',
        }[hidden];
        header.appendChild(led);
      }
    }

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
      // Nothing to fold while a filter is on. The filter already overrides
      // the fold, so the click would change the panel not at all and yet
      // change what the panel looks like the moment the filter is cleared
      // — a control that does nothing now and something later is worse
      // than one that does nothing. Dragging is off while filtering for
      // the same reason.
      if (filtering) return;
      const cur = forgetCollapsedGroups(groups);
      if (cur.has(groupName)) cur.delete(groupName);
      else cur.add(groupName);
      writeCollapsedGroups(cur);
      renderSessionList();
    });

    if (!filtering) {
      wireGroupHeaderDrag(header, groupName);
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
    if (draggingGroup) {
      // Another group is being carried: this header is where it lands, on
      // the side it would end up on.
      if (draggingGroup === groupName) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
      markGroupDrop(header, groupLandsBelow(app.sessionGroups, draggingGroup, groupName));
      return;
    }
    if (!draggingID) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    header.classList.add('drop-target');
  });
  header.addEventListener('dragleave', (e) => {
    if (!leftElement(header, e)) return;
    header.classList.remove('drop-target');
    if (draggingGroup) clearGroupMarkers();
  });
  header.addEventListener('drop', (e) => {
    e.preventDefault();
    e.stopPropagation();
    header.classList.remove('drop-target');
    const group = draggingGroup;
    const from = draggingID;
    endDrag();
    if (group) {
      if (group !== groupName) dropGroupOn(group, groupName);
      return;
    }
    if (from) dropSessionOnGroupHeader(from, groupName);
  });
}

// Dragging a group by its header. The header already folds on a click, and a
// drag does not click, so the two do not meet.
function wireGroupHeaderDrag(header, groupName) {
  header.draggable = true;
  header.setAttribute('draggable', 'true');
  header.addEventListener('dragstart', (e) => {
    draggingGroup = groupName;
    draggingID = null;
    header.classList.add('dragging');
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      // Some browsers start no drag at all without data on the transfer.
      try { e.dataTransfer.setData('text/plain', groupName); } catch { /* not fatal */ }
    }
  });
  header.addEventListener('dragend', () => {
    header.classList.remove('dragging');
    endDrag();
  });
}

function clearGroupMarkers() {
  for (const el of sessionListEl.children || []) {
    el.classList.remove('group-drop-before');
    el.classList.remove('group-drop-after');
  }
}

// markGroupDrop draws the line where the carried group would land
// relative to the group whose header is under the pointer: above that header
// when the group comes from below, and under the last row of that group's
// block when it comes from above, which is the edge between that group and
// the next one. A line under the header itself would read as "inside".
function markGroupDrop(header, below) {
  clearGroupMarkers();
  let target = header;
  if (below) {
    const kids = Array.from(sessionListEl.children);
    for (let i = kids.indexOf(header) + 1; i > 0 && i < kids.length && kids[i].classList.contains('session-item'); i++) {
      target = kids[i];
    }
  }
  target.classList.add(below ? 'group-drop-after' : 'group-drop-before');
}

// draggingID is module state rather than something carried on the event,
// because the dataTransfer payload is not readable during dragover in every
// browser, and dragover is where a row has to decide whether it is a
// possible drop target at all.
let draggingID = null;

// A group header being carried, by name. Not draggingID, because the two
// drags have different places to land: a card can land on a row, on a
// header, on the strip above the groups and on the archive, and a group can
// land only on another group's header. Everything that holds work back
// while a drag is in progress asks dragging(), which is either. Each
// dragstart clears the other one, so state left behind by a dragend that
// never came cannot turn the next drag into the other kind of move.
let draggingGroup = null;
function dragging() {
  return Boolean(draggingID) || Boolean(draggingGroup);
}

// A redraw that was asked for while a card was being carried, and is owed
// when the drag ends. See renderSessionList.
let redrawWanted = false;

// endDrag is the one place a drag stops being one: a drop on any target, a
// dragend on the source, or the document watchdog below. It clears the
// drag state first and draws what was held back second, so the redraw
// sees a panel with nothing in flight.
function endDrag() {
  draggingID = null;
  draggingGroup = null;
  clearDropMarkers();
  // The archive header lights up and renames itself while a card is over
  // it, and it only puts itself back on dragleave. A drag that ends there
  // (cancelled with Escape, or by the watchdog) never sends one.
  if (archiveToggleEl.classList.contains('drag-over')) {
    archiveToggleEl.classList.remove('drag-over');
    renderArchiveList();
  }
  if (redrawWanted) renderSessionList();
}

// leftElement says whether a dragleave took the pointer out of el, as
// opposed to into something inside it. The event fires on every border
// crossed, so moving across a card's title and buttons would otherwise
// switch the drop line off and let the next dragover switch it back on.
// Engines that do not report where the pointer went (relatedTarget is
// null) get the old answer: it left.
function leftElement(el, e) {
  const to = e && e.relatedTarget;
  return !(to && typeof el.contains === 'function' && el.contains(to));
}

// The page-level backstop for a drag that never announced its end.
//
// A redraw is held back while draggingID is set, so a dragend that never
// arrives (the window lost focus mid-drag, an engine that drops it) would
// leave the panel frozen, and would leave the archive header accepting
// anything that is dropped on it. Nothing the page can see says "the drag
// is over" in that case, but something does say "no drag is going on": a
// native drag sends the page no mouse events at all, so a press, or a
// move with no button down, means the pointer is back in the page's hands.
//
// Bubble phase on purpose. A capture listener on the document would run
// before the row's own drop handler and clear draggingID before it reads
// it; this one only ever sees a drop that no row, header or strip took,
// because those stop propagation.
export function wireSessionDragGuard() {
  document.addEventListener('dragend', endDrag);
  document.addEventListener('drop', endDrag);
  for (const type of ['mousedown', 'pointerdown']) {
    document.addEventListener(type, () => { if (dragging()) endDrag(); });
  }
  document.addEventListener('mousemove', (e) => {
    if (dragging() && e.buttons === 0) endDrag();
  });
}

function makeDraggable(div, id) {
  div.draggable = true;
  div.setAttribute('draggable', 'true');

  div.addEventListener('dragstart', (e) => {
    draggingID = id;
    draggingGroup = null;
    div.classList.add('dragging');
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      // Some browsers start no drag at all without data on the transfer.
      try { e.dataTransfer.setData('text/plain', id); } catch { /* not fatal */ }
    }
  });
  div.addEventListener('dragend', () => {
    div.classList.remove('dragging');
    endDrag();
  });
  div.addEventListener('dragover', (e) => {
    if (!draggingID || draggingID === id) return;
    // preventDefault on dragover is what says "a drop is allowed here";
    // without it the browser refuses the drop and the card springs back.
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    div.classList.add('drop-target');
    // The line is on the edge the card will land against: the bottom edge
    // when it comes from above, the top edge when it comes from below.
    div.classList.toggle('drop-after', landsBelow(app.sessions, draggingID, id));
  });
  div.addEventListener('dragleave', (e) => {
    if (leftElement(div, e)) div.classList.remove('drop-target', 'drop-after');
  });
  div.addEventListener('drop', (e) => {
    e.preventDefault();
    e.stopPropagation();
    const from = draggingID;
    endDrag();
    if (from && from !== id) dropSessionOn(from, id);
  });
}

function clearDropMarkers() {
  for (const el of sessionListEl.childNodes || []) {
    if (el.classList) {
      el.classList.remove('drop-target');
      el.classList.remove('drop-after');
      el.classList.remove('group-drop-before');
      el.classList.remove('group-drop-after');
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

// landsBelow says which side of toID a card dragged from fromID ends up
// on: below it when the card comes from above, above it when it comes from
// below. That is reorderList's own rule (remove, then reinsert at the
// target's old index) read as a comparison, so the line drawn while
// dragging and the place the card lands cannot disagree. It compares
// positions in the array the panel was drawn from, which is why that array
// has to stay in drawn order (see panelOrder).
export function landsBelow(sessions, fromID, toID) {
  const from = sessions.findIndex(s => s.id === fromID);
  const to = sessions.findIndex(s => s.id === toID);
  return from >= 0 && to >= 0 && from < to;
}

// groupLandsBelow is landsBelow for groups: a group carried down onto another
// ends up below it, one carried up ends up above it, the same rule that
// moves a card. Compared by position in the list of group names, which is
// the order the panel draws them in.
export function groupLandsBelow(names, fromName, toName) {
  const from = (names || []).indexOf(fromName);
  const to = (names || []).indexOf(toName);
  return from >= 0 && to >= 0 && from < to;
}

// Group saves reach the daemon one at a time, in the order of the drops. Each
// one reads the daemon's list before it sends its own, and without a queue
// the lists would arrive in the order those reads happen to be answered in,
// leaving the daemon on an older order than the panel shows.
let groupSaves = Promise.resolve();

// sameNames says whether two lists of group names hold the same groups,
// whatever order they are in.
function sameNames(a, b) {
  if (a.length !== b.length) return false;
  const have = new Set(a);
  return b.every(name => have.has(name));
}

// dropGroupOn moves a group to where another one is, on screen first and
// then in the daemon, the way a card moves. The list of groups is all that
// is saved: the order the groups are drawn in is the order of that list, and
// the cards in each group keep the order they had.
//
// The daemon replaces its list with the one it is sent, and a group the list
// leaves out is deleted with its place in every session. So a window that has
// not heard about a group made in another window must not send its list: the
// daemon's list is read first and compared by name (the order is what this
// move changes), and if the two differ nothing is sent and the panel is read
// back.
export async function dropGroupOn(fromName, toName) {
  const before = (app.sessionGroups || []).slice();
  const from = before.indexOf(fromName);
  const to = before.indexOf(toName);
  if (from < 0 || to < 0 || from === to) return;

  const names = before.slice();
  const [moved] = names.splice(from, 1);
  names.splice(to, 0, moved);
  app.sessionGroups = names;
  app.sessions = panelOrder(app.sessions, names);
  renderSessionList();

  // A refused move is put back to the list it started from only when no
  // other drop began while it was being saved. Otherwise the panel also holds
  // that drop's move, which this snapshot would undo even if the daemon
  // accepted it, so the panel is read back instead.
  const mark = savesStarted + 1;
  let failure = null;
  let stale = false;
  await trackSave(() => {
    const turn = groupSaves.then(async () => {
      try {
        const held = await apiClient.getGroups();
        if (!held || !Array.isArray(held.names)) throw new Error('the daemon sent no list of groups');
        if (!sameNames(held.names, before)) {
          stale = true;
          return;
        }
        await apiClient.setGroups(names);
      } catch (err) {
        failure = err;
      }
    });
    groupSaves = turn;
    return turn;
  });
  if (stale) {
    // Back to the list the panel had, so that if the read-back cannot reach
    // the daemon either, the panel does not keep a move that was not saved.
    appendError('the groups were changed in another window, so the move was not saved');
    app.sessionGroups = before;
    app.sessions = panelOrder(app.sessions, before);
    renderSessionList();
    await readBack();
    return;
  }
  if (failure === null) return;
  if (savesStarted !== mark) {
    await resyncAfterPartialSave(failure, 'could not save the group order');
    return;
  }
  appendError(`could not save the group order: ${failure}`);
  app.sessionGroups = before;
  app.sessions = panelOrder(app.sessions, before);
  renderSessionList();
}

// dropSessionOn applies the move on screen first and tells the daemon
// after. The drop already happened as far as the person doing it is
// concerned; waiting a round trip to redraw would show the card snap back
// to where it was and then move again.
// panelOrder puts a session list into the order the panel draws it:
// ungrouped first, then group by group, each keeping the order it already
// had. Everything that moves a card decides which side of the target to
// land on by comparing two positions, and once any group exists the order
// app.sessions is in and the order the rows appear in stop being the same
// list — so a card dragged downward across a group boundary lands above
// the row it was dropped on. Sorting the array into the order it is drawn
// in makes that one comparison honest again, and it is also the order that
// then goes to the daemon, so a reload draws what the drag left behind.
export function panelOrder(sessions, groups) {
  const lane = new Map((groups || []).map((name, i) => [name, i]));
  const laneOf = (s) => (s.group && lane.has(s.group) ? lane.get(s.group) : -1);
  // Array.prototype.sort is stable, so rows inside one lane keep the order
  // they arrived in and only the lanes move.
  return sessions.slice().sort((a, b) => laneOf(a) - laneOf(b));
}

// snapshot copies the list deeply enough to put it back. The rows are
// copied because a drop rewrites the group on one of them; a shallow
// slice would hand the rollback the very object the drop mutated.
function snapshot(sessions) {
  return sessions.map(s => ({ ...s }));
}

// resyncAfterPartialSave is what a drop does when part of it was saved and
// part of it was refused. The panel cannot be put back, because putting it
// back would show a state the daemon has already contradicted; and it
// cannot be left as it is, because the half that was refused never
// happened. So it asks. One request, and the panel shows what is true.
async function resyncAfterPartialSave(err, what) {
  appendError(`${what}: ${err}`);
  await readBack();
}

// readBack asks the daemon what the panel should show, for the cases where
// the page cannot work that out for itself.
async function readBack() {
  try {
    await loadSessions();
  } catch (reloadErr) {
    appendError(`could not read the session list back: ${reloadErr}`);
  }
}

// A drop is saved in up to two requests, and a listing fetched while they
// are in flight can be answered from before either landed. Painting that
// listing would put the card back where it was, while the daemon (a moment
// later) holds the new place. So the requests of a drop are tracked:
// loadSessions waits for them before it asks, and asks again if a drop
// began while it was waiting for the answer.
//
// savesStarted counts the drops, which is all "did a drop begin since I
// asked" needs: a listing is never requested while a save is in flight, so
// a save cannot end during one.
const savesInFlight = new Set();
let savesStarted = 0;

// trackSave runs work as one tracked save. work must not reject: the drop
// functions catch their own failures, and settle the panel after this
// returns, outside the tracked region, because settling can be a
// loadSessions and that waits for every tracked save, itself included.
function trackSave(work) {
  savesStarted++;
  const run = work().finally(() => savesInFlight.delete(run));
  savesInFlight.add(run);
  return run;
}

// saveMove tells the daemon what a drop did, after the panel already shows
// it: the card's new group when that changed (group is undefined when it
// did not), then the whole order. A refused group change saved nothing, so
// the panel goes back to before. A refused order after a saved group
// change is the one half-saved outcome, and the panel is read back instead
// (see resyncAfterPartialSave). A refused order on its own saved nothing.
async function saveMove(fromID, group, before, groupFailed, placeFailed) {
  let stage = '';
  let failure = null;
  await trackSave(async () => {
    if (group !== undefined) {
      try {
        await apiClient.setSessionGroup(fromID, group);
      } catch (err) {
        stage = 'group';
        failure = err;
        return;
      }
    }
    try {
      await apiClient.reorderSessions(app.sessions.map(s => s.id));
    } catch (err) {
      stage = 'order';
      failure = err;
    }
  });
  if (stage === '') return;
  if (stage === 'order' && group !== undefined) {
    await resyncAfterPartialSave(failure, placeFailed);
    return;
  }
  appendError(`${stage === 'group' ? groupFailed : 'could not save the session order'}: ${failure}`);
  app.sessions = before;
  renderSessionList();
}

export async function dropSessionOn(fromID, toID) {
  const fromIndex = app.sessions.findIndex(s => s.id === fromID);
  const toIndex = app.sessions.findIndex(s => s.id === toID);
  if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex) return;

  const targetGroup = app.sessions[toIndex].group || '';
  const oldGroup = app.sessions[fromIndex].group || '';
  const groupChanged = oldGroup !== targetGroup;

  const before = snapshot(app.sessions);
  const next = reorderList(snapshot(app.sessions), fromID, toID);
  const moved = next.find(s => s.id === fromID);
  if (moved) moved.group = targetGroup;
  app.sessions = panelOrder(next, app.sessionGroups);
  renderSessionList();

  await saveMove(
    fromID,
    groupChanged ? targetGroup : undefined,
    before,
    'could not move the session into that group',
    'the session moved group but its place could not be saved',
  );
}

// Dropping a card on a group's header puts it in that group, at the top.
// The header is the one drop target a collapsed group still offers, and
// "the top" is the only position it can mean — the rows it would be placed
// among are not on screen.
//
// A card that is already in the group is only reordered: no group request
// is sent for a group it is in, and a card that is already first has
// nothing to save at all.
export async function dropSessionOnGroupHeader(fromID, groupName) {
  const fromIndex = app.sessions.findIndex(s => s.id === fromID);
  if (fromIndex < 0) return;

  const groupChanged = (app.sessions[fromIndex].group || '') !== groupName;
  if (!groupChanged && app.sessions.find(s => s.group === groupName).id === fromID) return;

  const before = snapshot(app.sessions);
  const next = snapshot(app.sessions);
  const [moved] = next.splice(next.findIndex(s => s.id === fromID), 1);
  moved.group = groupName;
  // In front of everything already in the group, wherever the group sits.
  // Splicing it in beside the group's first row and then sorting by lane
  // would work too, but only while the sort is stable; putting it at the
  // head of the array and letting panelOrder carry it into its lane says
  // "first in this group" without depending on that.
  next.unshift(moved);
  app.sessions = panelOrder(next, app.sessionGroups);
  renderSessionList();

  await saveMove(
    fromID,
    groupChanged ? groupName : undefined,
    before,
    'could not move the session into that group',
    'the session moved group but its place could not be saved',
  );
}

// Dropping a card on the strip above the first group takes it out of
// whatever group it was in. The strip is only drawn when every session is
// in a group: with ungrouped rows on screen there is already somewhere to
// drop a card to ungroup it, and an empty target above them would be a
// second way to do the same thing.
export async function dropSessionToUngroupedTop(fromID) {
  const fromIndex = app.sessions.findIndex(s => s.id === fromID);
  if (fromIndex < 0) return;
  const oldGroup = app.sessions[fromIndex].group || '';

  const before = snapshot(app.sessions);
  const next = snapshot(app.sessions);
  const [moved] = next.splice(fromIndex, 1);
  moved.group = '';
  next.unshift(moved);
  app.sessions = panelOrder(next, app.sessionGroups);
  renderSessionList();

  await saveMove(
    fromID,
    oldGroup !== '' ? '' : undefined,
    before,
    'could not take the session out of its group',
    'the session left its group but its place could not be saved',
  );
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
    // The fold belongs to the group, not to the name it had. Carrying it
    // across means renaming a folded group leaves it folded, and the old
    // name does not stay behind to fold a future group of that name.
    const collapsed = readCollapsedGroups();
    if (collapsed.has(oldName)) {
      collapsed.delete(oldName);
      collapsed.add(trimmed);
      writeCollapsedGroups(collapsed);
    }
    app.sessionGroups = next;
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
    app.sessionGroups = next;
    // The daemon has taken the deletion, so the fold goes with it.
    forgetCollapsedGroups(next);
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
    archiveToggleEl.classList.remove('drag-over');
    endDrag();
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
