'use strict';

// A switch is a fact about the daemon, not about a conversation. It did
// not behave like one: a toggle typed at a prompt wrote a session-scoped
// event that only reached clients looking at that same session, and the
// settings window wrote nothing at all, so the window that changed a
// switch was the only thing that knew it had.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// The four permission switches are per conversation, so they arrive on
// that conversation's own stream rather than on the daemon-wide one: a
// window showing session A must not repaint because session B answered a
// prompt. The event carries the whole snapshot for the same reason
// settings.changed does.
test('a /permission-skip-all typed in this conversation moves the pill', async () => {
  const app = await load();
  assert.equal(app.el('permission-status-btn').classList.contains('skip'), false,
    'nothing is skipped to start with');

  app.applyEvent({
    type: 'permissions.changed',
    data: {
      effective: { skip_all: true, skip_tools: false, read_outside: false, write_outside: false },
      source: { skip_all: 'session' },
      remembered: { read: [], write: [] },
    },
  });

  assert.ok(app.el('permission-status-btn').classList.contains('skip'),
    'the pill is the one place the page says permissions are being skipped');
});

// An "allow anywhere outside" answered at the prompt turns the switch on,
// which is the point of routing that answer through the session's own
// setting: it is visible afterwards instead of being a private grant.
test('answering a boundary prompt with "anywhere" shows up as the switch', async () => {
  const app = await load();
  app.applyEvent({
    type: 'permissions.changed',
    data: {
      effective: { skip_all: false, skip_tools: false, read_outside: true, write_outside: false },
      source: { read_outside: 'session' },
      remembered: { read: [], write: [] },
    },
  });
  assert.equal(app.state.sessionPermissions.read_outside, true);
  assert.equal(app.state.sessionPermissions.write_outside, false,
    'allowing reads out there says nothing about writes');
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

  app.applyEvent({ type: 'permissions.changed', data: { effective: { skip_all: true } } });
  assert.ok(app.el('permission-status-btn').classList.contains('skip'));

  app.applyEvent({ type: 'permissions.changed', data: { effective: { skip_all: false } } });
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
