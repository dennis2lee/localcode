package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"localcode/internal/browser"
)

// The panel's tab strip: "+" opens an empty tab and makes it the active
// one, a click on a tab switches to it, and its "x" closes it. The
// location answer lists every tab.
func TestThePanelOpensSwitchesAndClosesTabs(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := browser.Find(""); err != nil {
		t.Skip("no Chrome or Edge here")
	}
	_, srv, id := browserDaemon(t)
	base := srv.URL + "/api/sessions/" + id + "/browser"
	type tab struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	tabsOf := func(resp *http.Response) []tab {
		t.Helper()
		defer resp.Body.Close()
		var out struct {
			HasTab bool  `json:"has_tab"`
			Tabs   []tab `json:"tabs"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Tabs
	}
	call := func(method, url string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if resp := call(http.MethodPost, base+"/tabs"); resp.StatusCode != http.StatusOK {
		t.Fatalf("new tab = %d", resp.StatusCode)
	} else if got := tabsOf(resp); len(got) != 1 || !got[0].Active {
		t.Fatalf("after the first new tab: %+v", got)
	}
	resp := call(http.MethodPost, base+"/tabs")
	two := tabsOf(resp)
	if len(two) != 2 || two[0].Active || !two[1].Active {
		t.Fatalf("after the second new tab: %+v", two)
	}
	state := tabsOf(call(http.MethodGet, base))
	if len(state) != 2 || state[1].ID != two[1].ID {
		t.Fatalf("the location answer lists %+v", state)
	}

	if got := tabsOf(call(http.MethodPost, base+"/tabs/"+two[0].ID+"/activate")); len(got) != 2 || !got[0].Active || got[1].Active {
		t.Errorf("after switching to the first: %+v", got)
	}
	if resp := call(http.MethodPost, base+"/tabs/no-such-tab/activate"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("switching to a tab that is not there = %d", resp.StatusCode)
	}
	if got := tabsOf(call(http.MethodDelete, base+"/tabs/"+two[0].ID)); len(got) != 1 || got[0].ID != two[1].ID || !got[0].Active {
		t.Errorf("after closing the active one: %+v", got)
	}
	if resp := call(http.MethodDelete, base+"/tabs/"+two[0].ID); resp.StatusCode != http.StatusNotFound {
		t.Errorf("closing it again = %d", resp.StatusCode)
	}
	// The last one closed: an empty list, which the panel draws, not null,
	// which it would skip and leave the closed tab drawn until a poll.
	last := call(http.MethodDelete, base+"/tabs/"+two[1].ID)
	body, _ := io.ReadAll(last.Body)
	last.Body.Close()
	if !strings.Contains(string(body), `"tabs":[]`) {
		t.Errorf("closing the last tab answered %s", body)
	}
}
