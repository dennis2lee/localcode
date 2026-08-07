// The one HTTP layer. Every request to the daemon goes through api() or
// uploadFile() below, and every URL template lives in exactly one of the
// named wrappers in this file — nothing outside this module builds a
// `/api/...` string.

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

// isBusy identifies the one status the caller usually has to treat
// differently: the daemon already has a turn running for this session, so
// the request is queue material, not a failure to report.
export const isBusy = (err) => err instanceof ApiError && err.status === 409;

export async function api(method, path, body) {
  const resp = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!resp.ok) {
    const text = await resp.text().catch(() => '');
    throw new ApiError(resp.status, `${method} ${path}: ${resp.status} ${text}`);
  }
  if (resp.status === 204) return null;
  const ct = resp.headers.get('content-type') || '';
  return ct.includes('application/json') ? resp.json() : null;
}

// uploadFile posts one dropped file to the daemon and returns its absolute
// server-side path — the model reads it with its own file tools; there's no
// separate "attachment" wire concept. Kept out of api() proper: multipart
// bodies skip the JSON content-type/encoding that every other call needs.
export async function uploadFile(sessionID, file) {
  const form = new FormData();
  form.append('file', file, file.name);
  const resp = await fetch(`/api/sessions/${sessionID}/uploads`, { method: 'POST', body: form });
  if (!resp.ok) {
    const text = await resp.text().catch(() => '');
    throw new ApiError(resp.status, `${resp.status} ${text}`);
  }
  const data = await resp.json();
  return data.path;
}

export const getAgents = () => api('GET', '/api/agents');
export const getCommands = () => api('GET', '/api/commands');
export const getSettings = () => api('GET', '/api/settings');
export const getVersion = () => api('GET', '/api/version');
export const getWorkspace = () => api('GET', '/api/workspace');
// sessionID is what lets the daemon record the move on the session, so the
// session list keeps naming where the conversation is and re-selecting it
// later doesn't put the workspace back where the session was created.
export const setWorkspace = (path, sessionID) => api('POST', '/api/workspace', { path, session_id: sessionID });
export const browseWorkspace = (start) => api('POST', '/api/workspace/browse', { start });
export const getMCPServers = () => api('GET', '/api/mcp-servers');

export const getDictationStatus = () => api('GET', '/api/dictation');
// Returns the id itself, not the {id} envelope the daemon sends. The
// caller only ever puts this straight back into a URL, and handing it an
// object meant every later request went to /api/dictation/[object
// Object]/... — which the daemon answered with "no dictation session",
// so dictation never started once.
export const startDictation = async () => (await api('POST', '/api/dictation')).id;
export const stopDictation = (id) => api('POST', `/api/dictation/${id}/stop`);
// Raw PCM, not JSON: base64 would cost a third more bytes per chunk on a
// request that happens four times a second, for nothing.
export async function sendDictationAudio(id, buffer) {
  const resp = await fetch(`/api/dictation/${id}/audio`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/octet-stream' },
    body: buffer,
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new ApiError(resp.status, data.error || `POST /api/dictation/${id}/audio: ${resp.status}`);
  return data;
}

export const getSessions = () => api('GET', '/api/sessions');
export const createSession = (agent) => api('POST', '/api/sessions', { agent });
export const renameSession = (id, title) => api('POST', `/api/sessions/${id}/rename`, { title });
export const forkSession = (id) => api('POST', `/api/sessions/${id}/fork`);
export const deleteSession = (id) => api('DELETE', `/api/sessions/${id}`);
export const deleteAllSessions = () => api('DELETE', '/api/sessions');

export const switchAgent = (sessionID, agent) => api('POST', `/api/sessions/${sessionID}/agent`, { agent });
export const sendChatMessage = (sessionID, text) => api('POST', `/api/sessions/${sessionID}/messages`, { text });
export const cancelSessionTurn = (sessionID) => api('POST', `/api/sessions/${sessionID}/cancel`, {});
export const resolvePermissionRequest = (sessionID, id, allow, scope) =>
  api('POST', `/api/sessions/${sessionID}/permissions/${id}`, { allow, scope });

export const setAutoDelegate = (patch) => api('POST', '/api/settings/auto-delegate', patch);
export const setSkipPermissions = (enabled) => api('POST', '/api/permissions/skip', { enabled });
export const addPermissionRule = (tool, match, decision) =>
  api('POST', '/api/permissions/rules', { tool, match, decision });
export const removePermissionRule = (tool, match, decision) =>
  api('POST', '/api/permissions/rules/remove', { tool, match, decision });
