package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The window's folder picker is answered by the window itself, in front of
// the daemon behind its proxy: another site's page may not open it, and
// the window's own page still can.
func TestAnotherSiteCannotOpenTheWindowsFolderPicker(t *testing.T) {
	front, _, _, work, dialogs := windowOnAProxy(t)
	body, _ := json.Marshal(map[string]string{"start": work})
	browse := func(header map[string]string) int {
		t.Helper()
		req, err := http.NewRequest("POST", front+"/api/workspace/browse", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := browse(map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); code != http.StatusForbidden {
		t.Errorf("another site's request: %d, want 403", code)
	}
	if *dialogs.picked != "" {
		t.Fatalf("another site's page opened the folder picker at %q", *dialogs.picked)
	}
	own := strings.TrimPrefix(front, "http://")
	if code := browse(map[string]string{"Origin": "http://" + own, "Sec-Fetch-Site": "same-origin"}); code != http.StatusOK {
		t.Fatalf("the window's own request: %d, want 200", code)
	}
	if *dialogs.picked != work {
		t.Fatalf("the window's own request did not open the picker (picked %q)", *dialogs.picked)
	}
}
