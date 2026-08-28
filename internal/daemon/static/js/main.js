import {
  inputEl, sendBtn, agentSelectEl, newSessionBtn, deleteAllSessionsBtn,
  permissionAllowBtn, permissionAllowSessionBtn, permissionAllowAlwaysBtn, permissionDenyBtn,
  autoDelegateBtn, delegateCloseBtn, delegateEnabledCheckbox, delegateAgentSelect,
  delegateMatchAddBtn, delegateMatchInput,
  permissionStatusBtn, permissionSettingsCloseBtn, skipPermissionsCheckbox, ruleAddBtn,
  workspaceBtn, workspaceCancelBtn, workspaceSaveBtn, workspaceInput, stopBtn,
  workspaceBrowseBtn, workspaceRevealBtn, workspaceStopBusyBtn, taskCancelBtn, taskDeleteBtn, taskCloseBtn,
  windowBarEl, windowMinimizeBtn, windowMaximizeBtn, windowCloseBtn, windowEdgesEl, windowTitleEl, windowEdges,
} from './dom.js';
import { app, session } from './state.js';
import { uploadFile, switchAgent } from './api.js';
import { appendError } from './transcript.js';
import { renderTasks, renderStatusBar } from './render.js';
import {
  sendMessage, cancelTurn, autoResizeInput, insertAtCursor,
  atInputStart, atInputEnd, historyPrev, historyNext,
  navigatingHistory, endHistoryNavigation,
} from './composer.js';
import { loadAgents, loadCommands, loadSkills, loadSlashCommands, loadSettings, loadWorkspace, loadMCPServers, loadVersion, cycleAgent } from './loaders.js';
import { loadSessions, selectSession, createNewSession, deleteAllSessions } from './sessions.js';
import {
  resolvePermission, openAutoDelegateSettings, closeDelegateModal, saveAutoDelegate, addDelegateMatch,
  openPermissionSettings, closePermissionSettings, toggleSkipPermissions, addPermissionRule,
  openWorkspacePicker, closeWorkspaceModal, saveWorkspace, anyModalOpen, permissionRequest,
  browseWorkspace, revealWorkspace, stopBlockingTurns,
} from './modals.js';
import { closeTaskView, cancelOpenTask, deleteOpenTask, taskView } from './taskview.js';
import { initResizers } from './resize.js';
import { tryComplete, resetCompletion } from './complete.js';
import { initSettings } from './settings.js';

agentSelectEl.addEventListener('change', async () => {
  const name = agentSelectEl.value;
  if (!session.sessionID || name === session.currentAgent) return;
  try {
    await switchAgent(session.sessionID, name);
    // currentAgent updates from the agent.switched event this call causes
    // the daemon to broadcast — see events.js — not here, so every client
    // (including this one) reacts the same way.
  } catch (err) {
    agentSelectEl.value = session.currentAgent; // revert the dropdown on failure
    appendError(`failed to switch agent: ${err}`);
  }
});

newSessionBtn.addEventListener('click', createNewSession);
deleteAllSessionsBtn.addEventListener('click', deleteAllSessions);

inputEl.addEventListener('dragover', (e) => {
  e.preventDefault();
  inputEl.classList.add('drag-over');
});
inputEl.addEventListener('dragleave', () => inputEl.classList.remove('drag-over'));
inputEl.addEventListener('drop', async (e) => {
  e.preventDefault();
  inputEl.classList.remove('drag-over');
  const files = e.dataTransfer && e.dataTransfer.files;
  if (!files || files.length === 0 || !session.sessionID) return;
  for (const file of files) {
    try {
      const path = await uploadFile(session.sessionID, file);
      insertAtCursor(inputEl, `[attached file: ${path}]\n`);
    } catch (err) {
      appendError(`upload failed (${file.name}): ${err}`);
    }
  }
  autoResizeInput();
});

stopBtn.addEventListener('click', cancelTurn);

// submitPrompt is the one way a prompt leaves the box, whichever control
// asked for it. Enter and the Send button have to stay the same thing:
// a split between them is the kind nobody finds by reading.
function submitPrompt() {
  return sendMessage();
}

sendBtn.addEventListener('click', submitPrompt);
inputEl.addEventListener('input', () => {
  autoResizeInput();
  // Typing ends a history walk: what is in the box is now this person's
  // own text, and the next Up should start again from the newest entry
  // instead of continuing the walk over the top of an edit. Only real
  // typing fires this — setting .value from code (which is how recall
  // fills the box) does not.
  endHistoryNavigation();
  // The hint under the box is about what is in the box, so it moves
  // with it.
  renderStatusBar();
});
inputEl.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    submitPrompt();
  } else if (e.key === 'Escape') {
    e.preventDefault();
    cancelTurn();
  } else if (e.key === 'ArrowUp' && (atInputStart() || navigatingHistory())) {
    // Either the caret is at the very top, which starts a walk, or one is
    // already under way — in which case recall has parked the caret at the
    // end of the text it inserted and the key means "keep going".
    if (historyPrev()) e.preventDefault();
  } else if (e.key === 'ArrowDown' && (atInputEnd() || navigatingHistory())) {
    if (historyNext()) e.preventDefault();
  } else if (e.key === 'ArrowRight') {
    // Completion, but only at the very end of a one-word "/name".
    // Anywhere else the key moves the caret, which is what it is for.
    if (tryComplete()) {
      e.preventDefault();
      renderStatusBar();
    }
  }
});

// Esc works even when focus is not in the prompt box, matching the TUI.
//
// Tab does too, and for the same reason: in the TUI it cycles the agent,
// and someone moving between the two shouldn't find that the same key
// instead walks the focus ring around the page. preventDefault is what
// takes it back from the browser. Tab still does its normal job inside a
// modal and in the modals' own fields, where moving between inputs is
// the only thing it could reasonably mean.
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && e.target !== inputEl && !permissionRequest.isOpen) {
    cancelTurn();
    return;
  }
  if (e.key === 'Tab' && !e.ctrlKey && !e.metaKey && !e.altKey && !anyModalOpen()) {
    const inOtherField = e.target !== inputEl
      && e.target !== document.body
      && (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' || e.target.tagName === 'SELECT');
    if (inOtherField) return;
    e.preventDefault();
    cycleAgent(e.shiftKey ? -1 : 1);
  }
});

permissionAllowBtn.addEventListener('click', () => resolvePermission(true, 'once'));
permissionAllowSessionBtn.addEventListener('click', () => resolvePermission(true, 'session'));
permissionAllowAlwaysBtn.addEventListener('click', () => resolvePermission(true, 'always'));
permissionDenyBtn.addEventListener('click', () => resolvePermission(false, 'once'));

autoDelegateBtn.addEventListener('click', openAutoDelegateSettings);
delegateCloseBtn.addEventListener('click', closeDelegateModal);
delegateEnabledCheckbox.addEventListener('change', () => {
  saveAutoDelegate({ enabled: delegateEnabledCheckbox.checked });
});
delegateAgentSelect.addEventListener('change', () => {
  saveAutoDelegate({ agent: delegateAgentSelect.value });
});
delegateMatchAddBtn.addEventListener('click', addDelegateMatch);
delegateMatchInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); addDelegateMatch(); }
});
permissionStatusBtn.addEventListener('click', openPermissionSettings);
permissionSettingsCloseBtn.addEventListener('click', closePermissionSettings);
skipPermissionsCheckbox.addEventListener('change', () => toggleSkipPermissions(skipPermissionsCheckbox.checked));
ruleAddBtn.addEventListener('click', addPermissionRule);

workspaceBtn.addEventListener('click', openWorkspacePicker);
workspaceBrowseBtn.addEventListener('click', browseWorkspace);
workspaceRevealBtn.addEventListener('click', revealWorkspace);
workspaceStopBusyBtn.addEventListener('click', stopBlockingTurns);
taskCloseBtn.addEventListener('click', closeTaskView);
taskCancelBtn.addEventListener('click', cancelOpenTask);
taskDeleteBtn.addEventListener('click', deleteOpenTask);
workspaceCancelBtn.addEventListener('click', closeWorkspaceModal);
workspaceSaveBtn.addEventListener('click', saveWorkspace);
workspaceInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); saveWorkspace(); }
});

// initWindowControls shows the page's own title bar, and only where there
// is no other one.
//
// The desktop window on Windows has had its system frame removed, and
// these buttons are what went with it — so the test for "should they be
// here" is whether the window that removed it bound the function they
// call. A browser tab has never seen lcWindowCommand and gets nothing;
// the macOS window keeps its real title bar, and its traffic lights.
function initWindowControls() {
  if (typeof window.lcWindowCommand !== 'function') return;
  windowBarEl.hidden = false;
  windowEdgesEl.hidden = false;
  windowMinimizeBtn.addEventListener('click', () => window.lcWindowCommand('minimize'));
  windowMaximizeBtn.addEventListener('click', () => window.lcWindowCommand('maximize'));
  windowCloseBtn.addEventListener('click', () => window.lcWindowCommand('close'));

  // Moving and resizing are asked for from here rather than worked out by
  // the window itself.
  //
  // The window has no frame left to hit-test, and the page is rendered
  // into a child window of it, so a press on the page is that child's
  // message and the top-level window is never asked what is under the
  // cursor. v0.44.0 relied on it being asked: the buttons above worked,
  // because those are ordinary page clicks, and dragging the bar and
  // pulling an edge did nothing whatsoever.
  //
  // pointerdown, not click: this hands the press itself over, and the
  // window enters the same move or resize loop it would have entered from
  // its own title bar — including snapping to a screen edge.
  windowTitleEl.addEventListener('pointerdown', (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    window.lcWindowCommand('drag');
  });
  windowTitleEl.addEventListener('dblclick', () => window.lcWindowCommand('maximize'));
  for (const { edge, el } of windowEdges) {
    el.addEventListener('pointerdown', (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      window.lcWindowCommand('resize:' + edge);
    });
  }
}

async function init() {
  initWindowControls();
  initResizers();
  initSettings();
  renderTasks();
  // Independent GETs against the same daemon — each one writes its own
  // slice of app state and renders its own pane, so there is no ordering
  // between them and no reason to pay a round-trip each in sequence
  // before the page is usable.
  await Promise.all([
    loadAgents(),
    loadCommands(),
    loadSkills(),
    loadSlashCommands(),
    loadSettings(),
    loadWorkspace(),
    loadMCPServers(),
    loadVersion(),
    loadSessions(),
  ]);
  // One deterministic status-bar render after the race settles: the bar
  // reads both the settings and the agent list, and whichever of those two
  // landed second would otherwise decide what it shows.
  renderStatusBar();

  if (!app.sessions || app.sessions.length === 0) {
    await createNewSession();
  } else {
    selectSession(app.sessions[0].id, app.sessions[0].agent, app.sessions[0].workspace);
  }
}

export const ready = init();

// Re-exports below give test/webui/harness.js one namespace to read instead
// of importing every module individually. In a browser nothing imports
// main.js by name (index.html loads it as the page's entry <script type=
// module>), so this surface exists purely for the test harness.
export { session, app } from './state.js';
export { escapeHtml, formatTime, shortenPath } from './format.js';
export { renderMarkdown, inline, unwrapMath } from './markdown.js';
export { HELP_TEXT, isPlainPrompt, tryLocalCommand } from './commands.js';
export { applyEvent } from './events.js';
export { setWaiting, setConnected, rememberPrompt, historyPrev, historyNext, cancelTurn, sendMessage } from './composer.js';
export { renderTasks, renderStatusBar, renderPermissionStatus, renderAutoDelegate, renderMCPServers, setCurrentAgent } from './render.js';
export { anyModalOpen, permissionRequest, permissionSettings, delegate, workspace } from './modals.js';
export { forkSession } from './sessions.js';
export { setPanelWidth } from './resize.js';
export { taskView, openTaskView, closeTaskView } from './taskview.js';
export { settings, openSettings } from './settings.js';
export { renderSessionList, selectSession, deleteSessionConfirm, reorderList, dropSessionOn } from './sessions.js';
