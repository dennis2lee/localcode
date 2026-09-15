// Typography: the faces and the text size, owned by this browser.
//
// A comfortable size is a property of the person and their screen, not
// of the window or the daemon, so these live in localStorage exactly
// like the ctrl+wheel zoom in zoom.js: somebody who sized the text once
// does not do it again in the next window, and a daemon reached over
// --server from another machine has no say in which font this screen
// uses. Nothing here touches config.json or /api/settings.
//
// Applying a value writes the custom property on the root element, so
// it takes effect on the next frame with no reload. The stored values
// are applied twice: once by the inline script in index.html's head
// (before the first paint, so a reload never flashes the default face)
// and once at the bottom of this module (for everything that script
// does not cover, and what the test harness exercises).
//
// There is no reliable way to enumerate installed fonts from a page, so
// each face offers the stacks that fit its role plus a free-text field.
// Whatever is chosen keeps a fallback stack behind it: a name that
// resolves to nothing degrades to something readable rather than to
// the browser default.

import {
  typoDocSelect, typoDocCustom, typoUiSelect, typoUiCustom,
  typoMonoSelect, typoMonoCustom, typoSizeSelect,
  typoSizeReadSelect, typoSizeUiSelect, typoSizeCodeSelect,
} from './dom.js';

// The fallback behind every choice for its role. Each is the stack the
// stylesheet already uses, so a custom name that resolves to nothing
// lands where the page was before rather than on the browser default.
export const DOC_FALLBACK = 'Charter, "Sitka Text", "Iowan Old Style", "Bitstream Charter", Cambria, Georgia, serif';
export const UI_FALLBACK = 'ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI Variable Text", "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif';
export const MONO_FALLBACK = 'ui-monospace, "SF Mono", SFMono-Regular, "Cascadia Mono", Consolas, Menlo, monospace';

const CUSTOM = 'custom …';

function stacksFor(role) {
  if (role === 'doc') {
    return [
      ['Default (Charter / Sitka Text)', ''],
      ['Charter, Sitka Text', DOC_FALLBACK],
      ['Georgia, Palatino', 'Georgia, Palatino, "Palatino Linotype", "Book Antiqua", serif'],
      ['System serif', 'ui-serif, Georgia, Cambria, "Times New Roman", serif'],
    ];
  }
  if (role === 'mono') {
    return [
      ['Default (system monospace)', ''],
      ['System monospace', MONO_FALLBACK],
      ['Consolas, Menlo', 'Consolas, Menlo, Monaco, "Courier New", monospace'],
      ['Cascadia, SF Mono', '"Cascadia Mono", "SF Mono", SFMono-Regular, Consolas, monospace'],
    ];
  }
  return [
    ['Default (system interface)', ''],
    ['System interface', UI_FALLBACK],
    ['Helvetica, Arial', '"Helvetica Neue", Helvetica, Arial, sans-serif'],
    ['Verdana', 'Verdana, Geneva, Tahoma, sans-serif'],
  ];
}

const FALLBACK_FOR = { doc: DOC_FALLBACK, ui: UI_FALLBACK, mono: MONO_FALLBACK };
const KEY_FOR = { doc: 'localcode.face.doc', ui: 'localcode.face.ui', mono: 'localcode.face.mono' };
const PROP_FOR = { doc: '--f-doc', ui: '--f-ui', mono: '--f-mono' };
const SELECT_FOR = { doc: () => typoDocSelect, ui: () => typoUiSelect, mono: () => typoMonoSelect };
const CUSTOM_FOR = { doc: () => typoDocCustom, ui: () => typoUiCustom, mono: () => typoMonoCustom };

// The text sizes, as multipliers of the nine-step scale. Not a free
// number: each step stays one of nine known values times one of these,
// so the type system keeps its shape at every setting.
export const SIZE_OPTIONS = [
  ['87.5%', 0.875],
  ['100% (default)', 1],
  ['112.5%', 1.125],
  ['125%', 1.25],
  ['140%', 1.4],
];
const SCALE_KEY = 'localcode.textScale';

// One trim per group of steps. The groups are places, not faces: the mono
// face is set at UI-group sizes wherever it appears in a row (the session
// id, the version, usage figures), so no control can promise "everything in
// this face". Each control promises its steps instead.
const GROUP_KEY_FOR = {
  read: 'localcode.textScale.read',
  ui: 'localcode.textScale.ui',
  code: 'localcode.textScale.code',
};
const GROUP_PROP_FOR = {
  read: '--t-scale-read',
  ui: '--t-scale-ui',
  code: '--t-scale-code',
};
const GROUP_SELECT_FOR = {
  read: () => typoSizeReadSelect,
  ui: () => typoSizeUiSelect,
  code: () => typoSizeCodeSelect,
};

// quoteName keeps a typed name one family: a bare word stays bare, and
// anything with spaces or punctuation is quoted, so the fallback stack
// behind it is still parsed as the fallback rather than as more names.
export function quoteName(name) {
  const bare = /^[a-zA-Z][a-zA-Z0-9-]*$/.test(name);
  return bare ? name : `"${name.replace(/"/g, '')}"`;
}

// faceValue puts the fallback stack behind whatever was chosen. A stack
// picked from the list already carries one, so this is for the typed
// name: without it a name that resolves to nothing falls back to the
// browser default, which is the unreadable outcome.
export function faceValue(name, role) {
  const fallback = FALLBACK_FOR[role] || UI_FALLBACK;
  const clean = String(name || '').trim().replace(/^"+|"+$/g, '').trim();
  if (!clean) return '';
  return `${quoteName(clean)}, ${fallback}`;
}

function rootEl() {
  return (typeof document !== 'undefined' && document.documentElement) || null;
}

function readStored(key) {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStored(key, value) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch { /* storage refused: this window keeps the choice, the next starts at default */ }
}

// readScale parses the stored multiplier. Anything unparseable or out
// of range is the default: a hostile or stale value must not set type
// to nothing or to a wall of pixels.
export function readScale(raw) {
  const s = typeof raw === 'string' ? parseFloat(raw) : raw;
  if (!Number.isFinite(s) || s < 0.5 || s > 2) return 1;
  return s;
}

// applyTypography reads every stored choice onto the root element. One
// function for the head script's shape and the module's: the head
// script cannot import this, so it repeats the same reads inline.
export function applyTypography() {
  const root = rootEl();
  if (!root || !root.style) return;
  for (const role of ['doc', 'ui', 'mono']) {
    const v = readStored(KEY_FOR[role]);
    if (v) root.style.setProperty(PROP_FOR[role], v);
    else if (root.style.removeProperty) root.style.removeProperty(PROP_FOR[role]);
  }
  const scale = readScale(readStored(SCALE_KEY));
  if (scale === 1) {
    if (root.style.removeProperty) root.style.removeProperty('--t-scale');
  } else {
    root.style.setProperty('--t-scale', String(scale));
  }
  for (const group of Object.keys(GROUP_KEY_FOR)) {
    const trim = readScale(readStored(GROUP_KEY_FOR[group]));
    if (trim === 1) {
      if (root.style.removeProperty) root.style.removeProperty(GROUP_PROP_FOR[group]);
    } else {
      root.style.setProperty(GROUP_PROP_FOR[group], String(trim));
    }
  }
}

export function setFace(role, value) {
  const root = rootEl();
  const v = String(value || '');
  writeStored(KEY_FOR[role], v);
  if (root && root.style) {
    if (v) root.style.setProperty(PROP_FOR[role], v);
    else if (root.style.removeProperty) root.style.removeProperty(PROP_FOR[role]);
  }
}

// writeScale stores one multiplier and publishes it, or clears both when
// it is the default: a default that left a property or a key behind would
// be indistinguishable from a choice, and would survive as one.
function writeScale(key, prop, scale) {
  const s = readScale(scale);
  writeStored(key, s === 1 ? '' : String(s));
  const root = rootEl();
  if (root && root.style) {
    if (s === 1) {
      if (root.style.removeProperty) root.style.removeProperty(prop);
    } else {
      root.style.setProperty(prop, String(s));
    }
  }
  return s;
}

export function setTextScale(scale) {
  return writeScale(SCALE_KEY, '--t-scale', scale);
}

export function setGroupScale(group, scale) {
  if (!GROUP_KEY_FOR[group]) return 1;
  return writeScale(GROUP_KEY_FOR[group], GROUP_PROP_FOR[group], scale);
}

// currentChoice maps a stored value back onto a select: the stack it
// matches, the custom row when it matches none, or the default.
function currentChoice(role, stored) {
  if (!stored) return { option: '', custom: '' };
  for (const [, stack] of stacksFor(role)) {
    if (stack && stack === stored) return { option: stack, custom: '' };
  }
  const first = String(stored).split(',')[0].replace(/"/g, '').trim();
  return { option: CUSTOM, custom: first };
}

function fillFaceControl(role) {
  const select = SELECT_FOR[role]();
  const custom = CUSTOM_FOR[role]();
  if (!select) return;
  select.innerHTML = '';
  for (const [label, stack] of stacksFor(role)) {
    const opt = document.createElement('option');
    opt.value = stack;
    opt.textContent = label;
    select.appendChild(opt);
  }
  const customOpt = document.createElement('option');
  customOpt.value = CUSTOM;
  customOpt.textContent = 'A font I name…';
  select.appendChild(customOpt);
  const { option, custom: name } = currentChoice(role, readStored(KEY_FOR[role]));
  select.value = option;
  if (custom) {
    custom.hidden = option !== CUSTOM;
    custom.value = name;
  }
}

function fillSizeControl(select, key) {
  if (!select) return;
  select.innerHTML = '';
  for (const [label, scale] of SIZE_OPTIONS) {
    const opt = document.createElement('option');
    opt.value = String(scale);
    opt.textContent = label;
    select.appendChild(opt);
  }
  select.value = String(readScale(readStored(key)));
}

export function renderTypography() {
  for (const role of ['doc', 'ui', 'mono']) fillFaceControl(role);
  fillSizeControl(typoSizeSelect, SCALE_KEY);
  for (const group of Object.keys(GROUP_KEY_FOR)) {
    fillSizeControl(GROUP_SELECT_FOR[group](), GROUP_KEY_FOR[group]);
  }
}

export function wireTypography() {
  renderTypography();
  for (const role of ['doc', 'ui', 'mono']) {
    const select = SELECT_FOR[role]();
    const custom = CUSTOM_FOR[role]();
    if (select) {
      select.addEventListener('change', () => {
        if (select.value === CUSTOM) {
          if (custom) {
            custom.hidden = false;
            custom.focus();
          }
          return;
        }
        if (custom) custom.hidden = true;
        setFace(role, select.value);
      });
    }
    if (custom) {
      custom.addEventListener('change', () => {
        const v = faceValue(custom.value, role);
        setFace(role, v);
      });
    }
  }
  if (typoSizeSelect) {
    typoSizeSelect.addEventListener('change', () => {
      setTextScale(parseFloat(typoSizeSelect.value));
      typoSizeSelect.value = String(readScale(readStored(SCALE_KEY)));
    });
  }
  for (const group of Object.keys(GROUP_KEY_FOR)) {
    const select = GROUP_SELECT_FOR[group]();
    if (select) {
      select.addEventListener('change', () => {
        setGroupScale(group, parseFloat(select.value));
        select.value = String(readScale(readStored(GROUP_KEY_FOR[group])));
      });
    }
  }
}

// The stored choice lands before the first paint beside the head
// script's own read: this covers everything the inline script does not
// (and is what the test harness sees, since it never runs inline tags).
applyTypography();
