import { escapeHtml } from './format.js';

// Dependency-free syntax highlighting for fenced code blocks.
//
// The page is served from the binary and never asks the network for
// anything, so a highlight library would have to be vendored; and the
// terminal side of this change (internal/tui/markdown.go) tints with a
// small word-list scanner rather than a grammar. This is the same idea
// for the browser: one linear scan colouring strings, comments, numbers
// and the keywords of a few common languages. Anything unrecognised
// passes through escaped but untinted, so an unknown language degrades
// to plain code rather than to nothing.
//
// Every code path below escapes raw text before it is placed in the
// output, never after — this module runs inside renderMarkdown, which
// is the one place attacker-influenced text becomes HTML.
const KEYWORDS = {
  go: 'func package import var const type struct interface map chan if else for range return switch case default break continue go defer select fallthrough goto nil true false new make len cap append panic',
  javascript: 'const let var function return if else for while class extends new import export from default async await try catch throw switch case break continue typeof instanceof this null undefined true false',
  python: 'def class return if elif else for while in not and or is None True False import from as with try except raise lambda pass yield async await',
  shell: 'if then else elif fi for while do done case esac function return exit export local echo in',
  json: 'true false null',
  yaml: 'true false null yes no on off',
};

const WORDS = {};
for (const [lang, list] of Object.entries(KEYWORDS)) {
  WORDS[lang] = new Set(list.split(' '));
}

function langGroup(lang) {
  switch (String(lang || '').trim().toLowerCase()) {
    case 'go':
    case 'golang':
      return 'go';
    case 'js':
    case 'jsx':
    case 'ts':
    case 'tsx':
    case 'typescript':
    case 'javascript':
      return 'javascript';
    case 'py':
    case 'python':
      return 'python';
    case 'sh':
    case 'bash':
    case 'zsh':
    case 'shell':
      return 'shell';
    case 'json':
      return 'json';
    case 'yaml':
    case 'yml':
      return 'yaml';
    case 'md':
    case 'mkd':
    case 'markdown':
      return 'markdown';
    default:
      return '';
  }
}

const isWord = (c) => /[\w$]/.test(c) || c > '\u007f';

export function highlightCode(code, lang) {
  const group = langGroup(lang);
  const kw = WORDS[group] || null;
  let lineComment = '';
  let blockOpen = '';
  let blockClose = '';
  let quotes = '"\'';
  if (group === 'go' || group === 'javascript') {
    lineComment = '//';
    blockOpen = '/*';
    blockClose = '*/';
    quotes = '"\'`';
  } else if (group === 'python' || group === 'shell') {
    lineComment = '#';
  } else if (group === 'json') {
    quotes = '"';
  } else if (group === 'yaml') {
    lineComment = '#';
    quotes = '"\'';
  } else if (group === 'markdown') {
    // Markdown has no line comments and no keywords of its own: what
    // the scan can honestly tint is code spans (backticks), HTML
    // comments, and the # headings below. Plain quotes stay quotes —
    // tinting every "word" in prose green would be noise, not signal.
    blockOpen = '<!--';
    blockClose = '-->';
    quotes = '`';
  }

  // Tokens are collected raw and escaped span by span at the end, so a
  // span boundary can never land in the middle of an HTML entity.
  const out = [];
  let i = 0;
  const plain = (s) => { if (s) out.push(escapeHtml(s)); };
  const span = (cls, s) => { out.push(`<span class="${cls}">${escapeHtml(s)}</span>`); };

  while (i < code.length) {
    const c = code[i];
    // A line comment runs to the newline. In a // language the second
    // slash is required, or every URL would comment out the line.
    if (lineComment && c === lineComment[0] &&
        (lineComment === '#' || code[i + 1] === '/')) {
      let j = i;
      while (j < code.length && code[j] !== '\n') j++;
      span('tok-com', code.slice(i, j));
      i = j;
      continue;
    }
    // A block comment runs to its closer, or to the end when the reply
    // is still streaming and the closer has not arrived yet.
    if (blockOpen && code.startsWith(blockOpen, i)) {
      const end = code.indexOf(blockClose, i + blockOpen.length);
      const j = end === -1 ? code.length : end + blockClose.length;
      span('tok-com', code.slice(i, j));
      i = j;
      continue;
    }
    // A Markdown heading is the one line-level structure prose has: a
    // run of # at the start of the line. Tinted with the comment voice
    // the way muted asides are elsewhere, not because it is a comment
    // but because it is scaffolding around the words.
    if (group === 'markdown' && c === '#' && (i === 0 || code[i - 1] === '\n')) {
      const m = /^#{1,6}(?=\s|$)/.exec(code.slice(i));
      if (m) {
        let j = i;
        while (j < code.length && code[j] !== '\n') j++;
        span('tok-com', code.slice(i, j));
        i = j;
        continue;
      }
    }
    // A string runs to its matching quote, honouring backslash escapes
    // everywhere except shell single quotes, where a backslash is a
    // backslash. An unterminated string (still streaming) runs to the
    // end rather than leaking the rest of the block out of the span.
    if (quotes.includes(c)) {
      const raw = c === "'" && group === 'shell';
      let j = i + 1;
      while (j < code.length) {
        if (!raw && code[j] === '\\') { j += 2; continue; }
        if (code[j] === c) { j++; break; }
        if (code[j] === '\n' && c !== '`') break;
        j++;
      }
      span('tok-str', code.slice(i, j));
      i = j;
      continue;
    }
    // A number starts at a digit that does not continue a word, so the
    // 123 in abc123 is an identifier and not a number. Markdown opts
    // out: prose is full of digits that are not quantities (years,
    // list markers, "3pm"), and tinting every one of them gold is
    // noise rather than signal.
    if (group !== 'markdown' && c >= '0' && c <= '9' && (i === 0 || !isWord(code[i - 1]))) {
      let j = i;
      while (j < code.length && (isWord(code[j]) || code[j] === '.')) j++;
      span('tok-num', code.slice(i, j));
      i = j;
      continue;
    }
    // A word is a keyword only on a full match: format is not for.
    // In YAML a line-leading word followed by a colon is a key — the
    // structure of the document — and gets the same voice. Line-leading
    // only (past an optional list marker), so the http in
    // http://example.com is not a key; and dashes join the word, so
    // my-key is one key rather than a key with "-key" after it.
    if (isWord(c) && (i === 0 || !isWord(code[i - 1]))) {
      let j = i;
      while (j < code.length && isWord(code[j])) j++;
      if (group === 'yaml') {
        // Guarded on length first: isWord coerces undefined past the
        // end into the string "undefined", which the word class
        // matches — an unguarded read would swallow a trailing dash.
        while (code[j] === '-' && j + 1 < code.length && isWord(code[j + 1])) {
          j++;
          while (j < code.length && isWord(code[j])) j++;
        }
      }
      const w = code.slice(i, j);
      if (kw && kw.has(w)) span('tok-kw', w);
      else if (group === 'yaml' && code[j] === ':' &&
        /^[ \t]*(-[ \t]+)?[ \t]*$/.test(code.slice(code.lastIndexOf('\n', i - 1) + 1, i))) span('tok-kw', w);
      else plain(w);
      i = j;
      continue;
    }
    plain(c);
    i++;
  }
  return out.join('');
}
