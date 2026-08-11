'use strict';

// Switching sessions while a permission request is open used to hide the
// modal by reaching past the Modal object and clearing its class directly.
// The flag stayed true for the life of the page, and two keyboard handlers
// read it: Escape silently stopped cancelling turns and Tab stopped cycling
// agents, with nothing on screen to explain either.
//
// The chain this sat at the top of is worth writing down, because it is
// what the user actually hit. The abandoned request is never answered, so
// that session's turn blocks on it forever; the daemon's turn registration
// is never cleared; and the workspace guard is daemon-wide, so *every*
// later workspace switch fails with 409 — including the one selectSession
// itself performs, which is how it surfaced: an error on every session
// click.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function askPermission(app) {
  app.sse.emit({
    seq: 1,
    type: 'permission.request',
    data: { id: 'p1', tool: 'bash', description: 'rm -rf build/', rule: 'rm *', can_always: true },
  });
}

test('switching sessions closes the permission modal through the Modal object', async () => {
  const app = await load();

  askPermission(app);
  assert.equal(app.permissionRequest.isOpen, true, 'the request should have opened the modal');

  app.selectSession('s2', 'general-purpose', '');

  assert.equal(
    app.permissionRequest.isOpen,
    false,
    'isOpen stayed true after the switch — Escape and Tab read this flag and would be dead for the life of the page',
  );
  assert.equal(
    app.el('permission-modal').classList.contains('open'),
    false,
    'the modal is still visible after the switch',
  );
});

test('the prompt box is usable again after switching away from a permission', async () => {
  const app = await load();

  askPermission(app);
  assert.equal(app.el('input').disabled, true, 'a pending request should lock the prompt box');

  app.selectSession('s2', 'general-purpose', '');
  assert.equal(app.el('input').disabled, false, 'the prompt box stayed locked in the new session');
});

// A turn running in a session you are not looking at had nothing to show
// for it anywhere: the status line under the prompt speaks only for the
// current conversation. That matters most for the case that motivated it
// — a turn stuck on an unanswered permission request, which blocks every
// workspace change until someone goes back and answers it.
test('the session list marks sessions with a turn running', async () => {
  const app = await load();
  app.state.sessions = [
    { id: 's1', title: 'one', agent: 'general-purpose', workspace: '/w', busy: false },
    { id: 's2', title: 'two', agent: 'general-purpose', workspace: '/w', busy: true },
  ];
  app.renderSessionList();

  const html = app.el('session-list').innerHTML;
  // Exactly one dot, and it belongs to the busy session's row.
  assert.equal((html.match(/session-led/g) || []).length, 1, html);
  const busyRow = html.slice(html.indexOf('two') - 400, html.indexOf('two'));
  assert.ok(busyRow.includes('session-led'), `the dot is not on the busy row: ${html}`);
  // Both titles still read as titles, dot or no dot.
  assert.match(html, /one/);
  assert.match(html, /two/);
});

test('a session.activity event moves the dot without a reload', async () => {
  const app = await load();
  app.state.sessions = [{ id: 's1', title: 'one', agent: 'general-purpose', workspace: '/w', busy: false }];
  app.renderSessionList();
  const dots = () => (app.el('session-list').innerHTML.match(/session-led/g) || []).length;
  assert.equal(dots(), 0);

  app.sse.emit({ type: 'session.activity', data: { session: 's1', busy: true } });
  assert.equal(dots(), 1, 'the dot did not appear');

  app.sse.emit({ type: 'session.activity', data: { session: 's1', busy: false } });
  assert.equal(dots(), 0, 'the dot did not clear');
});
