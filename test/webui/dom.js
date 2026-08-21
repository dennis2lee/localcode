'use strict';

// A minimal DOM, hand-written, with no dependencies.
//
// The Web UI is one plain <script> served straight off the daemon's embedded
// filesystem — no bundler, no package.json, no node_modules anywhere in this
// repo. Pulling in jsdom to test it would mean adding the project's first
// JavaScript dependency (and a lockfile, and a supply chain) for a 1400-line
// script. So this file implements exactly the DOM surface app.js touches and
// nothing else. If app.js starts using an API that isn't here, the test fails
// loudly with "not a function" rather than silently passing — that is the
// intended behaviour, and the fix is to implement the API here.
//
// Deliberate simplifications, all of which the tests are written to respect:
//
//   * There is no HTML parser. `innerHTML = '<b>hi</b>'` stores the string as
//     a single opaque raw node instead of building elements from it, and
//     reading `innerHTML` back serializes the children. Assertions about
//     innerHTML are therefore string assertions, which is what the real tests
//     want anyway (they check escaping, not tree shape).
//   * getElementById only sees elements declared in index.html, which is how
//     app.js uses it. Elements built by createElement are never looked up
//     by id.
//   * Layout is fake: scrollHeight/scrollTop are plain numbers that stay
//     whatever a test sets them to.

const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

function escapeText(s) {
  return String(s).replace(/[&<>]/g, (c) => ESCAPES[c]);
}

function escapeAttr(s) {
  return String(s).replace(/[&<>"]/g, (c) => ESCAPES[c]);
}

// Serialized as-is: what an innerHTML/insertAdjacentHTML assignment left
// behind. The page ships this string to a real browser verbatim, so tests
// asserting on it are asserting on exactly what a browser would parse.
class RawHTML {
  constructor(html) {
    this.html = String(html);
  }
  get text() {
    return this.html;
  }
  toHTML() {
    return this.html;
  }
}

class TextNode {
  constructor(text) {
    this.data = String(text);
  }
  get text() {
    return this.data;
  }
  toHTML() {
    return escapeText(this.data);
  }
}

class ClassList {
  constructor() {
    this._set = new Set();
  }
  add(...names) {
    for (const n of names) if (n) this._set.add(n);
  }
  remove(...names) {
    for (const n of names) this._set.delete(n);
  }
  contains(name) {
    return this._set.has(name);
  }
  toggle(name, force) {
    const on = force === undefined ? !this._set.has(name) : !!force;
    if (on) this._set.add(name);
    else this._set.delete(name);
    return on;
  }
  get value() {
    return [...this._set].join(' ');
  }
  set value(v) {
    this._set = new Set(String(v).split(/\s+/).filter(Boolean));
  }
  toString() {
    return this.value;
  }
}

// Attributes that round-trip into serialized HTML. Everything else stays a
// plain JS property on the element, invisible to the serializer — which is
// fine, because the assertions that matter are about escaping of text and
// about class/id/title/style.
const SERIALIZED_ATTRS = ['id', 'type', 'value', 'title', 'placeholder', 'href'];

const VOID_TAGS = new Set(['br', 'hr', 'img', 'input', 'link', 'meta']);

class Element {
  constructor(doc, tagName) {
    this.ownerDocument = doc;
    // Uppercase, like a real HTML element's tagName — app.js compares
    // against 'INPUT'/'TEXTAREA'/'SELECT' when deciding whether Tab should
    // cycle the agent or move focus.
    this.tagName = String(tagName).toUpperCase();
    this.childNodes = [];
    this.parentNode = null;
    this.classList = new ClassList();
    this.style = {};
    this.dataset = {};
    this.listeners = new Map();
    this.attributes = new Map();

    // Form/element properties app.js reads or writes directly.
    this.id = '';
    this.value = '';
    this.checked = false;
    this.disabled = false;
    // Present in the markup as a bare attribute, and set from script by
    // the stop button and the window title bar. Modelled because "is it
    // on screen" is exactly what those two are asserted on.
    this.hidden = false;
    this.title = '';
    this.placeholder = '';
    this.selectionStart = 0;
    this.selectionEnd = 0;
    // Layout, such as it is. A test sets these three to describe a view
    // that is scrolled somewhere, which is the only thing the code under
    // test asks the layout: am I at the bottom?
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.clientHeight = 0;
  }

  // Only the generic attribute bag. The handful of attributes the code
  // reaches for as properties (id, value, title, …) stay properties above
  // rather than being routed through here, because that is how the code
  // under test uses them and a second source of truth would only drift.
  setAttribute(name, value) {
    this.attributes.set(String(name), String(value));
  }
  getAttribute(name) {
    return this.attributes.has(String(name)) ? this.attributes.get(String(name)) : null;
  }
  removeAttribute(name) {
    this.attributes.delete(String(name));
  }

  get className() {
    return this.classList.value;
  }
  set className(v) {
    this.classList.value = v;
  }

  get children() {
    return this.childNodes.filter((n) => n instanceof Element);
  }

  // <select>.options — app.js spreads this to find whether an agent name is
  // already in the dropdown.
  get options() {
    return this.children.filter((el) => el.tagName === 'OPTION');
  }

  appendChild(node) {
    if (node.parentNode) node.parentNode.removeChild(node);
    node.parentNode = this;
    this.childNodes.push(node);
    return node;
  }

  removeChild(node) {
    const i = this.childNodes.indexOf(node);
    if (i >= 0) this.childNodes.splice(i, 1);
    node.parentNode = null;
    return node;
  }

  remove() {
    if (this.parentNode) this.parentNode.removeChild(this);
  }

  insertAdjacentHTML(position, html) {
    if (position !== 'beforeend') {
      throw new Error(`insertAdjacentHTML: only 'beforeend' is implemented, got '${position}'`);
    }
    this.appendChild(new RawHTML(html));
  }

  get innerHTML() {
    return this.childNodes.map((n) => n.toHTML()).join('');
  }
  set innerHTML(html) {
    for (const n of this.childNodes) n.parentNode = null;
    this.childNodes = [];
    if (String(html) !== '') this.appendChild(new RawHTML(html));
  }

  get textContent() {
    return this.childNodes.map((n) => (n instanceof Element ? n.textContent : n.text)).join('');
  }
  set textContent(v) {
    for (const n of this.childNodes) n.parentNode = null;
    this.childNodes = [];
    if (String(v) !== '') this.appendChild(new TextNode(v));
  }

  get outerHTML() {
    return this.toHTML();
  }

  toHTML() {
    const attrs = [];
    for (const name of SERIALIZED_ATTRS) {
      const v = this[name];
      if (v !== '' && v !== undefined && v !== null) attrs.push(`${name}="${escapeAttr(v)}"`);
    }
    if (this.className) attrs.push(`class="${escapeAttr(this.className)}"`);
    const style = Object.entries(this.style)
      .filter(([, v]) => v !== '' && v !== undefined && v !== null)
      .map(([k, v]) => `${k}:${v}`)
      .join(';');
    if (style) attrs.push(`style="${escapeAttr(style)}"`);
    if (this.disabled) attrs.push('disabled');
    if (this.checked) attrs.push('checked');
    const tag = this.tagName.toLowerCase();
    const open = `<${tag}${attrs.length ? ' ' + attrs.join(' ') : ''}>`;
    if (VOID_TAGS.has(tag)) return open;
    return `${open}${this.innerHTML}</${tag}>`;
  }

  // Layout is not simulated, so offsetWidth reports whatever width was
  // last written inline. That is exactly what the resize handles read back
  // between drags, and nothing else in the app asks.
  get offsetWidth() {
    return parseInt(this.style.width, 10) || 0;
  }

  addEventListener(type, fn) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(fn);
  }

  removeEventListener(type, fn) {
    const list = this.listeners.get(type);
    if (!list) return;
    const i = list.indexOf(fn);
    if (i >= 0) list.splice(i, 1);
  }

  // fire builds an event object and runs the handlers, returning it so a
  // test can check defaultPrevented. Handlers may be async; fire returns the
  // event synchronously, so a test that needs an async handler to finish
  // should await the harness's settle() afterwards.
  fire(type, props = {}) {
    const ev = {
      type,
      target: this,
      currentTarget: this,
      defaultPrevented: false,
      preventDefault() {
        this.defaultPrevented = true;
      },
      stopPropagation() {},
      ...props,
    };
    for (const fn of this.listeners.get(type) || []) fn.call(this, ev);
    return ev;
  }

  dispatchEvent(ev) {
    this.fire(ev.type, ev);
    return !ev.defaultPrevented;
  }

  click() {
    return this.fire('click');
  }

  focus() {
    this.ownerDocument.activeElement = this;
  }

  blur() {
    if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null;
  }

  setSelectionRange(start, end) {
    this.selectionStart = start;
    this.selectionEnd = end;
  }

  // Real inputs have this, and the workspace box uses it so the path
  // already in it can be replaced by typing.
  select() {
    this.setSelectionRange(0, String(this.value ?? '').length);
  }
}

// Extracts every element that carries an id from an HTML source string.
//
// This is not a parser: it scans start tags and keeps the ones with an id,
// with no nesting, which is all app.js needs (it reaches every element it
// touches through getElementById). The upside of scanning the real
// index.html rather than a fixture is that the harness enforces the
// index.html/app.js contract for free — delete an id from the markup and
// every test fails at load with "getElementById('...') returned null".
function elementsWithIDs(doc, html) {
  const byID = new Map();
  const tagRE = /<([a-zA-Z][a-zA-Z0-9-]*)\b([^>]*)>/g;
  let m;
  while ((m = tagRE.exec(html)) !== null) {
    const [, tagName, attrText] = m;
    const attrs = {};
    const attrRE = /([a-zA-Z_:][-a-zA-Z0-9_:.]*)(?:\s*=\s*"([^"]*)")?/g;
    let a;
    while ((a = attrRE.exec(attrText)) !== null) {
      if (!a[1]) continue;
      attrs[a[1].toLowerCase()] = a[2] === undefined ? '' : a[2];
    }
    if (!attrs.id) continue;
    const el = new Element(doc, tagName);
    el.id = attrs.id;
    if (attrs.class) el.className = attrs.class;
    if (attrs.title) el.title = attrs.title;
    if (attrs.placeholder) el.placeholder = attrs.placeholder;
    if (attrs.value) el.value = attrs.value;
    if (attrs.type) el.type = attrs.type;
    if ('hidden' in attrs) el.hidden = true;
    byID.set(attrs.id, el);
  }
  return byID;
}

class Document {
  constructor(html) {
    this.listeners = new Map();
    this.byID = elementsWithIDs(this, html);
    this.body = new Element(this, 'body');
    this.activeElement = null;
    // Every id app.js asked for, so a test can assert the markup and the
    // script still agree.
    this.requestedIDs = new Set();
    this.missingIDs = new Set();
  }

  getElementById(id) {
    this.requestedIDs.add(id);
    const el = this.byID.get(id);
    if (!el) this.missingIDs.add(id);
    return el || null;
  }

  createElement(tagName) {
    return new Element(this, tagName);
  }

  createTextNode(text) {
    return new TextNode(text);
  }

  addEventListener(type, fn) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(fn);
  }

  removeEventListener(type, fn) {
    const list = this.listeners.get(type);
    if (!list) return;
    const i = list.indexOf(fn);
    if (i >= 0) list.splice(i, 1);
  }

  // fire dispatches a document-level event (the app binds keydown here for
  // Esc/Tab, which work with focus anywhere on the page, and the
  // pointermove/pointerup of a panel drag).
  fire(type, props = {}) {
    const ev = {
      type,
      target: this.body,
      defaultPrevented: false,
      preventDefault() {
        this.defaultPrevented = true;
      },
      stopPropagation() {},
      ...props,
    };
    for (const fn of this.listeners.get(type) || []) fn.call(this, ev);
    return ev;
  }
}

module.exports = { Document, Element, TextNode, RawHTML, ClassList, escapeText, escapeAttr };
