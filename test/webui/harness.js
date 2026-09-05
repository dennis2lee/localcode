'use strict';

// Loads the real internal/daemon/static/{index.html,js/*.js} — the daemon's
// embedded Web UI, native ES modules and no bundler, exactly as a browser
// gets them — into the minimal DOM from dom.js and hands the test back the
// module graph's exports.
//
// Nothing here is a copy of production code: index.html is scanned for its
// element ids and every js/*.js file is evaluated verbatim as a real ES
// module (via node:vm's SourceTextModule, run with a hand-written linker —
// see linkModuleGraph below), so a test failure means the shipped files are
// wrong, not that a fixture drifted.
//
// The daemon is replaced by a routing table (see defaultRoutes) and the SSE
// stream by a FakeEventSource a test drives directly with .emit(event) — the
// same JSON shape internal/events puts on the wire.
//
// Running this file requires Node's --experimental-vm-modules flag (see
// package.json's test-js script / Makefile / webui_test.go — every path that
// runs this suite passes it). Without the flag, vm.SourceTextModule throws
// immediately; load() surfaces that as a clear error rather than a cryptic one.

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const { Document } = require('./dom');

const STATIC_DIR = path.join(__dirname, '..', '..', 'internal', 'daemon', 'static');
const JS_DIR = path.join(STATIC_DIR, 'js');
const ENTRY = path.join(JS_DIR, 'main.js');

if (typeof vm.SourceTextModule !== 'function') {
  throw new Error(
    'test/webui/harness.js needs node run with --experimental-vm-modules ' +
    '(it evaluates the real ES modules under internal/daemon/static/js/ directly). ' +
    'Use `make test-js`, or `go test ./internal/daemon/`.',
  );
}

// What a freshly started daemon with one existing session answers. Any test
// can replace or add to these through load({routes: {...}}).
function defaultRoutes() {
  return {
    'GET /api/agents': [
      { name: 'general-purpose', description: 'the default agent', model: 'test-model-1' },
      { name: 'plan', description: 'read-only planner', model: 'test-model-2' },
    ],
    'GET /api/commands': [],
    'GET /api/skills': [],
    'GET /api/slash-commands': [],
    'GET /api/settings': {
      auto_compact_enabled: true,
      show_tps: true,
      auto_delegate: false,
      auto_delegate_agent: '',
      auto_delegate_match: [],
      smart_agent: false,
      smart_agent_roster: ['explore', 'implement', 'librarian', 'oracle', 'plan', 'verify'],
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
// {status, body} for the cases a test needs to force — a 409 busy, a 500.
// {networkError: "..."} makes the fetch itself reject, which is a different
// thing from a reply that reports a failure: the daemon's own replies carry
// an "error" field of their own, and a bare `error` used to be read as this
// — so a route returning what the daemon really sends never arrived.
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
    // 0 CONNECTING, 1 OPEN, 2 CLOSED — the same three the real one has.
    // The page reads it to tell a drop the browser will retry from a
    // reply that failed the connection, which it never retries.
    this.readyState = 0;
    this.onopen = null;
    this.onmessage = null;
    this.onerror = null;
    // Every stream the page opens, in the order it opened them. There is
    // more than one now: the conversation's, and one per background-task
    // window. `harness.sse` used to be simply "the last one created",
    // which meant opening a task window silently re-pointed every test's
    // .emit() at the task's stream — events aimed at the conversation
    // went somewhere that ignores them.
    harness.streams.push(this);
    // A real EventSource connects asynchronously; opening here would mean
    // onopen ran before app code had a chance to assign it.
    queueMicrotask(() => {
      if (this.closed) return;
      this.readyState = 1;
      if (this.onopen) this.onopen();
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
  // fail is a transport drop: the browser reopens this one itself, so the
  // stream stays CONNECTING and the page is expected to wait.
  fail() {
    this.readyState = 0;
    if (this.onerror) this.onerror(new Error('stream failed'));
  }
  // failFatally is the other kind: a reply that was not 200 text/event-stream.
  // The spec says that fails the connection — CLOSED, and never retried —
  // which is what a 404 for a missing session or a 502 from a window whose
  // successor has gone actually produces.
  failFatally() {
    this.readyState = 2;
    if (this.onerror) this.onerror(new Error('stream failed fatally'));
  }
  // reopen is the other half of fail(): a real EventSource reconnects on
  // its own after an error and announces it, and the page has work to do
  // at that moment — a stream that has been away is a gap in what this
  // client knows. Without this the fake could only ever go down.
  reopen() {
    if (this.closed) return;
    this.readyState = 1;
    if (this.onopen) this.onopen();
  }
  close() {
    this.closed = true;
    this.readyState = 2;
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
// enough for any chain the app starts; the loop is generous rather than
// exact because init() awaits six requests in sequence.
async function settle(turns = 50) {
  for (let i = 0; i < turns; i++) await new Promise((r) => setImmediate(r));
}

// linkModuleGraph creates one fresh vm.SourceTextModule per source file
// reachable from entryPath and links their imports together, all inside
// `context`. Building the graph by hand — instead of Node's own ESM loader —
// is what gives each load() call full isolation for free: every call gets
// brand new Module instances with no shared cache, so two tests can never
// see each other's module-level state, the same guarantee vm.Script gave the
// single-file harness this replaced. A relative specifier is resolved
// against the importing module's own file, matching how the browser
// resolves `import './x.js'` in these files.
async function linkModuleGraph(entryPath, context) {
  const cache = new Map(); // absolute path -> vm.SourceTextModule

  function load(filePath) {
    if (cache.has(filePath)) return cache.get(filePath);
    const source = fs.readFileSync(filePath, 'utf8');
    const mod = new vm.SourceTextModule(source, {
      identifier: filePath,
      context,
      importModuleDynamically: async (specifier) => load(resolve(specifier, filePath)),
    });
    cache.set(filePath, mod);
    return mod;
  }

  function resolve(specifier, fromPath) {
    if (!specifier.startsWith('.')) {
      throw new Error(`unsupported bare import specifier "${specifier}" in ${fromPath} — the Web UI only uses relative imports`);
    }
    return path.normalize(path.join(path.dirname(fromPath), specifier));
  }

  const entry = load(entryPath);
  async function linker(specifier, referencingModule) {
    return load(resolve(specifier, referencingModule.identifier));
  }
  await entry.link(linker);
  await entry.evaluate();
  return entry;
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

  const document = new Document(html);
  const routes = { ...defaultRoutes(), ...(opts.routes || {}) };

  const harness = {
    document,
    routes,
    calls: [],
    consoleErrors: [],
    sse: null,
    // How many times the page asked to be reloaded.
    reloads: 0,
    // Every SSE stream the page opens, in the order it opened them: the
    // conversation's, and one per background-task window.
    streams: [],
    settle,
  };

  const fetchImpl = async (url, init = {}) => {
    const method = (init.method || 'GET').toUpperCase();
    const [urlPath, search = ''] = String(url).split('?');
    const body = typeof init.body === 'string' ? JSON.parse(init.body) : init.body;
    // The query goes to the route as well as into the call log. Some of
    // what the daemon answers depends on it — /api/workspace is per
    // session, and the session it is asked about rides in ?session= — and
    // a route that cannot see it can only answer one thing for every
    // session, which is precisely the confusion under test.
    const query = new URLSearchParams(search);
    harness.calls.push({ method, path: urlPath, body, query });
    const route = matchRoute(routes, method, urlPath);
    if (route === undefined) {
      throw new Error(`harness: no route for ${method} ${urlPath} (add one via load({routes}))`);
    }
    const value = typeof route === 'function' ? await route(body, { method, path: urlPath, query }) : route;
    if (value && value.networkError) throw new Error(value.networkError);
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
    URLSearchParams,
    queueMicrotask,
    AbortController,
    // Counted rather than performed: the page reloads itself when the
    // daemon under it is replaced, and a test has to be able to see that
    // without a browser to navigate.
    location: {
      href: 'http://127.0.0.1:4096/',
      reload() { harness.reloads += 1; },
    },
  };
  // Globals the page finds in a particular host rather than in a browser:
  // the desktop window binds lcWindowCommand, and the page draws its own
  // title bar only when it is there.
  Object.assign(sandbox, opts.globals || {});
  const context = vm.createContext(sandbox);
  // app code reaches for window.prompt / window.confirm; in a browser
  // window *is* the global, so wire it up the same way here.
  sandbox.window = sandbox;
  // A minimal localStorage: the resize handles persist panel widths
  // through it, and the app treats a throwing/absent store as "no saved
  // width" rather than an error, so the stub only has to be honest.
  const stored = new Map(Object.entries(opts.localStorage || {}));
  sandbox.localStorage = {
    getItem: (k) => (stored.has(k) ? stored.get(k) : null),
    setItem: (k, v) => stored.set(k, String(v)),
    removeItem: (k) => stored.delete(k),
  };
  harness.storage = stored;
  // sessionStorage is its own store, per window: which conversation this
  // one is looking at is kept there so two windows do not fight over one
  // key. The stub mirrors localStorage's shape.
  const sessionStored = new Map(Object.entries(opts.sessionStorage || {}));
  sandbox.sessionStorage = {
    getItem: (k) => (sessionStored.has(k) ? sessionStored.get(k) : null),
    setItem: (k, v) => sessionStored.set(k, String(v)),
    removeItem: (k) => sessionStored.delete(k),
  };
  harness.sessionStorage = sessionStored;
  sandbox.confirm = () => (opts.confirm === undefined ? true : opts.confirm);
  sandbox.prompt = () => (opts.prompt === undefined ? null : opts.prompt);

  const mainModule = await linkModuleGraph(ENTRY, context);
  const internals = mainModule.namespace;

  if (document.missingIDs.size > 0) {
    throw new Error(
      `js/*.js asked for element ids that index.html does not define: ${[...document.missingIDs].join(', ')}`,
    );
  }

  if (opts.init !== false) {
    await internals.ready;
    await settle();
  }

  // Defined rather than assigned, because it has to be evaluated at the
  // moment a test reads it: Object.assign would call the getter once, here,
  // and store whichever stream was current then.
  //
  // "The stream carrying the conversation being looked at" — not whichever
  // stream was opened most recently, which became a different thing as soon
  // as a background-task window could be open at the same time.
  Object.defineProperty(harness, 'sse', {
    configurable: true,
    get() {
      const id = internals.session.sessionID;
      for (let i = harness.streams.length - 1; i >= 0; i--) {
        const s = harness.streams[i];
        if (!s.closed && s.url.includes(`/api/sessions/${id}/`)) return s;
      }
      return harness.streams[harness.streams.length - 1];
    },
  });

  return Object.assign(harness, internals, {
    // streamFor finds a stream by a fragment of its URL — a task window's,
    // for instance.
    streamFor: (fragment) => harness.streams.find((s) => !s.closed && s.url.includes(fragment)),
    // doc is the document itself, for the handful of listeners the app
    // attaches there rather than to an element (global keys, and the
    // pointermove/pointerup of a panel drag).
    doc: document,
    // internals is the module namespace, for the few tests that call an
    // exported function directly rather than through the DOM.
    internals,
    sessionStorage: sessionStored,
    // el(id) is the element index.html declares — the same object the app
    // code holds a reference to.
    el: (id) => document.getElementById(id),
    // transcript() is everything the transcript module has written, as the
    // HTML string a browser would parse.
    transcript: () => document.getElementById('transcript').innerHTML,
    // userTurns() is the text of each of the reader's own prompts, in
    // order. It exists because the prompts stopped carrying a "You: "
    // prefix that a substring search could anchor on — and it is the
    // better assertion anyway: searching the whole transcript HTML for
    // "hello" also matches a model reply that quoted it back.
    userTurns: () =>
      Array.from(document.getElementById('transcript').querySelectorAll('.msg-user'), (el) => el.textContent),
    // wait lets real time pass, for the handful of behaviours that are
    // about a deadline rather than about an event.
    wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
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
    // state bridges the test-facing flat shape (state.sessionID, state.waiting,
    // ...) onto the module's real session/app-scoped objects (see
    // internal/daemon/static/js/state.js) — the split exists in the shipped
    // code, not in what tests have to know about.
    state: {
      get sessionID() { return internals.session.sessionID; }, set sessionID(v) { internals.session.sessionID = v; },
      get waiting() { return internals.session.waiting; }, set waiting(v) { internals.session.waiting = v; },
      get connected() { return internals.session.connected; },
      get runningTool() { return internals.session.runningTool; }, set runningTool(v) { internals.session.runningTool = v; },
      get promptQueue() { return internals.session.promptQueue; }, set promptQueue(v) { internals.session.promptQueue = v; },
      get history() { return internals.session.history; }, set history(v) { internals.session.history = v; },
      get historyIdx() { return internals.session.historyIdx; }, set historyIdx(v) { internals.session.historyIdx = v; },
      get tasks() { return internals.session.tasks; }, set tasks(v) { internals.session.tasks = v; },
      get agents() { return internals.app.agents; }, set agents(v) { internals.app.agents = v; },
      get currentAgent() { return internals.session.currentAgent; },
      get customCommands() { return internals.app.customCommands; }, set customCommands(v) { internals.app.customCommands = v; },
      get sessions() { return internals.app.sessions; }, set sessions(v) { internals.app.sessions = v; },
      get zoom() { return internals.app.zoom; },
      get mcpServers() { return internals.app.mcpServers; }, set mcpServers(v) { internals.app.mcpServers = v; },
      get lastUsage() { return internals.session.lastUsage; }, set lastUsage(v) { internals.session.lastUsage = v; },
      get skipPermissions() { return internals.app.skipPermissions; }, set skipPermissions(v) { internals.app.skipPermissions = v; },
      get smartAgent() { return internals.app.smartAgent; }, set smartAgent(v) { internals.app.smartAgent = v; },
      get keepGoing() { return internals.app.keepGoing; }, set keepGoing(v) { internals.app.keepGoing = v; },
      get permissionRules() { return internals.app.permissionRules; }, set permissionRules(v) { internals.app.permissionRules = v; },
      get sessionPermissions() { return internals.app.sessionPermissions; }, set sessionPermissions(v) { internals.app.sessionPermissions = v; },
      get rememberedOutside() { return internals.app.rememberedOutside; }, set rememberedOutside(v) { internals.app.rememberedOutside = v; },
      get autoDelegate() { return internals.app.autoDelegate; }, set autoDelegate(v) { internals.app.autoDelegate = v; },
      get autoDelegateAgent() { return internals.app.autoDelegateAgent; }, set autoDelegateAgent(v) { internals.app.autoDelegateAgent = v; },
      get autoDelegateMatch() { return internals.app.autoDelegateMatch; }, set autoDelegateMatch(v) { internals.app.autoDelegateMatch = v; },
      get showTPS() { return internals.app.showTPS; }, set showTPS(v) { internals.app.showTPS = v; },
      get pendingPermissionID() { return internals.session.pendingPermissionID; }, set pendingPermissionID(v) { internals.session.pendingPermissionID = v; },
      get workspacePath() { return internals.app.workspacePath; }, set workspacePath(v) { internals.app.workspacePath = v; },
    },
  });
}

module.exports = { load, settle, defaultRoutes };
