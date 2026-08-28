import { app } from './state.js';
import { inputEl } from './dom.js';

// Completing "/<name>" with the right arrow, matching the TUI key for
// key. See internal/tui/complete.go for the reasoning; the short version
// is that the shell answer of "complete to the longest common prefix" is
// useless here, because skills are named for what they do and the common
// prefix of "pdf-tools" and "pptx" is one letter. So the same key walks
// the candidates instead, and the text you typed is the last stop on the
// walk so a cycle is never a trap.

// A walk in progress: the prefix it started from, the text it last put
// in the box, and how far round it has gone. Keyed on the text, so any
// edit ends the walk without every editing path having to remember to.
let walk = { prefix: '', last: '', idx: -1 };

// Everything "/" can complete to: the skills and the custom commands.
// Both, because both are invoked the same way. The built-in commands are
// deliberately absent: there are twenty of them, they are in /help, and
// passing /compact on the way to a skill is worse than typing it out.
export function completionCandidates() {
  return [
    ...(app.skills || []).map(s => `/${s.name}`),
    ...(app.customCommands || []).map(c => `/${c.name}`),
  ];
}

export function completionsFor(candidates, prefix) {
  const lower = prefix.toLowerCase();
  return candidates.filter(c => c.toLowerCase().startsWith(lower) && c.toLowerCase() !== lower);
}

// A completable prompt is one word beginning with "/" and nothing else.
// "/pd" completes; "/pdf-tools split this" does not, because by then the
// completion is over and what follows is the request.
export function completionPrefix(text) {
  if (!text.startsWith('/') || text.length < 2) return null;
  if (/[\s]/.test(text)) return null;
  return text;
}

// nextCompletion advances the walk and returns what the box should say,
// or null when there is nothing to offer, which leaves the key to do
// what it did before.
export function nextCompletion(text, candidates = completionCandidates()) {
  const prefix = completionPrefix(text);
  if (!prefix) {
    walk = { prefix: '', last: '', idx: -1 };
    return null;
  }
  if (walk.last !== text || !walk.prefix) {
    // Before the first candidate, not on it: the walk advances and then
    // reads, so a fresh one starts a step back or its first press skips
    // to the second name.
    walk = { prefix, last: '', idx: -1 };
  }
  const matches = completionsFor(candidates, walk.prefix);
  if (matches.length === 0) return null;
  const ring = matches.concat([walk.prefix]);
  walk.idx = (walk.idx + 1) % ring.length;
  walk.last = ring[walk.idx];
  return walk.last;
}

// resetCompletion ends a walk from the outside: sending a prompt, or
// switching sessions, both of which make the box's contents no longer
// the thing being walked.
export function resetCompletion() {
  walk = { prefix: '', last: '', idx: -1 };
}

// atInputEnd is the one place the key is completion rather than cursor
// movement: after the last character, with nothing selected.
function caretAtEnd() {
  const n = inputEl.value.length;
  return inputEl.selectionStart === n && inputEl.selectionEnd === n;
}

// tryComplete is the keydown hook. Returns true when it consumed the
// key, which is the caller's signal to preventDefault.
export function tryComplete() {
  if (!caretAtEnd()) return false;
  const next = nextCompletion(inputEl.value);
  if (next === null) return false;
  inputEl.value = next;
  const n = inputEl.value.length;
  inputEl.setSelectionRange(n, n);
  return true;
}

// completionHint is what the placeholder-style line under the box says
// while a walk is available, so an ambiguous prefix looks ambiguous
// before anything is pressed.
export function completionHint(text, candidates = completionCandidates()) {
  let prefix = completionPrefix((text || '').trim());
  if (!prefix) return '';
  // While walking, count against the walk's own prefix: the box holds a
  // completion, not what was typed.
  if (walk.last === prefix && walk.prefix) prefix = walk.prefix;
  const matches = completionsFor(candidates, prefix);
  if (matches.length === 0) return '';
  if (matches.length === 1) return `→ ${matches[0]}`;
  return `→ ${matches[0]}  (${matches.length} matches, → cycles)`;
}
