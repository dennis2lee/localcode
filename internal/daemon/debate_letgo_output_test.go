package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/events"
)

// A debate let go of says to read each reviewer's review with
// "/tasks <id>", and that is where the review is once the reviewer
// finishes: its Verdict call, end to end through the daemon.
func TestALetGoReviewersReviewIsInItsTaskOutput(t *testing.T) {
	release := make(chan struct{})
	var reviewing atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1].Content
		if strings.Contains(last, "You are reviewing work by") {
			reviewing.Add(1)
			<-release
			writeSSEToolCall(w, "call_v", "Verdict", `{"approved":false,"findings":"found a race in round 1"}`)
			return
		}
		writeSSEText(w, "done")
	}))
	defer srv.Close()

	d := newTestDaemon(t, srv.URL)
	d.Loop.Tools.Register(agent.VerdictTool{})
	d.Loop.Config.SetSkipPermissionsRuntime(true)

	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	turnDone := make(chan error, 1)
	go func() {
		turnDone <- d.Loop.SendMessage(context.Background(), "s1", "general-purpose", "/debate plan,build 2 check it")
	}()

	deadline := time.Now().Add(5 * time.Second)
	for reviewing.Load() < 2 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("only %d of 2 reviewers reached the server", reviewing.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	ids, ok := d.Tasks.DetachChildOf("s1")
	if !ok || len(ids) != 2 {
		close(release)
		t.Fatalf("DetachChildOf = (%v, %v), want 2 reviewer task IDs", ids, ok)
	}

	if err := <-turnDone; err != nil {
		close(release)
		t.Fatalf("SendMessage: %v", err)
	}

	close(release)
	for time.Now().Before(deadline) {
		if len(d.Tasks.Running()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	evs, _ := d.Loop.Store.Events("s1", 0)
	var note string
	for _, ev := range evs {
		if ev.Type == events.TypeDebateEnded {
			note, _ = ev.Data["note"].(string)
		}
	}
	for _, id := range ids {
		if !strings.Contains(note, "/tasks "+id) {
			t.Fatalf("debate.ended note = %q, expected it to instruct reading /tasks %s", note, id)
		}
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+id+"/output", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/tasks/%s/output status = %d", id, rec.Code)
		}
		var body struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body.Output, "found a race in round 1") {
			t.Errorf("GET /api/tasks/%s/output = %q, want reviewer Verdict findings %q promised by %q",
				id, body.Output, "found a race in round 1", note)
		}
	}
}

func writeSSEText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	chunk, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]any{"content": text}}},
	})
	_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))
}

func writeSSEToolCall(w http.ResponseWriter, id, name, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	chunk, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index": 0,
					"id":    id,
					"type":  "function",
					"function": map[string]any{
						"name":      name,
						"arguments": args,
					},
				}},
			},
		}},
	})
	_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))
}
