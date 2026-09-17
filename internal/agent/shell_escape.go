package agent

import (
	"context"
	"encoding/json"
	"strings"

	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A message starting with "!" runs a shell command in the session's
// workspace and puts its output in the conversation, instead of reaching
// a model. The shape is an editor's shell escape, not a command: there
// is no name to complete and nothing to opt into, just the character and
// the command line after it.
//
// It never consults the permission rules, including a deny. That is a
// deliberate hole in an otherwise closed surface: a person typing in
// their own window runs what they could have run in their own terminal,
// so there is nobody to ask. The documentation states this next to the
// feature rather than in a footnote.
//
// A model must never cause one. The routing already carries a delegated
// flag, and a delegated task's text skips every command route; "!" sits
// inside that guard with the rest of them. A sub-agent's prompt starting
// with "!" therefore reaches the child model as literal characters, the
// same way a "/permission-skip-all" first line no longer flips the
// child's switches. See SendMessage and withDelegatedTask.
//
// The command runs through the bash tool's own execution, not a second
// one: same shell resolution, same timeout, same output cap and exit
// status wording. A second implementation is how the two drift.

// stripBangEscape reports whether text is an escaped ordinary message
// and, when it is, what the message is. "!!" starts a message that
// begins with "!", the only way to send one: a single "!" always runs.
func stripBangEscape(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "!!") {
		return "", false
	}
	return trimmed[1:], true
}

// parseBang reports whether text runs a shell command and, when it
// does, the command line after the "!". The command may be empty; the
// caller answers that with a usage error rather than running nothing.
func parseBang(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "!") || strings.HasPrefix(trimmed, "!!") {
		return "", false
	}
	return strings.TrimSpace(trimmed[1:]), true
}

// runBangCommand runs command and records the turn: the person's line as
// the user message, the output as the reply. The user message is not
// local, so the model sees on the next turn that the person ran this —
// that is the point, somebody runs "!git status" to talk about it — and
// the reply carries the command beside the text, so both clients draw
// command output instead of something the model said.
func (l *Loop) runBangCommand(ctx context.Context, sessionID, displayText, command string) error {
	if command == "" {
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})
		l.Store.Append(sessionID, events.TypeError, map[string]any{
			"error": "say what to run: `!` followed by a shell command, e.g. `!git status`. Start with `!!` to send a message that begins with `!`.",
		})
		return nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText})
	// The session's directory, not the daemon's, the same claim a tool
	// makes when it takes a relative path. The registry gate is not
	// consulted on purpose: this is the permission hole described above.
	gate := tools.WithWorkingDir(WithSessionID(ctx, sessionID), l.SessionDir(sessionID))
	input, _ := json.Marshal(map[string]string{"command": command})
	res := tools.Bash{}.Execute(gate, input)
	// Stored capped, so the transcript and the model share the one copy
	// — the same convention a tool result follows, rather than a new one.
	out := capToolResult(res.Content, 0)
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": out, "shell_command": command})
	l.appendShellTurn(sessionID, displayText, out)
	return nil
}

// appendShellTurn adds the person's command line and its output to the
// in-memory history the model sees, without any provider call — the
// shape appendDelegatedTurn records for a turn the model did not run.
func (l *Loop) appendShellTurn(sessionID, prompt, output string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages[sessionID] = append(l.messages[sessionID],
		provider.Message{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(prompt)}},
		provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(output)}},
	)
}
