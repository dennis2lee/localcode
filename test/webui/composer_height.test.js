'use strict';

// The prompt box's height, and the one moment it can be got wrong for
// good.
//
// autoResizeInput writes the measured height inline and nothing measures
// again, so a page laid out in a window with no size — a hidden pane, a
// desktop window not on screen yet — wrote whatever scrollHeight
// returned in that layout and kept it. Measured in a real browser: 996px
// inline, drawn as a 240px box (the max-height) over a third of a small
// window, empty.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('a box with no width is not measured', async () => {
  const app = await load();
  const input = app.el('input');
  input.style.width = '';        // offsetWidth 0: the zero-size layout
  input.style.height = '34px';   // what a real layout had measured
  input.scrollHeight = 996;      // what a zero-size one reports
  app.autoResizeInput();
  assert.equal(input.style.height, '34px', 'a height measured with no width was written to the box');
});

test('a window that gains a size corrects a stale height', async () => {
  const app = await load();
  const input = app.el('input');
  input.style.height = '996px';  // the stale value, from before there was a window
  input.style.width = '600px';
  input.scrollHeight = 34;
  app.fireWindow('resize');
  assert.equal(input.style.height, '34px', 'the composer kept a height measured before the window had a size');
});

test('a box with a width is measured as before', async () => {
  const app = await load();
  const input = app.el('input');
  input.style.width = '600px';
  input.scrollHeight = 72;
  app.autoResizeInput();
  assert.equal(input.style.height, '72px');
});
