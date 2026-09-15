package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/client"
	"localcode/internal/events"
)

// /commands lists custom commands in the transcript without calling the model
// or the daemon.
//
// Commands are loaded from .localcode/commands/*.md into Model.commandsList.
// If /commands is not recognized by the local dispatcher, it falls through to
// the model as prose — causing the model to hallucinate or attempt turns for
// what should be an instant local listing.
func TestCommandsCommandListsRegisteredCustomCommandsLocally(t *testing.T) {
	m := newTestModel()
	m.commandsList = []client.CommandInfo{
		{Name: "review", Description: "review pull request"},
		{Name: "deploy", Description: "deploy service to production"},
	}

	m, cmd := pressEnterWith(t, m, "/commands")
	if cmd != nil {
		t.Errorf("/commands should answer locally with no server command, got %v", cmd)
	}
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after /commands, got %q", m.input.Value())
	}
	transcript := m.transcriptText()
	for _, want := range []string{"Available custom commands:", "/review", "review pull request", "/deploy", "deploy service to production"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript missing %q: %s", want, transcript)
		}
	}

	// An empty list explains where to put command files rather than drawing
	// a blank block.
	mEmpty := newTestModel()
	mEmpty, _ = pressEnterWith(t, mEmpty, "/commands")
	if !strings.Contains(mEmpty.transcriptText(), "No custom commands registered") {
		t.Errorf("expected empty custom commands message, got: %s", mEmpty.transcriptText())
	}
}

// /clear-session is an alias for /new.
//
// Users arriving from other AI interfaces expect /clear-session to reset the
// conversation and start fresh. If this alias does not dispatch, it falls
// through to the model as an ordinary turn prompt, which can trigger
// unintended model actions instead of opening a brand new session.
// This test exercises the alias against an httptest daemon server,
// confirming it makes a POST /api/sessions request and returns sessionCreatedMsg.
func TestClearSessionAliasCreatesAndOpensNewSession(t *testing.T) {
	var requestedPath string
	var requestedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedMethod = r.Method
		if r.Method == http.MethodPost && r.URL.Path == "/api/sessions" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "new-session-cs1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event), false)
	cmd, ok := dispatchLocalCommand(&m, "/clear-session")
	if !ok || cmd == nil {
		t.Fatal("/clear-session produced no command")
	}
	msg, isCreated := cmd().(sessionCreatedMsg)
	if !isCreated {
		t.Fatalf("/clear-session returned %T, want sessionCreatedMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("clear-session: %v", msg.err)
	}
	if requestedMethod != http.MethodPost || requestedPath != "/api/sessions" {
		t.Errorf("asked %s %s, want POST /api/sessions", requestedMethod, requestedPath)
	}
	if msg.id != "new-session-cs1" {
		t.Errorf("new session id = %q, want new-session-cs1", msg.id)
	}
}
