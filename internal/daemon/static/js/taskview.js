import {
  taskModal, taskModalTitle, taskModalBody, taskModalNote, taskCancelBtn,
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
      line('msg-toolcall', `▸ ${d.name || 'tool'}  ${summarizeInput(d.input || '')}`);
      break;
    case 'tool.end':
      currentEl = null;
      break;
    case 'error':
      currentEl = null;
      line('msg-error', `Error: ${d.error || ''}`);
      break;
    default:
      break;
  }
}

export function openTaskView(taskID) {
  closeTaskStream();
  openTaskID = taskID;
  currentEl = null;

  const t = session.tasks.get(taskID);
  taskModalTitle.textContent = t && t.agent ? `${t.agent} — ${taskID}` : taskID;
  taskModalBody.innerHTML = '';
  // A newly opened task starts at its newest output, whatever the last
  // one this modal showed was left scrolled to.
  follower.force();
  taskModalNote.textContent = t ? `status: ${t.status}` : '';
  // Only a task still running has anything to stop.
  taskCancelBtn.style.display = (t && (t.status === 'running' || t.status === 'spawned')) ? '' : 'none';
  taskCancelBtn.disabled = false;

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

// refreshTaskViewStatus keeps the open window's status line and its stop
// button honest when a task.status event lands for the task being watched.
export function refreshTaskViewStatus(taskID, status) {
  if (!taskView.isOpen || taskID !== openTaskID) return;
  taskModalNote.textContent = `status: ${status}`;
  if (status !== 'running' && status !== 'spawned') taskCancelBtn.style.display = 'none';
}
