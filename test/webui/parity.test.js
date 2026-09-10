'use strict';

// The two things a command-parity review against opencode changed on this
// side: a workspace moved from the terminal has to reach this client's
// button, and an undone turn gives its prompt back.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('a workspace moved elsewhere moves this client\'s button', async () => {
  const app = await load();
  app.state.workspacePath = '/tmp/old';

  // "/workspace <path>" typed in the terminal, arriving here as an event.
  // Before it, the button went on naming the old directory until
  // something else made the page refetch — which is how a file lands in
  // the wrong project.
  app.sse.emit({ type: 'workspace.changed', data: { path: '/tmp/moved-in-the-terminal' } });

  assert.equal(app.state.workspacePath, '/tmp/moved-in-the-terminal');
  assert.equal(app.el('workspace-btn').textContent, '/tmp/moved-in-the-terminal');
});

test('a workspace event with no path leaves the button alone', async () => {
  const app = await load();
  app.sse.emit({ type: 'workspace.changed', data: { path: '/tmp/somewhere' } });

  app.sse.emit({ type: 'workspace.changed', data: {} });

  assert.equal(app.state.workspacePath, '/tmp/somewhere');
  assert.equal(app.el('workspace-btn').textContent, '/tmp/somewhere');
});

test('rewinding gives the undone prompt back', async () => {
  const app = await load();

  app.sse.emit({
    type: 'rewound',
    data: { turn_text: 'write the parser', prompt: 'write the parser, but in one pass', restored: 2 },
  });

  // So the turn can be retyped from where it went wrong rather than from
  // nothing.
  assert.equal(app.el('input').value, 'write the parser, but in one pass');
  assert.ok(app.transcript().includes('rewound one turn'), app.transcript());
});

test('rewinding does not overwrite something already typed', async () => {
  const app = await load();
  app.el('input').value = 'something else entirely';

  app.sse.emit({ type: 'rewound', data: { prompt: 'write the parser, but in one pass' } });

  // What is in the box is a newer intention than the one being undone.
  assert.equal(app.el('input').value, 'something else entirely');
});
