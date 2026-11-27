package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"localcode/internal/config"
	"localcode/internal/smart"
)

// handleGetSettings reports the daemon's current live "/config" settings
// (process-global, not per-session) — for a client that just opened to
// know the current state without waiting for a config.changed event.
//
// It carries the number of the last settings.changed, read under the same
// lock as the values, so a page can tell it from an announcement older or
// newer than what it read. See settingsChanged.
func (d *Daemon) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	d.settingsMu.Lock()
	skip, rules := d.Loop.Config.PermissionsSnapshot()
	delegateAgent, delegateMatch := d.autoDelegateTarget()
	resp := map[string]any{
		"seq":                  d.settingsSeq,
		"epoch":                d.settingsEpoch,
		"auto_compact_enabled": d.Loop.AutoCompactEnabled(),
		"auto_compact_percent": d.Loop.CompactPercent(),
		"keep_going":           d.Loop.KeepGoingEnabled(),
		"fold_thinking":        d.Loop.FoldThinkingEnabled(),
		"muse_profiles":        d.Loop.MuseProfiles(),
		"repeat_limit":         d.Loop.RepeatLimit(),
		"smart_agent":          d.Loop.SmartAgentEnabled(),
		"browser":              d.Loop.Config.BrowserLive(),
		"browser_available":    d.Browser != nil && d.Browser.Available(),
		"browser_import":       d.browserImport(),
		"ssh":                  d.Loop.Config.SSHLive(),
		"ssh_available":        d.SSH != nil,
		"powershell":           d.Loop.Config.PowerShellLive(),
		"powershell_available": d.PowerShell != nil,
		"orchestrate":          d.Loop.OrchestrateEnabled(),
		"model_invocable":      d.Loop.ModelInvocableEnabled(),
		"model_commands":       d.Loop.ModelCommandNames(),
		"orchestrate_roster":   smart.Names(),
		"show_tps":             d.Loop.ShowTPS(),
		"show_thinking":        d.Loop.ShowThinking(),
		"show_timestamps":      d.Loop.ShowTimestamps(),
		"auto_delegate":        d.Loop.AutoDelegateEnabled(),
		"auto_delegate_agent":  delegateAgent,
		"auto_delegate_match":  delegateMatch,
		"skip_permissions":     skip,
		"permission_rules":     rules,
		"can_edit_permissions": d.Broker.ConfigPath != "",
	}
	d.settingsMu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// autoDelegateTarget is the agent that answers delegated prompts and the
// patterns that qualify one, as a client is told them.
//
// The agent is empty when config.json has no auto_delegate block. Turning
// the setting on in that state is legal but does nothing, so clients
// report that rather than showing an "on" that silently delegates no
// prompt at all.
func (d *Daemon) autoDelegateTarget() (agent string, match []string) {
	match = []string{}
	if block := d.Loop.Config.AutoDelegateSnapshot(); block != nil {
		agent = block.Agent
		if block.Match != nil {
			match = block.Match
		}
	}
	return agent, match
}

// notPersisted is the "error" of a two-part answer whose save failed.
func notPersisted(err error) string {
	return fmt.Sprintf("applied for this run, but failed to persist to config.json: %v", err)
}

// handleSetAutoDelegate changes auto-delegation live — the switch itself,
// which agent handles delegated prompts, and which prompts qualify — and,
// when a config.json path is known, persists it so the choice survives a
// restart. The runtime change lands first and is never rolled back by a
// persistence failure: a caller that asked for the setting to apply "right
// now" gets that, and the answer tells them only the saving part failed.
//
// It answers in two parts, as handleSetSmartAgent does and for the same
// reason. The body carries the whole setting as the daemon now holds it,
// because a request may move one field of three.
//
// The target and the switch are written to config.json separately. A
// target that could not be written used to end the request there: a 500,
// the switch in the same body never applied, and nothing announced.
// Both are applied now whatever happens to either save, "persisted" is
// false when either failed, and the error names each one that did.
//
// Every field is a pointer, so a caller can change one thing without
// restating the others: the panel's checkbox sends only "enabled", its
// agent list only "agent", its patterns only "match".
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

	// What could not be written to config.json, one entry for each half.
	var unsaved []string
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
				unsaved = append(unsaved, fmt.Sprintf("agent and patterns: %v", err))
			}
		}
	}

	if req.Enabled != nil {
		d.Loop.SetAutoDelegateEnabled(*req.Enabled)
		if d.Broker.ConfigPath != "" {
			if err := config.SetAutoDelegateEnabledInFile(d.Broker.ConfigPath, *req.Enabled); err != nil {
				unsaved = append(unsaved, fmt.Sprintf("enabled: %v", err))
			}
		}
	}
	// Once, after both halves: the target and the switch are one change
	// to a reader, and two events would make a panel redraw twice.
	seq, held := d.settingsChanged()
	resp := map[string]any{
		"auto_delegate":       held["auto_delegate"],
		"auto_delegate_agent": held["auto_delegate_agent"],
		"auto_delegate_match": held["auto_delegate_match"],
		"seq":                 seq, "epoch": d.settingsEpoch,
		"applied":   true,
		"persisted": true,
	}
	if len(unsaved) > 0 {
		resp["persisted"] = false
		resp["error"] = notPersisted(errors.New(strings.Join(unsaved, "; ")))
	}
	writeJSON(w, http.StatusOK, resp)
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
	seq, held := d.settingsChanged()
	resp := map[string]any{
		"smart_agent": held["smart_agent"],
		"seq":         seq, "epoch": d.settingsEpoch,
		"applied":   true,
		"persisted": true,
	}
	if d.Broker.ConfigPath != "" {
		if err := config.SetSmartAgentInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetModelInvocable turns model-run commands on or off live and,
// when a config.json path is known, persists it. The same two-part answer
// as the two switches above.
//
// Only the switch. What it lets through is written down elsewhere — the
// built-ins in config.json's model_commands, a custom command's or a
// skill's own frontmatter — and a toggle that also edited those would
// turn "off for now" into "forget what I chose". GET /api/settings
// reports the resolved list alongside the switch, so a client can say
// what turning it on would actually reach rather than the endpoint
// refusing an empty one.
func (d *Daemon) handleSetModelInvocable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.SetModelInvocableEnabled(req.Enabled)
	seq, held := d.settingsChanged()
	resp := map[string]any{
		"model_invocable": held["model_invocable"],
		"seq":             seq, "epoch": d.settingsEpoch,
		"applied":   true,
		"persisted": true,
	}
	if d.Broker.ConfigPath != "" {
		if err := config.SetModelInvocableInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetOrchestrate turns the Orchestrate tool on or off live and, when
// a config.json path is known, persists it. The same two-part answer as
// handleSetSmartAgent above, for the same reason: applied and saved are
// different questions, and a client that treats an unsaved change as a
// refused one shows the opposite of the state the daemon is in.
//
// Nothing is validated here either. Turning it on with one agent
// configured is legal and inert, because a plan needs somewhere to
// delegate a stage to; GET /api/settings reports the roster alongside the
// switch so a client can say so rather than the endpoint refusing.
func (d *Daemon) handleSetOrchestrate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.SetOrchestrateEnabled(req.Enabled)
	seq, held := d.settingsChanged()
	resp := map[string]any{
		"orchestrate": held["orchestrate"],
		"seq":         seq, "epoch": d.settingsEpoch,
		"applied":   true,
		"persisted": true,
	}
	if d.Broker.ConfigPath != "" {
		if err := config.SetOrchestrateInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetKeepGoing turns the carry-on nudge on or off live and, when a
// config.json path is known, persists it. It backs the settings window's
// checkbox; "/keep-going" is the same switch from a prompt.
//
// Nothing here checks whether any profile runs a muse model, because
// that is not this endpoint's question: the switch is daemon state, and
// whether it reaches a given turn is decided per profile at turn time.
// The panel says the scope beside the checkbox instead.
//
// It answers in two parts, as handleSetSmartAgent does and for the same
// reason. A save that failed used to be a 500, which the settings window
// read as a change that had not happened: it put the box back and said
// "Not changed" while the daemon went on running the new value.
func (d *Daemon) handleSetKeepGoing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.SetKeepGoingEnabled(req.Enabled)
	seq, held := d.settingsChanged()
	resp := map[string]any{
		"keep_going": held["keep_going"],
		"seq":        seq, "epoch": d.settingsEpoch,
		"applied":   true,
		"persisted": true,
	}
	if d.Broker.ConfigPath != "" {
		if err := config.SetKeepGoingInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetFoldThinking turns the muse reasoning block on or off live
// and, when a config.json path is known, persists it. It does not check
// whether any profile runs a muse model: GET /api/settings reports
// muse_profiles, and the panel says it.
//
// It answers in two parts, as orchestrate does, rather than with a 500
// for a save that failed: the switch was applied either way, and an
// error status read as "not changed" left the box saying the opposite
// of the state the daemon was in.
func (d *Daemon) handleSetFoldThinking(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.SetFoldThinkingEnabled(req.Enabled)
	seq, held := d.settingsChanged()
	resp := map[string]any{
		"fold_thinking": held["fold_thinking"],
		"seq":           seq, "epoch": d.settingsEpoch,
		"applied":   true,
		"persisted": true,
	}
	if d.Broker.ConfigPath != "" {
		if err := config.SetFoldThinkingInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetRepeatLimit moves the repeat guard's ceiling; zero turns it
// off. Same shape as keep_going: applied live, then persisted where a
// config path is known, and answered in two parts.
//
// A limit out of range and a body that does not parse are still a 400,
// the one answer here that means nothing changed: both are refused
// before anything is applied.
func (d *Daemon) handleSetRepeatLimit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Limit int `json:"limit"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Limit < 0 || req.Limit > config.MaxRepeatLimit {
		http.Error(w, fmt.Sprintf("limit must be between 0 (off) and %d", config.MaxRepeatLimit), http.StatusBadRequest)
		return
	}
	d.Loop.SetRepeatLimit(req.Limit)
	seq, held := d.settingsChanged()
	resp := map[string]any{
		"repeat_limit": held["repeat_limit"],
		"seq":          seq, "epoch": d.settingsEpoch,
		"applied":   true,
		"persisted": true,
	}
	if d.Broker.ConfigPath != "" {
		if err := config.SetRepeatLimitInFile(d.Broker.ConfigPath, req.Limit); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetSkipPermissions toggles skip_permissions at runtime and, when a
// config.json path is known (see Broker.ConfigPath, the same path "always
// allow" writes to), persists it so the setting survives a restart.
//
// Answered in two parts, as handleSetSmartAgent is: this is the daemon's
// default for every conversation that has not answered for itself, and a
// 500 for a save that failed said a default that was in force was not.
func (d *Daemon) handleSetSkipPermissions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.Config.SetSkipPermissionsRuntime(req.Enabled)
	seq, held := d.settingsChanged()
	resp := map[string]any{
		"skip_permissions": held["skip_permissions"],
		"seq":              seq, "epoch": d.settingsEpoch,
		"applied":   true,
		"persisted": true,
	}
	if d.Broker.ConfigPath != "" {
		if err := config.SetSkipPermissionsInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeRuleChange answers a rule that was added or removed, in two parts
// as the switches are. A rule decides what a tool call may do without
// asking, and it is in force from the moment it is applied, so a 500 for
// a save that failed left the permissions window listing rules the
// daemon was no longer running, or not listing one it was.
//
// The body carries every rule the daemon now holds, in the shape
// GET /api/settings reports them, so a client draws its list from that
// and not from what it believes it changed.
func (d *Daemon) writeRuleChange(w http.ResponseWriter, saveErr error) {
	_, rules := d.Loop.Config.PermissionsSnapshot()
	resp := map[string]any{
		"permission_rules": rules,
		"applied":          true,
		"persisted":        true,
	}
	if saveErr != nil {
		resp["persisted"] = false
		resp["error"] = notPersisted(saveErr)
	}
	writeJSON(w, http.StatusOK, resp)
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
	// Refused before anything is stored: an unknown decision would
	// otherwise persist a rule the registry cannot honour.
	if !config.ValidDecision(config.Decision(req.Decision)) {
		http.Error(w, fmt.Sprintf("unknown decision %q (want one of %s)", req.Decision, config.DecisionNames()), http.StatusBadRequest)
		return
	}
	rule := config.PermissionRule{Match: req.Match, Decision: config.Decision(req.Decision)}
	d.Loop.Config.AddPermissionRuleRuntime(req.Tool, rule)
	var saveErr error
	if d.Broker.ConfigPath != "" {
		saveErr = config.AddPermissionRuleToFile(d.Broker.ConfigPath, req.Tool, rule)
	}
	d.writeRuleChange(w, saveErr)
}

func (d *Daemon) handleRemovePermissionRule(w http.ResponseWriter, r *http.Request) {
	var req permissionRuleRequest
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil || req.Tool == "" || req.Match == "" || req.Decision == "" {
		http.Error(w, "tool, match, and decision are all required", http.StatusBadRequest)
		return
	}
	// Same struct as add, so the same refusal: removing with a decision
	// that could never have been stored must not reach the file either.
	if !config.ValidDecision(config.Decision(req.Decision)) {
		http.Error(w, fmt.Sprintf("unknown decision %q (want one of %s)", req.Decision, config.DecisionNames()), http.StatusBadRequest)
		return
	}
	rule := config.PermissionRule{Match: req.Match, Decision: config.Decision(req.Decision)}
	d.Loop.Config.RemovePermissionRuleRuntime(req.Tool, rule)
	var saveErr error
	if d.Broker.ConfigPath != "" {
		saveErr = config.RemovePermissionRuleFromFile(d.Broker.ConfigPath, req.Tool, rule)
	}
	d.writeRuleChange(w, saveErr)
}
