'use strict';

// A speech server that accepts the connection and then says nothing.
//
// This had no symptom at all: every upload stayed open, no text appeared,
// no error appeared, and clicking the microphone off did nothing — because
// stopping waited for those uploads to finish. From the outside, dictation
// was hung, and the only way out was reloading the page.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// never resolves, which is exactly the server being modelled.
const hang = () => new Promise(() => {});

async function dictating(app, audioRoute) {
  app.el('mic').click();
  await app.settle();
  return audioRoute;
}

function shortTimeouts(app) {
  app.dictationTimeouts.requestMs = 30;
  app.dictationTimeouts.flushMs = 30;
  app.dictationTimeouts.stopMs = 30;
}

const ROUTES = {
  'GET /api/dictation': { ready: true, detail: '', language: 'ko', whisper_url: 'http://box:8080', engine: 'whisper at box:8080 (remote)', remote: true, can_save: true },
  'POST /api/dictation': { id: 'd-1' },
  'POST /api/dictation/d-1/stop': { final: '' },
};

test('an upload that never answers is given up on and said out loud', async () => {
  const app = await load({ routes: { ...ROUTES, 'POST /api/dictation/d-1/audio': hang } });
  shortTimeouts(app);

  await dictating(app);
  app.micChunk(new ArrayBuffer(64));
  await app.settle();
  await app.wait(120);

  assert.match(app.el('transcript').innerHTML, /has not answered/,
    'a server that never answers should say so, not sit there');
});

// The click has to work. It used to be swallowed: `live` stayed set until
// the last request came back, and every later click hit the "already
// dictating" guard.
test('the microphone switches off even while an upload is stuck', async () => {
  const app = await load({ routes: { ...ROUTES, 'POST /api/dictation/d-1/audio': hang } });
  shortTimeouts(app);

  await dictating(app);
  app.micChunk(new ArrayBuffer(64));
  await app.settle();

  await app.stopDictation();

  assert.equal(app.isDictating(), false, 'dictation is still running after being stopped');
  assert.match(app.el('mic').textContent, /dictation: off/);
});

// And the daemon is told, so its session (and the engine behind it) is not
// left open until the reaper notices.
test('stopping still reaches the daemon when the uploads are stuck', async () => {
  const app = await load({ routes: { ...ROUTES, 'POST /api/dictation/d-1/audio': hang } });
  shortTimeouts(app);

  await dictating(app);
  app.micChunk(new ArrayBuffer(64));
  await app.settle();
  await app.stopDictation();

  assert.equal(app.callsTo('POST', '/api/dictation/d-1/stop').length, 1);
});

// A stop request that hangs too — which is what the daemon looked like
// before it could cancel a transcription — must not take the UI with it.
test('a stop request that never answers still leaves the box usable', async () => {
  const app = await load({
    routes: { ...ROUTES, 'POST /api/dictation/d-1/audio': hang, 'POST /api/dictation/d-1/stop': hang },
  });
  shortTimeouts(app);

  await dictating(app);
  app.micChunk(new ArrayBuffer(64));
  await app.settle();
  await app.stopDictation();

  assert.equal(app.isDictating(), false);
  assert.equal(app.el('input').classList.contains('has-provisional'), false);
});

// The failure in the field, and the one the deadline above created.
//
// A slow engine leaves audio queued, and the queue is concatenated into
// one request so dictation does not fall further behind. Against a remote
// engine that backlog passed the daemon's body limit within seconds, and
// what came back was "read audio: http: request body too large" — three
// of those and dictation stopped. So one request now carries at most
// 512KB and the rest stays queued.
test('a backlog is uploaded in pieces the daemon will accept', async () => {
  const sizes = [];
  const app = await load({
    routes: {
      ...ROUTES,
      'POST /api/dictation/d-1/audio': (body) => {
        sizes.push(body.byteLength !== undefined ? body.byteLength : body.length);
        return { provisional: '', final: '' };
      },
    },
  });

  await dictating(app);
  // Two minutes of speech piled up behind one slow request: 480 chunks of
  // 8000 bytes, which is what 250ms of 16kHz 16-bit audio weighs.
  for (let i = 0; i < 480; i++) app.micChunk(new ArrayBuffer(8000));
  await app.settle();

  assert.ok(sizes.length > 1, 'the whole backlog went in one request');
  for (const n of sizes) {
    assert.ok(n <= 512 * 1024, `a request carried ${n} bytes, over the cap`);
  }
  // And nothing was dropped on the way.
  assert.equal(sizes.reduce((a, b) => a + b, 0), 480 * 8000);
});
