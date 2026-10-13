'use strict';

// The background task panel listed every task a conversation had ever
// started, and the only way to remove a row was to open it, wait for it to
// finish, and press Delete in its window. Each row now has its own delete,
// and the panel a delete all.
//
// A deleted row goes when the daemon's task.status "deleted" arrives, the
// record that also keeps it gone across a reload and takes it off every
// other client. These tests send that event where the daemon would.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const ROW = 'DELETE /api/sessions/sess-1/tasks/*';
const ALL = 'DELETE /api/sessions/sess-1/tasks';

let seq = 0;
function task(app, id, status, agent = 'explore') {
  app.sse.emit({ seq: ++seq, type: 'task.spawned', data: { task_id: id, agent } });
  app.sse.emit({ seq: ++seq, type: 'task.status', data: { task_id: id, status } });
}

function status(app, id, s) {
  app.sse.emit({ seq: ++seq, type: 'task.status', data: { task_id: id, status: s } });
}

function rowsOf(app, id) {
  return app.el('tasks').children.filter((r) => r.textContent.includes(id));
}

function rowOf(app, id) {
  return rowsOf(app, id)[0];
}

function deleteButtonOf(app, id) {
  const row = rowOf(app, id);
  assert.ok(row, `no row for ${id}`);
  const btn = Array.from(row.querySelectorAll('button')).find((b) => b.textContent === 'delete');
  assert.ok(btn, `the row for ${id} has no delete button`);
  return btn;
}

test('a finished task is deleted from its row at once, without opening it', async () => {
  const app = await load({ routes: { [ROW]: { status: 204 } } });
  task(app, 'task-1', 'completed');

  deleteButtonOf(app, 'task-1').click();
  await app.settle();

  assert.equal(app.callsTo('DELETE', '/api/sessions/sess-1/tasks/task-1').length, 1);
  assert.equal(app.el('prompt-modal').classList.contains('open'), false, 'a finished task was asked about');
  assert.equal(app.taskView.isOpen, false, 'the delete also opened the row it sits on');
  assert.equal(deleteButtonOf(app, 'task-1').disabled, true, 'the row can be deleted twice while it goes');

  status(app, 'task-1', 'deleted');
  assert.equal(rowOf(app, 'task-1'), undefined, 'the row is still there');
});

test('a running task asks first, because deleting it stops it', async () => {
  const app = await load({ routes: { [ROW]: { status: 204 } } });
  task(app, 'task-1', 'running');

  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  assert.match(app.el('prompt-message').textContent, /still running/);
  assert.match(app.el('prompt-message').textContent, /explore/);
  app.el('prompt-cancel').click();
  await app.settle();
  assert.equal(app.callsTo('DELETE', /\/tasks\//).length, 0, 'a refused question still deleted');
  assert.equal(deleteButtonOf(app, 'task-1').disabled, false);

  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  app.el('prompt-ok').click();
  await app.settle();
  assert.equal(app.callsTo('DELETE', '/api/sessions/sess-1/tasks/task-1').length, 1);
});

// Stopping the task has it say how it ended, and that status reaches this
// window around the reply. Taken off the panel on the reply, the row was
// drawn again by the "cancelled" behind it, nameless, until the "deleted"
// after that arrived.
test('a deleted task is not drawn again by the status its stop wrote', async () => {
  const app = await load({ routes: { [ROW]: { status: 204 } } });
  task(app, 'task-1', 'running');

  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  app.el('prompt-ok').click();
  await app.settle();

  status(app, 'task-1', 'cancelled');
  assert.equal(rowsOf(app, 'task-1').length, 1);
  assert.match(rowOf(app, 'task-1').textContent, /explore/, 'the row lost its agent');
  assert.equal(deleteButtonOf(app, 'task-1').disabled, true);

  status(app, 'task-1', 'deleted');
  assert.equal(rowsOf(app, 'task-1').length, 0);
});

// A task's progress arrives with every tool call it makes. Redrawing the
// list for each one replaced the button under the pointer, and a click
// that ended on the replacement went nowhere.
test('progress does not replace the row being clicked', async () => {
  const app = await load();
  task(app, 'task-1', 'running');
  const before = deleteButtonOf(app, 'task-1');

  app.sse.emit({ type: 'task.progress', data: { task_id: 'task-1', doing: 'bash go test ./...' } });

  assert.equal(deleteButtonOf(app, 'task-1'), before, 'the button was replaced');
  assert.match(rowOf(app, 'task-1').textContent, /bash go test/);
});

// A sub-agent its turn let go of is still working, until it says how it
// ended.
test('a detached task counts as running', async () => {
  const app = await load({ routes: { [ROW]: { status: 204 } } });
  task(app, 'task-1', 'detached');

  assert.match(app.el('status-text').textContent, /1 background task/);
  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  assert.match(app.el('prompt-message').textContent, /still running/);
  app.el('prompt-cancel').click();
  await app.settle();

  rowOf(app, 'task-1').click();
  await app.settle();
  assert.equal(app.el('task-cancel').style.display, '', 'a detached task can be stopped');
});

// No record comes for a row the daemon no longer has, so it goes on the
// reply.
test('a task another window already deleted leaves the panel too', async () => {
  const app = await load({ routes: { [ROW]: { status: 404, body: { error: 'task-1 is not a task of session sess-1' } } } });
  task(app, 'task-1', 'completed');

  deleteButtonOf(app, 'task-1').click();
  await app.settle();

  assert.equal(rowOf(app, 'task-1'), undefined);
  assert.doesNotMatch(app.el('tasks').textContent, /could not delete/);
});

test('a delete that fails says why on the row, and can be tried again', async () => {
  let reply = { status: 500, body: { error: 'disk full' } };
  const app = await load({ routes: { [ROW]: () => reply } });
  task(app, 'task-1', 'completed');

  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  assert.match(rowOf(app, 'task-1').textContent, /could not delete: .*disk full/);
  assert.equal(deleteButtonOf(app, 'task-1').disabled, false);

  reply = { status: 204 };
  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  assert.doesNotMatch(rowOf(app, 'task-1').textContent, /could not delete/, 'the old reason stayed');
  assert.equal(app.callsTo('DELETE', '/api/sessions/sess-1/tasks/task-1').length, 2);
});

test('a second click while the first delete is on its way sends nothing more', async () => {
  let answer;
  const app = await load({ routes: { [ROW]: () => new Promise((resolve) => { answer = resolve; }) } });
  task(app, 'task-1', 'completed');

  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  assert.equal(deleteButtonOf(app, 'task-1').disabled, true);
  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  answer({ status: 204 });
  await app.settle();

  assert.equal(app.callsTo('DELETE', '/api/sessions/sess-1/tasks/task-1').length, 1);
});

// The question can be on screen while the reader moves to another
// conversation. The answer is about the row they were looking at.
test('a delete confirmed after moving to another conversation sends nothing', async () => {
  const app = await load({ routes: { 'DELETE /api/sessions/*/tasks/*': { status: 204 } } });
  task(app, 'task-1', 'running');

  deleteButtonOf(app, 'task-1').click();
  await app.settle();
  app.state.sessionID = 'sess-2';
  app.el('prompt-ok').click();
  await app.settle();

  assert.equal(app.callsTo('DELETE', /\/tasks\//).length, 0);
});

test('the window on a task closes when the task is deleted anywhere', async () => {
  const app = await load();
  task(app, 'task-1', 'completed');
  rowOf(app, 'task-1').click();
  await app.settle();
  assert.equal(app.taskView.isOpen, true);

  status(app, 'task-1', 'deleted');
  await app.settle();
  assert.equal(app.taskView.isOpen, false);
});

test('delete all is there only while there is something to delete', async () => {
  const app = await load();
  assert.equal(app.el('tasks-delete-all-btn').style.display, 'none');

  task(app, 'task-1', 'completed');
  assert.equal(app.el('tasks-delete-all-btn').style.display, '');

  status(app, 'task-1', 'deleted');
  assert.equal(app.el('tasks-delete-all-btn').style.display, 'none');
});

test('delete all asks, says how many are running, and empties the panel', async () => {
  const app = await load({ routes: { [ALL]: { deleted: 3 } } });
  task(app, 'task-1', 'completed');
  task(app, 'task-2', 'running');
  task(app, 'task-3', 'failed');

  app.el('tasks-delete-all-btn').click();
  await app.settle();
  const message = app.el('prompt-message').textContent;
  assert.match(message, /all 3 background tasks/);
  assert.match(message, /1 is still running and will be stopped/);

  app.el('prompt-cancel').click();
  await app.settle();
  assert.equal(app.callsTo('DELETE', '/api/sessions/sess-1/tasks').length, 0);
  assert.equal(deleteButtonOf(app, 'task-2').disabled, false);

  rowOf(app, 'task-1').click();
  await app.settle();
  app.el('tasks-delete-all-btn').click();
  await app.settle();
  app.el('prompt-ok').click();
  await app.settle();
  assert.equal(app.callsTo('DELETE', '/api/sessions/sess-1/tasks').length, 1);
  for (const id of ['task-1', 'task-2', 'task-3']) {
    assert.equal(deleteButtonOf(app, id).disabled, true, `${id} could be deleted again while it goes`);
  }

  for (const id of ['task-1', 'task-2', 'task-3']) status(app, id, 'deleted');
  assert.match(app.el('tasks').innerHTML, /none/);
  assert.equal(app.taskView.isOpen, false, 'the window on a deleted task stayed open');
});

// A task that starts while the question is on screen was not part of it.
// If the daemon got to it, its own "deleted" record removes the row.
test('delete all leaves a task that started after the question alone', async () => {
  const app = await load({ routes: { [ALL]: { deleted: 1 } } });
  task(app, 'task-1', 'completed');

  app.el('tasks-delete-all-btn').click();
  await app.settle();
  task(app, 'task-2', 'running');
  app.el('prompt-ok').click();
  await app.settle();

  assert.equal(deleteButtonOf(app, 'task-1').disabled, true);
  assert.equal(deleteButtonOf(app, 'task-2').disabled, false, 'a task nobody was asked about is on its way out');
});

test('a delete all the daemon refuses is reported, and the rows stay usable', async () => {
  const app = await load({ routes: { [ALL]: { status: 409, body: { error: 'task task-1 has a turn in progress' } } } });
  task(app, 'task-1', 'completed');

  app.el('tasks-delete-all-btn').click();
  await app.settle();
  app.el('prompt-ok').click();
  await app.settle();

  assert.match(app.transcript(), /could not delete the background tasks/);
  assert.equal(deleteButtonOf(app, 'task-1').disabled, false);
  assert.equal(app.el('tasks-delete-all-btn').disabled, false);
});

// Every row already on its way out: there is nothing left to ask about.
test('delete all with every row already going asks nothing', async () => {
  const app = await load({ routes: { [ROW]: () => new Promise(() => {}) } });
  task(app, 'task-1', 'completed');
  deleteButtonOf(app, 'task-1').click();
  await app.settle();

  app.el('tasks-delete-all-btn').click();
  await app.settle();
  assert.equal(app.el('prompt-modal').classList.contains('open'), false);
});

test('a delete all confirmed after moving to another conversation sends nothing', async () => {
  const app = await load({ routes: { 'DELETE /api/sessions/*/tasks': { deleted: 1 } } });
  task(app, 'task-1', 'completed');

  app.el('tasks-delete-all-btn').click();
  await app.settle();
  app.state.sessionID = 'sess-2';
  app.el('prompt-ok').click();
  await app.settle();

  assert.equal(app.callsTo('DELETE', /\/tasks$/).length, 0);
});

// The row itself opens the task's window on a click, and in a browser the
// button's click reaches the row too unless it is stopped there. (The
// test DOM does not bubble, so this asks the handler directly.)
test("the row's delete does not reach the row it sits on", async () => {
  const app = await load({ routes: { [ROW]: { status: 204 } } });
  task(app, 'task-1', 'completed');
  let stopped = false;
  deleteButtonOf(app, 'task-1').fire('click', { stopPropagation() { stopped = true; } });
  await app.settle();
  assert.equal(stopped, true);
});
