'use strict';

// The SSH panel, in its own column after the browser panel.
//
// Drawn wherever the page runs (the desktop window and the Web UI) while
// the ssh setting is on. What to get right: which conversation and
// terminal its one stream is for, that the screen is drawn from the
// daemon's frames without letting a remote program style or script the
// page, that the person's keys go to the shell in order and are not taken
// by the page's own shortcuts, and that only the person answers a
// terminal's questions.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load, settle } = require('./harness');

const settingsWith = (extra) => ({
  'GET /api/settings': {
    auto_compact_enabled: true, show_tps: true, auto_delegate: false,
    auto_delegate_agent: '', auto_delegate_match: [], smart_agent: false,
    skip_permissions: false, permission_rules: {}, can_edit_permissions: true,
    ...extra,
  },
  'GET /api/ssh': [],
  'POST /api/sessions/*/ssh/*/input': {},
  'POST /api/sessions/*/ssh/*/size': {},
  'POST /api/sessions/*/ssh/*/answer': {},
  'POST /api/sessions/*/ssh/*/hold': {},
  'POST /api/sessions/*/ssh/connect': { terminal: 't1' },
});

const term = (extra) => ({
  id: 't1', epoch: 'e1', spec: 'devbox', target: 'me@10.0.0.5:22', state: 'connected',
  shell: 'bash', integrated: true, running: false, alt: false, held: false, opener: 'model',
  cols: 80, rows: 24, scroll_first: 0, scroll_next: 0, ...extra,
});

// Objects made in the page's realm compare by value through JSON.
const plain = (v) => JSON.parse(JSON.stringify(v));

const sshStream = (app) => app.streams.filter((s) => !s.closed && s.url.includes('/ssh/stream')).pop();

const frame = (stream, kind, data) => {
  stream.onmessage({ data: JSON.stringify({ kind, data }) });
};

async function withTerminal(extra = {}, opts = {}) {
  const app = await load({ routes: settingsWith({ ssh: true }), ...opts });
  const first = sshStream(app);
  frame(first, 'state', { enabled: true, terminals: [term(extra)], wants: 0 });
  await settle();
  return app;
}

test('the panel is drawn with the setting on, in a browser as in the desktop window', async () => {
  const off = await load({ routes: settingsWith({ ssh: false }) });
  assert.equal(off.el('ssh-panel').hidden, true);
  assert.equal(off.el('toggle-ssh').hidden, true);
  assert.equal(sshStream(off), undefined, 'no stream with SSH off');

  const on = await load({ routes: settingsWith({ ssh: true }) });
  assert.equal(on.el('ssh-panel').hidden, false);
  assert.equal(on.el('toggle-ssh').hidden, false);
  const s = sshStream(on);
  assert.ok(s, 'a stream for the conversation');
  assert.match(s.url, new RegExp(`/api/sessions/${on.state.sessionID}/ssh/stream\\?viewer=v`));
});

test('a state with a terminal shows it, through a stream for that terminal', async () => {
  const app = await withTerminal();
  const s = sshStream(app);
  assert.match(s.url, /terminal=t1/);
  const tabs = app.el('ssh-tab-list').children;
  assert.equal(tabs.length, 1);
  assert.match(tabs[0].textContent, /devbox/, 'the tab names what was asked for');
  assert.equal(app.el('ssh-term').hidden, false);
  assert.match(app.el('ssh-status').textContent, /me@10\.0\.0\.5:22/);
});

test('the screen is drawn as text, a changed row redrawn and an unchanged one kept', async () => {
  const app = await withTerminal();
  const s = sshStream(app);
  const line = (t) => [{ t }];
  frame(s, 'screen', { terminal: 't1', epoch: 'e1', screen: { cols: 80, rows: 2, lines: [line('one'), line('two')], cx: 0, cy: 1, cursor: true } });
  const rows = () => app.el('ssh-screen').children.filter((c) => c.className === 'ssh-row');
  assert.deepEqual(rows().map((r) => r.textContent), ['one', 'two']);
  const kept = rows()[0];
  frame(s, 'screen', { terminal: 't1', epoch: 'e1', screen: { cols: 80, rows: 2, lines: [line('one'), line('three')], cx: 0, cy: 1, cursor: true } });
  assert.deepEqual(rows().map((r) => r.textContent), ['one', 'three']);
  assert.equal(rows()[0], kept, 'an unchanged row is the same element, so a selection on it survives');
  // Another terminal's frame does not land here.
  frame(s, 'screen', { terminal: 't2', epoch: 'x', screen: { lines: [line('other')] } });
  assert.deepEqual(rows().map((r) => r.textContent), ['one', 'three']);
});

test('a run is styled by class or a checked colour, never by what the remote wrote', async () => {
  const app = await load({ routes: settingsWith({ ssh: true }) });
  const { runStyle } = app.internals;
  assert.deepEqual(plain(runStyle({ t: 'x', f: '1', b: '#102030', a: 1 })), { classes: ['f1', 'ssh-bold'], style: { background: '#102030' } });
  const evil = runStyle({ t: 'x', f: 'red;background:url(http://x)', b: '99' });
  assert.deepEqual(plain(evil.style), {}, 'a colour that is not #rrggbb or 0-15 is dropped');
  assert.deepEqual(plain(evil.classes), []);
  assert.deepEqual(plain(runStyle({ t: 'x', a: 16 }).classes), ['ssh-rev-fg', 'ssh-rev-bg'], 'reverse video with no colours uses the terminal\'s');
  assert.deepEqual(plain(runStyle({ t: '한', w: true }).classes), ['ssh-w']);
});

test('keys: text from input, keys from keydown, the window keeps its own', async () => {
  const app = await load({ routes: settingsWith({ ssh: true }) });
  const { keyAction } = app.internals;
  const k = (key, mods = {}) => ({ key, ...mods });
  assert.equal(keyAction(k('a'), false).kind, 'text');
  assert.equal(keyAction(k('Enter'), false).kind, 'key');
  assert.equal(keyAction(k('Escape'), false).kind, 'key');
  assert.equal(keyAction(k('Tab'), false).kind, 'key');
  assert.deepEqual(plain(keyAction(k('c', { ctrlKey: true }), false).key), { key: 'c', ctrl: true, alt: false, shift: false });
  assert.equal(keyAction(k('c', { ctrlKey: true }), false, true).kind, 'pass', 'Ctrl+C with a selection copies');
  assert.equal(keyAction(k('C', { ctrlKey: true, shiftKey: true }), false).kind, 'pass');
  assert.equal(keyAction(k('v', { metaKey: true }), true).kind, 'pass', 'Cmd belongs to the window');
  assert.equal(keyAction(k('=', { ctrlKey: true }), false).kind, 'pass', 'zoom');
  assert.equal(keyAction(k('ㅎ', { isComposing: true }), false).kind, 'ime');
  assert.equal(keyAction(k('Process', { keyCode: 229 }), false).kind, 'ime');
  assert.equal(keyAction(k('å', { altKey: true }), true).kind, 'text', 'Option composes on macOS');
  assert.equal(keyAction(k('x', { altKey: true }), false).kind, 'key', 'Alt+x is the shell\'s elsewhere');
  assert.equal(keyAction(k('Shift', { shiftKey: true }), false).kind, 'ignore');
});

test('what the person types goes to the shell in order, and Escape does not stop the turn', async () => {
  const app = await withTerminal();
  const input = app.el('ssh-input');
  let stopped = 0;
  input.value = 'l';
  input.fire('input', {});
  input.value = 's';
  input.fire('input', {});
  const enter = input.fire('keydown', { key: 'Enter', stopPropagation() { stopped += 1; } });
  const esc = input.fire('keydown', { key: 'Escape', stopPropagation() { stopped += 1; } });
  await settle();
  assert.equal(enter.defaultPrevented, true);
  assert.equal(esc.defaultPrevented, true);
  assert.equal(stopped, 2, 'Escape and Enter are the terminal\'s, not the page\'s');
  const sent = app.callsTo('POST', /\/ssh\/t1\/input$/).map((c) => c.body);
  assert.deepEqual(sent, [{ text: 'l' }, { text: 's' }, { key: { key: 'Enter', ctrl: false, alt: false, shift: false } }, { key: { key: 'Escape', ctrl: false, alt: false, shift: false } }]);
});

test('Korean composed by an input method is sent once, when it is done', async () => {
  const app = await withTerminal();
  const input = app.el('ssh-input');
  input.fire('compositionstart', {});
  input.value = 'ㅎ';
  input.fire('input', { isComposing: true });
  input.value = '한';
  input.fire('input', { isComposing: true });
  input.fire('compositionend', {});
  input.fire('input', { isComposing: false });
  await settle();
  assert.deepEqual(app.callsTo('POST', /\/ssh\/t1\/input$/).map((c) => c.body), [{ text: '한' }]);
});

test('a paste is sent as a paste', async () => {
  const app = await withTerminal();
  app.el('ssh-term').fire('paste', { clipboardData: { getData: () => 'echo hi\nls' } });
  await settle();
  assert.deepEqual(app.callsTo('POST', /\/ssh\/t1\/input$/).map((c) => c.body), [{ paste: 'echo hi\nls' }]);
});

test('a host key is accepted with the fingerprint it showed, and the panel comes into view', async () => {
  const app = await load({ routes: settingsWith({ ssh: true }) });
  app.el('ssh-panel').classList.add('collapsed');
  const s = sshStream(app);
  frame(s, 'state', {
    enabled: true, wants: 1,
    terminals: [term({ state: 'connecting', question: { id: 3, kind: 'host_key', for: 'me@10.0.0.5:22', host_key: { host: '10.0.0.5', key_type: 'ssh-ed25519', fingerprint: 'SHA256:abc' } } })],
  });
  await settle();
  assert.equal(app.el('ssh-panel').classList.contains('collapsed'), false, 'a question shows a hidden panel');
  const box = app.el('ssh-question');
  assert.equal(box.hidden, false);
  assert.match(box.textContent, /SHA256:abc/);
  assert.match(box.textContent, /localcode asks/);
  const accept = Array.from(box.querySelectorAll('button')).find((b) => b.textContent === 'Accept');
  accept.click();
  await settle();
  assert.deepEqual(app.callsTo('POST', /\/ssh\/t1\/answer$/).map((c) => c.body), [{ question: 3, accept: true, fingerprint: 'SHA256:abc' }]);
});

test('a password is answered in the panel and the field is emptied', async () => {
  const app = await withTerminal({ state: 'connecting', question: { id: 4, kind: 'password', for: 'me@10.0.0.5:22' } });
  const box = app.el('ssh-question');
  const field = box.querySelectorAll('input')[0];
  assert.equal(field.type, 'password');
  field.value = 'hunter2';
  Array.from(box.querySelectorAll('button')).find((b) => b.textContent === 'OK').click();
  await settle();
  assert.deepEqual(app.callsTo('POST', /\/ssh\/t1\/answer$/).map((c) => c.body), [{ question: 4, accept: true, values: ['hunter2'] }]);
  assert.equal(field.value, '', 'the password does not stay in the page');
});

test('Hold is sent and shown', async () => {
  const app = await withTerminal();
  app.el('ssh-hold').click();
  await settle();
  assert.deepEqual(app.callsTo('POST', /\/ssh\/t1\/hold$/).map((c) => c.body), [{ on: true }]);
  frame(sshStream(app), 'state', { enabled: true, terminals: [term({ held: true })], wants: 0 });
  assert.equal(app.el('ssh-hold').getAttribute('aria-pressed'), 'true');
  assert.match(app.el('ssh-status').textContent, /held/);
});

test('a terminal fits whole cells of its view', async () => {
  const app = await load({ routes: settingsWith({ ssh: true }) });
  const { fitSize } = app.internals;
  assert.deepEqual(plain(fitSize(812, 528, 8, 20)), { cols: 100, rows: 26 });
  assert.equal(fitSize(0, 528, 8, 20), null);
  assert.equal(fitSize(812, 528, 0, 20), null);
});

test('in a window too narrow for both, the browser and SSH panels take turns', async () => {
  const app = await load({ routes: settingsWith({ ssh: true }) });
  const { makeRoomFor, fitsWith } = app.internals;
  const browser = app.el('browser-panel');
  const ssh = app.el('ssh-panel');
  browser.hidden = false;
  browser.classList.remove('collapsed');
  ssh.classList.add('collapsed');
  assert.equal(fitsWith(ssh, 1100), false);
  assert.equal(fitsWith(ssh, 2000), true);
  makeRoomFor(ssh, 2000);
  assert.equal(browser.classList.contains('collapsed'), false, 'with room for both, both stay');
  makeRoomFor(ssh, 1100);
  assert.equal(browser.classList.contains('collapsed'), true, 'without, showing SSH hides the browser');
  assert.equal(app.el('toggle-browser').getAttribute('aria-expanded'), 'false');
});
