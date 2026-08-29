'use strict';

// A debate puts a second agent's voice in the transcript, and the one
// thing a reader must never have to guess is which of the two is
// talking. These cover what the page does with that: the banner before
// the first turn, the review drawn as its own thing, the closing note,
// and the message localcode sends on the person's behalf staying out of
// both the transcript and Up/Down recall.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function panel(app) {
  return app.el('transcript');
}

test('the banner says who is writing and who is reviewing, before anything runs', async () => {
  const app = await load();

  app.sse.emit({
    seq: 1,
    type: 'debate.started',
    data: { author: 'boy', reviewer: 'girl', model: 'review-model', rounds: 10, task: 'sum 1..10' },
  });
  await app.settle();

  const text = panel(app).textContent;
  assert.ok(text.includes('boy'), text);
  assert.ok(text.includes('girl'), text);
  assert.ok(text.includes('review-model'), 'the reviewer\'s model is the point of a debate: ' + text);
  assert.ok(text.includes('10 rounds'), text);
});

test('a review is drawn as the reviewing agent, not as the model or the user', async () => {
  const app = await load();

  app.sse.emit({
    seq: 1,
    type: 'debate.review',
    data: {
      round: 1, rounds: 5, reviewer: 'girl', model: 'review-model',
      approved: false, text: 'the loop is unbounded', session: 'task-7',
    },
  });
  await app.settle();

  const el = panel(app).querySelector('.msg-review');
  assert.ok(el, 'a review has no element of its own: ' + panel(app).innerHTML);
  const head = el.querySelector('.head').textContent;
  assert.ok(head.includes('girl'), head);
  assert.ok(head.includes('review-model'), head);
  assert.ok(head.includes('round 1/5'), head);
  assert.ok(head.includes('changes requested'), head);
  assert.ok(el.querySelector('.body').textContent.includes('the loop is unbounded'), el.innerHTML);
  assert.equal(el.className.includes('approved'), false, 'a review requesting changes was marked approved');

  assert.equal(
    panel(app).textContent.includes('You:'),
    false,
    'the review was painted as something the user said',
  );
});

test('an approval is marked as one', async () => {
  const app = await load();

  app.sse.emit({
    seq: 1,
    type: 'debate.review',
    data: { round: 2, rounds: 5, reviewer: 'girl', approved: true, text: 'good now' },
  });
  await app.settle();

  const el = panel(app).querySelector('.msg-review');
  assert.ok(el.className.includes('approved'), el.className);
  assert.ok(el.querySelector('.head').textContent.includes('approved'), el.querySelector('.head').textContent);
});

// The reason a debate ended is the difference between "this has been
// reviewed" and "this ran out of rounds", and at the bottom of a long
// conversation the two look identical unless somebody says which.
test('the closing note is shown as the daemon wrote it', async () => {
  const app = await load();

  app.sse.emit({
    seq: 1,
    type: 'debate.ended',
    data: { reason: 'rounds', rounds: 5, approved: false, note: '5 rounds used and girl has not approved.' },
  });
  await app.settle();

  assert.ok(panel(app).textContent.includes('5 rounds used and girl has not approved.'), panel(app).textContent);
});

// The message that carries a review back to the author is a user-role
// message localcode wrote. It has to be in the log — the model was
// really given it — and it must not be drawn as a typed line or offered
// back on Up, which is the same rule the carry-on nudge follows.
test('the message localcode sends on the user\'s behalf is not painted or recalled', async () => {
  const app = await load();

  app.sse.emit({
    seq: 1,
    type: 'message.user',
    data: { text: '↳ round 2/5 — girl\'s review goes back to boy', auto: true, source: 'reminder.debate' },
  });
  await app.settle();

  assert.equal(panel(app).textContent.includes('round 2/5'), false, panel(app).textContent);
  assert.equal(
    app.state.history.includes('↳ round 2/5 — girl\'s review goes back to boy'),
    false,
    'an automatic message went into Up/Down recall',
  );
});
