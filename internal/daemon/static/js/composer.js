import { inputEl, sendBtn, commDotEl } from './dom.js';
import { session, turnInFlight, historyLimit } from './state.js';
import * as apiClient from './api.js';
import { appendTool, appendError, appendPendingUser, resolvePendingUser } from './transcript.js';
import { renderStatusBar } from './render.js';
import { isPlainPrompt, tryLocalCommand } from './commands.js';

const defaultInputPlaceholder = inputEl.placeholder;

// Locks the prompt box while a permission request is pending, so a typed
// reply can never silently land in promptQueue instead of answering the
// modal above — `waiting` stays true for the whole time the daemon is
// blocked on that decision, which is otherwise indistinguishable from
// "the model is still working" from this client's point of view.
export function setInputLocked(locked, hint) {
  inputEl.disabled = locked;
  sendBtn.disabled = locked;
  inputEl.placeholder = locked ? hint : defaultInputPlaceholder;
}

// renderCommDot draws the three-state light left of the status text: gray
// when there's no live event stream to the daemon (so no path to the
// model), solid green when there is, blinking green while a turn is
// actually running.
//
// "Running" is turnInFlight(), not session.waiting: this light and the one
// on the session's row in the left panel report the same turn, and they
// have to agree. The panel reads the daemon's busy flag, so anything that
// left session.waiting false mid-turn — a reload, a session switch back
// into a working conversation, an error the loop recovered from — showed
// this dot solid while that one blinked.
export function renderCommDot() {
  const working = turnInFlight();
  commDotEl.classList.toggle('connected', session.connected);
  commDotEl.classList.toggle('active', session.connected && working);
  if (!session.connected) {
    commDotEl.title = 'not connected to the model (event stream is down)';
  } else if (working) {
    commDotEl.title = 'model is running your prompt';
  } else {
    commDotEl.title = 'connected to the model, idle';
  }
}

export function setWaiting(v) {
  session.waiting = v;
  renderCommDot();
  if (!v) dequeueNext();
  renderStatusBar();
}

export function setConnected(v) {
  if (session.connected === v) return;
  session.connected = v;
  renderCommDot();
}

export function rememberPrompt(text) {
  const h = session.history;
  if (h.length === 0 || h[h.length - 1] !== text) h.push(text);
  if (h.length > historyLimit) h.splice(0, h.length - historyLimit);
  session.historyIdx = h.length;
  session.historyDraft = '';
}

// recordHistoryEntry adds a prompt this client did not type: the ones the
// transcript replays when a session opens, and any sent from another client
// while it is open. That replay is what gives a reloaded — or reopened —
// session its recall list back, since nothing about history is persisted
// here.
//
// Unlike rememberPrompt it leaves navigation alone. An event arriving while
// someone is walking back through history must not yank the box out from
// under them, and appending at the end cannot move the entries they are
// already looking at.
export function recordHistoryEntry(text) {
  if (!text) return;
  const h = session.history;
  if (h.length > 0 && h[h.length - 1] === text) return;
  const composing = session.historyIdx >= h.length;
  h.push(text);
  if (h.length > historyLimit) h.splice(0, h.length - historyLimit);
  if (composing) session.historyIdx = h.length;
}

// navigatingHistory is true from the first Up until the walk ends (Down
// past the newest entry, or a new prompt sent). While it is true, Up and
// Down keep walking wherever the caret happens to be.
//
// Without it the second Up in a row did nothing: recall parks the caret at
// the end of the text it just inserted, and Up only recalled with the caret
// at offset 0 — so history was one entry deep unless you moved the caret
// back by hand between presses. The TUI never had this, because its
// condition is "the cursor is on the first visual row", which survives its
// own jump to the end.
export function navigatingHistory() {
  return session.historyIdx < session.history.length;
}

// endHistoryNavigation is called when the box is edited: at that point the
// recalled text has become a draft of its own, and the next Up should start
// a fresh walk from the newest entry rather than continuing one and
// throwing the edit away.
export function endHistoryNavigation() {
  session.historyIdx = session.history.length;
  session.historyDraft = '';
}

// Recall only fires when the caret is already at the very start (Up) or
// very end (Down) of the box, so arrows still move through a multi-line
// prompt normally and only reach for history at the boundary.
export function atInputStart() {
  return inputEl.selectionStart === 0 && inputEl.selectionEnd === 0;
}
export function atInputEnd() {
  const n = inputEl.value.length;
  return inputEl.selectionStart === n && inputEl.selectionEnd === n;
}

function setInputTo(text) {
  inputEl.value = text;
  autoResizeInput();
  const n = inputEl.value.length;
  inputEl.setSelectionRange(n, n);
}

export function historyPrev() {
  const h = session.history;
  if (h.length === 0 || session.historyIdx === 0) return false;
  if (session.historyIdx === h.length) session.historyDraft = inputEl.value;
  session.historyIdx--;
  setInputTo(h[session.historyIdx]);
  return true;
}

export function historyNext() {
  const h = session.history;
  if (session.historyIdx >= h.length) return false;
  session.historyIdx++;
  if (session.historyIdx === h.length) {
    setInputTo(session.historyDraft);
    session.historyDraft = '';
  } else {
    setInputTo(h[session.historyIdx]);
  }
  return true;
}

// cancelTurn stops the running turn. The "[cancelled]" transcript line
// comes from the turn.cancelled event the daemon broadcasts, so a cancel
// from any client is reported in every client the same way — but this
// client stops waiting on the strength of the reply, not the event.
export async function cancelTurn() {
  // turnInFlight, not session.waiting: the stop button is shown whenever
  // the daemon says this session is busy, so the key and the button that
  // share this function have to accept the same situations the button is
  // offered in — otherwise pressing stop on a turn this client did not
  // start is a no-op.
  if (!turnInFlight() || !session.sessionID) return;
  // Drop the queue here rather than waiting for the event, so a second Esc
  // press cannot race an already queued prompt out the door.
  session.promptQueue = [];
  try {
    await apiClient.cancelSessionTurn(session.sessionID);
    // Either answer clears this client's spinner, for different reasons.
    //
    // "cancelled: false" means the daemon had nothing running, so the
    // spinner is stale and no event is coming to clear it. Without this,
    // Esc is a dead key in exactly the situation someone reaches for it.
    //
    // "cancelled: true" means the daemon did stop the turn, and a
    // turn.cancelled event follows for every attached client. But if this
    // client's event stream has quietly died — see the heartbeat in
    // sse.go for how that happens — the event never lands, and the
    // spinner stays up over a turn that is already over. Pressing stop
    // and watching nothing happen is the worst possible answer to a
    // request the daemon honoured, so this client acts on the reply it
    // holds in its hand. The event still does the job for everyone else.
    session.runningTool = '';
    setWaiting(false);
  } catch (err) {
    appendError(err);
  }
}

// dequeueNext sends the next queued prompt once the current turn has
// actually finished (setWaiting(false) was just called) — the common case
// for someone who kept typing while the model was still streaming a reply.
// No-op if nothing is queued.
export function dequeueNext(isRetry = false) {
  if (session.promptQueue.length === 0) return;
  const next = session.promptQueue.shift();
  setWaiting(true);
  apiClient.sendChatMessage(session.sessionID, next).catch((err) => {
    if (apiClient.isBusy(err)) {
      // Still busy — put it back and wait for the next turn.done.
      session.promptQueue.unshift(next);
      // Unless nothing is running, in which case that turn.done has
      // already fired and waiting for another is waiting forever. The
      // daemon returns this same 409 for "a turn ended between the two
      // checks, send it again", and the two are indistinguishable from
      // here, so the safe reading is the one that retries — once.
      if (!isRetry) retryQueueSoon();
      return;
    }
    setWaiting(false);
    appendError(err);
  });
}

// retryQueueSoon re-attempts the queue shortly after a 409.
//
// A 409 means one of two things and the reply does not say which: a turn
// really is running, in which case its turn.done will drain the queue and
// this retry finds nothing to do — or the turn ended in the gap between
// the daemon's two checks, in which case the turn.done has already fired
// and nothing else will ever come. Without this, the second case parked
// the message: the activity dot blinked forever, the status bar read
// "working... (1 queued)", and everything typed afterwards queued behind
// it.
// One retry, never a loop: if the second attempt is refused too, a turn
// really is running and its turn.done is what drains the queue.
let retryTimer = null;
function retryQueueSoon() {
  if (retryTimer) return;
  retryTimer = setTimeout(() => {
    retryTimer = null;
    if (session.promptQueue.length > 0) dequeueNext(true);
  }, 300);
}

export function autoResizeInput() {
  inputEl.style.height = 'auto';
  inputEl.style.height = inputEl.scrollHeight + 'px';
}

export function insertAtCursor(el, text) {
  const start = el.selectionStart ?? el.value.length;
  const end = el.selectionEnd ?? el.value.length;
  el.value = el.value.slice(0, start) + text + el.value.slice(end);
  const pos = start + text.length;
  el.selectionStart = el.selectionEnd = pos;
  el.focus();
}

export async function sendMessage() {
  const text = inputEl.value.trim();
  if (!text) return;

  // A turn is already running: send the prompt anyway. The daemon hands
  // it to the running turn, which picks it up at its next tool call — so
  // "actually, skip the tests" reaches the model while it is still
  // working, instead of waiting for it to finish the wrong thing first.
  //
  // Commands still wait for the turn to end — they don't go through the
  // /messages endpoint, so there is nothing to hand over.
  if (turnInFlight()) {
    if (isPlainPrompt(text)) {
      rememberPrompt(text);
      inputEl.value = '';
      autoResizeInput();
      // The real transcript line comes from the message.user event the
      // daemon writes when the model is actually given the text. Until
      // then this stands in for it, since the wait can be minutes; it is
      // removed when that event lands.
      appendPendingUser(text, true);
      apiClient.sendChatMessage(session.sessionID, text).catch((err) => {
        if (apiClient.isBusy(err)) {
          // The turn ended in the gap. Queue it for dequeueNext, which
          // fires on the turn.done that is already on its way.
          session.promptQueue.push(text);
          renderStatusBar();
          return;
        }
        // Nothing took it, so the placeholder is a lie: remove it with the
        // error, rather than leaving "sent" above the reason it wasn't.
        resolvePendingUser(text);
        appendError(err);
      });
    } else {
      // A command cannot be handed to the running turn: the daemon passes
      // mid-turn text straight to the model, so "/compact" would arrive as
      // four words of chat rather than running. Queueing it is not safe
      // either — if a second turn has started by the time the queue
      // drains, it takes the same path and reaches the model as text.
      //
      // So it is refused, and said out loud. Before this the Enter did
      // nothing whatsoever: no request, nothing queued, no message, the
      // text still sitting in the box. The TUI has always explained this;
      // this is the same sentence.
      appendTool(`${text} can't run while a turn is in progress — wait for it to finish, or press Esc to cancel it.`);
    }
    return;
  }

  rememberPrompt(text);
  inputEl.value = '';
  autoResizeInput();

  if (await tryLocalCommand(text)) return;

  // A dimmed stand-in for the user line, drawn now and replaced by the real
  // one when the daemon's message.user event lands. The authoritative line
  // still comes from that event, so a replayed session shows exactly what a
  // live one did — but the gap before it arrives is the whole time the
  // daemon spends starting a turn (hooks, delegation, the first request),
  // and an Enter that leaves the screen unchanged for seconds reads as an
  // Enter that did not register.
  appendPendingUser(text);
  setWaiting(true);
  try {
    await apiClient.sendChatMessage(session.sessionID, text);
  } catch (err) {
    if (apiClient.isBusy(err)) {
      // The daemon already has a turn running (a race window, or a turn
      // another client started). Queue material, not an error: the running
      // turn's turn.done will drain it. waiting stays true so further
      // prompts queue too.
      session.promptQueue.unshift(text);
      renderStatusBar();
      retryQueueSoon();
      return;
    }
    resolvePendingUser(text);
    setWaiting(false);
    appendError(err);
  }
}
