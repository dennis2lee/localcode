'use strict';

// A turn's end puts away the model questions the page holds and not a
// permission request mirrored from a background task into this
// conversation: background tasks run off the daemon's root context, not the
// turn's, so the task goes on after the turn ends or is stopped and still
// waits on its request. The TUI keeps it on screen the same way.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const mirrored = (seq, id = 'paaaaaa-1', task = 't-1') => ({
  seq,
  type: 'permission.request',
  data: {
    id, tool: 'bash', description: `[background task ${task}] ls`,
    rule: 'ls *', can_always: false, once: false, task_session: task,
  },
});
const own = (seq, id) => ({
  seq, type: 'permission.request',
  data: { id, tool: 'bash', description: 'rm -rf build/', rule: 'rm *' },
});
const resolved = (seq, id) => ({ seq, type: 'permission.resolved', data: { id, allow: false, scope: 'once' } });

// What the reader sees of the permission request and the composer.
function view(app) {
  return {
    on: app.internals.session.pendingPermissionID,
    // Spread first: an array made in the page's realm is not deepEqual to
    // one made here.
    queue: [...app.internals.session.pendingPermissionQueue].map((q) => q.id),
    open: app.permissionRequest.isOpen,
    locked: app.el('input').disabled,
    hint: app.el('input').placeholder,
    asking: app.el('comm-dot').classList.contains('asking'),
    title: app.el('comm-dot').title,
  };
}

for (const end of ['turn.done', 'turn.cancelled']) {
  test(`the parent's ${end} leaves a background task's mirrored permission request on screen`, async () => {
    // The same request arriving with no turn running: what the page shows
    // once the turn is over.
    const idle = await load();
    idle.sse.emit(mirrored(11));
    await idle.settle();

    const app = await load();
    app.sse.emit({ seq: 10, type: 'message.user', data: { text: 'look into it in the background' } });
    app.setWaiting(true);
    app.sse.emit({ seq: 11, type: 'task.spawned', data: { task_id: 't-1', agent: 'explore', prompt: 'list' } });
    app.sse.emit(mirrored(12));
    await app.settle();
    assert.equal(app.internals.session.pendingPermissionID, 'paaaaaa-1', 'precondition: the mirrored request is on screen');

    app.sse.emit({ seq: 13, type: end, data: {} });
    await app.settle();
    assert.deepEqual(view(app), view(idle),
      `after the parent's ${end}, the background task's request is not shown as it is with no turn: the task still waits on it`);
  });

  test(`the parent's ${end} keeps every queued request from background tasks, in order`, async () => {
    const app = await load();
    app.sse.emit({ seq: 10, type: 'message.user', data: { text: 'two tasks' } });
    app.sse.emit(mirrored(11, 'paaaaaa-1', 't-1'));
    app.sse.emit(mirrored(12, 'paaaaaa-2', 't-2'));
    app.sse.emit({ seq: 13, type: end, data: {} });
    await app.settle();
    assert.deepEqual([view(app).on, view(app).queue], ['paaaaaa-1', ['paaaaaa-2']], `the parent's ${end} dropped the second task's request`);

    app.sse.emit(resolved(14, 'paaaaaa-1'));
    await app.settle();
    assert.equal(view(app).on, 'paaaaaa-2', 'the second task\'s request did not come up when the first was answered');
  });
}

// The cancelled turn's own request goes, even when turn.cancelled arrives
// before its permission.resolved: the light and the composer do not wait
// on the order of the two.
test('a cancelled turn takes its own permission request down before its resolution arrives', async () => {
  const app = await load();
  app.sse.emit({ seq: 10, type: 'message.user', data: { text: 'clean up' } });
  app.sse.emit(own(11, 'paaaaaa-2'));
  await app.settle();
  assert.equal(app.internals.session.pendingPermissionID, 'paaaaaa-2', 'precondition: the request is on screen');

  app.sse.emit({ seq: 12, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(app.internals.session.pendingPermissionID, null, 'the cancelled turn\'s own request stayed on screen');
  assert.equal(app.el('input').disabled, false);
});

// With a background task's request queued behind it, too: the queue is
// not what decides, the request on screen is.
test('a cancelled turn takes its own request down with a background task\'s request queued behind it', async () => {
  const app = await load();
  app.sse.emit({ seq: 10, type: 'message.user', data: { text: 'clean up' } });
  app.sse.emit(own(11, 'paaaaaa-2'));
  app.sse.emit(mirrored(12, 'paaaaaa-1'));
  await app.settle();
  assert.equal(view(app).on, 'paaaaaa-2', 'precondition: the own request is on screen');

  app.sse.emit({ seq: 13, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.deepEqual([view(app).on, view(app).queue], ['paaaaaa-1', []],
    'the cancelled turn\'s own request was not replaced by the background task\'s');
  assert.equal(view(app).locked, true, 'the background task\'s request is on screen with the composer unlocked');
});
