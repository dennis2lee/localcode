'use strict';

// The archive, in the session panel.
//
// Archiving is not deleting, and the panel says so in every way it can: no
// confirm, no red button, and the conversation one click from coming back.
// What these hold to is that difference, and that a conversation which has
// left the list cannot be opened by any gesture the panel still offers.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const ONE = { id: 's1', title: 'the one about parsers', agent: 'general-purpose', created_at: '2026-08-01T10:00:00Z' };
const TWO = { id: 's2', title: 'a throwaway', agent: 'general-purpose', created_at: '2026-08-02T10:00:00Z' };

function archivedRow(over) {
  return { ...over, archived_at: '2026-08-20T09:00:00Z' };
}

// rowFor finds a session card by id in either list, and its buttons by
// label. The panel has no ids on rows, so this is how a test reaches one.
function rowFor(app, listID, id) {
  for (const el of app.el(listID).querySelectorAll('.session-item')) {
    if (el.dataset && el.dataset.id === id) return el;
    if ((el.textContent || '').includes(id)) return el;
  }
  return null;
}

function buttonIn(el, label) {
  return Array.from(el.querySelectorAll('button')).find((b) => b.textContent === label);
}

// The active row for a session, found by its title since that is what the
// card shows.
function activeRow(app, title) {
  for (const el of app.el('session-list').querySelectorAll('.session-item')) {
    if ((el.textContent || '').includes(title)) return el;
  }
  return null;
}

// The two lists come from one endpoint, told apart by ?archived=1. The
// harness strips the query before matching a route key, so a static
// 'GET /api/sessions?archived=1' entry would quietly answer with the
// active list; a function route is handed the query and can tell them
// apart, which is what the daemon does too.
function sessionRoutes(activeRows, archivedRows) {
  return (body, { query }) => (query.get('archived') ? archivedRows : activeRows);
}

async function withArchive({ active = [ONE, TWO], inArchive = [], ...routes } = {}) {
  return load({
    routes: {
      'GET /api/sessions': sessionRoutes(active, inArchive),
      'POST /api/sessions/s1/archive': ONE,
      'POST /api/sessions/s1/retrieve': ONE,
      ...routes,
    },
  });
}

test('the archive starts collapsed and costs no request', async () => {
  const app = await withArchive();
  assert.equal(app.el('archive-list').hidden, true);
  assert.equal(app.el('archive-toggle').getAttribute('aria-expanded'), 'false');
  assert.equal(app.callsTo('GET', '/api/sessions').filter((c) => c.query.get('archived')).length, 0,
    'the archive was fetched before anybody asked for it');
});

test('opening it fetches the archive and says when there is nothing in it', async () => {
  const app = await withArchive();
  await app.el('archive-toggle').click();
  await app.settle();

  const archivedCalls = app.callsTo('GET', '/api/sessions').filter((c) => c.query.get('archived'));
  assert.equal(archivedCalls.length, 1, 'the archive list was not fetched');
  assert.equal(app.el('archive-list').hidden, false);
  assert.equal(app.el('archive-toggle').getAttribute('aria-expanded'), 'true');
  assert.match(app.el('archive-list').textContent, /Nothing archived/);
  // The empty state names both ways in, since one of them is a gesture
  // nothing on screen would otherwise suggest.
  assert.match(app.el('archive-list').textContent, /Drag a conversation here/);
});

test('the archive button puts a conversation away and does not ask first', async () => {
  const app = await withArchive({ inArchive: [archivedRow(ONE)] });

  const row = activeRow(app, 'the one about parsers');
  assert.ok(row, 'no card for the session');
  await buttonIn(row, 'archive').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/s1/archive').length, 1);
  // No confirm: delete confirms because it cannot be undone, and a
  // confirm on a reversible action teaches people to click through the
  // one that matters. If archiving started confirming, the fake confirm
  // answers true and this would still pass, so the real assertion is the
  // button's own class below.
  assert.ok(!buttonIn(row, 'archive').className.includes('danger-btn'),
    'archive is drawn as the irreversible action');
});

test('an archived row offers retrieve, and no way to open it', async () => {
  const app = await withArchive({ inArchive: [archivedRow(ONE)] });
  await app.el('archive-toggle').click();
  await app.settle();

  const rows = app.el('archive-list').querySelectorAll('.session-item');
  assert.equal(rows.length, 1);
  const row = rows.item(0);
  assert.ok(row.className.includes('archived'), row.className);
  assert.ok(!row.className.includes('session-led'), 'an archived row is marked as the open one');
  assert.equal(row.draggable, undefined, 'an archived row is draggable, so it offers a gesture the daemon refuses');

  const labels = Array.from(row.querySelectorAll('button'), (b) => b.textContent);
  assert.deepEqual(labels, ['retrieve', 'delete']);
});

test('retrieve brings it back and refreshes both lists', async () => {
  const app = await withArchive({ inArchive: [archivedRow(ONE)] });
  await app.el('archive-toggle').click();
  await app.settle();

  const before = app.callsTo('GET', '/api/sessions').length;
  const row = app.el('archive-list').querySelectorAll('.session-item').item(0);
  await buttonIn(row, 'retrieve').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/s1/retrieve').length, 1);
  assert.ok(app.callsTo('GET', '/api/sessions').length > before,
    'the active list was not reloaded, so the retrieved conversation is not in it');
});

// The drag. One way only: retrieve is a button, which keeps a single
// drag-state variable and no cross-rejection matrix.
test('dragging a session onto the archive header archives it', async () => {
  const app = await withArchive();
  const row = activeRow(app, 'the one about parsers');
  row.fire('dragstart', { dataTransfer: { setData() {}, effectAllowed: '' } });
  const target = app.el('archive-toggle');
  target.fire('dragover', { dataTransfer: {}, preventDefault() {} });
  assert.ok(target.className.includes('drag-over'), 'the drop target did not light up');
  // The label changes too, so the state is readable without perceiving a
  // colour change.
  assert.match(target.textContent, /drop to archive/);

  target.fire('drop', { preventDefault() {}, stopPropagation() {} });
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/s1/archive').length, 1);
});

test('the archive header is inert when nothing is being dragged', async () => {
  const app = await withArchive();
  const target = app.el('archive-toggle');

  target.fire('dragover', { dataTransfer: {}, preventDefault() {} });
  assert.ok(!target.className.includes('drag-over'),
    'the header lit up for a drag that was not a session');

  target.fire('drop', { preventDefault() {}, stopPropagation() {} });
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/s1/archive').length, 0);
});

// A refusal is shown as it came. The daemon's 409 body names the tasks or
// schedules still running and says what to do, which is the useful part.
test('a refused archive says why and leaves the list alone', async () => {
  const app = await withArchive({
    'POST /api/sessions/s1/archive': {
      status: 409,
      body: 'session s1 has 2 background task(s) still running; wait for them or cancel them first',
    },
  });

  await buttonIn(activeRow(app, 'the one about parsers'), 'archive').click();
  await app.settle();

  assert.match(app.transcript(), /failed to archive/);
  assert.match(app.transcript(), /background task/);
});

// Another client archived the conversation this one is looking at.
test('being archived elsewhere moves this client off it, and says so', async () => {
  // The daemon has already moved it, so the list this client reloads no
  // longer holds it. That is the shape of the real event.
  const app = await load({
    routes: { 'GET /api/sessions': sessionRoutes([TWO], [archivedRow(ONE)]) },
  });
  app.state.sessionID = 's1';

  app.sse.emit({ type: 'session.archived', data: { session: 's1', archived: true } });
  await app.settle();

  assert.equal(app.state.sessionID, 's2', 'the page stayed on a conversation it cannot work in');
  // Said after the switch, not before it: selectSession clears the
  // transcript on its way in, so a notice appended first is wiped by it
  // and the conversation changes under the reader with no explanation.
  assert.match(app.transcript(), /archived elsewhere/,
    'the conversation changed with nothing said about why');
});
