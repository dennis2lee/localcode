package dictation

import (
	"os"
	"strings"
	"testing"
	"time"
)

// An end-to-end run against a real whisper engine, driven exactly as the
// daemon drives it: audio in through Session.Write, text out through
// Result.
//
// Skipped unless LC_TEST_WHISPER_BIN and LC_TEST_WHISPER_MODEL point at
// an installed engine, because the model is hundreds of megabytes and
// nobody should have to download one to run `go test`. It exists because
// every unit test here is of a part, and the parts agreeing does not
// mean audio in at one end produces Korean out at the other — which is
// the only claim that matters.
func TestWhisperEndToEnd(t *testing.T) {
	bin := os.Getenv("LC_TEST_WHISPER_BIN")
	model := os.Getenv("LC_TEST_WHISPER_MODEL")
	wav := os.Getenv("LC_TEST_WHISPER_WAV")
	want := os.Getenv("LC_TEST_WHISPER_WANT")
	if bin == "" || model == "" || wav == "" {
		t.Skip("set LC_TEST_WHISPER_BIN, LC_TEST_WHISPER_MODEL and LC_TEST_WHISPER_WAV to run")
	}

	samples, _, err := ReadWAV(wav)
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{Engine: EngineWhisper, WhisperBin: bin, WhisperModel: model, Language: "ko"}
	if ready, why := NewManager(cfg).Ready(); !ready {
		t.Fatalf("not ready: %s", why)
	}
	rec, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession(rec)

	// Fed in 100ms chunks, the way a browser's AudioWorklet sends it,
	// rather than in one go: the chunking is where a buffering or VAD
	// framing mistake would show up.
	const chunk = SampleRate / 10
	var final string
	for off := 0; off < len(samples); off += chunk {
		end := off + chunk
		if end > len(samples) {
			end = len(samples)
		}
		res, err := sess.Write(pcm16(samples[off:end]))
		if err != nil {
			t.Fatal(err)
		}
		if res.Final != "" {
			final = res.Final
		}
	}
	// Trailing silence, which is what tells the VAD the speaker stopped.
	for i := 0; i < 15 && final == ""; i++ {
		res, err := sess.Write(pcm16(make([]float32, chunk)))
		if err != nil {
			t.Fatal(err)
		}
		if res.Final != "" {
			final = res.Final
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final == "" {
		final = sess.Stop().Final
	} else {
		sess.Stop()
	}

	t.Logf("transcript: %q", final)
	if final == "" {
		t.Fatal("no text at all came out of the engine")
	}
	if !strings.Contains(final, " ") {
		t.Errorf("transcript has no spaces in it, which is the fault this engine replaced: %q", final)
	}
	if want != "" && !strings.Contains(final, want) {
		t.Errorf("transcript %q does not contain %q", final, want)
	}
}

// pcm16 is the wire format Session.Write takes, so the test exercises
// the same decode path the daemon does.
func pcm16(samples []float32) []byte {
	out := make([]byte, len(samples)*2)
	for i, s := range samples {
		v := int16(s * 32767)
		out[i*2] = byte(v)
		out[i*2+1] = byte(v >> 8)
	}
	return out
}
