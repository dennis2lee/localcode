'use strict';

// The daemon reports orchestration stage progress on the task.status
// channel with task_id "orchestrate:<stage>". That id is a report, not
// a session: filing it as a task drew a clickable row out of a
// zero-value task — empty agent — and the click opened a task view for
// a session id that does not exist. Stage progress is a transcript
// line, and no orchestrate: row may ever open a task view.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function stage(app, seq, status, extra) {
  app.sse.emit({
    seq,
    type: 'task.status',
    data: { task_id: 'orchestrate:find', status, stage: 'find', ...extra },
  });
}

test('stage progress is a transcript line, not a task row', async () => {
  const app = await load();
  stage(app, 1, 'running', { agents: 4 });
  stage(app, 2, 'completed', { kept: 2 });
  await app.settle();

  assert.equal(app.state.tasks.size, 0, 'stage progress built a task row');
  assert.ok(!app.el('tasks').innerHTML.includes('orchestrate:find'), app.el('tasks').innerHTML);
  const text = app.el('transcript').textContent;
  assert.ok(text.includes('[orchestrate: find running (4 agents)]'), text);
  assert.ok(text.includes('[orchestrate: find finished (2 kept)]'), text);
});

test('a stage spawn builds no row either', async () => {
  const app = await load();
  app.sse.emit({
    seq: 1,
    type: 'task.spawned',
    data: { task_id: 'orchestrate:find', agent: 'explore' },
  });

  assert.equal(app.state.tasks.size, 0, 'a stage spawn built a task row');
});

test('an orchestrate row never opens a task view', async () => {
  const app = await load();
  // Straight into the state, past the intake filter: the point is the
  // row itself must not be clickable to a session that cannot be
  // opened, whatever path drew it.
  app.state.tasks.set('orchestrate:find', { agent: '', status: 'running' });
  app.internals.renderTasks();

  assert.match(app.el('tasks').innerHTML, /orchestrate:find/);
  app.el('tasks').children[0].click();
  await app.settle();

  assert.equal(app.taskView.isOpen, false, 'clicking a stage row opened a task view for a session that does not exist');
});

test('ordinary tasks still build rows that open', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'task.spawned', data: { task_id: 'task-1', agent: 'explore' } });
  app.sse.emit({ seq: 2, type: 'task.status', data: { task_id: 'task-1', status: 'running' } });

  assert.equal(app.state.tasks.get('task-1').status, 'running');
  app.el('tasks').children[0].click();
  await app.settle();
  assert.equal(app.taskView.isOpen, true, 'a real task row stopped opening');
});
