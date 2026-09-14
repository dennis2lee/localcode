'use strict';

// More than one permission request can be outstanding at once — an
// Orchestrate fanout raises up to four — and the broker keeps one
// question per waiter rather than one flag for the session. The page
// must show the oldest unresolved request, show the next when one
// resolves, and unlock the composer only when the queue empties.
//
// Two failures this guards: answering the second of two requests closed
// the first without answering it, stranding its turn; and on log replay
// a request answered days ago closed a live modal, because any
// permission.resolved closed the modal regardless of its id.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function ask(app, seq, id, description) {
  app.sse.emit({
    seq,
    type: 'permission.request',
    data: { id, tool: 'bash', description: description || id, can_always: true, rule: 'bash(*)' },
  });
}

function resolve(app, seq, id) {
  app.sse.emit({ seq, type: 'permission.resolved', data: { id, allow: true, scope: 'once' } });
}

const modalText = (app) => app.el('permission-text').textContent;
const modalOpen = (app) => app.permissionRequest.isOpen;
// Spread into this realm first: the queue lives in the harness's VM
// context, and assert.deepStrictEqual treats a cross-realm array as a
// different structure even when its contents match.
const queueIDs = (app) => [...app.state.pendingPermissionQueue].map((q) => q.id);

test('a second request waits behind the first instead of replacing it', async () => {
  const app = await load();
  ask(app, 1, 'p1', 'first command');
  ask(app, 2, 'p2', 'second command');

  assert.equal(app.state.pendingPermissionID, 'p1');
  assert.ok(modalText(app).includes('first command'), modalText(app));
  assert.deepEqual(queueIDs(app), ['p2']);
  assert.equal(modalOpen(app), true);
});

test('resolving the second request leaves the first on screen', async () => {
  const app = await load();
  ask(app, 1, 'p1', 'first command');
  ask(app, 2, 'p2', 'second command');
  resolve(app, 3, 'p2');

  assert.equal(app.state.pendingPermissionID, 'p1');
  assert.ok(modalText(app).includes('first command'), modalText(app));
  assert.deepEqual(queueIDs(app), []);
  assert.equal(modalOpen(app), true);
  assert.equal(app.el('input').disabled, true);
});

test('resolving the shown request promotes the next one', async () => {
  const app = await load();
  ask(app, 1, 'p1', 'first command');
  ask(app, 2, 'p2', 'second command');
  resolve(app, 3, 'p1');

  assert.equal(app.state.pendingPermissionID, 'p2');
  assert.ok(modalText(app).includes('second command'), modalText(app));
  assert.equal(modalOpen(app), true);
  assert.equal(app.el('input').disabled, true);
});

test('a resolution for another id leaves a live modal alone', async () => {
  const app = await load();
  ask(app, 1, 'p1', 'first command');
  resolve(app, 2, 'p9');

  assert.equal(app.state.pendingPermissionID, 'p1');
  assert.equal(modalOpen(app), true);
  assert.equal(app.el('input').disabled, true);
});

// Both halves of every permission live in the log, so opening a session
// replays each old answer in turn. Without the drop, answering nothing
// fills the queue with answered questions from last week.
test('replay drops answered pairs and shows the live request', async () => {
  const app = await load();
  ask(app, 1, 'p-old-1');
  resolve(app, 2, 'p-old-1');
  ask(app, 3, 'p-old-2');
  resolve(app, 4, 'p-old-2');
  ask(app, 5, 'p-live', 'live command');

  assert.equal(app.state.pendingPermissionID, 'p-live');
  assert.deepEqual(queueIDs(app), []);
  assert.ok(modalText(app).includes('live command'), modalText(app));
});

test('the composer unlocks only when the queue empties', async () => {
  const app = await load();
  ask(app, 1, 'p1', 'first command');
  ask(app, 2, 'p2', 'second command');

  app.el('permission-allow').click();
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/permissions/p1').length, 1);
  assert.equal(modalOpen(app), true, 'the second request should have come up');
  assert.ok(modalText(app).includes('second command'), modalText(app));
  assert.equal(app.el('input').disabled, true, 'the composer unlocked with a question still on screen');

  app.el('permission-allow').click();
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/permissions/p2').length, 1);
  assert.equal(modalOpen(app), false);
  assert.equal(app.el('input').disabled, false);
});

test('cancelling with two outstanding shows the second', async () => {
  const app = await load();
  ask(app, 1, 'p1', 'first command');
  ask(app, 2, 'p2', 'second command');

  app.sse.emit({ seq: 3, type: 'turn.cancelled', data: {} });
  // The daemon answers the cancelled question on its way out; that
  // resolution must not take the promoted one down with it.
  resolve(app, 4, 'p1');

  assert.equal(app.state.pendingPermissionID, 'p2');
  assert.ok(modalText(app).includes('second command'), modalText(app));
  assert.equal(modalOpen(app), true);
});
