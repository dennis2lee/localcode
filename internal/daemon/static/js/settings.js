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
  orchestrateCheckbox, orchestrateNoteEl, orchestrateWarnEl,
  modelInvocableCheckbox, modelInvocableNoteEl, modelInvocableWarnEl,
  keepGoingCheckbox, keepGoingWarnEl,
  foldThinkingCheckbox, foldThinkingNoteEl, foldThinkingWarnEl, museNoteEl,
  repeatLimitCheckbox, repeatLimitInput, repeatLimitWarnEl,
  updateCheckBtn, updateInstallBtn, updateNoteEl,
} from './dom.js';
import { Modal } from './modal.js';
import { app } from './state.js';
import * as apiClient from './api.js';
import { wireTypography, renderTypography } from './typography.js';

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
  renderSmartAgent();
  renderOrchestrate();
  renderModelInvocable();
  renderKeepGoing();
  renderMuseNote();
  renderFoldThinking();
  settings.open();
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
function renderFoldThinking(warning) {
  foldThinkingCheckbox.checked = !!app.foldThinking;
  foldThinkingWarnEl.textContent = warning || '';
  foldThinkingWarnEl.hidden = !warning;
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

async function toggleFoldThinking() {
  const enabled = foldThinkingCheckbox.checked;
  foldThinkingCheckbox.disabled = true;
  try {
    await apiClient.setFoldThinking(enabled);
    app.foldThinking = enabled;
    renderFoldThinking();
  } catch (err) {
    foldThinkingCheckbox.checked = !enabled;
    renderFoldThinking(`Not changed: ${err}`);
  } finally {
    foldThinkingCheckbox.disabled = false;
  }
}

// Keep going.
//
// The tab carries the scope (muse models only); this only moves the box
// and reports a save that did not stick.
function renderKeepGoing(warning) {
  keepGoingCheckbox.checked = !!app.keepGoing;
  keepGoingWarnEl.textContent = warning || '';
  keepGoingWarnEl.hidden = !warning;
}

// refreshKeepGoingIfOpen redraws the box when the switch moved somewhere
// else: "/keep-going" typed at any prompt, or another window.
export function refreshKeepGoingIfOpen() {
  if (settings.isOpen) renderKeepGoing();
}

async function toggleKeepGoing() {
  const enabled = keepGoingCheckbox.checked;
  keepGoingCheckbox.disabled = true;
  try {
    await apiClient.setKeepGoing(enabled);
    app.keepGoing = enabled;
    renderKeepGoing();
  } catch (err) {
    keepGoingCheckbox.checked = !enabled;
    renderKeepGoing(`Not changed: ${err}`);
  } finally {
    keepGoingCheckbox.disabled = false;
  }
}

// The repeat guard.
//
// A checkbox and a number, because "off" and "give it room" are different
// sizes of the same request. The number is only editable while the box
// is checked; unchecking sends zero, checking sends the number.
function renderRepeatLimit(warning) {
  const on = app.repeatLimit > 0;
  repeatLimitCheckbox.checked = on;
  repeatLimitInput.disabled = !on;
  if (on) repeatLimitInput.value = String(app.repeatLimit);
  repeatLimitWarnEl.textContent = warning || '';
  repeatLimitWarnEl.hidden = !warning;
}

export function refreshRepeatLimitIfOpen() {
  if (settings.isOpen) renderRepeatLimit();
}

async function applyRepeatLimit() {
  const wanted = repeatLimitCheckbox.checked ? Math.max(1, parseInt(repeatLimitInput.value, 10) || 3) : 0;
  repeatLimitCheckbox.disabled = true;
  repeatLimitInput.disabled = true;
  try {
    await apiClient.setRepeatLimit(wanted);
    app.repeatLimit = wanted;
    renderRepeatLimit();
  } catch (err) {
    renderRepeatLimit(`Not changed: ${err}`);
  } finally {
    repeatLimitCheckbox.disabled = false;
    repeatLimitInput.disabled = app.repeatLimit === 0;
  }
}

// Smart Agent.
//
// The switch is the whole control. What it turns on — the specialist
// roster, the orchestration prompt, the background delegation tools — is
// not configurable from here on purpose: the point of the feature is that
// it works without anyone writing six agent blocks by hand, and a panel
// full of knobs would put that back.
//
// The note below it carries the part that cannot be written into the
// page, because it depends on the daemon's build and on which profiles
// this config has: which specialists exist, and that they cost money.

function renderSmartAgent(warning) {
  smartAgentCheckbox.checked = !!app.smartAgent;
  smartAgentWarnEl.textContent = warning || '';
  smartAgentWarnEl.hidden = !warning;
  if (!app.smartAgent) {
    smartAgentNoteEl.textContent = 'Off. One model, one context.';
    return;
  }
  const roster = (app.smartAgentRoster || []).join(', ');
  const agents = roster ? `Agents: ${roster}. ` : '';
  smartAgentNoteEl.textContent = `On. ${agents}More model calls per request. Turn log in ~/.localcode/trace.`;
}

// Orchestration.
//
// A separate switch from Smart Agent, and the note carries why: a plan
// needs somewhere to delegate its stages to, so with one agent configured
// this is on and inert. The daemon reports the roster alongside both
// switches, which is what lets the panel say so instead of the endpoint
// refusing.

function renderOrchestrate(warning) {
  orchestrateCheckbox.checked = !!app.orchestrate;
  orchestrateWarnEl.textContent = warning || '';
  orchestrateWarnEl.hidden = !warning;
  if (!app.orchestrate) {
    orchestrateNoteEl.textContent = 'Off. One delegation at a time.';
    return;
  }
  const roster = (app.smartAgentRoster || []).length;
  if (!app.smartAgent && roster) {
    orchestrateNoteEl.textContent = 'On, but nobody to delegate to. Turn on Smart Agent for the built-in roster.';
    return;
  }
  orchestrateNoteEl.textContent =
    'On. Every run asks first. Limits: 8 stages and 32 agent turns, 10 minutes a stage, 30 a run.';
}

// The model running commands.
//
// The note carries the list, because the switch alone says nothing about
// what it reaches: the opt-ins live in config.json and in each command's
// own frontmatter, and somebody turning this on without seeing them has
// turned on something they cannot name.
function renderModelInvocable(warning) {
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
    if (!res.available) {
      showUpdate(res.detail || `localcode ${res.current} is the latest release`);
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
      showUpdate(`localcode ${res.latest} is available${from}. This will download ${res.asset}${size} and run the installer.`);
      updateInstallBtn.textContent = `Download and install ${res.latest}`;
      updateInstallBtn.hidden = false;
    } else {
      showUpdate(`localcode ${res.latest} is available${from}. ${res.detail || ''}`.trim());
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
  // means an elevation prompt and localcode closing.
  if (!window.confirm(`Download and install localcode ${latest.latest}?\n\nlocalcode restarts, or closes for an installer to replace its files.`)) return;

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
        showUpdate(`${res.detail} — closing localcode in a moment.`);
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
  orchestrateCheckbox.addEventListener('change', toggleOrchestrate);
  modelInvocableCheckbox.addEventListener('change', toggleModelInvocable);
  keepGoingCheckbox.addEventListener('change', toggleKeepGoing);
  foldThinkingCheckbox.addEventListener('change', toggleFoldThinking);
  repeatLimitCheckbox.addEventListener('change', applyRepeatLimit);
  repeatLimitInput.addEventListener('change', applyRepeatLimit);
  updateCheckBtn.addEventListener('click', checkForUpdate);
  updateInstallBtn.addEventListener('click', installUpdate);
}
