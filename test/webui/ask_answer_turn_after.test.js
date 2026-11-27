'use strict';

// A turn that ends after a model question the page still holds says
// nobody waits on it: the daemon resolves a question before the turn that
// asked it can end. Here the daemon that came up after a quiet restart
// runs a turn that asks nothing, and the held question is put away: its
// answer is not put back, and an unanswered one says left unanswered.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const X = 'qaaaaaa-1';
const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });

async function type(app, text) {
  app.el('input').value = text;
  app.el('send').click();
  await app.settle();
}

for (const [label, turnEnd] of [
  ['done', { seq: 501, type: 'turn.done', data: {} }],
  ['cancelled', { seq: 501, type: 'turn.cancelled', data: {} }],
]) {
  test(`an older answer failing after the successor ran a turn that asked nothing (turn ${label}) leaves the box disarmed`, async () => {
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

    // X's daemon stops; the successor's resume brings no stale resolution
    // (the same precondition as ask_answer_superseded.test.js).
    app.sse.fail();
    app.sse.reopen();
    await app.settle();
    // A turn there, typed in another window, asks nothing and ends.
    app.sse.emit({ seq: 500, type: 'message.user', data: { text: 'next' } });
    app.sse.emit(turnEnd);
    await app.settle();

    // X's answer, sent to the daemon that stopped, fails now.
    posts[X]({ networkError: 'connection reset' });
    await app.settle();

    const armed = app.internals.session.pendingAsk;
    const lit = app.el('comm-dot').classList.contains('asking');
    const before = app.callsTo('POST', `/api/sessions/sess-1/input/${X}`).length;
    const prompts = app.callsTo('POST', '/api/sessions/sess-1/messages').length;
    await type(app, 'now run the tests');
    const toX = app.callsTo('POST', `/api/sessions/sess-1/input/${X}`).length - before;
    const toModel = app.callsTo('POST', '/api/sessions/sess-1/messages').length - prompts;
    assert.deepEqual({ armed, lit, toX, toModel }, { armed: null, lit: false, toX: 0, toModel: 1 },
      'after a later turn ended in the session, the older answer\'s failure armed the box for X, and the next message went to X');
  });
}

// The same with no answer typed: the box stays armed for X across the
// successor's whole turn, and X's line goes on inviting the reply. A
// newer question would have put X away (ask_answer_superseded, its last
// test); a turn that asks nothing does not.
test('a question held across a quiet restart stays armed after the successor ran a turn that asked nothing', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } } },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, X));
  await app.settle();
  app.sse.fail();
  app.sse.reopen();
  await app.settle();
  app.sse.emit({ seq: 500, type: 'message.user', data: { text: 'next' } });
  app.sse.emit({ seq: 501, type: 'turn.done', data: {} });
  await app.settle();
  const armed = app.internals.session.pendingAsk;
  const lineEnd = Array.from(app.el('transcript').querySelectorAll('.msg-ask')).pop().textContent.split('\n').pop();
  assert.deepEqual({ armed, lineEnd }, { armed: null, lineEnd: '  (left unanswered)' },
    'a turn ran and ended in the session after X was asked, yet the box still takes the next message for X');
});
