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
