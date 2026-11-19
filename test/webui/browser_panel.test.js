'use strict';

// The browser panel in the right panel.
//
// Drawn only in the desktop window and only while the setting is on, and
// its frame stream is what tells the daemon a GUI window is showing the
// conversation. So the things to get right are when the stream is open
// and for which conversation, and that the person's clicks and keys reach
// the page in its own pixels.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const settingsWith = (extra) => ({
  'GET /api/settings': {
    auto_compact_enabled: true, show_tps: true, auto_delegate: false,
    auto_delegate_agent: '', auto_delegate_match: [], smart_agent: false,
    skip_permissions: false, permission_rules: {}, can_edit_permissions: true,
    ...extra,
  },
  'GET /api/sessions/*/browser': { has_tab: false, width: 1280, height: 800 },
});

test('outside the desktop window there is no panel, even with the setting on', async () => {
  const app = await load({ routes: settingsWith({ browser: true }) });
  assert.equal(app.el('browser-section').hidden, true);
  assert.equal(app.el('browser-view').getAttribute('src'), null,
    'a stream opened outside the GUI would offer the model a browser nobody can see');
});

test('in the desktop window with the setting off there is no panel', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: false }) });
  assert.equal(app.el('browser-section').hidden, true);
  assert.equal(app.el('browser-view').getAttribute('src'), null);
});

test('in the desktop window with it on, the panel streams the conversation on screen', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  assert.equal(app.el('browser-section').hidden, false);
  assert.equal(app.el('browser-view').getAttribute('src'), '/api/sessions/sess-1/browser/stream');
});

test('turning the setting off elsewhere closes the stream', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  app.internals.applyEvent({ type: 'settings.changed', data: { browser: false } });
  await app.settle();
  assert.equal(app.el('browser-section').hidden, true);
  assert.equal(app.el('browser-view').getAttribute('src'), null,
    'the stream stayed open, so the daemon would go on offering the browser');
});

test('opening another conversation points the stream at it', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(app.el('browser-view').getAttribute('src'), '/api/sessions/sess-2/browser/stream');
});

test('the address bar opens what is typed into it', async () => {
  const app = await load({
    globals: { lcGUI: true },
    routes: { ...settingsWith({ browser: true }), 'POST /api/sessions/*/browser/navigate': { url: 'https://example.com/', title: 'Example' } },
  });
  const url = app.el('browser-url');
  url.value = 'example.com';
  url.fire('keydown', { key: 'Enter' });
  await app.settle();
  const calls = app.callsTo('POST', '/api/sessions/sess-1/browser/navigate');
  assert.equal(calls.length, 1);
  assert.deepEqual({ ...calls[0].body }, { url: 'example.com' });
  assert.equal(url.value, 'https://example.com/');
});

test('a click on the view lands on the page in its own pixels', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const view = app.el('browser-view');
  view.clientWidth = 640;
  view.clientHeight = 400;
  view.fire('mousedown', { offsetX: 320, offsetY: 100, button: 0, detail: 1 });
  view.fire('mouseup', { offsetX: 320, offsetY: 100, button: 0, detail: 1 });
  await app.settle();
  const sent = app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body.mouse);
  assert.equal(sent.length, 2);
  assert.deepEqual([sent[0].type, sent[0].x, sent[0].y, sent[0].button], ['mousePressed', 640, 200, 'left']);
  assert.equal(sent[1].type, 'mouseReleased');
});

test('pagePoint scales to the page and keeps a point on it', async () => {
  const app = await load({ init: false });
  const { pagePoint } = app.internals;
  assert.deepEqual({ ...pagePoint(320, 200, 640, 400) }, { x: 640, y: 400 });
  assert.deepEqual({ ...pagePoint(-5, 900, 640, 400) }, { x: 0, y: 800 });
});

test('a key types its character, and a shortcut does not', async () => {
  const app = await load({ init: false });
  const { keyEventFor } = app.internals;
  const a = keyEventFor({ key: 'a', code: 'KeyA', keyCode: 65 }, true);
  assert.deepEqual([a.type, a.text, a.modifiers], ['keyDown', 'a', 0]);
  const ctrlA = keyEventFor({ key: 'a', code: 'KeyA', keyCode: 65, ctrlKey: true }, true);
  assert.deepEqual([ctrlA.type, ctrlA.text, ctrlA.modifiers], ['rawKeyDown', '', 2]);
  const enter = keyEventFor({ key: 'Enter', code: 'Enter', keyCode: 13 }, true);
  assert.deepEqual([enter.type, enter.text], ['keyDown', '\r']);
  const up = keyEventFor({ key: 'a', code: 'KeyA', keyCode: 65 }, false);
  assert.deepEqual([up.type, up.text], ['keyUp', '']);
});

test('the settings switch is in the desktop window only, and tells the daemon', async () => {
  const outside = await load({ routes: settingsWith({ browser: false }) });
  outside.el('settings-btn').click();
  await outside.settle();
  assert.equal(outside.el('browser-settings').hidden, true);

  const app = await load({
    globals: { lcGUI: true },
    routes: { ...settingsWith({ browser: false }), 'POST /api/settings/browser': { browser: true, applied: true, persisted: true } },
  });
  app.el('settings-btn').click();
  await app.settle();
  assert.equal(app.el('browser-settings').hidden, false);
  app.el('browser-checkbox').checked = true;
  app.el('browser-checkbox').fire('change');
  await app.settle();
  const calls = app.callsTo('POST', '/api/settings/browser');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].body.enabled, true);
  assert.equal(app.el('browser-section').hidden, false, 'turning it on did not draw the panel');
});
