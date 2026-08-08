//go:build !gui

package dictation

// Open reports that this build cannot do speech recognition.
//
// The real implementation is CGo (see sherpa.go), which the pure-Go
// builds the release pipeline cross-compiles from a single machine
// deliberately leave out — the same arrangement internal/gui uses for
// its native window. A TUI or headless daemon therefore says so plainly
// rather than appearing to have a broken microphone, and clients hide
// the button instead of offering one that can only fail.
func Open(Config) (Recognizer, error) { return nil, ErrUnavailable }

// Available reports whether this build has a recognizer at all, so a
// client can hide the microphone button instead of offering one that
// can only fail.
func Available() bool { return false }

// Diagnose reports that this build cannot do speech recognition. The
// real one is CGo (see sherpa.go); on Windows that is
// localcode-gui.exe, not localcode.exe.
func Diagnose(Config, []float32) (Diagnosis, error) { return Diagnosis{}, ErrUnavailable }
