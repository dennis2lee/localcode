'use strict';

// The let go button names what it let go of. A debate's reviewers are the
// turn's children all at once and are let go of together, so the answer
// can name several, and the line has to name every one of them.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('letting go of a debate panel names every reviewer task', async () => {
  const app = await load({
    routes: {
      'POST /api/sessions/*/detach': () => ({ detached: true, task_id: 'task-a', task_ids: ['task-a', 'task-b'] }),
    },
  });
  app.el('detach-btn').fire('click');
  await app.wait(0);
  assert.match(app.transcript(), /let go of task-a and task-b\. They keep working; their answers are in the task panel\./);
});

test('a daemon that names one task is still read', async () => {
  const app = await load({
    routes: {
      'POST /api/sessions/*/detach': () => ({ detached: true, task_id: 'task-a' }),
    },
  });
  app.el('detach-btn').fire('click');
  await app.wait(0);
  assert.match(app.transcript(), /let go of task-a\. It keeps working; its answer is in the task panel\./);
});
