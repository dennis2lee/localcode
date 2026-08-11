import { inputEl, sendBtn, commDotEl } from './dom.js';
import { session } from './state.js';
import * as apiClient from './api.js';
import { appendTool, appendError, appendPendingUser } from './transcript.js';
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
function renderCommDot() {
  commDotEl.classList.toggle('connected', session.connected);
  commDotEl.classList.toggle('active', session.connected && session.waiting);
  if (!session.connected) {
    commDotEl.title = 'not connected to the model (event stream is down)';
  } else if (session.waiting) {
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
  session.historyIdx = h.length;
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
  if (!session.waiting || !session.sessionID) return;
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
  if (session.waiting) {
    if (isPlainPrompt(text)) {
      rememberPrompt(text);
      inputEl.value = '';
      autoResizeInput();
      // The real transcript line comes from the message.user event the
      // daemon writes when the model is actually given the text. Until
      // then this stands in for it, since the wait can be minutes; it is
      // removed when that event lands.
      appendPendingUser(text);
      apiClient.sendChatMessage(session.sessionID, text).catch((err) => {
        if (apiClient.isBusy(err)) {
          // The turn ended in the gap. Queue it for dequeueNext, which
          // fires on the turn.done that is already on its way.
          session.promptQueue.push(text);
          renderStatusBar();
          return;
        }
        appendError(err);
      });
    }
    return;
  }

  rememberPrompt(text);
  inputEl.value = '';
  autoResizeInput();

  if (await tryLocalCommand(text)) return;

  // The user line renders from the message.user event (see applyEvent), not
  // optimistically here, so a resumed/replayed session shows the same
  // transcript a live one did.
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
      appendTool(`[queued] ${text}`);
      renderStatusBar();
      retryQueueSoon();
      return;
    }
    setWaiting(false);
    appendError(err);
  }
}
