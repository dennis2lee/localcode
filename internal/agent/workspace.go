package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"localcode/internal/events"
)

// "/workspace": the directory this conversation works in.
//
// It exists because a parity review turned up an asymmetry nobody had
// noticed: the workspace was reachable only from the Web UI's button, so
// a terminal user could not move a conversation to another project at
// all. Everything the button does was already here — the store records a
// workspace per session, and every turn carries its session's directory —
// and there was simply no way to ask for it in words.

// ResolveWorkspace turns what somebody typed into the absolute directory
// it names, or says why it is not one.
//
// Exported because the Web UI's handler resolves the same way, and a
// path the button accepts and the command refuses (or the reverse) would
// be two answers to one question.
func ResolveWorkspace(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", fmt.Errorf("a path is required")
	}
	// "~/work/thing" is what a person types; only the leading one, since
	// a tilde anywhere else is a legitimate character in a name.
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", p, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}

// routeWorkspace answers "/workspace" and "/workspace <path>".
func (l *Loop) routeWorkspace(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/workspace")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	if arg == "" {
		return true, l.replyText(sessionID, fmt.Sprintf(
			"workspace: %s\n\nusage: /workspace <path> to move this conversation to another directory.",
			l.SessionDir(sessionID)))
	}

	abs, err := ResolveWorkspace(arg)
	if err != nil {
		return true, l.replyText(sessionID, "workspace unchanged: "+err.Error())
	}
	if _, err := l.Store.SetWorkspace(sessionID, abs); err != nil {
		return true, l.replyText(sessionID, "workspace unchanged: "+err.Error())
	}
	// So the other client's workspace button moves too. Without it the
	// Web UI went on naming the old directory until something else made
	// it refetch, which is the kind of disagreement that ends with
	// somebody writing a file into the wrong project.
	l.Store.Append(sessionID, events.TypeWorkspaceChanged, map[string]any{"path": abs})
	return true, l.replyText(sessionID, "workspace: "+abs)
}
