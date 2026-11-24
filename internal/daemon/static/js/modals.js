import {
  modalEl,
  permissionTextEl, permissionAllowAlwaysBtn, permissionAllowSessionBtn,
  permissionOutsideEl, permissionAllowDirBtn, permissionAllowOutsideBtn,
  delegateModal, delegateEnabledCheckbox, delegateAgentSelect, delegateMatchListEl,
  delegateMatchInput, delegateNote, delegateWarn,
  permissionSettingsModal, skipPermissionsCheckbox, skipToolsCheckbox,
  readOutsideCheckbox, writeOutsideCheckbox, permissionScopeNote,
  permissionWorkspaceNote, rememberedOutsideEl, permissionRulesListEl,
  ruleToolInput, ruleMatchInput, ruleDecisionSelect,
  permissionRulesWarn, permissionSettingsNote,
  workspaceModal, workspaceInput, workspaceNote, workspaceBrowseBtn, workspaceStopBusyBtn,
  effortModal, effortModelEl, effortLevelsEl, effortNoteEl, effortDefaultBtn, effortCloseBtn,
} from './dom.js';
import { app, session } from './state.js';
import * as apiClient from './api.js';
import { appendError } from './transcript.js';
import { setInputLocked, renderCommDot } from './composer.js';
import { renderPermissionStatus, renderAutoDelegate, renderWorkspace, renderEffort } from './render.js';
import { Modal } from './modal.js';
import { usageView } from './usage.js';
import { settings, unsavedWarning } from './settings.js';
import { taskView } from './taskview.js';
import { anyPromptOpen } from './prompt.js';
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
export const effort = new Modal(effortModal);

// ---- Reasoning effort ----
//
// The levels are not written into the page. Which ones exist is a
// property of the model this conversation is on — Anthropic's newest
// families have one switch where muse has four steps — so the daemon
// sends the list with the answer and this draws whatever it is given.
// A page with its own list offers steps that do nothing on most models,
// and goes stale the next time a family is added.

export function applyEffort(view) {
  session.effort = view || null;
  renderEffort();
  if (effort.isOpen) renderEffortModal();
}

// loadEffort fetches it for the open conversation. Called on a session
// switch, because the answer belongs to the conversation and to the
// model it is on.
export async function loadEffort(sessionID) {
  if (!sessionID) return;
  try {
    const view = await apiClient.getEffort(sessionID);
    // Two switches in quick succession race, and whichever reply lands
    // last wins; without this the pill can end up describing a
    // conversation you have already left.
    if (sessionID !== session.sessionID) return;
    applyEffort(view);
  } catch {
    // A daemon that cannot answer leaves the last answer standing.
  }
}

function renderEffortModal() {
  const view = session.effort;
  effortLevelsEl.innerHTML = '';
  if (!view) {
    effortModelEl.textContent = 'no conversation open';
    effortNoteEl.textContent = '';
    return;
  }
  effortModelEl.textContent = view.model
    ? `${view.model} — ${view.agent}`
    : 'this conversation';
  for (const level of view.levels || []) {
    const btn = document.createElement('button');
    btn.textContent = level;
    btn.className = 'effort-level' + (level === view.level ? ' current' : '');
    btn.addEventListener('click', () => chooseEffort(level));
    effortLevelsEl.appendChild(btn);
  }
  // What the level actually reaches on this model. The whole reason the
  // control exists is that the same word means different things on
  // different wires, and saying so is cheaper than being asked.
  effortNoteEl.textContent = view.note || '';
  effortDefaultBtn.disabled = view.source !== 'session';
}

async function chooseEffort(level) {
  if (!session.sessionID) return;
  try {
    applyEffort(await apiClient.setEffort(session.sessionID, level));
  } catch (err) {
    appendError(`failed to set the effort: ${err}`);
  }
}

export function openEffort() {
  renderEffortModal();
  effort.open();
}

effortDefaultBtn.addEventListener('click', () => chooseEffort(''));
effortCloseBtn.addEventListener('click', () => effort.close());

// What every switch says under it for a change the daemon applied and
// could not write to config.json.
const NOT_SAVED = 'Applied, but not saved to config.json, so it lasts only until the daemon restarts';

// serial runs one change at a time, in the order they were made: the
// next is not sent until the one before it has been answered and drawn.
//
// The delegation panel and the rules list do not hold their controls
// while a request is out, so a second change used to be sent before the
// first was answered. The daemon applied them in order, and the answers
// could arrive in the other one. The page then drew the older value last
// (the agent chosen first, the switch as it was first set), listed rules
// in an order the daemon does not hold them in, lost a rule that was
// removed and added again, and let the answer to an earlier save decide
// the warning about a later one. One at a time, the answers cannot cross.
function serial() {
  let last = Promise.resolve();
  return (change) => {
    const done = last.then(change);
    // A change that throws has reported it to whoever made it, and the
    // ones behind it still run.
    last = done.catch(() => {});
    return done;
  };
}

// ---- Auto-delegation modal ----

// saveAutoDelegate posts only the fields given, so the panel's three
// controls each change one thing without restating the others. Every
// change applies to the running loop immediately and is written to
// config.json — see POST /api/settings/auto-delegate.
//
// The daemon answers in two parts, as it does for Smart Agent: what it
// now holds, which is what the panel has to show, and whether config.json
// was written. A request that fails is one it refused before applying
// anything: an agent it does not know, or fewer than two to choose from.
//
// Only the fields this request moved are taken from the answer. The
// answer carries the whole setting, and another window can have moved
// the rest of it since this page last read it.
//
// A patch can be given as a function, which is called when its turn
// comes. The patterns are sent as a whole list, so a pattern added while
// another change was still out has to be added to the list as that
// change left it, not as it was when the button was pressed: built
// early, the second of two quick additions sent a list without the
// first, and the daemon dropped it. A function that answers null has
// found nothing left to change.
//
// The switch and the target (the agent and the patterns) are written to
// the file separately, so each has its own warning: saving one says
// nothing about the other, and a warning goes when its own half is saved.
const delegationChanges = serial();

// What the daemon said when it refused the last change that was sent,
// and '' once a change has been applied since. The line under the panel
// is drawn from it (renderDelegatePanel), where it used to be read back
// from whether the line carried the error mark. A press that sent
// nothing then took the mark and left the words: a pattern the list
// already had, added after a refusal. Found by review.
let delegateRefusal = '';

export function saveAutoDelegate(patch) {
  return delegationChanges(() => {
    const now = typeof patch === 'function' ? patch() : patch;
    return now ? sendAutoDelegate(now) : true;
  });
}

async function sendAutoDelegate(patch) {
  let res;
  try {
    res = await apiClient.setAutoDelegate(patch);
  } catch (err) {
    delegateRefusal = String(err);
    // Nothing was applied, so the control that was moved goes back to
    // what the daemon has.
    renderDelegatePanel();
    return false;
  }
  const held = res || {};
  if (typeof patch.enabled === 'boolean') {
    app.autoDelegate = typeof held.auto_delegate === 'boolean' ? held.auto_delegate : patch.enabled;
  }
  if (typeof patch.agent === 'string') {
    app.autoDelegateAgent = typeof held.auto_delegate_agent === 'string' ? held.auto_delegate_agent : patch.agent;
  }
  if (Array.isArray(patch.match)) {
    app.autoDelegateMatch = (Array.isArray(held.auto_delegate_match) ? held.auto_delegate_match : patch.match).slice();
  }

  // '' for a save that reached the file, which takes the warning away.
  const reason = held.persisted === false ? held.error || 'unknown error' : '';
  if (typeof patch.enabled === 'boolean') {
    unsavedWarning('auto_delegate', reason && `The switch: ${NOT_SAVED}: ${reason}`);
  }
  if (typeof patch.agent === 'string' || Array.isArray(patch.match)) {
    unsavedWarning('auto_delegate_target', reason && `The agent and patterns: ${NOT_SAVED}: ${reason}`);
  }
  // A refusal was about an earlier request, and this one was applied.
  delegateRefusal = '';
  renderAutoDelegate();
  renderDelegatePanel();
  return true;
}

export function openAutoDelegateSettings() {
  delegateRefusal = '';
  renderDelegatePanel();
  delegate.open();
}

export function renderDelegatePanel() {
  delegateEnabledCheckbox.checked = app.autoDelegate;
  // Kept through every draw: opening the panel again, another window's
  // change, the daemon's announcement of this one.
  const warning = [unsavedWarning('auto_delegate'), unsavedWarning('auto_delegate_target')].filter(Boolean).join(' ');
  delegateWarn.textContent = warning;
  delegateWarn.hidden = !warning;

  // Only agents other than the one answering can be delegation targets:
  // delegating to the running agent would recurse, and the daemon refuses
  // it at turn time anyway, so it isn't offered here.
  delegateAgentSelect.innerHTML = '';
  const none = document.createElement('option');
  none.value = '';
  none.textContent = '(no agent chosen — nothing is delegated)';
  delegateAgentSelect.appendChild(none);
  // Only agents declared in config.json: the daemon refuses a built-in
  // specialist as the target, and the list used to offer all six while
  // orchestration was on, each one a choice that failed when made.
  for (const a of app.agents) {
    if (a.builtin) continue;
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
      // Nothing is sent for a pattern an earlier removal already took.
      saveAutoDelegate(() => (app.autoDelegateMatch.includes(pattern)
        ? { match: app.autoDelegateMatch.filter(p => p !== pattern) }
        : null));
    });
    row.appendChild(removeBtn);
    delegateMatchListEl.appendChild(row);
  }

  delegateNote.classList.toggle('err', !!delegateRefusal);
  if (delegateRefusal) {
    delegateNote.textContent = delegateRefusal;
  } else {
    if (app.autoDelegate && !app.autoDelegateAgent) {
      delegateNote.textContent = 'On, but no agent is chosen, so nothing is delegated.';
    } else if (app.autoDelegate && app.autoDelegateMatch.length === 0) {
      delegateNote.textContent = 'On, but no pattern matches anything yet, so nothing is delegated.';
    } else {
      // Not said beside a warning that one was not written.
      delegateNote.textContent = warning
        ? 'Changes apply to the next prompt.'
        : 'Changes apply to the next prompt and are written to config.json.';
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
  // Whether the list already has it is asked when this change's turn
  // comes, below. Asked here, a pattern added again while its own
  // removal was still out was found in the list and dropped, and the
  // removal then took it. Found by review.
  if (!pattern) return;
  saveAutoDelegate(() => (app.autoDelegateMatch.includes(pattern)
    ? null
    : { match: app.autoDelegateMatch.concat([pattern]) })).then(ok => {
    // Only what was added is cleared: the next pattern may already be
    // typed by the time this one is answered.
    if (ok && delegateMatchInput.value.trim() === pattern) delegateMatchInput.value = '';
  });
}

export function closeDelegateModal() {
  delegate.close();
}

// ---- Permission settings modal ----

// Rules are written to config.json one at a time, so a rule the daemon
// applied and could not write is remembered by name. A later rule that
// is saved says nothing about an earlier one that was not, which is why
// this is a list and not the one warning a switch keeps.
//
// An entry is a rule the daemon and the file disagree about: in force
// and not in the file, or in the file and no longer in force. It goes
// when they agree again, which is two ways. The same rule is added or
// removed and that is saved. Or the change is taken back: a rule added
// and not saved, then removed, is in neither, whether or not the removal
// was saved. What the file holds is what was listed before the first
// change that was not saved, since a rule with no entry here is one the
// two agree about.
//
// A rule is one of two kinds with the same text: a pattern, which is
// what this window adds, and an exact command, which "always allow" at
// a prompt writes and the list marks "(exact)". The daemon keeps them
// apart. It adds a pattern beside an exact rule of the same text, and it
// removes both when asked to remove that text. So both are followed
// here, for what is in force and for what the file holds: with only the
// text compared, a pattern added beside an exact rule was not listed and
// not warned about, and an exact rule in the file replaced by a pattern
// in force read as nothing having changed.
const unsavedRules = new Map();

// ruleKinds says which kinds of one rule the list holds. The list is
// what the daemon has in force, since every change here is applied to it
// as the daemon applies it.
function ruleKinds(tool, match, decision) {
  const same = (app.permissionRules[tool] || []).filter(r => r.match === match && r.decision === decision);
  return { pattern: same.some(r => !r.literal), exact: same.some(r => !!r.literal) };
}

// applyRuleChange does to the list what the daemon did to its own: a
// rule added is one more pattern unless that pattern is there already,
// and a rule removed takes every rule of that text, exact ones included.
// It then records whether config.json is believed to hold the same.
function applyRuleChange(change, tool, match, decision, res) {
  const before = ruleKinds(tool, match, decision);
  if (change === 'added') {
    if (!before.pattern) (app.permissionRules[tool] = app.permissionRules[tool] || []).push({ match, decision });
  } else {
    const kept = (app.permissionRules[tool] || []).filter(r => !(r.match === match && r.decision === decision));
    if (kept.length > 0) app.permissionRules[tool] = kept;
    else delete app.permissionRules[tool];
  }
  const inForce = ruleKinds(tool, match, decision);

  const key = JSON.stringify([tool, match, decision]);
  const entry = unsavedRules.get(key);
  const saved = !(res && res.persisted === false);
  let inFile = entry ? entry.inFile : before;
  // A save that worked did to the file what was done to the list.
  if (saved) inFile = change === 'added' ? { pattern: true, exact: inFile.exact } : { pattern: false, exact: false };

  const name = (exact) => `${tool}: "${match}"${exact ? ' (exact)' : ''} -> ${decision}`;
  const differs = [];
  if (inForce.pattern !== inFile.pattern) differs.push(`${inForce.pattern ? 'added' : 'removed'} ${name(false)}`);
  if (inForce.exact !== inFile.exact) differs.push(`${inForce.exact ? 'added' : 'removed'} ${name(true)}`);
  unsavedRules.delete(key);
  if (differs.length > 0) {
    const reason = (!saved && res.error) || (entry && entry.reason) || 'unknown error';
    unsavedRules.set(key, { inFile, text: differs.join('; '), reason });
  }
}

function renderUnsavedRules() {
  const entries = Array.from(unsavedRules.values());
  permissionRulesWarn.hidden = entries.length === 0;
  permissionRulesWarn.textContent = entries.length === 0 ? '' :
    `${NOT_SAVED}: ${entries[entries.length - 1].reason} Not saved: ${entries.map((e) => e.text).join('; ')}.`;
}

// The line under the rules. Three things are said on it, and each used
// to be written straight onto it, over whatever was there:
//
//   * one of the four switches above the rules could not be changed. Its
//     box is left where it was put, so this is the only sign the daemon
//     does not have that value. It stays until that switch is changed, or
//     the window is opened again and the boxes are drawn from what the
//     daemon has. Each switch has its own: a second that could not be
//     changed used to be written over the first;
//   * a rule the daemon refused. It is about one request, and goes with
//     the next rule change that is applied;
//   * with neither, how rules are kept.
//
// So they are kept apart and the line is drawn from them. Written over
// each other, a rule that was added took away the only sign of a switch
// that was not changed (found by review, in a fix for the opposite: a
// refused rule still said after a later one was applied).
const switchFailures = new Map();
let ruleRefusal = '';

function drawPermissionNote() {
  permissionSettingsNote.classList.toggle('err', switchFailures.size > 0);
  const said = [...switchFailures.values(), ruleRefusal].filter(Boolean).join(' ');
  if (said) {
    permissionSettingsNote.textContent = said;
  } else if (!app.canEditPermissions) {
    permissionSettingsNote.textContent = 'No config.json path is available in this run, so rules are read-only.';
  } else {
    // Not said beside a warning that one was not written.
    permissionSettingsNote.textContent = unsavedRules.size > 0
      ? 'Rules apply immediately. The four switches above are saved with the conversation.'
      : 'Rules apply immediately and are written to config.json. The four switches above are saved with the conversation.';
  }
}

export function renderPermissionRulesList() {
  renderUnsavedRules();
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
      // "(exact)" for a literal rule, which matches its text and nothing
      // else; the same text without it is a glob.
      text.textContent = `${tool}: "${rule.match}"${rule.literal ? ' (exact)' : ''} -> ${rule.decision}`;
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
    // The daemon has what this box shows again.
    if (switchFailures.delete(name)) drawPermissionNote();
  } catch (err) {
    switchFailures.set(name, `could not change ${name}: ${err.message}`);
    drawPermissionNote();
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
  switchFailures.clear();
  ruleRefusal = '';
  drawPermissionNote();
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

// Rule changes go one at a time, like the delegation panel's, and each
// is applied to the list when it is answered. The answer is in two
// parts: the rule is in force either way, and "persisted" says whether
// config.json has it.
const ruleChanges = serial();

export function addPermissionRule() {
  const tool = ruleToolInput.value.trim();
  const match = ruleMatchInput.value.trim();
  const decision = ruleDecisionSelect.value;
  if (!tool || !match) return undefined;
  return ruleChanges(async () => {
    try {
      const res = await apiClient.addPermissionRule(tool, match, decision);
      applyRuleChange('added', tool, match, decision, res);
      // Only what was added is cleared: the next rule may already be
      // typed by the time this one is answered.
      if (ruleToolInput.value.trim() === tool && ruleMatchInput.value.trim() === match && ruleDecisionSelect.value === decision) {
        ruleToolInput.value = '';
        ruleMatchInput.value = '';
      }
      ruleRefusal = '';
      drawPermissionNote();
      renderPermissionRulesList();
      renderPermissionStatus();
    } catch (err) {
      ruleRefusal = `Failed to add rule: ${String(err)}`;
      drawPermissionNote();
    }
  });
}

function removePermissionRule(tool, rule) {
  return ruleChanges(async () => {
    try {
      const res = await apiClient.removePermissionRule(tool, rule.match, rule.decision);
      applyRuleChange('removed', tool, rule.match, rule.decision, res);
      ruleRefusal = '';
      drawPermissionNote();
      renderPermissionRulesList();
      renderPermissionStatus();
    } catch (err) {
      ruleRefusal = `Failed to remove rule: ${String(err)}`;
      drawPermissionNote();
    }
  });
}

// ---- Permission request modal (SSE-driven; see events.js) ----

// showPermissionRequest puts one request on screen: the light, the text,
// the buttons for its shape, the lock on the composer. waiting names how
// many requests sit behind it, so answering the modal never answers a
// question the reader cannot see coming.
function showPermissionRequest(req, waiting) {
  session.pendingPermission = req;
  session.pendingPermissionID = req.id;
  session.pendingPermissionCanAlways = !!req.can_always;
  // The light says what the modal says. Without this the dot went on
  // blinking green — "working" — behind a dialog that had stopped the
  // work to ask a question.
  renderCommDot();
  let text = `[${req.tool}] ${req.description || '(no description given)'}`;
  if (waiting === 1) text += ' (1 more permission request waiting)';
  else if (waiting > 1) text += ` (${waiting} more permission requests waiting)`;
  permissionTextEl.textContent = text;
  // A boundary question is a different question, so it gets different
  // buttons: a place is answered at one of two sizes, and "always
  // allow" would write a tool rule that outlives the reason for it.
  const outside = req.outside === 'read' || req.outside === 'write' ? req.outside : '';
  permissionOutsideEl.hidden = !outside;
  if (outside) {
    permissionOutsideEl.textContent =
      `This path is outside the project this conversation is working in (${req.workspace || 'unknown'}).`;
    permissionAllowDirBtn.hidden = false;
    permissionAllowDirBtn.textContent = `Allow ${outside} under ${req.outside_dir || 'this directory'}`;
    permissionAllowDirBtn.title = 'for the rest of this session; /'
      + outside + '-outside mem-clear forgets it';
    permissionAllowOutsideBtn.hidden = false;
    permissionAllowOutsideBtn.textContent = `Allow ${outside} anywhere outside`;
    permissionAllowOutsideBtn.title = `turns ${outside}-outside on for this conversation`;
  } else {
    permissionAllowDirBtn.hidden = true;
    permissionAllowOutsideBtn.hidden = true;
  }
  // A question asked every time (a resume of a run that writes) takes
  // allow and deny only; the daemon treats any other answer as once.
  const once = req.once === true;
  const offerAlways = session.pendingPermissionCanAlways && !outside && !once;
  permissionAllowAlwaysBtn.style.display = offerAlways ? '' : 'none';
  permissionAllowSessionBtn.style.display = outside || once ? 'none' : '';
  if (offerAlways) {
    permissionAllowAlwaysBtn.title = `don't ask again — writes "${req.rule}" to config.json`;
  }
  permissionRequest.open();
  setInputLocked(true, 'Resolve the permission request above to continue.');
}

// enqueuePermissionRequest shows a request, or queues it behind the one
// on screen. The screen always holds the oldest unresolved request: a
// newer arrival must not cover an older question whose turn is still
// blocked on it, or answering the second would strand the first with no
// way to reach it again. A repeat of an id already held replaces it,
// keeping the latest description rather than stacking a duplicate.
export function enqueuePermissionRequest(req) {
  if (!req || !req.id) return;
  if (!session.pendingPermissionID) {
    showPermissionRequest(req, session.pendingPermissionQueue.length);
    return;
  }
  if (session.pendingPermissionID === req.id) {
    // Drawn again, not only stored: what is answered is what is shown.
    showPermissionRequest(req, session.pendingPermissionQueue.length);
    return;
  }
  const i = session.pendingPermissionQueue.findIndex((q) => q.id === req.id);
  if (i >= 0) session.pendingPermissionQueue[i] = req;
  else session.pendingPermissionQueue.push(req);
}

// settlePermissionRequest drops the request with this id wherever it is.
// A resolution for the request on screen promotes the oldest queued one;
// a resolution for a queued request just removes it. That second half is
// what keeps replay honest: both halves of every permission live in the
// log, so opening a session replays each old resolution in turn, and
// without it every request answered days ago would pile up as a modal
// behind the live one. An id that matches nothing is ignored, so a stale
// resolution — another session's answer, or a replayed one — never
// closes the modal on a live question.
export function settlePermissionRequest(id) {
  if (!id) return;
  session.answeringPermissions.delete(id);
  if (session.pendingPermissionID === id) {
    const next = session.pendingPermissionQueue.shift();
    if (next) {
      showPermissionRequest(next, session.pendingPermissionQueue.length);
      return;
    }
    closePermissionRequest();
    return;
  }
  const i = session.pendingPermissionQueue.findIndex((q) => q.id === id);
  if (i >= 0) session.pendingPermissionQueue.splice(i, 1);
}

// closePermissionRequest takes the modal down however the question went:
// answered from these buttons, answered in another window, given up on
// unattended, or cancelled along with the turn that asked it.
//
// resolvePermission clears this when the answer came from here. Every
// other way out arrives only as permission.resolved, and it used to close
// the modal and unlock the composer while leaving the id set — so the
// light under the prompt went on saying "waiting for you to answer a
// permission request" with nothing on screen to answer, for the rest
// of the session. Stopping a turn that was asking is the ordinary way
// to land there.
function closePermissionRequest() {
  session.pendingPermission = null;
  session.pendingPermissionID = null;
  session.pendingPermissionCanAlways = false;
  permissionRequest.close();
  setInputLocked(false);
  renderCommDot();
}

// resolvePermission answers a pending permission request. scope is 'once',
// 'session' (don't ask again this session), or 'always' (don't ask again
// ever — the daemon writes a matching rule to config.json). The policy
// change itself happens server-side; this only reports what the user chose.
export async function resolvePermission(allow, scope) {
  if (!session.pendingPermissionID) return;
  const id = session.pendingPermissionID;
  const req = session.pendingPermission || { id, can_always: session.pendingPermissionCanAlways };
  // The next queued request comes up now rather than waiting for this
  // answer's own resolved event to arrive — that event then matches
  // nothing and is ignored, which is exactly what an answer already
  // given should do.
  settlePermissionRequest(id);
  session.answeringPermissions.set(id, req);
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
  if (!session.pendingPermissionID) setInputLocked(false);
  try {
    await apiClient.resolvePermissionRequest(session.sessionID, id, allow, scope);
    session.answeringPermissions.delete(id);
  } catch (err) {
    // Back only while the daemon may still be waiting on it. A question it
    // says is not pending, put back, would lock the session on a question
    // nobody is asking: a replayed log can show one a turn left behind
    // when its daemon stopped.
    if (session.answeringPermissions.delete(id) && !apiClient.isNotPending(err)) {
      enqueuePermissionRequest(req);
    }
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
    workspace.isOpen || settings.isOpen || taskView.isOpen || effort.isOpen || usageView.isOpen ||
    anyPromptOpen();
}
