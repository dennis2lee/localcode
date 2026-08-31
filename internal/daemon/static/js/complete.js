import { app, session } from './state.js';
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

// Everything "/" can complete to: the skills, the custom commands, the
// commands the daemon answers, and the few this page answers itself.
//
// All of them, because they are all invoked the same way and somebody
// typing "/sm" is not thinking about which list "/smart-agent" is in. An
// earlier version left the built-ins out on the grounds that walking
// past "/compact" costs presses, which is true and is the smaller cost:
// a command you cannot complete is one you have to remember exactly, and
// "/permission-skip-all" is not a name anybody types twice from memory.
//
// Installed things first, since those are the ones somebody chose to
// have; the built-ins are the ones /help lists.
const localOnly = ['/help', '/version', '/agent', '/commands'];

export function completionCandidates() {
  const all = [
    ...(app.skills || []).map(s => `/${s.name}`),
    ...(app.customCommands || []).map(c => `/${c.name}`),
    ...(app.slashCommands || []).map(c => `/${c.name}`),
    ...localOnly,
  ];
  // A skill and a custom command can share a name, and offering it twice
  // in a walk looks like the key stopped working.
  const seen = new Set();
  return all.filter((n) => {
    const k = n.toLowerCase();
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
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

// Everything "#" can complete to: the other conversations on this daemon,
// by title where they have one and by id where they do not.
//
// The archived ones are in the list, and are close to the reason the list
// exists: a conversation you put away last month is exactly the one whose
// name you cannot remember. Referring to one is reading, and archiving
// only ever refuses starting work.
//
// This conversation is not in the list. A reference to it resolves to
// "there is nothing to read", so offering it is offering a mistake.
export function sessionCandidates() {
  const all = [...(app.sessions || []), ...(app.archivedSessions || [])];
  const seen = new Set();
  const out = [];
  for (const s of all) {
    if (!s || s.id === session.sessionID) continue;
    const name = (s.title || '').trim() || s.id;
    // Quoted when it has to be, because a title routinely has spaces in
    // it and the daemon's grammar reads an unquoted name up to the first
    // one. Completing to something that does not parse is worse than not
    // completing.
    const tok = /\s/.test(name) ? `#"${name}"` : `#${name}`;
    const k = tok.toLowerCase();
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(tok);
  }
  return out;
}

// bareName strips a token down to the part a person is typing, so "#S2",
// `#"S2"` and "s2" all compare the same.
function bareName(tok) {
  return tok.replace(/^#/, '').replace(/^"/, '').replace(/"$/, '').toLowerCase();
}

export function sessionCompletionsFor(candidates, prefix) {
  const want = bareName(prefix);
  if (want === '') return candidates.slice();
  return candidates.filter(c => {
    const name = bareName(c);
    return name.startsWith(want) && name !== want;
  });
}

// completionTarget finds what the caret is sitting in, and is the whole
// difference between the two completions.
//
// A command is the first word of the box and nothing else, so it is
// matched against the whole text. A reference is not: "check #S2 against
// the file here" is the shape the feature exists for, so its token has to
// be found where the caret is and spliced back in place rather than
// replacing the box.
//
// Returns {kind, start, end, prefix}, where start and end bound the text
// a completion replaces.
export function completionTarget(text, caret) {
  text = text || '';
  if (caret === undefined || caret === null) caret = text.length;
  caret = Math.max(0, Math.min(caret, text.length));

  const cmd = completionPrefix(text);
  if (cmd !== null && caret === text.length) {
    return { kind: 'command', start: 0, end: text.length, prefix: cmd };
  }

  // The nearest "#" at or before the caret that opens a token: at the
  // start of the box, or with whitespace in front of it. Anything else is
  // a fragment identifier or somebody's C include.
  //
  // The scan does not stop at whitespace, because a quoted name is
  // allowed to contain some — `#"the parser` is a name half typed, and a
  // scan that gave up at the space would have made every multi-word title
  // uncompletable, which is most of them. What decides it is the token,
  // checked once the "#" is found.
  for (let i = caret - 1; i >= 0; i--) {
    if (text[i] !== '#') continue;
    if (i > 0 && !/\s/.test(text[i - 1])) return null;
    const token = text.slice(i, caret);
    const name = token.slice(1);
    if (name.startsWith('"')) {
      // A closing quote ends the name, so anything after it means the
      // caret has left the reference and is somewhere else in the
      // sentence. The quote itself is fine to sit just after: that is
      // where a completed name leaves the caret, and the walk has to be
      // able to carry on from there or pressing the key twice offers the
      // first candidate twice.
      const close = name.slice(1).indexOf('"');
      if (close >= 0 && close !== name.length - 2) return null;
    } else if (/\s/.test(token)) {
      // Whitespace in an unquoted token means the token ended before the
      // caret and the caret is somewhere else entirely.
      return null;
    }
    // "#42" is an issue number, which is the daemon's rule as well. A
    // completion that offered something the grammar then ignored would be
    // the two halves disagreeing about what a reference is.
    if (/^[0-9]+$/.test(name)) return null;
    return { kind: 'session', start: i, end: caret, prefix: token };
  }
  return null;
}

// nextCompletion advances the walk and returns what the box should say,
// or null when there is nothing to offer, which leaves the key to do
// what it did before.
export function nextCompletion(text, caret = undefined, candidates = null) {
  const target = completionTarget(text, caret);
  if (!target) {
    walk = { prefix: '', last: '', idx: -1 };
    return null;
  }
  if (walk.last !== text || !walk.prefix) {
    // Before the first candidate, not on it: the walk advances and then
    // reads, so a fresh one starts a step back or its first press skips
    // to the second name.
    walk = { prefix: target.prefix, last: '', idx: -1 };
  }
  const matches = target.kind === 'session'
    ? sessionCompletionsFor(candidates || sessionCandidates(), walk.prefix)
    : completionsFor(candidates || completionCandidates(), walk.prefix);
  if (matches.length === 0) return null;
  const ring = matches.concat([walk.prefix]);
  walk.idx = (walk.idx + 1) % ring.length;
  const chosen = ring[walk.idx];
  // Spliced, not substituted. For a command the span is the whole box and
  // the two are the same thing; for a reference they are not, and
  // replacing the box would delete the sentence the reference is in.
  const next = text.slice(0, target.start) + chosen + text.slice(target.end);
  walk.last = next;
  return { text: next, caret: target.start + chosen.length };
}

// resetCompletion ends a walk from the outside: sending a prompt, or
// switching sessions, both of which make the box's contents no longer
// the thing being walked.
export function resetCompletion() {
  walk = { prefix: '', last: '', idx: -1 };
}

// The key is completion rather than cursor movement only when there is
// nothing selected and nothing to the right of the caret worth moving
// into.
//
// "Nothing to the right" used to mean the end of the box, which was right
// while "/" was the only thing completable — a command is the whole
// prompt. A reference sits mid-sentence, so the rule is now the end of
// the word: the arrow still walks through text, and only stops doing so
// where the next character is a space or the box ends. Inside a word it
// is a cursor key, as it always was.
function caretCompletable() {
  const { selectionStart: a, selectionEnd: b, value } = inputEl;
  if (a !== b) return false;
  return a === value.length || /\s/.test(value[a]);
}

// tryComplete is the keydown hook. Returns true when it consumed the
// key, which is the caller's signal to preventDefault.
export function tryComplete() {
  if (!caretCompletable()) return false;
  const next = nextCompletion(inputEl.value, inputEl.selectionStart);
  if (next === null) return false;
  inputEl.value = next.text;
  inputEl.setSelectionRange(next.caret, next.caret);
  return true;
}

// completionHint is what the placeholder-style line under the box says
// while a walk is available, so an ambiguous prefix looks ambiguous
// before anything is pressed.
export function completionHint(text, caret = undefined, candidates = null) {
  const target = completionTarget(text || '', caret);
  if (!target) return '';
  // While walking, count against the walk's own prefix: the box holds a
  // completion, not what was typed.
  let prefix = target.prefix;
  if (walk.last === text && walk.prefix) prefix = walk.prefix;
  const matches = target.kind === 'session'
    ? sessionCompletionsFor(candidates || sessionCandidates(), prefix)
    : completionsFor(candidates || completionCandidates(), prefix);
  if (matches.length === 0) return '';
  if (matches.length === 1) return `→ ${matches[0]}`;
  return `→ ${matches[0]}  (${matches.length} matches, → cycles)`;
}
