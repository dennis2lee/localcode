'use strict';

// Forking a session: the copy has to carry the conversation, keep the
// source's agent and workspace, and become the session the user is
// looking at.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, defaultRoutes } = require('./harness');

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

// Regression: the header showed the raw session id and nothing ever
// changed it, so renaming a session in the left panel updated the panel
// and left the header naming a timestamp.
test('renaming a session updates the header, not just the panel', async () => {
  let title = 'original';
  const app = await load({
    routes: {
      'GET /api/sessions': () => [{ id: 'sess-1', title, agent: 'general-purpose' }],
      'POST /api/sessions/*/rename': (body) => { title = body.title; return { id: 'sess-1', title }; },
    },
    prompt: 'renamed by hand',
  });
  assert.equal(app.el('session-id').textContent, 'original');

  buttonLabelled(app.el('session-list'), 'rename').click();
  await app.settle();

  assert.equal(app.el('session-id').textContent, 'renamed by hand');
  assert.match(app.el('session-list').innerHTML, /renamed by hand/);
  // The id is still reachable — a bug report needs it.
  assert.equal(app.el('session-id').title, 'sess-1');
});

// A rename from another client arrives as an event, which reloads the
// listing; the header has to follow from that same data rather than
// depending on whoever renamed it to also update the header.
test('a rename from elsewhere updates the header too', async () => {
  let title = 'original';
  const app = await load({
    routes: { 'GET /api/sessions': () => [{ id: 'sess-1', title, agent: 'general-purpose' }] },
  });
  title = 'renamed elsewhere';
  app.applyEvent({ type: 'session.renamed', data: { title } });
  await app.settle();
  assert.equal(app.el('session-id').textContent, 'renamed elsewhere');
});

// A session with no title falls back to the id rather than showing an
// empty header.
test('an unnamed session still shows something in the header', async () => {
  const app = await load({
    routes: { 'GET /api/sessions': [{ id: 'sess-1', agent: 'general-purpose' }] },
  });
  assert.equal(app.el('session-id').textContent, 'sess-1');
});

// A conversation deleted somewhere else leaves every other client's
// sidebar.
//
// The list is only refreshed by loadSessions(), which runs at init, on
// rename, on archive and on a switch — none of which a delete from
// another window, the TUI, or this page's own "delete all" triggers. So
// the row stayed, pointing at a conversation that was gone, and if it was
// the open one the header went on naming it.
test('a session deleted elsewhere goes from the list, and the open one is left', async () => {
  let sessions = [
    { id: 'sess-1', title: 'one' },
    { id: 'sess-2', title: 'two' },
  ];
  const app = await load({
    routes: { ...defaultRoutes(), 'GET /api/sessions': () => sessions },
  });
  assert.equal(app.el('session-list').children.length, 2);

  // Another client deletes the one this page is not looking at.
  sessions = [{ id: 'sess-1', title: 'one' }];
  app.sse.emit({ seq: 1, type: 'session.deleted', data: { session: 'sess-2' } });
  await app.settle();
  assert.equal(app.el('session-list').children.length, 1, 'the deleted conversation stayed in the sidebar');

  // And now the one it is looking at.
  sessions = [{ id: 'sess-9', title: 'nine' }];
  app.sse.emit({ seq: 2, type: 'session.deleted', data: { session: 'sess-1' } });
  await app.settle();
  assert.equal(app.state.sessionID, 'sess-9', 'the page stayed on a conversation that no longer exists');
});
