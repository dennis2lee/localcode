import { escapeHtml } from './format.js';

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
  const blocks = [];
  const placeholder = (i) => `\u0000${i}\u0000`;
  let text = src.replace(/```([^\n`]*)\n([\s\S]*?)(```|$)/g, (_, lang, code) => {
    const cls = lang.trim() ? ` class="language-${escapeHtml(lang.trim())}"` : '';
    const html = `<pre><code${cls}>${escapeHtml(code.replace(/\n$/, ''))}</code></pre>`;
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
  const spans = [];
  const spanToken = (i) => `${i}`;
  text = text.replace(/`([^`\n]+)`/g, (_, code) => {
    spans.push(`<code>${code}</code>`);
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
  text = text.replace(/\u0001(\d+)\u0001/g, (_, i) => spans[+i]);
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

// inline applies span-level markdown (bold, italic, links) to text that
// has already been through escapeHtml — it only ever matches the plain
// characters left behind (*, _, [, ], (, )), never entities.
export function inline(s) {
  return s
    .replace(/\*\*([^*]+)\*\*|__([^_]+)__/g, (_, a, b) => `<strong>${a || b}</strong>`)
    .replace(/\*([^*]+)\*|_([^_]+)_/g, (_, a, b) => `<em>${a || b}</em>`)
    .replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, (_, t, u) => `<a href="${u}" target="_blank" rel="noopener noreferrer">${t}</a>`);
}
