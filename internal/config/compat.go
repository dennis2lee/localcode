package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Normalized is what an opencode-shaped file becomes.
type Normalized struct {
	JSON    []byte   // localcode-shaped, ready for json.Unmarshal
	Ignored []string // opencode dotted paths accepted and not honoured, sorted
}

type refusal struct {
	path string
	msg  string
}

// NormalizeOpencode rewrites opencode's spellings into localcode's own.
// raw is the file's bytes after comments and {env:} have been handled.
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

	var refusals []refusal
	var ignored []string
	changed := false

	// Step 1: mcp -> mcp_servers
	// Both present -> refuse naming both.
	// Only mcp present -> rename to mcp_servers.
	_, hasMCP := root["mcp"]
	_, hasMCPServers := root["mcp_servers"]
	if hasMCP && hasMCPServers {
		refusals = append(refusals, refusal{
			path: "mcp",
			msg:  `mcp and mcp_servers are two spellings of the same block; keep one of them`,
		})
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
			return Normalized{}, fmt.Errorf(`"tools" must be an object of tool booleans: %w`, err)
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
					return Normalized{}, fmt.Errorf(
						`permission is %q, which is every tool, and "tools" names tools inside it; `+
							`write them as one permission block`, bare)
				}
				return Normalized{}, fmt.Errorf(`permission must be a decision string or an object of tool rules: %w`, err)
			}
		}

		mappedPerms := make(map[string]string)
		for k, rawVal := range toolsMap {
			var val bool
			if err := json.Unmarshal(rawVal, &val); err != nil {
				return Normalized{}, fmt.Errorf(`tools %q: expected boolean, got %s`, k, string(rawVal))
			}

			targetTool := k
			if k == "write" || k == "apply_patch" {
				targetTool = "edit"
			}

			decision := "allow"
			if !val {
				decision = "deny"
			}

			if existingPerms != nil {
				if _, collides := existingPerms[k]; collides {
					refusals = append(refusals, refusal{
						path: "tools." + k,
						msg:  fmt.Sprintf(`tool %q is configured in both "tools" and "permission"; keep one of them`, k),
					})
					continue
				}
				if targetTool != k {
					if _, collides := existingPerms[targetTool]; collides {
						refusals = append(refusals, refusal{
							path: "tools." + k,
							msg:  fmt.Sprintf(`tool %q is configured in both "tools" and "permission"; keep one of them`, targetTool),
						})
						continue
					}
				}
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

	if _, ok := root["snapshot"]; ok {
		refusals = append(refusals, refusal{
			path: "snapshot",
			msg:  `snapshot: false asks localcode not to record file snapshots, and localcode copies every file a turn edits before changing it so /rewind can put it back. There is no setting to turn that off. Remove the key, or accept that the copies are made.`,
		})
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
				msg: fmt.Sprintf(`share: %q asks for a session to be publishable to a share URL, and localcode has no sharing — nothing is ever published, `+
					`and nothing here will publish it for you. Remove the key, or set it to "disabled", which is what localcode does.`, mode),
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
		return Normalized{JSON: raw, Ignored: ignored}, nil
	}

	out, err := json.Marshal(root)
	if err != nil {
		return Normalized{}, err
	}
	return Normalized{JSON: out, Ignored: ignored}, nil
}
