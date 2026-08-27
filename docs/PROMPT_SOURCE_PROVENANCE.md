# Prompt Source Provenance

Where localcode's prompt architecture was researched, what was learned, and what was written independently.

The short version, because it is the part that matters: **no third-party prompt text is incorporated into localcode.** Public collections were read to identify what a coding-agent harness has to say to a model and under what conditions. Every string localcode ships was written for localcode.

## Sources

| Source | Version or retrieval | What was learned from it | Text incorporated |
|---|---|---|---|
| Anthropic public documentation (Claude API, prompt caching, tool use, extended thinking) | Retrieved 2026-08-26 | The mechanics that constrain assembly: how cache breakpoints work and how many are permitted, how native tool definitions are carried, how system content may be structured as blocks. Preferred over unofficial material wherever it documents the same behavior. | None. The behavior is implemented against the documented API shapes. |
| An external reviewer's prompt-architecture supplement, held outside this repository as audit material | 2026-08-26 | The conformance requirements this work implements: the asset metadata model, the activation and manifest contracts, the trust axis, the placement categories, the 104-item checklist. | None. The supplement is a specification; the code and prompts are localcode's. |
| The same reviewer's implementation guidance, held outside this repository | 2026-08-26 | The required prompt surface enumeration, the disposition vocabulary, the documentation artifacts, and the copyright boundary these notes exist to record. | None. |
| Piebald-AI Claude Code prompt collection, `https://github.com/Piebald-AI/claude-code-system-prompts` | Not vendored; consulted as a research claim about categories only | That a production harness's surface is far wider than a base prompt: conditional system fragments, per-tool descriptions, runtime reminders, per-agent prompts, slash-command prompts, and utility-call prompts are all distinct categories with distinct lifetimes. This is what drove the runtime-entry half of `docs/PROMPT_ASSET_INVENTORY.md`. | **None.** No file from that collection is in this repository, and no extracted string was copied, adapted, or paraphrased into localcode. |

## What "learned from" means here

Categories and activation conditions, not wording.

A worked example: the collection's existence of per-tool description strings is a fact about what a harness must account for. It told us that tool descriptions belong in the prompt inventory, that they steer the model as instructions do, and that an MCP server's description therefore cannot carry the same authority as a built-in one. What localcode does with that is its own: `toolEntries` in `internal/agent/prompt_surface.go` derives entries from the tool definitions localcode already registers in `internal/tools` and `internal/mcp`, hashing what the model is actually told. No description text came from anywhere but localcode's own tools.

The same applies to every other category. The auto-memory split, the trust classes, the compaction provenance header, the orchestration policy, and the per-model notes are all localcode's text, written for localcode's behavior.

## Independent authorship

Every prompt string shipped in the binary is authored in this repository and is visible in it:

* `internal/smart/prompt.go`: the orchestration policy and the trust boundary.
* `internal/agent/quirks.go`: the per-model family notes.
* `internal/agent/compact.go`: the summarizing instruction and the summary provenance header.
* `internal/memory/memory.go`: the auto-memory policy and the recalled-notes boundary.
* `internal/tools/*.go`: every built-in tool's own description.

Where a behavior existed in localcode before this work, it was reused rather than reimplemented, which is both the guidance's instruction and the reason the asset count is what it is.

## Licensing caution

localcode is MIT licensed. That covers localcode's own text and code and nothing else.

* A third-party repository's MIT license covers material its publisher is entitled to license. It is not evidence that extracted strings from another vendor's product can be relicensed, and this project does not treat it as such.
* No Anthropic prompt text is redistributed here, verbatim or mechanically paraphrased.
* Where any doubt existed about whether a formulation was too close to researched material, the behavior was implemented and the text written from scratch instead. That is the standing rule for future work in this area.

No attribution notice is required, because no third-party text is incorporated. This document exists to record that fact rather than to satisfy one.
