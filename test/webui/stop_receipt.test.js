'use strict';

// A stop rewrites a prompt still drawn as sent as never sent: the daemon
// drops the stopped turn's queue. The 202 for a send carries the log's
// last seq when the daemon took it, and only a stop with a larger seq can
// have dropped it. A prompt taken at or after the stop's seq started the
// next turn, or went to it, and its message.user resolves the
// placeholder. The TUI does the same (internal/tui/stop_receipt_test.go).

const test = require('node:test');
const assert = require('node:assert/strict');

const { load: loadPage, defaultRoutes, QUEUE_RETRY_MS } = require('./harness');

// load holds the queue's retry after a busy refusal (retryQueueSoon), so
// a test looks at the queue before the retry runs and runs it with
// pastTheRetry. Waiting real time for it raced a loaded machine: the
// 300ms had passed before settle() was done.
async function load(opts) {
  const app = await loadPage(opts);
  app.queueRetry = app.holdTimers(QUEUE_RETRY_MS);
  return app;
}

async function pastTheRetry(app) {
  app.queueRetry.fire();
  await app.settle();
}

const notSent = (app) => (app.transcript().match(/not sent — the turn was stopped/g) || []).length;
const sentLines = (app) => (app.transcript().match(/the model will pick this up/g) || []).length;

// daemon answers each send with the next reply queued on it, or with a
// bare 202 when none is.
function daemon() {
  const replies = [];
  return {
    // taken queues a 202 for a message taken when the log's last seq was
    // seq; with no seq, the answer of a daemon that says none.
    taken(seq) {
      replies.push({ status: 202, body: seq === undefined ? { status: 'accepted' } : { status: 'accepted', seq } });
    },
    busy() {
      replies.push({ status: 409, body: { error: 'session s is already processing a message' } });
    },
    failed() {
      replies.push({ status: 500, body: { error: 'disk full' } });
    },
    // holdBusy makes the next send wait; the function it returns refuses
    // it as busy.
    holdBusy() {
      let refuse;
      replies.push(new Promise((resolve) => { refuse = resolve; }));
      return () => refuse({ status: 409, body: { error: 'session s is already processing a message' } });
    },
    // hold makes the next send wait; the function it returns answers it.
    hold() {
      let answer;
      replies.push(new Promise((resolve) => { answer = resolve; }));
      return (seq) => answer({ status: 202, body: { status: 'injected', seq } });
    },
    routes: { 'POST /api/sessions/*/messages': () => replies.shift() ?? { status: 202 } },
  };
}

async function send(app, text) {
  app.type(text);
  await app.el('send').click();
  await app.settle();
}

test('a prompt the daemon took at the stop is not marked as never sent', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  // Taken once the stop at 20 was written: begin waits for it.
  d.taken(20);
  await send(app, 'and also check the tests');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 0, 'a prompt the daemon took after the stop is marked as never sent');
  assert.equal(sentLines(app), 1, 'the placeholder went with the stop');

  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'and also check the tests' } });
  await app.settle();
  assert.deepEqual(app.userTurns(), ['and also check the tests'], 'the prompt is drawn once, as its message.user');
  assert.equal(sentLines(app), 0, 'the placeholder was left above the prompt');
  const html = app.transcript();
  assert.ok(html.indexOf('[cancelled]') < html.indexOf('and also check the tests'),
    'the prompt is drawn after the stop, where the log has it');
});

test('a prompt the daemon took before the stop is marked as never sent', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken(19);
  await send(app, 'and also check the tests');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'a prompt in the queue the stop dropped is not marked as never sent');
});

for (const [name, at, want] of [['taken after the stop', 20, 0], ['taken before the stop', 19, 1]]) {
  test(`a stop before the answer waits for it: ${name}`, async () => {
    const d = daemon();
    const app = await load({ routes: d.routes });
    app.state.waiting = true;
    const answer = d.hold();
    await send(app, 'run it');

    app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
    await app.settle();
    assert.equal(notSent(app), 0, 'decided before the daemon said when it took the prompt');

    answer(at);
    await app.settle();
    assert.equal(notSent(app), want);
  });
}

// The stream fell behind while two prompts were sent and taken; the
// backlog brings a stop from before either was taken.
test('a stop replayed from a backed-up stream does not mark prompts taken after it', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  d.taken(30);
  await send(app, 'first');
  app.state.waiting = true;
  d.taken(30);
  await send(app, 'second');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  app.sse.emit({ seq: 31, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 40, type: 'turn.done', data: {} });
  app.sse.emit({ seq: 41, type: 'message.user', data: { text: 'second' } });
  await app.settle();

  assert.equal(notSent(app), 0, 'a replayed stop marked prompts the daemon took after it');
  assert.deepEqual(app.userTurns(), ['first', 'second']);
});

// Another client stops the turn and runs a short one of its own before
// this page's prompt reaches the daemon.
test('another client\'s short turn between the stop and the prompt does not mark it', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  const answer = d.hold();
  await send(app, 'run it');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'what time is it' } });
  app.sse.emit({ seq: 22, type: 'turn.done', data: {} });
  await app.settle();
  answer(22);
  await app.settle();
  app.sse.emit({ seq: 23, type: 'message.user', data: { text: 'run it' } });
  await app.settle();

  assert.equal(notSent(app), 0, 'a prompt the daemon started a turn for is marked as never sent');
  assert.deepEqual(app.userTurns(), ['what time is it', 'run it']);
  assert.equal(sentLines(app), 0);
});

test('an answer with no seq leaves a stop marking the prompt', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken();
  await send(app, 'run it');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'an older daemon\'s answer, with no seq, kept the prompt drawn as sent through a stop');
});

test('a stop with no seq marks every prompt', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken(50);
  await send(app, 'taken');
  d.hold();
  await send(app, 'awaited');

  app.sse.emit({ type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 2, 'a stop with nothing to compare against left a prompt drawn as sent');
});

// Another client's message with the same words, logged before this one
// was taken, comes in a backlog after the answer. It is not this send's.
test('a message from before the send was taken does not stand for it', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken(100);
  await send(app, 'yes');

  app.sse.emit({ seq: 100, type: 'message.user', data: { text: 'yes' } });
  await app.settle();
  assert.equal(sentLines(app), 1, 'an older message with the same words resolved the placeholder');

  app.sse.emit({ seq: 101, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'the stop dropped the send, and nothing says so');
});

test('a message after the send was taken stands for it', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken(100);
  await send(app, 'yes');

  app.sse.emit({ seq: 101, type: 'message.user', data: { text: 'yes' } });
  await app.settle();
  assert.equal(sentLines(app), 0);
  assert.deepEqual(app.userTurns(), ['yes']);
});

test('a late answer for a placeholder already resolved changes nothing', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  const answer = d.hold();
  await send(app, 'run it');
  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'run it' } });
  app.sse.emit({ seq: 22, type: 'turn.cancelled', data: {} });
  await app.settle();
  const before = app.transcript();

  answer(20);
  await app.settle();
  assert.equal(app.transcript(), before);
  assert.equal(notSent(app), 0);
});

// Of two placeholders with the same words, the stop decides each by its
// own send's answer.
test('each placeholder is decided by its own send', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken(19);
  await send(app, 'again');
  const answer = d.hold();
  await send(app, 'again');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  answer(20);
  await app.settle();
  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'again' } });
  await app.settle();

  assert.equal(notSent(app), 1, 'want the send taken before the stop marked and the other drawn');
  assert.deepEqual(app.userTurns(), ['again']);
  assert.equal(sentLines(app), 0);
});

// A send refused as busy waits in this page's queue, which the daemon
// knows nothing of, so a stop takes it as dropped with the queue.
test('a prompt refused as busy and queued is marked as never sent by a stop', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.busy();
  await send(app, 'later');
  assert.deepEqual(Array.from(app.state.promptQueue), ['later'], 'precondition: the busy send is queued');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'a queued prompt went on saying it was sent after the stop dropped the queue');
});

test('a queued prompt sent again is decided by its new answer', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.busy();
  await send(app, 'later');

  // The turn ends, and the queue sends the prompt again.
  const answer = d.hold();
  app.sse.emit({ seq: 25, type: 'turn.done', data: {} });
  await app.settle();
  assert.equal(app.state.promptQueue.length, 0, 'precondition: the queue sent it');

  // Another client's turn, stopped while the second send was on its way.
  app.sse.emit({ seq: 26, type: 'turn.cancelled', data: {} });
  await app.settle();
  answer(26);
  await app.settle();
  app.sse.emit({ seq: 27, type: 'message.user', data: { text: 'later' } });
  await app.settle();
  assert.equal(notSent(app), 0, 'the stop was decided by the queue\'s refusal, not by the second send\'s answer');
  assert.deepEqual(app.userTurns(), ['later']);
});

test('a stop before an answer with no seq marks the prompt once the answer comes', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  const answer = d.hold();
  await send(app, 'run it');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();

  answer(undefined);
  await app.settle();
  assert.equal(notSent(app), 1, 'an answer that says nothing of when the prompt was taken kept it drawn as sent through the stop');
});

test('a prompt sent to an idle session and taken before a stop is marked', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  d.taken(19);
  await send(app, 'start over');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'the stop ended the turn before the model was handed the prompt, and it is still drawn as sent');
});

test('a prompt sent to an idle session, refused as busy and queued, is marked by a stop', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  d.busy();
  await send(app, 'later');
  assert.deepEqual(Array.from(app.state.promptQueue), ['later'], 'precondition: the busy send is queued');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'a queued prompt went on saying it was sent after the stop dropped the queue');
});

test('a queued prompt refused as busy again is marked by a stop', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.busy();
  await send(app, 'later');
  d.busy();
  app.sse.emit({ seq: 25, type: 'turn.done', data: {} });
  await app.settle();
  assert.deepEqual(Array.from(app.state.promptQueue), ['later'], 'precondition: queued again');

  app.sse.emit({ seq: 30, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'a prompt back in the queue went on saying it was sent after the stop dropped the queue');
});

test('a queued prompt sent again and taken before a stop is marked', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.busy();
  await send(app, 'later');
  d.taken(25);
  app.sse.emit({ seq: 25, type: 'turn.done', data: {} });
  await app.settle();
  assert.equal(app.state.promptQueue.length, 0, 'precondition: the queue sent it');

  app.sse.emit({ seq: 26, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'taken at 25, so the stop at 26 dropped it, and it is still drawn as sent');
});

// Two stops while the answer is on its way: the later one decides. A send
// taken between them was in the queue the second one dropped.
test('of two stops before the answer, the later decides', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  const answer = d.hold();
  await send(app, 'run it');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'something else' } });
  app.sse.emit({ seq: 22, type: 'turn.cancelled', data: {} });
  await app.settle();
  answer(21);
  await app.settle();
  assert.equal(notSent(app), 1, 'a send taken before the second stop is still drawn as sent');
});

// A placeholder held by a stop and resolved before its answer came: the
// answer finds nothing of its own, and leaves another send of the same
// words alone.
test('a late answer leaves another placeholder of the same words alone', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  const answer = d.hold();
  await send(app, 'again');
  d.taken(20);
  await send(app, 'again');

  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  app.sse.emit({ seq: 21, type: 'message.user', data: { text: 'again' } });
  await app.settle();
  answer(19);
  await app.settle();
  app.sse.emit({ seq: 22, type: 'message.user', data: { text: 'again' } });
  await app.settle();

  assert.equal(sentLines(app), 0, 'the second send\'s placeholder was left drawn as sent above its own message');
  assert.deepEqual(app.userTurns(), ['again', 'again']);
});

// A busy refusal that comes after a stop is for a prompt the daemon did
// not take: it would have been in the queue the stop cleared, so it goes
// the way the queue went, marked, and nothing sends it later.
test('a prompt sent to an idle session and refused as busy after a stop is marked, not queued', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  const refuse = d.holdBusy();
  await send(app, 'look at this');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  refuse();
  await app.settle();

  assert.equal(notSent(app), 1, 'a prompt the stop would have dropped with the queue is drawn as sent');
  assert.deepEqual(Array.from(app.state.promptQueue), [], 'the prompt went into the queue the stop cleared');
  await pastTheRetry(app);
  assert.equal(app.callsTo('POST', /\/messages$/).length, 1, 'a prompt marked as never sent was sent again');
});

test('a prompt sent mid-turn and refused as busy after a stop is marked, not queued', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  const refuse = d.holdBusy();
  await send(app, 'and the docs');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  refuse();
  await app.settle();

  assert.equal(notSent(app), 1, 'a prompt the stop would have dropped with the queue is drawn as sent');
  assert.deepEqual(Array.from(app.state.promptQueue), [], 'the prompt went into the queue the stop cleared');
  // Another client's turn ends; the queue has nothing to send.
  app.sse.emit({ seq: 30, type: 'turn.done', data: {} });
  await app.settle();
  assert.equal(app.callsTo('POST', /\/messages$/).length, 1, 'a prompt marked as never sent was sent again');
});

test('a queued prompt refused as busy again after a stop is marked, not queued', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  d.busy();
  await send(app, 'look at this');
  assert.deepEqual(Array.from(app.state.promptQueue), ['look at this'], 'precondition: queued, retry armed');

  const refuse = d.holdBusy();
  await pastTheRetry(app);
  assert.equal(app.callsTo('POST', /\/messages$/).length, 2, 'precondition: the retry went out');
  app.sse.emit({ seq: 20, type: 'turn.cancelled', data: {} });
  await app.settle();
  refuse();
  await app.settle();

  assert.equal(notSent(app), 1, 'a prompt the stop would have dropped with the queue is drawn as sent');
  assert.deepEqual(Array.from(app.state.promptQueue), [], 'the prompt went into the queue the stop cleared');
});

// Esc empties this page's queue, and with nothing running no stop event
// follows to say what that dropped.
test('a prompt Esc takes out of the queue is marked as never sent', async () => {
  const app = await load({
    routes: {
      'POST /api/sessions/*/messages': { status: 409, body: { error: 'session s is already processing a message' } },
      'POST /api/sessions/*/cancel': { cancelled: false },
    },
  });
  app.state.waiting = true;
  await send(app, 'later');
  assert.deepEqual(Array.from(app.state.promptQueue), ['later'], 'precondition: queued');

  await app.cancelTurn();
  await app.settle();
  assert.equal(notSent(app), 1, 'a prompt Esc removed from the queue is still drawn as sent');
  assert.equal(sentLines(app), 0);
});

// A queue resend with no line of its own on screen (the transcript was
// drawn again) removes nothing when it fails: a line with the same words
// stands for another send.
test('a failed resend with no line of its own leaves another send\'s line', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.sse.emit({ seq: 50, type: 'message.user', data: { text: 'earlier' } });
  await app.settle();
  d.busy();
  await send(app, 'again');
  assert.deepEqual(Array.from(app.state.promptQueue), ['again'], 'precondition: queued');

  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();
  assert.deepEqual(Array.from(app.state.promptQueue), ['again'], 'precondition: the redraw left the queue');

  d.taken(60);
  await send(app, 'again');
  d.failed();
  await pastTheRetry(app);
  assert.equal(app.callsTo('POST', /\/messages$/).length, 3, 'precondition: the retry went out and failed');

  app.sse.emit({ seq: 61, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'the send the stop dropped lost its line to the failed resend');
});

// A failed send removes its own placeholder, not the oldest with its
// words, which stands for a send the daemon took.
test('a failed send leaves the placeholder of a send of the same words the daemon took', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken(10);
  await send(app, 'again');
  d.failed();
  await send(app, 'again');

  app.sse.emit({ seq: 15, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'the stop dropped the send the daemon took, and nothing says so');
  assert.equal(sentLines(app), 0, 'the failed send\'s placeholder is left saying the model will pick it up');
});

test('a failed send of a prompt sent to an idle session removes its own placeholder', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken(10);
  await send(app, 'again');
  app.state.waiting = false;
  d.failed();
  await send(app, 'again');

  app.sse.emit({ seq: 15, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'the stop dropped the send the daemon took, and nothing says so');
  assert.equal(sentLines(app), 0);
});

test('a queued prompt that fails when sent again removes its own placeholder', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.state.waiting = true;
  d.taken(10);
  await send(app, 'again');
  d.busy();
  await send(app, 'again');
  d.failed();
  app.sse.emit({ seq: 11, type: 'turn.done', data: {} });
  await app.settle();
  assert.equal(app.callsTo('POST', /\/messages$/).length, 3, 'precondition: the queue sent it again');

  app.sse.emit({ seq: 15, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(notSent(app), 1, 'the stop dropped the send the daemon took, and nothing says so');
  assert.equal(sentLines(app), 0);
});

// A lost turn marks every placeholder, one whose send is still on its way
// too; a busy refusal for it then finds it marked, and it is not queued.
test('a prompt a lost turn marked is not queued when its send is refused as busy', async () => {
  const d = daemon();
  const app = await load({ routes: { ...defaultRoutes(), ...d.routes, 'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy: false }] } });
  const refuse = d.holdBusy();
  await send(app, 'first');
  app.sse.fail();
  await app.settle();
  app.sse.reopen();
  await app.settle();
  for (let i = 0; i < 40 && !/did not finish/.test(app.el('transcript').textContent); i++) await app.wait(100);
  assert.equal(notSent(app), 1, 'precondition: the lost turn marked the prompt');

  refuse();
  await app.settle();
  assert.deepEqual(Array.from(app.state.promptQueue), [], 'a prompt marked as never sent went into the queue');
  await pastTheRetry(app);
  assert.equal(app.callsTo('POST', /\/messages$/).length, 1, 'a prompt marked as never sent was sent again');
});

// The transcript drawn again while a send is on its way takes its line
// with it; a busy refusal then queues the prompt, as before there was a
// line to go by.
test('a send refused as busy after the transcript was drawn again is queued', async () => {
  const d = daemon();
  const app = await load({ routes: d.routes });
  app.sse.emit({ seq: 50, type: 'message.user', data: { text: 'earlier' } });
  await app.settle();
  const refuse = d.holdBusy();
  await send(app, 'look at this');
  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();

  refuse();
  await app.settle();
  assert.deepEqual(Array.from(app.state.promptQueue), ['look at this'], 'a refused prompt with no line on screen was dropped');
});

// Esc marks only what it took out of the queue: a send the daemon took is
// still the daemon's to hand on.
test('Esc leaves a prompt the daemon took drawn as sent', async () => {
  const d = daemon();
  const app = await load({ routes: { ...d.routes, 'POST /api/sessions/*/cancel': { cancelled: false } } });
  app.state.waiting = true;
  d.taken(10);
  await send(app, 'taken');
  d.busy();
  await send(app, 'queued');

  await app.cancelTurn();
  await app.settle();
  assert.equal(notSent(app), 1, 'want only the queued prompt marked');
  app.sse.emit({ seq: 11, type: 'message.user', data: { text: 'taken' } });
  await app.settle();
  assert.equal(sentLines(app), 0);
  assert.deepEqual(app.userTurns(), ['taken']);
});
