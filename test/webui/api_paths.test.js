'use strict';

// Every request the page makes goes through api.js, and a path segment a
// URL resolves away sends it to another route: DELETE
// /api/sessions/{id}/tasks/.. is DELETE /api/sessions/{id}, which deletes
// the conversation. api.js refuses such a path before anything is sent,
// whatever wrapper built it. (Found by review: the task routes were closed
// and the schedule routes were not.)
//
// api.js is loaded on its own, with fetch replaced, and every URL it would
// send is resolved the way a browser resolves it (WHATWG URL).

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const API = pathToFileURL(path.join(__dirname, '../../internal/daemon/static/js/api.js')).href;

async function load() {
  const sent = [];
  global.fetch = async (url, init = {}) => {
    sent.push({ method: init.method || 'GET', url: String(url) });
    return { ok: true, status: 204, headers: { get: () => '' }, text: async () => '', json: async () => ({}) };
  };
  return { api: await import(API), sent };
}

const resolved = (u) => { const r = new URL(u, 'http://127.0.0.1:4096'); return r.pathname + r.search; };

// One call per wrapper that puts an id in a path, with the id where the
// wrapper puts it.
const calls = (api, id) => ({
  renameSession: () => api.renameSession(id, 't'),
  forkSession: () => api.forkSession(id),
  deleteSession: () => api.deleteSession(id),
  setSessionGroup: () => api.setSessionGroup(id, 'g'),
  switchAgent: () => api.switchAgent(id, 'plan'),
  sendChatMessage: () => api.sendChatMessage(id, 'hi'),
  cancelTask: () => api.cancelTask(id),
  deleteTask_task: () => api.deleteTask('S', id),
  deleteTask_session: () => api.deleteTask(id, 'T'),
  deleteAllTasks: () => api.deleteAllTasks(id),
  cancelSessionTurn: () => api.cancelSessionTurn(id),
  detachSessionChild: () => api.detachSessionChild(id),
  resolvePermissionRequest: () => api.resolvePermissionRequest('S', id, true, 'once'),
  answerQuestion: () => api.answerQuestion('S', id, 'a'),
  archiveSession: () => api.archiveSession(id),
  retrieveSession: () => api.retrieveSession(id),
  getEffort: () => api.getEffort(id),
  setEffort: () => api.setEffort(id, 'high'),
  listSchedules: () => api.listSchedules(id),
  markScheduleSeen: () => api.markScheduleSeen('S', id),
  renameSchedule: () => api.renameSchedule('S', id, 'n'),
  deleteSchedule: () => api.deleteSchedule('S', id),
  deleteSchedule_session: () => api.deleteSchedule(id, 'X'),
  uploadFile: () => api.uploadFile(id, new Blob(['x']), 'f.txt'),
});

test('an ordinary id gives a URL the browser sends as written', async () => {
  const { api, sent } = await load();
  for (const [name, call] of Object.entries(calls(api, 'task-1788019200000000000-1'))) {
    sent.length = 0;
    await call();
    assert.equal(sent.length, 1, `${name} sent nothing`);
    assert.equal(resolved(sent[0].url), sent[0].url, `${name}: ${sent[0].url}`);
  }
});

// Refused, or encoded into a segment that stays one: either way nothing
// reaches a route other than the one the wrapper names.
test('an id a URL would resolve away never reaches another route', async () => {
  const { api, sent } = await load();
  for (const id of ['.', '..', '%2e', '%2E%2e', '.%2e', '', 'a\\..']) {
    for (const [name, call] of Object.entries(calls(api, id))) {
      sent.length = 0;
      let refused = false;
      try { await call(); } catch (err) { refused = err.status === 400; }
      if (refused) {
        assert.equal(sent.length, 0, `${name}(${JSON.stringify(id)}) was refused and sent anyway`);
        continue;
      }
      assert.equal(sent.length, 1, `${name}(${JSON.stringify(id)}) neither sent nor refused`);
      assert.equal(resolved(sent[0].url), sent[0].url, `${name}(${JSON.stringify(id)}) sent ${sent[0].url}`);
    }
  }
});

test('the ids that become dot segments are refused, not sent', async () => {
  const { api, sent } = await load();
  for (const id of ['.', '..', '%2e%2e', '']) {
    sent.length = 0;
    await assert.rejects(api.deleteSchedule('S', id), (err) => err.status === 400, JSON.stringify(id));
    await assert.rejects(api.deleteTask('S', id === '%2e%2e' ? '..' : id), (err) => err.status === 400, JSON.stringify(id));
    assert.equal(sent.length, 0);
  }
});
