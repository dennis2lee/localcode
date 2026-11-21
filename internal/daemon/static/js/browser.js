// The browser panel: the page the model works in, shown in a column of
// its own between the conversation and the tasks panel in the desktop
// window, where the person can watch it and take it over.
//
// Every conversation has its own tab (internal/browser Manager.Tab), and
// the panel shows the one of the conversation on screen: opening another
// conversation points the stream at that conversation's tab, and nothing
// the panel showed or was told for the one before carries over.
//
// Drawn only in the GUI window (window.lcGUI, set by the window before any
// page script runs) and only while the browser setting is on. The frame
// stream it opens is also how the daemon knows a GUI window is showing
// this conversation, which is the condition for the model being offered
// the browser at all: closing the stream (the setting going off, another
// conversation opened) takes the tool away with it.

import { app, session } from './state.js';
import { api } from './api.js';
import { zoomKey, zoomWheel } from './zoom.js';

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
let lastPasteKey = 0;    // when a paste shortcut last went to the page
let tabSeenAt = 0;       // when a location poll of this visit first found a tab
let visitHadTab = false; // a location poll of this visit found a tab
let navSeq = 0;          // address bar requests made, so only the newest one's answer is used
let navAnswers = 0;      // address bar answers used, so a poll asked before one is dropped
let tabsDrawn = '';      // the tab strip as last drawn, so a poll that changes nothing redraws nothing
let activeTab = '';      // the id of the tab the view shows
let tabSeq = 0;          // tab strip clicks made, so only the newest one's answer is used

// visit counts the panel's showings of a conversation: it moves on when
// the stream is pointed at a conversation and when the panel is hidden.
// Every answer the panel waits for (a location poll, the address bar, a
// reconnect's timer) belongs to the visit it was asked in and is dropped
// in any other. A conversation's id cannot tell them apart: after a switch
// away and back, an answer to the first visit names the same conversation
// as the second, and it can say there is a page when that visit's view
// still holds the other conversation's frame.
let visit = 0;
let emptyText = null;    // the empty note's own words, kept before an error first replaces them

// How long a tab has to have been there before a location poll shows the
// view though no frame has been seen to arrive. The view is shown on the
// new stream's first frame (the <img>'s load event); this is for an engine
// that does not fire one for a part of a multipart stream, so the view
// cannot stay blank. Counted from the first poll of the visit that found
// the tab, not from the switch: a tab opened long after the switch has
// sent no frame when it is first found. Shorter than the poll interval, so
// it is the next poll after that one that shows it.
export const STALE_FALLBACK_MS = 1500;

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

// restoreEmptyText puts the empty note's own words back, in place of an
// error an address-bar open left there.
function restoreEmptyText() {
  const empty = el('browser-empty');
  if (empty && emptyText !== null) empty.textContent = emptyText;
}

// setStale hides the view's picture, or shows it again. Stale from a
// switch to another conversation until that conversation's stream sends
// a frame: until then the <img> goes on showing the last frame of the
// conversation before, which is another conversation's page.
function setStale(stale) {
  const view = el('browser-view');
  if (view) view.classList.toggle('stale', stale);
}

// reconnect opens the frame stream again after it broke (the daemon
// restarted, the connection dropped), if the panel still shows the same
// conversation. The query only makes the address new, so the <img>
// fetches it again.
function reconnect(delay) {
  if (reconnectTimer) return;
  // The stream opened again is a new one: the fallback waits for a tab
  // seen after it, as after a switch.
  tabSeenAt = 0;
  const asked = visit;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    const view = el('browser-view');
    if (asked !== visit || !view || !browserShown() || streamFor !== session.sessionID) return;
    view.setAttribute('src', `/api/sessions/${streamFor}/browser/stream?r=${Date.now()}`);
  }, delay);
}

// drawTabs draws the conversation's tabs: each a button that makes it the
// active one, with an "x" that closes it. Built with elements, never with
// markup: a tab's title is whatever the page it shows calls itself.
function drawTabs(tabs) {
  const list = el('browser-tab-list');
  if (!list) return;
  const key = JSON.stringify(tabs.map((t) => [t.id, t.title, t.url, t.active]));
  if (key === tabsDrawn) return;
  tabsDrawn = key;
  list.innerHTML = '';
  for (const t of tabs) {
    // A tab with no page calls itself "about:blank"; it is a new tab.
    const blank = !t.url || t.url === 'about:blank';
    const label = blank ? 'New tab' : (t.title && t.title !== 'about:blank' ? t.title : t.url);
    const tab = document.createElement('div');
    tab.className = t.active ? 'browser-tab active' : 'browser-tab';
    const open = document.createElement('button');
    open.className = 'browser-tab-open';
    open.textContent = label;
    open.title = blank ? label : `${label}\n${t.url}`;
    open.addEventListener('click', () => tabAction('POST', `tabs/${encodeURIComponent(t.id)}/activate`));
    const close = document.createElement('button');
    close.className = 'browser-tab-close';
    close.textContent = '×';
    close.title = 'close this tab';
    close.addEventListener('click', (e) => {
      if (e.stopPropagation) e.stopPropagation();
      tabAction('DELETE', `tabs/${encodeURIComponent(t.id)}`);
    });
    tab.appendChild(open);
    tab.appendChild(close);
    list.appendChild(tab);
  }
}

// tabAction is a click on the tab strip: open a tab, switch to one or
// close one. Reports whether its answer was used.
//
// The answer is the conversation's tabs, used only if nothing newer came
// since: the panel is on the visit it was asked in, no later tab click was
// made, and it counts as an answer the location poll has to wait for, as
// an address bar answer does, so a poll asked before it cannot put the
// strip back. A click also retires an address bar open still on its way,
// which is about the tab shown before. The tab it makes active is the one
// the person chose, so its address goes in the bar whatever has the focus.
async function tabAction(method, path) {
  const id = session.sessionID;
  if (!id) return false;
  const asked = visit;
  const n = ++tabSeq;
  navSeq += 1;
  let used = false;
  try {
    const res = await api(method, `/api/sessions/${id}/browser/${path}`);
    if (asked === visit && id === session.sessionID && n === tabSeq) {
      navAnswers += 1;
      const tabs = res && Array.isArray(res.tabs) ? res.tabs : [];
      drawTabs(tabs);
      const a = tabs.find((t) => t.active);
      activeTab = a ? a.id : '';
      const url = el('browser-url');
      if (url) url.value = a && a.url && a.url !== 'about:blank' ? a.url : '';
      used = true;
    }
  } catch {
    // A tab that closed by itself in the meantime: the poll below says
    // what is there now.
  }
  if (asked === visit) refreshLocation();
  return used;
}

async function refreshLocation() {
  if (!browserShown()) return;
  const id = session.sessionID;
  const asked = visit;
  // An address bar answer that arrives while the poll is on its way is
  // newer than what the poll will say: the poll is dropped, and the next
  // one asks again.
  const answers = navAnswers;
  try {
    const st = await api('GET', `/api/sessions/${id}/browser`);
    if (asked !== visit || id !== session.sessionID || answers !== navAnswers) return;
    if (daemonLost) {
      // The daemon is back; the stream it had open went with it.
      daemonLost = false;
      reconnect(0);
    }
    showEmpty(!!st.has_tab);
    const tabs = st.has_tab && Array.isArray(st.tabs) ? st.tabs : [];
    drawTabs(tabs);
    const active = (tabs.find((t) => t.active) || {}).id || '';
    const url = el('browser-url');
    if (active !== activeTab) {
      // Another tab is the one shown now: its address, not the last one's,
      // and none for a tab with no page yet.
      activeTab = active;
      if (url && document.activeElement !== url) url.value = st.url && st.url !== 'about:blank' ? st.url : '';
    }
    if (!st.has_tab) {
      if (visitHadTab) tabGone();
      tabSeenAt = 0;
    } else if (!tabSeenAt) {
      visitHadTab = true;
      tabSeenAt = Date.now();
    } else if (Date.now() - tabSeenAt >= STALE_FALLBACK_MS) {
      setStale(false);
    }
    // Not while the person is typing an address into it.
    if (url && document.activeElement !== url && st.url && st.url !== 'about:blank') url.value = st.url;
  } catch {
    // The daemon may be restarting; the next tick tries again.
    if (asked === visit) daemonLost = true;
  }
}

// newVisit starts a visit: answers to the one before are dropped from now
// on, and a reconnect still waiting for it is called off, since it would
// hold the one slot and drop a reconnect this visit may need.
function newVisit() {
  visit += 1;
  tabSeenAt = 0;
  visitHadTab = false;
  activeTab = '';
  drawTabs([]);
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
}

// tabGone starts the visit over when the conversation's tab closed under
// it (the browser restarted, or the profile was copied again). The view
// still holds the closed tab's last frame, and an answer still on its way
// is about the closed tab. A tab opened next sends its frames on the same
// stream, and an <img> fires load once for a stream, at its first frame,
// so the stream is opened again: the next tab's first frame is then that
// stream's first, and shows the view.
function tabGone() {
  newVisit();
  setStale(true);
  restoreEmptyText();
  const url = el('browser-url');
  if (url && document.activeElement !== url) url.value = '';
  const view = el('browser-view');
  if (view && streamFor) view.setAttribute('src', `/api/sessions/${streamFor}/browser/stream?r=${Date.now()}`);
}

// renderBrowserPanel shows or hides the panel and points its stream at
// the conversation on screen. Called when the setting changes and when a
// conversation is opened.
export function renderBrowserPanel() {
  const panel = el('browser-panel');
  const view = el('browser-view');
  if (!panel || !view) return;
  const shown = browserShown();
  panel.hidden = !shown;
  const handle = el('resize-browser');
  if (handle) handle.hidden = !shown;
  const toggle = el('toggle-browser');
  if (toggle) {
    toggle.hidden = !shown;
    toggle.setAttribute('aria-expanded', String(!panel.classList.contains('collapsed')));
  }
  if (!shown) {
    if (streamFor !== null) {
      newVisit();
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
    newVisit();
    daemonLost = false;
    streamFor = session.sessionID;
    setStale(true);
    // Through the attribute, which is what removeAttribute above takes
    // away again.
    view.setAttribute('src', `/api/sessions/${streamFor}/browser/stream`);
    const url = el('browser-url');
    if (url) url.value = '';
    restoreEmptyText();
    showEmpty(false);
  }
  if (!pollTimer) pollTimer = setInterval(refreshLocation, 2000);
  refreshLocation();
}

// navigate is the address bar and the back, forward and reload buttons.
// The answer can take seconds (the browser may be starting), so it is
// shown only in the visit it was asked in.
async function navigate(body) {
  const id = session.sessionID;
  if (!id) return;
  const asked = visit;
  // Only the newest request's answer is used: an older one that comes back
  // later, a failure from a slow host above all, is about a page the
  // person has already moved on from.
  const n = ++navSeq;
  const current = () => asked === visit && id === session.sessionID && n === navSeq;
  try {
    const res = await api('POST', `/api/sessions/${id}/browser/navigate`, body);
    if (!current()) return;
    navAnswers += 1;
    restoreEmptyText();
    // There is a page now: the note goes.
    showEmpty(true);
    const url = el('browser-url');
    if (url && res && res.url) url.value = res.url;
  } catch (err) {
    if (!current()) return;
    navAnswers += 1;
    const empty = el('browser-empty');
    if (empty) {
      if (emptyText === null) emptyText = empty.textContent;
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
  // A new tab is empty; the address bar is where its page is chosen, so it
  // gets the focus.
  // Only when its answer was used: a switch to another conversation, or a
  // later click, in the meantime is somewhere the focus does not belong.
  el('browser-tab-new')?.addEventListener('click', async () => {
    if (await tabAction('POST', 'tabs')) url.focus();
  });
  el('browser-forward')?.addEventListener('click', () => navigate({ action: 'forward' }));
  el('browser-reload')?.addEventListener('click', () => navigate({ action: 'reload' }));

  // A point on the view, in the page's pixels. From the pointer's place in
  // the window where the view can say where it is, so a drag that leaves
  // the view still lands on the page's edge; from the event's own offset
  // otherwise.
  //
  // The pointer's place and the view's rectangle are in the window's
  // pixels; the view's border and inner size (clientLeft, clientWidth)
  // are in the page's own layout pixels. The two differ by the zoom the
  // page applies to itself (js/zoom.js, a CSS zoom on the root), so every
  // click on a zoomed page landed that factor away from where it was
  // aimed: at 125%, a click on (1000, 600) reached (1257, 757), and a
  // field the person clicked never got the focus their typing needed. The
  // layout sizes are scaled into the window's pixels before dividing.
  const point = (e) => {
    if (typeof view.getBoundingClientRect === 'function' && typeof e.clientX === 'number') {
      const r = view.getBoundingClientRect();
      const kx = view.offsetWidth > 0 ? r.width / view.offsetWidth : 1;
      const ky = view.offsetHeight > 0 ? r.height / view.offsetHeight : kx;
      return pagePoint(
        e.clientX - r.left - (view.clientLeft || 0) * kx,
        e.clientY - r.top - (view.clientTop || 0) * ky,
        view.clientWidth * kx,
        view.clientHeight * ky,
      );
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
    // The window's zoom gesture, like its zoom shortcut below: js/zoom.js
    // takes it, and the page does not get it as well.
    if (zoomWheel(e)) return;
    e.preventDefault();
    const p = point(e);
    // A wheel that counts in lines or pages, as a page counts pixels.
    const unit = e.deltaMode === 1 ? 16 : (e.deltaMode === 2 ? PAGE_HEIGHT : 1);
    sendInput({ mouse: { type: 'mouseWheel', x: p.x, y: p.y, deltaX: e.deltaX * unit, deltaY: e.deltaY * unit, modifiers: modifierBits(e) } });
  }, { passive: false });
  // A stream that broke is opened again, after a pause so a daemon that
  // is down is not asked for it in a tight loop.
  view.addEventListener('error', () => reconnect(2000));
  // A frame arrived on the stream the view is on, so what it shows is the
  // conversation on screen. The event does not say which stream it is
  // for: one the stream before queued in the moment of a switch would show
  // the view early. An engine fires a stream's load once, at its first
  // frame, so that takes a switch in the same instant as that frame.
  // A reconnect still waiting from an error before it is not needed: the
  // stream is working.
  view.addEventListener('load', () => {
    setStale(false);
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  });
  view.addEventListener('contextmenu', (e) => e.preventDefault());
  // The window's zoom shortcut stays the window's: js/zoom.js takes it,
  // wherever the focus is, and one key press does one thing. Sent on to
  // the page as well, Ctrl+plus used to zoom the page in the panel too on
  // Windows and Linux (the daemon no longer lets a key reach the
  // browser's own shortcuts: browser.Tab.KeyEvent), and a zoomed page is
  // not where its picture says it is, so every click after it missed.
  //
  // A key coming up follows what its going down did, whatever is held by
  // then: Ctrl let go before the "=" is, and the page would get the end of
  // a key press it never saw the start of.
  const keptFromPage = new Set();
  view.addEventListener('keydown', (e) => {
    if (zoomKey(e) !== null) {
      keptFromPage.add(e.code || e.key);
      return;
    }
    keptFromPage.delete(e.code || e.key);
    e.preventDefault();
    const key = keyEventFor(e, true);
    if (key.commands && key.commands[0] === 'paste') lastPasteKey = Date.now();
    sendInput({ key });
  });
  view.addEventListener('keyup', (e) => {
    if (keptFromPage.delete(e.code || e.key)) return;
    e.preventDefault();
    sendInput({ key: keyEventFor(e, false) });
  });
  // A paste from the Edit menu. One right behind a paste shortcut is the
  // same paste, which the page has already done from the clipboard.
  view.addEventListener('paste', (e) => {
    if (Date.now() - lastPasteKey < 1000) {
      e.preventDefault();
      return;
    }
    const text = e.clipboardData && e.clipboardData.getData('text');
    if (text) {
      e.preventDefault();
      sendInput({ text });
    }
  });
}
