package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/events"
)

// A task whose message was written whole, with no deltas before it, shows
// that message in its output. An orchestration run's report is written
// that way, and a let-go run's reply says to read it with "/tasks <run>".
func TestATaskOutputShowsAMessageWrittenWhole(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	store := d.Loop.Store
	if _, err := store.CreateSession("conv", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSessionIn("run1", "conv", "orchestration run", "", false); err != nil {
		t.Fatal(err)
	}
	store.Append("run1", events.TypeUserMessage, map[string]any{"text": "g"})
	store.Append("run1", events.TypeMessagePartEnd, map[string]any{"text": "Orchestration finished: 1 agent turns across 1 stages."})
	// And a streamed message after it, which must not be printed twice.
	store.Append("run1", events.TypeMessagePartDelta, map[string]any{"text": "streamed"})
	store.Append("run1", events.TypeMessagePartEnd, map[string]any{"text": "streamed"})
	store.Append("run1", events.TypeMessagePartEnd, map[string]any{"text": "a second report"})

	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/run1/output", nil))
	var body struct {
		Output string `json:"output"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(body.Output, "Orchestration finished") {
		t.Errorf("the run's report is not in its output: %q", body.Output)
	}
	if !strings.Contains(body.Output, "a second report") {
		t.Errorf("a message written whole after a streamed one is not in the output: %q", body.Output)
	}
	if strings.Count(body.Output, "streamed") != 1 {
		t.Errorf("a streamed message was printed %d times: %q", strings.Count(body.Output, "streamed"), body.Output)
	}
}

// A debate reviewer's review is its Verdict call, which ends its turn, so
// the call is in its output. A debate let go of says to read each
// reviewer's review with "/tasks <id>", and that showed nothing.
func TestATaskOutputShowsAReviewersVerdict(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	store := d.Loop.Store
	if _, err := store.CreateSession("conv", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSessionIn("rev1", "conv", "plan", "", false); err != nil {
		t.Fatal(err)
	}
	store.Append("rev1", events.TypeUserMessage, map[string]any{"text": "review round 1"})
	store.Append("rev1", events.TypeToolStart, map[string]any{
		"name": "Verdict", "input": `{"approved":false,"findings":"found a race in round 1"}`,
	})
	store.Append("rev1", events.TypeToolStart, map[string]any{"name": "read_file", "input": `{"path":"a.go"}`})

	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/rev1/output", nil))
	var body struct {
		Output string `json:"output"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Output != "[verdict: not approved]\nfound a race in round 1" {
		t.Errorf("a reviewer's output = %q, want its verdict and findings", body.Output)
	}
}
