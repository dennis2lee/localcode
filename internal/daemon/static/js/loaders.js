import { agentSelectEl, appVersionEl } from './dom.js';
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

export async function loadCommands() {
  try {
    app.customCommands = await apiClient.getCommands();
  } catch (err) {
    app.customCommands = [];
  }
}

// Skills are loaded for the same reason commands are: to complete a
// "/<name>" without having to already know it. A daemon that cannot
// answer simply has none, which is not a failure worth reporting.
export async function loadSkills() {
  try {
    app.skills = await apiClient.getSkills();
  } catch (err) {
    app.skills = [];
  }
}

// The commands the daemon answers itself, so they complete like a skill
// does. A daemon too old to have the endpoint simply offers fewer names.
export async function loadSlashCommands() {
  try {
    app.slashCommands = await apiClient.getSlashCommands();
  } catch (err) {
    app.slashCommands = [];
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
    app.smartAgent = !!s.smart_agent;
    app.smartAgentRoster = s.smart_agent_roster || [];
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

// loadWorkspace reads the directory the current session works in and puts
// it in the header.
//
// A read, not a write. The workspace has been per-session since v0.39, so
// opening a conversation does not move anything — the daemon already knows
// which directory that session belongs to, and this asks it. Switching
// used to POST the session's own path back to the daemon purely to update
// this label, which was a no-op that could still fail: the daemon refuses
// a workspace change while that session has a turn in flight, so opening a
// conversation that was working left the header naming the *previous*
// session's project. That is "the workspace at the top sometimes doesn't
// switch".
//
// The reply is dropped if the session moved on while it was in flight.
// Switching quickly through three sessions starts three of these, and
// which one paints last is otherwise decided by the network.
export async function loadWorkspace() {
  const asked = session.sessionID;
  let w = null;
  try {
    w = await apiClient.getWorkspace(asked);
  } catch (err) {
    // Nothing to add: the header keeps whatever it had, which for a
    // session switch is the path the listing already carried.
  }
  if (asked !== session.sessionID) return;
  if (w) {
    app.workspacePath = w.path || '';
    app.canBrowseWorkspace = !!w.can_browse;
    app.canRevealWorkspace = !!w.can_reveal;
  } else if (!app.workspacePath) {
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
