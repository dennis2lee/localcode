package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// What a run reports: its answer once the turn has ended, and its usage
// counting every call it made, sub-agents' included, whether the run went
// through a daemon or ran in this process.

// runModel is an OpenAI-compatible model that answers the way a run with
// a delegation goes: the parent asks for a sub-agent, the sub-agent
// answers, and the parent answers. delay holds the parent's first answer
// back; tool names the delegation (Task, or TaskBackground for one that
// runs on after the parent's turn); fail refuses every request.
type runModel struct {
	delay time.Duration
	tool  string
	fail  bool

	mu         sync.Mutex
	calls      int
	parentDone chan struct{}
	closeOnce  sync.Once
}

func (m *runModel) asked() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *runModel) server(t *testing.T) *httptest.Server {
	t.Helper()
	m.parentDone = make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// A model call, not the daemon asking the server how large
		// its window is.
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		m.mu.Lock()
		m.calls++
		m.mu.Unlock()
		if m.fail {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"this model is not available"}}`)
			return
		}
		var hasTool, delegated bool
		for _, msg := range body.Messages {
			if msg.Role == "tool" {
				hasTool = true
			}
			if s, _ := json.Marshal(msg.Content); strings.Contains(string(s), "DELEGATED-WORK") {
				delegated = true
			}
		}
		first := ""
		for _, msg := range body.Messages {
			if msg.Role == "user" {
				first = fmt.Sprint(msg.Content)
				break
			}
		}
		send := func(chunks ...string) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, c := range chunks {
				fmt.Fprint(w, "data: "+c+"\n\n")
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			w.(http.Flusher).Flush()
		}
		switch {
		case m.tool == "":
			time.Sleep(m.delay)
			send(`{"choices":[{"delta":{"content":"the answer"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3}}`)
		case hasTool && strings.Contains(first, "please delegate"):
			send(`{"choices":[{"delta":{"content":"final answer"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":300,"completion_tokens":3}}`)
			m.closeOnce.Do(func() { close(m.parentDone) })
		case delegated && !strings.Contains(first, "please delegate"):
			if m.tool == "TaskBackground" {
				<-m.parentDone
				time.Sleep(300 * time.Millisecond)
			}
			send(`{"choices":[{"delta":{"content":"child answer"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":7}}`)
		default:
			time.Sleep(m.delay)
			args, _ := json.Marshal(map[string]string{"agent": "helper", "prompt": "DELEGATED-WORK: look it up"})
			argsField, _ := json.Marshal(string(args))
			send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"`+m.tool+`","arguments":`+string(argsField)+`}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":5}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runModelHome writes a config with a helper agent to delegate to.
func runModelHome(t *testing.T, modelURL string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{
	  "providers": {"local": {"type": "openai-compat", "base_url": %q}},
	  "profiles": {"balanced": {"provider": "local", "model": "m"}},
	  "default_profile": "balanced",
	  "smart_agent": true,
	  "agents": {
	    "general-purpose": {"profile": "balanced"},
	    "helper": {"profile": "balanced", "description": "Looks things up."}
	  }
	}`, modelURL+"/v1")
	if err := os.WriteFile(filepath.Join(home, ".localcode", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)
}

type runReport struct {
	Result string         `json:"result"`
	Usage  map[string]int `json:"usage"`
	Error  string         `json:"error"`
}

// throughATestDaemon runs the prompt the way "localcode run --session"
// does when a daemon is listening.
func throughATestDaemon(t *testing.T, prompt string) (runReport, error) {
	t.Helper()
	d, stop, err := buildDaemon(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("buildDaemon: %v", err)
	}
	t.Cleanup(stop)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	// Cancelled before the server closes: the event stream holds its
	// connection open until the client goes away.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	runErr := throughDaemon(ctx, runOptions{format: formatJSON, agent: "general-purpose", session: true}, srv.URL, prompt, &out)
	var got runReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, out.String())
	}
	return got, runErr
}

// The daemon takes the turn with 202 at once and runs it after. The run
// finished two seconds after the 202, so a model slower than that gave an
// empty result, no usage, and exit 0.
func TestARunThroughADaemonWaitsForItsTurn(t *testing.T) {
	m := &runModel{delay: 2500 * time.Millisecond}
	runModelHome(t, m.server(t).URL)
	got, err := throughATestDaemon(t, "what is this?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.Result != "the answer" {
		t.Errorf("result = %q, want the answer the model gave after 2.5s", got.Result)
	}
	if got.Usage["calls"] != 1 || got.Usage["input_tokens"] != 11 {
		t.Errorf("usage = %v, want the one call", got.Usage)
	}
}

// Through a daemon, a run's usage is its conversation's and its
// sub-agent's, read from the daemon's logs.
func TestARunThroughADaemonCountsItsSubAgents(t *testing.T) {
	m := &runModel{tool: "Task"}
	runModelHome(t, m.server(t).URL)
	got, err := throughATestDaemon(t, "please delegate this")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := m.asked(); n != 3 {
		t.Fatalf("the model was asked %d times, want 3", n)
	}
	if got.Usage["input_tokens"] != 450 || got.Usage["output_tokens"] != 15 || got.Usage["calls"] != 3 {
		t.Errorf("usage = %v, want the parent's two calls and the sub-agent's (450 in, 15 out, 3 calls)", got.Usage)
	}
}

// A turn that failed on the daemon fails the run, as it does in this
// process, rather than exiting 0 with the failure in the JSON.
func TestARunThroughADaemonFailsWithItsTurn(t *testing.T) {
	m := &runModel{fail: true}
	runModelHome(t, m.server(t).URL)
	got, err := throughATestDaemon(t, "what is this?")
	if err == nil {
		t.Fatalf("a turn the model refused ended the run without an error: %+v", got)
	}
	if got.Error == "" {
		t.Errorf("the report does not say what failed: %+v", got)
	}
}

// A background sub-agent runs on after the turn that started it, and the
// run waits for it before it exits. It waited after reporting, so the
// report's usage, which says it is the whole run, left its calls out.
func TestARunCountsABackgroundSubAgent(t *testing.T) {
	m := &runModel{tool: "TaskBackground"}
	runModelHome(t, m.server(t).URL)
	out, err := doRun(t, runOptions{format: formatJSON, agent: "general-purpose", skip: true, bare: true}, "please delegate this")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var got runReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, out)
	}
	if n := m.asked(); got.Usage["calls"] != n {
		t.Errorf("usage counts %d calls, the model was asked %d times: %v", got.Usage["calls"], n, got.Usage)
	}
}

// Through a daemon, a background sub-agent runs on after the turn ends,
// and the run waits for it as one in this process does, or its calls
// are missing from a usage figure that says it is the whole run.
func TestARunThroughADaemonWaitsForItsBackgroundSubAgents(t *testing.T) {
	m := &runModel{tool: "TaskBackground"}
	runModelHome(t, m.server(t).URL)
	got, err := throughATestDaemon(t, "please delegate this")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := m.asked(); got.Usage["calls"] != n || n != 3 {
		t.Errorf("usage counts %d calls, the model was asked %d times (want 3): %v", got.Usage["calls"], n, got.Usage)
	}
}
