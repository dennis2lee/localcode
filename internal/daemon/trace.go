package daemon

import (
	"net/http"
	"strconv"
)

// handleTrace returns the most recent structured turn records — what
// answered, what it cost, which tools ran and for how long, what was
// delegated and what had to be recovered from. See internal/trace.
//
// A tail rather than the whole file. The file is the record that lasts and
// is meant to be read with jq; this is for the question asked while a
// session is still open ("what is it actually doing?"), which needs the
// last few dozen lines and nothing older.
//
// An empty answer is the ordinary case with Smart Agent off: nothing is
// written then, which is a setting rather than a failure, so it is 200
// with no records rather than an error.
func (d *Daemon) handleTrace(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	records := d.Loop.Trace.Recent(limit, r.URL.Query().Get("session"), r.URL.Query().Get("trace"))
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": d.Loop.SmartAgentEnabled() && d.Loop.Trace != nil,
		"records": records,
	})
}
