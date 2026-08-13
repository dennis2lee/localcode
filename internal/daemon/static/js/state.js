// Two state objects with explicit ownership, instead of one flat bag of
// module-level `let`s: `app` is app-scoped and outlives a session switch
// (the agent catalog, the session list, MCP servers, settings read once at
// startup); `session` is per-conversation and gets wiped by resetSession on
// every switch. Both are exported as `const` and mutated in place — nothing
// here ever reassigns the exported binding, only its properties, so every
// importer's reference stays valid across a reset.

export const app = {
  agents: [],           // [{name, description, model}]
  customCommands: [],   // [{name, description}]
  sessions: [],          // cached list rendered in the aside
  mcpServers: [],
  workspacePath: '',
  canBrowseWorkspace: false, // true only in the desktop-window mode
  canRevealWorkspace: false, // likewise: a file-manager window needs a screen
  // Sessions that finished a turn while you were somewhere else, and so
  // are holding an answer nobody has looked at yet. Cleared by opening
  // the session. Not persisted: "unread" means since this page loaded,
  // which is the only span in which it can be true of *you*.
  unreadSessions: new Set(),
  autoCompactEnabled: true,
  showTPS: true,
  autoDelegate: false,
  autoDelegateAgent: '',      // '' when config.json has no auto_delegate block
  autoDelegateMatch: [],      // glob patterns that qualify a prompt for delegation
  skipPermissions: false,
  permissionRules: {},        // tool -> [{match, decision}]
  canEditPermissions: false,  // false when the daemon has no config.json path to persist to
};

// freshSessionState is everything that belongs to one conversation and MUST
// reset on session switch — the object being the reset unit is the point: a
// new field added here is automatically cleared by resetSession, instead of
// depending on someone remembering to add a line to a manual per-field reset.
export function freshSessionState(id) {
  return {
    sessionID: id,
    currentAgent: null,
    // connected tracks the SSE stream to the daemon, which is this client's
    // only channel to the model: while it's down, nothing typed here can
    // reach one, so it's what the status light reports as "connected".
    connected: false,
    waiting: false,
    tasks: new Map(), // task_id -> {agent, status}
    lastUsage: null,   // {input_tokens, output_tokens, max_context, percent, tps, show_tps, model}
    runningTool: '',   // tool currently executing, shown in the status bar
    // tool_use_id -> the transcript row for that call, so tool.end can find
    // the row tool.start created and fill in its result.
    toolRows: new Map(),
    promptQueue: [],   // plain prompts submitted while a turn is in flight
    // Up/Down prompt recall, mirroring the TUI. Client-side and in-memory:
    // a typing convenience, not session state that outlives the tab.
    history: [],       // submitted prompts, oldest first
    historyIdx: 0,      // === history.length means "not navigating"
    historyDraft: '',   // text stashed when recall started
    pendingPermissionID: null,
    pendingPermissionCanAlways: false,
    // Model output streams as markdown, so it renders as one growing bubble
    // per model message rather than raw text nodes: currentModelEl is that
    // bubble, currentModelBuffer the raw markdown accumulated for it so far.
    currentModelEl: null,
    currentModelBuffer: '',
  };
}

export const session = freshSessionState(null);

// turnInFlight answers "is the model working on this conversation right
// now" from both things that know something about it.
//
// session.waiting is this client's own belief, set when it sends a prompt
// and cleared on turn.done. It can be wrong in the middle of a turn: a
// reload or a session switch starts it at false while a turn is still
// running, and any path that clears it early (an error the loop recovered
// from, an optimistic Esc) leaves it false with the model still going.
//
// The session listing's `busy` flag is the daemon's own answer, kept
// current by session.activity events — it is what the blinking dot in the
// session panel reads. Taking either as "working" is what stops the light
// under the prompt from sitting solid while the light in the panel, three
// inches away, blinks about the same turn.
export function currentSessionBusy() {
  const s = (app.sessions || []).find(x => x.id === session.sessionID);
  return !!(s && s.busy);
}

export function turnInFlight() {
  return session.waiting || currentSessionBusy();
}

export function resetSession(id) {
  Object.assign(session, freshSessionState(id));
}
