// The settings window.
//
// Everything here belongs to the daemon rather than to this browser, and
// is shared by every client attached to it. Adding a setting later should
// be adding a block to index.html and a line to one of the handlers
// below, not a redesign.

import {
  settingsModalEl, settingsBtn, settingsCloseBtn,
  smartAgentCheckbox, smartAgentNoteEl, smartAgentWarnEl,
  keepGoingCheckbox, keepGoingWarnEl,
  updateCheckBtn, updateInstallBtn, updateNoteEl,
} from './dom.js';
import { Modal } from './modal.js';
import { app } from './state.js';
import * as apiClient from './api.js';

export const settings = new Modal(settingsModalEl);

export function openSettings() {
  // Cleared rather than checked: opening the panel is not asking GitHub
  // anything, and a stale answer from ten minutes ago would look like one.
  updateNoteEl.textContent = '';
  updateInstallBtn.hidden = true;
  renderSmartAgent();
  renderKeepGoing();
  settings.open();
}

// Keep going.
//
// The label in index.html carries the scope (muse models only); this
// only moves the box and reports a save that did not stick.
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
    smartAgentNoteEl.textContent =
      'Off. Every request is answered by this session\u2019s own model, in this session\u2019s context.';
    return;
  }
  const roster = (app.smartAgentRoster || []).join(', ');
  const agents = roster ? `Adds the ${roster} agents, each on whichever configured profile suits it. ` : '';
  smartAgentNoteEl.textContent =
    `On. ${agents}Also turns on fallback to another model when one will not answer, prompt cache breakpoints, `
    + 'the turn log under ~/.localcode/trace, and guards on credential files and paths outside the workspace. '
    + 'Expect more model calls per request.';
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
    const from = res.source && !res.source.startsWith('https://github.com/')
      ? ` (from ${res.source})` : '';
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
    const unverified = res.verified === false
      ? ' The download could not be verified: no checksum was published beside it.'
      : '';
    showUpdate((res.detail || `localcode ${res.version} downloaded.`) + unverified);
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
    updateInstallBtn.disabled = false;
  } finally {
    updateCheckBtn.disabled = false;
  }
}

export function initSettings() {
  settingsBtn.addEventListener('click', openSettings);
  settingsCloseBtn.addEventListener('click', () => settings.close());
  smartAgentCheckbox.addEventListener('change', toggleSmartAgent);
  keepGoingCheckbox.addEventListener('change', toggleKeepGoing);
  updateCheckBtn.addEventListener('click', checkForUpdate);
  updateInstallBtn.addEventListener('click', installUpdate);
}
