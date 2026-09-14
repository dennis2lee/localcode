package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// recordedModelCall is one chat request the mock model received: which
// model it was asked to run, and which tool names it was offered. Both
// are resolved per turn from the agent the turn runs as, so they say
// which agent a turn executed under without naming Loop internals.
type recordedModelCall struct {
	Model string
	Tools []string
}

// blockingModelServer answers every turn with plain text (no tool calls,
// so no permission prompts), records each request, and holds the first
// request open until release is closed. That keeps the first turn running
// while the test switches the agent and queues a second message.
func blockingModelServer(t *testing.T, mu *sync.Mutex, calls *[]recordedModelCall, release chan struct{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		call := recordedModelCall{Model: body.Model}
		for _, tool := range body.Tools {
			call.Tools = append(call.Tools, tool.Function.Name)
		}
		sort.Strings(call.Tools)
		mu.Lock()
		first := len(*calls) == 0
		*calls = append(*calls, call)
		mu.Unlock()

		if first {
			// Hold the first turn inside its model call. Unblocks when
			// the test closes release, or when the server goes away on
			// a failure path, so a failed test cannot hang in Close.
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// newTwoAgentDaemon is newTestDaemon with two agents that differ in both
// user-visible facts a turn resolves: the model profile and the tool
// allowlist. "restricted" runs model-restricted with only glob;
// "full" runs model-full with the whole registry.
func newTwoAgentDaemon(t *testing.T, modelURL string) *Daemon {
	t.Helper()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)

	broker := agent.NewPermissionBroker(store)
	registry := tools.NewRegistry(broker.Func())
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Glob{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"p-restricted": {Provider: "local", Model: "model-restricted"},
			"p-full":       {Provider: "local", Model: "model-full"},
		},
		Agents: map[string]config.AgentConfig{
			"restricted": {Profile: "p-restricted", Tools: []string{"glob"}},
			"full":       {Profile: "p-full"},
		},
		DefaultProfile:     "p-restricted",
		MaxConcurrentTasks: 2,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	providers := map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}
	loop := agent.New(store, registry, providers, cfg)
	tasks := agent.NewTaskManager(context.Background(), loop, cfg.MaxConcurrentTasks)

	return New(loop, broker, tasks, nil, nil, "test-version")
}

func postJSON(t *testing.T, url string, payload any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	var req *http.Request
	if payload != nil {
		req, _ = http.NewRequest(http.MethodPost, url, reader)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(http.MethodPost, url, reader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response from %s: %v", url, err)
	}
	return resp.StatusCode, out
}

// TestAgentSwitchMidTurnQueuedMessage pins the defect in
// handleSendMessage (internal/daemon/turns.go): the handler reads the
// session once, and the turn goroutine reuses that captured agent for
// every chained turn, including a message queued via inject after an
// agent switch.
//
// Sequence: session on "restricted" starts a turn (held inside its model
// call); the user switches to "full" (succeeds, clients show "full");
// the next message is injected into the running turn; when the first
// turn ends, finishOrTake hands that text back and it runs as a new turn.
// That new turn must run as "full". While the defect stands it runs as
// "restricted": model-restricted with only glob on offer.
func TestAgentSwitchMidTurnQueuedMessage(t *testing.T) {
	var mu sync.Mutex
	var calls []recordedModelCall
	release := make(chan struct{})
	model := blockingModelServer(t, &mu, &calls, release)
	defer model.Close()

	d := newTwoAgentDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	sess, err := func() (session.Session, error) {
		var out session.Session
		code, body := postJSON(t, httpSrv.URL+"/api/sessions", map[string]string{"agent": "restricted"})
		if code != http.StatusOK && code != http.StatusCreated {
			return out, fmt.Errorf("create session: status %d (%v)", code, body)
		}
		raw, _ := json.Marshal(body)
		if err := json.Unmarshal(raw, &out); err != nil {
			return out, err
		}
		return out, nil
	}()
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// First message starts a turn; the handler answers 202 at once and
	// the turn blocks inside its first model call.
	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/messages", map[string]string{"text": "first"}); code != http.StatusAccepted {
		t.Fatalf("first message: status %d (%v), want 202", code, body)
	}

	// Wait until the first turn is inside its model call: from here on a
	// second message cannot start its own turn and must be injected.
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(calls)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first turn to reach the model")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Mid-turn switch. This succeeds and every client now shows "full".
	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/agent", map[string]string{"agent": "full"}); code != http.StatusOK {
		t.Fatalf("switch agent: status %d (%v), want 200", code, body)
	}

	// Second message while the turn is still running. "injected" proves
	// it landed in pending and will run as its own turn via finishOrTake
	// rather than starting fresh (which would re-read the agent).
	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/messages", map[string]string{"text": "second"}); code != http.StatusAccepted || body["status"] != "injected" {
		t.Fatalf("second message: status %d (%v), want 202 injected", code, body)
	}

	close(release)

	// Wait for the queued turn to reach the model and for the turn
	// boundary to be recorded.
	deadline = time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(calls)
		mu.Unlock()
		all, err := d.Loop.Store.Events(sess.ID, 0)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		done := false
		for _, ev := range all {
			if ev.Type == events.TypeTurnDone {
				done = true
			}
		}
		if n >= 2 && done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the queued turn (model calls=%d, turnDone=%v)", n, done)
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	second := calls[1]
	mu.Unlock()
	got, err := d.Loop.Store.Get(sess.ID)
	if err != nil {
		t.Fatalf("re-fetch session: %v", err)
	}
	if got.Agent != "full" {
		t.Fatalf("session agent = %q, want full (the switch itself failed)", got.Agent)
	}
	if second.Model != "model-full" {
		t.Errorf("queued turn ran on model %q, want model-full: it kept the pre-switch agent %q shows", second.Model, got.Agent)
	}
	for _, tool := range second.Tools {
		if tool == "write_file" {
			return
		}
	}
	t.Errorf("queued turn was offered tools %v, want write_file among them: it kept the restricted allowlist [glob]", second.Tools)
}

// createSessionOnAgent opens a session on the named agent through the real
// HTTP surface, the same opening every agent-switch test below needs.
func createSessionOnAgent(t *testing.T, baseURL, agentName string) session.Session {
	t.Helper()
	var sess session.Session
	code, body := postJSON(t, baseURL+"/api/sessions", map[string]string{"agent": agentName})
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("create session: status %d (%v)", code, body)
	}
	raw, _ := json.Marshal(body)
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess
}

// waitForModelCalls blocks until the mock model has seen n requests.
func waitForModelCalls(t *testing.T, mu *sync.Mutex, calls *[]recordedModelCall, n int, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		got := len(*calls)
		mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s (model calls=%d, want %d)", what, got, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForTurnDone blocks until the session log holds a turn boundary.
func waitForTurnDone(t *testing.T, d *Daemon, sessID, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		all, err := d.Loop.Store.Events(sessID, 0)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		for _, ev := range all {
			if ev.Type == events.TypeTurnDone {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s (no turn.done)", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestQueuedFollowUpWithoutSwitchRunsAsTheSessionAgent is the ordinary case
// the fix must not change: with no switch anywhere, a message injected
// behind a running turn still runs as the session's agent, and the two
// messages are still answered as two turns (two model calls).
func TestQueuedFollowUpWithoutSwitchRunsAsTheSessionAgent(t *testing.T) {
	var mu sync.Mutex
	var calls []recordedModelCall
	release := make(chan struct{})
	model := blockingModelServer(t, &mu, &calls, release)
	defer model.Close()

	d := newTwoAgentDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	sess := createSessionOnAgent(t, httpSrv.URL, "restricted")

	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/messages", map[string]string{"text": "first"}); code != http.StatusAccepted {
		t.Fatalf("first message: status %d (%v), want 202", code, body)
	}
	waitForModelCalls(t, &mu, &calls, 1, "the first turn to reach the model")

	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/messages", map[string]string{"text": "second"}); code != http.StatusAccepted || body["status"] != "injected" {
		t.Fatalf("second message: status %d (%v), want 202 injected", code, body)
	}

	close(release)
	waitForModelCalls(t, &mu, &calls, 2, "the queued follow-up to reach the model")
	waitForTurnDone(t, d, sess.ID, "the chained turns to finish")

	mu.Lock()
	defer mu.Unlock()
	for i, call := range calls {
		if call.Model != "model-restricted" {
			t.Errorf("turn %d ran on model %q, want model-restricted: nothing switched", i+1, call.Model)
		}
		if len(call.Tools) != 1 || call.Tools[0] != "glob" {
			t.Errorf("turn %d was offered tools %v, want [glob]: nothing switched", i+1, call.Tools)
		}
	}
	if got, err := d.Loop.Store.Get(sess.ID); err != nil {
		t.Fatalf("re-fetch session: %v", err)
	} else if got.Agent != "restricted" {
		t.Errorf("session agent = %q, want restricted: nothing switched", got.Agent)
	}
}

// twoGateModelServer is blockingModelServer with a gate per turn: the first
// turn stays inside its model call until gate1 closes, the second until
// gate2 closes, later turns answer at once. Two gates make the switch
// between two chained turns deterministic: the test moves the agent while
// the second turn is provably still running.
func twoGateModelServer(t *testing.T, mu *sync.Mutex, calls *[]recordedModelCall, gate1, gate2 chan struct{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		call := recordedModelCall{Model: body.Model}
		for _, tool := range body.Tools {
			call.Tools = append(call.Tools, tool.Function.Name)
		}
		sort.Strings(call.Tools)
		mu.Lock()
		*calls = append(*calls, call)
		idx := len(*calls) - 1
		mu.Unlock()

		if idx < 2 {
			gate := gate2
			if idx == 0 {
				gate = gate1
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// TestEachQueuedTurnRunsAsTheAgentCurrentWhenItRuns pins that the agent is
// resolved per turn, not per message and not once for the chain. Both queued
// messages are posted while the session shows "restricted"; the first runs
// after a switch to "full" and must run as "full", and the second runs after
// a switch back while it was still queued and must run as "restricted"
// again. Under the defect both chained turns keep the entry agent, so the
// first of the two runs restricted and the test fails on its model.
func TestEachQueuedTurnRunsAsTheAgentCurrentWhenItRuns(t *testing.T) {
	var mu sync.Mutex
	var calls []recordedModelCall
	gate1 := make(chan struct{})
	gate2 := make(chan struct{})
	model := twoGateModelServer(t, &mu, &calls, gate1, gate2)
	defer model.Close()

	d := newTwoAgentDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	sess := createSessionOnAgent(t, httpSrv.URL, "restricted")

	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/messages", map[string]string{"text": "first"}); code != http.StatusAccepted {
		t.Fatalf("first message: status %d (%v), want 202", code, body)
	}
	waitForModelCalls(t, &mu, &calls, 1, "the first turn to reach the model")

	for _, text := range []string{"second", "third"} {
		if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/messages", map[string]string{"text": text}); code != http.StatusAccepted || body["status"] != "injected" {
			t.Fatalf("message %q: status %d (%v), want 202 injected", text, code, body)
		}
	}

	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/agent", map[string]string{"agent": "full"}); code != http.StatusOK {
		t.Fatalf("switch to full: status %d (%v), want 200", code, body)
	}

	close(gate1)
	waitForModelCalls(t, &mu, &calls, 2, "the second turn to reach the model")

	mu.Lock()
	second := calls[1]
	mu.Unlock()
	if second.Model != "model-full" {
		t.Errorf("second turn ran on model %q, want model-full: it kept the agent current when posted, not when it ran", second.Model)
	}

	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/agent", map[string]string{"agent": "restricted"}); code != http.StatusOK {
		t.Fatalf("switch back to restricted: status %d (%v), want 200", code, body)
	}

	close(gate2)
	waitForModelCalls(t, &mu, &calls, 3, "the third turn to reach the model")
	waitForTurnDone(t, d, sess.ID, "the chained turns to finish")

	mu.Lock()
	third := calls[2]
	mu.Unlock()
	if third.Model != "model-restricted" {
		t.Errorf("third turn ran on model %q, want model-restricted: it kept the agent of the previous turn, not the current one", third.Model)
	}
	if len(third.Tools) != 1 || third.Tools[0] != "glob" {
		t.Errorf("third turn was offered tools %v, want [glob]", third.Tools)
	}
	if got, err := d.Loop.Store.Get(sess.ID); err != nil {
		t.Fatalf("re-fetch session: %v", err)
	} else if got.Agent != "restricted" {
		t.Errorf("session agent = %q, want restricted", got.Agent)
	}
}

// TestQueuedTurnStillCompletesWhenTheAgentIsGone covers the degraded read:
// the session names an agent the config no longer holds (a switch followed
// by the agent's removal). The turn must still complete — the queued message
// gets a visible answer — rather than dying on a name nothing resolves.
//
// The unknown name is written through the store because the HTTP switch
// endpoint validates against the live roster and would refuse it; that
// refusal is correct at the API, and this test starts past it, where the
// running turn has to cope with what the record says.
func TestQueuedTurnStillCompletesWhenTheAgentIsGone(t *testing.T) {
	var mu sync.Mutex
	var calls []recordedModelCall
	release := make(chan struct{})
	model := blockingModelServer(t, &mu, &calls, release)
	defer model.Close()

	d := newTwoAgentDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	sess := createSessionOnAgent(t, httpSrv.URL, "restricted")

	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/messages", map[string]string{"text": "first"}); code != http.StatusAccepted {
		t.Fatalf("first message: status %d (%v), want 202", code, body)
	}
	waitForModelCalls(t, &mu, &calls, 1, "the first turn to reach the model")

	if _, err := d.Loop.Store.SetAgent(sess.ID, "ghost"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}

	if code, body := postJSON(t, httpSrv.URL+"/api/sessions/"+sess.ID+"/messages", map[string]string{"text": "second"}); code != http.StatusAccepted || body["status"] != "injected" {
		t.Fatalf("second message: status %d (%v), want 202 injected", code, body)
	}

	close(release)
	waitForModelCalls(t, &mu, &calls, 2, "the queued turn to reach the model")

	deadline := time.Now().Add(15 * time.Second)
	for {
		all, err := d.Loop.Store.Events(sess.ID, 0)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		replies := 0
		for _, ev := range all {
			if ev.Type == events.TypeMessagePartEnd {
				replies++
			}
		}
		if replies >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the queued turn never answered (model calls=%d, replies=%d): an unresolvable agent killed the turn", len(calls), replies)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got, err := d.Loop.Store.Get(sess.ID); err != nil {
		t.Fatalf("re-fetch session: %v", err)
	} else if got.Agent != "ghost" {
		t.Errorf("session agent = %q, want ghost: the fallback rewrote the record", got.Agent)
	}
}
