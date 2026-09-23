import { transcriptEl, jumpBottomBtn } from './dom.js';
import { renderMarkdown } from './markdown.js';
import { createFollower } from './scroll.js';
import { app, session } from './state.js';
import { editDiffForTool, diffMaxRenderLines } from './diff.js';
import { closeFind, findRefresh } from './find.js';

// The transcript follows the newest output only while the reader is at
// the bottom of it. See scroll.js: this is the module that owns
// transcriptEl, so it owns the following too, and every change to the
// content below goes through change() rather than writing
// scrollTop itself.
const follower = createFollower(transcriptEl, (following) => {
  jumpBottomBtn.hidden = following;
});
jumpBottomBtn.addEventListener('click', () => follower.force());

// scrollToBottom is the deliberate one: the reader asked to be at the
// bottom (sent a prompt, opened a session), so following resumes whether
// or not they had scrolled away.
export function scrollToBottom() { follower.force(); }

// Every change to the transcript goes through one of these two, and the
// difference between them is whether an open find bar is told about it.
//
// change tells it. changeQuietly does not, and there are exactly two
// callers: the model's reply and its thinking, both of which rewrite
// their element once per fragment. Searching the whole conversation per
// token is the one cost worth avoiding, and turn.done tells the bar when
// those two have stopped.
//
// The default is to tell. It was the other way round first — one caller
// announced, the rest silent — and what that produced was three handlers
// naming the refresh by hand with one of them in the wrong place, and
// every writer that was not thought of drawing text nobody could find.
// A kind of message added later is searchable by being written the
// ordinary way.
function change(mutate) {
  const out = follower.keeping(mutate);
  findRefresh();
  return out;
}

function changeQuietly(mutate) {
  return follower.keeping(mutate);
}

// The only module allowed to touch transcriptEl. Everything goes through
// createElement/textContent — no call site anywhere else builds an HTML
// string for the transcript, which is what closes the escape-a-string class
// of bug (B5) for good: there is nowhere left to forget it.
function appendDiv(cls, text) {
  const div = document.createElement('div');
  div.className = cls;
  div.textContent = text;
  change(() => transcriptEl.appendChild(div));
  return div;
}

// A user turn is two elements: the separator that says a turn starts
// here and names who is speaking, and the prompt itself.
//
// Two rather than one because they are marking different things — the
// separator spans the column and belongs to the boundary, the prompt
// carries the rule down its own left edge and belongs to the message —
// and CSS cannot draw the first from inside the second's border box.
// They are created and removed together; see resolvePendingUser, which
// has to take the pair back out when the real message lands.
function appendUserBlock(text, pending, images = []) {
  const sep = document.createElement('div');
  sep.className = pending ? 'turn-sep pending' : 'turn-sep';
  // A text node, not a ::before content string: this is the only thing
  // left saying "you" once the inline prefix is gone, so it has to be
  // readable by a screen reader and findable by the browser's own find.
  sep.appendChild(document.createTextNode('You'));
  // The time goes on the boundary rather than on every line: a
  // transcript is read as a conversation, and a column of times down the
  // side of one is noise. What somebody asking "when did this happen"
  // wants is when the turn started.
  if (app.showTimestamps) {
    const at = document.createElement('span');
    at.className = 'turn-time';
    at.textContent = new Date().toLocaleTimeString();
    sep.appendChild(at);
  }

  const div = document.createElement('div');
  div.className = pending ? 'msg-user pending' : 'msg-user';
  if (text) {
    div.textContent = text;
  }
  if (images && images.length > 0) {
    const imgContainer = document.createElement('div');
    imgContainer.className = 'msg-images';
    for (const img of images) {
      const el = document.createElement('img');
      el.className = 'msg-image';
      el.src = `data:${img.media_type};base64,${img.data}`;
      el.alt = 'attached image';
      imgContainer.appendChild(el);
    }
    div.appendChild(imgContainer);
  }

  change(() => {
    transcriptEl.appendChild(sep);
    transcriptEl.appendChild(div);
  });
  return { sep, div };
}

export function appendUser(text, images) {
  const { div } = appendUserBlock(text, false, images);
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

export function appendPendingUser(text, midTurn = false, images = []) {
  // A mid-turn send is a note, not a turn: it is one line of explanation
  // about when the model will see this, and giving it a turn separator
  // would announce a boundary that the transcript does not have there.
  const parts = midTurn
    ? { div: appendDiv('msg-tool', `[sent — the model will pick this up at its next step] ${text}`) }
    : appendUserBlock(text, true, images);
  const div = parts.div;
  parts.text = text;
  const list = sentPlaceholders.get(text) || [];
  list.push(parts);
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
  const parts = list.shift();
  if (list.length === 0) sentPlaceholders.delete(text);
  // Both halves, or the separator outlives the prompt it announced and
  // the transcript grows a boundary with nothing after it.
  if (parts.sep) parts.sep.remove();
  parts.div.remove();
}

// abandonPendingUsers answers a stopped turn: every prompt still showing
// as sent was in the queue the daemon has just dropped, and it was never
// handed to the model.
//
// Rewritten rather than removed. What is on screen is text somebody
// typed, and taking it away silently is the other half of the same
// fault — they would be left knowing neither that it was discarded nor
// what it said. The line keeps the words and stops claiming they went
// anywhere.
export function abandonPendingUsers() {
  for (const list of sentPlaceholders.values()) {
    for (const parts of list) {
      // The separator announced a turn boundary that never happened.
      if (parts.sep) parts.sep.remove();
      parts.div.className = 'msg-tool';
      parts.div.textContent = `[not sent — the turn was stopped before the model saw this] ${parts.text}`;
    }
  }
  sentPlaceholders.clear();
}

// Moving between your own turns.
//
// The separator and the rule make a turn findable by eye; this is the
// same job done by keyboard, and on a session with fifty turns in it,
// it is the one that actually gets used. Alt+Up / Alt+Down, bound in
// main.js.
//
// The elements are read out of the DOM on each call rather than kept in
// a list, because the DOM is already the record: a session switch
// replaces the transcript wholesale, a pending prompt is removed when
// the real one lands, and a list maintained beside all that is a second
// copy waiting to disagree with the first.
export function jumpToTurn(direction) {
  const offsetOf = (el) => el.offsetTop - transcriptEl.offsetTop;
  // Each turn is the prompt and the separator above it, when there is
  // one. The separator is what the view is scrolled to, so the boundary
  // lands on screen rather than just above it — and it is therefore also
  // what "which turn am I on" has to be measured against. Measuring the
  // position against the prompt while scrolling to the separator is an
  // off-by-one that only shows up in use: after jumping to a turn, the
  // prompt sits a few pixels below the fold, so the turn reads as the
  // previous one and pressing "next" comes straight back to where it
  // already was.
  const turns = Array.from(transcriptEl.querySelectorAll('.msg-user'), (msg) => {
    const prev = msg.previousElementSibling;
    const sep = prev && prev.className && prev.className.split(' ').includes('turn-sep') ? prev : msg;
    return { msg, top: offsetOf(sep) };
  });
  if (turns.length === 0) return false;

  // Which turn the view is on: the last one whose top has reached the top
  // of the scroll box. Measured against the box rather than the window,
  // since the transcript is the thing that scrolls. A few pixels of slack
  // so a turn scrolled exactly into place counts as the current one
  // rather than as the one behind it.
  const top = transcriptEl.scrollTop;
  let current = -1;
  for (let i = 0; i < turns.length; i++) {
    if (turns[i].top <= top + 4) current = i;
  }

  const next = direction < 0 ? current - 1 : current + 1;
  if (next < 0 || next >= turns.length) return false;

  transcriptEl.scrollTop = turns[next].top;

  // Say where the reader landed, on exactly one turn. Clearing every
  // other one first is not tidiness: the class outlives its animation, so
  // walking ten turns left ten of them marked as "here".
  //
  // Re-triggered by removing the class and forcing a reflow, or jumping
  // to the same turn twice would animate once and then look broken.
  const target = turns[next].msg;
  for (const t of turns) t.msg.classList.remove('landed');
  void target.offsetWidth;
  target.classList.add('landed');
  return true;
}
export function appendTool(text) { return appendDiv('msg-tool', text); }
export function appendError(err) { return appendDiv('msg-error', 'Error: ' + String(err)); }

// showEarlierBanner puts a line at the very top of the transcript saying
// that what is on screen does not start at the beginning, with the control
// that fetches the rest.
//
// It exists because the transcript opens at its end — a long conversation
// would otherwise spend its first second rendering thousands of messages
// nobody asked to see — and until now that cut was silent and permanent:
// the browser asked for a tail, drew it, and had no way to ever ask for
// anything before it. The conversation was whole on disk and unreachable
// from here.
//
// Inserted rather than appended, and it is the one thing in this file that
// goes to the top. Scroll position is left alone: the reader is at the
// bottom looking at the newest message, and yanking them upward to show
// them a notice would be worse than the notice is worth.
export function showEarlierBanner(onOpen) {
  if (transcriptEl.querySelector('.msg-earlier')) return;
  const div = document.createElement('div');
  div.className = 'msg-earlier';
  const label = document.createElement('span');
  label.textContent = 'Showing the end of this conversation.';
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'earlier-btn';
  button.textContent = 'Load the whole conversation';
  button.addEventListener('click', () => onOpen());
  div.appendChild(label);
  div.appendChild(button);
  transcriptEl.insertBefore(div, transcriptEl.firstChild);
}

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
  change(() => transcriptEl.appendChild(div));
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
  head.addEventListener('click', () => {
    detail.hidden = !detail.hidden;
    const diff = row.querySelector('.todiff');
    if (diff) diff.hidden = detail.hidden;
  });

  row.appendChild(head);
  row.appendChild(detail);
  change(() => transcriptEl.appendChild(row));
  // name and inputJSON stay on the entry so finishToolCall can build
  // the diff then: tool.end carries the same input, but keying the
  // diff on what the call started with keeps one source — the row —
  // rather than trusting two events to agree.
  session.toolRows.set(toolUseID, { row, stateEl, marker, detail, name, inputJSON });
  return row;
}

// appendDiff draws the before/after rows editDiffForTool computed for a
// finished edit or write_file: removed lines marked, added lines
// marked, collapsed like the detail block itself so a fifty-line change
// does not push the conversation off the screen. Every row is a
// textContent node — never HTML — because a diff is file content, the
// most attacker-influenced text this page draws.
function appendDiff(row, rows) {
  const wrap = document.createElement('div');
  wrap.className = 'todiff';
  wrap.hidden = true;
  const shown = rows.slice(0, diffMaxRenderLines);
  for (const r of shown) {
    const line = document.createElement('div');
    line.className = r.kind === 'del' ? 'diff-del' : r.kind === 'add' ? 'diff-add' : 'diff-ctx';
    line.textContent = (r.kind === 'del' ? '- ' : r.kind === 'add' ? '+ ' : '  ') + r.text;
    wrap.appendChild(line);
  }
  // Capped, not dropped: the count says how much is behind the cut, the
  // way the detail block's line count does for long results.
  if (rows.length > shown.length) {
    const more = document.createElement('div');
    more.className = 'diff-more';
    more.textContent = `… (${rows.length - shown.length} more diff lines, not shown)`;
    wrap.appendChild(more);
  }
  row.appendChild(wrap);
  return wrap;
}

export function finishToolCall(toolUseID, content, isError) {
  const entry = session.toolRows.get(toolUseID);
  if (!entry) return;
  session.toolRows.delete(toolUseID);
  const { row, stateEl, marker, detail, name, inputJSON } = entry;
  row.classList.remove('running');
  row.classList.toggle('failed', !!isError);
  marker.textContent = isError ? '✗' : '✓';
  const text = String(content ?? '');
  // A failed call changed nothing, so there is nothing to diff — and a
  // call whose input is gone or unparseable gets no diff rather than a
  // wrong one.
  const rows = !isError ? editDiffForTool(name, inputJSON, text) : null;
  change(() => {
    stateEl.textContent = isError ? 'failed' : resultSize(text);
    detail.textContent = `${detail.textContent}\n\n${text}`;
    if (rows && rows.length > 0) appendDiff(row, rows);
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
  changeQuietly(() => {
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
// It is also authoritative while a reply is open, and is written over
// whatever the deltas drew. Closing without redrawing was a defect: an
// SSE reconnect resumes from the last id this page saw, so a reconnect
// in the middle of a reply means the fragments sent while it was away
// are never replayed, and what is on screen is the reply with a hole in
// it. The daemon has the whole thing and sends it here, so there is
// never a reason to keep the fragments. When nothing was missed this
// redraws the same characters.
export function endModelText(text) {
  if (text && session.currentModelEl) {
    const el = session.currentModelEl;
    change(() => {
      session.currentModelBuffer = text;
      el.innerHTML = renderMarkdown(text);
    });
  } else if (text) {
    appendModelText(text);
  }
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

  change(() => transcriptEl.appendChild(wrap));
  return wrap;
}

// The model's reasoning, while it happens.
//
// Its own block, muted, and deliberately not part of the answer: it is
// the working, not the conclusion, and a reader scrolling back wants the
// conclusion. It is never replayed either — the daemon broadcasts these
// and logs none of them — so what is on screen is what this page watched
// arrive, which is the only claim it can honestly make.
//
// Two drawings. The plain one is the text in a muted block, and is what
// every model gets. A muse model's stream arrives marked "fold" (the
// daemon's fold_thinking switch, applied to that family only), and gets
// a block of its own instead: a header saying Thinking with its running
// time, the text under it held to a few lines while it streams, and the
// whole folded to the header once the answer starts. Muse reasons at
// length before every answer, often by restating the question, and the
// plain block of that sitting open above the reply read as its first
// half.
let thinkingEl = null;
let thinkingBuffer = '';
// The fold block streaming now, or null: { wrap, head, marker, label,
// time, body, since, buffer, stick, timer }.
let thinkingFold = null;

export function appendThinking(text, fold) {
  // The switch is read here rather than at the event handler, so the
  // deltas still arrive and are simply not painted: turning it back on
  // mid-turn then shows the rest of the reasoning instead of nothing
  // until the next turn.
  if (!app.showThinking) return;
  if (fold) {
    appendFoldedThinking(text);
    return;
  }
  // A plain delta while a fold block is still open is the next request
  // going to a model the fold does not apply to, with nothing having
  // closed the block: close it, and draw this one the plain way.
  foldThinking(0);
  if (!thinkingEl) {
    thinkingEl = document.createElement('div');
    thinkingEl.className = 'msg-thinking';
    thinkingBuffer = '';
    change(() => transcriptEl.appendChild(thinkingEl));
  }
  changeQuietly(() => {
    thinkingBuffer += text;
    thinkingEl.textContent = thinkingBuffer;
  });
}

// endThinking closes the block the daemon says has ended. elapsedMs is
// the daemon's time from the block's first delta to its end.
export function endThinking(elapsedMs) {
  foldThinking(elapsedMs);
  thinkingEl = null;
  thinkingBuffer = '';
}

// foldThinking folds the block streaming now, if there is one. Called
// with no time when the block ended without saying so — the answer
// started, a tool ran, the turn stopped — and then this page's own
// clock is the next best figure.
//
// Quietly, as far as an open find bar is concerned: folding changes what
// shows, not what the conversation says, and telling the bar made it land
// on its current match again, which opened what had just been folded.
export function foldThinking(elapsedMs) {
  const f = thinkingFold;
  if (!f) return;
  thinkingFold = null;
  clearInterval(f.timer);
  const ms = elapsedMs > 0 ? elapsedMs : Date.now() - f.since;
  changeQuietly(() => {
    f.wrap.classList.remove('live');
    f.label.textContent = 'Thought for';
    f.time.textContent = formatThinkingTime(ms);
    setThinkingOpen(f, false);
  });
}

function appendFoldedThinking(text) {
  // A plain block still open is the next request's reasoning being
  // marked for folding where the last one was not.
  thinkingEl = null;
  if (!thinkingFold) {
    // Not opened on whitespace alone: a block whose reasoning is a
    // couple of newlines would be a header over nothing.
    if (!text.trim()) return;
    thinkingFold = startFoldedThinking();
  }
  const f = thinkingFold;
  changeQuietly(() => {
    f.buffer += text;
    f.body.textContent = f.buffer;
    f.time.textContent = formatThinkingTime(Date.now() - f.since);
    // The body is held to a few lines while it streams, and follows its
    // own end the way the transcript follows the reply: unless the
    // reader has scrolled up inside it to read something.
    if (f.stick) f.body.scrollTop = f.body.scrollHeight;
  });
}

function startFoldedThinking() {
  const wrap = document.createElement('div');
  wrap.className = 'msg-thinking fold live';

  // A button, so the header is reachable and operable from the keyboard
  // and says to a screen reader whether the text under it is showing.
  const head = document.createElement('button');
  head.type = 'button';
  head.className = 'head';
  const marker = document.createElement('span');
  marker.className = 'marker';
  const label = document.createElement('span');
  label.className = 'label';
  label.textContent = 'Thinking';
  const time = document.createElement('span');
  time.className = 'time';
  time.textContent = formatThinkingTime(0);
  head.appendChild(marker);
  head.appendChild(label);
  head.appendChild(time);

  const body = document.createElement('div');
  body.className = 'body';

  const f = { wrap, head, marker, label, time, body, since: Date.now(), buffer: '', stick: true, timer: null };
  body.addEventListener('scroll', () => {
    f.stick = body.scrollHeight - body.scrollTop - body.clientHeight < 8;
  });
  // Quietly, like a tool row's toggle: a click is the reader choosing
  // what shows, and an open find bar told about it would land on its
  // current match again and open whatever that is inside.
  head.addEventListener('click', () => {
    changeQuietly(() => setThinkingOpen(f, body.hidden));
  });
  // A find landing on a match inside the folded text opens it through
  // here, so the header says open when the text is showing.
  body.reveal = () => setThinkingOpen(f, true);
  setThinkingOpen(f, true);
  // The clock runs between deltas too: a model can pause mid-thought for
  // longer than a second, and a time that stops then reads as a block
  // that has stopped.
  f.timer = setInterval(() => {
    if (thinkingFold !== f) {
      clearInterval(f.timer);
      return;
    }
    f.time.textContent = formatThinkingTime(Date.now() - f.since);
  }, 1000);

  wrap.appendChild(head);
  wrap.appendChild(body);
  change(() => transcriptEl.appendChild(wrap));
  return f;
}

// setThinkingOpen shows or hides a fold block's text. Read back from the
// body's own hidden flag on every click rather than kept beside it, so
// the click always does the opposite of what the reader sees.
function setThinkingOpen(f, open) {
  f.body.hidden = !open;
  f.marker.textContent = open ? '▾' : '▸';
  f.head.setAttribute('aria-expanded', open ? 'true' : 'false');
  f.head.title = open ? 'click to fold the reasoning' : 'click to show the reasoning';
}

// formatThinkingTime says a duration the way the TUI's busy line does:
// whole seconds, then minutes, then hours.
function formatThinkingTime(ms) {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const min = Math.floor(total / 60) % 60;
  const sec = total % 60;
  if (h > 0) return `${h}h${min}m${sec}s`;
  return min > 0 ? `${min}m${sec}s` : `${sec}s`;
}

export function clearTranscript() {
  thinkingEl = null;
  thinkingBuffer = '';
  if (thinkingFold) clearInterval(thinkingFold.timer);
  thinkingFold = null;
  // The find bar's matches were in the conversation being replaced, and
  // a bar left open over a different one counts hits nobody searched for.
  closeFind();
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
