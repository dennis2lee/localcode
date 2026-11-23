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

// sshFor answers with the conversation's id, or writes why there is none.
func (d *Daemon) sshFor(w http.ResponseWriter, r *http.Request, mustBeOn bool) (string, bool) {
	id := r.PathValue("id")
	if d.SSH == nil {
		writeError(w, http.StatusNotFound, errors.New("this daemon has no SSH"))
		return "", false
	}
	sess, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return "", false
	}
	if mustBeOn {
		if !d.Loop.Config.SSHLive() {
			writeError(w, http.StatusConflict, errors.New("SSH is off; turn it on in the settings window or with /ssh on"))
			return "", false
		}
		if refuseArchived(w, sess) {
			return "", false
		}
	}
	return id, true
}

// sshTerminal is sshFor plus the terminal the path names.
func (d *Daemon) sshTerminal(w http.ResponseWriter, r *http.Request, mustBeOn bool) (*sshterm.Terminal, bool) {
	id, ok := d.sshFor(w, r, mustBeOn)
	if !ok {
		return nil, false
	}
	t := d.SSH.Get(id, r.PathValue("term"))
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
			return refuse("a request to the SSH panel must be JSON")
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

// sshState is what the panel draws its tab strip and questions from.
func (d *Daemon) sshState(id string) map[string]any {
	terms := []sshterm.Info{}
	for _, t := range d.SSH.List(id) {
		terms = append(terms, t.Info())
	}
	return map[string]any{
		"enabled":   d.Loop.Config.SSHLive(),
		"terminals": terms,
		"wants":     d.SSH.WantsSeq(id),
	}
}

func (d *Daemon) handleSSHState(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.sshFor(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, d.sshState(id))
}

// sshFrameGap is the least time between two screens sent to one panel.
const sshFrameGap = 50 * time.Millisecond

// handleSSHStream streams the conversation's terminals to one panel.
func (d *Daemon) handleSSHStream(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.sshFor(w, r, false)
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
		if t := d.SSH.Get(id, shown); t != nil && viewer != "" {
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
		changed := d.SSH.Changed(id)
		state := d.sshState(id)
		if b, _ := json.Marshal(state); string(b) != string(lastState) {
			if !send("state", state) {
				return
			}
			lastState = b
		}
		if t := d.SSH.Get(id, shown); t != nil {
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
	id, ok := d.sshFor(w, r, true)
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
func (d *Daemon) handleSSHInput(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	t, ok := d.sshTerminal(w, r, true)
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
func (d *Daemon) handleSSHSize(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	t, ok := d.sshTerminal(w, r, false)
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
func (d *Daemon) handleSSHHold(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	t, ok := d.sshTerminal(w, r, false)
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
func (d *Daemon) handleSSHAnswer(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	t, ok := d.sshTerminal(w, r, false)
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
func (d *Daemon) handleSSHReconnect(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.sshFor(w, r, true)
	if !ok {
		return
	}
	t, err := d.SSH.Reconnect(id, r.PathValue("term"), sshterm.ByPerson)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminal": t.ID})
}

// handleSSHClose closes a terminal and takes it off the strip.
func (d *Daemon) handleSSHClose(w http.ResponseWriter, r *http.Request) {
	if !d.sshGuard(w, r) {
		return
	}
	id, ok := d.sshFor(w, r, false)
	if !ok {
		return
	}
	if !d.SSH.Remove(id, r.PathValue("term"), "closed in the SSH panel") {
		writeError(w, http.StatusNotFound, fmt.Errorf("no terminal %q", r.PathValue("term")))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSSHLive lists every live connection across conversations, for the
// settings window.
func (d *Daemon) handleSSHLive(w http.ResponseWriter, r *http.Request) {
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
	if d.SSH != nil {
		for _, t := range d.SSH.LiveTerminals() {
			info := t.Info()
			title := ""
			if s, err := d.Loop.Store.Get(t.Conv); err == nil {
				title = s.Title
			}
			target := info.Target
			if target == "" {
				target = info.Spec
			}
			out = append(out, row{Session: t.Conv, Title: title, ID: t.ID, Target: target, State: string(info.State)})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetSSH turns SSH on or off live and persists it. Off closes every
// connection.
func (d *Daemon) handleSetSSH(w http.ResponseWriter, r *http.Request) {
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
	d.announceSettings()
	resp := map[string]any{"ssh": req.Enabled, "applied": true, "persisted": true}
	if d.Broker.ConfigPath != "" {
		if err := config.SetSSHInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = fmt.Sprintf("applied for this run, but failed to persist to config.json: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// closeSSH closes the terminals of every session in ids.
func (d *Daemon) closeSSH(ids []string, reason string) {
	if d.SSH == nil {
		return
	}
	for _, id := range ids {
		d.SSH.CloseConversation(id, reason)
	}
}

// sshUpdateRefusal is "/update"'s answer while SSH connections are open:
// an update restarts the daemon, and a restart closes every connection,
// the shells and what runs in them included. Said once; asked again
// within two minutes, the update goes ahead. Called under updateMu.
func (d *Daemon) sshUpdateRefusal(now time.Time) error {
	open := d.liveSSHNames()
	if len(open) == 0 || now.Sub(d.updateConfirm) <= 2*time.Minute {
		return nil
	}
	d.updateConfirm = now
	return fmt.Errorf("updating restarts localcode, which closes %d SSH connection(s): %s. Run /update again within two minutes to update anyway",
		len(open), strings.Join(open, "; "))
}

// liveSSHNames names the live SSH connections, for an update that would
// close them.
func (d *Daemon) liveSSHNames() []string {
	if d.SSH == nil {
		return nil
	}
	var out []string
	for _, t := range d.SSH.LiveTerminals() {
		target := t.Info().Target
		if target == "" {
			target = t.Spec
		}
		out = append(out, target+" ("+t.Conv+")")
	}
	sort.Strings(out)
	return out
}
