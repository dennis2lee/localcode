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
