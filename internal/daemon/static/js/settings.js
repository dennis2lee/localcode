// The settings window: tabs over daemon-wide settings, plus the
// Typography tab owned by this browser (see typography.js).
//
// Everything outside Typography belongs to the daemon rather than to
// this browser, and is shared by every client attached to it. Adding a
// setting later is adding a block to a panel in index.html and a line
// to one of the handlers below; a setting in no panel fails the
// reachability test rather than disappearing.

import {
  settingsModalEl, settingsBtn, settingsCloseBtn, settingsTabs,
  smartAgentCheckbox, smartAgentNoteEl, smartAgentWarnEl,
  browserSettingsEl, browserCheckbox, browserNoteEl, browserWarnEl, browserImportBtn,
  orchestrateCheckbox, orchestrateNoteEl, orchestrateWarnEl,
  modelInvocableCheckbox, modelInvocableNoteEl, modelInvocableWarnEl,
  keepGoingCheckbox, keepGoingWarnEl,
  foldThinkingCheckbox, foldThinkingNoteEl, foldThinkingWarnEl, museNoteEl,
  repeatLimitCheckbox, repeatLimitInput, repeatLimitWarnEl,
  updateCheckBtn, updateInstallBtn, updateNoteEl,
} from './dom.js';
import { Modal } from './modal.js';
import { askConfirm } from './prompt.js';
import { app } from './state.js';
import * as apiClient from './api.js';
import { wireTypography, renderTypography } from './typography.js';
import { inGUI, renderBrowserPanel } from './browser.js';
import { renderSSHPanel } from './ssh.js';

export const settings = new Modal(settingsModalEl);

// The tabs. One panel visible at a time, chosen by name; the first tab
// is where the window always opens, so a choice made last time never
// leaks into the next opening.
let activeSettingsTab = settingsTabs.length ? settingsTabs[0].name : '';

export function selectSettingsTab(name, focus) {
  const known = settingsTabs.some((t) => t.name === name);
  if (!known) return;
  activeSettingsTab = name;
  for (const t of settingsTabs) {
    const on = t.name === name;
    if (t.tab) {
      t.tab.setAttribute('aria-selected', on ? 'true' : 'false');
      t.tab.tabIndex = on ? 0 : -1;
    }
    if (t.panel) t.panel.hidden = !on;
  }
  if (focus) {
    const current = settingsTabs.find((t) => t.name === name);
    if (current && current.tab) current.tab.focus();
  }
}

// moveSettingsTab steps between tabs with wrapping, for the arrow keys.
// The selection follows the focus: one keypress both moves and shows.
function moveSettingsTab(from, dir) {
  const i = settingsTabs.findIndex((t) => t.name === from);
  if (i < 0) return;
  const next = settingsTabs[(i + dir + settingsTabs.length) % settingsTabs.length];
  selectSettingsTab(next.name, true);
}

export function openSettings() {
  // Cleared rather than checked: opening the panel is not asking GitHub
  // anything, and a stale answer from ten minutes ago would look like one.
  updateNoteEl.textContent = '';
  updateInstallBtn.hidden = true;
  // The window always opens on the first tab: the tab is navigation,
  // not state, and reopening where it was left is how a setting ends
  // up changed in a panel nobody watched open.
  renderTypography();
  selectSettingsTab(settingsTabs.length ? settingsTabs[0].name : '');
  renderDaemonSettings();
  settings.open();
}

// renderDaemonSettings draws every control the daemon holds the value of,
// from what the page knows of it now.
//
// One list, used by both callers, because a control left out of it is a
// control that shows whatever the markup starts with. The repeat guard
// was left out of the window's opening: its box was drawn only when the
// number was changed, so after the page had been loaded again (a restart
// of the desktop window, a reload) the Turns tab showed the box unticked
// and the number empty whatever the daemon was holding, which reads as a
// setting that was not kept. test/webui/settings_restart.test.js opens
// the window against every one of them.
function renderDaemonSettings() {
  renderSmartAgent();
  renderBrowserSetting();
  renderSSHSetting();
  renderOrchestrate();
  renderModelInvocable();
  renderRepeatLimit();
  renderKeepGoing();
  renderMuseNote();
  renderFoldThinking();
}

// refreshSettingsIfOpen draws them again when the page's first read of
// the daemon's settings lands. The window can be opened before that, in
// the first moment after the page appears, and it then shows the values
// the page starts with: every switch off. Nothing else would correct it,
// because the other redraws wait for a setting to change.
export function refreshSettingsIfOpen() {
  if (settings.isOpen) renderDaemonSettings();
}

// The Muse tab.
//
// Its note answers the question somebody on another model opens it with:
// whether any of it is theirs. The daemon reports which profiles run a
// muse model, by the same rule the switches are applied with.
function renderMuseNote() {
  const names = app.museProfiles || [];
  museNoteEl.textContent = names.length
    ? `These switches apply only to models whose id contains "muse". Profiles on one: ${names.join(', ')}.`
    : 'These switches apply only to models whose id contains "muse", and no profile in this config runs one, so they change nothing until one does.';
}

// The reasoning block.
//
// The note says what the switch does in the state it is in, and says so
// when show_thinking is hiding reasoning altogether: the box can be on
// and still change nothing on screen, and that should be read here
// rather than worked out from an absence.
//
// The warning is kept here rather than passed in, so the redraw that a
// settings.changed brings (which arrives for this very change) does not
// erase a save that failed.
let foldThinkingWarning = '';

function renderFoldThinking() {
  foldThinkingCheckbox.checked = !!app.foldThinking;
  foldThinkingWarnEl.textContent = foldThinkingWarning;
  foldThinkingWarnEl.hidden = !foldThinkingWarning;
  if (!app.showThinking) {
    foldThinkingNoteEl.textContent = 'Reasoning is not drawn at all while show_thinking is off. /thinking on draws it again.';
    return;
  }
  foldThinkingNoteEl.textContent = app.foldThinking
    ? 'On. Click the folded line to read the reasoning again; Ctrl+O does the same in the TUI.'
    : 'Off. The reasoning is drawn as it arrives, with no label, and stays open above the answer.';
}

export function refreshFoldThinkingIfOpen() {
  if (settings.isOpen) renderFoldThinking();
}

// The daemon answers with what it did in two parts, as it does for
// Smart Agent: "applied" is the state the box has to show, "persisted"
// only whether config.json was written.
async function toggleFoldThinking() {
  const enabled = foldThinkingCheckbox.checked;
  foldThinkingCheckbox.disabled = true;
  try {
    const res = await apiClient.setFoldThinking(enabled);
    app.foldThinking = res && 'fold_thinking' in res ? !!res.fold_thinking : enabled;
    foldThinkingWarning = res && res.persisted === false
      ? `Applied, but not saved to config.json, so it lasts only until the daemon restarts: ${res.error || 'unknown error'}`
      : '';
  } catch (err) {
    // Nothing was applied. The box goes back to what the daemon has.
    foldThinkingWarning = `Not changed: ${err}`;
  } finally {
    foldThinkingCheckbox.disabled = false;
    renderFoldThinking();
  }
}

// A change the daemon applied and could not write to config.json is told
// under its switch, and stays told until that switch is saved.
//
// Each of the seven draws below is called two ways: with the outcome of a
// save (a warning, or '' for a save that reached the file), and with
// nothing, to draw again because something else changed: another
// setting, another window, the settings arriving. Drawn again with
// nothing, a warning used to go: turn Smart Agent on with a config that
// could not be written, then turn SSH on, and the first warning was gone
// while the switch it was about was still not saved.
//
// The delegation panel keeps its two here as well (modals.js), under
// "auto_delegate" and "auto_delegate_target".
const unsaved = {};

export function unsavedWarning(name, outcome) {
  if (outcome !== undefined) unsaved[name] = outcome || '';
  return unsaved[name] || '';
}

// movedElsewhere forgets the warning under a switch that somebody else
// has since moved: another window, or a command typed at a prompt. The
// warning was about this page's own save of the value it had then, and
// says nothing about the value the switch has now. The names are the
// daemon's own for each setting, as the announcement carries them.
export function movedElsewhere(name) {
  unsaved[name] = '';
}

// Keep going and the repeat guard have one line under them where the
// other switches have two, so a request the daemon refused is said on the
// line the unsaved warning is kept on. The refusal is about one request
// and is drawn once. The warning is about the value the daemon is still
// running, so it is drawn beside the refusal and is still there on the
// next draw.
function underSwitch(name, outcome, refused) {
  const kept = unsavedWarning(name, outcome);
  return refused && kept ? `${refused} ${kept}` : refused || kept;
}

// Keep going.
//
// The tab carries the scope (muse models only); this only moves the box
// and reports a save that did not stick.
function renderKeepGoing(outcome, refused) {
  const warning = underSwitch('keep_going', outcome, refused);
  keepGoingCheckbox.checked = !!app.keepGoing;
  keepGoingWarnEl.textContent = warning;
  keepGoingWarnEl.hidden = !warning;
}

// refreshKeepGoingIfOpen redraws the box when the switch moved somewhere
// else: "/keep-going" typed at any prompt, or another window.
export function refreshKeepGoingIfOpen() {
  if (settings.isOpen) renderKeepGoing();
}

// The daemon answers in two parts, as it does for Smart Agent: "applied"
// is the state the box has to show, "persisted" only whether config.json
// was written. An answer that fails is a change that was not made.
async function toggleKeepGoing() {
  const enabled = keepGoingCheckbox.checked;
  keepGoingCheckbox.disabled = true;
  try {
    const res = await apiClient.setKeepGoing(enabled);
    app.keepGoing = res && 'keep_going' in res ? !!res.keep_going : enabled;
    renderKeepGoing(res && res.persisted === false
      ? `Applied, but not saved to config.json, so it lasts only until the daemon restarts: ${res.error || 'unknown error'}`
      : '');
  } catch (err) {
    // Nothing was applied. The box goes back to what the daemon has.
    renderKeepGoing(undefined, `Not changed: ${err}`);
  } finally {
    keepGoingCheckbox.disabled = false;
  }
}

// The repeat guard.
//
// A checkbox and a number, because "off" and "give it room" are different
// sizes of the same request. The number is only editable while the box
// is checked; unchecking sends zero, checking sends the number.
function renderRepeatLimit(outcome, refused) {
  const warning = underSwitch('repeat_limit', outcome, refused);
  const on = app.repeatLimit > 0;
  repeatLimitCheckbox.checked = on;
  repeatLimitInput.disabled = !on;
  if (on) repeatLimitInput.value = String(app.repeatLimit);
  repeatLimitWarnEl.textContent = warning;
  repeatLimitWarnEl.hidden = !warning;
}

export function refreshRepeatLimitIfOpen() {
  if (settings.isOpen) renderRepeatLimit();
}

// Answered in two parts as well. The one refusal a person can cause from
// here is a number past the daemon's ceiling, which is a 400 and changes
// nothing.
async function applyRepeatLimit() {
  const wanted = repeatLimitCheckbox.checked ? Math.max(1, parseInt(repeatLimitInput.value, 10) || 3) : 0;
  repeatLimitCheckbox.disabled = true;
  repeatLimitInput.disabled = true;
  try {
    const res = await apiClient.setRepeatLimit(wanted);
    app.repeatLimit = res && typeof res.repeat_limit === 'number' ? res.repeat_limit : wanted;
    renderRepeatLimit(res && res.persisted === false
      ? `Applied, but not saved to config.json, so it lasts only until the daemon restarts: ${res.error || 'unknown error'}`
      : '');
  } catch (err) {
    // Nothing was applied. The box and the number go back to what the
    // daemon has.
    renderRepeatLimit(undefined, `Not changed: ${err}`);
  } finally {
    repeatLimitCheckbox.disabled = false;
    repeatLimitInput.disabled = app.repeatLimit === 0;
  }
}

// Smart Agent.
//
// How one agent works: its prompt, its tools, fallback, caching, the turn
// log. It delegates nothing; handing work to other agents is
// orchestration, below. The switch is the whole control, and the note says
// where the turn log is, which is the one thing it adds that a person
// goes looking for.
function renderSmartAgent(outcome) {
  const warning = unsavedWarning('smart_agent', outcome);
  smartAgentCheckbox.checked = !!app.smartAgent;
  smartAgentWarnEl.textContent = warning || '';
  smartAgentWarnEl.hidden = !warning;
  if (!app.smartAgent) {
    smartAgentNoteEl.textContent = 'Off.';
    return;
  }
  smartAgentNoteEl.textContent = 'On. Turn log in ~/.localcode/trace.';
}

// The browser.
//
// Only in the desktop window, which is the only place it can be used: the
// panel that shows the page is drawn there and nowhere else, and the model
// is offered the browser only while that panel is showing.

// importNote says what a copy of the person's profile did.
export function importNote(rep) {
  if (!rep) return '';
  const skipped = rep.skipped && rep.skipped.length
    ? ` Not copied, held by the running browser: ${rep.skipped.join(', ')}. Close Chrome and copy again for those.`
    : '';
  return `Last copied from ${rep.from}.${skipped}`;
}

function renderBrowserSetting(outcome) {
  let warning = unsavedWarning('browser', outcome);
  browserSettingsEl.hidden = !inGUI();
  browserCheckbox.checked = !!app.browser;
  browserImportBtn.hidden = !app.browser;
  if (!warning && !app.browserAvailable) {
    warning = 'No Chrome or Microsoft Edge found on this computer. Install one, or set browser_path in config.json.';
  }
  browserWarnEl.textContent = warning || '';
  browserWarnEl.hidden = !warning;
  const copied = importNote(app.browserImport);
  browserNoteEl.textContent = app.browser
    ? `On. The browser runs on its own copy of your Chrome profile in ~/.localcode/browser, and asks nothing before it acts. A deny rule for "browser" keeps a site out of reach.${copied ? ` ${copied}` : ''}`
    : 'Off.';
}

// refreshBrowserIfOpen redraws the switch when it was changed elsewhere.
export function refreshBrowserIfOpen() {
  if (settings.isOpen) renderBrowserSetting();
}

async function toggleBrowser() {
  const enabled = browserCheckbox.checked;
  browserCheckbox.disabled = true;
  try {
    const res = await apiClient.setBrowser(enabled);
    app.browser = res && 'browser' in res ? !!res.browser : enabled;
    renderBrowserSetting(res && res.persisted === false
      ? `Applied, but not saved to config.json, so it lasts only until the daemon restarts: ${res.error || 'unknown error'}`
      : '');
    renderBrowserPanel();
  } catch (err) {
    browserCheckbox.checked = !enabled;
    browserNoteEl.textContent = `Not changed: ${err}`;
  } finally {
    browserCheckbox.disabled = false;
  }
}

async function importBrowserProfile() {
  browserImportBtn.disabled = true;
  try {
    const rep = await apiClient.importBrowserProfile();
    if (rep && rep.from) app.browserImport = { from: rep.from, profile: rep.profile, skipped: rep.skipped || [] };
    browserNoteEl.textContent = `Copied. Every conversation's tab was closed. ${importNote(app.browserImport)}`.trim();
  } catch (err) {
    browserNoteEl.textContent = `Not copied: ${err}`;
  } finally {
    browserImportBtn.disabled = false;
  }
}

// SSH.
//
// In the desktop window and the Web UI, where the SSH panel is. Off
// closes every connection, which the note says before it happens. The
// list is every connection open now, in any conversation: a shell left
// open in a conversation nobody is looking at is a login somebody should
// be able to find and end.

const sshEl = (id) => document.getElementById(id);

function renderSSHSetting(outcome) {
  const warning = unsavedWarning('ssh', outcome);
  const box = sshEl('ssh-checkbox');
  if (!box) return;
  box.checked = !!app.ssh;
  box.disabled = !app.sshAvailable;
  const warn = sshEl('ssh-warn');
  if (warn) {
    warn.textContent = warning || '';
    warn.hidden = !warning;
  }
  const note = sshEl('ssh-note');
  if (note) {
    note.textContent = app.ssh
      ? 'On. The model asks before it connects, runs a command, or types: as bash asks, with the same rules ("ssh_connect" for hosts, "ssh" for commands, "ssh_input" for typing). Host keys and passwords are asked of you in the SSH panel. Turning it off closes every connection.'
      : 'Off.';
  }
  renderSSHLive();
}

async function renderSSHLive() {
  const list = sshEl('ssh-live');
  if (!list) return;
  const clearList = () => { for (const c of [...list.children]) c.remove(); };
  if (!app.ssh) {
    clearList();
    return;
  }
  let rows = [];
  try {
    rows = await apiClient.listSSH();
  } catch (err) {
    rows = [];
  }
  clearList();
  if (!Array.isArray(rows) || rows.length === 0) {
    const p = document.createElement('p');
    p.className = 'note';
    p.textContent = 'No connection is open.';
    list.appendChild(p);
    return;
  }
  for (const r of rows) {
    const row = document.createElement('div');
    row.className = 'ssh-live-row';
    const what = document.createElement('span');
    what.textContent = `${r.target} (${r.id}) in ${r.title || r.session}`;
    what.title = what.textContent;
    const close = document.createElement('button');
    close.className = 'icon-btn';
    close.textContent = 'Close';
    close.addEventListener('click', async () => {
      close.disabled = true;
      try {
        await apiClient.closeSSH(r.session, r.id);
      } catch (err) {
        // The list below says what is still open.
      }
      renderSSHLive();
    });
    row.append(what, close);
    list.appendChild(row);
  }
}

// refreshSSHIfOpen redraws the switch when it was changed elsewhere.
export function refreshSSHIfOpen() {
  if (settings.isOpen) renderSSHSetting();
}

async function toggleSSH() {
  const box = sshEl('ssh-checkbox');
  const enabled = box.checked;
  box.disabled = true;
  try {
    const res = await apiClient.setSSH(enabled);
    app.ssh = res && 'ssh' in res ? !!res.ssh : enabled;
    renderSSHSetting(res && res.persisted === false
      ? `Applied, but not saved to config.json, so it lasts only until the daemon restarts: ${res.error || 'unknown error'}`
      : '');
    renderSSHPanel();
  } catch (err) {
    box.checked = !enabled;
    const note = sshEl('ssh-note');
    if (note) note.textContent = `Not changed: ${err}`;
  } finally {
    box.disabled = false;
  }
}

// Orchestration.
//
// Everything that hands work to another agent: the built-in specialists,
// Task and the background pair, and the Orchestrate tool's plans. The note
// carries the part that cannot be written into the page, because it comes
// from the daemon: which specialists the build ships, and that delegation
// costs model calls.
//
// The note states the two ceilings that bound what a run can spend. The
// timeouts are in the permission prompt each run asks with, and all of
// them are in docs/USAGE.md. The numbers are the constants in
// internal/agent/orchestrate*.go, written out here by hand.
// TestTheSettingsNoteCeilingsAreTheRealOnes reads this function, so
// changing one side alone fails the build.

function renderOrchestrate(outcome) {
  const warning = unsavedWarning('orchestrate', outcome);
  orchestrateCheckbox.checked = !!app.orchestrate;
  orchestrateWarnEl.textContent = warning || '';
  orchestrateWarnEl.hidden = !warning;
  if (!app.orchestrate) {
    orchestrateNoteEl.textContent = 'Off. The model does the work itself and delegates to no agent.';
    return;
  }
  const roster = (app.orchestrateRoster || []).join(', ');
  const agents = roster ? `Specialists: ${roster}. ` : '';
  orchestrateNoteEl.textContent =
    `On. ${agents}More model calls per request. Each plan asks first unless pre-approved. Up to 8 stages and 32 agent turns.`;
}

// The model running commands.
//
// The note carries the list, because the switch alone says nothing about
// what it reaches: the opt-ins live in config.json and in each command's
// own frontmatter, and somebody turning this on without seeing them has
// turned on something they cannot name.
function renderModelInvocable(outcome) {
  const warning = unsavedWarning('model_invocable', outcome);
  modelInvocableCheckbox.checked = !!app.modelInvocable;
  modelInvocableWarnEl.textContent = warning || '';
  modelInvocableWarnEl.hidden = !warning;
  const names = app.modelCommands || [];
  if (!app.modelInvocable) {
    modelInvocableNoteEl.textContent = 'Off. Commands run only when you type them.';
    return;
  }
  if (!names.length) {
    modelInvocableNoteEl.textContent =
      'On, but nothing is opted in. Add built-ins to "model_commands" in config.json, or "model_invocable: true" to a command or skill.';
    return;
  }
  modelInvocableNoteEl.textContent = `On. May run: ${names.join(', ')}. Reachable from anything the model reads.`;
}

export function refreshModelInvocableIfOpen() {
  if (settings.isOpen) renderModelInvocable();
}

async function toggleModelInvocable() {
  const enabled = modelInvocableCheckbox.checked;
  modelInvocableCheckbox.disabled = true;
  try {
    const res = await apiClient.setModelInvocable(enabled);
    app.modelInvocable = res && 'model_invocable' in res ? !!res.model_invocable : enabled;
    renderModelInvocable(res && res.persisted === false
      ? `Applied, but not saved to config.json, so it lasts only until the daemon restarts: ${res.error || 'unknown error'}`
      : '');
  } catch (err) {
    modelInvocableCheckbox.checked = !enabled;
    modelInvocableNoteEl.textContent = `Not changed: ${err}`;
  } finally {
    modelInvocableCheckbox.disabled = false;
  }
}

export function refreshOrchestrateIfOpen() {
  if (settings.isOpen) renderOrchestrate();
}

async function toggleOrchestrate() {
  const enabled = orchestrateCheckbox.checked;
  orchestrateCheckbox.disabled = true;
  try {
    const res = await apiClient.setOrchestrate(enabled);
    app.orchestrate = res && 'orchestrate' in res ? !!res.orchestrate : enabled;
    renderOrchestrate(res && res.persisted === false
      ? `Applied, but not saved to config.json, so it lasts only until the daemon restarts: ${res.error || 'unknown error'}`
      : '');
  } catch (err) {
    orchestrateCheckbox.checked = !enabled;
    orchestrateNoteEl.textContent = `Not changed: ${err}`;
  } finally {
    orchestrateCheckbox.disabled = false;
  }
}

// refreshSmartAgentIfOpen redraws the switch when the setting was changed
// somewhere else — another browser, or "/config smart_agent on" typed in
// the TUI. Only while the panel is open: there is no status bar pill for
// this one, so there is nothing else on screen to keep in step.
export function refreshSmartAgentIfOpen() {
  if (settings.isOpen) renderSmartAgent();
}

async function toggleSmartAgent() {
  const enabled = smartAgentCheckbox.checked;
  smartAgentCheckbox.disabled = true;
  try {
    // The daemon answers with what it did, in two parts. "applied" is
    // whether the running daemon changed, and it is what the box has to
    // show; "persisted" is only whether config.json was written. Treating
    // an unsaved change as a refused one used to leave the box saying the
    // opposite of the state the daemon was actually in, which is the one
    // thing this switch must never do: it decides which model answers and
    // which tools an agent may call.
    const res = await apiClient.setSmartAgent(enabled);
    app.smartAgent = res && 'smart_agent' in res ? !!res.smart_agent : enabled;
    renderSmartAgent(res && res.persisted === false
      ? `Applied, but not saved to config.json, so it lasts only until the daemon restarts: ${res.error || 'unknown error'}`
      : '');
  } catch (err) {
    // Nothing was applied. Put the box back where it was.
    smartAgentCheckbox.checked = !enabled;
    smartAgentNoteEl.textContent = `Not changed: ${err}`;
  } finally {
    smartAgentCheckbox.disabled = false;
  }
}

// Updates.
//
// Nothing here runs on its own, and the two buttons are separate because
// what they do is not the same kind of thing. Checking asks GitHub what
// the latest release is — an outbound request that says which version
// this machine runs, so it happens when someone asks for it and not when
// the panel opens. Installing replaces the program being used, which is
// why it is a second click on a button that only appears once there is
// something to install.
//
// latest holds the last answer, so the install button knows which version
// it is about to fetch and can say so.
let latest = null;

function showUpdate(text, warn) {
  updateNoteEl.textContent = text;
  updateNoteEl.className = warn ? 'note warn' : 'note';
}

// lastInstallLine is the one line for a recorded install: the version,
// the exit code and what it means, and the log path. Worded by the
// status the daemon sent, not by the code: a record is reported with an
// installed status when its version is not the running one, for example
// when a daemon that is still the old version reads it. Only the
// not-installed statuses say "did not install". The failed-install
// sentence here is the same one the daemon writes on the next stream to
// open (MSIFailureLine in internal/update/msi_notice.go): the panel test
// in test/webui/update.test.js and the Go test pin the same literal, so
// the two cannot drift apart without a test failing.
function lastInstallLine(res) {
  const last = res && res.last_install;
  if (!last || !last.version) return '';
  if (last.status && last.status.startsWith('installed')) {
    return `Update to ${last.version} was installed: the installer exited ${last.exit_code} (${last.meaning}). Restart localcode to run it. Log: ${last.log}.`;
  }
  return `Update to ${last.version} did not install: the installer exited ${last.exit_code} (${last.meaning}). Log: ${last.log}.`;
}

async function checkForUpdate() {
  latest = null;
  updateInstallBtn.hidden = true;
  updateCheckBtn.disabled = true;
  // Not "Asking GitHub" any more: a machine configured with update_url
  // is asking somewhere else entirely, and saying GitHub while looking
  // at an internal server is the panel telling a small lie.
  showUpdate('Checking for a newer version…');
  try {
    const res = await apiClient.checkUpdate();
    if (!res.checked) {
      showUpdate(`Could not check: ${res.detail}`, true);
      return;
    }
    const last = lastInstallLine(res);
    const withLast = (text) => last ? `${text} ${last}` : text;
    if (!res.available) {
      showUpdate(withLast(res.detail || `localcode ${res.current} is the latest release`));
      return;
    }
    latest = res;
    const size = res.size ? ` (${Math.round(res.size / (1024 * 1024))}MB)` : '';
    // The install button appears only where the daemon and the person
    // clicking share a machine. Over --server it would replace the
    // program on the *server*, so the daemon says no and the panel says
    // where to get it instead.
    // Where it looked, when that is not the public releases page. An
    // internal build reported as "0.65.0 is available" reads as a public
    // release, and nobody notices it came from somewhere else.
    //
    // An http source is marked unverified beside the address, on every
    // offer rather than once: the URL alone does not say that nothing
    // authenticated the host, and the offer is read on its own each time.
    const rawSource = res.source && !res.source.startsWith('https://github.com/')
      ? res.source : '';
    const unverified = rawSource && res.source_unverified
      ? ', unverified: plain http, the host was not authenticated' : '';
    const from = rawSource ? ` (from ${rawSource}${unverified})` : '';
    if (res.can_install) {
      showUpdate(withLast(`localcode ${res.latest} is available${from}. This will download ${res.asset}${size} and run the installer.`));
      updateInstallBtn.textContent = `Download and install ${res.latest}`;
      updateInstallBtn.hidden = false;
    } else {
      showUpdate(withLast(`localcode ${res.latest} is available${from}. ${res.detail || ''}`.trim()));
    }
  } catch (err) {
    showUpdate(`Could not check: ${err}`, true);
  } finally {
    updateCheckBtn.disabled = false;
  }
}

async function installUpdate() {
  if (!latest) return;
  // Asked once, plainly, because the answer is not undoable: the program
  // the person is using is about to be replaced, and on Windows that also
  // means an elevation prompt and localcode closing. The words depend on
  // what will be installed, not on where the page runs: only the Windows
  // MSI closes the window and opens it again, or starts when localcode
  // exits. A bundle, a tarball, a .deb or a zip each does something else,
  // and for those the confirm keeps the generic sentence.
  const inWindow = typeof window.lcWindowCommand === 'function';
  const isMSI = typeof latest.asset === 'string' && latest.asset.endsWith('.msi');
  let confirmText;
  if (!isMSI) {
    confirmText = `Download and install localcode ${latest.latest}?\n\nlocalcode restarts, or closes for an installer to replace its files.`;
  } else if (inWindow) {
    confirmText = `Download and install localcode ${latest.latest}?\n\nThe window closes and the installer runs. The window opens again when the installer has finished.`;
  } else {
    confirmText = `Download and install localcode ${latest.latest}?\n\nThe installer starts when localcode exits. Quit localcode to run it.`;
  }
  if (!await askConfirm(`Install localcode ${latest.latest}?`, confirmText)) return;

  updateInstallBtn.disabled = true;
  updateCheckBtn.disabled = true;
  showUpdate(`Downloading ${latest.asset}… this can take a minute.`);
  try {
    const res = await apiClient.installUpdate();
    // An unverified download is stated rather than left unsaid. A file
    // share publishes the installer and usually nothing else, so there
    // was no checksum to check it against, and that is a true thing
    // about a file that has just been run.
    //
    // An http source is stated here too, not only on the check: the
    // install reply is read on its own, after the check has scrolled by.
    const unverifiedSource = res.source_unverified
      ? ' The source is unverified: plain http, so the host was not authenticated.'
      : '';
    const unverified = res.verified === false
      ? ' The download could not be verified: no checksum was published beside it.'
      : '';
    showUpdate((res.detail || `localcode ${res.version} downloaded.`) + unverifiedSource + unverified);
    // The daemon is about to replace itself with the version it just
    // installed, which takes this page's connection with it. Nothing to
    // do but say so and let the browser reconnect — the new daemon binds
    // the same address, and the event stream retries on its own.
    if (res.restarting) {
      updateInstallBtn.hidden = true;
      updateCheckBtn.disabled = true;
      return;
    }
    if (res.started) {
      updateInstallBtn.hidden = true;
      // The frameless desktop window can close itself; anywhere else the
      // sentence above is the whole instruction. Not done automatically
      // either way — a window vanishing under someone mid-click is worse
      // than a line asking them to close it.
      if (typeof window.lcWindowCommand === 'function') {
        showUpdate(`${res.detail} This window closes in 3 seconds.`);
        setTimeout(() => window.lcWindowCommand('close'), 3000);
      }
    }
  } catch (err) {
    showUpdate(`Not installed: ${err}`, true);
  } finally {
    // Both buttons, and here rather than in the catch.
    //
    // On the paths that hide the install button this is moot; on the one
    // that does neither — an install that replaced the files without
    // restarting, which is every macOS bundle — the button stayed visible
    // and disabled for the life of the page, with no way back to it but a
    // reload.
    updateInstallBtn.disabled = false;
    updateCheckBtn.disabled = false;
  }
}

export function initSettings() {
  settingsBtn.addEventListener('click', openSettings);
  settingsCloseBtn.addEventListener('click', () => settings.close());
  wireTypography();
  selectSettingsTab(activeSettingsTab);
  for (const t of settingsTabs) {
    if (!t.tab) continue;
    t.tab.addEventListener('click', () => selectSettingsTab(t.name));
    // Arrow keys move between tabs, with wrapping; Home and End jump.
    // Attached to each tab rather than the tablist, since that is what
    // holds the focus the keys move.
    t.tab.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
        e.preventDefault();
        moveSettingsTab(t.name, 1);
      } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
        e.preventDefault();
        moveSettingsTab(t.name, -1);
      } else if (e.key === 'Home') {
        e.preventDefault();
        selectSettingsTab(settingsTabs[0].name, true);
      } else if (e.key === 'End') {
        e.preventDefault();
        selectSettingsTab(settingsTabs[settingsTabs.length - 1].name, true);
      }
    });
  }
  // Escape closes the window, and only closes it: main.js's cancel-turn
  // handler stands down while this window is open, the way it already
  // does for a permission request. Closing a window you opened to look
  // at is not a request to stop the work going on behind it.
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && settings.isOpen) settings.close();
  });
  smartAgentCheckbox.addEventListener('change', toggleSmartAgent);
  browserCheckbox.addEventListener('change', toggleBrowser);
  browserImportBtn.addEventListener('click', importBrowserProfile);
  sshEl('ssh-checkbox')?.addEventListener('change', toggleSSH);
  orchestrateCheckbox.addEventListener('change', toggleOrchestrate);
  modelInvocableCheckbox.addEventListener('change', toggleModelInvocable);
  keepGoingCheckbox.addEventListener('change', toggleKeepGoing);
  foldThinkingCheckbox.addEventListener('change', toggleFoldThinking);
  repeatLimitCheckbox.addEventListener('change', applyRepeatLimit);
  repeatLimitInput.addEventListener('change', applyRepeatLimit);
  updateCheckBtn.addEventListener('click', checkForUpdate);
  updateInstallBtn.addEventListener('click', installUpdate);
}
