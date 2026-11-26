'use strict';

// An answer to an older model question X that fails after a newer
// question Y was asked does not put X back, even once Y has let go of the
// box. A daemon asks one question at a time in a session, so Y being asked
// means nobody waits on X any more. The successor's resume here brings no
// stale resolution for X (an update's handover, or a resume whose read of
// the whole log failed).

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const X = 'd1-i1';
const Y = 'd2-i1';
const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });

async function type(app, text) {
  app.el('input').value = text;
  app.el('send').click();
  await app.settle();
}

for (const how of ['answered in another window', 'answered from here']) {
  test(`an older answer failing after the newer question was ${how} leaves the box disarmed`, async () => {
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
    await type(app, '1');
    assert.ok(posts[X], 'precondition: X answered');
    const xPost = posts[X];

    // X's daemon stops; the successor's resume brings no stale resolution.
    app.sse.fail();
    app.sse.reopen();
    await app.settle();
    // A turn there asks Y, which takes the box.
    app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
    app.sse.emit(ask(500, Y));
    await app.settle();
    assert.equal(app.internals.session.pendingAsk, Y, 'precondition: the box is Y\'s');

    if (how === 'answered from here') {
      await type(app, '2');
      assert.ok(posts[Y], 'precondition: Y answered');
    } else {
      app.sse.emit({ seq: 501, type: 'input.resolved', data: { id: Y, answer: 'a' } });
      await app.settle();
    }
    assert.equal(app.internals.session.pendingAsk, null, 'precondition: Y let go of the box');

    // X's answer, sent to the daemon that stopped, fails now.
    xPost({ networkError: 'connection reset' });
    await app.settle();
    if (how === 'answered from here') {
      posts[Y]({ status: 200, body: { status: 'resolved' } });
      await app.settle();
      app.sse.emit({ seq: 501, type: 'input.resolved', data: { id: Y, answer: '2' } });
      await app.settle();
    }

    const armed = app.internals.session.pendingAsk;
    const lit = app.el('comm-dot').classList.contains('asking');
    const before = app.callsTo('POST', `/api/sessions/sess-1/input/${X}`).length;
    const prompts = app.callsTo('POST', '/api/sessions/sess-1/messages').length;
    await type(app, 'now run the tests');
    const toX = app.callsTo('POST', `/api/sessions/sess-1/input/${X}`).length - before;
    const toModel = app.callsTo('POST', '/api/sessions/sess-1/messages').length - prompts;
    assert.deepEqual({ armed, lit, toX, toModel }, { armed: null, lit: false, toX: 0, toModel: 1 },
      'after the newer question let go of the box, the older answer\'s failure armed it for X, and the next message went to X');
  });
}

// Control: the same events with X's answer failing before Y is asked. X is
// put back, Y takes the box from it, and once Y is answered the next
// message goes to the model. Only the order of the failure differs.
test('control: the older answer failing before the newer question is asked leaves the box disarmed after it', async () => {
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
  await type(app, '1');
  app.sse.fail();
  posts[X]({ networkError: 'connection reset' });
  await app.settle();
  app.sse.reopen();
  await app.settle();
  app.sse.emit({ seq: 499, type: 'message.user', data: { text: 'next' } });
  app.sse.emit(ask(500, Y));
  await app.settle();
  app.sse.emit({ seq: 501, type: 'input.resolved', data: { id: Y, answer: 'a' } });
  await app.settle();
  const prompts = app.callsTo('POST', '/api/sessions/sess-1/messages').length;
  assert.equal(app.internals.session.pendingAsk, null);
  await type(app, 'now run the tests');
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length - prompts, 1);
});

// Never answered: a newer question puts the older one away at once, with
// no later signal needed, and its line says so.
test('a question a newer one supersedes says at once that it was left unanswered', async () => {
  const app = await load();
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit({ seq: 401, type: 'input.request', data: { id: 'd1-i1', question: 'd1-i1?', options: ['a', 'b'] } });
  await app.settle();
  app.sse.fail();
  app.sse.reopen();
  await app.settle();
  app.sse.emit({ seq: 500, type: 'input.request', data: { id: 'd2-i1', question: 'd2-i1?', options: ['a', 'b'] } });
  await app.settle();
  const older = Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .find((el) => el.textContent.startsWith('[the model is asking] d1-i1?'));
  assert.ok(older.textContent.endsWith('\n  (left unanswered)'), `d1-i1 reads: ${JSON.stringify(older.textContent)}`);
  assert.equal(app.internals.session.pendingAsk, 'd2-i1');
});
