package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The exposition has hundreds of series; the diagnosis needs a handful,
// summed across labels, and the finished requests split by why they
// finished. The KV cache dtype is a label, not a value.
func TestParseVLLMMetricsKeepsWhatADiagnosisNeeds(t *testing.T) {
	text := `# HELP vllm:num_preemptions_total Cumulative number of preemption from the engine.
# TYPE vllm:num_preemptions_total counter
vllm:num_preemptions_total{model_name="m"} 3.0
vllm:kv_cache_usage_perc{model_name="m"} 0.25
vllm:num_requests_running{model_name="m"} 1.0
vllm:num_requests_waiting{model_name="m"} 0.0
vllm:request_success_total{finished_reason="stop",model_name="m"} 40.0
vllm:request_success_total{finished_reason="length",model_name="m"} 2.0
vllm:request_success_total{finished_reason="stop",model_name="lora"} 5.0
vllm:cache_config_info{block_size="16",cache_dtype="fp8",num_gpu_blocks="1000"} 1.0
vllm:something_else{model_name="m"} 99.0
not a metric line
`
	m, dtype := parseVLLMMetrics(text)
	want := map[string]float64{
		"preemptions": 3, "kv_cache_usage": 0.25, "requests_running": 1, "requests_waiting": 0,
		"finished_stop": 45, "finished_length": 2,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["something_else"]; ok {
		t.Error("an unwanted series was kept")
	}
	if dtype != "fp8" {
		t.Errorf("cache dtype = %q, want fp8", dtype)
	}
}

// A server that is not vLLM (LM Studio, llama.cpp) has the model list and
// nothing else. Every probe fails softly and the facts say so.
func TestServerFactsOnAServerThatIsNotVLLM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "muse-glimmer", "max_model_len": 16384},
		}})
	}))
	defer srv.Close()

	facts := NewOpenAICompat(srv.URL, "").ServerFacts(context.Background(), "muse-glimmer")
	if !facts.ModelsOK || facts.ModelID != "muse-glimmer" || facts.MaxModelLen != 16384 {
		t.Errorf("models: %+v", facts)
	}
	if facts.VersionOK || facts.MetricsOK || facts.Version != "" || facts.CacheDtype != "" {
		t.Errorf("a server without /version and /metrics was read as offering them: %+v", facts)
	}
}

// RawChat reads the answer the way the server wrote it, and a failure is
// the server's own words with its status: the person reading them is
// diagnosing that server.
func TestRawChatReturnsTheServersOwnWords(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		b.Write(buf[:n])
		gotBody = b.String()
		if strings.Contains(gotBody, "\"fail\"") {
			http.Error(w, `{"error":"seed is not supported on this engine"}`, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"system_fingerprint": "vllm-0.26.1rc1.dev608+g99a10304d-tp4-6e5f6fca",
			"choices": []map[string]any{{
				"message": map[string]any{
					"role": "assistant", "content": "", "reasoning_content": "thinking",
					"tool_calls": []map[string]any{{"function": map[string]any{"name": "read_file", "arguments": `{"path":"x"}`}}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 9},
		})
	}))
	defer srv.Close()
	p := NewOpenAICompat(srv.URL, "")

	reply, err := p.RawChat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != `{"model":"m","messages":[]}` {
		t.Errorf("the body was not sent as written: %s", gotBody)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Name != "read_file" || reply.FinishReason != "tool_calls" ||
		reply.Reasoning != "thinking" || reply.OutputTokens != 9 ||
		reply.Fingerprint != "vllm-0.26.1rc1.dev608+g99a10304d-tp4-6e5f6fca" {
		t.Errorf("reply = %+v", reply)
	}

	_, err = p.RawChat(context.Background(), []byte(`{"model":"fail"}`))
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "seed is not supported") {
		t.Errorf("err = %v, want the status and the server's words", err)
	}
}
