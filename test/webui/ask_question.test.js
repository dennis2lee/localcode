'use strict';

// The question the model stops a turn to ask (ask_user) is set at the size
// of the answers it interrupts.
//
// It used to be an ordinary tool line, which inherits the interface size
// (12.5px), so the one line a blocked turn is waiting on was the smallest
// text on the page while the reply it leads to was 17px. It is still a
// tool line (muted, italic, pre-wrap, the same text) and only its size
// changed. The fake DOM has no layout, so the size is held to the
// stylesheet: the line carries a class, the class names a rule, and the
// rule uses the token the model's replies use.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { load } = require('./harness');

const SHEET = fs.readFileSync(path.join(__dirname, '..', '..', 'internal', 'daemon', 'static', 'style.css'), 'utf8');

const HINT = '  (reply in the box below, in your own words or with a number)';

function request(seq, id, question, options) {
  const ev = { type: 'input.request', data: { id, question, options } };
  if (seq !== undefined) ev.seq = seq;
  return ev;
}

// The transcript's lines that carry the question's marker, as elements, so
// a test reads the class the handler really produced.
function questionLines(app) {
  return Array.from(app.el('transcript').querySelectorAll('.msg-tool'))
    .filter((el) => el.textContent.startsWith('[the model is asking]'));
}

// The class the handler added to the tool line besides the one every tool
// line has. The stylesheet tests start from it rather than from a literal,
// so renaming it on one side only fails them.
function askClass(app) {
  const [line] = questionLines(app);
  assert.ok(line, 'no question line was drawn');
  const extra = line.className.split(/\s+/).filter((c) => c && c !== 'msg-tool');
  assert.equal(extra.length, 1, `the question line should carry one class besides msg-tool, got "${line.className}"`);
  return extra[0];
}

test('the question draws as one tool line carrying the enlarged class', async () => {
  const app = await load();
  app.sse.emit(request(1, 'q1', 'which one?', ['a', 'b']));
  await app.settle();

  const lines = questionLines(app);
  assert.equal(lines.length, 1, 'one question should draw one line');
  assert.ok(lines[0].classList.contains('msg-tool'), 'it is still a tool line, which is what makes it muted, italic and pre-wrap');
  assert.ok(lines[0].classList.contains(askClass(app)));
  assert.equal(
    lines[0].textContent,
    `[the model is asking] which one?\n  1. a\n  2. b\n${HINT}`,
    'the text of the question is unchanged',
  );
  assert.equal(app.internals.session.pendingAsk, 'q1', 'the question must still arm the box to take the answer');
});

test('a question replayed from the log draws the same way', async () => {
  // A reload or a reconnect: the stream opens mid-log, and the question
  // arrives as a persisted frame between a prompt and the answer to it.
  const app = await load();
  app.sse.emit({ seq: 41, type: 'message.user', data: { text: 'pick a colour' } });
  app.sse.emit(request(42, 'q1', 'which one?', ['red', 'blue']));
  await app.settle();
  assert.equal(questionLines(app).length, 1);
  assert.equal(app.internals.session.pendingAsk, 'q1', 'a question with no answer yet in the log is still open');

  app.sse.emit({ seq: 43, type: 'input.resolved', data: { id: 'q1', answer: 'red' } });
  await app.settle();
  assert.equal(app.internals.session.pendingAsk, null, 'the answer in the log closes the question');
  const lines = questionLines(app);
  assert.equal(lines.length, 1, 'answering must not remove or redraw the question');
  assert.ok(lines[0].classList.contains(askClass(app)));
});

test('the class is on the line when it is inserted, not added afterwards', async () => {
  // The follower decides whether the reader is at the bottom before the
  // line goes in and scrolls after. A class added once appendChild has
  // returned would make the line grow after that scroll, and a reader
  // following the bottom would lose the last options below the fold.
  // This one pins the order directly; the two tests on following, below,
  // pin what the order is for.
  const app = await load();
  const transcript = app.el('transcript');
  const inserted = [];
  const appendChild = transcript.appendChild.bind(transcript);
  transcript.appendChild = (node) => {
    inserted.push({ text: node.textContent, className: node.className });
    return appendChild(node);
  };

  app.sse.emit(request(1, 'q1', 'which one?', ['a']));
  await app.settle();

  const [seen] = inserted.filter((n) => n.text.startsWith('[the model is asking]'));
  assert.ok(seen, 'the question line was never inserted into the transcript');
  assert.ok(seen.className.split(/\s+/).includes(askClass(app)), `inserted as "${seen.className}"`);
});

test('the question and its options stay plain text', async () => {
  const app = await load();
  app.sse.emit(request(1, 'q1', '<b>x</b><img src=x onerror=1>', ['<script>alert(1)</script>', 'a & b']));
  await app.settle();

  const [line] = questionLines(app);
  assert.ok(line, 'the question was not drawn');
  assert.equal(line.children.length, 0, 'markup in a question must not become elements');
  assert.ok(line.textContent.includes('<b>x</b><img src=x onerror=1>'));
  assert.ok(line.textContent.includes('  1. <script>alert(1)</script>'));
  assert.ok(line.textContent.includes('  2. a & b'));
  // As the browser would serialize it: the markup is escaped text, not tags.
  assert.ok(line.outerHTML.includes('&lt;b&gt;x&lt;/b&gt;'), `serialized as ${line.outerHTML}`);
  assert.ok(!line.outerHTML.includes('<img') && !line.outerHTML.includes('<script'), `serialized as ${line.outerHTML}`);
});

test('a question without options still draws with its hint', async () => {
  const app = await load();
  app.sse.emit(request(1, 'q1', 'what now?', undefined));
  app.sse.emit(request(2, 'q2', 'and then?', 'not a list'));
  await app.settle();

  const lines = questionLines(app);
  assert.equal(lines.length, 2);
  assert.equal(lines[0].textContent, `[the model is asking] what now?\n${HINT}`);
  assert.equal(lines[1].textContent, `[the model is asking] and then?\n${HINT}`);
  for (const l of lines) assert.ok(l.classList.contains(askClass(app)));
});

test('other tool lines keep their size', async () => {
  const app = await load();
  app.sse.emit(request(1, 'q1', 'which one?', ['a']));
  app.sse.emit({ seq: 2, type: 'input.resolved', data: { id: 'q1', answer: 'a' } });
  app.sse.emit({ seq: 3, type: 'plan.updated', data: { plan: [{ step: 'one', status: 'pending' }] } });
  app.sse.emit({ seq: 4, type: 'message.part.end', data: { text: ' M main.go', shell_command: 'git status' } });
  await app.settle();

  const cls = askClass(app);
  const tools = Array.from(app.el('transcript').querySelectorAll('.msg-tool'));
  const others = tools.filter((el) => !el.textContent.startsWith('[the model is asking]'));
  const texts = others.map((el) => el.textContent);
  assert.ok(texts.some((t) => t.startsWith('[answered] a')), 'the answer echo was not drawn');
  assert.ok(texts.some((t) => t.includes('$ git status')), 'the command output was not drawn');
  assert.ok(others.length >= 3, `expected the echo, the plan and the command output, got ${JSON.stringify(texts)}`);
  for (const el of others) {
    assert.ok(!el.classList.contains(cls), `"${el.textContent.slice(0, 30)}" must not be enlarged`);
  }
});

test('the next message answers the question and is not sent as a prompt', async () => {
  const app = await load({ routes: { 'POST /api/sessions/sess-1/input/q1': { status: 204 } } });
  app.sse.emit(request(1, 'q1', 'which one?', ['a', 'b']));
  await app.settle();

  app.el('input').value = 'neither, do X';
  app.el('send').click();
  await app.settle();

  const answers = app.callsTo('POST', '/api/sessions/sess-1/input/q1');
  assert.equal(answers.length, 1, 'the answer should go to the question\'s endpoint');
  assert.deepEqual(answers[0].body, { answer: 'neither, do X' });
  assert.equal(app.callsTo('POST', /\/messages$/).length, 0, 'the answer must not also go as a prompt');
  assert.equal(app.internals.session.pendingAsk, null);
});


// The size itself. The page cannot be measured here, so the stylesheet is
// read: whatever class the handler gave the line must have a rule, that
// rule must give it the model's reply size by the same token, and no other

// The size itself.

// Every rule of the stylesheet as [selector, body], at-rule wrappers
// ignored. Crude on purpose: this stylesheet has no nesting, and the checks
// below only need to know which selectors declare a font-size.
function rules() {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(SHEET.replace(/\/\*[\s\S]*?\*\//g, ''))) !== null) {
    out.push([m[1].trim().replace(/\s+/g, ' '), m[2]]);
  }
  return out;
}

const sizeOf = (body) => (/(?:^|;)\s*font-size\s*:\s*([^;]+)/.exec(body) || [])[1]?.trim();

test('the question has the size of the model\'s replies, by the same token', async () => {
  const app = await load();
  app.sse.emit(request(1, 'q1', 'which one?', ['a']));
  await app.settle();
  const cls = askClass(app);

  const own = rules().find(([sel]) => sel === `#transcript .${cls}`);
  assert.ok(own, `no rule for #transcript .${cls}`);
  const reply = rules().find(([sel, body]) => sel.split(',').some((x) => x.trim() === '#transcript .msg-model') && sizeOf(body));
  assert.ok(reply, 'the rule that sizes the replies was not found');
  assert.equal(sizeOf(own[1]), sizeOf(reply[1]), 'the question and the replies must use the same size');
  assert.match(sizeOf(own[1]), /^var\(--t-read\)$/, 'the size must be the reading token, not a literal');
});

test('the reading token follows the Reading trim, so the text-size setting moves the question', () => {
  const def = /--t-read:\s*calc\(([^;]+)\);/.exec(SHEET);
  assert.ok(def, '--t-read is not defined');
  assert.ok(def[1].includes('var(--t-scale-read, 1)'), `--t-read should follow the Reading trim: ${def[1]}`);
  assert.ok(!def[1].includes('--t-scale-ui'), `--t-read must not follow the Interface trim: ${def[1]}`);
});

test('only the question\'s own rule sizes the question, and a plain tool line keeps its size', async () => {
  const app = await load();
  app.sse.emit(request(1, 'q1', 'which one?', ['a']));
  await app.settle();
  const cls = askClass(app);

  const sizing = rules().filter(([sel, body]) => sizeOf(body) && sel.split(',').some((s) => /\.msg-tool\b/.test(s) || s.includes(`.${cls}`)));
  assert.deepEqual(sizing.map(([sel]) => sel), [`#transcript .${cls}`],
    'another rule gives a tool line or the question a size, which would change it or undo the question\'s');
});
