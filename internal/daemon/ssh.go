package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"localcode/internal/config"
	"localcode/internal/sshterm"
)

// The SSH panel's side of the daemon.
//
// One stream per window: the conversation on screen, and the terminal its
// panel shows. It carries the conversation's terminals (state), the shown
// terminal's screen, and its scrollback as lines are added, numbered
// within the terminal's epoch so a reconnecting panel asks from where it
// was. Input, size, hold, answers, connect and close are POSTs.
//
// These routes put a shell on another machine behind them, so they refuse
// what a page from another site could send (sshGuard). They cannot tell
// the person's window from a local process: the daemon has no
// authentication, and a program running as the same user (a model's bash
// command included) can call them as the window does. Such a program can
// also run ssh itself with the same keys; docs/USAGE.md says so.

// The PowerShell panel's routes are these same ones under
// /api/sessions/{id}/powershell, over the PowerShell manager: its terminals
// are the SSH panel's, started on this computer instead of over a
// connection (internal/sshterm's local.go). Opening one is the one route
// of its own (handlePowerShellOpen), and it asks no host.

// termPanel is what differs between the two panels' routes.
type termPanel struct {
	// name is the panel's name in a refusal; command turns it on.
	name, command string
	manager       func(*Daemon) *sshterm.Manager
	live          func(*Daemon) bool
}

var (
	sshRoutes = &termPanel{
		name: "SSH", command: "/ssh on",
		manager: func(d *Daemon) *sshterm.Manager { return d.SSH },
		live:    func(d *Daemon) bool { return d.Loop.Config.SSHLive() },
	}
	powershellRoutes = &termPanel{
		name: "PowerShell", command: "/powershell on",
		manager: func(d *Daemon) *sshterm.Manager { return d.PowerShell },
		live:    func(d *Daemon) bool { return d.Loop.Config.PowerShellLive() },
	}
)

// on adapts a handler of either panel to the mux.
func (d *Daemon) on(p *termPanel, h func(*termPanel, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h(p, w, r) }
}

// termFor answers with the conversation's id, or writes why there is none.
func (d *Daemon) termFor(p *termPanel, w http.ResponseWriter, r *http.Request, mustBeOn bool) (string, bool) {
	id := r.PathValue("id")
	if p.manager(d) == nil {
		writeError(w, http.StatusNotFound, errors.New("this daemon has no "+p.name))
		return "", false
	}
	sess, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return "", false
	}
	if mustBeOn {
		if !p.live(d) {
			writeError(w, http.StatusConflict, fmt.Errorf("%s is off; turn it on in the settings window or with %s", p.name, p.command))
			return "", false
		}
		if refuseArchived(w, sess) {
			return "", false
		}
	}
	return id, true
}

// termTerminal is termFor plus the terminal the path names.
func (d *Daemon) termTerminal(p *termPanel, w http.ResponseWriter, r *http.Request, mustBeOn bool) (*sshterm.Terminal, bool) {
	id, ok := d.termFor(p, w, r, mustBeOn)
	if !ok {
		return nil, false
	}
	t := p.manager(d).Get(id, r.PathValue("term"))
	if t == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no terminal %q", r.PathValue("term")))
		return nil, false
	}
	return t, true
}

// sshGuard refuses a request a page from another site could have made, or
// one that came through a DNS name pointed at this machine (rebinding):
//
//   - a write must be JSON, which a cross-site form cannot send and a
//     cross-site fetch cannot send without a preflight this daemon does
//     not answer;
//   - Sec-Fetch-Site, when the browser sends it, must be same-origin or
//     none;
//   - Origin, when present, must name the host the request was sent to
//     (the scheme is not compared, so a TLS proxy in front still works);
//   - the host the request was sent to must be an address or localhost,
//     or the name the daemon was started to listen on.
func (d *Daemon) sshGuard(w http.ResponseWriter, r *http.Request) bool {
	return d.sameSiteGuard(w, r, "the SSH panel")
}

// sameSiteGuard is sshGuard's rule for any route that needs it; what
// names the thing being asked for, in the refusal.
func (d *Daemon) sameSiteGuard(w http.ResponseWriter, r *http.Request, what string) bool {
	refuse := func(why string) bool {
		writeError(w, http.StatusForbidden, errors.New("refused: "+why))
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		ct := r.Header.Get("Content-Type")
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = ct[:i]
		}
		if !strings.EqualFold(strings.TrimSpace(ct), "application/json") {
			return refuse("a request to " + what + " must be JSON")
		}
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
		return refuse("a request from another site")
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return refuse("a request from another origin")
		}
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host != "" && net.ParseIP(host) == nil && !strings.EqualFold(host, "localhost") && !strings.EqualFold(host, d.ListenHost) {
		return refuse("a request sent to " + host + ", a name this daemon was not started on")
	}
	return true
}

// termState is what the panel draws its tab strip and questions from.
func (d *Daemon) termState(p *termPanel, id string) map[string]any {
	m := p.manager(d)
	terms := []sshterm.Info{}
	for _, t := range m.List(id) {
		terms = append(terms, t.Info())
	}
	return map[string]any{
		"enabled":   p.live(d),
		"terminals": terms,
		"wants":     m.WantsSeq(id),
	}
}

func (d *Daemon) handleTermState(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.termFor(p, w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, d.termState(p, id))
}

// sshFrameGap is the least time between two screens sent to one panel.
const sshFrameGap = 50 * time.Millisecond

// handleSSHStream streams the conversation's terminals to one panel.
func (d *Daemon) handleTermStream(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.termFor(p, w, r, false)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	q := r.URL.Query()
	shown := q.Get("terminal")
	viewer := q.Get("viewer")
	epoch := q.Get("epoch")
	from, _ := strconv.Atoi(q.Get("from"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// Every frame is an unnamed event, {"kind": ..., "data": ...}, so a
	// page reads them all from one onmessage.
	send := func(kind string, v any) bool {
		b, err := json.Marshal(map[string]any{"kind": kind, "data": v})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	defer func() {
		if t := p.manager(d).Get(id, shown); t != nil && viewer != "" {
			t.ViewerSize(viewer, 0, 0)
		}
	}()

	ctx := r.Context()
	var lastState []byte
	var lastVersion uint64
	var lastEpoch string
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for ctx.Err() == nil {
		changed := p.manager(d).Changed(id)
		state := d.termState(p, id)
		if b, _ := json.Marshal(state); string(b) != string(lastState) {
			if !send("state", state) {
				return
			}
			lastState = b
		}
		if t := p.manager(d).Get(id, shown); t != nil {
			if t.Epoch != lastEpoch {
				// A new terminal under this id (a reconnect), or the
				// first look: its scrollback from where the panel was in
				// it, or its last thousand lines.
				if t.Epoch != epoch {
					from = -1
				}
				lastEpoch, lastVersion = t.Epoch, 0
			}
			if v := t.Version(); v != lastVersion {
				lastVersion = v
				var lines []sshterm.Line
				var at int
				if from < 0 {
					next := t.Info().ScrollNext
					lines, at = t.Scrollback(max(0, next-1000), 1000)
				} else {
					lines, at = t.Scrollback(from, 2000)
				}
				if len(lines) > 0 || from < 0 {
					if !send("scroll", map[string]any{"terminal": t.ID, "epoch": t.Epoch, "from": at, "lines": lines}) {
						return
					}
				}
				from = at + len(lines)
				if !send("screen", map[string]any{"terminal": t.ID, "epoch": t.Epoch, "screen": t.Snapshot()}) {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sshFrameGap):
		}
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(jsonBody(w, r)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return false
	}
	return true
}

// handleSSHConnect is the person opening a connection from the panel.
func (d *Daemon) handleSSHConnect(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.termFor(sshRoutes, w, r, true)
	if !ok {
		return
	}
	var req struct {
		Target string `json:"target"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := d.SSH.Open(id, strings.TrimSpace(req.Target), sshterm.ByPerson)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminal": t.ID})
}

// handleSSHInput is the person typing.
func (d *Daemon) handleTermInput(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	t, ok := d.termTerminal(p, w, r, true)
	if !ok {
		return
	}
	var req struct {
		Key   *sshterm.PersonKey `json:"key"`
		Text  string             `json:"text"`
		Paste string             `json:"paste"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := t.PersonInput(req.Key, req.Text, req.Paste); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSSHSize records the size a viewer's panel fits.
func (d *Daemon) handleTermSize(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	t, ok := d.termTerminal(p, w, r, false)
	if !ok {
		return
	}
	var req struct {
		Viewer string `json:"viewer"`
		Cols   int    `json:"cols"`
		Rows   int    `json:"rows"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Viewer == "" {
		writeError(w, http.StatusBadRequest, errors.New("no viewer"))
		return
	}
	t.ViewerSize(req.Viewer, req.Cols, req.Rows)
	w.WriteHeader(http.StatusNoContent)
}

// handleSSHHold holds a terminal for the person, or lets it go.
func (d *Daemon) handleTermHold(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	t, ok := d.termTerminal(p, w, r, false)
	if !ok {
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t.SetHold(req.On)
	w.WriteHeader(http.StatusNoContent)
}

// handleSSHAnswer is the person answering a terminal's question. A host
// key is accepted only with the fingerprint the question showed.
func (d *Daemon) handleTermAnswer(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	t, ok := d.termTerminal(p, w, r, false)
	if !ok {
		return
	}
	var req struct {
		Question    int      `json:"question"`
		Accept      bool     `json:"accept"`
		Values      []string `json:"values"`
		Fingerprint string   `json:"fingerprint"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	a := sshterm.Answer{Accept: req.Accept}
	for _, v := range req.Values {
		a.Values = append(a.Values, []byte(v))
	}
	if err := t.Answer(req.Question, a, req.Fingerprint); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSSHReconnect opens a new connection in an ended terminal's tab.
func (d *Daemon) handleTermReconnect(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.termFor(p, w, r, true)
	if !ok {
		return
	}
	t, err := p.manager(d).Reconnect(id, r.PathValue("term"), sshterm.ByPerson)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminal": t.ID})
}

// handleSSHClose closes a terminal and takes it off the strip.
func (d *Daemon) handleTermClose(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.termFor(p, w, r, false)
	if !ok {
		return
	}
	if !p.manager(d).Remove(id, r.PathValue("term"), "closed in the "+p.name+" panel") {
		writeError(w, http.StatusNotFound, fmt.Errorf("no terminal %q", r.PathValue("term")))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTermLive lists every live terminal across conversations, for the
// settings window.
func (d *Daemon) handleTermLive(p *termPanel, w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	type row struct {
		Session string `json:"session"`
		Title   string `json:"title"`
		ID      string `json:"id"`
		Target  string `json:"target"`
		State   string `json:"state"`
	}
	out := []row{}
	if m := p.manager(d); m != nil {
		for _, t := range m.LiveTerminals() {
			// The summary, not Info: a terminal busy drawing does not hold
			// up the settings window.
			sum := t.Summarize()
			title := ""
			if s, err := d.Loop.Store.Get(t.Conv); err == nil {
				title = s.Title
			}
			out = append(out, row{Session: t.Conv, Title: title, ID: t.ID, Target: sum.Target, State: string(sum.State)})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetSSH turns SSH on or off live and persists it. Off closes every
// connection.
func (d *Daemon) handleSetSSH(w http.ResponseWriter, r *http.Request) {
	// Off closes every connection: not something another site may do.
	if !d.sshGuard(w, r) {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.Config.SetSSHRuntime(req.Enabled)
	if !req.Enabled && d.SSH != nil {
		d.SSH.CloseAll("SSH was turned off", false)
	}
	seq, held := d.settingsChanged()
	resp := map[string]any{"ssh": held["ssh"], "seq": seq, "epoch": d.settingsEpoch, "applied": true, "persisted": true}
	if d.Broker.ConfigPath != "" {
		if err := config.SetSSHInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePowerShellOpen is the person starting a PowerShell from the panel.
func (d *Daemon) handlePowerShellOpen(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.termFor(powershellRoutes, w, r, true)
	if !ok {
		return
	}
	t, err := d.PowerShell.Open(id, "PowerShell", sshterm.ByPerson)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminal": t.ID})
}

// handleSetPowerShell turns the PowerShell panel on or off live and
// persists it. Off closes every PowerShell terminal. Saved where there is
// no PowerShell too, so a config.json shared with a Windows machine says
// what that machine does.
func (d *Daemon) handleSetPowerShell(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.Config.SetPowerShellRuntime(req.Enabled)
	if !req.Enabled && d.PowerShell != nil {
		d.PowerShell.CloseAll("PowerShell was turned off", false)
	}
	seq, held := d.settingsChanged()
	resp := map[string]any{"powershell": held["powershell"], "seq": seq, "epoch": d.settingsEpoch, "applied": true, "persisted": true}
	if d.Broker.ConfigPath != "" {
		if err := config.SetPowerShellInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = notPersisted(err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// closeSSH closes the terminals of every session in ids, SSH's and
// PowerShell's.
func (d *Daemon) closeSSH(ids []string, reason string) {
	for _, m := range []*sshterm.Manager{d.SSH, d.PowerShell} {
		if m == nil {
			continue
		}
		for _, id := range ids {
			m.CloseConversation(id, reason)
		}
	}
}

// sshUpdateRefusal is "/update"'s answer while SSH connections or
// PowerShell terminals are open: an update restarts the daemon, and a
// restart closes every one, the shells and what runs in them included.
// Said once; asked again within two minutes, the update goes ahead.
// Called under updateMu.
func (d *Daemon) sshUpdateRefusal(now time.Time) error {
	ssh, ps := d.liveNames(d.SSH), d.liveNames(d.PowerShell)
	if len(ssh)+len(ps) == 0 || now.Sub(d.updateConfirm) <= 2*time.Minute {
		return nil
	}
	d.updateConfirm = now
	var parts []string
	if len(ssh) > 0 {
		parts = append(parts, fmt.Sprintf("%d SSH connection(s): %s", len(ssh), strings.Join(ssh, "; ")))
	}
	if len(ps) > 0 {
		parts = append(parts, fmt.Sprintf("%d PowerShell terminal(s): %s", len(ps), strings.Join(ps, "; ")))
	}
	return fmt.Errorf("updating restarts localcode, which closes %s. Run /update again within two minutes to update anyway",
		strings.Join(parts, ", and "))
}

// liveNames names m's live terminals, for an update that would close
// them.
func (d *Daemon) liveNames(m *sshterm.Manager) []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, t := range m.LiveTerminals() {
		out = append(out, t.Summarize().Target+" ("+t.Conv+")")
	}
	sort.Strings(out)
	return out
}
