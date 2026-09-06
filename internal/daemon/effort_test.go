package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The reasoning level over HTTP: what is in force, what the model tells
// apart, and what changing it does to the other clients.

func TestTheEffortEndpointSaysWhatTheModelTellsApart(t *testing.T) {
	d, srv := archiveDaemon(t)
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/sessions/s1/effort")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET effort: %d", resp.StatusCode)
	}
	var view struct {
		Model, Level, Source, Note string
		Levels                     []string
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Model != "test-model" {
		t.Errorf("model = %q, want the profile's", view.Model)
	}
	if view.Source != "unset" || view.Level != "" {
		t.Errorf("a new conversation reports level %q from %q, want unset", view.Level, view.Source)
	}
	if len(view.Levels) == 0 {
		t.Error("no levels offered, so a client has nothing to draw")
	}
	if view.Note == "" {
		t.Error("no note, so a client cannot say what the level reaches on this model")
	}
}

func TestSettingTheEffortSticksAndIsAnnounced(t *testing.T) {
	d, srv := archiveDaemon(t)
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	resp := post(t, srv, "/api/sessions/s1/effort", `{"level":"high"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST effort: %d", resp.StatusCode)
	}
	resp.Body.Close()

	if got := d.Loop.EffortView("s1"); got.Level != "high" || got.Source != "session" {
		t.Errorf("after setting: level %q from %q, want high from session", got.Level, got.Source)
	}
	// And the conversation's own log carries it, so a client that opens
	// the session later replays the answer rather than asking for it.
	evs, err := d.Loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var announced bool
	for _, ev := range evs {
		if ev.Type == "effort.changed" {
			announced = true
			if ev.Data["level"] != "high" {
				t.Errorf("the event says level %v", ev.Data["level"])
			}
			if _, ok := ev.Data["levels"]; !ok {
				t.Error("the event does not carry the levels a client should offer")
			}
		}
	}
	if !announced {
		t.Error("nothing in the log says the level changed")
	}

	// Clearing it hands the answer back to the profile.
	resp = post(t, srv, "/api/sessions/s1/effort", `{"level":""}`)
	resp.Body.Close()
	if got := d.Loop.EffortView("s1"); got.Source == "session" {
		t.Errorf("clearing left the conversation's own answer in place: %+v", got)
	}
}

// A level this model does not tell apart is refused rather than stored:
// it would show in the control and never be true of the request.
func TestALevelTheModelCannotTellApartIsRefused(t *testing.T) {
	d, srv := archiveDaemon(t)
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// The test daemon's profile is an OpenAI-compatible model that is not
	// muse, so the field's vocabulary stops at high.
	resp := post(t, srv, "/api/sessions/s1/effort", `{"level":"xhigh"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST an impossible level: %d, want 400", resp.StatusCode)
	}
	if got := d.Loop.EffortView("s1"); got.Level == "xhigh" {
		t.Error("the refused level was stored anyway")
	}
}

func TestTheEffortOfAnUnknownSessionIs404(t *testing.T) {
	_, srv := archiveDaemon(t)
	resp, err := http.Get(srv.URL + "/api/sessions/nope/effort")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// A fork is a verbatim copy, and how hard the model is asked to think is
// part of what it copies. Agent and workspace already came over; a fork
// that quietly reasoned less than the conversation it was taken from was
// a difference nobody asked for and nothing showed.
func TestAForkCarriesTheLevelsOver(t *testing.T) {
	d, srv := archiveDaemon(t)
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Loop.Store.SetEffort("s1", "test-model", "high"); err != nil {
		t.Fatal(err)
	}
	// And the shape a conversation from before this was per model has.
	if _, err := d.Loop.Store.SetEffort("s1", "", "low"); err != nil {
		t.Fatal(err)
	}

	resp := post(t, srv, "/api/sessions/s1/fork", `{}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("fork: %d", resp.StatusCode)
	}
	var forked struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&forked); err != nil {
		t.Fatal(err)
	}
	if got := d.Loop.Store.EffortFor(forked.ID, "test-model"); got != "high" {
		t.Errorf("the fork's level for the model = %q, want high", got)
	}
	if got := d.Loop.Store.EffortFor(forked.ID, "another-model"); got != "low" {
		t.Errorf("the fork lost the conversation-wide answer: %q", got)
	}
}
