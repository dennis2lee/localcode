# Prompt Asset Inventory

Every string localcode can put in front of a model, with what it is, where it came from, whether it may act as an instruction, and where it sits in the request.

The count follows localcode's own capabilities. It is not derived from any upstream extraction, and it is not a target: several upstream functions map to one asset here where the provenance and trust are the same, and one function becomes two assets where they differ. The auto-memory split is the clearest case of the second, and the reason it exists is in the trust column.

Two kinds of row appear:

* **Registered assets** are declared in `internal/agent/prompt_assets.go` and selected per call against an activation context. They have stable IDs, versions and activation conditions.
* **Runtime entries** describe model-visible text that only exists at request time (a tool result, a hook's injection, the instruction a particular compaction sent). They cannot be pre-declared, so they are derived from the request being sent and recorded on that request's manifest, which recomputes its identity.

Two lifetime columns, not one. **Text lifetime** is how long the text keeps being sent, and **entry lifetime** is how long the manifest describes it. They were one column and the word in it was "one shot", which was wrong for most of this table: a tool result enters the conversation and is re-sent on every request until a compaction replaces it. The entry now lasts exactly as long as the text, because it is derived from the messages rather than carried in a variable, and the two columns exist so that a row cannot claim otherwise again.

The **Researched from** column records what public material the row's classification was informed by, per the implementation guidance. `docs/PROMPT_SOURCE_PROVENANCE.md` holds the full statement, including what was deliberately not used.

Both appear in the assembly manifest with an ID, provenance, trust, placement, hash and token estimate. Neither ever carries a rendered body into the manifest, the trace, or a diagnostic.

## Registered assets

| LocalCode asset ID | Function | Kind | Provenance | Trust | Placement | Lifetime | Activation | Disposition | Code | Test |
|---|---|---|---|---|---|---|---|---|---|---|
| `system.base` | The product's description of itself and how it works | base system | product | system | system | stable | always, unless empty | Existing | `prompt_assets.go` | `TestAssemblyReproducesTheConcatenationItReplaced` |
| `skills.index` | What skills are installed and how to invoke them | skill | skill | user | system | durable reload | any skill installed | Implemented | `prompt_assets.go` | `TestTheInventoryAssemblesInTheGoldenOrder` |
| `memory.policy` | How auto memory works and where notes live | mode instruction | product | system | system | durable reload | auto memory on | Implemented | `prompt_assets.go`, `internal/memory` | `TestGeneratedMemoryCannotBecomeInstruction` |
| `memory.index` | Notes the model wrote in earlier sessions | external content | generated summary | **generated, never instruction** | system | durable reload | auto memory on and notes exist | Implemented | `prompt_assets.go`, `internal/memory` | `TestGeneratedMemoryCannotBecomeInstruction` |
| `project.rules` | `AGENTS.md`, `CLAUDE.md` and their imports | project instruction | workspace | project | system | session | the session's workspace ships rules | Existing | `prompt_assets.go`, `internal/rules` | `TestTheManifestDoesNotCarryTheProjectsRules` |
| `agent.prompt` | The role's own instructions from config | agent prompt | user | user | system | session | the agent defines one | Existing | `prompt_assets.go` | `TestASpecialistGetsTheBoundaryButNotTheOrchestrationPolicy` |
| `smart.orchestration` | How to decompose and delegate | mode instruction | product | system | system | session | Smart Agent on, orchestrator role, somewhere to delegate | Existing | `prompt_assets.go`, `internal/smart` | `TestSmartAgentAssetsAreAbsentAndExplainedWhenOff` |
| `smart.trust_boundary` | Which sources are instructions and which are data | safety policy | product | system | system | session | Smart Agent on, conversational call | Existing | `prompt_assets.go`, `internal/smart` | `TestASpecialistGetsTheBoundaryButNotTheOrchestrationPolicy` |
| `model.quirk` | How to write for this model family | mode instruction | product | system | system | session | a note exists for the family | Existing | `prompt_assets.go`, `quirks.go` | `TestAFallbackToAnotherFamilyProducesADifferentManifest` |
| `utility.compact` | The default summarizing instruction | utility prompt | product | system | utility call | stable | the compaction call only | Implemented | `prompt_assets.go`, `compact.go` | `TestTheCompactionPromptIsAUtilityAssetNotASystemBlock` |

Ten registered assets. The number is what localcode's system surface actually contains: one base prompt, two indexes, one workspace source, one per-agent source, three Smart Agent or per-model modes, and one utility instruction.

## Runtime entries

| Entry ID pattern | Function | Kind | Provenance | Trust | Placement | Text lifetime | Entry lifetime | Activation | Disposition | Researched from | Code | Test |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `tool.builtin.<name>` | A built-in tool's name, description and schema | tool description | product | system | tool definition | turn | turn | the tool is advertised for this call | Implemented | tool-definition category, public docs | `prompt_surface.go` | `TestToolDefinitionsAreInventoriedWithTheirTrust` |
| `tool.mcp.<name>` | An MCP server's tool name, description and schema | tool description | mcp server | **external** | tool definition | turn | turn | the server is connected and the tool advertised | Implemented | MCP deferred-discovery behaviour, public docs | `prompt_surface.go` | `TestToolDefinitionsAreInventoriedWithTheirTrust` |
| `hook.pre_model[#n]` | Text a `pre_model` hook injected for this request | runtime reminder | hook | user | system | one request | one request | the hook returned context | Implemented | hook evaluation category | `turn.go` | `TestRuntimeSurfaceEntriesCarryTheRightAuthority` |
| `skill.body.<name>` | The body of an invoked skill | skill | skill | user | message | conversation | conversation | the skill was invoked, and every request after it until a compaction | Implemented | skill index versus skill body distinction | `prompt_surface.go`, `commands.go` | `TestASkillBodyReachesTheRequestItIsSentIn` |
| `command.<name>` | A custom or built-in command's expansion | utility prompt | user | user | message | conversation | conversation | the command was invoked, and every request after it | Implemented | slash-command prompt category | `prompt_surface.go`, `commands.go` | `TestRuntimeSurfaceEntriesCarryTheRightAuthority` |
| `child.input.<agent>[#id]` | The task a sub-agent was given | agent prompt | **parent agent** | **delegated** | message | conversation | conversation | on the parent, the tool_use block that carries it; on the child, its opening message | Implemented | subagent prompt category | `prompt_surface.go`, `turn.go`, `taskmanager.go` | `TestADelegatedTaskIsNamedInBothRequestsThatCarryIt` |
| `child.result.<agent>[#task]` | What a sub-agent reported back | external content | child result | **external** | message | conversation | conversation | a delegation returned; one entry per child in a collection | Implemented | subagent result reinjection | `prompt_surface.go`, `background.go` | `TestACollectionNamesEveryChildInIt` |
| `result.<tool>#<call>` | A tool's output re-entering the conversation | external content | tool result, or mcp server | **external** | message | conversation | conversation | the tool ran, and every request after it until a compaction | Implemented | tool-result trust boundary | `prompt_surface.go`, `turn.go` | `TestALaterTurnStillNamesTheToolResultItIsSending` |
| `reminder.<kind>` | A carry-on nudge localcode wrote into the conversation | runtime reminder | product | system | message | conversation | conversation | the condition occurred | Implemented | system-reminder category | `prompt_surface.go`, `turn.go` | `TestRuntimeSurfaceEntriesCarryTheRightAuthority` |
| `compact.instruction` | The instruction a compaction attempt actually sent | utility prompt | user | user | utility call | one request | one request | a manual `/compact <text>` override | Implemented | compaction utility prompt | `compact.go` | `TestDifferentCompactionInstructionsGetDifferentManifests` |
| `compact.truncation_note` | The note that early messages did not fit this attempt | runtime reminder | product | system | utility call | one request | one request | the attempt had to drop messages | Implemented | compaction utility prompt | `compact.go` | `TestTheTruncationNoteIsTheProductsNotTheUsers` |
| `compact.carried.<asset>` | The session prompt carried into the summarizing call, block by block | *the carried asset's* | *the carried asset's* | *the carried asset's* | system | one request | one request | the session has a system prompt with per-asset seams | Implemented | compaction input provenance | `compact.go` | `TestCompactionDoesNotLaunderGeneratedMemory` |
| `compact.carried_system` | A carried prompt that arrived already folded, so its sources cannot be told apart | external content | generated summary | **generated, never instruction** | system | one request | one request | a caller had only the folded string | Implemented, and unreached: every production caller now passes blocks with their asset ids, so this is the fail-safe classification for a caller that cannot, exercised only by its test | compaction input provenance | `compact.go` | `TestCompactionDoesNotLaunderGeneratedMemory` |

Thirteen runtime entry patterns. The count is of what non-test code can emit, checked by a test rather than asserted: `TestTheInventoryDescribesEntriesThatExist` fails when a row here has no literal behind it, and `TestEveryEntryConstructorIsCalledFromARealPath` fails when something builds an entry no real path calls, and `TestTheInventoryCitesTestsThatExist` fails when a row names a test that is not in the tree. An earlier count said eleven, arrived at by adding up a description of the surface instead of reading the code; `compact.truncation_note` and the two carried-block forms were the difference.

## Placement is preserved, not flattened

Registering a tool description does not move it into the system prompt. Tool definitions reach every adapter as native tool definitions, and the entry describes them where they are. The same holds for the utility placement: the compaction instruction is in the inventory and never in a conversational system block.

## What is not covered

Stated rather than implied:

* Model-visible text produced by a future feature is not covered until it is added here. The inventory is a maintained list, not a derivation. Two tests narrow that gap from either side: `TestEveryEntryConstructorIsCalledFromARealPath` fails when something builds an entry that no real path calls, and `TestTheInventoryDescribesEntriesThatExist` fails when a row here describes an id nothing constructs. Neither can say whether an entry is attached in the *right* place, which stays a question for a reader.
* Trust classes are declarations to the model and to this record. They are not enforcement. The permission gate, the workspace boundary, the credential guards and the tool allowlists are the runtime controls, and they are outside prompt text by design.
* Provenance is preserved up to a compaction summary, not inside one: a summary is labelled as machine-written, and the several sources it condenses become one rendering.
