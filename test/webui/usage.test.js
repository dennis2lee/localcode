'use strict';

// Spend by model, in the page.
//
// /usage answers per-model token totals for this conversation, and with
// all|today|week|month across every conversation the daemon holds. The
// status line's context percentage is a different thing — how full this
// conversation's window is. This window draws the "all" totals: one
// labelled bar per model, from GET /api/usage, which the daemon answers
// with the function /usage all uses, so the two cannot disagree.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// A summary the way the daemon writes one.
function summary(models, extra = {}) {
  return { scope: 'every conversation', models, sessions: 3, unread: 0, ...extra };
}

function figures(input, output, calls, cache = {}) {
  return {
    input_tokens: input, output_tokens: output,
    cache_read_tokens: cache.read || 0, cache_write_tokens: cache.write || 0,
    cache_read_or_write_tokens: cache.unsplit || 0, calls,
  };
}

async function withUsage(answer) {
  return load({ routes: { 'GET /api/usage': answer } });
}

async function openUsage(app) {
  app.el('usage-btn').fire('click');
  await app.settle();
}

function rows(app) {
  return Array.from(app.el('usage-rows').querySelectorAll('.usage-row'));
}

function figuresByModel(app) {
  const out = {};
  for (const row of rows(app)) {
    out[row.querySelector('.usage-model').textContent] = row.querySelector('.usage-figures').textContent;
  }
  return out;
}

test('opening the window asks the daemon for every conversation\'s totals', async () => {
  const app = await withUsage(summary({ 'model-a': figures(100, 20, 2) }));
  await openUsage(app);
  const asked = app.calls.filter((c) => c.path === '/api/usage');
  assert.equal(asked.length, 1, 'the window did not ask the daemon');
  assert.equal(asked[0].query.get('window'), 'all');
  assert.deepEqual(figuresByModel(app), { 'model-a': 'input 100 · output 20 · total 120 (2 calls)' });
  assert.match(app.el('usage-scope').textContent, /\(3 sessions\)/);
});

test('each bar says what it holds: input and output as separate segments', async () => {
  const app = await withUsage(summary({
    'model-a': figures(100, 100, 2),
    'model-b': figures(25, 25, 1),
  }));
  await openUsage(app);
  const [first, second] = rows(app);
  const segWidth = (row, cls) => row.querySelector(cls).style.width;
  // Scaled to the largest model's total (200): the smaller model's
  // 25-input segment is an eighth of the track, not a half of its own row.
  assert.equal(segWidth(first, '.usage-input'), '50%');
  assert.equal(segWidth(first, '.usage-output'), '50%');
  assert.equal(segWidth(second, '.usage-input'), '12.5%');
  assert.equal(segWidth(second, '.usage-output'), '12.5%');
  assert.match(figuresByModel(app)['model-a'], /input 100 · output 100 · total 200/);
});

// Under a working prompt cache the repeatedly sent history is not in
// input_tokens: the provider reports it as a cache read, and the first
// time a prefix is cached as a cache write. Both are billed, apart from
// input and from each other, so each has its own figure and its own
// segment, and the total counts them.
test('the cache read and write are counted, named, and drawn apart', async () => {
  const app = await withUsage(summary({ 'model-a': figures(20, 100, 2, { read: 8096, write: 128 }) }));
  await openUsage(app);
  assert.equal(figuresByModel(app)['model-a'],
    'input 20 · cache read 8096 · cache write 128 · output 100 · total 8344 (2 calls)');
  const [row] = rows(app);
  const width = (cls) => row.querySelector(cls).style.width;
  assert.equal(width('.usage-cache-read'), `${(8096 / 8344) * 100}%`);
  assert.equal(width('.usage-cache-write'), `${(128 / 8344) * 100}%`);
  assert.equal(width('.usage-input'), `${(20 / 8344) * 100}%`);
  assert.equal(width('.usage-output'), `${(100 / 8344) * 100}%`);
  // The tooltip names the same figures the row does.
  assert.equal(row.querySelector('.usage-track').title,
    `model-a: ${row.querySelector('.usage-figures').textContent}, across every conversation, archived ones included`);
});

// A log written before the split was recorded names its cached prompt as
// one figure; the daemon reports it as cache_read_or_write_tokens. And a
// provider with no cache reads as it always did: no cache figure, no
// cache segment.
test('an unsplit cached figure is its own column, and no cache draws nothing', async () => {
  const app = await withUsage(summary({
    'old-log': figures(12, 30, 1, { unsplit: 4224 }),
    'no-cache': figures(150, 40, 1),
  }));
  await openUsage(app);
  const byModel = figuresByModel(app);
  assert.equal(byModel['old-log'], 'input 12 · cache read or write 4224 · output 30 · total 4266 (1 call)');
  assert.equal(byModel['no-cache'], 'input 150 · output 40 · total 190 (1 call)');
  const plain = rows(app).find((r) => r.querySelector('.usage-model').textContent === 'no-cache');
  assert.equal(plain.querySelectorAll('.usage-seg').length, 2, 'a provider with no cache drew a cache segment');
  const old = rows(app).find((r) => r.querySelector('.usage-model').textContent === 'old-log');
  assert.ok(old.querySelector('.usage-cache-unsplit'), 'the unsplit cached figure has no segment');
});

// Most-spending first, by the total that counts the cache, not by input
// and output alone: the cache-heavy model is second in the answer and
// first on screen.
test('rows are ordered by the total, cache included', async () => {
  const app = await withUsage(summary({
    'model-b': figures(150, 40, 1),
    'model-a': figures(12, 30, 1, { read: 4096 }),
  }));
  await openUsage(app);
  assert.deepEqual(rows(app).map((r) => r.querySelector('.usage-model').textContent), ['model-a', 'model-b']);
});

// Five colours of segment, and a bar whose colours and scale are a
// secret is decoration. The key names the kinds some row drew, and only
// those, each swatch the colour of the segment it names, and the total
// the bars are scaled to. Where there is a cache figure, the daemon's own
// note says what it is, in the words /usage prints.
test('the key names the segments drawn, their colours, the scale, and what the cache is', async () => {
  const note = 'Cache read and cache write are prompt the provider served from its prompt cache or wrote to it.';
  const app = await withUsage(summary({
    'model-a': figures(12, 30, 1, { read: 4096 }),
    'model-b': figures(150, 40, 1),
  }, { note }));
  await openUsage(app);
  const legend = app.el('usage-rows').querySelector('.usage-legend');
  assert.ok(legend, 'no key under the rows');
  const keys = Array.from(legend.querySelectorAll('.usage-key'));
  assert.deepEqual(keys.map((k) => k.textContent), ['input', 'cache read', 'output']);
  const drawn = new Set(Array.from(app.el('usage-rows').querySelectorAll('.usage-seg'), (s) => s.className.split(' ')[1]));
  for (const k of keys) {
    const cls = k.querySelector('.usage-swatch').className.split(' ')[1];
    assert.ok(drawn.has(cls), `the key for ${k.textContent} is coloured ${cls}, which no segment is`);
  }
  assert.match(legend.textContent, /scaled to the largest total, 4138 tokens/);
  assert.equal(legend.querySelector('.usage-legend-note').textContent, note);
});

test('with no cache there is no cache note', async () => {
  const app = await withUsage(summary({ 'model-b': figures(150, 40, 1) }));
  await openUsage(app);
  assert.equal(app.el('usage-rows').querySelector('.usage-legend-note'), null);
});

test('no usage at all says so', async () => {
  const app = await withUsage(summary({}, { sessions: 0 }));
  await openUsage(app);
  assert.deepEqual(rows(app), []);
  assert.match(app.el('usage-rows').textContent, /No usage recorded yet/);
});

test('huge totals never touch the status line or its warning thresholds', async () => {
  const app = await withUsage(summary({ 'model-a': figures(9000000, 9000000, 1) }));
  await openUsage(app);
  const bar = app.el('prompt-status');
  assert.ok(!bar.classList.contains('ctx-warn') && !bar.classList.contains('ctx-crit'),
    'a token total tripped the context-window warning styling');
  assert.ok(!app.el('status-text').textContent.includes('9000000'),
    'a token total leaked into the status line beside the context percentage');
});

test('a conversation that cannot be read is named, not silently dropped', async () => {
  const app = await withUsage(summary({ 'model-a': figures(1, 1, 1) }, { unread: 2 }));
  await openUsage(app);
  assert.match(app.el('usage-note').textContent, /2 session logs could not be read and are not in this total/);
  assert.match(app.el('usage-note').textContent, /Counted from the sessions’ own logs/);
});

test('totals the daemon could not give are said, not drawn as nothing', async () => {
  const app = await withUsage({ networkError: 'connection refused' });
  await openUsage(app);
  assert.match(app.el('usage-note').textContent, /could not be read: .*connection refused/);
  assert.deepEqual(rows(app), []);
  assert.doesNotMatch(app.el('usage-rows').textContent, /No usage recorded yet/, 'an unknown total was drawn as none');
});

test('closing the window closes it', async () => {
  const app = await withUsage(summary({ 'model-a': figures(1, 1, 1) }));
  await openUsage(app);
  app.el('usage-close').fire('click');
  assert.equal(app.internals.usageView.isOpen, false);
});

test('one session is one session', async () => {
  const app = await withUsage(summary({ 'model-a': figures(1, 1, 1) }, { sessions: 1, unread: 1 }));
  await openUsage(app);
  assert.match(app.el('usage-scope').textContent, /\(1 session\)/);
  assert.match(app.el('usage-note').textContent, /1 session log could not be read and is not in this total/);
});

// Closed and opened again while the first answer is still on its way: the
// older answer, arriving last, is not drawn over the newer one.
test('an answer an opening has overtaken is not drawn', async () => {
  let answer = 0;
  const releases = [];
  const app = await withUsage(() => new Promise((resolve) => {
    const n = ++answer;
    releases.push(() => resolve(summary({ [`model-${n}`]: figures(n, n, 1) })));
  }));
  app.el('usage-btn').fire('click');
  app.el('usage-close').fire('click');
  app.el('usage-btn').fire('click');
  await app.settle();
  releases[1]();
  await app.settle();
  releases[0]();
  await app.settle();
  assert.deepEqual(Object.keys(figuresByModel(app)), ['model-2'], 'the first answer, arriving last, was drawn over the second');
});
