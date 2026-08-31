'use strict';

// The Orchestration switch in the settings window.
//
// A second switch beside Smart Agent, and the reason there are two is the
// reason these tests exist: the two are different sizes. Smart Agent lets
// the model hand one question to a specialist; this lets it commit to a
// shape and spend up to thirty-two agent turns on it. So they have to move
// independently, and the panel has to be able to show one on and the other
// off without asking the daemon twice.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const SETTINGS = {
  auto_compact_enabled: true, show_tps: true, auto_delegate: false,
  auto_delegate_agent: '', auto_delegate_match: [],
  smart_agent: false, smart_agent_roster: ['explore', 'oracle'],
  orchestrate: false,
  skip_permissions: false, permission_rules: {}, can_edit_permissions: true,
};

async function openSettings(app) {
  app.el('settings-btn').click();
  await app.settle();
}

test('the switch is off, and says what that means', async () => {
  const app = await load({ routes: { 'GET /api/settings': SETTINGS } });
  await openSettings(app);

  assert.equal(app.el('orchestrate-checkbox').checked, false);
  assert.match(app.el('orchestrate-note').textContent, /^Off\./);
  assert.equal(app.callsTo('POST', '/api/settings/orchestrate').length, 0,
    'opening the panel changed a setting');
});

// On, and the note carries the ceilings. They are the part a person needs
// before agreeing to it: a run is the most expensive single thing
// localcode can be asked to do.
test('on, it names what a run can cost', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: true, orchestrate: true } },
  });
  await openSettings(app);

  assert.equal(app.el('orchestrate-checkbox').checked, true);
  const note = app.el('orchestrate-note').textContent;
  assert.match(note, /8 stages and 32 agent turns/);
  assert.match(note, /asks first/);
});

// A plan needs somewhere to delegate its stages to. Turning this on with
// Smart Agent off is legal and inert, and saying so beats letting it read
// as a change that took effect.
test('on with nobody to delegate to, it says so instead of pretending', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: false, orchestrate: true } },
  });
  await openSettings(app);

  assert.match(app.el('orchestrate-note').textContent, /nobody to delegate to/);
  assert.match(app.el('orchestrate-note').textContent, /Turn on Smart Agent/);
});

test('ticking it tells the daemon', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': SETTINGS,
      'POST /api/settings/orchestrate': { orchestrate: true, applied: true, persisted: true },
    },
  });
  await openSettings(app);

  app.el('orchestrate-checkbox').checked = true;
  app.el('orchestrate-checkbox').fire('change');
  await app.settle();

  const calls = app.callsTo('POST', '/api/settings/orchestrate');
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].body, { enabled: true });
  assert.equal(app.el('orchestrate-checkbox').checked, true);
});

// Applied and saved are different questions, and the daemon answers both.
// A change that reached the running daemon but not config.json is still a
// change: the box has to keep showing it, with the warning beside it.
test('applied but not saved keeps the box and warns', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': SETTINGS,
      'POST /api/settings/orchestrate': {
        orchestrate: true, applied: true, persisted: false, error: 'permission denied',
      },
    },
  });
  await openSettings(app);

  app.el('orchestrate-checkbox').checked = true;
  app.el('orchestrate-checkbox').fire('change');
  await app.settle();

  assert.equal(app.el('orchestrate-checkbox').checked, true,
    'an unsaved change was shown as a refused one, so the box now denies the state the daemon is in');
  assert.match(app.el('orchestrate-warn').textContent, /not saved to config\.json/);
  assert.equal(app.el('orchestrate-warn').hidden, false);
});

// Nothing was applied. Put the box back.
test('a refused change puts the box back', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': SETTINGS,
      'POST /api/settings/orchestrate': { status: 500, body: 'nope' },
    },
  });
  await openSettings(app);

  app.el('orchestrate-checkbox').checked = true;
  app.el('orchestrate-checkbox').fire('change');
  await app.settle();

  assert.equal(app.el('orchestrate-checkbox').checked, false);
  assert.match(app.el('orchestrate-note').textContent, /^Not changed:/);
});

// The two switches are independent, which is the whole argument for there
// being two of them.
test('the two switches are drawn from one payload and move apart', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, smart_agent: true, orchestrate: false } },
  });
  await openSettings(app);

  assert.equal(app.el('smart-agent-checkbox').checked, true);
  assert.equal(app.el('orchestrate-checkbox').checked, false);
});
