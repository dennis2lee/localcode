package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// Contention lives at the endpoint, not in one global integer.
//
// The daemon-wide max_concurrent_tasks bounds background tasks across every
// session, which is the wrong shape in both directions at once: one local
// model on one GPU serves one request at a time whatever that number says,
// and a hosted provider on the same machine is held to the same small
// number because the local one needed it small.

// laneServer counts how many requests are inside it at once and holds each
// one long enough for an overlap to be observable.
func laneServer(t *testing.T, hold time.Duration) (*httptest.Server, *int32, *int32) {
	t.Helper()
	var live, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&live, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
				break
			}
		}
		time.Sleep(hold)
		atomic.AddInt32(&live, -1)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)
	return srv, &live, &peak
}

// laneLoop wires a daemon with two providers, each with its own endpoint,
// and one agent pointed at each.
func laneLoop(t *testing.T, localURL, hostedURL string, localCap, globalCap int) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local":  {Type: config.ProviderOpenAICompat, BaseURL: localURL, MaxConcurrentTasks: localCap},
			"hosted": {Type: config.ProviderOpenAICompat, BaseURL: hostedURL},
		},
		Profiles: map[string]config.Profile{
			"onlocal":  {Provider: "local", Model: "qwen3-30b"},
			"onhosted": {Provider: "hosted", Model: "claude-sonnet-5"},
		},
		Agents: map[string]config.AgentConfig{
			"slow": {Profile: "onlocal"},
			"fast": {Profile: "onhosted"},
		},
		DefaultProfile:     "onlocal",
		MaxConcurrentTasks: globalCap,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local":  provider.NewOpenAICompat(localURL, ""),
		"hosted": provider.NewOpenAICompat(hostedURL, ""),
	}, cfg)
	NewTaskManager(context.Background(), loop, globalCap)
	return loop
}

// One local endpoint, three background tasks aimed at it, a lane of one.
func TestAProviderLaneBoundsItsOwnEndpoint(t *testing.T) {
	local, _, peak := laneServer(t, 80*time.Millisecond)
	hosted, _, _ := laneServer(t, 10*time.Millisecond)
	loop := laneLoop(t, local.URL, hosted.URL, 1, 8)
	if _, err := loop.Store.CreateSession("p", "", "slow", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var ids []string
	for range 3 {
		id, err := loop.Tasks.SpawnBackground(context.Background(), "p", "slow", "work", "")
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if _, err, _ := loop.Tasks.Wait(context.Background(), id); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}

	if got := atomic.LoadInt32(peak); got != 1 {
		t.Errorf("%d requests were inside the local endpoint at once, want 1", got)
	}
}

// The acquisition order, which is the whole reason there are two bounds.
//
// The lane is taken before the global slot, so a task queued on a busy
// endpoint is not sitting on a slot that another provider's task could
// use. With the order reversed, the three local tasks take every global
// slot and the hosted task waits behind an endpoint it does not use.
func TestATaskWaitingOnABusyEndpointDoesNotHoldAGlobalSlot(t *testing.T) {
	local, _, _ := laneServer(t, 300*time.Millisecond)
	hosted, _, _ := laneServer(t, 10*time.Millisecond)
	// Three global slots, three local tasks, a local lane of one. Under
	// lane-then-global the local tasks hold one global slot between them
	// and two stay free; under global-then-lane they hold all three.
	loop := laneLoop(t, local.URL, hosted.URL, 1, 3)
	if _, err := loop.Store.CreateSession("p", "", "slow", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	for range 3 {
		if _, err := loop.Tasks.SpawnBackground(context.Background(), "p", "slow", "work", ""); err != nil {
			t.Fatalf("spawn local: %v", err)
		}
	}
	// Let them all reach their wait.
	time.Sleep(60 * time.Millisecond)

	hostedID, err := loop.Tasks.SpawnBackground(context.Background(), "p", "fast", "work", "")
	if err != nil {
		t.Fatalf("spawn hosted: %v", err)
	}
	started := time.Now()
	if _, err, _ := loop.Tasks.Wait(context.Background(), hostedID); err != nil {
		t.Fatalf("wait hosted: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Errorf("the hosted task waited %v behind an endpoint it does not use", elapsed)
	}
}

// A provider that says nothing is bounded exactly as it was before the
// field existed.
func TestAProviderWithNoLimitIsUnchanged(t *testing.T) {
	hosted, _, peak := laneServer(t, 60*time.Millisecond)
	loop := laneLoop(t, hosted.URL, hosted.URL, 0, 4)
	if _, err := loop.Store.CreateSession("p", "", "fast", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var ids []string
	for range 3 {
		id, err := loop.Tasks.SpawnBackground(context.Background(), "p", "fast", "work", "")
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		loop.Tasks.Wait(context.Background(), id)
	}
	if got := atomic.LoadInt32(peak); got < 2 {
		t.Errorf("peak concurrency %d: an unlimited provider was serialised", got)
	}
}

// Esc while queued on a lane ends the task rather than leaving it in line
// for work nobody is waiting for.
func TestTakingALaneRespectsCancellation(t *testing.T) {
	l := lanes{"busy": make(chan struct{}, 1)}
	release, ok := l.take(context.Background(), "busy")
	if !ok {
		t.Fatal("the first take was refused")
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	var got bool
	go func() { defer wg.Done(); _, got = l.take(ctx, "busy") }()
	cancel()
	wg.Wait()
	if got {
		t.Error("a cancelled take was granted a lane slot")
	}

	// And a provider with no lane never blocks.
	if _, ok := l.take(context.Background(), "nolane"); !ok {
		t.Error("a provider with no lane was refused")
	}
}

func TestAProviderConcurrencyOutsideItsRangeIsRefusedAtLoad(t *testing.T) {
	for _, n := range []int{-1, 65} {
		cfg := &config.Config{
			Providers: map[string]config.ProviderConfig{
				"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://x", MaxConcurrentTasks: n},
			},
			Profiles:       map[string]config.Profile{"p": {Provider: "local", Model: "m"}},
			DefaultProfile: "p",
		}
		err := cfg.Validate()
		if err == nil {
			t.Errorf("max_concurrent_tasks %d was accepted", n)
			continue
		}
		if !strings.Contains(err.Error(), `provider "local"`) {
			t.Errorf("error %q does not name the provider", err)
		}
	}
}
