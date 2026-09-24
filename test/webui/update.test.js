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

// The install that replaces localcode's own binary and brings it back.
//
// Before this the daemon replaced the binary, said "restart localcode to
// run the new version", and nothing did — so the version in the header
// stayed where it was and the update read as one that had not happened.
// The page's job is only to say what is going on: the connection is about
// to go away, and the browser reconnects to the new daemon on the same
// address by itself.
test('a restart is reported and no second install is offered', async () => {
  const app = await load({
    routes: {
      'GET /api/update': AVAILABLE,
      'POST /api/update/install': {
        version: '0.46.0', replaced: true, restarting: true, started: false,
        path: '/home/u/.cache/localcode/updates/localcode-0.46.0-linux-amd64.tar.gz',
        detail: 'installed over /home/u/.local/bin/localcode — restarting localcode now',
      },
    },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /restarting localcode now/);
  // Gone, not merely disabled: the daemon behind it is on its way out, and
  // a second click would post to a server that is no longer there.
  assert.equal(app.el('update-install-btn').hidden, true);
});

// A daemon reached from another machine replaces its own binary and stays
// running, because restarting it is not a browser's to order. The sentence
// is then the whole of what the user gets, so it has to arrive intact.
// An http mirror authenticates nothing, so the panel says so beside the
// address, on the offer itself rather than once somewhere else. The https
// mirror next to it names its source with no such mark.
test('a check against an http mirror marks the source unverified', async () => {
  const app = await load({
    routes: {
      'GET /api/update': {
        ...AVAILABLE, source: 'http://mirror.internal/dl/', source_unverified: true,
      },
    },
  });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /from http:\/\/mirror\.internal\/dl\//);
  assert.match(app.el('update-note').textContent, /unverified/);
  assert.match(app.el('update-note').textContent, /plain http/);
});

test('a check against an https mirror names the source with no unverified mark', async () => {
  const app = await load({
    routes: {
      'GET /api/update': {
        ...AVAILABLE, source: 'https://mirror.internal/dl/',
      },
    },
  });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /from https:\/\/mirror\.internal\/dl\//);
  assert.doesNotMatch(app.el('update-note').textContent, /unverified/);
});

// The install reply is read on its own, after the check has scrolled by,
// so it states the http source again rather than relying on the offer.
test('installing from an http mirror states the unverified source', async () => {
  const app = await load({
    routes: {
      'GET /api/update': {
        ...AVAILABLE, source: 'http://mirror.internal/dl/', source_unverified: true,
      },
      'POST /api/update/install': {
        version: '0.46.0', verified: false, source_unverified: true,
        detail: 'installed over /home/u/.local/bin/localcode',
      },
    },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /unverified/);
  assert.match(app.el('update-note').textContent, /could not be verified/);
});

test('installing from an https mirror states no unverified source', async () => {
  const app = await load({
    routes: {
      'GET /api/update': AVAILABLE,
      'POST /api/update/install': {
        version: '0.46.0', verified: true,
        detail: 'installed over /home/u/.local/bin/localcode',
      },
    },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.doesNotMatch(app.el('update-note').textContent, /unverified/);
});

test('an install with no restart tells the user to restart', async () => {
  const app = await load({
    routes: {
      'GET /api/update': AVAILABLE,
      'POST /api/update/install': {
        version: '0.46.0', replaced: true, restarting: false, started: false,
        detail: 'installed over /home/u/.local/bin/localcode — restart localcode to run the new version',
      },
    },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /restart localcode to run the new version/);
});

// The install is asked once, in fixed words: the program in use is about
// to be replaced, and on Windows that means an elevation prompt and
// localcode closing. In the desktop window the window closes and comes
// back; anywhere else localcode has to be quit.
test('installing in the window asks in fixed words', async () => {
  const app = await load({
    routes: { 'GET /api/update': AVAILABLE },
    confirm: false,
    globals: { lcWindowCommand: () => {} },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.equal(app.confirmMessages.length, 1);
  assert.match(app.confirmMessages[0], /The window closes and the installer runs/);
  assert.match(app.confirmMessages[0], /opens again when the installer has finished/);
});

test('installing outside the window asks in fixed words', async () => {
  const app = await load({
    routes: { 'GET /api/update': AVAILABLE },
    confirm: false,
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.equal(app.confirmMessages.length, 1);
  assert.match(app.confirmMessages[0], /The installer starts when localcode exits/);
  assert.match(app.confirmMessages[0], /Quit localcode to run it/);
});

// In the desktop window the installer starts when the window closes,
// and LocalCode opens again when the installer has finished.
test('a window install says it starts on close and comes back', async () => {
  const app = await load({
    routes: {
      'GET /api/update': AVAILABLE,
      'POST /api/update/install': {
        version: '0.46.0', started: true,
        detail: 'The installer for localcode 0.46.0 starts when this window closes. LocalCode opens again when the installer has finished.',
      },
    },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /starts when this window closes/);
  assert.match(app.el('update-note').textContent, /opens again when the installer has finished/);
});

// In a terminal the installer starts when localcode exits, and the
// person has to quit it.
test('a terminal install says it starts on exit and must be quit', async () => {
  const app = await load({
    routes: {
      'GET /api/update': AVAILABLE,
      'POST /api/update/install': {
        version: '0.46.0', started: true,
        detail: 'The installer for localcode 0.46.0 starts when localcode exits. Quit localcode to run it.',
      },
    },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /starts when localcode exits/);
  assert.match(app.el('update-note').textContent, /Quit localcode to run it/);
});

// A recorded install that did not land is one line: the version, the
// exit code and what it means, and the log path.
test('a failed install is reported beside the offer', async () => {
  const app = await load({
    routes: {
      'GET /api/update': {
        ...AVAILABLE,
        last_install: {
          version: '0.46.0', exit_code: 1602, status: 'cancelled', meaning: 'cancelled',
          log: 'C:\\Users\\u\\AppData\\Local\\localcode\\updates\\localcode-0.46.0-msi.log',
        },
      },
    },
  });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  const note = app.el('update-note').textContent;
  assert.match(note, /0\.46\.0 did not install/);
  assert.match(note, /1602/);
  assert.match(note, /cancelled/);
  assert.match(note, /localcode-0\.46\.0-msi\.log/);
});

// A check with nothing new still reports the failed install.
test('a failed install is reported when already up to date', async () => {
  const app = await load({
    routes: {
      'GET /api/update': {
        ...UP_TO_DATE,
        last_install: {
          version: '0.45.0', exit_code: 1603, status: 'failed', meaning: 'failed (exit code 1603)',
          log: '/home/u/.cache/localcode/updates/localcode-0.45.0-msi.log',
        },
      },
    },
  });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  const note = app.el('update-note').textContent;
  assert.match(note, /latest release/);
  assert.match(note, /0\.45\.0 did not install/);
  assert.match(note, /1603/);
});

// A record with an installed status says the version was installed and
// that localcode has to be restarted to run it: a daemon that is still
// the old version reports the install it just staged. Only the
// not-installed statuses say "did not install".
test('an installed record says it was installed, not that it did not', async () => {
  const app = await load({
    routes: {
      'GET /api/update': {
        ...AVAILABLE,
        last_install: {
          version: '0.46.0', exit_code: 0, status: 'installed', meaning: 'installed',
          log: 'C:\\Users\\u\\AppData\\Local\\localcode\\updates\\localcode-0.46.0-msi.log',
        },
      },
    },
  });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  const note = app.el('update-note').textContent;
  assert.match(note, /0\.46\.0 was installed/);
  assert.match(note, /Restart localcode to run it/);
  assert.doesNotMatch(note, /did not install/);
});

test('a cancelled record still says it did not install', async () => {
  const app = await load({
    routes: {
      'GET /api/update': {
        ...AVAILABLE,
        last_install: {
          version: '0.46.0', exit_code: 1602, status: 'cancelled', meaning: 'cancelled',
          log: 'C:\\Users\\u\\AppData\\Local\\localcode\\updates\\localcode-0.46.0-msi.log',
        },
      },
    },
  });
  await settingsOpen(app);

  app.el('update-check-btn').click();
  await app.settle();

  const note = app.el('update-note').textContent;
  assert.match(note, /0\.46\.0 did not install/);
  assert.match(note, /cancelled/);
});

// In the desktop window the started installer gets its own sentence
// saying when this window closes, and the window closes itself.
test('a started window install says when it closes and closes it', async () => {
  const closed = [];
  const app = await load({
    routes: {
      'GET /api/update': AVAILABLE,
      'POST /api/update/install': {
        version: '0.46.0', started: true,
        detail: 'The installer for localcode 0.46.0 starts when this window closes. LocalCode opens again when the installer has finished.',
      },
    },
    globals: { lcWindowCommand: (cmd) => closed.push(cmd) },
  });
  await settingsOpen(app);
  app.el('update-check-btn').click();
  await app.settle();

  app.el('update-install-btn').click();
  await app.settle();

  assert.match(app.el('update-note').textContent, /This window closes in 3 seconds\./);
  await app.wait(3400);
  assert.deepEqual(closed, ['close']);
});
