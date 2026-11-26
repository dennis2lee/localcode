'use strict';

// A turn declared lost is one of the ways the page is told nobody waits
// on a model question. With the box armed for the question and no answer
// sent, its line says "(left unanswered)" as a stale resolution makes it
// say, rather than invite a reply the box no longer takes.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

const X = 'd1-i1';
const ask = (seq, id) => ({ seq, type: 'input.request', data: { id, question: `${id}?`, options: ['a', 'b'] } });

function askLine(app, id = X) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .filter((el) => el.textContent.startsWith(`[the model is asking] ${id}?`)).pop();
}

test('a question held when its turn is declared lost says it was left unanswered', async () => {
  let busy = false;
  const app = await load({
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }],
    },
  });
  app.el('input').value = 'pick a colour';
  app.el('send').click();
  await app.settle();
  busy = true;
  assert.equal(app.state.waiting, true, 'precondition: this page\'s turn');
  app.sse.emit(ask(1, X));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, X, 'precondition: the box is armed');

  // The daemon stops; its successor's resume brings no stale resolution
  // (the walker's quietRestart), and the turn is declared lost.
  app.sse.fail();
  await app.settle();
  busy = false;
  app.sse.reopen();
  await app.settle();
  for (let i = 0; i < 40 && app.state.waiting; i++) await app.wait(100);
  assert.match(app.el('transcript').textContent, /did not finish/, 'precondition: the turn was declared lost');
  assert.equal(app.internals.session.pendingAsk, null, 'precondition: the box is disarmed');

  const line = askLine(app).textContent;
  assert.ok(line.endsWith('\n  (left unanswered)'),
    `the box no longer takes a reply, and the line reads ${JSON.stringify(line.split('\n').pop())}`);
});

// Every answer on its way is to a question nobody will wait on once the
// turn is declared lost, not only the first one recorded.
test('a turn declared lost reaches every answer on its way', async () => {
  let busy = false;
  const posts = {};
  const app = await load({
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }],
      'POST /api/sessions/*/input/*': (_body, { path }) => new Promise((_, reject) => { posts[path.split('/').pop()] = reject; }),
    },
  });
  app.el('input').value = 'pick colours';
  app.el('send').click();
  await app.settle();
  busy = true;
  for (const [seq, id] of [[1, 'd1-i1'], [2, 'd1-i2']]) {
    app.sse.emit(ask(seq, id));
    await app.settle();
    app.el('input').value = 'red';
    app.el('send').click();
    await app.settle();
  }
  app.sse.fail();
  await app.settle();
  busy = false;
  app.sse.reopen();
  await app.settle();
  for (let i = 0; i < 40 && app.state.waiting; i++) await app.wait(100);
  assert.match(app.el('transcript').textContent, /did not finish/, 'precondition: the turn was declared lost');

  posts['d1-i2'](new Error('network error'));
  posts['d1-i1'](new Error('network error'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'a question from a turn declared lost was put back');
  for (const id of ['d1-i1', 'd1-i2']) {
    assert.ok(askLine(app, id).textContent.endsWith('\n  (left unanswered)'), `${id} reads: ${JSON.stringify(askLine(app, id).textContent)}`);
  }
});
