'use strict';

// The SSE event handling and the prompt queue — the two places where the Web
// UI holds state that has to stay in step with the daemon's turn boundaries.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

test('the page connects its event stream to the selected session', async () => {
  const app = await load();
  // ?tail= so a long conversation opens at its end rather than being
  // rebuilt from the beginning. A reconnect ignores it in favour of
  // Last-Event-ID, so nothing is skipped after the first load.
  assert.match(app.sse.url, /^\/api\/sessions\/sess-1\/events\?tail=\d+$/);
  assert.equal(app.state.connected, true);
  assert.ok(app.el('comm-dot').classList.contains('connected'));
});

test('a dropped stream turns the light off without clearing the turn', async () => {
  const app = await load();
  app.state.waiting = true;
  app.sse.fail();
  assert.equal(app.state.connected, false);
  assert.equal(app.el('comm-dot').classList.contains('connected'), false);
  assert.equal(app.state.waiting, true);
});

test('a malformed frame is logged, not thrown', async () => {
  const app = await load();
  assert.doesNotThrow(() => app.sse.raw('{not json'));
  assert.equal(app.consoleErrors.length, 1);
  assert.match(app.consoleErrors[0], /bad event/);
});

test('a user message renders escaped', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.user', data: { text: '<b>hi</b>' } });
  assert.ok(app.transcript().includes('You: &lt;b&gt;hi&lt;/b&gt;'), app.transcript());
});

// keep_going: the message localcode sends a stalled model on the user's
// behalf is in the log so the model's history survives a restart, and it
// is announced by its own note. Painting it as a typed line would put
// words in the user's mouth — and it must not enter Up/Down recall.
test('a keep_going carry-on is not painted as something the user typed', async () => {
  const app = await load();
  app.sse.emit({ type: 'message.user', data: { text: 'Continue. You ended your turn early.', auto: true } });
  assert.ok(!app.transcript().includes('You:'), app.transcript());
});

// message.part.end means one model message ended, not the turn: a turn with
// tool calls streams several. Ending the wait here is what used to make a
// prompt typed during tool execution skip the queue and bounce off the
// daemon's busy flag with a 409.
test('message.part.end does not end the turn, turn.done does', async () => {
  const app = await load();
  app.setWaiting(true);
  app.applyEvent({ type: 'message.part.end' });
  assert.equal(app.state.waiting, true);
  app.applyEvent({ type: 'turn.done' });
  assert.equal(app.state.waiting, false);
});

test('a tool call gets a transcript line that is completed when it ends', async () => {
  const app = await load();
  app.setWaiting(true);
  app.applyEvent({
    type: 'tool.start',
    data: { tool_use_id: 'call_1', name: 'bash', input: JSON.stringify({ command: 'sleep 300' }) },
  });
  assert.equal(app.state.runningTool, 'bash');
  assert.match(app.el('status-text').textContent, /bash…/);
  // The line exists while the tool is still running — that is the whole
  // point of it. A long turn used to leave the transcript empty.
  let html = app.transcript();
  assert.ok(html.includes('sleep 300'), html);
  assert.ok(html.includes('running…'), html);

  app.applyEvent({
    type: 'tool.end',
    data: { tool_use_id: 'call_1', content: 'a\nb\nc', is_error: false },
  });
  assert.equal(app.state.runningTool, '');
  html = app.transcript();
  assert.ok(!html.includes('running…'), html);
  assert.ok(html.includes('3 lines'), html);
});

test('a tool line reports failure, and its output is escaped', async () => {
  const app = await load();
  app.applyEvent({
    type: 'tool.start',
    data: { tool_use_id: 'call_1', name: 'bash', input: JSON.stringify({ command: 'boom <script>' }) },
  });
  app.applyEvent({
    type: 'tool.end',
    data: { tool_use_id: 'call_1', content: 'no <b>such</b> file', is_error: true },
  });
  const html = app.transcript();
  assert.ok(html.includes('failed'), html);
  assert.ok(html.includes('boom &lt;script&gt;'), html);
  assert.ok(html.includes('no &lt;b&gt;such&lt;/b&gt; file'), html);
});

test('a tool.end for an unknown call is ignored rather than throwing', async () => {
  const app = await load();
  app.applyEvent({ type: 'tool.end', data: { tool_use_id: 'never-started', content: 'x' } });
  assert.equal(app.transcript(), '');
});

test('an error event ends the turn and shows the message escaped', async () => {
  const app = await load();
  app.setWaiting(true);
  app.applyEvent({ type: 'error', data: { error: 'boom <script>' } });
  assert.equal(app.state.waiting, false);
  assert.ok(app.transcript().includes('Error: boom &lt;script&gt;'), app.transcript());
});

test('a permission request locks the prompt box until it is resolved', async () => {
  const app = await load();
  app.applyEvent({
    type: 'permission.request',
    data: { id: 'perm-1', tool: 'bash', description: 'rm -rf /', can_always: true, rule: 'bash(rm *)' },
  });
  assert.ok(app.el('permission-modal').classList.contains('open'));
  assert.equal(app.el('input').disabled, true);
  assert.equal(app.el('send').disabled, true);
  assert.equal(app.el('permission-text').textContent, '[bash] rm -rf /');

  app.el('permission-allow').click();
  await app.settle();
  assert.deepEqual(app.callsTo('POST', '/api/sessions/sess-1/permissions/perm-1')[0].body, {
    allow: true,
    scope: 'once',
  });

  app.applyEvent({ type: 'permission.resolved', data: { id: 'perm-1' } });
  assert.equal(app.el('permission-modal').classList.contains('open'), false);
  assert.equal(app.el('input').disabled, false);
});

test('the always-allow button is hidden when the daemon cannot persist a rule', async () => {
  const app = await load();
  app.applyEvent({ type: 'permission.request', data: { id: 'p', tool: 'bash', can_always: false } });
  assert.equal(app.el('permission-allow-always').style.display, 'none');
  app.applyEvent({ type: 'permission.request', data: { id: 'p2', tool: 'bash', can_always: true, rule: 'bash(*)' } });
  assert.equal(app.el('permission-allow-always').style.display, '');
});

test('task events feed the sidebar and the status line', async () => {
  const app = await load();
  app.sse.emit({ type: 'task.spawned', data: { task_id: 'task-1', agent: 'plan' } });
  assert.match(app.el('tasks').innerHTML, /task-1/);
  assert.match(app.el('status-text').textContent, /1 background task/);

  app.sse.emit({ type: 'task.status', data: { task_id: 'task-1', status: 'done' } });
  assert.equal(app.state.tasks.get('task-1').status, 'done');
  assert.doesNotMatch(app.el('status-text').textContent, /background task/);
});

test('agent.switched updates the dropdown and the status line', async () => {
  const app = await load();
  app.sse.emit({ type: 'agent.switched', data: { agent: 'plan' } });
  assert.equal(app.state.currentAgent, 'plan');
  assert.equal(app.el('agent-select').value, 'plan');
  assert.match(app.el('status-text').textContent, /model: test-model-2/);
});

// A prompt typed during a turn goes to the daemon straight away. The
// daemon hands it to the running turn, which picks it up at its next tool
// call — so a correction reaches the model while it is still working
// instead of after it has finished the wrong thing. Holding it in the
// client until turn.done was the old behaviour.
test('a plain prompt typed mid-turn is sent immediately, not held back', async () => {
  const app = await load();
  app.setWaiting(true);

  app.type('second question');
  await app.el('send').click();
  await app.settle();

  const sent = app.callsTo('POST', '/api/sessions/sess-1/messages');
  assert.equal(sent.length, 1);
  assert.deepEqual(sent[0].body, { text: 'second question' });
  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.el('input').value, '');
  assert.ok(app.transcript().includes('second question'), app.transcript());
  // The original turn is still the one running.
  assert.equal(app.state.waiting, true);
});

// Queueing a command would mean replaying it later as literal chat text to
// the model instead of running it, so commands are not queued. (The Web UI
// drops them silently; the TUI answers with an explanation. Worth aligning,
// but that is a product change, not a harness one — this test pins today's
// behaviour so a change to it is deliberate.)
test('a command typed mid-turn is not queued as chat text', async () => {
  const app = await load();
  app.setWaiting(true);
  app.type('/help');
  await app.el('send').click();
  await app.settle();
  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 0);
});

test('a 409 from the daemon queues the prompt instead of reporting an error', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/messages': { status: 409, body: { error: 'busy' } } },
  });
  app.type('hello');
  await app.el('send').click();
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), ['hello']);
  // The prompt is on screen as the pending line drawn when Enter was
  // pressed — it is queued, not lost, and the status bar counts it. There
  // is no separate "[queued]" line any more: it said the same thing twice.
  assert.ok(app.transcript().includes('You: hello'), app.transcript());
  assert.ok(!app.transcript().includes('Error:'), app.transcript());
  assert.equal(app.state.waiting, true);
});

test('a real send failure ends the turn and is reported', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/messages': { status: 500, body: { error: 'nope' } } },
  });
  app.type('hello');
  await app.el('send').click();
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.state.waiting, false);
  assert.ok(app.transcript().includes('Error:'), app.transcript());
});

test('Esc cancels the running turn and drops the queue immediately', async () => {
  const app = await load();
  app.setWaiting(true);
  app.state.promptQueue = ['queued one'];

  app.press('Escape');
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 1);

  app.sse.emit({ type: 'turn.cancelled', data: {} });
  assert.equal(app.state.waiting, false);
  assert.ok(app.transcript().includes('[cancelled]'), app.transcript());
});

// Regression: the reply arrived, the turn was over, and the status bar
// still said "working… esc to cancel" with the light blinking — and Esc
// did nothing at all, because the only thing that cleared the spinner was
// an event the daemon had no reason to send again. Every prompt typed
// afterwards queued behind a turn that had already finished.
//
// The daemon answers "cancelled: false" when it has nothing running.
// That answer is the client's proof its own spinner is stale.
test('Esc clears a spinner the daemon says is not running', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/cancel': { cancelled: false } },
  });
  app.setWaiting(true);
  assert.equal(app.state.waiting, true);

  app.press('Escape');
  await app.settle();

  assert.equal(app.state.waiting, false);
  // No "[cancelled]" line: nothing was cancelled, the display was just
  // wrong. Saying otherwise would be inventing an event.
  assert.ok(!app.transcript().includes('[cancelled]'), app.transcript());
});

// ...and once it is cleared, typing goes straight out instead of queuing.
test('a prompt after that stale-spinner Esc is sent, not queued', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/cancel': { cancelled: false } },
  });
  app.setWaiting(true);
  app.press('Escape');
  await app.settle();

  app.type('hello');
  app.press('Enter');
  await app.settle();

  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 1);
});

// A cancel the daemon confirms stops this client waiting straight away,
// without needing the event back.
//
// Waiting for the echo is what made "stop" look broken: the daemon does
// cancel, but if this client's event stream has quietly died the
// turn.cancelled never lands, and the spinner sits over a turn that
// ended. The event is still what reports it in the transcript, and what
// tells any *other* attached client.
test('a confirmed cancel stops this client waiting without the event', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/cancel': { cancelled: true } },
  });
  app.setWaiting(true);
  app.press('Escape');
  await app.settle();

  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 1);
  assert.equal(app.state.waiting, false);
  assert.equal(app.el('stop-btn').hidden, true);

  // The event still arrives and still writes the transcript line.
  app.sse.emit({ type: 'turn.cancelled', data: {} });
  assert.ok(app.transcript().includes('[cancelled]'), app.transcript());
  assert.equal(app.state.waiting, false);
});

test('Esc while idle does not call the daemon', async () => {
  const app = await load();
  app.press('Escape');
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 0);
});

test('Enter sends, Shift+Enter does not', async () => {
  const app = await load();
  app.type('one');
  app.press('Enter');
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 1);

  app.type('two');
  app.press('Enter', { shiftKey: true });
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/messages').length, 1);
});

test('Up and Down walk prompt history and restore the draft', async () => {
  const app = await load();
  app.type('first');
  app.press('Enter');
  await app.settle();
  app.applyEvent({ type: 'turn.done' });

  app.type('second');
  app.press('Enter');
  await app.settle();
  app.applyEvent({ type: 'turn.done' });

  const input = app.type('a draft');
  input.selectionStart = input.selectionEnd = 0; // caret at the start: Up recalls

  app.press('ArrowUp');
  assert.equal(input.value, 'second');

  input.selectionStart = input.selectionEnd = 0;
  app.press('ArrowUp');
  assert.equal(input.value, 'first');

  app.press('ArrowDown');
  assert.equal(input.value, 'second');
  app.press('ArrowDown');
  assert.equal(input.value, 'a draft'); // the stashed draft comes back
});

// This was a known gap until v0.42.1. Recall parks the caret at the end of
// the text it inserts, and Up only recalled with the caret at offset 0 — so
// the second Up in a row did nothing and history was one entry deep unless
// the caret was moved back by hand between presses. A walk already under
// way now continues wherever the caret is, which is what the TUI has always
// done ("the cursor is on the first visual row", internal/tui/history.go
// atInputTop, stays true after its own CursorEnd).
test('Up keeps walking back without touching the caret', async () => {
  const app = await load();
  app.state.history = ['oldest', 'older', 'newer'];
  app.state.historyIdx = 3;

  const input = app.type('');
  input.selectionStart = input.selectionEnd = 0;
  app.press('ArrowUp');
  assert.equal(input.value, 'newer');

  app.press('ArrowUp');
  assert.equal(input.value, 'older', 'the caret sits at the end after recall; the walk must continue anyway');
  app.press('ArrowUp');
  assert.equal(input.value, 'oldest');
  app.press('ArrowUp');
  assert.equal(input.value, 'oldest', 'there is nothing older');

  app.press('ArrowDown');
  assert.equal(input.value, 'older', 'Down walks back the other way from mid-list too');
});

// Typing ends the walk: the recalled text has become a draft of its own,
// and the next Up starts again from the newest entry instead of walking
// over the top of an edit.
test('editing a recalled prompt ends the walk', async () => {
  const app = await load();
  app.state.history = ['older', 'newer'];
  app.state.historyIdx = 2;

  const input = app.type('');
  input.selectionStart = input.selectionEnd = 0;
  app.press('ArrowUp');
  assert.equal(input.value, 'newer');

  input.value = 'newer, with an edit';
  input.fire('input');
  input.selectionStart = input.selectionEnd = 0;
  app.press('ArrowUp');

  assert.equal(input.value, 'newer', 'the walk restarted from the newest entry, as it should');
});

test('history does not record the same prompt twice in a row', async () => {
  const app = await load();
  app.rememberPrompt('same');
  app.rememberPrompt('same');
  app.rememberPrompt('other');
  assert.deepEqual(Array.from(app.state.history), ['same', 'other']);
});

test('switching session clears the transcript and queue', async () => {
  const app = await load();
  app.state.promptQueue = ['pending'];
  app.rememberPrompt('typed here');
  app.sse.emit({ type: 'message.user', data: { text: 'hello' } });
  assert.notEqual(app.transcript(), '');

  app.selectSession('sess-2', 'plan', '/tmp/workspace');
  await app.settle();

  assert.equal(app.state.sessionID, 'sess-2');
  assert.equal(app.transcript(), '');
  assert.deepEqual(Array.from(app.state.promptQueue), []);
  assert.deepEqual(Array.from(app.state.history), [], 'a different conversation starts with its own empty recall list');
  assert.match(app.sse.url, /^\/api\/sessions\/sess-2\/events\?tail=\d+$/);
});

// Esc is the fast way to stop a turn, but it only works if the key
// reaches the page — which a host webview does not guarantee, and when it
// does not, a long turn has no visible way out at all. A button cannot be
// swallowed by a keyboard handler that never runs.
test('a stop button appears while a turn runs and cancels it', async () => {
  const app = await load({
    routes: { 'POST /api/sessions/*/cancel': { cancelled: true } },
  });
  assert.equal(app.el('stop-btn').hidden, true);

  app.setWaiting(true);
  assert.equal(app.el('stop-btn').hidden, false);

  app.el('stop-btn').click();
  await app.settle();
  assert.equal(app.callsTo('POST', '/api/sessions/sess-1/cancel').length, 1);

  app.sse.emit({ type: 'turn.cancelled', data: {} });
  assert.equal(app.state.waiting, false);
  assert.equal(app.el('stop-btn').hidden, true);
});

// The tool being waited on is named, so a long turn reads as work rather
// than a hang.
test('the status line names the running tool', async () => {
  const app = await load();
  app.setWaiting(true);
  app.applyEvent({ type: 'tool.start', data: { name: 'bash' } });
  assert.match(app.el('status-text').textContent, /bash…/);

  app.applyEvent({ type: 'tool.end', data: {} });
  assert.match(app.el('status-text').textContent, /working…/);
});

// The acknowledgement shown when a prompt is sent mid-turn stands in for
// the real transcript line until the model is actually handed the text —
// that wait can be minutes. When the real line arrives the placeholder
// goes, so the transcript holds one entry per message and matches what a
// reload would show.
test('the mid-turn acknowledgement is replaced by the real line, not left above it', async () => {
  const app = await load();
  app.setWaiting(true);

  app.type('actually, skip the tests');
  await app.el('send').click();
  await app.settle();
  assert.ok(app.transcript().includes('will pick this up'), app.transcript());

  app.applyEvent({ type: 'message.user', data: { text: 'actually, skip the tests', injected: true } });

  const html = app.transcript();
  assert.ok(!html.includes('will pick this up'), html);
  assert.ok(html.includes('You: actually, skip the tests'), html);
  // Exactly one occurrence of the text, not two.
  assert.equal(html.split('actually, skip the tests').length - 1, 1, html);
});

// The unlock used to live only in the permission.resolved handler, so if
// the event stream had quietly died the click did everything except let
// the user type: modal gone, turn proceeding server-side, composer stuck
// disabled under "Resolve the permission request above" with no request
// on screen to resolve.
test('answering a permission unlocks the composer without waiting for the event', async () => {
  const app = await load();
  app.applyEvent({
    type: 'permission.request',
    data: { id: 'perm-1', tool: 'bash', description: 'rm -rf /', can_always: false },
  });
  assert.equal(app.el('input').disabled, true);

  app.el('permission-allow').click();
  await app.settle();

  // No permission.resolved applied — that is the whole point.
  assert.equal(app.el('input').disabled, false, 'composer stayed locked with nothing on screen to unlock it');
  assert.equal(app.el('send').disabled, false);
});

// Cancelling stops a tool call where it is, and the daemon emits no
// tool.end for it — there is no result to report. The row was left
// spinning under the "[cancelled]" line for the life of the page.
test('cancelling a turn closes out the tool row that was still running', async () => {
  const app = await load();
  app.applyEvent({ type: 'tool.start', data: { tool_use_id: 't1', name: 'bash', input: '{"command":"sleep 100"}' } });
  assert.ok(app.el('transcript').innerHTML.includes('running'), app.el('transcript').innerHTML);

  app.applyEvent({ type: 'turn.cancelled', data: {} });
  assert.ok(!app.el('transcript').innerHTML.includes('running'), 'the row is still spinning after a cancel');
  assert.ok(app.el('transcript').innerHTML.includes('stopped'), app.el('transcript').innerHTML);
});

// A slash command typed while a turn is running used to do nothing at
// all: no request, nothing queued, no message, and the text still sitting
// in the box. Enter looked like a dead key.
//
// It cannot be handed to the running turn — the daemon passes mid-turn
// text straight to the model, so "/compact" would arrive as four words of
// chat — and queueing it is not safe either, since a second turn may have
// started by the time the queue drains. So it is refused out loud, which
// is what the TUI has always done.
test('a command typed during a turn says why it cannot run', async () => {
  const app = await load();
  app.state.waiting = true;
  app.type('/compact');
  app.press('Enter');
  await app.settle();

  assert.equal(app.callsTo('POST', /messages/).length, 0, 'the command was sent to the model as text');
  assert.match(app.transcript(), /can't run while a turn is in progress/);
  assert.match(app.transcript(), /Esc/);
});

// An ordinary prompt still goes to the running turn, which is the whole
// point of being able to type during one.
test('a plain prompt typed during a turn still reaches the running turn', async () => {
  const app = await load();
  app.state.waiting = true;
  app.type('actually, skip the tests');
  app.press('Enter');
  await app.settle();

  assert.equal(app.callsTo('POST', /messages/).length, 1);
  assert.equal(app.el('input').value, '');
});

// The context-window overflow is handled inside the agent loop: the
// history is summarized and the request retried, so the turn is still
// running and a reply is still coming. The notice travels as an error
// event carrying recovered:true, and treating it like any other error
// stopped the activity light and painted a red failure over a session
// that went on to answer — the "it keeps erroring" impression that the
// recovery exists to remove.
test('a recovered error is a note, not the end of the turn', async () => {
  const app = await load();
  app.state.waiting = true;

  app.applyEvent({ type: 'error', data: {
    error: "the conversation no longer fits in this model's context window; summarizing it and retrying",
    recovered: true,
  } });

  assert.equal(app.state.waiting, true, 'the turn is still running; the spinner should not have stopped');
  assert.match(app.transcript(), /summarizing it and retrying/);
  assert.ok(!app.el('transcript').innerHTML.includes('class="error"'),
    'a handled condition should not be painted as a failure: ' + app.el('transcript').innerHTML);
});

// An error that nobody handled still ends the turn and still shows red.
test('an unrecovered error still ends the turn', async () => {
  const app = await load();
  app.state.waiting = true;

  app.applyEvent({ type: 'error', data: { error: 'model endpoint refused the connection' } });

  assert.equal(app.state.waiting, false);
  assert.ok(app.el('transcript').innerHTML.includes('error'), app.el('transcript').innerHTML);
});
