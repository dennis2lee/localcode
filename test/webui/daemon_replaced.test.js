'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { load } = require('./harness');

// The daemon under this page was replaced by a newer version, which is
// invisible to the page except for one thing: its own JavaScript is still
// the old version's. So the page says what happened and then goes and
// gets the new one.
//
// It reloads here rather than only where the handoff was started. The
// window reloads the page itself when the update went through its own
// handoff, and that was the only path that did: a window that updated at
// startup serves a fixed proxy and never captured the reload, and every
// later update is performed by the successor in another process, which
// cannot reach up to ask. This event arrives however the handoff was made.
test('a daemon that hands over says so, and the page goes and gets it', async () => {
  const app = await load();
  app.sse.emit({ seq: 0, type: 'daemon.replaced', data: { version: '9.9.9', pid: 4242 } });

  const text = app.el('transcript').textContent;
  assert.ok(text.includes('9.9.9'), 'the notice names the version that took over: ' + text);
  assert.match(text, /interface/i, 'the notice says what is about to happen: ' + text);

  // Not yet: this event arrives at the start of the retirement, before
  // anything has been pointed at the new daemon.
  await app.wait(200);
  assert.equal(app.reloads, 0, 'the page reloaded before the new daemon was behind the address');

  // The old daemon ends the streams; the reconnect lands on the new one,
  // and that is the moment.
  app.sse.failFatally();
  await app.wait(1400);
  app.sse.reopen();
  await app.settle();
  assert.equal(app.reloads, 1, 'the page did not come back on the new version');
});
