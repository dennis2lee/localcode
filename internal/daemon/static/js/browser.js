// The browser panel: the page the model works in, shown at the top of the
// right panel in the desktop window, where the person can watch it and
// take it over.
//
// Drawn only in the GUI window (window.lcGUI, set by the window before any
// page script runs) and only while the browser setting is on. The frame
// stream it opens is also how the daemon knows a GUI window is showing
// this conversation, which is the condition for the model being offered
// the browser at all: closing the stream (the setting going off, another
// conversation opened) takes the tool away with it.

import { app, session } from './state.js';
import { api } from './api.js';

// The page size the daemon renders every tab at (browser.ViewportWidth and
// ViewportHeight), which is the frame of every coordinate sent back.
export const PAGE_WIDTH = 1280;
export const PAGE_HEIGHT = 800;

const el = (id) => document.getElementById(id);

let streamFor = null;   // the conversation the <img> is streaming
let pollTimer = null;
let lastMove = 0;
let reconnectTimer = null;
let daemonLost = false;  // the last location poll failed
let dragging = false;    // a button went down on the view and is still held

// inGUI is whether this page is in the desktop window.
export function inGUI() {
  return typeof window !== 'undefined' && window.lcGUI === true;
}

// browserShown is whether the panel is drawn for the conversation on
// screen.
export function browserShown() {
  return inGUI() && !!app.browser && !!session.sessionID;
}

// pagePoint maps a point in the shown image to the page's own pixels.
// The image keeps the page's proportions (CSS aspect-ratio), so one scale
// serves both axes; clamped, so a drag that leaves the image still lands
// on the page.
export function pagePoint(offsetX, offsetY, shownWidth, shownHeight) {
  const sx = shownWidth > 0 ? PAGE_WIDTH / shownWidth : 1;
  const sy = shownHeight > 0 ? PAGE_HEIGHT / shownHeight : 1;
  const clamp = (v, max) => Math.max(0, Math.min(max, v));
  return { x: Math.round(clamp(offsetX * sx, PAGE_WIDTH)), y: Math.round(clamp(offsetY * sy, PAGE_HEIGHT)) };
}

// modifierBits are a DOM event's modifier keys as the DevTools protocol
// numbers them: Alt 1, Ctrl 2, Meta 4, Shift 8.
export function modifierBits(e) {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
}

const BUTTONS = ['left', 'middle', 'right'];

// isMac is whether the shortcuts here are the Command ones.
export function isMac() {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || '');
}

const SHORTCUTS = { a: 'selectAll', c: 'copy', x: 'cut', v: 'paste' };

// editCommandFor is the editing command a shortcut stands for, sent with
// the key so the page carries it out: the browser does not take Cmd+C or
// Cmd+V from a key event alone on macOS. Paste is the page reading the
// clipboard itself, so the person's Cmd+V pastes what they copied.
export function editCommandFor(e, mac = isMac()) {
  const primary = mac ? e.metaKey : e.ctrlKey;
  const other = mac ? e.ctrlKey : e.metaKey;
  if (!primary || other || e.altKey || typeof e.key !== 'string') return '';
  const k = e.key.toLowerCase();
  if (k === 'z') return e.shiftKey ? 'redo' : 'undo';
  if (k === 'y' && !mac) return 'redo';
  return SHORTCUTS[k] || '';
}

// keyEventFor is a DOM key event as the daemon passes it to the page.
//
// A printable key carries its text, so it types, unless a shortcut
// modifier is held: Ctrl+A selects rather than typing an "a". A
// character the keyboard layout composed with Option (macOS) or AltGr
// (Ctrl+Alt elsewhere) is text too, sent without those modifiers, so the
// page sees the character the person typed. Enter types a line break
// only on its own or with Shift; Ctrl+Enter or Cmd+Enter is a key press.
export function keyEventFor(e, down, mac = isMac()) {
  let mods = modifierBits(e);
  const printable = typeof e.key === 'string' && [...e.key].length === 1;
  const altGraph = (typeof e.getModifierState === 'function' && e.getModifierState('AltGraph')) || (!!e.ctrlKey && !!e.altKey);
  const composes = mac ? !e.metaKey && !e.ctrlKey : !e.metaKey && (altGraph || (!e.ctrlKey && !e.altKey));
  let text = '';
  if (down && printable && composes) {
    text = e.key;
    mods &= 8;
  } else if (down && e.key === 'Enter' && !e.ctrlKey && !e.metaKey && !e.altKey) {
    text = '\r';
  }
  let type = 'keyUp';
  if (down) type = text ? 'keyDown' : 'rawKeyDown';
  const ev = { type, key: e.key, code: e.code || '', keyCode: e.keyCode || 0, text, modifiers: mods };
  const command = down ? editCommandFor(e, mac) : '';
  if (command) ev.commands = [command];
  return ev;
}

// Input goes to the daemon one request at a time, in order: requests
// sent side by side can arrive in any order, and a release that overtakes
// its press is a click the page never sees.
let inputChain = Promise.resolve();
let queuedMove = null;

function postInput(id, body) {
  return api('POST', `/api/sessions/${id}/browser/input`, body).catch(() => {});
}

function sendInput(body) {
  const id = session.sessionID;
  if (!id) return Promise.resolve();
  inputChain = inputChain.then(() => postInput(id, body));
  return inputChain;
}

// sendMove queues a pointer move, replacing one still waiting its turn:
// only where the pointer is now matters.
function sendMove(body) {
  const id = session.sessionID;
  if (!id) return;
  if (queuedMove && queuedMove.id === id) {
    queuedMove.body = body;
    return;
  }
  const slot = { id, body };
  queuedMove = slot;
  inputChain = inputChain.then(() => {
    if (queuedMove === slot) queuedMove = null;
    return postInput(slot.id, slot.body);
  });
}

function showEmpty(hasPage) {
  const empty = el('browser-empty');
  if (empty) empty.hidden = hasPage;
}

// reconnect opens the frame stream again after it broke (the daemon
// restarted, the connection dropped), if the panel still shows the same
// conversation. The query only makes the address new, so the <img>
// fetches it again.
function reconnect(delay) {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    const view = el('browser-view');
    if (!view || !browserShown() || streamFor !== session.sessionID) return;
    view.setAttribute('src', `/api/sessions/${streamFor}/browser/stream?r=${Date.now()}`);
  }, delay);
}

async function refreshLocation() {
  if (!browserShown()) return;
  const id = session.sessionID;
  try {
    const st = await api('GET', `/api/sessions/${id}/browser`);
    if (id !== session.sessionID) return;
    if (daemonLost) {
      // The daemon is back; the stream it had open went with it.
      daemonLost = false;
      reconnect(0);
    }
    showEmpty(!!st.has_tab);
    const url = el('browser-url');
    // Not while the person is typing an address into it.
    if (url && document.activeElement !== url && st.url && st.url !== 'about:blank') url.value = st.url;
  } catch {
    // The daemon may be restarting; the next tick tries again.
    daemonLost = true;
  }
}

// renderBrowserPanel shows or hides the panel and points its stream at
// the conversation on screen. Called when the setting changes and when a
// conversation is opened.
export function renderBrowserPanel() {
  const section = el('browser-section');
  const view = el('browser-view');
  if (!section || !view) return;
  const shown = browserShown();
  section.hidden = !shown;
  if (!shown) {
    if (streamFor !== null) {
      // Dropping the source ends the stream, and with it the daemon's
      // reason to offer this conversation the browser.
      view.removeAttribute('src');
      streamFor = null;
    }
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    return;
  }
  if (streamFor !== session.sessionID) {
    streamFor = session.sessionID;
    // Through the attribute, which is what removeAttribute above takes
    // away again.
    view.setAttribute('src', `/api/sessions/${streamFor}/browser/stream`);
    const url = el('browser-url');
    if (url) url.value = '';
    showEmpty(false);
  }
  if (!pollTimer) pollTimer = setInterval(refreshLocation, 2000);
  refreshLocation();
}

async function navigate(body) {
  if (!session.sessionID) return;
  try {
    const res = await api('POST', `/api/sessions/${session.sessionID}/browser/navigate`, body);
    showEmpty(false);
    const url = el('browser-url');
    if (url && res && res.url) url.value = res.url;
  } catch (err) {
    const empty = el('browser-empty');
    if (empty) {
      empty.textContent = `Could not open it: ${err.message || err}`;
      empty.hidden = false;
    }
  }
}

// initBrowserPanel wires the panel's controls. Once, at startup.
export function initBrowserPanel() {
  const view = el('browser-view');
  const url = el('browser-url');
  if (!view || !url) return;

  url.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    e.preventDefault();
    const target = url.value.trim();
    if (target) navigate({ url: target });
  });
  el('browser-back')?.addEventListener('click', () => navigate({ action: 'back' }));
  el('browser-forward')?.addEventListener('click', () => navigate({ action: 'forward' }));
  el('browser-reload')?.addEventListener('click', () => navigate({ action: 'reload' }));

  // A point on the view, in the page's pixels. From the pointer's place in
  // the window where the view can say where it is, so a drag that leaves
  // the view still lands on the page's edge; from the event's own offset
  // otherwise.
  const point = (e) => {
    if (typeof view.getBoundingClientRect === 'function' && typeof e.clientX === 'number') {
      const r = view.getBoundingClientRect();
      return pagePoint(e.clientX - r.left - (view.clientLeft || 0), e.clientY - r.top - (view.clientTop || 0), view.clientWidth, view.clientHeight);
    }
    return pagePoint(e.offsetX, e.offsetY, view.clientWidth, view.clientHeight);
  };
  const button = (e) => BUTTONS[e.button] || 'left';
  // Hover effects need moves, and a page does not need sixty a second.
  const move = (e, held) => {
    const now = Date.now();
    if (now - lastMove < 30) return;
    lastMove = now;
    const p = point(e);
    const mouse = { type: 'mouseMoved', x: p.x, y: p.y, modifiers: modifierBits(e) };
    // A move with a button held is a drag: the page is told which.
    if (held && e.buttons) {
      mouse.buttons = e.buttons;
      mouse.button = e.buttons & 1 ? 'left' : (e.buttons & 2 ? 'right' : 'middle');
    }
    sendMove({ mouse });
  };
  // While a button is held the window follows the pointer, so a drag that
  // leaves the view goes on, and its release is never lost: a page that
  // missed it would go on thinking the button is down.
  const onDragMove = (e) => move(e, true);
  const onDragEnd = (e) => {
    if (!dragging) return;
    dragging = false;
    window.removeEventListener('mousemove', onDragMove);
    window.removeEventListener('mouseup', onDragEnd);
    const p = point(e);
    sendInput({ mouse: { type: 'mouseReleased', x: p.x, y: p.y, button: button(e), buttons: e.buttons || 0, clickCount: e.detail || 1, modifiers: modifierBits(e) } });
  };
  view.addEventListener('mousedown', (e) => {
    e.preventDefault();
    view.focus();
    const p = point(e);
    sendInput({ mouse: { type: 'mousePressed', x: p.x, y: p.y, button: button(e), buttons: e.buttons || 0, clickCount: e.detail || 1, modifiers: modifierBits(e) } });
    if (!dragging) {
      dragging = true;
      window.addEventListener('mousemove', onDragMove);
      window.addEventListener('mouseup', onDragEnd);
    }
  });
  view.addEventListener('mousemove', (e) => {
    if (!dragging) move(e, false);
  });
  view.addEventListener('wheel', (e) => {
    e.preventDefault();
    const p = point(e);
    // A wheel that counts in lines or pages, as a page counts pixels.
    const unit = e.deltaMode === 1 ? 16 : (e.deltaMode === 2 ? PAGE_HEIGHT : 1);
    sendInput({ mouse: { type: 'mouseWheel', x: p.x, y: p.y, deltaX: e.deltaX * unit, deltaY: e.deltaY * unit, modifiers: modifierBits(e) } });
  }, { passive: false });
  // A stream that broke is opened again, after a pause so a daemon that
  // is down is not asked for it in a tight loop.
  view.addEventListener('error', () => reconnect(2000));
  view.addEventListener('contextmenu', (e) => e.preventDefault());
  view.addEventListener('keydown', (e) => {
    e.preventDefault();
    sendInput({ key: keyEventFor(e, true) });
  });
  view.addEventListener('keyup', (e) => {
    e.preventDefault();
    sendInput({ key: keyEventFor(e, false) });
  });
  view.addEventListener('paste', (e) => {
    const text = e.clipboardData && e.clipboardData.getData('text');
    if (text) {
      e.preventDefault();
      sendInput({ text });
    }
  });
}
