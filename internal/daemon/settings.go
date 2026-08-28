package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"

	"localcode/internal/config"
	"localcode/internal/smart"
)

// handleGetSettings reports the daemon's current live "/config" settings
// (process-global, not per-session) — for a client that just opened to
// know the current state without waiting for a config.changed event.
func (d *Daemon) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	skip, rules := d.Loop.Config.PermissionsSnapshot()
	// auto_delegate_agent is empty when config.json has no auto_delegate
	// block. Turning the setting on in that state is legal but does nothing,
	// so clients report that rather than showing an "on" that silently
	// delegates no prompt at all.
	delegateAgent := ""
	delegateMatch := []string{}
	if block := d.Loop.Config.AutoDelegateSnapshot(); block != nil {
		delegateAgent = block.Agent
		if block.Match != nil {
			delegateMatch = block.Match
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auto_compact_enabled": d.Loop.AutoCompactEnabled(),
		"smart_agent":          d.Loop.SmartAgentEnabled(),
		"smart_agent_roster":   smart.Names(),
		"show_tps":             d.Loop.ShowTPS(),
		"auto_delegate":        d.Loop.AutoDelegateEnabled(),
		"auto_delegate_agent":  delegateAgent,
		"auto_delegate_match":  delegateMatch,
		"skip_permissions":     skip,
		"permission_rules":     rules,
		"can_edit_permissions": d.Broker.ConfigPath != "",
	})
}

// handleSetAutoDelegate changes auto-delegation live — the switch itself,
// which agent handles delegated prompts, and which prompts qualify — and,
// when a config.json path is known, persists it so the choice survives a
// restart. The runtime change lands first and is never rolled back by a
// persistence failure: a caller that asked for the setting to apply "right
// now" gets that, and the error tells them only the saving part failed.
//
// Every field is a pointer, so a caller can change one thing without
// restating the others: the status-bar pill sends only "enabled", the
// settings panel sends all three.
func (d *Daemon) handleSetAutoDelegate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool     `json:"enabled"`
		Agent   *string   `json:"agent"`
		Match   *[]string `json:"match"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validated before anything is applied, so a typo'd agent name can't
	// leave the setting half-changed. An empty agent is allowed: it means
	// "no target configured", which is how a client clears the block.
	if req.Agent != nil && *req.Agent != "" {
		if _, ok := d.Loop.Config.Agents[*req.Agent]; !ok {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown agent %q", *req.Agent))
			return
		}
	}
	// Delegating to the agent that's already running would recurse, and
	// delegateTarget refuses it at turn time — better to say so here than to
	// accept a setting that silently never fires.
	if req.Agent != nil && *req.Agent != "" && len(d.Loop.Config.Agents) < 2 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("auto-delegation needs at least two agents configured"))
		return
	}

	if req.Agent != nil || req.Match != nil {
		current := d.Loop.Config.AutoDelegateSnapshot()
		agent, match := "", []string(nil)
		if current != nil {
			agent, match = current.Agent, current.Match
		}
		if req.Agent != nil {
			agent = *req.Agent
		}
		if req.Match != nil {
			match = *req.Match
		}
		d.Loop.Config.SetAutoDelegateRuntime(agent, match)
		if d.Broker.ConfigPath != "" {
			if err := config.SetAutoDelegateTargetInFile(d.Broker.ConfigPath, agent, match); err != nil {
				http.Error(w, fmt.Sprintf("applied for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
				return
			}
		}
	}

	if req.Enabled != nil {
		d.Loop.SetAutoDelegateEnabled(*req.Enabled)
		if d.Broker.ConfigPath != "" {
			if err := config.SetAutoDelegateEnabledInFile(d.Broker.ConfigPath, *req.Enabled); err != nil {
				http.Error(w, fmt.Sprintf("applied for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
				return
			}
		}
	}
	// Once, after both halves: the target and the switch are one change
	// to a reader, and two events would make a panel redraw twice.
	d.announceSettings()
	w.WriteHeader(http.StatusNoContent)
}

// handleSetSmartAgent turns the Smart Agent bundle on or off live and,
// when a config.json path is known, persists it so the choice survives a
// restart. The runtime change lands first and is never rolled back by a
// persistence failure, so a caller that asked for it to apply now gets
// that.
//
// Which is why this one answers with a body rather than 204-or-500. The
// two outcomes are not success and failure, they are "applied and saved"
// and "applied but not saved", and an HTTP error for the second told the
// client the opposite of what had happened: every caller treats a failed
// request as a change that did not occur, so the settings panel put the
// checkbox back and the daemon went on running the state the box now
// denied. The switch decides which model answers and which tools an agent
// may call, so a client showing the wrong one is worse than a client
// showing an unsaved one.
//
// applied is therefore always true here, and persisted is the part that
// can fail. A client should render the switch from applied and the
// warning from persisted.
//
// Nothing is validated here because there is nothing to validate. Turning
// it on with no profiles configured is legal and inert — no profile means
// no specialist can be given a model to run on, so the roster comes out
// empty and the orchestration prompt is not added. GET /api/settings
// reports the roster alongside the switch so a client can say so.
func (d *Daemon) handleSetSmartAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.SetSmartAgentEnabled(req.Enabled)
	d.announceSettings()
	resp := map[string]any{
		"smart_agent": req.Enabled,
		"applied":     true,
		"persisted":   true,
	}
	if d.Broker.ConfigPath != "" {
		if err := config.SetSmartAgentInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = fmt.Sprintf("applied for this run, but failed to persist to config.json: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetSkipPermissions toggles skip_permissions at runtime and, when a
// config.json path is known (see Broker.ConfigPath, the same path "always
// allow" writes to), persists it so the setting survives a restart.
func (d *Daemon) handleSetSkipPermissions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.Config.SetSkipPermissionsRuntime(req.Enabled)
	d.announceSettings()
	if d.Broker.ConfigPath != "" {
		if err := config.SetSkipPermissionsInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			http.Error(w, fmt.Sprintf("saved for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// permissionRuleRequest is the body for both add and remove: which tool,
// and the exact rule (match pattern + decision) to add or remove.
type permissionRuleRequest struct {
	Tool     string `json:"tool"`
	Match    string `json:"match"`
	Decision string `json:"decision"`
}

func (d *Daemon) handleAddPermissionRule(w http.ResponseWriter, r *http.Request) {
	var req permissionRuleRequest
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil || req.Tool == "" || req.Match == "" || req.Decision == "" {
		http.Error(w, "tool, match, and decision are all required", http.StatusBadRequest)
		return
	}
	rule := config.PermissionRule{Match: req.Match, Decision: config.Decision(req.Decision)}
	d.Loop.Config.AddPermissionRuleRuntime(req.Tool, rule)
	if d.Broker.ConfigPath != "" {
		if err := config.AddPermissionRuleToFile(d.Broker.ConfigPath, req.Tool, rule); err != nil {
			http.Error(w, fmt.Sprintf("saved for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) handleRemovePermissionRule(w http.ResponseWriter, r *http.Request) {
	var req permissionRuleRequest
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil || req.Tool == "" || req.Match == "" || req.Decision == "" {
		http.Error(w, "tool, match, and decision are all required", http.StatusBadRequest)
		return
	}
	rule := config.PermissionRule{Match: req.Match, Decision: config.Decision(req.Decision)}
	d.Loop.Config.RemovePermissionRuleRuntime(req.Tool, rule)
	if d.Broker.ConfigPath != "" {
		if err := config.RemovePermissionRuleFromFile(d.Broker.ConfigPath, req.Tool, rule); err != nil {
			http.Error(w, fmt.Sprintf("saved for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
