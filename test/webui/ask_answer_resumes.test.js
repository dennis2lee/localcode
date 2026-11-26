'use strict';

// Resumes the order walker does not produce: two during one answer, an
// answer that gets through while the stream is down, and one resume
// putting away a refused question and a held newer one together.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const X = 'd1-i1';
const Y = 'd2-i1';
const ask = (seq, id, stale) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'], ...(stale ? { stale: true } : {}) } });
const staleResolution = (id) => ({ type: 'input.resolved', data: { id, cancelled: true, stale: true } });

function askLine(app, id = X) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

async function setup() {
  const posts = {};
  const app = await load({
    routes: {
      'POST /api/sessions/sess-1/input/*': (_b, { path }) => new Promise((resolve) => { posts[path.split('/').pop()] = resolve; }),
      'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  app.el('input').value = '1';
  app.el('send').click();
  await app.settle();
  return { app, posts };
}

test('two resumes during an answer, each with stale resolutions for X and an older question, then it fails', async () => {
  const { app, posts } = await setup();
  for (let i = 0; i < 2; i++) {
    app.sse.fail();
    app.sse.reopen();
    app.sse.emit(staleResolution('d0-i9'));
    app.sse.emit(staleResolution(X));
    await app.settle();
  }
  posts[X]({ networkError: 'connection reset' });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null);
  assert.ok(askLine(app).textContent.endsWith('\n  (left unanswered)'));
});

test('an answer that gets through while the stream is down keeps its line', async () => {
  const { app, posts } = await setup();
  app.sse.fail();
  posts[X]({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  app.sse.reopen();
  app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: X, answer: '1' } });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null);
  assert.equal(app.internals.session.answeringAsks.size, 0);
  assert.match(askLine(app).textContent, /reply in the box below/);
});

test('a resume with stale resolutions for X and a newer held Y, after X was refused', async () => {
  const { app, posts } = await setup();
  app.sse.fail();
  app.sse.reopen();
  await app.settle();
  posts[X]({ status: 404, body: { error: 'not pending' } });
  await app.settle();
  app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
  app.sse.emit(ask(500, Y));
  await app.settle();
  app.sse.fail();
  app.sse.reopen();
  app.sse.emit(staleResolution(X));
  app.sse.emit(staleResolution(Y));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null);
  assert.ok(askLine(app, X).textContent.endsWith('\n  (left unanswered)'));
  assert.ok(askLine(app, Y).textContent.endsWith('\n  (left unanswered)'));
});
