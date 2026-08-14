'use strict';

// Dragging a session card up or down the panel, and the daemon being told
// about it. The panel's default is newest-first, which sinks the
// conversation someone lives in below every throwaway one started since.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const THREE = [
  { id: 's1', title: 'one', agent: 'general-purpose', workspace: '/w' },
  { id: 's2', title: 'two', agent: 'general-purpose', workspace: '/w' },
  { id: 's3', title: 'three', agent: 'general-purpose', workspace: '/w' },
];

function ids(app) {
  return (app.el('session-list').children || []).map(el => {
    const m = (el.title || '').split('\n')[0];
    return m;
  });
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

// Dropping a card on another row puts it where that row was, and the
// daemon is told the whole order — not just the pair that moved, since a
// partial order is one where two sessions hold the same position.
test('dropping a card on another row moves it there and saves the order', async () => {
  const app = await load({ routes: { 'GET /api/sessions': THREE, 'POST /api/sessions/order': { status: 204 } } });

  const rows = app.el('session-list').children;
  assert.deepEqual(ids(app), ['s1', 's2', 's3']);

  rows[2].fire('dragstart', { dataTransfer: transfer() });
  rows[0].fire('dragover', { dataTransfer: transfer() });
  rows[0].fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(ids(app), ['s3', 's1', 's2'], 'the dragged card should take the target row\'s place');
  const posts = app.callsTo('POST', '/api/sessions/order');
  assert.equal(posts.length, 1);
  assert.deepEqual(posts[0].body.ids, ['s3', 's1', 's2']);
});

// Dragging downwards is the other direction of the same move, and it is
// the one an off-by-one gets wrong: the card is removed before it is
// reinserted, so the target's index has already shifted.
test('dragging a card downwards lands it below the rows it passed', async () => {
  const app = await load({ routes: { 'GET /api/sessions': THREE, 'POST /api/sessions/order': { status: 204 } } });

  const rows = app.el('session-list').children;
  rows[0].fire('dragstart', { dataTransfer: transfer() });
  rows[2].fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(ids(app), ['s2', 's3', 's1']);
});

// A drop on the card being dragged is not a move.
test('dropping a card on itself changes nothing', async () => {
  const app = await load({ routes: { 'GET /api/sessions': THREE, 'POST /api/sessions/order': { status: 204 } } });

  const rows = app.el('session-list').children;
  rows[1].fire('dragstart', { dataTransfer: transfer() });
  rows[1].fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(ids(app), ['s1', 's2', 's3']);
  assert.equal(app.callsTo('POST', '/api/sessions/order').length, 0);
});

// The move is applied on screen before the daemon answers, so the card
// does not visibly spring back and move again. If the daemon refuses, the
// panel has to return to what the daemon actually has rather than showing
// an order that will not survive a reload.
test('a refused reorder is reported and put back', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': THREE,
      'POST /api/sessions/order': { status: 400, body: { error: 'session s9 not found' } },
    },
  });

  const rows = app.el('session-list').children;
  rows[2].fire('dragstart', { dataTransfer: transfer() });
  rows[0].fire('drop', { dataTransfer: transfer() });
  await app.settle();

  assert.deepEqual(ids(app), ['s1', 's2', 's3']);
  assert.match(app.el('transcript').innerHTML, /could not save the session order/);
});

// dragover is what tells the browser a drop is allowed here at all. Without
// preventDefault the drop never fires and the card springs back, which
// looks exactly like a feature that does not exist.
test('dragover over another row accepts the drop', async () => {
  const app = await load({ routes: { 'GET /api/sessions': THREE, 'POST /api/sessions/order': { status: 204 } } });

  const rows = app.el('session-list').children;
  rows[0].fire('dragstart', { dataTransfer: transfer() });

  const overOther = rows[1].fire('dragover', { dataTransfer: transfer() });
  assert.equal(overOther.defaultPrevented, true, 'a different row should accept the drop');
  assert.ok(rows[1].classList.contains('drop-target'));

  const overSelf = rows[0].fire('dragover', { dataTransfer: transfer() });
  assert.equal(overSelf.defaultPrevented, false, 'the row being dragged is not a drop target');
});
