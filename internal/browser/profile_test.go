package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLastUsedProfileRefusesWhatIsNotAName(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{".", "..", "../x", `..\x`, ""} {
		os.WriteFile(filepath.Join(dir, "Local State"), []byte(`{"profile":{"last_used":"`+strings.ReplaceAll(v, `\`, `\\`)+`"}}`), 0o600)
		if got := LastUsedProfile(dir); got != "Default" {
			t.Errorf("last_used %q gave %q, want Default", v, got)
		}
	}
	os.WriteFile(filepath.Join(dir, "Local State"), []byte(`{"profile":{"last_used":"Profile 3"}}`), 0o600)
	if got := LastUsedProfile(dir); got != "Profile 3" {
		t.Errorf("got %q", got)
	}
}

// Logins come; payment cards and addresses (Web Data) do not.
func TestImportCopiesLoginsAndNotCards(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	prof := filepath.Join(src, "Default")
	os.MkdirAll(filepath.Join(prof, "Local Storage", "leveldb"), 0o700)
	for _, f := range []string{"Cookies", "Login Data", "Web Data", "Account Web Data", "History", "Preferences", filepath.Join("Local Storage", "leveldb", "x.log")} {
		os.WriteFile(filepath.Join(prof, f), []byte("x"), 0o600)
	}
	os.WriteFile(filepath.Join(src, "Local State"), []byte(`{}`), 0o600)
	if _, err := ImportProfile(src, "Default", dst); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"Cookies", "Login Data", "Preferences", filepath.Join("Local Storage", "leveldb", "x.log")} {
		if _, err := os.Stat(filepath.Join(dst, "Default", f)); err != nil {
			t.Errorf("%s was not copied", f)
		}
	}
	for _, f := range []string{"Web Data", "Account Web Data", "History"} {
		if _, err := os.Stat(filepath.Join(dst, "Default", f)); err == nil {
			t.Errorf("%s was copied", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "Local State")); err != nil {
		t.Error("Local State, which holds the key, was not copied")
	}
}

func TestFindRefusesARelativeBrowserPath(t *testing.T) {
	if _, err := Find("./chrome"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("Find(./chrome) = %v", err)
	}
	if !strings.Contains(ErrNotFound.Error(), "browser_path") {
		t.Errorf("ErrNotFound names %q", ErrNotFound)
	}
}
