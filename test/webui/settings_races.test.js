'use strict';

// The settings window's switches against requests that are still out and
// announcements that come in between, found in the review of the
// PowerShell panel. Each switch shows what the daemon has.

const test = require('node:test');
const assert = require('node:assert/strict');
const { load, defaultRoutes } = require('./harness.js');

function announce(app, data) {
  const stream = app.streams.find((s) => !s.closed && /\/events(\?|$)/.test(s.url));
  assert.ok(stream, 'no conversation stream');
  stream.emit({ type: 'settings.changed', data });
}

test('a save that fails leaves the switch as the daemon has it, moved by another window meanwhile', async () => {
  let rejectPost;
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], powershell: false, powershell_available: true },
      'GET /api/powershell': [],
      'POST /api/settings/powershell': () => new Promise((_, rej) => { rejectPost = () => rej(new Error('network error')); }),
    },
  });
  app.el('settings-btn').click();
  await app.settle();
  const box = app.el('powershell-checkbox');
  box.checked = true;
  box.fire('change');
  await app.settle();
  announce(app, { powershell: true });
  await app.settle();
  rejectPost();
  await app.settle();
  assert.equal(app.app.powershell, true);
  assert.equal(box.checked, true, 'the switch shows off while the daemon has it on');
});

test('a list of open terminals that arrives after its switch went off is not drawn', async () => {
  let resolveList;
  const app = await load({
    globals: { lcGUI: true },
    routes: {
      'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], powershell: true, powershell_available: true },
      'GET /api/powershell': () => new Promise((res) => { resolveList = res; }),
      'POST /api/settings/powershell': (body) => ({ powershell: body.enabled, applied: true, persisted: true }),
    },
  });
  app.el('settings-btn').click();
  await app.settle();
  const box = app.el('powershell-checkbox');
  box.checked = false;
  box.fire('change');
  await app.settle();
  assert.equal(app.app.powershell, false);
  resolveList([{ session: 'sess-1', title: 'first session', id: 't1', target: 'PowerShell', state: 'connected' }]);
  await app.settle();
  assert.equal(app.el('powershell-live').children.length, 0, 'a terminal listed under a switch that is off');
});

test('a refused Reasoning switch is told once, not after it was moved elsewhere', async () => {
  const app = await load({
    globals: { lcGUI: true },
    routes: { 'POST /api/settings/fold-thinking': { status: 500, body: 'boom' } },
  });
  app.el('settings-btn').click();
  await app.settle();
  const box = app.el('fold-thinking-checkbox');
  box.checked = false;
  box.fire('change');
  await app.settle();
  assert.match(app.el('fold-thinking-warn').textContent, /^Not changed:/);
  announce(app, { fold_thinking: false });
  await app.settle();
  app.el('settings-close').click();
  app.el('settings-btn').click();
  await app.settle();
  assert.equal(app.el('fold-thinking-warn').hidden, true, `still told: ${app.el('fold-thinking-warn').textContent}`);
});

for (const name of ['ssh', 'powershell']) {
  test(`the ${name} switch stays held while its request is out`, async () => {
    let resolveFirst;
    const app = await load({
      globals: { lcGUI: true },
      routes: {
        'GET /api/settings': { ...defaultRoutes()['GET /api/settings'], powershell_available: true },
        [`GET /api/${name}`]: [],
        [`POST /api/settings/${name}`]: () => new Promise((res) => { resolveFirst = () => res({ [name]: true, applied: true, persisted: true }); }),
      },
    });
    app.el('settings-btn').click();
    await app.settle();
    const box = app.el(`${name}-checkbox`);
    box.checked = true;
    box.fire('change');
    await app.settle();
    announce(app, { [name]: true });
    await app.settle();
    assert.equal(box.disabled, true, 'the announcement opened the switch with its request out');
    resolveFirst();
    await app.settle();
    assert.equal(box.disabled, false);
  });
}
