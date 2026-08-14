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

// What dictation is willing to wait for, in milliseconds. A var-like
// object rather than constants so a test can shorten them; nothing else
// writes to it.
//
// These exist because of one failure that had no symptom at all: a speech
// server that accepts the connection and then says nothing. Every upload
// stayed open, the queue behind it grew, no text ever appeared, no error
// ever appeared — and clicking the microphone off did nothing, because
// stopping waits for the uploads to finish and they never did. From the
// outside, dictation was simply hung.
//
//   requestMs  how long one chunk's upload may take. Past it the request
//              is abandoned and said out loud.
//   flushMs    how long stopping will wait for already-recorded audio to
//              finish uploading. The tail of a sentence is worth a moment;
//              it is not worth the microphone appearing not to switch off.
//   stopMs     how long stopping will wait for the daemon's own answer,
//              which carries whatever was mid-sentence.
export const dictationTimeouts = { requestMs: 12000, flushMs: 1500, stopMs: 4000 };

// after resolves once ms have passed, for racing against a wait that may
// never end.
const after = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// makeDownsampler builds the 48kHz-to-16kHz converter the capture worklet
// runs on every block of microphone audio.
//
// It is a plain function declaration with no references to anything
// outside itself, because its source is interpolated verbatim into the
// worklet below — a worklet runs in its own realm and can see nothing from
// this module. Written this way it is also the exact code a test can call.
//
// What it does, and why it is not just "take every third sample": at 48kHz
// the microphone signal carries energy up to 24kHz, and 16kHz audio can
// only represent 8kHz. Dropping samples without filtering first does not
// discard everything above 8kHz — it folds it back down into the speech
// band as noise that no later stage can tell from speech. Sibilants and
// plosives are where that energy lives, and running words together packs
// more of them per second, so the fastest speech is the worst-affected:
// "it stops recognising anything as soon as I speak a little quickly".
//
// So: a 4th-order Butterworth low-pass (two biquads, the Q values that
// make the pair maximally flat) at 7kHz, then linear interpolation at the
// fractional sample positions, which also makes a non-integer rate ratio —
// a 44.1kHz device, say — an ordinary case rather than a broken one.
export function makeDownsampler(inRate, outRate) {
  const step = inRate / outRate;
  const cutoff = Math.min(outRate * 0.44, inRate * 0.45);

  function biquad(q) {
    const w0 = (2 * Math.PI * cutoff) / inRate;
    const alpha = Math.sin(w0) / (2 * q);
    const cw = Math.cos(w0);
    const a0 = 1 + alpha;
    return {
      b0: ((1 - cw) / 2) / a0,
      b1: (1 - cw) / a0,
      b2: ((1 - cw) / 2) / a0,
      a1: (-2 * cw) / a0,
      a2: (1 - alpha) / a0,
      x1: 0, x2: 0, y1: 0, y2: 0,
    };
  }
  const stages = [biquad(0.54119610), biquad(1.30656296)];

  function filter(x) {
    for (const s of stages) {
      const y = s.b0 * x + s.b1 * s.x1 + s.b2 * s.x2 - s.a1 * s.y1 - s.a2 * s.y2;
      s.x2 = s.x1; s.x1 = x;
      s.y2 = s.y1; s.y1 = y;
      x = y;
    }
    return x;
  }

  // pos is where the next output sample falls, measured in input samples
  // since the last block's end, and prev is the one input sample before
  // this block that the interpolation may still need. Keeping both across
  // calls is what stops a click at every block boundary.
  let pos = 0;
  let prev = 0;
  return function push(input) {
    const out = [];
    for (let i = 0; i < input.length; i++) {
      const cur = filter(input[i]);
      while (pos <= i) {
        const frac = i - pos;
        out.push(cur * (1 - frac) + prev * frac);
        pos += step;
      }
      prev = cur;
    }
    pos -= input.length;
    return out;
  };
}

// The AudioWorklet. It lives here as a string because a worklet has to
// be loaded from its own URL, and a blob: URL keeps it in this file
// instead of a fifth static asset that has to be found and served.
//
// Its whole job is resampling and framing: the browser captures at
// whatever the device prefers (48kHz, usually) and the recognizer only
// accepts 16kHz, so the conversion happens once, here, rather than in
// every recognizer implementation.
export const WORKLET_SRC = `
${makeDownsampler.toString().replace(/^export\s+/, '')}

class Capture extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.down = makeDownsampler(sampleRate, options.processorOptions.target);
    this.out = [];
    this.frame = options.processorOptions.frame;
  }
  process(inputs) {
    const ch = inputs[0] && inputs[0][0];
    if (!ch) return true;
    for (const s of this.down(ch)) this.out.push(s);
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
    // Consecutive upload failures, so one hiccup is not fatal.
    failures: 0,
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

// takeQueued removes everything queued and returns it as one buffer.
//
// Audio arrives on a fixed clock — a chunk every CHUNK_MS — while a
// request can take much longer than that: committing an utterance
// re-transcribes the whole sentence, and on a slow machine that is
// seconds. Uploading one chunk per round trip then falls further and
// further behind, which is what "I am still talking and the text has
// stopped moving" is. Concatenating is lossless (the recognizer takes any
// length of PCM) and turns a backlog into a single request.
function takeQueued(session) {
  const chunks = session.queue.splice(0, session.queue.length);
  if (chunks.length === 1) return chunks[0];
  let total = 0;
  for (const c of chunks) total += c.byteLength;
  const joined = new Uint8Array(total);
  let at = 0;
  for (const c of chunks) {
    joined.set(new Uint8Array(c), at);
    at += c.byteLength;
  }
  return joined.buffer;
}

// How many upload failures in a row before dictation gives up.
//
// One was the old answer, and it made every hiccup fatal: a chunk that
// failed for any reason at all turned the microphone off mid-sentence and
// left an error in the transcript. Most causes are momentary — a request
// that raced a reap, an engine that was busy — and the next chunk is a
// quarter of a second away.
const MAX_UPLOAD_FAILURES = 3;

// postChunk uploads one chunk and gives up on it after requestMs.
//
// The abort matters as much as the deadline: without it the browser keeps
// the connection and the daemon keeps the session's lock, so the request
// that was given up on is still in the way of the next one and of the
// stop. The controller is kept on the session so stopping can abort
// whatever is in flight rather than waiting it out.
async function postChunk(session, chunk) {
  const controller = new AbortController();
  session.inFlight = controller;
  let timer = null;
  // The deadline rejects on its own rather than relying on the abort to
  // do it. Aborting is what frees the connection and the daemon's lock,
  // and it is what a browser turns into a rejected fetch — but a
  // transport that ignores the signal would otherwise leave this awaiting
  // a promise that never settles, which is the exact failure being fixed.
  const deadline = new Promise((_, reject) => {
    timer = setTimeout(() => {
      controller.abort();
      reject(new Error(
        `the speech engine has not answered in ${Math.round(dictationTimeouts.requestMs / 1000)}s`
        + ' — check the engine address in settings (the gear under the prompt)'));
    }, dictationTimeouts.requestMs);
  });
  try {
    return await Promise.race([
      apiClient.sendDictationAudio(session.id, chunk, controller.signal),
      deadline,
    ]);
  } catch (err) {
    // Said the first time it happens, not after three of them. The whole
    // complaint about this failure was that nothing was ever said, and
    // three deadlines is most of a minute of silence.
    if (controller.signal.aborted && !session.warnedSlow) {
      session.warnedSlow = true;
      appendError(String(err.message || err));
    }
    throw err;
  } finally {
    clearTimeout(timer);
    if (session.inFlight === controller) session.inFlight = null;
  }
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
      const chunk = takeQueued(session);
      let res;
      try {
        res = await postChunk(session, chunk);
        session.failures = 0;
      } catch (err) {
        // A chunk still in flight when the session was stopped fails
        // because the session is gone — which is what stopping means.
        // Reporting it was the "no dictation session" error that
        // appeared every time a dictated prompt was sent: pressing Enter
        // stops the microphone, and the last chunk lost the race.
        if (live !== session) return;

        // Back at the front of the queue, ahead of whatever arrived while
        // this was in flight, so a retry sends the same audio in the same
        // order. Dropping it would lose the words in it silently, which is
        // the one outcome dictation must not have.
        session.queue.unshift(chunk);

        // The daemon closes a session it believes has been abandoned. If
        // that happens while someone is still talking, the honest repair
        // is to open another one and carry on — the words already
        // committed stay in the box, and the alternative is the
        // microphone switching itself off mid-sentence.
        if (err && err.status === 404 && await reopen(session)) continue;

        session.failures = (session.failures || 0) + 1;
        if (session.failures < MAX_UPLOAD_FAILURES) continue;

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
      // A transcription that failed says why, once. Dictation carries on:
      // the microphone is still open and the next window may well work.
      // Saying nothing is what made a remote server speaking a different
      // protocol look like a broken microphone — audio going out four
      // times a second, every request refused, no text and no error.
      if (res.error) appendError(res.error);
      if (res.final) commitFinal(res.final);
      session.provisional = res.provisional || '';
      render();
    }
  } finally {
    session.sending = false;
  }
}

// reopen replaces the daemon-side session of a dictation that is still
// running, after the daemon closed the old one. Reports whether it worked;
// the caller falls through to the ordinary failure path if it did not.
async function reopen(session) {
  try {
    const id = await apiClient.startDictation();
    if (live !== session) {
      apiClient.stopDictation(id).catch(() => {});
      return false;
    }
    session.id = id;
    // The new recognizer has never heard the sentence in progress, so the
    // grey text belongs to a session that no longer exists. What was
    // committed stays; the unsettled tail is gone, and pretending
    // otherwise would leave words on screen that nothing will ever
    // correct.
    session.provisional = '';
    session.failures = 0;
    render();
    return true;
  } catch {
    return false;
  }
}

export async function stopDictation() {
  if (!live) return;
  const session = live;
  // A second click while the first stop is still finishing is not a
  // second stop. Without this it re-ran the whole path, including waiting
  // on the same requests again.
  if (session.stopping) return;
  session.stopping = true;

  // Capture stops first, so no new audio joins the queue while the tail
  // of it is flushed.
  if (session.node) session.node.port.onmessage = null;
  if (session.stream) session.stream.getTracks().forEach((t) => t.stop());

  // The microphone is genuinely off at this point — the tracks are
  // stopped — so the pill says so now rather than after the round trips
  // below. It used to say "on" until the last one came back, which
  // against an unresponsive engine was forever: the click had worked and
  // there was nothing on screen that said so.
  setMicState(false);

  // Then let the audio already recorded finish uploading, before the
  // session is closed out from under it. Nulling `live` first — which is
  // what this used to do — dropped whatever was still queued and left an
  // in-flight chunk to arrive at a session that no longer existed. That
  // cost the last word or two of every dictated sentence, and announced
  // it as an error.
  //
  // Bounded, though. This wait used to have no limit, so an engine that
  // never answered meant a stop that never finished: `live` stayed set,
  // every later click hit the guard at the top, and dictation could not
  // be switched off without reloading the page. The tail of a sentence is
  // worth a moment; it is not worth that.
  try {
    await Promise.race([session.draining, after(dictationTimeouts.flushMs)]);
  } catch (err) {
    // drain reports its own failures; there is nothing to add here.
  }

  live = null; // now nothing else will touch this session
  // Anything still in flight is now for a session nobody is listening to,
  // and on the daemon's side it is holding that session's lock — which
  // the stop below has to take.
  if (session.inFlight) session.inFlight.abort();
  if (session.ctx) await session.ctx.close().catch(() => {});

  try {
    const res = await Promise.race([
      apiClient.stopDictation(session.id),
      after(dictationTimeouts.stopMs).then(() => ({})),
    ]);
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
