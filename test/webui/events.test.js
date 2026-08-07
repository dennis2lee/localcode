'use strict';

// The SSE event handling and the prompt queue — the two places where the Web
// UI holds state that has to stay in step with the daemon's turn boundaries.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('the page connects its event stream to the selected session', async () => {
  const app = await load();
  assert.equal(app.sse.url, '/api/sessions/sess-1/events');
  assert.equal(app.state.connected, true);
  assert.ok(app.el('comm-dot').classList.contains('connected'));
});

test('a dropped stream turns the light off without clearing the turn', async () => {
  const app = await load();
  app.state.waiting = true;
  app.sse.fail();
  assert.equal(app.state.connected, false);
  assert.equal(app.el('comm-dot').classList.contains('connected'), false);
  assert.equal(app.state.waiting, true);
});

test('a malformed frame is logged, not thrown', async () => {
  const app = await load();
  assert.doesNotThrow(() => app.sse.raw('{not json'));
  assert.equal(app.consoleErrors.length, 1);
  assert.match(app.consoleErrors[0], /bad event/);
});

test('a user message renders escaped', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.user', data: { text: '<b>hi</b>' } });
  assert.ok(app.transcript().includes('You: &lt;b&gt;hi&lt;/b&gt;'), app.transcript());
});

// message.part.end means one model message ended, not the turn: a turn with
// tool calls streams several. Ending the wait here is what used to make a
// prompt typed during tool execution skip the queue and bounce off the
// daemon's busy flag with a 409.
test('message.part.end does not end the turn, turn.done does', async () => {
  const app = await load();
  app.setWaiting(true);
  app.applyEvent({ type: 'message.part.end' });
  assert.equal(app.state.waiting, true);
  app.applyEvent({ type: 'turn.done' });
  assert.equal(app.state.waiting, false);
});

test('tool.start and tool.end move the running-tool indicator only', async () => {
  const app = await load();
  app.setWaiting(true);
  app.applyEvent({ type: 'tool.start', data: { name: 'bash' } });
  assert.equal(app.state.runningTool, 'bash');
  assert.match(app.el('status-text').textContent, /bash…/);
  // No transcript line: tool activity lives in the status bar and vanishes
  // with the turn.
  assert.equal(app.transcript(), '');
  app.applyEvent({ type: 'tool.end', data: { name: 'bash' } });
  assert.equal(app.state.runningTool, '');
});

test('an error event ends the turn and shows the message escaped', async () => {
  const app = await load();
  app.setWaiting(true);
  app.applyEvent({ type: 'error', data: { error: 'boom <script>' } });
  assert.equal(app.state.waiting, false);
  assert.ok(app.transcript().includes('Error: boom &lt;script&gt;'), app.transcript());
});

test('a permission request locks the prompt box until it is resolved', async () => {
  const app = await load();
  app.applyEvent({
    type: 'permission.request',
    data: { id: 'perm-1', tool: 'bash', description: 'rm -rf /', can_always: true, rule: 'bash(rm *)' },
  });
  assert.ok(app.el('permission-modal').classList.contains('open'));
  assert.equal(app.el('input').disabled, true);
  assert.equal(app.el('send').disabled, true);
  assert.equal(app.el('permission-text').textContent, '[bash] rm -rf /');

  app.el('permission-allow').click();
  await app.settle();
  assert.deepEqual(app.callsTo('POST', '/api/sessions/sess-1/permissions/perm-1')[0].body, {
    allow: true,
    scope: 'once',
  });

  app.applyEvent({ type: 'permission.resolved', data: { id: 'perm-1' } });
  assert.equal(app.el('permission-modal').classList.contains('open'), false);
  assert.equal(app.el('input').disabled, false);
});

test('the always-allow button is hidden when the daemon cannot persist a rule', async () => {
  const app = await load();
  app.applyEvent({ type: 'permission.request', data: { id: 'p', tool: 'bash', can_always: false } });
  assert.equal(app.el('permission-allow-always').style.display, 'none');
  app.applyEvent({ type: 'permission.request', data: { id: 'p2', tool: 'bash', can_always: true, rule: 'bash(*)' } });
  assert.equal(app.el('permission-allow-always').style.display, '');
});

test('task events feed the sidebar and the status line', async () => {
  const app = await load();
  app.sse.emit({ type: 'task.spawned', data: { task_id: 'task-1', agent: 'plan' } });
  assert.match(app.el('tasks').innerHTML, /task-1/);
  assert.match(app.el('status-text').textContent, /1 background task/);

  app.sse.emit({ type: 'task.status', data: { task_id: 'task-1', status: 'done' } });
  assert.equal(app.state.tasks.get('task-1').status, 'done');
  assert.doesNotMatch(app.el('status-text').textContent, /background task/);
});

test('agent.switched updates the dropdown and the status line', async () => {
  const app = await load();
  app.sse.emit({ type: 'agent.switched', data: { agent: 'plan' } });
  assert.equal(app.state.currentAgent, 'plan');
  assert.equal(app.el('agent-select').value, 'plan');
  assert.match(app.el('status-text').textContent, /model: test-model-2/);
});

test('a plain prompt typed mid-turn is queued and sent when the turn finishes', async () => {
  const app = await load();
  app.setWaiting(true);

  app.type('second question');
  await app.el('send').click();
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), ['second question']);
  assert.ok(app.transcript().includes('[queued] second question'), app.transcript());
  assert.equal(app.el('input').value, '');
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 0);

  app.sse.emit({ type: 'turn.done', data: {} });
  await app.settle();

  const sent = app.callsTo('POST', '/api/sessions/sess-1/messages');
  assert.equal(sent.length, 1);
  assert.deepEqual(sent[0].body, { text: 'second question' });
  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.state.waiting, true); // the dequeued prompt started a turn
});

// Queueing a command would mean replaying it later as literal chat text to
// the model instead of running it, so commands are not queued. (The Web UI
// drops them silently; the TUI answers with an explanation. Worth aligning,
// but that is a product change, not a harness one — this test pins today's
// behaviour so a change to it is deliberate.)
test('a command typed mid-turn is not queued as chat text', async () => {
  const app = await load();
  app.setWaiting(true);
  app.type('/help');
  await app.el('send').click();
  await app.settle();
  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 0);
});

test('a 409 from the daemon queues the prompt instead of reporting an error', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/messages': { status: 409, body: { error: 'busy' } } },
  });
  app.type('hello');
  await app.el('send').click();
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), ['hello']);
  assert.ok(app.transcript().includes('[queued] hello'), app.transcript());
  assert.ok(!app.transcript().includes('Error:'), app.transcript());
  assert.equal(app.state.waiting, true);
});

test('a real send failure ends the turn and is reported', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/messages': { status: 500, body: { error: 'nope' } } },
  });
  app.type('hello');
  await app.el('send').click();
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.state.waiting, false);
  assert.ok(app.transcript().includes('Error:'), app.transcript());
});

test('Esc cancels the running turn and drops the queue immediately', async () => {
  const app = await load();
  app.setWaiting(true);
  app.state.promptQueue = ['queued one'];

  app.press('Escape');
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 1);

  app.sse.emit({ type: 'turn.cancelled', data: {} });
  assert.equal(app.state.waiting, false);
  assert.ok(app.transcript().includes('[cancelled]'), app.transcript());
});

// Regression: the reply arrived, the turn was over, and the status bar
// still said "working… esc to cancel" with the light blinking — and Esc
// did nothing at all, because the only thing that cleared the spinner was
// an event the daemon had no reason to send again. Every prompt typed
// afterwards queued behind a turn that had already finished.
//
// The daemon answers "cancelled: false" when it has nothing running.
// That answer is the client's proof its own spinner is stale.
test('Esc clears a spinner the daemon says is not running', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/cancel': { cancelled: false } },
  });
  app.setWaiting(true);
  assert.equal(app.state.waiting, true);

  app.press('Escape');
  await app.settle();

  assert.equal(app.state.waiting, false);
  // No "[cancelled]" line: nothing was cancelled, the display was just
  // wrong. Saying otherwise would be inventing an event.
  assert.ok(!app.transcript().includes('[cancelled]'), app.transcript());
});

// ...and once it is cleared, typing goes straight out instead of queuing.
test('a prompt after that stale-spinner Esc is sent, not queued', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/cancel': { cancelled: false } },
  });
  app.setWaiting(true);
  app.press('Escape');
  await app.settle();

  app.type('hello');
  app.press('Enter');
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 1);
});

// A real cancellation still comes from the event, so every client sees it.
test('a turn that really was running is still reported as cancelled', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/cancel': { cancelled: true } },
  });
  app.setWaiting(true);
  app.press('Escape');
  await app.settle();

  // Still waiting: the daemon owns the transition, and turn.cancelled is
  // what tells every attached client at once.
  assert.equal(app.state.waiting, true);
  app.sse.emit({ type: 'turn.cancelled', data: {} });
  assert.equal(app.state.waiting, false);
});

test('Esc while idle does not call the daemon', async () => {
  const app = await load();
  app.press('Escape');
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 0);
});

test('Enter sends, Shift+Enter does not', async () => {
  const app = await load();
  app.type('one');
  app.press('Enter');
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 1);

  app.type('two');
  app.press('Enter', { shiftKey: true });
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 1);
});

test('Up and Down walk prompt history and restore the draft', async () => {
  const app = await load();
  app.type('first');
  app.press('Enter');
  await app.settle();
  app.applyEvent({ type: 'turn.done' });

  app.type('second');
  app.press('Enter');
  await app.settle();
  app.applyEvent({ type: 'turn.done' });

  const input = app.type('a draft');
  input.selectionStart = input.selectionEnd = 0; // caret at the start: Up recalls

  app.press('ArrowUp');
  assert.equal(input.value, 'second');

  input.selectionStart = input.selectionEnd = 0;
  app.press('ArrowUp');
  assert.equal(input.value, 'first');

  app.press('ArrowDown');
  assert.equal(input.value, 'second');
  app.press('ArrowDown');
  assert.equal(input.value, 'a draft'); // the stashed draft comes back
});

// Known gap, found by this harness. Recall parks the caret at the end of the
// recalled text, and the Web UI only recalls when the caret is at offset 0 —
// so a second Up in a row does nothing and the caret has to be moved back to
// the start by hand. The TUI does not have this problem: its condition is
// "the cursor is on the first visual row" (internal/tui/history.go
// atInputTop), which stays true after its own CursorEnd. Pinned here as
// today's behaviour, not as desirable behaviour.
test('known gap: a second Up in a row does not walk further back', async () => {
  const app = await load();
  app.state.history = ['older', 'newer'];
  app.state.historyIdx = 2;

  const input = app.type('');
  input.selectionStart = input.selectionEnd = 0;
  app.press('ArrowUp');
  assert.equal(input.value, 'newer');

  app.press('ArrowUp'); // caret now sits at the end, so this is swallowed
  assert.equal(input.value, 'newer');
});

test('history does not record the same prompt twice in a row', async () => {
  const app = await load();
  app.rememberPrompt('same');
  app.rememberPrompt('same');
  app.rememberPrompt('other');
  assert.deepEqual(Array.from(app.state.history), ['same', 'other']);
});

test('switching session clears the transcript, queue and history', async () => {
  const app = await load();
  app.state.promptQueue = ['pending'];
  app.rememberPrompt('typed here');
  app.sse.emit({ type: 'message.user', data: { text: 'hello' } });
  assert.notEqual(app.transcript(), '');

  app.selectSession('sess-2', 'plan', '/tmp/workspace');
  await app.settle();

  assert.equal(app.state.sessionID, 'sess-2');
  assert.equal(app.transcript(), '');
  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.deepEqual(Array.from(app.state.history), []);
  assert.equal(app.sse.url, '/api/sessions/sess-2/events');
});
