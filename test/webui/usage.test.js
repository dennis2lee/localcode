'use strict';

// Spend by model, in the page.
//
// /usage answers per-model token totals for this conversation, and with
// all|today|week|month across every conversation the daemon holds,
// archived ones included. The status line's context percentage is a
// different thing — how full this conversation's window is — and the only
// per-model figures the page had were the command's own text. This window
// draws them: one labelled bar per model, from the same two event kinds
// the command counts, over the same conversations it counts.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const VISIBLE = [
  { id: 'sess-1', title: 'first session', agent: 'general-purpose', workspace: '/tmp/workspace', created_at: '2026-01-02T03:04:05Z' },
  { id: 'sess-2', title: 'second session', agent: 'general-purpose', workspace: '/tmp/workspace', created_at: '2026-01-03T03:04:05Z' },
];
const ARCHIVED = [
  { id: 'sess-9', title: 'put away', agent: 'general-purpose', workspace: '/tmp/workspace', created_at: '2026-01-04T03:04:05Z' },
];

async function withSessions(extra = {}) {
  return load({
    routes: {
      'GET /api/sessions': (body, { query }) => (query.get('archived') ? ARCHIVED : VISIBLE),
      ...extra,
    },
  });
}

// usageStreams are the full-log tails this window opens: the page's own
// conversation stream carries ?tail=400, these carry no query at all.
function usageStreams(app) {
  return app.streams.filter((s) => !s.closed && s.url.includes('/api/sessions/') && !s.url.includes('?'));
}

async function openUsage(app) {
  app.el('usage-btn').fire('click');
  await app.settle();
  return usageStreams(app);
}

function rows(app) {
  return Array.from(app.el('usage-rows').querySelectorAll('.usage-row'));
}

function figuresByModel(app) {
  const out = {};
  for (const row of rows(app)) {
    const name = row.querySelector('.usage-model').textContent;
    out[name] = row.querySelector('.usage-figures').textContent;
  }
  return out;
}

test('opening the window tails every conversation including the archive', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  const ids = streams.map((s) => s.url).sort();
  assert.deepEqual(ids, [
    '/api/sessions/sess-1/events',
    '/api/sessions/sess-2/events',
    '/api/sessions/sess-9/events',
  ], 'the window did not read every conversation it claims to cover');
});

test('usage from every session adds up under its model', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  const byId = Object.fromEntries(streams.map((s) => [s.url.split('/')[3], s]));
  byId['sess-1'].emit({ type: 'usage', data: { model: 'model-a', input_tokens: 100, output_tokens: 20 } });
  byId['sess-2'].emit({ type: 'usage', data: { model: 'model-a', input_tokens: 50, output_tokens: 10 } });
  byId['sess-9'].emit({ type: 'usage', data: { model: 'model-b', input_tokens: 7, output_tokens: 3 } });
  const figures = figuresByModel(app);
  assert.equal(figures['model-a'], 'input 150 · output 30 · total 180 (2 calls)');
  assert.equal(figures['model-b'], 'input 7 · output 3 · total 10 (1 call)');
});

test('the archived conversation is in the total, not silently dropped', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  streams.find((s) => s.url.includes('sess-9'))
    .emit({ type: 'usage', data: { model: 'archived-model', input_tokens: 40, output_tokens: 2 } });
  const figures = figuresByModel(app);
  assert.ok(figures['archived-model'], 'nothing from the archived conversation reached the rows');
  assert.match(app.el('usage-scope').textContent, /every conversation/);
  assert.match(app.el('usage-note').textContent, /archived ones included/);
});

test('each bar says what it holds: input and output as separate segments', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  streams[0].emit({ type: 'usage', data: { model: 'model-a', input_tokens: 100, output_tokens: 100 } });
  streams[0].emit({ type: 'usage', data: { model: 'model-b', input_tokens: 25, output_tokens: 25 } });
  const [first, second] = rows(app);
  const segWidth = (row, cls) => row.querySelector(cls).style.width;
  // Scaled to the largest model's combined total (200): the smaller
  // model's 25-input segment is an eighth of the track, not a half of its
  // own row — and input never merges into output.
  assert.equal(segWidth(first, '.usage-input'), '50%');
  assert.equal(segWidth(first, '.usage-output'), '50%');
  assert.equal(segWidth(second, '.usage-input'), '12.5%');
  assert.equal(segWidth(second, '.usage-output'), '12.5%');
  // The figures beside the bar name both halves, so the bar is never the
  // only place the split is stated.
  assert.match(figuresByModel(app)['model-a'], /input 100 · output 100 · total 200/);
});

test('a usage report naming no model is left out', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  streams[0].emit({ type: 'usage', data: { input_tokens: 999, output_tokens: 999 } });
  assert.deepEqual(rows(app), [], 'a model-less report drew a row it cannot belong to');
  assert.match(app.el('usage-rows').textContent, /No usage recorded yet/);
});

test('a compaction call counts under the model that made it', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  streams[0].emit({ type: 'compacted', data: { model: 'model-a', input_tokens: 60, output_tokens: 6 } });
  // A compaction marker without a model bills nobody in particular, so it
  // counts nowhere — the daemon only adds one that names its model.
  streams[0].emit({ type: 'compacted', data: { summary_length: 12 } });
  const figures = figuresByModel(app);
  assert.equal(figures['model-a'], 'input 60 · output 6 · total 66 (1 call)');
  assert.equal(Object.keys(figures).length, 1);
});

test('cleared and rewound markers add nothing to any model', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  streams[0].emit({ type: 'cleared', data: {} });
  streams[0].emit({ type: 'rewound', data: { prompt: 'try again' } });
  assert.deepEqual(rows(app), []);
});

test('huge totals never touch the status line or its warning thresholds', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  streams[0].emit({ type: 'usage', data: { model: 'model-a', input_tokens: 9000000, output_tokens: 9000000 } });
  const bar = app.el('prompt-status');
  assert.ok(!bar.classList.contains('ctx-warn') && !bar.classList.contains('ctx-crit'),
    'a token total tripped the context-window warning styling');
  assert.ok(!app.el('status-text').textContent.includes('9000000'),
    'a token total leaked into the status line beside the context percentage');
});

test('a conversation that cannot be read is named, not silently dropped', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  streams.find((s) => s.url.includes('sess-2')).failFatally();
  await app.settle();
  assert.match(app.el('usage-note').textContent, /could not be read and .* not in this total/);
});

test('closing the window closes every stream it opened', async () => {
  const app = await withSessions();
  const streams = await openUsage(app);
  assert.ok(streams.length > 0);
  app.el('usage-close').fire('click');
  assert.ok(streams.every((s) => s.closed), 'a viewing stream kept running after its window closed');
  assert.equal(app.internals.usageView.isOpen, false);
});
