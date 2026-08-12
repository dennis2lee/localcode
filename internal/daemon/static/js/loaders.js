import { agentSelectEl, appVersionEl, micBtn } from './dom.js';
import { app, session } from './state.js';
import * as apiClient from './api.js';
import { appendError } from './transcript.js';
import { renderStatusBar, renderPermissionStatus, renderAutoDelegate, renderWorkspace, renderMCPServers } from './render.js';

export async function loadAgents() {
  try {
    app.agents = await apiClient.getAgents();
  } catch (err) {
    app.agents = [];
  }
  agentSelectEl.innerHTML = '';
  for (const a of app.agents) {
    const opt = document.createElement('option');
    opt.value = a.name;
    // The model is the part that decides cost and capability, so it goes
    // in the option text itself; the description is long and varies
    // wildly in length, so it goes in the tooltip instead of pushing the
    // model off the end of a narrow dropdown.
    opt.textContent = a.model ? `${a.name} (${a.model})` : a.name;
    if (a.description) opt.title = a.description;
    agentSelectEl.appendChild(opt);
  }
  if (session.currentAgent) agentSelectEl.value = session.currentAgent;
}

// cycleAgent switches to the next (or previous) agent in the dropdown, the
// Web UI counterpart of the TUI's Tab key. The switch itself goes through
// the daemon, so currentAgent updates from the agent.switched event like
// every other client's does.
export async function cycleAgent(step) {
  if (!session.sessionID || app.agents.length < 2) return;
  const idx = app.agents.findIndex(a => a.name === session.currentAgent);
  const next = app.agents[((idx < 0 ? 0 : idx) + step + app.agents.length) % app.agents.length];
  if (!next || next.name === session.currentAgent) return;
  try {
    await apiClient.switchAgent(session.sessionID, next.name);
  } catch (err) {
    appendError(`failed to switch agent: ${err}`);
  }
}

// The daemon's version, shown next to the name in the header. Failure is
// silent: a missing version number is not worth an error line in the
// transcript, and every other part of the page works without it.
export async function loadVersion() {
  try {
    const v = await apiClient.getVersion();
    appVersionEl.textContent = v.version ? `v${v.version}` : '';
    appVersionEl.title = `daemon version ${v.version || '(unknown)'}`;
  } catch (err) {
    appVersionEl.textContent = '';
  }
}

// The microphone pill sits in the status row under the prompt box and is
// always there. It used to be hidden whenever a dictation could not
// start, on the reasoning that a button which can only fail is worse
// than no button — but the reason it cannot start is almost always "no
// model directory is configured", which is a thing you would fix if you
// knew about it. Hiding the control hid the feature: there was nothing
// on screen to suggest dictation existed, let alone how to turn it on.
//
// So it stays visible and says which of the two it is. Unavailable means
// disabled, with the daemon's own explanation in the tooltip.
export async function loadDictation() {
  let detail = 'the daemon did not answer';
  try {
    const s = await apiClient.getDictationStatus();
    if (s.ready) {
      setDictationAvailable(true);
      return;
    }
    detail = s.detail || 'no reason given';
  } catch (err) {
    // A daemon too old to know the endpoint 404s, which lands here.
  }
  setDictationAvailable(false, detail);
}

function setDictationAvailable(ready, detail) {
  micBtn.disabled = !ready;
  micBtn.textContent = ready ? '\u{1F3A4} dictation: off' : '\u{1F3A4} dictation: unavailable';
  micBtn.title = ready
    ? 'click to dictate a prompt'
    : `dictation is not available: ${detail}`;
}

export async function loadCommands() {
  try {
    app.customCommands = await apiClient.getCommands();
  } catch (err) {
    app.customCommands = [];
  }
}

export async function loadSettings() {
  try {
    const s = await apiClient.getSettings();
    app.autoCompactEnabled = s.auto_compact_enabled;
    app.showTPS = s.show_tps;
    app.autoDelegate = !!s.auto_delegate;
    app.autoDelegateAgent = s.auto_delegate_agent || '';
    app.autoDelegateMatch = s.auto_delegate_match || [];
    app.skipPermissions = !!s.skip_permissions;
    app.permissionRules = s.permission_rules || {};
    app.canEditPermissions = !!s.can_edit_permissions;
  } catch (err) {
    // keep defaults
  }
  renderStatusBar();
  renderPermissionStatus();
  renderAutoDelegate();
}

export async function loadWorkspace() {
  try {
    const w = await apiClient.getWorkspace();
    app.workspacePath = w.path || '';
    app.canBrowseWorkspace = !!w.can_browse;
    app.canRevealWorkspace = !!w.can_reveal;
  } catch (err) {
    app.workspacePath = '';
    app.canBrowseWorkspace = false;
    app.canRevealWorkspace = false;
  }
  renderWorkspace();
}

export async function loadMCPServers() {
  try {
    app.mcpServers = await apiClient.getMCPServers();
  } catch (err) {
    app.mcpServers = [];
  }
  renderMCPServers();
}
