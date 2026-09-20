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
		// The group is kept through archiving, so retrieving the
		// conversation later puts it back where it was.
		if sess.Group != "work" {
			t.Errorf("archived s1.Group = %q, want it kept as %q", sess.Group, "work")
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

// The wire format of a rename. The panel's rename button is the only thing
// that sends it, and nothing in the suite sent one over HTTP before — so
// the field name, its shape and its effect were all unguarded, and a
// rename that silently stopped carrying its sessions would have looked
// exactly like a rename that worked.
func TestRenameOverHTTPCarriesTheSessions(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")

	post := func(path, body string) (int, string) {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := post("/api/sessions/groups", `{"names":["work"]}`); code != http.StatusOK {
		t.Fatalf("create group = %d: %s", code, body)
	}
	sess, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if code, body := post("/api/sessions/"+sess.ID+"/group", `{"group":"work"}`); code != http.StatusOK {
		t.Fatalf("join group = %d: %s", code, body)
	}

	code, body := post("/api/sessions/groups", `{"names":["job"],"rename":{"from":"work","to":"job"}}`)
	if code != http.StatusOK {
		t.Fatalf("rename = %d: %s", code, body)
	}
	var reply struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("parse rename reply: %v", err)
	}
	if len(reply.Names) != 1 || reply.Names[0] != "job" {
		t.Errorf("reply names = %v, want [job]", reply.Names)
	}
	got, err := d.Loop.Store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Group != "job" {
		t.Errorf("session group after rename = %q, want %q: the rename did not carry it", got.Group, "job")
	}

	// And a rename the daemon cannot make sense of is refused rather than
	// half-applied.
	if code, _ := post("/api/sessions/groups", `{"names":["job"],"rename":{"from":"nosuch","to":"job"}}`); code != http.StatusBadRequest {
		t.Errorf("rename from a group that does not exist = %d, want 400", code)
	}
}

// A body with no names field must not be read as "the list is now empty".
// The route takes the whole list, so a client that forgot the field, or
// sent {} by mistake, would otherwise delete every group the person had —
// silently, with a 200.
func TestSetGroupsRefusesABodyWithNoNames(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")

	post := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/sessions/groups", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := post(`{"names":["work","personal"]}`); code != http.StatusOK {
		t.Fatalf("create groups = %d: %s", code, body)
	}

	for _, body := range []string{`{}`, `{"rename":{"from":"work","to":"job"}}`} {
		code, resp := post(body)
		if code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, code)
		}
		if !strings.Contains(resp, "names is required") {
			t.Errorf("POST %s answered %q, want it to say the field is required", body, resp)
		}
	}
	if got := d.Loop.Store.GetGroups(); len(got) != 2 {
		t.Errorf("groups after the refused calls = %v, want both still there", got)
	}

	// An explicitly empty list still means what it says.
	if code, body := post(`{"names":[]}`); code != http.StatusOK {
		t.Fatalf("empty list = %d: %s", code, body)
	}
	if got := d.Loop.Store.GetGroups(); len(got) != 0 {
		t.Errorf("groups after an explicit [] = %v, want none", got)
	}
}

// The empty reply is [] and not null. A browser holding the answer should
// not have to check which of the two it got, and a test that decodes into
// a Go slice cannot tell them apart — so this one reads the bytes.
func TestGroupsReplyIsAnArrayEvenWhenEmpty(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")

	req := httptest.NewRequest("GET", "/api/sessions/groups", nil)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET groups = %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := string(raw["names"]); got != "[]" {
		t.Errorf("names = %s, want []", got)
	}
}
