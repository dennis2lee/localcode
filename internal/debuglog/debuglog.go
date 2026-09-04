// Package debuglog writes everything localcode says to a model, and
// everything the model says back, to a file per prompt.
//
// It exists because the interesting failures are on the wire and nothing
// else shows them. The turn log under ~/.localcode/trace records that a
// call happened, which model answered and what it cost; "/context" says
// what was going to be in a request. Neither shows the request. When a
// local server answers differently today than yesterday, or a model
// ignores an instruction that is definitely in the prompt, the bytes are
// the evidence and everything else is a summary of them.
//
// So this is deliberately unsummarized: the request line, the headers,
// the body as sent, the status, and the response as it streams. One file
// per prompt, because that is the unit a person is investigating, and
// because a single growing file would interleave a sub-agent's calls
// with the conversation's own with no way to tell them apart.
//
// Off by default and never persisted. It writes files into the workspace
// and those files contain the whole conversation, including whatever the
// model was told about the project; that is a thing to switch on for a
// question and off afterwards, not a setting to leave in config.json
// and forget.
//
// Credentials never reach the file. See redactHeaders: the values that
// authenticate are replaced, not shortened, and the list is of header
// names rather than of value shapes, because a header nobody thought of
// is the one that leaks.
package debuglog

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxLoggedBody is the largest request body this reads out to log it.
//
// A ceiling because reading the body means holding it: the bytes are put
// back for the caller, so a request twice the size of memory would be
// held twice. Responses have no such ceiling, since they are copied as
// they stream rather than held.
const maxLoggedBody = 32 << 20

// Sink is one prompt's file. Safe for concurrent use: a turn's own calls
// are sequential, but a prompt that delegates has sub-agents writing to
// the same file at the same time.
type Sink struct {
	mu   sync.Mutex
	f    *os.File
	path string
	n    int
}

// Create opens the file for a prompt that started at when, under dir.
//
// The name is the timestamp, to the millisecond, because that is what a
// person has to go on: they know when they pressed enter. Two prompts in
// one millisecond get a suffix rather than one overwriting the other.
func Create(dir, sessionID string, when time.Time) (*Sink, error) {
	if dir == "" {
		return nil, fmt.Errorf("no workspace to write the log in")
	}
	base := "localcode-debug-" + when.Format("20060102-150405.000")
	path := filepath.Join(dir, base+".log")
	for i := 2; ; i++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			s := &Sink{f: f, path: path}
			s.writef("# localcode debug log\n# prompt started %s\n# session %s\n"+
				"# Every request to a model and every response, as they went over the wire,\n"+
				"# including binary bodies, written byte for byte and not escaped.\n"+
				"# Credentials are redacted; nothing else is.\n\n",
				when.Format(time.RFC3339Nano), sessionID)
			return s, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if i > 99 {
			return nil, err
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.log", base, i))
	}
}

// Path is the file this sink writes, for the reply that says where it is.
func (s *Sink) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// writef appends to the file, or does nothing once it is closed.
//
// The closed check is inside the lock, which is not a detail: a response
// that is still streaming when the turn ends goes on writing after Close
// has run. Reading the field outside the lock was a race the detector
// found, and the failure it describes is worse than a torn read — a
// write that passed the check a moment before Close could reach a file
// that is no longer open.
func (s *Sink) writef(format string, args ...any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return
	}
	fmt.Fprintf(s.f, format, args...)
}

// Close finishes the file. A sink that wrote nothing is removed rather
// than left behind: a prompt answered from a slash command makes no model
// call, and an empty file in the workspace for every one of those is
// litter that makes the real logs harder to find.
func (s *Sink) Close() error {
	if s == nil || s.f == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.f.Close()
	s.f = nil
	if s.n == 0 {
		_ = os.Remove(s.path)
	}
	return err
}

// exchange numbers the calls within one prompt, so a file with eleven of
// them can be read.
func (s *Sink) exchange() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.n
}

// redactHeaders copies h with the values that authenticate replaced.
//
// By name, and every name that carries a secret on any provider
// localcode speaks to, plus the AWS signature headers in case a
// transport is ever wired there. Replaced with a fixed string rather
// than a prefix of the value: a prefix is enough to identify a key, and
// the point of the file is that it can be pasted into an issue.
func redactHeaders(h http.Header) http.Header {
	secret := map[string]bool{
		"authorization":        true,
		"x-api-key":            true,
		"api-key":              true,
		"x-goog-api-key":       true,
		"proxy-authorization":  true,
		"cookie":               true,
		"set-cookie":           true,
		"x-amz-security-token": true,
		"x-amz-content-sha256": true,
	}
	out := make(http.Header, len(h))
	for k, v := range h {
		if secret[strings.ToLower(k)] {
			out[k] = []string{"[redacted]"}
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}

func writeHeaders(b *strings.Builder, h http.Header) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	// Sorted, so two exchanges of the same shape can be diffed.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintf(b, "%s: %s\n", k, v)
		}
	}
}

// Transport logs each request and its response to whatever sink the
// request's context carries, then behaves exactly like Base.
//
// A RoundTripper rather than a wrapper around Provider.Chat, because
// what is wanted is the bytes: the request localcode's own types
// produced, not the types. It is also the one place that catches every
// HTTP call a provider makes, including the ones that are not Chat —
// /v1/models, vLLM's /metrics, the probes "/llm-doctor" sends.
type Transport struct {
	Base http.RoundTripper
}

func (t Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	s := From(req.Context())
	if s == nil {
		return t.base().RoundTrip(req)
	}

	n := s.exchange()
	var b strings.Builder
	fmt.Fprintf(&b, "==== %d >>> %s %s  %s\n", n, req.Method, req.URL.String(),
		time.Now().Format(time.RFC3339Nano))
	writeHeaders(&b, redactHeaders(req.Header))

	// The body is copied as the transport sends it, not read out and put
	// back.
	//
	// Reading it up front needs a second copy from somewhere: either
	// buffer it and hand the caller a replacement, which turns a
	// streamed upload into a buffered one and doubles what it costs, or
	// ask the client for one through GetBody. Neither is general. The
	// AWS SDK sets no GetBody at all and marks a signed body's length
	// unknown, so a Bedrock turn logged its answer and not its question —
	// which is exactly how this was reported.
	//
	// A tee needs nothing from the client. The transport reads the body
	// to send it and the copy goes into the file on the way past, in the
	// order it is sent, for every client and every length. What it does
	// not cover is a retry that goes through GetBody: the first attempt
	// is logged and the replay is not.
	if req.Body != nil && req.Body != http.NoBody {
		b.WriteString("\n")
		s.writef("%s", b.String())
		req.Body = &teeRequestBody{r: req.Body, s: s, left: maxLoggedBody}
	} else {
		b.WriteString("\n")
		s.writef("%s", b.String())
	}

	start := time.Now()
	resp, err := t.base().RoundTrip(req)
	if err != nil {
		s.writef("==== %d <<< failed after %s: %v\n\n", n, time.Since(start).Round(time.Millisecond), err)
		return resp, err
	}

	var rb strings.Builder
	rb.WriteString("\n")
	fmt.Fprintf(&rb, "==== %d <<< %s  after %s\n", n, resp.Status, time.Since(start).Round(time.Millisecond))
	writeHeaders(&rb, redactHeaders(resp.Header))
	rb.WriteString("\n")
	s.writef("%s", rb.String())

	// Streamed as it arrives rather than buffered: the response is an SSE
	// stream the caller renders token by token, and holding it to log it
	// whole would turn a streaming answer into one that appears at the
	// end.
	resp.Body = &teeBody{r: resp.Body, s: s, n: n, start: start}
	return resp, nil
}

// teeRequestBody copies the request into the log as the transport sends
// it, and stops copying at the ceiling without stopping the send.
type teeRequestBody struct {
	r    io.ReadCloser
	s    *Sink
	left int
	cut  bool
}

func (t *teeRequestBody) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		switch {
		case t.left >= n:
			t.s.writef("%s", p[:n])
			t.left -= n
		case !t.cut:
			t.s.writef("%s", p[:t.left])
			t.s.writef("\n[the rest of this request body is past the %d-byte ceiling and is not shown]\n", maxLoggedBody)
			t.left, t.cut = 0, true
		}
	}
	return n, err
}

func (t *teeRequestBody) Close() error { return t.r.Close() }

// teeBody copies the response into the log as the caller reads it.
type teeBody struct {
	r     io.ReadCloser
	s     *Sink
	n     int
	start time.Time
	done  bool
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.s.writef("%s", p[:n])
	}
	if err != nil && !t.done {
		t.done = true
		if err == io.EOF {
			t.s.writef("\n==== %d end, %s\n\n", t.n, time.Since(t.start).Round(time.Millisecond))
		} else {
			t.s.writef("\n==== %d stream ended: %v\n\n", t.n, err)
		}
	}
	return n, err
}

func (t *teeBody) Close() error {
	if !t.done {
		t.done = true
		t.s.writef("\n==== %d closed after %s\n\n", t.n, time.Since(t.start).Round(time.Millisecond))
	}
	return t.r.Close()
}
