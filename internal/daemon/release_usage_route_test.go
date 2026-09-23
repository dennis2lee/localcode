package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/agent"
	"localcode/internal/client"
	"localcode/internal/events"
)

// What the usage window draws and what /usage all prints come from one
// function over the same logs. The window used to add the totals up
// itself from the session list, which shows neither the sessions of
// sub-agents nor where a fork's copy of another log ends, so the two gave
// different totals for the same daemon.

func usageAt(t *testing.T, url string) (agent.UsageSummary, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var s agent.UsageSummary
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return s, resp.StatusCode
}

func TestTheUsageRouteCountsWhatTheCommandCounts(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	c := client.New(srv.URL)
	ctx := context.Background()

	src, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	d.Loop.Store.Append(src.ID, events.TypeUserMessage, map[string]any{"text": "hello"})
	d.Loop.Store.Append(src.ID, events.TypeMessagePartEnd, map[string]any{"text": "hi"})
	d.Loop.Store.Append(src.ID, events.TypeUsage, map[string]any{
		"input_tokens": 12, "output_tokens": 30, "cached_input_tokens": 4224,
		"cache_read_tokens": 4096, "cache_write_tokens": 128, "model": "claude-x",
	})
	// A sub-agent's session: no list shows it, and it made a call.
	if _, err := d.Loop.Store.CreateSession("child-1", src.ID, "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	d.Loop.Store.Append("child-1", events.TypeUsage, map[string]any{"input_tokens": 50, "output_tokens": 7, "model": "claude-x"})

	// A fork copies the source's log, and the call in it was made once.
	resp, err := http.Post(srv.URL+"/api/sessions/"+src.ID+"/fork", "application/json", nil)
	if err != nil {
		t.Fatalf("POST fork: %v", err)
	}
	var fork struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&fork)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("fork status = %d", resp.StatusCode)
	}
	// And the fork then makes a call of its own, which counts.
	d.Loop.Store.Append(fork.ID, events.TypeUsage, map[string]any{"input_tokens": 3, "output_tokens": 1, "model": "claude-x"})

	summary, status := usageAt(t, srv.URL+"/api/usage")
	if status != http.StatusOK {
		t.Fatalf("GET /api/usage = %d", status)
	}
	want := agent.UsageFigures{InputTokens: 12 + 50 + 3, OutputTokens: 30 + 7 + 1, CacheReadTokens: 4096, CacheWriteTokens: 128, Calls: 3}
	if got := summary.Models["claude-x"]; got != want {
		t.Errorf("the window's totals = %+v, want %+v: every call once, the sub-agent's included, the fork's copy not", got, want)
	}
	if summary.Note == "" {
		t.Errorf("a summary with cache figures carries no note saying what they are")
	}

	// The command beside it prints the same figures.
	if err := d.Loop.SendMessage(ctx, src.ID, "general-purpose", "/usage all"); err != nil {
		t.Fatalf("/usage all: %v", err)
	}
	evs, _ := d.Loop.Store.Events(src.ID, 0)
	var report string
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == events.TypeMessagePartEnd {
			report, _ = evs[i].Data["text"].(string)
			break
		}
	}
	if line := "- claude-x: input 65 · cache read 4096 · cache write 128 · output 38 · total 4327 (3 calls)"; !strings.Contains(report, line) {
		t.Errorf("/usage all does not print the window's figures %q:\n%s", line, report)
	}

	if _, status := usageAt(t, srv.URL+"/api/usage?window=fortnight"); status != http.StatusBadRequest {
		t.Errorf("an unknown window answered %d, want 400", status)
	}

	// A conversation's own tree: itself and its sub-agent, not the fork.
	tree, status := usageAt(t, srv.URL+"/api/sessions/"+src.ID+"/usage")
	if status != http.StatusOK {
		t.Fatalf("GET session usage = %d", status)
	}
	if got := tree.Total(); got.Calls != 2 || got.InputTokens != 62 {
		t.Errorf("the conversation's tree = %+v, want its own call and its sub-agent's", got)
	}
	if _, status := usageAt(t, srv.URL+"/api/sessions/no-such-session/usage"); status != http.StatusNotFound {
		t.Errorf("an unknown session answered %d, want 404", status)
	}
}
