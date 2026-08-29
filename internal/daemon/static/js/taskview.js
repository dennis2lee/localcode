import {
  taskModal, taskModalTitle, taskModalBody, taskModalNote, taskCancelBtn, taskDeleteBtn,
} from './dom.js';
import { createFollower } from './scroll.js';
import { session } from './state.js';
import * as apiClient from './api.js';
import { Modal } from './modal.js';
import { summarizeInput } from './transcript.js';

// A window on a background task.
//
// A task used to report three words about itself — its agent, its id, and
// one of "spawned"/"running"/"completed" — and nothing else, ever. What it
// was doing, how far it had got, whether it was stuck on something: none
// of that was anywhere, because a task's own conversation is a session
// nothing lists and nothing opens. "1 background task" that never
// finishes is not a progress report.
//
// A task *is* a session, so its log streams over exactly the same endpoint
// as a conversation's. This is that stream, rendered read-only: there is
// no prompt box, because talking to a task is not a thing — it was given
// its instructions when it was spawned.
export const taskView = new Modal(taskModal);

let stream = null;
let openTaskID = '';

// A task's own transcript follows the newest output on the same terms as
// the main one: only while the reader is at the bottom of it. See
// scroll.js. There is no jump control in the modal — it is a small box
// with an obvious scrollbar, and the same window is reopened from the
// task list.
const follower = createFollower(taskModalBody);

// How much of a long task to load when opening it. Same reasoning as the
// transcript's own tail, and the same units — the daemon drops the
// fragments of finished replies, so this counts messages, not characters.
const TASK_TAIL = 200;

function line(cls, text) {
  const div = document.createElement('div');
  div.className = cls;
  div.textContent = text;
  follower.keeping(() => taskModalBody.appendChild(div));
  return div;
}

// The reply in progress, so streamed fragments extend one line instead of
// producing one line per fragment.
let currentEl = null;

// The tool calls still running, by tool_use_id, so the result can be put
// back on the row that asked for it.
//
// This window used to drop tool.end on the floor. A task showed "▸ bash
// go test ./..." and then, whatever happened next, nothing: no result, no
// success or failure, no sign the call had even ended. Watching a task
// meant watching the moment work started and never the moment it
// finished, which is the half you are usually waiting for.
const taskToolRows = new Map();

// toolRow is the task window's version of the transcript's tool block:
// the same classes, so it is styled by the same rules, and the same
// shape, so a task's tools read like the main conversation's.
function toolRow(toolUseID, name, inputJSON) {
  const row = document.createElement('div');
  row.className = 'msg-toolcall running';

  const head = document.createElement('div');
  head.className = 'head';
  const marker = document.createElement('span');
  marker.className = 'marker';
  marker.textContent = '▸';
  const nameEl = document.createElement('span');
  nameEl.className = 'name';
  nameEl.textContent = name || 'tool';
  const argEl = document.createElement('span');
  argEl.className = 'arg';
  argEl.textContent = summarizeInput(inputJSON || '');
  const stateEl = document.createElement('span');
  stateEl.className = 'state';
  stateEl.textContent = 'running…';
  // appendChild one at a time, the way every other builder in the
  // shipped code does. The variadic append() is equivalent in a browser.
  head.appendChild(marker);
  head.appendChild(nameEl);
  head.appendChild(argEl);
  head.appendChild(stateEl);

  const detail = document.createElement('pre');
  detail.className = 'detail';
  detail.textContent = String(inputJSON || '');

  row.appendChild(head);
  row.appendChild(detail);
  follower.keeping(() => taskModalBody.appendChild(row));
  if (toolUseID) taskToolRows.set(toolUseID, { row, stateEl, marker, detail });
}

function finishToolRow(toolUseID, content, isError) {
  const entry = taskToolRows.get(toolUseID);
  if (!entry) return;
  taskToolRows.delete(toolUseID);
  const { row, stateEl, marker, detail } = entry;
  row.classList.remove('running');
  row.classList.toggle('failed', !!isError);
  marker.textContent = isError ? '✗' : '✓';
  const text = String(content ?? '');
  follower.keeping(() => {
    stateEl.textContent = isError ? 'failed' : `${text.split('\n').length} lines`;
    detail.textContent = `${detail.textContent}\n\n${text}`;
  });
}

function applyTaskEvent(ev) {
  const d = ev.data || {};
  switch (ev.type) {
    case 'message.user':
      currentEl = null;
      if (typeof d.text === 'string') line('msg-user', d.text);
      break;
    case 'message.part.delta':
      if (typeof d.text !== 'string') return;
      if (!currentEl) currentEl = line('msg-model', '');
      follower.keeping(() => { currentEl.textContent += d.text; });
      break;
    case 'message.part.end':
      // On replay this carries the whole reply and the fragments are not
      // sent at all — see collapseFinishedDeltas in the daemon.
      if (!currentEl && typeof d.text === 'string' && d.text) line('msg-model', d.text);
      currentEl = null;
      break;
    case 'tool.start':
      currentEl = null;
      toolRow(d.tool_use_id, d.name || '', d.input || '');
      break;
    case 'tool.end':
      currentEl = null;
      finishToolRow(d.tool_use_id, d.content, d.is_error);
      break;
    // A task waiting on a permission looked exactly like a task working,
    // which is the worst thing this window could get wrong: it is stuck,
    // it is stuck on something specific, and nothing said so. The answer
    // is given in the main window, where the prompt appears; this says
    // what it is waiting for and stops saying it when it is answered.
    case 'permission.request':
      currentEl = null;
      line('msg-error', `⏸ waiting for permission: [${d.tool || '?'}] ${d.description || ''}`);
      break;
    case 'permission.resolved':
      currentEl = null;
      line('msg-tool', d.allowed ? '▶ permission granted' : '⛔ permission denied');
      break;
    // A task can delegate too, and a task waiting on its own children
    // with nothing on screen about them is the same gap one level down.
    case 'task.spawned':
      currentEl = null;
      line('msg-tool', `⇢ delegated to ${d.agent || '?'} (${d.task_id || ''})`);
      break;
    case 'task.status':
      currentEl = null;
      line('msg-tool', `⇠ ${d.task_id || 'task'}: ${d.status || ''}`);
      break;
    case 'delegated':
      currentEl = null;
      line('msg-tool', `⇢ handed to ${d.agent || '?'}`);
      break;
    case 'agent.switched':
      currentEl = null;
      line('msg-tool', `agent: ${d.agent || ''}`);
      break;
    case 'compacted':
      currentEl = null;
      line('msg-tool', `[the task's history was summarized to save context]`);
      break;
    // The end of the work, which had no marker at all: the last reply
    // simply stopped and you were left guessing whether more was coming.
    case 'turn.done':
      currentEl = null;
      abandonRunningToolRows();
      line('msg-tool', '— finished —');
      break;
    case 'turn.cancelled':
      currentEl = null;
      abandonRunningToolRows();
      line('msg-tool', '— cancelled —');
      break;
    case 'error':
      currentEl = null;
      abandonRunningToolRows();
      line('msg-error', `Error: ${d.error || ''}`);
      break;
    default:
      break;
  }
}

// abandonRunningToolRows closes out any row still spinning when the work
// ends. A cancelled turn emits no tool.end for the call it stopped, so
// without this the row spins under the "cancelled" line forever.
function abandonRunningToolRows() {
  for (const [, entry] of taskToolRows) {
    entry.row.classList.remove('running');
    entry.marker.textContent = '–';
    entry.stateEl.textContent = 'did not finish';
  }
  taskToolRows.clear();
}

// about, when given, describes a session this panel has no row for: a
// scheduled task's run. Without it the window opens with the id as its
// title and no status, because session.tasks knows nothing about it.
export function openTaskView(taskID, about) {
  closeTaskStream();
  openTaskID = taskID;
  currentEl = null;

  const t = about || session.tasks.get(taskID);
  taskModalTitle.textContent = t && t.title ? t.title
    : (t && t.agent ? `${t.agent} — ${taskID}` : taskID);
  taskModalBody.innerHTML = '';
  taskToolRows.clear();
  // A newly opened task starts at its newest output, whatever the last
  // one this modal showed was left scrolled to.
  follower.force();
  taskModalNote.textContent = t ? `status: ${t.status}` : '';
  // A window opened on something this panel does not own gets no
  // buttons. Stop and Delete both act on the *task* — cancelTask by id,
  // deleteSession on the conversation — and neither is right for a
  // scheduled run: stopping it would reach a task manager that has never
  // heard of it, and deleting it would remove the transcript while
  // leaving the schedule's own row pointing at a session that is gone.
  // The row in the Scheduled tasks panel has its own delete, which
  // removes the booking and the run together.
  showTaskButtons(about ? '' : (t ? t.status : ''));

  stream = new EventSource(`/api/sessions/${taskID}/events?tail=${TASK_TAIL}`);
  stream.onmessage = (e) => {
    try { applyTaskEvent(JSON.parse(e.data)); } catch (err) { console.error('bad task event', err); }
  };
  taskView.open();
}

export function closeTaskView() {
  closeTaskStream();
  taskView.close();
}

function closeTaskStream() {
  if (stream) stream.close();
  stream = null;
  openTaskID = '';
}

export async function cancelOpenTask() {
  if (!openTaskID) return;
  taskCancelBtn.disabled = true;
  try {
    await apiClient.cancelTask(openTaskID);
    taskModalNote.textContent = 'status: cancelling…';
  } catch (err) {
    taskModalNote.textContent = `could not stop this task: ${err}`;
    taskCancelBtn.disabled = false;
  }
}

// showTaskButtons offers exactly one of the two, because they are the
// same question at two different moments: a task that is running can be
// stopped, and a task that has finished can be thrown away. Neither is
// useful at the other's moment, and both at once would put a Delete
// beside a Stop for work still going on.
function showTaskButtons(status) {
  const running = status === 'running' || status === 'spawned';
  taskCancelBtn.style.display = running ? '' : 'none';
  taskCancelBtn.disabled = false;
  // Not for a task with no status at all: that is a window opened on
  // something the panel has no record of, and offering to delete it
  // would be offering to delete what might still be working.
  taskDeleteBtn.style.display = (!running && status) ? '' : 'none';
  taskDeleteBtn.disabled = false;
}

// refreshTaskViewStatus keeps the open window's status line and its
// buttons honest when a task.status event lands for the task being
// watched: a task that finishes while you are reading it stops offering
// to be stopped and starts offering to be removed.
export function refreshTaskViewStatus(taskID, status) {
  if (!taskView.isOpen || taskID !== openTaskID) return;
  taskModalNote.textContent = `status: ${status}`;
  showTaskButtons(status);
}

// deleteOpenTask removes a finished task's conversation for good.
//
// The work is over and its transcript is the only thing left, so this is
// the one way to be rid of a row that has served its purpose. It goes
// through the ordinary session delete, because a task is a session; the
// daemon records the removal on the parent's log, which is where the row
// in the panel comes from, so it does not come back on the next reload.
export async function deleteOpenTask() {
  if (!openTaskID) return;
  const id = openTaskID;
  taskDeleteBtn.disabled = true;
  try {
    await apiClient.deleteSession(id);
    // The row goes on the task.status event the daemon just recorded,
    // and this window has nothing left to show.
    closeTaskView();
  } catch (err) {
    taskModalNote.textContent = `could not delete this task: ${err}`;
    taskDeleteBtn.disabled = false;
  }
}
