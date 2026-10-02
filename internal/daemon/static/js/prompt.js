import {
  promptModal, promptTitleEl, promptMessageEl, promptInputEl,
  promptOkBtn, promptCancelBtn,
} from './dom.js';
import { Modal } from './modal.js';

// An in-page replacement for window.prompt and window.confirm.
//
// The Mac desktop window renders this page in a WKWebView whose UI
// delegate implements the file picker and none of the JavaScript
// dialog panels (alert, confirm, text input). A window.prompt or
// window.confirm call there never resolves, so every caller that waits
// on one silently stops: session rename, session and group delete,
// schedule rename, the update installer. The Windows window works
// because WebView2 provides those panels natively. One in-page dialog
// behaves the same in a browser and in both windows, so no caller
// reaches for the native panels again.
//
// Both functions return a promise: askText resolves to the typed string,
// or null when cancelled, with the same meaning as window.prompt's
// return; askConfirm resolves to true or false like window.confirm.
// While open, Escape cancels and Enter answers, the way the native
// panels do. Only one call is served at a time. Callers are sequential
// user gestures on one page, so a second call while one is open answers
// the new one with the cancelled value rather than queuing behind text
// somebody is still typing.
const promptDialog = new Modal(promptModal);

let pending = null;

function finish(value) {
  if (!pending) return;
  const resolve = pending.resolve;
  pending = null;
  promptDialog.close();
  promptInputEl.blur();
  resolve(value);
}

// wirePromptDialog attaches the button and key handlers. Called once
// from main.js init, after every module that asks questions is loaded.
// A document-level keydown, not per-field listeners: the confirm shape
// has no field to hang one on, and one place owns the behaviour.
export function wirePromptDialog() {
  promptOkBtn.addEventListener('click', () => {
    if (pending) finish(pending.input ? promptInputEl.value : true);
  });
  promptCancelBtn.addEventListener('click', () => finish(pending && pending.input ? null : false));
  document.addEventListener('keydown', (e) => {
    if (!promptDialog.isOpen || !pending) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      finish(pending.input ? null : false);
    } else if (e.key === 'Enter' && e.target !== promptCancelBtn) {
      // Enter answers from the input and from the OK button. Not from
      // Cancel: a focused Cancel button already answers on Enter through
      // the click above, and a second answer would be a no-op at best.
      e.preventDefault();
      finish(pending.input ? promptInputEl.value : true);
    }
  });
}

function open(title, message, { input, initial, okLabel, danger }) {
  // A second question while one is open cannot be shown. Answer it with
  // the cancelled value. The caller treats that as "do nothing", which
  // is the only safe answer for a question nobody saw.
  if (pending) return Promise.resolve(input ? null : false);
  promptTitleEl.textContent = title;
  promptMessageEl.textContent = message;
  promptInputEl.style.display = input ? '' : 'none';
  if (input) promptInputEl.value = initial || '';
  promptOkBtn.textContent = okLabel || 'OK';
  promptOkBtn.classList.toggle('danger-btn', !!danger);
  pending = { input: !!input, resolve: null };
  const answered = new Promise((resolve) => { pending.resolve = resolve; });
  promptDialog.open();
  // The field the answer comes from gets the focus. Selected whole, so
  // typing replaces the current name and Escape still cancels.
  if (input) {
    promptInputEl.focus();
    promptInputEl.select();
  } else {
    promptOkBtn.focus();
  }
  return answered;
}

// askText shows a text-input question. Returns the typed string, or
// null when the person cancels. An empty string is a real answer, the
// way window.prompt's empty string is: some callers clear on empty.
export function askText(title, message, initial, okLabel) {
  return open(title, message, { input: true, initial, okLabel });
}

// askConfirm shows a yes-or-no question. Returns true when answered
// yes, false on Cancel or Escape. danger marks the OK button red for
// answers that cannot be undone, matching the danger-btn treatment.
export function askConfirm(title, message, { okLabel, danger } = {}) {
  return open(title, message, { input: false, okLabel, danger });
}

// anyPromptOpen reports whether a question is on screen. main.js reads
// it alongside anyModalOpen so function keys stand down under it.
export function anyPromptOpen() {
  return promptDialog.isOpen;
}
