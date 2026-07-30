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

const workspaceNoteDefault = 'Changing this restarts relative-path resolution for every tool from the new directory. Refused while a turn is in progress.';

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
  delegateModal.classList.add('open');
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
  if (delegateModal.classList.contains('open')) renderDelegatePanel();
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
  delegateModal.classList.remove('open');
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
  permissionSettingsModal.classList.add('open');
}

export function closePermissionSettings() {
  permissionSettingsModal.classList.remove('open');
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
  modalEl.classList.remove('open');
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

export async function browseWorkspace() {
  let picked;
  try {
    picked = await apiClient.browseWorkspace(app.workspacePath);
  } catch (err) {
    // The picker itself failed (not a cancel) — fall back to typing, rather
    // than leaving the click doing nothing at all.
    appendError(`could not open the folder picker: ${err}`);
    openWorkspaceModal();
    return;
  }
  if (!picked || !picked.path) return; // 204: dialog dismissed, nothing to do
  await applyWorkspace(picked.path);
}

// applyWorkspace switches the daemon's working directory and reports the
// outcome in the transcript, since this changes where every later tool call
// and bash command resolves from — too consequential to happen silently.
// Returns whether it took effect.
export async function applyWorkspace(path) {
  if (path === app.workspacePath) return true;
  try {
    const w = await apiClient.setWorkspace(path);
    app.workspacePath = w.path;
    renderWorkspace();
    appendTool(`[workspace] ${w.path}`);
    return true;
  } catch (err) {
    appendError(`could not switch the workspace to ${path}: ${err}`);
    return false;
  }
}

export function openWorkspaceModal() {
  workspaceInput.value = app.workspacePath;
  workspaceNote.textContent = workspaceNoteDefault;
  workspaceNote.classList.remove('err');
  workspaceModal.classList.add('open');
  workspaceInput.focus();
}

export function closeWorkspaceModal() {
  workspaceModal.classList.remove('open');
}

export async function saveWorkspace() {
  const path = workspaceInput.value.trim();
  if (!path || path === app.workspacePath) {
    workspaceModal.classList.remove('open');
    return;
  }
  try {
    const w = await apiClient.setWorkspace(path);
    app.workspacePath = w.path;
    renderWorkspace();
    workspaceModal.classList.remove('open');
  } catch (err) {
    // Stays open with the error inline, so a typo can be corrected without
    // retyping the whole path.
    workspaceNote.textContent = String(err);
    workspaceNote.classList.add('err');
  }
}

export function anyModalOpen() {
  return modalEl.classList.contains('open')
    || permissionSettingsModal.classList.contains('open')
    || delegateModal.classList.contains('open')
    || workspaceModal.classList.contains('open');
}
