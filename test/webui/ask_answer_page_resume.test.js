'use strict';

// The page's own reconnect (a reply that fails the connection, then
// connectEvents({resume: true}) with ?since=) leaves a question and an
// answer to it as the browser's own retry with Last-Event-ID does.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const X = 'd1-i1';
const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });
const staleResolution = (id) => ({ type: 'input.resolved', data: { id, cancelled: true, stale: true } });

function askLine(app, id = X) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

for (const held of ['armed', 'answer on its way']) {
  test(`a page-rebuilt resume puts away a question held (${held})`, async () => {
    let settlePost;
    const app = await load({
      routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { settlePost = resolve; }) },
    });
    app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
    app.sse.emit(ask(401, X));
    await app.settle();
    if (held !== 'armed') {
      app.el('input').value = '1';
      app.el('send').click();
      await app.settle();
    }
    const first = app.streams.length;
    app.sse.failFatally();
    await app.settle();
    for (let i = 0; i < 30 && app.streams.length === first; i++) await app.wait(100);
    assert.ok(app.streams.length > first, 'precondition: the page opened a new stream');
    const s = app.streams[app.streams.length - 1];
    assert.match(s.url, /since=401/, 'precondition: resumed from where it got to');
    s.emit(staleResolution(X));
    await app.settle();
    if (held !== 'armed') {
      settlePost({ networkError: 'connection reset' });
      await app.settle();
    }
    assert.equal(app.internals.session.pendingAsk, null);
    assert.ok(askLine(app).textContent.endsWith('\n  (left unanswered)'), askLine(app).textContent);
  });
}
