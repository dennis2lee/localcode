import { inputEl, micBtn } from './dom.js';
import * as apiClient from './api.js';
import { appendError } from './transcript.js';
import { selectedMicDeviceId } from './settings.js';
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

// starting is true between asking the daemon for a session and having
// one. It exists so a second click cannot slip through the `live` check
// while the first is still waiting for an answer.
let starting = false;

// joinText is how dictated text is added to what precedes it, in one
// place so the box and the session's own record cannot drift apart.
function joinText(before, text) {
  if (!text) return before;
  return before + (before && !before.endsWith(' ') ? ' ' : '') + text;
}

// finishInput replaces the span of the prompt box that dictation owned
// with the settled text, leaving anything typed after it alone.
//
// The stop round trip lands after the microphone is off and the box is
// the user's again. Writing the session's own snapshot back — which is
// what this used to do — discarded everything typed in between and put
// the caret at the end. Appending blindly is not right either: the box
// already shows the provisional text, so the final transcription of the
// same words would appear twice.
//
// So: take what dictation put there, put the settled version in its
// place, keep the rest. If the box no longer starts with what dictation
// wrote, it has been edited in a way this cannot reason about, and the
// text belongs to the user — it is left as it is.
function finishInput(session, final) {
  const owned = joinText(session.committed, session.provisional);
  const settled = joinText(session.committed, final);
  const current = inputEl.value;
  if (current.startsWith(owned)) {
    inputEl.value = settled + current.slice(owned.length);
  } else if (current === '') {
    inputEl.value = settled;
  }
}

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
  // Claimed before the first await, not after it. `live` used to be
  // assigned only once the daemon answered, so two clicks — or one click
  // on a slow connection — both passed the guard, both opened a
  // microphone, and the second assignment orphaned the first stream and
  // its AudioContext. stopDictation could then never reach them: the
  // browser's recording indicator stayed lit with dictation off, and the
  // microphone was held until the tab closed.
  if (starting || live) return;
  starting = true;
  let id;
  try {
    id = await apiClient.startDictation();
  } catch (err) {
    appendError(`could not start dictation: ${err}`);
    return;
  } finally {
    starting = false;
  }
  if (live) {
    // A second call got there first while this one was waiting. Close
    // the session this one opened rather than leaking it daemon-side.
    apiClient.stopDictation(id).catch(() => {});
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
    // The chosen microphone, when there is one. "exact" so a saved
    // device that is no longer plugged in fails and says so, rather than
    // silently recording from a different one — someone who picked a
    // headset mic and got the laptop's built-in instead would have no
    // way to tell from the transcript that anything was wrong.
    const audio = { channelCount: 1, echoCancellation: true, noiseSuppression: true };
    const deviceId = selectedMicDeviceId();
    if (deviceId) audio.deviceId = { exact: deviceId };
    live.stream = await navigator.mediaDevices.getUserMedia({ audio });
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

  setMicState(true);
}

// The pill names its own state rather than relying on the recording dot
// alone: "is it listening?" is the one question a microphone control has
// to answer without being hovered.
function setMicState(recording) {
  micBtn.classList.toggle('recording', recording);
  micBtn.textContent = recording ? '\u{1F3A4} dictation: on' : '\u{1F3A4} dictation: off';
  micBtn.title = recording ? 'click to stop dictating' : 'click to dictate a prompt';
}

// enqueue serializes the uploads. Chunks arrive on a fixed clock while a
// request can take longer than one chunk, and posting them concurrently
// would let audio reach the recognizer out of order — which for a
// streaming model does not just reorder words, it corrupts the decode.
function enqueue(buffer) {
  if (!live) return;
  live.queue.push(buffer);
  // The promise is kept so stopDictation can wait for the tail to finish
  // uploading instead of racing it.
  if (!live.sending) live.draining = drain();
}

async function drain() {
  if (!live || live.sending) return;
  // Pinned rather than re-read through `live` on every line: this loop
  // awaits, and by the time an await returns the session may have been
  // stopped and a new one started. Comparing the pinned session against
  // `live` is how each step knows whether it is still the current one.
  const session = live;
  session.sending = true;
  try {
    while (live === session && session.queue.length > 0) {
      const chunk = session.queue.shift();
      let res;
      try {
        res = await apiClient.sendDictationAudio(session.id, chunk);
      } catch (err) {
        // A chunk still in flight when the session was stopped fails
        // because the session is gone — which is what stopping means.
        // Reporting it was the "no dictation session" error that
        // appeared every time a dictated prompt was sent: pressing Enter
        // stops the microphone, and the last chunk lost the race.
        if (live !== session) return;
        appendError(`dictation stopped: ${err}`);
        // Cleared before stopping: stopDictation waits on this promise to
        // let the tail of the audio finish, and this *is* that promise —
        // leaving it set would have it wait for the loop that is calling
        // it, which never returns.
        session.draining = null;
        await stopDictation();
        return;
      }
      if (live !== session) return; // stopped while the request was in flight
      if (res.final) commitFinal(res.final);
      session.provisional = res.provisional || '';
      render();
    }
  } finally {
    session.sending = false;
  }
}

export async function stopDictation() {
  if (!live) return;
  const session = live;

  // Capture stops first, so no new audio joins the queue while the tail
  // of it is flushed.
  if (session.node) session.node.port.onmessage = null;
  if (session.stream) session.stream.getTracks().forEach((t) => t.stop());

  // Then let the audio already recorded finish uploading, before the
  // session is closed out from under it. Nulling `live` first — which is
  // what this used to do — dropped whatever was still queued and left an
  // in-flight chunk to arrive at a session that no longer existed. That
  // cost the last word or two of every dictated sentence, and announced
  // it as an error.
  try {
    await session.draining;
  } catch (err) {
    // drain reports its own failures; there is nothing to add here.
  }

  live = null; // now nothing else will touch this session
  if (session.ctx) await session.ctx.close().catch(() => {});

  setMicState(false);

  try {
    const res = await apiClient.stopDictation(session.id);
    // Whatever was mid-sentence when the button was clicked is text the
    // person said and meant; dropping it because they stopped talking
    // half a second early would be its own small bug.
    //
    // Appended to what is in the box now, though, not to the snapshot
    // taken when dictation started. This round trip lands after the user
    // is free to type again, and writing the old snapshot back threw
    // away everything typed in between and moved the caret to the end.
    finishInput(session, res.final || '');
  } catch (err) {
    // Nothing to add to the box, and nothing to take out of it either:
    // whatever is there now is the user's.
  }
  inputEl.classList.remove('has-provisional');
  inputEl.setSelectionRange(inputEl.value.length, inputEl.value.length);
  autoResizeInput();
}

export function toggleDictation() {
  return live ? stopDictation() : startDictation();
}
