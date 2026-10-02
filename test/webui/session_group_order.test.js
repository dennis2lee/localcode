'use strict';

// Reordering by drag inside a group. The panel keeps one flat list in
// drawn order (ungrouped rows, then each group in turn), so a drag inside
// a group is the same gesture as a drag anywhere else: the card takes the
// target row's place and the daemon is told the whole order. These tests
// pin that for rows that share a group, the line that shows where the
// card will land, and what a drag does and does not tolerate while it is
// in progress.

const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function transfer() {
  const data = new Map();
  return {
    effectAllowed: '',
    dropEffect: '',
    setData: (k, v) => data.set(k, v),
    getData: (k) => data.get(k) || '',
  };
}

function row(id, group) {
  const s = { id, title: id, agent: 'general-purpose', workspace: '/w' };
  if (group) s.group = group;
  return s;
}

// u above the groups, g = [a, b, c], h = [d, e].
const PANEL = [row('u'), row('a', 'g'), row('b', 'g'), row('c', 'g'), row('d', 'h'), row('e', 'h')];

function routesFor(sessions, extra = {}) {
  return {
    'GET /api/sessions': sessions,
    'GET /api/sessions/groups': { names: ['g', 'h'] },
    'POST /api/sessions/order': { status: 204 },
    'POST /api/sessions/*/group': { status: 200 },
    ...extra,
  };
}

// The rows on screen, by id, with each group's header named after it.
function shown(app) {
  return (app.el('session-list').children || []).map((el) => {
    if (el.classList.contains('session-group-header')) return `[${el.querySelector('.group-name').textContent}]`;
    return el.title.split('\n')[0];
  });
}

// Just the session rows, in the order they are drawn.
function rowIDs(app) {
  return (app.el('session-list').children || [])
    .filter((el) => el.classList.contains('session-item'))
    .map((el) => el.title.split('\n')[0]);
}

function card(app, id) {
  const el = (app.el('session-list').children || []).find((c) => c.title && c.title.split('\n')[0] === id);
  assert.ok(el, `no row for ${id}`);
  return el;
}

function header(app, name) {
  const el = (app.el('session-list').children || []).find(
    (c) => c.classList.contains('session-group-header') && c.querySelector('.group-name').textContent === name,
  );
  assert.ok(el, `no header for ${name}`);
  return el;
}

function ledOf(app, id) {
  const led = card(app, id).querySelector('.session-led');
  return led ? led.className.replace('session-led ', '') : null;
}

function start(app, id) {
  card(app, id).fire('dragstart', { dataTransfer: transfer() });
}

function drag(app, fromID, toID) {
  start(app, fromID);
  const target = card(app, toID);
  target.fire('dragover', { dataTransfer: transfer() });
  target.fire('drop', { dataTransfer: transfer() });
}

function emit(app, type, data) {
  app.sse.emit({ seq: 1, type, data });
}

function sameRows(app, before) {
  const now = app.el('session-list').children;
  return now.length === before.length && now.every((el, i) => el === before[i]);
}

// A daemon that remembers what it was told, the way SetOrder and
// SetSessionGroup do, and lists sessions in the order it holds. A held
// promise makes a request wait, so a test can decide when it lands.
function fakeDaemon(initial) {
  const sessions = initial.map((s) => ({ ...s }));
  const d = {
    saved: null,
    holdOrder: null,
    holdGroup: null,
    holdNextGet: null,
    listing() {
      if (!d.saved) return sessions.map((s) => ({ ...s }));
      const pos = new Map(d.saved.map((id, i) => [id, i]));
      return sessions.map((s) => ({ ...s })).sort((a, b) => pos.get(a.id) - pos.get(b.id));
    },
  };
  d.routes = routesFor(null, {
    'GET /api/sessions': async () => {
      const answer = d.listing();
      if (d.holdNextGet) {
        const hold = d.holdNextGet;
        d.holdNextGet = null;
        await hold;
      }
      return answer;
    },
    'POST /api/sessions/order': async (body) => {
      if (d.holdOrder) await d.holdOrder;
      // One hold per request, in the order they arrive, for a test that
      // needs the first save to land while a later one is still waiting.
      const hold = d.orderHolds && d.orderHolds.shift();
      if (hold) await hold;
      d.saved = body.ids;
      return { status: 204 };
    },
    'POST /api/sessions/*/group': async (body, { path }) => {
      if (d.holdGroup) await d.holdGroup;
      sessions.find((s) => s.id === path.split('/')[3]).group = body.group;
      return { status: 200 };
    },
  });
  return d;
}

function gate() {
  let open;
  const promise = new Promise((resolve) => { open = resolve; });
  return { promise, open };
}

// ---- the move itself ------------------------------------------------

test('dragging a card up inside a group moves it above the row it was dropped on', async () => {
  const app = await load({ routes: routesFor(PANEL) });
  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', 'c', '[h]', 'd', 'e']);

  drag(app, 'c', 'a');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'c', 'a', 'b', '[h]', 'd', 'e']);
  const posts = app.callsTo('POST', '/api/sessions/order');
  assert.equal(posts.length, 1);
  assert.deepEqual(posts[0].body.ids, ['u', 'c', 'a', 'b', 'd', 'e']);
  assert.equal(app.callsTo('POST', /\/api\/sessions\/[^/]+\/group$/).length, 0, 'the group is not touched');
});

test('dragging a card down inside a group moves it below the row it was dropped on', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  drag(app, 'a', 'c');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'b', 'c', 'a', '[h]', 'd', 'e']);
  assert.deepEqual(app.callsTo('POST', '/api/sessions/order')[0].body.ids, ['u', 'b', 'c', 'a', 'd', 'e']);
  assert.equal(app.callsTo('POST', /\/api\/sessions\/[^/]+\/group$/).length, 0);
});

test('two neighbours in a group swap', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  drag(app, 'a', 'b');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'b', 'a', 'c', '[h]', 'd', 'e']);
});

test('reordering one group leaves the others where they were, and saves the whole panel', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  drag(app, 'e', 'd');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', 'c', '[h]', 'e', 'd']);
  assert.deepEqual(app.callsTo('POST', '/api/sessions/order')[0].body.ids, ['u', 'a', 'b', 'c', 'e', 'd']);
});

test('a reordered group is still in that order after a reload', async () => {
  const daemon = fakeDaemon(PANEL);
  const app = await load({ routes: daemon.routes });

  drag(app, 'c', 'a');
  await app.settle();
  assert.deepEqual(rowIDs(app), ['u', 'c', 'a', 'b', 'd', 'e']);

  // A new page against the same daemon: nothing carried over but what the
  // daemon was told.
  const again = await load({ routes: daemon.routes });
  assert.deepEqual(shown(again), ['u', '[g]', 'c', 'a', 'b', '[h]', 'd', 'e']);
  assert.equal(card(again, 'c').draggable, true);
});

test('rows inside a group are draggable, and stop being while a filter is on', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  for (const id of ['a', 'b', 'c']) {
    const el = card(app, id);
    assert.equal(el.draggable, true, `${id} should be draggable`);
    assert.equal(el.getAttribute('draggable'), 'true');
    assert.match(el.title, /drag/);
  }

  const filter = app.el('session-filter');
  filter.value = 'b';
  filter.fire('input');
  await app.settle();

  const b = card(app, 'b');
  assert.equal(b.draggable, false);
  assert.doesNotMatch(b.title, /drag/);
  start(app, 'b');
  const over = b.fire('dragover', { dataTransfer: transfer() });
  assert.equal(over.defaultPrevented, false, 'a filtered row is not a drop target either');
});

test('a folded group is reordered around, and still takes a card on its header', async () => {
  const app = await load({
    routes: routesFor(PANEL),
    localStorage: { 'localcode.collapsedGroups': JSON.stringify(['h']) },
  });
  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', 'c', '[h]']);

  drag(app, 'a', 'c');
  await app.settle();
  assert.deepEqual(shown(app), ['u', '[g]', 'b', 'c', 'a', '[h]']);
  assert.deepEqual(
    app.callsTo('POST', '/api/sessions/order')[0].body.ids,
    ['u', 'b', 'c', 'a', 'd', 'e'],
    'the rows hidden by the fold are still in the order that is saved',
  );

  start(app, 'a');
  header(app, 'h').fire('drop', { dataTransfer: transfer() });
  await app.settle();
  assert.deepEqual(app.callsTo('POST', '/api/sessions/a/group').map((c) => c.body), [{ group: 'h' }]);
  assert.deepEqual(app.callsTo('POST', '/api/sessions/order')[1].body.ids, ['u', 'b', 'c', 'a', 'd', 'e']);
  assert.deepEqual(shown(app), ['u', '[g]', 'b', 'c', '[h]']);
});

// ---- the line that shows where the card lands -----------------------

test('the drop line is on the bottom edge for a card from above and the top edge for one from below', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  start(app, 'a');
  const c = card(app, 'c');
  c.fire('dragover', { dataTransfer: transfer() });
  assert.ok(c.classList.contains('drop-target'));
  assert.ok(c.classList.contains('drop-after'), 'a card from above lands below the row');
  c.fire('dragleave');
  assert.ok(!c.classList.contains('drop-target'));
  assert.ok(!c.classList.contains('drop-after'), 'leaving the row takes the line away');

  // The same row, from the other side.
  card(app, 'a').fire('dragend');
  start(app, 'c');
  const a = card(app, 'a');
  a.fire('dragover', { dataTransfer: transfer() });
  assert.ok(a.classList.contains('drop-target'));
  assert.ok(!a.classList.contains('drop-after'), 'a card from below lands above the row');
});

test('the line is on the side the card lands, for every pair of rows', async () => {
  const ids = PANEL.map((s) => s.id);
  for (const from of ids) {
    for (const to of ids) {
      if (from === to) continue;
      const app = await load({ routes: routesFor(PANEL) });
      start(app, from);
      const target = card(app, to);
      target.fire('dragover', { dataTransfer: transfer() });
      assert.ok(target.classList.contains('drop-target'), `${from} over ${to}`);
      const lineBelow = target.classList.contains('drop-after');
      target.fire('drop', { dataTransfer: transfer() });
      await app.settle();

      const order = rowIDs(app);
      assert.equal(
        order.indexOf(from) > order.indexOf(to),
        lineBelow,
        `${from} onto ${to}: the line says ${lineBelow ? 'below' : 'above'} and the card landed ${order.join(',')}`,
      );
    }
  }
});

test('ending or cancelling a drag takes the line off every row', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  start(app, 'a');
  const c = card(app, 'c');
  c.fire('dragover', { dataTransfer: transfer() });
  assert.ok(c.classList.contains('drop-after'));
  card(app, 'a').fire('dragend');

  assert.ok(!c.classList.contains('drop-target'));
  assert.ok(!c.classList.contains('drop-after'));
  assert.ok(!card(app, 'a').classList.contains('dragging'));
});

// The class is toggled by the script and drawn by the stylesheet, and the
// fake DOM draws nothing: this is what ties the two together.
test('the stylesheet draws the line above a row by default and below it for drop-after', () => {
  const css = fs.readFileSync(path.join(__dirname, '..', '..', 'internal', 'daemon', 'static', 'style.css'), 'utf8');
  const offset = (selector) => {
    const at = css.indexOf(`${selector} {`);
    assert.ok(at >= 0, `${selector} has no rule`);
    const m = css.slice(at, css.indexOf('}', at)).match(/box-shadow:\s*0\s+(-?\d+)px/);
    assert.ok(m, `${selector} draws no line`);
    return Number(m[1]);
  };
  assert.ok(offset('.session-item.drop-target') < 0, 'the plain line belongs on the top edge');
  assert.ok(offset('.session-item.drop-target.drop-after') > 0, 'drop-after belongs on the bottom edge');
});

test('landsBelow compares positions: from above is below, from below is above', async () => {
  const app = await load({ routes: routesFor(PANEL) });
  const list = PANEL;

  assert.equal(app.landsBelow(list, 'a', 'c'), true);
  assert.equal(app.landsBelow(list, 'c', 'a'), false);
  assert.equal(app.landsBelow(list, 'b', 'b'), false, 'a row is not below itself');
  assert.equal(app.landsBelow(list, 'nope', 'a'), false);
  assert.equal(app.landsBelow(list, 'a', 'nope'), false);
});

// The documented way to put a card last in a group above it: a card from
// below lands above the row it is dropped on, so it takes a second drop.
test('a card from a lower group takes two drops to be last in a higher group', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  drag(app, 'd', 'c');
  await app.settle();
  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', 'd', 'c', '[h]', 'e']);

  drag(app, 'd', 'c');
  await app.settle();
  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', 'c', 'd', '[h]', 'e']);
  assert.deepEqual(app.callsTo('POST', '/api/sessions/d/group').map((c) => c.body), [{ group: 'g' }]);
});

// ---- a card dropped on the header of the group it is already in -----

test('a card dropped on its own group header goes first and only the order is saved', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  start(app, 'c');
  header(app, 'g').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'c', 'a', 'b', '[h]', 'd', 'e']);
  assert.equal(app.callsTo('POST', /\/api\/sessions\/[^/]+\/group$/).length, 0, 'it is already in the group');
  assert.deepEqual(app.callsTo('POST', '/api/sessions/order')[0].body.ids, ['u', 'c', 'a', 'b', 'd', 'e']);
});

test('a card that is already first in its group sends nothing when dropped on the header', async () => {
  const app = await load({ routes: routesFor(PANEL) });
  const before = app.calls.length;

  start(app, 'a');
  header(app, 'g').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.equal(app.calls.length, before, 'no request at all');
  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', 'c', '[h]', 'd', 'e']);
});

test('a refused order after a header drop on the own group says so and puts the panel back', async () => {
  const app = await load({
    routes: routesFor(PANEL, { 'POST /api/sessions/order': { status: 500, body: { error: 'disk full' } } }),
  });

  start(app, 'c');
  header(app, 'g').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', 'c', '[h]', 'd', 'e']);
  assert.match(app.transcript(), /could not save the session order/);
  assert.doesNotMatch(app.transcript(), /moved group/);
});

test('a refused order inside a group puts the rows back and reads nothing back', async () => {
  const app = await load({
    routes: routesFor(PANEL, { 'POST /api/sessions/order': { status: 500, body: { error: 'disk full' } } }),
  });
  const reads = app.callsTo('GET', '/api/sessions').length;

  drag(app, 'c', 'a');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', 'c', '[h]', 'd', 'e']);
  assert.match(app.transcript(), /could not save the session order/);
  assert.equal(app.callsTo('GET', '/api/sessions').length, reads);
});

// ---- the panel is not redrawn under a card that is being carried ----

test('activity in another session mid-drag changes the data and leaves the rows alone', async () => {
  const app = await load({ routes: routesFor(PANEL) });
  const before = app.el('session-list').children.slice();

  start(app, 'c');
  emit(app, 'session.activity', { session: 'd', busy: true });
  await app.settle();

  assert.ok(sameRows(app, before), 'the panel was rebuilt under the dragged row');
  assert.ok(card(app, 'c').classList.contains('dragging'), 'the dragged row kept its look');
  assert.equal(app.app.sessions.find((s) => s.id === 'd').busy, true, 'the data is current');

  card(app, 'a').fire('dragover', { dataTransfer: transfer() });
  card(app, 'a').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(rowIDs(app), ['u', 'c', 'a', 'b', 'd', 'e']);
  assert.equal(ledOf(app, 'd'), 'running', 'and the light is drawn once the drag is over');
});

test('a drag that is cancelled draws what was held back, and the panel works again', async () => {
  const app = await load({ routes: routesFor(PANEL) });
  const before = app.el('session-list').children.slice();

  start(app, 'c');
  emit(app, 'session.activity', { session: 'd', busy: true });
  await app.settle();
  assert.ok(sameRows(app, before));

  card(app, 'c').fire('dragend');
  assert.ok(!sameRows(app, before), 'the held-back redraw is owed once the drag is over');
  assert.equal(ledOf(app, 'd'), 'running');

  const over = card(app, 'a').fire('dragover', { dataTransfer: transfer() });
  assert.equal(over.defaultPrevented, false, 'nothing is being dragged any more');

  const now = app.el('session-list').children.slice();
  emit(app, 'session.activity', { session: 'd', busy: false });
  await app.settle();
  assert.ok(!sameRows(app, now), 'and the next change is drawn straight away');
  assert.equal(ledOf(app, 'd'), 'unread', 'a turn that finished out of sight leaves its light on');
});

// A native drag sends the page no mouse events, so any of these means the
// pointer is back in the page's hands, whether or not dragend ever came.
for (const [name, fire] of [
  ['a mouse move with no button down', (app) => app.doc.fire('mousemove', { buttons: 0 })],
  ['a mouse press', (app) => app.doc.fire('mousedown', { buttons: 1 })],
  ['a pointer press', (app) => app.doc.fire('pointerdown', { buttons: 1 })],
  ['a drop that no row took', (app) => app.doc.fire('drop')],
  ['a dragend that reached the page', (app) => app.doc.fire('dragend')],
]) {
  test(`a drag whose dragend never came is ended by ${name}`, async () => {
    const app = await load({ routes: routesFor(PANEL) });
    const before = app.el('session-list').children.slice();

    start(app, 'c');
    emit(app, 'session.activity', { session: 'd', busy: true });
    await app.settle();
    assert.ok(sameRows(app, before));

    fire(app);
    assert.ok(!sameRows(app, before), 'the held-back redraw was never drawn');
    assert.equal(ledOf(app, 'd'), 'running');
    const over = card(app, 'a').fire('dragover', { dataTransfer: transfer() });
    assert.equal(over.defaultPrevented, false, 'the drag state was left set');
  });
}

test('a mouse move with the button still down does not end a drag', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  start(app, 'c');
  app.doc.fire('mousemove', { buttons: 1 });
  const over = card(app, 'a').fire('dragover', { dataTransfer: transfer() });

  assert.equal(over.defaultPrevented, true);
});

test('a stale drag cannot archive anything', async () => {
  const app = await load({ routes: routesFor(PANEL) });
  const toggle = app.el('archive-toggle');

  start(app, 'c');
  app.doc.fire('mousemove', { buttons: 0 });

  const over = toggle.fire('dragover', { dataTransfer: {} });
  assert.equal(over.defaultPrevented, false);
  toggle.fire('drop');
  await app.settle();
  assert.equal(app.calls.filter((c) => /\/archive$/.test(c.path)).length, 0);
});

test('a refresh mid-drag leaves the rows alone, and the drop lands on the refreshed list', async () => {
  let current = PANEL;
  const app = await load({ routes: routesFor(null, { 'GET /api/sessions': () => current }) });
  const before = app.el('session-list').children.slice();

  start(app, 'c');
  current = [...PANEL, row('z')];
  emit(app, 'session.renamed', { session: 'u' });
  await app.settle();
  assert.ok(sameRows(app, before), 'the refresh rebuilt the panel under the dragged row');

  card(app, 'a').fire('dragover', { dataTransfer: transfer() });
  card(app, 'a').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(app.callsTo('POST', '/api/sessions/order')[0].body.ids, ['u', 'z', 'c', 'a', 'b', 'd', 'e']);
});

test('a card that disappears mid-drag is not moved, and nothing is sent', async () => {
  let current = PANEL;
  const app = await load({ routes: routesFor(null, { 'GET /api/sessions': () => current }) });

  start(app, 'c');
  current = PANEL.filter((s) => s.id !== 'c');
  emit(app, 'session.renamed', { session: 'u' });
  await app.settle();

  card(app, 'a').fire('dragover', { dataTransfer: transfer() });
  card(app, 'a').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/order').length, 0);
  assert.deepEqual(rowIDs(app), ['u', 'a', 'b', 'd', 'e'], 'the panel shows the refreshed list');
});

// ---- a refresh does not undo a drop that is still being saved -------

test('a refresh asked for while a drop is being saved waits for it', async () => {
  const daemon = fakeDaemon(PANEL);
  const app = await load({ routes: daemon.routes });
  const hold = gate();
  daemon.holdOrder = hold.promise;

  drag(app, 'c', 'a');
  await app.settle();
  assert.deepEqual(rowIDs(app), ['u', 'c', 'a', 'b', 'd', 'e'], 'the drop is on screen at once');

  const reads = app.callsTo('GET', '/api/sessions').length;
  const loading = app.loadSessions();
  await app.settle();
  assert.equal(app.callsTo('GET', '/api/sessions').length, reads, 'the listing was asked for before the save landed');
  assert.deepEqual(rowIDs(app), ['u', 'c', 'a', 'b', 'd', 'e']);

  hold.open();
  await loading;
  await app.settle();
  assert.equal(app.callsTo('GET', '/api/sessions').length, reads + 1);
  assert.deepEqual(rowIDs(app), ['u', 'c', 'a', 'b', 'd', 'e'], 'and what it read is the new order');
});

test('a listing that was already on its way when a drop began does not undo it', async () => {
  const daemon = fakeDaemon(PANEL);
  const app = await load({ routes: daemon.routes });
  const hold = gate();

  // The daemon answers this one now, from before the drop, and the answer
  // is delayed.
  daemon.holdNextGet = hold.promise;
  const loading = app.loadSessions();
  await app.settle();

  drag(app, 'u', 'b');
  await app.settle();
  assert.deepEqual(daemon.saved, ['a', 'b', 'u', 'c', 'd', 'e'], 'the drop was saved');
  const reads = app.callsTo('GET', '/api/sessions').length;

  hold.open();
  await loading;
  await app.settle();

  assert.ok(app.callsTo('GET', '/api/sessions').length > reads, 'the old answer was asked for again');
  assert.deepEqual(rowIDs(app), ['a', 'b', 'u', 'c', 'd', 'e']);
  assert.equal(app.app.sessions.find((s) => s.id === 'u').group, 'g');
});

test('an old listing arriving between the two requests of a drop does not change what is saved', async () => {
  const daemon = fakeDaemon(PANEL);
  const app = await load({ routes: daemon.routes });
  const holdGet = gate();
  const holdGroup = gate();

  daemon.holdNextGet = holdGet.promise;
  const loading = app.loadSessions();
  await app.settle();

  // u joins g: the group request is held, so the order request is not
  // built until the old listing has arrived and been dealt with.
  daemon.holdGroup = holdGroup.promise;
  drag(app, 'u', 'b');
  await app.settle();
  holdGet.open();
  await app.settle();
  holdGroup.open();
  await loading;
  await app.settle();

  assert.deepEqual(daemon.saved, ['a', 'b', 'u', 'c', 'd', 'e'], 'the order that was saved is the drop\'s');
  assert.deepEqual(rowIDs(app), ['a', 'b', 'u', 'c', 'd', 'e']);
});

// ---- the other places a card can be dropped end the drag too --------

test('a card dropped on the strip above the groups leaves its group and the panel shows it', async () => {
  const sessions = [row('a', 'g'), row('b', 'g'), row('d', 'h')];
  const app = await load({ routes: routesFor(sessions) });
  const strip = app.el('session-list').children.find((c) => c.classList.contains('session-group-ungrouped-drop'));
  assert.ok(strip, 'every session is in a group, so the strip is drawn');

  start(app, 'b');
  strip.fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(shown(app), ['b', '[g]', 'a', '[h]', 'd']);
  assert.deepEqual(app.callsTo('POST', '/api/sessions/b/group').map((c) => c.body), [{ group: '' }]);
  const over = card(app, 'a').fire('dragover', { dataTransfer: transfer() });
  assert.equal(over.defaultPrevented, false, 'the drag is over');
});

test('a card that is already ungrouped sends no group request when dropped on the strip', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  await app.dropSessionToUngroupedTop('u');

  assert.equal(app.callsTo('POST', /\/api\/sessions\/[^/]+\/group$/).length, 0);
  assert.equal(app.callsTo('POST', '/api/sessions/order').length, 1);
});

test('a card dropped on the archive leaves the panel at once and the drag is over', async () => {
  let current = PANEL;
  const app = await load({
    routes: routesFor(null, {
      'GET /api/sessions': (body, { query }) => (query.get('archived') ? [] : current),
      'POST /api/sessions/c/archive': () => {
        current = PANEL.filter((s) => s.id !== 'c');
        return { status: 200, body: {} };
      },
    }),
  });

  start(app, 'c');
  app.el('archive-toggle').fire('drop', {});
  await app.settle();

  assert.deepEqual(rowIDs(app), ['u', 'a', 'b', 'd', 'e']);
  const over = card(app, 'a').fire('dragover', { dataTransfer: transfer() });
  assert.equal(over.defaultPrevented, false, 'the drag is over');
});

// The line is switched off by dragleave, which fires on every border the
// pointer crosses, including the ones between a card and the title or
// buttons inside it. Crossing those must not flicker it off.
test('moving the pointer onto something inside the target row keeps its line', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  start(app, 'a');
  const c = card(app, 'c');
  c.fire('dragover', { dataTransfer: transfer() });
  const inside = c.querySelector('.title') || c.children[0];
  assert.ok(inside, 'the row has nothing inside it to move onto');

  c.fire('dragleave', { relatedTarget: inside });
  assert.ok(c.classList.contains('drop-target'), 'the line went out while the pointer was still on the row');
  assert.ok(c.classList.contains('drop-after'));

  c.fire('dragleave', { relatedTarget: card(app, 'b') });
  assert.ok(!c.classList.contains('drop-target'), 'the line stayed after the pointer left the row');
  assert.ok(!c.classList.contains('drop-after'));

  c.fire('dragover', { dataTransfer: transfer() });
  c.fire('dragleave', {});
  assert.ok(!c.classList.contains('drop-target'), 'an engine that does not say where the pointer went must still clear the line');
});

test('a group header keeps its outline while the pointer is on its name', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  start(app, 'a');
  const h = header(app, 'h');
  h.fire('dragover', { dataTransfer: transfer() });
  assert.ok(h.classList.contains('drop-target'));

  h.fire('dragleave', { relatedTarget: h.querySelector('.group-name') });
  assert.ok(h.classList.contains('drop-target'));
  h.fire('dragleave', { relatedTarget: card(app, 'd') });
  assert.ok(!h.classList.contains('drop-target'));
});

// The archive header changes its border and its words while a card is over
// it, and puts both back on dragleave. A drag that ends over it by any
// other road (Escape, the watchdog) sends no dragleave.
test('a drag that ends over the archive header puts the header back', async () => {
  for (const end of [
    (app) => card(app, 'c').fire('dragend'),
    (app) => app.doc.fire('mousemove', { buttons: 0 }),
  ]) {
    const app = await load({ routes: routesFor(PANEL) });
    const toggle = app.el('archive-toggle');
    const idle = toggle.textContent;

    start(app, 'c');
    toggle.fire('dragover', { dataTransfer: transfer() });
    assert.equal(toggle.textContent, 'drop to archive');
    assert.ok(toggle.classList.contains('drag-over'));

    end(app);
    assert.ok(!toggle.classList.contains('drag-over'), 'the archive header kept its drag highlight');
    assert.equal(toggle.textContent, idle, 'the archive header kept saying it would take the drop');
  }
});

// A cancelled drag leaves the carried card faded unless the end of the
// drag restores it, and the watchdog is the road where the row's own
// dragend never runs.
test('a drag ended by the watchdog brings the carried card back to full strength', async () => {
  const app = await load({ routes: routesFor(PANEL) });

  start(app, 'c');
  assert.ok(card(app, 'c').classList.contains('dragging'));
  app.doc.fire('mousemove', { buttons: 0 });

  assert.ok(!card(app, 'c').classList.contains('dragging'), 'the card stayed faded after the drag was over');
});

// High contrast removes box-shadow, which is how the line is drawn.
test('in forced colors the target row is still marked', () => {
  const css = fs.readFileSync(path.join(__dirname, '..', '..', 'internal', 'daemon', 'static', 'style.css'), 'utf8');
  const at = css.indexOf('@media (forced-colors: active)');
  assert.ok(at >= 0, 'no forced-colors rule: the drop line disappears in Windows High Contrast');
  const block = css.slice(at, css.indexOf('\n}', at));
  assert.match(block, /\.session-item\.drop-target[^}]*outline:\s*2px solid Highlight/);
});

// The guard's drop listener must run after the row's own, or it ends the
// drag before the row reads who was dragged. In a browser that is the
// bubble phase; the fake DOM cannot tell the two apart, so the
// registration is read instead.
test('the page-level drag guard listens in the bubble phase', async () => {
  const app = await load({ routes: routesFor(PANEL) });
  const registered = [];
  const add = app.doc.addEventListener.bind(app.doc);
  app.doc.addEventListener = (type, fn, opts) => {
    registered.push([type, opts]);
    return add(type, fn, opts);
  };
  const { wireSessionDragGuard } = app.internals;
  assert.equal(typeof wireSessionDragGuard, 'function', 'the guard is not reachable from the test');
  wireSessionDragGuard();

  const drop = registered.find(([type]) => type === 'drop');
  assert.ok(drop, 'the guard listens for no drop');
  const capture = drop[1] === true || Boolean(drop[1] && typeof drop[1] === 'object' && drop[1].capture === true);
  assert.equal(capture, false, 'a capture-phase drop listener ends the drag before any row can read it');
});

// Two cards dropped one after the other, with a refresh in between, must
// both survive on screen and in what was saved. The first save lands while
// the second is still waiting, which is the moment a refresh that waited
// only for the first save would ask the daemon and paint an old order.
test('a refresh between two quick drops waits for both and keeps both', async () => {
  const d = fakeDaemon(PANEL);
  let releaseFirst;
  let releaseSecond;
  d.orderHolds = [
    new Promise((resolve) => { releaseFirst = resolve; }),
    new Promise((resolve) => { releaseSecond = resolve; }),
  ];
  const app = await load({ routes: d.routes });

  drag(app, 'c', 'a');
  emit(app, 'session.renamed', { session: 'u' });
  drag(app, 'd', 'e');
  await app.settle();

  releaseFirst();
  await app.settle();
  assert.deepEqual(rowIDs(app), ['u', 'c', 'a', 'b', 'e', 'd'],
    'the refresh painted the daemon\'s order while the second drop was still being saved');

  releaseSecond();
  await app.settle();
  await app.settle();
  assert.deepEqual(rowIDs(app), ['u', 'c', 'a', 'b', 'e', 'd'], 'a drop was lost on screen');
  assert.deepEqual(d.saved, ['u', 'c', 'a', 'b', 'e', 'd'], 'a drop was lost in what was saved');
});

// The header of a group nobody is in is a drop target like any other.
test('a card dropped on the header of an empty group goes into it', async () => {
  const app = await load({
    routes: routesFor(PANEL, { 'GET /api/sessions/groups': { names: ['g', 'h', 'empty'] } }),
  });

  start(app, 'a');
  const h = header(app, 'empty');
  h.fire('dragover', { dataTransfer: transfer() });
  h.fire('drop', { dataTransfer: transfer() });
  await app.settle();

  const g = app.callsTo('POST', /\/a\/group$/);
  assert.equal(g.length, 1);
  assert.deepEqual(g[0].body, { group: 'empty' });
});

// Dragging a group by its header. The panel draws the groups in the order of
// the daemon's list of names, so moving a group is a change to that list and
// nothing else: the cards keep the order they had inside it.

// u above the groups; g = [a, b], h = [c, d], k = [e].
const THREE = [row('u'), row('a', 'g'), row('b', 'g'), row('c', 'h'), row('d', 'h'), row('e', 'k')];

// A daemon that keeps the list of groups it is given, and replaces it
// wholesale the way the real one does. holdGet makes the next listing of the
// groups wait, postHolds[n] makes the nth save wait, and failPosts holds the
// numbers of the saves that are refused (after their hold, if they have one).
// getQueue holds functions that answer the next listings of the groups instead.
function groupDaemon(names, sessions = THREE) {
  const d = { names: names.slice(), posts: [], holdGet: null, getQueue: [], postHolds: [], failPosts: new Set() };
  d.routes = routesFor(sessions, {
    'GET /api/sessions/groups': async () => {
      if (d.getQueue.length) return d.getQueue.shift()();
      const answer = { names: d.names.slice() };
      if (d.holdGet) {
        const hold = d.holdGet;
        d.holdGet = null;
        await hold;
      }
      return answer;
    },
    'POST /api/sessions/groups': async (body) => {
      const n = d.posts.length;
      d.posts.push(body);
      if (d.postHolds[n]) await d.postHolds[n];
      if (d.failPosts.has(n)) return { status: 500, body: { error: 'disk full' } };
      d.names = body.names;
      return { status: 200, body: { names: d.names } };
    },
  });
  return d;
}

function startGroup(app, name) {
  header(app, name).fire('dragstart', { dataTransfer: transfer() });
}

function dragGroup(app, from, to) {
  startGroup(app, from);
  const target = header(app, to);
  target.fire('dragover', { dataTransfer: transfer() });
  target.fire('drop', { dataTransfer: transfer() });
}

const GROUPS = ['g', 'h', 'k'];

test('a group dragged down onto another lands below it, with its cards, and the list is saved', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });

  dragGroup(app, 'g', 'h');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[h]', 'c', 'd', '[g]', 'a', 'b', '[k]', 'e']);
  assert.deepEqual(d.posts, [{ names: ['h', 'g', 'k'] }]);
  assert.equal(app.callsTo('POST', '/api/sessions/order').length, 0, 'the order of the cards did not change');
});

test('a group dragged up onto another lands above it', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });

  dragGroup(app, 'k', 'g');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[k]', 'e', '[g]', 'a', 'b', '[h]', 'c', 'd']);
  assert.deepEqual(d.posts, [{ names: ['k', 'g', 'h'] }]);
});

test('a group moved across two others, in either direction', async () => {
  for (const [from, to, want] of [['g', 'k', ['h', 'k', 'g']], ['k', 'g', ['k', 'g', 'h']]]) {
    const d = groupDaemon(GROUPS);
    const app = await load({ routes: d.routes });
    dragGroup(app, from, to);
    await app.settle();
    assert.deepEqual(d.posts, [{ names: want }], `${from} onto ${to}`);
  }
});

test('a group dropped on itself, or let go over nothing, saves nothing', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });

  dragGroup(app, 'h', 'h');
  startGroup(app, 'g');
  header(app, 'g').fire('dragend');
  await app.settle();

  assert.equal(d.posts.length, 0);
  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', '[h]', 'c', 'd', '[k]', 'e']);
});

test('the new order is what a reload draws', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });

  dragGroup(app, 'g', 'k');
  await app.settle();
  emit(app, 'session.renamed', { session: 'u' });
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[h]', 'c', 'd', '[k]', 'e', '[g]', 'a', 'b']);
});

// The line says where the group will land. Under a group is the edge between
// it and the next one, which is under its last card, not under its header.
test('the line is above the target for a group from below and under its last card for one from above', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });

  startGroup(app, 'g');
  header(app, 'h').fire('dragover', { dataTransfer: transfer() });
  assert.ok(card(app, 'd').classList.contains('group-drop-after'), 'the line belongs under the last card of the group it lands after');
  assert.ok(!header(app, 'h').classList.contains('group-drop-before'));
  assert.ok(!header(app, 'h').classList.contains('drop-target'), 'a group landing is not a card going into the group');

  header(app, 'k').fire('dragover', { dataTransfer: transfer() });
  assert.ok(card(app, 'e').classList.contains('group-drop-after'));
  assert.ok(!card(app, 'd').classList.contains('group-drop-after'), 'only one line at a time');

  header(app, 'g').fire('dragend');
  for (const id of ['a', 'b', 'c', 'd', 'e']) assert.ok(!card(app, id).classList.contains('group-drop-after'));

  startGroup(app, 'k');
  header(app, 'g').fire('dragover', { dataTransfer: transfer() });
  assert.ok(header(app, 'g').classList.contains('group-drop-before'));
  header(app, 'k').fire('dragend');
  assert.ok(!header(app, 'g').classList.contains('group-drop-before'));
});

test('a folded group has its line on its header', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  header(app, 'h').fire('click');
  await app.settle();

  startGroup(app, 'g');
  header(app, 'h').fire('dragover', { dataTransfer: transfer() });

  assert.ok(header(app, 'h').classList.contains('group-drop-after'));
  header(app, 'g').fire('dragend');
});

test('the line goes when the pointer leaves the header, and stays while it is on its name', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  startGroup(app, 'g');
  const h = header(app, 'h');
  h.fire('dragover', { dataTransfer: transfer() });

  h.fire('dragleave', { relatedTarget: h.querySelector('.group-name') });
  assert.ok(card(app, 'd').classList.contains('group-drop-after'));
  h.fire('dragleave', { relatedTarget: card(app, 'a') });
  assert.ok(!card(app, 'd').classList.contains('group-drop-after'));
  header(app, 'g').fire('dragend');
});

test('the carried header fades and comes back', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  startGroup(app, 'g');
  assert.ok(header(app, 'g').classList.contains('dragging'));
  header(app, 'g').fire('dragend');
  assert.ok(!header(app, 'g').classList.contains('dragging'));
});

// A group goes where a group is, and nowhere else.
test('rows, the strip above the groups and the archive take no group', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  startGroup(app, 'g');

  const targets = [
    card(app, 'c'),
    (app.el('session-list').children || []).find((c) => c.classList.contains('session-group-ungrouped-drop')),
    app.el('archive-toggle'),
  ].filter(Boolean);
  assert.ok(targets.length >= 2, 'the test found too few places to drop on');
  for (const el of targets) {
    const over = el.fire('dragover', { dataTransfer: transfer() });
    assert.equal(over.defaultPrevented, false, 'a group was accepted where only a card goes');
  }
  app.el('archive-toggle').fire('drop');
  await app.settle();
  assert.equal(app.calls.filter((c) => /\/archive$/.test(c.path)).length, 0);
  header(app, 'g').fire('dragend');
});

test('a card dropped on a group header still goes into that group', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });

  start(app, 'a');
  const h = header(app, 'h');
  h.fire('dragover', { dataTransfer: transfer() });
  assert.ok(h.classList.contains('drop-target'), 'the outline for a card going in');
  assert.ok(!h.classList.contains('group-drop-before'));
  h.fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.equal(d.posts.length, 0, 'no group list was saved');
  assert.equal(app.callsTo('POST', /\/a\/group$/).length, 1);
});

test('a group is not draggable while a filter is on', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  assert.equal(header(app, 'g').draggable, true);

  const filter = app.el('session-filter');
  filter.value = 'a';
  filter.fire('input');
  await app.settle();

  assert.notEqual(header(app, 'g').draggable, true);
});

test('activity in another session mid-drag leaves the panel alone until the group is dropped', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });
  const before = app.el('session-list').children.slice();

  startGroup(app, 'g');
  emit(app, 'session.activity', { session: 'e', busy: true });
  await app.settle();
  assert.ok(sameRows(app, before), 'the panel was rebuilt under the carried header');

  header(app, 'h').fire('dragover', { dataTransfer: transfer() });
  header(app, 'h').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(d.posts, [{ names: ['h', 'g', 'k'] }]);
  assert.equal(ledOf(app, 'e'), 'running', 'what was held back is drawn once the drag is over');
});

test('a stale group drag is ended by the page-level guard', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  const before = app.el('session-list').children.slice();

  startGroup(app, 'g');
  emit(app, 'session.activity', { session: 'e', busy: true });
  await app.settle();
  assert.ok(sameRows(app, before));

  app.doc.fire('mousemove', { buttons: 0 });
  assert.ok(!sameRows(app, before), 'the held-back redraw was never drawn');
  const over = header(app, 'h').fire('dragover', { dataTransfer: transfer() });
  assert.equal(over.defaultPrevented, false, 'the drag state was left set');
});

test('a refused save puts the groups back, says so, and saves nothing else', async () => {
  const d = groupDaemon(GROUPS);
  d.routes['POST /api/sessions/groups'] = () => ({ status: 500, body: { error: 'disk full' } });
  const app = await load({ routes: d.routes });

  dragGroup(app, 'g', 'h');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', '[h]', 'c', 'd', '[k]', 'e']);
  assert.match(app.el('transcript').textContent, /could not save the group order/);
});

test('a refresh while the list is being saved waits for it', async () => {
  const d = groupDaemon(GROUPS);
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  const post = d.routes['POST /api/sessions/groups'];
  d.routes['POST /api/sessions/groups'] = async (body) => {
    await held;
    return post(body);
  };
  const app = await load({ routes: d.routes });

  dragGroup(app, 'g', 'h');
  emit(app, 'session.renamed', { session: 'u' });
  await app.settle();
  assert.deepEqual(shown(app), ['u', '[h]', 'c', 'd', '[g]', 'a', 'b', '[k]', 'e'],
    'the refresh painted the daemon\'s old list over the drop');

  release();
  await app.settle();
  await app.settle();
  assert.deepEqual(shown(app), ['u', '[h]', 'c', 'd', '[g]', 'a', 'b', '[k]', 'e']);
});

test('groupLandsBelow compares positions in the list of groups', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  const { groupLandsBelow } = app.internals;
  assert.equal(groupLandsBelow(GROUPS, 'g', 'h'), true);
  assert.equal(groupLandsBelow(GROUPS, 'k', 'g'), false);
  assert.equal(groupLandsBelow(GROUPS, 'g', 'g'), false);
  assert.equal(groupLandsBelow(GROUPS, 'g', 'nope'), false);
  assert.equal(groupLandsBelow(undefined, 'g', 'h'), false);
});

test('in forced colors the group lines are still drawn', () => {
  const css = fs.readFileSync(path.join(__dirname, '..', '..', 'internal', 'daemon', 'static', 'style.css'), 'utf8');
  const at = css.indexOf('@media (forced-colors: active)');
  assert.ok(at >= 0);
  const block = css.slice(at, css.indexOf('\n}', at));
  assert.match(block, /\.group-drop-before[^}]*outline:\s*2px solid Highlight/);
  assert.match(css, /\.group-drop-before\s*\{\s*box-shadow:\s*0\s+-2px/);
  assert.match(css, /\.group-drop-after\s*\{\s*box-shadow:\s*0\s+2px/);
});

// ---- what a header drag must not get wrong ---------------------------

// The daemon deletes a group that the list it is sent leaves out, so a drag
// in a window that has not heard of a group must not send its list.
test('a window that has not heard of a new group does not save its list over the daemon\'s', async () => {
  const sessions = THREE.map(s => ({ ...s }));
  const d = groupDaemon(GROUPS, sessions);
  const app = await load({ routes: d.routes });
  // Another window makes group m and puts a session in it. Nothing tells this page.
  d.names = [...GROUPS, 'm'];
  sessions.push(row('x', 'm'));

  dragGroup(app, 'g', 'h');
  await app.settle();

  assert.deepEqual(d.posts, [], 'the list this page holds would have deleted m');
  assert.deepEqual(d.names, [...GROUPS, 'm']);
  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', '[h]', 'c', 'd', '[k]', 'e', '[m]', 'x'], 'the panel was read back');
  assert.match(app.el('transcript').textContent, /changed in another window/);
});

test('a carried group that was deleted in another window moves nothing when it is dropped', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });
  startGroup(app, 'g');
  d.names = ['h', 'k'];
  emit(app, 'session.renamed', { session: 'u' });
  await app.settle();

  header(app, 'h').fire('dragover', { dataTransfer: transfer() });
  header(app, 'h').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(d.posts, []);
  assert.deepEqual(app.app.sessionGroups, ['h', 'k']);
});

// A listing of the groups asked for before a drop and answered after it
// describes the panel as it was before the drop.
async function dropWhileGroupsAreBeingListed(d) {
  const app = await load({ routes: d.routes });
  const get = gate();
  const post = gate();
  d.holdGet = get.promise;
  d.postHolds[0] = post.promise;
  const loading = app.loadSessions();
  await app.settle();
  dragGroup(app, 'g', 'h');
  await app.settle();
  return { app, get, post, loading };
}

test('a listing of the groups that was on its way when a group drop began does not undo it', async () => {
  const { app, get, post, loading } = await dropWhileGroupsAreBeingListed(groupDaemon(GROUPS));

  get.open();
  await app.settle();
  assert.deepEqual(app.app.sessionGroups, ['h', 'g', 'k'], 'the old list came back');
  emit(app, 'session.activity', { session: 'e', busy: true });
  await app.settle();
  assert.deepEqual(shown(app), ['u', '[h]', 'c', 'd', '[g]', 'a', 'b', '[k]', 'e'], 'a redraw painted the old order');
  post.open();
  await loading;
});

test('a second group move made while that listing was on its way is computed from the first', async () => {
  const d = groupDaemon(GROUPS);
  const { app, get, post, loading } = await dropWhileGroupsAreBeingListed(d);

  get.open();
  await app.settle();
  dragGroup(app, 'k', 'h');
  await app.settle();
  post.open();
  await loading;
  await app.settle();

  assert.deepEqual(d.posts[1], { names: ['k', 'h', 'g'] });
});

// Each move is put back to what the panel held before it, which is only the
// right thing when no other move overlapped it.
test('a refused move does not undo a later move that was accepted', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });
  const first = gate();
  d.postHolds[0] = first.promise;
  d.failPosts.add(0);

  dragGroup(app, 'g', 'h');
  await app.settle();
  dragGroup(app, 'g', 'k');
  await app.settle();
  first.open();
  await app.settle();

  assert.deepEqual(d.names, ['h', 'k', 'g']);
  assert.deepEqual(app.app.sessionGroups, d.names, 'the page and the daemon disagree');
  assert.match(app.el('transcript').textContent, /could not save the group order/);
});

// Reading the panel back asks the daemon, and a daemon that cannot save is
// often one that cannot answer either: that must not blank the panel.
test('a move that fails because the daemon is gone puts the groups back and keeps the panel', async () => {
  const d = groupDaemon(GROUPS);
  let down = false;
  for (const key of ['GET /api/sessions', 'GET /api/sessions/groups', 'POST /api/sessions/groups']) {
    const answer = d.routes[key];
    d.routes[key] = (...args) => {
      if (down) return { status: 500, body: { error: 'down' } };
      return typeof answer === 'function' ? answer(...args) : answer;
    };
  }
  const app = await load({ routes: d.routes });
  down = true;

  dragGroup(app, 'g', 'h');
  await app.settle();

  assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', '[h]', 'c', 'd', '[k]', 'e']);
  assert.match(app.el('transcript').textContent, /could not save the group order/);
});

test('two refused moves leave the page on the order the daemon holds', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });
  const first = gate();
  const second = gate();
  d.postHolds[0] = first.promise;
  d.postHolds[1] = second.promise;
  d.failPosts.add(0).add(1);

  dragGroup(app, 'g', 'h');
  await app.settle();
  dragGroup(app, 'g', 'k');
  await app.settle();
  first.open();
  await app.settle();
  second.open();
  await app.settle();

  assert.deepEqual(d.names, GROUPS);
  assert.deepEqual(app.app.sessionGroups, GROUPS, 'the page shows an order the daemon never held');
});

// A dragend that never arrives must not decide what the next drag is.
test('a card drag that starts after a group drag that never ended is a card drag', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });

  startGroup(app, 'g');
  start(app, 'a');
  header(app, 'h').fire('dragover', { dataTransfer: transfer() });
  header(app, 'h').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(d.posts, [], 'the card drop was saved as a group move');
  assert.deepEqual(app.callsTo('POST', '/api/sessions/a/group').map(c => c.body), [{ group: 'h' }]);
});

test('a group drag that starts after a card drag that never ended is a group drag', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });

  start(app, 'a');
  startGroup(app, 'g');
  const over = card(app, 'c').fire('dragover', { dataTransfer: transfer() });

  assert.equal(over.defaultPrevented, false, 'a card row took a group');
});

// ---- the browser decides from dragover, not from drop ---------------

// A drop is only sent to an element whose dragover was cancelled, and the
// fake DOM sends it regardless. So the cancelling is asserted itself.
test('the header under a carried group cancels dragover, or no browser sends the drop', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  startGroup(app, 'g');

  const over = header(app, 'h').fire('dragover', { dataTransfer: transfer() });
  assert.equal(over.defaultPrevented, true);
  header(app, 'g').fire('dragend');
});

test('the carried header refuses its own dragover and draws no line on itself', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  startGroup(app, 'h');

  const self = header(app, 'h');
  assert.equal(self.fire('dragover', { dataTransfer: transfer() }).defaultPrevented, false);
  assert.ok(!self.classList.contains('group-drop-before') && !self.classList.contains('group-drop-after'));
  self.fire('dragend');
});

test('the strip above the groups, when it is on screen, refuses a carried group', async () => {
  const everyone = [row('a', 'g'), row('b', 'g'), row('c', 'h')];
  const app = await load({ routes: groupDaemon(['g', 'h'], everyone).routes });
  const strip = (app.el('session-list').children || []).find(c => c.classList.contains('session-group-ungrouped-drop'));
  assert.ok(strip, 'every session is in a group, so the strip is drawn');

  startGroup(app, 'g');
  assert.equal(strip.fire('dragover', { dataTransfer: transfer() }).defaultPrevented, false);
  header(app, 'g').fire('dragend');
});

test('a header drag puts data on the transfer, because some browsers start no drag without it', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  const t = transfer();

  header(app, 'g').fire('dragstart', { dataTransfer: t });

  assert.equal(t.getData('text/plain'), 'g');
  header(app, 'g').fire('dragend');
});

test('a carried card is accepted over a header, the strip and the archive, and a drop on a header is cancelled', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  start(app, 'a');
  assert.equal(header(app, 'h').fire('dragover', { dataTransfer: transfer() }).defaultPrevented, true, 'header');
  assert.equal(app.el('archive-toggle').fire('dragover', { dataTransfer: transfer() }).defaultPrevented, true, 'archive');
  assert.equal(header(app, 'k').fire('drop', { dataTransfer: transfer() }).defaultPrevented, true, 'drop on a header');
  await app.settle();

  const everyone = [row('a', 'g'), row('b', 'g'), row('c', 'h')];
  const other = await load({ routes: groupDaemon(['g', 'h'], everyone).routes });
  const strip = (other.el('session-list').children || []).find(c => c.classList.contains('session-group-ungrouped-drop'));
  start(other, 'b');
  assert.equal(strip.fire('dragover', { dataTransfer: transfer() }).defaultPrevented, true, 'strip');
  card(other, 'b').fire('dragend');

  startGroup(app, 'g');
  header(app, 'h').fire('dragover', { dataTransfer: transfer() });
  assert.equal(header(app, 'h').fire('drop', { dataTransfer: transfer() }).defaultPrevented, true, 'group dropped on a header');
  await app.settle();
});

// After a group has moved, or a move was refused and put back, the order
// app.sessions is in is what decides which side a card lands on, so it has to
// be the order the rows are drawn in.
async function cardsLandWhereTheLineIs(prepare) {
  const wrong = [];
  for (const from of ['a', 'b', 'c', 'd', 'e']) {
    for (const to of ['a', 'b', 'c', 'd', 'e']) {
      if (from === to) continue;
      const d = groupDaemon(GROUPS);
      const app = await load({ routes: d.routes });
      await prepare(app, d);
      const order = rowIDs(app);
      start(app, from);
      const target = card(app, to);
      target.fire('dragover', { dataTransfer: transfer() });
      const lineBelow = target.classList.contains('drop-after');
      target.fire('drop', { dataTransfer: transfer() });
      await app.settle();
      const after = rowIDs(app);
      const landedBelow = after.indexOf(from) > after.indexOf(to);
      const fromAbove = order.indexOf(from) < order.indexOf(to);
      if (lineBelow !== landedBelow || fromAbove !== landedBelow) wrong.push(`${from} onto ${to}`);
    }
  }
  return wrong;
}

test('after a group move, a card lands on the side its line shows', async () => {
  const wrong = await cardsLandWhereTheLineIs(async (app) => {
    dragGroup(app, 'g', 'h');
    await app.settle();
  });
  assert.deepEqual(wrong, []);
});

test('after a refused group move was put back, a card lands on the side its line shows', async () => {
  const wrong = await cardsLandWhereTheLineIs(async (app, d) => {
    d.failPosts.add(0);
    dragGroup(app, 'g', 'h');
    await app.settle();
  });
  assert.deepEqual(wrong, []);
});

// ---- the save is checked against the daemon and kept in order -------

// Compared by name, not by how many: a group renamed in another window, or one
// deleted while another was made, leaves the count as it was.
for (const [what, daemonNames, regroup] of [
  ['renamed', ['g2', 'h', 'k'], { g: 'g2' }],
  ['deleted while another was made', ['h', 'k', 'z'], { g: '' }],
]) {
  test(`a group ${what} in another window is not overwritten by a move from a window that has not heard`, async () => {
    const sessions = THREE.map(s => ({ ...s }));
    const d = groupDaemon(GROUPS, sessions);
    const app = await load({ routes: d.routes });
    d.names = daemonNames;
    for (const s of sessions) if (s.group in regroup) s.group = regroup[s.group];

    dragGroup(app, 'h', 'k');
    await app.settle();

    assert.deepEqual(d.posts, []);
    assert.deepEqual(d.names, daemonNames);
    assert.deepEqual(app.app.sessionGroups, daemonNames, 'the panel was not read back');
  });
}

test('a group dropped on a header whose group was deleted meanwhile moves nothing', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });
  startGroup(app, 'k');
  d.names = ['g', 'k'];
  emit(app, 'session.renamed', { session: 'u' });
  await app.settle();
  assert.deepEqual(app.app.sessionGroups, ['g', 'k'], 'the refresh reached the page');

  header(app, 'h').fire('dragover', { dataTransfer: transfer() });
  header(app, 'h').fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(d.posts, []);
});

for (const [what, answer] of [['nothing', () => ({ status: 204 })], ['an object without names', () => ({})]]) {
  test(`a listing of the groups that answers ${what} saves nothing and reports a failed save`, async () => {
    const d = groupDaemon(GROUPS);
    const app = await load({ routes: d.routes });
    d.getQueue.push(answer);

    dragGroup(app, 'g', 'h');
    await app.settle();

    assert.deepEqual(d.posts, []);
    assert.deepEqual(shown(app), ['u', '[g]', 'a', 'b', '[h]', 'c', 'd', '[k]', 'e']);
    assert.match(app.el('transcript').textContent, /could not save the group order: Error: the daemon sent no list of groups/);
  });
}

// A read-back that cannot reach the daemon must not leave a move on screen
// that was not saved.
test('a refused stale move stays off the screen when the read-back cannot list the groups', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });
  d.names = [...GROUPS, 'm'];
  d.getQueue.push(() => ({ names: [...GROUPS, 'm'] }), () => ({ status: 500, body: { error: 'down' } }));

  dragGroup(app, 'g', 'h');
  await app.settle();
  await app.settle();

  assert.deepEqual(d.posts, []);
  assert.deepEqual(app.app.sessionGroups, GROUPS, 'the page holds a move that was not saved');
  assert.match(app.el('transcript').textContent, /was not saved/);
});

// Each drop reads the daemon's list before it sends its own. The lists still
// have to arrive in the order of the drops, or the daemon ends on an older
// order than the one on screen.
test('two group drops reach the daemon in the order they were made when the first read is slow', async () => {
  const d = groupDaemon(GROUPS);
  const app = await load({ routes: d.routes });
  const slow = gate();
  d.holdGet = slow.promise;

  dragGroup(app, 'g', 'h');
  await app.settle();
  dragGroup(app, 'k', 'h');
  await app.settle();
  slow.open();
  await app.settle();
  await app.settle();

  assert.deepEqual(d.posts, [{ names: ['h', 'g', 'k'] }, { names: ['k', 'h', 'g'] }]);
  assert.deepEqual(d.names, app.app.sessionGroups, 'the daemon and the panel disagree');
  assert.doesNotMatch(app.el('transcript').textContent, /Error/);
});

// Installed before load(), because the harness copies the routes when it
// starts. arm() makes the next listing of the sessions wait for open();
// breakIt() makes every listing after it answer something that is not a list.
function listingGate(d) {
  const hold = gate();
  const state = { armed: false, broken: false };
  const inner = d.routes['GET /api/sessions'];
  d.routes['GET /api/sessions'] = async (...args) => {
    const answer = typeof inner === 'function' ? await inner(...args) : inner;
    if (state.armed) {
      state.armed = false;
      await hold.promise;
    }
    return state.broken ? { not: 'a list' } : answer;
  };
  return { arm() { state.armed = true; }, open: hold.open, breakIt() { state.broken = true; } };
}

// The person is told why the panel is changing before the read-back is done.
test('the message that a move was not saved is on screen while the panel is being read back', async () => {
  const d = groupDaemon(GROUPS);
  const slow = listingGate(d);
  const app = await load({ routes: d.routes });
  d.names = [...GROUPS, 'm'];
  slow.arm();

  dragGroup(app, 'g', 'h');
  await app.settle();

  assert.match(app.el('transcript').textContent, /changed in another window/);
  slow.open();
  await app.settle();
});

test('a refusal that overlapped another drop says so while the panel is being read back', async () => {
  const d = groupDaemon(GROUPS);
  const slow = listingGate(d);
  const app = await load({ routes: d.routes });
  const first = gate();
  d.postHolds[0] = first.promise;
  d.failPosts.add(0);

  dragGroup(app, 'g', 'h');
  await app.settle();
  dragGroup(app, 'g', 'k');
  await app.settle();
  slow.arm();
  first.open();
  await app.settle();

  assert.match(app.el('transcript').textContent, /could not save the group order/);
  slow.open();
  await app.settle();
});

test('dropGroupOn finishes only after the panel has been read back', async () => {
  const d = groupDaemon(GROUPS);
  const slow = listingGate(d);
  const app = await load({ routes: d.routes });
  d.names = [...GROUPS, 'm'];
  slow.arm();

  let done = false;
  const dropped = app.dropGroupOn('g', 'h').then(() => { done = true; });
  await app.settle();
  assert.equal(done, false, 'it finished before the read-back answered');
  slow.open();
  await dropped;

  assert.deepEqual(app.app.sessionGroups, [...GROUPS, 'm']);
});

test('a read-back that cannot be drawn says so', async () => {
  const d = groupDaemon(GROUPS);
  const slow = listingGate(d);
  const app = await load({ routes: d.routes });
  d.names = [...GROUPS, 'm'];
  slow.breakIt();

  dragGroup(app, 'g', 'h');
  await app.settle();

  assert.match(app.el('transcript').textContent, /could not read the session list back/);
});

// ---- the line and what the browser is told ----------------------------

test('moving a carried group between two headers it lands above leaves one line', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  startGroup(app, 'k');
  header(app, 'g').fire('dragover', { dataTransfer: transfer() });
  assert.ok(header(app, 'g').classList.contains('group-drop-before'));

  header(app, 'h').fire('dragover', { dataTransfer: transfer() });

  assert.ok(header(app, 'h').classList.contains('group-drop-before'));
  assert.ok(!header(app, 'g').classList.contains('group-drop-before'), 'two lines at once');
  header(app, 'k').fire('dragend');
});

test('a carried group says it is a move, and a header it is over says it takes a move', async () => {
  const app = await load({ routes: groupDaemon(GROUPS).routes });
  const out = transfer();
  header(app, 'g').fire('dragstart', { dataTransfer: out });
  assert.equal(out.effectAllowed, 'move');

  const over = transfer();
  header(app, 'h').fire('dragover', { dataTransfer: over });
  assert.equal(over.dropEffect, 'move');
  header(app, 'g').fire('dragend');
});
