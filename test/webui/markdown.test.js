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

// An identifier is not emphasis. A model naming read_file, write_file or
// task_tool.go in prose writes underscores, and treating them as italics
// deletes the character that makes the name a name.
//
// The damage is not limited to the one word either: the closing _ of a
// pair can be found in the *next* identifier, so "call read_file then
// write_file now" came out as one italic run spanning the middle of the
// sentence with two underscores gone.
//
// This is also what CommonMark says: a _ opens emphasis only when it is
// not inside a word, which is exactly the rule that separates prose from
// snake_case. Asterisks keep their intraword behaviour, which CommonMark
// also allows and which nothing writes by accident.
test('markdown leaves the underscores in identifiers alone', () => {
  for (const name of [
    'read_file_contents',
    'some_file_name.go',
    'call read_file then write_file now',
    '_leading_underscore_name',
    'trailing_name_',
    'a_b_c_d',
  ]) {
    const out = app.renderMarkdown(name);
    assert.equal(out, `<p>${name}</p>`, `${name} was rendered as ${out}`);
  }
});

test('markdown still emphasises underscores that are not inside a word', () => {
  assert.equal(app.renderMarkdown('a _real_ emphasis'), '<p>a <em>real</em> emphasis</p>');
  assert.equal(app.renderMarkdown('_opening_ the line'), '<p><em>opening</em> the line</p>');
  assert.equal(app.renderMarkdown('ends with _this_.'), '<p>ends with <em>this</em>.</p>');
  assert.equal(app.renderMarkdown('(_parenthesised_)'), '<p>(<em>parenthesised</em>)</p>');
  assert.equal(app.renderMarkdown('__bold__ here'), '<p><strong>bold</strong> here</p>');
  // Korean, and every other script: JavaScript's \w is ASCII-only, so a
  // boundary test written with it would call a Hangul syllable a
  // non-word character and italicise 강조_텍스트_ against the same rule
  // this test exists to enforce.
  assert.equal(app.renderMarkdown('한글 _강조_ 입니다'), '<p>한글 <em>강조</em> 입니다</p>');
  assert.equal(app.renderMarkdown('식별자_이름_형식'), '<p>식별자_이름_형식</p>');
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

test('fenced code is escaped, keeps its language class, and is highlighted', () => {
  const out = app.renderMarkdown('```go\nif a < b && c { *p = "x" }\n```');
  assert.ok(out.includes('<pre><code class="language-go">'), out);
  // Escaped, not interpreted: the raw characters are gone.
  assert.ok(out.includes('a &lt; b &amp;&amp; c'), out);
  assert.ok(!out.includes(' & '), out);
  // Highlighted: the keyword and the string are tinted spans.
  assert.match(out, /<span class="tok-kw">if<\/span>/);
  assert.match(out, /<span class="tok-str">&quot;x&quot;<\/span>/);
  // Markdown inside code is still not markup: *p did not become emphasis.
  assert.ok(!out.includes('<em>'), out);
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

// The next one through: `$\mathbf{\text{ vs }}$`, a word wrapped twice.
// One pass takes the inner \text and leaves the \mathbf, which then reads
// as real maths — so the whole thing stayed on screen as source.
test('font commands nested in each other are all unwrapped', async () => {
  const app = await load();
  const cases = [
    ['Retention $\\mathbf{\\text{ vs }}$ Present Invention', 'Retention vs Present Invention'],
    ['$\\text{\\mathbf{Ring}}$ buffer', 'Ring buffer'],
    ['$\\boldsymbol{\\mathrm{\\text{Fork}}}$', 'Fork'],
    ['$\\mathbf{\\text{S1}} \\sim \\mathbf{\\text{S6}}$', 'S1 ∼ S6'],
  ];
  for (const [input, want] of cases) {
    assert.equal(app.unwrapMath(input), want, input);
  }
});

// A web address the model wrote is a link wherever it is, opening in the
// person's own browser (target="_blank"; js/links.js for the desktop
// window). What is around it stays text.
const link = (u) => `<a href="${u}" target="_blank" rel="noopener noreferrer">${u}</a>`;

test('a bare web address in prose is a link, without the punctuation after it', () => {
  assert.equal(app.renderMarkdown('see https://example.com/a for more.'),
    `<p>see ${link('https://example.com/a')} for more.</p>`);
  assert.equal(app.renderMarkdown('http://localhost:8080/x, then'),
    `<p>${link('http://localhost:8080/x')}, then</p>`);
  assert.equal(app.renderMarkdown('Is it https://example.com?'),
    `<p>Is it ${link('https://example.com')}?</p>`);
});

test('an address keeps its query, and its own brackets, but not the ones around it', () => {
  assert.equal(app.renderMarkdown('https://x.example/?a=1&b=2'),
    `<p>${link('https://x.example/?a=1&amp;b=2')}</p>`);
  assert.equal(app.renderMarkdown('(see https://en.example/wiki/Foo_(bar))'),
    `<p>(see ${link('https://en.example/wiki/Foo_(bar)')})</p>`);
  assert.equal(app.renderMarkdown('"https://x.example/q" and <https://y.example/>'),
    `<p>&quot;${link('https://x.example/q')}&quot; and &lt;${link('https://y.example/')}&gt;</p>`);
});

// An address is full of underscores and asterisks; none of them is
// emphasis, and the address is not cut where one is.
test('emphasis does not cut an address, and still goes around one', () => {
  assert.equal(app.renderMarkdown('https://x.example/_next/static/a_b.js'),
    `<p>${link('https://x.example/_next/static/a_b.js')}</p>`);
  assert.equal(app.renderMarkdown('https://x.example/_a_/b?q=*x*&r=1'),
    `<p>${link('https://x.example/_a_/b?q=*x*&amp;r=1')}</p>`);
  assert.equal(app.renderMarkdown('**https://x.example/**'),
    `<p><strong>${link('https://x.example/')}</strong></p>`);
});

test('an address in inline code and in a code block is a link too', () => {
  assert.equal(app.renderMarkdown('run `curl https://api.example/v1`'),
    `<p>run <code>curl ${link('https://api.example/v1')}</code></p>`);
  const block = app.renderMarkdown('```\nGET https://api.example/v1/items\n```');
  assert.ok(block.includes(link('https://api.example/v1/items')), block);
});

test('a [text](address) link stays one link, its text keeping its emphasis', () => {
  assert.equal(app.renderMarkdown('[docs](https://x.example/d)'),
    '<p><a href="https://x.example/d" target="_blank" rel="noopener noreferrer">docs</a></p>');
  assert.equal(app.renderMarkdown('[**docs**](https://x.example/d)'),
    '<p><a href="https://x.example/d" target="_blank" rel="noopener noreferrer"><strong>docs</strong></a></p>');
  assert.equal(app.renderMarkdown('[https://x.example/](https://x.example/)'),
    '<p><a href="https://x.example/" target="_blank" rel="noopener noreferrer">https://x.example/</a></p>');
});

test('only http and https with a host are links', () => {
  for (const s of ['https://', 'ftp://x.example/a', 'javascript:alert(1)', 'mailto:a@x.example', 'file:///etc/passwd']) {
    assert.ok(!app.renderMarkdown(s).includes('<a '), s);
  }
});

// The address is matched in escaped text, so nothing in it can end the
// attribute it goes into.
test('a quote or angle bracket the model put after an address ends it', () => {
  const out = app.renderMarkdown('https://x.example/"onmouseover=alert(1)<b>');
  assert.ok(out.includes(`${link('https://x.example/')}&quot;onmouseover=alert(1)&lt;b&gt;`), out);
  assert.equal((out.match(/<a /g) || []).length, 1);
});

// A Korean particle is written onto the word before it, an address
// included. The address is linked without it; a path in Korean is kept.
test('a word in another script written onto an address is not part of it', () => {
  assert.equal(app.renderMarkdown('https://example.com에서 확인하세요.'),
    `<p>${link('https://example.com')}에서 확인하세요.</p>`);
  assert.equal(app.renderMarkdown('https://x.example/docs를 보세요'),
    `<p>${link('https://x.example/docs')}를 보세요</p>`);
  assert.equal(app.renderMarkdown('(https://x.example/a)에서'),
    `<p>(${link('https://x.example/a')})에서</p>`);
  assert.equal(app.renderMarkdown('문서: https://ko.example/위키/서울 참고'),
    `<p>문서: ${link('https://ko.example/위키/서울')} 참고</p>`);
});

// Every start tag of a link, read the way an HTML tokenizer reads it: the
// attribute names, quoted values and unquoted ones alike. A link the model
// wrote has href, target and rel, and nothing else.
function linkAttrs(html) {
  const out = [];
  for (const m of html.matchAll(/<a\s([^>]*)>/g)) {
    const names = [];
    const re = /\s*([^\s=/>"']+)(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]*))?/g;
    let a;
    while ((a = re.exec(m[1])) && a[0].length) names.push(a[1]);
    out.push(names.join(' '));
  }
  return out;
}

test('nothing inside a [text](address) target can end its href and start another attribute', () => {
  for (const s of [
    '[x](https://a.example/`https://q.example/onmouseover=alert(1)//`)',
    '[x](https://a.example/```\nhttps://q.example/onmouseover=alert(1)\n```)',
    '# [x](https://a.example/`https://q.example/onmouseover=alert(1)`)',
    '[x](https://a.example/```x onmouseover=alert(1)//\ncode\n```)',
  ]) {
    const out = app.renderMarkdown(s);
    for (const attrs of linkAttrs(out)) assert.equal(attrs, 'href target rel', `${s}\n=> ${out}`);
  }
});

test('a fence language names a class and nothing else', () => {
  const out = app.renderMarkdown('```x onmouseover=alert(1)//\ncode\n```');
  assert.ok(out.startsWith('<pre><code class="language-xonmouseoveralert1">'), out);
  assert.ok(!/<code[^>]*\sonmouseover/.test(out), out);
});

// The placeholders are made of control characters; one the model wrote is
// not one.
test('a placeholder character the model wrote is dropped, not read as a placeholder', () => {
  assert.equal(app.renderMarkdown('before \u00020\u0002 after'), '<p>before 0 after</p>');
  assert.equal(app.renderMarkdown('a \u00010\u0001 \u00000\u0000 b'), '<p>a 0 0 b</p>');
});

test('punctuation from other scripts, curly quotes and an ellipsis are not part of an address', () => {
  assert.equal(app.renderMarkdown('https://example.com。'), `<p>${link('https://example.com')}。</p>`);
  assert.equal(app.renderMarkdown('“https://example.com/x”'), `<p>“${link('https://example.com/x')}”</p>`);
  assert.equal(app.renderMarkdown('https://example.com/x…'), `<p>${link('https://example.com/x')}…</p>`);
  assert.equal(app.renderMarkdown('https://ko.example/위키/가이드。'), `<p>${link('https://ko.example/위키/가이드')}。</p>`);
});

// Highlighting puts tags round numbers and keywords; an address in a code
// block is linked whole whatever it put round its parts.
test('an address in a code block is one link, port and dates included', () => {
  for (const u of ['http://localhost:8080/api', 'http://127.0.0.1:4096/', 'https://example.com/2024/05/post']) {
    for (const lang of ['sh', 'bash', '', 'js']) {
      const out = app.renderMarkdown('```' + lang + '\ncurl ' + u + ' # 42\n```');
      assert.ok(out.includes(link(u)), `${lang}: ${out}`);
    }
  }
  const quoted = app.renderMarkdown('```js\nfetch("https://api.example/v1?a=1&b=2")\n```');
  assert.ok(quoted.includes(link('https://api.example/v1?a=1&amp;b=2')), quoted);
});

test('an address keeps a trailing "&", and an apostrophe inside a word', () => {
  assert.equal(app.renderMarkdown('https://a.example/?a=1&'), `<p>${link('https://a.example/?a=1&amp;')}</p>`);
  assert.equal(app.renderMarkdown("https://en.example/wiki/Hawai'i is"), `<p>${link('https://en.example/wiki/Hawai&#39;i')} is</p>`);
  assert.equal(app.renderMarkdown("'https://x.example/q'"), `<p>&#39;${link('https://x.example/q')}&#39;</p>`);
});

test('a [text](address) target keeps the brackets it balances', () => {
  assert.equal(app.renderMarkdown('[Function](https://en.example/wiki/Function_(mathematics))'),
    '<p><a href="https://en.example/wiki/Function_(mathematics)" target="_blank" rel="noopener noreferrer">Function</a></p>');
});

// The model's text is what a page it read can write. A link whose text is
// a web address opens that address; the one it pointed at is shown beside
// it, so no link shows one address and opens another.
test('a link whose text is another address does not open somewhere else', () => {
  assert.equal(app.renderMarkdown('[https://good.example](https://evil.example)'),
    `<p>${link('https://good.example')} (${link('https://evil.example')})</p>`);
  assert.equal(app.renderMarkdown('[https://same.example/](https://same.example/)'),
    `<p>${link('https://same.example/')}</p>`);
});

test('math is not unwrapped inside an address', () => {
  assert.equal(app.renderMarkdown('https://x.example/p?x=$5\\sim$'), `<p>${link('https://x.example/p?x=$5\\sim$')}</p>`);
});

test('addresses side by side in quotes or brackets are each their own link', () => {
  assert.equal(app.renderMarkdown('"https://a.example","https://b.example"'),
    `<p>&quot;${link('https://a.example')}&quot;,&quot;${link('https://b.example')}&quot;</p>`);
  assert.equal(app.renderMarkdown('(https://a.example),(https://b.example)'),
    `<p>(${link('https://a.example')}),(${link('https://b.example')})</p>`);
  assert.equal(app.renderMarkdown('<https://a.example>,<https://b.example>'),
    `<p>&lt;${link('https://a.example')}&gt;,&lt;${link('https://b.example')}&gt;</p>`);
  const code = app.renderMarkdown('```json\n["https://a.example","https://b.example"]\n```');
  assert.ok(code.includes(link('https://a.example')) && code.includes(link('https://b.example')), code);
});

test('an address with no host is not a link', () => {
  for (const s of ['https:///', 'https://:8080', 'https://@', 'https://?a=1', '[x](https:///)', '[x](https://#top)']) {
    assert.ok(!app.renderMarkdown(s).includes('<a '), s);
  }
  assert.equal(app.renderMarkdown('http://[::1]:8080/a'), `<p>${link('http://[::1]:8080/a')}</p>`);
});

// A link inside a link is not HTML: inline code in a link's text is drawn
// without its own links. Inline code that is another address is that
// address, shown beside the one the link pointed at.
test('inline code in a link\'s text does not make a link inside the link', () => {
  assert.equal(app.renderMarkdown('[see `curl -s x`](https://b.example)'),
    '<p><a href="https://b.example" target="_blank" rel="noopener noreferrer">see <code>curl -s x</code></a></p>');
  assert.equal(app.renderMarkdown('[see `curl https://a.example`](https://b.example)'),
    `<p>see <code>curl ${link('https://a.example')}</code> (${link('https://b.example')})</p>`);
  assert.equal(app.renderMarkdown('[`https://a.example`](https://b.example)'),
    `<p><code>${link('https://a.example')}</code> (${link('https://b.example')})</p>`);
});

test('an address in a link\'s text is not cut up by emphasis', () => {
  const u = 'https://x.example/_a_/b?q=*x*&amp;r=1';
  assert.equal(app.renderMarkdown('[https://x.example/_a_/b?q=*x*&r=1](https://x.example/_a_/b?q=*x*&r=1)'),
    `<p><a href="${u}" target="_blank" rel="noopener noreferrer">${u}</a></p>`);
});

// A link's text is read as a reader sees it before it is trusted not to
// be an address: emphasis, case, what shows as nothing and control
// characters do not hide one.
test('no way of writing an address in a link\'s text makes it open another', () => {
  const beside = (shown) => `<p>${shown} (${link('https://evil.example')})</p>`;
  assert.equal(app.renderMarkdown('[**http**s://good.example](https://evil.example)'), beside('<strong>http</strong>s://good.example'));
  assert.equal(app.renderMarkdown('[HTTPS://good.example](https://evil.example)'), beside('HTTPS://good.example'));
  assert.equal(app.renderMarkdown('[https:/\u200b/good.example](https://evil.example)'), beside('https:/\u200b/good.example'));
  assert.equal(app.renderMarkdown('[https:／／good.example](https://evil.example)'), beside('https:／／good.example'));
  assert.equal(app.renderMarkdown('[https://good.example\u0004](https://evil.example)'), beside(link('https://good.example')));
  assert.equal(app.renderMarkdown('[www.good.example](https://evil.example)'), beside('www.good.example'));
  // The address itself, plain, bold or with an ellipsis, is one link.
  assert.equal(app.renderMarkdown('[**https://same.example/**](https://same.example/)'), `<p>${link('https://same.example/')}</p>`);
  assert.equal(app.renderMarkdown('[https://same.example/…](https://same.example/…)'), `<p>${link('https://same.example/…')}</p>`);
});

test('a code block is never a link\'s text, in inline code or not', () => {
  for (const s of ['[```\nhttps://a.example\n```](https://b.example)', '[` ```\nhttps://a.example\n``` `](https://b.example)']) {
    const out = app.renderMarkdown(s);
    assert.ok(!/<a [^>]*>(?:(?!<\/a>)[\s\S])*<a /.test(out), `${s}\n=> ${out}`);
  }
});

test('a host is a name or an IPv6 address after any user part', () => {
  for (const s of ['https://a@', 'https://a@/', 'https://a@:8080', 'http://[', 'https://[]', '[x](https://a@/)']) {
    assert.ok(!app.renderMarkdown(s).includes('<a '), s);
  }
  assert.equal(app.renderMarkdown('https://user@host.example/a'), `<p>${link('https://user@host.example/a')}</p>`);
});

test('an apostrophe before a word in another script goes with the word', () => {
  assert.equal(app.renderMarkdown("https://example.com/'한글"), `<p>${link('https://example.com/')}&#39;한글</p>`);
});

// Two older faults the reviews found beside the links.
test('a double-backtick span inside a single-backtick one is part of its text', () => {
  assert.equal(app.renderMarkdown('`a``b``c`'), '<p><code>a``b``c</code></p>');
});

test('math reads only its own symbols, not an object\'s inherited names', () => {
  assert.equal(app.renderMarkdown('$\\toString$'), '<p>$\\toString$</p>');
});
