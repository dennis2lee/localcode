import { transcriptEl } from './dom.js';
import { renderMarkdown } from './markdown.js';
import { session } from './state.js';

// The only module allowed to touch transcriptEl. Everything goes through
// createElement/textContent — no call site anywhere else builds an HTML
// string for the transcript, which is what closes the escape-a-string class
// of bug (B5) for good: there is nowhere left to forget it.
function appendDiv(cls, text) {
  const div = document.createElement('div');
  div.className = cls;
  div.textContent = text;
  transcriptEl.appendChild(div);
  transcriptEl.scrollTop = transcriptEl.scrollHeight;
  return div;
}

export function appendUser(text) { return appendDiv('msg-user', 'You: ' + text); }

// A prompt sent mid-turn shows a placeholder straight away — the wait
// until the model is handed it can be minutes, and silence in between
// reads as the message having gone nowhere. When the real line arrives
// (the message.user event the daemon writes at that moment) the
// placeholder is removed, so the transcript ends up with one entry per
// message rather than two, and matches what a reload would show.
const sentPlaceholders = new Map(); // text -> [element]

export function appendPendingUser(text) {
  const div = appendDiv('msg-tool', `[sent — the model will pick this up at its next step] ${text}`);
  const list = sentPlaceholders.get(text) || [];
  list.push(div);
  sentPlaceholders.set(text, list);
  return div;
}

export function resolvePendingUser(text) {
  const list = sentPlaceholders.get(text);
  if (!list || list.length === 0) return;
  // Oldest first: the same text can be sent twice, and each send owns one
  // placeholder.
  const div = list.shift();
  if (list.length === 0) sentPlaceholders.delete(text);
  div.remove();
}
export function appendTool(text) { return appendDiv('msg-tool', text); }
export function appendError(err) { return appendDiv('msg-error', 'Error: ' + String(err)); }

// appendToolLines renders a client-side command's multi-line reply (e.g.
// /help, /agent) as one bubble with <br> breaks between lines — the one
// place that still needs literal markup, since textContent would collapse
// the newlines into a single unreadable line. text is a plain string built
// entirely from data already safe to display verbatim (the caller is
// responsible for that, same as it always was for HELP_TEXT and friends).
export function appendToolLines(lines) {
  const div = document.createElement('div');
  div.className = 'msg-tool';
  lines.forEach((line, i) => {
    if (i > 0) div.appendChild(document.createElement('br'));
    div.appendChild(document.createTextNode(line));
  });
  transcriptEl.appendChild(div);
  transcriptEl.scrollTop = transcriptEl.scrollHeight;
  return div;
}

// Tool calls in the transcript.
//
// These used to show only as a name in the status bar, which vanished the
// moment the tool finished. A turn that spends minutes in tools therefore
// produced a transcript that said nothing at all while it ran and nothing
// afterwards about what it had done — the report was a blinking light and
// no output. One line per call, written when the call starts and completed
// when it ends, is the missing half of "show me what is happening".
//
// One line, not the output: a file read is thousands of lines and would
// bury the conversation. The full arguments and result are one click away
// on the row itself.
const ARG_KEYS = ['command', 'path', 'file_path', 'pattern', 'query', 'url', 'name', 'prompt', 'description'];

// summarizeInput picks the one value worth showing beside a tool's name —
// the command for bash, the path for a file read. Falls back to the first
// string in the object, then to the raw JSON, so an unknown tool (an MCP
// server's, say) still says something rather than nothing.
export function summarizeInput(inputJSON) {
  let obj;
  try { obj = JSON.parse(inputJSON || '{}'); } catch { return oneLine(inputJSON || ''); }
  if (!obj || typeof obj !== 'object') return oneLine(String(obj ?? ''));
  for (const k of ARG_KEYS) {
    if (typeof obj[k] === 'string' && obj[k]) return oneLine(obj[k]);
  }
  for (const v of Object.values(obj)) {
    if (typeof v === 'string' && v) return oneLine(v);
  }
  const keys = Object.keys(obj);
  return keys.length ? oneLine(JSON.stringify(obj)) : '';
}

function oneLine(s) {
  const flat = s.replace(/\s+/g, ' ').trim();
  return flat.length > 140 ? flat.slice(0, 139) + '…' : flat;
}

export function appendToolCall(toolUseID, name, inputJSON) {
  const row = document.createElement('div');
  row.className = 'msg-toolcall running';

  const head = document.createElement('div');
  head.className = 'head';

  const marker = document.createElement('span');
  marker.className = 'marker';
  marker.textContent = '▸';
  head.appendChild(marker);

  const nameEl = document.createElement('span');
  nameEl.className = 'name';
  nameEl.textContent = name || 'tool';
  head.appendChild(nameEl);

  const argEl = document.createElement('span');
  argEl.className = 'arg';
  argEl.textContent = summarizeInput(inputJSON);
  head.appendChild(argEl);

  const stateEl = document.createElement('span');
  stateEl.className = 'state';
  stateEl.textContent = 'running…';
  head.appendChild(stateEl);

  // The full arguments are available from the start; the result is added
  // to the same block when the call ends.
  const detail = document.createElement('pre');
  detail.className = 'detail';
  detail.hidden = true;
  detail.textContent = prettyJSON(inputJSON);

  head.title = 'click to show the full arguments and result';
  head.addEventListener('click', () => { detail.hidden = !detail.hidden; });

  row.appendChild(head);
  row.appendChild(detail);
  transcriptEl.appendChild(row);
  transcriptEl.scrollTop = transcriptEl.scrollHeight;
  session.toolRows.set(toolUseID, { row, stateEl, marker, detail });
  return row;
}

export function finishToolCall(toolUseID, content, isError) {
  const entry = session.toolRows.get(toolUseID);
  if (!entry) return;
  session.toolRows.delete(toolUseID);
  const { row, stateEl, marker, detail } = entry;
  row.classList.remove('running');
  row.classList.toggle('failed', !!isError);
  marker.textContent = isError ? '✗' : '✓';
  const text = String(content ?? '');
  stateEl.textContent = isError ? 'failed' : resultSize(text);
  detail.textContent = `${detail.textContent}\n\n${text}`;
  transcriptEl.scrollTop = transcriptEl.scrollHeight;
}

// resultSize describes a tool result without showing it: a short one is
// worth reading inline, a long one is better summed up by its size than
// by its first line taken out of context.
function resultSize(text) {
  if (!text) return 'done';
  const lines = text.split('\n');
  if (lines.length === 1 && text.length <= 60) return text;
  return `${lines.length} line${lines.length === 1 ? '' : 's'}`;
}

function prettyJSON(s) {
  try { return JSON.stringify(JSON.parse(s || '{}'), null, 2); } catch { return String(s || ''); }
}

export function appendModelText(text) {
  if (!session.currentModelEl) {
    session.currentModelEl = document.createElement('div');
    session.currentModelEl.className = 'msg-model';
    transcriptEl.appendChild(session.currentModelEl);
    session.currentModelBuffer = '';
  }
  session.currentModelBuffer += text;
  session.currentModelEl.innerHTML = renderMarkdown(session.currentModelBuffer);
  transcriptEl.scrollTop = transcriptEl.scrollHeight;
}

export function endModelText() {
  session.currentModelEl = null;
  session.currentModelBuffer = '';
}

export function clearTranscript() {
  transcriptEl.innerHTML = '';
  session.currentModelEl = null;
  session.currentModelBuffer = '';
  // The rows these pointed at are gone with the innerHTML above; holding
  // them would leave finishToolCall or resolvePendingUser writing into
  // detached nodes.
  session.toolRows.clear();
  sentPlaceholders.clear();
}
