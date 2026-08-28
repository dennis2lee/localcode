import {
  tasksEl, mcpServersEl, statusTextEl, statusBarEl, agentSelectEl,
  permissionStatusBtn, autoDelegateBtn, workspaceBtn, workspaceRevealBtn, stopBtn, inputEl,
} from './dom.js';
import { app, session, turnInFlight } from './state.js';
import { completionHint } from './complete.js';
import { openTaskView } from './taskview.js';

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

    // What it is doing right now. A status of "running" for twenty
    // minutes says nothing about whether anything is happening; the name
    // of the tool it is in says quite a lot.
    if (t.doing) {
      const doing = document.createElement('div');
      doing.className = 'doing';
      doing.textContent = t.doing;
      div.appendChild(doing);
    }

    // The whole row opens the task's own conversation. A task is a
    // session, so there is a full transcript behind these three words —
    // there was just no way to reach it.
    div.title = 'click to watch this task';
    div.addEventListener('click', () => openTaskView(id));

    tasksEl.appendChild(div);
  }
}

// Each row gets a light: green for connected, blinking green while
// degraded (something failed but the session may still recover, so
// showing it as dead would be crying wolf), grey once disconnected. The
// daemon reports every *configured* server, including ones that never
// came up — a broken server has to be visible, or it looks like one
// nobody set up.
export function renderMCPServers() {
  mcpServersEl.innerHTML = '';
  const servers = app.mcpServers || [];
  if (servers.length === 0) {
    mcpServersEl.innerHTML = '<div style="color:var(--muted)">no configured servers</div>';
    return;
  }
  for (const s of servers) {
    const div = document.createElement('div');
    div.className = 'mcp-item';

    const led = document.createElement('span');
    led.className = `led led-${s.status || 'disconnected'}`;
    div.appendChild(led);

    const name = document.createElement('span');
    name.textContent = s.name;
    div.appendChild(name);

    // The detail is the last error. It goes in the title rather than the
    // row because it can be a paragraph of stderr, but without it
    // "disconnected" is a dead end for someone trying to fix it.
    div.title = s.detail ? `${s.name}: ${s.status}\n${s.detail}` : `${s.name}: ${s.status}`;
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
  // "~" marks a live estimate made while the model is still generating,
  // counted from stream deltas because the real token count only arrives
  // when the stream ends. It is replaced by the exact figure at that
  // point; the tilde is there so a number that can be off is never shown
  // as though it were measured.
  if (app.showTPS && session.lastUsage && session.lastUsage.tps) {
    const tick = session.lastUsage.estimated ? '~' : '';
    parts.push(`${tick}${session.lastUsage.tps.toFixed(1)} tok/s`);
  }
  // The stop button is the visible half of "esc to cancel". Esc is still
  // the fast way, but it depends on the key reaching the page — which is
  // not something a host webview guarantees, and when it does not the
  // only apparent way out of a long turn is to kill the window. A button
  // cannot be swallowed.
  // turnInFlight, not session.waiting: a turn running in this session is a
  // turn worth offering to stop, whoever started it and whatever this
  // client believes about it.
  const working = turnInFlight();
  stopBtn.hidden = !working;
  if (working) {
    let busyText = session.runningTool ? `${session.runningTool}…` : 'working…';
    if (session.promptQueue.length > 0) busyText += ` (${session.promptQueue.length} queued)`;
    parts.push(busyText);
  }
  const activeTasks = [...session.tasks.values()].filter(t => t.status === 'spawned' || t.status === 'running').length;
  if (activeTasks > 0) parts.push(`${activeTasks} background task${activeTasks > 1 ? 's' : ''}`);

  // While a "/name" is being typed, the line also says what the right
  // arrow would complete it to and how many other candidates there are.
  //
  // Appended rather than substituted, unlike the TUI's footer. There the
  // busy indicator has a band of its own; here this one line carries it,
  // so a line replaced by a completion hint is a line that stopped
  // saying a turn is running the moment you started typing the next
  // prompt, which is exactly when you are watching it.
  const hint = completionHint(inputEl.value);
  if (hint) parts.push(hint);
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
  // This conversation's own answer, not the daemon's default: the four
  // switches are per session, and a pill reading "skip" while the open
  // conversation is asking would be describing something else.
  const p = app.sessionPermissions || {};
  permissionStatusBtn.classList.toggle('skip', !!p.skip_all);
  if (p.skip_all) {
    permissionStatusBtn.textContent = 'permissions: skip';
  } else if (p.skip_tools) {
    // Said as what it is, because "skip" here would overstate it and
    // "ask" would understate it: tools do not ask, leaving the project
    // does.
    permissionStatusBtn.textContent = 'permissions: tools skipped';
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
  workspaceBtn.title = `${app.workspacePath}\nclick to change the workspace directory`;
  // Only where a window would open in front of the person clicking. Over
  // the network the daemon would open Explorer on the server, which is
  // the same reason the folder picker is hidden there.
  workspaceRevealBtn.style.display = app.canRevealWorkspace ? '' : 'none';
  workspaceRevealBtn.title = `open ${app.workspacePath} in a file-manager window`;
}

// setCurrentAgent updates session state and the header dropdown together —
// the two must never drift, so nothing sets one without the other.
export function setCurrentAgent(name) {
  session.currentAgent = name;
  if ([...agentSelectEl.options].some(o => o.value === name)) {
    agentSelectEl.value = name;
  }
}
