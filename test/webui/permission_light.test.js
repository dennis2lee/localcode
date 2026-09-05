'use strict';

// The light under the prompt box, after a permission request ends any way
// other than by pressing one of its own buttons.
//
// Those buttons clear the pending id themselves (see resolvePermission).
// Every other way out — the turn stopped, another window answered, an
// unattended request given up on — arrives only as permission.resolved,
// and that handler closed the modal and unlocked the composer while
// leaving the id set. The dot then said "waiting for you to answer a
// permission request", amber, with nothing on screen to answer, until the
// page was reloaded.
//
// Found by stopping a turn that was waiting on a bash call.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const dot = (app) => app.el('comm-dot');

function ask(app, seq) {
  app.sse.emit({
    seq,
    type: 'permission.request',
    data: { id: 'p1', tool: 'bash', description: 'bash {"command": "rm -rf dist"}', can_always: true },
  });
}

test('stopping a turn that was asking puts the light back', async () => {
  const app = await load();

  app.sse.emit({ seq: 1, type: 'session.activity', data: { session: 'sess-1', busy: true } });
  ask(app, 2);
  assert.equal(dot(app).classList.contains('asking'), true, 'the dot should be amber while a question is open');
  assert.equal(app.permissionRequest.isOpen, true);

  // Stop. The daemon answers the question it was holding on the way out.
  app.sse.emit({ seq: 3, type: 'turn.cancelled', data: {} });
  app.sse.emit({
    seq: 4,
    type: 'permission.resolved',
    data: { id: 'p1', allow: false, scope: 'once', cancelled: true },
  });
  app.sse.emit({ seq: 5, type: 'session.activity', data: { session: 'sess-1', busy: false } });

  assert.equal(app.permissionRequest.isOpen, false, 'the question went off screen');
  assert.equal(
    dot(app).classList.contains('asking'),
    false,
    'the light still says it is waiting for an answer, with nothing on screen to answer',
  );
  assert.equal(app.el('input').disabled, false, 'the composer stayed locked behind a question that is gone');
});

test('an unattended request that times out puts the light back', async () => {
  const app = await load();

  ask(app, 1);
  assert.equal(dot(app).classList.contains('asking'), true);

  // No turn.cancelled here: nobody stopped anything, the request simply
  // ran out of patience. permission.resolved is the only event that comes.
  app.sse.emit({
    seq: 2,
    type: 'permission.resolved',
    data: { id: 'p1', allow: false, scope: 'once', unanswered: true },
  });

  assert.equal(dot(app).classList.contains('asking'), false);
  assert.equal(app.permissionRequest.isOpen, false);
  assert.equal(app.el('input').disabled, false);
});

test('answering it normally still works', async () => {
  const app = await load();

  ask(app, 1);
  app.el('permission-allow').click();
  await app.settle();

  assert.equal(dot(app).classList.contains('asking'), false);
  assert.equal(app.permissionRequest.isOpen, false);
});
