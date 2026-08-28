'use strict';

// A background task reported three words about itself — its agent, its id,
// and one of "spawned"/"running"/"completed" — and nothing else, ever.
// What it was doing, how far it had got, whether it was stuck: none of
// that was anywhere, because a task's own conversation is a session
// nothing listed and nothing opened. "1 background task" that never
// finishes is not a progress report.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function spawnTask(app, taskID = 'task-1') {
  app.sse.emit({ seq: 1, type: 'task.spawned', data: { task_id: taskID, agent: 'explore' } });
  app.sse.emit({ seq: 2, type: 'task.status', data: { task_id: taskID, status: 'running' } });
}

test('the panel says what a running task is doing, not just that it is running', async () => {
  const app = await load();
  spawnTask(app);
  assert.match(app.el('tasks').innerHTML, /running/);

  app.sse.emit({ type: 'task.progress', data: { task_id: 'task-1', doing: 'bash' } });
  assert.match(app.el('tasks').innerHTML, /bash/, app.el('tasks').innerHTML);
});

test('clicking a task opens its own conversation', async () => {
  const app = await load();
  spawnTask(app);

  app.el('tasks').children[0].click();
  await app.settle();

  assert.equal(app.taskView.isOpen, true, 'the task window did not open');
  assert.match(app.el('task-modal-title').textContent, /explore/);
  assert.match(app.el('task-modal-title').textContent, /task-1/);
  // A task still running is one you can stop.
  assert.equal(app.el('task-cancel').style.display, '');
});

test('a finished task can be read but not stopped', async () => {
  const app = await load();
  spawnTask(app);
  app.sse.emit({ seq: 3, type: 'task.status', data: { task_id: 'task-1', status: 'completed' } });

  app.el('tasks').children[0].click();
  await app.settle();

  assert.equal(app.taskView.isOpen, true);
  assert.equal(app.el('task-cancel').style.display, 'none');
});

test('stopping an open task asks the daemon to cancel it', async () => {
  const app = await load({ routes: { 'POST /api/tasks/task-1/cancel': { cancelled: true } } });
  spawnTask(app);
  app.el('tasks').children[0].click();
  await app.settle();

  app.el('task-cancel').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/tasks/task-1/cancel').length, 1);
});

// The window watching a task has to notice when the task ends, or it goes
// on offering a stop button for something that already finished.
test('a task that finishes while being watched updates its own window', async () => {
  const app = await load();
  spawnTask(app);
  app.el('tasks').children[0].click();
  await app.settle();
  assert.equal(app.el('task-cancel').style.display, '');

  app.sse.emit({ seq: 3, type: 'task.status', data: { task_id: 'task-1', status: 'completed' } });

  assert.match(app.el('task-modal-note').textContent, /completed/);
  assert.equal(app.el('task-cancel').style.display, 'none');
});

// openWatching opens the task window and returns the stream feeding it,
// which is the task's own session stream rather than the conversation's.
async function openWatching(app, taskID = 'task-1') {
  app.el('tasks').children[0].click();
  await app.settle();
  const s = app.streamFor(`/api/sessions/${taskID}/events`);
  assert.ok(s, 'the task window opened no stream on the task session');
  return s;
}

// A tool call that starts and never visibly ends is the half you are
// usually waiting for. tool.end was dropped on the floor here: the window
// showed work beginning and nothing at all about how it went.
test('a task shows what its tools returned, not just that they started', async () => {
  const app = await load();
  spawnTask(app);
  const s = await openWatching(app);

  s.emit({ type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{"command":"go test ./..."}' } });
  const body = app.el('task-modal-body');
  assert.match(body.innerHTML, /bash/, 'the call is not shown');
  assert.match(body.innerHTML, /running/, 'a started call should say it is running');

  s.emit({ type: 'tool.end', data: { tool_use_id: 't1', content: 'ok\nall tests pass', is_error: false } });
  assert.match(body.textContent, /all tests pass/, 'the result never reached the window');
  assert.doesNotMatch(body.innerHTML, /running…/, 'the call still says it is running after it ended');
});

test('a failed tool call is marked as failed', async () => {
  const app = await load();
  spawnTask(app);
  const s = await openWatching(app);

  s.emit({ type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{}' } });
  s.emit({ type: 'tool.end', data: { tool_use_id: 't1', content: 'exit status 1', is_error: true } });

  assert.match(app.el('task-modal-body').innerHTML, /failed/);
});

// A task blocked on a permission looked exactly like a task working,
// which is the worst thing this window could get wrong.
test('a task waiting on a permission says so', async () => {
  const app = await load();
  spawnTask(app);
  const s = await openWatching(app);

  s.emit({ type: 'permission.request', data: { id: 'p1', tool: 'bash', description: 'rm -rf build' } });
  const body = app.el('task-modal-body');
  assert.match(body.textContent, /waiting for permission/);
  assert.match(body.textContent, /rm -rf build/, 'it should say what it is waiting on');

  s.emit({ type: 'permission.resolved', data: { allowed: true } });
  assert.match(body.textContent, /permission granted/);
});

// A task can delegate, and a task waiting on its own children with
// nothing on screen about them is the same gap one level down.
test('a task shows the work it delegates', async () => {
  const app = await load();
  spawnTask(app);
  const s = await openWatching(app);

  s.emit({ type: 'task.spawned', data: { task_id: 'task-2', agent: 'librarian' } });
  assert.match(app.el('task-modal-body').textContent, /librarian/);
});

// The end of the work had no marker at all: the last reply stopped and
// you were left guessing whether more was coming.
test('a task says when it has finished', async () => {
  const app = await load();
  spawnTask(app);
  const s = await openWatching(app);

  s.emit({ type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{}' } });
  s.emit({ type: 'turn.done', data: {} });

  const body = app.el('task-modal-body');
  assert.match(body.textContent, /finished/);
  // And a call the turn ended without finishing stops spinning, rather
  // than sitting under the "finished" line forever.
  assert.doesNotMatch(body.innerHTML, /running…/);
});

// A finished task's transcript is the only thing left of it, so being
// able to throw it away is the other half of being able to read it.
test('a finished task offers a delete button, and a running one does not', async () => {
  const app = await load();
  spawnTask(app);
  await openWatching(app);
  assert.equal(app.el('task-delete').style.display, 'none', 'work still running is not for deleting');

  app.sse.emit({ seq: 3, type: 'task.status', data: { task_id: 'task-1', status: 'completed' } });
  await app.settle();
  assert.equal(app.el('task-delete').style.display, '', 'a finished task has nothing left but its record');
  assert.equal(app.el('task-cancel').style.display, 'none', 'there is nothing left to stop');
});

test('deleting a finished task removes its conversation and its row', async () => {
  const app = await load({ routes: { 'DELETE /api/sessions/task-1': { status: 204 } } });
  spawnTask(app);
  app.sse.emit({ seq: 3, type: 'task.status', data: { task_id: 'task-1', status: 'completed' } });
  app.el('tasks').children[0].click();
  await app.settle();

  app.el('task-delete').click();
  await app.settle();

  assert.equal(app.callsTo('DELETE', '/api/sessions/task-1').length, 1);
  assert.equal(app.taskView.isOpen, false, 'the window has nothing left to show');

  // The row is built from this session's own task.spawned, so it comes
  // back on the next reload unless the removal is recorded there too.
  // The daemon records it as a status; this is the client acting on it.
  app.sse.emit({ seq: 4, type: 'task.status', data: { task_id: 'task-1', status: 'deleted' } });
  assert.match(app.el('tasks').innerHTML, /none/, 'the row outlived the conversation');
});
