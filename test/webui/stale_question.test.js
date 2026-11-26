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
  assert.match(text, / {2}1\. a\n {2}2\. b/, 'the options it offered are part of the record');
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

// A page that saw a question live from a daemon that has since stopped
// still holds it when the resume brings no stale resolution for it: an
// update's handover, or a resume whose read of the whole log failed. The
// stream resumes after the question, so no copy arrives, and a page only
// watching the turn is not told the turn was lost. Loading the
// whole conversation replays the log from the start without starting the
// page over, and the new daemon sends the question stale. The page has to
// put it away, or the transcript says "left unanswered" about a question
// the box or the modal is still holding.
async function heldAcrossARestart(question) {
  const app = await load({ routes: { 'POST /api/sessions/sess-1/input/*': { status: 404, body: { error: 'not pending' } } } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(question(401, false));
  await app.settle();
  app.sse.fail();
  app.sse.reopen();
  await app.settle();
  return app;
}

async function showWholeConversation(app, ...frames) {
  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();
  assert.match(app.sse.url, /\/events$/, 'precondition: the whole log was asked for');
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  for (const f of frames) app.sse.emit(f);
  await app.settle();
}

test('a stale copy of the question the box is armed for disarms it', async () => {
  const q = (seq, stale) => ask(seq, 'd1-i1', 'which one?', stale);
  const app = await heldAcrossARestart(q);
  assert.equal(app.internals.session.pendingAsk, 'd1-i1', 'precondition: the live question armed the box');

  await showWholeConversation(app, q(401, true));
  assert.match(app.el('transcript').textContent, /\(left unanswered\)/);
  assert.equal(app.internals.session.pendingAsk, null, 'the box is still armed for a question left unanswered');

  app.el('input').value = 'hello';
  app.el('send').click();
  await app.settle();
  assert.equal(app.callsTo('POST', /\/input\//).length, 0, 'the message went to the stale question');
  assert.equal(app.callsTo('POST', /\/messages$/).length, 1, 'the message was not sent as a prompt');
});

test('a stale copy of the permission request on screen takes it down', async () => {
  const q = (seq, stale) => permission(seq, 'd1-p1', 'rm -rf build/', stale);
  const app = await heldAcrossARestart(q);
  assert.equal(app.state.pendingPermissionID, 'd1-p1', 'precondition: the live request is on screen');

  await showWholeConversation(app, q(401, true));
  assert.equal(app.permissionRequest.isOpen, false, 'the modal stays up for a request nobody is asking');
  assert.equal(app.state.pendingPermissionID, null);
  assert.equal(app.el('input').disabled, false, 'the composer stays locked on it');
  assert.equal(app.el('comm-dot').classList.contains('asking'), false, 'the light still says it is waiting for you');
});

test('a stale copy of another question leaves the one held alone', async () => {
  const app = await load();
  app.sse.emit(permission(1, 'd2-p1', 'go test ./...', false));
  app.sse.emit(ask(2, 'd2-i1', 'which other one?', false));
  app.sse.emit(permission(3, 'd1-p1', 'rm -rf build/', true));
  app.sse.emit(ask(4, 'd1-i1', 'which one?', true));
  await app.settle();

  assert.equal(app.permissionRequest.isOpen, true);
  assert.equal(app.state.pendingPermissionID, 'd2-p1', 'a stale copy of d1-p1 took down d2-p1');
  assert.equal(app.internals.session.pendingAsk, 'd2-i1', 'a stale copy of d1-i1 disarmed the box for d2-i1');
});

// A stale copy of a request queued behind the one on screen takes it out
// of the queue too. The queue's order is not always the log's: an answer
// that fails to reach the daemon puts its question back behind the one
// now on screen, and a daemon that has just stopped is what makes an
// answer fail.
test('a stale copy of a queued permission request takes it out of the queue', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/permissions/*': { status: 502, body: { error: 'daemon unreachable' } } },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(permission(401, 'd1-p1', 'rm -rf build/', false));
  app.sse.emit(permission(402, 'd1-p2', 'rm -rf dist/', false));
  await app.settle();
  app.el('permission-allow').click();
  await app.settle();
  assert.equal(app.state.pendingPermissionID, 'd1-p2', 'precondition: d1-p2 is on screen');
  assert.deepEqual([...app.state.pendingPermissionQueue].map((q) => q.id), ['d1-p1'], 'precondition: d1-p1 is queued');
  app.sse.fail();
  app.sse.reopen();
  await app.settle();

  await showWholeConversation(app, permission(401, 'd1-p1', 'rm -rf build/', true), permission(402, 'd1-p2', 'rm -rf dist/', true));
  assert.equal(app.permissionRequest.isOpen, false, `the modal is up for ${app.state.pendingPermissionID}, a request nobody is asking`);
  assert.equal(app.state.pendingPermissionID, null);
  assert.deepEqual([...app.state.pendingPermissionQueue], []);
  assert.equal(app.el('input').disabled, false, 'the composer is locked on a stale request');
});

// A stale copy of the request on screen brings up the one queued behind
// it, as a resolution does: here a question the new daemon asked.
test('a stale copy of the request on screen brings up the live one queued behind it', async () => {
  const app = await heldAcrossARestart((seq, stale) => permission(seq, 'd1-p1', 'rm -rf build/', stale));
  app.sse.emit(permission(402, 'd2-p1', 'go test ./...', false));
  await app.settle();
  assert.equal(app.state.pendingPermissionID, 'd1-p1', 'precondition: the held request is on screen');
  assert.deepEqual([...app.state.pendingPermissionQueue].map((q) => q.id), ['d2-p1'], 'precondition: the live one is queued');

  await showWholeConversation(app, permission(401, 'd1-p1', 'rm -rf build/', true), permission(402, 'd2-p1', 'go test ./...', false));
  assert.equal(app.state.pendingPermissionID, 'd2-p1');
  assert.deepEqual([...app.state.pendingPermissionQueue].map((q) => q.id), [], 'd2-p1 is on screen and still queued behind itself');
  assert.doesNotMatch(app.el('permission-text').textContent, /more permission request/);
});

// An answer on its way when the stale copy arrives is to a question the
// daemon says it is not waiting on, so its failure does not bring the
// question back, as with a resolution.
test('a stale copy of a request being answered keeps it away when the answer fails', async () => {
  let rejectPost;
  const app = await load({
    routes: {
      'POST /api/sessions/*/permissions/*': () => new Promise((_, reject) => { rejectPost = reject; }),
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(permission(401, 'd1-p1', 'rm -rf build/', false));
  await app.settle();
  app.el('permission-allow').click();
  await app.settle();
  assert.equal(app.state.pendingPermissionID, null, 'precondition: answered here');
  app.sse.fail();
  app.sse.reopen();
  await app.settle();

  await showWholeConversation(app, permission(401, 'd1-p1', 'rm -rf build/', true));
  rejectPost(new Error('network error'));
  await app.settle();
  assert.equal(app.permissionRequest.isOpen, false, 'the failed answer brought back a request nobody is asking');
  assert.equal(app.state.pendingPermissionID, null);
  assert.equal(app.el('input').disabled, false);
});

// The resolutions a daemon sends a resuming stream for questions before
// its resume point that nobody is waiting on: no seq, marked stale (see
// staleResolutions in the daemon). A page that saw the question live from
// a daemon that has since stopped holds it until one arrives.
function staleResolution(type, id) {
  return type === 'permission'
    ? { type: 'permission.resolved', data: { id, allow: false, unanswered: true, stale: true } }
    : { type: 'input.resolved', data: { id, cancelled: true, stale: true } };
}

test('a resumed stream puts away the questions the page held', async () => {
  const app = await load();
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(permission(401, 'd1-p1', 'rm -rf build/', false));
  app.sse.emit(ask(402, 'd1-i1', 'which one?', false));
  app.sse.emit(permission(403, 'd1-p2', 'go test ./...', false));
  await app.settle();
  assert.equal(app.state.pendingPermissionID, 'd1-p1', 'precondition: d1-p1 is on screen');
  assert.equal(app.internals.session.pendingAsk, 'd1-i1', 'precondition: the box is armed');
  app.sse.fail();
  app.sse.reopen();
  await app.settle();
  const before = app.el('transcript').textContent;

  app.sse.emit(staleResolution('permission', 'd0-p9'));
  app.sse.emit(staleResolution('permission', 'd1-p1'));
  app.sse.emit(staleResolution('input', 'd1-i1'));
  await app.settle();
  assert.equal(app.state.pendingPermissionID, 'd1-p2', 'd1-p1 should be put away and d1-p2 brought up');
  assert.deepEqual([...app.state.pendingPermissionQueue], []);
  assert.equal(app.internals.session.pendingAsk, null, 'the box is still armed for a question nobody is waiting on');
  assert.equal(
    app.el('transcript').textContent,
    before.replace('(reply in the box below, in your own words or with a number)', '(left unanswered)'),
    'putting the questions away changed anything but the held question, which says it was left unanswered',
  );

  app.sse.emit(staleResolution('permission', 'd1-p2'));
  await app.settle();
  assert.equal(app.permissionRequest.isOpen, false);
  assert.equal(app.el('input').disabled, false);
  assert.equal(app.el('comm-dot').classList.contains('asking'), false);
});

test('a task window says a request it showed waiting was left unanswered, once', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'task.spawned', data: { task_id: 'task-1', agent: 'explore' } });
  app.sse.emit({ seq: 2, type: 'task.status', data: { task_id: 'task-1', status: 'running' } });
  app.el('tasks').children[0].click();
  await app.settle();
  const s = app.streamFor('/api/sessions/task-1/events');
  assert.ok(s, 'the task window opened no stream on the task session');
  const body = app.el('task-modal-body');
  const count = (re) => (body.textContent.match(re) || []).length;

  s.emit(permission(3, 'd1-p1', 'rm -rf build/', false));
  assert.match(body.textContent, /waiting for permission/);
  // Every resume brings the resolutions again, and for every question
  // before the resume point, drawn here or not.
  s.emit(staleResolution('permission', 'd0-p9'));
  s.emit(staleResolution('permission', 'd1-p1'));
  s.emit(staleResolution('permission', 'd1-p1'));
  assert.equal(count(/permission left unanswered/g), 1, 'the window should say it once, for the request it showed waiting');
  assert.doesNotMatch(body.textContent, /permission not answered|permission denied/);
});

// The line of a question the page held from before a restart says it was
// left unanswered once it is put away. It went on inviting a reply, and
// the box, no longer armed, sent that reply to the model as a new prompt.
test('a model question held through a restart says on its line it was left unanswered', async () => {
  const app = await load();
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1', 'which one?', false));
  await app.settle();
  app.sse.fail();
  app.sse.reopen();
  await app.settle();

  app.sse.emit(staleResolution('input', 'd0-i9'));
  await app.settle();
  assert.match(app.el('transcript').textContent, /reply in the box below/, 'a stale resolution of another question rewrote this one');

  app.sse.emit(staleResolution('input', 'd1-i1'));
  await app.settle();
  const [line] = Array.from(app.el('transcript').querySelectorAll('.msg-ask'));
  assert.ok(line.textContent.endsWith('\n  (left unanswered)'), `the held question reads: ${JSON.stringify(line.textContent)}`);
  assert.doesNotMatch(line.textContent, /reply in the box below/);
  assert.match(line.textContent, / {2}1\. a\n {2}2\. b/, 'the rewrite lost the options');
});

// Only a stale resolution says so. A question answered keeps its line as
// drawn, with the answer below it.
test('an answered model question keeps its line', async () => {
  const app = await load();
  app.sse.emit(ask(1, 'q1', 'which one?', false));
  app.sse.emit({ seq: 2, type: 'input.resolved', data: { id: 'q1', answer: 'a' } });
  await app.settle();
  const text = app.el('transcript').textContent;
  assert.match(text, /reply in the box below/);
  assert.doesNotMatch(text, /left unanswered/);
  assert.match(text, /\[answered\] a/);
});

// A task window that drew a request stale has nothing waiting, so a later
// resume's stale resolution for it adds nothing.
test('a task window that drew a request stale says so once across a resume', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'task.spawned', data: { task_id: 'task-1', agent: 'explore' } });
  app.sse.emit({ seq: 2, type: 'task.status', data: { task_id: 'task-1', status: 'running' } });
  app.el('tasks').children[0].click();
  await app.settle();
  const s = app.streamFor('/api/sessions/task-1/events');
  s.emit(permission(3, 'pA-1', 'rm -rf build/', true));
  s.fail();
  s.reopen();
  s.emit(staleResolution('permission', 'pA-1'));
  const said = (app.el('task-modal-body').textContent.match(/permission left unanswered/g) || []).length;
  assert.equal(said, 1, `the window said "left unanswered" ${said} times`);
});

// A task window reopened after its daemon stopped draws the request stale
// and forgets what the first opening drew waiting.
test('a task window reopened on a task whose question was left open says so once', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'task.spawned', data: { task_id: 'task-1', agent: 'explore' } });
  app.sse.emit({ seq: 2, type: 'task.status', data: { task_id: 'task-1', status: 'running' } });
  app.el('tasks').children[0].click();
  await app.settle();
  const first = app.streamFor('/api/sessions/task-1/events');
  first.emit(permission(3, 'pA-1', 'rm -rf build/', false));
  const body = app.el('task-modal-body');
  assert.match(body.textContent, /waiting for permission/, 'precondition: drawn waiting');

  app.el('task-close').click();
  await app.settle();
  app.el('tasks').children[0].click();
  await app.settle();
  const second = app.streamFor('/api/sessions/task-1/events');
  assert.notEqual(second, first, 'precondition: a new stream');
  second.emit(permission(3, 'pA-1', 'rm -rf build/', true));
  assert.doesNotMatch(body.textContent, /waiting for permission/);
  second.fail();
  second.reopen();
  second.emit(staleResolution('permission', 'pA-1'));
  const said = (body.textContent.match(/permission left unanswered/g) || []).length;
  assert.equal(said, 1, `the window said "left unanswered" ${said} times`);
});
