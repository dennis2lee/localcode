'use strict';

// Pressing Enter used to leave the transcript untouched. The user line was
// drawn only from the daemon's message.user event, which is written when
// the model is actually handed the text — after the hooks, the delegation
// decision and the first request have all happened. For anything but a
// local model that is a visible pause, and the honest reading of a screen
// that does not change is "the Enter did not register", so people typed it
// again.
//
// The prompt is now echoed straight away as a dimmed line, and the real
// event replaces it. One entry per message either way, and a reload shows
// exactly the same transcript.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// count occurrences, because the failure mode of an echo is a duplicate.
function occurrences(haystack, needle) {
  return haystack.split(needle).length - 1;
}

test('the prompt appears the moment it is submitted', async () => {
  const app = await load();

  app.type('hello');
  await app.el('send').click();

  assert.deepEqual(app.userTurns(), ['hello'], app.transcript());
  assert.ok(
    app.transcript().includes('msg-user pending'),
    'the echo should be marked pending until the daemon confirms it',
  );
  assert.equal(app.el('input').value, '', 'the box should be empty as soon as it is sent');
});

test('the daemon event replaces the echo instead of duplicating it', async () => {
  const app = await load();

  app.type('hello');
  await app.el('send').click();
  await app.settle();

  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'hello' } });

  assert.deepEqual(app.userTurns(), ['hello'], app.transcript());
  assert.ok(!app.transcript().includes('pending'), 'the pending line survived the real one');
});

test('the same prompt sent twice keeps one line each', async () => {
  const app = await load();

  app.type('again');
  await app.el('send').click();
  await app.settle();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'again' } });
  app.sse.emit({ seq: 2, type: 'turn.done', data: {} });
  await app.settle();

  app.type('again');
  await app.el('send').click();
  await app.settle();
  app.sse.emit({ seq: 3, type: 'message.user', data: { text: 'again' } });

  assert.deepEqual(app.userTurns(), ['again', 'again'], app.transcript());
  assert.ok(!app.transcript().includes('pending'), app.transcript());
});

// A local command never reaches the daemon, so no message.user event is
// coming to replace anything. It writes its own line (see tryLocalCommand)
// and the echo must not add a second one.
test('a local command keeps its own single line', async () => {
  const app = await load();

  app.type('/help');
  await app.el('send').click();
  await app.settle();

  assert.deepEqual(app.userTurns(), ['/help'], app.transcript());
  assert.ok(!app.transcript().includes('pending'), 'a command left a pending line nothing would ever resolve');
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 0, '/help is not a message');
});

test('a send that fails takes its echo down with it', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/messages': { status: 500, body: { error: 'nope' } } },
  });

  app.type('hello');
  await app.el('send').click();
  await app.settle();

  assert.ok(app.transcript().includes('Error:'), app.transcript());
  assert.ok(
    !app.userTurns().includes('hello'),
    'the echo claimed the prompt was sent, above the error saying it was not',
  );
  assert.equal(app.state.waiting, false);
});

// A prompt typed into a turn that is already running says something
// different — the model picks it up at its next step, not now — so it keeps
// its own wording rather than looking like an ordinary line.
test('a prompt sent mid-turn still explains itself', async () => {
  const app = await load();
  app.state.waiting = true;

  app.type('skip the tests');
  await app.el('send').click();
  await app.settle();

  assert.ok(app.transcript().includes('[sent — the model will pick this up at its next step]'), app.transcript());

  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'skip the tests', injected: true } });
  assert.equal(occurrences(app.transcript(), 'skip the tests'), 1, app.transcript());
});
