'use strict';

// A stale copy of the question an answer is on its way to (the
// whole-conversation replay, which replays the log without starting the
// page over) is the daemon saying nobody is waiting on it, as a stale
// resolution is. The answer's failure does not put the question back,
// as a permission request's does not ("a stale copy of a request being
// answered keeps it away when the answer fails" in stale_question.test.js).

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const ask = (seq, id, stale) => ({
  seq,
  type: 'input.request',
  data: { id, question: `${id}?`, options: ['a', 'b'], ...(stale ? { stale: true } : {}) },
});

function askLine(app, id = 'd1-i1') {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

for (const [name, failure] of [
  ['network error', { networkError: 'connection reset' }],
  ['502', { status: 502, body: { error: 'bad gateway' } }],
]) {
  test(`an answer that fails (${name}) after a stale copy of its question does not put it back`, async () => {
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
    assert.equal(app.internals.session.pendingAsk, 'd1-i1', 'precondition: the box is armed');

    app.el('input').value = '1';
    app.el('send').click();
    await app.settle();
    assert.equal(app.callsTo('POST', '/api/sessions/sess-1/input/d1-i1').length, 1, 'precondition: answered');

    // The whole conversation, from a daemon not waiting on d1-i1.
    app.el('transcript').querySelector('.earlier-btn').click();
    await app.settle();
    app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'first' } });
    app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
    app.sse.emit(ask(401, 'd1-i1', true));
    await app.settle();
    assert.ok(askLine(app).textContent.endsWith('\n  (left unanswered)'), 'precondition: drawn stale');

    settlePost(failure);
    await app.settle();
    assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: the answer failed');

    const line = askLine(app).textContent;
    assert.equal(app.internals.session.pendingAsk, null,
      `the box was armed again for a question the daemon said nobody is waiting on, under a line that reads ${JSON.stringify(line)}`);
    assert.equal(app.el('comm-dot').classList.contains('asking'), false, 'the light says a question waits for you');

    app.el('input').value = 'carry on';
    app.el('send').click();
    await app.settle();
    assert.equal(app.callsTo('POST', '/api/sessions/sess-1/input/d1-i1').length, 1, 'the next message went to the stale question');
    assert.equal(app.callsTo('POST', /\/messages$/).length, 1, 'the next message was not sent as a prompt');
  });
}
