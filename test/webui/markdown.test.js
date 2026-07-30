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
