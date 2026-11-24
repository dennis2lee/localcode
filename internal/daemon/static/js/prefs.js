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
const KEEPALIVE_MAX = 60000;

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
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

// writePref keeps value under key, or forgets key when value is empty.
export function writePref(key, value) {
  const v = value === null || value === undefined || value === '' ? null : String(value);
  let was;
  try {
    was = localStorage.getItem(key);
    if (v === null) localStorage.removeItem(key);
    else localStorage.setItem(key, v);
  } catch { /* storage refused: this page keeps the choice, the next starts without it */ }
  // Nothing changed, so there is nothing to send: several of these are
  // written again on every page load with the value they already have.
  if (was === v) return;
  keep(key, v);
}

// What has not reached the daemon yet, by name: a later write to a name
// replaces an earlier one that was still waiting.
const waiting = new Map();
let sending = false;

function keep(key, value) {
  if (!keeping() || !OWN.test(key)) return;
  waiting.set(key, value);
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
    while (waiting.size > 0) {
      const batch = new Map(waiting);
      waiting.clear();
      const body = JSON.stringify(Object.fromEntries(batch));
      let done = false;
      try {
        const res = await fetch('/api/window/state', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body,
          keepalive: body.length <= KEEPALIVE_MAX,
        });
        done = res.ok || (res.status >= 400 && res.status < 500);
      } catch { /* the daemon is not answering: tried again below */ }
      if (done) {
        failed = 0;
        continue;
      }
      for (const [k, v] of batch) if (!waiting.has(k)) waiting.set(k, v);
      failed += 1;
      if (failed >= RETRIES) return;
      await new Promise((resolve) => setTimeout(resolve, Math.min(5000, 300 * 2 ** (failed - 1))));
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
      if (value !== null && value !== '') waiting.set(key, value);
    }
  } catch { /* storage refused: nothing to hand over */ }
  if (waiting.size > 0) send();
}

handOver();
