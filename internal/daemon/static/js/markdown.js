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

  // 3. Inline code spans.
  text = text.replace(/`([^`\n]+)`/g, (_, code) => `<code>${code}</code>`);

  // 4. Block-level constructs, line by line: headers, quotes, rules,
  // and list runs (consecutive - / * / N. lines become one <ul>/<ol>).
  const lines = text.split('\n');
  const out = [];
  let listTag = null; // 'ul' | 'ol' | null — the list currently open
  const closeList = () => { if (listTag) { out.push(`</${listTag}>`); listTag = null; } };
  for (const line of lines) {
    const h = line.match(/^(#{1,6})\s+(.*)$/);
    const bullet = line.match(/^[-*]\s+(.*)$/);
    const numbered = line.match(/^\d+\.\s+(.*)$/);
    const quote = line.match(/^&gt;\s?(.*)$/);
    const isPlaceholder = /^\u0000\d+\u0000$/.test(line);
    if (isPlaceholder) {
      // A spliced-in code block: pass the line through untouched rather
      // than wrapping it in <p>, which would nest <pre> inside <p>.
      closeList();
      out.push(line);
    } else if (h) {
      closeList();
      out.push(`<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`);
    } else if (/^(-{3,}|\*{3,})$/.test(line.trim())) {
      closeList();
      out.push('<hr>');
    } else if (bullet) {
      if (listTag !== 'ul') { closeList(); out.push('<ul>'); listTag = 'ul'; }
      out.push(`<li>${inline(bullet[1])}</li>`);
    } else if (numbered) {
      if (listTag !== 'ol') { closeList(); out.push('<ol>'); listTag = 'ol'; }
      out.push(`<li>${inline(numbered[1])}</li>`);
    } else if (quote) {
      closeList();
      out.push(`<blockquote>${inline(quote[1])}</blockquote>`);
    } else if (line.trim() === '') {
      closeList();
      out.push('');
    } else {
      closeList();
      out.push(`<p>${inline(line)}</p>`);
    }
  }
  closeList();
  text = out.join('\n');

  // 5. Splice the fenced code blocks back in.
  text = text.replace(/\u0000(\d+)\u0000/g, (_, i) => blocks[+i]);
  return text;
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
