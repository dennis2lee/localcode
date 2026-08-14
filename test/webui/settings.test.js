'use strict';

// The settings window holds two kinds of setting that behave differently,
// and these tests pin that difference: the microphone is remembered in
// this browser and never sent to the daemon, while the language and the
// engine address are the daemon's and are persisted there.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const MICS = [
  { kind: 'audioinput', deviceId: 'default', label: 'MacBook Pro Microphone' },
  { kind: 'audioinput', deviceId: 'usb-1234', label: 'Yeti Stereo Microphone' },
  { kind: 'videoinput', deviceId: 'cam-1', label: 'FaceTime HD Camera' },
];

function dictationRoutes(extra = {}) {
  return {
    'GET /api/dictation': {
      ready: true, detail: '', language: 'ko', whisper_url: '',
      engine: 'whisper (local): /m/whisper-server with /m/ggml-small.bin',
      remote: false, can_save: true, ...extra,
    },
  };
}

test('the settings button opens the window and lists microphones', async () => {
  const app = await load({ devices: MICS, routes: dictationRoutes() });

  app.el('settings-btn').click();
  await app.settle();

  assert.equal(app.settings.isOpen, true);
  const html = app.el('mic-device-select').innerHTML;
  assert.match(html, /System default/);
  assert.match(html, /Yeti Stereo Microphone/);
  // Cameras are not microphones.
  assert.ok(!html.includes('FaceTime'), html);
});

test('the daemon settings are shown as the daemon reports them', async () => {
  const app = await load({ devices: MICS, routes: dictationRoutes() });

  app.el('settings-btn').click();
  await app.settle();

  assert.equal(app.el('dictation-language-select').value, 'ko');
  assert.equal(app.el('whisper-url-input').value, '');
  assert.match(app.el('dictation-engine-note').textContent, /whisper \(local\)/);
});

test('saving sends the daemon settings and keeps the microphone local', async () => {
  const app = await load({
    devices: MICS,
    routes: {
      ...dictationRoutes(),
      'POST /api/dictation/settings': {
        ready: true, detail: '', language: 'en', whisper_url: 'http://box:8080',
        engine: 'whisper at box:8080 (remote — recorded audio leaves this machine)',
        remote: true, save_error: '',
      },
    },
  });

  app.el('settings-btn').click();
  await app.settle();

  app.el('mic-device-select').value = 'usb-1234';
  app.el('dictation-language-select').value = 'en';
  app.el('whisper-url-input').value = '  http://box:8080  ';
  app.el('settings-save').click();
  await app.settle();

  const posts = app.callsTo('POST', '/api/dictation/settings');
  assert.equal(posts.length, 1);
  assert.equal(posts[0].body.language, 'en');
  assert.equal(posts[0].body.whisper_url, 'http://box:8080');
  assert.equal(Object.keys(posts[0].body).length, 3);
  // The microphone is this browser's business — it must not travel.
  assert.ok(!('mic' in posts[0].body), JSON.stringify(posts[0].body));
  assert.equal(app.storage.get('localcode.micDeviceId'), 'usb-1234');

  assert.match(app.el('settings-save-note').textContent, /Saved/);
  // A remote engine is called out rather than merely stated.
  assert.match(app.el('dictation-engine-note').textContent, /leaves this machine/);
  assert.match(app.el('dictation-engine-note').className, /warn/);
});

test('dictation opens the chosen microphone', async () => {
  const app = await load({
    devices: MICS,
    localStorage: { 'localcode.micDeviceId': 'usb-1234' },
    routes: { ...dictationRoutes(), 'POST /api/dictation': { id: 'd-1' } },
  });

  app.el('mic').click();
  await app.settle();

  assert.equal(app.mediaConstraints.length, 1, 'getUserMedia was not called');
  // "exact", so an unplugged device fails loudly instead of quietly
  // recording from a different one.
  assert.equal(app.mediaConstraints[0].audio.deviceId.exact, 'usb-1234');
});

test('with no microphone chosen the browser picks', async () => {
  const app = await load({
    devices: MICS,
    routes: { ...dictationRoutes(), 'POST /api/dictation': { id: 'd-1' } },
  });

  app.el('mic').click();
  await app.settle();

  assert.equal(app.mediaConstraints.length, 1);
  assert.equal(app.mediaConstraints[0].audio.deviceId, undefined);
});

// A daemon started without a config.json can still be configured, but the
// change does not outlive it. Saying so beats a control that silently
// forgets.
test('a daemon with nowhere to save says so', async () => {
  const app = await load({ devices: MICS, routes: dictationRoutes({ can_save: false }) });

  app.el('settings-btn').click();
  await app.settle();

  assert.match(app.el('settings-save-note').textContent, /not saved/i);
});

// Before microphone access has been granted once, browsers hide the
// labels. A list of anonymous entries is not a choice, so that state is
// named rather than presented as one.
test('unnamed devices are explained rather than listed blankly', async () => {
  const app = await load({
    devices: [{ kind: 'audioinput', deviceId: 'a', label: '' }, { kind: 'audioinput', deviceId: 'b', label: '' }],
    routes: dictationRoutes(),
  });

  app.el('settings-btn').click();
  await app.settle();

  assert.match(app.el('mic-device-select').innerHTML, /after you allow microphone access/);
});

test('Tab does not cycle agents while the settings window is open', async () => {
  const app = await load({ devices: MICS, routes: dictationRoutes() });
  app.el('settings-btn').click();
  await app.settle();

  const before = app.callsTo('POST', /\/agent$/).length;
  app.press('Tab');
  await app.settle();
  assert.equal(app.callsTo('POST', /\/agent$/).length, before, 'Tab switched agents under the modal');
});

// The engine's address is one string on the daemon and two boxes in the
// panel: the machine is one decision and the port is another, and a single
// field invites an address with no port and nothing to say one was wanted.
test('the address and port boxes are filled from the one setting', async () => {
  const app = await load({
    devices: MICS,
    routes: {
      ...dictationRoutes(),
      'GET /api/dictation': {
        ready: true, detail: '', language: 'ko',
        whisper_url: 'http://192.168.1.50:9000', whisper_api: 'whisperx',
        engine: 'whisper at 192.168.1.50:9000 (remote)', remote: true, can_save: true,
      },
    },
  });

  app.el('settings-btn').click();
  await app.settle();

  assert.equal(app.el('whisper-url-input').value, 'http://192.168.1.50');
  assert.equal(app.el('whisper-port-input').value, '9000');
  assert.equal(app.el('whisper-api-select').value, 'whisperx');
});

test('the address and port boxes are sent as one address', async () => {
  const app = await load({
    devices: MICS,
    routes: {
      ...dictationRoutes(),
      'POST /api/dictation/settings': {
        ready: true, detail: '', language: '', whisper_url: 'http://192.168.1.50:9000',
        whisper_api: 'openai', engine: 'whisper at 192.168.1.50:9000 (remote)', remote: true, save_error: '',
      },
    },
  });

  app.el('settings-btn').click();
  await app.settle();

  app.el('whisper-url-input').value = 'http://192.168.1.50';
  app.el('whisper-port-input').value = '9000';
  app.el('whisper-api-select').value = 'openai';
  app.el('settings-save').click();
  await app.settle();

  const body = app.callsTo('POST', '/api/dictation/settings')[0].body;
  assert.equal(body.whisper_url, 'http://192.168.1.50:9000');
  assert.equal(body.whisper_api, 'openai');
});

// An empty address means "run it here", whatever is in the port box —
// a leftover port must not turn into an address of its own.
test('an empty address means the engine runs on this machine', async () => {
  const app = await load({ devices: MICS, routes: dictationRoutes() });
  assert.equal(app.joinAddress('', '8080'), '');
  assert.equal(app.joinAddress('  ', ''), '');
});

// The forgiving cases: a port pasted into the address box, a bare host, a
// trailing slash, and an IPv6 literal whose colons are not a port.
test('splitting and joining an address survives the ways people type one', async () => {
  const app = await load({ devices: MICS, routes: dictationRoutes() });

  // Compared field by field: these objects are made inside the page's
  // realm, so they are not deepEqual to a plain object from this one.
  const split = (s) => { const r = app.splitAddress(s); return [r.host, r.port]; };
  assert.deepEqual(split('box:8080'), ['box', '8080']);
  assert.deepEqual(split('https://speech.example.com/'), ['https://speech.example.com', '']);
  assert.deepEqual(split('[::1]:9000'), ['[::1]', '9000']);
  assert.deepEqual(split(''), ['', '']);

  // A port in the address box, and the port box left empty: what was
  // typed is what is meant.
  assert.equal(app.joinAddress('box:9000', ''), 'box:9000');
  // Both given: the port box is the one being edited, so it wins.
  assert.equal(app.joinAddress('box:9000', '8080'), 'box:8080');
});
