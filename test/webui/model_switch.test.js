'use strict';

// After an agent switch the status line must name the model the next turn
// will actually run on: the new agent's own choice if this conversation
// made one for that agent, otherwise what the new agent's profile resolves
// to. Never the previous agent's.
//
// The server keeps one model choice per agent; the page used to cache the
// choice as a bare string that outranked the usage model and was never
// cleared on a switch, so a conversation that had ever used /model kept
// showing the agent just left.

const test = require('node:test');
const assert = require('node:assert/strict');

const { load } = require('./harness');

function statusText(app) {
  return app.el('status-text').textContent;
}

// The conversation chose old-choice under general-purpose, then switched
// to plan, which has no choice of its own: the line must fall back to
// plan's profile model rather than naming the agent just left.
test('switching agents drops the previous agent\u2019s chosen model from the status line', async () => {
  const app = await load();
  app.sse.emit({ type: 'model.changed', data: { agent: 'general-purpose', model: 'old-choice', source: 'conversation' } });
  app.sse.emit({ type: 'usage', data: { model: 'old-choice', percent: 10 } });
  await app.wait(0);
  assert.match(statusText(app), /old-choice/, 'setup: the chosen model should show before the switch');

  app.sse.emit({ type: 'agent.switched', data: { agent: 'plan' } });
  await app.wait(0);
  const text = statusText(app);
  assert.match(text, /agent: plan/, 'the line should name the new agent');
  assert.match(text, /model: test-model-2/, 'the line should name the new agent\u2019s profile model');
  assert.ok(!text.includes('old-choice'), `the line still names the agent just left: ${text}`);
});

// The higher-precedence value is the one that was forgotten before: the
// usage model was already cleared on a switch while the conversation's own
// choice was not. A future handler that clears only the usage model again
// must fail this test, so it sets no usage model at all.
test('the rendered model follows the current agent even when only the choice is stale', async () => {
  const app = await load();
  app.sse.emit({ type: 'model.changed', data: { agent: 'general-purpose', model: 'old-choice', source: 'conversation' } });
  await app.wait(0);
  assert.match(statusText(app), /old-choice/);

  app.sse.emit({ type: 'agent.switched', data: { agent: 'plan' } });
  await app.wait(0);
  const text = statusText(app);
  assert.match(text, /model: test-model-2/);
  assert.ok(!text.includes('old-choice'), `the stale choice survived the switch: ${text}`);
});

// The new agent has its own chosen model: the daemon announces it right
// after the switch, and the line must name it rather than the profile
// default or the previous agent's choice.
test('a choice the new agent made itself is named after the switch', async () => {
  const app = await load();
  app.sse.emit({ type: 'model.changed', data: { agent: 'general-purpose', model: 'old-choice', source: 'conversation' } });
  await app.wait(0);

  app.sse.emit({ type: 'agent.switched', data: { agent: 'plan' } });
  app.sse.emit({ type: 'model.changed', data: { agent: 'plan', model: 'new-choice', source: 'conversation' } });
  await app.wait(0);
  const text = statusText(app);
  assert.match(text, /agent: plan/);
  assert.match(text, /model: new-choice/);
  assert.ok(!text.includes('old-choice'), `the line still names the agent just left: ${text}`);
});

// The renderer guard itself: a cached choice that names another agent is
// stale no matter how it got there, so the line must not trust it even if
// the switch handler ever forgets to drop it. Pokes the cached state
// directly rather than going through the switch event.
test('a cached choice for another agent is not rendered', async () => {
  const app = await load();
  app.sse.emit({ type: 'agent.switched', data: { agent: 'plan' } });
  await app.wait(0);
  app.internals.session.chosenModel = 'old-choice';
  app.internals.session.chosenModelAgent = 'general-purpose';
  app.renderStatusBar();
  const text = statusText(app);
  assert.match(text, /model: test-model-2/);
  assert.ok(!text.includes('old-choice'), `the stale choice was rendered: ${text}`);
});

// Back on the agent's own model, the line reads the new agent's profile:
// going back to the default under one agent must not pin the other
// agent's choice.
test('returning to the agent\u2019s own model reads the current agent\u2019s profile', async () => {
  const app = await load();
  app.sse.emit({ type: 'model.changed', data: { agent: 'general-purpose', model: 'old-choice', source: 'conversation' } });
  await app.wait(0);

  app.sse.emit({ type: 'agent.switched', data: { agent: 'plan' } });
  app.sse.emit({ type: 'model.changed', data: { agent: 'plan', model: '', source: 'agent' } });
  await app.wait(0);
  const text = statusText(app);
  assert.match(text, /model: test-model-2/);
  assert.ok(!text.includes('old-choice'), `the line still names the agent just left: ${text}`);
});
