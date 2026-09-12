package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Choosing a Smart Agent specialist by hand.
//
// The six specialists were delegatable and not selectable: GET
// /api/agents listed Config.Agents alone, so Tab, the Web UI menu and
// "localcode run --agent oracle" could not reach any of them. Nothing
// underneath was ever the obstacle — profileFor and agentConfig have
// always resolved a specialist by name — so the gap was the listing and
// the check beside it.

func agentNames(t *testing.T, d *Daemon) []AgentInfo {
	t.Helper()
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/agents = %d: %s", rec.Code, rec.Body)
	}
	var out []AgentInfo
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// With the switch on, the specialists are in the list; with it off they
// are not, which is the same rule delegation has always followed.
func TestTheSpecialistsAreOfferedWhileSmartAgentIsOn(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")

	d.Loop.SetSmartAgentEnabled(false)
	off := agentNames(t, d)
	for _, a := range off {
		if a.Builtin {
			t.Errorf("%q is offered with Smart Agent off", a.Name)
		}
	}

	d.Loop.SetSmartAgentEnabled(true)
	on := agentNames(t, d)
	if len(on) <= len(off) {
		t.Fatalf("%d agents with the switch on, %d with it off; the specialists are still missing", len(on), len(off))
	}
	var builtins int
	for _, a := range on {
		if !a.Builtin {
			continue
		}
		builtins++
		// A specialist runs on its own profile. Reporting the session's
		// model instead would be most of the cost of Smart Agent and none
		// of the benefit, and that is what a plain ResolveProfile does for
		// a name that is not in Config.Agents.
		if a.Model == "" {
			t.Errorf("specialist %q is listed with no model", a.Name)
		}
	}
	if builtins == 0 {
		t.Error("no specialist is marked as one, so a client cannot tell them apart")
	}
}

// And a name the listing just offered can actually be chosen.
func TestASpecialistCanBeSwitchedTo(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Loop.SetSmartAgentEnabled(true)

	var specialist string
	for _, a := range agentNames(t, d) {
		if a.Builtin {
			specialist = a.Name
			break
		}
	}
	if specialist == "" {
		t.Skip("this configuration produces no specialists")
	}

	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/agent",
		strings.NewReader(`{"agent":"`+specialist+`"}`))
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("switching to the specialist %q answered %d: %s", specialist, rec.Code, rec.Body)
	}
	if sess, err := d.Loop.Store.Get("s1"); err != nil {
		t.Fatal(err)
	} else if sess.Agent != specialist {
		t.Errorf("the session is on %q, want %q", sess.Agent, specialist)
	}
}

// A name that is neither is still refused, so widening the check did not
// turn it off.
func TestAnAgentThatDoesNotExistIsStillRefused(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Loop.SetSmartAgentEnabled(true)
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/agent", strings.NewReader(`{"agent":"nobody"}`))
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an agent that does not exist answered %d, want 400", rec.Code)
	}
}
