// Token spend by model, across every conversation on this daemon.
//
// The command beside it is /usage: bare for this conversation, and with
// all|today|week|month across every conversation the daemon holds. This
// view is the "all" window of that command, drawn where the command's
// text cannot be: as one labelled bar per model next to the figures, in
// a window that can be opened without typing anything into the
// conversation it is looking at.
//
// Asked of the daemon (GET /api/usage), which adds the logs up with the
// same function the command uses. The window used to add them up itself,
// one event stream per conversation in the session list, and a list
// shows neither the sessions of sub-agents, scheduled runs and debate
// reviewers nor where a fork's copy of another log ends: the window and
// the command beside it gave two different totals for the same daemon.

import {
  usageBtn, usageModalEl, usageScopeEl, usageRowsEl, usageNoteEl, usageCloseBtn,
} from './dom.js';
import { app } from './state.js';
import * as apiClient from './api.js';
import { Modal } from './modal.js';

// The window itself. Exported so modals.js can count it in anyModalOpen —
// the Tab and Alt+arrow keys stand down while any window is up, and a
// window this module opens behind that function's back would not stop
// them.
export const usageView = new Modal(usageModalEl);

// The kinds of token a row shows, in the order its bar draws them: the
// class that colours both the segment and its key, the label the figures
// and the key use, and which figure it is. One table, so a segment and
// the key that names it cannot disagree. Input and output are always
// shown; a cache figure only where there is one, so a provider with no
// prompt cache reads as it always has.
const KINDS = [
  { cls: 'usage-input', label: 'input', key: 'input_tokens', always: true },
  { cls: 'usage-cache-read', label: 'cache read', key: 'cache_read_tokens' },
  { cls: 'usage-cache-write', label: 'cache write', key: 'cache_write_tokens' },
  { cls: 'usage-cache-unsplit', label: 'cache read or write', key: 'cache_read_or_write_tokens' },
  { cls: 'usage-output', label: 'output', key: 'output_tokens', always: true },
];

// count reads one reported figure. Anything that is not a number reads
// as zero rather than poisoning the sum.
function count(v) {
  return Number.isFinite(+v) ? Math.trunc(+v) : 0;
}

// shown is the kinds a row draws, with their figures.
function shown(t) {
  return KINDS.map((k) => ({ ...k, n: count(t[k.key]) })).filter((k) => k.always || k.n > 0);
}

// totalOf is every token a model's calls processed: what they were sent,
// fresh or from the cache, and what they wrote back.
export function totalOf(t) {
  return KINDS.reduce((n, k) => n + count(t[k.key]), 0);
}

// figuresOf is one model's figures as /usage prints them.
export function figuresOf(t) {
  const calls = count(t.calls);
  const parts = shown(t).map((k) => `${k.label} ${k.n}`);
  return `${parts.join(' · ')} · total ${totalOf(t)} (${calls} call${calls === 1 ? '' : 's'})`;
}

// renderUsage draws the summary: one row per model, most-spending first,
// each with its figures beside a bar showing the same numbers as
// segments of one track. Segments rather than one summed bar because
// these tokens are neither interchangeable nor priced alike, and a
// single bar summing them tells a comforting lie about where the spend
// went. The track is scaled to the largest model's total, which the key
// under the rows says — a bar whose scale is a secret is decoration, not
// information.
//
// Totals, never percentages: there is no window here to be a percent of,
// and the 70/90 context thresholds of the status line belong to this
// conversation's window fill, a different thing entirely.
export function renderUsage() {
  usageRowsEl.innerHTML = '';
  // No answer yet, or none coming: the totals are unknown, which is not
  // the same as none. "No usage recorded yet." under "the totals could not
  // be read" said both at once.
  if (!app.usageSummary) return;
  const models = app.usageSummary.models || {};
  const names = Object.keys(models).sort((a, b) => totalOf(models[b]) - totalOf(models[a]));
  if (names.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'meta';
    empty.textContent = 'No usage recorded yet.';
    usageRowsEl.appendChild(empty);
    return;
  }
  const largest = Math.max(...names.map((m) => totalOf(models[m])));
  for (const m of names) {
    const t = models[m];
    const row = document.createElement('div');
    row.className = 'usage-row';

    const name = document.createElement('div');
    name.className = 'usage-model';
    name.textContent = m;
    row.appendChild(name);

    const figures = document.createElement('div');
    figures.className = 'usage-figures';
    figures.textContent = figuresOf(t);
    row.appendChild(figures);

    const track = document.createElement('div');
    track.className = 'usage-track';
    track.title = `${m}: ${figuresOf(t)}, across every conversation, archived ones included`;
    for (const k of shown(t)) {
      const seg = document.createElement('div');
      seg.className = `usage-seg ${k.cls}`;
      seg.style.width = largest > 0 ? `${(k.n / largest) * 100}%` : '0%';
      track.appendChild(seg);
    }
    row.appendChild(track);

    usageRowsEl.appendChild(row);
  }
  usageRowsEl.appendChild(usageLegend(names.map((m) => models[m]), largest));
}

// usageLegend names each colour of segment some row drew, and only
// those, and the total the bars are scaled to. Under it, the daemon's own
// explanation of the cache figures when there are any, in the words
// /usage prints, so the window cannot explain them differently.
function usageLegend(rowsShown, largest) {
  const legend = document.createElement('div');
  legend.className = 'usage-legend';
  for (const k of KINDS) {
    if (!rowsShown.some((t) => shown(t).some((s) => s.cls === k.cls))) continue;
    const key = document.createElement('span');
    key.className = 'usage-key';
    const swatch = document.createElement('span');
    swatch.className = `usage-swatch ${k.cls}`;
    key.appendChild(swatch);
    key.appendChild(document.createTextNode(k.label));
    legend.appendChild(key);
  }
  const scale = document.createElement('span');
  scale.className = 'usage-scale';
  scale.textContent = `bars scaled to the largest total, ${largest} tokens`;
  legend.appendChild(scale);
  const note = app.usageSummary && app.usageSummary.note;
  if (typeof note === 'string' && note !== '') {
    const explain = document.createElement('p');
    explain.className = 'usage-legend-note';
    explain.textContent = note;
    legend.appendChild(explain);
  }
  return legend;
}

// renderUsageScope names what the figures cover and what they left out.
// The daemon names unreadable logs rather than skipping them in silence,
// and the window says so too: a total quietly missing a conversation is
// the failure this view would be judged on.
export function renderUsageScope() {
  const s = app.usageSummary || {};
  const sessions = count(s.sessions);
  // Sessions, not conversations: a sub-agent's, a scheduled run's and a
  // debate reviewer's are counted, and none is a conversation in the list.
  usageScopeEl.textContent =
    `Token usage across every conversation (${sessions} session${sessions === 1 ? '' : 's'})`;
  // Both lines, as /usage all prints both: what could not be read, and
  // what the figures are counted from.
  const lines = [];
  const unread = count(s.unread);
  if (unread > 0) {
    lines.push(`${unread} session log${unread === 1 ? '' : 's'} could not be read and ${unread === 1 ? 'is' : 'are'} not in this total.`);
  }
  lines.push('Counted from the sessions’ own logs, archived conversations and sub-agents included. A turn that was later undone still cost what it cost, so it is still counted.');
  usageNoteEl.textContent = lines.join(' ');
}

export function closeUsage() {
  usageView.close();
}

// asking counts the requests openUsage has made, so an answer to one a
// later opening has overtaken is dropped rather than drawn over the newer
// one.
let asking = 0;

// openUsage asks the daemon for the totals and draws them. The answer is
// the moment it was asked for; reopening the window asks again.
export async function openUsage() {
  const mine = ++asking;
  usageView.open();
  app.usageSummary = null;
  renderUsage();
  usageScopeEl.textContent = 'Reading every conversation’s log…';
  usageNoteEl.textContent = '';
  let summary;
  try {
    summary = await apiClient.getUsage('all');
  } catch (err) {
    if (mine !== asking) return;
    usageScopeEl.textContent = 'Token usage across every conversation';
    usageNoteEl.textContent = `The totals could not be read: ${err}`;
    return;
  }
  if (mine !== asking) return;
  app.usageSummary = summary && typeof summary === 'object' ? summary : {};
  renderUsage();
  renderUsageScope();
}

usageBtn.addEventListener('click', openUsage);
usageCloseBtn.addEventListener('click', closeUsage);
