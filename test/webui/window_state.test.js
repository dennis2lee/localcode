'use strict';

// What the page keeps for itself, and where.
//
// In a browser tab that is localStorage and nothing else. In the desktop
// window the same storage is tied to an address, port included, that the
// window does not always get back: another program on the port, a range
// Windows reserved at boot, a second window, the program under another
// file name. Each of those was measured in the real window on a Windows
// runner to bring the text size back at its default. So in the window a
// change also goes to the daemon (js/prefs.js), which writes it to
// ~/.localcode/window-state.json, and js/kept.js reads that back into
// localStorage before the page draws.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const { load } = require('./harness');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');
const JS_DIR = path.join(STATIC_DIR, 'js');
const ROUTE = '/api/window/state';

// The desktop window, with js/kept.js having read the file at the start
// of the page. kept says what it found there.
function inWindow(kept = 'read', opts = {}) {
  return load({
    ...opts,
    globals: { lcGUI: true, localcodeKept: kept, ...(opts.globals || {}) },
    routes: { [`PUT ${ROUTE}`]: { status: 204 }, ...(opts.routes || {}) },
  });
}

function writes(app) {
  return app.calls.filter((c) => c.path === ROUTE);
}

// Everything sent, as one object: a later write of a name over an
// earlier one, the way the daemon applies them. from is how many writes
// to skip: a page writes two things as it loads (the zoom it starts at,
// and which panels the conversation it opens shows), and a test about
// one change counts from after those.
function sent(app, from = 0) {
  const all = {};
  for (const c of writes(app).slice(from)) Object.assign(all, c.body);
  return all;
}

async function openTypography(app) {
  app.el('settings-btn').click();
  await app.settle();
  app.el('settings-tab-typography').click();
}

async function setTextSize(app, value) {
  const select = app.el('typo-size-select');
  select.value = value;
  select.fire('change');
  await app.settle();
}

test('a browser tab keeps its settings in its own storage and tells the daemon nothing', async () => {
  const app = await load();
  await openTypography(app);
  await setTextSize(app, '1.25');
  app.applyZoom(1.5);
  await app.settle();

  assert.equal(app.storage.get('localcode.textScale'), '1.25');
  assert.equal(app.storage.get('localcode.zoom'), '1.5');
  assert.deepEqual(writes(app), [], 'a browser tab wrote to the window\'s file');
});

test('in the desktop window a setting made in Settings goes to the daemon as it is made', async () => {
  const app = await inWindow();
  const loaded = writes(app).length;
  await openTypography(app);
  await setTextSize(app, '1.125');

  assert.equal(app.storage.get('localcode.textScale'), '1.125', 'the page\'s own storage is still written');
  const calls = writes(app).slice(loaded);
  assert.equal(calls.length, 1, `expected one write, got ${JSON.stringify(calls)}`);
  assert.equal(calls[0].method, 'PUT');
  assert.deepEqual(calls[0].body, { 'localcode.textScale': '1.125' });
});

test('putting a setting back to its default forgets it in the file too', async () => {
  const app = await inWindow('read', { localStorage: { 'localcode.textScale': '1.25' } });
  const loaded = writes(app).length;
  await openTypography(app);
  await setTextSize(app, '1');

  assert.equal(app.storage.has('localcode.textScale'), false);
  assert.deepEqual(sent(app, loaded), { 'localcode.textScale': null });
});

test('every kind of thing the page remembers is written, each under the name it is read by', async () => {
  const app = await inWindow('read', {
    routes: {
      'GET /api/sessions/groups': { names: ['work'] },
      'POST /api/sessions/groups': { status: 204 },
    },
  });

  // The faces and the four sizes.
  await openTypography(app);
  const doc = app.el('typo-doc-select');
  doc.value = Array.from(doc.querySelectorAll('option')).map((o) => o.value).find((v) => v.includes('Georgia'));
  doc.fire('change');
  await setTextSize(app, '1.125');
  const read = app.el('typo-size-read-select');
  read.value = '1.25';
  read.fire('change');
  // The zoom.
  app.applyZoom(1.25);
  // A panel's width, dragged, and a panel hidden.
  app.el('center').style.width = '4000px';
  app.el('left-panel').style.width = '260px';
  app.el('resize-left').fire('pointerdown', { button: 0, clientX: 260 });
  app.doc.fire('pointermove', { clientX: 400 });
  app.doc.fire('pointerup', { clientX: 400 });
  app.el('toggle-right').click();
  // The archive, opened.
  await app.el('archive-toggle').click();
  // A group, folded.
  app.internals.writeCollapsedGroups(new Set(['work']));
  await app.settle();

  const all = sent(app);
  assert.match(all['localcode.face.doc'], /Georgia/);
  assert.equal(all['localcode.textScale'], '1.125');
  assert.equal(all['localcode.textScale.read'], '1.25');
  assert.equal(all['localcode.zoom'], '1.25');
  assert.equal(JSON.parse(all['localcode.panelWidths']).left, 400);
  assert.equal(JSON.parse(all['localcode.panelWidths']).rightCollapsed, true);
  assert.ok(Array.isArray(JSON.parse(all['localcode.panelViews'])), 'which panels a conversation shows was not written');
  assert.equal(all.archiveOpen, '1');
  assert.deepEqual(JSON.parse(all['localcode.collapsedGroups']), ['work']);

  // And each is in the page's own storage with the same value, which is
  // what the readers read.
  for (const [name, value] of Object.entries(all)) {
    assert.equal(app.storage.get(name), value, `${name} differs between the file and the page's storage`);
  }
});

test('a value written again unchanged is not sent again', async () => {
  // The zoom is written on every page load with the value it already
  // has, and so is which panels the open conversation shows.
  const app = await inWindow('read', { localStorage: { 'localcode.zoom': '1.25' } });
  assert.equal(writes(app).filter((c) => 'localcode.zoom' in c.body).length, 0, 'a page load re-sent the zoom it had just read');

  app.writePref('localcode.zoom', '1.25');
  await app.settle();
  assert.equal(writes(app).filter((c) => 'localcode.zoom' in c.body).length, 0);

  app.writePref('localcode.zoom', '1.5');
  await app.settle();
  assert.deepEqual(writes(app).filter((c) => 'localcode.zoom' in c.body).map((c) => c.body['localcode.zoom']), ['1.5']);
});

test('the first page that finds nothing kept hands over what its own storage holds', async () => {
  // Somebody who set these before the file existed, on an address that
  // still has them: the first start of this version keeps them rather
  // than losing them the next time the address changes.
  const app = await inWindow('nothing yet', {
    localStorage: {
      'localcode.textScale': '1.25',
      'localcode.face.ui': 'Verdana, Geneva, Tahoma, sans-serif',
      'localcode.zoom': '1.1',
      archiveOpen: '1',
      // Not the page's own: left where it is.
      'somebody.elses': 'x',
    },
  });
  const all = sent(app);
  assert.equal(all['localcode.textScale'], '1.25');
  assert.equal(all['localcode.face.ui'], 'Verdana, Geneva, Tahoma, sans-serif');
  assert.equal(all['localcode.zoom'], '1.1');
  assert.equal(all.archiveOpen, '1');
  assert.equal('somebody.elses' in all, false, 'a name that is not the page\'s was sent');
  // Once, in one request, with everything in it.
  const carrying = writes(app).filter((c) => 'localcode.textScale' in c.body);
  assert.equal(carrying.length, 1);
  assert.equal(carrying[0].body['localcode.face.ui'], 'Verdana, Geneva, Tahoma, sans-serif');
});

test('with a file already kept, the page hands nothing over on load', async () => {
  const app = await inWindow('read', { localStorage: { 'localcode.textScale': '1.25', 'localcode.zoom': '1.1' } });
  const all = sent(app);
  assert.equal('localcode.textScale' in all, false);
  assert.equal('localcode.zoom' in all, false);
});

test('a window whose daemon did not answer at the start writes nothing to it', async () => {
  // js/kept.js sets localcodeKept only when it got an answer. Without one
  // the page has not been given the file, and writing to it from here
  // would replace settings it never read.
  const app = await load({ globals: { lcGUI: true }, routes: { [`PUT ${ROUTE}`]: { status: 204 } } });
  await openTypography(app);
  await setTextSize(app, '1.25');
  assert.equal(app.storage.get('localcode.textScale'), '1.25');
  assert.deepEqual(writes(app), []);
});

// A timer that fires at once, so a retry does not wait in a test.
function instantTimers() {
  return { setTimeout: (fn) => { queueMicrotask(fn); return 0; } };
}

test('a write the daemon did not take is sent again', async () => {
  // Counted from the change under test, not from the writes of the load.
  let attempts = null;
  const app = await inWindow('read', {
    globals: instantTimers(),
    routes: {
      [`PUT ${ROUTE}`]: () => {
        if (attempts === null) return { status: 204 };
        attempts += 1;
        if (attempts === 1) return { networkError: 'connection refused' };
        if (attempts === 2) return { status: 500, body: { error: 'could not write' } };
        return { status: 204 };
      },
    },
  });
  const before = writes(app).length;
  attempts = 0;
  app.writePref('localcode.textScale', '1.4');
  await app.settle();
  const mine = writes(app).slice(before);
  assert.equal(mine.length, 3, 'expected the write to be tried until it was taken');
  for (const c of mine) assert.deepEqual(c.body, { 'localcode.textScale': '1.4' });
});

test('a write the daemon refuses is not sent again', async () => {
  const app = await inWindow('read', {
    globals: instantTimers(),
    routes: { [`PUT ${ROUTE}`]: { status: 400, body: { error: 'not a name the window keeps a setting under' } } },
  });
  const before = writes(app).length;
  app.writePref('localcode.textScale', '1.4');
  await app.settle();
  assert.equal(writes(app).length - before, 1);
});

test('a daemon that never takes a write is given up on, and the next change tries again', async () => {
  let down = false;
  const app = await inWindow('read', {
    globals: instantTimers(),
    routes: { [`PUT ${ROUTE}`]: () => (down ? { networkError: 'connection refused' } : { status: 204 }) },
  });
  down = true;
  const before = writes(app).length;
  app.writePref('localcode.textScale', '1.4');
  await app.settle();
  const tried = writes(app).length - before;
  assert.equal(tried, 6, `tried ${tried} times, want six and then a stop`);

  // What was not taken is still waiting, and goes with the next change.
  down = false;
  app.writePref('localcode.zoom', '1.1');
  await app.settle();
  const last = writes(app)[writes(app).length - 1];
  assert.deepEqual(last.body, { 'localcode.textScale': '1.4', 'localcode.zoom': '1.1' });
});

test('changes made while a write is on its way go out together, the last value of each', async () => {
  let release;
  let held = null;
  const app = await inWindow('read', {
    routes: {
      [`PUT ${ROUTE}`]: () => {
        if (held === null) return { status: 204 };
        held += 1;
        if (held === 1) return new Promise((resolve) => { release = () => resolve({ status: 204 }); });
        return { status: 204 };
      },
    },
  });
  const loaded = writes(app).length;
  held = 0;
  app.writePref('localcode.zoom', '1.1');
  await app.settle();
  // The first is with the daemon. Three more changes, two to one name.
  app.writePref('localcode.zoom', '1.25');
  app.writePref('localcode.textScale', '1.125');
  app.writePref('localcode.zoom', '1.5');
  await app.settle();
  assert.equal(writes(app).length - loaded, 1, 'a second request went out before the first was answered');
  release();
  await app.settle();
  const calls = writes(app).slice(loaded);
  assert.equal(calls.length, 2);
  assert.deepEqual(calls[1].body, { 'localcode.zoom': '1.5', 'localcode.textScale': '1.125' });
});

test('a change made while a failing write is on its way is the one that is sent next', async () => {
  // The write that failed is put back to be tried again, and must not be
  // put back over a newer value of the same name.
  let fail;
  let held = null;
  const app = await inWindow('read', {
    globals: instantTimers(),
    routes: {
      [`PUT ${ROUTE}`]: () => {
        if (held === null) return { status: 204 };
        held += 1;
        if (held === 1) return new Promise((resolve) => { fail = () => resolve({ status: 500, body: { error: 'could not write' } }); });
        return { status: 204 };
      },
    },
  });
  const loaded = writes(app).length;
  held = 0;
  app.writePref('localcode.zoom', '1.1');
  app.writePref('localcode.textScale', '1.125');
  await app.settle();
  app.writePref('localcode.zoom', '1.5');
  fail();
  await app.settle();
  const calls = writes(app).slice(loaded);
  assert.equal(calls.length, 2);
  assert.deepEqual(calls[1].body, { 'localcode.zoom': '1.5', 'localcode.textScale': '1.125' });
});

test('a small write asks to outlive the page, and one too large for that is sent plainly', async () => {
  // The browser finishes a keepalive request after the page has gone,
  // which is what a change made just before the window closes needs. It
  // refuses one over 64 KiB of body outright, counted in bytes: 21,000
  // Korean characters are 21,000 units of a string and 63,000 bytes.
  const app = await inWindow();
  const loaded = writes(app).length;
  app.writePref('localcode.zoom', '1.5');
  await app.settle();
  app.writePref('localcode.collapsedGroups', JSON.stringify(['가'.repeat(21000)]));
  await app.settle();
  app.writePref('localcode.face.doc', 'x'.repeat(19000));
  await app.settle();
  const calls = writes(app).slice(loaded);
  assert.equal(calls.length, 3);
  assert.equal(calls[0].keepalive, true, 'an ordinary change did not ask to outlive the page');
  assert.equal(calls[1].keepalive, undefined, 'a body over the limit in bytes asked for what the browser refuses');
  assert.equal(calls[2].keepalive, true, 'a body under the limit even at three bytes a unit was sent plainly');
});

test('a write the browser would not send as one that outlives the page is sent plainly', async () => {
  // The limit is shared by every such request of the page, so one can be
  // refused whatever its own size. The fetch then throws without sending.
  const app = await inWindow('read', { globals: instantTimers() });
  const loaded = writes(app).length;
  const real = app.routes[`PUT ${ROUTE}`];
  let refused = 0;
  app.routes[`PUT ${ROUTE}`] = (body) => {
    const call = app.calls[app.calls.length - 1];
    if (call.keepalive) { refused += 1; return { networkError: 'TypeError: Failed to fetch' }; }
    return real;
  };
  app.writePref('localcode.zoom', '1.5');
  await app.settle();
  const calls = writes(app).slice(loaded);
  assert.equal(refused, 1, 'asked to outlive the page again after being refused');
  assert.equal(calls.length, 2);
  assert.equal(calls[1].keepalive, undefined);
  assert.deepEqual(calls[1].body, { 'localcode.zoom': '1.5' });
});

test('a name that is not the page\'s own is kept in the page and not sent', async () => {
  const app = await inWindow();
  const before = writes(app).length;
  app.writePref('smart_agent', 'true');
  app.writePref('localcode.', 'x');
  await app.settle();
  assert.equal(app.storage.get('smart_agent'), 'true');
  assert.equal(writes(app).length, before);
});

test('storage that refuses does not stop the page, and the change still reaches the file', async () => {
  const app = await inWindow();
  // The harness's localStorage is a Map underneath: make that refuse.
  app.storage.set = () => { throw new Error('QuotaExceededError'); };
  app.storage.has = () => { throw new Error('SecurityError'); };
  assert.equal(app.readPref('localcode.zoom'), null);
  assert.doesNotThrow(() => app.writePref('localcode.textScale', '1.25'));
  await app.settle();
  assert.deepEqual(writes(app)[writes(app).length - 1].body, { 'localcode.textScale': '1.25' });
});

// ---- js/kept.js, run as the page runs it: a classic script, first in
// the head, against the window it finds.

// A localStorage that can be listed, as a real one can.
function storage(initial = {}) {
  const map = new Map(Object.entries(initial));
  return {
    map,
    api: {
      getItem: (k) => (map.has(k) ? map.get(k) : null),
      setItem: (k, v) => { map.set(k, String(v)); },
      removeItem: (k) => { map.delete(k); },
      get length() { return map.size; },
      key: (i) => [...map.keys()][i] ?? null,
    },
  };
}

// runKept runs the shipped script. answer is what the daemon says: an
// object for a 200 with that body, a number for that status, a string for
// that text as the body, or a function that throws from send().
function runKept({ gui = true, initial = {}, answer, store } = {}) {
  const st = store || storage(initial);
  const asked = [];
  class FakeXHR {
    open(method, url, isAsync) { asked.push({ method, url, isAsync }); }
    send() {
      if (typeof answer === 'function') return answer(this);
      if (typeof answer === 'number') { this.status = answer; this.responseText = ''; return undefined; }
      this.status = 200;
      this.responseText = typeof answer === 'string' ? answer : JSON.stringify(answer);
      return undefined;
    }
  }
  const sandbox = { XMLHttpRequest: FakeXHR, localStorage: st.api, Object, JSON };
  if (gui) sandbox.lcGUI = true;
  sandbox.window = sandbox;
  vm.runInNewContext(fs.readFileSync(path.join(JS_DIR, 'kept.js'), 'utf8'), sandbox, { filename: 'kept.js' });
  return { stored: Object.fromEntries(st.map), asked, kept: sandbox.localcodeKept };
}

test('kept.js does nothing at all in a browser tab', () => {
  const r = runKept({ gui: false, initial: { 'localcode.zoom': '1.5' }, answer: { available: true, kept: {} } });
  assert.deepEqual(r.asked, [], 'a browser tab asked the daemon for the window\'s file');
  assert.deepEqual(r.stored, { 'localcode.zoom': '1.5' });
  assert.equal(r.kept, undefined);
});

test('kept.js puts what was kept into the page\'s storage before anything reads it', () => {
  // A window that came up on an address it has never been on: nothing in
  // storage, everything in the file.
  const r = runKept({
    answer: { available: true, kept: { 'localcode.textScale': '1.125', 'localcode.zoom': '1.25', archiveOpen: '1' } },
  });
  assert.deepEqual(r.asked, [{ method: 'GET', url: '/api/window/state', isAsync: false }],
    'the file has to be asked for before the first paint, which is one synchronous request');
  assert.deepEqual(r.stored, { 'localcode.textScale': '1.125', 'localcode.zoom': '1.25', archiveOpen: '1' });
  assert.equal(r.kept, 'read');
});

test('kept.js makes the page\'s storage what the file says, and leaves other names alone', () => {
  // An address that holds older settings: the size was since changed in
  // another window, and the reading face put back to its default.
  const r = runKept({
    initial: {
      'localcode.textScale': '1.4',
      'localcode.face.doc': 'Georgia, serif',
      'localcode.zoom': '1.1',
      'somebody.elses': 'kept',
    },
    answer: { available: true, kept: { 'localcode.textScale': '1.125', 'localcode.zoom': '1.1' } },
  });
  assert.deepEqual(r.stored, {
    'localcode.textScale': '1.125',
    'localcode.zoom': '1.1',
    'somebody.elses': 'kept',
  });
  assert.equal(r.kept, 'read');
});

test('kept.js leaves the page\'s storage alone when nothing has been kept yet', () => {
  const r = runKept({ initial: { 'localcode.textScale': '1.4' }, answer: { available: true, kept: null } });
  assert.deepEqual(r.stored, { 'localcode.textScale': '1.4' });
  assert.equal(r.kept, 'nothing yet', 'the page is told to hand over what it has');
});

test('kept.js with an empty file kept clears the page\'s own names', () => {
  // Kept, and empty: everything was put back to its default somewhere.
  const r = runKept({ initial: { 'localcode.textScale': '1.4', other: 'x' }, answer: { available: true, kept: {} } });
  assert.deepEqual(r.stored, { other: 'x' });
  assert.equal(r.kept, 'read');
});

test('kept.js changes nothing when the daemon gives it no answer it can use', () => {
  const initial = { 'localcode.textScale': '1.4' };
  for (const [name, answer] of Object.entries({
    'a daemon that keeps nothing': { available: false, kept: null },
    'an older daemon with no such route': 404,
    'a daemon that failed': 500,
    'a refusal': 403,
    'a body that is not JSON': '<html>',
    'a body that is JSON and not an object': 'null',
    'a request that throws': () => { throw new Error('NetworkError'); },
  })) {
    const r = runKept({ initial, answer });
    assert.deepEqual(r.stored, initial, `${name}: the page's storage was changed`);
    assert.equal(r.kept, undefined, `${name}: the page was told the file had been read`);
  }
});

test('kept.js asks again when the daemon does not answer the first time', () => {
  // One answer that did not come would leave the page writing nothing to
  // the file for as long as it is open.
  let n = 0;
  const r = runKept({
    initial: { 'localcode.textScale': '1.4' },
    answer: (xhr) => {
      n += 1;
      if (n === 1) throw new Error('NetworkError');
      if (n === 2) { xhr.status = 500; xhr.responseText = ''; return; }
      xhr.status = 200;
      xhr.responseText = JSON.stringify({ available: true, kept: { 'localcode.textScale': '1.125' } });
    },
  });
  assert.equal(r.asked.length, 3);
  assert.deepEqual(r.stored, { 'localcode.textScale': '1.125' });
  assert.equal(r.kept, 'read');

  // And stops at three.
  const never = runKept({ initial: { 'localcode.textScale': '1.4' }, answer: 500 });
  assert.equal(never.asked.length, 3);
  assert.equal(never.kept, undefined);
  assert.deepEqual(never.stored, { 'localcode.textScale': '1.4' });
});

test('kept.js takes only strings under the page\'s own names from the answer', () => {
  const r = runKept({
    initial: { 'localcode.zoom': '1.1' },
    // Written as text, so "__proto__" is a name in the answer rather than
    // this test's own object's prototype.
    answer: '{"available":true,"kept":{"localcode.zoom":"1.5","localcode.textScale":1.25,'
      + '"smart_agent":"true","__proto__":"x","localcode.":"y","localcode.face.doc":null}}',
  });
  assert.deepEqual(r.stored, { 'localcode.zoom': '1.5' });
});

test('kept.js survives storage that refuses', () => {
  const st = storage({ 'localcode.zoom': '1.1' });
  st.api.setItem = () => { throw new Error('QuotaExceededError'); };
  let r;
  assert.doesNotThrow(() => {
    r = runKept({ store: st, answer: { available: true, kept: { 'localcode.zoom': '1.5' } } });
  });
  assert.equal(r.kept, undefined, 'storage that could not be written is not "read"');
});

// ---- the two together, against one file

// A daemon's file, and windows on it. Each window is its own address, so
// its own localStorage; open() is a page load there: kept.js first, then
// the page.
function sharedFile() {
  const file = { kept: null };
  const answer = () => ({ available: true, kept: file.kept === null ? null : { ...file.kept } });
  const put = (body) => {
    if (file.kept === null) file.kept = {};
    for (const [k, v] of Object.entries(body)) {
      if (v === null) delete file.kept[k];
      else file.kept[k] = v;
    }
    return { status: 204 };
  };
  const window_ = () => {
    const st = storage();
    return {
      stored: st.map,
      async open() {
        const r = runKept({ store: st, answer: answer() });
        return load({
          localStorage: Object.fromEntries(st.map),
          globals: { lcGUI: true, localcodeKept: r.kept },
          routes: { [`PUT ${ROUTE}`]: put },
        }).then((app) => {
          // The page's storage is the harness's own Map from here on:
          // carry what it writes back to this window's, as a real
          // localStorage would simply be the same one.
          this.sync = () => { st.map.clear(); for (const [k, v] of app.storage) st.map.set(k, v); };
          return app;
        });
      },
    };
  };
  return { file, window: window_ };
}

test('a setting made in one window is there in a window on another address', async () => {
  // The whole fault, end to end: the second window is where the next
  // start lands when the port has changed.
  const daemon = sharedFile();
  const first = daemon.window();
  const a = await first.open();
  await openTypography(a);
  await setTextSize(a, '1.125');
  a.applyZoom(1.25);
  await a.settle();
  first.sync();

  const second = daemon.window();
  const b = await second.open();
  await b.settle();
  assert.equal(b.storage.get('localcode.textScale'), '1.125');
  assert.equal(b.document.documentElement.style.getPropertyValue('--t-scale'), '1.125', 'the size was kept and not applied');
  assert.equal(b.document.documentElement.style.zoom, '1.25');
  await openTypography(b);
  assert.equal(b.el('typo-size-select').value, '1.125', 'the settings window does not show the size that was kept');
});

test('two windows open at once do not undo each other\'s settings', async () => {
  const daemon = sharedFile();
  const first = daemon.window();
  const second = daemon.window();
  const a = await first.open();
  const b = await second.open();
  await a.settle();
  await b.settle();

  // Each changes a different setting, the second after the first.
  await openTypography(a);
  await setTextSize(a, '1.25');
  b.applyZoom(1.5);
  await a.settle();
  await b.settle();
  assert.equal(daemon.file.kept['localcode.textScale'], '1.25');
  assert.equal(daemon.file.kept['localcode.zoom'], '1.5', 'the second window\'s write replaced the file instead of adding to it');

  // And one puts a setting back to its default.
  await setTextSize(a, '1');
  await a.settle();
  assert.equal('localcode.textScale' in daemon.file.kept, false);

  // The other window, loaded again, has both: the zoom it set, and no
  // size, although its own storage never heard of either change to it.
  second.sync();
  second.stored.set('localcode.textScale', '1.4'); // an older value this address still holds
  const again = await second.open();
  await again.settle();
  assert.equal(again.storage.has('localcode.textScale'), false, 'a size put back to its default came back from an address\'s older copy');
  assert.equal(again.storage.get('localcode.zoom'), '1.5');
});

test('the first start after the upgrade keeps what the window already had, and the next address gets it', async () => {
  const daemon = sharedFile();
  const first = daemon.window();
  // Set before the file existed, on the address the window is still on.
  first.stored.set('localcode.textScale', '1.25');
  first.stored.set('localcode.face.doc', 'Georgia, Palatino, "Palatino Linotype", "Book Antiqua", serif');
  const a = await first.open();
  await a.settle();
  assert.equal(daemon.file.kept['localcode.textScale'], '1.25', 'what the window held was not handed to the file');
  assert.match(daemon.file.kept['localcode.face.doc'], /Georgia/);

  const second = daemon.window();
  const b = await second.open();
  await b.settle();
  assert.equal(b.storage.get('localcode.textScale'), '1.25');
  assert.match(b.storage.get('localcode.face.doc'), /Georgia/);
});

// ---- the rules that hold it together

function stripComments(src) {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`])\/\/.*$/gm, '$1');
}

test('only prefs.js and kept.js touch localStorage', () => {
  // A write that goes straight to localStorage is one the window forgets
  // the next time its address changes, and nothing would say so.
  for (const name of fs.readdirSync(JS_DIR)) {
    if (name === 'prefs.js' || name === 'kept.js') continue;
    const src = stripComments(fs.readFileSync(path.join(JS_DIR, name), 'utf8'));
    assert.ok(!/\blocalStorage\b/.test(src), `${name} uses localStorage directly; go through js/prefs.js`);
  }
  // The head script reads and never writes: it runs before any module,
  // and after kept.js.
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  const inline = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => stripComments(m[1])).join('\n');
  assert.ok(/localStorage\.getItem/.test(inline), 'the head script no longer reads localStorage; this test reads the wrong thing');
  assert.ok(!/localStorage\.(setItem|removeItem|clear)/.test(inline), 'the head script writes localStorage');
});

test('the page and the daemon agree on which names are the page\'s own', () => {
  const rule = (file, re) => {
    const m = fs.readFileSync(file, 'utf8').match(re);
    assert.ok(m, `no name rule found in ${path.basename(file)}`);
    return m[1];
  };
  const kept = rule(path.join(JS_DIR, 'kept.js'), /var own = \/(.+)\/;/);
  const prefs = rule(path.join(JS_DIR, 'prefs.js'), /const OWN = \/(.+)\/;/);
  const daemon = rule(path.join(STATIC_DIR, '..', 'windowstate.go'), /windowStateKey = regexp\.MustCompile\(`(.+)`\)/);
  assert.equal(kept, prefs);
  assert.equal(kept, daemon);

  // And every name the page stores something under passes it.
  const own = new RegExp(kept);
  const names = new Set();
  for (const name of fs.readdirSync(JS_DIR)) {
    const src = stripComments(fs.readFileSync(path.join(JS_DIR, name), 'utf8'));
    for (const m of src.matchAll(/['"](localcode\.[A-Za-z0-9._-]*|archiveOpen)['"]/g)) names.add(m[1]);
  }
  assert.ok(names.size >= 10, `only ${names.size} names found; this test no longer reads what it thinks it reads`);
  for (const n of names) assert.ok(own.test(n), `the page stores under "${n}", which the daemon would refuse`);
});

test('kept.js is loaded before the first script that reads localStorage', () => {
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  const kept = html.indexOf('<script src="js/kept.js"></script>');
  assert.ok(kept > 0, 'index.html does not load js/kept.js');
  const firstRead = html.indexOf('localStorage.getItem');
  const entry = html.indexOf('<script type="module"');
  assert.ok(kept < firstRead, 'js/kept.js comes after the head script that reads localStorage');
  assert.ok(kept < entry, 'js/kept.js comes after the module entry point');
  assert.ok(kept < html.indexOf('</head>'), 'js/kept.js is not in the head');
});
