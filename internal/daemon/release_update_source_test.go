package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An http update source is reported as unverified beside the source, on
// the check, so the panel can say it on the offer. The mirror is plain
// http on loopback, which the new rule accepts; the flag is what the
// client renders, and it is asserted on the JSON a client receives.
func TestUpdateCheckMarksAnHTTPMirrorAsUnverified(t *testing.T) {
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One asset per platform, so AssetFor succeeds wherever this runs.
		w.Write([]byte("localcode-0.46.0-windows-amd64.msi\n" +
			"localcode-0.46.0-darwin-universal.tar.gz\n" +
			"localcode-0.46.0-linux-amd64.tar.gz\n"))
	}))
	defer mirror.Close()

	// No model server: the check never runs a turn, so the provider URL is
	// never dialled.
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.45.0"
	d.AllowUpdateInstall = true
	d.Loop.Config.UpdateURL = mirror.URL + "/dl/"
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	resp, err := http.Get(httpSrv.URL + "/api/update")
	if err != nil {
		t.Fatalf("GET /api/update: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode check reply: %v", err)
	}
	if body["checked"] != true {
		t.Fatalf("checked = %v (%v), want the loopback http mirror to answer", body["checked"], body["detail"])
	}
	if body["source"] != mirror.URL+"/dl/" {
		t.Errorf("source = %v, want the mirror address rather than an implied GitHub", body["source"])
	}
	if body["source_unverified"] != true {
		t.Errorf("source_unverified = %v, want true: an http address alone does not say the host was never authenticated", body["source_unverified"])
	}
	if body["available"] != true {
		t.Errorf("available = %v, want true (0.46.0 over 0.45.0)", body["available"])
	}
}

// The marker follows the scheme, not the host: https is never flagged,
// loopback or not, and the default GitHub source is never flagged either.
func TestOnlyAnHTTPSourceIsReportedAsUnverified(t *testing.T) {
	for _, tt := range []struct{ name, url string }{
		{"https loopback", "https://127.0.0.1:1/dl/"},
		{"https private name", "https://mirror.internal/dl/"},
		{"https public", "https://example.com/dl/"},
		{"no update_url", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDaemon(t, "http://127.0.0.1:1")
			d.Loop.Config.UpdateURL = tt.url
			if d.updateSourceUnverified() {
				t.Errorf("updateSourceUnverified() = true for %q, want false: only plain http is unverified", tt.url)
			}
		})
	}

	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Loop.Config.UpdateURL = "http://mirror/dl/"
	if !d.updateSourceUnverified() {
		t.Error("updateSourceUnverified() = false for an http mirror, want true")
	}
	d.Loop.Config.UpdateURL = "  http://mirror/dl/  "
	if !d.updateSourceUnverified() {
		t.Error("updateSourceUnverified() = false for an http mirror with surrounding whitespace, want true")
	}
}

// The TUI names the source in the /update reply, so the reply carries the
// sentence there too. No network is reached: a daemon that may not
// install refuses before it ever looks, which is the path that names the
// source.
func TestUpdateRefusalSaysAnHTTPMirrorIsUnverified(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.AllowUpdateInstall = false

	d.Loop.Config.UpdateURL = "http://mirror/dl/"
	_, err := d.SelfUpdate("S1")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "http://mirror/dl/") {
		t.Errorf("the refusal does not name the source: %v", err)
	}
	if !strings.Contains(err.Error(), httpSourceNote) {
		t.Errorf("the refusal names an http source without saying it is unverified: %v", err)
	}

	d.Loop.Config.UpdateURL = "https://mirror.internal/dl/"
	_, err = d.SelfUpdate("S1")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), httpSourceNote) {
		t.Errorf("the refusal marks an https source as unverified: %v", err)
	}
}
