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
  // Seven: the first asked to outlive the page and was not sent at all,
  // which is not counted, and then six plain ones.
  const tried = writes(app).length - before;
  assert.equal(tried, 7, `tried ${tried} times, want one uncounted and six more, and then a stop`);

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

test('a change made during the last try of another is still sent', async () => {
  // The first change is on its sixth and last try when the second is
  // made. Stopping there left the second never tried once, though the
  // daemon had begun to answer.
  let active = false;
  let attempts = 0;
  let failSixth;
  const app = await inWindow('read', {
    globals: instantTimers(),
    routes: {
      [`PUT ${ROUTE}`]: () => {
        if (!active) return { status: 204 };
        attempts += 1;
        // 500s, so each is a request that was sent and counts.
        if (attempts <= 5) return { status: 500, body: { error: 'busy' } };
        if (attempts === 6) return new Promise((resolve) => { failSixth = () => resolve({ status: 500, body: { error: 'busy' } }); });
        return { status: 204 };
      },
    },
  });
  const before = writes(app).length;
  active = true;
  app.writePref('localcode.zoom', '1.1');
  await app.settle();
  assert.equal(attempts, 6, 'the sixth try is not the one on its way');

  app.writePref('localcode.textScale', '1.25');
  failSixth();
  await app.settle();
  const last = writes(app)[writes(app).length - 1];
  assert.ok(writes(app).length - before > 6, 'nothing was sent after the last try failed');
  assert.deepEqual(last.body, { 'localcode.textScale': '1.25', 'localcode.zoom': '1.1' });
});

test('a last try the browser would not send is still sent plainly', async () => {
  // Five tries the daemon failed, then one the browser refused to send
  // as a request that outlives the page. That one never left, so it is
  // not the sixth failure.
  let active = false;
  let attempts = 0;
  const app = await inWindow('read', {
    globals: instantTimers(),
    routes: {
      [`PUT ${ROUTE}`]: () => {
        if (!active) return { status: 204 };
        attempts += 1;
        if (attempts <= 5) return { status: 500, body: { error: 'busy' } };
        const call = app.calls[app.calls.length - 1];
        if (call.keepalive) return { networkError: 'TypeError: Failed to fetch' };
        return { status: 204 };
      },
    },
  });
  const before = writes(app).length;
  active = true;
  app.writePref('localcode.zoom', '1.5');
  await app.settle();
  const mine = writes(app).slice(before);
  const last = mine[mine.length - 1];
  assert.equal(last.keepalive, undefined, 'never sent plainly');
  assert.deepEqual(last.body, { 'localcode.zoom': '1.5' });
  assert.equal(mine.length, 7);
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
  assert.equal(app.readPref('localcode.face.doc'), null);
  assert.doesNotThrow(() => app.writePref('localcode.textScale', '1.25'));
  await app.settle();
  assert.deepEqual(writes(app)[writes(app).length - 1].body, { 'localcode.textScale': '1.25' });
  // And the page goes on from what it sent, which its storage never took.
  assert.equal(app.readPref('localcode.textScale'), '1.25');
  // Until the storage takes a write of that name again: then it is the
  // storage that answers, as it does for everything else.
  delete app.storage.set;
  delete app.storage.has;
  app.writePref('localcode.textScale', '1.125');
  await app.settle();
  app.storage.set('localcode.textScale', '1.4');
  assert.equal(app.readPref('localcode.textScale'), '1.4');
});

test('a browser tab whose storage refuses reads what the storage answers, as before', async () => {
  // Nothing is sent from a tab, so there is nothing else to go by.
  const app = await load({ localStorage: { 'localcode.textScale': '1.125' } });
  app.storage.set = () => { throw new Error('QuotaExceededError'); };
  app.writePref('localcode.textScale', '1.25');
  assert.equal(app.readPref('localcode.textScale'), '1.125');
  assert.equal(app.readShared('localcode.textScale'), '1.125');
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

test('kept.js reads only an object of names, or null, as what was kept', () => {
  // An array has no names: read as "kept, and empty" it cleared every
  // setting this address holds. Anything else that is not null read as
  // "nothing yet", and the page then handed its own storage over.
  const initial = { 'localcode.textScale': '1.25', 'localcode.zoom': '1.5' };
  for (const kept of [[], ['localcode.zoom'], 'oops', 42, true, false]) {
    const r = runKept({ initial, answer: { available: true, kept } });
    assert.deepEqual(r.stored, initial, `kept=${JSON.stringify(kept)} changed the page's storage`);
    assert.equal(r.kept, undefined, `kept=${JSON.stringify(kept)} was read as an answer`);
  }
});

test('kept.js leaves what the address had when storage refuses a write part way', () => {
  // What was kept is written before what is gone is removed, so a write
  // that is refused has removed nothing.
  const st = storage({ 'localcode.textScale': '1.25', 'localcode.face.doc': 'Georgia, serif' });
  st.api.setItem = () => { throw new Error('QuotaExceededError'); };
  const r = runKept({ store: st, answer: { available: true, kept: { 'localcode.zoom': '1.5' } } });
  assert.deepEqual(r.stored, { 'localcode.textScale': '1.25', 'localcode.face.doc': 'Georgia, serif' });
  assert.equal(r.kept, undefined);
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
// the page. An open page asks for the file again before it changes a
// value that holds several items (readShared in js/prefs.js), with the
// same synchronous request: asked lists those, and answer is what the
// daemon says to them, in the forms runKept takes.
function sharedFile(initial = null) {
  const file = { kept: initial };
  const answer = () => ({ available: true, kept: file.kept === null ? null : { ...file.kept } });
  const put = (body) => {
    if (file.kept === null) file.kept = {};
    for (const [k, v] of Object.entries(body)) {
      if (v === null) delete file.kept[k];
      else file.kept[k] = v;
    }
    return { status: 204 };
  };
  const window_ = (opts = {}) => {
    const st = storage();
    const win = {
      stored: st.map,
      asked: [],
      answer,
      async open() {
        const r = runKept({ store: st, answer: answer() });
        class Ask {
          open(method, url, isAsync) { this.asking = { method, url, isAsync }; }
          send() {
            win.asked.push(this.asking);
            const a = win.answer();
            if (typeof a === 'function') return a(this);
            if (typeof a === 'number') { this.status = a; this.responseText = ''; return undefined; }
            this.status = 200;
            this.responseText = typeof a === 'string' ? a : JSON.stringify(a);
            return undefined;
          }
        }
        return load({
          localStorage: Object.fromEntries(st.map),
          globals: { lcGUI: true, localcodeKept: r.kept, XMLHttpRequest: Ask, ...(opts.globals || {}) },
          routes: { [`PUT ${ROUTE}`]: put, ...(opts.routes || {}) },
        }).then((app) => {
          // The page's storage is the harness's own Map from here on:
          // carry what it writes back to this window's, as a real
          // localStorage would simply be the same one.
          this.sync = () => { st.map.clear(); for (const [k, v] of app.storage) st.map.set(k, v); };
          return app;
        });
      },
    };
    return win;
  };
  return { file, put, window: window_ };
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

// ---- a value that holds several items, changed in two windows
//
// The panel widths, the panels each conversation shows and the folded
// groups are one value each, written whole. A window that changed one
// item starting from the copy it read when its page loaded put every
// other item back to what it was then.

const open = (app, id) => app.selectSession(id, 'general-purpose', '/tmp/workspace');
const kept = (daemon, name) => JSON.parse(daemon.file.kept[name]);
const viewsOf = (daemon) => Object.fromEntries(kept(daemon, 'localcode.panelViews'));

test('hiding a panel for one conversation does not undo what another window hid for another', async () => {
  // As it was found: two pages on one file, each on its own conversation.
  const daemon = sharedFile();
  const a = await daemon.window().open();
  const b = await daemon.window().open();
  open(a, 'sess-1');
  open(b, 'sess-2');
  await a.settle();
  await b.settle();

  a.el('toggle-right').click();
  await a.settle();
  b.el('toggle-right').click();
  await b.settle();
  assert.deepEqual(viewsOf(daemon), { 'sess-1': ['right'], 'sess-2': ['right'] });

  // And a page that starts now shows the first conversation without it.
  const fresh = await daemon.window().open();
  await fresh.settle();
  assert.deepEqual(Object.fromEntries(JSON.parse(fresh.storage.get('localcode.panelViews')))['sess-1'], ['right']);
  assert.equal(fresh.el('right-panel').classList.contains('collapsed'), true, 'a fresh page shows the panel the first window hid');
});

test('a conversation opened in a second window shows the panels the first window left it with', async () => {
  // Read from the copy the second page started with, the conversation
  // looked like one opened for the first time, and was written down with
  // the panels as they stood: the first window's choice was gone.
  const daemon = sharedFile();
  const a = await daemon.window().open();
  const b = await daemon.window().open();
  open(a, 'sess-2');
  a.el('toggle-right').click();
  await a.settle();

  open(b, 'sess-2');
  await b.settle();
  assert.equal(b.el('right-panel').classList.contains('collapsed'), true);
  assert.deepEqual(viewsOf(daemon)['sess-2'], ['right']);
});

test('a conversation deleted in one window does not come back with another window\'s change', async () => {
  const daemon = sharedFile();
  const a = await daemon.window().open();
  open(a, 'sess-2');
  await a.settle();
  const b = await daemon.window().open();
  await b.settle();

  a.forgetPanels('sess-2');
  await a.settle();
  assert.deepEqual(Object.keys(viewsOf(daemon)), ['sess-1']);
  b.el('toggle-right').click();
  await b.settle();
  assert.deepEqual(viewsOf(daemon), { 'sess-1': ['right'] });
});

test('a conversation deleted in a window that never had its panels is forgotten in the file', async () => {
  // Looked for in this page's copy, there was nothing to forget, and the
  // choice the other window wrote stayed in the file for good.
  const daemon = sharedFile();
  const a = await daemon.window().open();
  const b = await daemon.window().open();
  await a.settle();
  await b.settle();
  open(a, 'sess-2');
  a.el('toggle-right').click();
  await a.settle();
  assert.deepEqual(viewsOf(daemon)['sess-2'], ['right']);
  assert.equal(b.storage.get('localcode.panelViews').includes('sess-2'), false, 'the second window holds the conversation; this test reads the wrong thing');

  b.forgetPanels('sess-2');
  await b.settle();
  assert.deepEqual(Object.keys(viewsOf(daemon)), ['sess-1']);
});

test('at the limit the conversation forgotten is the oldest in the file, not the oldest a page knew', async () => {
  // The limit full, the conversation on screen among them so a page load
  // forgets nothing.
  const full = [['sess-1', []]];
  for (let i = 1; i < 200; i++) full.push([`old-${i}`, ['ssh']]);
  const daemon = sharedFile({ 'localcode.panelViews': JSON.stringify(full) });
  const a = await daemon.window().open();
  const b = await daemon.window().open();
  await a.settle();
  await b.settle();
  assert.equal(kept(daemon, 'localcode.panelViews').length, 200);
  assert.equal(kept(daemon, 'localcode.panelViews')[0][0], 'old-1');

  // The first window opens the one opened longest ago, which makes it
  // the newest. The second, which read the file before that, opens a
  // conversation nobody has opened.
  open(a, 'old-1');
  await a.settle();
  open(b, 'sess-2');
  await b.settle();
  const now = viewsOf(daemon);
  assert.equal(Object.keys(now).length, 200);
  assert.deepEqual(now['old-1'], ['ssh'], 'the conversation one window had just opened was forgotten by the other');
  assert.equal('old-2' in now, false, 'the oldest in the file was not the one forgotten');
  assert.deepEqual(now['sess-2'], []);
});

// A drag of the session panel's handle to 400 pixels.
function dragLeftTo400(app) {
  app.el('center').style.width = '4000px';
  app.el('left-panel').style.width = '260px';
  app.el('resize-left').fire('pointerdown', { button: 0, clientX: 260 });
  app.doc.fire('pointermove', { clientX: 400 });
  app.doc.fire('pointerup', { clientX: 400 });
}

test('a panel dragged in one window keeps its width when another window changes a different panel', async () => {
  const daemon = sharedFile();
  const a = await daemon.window().open();
  const b = await daemon.window().open();
  await a.settle();
  await b.settle();

  dragLeftTo400(a);
  await a.settle();
  b.el('toggle-left').click();
  await b.settle();
  let widths = kept(daemon, 'localcode.panelWidths');
  assert.equal(widths.left, 400, 'the width dragged in the first window was put back by the second');
  assert.equal(widths.leftCollapsed, true);

  // And a width put back to the stylesheet's in one window is not
  // brought back by the copy another window still holds.
  a.el('resize-left').fire('dblclick');
  await a.settle();
  b.el('toggle-left').click();
  await b.settle();
  widths = kept(daemon, 'localcode.panelWidths');
  assert.equal(widths.left, null, 'a width that was reset came back from the other window\'s copy');
  assert.equal(widths.leftCollapsed, false);
});

const GROUPED = [
  { id: 'sess-1', title: 'ungrouped', agent: 'general-purpose', workspace: '/tmp/workspace' },
  { id: 'g-work', title: 'work task', agent: 'general-purpose', workspace: '/tmp/workspace', group: 'work' },
  { id: 'g-home', title: 'home notes', agent: 'general-purpose', workspace: '/tmp/workspace', group: 'home' },
];
const withGroups = (names = ['work', 'home'], sessions = GROUPED) => ({
  routes: {
    'GET /api/sessions': sessions,
    'GET /api/sessions/groups': { names },
    'POST /api/sessions/groups': { status: 204 },
  },
});
const folds = (daemon) => kept(daemon, 'localcode.collapsedGroups');
const header = (app, name) => Array.from(app.el('session-list').querySelectorAll('.session-group-header'))
  .find((h) => h.querySelector('.group-name').textContent === name);
const shut = (app, name) => header(app, name).querySelector('.group-toggle').textContent === '▸';

test('a group folded in one window stays folded when another window folds a different group', async () => {
  const daemon = sharedFile();
  const a = await daemon.window(withGroups()).open();
  const b = await daemon.window(withGroups()).open();
  await a.settle();
  await b.settle();

  header(a, 'work').fire('click');
  await a.settle();
  header(b, 'home').fire('click');
  await b.settle();
  assert.deepEqual(folds(daemon).sort(), ['home', 'work']);

  // And one opened again in the first window is not folded again by the
  // copy the second holds, which has it folded by now.
  header(a, 'work').fire('click');
  await a.settle();
  assert.deepEqual(folds(daemon), ['home'], 'the first window starts from the file too, and keeps the second\'s fold');
  header(b, 'home').fire('click');
  await b.settle();
  assert.deepEqual(folds(daemon), [], 'a group opened in the first window was folded again by the second');
});

test('a click folds or opens the group as this window shows it, whatever another window did to it', async () => {
  const daemon = sharedFile();
  const a = await daemon.window(withGroups()).open();
  const b = await daemon.window(withGroups()).open();
  await a.settle();
  await b.settle();

  // Folded in the first. The second still shows it open, and a click on
  // it there is a click to fold it.
  header(a, 'work').fire('click');
  await a.settle();
  assert.equal(shut(b, 'work'), false);
  header(b, 'work').fire('click');
  await b.settle();
  assert.equal(shut(b, 'work'), true, 'the click changed nothing in the window it was made in');
  assert.deepEqual(folds(daemon), ['work']);
});

test('a fold another window made for a group this window lists is dropped when the group goes', async () => {
  // The fold reaches this page from the file, not from a click here. The
  // group is one it has been given, so the group gone from its list is a
  // group that was deleted, and the fold is not left behind.
  const daemon = sharedFile();
  const a = await daemon.window(withGroups()).open();
  const b = await daemon.window(withGroups()).open();
  await a.settle();
  await b.settle();
  header(a, 'work').fire('click');
  await a.settle();
  header(b, 'home').fire('click');
  await b.settle();
  assert.deepEqual(folds(daemon), ['work', 'home']);

  b.routes['GET /api/sessions/groups'] = { names: ['home'] };
  await b.loadSessions();
  await b.settle();
  assert.deepEqual(folds(daemon), ['home']);
});

test('a fold made in another window for a group this window was never given is left alone', async () => {
  // Each window has its own daemon, and a group made through one is not
  // known to the other until it starts again. The fold is in the file
  // they share.
  const daemon = sharedFile();
  const a = await daemon.window(withGroups(['work', 'home', 'later'], [
    ...GROUPED, { id: 'g-later', title: 'made later', agent: 'general-purpose', workspace: '/tmp/workspace', group: 'later' },
  ])).open();
  const b = await daemon.window(withGroups()).open();
  await a.settle();
  await b.settle();

  header(a, 'later').fire('click');
  await a.settle();
  header(b, 'home').fire('click');
  await b.settle();
  assert.deepEqual(folds(daemon).sort(), ['home', 'later'], 'the second window\'s click dropped a fold for a group it cannot see');
  // The second window's list is read again, still without the group.
  await b.loadSessions();
  await b.settle();
  assert.deepEqual(folds(daemon).sort(), ['home', 'later'], 'the second window\'s list dropped a fold for a group it cannot see');

  // Once it has been given the group, the fold is one it can judge: the
  // group gone from its list after that is a group that was deleted.
  b.routes['GET /api/sessions/groups'] = { names: ['work', 'home', 'later'] };
  await b.loadSessions();
  await b.settle();
  assert.deepEqual(folds(daemon).sort(), ['home', 'later']);
  b.routes['GET /api/sessions/groups'] = { names: ['work', 'home'] };
  await b.loadSessions();
  await b.settle();
  assert.deepEqual(folds(daemon), ['home']);
});

test('dropping the fold of a group that is gone leaves the folds another window made', async () => {
  // The second window holds a fold for a group deleted since, and drops
  // it when its list is read. Written from its own copy, that took the
  // first window's fold with it.
  const daemon = sharedFile({ 'localcode.collapsedGroups': JSON.stringify(['deleted since']) });
  const b = await daemon.window(withGroups(['work', 'home', 'deleted since'])).open();
  await b.settle();
  const a = await daemon.window(withGroups(['work', 'home', 'deleted since'])).open();
  await a.settle();

  header(a, 'work').fire('click');
  await a.settle();
  assert.deepEqual(folds(daemon), ['deleted since', 'work']);
  b.routes['GET /api/sessions/groups'] = { names: ['work', 'home'] };
  await b.loadSessions();
  await b.settle();
  assert.deepEqual(folds(daemon), ['work']);

  // With nothing to drop, a list read again does not ask for the file:
  // the list is read on every change to any conversation.
  const third = daemon.window(withGroups());
  const c = await third.open();
  await c.settle();
  const n = third.asked.length;
  await c.loadSessions();
  await c.settle();
  assert.equal(third.asked.length, n, 'every refresh of the session list asked for the file');
});

// Answers the in-page dialog that a group action opens.
async function answer(app, asking, text) {
  await app.settle();
  if (text !== undefined) app.el('prompt-input').value = text;
  app.el('prompt-ok').click();
  await asking;
  await app.settle();
}

test('a fold another window makes for a group this window deleted or renamed is left alone', async () => {
  // A daemon does not hear what is done to the groups through another:
  // the group this window deleted or renamed is still listed in the
  // other, which can fold it. That fold arrives here for a name this
  // page once listed, and it is still not a fold left behind.
  for (const [how, act, left] of [
    ['deleted', (b) => answer(b, b.internals.promptDeleteGroup('work')), ['home']],
    ['renamed', (b) => answer(b, b.internals.promptRenameGroup('work'), 'job'), ['job', 'home']],
  ]) {
    const daemon = sharedFile();
    const a = await daemon.window(withGroups()).open();
    const b = await daemon.window(withGroups()).open();
    await a.settle();
    await b.settle();
    b.routes['GET /api/sessions/groups'] = { names: left };
    await act(b);

    header(a, 'work').fire('click');
    await a.settle();
    header(b, 'home').fire('click');
    await b.settle();
    assert.deepEqual(folds(daemon).sort(), ['home', 'work'], `${how}: the click dropped the other window's fold`);
    await b.loadSessions();
    await b.settle();
    assert.deepEqual(folds(daemon).sort(), ['home', 'work'], `${how}: the list read again dropped the other window's fold`);
  }
});

test('folds a delete or a rename brings in from another window are drawn at once', async () => {
  // Both go on to wait for the session list. The panel draws this
  // page's copy and a click goes by it, so a copy changed and not drawn
  // made a click in that wait do the opposite of what the person saw.
  for (const [how, act] of [
    ['delete', (b) => b.internals.promptDeleteGroup('work')],
    ['rename', (b) => b.internals.promptRenameGroup('work')],
  ]) {
    const daemon = sharedFile();
    const a = await daemon.window(withGroups()).open();
    let hold = false;
    let release;
    const b = await daemon.window({
      routes: {
        ...withGroups().routes,
        'GET /api/sessions': () => (hold ? new Promise((resolve) => { release = () => resolve(GROUPED); }) : GROUPED),
      },
    }).open();
    await a.settle();
    await b.settle();
    header(a, 'home').fire('click');
    await a.settle();
    assert.equal(shut(b, 'home'), false);

    hold = true;
    const asking = act(b);
    await b.settle();
    if (how === 'rename') b.el('prompt-input').value = 'job';
    b.el('prompt-ok').click();
    await b.settle();
    assert.equal(typeof release, 'function', `${how}: the session list is not being waited for; this test reads the wrong thing`);
    assert.equal(shut(b, 'home'), true, `${how}: the other window's fold is in this page's copy and not on its panel`);

    // A click in that wait opens the group the panel shows folded.
    header(b, 'home').fire('click');
    await b.settle();
    assert.equal(shut(b, 'home'), false, `${how}: the click did not do what the panel showed`);
    hold = false;
    release();
    await asking;
    await b.settle();
    assert.equal(folds(daemon).includes('home'), false, `${how}: the group is open on the panel and folded in the file`);
  }

  // The same for a fold the other window took away: the panel shows the
  // group folded until the copy that says otherwise is drawn.
  const daemon = sharedFile({ 'localcode.collapsedGroups': JSON.stringify(['home']) });
  const a = await daemon.window(withGroups()).open();
  let hold = false;
  let release;
  const b = await daemon.window({
    routes: {
      ...withGroups().routes,
      'GET /api/sessions': () => (hold ? new Promise((resolve) => { release = () => resolve(GROUPED); }) : GROUPED),
    },
  }).open();
  await a.settle();
  await b.settle();
  assert.equal(shut(b, 'home'), true);
  header(a, 'home').fire('click');
  await a.settle();
  hold = true;
  const asking = b.internals.promptDeleteGroup('work');
  await b.settle();
  b.el('prompt-ok').click();
  await b.settle();
  assert.equal(shut(b, 'home'), false, 'a group the other window opened is still drawn folded');
  hold = false;
  release();
  await asking;

  // And for one fold exchanged for another, which leaves as many.
  const swapped = sharedFile({ 'localcode.collapsedGroups': JSON.stringify(['home']) });
  const first = await swapped.window(withGroups(['work', 'home', 'more'])).open();
  const second = await heldWindow(swapped, withGroups(['work', 'home', 'more']));
  await first.settle();
  first.internals.writeCollapsedGroups(new Set(['more']));
  await first.settle();
  second.hold = true;
  const waiting = second.app.internals.promptDeleteGroup('work');
  await second.app.settle();
  second.app.el('prompt-ok').click();
  await second.app.settle();
  assert.equal(shut(second.app, 'home'), false, 'one fold exchanged for another was not drawn');
  assert.equal(shut(second.app, 'more'), true);
  second.hold = false;
  second.release();
  await waiting;
});

// A window whose list of sessions can be held back, to stand in the wait
// a delete or a rename makes before the panel is drawn from the new list.
async function heldWindow(daemon, opts = withGroups()) {
  const held = { hold: false, release: null };
  const sessions = opts.routes['GET /api/sessions'];
  held.app = await daemon.window({
    routes: {
      ...opts.routes,
      'GET /api/sessions': () => (held.hold ? new Promise((resolve) => { held.release = () => resolve(sessions); }) : sessions),
    },
  }).open();
  await held.app.settle();
  return held;
}

test('a click made while a delete or a rename waits judges folds by the list as it is, not as it was drawn', async () => {
  // A header keeps the list it was drawn with. The group this window has
  // just deleted or renamed is in that list and not in the window's own
  // any more, and the other window, which still lists it, folds it.
  for (const [how, act, left] of [
    ['delete', (b) => b.internals.promptDeleteGroup('work'), ['home']],
    ['rename', (b) => b.internals.promptRenameGroup('work'), ['job', 'home']],
  ]) {
    const daemon = sharedFile();
    const a = await daemon.window(withGroups()).open();
    const win = await heldWindow(daemon);
    const b = win.app;
    await a.settle();

    b.routes['GET /api/sessions/groups'] = { names: left };
    win.hold = true;
    const asking = act(b);
    await b.settle();
    if (how === 'rename') b.el('prompt-input').value = 'job';
    b.el('prompt-ok').click();
    await b.settle();
    assert.equal(typeof win.release, 'function', `${how}: the session list is not being waited for; this test reads the wrong thing`);

    header(a, 'work').fire('click');
    await a.settle();
    header(b, 'home').fire('click');
    await b.settle();
    win.hold = false;
    win.release();
    await asking;
    await b.settle();
    assert.deepEqual(folds(daemon).sort(), ['home', 'work'], `${how}: the other window's fold was dropped when the list arrived`);
  }
});

test('a group renamed here is this window\'s to judge under its new name, even when the list cannot be read after', async () => {
  // The new name was another window's fold of a group this one could not
  // see. The rename reads the folds with the old list, and the read of
  // the list that would say the new name is listed fails.
  const daemon = sharedFile();
  const a = await daemon.window(withGroups(['work', 'home', 'job'], [
    ...GROUPED, { id: 'g-job', title: 'the other job', agent: 'general-purpose', workspace: '/tmp/workspace', group: 'job' },
  ])).open();
  const b = await daemon.window(withGroups()).open();
  await a.settle();
  await b.settle();
  header(a, 'job').fire('click');
  await a.settle();

  b.routes['GET /api/sessions/groups'] = { networkError: 'connection refused' };
  await answer(b, b.internals.promptRenameGroup('work'), 'job');
  assert.deepEqual(folds(daemon), ['job']);
  b.routes['GET /api/sessions/groups'] = { names: ['home'] };
  await b.loadSessions();
  await b.settle();
  assert.deepEqual(folds(daemon), []);
});

test('a group made here is this window\'s to judge from then on, whoever folded the name before', async () => {
  // The name was another window's fold of a group this one could not
  // see. Made here, it is listed here, and gone from this list later it
  // is a group that was deleted.
  const daemon = sharedFile();
  const a = await daemon.window(withGroups(['work', 'home', 'temp'], [
    ...GROUPED, { id: 'g-temp', title: 'for now', agent: 'general-purpose', workspace: '/tmp/workspace', group: 'temp' },
  ])).open();
  const b = await daemon.window(withGroups()).open();
  await a.settle();
  await b.settle();
  header(a, 'temp').fire('click');
  await a.settle();
  header(b, 'home').fire('click');
  await b.settle();

  await answer(b, b.internals.promptCreateGroup(), 'temp');
  assert.deepEqual(folds(daemon).sort(), ['home', 'temp']);
  // Deleted through this window's daemon by something else, with no
  // read of the list in between.
  b.routes['GET /api/sessions/groups'] = { names: ['work', 'home'] };
  await b.loadSessions();
  await b.settle();
  assert.deepEqual(folds(daemon), ['home']);
});

test('the panel a delete draws while it waits is in the order its sessions are in', async () => {
  // Reading the folds can draw the panel in the middle of a delete. The
  // sessions are still in the order of the list from before it, so that
  // is the list drawn: a drag lands by the order of the sessions.
  const sessions = [GROUPED[0], GROUPED[2], GROUPED[1]];
  const groups = ['home', 'work', 'aside'];
  const daemon = sharedFile();
  const a = await daemon.window(withGroups(groups, sessions)).open();
  const win = await heldWindow(daemon, withGroups(groups, sessions));
  const b = win.app;
  await a.settle();
  // A fold of a group with nothing in it: it changes this page's copy,
  // which is what draws the panel, and hides no row.
  header(a, 'aside').fire('click');
  await a.settle();
  const rows = () => Array.from(b.el('session-list').querySelectorAll('.session-item'), (el) => el.title.split('\n')[0]);
  assert.deepEqual(rows(), ['sess-1', 'g-home', 'g-work']);
  assert.equal(shut(b, 'aside'), false);

  b.routes['GET /api/sessions/groups'] = { names: ['home', 'aside'] };
  win.hold = true;
  const asking = b.internals.promptDeleteGroup('work');
  await b.settle();
  b.el('prompt-ok').click();
  await b.settle();
  assert.equal(typeof win.release, 'function');
  assert.equal(shut(b, 'aside'), true, 'the delete did not draw the panel; this test reads the wrong thing');
  assert.deepEqual(rows(), b.app.sessions.map((one) => one.id), 'the panel was drawn in an order its sessions are not in');
  win.hold = false;
  win.release();
  await asking;
});

test('with storage that refuses writes, a fold another window makes is still told from this window\'s own', async () => {
  // The storage goes on answering with a fold this window has since
  // dropped. Read as this window's copy, the same fold made again in the
  // other window looked like one this window already had.
  for (const [how, act, left] of [
    ['delete', (b) => answer(b, b.internals.promptDeleteGroup('work')), ['home']],
    ['rename', (b) => answer(b, b.internals.promptRenameGroup('work'), 'job'), ['job', 'home']],
  ]) {
    const daemon = sharedFile({ 'localcode.collapsedGroups': JSON.stringify(['work']) });
    const a = await daemon.window(withGroups()).open();
    const b = await daemon.window(withGroups()).open();
    await a.settle();
    await b.settle();
    b.storage.set = () => { throw new Error('QuotaExceededError'); };
    b.storage.delete = () => { throw new Error('QuotaExceededError'); };
    b.routes['GET /api/sessions/groups'] = { names: left };
    await act(b);
    assert.equal(folds(daemon).includes('work'), false);

    // The other window, which still lists the group, folds it again: it
    // shows the group folded, so one click to open and one to fold.
    header(a, 'work').fire('click');
    await a.settle();
    header(a, 'work').fire('click');
    await a.settle();
    assert.equal(folds(daemon).includes('work'), true, `${how}: the other window did not fold the group; this test reads the wrong thing`);
    await b.loadSessions();
    await b.settle();
    assert.equal(folds(daemon).includes('work'), true, `${how}: the other window's fold was dropped`);
  }
});

test('a fold read from the file for a group this window lists is its own to drop, whatever read brought it', async () => {
  // A rename reads the file too. The fold it brings in for another group
  // this window lists is not another window's to keep: when that group
  // goes from this window's list, the fold goes. Here the list cannot be
  // read right after the rename, so nothing else has judged the fold.
  const daemon = sharedFile();
  const a = await daemon.window(withGroups(['work', 'home', 'extra'])).open();
  const b = await daemon.window(withGroups(['work', 'home', 'extra'])).open();
  await a.settle();
  await b.settle();
  a.app.sessionGroups = ['work', 'home', 'extra'];
  a.internals.writeCollapsedGroups(new Set(['extra']));
  await a.settle();
  assert.deepEqual(folds(daemon), ['extra']);

  b.routes['GET /api/sessions/groups'] = { networkError: 'connection refused' };
  await answer(b, b.internals.promptRenameGroup('work'), 'job');
  assert.equal(b.storage.get('localcode.collapsedGroups'), JSON.stringify(['extra']), 'the rename did not read the file; this test reads the wrong thing');

  b.routes['GET /api/sessions/groups'] = { names: ['job', 'home'] };
  await b.loadSessions();
  await b.settle();
  assert.deepEqual(folds(daemon), []);
});

test('a group made in a window starts as the folds are kept now, not as an older copy has them', async () => {
  // The first window folds a group of its own and deletes it. The second
  // took that fold into its copy with a change in between, and then makes
  // a group of the same name.
  const daemon = sharedFile();
  const a = await daemon.window(withGroups(['work', 'home', 'temp'], [
    ...GROUPED, { id: 'g-temp', title: 'for now', agent: 'general-purpose', workspace: '/tmp/workspace', group: 'temp' },
  ])).open();
  const b = await daemon.window(withGroups()).open();
  await a.settle();
  await b.settle();
  header(a, 'temp').fire('click');
  await a.settle();
  header(b, 'home').fire('click');
  await b.settle();
  a.routes['GET /api/sessions/groups'] = { names: ['work', 'home'] };
  await answer(a, a.internals.promptDeleteGroup('temp'));
  assert.deepEqual(folds(daemon), ['home']);

  b.routes['GET /api/sessions/groups'] = { names: ['work', 'home', 'temp'] };
  await answer(b, b.internals.promptCreateGroup(), 'temp');
  assert.equal(shut(b, 'temp'), false, 'a group made a moment ago was drawn folded shut');
  assert.equal(shut(b, 'home'), true);
  assert.deepEqual(folds(daemon), ['home']);
});

test('a group deleted or renamed in one window takes the fold another window made', async () => {
  const daemon = sharedFile();
  const a = await daemon.window(withGroups()).open();
  const b = await daemon.window(withGroups()).open();
  const c = await daemon.window(withGroups()).open();
  await a.settle();
  await b.settle();
  await c.settle();
  header(a, 'work').fire('click');
  header(a, 'home').fire('click');
  await a.settle();
  assert.deepEqual(folds(daemon).sort(), ['home', 'work']);

  // Deleted in the second, whose own copy has neither folded: nothing in
  // it says there is a fold to drop.
  b.routes['GET /api/sessions/groups'] = { names: ['work'] };
  const deleted = b.internals.promptDeleteGroup('home');
  await b.settle();
  b.el('prompt-ok').click();
  await deleted;
  await b.settle();
  assert.deepEqual(folds(daemon), ['work'], 'the fold of a deleted group stayed in the file');

  // And renamed in a third, whose own copy has nothing folded either.
  c.routes['GET /api/sessions/groups'] = { names: ['job'] };
  const renamed = c.internals.promptRenameGroup('work');
  await c.settle();
  c.el('prompt-input').value = 'job';
  c.el('prompt-ok').click();
  await renamed;
  await c.settle();
  assert.deepEqual(folds(daemon), ['job'], 'the fold did not go to the new name');
});

// The same claim, without choosing the cases: three windows, random
// changes, one window at a time as one person makes them. After every
// step, each item the step did not name is in the file as it was before
// the step, and at the end a page that starts fresh holds what the file
// holds. Before the page read the file again, every sequence failed this
// within a few steps, on all three values.
test('whatever three windows change, in any order, an item nobody changed stays as it was', async () => {
  const GROUPS = ['work', 'home'];
  const CONVS = ['sess-1', 'c-a', 'c-b', 'c-c'];
  // The file as items: one a conversation, one a width or a flag, one a group.
  const items = (keptNow) => {
    const out = new Map();
    const k = keptNow || {};
    for (const [id, hidden] of JSON.parse(k['localcode.panelViews'] || '[]')) out.set(`view:${id}`, JSON.stringify([...hidden].sort()));
    for (const [n, v] of Object.entries(JSON.parse(k['localcode.panelWidths'] || '{}'))) {
      if (v !== null && v !== false) out.set(`width:${n}`, JSON.stringify(v));
    }
    for (const g of JSON.parse(k['localcode.collapsedGroups'] || '[]')) out.set(`fold:${g}`, 'true');
    return out;
  };
  // Seeded, so a failure names a sequence that fails again.
  const random = (seed) => {
    let a = seed >>> 0;
    return () => {
      a = (a + 0x6D2B79F5) | 0;
      let t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  };

  for (let run = 0; run < 6; run++) {
    const r = random(7000 + run);
    const pick = (list) => list[Math.floor(r() * list.length)];
    const daemon = sharedFile();
    const wins = [];
    for (let i = 0; i < 3; i++) {
      const win = daemon.window(withGroups(GROUPS));
      const app = await win.open();
      await app.settle();
      wins.push({ win, app, conv: 'sess-1' });
    }
    const steps = [];
    for (let step = 0; step < 25; step++) {
      const w = pick(wins);
      const before = items(daemon.file.kept);
      const named = new Set();
      const kind = pick(['right', 'right', 'left', 'open', 'open', 'drag', 'reset', 'fold', 'fold', 'forget', 'reload']);
      let what = kind;
      if (kind === 'right' || kind === 'left') {
        w.app.el(`toggle-${kind}`).click();
        named.add(`width:${kind}Collapsed`).add(`view:${w.conv}`);
      } else if (kind === 'open') {
        w.conv = pick(CONVS);
        what += ` ${w.conv}`;
        open(w.app, w.conv);
        named.add(`view:${w.conv}`).add('width:browserCollapsed').add('width:sshCollapsed').add('width:rightCollapsed');
      } else if (kind === 'drag') {
        if (w.app.el('left-panel').classList.contains('collapsed')) continue;
        dragLeftTo400(w.app);
        named.add('width:left');
      } else if (kind === 'reset') {
        w.app.el('resize-left').fire('dblclick');
        named.add('width:left');
      } else if (kind === 'fold') {
        const g = pick(GROUPS);
        what += ` ${g}`;
        header(w.app, g).fire('click');
        named.add(`fold:${g}`);
      } else if (kind === 'forget') {
        const c = pick(CONVS.filter((id) => id !== w.conv));
        what += ` ${c}`;
        w.app.forgetPanels(c);
        named.add(`view:${c}`);
      } else {
        // The page loaded again, on the same address.
        await w.app.settle();
        w.win.sync();
        w.app = await w.win.open();
        w.conv = 'sess-1';
        named.add('view:sess-1').add('width:browserCollapsed').add('width:sshCollapsed').add('width:rightCollapsed');
      }
      await w.app.settle();
      steps.push(`window ${wins.indexOf(w)} ${what}`);
      const after = items(daemon.file.kept);
      for (const item of new Set([...before.keys(), ...after.keys()])) {
        if (named.has(item)) continue;
        assert.equal(after.get(item), before.get(item), `run ${run}: ${item} changed in a step that did not name it. Steps: ${steps.join(', ')}`);
      }
    }
    const fresh = await daemon.window(withGroups(GROUPS)).open();
    await fresh.settle();
    assert.deepEqual([...items(Object.fromEntries(fresh.storage))].sort(), [...items(daemon.file.kept)].sort(), `run ${run}: a fresh page does not hold what the file holds`);
  }
});

// ---- the read that starts such a change (readShared in js/prefs.js)

test('a browser tab asks the daemon for nothing before a change', async () => {
  // Tabs share one localStorage, which is already the newest copy.
  const asked = [];
  class Ask {
    open(...args) { asked.push(args); }
    send() { throw new Error('a browser tab made the request'); }
  }
  const app = await load({ globals: { XMLHttpRequest: Ask }, localStorage: { 'localcode.panelWidths': JSON.stringify({ left: 300 }) } });
  app.el('toggle-right').click();
  open(app, 'sess-2');
  await app.settle();
  assert.deepEqual(asked, []);
  assert.equal(app.readShared('localcode.panelWidths'), app.storage.get('localcode.panelWidths'));
  assert.deepEqual(JSON.parse(app.storage.get('localcode.panelWidths')), { left: 300, rightCollapsed: true });
  assert.deepEqual(writes(app), []);
});

test('a window that was not given the file at the start asks for nothing before a change', async () => {
  // js/kept.js got no answer, so this page writes nothing to the file,
  // and has no use for what the file holds.
  const asked = [];
  class Ask {
    open(...args) { asked.push(args); }
    send() { throw new Error('a page that writes nothing made the request'); }
  }
  const app = await load({ globals: { lcGUI: true, XMLHttpRequest: Ask }, routes: { [`PUT ${ROUTE}`]: { status: 204 } } });
  app.el('toggle-right').click();
  await app.settle();
  assert.deepEqual(asked, []);
  assert.deepEqual(writes(app), []);
  assert.equal(JSON.parse(app.storage.get('localcode.panelWidths')).rightCollapsed, true);
});

test('a change asks for the file once, however many values it changes', async () => {
  // A toggle writes the window's own record and the conversation's.
  const daemon = sharedFile();
  const win = daemon.window();
  const app = await win.open();
  await app.settle();
  const n = win.asked.length;
  app.el('toggle-right').click();
  assert.equal(win.asked.length - n, 1);
  assert.deepEqual(win.asked[n], { method: 'GET', url: ROUTE, isAsync: false }, 'the answer has to be in hand before the change is made');
  await app.settle();
  // The next change is another turn, and asks again.
  app.el('toggle-right').click();
  assert.equal(win.asked.length - n, 2);
});

test('a change still on its way to the daemon is not undone by the file it has not reached', async () => {
  // The first change is with the daemon, and the file does not have it.
  // A second change to another item of the same value starts from this
  // page's copy: started from the file's, it would drop the first.
  const daemon = sharedFile({ 'localcode.panelWidths': JSON.stringify({ left: 300 }) });
  let hold = false;
  const held = [];
  const win = daemon.window({
    routes: {
      [`PUT ${ROUTE}`]: (body) => (hold
        ? new Promise((resolve) => { held.push(() => resolve(daemon.put(body))); })
        : daemon.put(body)),
    },
  });
  const app = await win.open();
  await app.settle();

  hold = true;
  app.el('toggle-right').click();
  await app.settle();
  assert.equal(held.length, 1);
  assert.equal(kept(daemon, 'localcode.panelWidths').rightCollapsed, undefined, 'the first change is not held back; this test reads the wrong thing');
  const askedBefore = win.asked.length;
  dragLeftTo400(app);
  await app.settle();
  assert.equal(win.asked.length, askedBefore, 'the file was asked for under a name this page is ahead of it on');
  // The first is taken and the second, made meanwhile, goes out and is
  // held in its turn. The file has the first only, so a third change
  // still starts from this page's copy.
  held.shift()();
  await app.settle();
  assert.equal(held.length, 1);
  assert.equal(kept(daemon, 'localcode.panelWidths').left, 300, 'the second change is not held back; this test reads the wrong thing');
  app.el('toggle-left').click();
  await app.settle();
  hold = false;
  while (held.length) held.shift()();
  await app.settle();
  const widths = kept(daemon, 'localcode.panelWidths');
  assert.equal(widths.rightCollapsed, true, 'the change that was on its way was undone by the one after it');
  assert.equal(widths.left, 400, 'a change made while another was on its way was undone by the one after it');
  assert.equal(widths.leftCollapsed, true);

  // Taken by the daemon, the name is the file's to answer for again.
  daemon.put({ 'localcode.panelWidths': JSON.stringify({ ...widths, ssh: 500 }) });
  app.el('toggle-right').click();
  await app.settle();
  assert.equal(kept(daemon, 'localcode.panelWidths').ssh, 500);
  assert.equal(kept(daemon, 'localcode.panelWidths').rightCollapsed, false);
});

test('a change the daemon refused stays in the page, which goes on from its own copy', async () => {
  // Nothing the page writes is refused today. If something were, the
  // file would never have it, and a page that went back to the file's
  // copy before each change would forget each one as it made the next.
  const daemon = sharedFile({ 'localcode.panelWidths': JSON.stringify({ left: 300 }) });
  let refuse = false;
  const win = daemon.window({
    routes: { [`PUT ${ROUTE}`]: (body) => (refuse ? { status: 413, body: { error: 'too large' } } : daemon.put(body)) },
  });
  const app = await win.open();
  await app.settle();
  refuse = true;
  app.el('toggle-right').click();
  await app.settle();
  dragLeftTo400(app);
  await app.settle();
  const mine = JSON.parse(app.storage.get('localcode.panelWidths'));
  assert.equal(mine.rightCollapsed, true);
  assert.equal(mine.left, 400);
});

test('what a page is handing over is not replaced by a file another window began meanwhile', async () => {
  // The page found nothing kept and is sending what its address holds.
  // Before that is taken, another window writes the first setting, and
  // the file is there without any of this page's.
  const daemon = sharedFile();
  let hold = true;
  const held = [];
  const win = daemon.window({
    routes: {
      [`PUT ${ROUTE}`]: (body) => (hold
        ? new Promise((resolve) => { held.push(() => resolve(daemon.put(body))); })
        : daemon.put(body)),
    },
  });
  win.stored.set('localcode.panelWidths', JSON.stringify({ left: 300 }));
  const app = await win.open();
  await app.settle();
  assert.equal(held.length, 1, 'nothing was handed over; this test reads the wrong thing');
  daemon.put({ 'localcode.zoom': '1.1' });

  app.el('toggle-right').click();
  await app.settle();
  hold = false;
  while (held.length) held.shift()();
  await app.settle();
  const widths = kept(daemon, 'localcode.panelWidths');
  assert.equal(widths.left, 300, 'a width the page was handing over was dropped for a file that did not have it yet');
  assert.equal(widths.rightCollapsed, true);
});

test('a change the daemon stopped being asked to take is still the page\'s, and the file\'s again once taken', async () => {
  // Six tries and the page stops. What was not taken waits for the next
  // change, and until it is taken the file is the older of the two.
  const daemon = sharedFile({ 'localcode.panelWidths': JSON.stringify({ left: 300 }) });
  let down = false;
  const win = daemon.window({
    globals: instantTimers(),
    routes: { [`PUT ${ROUTE}`]: (body) => (down ? { status: 500, body: { error: 'could not write' } } : daemon.put(body)) },
  });
  const app = await win.open();
  await app.settle();
  down = true;
  const before = writes(app).length;
  app.el('toggle-right').click();
  await app.settle();
  assert.ok(writes(app).length - before >= 6, 'the write was not tried to the end; this test reads the wrong thing');
  assert.equal(kept(daemon, 'localcode.panelWidths').rightCollapsed, undefined);

  // Another window writes meanwhile. This page's next change still starts
  // from its own copy, which has the change the daemon never took.
  daemon.put({ 'localcode.panelWidths': JSON.stringify({ left: 300, ssh: 500 }) });
  down = false;
  dragLeftTo400(app);
  await app.settle();
  let widths = kept(daemon, 'localcode.panelWidths');
  assert.equal(widths.rightCollapsed, true, 'the change that was never taken was dropped by the one after it');
  assert.equal(widths.left, 400);

  // Taken now, so the next change starts from the file.
  daemon.put({ 'localcode.panelWidths': JSON.stringify({ ...widths, ssh: 500 }) });
  app.el('toggle-left').click();
  await app.settle();
  widths = kept(daemon, 'localcode.panelWidths');
  assert.equal(widths.ssh, 500, 'the page went on from its own copy after its change was taken');
  assert.equal(widths.leftCollapsed, true);
});

test('a change sent plainly after the browser refused it, or taken after a refusal, is the file\'s again', async () => {
  for (const how of ['the browser would not send it to outlive the page', 'the daemon refused it once']) {
    const daemon = sharedFile({ 'localcode.panelWidths': JSON.stringify({ left: 300 }) });
    let trouble = false;
    const win = daemon.window({
      globals: instantTimers(),
      routes: {
        [`PUT ${ROUTE}`]: (body) => {
          if (!trouble) return daemon.put(body);
          if (how.startsWith('the daemon')) { trouble = false; return { status: 413, body: { error: 'too large' } }; }
          const call = win.app.calls[win.app.calls.length - 1];
          return call.keepalive ? { networkError: 'TypeError: Failed to fetch' } : daemon.put(body);
        },
      },
    });
    win.app = await win.open();
    const app = win.app;
    await app.settle();
    trouble = true;
    app.el('toggle-right').click();
    await app.settle();
    if (how.startsWith('the daemon')) {
      // Refused, so the file does not have it. The next change is taken.
      assert.equal(kept(daemon, 'localcode.panelWidths').rightCollapsed, undefined, how);
      app.el('toggle-right').click();
      await app.settle();
    }
    trouble = false;
    const mine = kept(daemon, 'localcode.panelWidths');
    daemon.put({ 'localcode.panelWidths': JSON.stringify({ ...mine, ssh: 500 }) });
    dragLeftTo400(app);
    await app.settle();
    assert.equal(kept(daemon, 'localcode.panelWidths').ssh, 500, `${how}: the page went on from its own copy after its change was taken`);
    assert.equal(kept(daemon, 'localcode.panelWidths').left, 400, how);
  }
});

test('with storage that refuses writes, a change back to the value it still holds reaches the file', async () => {
  // The storage answers with what it held before it began refusing. A
  // change to that same value was compared with it, taken for no change,
  // and never sent, though the file had moved on.
  const daemon = sharedFile({ 'localcode.panelWidths': JSON.stringify({ left: 400 }) });
  const a = await daemon.window().open();
  const b = await daemon.window().open();
  await a.settle();
  await b.settle();
  b.storage.set = () => { throw new Error('QuotaExceededError'); };

  a.el('resize-left').fire('dblclick');
  await a.settle();
  assert.equal(kept(daemon, 'localcode.panelWidths').left, null);
  dragLeftTo400(b);
  await b.settle();
  assert.equal(kept(daemon, 'localcode.panelWidths').left, 400);
});

test('with storage that refuses writes, two changes in one turn still keep another window\'s item', async () => {
  // The click drops the fold of a group that is gone and then folds the
  // group clicked: two writes of one value. The second goes on from what
  // the first sent, not from what the storage would not take.
  const daemon = sharedFile({ 'localcode.collapsedGroups': JSON.stringify(['old']) });
  const a = await daemon.window(withGroups(['work', 'home', 'old'])).open();
  const b = await daemon.window(withGroups(['work', 'home', 'old'])).open();
  await a.settle();
  await b.settle();
  header(a, 'work').fire('click');
  await a.settle();

  b.routes['GET /api/sessions/groups'] = { names: ['work', 'home'] };
  b.storage.set = () => { throw new Error('QuotaExceededError'); };
  b.app.sessionGroups = ['work', 'home'];
  b.renderSessionList();
  header(b, 'home').fire('click');
  await b.settle();
  assert.deepEqual(folds(daemon).sort(), ['home', 'work']);
});

test('with storage that refuses writes and no answer from the daemon, a change still starts from what the page last sent', async () => {
  const daemon = sharedFile({ 'localcode.panelWidths': JSON.stringify({ left: 300 }) });
  const win = daemon.window();
  const app = await win.open();
  await app.settle();
  app.storage.set = () => { throw new Error('QuotaExceededError'); };
  dragLeftTo400(app);
  await app.settle();
  assert.equal(kept(daemon, 'localcode.panelWidths').left, 400);

  win.answer = () => 500;
  app.el('toggle-right').click();
  await app.settle();
  const widths = kept(daemon, 'localcode.panelWidths');
  assert.equal(widths.left, 400, 'the width this page had sent was put back to what its storage still held');
  assert.equal(widths.rightCollapsed, true);
});

test('with no answer from the daemon a change is made from what the page holds, and still written', async () => {
  for (const [name, answer] of Object.entries({
    'a request that throws': () => { throw new Error('NetworkError'); },
    'a daemon that failed': 500,
    'a refusal': 403,
    'a body that is not JSON': '<html>',
    'a body that is JSON and not an object': 'null',
    'a daemon that keeps nothing': { available: false, kept: null },
    'a daemon that keeps nothing and sends something': { available: false, kept: { 'localcode.panelWidths': '{"left":1}' } },
    'a failure with a body that reads as an answer': (xhr) => {
      xhr.status = 500;
      xhr.responseText = JSON.stringify({ available: true, kept: { 'localcode.panelWidths': '{"left":1}' } });
    },
    'a file not written yet': { available: true, kept: null },
    'an array': { available: true, kept: ['localcode.panelWidths'] },
    'a number': { available: true, kept: 42 },
    'a string': { available: true, kept: 'oops' },
    'true': { available: true, kept: true },
    'a value that is not a string': { available: true, kept: { 'localcode.panelWidths': { left: 1 }, 'localcode.panelViews': 7 } },
  })) {
    const daemon = sharedFile({ 'localcode.panelWidths': JSON.stringify({ left: 300 }) });
    const win = daemon.window();
    const app = await win.open();
    await app.settle();
    const n = win.asked.length;
    win.answer = () => answer;
    app.el('toggle-right').click();
    await app.settle();
    assert.equal(win.asked.length - n, 1, `${name}: asked again in the same turn`);
    const widths = kept(daemon, 'localcode.panelWidths');
    assert.equal(widths.left, 300, `${name}: the page's own copy was not what the change started from`);
    assert.equal(widths.rightCollapsed, true, `${name}: the change was not written`);
    assert.deepEqual(viewsOf(daemon)['sess-1'], ['right'], `${name}: the conversation's panels were not written`);
  }
});

test('a name the file does not have is not brought back by the copy a page holds', async () => {
  // The rule js/kept.js has at the start of a page: the file is the
  // record, and a name it does not have is one that was forgotten.
  const daemon = sharedFile();
  const win = daemon.window(withGroups());
  const app = await win.open();
  await app.settle();
  header(app, 'work').fire('click');
  await app.settle();
  assert.deepEqual(folds(daemon), ['work']);

  daemon.put({ 'localcode.collapsedGroups': null });
  assert.equal(app.readShared('localcode.collapsedGroups'), null);
  assert.equal(app.storage.has('localcode.collapsedGroups'), false);
  header(app, 'home').fire('click');
  await app.settle();
  assert.deepEqual(folds(daemon), ['home']);
});

test('the read that starts a change takes the file\'s value even when storage refuses it', async () => {
  const daemon = sharedFile({ 'localcode.collapsedGroups': JSON.stringify(['work']) });
  const win = daemon.window();
  const app = await win.open();
  await app.settle();
  // With storage that takes it, the page's copy is the file's afterwards:
  // it is what the panel draws.
  daemon.put({ 'localcode.collapsedGroups': JSON.stringify(['home']) });
  assert.equal(app.readShared('localcode.collapsedGroups'), JSON.stringify(['home']));
  assert.equal(app.storage.get('localcode.collapsedGroups'), JSON.stringify(['home']));
  await app.settle();

  daemon.put({ 'localcode.collapsedGroups': JSON.stringify(['work', 'home']) });
  app.storage.set = () => { throw new Error('QuotaExceededError'); };
  assert.equal(app.readShared('localcode.collapsedGroups'), JSON.stringify(['work', 'home']));
  // The page goes on from that value, which its storage does not have.
  assert.equal(app.readPref('localcode.collapsedGroups'), JSON.stringify(['work', 'home']));
  // And asks for nothing under a name that is not the page's own.
  const n = win.asked.length;
  await app.settle();
  assert.equal(app.readShared('smart_agent'), null);
  assert.equal(win.asked.length, n);

  // Storage that takes writes again answers for itself again.
  delete app.storage.set;
  daemon.put({ 'localcode.collapsedGroups': JSON.stringify(['home']) });
  assert.equal(app.readShared('localcode.collapsedGroups'), JSON.stringify(['home']));
  assert.equal(app.readPref('localcode.collapsedGroups'), JSON.stringify(['home']));
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
