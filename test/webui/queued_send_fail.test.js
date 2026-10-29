'use strict';

// A queued prompt whose later send fails takes its pending line with it.
//
// A busy 409 queues the prompt and leaves the line drawn when Enter was
// pressed, which the status bar counts. When the queue sends it again
// and that send fails for any other reason, the error was shown and the
// line stayed, still saying the prompt was sent above the reason it was
// not. The first send's own failure has always removed it.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('a queued prompt that then fails to send leaves no pending line', async () => {
  let calls = 0;
  const app = await load({
    routes: {
      'POST /api/sessions/*/messages': () => {
        calls++;
        return calls === 1
          ? { status: 409, body: { error: 'session is already processing a message' } }
          : { status: 500, body: { error: 'the daemon fell over' } };
      },
    },
  });
  app.type('check the logs');
  await app.el('send').click();
  await app.settle();
  assert.deepEqual(Array.from(app.state.promptQueue), ['check the logs'], 'the busy send was not queued');
  assert.ok(app.userTurns().includes('check the logs'), 'the queued prompt should still be on screen while it waits');

  // The page retries a 409 once by itself (retryQueueSoon).
  await app.wait(400);
  await app.settle();

  assert.equal(calls, 2, 'the queue was not sent again');
  assert.ok(app.transcript().includes('the daemon fell over'), app.transcript());
  assert.ok(!app.userTurns().includes('check the logs'), `the failed send's pending line is still on screen: ${app.transcript()}`);
  assert.equal(app.state.waiting, false);
});
