package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// After an agent switch the daemon announces the new agent's model view,
// the way it already announces effort: the server keeps one model choice
// per agent, so only it can say which choice is in force for the agent now
// current. Both clients redraw from the announced event.
//
// These drive the real HTTP routes through d.Handler() with a recorder, so
// no test server is bound.
func switchAgent(t *testing.T, d *Daemon, id, agent string) {
	t.Helper()
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/sessions/"+id+"/agent",
		strings.NewReader(`{"agent":`+strconv.Quote(agent)+`}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST agent switch to %q = %d: %s", agent, rec.Code, rec.Body.String())
	}
}

func modelViewOf(t *testing.T, d *Daemon, id string) (agent, source, model string) {
	t.Helper()
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/model", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET model = %d: %s", rec.Code, rec.Body.String())
	}
	var view struct {
		Agent, Model, Source string
	}
	if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
		t.Fatalf("decode model view: %v", err)
	}
	return view.Agent, view.Source, view.Model
}

func lastModelChanged(t *testing.T, d *Daemon, id string) map[string]any {
	t.Helper()
	evs, err := d.Loop.Store.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var last map[string]any
	for _, ev := range evs {
		if ev.Type == "model.changed" {
			last = ev.Data
		}
	}
	return last
}

func TestSwitchingAgentsAnnouncesTheNewAgentsModelView(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// This conversation chose a model under general-purpose.
	if _, err := d.Loop.Store.SetSessionProfile("s1", "general-purpose", "balanced"); err != nil {
		t.Fatal(err)
	}

	switchAgent(t, d, "s1", "plan")

	// Plan has no choice of its own, so the announced view is the
	// profile's answer again rather than general-purpose's choice.
	last := lastModelChanged(t, d, "s1")
	if last == nil {
		t.Fatal("no model.changed after the switch: clients keep naming the agent just left")
	}
	if last["agent"] != "plan" {
		t.Errorf("the announced view belongs to %v, want the new agent", last["agent"])
	}
	if last["source"] != "agent" {
		t.Errorf("the announced view is from %v, want the profile's answer", last["source"])
	}

	// And the endpoint a client would ask agrees: the next turn runs on
	// plan's resolution, not the previous agent's choice.
	if agent, source, _ := modelViewOf(t, d, "s1"); agent != "plan" || source != "agent" {
		t.Errorf("GET model = agent %q from %q, want plan's profile answer", agent, source)
	}
}

// When the new agent has a choice of its own, the announced view carries
// it rather than the profile default or the previous agent's choice.
func TestSwitchingAgentsAnnouncesTheNewAgentsOwnChoice(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Loop.Store.SetSessionProfile("s1", "general-purpose", "balanced"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Loop.Store.SetSessionProfile("s1", "plan", "balanced"); err != nil {
		t.Fatal(err)
	}

	switchAgent(t, d, "s1", "plan")

	last := lastModelChanged(t, d, "s1")
	if last == nil {
		t.Fatal("no model.changed after the switch")
	}
	if last["agent"] != "plan" || last["source"] != "conversation" {
		t.Errorf("the announced view = %v, want plan's own choice", last)
	}
}
