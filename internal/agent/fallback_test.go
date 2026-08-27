package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"localcode/internal/hooks"
	"localcode/internal/trace"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// failingServer refuses with status for the first n requests and answers
// normally afterwards, recording what each request was.
func failingServer(t *testing.T, status int, body string, failures int) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []recordedRequest
	seen := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		system := ""
		for _, m := range req.Messages {
			if m["role"] == "system" {
				system, _ = m["content"].(string)
			}
		}
		mu.Lock()
		requests = append(requests, recordedRequest{model: req.Model, system: system})
		n := seen
		seen++
		mu.Unlock()

		if n < failures {
			http.Error(w, body, status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answered\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), requests...)
	}
}

// newFallbackLoop wires one provider serving three model names, so a
// fallback is a different profile without needing three servers.
func newFallbackLoop(t *testing.T, url string) *Loop {
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
			// A hosted flagship first, a local open-weight model second:
			// the pair that makes the prompt-variant question real.
			"primary": {Provider: "local", Model: "claude-opus-5", Fallback: []string{"backup", "last"}},
			"backup":  {Provider: "local", Model: "qwen3-coder-30b"},
			"last":    {Provider: "local", Model: "claude-haiku-4-5"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "primary", Description: "the default"},
			"other":           {Profile: "last", Description: "somewhere to delegate"},
		},
		DefaultProfile: "primary",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": provider.NewOpenAICompat(url, "")}, cfg)
	return loop
}

func recovered(t *testing.T, loop *Loop, sid string) []string {
	t.Helper()
	all, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var out []string
	for _, ev := range all {
		if ev.Type == events.TypeError {
			text, _ := ev.Data["error"].(string)
			out = append(out, text)
		}
	}
	return out
}

// quickRetries compresses the same-endpoint backoff so a test exercising
// the retry loop does not sit through real seconds.
func quickRetries(t *testing.T) {
	t.Helper()
	old := retryBase.Load()
	retryBase.Store(int64(time.Millisecond))
	t.Cleanup(func() { retryBase.Store(old) })
}

// A rate limit that persists is not the end of a turn when there is
// somewhere else to ask — but the chain is consulted only after the
// bounded same-endpoint retries are spent, because the common rate limit
// is a blip and the fallback is a different model with a different cache
// prefix.
func TestARateLimitedModelFallsBackAfterItsRetriesAreSpent(t *testing.T) {
	quickRetries(t)
	srv, recordedReqs := failingServer(t, http.StatusTooManyRequests, "rate limit exceeded", 3)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reqs := recordedReqs()
	if len(reqs) != 4 {
		t.Fatalf("got %d requests, want the failure, two retries, and the fallback", len(reqs))
	}
	for i := 0; i < 3; i++ {
		if reqs[i].model != "claude-opus-5" {
			t.Errorf("request %d went to %q, want the primary retried in place", i, reqs[i].model)
		}
	}
	if reqs[3].model != "qwen3-coder-30b" {
		t.Errorf("the fourth request went to %q, want the first fallback", reqs[3].model)
	}
	msgs := recovered(t, loop, sid)
	var sawRetry, sawFallback bool
	for _, m := range msgs {
		if strings.Contains(m, "retrying it in") {
			sawRetry = true
		}
		if strings.Contains(m, "falling back to qwen3-coder-30b") {
			sawFallback = true
		}
	}
	if !sawRetry || !sawFallback {
		t.Errorf("transcript did not report both the retries and the switch: %v", msgs)
	}
}

// The step before the chain: a transient failure is asked about again on
// the same endpoint, and when that answers, no fallback happens at all.
// The conversation stays on the model and the cache prefix it started on.
func TestATransientFailureIsRetriedOnTheSameEndpointFirst(t *testing.T) {
	quickRetries(t)
	srv, recordedReqs := failingServer(t, http.StatusTooManyRequests, "rate limit exceeded", 1)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reqs := recordedReqs()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want the failure and one retry", len(reqs))
	}
	if reqs[0].model != "claude-opus-5" || reqs[1].model != "claude-opus-5" {
		t.Errorf("models were %q then %q, want the same endpoint both times", reqs[0].model, reqs[1].model)
	}
	for _, m := range recovered(t, loop, sid) {
		if strings.Contains(m, "falling back") {
			t.Errorf("a retry that succeeded still walked the chain: %v", m)
		}
	}
}

// A credential failure goes straight to the chain. A 401 will be a 401
// in two seconds too, and retrying it in place spends the user's time on
// a cause time does not fix.
func TestACredentialFailureIsNotRetriedInPlace(t *testing.T) {
	quickRetries(t)
	srv, recordedReqs := failingServer(t, http.StatusUnauthorized, "invalid api key", 1)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reqs := recordedReqs()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want the refusal and the fallback with no retry between", len(reqs))
	}
	if reqs[1].model != "qwen3-coder-30b" {
		t.Errorf("the second request went to %q, want the first fallback immediately", reqs[1].model)
	}
}

// The classifier, on its own. Transient causes retry; identity, request
// and resolution causes do not.
func TestRetryableInPlaceClassifiesByCause(t *testing.T) {
	retryable := []error{
		fmt.Errorf("openai-compat endpoint returned 429: rate limit exceeded"),
		fmt.Errorf("openai-compat endpoint returned 503: service unavailable"),
		fmt.Errorf("bedrock ConverseStream: ThrottlingException: Too many requests"),
		fmt.Errorf("do request: dial tcp 127.0.0.1:1234: connect: connection refused"),
		fmt.Errorf("unexpected EOF"),
	}
	for _, err := range retryable {
		if !retryableInPlace(err) {
			t.Errorf("%v was not treated as transient", err)
		}
	}
	notRetryable := []error{
		nil,
		fmt.Errorf("openai-compat endpoint returned 401: invalid api key"),
		fmt.Errorf("bedrock ConverseStream: ValidationException: The provided model identifier is invalid"),
		fmt.Errorf("model not found"),
		fmt.Errorf("dial tcp: lookup api.example.invalid: no such host"),
		fmt.Errorf("the request exceeds the maximum context length"),
		fmt.Errorf("HTTP 429 while processing: 'temperature' is deprecated for this model"),
	}
	for _, err := range notRetryable {
		if retryableInPlace(err) {
			t.Errorf("%v was treated as transient", err)
		}
	}
}

// The point the whitepaper makes about fallback chains, and the reason
// this is not three lines: changing the model changes which prompt the
// model should be given. A local open-weight model that catches an
// overflow from a hosted flagship must get the prompt written for it, not
// the one written for the model that just failed.
func TestAFallbackGetsThePromptWrittenForItsOwnModel(t *testing.T) {
	quickRetries(t)
	srv, recordedReqs := failingServer(t, http.StatusServiceUnavailable, "service unavailable", 3)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reqs := recordedReqs()
	if len(reqs) != 4 {
		t.Fatalf("got %d requests, want the primary three times and then the fallback", len(reqs))
	}
	if !strings.Contains(reqs[0].system, "1. Understand.") {
		t.Error("the primary did not get the full orchestration policy")
	}
	if strings.Contains(reqs[3].system, "1. Understand.") {
		t.Error("the local fallback was sent the policy written for the flagship")
	}
	if !strings.Contains(reqs[3].system, "Smart Agent is on") {
		t.Error("the fallback got no orchestration prompt at all")
	}
}

// Two failures walk two links. The chain is flat, so the second fallback
// comes from the primary's own list rather than from the first fallback's.
func TestTheChainIsWalkedInOrder(t *testing.T) {
	quickRetries(t)
	srv, recordedReqs := failingServer(t, http.StatusBadGateway, "bad gateway", 6)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	got := []string{}
	for _, r := range recordedReqs() {
		got = append(got, r.model)
	}
	// Each endpoint gets its own retry allowance before the chain moves,
	// so a fully failing endpoint is asked three times.
	want := []string{
		"claude-opus-5", "claude-opus-5", "claude-opus-5",
		"qwen3-coder-30b", "qwen3-coder-30b", "qwen3-coder-30b",
		"claude-haiku-4-5",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("models tried = %v, want %v", got, want)
	}
}

// The chain is finite. A provider having a bad hour must not turn one turn
// into an unbounded sweep of every model configured.
func TestAnExhaustedChainFailsTheTurn(t *testing.T) {
	quickRetries(t)
	srv, recordedReqs := failingServer(t, http.StatusServiceUnavailable, "service unavailable", 99)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err == nil {
		t.Fatal("the turn reported success with every model refusing")
	}
	if n := len(recordedReqs()); n != 9 {
		t.Errorf("made %d requests, want three per profile in the chain: the ask and two retries", n)
	}
}

// Off is off. A fallback is a visible change in who is answering, and a
// retry is a turn silently taking seconds longer, so neither starts
// happening to somebody who installed an update. One request means no
// fallback and no retry both.
func TestWithSmartAgentOffThereIsNoFallback(t *testing.T) {
	quickRetries(t)
	srv, recordedReqs := failingServer(t, http.StatusTooManyRequests, "rate limit", 1)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err == nil {
		t.Fatal("the turn survived a rate limit with Smart Agent off")
	}
	if n := len(recordedReqs()); n != 1 {
		t.Errorf("made %d requests, want just the one that failed", n)
	}
}

// A conversation too long for the window is not an endpoint failure. It is
// already handled properly, by summarizing and retrying on the same model,
// and sending it to a smaller fallback would make it worse and throw away
// the cached prefix as well.
func TestAnOverflowIsNotAFallbackCase(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("openai-compat endpoint returned 400: the request exceeds the maximum context length"),
		fmt.Errorf("input is too long for requested model"),
	} {
		if worthFallingBackOver(err) {
			t.Errorf("%v was treated as an endpoint failure", err)
		}
	}
	for _, err := range []error{
		fmt.Errorf("openai-compat endpoint returned 429: rate limit exceeded"),
		fmt.Errorf("do request: dial tcp 127.0.0.1:1234: connect: connection refused"),
		fmt.Errorf("bedrock ConverseStream: ThrottlingException"),
		fmt.Errorf("openai-compat endpoint returned 503: service unavailable"),
	} {
		if !worthFallingBackOver(err) {
			t.Errorf("%v was not treated as an endpoint failure", err)
		}
	}
}

// SA1. The classifier existed and was tested; nothing called it, so every
// direct request error walked the chain. A deterministic 400 is the case
// that matters: the request is malformed, every endpoint would refuse it
// the same way, and asking two more models costs money and buries the
// configuration error that needs fixing behind whatever the last one said.
func TestADeterministicRequestErrorDoesNotWalkTheChain(t *testing.T) {
	srv, recordedReqs := failingServer(t, http.StatusBadRequest, "invalid tool schema for parameter 'foo'", 1)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err == nil {
		t.Fatal("SendMessage succeeded, want the 400 to end the turn")
	}

	reqs := recordedReqs()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want only the primary — a malformed request is malformed everywhere", len(reqs))
	}
	if reqs[0].model != "claude-opus-5" {
		t.Errorf("the one request went to %q, want the primary", reqs[0].model)
	}
}

// Both fallback call sites go through nextFallback, so the gate cannot
// drift between them, and a refused error must not quietly consume a link
// of the chain either: a rate limit arriving after one is still entitled
// to the first fallback rather than the second.
//
// Tested here rather than through the stream call site because a stream
// that dies for a reason another endpoint could not survive is not
// something an HTTP server can be made to produce on demand: the transport
// failures a stream actually suffers (a reset, an early EOF) are all
// classified as worth retrying elsewhere, which is correct. The gate is
// there so the two sites keep agreeing when that stops being true.
func TestARefusedErrorDoesNotConsumeALinkOfTheChain(t *testing.T) {
	loop := newFallbackLoop(t, "http://127.0.0.1:1")
	loop.SetSmartAgentEnabled(true)

	chain := loop.fallbackChain(context.Background(), loop.Config.Profiles["primary"])
	if len(chain) != 2 {
		t.Fatalf("chain has %d links, want 2", len(chain))
	}
	at := 0

	if _, ok := loop.nextFallback(chain, &at, fmt.Errorf("400 invalid tool schema")); ok {
		t.Error("a malformed request was offered a fallback")
	}
	if at != 0 {
		t.Fatalf("cursor moved to %d on a refused error, want it left alone", at)
	}
	if _, ok := loop.nextFallback(chain, &at, fmt.Errorf("context length exceeded")); ok {
		t.Error("a context overflow was offered a fallback rather than being compacted")
	}
	next, ok := loop.nextFallback(chain, &at, fmt.Errorf("429 rate limit exceeded"))
	if !ok {
		t.Fatal("a rate limit was refused a fallback")
	}
	if next.name != "backup" {
		t.Errorf("fell back to %q, want the first link — the refused errors above ate one", next.name)
	}
}

// The positive half of the same gate, so the two tests above cannot be
// satisfied by simply never falling back: a 503 still moves.
func TestAServiceFailureStillWalksTheChain(t *testing.T) {
	quickRetries(t)
	srv, recordedReqs := failingServer(t, http.StatusBadGateway, "bad gateway", 6)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if reqs := recordedReqs(); len(reqs) != 7 {
		t.Fatalf("got %d requests, want three per failing endpoint and the last one answering", len(reqs))
	}
}

// R2 SA1. An exception class is not a diagnosis. Bedrock raises
// ValidationException for a model id that does not exist in the region,
// for an account not entitled to the model, and for a request field the
// model will not take: an availability problem, an authorization problem
// and a request bug under one name. docs/MODELS.md lists all three under
// that heading. Treating the class as recoverable, which is what the
// classifier used to do, sent a deprecated "temperature" field to every
// profile in the chain.
func TestBedrockValidationIsClassifiedByCauseNotByExceptionClass(t *testing.T) {
	deterministic := []error{
		fmt.Errorf("bedrock ConverseStream: ValidationException: The model returned the following errors: 'temperature' is deprecated for this model"),
		fmt.Errorf("bedrock ConverseStream: ValidationException: Malformed input request: #/messages/0: extraneous key [foo] is not permitted"),
		fmt.Errorf("bedrock ConverseStream: ValidationException: unsupported parameter: top_k"),
		fmt.Errorf("bedrock ConverseStream: ValidationException: invalid tool schema for 'edit'"),
	}
	for _, err := range deterministic {
		if worthFallingBackOver(err) {
			t.Errorf("a request defect was sent to the fallback chain: %v", err)
		}
	}

	recoverable := []error{
		fmt.Errorf("bedrock ConverseStream: ValidationException: The provided model identifier is invalid"),
		fmt.Errorf("bedrock ConverseStream: ValidationException: Your account is not authorized to invoke this API operation"),
		fmt.Errorf("bedrock ConverseStream: ValidationException: Invocation of model ID anthropic.claude-opus-4-6 with on-demand throughput isn't supported"),
		fmt.Errorf("bedrock ConverseStream: ThrottlingException: Too many requests"),
	}
	for _, err := range recoverable {
		if !worthFallingBackOver(err) {
			t.Errorf("an endpoint that cannot serve this model was refused a fallback: %v", err)
		}
	}
}

// A request defect wins over anything else the message happens to carry.
// Provider error strings are concatenations of a status, a class and a
// body, so one message can match both lists; the one that decides has to
// be the one about the request.
func TestARequestDefectBeatsAnEndpointPhraseInTheSameMessage(t *testing.T) {
	err := fmt.Errorf("bedrock ConverseStream: ValidationException (HTTP 400): 'temperature' is deprecated for this model, quota unaffected")
	if worthFallingBackOver(err) {
		t.Error("a message naming both a request defect and a quota was treated as an endpoint failure")
	}
}

// R10N4. The retry used to be counted, announced and traced before the
// backoff wait — so a turn cancelled during the wait carried a retry
// record for an attempt no provider ever saw, and then walked the
// fallback chain with a dead context, recording a model switch that
// never happened either. A cancellation during the backoff now ends the
// turn: one provider request, no retry counted, no fallback consulted.
func TestCancellationDuringTheBackoffEndsTheTurnWithoutFallback(t *testing.T) {
	// A long backoff, so the only way this test finishes quickly is the
	// cancellation actually short-circuiting the wait.
	old := retryBase.Load()
	retryBase.Store(int64(time.Hour))
	t.Cleanup(func() { retryBase.Store(old) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The cancellation lands after the retry is announced and before its
	// wait begins, which is the deterministic middle of the backoff.
	retryWaitBarrier = cancel
	t.Cleanup(func() { retryWaitBarrier = nil })

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	err := loop.SendMessage(ctx, sid, "general-purpose", "hello")
	if err == nil {
		t.Fatal("a turn cancelled mid-backoff reported success")
	}

	mu.Lock()
	n := requests
	mu.Unlock()
	if n != 1 {
		t.Errorf("the provider was asked %d times, want exactly the one request before the cancellation", n)
	}

	msgs := recovered(t, loop, sid)
	var sawScheduled, sawCancelled bool
	for _, m := range msgs {
		if strings.Contains(m, "retrying it in") {
			sawScheduled = true
		}
		if strings.Contains(m, "never attempted") {
			sawCancelled = true
		}
		if strings.Contains(m, "falling back") {
			t.Errorf("a cancelled turn still consulted the fallback chain: %v", m)
		}
	}
	if !sawScheduled || !sawCancelled {
		t.Errorf("the transcript should show the scheduled wait and its cancellation: %v", msgs)
	}
}

// R11N2. The retry used to be counted and traced the moment its backoff
// finished, which is not the moment a provider is asked: a pre_model
// hook still stands between the two and can block the turn outright. A
// trace that then says a retry happened describes an attempt no provider
// ever received. The count is committed at the call now, so a blocked
// retry leaves the record saying what actually occurred: one request,
// no retry.
func TestARetryBlockedByAHookIsNotCounted(t *testing.T) {
	quickRetries(t)

	// A hook that allows the first request and blocks every one after
	// it. The marker file is how it tells them apart, since a hook is a
	// shell command with no memory of its own.
	marker := filepath.Join(t.TempDir(), "seen")
	loop := newFallbackLoop(t, "")
	loop.Config.Hooks = hooks.Config{
		hooks.EventPreModel: {{Command: fmt.Sprintf(
			`if [ -e %q ]; then echo '{"decision":"block","reason":"the retry is not allowed"}'; else touch %q; fi`,
			marker, marker)}},
	}

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	loop.Providers["local"] = provider.NewOpenAICompat(srv.URL, "")
	loop.Config.Providers["local"] = config.ProviderConfig{Type: config.ProviderOpenAICompat, BaseURL: srv.URL}
	loop.SetSmartAgentEnabled(true)
	w := withTracing(t, loop)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello")
	if err == nil || !strings.Contains(err.Error(), "the retry is not allowed") {
		t.Fatalf("the blocked retry did not end the turn with the hook's reason: %v", err)
	}

	mu.Lock()
	n := requests
	mu.Unlock()
	if n != 1 {
		t.Errorf("the provider received %d requests, want only the original", n)
	}
	for _, rec := range w.Recent(100, sid, "") {
		if rec.Span == trace.SpanRetry {
			t.Errorf("a retry span was written although the retry never reached the provider: %+v", rec)
		}
	}
	// The transcript still says the retry was scheduled, because it was:
	// the turn genuinely waited. What must not appear is a count.
	var announced bool
	for _, m := range recovered(t, loop, sid) {
		if strings.Contains(m, "retrying it in") {
			announced = true
		}
	}
	if !announced {
		t.Error("the scheduled retry was not in the transcript at all")
	}
}
