import { app, session } from './state.js';
import {
  schedulesEl, scheduleModal, scheduleWhenInput, scheduleWhenNote,
  schedulePromptInput, scheduleNote, scheduleSaveBtn,
} from './dom.js';
import { Modal } from './modal.js';
import * as apiClient from './api.js';
import { openTaskView } from './taskview.js';

// Work booked for later, in the right-hand panel.
//
// The rows are built from the conversation's own schedule.* events, the
// same way the background-task rows are built from task.spawned: that is
// what makes them survive a reload, and it is why a removal has to be
// recorded rather than only done.

// The light. Three states, and the third one is the point: blinking
// green while it waits, solid green once there is an answer nobody has
// read, grey once it has been read. Without the third, the panel becomes
// a list of everything that ever ran instead of a list of what still
// wants attention.
//
// The classes are the ones the MCP indicator already uses, so blinking
// green means the same thing in both places and the reduced-motion rule
// that stops the blink covers this too.
function ledClass(s) {
  if (s.status === 'pending' || s.status === 'running') return 'led-degraded';
  if (s.seen) return 'led-disconnected';
  return s.status === 'done' ? 'led-connected' : 'led-disconnected';
}

// whenText is the row's first line: what it is waiting for, or what
// happened. A failure says so here rather than only inside the window,
// because a row you have to open to discover is broken is a row that gets
// left alone.
function whenText(s) {
  const at = new Date(s.at);
  const stamp = isNaN(at) ? s.at : at.toLocaleString();
  switch (s.status) {
    case 'pending': return `${stamp}`;
    case 'running': return `${stamp} — running now`;
    case 'done': return `${stamp} — done`;
    case 'missed': return `${stamp} — missed`;
    default: return `${stamp} — ${s.status}`;
  }
}

export function renderSchedules() {
  if (!schedulesEl) return;
  schedulesEl.innerHTML = '';
  const list = session.schedules ? [...session.schedules.values()] : [];
  if (list.length === 0) {
    schedulesEl.innerHTML = '<div style="color:var(--muted)">none</div>';
    return;
  }
  list.sort((a, b) => {
    const pa = a.status === 'pending', pb = b.status === 'pending';
    if (pa !== pb) return pa ? -1 : 1;
    return String(a.at).localeCompare(String(b.at));
  });

  for (const s of list) {
    const row = document.createElement('div');
    row.className = 'sched';

    const head = document.createElement('div');
    head.className = 'head';
    const led = document.createElement('span');
    led.className = `led ${ledClass(s)}`;
    head.appendChild(led);
    const when = document.createElement('span');
    when.className = 'when';
    when.textContent = whenText(s);
    head.appendChild(when);

    const del = document.createElement('button');
    del.className = 'del';
    del.textContent = '×';
    del.title = 'delete this scheduled task';
    // Stopped here, or clicking delete would also open the row it is on.
    del.addEventListener('click', (e) => { e.stopPropagation(); deleteSchedule(s.id); });
    head.appendChild(del);
    row.appendChild(head);

    const prompt = document.createElement('div');
    prompt.className = 'prompt';
    prompt.textContent = s.prompt || '';
    row.appendChild(prompt);

    if (s.error) {
      const err = document.createElement('div');
      err.className = 'err';
      err.textContent = s.error;
      row.appendChild(err);
    }

    row.addEventListener('click', () => openSchedule(s));
    schedulesEl.appendChild(row);
  }
}

// openSchedule shows what the run produced, in the same window a
// background task opens in — it is the same thing: a prompt that ran in
// its own session, read afterwards.
//
// Opening it is also what marks it read, which is the light's third
// state. A task that has not run yet has nothing to show and says so
// rather than opening an empty window.
export async function openSchedule(s) {
  if (!s.run_session) {
    const note = s.status === 'pending'
      ? 'This has not run yet.'
      : `This produced nothing to read (${s.status}).`;
    schedulesEl.title = note;
    return;
  }
  openTaskView(s.run_session, { title: `scheduled — ${s.prompt}`, status: s.status });
  if (!s.seen) {
    try {
      await apiClient.markScheduleSeen(session.sessionID, s.id);
      s.seen = true;
      renderSchedules();
    } catch { /* the light stays solid; nothing else breaks */ }
  }
}

export async function deleteSchedule(id) {
  try {
    await apiClient.deleteSchedule(session.sessionID, id);
    // The row goes on the schedule.removed event the daemon records,
    // which is also what keeps it gone across a reload.
    session.schedules.delete(id);
    renderSchedules();
  } catch (err) {
    if (schedulesEl) schedulesEl.title = `could not delete: ${err}`;
  }
}

// loadSchedules fetches the list for the open conversation. Called on a
// session switch, since these belong to the conversation.
export async function loadSchedules(sessionID) {
  if (!sessionID) return;
  try {
    const data = await apiClient.listSchedules(sessionID);
    session.schedules = new Map((data.schedules || []).map(s => [s.id, s]));
  } catch {
    session.schedules = new Map();
  }
  renderSchedules();
}

// applyScheduleEvent folds one schedule.* event into the panel. A
// snapshot per field rather than a replacement, because created carries
// the booking and status carries only what changed about it.
export function applyScheduleEvent(type, d) {
  if (!d || !d.id) return;
  if (!session.schedules) session.schedules = new Map();
  const cur = session.schedules.get(d.id) || { id: d.id, seen: false };
  switch (type) {
    case 'schedule.created':
      session.schedules.set(d.id, { ...cur, at: d.at, prompt: d.prompt, agent: d.agent, status: 'pending' });
      break;
    case 'schedule.status':
      session.schedules.set(d.id, {
        ...cur,
        status: d.status,
        run_session: d.run_session || cur.run_session,
        error: d.error || '',
      });
      break;
    case 'schedule.seen':
      session.schedules.set(d.id, { ...cur, seen: true });
      break;
    case 'schedule.removed':
      session.schedules.delete(d.id);
      break;
    default:
      return;
  }
  renderSchedules();
}

// ---- Booking one from the window ----


export const scheduleDialog = new Modal(scheduleModal);

// The moment and the request are asked for separately, which is the whole
// difference from typing "/schedule". At a prompt the split between the
// two has to be guessed out of one string — where does "내일 아침에" end
// and the request begin — and in two fields there is nothing to guess.
export function openScheduleDialog() {
  scheduleWhenInput.value = '';
  schedulePromptInput.value = '';
  setNote(scheduleWhenNote, '');
  setNote(scheduleNote, '');
  scheduleSaveBtn.disabled = false;
  scheduleDialog.open();
  scheduleWhenInput.focus();
}

export function closeScheduleDialog() {
  scheduleDialog.close();
}

function setNote(el, text, kind) {
  el.textContent = text;
  el.className = kind ? `note ${kind}` : 'note';
}

// previewWhen shows what the daemon read the time as, while it is still
// being typed. That echo is the reason the parser is allowed to guess at
// all: a misread moment is caught here, before the work is booked,
// instead of by the work not happening.
let previewTimer = null;
// Which preview is the current one. Debouncing alone does not settle it:
// two requests can be in flight when typing pauses and resumes, and the
// older one answering last would leave the box showing a moment that is
// not what is written in the field — which is the exact failure this echo
// exists to catch, produced by the echo itself.
let previewSeq = 0;
export function previewWhen() {
  clearTimeout(previewTimer);
  const text = scheduleWhenInput.value.trim();
  const seq = ++previewSeq;
  if (!text) {
    setNote(scheduleWhenNote, '');
    return;
  }
  // Debounced, because this runs on every keystroke and half a word is
  // not a question worth asking the daemon.
  previewTimer = setTimeout(async () => {
    let res;
    try {
      res = await apiClient.previewSchedule(text);
    } catch {
      if (seq === previewSeq) setNote(scheduleWhenNote, '');
      return;
    }
    if (seq !== previewSeq) return; // a newer answer has already landed
    if (res.ok) setNote(scheduleWhenNote, `→ ${res.human}`, 'ok');
    else setNote(scheduleWhenNote, res.detail || 'not a time', 'err');
  }, 250);
}

export async function saveSchedule() {
  const at = scheduleWhenInput.value.trim();
  const prompt = schedulePromptInput.value.trim();
  if (!at || !prompt) {
    setNote(scheduleNote, 'Both fields are needed: when, and what to do.', 'err');
    return;
  }
  scheduleSaveBtn.disabled = true;
  setNote(scheduleNote, '');
  try {
    const res = await apiClient.bookSchedule(session.sessionID, at, prompt);
    // The row arrives on the schedule.created event, which is also what
    // makes it survive a reload, so there is nothing to add to the panel
    // here.
    setNote(scheduleNote, `Scheduled for ${res.human}.`, 'ok');
    setTimeout(closeScheduleDialog, 900);
  } catch (err) {
    setNote(scheduleNote, String(err && err.message ? err.message : err), 'err');
    scheduleSaveBtn.disabled = false;
  }
}
