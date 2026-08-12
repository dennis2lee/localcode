package dictation

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"localcode/internal/childproc"
)

// whisperProcess owns a running whisper.cpp server and turns audio into
// text over its HTTP interface.
//
// A child process rather than CGo because CGo is what confined dictation
// to desktop builds: the release pipeline cross-compiles every platform
// from one machine and cannot link C. With the recognizer behind a pipe,
// the Go side compiles everywhere and the native part ships as its own
// file per platform.
//
// whisper.cpp's *own* server binary rather than an engine written here.
// The obvious design is a small C++ program speaking PCM in and JSON
// out, and it would be a program to build, test, and keep working
// against upstream on three platforms. whisper-server already is that
// program, it is maintained by the people who maintain the model
// runtime, and talking to it costs a multipart POST. There is no C++ in
// this repository as a result.
//
// One process serves every session. Loading the model takes on the order
// of a second and hundreds of megabytes; requests are independent, so
// there is nothing to gain from a copy per speaker and a great deal to
// lose.
type whisperProcess struct {
	// cmd is nil for an engine this process did not start — see
	// acquireWhisper's remote branch. Everything that touches it is
	// guarded, because for a remote engine there is nothing to wait on,
	// nothing to kill, and no stderr to quote back.
	cmd *exec.Cmd
	// host is where the engine answers, "host:port". For a local engine
	// that is 127.0.0.1 and a port picked at startup; for a remote one it
	// is whatever the user configured.
	host string
	log  *syncBuffer

	// api is the dialect this server speaks, once a request has proved
	// which one it is. Guarded by apiMu rather than the engine's own
	// lock: it is written once, from whichever transcription happened to
	// discover it, and read by every one after that.
	// scheme is "http" or "https" — see Config.RemoteScheme. A local
	// engine is always plain http on loopback.
	scheme string

	// model is sent as the OpenAI "model" field when set — see
	// transcribeVia. Comes from whisper_model, which is otherwise only
	// used to find a local model file.
	model string

	apiMu      sync.Mutex
	api        whisperAPI
	apiSettled bool

	// exited is closed when the process ends.
	//
	// A channel rather than cmd.ProcessState, which exec only fills in
	// when Wait or Run is called — and neither is called on a server
	// meant to keep running, so reading it reports "not exited" forever.
	// A crashed engine would then be handed to the next session, failing
	// every request with a connection error, and startup would wait the
	// whole timeout instead of reporting what the engine printed on its
	// way out.
	exited chan struct{}

	mu   sync.Mutex
	refs int
}

// watch reaps the process and records that it ended. Calling Wait is
// also what keeps a finished child from lingering as a zombie.
func (p *whisperProcess) watch() {
	p.exited = make(chan struct{})
	go func() {
		p.cmd.Wait()
		close(p.exited)
	}()
}

var (
	sharedMu      sync.Mutex
	sharedWhisper *whisperProcess
	sharedKey     string
)

// acquireWhisper returns the shared server for this configuration,
// starting it if it is not running, and counts one more user of it.
//
// Keyed on binary and model together: changing either has to mean a new
// process, since the model is chosen on the command line and cannot be
// swapped in a running one.
func acquireWhisper(cfg Config) (*whisperProcess, error) {
	// A remote engine is not a process at all: nothing is spawned, no
	// binary or model has to be installed here, and the machine at the
	// other end is responsible for its own lifetime. All this side needs
	// is somewhere to POST audio to.
	//
	// Not shared through sharedWhisper either — there is no expensive
	// thing to keep warm, so a plain value per recognizer avoids the
	// refcount and the kill-on-key-change question entirely.
	if remote := cfg.RemoteHost(); remote != "" {
		p := &whisperProcess{host: remote, log: &syncBuffer{}, model: strings.TrimSpace(cfg.WhisperModel), scheme: cfg.RemoteScheme()}
		// A named dialect skips discovery. An unknown name is not
		// silently ignored: it would look like it had been honoured while
		// the search quietly did something else.
		if name := strings.TrimSpace(cfg.WhisperAPI); name != "" {
			api, ok := apiByName(name)
			if !ok {
				return nil, fmt.Errorf("whisper_api %q is not one of: %s", name, whisperAPINames())
			}
			p.api, p.apiSettled = api, true
		}
		if err := p.reachable(); err != nil {
			return nil, err
		}
		return p, nil
	}

	bin, model, err := cfg.whisperPaths()
	if err != nil {
		return nil, err
	}
	// The key uses the thread count the process will actually be started
	// with, not the one asked for. startWhisper turns anything <= 0 into
	// defaultThreads(), so Threads:0 and Threads:4 on a four-thread
	// machine describe the same server — keyed on the raw value they
	// looked like different ones and took turns killing each other.
	key := bin + "\x00" + model + "\x00" + strconv.Itoa(normalizeThreads(cfg.Threads))

	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedWhisper != nil && sharedKey == key && sharedWhisper.alive() {
		sharedWhisper.mu.Lock()
		sharedWhisper.refs++
		sharedWhisper.mu.Unlock()
		return sharedWhisper, nil
	}
	if sharedWhisper != nil {
		// Only if nobody is using it. Killing a process with live users
		// aborts whatever request is in flight: the session that owned it
		// sees its transcription fail, silently keeps showing stale grey
		// text, and there is nothing on screen to say a different
		// configuration took its engine away.
		//
		// A still-referenced engine is left alone and this recognizer gets
		// its own process. Two servers is the honest cost of two
		// configurations; it stops being shared, which is all the sharing
		// was ever promising.
		sharedWhisper.mu.Lock()
		inUse := sharedWhisper.refs > 0 && sharedWhisper.alive()
		sharedWhisper.mu.Unlock()
		if !inUse {
			sharedWhisper.kill()
		}
		sharedWhisper = nil
	}

	p, err := startWhisper(bin, model, cfg.Threads)
	if err != nil {
		return nil, err
	}
	p.refs = 1
	sharedWhisper, sharedKey = p, key
	return p, nil
}

// release drops one user. The process is deliberately left running when
// the last one goes: reloading the model costs about a second, and
// someone who just finished dictating is the person most likely to
// dictate again. Close tears it down for real.
func (p *whisperProcess) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refs > 0 {
		p.refs--
	}
}

// normalizeThreads resolves the thread count a server will really run
// with, so the value that starts a process and the value that identifies
// it cannot disagree.
func normalizeThreads(threads int) int {
	if threads <= 0 {
		return defaultThreads()
	}
	return threads
}

// syncBuffer collects the engine's output for error reporting.
//
// Synchronised because two goroutines touch it: os/exec's copier, which
// writes whatever the child prints, and whoever is waiting for the server
// to come up, which reads it to explain a timeout. A plain bytes.Buffer
// there is a data race, and it only shows up when startup is slow —
// which is exactly when the read happens.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startWhisper(bin, model string, threads int) (*whisperProcess, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	threads = normalizeThreads(threads)

	args := []string{
		"--model", model,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--threads", strconv.Itoa(threads),
		// No timestamps: this fills a prompt box, and "[00:00.000 -->"
		// in front of every line is not what anyone dictating wants.
		"--no-timestamps",
		// Whisper will otherwise hallucinate a plausible sentence out of
		// silence, and dictation is mostly silence between phrases.
		"--no-fallback",
	}
	cmd := exec.Command(bin, args...)
	// The engine is a console program and the desktop build has no
	// console of its own, so on Windows this would otherwise put a black
	// box on screen for as long as dictation is available — which is the
	// whole session.
	childproc.Hide(cmd)
	logBuf := &syncBuffer{}
	// Kept rather than discarded: when the server refuses to start, its
	// reason is on stderr, and "whisper did not become ready" on its own
	// sends people looking in the wrong place.
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start whisper engine %s: %w", bin, err)
	}

	p := &whisperProcess{cmd: cmd, host: "127.0.0.1:" + strconv.Itoa(port), log: logBuf}
	p.watch()
	if err := p.waitReady(); err != nil {
		p.kill()
		return nil, err
	}
	return p, nil
}

// waitReady blocks until the server answers, or gives up with whatever
// it printed on the way to not answering.
func (p *whisperProcess) waitReady() error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if !p.alive() {
			return fmt.Errorf("whisper engine exited at startup: %s", p.tail())
		}
		conn, err := net.DialTimeout("tcp", p.addr(), 300*time.Millisecond)
		if err == nil {
			conn.Close()
			// Something answered — but the port was picked by opening a
			// listener and closing it again, so between that and the
			// child's bind anything could have taken it. If our process
			// has died, whatever just answered is not ours, and treating
			// it as the engine would send audio to a stranger.
			if !p.alive() {
				return fmt.Errorf("whisper engine exited at startup: %s", p.tail())
			}
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("whisper engine did not become ready within 60s: %s", p.tail())
}

func (p *whisperProcess) addr() string { return p.host }

// baseURL is the address requests are actually sent to, scheme included.
func (p *whisperProcess) baseURL() string {
	scheme := p.scheme
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + p.host
}

// remote reports that this engine belongs to another machine, so there is
// no child process behind it.
func (p *whisperProcess) remote() bool { return p.cmd == nil }

// reachable checks that something is listening before dictation starts.
//
// A wrong address should fail when it is entered, with the address in the
// message, rather than as silence the first time someone speaks — which
// is indistinguishable from a microphone that is not working.
func (p *whisperProcess) reachable() error {
	conn, err := net.DialTimeout("tcp", p.host, 5*time.Second)
	if err != nil {
		return fmt.Errorf("no speech engine answering at %s: %w", p.host, err)
	}
	conn.Close()
	return nil
}

func (p *whisperProcess) alive() bool {
	// A remote engine's health is not this process's to know: it can go
	// away and come back without anything here noticing, so every request
	// carries its own verdict and there is nothing useful to cache.
	if p.remote() {
		return true
	}
	if p.cmd == nil || p.cmd.Process == nil || p.exited == nil {
		return false
	}
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// tail returns the last of the engine's output, for error messages.
func (p *whisperProcess) tail() string {
	s := strings.TrimSpace(p.log.String())
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	if s == "" {
		return "(no output)"
	}
	return s
}

func (p *whisperProcess) kill() {
	if p.remote() || p.cmd.Process == nil {
		return
	}
	p.cmd.Process.Kill()
	// Waited for by watch, so this waits on that rather than calling
	// Wait a second time — two Waits on one Cmd is an error, and the
	// second one would return it instead of blocking.
	if p.exited != nil {
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
		}
	}
}

// Shutdown stops the shared engine.
//
// The engine deliberately outlives the last dictation session, so that
// dictating again does not pay to reload the model. It must not outlive
// the program: a server holding a few hundred megabytes, still running
// after localcode exits, is a process the user never started and would
// have to find and kill by hand. Manager.Close calls this.
func Shutdown() {
	sharedMu.Lock()
	p := sharedWhisper
	sharedWhisper, sharedKey = nil, ""
	sharedMu.Unlock()
	if p != nil {
		p.kill()
	}
}

// transcribe sends one window of audio and returns what was said in it.
//
// The samples are wrapped as a WAV because that is what the server's
// upload endpoint takes, and building a 44 byte header is cheaper than
// any argument for a different transport.
func (p *whisperProcess) transcribe(ctx context.Context, samples []float32, language string) (string, error) {
	if len(samples) == 0 {
		return "", nil
	}
	if language == "" {
		language = "auto"
	}

	// The dialect this server speaks, once it is known. A locally spawned
	// engine is whisper.cpp and always has been, so there is nothing to
	// discover; a remote one is whatever someone put on that port.
	p.apiMu.Lock()
	known, settled := p.api, p.apiSettled
	p.apiMu.Unlock()
	if settled {
		return p.transcribeVia(ctx, known, samples, language)
	}

	// Not known yet: try each in turn. A 404 or a 405 means this server
	// does not have that endpoint, which is the one answer that says
	// nothing about the audio and everything about the dialect — so it
	// moves on. Any other outcome, success or failure, is this server
	// answering the question that was asked, and settles the choice.
	var firstErr error
	transportOnly := true
	for _, candidate := range whisperAPIs {
		text, err := p.transcribeVia(ctx, candidate, samples, language)

		// A request that never got an answer says nothing about which
		// endpoint is there — the connection failed, or the server closed
		// it mid-upload, which it does when it means to reject the
		// request and does not wait for the rest of the body. Settling on
		// a candidate for that reason is how the first version of this
		// picked /v1/audio/transcriptions on a server that was resetting
		// the connection, and then sent every later utterance to the same
		// place instead of trying the endpoint that worked.
		if isTransportFailure(err) || isWrongEndpoint(err) {
			if firstErr == nil {
				firstErr = err
			}
			if !isTransportFailure(err) {
				transportOnly = false
			}
			continue
		}

		// An actual HTTP response, whatever it says. That is the server
		// answering the question that was asked, and it settles the
		// choice.
		p.apiMu.Lock()
		p.api, p.apiSettled = candidate, true
		p.apiMu.Unlock()
		debugf("dictation: %s speaks the %q API (%s)", p.addr(), candidate.name, candidate.path)
		return text, err
	}

	if firstErr == nil {
		firstErr = fmt.Errorf("no endpoint answered")
	}
	// Nothing was reachable at all: report it as what it is rather than
	// as "this server has none of the endpoints", and leave the dialect
	// unsettled so the next utterance tries again. A network that dropped
	// for a second must not disable dictation for the rest of the run.
	if transportOnly {
		return "", fmt.Errorf("could not reach the speech engine at %s: %w", p.addr(), firstErr)
	}
	return "", fmt.Errorf("%s has none of the transcription endpoints localcode knows (%s): %w",
		p.addr(), whisperAPINames(), firstErr)
}

// transcribeVia sends one window of audio to one endpoint.
//
// The samples are wrapped as a WAV because that is what every one of these
// servers takes, and building a 44 byte header is cheaper than any
// argument for a different transport.
func (p *whisperProcess) transcribeVia(ctx context.Context, api whisperAPI, samples []float32, language string) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile(api.fileField, "audio.wav")
	if err != nil {
		return "", err
	}
	if err := writeWAV(part, samples); err != nil {
		return "", err
	}
	if err := w.WriteField("language", language); err != nil {
		return "", err
	}
	// The OpenAI shape has a required "model" on the real API, and the
	// servers that imitate it vary in whether they enforce it. Sent only
	// when configured: a server hosting one model rejects a name it does
	// not recognise, so guessing one would break the servers that do not
	// need it in order to satisfy the ones that do.
	if api.name == "openai" && p.model != "" {
		if err := w.WriteField("model", p.model); err != nil {
			return "", err
		}
	}
	for k, v := range api.extra {
		if err := w.WriteField(k, v); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL()+api.path, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	// Ask before sending the audio.
	//
	// A server that means to reject the request — a missing field, a
	// path it does not serve — answers as soon as it has the headers and
	// closes the connection without draining the body. From this side
	// that arrives as a connection reset partway through the upload, with
	// no status and no message: "an existing connection was forcibly
	// closed by the remote host", which says nothing about what was
	// wrong. With 100-continue the rejection comes back as the status it
	// really is, and the body is never sent.
	req.Header.Set("Expect", "100-continue")

	resp, err := transcribeClient.Do(req)
	if err != nil {
		return "", &transportError{err: fmt.Errorf("whisper engine: %w", err)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &wrongEndpointError{
			status: resp.StatusCode,
			err: fmt.Errorf("whisper engine returned %s for %s: %s",
				resp.Status, api.path, truncateForError(string(raw))),
		}
	}
	text, err := parseTranscript(raw)
	if err != nil {
		return "", err
	}
	return cleanTranscript(text), nil
}

// transcribeClient is the client transcription requests go through.
//
// It differs from the default in one way that matters: ExpectContinueTimeout
// is set, without which Go sends the body immediately and the Expect header
// above does nothing.
var transcribeClient = &http.Client{
	Transport: &http.Transport{
		// Env first, then the Windows system proxy — see sysproxy.go.
		// The browser on the same machine reaches the speech server
		// through that proxy; connecting directly instead is how a
		// working server looked permanently unreachable.
		Proxy:                 dictationProxy,
		ExpectContinueTimeout: 2 * time.Second,
	},
}

// transportError marks a failure that produced no HTTP response at all.
// It is not evidence about the endpoint, so it neither settles the dialect
// search nor ends it.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

func isTransportFailure(err error) bool {
	var te *transportError
	return errors.As(err, &te)
}

// wrongEndpointError carries the status alongside the message so the
// dialect search can tell "this endpoint is not here" from "this endpoint
// is here and something went wrong", without parsing the message.
type wrongEndpointError struct {
	status int
	err    error
}

func (e *wrongEndpointError) Error() string { return e.err.Error() }
func (e *wrongEndpointError) Unwrap() error { return e.err }

// isWrongEndpoint reports whether err means the server has no such
// endpoint, as opposed to having one that refused this request.
//
// Only 404 and 405 qualify. A 400 or a 422 is the endpoint existing and
// disliking the request, which is a real answer and must not send the
// search on to try a different path — that would turn one clear error
// into three confusing ones.
func isWrongEndpoint(err error) bool {
	var we *wrongEndpointError
	if !errors.As(err, &we) {
		return false
	}
	return we.status == http.StatusNotFound || we.status == http.StatusMethodNotAllowed
}

// writeWAV emits 16 kHz mono 16-bit PCM with the smallest legal header.
func writeWAV(w io.Writer, samples []float32) error {
	dataLen := len(samples) * 2
	h := make([]byte, 0, 44)
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(36+dataLen))
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16) // PCM header size
	h = binary.LittleEndian.AppendUint16(h, 1)  // PCM
	h = binary.LittleEndian.AppendUint16(h, 1)  // mono
	h = binary.LittleEndian.AppendUint32(h, SampleRate)
	h = binary.LittleEndian.AppendUint32(h, SampleRate*2) // byte rate
	h = binary.LittleEndian.AppendUint16(h, 2)            // block align
	h = binary.LittleEndian.AppendUint16(h, 16)           // bits
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(dataLen))
	if _, err := w.Write(h); err != nil {
		return err
	}

	buf := make([]byte, dataLen)
	for i, s := range samples {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(s*32767)))
	}
	_, err := w.Write(buf)
	return err
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find a port for the whisper engine: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
