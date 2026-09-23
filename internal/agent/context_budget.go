package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"localcode/internal/config"
	"localcode/internal/modelinfo"
	"localcode/internal/provider"
)

// This file is about one failure, which looks like this:
//
//	Error: openai-compat endpoint returned 400: This model's maximum
//	context length is 131072 tokens. However, you requested 64000 output
//	tokens and your prompt contains at least 67073 input tokens, for a
//	total of at least 131073 tokens.
//
// Note the arithmetic. 67073 input tokens is 51% of the window, and the
// context meter said 53% — nowhere near the threshold that triggers
// auto-compaction. Nothing was going to save this session, because the
// thing that overflowed was not the history: it was the history *plus the
// output the request reserved room for*. A max_tokens of 64000 against a
// 131072-token window means every turn is refused from 67k of history
// onwards, and the meter reads half full while it happens.
//
// So there are two jobs here. Ask for an amount of output that fits
// (clampMaxTokens), and when a request is refused anyway — a wrong window
// size, a provider counting differently — recover instead of ending the
// turn (isContextOverflow, and the retry in sendWithModelText).

// contextHeadroom is held back from the window when sizing a request.
//
// Every token count on this side is an estimate: the tokenizer is the
// server's, tools and the system prompt are counted differently by
// different providers, and a chat template adds framing of its own. The
// margin is what keeps a small underestimate from turning into a refused
// request, at the price of a slightly shorter reply.
const contextHeadroom = 2048

// minOutputTokens is the floor clampMaxTokens will not go below.
//
// A reply capped at a few dozen tokens is not a reply, and a session that
// has genuinely no room left should be compacted rather than answered in
// fragments — so the floor is deliberately high enough that hitting it
// means "compact", not "carry on".
//
// It is a floor on shrinking, never a reason to grow: a profile that asks
// for less than this gets what it asked for.
const minOutputTokens = 1024

// contextWindow is the model's total input+output budget: what the config
// says, or a guess from the model name.
//
// Prefer (*Loop).contextWindow, which asks the server first where there is
// one to ask. This is the answer with no server involved — used where
// there is no provider to hand, and as the fallback when the server does
// not say.
func contextWindow(profile config.Profile) int {
	if profile.ContextWindow > 0 {
		return profile.ContextWindow
	}
	return modelinfo.MaxContextTokens(profile.Model)
}

// probeTimeout bounds the question asked of the server. The answer is a
// nicety — everything works without it — so a slow or hanging server costs
// a fraction of a turn's setup, not the turn.
const probeTimeout = 3 * time.Second

// contextWindow resolves the window for one request, asking the server
// when the answer is not already known.
//
// The order is deliberate:
//
//  1. What the config says. An explicit context_window is someone stating
//     a fact about their setup, and nothing discovered at runtime should
//     quietly overrule it.
//  2. What the server says, via ProbeContextWindow. For a local server
//     this is the only source that can be right: it serves whatever was
//     loaded, under whatever name, and — the part the name cannot express
//     — it is nearly always started with a smaller window than the model
//     supports, because the window is what costs VRAM.
//  3. A guess from the model name, which is what this used to do alone.
//
// Asked once per provider and model, then remembered — including when the
// answer was "I do not say", so a server with no answer is not asked again
// every turn.
func (l *Loop) contextWindow(ctx context.Context, profile config.Profile) int {
	n, _ := l.resolveContextWindow(ctx, profile)
	return n
}

// windowSource is where a context window figure came from, which is the
// half of the answer nobody could see.
//
// The number on its own is not enough to trust. A server that reports
// nothing and a server that reports its real window can produce the same
// session, and the only difference is whether 128000 was measured or made
// up — so a person with a reply cut short at an absurd length asked
// whether localcode had actually asked the server at all, and nothing it
// printed could say.
type windowSource string

const (
	windowFromConfig windowSource = "set by context_window in config.json"
	windowFromServer windowSource = "reported by the server"
	windowGuessed    windowSource = "guessed from the model name, because the server did not report one"
)

// resolveContextWindow is contextWindow with its source.
func (l *Loop) resolveContextWindow(ctx context.Context, profile config.Profile) (int, windowSource) {
	if profile.ContextWindow > 0 {
		return profile.ContextWindow, windowFromConfig
	}
	guess := modelinfo.MaxContextTokens(profile.Model)
	if l.ProbeContextWindow == nil {
		return guess, windowGuessed
	}

	key := profile.Provider + "\x00" + profile.Model
	l.mu.Lock()
	cached, seen := l.probedWindows[key]
	l.mu.Unlock()
	if seen {
		if cached > 0 {
			return cached, windowFromServer
		}
		return guess, windowGuessed
	}

	// Bounded, and still cancelled with the turn: a probe must not outlive
	// an Esc, and must not inherit a deadline meant for a streaming reply.
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	n, found := l.ProbeContextWindow(pctx, profile.Provider, profile.Model)
	cancel()
	if !found {
		n = 0
	}

	l.mu.Lock()
	if l.probedWindows == nil {
		l.probedWindows = map[string]int{}
	}
	l.probedWindows[key] = n
	l.mu.Unlock()

	if n > 0 {
		return n, windowFromServer
	}
	return guess, windowGuessed
}

// clampMaxTokens returns how much output to ask for so that the request as
// a whole fits inside the window.
//
// Returns want unchanged when it already fits, or when there is no usable
// window figure — this must never be the reason a request gets *smaller*
// than what was configured for no reason. Nor larger: the floor used to
// be returned as it was, so a profile asking for 512 sent 1024 whenever
// the window was nearly full.
func clampMaxTokens(want, window, inputTokens int) int {
	if window <= 0 || want <= 0 {
		return want
	}
	room := window - inputTokens - contextHeadroom
	if room >= want {
		return want
	}
	if room < minOutputTokens {
		return min(want, minOutputTokens)
	}
	return room
}

// estimateTokens approximates how many tokens a request's input will cost.
//
// Four characters per token is the usual rule of thumb for English and is
// generous for code; it is wrong in both directions for CJK, where a
// character is often a token on its own. It does not need to be right — it
// needs to be close enough to size a reply against, and it is only
// consulted when the provider has not yet reported a real number for this
// session (see Loop.inputEstimate).
func estimateTokens(system string, msgs []provider.Message) int {
	n := len(system)
	for _, m := range msgs {
		for _, b := range m.Content {
			n += len(b.Text) + len(b.ToolInput) + len(b.ToolResultContent)
			// Per-block framing: role, type, ids. Small, but there can be
			// thousands of blocks in a long session.
			n += 16
		}
	}
	return n / 4
}

// inputEstimate is how many input tokens the next request will cost.
//
// The provider's own count for the messages it was given, plus this
// side's estimate of everything appended since.
//
// Each half is there because the other cannot do its job. The count
// comes from the same tokenizer that will refuse the next request, so
// for the messages it covers nothing here beats it. But it only ever
// describes those messages. Anything appended afterwards is invisible to
// it, and that is not a rounding error: a tool result is capped at a
// quarter of the window, so one of them can outweigh the whole
// conversation the count was taken over, and a turn that calls several
// tools would size every request after the first against a number that
// predates them.
//
// Counting characters sees all of it and is crude: four characters to a
// token is about right for English and several times over for Korean or
// Japanese, where it reads as a floor. So it is applied to the part
// nothing else can see, and the error it carries scales with that part
// rather than with the whole conversation. Taking the larger of the two
// instead threw the exact count away whenever the ratio happened to
// overshoot, and shortened the reply for no reason.
//
// Telling the two apart needs the estimate of the messages the count
// covered, recorded when the count arrived: see sessionUsage.Measured.
// A history that shrinks would make that difference negative, and every
// place that replaces a history clears the count instead, so the
// subtraction is floored at zero rather than trusted to stay positive.
//
// The count here is the whole prompt the provider read, cached prefix
// included: see sessionUsage.promptTokens for why that is not the field
// the provider calls input_tokens.
//
// A count with no measurement behind it comes from a log written before
// that was recorded, which every session restored from disk had until
// it takes its next turn. There is no way to know what it covered, so
// adding the conversation to it would count the part they share twice
// and clamp the reply to the floor. Those fall back to the larger of
// the count and the conversation: never under the count, and never
// double.
func (l *Loop) inputEstimate(sessionID, system string, msgs []provider.Message) int {
	now := estimateTokens(system, msgs)
	u, ok := l.getUsage(sessionID)
	switch {
	case !ok || u.promptTokens() <= 0:
		return now
	case u.Measured <= 0:
		return max(u.promptTokens()+u.OutputTokens, now)
	}
	return u.promptTokens() + max(0, now-u.Measured)
}

// overflowPhrases are how the providers say "this did not fit".
//
// There is no status code or error type for it — Anthropic, Bedrock and
// every openai-compatible server phrase it differently, and the local
// servers wrap it in whatever their proxy says. Matching on text is
// unpleasant and it is what there is; a phrase that stops matching costs a
// missed recovery, not a wrong one, because every one of these is checked
// against an error that has already failed.
var overflowPhrases = []string{
	"context length",
	"context window",
	"contextwindowexceeded",
	"maximum context",
	"prompt is too long",
	"too many tokens",
	"exceed context limit",
	"reduce the length of the",
	"input length and `max_tokens`",
}

// isContextOverflow reports whether err is the provider refusing a request
// for being too big — as opposed to any of the other things a 400 can be.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, p := range overflowPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// errNoRoom is returned when a history cannot be trimmed to fit at all.
var errNoRoom = errors.New("the conversation does not fit in this model's context window even after trimming")

// fitHistory drops whole messages from the front of a history until an
// estimate of what is left fits in budget, and reports how many it
// dropped.
//
// Used only by compaction, and that is the point: compaction is what
// rescues a session that has run out of room, and it worked by sending the
// entire history — the very thing that did not fit — to be summarized. So
// the one operation that could save an overflowing session was the one
// operation guaranteed to be refused by it.
//
// From the front, because the recent end is what a summary most needs to
// carry forward, and because anything old enough to be dropped here has
// usually been through a previous compaction already. The caller says so
// in the summarization prompt rather than letting the model believe the
// conversation began where the trim starts.
//
// A trailing tool_result whose tool_use has been dropped is not left
// behind: providers reject that pairing outright, and a rescue path that
// produces an invalid request has rescued nothing.
func fitHistory(system string, msgs []provider.Message, budget int) ([]provider.Message, int, error) {
	if budget <= 0 {
		return msgs, 0, errNoRoom
	}
	start := 0
	for start < len(msgs) {
		if estimateTokens(system, msgs[start:]) <= budget {
			break
		}
		start++
	}
	if start >= len(msgs) {
		return nil, 0, errNoRoom
	}
	// Never open on orphaned tool results: their tool_use blocks are in a
	// message that has just been dropped.
	for start < len(msgs) && startsWithToolResult(msgs[start]) {
		start++
	}
	if start >= len(msgs) {
		return nil, 0, errNoRoom
	}
	return msgs[start:], start, nil
}

// charsPerToken is the ratio estimateTokens works in, named here because
// the truncation below has to convert a token budget back into bytes.
const charsPerToken = 4

// toolResultWindowShare is the largest share of the context window one
// tool result is allowed to occupy: a quarter.
//
// A tool result is the one thing in a conversation whose size nobody
// chose. read_file on a 5MB file put 5MB into the history, and bash
// returns whatever the command printed — so a single `cat` of a log could
// exceed the whole window by itself, in one message, which no amount of
// summarizing or dropping older messages can fix. That was the failure
// with no way out: the history could not be made to fit because the thing
// that did not fit was one message inside it.
//
// A quarter leaves room for the conversation the result is supposed to be
// part of. Above that, the result is not context, it is the whole context.
const toolResultWindowShare = 4

// truncateMiddle cuts text down to maxBytes, keeping the start and the end
// and saying in the middle what it removed.
//
// Both ends, because which one matters depends on the tool: the head of a
// file is its imports and structure, and the tail of a command is its
// error and exit status. Keeping only one end reliably discards the point
// of about half of all calls.
//
// note is addressed to the model, since the model is who has to decide
// what to do about it.
func truncateMiddle(text string, maxBytes int, note string) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	marker := fmt.Sprintf("\n\n... [%d bytes omitted — %s] ...\n\n", len(text)-maxBytes, note)
	if len(marker) >= maxBytes {
		// Pathological budget: no room for both ends and the explanation.
		// The explanation wins, because a silent truncation is worse than
		// a short one.
		return marker
	}
	room := maxBytes - len(marker)
	head := room * 3 / 5
	tail := room - head
	return text[:head] + marker + text[len(text)-tail:]
}

// capToolResult limits one tool result to a share of the window.
//
// Applied to what is stored as well as what is sent, so the transcript and
// the model see the same thing — and so the event log, the SSE stream to
// every attached client, and the replay after a restart are not each
// carrying a copy of a file that nothing was ever going to be able to use.
func capToolResult(content string, window int) string {
	if window <= 0 {
		window = modelinfo.DefaultMaxContextTokens
	}
	maxBytes := (window / toolResultWindowShare) * charsPerToken
	return truncateMiddle(content, maxBytes,
		"this result was too large for the model's context window; read the file in ranges, or narrow the command, to see the rest")
}

// shrinkBudget returns the next, smaller budget to try after a request has
// been refused for being too long.
//
// Two thirds of the current budget, and never more than two thirds of what
// the history actually measures — the second clause is what makes this
// terminate. A budget derived from the window alone can sit far above the
// real size of the conversation, so shrinking it does nothing for several
// rounds while every round costs a refused request. Taking the measured
// size into account means each attempt removes about a third of what is
// really there, whatever the window says.
//
// No floor: a floor is what turns "cut something" into "already fits,
// nothing to do", which is the one answer this must never give — a
// request the server has just refused is not one to leave alone. The
// callers bound the number of attempts instead.
//
// Both clauses matter because the estimate is unreliable in the direction
// that hurts: four characters per token is about right for English and
// about four times too generous for Korean, so the server can refuse a
// history this side measures as comfortably small.
func shrinkBudget(current int, system string, msgs []provider.Message) int {
	next := current * 2 / 3
	if est := estimateTokens(system, msgs); est > 0 && est*2/3 < next {
		next = est * 2 / 3
	}
	return next
}

// forceFit makes a history fit budget, whatever it takes, and reports
// whether it had to change anything.
//
// This is the last line of defence, and unlike fitHistory it cannot fail:
// it drops whole messages from the front first, and when what remains
// still does not fit — one enormous message, the case dropping cannot
// solve — it truncates the text inside the messages that are left.
//
// Something being cut is not in question by the time this runs; the
// request has already been refused. The choice is between cutting and a
// session that answers nothing ever again, and a session that keeps
// working with a gap in it, clearly marked, is worth more than a correct
// refusal.
func forceFit(system string, msgs []provider.Message, budget int) ([]provider.Message, bool) {
	if budget <= 0 || len(msgs) == 0 {
		return msgs, false
	}
	if estimateTokens(system, msgs) <= budget {
		return msgs, false
	}

	// Drop from the front while there is more than one message left. The
	// last message is kept whatever its size — it is the request being
	// answered — and truncated below if it is what overflowed.
	start := 0
	for start < len(msgs)-1 && estimateTokens(system, msgs[start:]) > budget {
		start++
	}
	for start < len(msgs)-1 && startsWithToolResult(msgs[start]) {
		start++
	}
	kept := append([]provider.Message(nil), msgs[start:]...)

	if estimateTokens(system, kept) <= budget {
		return kept, true
	}

	// Still too big: the content itself is. Truncate the largest block in
	// the largest message and repeat, so a conversation of ordinary
	// messages plus one monster loses only the monster.
	for range 64 {
		if estimateTokens(system, kept) <= budget {
			break
		}
		mi, bi, size := -1, -1, 0
		for i := range kept {
			for j, b := range kept[i].Content {
				if n := len(b.Text) + len(b.ToolResultContent); n > size {
					mi, bi, size = i, j, n
				}
			}
		}
		if mi < 0 || size == 0 {
			break
		}
		// Copy before writing: history blocks are shared with the session's
		// stored history, and this must not edit it in place.
		blocks := append([]provider.Block(nil), kept[mi].Content...)
		target := size / 2
		if target < 512 {
			target = 512
		}
		const note = "cut to fit the model's context window"
		blocks[bi].Text = truncateMiddle(blocks[bi].Text, target, note)
		blocks[bi].ToolResultContent = truncateMiddle(blocks[bi].ToolResultContent, target, note)
		kept[mi].Content = blocks
	}
	return kept, true
}

func startsWithToolResult(m provider.Message) bool {
	for _, b := range m.Content {
		if b.Type == provider.BlockToolResult {
			return true
		}
	}
	return false
}

// requestSizing is what one request was sized against.
//
// Kept, rather than worked out again afterwards, so that what is said about
// a reply describes the request that produced it. The reply itself changes
// the answer: its usage is recorded before anything reads it, so the input
// a notice recomputes after the reply already includes the reply. A request
// that went out with the profile's full reservation and filled it would
// then re-read as one the window had shrunk — and the notice would say
// raising max_tokens cannot help, about the one case where it is exactly
// the fix.
type requestSizing struct {
	wanted int // the profile's max_tokens, or the default
	sent   int // what the request actually asked for
	input  int // the input estimate the request was sized against
	window int
	source windowSource
}

// sizeRequest works out how much output one request may ask for, and
// keeps the figures it used.
func (l *Loop) sizeRequest(ctx context.Context, sessionID string, run modelRun, messages []provider.Message) requestSizing {
	window, source := l.resolveContextWindow(ctx, run.profile)
	input := l.inputEstimate(sessionID, run.system, messages)
	return requestSizing{
		wanted: run.maxTokens,
		sent:   clampMaxTokens(run.maxTokens, window, input),
		input:  input,
		window: window,
		source: source,
	}
}

// cutOffNotice says why a reply stopped at its length cap, and what would
// let it run longer.
//
// Two different limits end a reply this way and they want opposite
// advice. One is the profile's own max_tokens, and raising it is the fix.
// The other is the context window: clampMaxTokens shrinks the request to
// fit what is left, down to a floor of minOutputTokens, and a reply cut
// off there was cut off by a full window — raising max_tokens changes
// nothing, because the next request is shrunk the same way.
//
// This used to give the first answer to both. It printed the shrunk
// figure as though it were the profile's limit, so a reply stopped at
// 1024 read as "your max_tokens is 1024" on a profile that set none, and
// told the person to raise a number that was not the one in the way. It
// also named the profile by its model id, which is not a key anyone can
// find in their config.json.
//
// Which limit it was is read off the request that was sent, not worked
// out again — see requestSizing for why that is not the same thing.
//
// And when the window is the cause, it says where the window figure came
// from, because a figure guessed from a model name is the likeliest
// reason a window looks full when it is not.
// tryALargerWindow is the way out of every branch that blames the
// window, for a window figure nobody stated.
//
// A figure guessed from a model name is the likeliest reason a window
// looks full when it is not, so a branch that blames the window and
// does not offer this is telling somebody to compact a conversation
// that fits. It says nothing when the figure came from config.json,
// where the person has already answered the question.
func tryALargerWindow(s requestSizing, name string) string {
	if s.source == windowFromConfig {
		return ""
	}
	return fmt.Sprintf("; if the model's real window is larger than %d, set context_window on the %q profile in config.json", s.window, name)
}

func cutOffNotice(s requestSizing, profileName, model string) string {
	name := profileName
	if name == "" {
		name = model
	}
	raiseIt := "raise max_tokens on that profile in config.json for longer answers"
	hit := fmt.Sprintf("the reply hit the %q profile's max_tokens limit of %d and was cut off", name, s.sent)
	// What each move would send, worked out by the function that sizes
	// the next request, so the advice cannot promise what the request
	// will not do. With no window figure clampMaxTokens has no opinion,
	// so raising is all that is left, which is what the arithmetic says.
	//
	// Raising is priced against the input the NEXT request will carry,
	// which is this one's plus the reply that has just been added to it:
	// inputEstimate returns the provider's own input+output once a turn
	// has reported usage. Priced at this request's input instead, the
	// notice told a profile of 3000 on an 8192 window to raise
	// max_tokens, when the 3000-token reply joining the history left
	// room for 2644 and raising made the next reply shorter than the one
	// that had just been cut off.
	//
	// It is still a floor, not a promise: whatever the person types next
	// is on top of it and cannot be known here. The floor is the honest
	// number, because it is the smallest the next request can be.
	//
	// Compacting is modelled as an empty conversation, the most it could
	// ever free, and it takes the reply with it: a move that does not
	// help even then does not help.
	raised := clampMaxTokens(math.MaxInt, s.window, s.input+s.sent)
	both := clampMaxTokens(math.MaxInt, s.window, 0)
	inUse := fmt.Sprintf("about %d of %d tokens were in use", s.input, s.window)

	switch {
	case both <= s.sent:
		// A window too small to give more to any request: most of it is
		// the margin held back for estimation error. Only a larger
		// window helps, and a small figure is as likely to be wrong as
		// the model's.
		msg := fmt.Sprintf(
			"the reply was cut off at %d tokens, and a %d-token context window cannot give a reply more: after the %d tokens held back as a margin, even an empty conversation leaves room for %d. The window figure was %s",
			s.sent, s.window, contextHeadroom, both, s.source)
		if s.source == windowFromConfig {
			return msg + fmt.Sprintf("; if the model's real window is larger, raise context_window on the %q profile in config.json", name)
		}
		return msg + fmt.Sprintf("; if the model's real window is larger, set context_window on the %q profile in config.json", name)

	case s.sent < s.wanted:
		// The window shrank it, and a smaller conversation lets it grow.
		return fmt.Sprintf(
			"the reply was cut off at %d tokens because the context window was nearly full: %s, and the window figure was %s. Raising max_tokens will not help — the next reply is shrunk the same way. /compact makes room now",
			s.sent, inUse, s.source) + tryALargerWindow(s, name)

	case raised <= s.sent:
		// The profile's figure was sent, and the window would give a
		// larger one no more. Neither move works alone: a raised
		// max_tokens is shrunk back, and /compact makes room the
		// profile's figure then caps.
		return hit + fmt.Sprintf(
			", and the context window had no room for more: %s, and the window figure was %s. A longer answer needs both /compact and a higher max_tokens on that profile in config.json",
			inUse, s.source) + tryALargerWindow(s, name)

	case raised <= minOutputTokens:
		// Raising helps, as far as the floor. Past that only a smaller
		// conversation helps, if even an empty one would give more.
		msg := hit + " — " + raiseIt + fmt.Sprintf(
			"; the context window is nearly full as well (%s, and the window figure was %s), so a raised max_tokens gets at most %d", inUse, s.source, raised)
		if both > raised {
			msg += " until /compact makes room"
		}
		return msg + tryALargerWindow(s, name)
	}
	return hit + " — " + raiseIt
}
