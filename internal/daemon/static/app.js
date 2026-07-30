(function () {
  const transcriptEl = document.getElementById('transcript');
  const tasksEl = document.getElementById('tasks');
  const sessionIdEl = document.getElementById('session-id');
  const inputEl = document.getElementById('input');
  const sendBtn = document.getElementById('send');
  const modalEl = document.getElementById('permission-modal');
  const permissionTextEl = document.getElementById('permission-text');
  const permissionAllowBtn = document.getElementById('permission-allow');
  const permissionAllowSessionBtn = document.getElementById('permission-allow-session');
  const permissionAllowAlwaysBtn = document.getElementById('permission-allow-always');
  const permissionDenyBtn = document.getElementById('permission-deny');
  const sessionListEl = document.getElementById('session-list');
  const newSessionBtn = document.getElementById('new-session-btn');
  const deleteAllSessionsBtn = document.getElementById('delete-all-sessions-btn');
  const agentSelectEl = document.getElementById('agent-select');
  const mcpServersEl = document.getElementById('mcp-servers');
  const statusTextEl = document.getElementById('status-text');
  const statusBarEl = document.getElementById('prompt-status');
  const commDotEl = document.getElementById('comm-dot');
  const autoDelegateBtn = document.getElementById('auto-delegate-btn');
  const delegateModal = document.getElementById('auto-delegate-modal');
  const delegateEnabledCheckbox = document.getElementById('delegate-enabled-checkbox');
  const delegateAgentSelect = document.getElementById('delegate-agent-select');
  const delegateMatchListEl = document.getElementById('delegate-match-list');
  const delegateMatchInput = document.getElementById('delegate-match-input');
  const delegateMatchAddBtn = document.getElementById('delegate-match-add');
  const delegateNote = document.getElementById('delegate-note');
  const delegateCloseBtn = document.getElementById('delegate-close');
  const permissionStatusBtn = document.getElementById('permission-status-btn');
  const permissionSettingsModal = document.getElementById('permission-settings-modal');
  const skipPermissionsCheckbox = document.getElementById('skip-permissions-checkbox');
  const permissionRulesListEl = document.getElementById('permission-rules-list');
  const ruleToolInput = document.getElementById('rule-tool-input');
  const ruleMatchInput = document.getElementById('rule-match-input');
  const ruleDecisionSelect = document.getElementById('rule-decision-select');
  const ruleAddBtn = document.getElementById('rule-add-btn');
  const permissionSettingsNote = document.getElementById('permission-settings-note');
  const permissionSettingsCloseBtn = document.getElementById('permission-settings-close');
  const workspaceBtn = document.getElementById('workspace-btn');
  const workspaceModal = document.getElementById('workspace-modal');
  const workspaceInput = document.getElementById('workspace-input');
  const workspaceNote = document.getElementById('workspace-note');
  const workspaceSaveBtn = document.getElementById('workspace-save');
  const workspaceCancelBtn = document.getElementById('workspace-cancel');

  let sessionID = null;
  let eventSource = null;
  let pendingPermissionID = null;
  let pendingPermissionCanAlways = false;
  let waiting = false;
  let tasks = new Map(); // task_id -> {agent, status}
  let agents = []; // [{name, description, model}]
  let currentAgent = null;
  let customCommands = []; // [{name, description}]
  let sessions = []; // cached list rendered in the aside
  let mcpServers = [];
  let lastUsage = null; // {input_tokens, output_tokens, max_context, percent, tps, show_tps, model}
  let workspacePath = '';
  let canBrowseWorkspace = false; // true only in the desktop-window mode
  const workspaceNoteDefault = 'Changing this restarts relative-path resolution for every tool from the new directory. Refused while a turn is in progress.';
  let skipPermissions = false;
  let permissionRules = {}; // tool -> [{match, decision}]
  let canEditPermissions = false; // false when the daemon has no config.json path to persist to
  let autoCompactEnabled = true;
  let showTPS = true;
  let autoDelegate = false;
  let autoDelegateAgent = ''; // '' when config.json has no auto_delegate block
  let autoDelegateMatch = []; // glob patterns that qualify a prompt for delegation
  // connected tracks the SSE stream to the daemon, which is this client's
  // only channel to the model: while it's down, nothing typed here can
  // reach one, so it's what the status light reports as "connected".
  let connected = false;
  let runningTool = ''; // tool currently executing, shown in the status bar
  let promptQueue = []; // plain prompts submitted while a turn is in flight
  // Up/Down prompt recall, mirroring the TUI. Client-side and in-memory:
  // a typing convenience, not session state.
  let history = [];              // submitted prompts, oldest first
  let historyIdx = 0;            // === history.length means "not navigating"
  let historyDraft = '';         // text stashed when recall started

  function appendLine(html) {
    transcriptEl.insertAdjacentHTML('beforeend', html);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // Model output streams as markdown, so it renders as one growing bubble
  // per model message rather than raw text nodes: currentModelEl is that
  // bubble, currentModelBuffer the raw markdown accumulated for it so far
  // (re-rendering the whole buffer per delta is what lets a construct that
  // only makes sense once complete, like a closing code fence, still
  // render correctly instead of needing incremental HTML patching).
  let currentModelEl = null;
  let currentModelBuffer = '';

  function appendModelText(text) {
    if (!currentModelEl) {
      currentModelEl = document.createElement('div');
      currentModelEl.className = 'msg-model';
      transcriptEl.appendChild(currentModelEl);
      currentModelBuffer = '';
    }
    currentModelBuffer += text;
    currentModelEl.innerHTML = renderMarkdown(currentModelBuffer);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  function endModelText() {
    currentModelEl = null;
    currentModelBuffer = '';
  }

  const defaultInputPlaceholder = inputEl.placeholder;

  // Locks the prompt box while a permission request is pending, so a typed
  // reply can never silently land in promptQueue instead of answering the
  // modal above — `waiting` stays true for the whole time the daemon is
  // blocked on that decision, which is otherwise indistinguishable from
  // "the model is still working" from this client's point of view.
  function setInputLocked(locked, hint) {
    inputEl.disabled = locked;
    sendBtn.disabled = locked;
    inputEl.placeholder = locked ? hint : defaultInputPlaceholder;
  }

  function setWaiting(v) {
    waiting = v;
    renderCommDot();
    if (!v) dequeueNext();
    renderStatusBar();
  }

  // renderCommDot draws the three-state light left of the status text:
  // gray when there's no live event stream to the daemon (so no path to
  // the model), solid green when there is, blinking green while a turn is
  // actually running.
  function renderCommDot() {
    commDotEl.classList.toggle('connected', connected);
    commDotEl.classList.toggle('active', connected && waiting);
    if (!connected) {
      commDotEl.title = 'not connected to the model (event stream is down)';
    } else if (waiting) {
      commDotEl.title = 'model is running your prompt';
    } else {
      commDotEl.title = 'connected to the model, idle';
    }
  }

  function setConnected(v) {
    if (connected === v) return;
    connected = v;
    renderCommDot();
  }

  // isPlainPrompt reports whether text is an ordinary chat message rather
  // than a "/"-prefixed command. Only plain prompts are safe to queue while
  // a turn is in flight — queueing a command would mean replaying it as
  // literal chat text to the model once dequeued, instead of running it.
  function isPlainPrompt(text) {
    return !text.startsWith('/');
  }

  function rememberPrompt(text) {
    if (history.length === 0 || history[history.length - 1] !== text) history.push(text);
    historyIdx = history.length;
    historyDraft = '';
  }

  // Recall only fires when the caret is already at the very start (Up) or
  // very end (Down) of the box, so arrows still move through a multi-line
  // prompt normally and only reach for history at the boundary.
  function atInputStart() {
    return inputEl.selectionStart === 0 && inputEl.selectionEnd === 0;
  }
  function atInputEnd() {
    const n = inputEl.value.length;
    return inputEl.selectionStart === n && inputEl.selectionEnd === n;
  }

  function setInputTo(text) {
    inputEl.value = text;
    autoResizeInput();
    const n = inputEl.value.length;
    inputEl.setSelectionRange(n, n);
  }

  function historyPrev() {
    if (history.length === 0 || historyIdx === 0) return false;
    if (historyIdx === history.length) historyDraft = inputEl.value;
    historyIdx--;
    setInputTo(history[historyIdx]);
    return true;
  }

  function historyNext() {
    if (historyIdx >= history.length) return false;
    historyIdx++;
    if (historyIdx === history.length) {
      setInputTo(historyDraft);
      historyDraft = '';
    } else {
      setInputTo(history[historyIdx]);
    }
    return true;
  }

  // cancelTurn stops the running turn. The transcript line and the cleared
  // spinner come from the turn.cancelled event the daemon broadcasts, so a
  // cancel from any client updates every client the same way.
  async function cancelTurn() {
    if (!waiting || !sessionID) return;
    // Drop the queue here rather than waiting for the event, so a second
    // Esc press cannot race an already queued prompt out the door.
    promptQueue = [];
    try {
      await api('POST', `/api/sessions/${sessionID}/cancel`, {});
    } catch (err) {
      appendLine(`<div class="msg-error">Error: ${escapeHtml(String(err))}</div>`);
    }
  }

  // dequeueNext sends the next queued prompt once the current turn has
  // actually finished (setWaiting(false) was just called) — the common
  // case for someone who kept typing while the model was still streaming a
  // reply. No-op if nothing is queued.
  function dequeueNext() {
    if (promptQueue.length === 0) return;
    const next = promptQueue.shift();
    setWaiting(true);
    api('POST', `/api/sessions/${sessionID}/messages`, { text: next }).catch((err) => {
      if (err && err.status === 409) {
        // Still busy — put it back and wait for the next turn.done.
        promptQueue.unshift(next);
        return;
      }
      setWaiting(false);
      appendLine(`<div class="msg-error">Error: ${escapeHtml(String(err))}</div>`);
    });
  }

  // renderStatusBar draws the single line below the prompt box: current
  // agent/model, context-window usage, tokens-per-second (if enabled), and
  // (via commDotEl, toggled from setWaiting) whether a turn is in flight.
  // modelForAgent returns the model configured for an agent, from the
  // /api/agents listing. It is what lets the status line name a model
  // before the first usage event of a session has arrived — otherwise a
  // freshly opened session shows no model at all until it answers once.
  function modelForAgent(name) {
    const a = agents.find(x => x.name === name);
    return (a && a.model) || '';
  }

  function renderStatusBar() {
    const parts = [];
    parts.push(`agent: ${currentAgent || '?'}`);
    // Prefer what the model actually reported over what config says it
    // should be; they differ when a profile is overridden server-side.
    const model = (lastUsage && lastUsage.model) || modelForAgent(currentAgent);
    if (model) parts.push(`model: ${model}`);
    if (lastUsage && typeof lastUsage.percent === 'number') {
      parts.push(`context: ${lastUsage.percent.toFixed(1)}%`);
    }
    if (showTPS && lastUsage && lastUsage.tps) {
      parts.push(`${lastUsage.tps.toFixed(1)} tok/s`);
    }
    if (waiting) {
      let busyText = runningTool ? `${runningTool}… esc to cancel` : 'working… esc to cancel';
      if (promptQueue.length > 0) busyText += ` (${promptQueue.length} queued)`;
      parts.push(busyText);
    }
    const activeTasks = [...tasks.values()].filter(t => t.status === 'spawned' || t.status === 'running').length;
    if (activeTasks > 0) parts.push(`${activeTasks} background task${activeTasks > 1 ? 's' : ''}`);
    statusTextEl.textContent = parts.join('  ·  ');

    statusBarEl.classList.remove('ctx-warn', 'ctx-crit');
    if (lastUsage && typeof lastUsage.percent === 'number') {
      if (lastUsage.percent >= 90) statusBarEl.classList.add('ctx-crit');
      else if (lastUsage.percent >= 70) statusBarEl.classList.add('ctx-warn');
    }
  }

  // renderPermissionStatus draws the small pill next to the status line:
  // "permissions: skip" (in warn color) when skip_permissions is on, or the
  // count of custom rules otherwise. Click opens the settings modal in
  // both the browser Web UI and the native GUI window (same page).
  function renderPermissionStatus() {
    const ruleCount = Object.values(permissionRules).reduce((n, rs) => n + rs.length, 0);
    permissionStatusBtn.classList.toggle('skip', skipPermissions);
    if (skipPermissions) {
      permissionStatusBtn.textContent = 'permissions: skip';
    } else if (ruleCount > 0) {
      permissionStatusBtn.textContent = `permissions: ask (${ruleCount} rule${ruleCount > 1 ? 's' : ''})`;
    } else {
      permissionStatusBtn.textContent = 'permissions: ask';
    }
  }

  // renderAutoDelegate draws the auto-delegation pill next to the
  // permission pill. Auto-delegation needs an auto_delegate block in
  // config.json to say which agent handles delegated prompts; with no such
  // block the setting can still be flipped but delegates nothing, so the
  // pill says so rather than showing a bare "on" that does nothing.
  function renderAutoDelegate() {
    autoDelegateBtn.classList.toggle('on', autoDelegate);
    if (autoDelegate && !autoDelegateAgent) {
      autoDelegateBtn.textContent = 'auto-delegate: on (unconfigured)';
      autoDelegateBtn.title = 'auto-delegate is on but no agent is chosen to answer delegated prompts, so nothing is delegated — click to configure it';
      return;
    }
    autoDelegateBtn.textContent = `auto-delegate: ${autoDelegate ? 'on' : 'off'}`;
    autoDelegateBtn.title = autoDelegateAgent
      ? `matching prompts ${autoDelegate ? 'go' : 'would go'} to the "${autoDelegateAgent}" agent (${autoDelegateMatch.length} pattern${autoDelegateMatch.length === 1 ? '' : 's'}) — click to configure`
      : 'click to choose which prompts are delegated and which agent answers them';
  }

  // saveAutoDelegate posts only the fields given, so the panel's three
  // controls each change one thing without restating the others. Every
  // change applies to the running loop immediately and is written to
  // config.json — see POST /api/settings/auto-delegate.
  async function saveAutoDelegate(patch) {
    try {
      await api('POST', '/api/settings/auto-delegate', patch);
    } catch (err) {
      delegateNote.textContent = String(err);
      delegateNote.classList.add('err');
      return false;
    }
    if (typeof patch.enabled === 'boolean') autoDelegate = patch.enabled;
    if (typeof patch.agent === 'string') autoDelegateAgent = patch.agent;
    if (Array.isArray(patch.match)) autoDelegateMatch = patch.match.slice();
    renderAutoDelegate();
    renderDelegatePanel();
    return true;
  }

  function openAutoDelegateSettings() {
    delegateNote.classList.remove('err');
    renderDelegatePanel();
    delegateModal.classList.add('open');
  }

  function renderDelegatePanel() {
    delegateEnabledCheckbox.checked = autoDelegate;

    // Only agents other than the one answering can be delegation targets:
    // delegating to the running agent would recurse, and the daemon refuses
    // it at turn time anyway, so it isn't offered here.
    delegateAgentSelect.innerHTML = '';
    const none = document.createElement('option');
    none.value = '';
    none.textContent = '(no agent chosen — nothing is delegated)';
    delegateAgentSelect.appendChild(none);
    for (const a of agents) {
      const opt = document.createElement('option');
      opt.value = a.name;
      opt.textContent = a.model ? `${a.name} (${a.model})` : a.name;
      delegateAgentSelect.appendChild(opt);
    }
    delegateAgentSelect.value = autoDelegateAgent || '';

    delegateMatchListEl.innerHTML = '';
    if (autoDelegateMatch.length === 0) {
      delegateMatchListEl.innerHTML = '<div class="note">No patterns yet, so nothing is delegated. Patterns are globs matched case-insensitively against the whole prompt: <code>*</code> is any run of characters, <code>?</code> is one.</div>';
    }
    for (const pattern of autoDelegateMatch) {
      const row = document.createElement('div');
      row.className = 'match-row';
      const text = document.createElement('span');
      text.className = 'match-text';
      text.textContent = pattern;
      row.appendChild(text);
      const removeBtn = document.createElement('button');
      removeBtn.textContent = 'remove';
      removeBtn.addEventListener('click', () => {
        saveAutoDelegate({ match: autoDelegateMatch.filter(p => p !== pattern) });
      });
      row.appendChild(removeBtn);
      delegateMatchListEl.appendChild(row);
    }

    if (!delegateNote.classList.contains('err')) {
      if (autoDelegate && !autoDelegateAgent) {
        delegateNote.textContent = 'On, but no agent is chosen, so nothing is delegated.';
      } else if (autoDelegate && autoDelegateMatch.length === 0) {
        delegateNote.textContent = 'On, but no pattern matches anything yet, so nothing is delegated.';
      } else {
        delegateNote.textContent = 'Changes apply to the next prompt and are written to config.json.';
      }
    }
  }

  function addDelegateMatch() {
    const pattern = delegateMatchInput.value.trim();
    if (!pattern || autoDelegateMatch.includes(pattern)) return;
    delegateNote.classList.remove('err');
    saveAutoDelegate({ match: autoDelegateMatch.concat([pattern]) }).then(ok => {
      if (ok) delegateMatchInput.value = '';
    });
  }

  function renderPermissionRulesList() {
    permissionRulesListEl.innerHTML = '';
    const tools = Object.keys(permissionRules).sort();
    if (tools.length === 0) {
      permissionRulesListEl.innerHTML = '<div class="note">No custom rules yet. Built-in defaults (e.g. git auto-allowed) aren\'t listed here.</div>';
      return;
    }
    for (const tool of tools) {
      for (const rule of permissionRules[tool]) {
        const row = document.createElement('div');
        row.className = 'rule-row';
        const text = document.createElement('span');
        text.className = 'rule-text';
        text.textContent = `${tool}: "${rule.match}" -> ${rule.decision}`;
        row.appendChild(text);
        if (canEditPermissions) {
          const removeBtn = document.createElement('button');
          removeBtn.textContent = 'remove';
          removeBtn.addEventListener('click', () => removePermissionRule(tool, rule));
          row.appendChild(removeBtn);
        }
        permissionRulesListEl.appendChild(row);
      }
    }
  }

  function openPermissionSettings() {
    skipPermissionsCheckbox.checked = skipPermissions;
    skipPermissionsCheckbox.disabled = !canEditPermissions;
    ruleToolInput.disabled = ruleMatchInput.disabled = ruleDecisionSelect.disabled = ruleAddBtn.disabled = !canEditPermissions;
    permissionSettingsNote.textContent = canEditPermissions
      ? 'Changes apply immediately and are written to config.json.'
      : 'No config.json path is available in this run, so settings are read-only.';
    renderPermissionRulesList();
    permissionSettingsModal.classList.add('open');
  }

  async function toggleSkipPermissions(enabled) {
    try {
      await api('POST', '/api/permissions/skip', { enabled });
      skipPermissions = enabled;
      renderPermissionStatus();
    } catch (err) {
      skipPermissionsCheckbox.checked = skipPermissions; // revert on failure
      permissionSettingsNote.textContent = `Failed to change: ${String(err)}`;
    }
  }

  async function addPermissionRule() {
    const tool = ruleToolInput.value.trim();
    const match = ruleMatchInput.value.trim();
    const decision = ruleDecisionSelect.value;
    if (!tool || !match) return;
    try {
      await api('POST', '/api/permissions/rules', { tool, match, decision });
      permissionRules[tool] = permissionRules[tool] || [];
      permissionRules[tool].push({ match, decision });
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
      await api('POST', '/api/permissions/rules/remove', { tool, match: rule.match, decision: rule.decision });
      permissionRules[tool] = (permissionRules[tool] || []).filter(r => !(r.match === rule.match && r.decision === rule.decision));
      if (permissionRules[tool].length === 0) delete permissionRules[tool];
      renderPermissionRulesList();
      renderPermissionStatus();
    } catch (err) {
      permissionSettingsNote.textContent = `Failed to remove rule: ${String(err)}`;
    }
  }

  // renderTasks builds each row with createElement/textContent rather than
  // an innerHTML template string — t.agent and t.status come straight from
  // SSE payloads (task.spawned/task.status), and this was the one listing
  // in the file that spliced such values into innerHTML unescaped.
  function renderTasks() {
    tasksEl.innerHTML = '';
    if (tasks.size === 0) {
      tasksEl.innerHTML = '<div style="color:var(--muted)">none</div>';
      return;
    }
    for (const [id, t] of tasks) {
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

  async function api(method, path, body) {
    const resp = await fetch(path, {
      method,
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!resp.ok) {
      const text = await resp.text().catch(() => '');
      const err = new Error(`${method} ${path}: ${resp.status} ${text}`);
      err.status = resp.status;
      throw err;
    }
    if (resp.status === 204) return null;
    const ct = resp.headers.get('content-type') || '';
    return ct.includes('application/json') ? resp.json() : null;
  }

  function applyEvent(ev) {
    switch (ev.type) {
      case 'message.user':
        if (ev.data && typeof ev.data.text === 'string') {
          appendLine(`<div class="msg-user">You: ${escapeHtml(ev.data.text)}</div>`);
        }
        break;
      case 'message.part.delta':
        if (ev.data && typeof ev.data.text === 'string') appendModelText(ev.data.text);
        break;
      case 'message.part.end':
        // One model message ended, NOT the turn — a turn with tool calls
        // streams several of these. Ending the wait here is what used to
        // make a prompt typed during tool execution skip the queue and
        // bounce off the daemon's busy flag with a 409.
        endModelText();
        break;
      case 'turn.done':
        // The daemon's real turn boundary, emitted after its busy flag is
        // cleared — safe to stop waiting and let the queue drain.
        runningTool = '';
        setWaiting(false);
        break;
      case 'tool.start':
        // No transcript line — tool activity shows in the status bar
        // under the prompt while it runs, and vanishes with the turn.
        runningTool = ev.data.name || '';
        renderStatusBar();
        break;
      case 'tool.end':
        runningTool = '';
        renderStatusBar();
        break;
      case 'permission.request':
        pendingPermissionID = ev.data.id;
        pendingPermissionCanAlways = !!ev.data.can_always;
        permissionTextEl.textContent = `[${ev.data.tool}] ${ev.data.description || '(no description given)'}`;
        permissionAllowAlwaysBtn.style.display = pendingPermissionCanAlways ? '' : 'none';
        if (pendingPermissionCanAlways) {
          permissionAllowAlwaysBtn.title = `don't ask again — writes "${ev.data.rule}" to config.json`;
        }
        modalEl.classList.add('open');
        setInputLocked(true, 'Resolve the permission request above to continue.');
        break;
      case 'permission.resolved':
        modalEl.classList.remove('open');
        setInputLocked(false);
        break;
      case 'task.spawned':
        // Sidebar + status bar carry this; no transcript line.
        tasks.set(ev.data.task_id, { agent: ev.data.agent, status: 'spawned' });
        renderTasks();
        renderStatusBar();
        break;
      case 'task.status':
        if (tasks.has(ev.data.task_id)) tasks.get(ev.data.task_id).status = ev.data.status;
        else tasks.set(ev.data.task_id, { agent: '', status: ev.data.status });
        renderTasks();
        renderStatusBar();
        break;
      case 'agent.switched':
        // Just update the state the status bar already renders every time
        // — no transcript line here. setCurrentAgent + renderStatusBar
        // already reflect the new agent; writing a line here too would
        // leave a permanent "switched to X" entry in the transcript on
        // every single switch.
        setCurrentAgent(ev.data.agent);
        renderStatusBar();
        break;
      case 'usage':
        lastUsage = ev.data;
        renderStatusBar();
        break;
      case 'compacted':
        appendLine(`<div class="msg-tool">[system] conversation compacted to save context (summary: ${ev.data.summary_length || 0} chars).</div>`);
        break;
      case 'config.changed':
        if (typeof ev.data.auto_compact_enabled === 'boolean') autoCompactEnabled = ev.data.auto_compact_enabled;
        if (typeof ev.data.show_tps === 'boolean') showTPS = ev.data.show_tps;
        if (typeof ev.data.auto_delegate === 'boolean') {
          autoDelegate = ev.data.auto_delegate;
          renderAutoDelegate();
          if (delegateModal.classList.contains('open')) renderDelegatePanel();
        }
        renderStatusBar();
        break;
      case 'session.renamed':
        loadSessions();
        break;
      case 'delegated':
        appendLine(`<div class="msg-tool">[delegated to ${escapeHtml(ev.data.agent || '')}]</div>`);
        break;
      case 'turn.cancelled':
        promptQueue = [];
        runningTool = '';
        setWaiting(false);
        appendLine(`<div class="msg-tool">[cancelled]</div>`);
        break;
      case 'error':
        runningTool = '';
        setWaiting(false);
        appendLine(`<div class="msg-error">Error: ${escapeHtml(ev.data.error || '')}</div>`);
        break;
    }
  }

  async function loadAgents() {
    try {
      agents = await api('GET', '/api/agents');
    } catch (err) {
      agents = [];
    }
    agentSelectEl.innerHTML = '';
    for (const a of agents) {
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
    if (currentAgent) agentSelectEl.value = currentAgent;
  }

  // cycleAgent switches to the next (or previous) agent in the dropdown,
  // the Web UI counterpart of the TUI's Tab key. The switch itself goes
  // through the daemon, so currentAgent updates from the agent.switched
  // event like every other client's does.
  async function cycleAgent(step) {
    if (!sessionID || agents.length < 2) return;
    const idx = agents.findIndex(a => a.name === currentAgent);
    const next = agents[((idx < 0 ? 0 : idx) + step + agents.length) % agents.length];
    if (!next || next.name === currentAgent) return;
    try {
      await api('POST', `/api/sessions/${sessionID}/agent`, { agent: next.name });
    } catch (err) {
      appendLine(`<div class="msg-error">failed to switch agent: ${escapeHtml(String(err))}</div>`);
    }
  }

  async function loadCommands() {
    try {
      customCommands = await api('GET', '/api/commands');
    } catch (err) {
      customCommands = [];
    }
  }

  async function loadSettings() {
    try {
      const s = await api('GET', '/api/settings');
      autoCompactEnabled = s.auto_compact_enabled;
      showTPS = s.show_tps;
      autoDelegate = !!s.auto_delegate;
      autoDelegateAgent = s.auto_delegate_agent || '';
      autoDelegateMatch = s.auto_delegate_match || [];
      skipPermissions = !!s.skip_permissions;
      permissionRules = s.permission_rules || {};
      canEditPermissions = !!s.can_edit_permissions;
    } catch (err) {
      // keep defaults
    }
    renderStatusBar();
    renderPermissionStatus();
    renderAutoDelegate();
  }

  async function loadWorkspace() {
    try {
      const w = await api('GET', '/api/workspace');
      workspacePath = w.path || '';
      canBrowseWorkspace = !!w.can_browse;
    } catch (err) {
      workspacePath = '';
      canBrowseWorkspace = false;
    }
    renderWorkspace();
  }

  function renderWorkspace() {
    workspaceBtn.textContent = workspacePath || '(unknown workspace)';
    workspaceBtn.title = canBrowseWorkspace
      ? `${workspacePath}\nclick to pick a workspace folder`
      : `${workspacePath}\nclick to change the workspace directory`;
  }

  // The desktop window can open the OS folder picker, so clicking the
  // workspace goes straight to it rather than to a box you have to type an
  // absolute path into. A browser can't: neither <input webkitdirectory>
  // nor showDirectoryPicker() hands back a real filesystem path, and asking
  // the *daemon* to open a dialog only makes sense when it's on the same
  // machine as the screen — so that case keeps the typed-path modal.
  function openWorkspacePicker() {
    if (canBrowseWorkspace) browseWorkspace();
    else openWorkspaceModal();
  }

  async function browseWorkspace() {
    let picked;
    try {
      picked = await api('POST', '/api/workspace/browse', { start: workspacePath });
    } catch (err) {
      // The picker itself failed (not a cancel) — fall back to typing,
      // rather than leaving the click doing nothing at all.
      appendLine(`<div class="msg-error">could not open the folder picker: ${escapeHtml(String(err))}</div>`);
      openWorkspaceModal();
      return;
    }
    if (!picked || !picked.path) return; // 204: dialog dismissed, nothing to do
    await applyWorkspace(picked.path);
  }

  // applyWorkspace switches the daemon's working directory and reports the
  // outcome in the transcript, since this changes where every later tool
  // call and bash command resolves from — too consequential to happen
  // silently. Returns whether it took effect.
  async function applyWorkspace(path) {
    if (path === workspacePath) return true;
    try {
      const w = await api('POST', '/api/workspace', { path });
      workspacePath = w.path;
      renderWorkspace();
      appendLine(`<div class="msg-tool">[workspace] ${escapeHtml(w.path)}</div>`);
      return true;
    } catch (err) {
      appendLine(`<div class="msg-error">could not switch the workspace to ${escapeHtml(path)}: ${escapeHtml(String(err))}</div>`);
      return false;
    }
  }

  function openWorkspaceModal() {
    workspaceInput.value = workspacePath;
    workspaceNote.textContent = workspaceNoteDefault;
    workspaceNote.classList.remove('err');
    workspaceModal.classList.add('open');
    workspaceInput.focus();
  }

  async function saveWorkspace() {
    const path = workspaceInput.value.trim();
    if (!path || path === workspacePath) {
      workspaceModal.classList.remove('open');
      return;
    }
    try {
      const w = await api('POST', '/api/workspace', { path });
      workspacePath = w.path;
      renderWorkspace();
      workspaceModal.classList.remove('open');
    } catch (err) {
      // Stays open with the error inline, so a typo can be corrected
      // without retyping the whole path.
      workspaceNote.textContent = String(err);
      workspaceNote.classList.add('err');
    }
  }

  async function loadMCPServers() {
    try {
      mcpServers = await api('GET', '/api/mcp-servers');
    } catch (err) {
      mcpServers = [];
    }
    renderMCPServers();
  }

  function renderMCPServers() {
    mcpServersEl.innerHTML = '';
    if (!mcpServers || mcpServers.length === 0) {
      mcpServersEl.innerHTML = '<div style="color:var(--muted)">no connected servers</div>';
      return;
    }
    for (const name of mcpServers) {
      const div = document.createElement('div');
      div.className = 'mcp-item';
      div.textContent = name;
      mcpServersEl.appendChild(div);
    }
  }

  function setCurrentAgent(name) {
    currentAgent = name;
    if ([...agentSelectEl.options].some(o => o.value === name)) {
      agentSelectEl.value = name;
    }
  }

  agentSelectEl.addEventListener('change', async () => {
    const name = agentSelectEl.value;
    if (!sessionID || name === currentAgent) return;
    try {
      await api('POST', `/api/sessions/${sessionID}/agent`, { agent: name });
      // currentAgent updates from the agent.switched event this call
      // causes the daemon to broadcast — see applyEvent — not here, so
      // every client (including this one) reacts the same way.
    } catch (err) {
      agentSelectEl.value = currentAgent; // revert the dropdown on failure
      appendLine(`<div class="msg-error">failed to switch agent: ${escapeHtml(String(err))}</div>`);
    }
  });

  function escapeHtml(s) {
    return s.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  // renderMarkdown turns model output into safe HTML for display. It is
  // deliberately small (no dependency, this is a fully offline app) and
  // covers what models actually produce in practice: fenced/inline code,
  // headers, bold/italic, links, block quotes, lists, and a rule. It is
  // re-run on the full buffer for every streamed delta, so it also has to
  // tolerate a mid-render document (an unclosed fence, a half-typed link)
  // without throwing or leaking unescaped input — every code path below
  // escapes raw text before it is placed in the output, never after.
  function renderMarkdown(src) {
    // 1. Pull out fenced code blocks first so nothing else touches their
    // contents, replacing each with a placeholder token to splice back in
    // at the end. An unterminated trailing fence (still streaming) is
    // rendered as-is rather than left as literal ``` markers.
    //
    // The placeholder uses U+0000 (a control character no model output
    // will ever contain) rather than bare digits — a plain " 3 " collided
    // with any model text that happened to contain a bare number after a
    // code block, silently splicing the wrong block (or the literal string
    // "undefined") into the rendered output.
    const blocks = [];
    const placeholder = (i) => `\u0000${i}\u0000`;
    let text = src.replace(/```([^\n`]*)\n([\s\S]*?)(```|$)/g, (_, lang, code) => {
      const cls = lang.trim() ? ` class="language-${escapeHtml(lang.trim())}"` : '';
      const html = `<pre><code${cls}>${escapeHtml(code.replace(/\n$/, ''))}</code></pre>`;
      blocks.push(html);
      return placeholder(blocks.length - 1);
    });

    // 2. Escape everything else as plain text now, before any markup is
    // introduced, so nothing the model wrote can inject an element.
    text = escapeHtml(text);

    // 3. Inline code spans.
    text = text.replace(/`([^`\n]+)`/g, (_, code) => `<code>${code}</code>`);

    // 4. Block-level constructs, line by line: headers, quotes, rules,
    // and list runs (consecutive - / * / N. lines become one <ul>/<ol>).
    const lines = text.split('\n');
    const out = [];
    let listTag = null; // 'ul' | 'ol' | null — the list currently open
    const closeList = () => { if (listTag) { out.push(`</${listTag}>`); listTag = null; } };
    for (const line of lines) {
      const h = line.match(/^(#{1,6})\s+(.*)$/);
      const bullet = line.match(/^[-*]\s+(.*)$/);
      const numbered = line.match(/^\d+\.\s+(.*)$/);
      const quote = line.match(/^&gt;\s?(.*)$/);
      const isPlaceholder = /^\u0000\d+\u0000$/.test(line);
      if (isPlaceholder) {
        // A spliced-in code block: pass the line through untouched rather
        // than wrapping it in <p>, which would nest <pre> inside <p>.
        closeList();
        out.push(line);
      } else if (h) {
        closeList();
        out.push(`<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`);
      } else if (/^(-{3,}|\*{3,})$/.test(line.trim())) {
        closeList();
        out.push('<hr>');
      } else if (bullet) {
        if (listTag !== 'ul') { closeList(); out.push('<ul>'); listTag = 'ul'; }
        out.push(`<li>${inline(bullet[1])}</li>`);
      } else if (numbered) {
        if (listTag !== 'ol') { closeList(); out.push('<ol>'); listTag = 'ol'; }
        out.push(`<li>${inline(numbered[1])}</li>`);
      } else if (quote) {
        closeList();
        out.push(`<blockquote>${inline(quote[1])}</blockquote>`);
      } else if (line.trim() === '') {
        closeList();
        out.push('');
      } else {
        closeList();
        out.push(`<p>${inline(line)}</p>`);
      }
    }
    closeList();
    text = out.join('\n');

    // 5. Splice the fenced code blocks back in.
    text = text.replace(/\u0000(\d+)\u0000/g, (_, i) => blocks[+i]);
    return text;
  }

  // inline applies span-level markdown (bold, italic, links) to text that
  // has already been through escapeHtml — it only ever matches the plain
  // characters left behind (*, _, [, ], (, )), never entities.
  function inline(s) {
    return s
      .replace(/\*\*([^*]+)\*\*|__([^_]+)__/g, (_, a, b) => `<strong>${a || b}</strong>`)
      .replace(/\*([^*]+)\*|_([^_]+)_/g, (_, a, b) => `<em>${a || b}</em>`)
      .replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, (_, t, u) => `<a href="${u}" target="_blank" rel="noopener noreferrer">${t}</a>`);
  }

  function connectEvents() {
    if (eventSource) eventSource.close();
    setConnected(false);
    eventSource = new EventSource(`/api/sessions/${sessionID}/events`);
    eventSource.onopen = () => setConnected(true);
    eventSource.onmessage = (e) => {
      // An event arriving is itself proof the stream is up, which matters
      // because onopen doesn't fire again after an auto-reconnect in every
      // browser.
      setConnected(true);
      try { applyEvent(JSON.parse(e.data)); } catch (err) { console.error('bad event', err); }
    };
    eventSource.onerror = () => {
      // EventSource auto-reconnects using Last-Event-ID, so there's nothing
      // to do but show the light as down until it comes back.
      setConnected(false);
    };
  }

  function formatTime(iso) {
    try {
      const d = new Date(iso);
      return d.toLocaleString();
    } catch { return iso; }
  }

  async function loadSessions() {
    try {
      sessions = await api('GET', '/api/sessions');
    } catch (err) {
      sessions = [];
    }
    renderSessionList();
  }

  // shortenPath trims a long path from the front, keeping the tail — the
  // project directory, which is what identifies the session — and dropping
  // the leading directories, which for most people are the same on every
  // row. The full path stays in the title attribute.
  function shortenPath(path, max = 32) {
    if (path.length <= max) return path;
    const tail = path.slice(-(max - 1));
    const cut = tail.indexOf('/');
    // Prefer starting at a path separator so the result reads as a path
    // rather than a word chopped in half.
    return '…' + (cut > 0 && cut < 8 ? tail.slice(cut) : tail);
  }

  function renderSessionList() {
    sessionListEl.innerHTML = '';
    if (!sessions || sessions.length === 0) {
      sessionListEl.innerHTML = '<div style="color:var(--muted)">no sessions</div>';
      return;
    }
    for (const s of sessions) {
      const div = document.createElement('div');
      div.className = 'session-item' + (s.id === sessionID ? ' active' : '');
      // The whole card switches to the session — the old dedicated "switch"
      // button made the single most common action the smallest target on
      // the row. The rename/delete buttons below stop propagation so they
      // don't switch as a side effect of being clicked.
      div.title = `${s.id}\nclick to switch to this session`;
      div.addEventListener('click', () => {
        if (s.id !== sessionID) selectSession(s.id, s.agent, s.workspace);
      });

      const title = document.createElement('div');
      title.className = 'title';
      title.textContent = s.title ? s.title : s.id;
      div.appendChild(title);

      // Which project a conversation belongs to is the thing that
      // distinguishes otherwise identical sessions, so it's shown here
      // instead of the agent name (which the header dropdown and the
      // status line under the prompt both already carry).
      const workspace = document.createElement('div');
      workspace.className = 'workspace';
      workspace.textContent = s.workspace ? shortenPath(s.workspace) : '(workspace not recorded)';
      workspace.title = s.workspace || 'this session predates workspace tracking';
      div.appendChild(workspace);

      const meta = document.createElement('div');
      meta.className = 'meta';
      meta.textContent = formatTime(s.created_at);
      div.appendChild(meta);

      const actions = document.createElement('div');
      actions.className = 'actions';

      const renameBtn = document.createElement('button');
      renameBtn.textContent = 'rename';
      renameBtn.addEventListener('click', (e) => { e.stopPropagation(); renameSession(s); });
      actions.appendChild(renameBtn);

      const delBtn = document.createElement('button');
      delBtn.textContent = 'delete';
      delBtn.className = 'danger-btn';
      delBtn.addEventListener('click', (e) => { e.stopPropagation(); deleteSession(s); });
      actions.appendChild(delBtn);

      div.appendChild(actions);
      sessionListEl.appendChild(div);
    }
  }

  async function renameSession(s) {
    const newTitle = window.prompt('New session name:', s.title || '');
    if (newTitle === null) return;
    try {
      await api('POST', `/api/sessions/${s.id}/rename`, { title: newTitle });
      await loadSessions();
    } catch (err) {
      appendLine(`<div class="msg-error">failed to rename: ${escapeHtml(String(err))}</div>`);
    }
  }

  async function deleteSession(s) {
    if (!window.confirm(`Delete session "${s.title || s.id}"? This cannot be undone.`)) return;
    try {
      await api('DELETE', `/api/sessions/${s.id}`);
    } catch (err) {
      appendLine(`<div class="msg-error">failed to delete session: ${escapeHtml(String(err))}</div>`);
      return;
    }
    if (s.id === sessionID) {
      await loadSessions();
      if (sessions.length > 0) {
        selectSession(sessions[0].id, sessions[0].agent, sessions[0].workspace);
      } else {
        await createNewSession();
      }
    } else {
      await loadSessions();
    }
  }

  // selectSession switches the UI to a session and, if that session was
  // started somewhere else, moves the daemon's workspace to match — so
  // opening a conversation about another project actually puts you back in
  // that project rather than leaving its old transcript pointed at the
  // current directory. workspace is that session's recorded directory;
  // sessions from before the field existed have none, and those leave the
  // workspace alone rather than guessing.
  function selectSession(id, agent, workspace) {
    sessionID = id;
    sessionIdEl.textContent = sessionID;
    transcriptEl.innerHTML = '';
    currentModelEl = null;
    currentModelBuffer = '';
    tasks = new Map();
    renderTasks();
    lastUsage = null;
    // A queued prompt belongs to the session it was typed in — carrying it
    // over to whatever session is selected next would send it somewhere
    // the user never intended. Prompt recall history goes with it, for the
    // same reason: it is that conversation's history, not the window's.
    promptQueue = [];
    history = [];
    historyIdx = 0;
    historyDraft = '';
    setWaiting(false);
    pendingPermissionID = null;
    modalEl.classList.remove('open');
    setInputLocked(false);
    setCurrentAgent(agent);
    renderStatusBar(); // the new session's agent/model, before any event arrives
    renderSessionList();
    connectEvents();
    // Deliberately not awaited: the session is already switched and its
    // transcript already replaying. The workspace change lands a moment
    // later and announces itself in the transcript, and if it's refused
    // (another session mid-turn) that's reported without having blocked
    // the switch the user actually asked for.
    if (workspace) applyWorkspace(workspace);
  }

  async function createNewSession() {
    try {
      const sess = await api('POST', '/api/sessions', { agent: 'general-purpose' });
      await loadSessions();
      selectSession(sess.id, sess.agent, sess.workspace);
    } catch (err) {
      sessionIdEl.textContent = 'error';
      appendLine(`<div class="msg-error">failed to create session: ${escapeHtml(String(err))}</div>`);
    }
  }

  async function deleteAllSessions() {
    if (!window.confirm('Delete ALL sessions? This cannot be undone.')) return;
    try {
      await api('DELETE', '/api/sessions');
    } catch (err) {
      appendLine(`<div class="msg-error">failed to delete all sessions: ${escapeHtml(String(err))}</div>`);
      return;
    }
    await createNewSession();
  }

  newSessionBtn.addEventListener('click', createNewSession);
  deleteAllSessionsBtn.addEventListener('click', deleteAllSessions);

  // Every line here is raw text, escaped exactly once (by tryLocalCommand,
  // where the whole block goes through escapeHtml() before display) — none
  // of it is pre-escaped. A previous version pre-escaped one entry
  // ("/<skill name>") while leaving the others raw, so that one line was
  // escaped twice and rendered literally as "&lt;skill name&gt;" instead
  // of "<skill name>".
  const HELP_TEXT = [
    'Available commands:',
    '  /help              show this help',
    '  /version            show the daemon version',
    '  /skill              list registered skills',
    '  /<skill name>        run that skill (e.g. /pdf-tools)',
    '  Esc                 cancel the running turn',
    '  Tab / Shift+Tab      switch to the next/previous agent',
    '  /agent              list registered agents',
    '  /agent <name>        switch to that agent (also available via the header dropdown)',
    '  /init              scan the repo and create/improve an AGENTS.md rules file',
    '  /memory            show the auto memory directory/index (MEMORY.md)',
    '  /config            show current settings (auto_compact, show_tps, auto_delegate)',
    '  /config auto_compact on|off   toggle auto-compaction above 80% context usage',
    '  /config show_tps on|off       toggle the tokens/sec display under the prompt',
    '  /config auto_delegate on|off  send matching prompts to a cheaper sub-agent',
    '                        (the pill under the prompt box also sets the target agent and patterns)',
    '  /compact           summarize and compact the conversation right now',
    '  /compact <instructions>      give instructions for how to compact',
    '  /usage              show cumulative token usage per model',
    '  /commands          list registered custom commands',
    '  /<custom command>   run a command defined in .localcode/commands/*.md',
    '  exit, :q            just show a message (close the browser tab yourself)',
    '',
    'You can drag a file onto the input box to attach it.',
  ].join('\n');

  // Commands handled entirely client-side — never touch the session's
  // event log, so they don't show up again on session replay. Returns
  // true if `text` was a local command (and thus already handled).
  async function tryLocalCommand(text) {
    const lower = text.toLowerCase();

    if (lower === 'exit' || lower === ':q') {
      appendLine(`<div class="msg-user">You: ${escapeHtml(text)}</div>`);
      appendLine(`<div class="msg-tool">Closing the browser tab ends the session (the Web UI can't quit the program directly).</div>`);
      return true;
    }

    if (lower === '/help') {
      appendLine(`<div class="msg-user">You: ${escapeHtml(text)}</div>`);
      appendLine(`<div class="msg-tool">${escapeHtml(HELP_TEXT).replace(/\n/g, '<br>')}</div>`);
      return true;
    }

    if (lower === '/version') {
      appendLine(`<div class="msg-user">You: ${escapeHtml(text)}</div>`);
      try {
        const v = await api('GET', '/api/version');
        appendLine(`<div class="msg-tool">localcode ${escapeHtml(v.version)}</div>`);
      } catch (err) {
        appendLine(`<div class="msg-error">failed to fetch version: ${escapeHtml(String(err))}</div>`);
      }
      return true;
    }

    if (lower === '/agent') {
      appendLine(`<div class="msg-user">You: ${escapeHtml(text)}</div>`);
      if (agents.length === 0) {
        appendLine(`<div class="msg-tool">No agents registered.</div>`);
      } else {
        const lines = [`Available agents (/agent &lt;name&gt; to switch, current: ${escapeHtml(currentAgent || '')}):`]
          .concat(agents.map(a => `- ${escapeHtml(a.name)}: ${escapeHtml(a.description || '')}`));
        appendLine(`<div class="msg-tool">${lines.join('<br>')}</div>`);
      }
      return true;
    }

    if (lower === '/commands') {
      appendLine(`<div class="msg-user">You: ${escapeHtml(text)}</div>`);
      if (customCommands.length === 0) {
        appendLine(`<div class="msg-tool">No custom commands registered. (add one under .localcode/commands/*.md)</div>`);
      } else {
        const lines = ['Available custom commands:']
          .concat(customCommands.map(c => `- /${escapeHtml(c.name)}: ${escapeHtml(c.description || '')}`));
        appendLine(`<div class="msg-tool">${lines.join('<br>')}</div>`);
      }
      return true;
    }

    const agentMatch = text.match(/^\/agent\s+(\S+)/);
    if (agentMatch) {
      appendLine(`<div class="msg-user">You: ${escapeHtml(text)}</div>`);
      try {
        await api('POST', `/api/sessions/${sessionID}/agent`, { agent: agentMatch[1] });
      } catch (err) {
        appendLine(`<div class="msg-error">failed to switch agent: ${escapeHtml(String(err))}</div>`);
      }
      return true;
    }

    // "/config" itself is handled server-side (agent.Loop) like /memory —
    // not intercepted here, so it falls through to sendMessage() below and
    // its response/config.changed event flow back over SSE like anything
    // else the model or a local command answers.
    return false;
  }

  async function sendMessage() {
    const text = inputEl.value.trim();
    if (!text) return;

    // A turn is already streaming: queue a plain prompt so it sends
    // automatically the moment the current one finishes, instead of
    // silently doing nothing and making the user remember to retype it.
    // Commands still wait for the turn to finish first — they don't go
    // through the /messages endpoint, so queueing them would mean
    // replaying them as literal chat text later.
    if (waiting) {
      if (isPlainPrompt(text)) {
        promptQueue.push(text);
        appendLine(`<div class="msg-tool">[queued] ${escapeHtml(text)}</div>`);
        renderStatusBar();
        rememberPrompt(text);
        inputEl.value = '';
        autoResizeInput();
      }
      return;
    }

    rememberPrompt(text);
    inputEl.value = '';
    autoResizeInput();

    if (await tryLocalCommand(text)) return;

    // The user line renders from the message.user event (see applyEvent),
    // not optimistically here, so a resumed/replayed session shows the
    // same transcript a live one did.
    setWaiting(true);
    try {
      await api('POST', `/api/sessions/${sessionID}/messages`, { text });
    } catch (err) {
      if (err && err.status === 409) {
        // The daemon already has a turn running (a race window, or a turn
        // another client started). Queue material, not an error: the
        // running turn's turn.done will drain it. waiting stays true so
        // further prompts queue too.
        promptQueue.unshift(text);
        appendLine(`<div class="msg-tool">[queued] ${escapeHtml(text)}</div>`);
        renderStatusBar();
        return;
      }
      setWaiting(false);
      appendLine(`<div class="msg-error">Error: ${escapeHtml(String(err))}</div>`);
    }
  }

  function autoResizeInput() {
    inputEl.style.height = 'auto';
    inputEl.style.height = inputEl.scrollHeight + 'px';
  }

  function insertAtCursor(el, text) {
    const start = el.selectionStart ?? el.value.length;
    const end = el.selectionEnd ?? el.value.length;
    el.value = el.value.slice(0, start) + text + el.value.slice(end);
    const pos = start + text.length;
    el.selectionStart = el.selectionEnd = pos;
    el.focus();
  }

  // uploadFile posts one dropped file to the daemon and returns its
  // absolute server-side path — the model reads it with its own file
  // tools; there's no separate "attachment" wire concept.
  async function uploadFile(file) {
    const form = new FormData();
    form.append('file', file, file.name);
    const resp = await fetch(`/api/sessions/${sessionID}/uploads`, { method: 'POST', body: form });
    if (!resp.ok) {
      const text = await resp.text().catch(() => '');
      throw new Error(`${resp.status} ${text}`);
    }
    const data = await resp.json();
    return data.path;
  }

  inputEl.addEventListener('dragover', (e) => {
    e.preventDefault();
    inputEl.classList.add('drag-over');
  });
  inputEl.addEventListener('dragleave', () => inputEl.classList.remove('drag-over'));
  inputEl.addEventListener('drop', async (e) => {
    e.preventDefault();
    inputEl.classList.remove('drag-over');
    const files = e.dataTransfer && e.dataTransfer.files;
    if (!files || files.length === 0 || !sessionID) return;
    for (const file of files) {
      try {
        const path = await uploadFile(file);
        insertAtCursor(inputEl, `[attached file: ${path}]\n`);
      } catch (err) {
        appendLine(`<div class="msg-error">upload failed (${escapeHtml(file.name)}): ${escapeHtml(String(err))}</div>`);
      }
    }
    autoResizeInput();
  });

  sendBtn.addEventListener('click', sendMessage);
  inputEl.addEventListener('input', autoResizeInput);
  inputEl.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendMessage();
    } else if (e.key === 'Escape') {
      e.preventDefault();
      cancelTurn();
    } else if (e.key === 'ArrowUp' && atInputStart()) {
      if (historyPrev()) e.preventDefault();
    } else if (e.key === 'ArrowDown' && atInputEnd()) {
      if (historyNext()) e.preventDefault();
    }
  });
  function anyModalOpen() {
    return modalEl.classList.contains('open')
      || permissionSettingsModal.classList.contains('open')
      || delegateModal.classList.contains('open')
      || workspaceModal.classList.contains('open');
  }

  // Esc works even when focus is not in the prompt box, matching the TUI.
  //
  // Tab does too, and for the same reason: in the TUI it cycles the agent,
  // and someone moving between the two shouldn't find that the same key
  // instead walks the focus ring around the page. preventDefault is what
  // takes it back from the browser. Tab still does its normal job inside a
  // modal and in the modals' own fields, where moving between inputs is
  // the only thing it could reasonably mean.
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && e.target !== inputEl && !modalEl.classList.contains('open')) {
      cancelTurn();
      return;
    }
    if (e.key === 'Tab' && !e.ctrlKey && !e.metaKey && !e.altKey && !anyModalOpen()) {
      const inOtherField = e.target !== inputEl
        && e.target !== document.body
        && (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' || e.target.tagName === 'SELECT');
      if (inOtherField) return;
      e.preventDefault();
      cycleAgent(e.shiftKey ? -1 : 1);
    }
  });

  permissionAllowBtn.addEventListener('click', () => resolvePermission(true, 'once'));
  permissionAllowSessionBtn.addEventListener('click', () => resolvePermission(true, 'session'));
  permissionAllowAlwaysBtn.addEventListener('click', () => resolvePermission(true, 'always'));
  permissionDenyBtn.addEventListener('click', () => resolvePermission(false, 'once'));

  autoDelegateBtn.addEventListener('click', openAutoDelegateSettings);
  delegateCloseBtn.addEventListener('click', () => delegateModal.classList.remove('open'));
  delegateEnabledCheckbox.addEventListener('change', () => {
    delegateNote.classList.remove('err');
    saveAutoDelegate({ enabled: delegateEnabledCheckbox.checked });
  });
  delegateAgentSelect.addEventListener('change', () => {
    delegateNote.classList.remove('err');
    saveAutoDelegate({ agent: delegateAgentSelect.value });
  });
  delegateMatchAddBtn.addEventListener('click', addDelegateMatch);
  delegateMatchInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); addDelegateMatch(); }
  });
  permissionStatusBtn.addEventListener('click', openPermissionSettings);
  permissionSettingsCloseBtn.addEventListener('click', () => permissionSettingsModal.classList.remove('open'));
  skipPermissionsCheckbox.addEventListener('change', () => toggleSkipPermissions(skipPermissionsCheckbox.checked));
  ruleAddBtn.addEventListener('click', addPermissionRule);

  workspaceBtn.addEventListener('click', openWorkspacePicker);
  workspaceCancelBtn.addEventListener('click', () => workspaceModal.classList.remove('open'));
  workspaceSaveBtn.addEventListener('click', saveWorkspace);
  workspaceInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); saveWorkspace(); }
  });

  // resolvePermission answers a pending permission request. scope is
  // 'once', 'session' (don't ask again this session), or 'always' (don't
  // ask again ever — the daemon writes a matching rule to config.json).
  // The policy change itself happens server-side; this only reports what
  // the user chose.
  async function resolvePermission(allow, scope) {
    if (!pendingPermissionID) return;
    const id = pendingPermissionID;
    pendingPermissionID = null;
    modalEl.classList.remove('open');
    try {
      await api('POST', `/api/sessions/${sessionID}/permissions/${id}`, { allow, scope });
    } catch (err) {
      appendLine(`<div class="msg-error">failed to respond to permission request: ${escapeHtml(String(err))}</div>`);
    }
  }

  async function init() {
    renderTasks();
    await loadAgents();
    await loadCommands();
    await loadSettings();
    await loadWorkspace();
    await loadMCPServers();
    await loadSessions();

    if (!sessions || sessions.length === 0) {
      await createNewSession();
    } else {
      selectSession(sessions[0].id, sessions[0].agent, sessions[0].workspace);
    }
  }

  init();
})();
