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

// keyEventFor is a DOM key event as the daemon passes it to the page. A
// printable key with no Ctrl, Alt or Meta carries its text, so it types;
// everything else is a key press with no text, so Ctrl+A selects rather
// than typing an "a".
export function keyEventFor(e, down) {
  const mods = modifierBits(e);
  const printable = typeof e.key === 'string' && [...e.key].length === 1;
  const text = down && printable && !(mods & (1 | 2 | 4)) ? e.key : (down && e.key === 'Enter' ? '\r' : '');
  let type = 'keyUp';
  if (down) type = text ? 'keyDown' : 'rawKeyDown';
  return { type, key: e.key, code: e.code || '', keyCode: e.keyCode || 0, text, modifiers: mods };
}

function sendInput(body) {
  if (!session.sessionID) return Promise.resolve();
  return api('POST', `/api/sessions/${session.sessionID}/browser/input`, body).catch(() => {});
}

function showEmpty(hasPage) {
  const empty = el('browser-empty');
  if (empty) empty.hidden = hasPage;
}

async function refreshLocation() {
  if (!browserShown()) return;
  const id = session.sessionID;
  try {
    const st = await api('GET', `/api/sessions/${id}/browser`);
    if (id !== session.sessionID) return;
    showEmpty(!!st.has_tab);
    const url = el('browser-url');
    // Not while the person is typing an address into it.
    if (url && document.activeElement !== url && st.url && st.url !== 'about:blank') url.value = st.url;
  } catch {
    // The daemon may be restarting; the next tick tries again.
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

  const point = (e) => pagePoint(e.offsetX, e.offsetY, view.clientWidth, view.clientHeight);
  view.addEventListener('mousedown', (e) => {
    e.preventDefault();
    view.focus();
    const p = point(e);
    sendInput({ mouse: { type: 'mousePressed', x: p.x, y: p.y, button: BUTTONS[e.button] || 'left', clickCount: e.detail || 1, modifiers: modifierBits(e) } });
  });
  view.addEventListener('mouseup', (e) => {
    const p = point(e);
    sendInput({ mouse: { type: 'mouseReleased', x: p.x, y: p.y, button: BUTTONS[e.button] || 'left', clickCount: e.detail || 1, modifiers: modifierBits(e) } });
  });
  view.addEventListener('mousemove', (e) => {
    // Hover effects need moves, and a page does not need sixty a second.
    const now = Date.now();
    if (now - lastMove < 50) return;
    lastMove = now;
    const p = point(e);
    sendInput({ mouse: { type: 'mouseMoved', x: p.x, y: p.y, modifiers: modifierBits(e) } });
  });
  view.addEventListener('wheel', (e) => {
    e.preventDefault();
    const p = point(e);
    sendInput({ mouse: { type: 'mouseWheel', x: p.x, y: p.y, deltaX: e.deltaX, deltaY: e.deltaY, modifiers: modifierBits(e) } });
  }, { passive: false });
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
