import {
  leftPanel, rightPanel, browserPanel, centerEl,
  resizeLeftHandle, resizeRightHandle, resizeBrowserHandle,
  toggleLeftBtn, toggleRightBtn, toggleBrowserBtn,
} from './dom.js';

// Every panel is a flex item with a fixed width, so resizing is just
// writing a new width — no layout recalculation of our own, and the
// transcript keeps absorbing whatever is left over via flex: 1.
//
// The bounds are not cosmetic. Below the minimum a panel is too narrow for
// the session titles and MCP server names it exists to show, which makes
// the handle feel broken rather than flexible; above the maximum one panel
// can eat enough of the window that the transcript — the actual content —
// becomes unreadable. They are absolute pixel values rather than a fraction
// of the window because the things inside a panel are a fixed size too.
export const SIDE_BOUNDS = { min: 160, max: 640 };

// The browser panel shows a page rendered at 1280 pixels wide (see
// js/browser.js PAGE_WIDTH). Its maximum is the width that shows that page
// at its own pixels: the page, the image's 1px border on either side, the
// panel's 12px padding on either side, and the panel's own 1px left border
// (every box here is border-box). Below its minimum the page is too small
// to read or to click on.
export const BROWSER_BOUNDS = { min: 360, max: 1280 + 2 + 24 + 1 };

// What a drag leaves the transcript, however wide the panel being dragged
// is allowed to be. A window narrower than every panel's width put
// together cannot keep it, and does not try: that is what the toggles are
// for.
export const CENTER_MIN = 280;

const STORAGE_KEY = 'localcode.panelWidths';

function clamp(px, bounds) {
  return Math.max(bounds.min, Math.min(bounds.max, Math.round(px)));
}

// The store is best-effort on purpose. A WebView with storage disabled, or
// a browser in a private mode that throws on write, should cost the user a
// remembered width — not a dead handle or a page that fails to start.
function readStored() {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEY)) || {};
  } catch (err) {
    return {};
  }
}

function store(patch) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...readStored(), ...patch }));
  } catch (err) {
    // ignore
  }
}

export function setPanelWidth(panel, px, bounds = SIDE_BOUNDS) {
  panel.style.width = `${clamp(px, bounds)}px`;
}

const widthOf = (el) => Number(el && el.offsetWidth) || 0;

// roomFor is how much wider panel can get before the transcript is
// narrower than CENTER_MIN. The transcript is always laid out, so a width
// of zero is a transcript the panels have squeezed out, which has no room
// to give. While the browser panel is shown, a side panel can also take
// what the browser panel has above its minimum: the stylesheet shrinks the
// browser panel before the transcript. The browser panel's own drag gets
// only the transcript's room, so widening it never squeezes the side
// panels.
function roomFor(panel) {
  let room = Math.max(0, widthOf(centerEl) - CENTER_MIN);
  if (browserPanel && panel !== browserPanel && !browserPanel.hidden && !browserPanel.classList.contains('collapsed')) {
    room += Math.max(0, widthOf(browserPanel) - BROWSER_BOUNDS.min);
  }
  return room;
}

// A handle drags one panel. side is which edge of that panel the handle
// sits on: the left panel grows as the pointer moves right, the right
// panel grows as it moves left, so the two run in opposite directions.
function attach(handle, panel, side, key, bounds) {
  if (!handle || !panel) return;
  let startX = 0;
  let startWidth = 0;
  let room = 0;
  let dragging = false;

  // A drag ends without saving when its panel goes away under it (the
  // browser setting turned off, or a side panel's toggle pressed from the
  // keyboard, mid-drag): a width for a panel that is not on screen is one
  // nobody chose.
  function gone() {
    return panel.hidden || handle.hidden || panel.classList.contains('collapsed');
  }

  function onMove(e) {
    if (!dragging) return;
    if (gone()) {
      end(false);
      return;
    }
    const delta = side === 'left' ? e.clientX - startX : startX - e.clientX;
    // Growing is limited by the room the transcript has; shrinking is not,
    // so a drag that started with the transcript already narrow can still
    // give it back. room is never negative, so a drag that only tried to
    // widen a panel never makes it narrower than it started.
    setPanelWidth(panel, startWidth + (delta > 0 ? Math.min(delta, room) : delta), bounds);
  }

  function end(save) {
    dragging = false;
    handle.classList.remove('dragging');
    document.body.classList.remove('resizing');
    document.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerup', onUp);
    if (save) store({ [key]: parseInt(panel.style.width, 10) });
  }

  function onUp() {
    if (!dragging) return;
    end(!gone());
  }

  handle.addEventListener('pointerdown', (e) => {
    // Left button only: a right-click on the handle should open the
    // context menu, not silently start a drag that ends on the next click.
    if (e.button !== undefined && e.button !== 0) return;
    dragging = true;
    startX = e.clientX;
    startWidth = panel.offsetWidth || parseInt(panel.style.width, 10) || bounds.min;
    room = roomFor(panel);
    handle.classList.add('dragging');
    document.body.classList.add('resizing');
    // Listeners go on the document, not the handle: the pointer moves
    // faster than layout and will be off the 4px handle for most of the
    // drag. preventDefault stops the browser starting a text selection or
    // a native drag from the mousedown.
    document.addEventListener('pointermove', onMove);
    document.addEventListener('pointerup', onUp);
    if (e.preventDefault) e.preventDefault();
  });

  // Double-click resets to the width the stylesheet asks for, which is the
  // only way back to the default once a drag has written an inline one.
  handle.addEventListener('dblclick', () => {
    panel.style.width = '';
    store({ [key]: null });
  });
}

// Collapsing hides the panel and its handle together — see the CSS. It is
// a separate control from the drag because they answer different
// questions: a drag tunes how much room a visible panel gets, and cannot
// go below the minimum, because a panel narrower than its own content is
// worse than no panel at all. This is how you get to "no panel at all".
export function setCollapsed(panel, handle, collapsed) {
  if (!panel) return;
  panel.classList.toggle('collapsed', collapsed);
  if (handle) handle.classList.toggle('collapsed', collapsed);
}

function attachToggle(btn, panel, handle, key) {
  if (!btn) return;
  btn.addEventListener('click', () => {
    const collapsed = !panel.classList.contains('collapsed');
    setCollapsed(panel, handle, collapsed);
    store({ [key]: collapsed });
    btn.setAttribute('aria-expanded', String(!collapsed));
  });
}

// The browser panel is there while the browser setting is on in the
// desktop window: js/browser.js shows it and its handle, and its header
// button, then. That button collapses it like the side panels' toggles
// do, and the choice is remembered with theirs.
export function initResizers() {
  const saved = readStored();
  if (typeof saved.left === 'number') setPanelWidth(leftPanel, saved.left);
  if (typeof saved.right === 'number') setPanelWidth(rightPanel, saved.right);
  if (typeof saved.browser === 'number' && browserPanel) setPanelWidth(browserPanel, saved.browser, BROWSER_BOUNDS);
  setCollapsed(leftPanel, resizeLeftHandle, saved.leftCollapsed === true);
  setCollapsed(rightPanel, resizeRightHandle, saved.rightCollapsed === true);
  setCollapsed(browserPanel, resizeBrowserHandle, saved.browserCollapsed === true);
  attach(resizeLeftHandle, leftPanel, 'left', 'left', SIDE_BOUNDS);
  attach(resizeRightHandle, rightPanel, 'right', 'right', SIDE_BOUNDS);
  attach(resizeBrowserHandle, browserPanel, 'right', 'browser', BROWSER_BOUNDS);
  attachToggle(toggleLeftBtn, leftPanel, resizeLeftHandle, 'leftCollapsed');
  attachToggle(toggleRightBtn, rightPanel, resizeRightHandle, 'rightCollapsed');
  attachToggle(toggleBrowserBtn, browserPanel, resizeBrowserHandle, 'browserCollapsed');
}
