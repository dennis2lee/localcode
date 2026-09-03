package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
)

// doctorFake is a vLLM as "/llm-doctor" sees it: the model list, the
// version, a metrics page, and a chat endpoint that answers each canary
// the right way — or, when told to, the wrong way a broken server does.
type doctorFake struct {
	mu          sync.Mutex
	maxLen      int
	version     string
	cacheDtype  string
	fingerprint string
	toolAsText  bool
	okReply     string
	// reasoningOnly answers every canary the way a reasoning model does
	// when its budget runs out: nothing in content, everything in
	// reasoning_content, finish_reason "length".
	reasoningOnly bool
	// flipCodeFix corrects the function every other time, which is a
	// server that has not decided what it thinks.
	flipCodeFix bool
	chats       int
	// what the last chat request carried, for the tests that care how a
	// canary is sent rather than what comes back
	sawSystem string
	sawTemp   float64
	sawTopP   float64
	sawTopK   int
	sawAuth   string
}

func (f *doctorFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/models":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"id": "google/gemma-3-27b-it", "object": "model", "max_model_len": f.maxLen},
			}})
		case "/version":
			json.NewEncoder(w).Encode(map[string]string{"version": f.version})
		case "/metrics":
			w.Write([]byte("# HELP vllm:num_preemptions_total Cumulative number of preemption from the engine.\n" +
				"vllm:num_preemptions_total{model_name=\"google/gemma-3-27b-it\"} 3.0\n" +
				"vllm:gpu_cache_usage_perc{model_name=\"google/gemma-3-27b-it\"} 0.12\n" +
				"vllm:request_success_total{finished_reason=\"stop\",model_name=\"google/gemma-3-27b-it\"} 40.0\n" +
				"vllm:request_success_total{finished_reason=\"length\",model_name=\"google/gemma-3-27b-it\"} 2.0\n" +
				"vllm:cache_config_info{block_size=\"16\",cache_dtype=\"" + f.cacheDtype + "\"} 1.0\n"))
		case "/chat/completions":
			f.chats++
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
				Tools       []any    `json:"tools"`
				Temperature *float64 `json:"temperature"`
				TopP        float64  `json:"top_p"`
				TopK        int      `json:"top_k"`
				Stream      bool     `json:"stream"`
				Seed        int      `json:"seed"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if req.Temperature == nil || req.Stream || req.Seed == 0 {
				http.Error(w, "a canary must carry a temperature and a seed, and must not be streamed", 400)
				return
			}
			f.sawTemp, f.sawTopP, f.sawTopK = *req.Temperature, req.TopP, req.TopK
			f.sawAuth = r.Header.Get("Authorization")
			f.sawSystem = ""
			if req.Messages[0].Role == "system" {
				f.sawSystem = req.Messages[0].Content
			}
			user := req.Messages[len(req.Messages)-1].Content
			content, finish := "?", "stop"
			var toolCalls []any
			switch {
			case len(req.Tools) > 0:
				if f.toolAsText {
					content = `read_file({"path": "main.go"})`
				} else {
					content = ""
					finish = "tool_calls"
					toolCalls = []any{map[string]any{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "read_file", "arguments": `{"path":"main.go"}`},
					}}
				}
			case strings.Contains(user, "sum of a and b"):
				content = "func add(a, b int) int {\n\treturn a + b\n}"
			case strings.Contains(user, "exactly the word OK"):
				content = f.okReply
				if content == "" {
					content = "OK"
				}
			case strings.Contains(user, "1 to 5"):
				content = "1\n2\n3\n4\n5"
			}
			if f.flipCodeFix && strings.Contains(user, "sum of a and b") && f.chats%2 == 0 {
				content = "I would rather explain it."
			}
			reasoning := ""
			if f.reasoningOnly {
				content, toolCalls, finish = "", nil, "length"
				reasoning = strings.Repeat("Let me reconsider. ", 20)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"system_fingerprint": f.fingerprint,
				"choices": []map[string]any{{
					"message": map[string]any{
						"role": "assistant", "content": content,
						"reasoning_content": reasoning, "tool_calls": toolCalls,
					},
					"finish_reason": finish,
				}},
				"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 7},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func healthyDoctorFake() *doctorFake {
	return &doctorFake{maxLen: 32768, version: "0.10.1", cacheDtype: "auto", fingerprint: "vllm-0.10.1-tp4-aaaa"}
}

func doctorLoop(t *testing.T, srvURL, model string) (*Loop, string) {
	t.Helper()
	loop, sid := testLoop(t, srvURL)
	loop.Config.Profiles["balanced"] = config.Profile{Provider: "local", Model: model}
	loop.DoctorDir = t.TempDir()
	loop.Version = "0.89.0"
	return loop, sid
}

func runDoctorCommand(t *testing.T, loop *Loop, sid, text string) string {
	t.Helper()
	handled, err := loop.routeLLMDoctor(t.Context(), sid, "general-purpose", text)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	if !handled {
		t.Fatalf("%s was not recognized as a command", text)
	}
	return lastReply(t, loop, sid)
}

// The gate is the model's name. On anything that is not muse or gemma
// the command says so and the server is never contacted: a probe of a
// hosted model is a probe of somebody else's server.
func TestLLMDoctorIsOnlyForMuseAndGemma(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "test-model")

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	if !strings.Contains(got, "muse") || !strings.Contains(got, "gemma") || !strings.Contains(got, "test-model") {
		t.Errorf("reply = %q, want it to name the families and this model", got)
	}
	if fake.chats != 0 {
		t.Errorf("%d chat requests were sent for a model the command does not cover", fake.chats)
	}

	for _, model := range []string{"muse-glimmer", "Gemma-3-27b", "google/GEMMA-2"} {
		if !doctorApplies(model) {
			t.Errorf("doctorApplies(%q) = false", model)
		}
	}
}

// A run asks the server what it says about itself, sends the four
// canaries, judges them, and keeps the result, all in one reply.
func TestLLMDoctorRunsTheCanariesAndKeepsTheRun(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "google/gemma-3-27b-it")

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	for _, want := range []string{
		"max_model_len: 32768", "vLLM: 0.10.1", "kv cache dtype: auto",
		"preemptions 3", "kv cache in use 12%", "by stop 40", "by length 2",
		"tool_call: pass", "code_fix: pass", "exact_reply: pass", "stops: pass", "4 of 4 pass",
		"No baseline yet",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
	if fake.chats != 2*len(doctorCanaries) {
		t.Errorf("%d chat requests, want each of the %d canaries asked twice", fake.chats, len(doctorCanaries))
	}

	file, err := loadDoctorFile(doctorPath(loop.DoctorDir, "google/gemma-3-27b-it"))
	if err != nil {
		t.Fatalf("the run was not saved: %v", err)
	}
	if file.Last == nil || file.Baseline != nil {
		t.Errorf("after one run: last=%v baseline=%v; the first run must not become the baseline by itself", file.Last != nil, file.Baseline != nil)
	}
	if strings.Contains(got, "curl") {
		t.Errorf("nothing failed, so there is nothing to replay:\n%s", got)
	}
}

// The comparison is the point. A baseline taken on a good day, then a
// server that came back with a shorter window, a newer vLLM, a tool
// parser that is off and a model that no longer follows "exactly": the
// report names each, and says that the evidence points at the server.
func TestLLMDoctorSaysWhatDiffersFromTheBaseline(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "google/gemma-3-27b-it")

	runDoctorCommand(t, loop, sid, "/llm-doctor")
	if got := runDoctorCommand(t, loop, sid, "/llm-doctor baseline"); !strings.Contains(got, "kept the run") {
		t.Fatalf("baseline reply = %q", got)
	}

	fake.mu.Lock()
	fake.maxLen = 8192
	fake.version = "0.11.0"
	fake.cacheDtype = "fp8"
	fake.toolAsText = true
	fake.okReply = "Okay! How can I help you today?"
	fake.mu.Unlock()
	loop.Version = "0.90.0"

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	for _, want := range []string{
		"max_model_len 32768 → 8192",
		"vLLM 0.10.1 → 0.11.0",
		"kv cache dtype auto → fp8",
		"tool_call: pass → FAIL",
		"exact_reply: pass → FAIL",
		"localcode 0.89.0 → 0.90.0",
		"points at the server",
		"tool-call parser is off or changed",
		"curl -s " + srv.URL + "/chat/completions",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "code_fix: pass → FAIL") || strings.Contains(got, "stops: pass → FAIL") {
		t.Errorf("canaries that still pass were reported as changed:\n%s", got)
	}

	// The replay file is the failing request, byte for byte.
	replay := strings.TrimSuffix(doctorPath(loop.DoctorDir, "google/gemma-3-27b-it"), ".json") + ".tool_call.json"
	body, err := os.ReadFile(replay)
	if err != nil {
		t.Fatalf("no replay file for the failing canary: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil || req["model"] != "google/gemma-3-27b-it" || req["temperature"] != float64(0) {
		t.Errorf("the replay file is not the canary request: %s", body)
	}
}

// The verdict is honest about what it cannot see. Same facts, same
// canaries: the report says the change is not in anything a client can
// observe, rather than clearing the server.
func TestLLMDoctorWithNothingChangedSaysSo(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")

	runDoctorCommand(t, loop, sid, "/llm-doctor")
	runDoctorCommand(t, loop, sid, "/llm-doctor baseline")
	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	if !strings.Contains(got, "nothing differs") || !strings.Contains(got, "Nothing differs from the baseline in what a client can see") {
		t.Errorf("reply = %q", got)
	}
}

// A server that is not vLLM offers no /version and no /metrics. The run
// still happens, and the report says those were not offered rather than
// failing on them.
func TestLLMDoctorSurvivesAServerWithoutVLLMEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "muse-glimmer"}}})
		case "/chat/completions":
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": "stop"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	for _, want := range []string{"/version: not offered", "/metrics: not offered", "max_model_len: not reported", "exact_reply: pass", "tool_call: FAIL"} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
}

// Keeping a baseline needs a run to keep.
func TestLLMDoctorBaselineWithoutARunSaysSo(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")

	if got := runDoctorCommand(t, loop, sid, "/llm-doctor baseline"); !strings.Contains(got, "no run to keep yet") {
		t.Errorf("reply = %q", got)
	}
	if fake.chats != 0 {
		t.Errorf("keeping a baseline sent %d requests", fake.chats)
	}
}

// Only the command and its one argument. "/llm-doctors" is not it, and
// "/llm-doctor now" is a usage line rather than a run.
func TestLLMDoctorIsNotAPrefix(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")

	handled, err := loop.routeLLMDoctor(t.Context(), sid, "general-purpose", "/llm-doctors")
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Error("/llm-doctors was taken as the command")
	}
	if got := runDoctorCommand(t, loop, sid, "/llm-doctor now"); !strings.Contains(got, "usage: /llm-doctor [baseline]") {
		t.Errorf("reply = %q", got)
	}
	if fake.chats != 0 {
		t.Errorf("%d requests were sent for text that was not the command", fake.chats)
	}
}

// The command is only for OpenAI-compatible servers, which is where the
// probes have somewhere to go.
func TestLLMDoctorNeedsAnOpenAICompatibleProvider(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")
	loop.Providers["local"] = doctorOtherProvider{}
	loop.Config.Providers["local"] = config.ProviderConfig{Type: config.ProviderAnthropic}

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	if !strings.Contains(got, "OpenAI-compatible") || !strings.Contains(got, "anthropic") {
		t.Errorf("reply = %q", got)
	}
}

// doctorOtherProvider is a provider that is not *provider.OpenAICompat,
// which is all the gate looks at.
type doctorOtherProvider struct{}

func (doctorOtherProvider) Chat(context.Context, provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

// Muse is not run greedily. Its own vLLM recipe asks for temperature 1.0
// with top_p and top_k, and takes reasoning strength from the system
// prompt rather than from "reasoning_effort", so that is how a canary
// goes out. A canary sent the way the publisher warns against would be
// measuring the warning.
func TestLLMDoctorSendsMuseTheSamplingItsRecipeAsksFor(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "Muse-Glimmer-30B")

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	if fake.sawTemp != 1.0 || fake.sawTopP != 0.95 || fake.sawTopK != 64 {
		t.Errorf("muse was sent temperature %v, top_p %v, top_k %v", fake.sawTemp, fake.sawTopP, fake.sawTopK)
	}
	if fake.sawSystem != "Reasoning strength: high" {
		t.Errorf("system prompt = %q, want the reasoning strength muse reads there", fake.sawSystem)
	}
	if !strings.Contains(got, "temperature 1.0, top_p 0.95, top_k 64") {
		t.Errorf("the report does not say what the canaries were sent with:\n%s", got)
	}

	// Gemma has no such recipe, and greedy is still the better probe there.
	loop.Config.Profiles["balanced"] = config.Profile{Provider: "local", Model: "google/gemma-3-27b-it"}
	runDoctorCommand(t, loop, sid, "/llm-doctor")
	if fake.sawTemp != 0 || fake.sawSystem != "" {
		t.Errorf("gemma was sent temperature %v and system %q", fake.sawTemp, fake.sawSystem)
	}
}

// Behind a gateway that routes only the chat endpoint, system_fingerprint
// is the whole of what the server says about itself. It comes off the
// answer, it is reported, and a build swapped underneath is a difference
// against the baseline like any other.
func TestLLMDoctorTakesTheFingerprintFromTheAnswer(t *testing.T) {
	fake := healthyDoctorFake()
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")

	runDoctorCommand(t, loop, sid, "/llm-doctor")
	runDoctorCommand(t, loop, sid, "/llm-doctor baseline")
	fake.mu.Lock()
	fake.fingerprint = "vllm-0.11.0-tp4-bbbb"
	fake.mu.Unlock()

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	for _, want := range []string{
		"system_fingerprint: vllm-0.11.0-tp4-bbbb",
		"system_fingerprint vllm-0.10.1-tp4-aaaa → vllm-0.11.0-tp4-bbbb",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
}

// A gateway that hides /version does not make the build unknown, and the
// report should not say "not vLLM" when the answer names one.
func TestLLMDoctorSaysWhereTheBuildCameFromWithoutVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "muse-glimmer"}}})
		case "/chat/completions":
			json.NewEncoder(w).Encode(map[string]any{
				"system_fingerprint": "vllm-0.26.1rc1.dev608-tp4",
				"choices": []map[string]any{{
					"message": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": "stop",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	if !strings.Contains(got, "system_fingerprint: vllm-0.26.1rc1.dev608-tp4") || !strings.Contains(got, "/version: not routed") {
		t.Errorf("reply = %q", got)
	}
	if strings.Contains(got, "not vLLM") {
		t.Errorf("the fingerprint names vLLM, so the report must not guess otherwise:\n%s", got)
	}
}

// An answer that never began is not a verdict. A reasoning model that
// spends the whole budget thinking says nothing about the server, and
// calling that a failure would be inventing a finding.
func TestLLMDoctorWillNotJudgeAnAnswerThatNeverBegan(t *testing.T) {
	fake := healthyDoctorFake()
	fake.reasoningOnly = true
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	for _, want := range []string{
		"tool_call: inconclusive", "budget went to reasoning_content",
		"chars of reasoning", "0 of 4 pass, 4 inconclusive",
		"No canary reached a verdict",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "FAIL") {
		t.Errorf("a budget this command chose was reported as the server failing:\n%s", got)
	}
}

// Sampled canaries can disagree with themselves. A canary that passes
// once and fails once has measured a coin toss, and the report says that
// rather than reporting whichever side came up first.
func TestLLMDoctorCallsACoinTossInconclusive(t *testing.T) {
	fake := healthyDoctorFake()
	fake.flipCodeFix = true
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	for _, want := range []string{
		"code_fix: inconclusive", "passed once and failed once",
		"answered differently the second time", "one sample rather than a property",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
}

// The replay line has to work, and the key has to stay out of it: a
// report is pasted into chats and issues.
func TestLLMDoctorReplayNamesTheKeyHeaderWithoutTheKey(t *testing.T) {
	fake := healthyDoctorFake()
	fake.toolAsText = true
	srv := fake.server(t)
	defer srv.Close()
	loop, sid := doctorLoop(t, srv.URL, "muse-glimmer")
	loop.Providers["local"] = provider.NewOpenAICompat(srv.URL, "sekret-key")

	got := runDoctorCommand(t, loop, sid, "/llm-doctor")
	if !strings.Contains(got, "-H 'Authorization: Bearer <this profile's api_key>'") {
		t.Errorf("the replay line will 401 as printed:\n%s", got)
	}
	if strings.Contains(got, "sekret-key") {
		t.Errorf("the report carries the key:\n%s", got)
	}
	if fake.sawAuth != "Bearer sekret-key" {
		t.Errorf("the probe itself did not send the key: %q", fake.sawAuth)
	}
}
