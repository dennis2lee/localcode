'use strict';

// The "Commands the model may run" switch in the settings window.
//
// The third switch on the Agents tab and the only one that had no test of
// its own. What it has to get right is the same as the other two: the box
// shows what the daemon says, a refused change puts it back, and the note
// beside it says what turning it on reaches, because the switch alone
// names nothing and the list lives in config.json and in each command's
// own frontmatter.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const SETTINGS = {
  auto_compact_enabled: true, show_tps: true, auto_delegate: false,
  auto_delegate_agent: '', auto_delegate_match: [],
  smart_agent: false, orchestrate_roster: ['explore', 'oracle'],
  orchestrate: false, model_invocable: false, model_commands: [],
  skip_permissions: false, permission_rules: {}, can_edit_permissions: true,
};

async function openSettings(app) {
  app.el('settings-btn').click();
  await app.settle();
}

test('off, the note says commands run only when typed, and nothing changed', async () => {
  const app = await load({ routes: { 'GET /api/settings': SETTINGS } });
  await openSettings(app);

  assert.equal(app.el('model-invocable-checkbox').checked, false);
  assert.match(app.el('model-invocable-note').textContent, /^Off\. Commands run only when you type them\.$/);
  assert.equal(app.callsTo('POST', '/api/settings/model-invocable').length, 0,
    'opening the panel changed a setting');
});

// On with nothing opted in is inert: the Command tool is not offered with
// an empty list. The note says where an opt-in is written.
test('on with nothing opted in, the note says where to opt a command in', async () => {
  const app = await load({
    routes: { 'GET /api/settings': { ...SETTINGS, model_invocable: true, model_commands: [] } },
  });
  await openSettings(app);

  assert.equal(app.el('model-invocable-checkbox').checked, true);
  const note = app.el('model-invocable-note').textContent;
  assert.match(note, /^On, but nothing is opted in\./);
  assert.match(note, /"model_commands" in config\.json/);
  assert.match(note, /"model_invocable: true"/);
});

test('on, the note lists what the model may run and that anything it reads can reach it', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': { ...SETTINGS, model_invocable: true, model_commands: ['/compact', '/usage'] },
    },
  });
  await openSettings(app);

  const note = app.el('model-invocable-note').textContent;
  assert.match(note, /^On\./);
  assert.match(note, /May run: \/compact, \/usage\./);
  assert.match(note, /Reachable from anything the model reads\./);
});

test('ticking it tells the daemon', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': { ...SETTINGS, model_commands: ['/compact'] },
      'POST /api/settings/model-invocable': { model_invocable: true, applied: true, persisted: true },
    },
  });
  await openSettings(app);

  app.el('model-invocable-checkbox').checked = true;
  app.el('model-invocable-checkbox').fire('change');
  await app.settle();

  const calls = app.callsTo('POST', '/api/settings/model-invocable');
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].body, { enabled: true });
  assert.equal(app.el('model-invocable-checkbox').checked, true);
  assert.match(app.el('model-invocable-note').textContent, /May run: \/compact\./);
});

// Applied and saved are different questions. A change that reached the
// running daemon but not config.json is still a change: the box keeps
// showing it, with the warning beside it.
test('applied but not saved keeps the box and warns', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': SETTINGS,
      'POST /api/settings/model-invocable': {
        model_invocable: true, applied: true, persisted: false, error: 'permission denied',
      },
    },
  });
  await openSettings(app);

  app.el('model-invocable-checkbox').checked = true;
  app.el('model-invocable-checkbox').fire('change');
  await app.settle();

  assert.equal(app.el('model-invocable-checkbox').checked, true,
    'an unsaved change was shown as a refused one, so the box now denies the state the daemon is in');
  assert.match(app.el('model-invocable-warn').textContent, /not saved to config\.json/);
  assert.equal(app.el('model-invocable-warn').hidden, false);
});

// Nothing was applied. Put the box back.
test('a refused change puts the box back and says why', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': SETTINGS,
      'POST /api/settings/model-invocable': { status: 500, body: 'nope' },
    },
  });
  await openSettings(app);

  app.el('model-invocable-checkbox').checked = true;
  app.el('model-invocable-checkbox').fire('change');
  await app.settle();

  assert.equal(app.el('model-invocable-checkbox').checked, false);
  assert.match(app.el('model-invocable-note').textContent, /^Not changed:/);
});
