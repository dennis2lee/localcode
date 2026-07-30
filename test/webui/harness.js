'use strict';

// Loads the real internal/daemon/static/{index.html,app.js} into the minimal
// DOM from dom.js and hands the test back the script's internals.
//
// Nothing here is a copy of production code: index.html is scanned for its
// element ids and app.js is evaluated verbatim in a fresh vm context, so a
// test failure means the shipped files are wrong, not that a fixture drifted.
//
// The daemon is replaced by a routing table (see DEFAULT_ROUTES) and the SSE
// stream by a FakeEventSource a test drives directly with .emit(event) — the
// same JSON shape internal/events puts on the wire.

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const { Document } = require('./dom');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');

// What a freshly started daemon with one existing session answers. Any test
// can replace or add to these through load({routes: {...}}).
function defaultRoutes() {
  return {
    'GET /api/agents': [
      { name: 'general-purpose', description: 'the default agent', model: 'test-model-1' },
      { name: 'plan', description: 'read-only planner', model: 'test-model-2' },
    ],
    'GET /api/commands': [],
    'GET /api/settings': {
      auto_compact_enabled: true,
      show_tps: true,
      auto_delegate: false,
      auto_delegate_agent: '',
      auto_delegate_match: [],
      skip_permissions: false,
      permission_rules: {},
      can_edit_permissions: true,
    },
    'GET /api/workspace': { path: '/tmp/workspace', can_browse: false },
    'GET /api/mcp-servers': [],
    'GET /api/sessions': [
      {
        id: 'sess-1',
        title: 'first session',
        agent: 'general-purpose',
        workspace: '/tmp/workspace',
        created_at: '2026-01-02T03:04:05Z',
      },
    ],
    'GET /api/version': { version: 'test' },
    'POST /api/sessions': { id: 'sess-new', agent: 'general-purpose', workspace: '/tmp/workspace' },
    'POST /api/sessions/*/messages': { status: 202 },
    'POST /api/sessions/*/cancel': { status: 202 },
    'POST /api/sessions/*/agent': { status: 204 },
    'POST /api/sessions/*/permissions/*': { status: 204 },
    'POST /api/settings/auto-delegate': { status: 204 },
    'POST /api/settings/permissions': { status: 204 },
    'POST /api/workspace': { path: '/tmp/workspace' },
  };
}

// A route value is either a plain JSON body (answered 200), or a descriptor
// {status, body, error} for the cases a test needs to force — a 409 busy, a
// 500, a network failure.
function toResponse(value) {
  const desc = value && typeof value === 'object' && !Array.isArray(value) && 'status' in value
    ? value
    : { status: 200, body: value };
  const status = desc.status ?? 200;
  const hasBody = desc.body !== undefined;
  const text = hasBody ? JSON.stringify(desc.body) : '';
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: (h) => (h.toLowerCase() === 'content-type' && hasBody ? 'application/json' : '') },
    async text() { return text; },
    async json() { return JSON.parse(text); },
  };
}

function matchRoute(routes, method, urlPath) {
  const exact = `${method} ${urlPath}`;
  if (Object.prototype.hasOwnProperty.call(routes, exact)) return routes[exact];
  for (const key of Object.keys(routes)) {
    if (!key.includes('*')) continue;
    const [m, pattern] = [key.slice(0, key.indexOf(' ')), key.slice(key.indexOf(' ') + 1)];
    if (m !== method) continue;
    const re = new RegExp('^' + pattern.split('*').map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('[^/]+') + '$');
    if (re.test(urlPath)) return routes[key];
  }
  return undefined;
}

class FakeEventSource {
  constructor(url, harness) {
    this.url = url;
    this.closed = false;
    this.onopen = null;
    this.onmessage = null;
    this.onerror = null;
    harness.sse = this;
    // A real EventSource connects asynchronously; opening here would mean
    // onopen ran before app.js had a chance to assign it.
    queueMicrotask(() => {
      if (!this.closed && this.onopen) this.onopen();
    });
  }
  // emit delivers one server event, exactly as the daemon frames it.
  emit(event) {
    if (this.onmessage) this.onmessage({ data: JSON.stringify(event) });
  }
  // raw delivers an unparseable frame, to prove a malformed event can't take
  // the page down.
  raw(data) {
    if (this.onmessage) this.onmessage({ data });
  }
  fail() {
    if (this.onerror) this.onerror(new Error('stream failed'));
  }
  close() {
    this.closed = true;
  }
}

class FakeFormData {
  constructor() {
    this.entries = [];
  }
  append(name, value, filename) {
    this.entries.push({ name, value, filename });
  }
}

// settle lets every already-resolved promise chain run to completion. The
// harness never uses real timers, so a bounded number of microtask turns is
// enough for any chain app.js starts; the loop is generous rather than exact
// because init() awaits six requests in sequence.
async function settle(turns = 50) {
  for (let i = 0; i < turns; i++) await new Promise((r) => setImmediate(r));
}

/**
 * load evaluates the shipped Web UI and returns a handle on it.
 *
 * @param {object} [opts]
 * @param {object} [opts.routes]  route overrides, merged over defaultRoutes()
 * @param {boolean} [opts.confirm] what window.confirm returns (default true)
 * @param {string|null} [opts.prompt] what window.prompt returns (default null)
 * @param {boolean} [opts.init] run init() to completion before returning
 *                              (default true)
 */
async function load(opts = {}) {
  const html = fs.readFileSync(path.join(STATIC_DIR, 'index.html'), 'utf8');
  const source = fs.readFileSync(path.join(STATIC_DIR, 'app.js'), 'utf8');

  const document = new Document(html);
  const routes = { ...defaultRoutes(), ...(opts.routes || {}) };

  const harness = {
    document,
    routes,
    calls: [],
    consoleErrors: [],
    sse: null,
    settle,
  };

  const fetchImpl = async (url, init = {}) => {
    const method = (init.method || 'GET').toUpperCase();
    const urlPath = String(url).split('?')[0];
    const body = typeof init.body === 'string' ? JSON.parse(init.body) : init.body;
    harness.calls.push({ method, path: urlPath, body });
    const route = matchRoute(routes, method, urlPath);
    if (route === undefined) {
      throw new Error(`harness: no route for ${method} ${urlPath} (add one via load({routes}))`);
    }
    const value = typeof route === 'function' ? await route(body, { method, path: urlPath }) : route;
    if (value && value.error) throw new Error(value.error);
    return toResponse(value);
  };

  const sandbox = {
    document,
    fetch: fetchImpl,
    EventSource: class extends FakeEventSource {
      constructor(url) {
        super(url, harness);
      }
    },
    FormData: FakeFormData,
    console: {
      log() {},
      warn() {},
      error(...args) {
        harness.consoleErrors.push(args.map(String).join(' '));
      },
    },
    setTimeout,
    clearTimeout,
    queueMicrotask,
  };
  vm.createContext(sandbox);
  // app.js reaches for window.prompt / window.confirm; in a browser window
  // *is* the global, so wire it up the same way here.
  sandbox.window = sandbox;
  sandbox.confirm = () => (opts.confirm === undefined ? true : opts.confirm);
  sandbox.prompt = () => (opts.prompt === undefined ? null : opts.prompt);

  let internals = null;
  sandbox.__localcodeTestHook = (api) => {
    internals = api;
  };

  vm.runInContext(source, sandbox, { filename: 'app.js' });

  if (!internals) {
    throw new Error('app.js did not call __localcodeTestHook — is the test seam at the end of the file still there?');
  }
  if (document.missingIDs.size > 0) {
    throw new Error(
      `app.js asked for element ids that index.html does not define: ${[...document.missingIDs].join(', ')}`,
    );
  }

  if (opts.init !== false) {
    await internals.ready;
    await settle();
  }

  return Object.assign(harness, internals, {
    // el(id) is the element index.html declares — the same object app.js
    // holds a reference to.
    el: (id) => document.getElementById(id),
    // transcript() is everything appendLine has written, as the HTML string a
    // browser would parse.
    transcript: () => document.getElementById('transcript').innerHTML,
    // callsTo lists the requests made to one endpoint pattern.
    callsTo: (method, pattern) =>
      harness.calls.filter(
        (c) => c.method === method && (pattern instanceof RegExp ? pattern.test(c.path) : c.path === pattern),
      ),
    // type() puts text in the prompt box the way a user would, caret at the
    // end, so Enter/history behave as they do on the page.
    type: (text) => {
      const input = document.getElementById('input');
      input.value = text;
      input.selectionStart = input.selectionEnd = text.length;
      return input;
    },
    // press() sends a keydown to the prompt box, then to the document-level
    // handler that also sees it in a browser.
    press: (key, props = {}) => {
      const input = document.getElementById('input');
      const ev = input.fire('keydown', { key, ...props });
      if (!ev.defaultPrevented) document.fire('keydown', { key, target: input, ...props });
      return ev;
    },
  });
}

module.exports = { load, settle, defaultRoutes };
