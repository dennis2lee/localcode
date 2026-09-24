package update

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every msiexec exit code the helper can record, and what each means.
func TestMSIExitClassification(t *testing.T) {
	cases := []struct {
		code      int
		status    string
		installed bool
	}{
		{0, "installed", true},
		{3010, "installed-restart-needed", true},
		{1641, "installed-restart-started", true},
		{1602, "cancelled", false},
		{1618, "another-installation", false},
		{1603, "failed", false},
		{1, "failed", false},
	}
	for _, tc := range cases {
		status, installed := ClassifyMSIExit(tc.code)
		if status != tc.status || installed != tc.installed {
			t.Errorf("ClassifyMSIExit(%d) = (%q, %v), want (%q, %v)",
				tc.code, status, installed, tc.status, tc.installed)
		}
		if m := MSIExitMeaning(tc.code); m == "" {
			t.Errorf("MSIExitMeaning(%d) is empty", tc.code)
		}
	}
	// The panel names the code's meaning, so each pinned code says what
	// it is rather than repeating the number.
	for code, want := range map[int]string{
		0:    "installed",
		3010: "restart needed",
		1641: "restart started",
		1602: "cancelled",
		1618: "another installation",
	} {
		if got := MSIExitMeaning(code); !strings.Contains(got, want) {
			t.Errorf("MSIExitMeaning(%d) = %q, want it to say %q", code, got, want)
		}
	}
}

func TestMSIVersionFromName(t *testing.T) {
	if got := MSIVersionFromName("C:/u/localcode-0.46.0-windows-amd64.msi"); got != "0.46.0" {
		t.Errorf("version = %q, want 0.46.0", got)
	}
	for _, name := range []string{"localcode-0.46.0-linux-amd64.tar.gz", "localcode.exe", "other-1.2.3-windows-amd64.msi"} {
		if got := MSIVersionFromName(name); got != "" {
			t.Errorf("MSIVersionFromName(%q) = %q, want empty", name, got)
		}
	}
}

func TestMSIHelperNames(t *testing.T) {
	if got := MSIHelperName("windows"); got != "localcode-msi-helper.exe" {
		t.Errorf("helper = %q", got)
	}
	if got := MSIHelperName("darwin"); got != "localcode-msi-helper" {
		t.Errorf("helper = %q", got)
	}
	dir := filepath.Join("c:", "updates")
	if got := MSILogPath(filepath.Join(dir, "localcode-0.46.0-windows-amd64.msi"), "0.46.0"); got != filepath.Join(dir, "localcode-0.46.0-msi.log") {
		t.Errorf("log = %q", got)
	}
}

// The exact reply text, pinned. Two sentences, one period each: the old
// reply joined two sentences without a period, and joined two that
// contradicted each other.
func TestMSIDetailText(t *testing.T) {
	if got, want := MSIDetailWindow("0.46.0"),
		"The installer for localcode 0.46.0 starts when this window closes. LocalCode opens again when the installer has finished."; got != want {
		t.Errorf("window detail:\n got: %q\nwant: %q", got, want)
	}
	if got, want := MSIDetailTerminal("0.46.0"),
		"The installer for localcode 0.46.0 starts when localcode exits. Quit localcode to run it."; got != want {
		t.Errorf("terminal detail:\n got: %q\nwant: %q", got, want)
	}
	for _, d := range []string{MSIDetailWindow("0.46.0"), MSIDetailTerminal("0.46.0")} {
		if strings.Contains(d, ";") {
			t.Errorf("the reply joins sentences with a semicolon again: %q", d)
		}
	}
}

func TestMSIRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadMSIRecord(dir); !errors.Is(err, ErrNoMSIRecord) {
		t.Fatalf("missing record = %v, want ErrNoMSIRecord", err)
	}
	rec := MSIRecord{Version: "0.46.0", ExitCode: 1602, Log: filepath.Join(dir, "localcode-0.46.0-msi.log"), Time: time.Now().Truncate(time.Second)}
	if err := WriteMSIRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMSIRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != rec.Version || got.ExitCode != rec.ExitCode || got.Log != rec.Log {
		t.Errorf("record = %+v, want %+v", got, rec)
	}
	if err := ClearMSIRecord(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMSIRecord(dir); !errors.Is(err, ErrNoMSIRecord) {
		t.Errorf("cleared record = %v, want ErrNoMSIRecord", err)
	}
}

// The check reports a record unless an installed status is for the
// running version. Every installed status crossed with the version
// being equal or not to the running one: an installed record for the
// running version says nothing and is cleared, or the panel reports
// that install on every check forever.
func TestReportMSIRecord(t *testing.T) {
	log := filepath.Join("c:", "updates", "localcode-0.46.0-msi.log")
	cases := []struct {
		name    string
		code    int
		running string
		report  bool
	}{
		{"installed, older running", 0, "0.45.2", true},
		{"installed, running", 0, "0.46.0", false},
		{"restart needed, older running", 3010, "0.45.2", true},
		{"restart needed, running", 3010, "0.46.0", false},
		{"restart started, older running", 1641, "0.45.2", true},
		{"restart started, running", 1641, "0.46.0", false},
		{"cancelled, older running", 1602, "0.45.2", true},
		{"cancelled, running", 1602, "0.46.0", true},
		{"another installation, older running", 1618, "0.45.2", true},
		{"another installation, running", 1618, "0.46.0", true},
		{"failed, older running", 1603, "0.45.2", true},
		{"failed, running", 1603, "0.46.0", true},
	}
	for _, tc := range cases {
		rec := MSIRecord{Version: "0.46.0", ExitCode: tc.code, Log: log}
		if got := ReportMSIRecord(rec, tc.running); got != tc.report {
			t.Errorf("%s: ReportMSIRecord = %v, want %v", tc.name, got, tc.report)
		}
	}
}

func TestMSIPendingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := MSIPending{
		Version: "0.46.0", MSI: filepath.Join(dir, "x.msi"), Log: filepath.Join(dir, "x.log"),
		ParentExe:  `C:\Program Files\LocalCode\localcode-gui.exe`,
		ParentArgs: []string{"--gui"}, GUI: true, Time: time.Now().Truncate(time.Second),
	}
	path, err := WriteMSIPending(dir, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadMSIPending(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || got.MSI != want.MSI || got.Log != want.Log ||
		got.ParentExe != want.ParentExe || !strings.EqualFold(got.ParentExe, want.ParentExe) ||
		len(got.ParentArgs) != 1 || got.ParentArgs[0] != "--gui" || !got.GUI {
		t.Errorf("pending = %+v, want %+v", got, want)
	}
	if err := ClearMSIPending(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMSIPending(path); err == nil {
		t.Error("cleared pending still reads")
	}
}

// A second request while a helper is pending says so.
func TestMSIPendingError(t *testing.T) {
	err := &ErrMSIPending{Version: "0.46.0"}
	if !strings.Contains(err.Error(), "already pending") {
		t.Errorf("refusal = %q, want it to say an install is pending", err.Error())
	}
	if !strings.Contains(err.Error(), "0.46.0") {
		t.Errorf("refusal = %q, want the pending version", err.Error())
	}
}
