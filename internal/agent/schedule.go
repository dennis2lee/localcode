package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// Work booked for later.
//
// A scheduled task is a prompt, a moment, and a conversation it belongs
// to. When the moment comes it runs exactly the way a background task
// runs: in a child session, under the parent's workspace and the parent's
// permission switches, with its own durable log. That is not a
// coincidence and it is most of why this is small — "run a prompt
// unattended in the right directory" was already built.
//
// What is deliberately not here:
//
//   - The model. It is the worker, never the clock. The time is parsed by
//     internal/when, which is a testable function that says no clearly; a
//     local model asked for a timestamp gets the year wrong occasionally,
//     and a scheduled task is exactly where an occasional wrong answer is
//     invisible until the day it matters.
//   - Anything that outlives the process. A schedule fires while
//     localcode is running and not otherwise. There is no service, no
//     launchd job, no waking the machine, and pretending otherwise would
//     be a promise the program cannot keep. A schedule whose moment
//     passed while the daemon was down is reported as missed, in those
//     words, rather than fired late as though nothing happened.
//   - Repeats. One prompt, one moment. A repeating job needs a failure
//     policy and a stop condition of its own, and shipping it without
//     them is how an expired credential becomes five hundred identical
//     failed sessions.

// Schedule statuses. Five, and each is a different thing to look at.
const (
	// SchedulePending is booked and waiting. The panel blinks for this.
	SchedulePending = "pending"
	// ScheduleRunning is the turn actually going.
	ScheduleRunning = "running"
	// ScheduleDone finished and has an answer to read.
	ScheduleDone = "done"
	// ScheduleFailed ran and did not get there.
	ScheduleFailed = "failed"
	// ScheduleMissed is the honest outcome of a moment that passed while
	// localcode was not running. Not fired late: the request was for a
	// time, and a report at breakfast about work nobody did is worth more
	// than the same work done at breakfast without being asked.
	ScheduleMissed = "missed"
)

// Scheduled is one booked prompt, as a client sees it.
type Scheduled struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	Prompt    string    `json:"prompt"`
	Agent     string    `json:"agent"`
	Status    string    `json:"status"`
	// RunSession is the child session the work ran in, and therefore
	// where its output is. Empty until it fires.
	RunSession string `json:"run_session,omitempty"`
	// Seen is whether somebody has looked at the result. It is the third
	// LED state: blinking while it waits, solid once there is something
	// to read, grey once it has been read.
	Seen  bool   `json:"seen"`
	Error string `json:"error,omitempty"`
}

// Scheduler holds the timers and the books.
type Scheduler struct {
	loop    *Loop
	rootCtx context.Context

	mu      sync.Mutex
	counter int
	entries map[string]*Scheduled
	timers  map[string]*time.Timer
}

func NewScheduler(rootCtx context.Context, loop *Loop) *Scheduler {
	s := &Scheduler{
		loop:    loop,
		rootCtx: rootCtx,
		entries: map[string]*Scheduled{},
		timers:  map[string]*time.Timer{},
	}
	loop.Schedules = s
	return s
}

// maxPendingPerSession bounds how many booked prompts one conversation
// may have waiting.
//
// A ceiling rather than a queue, and low on purpose. Each one is a turn
// that will run unattended, and the failure this guards is not a burst,
// it is a loop that books work nobody asked for. Sixteen is more than any
// real use has, so hitting it is a signal.
const maxPendingPerSession = 16

// Add books a prompt. at must be in the future; the caller has already
// parsed and echoed it (see internal/when).
func (s *Scheduler) Add(sessionID, agentName, prompt string, at time.Time) (Scheduled, error) {
	if _, err := s.loop.Store.Get(sessionID); err != nil {
		return Scheduled{}, fmt.Errorf("schedule: %w", err)
	}
	s.mu.Lock()
	pending := 0
	for _, e := range s.entries {
		if e.SessionID == sessionID && e.Status == SchedulePending {
			pending++
		}
	}
	if pending >= maxPendingPerSession {
		s.mu.Unlock()
		return Scheduled{}, fmt.Errorf("this conversation already has %d scheduled tasks waiting; run or delete some before booking more", pending)
	}
	s.counter++
	id := fmt.Sprintf("sched-%d-%d", at.UnixNano(), s.counter)
	entry := &Scheduled{
		ID: id, SessionID: sessionID, At: at, Prompt: prompt,
		Agent: agentName, Status: SchedulePending,
	}
	s.entries[id] = entry
	s.mu.Unlock()

	// Recorded on the conversation's own log, which is what makes the
	// row in the panel survive a reload — the same reason a background
	// task's row is built from task.spawned rather than from memory.
	if _, err := s.loop.Store.Append(sessionID, events.TypeScheduleCreated, map[string]any{
		"id": id, "at": at.Format(time.RFC3339), "prompt": prompt, "agent": agentName,
	}); err != nil {
		s.forget(id)
		return Scheduled{}, fmt.Errorf("schedule: %w", err)
	}
	s.arm(id, at)
	return *entry, nil
}

// arm sets the timer. Separate from Add because a restart re-arms
// everything it finds still in the future, and that path has no booking
// to do.
func (s *Scheduler) arm(id string, at time.Time) {
	d := time.Until(at)
	if d < 0 {
		d = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return
	}
	s.timers[id] = time.AfterFunc(d, func() { s.fire(id) })
}

// Cancel removes a booked task. Reports whether there was one.
func (s *Scheduler) Cancel(id string) bool {
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	sessionID := entry.SessionID
	if t := s.timers[id]; t != nil {
		t.Stop()
	}
	delete(s.timers, id)
	delete(s.entries, id)
	s.mu.Unlock()

	// Recorded where the row comes from, so it does not come back on the
	// next reload — the lesson the task rows already taught.
	s.loop.Store.Append(sessionID, events.TypeScheduleRemoved, map[string]any{"id": id})
	return true
}

func (s *Scheduler) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[id]; t != nil {
		t.Stop()
	}
	delete(s.timers, id)
	delete(s.entries, id)
}

// MarkSeen records that somebody has read the result: the third LED
// state, and the one that makes the panel a list of what still wants
// attention rather than a list of everything that ever ran.
func (s *Scheduler) MarkSeen(id string) bool {
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok || entry.Seen {
		s.mu.Unlock()
		return false
	}
	entry.Seen = true
	sessionID := entry.SessionID
	s.mu.Unlock()
	s.loop.Store.Append(sessionID, events.TypeScheduleSeen, map[string]any{"id": id})
	return true
}

// List returns one conversation's booked tasks, soonest first, with the
// ones still waiting ahead of the ones that have run.
func (s *Scheduler) List(sessionID string) []Scheduled {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Scheduled
	for _, e := range s.entries {
		if sessionID == "" || e.SessionID == sessionID {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := out[i].Status == SchedulePending, out[j].Status == SchedulePending
		if pi != pj {
			return pi
		}
		return out[i].At.Before(out[j].At)
	})
	return out
}

// Get returns one entry by id.
func (s *Scheduler) Get(id string) (Scheduled, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return Scheduled{}, false
	}
	return *e, true
}

// fire runs the booked prompt.
func (s *Scheduler) fire(id string) {
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok || entry.Status != SchedulePending {
		s.mu.Unlock()
		return
	}
	entry.Status = ScheduleRunning
	sessionID, agentName, prompt := entry.SessionID, entry.Agent, entry.Prompt
	delete(s.timers, id)
	s.mu.Unlock()

	// The parent may have been deleted between booking and firing.
	if _, err := s.loop.Store.Get(sessionID); err != nil {
		s.finish(id, "", ScheduleFailed, "the conversation this was scheduled in no longer exists")
		return
	}

	runID := id + "-run"
	// Under the parent's workspace and, through the parent, its
	// permission switches: the work was booked in a project and by
	// somebody who had already decided what may happen without asking.
	if _, err := s.loop.Store.CreateSessionIn(runID, sessionID, agentName, s.loop.SessionDir(sessionID), false); err != nil {
		s.finish(id, "", ScheduleFailed, fmt.Sprintf("could not create the session to run in: %v", err))
		return
	}
	s.status(id, ScheduleRunning, runID, "")

	// Unattended: nobody is watching this turn, so a permission request
	// it raises must not wait forever. See WithUnattended.
	ctx := WithUnattended(s.rootCtx)
	err := s.loop.SendMessage(ctx, runID, agentName, prompt)
	if err != nil {
		s.finish(id, runID, ScheduleFailed, err.Error())
		return
	}
	s.finish(id, runID, ScheduleDone, "")
}

func (s *Scheduler) finish(id, runID, status, errText string) {
	s.status(id, status, runID, errText)
}

// status updates one entry and tells the conversation it belongs to.
func (s *Scheduler) status(id, status, runID, errText string) {
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	entry.Status = status
	if runID != "" {
		entry.RunSession = runID
	}
	entry.Error = errText
	sessionID := entry.SessionID
	data := map[string]any{"id": id, "status": status}
	if entry.RunSession != "" {
		data["run_session"] = entry.RunSession
	}
	if errText != "" {
		data["error"] = errText
	}
	s.mu.Unlock()
	s.loop.Store.Append(sessionID, events.TypeScheduleStatus, data)
}

// Restore rebuilds the books from what the session logs already say, and
// re-arms the ones whose moment has not yet come.
//
// This is not catch-up firing. A moment that passed while localcode was
// not running is marked missed and stays missed, because the request was
// for a time: running "summarize yesterday's commits" at four in the
// afternoon because the machine was asleep at nine would be doing
// something nobody asked for, at a moment they did not choose. The row
// says so instead.
func (s *Scheduler) Restore(sessions []string, now time.Time) {
	for _, sessionID := range sessions {
		evs, err := s.loop.Store.Events(sessionID, 0)
		if err != nil {
			continue
		}
		byID := map[string]*Scheduled{}
		var order []string
		for _, ev := range evs {
			id, _ := ev.Data["id"].(string)
			if id == "" {
				continue
			}
			switch ev.Type {
			case events.TypeScheduleCreated:
				at, terr := time.Parse(time.RFC3339, str(ev.Data["at"]))
				if terr != nil {
					continue
				}
				byID[id] = &Scheduled{
					ID: id, SessionID: sessionID, At: at,
					Prompt: str(ev.Data["prompt"]), Agent: str(ev.Data["agent"]),
					Status: SchedulePending,
				}
				order = append(order, id)
			case events.TypeScheduleStatus:
				if e := byID[id]; e != nil {
					e.Status = str(ev.Data["status"])
					if rs := str(ev.Data["run_session"]); rs != "" {
						e.RunSession = rs
					}
					e.Error = str(ev.Data["error"])
				}
			case events.TypeScheduleSeen:
				if e := byID[id]; e != nil {
					e.Seen = true
				}
			case events.TypeScheduleRemoved:
				delete(byID, id)
			}
		}
		for _, id := range order {
			e := byID[id]
			if e == nil {
				continue
			}
			// A turn that was running when the process ended did not
			// finish, and nothing will finish it.
			if e.Status == ScheduleRunning {
				e.Status = ScheduleFailed
				e.Error = "localcode stopped while this was running"
			}
			s.mu.Lock()
			s.entries[id] = e
			s.mu.Unlock()
			if e.Status != SchedulePending {
				continue
			}
			if e.At.After(now) {
				s.arm(id, e.At)
			} else {
				s.status(id, ScheduleMissed, "", "localcode was not running at "+e.At.Format("2006-01-02 15:04"))
			}
		}
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// ForgetSession drops a deleted conversation's schedules, timers and all,
// so a booked prompt cannot fire into a session that is gone.
func (s *Scheduler) ForgetSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.entries {
		if e.SessionID != sessionID {
			continue
		}
		if t := s.timers[id]; t != nil {
			t.Stop()
		}
		delete(s.timers, id)
		delete(s.entries, id)
	}
}

// Describe renders one entry as the line "/show-scheduled-task" prints.
func Describe(e Scheduled, now time.Time) string {
	line := fmt.Sprintf("%s  %s  %s", e.ID, e.Status, e.At.Format("2006-01-02 15:04"))
	if e.Status == SchedulePending {
		line += fmt.Sprintf(" (in %s)", roughUntil(e.At, now))
	}
	line += "\n    " + promptSummary(e.Prompt)
	if e.Error != "" {
		line += "\n    " + e.Error
	}
	return line
}

func roughUntil(at, now time.Time) string {
	d := at.Sub(now)
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1f hours", d.Hours())
	default:
		return fmt.Sprintf("%.1f days", d.Hours()/24)
	}
}

// promptSummary is the booked prompt on one line, since the panel and
// the list are both one row per task.
func promptSummary(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i] + " …"
		}
	}
	return s
}

// Compile-time check that the scheduler is what the loop's field expects.
var _ interface {
	ForgetSession(string)
} = (*Scheduler)(nil)

// unattendedKey marks a turn nobody is watching.
type unattendedKey struct{}

// WithUnattended marks ctx as a turn with no one at the keyboard.
//
// It exists for one reason, and it is the reason scheduled work is worth
// being careful about: a permission request has no timeout. A turn that
// asks at three in the morning blocks on a channel nothing will ever
// send to, holds its session busy, and is found in the morning having
// done nothing at all — the same bug a background task had when its
// question was written to a log nobody reads, one level up.
//
// So an unattended turn waits a bounded time for an answer and then
// stops, saying what it asked for. The wait is not zero because the
// common case under a schedule that only fires while localcode is
// running is that somebody *is* at the desk: the question is mirrored
// into the conversation that booked the work, and a few minutes is long
// enough to notice it and answer.
func WithUnattended(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedKey{}, true)
}

// Unattended reports whether this turn has nobody watching it.
func Unattended(ctx context.Context) bool {
	v, _ := ctx.Value(unattendedKey{}).(bool)
	return v
}

// unattendedPermissionWait is how long a scheduled turn waits for a
// permission answer before giving up and saying what it wanted.
//
// Five minutes rather than zero because a schedule only fires while
// localcode is running, which usually means somebody is at the desk: the
// question is mirrored into the conversation that booked the work, and
// five minutes is long enough to notice it. A var so the tests can move
// it; nothing else writes to it.
var unattendedPermissionWait = 5 * time.Minute

// unattendedWaitForTest shortens the wait and returns a restore. Test
// only, and here rather than in a _test file so the var above has exactly
// one writer.
func unattendedWaitForTest(d time.Duration) func() {
	prev := unattendedPermissionWait
	unattendedPermissionWait = d
	return func() { unattendedPermissionWait = prev }
}

// unattendedRefusal is what the model is told when nobody answered. It
// says the tool was not run and why, so the turn can end honestly rather
// than reporting a step it never took.
func unattendedRefusal(ask tools.Ask) string {
	return fmt.Sprintf(
		"not run: this turn is scheduled work with nobody watching, it needed permission for %q, and nobody answered within %s",
		ask.Description, unattendedPermissionWait)
}
