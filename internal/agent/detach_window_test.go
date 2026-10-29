package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localcode/internal/hooks"
)

// A child whose spawn began before a let go starts let go, however its
// registration falls against DetachChildOf's scan, its lets go and its
// count. The interleaving is steered through the manager's lock: the test
// holds it while DetachChildOf queues for its scan and then, behind it,
// child B queues for its registration (B's spawn began before the let go,
// in a delegate hook). Which of them takes the lock next is the
// scheduler's, so the test repeats the attempt.
func TestAChildRegisteringAsLetGoHappensStartsLetGo(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := slowServer(t, release)
	loop, tasks := newBackgroundLoop(t, srv.URL)
	dir := t.TempDir()
	// Set once: children let go in earlier attempts are still running and
	// read the config. The attempt is in the prompt, which the hook gets.
	loop.Config.Hooks = hooks.Config{hooks.EventDelegate: {{
		Command: "n=$(grep -o 'attempt[0-9]*' | head -1); touch '" + dir + "'/started-$n; " +
			"while [ ! -f '" + dir + "'/go-$n ]; do sleep 0.01; done; exit 0",
	}}}

	const attempts = 12
	attached := 0
	for i := 0; i < attempts && attached == 0; i++ {
		parent, childB := fmt.Sprintf("parent%d", i), fmt.Sprintf("childB%d", i)
		if _, err := loop.Store.CreateSession(parent, "", "explore", true); err != nil {
			t.Fatal(err)
		}
		if _, err := loop.Store.CreateSessionIn(childB, parent, "explore", "", false); err != nil {
			t.Fatal(err)
		}
		started, goOn := filepath.Join(dir, fmt.Sprintf("started-attempt%d", i)), filepath.Join(dir, fmt.Sprintf("go-attempt%d", i))

		// A: registered and blocking the parent, as a debate's reviewers do.
		var as []string
		for j := 0; j < 3; j++ {
			entered := make(chan string)
			go tasks.SpawnRun(context.Background(), parent, "goal", nil, func(ctx context.Context, taskID string) (string, error) {
				entered <- taskID
				<-ctx.Done()
				return "", ctx.Err()
			})
			as = append(as, <-entered)
		}

		// B: its spawn begins now, and waits in the delegate hook.
		bDone := make(chan error, 1)
		go func() {
			_, _, err := tasks.SpawnSyncInto(context.Background(), parent, childB, "explore", fmt.Sprintf("take your time, attempt%d", i))
			bDone <- err
		}()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(started); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("B's delegate hook never started")
			}
			time.Sleep(5 * time.Millisecond)
		}

		tasks.mu.Lock()
		letGo := make(chan []string, 1)
		go func() {
			ids, _ := tasks.DetachChildOf(parent)
			letGo <- ids
		}()
		time.Sleep(100 * time.Millisecond) // DetachChildOf is queued for its scan
		if err := os.WriteFile(goOn, nil, 0o600); err != nil {
			tasks.mu.Unlock()
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond) // B is out of its hook and queued for its registration
		tasks.mu.Unlock()
		ids := <-letGo

		// Wait for B to be registered, then ask whether it started let go.
		for {
			tasks.mu.Lock()
			_, registered := tasks.detach[childB]
			tasks.mu.Unlock()
			if registered || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if len(ids) > 0 && !tasks.LetGo(childB) {
			attached++
			t.Errorf("attempt %d: let go of %v, and B, whose spawn began before it, started attached; the turn waits on B", i+1, ids)
		}
		tasks.Detach(childB)
		<-bDone
		for _, id := range as {
			tasks.Cancel(id)
		}
	}
}
