//go:build !gui

package dictation

// The sherpa recognizer is CGo, and the release pipeline cross-compiles
// every platform from one machine, so it is absent from all but the
// desktop build. See engine.go: this is exactly the limitation the
// whisper engine exists to remove, and dictation still works in this
// build through that path.

func openSherpa(Config) (Recognizer, error) { return nil, ErrUnavailable }

// sherpaAvailable reports that this build has no sherpa compiled in.
func sherpaAvailable() bool { return false }

// Diagnose needs a recognizer that can report its own tokens, which is a
// sherpa capability. The whisper engine returns text, not tokens, so
// there is nothing for this to take apart.
func Diagnose(Config, []float32) (Diagnosis, error) { return Diagnosis{}, ErrUnavailable }
