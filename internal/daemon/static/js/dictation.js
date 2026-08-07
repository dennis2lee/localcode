import { inputEl, micBtn } from './dom.js';
import * as apiClient from './api.js';
import { appendError } from './transcript.js';
import { autoResizeInput } from './composer.js';

// Live dictation: the microphone button captures audio, the daemon turns
// it into text, and the text lands in the prompt box — provisional text
// in grey while the sentence is still being spoken, committed as
// ordinary text when the speaker pauses.
//
// The grey text is not a stylistic flourish. A streaming recognizer
// revises what it already said as later words give it context, so the
// tail of the box genuinely is not settled yet; showing it as normal
// text would have words silently rewriting themselves under the cursor.

// CHUNK_MS is how much audio goes in one request. Short enough that the
// grey text keeps up with speech, long enough that a sentence is not a
// hundred round trips.
const CHUNK_MS = 250;
const SAMPLE_RATE = 16000;

// The AudioWorklet. It lives here as a string because a worklet has to
// be loaded from its own URL, and a blob: URL keeps it in this file
// instead of a fifth static asset that has to be found and served.
//
// Its whole job is resampling and framing: the browser captures at
// whatever the device prefers (48kHz, usually) and the recognizer only
// accepts 16kHz, so the conversion happens once, here, rather than in
// every recognizer implementation.
const WORKLET_SRC = `
class Capture extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.ratio = sampleRate / options.processorOptions.target;
    this.pos = 0;
    this.out = [];
    this.frame = options.processorOptions.frame;
  }
  process(inputs) {
    const ch = inputs[0] && inputs[0][0];
    if (!ch) return true;
    // Nearest-sample decimation. Not a great resampler, but the input is
    // a 48kHz voice band being taken to 16kHz and the recognizer's own
    // feature extraction low-passes it anyway; a polyphase filter here
    // would cost more than it buys.
    for (let i = 0; i < ch.length; i++) {
      this.pos += 1;
      if (this.pos >= this.ratio) {
        this.pos -= this.ratio;
        this.out.push(ch[i]);
      }
    }
    while (this.out.length >= this.frame) {
      const take = this.out.splice(0, this.frame);
      const pcm = new Int16Array(take.length);
      for (let i = 0; i < take.length; i++) {
        // Clamped before scaling: a sample outside [-1, 1) wraps around
        // to the opposite polarity as an int16, which is heard as a
        // click and read by the recognizer as a transient.
        const s = Math.max(-1, Math.min(1, take[i]));
        pcm[i] = s < 0 ? s * 32768 : s * 32767;
      }
      this.port.postMessage(pcm.buffer, [pcm.buffer]);
    }
    return true;
  }
}
registerProcessor('capture', Capture);
`;

// Everything a running dictation owns, so stop() can take it all down
// even if it was interrupted halfway through starting.
let live = null;

export function isDictating() {
  return live !== null;
}

// commitFinal replaces the grey tail with settled text. The caret is put
// after it so someone can keep typing where the dictation left off.
function commitFinal(text) {
  if (!text) return;
  const before = live.committed;
  live.committed = before + (before && !before.endsWith(' ') ? ' ' : '') + text;
  live.provisional = '';
  render();
}

function render() {
  const provisional = live.provisional;
  inputEl.value = live.committed + (provisional ? (live.committed ? ' ' : '') + provisional : '');
  // The grey is drawn by a class on the box rather than by inserting
  // markup: a <textarea> cannot contain styled spans, and swapping it
  // for a contenteditable would cost the IME behaviour that took real
  // work to get right (see the TUI's notes on marked text).
  inputEl.classList.toggle('has-provisional', provisional !== '');
  inputEl.setSelectionRange(inputEl.value.length, inputEl.value.length);
  autoResizeInput();
}

export async function startDictation() {
  if (live) return;
  let id;
  try {
    id = await apiClient.startDictation();
  } catch (err) {
    appendError(`could not start dictation: ${err}`);
    return;
  }

  live = {
    id,
    // Whatever was already typed stays: dictation appends to the box, it
    // does not take it over.
    committed: inputEl.value,
    provisional: '',
    stream: null,
    ctx: null,
    node: null,
    sending: false,
    queue: [],
  };

  try {
    live.stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
    });
    live.ctx = new AudioContext();
    const url = URL.createObjectURL(new Blob([WORKLET_SRC], { type: 'text/javascript' }));
    await live.ctx.audioWorklet.addModule(url);
    URL.revokeObjectURL(url);

    live.node = new AudioWorkletNode(live.ctx, 'capture', {
      processorOptions: { target: SAMPLE_RATE, frame: (SAMPLE_RATE * CHUNK_MS) / 1000 },
    });
    live.node.port.onmessage = (e) => enqueue(e.data);
    live.ctx.createMediaStreamSource(live.stream).connect(live.node);
  } catch (err) {
    // The usual cause is a denied microphone permission, which is a
    // decision rather than a fault — reported plainly and cleaned up.
    appendError(`microphone unavailable: ${err}`);
    await stopDictation();
    return;
  }

  micBtn.classList.add('recording');
  micBtn.title = 'stop dictation';
}

// enqueue serializes the uploads. Chunks arrive on a fixed clock while a
// request can take longer than one chunk, and posting them concurrently
// would let audio reach the recognizer out of order — which for a
// streaming model does not just reorder words, it corrupts the decode.
function enqueue(buffer) {
  if (!live) return;
  live.queue.push(buffer);
  drain();
}

async function drain() {
  if (!live || live.sending) return;
  live.sending = true;
  try {
    while (live && live.queue.length > 0) {
      const chunk = live.queue.shift();
      let res;
      try {
        res = await apiClient.sendDictationAudio(live.id, chunk);
      } catch (err) {
        appendError(`dictation stopped: ${err}`);
        await stopDictation();
        return;
      }
      if (!live) return; // stopped while the request was in flight
      if (res.final) commitFinal(res.final);
      live.provisional = res.provisional || '';
      render();
    }
  } finally {
    if (live) live.sending = false;
  }
}

export async function stopDictation() {
  if (!live) return;
  const session = live;
  live = null; // stop the drain loop and any late callbacks first

  if (session.node) session.node.port.onmessage = null;
  if (session.stream) session.stream.getTracks().forEach((t) => t.stop());
  if (session.ctx) await session.ctx.close().catch(() => {});

  micBtn.classList.remove('recording');
  micBtn.title = 'dictate a prompt';

  try {
    const res = await apiClient.stopDictation(session.id);
    // Whatever was mid-sentence when the button was clicked is text the
    // person said and meant; dropping it because they stopped talking
    // half a second early would be its own small bug.
    if (res.final) {
      const before = session.committed;
      inputEl.value = before + (before && !before.endsWith(' ') ? ' ' : '') + res.final;
    } else {
      inputEl.value = session.committed;
    }
  } catch (err) {
    inputEl.value = session.committed;
  }
  inputEl.classList.remove('has-provisional');
  inputEl.setSelectionRange(inputEl.value.length, inputEl.value.length);
  autoResizeInput();
}

export function toggleDictation() {
  return live ? stopDictation() : startDictation();
}
