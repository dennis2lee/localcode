package dictation

import (
	"os/exec"
	"testing"
	"time"
)

// A process that has died has to look dead.
//
// exec.Cmd only fills in ProcessState when Wait or Run is called, and
// neither is called on a server that is meant to keep running. Checking
// it therefore reports "alive" forever: a crashed engine gets handed to
// the next session, which fails on every request with a connection
// error, and startup waits the full timeout instead of reporting what
// the engine printed on its way out.
func TestDeadEngineLooksDead(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 3")
	p := &whisperProcess{cmd: cmd}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a test process here: %v", err)
	}
	p.watch()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && p.alive() {
		time.Sleep(20 * time.Millisecond)
	}
	if p.alive() {
		t.Error("a process that exited two seconds ago still reports alive")
	}
}

// The engine outlives the last session on purpose, so that dictating
// again does not pay for reloading the model. It must not outlive the
// program: a server holding a few hundred megabytes, left running after
// localcode exits, is a process the user never started and has to find
// and kill by hand.
func TestShutdownStopsTheEngine(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30")
	p := &whisperProcess{cmd: cmd}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a test process here: %v", err)
	}
	p.watch()

	sharedMu.Lock()
	prev, prevKey := sharedWhisper, sharedKey
	sharedWhisper, sharedKey = p, "test"
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		sharedWhisper, sharedKey = prev, prevKey
		sharedMu.Unlock()
	})

	Shutdown()

	if p.alive() {
		t.Error("the engine is still running after Shutdown")
	}
	sharedMu.Lock()
	still := sharedWhisper
	sharedMu.Unlock()
	if still != nil {
		t.Error("Shutdown left the dead process registered for reuse")
	}
}
