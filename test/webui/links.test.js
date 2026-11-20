'use strict';

// A click on a web link in the desktop window on macOS or Linux goes to
// the window's lcOpenExternal, which opens it in the system's default
// browser: WebKit drops a target="_blank" click there. Anywhere the window
// does not bind it (a browser, the Windows window), the link does what a
// link does.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

// A link as the fake DOM can stand one in: an element whose closest()
// finds it.
const linkTo = (href) => {
  const a = { getAttribute: (name) => (name === 'href' ? href : null) };
  return { closest: (sel) => (sel === 'a[href]' ? a : null) };
};
const click = (target, props = {}) => ({
  target, button: 0, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...props,
});

test('in a window that binds lcOpenExternal, a web link is opened by it', async () => {
  const opened = [];
  const app = await load({ globals: { lcGUI: true, lcOpenExternal: (u) => { opened.push(u); return Promise.resolve(); } } });
  const ev = click(linkTo('https://example.com/a?b=1'));
  assert.equal(app.internals.openExternally(ev), true);
  assert.equal(ev.defaultPrevented, true, 'the page also tried to follow it');
  await app.settle();
  assert.deepEqual(opened, ['https://example.com/a?b=1']);

  // A middle click asks for a new window too.
  app.internals.openExternally(click(linkTo('http://localhost:3000/'), { button: 1 }));
  await app.settle();
  assert.deepEqual(opened, ['https://example.com/a?b=1', 'http://localhost:3000/']);
});

test('the page\'s own links, and a right click, are left alone', async () => {
  const opened = [];
  const app = await load({ globals: { lcGUI: true, lcOpenExternal: (u) => { opened.push(u); } } });
  for (const ev of [
    click(linkTo('/api/sessions/s1/snapshots/a.png')),
    click(linkTo('mailto:a@example.com')),
    click({ closest: () => null }),
    click(linkTo('https://example.com/'), { button: 2 }),
  ]) {
    assert.equal(app.internals.openExternally(ev), false);
    assert.equal(ev.defaultPrevented, false);
  }
  await app.settle();
  assert.deepEqual(opened, []);
});

test('where nothing binds lcOpenExternal, a link does what a link does', async () => {
  const app = await load();
  const ev = click(linkTo('https://example.com/'));
  assert.equal(app.internals.openExternally(ev), false);
  assert.equal(ev.defaultPrevented, false);
});

test('a click anywhere on the page is looked at', async () => {
  const opened = [];
  const app = await load({ globals: { lcGUI: true, lcOpenExternal: (u) => { opened.push(u); } } });
  const ev = app.doc.fire('click', { target: linkTo('https://example.com/b'), button: 0 });
  assert.equal(ev.defaultPrevented, true);
  await app.settle();
  assert.deepEqual(opened, ['https://example.com/b']);
});
