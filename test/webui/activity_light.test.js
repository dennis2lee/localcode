'use strict';

// The light under the prompt box and the light on the session's row in the
// left panel report the same thing — "the model is working on this
// conversation" — and they used to read different sources. The panel reads
// the daemon's busy flag; the dot read session.waiting, this client's own
// belief, which is false in every situation where the client did not start
// the turn itself or has since been reset:
//
//   - the page was reloaded, or the session was switched away and back,
//     while a turn was running
//   - the turn was started from another client (the TUI, a second tab)
//
// The result was a solid green dot under the prompt and a blinking one in
// the panel three inches away, about the same turn.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

const dot = (app) => app.el('comm-dot');

test('the dot blinks for a turn this client did not start', async () => {
  const app = await load();

  assert.equal(app.state.waiting, false, 'nothing was sent from here');
  assert.equal(dot(app).classList.contains('active'), false, 'idle session, idle dot');

  // Another client sent a prompt: the daemon says the session is busy.
  app.sse.emit({ seq: 1, type: 'session.activity', data: { session: 'sess-1', busy: true } });

  assert.equal(
    dot(app).classList.contains('active'),
    true,
    'the dot sat solid while the session panel blinked about the same turn',
  );
  assert.equal(app.el('stop-btn').hidden, false, 'a running turn should be stoppable from here');
  assert.match(app.el('status-text').textContent, /working…/);

  app.sse.emit({ seq: 2, type: 'session.activity', data: { session: 'sess-1', busy: false } });
  assert.equal(dot(app).classList.contains('active'), false, 'the dot kept blinking after the turn ended');
});

test('a reload into a working session finds the dot already blinking', async () => {
  // No activity event is coming — nothing changes at load time, the busy
  // flag simply arrives with the session listing.
  const sessions = defaultRoutes()['GET /api/sessions'].map((s) => ({ ...s, busy: true }));
  const app = await load({ routes: { 'GET /api/sessions': sessions } });

  assert.equal(app.state.waiting, false, 'a fresh page has sent nothing');
  assert.equal(
    dot(app).classList.contains('active'),
    true,
    'the listing said this session was busy and the dot ignored it',
  );
});

test('a busy session elsewhere leaves this dot alone', async () => {
  const app = await load();

  app.sse.emit({ seq: 1, type: 'session.activity', data: { session: 'other-session', busy: true } });

  assert.equal(
    dot(app).classList.contains('active'),
    false,
    'another conversation working is not this conversation working',
  );
});

// Esc and the stop button share cancelTurn, and the button is offered
// whenever the daemon says the session is busy — so the function has to
// accept that situation too, or the button does nothing when pressed.
test('stop works on a turn this client did not start', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'session.activity', data: { session: 'sess-1', busy: true } });

  await app.cancelTurn();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 1, 'stop sent nothing to the daemon');
});

// A background task keeps the light blinking after the turn that launched
// it has ended.
//
// This is the case the light used to get wrong, and it got it wrong in the
// direction that matters: a task deliberately outlives its turn and holds
// no turn slot, so the daemon's busy flag goes false the moment the
// launching turn ends. The light then read "connected to the model, idle"
// about a conversation with several agents still working in it.
test('the light blinks while a background task is still running', async () => {
  const app = await load();
  const dot = app.el('comm-dot');

  app.sse.emit({ type: 'task.spawned', data: { task_id: 't1', agent: 'explore' } });
  await app.settle();
  assert.ok(dot.className.includes('active'),
    'a launched task left the light solid, so a working conversation reads as idle');
  assert.match(dot.title, /background task/);

  app.sse.emit({ type: 'task.status', data: { task_id: 't1', status: 'running' } });
  await app.settle();
  assert.ok(dot.className.includes('active'), dot.className);

  app.sse.emit({ type: 'task.status', data: { task_id: 't1', status: 'completed' } });
  await app.settle();
  assert.ok(!dot.className.includes('active'),
    'the light kept blinking after the last task finished');
  assert.match(dot.title, /idle/);
});

// A turn and a task at once: the turn is what the tooltip names, since it
// is the one holding the prompt box.
test('a turn takes precedence over a task in what the light says', async () => {
  const app = await load();
  const dot = app.el('comm-dot');

  app.sse.emit({ type: 'task.spawned', data: { task_id: 't1', agent: 'explore' } });
  app.state.waiting = true;
  app.sse.emit({ type: 'task.status', data: { task_id: 't1', status: 'running' } });
  await app.settle();

  assert.ok(dot.className.includes('active'));
  assert.match(dot.title, /running your prompt/);
});

// One task finishing while another is still going must not put the light
// out: the question is whether any work is in flight, not the last event.
test('the light stays lit while any task is still going', async () => {
  const app = await load();
  const dot = app.el('comm-dot');

  app.sse.emit({ type: 'task.spawned', data: { task_id: 't1', agent: 'explore' } });
  app.sse.emit({ type: 'task.spawned', data: { task_id: 't2', agent: 'oracle' } });
  app.sse.emit({ type: 'task.status', data: { task_id: 't1', status: 'completed' } });
  await app.settle();

  assert.ok(dot.className.includes('active'),
    'one task finishing put the light out while another was still running');

  app.sse.emit({ type: 'task.status', data: { task_id: 't2', status: 'failed' } });
  await app.settle();
  assert.ok(!dot.className.includes('active'), dot.className);
});
