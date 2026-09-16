'use strict';

// The agent dropdown must name what the daemon serves now, not what it
// served when the page loaded.
//
// The roster moves: an update handoff or a restart under an open window can
// change which agents exist and which models their labels name, and turning
// Smart Agent on or off adds or removes the six specialists. The page used
// to fetch /api/agents once at startup and never again, so the dropdown
// could offer an agent the daemon now refuses, or omit one it would accept.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const V1 = [
  { name: 'general-purpose', description: 'the default agent', model: 'test-model-1' },
  { name: 'plan', description: 'read-only planner', model: 'test-model-2' },
];
const V2 = [
  { name: 'general-purpose', description: 'the default agent', model: 'test-model-9' },
  { name: 'explore', description: 'a smart specialist', model: 'test-model-3' },
];

function dropdownLabels(app) {
  return Array.from(
    app.el('agent-select').querySelectorAll('option'),
    (o) => o.textContent,
  );
}

function agentCalls(app) {
  return app.callsTo('GET', '/api/agents').length;
}

test('after the stream reconnects, the dropdown offers what the daemon now offers', async () => {
  let roster = V1;
  const app = await load({ routes: { 'GET /api/agents': () => roster } });
  assert.deepEqual(dropdownLabels(app), ['general-purpose (test-model-1)', 'plan (test-model-2)']);
  const atLoad = agentCalls(app);

  // The daemon under the window is replaced: new agents, new models.
  roster = V2;
  app.sse.fail();
  await app.settle();
  app.sse.reopen();
  await app.settle();

  assert.equal(agentCalls(app), atLoad + 1, 'a reconnect must refetch the roster rather than trust the old one');
  assert.deepEqual(
    dropdownLabels(app),
    ['general-purpose (test-model-9)', 'explore (test-model-3)'],
    'the dropdown still names an agent the daemon no longer serves, under a model it no longer runs',
  );
});

test('a Smart Agent flip announced by the daemon reloads the dropdown', async () => {
  let roster = V1;
  const app = await load({ routes: { 'GET /api/agents': () => roster } });
  const atLoad = agentCalls(app);

  // Smart Agent turned on somewhere else: the specialists now exist.
  roster = V2;
  app.sse.emit({ type: 'settings.changed', data: { smart_agent: true } });
  await app.settle();

  assert.equal(agentCalls(app), atLoad + 1, 'a roster-changing switch must refetch the roster');
  assert.deepEqual(
    dropdownLabels(app),
    ['general-purpose (test-model-9)', 'explore (test-model-3)'],
    'the dropdown omits an agent the daemon would now accept',
  );
});

test('the same switch announced without a flip refetches nothing', async () => {
  const app = await load();
  const atLoad = agentCalls(app);

  // Every settings.changed snapshot carries smart_agent, flipped or not.
  app.sse.emit({ type: 'settings.changed', data: { smart_agent: false } });
  await app.settle();

  assert.equal(agentCalls(app), atLoad, 'an unchanged switch must not cost a roster fetch');
});

test('a flip arriving on the session-scoped channel reloads the dropdown too', async () => {
  let roster = V1;
  const app = await load({ routes: { 'GET /api/agents': () => roster } });
  const atLoad = agentCalls(app);

  // "/config smart_agent on" typed at a prompt reaches this window as
  // config.changed rather than settings.changed.
  roster = V2;
  app.sse.emit({ type: 'config.changed', data: { smart_agent: true } });
  await app.settle();

  assert.equal(agentCalls(app), atLoad + 1);
  assert.deepEqual(
    dropdownLabels(app),
    ['general-purpose (test-model-9)', 'explore (test-model-3)'],
  );
});
