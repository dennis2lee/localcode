package dictation

import (
	"math"
	"time"
)

// Voice activity detection, by loudness.
//
// Deliberately the simplest thing that works. A streaming transducer
// brought its own endpoint detector; whisper.cpp has no notion of "the
// speaker has stopped" at all, so something here has to decide when an
// utterance is finished and the grey text becomes committed text.
//
// Energy alone is enough for the job this does. It is not deciding what
// is speech, only whether the microphone has gone quiet for about a
// second, and the cost of being wrong is a sentence committed a beat
// early or late rather than a wrong transcript. Silero and friends are
// a better detector and a model to ship, load and keep; that trade is
// not worth making until this one is observed failing.
const (
	// vadFrame is the window energy is measured over. 30ms is the usual
	// choice: long enough to average out a single glottal pulse, short
	// enough to catch the gap between words.
	vadFrame = SampleRate / 1000 * 30

	// speechRMS is the loudness above which a frame counts as speech.
	// Room tone on a laptop microphone sits well below this; ordinary
	// speech sits well above it.
	speechRMS = 0.012

	// silenceToEndpoint is how much quiet ends an utterance. Long enough
	// to think mid-sentence without the text being cut off under you,
	// short enough that finishing a sentence does not feel like waiting.
	silenceToEndpoint = 900 * time.Millisecond

	// minSpeechForEndpoint stops a cough or a door closing from being
	// committed as an utterance of its own.
	minSpeechForEndpoint = 300 * time.Millisecond
)

// vad tracks whether the speaker is talking, from the audio alone.
type vad struct {
	// speech and silence are counted in samples rather than wall clock:
	// audio arrives in chunks whose arrival time says more about the
	// network than about when the words were said.
	speechSamples  int
	silenceSamples int
	partial        []float32 // carried between calls, less than one frame
}

// feed measures samples and reports whether an utterance has just ended.
func (v *vad) feed(samples []float32) (endpoint bool) {
	buf := samples
	if len(v.partial) > 0 {
		buf = append(v.partial, samples...)
		v.partial = nil
	}
	n := len(buf) / vadFrame
	for i := 0; i < n; i++ {
		if rms(buf[i*vadFrame:(i+1)*vadFrame]) >= speechRMS {
			v.speechSamples += vadFrame
			v.silenceSamples = 0
			continue
		}
		// Only counts as silence once something has been said. Quiet
		// before an utterance is just quiet, and letting it accumulate
		// would fire an endpoint on an empty buffer.
		if v.speechSamples > 0 {
			v.silenceSamples += vadFrame
		}
	}
	// The remainder is kept whole rather than measured short: a partial
	// frame has less energy than a full one and would read as silence.
	if rest := buf[n*vadFrame:]; len(rest) > 0 {
		v.partial = append([]float32(nil), rest...)
	}

	return v.speechSamples >= durSamples(minSpeechForEndpoint) &&
		v.silenceSamples >= durSamples(silenceToEndpoint)
}

// spoke reports whether anything at all has been said since the last
// reset, so an utterance of pure silence is never sent for transcription.
func (v *vad) spoke() bool { return v.speechSamples > 0 }

func (v *vad) reset() { *v = vad{} }

func rms(frame []float32) float64 {
	var sum float64
	for _, s := range frame {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(frame)))
}

func durSamples(d time.Duration) int {
	return int(d.Seconds() * SampleRate)
}
