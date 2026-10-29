'use strict';

// If the POST to resolve a permission request fails after the modal was
// settled locally, the modal must come back from the stored request and
// the composer must be locked again.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function ask(app, seq, id, description) {
  app.sse.emit({
    seq,
    type: 'permission.request',
    data: { id, tool: 'bash', description: description || id, can_always: true, rule: 'rm *' },
  });
}

test('a failed permission resolve puts the modal back and re-locks the composer', async () => {
  const app = await load({
    routes: {
      'POST /api/sessions/*/permissions/*': () => ({
        status: 500,
        body: { error: 'daemon unreachable' },
      }),
    },
  });

  ask(app, 1, 'p1', 'rm -rf build/');
  assert.equal(app.permissionRequest.isOpen, true);
  assert.equal(app.el('input').disabled, true);

  app.el('permission-allow').click();
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, true, 'the modal should come back after the POST failed');
  assert.equal(app.state.pendingPermissionID, 'p1');
  assert.ok(app.el('permission-text').textContent.includes('rm -rf build/'));
  assert.equal(app.el('input').disabled, true, 'the composer should be locked again');
  assert.equal(app.el('comm-dot').classList.contains('asking'), true);
  assert.match(app.transcript(), /failed to respond to permission request/);
});

test('another request arriving before the resolve fails stays shown first', async () => {
  let rejectPost;
  const app = await load({
    routes: {
      'POST /api/sessions/*/permissions/*': () => new Promise((_, reject) => {
        rejectPost = reject;
      }),
    },
  });

  ask(app, 1, 'p1', 'first command');
  app.el('permission-allow').click();
  await app.settle();

  ask(app, 2, 'p2', 'second command');
  rejectPost(new Error('network error'));
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, true);
  assert.equal(app.state.pendingPermissionID, 'p2');
  assert.ok(app.el('permission-text').textContent.includes('second command'));
  assert.deepEqual([...app.state.pendingPermissionQueue].map((q) => q.id), ['p1']);
  assert.equal(app.el('input').disabled, true);
});

test('a request resolved by another client before the POST fails does not come back', async () => {
  let rejectPost;
  const app = await load({
    routes: {
      'POST /api/sessions/*/permissions/*': () => new Promise((_, reject) => {
        rejectPost = reject;
      }),
    },
  });

  ask(app, 1, 'p1', 'first command');
  app.el('permission-allow').click();
  await app.settle();

  app.sse.emit({ seq: 2, type: 'permission.resolved', data: { id: 'p1', allow: true, scope: 'once' } });
  rejectPost(new Error('network error'));
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, false);
  assert.equal(app.state.pendingPermissionID, null);
  assert.equal(app.el('input').disabled, false);
});

test('answering p1 then p2: p1 fails first and p2 succeeds restores p1 and not p2', async () => {
  const posts = {};
  const app = await load({
    routes: {
      'POST /api/sessions/*/permissions/*': (_body, { path }) => new Promise((resolve, reject) => {
        posts[path.split('/').pop()] = { resolve, reject };
      }),
    },
  });

  ask(app, 1, 'p1', 'first command');
  ask(app, 2, 'p2', 'second command');

  app.el('permission-allow').click();
  await app.settle();
  app.el('permission-allow').click();
  await app.settle();

  posts.p1.reject(new Error('network error'));
  await app.settle();
  posts.p2.resolve({});
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, true);
  assert.equal(app.state.pendingPermissionID, 'p1');
  assert.ok(app.el('permission-text').textContent.includes('first command'));
  assert.deepEqual([...app.state.pendingPermissionQueue].map((q) => q.id), []);
  assert.equal(app.el('input').disabled, true);
});

test('answering p1 then p2: p2 succeeds first and p1 fails restores p1 and not p2', async () => {
  const posts = {};
  const app = await load({
    routes: {
      'POST /api/sessions/*/permissions/*': (_body, { path }) => new Promise((resolve, reject) => {
        posts[path.split('/').pop()] = { resolve, reject };
      }),
    },
  });

  ask(app, 1, 'p1', 'first command');
  ask(app, 2, 'p2', 'second command');

  app.el('permission-allow').click();
  await app.settle();
  app.el('permission-allow').click();
  await app.settle();

  posts.p2.resolve({});
  await app.settle();
  posts.p1.reject(new Error('network error'));
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, true);
  assert.equal(app.state.pendingPermissionID, 'p1');
  assert.ok(app.el('permission-text').textContent.includes('first command'));
  assert.deepEqual([...app.state.pendingPermissionQueue].map((q) => q.id), []);
  assert.equal(app.el('input').disabled, true);
});
