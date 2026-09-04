import { app, session } from './state.js';
import * as apiClient from './api.js';
import { appendUser, appendTool, appendToolLines, appendError } from './transcript.js';

// isPlainPrompt reports whether text is an ordinary chat message rather
// than a "/"-prefixed command. Only plain prompts are safe to queue while
// a turn is in flight — queueing a command would mean replaying it as
// literal chat text to the model once dequeued, instead of running it.
export function isPlainPrompt(text) {
  return !text.startsWith('/');
}

// Every line here is plain text, rendered through appendToolLines (text
// nodes, never HTML) — so "/<skill name>" displays as written with no
// escaping step for any call site to forget. A previous version built this
// as an HTML string with one entry pre-escaped and the rest not, so that one
// line rendered double-escaped as "&lt;skill name&gt;" (bug B7); the current
// text-node rendering makes that class of bug unrepresentable.
export const HELP_TEXT = [
  'Available commands:',
  '  /help              show this help',
  '  /version            show the daemon version',
  '  /skill              list registered skills',
  '  /<skill name>        run that skill (e.g. /pdf-tools)',
  '                        type part of a name and press the right arrow to complete it;',
  '                        press it again to cycle through the other matches',
  '  Esc                 cancel the running turn',
  '  Tab / Shift+Tab      switch to the next/previous agent',
  '  Alt+Up / Alt+Down    jump back and forth between your own prompts',
  '  /agent              list registered agents',
  '  /agent <name>        switch to that agent (also available via the header dropdown)',
  '  /init              scan the repo and create/improve an AGENTS.md rules file',
  '  /memory            show the auto memory directory/index (MEMORY.md)',
  '  /config            show current settings (auto_compact, show_tps, auto_delegate)',
  '  /auto-compact [on|off|<percent>]  toggle auto-compaction, or set its threshold (default 50%)',
  '  /keep-going [on|off]  toggle the carry-on nudge for muse models',
  '  /repeat-limit [on|off|N]  end a turn after N nothing-new steps; on is 3, off (default) never',
  '  /debug-log            write every model request and response to a file per prompt, here',
  '  /llm-doctor [baseline]  probe a muse or gemma server: facts, four canaries, what differs from the baseline',
  '  /update             install the newest release and move the daemon onto it; the terminal keeps running',
  '  /reset-mcp          reconnect MCP servers and pick up config changes, no restart',
  '  /reset-skills       reload skills from disk, no restart',
  '  /config show_tps on|off       toggle the tokens/sec display under the prompt',
  '  /config auto_delegate on|off  send matching prompts to a cheaper sub-agent',
  '  /config smart_agent on|off    turn the Smart Agent bundle on or off',
  '  /smart-agent [on|off]  toggle the Smart Agent bundle, and save the choice',
  '  /orchestrate [on|off]  toggle the Orchestrate tool, and save the choice',
  '  /auto-delegate [on|off]  toggle auto-delegation, and save the choice',
  '  /permission-skip-all [on|off]  allow every prompt that would have asked',
  '                        (the pill under the prompt box also sets the target agent and patterns)',
  '  /permission-skip-tools [on|off]  allow tool prompts, still ask before leaving the project',
  '  /read-outside [on|off|mem-clear]   reading outside this project\'s directory',
  '  /write-outside [on|off|mem-clear]  writing outside it',
  '  /effort [off|low|medium|high|xhigh]  how hard the model is asked to think in this conversation',
  '  /debate <reviewer>[,<reviewer>] [rounds] <what to do>  other agents review this one\'s work, round after round',
  '  /schedule <when> <what to do>  book a prompt for later (only while localcode runs)',
  '  /show-scheduled-task  list the prompts booked for later',
  '  /model-invocable [on|off]  whether the model may run this session\'s commands itself',
  '  /clear             start the model fresh; the conversation itself is kept',
  '  /rewind            undo the last turn, and the files write_file and edit changed in it',
  '  /compact           summarize and compact the conversation right now',
  '  /compact <instructions>      give instructions for how to compact',
  '  /usage              show cumulative token usage per model',
  '  /context            what the next request is made of; /context all, /context <id>',
  '  /commands          list registered custom commands',
  '  /<custom command>   run a command defined in .localcode/commands/*.md',
  '  exit, :q            just show a message (close the browser tab yourself)',
];

// Commands handled entirely client-side — never touch the session's
// event log, so they don't show up again on session replay. Returns
// true if `text` was a local command (and thus already handled).
export async function tryLocalCommand(text) {
  const lower = text.toLowerCase();

  if (lower === 'exit' || lower === ':q') {
    appendUser(text);
    appendTool("Closing the browser tab ends the session (the Web UI can't quit the program directly).");
    return true;
  }

  if (lower === '/help') {
    appendUser(text);
    appendToolLines(HELP_TEXT.concat(['', 'You can drag a file onto the input box to attach it.']));
    return true;
  }

  if (lower === '/version') {
    appendUser(text);
    try {
      const v = await apiClient.getVersion();
      appendTool(`localcode ${v.version}`);
    } catch (err) {
      appendError(`failed to fetch version: ${err}`);
    }
    return true;
  }

  if (lower === '/agent') {
    appendUser(text);
    if (app.agents.length === 0) {
      appendTool('No agents registered.');
    } else {
      appendToolLines(
        [`Available agents (/agent <name> to switch, current: ${session.currentAgent || ''}):`]
          .concat(app.agents.map(a => `- ${a.name}: ${a.description || ''}`)),
      );
    }
    return true;
  }

  if (lower === '/commands') {
    appendUser(text);
    if (app.customCommands.length === 0) {
      appendTool('No custom commands registered. (add one under .localcode/commands/*.md)');
    } else {
      appendToolLines(
        ['Available custom commands:'].concat(app.customCommands.map(c => `- /${c.name}: ${c.description || ''}`)),
      );
    }
    return true;
  }

  const agentMatch = text.match(/^\/agent\s+(\S+)/);
  if (agentMatch) {
    appendUser(text);
    try {
      await apiClient.switchAgent(session.sessionID, agentMatch[1]);
    } catch (err) {
      appendError(`failed to switch agent: ${err}`);
    }
    return true;
  }

  // "/config" itself is handled server-side (agent.Loop) like /memory —
  // not intercepted here, so it falls through to sendMessage() below and
  // its response/config.changed event flow back over SSE like anything
  // else the model or a local command answers.
  return false;
}
