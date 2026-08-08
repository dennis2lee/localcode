package dictation

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// Diagnosis is what a recognizer produced for one recording, in enough
// detail to tell two different faults apart.
//
// "The transcript is wrong" is really two complaints that need different
// fixes, and from the finished text alone they look identical:
//
//   - The tokens are right and the joining is wrong. Sentencepiece marks
//     the start of a word with "▁"; if that is not turned into a space,
//     a correct transcript still reads as one unbroken run of characters.
//     That is a decoding fault, fixable here.
//   - The tokens themselves are wrong. That is the model mishearing, and
//     no amount of decoding will repair it.
//
// The token list separates them at a glance, which is why this exists:
// the same look at the tokens is what identified a different fault in
// Whisper, where Korean syllables arrived as empty strings because
// byte-level BPE pieces were being lost.
type Diagnosis struct {
	// Text is what dictation would have typed into the prompt box.
	Text string
	// RawText is what the recognizer's own joining produced, kept so a
	// rebuild can be seen for what it is rather than taken on faith.
	RawText string
	// Rebuilt reports that Text was reassembled from the tokens because
	// RawText had lost the spacing. See joinTokens.
	Rebuilt bool
	// Tokens is what the model actually produced, before joining.
	Tokens []string
	// WordMarks counts tokens that mark the start of a word — either the
	// sentencepiece "▁" prefix or a plain leading space, since models
	// reach us both ways. A count well above zero alongside a text with
	// no spaces in it is the signature of a joining fault.
	WordMarks int
	// Empty counts tokens that decoded to nothing. Any at all means
	// pieces of the transcript are being dropped between the model and
	// the text.
	Empty int
	// AudioSeconds is the length of the recording, so a result that is
	// obviously too short to match is visible as such.
	AudioSeconds float64
}

// String renders a diagnosis for a terminal, leading with the reading of
// it rather than making the reader work the meaning out from the numbers.
func (d Diagnosis) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "audio:  %.2fs\n", d.AudioSeconds)
	fmt.Fprintf(&b, "text:   %q\n", d.Text)
	if d.Rebuilt {
		fmt.Fprintf(&b, "  (rebuilt from tokens; the recognizer returned %q)\n", d.RawText)
	}
	fmt.Fprintf(&b, "tokens: %d (%d start a word, %d decoded to nothing)\n", len(d.Tokens), d.WordMarks, d.Empty)
	fmt.Fprintf(&b, "        %q\n", d.Tokens)
	fmt.Fprintf(&b, "valid utf-8: %v\n\n", utf8.ValidString(d.Text))

	switch {
	case len(d.Tokens) == 0:
		b.WriteString("Reading: the model produced nothing at all. That is not a decoding\n" +
			"problem — either the audio never reached it, or this model does not run\n" +
			"on this machine. Check that the recording is 16 kHz mono, and re-run with\n" +
			"LC_SHERPA_DEBUG=1 to see what the model reported when it loaded.\n")
	case d.Empty > 0:
		b.WriteString("Reading: some tokens decoded to nothing, so pieces of the transcript are\n" +
			"being lost between the model and the text. This is a decoding fault, not\n" +
			"the model mishearing. It is what happens when a vocabulary splits a\n" +
			"character across several tokens and the pieces are not reassembled.\n")
	case d.Rebuilt:
		b.WriteString("Reading: the tokens mark where words begin and the recognizer's own\n" +
			"text had lost those marks, so the spacing above was rebuilt from the\n" +
			"tokens. That part is working as intended. Accuracy is a separate\n" +
			"question: compare the tokens above against what you actually said.\n")
	case d.WordMarks > 0 && !strings.Contains(strings.TrimSpace(d.Text), " "):
		b.WriteString("Reading: the tokens mark where words begin, but the text has no spaces\n" +
			"in it — so the marks are not being turned into spaces. A decoding fault,\n" +
			"and a fixable one. Accuracy is a separate question: compare the tokens\n" +
			"above against what you actually said.\n")
	case d.WordMarks == 0:
		b.WriteString("Reading: no token marks the start of a word, so this vocabulary carries\n" +
			"no word boundaries and spacing cannot come from decoding. Any spacing has\n" +
			"to come from the model itself or from a later step.\n")
	default:
		b.WriteString("Reading: the tokens carry word boundaries and the text has spaces, so\n" +
			"decoding is doing its job. If the transcript is still wrong, the model is\n" +
			"mishearing — compare the tokens above against what you actually said.\n")
	}
	return b.String()
}

// ReadWAV loads 16-bit PCM from a .wav file as the mono float32 samples a
// recognizer wants, and reports the sample rate it found.
//
// Deliberately strict about what it accepts and specific about what it
// rejects: a diagnosis run on audio the recognizer was never going to
// understand is worse than no diagnosis, because it blames the model for
// the file.
func ReadWAV(path string) ([]float32, int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("%s is not a WAV file", path)
	}
	channels := int(binary.LittleEndian.Uint16(b[22:24]))
	rate := int(binary.LittleEndian.Uint32(b[24:28]))
	bits := int(binary.LittleEndian.Uint16(b[34:36]))

	// Walk the chunks rather than assuming the data starts at byte 44:
	// plenty of recorders write a LIST or fact chunk first, and reading
	// that as audio produces a burst of noise the model then dutifully
	// transcribes as nonsense.
	var data []byte
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		if id == "data" {
			end := off + 8 + size
			if end > len(b) {
				end = len(b)
			}
			data = b[off+8 : end]
			break
		}
		off += 8 + size + (size & 1) // chunks are word-aligned
	}
	if data == nil {
		return nil, 0, fmt.Errorf("%s has no audio data chunk", path)
	}
	if bits != 16 {
		return nil, 0, fmt.Errorf("%s is %d-bit; this needs 16-bit PCM", path, bits)
	}
	if channels != 1 {
		return nil, 0, fmt.Errorf("%s has %d channels; this needs mono", path, channels)
	}
	if rate != SampleRate {
		return nil, 0, fmt.Errorf("%s is %d Hz; this needs %d Hz", path, rate, SampleRate)
	}

	samples := make([]float32, len(data)/2)
	for i := range samples {
		samples[i] = float32(int16(binary.LittleEndian.Uint16(data[i*2:]))) / 32768
	}
	return samples, rate, nil
}

// summarize counts what matters in a token list.
func summarize(tokens []string) (wordMarks, empty int) {
	for _, t := range tokens {
		if t == "" {
			empty++
			continue
		}
		// Both spellings of "a word starts here". Counting only "▁"
		// reported zero marks for a Korean model whose tokens plainly
		// carried spaces, and the reading below then blamed the
		// vocabulary for a fault that was in the joining.
		if strings.HasPrefix(t, wordMark) || strings.HasPrefix(t, " ") {
			wordMarks++
		}
	}
	return wordMarks, empty
}
