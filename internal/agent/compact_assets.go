package agent

import (
	"fmt"
	"sort"
	"strings"

	"localcode/internal/provider"
)

// What compaction takes away besides messages.
//
// A skill body, a custom command's expansion, a spliced file and a
// person's mid-turn instruction all arrive as message content rather
// than as system text. That is the right placement — they belong to a
// moment in the conversation, not to the session — and it has a
// consequence nobody stated: compaction replaces the history with a
// summary, so all of them are gone, and what survives is whatever the
// summarizing model happened to think worth mentioning.
//
// Usually that is fine. A file spliced in ten turns ago has done its
// work. It is not fine for the ones still in force: a skill invoked two
// messages ago is a procedure the model is in the middle of following,
// and after compaction it is following a paraphrase of it.
//
// Codex has the same problem and solves it by writing markers into the
// text so a fragment can recognise itself in retained history. localcode
// does not need markers, because its history blocks already carry
// structured provenance (provider.BlockSource) — the id is right there.
// So the answer here is smaller: name them, on the record and to the
// model, and let it re-invoke what it still needs.
//
// Naming rather than re-injecting. Automatically re-attaching a skill
// body after every compaction would grow the thing compaction just shrank,
// and would re-assert an instruction the conversation may have finished
// with. The model knows which it is still working on; it only has to be
// told that they went.

// carriedAssetKinds are the message-placed asset families worth naming
// when they disappear. Each is content some other party authored — a
// skill the user installed, a command they wrote, a file, their own typed
// instruction — as opposed to localcode's own framing and notices, which
// are rebuilt on the next request anyway.
var carriedAssetKinds = []struct {
	prefix string
	label  string
}{
	{"skill.body.", "skill"},
	{"command.", "command"},
	{"argument.", "command argument"},
	{"file.", "spliced file"},
	{"shell.", "spliced command output"},
}

// droppedCarriedAssets names the message-placed assets in history, most
// recent first, with duplicates removed.
//
// Most recent first because that is the order they matter in: the skill
// invoked in the last exchange is the one the model is still inside.
func droppedCarriedAssets(history []provider.Message) []string {
	seen := map[string]bool{}
	var out []string
	for i := len(history) - 1; i >= 0; i-- {
		for _, b := range history[i].Content {
			for _, src := range b.Sources {
				name, ok := carriedAssetName(src.ID)
				if !ok || seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	// The person's own mid-turn instruction has no name of its own, and
	// listing it once at the end reads better than interleaved.
	if hasInjectedUser(history) {
		out = append(out, "an instruction you typed while a turn was running")
	}
	return out
}

func carriedAssetName(id string) (string, bool) {
	for _, k := range carriedAssetKinds {
		if rest := strings.TrimPrefix(id, k.prefix); rest != id && rest != "" {
			return k.label + " " + rest, true
		}
	}
	return "", false
}

func hasInjectedUser(history []provider.Message) bool {
	for _, m := range history {
		for _, b := range m.Content {
			for _, src := range b.Sources {
				if src.ID == "injected.user" {
					return true
				}
			}
		}
	}
	return false
}

// carriedAssetNote is the sentence appended to the summary, or "" when
// the replaced history carried nothing of the kind.
//
// Capped, and sorted after the cap, so a session that invoked forty
// skills does not spend the summary listing them. Six is enough to
// recognise the one being worked on.
func carriedAssetNote(names []string) string {
	if len(names) == 0 {
		return ""
	}
	const cap = 6
	more := 0
	if len(names) > cap {
		more = len(names) - cap
		names = names[:cap]
	}
	shown := append([]string(nil), names...)
	sort.Strings(shown)

	var b strings.Builder
	b.WriteString("\n\n[The summary replaced the messages that carried: ")
	b.WriteString(strings.Join(shown, ", "))
	if more > 0 {
		fmt.Fprintf(&b, ", and %d more", more)
	}
	b.WriteString(". Their full text is no longer in this conversation. " +
		"Load again anything you are still working from rather than working from the summary of it.]")
	return b.String()
}
