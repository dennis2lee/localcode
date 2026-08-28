package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/tools"
)

// A turn in some *other* session no longer blocks a switch.
//
// It used to, because the working directory was one process-wide thing, so
// the guard had to be daemon-wide — including a turn nobody was watching
// and one parked forever on an unanswered permission request. That is what
// "I often just can't change the workspace" was. Each session carries its
// own directory now, so another session being busy is simply not this
// session's business.
func TestAnotherSessionsTurnNoLongerBlocksASwitch(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	for _, id := range []string{"s-busy", "s-mine"} {
		if _, err := d.Loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !d.turns.begin("s-busy", cancel) {
		t.Fatal("could not mark the other session busy")
	}

	target := t.TempDir()
	body, _ := json.Marshal(map[string]string{"path": target, "session_id": "s-mine"})
	resp, err := http.Post(srv.URL+"/api/workspace", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 300)
		n, _ := resp.Body.Read(buf)
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, buf[:n])
	}

	// And it moved the session that asked, not the busy one.
	mine, err := d.Loop.Store.Get("s-mine")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Workspace != target {
		t.Errorf("s-mine workspace = %q, want %q", mine.Workspace, target)
	}
	busy, err := d.Loop.Store.Get("s-busy")
	if err != nil {
		t.Fatal(err)
	}
	if busy.Workspace == target {
		t.Error("moving one session moved another; that is the shared workspace this replaced")
	}
}

// This session's own turn still blocks it: its tool call, mid-execution,
// would otherwise find the ground moved under it. Unlike the old
// daemon-wide guard, this is a turn the person asking can see.
func TestThisSessionsOwnTurnStillBlocksASwitch(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	if _, err := d.Loop.Store.CreateSession("s-mine", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !d.turns.begin("s-mine", cancel) {
		t.Fatal("could not mark the session busy")
	}

	body, _ := json.Marshal(map[string]string{"path": t.TempDir(), "session_id": "s-mine"})
	resp, err := http.Post(srv.URL+"/api/workspace", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var out struct {
		Busy []string `json:"busy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("the refusal is not JSON a client can act on: %v", err)
	}
	if len(out.Busy) != 1 || out.Busy[0] != "s-mine" {
		t.Errorf("busy = %v, want [s-mine]", out.Busy)
	}
}

// Opening a file-manager window only makes sense on the machine with the
// screen, which is the same rule the folder picker follows. A daemon
// reached over the network says so rather than opening a window in front
// of nobody.
func TestRevealWorkspaceIsRefusedWithoutAScreen(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/workspace/reveal", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}

	// And the client is told, so it can hide the button rather than
	// offering one that always fails.
	wresp, err := http.Get(srv.URL + "/api/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer wresp.Body.Close()
	var w struct {
		CanReveal bool `json:"can_reveal"`
	}
	json.NewDecoder(wresp.Body).Decode(&w)
	if w.CanReveal {
		t.Error("can_reveal is true on a daemon with no screen attached")
	}
}

// It opens the daemon's own workspace, not a path from the request: this
// starts a process with a path argument, and taking that path from the
// caller would make it a way to ask the daemon to open anything at all.
func TestRevealWorkspaceOpensTheWorkspaceNotACallerPath(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	workspace := t.TempDir()
	d.Loop.SetProjectDir(workspace)

	var opened string
	d.RevealDirectory = func(ctx context.Context, dir string) error {
		opened = dir
		return nil
	}
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/workspace/reveal", "application/json",
		strings.NewReader(`{"path":"/etc"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if opened != workspace {
		t.Errorf("opened %q, want the daemon's own workspace %q", opened, workspace)
	}
}

// The refusal offers to stop the turns holding the switch up, and the case
// that matters most is a session parked on a permission request nobody
// answered — the daemon's own message names it, because that session stays
// busy indefinitely and its question is only visible in a session the user
// may not be looking at. If stopping it did not actually release the
// workspace, the button would be an offer the daemon cannot keep.
func TestStoppingATurnParkedOnAPermissionFreesTheWorkspace(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	// The parked session is the one being moved: with a per-session
	// workspace, that is the only session whose turn can block it.
	const busy = "s-parked"
	if _, err := d.Loop.Store.CreateSession(busy, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	// A turn parked exactly the way a permission request parks one: the
	// broker blocks until an answer arrives or the turn's context is
	// cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	if !d.turns.begin(busy, cancel) {
		t.Fatal("could not mark the session busy")
	}
	asked := make(chan struct{})
	answered := make(chan bool, 1)
	go func() {
		close(asked)
		// The session id has to travel on the context, the way a real turn
		// carries it — without it the broker refuses rather than asking,
		// and nothing parks.
		allow, _ := d.Broker.Func()(agent.WithSessionID(ctx, busy), tools.Ask{Tool: "bash", Subject: "rm -rf /", Description: "delete everything"})
		answered <- allow
		d.turns.end(busy)
	}()
	<-asked

	// The switch is refused, and says which session to go and deal with.
	target := t.TempDir()
	body, _ := json.Marshal(map[string]string{"path": target, "session_id": busy})
	resp, err := http.Post(srv.URL+"/api/workspace", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var refusal struct {
		Busy []string `json:"busy"`
	}
	json.NewDecoder(resp.Body).Decode(&refusal)
	if len(refusal.Busy) != 1 || refusal.Busy[0] != busy {
		t.Fatalf("busy = %v, want [%s]", refusal.Busy, busy)
	}

	// What the button does: cancel the named turn...
	cresp, err := http.Post(srv.URL+"/api/sessions/"+busy+"/cancel", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	cresp.Body.Close()
	if cresp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", cresp.StatusCode)
	}

	select {
	case allow := <-answered:
		if allow {
			t.Error("a cancelled permission request was treated as an approval")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling did not release the turn parked on the permission request")
	}

	// ...and then retries the switch, which must now succeed.
	resp2, err := http.Post(srv.URL+"/api/workspace", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		buf := make([]byte, 300)
		n, _ := resp2.Body.Read(buf)
		t.Fatalf("switch after stopping the turn = %d: %s", resp2.StatusCode, buf[:n])
	}
}
