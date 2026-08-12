package dictation

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Probe reports, endpoint by endpoint, what a remote speech server
// actually does when localcode talks to it.
//
// This exists because the failure it diagnoses is one nobody can reason
// about from the outside. A server that resets the connection produces
// "an existing connection was forcibly closed by the remote host" and
// nothing else: no status, no body, no clue whether the endpoint is
// wrong, the request is wrong, or something between the two machines is
// dropping it. Three candidate endpoints then fail identically and the
// transcript gets one line that names only the first.
//
// So this asks the questions separately and prints every answer:
//
//	does TCP connect at all
//	does a plain GET work        — separates "HTTP is fine, the POST is not"
//	                               from "nothing gets through"
//	what does each POST endpoint say
//
// The distinction the first two draw is the one that matters most. If GET
// works and every POST is reset, the problem is the request or something
// inspecting it, not the address and not the endpoint list.
type ProbeResult struct {
	Address string
	Steps   []ProbeStep
}

// ProbeStep is one question and its answer.
type ProbeStep struct {
	What   string // "tcp", "GET /", "POST /asr"
	Status string // "connected", "200 OK", "404 Not Found"
	Detail string // the reply, trimmed, or the error
	OK     bool
}

// Probe runs the checks against cfg's remote server. It never returns an
// error for a server that answers badly — a bad answer is the result.
// An error means the probe could not be run at all.
func Probe(ctx context.Context, cfg Config) (*ProbeResult, error) {
	addr := cfg.RemoteHost()
	if addr == "" {
		return nil, fmt.Errorf("no remote speech server configured (set dictation.whisper_url)")
	}
	res := &ProbeResult{Address: addr}

	// 1. TCP. This is the check that used to stand in for the whole
	// thing, which is why a server answering nothing still counted as
	// ready.
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		res.Steps = append(res.Steps, ProbeStep{What: "tcp", Status: "unreachable", Detail: err.Error()})
		return res, nil
	}
	conn.Close()
	res.Steps = append(res.Steps, ProbeStep{What: "tcp", Status: "connected", OK: true})

	// 2. Ordinary GETs. If these work and the uploads do not, the address
	// is right and the endpoint list is not the problem.
	scheme := cfg.RemoteScheme()
	httpWorks := false
	for _, path := range []string{"/", "/health"} {
		step := probeGet(ctx, scheme, addr, path)
		if step.OK {
			httpWorks = true
		}
		res.Steps = append(res.Steps, step)
	}

	// 3. If nothing spoke HTTP, find out whether the port speaks TLS.
	//
	// A TLS port answers a plaintext request by closing the connection,
	// with no status and no message — which is exactly what "an existing
	// connection was forcibly closed by the remote host" is, and is
	// indistinguishable from a server refusing the request. Asking
	// directly turns the whole dead end into one line of instruction.
	if !httpWorks && scheme == "http" {
		res.Steps = append(res.Steps, probeTLS(ctx, addr))
	}

	// 4. Every endpoint localcode would try, with real audio: half a
	// second of silence, which is a valid WAV and a legitimate thing to
	// transcribe.
	p := &whisperProcess{host: addr, log: &syncBuffer{}, model: strings.TrimSpace(cfg.WhisperModel), scheme: scheme}
	silence := make([]float32, SampleRate/2)
	for _, api := range whisperAPIs {
		step := ProbeStep{What: "POST " + api.path + " (" + api.name + ")"}
		text, err := p.transcribeVia(ctx, api, silence, "auto")
		switch {
		case err == nil:
			step.Status = "200 OK"
			step.OK = true
			step.Detail = fmt.Sprintf("transcribed %q", text)
		case isTransportFailure(err):
			step.Status = "no reply"
			step.Detail = err.Error()
		default:
			step.Status = "refused"
			step.Detail = err.Error()
		}
		res.Steps = append(res.Steps, step)
	}
	return res, nil
}

func probeGet(ctx context.Context, scheme, addr, path string) ProbeStep {
	step := ProbeStep{What: "GET " + path}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+addr+path, nil)
	if err != nil {
		step.Status = "bad request"
		step.Detail = err.Error()
		return step
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		step.Status = "no reply"
		step.Detail = err.Error()
		return step
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
	step.Status = resp.Status
	step.Detail = truncateForError(string(body))
	step.OK = resp.StatusCode == http.StatusOK
	return step
}

// probeTLS reports whether the port completes a TLS handshake, which
// answers the question a plaintext failure cannot: is this an HTTPS
// service being addressed as HTTP.
//
// The certificate is not verified. This is a question about what protocol
// the port speaks, not about whether to trust it — and refusing to answer
// because of a self-signed certificate would withhold the one fact being
// asked for.
func probeTLS(ctx context.Context, addr string) ProbeStep {
	step := ProbeStep{What: "TLS handshake"}
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // see above
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		step.Status = "not TLS"
		step.Detail = err.Error()
		return step
	}
	conn.Close()
	step.Status = "this port speaks TLS"
	step.OK = true
	step.Detail = "set whisper_url to https://" + addr + " — it is being addressed as plain http, and a TLS port answers that by closing the connection"
	return step
}

// Summary is the one sentence to read first: what the answers add up to.
func (r *ProbeResult) Summary() string {
	var getOK, postOK, postNoReply int
	for _, s := range r.Steps {
		switch {
		case strings.HasPrefix(s.What, "GET") && s.OK:
			getOK++
		case strings.HasPrefix(s.What, "POST") && s.OK:
			postOK++
		case strings.HasPrefix(s.What, "POST") && s.Status == "no reply":
			postNoReply++
		}
	}
	for _, s := range r.Steps {
		if s.What == "TLS handshake" && s.OK {
			return "this port speaks TLS, and localcode is addressing it as plain http — which is why " +
				"every request is closed without an answer. Set dictation.whisper_url to https://" + r.Address + "."
		}
	}
	switch {
	case postOK > 0:
		return "dictation should work: at least one transcription endpoint answered."
	case getOK > 0 && postNoReply == len(whisperAPIs):
		return "the server answers ordinary requests but closes the connection on every upload. " +
			"That is not a wrong endpoint — the address and the paths are fine, and something is " +
			"rejecting or dropping the audio itself. Look for a proxy or gateway between the two " +
			"machines, a body-size or content-type rule on it, and whether the server is reached " +
			"directly from this machine."
	case getOK > 0:
		return "the server is reachable, but none of the transcription endpoints accepted the audio. " +
			"The refusals above say why."
	default:
		return "nothing got through over HTTP, though the port accepts a TCP connection. " +
			"Something is listening that is not this server, or not speaking plain HTTP on this port."
	}
}
