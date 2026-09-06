'use strict';

// How hard the model is asked to think, in the page.
//
// The levels are not written into the page: which ones exist is a
// property of the model the conversation is on — Anthropic's newest
// families have one switch where muse has four steps — so the daemon
// sends the list with the answer and the picker draws whatever it is
// given. A page with its own list offers steps that do nothing on most
// models, and goes stale the next time a family is added.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const MUSE = {
  model: 'muse-glimmer-30b', agent: 'general-purpose', level: 'xhigh', source: 'session',
  levels: ['off', 'low', 'medium', 'high', 'xhigh'],
  note: 'Muse reads its reasoning strength from the system prompt.',
};
const CLAUDE = {
  model: 'claude-sonnet-5', agent: 'plan', level: '', source: 'unset',
  levels: ['off', 'high'],
  note: 'That family decides the amount itself, so this is on or off.',
};

async function withEffort(view = MUSE, extra = {}) {
  return load({
    routes: {
      'GET /api/sessions/*/effort': view,
      'POST /api/sessions/*/effort': (body) => ({ ...view, level: body.level, source: body.level ? 'session' : 'unset' }),
      ...extra,
    },
  });
}

test('the pill names the level in force, beside the model', async () => {
  const app = await withEffort();
  assert.equal(app.el('effort-btn').textContent, 'effort: xhigh');
});

test('a level nobody has chosen reads as the profile\'s', async () => {
  const app = await withEffort(CLAUDE);
  assert.equal(app.el('effort-btn').textContent, 'effort: default');
});

test('the picker offers the levels the daemon sent, and no others', async () => {
  const app = await withEffort(CLAUDE);
  app.el('effort-btn').fire('click');
  await app.wait(0);
  const levels = Array.from(app.el('effort-levels').querySelectorAll('button'), (b) => b.textContent);
  assert.deepEqual(levels, ['off', 'high'], 'the page drew a list of its own instead of the model\'s');
  assert.match(app.el('effort-note').textContent, /on or off/, 'the note about what the level reaches is missing');
});

test('choosing a level sends it and redraws', async () => {
  const app = await withEffort();
  app.el('effort-btn').fire('click');
  await app.wait(0);
  const off = Array.from(app.el('effort-levels').querySelectorAll('button')).find((b) => b.textContent === 'off');
  off.fire('click');
  await app.wait(0);
  const calls = app.callsTo('POST', /\/effort$/);
  assert.equal(calls.length, 1, 'the level was not sent');
  assert.equal(calls[0].body.level, 'off');
  assert.equal(app.el('effort-btn').textContent, 'effort: off');
});

// A second client changing it, or an agent switch changing the model:
// both arrive as the same event, and both have to redraw the list as
// well as the level.
test('a change announced by the daemon redraws the level and the levels', async () => {
  const app = await withEffort();
  app.sse.emit({ type: 'effort.changed', data: CLAUDE });
  await app.wait(0);
  assert.equal(app.el('effort-btn').textContent, 'effort: default');
  app.el('effort-btn').fire('click');
  await app.wait(0);
  const levels = Array.from(app.el('effort-levels').querySelectorAll('button'), (b) => b.textContent);
  assert.deepEqual(levels, ['off', 'high'], 'the picker kept the levels of the model it had left');
});
