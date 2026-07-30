'use strict';

// Self-tests for the minimal DOM. Without these, a bug in dom.js would show
// up as a silently passing app test — the harness asserting against its own
// mistake. Everything the real tests rely on is pinned here.

const test = require('node:test');
const assert = require('node:assert/strict');

const { Document, Element } = require('./dom');

const FIXTURE = `
<div id="box" class="a b" title="t"></div>
<input id="field" type="text" placeholder="type here">
<select id="picker"></select>
`;

test('getElementById returns the ids declared in the markup, null otherwise', () => {
  const doc = new Document(FIXTURE);
  assert.equal(doc.getElementById('box').tagName, 'DIV');
  assert.equal(doc.getElementById('field').placeholder, 'type here');
  assert.equal(doc.getElementById('nope'), null);
  assert.deepEqual([...doc.missingIDs], ['nope']);
});

test('tagName is uppercase, as in a real browser', () => {
  const doc = new Document('');
  assert.equal(doc.createElement('textarea').tagName, 'TEXTAREA');
});

test('textContent escapes on the way out, innerHTML does not', () => {
  const doc = new Document('');
  const el = doc.createElement('div');
  el.textContent = '<img src=x onerror=boom>';
  assert.equal(el.innerHTML, '&lt;img src=x onerror=boom&gt;');
  assert.equal(el.textContent, '<img src=x onerror=boom>');

  el.innerHTML = '<b>bold</b>';
  assert.equal(el.innerHTML, '<b>bold</b>');
});

test('innerHTML = "" clears the children', () => {
  const doc = new Document('');
  const el = doc.createElement('div');
  el.appendChild(doc.createElement('span'));
  el.innerHTML = '';
  assert.equal(el.childNodes.length, 0);
  assert.equal(el.innerHTML, '');
});

test('appendChild reparents, remove detaches', () => {
  const doc = new Document('');
  const a = doc.createElement('div');
  const b = doc.createElement('div');
  const child = doc.createElement('span');
  a.appendChild(child);
  b.appendChild(child);
  assert.equal(a.childNodes.length, 0);
  assert.equal(child.parentNode, b);
  child.remove();
  assert.equal(b.childNodes.length, 0);
});

test('serialization keeps class, title and style, and escapes attribute values', () => {
  const doc = new Document('');
  const el = doc.createElement('div');
  el.className = 'task';
  el.title = 'a "quoted" <title>';
  el.style.display = 'none';
  el.textContent = 'x';
  assert.equal(
    el.outerHTML,
    '<div title="a &quot;quoted&quot; &lt;title&gt;" class="task" style="display:none">x</div>',
  );
});

test('classList add/remove/toggle/contains', () => {
  const doc = new Document('');
  const el = doc.createElement('div');
  el.classList.add('one');
  el.classList.toggle('two');
  assert.ok(el.classList.contains('one') && el.classList.contains('two'));
  el.classList.toggle('two');
  assert.equal(el.classList.contains('two'), false);
  el.classList.toggle('three', false);
  assert.equal(el.classList.contains('three'), false);
  el.classList.toggle('three', true);
  assert.equal(el.className, 'one three');
  el.classList.remove('one');
  assert.equal(el.className, 'three');
});

test('select.options lists only child <option> elements', () => {
  const doc = new Document('');
  const sel = doc.createElement('select');
  const opt = doc.createElement('option');
  opt.value = 'a';
  sel.appendChild(opt);
  sel.appendChild(doc.createElement('div'));
  assert.deepEqual(sel.options.map((o) => o.value), ['a']);
});

test('insertAdjacentHTML appends raw html and rejects other positions', () => {
  const doc = new Document('');
  const el = doc.createElement('div');
  el.insertAdjacentHTML('beforeend', '<p>1</p>');
  el.insertAdjacentHTML('beforeend', '<p>2</p>');
  assert.equal(el.innerHTML, '<p>1</p><p>2</p>');
  assert.throws(() => el.insertAdjacentHTML('afterbegin', 'x'), /only 'beforeend'/);
});

test('fire runs listeners and reports preventDefault', () => {
  const doc = new Document('');
  const el = doc.createElement('button');
  let seen = 0;
  el.addEventListener('click', () => { seen++; });
  el.addEventListener('click', (e) => e.preventDefault());
  const ev = el.click();
  assert.equal(seen, 1);
  assert.equal(ev.defaultPrevented, true);
});

test('element instances are Elements, so children filters correctly', () => {
  const doc = new Document('');
  const el = doc.createElement('div');
  el.textContent = 'text';
  el.appendChild(doc.createElement('span'));
  assert.equal(el.children.length, 1);
  assert.ok(el.children[0] instanceof Element);
});
