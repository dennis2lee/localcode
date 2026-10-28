package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/tools"
)

// Letting go of the run a turn is waiting on works after an earlier run
// of the same conversation was let go and is still going: the earlier one
// is not what blocks the turn, and must not be what let go stops at.
func TestLettingGoOfTheSecondRunIsNotAnsweredByTheFirst(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int32
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "hold") {
			started.Add(1)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
		return "ok", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	defer func() {
		close(release)
		deadline := time.Now().Add(5 * time.Second)
		for len(loop.Tasks.Running()) > 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}()

	var wrong []string
	for i := 1; i <= 5; i++ {
		done := make(chan tools.Result, 1)
		go func() {
			done <- executeOrchestrate(context.Background(), loop,
				fmt.Sprintf(`{"goal":"g%d","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"hold %d"}]}`, i, i))
		}()
		deadline := time.Now().Add(3 * time.Second)
		for started.Load() < int32(i) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if started.Load() < int32(i) {
			t.Fatalf("run %d never started", i)
		}
		id, ok := loop.Tasks.DetachChildOf("main")
		if !ok {
			wrong = append(wrong, fmt.Sprintf("run %d: let go answered %q, false", i, id))
			// Keep pressing, as a person would, so the next round starts.
			for tries := 0; !ok && tries < 200; tries++ {
				_, ok = loop.Tasks.DetachChildOf("main")
			}
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("run %d still holds the turn", i)
		}
	}
	if len(wrong) > 0 {
		t.Errorf("letting go of the run the turn was waiting on did nothing:\n%s", strings.Join(wrong, "\n"))
	}
}
