'use strict';

// Forking a session: the copy has to carry the conversation, keep the
// source's agent and workspace, and become the session the user is
// looking at.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// The fake DOM has no querySelectorAll, so walk it. Buttons are found by
// their label because that is what a user clicks — an id or an nth-child
// index would keep passing after the row was rearranged.
function buttonLabelled(root, label) {
  for (const child of root.children || []) {
    if (child.tagName === 'BUTTON' && child.textContent === label) return child;
    const found = buttonLabelled(child, label);
    if (found) return found;
  }
  return null;
}

// Switching to the copy is the point: the reason to fork is to take this
// thread somewhere else now, and the original stays one click away in
// the panel.
test('fork creates a copy and switches to it', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [{ id: 'sess-1', title: 'original', agent: 'plan', workspace: '/srv/project' }],
      'POST /api/sessions/*/fork': { id: 'sess-fork', agent: 'plan', workspace: '/srv/project', title: 'fork of original' },
      'POST /api/workspace': { path: '/srv/project' },
    },
  });

  const forkBtn = buttonLabelled(app.el('session-list'), 'fork');
  assert.ok(forkBtn, app.el('session-list').innerHTML);
  forkBtn.click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/fork').length, 1);
  assert.equal(app.state.sessionID, 'sess-fork');
  // The fork inherits the source's agent rather than falling back to the
  // default — a plan-mode thread forked into build mode would be a
  // surprise, and the agent is part of what is being copied.
  assert.equal(app.state.currentAgent, 'plan');
});

test('a refused fork is reported and leaves the session alone', async () => {
  const app = await load({
    routes: {
      'GET /api/sessions': [{ id: 'sess-1', title: 'original', agent: 'general-purpose' }],
      'POST /api/sessions/*/fork': { status: 409, body: { error: 'a turn is in progress' } },
    },
  });

  buttonLabelled(app.el('session-list'), 'fork').click();
  await app.settle();

  assert.equal(app.state.sessionID, 'sess-1');
  assert.match(app.transcript(), /failed to fork session/);
});
