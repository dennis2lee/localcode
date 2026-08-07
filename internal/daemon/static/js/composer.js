import { inputEl, sendBtn, commDotEl } from './dom.js';
import { session } from './state.js';
import * as apiClient from './api.js';
import { appendTool, appendError } from './transcript.js';
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
export function dequeueNext() {
  if (session.promptQueue.length === 0) return;
  const next = session.promptQueue.shift();
  setWaiting(true);
  apiClient.sendChatMessage(session.sessionID, next).catch((err) => {
    if (apiClient.isBusy(err)) {
      // Still busy — put it back and wait for the next turn.done.
      session.promptQueue.unshift(next);
      return;
    }
    setWaiting(false);
    appendError(err);
  });
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

  // A turn is already streaming: queue a plain prompt so it sends
  // automatically the moment the current one finishes, instead of
  // silently doing nothing and making the user remember to retype it.
  // Commands still wait for the turn to finish first — they don't go
  // through the /messages endpoint, so queueing them would mean
  // replaying them as literal chat text later.
  if (session.waiting) {
    if (isPlainPrompt(text)) {
      session.promptQueue.push(text);
      appendTool(`[queued] ${text}`);
      renderStatusBar();
      rememberPrompt(text);
      inputEl.value = '';
      autoResizeInput();
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
      return;
    }
    setWaiting(false);
    appendError(err);
  }
}
