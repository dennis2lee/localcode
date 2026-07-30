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
}
