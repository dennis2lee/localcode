import { transcriptEl, jumpBottomBtn } from './dom.js';
import { renderMarkdown } from './markdown.js';
import { createFollower } from './scroll.js';
import { session } from './state.js';

// The transcript follows the newest output only while the reader is at
// the bottom of it. See scroll.js: this is the module that owns
// transcriptEl, so it owns the following too, and every change to the
// content below goes through follower.keeping() rather than writing
// scrollTop itself.
const follower = createFollower(transcriptEl, (following) => {
  jumpBottomBtn.hidden = following;
});
jumpBottomBtn.addEventListener('click', () => follower.force());

// scrollToBottom is the deliberate one: the reader asked to be at the
// bottom (sent a prompt, opened a session), so following resumes whether
// or not they had scrolled away.
export function scrollToBottom() { follower.force(); }

// The only module allowed to touch transcriptEl. Everything goes through
// createElement/textContent — no call site anywhere else builds an HTML
// string for the transcript, which is what closes the escape-a-string class
// of bug (B5) for good: there is nowhere left to forget it.
function appendDiv(cls, text) {
  const div = document.createElement('div');
  div.className = cls;
  div.textContent = text;
  follower.keeping(() => transcriptEl.appendChild(div));
  return div;
}

export function appendUser(text) {
  const div = appendDiv('msg-user', 'You: ' + text);
  scrollToBottom();
  return div;
}

// A prompt shows a placeholder straight away — the wait until the model is
// handed it can be seconds (a turn starting) or minutes (a turn already
// running), and silence in between reads as the message having gone
// nowhere. When the real line arrives (the message.user event the daemon
// writes at that moment) the placeholder is removed, so the transcript ends
// up with one entry per message rather than two, and matches what a reload
// would show.
//
// Two shapes, because the two waits mean different things. Sending into a
// running turn is worth explaining — the model picks it up at its next
// step, not now. An ordinary prompt is not: it is going to be answered, so
// it is drawn as the user line it is about to become, just dimmed until
// the daemon confirms it. Before this the ordinary case drew nothing at
// all, and a prompt typed into an idle session sat invisible until the
// model started work on it.
const sentPlaceholders = new Map(); // text -> [element]

export function appendPendingUser(text, midTurn = false) {
  const div = midTurn
    ? appendDiv('msg-tool', `[sent — the model will pick this up at its next step] ${text}`)
    : appendDiv('msg-user pending', 'You: ' + text);
  const list = sentPlaceholders.get(text) || [];
  list.push(div);
  sentPlaceholders.set(text, list);
  // Sending is a deliberate act, and the answer is going to arrive at the
  // bottom. Somebody who had scrolled up to re-read something and then
  // typed wants to watch the reply, not stay where they were.
  scrollToBottom();
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
  follower.keeping(() => transcriptEl.appendChild(div));
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
  follower.keeping(() => transcriptEl.appendChild(row));
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
  follower.keeping(() => {
    stateEl.textContent = isError ? 'failed' : resultSize(text);
    detail.textContent = `${detail.textContent}\n\n${text}`;
  });
}

// abandonRunningToolCalls closes out every row still spinning.
//
// Cancelling a turn stops the tool call where it is, and the daemon emits
// no tool.end for it — nothing ran to completion, so there is no result
// to report. The rows were left spinning under the "[cancelled]" line
// forever, and their entries leaked until the session was switched.
export function abandonRunningToolCalls(why) {
  for (const [id, entry] of session.toolRows) {
    const { row, stateEl, marker } = entry;
    row.classList.remove('running');
    marker.textContent = '–';
    stateEl.textContent = why;
    session.toolRows.delete(id);
  }
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
  follower.keeping(() => {
    session.currentModelBuffer += text;
    session.currentModelEl.innerHTML = renderMarkdown(session.currentModelBuffer);
  });
}

// endModelText closes off one model message. text is the whole reply, as
// the daemon recorded it.
//
// Passing it matters on replay. A conversation being re-opened does not
// receive the deltas of replies that already finished — they are the same
// characters as the message.part.end that follows them, and sending both
// meant the client re-rendered markdown once per fragment for text it was
// about to replace, and meant a "last 400 events" window could be filled
// entirely by one long answer. So on replay this is the only place the
// text arrives, and it has to be drawn here.
export function endModelText(text) {
  if (!session.currentModelEl && text) appendModelText(text);
  session.currentModelEl = null;
  session.currentModelBuffer = '';
}

// appendReview draws one round of a debate: what the reviewing agent
// said about this session's work.
//
// Its own shape rather than a model message or a user one, because in a
// two-agent conversation the one thing a reader must never have to guess
// is which of them is talking. The header names the agent, its model, the
// round and the verdict; the body is rendered as markdown like any other
// model output, since that is what it is.
export function appendReview(d) {
  const wrap = document.createElement('div');
  wrap.className = 'msg-review' + (d.approved ? ' approved' : '');

  const head = document.createElement('div');
  head.className = 'head';
  const who = d.reviewer || 'reviewer';
  const model = d.model ? ` (${d.model})` : '';
  head.textContent = `${who}${model} · round ${d.round || 0}/${d.rounds || 0} · ` +
    (d.approved ? 'approved' : 'changes requested');
  wrap.appendChild(head);

  const body = document.createElement('div');
  body.className = 'body msg-model';
  body.innerHTML = renderMarkdown(String(d.text || ''));
  wrap.appendChild(body);

  follower.keeping(() => transcriptEl.appendChild(wrap));
  return wrap;
}

// The model's reasoning, while it happens.
//
// Its own block, muted, and deliberately not part of the answer: it is
// the working, not the conclusion, and a reader scrolling back wants the
// conclusion. It is never replayed either — the daemon broadcasts these
// and logs none of them — so what is on screen is what this page watched
// arrive, which is the only claim it can honestly make.
let thinkingEl = null;
let thinkingBuffer = '';

export function appendThinking(text) {
  if (!thinkingEl) {
    thinkingEl = document.createElement('div');
    thinkingEl.className = 'msg-thinking';
    thinkingBuffer = '';
    follower.keeping(() => transcriptEl.appendChild(thinkingEl));
  }
  follower.keeping(() => {
    thinkingBuffer += text;
    thinkingEl.textContent = thinkingBuffer;
  });
}

export function endThinking() {
  thinkingEl = null;
  thinkingBuffer = '';
}

export function clearTranscript() {
  thinkingEl = null;
  thinkingBuffer = '';
  transcriptEl.innerHTML = '';
  // A different conversation, drawn from the bottom up. Carrying the
  // previous one's scrolled-up state over would open a session showing
  // its middle.
  scrollToBottom();
  session.currentModelEl = null;
  session.currentModelBuffer = '';
  // The rows these pointed at are gone with the innerHTML above; holding
  // them would leave finishToolCall or resolvePendingUser writing into
  // detached nodes.
  session.toolRows.clear();
  sentPlaceholders.clear();
}
