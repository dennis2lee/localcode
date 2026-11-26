'use strict';

// An answer to an older question can still be on its way when a newer
// question is asked and answered. What the page was told about the older
// question (a stale resolution) stays with that question's answer, so
// its failure still says on its line that it was left unanswered.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const X = 'd1-i1';
const Y = 'd2-i1';
const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });
const staleResolution = (id) => ({ type: 'input.resolved', data: { id, cancelled: true, stale: true } });

function askLine(app, id) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

async function answer(app, text) {
  app.el('input').value = text;
  app.el('send').click();
  await app.settle();
}

for (const [name, failure] of [
  ['network error', { networkError: 'connection reset' }],
  ['502', { status: 502, body: { error: 'bad gateway' } }],
]) {
  for (const yOutcome of ['still on its way', 'got through']) {
    test(`an older answer told stale fails (${name}) after the newer question was answered (${yOutcome}): its line says left unanswered`, async () => {
      const posts = {};
      const app = await load({
        routes: {
          'POST /api/sessions/sess-1/input/*': (_body, { path }) => new Promise((resolve) => { posts[path] = resolve; }),
        },
      });
      app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
      app.sse.emit(ask(401, X));
      await app.settle();
      await answer(app, '1');
      assert.ok(posts[`/api/sessions/sess-1/input/${X}`], 'precondition: X answered');

      // X's daemon stopped. The successor's resumed stream says nobody
      // waits on X; a turn there asks Y, and Y is answered.
      app.sse.fail();
      app.sse.reopen();
      app.sse.emit(staleResolution(X));
      await app.settle();
      app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
      app.sse.emit(ask(500, Y));
      await app.settle();
      assert.equal(app.internals.session.pendingAsk, Y, 'precondition: the box is Y\'s');
      await answer(app, '2');
      assert.ok(posts[`/api/sessions/sess-1/input/${Y}`], 'precondition: Y answered');
      if (yOutcome === 'got through') {
        posts[`/api/sessions/sess-1/input/${Y}`]({ status: 200, body: { status: 'resolved' } });
        await app.settle();
      }

      posts[`/api/sessions/sess-1/input/${X}`](failure);
      await app.settle();
      assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: X\'s answer failed');
      const line = askLine(app, X).textContent;
      assert.ok(line.endsWith('\n  (left unanswered)'),
        `X, told stale, its answer failed, still reads ${JSON.stringify(line.split('\n').pop())}`);
    });
  }
}

// The same slot, with a 404 recorded for X before Y: the stale resolution
// that follows (the next resume after one whose whole-log read failed) no
// longer finds X's record once Y has been answered.
test('a refused older answer is not said left unanswered once a newer question was answered', async () => {
  const posts = {};
  const app = await load({
    routes: {
      'POST /api/sessions/sess-1/input/*': (_body, { path }) => new Promise((resolve) => { posts[path] = resolve; }),
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  await answer(app, '1');
  // A resume with no stale resolution (the successor's whole-log read
  // failed): only the 404 says X is not waited on.
  app.sse.fail();
  app.sse.reopen();
  await app.settle();
  posts[`/api/sessions/sess-1/input/${X}`]({ status: 404, body: { error: `question ${X} is not pending` } });
  await app.settle();
  app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
  app.sse.emit(ask(500, Y));
  await app.settle();
  await answer(app, '2');
  // The next resume reads the log and puts X away.
  app.sse.fail();
  app.sse.reopen();
  app.sse.emit(staleResolution(X));
  await app.settle();
  const line = askLine(app, X).textContent;
  assert.ok(line.endsWith('\n  (left unanswered)'),
    `X, refused and then put away stale, still reads ${JSON.stringify(line.split('\n').pop())}`);
});
