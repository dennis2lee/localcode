package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
	"localcode/internal/when"
)

// A repeating booking, which was refused for a long time with the reason
// attached: "a repeat needs a stop condition and a policy for what
// happens when it fails". These are the two halves, and the housekeeping
// that a run-per-occurrence turns out to need.
//
// The timers are not what is under test here — the one-off tests already
// cover arming — so these drive fire() directly. What each run does to
// the booking is the whole subject.

// repeatProvider answers every turn the same way, and fails on demand.
type repeatProvider struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (p *repeatProvider) setFail(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = v
}

func (p *repeatProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *repeatProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	fail := p.fail
	p.mu.Unlock()
	if fail {
		return nil, errors.New("the credential expired")
	}
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: "did the thing"}
	ch <- provider.StreamEvent{Type: provider.EventMessageStop, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

// loopWithProvider is scheduleLoop with a provider that can serve more
// than one turn, which is the whole point of a repeat.
func loopWithProvider(t *testing.T, p provider.Provider) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	return New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": p}, cfg)
}

func repeatingSetup(t *testing.T, opts RepeatOptions) (*Scheduler, *repeatProvider, string, string) {
	t.Helper()
	p := &repeatProvider{}
	loop := loopWithProvider(t, p)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)
	// Far enough out that the timer never fires on its own; every run in
	// these tests is one this test asked for.
	entry, err := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(time.Hour), opts)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	return sched, p, sid, entry.ID
}

func hourly(keep int) RepeatOptions {
	return RepeatOptions{Rule: when.Repeat{Every: 1, Unit: when.UnitHour}, Keep: keep}
}

// The defect that made repeats impossible rather than merely absent: the
// run session's id was fixed per booking, and creating a session that
// already exists is an error, so the second occurrence failed outright.
func TestEachRunGetsItsOwnSession(t *testing.T) {
	sched, _, sid, id := repeatingSetup(t, hourly(-1))

	var seen []string
	for i := 0; i < 3; i++ {
		sched.fire(sid, id)
		e, _ := sched.Get(sid, id)
		if e.Status != SchedulePending {
			t.Fatalf("after run %d the booking is %q (%s), want it armed for the next one", i+1, e.Status, e.Error)
		}
		seen = append(seen, e.RunSession)
	}
	if len(seen) != 3 || seen[0] == seen[1] || seen[1] == seen[2] {
		t.Fatalf("run sessions = %v, want three different ones", seen)
	}
	for _, runID := range seen {
		if _, err := sched.loop.Store.Get(runID); err != nil {
			t.Errorf("run session %s is not there: %v", runID, err)
		}
	}
	if e, _ := sched.Get(sid, id); e.Runs != 3 {
		t.Errorf("runs = %d, want 3", e.Runs)
	}
}

// Every run session opens with a line saying which booking made it and
// when. Opened on its own it is otherwise a conversation that starts with
// an instruction nobody in it typed.
func TestARunSessionSaysWhereItCameFrom(t *testing.T) {
	sched, _, sid, id := repeatingSetup(t, hourly(-1))
	sched.Rename(sid, id, "nightly check")
	sched.fire(sid, id)

	e, _ := sched.Get(sid, id)
	evs, err := sched.loop.Store.Events(e.RunSession, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("the run session has no events at all")
	}
	head := evs[0]
	if head.Type != events.TypeSessionScheduled {
		t.Fatalf("the run session opens with %q, want the line saying where it came from", head.Type)
	}
	if got := head.Data["schedule"]; got != id {
		t.Errorf("head names schedule %v, want %q", got, id)
	}
	if got := head.Data["name"]; got != "nightly check" {
		t.Errorf("head names %v, want the booking's name", got)
	}
	if got := head.Data["from"]; got != sid {
		t.Errorf("head names conversation %v, want %q", got, sid)
	}
	if head.Data["at"] == nil || head.Data["at"] == "" {
		t.Error("head does not say when it was made")
	}
}

// Stop condition one: a count.
func TestASeriesStopsAfterTheNumberOfRunsItWasGiven(t *testing.T) {
	sched, p, sid, id := repeatingSetup(t, RepeatOptions{
		Rule: when.Repeat{Every: 1, Unit: when.UnitHour}, StopAfter: 2, Keep: -1,
	})

	sched.fire(sid, id)
	if e, _ := sched.Get(sid, id); e.Status != SchedulePending {
		t.Fatalf("after one of two runs the booking is %q, want it armed for the second", e.Status)
	}
	sched.fire(sid, id)
	e, _ := sched.Get(sid, id)
	if e.Status != ScheduleDone {
		t.Errorf("after the last run the booking is %q, want it finished", e.Status)
	}
	// And it really is over: firing again is a no-op, since the entry is
	// no longer pending.
	sched.fire(sid, id)
	if p.count() != 2 {
		t.Errorf("the model was called %d times, want exactly 2", p.count())
	}
}

// Stop condition two: a date.
func TestASeriesStopsAtTheDateItWasGiven(t *testing.T) {
	// A stop an hour and a half out against an hourly rule: the first run
	// happens, the second would fall outside it.
	sched, p, sid, id := repeatingSetup(t, RepeatOptions{
		Rule: when.Repeat{Every: 1, Unit: when.UnitHour}, Keep: -1,
		StopAt: time.Now().Add(90 * time.Minute),
	})

	sched.fire(sid, id)
	e, _ := sched.Get(sid, id)
	if e.Status != ScheduleDone {
		t.Errorf("booking is %q, want it finished — the next run is past the stop date", e.Status)
	}
	if p.count() != 1 {
		t.Errorf("the model was called %d times, want 1", p.count())
	}
}

// Stop condition three: none at all. It runs until somebody deletes it,
// which is only safe because of the suspension below.
func TestASeriesWithNoStopKeepsGoing(t *testing.T) {
	sched, p, sid, id := repeatingSetup(t, hourly(-1))
	for i := 0; i < 5; i++ {
		sched.fire(sid, id)
	}
	e, _ := sched.Get(sid, id)
	if e.Status != SchedulePending {
		t.Errorf("booking is %q after five runs, want it still armed", e.Status)
	}
	if p.count() != 5 {
		t.Errorf("the model was called %d times, want 5", p.count())
	}
}

// The failure policy, which is the half the stop conditions do not
// answer. An expired credential fails identically every time; without
// this, one wrong booking is a machine that wakes up forever to do
// nothing.
func TestASeriesThatKeepsFailingStopsItself(t *testing.T) {
	sched, p, sid, id := repeatingSetup(t, hourly(-1))
	p.setFail(true)

	for i := 0; i < maxConsecutiveFailures; i++ {
		sched.fire(sid, id)
	}
	e, _ := sched.Get(sid, id)
	if e.Status != ScheduleSuspended {
		t.Fatalf("booking is %q after %d failures, want it suspended", e.Status, maxConsecutiveFailures)
	}
	if !strings.Contains(e.Error, "in a row failed") {
		t.Errorf("error = %q, want it to say why it stopped", e.Error)
	}
	// The last error is named, so the fix is visible without opening
	// anything.
	if !strings.Contains(e.Error, "credential expired") {
		t.Errorf("error = %q, want it to carry the last failure", e.Error)
	}
	before := p.count()
	sched.fire(sid, id)
	if p.count() != before {
		t.Error("a suspended booking ran again")
	}
}

// A run that works clears the count, so a series that survives a blip is
// not punished for it two hours later.
func TestASuccessForgivesTheFailuresBeforeIt(t *testing.T) {
	sched, p, sid, id := repeatingSetup(t, hourly(-1))

	p.setFail(true)
	sched.fire(sid, id)
	sched.fire(sid, id)
	if e, _ := sched.Get(sid, id); e.Fails != 2 {
		t.Fatalf("fails = %d, want 2", e.Fails)
	}

	p.setFail(false)
	sched.fire(sid, id)
	if e, _ := sched.Get(sid, id); e.Fails != 0 {
		t.Errorf("fails = %d after a run that worked, want 0", e.Fails)
	}

	// And the two before it do not add up with two more.
	p.setFail(true)
	sched.fire(sid, id)
	sched.fire(sid, id)
	if e, _ := sched.Get(sid, id); e.Status == ScheduleSuspended {
		t.Error("two failures after a success suspended the booking; the count did not reset")
	}
}

// Retention. A repeat makes a session per run and nothing was ever going
// to prune them.
func TestTheRetentionCapKeepsWhatItSays(t *testing.T) {
	for _, tt := range []struct {
		keep, runs, want int
		because          string
	}{
		{-1, 4, 4, "-1 keeps every one"},
		{0, 3, 0, "0 keeps none, including the run that has just finished"},
		{2, 5, 2, "n keeps the most recent n"},
	} {
		sched, _, sid, id := repeatingSetup(t, hourly(tt.keep))
		for i := 0; i < tt.runs; i++ {
			sched.fire(sid, id)
		}
		e, _ := sched.Get(sid, id)
		if len(e.History) != tt.want {
			t.Errorf("keep=%d after %d runs kept %d transcripts, want %d (%s)",
				tt.keep, tt.runs, len(e.History), tt.want, tt.because)
		}
		// What it says it kept is what is really on disk, and what it
		// dropped is really gone.
		for _, runID := range e.History {
			if _, err := sched.loop.Store.Get(runID); err != nil {
				t.Errorf("keep=%d: %s is in the history but not on disk", tt.keep, runID)
			}
		}
		for i := 1; i <= tt.runs-tt.want; i++ {
			gone := runSessionID(sid, id, i)
			if _, err := sched.loop.Store.Get(gone); err == nil {
				t.Errorf("keep=%d: %s should have been pruned", tt.keep, gone)
			}
		}
	}
}

// Keeping nothing must not leave the row pointing at a transcript that
// has been deleted: a link to nothing is worse than no link.
func TestKeepingNothingLeavesNoDanglingLink(t *testing.T) {
	sched, _, sid, id := repeatingSetup(t, hourly(0))
	sched.fire(sid, id)

	e, _ := sched.Get(sid, id)
	if e.RunSession != "" {
		t.Errorf("run session = %q, want nothing to open", e.RunSession)
	}
	// The record of whether it worked is not in the transcript, so it
	// survives keeping none of them.
	if e.Runs != 1 {
		t.Errorf("runs = %d, want the run still counted", e.Runs)
	}
}

// A booking that runs once is untouched by any of this, and that is most
// of them.
func TestAOneOffIsNotAdvancedOrPruned(t *testing.T) {
	sched, p, sid, id := repeatingSetup(t, RepeatOptions{})
	sched.fire(sid, id)

	e, _ := sched.Get(sid, id)
	if e.Status != ScheduleDone {
		t.Fatalf("status = %q, want done", e.Status)
	}
	if e.RunSession == "" {
		t.Error("a one-off lost its transcript")
	}
	if _, err := sched.loop.Store.Get(e.RunSession); err != nil {
		t.Errorf("a one-off's run session was pruned: %v", err)
	}
	sched.fire(sid, id)
	if p.count() != 1 {
		t.Errorf("a one-off ran %d times", p.count())
	}
}

// runSessionID is the id fire builds, so the retention test can name a
// run it expects to be gone.
func runSessionID(sessionID, id string, run int) string {
	return sessionID + "-" + id + "-run" + itoa(run)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
