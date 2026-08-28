'use strict';

// A switch is a fact about the daemon, not about a conversation. It did
// not behave like one: a toggle typed at a prompt wrote a session-scoped
// event that only reached clients looking at that same session, and the
// settings window wrote nothing at all, so the window that changed a
// switch was the only thing that knew it had.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('a /permission-skip-all typed anywhere moves the pill here', async () => {
  const app = await load();
  assert.equal(app.el('permission-status-btn').classList.contains('skip'), false,
    'nothing is skipped to start with');

  app.applyEvent({
    type: 'settings.changed',
    data: { skip_permissions: true, smart_agent: false, auto_delegate: false },
  });

  assert.ok(app.el('permission-status-btn').classList.contains('skip'),
    'the pill is the one place the page says permissions are being skipped');
});

test('the same event moves the Smart Agent and auto-delegate state', async () => {
  const app = await load();

  app.applyEvent({
    type: 'settings.changed',
    data: { skip_permissions: false, smart_agent: true, auto_delegate: true },
  });

  assert.equal(app.state.smartAgent, true);
  assert.equal(app.state.autoDelegate, true);
});

// The event carries every switch, so the page applies a snapshot rather
// than merging a sequence and cannot end up half-updated by one it
// missed.
test('a later event turns a switch back off', async () => {
  const app = await load();

  app.applyEvent({ type: 'settings.changed', data: { skip_permissions: true } });
  assert.ok(app.el('permission-status-btn').classList.contains('skip'));

  app.applyEvent({ type: 'settings.changed', data: { skip_permissions: false } });
  assert.equal(app.el('permission-status-btn').classList.contains('skip'), false,
    'turning it off has to reach the page too');
});
