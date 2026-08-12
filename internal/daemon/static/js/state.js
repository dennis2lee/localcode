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

export function resetSession(id) {
  Object.assign(session, freshSessionState(id));
}
