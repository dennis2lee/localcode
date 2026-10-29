'use strict';

// A question the daemon serves stale is not put in front of the person.
//
// The daemon marks a replayed permission.request or input.request with
// "stale": true when it is not waiting on it and nothing in the log
// answers it: a question a stopped daemon left open, or one copied into a
// fork. Shown, the permission modal locked the composer on a question
// nobody would resolve, and the model's question took the next message as
// an answer. Both answers got the daemon's 404. A live question is still
// shown.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function permission(seq, id, description, stale) {
  const data = { id, tool: 'bash', description, can_always: true, rule: 'rm *' };
  if (stale) data.stale = true;
  return { seq, type: 'permission.request', data };
}

function ask(seq, id, question, stale) {
  const data = { id, question, options: ['a', 'b'] };
  if (stale) data.stale = true;
  return { seq, type: 'input.request', data };
}

test('a stale permission request opens no modal and leaves the composer unlocked', async () => {
  const app = await load();
  app.sse.emit(permission(1, 'd1-p1', 'rm -rf build/', true));
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, false, 'the modal opened for a question nobody is asking');
  assert.equal(app.state.pendingPermissionID, null);
  assert.deepEqual([...app.state.pendingPermissionQueue], []);
  assert.equal(app.el('input').disabled, false, 'the composer is locked on a stale question');
  assert.equal(app.el('comm-dot').classList.contains('asking'), false);
});

test('a live permission request after a stale one is shown first', async () => {
  const app = await load();
  app.sse.emit(permission(1, 'd1-p1', 'rm -rf build/', true));
  app.sse.emit(permission(2, 'd2-p1', 'go test ./...', false));
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, true);
  assert.equal(app.state.pendingPermissionID, 'd2-p1');
  assert.ok(app.el('permission-text').textContent.includes('go test ./...'));
  assert.deepEqual([...app.state.pendingPermissionQueue], [], 'the stale request was queued behind the live one');
  assert.equal(app.el('input').disabled, true);
});

test('a stale model question is drawn as the record and does not take the next message', async () => {
  const app = await load();
  app.sse.emit(ask(1, 'd1-i1', 'which one?', true));
  await app.settle();

  assert.equal(app.internals.session.pendingAsk, null, 'the box was armed for a question nobody will take');
  const text = app.el('transcript').textContent;
  assert.match(text, /\[the model is asking\] which one\?/, 'what the model asked is part of the record');
  assert.match(text, /\(left unanswered\)/);
  assert.doesNotMatch(text, /reply in the box below/, 'the line invites an answer nobody will take');

  app.el('input').value = 'a';
  app.el('send').click();
  await app.settle();
  assert.equal(app.callsTo('POST', /\/input\//).length, 0, 'the message went to the stale question');
  assert.equal(app.callsTo('POST', /\/messages$/).length, 1, 'the message was not sent as a prompt');
});

test('a live model question after a stale one takes the next message', async () => {
  const app = await load({ routes: { 'POST /api/sessions/sess-1/input/d2-i1': { status: 204 } } });
  app.sse.emit(ask(1, 'd1-i1', 'which one?', true));
  app.sse.emit(ask(2, 'd2-i1', 'which other one?', false));
  await app.settle();

  assert.equal(app.internals.session.pendingAsk, 'd2-i1');
  assert.match(app.el('transcript').textContent, /reply in the box below/);

  app.el('input').value = 'b';
  app.el('send').click();
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/input/d2-i1').length, 1);
});

test('a task window says a stale permission request was left unanswered', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'task.spawned', data: { task_id: 'task-1', agent: 'explore' } });
  app.sse.emit({ seq: 2, type: 'task.status', data: { task_id: 'task-1', status: 'running' } });
  app.el('tasks').children[0].click();
  await app.settle();
  const s = app.streamFor('/api/sessions/task-1/events');
  assert.ok(s, 'the task window opened no stream on the task session');

  s.emit(permission(3, 'd1-p1', 'rm -rf build/', true));
  const body = app.el('task-modal-body');
  assert.match(body.textContent, /left unanswered/);
  assert.match(body.textContent, /rm -rf build\//, 'it should say which request it was');
  assert.doesNotMatch(body.textContent, /waiting for permission/, 'the task is not waiting on it');

  s.emit(permission(4, 'd2-p1', 'go test ./...', false));
  assert.match(body.textContent, /waiting for permission: \[bash\] go test/);
});
