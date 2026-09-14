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
    default:
      return '';
  }
}

const isWord = (c) => /[\w$]/.test(c) || c > '\u007f';

export function highlightCode(code, lang) {
  const group = langGroup(lang);
  const kw = WORDS[group] || null;
  let lineComment = '';
  let blockComment = false;
  let quotes = '"\'';
  if (group === 'go' || group === 'javascript') {
    lineComment = '//';
    blockComment = true;
    quotes = '"\'`';
  } else if (group === 'python' || group === 'shell') {
    lineComment = '#';
  } else if (group === 'json') {
    quotes = '"';
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
    if (blockComment && c === '/' && code[i + 1] === '*') {
      const end = code.indexOf('*/', i + 2);
      const j = end === -1 ? code.length : end + 2;
      span('tok-com', code.slice(i, j));
      i = j;
      continue;
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
    // 123 in abc123 is an identifier and not a number.
    if (c >= '0' && c <= '9' && (i === 0 || !isWord(code[i - 1]))) {
      let j = i;
      while (j < code.length && (isWord(code[j]) || code[j] === '.')) j++;
      span('tok-num', code.slice(i, j));
      i = j;
      continue;
    }
    // A word is a keyword only on a full match: format is not for.
    if (isWord(c) && (i === 0 || !isWord(code[i - 1]))) {
      let j = i;
      while (j < code.length && isWord(code[j])) j++;
      const w = code.slice(i, j);
      if (kw && kw.has(w)) span('tok-kw', w);
      else plain(w);
      i = j;
      continue;
    }
    plain(c);
    i++;
  }
  return out.join('');
}
