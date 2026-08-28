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
  skills: [],            // [{name, description}], for completing "/<skill name>"
  slashCommands: [],     // [{name, description}] the daemon answers itself
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
  smartAgent: false,
  smartAgentRoster: [],       // the specialist names the daemon build ships
  skipPermissions: false,
  keepGoing: true,       // the carry-on nudge for muse models
  autoCompactPercent: 50,
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
    // The array itself belongs to the session (see promptHistories) and is
    // reattached by resetSession, so switching away and back finds the same
    // prompts rather than an empty list.
    history: [],       // this session's prompts, oldest first
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

// Prompt recall is per conversation, and outlives a switch away from it.
//
// It used to live entirely inside the per-session state, which resetSession
// wipes — so opening another session and coming back left Up recalling
// nothing, and the prompts of the session you were in were gone for good.
// Recall is most useful exactly there: the last thing you asked in *this*
// project, after a detour through another one.
//
// Still in memory only. These are the prompts of this page's lifetime plus
// whatever the transcript replayed when the session opened (see
// recordHistoryEntry), which is what makes a reloaded session's history
// come back without anything being persisted client-side.
const promptHistories = new Map(); // sessionID -> [prompts, oldest first]

// historyLimit caps one session's recall list. The transcript tail is 400
// events, so a busy session can seed a few hundred entries; past this many
// the list has stopped being something anyone walks through with an arrow
// key and is just memory held by a page that never reloads.
export const historyLimit = 200;

export function historyFor(id) {
  if (!id) return [];
  let h = promptHistories.get(id);
  if (!h) {
    h = [];
    promptHistories.set(id, h);
  }
  return h;
}

export function forgetHistory(id) {
  promptHistories.delete(id);
}

export function resetSession(id) {
  Object.assign(session, freshSessionState(id));
  // The array is shared with promptHistories rather than copied: everything
  // appended while this session is open is still there when it is reopened.
  session.history = historyFor(id);
  session.historyIdx = session.history.length;
}
