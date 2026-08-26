'use strict';

// The Smart Agent switch in the settings window.
//
// One checkbox, and the whole of what it has to get right is that the
// daemon is the one holding the setting: the box reflects what the daemon
// says, a click asks the daemon to change it, and a refused change puts
// the box back rather than leaving it claiming something that is not true.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

async function openSettings(app) {
  app.el('settings-btn').click();
  await app.settle();
}

// The default the feature rests on. With it on, one request can become
// several model calls against several contexts, which is a bill nobody
// agreed to by installing an update.
test('the switch is off, and says what that means', async () => {
  const app = await load();
  await openSettings(app);

  assert.equal(app.el('smart-agent-checkbox').checked, false);
  assert.match(app.el('smart-agent-note').textContent, /^Off\./);
  assert.equal(app.callsTo('POST', '/api/settings/smart-agent').length, 0,
    'opening the panel changed a setting');
});

test('a daemon with it already on opens with the box ticked', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': {
        auto_compact_enabled: true, show_tps: true, auto_delegate: false,
        auto_delegate_agent: '', auto_delegate_match: [],
        smart_agent: true, smart_agent_roster: ['explore', 'oracle'],
        skip_permissions: false, permission_rules: {}, can_edit_permissions: true,
      },
    },
  });
  await openSettings(app);

  assert.equal(app.el('smart-agent-checkbox').checked, true);
  // The roster is the part the page cannot know for itself: it depends on
  // the daemon's build and on which profiles the config has.
  assert.match(app.el('smart-agent-note').textContent, /explore, oracle/);
});

test('ticking it tells the daemon', async () => {
  const app = await load({ routes: { 'POST /api/settings/smart-agent': { status: 204 } } });
  await openSettings(app);

  app.el('smart-agent-checkbox').checked = true;
  app.el('smart-agent-checkbox').fire('change');
  await app.settle();

  const calls = app.callsTo('POST', '/api/settings/smart-agent');
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].body, { enabled: true });
  assert.match(app.el('smart-agent-note').textContent, /^On\./);
});

test('unticking it tells the daemon too', async () => {
  const app = await load({
    routes: {
      'GET /api/settings': {
        auto_compact_enabled: true, show_tps: true, auto_delegate: false,
        auto_delegate_agent: '', auto_delegate_match: [],
        smart_agent: true, smart_agent_roster: ['explore'],
        skip_permissions: false, permission_rules: {}, can_edit_permissions: true,
      },
      'POST /api/settings/smart-agent': { status: 204 },
    },
  });
  await openSettings(app);

  app.el('smart-agent-checkbox').checked = false;
  app.el('smart-agent-checkbox').fire('change');
  await app.settle();

  assert.deepEqual(app.callsTo('POST', '/api/settings/smart-agent')[0].body, { enabled: false });
  assert.match(app.el('smart-agent-note').textContent, /^Off\./);
});

// The daemon is the one that decides. A box left ticked after a refused
// request says the opposite of what is true, and the next thing the user
// does is act on it.
test('a refused change puts the box back and says why', async () => {
  const app = await load({
    routes: { 'POST /api/settings/smart-agent': { status: 500, body: 'disk is full' } },
  });
  await openSettings(app);

  app.el('smart-agent-checkbox').checked = true;
  app.el('smart-agent-checkbox').fire('change');
  await app.settle();

  assert.equal(app.el('smart-agent-checkbox').checked, false);
  assert.match(app.el('smart-agent-note').textContent, /Not changed/);
});

// "/config smart_agent on" typed in the TUI, or a second browser. The
// daemon broadcasts the change; a panel that is open has to follow it.
test('the switch follows a change made somewhere else', async () => {
  const app = await load();
  await openSettings(app);
  assert.equal(app.el('smart-agent-checkbox').checked, false);

  app.sse.emit({ type: 'config.changed', data: { smart_agent: true } });
  await app.settle();

  assert.equal(app.el('smart-agent-checkbox').checked, true);
});
