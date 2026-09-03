package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// The probes behind "/llm-doctor". They ask an OpenAI-compatible server
// what it will say about itself, and send it a request exactly as
// written, so the answer can be judged without this package's own
// translation in the way. A diagnostic that went through Chat would be
// measuring Chat as much as the server.

// ServerFacts is what a server reports about itself: the model entry from
// /v1/models, and — where the server is vLLM — its version, the KV cache
// dtype it started with, and a few counters. Every field has an "offered"
// flag or a zero value that means "not said", because the point of the
// facts is to compare them with yesterday's, and "not offered" is itself
// a fact worth keeping.
type ServerFacts struct {
	ModelsOK    bool   `json:"models_ok"`
	ModelID     string `json:"model_id,omitempty"`
	MaxModelLen int    `json:"max_model_len,omitempty"`

	VersionOK bool   `json:"version_ok"`
	Version   string `json:"version,omitempty"`

	MetricsOK  bool               `json:"metrics_ok"`
	CacheDtype string             `json:"cache_dtype,omitempty"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`

	// Fingerprint is what the chat endpoint stamps on an answer. vLLM
	// puts its build and the parallelism it was started with there
	// ("vllm-0.26.1rc1.dev608+g99a10304d-tp4-..."). It arrives through
	// the one endpoint every gateway routes, so behind a gateway that
	// hides /version and /metrics it is often the only server fact
	// there is. The caller fills it from the first answer it gets.
	Fingerprint string `json:"system_fingerprint,omitempty"`
}

// ServerFacts collects what the server at BaseURL says about model.
// Best-effort throughout: a missing endpoint leaves its flag false and
// says nothing else, so a server that is not vLLM still yields the model
// entry.
func (p *OpenAICompat) ServerFacts(ctx context.Context, model string) ServerFacts {
	var facts ServerFacts

	var models struct {
		Data []map[string]any `json:"data"`
	}
	if p.getJSON(ctx, p.BaseURL+"/models", &models) {
		facts.ModelsOK = true
		var entry map[string]any
		for _, e := range models.Data {
			id, _ := e["id"].(string)
			if idMatches(id, model) {
				entry = e
				break
			}
		}
		if entry == nil && len(models.Data) == 1 {
			entry = models.Data[0]
		}
		if entry != nil {
			facts.ModelID, _ = entry["id"].(string)
			if n, ok := positiveInt(entry["max_model_len"]); ok {
				facts.MaxModelLen = n
			}
		}
	}

	root := strings.TrimSuffix(p.BaseURL, "/v1")
	var version struct {
		Version string `json:"version"`
	}
	if p.getJSON(ctx, root+"/version", &version) && version.Version != "" {
		facts.VersionOK = true
		facts.Version = version.Version
	}

	if text, ok := p.getText(ctx, root+"/metrics"); ok {
		facts.MetricsOK = true
		facts.Metrics, facts.CacheDtype = parseVLLMMetrics(text)
	}
	return facts
}

// metricsWanted are the vLLM gauges and counters a person asking "what is
// different about the server today" can use, by the name the exposition
// gives them. Both spellings of the cache gauge, since the V1 engine
// renamed it.
var metricsWanted = map[string]string{
	"vllm:num_preemptions_total":   "preemptions",
	"vllm:gpu_cache_usage_perc":    "kv_cache_usage",
	"vllm:kv_cache_usage_perc":     "kv_cache_usage",
	"vllm:num_requests_running":    "requests_running",
	"vllm:num_requests_waiting":    "requests_waiting",
	"vllm:prompt_tokens_total":     "prompt_tokens",
	"vllm:generation_tokens_total": "generation_tokens",
}

// parseVLLMMetrics reads a Prometheus exposition and keeps the handful of
// series above, summed across labels (a server with one model has one
// label set; summing is what makes that not matter). Requests that
// finished are split by their finished_reason, because a rising share of
// "length" is the single clearest sign of a server cutting answers short.
// The KV cache dtype is a label on vllm:cache_config_info, and a change
// in it is a change in the arithmetic every answer is made with.
func parseVLLMMetrics(text string) (map[string]float64, string) {
	out := map[string]float64{}
	cacheDtype := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// name{labels} value  |  name value
		name, labels, value := splitMetricLine(line)
		if name == "" {
			continue
		}
		switch {
		case name == "vllm:cache_config_info":
			if v := labelValue(labels, "cache_dtype"); v != "" {
				cacheDtype = v
			}
		case name == "vllm:request_success_total":
			reason := labelValue(labels, "finished_reason")
			if reason == "" {
				reason = "unknown"
			}
			out["finished_"+reason] += value
		default:
			if key, ok := metricsWanted[name]; ok {
				out[key] += value
			}
		}
	}
	return out, cacheDtype
}

func splitMetricLine(line string) (name, labels string, value float64) {
	rest := line
	if i := strings.IndexByte(rest, '{'); i >= 0 {
		name = rest[:i]
		j := strings.IndexByte(rest, '}')
		if j < i {
			return "", "", 0
		}
		labels = rest[i+1 : j]
		rest = rest[j+1:]
	} else {
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			return "", "", 0
		}
		name = fields[0]
		rest = fields[1]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", "", 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", "", 0
	}
	return name, labels, v
}

// labelValue reads one label out of a Prometheus label list:
// a="x",b="y". Values are quoted; nothing here needs the escapes.
func labelValue(labels, key string) string {
	for _, part := range strings.Split(labels, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k == key {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func (p *OpenAICompat) getText(ctx context.Context, url string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	// An exposition is a few hundred lines; a megabyte is a server that
	// is not what this expected.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// RawReply is one non-streamed chat completion, read back with as little
// interpretation as possible.
type RawReply struct {
	Content      string
	Reasoning    string
	ToolCalls    []RawToolCall
	FinishReason string
	// Fingerprint is the server's system_fingerprint, if it stamps one.
	Fingerprint  string
	PromptTokens int
	OutputTokens int
}

type RawToolCall struct {
	Name      string
	Arguments string
}

// RawChat posts body — a complete chat/completions request the caller
// wrote — and returns the first choice. Unlike Chat it does not stream,
// does not translate the messages, and does not soften a failure: an
// error names the status and what the server said, because the person
// reading it is diagnosing that server.
func (p *OpenAICompat) RawChat(ctx context.Context, body []byte) (RawReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return RawReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return RawReply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return RawReply{}, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return RawReply{}, fmt.Errorf("the server answered %d: %s", resp.StatusCode, msg)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		SystemFingerprint string `json:"system_fingerprint"`
		Usage             struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return RawReply{}, fmt.Errorf("the server's answer is not a chat completion: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return RawReply{}, fmt.Errorf("the server's answer has no choices")
	}
	c := parsed.Choices[0]
	out := RawReply{
		Content:      c.Message.Content,
		Reasoning:    c.Message.ReasoningContent,
		FinishReason: c.FinishReason,
		Fingerprint:  parsed.SystemFingerprint,
		PromptTokens: parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}
	if out.Reasoning == "" {
		out.Reasoning = c.Message.Reasoning
	}
	for _, tc := range c.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, RawToolCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return out, nil
}
