'use strict';

// What happens to another question leaves an answer on its way alone:
// neither a real resolution of a newer question nor a stale copy of an
// older one touches the record of an answer to this one.

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

test('a stale copy of an older question leaves an answer on its way alone', async () => {
  let settlePost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }) },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  app.el('input').value = '1';
  app.el('send').click();
  await app.settle();
  // The whole conversation, from the daemon still waiting on X. An
  // earlier daemon left d0-i9 open, so it comes as a stale copy.
  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 5, type: 'input.request', data: { id: 'd0-i9', question: 'd0-i9?', options: [], stale: true } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, X, 'precondition: the replay armed the box again');

  settlePost({ networkError: 'connection reset' });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, X, 'precondition: the box is still X\'s');
  const line = askLine(app, X).textContent;
  assert.ok(line.endsWith('\n  (reply in the box below, in your own words or with a number)'),
    `the box is armed for X and its line reads ${JSON.stringify(line.split('\n').pop())}`);
});

test('a real resolution of a newer question leaves an older answer on its way alone', async () => {
  let settlePost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }) },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  app.el('input').value = '1';
  app.el('send').click();
  await app.settle();
  // X's daemon stopped; the successor puts X away, asks Y in a turn of its
  // own, and Y is answered in another window.
  app.sse.fail();
  app.sse.reopen();
  app.sse.emit(staleResolution(X));
  await app.settle();
  app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
  app.sse.emit(ask(500, Y));
  app.sse.emit({ seq: 501, type: 'input.resolved', data: { id: Y, answer: 'a' } });
  await app.settle();

  settlePost({ networkError: 'connection reset' });
  await app.settle();
  assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: X\'s answer failed');
  const line = askLine(app, X).textContent;
  assert.ok(line.endsWith('\n  (left unanswered)'),
    `X, told stale, its answer failed, reads ${JSON.stringify(line.split('\n').pop())}`);
});
