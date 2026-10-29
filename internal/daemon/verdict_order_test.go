package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/agent"
	"localcode/internal/events"
)

// A reviewer that writes a sentence and calls Verdict in the same reply.
// tool.start is written while the reply streams, before its
// message.part.end, so the verdict has to start a line of its own.
func TestAVerdictIsNotGluedToTheTextBeforeIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(last, "You are reviewing work by") {
			text, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]any{"content": "I read sum.go."}}},
			})
			call, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{
					"delta": map[string]any{
						"tool_calls": []map[string]any{{
							"index": 0, "id": "call_v", "type": "function",
							"function": map[string]any{"name": "Verdict", "arguments": `{"approved":true,"findings":"sum is right"}`},
						}},
					},
				}},
			})
			_, _ = w.Write([]byte("data: " + string(text) + "\n\ndata: " + string(call) + "\n\ndata: [DONE]\n\n"))
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
	if err := d.Loop.SendMessage(context.Background(), "s1", "general-purpose", "/debate plan 1 write a sum function"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	evs, _ := d.Loop.Store.Events("s1", 0)
	var reviewer string
	for _, ev := range evs {
		if ev.Type == events.TypeTaskSpawned {
			reviewer, _ = ev.Data["task_id"].(string)
		}
	}
	if reviewer == "" {
		t.Fatal("no reviewer was spawned")
	}
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+reviewer+"/output", nil))
	var body struct {
		Output string `json:"output"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	want := "I read sum.go.\n[verdict: approved]\nsum is right"
	if body.Output != want {
		t.Errorf("reviewer output = %q, want %q", body.Output, want)
	}
}
