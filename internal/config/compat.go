package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
)

// opencodeModelFacts is what a provider's models block says about one
// model: the id it is served under, and its limits.
type opencodeModelFacts struct {
	wireID        string
	contextWindow int
	maxTokens     int
}

// listedModels is the names a whitelist or blacklist entry can match: the
// key as the file wrote it, and the wire id that key maps to.
//
// Both, because a profile carries the wire id. A models block may rename
// what it serves — {"fast": {"id": "deepseek-ai/DeepSeek-V3"}} — and the
// synthesised profile takes the id, since that is what goes on the wire.
// Matching only the key meant a whitelist rejected the very model it
// listed, and a blacklist let through the one it forbade: the wrong
// answer in both directions, and the blacklist's was the open one.
func listedModels(list []string, facts map[string]opencodeModelFacts) map[string]bool {
	out := make(map[string]bool, len(list)*2)
	for _, item := range list {
		out[item] = true
		if f, ok := facts[item]; ok && f.wireID != "" {
			out[f.wireID] = true
		}
	}
	return out
}

// namedAs is how a message should refer to a model the file listed: the
// entry the person typed, and the id it resolves to when those differ.
//
// A models block may rename what it serves, and the profile carries the
// served id — so a refusal about a blacklist entry that quoted only the
// id sent somebody looking through their own list for a string that is
// not in it.
func namedAs(list []string, facts map[string]opencodeModelFacts, model string) string {
	for _, item := range list {
		if item == model {
			return fmt.Sprintf("%q", item)
		}
		if f, ok := facts[item]; ok && f.wireID == model {
			return fmt.Sprintf("%q (served as %q)", item, model)
		}
	}
	return fmt.Sprintf("%q", model)
}

// mergeServerBlocks joins two objects of MCP servers, reporting the first
// name they share. ok is false when either is not an object at all.
func mergeServerBlocks(a, b json.RawMessage) (merged json.RawMessage, clash string, ok bool) {
	var am, bm map[string]json.RawMessage
	if json.Unmarshal(a, &am) != nil || json.Unmarshal(b, &bm) != nil {
		return nil, "", false
	}
	out := make(map[string]json.RawMessage, len(am)+len(bm))
	for k, v := range bm {
		out[k] = v
	}
	for _, k := range sortedKeys(am) {
		if _, both := bm[k]; both {
			return nil, k, true
		}
		out[k] = am[k]
	}
	b2, err := json.Marshal(out)
	if err != nil {
		return nil, "", false
	}
	return b2, "", true
}

// jsonText is a value as the file wrote it, for a message about it.
//
// Refusals used to interpolate a Go variable, which is the value only
// when the unmarshal into it succeeded: `share: {"mode":"auto"}` came
// back as `share: ""` because the string stayed zero, and `snapshot:
// true` was refused with a sentence that said `snapshot: false`. A
// message that names a value the file does not contain sends somebody
// looking for text that is not there.
func jsonText(raw json.RawMessage) string {
	return strings.TrimSpace(string(raw))
}

// toolsCoveredBy is the localcode tool names a permission key stands
// for: itself, plus whatever the opencode alias table adds. Sorted, so a
// message built from it reads the same every time.
func toolsCoveredBy(key string) []string {
	if covered, ok := ToolAliases[key]; ok {
		out := append([]string(nil), covered...)
		sort.Strings(out)
		return out
	}
	return []string{key}
}

// collidingPermissionKey is the key in an existing permission block that
// stands for any of the same tools as target, or "" when none does.
func collidingPermissionKey(perms map[string]json.RawMessage, target string) string {
	if len(perms) == 0 {
		return ""
	}
	want := make(map[string]bool)
	for _, t := range toolsCoveredBy(target) {
		want[t] = true
	}
	for _, key := range sortedKeys(perms) {
		for _, t := range toolsCoveredBy(key) {
			if want[t] {
				return key
			}
		}
	}
	return ""
}

// reservedProfilePrefix marks a profile localcode wrote for itself out of
// an opencode key, rather than one a person wrote. Reserved so the
// synthesis cannot land on a name somebody is already using: a collision
// there would be silent, and the profile that lost would take a session's
// model with it.
const reservedProfilePrefix = "opencode:"

// Normalized is what an opencode-shaped file becomes.
type Normalized struct {
	JSON        []byte   // localcode-shaped, ready for json.Unmarshal
	Ignored     []string // opencode dotted paths accepted and not honoured, sorted
	Synthesised []string // profile names synthesised from opencode keys
}

type refusal struct {
	path string
	msg  string
}

// NormalizeOpencode rewrites opencode's spellings into localcode's own.
// raw is the file's bytes with its comments already blanked, and with its
// {env:NAME} placeholders still in it: substitution runs after this, so
// one pass covers both the file's placeholders and the ones written here.
// An error is the refusal, and it names every key that caused one.
//
// Pure function: no file system, no network, no OS or environment queries,
// no package-level mutable state. Everything it needs is in the bytes.
// Operates only on the root JSON object, ensuring nested keys such as
// profiles.*.provider or providers.*.profile are never rewritten.
func NormalizeOpencode(raw []byte) (Normalized, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Normalized{JSON: raw}, nil
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		// If raw is not a JSON object, return raw so downstream json.Unmarshal
		// reports the parse error with line context.
		return Normalized{JSON: raw}, nil
	}

	var reserved []refusal
	// The reserved prefix, checked against what the file itself declares
	// and before anything is synthesised into the same namespace.
	//
	// Here rather than in Validate, and this is the point: Validate sees a
	// merged Config in which a synthesised profile and a hand-written one
	// are the same kind of thing, so telling them apart there needs the
	// loader to have remembered which was which — a fact carried on the
	// struct, surviving a merge, and read by a rule that then gives two
	// answers for the same file depending on how it was loaded. The bytes
	// know already: a profiles block in the file is hand-written by
	// definition.
	if profilesRaw, ok := root["profiles"]; ok {
		var declared map[string]json.RawMessage
		if json.Unmarshal(profilesRaw, &declared) == nil {
			var taken []string
			for name := range declared {
				if strings.HasPrefix(name, reservedProfilePrefix) {
					taken = append(taken, name)
				}
			}
			sort.Strings(taken)
			for _, name := range taken {
				reserved = append(reserved, refusal{
					path: "profiles." + name,
					msg: fmt.Sprintf(
						"profile %q: a profile whose name begins with %q is reserved for keys read from an opencode file; rename yours",
						name, reservedProfilePrefix),
				})
			}
		}
	}

	refusals := reserved
	var ignored []string
	var synthesised []string
	changed := false

	// Step 1: mcp -> mcp_servers
	// Both present -> refuse naming both.
	// Only mcp present -> rename to mcp_servers.
	rawMCP, hasMCP := root["mcp"]
	rawMCPServers, hasMCPServers := root["mcp_servers"]
	if hasMCP && hasMCPServers {
		// Two spellings of one block, and unlike a scalar key they can
		// hold different things: "mcp" naming one server and
		// "mcp_servers" another is two halves of one list rather than a
		// contradiction, so they are joined. What is not joinable is a
		// server named in both — that is the file saying two things about
		// one server, and picking a winner would leave the other read by
		// nobody.
		merged, clash, ok := mergeServerBlocks(rawMCP, rawMCPServers)
		switch {
		case !ok:
			refusals = append(refusals, refusal{
				path: "mcp",
				msg:  `mcp and mcp_servers are both there and at least one of them is not an object of servers; keep one of them`,
			})
		case clash != "":
			refusals = append(refusals, refusal{
				path: "mcp." + clash,
				msg:  fmt.Sprintf(`server %q is in both "mcp" and "mcp_servers"; keep one of them`, clash),
			})
		default:
			root["mcp_servers"] = merged
			delete(root, "mcp")
			changed = true
		}
	} else if hasMCP {
		root["mcp_servers"] = root["mcp"]
		delete(root, "mcp")
		changed = true
	}

	// Step 1: tools -> permission
	// Translates boolean switches: false -> deny, true -> allow.
	// Maps write and apply_patch to edit (per opencode documentation).
	// Refuses if any tool is defined in both tools and permission.
	if toolsRaw, ok := root["tools"]; ok {
		delete(root, "tools")
		changed = true

		var toolsMap map[string]json.RawMessage
		if err := json.Unmarshal(toolsRaw, &toolsMap); err != nil {
			refusals = append(refusals, refusal{
				path: "tools",
				msg:  fmt.Sprintf(`"tools" must be an object of tool booleans, and it is %s`, jsonText(toolsRaw)),
			})
			toolsMap = nil
		}

		var existingPerms map[string]json.RawMessage
		if permRaw, exists := root["permission"]; exists {
			if err := json.Unmarshal(permRaw, &existingPerms); err != nil {
				// A bare decision string, which opencode allows and which
				// means that decision for every tool. It collides with
				// nothing by name and overlaps everything, so there is no
				// merge to do that would not be an invented precedence:
				// does "ask" for everything sit over "bash": false, or
				// under it? The file has to say which it means.
				//
				// Refused rather than merged, and certainly rather than
				// dropped — which is what happened here, silently, in the
				// direction that removes a confirmation: the blanket ask
				// disappeared and every tool but the one named in tools
				// came back allow.
				var bare string
				if json.Unmarshal(permRaw, &bare) == nil {
					refusals = append(refusals, refusal{
						path: "permission",
						msg: fmt.Sprintf(`permission is %q, which is every tool, and "tools" names tools inside it; `+
							`write them as one permission block`, bare),
					})
				} else {
					refusals = append(refusals, refusal{
						path: "permission",
						msg:  fmt.Sprintf(`permission must be a decision string or an object of tool rules, and it is %s`, jsonText(permRaw)),
					})
				}
				toolsMap = nil
			}
		}

		mappedPerms := make(map[string]string)
		for _, k := range sortedKeys(toolsMap) {
			rawVal := toolsMap[k]
			var val bool
			if err := json.Unmarshal(rawVal, &val); err != nil {
				refusals = append(refusals, refusal{
					path: "tools." + k,
					msg:  fmt.Sprintf(`tools %q is %s, and every entry there is true or false`, k, jsonText(rawVal)),
				})
				continue
			}

			targetTool := k
			if k == "write" || k == "apply_patch" {
				targetTool = "edit"
			}

			decision := "allow"
			if !val {
				decision = "deny"
			}

			if other := collidingPermissionKey(existingPerms, targetTool); other != "" {
				// Overlap, not the same spelling. "tools": {"write": false}
				// becomes edit, which covers write_file as well, so a
				// permission block naming write_file is talking about the
				// same call — and resolution prefers the exact name over
				// the alias, so the file denied edits in one spelling and
				// allowed them in the other, with nothing said. Comparing
				// literal keys missed it because the two strings differ.
				//
				// The message names what the person wrote on both sides,
				// since that is what they will look for in their file.
				refusals = append(refusals, refusal{
					path: "tools." + k,
					msg: fmt.Sprintf(`tools %q and permission %q are the same tools (%s); keep one of them`,
						k, other, strings.Join(toolsCoveredBy(targetTool), ", ")),
				})
				continue
			}

			if prevDecision, seen := mappedPerms[targetTool]; seen && prevDecision != decision {
				refusals = append(refusals, refusal{
					path: "tools." + k,
					msg:  fmt.Sprintf(`tools %q conflicts with another entry for %q; keep one of them`, k, targetTool),
				})
				continue
			}

			mappedPerms[targetTool] = decision
		}

		if len(mappedPerms) > 0 {
			if existingPerms == nil {
				b, err := json.Marshal(mappedPerms)
				if err != nil {
					return Normalized{}, err
				}
				root["permission"] = b
			} else {
				for t, d := range mappedPerms {
					valBytes, _ := json.Marshal(d)
					existingPerms[t] = valBytes
				}
				b, err := json.Marshal(existingPerms)
				if err != nil {
					return Normalized{}, err
				}
				root["permission"] = b
			}
		}
	}

	// Step 2: compaction
	// compaction.auto is honoured -> auto_compact_enabled.
	// Disagreeing values between auto_compact_enabled and compaction.auto refuse.
	// prune, tail_turns, preserve_recent_tokens, reserved are refused.
	if compRaw, ok := root["compaction"]; ok {
		delete(root, "compaction")
		changed = true

		var compMap map[string]json.RawMessage
		if err := json.Unmarshal(compRaw, &compMap); err == nil && compMap != nil {
			if autoRaw, hasAuto := compMap["auto"]; hasAuto {
				var autoVal bool
				if err := json.Unmarshal(autoRaw, &autoVal); err == nil {
					if existingRaw, hasExisting := root["auto_compact_enabled"]; hasExisting {
						var existingVal bool
						if err := json.Unmarshal(existingRaw, &existingVal); err == nil && existingVal != autoVal {
							refusals = append(refusals, refusal{
								path: "compaction.auto",
								msg:  fmt.Sprintf("auto_compact_enabled is %v and compaction.auto is %v, which disagree; keep one of them", existingVal, autoVal),
							})
						}
					} else {
						root["auto_compact_enabled"] = autoRaw
					}
				}
			}
			if _, hasPrune := compMap["prune"]; hasPrune {
				refusals = append(refusals, refusal{
					path: "compaction.prune",
					msg:  `compaction.prune asks localcode to drop old tool outputs from the history it sends, and localcode has no pruning pass to turn on. Remove it, or use compaction.auto, which localcode does honour.`,
				})
			}
			if _, hasTailTurns := compMap["tail_turns"]; hasTailTurns {
				refusals = append(refusals, refusal{
					path: "compaction.tail_turns",
					msg:  `compaction.tail_turns says how many recent turns to keep verbatim when compacting, and localcode's compaction has no turn-count retention to set. Remove it; compaction.auto is the part localcode can honour.`,
				})
			}
			if _, hasPreserve := compMap["preserve_recent_tokens"]; hasPreserve {
				refusals = append(refusals, refusal{
					path: "compaction.preserve_recent_tokens",
					msg:  `compaction.preserve_recent_tokens sets how much recent history survives compaction verbatim, and localcode's compaction has no such budget. Remove it; compaction.auto is the part localcode can honour.`,
				})
			}
			if _, hasReserved := compMap["reserved"]; hasReserved {
				refusals = append(refusals, refusal{
					path: "compaction.reserved",
					msg:  `compaction.reserved reserves a slice of the context window so compaction itself cannot overflow it, and localcode sizes that headroom itself with no setting to override. Remove it; compaction.auto is the part localcode can honour.`,
				})
			}
		}
	}

	// Step 2: Standalone refusals
	if _, ok := root["small_model"]; ok {
		refusals = append(refusals, refusal{
			path: "small_model",
			msg:  `small_model names a second model for lightweight work, and localcode has no title-generation or cheap-utility lane to point it at — its only cheap lane, the Smart Agent "quick" category, delegates real work rather than trivia. Remove it, or write a profile named "smart-quick" if that is what you meant.`,
		})
	}

	if _, ok := root["tool_output"]; ok {
		refusals = append(refusals, refusal{
			path: "tool_output",
			msg:  `tool_output sets how much of a tool result reaches the model, and localcode sizes that from the model's context window instead, with no setting to override it and nowhere that keeps the part it cuts. Remove it; a large result is already trimmed, but to localcode's budget rather than yours.`,
		})
	}

	if raw, ok := root["snapshot"]; ok {
		// opencode's default is true, and localcode always records them,
		// so "snapshot": true asks for what already happens.
		var want bool
		if json.Unmarshal(raw, &want) == nil && want {
			delete(root, "snapshot")
			changed = true
		} else {
			refusals = append(refusals, refusal{
				path: "snapshot",
				msg: fmt.Sprintf(`snapshot: %s asks localcode not to record file snapshots, and localcode copies every file a turn edits before changing it so /rewind can put it back. `+
					`There is no setting to turn that off. Remove the key, or accept that the copies are made.`, jsonText(raw)),
			})
		}
	}

	if _, ok := root["plugin"]; ok {
		refusals = append(refusals, refusal{
			path: "plugin",
			msg:  `plugin names code for localcode to load at startup, and localcode does not install or import anything at runtime. Remove the key; a plugin's tools will not be there and its checks will not run.`,
		})
	}

	if _, ok := root["enterprise"]; ok {
		refusals = append(refusals, refusal{
			path: "enterprise",
			msg:  `enterprise.url points localcode at an organisation server for configuration, and localcode reads its configuration only from the files on this machine. Remove the key; whatever that server mandates will not be applied here.`,
		})
	}

	if _, ok := root["experimental"]; ok {
		refusals = append(refusals, refusal{
			path: "experimental",
			msg:  `experimental configures opencode internals that localcode does not have, and two of its fields — primary_tools and policies — take access away rather than add it, so ignoring them would leave localcode more permissive than your file. Remove the key.`,
		})
	}

	if _, ok := root["attachment"]; ok {
		refusals = append(refusals, refusal{
			path: "attachment",
			msg:  `attachment limits the size of an image before it is sent to the model, and localcode sends images as they are, bounded only by a 32MB cap on the upload itself. Remove the key; an image larger than your limits will still be sent whole.`,
		})
	}

	if _, ok := root["command"]; ok {
		refusals = append(refusals, refusal{
			path: "command",
			msg:  `command defines slash commands inside the config file, and localcode reads commands only from files. Move each entry to .opencode/command/<name>.md — the template becomes the body, and description, agent and model become its frontmatter — and localcode will pick it up as it stands.`,
		})
	}

	if skillsRaw, ok := root["skills"]; ok {
		var skillsMap map[string]json.RawMessage
		if err := json.Unmarshal(skillsRaw, &skillsMap); err == nil && skillsMap != nil {
			if _, hasPaths := skillsMap["paths"]; hasPaths {
				refusals = append(refusals, refusal{
					path: "skills.paths",
					msg:  `skills.paths adds skill folders from elsewhere on the disk, and localcode reads skills only from the project and the home directory it resolves. Remove it, or put the skills under .opencode/skills, which localcode already reads.`,
				})
			}
			if _, hasUrls := skillsMap["urls"]; hasUrls {
				refusals = append(refusals, refusal{
					path: "skills.urls",
					msg:  `skills.urls fetches skills over the network, and localcode does not fetch text the model follows from the network. Remove it, and keep the skills you want in .opencode/skills, which localcode already reads.`,
				})
			}
		}
	}

	if _, ok := root["references"]; ok {
		refusals = append(refusals, refusal{
			path: "references",
			msg:  `references makes directories outside this project, and cloned git repositories, readable as part of it, and localcode decides what may be read outside the workspace with read_outside_workspace and the external_directory permission instead. Remove the key; a directory listed here is not readable, and no repository is cloned.`,
		})
	}

	if _, ok := root["reference"]; ok {
		refusals = append(refusals, refusal{
			path: "reference",
			msg:  `reference is opencode's older spelling of references, and localcode refuses both: a directory outside this project is governed by read_outside_workspace and the external_directory permission, not by an alias in the config. Remove the key.`,
		})
	}

	if shareRaw, ok := root["share"]; ok {
		var mode string
		if err := json.Unmarshal(shareRaw, &mode); err == nil && mode == "disabled" {
			delete(root, "share")
			changed = true
		} else {
			refusals = append(refusals, refusal{
				path: "share",
				msg: fmt.Sprintf(`share: %s asks for a session to be publishable to a share URL, and localcode has no sharing — nothing is ever published, `+
					`and nothing here will publish it for you. Remove the key, or set it to "disabled", which is what localcode does.`, jsonText(shareRaw)),
			})
		}
	}

	if autoshareRaw, ok := root["autoshare"]; ok {
		var b bool
		if err := json.Unmarshal(autoshareRaw, &b); err == nil && !b {
			delete(root, "autoshare")
			changed = true
		} else {
			refusals = append(refusals, refusal{
				path: "autoshare",
				msg:  `autoshare: true asks for every new session to be published to a share URL, and localcode has no sharing — nothing is ever published. Remove the key; do not rely on this file to have turned sharing on.`,
			})
		}
	}

	if formatterRaw, ok := root["formatter"]; ok {
		var b bool
		if err := json.Unmarshal(formatterRaw, &b); err == nil && !b {
			delete(root, "formatter")
			changed = true
		} else {
			refusals = append(refusals, refusal{
				path: "formatter",
				msg:  `formatter asks for files to be formatted after they are edited, and localcode does not run formatters. Remove the key, or format from a hook or your own command; files localcode edits are left exactly as written.`,
			})
		}
	}

	if lspRaw, ok := root["lsp"]; ok {
		var b bool
		if err := json.Unmarshal(lspRaw, &b); err == nil && !b {
			delete(root, "lsp")
			changed = true
		} else {
			refusals = append(refusals, refusal{
				path: "lsp",
				msg:  `lsp configures language servers for localcode to start, and localcode starts none. Remove the key; there will be no diagnostics and no lsp tool.`,
			})
		}
	}

	if _, ok := root["server"]; ok {
		refusals = append(refusals, refusal{
			path: "server",
			msg:  `server is not supported — localcode takes its listen address from the --listen flag, not from the config file, so run localcode --listen 127.0.0.1:1234 rather than "server": {"port": 1234}. mdns, mdnsDomain and cors have no localcode equivalent.`,
		})
	}

	// Step 2: INERT keys (accepted and not honoured)
	if _, ok := root["logLevel"]; ok {
		ignored = append(ignored, "logLevel")
		delete(root, "logLevel")
		changed = true
	}
	if _, ok := root["watcher"]; ok {
		ignored = append(ignored, "watcher")
		delete(root, "watcher")
		changed = true
	}
	if _, ok := root["username"]; ok {
		ignored = append(ignored, "username")
		delete(root, "username")
		changed = true
	}
	if _, ok := root["layout"]; ok {
		ignored = append(ignored, "layout")
		delete(root, "layout")
		changed = true
	}

	// Step 3: provider -> providers, with all provider and model sub-rules,
	// and enabled_providers / disabled_providers filtering.
	//
	// Renaming the provider container without simultaneously handling npm/id
	// type selection and option translation would produce ProviderConfig{Type: ""}
	// which fails at wire-time with an opaque runtime error. Likewise, model limits
	// must be parsed alongside providers because an unrecognised endpoint would
	// otherwise default to modelinfo's 128k token guess.
	rawProvider, hasProvider := root["provider"]
	rawProviders, hasProviders := root["providers"]
	if hasProvider && hasProviders {
		var provA, provB any
		_ = json.Unmarshal(rawProvider, &provA)
		_ = json.Unmarshal(rawProviders, &provB)
		if !reflect.DeepEqual(provA, provB) {
			refusals = append(refusals, refusal{
				path: "provider",
				msg:  `provider and providers are two spellings of the same block; keep one of them`,
			})
		}
	} else if hasProvider {
		root["providers"] = rawProvider
		delete(root, "provider")
		changed = true
	}

	providersChanged := hasProvider

	var providersMap map[string]json.RawMessage
	if rawP, ok := root["providers"]; ok {
		_ = json.Unmarshal(rawP, &providersMap)
	}
	pNames := sortedKeys(providersMap)

	// disabled_providers: refuse on overlap only. If a listed provider is also defined,
	// localcode would still load it because localcode has no auto-provider loading to disable.
	if rawDP, ok := root["disabled_providers"]; ok {
		delete(root, "disabled_providers")
		changed = true
		var dpList []string
		if err := json.Unmarshal(rawDP, &dpList); err == nil {
			for _, dName := range dpList {
				if _, exists := providersMap[dName]; exists {
					refusals = append(refusals, refusal{
						path: "disabled_providers." + dName,
						msg:  fmt.Sprintf(`disabled_providers lists %q, and this file also defines providers.%s — localcode has no automatic provider loading to switch off, so it would load that block and use it. Remove %q from disabled_providers, or remove the providers.%s block.`, dName, dName, dName, dName),
					})
				}
			}
		}
	}

	// enabled_providers: refuse on omission only. If a defined provider is omitted,
	// opencode ignores it but localcode would load and use it.
	if rawEP, ok := root["enabled_providers"]; ok {
		delete(root, "enabled_providers")
		changed = true
		var epList []string
		if err := json.Unmarshal(rawEP, &epList); err == nil {
			epSet := make(map[string]bool, len(epList))
			for _, e := range epList {
				epSet[e] = true
			}
			for _, pName := range pNames {
				if !epSet[pName] {
					refusals = append(refusals, refusal{
						path: "enabled_providers." + pName,
						msg:  fmt.Sprintf(`enabled_providers does not list %q, and this file defines providers.%s — enabled_providers means every other provider is ignored, but localcode would still load that block and use it. Add %q to enabled_providers, or remove the providers.%s block.`, pName, pName, pName, pName),
					})
				}
			}
		}
	}

	providerModelFacts := make(map[string]map[string]opencodeModelFacts)
	providerBlacklists := make(map[string][]string)
	providerWhitelists := make(map[string][]string)

	for _, pName := range pNames {
		var provMap map[string]json.RawMessage
		if err := json.Unmarshal(providersMap[pName], &provMap); err != nil || provMap == nil {
			continue
		}
		provChanged := false

		// provider.<name>.name is INERT (localcode surfaces display map keys).
		if _, ok := provMap["name"]; ok {
			ignored = append(ignored, fmt.Sprintf("provider.%s.name", pName))
			delete(provMap, "name")
			provChanged = true
		}

		// provider.<name>.npm and id select ProviderType.
		var npmVal string
		hasNPM := false
		if rawNPM, ok := provMap["npm"]; ok {
			hasNPM = true
			_ = json.Unmarshal(rawNPM, &npmVal)
			delete(provMap, "npm")
			provChanged = true
		}

		var idVal string
		hasID := false
		if rawID, ok := provMap["id"]; ok {
			hasID = true
			_ = json.Unmarshal(rawID, &idVal)
			delete(provMap, "id")
			provChanged = true
		}

		var provType string
		if rawType, ok := provMap["type"]; ok {
			_ = json.Unmarshal(rawType, &provType)
		}

		if hasNPM {
			switch npmVal {
			case "@ai-sdk/openai-compatible":
				provType = "openai-compat"
			case "@ai-sdk/anthropic":
				provType = "anthropic"
			case "@ai-sdk/amazon-bedrock":
				provType = "bedrock"
			default:
				refusals = append(refusals, refusal{
					path: fmt.Sprintf("provider.%s.npm", pName),
					msg:  fmt.Sprintf(`provider %q: npm is %q, and localcode has no client for it — localcode speaks three protocols (Anthropic messages, Bedrock Converse, OpenAI chat/completions) and installs nothing at runtime. Use "@ai-sdk/openai-compatible" if the endpoint serves /v1/chat/completions.`, pName, npmVal),
				})
			}
		} else if hasID {
			switch idVal {
			case "anthropic":
				provType = "anthropic"
			case "amazon-bedrock":
				provType = "bedrock"
			default:
				refusals = append(refusals, refusal{
					path: fmt.Sprintf("provider.%s.id", pName),
					msg:  fmt.Sprintf(`provider %q: id is %q and there is no npm, so which client to use is a fact localcode would have to look up in models.dev — it does not. Say npm: "@ai-sdk/openai-compatible" with options.baseURL, or use a provider localcode has a client for.`, pName, idVal),
				})
			}
		}

		if (hasNPM || hasID) && provType != "" {
			b, _ := json.Marshal(provType)
			provMap["type"] = b
			provChanged = true
		}

		// Options handling
		var optionsMap map[string]json.RawMessage
		if rawOpts, ok := provMap["options"]; ok {
			_ = json.Unmarshal(rawOpts, &optionsMap)
		}

		// options.baseURL vs api
		var baseURLVal string
		hasBaseURL := false
		if optionsMap != nil {
			if rawBU, ok := optionsMap["baseURL"]; ok {
				hasBaseURL = true
				_ = json.Unmarshal(rawBU, &baseURLVal)
				delete(optionsMap, "baseURL")
				provChanged = true
			}
		}

		var apiVal string
		hasAPI := false
		if rawAPI, ok := provMap["api"]; ok {
			hasAPI = true
			_ = json.Unmarshal(rawAPI, &apiVal)
			delete(provMap, "api")
			provChanged = true
		}

		if hasBaseURL && hasAPI && baseURLVal != apiVal {
			refusals = append(refusals, refusal{
				path: fmt.Sprintf("provider.%s.options.baseURL", pName),
				msg:  fmt.Sprintf(`provider %q: api is %q and options.baseURL is %q, which disagree; keep one of them`, pName, apiVal, baseURLVal),
			})
		}

		rawURL := baseURLVal
		usedKey := "options.baseURL"
		if !hasBaseURL && hasAPI {
			rawURL = apiVal
			usedKey = "api"
		}

		if rawURL != "" {
			switch provType {
			case "openai-compat":
				b, _ := json.Marshal(rawURL)
				provMap["base_url"] = b
				provChanged = true
			case "anthropic":
				// The /v1 trap: localcode's anthropic client appends /v1/messages to a host root,
				// while opencode writes https://api.anthropic.com/v1. Strip one trailing /v1.
				// If a non-v1 path segment is present, refuse because no base_url reaches it.
				trimmed := strings.TrimRight(rawURL, "/")
				u, err := url.Parse(trimmed)
				if err != nil {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("provider.%s.%s", pName, usedKey),
						msg:  fmt.Sprintf(`provider %q: %s is %q, and localcode's anthropic client always appends /v1/messages, so there is no base_url that reaches the path this names.`, pName, usedKey, rawURL),
					})
				} else {
					path := strings.TrimRight(u.Path, "/")
					if path == "" {
						b, _ := json.Marshal(trimmed)
						provMap["base_url"] = b
						provChanged = true
					} else if strings.HasSuffix(path, "/v1") {
						u.Path = strings.TrimSuffix(path, "/v1")
						cleaned := strings.TrimRight(u.String(), "/")
						b, _ := json.Marshal(cleaned)
						provMap["base_url"] = b
						provChanged = true
					} else {
						refusals = append(refusals, refusal{
							path: fmt.Sprintf("provider.%s.%s", pName, usedKey),
							msg:  fmt.Sprintf(`provider %q: %s is %q, and localcode's anthropic client always appends /v1/messages, so there is no base_url that reaches the path this names.`, pName, usedKey, rawURL),
						})
					}
				}
			}
		} else if provType == "openai-compat" {
			if _, hasExistingBU := provMap["base_url"]; !hasExistingBU {
				refusals = append(refusals, refusal{
					path: fmt.Sprintf("provider.%s.npm", pName),
					msg:  fmt.Sprintf(`provider %q: npm is "@ai-sdk/openai-compatible" requires options.baseURL or api`, pName),
				})
			}
		}

		// options.apiKey
		if optionsMap != nil {
			if rawAK, ok := optionsMap["apiKey"]; ok {
				provMap["api_key"] = rawAK
				delete(optionsMap, "apiKey")
				provChanged = true
			}
		}

		// provider.<name>.env: fallback for API key
		if rawEnv, ok := provMap["env"]; ok {
			delete(provMap, "env")
			provChanged = true
			var envList []string
			if json.Unmarshal(rawEnv, &envList) == nil && len(envList) > 0 {
				if (provType == "anthropic" || provType == "openai-compat") && provMap["api_key"] == nil {
					b, _ := json.Marshal(fmt.Sprintf("{env:%s}", envList[0]))
					provMap["api_key"] = b
				}
			}
		}

		// options.region and options.profile (Bedrock)
		if optionsMap != nil {
			if rawReg, ok := optionsMap["region"]; ok {
				if provType == "bedrock" {
					provMap["region"] = rawReg
					provChanged = true
				}
				delete(optionsMap, "region")
				provChanged = true
			}
			if rawProf, ok := optionsMap["profile"]; ok {
				provMap["profile"] = rawProf
				delete(optionsMap, "profile")
				provChanged = true
			}

			// Refused provider options
			if _, ok := optionsMap["endpoint"]; ok {
				refusals = append(refusals, refusal{
					path: fmt.Sprintf("provider.%s.options.endpoint", pName),
					msg:  fmt.Sprintf(`provider %q: options.endpoint names a Bedrock VPC endpoint, and localcode's bedrock provider is built from region and profile alone (there is no endpoint to set), so requests would go to the public regional endpoint instead — remove it, or use a provider type whose base_url localcode can set.`, pName),
				})
				delete(optionsMap, "endpoint")
				provChanged = true
			}
			if _, ok := optionsMap["headers"]; ok {
				refusals = append(refusals, refusal{
					path: fmt.Sprintf("provider.%s.options.headers", pName),
					msg:  fmt.Sprintf(`provider %q: options.headers sets headers on every request to this provider, and localcode's clients send only their own — the headers named here would not be sent.`, pName),
				})
				delete(optionsMap, "headers")
				provChanged = true
			}
			if _, ok := optionsMap["enterpriseUrl"]; ok {
				refusals = append(refusals, refusal{
					path: fmt.Sprintf("provider.%s.options.enterpriseUrl", pName),
					msg:  fmt.Sprintf(`provider %q: options.enterpriseUrl is GitHub Copilot's enterprise authentication host, and localcode has no Copilot provider — it reaches model endpoints directly rather than through another vendor's client.`, pName),
				})
				delete(optionsMap, "enterpriseUrl")
				provChanged = true
			}
			if _, ok := optionsMap["setCacheKey"]; ok {
				refusals = append(refusals, refusal{
					path: fmt.Sprintf("provider.%s.options.setCacheKey", pName),
					msg:  fmt.Sprintf(`provider %q: options.setCacheKey asks for a prompt cache key on every request, and localcode sends none — the caching this turns on would not happen.`, pName),
				})
				delete(optionsMap, "setCacheKey")
				provChanged = true
			}
			if rawTO, ok := optionsMap["timeout"]; ok {
				var b bool
				if err := json.Unmarshal(rawTO, &b); err == nil && !b {
					// false is accepted
				} else {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("provider.%s.options.timeout", pName),
						msg:  fmt.Sprintf(`provider %q: options.timeout bounds a request at %s ms, and localcode's model requests have no deadline — the bound written here would not be applied.`, pName, string(rawTO)),
					})
				}
				delete(optionsMap, "timeout")
				provChanged = true
			}
			if rawHT, ok := optionsMap["headerTimeout"]; ok {
				var b bool
				if err := json.Unmarshal(rawHT, &b); err == nil && !b {
					// false is accepted
				} else {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("provider.%s.options.headerTimeout", pName),
						msg:  fmt.Sprintf(`provider %q: options.headerTimeout waits %s ms for response headers and then aborts, and localcode's model requests have no deadline of any kind — this bound would not be applied.`, pName, string(rawHT)),
					})
				}
				delete(optionsMap, "headerTimeout")
				provChanged = true
			}
			if rawCT, ok := optionsMap["chunkTimeout"]; ok {
				var b bool
				if err := json.Unmarshal(rawCT, &b); err == nil && !b {
					// false is accepted
				} else {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("provider.%s.options.chunkTimeout", pName),
						msg:  fmt.Sprintf(`provider %q: options.chunkTimeout aborts a stream when no chunk arrives for %s ms, and localcode does not watch the gap between chunks — a stalled stream would hang until the turn is cancelled.`, pName, string(rawCT)),
					})
				}
				delete(optionsMap, "chunkTimeout")
				provChanged = true
			}

			if len(optionsMap) == 0 {
				delete(provMap, "options")
				provChanged = true
			} else {
				b, _ := json.Marshal(optionsMap)
				provMap["options"] = b
				provChanged = true
			}
		}

		// Blacklist and whitelist: save for checking after all profiles are synthesised.
		if rawBL, ok := provMap["blacklist"]; ok {
			var bl []string
			if json.Unmarshal(rawBL, &bl) == nil {
				providerBlacklists[pName] = bl
			}
			delete(provMap, "blacklist")
			provChanged = true
		}
		if rawWL, ok := provMap["whitelist"]; ok {
			var wl []string
			if json.Unmarshal(rawWL, &wl) == nil {
				providerWhitelists[pName] = wl
			}
			delete(provMap, "whitelist")
			provChanged = true
		}

		// provider.<name>.models: not a profile factory. Extracts limits and wire IDs,
		// ignores inert display metadata, and refuses unsupported model constraints.
		if rawModels, ok := provMap["models"]; ok {
			delete(provMap, "models")
			provChanged = true
			var modelsMap map[string]json.RawMessage
			if json.Unmarshal(rawModels, &modelsMap) == nil {
				mKeys := sortedKeys(modelsMap)
				for _, mKey := range mKeys {
					var mEntry map[string]json.RawMessage
					if json.Unmarshal(modelsMap[mKey], &mEntry) != nil || mEntry == nil {
						continue
					}
					// Inert metadata
					for _, inKey := range []string{"name", "cost", "release_date", "status", "experimental"} {
						if _, ok := mEntry[inKey]; ok {
							ignored = append(ignored, fmt.Sprintf("provider.%s.models.%s.%s", pName, mKey, inKey))
							delete(mEntry, inKey)
						}
					}

					// Refusals
					if rawFam, ok := mEntry["family"]; ok {
						var fStr string
						_ = json.Unmarshal(rawFam, &fStr)
						refusals = append(refusals, refusal{
							path: fmt.Sprintf("provider.%s.models.%s.family", pName, mKey),
							msg:  fmt.Sprintf(`provider %q model %q: family is %q, and localcode classifies a model by its id — context window, per-family system-prompt text, keep-going budget — with nowhere to record a declared family, so the declaration would be silently dropped. Give the model its real id, or pin the window with the profile's context_window.`, pName, mKey, fStr),
						})
					}
					if rawTC, ok := mEntry["tool_call"]; ok {
						var tc bool
						if json.Unmarshal(rawTC, &tc) == nil && !tc {
							refusals = append(refusals, refusal{
								path: fmt.Sprintf("provider.%s.models.%s.tool_call", pName, mKey),
								msg:  fmt.Sprintf(`provider %q model %q: tool_call is false, and localcode offers its tools on every turn — this model would be sent the tool schemas the file says it cannot use.`, pName, mKey),
							})
						}
					}
					if rawAtt, ok := mEntry["attachment"]; ok {
						var att bool
						if json.Unmarshal(rawAtt, &att) == nil && !att {
							refusals = append(refusals, refusal{
								path: fmt.Sprintf("provider.%s.models.%s.attachment", pName, mKey),
								msg:  fmt.Sprintf(`provider %q model %q: attachment is false, and localcode sends an attached image to whatever model is in hand — an attachment would be sent to a model the file says cannot take one.`, pName, mKey),
							})
						}
					}
					if rawMod, ok := mEntry["modalities"]; ok {
						var modMap map[string][]string
						if json.Unmarshal(rawMod, &modMap) == nil {
							if inputList, hasInput := modMap["input"]; hasInput {
								hasImg := false
								for _, m := range inputList {
									if m == "image" {
										hasImg = true
										break
									}
								}
								if !hasImg {
									refusals = append(refusals, refusal{
										path: fmt.Sprintf("provider.%s.models.%s.modalities", pName, mKey),
										msg:  fmt.Sprintf(`provider %q model %q: modalities.input does not include "image", and localcode sends an attached image to whatever model is in hand — remove the field, or attach nothing to this model.`, pName, mKey),
									})
								}
							}
						}
					}
					if rawTemp, ok := mEntry["temperature"]; ok {
						var temp bool
						if json.Unmarshal(rawTemp, &temp) == nil && !temp {
							refusals = append(refusals, refusal{
								path: fmt.Sprintf("provider.%s.models.%s.temperature", pName, mKey),
								msg:  fmt.Sprintf(`provider %q model %q: temperature is false, and localcode sends a profile's temperature to whatever model is in hand — a profile with a temperature would be refused by this model.`, pName, mKey),
							})
						}
					}
					if rawReas, ok := mEntry["reasoning"]; ok {
						var reas bool
						if json.Unmarshal(rawReas, &reas) == nil && !reas {
							refusals = append(refusals, refusal{
								path: fmt.Sprintf("provider.%s.models.%s.reasoning", pName, mKey),
								msg:  fmt.Sprintf(`provider %q model %q: reasoning is false, and localcode sends a profile's effort as reasoning_effort or as an extended-thinking block — a profile with an effort would ask this model for reasoning the file says it has none of.`, pName, mKey),
							})
						}
					}
					if rawInt, ok := mEntry["interleaved"]; ok {
						var intStr string
						var intObj map[string]string
						isExempt := false
						if json.Unmarshal(rawInt, &intStr) == nil {
							if intStr == "reasoning" || intStr == "reasoning_content" {
								isExempt = true
							}
						} else if json.Unmarshal(rawInt, &intObj) == nil {
							if fld := intObj["field"]; fld == "reasoning" || fld == "reasoning_content" {
								isExempt = true
							}
						}
						if !isExempt {
							nameVal := string(rawInt)
							if intStr != "" {
								nameVal = fmt.Sprintf("%q", intStr)
							} else if fld, ok := intObj["field"]; ok && fld != "" {
								nameVal = fmt.Sprintf("%q", fld)
							}
							refusals = append(refusals, refusal{
								path: fmt.Sprintf("provider.%s.models.%s.interleaved", pName, mKey),
								msg:  fmt.Sprintf(`provider %q model %q: interleaved names %s as the field this model's reasoning arrives in, and localcode reads reasoning only from "reasoning_content" and "reasoning" on an openai-compat stream and cannot be told another field name. Remove it.`, pName, mKey, nameVal),
							})
						}
					}
					if _, ok := mEntry["options"]; ok {
						refusals = append(refusals, refusal{
							path: fmt.Sprintf("provider.%s.models.%s.options", pName, mKey),
							msg:  fmt.Sprintf(`provider %q model %q: options carries request settings for this model, and localcode cannot read them — it sends the parameters its own profile describes (max_tokens, temperature, top_p, top_k, effort) and nothing else. Move what you need into the profile, or remove it.`, pName, mKey),
						})
					}
					if _, ok := mEntry["headers"]; ok {
						refusals = append(refusals, refusal{
							path: fmt.Sprintf("provider.%s.models.%s.headers", pName, mKey),
							msg:  fmt.Sprintf(`provider %q model %q: headers sets headers on requests for this model, and localcode's clients send only their own — the headers named here would not be sent.`, pName, mKey),
						})
					}
					if _, ok := mEntry["variants"]; ok {
						refusals = append(refusals, refusal{
							path: fmt.Sprintf("provider.%s.models.%s.variants", pName, mKey),
							msg:  fmt.Sprintf(`provider %q model %q: variants define alternative settings for this model, and localcode has no variants — one profile is one set of settings. Write the settings you want into the profile (effort, temperature, top_p), or remove the block.`, pName, mKey),
						})
					}
					if _, ok := mEntry["provider"]; ok {
						refusals = append(refusals, refusal{
							path: fmt.Sprintf("provider.%s.models.%s.provider", pName, mKey),
							msg:  fmt.Sprintf(`provider %q model %q: provider.npm names a different client for this one model, and localcode picks a client per provider, not per model — give the model its own provider block with its own npm and baseURL.`, pName, mKey),
						})
					}

					wireID := mKey
					if rawWireID, ok := mEntry["id"]; ok {
						_ = json.Unmarshal(rawWireID, &wireID)
					}
					var cw, mt int
					if rawLim, ok := mEntry["limit"]; ok {
						var limMap map[string]int
						if json.Unmarshal(rawLim, &limMap) == nil {
							limContext := limMap["context"]
							limOutput := limMap["output"]
							limInput, hasInput := limMap["input"]
							if hasInput {
								sum := limInput + limOutput
								if limContext > 0 && sum > 0 {
									cw = min(limContext, sum)
								} else if limContext > 0 {
									cw = limContext
								} else {
									cw = sum
								}
							} else {
								cw = limContext
							}
							mt = limOutput
						}
					}
					facts := opencodeModelFacts{
						wireID:        wireID,
						contextWindow: cw,
						maxTokens:     mt,
					}
					if providerModelFacts[pName] == nil {
						providerModelFacts[pName] = make(map[string]opencodeModelFacts)
					}
					providerModelFacts[pName][mKey] = facts
					if wireID != mKey {
						providerModelFacts[pName][wireID] = facts
					}
				}
			}
		}

		if provChanged {
			b, _ := json.Marshal(provMap)
			providersMap[pName] = b
			providersChanged = true
			changed = true
		}
	}

	if providersChanged && len(providersMap) > 0 {
		b, _ := json.Marshal(providersMap)
		root["providers"] = b
	}

	// Step 4: root model -> opencode:default profile + default_profile.
	// Depends on Step 3: left half of the model string must resolve to a
	// provider block defined in this same file.
	var profilesMap map[string]json.RawMessage
	if rawProf, ok := root["profiles"]; ok {
		_ = json.Unmarshal(rawProf, &profilesMap)
	}
	if profilesMap == nil {
		profilesMap = make(map[string]json.RawMessage)
	}

	var curDefaultProfile string
	if rawDP, ok := root["default_profile"]; ok {
		_ = json.Unmarshal(rawDP, &curDefaultProfile)
	}

	if rawModel, ok := root["model"]; ok {
		var modelStr string
		if json.Unmarshal(rawModel, &modelStr) == nil {
			before, after, found := strings.Cut(modelStr, "/")
			if !found || providersMap[before] == nil {
				provName := before
				if !found {
					provName = modelStr
				}
				refusals = append(refusals, refusal{
					path: "model",
					msg:  fmt.Sprintf(`model is %q, and %q is not a provider this config defines. opencode resolves that name against its models.dev catalogue; localcode never reaches a catalogue, so it has no endpoint, no credential and no limits for it. Add a providers.%q block naming its type, base_url and key, or write the model under a provider this file already defines.`, modelStr, provName, provName),
				})
			} else {
				providerKey := before
				modelKey := after
				wireModel := modelKey
				var cw, mt int
				if facts, ok := providerModelFacts[providerKey][modelKey]; ok {
					if facts.wireID != "" {
						wireModel = facts.wireID
					}
					cw = facts.contextWindow
					mt = facts.maxTokens
				}

				if curDefaultProfile != "" && curDefaultProfile != "opencode:default" {
					refusals = append(refusals, refusal{
						path: "model",
						msg:  fmt.Sprintf(`default_profile is %q and model is %q, which disagree; keep one of them`, curDefaultProfile, modelStr),
					})
				}
				if profilesMap["opencode:default"] != nil {
					refusals = append(refusals, refusal{
						path: "model",
						msg:  `model wants to create profile "opencode:default", but a hand-written profile already holds that name; rename yours`,
					})
				}

				prof := Profile{
					Provider:      providerKey,
					Model:         wireModel,
					ContextWindow: cw,
					MaxTokens:     mt,
				}
				profBytes, _ := json.Marshal(prof)
				profilesMap["opencode:default"] = profBytes
				synthesised = append(synthesised, "opencode:default")
				root["default_profile"], _ = json.Marshal("opencode:default")
				curDefaultProfile = "opencode:default"
				delete(root, "model")
				changed = true
			}
		}
	}

	// Step 5: agent -> agents, with mode as deprecated alias.
	// Depends on Step 4: profile synthesis points to opencode:agent:<name>
	// or inherits default_profile so Validate's profile reference checks pass.
	rawAgent, hasAgent := root["agent"]
	rawMode, hasMode := root["mode"]
	var agentRawObj json.RawMessage
	// Which of the two names the block arrived under, for the messages
	// below: opencode's older spelling is "mode", and a refusal naming
	// "agent" to somebody whose file says "mode" is a refusal about a key
	// they do not have.
	agentSpelling := "agent"
	if hasAgent && hasMode {
		var aMap, mMap map[string]json.RawMessage
		_ = json.Unmarshal(rawAgent, &aMap)
		_ = json.Unmarshal(rawMode, &mMap)
		allKeys := sortedUnion(sortedKeys(aMap), sortedKeys(mMap))
		for _, k := range allKeys {
			aVal, aOk := aMap[k]
			mVal, mOk := mMap[k]
			if !aOk || !mOk || !bytes.Equal(bytes.TrimSpace(aVal), bytes.TrimSpace(mVal)) {
				refusals = append(refusals, refusal{
					path: "agent",
					msg:  fmt.Sprintf(`agent and mode are two spellings of the same block and they disagree on %q; keep one of them`, k),
				})
			}
		}
		agentRawObj, agentSpelling = rawAgent, "agent"
		delete(root, "mode")
		changed = true
	} else if hasMode {
		agentRawObj, agentSpelling = rawMode, "mode"
		delete(root, "mode")
		changed = true
	} else if hasAgent {
		agentRawObj, agentSpelling = rawAgent, "agent"
		delete(root, "agent")
		changed = true
	}

	rawAgents, hasAgents := root["agents"]
	if agentRawObj != nil && hasAgents {
		if !bytes.Equal(bytes.TrimSpace(agentRawObj), bytes.TrimSpace(rawAgents)) {
			// agentSpelling, not "agent": the block may have arrived under
			// opencode's older name, and a refusal naming a key the file
			// does not contain sends somebody looking for text that is not
			// there.
			refusals = append(refusals, refusal{
				path: agentSpelling,
				msg:  fmt.Sprintf(`%s and agents are two spellings of the same block; keep one of them`, agentSpelling),
			})
		}
	}

	if agentRawObj != nil {
		var agentsMap map[string]json.RawMessage
		if json.Unmarshal(agentRawObj, &agentsMap) == nil {
			aNames := sortedKeys(agentsMap)
			for _, aName := range aNames {
				var aEntry map[string]json.RawMessage
				if json.Unmarshal(agentsMap[aName], &aEntry) != nil || aEntry == nil {
					continue
				}

				// Refusals for agent keys
				if _, ok := aEntry["tools"]; ok {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.tools", aName),
						msg:  fmt.Sprintf(`agent %q: tools is a set of per-tool on/off switches, and localcode's per-agent tools is a closed allowlist — the two cannot be converted without changing what this file says. Write the restriction as a top-level "permission" map if it is meant for every agent; localcode has no per-agent permission.`, aName),
					})
				}
				if _, ok := aEntry["permission"]; ok {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.permission", aName),
						msg:  fmt.Sprintf(`agent %q: permission sets rules for this agent alone, and localcode's permission rules are daemon-wide — folding them in would apply them to every agent. Move them to the top-level "permission", which localcode already reads, if that is what you mean.`, aName),
					})
				}
				if _, ok := aEntry["disable"]; ok {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.disable", aName),
						msg:  fmt.Sprintf(`agent %q: disable is true, and localcode has no switch that turns an agent off — an agent named here is available to /agent, to Tab, and to delegation. Remove the entry instead.`, aName),
					})
				}
				if rawM, ok := aEntry["mode"]; ok {
					var mStr string
					_ = json.Unmarshal(rawM, &mStr)
					if mStr == "all" {
						delete(aEntry, "mode")
					} else {
						refusals = append(refusals, refusal{
							path: fmt.Sprintf("agent.%s.mode", aName),
							msg:  fmt.Sprintf(`agent %q: mode is %q, and localcode makes every agent in this file both switchable (/agent, Tab) and delegatable (the Task tool) — the half this mode excludes would still be available.`, aName, mStr),
						})
					}
				}
				if _, ok := aEntry["hidden"]; ok {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.hidden", aName),
						msg:  fmt.Sprintf(`agent %q: hidden asks for this agent to stay out of the menu a person picks from while staying delegatable, and localcode keeps one roster that both the person and the model choose from — /agent, Tab, the /model picker, the Web UI dropdown, the Debate reviewer list. It cannot be hidden from one and not the other. Remove it.`, aName),
					})
				}
				rawSteps, hasSteps := aEntry["steps"]
				rawMaxSteps, hasMaxSteps := aEntry["maxSteps"]
				if hasSteps && hasMaxSteps && !bytes.Equal(bytes.TrimSpace(rawSteps), bytes.TrimSpace(rawMaxSteps)) {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.steps", aName),
						msg:  fmt.Sprintf(`agent %q: steps and maxSteps disagree; keep one of them`, aName),
					})
				} else if hasSteps {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.steps", aName),
						msg:  fmt.Sprintf(`agent %q: steps caps this agent at %s tool-using iterations, and localcode has no per-turn iteration limit — the turn would keep going until the model stops or you interrupt it.`, aName, string(rawSteps)),
					})
				} else if hasMaxSteps {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.maxSteps", aName),
						msg:  fmt.Sprintf(`agent %q: maxSteps caps this agent at %s tool-using iterations (opencode's older spelling of steps), and localcode has no per-turn iteration limit — the turn would keep going until the model stops or you interrupt it.`, aName, string(rawMaxSteps)),
					})
				}
				if rawV, ok := aEntry["variant"]; ok {
					var vStr string
					_ = json.Unmarshal(rawV, &vStr)
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.variant", aName),
						msg:  fmt.Sprintf(`agent %q: variant is %q, and localcode has no variants — what a variant means lives in opencode's built-in table or in a provider's variants block, neither of which localcode reads. Set the profile's effort instead (off, low, medium, high, xhigh).`, aName, vStr),
					})
				}
				if _, ok := aEntry["options"]; ok {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("agent.%s.options", aName),
						msg:  fmt.Sprintf(`agent %q: options carries settings localcode cannot read — an agent's model settings live in its profile (max_tokens, temperature, top_p, top_k, effort). Move what you need there, or remove it.`, aName),
					})
				}
				if _, ok := aEntry["color"]; ok {
					ignored = append(ignored, fmt.Sprintf("agent.%s.color", aName))
					delete(aEntry, "color")
				}

				// Profile synthesis for agent
				hasAgentModel := false
				var agentModelStr string
				if rawAM, ok := aEntry["model"]; ok {
					hasAgentModel = true
					_ = json.Unmarshal(rawAM, &agentModelStr)
					delete(aEntry, "model")
				}
				hasTemp := false
				var tempVal float64
				if rawTemp, ok := aEntry["temperature"]; ok {
					hasTemp = true
					_ = json.Unmarshal(rawTemp, &tempVal)
					delete(aEntry, "temperature")
				}
				hasTopP := false
				var topPVal float64
				if rawTP, ok := aEntry["top_p"]; ok {
					hasTopP = true
					_ = json.Unmarshal(rawTP, &topPVal)
					delete(aEntry, "top_p")
				}

				if hasAgentModel || hasTemp || hasTopP {
					profName := "opencode:agent:" + aName
					if profilesMap[profName] != nil {
						refusals = append(refusals, refusal{
							path: fmt.Sprintf("agent.%s.model", aName),
							msg:  fmt.Sprintf(`agent %q wants to create profile %q, but a hand-written profile already holds that name; rename yours`, aName, profName),
						})
					}
					var provKey, wireModel string
					var cw, mt int
					if hasAgentModel {
						before, after, found := strings.Cut(agentModelStr, "/")
						if !found {
							refusals = append(refusals, refusal{
								path: fmt.Sprintf("agent.%s.model", aName),
								msg:  fmt.Sprintf(`agent %q: model is %q with no provider in front of it, and localcode does not look models up in a catalogue to find out who serves them.`, aName, agentModelStr),
							})
						} else if providersMap[before] == nil {
							refusals = append(refusals, refusal{
								path: fmt.Sprintf("agent.%s.model", aName),
								msg:  fmt.Sprintf(`agent %q: model is %q, and %q is not a provider this config defines.`, aName, agentModelStr, before),
							})
						} else {
							provKey = before
							wireModel = after
							if facts, ok := providerModelFacts[provKey][after]; ok {
								if facts.wireID != "" {
									wireModel = facts.wireID
								}
								cw = facts.contextWindow
								mt = facts.maxTokens
							}
						}
					} else {
						// Inherit provider and model from default profile
						if curDefaultProfile != "" && profilesMap[curDefaultProfile] != nil {
							var defProf Profile
							_ = json.Unmarshal(profilesMap[curDefaultProfile], &defProf)
							provKey = defProf.Provider
							wireModel = defProf.Model
							cw = defProf.ContextWindow
							mt = defProf.MaxTokens
						}
					}

					// Nothing to attach the agent's settings to. That is
					// the ordinary shape of a project opencode.json whose
					// model comes from the global file: this function sees
					// one file at a time, so the default it would inherit
					// from is not in front of it.
					//
					// So the agent keeps working — it takes the merged
					// default, the way an agent that named no profile
					// always has — and what could not be applied is said
					// rather than synthesised into a profile with no
					// provider in it, which is what used to happen and
					// what came back as `profile "opencode:agent:writer"
					// references unknown provider ""`.
					if provKey == "" {
						if hasTemp {
							ignored = append(ignored, fmt.Sprintf("agent.%s.temperature", aName))
						}
						if hasTopP {
							ignored = append(ignored, fmt.Sprintf("agent.%s.top_p", aName))
						}
						if curDefaultProfile != "" {
							aEntry["profile"], _ = json.Marshal(curDefaultProfile)
						}
						b, _ := json.Marshal(aEntry)
						agentsMap[aName] = b
						continue
					}

					prof := Profile{
						Provider:      provKey,
						Model:         wireModel,
						ContextWindow: cw,
						MaxTokens:     mt,
					}
					if hasTemp {
						prof.Temperature = tempVal
					}
					if hasTopP {
						prof.TopP = &topPVal
					}
					profBytes, _ := json.Marshal(prof)
					profilesMap[profName] = profBytes
					synthesised = append(synthesised, profName)
					aEntry["profile"], _ = json.Marshal(profName)
				} else {
					if curDefaultProfile != "" {
						aEntry["profile"], _ = json.Marshal(curDefaultProfile)
					}
				}

				b, _ := json.Marshal(aEntry)
				agentsMap[aName] = b
			}
			root["agents"], _ = json.Marshal(agentsMap)
			changed = true
		}
	}

	// Provider blacklist and whitelist checks against all profiles
	for _, pName := range pNames {
		blList := providerBlacklists[pName]
		if len(blList) > 0 {
			blSet := listedModels(blList, providerModelFacts[pName])
			for _, profName := range sortedKeys(profilesMap) {
				var p Profile
				if json.Unmarshal(profilesMap[profName], &p) == nil && p.Provider == pName && blSet[p.Model] {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("provider.%s.blacklist", pName),
						msg: fmt.Sprintf(`provider %q.blacklist hides %s, and profile %q is that model on that provider. `+
							`localcode's /model lists profiles and cannot hide one — remove the profile, or the blacklist entry.`,
							pName, namedAs(blList, providerModelFacts[pName], p.Model), profName),
					})
				}
			}
		}
		wlList := providerWhitelists[pName]
		if len(wlList) > 0 {
			wlSet := listedModels(wlList, providerModelFacts[pName])
			for _, profName := range sortedKeys(profilesMap) {
				var p Profile
				if json.Unmarshal(profilesMap[profName], &p) == nil && p.Provider == pName && !wlSet[p.Model] {
					refusals = append(refusals, refusal{
						path: fmt.Sprintf("provider.%s.whitelist", pName),
						msg: fmt.Sprintf(`provider %q.whitelist keeps only the models it lists, and profile %q is %q on that provider, which it does not list. `+
							`localcode's /model lists profiles and cannot hide one — remove the profile, or add the model.`,
							pName, profName, p.Model),
					})
				}
			}
		}
	}

	// Write back profiles if synthesised profiles were generated
	if len(synthesised) > 0 {
		b, _ := json.Marshal(profilesMap)
		root["profiles"] = b
		changed = true
	}

	if len(refusals) > 0 {
		sort.Slice(refusals, func(i, j int) bool {
			return refusals[i].path < refusals[j].path
		})
		msgs := make([]string, len(refusals))
		for i, r := range refusals {
			msgs[i] = r.msg
		}
		return Normalized{}, errors.New(strings.Join(msgs, "\n"))
	}

	sort.Strings(ignored)

	if !changed {
		return Normalized{JSON: raw, Ignored: ignored, Synthesised: synthesised}, nil
	}

	out, err := json.Marshal(root)
	if err != nil {
		return Normalized{}, err
	}
	return Normalized{JSON: out, Ignored: ignored, Synthesised: synthesised}, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedUnion(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var res []string
	for _, k := range a {
		if !seen[k] {
			seen[k] = true
			res = append(res, k)
		}
	}
	for _, k := range b {
		if !seen[k] {
			seen[k] = true
			res = append(res, k)
		}
	}
	sort.Strings(res)
	return res
}
