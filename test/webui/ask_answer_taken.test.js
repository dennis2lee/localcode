'use strict';

// An answer the daemon took leaves the box disarmed. Loading the whole
// conversation while the answer was on its way armed the box for the
// question again; without this, the box stayed armed, light amber, until
// the resolution came over the stream, and with the stream down the next
// message went to the answered question (404) instead of to the model.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const X = 'd1-i1';
const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });

test('a box re-armed during an answer is not left armed once the daemon took the answer', async () => {
  const posts = [];
  const app = await load({
    routes: {
      'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { posts.push(resolve); }),
      'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  app.el('input').value = '1';
  app.el('send').click();
  await app.settle();
  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, X, 'precondition: the replay armed the box again');
  app.sse.fail();
  await app.settle();
  posts[0]({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'the box is still armed for a question whose answer the daemon took');
});

// Two answers to one question on their way, the second typed after the
// whole conversation armed the box for it again: once the first gets
// through, the daemon is done with the question, and the second's failure
// does not put it back.
test('a second answer failing after the first got through puts nothing back', async () => {
  const posts = [];
  const app = await load({
    routes: {
      'POST /api/sessions/sess-1/input/*': () => new Promise((resolve, reject) => { posts.push({ resolve, reject }); }),
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit({ seq: 401, type: 'input.request', data: { id: 'd1-i1', question: 'd1-i1?', options: ['a', 'b'] } });
  await app.settle();
  app.el('input').value = 'a';
  app.el('send').click();
  await app.settle();

  app.el('transcript').querySelector('.earlier-btn').click();
  await app.settle();
  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit({ seq: 401, type: 'input.request', data: { id: 'd1-i1', question: 'd1-i1?', options: ['a', 'b'] } });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd1-i1', 'precondition: the reload armed the box again');
  app.el('input').value = 'b';
  app.el('send').click();
  await app.settle();
  assert.equal(posts.length, 2, 'precondition: two answers on their way');

  posts[0].resolve({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  posts[1].reject(new Error('network error'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'the question the daemon took an answer for was put back');
});
