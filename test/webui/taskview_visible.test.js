'use strict';

// The task window draws a permission question's tool and description
// through visibleText on both of its lines, the one waiting for an answer
// and the one left unanswered.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function permission(seq, id, tool, description, stale) {
  const data = { id, tool, description };
  if (stale) data.stale = true;
  return { seq, type: 'permission.request', data };
}

async function openTask(app) {
  app.sse.emit({ seq: 1, type: 'task.spawned', data: { task_id: 'task-1', agent: 'explore' } });
  app.sse.emit({ seq: 2, type: 'task.status', data: { task_id: 'task-1', status: 'running' } });
  app.el('tasks').children[0].click();
  await app.settle();
  const s = app.streamFor('/api/sessions/task-1/events');
  assert.ok(s, 'precondition: the task window opened its stream');
  return s;
}

for (const stale of [false, true]) {
  test(`a task window draws a ${stale ? 'stale' : 'waiting'} permission question with its hidden characters as escapes`, async () => {
    const app = await load();
    const s = await openTask(app);
    s.emit(permission(3, 'd1-p1', 'ba‮sh', 'rm -rf build/\r\x1b[8mhidden\x1b[0m', stale));
    const text = app.el('task-modal-body').textContent;
    assert.match(text, stale ? /permission left unanswered/ : /waiting for permission/, 'precondition: the line was drawn');
    assert.doesNotMatch(text, /[\r\x1b‮]/, `the window drew raw control characters: ${JSON.stringify(text)}`);
    assert.ok(text.includes('[ba\\u202esh]'), `the tool's mark is not shown as an escape: ${JSON.stringify(text)}`);
    assert.ok(text.includes('rm -rf build/\\r\\x1b[8mhidden'), `the description's controls are not shown as escapes: ${JSON.stringify(text)}`);
  });
}
