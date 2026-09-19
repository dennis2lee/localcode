package main

import (
	"strings"
	"testing"

	"localcode/internal/gui"
)

// A Windows user launching localcode-gui expects a desktop window. When that
// binary was compiled without the "gui" tag, it defaults to the terminal
// interface and says nothing. guiNameNote produces a single clear explanation
// when the binary name implies a GUI but the build has none, and stays silent
// for any console binary name or when a GUI is actually present.
func TestGUINameNote(t *testing.T) {
	stubText := gui.Unavailable()
	if stubText == "" {
		// In a -tags gui test run, gui.Unavailable() returns "". Provide the
		// representative stub explanation so the unit tests below always have
		// non-empty stub text to test against.
		stubText = "this build has no desktop window: it was compiled without -tags gui. " +
			"The macOS .app and the Windows .msi on the releases page have it, or build one with: go build -tags gui -o localcode-gui ./cmd/localcode. " +
			"Without it, run localcode and open the Web UI in a browser (http://127.0.0.1:4096 unless --listen says otherwise)"
	}

	tests := []struct {
		name        string
		exeBase     string
		unavailable string
		wantEmpty   bool
	}{
		{
			name:        "standard windows desktop exe name with unavailable note",
			exeBase:     "localcode-gui.exe",
			unavailable: stubText,
			wantEmpty:   false,
		},
		{
			name:        "standard unix desktop binary name with unavailable note",
			exeBase:     "localcode-gui",
			unavailable: stubText,
			wantEmpty:   false,
		},
		{
			name:        "all uppercase windows desktop exe name",
			exeBase:     "LOCALCODE-GUI.EXE",
			unavailable: stubText,
			wantEmpty:   false,
		},
		{
			name:        "mixed case unix desktop binary name",
			exeBase:     "LocalCode-Gui",
			unavailable: stubText,
			wantEmpty:   false,
		},
		{
			name:        "mixed case windows desktop exe name",
			exeBase:     "localcode-GUI.exe",
			unavailable: stubText,
			wantEmpty:   false,
		},
		{
			name:        "ordinary console binary name with stub text stays silent",
			exeBase:     "localcode.exe",
			unavailable: stubText,
			wantEmpty:   true,
		},
		{
			name:        "ordinary unix console binary name stays silent",
			exeBase:     "localcode",
			unavailable: stubText,
			wantEmpty:   true,
		},
		{
			name:        "uppercase console binary name stays silent",
			exeBase:     "LOCALCODE.EXE",
			unavailable: stubText,
			wantEmpty:   true,
		},
		{
			name:        "desktop exe name with empty unavailable stays silent",
			exeBase:     "localcode-gui.exe",
			unavailable: "",
			wantEmpty:   true,
		},
		{
			name:        "desktop unix name with empty unavailable stays silent",
			exeBase:     "localcode-gui",
			unavailable: "",
			wantEmpty:   true,
		},
		{
			name:        "unrelated binary name stays silent",
			exeBase:     "other-binary.exe",
			unavailable: stubText,
			wantEmpty:   true,
		},
		{
			name:        "empty binary name stays silent",
			exeBase:     "",
			unavailable: stubText,
			wantEmpty:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := guiNameNote(tt.exeBase, tt.unavailable)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("guiNameNote(%q, %q) = %q, want empty", tt.exeBase, tt.unavailable, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("guiNameNote(%q, %q) returned empty, want explanatory note", tt.exeBase, tt.unavailable)
			}
			// Which file, and what to do about it. Not the wording:
			// the note has to name the binary the person ran, since
			// two localcodes sit in the same directory, and it has to
			// hand over the explanation whole rather than paraphrase
			// it, since that is the sentence naming the MSI, the .app
			// and the Web UI.
			if !strings.Contains(got, tt.exeBase) {
				t.Errorf("note does not name the file that was run (%q): %q", tt.exeBase, got)
			}
			if !strings.Contains(got, tt.unavailable) {
				t.Errorf("note does not carry the explanation: %q", got)
			}
		})
	}
}
