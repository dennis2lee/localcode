package dictation

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Streaming dictation, for a speech server that can transcribe while
// someone is still speaking.
//
// The HTTP path in whisper_process.go exists because whisper is not a
// streaming model: it reads a window of audio and returns the whole of
// it, so the only way to show text mid-sentence is to re-send the
// utterance so far every 900ms and throw the previous answer away. That
// is wasted work by construction, and over a network it is wasted
// *bandwidth* by construction too: at ten seconds into a sentence, every
// preview ships ten seconds of audio again.
//
// A streaming server removes both halves of that. The audio goes out
// once, as it is recorded, and text comes back when the server has it —
// so the grey text appears while the words are still being said rather
// than up to a second behind, and the cost per second of speech stops
// growing with the length of the sentence.
//
// Only remote engines take this path. A locally spawned engine is
// whisper.cpp's own server, which has no streaming endpoint at all, and
// on the same machine the round trip this saves is not what anyone is
// waiting for.

// streamProto is one server's streaming protocol.
//
// An interface rather than a table of paths and field names, which is
// what whisperAPI is, because these dialects differ in more than their
// spelling: one has a JSON handshake and answers with cumulative
// segments, another has no handshake and replaces its whole transcript
// each message. A second dialect should be a new implementation here,
// not another column in a struct that fits only the first one.
type streamProto interface {
	// name is what whisper_api can be set to, and what a diagnostic prints.
	name() string
	// url is the address to connect to.
	url(scheme, host, language string) string
	// hello is the first message to send, or "" for a dialect that has
	// no handshake.
	hello(cfg Config, language string) string
	// ready folds one message received before any audio was sent.
	// Returns true once the server has said it is ready to receive audio.
	// A dialect with no handshake never sees this.
	ready(msg []byte) (bool, error)
	// encode turns samples into the bytes this dialect wants on the wire.
	encode(samples []float32) []byte
	// accept folds one transcript message into st.
	accept(msg []byte, st *streamText) error
}

// streamProtos are tried in order when whisper_api is not set.
var streamProtos = []streamProto{whisperLive{}}

// streamProtoByName finds a streaming dialect the user named in config.
func streamProtoByName(name string) (streamProto, bool) {
	for _, p := range streamProtos {
		if strings.EqualFold(p.name(), name) {
			return p, true
		}
	}
	return nil, false
}

// streamProtoNames lists the streaming dialects, for error messages.
func streamProtoNames() []string {
	names := make([]string, len(streamProtos))
	for i, p := range streamProtos {
		names[i] = p.name()
	}
	return names
}

// streamHandshake bounds getting from "connect" to "ready for audio".
//
// Short, because this runs before the microphone is live and every
// second of it is a second of someone speaking into a dead session. Long
// enough for a server that has to load a model first, which is the
// normal reason a handshake is slow.
const streamHandshake = 10 * time.Second

// streamTail is how long a finished utterance waits for the server to
// catch up with the last words of it.
//
// The audio was sent as it was recorded, so at the moment the speaker
// stops there is always a little of it the server has not transcribed
// yet — a fraction of a second of processing, not a whole utterance.
// Waiting for it is the difference between committing the sentence and
// committing the sentence minus its last word.
const streamTail = 2 * time.Second

// streamQuiet ends that wait early: once the server has sent nothing new
// for this long, it has finished with what it was given.
const streamQuiet = 350 * time.Millisecond

// streamGrace is the least a finished utterance waits when the server has
// not said it is finished with it.
//
// Silence from the server is ambiguous: it means either "there is nothing
// more to say" or "the last of the audio is still being transcribed", and
// the second one is the normal state a fraction of a second after someone
// stops talking. A dialect that marks its segments complete answers the
// question outright and never waits this out; this is the floor for one
// that does not.
const streamGrace = 500 * time.Millisecond

// streamRetry is how long a dropped connection waits before it is dialed
// again, so a server that is down does not get one connection attempt
// per chunk of audio.
const streamRetry = 3 * time.Second

// ---------------------------------------------------------------------
// The transcript being assembled.

// streamSeg is one segment of transcript as the server sees it.
type streamSeg struct {
	// start is the segment's offset in seconds from the beginning of the
	// connection, which is what makes it an identity: a server revising
	// what it thinks was said sends the same start again with better
	// text.
	start float64
	text  string
	// done marks a segment the server says it will not revise.
	done bool
}

// streamText is the transcript of the utterance being spoken.
//
// A streaming server holds one transcript for the whole connection and
// keeps sending the tail of it; this side commits a sentence whenever
// the speaker pauses, and must not commit the same words twice. mark is
// what draws that line.
type streamText struct {
	segs []streamSeg
	// mark is the offset past which segments are still ours to show.
	// Everything before it has been committed to the prompt box already.
	mark float64
}

// upsert records a segment, replacing an earlier version of the same one.
func (t *streamText) upsert(seg streamSeg) {
	if seg.start < t.mark {
		return
	}
	for i := range t.segs {
		// Times are compared with a tolerance because they arrive as
		// text with three decimals, and because a server is entitled to
		// nudge a boundary by a millisecond while revising.
		if math.Abs(t.segs[i].start-seg.start) < 0.005 {
			t.segs[i] = seg
			return
		}
	}
	t.segs = append(t.segs, seg)
	sort.Slice(t.segs, func(i, j int) bool { return t.segs[i].start < t.segs[j].start })
}

// String is the text of everything not yet committed.
func (t *streamText) String() string {
	var b strings.Builder
	for _, s := range t.segs {
		b.WriteString(s.text)
	}
	return cleanTranscript(b.String())
}

// take draws the line: whatever is here has been committed, and the
// server's next message about it must not put it on screen again.
func (t *streamText) take() {
	for _, s := range t.segs {
		if s.start >= t.mark {
			// Just past this segment, so the same one arriving again is
			// recognised as old. A segment the server later revises is
			// lost rather than duplicated, which is the right way round:
			// this only happens when a revision lands after the speaker
			// has already paused, and a sentence appearing twice in the
			// prompt box is worse than one missing a late correction.
			t.mark = s.start + 0.001
		}
	}
	t.segs = nil
}

// settled reports that the server has marked everything it sent as final,
// which is it saying it has finished with the audio.
func (t *streamText) settled() bool {
	for _, s := range t.segs {
		if !s.done {
			return false
		}
	}
	return len(t.segs) > 0
}

// reset forgets everything, for a connection being started again.
func (t *streamText) reset() { *t = streamText{} }

// ---------------------------------------------------------------------
// WhisperLive.

// whisperLive speaks Collabora's WhisperLive protocol, which is the one
// most streaming whisper deployments run.
//
// The shape of it: connect, send one JSON object of options, wait for
// {"message": "SERVER_READY"}, then send raw 32-bit float samples as
// binary frames and read {"segments": [...]} as they come back. Segment
// times are strings, and are offsets from the start of the connection
// rather than from the start of the sentence.
type whisperLive struct{}

func (whisperLive) name() string { return "whisperlive" }

func (whisperLive) url(scheme, host, _ string) string { return wsURL(scheme, host, "/") }

func (whisperLive) hello(cfg Config, language string) string {
	opts := map[string]any{
		// A client id the server puts on everything it sends back. It
		// only has to be unique among the clients of one server.
		"uid":  strconv.FormatUint(rand.Uint64(), 36),
		"task": "transcribe",
		// The server's own voice activity detection, so silence is not
		// sent to the model. This side has a detector too and they are
		// not doing the same job: ours decides when a sentence is
		// finished, theirs decides what is worth transcribing.
		"use_vad": true,
		"model":   liveModel(cfg),
	}
	// Explicitly null rather than omitted: the server reads
	// options["language"] directly, and null is how its own client says
	// "detect it".
	if language != "" {
		opts["language"] = language
	} else {
		opts["language"] = nil
	}
	b, _ := json.Marshal(opts)
	return string(b)
}

// liveModel picks the model name to ask for.
//
// whisper_model is a path to a ggml file for a locally spawned engine,
// and a streaming server wants a name it can load itself. A configured
// value is passed through only when it looks like a name; a path is not
// silently turned into one.
func liveModel(cfg Config) string {
	m := strings.TrimSpace(cfg.WhisperModel)
	if m == "" || strings.ContainsAny(m, `/\`) || strings.HasSuffix(m, ".bin") {
		// The server's own default, and the size that keeps up with
		// speech on hardware people actually run these on.
		return "small"
	}
	return m
}

func (whisperLive) ready(msg []byte) (bool, error) {
	var m struct {
		Message any    `json:"message"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return false, fmt.Errorf("speech server sent unreadable JSON: %s", truncateForError(string(msg)))
	}
	text, _ := m.Message.(string)
	switch {
	case strings.EqualFold(m.Status, "ERROR"):
		return false, fmt.Errorf("speech server: %s", text)
	case strings.EqualFold(m.Status, "WAIT"):
		// The server is full and is saying how many minutes until a slot
		// frees. Reported rather than waited out: dictation is something
		// someone just switched on.
		return false, fmt.Errorf("speech server is full (it says about %v minutes)", m.Message)
	case text == "SERVER_READY":
		return true, nil
	}
	// A warning, a disconnect notice, anything else: not ready yet, and
	// not a failure either.
	return false, nil
}

func (whisperLive) encode(samples []float32) []byte {
	out := make([]byte, len(samples)*4)
	for i, s := range samples {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(s))
	}
	return out
}

func (whisperLive) accept(msg []byte, st *streamText) error {
	var m struct {
		Segments []struct {
			Start     json.RawMessage `json:"start"`
			Text      string          `json:"text"`
			Completed bool            `json:"completed"`
		} `json:"segments"`
		Message any    `json:"message"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return fmt.Errorf("speech server sent unreadable JSON: %s", truncateForError(string(msg)))
	}
	if strings.EqualFold(m.Status, "ERROR") {
		text, _ := m.Message.(string)
		return fmt.Errorf("speech server: %s", text)
	}
	for _, seg := range m.Segments {
		st.upsert(streamSeg{start: jsonSeconds(seg.Start), text: seg.Text, done: seg.Completed})
	}
	return nil
}

// jsonSeconds reads a time that may be a number or a string holding one.
// WhisperLive formats its offsets as "1.234"; nothing says the next
// server to speak this protocol will.
func jsonSeconds(raw json.RawMessage) float64 {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// ---------------------------------------------------------------------
// The recognizer.

// streamRecognizer is a Recognizer backed by a live connection to a
// streaming server.
//
// It keeps this package's own voice activity detector, and that is
// deliberate even though the server has one: theirs decides which audio
// is worth transcribing, ours decides when a sentence is finished and
// becomes committed text. Dictation's behaviour — grey text while
// speaking, black text after a pause — is the same on every engine
// because that decision is made here, on audio, rather than by whichever
// server happens to be at the other end.
type streamRecognizer struct {
	proto    streamProto
	cfg      Config
	language string

	mu       sync.Mutex
	conn     *wsConn
	text     streamText
	detector vad
	// ended records that this utterance is over: the speaker paused, or
	// it ran past maxUtterance.
	ended bool
	// since counts samples fed since the last reset, so an utterance with
	// no pause in it still commits eventually.
	since int
	// lastMsg is when the server last sent anything, which is how Final
	// knows it has caught up.
	lastMsg time.Time
	// retryAt holds off reconnecting, so a server that is down costs one
	// attempt every few seconds rather than one per chunk of audio.
	retryAt time.Time
	lastErr error
	closed  bool

	readers sync.WaitGroup
}

// openStream connects to a streaming speech server, or reports why it
// could not. ok is false when this configuration is not asking for one.
func openStream(cfg Config) (Recognizer, error, bool) {
	if cfg.RemoteHost() == "" {
		// A locally spawned engine is whisper.cpp's own server, which has
		// no streaming endpoint. Nothing to try.
		return nil, nil, false
	}

	name := strings.TrimSpace(cfg.WhisperAPI)
	var try []streamProto
	switch {
	case name == "":
		// Streaming first, HTTP if nothing here answers: the fallback is
		// what makes this safe to attempt against every remote server,
		// including the great many that only speak HTTP.
		try = streamProtos
	default:
		p, ok := streamProtoByName(name)
		if !ok {
			// A named HTTP dialect. Not an error and not a streaming
			// session either.
			return nil, nil, false
		}
		try = []streamProto{p}
	}

	pinned := name != ""
	var firstErr error
	for _, proto := range try {
		rec, err := dialStreamRecognizer(cfg, proto)
		if err == nil {
			debugf("dictation: streaming from %s (%s)", cfg.RemoteHost(), proto.name())
			return rec, nil, true
		}
		if firstErr == nil {
			firstErr = err
		}
		debugf("dictation: %s does not speak %s: %v", cfg.RemoteHost(), proto.name(), err)
	}
	// Unpinned, this is not a failure: it is the ordinary answer from an
	// HTTP-only server, and the caller goes on to use HTTP. Pinned, the
	// user named this protocol and has to be told it is not there.
	return nil, firstErr, pinned
}

func dialStreamRecognizer(cfg Config, proto streamProto) (*streamRecognizer, error) {
	r := &streamRecognizer{proto: proto, cfg: cfg, language: cfg.Language}
	conn, err := r.dial(context.Background())
	if err != nil {
		return nil, err
	}
	r.conn = conn
	// Counted from the connection rather than from the first transcript,
	// so an utterance the server has nothing to say about is not a wait.
	r.lastMsg = time.Now()
	r.start(conn)
	return r, nil
}

// dial opens one connection and completes the dialect's handshake.
func (r *streamRecognizer) dial(parent context.Context) (*wsConn, error) {
	ctx, cancel := context.WithTimeout(parent, streamHandshake)
	defer cancel()

	conn, err := dialWebSocket(ctx, r.proto.url(r.cfg.RemoteScheme(), r.cfg.RemoteHost(), r.language))
	if err != nil {
		return nil, err
	}
	hello := r.proto.hello(r.cfg, r.language)
	if hello == "" {
		return conn, nil
	}
	if err := conn.writeText(hello); err != nil {
		conn.Close()
		return nil, err
	}
	deadline := time.Now().Add(streamHandshake)
	for {
		op, msg, err := conn.read(deadline)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if op != opText {
			continue
		}
		ready, err := r.proto.ready(msg)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if ready {
			return conn, nil
		}
	}
}

// start runs the reader for one connection.
func (r *streamRecognizer) start(conn *wsConn) {
	r.readers.Add(1)
	go func() {
		defer r.readers.Done()
		for {
			op, msg, err := conn.read(time.Time{})
			if err != nil {
				r.drop(conn, fmt.Errorf("speech server at %s: %w", r.cfg.RemoteHost(), err))
				return
			}
			if op != opText {
				continue
			}
			r.mu.Lock()
			r.lastMsg = time.Now()
			if err := r.proto.accept(msg, &r.text); err != nil {
				r.noteErr(err)
			}
			r.mu.Unlock()
		}
	}()
}

// drop retires a connection that failed, and says why once.
func (r *streamRecognizer) drop(conn *wsConn, err error) {
	r.mu.Lock()
	if r.conn == conn {
		r.conn = nil
		r.retryAt = time.Now().Add(streamRetry)
	}
	// A connection this recognizer closed on purpose has nothing to
	// report: the microphone was switched off, which is not a fault.
	if !r.closed {
		r.noteErr(err)
	}
	r.mu.Unlock()
	conn.Close()
}

func (r *streamRecognizer) noteErr(err error) {
	if err == nil {
		return
	}
	r.lastErr = err
}

func (r *streamRecognizer) Accept(samples []float32) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if r.detector.feed(samples) {
		r.ended = true
	}
	r.since += len(samples)
	if r.since >= maxUtterance {
		r.ended = true
	}
	conn := r.conn
	retry := r.conn == nil && time.Now().After(r.retryAt)
	r.mu.Unlock()

	if conn == nil {
		if !retry {
			return
		}
		// Reconnecting loses whatever the server had in flight, so the
		// transcript starts again from zero — including its clock, which
		// is what streamText is keyed on.
		fresh, err := r.dial(context.Background())
		r.mu.Lock()
		if err != nil {
			r.retryAt = time.Now().Add(streamRetry)
			r.noteErr(fmt.Errorf("speech server at %s: %w", r.cfg.RemoteHost(), err))
			r.mu.Unlock()
			return
		}
		if r.closed {
			r.mu.Unlock()
			fresh.Close()
			return
		}
		r.text.reset()
		r.lastMsg = time.Now()
		r.conn, conn = fresh, fresh
		r.mu.Unlock()
		r.start(fresh)
	}

	if err := conn.writeBinary(r.proto.encode(samples)); err != nil {
		r.drop(conn, fmt.Errorf("speech server at %s: %w", r.cfg.RemoteHost(), err))
	}
}

func (r *streamRecognizer) Partial() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text.String()
}

func (r *streamRecognizer) Endpoint() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ended
}

// Final waits a moment for the server to finish the sentence.
//
// The audio was sent as it was spoken, so when the speaker stops there is
// always a little of it the server has not read yet — a fraction of a
// second, not a whole utterance. Committing without waiting for it drops
// the end of every sentence, which is exactly the word people check for.
func (r *streamRecognizer) Final(ctx context.Context) string {
	start := time.Now()
	deadline := start.Add(streamTail)
	for {
		r.mu.Lock()
		// Three ways to be done: the server says so, the server has gone
		// quiet for long enough that it has nothing more, or there is no
		// server to wait for any more.
		done := r.text.settled() ||
			(time.Since(start) >= streamGrace && time.Since(r.lastMsg) >= streamQuiet) ||
			r.closed || r.conn == nil
		r.mu.Unlock()
		if done || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			// The client gave up on this request. Whatever is here is
			// still what the person said.
		case <-time.After(25 * time.Millisecond):
			continue
		}
		break
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text.String()
}

func (r *streamRecognizer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text.take()
	r.detector.reset()
	r.ended = false
	r.since = 0
}

func (r *streamRecognizer) TakeError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastErr == nil {
		return ""
	}
	msg := r.lastErr.Error()
	r.lastErr = nil
	return msg
}

func (r *streamRecognizer) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	conn := r.conn
	r.conn = nil
	r.mu.Unlock()

	if conn != nil {
		conn.Close()
	}
	r.readers.Wait()
}

// probeStream asks whether the server speaks one streaming dialect,
// for `localcode dictation probe`.
//
// The same question dictation asks at the start of a session, with the
// answer printed instead of acted on: whether the address upgrades to a
// WebSocket at all, and whether the handshake behind it completes.
func probeStream(ctx context.Context, cfg Config, proto streamProto) ProbeStep {
	addr := proto.url(cfg.RemoteScheme(), cfg.RemoteHost(), cfg.Language)
	step := ProbeStep{What: "WS " + addr + " (" + proto.name() + ")"}

	r := &streamRecognizer{proto: proto, cfg: cfg, language: cfg.Language}
	conn, err := r.dial(ctx)
	switch {
	case err == nil:
		conn.Close()
		step.Status = "streaming"
		step.OK = true
		step.Detail = "the server transcribes as the audio arrives"
	case isNotWebSocket(err):
		// The ordinary answer from the great majority of speech servers,
		// and not a fault: dictation uses HTTP against them.
		step.Status = "no streaming endpoint"
		step.Detail = err.Error()
	default:
		step.Status = "refused"
		step.Detail = err.Error()
	}
	return step
}
