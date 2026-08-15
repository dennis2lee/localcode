'use strict';

// The update controls in the settings panel.
//
// Two buttons rather than one, because what they do is not the same kind
// of thing: checking asks GitHub what the latest release is, and
// installing replaces the program being used. Nothing happens on opening
// the panel — a check is an outbound request that says which version this
// machine runs, so it is asked for.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const UP_TO_DATE = {
  current: '0.46.0', checked: true, latest: '0.46.0', available: false,
  can_install: true, detail: 'localcode 0.46.0 is the latest release',
};
const AVAILABLE = {
  current: '0.45.2', checked: true, latest: '0.46.0', tag: 'v0.46.0', available: true,
  can_install: true, asset: 'localcode-0.46.0-windows-amd64.msi', size: 23023104,
  page_url: 'https://github.com/o/r/releases/tag/v0.46.0',
};

async function settingsOpen(app) {
  app.el('settings-btn').click();
  await app.settle();
}

test('opening the settings panel asks GitHub nothing', async () => {
  const app = await load({ routes: { 'GET /api/update': AVAILABLE } });
  await settingsOpen(app);

  assert.equal(app.callsTo('GET', '/api/update').length, 0,
    'opening a panel should not tell GitHub which version this machine runs');
  assert.equal(app.el('update-note').textContent, '');
  assert.equal(app.el('update-install-btn').hidden, true);
});

test('a check with nothing new says so and offers no install', async () => {
  const app = await load({ routes: { 'GET /api/update': UP_TO_DATE } });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /latest release/);
  assert.equal(app.el('update-install-btn').hidden, true);
});

test('a newer release brings out the install button, naming what it will fetch', async () => {
  const app = await load({ routes: { 'GET /api/update': AVAILABLE } });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /0\.46\.0 is available/);
  assert.match(app.el('update-note').textContent, /localcode-0\.46\.0-windows-amd64\.msi/);
  assert.equal(app.el('update-install-btn').hidden, false);
  assert.match(app.el('update-install-btn').textContent, /0\.46\.0/);
});

// A daemon reached over the network says so, and the button stays away:
// installing there would replace the program on the server, at the
// request of a browser somewhere else.
test('a daemon that cannot install offers the release page instead', async () => {
  const app = await load({
    routes: {
      'GET /api/update': {
        ...AVAILABLE, can_install: false,
        detail: 'install it on the machine running localcode, or from https://github.com/o/r/releases/tag/v0.46.0',
      },
    },
  });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  assert.equal(app.el('update-install-btn').hidden, true);
  assert.match(app.el('update-note').textContent, /machine running localcode/);
});

// Replacing the running program is not undoable, so it is asked once
// plainly and a "no" does nothing at all.
test('installing asks first, and declining downloads nothing', async () => {
  const app = await load({
    routes: { 'GET /api/update': AVAILABLE, 'POST /api/update/install': { started: true } },
    confirm: false,
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/update/install').length, 0);
});

test('installing reports what the daemon did', async () => {
  const app = await load({
    routes: {
      'GET /api/update': AVAILABLE,
      'POST /api/update/install': {
        version: '0.46.0', started: true, path: 'C:\\cache\\localcode-0.46.0-windows-amd64.msi',
        detail: 'the installer is running; localcode has to close for it to replace the files',
      },
    },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/update/install').length, 1);
  assert.match(app.el('update-note').textContent, /installer is running/);
});

// GitHub being unreachable is a sentence beside the button, not a silence.
test('a check that fails says why', async () => {
  const app = await load({
    routes: { 'GET /api/update': { current: '0.45.2', checked: false, detail: 'dial tcp: no route to host' } },
  });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /no route to host/);
  assert.equal(app.el('update-install-btn').hidden, true);
});
