package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// refuseUntilSmallServer refuses any request whose body is larger than
// limitBytes, the way a server refuses one that does not fit, and answers
// anything smaller.
//
// This is the shape the character-count estimate gets wrong. Four
// characters per token is about right for English and about four times
// too generous for Korean, so a history this server measures as far too
// long can measure comfortably small on our side — and every decision
// made from the estimate alone is then made against a number the server
// does not agree with.
func refuseUntilSmallServer(t *testing.T, limitBytes int) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		n++
		mu.Unlock()

		if len(body) > limitBytes {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your prompt contains more."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range []string{
			`{"choices":[{"delta":{"content":"a summary"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))

	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

func compactTestLoop(t *testing.T, url string) (*Loop, *session.Store) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: url},
		},
		Profiles: map[string]config.Profile{
			// A window far larger than what the server will actually
			// accept — the estimate says there is plenty of room.
			"small": {Provider: "local", Model: "small-model", ContextWindow: 200000},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "small"}},
		DefaultProfile: "small",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	return New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(url, ""),
	}, cfg), store
}

// /compact is what someone runs *because* the session is too long, so it
// failing for being too long is the one failure it cannot have. It sized
// its own request from the character-count estimate and sent it once: when
// the server disagreed — which is the normal case for Korean or Japanese,
// where that estimate runs about 4x low — the answer was "compaction
// failed: ... maximum context length", and the session had no way out
// left.
func TestManualCompactShrinksUntilItFitsInsteadOfFailing(t *testing.T) {
	srv, count := refuseUntilSmallServer(t, 4000)
	defer srv.Close()

	loop, store := compactTestLoop(t, srv.URL)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := range 20 {
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(fmt.Sprintf("메시지 %d: %s", i, strings.Repeat("한국어 ", 200)))},
		})
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Block{provider.TextBlock("네")},
		})
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	compacted, fatal := false, ""
	for _, ev := range all {
		switch ev.Type {
		case "compacted":
			compacted = true
		case "error":
			if ev.Data["recovered"] != true {
				fatal, _ = ev.Data["error"].(string)
			}
		}
	}
	if fatal != "" {
		t.Errorf("compaction reported a failure instead of shrinking until it fit: %s", fatal)
	}
	if !compacted {
		t.Error("the conversation was never compacted")
	}
	if n := count(); n < 2 {
		t.Errorf("only %d requests — the shrink-and-retry never happened", n)
	}
}

// A turn against a server that keeps refusing must trim its way down to
// something that fits, not stop after a fixed number of tries that happens
// to be too few.
//
// This is the same tokenizer disagreement as above, seen from the turn
// loop: the window says there is room, the server says there is not, and
// every attempt sized from the window alone is refused again. One summary
// plus one trim covered the case where the estimate was roughly right;
// where it is 4x out, the trim has to keep going.
func TestATurnTrimsRepeatedlyUntilTheServerAcceptsIt(t *testing.T) {
	srv, count := refuseUntilSmallServer(t, 4000)
	defer srv.Close()

	loop, store := compactTestLoop(t, srv.URL)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := range 30 {
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(fmt.Sprintf("메시지 %d: %s", i, strings.Repeat("한국어 ", 200)))},
		})
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Block{provider.TextBlock("네")},
		})
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "계속해줘"); err != nil {
		t.Fatalf("the turn gave up instead of trimming until it fit: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var text strings.Builder
	fatal := ""
	for _, ev := range all {
		switch ev.Type {
		case "message.part.delta":
			if s, ok := ev.Data["text"].(string); ok {
				text.WriteString(s)
			}
		case "error":
			if ev.Data["recovered"] != true {
				fatal, _ = ev.Data["error"].(string)
			}
		}
	}
	if fatal != "" {
		t.Errorf("an unrecovered error reached the transcript: %s", fatal)
	}
	if got := text.String(); got == "" {
		t.Error("the turn produced no answer at all")
	}
	t.Logf("took %d requests to get there", count())
}
