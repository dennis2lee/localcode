import {
  modalEl,
  delegateModal, delegateEnabledCheckbox, delegateAgentSelect, delegateMatchListEl,
  delegateMatchInput, delegateNote,
  permissionSettingsModal, skipPermissionsCheckbox, skipToolsCheckbox,
  readOutsideCheckbox, writeOutsideCheckbox, permissionScopeNote,
  permissionWorkspaceNote, rememberedOutsideEl, permissionRulesListEl,
  ruleToolInput, ruleMatchInput, ruleDecisionSelect,
  permissionSettingsNote,
  workspaceModal, workspaceInput, workspaceNote, workspaceBrowseBtn, workspaceStopBusyBtn,
} from './dom.js';
import { app, session } from './state.js';
import * as apiClient from './api.js';
import { appendError } from './transcript.js';
import { setInputLocked, renderCommDot } from './composer.js';
import { renderPermissionStatus, renderAutoDelegate, renderWorkspace } from './render.js';
import { Modal } from './modal.js';
import { settings } from './settings.js';
import { taskView } from './taskview.js';
// Circular with sessions.js, which imports permissionRequest from here —
// safe for the same reason as the events.js/modals.js pair: both
// references are only ever called from a function body at runtime, never
// while the module is being evaluated.
import { renderSessionList } from './sessions.js';

const workspaceNoteDefault = 'Changing this restarts relative-path resolution for every tool from the new directory. Refused while a turn is in progress.';

// The four modals, each owning its own open/closed flag. Exported because
// events.js drives the permission one from the SSE stream and main.js needs
// to know whether Escape should close something.
export const permissionRequest = new Modal(modalEl);
export const permissionSettings = new Modal(permissionSettingsModal);
export const delegate = new Modal(delegateModal);
export const workspace = new Modal(workspaceModal);

// ---- Auto-delegation modal ----

// saveAutoDelegate posts only the fields given, so the panel's three
// controls each change one thing without restating the others. Every
// change applies to the running loop immediately and is written to
// config.json — see POST /api/settings/auto-delegate.
export async function saveAutoDelegate(patch) {
  try {
    await apiClient.setAutoDelegate(patch);
  } catch (err) {
    delegateNote.textContent = String(err);
    delegateNote.classList.add('err');
    return false;
  }
  if (typeof patch.enabled === 'boolean') app.autoDelegate = patch.enabled;
  if (typeof patch.agent === 'string') app.autoDelegateAgent = patch.agent;
  if (Array.isArray(patch.match)) app.autoDelegateMatch = patch.match.slice();
  renderAutoDelegate();
  renderDelegatePanel();
  return true;
}

export function openAutoDelegateSettings() {
  delegateNote.classList.remove('err');
  renderDelegatePanel();
  delegate.open();
}

export function renderDelegatePanel() {
  delegateEnabledCheckbox.checked = app.autoDelegate;

  // Only agents other than the one answering can be delegation targets:
  // delegating to the running agent would recurse, and the daemon refuses
  // it at turn time anyway, so it isn't offered here.
  delegateAgentSelect.innerHTML = '';
  const none = document.createElement('option');
  none.value = '';
  none.textContent = '(no agent chosen — nothing is delegated)';
  delegateAgentSelect.appendChild(none);
  for (const a of app.agents) {
    const opt = document.createElement('option');
    opt.value = a.name;
    opt.textContent = a.model ? `${a.name} (${a.model})` : a.name;
    delegateAgentSelect.appendChild(opt);
  }
  delegateAgentSelect.value = app.autoDelegateAgent || '';

  delegateMatchListEl.innerHTML = '';
  if (app.autoDelegateMatch.length === 0) {
    delegateMatchListEl.innerHTML = '<div class="note">No patterns yet, so nothing is delegated. Patterns are globs matched case-insensitively against the whole prompt: <code>*</code> is any run of characters, <code>?</code> is one.</div>';
  }
  for (const pattern of app.autoDelegateMatch) {
    const row = document.createElement('div');
    row.className = 'match-row';
    const text = document.createElement('span');
    text.className = 'match-text';
    text.textContent = pattern;
    row.appendChild(text);
    const removeBtn = document.createElement('button');
    removeBtn.textContent = 'remove';
    removeBtn.addEventListener('click', () => {
      saveAutoDelegate({ match: app.autoDelegateMatch.filter(p => p !== pattern) });
    });
    row.appendChild(removeBtn);
    delegateMatchListEl.appendChild(row);
  }

  if (!delegateNote.classList.contains('err')) {
    if (app.autoDelegate && !app.autoDelegateAgent) {
      delegateNote.textContent = 'On, but no agent is chosen, so nothing is delegated.';
    } else if (app.autoDelegate && app.autoDelegateMatch.length === 0) {
      delegateNote.textContent = 'On, but no pattern matches anything yet, so nothing is delegated.';
    } else {
      delegateNote.textContent = 'Changes apply to the next prompt and are written to config.json.';
    }
  }
}

// refreshDelegatePanelIfOpen re-renders the panel when a config.changed
// event moves auto_delegate out from under a modal the user has open —
// another client's toggle, or the /config command run from either client.
export function refreshDelegatePanelIfOpen() {
  if (delegate.isOpen) renderDelegatePanel();
}

export function addDelegateMatch() {
  const pattern = delegateMatchInput.value.trim();
  if (!pattern || app.autoDelegateMatch.includes(pattern)) return;
  delegateNote.classList.remove('err');
  saveAutoDelegate({ match: app.autoDelegateMatch.concat([pattern]) }).then(ok => {
    if (ok) delegateMatchInput.value = '';
  });
}

export function closeDelegateModal() {
  delegate.close();
}

// ---- Permission settings modal ----

export function renderPermissionRulesList() {
  permissionRulesListEl.innerHTML = '';
  const tools = Object.keys(app.permissionRules).sort();
  if (tools.length === 0) {
    permissionRulesListEl.innerHTML = '<div class="note">No custom rules yet. Built-in defaults (e.g. git auto-allowed) aren\'t listed here.</div>';
    return;
  }
  for (const tool of tools) {
    for (const rule of app.permissionRules[tool]) {
      const row = document.createElement('div');
      row.className = 'rule-row';
      const text = document.createElement('span');
      text.className = 'rule-text';
      text.textContent = `${tool}: "${rule.match}" -> ${rule.decision}`;
      row.appendChild(text);
      if (app.canEditPermissions) {
        const removeBtn = document.createElement('button');
        removeBtn.textContent = 'remove';
        removeBtn.addEventListener('click', () => removePermissionRule(tool, rule));
        row.appendChild(removeBtn);
      }
      permissionRulesListEl.appendChild(row);
    }
  }
}

// ---- The four switches, per conversation ----

// The checkbox for each switch, in the order the panel shows them: the
// two blankets first, then the two that survive the second blanket.
const SWITCH_BOXES = [
  ['skip_all', () => skipPermissionsCheckbox],
  ['skip_tools', () => skipToolsCheckbox],
  ['read_outside', () => readOutsideCheckbox],
  ['write_outside', () => writeOutsideCheckbox],
];

// applySessionPermissions takes one whole snapshot from the daemon and
// applies it. A snapshot rather than a field, so a client that missed an
// event cannot end up half-updated.
export function applySessionPermissions(d) {
  if (!d || !d.effective) return;
  app.sessionPermissions = d.effective;
  app.permissionSource = d.source || {};
  app.rememberedOutside = d.remembered || { read: [], write: [] };
  renderPermissionStatus();
  refreshPermissionSettingsIfOpen();
}

// loadSessionPermissions fetches them for the open conversation. Called
// on a session switch, because these belong to the conversation and the
// panel would otherwise show the previous one's.
export async function loadSessionPermissions(sessionID) {
  if (!sessionID) return;
  try {
    const answer = await apiClient.getSessionPermissions(sessionID);
    // Switching conversations fires this without waiting for it, so two
    // switches in quick succession race and whichever reply lands last
    // wins. loadWorkspace next to it already guards; these two did not,
    // and the permissions pill could end up describing a conversation you
    // had already left.
    if (sessionID !== session.sessionID) return;
    applySessionPermissions(answer);
  } catch {
    // A daemon that cannot answer leaves the last snapshot standing,
    // which is better than blanking the panel to a state nothing is in.
  }
}

// setSessionSwitch is a checkbox click. The checkbox shows the effective
// answer and clicking it writes this conversation's own, which is why
// there is no third visual state: "unset" and "set to what the default
// happens to be" look the same and behave the same until the default
// changes, and the source line under the panel says which one it is.
async function setSessionSwitch(name, enabled) {
  if (!session.sessionID) return;
  try {
    applySessionPermissions(await apiClient.setSessionPermission(session.sessionID, name, enabled));
  } catch (err) {
    permissionSettingsNote.textContent = `could not change ${name}: ${err.message}`;
    permissionSettingsNote.classList.add('err');
  }
}

// renderRememberedOutside lists the directories answered with "allow this
// directory" at a prompt, each with a way to take it back. Without this
// the grant is a decision with no record and no undo.
function renderRememberedOutside() {
  rememberedOutsideEl.innerHTML = '';
  for (const cls of ['read', 'write']) {
    const dirs = (app.rememberedOutside && app.rememberedOutside[cls]) || [];
    if (dirs.length === 0) continue;
    const row = document.createElement('div');
    row.className = 'rule-row';
    const text = document.createElement('span');
    text.className = 'rule-text';
    text.textContent = `${cls}: ${dirs.join(', ')}`;
    row.appendChild(text);
    const clear = document.createElement('button');
    clear.textContent = 'forget';
    clear.title = `same as /${cls}-outside mem-clear`;
    clear.addEventListener('click', async () => {
      try {
        applySessionPermissions(await apiClient.forgetOutside(session.sessionID, cls));
      } catch { /* the list simply stays as it was */ }
    });
    row.appendChild(clear);
    rememberedOutsideEl.appendChild(row);
  }
}

// refreshPermissionSettingsIfOpen re-reads the checkboxes from app state
// after somebody else changed them: a "/read-outside" typed at a prompt,
// another window's own checkbox, or an "allow anywhere" answer given to
// a prompt. Only when the panel is open, since that is the only time it
// is on screen; the pill beside the status line is redrawn either way.
export function refreshPermissionSettingsIfOpen() {
  if (!permissionSettingsModal || permissionSettingsModal.hidden) return;
  drawPermissionSettings();
}

function drawPermissionSettings() {
  for (const [name, box] of SWITCH_BOXES) {
    box().checked = !!app.sessionPermissions[name];
  }
  const sources = app.permissionSource || {};
  const inherited = Object.keys(sources).filter(k => sources[k] === 'parent');
  permissionScopeNote.textContent =
    'These four apply to this conversation only. Defaults for new ones come from config.json.'
    + (inherited.length ? ` Inherited from the conversation that started this one: ${inherited.join(', ')}.` : '');
  permissionWorkspaceNote.textContent = app.workspacePath
    ? `Paths that land outside ${app.workspacePath}: an absolute path, a "..", a symlink that leads out.`
    : 'Paths that land outside this conversation\'s own directory.';
  renderRememberedOutside();
}

export function openPermissionSettings() {
  drawPermissionSettings();
  ruleToolInput.disabled = ruleMatchInput.disabled = ruleDecisionSelect.disabled = !app.canEditPermissions;
  permissionSettingsNote.classList.remove('err');
  permissionSettingsNote.textContent = app.canEditPermissions
    ? 'Rules apply immediately and are written to config.json. The four switches above are saved with the conversation.'
    : 'No config.json path is available in this run, so rules are read-only.';
  renderPermissionRulesList();
  permissionSettings.open();
}

// wireSessionPermissionCheckboxes is called once from main.js.
export function wireSessionPermissionCheckboxes() {
  for (const [name, box] of SWITCH_BOXES) {
    box().addEventListener('change', (e) => setSessionSwitch(name, e.target.checked));
  }
}

export function closePermissionSettings() {
  permissionSettings.close();
}

export async function addPermissionRule() {
  const tool = ruleToolInput.value.trim();
  const match = ruleMatchInput.value.trim();
  const decision = ruleDecisionSelect.value;
  if (!tool || !match) return;
  try {
    await apiClient.addPermissionRule(tool, match, decision);
    app.permissionRules[tool] = app.permissionRules[tool] || [];
    app.permissionRules[tool].push({ match, decision });
    ruleToolInput.value = '';
    ruleMatchInput.value = '';
    renderPermissionRulesList();
    renderPermissionStatus();
  } catch (err) {
    permissionSettingsNote.textContent = `Failed to add rule: ${String(err)}`;
  }
}

async function removePermissionRule(tool, rule) {
  try {
    await apiClient.removePermissionRule(tool, rule.match, rule.decision);
    app.permissionRules[tool] = (app.permissionRules[tool] || []).filter(r => !(r.match === rule.match && r.decision === rule.decision));
    if (app.permissionRules[tool].length === 0) delete app.permissionRules[tool];
    renderPermissionRulesList();
    renderPermissionStatus();
  } catch (err) {
    permissionSettingsNote.textContent = `Failed to remove rule: ${String(err)}`;
  }
}

// ---- Permission request modal (SSE-driven; see events.js) ----

// resolvePermission answers a pending permission request. scope is 'once',
// 'session' (don't ask again this session), or 'always' (don't ask again
// ever — the daemon writes a matching rule to config.json). The policy
// change itself happens server-side; this only reports what the user chose.
export async function resolvePermission(allow, scope) {
  if (!session.pendingPermissionID) return;
  const id = session.pendingPermissionID;
  session.pendingPermissionID = null;
  permissionRequest.close();
  // Answered, so the light stops being yours: back to blinking green if
  // the turn it interrupted is still going, steady green if it is not.
  renderCommDot();
  // Unlocked here, on the answer in hand, rather than waiting for the
  // permission.resolved event to come back. The lock exists to stop a
  // message being typed into an unanswered question; the question has
  // just been answered. If the event stream has quietly died — which the
  // heartbeat makes rare but does not rule out — waiting for it left the
  // composer disabled under "Resolve the permission request above" with
  // no request on screen and nothing to click, while the turn carried on
  // server-side. cancelTurn already works this way.
  setInputLocked(false);
  try {
    await apiClient.resolvePermissionRequest(session.sessionID, id, allow, scope);
  } catch (err) {
    appendError(`failed to respond to permission request: ${err}`);
  }
}

// ---- Workspace modal / picker ----

// Clicking the workspace always opens the modal, which offers both ways
// in: a box to type or paste a path into, and a Browse button that opens
// the OS folder picker when this daemon has one.
//
// It used to go straight to the picker whenever the picker existed, which
// meant the desktop build — the one build that has a picker — was the one
// build with no way to type a path at all. That is the wrong way round: a
// path is often something you already have (copied from a terminal, from
// an editor's title bar, from a bug report), and clicking through a folder
// tree to reach somewhere you could have pasted in full is the slow way to
// do it. A browser has no picker to offer either way: neither <input
// webkitdirectory> nor showDirectoryPicker() hands back a real filesystem
// path, and a dialog opened by the *daemon* only makes sense when the
// daemon is on the same machine as the screen.
export function openWorkspacePicker() {
  openWorkspaceModal();
}

// revealWorkspace asks the daemon to open the current workspace in the
// machine's own file manager — an Explorer or Finder window on the folder
// the agent is working in. Only offered when the daemon is on the machine
// with the screen (see can_reveal); anywhere else the window would open in
// front of nobody.
export async function revealWorkspace() {
  try {
    await apiClient.revealWorkspace(session.sessionID);
  } catch (err) {
    appendError(`could not open a window on ${app.workspacePath}: ${err}`);
  }
}

// browsing guards against opening a second folder dialog on top of the
// first.
//
// The OS dialog is modal to the *daemon*, not to this page — the request
// that opens it simply does not answer until someone picks or cancels,
// and the page carries on handling clicks the whole time. So a stray
// second click on the workspace button put up a second "Browse for
// Folder" with the first still waiting behind it, and the person had to
// answer both. Worse, both then resolve: whichever is answered last wins
// and silently overwrites the first choice.
//
// The button is disabled as well as the call being dropped, so the state
// is visible rather than a click that does nothing for no stated reason.
let browsing = false;

// browseWorkspace opens the OS folder picker and puts what was chosen in
// the modal's path box, rather than applying it straight away — so the
// picker is a way to fill the field, and Save is still the one action that
// moves the workspace. Picking the wrong folder is then a correction, not
// a switch that has to be undone.
export async function browseWorkspace() {
  if (browsing) return;
  browsing = true;
  workspaceBrowseBtn.disabled = true;
  try {
    let picked;
    try {
      picked = await apiClient.browseWorkspace(workspaceInput.value || app.workspacePath);
    } catch (err) {
      // The picker itself failed (not a cancel). The typed path is still
      // there and still works, so this is a note, not a dead end.
      workspaceNote.textContent = `The folder picker could not open (${err}). Type or paste a path instead.`;
      workspaceNote.classList.add('err');
      return;
    }
    if (!picked || !picked.path) return; // 204: dialog dismissed, nothing to do
    workspaceInput.value = picked.path;
    workspaceNote.textContent = workspaceNoteDefault;
    workspaceNote.classList.remove('err');
  } finally {
    browsing = false;
    workspaceBrowseBtn.disabled = false;
  }
}

// switchWorkspace is the one place that moves the daemon's working
// directory. Both entry points — the OS folder picker and the typed-path
// modal — go through it, so the local state update can't drift between
// them; they differ only in how a failure is reported, which is why the
// error comes back rather than being handled here.
async function switchWorkspace(path) {
  try {
    const w = await apiClient.setWorkspace(path, session.sessionID);
    app.workspacePath = w.path;
    renderWorkspace();
    // The daemon has just recorded the move on this session; mirror it in
    // the cached listing the left panel renders from, which is otherwise
    // only refreshed on a rename or a session switch — so the panel went
    // on naming the directory the session was created in.
    const current = (app.sessions || []).find(s => s.id === session.sessionID);
    if (current) {
      current.workspace = w.path;
      renderSessionList();
    }
    return { path: w.path };
  } catch (err) {
    return { err };
  }
}

export function openWorkspaceModal() {
  workspaceInput.value = app.workspacePath;
  workspaceNote.textContent = workspaceNoteDefault;
  workspaceNote.classList.remove('err');
  // Hidden rather than disabled where there is no picker to open: a
  // browser talking to a daemon on another machine has nothing behind
  // this button, and an inert control invites clicking.
  workspaceBrowseBtn.style.display = app.canBrowseWorkspace ? '' : 'none';
  blockingSessions = [];
  workspaceStopBusyBtn.style.display = 'none';
  workspace.open();
  workspaceInput.focus();
  workspaceInput.select();
}

export function closeWorkspaceModal() {
  workspace.close();
}

// The sessions the daemon named as holding up the last refused switch.
//
// The working directory is one process-wide thing, so a turn in *any*
// session blocks a move — including one nobody is watching, and including
// one parked forever on a permission request nobody answered. Being told
// "a turn is in progress" and left to go and find it is what makes this
// read as "I often just can't change the workspace". Keeping the ids means
// the way out can be a button.
let blockingSessions = [];

export async function saveWorkspace() {
  const path = workspaceInput.value.trim();
  if (!path || path === app.workspacePath) {
    workspace.close();
    return;
  }
  const res = await switchWorkspace(path);
  if (res.err) {
    // Stays open with the error inline, so a typo can be corrected without
    // retyping the whole path.
    workspaceNote.textContent = String(res.err.message || res.err);
    workspaceNote.classList.add('err');
    blockingSessions = (res.err.data && Array.isArray(res.err.data.busy)) ? res.err.data.busy : [];
    workspaceStopBusyBtn.style.display = blockingSessions.length > 0 ? '' : 'none';
    return;
  }
  workspace.close();
}

// stopBlockingTurns cancels the turns the daemon named, then tries the
// switch again.
//
// Cancelling is the honest thing to offer here rather than switching
// anyway: the guard exists because a tool call mid-execution would
// otherwise find the ground moved under it. So the turns really do have to
// end first — this only saves having to find them.
export async function stopBlockingTurns() {
  const ids = blockingSessions.slice();
  if (ids.length === 0) return;
  workspaceStopBusyBtn.disabled = true;
  try {
    for (const id of ids) {
      try {
        await apiClient.cancelSessionTurn(id);
      } catch (err) {
        workspaceNote.textContent = `could not stop the turn in ${id}: ${err}`;
        workspaceNote.classList.add('err');
        return;
      }
    }
    workspaceStopBusyBtn.style.display = 'none';
    blockingSessions = [];
    await saveWorkspace();
  } finally {
    workspaceStopBusyBtn.disabled = false;
  }
}

// Every modal, so Tab does not cycle agents underneath an open one. A
// new modal has to be added here — the list is the whole mechanism, and
// forgetting it is silent.
export function anyModalOpen() {
  return permissionRequest.isOpen || permissionSettings.isOpen || delegate.isOpen ||
    workspace.isOpen || settings.isOpen || taskView.isOpen;
}
