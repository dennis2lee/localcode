package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
)

// Two agents in one conversation.
//
// The session's own agent does the work; another agent, usually on
// another model, reviews it; the first one answers the review; and that
// repeats until the reviewer approves or the rounds run out. The shape
// comes from somebody's actual sentence: "write X, then have girl review
// it, then fix it, then let her look again, ten times, and stop when you
// both agree."
//
// Everything in that sentence except "write X" is machinery, and it lives
// here rather than in the prompt. That division is the whole design:
//
//   - The rounds are counted here. A model told "repeat ten times" inside
//     its own turn stops at three and says it is done, and there is no way
//     to tell that from having finished. Worse, a prompt that still
//     carries the protocol hands the author a loop of its own to run
//     inside the loop that is already running it, and the review requests
//     multiply.
//   - The reviewer cannot write. It is given reading tools and a verdict,
//     which is what makes a round a review rather than two agents editing
//     the same file from two directions and overwriting each other.
//   - Ending is a decision with something behind it: an explicit approval,
//     the round budget, or a stall this can actually measure. Two models
//     agreeing is not evidence that the work is correct, and nothing here
//     pretends otherwise — the debate ends and the person reads what came
//     out of it.
//
// The author runs in this session, the reviewer in a child session of its
// own. Not symmetry for its own sake: switching the session's model
// mid-conversation would invalidate its cached prefix, its tools and its
// system prompt at once (see delegatePrompt, which exists for the same
// reason), and the author needs the conversation it has been having. The
// reviewer needs the opposite — a session of its own, kept across rounds,
// so that in round four it still knows what it asked for in round one.

const (
	// debateDefaultRounds is what "/debate girl" books when nobody says a
	// number. Three, not ten: a round is two model turns plus whatever
	// tools each of them runs, so ten is a bill somebody should have to
	// ask for by name.
	debateDefaultRounds = 3
	// debateMaxRounds is the ceiling, wherever the number came from.
	debateMaxRounds = 10
	// debateStallLimit is how many rounds in which the author ran no tools
	// at all end the debate. Two, so one round of pure argument is
	// allowed and a standoff is not.
	debateStallLimit = 2
)

// debateRun is one debate, resolved: who, how many rounds, and the work.
type debateRun struct {
	sessionID string
	author    string
	reviewer  string
	rounds    int
	// command is what the person typed, shown in the transcript in place
	// of the bare task so the record says how the turn was started.
	command string
	task    string
}

// runDebate drives the whole thing and returns when it is over.
//
// It runs inside the turn the person's message opened, which is what
// keeps the session busy for the duration: a second prompt is queued by
// the daemon exactly as it would be during any long turn, and Stop
// cancels this context and takes the reviewer's child with it.
func (l *Loop) runDebate(ctx context.Context, d debateRun) error {
	reviewerCfg := l.agentConfig(ctx, d.reviewer)
	_, profile, err := l.profileFor(ctx, d.reviewer)
	if err != nil {
		return l.refuseDebate(d, fmt.Sprintf("cannot run a debate: %v", err))
	}

	l.Store.Append(d.sessionID, events.TypeDebateStarted, map[string]any{
		"author":   d.author,
		"reviewer": d.reviewer,
		"model":    profile.Model,
		"rounds":   d.rounds,
		"task":     d.task,
	})

	// The reviewer's allowlist, resolved once and pinned to every review
	// turn's context. Read tools and the verdict, and nothing else,
	// whatever the reviewer agent is allowed to do when somebody talks to
	// it directly — a reviewer that can edit is not reviewing.
	reviewCtx := withReviewerTools(ctx, reviewerToolNames(reviewerCfg))

	reviewSession := ""
	stalls := 0
	// Round one is the person's own words: the transcript shows the
	// command they typed and the model is given the work out of it, which
	// is the same displayText/modelText split "/skill" uses.
	display, work := d.command, d.task
	origin := messageOrigin{source: "command.debate"}

	for round := 1; round <= d.rounds; round++ {
		if err := ctx.Err(); err != nil {
			l.endDebate(d, "stopped", round-1, false)
			return err
		}

		mark := l.lastSeq(d.sessionID)
		if err := l.sendWithModelText(ctx, d.sessionID, d.author, display, work, "", "", origin); err != nil {
			// A turn that ended because somebody pressed Stop is not a
			// failure, and reporting it as one puts a red line under a
			// deliberate act. The turn's own error is a cancellation
			// wrapped in whatever the provider said about it, so the
			// context is asked rather than the error inspected.
			reason := "failed"
			if ctx.Err() != nil {
				reason = "stopped"
			}
			l.endDebate(d, reason, round-1, false)
			return err
		}
		done := lastAssistantText(l.Store, d.sessionID)
		files, calls := l.debateProgress(d.sessionID, mark)

		brief := reviewBrief(d, round, done, files, reviewSession != "")
		since := l.lastSeq(reviewSession)
		id, review, err := l.Tasks.SpawnSyncInto(reviewCtx, d.sessionID, reviewSession, d.reviewer, brief)
		if err != nil {
			// The author's work is done and recorded; only the review
			// failed. Saying so and stopping is better than looping on a
			// reviewer that is not answering.
			l.Store.Append(d.sessionID, events.TypeError, map[string]any{
				"error": fmt.Sprintf("the %s agent could not review this round: %v", d.reviewer, err),
			})
			l.endDebate(d, "failed", round, false)
			return nil
		}
		reviewSession = id

		v := readVerdict(l, reviewSession, since, review)
		l.Store.Append(d.sessionID, events.TypeDebateReview, map[string]any{
			"round":    round,
			"rounds":   d.rounds,
			"reviewer": d.reviewer,
			"model":    profile.Model,
			"text":     v.text,
			"approved": v.approved,
			"session":  reviewSession,
		})

		if v.approved {
			l.endDebate(d, "approved", round, true)
			return nil
		}
		// A round in which the author called no tool at all changed
		// nothing: it read the review and talked. One of those is an
		// argument; two in a row is a standoff, and the rounds left will
		// be spent restating it.
		if calls == 0 {
			stalls++
		} else {
			stalls = 0
		}
		if stalls >= debateStallLimit {
			l.endDebate(d, "stalled", round, false)
			return nil
		}

		display = fmt.Sprintf("↳ round %d/%d — %s's review goes back to %s", round+1, d.rounds, d.reviewer, d.author)
		work, origin = authorBrief(d, round, v.text)
	}

	l.endDebate(d, "rounds", d.rounds, false)
	return nil
}

// endDebate writes the closing event, carrying the sentence a person
// reads with it.
//
// The sentence travels in the event rather than being written as a reply,
// and that is not a formatting choice. A reply is a message.part.end, and
// a message.part.end with no user message before it rehydrates as a
// second assistant message immediately after the author's — two assistant
// turns in a row, which some providers reject outright. So the debate's
// last word is data on its own event, composed here so both clients say
// the same thing, and no part of it enters the model's history.
//
// The reason is always named. "It stopped" and "it agreed" look identical
// at the bottom of a long conversation, and only one of them means the
// work has been through review.
func (l *Loop) endDebate(d debateRun, reason string, rounds int, approved bool) {
	var note string
	switch reason {
	case "approved":
		note = fmt.Sprintf("%s approved after %s.", d.reviewer, roundCount(rounds))
	case "rounds":
		note = fmt.Sprintf("all %s used and %s has not approved. The work stands as it is; read it before trusting it.",
			roundCount(d.rounds), d.reviewer)
	case "stalled":
		note = fmt.Sprintf("stopped after %s: %s ran no tool in the last %d, so the rounds left would only restate the disagreement. It is yours to settle.",
			roundCount(rounds), d.author, debateStallLimit)
	case "stopped":
		note = fmt.Sprintf("debate stopped after %s. What was done is kept.", roundCount(rounds))
	default:
		note = fmt.Sprintf("debate ended after %s.", roundCount(rounds))
	}
	l.Store.Append(d.sessionID, events.TypeDebateEnded, map[string]any{
		"reason":   reason,
		"rounds":   rounds,
		"approved": approved,
		"note":     note,
	})
}

// roundCount is "1 round" or "3 rounds".
//
// Its own helper because the number is the whole content of the sentence
// it appears in: plural() answers which word to use and says nothing
// about how many, and "girl approved after rounds" is what comes out of
// forgetting that.
func roundCount(n int) string {
	return fmt.Sprintf("%d %s", n, plural(n, "round", "rounds"))
}

// refuseDebate answers a debate that cannot start, in the shape every
// other local command uses: the typed line, then the reason.
func (l *Loop) refuseDebate(d debateRun, reason string) error {
	l.Store.Append(d.sessionID, events.TypeUserMessage, map[string]any{"text": d.command, "local": true})
	return l.replyText(d.sessionID, reason)
}

// reviewBrief is what the reviewer is given for one round.
//
// What it is not is the author's conversation. A transcript from another
// model carries tool_use blocks for tools this one was never offered,
// which some providers reject outright, and it would be re-sent every
// round. What the reviewer gets instead is the task, the author's own
// account of what it did, the files localcode saw change — and the
// workspace, which it can read for itself.
//
// The account and the file list are deliberately separated. One is what
// the author says; the other is what happened. A reviewer told to check
// the second against the first is doing the job; one handed a summary is
// reviewing a summary.
func reviewBrief(d debateRun, round int, work string, files []string, resumed bool) string {
	var b strings.Builder
	if !resumed {
		fmt.Fprintf(&b, "You are reviewing work by the %q agent, round %d of %d.\n\n", d.author, round, d.rounds)
		fmt.Fprintf(&b, "THE TASK AS THE USER GAVE IT:\n%s\n\n", d.task)
	} else {
		fmt.Fprintf(&b, "The author has answered your last review. Round %d of %d.\n\n", round, d.rounds)
	}

	b.WriteString("WHAT THE AUTHOR SAYS THEY DID:\n")
	b.WriteString(strings.TrimSpace(work))
	b.WriteString("\n\n")

	b.WriteString("FILES LOCALCODE SAW THEM WRITE THIS ROUND:\n")
	if len(files) == 0 {
		b.WriteString("(none — no file was written this round)\n\n")
	} else {
		for _, f := range files {
			b.WriteString("  " + f + "\n")
		}
		b.WriteString("\n")
	}

	if resumed {
		b.WriteString("Check whether the findings you raised are actually addressed in the files, " +
			"not merely answered in the text above.\n")
	} else {
		b.WriteString("Read the files yourself before judging: the account above is the author's, not evidence.\n")
	}
	b.WriteString("You cannot edit anything, and you are not being asked to — say what is wrong and why.\n\n")
	b.WriteString("Finish by calling the Verdict tool. Set approved only if you would ship this as it stands; " +
		"anything less is not approved, and say what would change your mind. " +
		"If you cannot call the tool, make the last line of your reply exactly APPROVED or CHANGES REQUESTED.")
	return b.String()
}

// authorBrief is the review handed back to the author, and the message
// that opens the next round.
//
// The framing is localcode's and the review is another model's, so they
// are not the same text with the same authority: the span marks where the
// reviewer's words start and stop, and everything outside it is the
// product's own. This is the same rule injected user text follows.
func authorBrief(d debateRun, round int, review string) (string, messageOrigin) {
	head := fmt.Sprintf("The %q agent reviewed your work — round %d of %d.\n\n--- review ---\n", d.reviewer, round, d.rounds)
	body := strings.TrimSpace(review)
	tail := "\n--- end review ---\n\n" +
		"Fix what you agree with. Where you disagree, say so and say why — do not pass over it in silence. " +
		"Do not ask for another review: it happens on its own when your turn ends."
	text := head + body + tail
	return text, messageOrigin{
		source: "reminder.debate",
		spans: []provider.BlockSource{{
			ID: "debate.review." + d.reviewer, From: len(head), To: len(head) + len(body),
		}},
		// Nobody typed this. It is in the log because the model was given
		// it, and it is not a line to paint above the author's reply or to
		// hand back on Up — the review it carries is shown by its own
		// event, with the reviewer's name on it.
		auto: true,
	}
}

// verdict is what a round of review came to.
type verdict struct {
	approved bool
	text     string
}

// readVerdict works out whether the reviewer approved, and what the
// author is given.
//
// The tool first, because it is a boolean the model set rather than a
// sentence somebody has to interpret, and prose is exactly what cannot be
// interpreted reliably across two languages and a small local model. The
// fallback is a single exact last line, for a model that will not call
// tools at all.
//
// The findings are the review when the tool was called, not the
// reviewer's closing message. The tool result tells it so in as many
// words, and a model that has just been told "the author will be given
// your findings" reasonably ends its turn with "done" — which, taken as
// the review, is a round in which the author is handed nothing.
//
// Everything that is not an explicit approval is "not approved":
// silence, an unreadable tool call, a reply with both words in it.
// Ending a debate one round early on a misread is the failure that
// ships, because the work stops being looked at while the transcript
// says somebody approved it.
func readVerdict(l *Loop, reviewSession string, since uint64, reply string) verdict {
	v := verdict{text: strings.TrimSpace(reply)}
	if l.Store != nil && reviewSession != "" {
		evs, err := l.Store.Events(reviewSession, since)
		if err == nil {
			for i := len(evs) - 1; i >= 0; i-- {
				ev := evs[i]
				if ev.Type != events.TypeToolStart {
					continue
				}
				if name, _ := ev.Data["name"].(string); name != verdictToolName {
					continue
				}
				raw, _ := ev.Data["input"].(string)
				var args verdictArgs
				if json.Unmarshal([]byte(raw), &args) != nil {
					break
				}
				v.approved = args.Approved
				if f := strings.TrimSpace(args.Findings); f != "" {
					v.text = f
				}
				return v
			}
		}
	}
	v.approved = approvedByLastLine(v.text)
	return v
}

// approvedByLastLine is the fallback: the reply's last non-empty line,
// and it has to be the approval and nothing else.
//
// Deliberately not a search for the word. A review that says "I would
// have approved this if the loop were bounded" contains it, and a
// contains-check would end the debate on the sentence that explains why
// it should not.
func approvedByLastLine(text string) bool {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		line = strings.Trim(line, "*_`#.!。 ")
		if line == "" {
			continue
		}
		return strings.EqualFold(line, "APPROVED")
	}
	return false
}

// debateProgress reports what the author's turn did: the files localcode
// saw written, and how many tool calls it made at all.
//
// The two answers are for two different questions. The file list goes to
// the reviewer, and it is honest about its limits — a file moved by a
// shell command is not in it, which is why it is labelled as what
// localcode saw rather than as what changed. The count is the stall
// check, and it counts every tool: a round spent running tests and
// arguing is work, and only a round with no tool call at all is a round
// where nothing happened.
func (l *Loop) debateProgress(sessionID string, since uint64) (files []string, calls int) {
	if l.Store == nil {
		return nil, 0
	}
	evs, err := l.Store.Events(sessionID, since)
	if err != nil {
		return nil, 0
	}
	seen := map[string]bool{}
	for _, ev := range evs {
		if ev.Type != events.TypeToolStart {
			continue
		}
		calls++
		name, _ := ev.Data["name"].(string)
		if name != "write_file" && name != "edit" {
			continue
		}
		var args struct {
			Path string `json:"path"`
		}
		raw, _ := ev.Data["input"].(string)
		if json.Unmarshal([]byte(raw), &args) != nil || args.Path == "" || seen[args.Path] {
			continue
		}
		seen[args.Path] = true
		files = append(files, args.Path)
	}
	return files, calls
}

// lastSeq is the watermark a round starts from: everything already in the
// log, so what follows can be attributed to this round and nothing else.
func (l *Loop) lastSeq(sessionID string) uint64 {
	if l.Store == nil || sessionID == "" {
		return 0
	}
	evs, err := l.Store.Events(sessionID, 0)
	if err != nil || len(evs) == 0 {
		return 0
	}
	return evs[len(evs)-1].Seq
}

// readOnlyTools is what a reviewer may call, besides saying what it
// thinks.
//
// An allowlist, not "everything except the writing ones". bash is the
// reason: "cd /tmp && sh -c ..." is not a path and cannot be judged by
// looking at it, so a reviewer with a shell is a reviewer that can write.
// The cost is real and worth stating — a reviewer cannot run the tests,
// so it judges by reading, and running them stays the author's job.
var readOnlyTools = []string{"read_file", "glob", "grep"}

// reviewerToolNames is the allowlist for one reviewer: the reading tools
// its own configuration also permits, plus the verdict.
//
// The verdict is appended after the intersection rather than included in
// it, because an agent configured with `"tools": ["read_file"]` would
// otherwise be a reviewer with no way to report, which is a debate that
// can never end early.
func reviewerToolNames(cfg config.AgentConfig) []string {
	allowed := readOnlyTools
	if len(cfg.Tools) > 0 {
		own := map[string]bool{}
		for _, name := range cfg.Tools {
			own[name] = true
		}
		allowed = nil
		for _, name := range readOnlyTools {
			if own[name] {
				allowed = append(allowed, name)
			}
		}
	}
	return append(append([]string(nil), allowed...), verdictToolName)
}

// reviewerToolsKey carries a review turn's allowlist, and marks the turn
// as a review.
type reviewerToolsKey struct{}

func withReviewerTools(ctx context.Context, allowed []string) context.Context {
	return context.WithValue(ctx, reviewerToolsKey{}, allowed)
}

// reviewerTools reports the allowlist a debate review runs under, and
// whether this turn is one at all.
func reviewerTools(ctx context.Context) ([]string, bool) {
	allowed, ok := ctx.Value(reviewerToolsKey{}).([]string)
	return allowed, ok && len(allowed) > 0
}

// routeDebate answers "/debate <reviewer> [rounds] <task>".
func (l *Loop) routeDebate(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/debate")
	if !ok {
		return false, nil
	}
	refuse := func(reason string) (bool, error) {
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
		return true, l.replyText(sessionID, reason)
	}

	agents := l.delegatableAgents(ctx)
	if arg == "" {
		return refuse("usage: /debate <reviewer> [rounds] <what to do>\n" +
			"  e.g. /debate " + exampleReviewer(agents, agentName) + " 5 write a script that sums 1..10\n\n" +
			"The session's own agent does the work and the reviewer reads it and says what is wrong, " +
			"round after round, until it approves or the rounds run out (default " +
			strconv.Itoa(debateDefaultRounds) + ", at most " + strconv.Itoa(debateMaxRounds) + ").\n" +
			"The reviewer can read but not write.\nAvailable agents: " +
			strings.Join(agentNamesOf(agents), ", "))
	}
	if l.Tasks == nil {
		return refuse("this build cannot run a debate: no sub-agent manager")
	}
	// A review turn is a sub-agent's, and a sub-agent starting a debate of
	// its own is a tree of them nobody asked for. Same reason a scheduled
	// turn may not book another.
	if Unattended(ctx) || l.isDelegatedSession(sessionID) {
		return refuse("a debate can only be started from a conversation somebody is having, not from a sub-agent or a scheduled run")
	}

	reviewer, rounds, task, err := parseDebateCommand(arg)
	if err != nil {
		return refuse(err.Error())
	}
	if _, known := agents[reviewer]; !known {
		return refuse(fmt.Sprintf("no agent named %q. Available: %s", reviewer, strings.Join(agentNamesOf(agents), ", ")))
	}
	if reviewer == agentName {
		// The alternatives, with the refused name taken out of them.
		// Offering "boy, girl" to somebody who was just told boy will not
		// do reads as a message nobody checked.
		return refuse(fmt.Sprintf("%q is already this conversation's agent, so it would be reviewing itself. "+
			"Name a different one: %s", reviewer, strings.Join(otherAgents(agents, agentName), ", ")))
	}

	return true, l.runDebate(ctx, debateRun{
		sessionID: sessionID,
		author:    agentName,
		reviewer:  reviewer,
		rounds:    rounds,
		command:   strings.TrimSpace(text),
		task:      task,
	})
}

// parseDebateCommand splits "<reviewer> [rounds] <task>".
//
// The rounds are optional and positional, which is one real ambiguity:
// "/debate girl 10페이지 문서를 써라" opens with a token that starts with
// digits and is not a count. So a token is the round count only if it is
// digits and nothing else — anything with a letter attached is the first
// word of the task.
func parseDebateCommand(arg string) (reviewer string, rounds int, task string, err error) {
	reviewer, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)
	rounds = debateDefaultRounds

	if head, tail, _ := strings.Cut(rest, " "); allDigits(head) {
		n, convErr := strconv.Atoi(head)
		if convErr != nil || n < 1 || n > debateMaxRounds {
			return "", 0, "", fmt.Errorf("%s rounds is not a number between 1 and %d", head, debateMaxRounds)
		}
		rounds, rest = n, strings.TrimSpace(tail)
	}
	if rest == "" {
		return "", 0, "", fmt.Errorf("say what to do: /debate %s %d <what to do>", reviewer, rounds)
	}
	return reviewer, rounds, rest, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// otherAgents is every agent except the one already running: the names
// that could actually be a reviewer here.
func otherAgents(agents map[string]config.AgentConfig, self string) []string {
	var out []string
	for _, name := range agentNamesOf(agents) {
		if name != self {
			out = append(out, name)
		}
	}
	return out
}

// exampleReviewer picks a name for the usage line that is not the agent
// already running, so the example is one that would actually work.
func exampleReviewer(agents map[string]config.AgentConfig, self string) string {
	if others := otherAgents(agents, self); len(others) > 0 {
		return others[0]
	}
	return "<reviewer>"
}
