'use strict';

// The record of an answer typed for a model question (session.answeringAsk):
// which question it belongs to, which conversation, and which answer.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const ask = (seq, id, stale) => ({
  seq,
  type: 'input.request',
  data: { id, question: `${id}?`, options: ['a', 'b'], ...(stale ? { stale: true } : {}) },
});
const staleResolution = (id) => ({ type: 'input.resolved', data: { id, cancelled: true, stale: true } });

function askLine(app, id = 'd1-i1') {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

async function answer(app, text) {
  app.el('input').value = text;
  app.el('send').click();
  await app.settle();
}

const twoSessions = [
  { id: 'sess-1', title: 'one', agent: 'general-purpose', workspace: '/tmp/workspace' },
  { id: 'sess-2', title: 'two', agent: 'general-purpose', workspace: '/tmp/workspace' },
];

// A resumed stream brings a stale resolution for every question before the
// resume point nobody waits on, old ones from earlier daemons included.
test('a stale resolution of another question leaves an answer on its way alone', async () => {
  let settlePost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }) },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  // A blip: the daemon is still waiting on d1-i1, and puts away d0-i9, a
  // question an earlier daemon left open.
  app.sse.fail();
  app.sse.reopen();
  app.sse.emit(staleResolution('d0-i9'));
  await app.settle();
  settlePost({ networkError: 'connection reset' });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd1-i1', 'the question the daemon still waits on was not put back');
  assert.match(askLine(app).textContent, /reply in the box below/);
});

test('a refused answer is not said left unanswered by another question\'s stale resolution', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': { status: 404, body: { error: 'question d1-i1 is not pending' } } },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  // Answered in another window while the stream was down: the resume
  // opens with d0-i9's stale resolution, then the backlog's answer.
  app.sse.fail();
  app.sse.reopen();
  app.sse.emit(staleResolution('d0-i9'));
  app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: 'd1-i1', answer: 'b' } });
  await app.settle();
  assert.match(app.el('transcript').textContent, /\[answered\] b/);
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/, 'the line says left unanswered above its answer');
});

// resetSession assigns the fresh state over the old one, so a field left
// out of it survives a switch.
test('an answer that fails after a switch to another conversation arms nothing there', async () => {
  let settlePost;
  const app = await load({
    routes: {
      'GET /api/sessions': twoSessions,
      'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }),
      'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  settlePost({ networkError: 'connection reset' });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'sess-1\'s question was put back in sess-2');
  await answer(app, 'hello');
  assert.equal(app.callsTo('POST', /\/input\//).length, 1, 'a message in sess-2 went to sess-1\'s question');
  assert.equal(app.callsTo('POST', '/api/sessions/sess-2/messages').length, 1);
});

test('an answer that gets through does not forget an answer typed in another conversation', async () => {
  const settle = {};
  const app = await load({
    routes: {
      'GET /api/sessions': twoSessions,
      'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settle.one = resolve; }),
      'POST /api/sessions/sess-2/input/*': () => new Promise((resolve) => { settle.two = resolve; }),
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  app.internals.selectSession('sess-2', 'general-purpose', '/tmp/workspace');
  await app.settle();
  app.sse.emit({ seq: 10, type: 'message.user', data: { text: 'pick another' } });
  app.sse.emit(ask(11, 'd1-i2'));
  await app.settle();
  await answer(app, '2');
  settle.one({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  settle.two({ networkError: 'connection reset' });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd1-i2', 'the failed answer in sess-2 did not put its question back');
});

// The whole conversation re-arms a question the daemon still waits on,
// and a second answer can be sent while the first is on its way.
test('the first of two answers failing does not put the question back under the second', async () => {
  const pending = [];
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { pending.push(resolve); }) },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd1-i1', 'precondition: the replay armed the box again');
  await answer(app, '2');
  assert.equal(pending.length, 2, 'precondition: two answers on their way');
  pending[0]({ networkError: 'connection reset' });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'the question was put back while the second answer is on its way');
});

test('an answer that fails after a newer question leaves the newer question\'s line alone', async () => {
  let settlePost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }) },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  app.sse.fail();
  app.sse.reopen();
  app.sse.emit(staleResolution('d1-i1'));
  app.sse.emit(ask(500, 'd2-i1'));
  await app.settle();
  settlePost({ networkError: 'connection reset' });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd2-i1');
  assert.match(askLine(app, 'd2-i1').textContent, /reply in the box below/, 'the newer question\'s line was rewritten');
});

//   composer.js: `if (answering.stale) {` -> `if (answering.stale && !apiClient.isNotPending(err)) {`
// The answer and the resumed stream both reach the new daemon after a
// restart, and either reply can arrive first.
test('an answer refused after the stale resolution says its question was left unanswered', async () => {
  let settlePost;
  const app = await load({
    routes: {
      'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }),
      'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  app.sse.fail();
  await answer(app, '1');
  app.sse.reopen();
  app.sse.emit(staleResolution('d1-i1'));
  await app.settle();
  settlePost({ status: 404, body: { error: 'question d1-i1 is not pending' } });
  await app.settle();
  assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: the answer failed');
  assert.ok(askLine(app).textContent.endsWith('\n  (left unanswered)'),
    `the question reads ${JSON.stringify(askLine(app).textContent.split('\n').pop())}`);
  assert.equal(app.internals.session.answeringAsk, null, 'the refused answer is still recorded, waiting for a resolution already given');
});
