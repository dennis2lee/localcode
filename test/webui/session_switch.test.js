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
  // Every row has a light; exactly one of them is the working one, and it
  // belongs to the busy session's row.
  assert.equal((html.match(/session-led/g) || []).length, 2, html);
  assert.equal((html.match(/session-led running/g) || []).length, 1, html);
  assert.equal((html.match(/session-led idle/g) || []).length, 1, html);
  const busyRow = html.slice(html.indexOf('two') - 400, html.indexOf('two'));
  assert.ok(busyRow.includes('session-led running'), `the blinking dot is not on the busy row: ${html}`);
  // Both titles still read as titles, dot or no dot.
  assert.match(html, /one/);
  assert.match(html, /two/);
});

test('the dot follows a turn from running to unread to read', async () => {
  const app = await load();
  // The session on screen is some other one, so s1 is a conversation the
  // user is not watching — which is the whole point of the light.
  app.state.sessions = [{ id: 's1', title: 'one', agent: 'general-purpose', workspace: '/w', busy: false }];
  app.renderSessionList();
  const led = () => {
    const m = app.el('session-list').innerHTML.match(/class="session-led ?(\w*)"/);
    return m ? (m[1] || 'plain') : 'none';
  };
  assert.equal(led(), 'idle', 'an idle session should show a grey dot');

  app.sse.emit({ type: 'session.activity', data: { session: 's1', busy: true } });
  assert.equal(led(), 'running', 'a working session should blink');

  // Finished while the user was elsewhere: the answer is waiting, and a
  // steady light is what says so. This used to go straight back to no
  // dot, which is what the list shows for a session that has done nothing
  // all day — so the one moment worth noticing looked like nothing.
  app.sse.emit({ type: 'session.activity', data: { session: 's1', busy: false } });
  assert.equal(led(), 'unread', 'a finished reply nobody has seen should show a steady dot');

  // Opening it is reading it.
  app.selectSession('s1', 'general-purpose', '/w');
  await app.settle();
  app.renderSessionList();
  assert.equal(led(), 'idle', 'the dot should go back to grey once the session is opened');
});

// Watching the reply arrive is reading it. Being asked to acknowledge
// something you just sat and watched is worse than no light at all.
test('a turn finishing in the session on screen leaves no unread dot', async () => {
  const app = await load();
  const id = app.state.sessionID;
  app.state.sessions = [{ id, title: 'mine', agent: 'general-purpose', workspace: '/w', busy: false }];
  app.renderSessionList();
  const dots = (kind) => (app.el('session-list').innerHTML.match(new RegExp(`session-led ${kind}`, 'g')) || []).length;

  app.sse.emit({ type: 'session.activity', data: { session: id, busy: true } });
  assert.equal(dots('running'), 1, 'the session on screen should still blink while it works');

  app.sse.emit({ type: 'session.activity', data: { session: id, busy: false } });
  assert.equal(dots('unread'), 0, 'you watched this reply arrive; it must not ask to be read');
  assert.equal(dots('idle'), 1, 'and it goes back to the ordinary grey light');
});

// Coming back to a conversation has to show the conversation.
//
// The transcript was built entirely from message.part.delta events — one
// per few characters of streamed text — and the replay window is counted
// in events. So a single long answer was hundreds of events, the window
// landed inside it, and re-opening a session showed the tail of the last
// reply with everything above it gone. The daemon now drops the fragments
// of replies that have finished and sends only the message.part.end, which
// carries the same text whole; this is the client half of that.
test('a replayed reply renders from its message.part.end', async () => {
  const app = await load();

  app.sse.emit({ seq: 1, type: 'message.user', data: { text: 'what is 2+2' } });
  app.sse.emit({ seq: 2, type: 'message.part.end', data: { text: 'It is 4.' } });

  const html = app.el('transcript').innerHTML;
  assert.match(html, /what is 2\+2/);
  assert.match(html, /It is 4\./, `the replayed reply is missing: ${html}`);
});

// The live path is unchanged: the fragments still draw the reply as it
// arrives, and the end that follows must not append a second copy of it.
test('a live reply is not duplicated by the message.part.end that closes it', async () => {
  const app = await load();

  app.sse.emit({ seq: 1, type: 'message.part.delta', data: { text: 'It is ' } });
  app.sse.emit({ seq: 2, type: 'message.part.delta', data: { text: '4.' } });
  app.sse.emit({ seq: 3, type: 'message.part.end', data: { text: 'It is 4.' } });

  const text = app.el('transcript').textContent;
  assert.equal((text.match(/It is 4\./g) || []).length, 1, `the reply appears twice: ${text}`);
});
