package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// backgroundServer answers with whichever text the prompt asked it to say,
// after an optional pause. The pause is what makes "launched in the
// background" mean something: without it every task would be finished
// before the next line of the test ran, and collecting them in launch
// order would pass whether or not the order was preserved.
func backgroundServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		say := "nothing"
		for _, m := range body.Messages {
			if m["role"] != "user" {
				continue
			}
			if s, ok := m["content"].(string); ok && strings.HasPrefix(s, "say ") {
				say = strings.TrimPrefix(s, "say ")
			}
		}
		// The first task launched is the slowest to answer, so collecting
		// in launch order is a different answer from collecting in
		// completion order.
		if strings.HasSuffix(say, "-slow") {
			time.Sleep(120 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", say)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
}

func newBackgroundLoop(t *testing.T, modelURL string) (*Loop, *TaskManager) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(nil)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles:       map[string]config.Profile{"only": {Provider: "local", Model: "m"}},
		DefaultProfile: "only",
	}
	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}, cfg)
	loop.SetSmartAgentEnabled(true)
	tasks := NewTaskManager(context.Background(), loop, 5)
	registry.Register(NewTaskBackgroundTool(tasks, loop.DelegatableAgents))
	registry.Register(NewTaskCollectTool(tasks))
	return loop, tasks
}

func launch(t *testing.T, reg *tools.Registry, ctx context.Context, agentName, prompt string) tools.Result {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"agent": agentName, "prompt": prompt})
	return reg.Call(ctx, "TaskBackground", input, "")
}

// The point of the pair of tools: three questions asked at once take as
// long as the slowest, not as long as all three together, and the answers
// come back matched to the order they were asked in.
func TestBackgroundTasksRunAtOnceAndCollectInLaunchOrder(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	for _, prompt := range []string{"say first-slow", "say second", "say third"} {
		if res := launch(t, loop.Tools, ctx, "explore", prompt); res.IsError {
			t.Fatalf("launch %q: %s", prompt, res.Content)
		}
	}

	started := time.Now()
	res := loop.Tools.Call(ctx, "TaskCollect", json.RawMessage(`{}`), "")
	if res.IsError {
		t.Fatalf("collect: %s", res.Content)
	}
	// Three sequential 120ms answers would be 360ms; only the first task
	// is slow, so anything near that means they ran one after another.
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Errorf("collecting took %v, which is long enough that the tasks did not run at once", elapsed)
	}

	firstAt := strings.Index(res.Content, "first-slow")
	secondAt := strings.Index(res.Content, "second")
	thirdAt := strings.Index(res.Content, "third")
	if firstAt < 0 || secondAt < 0 || thirdAt < 0 {
		t.Fatalf("not every answer came back:\n%s", res.Content)
	}
	if !(firstAt < secondAt && secondAt < thirdAt) {
		t.Errorf("answers came back out of launch order:\n%s", res.Content)
	}
}

func TestCollectingOneTaskLeavesTheOthersOutstanding(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	first := launch(t, loop.Tools, ctx, "explore", "say alpha")
	if first.IsError {
		t.Fatalf("launch: %s", first.Content)
	}
	if res := launch(t, loop.Tools, ctx, "explore", "say beta"); res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}

	id := taskIDFrom(t, first.Content)
	input, _ := json.Marshal(map[string]string{"task_id": id})
	res := loop.Tools.Call(ctx, "TaskCollect", input, "")
	if res.IsError {
		t.Fatalf("collect one: %s", res.Content)
	}
	if !strings.Contains(res.Content, "alpha") || strings.Contains(res.Content, "beta") {
		t.Errorf("collecting one task returned the wrong answers:\n%s", res.Content)
	}

	rest := loop.Tools.Call(ctx, "TaskCollect", json.RawMessage(`{}`), "")
	if !strings.Contains(rest.Content, "beta") {
		t.Errorf("the other task was not still outstanding:\n%s", rest.Content)
	}
}

func TestCollectingWithNothingOutstandingSaysSo(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	res := loop.Tools.Call(ctx, "TaskCollect", json.RawMessage(`{}`), "")
	if res.IsError {
		t.Errorf("collecting nothing is not an error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "no background tasks") {
		t.Errorf("got %q, want it to say there is nothing outstanding", res.Content)
	}
}

// The ceiling exists for the model that launches work it never comes back
// for. Every outstanding task is tokens being spent in a session nobody is
// reading, so hitting the limit has to say what to do rather than queue.
func TestASessionCannotLaunchUnboundedBackgroundWork(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	for i := 0; i < maxBackgroundTasks; i++ {
		if res := launch(t, loop.Tools, ctx, "explore", "say one"); res.IsError {
			t.Fatalf("launch %d: %s", i, res.Content)
		}
	}
	res := launch(t, loop.Tools, ctx, "explore", "say one more")
	if !res.IsError {
		t.Fatal("a ninth background task was accepted")
	}
	if !strings.Contains(res.Content, "collect them before launching more") {
		t.Errorf("the refusal does not say what to do: %q", res.Content)
	}
}

// Background delegation must not be the way around the depth guard the
// synchronous Task tool applies: a sub-agent that cannot call Task could
// otherwise launch one and never collect it, and nothing would be waiting
// to notice.
func TestBackgroundDelegationObeysTheDepthLimit(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := withTaskDepth(WithSessionID(context.Background(), sid), maxTaskDepth)

	res := launch(t, loop.Tools, ctx, "explore", "say deeper")
	if !res.IsError {
		t.Fatal("a task was launched past the delegation depth limit")
	}
	if !strings.Contains(res.Content, "depth") {
		t.Errorf("got %q, want it to say why", res.Content)
	}
}

// taskIDFrom pulls the id out of what TaskBackground reports.
func taskIDFrom(t *testing.T, content string) string {
	t.Helper()
	const marker = "as task "
	i := strings.Index(content, marker)
	if i < 0 {
		t.Fatalf("no task id in %q", content)
	}
	rest := content[i+len(marker):]
	if end := strings.IndexAny(rest, ". \n"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}
