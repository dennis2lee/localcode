package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// webDaemon is newTestDaemon with the embedded Web UI served, the same
// wiring production uses (daemon.go registers "/" only when webFS is
// non-nil; the shared helper builds a headless daemon).
func webDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.mux.Handle("/", http.FileServerFS(WebFS()))
	return d
}

// The settings window groups its sections into one tab per subject. The
// page the daemon serves is where both clients get it (the desktop GUI
// is the same page in a native window), so the served markup carrying
// the tabs is what makes the grouping real on every platform.
func TestSettingsWindowGroupsSectionsIntoTabs(t *testing.T) {
	d := webDaemon(t)

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	page := rec.Body.String()

	for _, want := range []string{
		`role="tablist"`,
		`id="settings-tab-agents"`,
		`id="settings-tab-turns"`,
		`id="settings-tab-updates"`,
		`id="settings-tab-typography"`,
		`id="settings-panel-agents"`,
		`id="settings-panel-turns"`,
		`id="settings-panel-updates"`,
		`id="settings-panel-typography"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("served page has no %s: the settings sections are not tabbed", want)
		}
	}
}

// The typography controls are page state, not daemon state: the served
// page must carry the pickers and the pre-paint read of the browser's
// own storage, with nothing under /api/settings behind them.
func TestSettingsTypographyControlsAreServed(t *testing.T) {
	d := webDaemon(t)

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	page := rec.Body.String()

	for _, want := range []string{
		`id="typo-doc-select"`,
		`id="typo-ui-select"`,
		`id="typo-mono-select"`,
		`id="typo-size-select"`,
		`localcode.textScale`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("served page has no %s: the typography controls are missing", want)
		}
	}

	// The module the controls run on is served too, not just referenced.
	modReq := httptest.NewRequest("GET", "/js/typography.js", nil)
	modRec := httptest.NewRecorder()
	d.Handler().ServeHTTP(modRec, modReq)
	if modRec.Code != http.StatusOK {
		t.Fatalf("GET /js/typography.js = %d, want 200", modRec.Code)
	}
	if !strings.Contains(modRec.Body.String(), "localcode.textScale") {
		t.Error("served typography.js never reads the stored text size")
	}
}
