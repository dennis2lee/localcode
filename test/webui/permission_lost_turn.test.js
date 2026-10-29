'use strict';

// An answer in flight when the turn that asked is declared lost. The
// daemon that would resolve the question is gone, so when the answer's
// POST then fails, putting the question back would lock the composer
// under a question nobody will ever ask again.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

test('an answer in flight when the turn is lost does not bring its question back', async () => {
  let busy = false;
  let rejectPost;
  const app = await load({
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }],
      'POST /api/sessions/*/permissions/*': () => new Promise((_, reject) => {
        rejectPost = reject;
      }),
    },
  });

  app.el('input').value = 'clean the build';
  app.el('send').click();
  await app.settle();
  busy = true;
  assert.equal(app.state.waiting, true);

  app.sse.emit({
    seq: 1,
    type: 'permission.request',
    data: { id: 'p1', tool: 'bash', description: 'rm -rf build/', can_always: true, rule: 'rm -rf build/' },
  });
  assert.equal(app.permissionRequest.isOpen, true);

  // Answered; the POST is on its way to a daemon that is about to go.
  app.el('permission-allow').click();
  await app.settle();

  app.sse.fail();
  await app.settle();
  busy = false;
  app.sse.reopen();
  await app.settle();
  for (let i = 0; i < 40 && app.state.waiting; i++) await app.wait(100);
  assert.equal(app.state.waiting, false, 'the turn was not declared lost');
  assert.match(app.el('transcript').textContent, /did not finish/);
  assert.equal(app.el('input').disabled, false, 'the composer is unlocked once the turn is lost');

  // Now the answer's POST fails: the daemon it went to is gone.
  rejectPost(new Error('network error'));
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, false,
    'a question from a turn declared lost came back on screen');
  assert.equal(app.state.pendingPermissionID, null);
  assert.equal(app.el('input').disabled, false,
    'the composer is locked under a question no daemon is asking');
});
