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

// One prompt, one answer, no window.
//
// These drive oneShot() end to end against a fake model server, because
// the whole point of the mode is what comes out of the pipe — a test of
// the pieces would not have caught a format that prints nothing.

// fakeModel answers every request the same way and records what it was
// asked, which is how the --model and --bare tests see what actually went
// out rather than what was meant to.
type fakeModel struct {
	mu       sync.Mutex
	model    string
	system   string
	requests int
	fail     bool
}

func (f *fakeModel) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		blob, _ := json.Marshal(body.Messages)

		f.mu.Lock()
		f.model = body.Model
		f.system = string(blob)
		f.requests++
		fail := f.fail
		f.mu.Unlock()

		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"the answer"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"},{}],"usage":{"prompt_tokens":11,"completion_tokens":3}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeModel) sawModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.model
}

func (f *fakeModel) sawSystem() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.system
}

// runHome lays down a config and a project with an AGENTS.md in it, and
// points HOME and the working directory at them, so a run in this test is
// a run in a project of its own.
func runHome(t *testing.T, modelURL string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{
	  "providers": {"local": {"type": "openai-compat", "base_url": %q}},
	  "profiles": {
	    "balanced": {"provider": "local", "model": "the-default-model"},
	    "fast": {"provider": "local", "model": "the-fast-model"}
	  },
	  "default_profile": "balanced",
	  "agents": {"general-purpose": {"profile": "balanced"}}
	}`, modelURL+"/v1")
	if err := os.WriteFile(filepath.Join(home, ".localcode", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("# Rules\nAlways mention the marmalade.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(work)
	return home
}

func doRun(t *testing.T, o runOptions, prompt string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := oneShot(context.Background(), o, prompt, &out)
	return out.String(), err
}

// The whole of it: a prompt in, an answer out, and the process is done.
func TestRunAnswersOnePromptAsPlainText(t *testing.T) {
	f := &fakeModel{}
	runHome(t, f.server(t).URL)

	out, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "what is this?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(out) != "the answer" {
		t.Errorf("stdout = %q, want just the answer", out)
	}
}

// A harness parses one object; the text format is for a person.
func TestRunReportsOneJSONObject(t *testing.T) {
	f := &fakeModel{}
	runHome(t, f.server(t).URL)

	out, err := doRun(t, runOptions{format: formatJSON, agent: "general-purpose"}, "what is this?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var got struct {
		SessionID string `json:"session_id"`
		Result    string `json:"result"`
		Usage     struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, out)
	}
	if got.Result != "the answer" {
		t.Errorf("result = %q", got.Result)
	}
	if got.Usage.InputTokens != 11 || got.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v, want what the model reported", got.Usage)
	}
	if got.Error != "" {
		t.Errorf("error = %q on a run that worked", got.Error)
	}
}

// stream-json is the event stream itself, one object per line — not a new
// schema, so a harness written against it is written against the thing
// every other client reads.
func TestRunStreamsTheEventsAsJSONLines(t *testing.T) {
	f := &fakeModel{}
	runHome(t, f.server(t).URL)

	out, err := doRun(t, runOptions{format: formatStreamJSON, agent: "general-purpose"}, "what is this?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line is not JSON: %v\n%s", err, line)
		}
		types = append(types, ev.Type)
	}
	for _, want := range []string{"message.user", "message.part.delta", "message.part.end"} {
		if !contains(types, want) {
			t.Errorf("stream has no %q event: %v", want, types)
		}
	}
}

// --profile picks which profile answers, and --model overrides the model
// inside it. Checked against what the server was actually sent.
func TestRunPicksTheProfileAndTheModel(t *testing.T) {
	for _, tt := range []struct {
		o    runOptions
		want string
	}{
		{runOptions{agent: "general-purpose"}, "the-default-model"},
		{runOptions{agent: "general-purpose", profile: "fast"}, "the-fast-model"},
		{runOptions{agent: "general-purpose", model: "qwen3:32b"}, "qwen3:32b"},
		{runOptions{agent: "general-purpose", profile: "fast", model: "qwen3:32b"}, "qwen3:32b"},
	} {
		f := &fakeModel{}
		runHome(t, f.server(t).URL)
		tt.o.format = formatText
		if _, err := doRun(t, tt.o, "x"); err != nil {
			t.Fatalf("run: %v", err)
		}
		if got := f.sawModel(); got != tt.want {
			t.Errorf("profile=%q model=%q sent %q, want %q", tt.o.profile, tt.o.model, got, tt.want)
		}
	}
}

// The name is worth saying rather than falling back to a default: a
// benchmark run against the wrong model is a result nobody can tell is
// wrong.
func TestRunRefusesAProfileOrAgentThatIsNotConfigured(t *testing.T) {
	f := &fakeModel{}
	runHome(t, f.server(t).URL)

	if _, err := doRun(t, runOptions{format: formatText, agent: "nope"}, "x"); err == nil {
		t.Error("an agent that is not in the config was accepted")
	}
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose", profile: "nope"}, "x"); err == nil {
		t.Error("a profile that is not in the config was accepted")
	}
}

// --bare is what makes a comparison fair: without it the project's own
// AGENTS.md is in the system prompt, which is real context in ordinary use
// and an unfair advantage against a tool that was not given it.
func TestBareLeavesTheProjectRulesOut(t *testing.T) {
	f := &fakeModel{}
	runHome(t, f.server(t).URL)
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.sawSystem(), "marmalade") {
		t.Fatal("the project's AGENTS.md did not reach the model at all, so --bare proves nothing")
	}

	g := &fakeModel{}
	runHome(t, g.server(t).URL)
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose", bare: true}, "x"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(g.sawSystem(), "marmalade") {
		t.Error("--bare still sent the project's AGENTS.md")
	}
}

// A one-shot leaves nothing in the session list, and --bare goes further
// and creates no directories at all — which is what makes it safe to run a
// thousand times in a row.
func TestARunLeavesNothingBehind(t *testing.T) {
	f := &fakeModel{}
	home := runHome(t, f.server(t).URL)
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose", bare: true}, "x"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"sessions", "projects"} {
		if _, err := os.Stat(filepath.Join(home, ".localcode", dir)); err == nil {
			t.Errorf("--bare created ~/.localcode/%s", dir)
		}
	}
}

// A failed run has to be a failed process, or a script cannot tell.
func TestAFailedRunIsAnError(t *testing.T) {
	f := &fakeModel{fail: true}
	runHome(t, f.server(t).URL)

	out, err := doRun(t, runOptions{format: formatJSON, agent: "general-purpose"}, "x")
	if err == nil {
		t.Error("a run whose model call failed reported success")
	}
	// And the json format still says what happened, rather than printing
	// an empty result and leaving the reason on stderr only.
	if !strings.Contains(out, `"error"`) {
		t.Errorf("json output carries no error:\n%s", out)
	}
}

// The prompt is allowed to come from stdin, which is how a prompt with
// newlines and quotes in it survives the trip.
func TestThePromptCanComeFromStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = prev })
	go func() {
		fmt.Fprint(w, "  a prompt\nwith two lines  ")
		w.Close()
	}()

	got, err := readPrompt(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "a prompt\nwith two lines" {
		t.Errorf("prompt = %q", got)
	}
}

func TestAnEmptyPromptIsRefused(t *testing.T) {
	r, w, _ := os.Pipe()
	prev := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = prev })
	w.Close()

	if _, err := readPrompt(nil); err == nil {
		t.Error("an empty prompt was accepted")
	}
}

// Nobody is watching a pipe, so a tool that needs permission is refused at
// once rather than after the scheduler's five minutes.
func TestAnUnattendedRunDoesNotWaitForAnAnswerNobodyWillGive(t *testing.T) {
	f := &fakeModel{}
	runHome(t, f.server(t).URL)

	start := time.Now()
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "x"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("the run took %v, which is the shape of waiting on a permission answer", d)
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
