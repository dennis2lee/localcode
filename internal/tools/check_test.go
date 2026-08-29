package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The point of this tool is that a model cannot influence what runs. A
// schema with no properties and `additionalProperties: false` is how
// that is said to the provider, and Execute ignoring its input is how it
// is true regardless of what a provider does with the schema.
func TestTheCheckCommandCannotBeInfluencedByTheModel(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	c := NewCheck(func() string { return "echo checked > " + marker })

	var schema struct {
		Properties           map[string]any `json:"properties"`
		AdditionalProperties *bool          `json:"additionalProperties"`
	}
	if err := json.Unmarshal(c.InputSchema(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if len(schema.Properties) != 0 {
		t.Errorf("the check tool takes %d arguments, want none", len(schema.Properties))
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Error("the schema allows extra properties, so a model can put something in the call")
	}

	// Whatever is in the input, the configured command is what runs.
	res := c.Execute(WithWorkingDir(context.Background(), dir),
		json.RawMessage(`{"command":"rm -rf /","args":["--danger"]}`))
	if res.IsError {
		t.Fatalf("check failed: %s", res.Content)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the configured command did not run: %v", err)
	}
	if !strings.Contains(res.Content, "passed") {
		t.Errorf("result = %q, want it to say the check passed", res.Content)
	}
}

// A check that fails is the answer, not a broken tool. Reporting a
// failing test suite as a tool error paints the one outcome the caller
// most needs to read as though the reviewer itself were broken.
func TestAFailingCheckIsAnAnswerNotAToolError(t *testing.T) {
	c := NewCheck(func() string { return "exit 3" })
	res := c.Execute(WithWorkingDir(context.Background(), t.TempDir()), json.RawMessage(`{}`))
	if res.IsError {
		t.Errorf("a failing check was reported as a tool error: %+v", res)
	}
	if !strings.Contains(res.Content, "failed") {
		t.Errorf("result = %q, want it to say the check failed", res.Content)
	}
}

// It runs in the session's own directory, like every other tool. A check
// that ran wherever the daemon was started would test the wrong project
// on a daemon serving two.
func TestTheCheckRunsInTheSessionsWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("here"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := NewCheck(func() string { return "cat marker.txt" })
	res := c.Execute(WithWorkingDir(context.Background(), dir), json.RawMessage(`{}`))
	if !strings.Contains(res.Content, "here") {
		t.Errorf("result = %q, want the file read from the session's directory", res.Content)
	}
}

// An unconfigured project says so rather than running something.
func TestAnUnconfiguredCheckSaysSo(t *testing.T) {
	res := Check{}.Execute(context.Background(), json.RawMessage(`{}`))
	if !res.IsError || !strings.Contains(res.Content, "verify_command") {
		t.Errorf("result = %+v, want it to name the missing setting", res)
	}
}

// The description carries the command, so the model knows what it is
// choosing to run, and it follows a configuration reloaded at runtime.
func TestTheDescriptionNamesTheCommandAndFollowsAReload(t *testing.T) {
	command := "go test ./..."
	c := NewCheck(func() string { return command })
	if !strings.Contains(c.Description(), "go test ./...") {
		t.Errorf("description = %q, want it to name the command", c.Description())
	}
	command = "make verify"
	if !strings.Contains(c.DescriptionFor(context.Background()), "make verify") {
		t.Errorf("description did not follow the reloaded command: %q", c.DescriptionFor(context.Background()))
	}
	if got := c.Subject(nil); got != "make verify" {
		t.Errorf("permission subject = %q, want the command a rule would match", got)
	}
}
