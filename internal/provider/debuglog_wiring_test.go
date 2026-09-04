package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"localcode/internal/debuglog"
)

// Reported: the log holds what the model said and not what was sent. This
// goes through the provider's own Chat rather than through the transport
// in isolation, because that is the path the report is about.
func TestTheLogHoldsTheRequestTheProviderSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	sink, err := debuglog.Create(t.TempDir(), "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx := debuglog.With(context.Background(), sink)

	p := NewOpenAICompat(srv.URL, "")
	events, err := p.Chat(ctx, ChatRequest{
		Model:     "muse-glimmer-30b",
		System:    "you are a coding agent",
		MaxTokens: 128,
		Messages:  []Message{{Role: RoleUser, Content: []Block{TextBlock("count to three")}}},
		Tools:     []Tool{{Name: "read_file", Description: "Read a file.", InputSchema: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	sink.Close()

	body, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	log := string(body)

	// The request: the line, the headers, and every part of the body the
	// model was actually sent.
	for _, want := range []string{
		"POST " + srv.URL + "/chat/completions",
		"Content-Type: application/json",
		`"model":"muse-glimmer-30b"`,
		"count to three",
		"you are a coding agent",
		"read_file",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log is missing %q from the request:\n%s", want, log)
		}
	}
	// And the answer, so a passing test cannot be one that logged nothing.
	if !strings.Contains(log, `"content":"hi"`) {
		t.Errorf("the log is missing the response:\n%s", log)
	}
	// The request has to come before the response it is a request for.
	if strings.Index(log, "count to three") > strings.Index(log, `"content":"hi"`) {
		t.Error("the request is logged after its own response")
	}
}
