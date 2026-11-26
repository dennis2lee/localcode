'use strict';

// A question that lost the box to a newer one, never answered or answered
// with an answer that failed while the newer one held the box, still has
// its line. The next time the page is told nobody waits on it (a later
// resume's stale resolution), the line says it was left unanswered.

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

// With no answer at all: the newer question takes the box from X, and the
// stale resolution of X still reaches X's line.
test('a question displaced from the box by a newer one, then told stale: its line says left unanswered', async () => {
  const app = await load();
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  app.sse.fail();
  app.sse.reopen();
  await app.settle();
  app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
  app.sse.emit(ask(500, Y));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, Y, 'precondition: the box is Y\'s');
  app.sse.fail();
  app.sse.reopen();
  app.sse.emit(staleResolution(X));
  await app.settle();
  const line = askLine(app, X).textContent;
  assert.ok(line.endsWith('\n  (left unanswered)'),
    `X, told stale, never answered, reads ${JSON.stringify(line.split('\n').pop())}`);
});

for (const [name, failure] of [
  ['network error', { networkError: 'connection reset' }],
  ['502', { status: 502, body: { error: 'bad gateway' } }],
]) {
  for (const yState of ['Y still held', 'Y answered elsewhere']) {
    test(`an older answer failing (${name}) behind a newer question, then told stale (${yState}): its line says left unanswered`, async () => {
      const posts = {};
      const app = await load({
        routes: {
          'POST /api/sessions/sess-1/input/*': (_body, { path }) => new Promise((resolve) => { posts[path.split('/').pop()] = resolve; }),
        },
      });
      app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
      app.sse.emit(ask(401, X));
      await app.settle();
      app.el('input').value = '1';
      app.el('send').click();
      await app.settle();
      assert.ok(posts[X], 'precondition: X answered');

      // X's daemon stops; the successor's resume brings no stale
      // resolution (the walker's quietRestart). A turn there asks Y.
      app.sse.fail();
      app.sse.reopen();
      await app.settle();
      app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
      app.sse.emit(ask(500, Y));
      await app.settle();
      assert.equal(app.internals.session.pendingAsk, Y, 'precondition: the box is Y\'s');

      // X's answer, sent to the daemon that stopped, fails. The box is
      // Y's, so X is not put back.
      posts[X](failure);
      await app.settle();
      assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: X\'s answer failed');
      assert.equal(app.internals.session.pendingAsk, Y, 'precondition: the box is still Y\'s');
      if (yState === 'Y answered elsewhere') {
        app.sse.emit({ seq: 501, type: 'input.resolved', data: { id: Y, answer: 'a' } });
        await app.settle();
      }

      // The next resume reads the log and puts X away.
      app.sse.fail();
      app.sse.reopen();
      app.sse.emit(staleResolution(X));
      await app.settle();
      assert.equal(app.internals.session.answeringAsks.size, 0, 'precondition: nothing on its way');
      const line = askLine(app, X).textContent;
      assert.ok(line.endsWith('\n  (left unanswered)'),
        `X, told stale with nothing on its way, reads ${JSON.stringify(line.split('\n').pop())}`);
    });
  }
}
