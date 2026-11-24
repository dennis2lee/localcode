'use strict';

// The PowerShell panel, in its own column after the SSH panel.
//
// It is the SSH panel's terminal (js/termpanel.js) over the PowerShell
// routes, drawn while the powershell setting is on and the daemon has
// PowerShell (Windows), in a browser as in the desktop window. What to
// get right: that it is never drawn where there is no PowerShell, that
// every request goes to a PowerShell route, that + starts a terminal at
// once, and that with the SSH panel on screen too the two keep their own
// streams, terminals and typed text.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load, settle, defaultRoutes } = require('./harness');

const settingsWith = (extra) => ({
  'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], ...extra },
  'GET /api/ssh': [],
  'GET /api/powershell': [],
  'POST /api/sessions/*/ssh/*/input': {},
  'POST /api/sessions/*/ssh/*/size': {},
  'POST /api/sessions/*/powershell/*/input': {},
  'POST /api/sessions/*/powershell/*/size': {},
  'POST /api/sessions/*/powershell/*/hold': {},
  'POST /api/sessions/*/powershell/*/reconnect': { terminal: 'p1' },
  'POST /api/sessions/*/powershell/open': { terminal: 'p1' },
  'DELETE /api/sessions/*/powershell/*': { status: 204 },
});

const ON = { powershell: true, powershell_available: true };

const term = (extra) => ({
  id: 'p1', epoch: 'e1', spec: 'PowerShell', target: '', state: 'connected',
  shell: 'powershell', integrated: false, running: false, alt: false, held: false, opener: 'person',
  cols: 80, rows: 24, scroll_first: 0, scroll_next: 0, ...extra,
});

const psStream = (app) => app.streams.filter((s) => !s.closed && s.url.includes('/powershell/stream')).pop();
const sshStream = (app) => app.streams.filter((s) => !s.closed && s.url.includes('/ssh/stream')).pop();

const frame = (stream, kind, data) => {
  stream.onmessage({ data: JSON.stringify({ kind, data }) });
};

async function withTerminal(extra = {}, settingsExtra = {}) {
  const app = await load({ routes: settingsWith({ ...ON, ...settingsExtra }) });
  frame(psStream(app), 'state', { enabled: true, terminals: [term(extra)], wants: 0 });
  await settle();
  return app;
}

// Every request the panel made, which must all be PowerShell routes.
const terminalCalls = (app) => app.calls.filter((c) => /^\/api\/sessions\/[^/]+\/(ssh|powershell)(\/|$)/.test(c.path));

test('the panel is drawn only with the setting on where the daemon has PowerShell', async () => {
  for (const [what, extra] of [
    ['the setting off', { powershell: false, powershell_available: true }],
    ['no PowerShell here (macOS, Linux)', { powershell: true, powershell_available: false }],
    ['a daemon that does not say', { powershell: true }],
  ]) {
    const app = await load({ routes: settingsWith(extra) });
    assert.equal(app.el('ps-panel').hidden, true, `${what}: the panel is drawn`);
    assert.equal(app.el('resize-ps').hidden, true, `${what}: the handle is drawn`);
    assert.equal(app.el('toggle-ps').hidden, true, `${what}: the header button is drawn`);
    assert.equal(psStream(app), undefined, `${what}: a stream was opened`);
    assert.equal(app.internals.powershellShown(), false);
  }

  const on = await load({ routes: settingsWith(ON) });
  assert.equal(on.el('ps-panel').hidden, false);
  assert.equal(on.el('resize-ps').hidden, false);
  assert.equal(on.el('toggle-ps').hidden, false);
  assert.equal(on.internals.powershellShown(), true);
  const s = psStream(on);
  assert.ok(s, 'a stream for the conversation');
  assert.match(s.url, new RegExp(`^/api/sessions/${on.state.sessionID}/powershell/stream\\?viewer=v`));
  // The SSH panel is not drawn by the PowerShell setting.
  assert.equal(on.el('ssh-panel').hidden, true);
  assert.equal(sshStream(on), undefined);
});

test('the setting turned on or off elsewhere draws or removes the panel', async () => {
  const app = await load({ routes: settingsWith({ powershell: false, powershell_available: true }) });
  const conv = app.streams.find((s) => !s.closed && /\/events(\?|$)/.test(s.url));
  conv.emit({ type: 'settings.changed', data: { powershell: true } });
  await settle();
  assert.equal(app.el('ps-panel').hidden, false, 'settings.changed did not draw the panel');
  assert.ok(psStream(app));

  conv.emit({ type: 'config.changed', data: { powershell: false } });
  await settle();
  assert.equal(app.el('ps-panel').hidden, true, 'config.changed did not remove the panel');
  assert.equal(psStream(app), undefined, 'the stream stayed open');
});

test('a state with a terminal shows it, through a stream for that terminal', async () => {
  const app = await withTerminal();
  assert.match(psStream(app).url, /\/powershell\/stream\?.*terminal=p1/);
  const tabs = app.el('ps-tab-list').children;
  assert.equal(tabs.length, 1);
  assert.match(tabs[0].textContent, /PowerShell/);
  assert.equal(app.el('ps-term').hidden, false);
  assert.equal(app.el('ps-empty').hidden, true);
  assert.match(app.el('ps-status').textContent, /^PowerShell · powershell$/);
  frame(psStream(app), 'state', { enabled: true, terminals: [term({ state: 'connecting' })], wants: 0 });
  assert.equal(app.el('ps-status').textContent, 'starting PowerShell');
  // Nothing the SSH panel would draw moved.
  assert.equal(app.el('ssh-tab-list').children.length, 0);
});

test('+ starts a PowerShell at once and shows it', async () => {
  const app = await load({ routes: settingsWith(ON) });
  frame(psStream(app), 'state', { enabled: true, terminals: [], wants: 0 });
  assert.equal(app.el('ps-empty').hidden, false);
  app.el('ps-tab-new').click();
  await settle();
  const posted = app.callsTo('POST', /\/powershell\/open$/);
  assert.equal(posted.length, 1, 'no request to start one');
  assert.equal(posted[0].path, `/api/sessions/${app.state.sessionID}/powershell/open`);
  assert.deepEqual(JSON.parse(JSON.stringify(posted[0].body)), {});
  assert.match(psStream(app).url, /terminal=p1/, 'the panel did not turn to the terminal it started');
  assert.equal(app.callsTo('POST', /\/ssh\/connect$/).length, 0);
  frame(psStream(app), 'state', { enabled: true, terminals: [term()], wants: 0 });
  assert.equal(app.el('ps-term').hidden, false);
  assert.match(app.el('ps-tab-list').children[0].className, /active/);
});

test('+ pressed twice while the first start is out starts one PowerShell', async () => {
  let answer;
  const app = await load({
    routes: {
      ...settingsWith(ON),
      'POST /api/sessions/*/powershell/open': () => new Promise((resolve) => { answer = resolve; }),
    },
  });
  app.el('ps-tab-new').click();
  await settle();
  app.el('ps-tab-new').click();
  await settle();
  answer({ terminal: 'p1' });
  await settle();
  assert.equal(app.callsTo('POST', /\/powershell\/open$/).length, 1);
  assert.equal(app.el('ps-tab-new').disabled, false, 'the button was left held');
});

test('a start the daemon refuses is said in the panel', async () => {
  const app = await load({
    routes: {
      ...settingsWith(ON),
      'POST /api/sessions/*/powershell/open': { status: 400, body: { error: 'at most 8 terminals in a conversation' } },
    },
  });
  app.el('ps-tab-new').click();
  await settle();
  assert.equal(app.el('ps-error').hidden, false);
  assert.match(app.el('ps-error').textContent, /^Not started: /);
  assert.equal(app.el('ssh-error').hidden, true);
});

test('what the person types goes to the PowerShell input route, in order', async () => {
  const app = await withTerminal();
  const input = app.el('ps-input');
  let stopped = 0;
  input.value = 'd';
  input.fire('input', {});
  input.value = 'ir';
  input.fire('input', {});
  const enter = input.fire('keydown', { key: 'Enter', stopPropagation() { stopped += 1; } });
  await settle();
  assert.equal(enter.defaultPrevented, true);
  assert.equal(stopped, 1, 'Enter is the terminal\'s, not the page\'s');
  app.el('ps-term').fire('paste', { clipboardData: { getData: () => 'Get-ChildItem' } });
  await settle();
  const sent = app.callsTo('POST', /\/input$/);
  assert.ok(sent.every((c) => c.path === `/api/sessions/${app.state.sessionID}/powershell/p1/input`), sent.map((c) => c.path).join(', '));
  assert.deepEqual(JSON.parse(JSON.stringify(sent.map((c) => c.body))), [
    { text: 'd' }, { text: 'ir' }, { key: { key: 'Enter', ctrl: false, alt: false, shift: false } }, { paste: 'Get-ChildItem' },
  ]);
});

test('Korean composed in the PowerShell panel is sent once, when it is done', async () => {
  const app = await withTerminal();
  const input = app.el('ps-input');
  input.fire('compositionstart', {});
  input.value = 'ㅎ';
  input.fire('input', { isComposing: true });
  input.value = '한';
  input.fire('input', { isComposing: true });
  input.fire('compositionend', {});
  input.fire('input', { isComposing: false });
  await app.wait(30);
  await settle();
  assert.deepEqual(JSON.parse(JSON.stringify(app.callsTo('POST', /\/powershell\/p1\/input$/).map((c) => c.body))), [{ text: '한' }]);
});

test('Hold, Start again and close use the PowerShell routes', async () => {
  const app = await withTerminal();
  const id = app.state.sessionID;
  app.el('ps-hold').click();
  await settle();
  assert.deepEqual(app.callsTo('POST', /\/hold$/).map((c) => [c.path, c.body.on]), [[`/api/sessions/${id}/powershell/p1/hold`, true]]);
  frame(psStream(app), 'state', { enabled: true, terminals: [term({ held: true })], wants: 0 });
  assert.equal(app.el('ps-hold').getAttribute('aria-pressed'), 'true');
  assert.equal(app.el('ps-hold').textContent, 'Let go');

  // An ended PowerShell is started again in its tab.
  frame(psStream(app), 'state', { enabled: true, terminals: [term({ state: 'ended', reason: 'the shell exited' })], wants: 0 });
  const again = app.el('ps-reconnect');
  assert.equal(again.hidden, false);
  assert.equal(app.el('ps-hold').hidden, true);
  assert.equal(app.el('ps-status').textContent, 'ended: the shell exited');
  const before = psStream(app);
  again.click();
  await settle();
  assert.deepEqual(app.callsTo('POST', /\/reconnect$/).map((c) => c.path), [`/api/sessions/${id}/powershell/p1/reconnect`]);
  assert.notEqual(psStream(app), before, 'the screen was not started again');
  assert.equal(before.closed, true);

  // The tab's x ends it.
  frame(psStream(app), 'state', { enabled: true, terminals: [term()], wants: 0 });
  const close = app.el('ps-tab-list').children[0].children[1];
  assert.equal(close.title, 'end this PowerShell');
  close.click();
  await settle();
  assert.deepEqual(app.callsTo('DELETE', /\/p1$/).map((c) => c.path), [`/api/sessions/${id}/powershell/p1`]);

  assert.deepEqual(terminalCalls(app).filter((c) => c.path.includes('/ssh')), [], 'a request went to an SSH route');
});

test('the SSH and PowerShell panels on screen together keep their own streams, terminals and typing', async () => {
  const app = await load({ routes: settingsWith({ ssh: true, ...ON }) });
  const ssh = sshStream(app);
  const ps = psStream(app);
  assert.ok(ssh && ps && ssh !== ps, 'two streams');
  frame(ssh, 'state', { enabled: true, wants: 0, terminals: [term({ id: 't1', spec: 'devbox', target: 'me@10.0.0.5:22', shell: 'bash', opener: 'model' })] });
  frame(ps, 'state', { enabled: true, wants: 0, terminals: [term()] });
  await settle();
  assert.match(sshStream(app).url, /\/ssh\/stream\?.*terminal=t1/);
  assert.match(psStream(app).url, /\/powershell\/stream\?.*terminal=p1/);
  assert.deepEqual(app.el('ssh-tab-list').children.map((t) => t.textContent.replace('×', '')), ['devbox']);
  assert.deepEqual(app.el('ps-tab-list').children.map((t) => t.textContent.replace('×', '')), ['PowerShell']);

  // A screen lands in its own panel.
  const line = (t) => [[{ t }]];
  frame(sshStream(app), 'screen', { terminal: 't1', epoch: 'e1', screen: { cols: 80, rows: 1, lines: line('me@devbox:~$'), cx: 0, cy: 0, cursor: true } });
  frame(psStream(app), 'screen', { terminal: 'p1', epoch: 'e1', screen: { cols: 80, rows: 1, lines: line('PS C:\\> '), cx: 0, cy: 0, cursor: true } });
  const rows = (id) => app.el(id).children.filter((c) => c.className === 'ssh-row').map((r) => r.textContent);
  assert.deepEqual(rows('ssh-screen'), ['me@devbox:~$']);
  assert.deepEqual(rows('ps-screen'), ['PS C:\\> ']);

  // Typing in one, with an input method still composing in the other.
  const sshInput = app.el('ssh-input');
  const psInput = app.el('ps-input');
  sshInput.fire('compositionstart', {});
  sshInput.value = 'ㅎ';
  sshInput.fire('input', { isComposing: true });
  psInput.value = 'cls';
  psInput.fire('input', {});
  psInput.fire('keydown', { key: 'Enter', stopPropagation() {} });
  await settle();
  sshInput.value = '한';
  sshInput.fire('compositionend', {});
  await app.wait(30);
  await settle();
  sshInput.fire('keydown', { key: 'Tab', stopPropagation() {} });
  await settle();
  const sent = (route) => JSON.parse(JSON.stringify(app.callsTo('POST', new RegExp(`/${route}/[^/]+/input$`)).map((c) => [c.path.split('/').slice(-2, -1)[0], c.body])));
  assert.deepEqual(sent('powershell'), [['p1', { text: 'cls' }], ['p1', { key: { key: 'Enter', ctrl: false, alt: false, shift: false } }]],
    'the PowerShell panel sent what was typed in the SSH panel, or was held by its composition');
  assert.deepEqual(sent('ssh'), [['t1', { text: '한' }], ['t1', { key: { key: 'Tab', ctrl: false, alt: false, shift: false } }]],
    'the SSH panel sent what was typed in the PowerShell panel');
  assert.equal(psInput.value, '', 'the PowerShell typing box was left holding text');

  // Hiding one keeps the other's stream and terminal.
  app.el('toggle-ps').click();
  assert.ok(app.el('ps-panel').classList.contains('collapsed'));
  assert.equal(app.el('ssh-panel').classList.contains('collapsed'), false);
  assert.match(sshStream(app).url, /terminal=t1/);
  assert.match(psStream(app).url, /terminal=p1/, 'a hidden panel keeps its stream');
});

test('a key typed in one panel never reaches the other\'s input route', async () => {
  const app = await load({ routes: settingsWith({ ssh: true, ...ON }) });
  frame(sshStream(app), 'state', { enabled: true, wants: 0, terminals: [term({ id: 't1', spec: 'devbox', target: 'me@10.0.0.5:22' })] });
  frame(psStream(app), 'state', { enabled: true, wants: 0, terminals: [term()] });
  await settle();
  const keys = ['a', 'Enter', 'Escape', 'Tab', 'ArrowUp', 'Backspace'];
  for (const [id, route] of [['ps', 'powershell'], ['ssh', 'ssh']]) {
    const before = app.calls.length;
    for (const key of keys) {
      // On the terminal's box rather than the typing box: a printable key
      // is then sent from keydown, as it is when the box has the focus.
      app.el(`${id}-term`).fire('keydown', { key, target: app.el(`${id}-term`), stopPropagation() {} });
    }
    await settle();
    const made = app.calls.slice(before).filter((c) => c.path.endsWith('/input'));
    assert.equal(made.length, keys.length, `${id}: ${made.length} of ${keys.length} keys were sent`);
    for (const c of made) assert.match(c.path, new RegExp(`/${route}/[^/]+/input$`), `a key typed in the ${id} panel went to ${c.path}`);
  }
});

test('a stream the daemon refuses is said as the PowerShell panel\'s', async () => {
  const app = await load({ routes: { ...settingsWith(ON), 'GET /api/sessions/*/powershell': { status: 403, body: { error: 'refused' } } } });
  psStream(app).failFatally();
  await settle();
  assert.match(app.el('ps-error').textContent, /^The PowerShell panel cannot reach its terminals: /);
  assert.equal(app.callsTo('GET', /\/ssh$/).length, 0);
});

test('a terminal that wants the person shows a hidden PowerShell panel', async () => {
  const app = await withTerminal();
  app.el('toggle-ps').click();
  assert.ok(app.el('ps-panel').classList.contains('collapsed'));
  frame(psStream(app), 'state', { enabled: true, terminals: [term()], wants: 1 });
  assert.equal(app.el('ps-panel').classList.contains('collapsed'), false);
  assert.equal(app.el('toggle-ps').getAttribute('aria-expanded'), 'true');
});

test('the panel has no connect form and the SSH panel keeps its own', async () => {
  const app = await load({ routes: settingsWith({ ssh: true, ...ON }) });
  assert.equal(app.el('ps-connect'), null, 'the PowerShell panel has a connect form');
  app.el('ssh-tab-new').click();
  assert.equal(app.el('ssh-connect').hidden, false, 'the SSH panel\'s + no longer opens its form');
  assert.equal(app.callsTo('POST', /\/open$/).length, 0, 'the SSH panel\'s + started a PowerShell');
});

// The markup, read from the shipped page: the fake DOM keeps ids and
// attributes, not the words between the tags.
test('the panel\'s markup is the SSH panel\'s, with its own ids and words', () => {
  const html = fs.readFileSync(path.join(__dirname, '..', '..', 'internal', 'daemon', 'static', 'index.html'), 'utf8');
  const between = (from, to) => {
    const a = html.indexOf(from);
    const b = html.indexOf(to, a);
    assert.ok(a >= 0 && b > a, `cannot find ${from} .. ${to}`);
    return html.slice(a, b);
  };
  // The SSH panel without its connect form, which the PowerShell panel
  // has no use for.
  const ssh = between('<aside id="ssh-panel"', '</aside>').replace(/<form class="ssh-connect"[\s\S]*?<\/form>/, '');
  const ps = between('<aside id="ps-panel"', '</aside>');
  const ids = (m) => [...m.matchAll(/\bid="([^"]+)"/g)].map((x) => x[1]);
  assert.deepEqual(ids(ps), ids(ssh).map((id) => id.replace(/^ssh-/, 'ps-')), 'every id, with ps- for ssh-');
  // The inner elements keep the ssh-* classes, so the styles apply.
  const classes = (m) => [...m.matchAll(/\bclass="([^"]+)"/g)].map((x) => x[1]);
  assert.deepEqual(classes(ps), classes(ssh));
  assert.match(ps, /<h2>PowerShell<\/h2>/);
  assert.match(ps, /No PowerShell yet\. Press \+ to start one, or ask the model to open one\./);
  assert.match(ps, /id="ps-reconnect"[^>]*>Start again</);
  assert.doesNotMatch(ps, /ssh-connect|>Reconnect</);
  // After the SSH panel, each with its handle before it.
  assert.ok(html.indexOf('id="resize-ssh"') < html.indexOf('id="ssh-panel"'));
  assert.ok(html.indexOf('id="ssh-panel"') < html.indexOf('id="resize-ps"'));
  assert.ok(html.indexOf('id="resize-ps"') < html.indexOf('id="ps-panel"'));
  assert.match(between('<div class="resizer" id="resize-ps"', '>'), /hidden/);
  // The header button, after the SSH panel's, with an icon of its own.
  const header = between('<header>', '</header>');
  assert.ok(header.indexOf('id="toggle-ssh"') < header.indexOf('id="toggle-ps"'), 'the PowerShell toggle is not after the SSH toggle');
  assert.match(header, /id="toggle-ps"[^>]*hidden[^>]*><svg class="ico"><use href="#i-powershell"\/><\/svg><\/button>/);
  const symbol = between('<symbol id="i-powershell"', '</symbol>');
  for (const attr of ['viewBox="0 0 16 16"', 'fill="none"', 'stroke="currentColor"', 'stroke-width="1.25"', 'stroke-linecap="round"', 'stroke-linejoin="round"']) {
    assert.ok(symbol.includes(attr), `the icon has no ${attr}`);
  }
});
