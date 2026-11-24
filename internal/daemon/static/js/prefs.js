// The settings the page keeps for itself: the one place that reads and
// writes them.
//
// They live in localStorage, which belongs to the person and their screen
// rather than to the daemon. In a browser tab that is all there is to it.
// In the desktop window the storage is tied to an address the window does
// not always get back (see js/kept.js), so there each change is also sent
// to the daemon, which writes it into ~/.localcode/window-state.json
// before it answers. js/kept.js reads that file back at the start of the
// next page.
//
// Every read and write is wrapped: a private window, cleared site data or
// a storage quota all throw, and none of them is a reason for the page
// not to draw.

// The page's own names. The daemon refuses any other.
const OWN = /^(?:localcode\.[A-Za-z0-9][A-Za-z0-9._-]{0,63}|archiveOpen)$/;

// A request small enough to be finished by the browser after the page is
// gone, which is what a change made just before the window closes needs.
// The browser's limit is 64 KiB of body, counted in bytes, and it refuses
// a larger one outright rather than sending it without the promise. One
// unit of a JavaScript string is at most three bytes when it is sent,
// which is the bound used: counting units alone let a body of Korean
// group names through at a third of its real size, and every retry was
// refused.
const KEEPALIVE_MAX = 60000;
const BYTES_PER_CHAR = 3;

// How many times a write that failed is sent before it is left for the
// next change: six, with the waits doubling from 0.3 s, is 9.3 s in all.
const RETRIES = 6;

// keeping is whether this page writes to the daemon: js/kept.js got an
// answer at the start of this page, which it only asks for in the desktop
// window.
function keeping() {
  if (typeof window === 'undefined') return false;
  return window.localcodeKept === 'read' || window.localcodeKept === 'nothing yet';
}

export function readPref(key) {
  // What the storage would not take, in the desktop window: see unstored.
  if (unstored.has(key)) return unstored.get(key);
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

// What this page holds under a name that its storage would not take, in
// the desktop window. Empty while storage takes writes, which is all but
// always, and then nothing here is done differently for it.
//
// Storage that refuses a write goes on answering with the value from
// before it. A page that went by that answer built its next change of a
// value on one that left out its last, sent nothing for a change back to
// the value the storage still gave, and took a fold another window made
// for one it already had. The file is where the window's settings are
// kept, so the page goes on from what it read from the file or sent to
// it, and the storage only costs this address its own copy. A browser
// tab sends nothing and has only its storage to go by, as before.
const unstored = new Map();

// writePref keeps value under key, or forgets key when value is empty.
export function writePref(key, value) {
  const v = value === null || value === undefined || value === '' ? null : String(value);
  const held = unstored.has(key);
  let was = held ? unstored.get(key) : undefined;
  try {
    if (!held) was = localStorage.getItem(key);
    if (v === null) localStorage.removeItem(key);
    else localStorage.setItem(key, v);
    unstored.delete(key);
  } catch {
    // Storage refused: this page keeps the choice, and the next page at
    // this address starts without it unless the window's file has it.
    if (keeping() && OWN.test(key)) unstored.set(key, v);
  }
  // Nothing changed, so there is nothing to send: several of these are
  // written again on every page load with the value they already have.
  if (was === v) return;
  keep(key, v);
}

// readShared is readPref for the read that starts a change to a value
// holding several items: the panel widths, the panels each conversation
// shows, the folded groups. A change to one item is written as the whole
// value with that item changed, so the value the change starts from
// decides what every other item is afterwards.
//
// Two browser tabs share one localStorage, and a tab starts from what the
// other last wrote. Two desktop windows do not: each is on its own port,
// with its own storage, filled from the file when its page started (see
// js/kept.js). A change that started from that copy put every other item
// back to what the page had read then. Hiding a panel for one
// conversation in one window brought back the panel another window had
// hidden for a different conversation since.
//
// So in the window the page's copy of key is made the file's again
// first, with the synchronous request js/kept.js makes at the start of
// the page: about a millisecond on loopback, and the answer has to be in
// hand before the caller goes on. A read that only draws stays readPref.
//
// The page's copy is left as it is in two cases. A name this page has
// changed and the daemon has not taken: the page's is then the newer of
// the two, and the file's would undo the change that is on its way. And
// any name when the daemon gives no answer that can be read: the change
// is then made from what the page holds, as every change was before.
export function readShared(key) {
  const mine = readPref(key);
  if (!keeping() || !OWN.test(key) || ahead.has(key)) return mine;
  const kept = fileNow();
  if (kept === null) return mine;
  const has = Object.prototype.hasOwnProperty.call(kept, key);
  // The daemon writes strings. Anything else is not read as a value,
  // which is the rule js/kept.js has.
  if (has && typeof kept[key] !== 'string') return mine;
  const theirs = has ? kept[key] : null;
  if (theirs !== mine) {
    try {
      if (theirs === null) localStorage.removeItem(key);
      else localStorage.setItem(key, theirs);
      unstored.delete(key);
    } catch {
      // Storage refused: the change still starts from the file's, and
      // the page goes on from it.
      unstored.set(key, theirs);
    }
  }
  return theirs;
}

// What the file holds, asked for once in a turn of the page: a panel's
// toggle changes two of these values, and opening a conversation reads
// two. null when there is no answer to read, which is also kept for the
// turn, so a daemon that is not answering is asked once and not three
// times with the page stopped for each.
let asked = null;

function fileNow() {
  if (asked !== null) return asked.kept;
  let kept = null;
  try {
    const ask = new XMLHttpRequest();
    ask.open('GET', '/api/window/state', false);
    ask.send(null);
    if (ask.status === 200) {
      const answer = JSON.parse(ask.responseText);
      const k = answer && answer.available === true ? answer.kept : null;
      // An object of names. null is a file not written yet, and an array
      // has no names: neither is something to replace the page's copy
      // with.
      if (k && typeof k === 'object' && !Array.isArray(k)) kept = k;
    }
  } catch { /* no answer */ }
  asked = { kept };
  queueMicrotask(() => { asked = null; });
  return kept;
}

// What has not reached the daemon yet, by name: a later write to a name
// replaces an earlier one that was still waiting.
const waiting = new Map();
// The names whose value in this page the file does not have: changed
// here, and not yet taken by the daemon. That is one waiting to be sent,
// one in the request on its way, and one the daemon refused. readShared
// leaves these as the page has them.
const ahead = new Set();
let sending = false;
// Whether a change was made while a request was on its way.
let arrived = false;

function keep(key, value) {
  if (!keeping() || !OWN.test(key)) return;
  waiting.set(key, value);
  ahead.add(key);
  arrived = true;
  send();
}

// send writes what is waiting, one request at a time. A request that
// fails is tried again, a little later each time, for about ten seconds:
// long enough for a daemon that is being replaced under the window. Then
// it stops, and what was not taken goes with the next change. A request
// the daemon refuses is dropped, because sending it again changes nothing.
async function send() {
  if (sending) return;
  sending = true;
  try {
    let failed = 0;
    // Whether a request may ask to outlive the page. Given up for the
    // rest of this run once a request that asked was not sent at all:
    // the limit is shared with every other such request of the page.
    let outlive = true;
    while (waiting.size > 0) {
      arrived = false;
      const batch = new Map(waiting);
      waiting.clear();
      const body = JSON.stringify(Object.fromEntries(batch));
      const keepalive = outlive && body.length * BYTES_PER_CHAR <= KEEPALIVE_MAX;
      let done = false;
      let refusedUnsent = false;
      try {
        const res = await fetch('/api/window/state', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body,
          keepalive,
        });
        done = res.ok || (res.status >= 400 && res.status < 500);
        // Taken: the file has these now, except a name changed again
        // while this was on its way.
        if (res.ok) for (const k of batch.keys()) if (!waiting.has(k)) ahead.delete(k);
      } catch {
        // The daemon is not answering, or the browser would not send
        // it as one that outlives the page.
        if (keepalive) {
          outlive = false;
          refusedUnsent = true;
        }
      }
      if (done) {
        failed = 0;
        continue;
      }
      for (const [k, v] of batch) if (!waiting.has(k)) waiting.set(k, v);
      // Sent plainly at once, and not counted: it may never have left.
      if (refusedUnsent) continue;
      failed += 1;
      if (failed >= RETRIES) {
        // Stopped here, unless something was changed while this last
        // request was on its way: that change has not been tried once,
        // and the daemon may be answering again.
        if (!arrived) return;
        failed = 0;
      }
      await new Promise((resolve) => setTimeout(resolve, Math.min(5000, 300 * 2 ** Math.max(0, failed - 1))));
    }
  } finally {
    sending = false;
  }
}

// handOver gives the daemon what this address already holds, the first
// time a page finds that nothing has been kept: the settings somebody made
// before this file existed are the ones to keep, not ones to lose.
function handOver() {
  if (!keeping() || window.localcodeKept !== 'nothing yet') return;
  window.localcodeKept = 'read';
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (typeof key !== 'string' || !OWN.test(key)) continue;
      const value = localStorage.getItem(key);
      if (value !== null && value !== '') {
        waiting.set(key, value);
        ahead.add(key);
      }
    }
  } catch { /* storage refused: nothing to hand over */ }
  if (waiting.size > 0) send();
}

handOver();
