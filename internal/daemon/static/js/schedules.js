import { app, session } from './state.js';
import {
  schedulesEl, scheduleModal, scheduleWhenInput, scheduleWhenNote,
  schedulePromptInput, scheduleNote, scheduleSaveBtn,
  scheduleRepeatBox, scheduleRepeatNote, scheduleTimesInput, scheduleUntilInput, scheduleKeepInput,
  scheduleDetailsModal, scheduleDetailsWhen, scheduleDetailsStatus,
  scheduleDetailsError, scheduleDetailsPrompt, scheduleDetailsAgent,
  scheduleDetailsRepeat, scheduleDetailsRule,
  scheduleDetailsOpenBtn,
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
  // A repeat that stopped itself is the one state that wants a person:
  // amber means "this one is yours" everywhere else in the product, and
  // a suspended booking is not going to un-suspend on its own.
  if (s.status === 'suspended') return 'led-asking';
  // A repeat between runs is armed and has an unread answer at the same
  // time. Unread wins: "there is something to read" is the useful half,
  // and "it will run again" is what the row's own line already says.
  if (s.runs > 0 && !s.seen && s.status === 'pending') return 'led-connected';
  if (s.status === 'pending' || s.status === 'running') return 'led-degraded';
  if (s.seen) return 'led-disconnected';
  return s.status === 'done' ? 'led-connected' : 'led-disconnected';
}

// repeatText is how often a booking runs, in the words the confirmation
// used. Empty for a booking that runs once, which is most of them.
export function repeatText(s) {
  if (!s.repeat || !(s.repeat.every > 0)) return '';
  const unit = s.repeat.every === 1 ? s.repeat.unit : `${s.repeat.every} ${s.repeat.unit}s`;
  return `every ${unit}`;
}

// limitText says how long the series goes on for. Spelled out rather than
// summarised: "until you delete it" and "10 times" are the difference
// between a helper and something that wakes the machine up all year.
export function limitText(s) {
  if (!repeatText(s)) return '';
  const parts = [];
  if (s.stop_after > 0) parts.push(`${s.stop_after} times`);
  if (s.stop_at) {
    const at = new Date(s.stop_at);
    if (!isNaN(at)) parts.push(`until ${at.toLocaleString()}`);
  }
  return parts.length ? parts.join(' or ') : 'until you delete it';
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

    row.appendChild(head);

    // The name when there is one, and the prompt underneath it in the
    // muted line — so naming a task adds a label rather than hiding what
    // it will actually run, which is the thing worth being able to check.
    if (s.name) {
      const name = document.createElement('div');
      name.className = 'name';
      name.textContent = s.name;
      row.appendChild(name);
    }
    // How often, and how far in. Its own line rather than folded into the
    // time, because a row that says only the next moment reads exactly
    // like a booking that runs once.
    const rule = repeatText(s);
    if (rule) {
      const rep = document.createElement('div');
      rep.className = 'repeat';
      const done = s.runs > 0 ? `, ${s.runs} run${s.runs === 1 ? '' : 's'} so far` : '';
      rep.textContent = `${rule}, ${limitText(s)}${done}`;
      row.appendChild(rep);
    }

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

    // Named buttons on their own line, the same shape a session row uses
    // for the same two jobs. An icon is smaller and this panel is not
    // short of room; "rename" and "delete" are what the left-hand list
    // says, and one word in two places beats two glyphs somebody has to
    // learn. Both stop propagation, or clicking either would also open
    // the row they sit on.
    const actions = document.createElement('div');
    actions.className = 'actions';

    const renameBtn = document.createElement('button');
    renameBtn.textContent = 'rename';
    renameBtn.title = 'name this scheduled task';
    renameBtn.addEventListener('click', (e) => { e.stopPropagation(); renameSchedulePrompt(s); });
    actions.appendChild(renameBtn);

    const delBtn = document.createElement('button');
    delBtn.textContent = 'delete';
    delBtn.className = 'danger-btn';
    delBtn.title = 'delete this scheduled task and its transcript';
    delBtn.addEventListener('click', (e) => { e.stopPropagation(); deleteSchedule(s.id); });
    actions.appendChild(delBtn);

    row.appendChild(actions);

    // One click opens the run, two open the booking.
    //
    // Both on the same row, so the single click has to wait long enough
    // to find out whether a second one is coming — otherwise a
    // double-click opens the run *and* the details, which is two windows
    // for one gesture. The wait is only paid where the click has
    // somewhere to go: a task that has not run yet has no window to open,
    // so its details appear the instant they are asked for.
    let pendingOpen = null;
    row.addEventListener('click', () => {
      if (!s.run_session) {
        openSchedule(s);
        return;
      }
      if (pendingOpen) return; // the second click of a pair; dblclick has it
      pendingOpen = setTimeout(() => {
        pendingOpen = null;
        openSchedule(s);
      }, doubleClickGrace);
    });
    row.addEventListener('dblclick', () => {
      if (pendingOpen) {
        clearTimeout(pendingOpen);
        pendingOpen = null;
      }
      openScheduleDetails(s);
    });
    schedulesEl.appendChild(row);
  }
}

// How long a single click waits to see whether it is really the first
// half of a double one. 250ms is about where a deliberate double-click
// lands and is short enough not to read as lag on the single one.
const doubleClickGrace = 250;

export const scheduleDetails = new Modal(scheduleDetailsModal);

// openScheduleDetails shows one booking in full.
//
// Every field the row has, it clips: the time, the name and the prompt
// are each one line with an ellipsis, so a prompt of any length is a few
// words followed by nothing. Since the prompt is the whole of what the
// booking will do, that left the panel unable to answer the one question
// worth asking of it — and the daemon already sends every field, so this
// is a window rather than a request.
//
// Read-only. Rename and delete stay on the row; a window opened to check
// something is the wrong place to change it by accident.
export function openScheduleDetails(s) {
  const at = new Date(s.at);
  scheduleDetailsWhen.textContent = isNaN(at) ? String(s.at || '') : at.toLocaleString();

  // The name belongs with the status line rather than in a heading of its
  // own: an unnamed booking is the common case, and a heading that is
  // empty half the time is a hole in the layout.
  const status = s.name ? `${s.name} — ${s.status}` : String(s.status || '');
  scheduleDetailsStatus.textContent = s.seen ? `${status} (read)` : status;

  scheduleDetailsError.textContent = s.error || '';
  scheduleDetailsError.hidden = !s.error;

  // The whole commitment, in the one place with room for it: how often,
  // how long for, how far in, and what happens to the transcripts. The
  // row can only clip these onto one line.
  const rule = repeatText(s);
  scheduleDetailsRepeat.hidden = !rule;
  if (rule) {
    const lines = [`${rule}, ${limitText(s)}.`];
    if (s.runs > 0) lines.push(`${s.runs} run${s.runs === 1 ? '' : 's'} so far.`);
    lines.push(keepText(s.keep));
    lines.push('It stops itself if 3 runs in a row fail.');
    scheduleDetailsRule.textContent = lines.join(' ');
  }

  // textContent, into a <pre>: the prompt is somebody's own words and the
  // newlines in it are part of what it says.
  scheduleDetailsPrompt.textContent = s.prompt || '';
  scheduleDetailsAgent.textContent = s.agent || 'the conversation\'s own agent';

  scheduleDetailsOpenBtn.hidden = !s.run_session;
  scheduleDetailsOpenBtn.onclick = () => {
    scheduleDetails.close();
    openSchedule(s);
  };

  scheduleDetails.open();
}

// keepText says what happens to the run transcripts, in words rather
// than a number, because -1 and 0 are the two answers a number does not
// explain — and 0 really does delete the run that has just finished.
function keepText(keep) {
  if (keep < 0) return 'Every run\u2019s transcript is kept.';
  if (keep === 0) return 'No run transcripts are kept \u2014 this row still says whether each run worked.';
  return `The last ${keep} runs\u2019 transcripts are kept.`;
}

export function closeScheduleDetails() {
  scheduleDetails.close();
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
  openTaskView(s.run_session, { title: `scheduled — ${s.name || s.prompt}`, status: s.status });
  if (!s.seen) {
    try {
      await apiClient.markScheduleSeen(session.sessionID, s.id);
      s.seen = true;
      renderSchedules();
    } catch { /* the light stays solid; nothing else breaks */ }
  }
}

// renameSchedulePrompt asks for the label, the same window.prompt the
// session list uses for the same job.
export async function renameSchedulePrompt(s) {
  const name = window.prompt('Name for this scheduled task (empty to clear):', s.name || '');
  if (name === null) return;
  try {
    const updated = await apiClient.renameSchedule(session.sessionID, s.id, name);
    // The row also arrives on the schedule.renamed event, which is what
    // makes it survive a reload and reach a second window; this is the
    // one in front of the person who typed it.
    session.schedules.set(updated.id, { ...s, name: updated.name });
    renderSchedules();
  } catch (err) {
    if (schedulesEl) schedulesEl.title = `could not rename: ${err}`;
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
    // The switch that asked for this did not wait for it, so two switches
    // in quick succession race and the later reply can arrive first. A
    // list belonging to a conversation you have left is not this panel's.
    if (sessionID !== session.sessionID) return;
    session.schedules = new Map((data.schedules || []).map(s => [s.id, s]));
  } catch {
    if (sessionID !== session.sessionID) return;
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
      session.schedules.set(d.id, {
        ...cur, at: d.at, prompt: d.prompt, agent: d.agent, status: 'pending',
        // The repeat and its limits, when it has any. Carried on the
        // created event because that is what a reload rebuilds from.
        repeat: d.repeat_every > 0 ? { every: d.repeat_every, unit: d.repeat_unit } : null,
        keep: d.keep,
        stop_at: d.stop_at || '',
        stop_after: d.stop_after || 0,
        runs: 0,
      });
      break;
    case 'schedule.status':
      session.schedules.set(d.id, {
        ...cur,
        status: d.status,
        run_session: d.run_session || cur.run_session,
        error: d.error || '',
        // A repeat re-arms itself, so its next moment and its tally
        // arrive on the status event that does it.
        at: d.at || cur.at,
        runs: typeof d.runs === 'number' ? d.runs : cur.runs,
      });
      break;
    case 'schedule.seen':
      session.schedules.set(d.id, { ...cur, seen: true });
      break;
    case 'schedule.renamed':
      session.schedules.set(d.id, { ...cur, name: d.name || '' });
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
// showRepeatFields reveals the limits when the time asks for a repeat,
// and says which repeat was read — so "매일 9시" being taken as daily is
// visible before anything is booked rather than at the second run.
function showRepeatFields(rule) {
  scheduleRepeatBox.hidden = !rule;
  setNote(scheduleRepeatNote, rule ? `Repeats ${rule}. Leave both blank and it runs until you delete it.` : '');
}

export function openScheduleDialog() {
  scheduleWhenInput.value = '';
  schedulePromptInput.value = '';
  scheduleTimesInput.value = '';
  scheduleUntilInput.value = '';
  scheduleKeepInput.value = '';
  showRepeatFields('');
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
    // The limits appear only once the time asks for a repeat. Most
    // bookings run once, and three boxes they never need is three boxes
    // in the way of the two they do.
    showRepeatFields(res.ok ? res.repeat : '');
  }, 250);
}

// repeatFields is what the three boxes say, with blank meaning "no
// limit" for the two stop conditions and "the default" for keep. An empty
// box is not a zero: 0 in the keep box means keep nothing, which is a
// different answer from not having said.
function repeatFields() {
  const out = {};
  if (scheduleRepeatBox.hidden) return out;
  const times = parseInt(scheduleTimesInput.value, 10);
  if (Number.isFinite(times) && times > 0) out.times = times;
  const until = scheduleUntilInput.value.trim();
  if (until) out.until = until;
  const keep = parseInt(scheduleKeepInput.value, 10);
  if (Number.isFinite(keep)) out.keep = keep;
  return out;
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
    const res = await apiClient.bookSchedule(session.sessionID, at, prompt, repeatFields());
    // The row arrives on the schedule.created event, which is also what
    // makes it survive a reload, so there is nothing to add to the panel
    // here.
    //
    // The repeat is spelled out rather than summarised, because "until
    // you delete it" and "ten times" are the difference between a helper
    // and something that wakes the machine up all year.
    setNote(scheduleNote, res.repeat
      ? `Scheduled for ${res.human}. ${res.repeat}.`
      : `Scheduled for ${res.human}.`, 'ok');
    setTimeout(closeScheduleDialog, 900);
  } catch (err) {
    setNote(scheduleNote, String(err && err.message ? err.message : err), 'err');
    scheduleSaveBtn.disabled = false;
  }
}
