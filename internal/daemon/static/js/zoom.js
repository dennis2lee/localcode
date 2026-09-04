import { app } from './state.js';

// Ctrl+wheel zoom, owned by the page.
//
// The window and the browser both have a zoom of their own, and neither
// is ours to read or restore: WebView2 keeps its factor on the controller
// where no script can reach it, and a reload is a navigation. So somebody
// who had zoomed in to read a long diff, and then ran "/update", came
// back to a page at 100% with no way for localcode to put it back.
//
// The fix is to stop borrowing. The page takes ctrl+wheel and the zoom
// keystrokes for itself, applies the factor as a CSS zoom on the root
// element, and writes it down. A reload then restores it, because it is
// the page's own state rather than the container's.
//
// preventDefault on every one of them, or both zooms apply and one turn
// of the wheel moves two steps.
//
// localStorage rather than sessionStorage, unlike the open conversation:
// a comfortable size is a property of the person and their screen, not
// of this window, and somebody who sized the text once should not do it
// again in the next window.

const KEY = 'localcode.zoom';
const MIN = 0.5;
const MAX = 3;
// The steps browsers use, so ctrl+plus lands where the eye expects.
const STEPS = [0.5, 0.67, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3];

function clamp(z) {
  return Math.max(MIN, Math.min(MAX, z));
}

function read() {
  try {
    const v = parseFloat(localStorage.getItem(KEY));
    return Number.isFinite(v) ? clamp(v) : 1;
  } catch {
    return 1;
  }
}

function write(z) {
  try {
    localStorage.setItem(KEY, String(z));
  } catch { /* storage refused: this window keeps the size, the next starts at 1 */ }
}

// apply sets the factor on the root element.
//
// CSS zoom rather than a transform: a transform scales the box without
// changing layout, so the page would keep the width it had and spill or
// leave a gap. zoom re-lays out, which is what a zoom is, and it is what
// every engine localcode's UI runs in supports today.
export function applyZoom(z) {
  app.zoom = clamp(z);
  // Guarded because this runs during init: a document without a root
  // element is not a browser, and a page that fails to start is a worse
  // answer than one that starts at 100%.
  const root = document.documentElement;
  if (root && root.style) root.style.zoom = app.zoom === 1 ? '' : String(app.zoom);
  write(app.zoom);
}

// step moves one notch in the direction given, from wherever the current
// factor sits between the notches.
function step(dir) {
  const at = app.zoom || 1;
  if (dir > 0) {
    applyZoom(STEPS.find((s) => s > at + 1e-9) ?? MAX);
    return;
  }
  const below = STEPS.filter((s) => s < at - 1e-9);
  applyZoom(below.length ? below[below.length - 1] : MIN);
}

export function wireZoom(target) {
  const el = target || document;
  applyZoom(read());

  el.addEventListener('wheel', (e) => {
    if (!e.ctrlKey && !e.metaKey) return;
    e.preventDefault();
    step(e.deltaY < 0 ? 1 : -1);
  }, { passive: false });

  el.addEventListener('keydown', (e) => {
    if (!e.ctrlKey && !e.metaKey) return;
    // "=" is the unshifted key on the same cap as "+", which is what
    // ctrl+plus actually delivers on most layouts.
    if (e.key === '+' || e.key === '=') {
      e.preventDefault();
      step(1);
    } else if (e.key === '-' || e.key === '_') {
      e.preventDefault();
      step(-1);
    } else if (e.key === '0') {
      e.preventDefault();
      applyZoom(1);
    }
  });
}
