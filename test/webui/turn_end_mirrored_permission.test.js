'use strict';

// A turn's end puts away the model questions the page holds and not a
// permission request mirrored from a background task into this
// conversation: background tasks run off the daemon's root context, not the
// turn's, so the task goes on after the turn ends or is stopped and still
// waits on its request. The TUI keeps it on screen the same way.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const mirrored = (seq) => ({
  seq,
  type: 'permission.request',
  data: {
    id: 'paaaaaa-1', tool: 'bash', description: '[background task t-1] ls',
    rule: 'ls *', can_always: false, once: false, task_session: 't-1',
  },
});

for (const end of ['turn.done', 'turn.cancelled']) {
  test(`the parent's ${end} leaves a background task's mirrored permission request on screen`, async () => {
    const app = await load();
    app.sse.emit({ seq: 10, type: 'message.user', data: { text: 'look into it in the background' } });
    app.sse.emit({ seq: 11, type: 'task.spawned', data: { task_id: 't-1', agent: 'explore', prompt: 'list' } });
    app.sse.emit(mirrored(12));
    await app.settle();
    assert.equal(app.internals.session.pendingPermissionID, 'paaaaaa-1', 'precondition: the mirrored request is on screen');

    app.sse.emit({ seq: 13, type: end, data: {} });
    await app.settle();
    assert.equal(app.internals.session.pendingPermissionID, 'paaaaaa-1',
      `the parent's ${end} took the background task's request off screen; the task still waits on it, and nothing here can answer it until a reload`);
  });
}

// The cancelled turn's own request goes, even when turn.cancelled arrives
// before its permission.resolved: the light and the composer do not wait
// on the order of the two.
test('a cancelled turn takes its own permission request down before its resolution arrives', async () => {
  const app = await load();
  app.sse.emit({ seq: 10, type: 'message.user', data: { text: 'clean up' } });
  app.sse.emit({ seq: 11, type: 'permission.request', data: { id: 'paaaaaa-2', tool: 'bash', description: 'rm -rf build/', rule: 'rm *' } });
  await app.settle();
  assert.equal(app.internals.session.pendingPermissionID, 'paaaaaa-2', 'precondition: the request is on screen');

  app.sse.emit({ seq: 12, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(app.internals.session.pendingPermissionID, null, 'the cancelled turn\'s own request stayed on screen');
  assert.equal(app.el('input').disabled, false);
});
