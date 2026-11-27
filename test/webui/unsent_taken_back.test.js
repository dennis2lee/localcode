'use strict';

// A stop rewrites every prompt still drawn as sent as never sent: the
// daemon drops the stopped turn's queue. A prompt that reached the daemon
// as the turn was ending is not in that queue. The daemon starts a turn for
// it once the stopped turn's end is written, so its message.user comes
// after the turn.cancelled, and the line is taken back when it does.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const accepted = { routes: { 'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } } } };
const notSent = (app) => (app.transcript().match(/not sent — the turn was stopped/g) || []).length;

async function send(app, text) {
  app.type(text);
  await app.el('send').click();
  await app.settle();
}

test('a prompt sent into a stopping turn that started the next turn is not left saying it was never sent', async () => {
  const app = await load(accepted);
  app.state.waiting = true;
  await send(app, 'and also check the tests');
  assert.match(app.transcript(), /the model will pick this up/, 'precondition: drawn as sent into the turn');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'precondition: the stop marks it as never sent');

  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'and also check the tests' } });
  await app.settle();
  assert.equal(notSent(app), 0, 'the model was handed the prompt, and the page still says it never was');
  assert.deepEqual(app.userTurns(), ['and also check the tests'], 'the prompt is drawn once, as its message.user');
  const html = app.transcript();
  assert.ok(html.indexOf('[cancelled]') < html.indexOf('and also check the tests'),
    'the prompt is drawn after the stop, where the log has it');
});

test('a prompt drawn as waiting for its own turn is taken back the same way', async () => {
  const app = await load(accepted);
  await send(app, 'start over');
  assert.deepEqual(app.userTurns(), ['start over'], 'precondition: drawn as a dimmed prompt');

  // Another client's turn, stopped while this prompt was on its way.
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'precondition: the stop marks it as never sent');

  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'start over' } });
  await app.settle();
  assert.equal(notSent(app), 0);
  assert.deepEqual(app.userTurns(), ['start over']);
});

test('a prompt the stop dropped stays marked as never sent when the next turn is another prompt', async () => {
  const app = await load(accepted);
  app.state.waiting = true;
  await send(app, 'and also check the tests');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'something else' } });
  await app.settle();
  assert.equal(notSent(app), 1, 'a different prompt took back the line of one nobody was handed');
});

test('a prompt sent again after the stop answers its own placeholder first', async () => {
  const app = await load(accepted);
  app.state.waiting = true;
  await send(app, 'run it');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();

  // Seen as never sent, and sent again into the idle session.
  await send(app, 'run it');
  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'run it' } });
  await app.settle();
  assert.equal(notSent(app), 1, 'the second send\'s message.user took back the first send\'s line instead of its own placeholder');
  assert.deepEqual(app.userTurns(), ['run it'], 'the second send\'s placeholder was left above its own line');
});

test('one message.user takes back one line of the same text', async () => {
  const app = await load(accepted);
  app.state.waiting = true;
  await send(app, 'again');
  await send(app, 'again');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 2, 'precondition: both marked as never sent');

  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'again' } });
  await app.settle();
  assert.equal(notSent(app), 1);
});

test('a line from a conversation left behind is not what a later message.user takes back', async () => {
  const app = await load(accepted);
  app.state.waiting = true;
  await send(app, 'and also check the tests');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();

  app.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  app.state.waiting = true;
  await send(app, 'and also check the tests');
  app.sse.emit({ seq: 5, type: 'turn.cancelled', data: {} });
  app.sse.emit({ seq: 6, type: 'message.user', data: { text: 'and also check the tests' } });
  await app.settle();
  assert.equal(notSent(app), 0, 'the line on screen stayed, and one from the conversation left behind was taken back instead');
});

test('the line taken back is the one from the stop the prompt raced, not one an earlier stop dropped', async () => {
  const app = await load(accepted);
  app.state.waiting = true;
  await send(app, 'run it');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'something else' } });
  await app.settle();

  // The same words, sent into the next turn as it is stopped.
  app.state.waiting = true;
  await send(app, 'run it');
  app.sse.emit({ seq: 30, type: 'turn.cancelled', data: {} });
  app.sse.emit({ seq: 31, type: 'message.user', data: { text: 'run it' } });
  await app.settle();

  assert.equal(notSent(app), 1);
  const html = app.transcript();
  assert.ok(html.indexOf('not sent — the turn was stopped') < html.indexOf('[cancelled]'),
    'the line the first stop dropped was taken back, and the one from the stop the prompt raced is left');
});
