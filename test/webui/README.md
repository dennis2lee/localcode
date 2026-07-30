# Web UI tests

Unit tests for `internal/daemon/static/app.js`, the script the daemon serves to
the browser and to the native GUI window.

```bash
node --test test/webui/*.test.js
```

`go test ./internal/daemon/` runs the same suite (see `webui_test.go`), and
skips it with a note when `node` is not installed — Go tests never depend on a
JavaScript toolchain being present.

## Why it looks like this

There is no `package.json`, no `node_modules` and no bundler anywhere in this
repo, and the Web UI is deliberately a single plain `<script>` served off an
embedded filesystem so the binary stays self-contained and works offline. A
test harness for it should not be the thing that drags in the project's first
JavaScript dependency, so this one is written against Node's built-in test
runner and a hand-written DOM:

| file | what it is |
| --- | --- |
| `dom.js` | ~300 lines implementing exactly the DOM surface `app.js` touches |
| `dom.test.js` | self-tests for `dom.js`, so a harness bug can't make an app test pass |
| `harness.js` | loads the real `index.html` + `app.js`, fakes the daemon and the SSE stream |
| `*.test.js` | the tests themselves |

Nothing here copies production code. `harness.js` reads the shipped
`index.html` and `app.js` off disk and evaluates the script verbatim in a
`node:vm` context, so a failing test means the shipped files are wrong.

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

`load(opts)` returns the script's internals plus these helpers:

- `app.state.*` — module-level state, readable and writable, so a test can put
  the page into a condition without driving the whole UI to get there
- `app.el(id)` — an element from `index.html`
- `app.transcript()` — everything written to the transcript, as HTML
- `app.sse` — the fake event stream: `.emit(event)`, `.raw(text)`, `.fail()`
- `app.calls` / `app.callsTo(method, path)` — the requests the page made
- `app.type(text)` / `app.press(key, props)` — drive the prompt box
- `app.settle()` — let pending promise chains finish

`load({routes: {...}})` overrides the fake daemon per test. A route value is
either a JSON body (answered `200`) or `{status, body}`:

```js
await load({ routes: { 'POST /api/sessions/*/messages': { status: 409 } } });
```

## Things to know

- **`app.js` ends in a test seam.** The script is one IIFE, so its internals
  are otherwise unreachable. The last block calls
  `globalThis.__localcodeTestHook` when the harness has installed one; in a
  browser it is always undefined and the block does nothing. Delete it and
  every test here fails with a clear message.
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
  markup and `load()` fails with the name of the id `app.js` wanted.
