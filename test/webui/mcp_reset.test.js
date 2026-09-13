'use strict';

// The reconnect control in the MCP panel. The status rows were already
// there; the control was not. It sends /reset-mcp to the current session
// — a daemon-side command (stop the servers, re-read the configuration,
// reconnect), so the client needs no new endpoint. One button for the
// whole panel, because that is what it acts on: every server, for every
// conversation on the machine.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// The button must say what it is going to do before it is clicked: a
// control that looks per-row and acts globally is worse than no control.
test('the reconnect button names /reset-mcp and its machine-wide scope in its title', async () => {
  const app = await load({
    routes: {
      'GET /api/mcp-servers': [
        { name: 'filesystem', status: 'disconnected', detail: 'connection refused' },
      ],
    },
  });

  const btn = app.el('mcp-reset-btn');
  assert.ok(btn, 'the MCP panel has a reconnect control');
  assert.match(btn.title, /\/reset-mcp/);
  assert.match(btn.title, /every conversation/);
  assert.match(btn.title, /mcp__/);
  assert.match(btn.title, /not a per-server retry/);
});

// The confirmation the click asks for has to carry the same warning: by
// the time it is on screen the button is already behind the person.
test('the confirmation names the machine-wide reset before anything is sent', async () => {
  const app = await load();

  assert.match(app.mcpResetConfirmText, /\/reset-mcp/);
  assert.match(app.mcpResetConfirmText, /every conversation/);
  assert.match(app.mcpResetConfirmText, /mcp__/);
  assert.match(app.mcpResetConfirmText, /not a per-server retry/);
});

// Confirming sends the slash command to the open conversation as a chat
// message, which the daemon routes to its reset handler.
test('confirming the reconnect sends /reset-mcp to the current session', async () => {
  const app = await load({ confirm: true });
  assert.equal(app.state.sessionID, 'sess-1');

  app.el('mcp-reset-btn').click();
  await app.settle();

  const posts = app.callsTo('POST', '/api/sessions/sess-1/messages');
  assert.equal(posts.length, 1);
  assert.deepEqual(posts[0].body, { text: '/reset-mcp' });
});

// Answering no sends nothing: the reset closes every server, so the
// confirmation is the only thing between a misclick and a machine-wide
// reconnect.
test('cancelling the confirmation sends nothing', async () => {
  const app = await load({ confirm: false });

  app.el('mcp-reset-btn').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 0);
});

// A reset the daemon refuses is reported in the transcript rather than
// left looking as though it happened.
test('a refused reset is reported', async () => {
  const app = await load({
    confirm: true,
    routes: {
      'POST /api/sessions/*/messages': { status: 500, body: { error: 'turn in progress' } },
    },
  });

  app.el('mcp-reset-btn').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 1);
  assert.match(app.transcript(), /could not reset MCP servers/);
});
