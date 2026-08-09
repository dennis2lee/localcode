package agent

import "localcode/internal/provider"

// sendableHistory returns history in a shape every provider will accept:
// no empty messages, and no two messages of the same role in a row.
//
// This is a guard, not a design. History *should* already alternate, and
// the places that broke it are fixed at their source — but the cost of
// getting it wrong is out of all proportion to the mistake, so the shape
// is enforced once, here, on the way out.
//
// What made it worth enforcing: Bedrock's Converse API rejects both
// shapes outright, and a session's history persists. So a single
// transient error produced a request that failed, and every retry after
// it failed the same way, and the failure survived a restart — one
// dropped connection and the session was over. Three separate paths
// produced it:
//
//   - A provider error mid-turn returned before the assistant reply was
//     appended, leaving history ending in a user message. The user's
//     retry appended a second one.
//   - Cancelling before the first token (Esc during prefill) produced an
//     assistant message with no content blocks at all.
//   - Compaction replaces history with a single user-role summary, and
//     the next turn appends the user's prompt right after it — so every
//     successful compaction was immediately followed by a broken turn.
//
// Merging rather than dropping, because both messages are real context
// the model should see: two user turns become one user turn carrying
// both sets of blocks, in order.
func sendableHistory(msgs []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		// An empty message carries nothing and is rejected by name by
		// Anthropic and Bedrock alike.
		if len(m.Content) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			// Copy the block slice before appending: out[n-1].Content
			// still aliases the caller's array, and appending to it in
			// place would write into the history this was called on.
			merged := make([]provider.Block, 0, len(out[n-1].Content)+len(m.Content))
			merged = append(merged, out[n-1].Content...)
			merged = append(merged, m.Content...)
			out[n-1].Content = merged
			continue
		}
		out = append(out, m)
	}
	return out
}
