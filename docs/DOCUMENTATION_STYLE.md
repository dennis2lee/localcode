# Documentation Style

## Required result

LocalCode documentation must provide the conclusion, action, or constraint before supporting detail. Readers should be able to find a command, decision, or limitation without reading unrelated prose.

## Rules

| Area | Requirement |
|---|---|
| Structure | BLUF at the document and major section level. |
| Tone | Direct engineering language. No promotional or conversational filler. |
| Sentences | One claim per sentence. Explicit subject, condition, and result. |
| Layout | Prefer short noun phrases, `*` bullets, and tables. |
| Prose | Use for sequence, rationale, and constraints that a table cannot express clearly. |
| Terminology | One term per concept. Match names used by the CLI, UI, API, and config. |
| Punctuation | No em dashes. Use a period, colon, comma, or parentheses. |
| Evidence | Separate measured results, documented behavior, inference, and unknowns. |
| Audit records | Preserve findings, dispositions, dates, and evidence. Style changes must not alter conclusions. |
| Language | English, except files explicitly maintained as translations. |

## BLUF pattern

Use this order:

1. Result or required action.
2. Scope and prerequisites.
3. Procedure or supporting evidence.
4. Limitations and recovery steps.

Example:

```text
Bedrock credentials are loaded on the first Bedrock request.

Requirements:
* AWS profile or environment credentials
* Bedrock model access in the configured region

Failure behavior:
* Local-only startup remains available.
* The next Bedrock request retries credential loading.
```

## Content checks

Before publishing:

* Remove repeated claims and duplicated instructions.
* Replace metaphors with the actual component, state, or transition.
* State defaults, activation conditions, and failure behavior.
* Verify command names, flags, config keys, paths, and links against the code.
* Use measured numbers only with a method or source.
* Mark unsupported or unverified behavior explicitly.
* Run `make check`.
