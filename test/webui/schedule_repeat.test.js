'use strict';

// A repeating booking in the panel.
//
// Most bookings run once, so everything here appears only when one does
// not: the three limit boxes in the dialog, the line on the row, and the
// section in the window. A form that always showed them would be three
// boxes almost nobody needs in front of the two everybody does.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// previewWhen debounces by 250ms of real time, so a microtask flush is
// not enough to see its answer.
const debounced = async (app) => {
  await new Promise((r) => setTimeout(r, 320));
  await app.settle();
};

function bookRepeating(app, over) {
  app.applyEvent({
    type: 'schedule.created',
    data: {
      id: 's1',
      at: '2026-09-01T09:00:00Z',
      prompt: 'run the tests',
      agent: 'general-purpose',
      repeat_every: 1,
      repeat_unit: 'day',
      keep: 10,
      ...over,
    },
  });
}

const row = (app) => app.el('schedules').querySelectorAll('.sched')[0];

test('a repeating booking says how often, and a one-off says nothing', async () => {
  const app = await load();
  bookRepeating(app);
  assert.match(app.el('schedules').innerHTML, /every day/);

  const plain = await load();
  plain.applyEvent({
    type: 'schedule.created',
    data: { id: 's1', at: '2026-09-01T09:00:00Z', prompt: 'run the tests', agent: 'general-purpose' },
  });
  assert.ok(!plain.el('schedules').innerHTML.includes('every'), plain.el('schedules').innerHTML);
});

// The three stop conditions, each said in the row rather than left to be
// discovered at the third run.
test('the row says how long the series goes on for', async () => {
  for (const [over, want] of [
    [{}, /until you delete it/],
    [{ stop_after: 10 }, /10 times/],
    [{ stop_at: '2026-12-01T09:00:00Z' }, /until/],
  ]) {
    const app = await load();
    bookRepeating(app, over);
    assert.match(app.el('schedules').innerHTML, want, JSON.stringify(over));
  }
});

test('the row counts the runs as they happen', async () => {
  const app = await load();
  bookRepeating(app);
  app.applyEvent({
    type: 'schedule.status',
    data: { id: 's1', status: 'pending', at: '2026-09-02T09:00:00Z', runs: 3 },
  });
  assert.match(app.el('schedules').innerHTML, /3 runs so far/);
});

// A repeat between runs is armed and has an unread answer at the same
// time, and unread is the useful half: "it will run again" is what the
// row's own line already says.
test('an unread result outranks being armed for the next run', async () => {
  const app = await load();
  bookRepeating(app);
  assert.match(app.el('schedules').innerHTML, /led-degraded/, 'nothing has run yet, so it blinks');

  app.applyEvent({
    type: 'schedule.status',
    data: { id: 's1', status: 'pending', at: '2026-09-02T09:00:00Z', runs: 1, run_session: 's1-run1' },
  });
  assert.match(app.el('schedules').innerHTML, /led-connected/,
    'a run finished and nobody has read it');

  app.applyEvent({ type: 'schedule.seen', data: { id: 's1' } });
  assert.match(app.el('schedules').innerHTML, /led-degraded/,
    'once read, it is back to waiting for the next one');
});

// The one state that wants a person. Amber means "this one is yours"
// everywhere else in the product, and a suspended booking will not
// un-suspend on its own.
test('a booking that stopped itself is amber', async () => {
  const app = await load();
  bookRepeating(app);
  app.applyEvent({
    type: 'schedule.status',
    data: { id: 's1', status: 'suspended', runs: 3, error: 'stopped after 3 runs in a row failed' },
  });
  const html = app.el('schedules').innerHTML;
  assert.match(html, /led-asking/, html);
  assert.ok(html.includes('stopped after 3 runs in a row failed'), html);
});

// The window is the one place with room for the whole commitment.
test('the window spells out the repeat, the limit and the retention', async () => {
  const app = await load();
  bookRepeating(app, { stop_after: 10, keep: 3 });
  app.applyEvent({ type: 'schedule.status', data: { id: 's1', status: 'pending', runs: 2 } });
  row(app).fire('dblclick');

  const text = app.el('schedule-details-rule').textContent;
  assert.match(text, /every day/);
  assert.match(text, /10 times/);
  assert.match(text, /2 runs so far/);
  assert.match(text, /last 3 runs/);
  assert.match(text, /stops itself if 3 runs in a row fail/);
});

// -1 and 0 are the two answers a number does not explain, and 0 really
// does delete the run that has just finished.
test('the window says what keeping none and keeping all mean', async () => {
  const none = await load();
  bookRepeating(none, { keep: 0 });
  none.el('schedules').querySelectorAll('.sched')[0].fire('dblclick');
  assert.match(none.el('schedule-details-rule').textContent, /No run transcripts are kept/);

  const all = await load();
  bookRepeating(all, { keep: -1 });
  all.el('schedules').querySelectorAll('.sched')[0].fire('dblclick');
  assert.match(all.el('schedule-details-rule').textContent, /Every run.s transcript is kept/);
});

test('a one-off has no repeat section in the window', async () => {
  const app = await load();
  app.applyEvent({
    type: 'schedule.created',
    data: { id: 's1', at: '2026-09-01T09:00:00Z', prompt: 'run the tests', agent: 'general-purpose' },
  });
  row(app).fire('dblclick');
  assert.equal(app.el('schedule-details-repeat').hidden, true);
});

// The limits appear only once the time asks for a repeat, and the daemon
// is what decides that — the page does not parse times itself.
test('the dialog reveals its limits only for a repeating time', async () => {
  const app = await load({
    routes: {
      'POST /api/schedules/preview': (body) =>
        String(body.when).includes('every')
          ? { ok: true, human: 'tomorrow 09:00', repeat: 'every day' }
          : { ok: true, human: 'tomorrow 09:00', repeat: '' },
    },
  });
  app.el('schedule-btn').click();
  assert.equal(app.el('schedule-repeat').hidden, true, 'the limits start hidden');

  app.el('schedule-when').value = 'tomorrow 9am';
  app.el('schedule-when').fire('input');
  await debounced(app);
  assert.equal(app.el('schedule-repeat').hidden, true, 'a one-off time showed the limits');

  app.el('schedule-when').value = 'every day at 9am';
  app.el('schedule-when').fire('input');
  await debounced(app);
  assert.equal(app.el('schedule-repeat').hidden, false, 'a repeating time did not show the limits');
  assert.match(app.el('schedule-repeat-note').textContent, /every day/);
});
