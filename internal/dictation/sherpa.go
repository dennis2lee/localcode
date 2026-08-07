//go:build gui && !windows

package dictation

import (
	"fmt"
	"runtime"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// sherpaRecognizer wraps a sherpa-onnx streaming transducer.
//
// A streaming transducer, not Whisper, because this feature shows text
// while someone is still speaking. Whisper processes a fixed window as a
// unit and has to be faked into looking live by re-transcribing an
// overlapping window over and over; a transducer emits partial results
// as audio arrives, which is the behaviour the grey text *is*.
type sherpaRecognizer struct {
	rec    *sherpa.OnlineRecognizer
	stream *sherpa.OnlineStream
}

// Open loads the model and returns a recognizer ready for audio.
func Open(cfg Config) (Recognizer, error) {
	files, err := resolveModel(cfg.ModelDir)
	if err != nil {
		return nil, err
	}

	threads := cfg.Threads
	if threads <= 0 {
		// Half the cores, at least one, at most four. This runs beside a
		// language model doing the actual work: taking every core to
		// transcribe speech would slow down the thing being asked for.
		threads = runtime.NumCPU() / 2
		if threads < 1 {
			threads = 1
		}
		if threads > 4 {
			threads = 4
		}
	}

	c := sherpa.OnlineRecognizerConfig{}
	c.FeatConfig.SampleRate = SampleRate
	c.FeatConfig.FeatureDim = 80
	c.ModelConfig.Transducer.Encoder = files.encoder
	c.ModelConfig.Transducer.Decoder = files.decoder
	c.ModelConfig.Transducer.Joiner = files.joiner
	c.ModelConfig.Tokens = files.tokens
	c.ModelConfig.NumThreads = threads
	c.ModelConfig.Provider = "cpu"
	c.DecodingMethod = "greedy_search"

	// Endpointing is what turns a stream of words into utterances, and
	// therefore what decides when grey text becomes committed text.
	// Rule 2 (a pause after speech) is the one that fires in practice;
	// 2.4s of silence is long enough to think mid-sentence without the
	// text being cut off under you, short enough not to feel stuck.
	c.EnableEndpoint = 1
	c.Rule1MinTrailingSilence = 2.4
	c.Rule2MinTrailingSilence = 1.2
	c.Rule3MinUtteranceLength = 20

	rec := sherpa.NewOnlineRecognizer(&c)
	if rec == nil {
		return nil, fmt.Errorf("could not create a recognizer from the model in %s", cfg.ModelDir)
	}
	stream := sherpa.NewOnlineStream(rec)
	if stream == nil {
		sherpa.DeleteOnlineRecognizer(rec)
		return nil, fmt.Errorf("could not open an audio stream for the model in %s", cfg.ModelDir)
	}
	return &sherpaRecognizer{rec: rec, stream: stream}, nil
}

// Available reports that this build can do speech recognition. Whether a
// *model* is present is a separate question, answered by Open.
func Available() bool { return true }

func (s *sherpaRecognizer) Accept(samples []float32) {
	if len(samples) == 0 {
		return
	}
	s.stream.AcceptWaveform(SampleRate, samples)
	// Decode as far as the audio allows before returning, so Partial()
	// reflects everything fed so far rather than lagging a chunk behind.
	for s.rec.IsReady(s.stream) {
		s.rec.Decode(s.stream)
	}
}

func (s *sherpaRecognizer) Partial() string {
	return s.rec.GetResult(s.stream).Text
}

func (s *sherpaRecognizer) Endpoint() bool {
	return s.rec.IsEndpoint(s.stream)
}

func (s *sherpaRecognizer) Reset() {
	s.rec.Reset(s.stream)
}

func (s *sherpaRecognizer) Close() {
	// Order matters: the stream belongs to the recognizer, so it goes
	// first. Freeing the recognizer out from under a live stream is a
	// use-after-free in C, not a Go error.
	if s.stream != nil {
		sherpa.DeleteOnlineStream(s.stream)
		s.stream = nil
	}
	if s.rec != nil {
		sherpa.DeleteOnlineRecognizer(s.rec)
		s.rec = nil
	}
}
