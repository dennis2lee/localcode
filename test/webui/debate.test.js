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

// The form. Three things asked for as three things, the command shown as
// it is filled in, and the command is what gets sent — one code path,
// with every guard the prompt box already has.

// The current agent comes from the daemon's agent.switched event, which
// is how the page learns it after a switch; setting it that way keeps
// the test on the same path the app uses.
function useAgent(app, name) {
  app.sse.emit({ seq: 1, type: 'agent.switched', data: { agent: name } });
}


test('the reviewer list offers every agent except the one already running', async () => {
  const app = await load();
  useAgent(app, 'boy');
  app.state.agents = [
    { name: 'boy', model: 'author-model' },
    { name: 'girl', model: 'review-model' },
    { name: 'tom', model: 'third-model' },
  ];
  await app.el('debate-btn').click();

  const names = Array.from(app.el('debate-reviewers').querySelectorAll('.name')).map((e) => e.textContent);
  assert.deepEqual(names.sort(), ['girl', 'tom'], 'the running agent must not be offered as its own reviewer');
  const models = Array.from(app.el('debate-reviewers').querySelectorAll('.model')).map((e) => e.textContent);
  assert.ok(models.includes('review-model'), 'the model is why one reviewer is picked over another: ' + models);
});

test('the preview shows the command and what it will cost', async () => {
  const app = await load();
  useAgent(app, 'boy');
  app.state.agents = [{ name: 'boy' }, { name: 'girl' }, { name: 'tom' }];
  await app.el('debate-btn').click();

  const boxes = app.el('debate-reviewers').querySelectorAll('input');
  boxes[0].checked = true;
  boxes[0].fire('change');
  boxes[1].checked = true;
  boxes[1].fire('change');
  app.el('debate-rounds').value = '4';
  app.el('debate-task').value = 'write a sum function';
  app.el('debate-task').fire('input');

  const preview = app.el('debate-preview').textContent;
  assert.ok(preview.includes('/debate girl,tom 4 write a sum function'), preview);
  // rounds x (1 + reviewers): the number worth seeing before agreeing.
  assert.ok(preview.includes('12 model turns'), preview);
});

test('starting sends the command through the prompt box', async () => {
  const app = await load();
  useAgent(app, 'boy');
  app.state.agents = [{ name: 'boy' }, { name: 'girl' }];
  await app.el('debate-btn').click();

  const box = app.el('debate-reviewers').querySelectorAll('input')[0];
  box.checked = true;
  box.fire('change');
  app.el('debate-task').value = 'write a sum function';
  await app.el('debate-start').click();
  await app.settle();

  const sent = app.callsTo('POST', /\/messages$/);
  assert.equal(sent.length, 1, 'expected one message, got ' + JSON.stringify(sent));
  const body = typeof sent[0].body === 'string' ? JSON.parse(sent[0].body) : sent[0].body;
  assert.equal(body.text, '/debate girl 3 write a sum function');
});

test('it refuses to start without a reviewer or without a task', async () => {
  const app = await load();
  useAgent(app, 'boy');
  app.state.agents = [{ name: 'boy' }, { name: 'girl' }];
  await app.el('debate-btn').click();

  app.el('debate-task').value = 'write a sum function';
  await app.el('debate-start').click();
  assert.ok(app.el('debate-note').textContent.includes('reviewer'), app.el('debate-note').textContent);

  const box = app.el('debate-reviewers').querySelectorAll('input')[0];
  box.checked = true;
  box.fire('change');
  app.el('debate-task').value = '   ';
  await app.el('debate-start').click();
  assert.ok(app.el('debate-note').textContent.includes('what to do'), app.el('debate-note').textContent);

  assert.equal(app.callsTo('POST', /\/messages$/).length, 0, 'nothing should have been sent');
});

// Each reviewer is a model turn in every round, so the fourth box does
// not tick rather than the form being refused once it is filled in.
test('a fourth reviewer will not tick', async () => {
  const app = await load();
  useAgent(app, 'boy');
  app.state.agents = [{ name: 'boy' }, { name: 'a' }, { name: 'b' }, { name: 'c' }, { name: 'd' }];
  await app.el('debate-btn').click();

  const boxes = Array.from(app.el('debate-reviewers').querySelectorAll('input'));
  for (const b of boxes) { b.checked = true; b.fire('change'); }
  assert.equal(boxes.filter((b) => b.checked).length, 3, 'a fourth reviewer was accepted');
  assert.ok(app.el('debate-note').textContent.includes('At most 3'), app.el('debate-note').textContent);
});

// The debate dialog sends its command through the prompt box, which is
// the right call: the transcript then records the command that started
// the debate, and every guard the box already has applies. What it must
// not do is treat the box as empty.
//
// It was overwriting whatever was being composed there. On the path where
// the command sends, the draft was simply gone. On the path where it does
// not — a command cannot run while a turn is in flight, so it is refused
// and deliberately left in the box for you to retry — the person was left
// holding a command they never typed, in place of the sentence they were
// writing.
//
// That second one is how a debate runs from a prompt with no "/debate" in
// it. The next thing typed lands after the command still sitting there
// and goes out as "/debate girl 3 review the parserwhat is 2+2", which
// starts a debate on a task nobody wrote.
test('the debate dialog gives the prompt box back', async () => {
  const app = await load();
  useAgent(app, 'boy');
  app.state.agents = [{ name: 'boy' }, { name: 'girl' }];
  const input = app.el('input');
  input.value = 'a sentence I was in the middle of';

  await app.el('debate-btn').click();
  const box = app.el('debate-reviewers').querySelectorAll('input')[0];
  box.checked = true;
  box.fire('change');
  app.el('debate-task').value = 'review the parser';
  await app.el('debate-start').click();
  await app.settle();

  const sent = app.callsTo('POST', /\/messages$/);
  assert.equal(sent.length, 1, 'the debate should still have been sent');
  assert.equal(
    input.value,
    'a sentence I was in the middle of',
    'starting a debate threw away what was being composed in the prompt box',
  );
});

test('a debate refused mid-turn leaves no command behind in the box', async () => {
  const app = await load();
  useAgent(app, 'boy');
  app.state.agents = [{ name: 'boy' }, { name: 'girl' }];
  const input = app.el('input');
  input.value = 'a sentence I was in the middle of';
  app.state.waiting = true; // a turn is running: a command cannot go now

  await app.el('debate-btn').click();
  const box = app.el('debate-reviewers').querySelectorAll('input')[0];
  box.checked = true;
  box.fire('change');
  app.el('debate-task').value = 'review the parser';
  await app.el('debate-start').click();
  await app.settle();

  assert.equal(app.callsTo('POST', /\/messages$/).length, 0, 'nothing should have been sent mid-turn');
  assert.ok(
    !input.value.includes('/debate'),
    'the refused command was left in the box: ' + JSON.stringify(input.value),
  );
  assert.equal(
    input.value,
    'a sentence I was in the middle of',
    'the box should hold what the person was writing, not a command they never typed',
  );
});
