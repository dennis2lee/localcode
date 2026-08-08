package dictation

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
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
	cmd  *exec.Cmd
	port int
	log  *bytes.Buffer

	mu   sync.Mutex
	refs int
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
	bin, model, err := cfg.whisperPaths()
	if err != nil {
		return nil, err
	}
	key := bin + "\x00" + model + "\x00" + strconv.Itoa(cfg.Threads)

	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedWhisper != nil && sharedKey == key && sharedWhisper.alive() {
		sharedWhisper.mu.Lock()
		sharedWhisper.refs++
		sharedWhisper.mu.Unlock()
		return sharedWhisper, nil
	}
	if sharedWhisper != nil {
		sharedWhisper.kill()
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

func startWhisper(bin, model string, threads int) (*whisperProcess, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	if threads <= 0 {
		threads = defaultThreads()
	}

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
	logBuf := &bytes.Buffer{}
	// Kept rather than discarded: when the server refuses to start, its
	// reason is on stderr, and "whisper did not become ready" on its own
	// sends people looking in the wrong place.
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start whisper engine %s: %w", bin, err)
	}

	p := &whisperProcess{cmd: cmd, port: port, log: logBuf}
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
		if p.cmd.ProcessState != nil && p.cmd.ProcessState.Exited() {
			return fmt.Errorf("whisper engine exited at startup: %s", p.tail())
		}
		conn, err := net.DialTimeout("tcp", p.addr(), 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("whisper engine did not become ready within 60s: %s", p.tail())
}

func (p *whisperProcess) addr() string { return "127.0.0.1:" + strconv.Itoa(p.port) }

func (p *whisperProcess) alive() bool {
	return p.cmd != nil && p.cmd.Process != nil && (p.cmd.ProcessState == nil || !p.cmd.ProcessState.Exited())
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
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd.Wait()
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
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if err := writeWAV(part, samples); err != nil {
		return "", err
	}
	if language == "" {
		language = "auto"
	}
	for k, v := range map[string]string{
		"response_format": "json",
		"language":        language,
		"temperature":     "0.0",
	} {
		if err := w.WriteField(k, v); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+p.addr()+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper engine: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper engine returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("whisper engine returned unreadable JSON: %s", strings.TrimSpace(string(raw)))
	}
	if out.Error != "" {
		return "", fmt.Errorf("whisper engine: %s", out.Error)
	}
	return cleanTranscript(out.Text), nil
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
