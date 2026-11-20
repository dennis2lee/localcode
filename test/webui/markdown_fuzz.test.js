'use strict';

// renderMarkdown's output goes into innerHTML, and its input is the
// model's text, which a page or file the model read can write. Whatever
// that text is, the HTML has only the elements and attributes the renderer
// means to make, and every link opens the web address it is for.
//
// Strings are built from pieces that have each broken the renderer once
// (placeholders, fences with languages, code spans, link syntax, emphasis,
// entities, math, Korean, private-use characters) with a fixed seed, so a
// failure names the input that broke it and reproduces.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const PIECES = [
  '`', '``', '```', '```js\n', '```x onmouseover=alert(1)//\n', '\n```\n', '\n', ' ', '  ',
  '[', ']', '(', ')', '](', '[x](', '*', '**', '_', '__', '$', '\\', '\\sim', '|', '| a | b |\n|---|---|\n',
  '"', "'", '<', '>', '&', '&amp;', '&quot;', '&#39;', '&lt;', '#', '# ', '- ', '1. ', '> ', '---',
  'http://', 'https://', 'https://a.example', 'https://b.example/p_(q)', 'http://localhost:8080/x',
  'javascript:alert(1)', 'onmouseover=alert(1)', 'onfocus=x autofocus', '//', 'x', 'y', '0', '42',
  '에서', '가이드', '。', '”', '…', '\u0000', '\u0001', '\u0002', '\u0003', '', '',
  '\u00010\u0001', '\u00020\u0002', '\u00000\u0000',
  'HTTPS://', 'Http://', 'https://a@', 'http://[', 'https://[::1]', '\u0004', '\u200b', '\u00ad', '\u202e',
  '／', '：', '⁄', '` ```\n', '[`', '`](', "'s", 'www.',
];

// mulberry32: a small seeded generator, the same sequence on every machine.
function rng(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const ELEMENTS = new Set(['p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'li', 'blockquote', 'hr',
  'strong', 'em', 'code', 'pre', 'span', 'a', 'div', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'br']);

// tags reads the start and end tags of html the way an HTML tokenizer
// does: an attribute value is double-quoted, single-quoted or unquoted, and
// a quote that ends one early starts the next attribute.
function tags(html) {
  const out = [];
  let i = 0;
  while ((i = html.indexOf('<', i)) >= 0) {
    const end = html.indexOf('>', i);
    if (end < 0) break;
    const close = html[i + 1] === '/';
    const m = /^<\/?([a-zA-Z][a-zA-Z0-9]*)/.exec(html.slice(i));
    if (!m) { i += 1; continue; }
    let j = i + m[0].length;
    const attrs = [];
    // Read attributes up to the '>' that really ends the tag.
    for (;;) {
      while (/[\s/]/.test(html[j] || '')) j += 1;
      if (html[j] === '>' || j >= html.length) break;
      const name = /^[^\s/>=]+/.exec(html.slice(j));
      if (!name) { j += 1; continue; }
      j += name[0].length;
      let value = null;
      while (/\s/.test(html[j] || '')) j += 1;
      if (html[j] === '=') {
        j += 1;
        while (/\s/.test(html[j] || '')) j += 1;
        const q = html[j];
        if (q === '"' || q === "'") {
          const k = html.indexOf(q, j + 1);
          value = html.slice(j + 1, k < 0 ? html.length : k);
          j = k < 0 ? html.length : k + 1;
        } else {
          const v = /^[^\s>]*/.exec(html.slice(j))[0];
          value = v;
          j += v.length;
        }
      }
      attrs.push([name[0].toLowerCase(), value]);
    }
    out.push({ name: m[1].toLowerCase(), close, attrs, at: i });
    i = j + 1;
  }
  return out;
}

const decode = (s) => s.replace(/&(amp|lt|gt|quot|#39);/g, (_, e) => ({ amp: '&', lt: '<', gt: '>', quot: '"', '#39': "'" }[e]));

function check(html) {
  let inLink = 0;
  for (const t of tags(html)) {
    if (!ELEMENTS.has(t.name)) return `element <${t.name}>`;
    if (t.close) {
      if (t.name === 'a') inLink -= 1;
      continue;
    }
    const names = t.attrs.map(([n]) => n);
    if (t.name === 'a') {
      if (inLink > 0) return 'a link inside a link';
      inLink += 1;
      if (names.join(' ') !== 'href target rel') return `link attributes "${names.join(' ')}"`;
      const href = decode(t.attrs[0][1] || '');
      const host = (/^https?:\/\/([^/?#]*)/.exec(href) || [, ''])[1].replace(/^.*@/, '');
      if (!/^(?:[\p{L}\p{N}](?:[\p{L}\p{N}.-]*[\p{L}\p{N}])?|\[[0-9A-Fa-f:.]+\])(?::\d*)?$/u.test(host)) return `href ${JSON.stringify(href)}`;
      if (t.attrs[1][1] !== '_blank' || t.attrs[2][1] !== 'noopener noreferrer') return 'target or rel';
      continue;
    }
    for (const [n, v] of t.attrs) {
      const ok = (n === 'class' && ['code', 'span', 'div'].includes(t.name) && /^[\w+#. -]*$/.test(v || ''))
        || (n === 'style' && ['th', 'td'].includes(t.name) && /^text-align:(left|right|center)$/.test(v || ''));
      if (!ok) return `<${t.name}> attribute ${n}=${JSON.stringify(v)}`;
    }
  }
  return '';
}

// A link whose text reads as a web address opens that address. Read as a
// reader sees it: tags gone, entities read, width variants folded, what
// shows as nothing taken out.
function linkTextMatchesHref(html) {
  for (const m of html.matchAll(/<a href="([^"]*)" target="_blank" rel="noopener noreferrer">((?:(?!<\/a>).)*)<\/a>/gs)) {
    const read = (v) => v.normalize('NFKC')
      .replace(/\p{Default_Ignorable_Code_Point}/gu, '').replace(/[\u2044\u2215\u29f8]/g, '/').trim();
    const text = read(decode(m[2].replace(/<[^>]*>/g, '')));
    if (text.includes('://') && text.replace(/\/$/, '') !== read(decode(m[1])).replace(/\/$/, '')) {
      return `link text ${JSON.stringify(text)} opens ${JSON.stringify(decode(m[1]))}`;
    }
  }
  return '';
}

test('no model text puts an element, an attribute or a link the renderer does not make', async () => {
  const app = await load({ init: false });
  const next = rng(20261120);
  for (let n = 0; n < 4000; n++) {
    const len = 1 + Math.floor(next() * 14);
    let src = '';
    for (let k = 0; k < len; k++) src += PIECES[Math.floor(next() * PIECES.length)];
    const html = app.renderMarkdown(src);
    const bad = check(html) || linkTextMatchesHref(html);
    assert.equal(bad, '', `input ${JSON.stringify(src)}\n=> ${html}\nbecause ${bad}`);
  }
});

// The checker itself: each of these is a way out it has to see.
test('the checker sees an attribute, an element and a link out of place', () => {
  assert.match(check('<a href="https://a.example" target="_blank" rel="noopener noreferrer" onclick="x">a</a>'), /link attributes/);
  assert.match(check('<a href="https://a.example/"x onfocus=y" target="_blank" rel="noopener noreferrer">a</a>'), /link attributes/);
  assert.match(check('<img src=x>'), /element <img>/);
  assert.match(check('<a href="javascript:x" target="_blank" rel="noopener noreferrer">a</a>'), /href/);
  assert.match(check('<a href="https://a.example" target="_blank" rel="noopener noreferrer"><a href="https://b.example" target="_blank" rel="noopener noreferrer">b</a></a>'), /inside a link/);
  assert.match(check('<code class="x" onmouseover=y>'), /attribute onmouseover/);
  assert.match(linkTextMatchesHref('<a href="https://evil.example" target="_blank" rel="noopener noreferrer">https://good.example</a>'), /opens/);
  assert.match(linkTextMatchesHref('<a href="https://evil.example" target="_blank" rel="noopener noreferrer"><strong>HTTP</strong>S:/\u200b/good.example</a>'), /opens/);
  assert.match(check('<a href="https://a@/" target="_blank" rel="noopener noreferrer">a</a>'), /href/);
  assert.equal(check('<p><a href="https://a.example/?a=1&amp;b" target="_blank" rel="noopener noreferrer">x</a></p>'), '');
});
