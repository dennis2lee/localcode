'use strict';

// renderMarkdown is the only place in the app where model output — text an
// attacker can influence through a web page a tool fetched, a file the model
// read, or a prompt-injected reply — becomes HTML. Its escaping rules and its
// placeholder scheme are what these tests pin down.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

let app;
test.before(async () => {
  app = await load();
});

test('escapeHtml escapes every character that can break out of markup', () => {
  assert.equal(app.escapeHtml(`<>&"'`), '&lt;&gt;&amp;&quot;&#39;');
});

test('markdown renders paragraphs, headers, rules and emphasis', () => {
  assert.equal(app.renderMarkdown('plain line'), '<p>plain line</p>');
  assert.equal(app.renderMarkdown('### Title'), '<h3>Title</h3>');
  assert.equal(app.renderMarkdown('---'), '<hr>');
  assert.equal(app.renderMarkdown('**b** and *i*'), '<p><strong>b</strong> and <em>i</em></p>');
});

test('markdown groups consecutive bullets into one list and closes it', () => {
  assert.equal(app.renderMarkdown('- a\n- b'), '<ul>\n<li>a</li>\n<li>b</li>\n</ul>');
  assert.equal(app.renderMarkdown('1. a\n2. b'), '<ol>\n<li>a</li>\n<li>b</li>\n</ol>');
  // A non-list line closes the list rather than leaving <ul> hanging open.
  assert.ok(app.renderMarkdown('- a\ntail').includes('</ul>'));
});

test('markdown renders block quotes from the escaped > that escapeHtml leaves', () => {
  assert.equal(app.renderMarkdown('> quoted'), '<blockquote>quoted</blockquote>');
});

test('markdown links only accept http(s) targets', () => {
  assert.equal(
    app.renderMarkdown('[x](https://example.com/a)'),
    '<p><a href="https://example.com/a" target="_blank" rel="noopener noreferrer">x</a></p>',
  );
  // javascript: is not a link at all — it stays literal text.
  const out = app.renderMarkdown('[x](javascript:alert(1))');
  assert.ok(!out.includes('<a '), out);
  assert.ok(!out.includes('javascript:alert(1)"'), out);
});

test('model text can never inject an element', () => {
  const out = app.renderMarkdown('<script>alert(1)</script>\n<img src=x onerror=alert(1)>');
  assert.ok(!out.includes('<script'), out);
  assert.ok(!out.includes('<img'), out);
  assert.ok(out.includes('&lt;script&gt;'), out);
});

test('fenced code is escaped, keeps its language class, and is not marked up', () => {
  const out = app.renderMarkdown('```go\nif a < b && c { *p = "x" }\n```');
  assert.equal(
    out,
    '<pre><code class="language-go">if a &lt; b &amp;&amp; c { *p = &quot;x&quot; }</code></pre>',
  );
});

test('an unterminated fence still renders while the reply is streaming', () => {
  assert.equal(app.renderMarkdown('```\nhalf a block'), '<pre><code>half a block</code></pre>');
});

test('inline code spans are preserved', () => {
  assert.equal(app.renderMarkdown('use `a < b` here'), '<p>use <code>a &lt; b</code> here</p>');
});

// Regression, B6. The code-block placeholder used to be a bare number, so any
// model text with a plain number on its own line after a code block matched
// the splice-back regexp and was replaced by the wrong block — or, past the
// end of the array, by the literal string "undefined".
test('a bare number after a code block stays that number', () => {
  const out = app.renderMarkdown('```\ncode\n```\n\n3\n');
  assert.ok(out.includes('<pre><code>code</code></pre>'), out);
  assert.ok(out.includes('<p>3</p>'), out);
  assert.ok(!out.includes('undefined'), out);
});

test('several code blocks splice back in the order they were written', () => {
  const out = app.renderMarkdown('```\nfirst\n```\n\n1\n\n```\nsecond\n```');
  const firstAt = out.indexOf('first');
  const secondAt = out.indexOf('second');
  assert.ok(firstAt >= 0 && secondAt > firstAt, out);
  assert.ok(out.includes('<p>1</p>'), out);
});

test('no placeholder sentinel survives into the rendered output', () => {
  const out = app.renderMarkdown('```\nx\n```\n\ntail 7 tail\n');
  assert.ok(!out.includes('\u0000'), JSON.stringify(out));
});

test('partial input never throws — every delta re-renders the whole buffer', () => {
  const partials = ['`', '``', '```', '```j', '```js\n', '```js\nx', '[', '[a', '[a](', '**', '*', '> ', '- '];
  for (const p of partials) {
    assert.doesNotThrow(() => app.renderMarkdown(p), `renderMarkdown(${JSON.stringify(p)}) threw`);
  }
});

test('streaming a reply one delta at a time ends up identical to rendering it whole', () => {
  const full = '# Title\n\nSome **bold** text.\n\n```go\nfmt.Println("hi")\n```\n\n- one\n- two\n';
  const el = app.el('transcript');
  el.innerHTML = '';
  for (const ch of full) app.applyEvent({ type: 'message.part.delta', data: { text: ch } });
  const streamed = el.innerHTML;
  app.applyEvent({ type: 'message.part.end' });
  assert.ok(streamed.includes(app.renderMarkdown(full)), streamed);
});

// Regression: the renderer emitted one <p> per source line and an empty
// entry per blank line, and #transcript's white-space: pre-wrap was
// inherited by .msg-model — so every newline in the generated HTML also
// rendered literally, on top of the block margins. An ordinary reply came
// out with a gaping hole between every paragraph, list and code block.
test('a paragraph spread over several lines is one <p>, not one per line', () => {
  const out = app.renderMarkdown('the parser drops\nthe trailing token\nin some cases');
  assert.equal(out, '<p>the parser drops the trailing token in some cases</p>');
});

test('blank lines separate blocks without leaving empty output entries', () => {
  const out = app.renderMarkdown('first\n\n\n\nsecond');
  assert.equal(out, '<p>first</p>\n<p>second</p>');
});

test('a blank line ends a list, and the next prose is its own paragraph', () => {
  const out = app.renderMarkdown('- a\n- b\n\ntail');
  assert.equal(out, '<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n<p>tail</p>');
});

test('prose wrapped around a code block keeps its blocks adjacent', () => {
  const out = app.renderMarkdown('before\n\n```\nx\n```\n\nafter');
  assert.equal(out, '<p>before</p>\n<pre><code>x</code></pre>\n<p>after</p>');
});

// Models produce tables constantly — comparisons, option lists, API
// summaries. The renderer had no table support at all, so one arrived as
// a paragraph of literal pipes and dashes.
test('a table renders as a table', () => {
  const html = app.renderMarkdown([
    '| Field | Meaning |',
    '|---|---|',
    '| `providers` | Model backends |',
    '| agents | Maps a name to a profile |',
  ].join('\n'));

  assert.match(html, /<table>/);
  assert.match(html, /<th[^>]*>Field<\/th>/);
  assert.match(html, /<th[^>]*>Meaning<\/th>/);
  assert.match(html, /<td[^>]*><code>providers<\/code><\/td>/);
  assert.match(html, /<td[^>]*>Maps a name to a profile<\/td>/);
  // Nothing is left over as literal markdown.
  assert.ok(!html.includes('|---|'), html);
  assert.ok(!/<p>[^<]*\|/.test(html), html);
});

test('column alignment from the delimiter row reaches the cells', () => {
  const html = app.renderMarkdown([
    '| l | c | r | n |',
    '|:---|:---:|---:|---|',
    '| a | b | c | d |',
  ].join('\n'));
  assert.match(html, /<th style="text-align:left">l<\/th>/);
  assert.match(html, /<th style="text-align:center">c<\/th>/);
  assert.match(html, /<th style="text-align:right">r<\/th>/);
  assert.match(html, /<th>n<\/th>/);
});

// A pipe inside a code span is not a cell boundary. Splitting there
// would cut the <code> tag in half across two cells.
test('a pipe inside inline code does not split a cell', () => {
  const html = app.renderMarkdown([
    '| expr | note |',
    '|---|---|',
    '| `a|b` | alternation |',
  ].join('\n'));
  assert.match(html, /<td[^>]*><code>a\|b<\/code><\/td>/);
});

test('an escaped pipe is a literal pipe, not a boundary', () => {
  const html = app.renderMarkdown('| a | b |\n|---|---|\n| x \\| y | z |');
  assert.match(html, /<td[^>]*>x \| y<\/td>/);
  assert.match(html, /<td[^>]*>z<\/td>/);
});

// The renderer re-runs on the whole buffer for every streamed delta, so
// it constantly sees a table that is not finished being written.
test('a half-written table does not break the render', () => {
  const header = app.renderMarkdown('| a | b |');
  // No delimiter row yet, so this is not a table — but it must not throw
  // and must not silently vanish.
  assert.match(header, /a/);

  const noRows = app.renderMarkdown('| a | b |\n|---|---|');
  assert.match(noRows, /<table>/);
  assert.match(noRows, /<th[^>]*>a<\/th>/);
  assert.ok(!noRows.includes('<tbody>'), noRows);

  const midRow = app.renderMarkdown('| a | b |\n|---|---|\n| one |');
  assert.match(midRow, /<td[^>]*>one<\/td>/);
});

test('text after a table is not swallowed by it', () => {
  const html = app.renderMarkdown('| a |\n|---|\n| 1 |\n\nAfter the table.');
  assert.match(html, /<table>/);
  assert.match(html, /<p>After the table\.<\/p>/);
});

// A row of dashes is a horizontal rule; a row of dashes and pipes is a
// table delimiter. Confusing the two would turn every table into an <hr>.
test('a horizontal rule still renders as one', () => {
  assert.match(app.renderMarkdown('above\n\n---\n\nbelow'), /<hr>/);
});

test('cell contents are escaped, not interpreted as HTML', () => {
  const html = app.renderMarkdown('| x |\n|---|\n| <img src=q onerror=alert(1)> |');
  assert.ok(!html.includes('<img'), html);
  assert.match(html, /&lt;img/);
});

// Some models write arrows and names as LaTeX — right for a client with
// MathJax in it, literal noise here. The model is asked not to (see
// internal/agent/quirks.go); this is the other half, for the replies that
// already exist and the times it does it anyway.
test('inline LaTeX a model meant as a symbol is unwrapped', async () => {
  const app = await load();
  const cases = [
    ['a $\\rightarrow$ b', 'a → b'],
    ['$\\text{Bla-Bla}$ is the name', 'Bla-Bla is the name'],
    ['x $\\le$ y and p $\\times$ q', 'x ≤ y and p × q'],
    ['$\\mathrm{max}$ tokens', 'max tokens'],
  ];
  for (const [input, want] of cases) {
    assert.equal(app.unwrapMath(input), want, input);
  }
});

// Narrow on purpose: a dollar sign is usually a dollar sign.
test('ordinary dollars are left alone', async () => {
  const app = await load();
  for (const s of ['it costs $5 and $10', 'echo $PATH is $HOME', 'no math here']) {
    assert.equal(app.unwrapMath(s), s, s);
  }
});

// Real maths is not something this can render, and half-translating it
// would be worse than leaving the delimiters that say what it was.
test('actual formulas keep their delimiters', async () => {
  const app = await load();
  const s = 'the bound is $\\sum_{i=1}^{n} x_i^2$ here';
  assert.equal(app.unwrapMath(s), s);
});

// And it runs as part of rendering a reply, not just on its own.
test('a rendered reply shows the symbol, not the LaTeX', async () => {
  const app = await load();
  const html = app.renderMarkdown('plan: read $\\rightarrow$ edit');
  assert.match(html, /read → edit/);
  assert.ok(!html.includes('rightarrow'), html);
});

// The one that got through: a model writing a range as `$S1 \sim S6$`.
// Ranges like that are ordinary prose, not formulas, and far more common
// than anything this could not translate.
test('symbols outside the first table are unwrapped too', async () => {
  const app = await load();
  const cases = [
    ['$S1 \\sim S6$ 구간', 'S1 ∼ S6 구간'],
    ['$\\alpha$ and $\\Omega$', 'α and Ω'],
    ['$x \\in S$', 'x ∈ S'],
    ['$a \\ne b$, $c \\propto d$', 'a ≠ b, c ∝ d'],
    ['$\\text{peak} \\approx 3$', 'peak ≈ 3'],
  ];
  for (const [input, want] of cases) {
    assert.equal(app.unwrapMath(input), want, input);
  }
});
