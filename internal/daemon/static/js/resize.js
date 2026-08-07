import {
  leftPanel, rightPanel, resizeLeftHandle, resizeRightHandle,
  toggleLeftBtn, toggleRightBtn,
} from './dom.js';

// Both panels are flex items with a fixed width, so resizing is just
// writing a new width — no layout recalculation of our own, and the
// transcript keeps absorbing whatever is left over via flex: 1.
//
// The bounds are not cosmetic. Below MIN a panel is too narrow for the
// session titles and MCP server names it exists to show, which makes the
// handle feel broken rather than flexible; above MAX one panel can eat
// enough of the window that the transcript — the actual content — becomes
// unreadable. They are absolute pixel values rather than a fraction of the
// window because the things inside a panel are a fixed size too.
const MIN = 160;
const MAX = 640;

const STORAGE_KEY = 'localcode.panelWidths';

function clamp(px) {
  return Math.max(MIN, Math.min(MAX, Math.round(px)));
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

export function setPanelWidth(panel, px) {
  panel.style.width = `${clamp(px)}px`;
}

// A handle drags one panel. side is which edge of that panel the handle
// sits on: the left panel grows as the pointer moves right, the right
// panel grows as it moves left, so the two run in opposite directions.
function attach(handle, panel, side, key) {
  if (!handle || !panel) return;
  let startX = 0;
  let startWidth = 0;
  let dragging = false;

  function onMove(e) {
    if (!dragging) return;
    const delta = side === 'left' ? e.clientX - startX : startX - e.clientX;
    setPanelWidth(panel, startWidth + delta);
  }

  function onUp() {
    if (!dragging) return;
    dragging = false;
    handle.classList.remove('dragging');
    document.body.classList.remove('resizing');
    document.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerup', onUp);
    store({ [key]: parseInt(panel.style.width, 10) });
  }

  handle.addEventListener('pointerdown', (e) => {
    // Left button only: a right-click on the handle should open the
    // context menu, not silently start a drag that ends on the next click.
    if (e.button !== undefined && e.button !== 0) return;
    dragging = true;
    startX = e.clientX;
    startWidth = panel.offsetWidth || parseInt(panel.style.width, 10) || MIN;
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
// go below MIN, because a panel narrower than its own content is worse
// than no panel at all. This is how you get to "no panel at all".
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

export function initResizers() {
  const saved = readStored();
  if (typeof saved.left === 'number') setPanelWidth(leftPanel, saved.left);
  if (typeof saved.right === 'number') setPanelWidth(rightPanel, saved.right);
  setCollapsed(leftPanel, resizeLeftHandle, saved.leftCollapsed === true);
  setCollapsed(rightPanel, resizeRightHandle, saved.rightCollapsed === true);
  attach(resizeLeftHandle, leftPanel, 'left', 'left');
  attach(resizeRightHandle, rightPanel, 'right', 'right');
  attachToggle(toggleLeftBtn, leftPanel, resizeLeftHandle, 'leftCollapsed');
  attachToggle(toggleRightBtn, rightPanel, resizeRightHandle, 'rightCollapsed');
}
