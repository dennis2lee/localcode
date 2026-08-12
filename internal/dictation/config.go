package dictation

import (
	"fmt"
	"os"
	"path/filepath"
)

// Config points at the model files a recognizer needs. The zero value is
// not usable: there is no sensible default model to fall back on, and
// guessing one would mean a silent download at first use.
type Config struct {
	// Engine picks a backend. Empty means "whatever is installed",
	// preferring whisper. See Engine for why they are not equals.
	Engine Engine
	// WhisperBin and WhisperModel override where the whisper engine and
	// its model are found. Empty means look beside this binary, which is
	// where the installer puts them. Both are ignored when WhisperURL is
	// set, since then nothing is run here.
	WhisperBin   string
	WhisperModel string
	// WhisperURL points at a whisper.cpp server on another machine, as
	// "host:port" or "http://host:port". Set, it wins over everything
	// local: no engine or model needs to be installed on this machine and
	// no child process is started.
	//
	// The trade is worth stating plainly, because it reverses this
	// feature's main property: with it set, recorded audio leaves this
	// machine. That is the point — it puts the work on a box with the
	// CPU or GPU for it — but it is no longer true that nothing is sent
	// anywhere, and the wire is plain HTTP.
	WhisperURL string

	// WhisperAPI names the remote server's dialect, or "" to discover it.
	// See whisperAPIs.
	WhisperAPI string
	// Language is the spoken language as an ISO 639-1 code ("ko", "en").
	// Empty means auto-detect, which is right for mixed speech and a
	// little slower and less certain for speech that is only ever one
	// language.
	Language string

	// ModelDir holds an unpacked sherpa-onnx streaming transducer model:
	// encoder/decoder/joiner .onnx plus tokens.txt. Sherpa only.
	ModelDir string
	// Threads is how many CPU threads the recognizer may use. 0 picks a
	// modest default — this runs alongside a model doing real work, and
	// taking every core to transcribe speech would slow down the thing
	// the speech is asking for.
	Threads int
}

// modelFiles are the files a streaming transducer model is made of, as
// named in every sherpa-onnx release archive.
type modelFiles struct {
	encoder string
	decoder string
	joiner  string
	tokens  string
	// bpeVocab is the sentencepiece model, present only in archives whose
	// vocabulary is BPE. "" for a model that has none, which is not an
	// error — see Open for what its presence changes.
	bpeVocab string
}

// resolveModel finds the model files in dir and explains precisely what
// is missing when it can't. "model not found" is a dead end for someone
// who has downloaded the wrong archive or unpacked it one level too
// deep, which is the usual mistake.
func resolveModel(dir string) (modelFiles, error) {
	if dir == "" {
		return modelFiles{}, fmt.Errorf("no dictation model directory configured")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return modelFiles{}, fmt.Errorf("dictation model directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return modelFiles{}, fmt.Errorf("dictation model path %s is not a directory", dir)
	}

	// int8 quantised weights are preferred when present: they are a third
	// of the size, and for a 60MB streaming model the accuracy difference
	// is not what limits this feature.
	pick := func(names ...string) string {
		for _, n := range names {
			p := filepath.Join(dir, n)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		return ""
	}

	m := modelFiles{
		encoder: pick("encoder-epoch-99-avg-1.int8.onnx", "encoder-epoch-99-avg-1.onnx"),
		decoder: pick("decoder-epoch-99-avg-1.int8.onnx", "decoder-epoch-99-avg-1.onnx"),
		joiner:  pick("joiner-epoch-99-avg-1.int8.onnx", "joiner-epoch-99-avg-1.onnx"),
		tokens:  pick("tokens.txt"),
		// Optional, and deliberately not in the missing-files check below:
		// plenty of models have no sentencepiece vocabulary at all.
		bpeVocab: pick("bpe.model"),
	}

	var missing []string
	for label, path := range map[string]string{
		"encoder-*.onnx": m.encoder,
		"decoder-*.onnx": m.decoder,
		"joiner-*.onnx":  m.joiner,
		"tokens.txt":     m.tokens,
	} {
		if path == "" {
			missing = append(missing, label)
		}
	}
	if len(missing) > 0 {
		return modelFiles{}, fmt.Errorf(
			"dictation model directory %s is missing %v — it should contain the unpacked contents of a sherpa-onnx streaming model archive, not the archive or a directory holding it",
			dir, missing)
	}
	return m, nil
}
