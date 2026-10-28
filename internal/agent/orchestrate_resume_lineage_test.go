package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A resume is refused while any run of its lineage is running: the
// original, a resume of it, or a resume of that, let go or not. Each of
// them reruns the same unfinished units.
func TestAResumeIsRefusedWhileAnyRunOfItsLineageIsLive(t *testing.T) {
	var slow atomic.Bool
	var started atomic.Int32
	release := make(chan struct{})
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.HasSuffix(p, "second") && slow.Load() {
			started.Add(1)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}
		return "ok", nil
	}}
	fail := &sync.Map{}
	fail.Store("second", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))

	// Run A: "second" fails.
	executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[
	  {"name":"a","kind":"step","agent":"oracle","prompt":"first"},
	  {"name":"b","kind":"step","agent":"oracle","prompt":"second"}]}`)
	runA := runIDs(loop, "main")[0]

	// Resume A as B: "second" fails again, so B stops too.
	resume(t, loop, "main", runA)
	ids := runIDs(loop, "main")
	if len(ids) != 2 {
		t.Fatalf("runs = %v, want A and its resume B", ids)
	}

	// Resume B (the last run, so no id needed) as C, and let C go while
	// "second" is in flight.
	fail.Delete("second")
	slow.Store(true)
	resumed := make(chan string, 1)
	go func() { resumed <- resume(t, loop, "main", "") }()
	deadline := time.Now().Add(3 * time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if started.Load() != 1 {
		t.Fatalf("the resume of B never started \"second\"")
	}
	if _, ok := loop.Tasks.DetachChildOf("main"); !ok {
		t.Fatal("nothing to let go of while the resume of B ran")
	}
	<-resumed

	// C is live and running A's unfinished unit. Resuming A must be refused.
	second := make(chan string, 1)
	go func() { second <- resume(t, loop, "main", runA) }()
	deadline = time.Now().Add(2 * time.Second)
	for started.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	twice := started.Load() >= 2
	close(release)
	out := <-second
	if twice || !strings.Contains(out, "being resumed already") {
		t.Errorf("resuming %s while a resume of its resume was live was not refused; "+
			"\"second\" was started %d times at once. Reply:\n%s", runA, started.Load(), out)
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(loop.Tasks.Running()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}
