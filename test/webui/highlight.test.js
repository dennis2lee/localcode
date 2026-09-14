'use strict';

// Code highlighting (highlight.js, wired into renderMarkdown's fences)
// and the one document that exercises every construct end to end.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

let app;
test.before(async () => {
  app = await load();
});

test('one document exercising every construct renders each of them', () => {
  const doc = [
    '# Heading',
    '',
    'Some **bold** and *italic* with `inline` code.',
    '',
    '- one',
    '- two',
    '1. first',
    '2. second',
    '',
    '```go',
    'func main() {}',
    '```',
    '',
    '```',
    'plain block',
    '```',
    '',
    'Use ``code with `backticks` inside`` here.',
    '',
    'See [the docs](https://example.com/a) for more.',
    '',
    '> quoted',
    '',
    '---',
  ].join('\n');
  const out = app.renderMarkdown(doc);
  for (const want of [
    '<h1>Heading</h1>',
    '<strong>bold</strong>',
    '<em>italic</em>',
    '<code>inline</code>',
    '<ul>',
    '<li>one</li>',
    '<ol>',
    '<li>first</li>',
    'plain block',
    '<code>code with `backticks` inside</code>',
    '<a href="https://example.com/a"',
    '<blockquote>quoted</blockquote>',
    '<hr>',
  ]) {
    assert.ok(out.includes(want), `rendered document has no ${want}:\n${out}`);
  }
  assert.ok(!out.includes('```'), `fence markers leaked:\n${out}`);
  assert.ok(!out.includes('# Heading'), `heading marker leaked:\n${out}`);
});

test('text that is not markdown comes through unharmed', () => {
  const out = app.renderMarkdown(
    'call read_file then write_file now\nthe cost is $5 and $PATH is set',
  );
  for (const want of ['read_file', 'write_file', '$5', '$PATH']) {
    assert.ok(out.includes(want), `plain text lost ${want}:\n${out}`);
  }
  assert.ok(!out.includes('<h'), out);
  assert.ok(!out.includes('<em>'), out);
});

test('highlighting tints keywords, strings, comments and numbers', () => {
  const out = app.highlightCode('func main() { // greet\ns := "hi"\nn := 42\n}', 'go');
  assert.match(out, /<span class="tok-kw">func<\/span>/);
  assert.match(out, /<span class="tok-com">\/\/ greet<\/span>/);
  assert.match(out, /<span class="tok-str">&quot;hi&quot;<\/span>/);
  assert.match(out, /<span class="tok-num">42<\/span>/);
  // A word merely containing a keyword is not one.
  assert.ok(!out.includes('<span class="tok-kw">format</span>'),
    app.highlightCode('format := 1', 'go'));
});

test('an unknown language still escapes, and still colours strings', () => {
  const out = app.highlightCode('x = "<b>" // uno', 'klingon');
  assert.ok(!out.includes('<b>'), out);
  assert.ok(out.includes('&lt;b&gt;'), out);
  assert.match(out, /<span class="tok-str">/);
});

test('code can never inject an element through the highlighter', () => {
  const out = app.renderMarkdown('```js\n<script>alert(1)</script>\n```');
  assert.ok(!out.includes('<script'), out);
  assert.ok(out.includes('&lt;script&gt;'), out);
});

test('a fenced block with a language keeps its class and is highlighted', () => {
  const out = app.renderMarkdown('```go\nfunc main() {}\n```');
  assert.ok(out.includes('class="language-go"'), out);
  assert.ok(out.includes('tok-kw'), out);
});
