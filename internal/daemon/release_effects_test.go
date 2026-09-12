package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/client"
	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

type modelCapture struct {
	mu         sync.Mutex
	lastPrompt string
	toolName   string
	toolArgs   string
	replyText  string
}

func (mc *modelCapture) getLastPrompt() string {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.lastPrompt
}

func (mc *modelCapture) setToolCall(name, args string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.toolName = name
	mc.toolArgs = args
}

func (mc *modelCapture) clearToolCall() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.toolName = ""
	mc.toolArgs = ""
}

func (mc *modelCapture) setReply(reply string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.replyText = reply
}

func (mc *modelCapture) clearReply() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.replyText = ""
}

func newEffectsDaemon(t *testing.T) (*Daemon, *client.Client, *modelCapture, string, string) {
	t.Helper()

	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	store, err := session.NewStore(sessionsDir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)

	mc := &modelCapture{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode mock model request: %v", err)
		}

		mc.mu.Lock()
		for _, m := range req.Messages {
			if m["role"] == "user" {
				if txt, ok := m["content"].(string); ok {
					mc.lastPrompt = txt
				}
			}
		}
		hasToolResult := false
		for _, m := range req.Messages {
			if m["role"] == "tool" {
				hasToolResult = true
			}
		}
		tName := mc.toolName
		tArgs := mc.toolArgs
		ans := "mock model answer"
		if mc.replyText != "" {
			ans = mc.replyText
		}
		mc.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		if tName != "" && !hasToolResult {
			argsField, err := json.Marshal(tArgs)
			if err != nil {
				t.Fatalf("marshal tool arguments string: %v", err)
			}
			chunks := []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"` + tName + `","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + string(argsField) + `}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			}
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
		} else {
			ansJSON, _ := json.Marshal(ans)
			chunks := []string{
				`{"choices":[{"delta":{"content":` + string(ansJSON) + `}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			}
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(model.Close)

	broker := agent.NewPermissionBroker(store)
	registry := tools.NewRegistry(func(context.Context, tools.Ask) (bool, error) { return true, nil })
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Edit{})
	registry.Register(tools.Glob{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: model.URL},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "test-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile:     "balanced",
		MaxConcurrentTasks: 2,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	providers := map[string]provider.Provider{"local": provider.NewOpenAICompat(model.URL, "")}
	loop := agent.New(store, registry, providers, cfg)
	registry.BeforeWrite = loop.CheckpointWrite

	initialWorkspace := filepath.Join(dir, "initial-workspace")
	if err := os.MkdirAll(initialWorkspace, 0o755); err != nil {
		t.Fatalf("create initial workspace: %v", err)
	}
	loop.SetProjectDir(initialWorkspace)

	tasks := agent.NewTaskManager(context.Background(), loop, cfg.MaxConcurrentTasks)
	d := New(loop, broker, tasks, nil, nil, "test-version")
	httpSrv := httptest.NewServer(d.Handler())
	t.Cleanup(httpSrv.Close)

	c := client.New(httpSrv.URL)
	return d, c, mc, dir, initialWorkspace
}

func countTurnDone(d *Daemon, sessionID string) int {
	evs, err := d.Loop.Store.Events(sessionID, 0)
	if err != nil {
		return 0
	}
	count := 0
	for _, ev := range evs {
		if ev.Type == events.TypeTurnDone || ev.Type == events.TypeTurnCancelled {
			count++
		}
	}
	return count
}

func waitForTurn(t *testing.T, d *Daemon, sessionID string, prevDoneCount int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.turns.mu.Lock()
		busy := d.turns.cancels[sessionID] != nil
		d.turns.mu.Unlock()
		if !busy && countTurnDone(d, sessionID) > prevDoneCount {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for turn in session %s to finish", sessionID)
}

func sendMessageAndWait(t *testing.T, c *client.Client, d *Daemon, sessionID, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prev := countTurnDone(d, sessionID)
	if err := c.SendMessage(ctx, sessionID, text); err != nil {
		t.Fatalf("SendMessage(%q): %v", text, err)
	}
	waitForTurn(t, d, sessionID, prev)
}

func lastReply(t *testing.T, d *Daemon, sessionID string) string {
	t.Helper()
	evs, err := d.Loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("reading events for %s: %v", sessionID, err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == events.TypeMessagePartEnd {
			if txt, ok := evs[i].Data["text"].(string); ok {
				return txt
			}
		}
	}
	return ""
}

func parseExportPath(reply string) string {
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Written to ") {
			rest := strings.TrimPrefix(line, "Written to ")
			if idx := strings.LastIndex(rest, " ("); idx > 0 {
				return rest[:idx]
			}
			return rest
		}
	}
	return ""
}

// /workspace reports the directory; /workspace <path> moves it and the session
// really works there afterwards; a path that is not a directory changes nothing;
// and the move is refused while a turn is running (it is in heldUntilIdle, and
// the refusal carries a "held" marker the clients tell from an ordinary 409).
func TestWorkspaceMovesTheConversation(t *testing.T) {
	d, c, mc, dir, initialWorkspace := newEffectsDaemon(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 1. Bare /workspace reports the directory.
	sendMessageAndWait(t, c, d, sess.ID, "/workspace")
	reply := lastReply(t, d, sess.ID)
	if !strings.Contains(reply, "workspace: "+initialWorkspace) {
		t.Errorf("bare /workspace reply = %q, want containing %q", reply, initialWorkspace)
	}
	if !strings.Contains(reply, "usage: /workspace <path>") {
		t.Errorf("bare /workspace reply = %q, want usage explanation", reply)
	}
	if got := d.Loop.SessionDir(sess.ID); got != initialWorkspace {
		t.Errorf("SessionDir = %q, want initial %q", got, initialWorkspace)
	}

	// 2. /workspace <path> moves it and the session really works there afterwards.
	targetDir := filepath.Join(dir, "moved-workspace")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sendMessageAndWait(t, c, d, sess.ID, "/workspace "+targetDir)
	reply = lastReply(t, d, sess.ID)
	if !strings.Contains(reply, "workspace: "+targetDir) {
		t.Errorf("/workspace <path> reply = %q, want naming %q", reply, targetDir)
	}
	if got := d.Loop.SessionDir(sess.ID); got != targetDir {
		t.Errorf("after move SessionDir = %q, want target %q", got, targetDir)
	}
	stored, err := d.Loop.Store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Workspace != targetDir {
		t.Errorf("stored workspace = %q, want %q", stored.Workspace, targetDir)
	}

	// Verify the session really works there afterwards: tool calls resolve relative to targetDir.
	relName := "created-after-move.txt"
	content := "session really works in moved workspace"
	toolArgs, _ := json.Marshal(map[string]string{"path": relName, "content": content})
	mc.setToolCall("write_file", string(toolArgs))
	sendMessageAndWait(t, c, d, sess.ID, "please create "+relName)
	mc.clearToolCall()

	createdInTarget := filepath.Join(targetDir, relName)
	data, err := os.ReadFile(createdInTarget)
	if err != nil {
		t.Fatalf("expected file created in target workspace %s: %v", createdInTarget, err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", string(data), content)
	}
	createdInInitial := filepath.Join(initialWorkspace, relName)
	if _, err := os.Stat(createdInInitial); !os.IsNotExist(err) {
		t.Errorf("file was written to old workspace %s instead of %s", createdInInitial, createdInTarget)
	}

	// 3. A path that is not a directory changes nothing.
	nonexistent := filepath.Join(dir, "does-not-exist")
	sendMessageAndWait(t, c, d, sess.ID, "/workspace "+nonexistent)
	reply = lastReply(t, d, sess.ID)
	if !strings.Contains(reply, "workspace unchanged:") {
		t.Errorf("nonexistent path reply = %q, want workspace unchanged", reply)
	}
	if got := d.Loop.SessionDir(sess.ID); got != targetDir {
		t.Errorf("SessionDir after invalid move = %q, want unchanged %q", got, targetDir)
	}

	regularFile := filepath.Join(targetDir, "regular.txt")
	if err := os.WriteFile(regularFile, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendMessageAndWait(t, c, d, sess.ID, "/workspace "+regularFile)
	reply = lastReply(t, d, sess.ID)
	if !strings.Contains(reply, "workspace unchanged:") || !strings.Contains(reply, "not a directory") {
		t.Errorf("regular file path reply = %q, want workspace unchanged and not a directory", reply)
	}
	if got := d.Loop.SessionDir(sess.ID); got != targetDir {
		t.Errorf("SessionDir after file move = %q, want unchanged %q", got, targetDir)
	}

	// 4. The move is refused while a turn is running.
	// (it is in heldUntilIdle, and the refusal carries a "held" marker the clients tell from an ordinary 409).
	if !d.turns.begin(sess.ID, func() {}) {
		t.Fatal("could not mark session busy")
	}

	moveErr := c.SendMessage(ctx, sess.ID, "/workspace "+initialWorkspace)
	if moveErr == nil {
		t.Fatal("expected /workspace <path> to be refused while turn is running, got nil")
	}
	if !client.IsHeld(moveErr) {
		t.Errorf("IsHeld(%v) = false, want true for held command", moveErr)
	}
	var se *client.StatusError
	if !errors.As(moveErr, &se) {
		t.Fatalf("expected *client.StatusError, got %T", moveErr)
	}
	if se.Status != http.StatusConflict {
		t.Errorf("status = %d, want 409", se.Status)
	}
	if se.Held != "/workspace" {
		t.Errorf("se.Held = %q, want /workspace", se.Held)
	}
	if client.IsBusy(moveErr) {
		t.Errorf("IsBusy(%v) = true, want false (clients tell held apart from ordinary 409)", moveErr)
	}
	d.turns.end(sess.ID)

	// Contrast with an ordinary 409 (e.g. exclusive hold where injection is refused):
	sess2, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatal(err)
	}
	if !d.turns.beginExclusive(sess2.ID, func() {}) {
		t.Fatal("could not mark session exclusive")
	}
	busyErr := c.SendMessage(ctx, sess2.ID, "an ordinary message")
	if busyErr == nil {
		t.Fatal("expected ordinary message to 409 while session is exclusively held")
	}
	if !client.IsBusy(busyErr) {
		t.Errorf("IsBusy(busyErr) = false, want true for ordinary 409")
	}
	if client.IsHeld(busyErr) {
		t.Errorf("IsHeld(busyErr) = true, want false for ordinary busy message")
	}
	d.turns.end(sess2.ID)

	if got := d.Loop.SessionDir(sess.ID); got != targetDir {
		t.Errorf("SessionDir after held move = %q, want unchanged %q", got, targetDir)
	}
}

// /export writes a Markdown file and reports the path; the file holds the
// conversation; it is written 0600; an undone turn is not in it.
func TestExportWritesTheConversationToAFile(t *testing.T) {
	d, c, mc, _, _ := newEffectsDaemon(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Populate initial conversation.
	firstPrompt := "Keep this message in the exported file"
	sendMessageAndWait(t, c, d, sess.ID, firstPrompt)

	// Run /export.
	sendMessageAndWait(t, c, d, sess.ID, "/export")
	reply := lastReply(t, d, sess.ID)
	exportPath := parseExportPath(reply)
	if exportPath == "" {
		t.Fatalf("could not parse export path from reply: %q", reply)
	}

	// Verify file exists on disk.
	info, err := os.Stat(exportPath)
	if err != nil {
		t.Fatalf("stat exported file %s: %v", exportPath, err)
	}

	// Verify written mode is 0600.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("exported file mode = %o, want 0600", perm)
	}

	// Verify the file holds the conversation.
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	if !strings.Contains(string(data), firstPrompt) {
		t.Errorf("exported content missing %q:\n%s", firstPrompt, string(data))
	}

	// An undone turn is not in it.
	undonePrompt := "Prompt to be undone\nSecret undone body text that must not appear in export transcript"
	mc.mu.Lock()
	undoneReplyText := "Special model answer for undone turn that must not appear in export"
	mc.mu.Unlock()
	// Temporarily make mock model return undoneReplyText:
	// We can update the mock model response in modelCapture
	mc.setReply(undoneReplyText)
	sendMessageAndWait(t, c, d, sess.ID, undonePrompt)
	mc.clearReply()

	sendMessageAndWait(t, c, d, sess.ID, "/rewind")
	sendMessageAndWait(t, c, d, sess.ID, "/export")
	reply = lastReply(t, d, sess.ID)
	secondExportPath := parseExportPath(reply)
	if secondExportPath == "" {
		t.Fatalf("could not parse second export path from reply: %q", reply)
	}

	info2, err := os.Stat(secondExportPath)
	if err != nil {
		t.Fatalf("stat second exported file: %v", err)
	}
	if perm := info2.Mode().Perm(); perm != 0o600 {
		t.Errorf("second exported file mode = %o, want 0600", perm)
	}

	data2, err := os.ReadFile(secondExportPath)
	if err != nil {
		t.Fatalf("read second exported file: %v", err)
	}
	transcript := string(data2)
	if !strings.Contains(transcript, firstPrompt) {
		t.Errorf("transcript missing initial prompt %q", firstPrompt)
	}
	// The undone turn's body and model response are not in the file.
	if strings.Contains(transcript, "Secret undone body text that must not appear in export transcript") {
		t.Errorf("transcript contains undone turn body text")
	}
	if strings.Contains(transcript, undoneReplyText) {
		t.Errorf("transcript contains undone turn model reply %q", undoneReplyText)
	}
	// The user message header for the undone turn was dropped, leaving only the first turn's header.
	if count := strings.Count(transcript, "## "); count != 1 {
		t.Errorf("transcript has %d user message headings, want 1 (undone turn dropped)", count)
	}
	// The rewind marker itself is recorded.
	if !strings.Contains(transcript, "_One turn was undone here._") {
		t.Errorf("transcript missing rewind marker note")
	}
}

// /review reaches the model with a prompt naming the range and saying not to change
// anything; the range follows the argument (none, staged, head, a revision);
// a custom command called "review" wins over the built-in.
func TestReviewYieldsToACustomReviewCommand(t *testing.T) {
	d, c, mc, _, _ := newEffectsDaemon(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const noChangeSentence = "Do not change any file. This is a review; the person asked what is wrong, not for it to be fixed."

	// 1. None: uncommitted changes.
	sendMessageAndWait(t, c, d, sess.ID, "/review")
	prompt := mc.getLastPrompt()
	if !strings.Contains(prompt, "the uncommitted changes in this working tree") {
		t.Errorf("bare /review prompt missing range name:\n%s", prompt)
	}
	if !strings.Contains(prompt, noChangeSentence) {
		t.Errorf("bare /review prompt missing instruction not to change files:\n%s", prompt)
	}

	// 2. Staged changes.
	sendMessageAndWait(t, c, d, sess.ID, "/review staged")
	prompt = mc.getLastPrompt()
	if !strings.Contains(prompt, "the staged changes") {
		t.Errorf("/review staged prompt missing range name:\n%s", prompt)
	}
	if !strings.Contains(prompt, noChangeSentence) {
		t.Errorf("/review staged prompt missing instruction not to change files:\n%s", prompt)
	}

	// 3. Head commit.
	sendMessageAndWait(t, c, d, sess.ID, "/review head")
	prompt = mc.getLastPrompt()
	if !strings.Contains(prompt, "the most recent commit") {
		t.Errorf("/review head prompt missing range name:\n%s", prompt)
	}
	if !strings.Contains(prompt, noChangeSentence) {
		t.Errorf("/review head prompt missing instruction not to change files:\n%s", prompt)
	}

	// 4. A revision / branch / range.
	sendMessageAndWait(t, c, d, sess.ID, "/review v1.4.0..HEAD")
	prompt = mc.getLastPrompt()
	if !strings.Contains(prompt, `the changes in "v1.4.0..HEAD"`) {
		t.Errorf("/review <rev> prompt missing range name:\n%s", prompt)
	}
	if !strings.Contains(prompt, noChangeSentence) {
		t.Errorf("/review <rev> prompt missing instruction not to change files:\n%s", prompt)
	}

	// 5. A custom command called "review" wins over the built-in.
	customBody := "Custom team review instructions: check security and error handling only."
	d.Loop.Commands = append(d.Loop.Commands, commands.Command{
		Name:        "review",
		Description: "custom review override",
		Body:        customBody,
	})

	sendMessageAndWait(t, c, d, sess.ID, "/review")
	prompt = mc.getLastPrompt()
	if !strings.Contains(prompt, customBody) {
		t.Errorf("custom review prompt not sent to model:\n%s", prompt)
	}
	if strings.Contains(prompt, "the uncommitted changes in this working tree") {
		t.Errorf("custom review was shadowed by built-in review")
	}
	if strings.Contains(prompt, noChangeSentence) {
		t.Errorf("custom review prompt has built-in text")
	}
}

// "/rewind" and "/redo" over the HTTP path: a real turn writes a file, the
// rewind puts it back, the redo puts it forward again, and once the
// conversation has moved on there is nothing to put back.
//
// The turn is a real one — the model is told to call write_file, so the loop
// opens the turn and the checkpoint sink runs the way it does in a daemon.
// That is the half internal/agent/redo_test.go cannot reach: it drives the
// loop directly, so it proves the restore but not that a client asking over
// HTTP gets it.
func TestRewindAndRedoTravelTheHTTPPath(t *testing.T) {
	d, c, mc, _, workspace := newEffectsDaemon(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const before, after = "before the turn", "after the turn"
	path := filepath.Join(workspace, "changed.txt")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	// One real turn, whose only act is to overwrite that file.
	args, err := json.Marshal(map[string]string{"path": path, "content": after})
	if err != nil {
		t.Fatal(err)
	}
	mc.setToolCall("write_file", string(args))
	sendMessageAndWait(t, c, d, sess.ID, "change the file")
	mc.clearToolCall()
	if b, _ := os.ReadFile(path); string(b) != after {
		t.Fatalf("the turn left %q, want %q — it never wrote the file", string(b), after)
	}

	sendMessageAndWait(t, c, d, sess.ID, "/rewind")
	if reply := lastReply(t, d, sess.ID); !strings.Contains(reply, "Rewound one turn") || !strings.Contains(reply, "change the file") {
		t.Errorf("/rewind said %q, want it to name the turn it undid", reply)
	}
	if b, _ := os.ReadFile(path); string(b) != before {
		t.Errorf("after the rewind the file holds %q, want the pre-image %q", string(b), before)
	}

	sendMessageAndWait(t, c, d, sess.ID, "/redo")
	if reply := lastReply(t, d, sess.ID); !strings.Contains(reply, "Put the turn back: change the file") {
		t.Errorf("/redo said %q, want it to name the turn it put back", reply)
	}
	if b, _ := os.ReadFile(path); string(b) != after {
		t.Errorf("after the redo the file holds %q, want the post-image %q", string(b), after)
	}

	// Rewind once more, then let the conversation move on. The redo is gone.
	sendMessageAndWait(t, c, d, sess.ID, "/rewind")
	sendMessageAndWait(t, c, d, sess.ID, "somewhere else entirely")
	sendMessageAndWait(t, c, d, sess.ID, "/redo")
	reply := lastReply(t, d, sess.ID)
	if !strings.Contains(reply, "there is nothing to put back") || !strings.Contains(reply, "once the conversation has moved on") {
		t.Errorf("/redo said %q, want it to say why there is nothing to put back", reply)
	}
	if b, _ := os.ReadFile(path); string(b) != before {
		t.Errorf("the refused redo changed the file to %q, want it left at %q", string(b), before)
	}

	// Both are held while a turn is running, rather than racing it.
	if !d.turns.begin(sess.ID, func() {}) {
		t.Fatal("could not mark the session busy")
	}
	defer d.turns.end(sess.ID)
	for _, cmd := range []string{"/rewind", "/redo"} {
		err := c.SendMessage(ctx, sess.ID, cmd)
		if err == nil {
			t.Fatalf("%s was accepted while a turn was running", cmd)
		}
		if !client.IsHeld(err) {
			t.Errorf("IsHeld(%s) = false, want true", cmd)
		}
		var se *client.StatusError
		if !errors.As(err, &se) {
			t.Fatalf("%s returned %T, want *client.StatusError", cmd, err)
		}
		if se.Status != http.StatusConflict {
			t.Errorf("%s returned %d, want 409", cmd, se.Status)
		}
		if se.Held != cmd {
			t.Errorf("%s was held as %q, want %q", cmd, se.Held, cmd)
		}
	}
}
