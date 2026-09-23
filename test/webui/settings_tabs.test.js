'use strict';

// The settings window, in tabs.
//
// The modal groups its sections into one tab per subject. The guard that
// matters is reachability: a setting that moves into a tab nobody opens
// is worse than a long list, so every control in the modal must sit
// inside exactly one tab panel. Written against the shipped index.html
// source rather than a list of ids, so a setting added later without a
// tab fails by name instead of disappearing.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');
const CSS_PATH = path.join(STATIC_DIR, 'style.css');

function settingsMarkup() {
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  const start = html.indexOf('<div id="settings-modal">');
  assert.ok(start >= 0, 'index.html has no settings modal');
  const end = html.indexOf('<div id="effort-modal">', start);
  assert.ok(end > start, 'cannot find the end of the settings modal');
  return html.slice(start, end);
}

// panelsIn returns each tabpanel's id with its source span, so a
// control is located by position rather than by a hardcoded list.
function panelsIn(modal) {
  const panels = [];
  const open = /<section\b([^>]*)>/g;
  let m;
  while ((m = open.exec(modal)) !== null) {
    if (!/role="tabpanel"/.test(m[1])) continue;
    const id = /id="([^"]+)"/.exec(m[1]);
    const close = modal.indexOf('</section>', m.index);
    assert.ok(id && close > m.index, `tabpanel with no id or no close: ${m[0]}`);
    panels.push({ id: id[1], start: m.index, end: close });
  }
  return panels;
}

function controlsIn(modal) {
  const controls = [];
  const re = /<(input|select|textarea|button)\b([^>]*)>/g;
  let m;
  while ((m = re.exec(modal)) !== null) {
    const id = /id="([^"]+)"/.exec(m[2]);
    if (id) controls.push({ tag: m[1], id: id[1], at: m.index });
  }
  return controls;
}

test('every setting is reachable through exactly one tab', () => {
  const modal = settingsMarkup();
  const panels = panelsIn(modal);
  assert.ok(panels.length >= 2, `expected tab panels, found ${panels.length}`);
  // Chrome, not settings: the tab buttons themselves and the Close that
  // dismisses the window live outside every panel by design.
  const outside = new Set(['settings-close']);
  for (const m of modal.matchAll(/<button\b([^>]*)>/g)) {
    if (/role="tab"/.test(m[1])) {
      const id = /id="([^"]+)"/.exec(m[1]);
      if (id) outside.add(id[1]);
    }
  }
  const orphans = [];
  const doubled = [];
  for (const c of controlsIn(modal)) {
    if (outside.has(c.id)) continue;
    const inside = panels.filter((p) => c.at > p.start && c.at < p.end);
    if (inside.length === 0) orphans.push(c.id);
    if (inside.length > 1) doubled.push(c.id);
  }
  assert.deepEqual(orphans, [], `settings outside every tab panel (unreachable): ${orphans.join(', ')}`);
  assert.deepEqual(doubled, [], `settings in more than one tab panel: ${doubled.join(', ')}`);
});

test('every tab controls exactly one panel and every panel answers to one tab', () => {
  const modal = settingsMarkup();
  const panels = new Set(panelsIn(modal).map((p) => p.id));
  const tabs = [...modal.matchAll(/<button\b([^>]*)role="tab"([^>]*)>/g)].map((m) => m[1] + m[2]);
  assert.ok(tabs.length >= 2, 'expected tab buttons');
  for (const t of tabs) {
    const controls = /aria-controls="([^"]+)"/.exec(t);
    const labelled = /id="([^"]+)"/.exec(t);
    assert.ok(controls, `a tab names no panel: ${t}`);
    assert.ok(panels.has(controls[1]), `tab ${labelled && labelled[1]} controls ${controls[1]}, which is no panel`);
  }
  for (const p of panels) {
    const section = new RegExp(`<section\\b[^>]*id="${p}"[^>]*>`).exec(modal);
    assert.ok(section, `panel ${p} is not a section`);
    assert.ok(/aria-labelledby="[^"]+"/.test(section[0]), `panel ${p} is labelled by no tab`);
  }
});

test('switching tabs shows one panel and hides the others', async () => {
  const app = await load();
  app.el('settings-btn').click();
  await app.settle();

  const visible = () => ['agents', 'turns', 'muse', 'updates', 'typography']
    .filter((n) => !app.el(`settings-panel-${n}`).hidden);
  assert.deepEqual(visible(), ['agents'], 'the window does not open on the first tab');

  app.el('settings-tab-turns').click();
  assert.deepEqual(visible(), ['turns'], 'two panels visible, or none');
  assert.equal(app.el('settings-tab-turns').getAttribute('aria-selected'), 'true');
  assert.equal(app.el('settings-tab-agents').getAttribute('aria-selected'), 'false');

  app.el('settings-tab-typography').click();
  assert.deepEqual(visible(), ['typography']);
});

test('the choice of tab does not leak between openings', async () => {
  const app = await load();
  app.el('settings-btn').click();
  await app.settle();
  app.el('settings-tab-updates').click();
  assert.equal(app.el('settings-panel-updates').hidden, false);

  app.el('settings-close').click();
  app.el('settings-btn').click();
  await app.settle();

  const visible = () => ['agents', 'turns', 'muse', 'updates', 'typography']
    .filter((n) => !app.el(`settings-panel-${n}`).hidden);
  assert.deepEqual(visible(), ['agents'], 'the window reopened where it was left');
});

test('arrow keys move between tabs and carry the focus', async () => {
  const app = await load();
  app.el('settings-btn').click();
  await app.settle();

  app.el('settings-tab-agents').focus();
  app.el('settings-tab-agents').fire('keydown', { key: 'ArrowRight' });
  assert.equal(app.document.activeElement, app.el('settings-tab-turns'), 'ArrowRight did not move focus');
  assert.equal(app.el('settings-panel-turns').hidden, false, 'the focused tab did not show');

  app.el('settings-tab-turns').fire('keydown', { key: 'ArrowLeft' });
  assert.equal(app.document.activeElement, app.el('settings-tab-agents'), 'ArrowLeft did not move back');

  // Wrapping: left of the first tab is the last one.
  app.el('settings-tab-agents').fire('keydown', { key: 'ArrowLeft' });
  assert.equal(app.document.activeElement, app.el('settings-tab-typography'), 'tabs do not wrap');

  app.el('settings-tab-typography').fire('keydown', { key: 'Home' });
  assert.equal(app.document.activeElement, app.el('settings-tab-agents'), 'Home did not jump to the first tab');
  app.el('settings-tab-agents').fire('keydown', { key: 'End' });
  assert.equal(app.document.activeElement, app.el('settings-tab-typography'), 'End did not jump to the last tab');
});

test('tabs show a visible focus ring', () => {
  const css = fs.readFileSync(CSS_PATH, 'utf8');
  const rule = /#settings-modal\s+\[role="tab"\]:focus-visible\s*\{[^}]*\}/.exec(css);
  assert.ok(rule, 'no :focus-visible rule for the settings tabs');
  assert.ok(/outline:\s*[^;]+;/.test(rule[0]), 'the focus ring sets no outline');
});

test('Escape closes the settings window', async () => {
  const app = await load();
  app.el('settings-btn').click();
  await app.settle();
  assert.equal(app.settings.isOpen, true);

  app.doc.fire('keydown', { key: 'Escape', target: app.el('settings-tab-agents') });
  assert.equal(app.settings.isOpen, false, 'Escape did not close the settings window');
});

test('Escape with the settings window open still cancels nothing by itself', async () => {
  const app = await load();
  app.el('settings-btn').click();
  await app.settle();

  app.doc.fire('keydown', { key: 'Escape', target: app.el('settings-tab-agents') });
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 0,
    'closing the settings window called the daemon');
});

// Escape puts the window away and leaves the work alone.
//
// The document-level Escape handler cancels the running turn, and it
// already stands down for a permission request. The settings window is
// the stronger case: somebody opens it mid-turn to look at a switch,
// changes nothing, and presses Escape to dismiss it. Cancelling the turn
// as well would throw away minutes of work for a keystroke that meant
// "close this".
//
// A turn has to actually be running for this to say anything: cancelTurn
// returns early when nothing is in flight, so the same key press on an
// idle session calls the daemon either way and proves nothing.
test('Escape closes the settings window and leaves a running turn alone', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'session.activity', data: { session: 'sess-1', busy: true } });
  await app.settle();

  app.el('settings-btn').click();
  await app.settle();
  assert.equal(app.settings.isOpen, true, 'the settings window did not open');

  app.doc.fire('keydown', { key: 'Escape', target: app.el('settings-tab-agents') });
  await app.settle();

  assert.equal(app.settings.isOpen, false, 'Escape left the settings window open');
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 0,
    'Escape cancelled the running turn while it was only asked to close the settings window');
});

// And with the window shut, Escape still means what it always meant.
test('Escape still cancels a running turn when the settings window is closed', async () => {
  const app = await load();
  app.sse.emit({ seq: 1, type: 'session.activity', data: { session: 'sess-1', busy: true } });
  await app.settle();

  app.doc.fire('keydown', { key: 'Escape', target: app.doc.body });
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 1,
    'Escape stopped cancelling the turn outside the settings window');
});
