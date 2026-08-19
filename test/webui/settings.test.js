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

// A microphone plugged in after the page loaded.
//
// Until a browser has been allowed to use the microphone it does not just
// hide the device names — it reports one generic input and nothing else.
// So a USB microphone or a headset was missing from this list entirely,
// and appeared only after dictating once, which is exactly backwards: the
// panel is where you go to pick the microphone you are about to use.
test('the microphone list can be filled in without dictating first', async () => {
  let granted = false;
  const app = await load({
    devices: () => (granted
      ? [
        { kind: 'audioinput', deviceId: 'default', label: 'MacBook Pro Microphone' },
        { kind: 'audioinput', deviceId: 'usb-1234', label: 'Yeti Stereo Microphone' },
      ]
      : [{ kind: 'audioinput', deviceId: '', label: '' }]),
    routes: dictationRoutes(),
  });
  app.el('settings-btn').click();
  await app.settle();

  assert.equal(app.el('mic-list-btn').hidden, false,
    'with anonymous devices the list is not to be trusted, and the way to fix it should be offered');
  assert.doesNotMatch(app.el('mic-device-select').innerHTML, /Yeti/);

  granted = true;
  app.el('mic-list-btn').click();
  await app.settle();

  assert.match(app.el('mic-device-select').innerHTML, /Yeti Stereo Microphone/,
    'the USB microphone is still missing after asking for access');
  assert.equal(app.el('mic-list-btn').hidden, true, 'there is nothing left for the button to do');
});

// Opening the panel must not ask for the microphone: a permission prompt
// in front of someone who came to change the spoken language is one they
// did not invite.
test('opening the panel does not ask for microphone access', async () => {
  const app = await load({
    devices: [{ kind: 'audioinput', deviceId: '', label: '' }],
    routes: dictationRoutes(),
  });
  app.el('settings-btn').click();
  await app.settle();

  assert.equal(app.mediaConstraints.length, 0);
});

test('a refused microphone says so on the button rather than silently', async () => {
  const app = await load({
    devices: [{ kind: 'audioinput', deviceId: '', label: '' }],
    denyMicrophone: 'NotAllowedError: Permission denied',
    routes: dictationRoutes(),
  });
  app.el('settings-btn').click();
  await app.settle();
  app.el('mic-list-btn').click();
  await app.settle();

  assert.match(app.el('mic-list-btn').textContent, /refused/);
});

// Plugging one in while the panel is open.
test('a device appearing while the panel is open lands in the list', async () => {
  let plugged = false;
  const app = await load({
    devices: () => (plugged
      ? [{ kind: 'audioinput', deviceId: 'usb-1234', label: 'Yeti Stereo Microphone' }]
      : [{ kind: 'audioinput', deviceId: 'default', label: 'MacBook Pro Microphone' }]),
    routes: dictationRoutes(),
  });
  app.el('settings-btn').click();
  await app.settle();
  assert.doesNotMatch(app.el('mic-device-select').innerHTML, /Yeti/);

  plugged = true;
  app.devicesChanged();
  await app.settle();

  assert.match(app.el('mic-device-select').innerHTML, /Yeti Stereo Microphone/);
});

// An engine that cannot honour the spoken language has to say so, beside
// the control it is about.
//
// The report: "I said 'I'm a boy' in English and it wrote 아이엠어보이."
// The sherpa engine is one model per language and the model localcode
// installs is Korean, so English dictated into it is spelled out in
// Hangul — while the panel says Spoken language: English the whole time.
// Nothing connected the two, and the conclusion anyone would draw from
// that is that dictation is broken.
test('an engine that ignores the spoken language says so next to it', async () => {
  const app = await load({
    devices: MICS,
    routes: dictationRoutes({
      engine: 'sherpa (local, in-process)',
      language: 'en',
      language_note: 'The sherpa engine is one model per language, and the model localcode installs is Korean',
    }),
  });
  app.el('settings-btn').click();
  await app.settle();

  const note = app.el('dictation-language-note');
  assert.equal(note.hidden, false, 'the panel offered a setting the engine ignores and said nothing');
  assert.match(note.textContent, /Korean/);
});

// And says nothing when there is nothing to say: a warning that is always
// there is not read.
test('whisper leaves the language note out of the way', async () => {
  const app = await load({ devices: MICS, routes: dictationRoutes() });
  app.el('settings-btn').click();
  await app.settle();

  assert.equal(app.el('dictation-language-note').hidden, true);
});

// The spoken language takes effect when it is chosen.
//
// Closing the panel used to throw it away without a word: pick English,
// close, dictate — and it is still set to Korean. For whisper that does
// not mean "worse English", it means the audio is written *in* the
// language you left selected, so English comes back as 아이엠어보이.
test('choosing a spoken language applies it without pressing Save', async () => {
  const applied = [];
  const app = await load({
    devices: MICS,
    routes: {
      ...dictationRoutes({ language: 'ko', whisper_url: 'box:8080', whisper_api: 'whispercpp' }),
      'POST /api/dictation/settings': (body) => {
        applied.push(body);
        return { ready: true, detail: '', language: body.language, whisper_url: body.whisper_url, whisper_api: body.whisper_api, engine: 'whisper (local)', remote: false };
      },
    },
  });
  app.el('settings-btn').click();
  await app.settle();

  app.el('dictation-language-select').value = 'en';
  app.el('dictation-language-select').fire('change');
  await app.settle();

  assert.equal(applied.length, 1, 'choosing a language did nothing until Save, which is where it was lost');
  assert.equal(applied[0].language, 'en');
  assert.match(app.el('settings-save-note').textContent, /applied/i);
});

// And it carries the engine address the daemon last confirmed, not
// whatever is half-typed in the boxes at that moment.
test('applying the language does not carry a half-typed engine address', async () => {
  const applied = [];
  const app = await load({
    devices: MICS,
    routes: {
      ...dictationRoutes({ language: 'ko', whisper_url: 'box:8080', whisper_api: 'whispercpp' }),
      'POST /api/dictation/settings': (body) => {
        applied.push(body);
        return { ready: true, detail: '', language: body.language, whisper_url: body.whisper_url, whisper_api: body.whisper_api, engine: 'whisper', remote: true };
      },
    },
  });
  app.el('settings-btn').click();
  await app.settle();

  app.el('whisper-url-input').value = 'half-typed-ho';
  app.el('dictation-language-select').value = 'en';
  app.el('dictation-language-select').fire('change');
  await app.settle();

  assert.equal(applied[0].whisper_url, 'box:8080',
    'a language change sent the address mid-keystroke and pointed dictation at nothing');
});

// The address does keep its Save button — it cannot be applied as it is
// typed — so the panel has to say that what is on screen is not in force.
test('an edited engine address says it is not saved yet', async () => {
  const app = await load({ devices: MICS, routes: dictationRoutes() });
  app.el('settings-btn').click();
  await app.settle();

  app.el('whisper-url-input').value = 'box';
  app.el('whisper-url-input').fire('input');

  assert.match(app.el('settings-save-note').textContent, /not saved/i);
});
