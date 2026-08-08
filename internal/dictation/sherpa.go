//go:build gui

package dictation

import (
	"fmt"
	"os"
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

	// Word boundaries.
	//
	// sherpa's modelling unit defaults to "cjkchar", which treats every
	// token as a character and joins them with nothing between. For a
	// model whose vocabulary is actually sentencepiece BPE that is wrong
	// in a specific, visible way: the "▁" prefix that marks the start of
	// a word is never acted on, so a sentence comes out as one unbroken
	// run of characters with no spaces at all.
	//
	// The Korean model this ships with is exactly that case — 5000
	// tokens, 2352 of them carrying "▁", and a bpe.model in the archive.
	// Its own reference transcripts have spaces; ours did not.
	//
	// Keyed off the file rather than the model's name: a sentencepiece
	// vocabulary is what makes a model BPE, and an archive that has one
	// wants to be decoded as BPE whoever produced it. Models without one
	// keep sherpa's default, unchanged.
	if files.bpeVocab != "" {
		c.ModelConfig.ModelingUnit = "bpe"
		c.ModelConfig.BpeVocab = files.bpeVocab
	}
	// An escape hatch, because the above is a judgement about someone
	// else's model file and this is not a setting anyone should have to
	// discover to get working speech. LC_SHERPA_MODELING_UNIT accepts
	// sherpa's own values — cjkchar, bpe, cjkchar+bpe — and "cjkchar"
	// restores the previous behaviour exactly.
	if unit := os.Getenv("LC_SHERPA_MODELING_UNIT"); unit != "" {
		c.ModelConfig.ModelingUnit = unit
		if unit == "cjkchar" {
			c.ModelConfig.BpeVocab = ""
		}
	}
	// LC_SHERPA_DEBUG=1 makes sherpa print what it loaded and how it
	// understood the model — vocabulary size, encoder shape, and the
	// modeling unit it settled on. That is the only way to tell a model
	// that failed to load from one that loaded and decodes to nothing,
	// and this is a build that only exists on machines the author may
	// not have. Off by default: it is pages of output on stderr.
	if os.Getenv("LC_SHERPA_DEBUG") != "" {
		c.ModelConfig.Debug = 1
	}
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

// Diagnose transcribes one recording and reports the tokens behind the
// text as well as the text itself — see Diagnosis for why both are
// needed to tell a decoding fault from a model that is mishearing.
//
// It runs the same recognizer dictation uses, configured the same way, so
// what it reports is what the microphone would have produced. The audio
// is fed in one go and flushed with InputFinished, which is what releases
// the last words of an utterance; a live session gets that from the
// endpoint detector instead.
func Diagnose(cfg Config, samples []float32) (Diagnosis, error) {
	rec, err := Open(cfg)
	if err != nil {
		return Diagnosis{}, err
	}
	defer rec.Close()

	s, ok := rec.(*sherpaRecognizer)
	if !ok {
		return Diagnosis{}, ErrUnavailable
	}

	s.Accept(samples)
	// Half a second of silence, then end of input. A streaming model
	// holds back its last few tokens waiting for more audio; without
	// this the tail of every recording is missing, which would look
	// exactly like the model mishearing the end of the sentence.
	s.Accept(make([]float32, SampleRate/2))
	s.stream.InputFinished()
	for s.rec.IsReady(s.stream) {
		s.rec.Decode(s.stream)
	}

	res := s.rec.GetResult(s.stream)
	marks, empty := summarize(res.Tokens)
	return Diagnosis{
		Text:         res.Text,
		Tokens:       res.Tokens,
		WordMarks:    marks,
		Empty:        empty,
		AudioSeconds: float64(len(samples)) / float64(SampleRate),
	}, nil
}
