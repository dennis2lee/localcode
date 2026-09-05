'use strict';

// What this page believes about a turn, after the stream it was learning
// from has been away.
//
// session.waiting is the page's own memory: set when it sends a prompt,
// cleared by the turn.end that answers it. If the daemon serving that turn
// goes away — killed, crashed, or replaced by an update that did not
// finish — that event never arrives and the memory has no expiry. The
// stream drops, the light goes grey, and when the stream comes back the
// page is still saying "working…" about a turn that no longer exists, with
// a stop button that answers 502.
//
// Reproduced by starting a turn against a real daemon and killing it:
// minutes later the daemon said busy:false and the page said working….

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

test('a turn whose daemon went away stops being reported as running', async () => {
  let busy = false;
  const app = await load({
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }],
    },
  });

  // A prompt goes out: this client now remembers a turn of its own.
  app.el('input').value = 'explain the handoff';
  app.el('send').click();
  await app.settle();
  busy = true;
  assert.equal(app.state.waiting, true, 'the page should be waiting on the turn it just sent');
  assert.equal(app.el('stop-btn').hidden, false);

  // The daemon goes away mid-turn.
  app.sse.fail();
  await app.settle();
  assert.equal(app.el('comm-dot').classList.contains('connected'), false, 'the light should say the stream is down');

  // It comes back — a different process, with no memory of that turn.
  busy = false;
  app.sse.reopen();
  await app.settle();

  assert.equal(app.state.waiting, false, 'the page went on claiming a turn that no daemon is running');
  assert.equal(app.el('stop-btn').hidden, true, 'a stop button was still offered for a turn that is gone');
  assert.equal(app.el('comm-dot').classList.contains('active'), false);
  assert.match(
    app.el('transcript').textContent,
    /did not finish/,
    'the transcript should say the turn was lost rather than just going quiet',
  );
});

test('a blip that the turn survives changes nothing', async () => {
  // Idle at load, or the composer would queue the prompt behind the turn
  // the daemon says is already running instead of sending it.
  let busy = false;
  const app = await load({
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }],
    },
  });

  app.el('input').value = 'explain the handoff';
  app.el('send').click();
  await app.settle();
  // The daemon is running it, and goes on running it through the blip.
  busy = true;

  app.sse.fail();
  await app.settle();
  app.sse.reopen();
  await app.settle();

  assert.equal(app.state.waiting, true, 'a stream blip must not cancel a turn that is still running');
  assert.equal(app.el('stop-btn').hidden, false, 'the turn is still stoppable');
  assert.doesNotMatch(app.el('transcript').textContent, /did not finish/);
});
