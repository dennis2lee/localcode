package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunNoHooksRegisteredIsNoop(t *testing.T) {
	blocked, reason, warnings := Run(context.Background(), Config{}, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if blocked || reason != "" || len(warnings) != 0 {
		t.Errorf("Run() = (%v, %q, %v), want (false, \"\", nil)", blocked, reason, warnings)
	}
}

func TestRunAllowsByDefault(t *testing.T) {
	cfg := Config{EventPreToolUse: {{Command: "true"}}}
	blocked, _, warnings := Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if blocked {
		t.Error("expected a hook that exits 0 to not block")
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

func TestRunBlocksViaExitCode2(t *testing.T) {
	cfg := Config{EventPreToolUse: {{Command: "echo 'nope' >&2; exit 2"}}}
	blocked, reason, _ := Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if !blocked {
		t.Fatal("expected exit code 2 to block")
	}
	if reason != "nope" {
		t.Errorf("reason = %q, want %q (from stderr)", reason, "nope")
	}
}

func TestRunBlocksViaExitCode2WithNoStderrGetsFallbackReason(t *testing.T) {
	cfg := Config{EventPreToolUse: {{Command: "exit 2"}}}
	blocked, reason, _ := Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if !blocked {
		t.Fatal("expected exit code 2 to block")
	}
	if reason == "" {
		t.Error("expected a fallback reason when stderr is empty")
	}
}

func TestRunBlocksViaJSONDecision(t *testing.T) {
	cfg := Config{EventPreToolUse: {{Command: `echo '{"decision":"block","reason":"custom validation failed"}'`}}}
	blocked, reason, _ := Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if !blocked {
		t.Fatal("expected the JSON decision:block to block")
	}
	if reason != "custom validation failed" {
		t.Errorf("reason = %q, want %q", reason, "custom validation failed")
	}
}

func TestRunNonZeroExitWithoutBlockSignalIsWarningNotBlock(t *testing.T) {
	cfg := Config{EventPreToolUse: {{Command: "exit 1"}}}
	blocked, _, warnings := Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if blocked {
		t.Error("a plain nonzero exit (not code 2) should not block")
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %v", warnings)
	}
}

func TestRunMatcherFiltersRegisteredHooks(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	cfg := Config{EventPreToolUse: {{Matcher: "^write_file$", Command: "echo ran > " + marker}}}

	Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if _, err := os.Stat(marker); err == nil {
		t.Error("hook matched \"bash\" against pattern \"^write_file$\" and should not have run")
	}

	Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "write_file"})
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected the hook to run for a matching tool_name")
	}
}

func TestRunMatcherIsAnchoredToFullToolName(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	cfg := Config{EventPreToolUse: {{Matcher: "bash", Command: "echo ran > " + marker}}}

	Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "mcp__server__run_bash"})
	if _, err := os.Stat(marker); err == nil {
		t.Error("matcher \"bash\" should not match tool \"mcp__server__run_bash\" (substring match)")
	}

	Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if _, err := os.Stat(marker); err != nil {
		t.Error("matcher \"bash\" should match tool \"bash\" exactly")
	}
}

func TestRunMatcherAlternationAndWildcards(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	cfg := Config{EventPreToolUse: {{Matcher: "bash|mcp__github__.*", Command: "echo ran > " + marker}}}

	Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "mcp__github__create_issue"})
	if _, err := os.Stat(marker); err != nil {
		t.Error("matcher alternation with a wildcard arm should match mcp__github__create_issue")
	}

	os.Remove(marker)
	Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "edit"})
	if _, err := os.Stat(marker); err == nil {
		t.Error("matcher \"bash|mcp__github__.*\" should not match tool \"edit\"")
	}
}

func TestRunEmptyMatcherAlwaysRuns(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	cfg := Config{EventPreToolUse: {{Command: "echo ran > " + marker}}}

	Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "anything"})
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected an empty matcher to always run")
	}
}

func TestRunStopsAtFirstBlockingHook(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "second-ran")
	cfg := Config{EventPreToolUse: {
		{Command: "exit 2"},
		{Command: "echo ran > " + marker}, // should never run
	}}

	blocked, _, _ := Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if !blocked {
		t.Fatal("expected the first hook to block")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the second hook should not have run once the first one blocked")
	}
}

func TestRunReceivesPayloadOnStdin(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "captured")
	cfg := Config{EventPreToolUse: {{Command: "cat > " + out}}}

	Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash", "session_id": "s1"})

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"tool_name":"bash"`) || !strings.Contains(got, `"session_id":"s1"`) {
		t.Errorf("captured stdin = %q, want it to contain the payload fields", got)
	}
}

func TestRunUnmatchedRegexInvalidPatternWarnsAndContinues(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	cfg := Config{EventPreToolUse: {
		{Matcher: "(unclosed", Command: "echo bad"},
		{Command: "echo ran > " + marker},
	}}

	blocked, _, warnings := Run(context.Background(), cfg, EventPreToolUse, "", map[string]any{"tool_name": "bash"})
	if blocked {
		t.Error("an invalid matcher should not block")
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning for the invalid regex, got %v", warnings)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected the second (valid) hook to still run after the invalid matcher was skipped")
	}
}

// A hook is a shell command, and where it runs decides what it sees. It
// used to run in the daemon's own working directory: a `git status`, a
// `./scripts/check.sh`, a formatter reaching for the project's config all
// looked at whichever directory localcode was started in, however far the
// session that triggered them had moved.
func TestRunExecutesInTheDirectoryItIsGiven(t *testing.T) {
	project := t.TempDir()
	out := filepath.Join(t.TempDir(), "where")
	cfg := Config{EventPreToolUse: {{Command: "pwd > " + out}}}

	Run(context.Background(), cfg, EventPreToolUse, project, map[string]any{"tool_name": "bash"})

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read pwd output: %v", err)
	}
	got := strings.TrimSpace(string(data))
	// Resolved on both sides: a temp dir on macOS is reached through
	// /var -> /private/var, and pwd reports what it resolves to.
	want, _ := filepath.EvalSymlinks(project)
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		got = resolved
	}
	if got != want {
		t.Errorf("the hook ran in %q, want the directory it was given (%q)", got, want)
	}
}

// The same directory is on stdin as well, because a hook that shells out
// elsewhere, or appends to a log shared by several projects, cannot work
// out which project it was called about from anything else in the payload.
func TestRunPutsTheDirectoryInThePayload(t *testing.T) {
	project := t.TempDir()
	out := filepath.Join(t.TempDir(), "captured")
	cfg := Config{EventPreToolUse: {{Command: "cat > " + out}}}

	payload := map[string]any{"tool_name": "bash"}
	Run(context.Background(), cfg, EventPreToolUse, project, payload)

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	if !strings.Contains(string(data), `"cwd":`) || !strings.Contains(string(data), project) {
		t.Errorf("captured stdin = %q, want a cwd of %q", string(data), project)
	}
	if _, added := payload["cwd"]; added {
		t.Error("the caller's own payload map was written into; it should have been copied")
	}
}
