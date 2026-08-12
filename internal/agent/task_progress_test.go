package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// slowTool blocks until released, so the test can look at the parent while
// the task is genuinely in the middle of something.
type slowTool struct{ release chan struct{} }

func (slowTool) Name() string                            { return "slow_thing" }
func (slowTool) Description() string                     { return "blocks" }
func (slowTool) InputSchema() json.RawMessage            { return json.RawMessage(`{"type":"object"}`) }
func (slowTool) RequiresPermission(json.RawMessage) bool { return false }
func (t slowTool) Execute(ctx context.Context, in json.RawMessage) tools.Result {
	<-t.release
	return tools.Result{Content: "done"}
}

// A background task's own log is a session nothing lists and nothing
// opens, so the conversation that started it heard three words about it:
// "spawned", "running", "completed". A task twenty minutes into a tool
// looked exactly like a task that was wedged.
func TestTaskProgressReachesTheParent(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		first := false
		once.Do(func() { first = true })
		if first {
			// Ask for the slow tool, so the task sits in it.
			for _, c := range []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","function":{"name":"slow_thing","arguments":"{}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
		} else {
			for _, c := range []string{
				`{"choices":[{"delta":{"content":"all done"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(nil)
	registry.Register(slowTool{release: release})

	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:       map[string]config.Profile{"p": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "p"}, "worker": {Profile: "p"}},
		DefaultProfile: "p",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(srv.URL, "")}, cfg)
	tm := NewTaskManager(context.Background(), loop, 2)

	const parent = "s1"
	if _, err := store.CreateSession(parent, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// Subscribed before the spawn: task.progress is transient, so a client
	// that is not listening simply misses it — which is right for a
	// progress indicator and means the test has to be listening.
	ch, _, unsub, err := store.Subscribe(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	if _, err := tm.Spawn(parent, "worker", "do the slow thing"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type != events.TypeTaskProgress {
				continue
			}
			if got := ev.Data["doing"]; got != "slow_thing" {
				t.Fatalf("progress says %q, want the name of the tool the task is in", got)
			}
			if ev.Seq != 0 {
				t.Errorf("progress was written to the log (seq %d); it is true only right now", ev.Seq)
			}
			return
		case <-deadline:
			t.Fatal("the parent was never told what the task was doing")
		}
	}
}
