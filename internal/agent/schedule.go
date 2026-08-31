package agent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
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
	// ScheduleMissed is the honest outcome of a moment that passed with
	// the work not done: localcode was not running, or the conversation
	// had been archived by the time it came round. Not fired late: the
	// request was for a time, and a report at breakfast about work nobody
	// did is worth more than the same work done at breakfast without
	// being asked. Retrieving the conversation does not re-run it.
	ScheduleMissed = "missed"
)

// Scheduled is one booked prompt, as a client sees it.
type Scheduled struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	Prompt    string    `json:"prompt"`
	// Name is what the panel calls this task, when somebody has given it
	// one. Empty otherwise, and the row falls back to the prompt.
	//
	// It exists because a booked prompt is a paragraph and a row is one
	// truncated line: "run the tests and report the fail…" beside "check
	// the nightly build and report the fail…" is two rows nobody can tell
	// apart at the moment they need to. Cosmetic, like a session's title —
	// nothing resolves by it.
	Name   string `json:"name,omitempty"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
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

	mu sync.Mutex
	// counters is the next number to hand out per conversation, and
	// entries and timers are keyed by conversation and id together.
	//
	// An id is short and per conversation — "s1", "s2" — because the one
	// place it has to be typed is a TUI, and
	// "sched-1788019200000000000-1" is not something anybody retypes to
	// cancel a task. It is only ever meaningful inside the conversation
	// it belongs to, which is also the only place it is ever shown.
	counters map[string]int
	entries  map[string]*Scheduled
	timers   map[string]*time.Timer
}

// key namespaces an id by the conversation it belongs to, since "s1"
// means a different task in every conversation that has one.
func key(sessionID, id string) string { return sessionID + "\x00" + id }

func NewScheduler(rootCtx context.Context, loop *Loop) *Scheduler {
	s := &Scheduler{
		loop:     loop,
		rootCtx:  rootCtx,
		counters: map[string]int{},
		entries:  map[string]*Scheduled{},
		timers:   map[string]*time.Timer{},
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
	parent, err := s.loop.Store.Get(sessionID)
	if err != nil {
		return Scheduled{}, fmt.Errorf("schedule: %w", err)
	}
	// Beside the daemon's own refusal rather than instead of it, so the
	// endpoint and the scheduler cannot disagree about what a booking
	// into an archived conversation does.
	if parent.ArchivedAt != nil {
		return Scheduled{}, fmt.Errorf("schedule: this conversation is archived; retrieve it first")
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
	// Never reused, even after a deletion: a fresh s1 where a cancelled
	// s1 used to be is the one way a short id can mislead.
	s.counters[sessionID]++
	id := fmt.Sprintf("s%d", s.counters[sessionID])
	entry := &Scheduled{
		ID: id, SessionID: sessionID, At: at, Prompt: prompt,
		Agent: agentName, Status: SchedulePending,
	}
	s.entries[key(sessionID, id)] = entry
	s.mu.Unlock()

	// Recorded on the conversation's own log, which is what makes the
	// row in the panel survive a reload — the same reason a background
	// task's row is built from task.spawned rather than from memory.
	if _, err := s.loop.Store.Append(sessionID, events.TypeScheduleCreated, map[string]any{
		"id": id, "at": at.Format(time.RFC3339), "prompt": prompt, "agent": agentName,
	}); err != nil {
		s.forget(sessionID, id)
		return Scheduled{}, fmt.Errorf("schedule: %w", err)
	}
	s.arm(sessionID, id, at)
	return *entry, nil
}

// arm sets the timer. Separate from Add because a restart re-arms
// everything it finds still in the future, and that path has no booking
// to do.
func (s *Scheduler) arm(sessionID, id string, at time.Time) {
	d := time.Until(at)
	if d < 0 {
		d = 0
	}
	k := key(sessionID, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[k]; !ok {
		return
	}
	s.timers[k] = time.AfterFunc(d, func() { s.fire(sessionID, id) })
}

// Cancel removes a booked task. Reports whether there was one.
func (s *Scheduler) Cancel(sessionID, id string) bool {
	k := key(sessionID, id)
	s.mu.Lock()
	if _, ok := s.entries[k]; !ok {
		s.mu.Unlock()
		return false
	}
	if t := s.timers[k]; t != nil {
		t.Stop()
	}
	delete(s.timers, k)
	delete(s.entries, k)
	s.mu.Unlock()

	// Recorded where the row comes from, so it does not come back on the
	// next reload — the lesson the task rows already taught.
	s.loop.Store.Append(sessionID, events.TypeScheduleRemoved, map[string]any{"id": id})
	return true
}

func (s *Scheduler) forget(sessionID, id string) {
	k := key(sessionID, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[k]; t != nil {
		t.Stop()
	}
	delete(s.timers, k)
	delete(s.entries, k)
}

// MarkSeen records that somebody has read the result: the third LED
// state, and the one that makes the panel a list of what still wants
// attention rather than a list of everything that ever ran.
func (s *Scheduler) MarkSeen(sessionID, id string) bool {
	s.mu.Lock()
	entry, ok := s.entries[key(sessionID, id)]
	if !ok || entry.Seen {
		s.mu.Unlock()
		return false
	}
	entry.Seen = true
	s.mu.Unlock()
	s.loop.Store.Append(sessionID, events.TypeScheduleSeen, map[string]any{"id": id})
	return true
}

// Rename gives a booked task a name, or clears it. Reports whether there
// was one to rename.
//
// Recorded on the conversation's log like every other change to a row,
// which is what makes the name survive a reload and reach a second window
// without either having to ask again.
func (s *Scheduler) Rename(sessionID, id, name string) (Scheduled, bool) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) > maxScheduleName {
		name = strings.TrimSpace(string([]rune(name)[:maxScheduleName]))
	}
	s.mu.Lock()
	entry, ok := s.entries[key(sessionID, id)]
	if !ok {
		s.mu.Unlock()
		return Scheduled{}, false
	}
	entry.Name = name
	out := *entry
	s.mu.Unlock()

	s.loop.Store.Append(sessionID, events.TypeScheduleRenamed, map[string]any{"id": id, "name": name})
	return out, true
}

// maxScheduleName bounds a name at what a panel row can show. A row is
// one line; a name longer than this is a prompt in the wrong field, and
// truncating says so more usefully than a scrollbar would.
const maxScheduleName = 80

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
func (s *Scheduler) Get(sessionID, id string) (Scheduled, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key(sessionID, id)]
	if !ok {
		return Scheduled{}, false
	}
	return *e, true
}

// fire runs the booked prompt.
func (s *Scheduler) fire(sessionID, id string) {
	k := key(sessionID, id)
	s.mu.Lock()
	entry, ok := s.entries[k]
	if !ok || entry.Status != SchedulePending {
		s.mu.Unlock()
		return
	}
	entry.Status = ScheduleRunning
	agentName, prompt := entry.Agent, entry.Prompt
	delete(s.timers, k)
	s.mu.Unlock()

	// The parent may have been deleted, or put away, between booking and
	// firing.
	parent, err := s.loop.Store.Get(sessionID)
	if err != nil {
		s.finish(sessionID, id, "", ScheduleFailed, "the conversation this was scheduled in no longer exists")
		return
	}
	// Missed rather than failed: the moment passed without the work being
	// done, which is what missed means, and nothing went wrong. Retrieving
	// the conversation does not re-run it, for the reason a missed
	// schedule is never fired late.
	if parent.ArchivedAt != nil {
		s.finish(sessionID, id, "", ScheduleMissed, "the conversation this was scheduled in is archived")
		return
	}

	// The conversation's id is in it, because a short id is only unique
	// inside one conversation and a session id has to be unique in the
	// store.
	runID := sessionID + "-" + id + "-run"
	// Under the parent's workspace and, through the parent, its
	// permission switches: the work was booked in a project and by
	// somebody who had already decided what may happen without asking.
	if _, err := s.loop.Store.CreateSessionIn(runID, sessionID, agentName, s.loop.SessionDir(sessionID), false); err != nil {
		s.finish(sessionID, id, "", ScheduleFailed, fmt.Sprintf("could not create the session to run in: %v", err))
		return
	}
	s.status(sessionID, id, ScheduleRunning, runID, "")

	// Unattended: nobody is watching this turn, so a permission request
	// it raises must not wait forever. See WithUnattended.
	ctx := WithUnattended(s.rootCtx)
	err = s.loop.SendMessage(ctx, runID, agentName, prompt)
	if err != nil {
		s.finish(sessionID, id, runID, ScheduleFailed, err.Error())
		return
	}
	s.finish(sessionID, id, runID, ScheduleDone, "")
}

func (s *Scheduler) finish(sessionID, id, runID, status, errText string) {
	s.status(sessionID, id, status, runID, errText)
}

// status updates one entry and tells the conversation it belongs to.
func (s *Scheduler) status(sessionID, id, status, runID, errText string) {
	s.mu.Lock()
	entry, ok := s.entries[key(sessionID, id)]
	if !ok {
		s.mu.Unlock()
		return
	}
	entry.Status = status
	if runID != "" {
		entry.RunSession = runID
	}
	entry.Error = errText
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
			case events.TypeScheduleRenamed:
				if e := byID[id]; e != nil {
					e.Name = str(ev.Data["name"])
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
			s.entries[key(sessionID, id)] = e
			// The counter picks up where the log left off, so a restart
			// cannot hand out an id a cancelled task already used.
			if n := idNumber(id); n > s.counters[sessionID] {
				s.counters[sessionID] = n
			}
			s.mu.Unlock()
			if e.Status != SchedulePending {
				continue
			}
			if e.At.After(now) {
				s.arm(sessionID, id, e.At)
			} else {
				s.status(sessionID, id, ScheduleMissed, "", "localcode was not running at "+e.At.Format("2006-01-02 15:04"))
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
	for k, e := range s.entries {
		if e.SessionID != sessionID {
			continue
		}
		if t := s.timers[k]; t != nil {
			t.Stop()
		}
		delete(s.timers, k)
		delete(s.entries, k)
	}
	delete(s.counters, sessionID)
}

// idNumber reads the number out of "s12", for restoring the counter. Zero
// for anything that is not one of these ids, including the long ones
// booked before they were short.
func idNumber(id string) int {
	rest, ok := strings.CutPrefix(id, "s")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Describe renders one entry as the line "/show-scheduled-task" prints.
func Describe(e Scheduled, now time.Time) string {
	line := fmt.Sprintf("%s  %s  %s", e.ID, e.Status, e.At.Format("2006-01-02 15:04"))
	if e.Status == SchedulePending {
		line += fmt.Sprintf(" (in %s)", roughUntil(e.At, now))
	}
	if e.Name != "" {
		line += "  " + e.Name
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

// RunningIn reports which of this conversation's booked prompts are
// actually mid-turn.
//
// Read for archiving, which refuses while one is going. A schedule that
// commits to running in the microseconds between this read and the archive
// completing its write finishes its turn and reports into a conversation
// that is by then archived: the log still accepts it, so nothing is lost,
// and that residual is written down in docs/USAGE.md rather than papered
// over. Closing it properly would mean suspending the timers, which is a
// second mutex protocol and a rollback question for one line of report.
func (s *Scheduler) RunningIn(sessionID string) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var running []string
	for _, entry := range s.entries {
		if entry.SessionID == sessionID && entry.Status == ScheduleRunning {
			running = append(running, entry.ID)
		}
	}
	sort.Strings(running)
	return running
}
