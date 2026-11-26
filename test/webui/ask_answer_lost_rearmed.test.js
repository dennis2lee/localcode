'use strict';

// A turn declared lost while an answer to the question the box holds is
// on its way (loading the whole conversation armed the box again) leaves
// the line to that answer's outcome, as a stale resolution does: one that
// got through keeps its line.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

const X = 'd1-i1';
const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });

function askLine(app, id = X) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

async function type(app, text) {
  app.el('input').value = text;
  app.el('send').click();
  await app.settle();
}

test('a lost turn with the box re-armed for a question whose answer is on its way leaves the line to that answer', async () => {
  let busy = false;
  let resolvePost;
  const app = await load({
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }],
      'POST /api/sessions/*/input/*': () => new Promise((resolve) => { resolvePost = resolve; }),
    },
  });
  await type(app, 'pick a colour');
  busy = true;
  assert.equal(app.state.waiting, true, 'precondition: this page\'s turn');
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick a colour' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  await type(app, 'red');
  assert.ok(resolvePost, 'precondition: the answer is on its way');

  // The whole conversation, from the daemon still waiting on X: the box is
  // armed for X again.
  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick a colour' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, X, 'precondition: the replay armed the box again');
  assert.equal(app.internals.session.answeringAsks.get(X).inFlight, true, 'precondition: the answer is still on its way');

  app.sse.fail();
  await app.settle();
  busy = false;
  app.sse.reopen();
  await app.settle();
  for (let i = 0; i < 40 && app.state.waiting; i++) await app.wait(100);
  assert.match(app.el('transcript').textContent, /did not finish/, 'precondition: the turn was declared lost');
  assert.equal(app.internals.session.pendingAsk, null, 'precondition: the box is disarmed');

  resolvePost({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/,
    'the daemon took the answer, and the line says the question was left unanswered');
});
