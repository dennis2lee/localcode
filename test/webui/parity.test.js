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

test('a model chosen apart from the agent shows in the status bar', async () => {
  const app = await load();
  app.sse.emit({
    type: 'model.changed',
    data: { agent: 'general-purpose', profile: 'big', model: 'a-much-larger-model', source: 'conversation' },
  });

  assert.equal(app.state.chosenModel, 'a-much-larger-model');
  assert.match(app.el('status-text').textContent, /a-much-larger-model/);
});

test('going back to the agent stops overriding the readout', async () => {
  const app = await load();
  app.sse.emit({ type: 'model.changed', data: { model: 'a-much-larger-model', source: 'conversation' } });
  app.sse.emit({ type: 'model.changed', data: { model: 'muse-glimmer-30b', source: 'agent' } });

  // Cleared rather than pinned to the agent's current model: the status
  // bar reads that from the agent, which is where it stays right when the
  // agent is switched.
  assert.equal(app.state.chosenModel, '');
});

test('stopping a turn stops the queued prompts claiming they were sent', async () => {
  const app = await load();
  app.state.waiting = true;
  // Typed while a turn was running: shown as sent, with a promise the
  // model will pick it up at its next step.
  app.type('and also check the tests');
  await app.el('send').click();
  await app.settle();
  assert.match(app.transcript(), /the model will pick this up/);

  app.sse.emit({ type: 'turn.cancelled', data: {} });

  // The daemon drops the queue with the turn, so that promise was about a
  // message nobody has.
  assert.doesNotMatch(app.transcript(), /the model will pick this up/);
  assert.match(app.transcript(), /not sent — the turn was stopped/);
  // And the words are kept: taking them away silently is the other half
  // of the same fault.
  assert.match(app.transcript(), /and also check the tests/);
});

test('reasoning is not painted while /thinking is off', async () => {
  const app = await load();
  app.sse.emit({ type: 'thinking.delta', data: { text: 'weighing it up' } });
  assert.match(app.transcript(), /weighing it up/);

  app.sse.emit({ type: 'settings.changed', data: { show_thinking: false } });
  app.sse.emit({ type: 'thinking.end', data: {} });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'this should not appear' } });

  assert.doesNotMatch(app.transcript(), /this should not appear/);
  // The deltas still arrive and are simply not painted, so turning it
  // back on mid-turn shows the rest rather than nothing until next turn.
  app.sse.emit({ type: 'settings.changed', data: { show_thinking: true } });
  app.sse.emit({ type: 'thinking.delta', data: { text: 'and now it does' } });
  assert.match(app.transcript(), /and now it does/);
});

test('a turn boundary carries a time only while /timestamps is on', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.user', data: { text: 'first' } });
  assert.doesNotMatch(app.transcript(), /turn-time/);

  app.sse.emit({ type: 'settings.changed', data: { show_timestamps: true } });
  app.sse.emit({ type: 'message.user', data: { text: 'second' } });
  assert.match(app.transcript(), /turn-time/);
});
