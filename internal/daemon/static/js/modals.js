import {
  modalEl,
  delegateModal, delegateEnabledCheckbox, delegateAgentSelect, delegateMatchListEl,
  delegateMatchInput, delegateNote,
  permissionSettingsModal, skipPermissionsCheckbox, permissionRulesListEl,
  ruleToolInput, ruleMatchInput, ruleDecisionSelect,
  permissionSettingsNote,
  workspaceModal, workspaceInput, workspaceNote, workspaceBtn,
} from './dom.js';
import { app, session } from './state.js';
import * as apiClient from './api.js';
import { appendError, appendTool } from './transcript.js';
import { renderPermissionStatus, renderAutoDelegate, renderWorkspace } from './render.js';
import { Modal } from './modal.js';
// Circular with sessions.js, which imports applyWorkspace from here — safe
// for the same reason as the events.js/modals.js pair: both references are
// only ever called from a function body at runtime, never while the module
// is being evaluated.
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

export function openPermissionSettings() {
  skipPermissionsCheckbox.checked = app.skipPermissions;
  skipPermissionsCheckbox.disabled = !app.canEditPermissions;
  ruleToolInput.disabled = ruleMatchInput.disabled = ruleDecisionSelect.disabled = !app.canEditPermissions;
  permissionSettingsNote.textContent = app.canEditPermissions
    ? 'Changes apply immediately and are written to config.json.'
    : 'No config.json path is available in this run, so settings are read-only.';
  renderPermissionRulesList();
  permissionSettings.open();
}

export function closePermissionSettings() {
  permissionSettings.close();
}

export async function toggleSkipPermissions(enabled) {
  try {
    await apiClient.setSkipPermissions(enabled);
    app.skipPermissions = enabled;
    renderPermissionStatus();
  } catch (err) {
    skipPermissionsCheckbox.checked = app.skipPermissions; // revert on failure
    permissionSettingsNote.textContent = `Failed to change: ${String(err)}`;
  }
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
  try {
    await apiClient.resolvePermissionRequest(session.sessionID, id, allow, scope);
  } catch (err) {
    appendError(`failed to respond to permission request: ${err}`);
  }
}

// ---- Workspace modal / picker ----

// The desktop window can open the OS folder picker, so clicking the
// workspace goes straight to it rather than to a box you have to type an
// absolute path into. A browser can't: neither <input webkitdirectory> nor
// showDirectoryPicker() hands back a real filesystem path, and asking the
// *daemon* to open a dialog only makes sense when it's on the same machine
// as the screen — so that case keeps the typed-path modal.
export function openWorkspacePicker() {
  if (app.canBrowseWorkspace) browseWorkspace();
  else openWorkspaceModal();
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

export async function browseWorkspace() {
  if (browsing) return;
  browsing = true;
  workspaceBtn.disabled = true;
  try {
    let picked;
    try {
      picked = await apiClient.browseWorkspace(app.workspacePath);
    } catch (err) {
      // The picker itself failed (not a cancel) — fall back to typing,
      // rather than leaving the click doing nothing at all.
      appendError(`could not open the folder picker: ${err}`);
      openWorkspaceModal();
      return;
    }
    if (!picked || !picked.path) return; // 204: dialog dismissed, nothing to do
    await applyWorkspace(picked.path);
  } finally {
    browsing = false;
    workspaceBtn.disabled = false;
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

// applyWorkspace switches the workspace and reports the outcome in the
// transcript, since this changes where every later tool call and bash
// command resolves from — too consequential to happen silently. Returns
// whether it took effect.
export async function applyWorkspace(path) {
  if (path === app.workspacePath) return true;
  const res = await switchWorkspace(path);
  if (res.err) {
    appendError(`could not switch the workspace to ${path}: ${res.err}`);
    return false;
  }
  appendTool(`[workspace] ${res.path}`);
  return true;
}

export function openWorkspaceModal() {
  workspaceInput.value = app.workspacePath;
  workspaceNote.textContent = workspaceNoteDefault;
  workspaceNote.classList.remove('err');
  workspace.open();
  workspaceInput.focus();
}

export function closeWorkspaceModal() {
  workspace.close();
}

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
    workspaceNote.textContent = String(res.err);
    workspaceNote.classList.add('err');
    return;
  }
  workspace.close();
}

export function anyModalOpen() {
  return permissionRequest.isOpen || permissionSettings.isOpen || delegate.isOpen || workspace.isOpen;
}
