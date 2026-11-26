'use strict';

// What an answer to a model question comes to, and what its line says.
//
// A model question (input.request) is drawn with a last line inviting a
// reply, and arms the box so the next message is its answer. Sending the
// answer disarms the box. The line and the box have to agree: a line that
// invites a reply over a box that is not armed sends the reply to the
// model as a new prompt.
//
// So an answer that fails puts the question back while the daemon may
// still be waiting on it, as a permission request is put back. Once the
// daemon says nobody is waiting (a 404, or a stale resolution, which a
// daemon sends a resuming page for a question its predecessor left open),
// the line says "(left unanswered)" instead. An answer that got through
// keeps the line as drawn, even when a stale resolution overtakes the
// real one.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

const ask = (seq, id, stale) => ({
  seq,
  type: 'input.request',
  data: { id, question: `${id}?`, options: ['a', 'b'], ...(stale ? { stale: true } : {}) },
});
const staleResolution = (id) => ({ type: 'input.resolved', data: { id, cancelled: true, stale: true } });

function askLine(app, id = 'd1-i1') {
  return Array.from(app.el('transcript').querySelectorAll('.msg-ask'))
    .find((el) => el.textContent.startsWith(`[the model is asking] ${id}?`));
}

function leftUnanswered(app, id = 'd1-i1') {
  return askLine(app, id).textContent.endsWith('\n  (left unanswered)');
}

// A page held d1-i1 live from a daemon that then stopped, and the stream
// is down.
async function heldAndDown(app) {
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd1-i1', 'precondition: the box is armed');
  app.sse.fail();
}

async function answer(app, text) {
  app.el('input').value = text;
  app.el('send').click();
  await app.settle();
}

for (const [name, route] of [
  ['the new daemon says it is not pending (404)', { status: 404, body: { error: 'question d1-i1 is not pending (already answered, or from a turn that has since ended)' } }],
  ['the old daemon is gone (network error)', { networkError: 'connection refused' }],
]) {
  test(`a held question whose answer failed says it was left unanswered once put away: ${name}`, async () => {
    const app = await load({
      routes: {
        'POST /api/sessions/sess-1/input/*': route,
        'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
      },
    });
    await heldAndDown(app);
    await answer(app, '1');
    assert.equal(app.callsTo('POST', '/api/sessions/sess-1/input/d1-i1').length, 1, 'precondition: the answer was sent');
    assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: the answer failed');

    app.sse.reopen();
    app.sse.emit(staleResolution('d1-i1'));
    await app.settle();
    assert.ok(leftUnanswered(app), `the held question still reads: ${JSON.stringify(askLine(app).textContent)}`);

    // What the line says and what the box does agree: a reply now is a
    // prompt, and the line no longer invites one.
    await answer(app, '1');
    assert.equal(app.callsTo('POST', '/api/sessions/sess-1/input/d1-i1').length, 1);
    assert.equal(app.callsTo('POST', /\/messages$/).length, 1);
  });
}

test('a held question whose answer fails after the stale resolution says it was left unanswered', async () => {
  let rejectPost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((_, reject) => { rejectPost = reject; }) },
  });
  await heldAndDown(app);
  await answer(app, '1');
  app.sse.reopen();
  app.sse.emit(staleResolution('d1-i1'));
  await app.settle();
  rejectPost(new Error('network error'));
  await app.settle();
  assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: the answer failed');
  assert.ok(leftUnanswered(app), `the held question still reads: ${JSON.stringify(askLine(app).textContent)}`);
  assert.equal(app.internals.session.pendingAsk, null, 'the box was armed for a question nobody is waiting on');
});

test('an answer that fails in transit puts the question back for the retry', async () => {
  let rejectFirst;
  let posts = 0;
  const app = await load({
    routes: {
      'POST /api/sessions/sess-1/input/*': () => (++posts === 1
        ? new Promise((_, reject) => { rejectFirst = reject; })
        : { status: 200, body: { status: 'resolved' } }),
      'POST /api/sessions/*/messages': { status: 202, body: { status: 'accepted' } },
    },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  // The stream goes on while the answer is on its way, and the light
  // stops saying the question waits.
  app.sse.emit({ seq: 402, type: 'plan.updated', data: { plan: [] } });
  await app.settle();
  assert.equal(app.el('comm-dot').classList.contains('asking'), false, 'precondition: the light let go while the answer was on its way');
  rejectFirst(new Error('daemon unreachable'));
  await app.settle();
  assert.match(app.el('transcript').textContent, /\[could not answer:/, 'precondition: the answer failed');
  assert.equal(app.internals.session.pendingAsk, 'd1-i1', 'the question was not put back');
  assert.equal(app.el('comm-dot').classList.contains('asking'), true, 'the light does not say the question is waiting again');
  assert.match(askLine(app).textContent, /reply in the box below/);

  await answer(app, '1');
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/input/d1-i1').length, 2, 'the retry did not reach the question');
  assert.equal(app.callsTo('POST', /\/messages$/).length, 0, 'the retry went to the model as a new prompt');
});

// A 404 says the daemon is not waiting now, which an answer from another
// window also produces. The question is not put back, and its line is
// left to its resolution: answered elsewhere, it keeps its line.
test('an answer refused because another window answered first keeps the line', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': { status: 404, body: { error: 'question d1-i1 is not pending' } } },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  assert.equal(app.internals.session.pendingAsk, null, 'a question the daemon refused an answer to was put back');
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/, 'said before anything said what became of it');

  app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: 'd1-i1', answer: 'b' } });
  await app.settle();
  assert.match(app.el('transcript').textContent, /\[answered\] b/);
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/, 'the line says left unanswered above its answer');

  // A stale resolution after the real one finds nothing to say.
  app.sse.emit(staleResolution('d1-i1'));
  await app.settle();
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/);
});

// Answered from another window, or the turn cancelled, while this page's
// answer was on its way: the question is done with, so the failure of
// this answer puts nothing back and says nothing on the line.
test('an answer that fails after the question was resolved elsewhere puts nothing back', async () => {
  let rejectPost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((_, reject) => { rejectPost = reject; }) },
  });
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: 'd1-i1', answer: 'b' } });
  await app.settle();
  rejectPost(new Error('network error'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'a question answered elsewhere was put back');
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/);
});

// A failed answer does not take the box from a newer question.
test('an answer that fails does not take the box from a newer question', async () => {
  let rejectPost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/d1-i1': () => new Promise((_, reject) => { rejectPost = reject; }) },
  });
  app.sse.emit(ask(401, 'd1-i1'));
  await app.settle();
  await answer(app, '1');
  app.sse.emit(ask(402, 'd2-i1'));
  await app.settle();
  rejectPost(new Error('network error'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd2-i1', 'the failed answer took the box from the newer question');
});

test('a question answered here keeps its line when a stale resolution overtakes the real one', async () => {
  const app = await load({ routes: { 'POST /api/sessions/sess-1/input/*': { status: 200, body: { status: 'resolved' } } } });
  await heldAndDown(app);
  app.sse.reopen();
  await app.settle();
  await answer(app, 'a');
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/input/d1-i1').length, 1, 'precondition: answered here');

  app.sse.emit(staleResolution('d1-i1'));
  app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: 'd1-i1', answer: 'a' } });
  await app.settle();
  const text = app.el('transcript').textContent;
  assert.match(text, /\[answered\] a/);
  assert.doesNotMatch(text, /left unanswered/, 'the line says the question was left unanswered above its answer');
});

test('a question put away by its turn being cancelled keeps its line as drawn', async () => {
  const app = await load();
  app.sse.emit({ seq: 400, type: 'message.user', data: { text: 'pick one' } });
  app.sse.emit(ask(401, 'd1-i1'));
  app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: 'd1-i1', cancelled: true } });
  app.sse.emit({ seq: 403, type: 'turn.cancelled', data: {} });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null);
  assert.match(askLine(app).textContent, /reply in the box below/);
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/);
});

test('a stale copy of another question does not cost the armed question its rewrite', async () => {
  const app = await load();
  app.sse.emit(ask(2, 'd2-i1', false));
  app.sse.emit(ask(4, 'd1-i1', true));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, 'd2-i1', 'precondition: the box is armed for d2-i1');
  app.sse.fail();
  app.sse.reopen();
  app.sse.emit(staleResolution('d2-i1'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'precondition: the box was disarmed');
  assert.ok(leftUnanswered(app, 'd2-i1'), `d2-i1 still reads: ${JSON.stringify(askLine(app, 'd2-i1').textContent)}`);
});

// The rewrite goes through the transcript's own change, so an open find
// bar stops counting text that is no longer on screen.
test('an open find bar stops counting the reply hint once the line is rewritten', async () => {
  const app = await load();
  await heldAndDown(app);
  app.sse.reopen();
  await app.settle();
  app.doc.fire('keydown', { key: 'f', ctrlKey: true, target: app.document.body });
  app.el('find-input').value = 'reply in the box';
  app.el('find-input').fire('input');
  await app.settle();
  assert.equal(app.el('find-count').textContent, '1 of 1', 'precondition: the hint is found');

  app.sse.emit(staleResolution('d1-i1'));
  await app.settle();
  assert.ok(leftUnanswered(app), 'precondition: the line was rewritten');
  assert.equal(app.el('find-count').textContent, 'no matches', 'the bar still counts the hint the rewrite took away');
});

// An answer on its way when the turn that asked is declared lost is to a
// question no daemon will wait on again, so its failure says so on the
// line rather than putting the question back.
test('an answer in flight when the turn is lost does not put its question back', async () => {
  let busy = false;
  let rejectPost;
  const app = await load({
    routes: {
      ...defaultRoutes(),
      'GET /api/sessions': () => [{ id: 'sess-1', title: 'one', busy }],
      'POST /api/sessions/*/input/*': () => new Promise((_, reject) => { rejectPost = reject; }),
    },
  });
  await answer(app, 'pick a colour');
  busy = true;
  assert.equal(app.state.waiting, true);
  app.sse.emit(ask(1, 'd1-i1'));
  await app.settle();
  await answer(app, 'red');

  app.sse.fail();
  await app.settle();
  busy = false;
  app.sse.reopen();
  await app.settle();
  for (let i = 0; i < 40 && app.state.waiting; i++) await app.wait(100);
  assert.match(app.el('transcript').textContent, /did not finish/, 'precondition: the turn was declared lost');

  rejectPost(new Error('network error'));
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'a question from a turn declared lost was put back');
  assert.ok(leftUnanswered(app), `the question reads: ${JSON.stringify(askLine(app).textContent)}`);
});

// The same with the stale resolution arriving while the answer is still
// on its way: whether it got through is the answer's to say.
test('an answer in flight when a stale resolution arrives keeps the line when it gets through', async () => {
  let resolvePost;
  const app = await load({
    routes: { 'POST /api/sessions/sess-1/input/*': () => new Promise((resolve) => { resolvePost = resolve; }) },
  });
  await heldAndDown(app);
  app.sse.reopen();
  await app.settle();
  await answer(app, 'a');
  app.sse.emit(staleResolution('d1-i1'));
  await app.settle();
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/, 'said while the answer was still on its way');
  resolvePost({ status: 200, body: { status: 'resolved' } });
  await app.settle();
  app.sse.emit({ seq: 402, type: 'input.resolved', data: { id: 'd1-i1', answer: 'a' } });
  await app.settle();
  assert.match(app.el('transcript').textContent, /\[answered\] a/);
  assert.doesNotMatch(askLine(app).textContent, /left unanswered/, 'the line says left unanswered above its answer');
});
