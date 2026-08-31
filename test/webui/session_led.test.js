'use strict';

// What colour a conversation's light is.
//
// One rule across the product: green is the machine's colour, amber is
// yours. Blinking means something is happening; steady amber means
// nothing is, and will not until you answer something. Before this, work
// blinked amber and so did nothing else — which put "the model is busy"
// and "the model is stopped waiting for you" in one colour, the two
// states a person most needs to tell apart, because exactly one of them
// is theirs to end.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function leds(app) {
  return (app.el('session-list').children || []).map(row => {
    const led = row.querySelector ? row.querySelector('.session-led') : null;
    return led ? led.className.replace('session-led ', '') : null;
  });
}

test('a session waiting for an answer is drawn differently from one that is working', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'working', agent: 'general-purpose', workspace: '/w', busy: true },
        { id: 's2', title: 'asking', agent: 'general-purpose', workspace: '/w', busy: true, asking: true },
        { id: 's3', title: 'idle', agent: 'general-purpose', workspace: '/w' },
      ],
    },
  });

  assert.deepEqual(leds(app), ['running', 'asking', 'idle']);
});

// The ordering is the whole point of the state, not a detail of it. A
// permission request is raised from inside a turn and the turn stays open
// while the question sits there, so "busy" is true for very nearly every
// session that is also waiting. Drawing busy would mean the waiting state
// never appeared at all.
test('waiting wins over working, because both are almost always true at once', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'both', agent: 'general-purpose', workspace: '/w', busy: true, asking: true },
      ],
    },
  });

  assert.deepEqual(leds(app), ['asking']);
  const row = app.el('session-list').children[0];
  const led = row.querySelector('.session-led');
  assert.match(led.title, /waiting for you/);
});

// A daemon that does not report the field is an older one, and an older
// daemon is a normal thing to be attached to. It must read as "not
// waiting" rather than as anything else.
test('a session list with no waiting field draws the old three states', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [
        { id: 's1', title: 'working', agent: 'general-purpose', workspace: '/w', busy: true },
        { id: 's2', title: 'idle', agent: 'general-purpose', workspace: '/w' },
      ],
    },
  });

  assert.deepEqual(leds(app), ['running', 'idle']);
});

// The light under the prompt follows the same rule as the ones in the
// panel. (The rest of that light's behaviour is in activity_light.test.js;
// this is the state the two files share, and it has to agree.)
test('the light under the prompt goes amber and steady while a question is unanswered', async () => {
  const app = await load();
  const dot = app.el('comm-dot');

  app.sse.emit({ seq: 1, type: 'session.activity', data: { session: 'sess-1', busy: true } });
  await app.settle();
  assert.ok(dot.className.includes('active'), 'a running turn should blink');
  assert.ok(!dot.className.includes('asking'));

  app.applyEvent({ type: 'permission.request', data: { id: 'p1', tool: 'bash', description: 'run: ls', can_always: false } });
  await app.settle();
  assert.ok(dot.className.includes('asking'), dot.className);
  assert.match(dot.title, /waiting for you/);

  // Answered: the turn it interrupted is still running, so the light goes
  // back to blinking rather than to idle.
  app.el('permission-allow').click();
  await app.settle();
  assert.ok(!dot.className.includes('asking'), dot.className);
  assert.ok(dot.className.includes('active'), 'the turn is still running');
});
