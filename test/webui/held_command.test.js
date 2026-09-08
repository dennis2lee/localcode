'use strict';

// The one 409 the page must show rather than queue.
//
// An ordinary 409 means "a turn is running, send this again when it
// ends", and the page queues it. The daemon answers a command it refuses
// to hand to a running turn with the same status and the opposite
// meaning, and the page could not tell them apart — so "/clear" typed
// during a long turn was queued in silence and arrived minutes later.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('a held command is shown, not queued', async () => {
  const app = await load({
    routes: {
      'POST /api/sessions/*/messages': () => ({
        status: 409,
        body: { held: '/clear', error: '/clear decides what this conversation is, so it does not go to a turn already running.' },
      }),
    },
  });
  app.type('/clear');
  app.el('send').fire('click');
  await app.wait(0);

  assert.equal(app.internals.session.promptQueue.length, 0, 'the held command was queued instead of shown');
  assert.match(app.transcript(), /does not go to a turn already running/, 'the reason was not shown');
});

test('an ordinary busy refusal is still queued', async () => {
  const app = await load({
    routes: {
      'POST /api/sessions/*/messages': () => ({
        status: 409,
        body: { error: 'session is already processing a message' },
      }),
    },
  });
  app.type('hello');
  app.el('send').fire('click');
  await app.wait(0);

  assert.equal(app.internals.session.promptQueue.length, 1, 'an ordinary busy 409 should be queued for the next turn');
});
