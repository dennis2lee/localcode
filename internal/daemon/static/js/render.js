import {
  tasksEl, mcpServersEl, statusTextEl, statusBarEl, agentSelectEl,
  permissionStatusBtn, autoDelegateBtn, workspaceBtn,
} from './dom.js';
import { app, session } from './state.js';

// renderTasks builds each row with createElement/textContent rather than an
// innerHTML template string — t.agent and t.status come straight from SSE
// payloads (task.spawned/task.status), and this was the one listing in the
// file that spliced such values into innerHTML unescaped (bug B5).
export function renderTasks() {
  tasksEl.innerHTML = '';
  if (session.tasks.size === 0) {
    tasksEl.innerHTML = '<div style="color:var(--muted)">none</div>';
    return;
  }
  for (const [id, t] of session.tasks) {
    const div = document.createElement('div');
    div.className = 'task';

    const agentDiv = document.createElement('div');
    agentDiv.className = 'agent';
    agentDiv.textContent = t.agent || '';
    div.appendChild(agentDiv);

    const idDiv = document.createElement('div');
    idDiv.textContent = id;
    div.appendChild(idDiv);

    const statusDiv = document.createElement('div');
    statusDiv.className = `status-${t.status}`;
    statusDiv.textContent = t.status;
    div.appendChild(statusDiv);

    tasksEl.appendChild(div);
  }
}

export function renderMCPServers() {
  mcpServersEl.innerHTML = '';
  if (!app.mcpServers || app.mcpServers.length === 0) {
    mcpServersEl.innerHTML = '<div style="color:var(--muted)">no connected servers</div>';
    return;
  }
  for (const name of app.mcpServers) {
    const div = document.createElement('div');
    div.className = 'mcp-item';
    div.textContent = name;
    mcpServersEl.appendChild(div);
  }
}

// modelForAgent returns the model configured for an agent, from the
// /api/agents listing. It is what lets the status line name a model before
// the first usage event of a session has arrived — otherwise a freshly
// opened session shows no model at all until it answers once.
export function modelForAgent(name) {
  const a = app.agents.find(x => x.name === name);
  return (a && a.model) || '';
}

// renderStatusBar draws the single line below the prompt box: current
// agent/model, context-window usage, tokens-per-second (if enabled), and
// whether a turn is in flight.
export function renderStatusBar() {
  const parts = [];
  parts.push(`agent: ${session.currentAgent || '?'}`);
  // Prefer what the model actually reported over what config says it
  // should be; they differ when a profile is overridden server-side.
  const model = (session.lastUsage && session.lastUsage.model) || modelForAgent(session.currentAgent);
  if (model) parts.push(`model: ${model}`);
  if (session.lastUsage && typeof session.lastUsage.percent === 'number') {
    parts.push(`context: ${session.lastUsage.percent.toFixed(1)}%`);
  }
  if (app.showTPS && session.lastUsage && session.lastUsage.tps) {
    parts.push(`${session.lastUsage.tps.toFixed(1)} tok/s`);
  }
  if (session.waiting) {
    let busyText = session.runningTool ? `${session.runningTool}… esc to cancel` : 'working… esc to cancel';
    if (session.promptQueue.length > 0) busyText += ` (${session.promptQueue.length} queued)`;
    parts.push(busyText);
  }
  const activeTasks = [...session.tasks.values()].filter(t => t.status === 'spawned' || t.status === 'running').length;
  if (activeTasks > 0) parts.push(`${activeTasks} background task${activeTasks > 1 ? 's' : ''}`);
  statusTextEl.textContent = parts.join('  ·  ');

  statusBarEl.classList.remove('ctx-warn', 'ctx-crit');
  if (session.lastUsage && typeof session.lastUsage.percent === 'number') {
    if (session.lastUsage.percent >= 90) statusBarEl.classList.add('ctx-crit');
    else if (session.lastUsage.percent >= 70) statusBarEl.classList.add('ctx-warn');
  }
}

// renderPermissionStatus draws the small pill next to the status line:
// "permissions: skip" (in warn color) when skip_permissions is on, or the
// count of custom rules otherwise. Click opens the settings modal in
// both the browser Web UI and the native GUI window (same page).
export function renderPermissionStatus() {
  const ruleCount = Object.values(app.permissionRules).reduce((n, rs) => n + rs.length, 0);
  permissionStatusBtn.classList.toggle('skip', app.skipPermissions);
  if (app.skipPermissions) {
    permissionStatusBtn.textContent = 'permissions: skip';
  } else if (ruleCount > 0) {
    permissionStatusBtn.textContent = `permissions: ask (${ruleCount} rule${ruleCount > 1 ? 's' : ''})`;
  } else {
    permissionStatusBtn.textContent = 'permissions: ask';
  }
}

// renderAutoDelegate draws the auto-delegation pill next to the permission
// pill. Auto-delegation needs an auto_delegate block in config.json to say
// which agent handles delegated prompts; with no such block the setting can
// still be flipped but delegates nothing, so the pill says so rather than
// showing a bare "on" that does nothing.
export function renderAutoDelegate() {
  autoDelegateBtn.classList.toggle('on', app.autoDelegate);
  if (app.autoDelegate && !app.autoDelegateAgent) {
    autoDelegateBtn.textContent = 'auto-delegate: on (unconfigured)';
    autoDelegateBtn.title = 'auto-delegate is on but no agent is chosen to answer delegated prompts, so nothing is delegated — click to configure it';
    return;
  }
  autoDelegateBtn.textContent = `auto-delegate: ${app.autoDelegate ? 'on' : 'off'}`;
  autoDelegateBtn.title = app.autoDelegateAgent
    ? `matching prompts ${app.autoDelegate ? 'go' : 'would go'} to the "${app.autoDelegateAgent}" agent (${app.autoDelegateMatch.length} pattern${app.autoDelegateMatch.length === 1 ? '' : 's'}) — click to configure`
    : 'click to choose which prompts are delegated and which agent answers them';
}

export function renderWorkspace() {
  workspaceBtn.textContent = app.workspacePath || '(unknown workspace)';
  workspaceBtn.title = app.canBrowseWorkspace
    ? `${app.workspacePath}\nclick to pick a workspace folder`
    : `${app.workspacePath}\nclick to change the workspace directory`;
}

// setCurrentAgent updates session state and the header dropdown together —
// the two must never drift, so nothing sets one without the other.
export function setCurrentAgent(name) {
  session.currentAgent = name;
  if ([...agentSelectEl.options].some(o => o.value === name)) {
    agentSelectEl.value = name;
  }
}
