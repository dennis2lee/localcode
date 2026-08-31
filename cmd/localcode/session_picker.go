package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"localcode/internal/client"
	"localcode/internal/session"
)

// pickOrCreateSession lists existing (visible, resumable) sessions on the
// daemon and, if any exist, prompts on stdin before the TUI takes over the
// screen. This runs before tea.NewProgram's alt-screen, so plain
// stdin/stdout is fine here. A listing failure or an empty list falls
// back to creating a new session without prompting.
//
// Besides picking a session by number or starting a new one ("n"), the
// prompt also supports deleting sessions right here — "d<N>" deletes one
// listed session and re-shows the (shorter) list, "da" deletes every
// session after a yes/no confirmation. There's no other session-management
// screen in the TUI, so this is where it lives.
func pickOrCreateSession(ctx context.Context, c *client.Client, agentName string) (session.Session, error) {
	return pickOrCreateSessionFrom(ctx, c, agentName, os.Stdin, os.Stdout)
}

// pickOrCreateSessionFrom is pickOrCreateSession with its stdin/stdout
// injected, so the "d<N>"/"da" mini-language (see parseChoice) is testable
// without a real terminal.
func pickOrCreateSessionFrom(ctx context.Context, c *client.Client, agentName string, in io.Reader, out io.Writer) (session.Session, error) {
	reader := bufio.NewReader(in)

	for {
		sessions, err := c.ListSessions(ctx)
		if err != nil || len(sessions) == 0 {
			return c.CreateSession(ctx, agentName)
		}

		fmt.Fprintln(out, "Pick a session to resume:")
		for i, s := range sessions {
			fmt.Fprintf(out, "  [%d] %s  (%s, %s)\n", i+1, s.ID, s.Agent, s.CreatedAt.Local().Format("2006-01-02 15:04"))
		}
		fmt.Fprint(out, "  [n] start a new session\n  [d<N>] delete session N (e.g. d1)\n  [da] delete ALL sessions\nChoice (number, n, d<N>, or da; default n): ")

		line, _ := reader.ReadString('\n')
		choice := parseChoice(line, len(sessions))

		switch choice.action {
		case actionNew:
			return c.CreateSession(ctx, agentName)

		case actionDeleteAll:
			fmt.Fprint(out, "Delete ALL sessions, including archived ones? This cannot be undone. Type \"yes\" to confirm: ")
			confirm, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(confirm)) != "yes" {
				fmt.Fprintln(out, "Cancelled.")
				continue
			}
			if err := c.DeleteAllSessions(ctx); err != nil {
				fmt.Fprintln(out, "Failed to delete all sessions:", err)
				continue
			}
			fmt.Fprintln(out, "All sessions deleted.")
			return c.CreateSession(ctx, agentName)

		case actionDeleteOne:
			target := sessions[choice.index-1]
			if err := c.DeleteSession(ctx, target.ID); err != nil {
				fmt.Fprintln(out, "Failed to delete:", err)
			} else {
				fmt.Fprintf(out, "Deleted session %s.\n", target.ID)
			}
			continue

		case actionResume:
			return sessions[choice.index-1], nil

		default: // actionInvalid
			fmt.Fprintln(out, "Invalid input — starting a new session.")
			return c.CreateSession(ctx, agentName)
		}
	}
}

type choiceAction int

const (
	actionInvalid choiceAction = iota
	actionNew
	actionDeleteAll
	actionDeleteOne
	actionResume
)

type sessionChoice struct {
	action choiceAction
	index  int // 1-based, valid for actionDeleteOne/actionResume
}

// parseChoice interprets one line of input against pickOrCreateSession's
// mini-language, given n (the number of sessions currently listed, so a
// "d<N>"/plain index can be range-checked). Pulled out as a pure function
// so the parsing logic is unit-testable without a real terminal.
func parseChoice(line string, n int) sessionChoice {
	line = strings.TrimSpace(line)

	switch {
	case line == "" || strings.EqualFold(line, "n"):
		return sessionChoice{action: actionNew}
	case strings.EqualFold(line, "da"):
		return sessionChoice{action: actionDeleteAll}
	}

	if rest, ok := strings.CutPrefix(strings.ToLower(line), "d"); ok {
		if idx, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && idx >= 1 && idx <= n {
			return sessionChoice{action: actionDeleteOne, index: idx}
		}
	}

	if idx, err := strconv.Atoi(line); err == nil && idx >= 1 && idx <= n {
		return sessionChoice{action: actionResume, index: idx}
	}

	return sessionChoice{action: actionInvalid}
}
