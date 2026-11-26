package config

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Rules are resolved on any goroutine while the runtime setters change
// them: a tool call deciding whether to ask, and the browser's request
// guard for every request a page makes. Every write a setter makes has to
// be under the lock the resolvers read under. The race lane is what sees a
// write that is not; elsewhere this only checks that readers and writers
// finish.
func TestRulesChangeWhileTheyAreResolved(t *testing.T) {
	c := &Config{}
	deny := PermissionRule{Match: "*/later*", Decision: DecisionDeny}
	agents := []map[string]AgentConfig{
		{"web": {Permission: map[string]ToolPermission{"browser": {Rules: []PermissionRule{{Match: "*/later*", Decision: DecisionAllow}}}}}},
		{"other": {Permission: map[string]ToolPermission{"browser": {Rules: []PermissionRule{deny}}}}},
	}
	var stop atomic.Bool
	var reads atomic.Int64
	var wg sync.WaitGroup
	asWeb := WithAgent(context.Background(), "web")
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				c.DeniedAnywhere("browser", "https://site.example/later")
				c.ResolvePermissionFor(asWeb, "browser", "https://site.example/later", false)
				reads.Add(1)
			}
		}()
	}
	// Writing goes on until the readers have resolved a good many times,
	// not for a set count: a fixed count of writes can finish before a
	// reader has been scheduled at all, and then nothing ran beside them.
	const enough = 1000
	deadline := time.Now().Add(10 * time.Second)
	for i := 0; i < 500 || reads.Load() < enough; i++ {
		c.AddPermissionRuleRuntime("browser", deny)
		c.SetAgentsRuntime(agents[i%2])
		c.SetSkipPermissionsRuntime(i%2 == 0)
		c.RemovePermissionRuleRuntime("browser", deny)
		if time.Now().After(deadline) {
			break
		}
	}
	stop.Store(true)
	wg.Wait()
	if n := reads.Load(); n < enough {
		t.Fatalf("only %d resolves ran beside the writes in 10s", n)
	}
}
