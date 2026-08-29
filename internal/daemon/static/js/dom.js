// Every element id index.html declares that some other module needs. This is
// the one file allowed to call document.getElementById — everyone else
// imports the reference from here, so an id that index.html drops surfaces as
// an import returning undefined at a call site instead of a silent re-query
// scattered through a dozen files.
export const transcriptEl = document.getElementById('transcript');
export const jumpBottomBtn = document.getElementById('jump-bottom');
export const tasksEl = document.getElementById('tasks');
export const sessionIdEl = document.getElementById('session-id');
export const inputEl = document.getElementById('input');
export const sendBtn = document.getElementById('send');
export const modalEl = document.getElementById('permission-modal');
export const permissionTextEl = document.getElementById('permission-text');
export const permissionAllowBtn = document.getElementById('permission-allow');
export const permissionAllowSessionBtn = document.getElementById('permission-allow-session');
export const permissionAllowAlwaysBtn = document.getElementById('permission-allow-always');
export const permissionDenyBtn = document.getElementById('permission-deny');
export const permissionOutsideEl = document.getElementById('permission-outside');
export const permissionAllowDirBtn = document.getElementById('permission-allow-dir');
export const permissionAllowOutsideBtn = document.getElementById('permission-allow-outside');
export const schedulesEl = document.getElementById('schedules');
export const sessionListEl = document.getElementById('session-list');
export const newSessionBtn = document.getElementById('new-session-btn');
export const deleteAllSessionsBtn = document.getElementById('delete-all-sessions-btn');
export const agentSelectEl = document.getElementById('agent-select');
export const mcpServersEl = document.getElementById('mcp-servers');
export const statusTextEl = document.getElementById('status-text');
export const statusBarEl = document.getElementById('prompt-status');
export const commDotEl = document.getElementById('comm-dot');
export const autoDelegateBtn = document.getElementById('auto-delegate-btn');
export const delegateModal = document.getElementById('auto-delegate-modal');
export const delegateEnabledCheckbox = document.getElementById('delegate-enabled-checkbox');
export const delegateAgentSelect = document.getElementById('delegate-agent-select');
export const delegateMatchListEl = document.getElementById('delegate-match-list');
export const delegateMatchInput = document.getElementById('delegate-match-input');
export const delegateMatchAddBtn = document.getElementById('delegate-match-add');
export const delegateNote = document.getElementById('delegate-note');
export const delegateCloseBtn = document.getElementById('delegate-close');
export const permissionStatusBtn = document.getElementById('permission-status-btn');
export const permissionSettingsModal = document.getElementById('permission-settings-modal');
export const skipPermissionsCheckbox = document.getElementById('skip-permissions-checkbox');
export const skipToolsCheckbox = document.getElementById('skip-tools-checkbox');
export const readOutsideCheckbox = document.getElementById('read-outside-checkbox');
export const writeOutsideCheckbox = document.getElementById('write-outside-checkbox');
export const permissionScopeNote = document.getElementById('permission-scope-note');
export const permissionWorkspaceNote = document.getElementById('permission-workspace-note');
export const rememberedOutsideEl = document.getElementById('remembered-outside');
export const permissionRulesListEl = document.getElementById('permission-rules-list');
export const ruleToolInput = document.getElementById('rule-tool-input');
export const ruleMatchInput = document.getElementById('rule-match-input');
export const ruleDecisionSelect = document.getElementById('rule-decision-select');
export const ruleAddBtn = document.getElementById('rule-add-btn');
export const permissionSettingsNote = document.getElementById('permission-settings-note');
export const permissionSettingsCloseBtn = document.getElementById('permission-settings-close');
export const workspaceBtn = document.getElementById('workspace-btn');
export const workspaceModal = document.getElementById('workspace-modal');
export const workspaceInput = document.getElementById('workspace-input');
export const workspaceNote = document.getElementById('workspace-note');
export const workspaceSaveBtn = document.getElementById('workspace-save');
export const workspaceCancelBtn = document.getElementById('workspace-cancel');
export const workspaceBrowseBtn = document.getElementById('workspace-browse');
export const workspaceRevealBtn = document.getElementById('workspace-reveal-btn');
export const workspaceStopBusyBtn = document.getElementById('workspace-stop-busy');
export const taskModal = document.getElementById('task-modal');
export const taskModalTitle = document.getElementById('task-modal-title');
export const taskModalBody = document.getElementById('task-modal-body');
export const taskModalNote = document.getElementById('task-modal-note');
export const taskCancelBtn = document.getElementById('task-cancel');
export const taskDeleteBtn = document.getElementById('task-delete');
export const taskCloseBtn = document.getElementById('task-close');
export const leftPanel = document.getElementById('left-panel');
export const rightPanel = document.getElementById('right-panel');
export const resizeLeftHandle = document.getElementById('resize-left');
export const resizeRightHandle = document.getElementById('resize-right');
export const stopBtn = document.getElementById('stop-btn');
export const toggleLeftBtn = document.getElementById('toggle-left');
export const toggleRightBtn = document.getElementById('toggle-right');
export const appVersionEl = document.getElementById('app-version');

// The settings window.
export const settingsModalEl = document.getElementById('settings-modal');
export const settingsBtn = document.getElementById('settings-btn');
export const settingsCloseBtn = document.getElementById('settings-close');
export const windowBarEl = document.getElementById('window-bar');
export const windowEdgesEl = document.getElementById('window-edges');
// The resize edges, paired with the direction each one means. An array
// rather than eight exports because nothing ever wants one of them by
// name — they are handled identically and differ only in what they say.
export const windowEdges = ['top', 'bottom', 'left', 'right', 'topleft', 'topright', 'bottomleft', 'bottomright']
  .map(edge => ({ edge, el: document.getElementById('window-edge-' + edge) }));
export const windowTitleEl = document.getElementById('window-title');
export const windowMinimizeBtn = document.getElementById('window-minimize');
export const windowMaximizeBtn = document.getElementById('window-maximize');
export const windowCloseBtn = document.getElementById('window-close');
export const smartAgentCheckbox = document.getElementById('smart-agent-checkbox');
export const keepGoingCheckbox = document.getElementById('keep-going-checkbox');
export const keepGoingWarnEl = document.getElementById('keep-going-warn');
export const smartAgentNoteEl = document.getElementById('smart-agent-note');
export const smartAgentWarnEl = document.getElementById('smart-agent-warn');
export const updateCheckBtn = document.getElementById('update-check-btn');
export const updateInstallBtn = document.getElementById('update-install-btn');
export const updateNoteEl = document.getElementById('update-note');
