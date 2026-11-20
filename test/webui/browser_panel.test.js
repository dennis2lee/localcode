'use strict';

// The browser panel, in its own column between the conversation and the
// tasks panel.
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
  assert.equal(app.el('browser-panel').hidden, true);
  assert.equal(app.el('browser-view').getAttribute('src'), null,
    'a stream opened outside the GUI would offer the model a browser nobody can see');
});

test('in the desktop window with the setting off there is no panel', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: false }) });
  assert.equal(app.el('browser-panel').hidden, true);
  assert.equal(app.el('browser-view').getAttribute('src'), null);
});

test('in the desktop window with it on, the panel streams the conversation on screen', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  assert.equal(app.el('browser-panel').hidden, false);
  assert.equal(app.el('resize-browser').hidden, false, 'the panel is shown without its drag handle');
  assert.equal(app.el('browser-view').getAttribute('src'), '/api/sessions/sess-1/browser/stream');
});

test('turning the setting off elsewhere closes the stream', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  app.internals.applyEvent({ type: 'settings.changed', data: { browser: false } });
  await app.settle();
  assert.equal(app.el('browser-panel').hidden, true);
  assert.equal(app.el('resize-browser').hidden, true, 'a handle for a panel that is not there is a dead strip');
  assert.equal(app.el('browser-view').getAttribute('src'), null,
    'the stream stayed open, so the daemon would go on offering the browser');
});

test('opening another conversation points the stream at it', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(app.el('browser-view').getAttribute('src'), '/api/sessions/sess-2/browser/stream');
});

// Every conversation has its own tab, so each shows its own page. The
// stream of a conversation with no tab sends nothing, and an <img> whose
// source changes goes on showing its last picture until the new source
// sends one: without the stale state, the conversation opened next shows
// the page of the one before.
const perConversation = (tabs) => ({
  ...settingsWith({ browser: true }),
  'GET /api/sessions/*/browser': (_body, { path }) => {
    const id = path.split('/')[3];
    const url = tabs[id];
    return url ? { has_tab: true, url, title: id } : { has_tab: false, width: 1280, height: 800 };
  },
});

test('each conversation shows its own page, never the one before', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: perConversation({ 'sess-1': 'https://one.example/' }) });
  const view = app.el('browser-view');
  view.fire('load');
  await app.settle();
  assert.equal(view.classList.contains('stale'), false);
  assert.equal(app.el('browser-url').value, 'https://one.example/');

  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(view.getAttribute('src'), '/api/sessions/sess-2/browser/stream');
  assert.equal(view.classList.contains('stale'), true,
    'the view went on showing the first conversation\'s page in the second');
  assert.equal(app.el('browser-url').value, '', 'the address bar kept the first conversation\'s address');
  assert.equal(app.el('browser-empty').hidden, false, 'the second conversation has no page, and the panel did not say so');

  // Its own first frame shows it.
  view.fire('load');
  assert.equal(view.classList.contains('stale'), false);
});

test('a conversation with a page shows it from its first frame, not from the location poll', async () => {
  let now = 1_000_000;
  class ClockDate extends Date { static now() { return now; } }
  const app = await load({
    globals: { lcGUI: true, Date: ClockDate },
    routes: perConversation({ 'sess-1': 'https://one.example/', 'sess-2': 'https://two.example/' }),
  });
  const view = app.el('browser-view');
  view.fire('load');
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(app.el('browser-url').value, 'https://two.example/');
  assert.equal(view.classList.contains('stale'), true,
    'the poll that found the tab showed the view before its first frame, so the page before was on screen');

  // An engine that fires no load event for a part of a stream still shows
  // it, from a poll a while after the switch.
  now += app.internals.STALE_FALLBACK_MS;
  app.internals.renderBrowserPanel();
  await app.settle();
  assert.equal(view.classList.contains('stale'), false);
});

test('an address bar answer for one conversation does not land in another', async () => {
  let release;
  const gate = new Promise((r) => { release = r; });
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      ...perConversation({}),
      'POST /api/sessions/*/browser/navigate': () => gate.then(() => ({ url: 'https://one.example/' })),
    },
  });
  const url = app.el('browser-url');
  url.value = 'one.example';
  url.fire('keydown', { key: 'Enter' });
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  release();
  await app.settle();
  assert.equal(url.value, '', 'the first conversation\'s address was written into the second one\'s bar');
  assert.equal(app.el('browser-empty').hidden, false);
});

test('a failed open in one conversation is not reported in the next', async () => {
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      ...perConversation({}),
      'POST /api/sessions/*/browser/navigate': { status: 503, body: { error: 'no browser' } },
    },
  });
  const empty = app.el('browser-empty');
  const words = 'No page yet.';
  empty.textContent = words;
  const url = app.el('browser-url');
  url.value = 'one.example';
  url.fire('keydown', { key: 'Enter' });
  await app.settle();
  assert.match(empty.textContent, /^Could not open it/);

  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(empty.textContent, words, 'the first conversation\'s error was shown in the second');
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
  view.fire('mousedown', { offsetX: 320, offsetY: 100, button: 0, buttons: 1, detail: 1 });
  // The release reaches the window, wherever the pointer is by then.
  app.fireWindow('mouseup', { offsetX: 320, offsetY: 100, button: 0, buttons: 0, detail: 1 });
  await app.settle();
  const sent = app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body.mouse);
  assert.equal(sent.length, 2);
  assert.deepEqual([sent[0].type, sent[0].x, sent[0].y, sent[0].button], ['mousePressed', 640, 200, 'left']);
  assert.equal(sent[1].type, 'mouseReleased');
});

test('a drag tells the page the button is held, and ends where the pointer left the view', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const view = app.el('browser-view');
  view.clientWidth = 640;
  view.clientHeight = 400;
  view.clientLeft = 1;
  view.clientTop = 1;
  view.getBoundingClientRect = () => ({ left: 100, top: 50, width: 642, height: 402 });
  view.fire('mousedown', { clientX: 101, clientY: 51, button: 0, buttons: 1, detail: 1 });
  app.fireWindow('mousemove', { clientX: 421, clientY: 251, buttons: 1 });
  // Released outside the view, to its right.
  app.fireWindow('mouseup', { clientX: 2000, clientY: 251, button: 0, buttons: 0, detail: 1 });
  await app.settle();
  const sent = app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body.mouse);
  assert.deepEqual(sent.map((m) => m.type), ['mousePressed', 'mouseMoved', 'mouseReleased']);
  assert.deepEqual([sent[0].x, sent[0].y], [0, 0]);
  assert.deepEqual([sent[1].x, sent[1].y, sent[1].buttons, sent[1].button], [640, 400, 1, 'left'],
    'a move with the button held is not a drag to the page without the buttons');
  assert.deepEqual([sent[2].x, sent[2].y], [1280, 400], 'the release did not land on the page edge');

  // Released, the window stops following the pointer.
  app.fireWindow('mousemove', { clientX: 300, clientY: 200, buttons: 0 });
  app.fireWindow('mouseup', { clientX: 300, clientY: 200, button: 0, buttons: 0 });
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/browser/input').length, 3);
});

test('input reaches the daemon one request at a time, in order', async () => {
  let release;
  const gate = new Promise((r) => { release = r; });
  const app = await load({
    globals: { lcGUI: true },
    routes: { ...settingsWith({ browser: true }), 'POST /api/sessions/*/browser/input': () => gate.then(() => ({})) },
  });
  const view = app.el('browser-view');
  view.clientWidth = 640;
  view.clientHeight = 400;
  view.fire('mousedown', { offsetX: 10, offsetY: 10, button: 0, buttons: 1, detail: 1 });
  app.fireWindow('mouseup', { offsetX: 10, offsetY: 10, button: 0, buttons: 0, detail: 1 });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/browser/input').length, 1,
    'the release went out before the press was answered, so it could overtake it');
  release();
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/browser/input').length, 2);
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

test('a character the layout composed types, without the keys that composed it', async () => {
  const app = await load({ init: false });
  const { keyEventFor } = app.internals;
  // Option+E on a Mac, AltGr+Q (Ctrl+Alt) on a German Windows layout.
  const option = keyEventFor({ key: '\u00b4', code: 'KeyE', keyCode: 69, altKey: true }, true, true);
  assert.deepEqual([option.type, option.text, option.modifiers], ['keyDown', '\u00b4', 0]);
  const altGr = keyEventFor({ key: '@', code: 'KeyQ', keyCode: 81, altKey: true, ctrlKey: true }, true, false);
  assert.deepEqual([altGr.type, altGr.text, altGr.modifiers], ['keyDown', '@', 0]);
  // Alt alone on Windows is a menu key, not a character.
  const alt = keyEventFor({ key: 'f', code: 'KeyF', keyCode: 70, altKey: true }, true, false);
  assert.deepEqual([alt.type, alt.text], ['rawKeyDown', '']);
  // Shift stays: it is part of the character.
  const shifted = keyEventFor({ key: 'A', code: 'KeyA', keyCode: 65, shiftKey: true }, true, false);
  assert.deepEqual([shifted.text, shifted.modifiers], ['A', 8]);
});

test('Ctrl+Enter and Cmd+Enter are key presses, not a line break', async () => {
  const app = await load({ init: false });
  const { keyEventFor } = app.internals;
  for (const mods of [{ ctrlKey: true }, { metaKey: true }]) {
    const ev = keyEventFor({ key: 'Enter', code: 'Enter', keyCode: 13, ...mods }, true);
    assert.deepEqual([ev.type, ev.text], ['rawKeyDown', ''], JSON.stringify(mods));
  }
  const shiftEnter = keyEventFor({ key: 'Enter', code: 'Enter', keyCode: 13, shiftKey: true }, true);
  assert.equal(shiftEnter.text, '\r');
});

test('a shortcut carries the editing command, by the platform\'s own modifier', async () => {
  const app = await load({ init: false });
  const { keyEventFor, editCommandFor } = app.internals;
  const key = (k, mods) => ({ key: k, code: `Key${k.toUpperCase()}`, keyCode: k.toUpperCase().charCodeAt(0), ...mods });
  const mac = true;
  assert.deepEqual([...keyEventFor(key('v', { metaKey: true }), true, mac).commands], ['paste']);
  assert.deepEqual([...keyEventFor(key('a', { metaKey: true }), true, mac).commands], ['selectAll']);
  assert.deepEqual([...keyEventFor(key('z', { metaKey: true, shiftKey: true }), true, mac).commands], ['redo']);
  assert.equal(editCommandFor(key('a', { ctrlKey: true }), mac), '',
    'Ctrl+A on a Mac is the start of the line, not select all');
  assert.deepEqual([...keyEventFor(key('c', { ctrlKey: true }), true, false).commands], ['copy']);
  assert.deepEqual([...keyEventFor(key('y', { ctrlKey: true }), true, false).commands], ['redo']);
  assert.equal(editCommandFor(key('v', { metaKey: true }), false), '');
  assert.equal(keyEventFor(key('v', { ctrlKey: true }), false, false).commands, undefined,
    'a key coming up carries the command a second time');
});

test('a paste shortcut pastes once, from the page\'s side', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const view = app.el('browser-view');
  const down = view.fire('keydown', { key: 'v', code: 'KeyV', keyCode: 86, ctrlKey: true });
  assert.equal(down.defaultPrevented, true);
  // The Edit menu's paste arriving behind it is the same paste.
  view.fire('paste', { clipboardData: { getData: () => 'copied' } });
  await app.settle();
  const sent = app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body);
  assert.equal(sent.length, 1, JSON.stringify(sent));
  assert.deepEqual([...sent[0].key.commands], ['paste']);

  // A paste from the menu alone sends the text.
  const app2 = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  app2.el('browser-view').fire('paste', { clipboardData: { getData: () => 'copied' } });
  await app2.settle();
  const sent2 = app2.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body);
  assert.deepEqual(sent2.map((b) => b.text), ['copied']);
});

test('a broken stream opens again after a pause', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const view = app.el('browser-view');
  view.fire('load');
  const realTimeout = setTimeout;
  view.fire('error');
  await new Promise((r) => realTimeout(r, 2100));
  assert.match(view.getAttribute('src'), /^\/api\/sessions\/sess-1\/browser\/stream\?r=\d+$/);
  assert.equal(view.classList.contains('stale'), false,
    'the same conversation\'s stream opened again is not another conversation\'s page');
});

test('a snapshot the tool saved shows under its call, now and on replay', async () => {
  const app = await load();
  const end = { type: 'tool.end', data: { tool_use_id: 'b1', content: 'Saved .localcode/snapshots/20261119-101500-001.png', is_error: false, snapshot: '/w/.localcode/snapshots/20261119-101500-001.png' } };
  app.applyEvent({ type: 'tool.start', data: { tool_use_id: 'b1', name: 'browser', input: '{"action":"screenshot"}' } });
  app.applyEvent(end);
  const rows = app.el('transcript').querySelectorAll('.msg-toolcall');
  const img = rows[rows.length - 1].querySelector('.tool-snapshot');
  assert.ok(img, 'no image under the call');
  assert.equal(img.getAttribute('src'), '/api/sessions/sess-1/snapshots/20261119-101500-001.png');

  // A call with no snapshot draws none.
  app.applyEvent({ type: 'tool.start', data: { tool_use_id: 'b2', name: 'browser', input: '{"action":"read"}' } });
  app.applyEvent({ type: 'tool.end', data: { tool_use_id: 'b2', content: 'URL: x', is_error: false } });
  const after = app.el('transcript').querySelectorAll('.msg-toolcall');
  assert.equal(after[after.length - 1].querySelector('.tool-snapshot'), null);
});

test('snapshotURL names the file only, whatever path it was saved under', async () => {
  const app = await load({ init: false });
  const { snapshotURL } = app.internals;
  assert.equal(snapshotURL('s 1', 'C:\\w\\.localcode\\snapshots\\20261119-101500-002.png'),
    '/api/sessions/s%201/snapshots/20261119-101500-002.png');
  assert.equal(snapshotURL('s1', '../../etc/passwd'), '/api/sessions/s1/snapshots/passwd');
});

test('the settings say what the last profile copy left behind', async () => {
  const app = await load({ init: false });
  const { importNote } = app.internals;
  assert.equal(importNote(null), '');
  assert.equal(importNote({ from: '/p/Default', skipped: [] }), 'Last copied from /p/Default.');
  assert.match(importNote({ from: '/p/Default', skipped: ['Cookies'] }), /held by the running browser: Cookies\. Close Chrome/);
});

test('settings warn when there is no browser to start', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: false, browser_available: false }) });
  app.el('settings-btn').click();
  await app.settle();
  assert.equal(app.el('browser-warn').hidden, false);
  assert.match(app.el('browser-warn').textContent, /No Chrome or Microsoft Edge/);
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
  assert.equal(app.el('browser-panel').hidden, false, 'turning it on did not draw the panel');
});
