package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// Asking the person a question the work cannot answer.
//
// The alternative is what happens today: the model reaches a fork it has
// no way to settle, picks one, and says so in the final answer. Half the
// time it picks the branch the person did not want, and the cost is the
// whole turn plus the correction. The other half it stops and asks in
// prose, which is fine except that the turn has ended, the tools it had
// open are gone, and picking up means re-establishing all of it.
//
// So the question happens inside the turn. The model calls a tool, the
// turn blocks, the person answers with a keystroke, and the work carries
// on with the answer in hand.
//
// Two limits, both deliberate. The question is asked at most once per
// turn, because a model given a way to ask will ask about things it
// could have looked up, and a session that stops three times is worse
// than one that guessed. And it is only offered when somebody is there:
// a scheduled run, a one-shot in a pipe, and a delegated sub-agent all
// have no one at the keyboard, and a tool that can only block is worse
// than no tool.
//
// Behind Smart Agent with the rest of the bundle.
const askUserToolName = "ask_user"

// askUserMaxOptions is the ceiling Codex's own version uses, and it is
// the right one for a different reason: a question with seven answers is
// a question the model has not finished thinking about.
const askUserMaxOptions = 4

// InputBroker carries a question to whoever is watching a session and
// blocks until they answer.
//
// Its own type rather than a mode on PermissionBroker. The two look alike
// and mean different things: a permission answer is a policy decision
// with a scope and a persisted rule, and this is one reply to one
// question. Sharing the vocabulary would put "allow for the session" on a
// question about which database to use.
type InputBroker struct {
	store *session.Store

	mu      sync.Mutex
	counter int
	pending map[string]chan string
}

func NewInputBroker(store *session.Store) *InputBroker {
	return &InputBroker{store: store, pending: map[string]chan string{}}
}

// Ask puts the question in the session and waits. It returns the answer,
// or "" and false when the turn ends before anybody answers.
func (b *InputBroker) Ask(ctx context.Context, sessionID, question string, options []string) (string, bool) {
	if b == nil || b.store == nil {
		return "", false
	}
	b.mu.Lock()
	b.counter++
	id := fmt.Sprintf("q%d", b.counter)
	ch := make(chan string, 1)
	b.pending[id] = ch
	b.mu.Unlock()

	opts := make([]any, 0, len(options))
	for _, o := range options {
		opts = append(opts, o)
	}
	b.store.Append(sessionID, events.TypeInputRequest, map[string]any{
		"id": id, "question": question, "options": opts,
	})

	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	select {
	case answer := <-ch:
		b.store.Append(sessionID, events.TypeInputResolved, map[string]any{"id": id, "answer": answer})
		return answer, true
	case <-ctx.Done():
		// The same event an answer produces, so a question does not sit
		// on screen for a turn that has already ended.
		b.store.Append(sessionID, events.TypeInputResolved, map[string]any{"id": id, "cancelled": true})
		return "", false
	}
}

// Resolve delivers an answer. It reports whether a question was waiting
// for one, so a client answering a stale id is told rather than ignored.
func (b *InputBroker) Resolve(id, answer string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	ch <- answer
	return true
}

// AskUserTool lets the model put one question to the person mid-turn.
type AskUserTool struct {
	loop *Loop
}

func NewAskUserTool(loop *Loop) AskUserTool { return AskUserTool{loop: loop} }

func (AskUserTool) Name() string { return askUserToolName }

func (t AskUserTool) Description() string { return t.DescriptionFor(context.Background()) }

func (AskUserTool) DescriptionFor(context.Context) string {
	return "Ask the user one question and wait for the answer, without ending your turn. " +
		"Use it only for a decision the work cannot settle: which of two designs they want, which " +
		"account to deploy to, whether a breaking change is acceptable. Do not use it for anything " +
		"you can find out by reading the code, running a command, or following the project's " +
		"conventions.\n" +
		"Give 2 to 4 short options, most recommended first. The user can always answer in their own " +
		"words instead, so do not add an \"other\" option yourself.\n" +
		"You may ask once per turn. Choose the question that unblocks the most work."
}

func (AskUserTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"question":{"type":"string","description":"One sentence, ending in a question mark."},` +
		`"options":{"type":"array","description":"2 to 4 short answers, most recommended first.",` +
		`"items":{"type":"string"}}},"required":["question","options"],"additionalProperties":false}`)
}

func (t AskUserTool) InputSchemaFor(context.Context) json.RawMessage { return t.InputSchema() }

// RequiresPermission is false: the question is the prompt. Asking to
// approve a question before showing it would put two prompts on screen
// for one decision.
func (AskUserTool) RequiresPermission(json.RawMessage) bool { return false }

func (t AskUserTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var args struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tools.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}
	args.Question = strings.TrimSpace(args.Question)
	if args.Question == "" {
		return tools.Result{Content: "the question is empty", IsError: true}
	}
	var opts []string
	for _, o := range args.Options {
		if o = strings.TrimSpace(o); o != "" {
			opts = append(opts, o)
		}
	}
	if len(opts) < 2 {
		return tools.Result{
			Content: "give at least two options; a question with one answer is a statement",
			IsError: true,
		}
	}
	if len(opts) > askUserMaxOptions {
		return tools.Result{Content: fmt.Sprintf(
			"%d options is too many; give at most %d. A question with more answers than that is one "+
				"you have not finished thinking about", len(opts), askUserMaxOptions), IsError: true}
	}

	sessionID, ok := SessionIDFromContext(ctx)
	if !ok || t.loop == nil || t.loop.Input == nil {
		return tools.Result{Content: "there is no one to ask in this run", IsError: true}
	}
	if !t.loop.claimAsk(sessionID) {
		return tools.Result{Content: "you have already asked once this turn; decide with what you have, " +
			"or finish and put the question in your answer", IsError: true}
	}

	answer, answered := t.loop.Input.Ask(ctx, sessionID, args.Question, opts)
	if !answered {
		return tools.Result{Content: "the question was not answered before the turn ended", IsError: true}
	}
	return tools.Result{Content: "the user answered: " + answer}
}

// One question per turn.
//
// The claim is taken when the tool is called and released when the turn
// ends, so "once" means once per turn rather than once per session: a
// long conversation may reach a real fork more than once, and a session
// that could ask only ever again would push the model back to guessing.
//
// A model given an unlimited way to ask uses it. Not maliciously: asking
// is cheap for it and expensive for the person, and there is always
// another detail that could be confirmed. So the budget is one, and the
// refusal says what to do instead.
func (l *Loop) claimAsk(sessionID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.asked == nil {
		l.asked = map[string]bool{}
	}
	if l.asked[sessionID] {
		return false
	}
	l.asked[sessionID] = true
	return true
}

// releaseAsk gives the session its question back, at the end of a turn.
func (l *Loop) releaseAsk(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.asked, sessionID)
}
