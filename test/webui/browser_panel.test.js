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

// A conversation's id does not tell one visit of it from the next. An
// answer asked for in one visit, arriving after a switch away and back, is
// dropped like one arriving in another conversation.
for (const outcome of ['answer', 'error']) {
  test(`an address bar ${outcome} from an earlier visit is dropped after a switch away and back`, async () => {
    let finish;
    const pending = new Promise((r) => { finish = r; });
    const app = await load({
      globals: { lcGUI: true },
      routes: { ...perConversation({}), 'POST /api/sessions/*/browser/navigate': () => pending },
    });
    const url = app.el('browser-url');
    const empty = app.el('browser-empty');
    empty.textContent = 'No page yet.';
    url.value = 'old.example';
    url.fire('keydown', { key: 'Enter' });
    app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
    await app.settle();
    app.internals.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
    await app.settle();
    finish(outcome === 'answer' ? { url: 'https://old.example/' } : { status: 503, body: { error: 'old failure' } });
    await app.settle();
    assert.equal(url.value, '', 'the earlier visit\'s address was written into this one\'s bar');
    assert.equal(empty.textContent, 'No page yet.', 'the earlier visit\'s error was shown in this one');
  });
}

test('an address bar error from before the setting went off and on again is dropped', async () => {
  let finish;
  const pending = new Promise((r) => { finish = r; });
  const app = await load({
    globals: { lcGUI: true },
    routes: { ...perConversation({}), 'POST /api/sessions/*/browser/navigate': () => pending },
  });
  const empty = app.el('browser-empty');
  empty.textContent = 'No page yet.';
  app.el('browser-url').value = 'old.example';
  app.el('browser-url').fire('keydown', { key: 'Enter' });
  app.internals.applyEvent({ type: 'settings.changed', data: { browser: false } });
  await app.settle();
  app.internals.applyEvent({ type: 'settings.changed', data: { browser: true } });
  await app.settle();
  finish({ status: 503, body: { error: 'old failure' } });
  await app.settle();
  assert.equal(empty.textContent, 'No page yet.');
});

// The worst of them: a location poll from the first visit, answered late,
// says there is a page while the view still holds the frame of the
// conversation visited in between.
test('a location poll from an earlier visit cannot show the view', async () => {
  let now = 1_000_000;
  class ClockDate extends Date { static now() { return now; } }
  const tabs = { 'sess-2': 'https://two.example/' };
  const app = await load({ globals: { lcGUI: true, Date: ClockDate }, routes: perConversation(tabs) });
  let finish;
  const late = new Promise((r) => { finish = r; });
  app.routes['GET /api/sessions/sess-1/browser'] = () => late;
  app.internals.renderBrowserPanel();
  app.routes['GET /api/sessions/sess-1/browser'] = { has_tab: false, width: 1280, height: 800 };

  const view = app.el('browser-view');
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  view.fire('load');
  app.internals.selectSession('sess-1', 'general-purpose', '/tmp/workspace');
  await app.settle();
  assert.equal(view.classList.contains('stale'), true);

  now += app.internals.STALE_FALLBACK_MS;
  finish({ has_tab: true, url: 'https://one.example/' });
  await app.settle();
  assert.equal(view.classList.contains('stale'), true,
    'a poll from the first visit showed the view, which holds the second conversation\'s frame');
  assert.equal(app.el('browser-url').value, '');
});

test('a reconnect left from the conversation before does not take the next one\'s place', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: perConversation({}) });
  const view = app.el('browser-view');
  const realTimeout = setTimeout;
  view.fire('error');
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  view.fire('error');
  await new Promise((r) => realTimeout(r, 2100));
  assert.match(view.getAttribute('src'), /^\/api\/sessions\/sess-2\/browser\/stream\?r=\d+$/,
    'the second conversation\'s broken stream was never opened again');
});

// The fallback counts from when the tab was found, not from the switch: a
// conversation that gets its tab long after it was opened has sent no
// frame when the tab is first found, and the view still holds the frame
// of the conversation before.
test('a tab opened long after the switch is not shown before its first frame', async () => {
  let now = 1_000_000;
  class ClockDate extends Date { static now() { return now; } }
  const tabs = { 'sess-1': 'https://one.example/' };
  const app = await load({ globals: { lcGUI: true, Date: ClockDate }, routes: perConversation(tabs) });
  const view = app.el('browser-view');
  view.fire('load');
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  now += 10 * app.internals.STALE_FALLBACK_MS;
  tabs['sess-2'] = 'https://two.example/';
  app.internals.renderBrowserPanel();
  await app.settle();
  assert.equal(view.classList.contains('stale'), true,
    'the poll that first found the tab showed the view, which holds the first conversation\'s frame');

  now += app.internals.STALE_FALLBACK_MS;
  app.internals.renderBrowserPanel();
  await app.settle();
  assert.equal(view.classList.contains('stale'), false);
});

test('an address bar open that succeeds takes the empty note away', async () => {
  const app = await load({
    globals: { lcGUI: true },
    routes: { ...perConversation({}), 'POST /api/sessions/*/browser/navigate': { url: 'https://example.com/' } },
  });
  assert.equal(app.el('browser-empty').hidden, false);
  app.el('browser-url').value = 'example.com';
  app.el('browser-url').fire('keydown', { key: 'Enter' });
  await app.settle();
  assert.equal(app.el('browser-empty').hidden, true, '"No page yet" was shown over the page just opened');
});

// A tab can close under the panel without a switch (the browser restarts,
// the profile is copied again). The view still holds its last frame, and
// a tab opened next sends its frames on the same stream, where an <img>
// fires no second load: the stream is opened again for it.
test('a tab that closes under the panel is not shown again before the next tab\'s first frame', async () => {
  const tabs = { 'sess-1': 'https://one.example/' };
  const app = await load({
    globals: { lcGUI: true },
    routes: { ...perConversation(tabs), 'POST /api/sessions/*/browser/navigate': { status: 400, body: { error: 'there is no page to go back to' } } },
  });
  const view = app.el('browser-view');
  app.el('browser-empty').textContent = 'No page yet.';
  view.fire('load');
  await app.settle();
  assert.equal(view.classList.contains('stale'), false);

  // An open that failed while the page stayed: its error is in the note,
  // which the next poll hides again.
  app.el('browser-back').click();
  await app.settle();
  assert.match(app.el('browser-empty').textContent, /^Could not open it/);
  delete tabs['sess-1'];
  app.internals.renderBrowserPanel();
  await app.settle();
  assert.equal(view.classList.contains('stale'), true, 'the closed tab\'s last frame was left on show');
  assert.equal(app.el('browser-url').value, '', 'the closed tab\'s address was left in the bar');
  assert.equal(app.el('browser-empty').textContent, 'No page yet.',
    'an earlier error came back as the reason there is no page');
  assert.match(view.getAttribute('src'), /^\/api\/sessions\/sess-1\/browser\/stream\?r=\d+$/,
    'the stream was not opened again, so the next tab\'s first frame fires no load');

  tabs['sess-1'] = 'https://again.example/';
  app.internals.renderBrowserPanel();
  await app.settle();
  assert.equal(view.classList.contains('stale'), true, 'the next tab was shown before it sent a frame');
  view.fire('load');
  assert.equal(view.classList.contains('stale'), false);
});

test('an address bar answer about a tab that has since closed is dropped', async () => {
  let finish;
  const pending = new Promise((r) => { finish = r; });
  const tabs = { 'sess-1': 'https://one.example/' };
  const app = await load({
    globals: { lcGUI: true },
    routes: { ...perConversation(tabs), 'POST /api/sessions/*/browser/navigate': () => pending },
  });
  const url = app.el('browser-url');
  url.value = 'two.example';
  url.fire('keydown', { key: 'Enter' });
  delete tabs['sess-1'];
  app.internals.renderBrowserPanel();
  await app.settle();
  finish({ url: 'https://two.example/' });
  await app.settle();
  assert.equal(app.el('browser-empty').hidden, false, 'an answer about the closed tab took "No page yet" away');
  assert.notEqual(url.value, 'https://two.example/');
});

// Within one visit, only the newest address bar request's answer is
// used, and a location poll asked before an address bar answer arrived
// says less than that answer did.
test('an older address bar failure that comes back last does not cover the newer page', async () => {
  let finishFirst;
  let call = 0;
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      ...perConversation({}),
      'POST /api/sessions/*/browser/navigate': () => {
        call += 1;
        if (call === 1) return new Promise((r) => { finishFirst = r; });
        return { url: 'https://good.example/' };
      },
    },
  });
  const url = app.el('browser-url');
  url.value = 'slow.example';
  url.fire('keydown', { key: 'Enter' });
  url.value = 'good.example';
  url.fire('keydown', { key: 'Enter' });
  await app.settle();
  finishFirst({ status: 400, body: { error: 'could not load https://slow.example/' } });
  await app.settle();
  assert.equal(app.el('browser-empty').hidden, true, 'the older failure covered the newer page');
  assert.equal(url.value, 'https://good.example/');
});

test('a location poll asked before an address bar answer does not undo it', async () => {
  let finishPoll;
  let hold = false;
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      ...perConversation({}),
      'GET /api/sessions/*/browser': () => (hold ? new Promise((r) => { finishPoll = r; }) : { has_tab: false, width: 1280, height: 800 }),
      'POST /api/sessions/*/browser/navigate': { url: 'https://opened.example/' },
    },
  });
  hold = true;
  app.internals.renderBrowserPanel();
  const url = app.el('browser-url');
  url.value = 'opened.example';
  url.fire('keydown', { key: 'Enter' });
  await app.settle();
  finishPoll({ has_tab: false, width: 1280, height: 800 });
  await app.settle();
  assert.equal(app.el('browser-empty').hidden, true, 'a poll from before the page opened put "No page yet" back over it');
});

// Timers a test runs by hand.
function handTimers() {
  const timers = [];
  return {
    timers,
    setTimeout: (fn, delay) => { const t = { fn, delay, cleared: false }; timers.push(t); return t; },
    clearTimeout: (t) => { if (t) t.cleared = true; },
  };
}

test('a stream opened again after the daemon came back waits for its own frame', async () => {
  let now = 1_000_000;
  class ClockDate extends Date { static now() { return now; } }
  const clock = handTimers();
  let down = false;
  const tabs = { 'sess-1': 'https://one.example/', 'sess-2': 'https://two.example/' };
  const app = await load({
    globals: { lcGUI: true, Date: ClockDate, setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout },
    routes: {
      ...perConversation(tabs),
      'GET /api/sessions/*/browser': (_b, { path }) => {
        if (down) return { networkError: 'daemon restarting' };
        const id = path.split('/')[3];
        return tabs[id] ? { has_tab: true, url: tabs[id] } : { has_tab: false };
      },
    },
  });
  const view = app.el('browser-view');
  view.fire('load');
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  now += 500;
  down = true;
  app.internals.renderBrowserPanel();
  await app.settle();
  now += app.internals.STALE_FALLBACK_MS;
  down = false;
  app.internals.renderBrowserPanel();
  await app.settle();
  assert.ok(clock.timers.some((t) => !t.cleared && t.delay === 0), 'the stream was not opened again');
  assert.equal(view.classList.contains('stale'), true,
    'the view was shown before the stream opened again sent a frame, with the first conversation\'s frame in it');
});

test('a frame on the stream calls off a reconnect still waiting', async () => {
  const clock = handTimers();
  const app = await load({
    globals: { lcGUI: true, setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout },
    routes: perConversation({ 'sess-1': 'https://one.example/' }),
  });
  const view = app.el('browser-view');
  const src = view.getAttribute('src');
  view.fire('error');
  view.fire('load');
  for (const t of clock.timers) if (!t.cleared) t.fn();
  assert.equal(view.getAttribute('src'), src, 'a reconnect opened a working stream again');
});

test('a stream opened again for a closed tab can still reconnect', async () => {
  const clock = handTimers();
  // Every reading a millisecond on, so two addresses made in one real
  // millisecond still differ.
  let now = 1_000_000;
  class TickingDate extends Date { static now() { now += 1; return now; } }
  const tabs = { 'sess-1': 'https://one.example/' };
  const app = await load({
    globals: { lcGUI: true, Date: TickingDate, setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout },
    routes: perConversation(tabs),
  });
  const view = app.el('browser-view');
  view.fire('load');
  await app.settle();
  view.fire('error');
  delete tabs['sess-1'];
  app.internals.renderBrowserPanel();
  await app.settle();
  const reopened = view.getAttribute('src');
  assert.match(reopened, /\?r=\d+$/);
  // The stream opened for the next tab breaks too.
  view.fire('error');
  for (const t of clock.timers.slice()) if (!t.cleared) t.fn();
  assert.notEqual(view.getAttribute('src'), reopened,
    'a reconnect from before the tab closed held the slot, and the new stream was never opened again');
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

// The page zooms itself (js/zoom.js, a CSS zoom on the root). The view's
// rectangle and the pointer are then in the window's pixels and its
// border and inner size in the page's layout pixels, larger by the zoom.
// A click lands where it was aimed whatever the zoom.
test('a click on a zoomed page lands where it was aimed', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const view = app.el('browser-view');
  const zoom = 1.25;
  view.style.width = '642px';
  view.clientWidth = 640;
  view.clientHeight = 400;
  view.clientLeft = 1;
  view.clientTop = 1;
  view.getBoundingClientRect = () => ({ left: 100, top: 50, width: 642 * zoom, height: 402 * zoom });
  // Where page point (1000, 600) is drawn in the window.
  const x = 100 + 1 * zoom + 1000 * (640 * zoom / 1280);
  const y = 50 + 1 * zoom + 600 * (400 * zoom / 800);
  view.fire('mousedown', { clientX: x, clientY: y, button: 0, buttons: 1, detail: 1 });
  app.fireWindow('mouseup', { clientX: x, clientY: y, button: 0, buttons: 0, detail: 1 });
  await app.settle();
  const sent = app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body.mouse);
  assert.deepEqual([sent[0].type, sent[0].x, sent[0].y], ['mousePressed', 1000, 600],
    'the click landed the zoom factor away from where it was aimed');
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

// Ctrl+Shift+C is not a copy in a browser. Sent as one, it put the
// selection on the clipboard under a page that uses the keys itself.
test('with Shift held as well, only redo and paste are editing shortcuts', async () => {
  const app = await load({ init: false });
  const { editCommandFor } = app.internals;
  const key = (k, mods) => ({ key: k, code: `Key${k.toUpperCase()}`, keyCode: k.toUpperCase().charCodeAt(0), shiftKey: true, ...mods });
  for (const mac of [true, false]) {
    const held = mac ? { metaKey: true } : { ctrlKey: true };
    for (const k of ['A', 'C', 'X', 'Y']) {
      assert.equal(editCommandFor(key(k, held), mac), '', `${mac ? 'Cmd' : 'Ctrl'}+Shift+${k} carries an editing command`);
    }
    assert.equal(editCommandFor(key('Z', held), mac), 'redo');
    assert.equal(editCommandFor(key('V', held), mac), 'paste');
  }
});

// The window's zoom shortcut (js/zoom.js) is the window's alone. Passed on
// to the page as well, Ctrl+plus zoomed the page in the panel on Windows
// and Linux, the browser kept that zoom for the site, and every click
// after it missed: a zoomed page is not where its picture says it is.
test('the window\'s zoom shortcut is not passed on to the page', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const view = app.el('browser-view');
  const shortcuts = [
    { key: '=', code: 'Equal', keyCode: 187, ctrlKey: true },
    { key: '+', code: 'Equal', keyCode: 187, ctrlKey: true, shiftKey: true },
    { key: '-', code: 'Minus', keyCode: 189, ctrlKey: true },
    { key: '0', code: 'Digit0', keyCode: 48, ctrlKey: true },
    { key: '+', code: 'NumpadAdd', keyCode: 107, ctrlKey: true },
    { key: '=', code: 'Equal', keyCode: 187, metaKey: true },
  ];
  for (const k of shortcuts) {
    view.fire('keydown', k);
    view.fire('keyup', k);
  }
  await app.settle();
  assert.deepEqual(app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body), [],
    'a zoom shortcut reached the page');

  // The same keys with no Ctrl or Cmd are characters, and go to the page.
  view.fire('keydown', { key: '=', code: 'Equal', keyCode: 187 });
  view.fire('keydown', { key: '0', code: 'Digit0', keyCode: 48 });
  await app.settle();
  const sent = app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body.key.text);
  assert.deepEqual(sent, ['=', '0']);
});

// A key coming up follows what its going down did, whichever of the two
// had Ctrl held: the page gets both halves of a key press or neither.
test('a key press reaches the page whole or not at all, whenever Ctrl is let go', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const view = app.el('browser-view');
  const sent = () => app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => `${c.body.key.type} ${c.body.key.key}`);

  // Ctrl+= as a zoom shortcut, with Ctrl let go first: neither half.
  view.fire('keydown', { key: '=', code: 'Equal', keyCode: 187, ctrlKey: true });
  view.fire('keyup', { key: '=', code: 'Equal', keyCode: 187 });
  await app.settle();
  assert.deepEqual(sent(), [], 'the end of a key press the page never saw the start of');

  // "=" typed, with Ctrl pressed before it comes up: both halves.
  view.fire('keydown', { key: '=', code: 'Equal', keyCode: 187 });
  view.fire('keyup', { key: '=', code: 'Equal', keyCode: 187, ctrlKey: true });
  await app.settle();
  assert.deepEqual(sent(), ['keyDown =', 'keyUp ='], 'a key left held down in the page');
});

// AltGr is Ctrl+Alt on Windows and Linux. A character typed with it is a
// character, for the page, and not the window's zoom shortcut.
test('a character typed with AltGr is not a zoom shortcut', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const { zoomKey } = app.internals;
  assert.equal(zoomKey({ key: '0', ctrlKey: true, altKey: true }), null);
  assert.equal(zoomKey({ key: '=', ctrlKey: true, altKey: true }), null);
  const view = app.el('browser-view');
  view.fire('keydown', { key: '=', code: 'Digit0', keyCode: 48, ctrlKey: true, altKey: true });
  await app.settle();
  const sent = app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body.key.text);
  assert.deepEqual(sent, ['=']);
});

// The wheel with Ctrl or Cmd zooms the window (js/zoom.js), and that turn
// is not passed on to the page as well.
test('the window\'s zoom gesture is not passed on to the page', async () => {
  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const view = app.el('browser-view');
  view.fire('wheel', { offsetX: 100, offsetY: 100, deltaX: 0, deltaY: -100, deltaMode: 0, ctrlKey: true });
  view.fire('wheel', { offsetX: 100, offsetY: 100, deltaX: 0, deltaY: 100, deltaMode: 0, metaKey: true });
  await app.settle();
  assert.deepEqual(app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body), [],
    'a zoom gesture reached the page');
  // A plain turn scrolls the page.
  view.fire('wheel', { offsetX: 100, offsetY: 100, deltaX: 0, deltaY: 40, deltaMode: 0 });
  await app.settle();
  const sent = app.callsTo('POST', '/api/sessions/sess-1/browser/input').map((c) => c.body.mouse.type);
  assert.deepEqual(sent, ['mouseWheel']);
});

test('a zoom shortcut says which way the window zooms', async () => {
  const app = await load({ init: false });
  const { zoomKey } = app.internals;
  assert.equal(zoomKey({ key: '=', ctrlKey: true }), 1);
  assert.equal(zoomKey({ key: '+', metaKey: true }), 1);
  assert.equal(zoomKey({ key: '-', ctrlKey: true }), -1);
  assert.equal(zoomKey({ key: '_', ctrlKey: true }), -1);
  assert.equal(zoomKey({ key: '0', ctrlKey: true }), 0);
  assert.equal(zoomKey({ key: '=' }), null, 'a character, not a shortcut');
  assert.equal(zoomKey({ key: 'a', ctrlKey: true }), null);
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

// The conversation's tabs, from the location answer: a click on one
// switches to it, its "x" closes it, "+" opens another and hands the
// address bar the focus.
const withTabs = (tabs, extra = {}) => ({
  ...settingsWith({ browser: true }),
  'GET /api/sessions/*/browser': () => {
    const active = tabs.find((t) => t.active);
    return { has_tab: tabs.length > 0, url: active ? active.url : undefined, title: active ? active.title : undefined, tabs };
  },
  ...extra,
});

test('the panel draws the conversation\'s tabs, the active one marked', async () => {
  const tabs = [
    { id: 't1', url: 'https://one.example/', title: 'One', active: false },
    { id: 't2', url: 'about:blank', title: 'about:blank', active: true },
  ];
  const app = await load({ globals: { lcGUI: true }, routes: withTabs(tabs) });
  const drawn = app.el('browser-tab-list').querySelectorAll('.browser-tab');
  assert.equal(drawn.length, 2);
  assert.equal(drawn[0].querySelector('.browser-tab-open').textContent, 'One');
  assert.equal(drawn[1].querySelector('.browser-tab-open').textContent, 'New tab');
  assert.ok(!drawn[0].classList.contains('active') && drawn[1].classList.contains('active'));
  // The active tab has no page, so the address bar is empty.
  assert.equal(app.el('browser-url').value, '');
});

test('a click on a tab switches to it, its x closes it, and + opens one', async () => {
  const tabs = [
    { id: 't1', url: 'https://one.example/', title: 'One', active: true },
    { id: 't2', url: 'https://two.example/', title: 'Two', active: false },
  ];
  const answer = () => ({ tabs });
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'POST /api/sessions/*/browser/tabs': answer,
      'POST /api/sessions/*/browser/tabs/*/activate': answer,
      'DELETE /api/sessions/*/browser/tabs/*': answer,
    }),
  });
  const tab = (i) => app.el('browser-tab-list').querySelectorAll('.browser-tab')[i];
  tab(1).querySelector('.browser-tab-open').click();
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/browser/tabs/t2/activate').length, 1);

  tab(0).querySelector('.browser-tab-close').click();
  await app.settle();
  assert.equal(app.callsTo('DELETE', '/api/sessions/sess-1/browser/tabs/t1').length, 1);
  assert.equal(app.callsTo('POST', /\/browser\/tabs\/[^/]+\/activate$/).length, 1, 'the x also switched to the tab it closes');

  app.el('browser-tab-new').click();
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/browser/tabs').length, 1);
  assert.equal(app.doc.activeElement, app.el('browser-url'), 'the new tab\'s address is not where the focus went');
});

test('the address bar follows the active tab', async () => {
  const tabs = [
    { id: 't1', url: 'https://one.example/', title: 'One', active: true },
    { id: 't2', url: 'https://two.example/', title: 'Two', active: false },
  ];
  const app = await load({ globals: { lcGUI: true }, routes: withTabs(tabs) });
  assert.equal(app.el('browser-url').value, 'https://one.example/');
  tabs[0].active = false;
  tabs[1].active = true;
  app.internals.renderBrowserPanel();
  await app.settle();
  assert.equal(app.el('browser-url').value, 'https://two.example/');
  tabs.push({ id: 't3', url: 'about:blank', title: '', active: true });
  tabs[1].active = false;
  app.internals.renderBrowserPanel();
  await app.settle();
  assert.equal(app.el('browser-url').value, '', 'a new tab showed the last tab\'s address');
});

// The header button hides the panel and shows it again. It is there only
// while the panel is, and hiding keeps the page running: the stream stays
// open, so the model keeps the browser.
test('the header button hides and shows the browser panel, and is there only with it', async () => {
  const off = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: false }) });
  assert.equal(off.el('toggle-browser').hidden, true);

  const app = await load({ globals: { lcGUI: true }, routes: settingsWith({ browser: true }) });
  const toggle = app.el('toggle-browser');
  assert.equal(toggle.hidden, false);
  toggle.click();
  assert.ok(app.el('browser-panel').classList.contains('collapsed'));
  assert.ok(app.el('resize-browser').classList.contains('collapsed'));
  assert.equal(app.el('browser-view').getAttribute('src'), '/api/sessions/sess-1/browser/stream',
    'hiding the panel closed its stream, and with it the model\'s browser');
  assert.equal(JSON.parse(app.storage.get('localcode.panelWidths')).browserCollapsed, true);

  const reopened = await load({
    globals: { lcGUI: true },
    routes: settingsWith({ browser: true }),
    localStorage: { 'localcode.panelWidths': JSON.stringify({ browserCollapsed: true }) },
  });
  assert.ok(reopened.el('browser-panel').classList.contains('collapsed'));
  reopened.el('toggle-browser').click();
  assert.ok(!reopened.el('browser-panel').classList.contains('collapsed'));
});

// "+" hands the address bar the focus for the new tab's page. The bar is
// emptied first: a poll leaves a focused bar alone, and what is typed
// would otherwise be added to the last tab's address.
test('a new tab\'s address bar is empty when it gets the focus', async () => {
  const tabs = [{ id: 't1', url: 'https://one.example/', title: 'One', active: true }];
  const app = await load({
    globals: { lcGUI: true },
    routes: withTabs(tabs, {
      'POST /api/sessions/*/browser/tabs': () => {
        tabs[0].active = false;
        tabs.push({ id: 't2', url: 'about:blank', title: 'about:blank', active: true });
        return { tabs };
      },
    }),
  });
  assert.equal(app.el('browser-url').value, 'https://one.example/');
  app.el('browser-tab-new').click();
  await app.settle();
  assert.equal(app.doc.activeElement, app.el('browser-url'));
  assert.equal(app.el('browser-url').value, '', 'the last tab\'s address was left for the new tab\'s');
});
