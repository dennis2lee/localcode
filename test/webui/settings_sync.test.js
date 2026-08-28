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

// The keep-going switch is one switch with two homes: the settings
// window's checkbox and "/keep-going" at any prompt. Flipped in either,
// the other has to hear.
test('a /keep-going typed anywhere moves the settings checkbox here', async () => {
  const app = await load();
  await app.settle();
  assert.equal(app.state.keepGoing, true, 'the nudge defaults to on');

  app.applyEvent({ type: 'settings.changed', data: { keep_going: false } });
  assert.equal(app.state.keepGoing, false, 'the switch did not move');

  // With the panel open, the box itself follows.
  app.el('settings-btn').click();
  await app.settle();
  app.applyEvent({ type: 'settings.changed', data: { keep_going: true } });
  assert.equal(app.el('keep-going-checkbox').checked, true, 'the open panel shows a stale box');
});

test('the checkbox posts the change to the daemon', async () => {
  const app = await load({ routes: { 'POST /api/settings/keep-going': { status: 204 } } });
  app.el('settings-btn').click();
  await app.settle();

  const box = app.el('keep-going-checkbox');
  box.checked = false;
  box.fire('change');
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/settings/keep-going').length, 1);
  assert.equal(app.state.keepGoing, false);
});
