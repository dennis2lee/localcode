package dictation

import "fmt"

// Engine names a speech recognition backend.
//
// There are two, and they are not alternatives of equal standing.
//
// Sherpa is CGo. That is why dictation has always been desktop-only:
// the release pipeline cross-compiles every platform from one machine,
// and CGo cannot be cross-compiled, so the TUI, the headless daemon and
// the macOS release build have no recognizer at all.
//
// Whisper runs as a child process, so the Go side of it is ordinary Go
// that compiles everywhere. Dictation stops being a property of how the
// binary was built and becomes a property of what is installed beside
// it.
type Engine string

const (
	// EngineWhisper drives whisper.cpp's own server binary over HTTP.
	// See whisperProcess for why an upstream binary rather than one of
	// ours.
	EngineWhisper Engine = "whisper"
	// EngineSherpa is the original in-process sherpa-onnx streaming
	// transducer. Frozen: it works where it is already installed, and
	// nothing new is being built on it.
	EngineSherpa Engine = "sherpa"
)

// resolveEngine decides which backend a config is asking for, and says
// what is missing when it is asking for nothing available.
//
// Explicit wins. Otherwise whisper is preferred wherever it is
// installed, because it is the one that works in every build, and
// sherpa is the fallback so an existing setup keeps running untouched
// after an upgrade.
func (c Config) resolveEngine() (Engine, error) {
	whisperOK := c.whisperReady() == nil
	sherpaOK := sherpaAvailable() && resolveModelErr(c.ModelDir) == nil

	switch c.Engine {
	case EngineWhisper:
		if err := c.whisperReady(); err != nil {
			return "", err
		}
		return EngineWhisper, nil
	case EngineSherpa:
		if !sherpaAvailable() {
			return "", ErrUnavailable
		}
		if err := resolveModelErr(c.ModelDir); err != nil {
			return "", err
		}
		return EngineSherpa, nil
	case "":
		switch {
		case whisperOK:
			return EngineWhisper, nil
		case sherpaOK:
			return EngineSherpa, nil
		}
		// Neither. Report against whisper, since that is the one a new
		// install is meant to end up with and the one whose absence is
		// fixable by installing something.
		return "", c.whisperReady()
	default:
		return "", fmt.Errorf("unknown dictation engine %q (want %q or %q)", c.Engine, EngineWhisper, EngineSherpa)
	}
}

// resolveModelErr is resolveModel reduced to its error, for the readiness
// checks that do not need the file paths.
func resolveModelErr(dir string) error {
	_, err := resolveModel(dir)
	return err
}

// Open starts a recognizer for whichever engine the config resolves to.
func Open(cfg Config) (Recognizer, error) {
	engine, err := cfg.resolveEngine()
	if err != nil {
		return nil, err
	}
	switch engine {
	case EngineWhisper:
		return openWhisper(cfg)
	default:
		return openSherpa(cfg)
	}
}

// Describe names the engine a config resolves to, for `dictation status`
// — which is otherwise a bare "ready" that does not say whether the audio
// is staying on this machine.
func Describe(cfg Config) string {
	engine, err := cfg.resolveEngine()
	if err != nil {
		return ""
	}
	if engine == EngineSherpa {
		return "sherpa (local, in-process)"
	}
	if host := cfg.RemoteHost(); host != "" {
		return "whisper at " + host + " (remote — recorded audio leaves this machine)"
	}
	bin, model, err := cfg.whisperPaths()
	if err != nil {
		return "whisper (local)"
	}
	return "whisper (local): " + bin + " with " + model
}

// SpokenLanguageNote explains why the spoken-language setting is not in
// force, or "" when it is.
//
// Not every engine has a language to be set, and one of them cannot even
// be asked. Sherpa is one model per language — the model localcode
// installs is Korean — so English spoken into it does not come back in
// English or as nothing: it comes back *transliterated*, "I'm a boy" as
// 아이엠어보이, while the settings panel says Spoken language: English the
// whole time. Nothing on screen connects the two, and the reasonable
// conclusion is that dictation is broken.
//
// A control that cannot do anything has to say so. This is the sentence
// that says it.
func SpokenLanguageNote(cfg Config) string {
	engine, err := cfg.resolveEngine()
	if err != nil {
		return ""
	}
	return spokenLanguageNote(engine)
}

// spokenLanguageNote is the note for a resolved engine, split out because
// which engine a config resolves to depends on what is installed on the
// machine the test happens to run on — and this sentence does not.
func spokenLanguageNote(engine Engine) string {
	if engine != EngineSherpa {
		return ""
	}
	return "The sherpa engine is one model per language, and the model localcode installs is Korean — " +
		"the language above does not apply to it. English spoken into it is written out in Hangul " +
		"rather than in English. Run `localcode dictation install` to add whisper, which is " +
		"multilingual and is preferred wherever it is installed."
}

// Available reports whether this process could dictate for *some*
// configuration. It is a build-and-installation question, not a
// configuration one: a build with no sherpa compiled in and no whisper
// binary installed can never dictate, and a client should hide the
// microphone rather than offer a button that can only fail.
func Available() bool { return AvailableFor(Config{}) }

// AvailableFor is Available for a specific configuration, so a
// configured whisper_bin counts. Available() answers for the default
// locations only, which reported "no recognizer" on a machine where
// dictation would in fact have started.
func AvailableFor(cfg Config) bool {
	if sherpaAvailable() {
		return true
	}
	if cfg.RemoteHost() != "" {
		return true
	}
	return findWhisperBin(cfg.WhisperBin) != ""
}
