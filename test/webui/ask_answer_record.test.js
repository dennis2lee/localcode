'use strict';

// What an answer's outcome and a turn declared lost do with the record of
// answers on their way: a success touches only its own question, puts the
// light out when it disarms the box, and a lost turn leaves an answer on
// its way to its outcome and reaches one already refused.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

const X = 'd1-i1';
const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });

function askLine(app, id = X) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

async function answer(app, text) {
  app.el('input').value = text;
  app.el('send').click();
  await app.settle();
}

test('an answer that gets through after a switch leaves the other conversation\'s question armed', async () => {
  let settlePost;
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 'sess-1', title: 'one', agent: 'general-purpose', workspace: '/tmp/workspace' },
        { id: 'sess-2', title: 'two', agent: 'general-purpose', workspace: '/tmp/workspace' },
      ],
      'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }),
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  await answer(app, '1');
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  app.sse.emit({ seq: 10, type: 'message.user', data: { text: 'pick another' } });
  app.sse.emit(ask(11, 'd1-i2'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd1-i2', 'precondition: sess-2\'s box is armed');
  settlePost({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd1-i2', 'sess-1\'s answer getting through disarmed sess-2\'s question');
  assert.ok(app.el('comm-dot').classList.contains('asking'), 'the light stopped saying sess-2 waits on an answer');
});

test('an answer taken after the whole conversation re-armed the box puts the light out', async () => {
  let settlePost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }) },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  await answer(app, '1');
  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, X, 'precondition: the replay armed the box again');
  assert.ok(app.el('comm-dot').classList.contains('asking'), 'precondition: the light says a question waits');
  settlePost({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'precondition: the box is disarmed');
  assert.equal(app.el('comm-dot').classList.contains('asking'), false, 'the light still says a question waits, with the box disarmed');
});

async function lostTurnWith(postRoute, beforeLost) {
  let busy = false;
  const app = await load({
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }],
      'POST /api/sessions/*/input/*': postRoute,
    },
  });
  await answer(app, 'pick a colour');
  busy = true;
  assert.equal(app.state.waiting, true, 'precondition: this page\'s turn');
  app.sse.emit(ask(1, X));
  await app.settle();
  app.sse.fail();
  await app.settle();
  await beforeLost(app);
  busy = false;
  app.sse.reopen();
  await app.settle();
  for (let i = 0; i < 40 && app.state.waiting; i++) await app.wait(100);
  assert.match(app.el('transcript').textContent, /did not finish/, 'precondition: the turn was declared lost');
  return app;
}

test('a lost turn leaves an answer on its way to its outcome: one that got through keeps its line', async () => {
  let resolvePost;
  const app = await lostTurnWith(() => new Promise((resolve) => { resolvePost = resolve; }), async (a) => {
    await answer(a, 'red');
  });
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/, 'said while the answer was still on its way');
  resolvePost({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/, 'an answer the daemon took is said left unanswered');
});

test('a lost turn reaches an answer already refused: its line says left unanswered', async () => {
  const app = await lostTurnWith(
    { status: 404, body: { error: `question ${X} is not pending` } },
    async (a) => {
      await answer(a, 'red');
      assert.match(a.el('transcript').textContent, /\[could not answer:/, 'precondition: the answer was refused');
      assert.equal(a.internals.session.answeringAsks.has(X), true, 'precondition: the refusal is recorded');
    },
  );
  assert.equal(app.internals.session.pendingAsk, null);
  assert.ok(askLine(app).textContent.endsWith('\n  (left unanswered)'), `X reads ${JSON.stringify(askLine(app).textContent.split('\n').pop())}`);
});
