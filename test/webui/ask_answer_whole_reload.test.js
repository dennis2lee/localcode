'use strict';

// The whole-conversation reload replays an older question that was
// answered as a request without "stale", ahead of the one an answer from
// here is on its way to. That older request is not a newer question: the
// answer is not marked stale by it, and when it fails, the question the
// daemon still waits on keeps its line and its box.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const A = 'qaaaaaa-1';
const C = 'qaaaaaa-2';
const REPLY = '  (reply in the box below, in your own words or with a number)';

const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });

function lineOf(app, id) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

// The log on disk: an earlier turn whose question A was answered, then the
// turn asking C, which the daemon is still waiting on.
function wholeLog(app, withA) {
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  if (withA) {
    app.sse.emit({ seq: 300, type: 'message.user', data: { text: 'earlier' } });
    app.sse.emit(ask(301, A));
    app.sse.emit({ seq: 302, type: 'input.resolved', data: { id: A, answer: 'a' } });
    app.sse.emit({ seq: 303, type: 'turn.done', data: {} });
  }
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, C));
}

for (const withA of [true, false]) {
  for (const [name, failure] of [
    ['network error', { networkError: 'connection reset' }],
    ['502', { status: 502, body: { error: 'bad gateway' } }],
  ]) {
    test(`${withA ? '' : 'control, no earlier question: '}an answer on its way across the whole reload fails (${name}): the question still waited on keeps its line`, async () => {
      const posts = {};
      const app = await load({
        routes: {
          'POST /api/sessions/sess-1/input/*': (_b, { path }) => new Promise((resolve) => { posts[path.split('/').pop()] = resolve; }),
        },
      });
      // The tail: C, with the banner offering the rest.
      app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
      app.sse.emit(ask(401, C));
      await app.settle();
      app.el('input').value = '1';
      app.el('send').click();
      await app.settle();
      assert.ok(posts[C], 'precondition: C answered');

      // The person loads the whole conversation while the answer is out.
      const btn = app.el('transcript').querySelector('.earlier-btn');
      assert.ok(btn, 'precondition: the banner offers the whole conversation');
      btn.click();
      await app.settle();
      wholeLog(app, withA);
      await app.settle();
      assert.equal(app.internals.session.pendingAsk, C, 'precondition: C\'s replay armed the box again');

      // The answer never reached the daemon, which still waits on C.
      posts[C](failure);
      await app.settle();
      assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: the answer failed');

      const armed = app.internals.session.pendingAsk;
      const last = lineOf(app, C).textContent.split('\n').pop();
      assert.deepEqual({ armed, last }, { armed: C, last: REPLY },
        'the box is armed for C, which the daemon still waits on, and its line must still invite the reply');
    });
  }
}
