// Token spend by model, across every conversation on this daemon.
//
// The command beside it is /usage: bare for this conversation, and with
// all|today|week|month across every conversation the daemon holds,
// archived ones included. This view is the "all" window of that command —
// which models this daemon has spent tokens on, and how much each — drawn
// where the command's text cannot be: as one labelled bar per model next
// to the figures, in a window that can be opened without typing anything
// into the conversation it is looking at.
//
// Computed here, in the client, rather than fetched as totals: the daemon
// serves no JSON route carrying aggregated per-model sums — /usage answers
// as transcript text through POST .../messages, which would write a
// "/usage all" line and its reply into the open conversation on every
// viewing. What it does serve is everything usageAcross adds up: the
// session lists (visible and archived) and each session's own event log,
// whose usage events and model-carrying compacted events are exactly the
// two kinds that command counts. So this opens one stream per session,
// folds the same two event kinds the same way, and draws that.
//
// A stream per session is heavier than one request, and it is opened only
// while the window is open and closed with it: a standing fan of streams
// for a window nobody is looking at is how a page that never reloads ends
// up holding the daemon's connection table hostage.

import {
  usageBtn, usageModalEl, usageScopeEl, usageRowsEl, usageNoteEl, usageCloseBtn,
} from './dom.js';
import { app } from './state.js';
import * as apiClient from './api.js';
import { appendError } from './transcript.js';
import { Modal } from './modal.js';

// The window itself. Exported so modals.js can count it in anyModalOpen —
// the Tab and Alt+arrow keys stand down while any window is up, and a
// window this module opens behind that function's back would not stop
// them.
export const usageView = new Modal(usageModalEl);

// Streams opened for the viewing in progress, session id -> EventSource.
// Kept so closing the window closes every stream it opened: each one is a
// live subscription the daemon keeps writing heartbeats into, and leaving
// them running after the rows are gone is a leak that grows by one fan per
// opening.
let viewingStreams = new Map();

// addModelTokens folds one billable model call into the running per-model
// totals. A mirror of the daemon's addModelTotals (internal/agent/
// rehydrate.go): a usage report naming no model is left out, because there
// is no row it could belong to and a row for "unknown" would invite
// reading it as a model. Non-numeric counts read as zero rather than
// poisoning the sum — the daemon's dataInt likewise answers 0 for
// anything that is not a number.
export function addModelTokens(totals, model, inputTokens, outputTokens) {
  if (typeof model !== 'string' || model === '') return totals;
  const input = Number.isFinite(+inputTokens) ? Math.trunc(+inputTokens) : 0;
  const output = Number.isFinite(+outputTokens) ? Math.trunc(+outputTokens) : 0;
  const row = totals[model] || { input: 0, output: 0, calls: 0 };
  row.input += input;
  row.output += output;
  row.calls += 1;
  totals[model] = row;
  return totals;
}

// foldUsageEvent counts one log event toward the per-model totals, if it
// is one of the two kinds /usage all counts. usage events are every
// provider call's own full request (history included), so summing them is
// the spend — not the context fill, which is the latest snapshot and a
// different number. compacted events count when they name the model whose
// summarising call they billed; cleared and rewound markers carry no model
// and no tokens, so they pass through untouched, exactly as on the daemon
// side where only a non-empty model adds anything.
export function foldUsageEvent(totals, ev) {
  if (!ev || typeof ev.type !== 'string') return totals;
  const d = (ev.data && typeof ev.data === 'object') ? ev.data : {};
  if (ev.type === 'usage') {
    addModelTokens(totals, d.model, d.input_tokens, d.output_tokens);
  } else if (ev.type === 'compacted') {
    if (typeof d.model === 'string' && d.model !== '') {
      addModelTokens(totals, d.model, d.input_tokens, d.output_tokens);
    }
  }
  return totals;
}

// summarizeUsageEvents folds a whole log's events into per-model totals.
// Pure — the live view folds arriving events one at a time through
// foldUsageEvent instead — and exported so the requirement (what counts,
// what does not) is testable without opening any stream.
export function summarizeUsageEvents(events) {
  const totals = {};
  for (const ev of events || []) foldUsageEvent(totals, ev);
  return totals;
}

// renderUsage draws the totals: one row per model, most-spending first,
// each with its input, output and combined figures beside a bar showing
// the same two numbers as two segments of one track. Two segments rather
// than one summed bar because input and output tokens are neither
// interchangeable nor priced alike, and a single bar summing them tells a
// comforting lie about where the spend went. The track is scaled to the
// largest model's combined total, which the caption says — a bar whose
// scale is a secret is decoration, not information.
//
// Totals, never percentages: there is no window here to be a percent of,
// and the 70/90 context thresholds of the status line belong to this
// conversation's window fill, a different thing entirely.
export function renderUsage() {
  const totals = app.usageTotals || {};
  const models = Object.keys(totals).sort((a, b) =>
    (totals[b].input + totals[b].output) - (totals[a].input + totals[a].output));
  usageRowsEl.innerHTML = '';
  if (models.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'meta';
    empty.textContent = 'No usage recorded yet.';
    usageRowsEl.appendChild(empty);
    return;
  }
  const largest = Math.max(...models.map((m) => totals[m].input + totals[m].output));
  for (const m of models) {
    const t = totals[m];
    const combined = t.input + t.output;
    const row = document.createElement('div');
    row.className = 'usage-row';

    const name = document.createElement('div');
    name.className = 'usage-model';
    name.textContent = m;
    row.appendChild(name);

    const figures = document.createElement('div');
    figures.className = 'usage-figures';
    figures.textContent =
      `input ${t.input} · output ${t.output} · total ${combined} (${t.calls} call${t.calls === 1 ? '' : 's'})`;
    row.appendChild(figures);

    const track = document.createElement('div');
    track.className = 'usage-track';
    track.title = `${m}: input ${t.input}, output ${t.output} tokens across every conversation, archived ones included`;
    const inputSeg = document.createElement('div');
    inputSeg.className = 'usage-seg usage-input';
    inputSeg.style.width = largest > 0 ? `${(t.input / largest) * 100}%` : '0%';
    track.appendChild(inputSeg);
    const outputSeg = document.createElement('div');
    outputSeg.className = 'usage-seg usage-output';
    outputSeg.style.width = largest > 0 ? `${(t.output / largest) * 100}%` : '0%';
    track.appendChild(outputSeg);
    row.appendChild(track);

    usageRowsEl.appendChild(row);
  }
}

// renderUsageScope names what the figures cover and what they left out.
// The daemon names unreadable logs rather than skipping them in silence,
// and this does the same for sessions whose streams failed: a total
// quietly missing a conversation is the failure this view would be judged
// on.
export function renderUsageScope() {
  usageScopeEl.textContent =
    `Token usage across every conversation (${app.usageSessions} conversation${app.usageSessions === 1 ? '' : 's'})`;
  const unread = app.usageUnread || [];
  if (unread.length > 0) {
    usageNoteEl.textContent =
      `${unread.length} conversation${unread.length === 1 ? '' : 's'} could not be read and ${unread.length === 1 ? 'is' : 'are'} not in this total.`;
  } else {
    usageNoteEl.textContent =
      'Counted from the conversations\u2019 own logs, archived ones included. A turn that was later undone still cost what it cost, so it is still counted.';
  }
}

// closeUsage shuts the window and every stream the viewing opened. Streams
// first, then the window: a failure between the two in the other order
// would leave subscriptions running for rows nobody can see any more.
export function closeUsage() {
  for (const [, source] of viewingStreams) {
    try { source.close(); } catch { /* already gone: the total stays */ }
  }
  viewingStreams = new Map();
  usageView.close();
}

// openUsage loads the membership (visible and archived — tokens are spent
// whether or not a conversation is still open, so a view that silently
// dropped the archive would disagree with the command beside it) and then
// tails every session's own log for the two event kinds that count. Rows
// render progressively as backlogs arrive rather than waiting for the
// slowest log, because the point of opening the window is to look at it.
export async function openUsage() {
  closeUsageStreamsOnly();
  usageView.open();
  app.usageTotals = {};
  app.usageSessions = 0;
  app.usageUnread = [];
  renderUsage();
  usageScopeEl.textContent = 'Reading every conversation\u2019s log\u2026';
  usageNoteEl.textContent = '';
  let sessions = [];
  try {
    const [visible, archived] = await Promise.all([
      apiClient.getSessions(),
      apiClient.getArchivedSessions(),
    ]);
    sessions = [...(visible || []), ...(archived || [])];
  } catch (err) {
    usageScopeEl.textContent = 'Token usage across every conversation';
    usageNoteEl.textContent = `The session list could not be read: ${err}`;
    return;
  }
  const counted = new Set();
  for (const s of sessions) {
    if (!s || !s.id) continue;
    watchSession(s.id, counted);
  }
  renderUsageScope();
}

// closeUsageStreamsOnly stops the viewing's streams without touching the
// window. Split out of closeUsage because opening a fresh viewing must
// stop the previous fan of streams first, and that must not close (or
// flicker) the window it is about to fill.
function closeUsageStreamsOnly() {
  for (const [, source] of viewingStreams) {
    try { source.close(); } catch { /* already gone */ }
  }
  viewingStreams = new Map();
}

// watchSession tails one session's whole log — no ?tail=, so the daemon
// replays from the first event — and folds the two countable kinds into
// the shared totals as they arrive. A stream that fails marks its session
// unread rather than vanishing: see renderUsageScope.
function watchSession(id, counted) {
  let source;
  try {
    source = new EventSource(`/api/sessions/${id}/events`);
  } catch (err) {
    markUnread(id);
    return;
  }
  viewingStreams.set(id, source);
  source.onmessage = (e) => {
    let ev;
    try {
      ev = JSON.parse(e.data);
    } catch {
      return; // a malformed frame is not a usage report; skip it, keep the stream
    }
    app.usageTotals = foldUsageEvent(app.usageTotals || {}, ev);
    if (!counted.has(id) && countable(ev)) {
      counted.add(id);
      app.usageSessions = counted.size;
    }
    // Only a countable event can have moved any figure, so only one
    // re-renders: the stream also replays the whole transcript, and
    // redrawing the rows per model reply would thrash the window for
    // nothing.
    if (countable(ev)) renderUsage();
    renderUsageScope();
  };
  source.onerror = () => {
    // A reply that failed the connection (a log the daemon cannot read,
    // a session deleted mid-viewing) never retries per the SSE spec, so
    // this is the moment it is known to be missing — not a transient the
    // browser will heal on its own.
    try { source.close(); } catch { /* already closed */ }
    viewingStreams.delete(id);
    markUnread(id);
    renderUsageScope();
  };
}

// countable is the membership half of usageAcross's `counted` flag: a
// session counts toward "N conversations" once one countable event has
// been seen in it, and only then. An event without a model adds nothing
// to any row, so it must not add the session either, or the header would
// name conversations the rows know nothing about.
function countable(ev) {
  if (!ev || typeof ev.type !== 'string') return false;
  const d = (ev.data && typeof ev.data === 'object') ? ev.data : {};
  if (ev.type === 'usage') return typeof d.model === 'string' && d.model !== '';
  if (ev.type === 'compacted') return typeof d.model === 'string' && d.model !== '';
  return false;
}

function markUnread(id) {
  app.usageUnread = app.usageUnread || [];
  if (!app.usageUnread.includes(id)) app.usageUnread.push(id);
}

usageBtn.addEventListener('click', openUsage);
usageCloseBtn.addEventListener('click', closeUsage);
