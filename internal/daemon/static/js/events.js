import { permissionTextEl, permissionAllowAlwaysBtn } from './dom.js';
import { app, session } from './state.js';
import { appendUser, appendTool, appendError, appendModelText, endModelText } from './transcript.js';
import { renderStatusBar, renderTasks, setCurrentAgent, renderAutoDelegate } from './render.js';
import { setWaiting, setConnected, setInputLocked } from './composer.js';
import { refreshDelegatePanelIfOpen, permissionRequest } from './modals.js';
// events.js and sessions.js import each other (session.renamed reloads the
// session list; selectSession opens the event stream). Both references are
// only ever called from inside a function body, never read at module-
// evaluation time, so the cycle is safe — see MDN's notes on circular ES
// module imports.
import { loadSessions } from './sessions.js';

let eventSource = null;

// Each handler receives ev.data ?? {}, so a malformed event (missing data)
// degrades to "nothing to read" instead of throwing out of the whole
// dispatch — before this table, `ev.data.name`/`ev.data.id` were dereferenced
// unguarded and a malformed frame from the daemon could abort the handler.
const handlers = {
  'message.user': (d) => {
    if (typeof d.text === 'string') appendUser(d.text);
  },
  'message.part.delta': (d) => {
    if (typeof d.text === 'string') appendModelText(d.text);
  },
  // One model message ended, NOT the turn — a turn with tool calls streams
  // several of these. Ending the wait here is what used to make a prompt
  // typed during tool execution skip the queue and bounce off the daemon's
  // busy flag with a 409.
  'message.part.end': () => {
    endModelText();
  },
  // The daemon's real turn boundary, emitted after its busy flag is
  // cleared — safe to stop waiting and let the queue drain.
  'turn.done': () => {
    session.runningTool = '';
    setWaiting(false);
  },
  // No transcript line for tool activity — it shows in the status bar under
  // the prompt while it runs, and vanishes with the turn.
  'tool.start': (d) => {
    session.runningTool = d.name || '';
  },
  'tool.end': () => {
    session.runningTool = '';
  },
  'permission.request': (d) => {
    session.pendingPermissionID = d.id;
    session.pendingPermissionCanAlways = !!d.can_always;
    permissionTextEl.textContent = `[${d.tool}] ${d.description || '(no description given)'}`;
    permissionAllowAlwaysBtn.style.display = session.pendingPermissionCanAlways ? '' : 'none';
    if (session.pendingPermissionCanAlways) {
      permissionAllowAlwaysBtn.title = `don't ask again — writes "${d.rule}" to config.json`;
    }
    permissionRequest.open();
    setInputLocked(true, 'Resolve the permission request above to continue.');
  },
  'permission.resolved': () => {
    permissionRequest.close();
    setInputLocked(false);
  },
  // Sidebar + status bar carry task activity; no transcript line.
  'task.spawned': (d) => {
    session.tasks.set(d.task_id, { agent: d.agent, status: 'spawned' });
    renderTasks();
  },
  'task.status': (d) => {
    if (session.tasks.has(d.task_id)) session.tasks.get(d.task_id).status = d.status;
    else session.tasks.set(d.task_id, { agent: '', status: d.status });
    renderTasks();
  },
  // Just update the state the status bar already renders every time — no
  // transcript line here. A line on every single switch would leave a
  // permanent "switched to X" entry for something already visible in the
  // header dropdown and the status line.
  'agent.switched': (d) => {
    setCurrentAgent(d.agent);
  },
  usage: (d) => {
    session.lastUsage = d;
  },
  compacted: (d) => {
    appendTool(`[system] conversation compacted to save context (summary: ${d.summary_length || 0} chars).`);
  },
  'config.changed': (d) => {
    if (typeof d.auto_compact_enabled === 'boolean') app.autoCompactEnabled = d.auto_compact_enabled;
    if (typeof d.show_tps === 'boolean') app.showTPS = d.show_tps;
    if (typeof d.auto_delegate === 'boolean') {
      app.autoDelegate = d.auto_delegate;
      renderAutoDelegate();
      refreshDelegatePanelIfOpen();
    }
  },
  'session.renamed': () => {
    loadSessions();
  },
  delegated: (d) => {
    appendTool(`[delegated to ${d.agent || ''}]`);
  },
  'turn.cancelled': () => {
    session.promptQueue = [];
    session.runningTool = '';
    setWaiting(false);
    appendTool('[cancelled]');
  },
  error: (d) => {
    session.runningTool = '';
    setWaiting(false);
    appendError(d.error || '');
  },
};

export function applyEvent(ev) {
  const h = handlers[ev.type];
  if (!h) return;
  h(ev.data ?? {});
  renderStatusBar(); // one place, instead of a hand-picked call site per case
}

export function connectEvents() {
  if (eventSource) eventSource.close();
  setConnected(false);
  eventSource = new EventSource(`/api/sessions/${session.sessionID}/events`);
  eventSource.onopen = () => setConnected(true);
  eventSource.onmessage = (e) => {
    // An event arriving is itself proof the stream is up, which matters
    // because onopen doesn't fire again after an auto-reconnect in every
    // browser.
    setConnected(true);
    try { applyEvent(JSON.parse(e.data)); } catch (err) { console.error('bad event', err); }
  };
  eventSource.onerror = () => {
    // EventSource auto-reconnects using Last-Event-ID, so there's nothing to
    // do but show the light as down until it comes back.
    setConnected(false);
  };
}
