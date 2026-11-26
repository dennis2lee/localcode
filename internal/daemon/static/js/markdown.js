import { escapeHtml } from './format.js';
import { highlightCode } from './highlight.js';

// renderMarkdown turns model output into safe HTML for display. It is
// deliberately small (no dependency, this is a fully offline app) and
// covers what models actually produce in practice: fenced/inline code,
// headers, bold/italic, links, block quotes, lists, and a rule. It is
// re-run on the full buffer for every streamed delta, so it also has to
// tolerate a mid-render document (an unclosed fence, a half-typed link)
// without throwing or leaking unescaped input — every code path below
// escapes raw text before it is placed in the output, never after.
export function renderMarkdown(src) {
  // 1. Pull out fenced code blocks first so nothing else touches their
  // contents, replacing each with a placeholder token to splice back in
  // at the end. An unterminated trailing fence (still streaming) is
  // rendered as-is rather than left as literal ``` markers.
  //
  // The placeholder uses U+0000 (a control character no model output
  // will ever contain) rather than bare digits — a plain " 3 " collided
  // with any model text that happened to contain a bare number after a
  // code block, silently splicing the wrong block (or the literal string
  // "undefined") into the rendered output.
  //
  // Control characters (but tab and the line ends) are taken out of what
  // the model wrote first. Every placeholder here is made of them, and one
  // the model wrote itself would otherwise be read as a placeholder:
  // spliced back as "undefined", or as another span's HTML in a place
  // that span was never meant to be. The rest show as nothing, so they
  // can only hide what an address or a link's text says.
  const blocks = [];
  const placeholder = (i) => `\u0000${i}\u0000`;
  let text = String(src).replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '').replace(/```([^\n`]*)\n([\s\S]*?)(```|$)/g, (_, lang, code) => {
    // The language names a class and nothing else.
    const name = lang.trim().replace(/[^\w+#.-]/g, '');
    const cls = name ? ` class="language-${name}"` : '';
    // Code is highlighted, not merely escaped: highlightCode escapes
    // every span itself, so raw code never reaches the output.
    const html = `<pre><code${cls}>${highlightLinked(code.replace(/\n$/, ''), name)}</code></pre>`;
    blocks.push(html);
    return placeholder(blocks.length - 1);
  });

  // 2. Escape everything else as plain text now, before any markup is
  // introduced, so nothing the model wrote can inject an element.
  text = escapeHtml(text);

  // 3. Inline code spans. Held aside behind their own placeholder rather
  // than emitted as <code> here, for the same reason as the fences: what
  // is inside one is not markdown. A pipe in `a|b` would otherwise be
  // read as a table cell boundary further down and split the <code> tag
  // across two cells.
  // Each span three ways: as drawn, with its addresses linked; as drawn
  // inside a link, without them; and its text, for inline() to read.
  const spans = [];
  codeSpans = spans;
  const spanToken = (i) => `\u0001${i}\u0001`;
  // Double-backtick spans first: they may hold single backticks
  // inside (that is their whole point), and the single-backtick pass
  // below would otherwise eat the inner pair and leave the outers.
  text = text.replace(/``((?:[^`]|`[^`])*?)``/g, (whole, code) => {
    spans.push({ html: `<code>${linkify(code)}</code>`, plain: `<code>${code}</code>`, text: code, raw: whole });
    return spanToken(spans.length - 1);
  });
  text = text.replace(/`([^`\n]+)`/g, (_, written) => {
    // A double-backtick span inside this one is part of its text, as it
    // was written: a code span holds no markup, and its placeholder would
    // otherwise be left in the output.
    const code = written.replace(/\u0001(\d+)\u0001/g, (_, i) => spans[+i].raw);
    spans.push({ html: `<code>${linkify(code)}</code>`, plain: `<code>${code}</code>`, text: code, raw: `\`${code}\`` });
    return spanToken(spans.length - 1);
  });

  // 4. Block-level constructs, line by line: headers, quotes, rules,
  // and list runs (consecutive - / * / N. lines become one <ul>/<ol>).
  const lines = text.split('\n');
  const out = [];
  let listTag = null; // 'ul' | 'ol' | null — the list currently open
  const closeList = () => { if (listTag) { out.push(`</${listTag}>`); listTag = null; } };
  // para collects the consecutive plain lines of one paragraph. Markdown
  // ends a paragraph at a blank line or a block construct, not at every
  // newline — a model that hard-wraps its prose at 80 columns writes one
  // paragraph across six lines, and emitting a <p> per line turned that
  // into six paragraphs with a margin between each.
  let para = [];
  const closePara = () => {
    if (para.length === 0) return;
    out.push(`<p>${inline(para.join(' '))}</p>`);
    para = [];
  };
  const endBlock = () => { closePara(); closeList(); };
  for (let ln = 0; ln < lines.length; ln++) {
    const line = lines[ln];
    // A table is the one construct here that cannot be recognised from
    // its own line: "| a | b |" is only a header if the line after it is
    // a delimiter row. So it is checked first, with a look-ahead, and it
    // consumes as many rows as follow.
    const consumed = tableAt(lines, ln, out, endBlock);
    if (consumed > 0) { ln += consumed - 1; continue; }
    const h = line.match(/^(#{1,6})\s+(.*)$/);
    const bullet = line.match(/^[-*]\s+(.*)$/);
    const numbered = line.match(/^\d+\.\s+(.*)$/);
    const quote = line.match(/^&gt;\s?(.*)$/);
    const isPlaceholder = /^\u0000\d+\u0000$/.test(line);
    if (isPlaceholder) {
      // A spliced-in code block: pass the line through untouched rather
      // than wrapping it in <p>, which would nest <pre> inside <p>.
      endBlock();
      out.push(line);
    } else if (h) {
      endBlock();
      out.push(`<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`);
    } else if (/^(-{3,}|\*{3,})$/.test(line.trim())) {
      endBlock();
      out.push('<hr>');
    } else if (bullet) {
      closePara();
      if (listTag !== 'ul') { closeList(); out.push('<ul>'); listTag = 'ul'; }
      out.push(`<li>${inline(bullet[1])}</li>`);
    } else if (numbered) {
      closePara();
      if (listTag !== 'ol') { closeList(); out.push('<ol>'); listTag = 'ol'; }
      out.push(`<li>${inline(numbered[1])}</li>`);
    } else if (quote) {
      endBlock();
      out.push(`<blockquote>${inline(quote[1])}</blockquote>`);
    } else if (line.trim() === '') {
      // A blank line ends whatever was open. Nothing is emitted for it:
      // the gap between blocks is the margin the stylesheet gives them.
      endBlock();
    } else {
      closeList();
      para.push(line);
    }
  }
  endBlock();
  text = out.join('\n');

  // 5. Splice the code back in, inline spans and fenced blocks alike.
  text = text.replace(/\u0001(p?)(\d+)\u0001/g, (_, plain, i) => (plain ? spans[+i].plain : spans[+i].html));
  text = text.replace(/\u0000(\d+)\u0000/g, (_, i) => blocks[+i]);
  return text;
}

// splitCells breaks one table row into its cells.
//
// Written out rather than done with a split(): a cell may contain an
// escaped pipe ("\\|"), and the regex that expresses that needs a
// lookbehind, which Safari only learned in 16.4 — this has to run in
// whatever WKWebView the machine came with.
function splitCells(row) {
  const cells = [];
  let cur = '';
  for (let i = 0; i < row.length; i++) {
    if (row[i] === '\\' && row[i + 1] === '|') { cur += '|'; i++; continue; }
    if (row[i] === '|') { cells.push(cur); cur = ''; continue; }
    cur += row[i];
  }
  cells.push(cur);
  // The leading and trailing pipes are conventional, not structural, so
  // the empty cells they produce are dropped — "| a | b |" is two cells,
  // not four.
  if (cells.length && cells[0].trim() === '') cells.shift();
  if (cells.length && cells[cells.length - 1].trim() === '') cells.pop();
  return cells.map((c) => c.trim());
}

// alignments reads a delimiter row, returning one alignment per column,
// or null if the row is not a delimiter row at all. That second job is
// what decides whether the line above it was a table header.
function alignments(row) {
  if (row.indexOf('|') === -1 && row.indexOf('-') === -1) return null;
  const cells = splitCells(row);
  if (cells.length === 0) return null;
  const out = [];
  for (const cell of cells) {
    if (!/^:?-+:?$/.test(cell)) return null;
    const left = cell.startsWith(':');
    const right = cell.endsWith(':') && cell.length > 1;
    out.push(left && right ? 'center' : right ? 'right' : left ? 'left' : '');
  }
  return out;
}

// tableAt renders a GFM table starting at lines[i], returning how many
// lines it consumed (0 if there is no table here).
//
// Models produce tables constantly — comparisons, option lists, API
// summaries — and without this they arrived as a wall of pipes and
// dashes, which is both unreadable and a visible sign the renderer does
// not understand its own input.
function tableAt(lines, i, out, endBlock) {
  const header = lines[i];
  if (i + 1 >= lines.length || header.indexOf('|') === -1) return 0;
  const align = alignments(lines[i + 1]);
  if (!align) return 0;

  const head = splitCells(header);
  if (head.length === 0) return 0;

  endBlock();
  const cell = (tag, text, col) => {
    const a = align[col] || '';
    return `<${tag}${a ? ` style="text-align:${a}"` : ''}>${inline(text)}</${tag}>`;
  };

  const rows = [];
  rows.push(`<thead><tr>${head.map((c, n) => cell('th', c, n)).join('')}</tr></thead>`);

  let n = i + 2;
  const body = [];
  for (; n < lines.length; n++) {
    const row = lines[n];
    // A blank line, or a line with no pipe in it, ends the table. Both
    // are how a model actually stops writing one.
    if (row.trim() === '' || row.indexOf('|') === -1) break;
    const cells = splitCells(row);
    // Ragged rows are padded or truncated to the header's width rather
    // than rejected: a model miscounting one row should cost that row's
    // shape, not the whole table's rendering.
    const padded = head.map((_, c) => cells[c] === undefined ? '' : cells[c]);
    body.push(`<tr>${padded.map((c, col) => cell('td', c, col)).join('')}</tr>`);
  }
  if (body.length) rows.push(`<tbody>${body.join('')}</tbody>`);

  // Wrapped so a wide table scrolls inside the transcript instead of
  // stretching it — the transcript is a fixed column between two panels.
  out.push(`<div class="table-wrap"><table>${rows.join('')}</table></div>`);
  return n - i;
}

// The LaTeX a model writes for a client that renders it, unwrapped for
// one that does not.
//
// Some models — Gemma is the one this was reported for — write arrows and
// symbols as `$\rightarrow$` and names as `$\text{Bla}$`, which is right
// in a chat client with MathJax in it and literal noise here: the dollars
// and the backslashes are shown exactly as typed. The models are asked not
// to (see internal/agent/quirks.go), and this is the other half, for the
// replies that already exist and the times the model does it anyway.
//
// Deliberately narrow. A `$` span is only touched when it contains a
// backslash command, so a shell variable, a price, and `$PATH` are all
// left alone; and only commands with an obvious plain-text equivalent are
// translated, so nothing is invented for the ones that do not have one.
const mathSymbols = {
  // Arrows.
  rightarrow: '→', to: '→', longrightarrow: '→', Rightarrow: '⇒', implies: '⇒',
  leftarrow: '←', gets: '←', Leftarrow: '⇐', leftrightarrow: '↔', Leftrightarrow: '⇔',
  uparrow: '↑', downarrow: '↓', mapsto: '↦',
  // Operators and relations. `sim` is the one this list was missing:
  // "$S1 \\sim S6$" is how a model writes "S1 ~ S6", and a range like
  // that is far more common in ordinary prose than any actual formula.
  sim: '∼', simeq: '≃', propto: '∝', cong: '≅',
  times: '×', cdot: '·', div: '÷', pm: '±', mp: '∓', ast: '∗', star: '⋆',
  le: '≤', leq: '≤', ge: '≥', geq: '≥', ne: '≠', neq: '≠',
  ll: '≪', gg: '≫', approx: '≈', equiv: '≡',
  in: '∈', notin: '∉', subset: '⊂', subseteq: '⊆', supset: '⊃', supseteq: '⊇',
  cup: '∪', cap: '∩', setminus: '∖', emptyset: '∅', varnothing: '∅',
  forall: '∀', exists: '∃', neg: '¬', land: '∧', lor: '∨', therefore: '∴', because: '∵',
  infty: '∞', partial: '∂', nabla: '∇', deg: '°', prime: '′',
  ldots: '…', dots: '…', cdots: '⋯', bullet: '•', circ: '∘',
  perp: '⊥', parallel: '∥', angle: '∠', triangle: '△', square: '□',
  // Greek, lower and upper. A model naming a variable α writes \\alpha.
  alpha: 'α', beta: 'β', gamma: 'γ', delta: 'δ', epsilon: 'ε', varepsilon: 'ε',
  zeta: 'ζ', eta: 'η', theta: 'θ', vartheta: 'ϑ', iota: 'ι', kappa: 'κ',
  lambda: 'λ', mu: 'μ', nu: 'ν', xi: 'ξ', pi: 'π', rho: 'ρ', sigma: 'σ',
  tau: 'τ', upsilon: 'υ', phi: 'φ', varphi: 'φ', chi: 'χ', psi: 'ψ', omega: 'ω',
  Gamma: 'Γ', Delta: 'Δ', Theta: 'Θ', Lambda: 'Λ', Xi: 'Ξ', Pi: 'Π',
  Sigma: 'Σ', Upsilon: 'Υ', Phi: 'Φ', Psi: 'Ψ', Omega: 'Ω',
  // Spacing commands, which carry no meaning outside a formula.
  quad: ' ', qquad: ' ', thinspace: ' ', space: ' ',
};

// \text{x}, \mathrm{x}, \mathbf{x}: the braces are the markup and the
// contents are the words. They nest — \mathbf{\text{ vs }} is one phrase
// wrapped twice — and a single pass can only take the innermost pair,
// which leaves a \mathbf behind and makes the whole formula look like real
// maths to the test at the end. So strip until nothing changes.
function stripFontCommands(s) {
  const font = /\\(?:text|textbf|textit|textrm|textsf|texttt|mathrm|mathbf|mathit|mathsf|mathtt|mathcal|boldsymbol|operatorname)\{([^{}]*)\}/g;
  for (let i = 0; i < 8; i++) {
    const next = s.replace(font, '$1');
    if (next === s) break;
    s = next;
  }
  return s;
}

export function unwrapMath(s) {
  return s.replace(/\$\$?([^$\n]+?)\$?\$/g, (whole, body) => {
    if (!body.includes('\\')) return whole; // no LaTeX in it; not ours to touch
    let out = stripFontCommands(body)
      .replace(/\\([a-zA-Z]+)/g, (cmd, name) => (Object.prototype.hasOwnProperty.call(mathSymbols, name) ? mathSymbols[name] : cmd))
      // Escaped punctuation, which is only escaped because it was inside
      // maths. Deliberately not & or #: this text has already been through
      // escapeHtml, and putting a bare & back would undo that.
      .replace(/\\([%_])/g, '$1')
      // Thin spaces: \, \; \! and a backslash-space are all spacing
      // inside a formula and nothing outside one.
      .replace(/\\[,;!:> ]/g, ' ')
      .replace(/ +/g, ' ');
    out = out.trim();
    // If it still carries backslashes, this is real maths rather than a
    // symbol in disguise. Leaving it as it was at least keeps the
    // delimiters, which say "this was meant to be a formula".
    return /\\/.test(out) ? whole : out;
  });
}

// Underscores inside a word are part of the word, not emphasis.
//
// This is CommonMark's rule and it is the one that matters most here,
// because the text being rendered is a coding assistant's: read_file,
// task_tool.go and auto_compact_percent are written in prose constantly,
// and `_([^_]+)_` matched straight through them. "read_file_contents"
// rendered as "readfilecontents" with the middle in italics — the
// underscore is what makes an identifier an identifier, and deleting it
// silently produces a name that does not exist.
//
// The pair does not have to be inside one word to do damage. The closing
// _ is found wherever the next one is, so "call read_file then
// write_file now" came back as one italic run across the middle of the
// sentence with two underscores gone.
//
// Asterisks are left as they were: *foo* intraword is also legal
// CommonMark, and nothing writes one by accident the way every codebase
// writes snake_case.
//
// Written as a captured leading character rather than a lookbehind,
// which Safari only learned in 16.4 — and an unsupported group in a
// regex literal is a parse error, so the whole module would fail to load
// rather than one span rendering oddly. The classes are Unicode
// (\p{L}\p{N}, needing the u flag) rather than \w, which is ASCII-only:
// with \w a Hangul syllable counts as a non-word character, so
// "식별자_이름_형식" would be italicised by the very rule meant to
// prevent it.
const strongUnderscore = /(^|[^\p{L}\p{N}_])__([^_]+)__(?![\p{L}\p{N}])/gu;
const emUnderscore = /(^|[^\p{L}\p{N}_])_([^_]+)_(?![\p{L}\p{N}])/gu;

// A web address the model wrote, linked wherever it is: in prose, in
// inline code and in a code block. Every one opens in the person's own
// browser: target="_blank" is a new tab in a browser, and the desktop
// window hands it to the system (internal/gui newwindow*.go, and
// js/links.js for the window that has to be told).
//
// Two forms of the one pattern. bareURL reads text already through
// escapeHtml, where a quote or an angle bracket is an entity: it takes
// "&amp;" (an address's own "&") and an apostrophe inside a word
// ("/wiki/Hawai'i"), and stops at any other entity, so two addresses in
// quotes beside each other are two. rawURL reads code before it is
// highlighted, where they are still the characters. Control characters
// end both: the placeholders below are made of them.
const bareURL = /https?:\/\/(?:[^\s<>"'`\u0000-\u001f&]|&amp;|&#39;(?=[\p{L}\p{N}]))+/gu;
const rawURL = /https?:\/\/(?:[^\s<>"'`\u0000-\u001f]|'(?=[\p{L}\p{N}]))+/gu;

// hasHost is whether an address names a host: what follows the last "@"
// of the part before the path is a name of letters, digits, dots and
// hyphens, or an IPv6 address in brackets, with a port or none.
// "https:///", "https://:8080", "https://a@/" and "http://[" do not.
function hasHost(url) {
  const m = /^https?:\/\/([^/?#]*)/i.exec(url);
  if (!m) return false;
  const host = m[1].slice(m[1].lastIndexOf('@') + 1);
  return /^(?:[\p{L}\p{N}](?:[\p{L}\p{N}.-]*[\p{L}\p{N}])?|\[[0-9A-Fa-f:.]+\])(?::\d*)?$/u.test(host);
}

const CLOSES = { ')': '(', ']': '[', '}': '{' };

// splitURL separates an address from what the match took after it:
//
// * a bracket it closes that the address did not open, wherever it is,
//   and everything after it: "(https://a.example),(https://b.example)" is
//   two addresses in brackets, not one with a comma in it;
// * then, from its end, punctuation that ends a sentence (in any script),
//   and a word in another script written onto it.
//
// The last is Korean above all: a particle is written onto the word before
// it, so "https://example.com에서" is the address and "에서", and linking
// all of it opened a host that does not exist. A run of non-ASCII letters
// is cut when it follows an ASCII letter or digit, or punctuation the loop
// would cut. One that follows a "/" is a path segment in that script
// ("/문서/가이드") and is kept; so, unavoidably, is a particle after one.
//
// Returns ['', m] for a match that is no address once that is gone.
export function splitURL(m) {
  let url = m;
  let rest = '';
  const depth = { '(': 0, '[': 0, '{': 0 };
  for (let i = 0; i < url.length; i++) {
    const c = url[i];
    if (c in depth) depth[c] += 1;
    else if (CLOSES[c]) {
      if (depth[CLOSES[c]] === 0) {
        rest = url.slice(i);
        url = url.slice(0, i);
        break;
      }
      depth[CLOSES[c]] -= 1;
    }
  }
  for (;;) {
    // An apostrophe at the end closes a quote, and goes whole: cutting
    // the ";" of "&#39;" would leave half an entity in the link.
    if (url.endsWith('&#39;')) {
      rest = '&#39;' + rest;
      url = url.slice(0, -5);
      continue;
    }
    const last = url.slice(-1);
    // A ";" that ends "&amp;" is the address's own "&", not punctuation.
    if (/[.,;:!?*_~'"。、，．！？：；」』】〉》”’…]/.test(last) && !(last === ';' && url.endsWith('&amp;'))) {
      rest = last + rest;
      url = url.slice(0, -1);
      continue;
    }
    const word = url.match(/[^\x00-\x7f]+$/);
    if (word && /[A-Za-z0-9.,;:!?*_~'")\]}]$/.test(url.slice(0, -word[0].length))) {
      rest = word[0] + rest;
      url = url.slice(0, -word[0].length);
      continue;
    }
    break;
  }
  if (!hasHost(url)) return ['', m];
  return [url, rest];
}

const anchor = (href, body) => `<a href="${href}" target="_blank" rel="noopener noreferrer">${body}</a>`;

// linkMatch is what one match of bareURL becomes: its address, made into
// a link by link(url), then the rest of the match, scanned again for the
// next address a bracket or a quote left in it.
function linkMatch(m, link) {
  const [url, rest] = splitURL(m);
  if (!url) return m;
  return link(url) + rest.replace(bareURL, (r) => linkMatch(r, link));
}

// linkify links every bare address in escaped text.
export function linkify(escaped) {
  return escaped.replace(bareURL, (m) => linkMatch(m, (url) => anchor(url, url)));
}

const unescapeHtml = (s) => s.replace(/&(amp|lt|gt|quot|#39);/g, (_, e) => ({ amp: '&', lt: '<', gt: '>', quot: '"', '#39': "'" }[e]));

// highlightLinked highlights code with its addresses linked whole. They
// are found in the code before highlighting and held aside behind
// placeholders the highlighter passes through untouched (a control
// character around a private-use one: no digit or letter in it for a
// number or keyword rule to colour). Linked after highlighting instead,
// an address was cut wherever the highlighter had put a tag in it: a port
// or a date in its path is a number to a shell highlighter, so
// "http://localhost:8080/api" linked "http://localhost".
function highlightLinked(code, lang) {
  const links = [];
  const hold = (m) => {
    const [url, rest] = splitURL(escapeHtml(m));
    // One private-use character names each, so a block holds as many as
    // there are of them; any after that is left as code.
    if (!url || links.length >= 0x1900) return m;
    links.push(anchor(url, url));
    return `\u0003${String.fromCharCode(0xe000 + links.length - 1)}\u0003${unescapeHtml(rest).replace(rawURL, hold)}`;
  };
  const held = code.replace(rawURL, hold);
  return highlightCode(held, lang).replace(/\u0003([-])\u0003/g, (_, c) => links[c.charCodeAt(0) - 0xe000]);
}

function emphasis(s) {
  return s
    .replace(/\*\*([^*]+)\*\*/g, (_, body) => `<strong>${body}</strong>`)
    .replace(strongUnderscore, (_, pre, body) => `${pre}<strong>${body}</strong>`)
    .replace(/\*([^*]+)\*/g, (_, body) => `<em>${body}</em>`)
    .replace(emUnderscore, (_, pre, body) => `${pre}<em>${body}</em>`);
}

// The inline code spans of the text renderMarkdown is rendering, for
// inline() to read what a span placeholder in a link's text says.
// renderMarkdown runs start to end without yielding, so this is only ever
// the one render's.
let codeSpans = [];

// inline applies span-level markdown (bold, italic, links) to text that
// has already been through escapeHtml — it only ever matches the plain
// characters left behind (*, _, [, ], (, )), never entities.
//
// Links first, each held aside behind a placeholder, then emphasis: an
// address is full of underscores and asterisks that are not emphasis
// ("/_next/static/", "?q=*"), and the emphasis rules would cut it up. The
// text of a [text](address) link still gets its emphasis, except an
// address written in it, which is held aside the same way. Math is
// unwrapped after the links are held aside, so it cannot edit one either.
//
// The address of [text](address) is held to what a bare one is: the
// same characters, so a placeholder for a code span or a code block
// (spliced back after this runs) can never land inside href="...", where
// its quotes would end the attribute and start others, and a host.
// Brackets in it balance ("/wiki/Function_(mathematics)"). Its text holds
// no code block; inline code in it is drawn without its own links, since
// a link inside a link is not HTML.
//
// A link whose text reads as a web address goes to that address: when
// the text has "://" in it, or starts a word with "www.", and is not the
// address the link opens, the text is shown as written with the address
// it pointed at beside it, each link opening itself.
// [https://good.example](https://evil.example) could otherwise show one
// address and open another, and the text is what a page the model read
// can write. The text is read as a reader sees it: inline code as its
// text, entities read, width variants folded ("／" is "/"), a slash that
// only looks like one taken as one, and what shows as nothing (a
// zero-width space, a soft hyphen, a direction mark) taken out. So none
// of those, nor emphasis or case, hides "://" from the check. A heuristic
// like any reading of what text looks like: a script whose letters look
// like a Latin address's ("һttps") is not caught.
const URL_CHAR = `(?:[^\\s()<>"'\`\\u0000-\\u001f&]|&amp;|&#39;(?=[\\p{L}\\p{N}]))`;
const bracketLink = new RegExp(`\\[([^\\]\\u0000]+)\\]\\((https?:\\/\\/(?:${URL_CHAR}|\\(${URL_CHAR}*\\))+)\\)`, 'gu');

const asRead = (v) => v.normalize('NFKC')
  .replace(/\p{Default_Ignorable_Code_Point}/gu, '')
  .replace(/[\u2044\u2215\u29f8]/g, '/')
  .trim();
const visibleText = (t) => asRead(unescapeHtml(t.replace(/\u0001(\d+)\u0001/g, (_, i) => (codeSpans[+i] ? codeSpans[+i].text : ''))));
const readsAsAddress = (v) => v.includes('://') || /(?:^|\s)www\./i.test(v);

export function inline(s) {
  const links = [];
  const keep = (html) => {
    links.push(html);
    return `\u0002${links.length - 1}\u0002`;
  };
  const bare = (m) => linkMatch(m, (url) => keep(anchor(url, url)));
  const out = s
    .replace(bracketLink, (whole, t, u) => {
      if (!hasHost(u)) return whole;
      // A code block is never a link's text, inside inline code included:
      // the block's own links would be links inside this one, and the
      // address check below would read its placeholder, not its text.
      if (t.replace(/\u0001(\d+)\u0001/g, (_, i) => (codeSpans[+i] ? codeSpans[+i].text : '')).includes('\u0000')) return whole;
      const shown = visibleText(t);
      if (readsAsAddress(shown)) {
        // The address itself, as written or in bold: a link to it.
        const target = asRead(unescapeHtml(u)).replace(/\/$/, '');
        const unbolded = visibleText(emphasis(t).replace(/<[^>]*>/g, ''));
        if (shown.replace(/\/$/, '') === target || unbolded.replace(/\/$/, '') === target) return keep(anchor(u, u));
        return `${t} (${keep(anchor(u, u))})`;
      }
      const text = t.replace(/\u0001(\d+)\u0001/g, '\u0001p$1\u0001');
      return keep(anchor(u, emphasis(text)));
    })
    .replace(bareURL, bare);
  const restore = (str) => str.replace(/\u0002(\d+)\u0002/g, (_, i) => restore(links[+i]));
  return restore(emphasis(unwrapMath(out)));
}
