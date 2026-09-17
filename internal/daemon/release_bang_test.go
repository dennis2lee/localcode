package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
)

// A "!" line runs a shell command in the session's workspace and its
// output lands in the conversation, without ever reaching the model.
// The mock model counts requests, so sendCommand fails the test if the
// turn touches it at all.
func TestBangRunsTheCommandAndShowsItsOutput(t *testing.T) {
	h := newReleaseTestHarness(t)

	const content = "bang-output-marker-4123"
	if err := os.WriteFile(filepath.Join(h.wsDir, "note.txt"), []byte(content+"\n"), 0o600); err != nil {
		t.Fatalf("write note.txt: %v", err)
	}

	// Relative on purpose: it pins that the command runs in the
	// session's workspace rather than wherever the daemon started.
	reply := h.sendCommand(t, "!cat note.txt")
	if !strings.Contains(reply, content) {
		t.Errorf("bang reply missing file content %q:\n%s", content, reply)
	}

	evs, err := h.daemon.Loop.Store.Events(h.session.ID, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var sawUser, sawShell bool
	for _, ev := range evs {
		if ev.Type == events.TypeUserMessage {
			if txt, _ := ev.Data["text"].(string); txt == "!cat note.txt" {
				sawUser = true
				if local, _ := ev.Data["local"].(bool); local {
					t.Errorf("bang user message is local, so the model would never see the person ran it")
				}
			}
		}
		if ev.Type == events.TypeMessagePartEnd {
			if cmd, _ := ev.Data["shell_command"].(string); cmd == "cat note.txt" {
				sawShell = true
				if txt, _ := ev.Data["text"].(string); !strings.Contains(txt, content) {
					t.Errorf("shell output event missing file content %q:\n%s", content, txt)
				}
			}
		}
	}
	if !sawUser {
		t.Errorf("no user message holding exactly what was typed")
	}
	if !sawShell {
		t.Errorf("no message.part.end carrying the output with its shell_command")
	}
}

// A failing command must show the failure: the exit status rides on the
// reply, so a typo never reads as an empty answer.
func TestBangFailureShowsTheExitStatus(t *testing.T) {
	h := newReleaseTestHarness(t)

	reply := h.sendCommand(t, "!exit 3")
	if !strings.Contains(reply, "exited with status 3") {
		t.Errorf("failing bang reply missing the exit status:\n%s", reply)
	}
}

// A shell escape hands back what the shell wrote, whole.
//
// Neither the bash tool nor the turn loop trims a command's output, and a
// shell escape is the bash tool with the permission gate taken off — so
// trimming here would make "!" and the tool disagree about the same
// command, which is the drift this feature exists to avoid. The cost is
// real and is documented rather than hidden: "!find /" puts everything it
// prints into the conversation.
func TestBangHandsBackTheWholeOutput(t *testing.T) {
	h := newReleaseTestHarness(t)

	reply := h.sendCommand(t, "!seq 1 2000")
	if !strings.Contains(reply, "\n2000") {
		t.Errorf("the last line of the output is missing, so something trimmed it (last 200 chars):\n%.200s", reply[max(0, len(reply)-200):])
	}
	if strings.Contains(reply, "omitted") || strings.Contains(reply, "…") {
		t.Errorf("the reply carries a truncation marker the bash tool never adds:\n%.200s", reply)
	}
}

// "!" on its own runs nothing and reaches no model: a local usage error
// answers it.
func TestBangAloneSaysWhatToRun(t *testing.T) {
	h := newReleaseTestHarness(t)

	h.sendCommand(t, "!")

	evs, err := h.daemon.Loop.Store.Events(h.session.ID, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var sawErr bool
	for _, ev := range evs {
		if ev.Type == events.TypeError {
			if msg, _ := ev.Data["error"].(string); strings.Contains(msg, "say what to run") {
				sawErr = true
			}
		}
	}
	if !sawErr {
		t.Errorf("bare ! produced no usage error")
	}
}

// "!!hello" sends "!hello" to the model and runs nothing. The marker
// file staying absent is the proof: the escaped line names a command
// with a visible side effect, and the side effect must not happen.
func TestBangEscapeSendsAnOrdinaryMessage(t *testing.T) {
	h := newReleaseTestHarness(t)

	marker := filepath.Join(t.TempDir(), "escaped-marker.txt")
	h.modelReqs.Store(0)
	ctx := context.Background()
	if err := h.client.SendMessage(ctx, h.session.ID, "!!touch "+marker); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-h.evCh:
			if !ok {
				t.Fatalf("events channel closed waiting for the escaped message")
			}
			if ev.Type == events.TypeTurnDone {
				goto done
			}
		case <-timeout:
			t.Fatalf("timed out waiting for the escaped message to complete")
		}
	}
done:
	if reqs := h.modelReqs.Load(); reqs == 0 {
		t.Errorf("escaped message never reached the model")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("escaped message ran a command: %s exists", marker)
	}
	evs, err := h.daemon.Loop.Store.Events(h.session.ID, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var sawUser bool
	for _, ev := range evs {
		if ev.Type == events.TypeUserMessage {
			if txt, _ := ev.Data["text"].(string); txt == "!touch "+marker {
				sawUser = true
			}
		}
		if ev.Type == events.TypeMessagePartEnd {
			if _, ok := ev.Data["shell_command"]; ok {
				t.Errorf("escaped message produced command output")
			}
		}
	}
	if !sawUser {
		t.Errorf("no user message holding the unescaped !touch line")
	}
}

// A plain "!" line only ever executes: paired with the escape test
// above, the two pin that "!!" is the one way to send a message
// beginning with "!".
func TestBangPlainLineAlwaysExecutes(t *testing.T) {
	h := newReleaseTestHarness(t)

	reply := h.sendCommand(t, "!echo executed-marker-9917")
	if !strings.Contains(reply, "executed-marker-9917") {
		t.Errorf("plain ! line did not execute:\n%s", reply)
	}
}

// A model-composed prompt beginning with "!" is never executed, driven
// through the real delegated path: SpawnSync wraps the prompt the way
// the Task tool does, and the marker file staying absent proves the
// shell never ran.
func TestBangNeverRunsFromAModelComposedPrompt(t *testing.T) {
	h := newReleaseTestHarness(t)

	marker := filepath.Join(t.TempDir(), "delegated-marker.txt")
	if _, err := h.daemon.Tasks.SpawnSync(context.Background(), h.session.ID, "build", "!touch "+marker); err != nil {
		t.Fatalf("SpawnSync: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("delegated ! prompt ran a command: %s exists", marker)
	}
}

// Nothing that was routable before routes differently now: an ordinary
// prompt still reaches the model, and a local slash command and an
// unknown one are still answered without it.
func TestBangChangesNoExistingRoute(t *testing.T) {
	h := newReleaseTestHarness(t)

	ctx := context.Background()
	h.modelReqs.Store(0)
	if err := h.client.SendMessage(ctx, h.session.ID, "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-h.evCh:
			if !ok {
				t.Fatalf("events channel closed waiting for the ordinary prompt")
			}
			if ev.Type == events.TypeTurnDone {
				goto done
			}
		case <-timeout:
			t.Fatalf("timed out waiting for the ordinary prompt to complete")
		}
	}
done:
	if reqs := h.modelReqs.Load(); reqs == 0 {
		t.Errorf("ordinary prompt no longer reaches the model")
	}

	reply := h.sendCommand(t, "/status")
	if !strings.Contains(reply, "MCP servers") {
		t.Errorf("/status no longer answered locally:\n%s", reply)
	}

	// Refused with the build's own wording, and never sent to the model:
	// that refusal is the reason a slash is read as a command at all.
	unknown := h.sendCommand(t, "/definitely-not-a-command-xyz")
	if !strings.Contains(unknown, "There is no /definitely-not-a-command-xyz in this build") {
		t.Errorf("unknown slash no longer refused as before:\n%s", unknown)
	}
}
