package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDaemonSessionGroupsAPI(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")

	// 1. Initial GET /api/sessions/groups returns empty list.
	{
		req := httptest.NewRequest("GET", "/api/sessions/groups", nil)
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/sessions/groups code = %d, want 200", rec.Code)
		}
		var body map[string][]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal groups response: %v", err)
		}
		if len(body["names"]) != 0 {
			t.Fatalf("initial groups = %v, want empty", body["names"])
		}
	}

	// 2. POST /api/sessions/groups with invalid names returns 400 and error message.
	{
		reqBody := `{"names": ["  invalid  "]}`
		req := httptest.NewRequest("POST", "/api/sessions/groups", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/sessions/groups with invalid name code = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "invalid") {
			t.Errorf("error response %q does not mention 'invalid'", rec.Body.String())
		}
	}

	// 3. POST /api/sessions/groups sets groups.
	{
		reqBody := `{"names": ["work", "personal"]}`
		req := httptest.NewRequest("POST", "/api/sessions/groups", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /api/sessions/groups code = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var body map[string][]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal groups response: %v", err)
		}
		if len(body["names"]) != 2 || body["names"][0] != "work" || body["names"][1] != "personal" {
			t.Fatalf("saved groups = %v, want [work, personal]", body["names"])
		}
	}

	// Create test sessions.
	sess1, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true)
	if err != nil {
		t.Fatalf("create session s1: %v", err)
	}
	if sess1.Group != "" {
		t.Errorf("new session group = %q, want empty", sess1.Group)
	}

	// 4. POST /api/sessions/{id}/group with nonexistent group returns 400.
	{
		reqBody := `{"group": "unknown"}`
		req := httptest.NewRequest("POST", "/api/sessions/s1/group", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/sessions/s1/group nonexistent code = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "unknown") {
			t.Errorf("error %q does not name 'unknown'", rec.Body.String())
		}
	}

	// 5. POST /api/sessions/{id}/group with non-existent session returns 404.
	{
		reqBody := `{"group": "work"}`
		req := httptest.NewRequest("POST", "/api/sessions/nosuchsession/group", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("POST nonexistent session code = %d, want 404", rec.Code)
		}
	}

	// 6. POST /api/sessions/{id}/group assigns session to group.
	{
		reqBody := `{"group": "work"}`
		req := httptest.NewRequest("POST", "/api/sessions/s1/group", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /api/sessions/s1/group code = %d, want 200", rec.Code)
		}

		sess, err := d.Loop.Store.Get("s1")
		if err != nil {
			t.Fatalf("Get s1: %v", err)
		}
		if sess.Group != "work" {
			t.Errorf("s1.Group = %q, want 'work'", sess.Group)
		}
	}

	// 7. GET /api/sessions includes group field on session row.
	{
		req := httptest.NewRequest("GET", "/api/sessions", nil)
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/sessions code = %d, want 200", rec.Code)
		}
		var rows []map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("unmarshal sessions: %v", err)
		}
		if len(rows) != 1 || rows[0]["group"] != "work" {
			t.Fatalf("rows = %+v, want session with group=work", rows)
		}
	}

	// 8. POST /api/sessions/{id}/group with empty group string ungroups session.
	{
		reqBody := `{"group": ""}`
		req := httptest.NewRequest("POST", "/api/sessions/s1/group", bytes.NewBufferString(reqBody))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /api/sessions/s1/group ungroup code = %d, want 200", rec.Code)
		}

		sess, err := d.Loop.Store.Get("s1")
		if err != nil {
			t.Fatalf("Get s1: %v", err)
		}
		if sess.Group != "" {
			t.Errorf("s1.Group = %q, want empty", sess.Group)
		}
	}

	// Reassign to "work" for archive check.
	if _, err := d.Loop.Store.SetSessionGroup("s1", "work"); err != nil {
		t.Fatalf("SetSessionGroup: %v", err)
	}

	// 9. Archiving session clears group.
	{
		req := httptest.NewRequest("POST", "/api/sessions/s1/archive", nil)
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("archive s1 code = %d, want 200", rec.Code)
		}

		sess, err := d.Loop.Store.Get("s1")
		if err != nil {
			t.Fatalf("Get s1: %v", err)
		}
		if sess.Group != "" {
			t.Errorf("archived s1.Group = %q, want empty", sess.Group)
		}

		// Group modification on archived session is refused with 400.
		reqGroup := httptest.NewRequest("POST", "/api/sessions/s1/group", bytes.NewBufferString(`{"group": "work"}`))
		recGroup := httptest.NewRecorder()
		d.Handler().ServeHTTP(recGroup, reqGroup)
		if recGroup.Code != http.StatusBadRequest {
			t.Fatalf("POST group on archived session code = %d, want 400", recGroup.Code)
		}
	}
}

// A group called "session" is the case that decides whether the two
// refusals are told apart by what they are or by what they say. Naming a
// group that does not exist is the caller's mistake, a 400; naming a
// session that does not exist is a 404. Both refusals mention a session
// and say "not found" when the group is called that, so reading the
// message cannot separate them — and a browser that got a 404 here would
// conclude the conversation it is looking at had been deleted.
func TestUnknownGroupIs400EvenWhenItIsCalledSession(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")

	sess, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	post := func(path, body string) int {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	if code := post("/api/sessions/"+sess.ID+"/group", `{"group":"session"}`); code != http.StatusBadRequest {
		t.Errorf("no such group %q = %d, want 400", "session", code)
	}
	if code := post("/api/sessions/nosuchsession/group", `{"group":""}`); code != http.StatusNotFound {
		t.Errorf("no such session = %d, want 404", code)
	}

	// And once the group exists, the same request is accepted — so the 400
	// above was about the group, not about the word.
	if code := post("/api/sessions/groups", `{"names":["session"]}`); code != http.StatusOK {
		t.Fatalf("creating a group called %q = %d, want 200", "session", code)
	}
	if code := post("/api/sessions/"+sess.ID+"/group", `{"group":"session"}`); code != http.StatusOK {
		t.Errorf("joining a group called %q = %d, want 200", "session", code)
	}
}
