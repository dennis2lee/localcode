'use strict';

// A newer question drawn while an answer to an older one is on its way
// does not take the older one's line from it: when the daemon has said
// nobody waits on the older question, the answer's failure still says so
// on that question's line, not only on the newest.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });
const staleResolution = (id) => ({ type: 'input.resolved', data: { id, cancelled: true, stale: true } });

function askLine(app, id) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

for (const [name, failure] of [
  ['network error', { networkError: 'connection reset' }],
  ['502', { status: 502, body: { error: 'bad gateway' } }],
  ['404', { status: 404, body: { error: 'question d1-i1 is not pending' } }],
]) {
  test(`a failed answer (${name}) to a question put away stale says so after a newer question`, async () => {
    let settlePost;
    const app = await load({
      routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }) },
    });
    app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
    app.sse.emit(ask(401, 'd1-i1'));
    await app.settle();
    app.el('input').value = '1';
    app.el('send').click();
    await app.settle();

    // Its daemon stopped; the successor's resumed stream says nobody waits
    // on d1-i1, and a turn there asks a question of its own.
    app.sse.fail();
    app.sse.reopen();
    app.sse.emit(staleResolution('d1-i1'));
    await app.settle();
    app.sse.emit(ask(500, 'd2-i1'));
    await app.settle();

    settlePost(failure);
    await app.settle();
    assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: the answer failed');
    assert.equal(app.internals.session.pendingAsk, 'd2-i1', 'precondition: the box is the newer question\'s');
    const line = askLine(app, 'd1-i1').textContent;
    assert.ok(line.endsWith('\n  (left unanswered)'),
      `d1-i1, which nobody waits on and nobody answered, still reads ${JSON.stringify(line.split('\n').pop())} above d2-i1's line`);
  });
}
