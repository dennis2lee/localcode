//go:build !gui || windows

package dictation

// Open reports that this build cannot do speech recognition.
//
// Two different builds land here.
//
// Every non-desktop build, because the real implementation is CGo (see
// sherpa.go) and the pure-Go builds the release pipeline cross-compiles
// from a single machine deliberately leave it out — the same arrangement
// internal/gui uses for its native window.
//
// And, for now, the Windows desktop build. sherpa-onnx-go-windows ships
// only DLLs — no .a or .lib import libraries — and MinGW's linker cannot
// resolve "-lsherpa-onnx-c-api" against a bare DLL, so the GUI build
// fails at link time with "have you installed the static version of the
// sherpa-onnx-c-api library?". macOS is unaffected because that module
// ships .dylib files, which -l resolves directly. Fixing it means
// generating import libraries in CI (gendef + dlltool) and shipping the
// three DLLs (~22MB) alongside the exe; until that is done and actually
// tested on Windows, this build says dictation is unavailable rather
// than failing to build at all.
//
// Either way the daemon says so plainly instead of appearing to have a
// broken microphone, and clients hide the button.
func Open(Config) (Recognizer, error) { return nil, ErrUnavailable }

// Available reports whether this build has a recognizer at all, so a
// client can hide the microphone button instead of offering one that
// can only fail.
func Available() bool { return false }
