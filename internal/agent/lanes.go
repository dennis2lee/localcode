package agent

import (
	"context"

	"localcode/internal/config"
)

// Where contention actually lives.
//
// TaskManager has one semaphore, sized by max_concurrent_tasks, shared by
// every session on the daemon. That is the wrong shape for the machine it
// runs on, in both directions at once. One local model on one GPU serves
// one request at a time whatever the daemon-wide number says, so background
// tasks aimed at it are a queue however many slots they were admitted
// through; and a hosted provider on the same daemon, which would happily
// take ten, is held to the same small number because the local endpoint
// needed it to be small.
//
// So: a second, inner bound keyed by the provider config key. It is taken
// *before* the global one, so a task waiting on a busy local endpoint is
// not sitting on a global slot that a hosted task could have used. That
// ordering is the whole point, and it is what the test pins.
//
// What this deliberately is not: a scheduler. An earlier design grouped
// consecutive grants by profile to keep a local server's prefix cache warm,
// and it was built on a claim that does not hold here. A profile decides
// the model and the sampling parameters; the system prompt is assembled per
// *agent* (see AgentConfig.Prompt and toolsForTurn), so two tasks on one
// profile routinely carry different prefixes anyway. Grouping by the wrong
// key buys nothing and costs a queue, a starvation bound and three tests
// whose correctness is not obvious. A counting semaphore is obvious.
type lanes map[string]chan struct{}

// newLanes builds one lane per provider that asked for a limit. A provider
// that says nothing gets no lane and is bounded only by the daemon-wide
// number, which is exactly what every provider was before this existed.
func newLanes(cfg *config.Config) lanes {
	if cfg == nil {
		return nil
	}
	var out lanes
	for name, p := range cfg.Providers {
		if p.MaxConcurrentTasks <= 0 {
			continue
		}
		if out == nil {
			out = lanes{}
		}
		out[name] = make(chan struct{}, p.MaxConcurrentTasks)
	}
	return out
}

// take blocks until this provider's lane has room, and returns the release.
// A provider with no lane, or a manager with none, returns immediately.
//
// Reports false when ctx ended first, so Esc during the wait ends the task
// rather than queueing it behind work nobody is waiting for any more.
func (l lanes) take(ctx context.Context, provider string) (release func(), ok bool) {
	lane, has := l[provider]
	if !has {
		return func() {}, true
	}
	select {
	case lane <- struct{}{}:
		return func() { <-lane }, true
	case <-ctx.Done():
		return func() {}, false
	}
}

// providerFor is the provider config key a task running as agentName will
// use, so the lane can be chosen before the turn starts.
//
// Resolved the same way the turn itself will resolve it, through the same
// per-turn Smart Agent pin: a specialist admitted while the switch was on
// runs on the profile that roster gave it, and queueing it on another
// provider's lane would bound the wrong endpoint. An agent whose profile
// does not resolve gets no lane rather than an error, because refusing to
// launch a task over a lane lookup would turn a queueing detail into a
// failure.
func (l *Loop) providerFor(ctx context.Context, agentName string) string {
	if l == nil || l.Config == nil {
		return ""
	}
	_, profile, err := l.profileFor(ctx, agentName)
	if err != nil {
		return ""
	}
	return profile.Provider
}
