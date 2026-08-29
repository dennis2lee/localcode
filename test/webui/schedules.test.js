'use strict';

// Work booked for later, in the right-hand panel.
//
// The light is the whole row: blinking green while it waits, solid green
// once there is an answer nobody has read, grey once it has been read.
// The third state is what makes the panel a list of what still wants
// attention rather than a list of everything that ever ran.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const XSS = '<img src=x onerror="alert(1)">';

function book(app, over) {
  app.applyEvent({
    type: 'schedule.created',
    data: { id: 's1', at: '2026-09-01T09:00:00Z', prompt: 'run the tests', agent: 'general-purpose', ...over },
  });
}

test('a booked task blinks until it has run', async () => {
  const app = await load();
  book(app);
  const html = app.el('schedules').innerHTML;
  assert.match(html, /led-degraded/, 'a task still waiting should blink');
  assert.ok(html.includes('run the tests'), html);
});

test('a finished task goes solid, and grey once it has been read', async () => {
  const app = await load();
  book(app);

  app.applyEvent({ type: 'schedule.status', data: { id: 's1', status: 'done', run_session: 's1-run' } });
  assert.match(app.el('schedules').innerHTML, /led-connected/,
    'a finished task nobody has read should be solid green');

  app.applyEvent({ type: 'schedule.seen', data: { id: 's1' } });
  const html = app.el('schedules').innerHTML;
  assert.match(html, /led-disconnected/, 'a task that has been read should go grey');
  assert.ok(!html.includes('led-connected'), html);
});

test('a task that ran while localcode was closed says it was missed', async () => {
  const app = await load();
  book(app);
  app.applyEvent({
    type: 'schedule.status',
    data: { id: 's1', status: 'missed', error: 'localcode was not running at 2026-09-01 09:00' },
  });
  const html = app.el('schedules').innerHTML;
  assert.ok(html.includes('missed'), html);
  assert.ok(html.includes('not running'), 'the row has to say why it did not happen');
  // Not solid green: there is nothing to read.
  assert.ok(!html.includes('led-connected'), html);
});

test('a removed task leaves no row behind', async () => {
  const app = await load();
  book(app);
  app.applyEvent({ type: 'schedule.removed', data: { id: 's1' } });
  assert.match(app.el('schedules').innerHTML, /none/);
});

test('an empty panel says so', async () => {
  const app = await load();
  app.renderSchedules();
  assert.match(app.el('schedules').innerHTML, /none/);
});

// The prompt is text somebody typed and the id comes off the wire, so
// both render as text. Same rule as the task rows.
test('a booked prompt containing markup renders as text', async () => {
  const app = await load();
  book(app, { prompt: XSS });
  const html = app.el('schedules').innerHTML;
  assert.ok(!html.includes('<img'), html);
  assert.ok(html.includes('&lt;img'), html);
});
