# Web UI tests

Unit tests for `internal/daemon/static/js/*.js`, the ES modules the daemon
serves to the browser and to the native GUI window.

```bash
node --experimental-vm-modules --test test/webui/*.test.js
```

(or `make test-js`.) `go test ./internal/daemon/` runs the same suite (see
`webui_test.go`), and skips it with a note when `node` is not installed — Go
tests never depend on a JavaScript toolchain being present.

## Why it looks like this

There is no `package.json`, no `node_modules` and no bundler anywhere in this
repo, and the Web UI is deliberately native ES modules served off an embedded
filesystem (`//go:embed all:static` + `http.FileServerFS`) so the binary stays
self-contained and works offline. A test harness for it should not be the
thing that drags in the project's first JavaScript dependency, so this one is
written against Node's built-in test runner, a hand-written DOM, and Node's
`node:vm` module linker:

| file | what it is |
| --- | --- |
| `dom.js` | the DOM surface `js/*.js` touches, and nothing else |
| `dom.test.js` | self-tests for `dom.js`, so a harness bug can't make an app test pass |
| `harness.js` | links and evaluates the real `js/*.js` module graph, fakes the daemon and the SSE stream |
| `*.test.js` | the tests themselves |

Nothing here copies production code. `harness.js` reads every shipped
`internal/daemon/static/js/*.js` file off disk and evaluates it as a real ES
module (`vm.SourceTextModule`, with a small hand-written linker resolving each
file's relative imports to the others), so a failing test means the shipped
files are wrong.

`--experimental-vm-modules` is needed because `vm.SourceTextModule` is gated
behind it — every path that runs this suite (`make test-js`,
`go test ./internal/daemon/`, this README) passes it. It is not needed to run
the app itself; browsers execute the same files as native modules with no
flag at all.

## Writing a test

```js
const { load } = require('./harness');

test('a turn ends on turn.done', async () => {
  const app = await load();               // page loaded, session selected
  app.setWaiting(true);
  app.sse.emit({ type: 'turn.done' });    // one SSE frame, as the daemon sends it
  assert.equal(app.state.waiting, false);
});
```

`load(opts)` returns the module graph's exports (see the bottom of
`js/main.js` for the list) plus these helpers:

- `app.state.*` — a flat view over `js/state.js`'s `session`/`app` objects,
  readable and writable, so a test can put the page into a condition without
  driving the whole UI to get there
- `app.el(id)` — an element from `index.html`
- `app.doc` — the document itself, for the listeners the app attaches there
  rather than to an element (global keys, a panel drag's pointer events)
- `app.transcript()` — everything written to the transcript, as HTML
- `app.sse` — the fake event stream: `.emit(event)`, `.raw(text)`, `.fail()`
- `app.streamFor(fragment)` — one open stream by a piece of its URL, for a
  task window's own transcript
- `app.calls` / `app.callsTo(method, path)` — the requests the page made
  (`path` may be a string or a RegExp)
- `app.type(text)` / `app.press(key, props)` — drive the prompt box
- `app.micChunk(bytes)` — push one buffer of "audio" the way the capture
  worklet would, for the dictation tests
- `app.devicesChanged()` — fire the event a browser sends when a microphone
  is plugged in or removed
- `app.settle()` — let pending promise chains finish
- `app.wait(ms)` — let real time pass, for the few behaviours that are about
  a deadline rather than an event (dictation gives up on a slow upload)

`load(opts)` also takes:

| option | what it does |
| --- | --- |
| `routes` | overrides the fake daemon per test (see below) |
| `globals` | extra globals in the page's sandbox |
| `devices` | the microphone list `enumerateDevices` returns — an array, or a function called per query |
| `denyMicrophone` | make `getUserMedia` reject, the way a browser does when permission is refused |
| `localStorage` | the stored values the page starts with |
| `confirm` / `prompt` | answers for the browser dialogs the page opens |
| `init` | run before the modules are evaluated, for a page condition that has to exist at load time |

A route value is either a JSON body (answered `200`) or `{status, body}`, and
a route may be a function taking `{method, path, query}`:

```js
await load({ routes: { 'POST /api/sessions/*/messages': { status: 409 } } });
```

## Things to know

- **Every `load()` call gets a brand-new module graph.** `harness.js` builds
  the graph by hand (see `linkModuleGraph`) instead of using Node's own
  `import()`, specifically so nothing is cached between tests — two tests can
  never see each other's module-level state, the same guarantee the old
  single-file `vm.Script` harness gave before the module split.
- **The DOM has no HTML parser.** `innerHTML = '<b>x</b>'` stores the string
  as one opaque node; reading `innerHTML` serializes children back. Assertions
  about markup are string assertions — which is what tests about escaping
  want anyway.
- **Values crossing the vm boundary have different prototypes.** An array read
  out of `app.state` is not `instanceof Array` on this side, so use
  `assert.deepEqual(Array.from(app.state.promptQueue), [...])` rather than
  comparing it directly.
- **`getElementById` only sees ids declared in `index.html`.** That is what
  makes the harness enforce the markup/script contract: remove an id from the
  markup and `load()` fails with the name of the id that went missing.
- **Only relative imports.** `js/*.js` files may only `import` each other with
  `./` / `../` specifiers — see the "only relative imports" test in
  `startup.test.js`. A bare specifier (`import x from 'some-package'`) would
  need a real module resolver (npm layout, `node_modules`) that this harness
  deliberately does not have.
