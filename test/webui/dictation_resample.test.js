'use strict';

// The microphone is captured at whatever the device prefers (48kHz,
// usually) and the recognizer only takes 16kHz. This is the conversion.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

const IN = 48000;
const OUT = 16000;

function tone(hz, rate, n, at = 0) {
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) out[i] = Math.sin((2 * Math.PI * hz * (i + at)) / rate);
  return out;
}

function rms(samples) {
  let sum = 0;
  for (const s of samples) sum += s * s;
  return Math.sqrt(sum / samples.length);
}

// Feed a long tone a block at a time, the way the worklet does, and
// measure the second half — the first blocks include the filter settling
// from silence, which is not what is being asked about here.
function through(down, hz, blocks = 40, size = 128) {
  const out = [];
  for (let b = 0; b < blocks; b++) out.push(...down(tone(hz, IN, size, b * size)));
  return out.slice(Math.floor(out.length / 2));
}

// A voice band tone comes through as itself.
test('speech-band audio passes at close to full level', async () => {
  const app = await load();
  const down = app.makeDownsampler(IN, OUT);
  const level = rms(through(down, 1000));
  assert.ok(level > 0.6, `1kHz came through at ${level.toFixed(3)} rms, want ~0.7`);
});

// And one above what 16kHz can represent does not — which is the whole
// point. Dropping every third sample without filtering first does not
// discard 12kHz, it folds it down to 4kHz at full strength, right in the
// middle of the speech band, where nothing downstream can tell it from
// speech. Fast talking puts more energy up there (sibilants, plosives,
// shorter phonemes), which is why speaking quickly was what broke it.
test('audio above the 16kHz band is filtered out rather than folded into it', async () => {
  const app = await load();
  const down = app.makeDownsampler(IN, OUT);
  const level = rms(through(down, 12000));
  assert.ok(level < 0.05, `12kHz aliased back in at ${level.toFixed(3)} rms, want it near silent`);
});

// The output rate has to hold across blocks: a converter that keeps its
// own position only within one block drifts, and the drift is a click at
// every block boundary and a slowly wrong sample rate.
test('the output rate is exactly one sample in three, block after block', async () => {
  const app = await load();
  const down = app.makeDownsampler(IN, OUT);
  let total = 0;
  for (let b = 0; b < 100; b++) total += down(tone(440, IN, 128, b * 128)).length;
  const expected = (100 * 128) / 3;
  assert.ok(Math.abs(total - expected) <= 1, `produced ${total} samples, want about ${expected}`);
});

// A 44.1kHz device is an ordinary case, not a broken one: the ratio is
// not a whole number and the sample positions land between input samples.
test('a device that captures at 44.1kHz still converts', async () => {
  const app = await load();
  const down = app.makeDownsampler(44100, OUT);
  let total = 0;
  for (let b = 0; b < 100; b++) total += down(tone(440, 44100, 441, b * 441)).length;
  const expected = (100 * 441 * OUT) / 44100;
  assert.ok(Math.abs(total - expected) <= 1, `produced ${total} samples, want about ${expected}`);
});

// The worklet is a string, assembled by interpolating the converter's own
// source into it — a worklet runs in its own realm and can see nothing
// from the module. A mistake there is invisible in ordinary testing: the
// worklet fails to load, the catch around it reports "microphone
// unavailable", and it looks like a permissions problem.
test('the worklet source is runnable and emits frames of the size asked for', async () => {
  const app = await load();

  class AudioWorkletProcessor {
    constructor() {
      this.port = { postMessage: (buf) => this.sent.push(buf), onmessage: null };
      this.sent = [];
    }
  }
  let Registered = null;
  const registerProcessor = (name, cls) => { Registered = cls; };

  // eslint-disable-next-line no-new-func
  new Function('AudioWorkletProcessor', 'registerProcessor', 'sampleRate', app.WORKLET_SRC)(
    AudioWorkletProcessor, registerProcessor, IN,
  );
  assert.ok(Registered, 'the worklet registered no processor');

  const frame = (OUT * 250) / 1000; // one 250ms chunk, as the page asks for
  const node = new Registered({ processorOptions: { target: OUT, frame } });
  for (let b = 0; b < 200; b++) node.process([[tone(440, IN, 128, b * 128)]]);

  assert.ok(node.sent.length >= 1, 'the worklet produced no audio');
  for (const buf of node.sent) {
    assert.equal(buf.byteLength, frame * 2, 'a chunk is 16-bit samples, one frame long');
  }
});
