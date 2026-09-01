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
	"localcode/internal/when"
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
// Repeats were on that list for a long time, with the reason attached: a
// repeating job needs a failure policy and a stop condition of its own,
// and shipping it without them is how an expired credential becomes five
// hundred identical failed sessions. Both exist now. The stop conditions
// are StopAt, StopAfter, and neither — a series that runs until somebody
// deletes it. The failure policy is maxConsecutiveFailures, and it is
// what makes the third of those safe to offer at all.

// Schedule statuses. Six, and each is a different thing to look at.
const (
	// SchedulePending is booked and waiting. The panel blinks for this.
	SchedulePending = "pending"
	// ScheduleRunning is the turn actually going.
	ScheduleRunning = "running"
	// ScheduleDone finished and has an answer to read.
	ScheduleDone = "done"
	// ScheduleFailed ran and did not get there.
	ScheduleFailed = "failed"
	// ScheduleSuspended is a repeat stopped because it kept failing. It
	// is not the series ending on its own terms — that is done or failed
	// — but localcode refusing to go on waking up for something that has
	// not worked the last maxConsecutiveFailures times. The row says the
	// count and the last error, so the fix is visible without opening
	// anything.
	ScheduleSuspended = "suspended"
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

	// Repeat is the step between runs; the zero value runs once, which is
	// most bookings. See internal/when.
	Repeat when.Repeat `json:"repeat,omitzero"`
	// StopAt ends the series on a date, and StopAfter ends it on a count.
	// Both zero is a series with no end of its own — it runs until it is
	// deleted, or until Suspend stops it. Either may be set; whichever
	// comes first wins.
	StopAt    time.Time `json:"stop_at,omitzero"`
	StopAfter int       `json:"stop_after,omitempty"`
	// Keep is how many run transcripts to hold on to.
	//
	// -1 keeps every one, 0 keeps none, and n keeps the most recent n.
	// It exists because a repeat makes a session per run and nothing was
	// ever going to prune them: hourly is twenty-four a day, and the
	// session list becomes a log of a job nobody is reading. Zero is a
	// real answer for a booking whose result is its status — the row
	// still says whether each run worked, since that lives on this
	// conversation's log rather than in the run's transcript.
	Keep int `json:"keep"`
	// Runs is how many times this has fired, and Fails how many of those
	// failed in a row. Both are counted from the log on a restart rather
	// than stored, since every run already appends a status event.
	Runs  int `json:"runs,omitempty"`
	Fails int `json:"fails,omitempty"`
	// History is the run sessions still on disk, oldest first. What Keep
	// prunes from the back.
	History []string `json:"history,omitempty"`
}

// RepeatOptions is everything about a booking that repeats, so Add keeps
// one parameter for the subject rather than five.
type RepeatOptions struct {
	Rule      when.Repeat
	StopAt    time.Time
	StopAfter int
	Keep      int
}

// maxConsecutiveFailures suspends a repeat that keeps failing.
//
// This is the half of the old refusal that the stop conditions do not
// answer: "a repeat needs a stop condition and a policy for what happens
// when it fails". An expired credential fails identically every time, and
// an unattended turn that needs a permission nobody answers spends
// unattendedPermissionWait on every single occurrence — so without this,
// one wrong booking is a machine that wakes up forever to do nothing.
//
// Three rather than one, because the failure worth suspending for is the
// one that will not clear on its own, and a single network blip is not
// it. A success resets the count, so a series that recovers keeps going
// without anybody being told.
const maxConsecutiveFailures = 3

// defaultKeep is how many run transcripts a repeat holds on to when
// nobody says.
//
// Not -1. A booking left to run all year would fill the session list with
// a conversation an hour, and a default nobody chose should not be the
// one that grows without limit. Not 0 either: deleting what somebody did
// not ask to have deleted is the worse mistake of the two, and a default
// that silently throws away every result would be exactly that. Ten is
// enough to look back over a few days of a daily job and see what
// changed.
const defaultKeep = 10

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
func (s *Scheduler) Add(sessionID, agentName, prompt string, at time.Time, rep RepeatOptions) (Scheduled, error) {
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
		Repeat: rep.Rule, StopAt: rep.StopAt, StopAfter: rep.StopAfter,
		Keep: rep.Keep,
	}
	s.entries[key(sessionID, id)] = entry
	s.mu.Unlock()

	// Recorded on the conversation's own log, which is what makes the
	// row in the panel survive a reload — the same reason a background
	// task's row is built from task.spawned rather than from memory.
	created := map[string]any{
		"id": id, "at": at.Format(time.RFC3339), "prompt": prompt, "agent": agentName,
	}
	// The rule and its limits go on the event, because Restore rebuilds
	// the books from this log and a field that is not written here does
	// not survive a restart.
	if rep.Rule.On() {
		created["repeat_every"] = rep.Rule.Every
		created["repeat_unit"] = rep.Rule.Unit
		created["keep"] = rep.Keep
		if !rep.StopAt.IsZero() {
			created["stop_at"] = rep.StopAt.Format(time.RFC3339)
		}
		if rep.StopAfter > 0 {
			created["stop_after"] = rep.StopAfter
		}
	}
	if _, err := s.loop.Store.Append(sessionID, events.TypeScheduleCreated, created); err != nil {
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
	entryName, stopAfter := entry.Name, entry.StopAfter
	repeatWords := entry.Repeat.String()
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
	// store. The run number is in it because a repeat runs more than
	// once: a fixed id made the second occurrence fail outright, since
	// creating a session that already exists is an error.
	s.mu.Lock()
	runNo := entry.Runs + 1
	s.mu.Unlock()
	runID := fmt.Sprintf("%s-%s-run%d", sessionID, id, runNo)
	// Under the parent's workspace and, through the parent, its
	// permission switches: the work was booked in a project and by
	// somebody who had already decided what may happen without asking.
	if _, err := s.loop.Store.CreateSessionIn(runID, sessionID, agentName, s.loop.SessionDir(sessionID), false); err != nil {
		s.finish(sessionID, id, "", ScheduleFailed, fmt.Sprintf("could not create the session to run in: %v", err))
		return
	}
	s.status(sessionID, id, ScheduleRunning, runID, "")
	// At the head of the run's own transcript, before the prompt: opened
	// on its own, a run session is a conversation that starts with an
	// instruction nobody in it typed. The same thing session.forked does
	// for a copied conversation, and for the same reason.
	s.loop.Store.Append(runID, events.TypeSessionScheduled, map[string]any{
		"schedule":  id,
		"name":      entryName,
		"run":       runNo,
		"at":        time.Now().Format(time.RFC3339),
		"repeat":    repeatWords,
		"from":      sessionID,
		"run_total": stopAfter,
	})

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
	s.advance(sessionID, id, status, errText)
}

// advance is what happens to a repeating booking after one run: arm the
// next one, or end the series and say which of the four reasons it was.
//
// A booking that does not repeat falls straight out, which is most of
// them and is why this is the only place the repeat logic lives.
func (s *Scheduler) advance(sessionID, id, status, errText string) {
	s.mu.Lock()
	entry, ok := s.entries[key(sessionID, id)]
	if !ok || !entry.Repeat.On() {
		s.mu.Unlock()
		return
	}
	// A run that never happened is not a run. "missed" means the moment
	// passed with the work not done — an archived conversation, or a
	// parent that is gone — and neither is something the next occurrence
	// would do any better, so the series ends rather than counting it as
	// a failure and retrying twice more.
	if status == ScheduleMissed {
		s.mu.Unlock()
		return
	}

	entry.Runs++
	if status == ScheduleFailed {
		entry.Fails++
	} else {
		entry.Fails = 0
	}
	if entry.RunSession != "" {
		entry.History = append(entry.History, entry.RunSession)
	}
	runs, fails, keep := entry.Runs, entry.Fails, entry.Keep
	rule, stopAt, stopAfter := entry.Repeat, entry.StopAt, entry.StopAfter
	from := entry.At
	history := append([]string(nil), entry.History...)
	s.mu.Unlock()

	// Pruning happens whatever comes next, including when the series is
	// ending: the transcripts a finished series leaves behind are the
	// same clutter as the ones a running one does.
	s.prune(sessionID, id, history, keep)

	switch {
	case fails >= maxConsecutiveFailures:
		// The policy the old refusal named. Whatever is wrong has been
		// wrong three times running and is not going to be fixed by
		// waking up again.
		s.stop(sessionID, id, ScheduleSuspended, fmt.Sprintf(
			"stopped after %d runs in a row failed. Last error: %s", fails, errText))
		return
	case stopAfter > 0 && runs >= stopAfter:
		s.stop(sessionID, id, status, "")
		return
	}

	next := rule.Next(from, time.Now())
	if !stopAt.IsZero() && next.After(stopAt) {
		s.stop(sessionID, id, status, "")
		return
	}

	s.mu.Lock()
	if e, ok := s.entries[key(sessionID, id)]; ok {
		e.At, e.Status, e.Seen = next, SchedulePending, false
	}
	s.mu.Unlock()
	s.loop.Store.Append(sessionID, events.TypeScheduleStatus, map[string]any{
		"id": id, "status": SchedulePending, "at": next.Format(time.RFC3339),
		"runs": runs,
	})
	s.arm(sessionID, id, next)
}

// stop ends a series without arming another run.
func (s *Scheduler) stop(sessionID, id, status, errText string) {
	s.mu.Lock()
	entry, ok := s.entries[key(sessionID, id)]
	if !ok {
		s.mu.Unlock()
		return
	}
	entry.Status = status
	if errText != "" {
		entry.Error = errText
	}
	runs := entry.Runs
	s.mu.Unlock()
	data := map[string]any{"id": id, "status": status, "runs": runs, "ended": true}
	if errText != "" {
		data["error"] = errText
	}
	s.loop.Store.Append(sessionID, events.TypeScheduleStatus, data)
}

// prune deletes the run transcripts a repeat has outgrown.
//
// keep is -1 for all of them, 0 for none, and n for the most recent n.
// Zero really does delete the run that has just finished: for a booking
// whose result is its status — a health check, a nightly build kick —
// the transcript is the part nobody reads, and a session per hour is how
// the session list stops being a list of conversations.
//
// What is not deleted is the record of whether each run worked. That
// lives on this conversation's own log as schedule.status events, so a
// booking with keep 0 still shows its failures.
func (s *Scheduler) prune(sessionID, id string, history []string, keep int) {
	if keep < 0 {
		return
	}
	cut := len(history) - keep
	if cut <= 0 {
		return
	}
	for _, runID := range history[:cut] {
		// DeleteTree rather than Delete: a run may have spawned
		// background tasks of its own, and leaving those behind would
		// prune the parent and keep the children.
		if err := s.loop.Store.DeleteTree(runID); err != nil {
			// Nothing to escalate to. The next prune tries again, since
			// the id stays in the history until it is really gone.
			continue
		}
	}
	s.mu.Lock()
	if e, ok := s.entries[key(sessionID, id)]; ok {
		e.History = append([]string(nil), history[cut:]...)
		// The row points at the newest kept run, or at nothing when none
		// are kept. A link to a transcript that has been deleted is worse
		// than no link.
		if len(e.History) == 0 {
			e.RunSession = ""
		}
	}
	s.mu.Unlock()
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
				e := &Scheduled{
					ID: id, SessionID: sessionID, At: at,
					Prompt: str(ev.Data["prompt"]), Agent: str(ev.Data["agent"]),
					Status: SchedulePending,
				}
				// The rule and its limits, if this one repeats. Rebuilt
				// from the log rather than stored anywhere else, which is
				// the same thing that makes the row survive a reload.
				if every := num(ev.Data["repeat_every"]); every > 0 {
					e.Repeat = when.Repeat{Every: every, Unit: str(ev.Data["repeat_unit"])}
					e.Keep = num(ev.Data["keep"])
					e.StopAfter = num(ev.Data["stop_after"])
					if t, terr := time.Parse(time.RFC3339, str(ev.Data["stop_at"])); terr == nil {
						e.StopAt = t
					}
				}
				byID[id] = e
				order = append(order, id)
			case events.TypeScheduleStatus:
				if e := byID[id]; e != nil {
					e.Status = str(ev.Data["status"])
					if rs := str(ev.Data["run_session"]); rs != "" {
						e.RunSession = rs
						// One entry per run, oldest first, which is what
						// prune walks. Duplicates are skipped because a
						// single run appends its id twice: once when it
						// starts running and once when it finishes.
						if n := len(e.History); n == 0 || e.History[n-1] != rs {
							e.History = append(e.History, rs)
						}
					}
					e.Error = str(ev.Data["error"])
					// A repeat's next moment travels on the status event
					// that re-arms it, so a restart finds the occurrence
					// it is actually waiting for rather than the first one.
					if t, terr := time.Parse(time.RFC3339, str(ev.Data["at"])); terr == nil {
						e.At = t
					}
					if r := num(ev.Data["runs"]); r > 0 {
						e.Runs = r
					}
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

// num reads a whole number back off an event.
//
// float64 first, because that is what a number becomes once the log has
// been through JSON — which every value here has, since Restore reads
// the file rather than remembering what it wrote.
func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
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

// SetUnattendedWait changes how long an unattended turn waits, and
// returns a func that puts it back.
//
// Two callers want opposite things, which is why this is settable at all.
// A scheduled run fires while localcode is running, which usually means
// somebody is at the desk: the question is mirrored into the conversation
// that booked the work, and five minutes is long enough to notice it. A
// one-shot "localcode run" is a pipe, and a pipe has no desk — it waits
// not at all and refuses at once. Tests move it to avoid waiting either
// amount.
//
// Here rather than in a _test file so the var above has exactly one
// writer, and one place saying why it moves.
func SetUnattendedWait(d time.Duration) func() {
	prev := unattendedPermissionWait
	unattendedPermissionWait = d
	return func() { unattendedPermissionWait = prev }
}

// unattendedRefusal is what the model is told when nobody answered. It
// says the tool was not run and why, so the turn can end honestly rather
// than reporting a step it never took.
//
// It used to name one of its two callers: "this turn is scheduled work
// with nobody watching". A one-shot run in a pipe is the other, and it
// waits not at all, so the sentence also read "nobody answered within 0s"
// — which describes somebody being asked. Both halves now say what is
// actually true of either caller, since the fact is the same one and only
// the waiting differs.
func unattendedRefusal(ask tools.Ask) string {
	if unattendedPermissionWait <= 0 {
		return fmt.Sprintf(
			"not run: %q needs permission, and nobody is watching this turn, so there was nobody to ask",
			ask.Description)
	}
	return fmt.Sprintf(
		"not run: %q needs permission, nobody is watching this turn, and no answer came within %s",
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
