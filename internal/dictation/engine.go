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
