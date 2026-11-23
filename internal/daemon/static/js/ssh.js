// The SSH panel: the terminals the model and the person work in, in a
// column of their own after the browser panel, in the desktop window and
// in the Web UI.
//
// The daemon holds each terminal's screen (internal/sshterm). The panel
// draws the one its tab strip selects from a stream of screens, and sends
// what the person types: keys from keydown, text from the input event (so
// an input method composing Korean sends what it composed, once), pastes
// from the paste event. One stream per page, for the conversation on
// screen and the terminal shown; its frames are {kind, data}:
//
//   state   the conversation's terminals, and how many times one of them
//           wanted the person (a question, a password prompt);
//   screen  the shown terminal's screen;
//   scroll  lines that scrolled off its top, numbered within its epoch.
//
// What only the person may answer (a host key, a password, a passphrase,
// a server's prompts) is asked in a box drawn by the page, not by the
// remote, and answered here; nothing in the conversation can answer it.

import { app, session } from './state.js';
import { api } from './api.js';
import { zoomKey } from './zoom.js';
import { expandPanel } from './resize.js';

const el = (id) => document.getElementById(id);

// clear removes a node's children, keeping keep.
function clear(node, keep = null) {
  if (!node) return;
  for (const child of [...node.children]) {
    if (child !== keep) child.remove();
  }
}

// VIEWER names this page to the daemon, for the terminal's size: the
// smallest visible panel showing a terminal sets it.
export const VIEWER = 'v' + Math.random().toString(36).slice(2, 10);

// Flags of a run, as internal/sshterm/screen.go numbers them.
export const FLAGS = { bold: 1, faint: 2, italic: 4, under: 8, reverse: 16, strike: 32, conceal: 64, blink: 128 };

// How many scrollback lines the panel keeps.
export const SCROLL_KEEP = 5000;

let es = null;
let streamFor = null;     // the conversation the stream is for
let streamTerm = '';      // the terminal it shows
const chosen = new Map(); // conversation -> the terminal its panel shows
const wantsSeen = new Map(); // conversation -> the last "wants" count acted on
let terms = [];           // the conversation's terminals, from the last state
let epoch = '';           // the shown terminal's scrollback numbering
let scrollNext = 0;       // the number of the next scrollback line expected
let rowKeys = [];         // the screen's rows as last drawn
let questionShown = 0;    // the id of the question whose form is drawn
const questionsSeen = new Set(); // questions that have shown the panel once
let composing = false;     // an input method is composing in the typing box
const queue = [];
let sending = false;
let sizeTimer = null;
let lastSize = '';

// sshShown is whether the panel is drawn for the conversation on screen.
export function sshShown() {
  return !!app.ssh && !!session.sessionID;
}

// isMac is whether the window's shortcuts are the Command ones.
export function isMac() {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || '');
}

const HEX = /^#[0-9a-fA-F]{6}$/;
const INDEX = /^(1[0-5]|[0-9])$/;

// runStyle is how a run of cells is drawn: its classes, and a colour of
// its own for a 256 or true colour. Reverse video swaps the two, using
// the terminal's own colours where the run has none.
export function runStyle(run) {
  const classes = [];
  let fg = run.f || '';
  let bg = run.b || '';
  const flags = run.a || 0;
  if (flags & FLAGS.reverse) {
    [fg, bg] = [bg, fg];
    if (!fg) classes.push('ssh-rev-fg');
    if (!bg) classes.push('ssh-rev-bg');
  }
  const style = {};
  if (INDEX.test(fg)) classes.push('f' + fg);
  else if (HEX.test(fg)) style.color = fg;
  if (INDEX.test(bg)) classes.push('b' + bg);
  else if (HEX.test(bg)) style.background = bg;
  if (flags & FLAGS.bold) classes.push('ssh-bold');
  if (flags & FLAGS.faint) classes.push('ssh-faint');
  if (flags & FLAGS.italic) classes.push('ssh-italic');
  if (flags & FLAGS.under) classes.push('ssh-under');
  if (flags & FLAGS.strike) classes.push('ssh-strike');
  if (flags & FLAGS.conceal) classes.push('ssh-conceal');
  if (run.w) classes.push('ssh-w');
  return { classes, style };
}

function rowEl(line) {
  const row = document.createElement('div');
  row.className = 'ssh-row';
  for (const run of line || []) {
    const span = document.createElement('span');
    span.textContent = run.t;
    const { classes, style } = runStyle(run);
    if (classes.length) span.className = classes.join(' ');
    if (style.color) span.style.color = style.color;
    if (style.background) span.style.background = style.background;
    row.appendChild(span);
  }
  return row;
}

// keyAction is what a key pressed in the terminal does:
//   ime    an input method is composing; its text comes as input.
//   pass   the window's own (zoom, Cmd shortcuts, copy, paste); left alone.
//   text   a character; the input event brings it.
//   ignore a modifier on its own.
//   key    a key for the shell, sent as {key, ctrl, alt, shift}.
//
// Copy is the window's while text is selected in the terminal: Cmd+C on
// macOS, Ctrl+C or Ctrl+Shift+C elsewhere. Without a selection Ctrl+C is
// the interrupt. Paste is the paste event's: Cmd+V, Ctrl+Shift+V,
// Shift+Insert. Ctrl+V on its own goes to the shell.
export function keyAction(e, mac = isMac(), selected = false) {
  if (e.isComposing || e.keyCode === 229) return { kind: 'ime' };
  if (zoomKey(e) !== null) return { kind: 'pass' };
  const k = typeof e.key === 'string' ? e.key : '';
  const lower = k.toLowerCase();
  if (e.metaKey) return { kind: 'pass' };
  if (!mac && e.ctrlKey && e.shiftKey && !e.altKey && (lower === 'c' || lower === 'v')) return { kind: 'pass' };
  if (!mac && e.ctrlKey && !e.shiftKey && !e.altKey && lower === 'c' && selected) return { kind: 'pass' };
  if (e.shiftKey && k === 'Insert') return { kind: 'pass' };
  if (['Shift', 'Control', 'Alt', 'Meta', 'CapsLock', 'Dead', 'Unidentified', 'Process', 'Compose', ''].includes(k)) {
    return { kind: 'ignore' };
  }
  const printable = [...k].length === 1;
  // A character the layout composed is text: Option on macOS, AltGr
  // (Ctrl+Alt) elsewhere.
  const composes = mac ? !e.ctrlKey : (!e.ctrlKey && !e.altKey) || (e.ctrlKey && e.altKey);
  if (printable && composes) return { kind: 'text', text: k };
  return { kind: 'key', key: { key: k, ctrl: !!e.ctrlKey, alt: !!e.altKey, shift: !!e.shiftKey } };
}

// fitSize is the terminal size a view fits: whole cells, inside the
// view's padding.
export function fitSize(width, height, cellWidth, rowHeight) {
  if (!(cellWidth > 0) || !(rowHeight > 0) || !(width > 0) || !(height > 0)) return null;
  return { cols: Math.floor((width - 12) / cellWidth), rows: Math.floor((height - 8) / rowHeight) };
}

function base(id, term) {
  return `/api/sessions/${encodeURIComponent(id)}/ssh/${encodeURIComponent(term)}`;
}

function showError(msg) {
  const e = el('ssh-error');
  if (!e) return;
  e.textContent = msg || '';
  e.hidden = !msg;
}

// send queues what the person typed. One request at a time, in order: two
// keys sent side by side can arrive the other way round. Text typed in a
// row is sent as one.
function send(item) {
  const id = streamFor;
  const term = streamTerm;
  if (!id || !term) return;
  const last = queue[queue.length - 1];
  if (last && last.id === id && last.term === term && item.text && last.body.text && !last.inFlight) {
    last.body.text += item.text;
  } else {
    queue.push({ id, term, body: item });
  }
  drain();
}

async function drain() {
  if (sending) return;
  sending = true;
  try {
    while (queue.length) {
      const next = queue[0];
      next.inFlight = true;
      try {
        await api('POST', `${base(next.id, next.term)}/input`, next.body);
        showError('');
      } catch (err) {
        showError(`Not sent: ${err.message || err}`);
      }
      queue.shift();
    }
  } finally {
    sending = false;
  }
}

function closeStream() {
  if (es) {
    es.close();
    es = null;
  }
}

function resetView() {
  rowKeys = [];
  epoch = '';
  scrollNext = 0;
  questionShown = 0;
  clear(el('ssh-scroll'));
  clear(el('ssh-screen'), el('ssh-cursor'));
  lastSize = '';
}

function openStream() {
  closeStream();
  const id = streamFor;
  if (!id) return;
  let url = `/api/sessions/${encodeURIComponent(id)}/ssh/stream?viewer=${VIEWER}`;
  if (streamTerm) {
    url += `&terminal=${encodeURIComponent(streamTerm)}`;
    if (epoch) url += `&epoch=${encodeURIComponent(epoch)}&from=${scrollNext}`;
  }
  const source = new EventSource(url);
  es = source;
  // The daemon forgets a viewer's size when its stream ends; a stream
  // that opens again says it again.
  source.onopen = () => {
    if (es !== source) return;
    lastSize = '';
    sendSize();
  };
  // A stream the daemon refused says nothing of why: ask the state route,
  // whose answer does.
  source.onerror = () => {
    if (es !== source || source.readyState !== 2) return;
    api('GET', `/api/sessions/${encodeURIComponent(id)}/ssh`).catch((err) => {
      if (es === source) showError(`The SSH panel cannot reach its terminals: ${err.message || err}`);
    });
  };
  source.onmessage = (e) => {
    if (es !== source) return;
    let m;
    try {
      m = JSON.parse(e.data);
    } catch (err) {
      return;
    }
    if (!m || typeof m !== 'object') return;
    if (m.kind === 'state') onState(m.data || {});
    else if (m.kind === 'screen') drawScreen(m.data || {});
    else if (m.kind === 'scroll') addScroll(m.data || {});
  };
}

// show points the panel at terminal term of the conversation on screen.
function show(term) {
  if (term === streamTerm) return;
  chosen.set(streamFor, term);
  streamTerm = term;
  resetView();
  openStream();
  sendSize();
}

function current() {
  return terms.find((t) => t.id === streamTerm) || null;
}

function onState(st) {
  terms = Array.isArray(st.terminals) ? st.terminals : [];
  const id = streamFor;
  // A terminal waiting for an answer, or a command waiting for a
  // password, wants the person: the panel comes into view, and shows the
  // terminal that asks.
  const wants = Number(st.wants) || 0;
  const seen = wantsSeen.has(id) ? wantsSeen.get(id) : null;
  wantsSeen.set(id, wants);
  // Once per question, and once per time a command wanted the person: a
  // panel the person hid while a question stays open stays hidden.
  const asking = terms.find((t) => t.question);
  const newQuestion = asking && !questionsSeen.has(`${id}/${asking.id}/${asking.epoch}/${asking.question.id}`);
  if (newQuestion) questionsSeen.add(`${id}/${asking.id}/${asking.epoch}/${asking.question.id}`);
  if ((seen !== null && wants > seen) || newQuestion) {
    if (asking && asking.id !== streamTerm) show(asking.id);
    reveal();
  }
  if (!current()) {
    const pick = chosen.get(id);
    const next = terms.find((t) => t.id === pick) || terms[terms.length - 1];
    if (next && next.id !== streamTerm) {
      show(next.id);
    } else if (!next && streamTerm) {
      streamTerm = '';
      resetView();
    }
  }
  drawTabs();
  drawBar();
  drawQuestion();
}

// reveal shows a collapsed panel, and marks the toggle until it is seen.
function reveal() {
  const panel = el('ssh-panel');
  if (!panel || panel.hidden) return;
  if (panel.classList.contains('collapsed')) {
    expandPanel('ssh');
  }
}

function stateOf(t) {
  if (t.question) return 'asking';
  if (t.held) return 'held';
  return t.state;
}

function drawTabs() {
  const list = el('ssh-tab-list');
  if (!list) return;
  clear(list);
  for (const t of terms) {
    const tab = document.createElement('div');
    tab.className = 'ssh-tab' + (t.id === streamTerm ? ' active' : '');
    const open = document.createElement('button');
    open.className = 'ssh-tab-open';
    open.setAttribute('role', 'tab');
    open.setAttribute('aria-selected', String(t.id === streamTerm));
    const dot = document.createElement('span');
    dot.className = 'ssh-dot ' + stateOf(t);
    // What was asked for: an alias reads better than the address it
    // resolved to, which the status line and the tooltip give.
    const label = document.createElement('span');
    label.textContent = t.spec || t.target;
    open.title = `${t.id}: ${t.target || t.spec}, ${t.question ? 'waiting for you' : t.state}${t.held ? ', held' : ''}`;
    open.append(dot, label);
    open.addEventListener('click', () => show(t.id));
    const close = document.createElement('button');
    close.className = 'ssh-tab-close';
    close.title = t.state === 'ended' ? 'remove this terminal' : 'close this connection';
    close.textContent = '×';
    close.addEventListener('click', async () => {
      try {
        await api('DELETE', base(streamFor, t.id), {});
      } catch (err) {
        showError(`Not closed: ${err.message || err}`);
      }
    });
    tab.append(open, close);
    list.appendChild(tab);
  }
  const empty = el('ssh-empty');
  if (empty) empty.hidden = terms.length > 0;
}

function drawBar() {
  const bar = el('ssh-bar');
  const t = current();
  if (bar) bar.hidden = !t;
  const term = el('ssh-term');
  if (term) term.hidden = !t;
  if (!t) return;
  const where = t.target || t.spec;
  let status;
  if (t.question) status = `${where}: waiting for you`;
  else if (t.state === 'connecting') status = `connecting to ${where}`;
  else if (t.state === 'ended') status = `ended: ${t.reason || 'closed'}`;
  else {
    const parts = [where];
    if (t.shell) parts.push(t.shell);
    if (t.held) parts.push('held: the model can neither see nor type here');
    else if (t.running) parts.push('a command is running');
    if (t.input_lost) parts.push('some input was dropped: the shell was not reading');
    status = parts.join(' · ');
  }
  const s = el('ssh-status');
  if (s) {
    s.textContent = status;
    s.title = status;
  }
  const hold = el('ssh-hold');
  if (hold) {
    hold.setAttribute('aria-pressed', String(!!t.held));
    hold.textContent = t.held ? 'Let go' : 'Hold';
    hold.hidden = t.state === 'ended';
  }
  const again = el('ssh-reconnect');
  if (again) again.hidden = t.state !== 'ended';
}

async function answer(t, q, body, fields = []) {
  // A secret does not stay in the page, whatever the answer was.
  for (const f of fields) f.value = '';
  try {
    await api('POST', `${base(streamFor, t.id)}/answer`, { question: q.id, ...body });
    showError('');
  } catch (err) {
    showError(`Not answered: ${err.message || err}`);
  }
}

function button(text, onClick) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'icon-btn';
  b.textContent = text;
  b.addEventListener('click', onClick);
  return b;
}

function drawQuestion() {
  const box = el('ssh-question');
  if (!box) return;
  const t = current();
  const q = t && t.question;
  if (!q) {
    box.hidden = true;
    clear(box);
    questionShown = 0;
    return;
  }
  if (questionShown === q.id && !box.hidden) return;
  questionShown = q.id;
  clear(box);
  const who = document.createElement('div');
  who.className = 'ssh-q-who';
  who.textContent = `localcode asks, for ${q.for}:`;
  box.appendChild(who);
  const actions = document.createElement('div');
  actions.className = 'ssh-q-actions';
  if (q.kind === 'host_key' && q.host_key) {
    const p = document.createElement('div');
    p.textContent = `${q.host_key.host} is not a host this computer knows. Its ${q.host_key.key_type} key has the fingerprint:`;
    const fp = document.createElement('div');
    fp.className = 'ssh-q-fp';
    fp.textContent = q.host_key.fingerprint;
    const ask = document.createElement('div');
    ask.textContent = 'Accept it only if it is the fingerprint the host\'s administrator gave you. Accepted, it is kept in ~/.localcode/ssh/known_hosts.';
    box.append(p, fp, ask);
    actions.append(
      button('Reject', () => answer(t, q, { accept: false })),
      button('Accept', () => answer(t, q, { accept: true, fingerprint: q.host_key.fingerprint })),
    );
    box.appendChild(actions);
    box.hidden = false;
    return;
  }
  const fields = [];
  const form = document.createElement('form');
  form.className = 'ssh-q-form';
  const field = (labelText, echo) => {
    const label = document.createElement('label');
    const span = document.createElement('span');
    span.textContent = labelText;
    const input = document.createElement('input');
    input.type = echo ? 'text' : 'password';
    input.autocomplete = 'off';
    input.spellcheck = false;
    label.append(span, input);
    form.appendChild(label);
    fields.push(input);
  };
  if (q.kind === 'password') field('Password', false);
  else if (q.kind === 'passphrase') field(`Passphrase for ${q.key}`, false);
  else {
    if (q.instruction) {
      const p = document.createElement('div');
      p.textContent = q.instruction;
      form.appendChild(p);
    }
    for (const pr of q.prompts || []) field(pr.text, !!pr.echo);
  }
  const submit = () => {
    const values = fields.map((f) => f.value);
    answer(t, q, { accept: true, values }, fields);
  };
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    submit();
  });
  actions.append(button('Cancel', () => answer(t, q, { accept: false }, fields)), button('OK', submit));
  form.appendChild(actions);
  box.appendChild(form);
  box.hidden = false;
  if (fields[0] && typeof fields[0].focus === 'function') fields[0].focus();
}

function atBottom(lines) {
  return lines.scrollTop + lines.clientHeight >= lines.scrollHeight - 4;
}

function drawScreen(d) {
  if (d.terminal !== streamTerm || !d.screen) return;
  const lines = el('ssh-lines');
  const screen = el('ssh-screen');
  if (!lines || !screen) return;
  const stick = atBottom(lines);
  const s = d.screen;
  const rows = Array.isArray(s.lines) ? s.lines : [];
  const cursor = el('ssh-cursor');
  // Only rows that changed are drawn again, so a selection on the others
  // survives the next screen.
  const existing = [...screen.children].filter((c) => c !== cursor);
  for (let y = 0; y < rows.length; y++) {
    const key = JSON.stringify(rows[y]);
    if (rowKeys[y] === key && existing[y]) continue;
    const row = rowEl(rows[y]);
    if (existing[y]) {
      screen.insertBefore(row, existing[y]);
      existing[y].remove();
    } else {
      screen.appendChild(row);
    }
    existing[y] = row;
    rowKeys[y] = key;
  }
  for (let y = rows.length; y < existing.length; y++) {
    if (existing[y]) existing[y].remove();
  }
  rowKeys.length = rows.length;
  if (cursor) {
    cursor.hidden = !s.cursor;
    cursor.style.left = `${Number(s.cx) || 0}ch`;
    cursor.style.top = `calc(${Number(s.cy) || 0} * 1.3em)`;
  }
  if (stick) lines.scrollTop = lines.scrollHeight;
  placeInput();
}

// How far the typing box stays from the right and bottom edges it is
// kept inside, in layout pixels: the two kinds of pixel are converted
// with widths the engine rounds, and a caret can be a fraction of a
// pixel taller than its box.
const INPUT_EDGE = 2;

// inputPlace is where the typing box goes, as left and top in the page's
// layout pixels: at the cursor, and inside what the window shows of the
// terminal's box.
//
// c and t are the cursor's and the terminal's rectangles, in the window's
// pixels. m is the terminal's layout: its border box (offsetWidth,
// offsetHeight), its border (clientLeft, clientTop), its inside
// (clientWidth, clientHeight), the typing box's size (boxWidth,
// boxHeight), and the window's size (winWidth, winHeight).
//
// The window's pixels and the layout's differ by the zoom the page
// applies to itself (js/zoom.js, a CSS zoom on the root) in an engine
// that reports rectangles as drawn (Chrome and Edge from 128). Set from
// the rectangles alone, the box was the zoom factor further from the
// terminal's corner than the cursor is: at 125%, 128 pixels under the
// terminal's box for a cursor on its last row. The factor is read from
// the terminal's own two widths, so it is 1 in an engine that reports
// rectangles in layout pixels.
//
// A typing box that sticks out of a box that clips it is brought into
// view by the engine on every character typed, by scrolling that box,
// whatever its overflow says, and the document after it. The screen
// jumped by as much, and jumped back when the next screen moved the
// typing box. So the place is also kept inside the terminal's box and
// inside the window: for a cursor in the last column, a terminal wider
// than its panel, lines scrolled back, a panel partly outside a narrow
// window.
export function inputPlace(c, t, m) {
  const kx = m.offsetWidth > 0 && t.width > 0 ? t.width / m.offsetWidth : 1;
  const ky = m.offsetHeight > 0 && t.height > 0 ? t.height / m.offsetHeight : kx;
  const x = (px) => (px - t.left) / kx - (m.clientLeft || 0);
  const y = (px) => (px - t.top) / ky - (m.clientTop || 0);
  let x0 = 0;
  let y0 = 0;
  let x1 = m.clientWidth;
  let y1 = m.clientHeight;
  if (m.winWidth > 0 && m.winHeight > 0) {
    x0 = Math.max(x0, x(0));
    y0 = Math.max(y0, y(0));
    x1 = Math.min(x1, x(m.winWidth));
    y1 = Math.min(y1, y(m.winHeight));
  }
  // Inside what the window shows, where that has room for the box; inside
  // the terminal's box whatever the window shows. A terminal with less of
  // it in the window than the box is wide has no place that is both, and
  // the terminal's box is the one a key would otherwise move.
  const fit = (want, lo, hi, max) => {
    const shown = Math.max(lo, Math.min(want, hi));
    return Math.round(Math.max(0, Math.min(shown, max)) * 100) / 100;
  };
  return {
    left: fit(x(c.left), x0, x1 - m.boxWidth - INPUT_EDGE, m.clientWidth - m.boxWidth - INPUT_EDGE),
    top: fit(y(c.top), y0, y1 - m.boxHeight - INPUT_EDGE, m.clientHeight - m.boxHeight - INPUT_EDGE),
  };
}

// placeInput puts the typing box at the cursor, so an input method opens
// its candidates beside the text it composes.
function placeInput() {
  const input = el('ssh-input');
  const cursor = el('ssh-cursor');
  const term = el('ssh-term');
  if (!input || !cursor || !term || typeof cursor.getBoundingClientRect !== 'function') return;
  const c = cursor.getBoundingClientRect();
  const t = term.getBoundingClientRect();
  if (!c || !t) return;
  // The window's size as a rectangle gives it, so it is in the pixels the
  // other two rectangles are in, whichever pixels an engine reports for a
  // zoomed page.
  const root = typeof document !== 'undefined' ? document.documentElement : null;
  const win = root && typeof root.getBoundingClientRect === 'function' ? root.getBoundingClientRect() : null;
  const at = inputPlace(c, t, {
    offsetWidth: term.offsetWidth || 0,
    offsetHeight: term.offsetHeight || 0,
    clientLeft: term.clientLeft || 0,
    clientTop: term.clientTop || 0,
    clientWidth: term.clientWidth || 0,
    clientHeight: term.clientHeight || 0,
    boxWidth: input.offsetWidth || 0,
    boxHeight: input.offsetHeight || 0,
    winWidth: (win && win.width) || 0,
    winHeight: (win && win.height) || 0,
  });
  input.style.left = `${at.left}px`;
  input.style.top = `${at.top}px`;
}

function addScroll(d) {
  if (d.terminal !== streamTerm) return;
  const scroll = el('ssh-scroll');
  const lines = el('ssh-lines');
  if (!scroll) return;
  const stick = lines ? atBottom(lines) : false;
  const from = Number(d.from) || 0;
  let add = Array.isArray(d.lines) ? d.lines : [];
  if (d.epoch !== epoch || from > scrollNext) {
    // Another terminal under this id (a reconnect, made here or in
    // another window), or lines the daemon no longer has: start again
    // from what came, numbered from it.
    clear(scroll);
    epoch = d.epoch;
    scrollNext = from;
  } else if (from < scrollNext) {
    add = add.slice(scrollNext - from);
  }
  for (const line of add) scroll.appendChild(rowEl(line));
  while (scroll.children.length > SCROLL_KEEP) scroll.firstChild.remove();
  scrollNext = Math.max(scrollNext, from + (Array.isArray(d.lines) ? d.lines.length : 0));
  if (stick && lines) lines.scrollTop = lines.scrollHeight;
}

// measure is a monospace cell's width and a row's height, in pixels.
function measure() {
  const lines = el('ssh-lines');
  if (!lines) return null;
  // A row as the screen draws one: its height is the line height, which
  // a span's is not.
  const probe = document.createElement('div');
  probe.className = 'ssh-row';
  probe.style.visibility = 'hidden';
  probe.style.position = 'absolute';
  const cells = document.createElement('span');
  cells.textContent = 'MMMMMMMMMM';
  probe.appendChild(cells);
  lines.appendChild(probe);
  const w = cells.offsetWidth / 10;
  const h = probe.offsetHeight;
  probe.remove();
  return w > 0 && h > 0 ? { w, h } : null;
}

// sendSize tells the daemon the size this panel fits, or that it fits
// none (hidden, collapsed): a hidden panel must not shrink the screen
// another one shows.
function sendSize() {
  if (sizeTimer) clearTimeout(sizeTimer);
  sizeTimer = setTimeout(() => {
    sizeTimer = null;
    const id = streamFor;
    const term = streamTerm;
    if (!id || !term) return;
    const panel = el('ssh-panel');
    const lines = el('ssh-lines');
    let size = { cols: 0, rows: 0 };
    if (panel && !panel.hidden && !panel.classList.contains('collapsed') && lines) {
      const m = measure();
      const fit = m && fitSize(lines.clientWidth, lines.clientHeight, m.w, m.h);
      if (fit) size = fit;
    }
    const key = `${id}/${term}/${size.cols}x${size.rows}`;
    if (key === lastSize) return;
    lastSize = key;
    api('POST', `${base(id, term)}/size`, { viewer: VIEWER, cols: size.cols, rows: size.rows }).catch(() => {});
  }, 150);
}

// renderSSHPanel shows or hides the panel and points its stream at the
// conversation on screen. Called when the setting changes and when a
// conversation is opened.
export function renderSSHPanel() {
  const panel = el('ssh-panel');
  if (!panel) return;
  const shown = sshShown();
  panel.hidden = !shown;
  const handle = el('resize-ssh');
  if (handle) handle.hidden = !shown;
  const toggle = el('toggle-ssh');
  if (toggle) {
    toggle.hidden = !shown;
    toggle.setAttribute('aria-expanded', String(!panel.classList.contains('collapsed')));
  }
  if (!shown) {
    closeStream();
    streamFor = null;
    streamTerm = '';
    terms = [];
    resetView();
    return;
  }
  if (streamFor !== session.sessionID) {
    streamFor = session.sessionID;
    streamTerm = chosen.get(streamFor) || '';
    terms = [];
    resetView();
    drawTabs();
    drawBar();
    drawQuestion();
    showError('');
    openStream();
  }
  sendSize();
}

async function connect(target) {
  const id = streamFor;
  if (!id || !target) return;
  try {
    const res = await api('POST', `/api/sessions/${encodeURIComponent(id)}/ssh/connect`, { target });
    if (id !== streamFor) return;
    el('ssh-connect').hidden = true;
    el('ssh-target').value = '';
    showError('');
    if (res && res.terminal) show(res.terminal);
  } catch (err) {
    showError(`Not connected: ${err.message || err}`);
  }
}

// initSSHPanel wires the panel's controls. Once, at startup.
export function initSSHPanel() {
  const term = el('ssh-term');
  const input = el('ssh-input');
  const lines = el('ssh-lines');
  if (!term || !input || !lines) return;

  el('ssh-tab-new')?.addEventListener('click', () => {
    const form = el('ssh-connect');
    form.hidden = !form.hidden;
    if (!form.hidden) el('ssh-target').focus();
  });
  el('ssh-connect-cancel')?.addEventListener('click', () => {
    el('ssh-connect').hidden = true;
  });
  el('ssh-connect')?.addEventListener('submit', (e) => {
    e.preventDefault();
    connect(el('ssh-target').value.trim());
  });
  el('ssh-hold')?.addEventListener('click', async () => {
    const t = current();
    if (!t) return;
    try {
      await api('POST', `${base(streamFor, t.id)}/hold`, { on: !t.held });
    } catch (err) {
      showError(`Not changed: ${err.message || err}`);
    }
  });
  el('ssh-reconnect')?.addEventListener('click', async () => {
    const t = current();
    if (!t) return;
    try {
      await api('POST', `${base(streamFor, t.id)}/reconnect`, {});
      // The same id, a new epoch: the stream starts the screen again.
      resetView();
      openStream();
      sendSize();
    } catch (err) {
      showError(`Not reconnected: ${err.message || err}`);
    }
  });

  const selected = () => {
    const sel = typeof window !== 'undefined' && window.getSelection ? window.getSelection() : null;
    return !!sel && !sel.isCollapsed && String(sel).length > 0;
  };
  const onKey = (e) => {
    const a = keyAction(e, isMac(), selected());
    if (a.kind === 'pass') return;
    // A key an input method is composing with is the input method's: not
    // sent, not prevented, and not the page's either (Escape there would
    // stop the turn).
    if (a.kind === 'ime') {
      e.stopPropagation();
      return;
    }
    // Everything else is the terminal's: Escape is not "stop the turn",
    // Tab not "next agent", Ctrl+F not "find" while the terminal has the
    // keys.
    e.stopPropagation();
    if (a.kind === 'ignore') return;
    if (a.kind === 'text') {
      if (e.target === input) return; // the input event brings it
      e.preventDefault();
      send({ text: a.text });
      input.focus({ preventScroll: true });
      return;
    }
    e.preventDefault();
    send({ key: a.key });
  };
  input.addEventListener('keydown', onKey);
  term.addEventListener('keydown', (e) => {
    if (e.target !== input) onKey(e);
  });
  // Text reaches the shell from the typing box by position: everything
  // before sentUpTo has been sent. An input method composes at the end of
  // the box (from compStart), and what it composes is sent once it
  // commits, after the browser has finished with the event. The box is
  // emptied only when nothing is being composed and nothing waits to be
  // sent: emptied in the middle, it lost or resent the start of the next
  // syllable.
  let sentUpTo = 0;
  let compStart = 0;
  let pending = null;
  const sendUpTo = (end) => {
    const text = input.value.substring(sentUpTo, end);
    sentUpTo = Math.max(sentUpTo, end);
    if (text) send({ text });
  };
  const tidy = () => {
    if (composing || pending) return;
    input.value = '';
    sentUpTo = 0;
    compStart = 0;
  };
  input.addEventListener('compositionstart', () => {
    // Plain text typed before the composition goes first.
    sendUpTo(input.value.length);
    composing = true;
    compStart = input.value.length;
  });
  input.addEventListener('compositionend', () => {
    composing = false;
    if (pending) clearTimeout(pending);
    pending = setTimeout(() => {
      pending = null;
      // A composition started since (the next syllable) is not sent.
      sendUpTo(composing ? compStart : input.value.length);
      tidy();
    }, 0);
  });
  input.addEventListener('input', (e) => {
    if (composing || e.isComposing || pending) return;
    sendUpTo(input.value.length);
    tidy();
  });
  term.addEventListener('paste', (e) => {
    const text = e.clipboardData && e.clipboardData.getData('text');
    e.preventDefault();
    if (text) send({ paste: text });
  });
  // A click puts the keys in the terminal; a drag selects text to copy.
  lines.addEventListener('mouseup', () => {
    if (!selected()) input.focus({ preventScroll: true });
  });
  // Nothing in the terminal's box is larger than it: its lines scroll in a
  // box of their own. A scroll of the terminal's box is the engine
  // bringing something into view, and it moves the whole screen: put back
  // before it is drawn.
  term.addEventListener('scroll', () => {
    if (term.scrollTop) term.scrollTop = 0;
    if (term.scrollLeft) term.scrollLeft = 0;
  });
  // A view that changed size moved the cursor without a new screen.
  const resized = () => {
    sendSize();
    placeInput();
  };
  if (typeof ResizeObserver !== 'undefined') {
    new ResizeObserver(resized).observe(term);
  }
  if (typeof window !== 'undefined' && window.addEventListener) {
    window.addEventListener('resize', resized);
  }
  el('toggle-ssh')?.addEventListener('click', () => sendSize());
}
