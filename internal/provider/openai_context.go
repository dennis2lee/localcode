package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// This file asks the server how big its context window is, instead of
// guessing from the model's name.
//
// Guessing is what internal/modelinfo does, and for a hosted model it is
// reliable: the name identifies the model and the model has one window.
// A local server is the opposite case. It serves whatever was loaded,
// under whatever name the file happened to have, and — the part that
// matters — it is nearly always started with a *smaller* window than the
// model supports, because the window is what costs VRAM. A model capable
// of 128k routinely runs at 8k on a laptop.
//
// So the name cannot answer the question even in principle, while the
// server can answer it exactly. Nothing here is a fallback for a bad
// guess; the server is simply the authority and was never asked.

// contextProbeFields are the keys the various servers report the window
// under. No two of them agree, and none of it is in the OpenAI spec — the
// /v1/models response is only required to carry an id — so each server
// invented its own.
//
//	max_model_len          vLLM
//	max_context_length     LM Studio
//	loaded_context_length  LM Studio, when it differs from the model's max
//	context_length         TGI, and several proxies
//	n_ctx                  llama.cpp
//
// Ordered so that a *loaded* size wins over a *maximum* size wherever a
// server reports both: what a request has to fit inside is the window the
// server actually started with, and taking the larger number would put
// this back to guessing high, which is the harmful direction.
var contextProbeFields = []string{
	"loaded_context_length",
	"n_ctx",
	"max_model_len",
	"max_context_length",
	"context_length",
}

// maxPlausibleWindow rejects nonsense. Some servers report a byte count or
// a parameter count in a field named like a window, and a wrong number
// this large would size every request against a limit that is not there.
const maxPlausibleWindow = 20_000_000

// ContextWindow asks the server what context window it is serving model
// with, and reports whether it got an answer.
//
// Best-effort by design: every failure — no such endpoint, a shape nobody
// anticipated, a server that simply does not say — returns false, and the
// caller falls back to guessing from the name exactly as before. This must
// never be the reason a session cannot start.
func (p *OpenAICompat) ContextWindow(ctx context.Context, model string) (int, bool) {
	if n, ok := p.probeModels(ctx, model); ok {
		return n, true
	}
	// llama.cpp's own server does not put the window in /v1/models, but
	// serves it from /props — and llama.cpp is the single most likely
	// thing behind an unrecognised model name.
	return p.probeProps(ctx)
}

func (p *OpenAICompat) probeModels(ctx context.Context, model string) (int, bool) {
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if !p.getJSON(ctx, p.BaseURL+"/models", &body) {
		return 0, false
	}

	// The entry for this model, matched loosely: a server may report
	// "Qwen/Qwen3-30B" for a request that says "qwen3-30b".
	var fallback map[string]any
	for _, entry := range body.Data {
		id, _ := entry["id"].(string)
		if idMatches(id, model) {
			if n, ok := windowFrom(entry); ok {
				return n, true
			}
		}
		if fallback == nil {
			fallback = entry
		}
	}
	// One model loaded and it did not match by name: it is still the only
	// thing this server can be serving, so its window is the one that
	// applies. Local servers are routinely addressed by an alias.
	if len(body.Data) == 1 && fallback != nil {
		return windowFrom(fallback)
	}
	return 0, false
}

// probeProps reads llama.cpp's /props, where n_ctx is the size the server
// was actually started with.
func (p *OpenAICompat) probeProps(ctx context.Context) (int, bool) {
	var body map[string]any
	// /props sits beside the API root rather than under /v1.
	if !p.getJSON(ctx, strings.TrimSuffix(p.BaseURL, "/v1")+"/props", &body) {
		return 0, false
	}
	if settings, ok := body["default_generation_settings"].(map[string]any); ok {
		if n, ok := windowFrom(settings); ok {
			return n, true
		}
	}
	return windowFrom(body)
}

func (p *OpenAICompat) getJSON(ctx context.Context, url string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
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
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

// windowFrom pulls the first plausible window figure out of one JSON
// object, looking inside a nested "meta" too — llama.cpp puts the model's
// numbers there.
func windowFrom(entry map[string]any) (int, bool) {
	for _, field := range contextProbeFields {
		if n, ok := positiveInt(entry[field]); ok {
			return n, true
		}
	}
	for _, nested := range []string{"meta", "model_info", "settings"} {
		if sub, ok := entry[nested].(map[string]any); ok {
			for _, field := range contextProbeFields {
				if n, ok := positiveInt(sub[field]); ok {
					return n, true
				}
			}
		}
	}
	return 0, false
}

// positiveInt accepts the number however JSON delivered it. encoding/json
// decodes into float64 for an `any`, and more than one server sends the
// figure as a string.
func positiveInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		n := int(t)
		if n > 0 && n <= maxPlausibleWindow {
			return n, true
		}
	case json.Number:
		if n, err := t.Int64(); err == nil && n > 0 && n <= maxPlausibleWindow {
			return int(n), true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil && n > 0 && n <= maxPlausibleWindow {
			return n, true
		}
	}
	return 0, false
}

// idMatches compares a served model id with the one in config, tolerating
// the vendor prefix and separator differences that are normal between the
// two ("Qwen/Qwen3-30B-A3B" vs "qwen3-30b-a3b").
func idMatches(served, want string) bool {
	if served == "" || want == "" {
		return false
	}
	return normalizeID(served) == normalizeID(want)
}

func normalizeID(s string) string {
	s = strings.ToLower(s)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return strings.NewReplacer("_", "", "-", "", ".", "", ":", "").Replace(s)
}
