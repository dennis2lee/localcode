package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
)

// Two agents in one conversation, or four.
//
// The session's own agent does the work; one or more other agents, each
// usually on another model, review it; the first one answers them; and
// that repeats until every reviewer approves or the rounds run out. The
// shape comes from somebody's actual sentence: "write X, then have girl
// review it, then fix it, then let her look again, ten times, and stop
// when you both agree."
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
//   - The reviewers cannot write. They are given reading tools, the
//     project's own check, and a verdict — which is what makes a round a
//     review rather than several agents editing the same file from
//     different directions and overwriting each other.
//   - Ending is a decision with something behind it: every reviewer
//     approving, the round budget, or a stall this can actually measure.
//     Several models agreeing is not evidence that the work is correct,
//     and nothing here pretends otherwise — the debate ends and the
//     person reads what came out of it.
//
// The author runs in this session, each reviewer in a child session of
// its own. Not symmetry for its own sake: switching the session's model
// mid-conversation would invalidate its cached prefix, its tools and its
// system prompt at once (see delegatePrompt, which exists for the same
// reason), and the author needs the conversation it has been having. A
// reviewer needs the opposite — a session of its own, kept across rounds,
// so that in round four it still knows what it asked for in round one.
//
// Reviewers run concurrently and cannot see each other. Three models
// agreeing is worth something only if they arrived there separately; a
// panel that reads the first opinion is one opinion with witnesses.

const (
	// debateDefaultRounds is what "/debate girl" books when nobody says a
	// number. Three, not ten: a round is one author turn plus one turn per
	// reviewer, plus whatever tools each of them runs, so ten is a bill
	// somebody should have to ask for by name.
	debateDefaultRounds = 3
	// debateMaxRounds is the ceiling, wherever the number came from.
	debateMaxRounds = 10
	// debateMaxReviewers bounds a panel. Each one is a model turn per
	// round, so the turn count is rounds x (1 + reviewers) and three is
	// already forty turns at the round ceiling.
	debateMaxReviewers = 3
	// debateStallLimit is how many rounds in which the author ran no tools
	// at all end the debate. Two, so one round of pure argument is
	// allowed and a standoff is not.
	debateStallLimit = 2
)

// debateRun is one debate, resolved: who, how many rounds, and the work.
type debateRun struct {
	sessionID string
	author    string
	reviewers []string
	rounds    int
	// command is what the person typed, shown in the transcript in place
	// of the bare task so the record says how the turn was started.
	command string
	task    string
	// historyMark is where this debate's own turns start in the author
	// session's history, taken before the first of them. See
	// collapsedDebate.
	historyMark int
}

// reviewResult is one reviewer's round.
type reviewResult struct {
	reviewer string
	model    string
	session  string
	verdict  verdict
	err      error
}

// runDebate drives the whole thing and returns when it is over.
//
// It runs inside the turn the person's message opened, which is what
// keeps the session busy for the duration: a second prompt is queued by
// the daemon exactly as it would be during any long turn, and Stop
// cancels this context and takes the reviewers' children with it.
func (l *Loop) runDebate(ctx context.Context, d debateRun) error {
	models := map[string]string{}
	allowed := map[string][]string{}
	for _, name := range d.reviewers {
		_, profile, err := l.profileFor(ctx, name)
		if err != nil {
			return l.refuseDebate(d, fmt.Sprintf("cannot run a debate: %v", err))
		}
		models[name] = profile.Model
		// Each reviewer's allowlist is resolved once, from its own agent
		// config, and pinned to every review turn it runs.
		allowed[name] = reviewerToolNames(l.agentConfig(ctx, name))
	}

	l.Store.Append(d.sessionID, events.TypeDebateStarted, map[string]any{
		"author":    d.author,
		"reviewer":  strings.Join(d.reviewers, ", "),
		"reviewers": d.reviewers,
		"model":     models[d.reviewers[0]],
		"models":    models,
		"rounds":    d.rounds,
		"task":      d.task,
	})

	// Marks every turn of this debate, the author's included, so the
	// Debate tool can refuse to open another one from inside it.
	ctx = withInDebate(ctx)

	// Where the debate's own turns begin. Everything appended from here
	// belongs to the debate and leaves again when it ends; see
	// collapsedDebate for why, and rehydrateHistory for the same line
	// taken from the log after a restart.
	d.historyMark = len(l.history(d.sessionID))

	sessions := map[string]string{}
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
		changes := l.changeReport(d.sessionID, files)

		results := l.runReviews(ctx, d, round, done, changes, sessions, models, allowed)
		answered := 0
		approvals := 0
		for _, r := range results {
			if r.session != "" {
				sessions[r.reviewer] = r.session
			}
			if r.err != nil {
				l.Store.Append(d.sessionID, events.TypeError, map[string]any{
					"error":     fmt.Sprintf("the %s agent could not review round %d: %v", r.reviewer, round, r.err),
					"recovered": true,
				})
				continue
			}
			answered++
			if r.verdict.approved {
				approvals++
			}
			l.Store.Append(d.sessionID, events.TypeDebateReview, map[string]any{
				"round":    round,
				"rounds":   d.rounds,
				"reviewer": r.reviewer,
				"model":    r.model,
				"text":     r.verdict.text,
				"approved": r.verdict.approved,
				"session":  r.session,
			})
		}

		if answered == 0 {
			// Nobody reviewed anything. Looping would spend the budget on
			// a provider that is not answering.
			l.endDebate(d, "failed", round, false)
			return nil
		}
		// Every reviewer that answered has to approve. A panel where one
		// approves and one does not is a disagreement, and taking the
		// approval would be picking the answer that ends the work.
		if approvals == answered && answered == len(d.reviewers) {
			l.endDebate(d, "approved", round, true)
			return nil
		}
		// A round in which the author called no tool at all changed
		// nothing: it read the reviews and talked. One of those is an
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

		display = fmt.Sprintf("↳ round %d/%d — %s back to %s",
			round+1, d.rounds, plural(len(d.reviewers), "the review goes", "the reviews go"), d.author)
		work, origin = authorBrief(d, round, results)
	}

	l.endDebate(d, "rounds", d.rounds, false)
	return nil
}

// runReviews runs one round's reviewers, at the same time and apart.
//
// Concurrent because they are independent and a panel run in series
// multiplies the wall clock by the number of opinions. Apart because
// that independence is the only thing a panel is for: each reviewer has
// its own session, sees only the author's work, and never sees another
// reviewer's finding.
func (l *Loop) runReviews(
	ctx context.Context, d debateRun, round int, work, changes string,
	sessions map[string]string, models map[string]string, allowed map[string][]string,
) []reviewResult {
	results := make([]reviewResult, len(d.reviewers))
	var wg sync.WaitGroup
	for i, name := range d.reviewers {
		i, name := i, name
		prior := sessions[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := reviewResult{reviewer: name, model: models[name], session: prior}
			brief := reviewBrief(d, name, round, work, changes, prior != "")
			since := l.lastSeq(prior)
			id, review, err := l.Tasks.SpawnSyncInto(
				withReviewerTools(ctx, allowed[name]), d.sessionID, prior, name, brief)
			if id != "" {
				r.session = id
			}
			if err != nil {
				r.err = err
			} else {
				r.verdict = readVerdict(l, r.session, since, review)
			}
			results[i] = r
		}()
	}
	wg.Wait()
	return results
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
	who := strings.Join(d.reviewers, " and ")
	var note string
	switch reason {
	case "approved":
		note = fmt.Sprintf("%s approved after %s.", who, roundCount(rounds))
	case "rounds":
		note = fmt.Sprintf("all %s used and %s %s not approved. The work stands as it is; read it before trusting it.",
			roundCount(d.rounds), who, plural(len(d.reviewers), "has", "have"))
	case "stalled":
		note = fmt.Sprintf("stopped after %s: %s ran no tool in the last %d, so the rounds left would only restate the disagreement. It is yours to settle.",
			roundCount(rounds), d.author, debateStallLimit)
	case "stopped":
		note = fmt.Sprintf("debate stopped after %s. What was done is kept.", roundCount(rounds))
	case "failed":
		note = fmt.Sprintf("debate ended after %s: no review came back. What was done is kept.", roundCount(rounds))
	default:
		note = fmt.Sprintf("debate ended after %s.", roundCount(rounds))
	}
	// The rounds leave the model's context here, and only here: the
	// author needed them while they were running. Read before the note is
	// finished, because whether anything was collapsed is what decides
	// whether the note should mention it.
	switch collapsed, kept := l.collapseDebate(d); {
	case !collapsed:
		// Nothing to take out: a debate that ended on its first round
		// with no review to answer is already just the task and the
		// answer. Saying otherwise would describe work that did not
		// happen.
	case kept == 0:
		note += " The debate produced no answer, so nothing from it is in the model's context. " +
			"The rounds are still in this conversation and in its log."
	default:
		note += " The rounds leave the model's context here; what it carries on with is the task and the " +
			"work as it now stands. They stay in this conversation and in its log."
	}
	l.Store.Append(d.sessionID, events.TypeDebateEnded, map[string]any{
		"reason":   reason,
		"rounds":   rounds,
		"approved": approved,
		"note":     note,
	})
}

// collapseDebate replaces the debate's own turns in the author session's
// history with what came out of them, and reports whether there was
// anything to replace.
//
// A debate runs in this session because the author needs the conversation
// it has been having: its history, its cached prefix, its tools. That is
// an argument for the rounds being there while they run, and none at all
// for their being there afterwards, which is what this used to do.
//
// What went wrong when they stayed is that a brief is localcode's own
// text in a user message, and it ends in an instruction — "Fix what you
// agree with ... Do not ask for another review: it happens on its own
// when your turn ends" — which is false the moment the debate is over.
// Every later message in that conversation carried it, and a standing
// instruction that has stopped being true is the worst kind: nothing in
// the request says it has expired.
//
// The rounds also cost what they cost. Three rounds of three reviewers is
// several kilobytes of review text on every message sent afterwards, for
// a panel that has finished.
//
// What this is *not* is the explanation for a debate starting without
// anybody typing "/debate". That one had a mechanical cause and it was in
// the Web UI: the debate dialog wrote its command into the prompt box
// over whatever was being composed there, and a command refused mid-turn
// is deliberately left in the box, so the next thing typed went out
// joined to it. See startDebate in static/js/debate.js. Whether a history
// full of rounds also makes a model reach for the Debate tool again is a
// reasonable worry and is not something this can claim to have measured.
//
// So the debate costs the conversation what a delegation costs it: the
// result. The rounds are in the transcript and in the log, where the
// person reads them; they are not in what the next message is sent with.
func (l *Loop) collapseDebate(d debateRun) (collapsed bool, kept int) {
	h := l.history(d.sessionID)
	out := collapsedDebate(h, d.historyMark, d.task)
	if len(out) == len(h) {
		return false, len(h) - d.historyMark
	}
	l.setHistory(d.sessionID, out)
	return true, len(out) - d.historyMark
}

// collapsedDebate is that rule as a function of the history alone, so the
// live path and the one that rebuilds a session from its log cannot come
// to different answers about the same debate.
//
// What is kept is the message that opened the debate and the last thing
// the author actually said. A debate that said nothing keeps neither: the
// opening was localcode's message on the author's behalf, and leaving it
// alone would end the history on a user message nothing answered, which
// is a shape Bedrock rejects outright and a claim that work was asked for
// and abandoned.
func collapsedDebate(h []provider.Message, mark int, task string) []provider.Message {
	if mark < 0 || mark >= len(h) {
		return h
	}
	// The mark is an offset into a history that something else is allowed
	// to replace out from under it. Auto-compaction is the one that
	// actually happens: it is tried at the top of every turn, the author's
	// rounds included, and a long debate is exactly what crosses the
	// threshold. It calls setHistory with a single summary message, after
	// which the mark names a position in a history that no longer exists
	// and collapsing to it would delete the wrong messages.
	//
	// So the mark has to be confirmed rather than trusted, and the debate
	// already knows what is supposed to be there: its own opening message,
	// which is the task and nothing else. Anything else and this leaves
	// the history alone — the rounds stay, which is the old behaviour and
	// is merely worse rather than wrong.
	if opening := h[mark]; opening.Role != provider.RoleUser || messageText(opening) != task {
		return h
	}
	answer := ""
	for i := len(h) - 1; i > mark; i-- {
		if h[i].Role != provider.RoleAssistant {
			continue
		}
		if text := messageText(h[i]); strings.TrimSpace(text) != "" {
			answer = text
			break
		}
	}
	// A fresh backing array either way: the tail being dropped must not
	// stay reachable through the slice that replaces it.
	kept := h[:mark:mark]
	if answer == "" {
		return kept
	}
	return append(kept, h[mark], provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Block{provider.TextBlock(answer)},
	})
}

// messageText is a message's text, with tool calls and results left out.
func messageText(m provider.Message) string {
	var b strings.Builder
	for _, blk := range m.Content {
		if blk.Type == provider.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
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

// reviewBrief is what one reviewer is given for one round.
//
// What it is not is the author's conversation. A transcript from another
// model carries tool_use blocks for tools this one was never offered,
// which some providers reject outright, and it would be re-sent every
// round. What the reviewer gets instead is the task, the author's own
// account of what it did, what actually changed — and the workspace,
// which it can read for itself.
//
// The account and the change report are deliberately separate. One is
// what the author says; the other is what happened. A reviewer told to
// check the second against the first is doing the job; one handed a
// summary is reviewing a summary.
func reviewBrief(d debateRun, reviewer string, round int, work, changes string, resumed bool) string {
	var b strings.Builder
	if !resumed {
		fmt.Fprintf(&b, "You are reviewing work by the %q agent, round %d of %d.\n\n", d.author, round, d.rounds)
		fmt.Fprintf(&b, "THE TASK AS THE USER GAVE IT:\n%s\n\n", d.task)
		if len(d.reviewers) > 1 {
			// Named, and named as separate: a reviewer that believes
			// somebody else is covering half of it covers less than it
			// would alone, which is the opposite of what a panel is for.
			fmt.Fprintf(&b, "%d agents are reviewing this independently (%s). You cannot see the others' "+
				"findings and they cannot see yours. Review the whole of it yourself.\n\n",
				len(d.reviewers), strings.Join(d.reviewers, ", "))
		}
	} else {
		fmt.Fprintf(&b, "The author has answered your last review. Round %d of %d.\n\n", round, d.rounds)
	}

	b.WriteString("WHAT THE AUTHOR SAYS THEY DID:\n")
	b.WriteString(strings.TrimSpace(work))
	b.WriteString("\n\n")
	b.WriteString(changes)
	b.WriteString("\n")

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

// changeReport is the "what actually changed" half of the brief.
//
// A real diff where there is a repository to ask, and the tool-call list
// where there is not — labelled as what it is either way, because a
// reviewer that treats a list of write_file calls as a diff reviews the
// wrong set of files and does not know it.
func (l *Loop) changeReport(sessionID string, files []string) string {
	dir := l.SessionDir(sessionID)
	if diff, ok := workspaceDiff(dir); ok {
		if diff == "" {
			return "THE CHANGE, AS GIT SEES IT:\n(nothing has changed in the working tree)\n"
		}
		return "THE CHANGE, AS GIT SEES IT (against the last commit, so it includes earlier rounds):\n" + diff + "\n"
	}
	var b strings.Builder
	b.WriteString("FILES LOCALCODE SAW WRITTEN (this is not a diff — " +
		"it is the file tools it watched, so anything the shell changed is missing from it):\n")
	if len(files) == 0 {
		b.WriteString("  (none)\n")
		return b.String()
	}
	for _, f := range files {
		b.WriteString("  " + f + "\n")
	}
	return b.String()
}

// authorBrief is the round's reviews handed back to the author, and the
// message that opens the next round.
//
// The framing is localcode's and each review is another model's, so they
// are not the same text with the same authority: a span marks where each
// reviewer's words start and stop, and everything outside them is the
// product's own. This is the same rule injected user text follows.
func authorBrief(d debateRun, round int, results []reviewResult) (string, messageOrigin) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s reviewed your work — round %d of %d.\n",
		plural(len(d.reviewers), "The reviewer", "The reviewers"), round, d.rounds)

	var spans []provider.BlockSource
	for _, r := range results {
		if r.err != nil {
			continue
		}
		verdict := "changes requested"
		if r.verdict.approved {
			verdict = "approved"
		}
		fmt.Fprintf(&b, "\n--- %s (%s) — %s ---\n", r.reviewer, r.model, verdict)
		body := strings.TrimSpace(r.verdict.text)
		start := b.Len()
		b.WriteString(body)
		spans = append(spans, provider.BlockSource{
			ID: "debate.review." + r.reviewer, From: start, To: start + len(body),
		})
		b.WriteString("\n")
	}

	b.WriteString("\nFix what you agree with. Where you disagree, say so and say why — do not pass over it " +
		"in silence. Do not ask for another review: it happens on its own when your turn ends.")
	return b.String(), messageOrigin{
		source: "reminder.debate",
		spans:  spans,
		// Nobody typed this. It is in the log because the model was given
		// it, and it is not a line to paint above the author's reply or to
		// hand back on Up — the reviews it carries are shown by their own
		// events, with the reviewers' names on them.
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
// The two answers are for two different questions. The file list is the
// fallback change report, for a workspace that is not a git repository,
// and it is honest about its limits. The count is the stall check, and
// it counts every tool: a round spent running the tests and arguing is
// work, and only a round with no tool call at all is a round where
// nothing happened.
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
//
// `check` is the one exception, and it is not a hole in that rule: it
// runs `verify_command` from config.json, which the person wrote before
// any of this started and which no model can change or add an argument
// to. Without it a reviewer judges by reading alone, which is the weaker
// half of a review — the strongest thing anybody can say about a change
// is that it builds and the tests pass. A project that has not set the
// command does not register the tool, and an allowlist naming a tool
// that is not registered simply offers one fewer.
var readOnlyTools = []string{"read_file", "glob", "grep", "check"}

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

// inDebateKey marks every turn of a running debate, the author's turns
// included, so nothing inside one can start another.
type inDebateKey struct{}

func withInDebate(ctx context.Context) context.Context {
	return context.WithValue(ctx, inDebateKey{}, true)
}

func inDebate(ctx context.Context) bool {
	on, _ := ctx.Value(inDebateKey{}).(bool)
	return on
}

// setPendingDebate records a debate the model asked for, to be started
// once the turn that asked is finished. One per session: a second call in
// the same turn replaces the first rather than queueing, because two
// debates over one conversation is not a thing anybody meant.
func (l *Loop) setPendingDebate(d debateRun) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pendingDebate == nil {
		l.pendingDebate = map[string]debateRun{}
	}
	l.pendingDebate[d.sessionID] = d
}

// takePendingDebate removes and returns a session's booked debate.
//
// Taken rather than read, and taken on every path out of a turn
// including the failing ones: a booking left behind would start a debate
// on some later, unrelated message.
func (l *Loop) takePendingDebate(sessionID string) (debateRun, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	d, ok := l.pendingDebate[sessionID]
	delete(l.pendingDebate, sessionID)
	return d, ok
}

// routeDebate answers "/debate <reviewer>[,<reviewer>] [rounds] <task>".
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
			"Name up to " + strconv.Itoa(debateMaxReviewers) + " reviewers, comma separated, to have them " +
			"review it independently — all of them have to approve.\n" +
			"A reviewer can read and run this project's check, and cannot write.\nAvailable agents: " +
			strings.Join(agentNamesOf(agents), ", "))
	}

	reviewers, rounds, task, err := parseDebateCommand(arg)
	if err != nil {
		return refuse(err.Error())
	}
	if reason, ok := l.debateRefusal(ctx, sessionID, agentName, agents, reviewers); !ok {
		return refuse(reason)
	}

	return true, l.runDebate(ctx, debateRun{
		sessionID: sessionID,
		author:    agentName,
		reviewers: reviewers,
		rounds:    rounds,
		command:   strings.TrimSpace(text),
		task:      task,
	})
}

// debateRefusal holds the checks both entrances share — the command and
// the tool — so the two cannot drift into disagreeing about who may
// start a debate.
func (l *Loop) debateRefusal(
	ctx context.Context, sessionID, agentName string,
	agents map[string]config.AgentConfig, reviewers []string,
) (string, bool) {
	if l.Tasks == nil {
		return "this build cannot run a debate: no sub-agent manager", false
	}
	// A review turn is a sub-agent's, and a sub-agent starting a debate of
	// its own is a tree of them nobody asked for. Same reason a scheduled
	// turn may not book another.
	if Unattended(ctx) || inDebate(ctx) || l.isDelegatedSession(sessionID) {
		return "a debate can only be started from a conversation somebody is having, not from a sub-agent, " +
			"a scheduled run, or a debate already under way", false
	}
	seen := map[string]bool{}
	for _, name := range reviewers {
		if _, known := agents[name]; !known {
			return fmt.Sprintf("no agent named %q. Available: %s", name, strings.Join(agentNamesOf(agents), ", ")), false
		}
		if name == agentName {
			// The alternatives, with the refused name taken out of them.
			// Offering "boy, girl" to somebody who was just told boy will
			// not do reads as a message nobody checked.
			return fmt.Sprintf("%q is already this conversation's agent, so it would be reviewing itself. "+
				"Name a different one: %s", name, strings.Join(otherAgents(agents, agentName), ", ")), false
		}
		if seen[name] {
			return fmt.Sprintf("%q is named twice. One model asked the same question twice is one opinion, "+
				"not two", name), false
		}
		seen[name] = true
	}
	return "", true
}

// parseDebateCommand splits "<reviewer>[,<reviewer>] [rounds] <task>".
//
// The rounds are optional and positional, which is one real ambiguity:
// "/debate girl 10페이지 문서를 써라" opens with a token that starts with
// digits and is not a count. So a token is the round count only if it is
// digits and nothing else — anything with a letter attached is the first
// word of the task.
func parseDebateCommand(arg string) (reviewers []string, rounds int, task string, err error) {
	head, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)
	rounds = debateDefaultRounds

	for _, name := range strings.Split(head, ",") {
		if name = strings.TrimSpace(name); name != "" {
			reviewers = append(reviewers, name)
		}
	}
	if len(reviewers) == 0 {
		return nil, 0, "", fmt.Errorf("name a reviewer: /debate <reviewer> [rounds] <what to do>")
	}
	if len(reviewers) > debateMaxReviewers {
		return nil, 0, "", fmt.Errorf("%d reviewers is more than the %d this allows: each one is a model turn "+
			"in every round", len(reviewers), debateMaxReviewers)
	}

	if n, tail, _ := strings.Cut(rest, " "); allDigits(n) {
		count, convErr := strconv.Atoi(n)
		if convErr != nil || count < 1 || count > debateMaxRounds {
			return nil, 0, "", fmt.Errorf("%s rounds is not a number between 1 and %d", n, debateMaxRounds)
		}
		rounds, rest = count, strings.TrimSpace(tail)
	}
	if rest == "" {
		return nil, 0, "", fmt.Errorf("say what to do: /debate %s %d <what to do>", strings.Join(reviewers, ","), rounds)
	}
	return reviewers, rounds, rest, nil
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
