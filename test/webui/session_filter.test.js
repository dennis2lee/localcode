'use strict';

// Filtering the session list by title or workspace, in the box above the
// list. Client-side only: typing narrows the rows already held, clearing
// shows them all again, and nothing is fetched.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const FOUR = [
  { id: 's1', title: 'alpha notes', agent: 'general-purpose', workspace: '/home/alice/very/long/prefix/directory/project-x' },
  { id: 's2', title: 'beta notes', agent: 'general-purpose', workspace: '/srv/other' },
  { id: 's3', title: 'gamma notes', agent: 'general-purpose', workspace: '/srv/third' },
  { id: 's4', title: 'delta notes', agent: 'general-purpose', workspace: '/srv/fourth' },
];

function shownIDs(app) {
  return (app.el('session-list').children || []).map(el => (el.title || '').split('\n')[0]);
}

function typeFilter(app, text) {
  app.el('session-filter').value = text;
  app.el('session-filter').fire('input');
}

// A fake dataTransfer: enough of one that the handlers can set the drag
// effect and stash the id without a browser.
function transfer() {
  const data = new Map();
  return {
    effectAllowed: '',
    dropEffect: '',
    setData: (k, v) => data.set(k, v),
    getData: (k) => data.get(k) || '',
  };
}

// The box narrows the panel to the sessions whose title contains the
// text, and the listing behind it is untouched — the rows are hidden,
// not removed.
test('typing in the filter box shows only sessions whose title matches', async () => {
  const app = await load({ routes: { 'GET /api/sessions': FOUR } });
  assert.deepEqual(shownIDs(app), ['s1', 's2', 's3', 's4']);

  typeFilter(app, 'alp');
  await app.settle();

  assert.deepEqual(shownIDs(app), ['s1']);
  assert.equal(app.state.sessions.length, 4, 'filtering must hide rows, not drop sessions');

  // Case is not a distinction worth making when looking for a session.
  typeFilter(app, 'BETA');
  await app.settle();
  assert.deepEqual(shownIDs(app), ['s2']);
});

// The card shortens a long workspace from the front (see shortenPath),
// so the leading directories never appear on screen. Searching for one
// of them still has to find the session: the match is against the full
// path the session recorded, not the shortened text the card displays.
test('a workspace search matches the full path, not the shortened card text', async () => {
  const app = await load({ routes: { 'GET /api/sessions': FOUR } });

  // textContent is what the card shows (the tooltip carrying the full
  // path is an attribute, not displayed text). The shortened workspace
  // on screen really does not contain the searched directory.
  const shown = app.el('session-list').textContent;
  assert.ok(!shown.includes('alice'), 'the card must really not display the searched directory:\n' + shown);

  typeFilter(app, 'alice');
  await app.settle();

  assert.deepEqual(shownIDs(app), ['s1']);
});

// Clearing the box is the undo: every session comes back, in the order
// the daemon gave.
test('clearing the filter shows every session again', async () => {
  const app = await load({ routes: { 'GET /api/sessions': FOUR } });

  typeFilter(app, 'beta');
  await app.settle();
  assert.deepEqual(shownIDs(app), ['s2']);

  typeFilter(app, '');
  await app.settle();
  assert.deepEqual(shownIDs(app), ['s1', 's2', 's3', 's4']);
});

// A filter that matches nothing is an answer, not a broken list: it says
// so, rather than showing an empty panel that reads as "no sessions".
test('a filter that matches nothing says so', async () => {
  const app = await load({ routes: { 'GET /api/sessions': FOUR } });

  typeFilter(app, 'no such session anywhere');
  await app.settle();

  assert.match(app.el('session-list').innerHTML, /no sessions match/);
});

// Drag-to-reorder counts positions in the full app.sessions array (see
// reorderList/dropSessionOn), but a filtered panel shows a subset — so a
// drop there would move a session nobody pointed at. While a filter is
// on, the rows are not draggable at all: no drag starts, no drop is
// accepted, and the daemon is told nothing.
test('dragging is disabled while a filter is active', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': FOUR,
      'POST /api/sessions/order': { status: 204 },
    },
  });

  typeFilter(app, 'notes');
  await app.settle();
  assert.deepEqual(shownIDs(app), ['s1', 's2', 's3', 's4']);

  const rows = app.el('session-list').children;
  for (const row of rows) {
    assert.equal(row.draggable, false, 'a filtered row must not offer a drag');
    assert.ok(!row.title.includes('drag'), 'a filtered row must not promise a move:\n' + row.title);
  }

  rows[3].fire('dragstart', { dataTransfer: transfer() });
  const over = rows[0].fire('dragover', { dataTransfer: transfer() });
  assert.equal(over.defaultPrevented, false, 'a filtered row must not accept a drop');
  rows[0].fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(shownIDs(app), ['s1', 's2', 's3', 's4']);
  assert.equal(app.callsTo('POST', '/api/sessions/order').length, 0);
});

// The disable is conditional on the filter, not a state the panel gets
// stuck in: once the box is cleared the same gesture moves the card and
// saves the order again.
test('clearing the filter brings dragging back', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': FOUR,
      'POST /api/sessions/order': { status: 204 },
    },
  });

  typeFilter(app, 'notes');
  await app.settle();
  typeFilter(app, '');
  await app.settle();

  const rows = app.el('session-list').children;
  rows[3].fire('dragstart', { dataTransfer: transfer() });
  rows[0].fire('dragover', { dataTransfer: transfer() });
  rows[0].fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(shownIDs(app), ['s4', 's1', 's2', 's3']);
  assert.equal(app.callsTo('POST', '/api/sessions/order').length, 1);
});
