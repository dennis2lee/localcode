package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func permissionDecisionDaemon(t *testing.T) (*Daemon, *httptest.Server) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(model.Close)
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	return d, srv
}

func postPermissionRule(t *testing.T, srv *httptest.Server, path, tool, match, decision string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"tool": tool, "match": match, "decision": decision})
	if err != nil {
		t.Fatalf("marshal rule: %v", err)
	}
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// A rule whose decision is not one of allow/ask/deny is refused at the API
// with 400 and stored nowhere: a typo'd prohibition must not become a
// persisted allow.
func TestUnknownPermissionDecisionIsRefusedAtTheAPI(t *testing.T) {
	for _, bad := range []string{"denied", "DENY", "deny "} {
		d, srv := permissionDecisionDaemon(t)
		status, reply := postPermissionRule(t, srv, "/api/permissions/rules", "bash", "rm *", bad)
		if status != http.StatusBadRequest {
			t.Errorf("decision %q: POST status = %d, want 400", bad, status)
		}
		for _, want := range []string{bad, "allow", "ask", "deny"} {
			if !strings.Contains(reply, want) {
				t.Errorf("decision %q: refusal body %q does not name %q", bad, reply, want)
			}
		}
		if _, rules := d.Loop.Config.PermissionsSnapshot(); len(rules) != 0 {
			t.Errorf("decision %q: refused rule reached the live config: %v", bad, rules)
		}
	}
}

// The remove endpoint takes the same struct, so it refuses unknown
// decisions the same way rather than touching the config.
func TestUnknownPermissionDecisionIsRefusedAtTheRemoveAPI(t *testing.T) {
	_, srv := permissionDecisionDaemon(t)
	status, _ := postPermissionRule(t, srv, "/api/permissions/rules/remove", "bash", "rm *", "denied")
	if status != http.StatusBadRequest {
		t.Errorf("POST status = %d, want 400", status)
	}
}

// The three valid decisions still store through both endpoints: the
// refusal above must not narrow what a user can actually write.
func TestKnownPermissionDecisionsAreStillStored(t *testing.T) {
	d, srv := permissionDecisionDaemon(t)
	for _, decision := range []string{"allow", "ask", "deny"} {
		status, _ := postPermissionRule(t, srv, "/api/permissions/rules", "bash", "rule-"+decision+" *", decision)
		if status != http.StatusNoContent {
			t.Errorf("decision %q: POST status = %d, want 204", decision, status)
		}
	}
	if _, rules := d.Loop.Config.PermissionsSnapshot(); len(rules["bash"]) != 3 {
		t.Errorf("live config holds %d bash rules, want 3 (one per valid decision)", len(rules["bash"]))
	}
	status, _ := postPermissionRule(t, srv, "/api/permissions/rules/remove", "bash", "rule-deny *", "deny")
	if status != http.StatusNoContent {
		t.Errorf("remove POST status = %d, want 204", status)
	}
	if _, rules := d.Loop.Config.PermissionsSnapshot(); len(rules["bash"]) != 2 {
		t.Errorf("live config holds %d bash rules after remove, want 2", len(rules["bash"]))
	}
}
