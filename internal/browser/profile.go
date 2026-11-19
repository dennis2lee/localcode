package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// profileEntries are what is copied from the everyday profile: what keeps a
// person signed in (cookies, the stores sites keep their own login state
// in), the passwords Chrome saved, and the preferences. Caches, history,
// extensions and the rest are left behind: they are most of a profile's
// size, and none of them is a login.
var profileEntries = []string{
	"Cookies", "Cookies-journal", "Network",
	"Login Data", "Login Data-journal", "Login Data For Account", "Login Data For Account-journal",
	"Web Data", "Web Data-journal", "Account Web Data", "Account Web Data-journal",
	"Preferences", "Secure Preferences",
	"Local Storage", "Session Storage", "IndexedDB",
}

// ImportReport says what an import did.
type ImportReport struct {
	From    string   // the profile directory copied from
	Profile string   // its name, e.g. "Default" or "Profile 1"
	Skipped []string // entries that could not be read, usually locked by the running browser
}

// LastUsedProfile is the profile the person last used in userDataDir, from
// its Local State, or "Default".
func LastUsedProfile(userDataDir string) string {
	data, err := os.ReadFile(filepath.Join(userDataDir, "Local State"))
	if err != nil {
		return "Default"
	}
	var st struct {
		Profile struct {
			LastUsed string `json:"last_used"`
		} `json:"profile"`
	}
	if json.Unmarshal(data, &st) != nil || st.Profile.LastUsed == "" ||
		strings.ContainsAny(st.Profile.LastUsed, `/\`) || st.Profile.LastUsed == ".." {
		return "Default"
	}
	return st.Profile.LastUsed
}

// ImportProfile copies profile from the person's userDataDir into dst, a
// user data directory of localcode's own, replacing what an earlier import
// left there. Local State goes with it: on macOS and Windows it holds the
// key the cookies and passwords are encrypted with.
//
// A file the running browser holds locked (Windows does this to its
// databases) is skipped and reported, not fatal: the rest still signs the
// person in to most sites, and the report says to close the browser and
// import again for the rest.
func ImportProfile(userDataDir, profile, dst string) (ImportReport, error) {
	rep := ImportReport{From: filepath.Join(userDataDir, profile), Profile: profile}
	if fi, err := os.Stat(rep.From); err != nil || !fi.IsDir() {
		return rep, fmt.Errorf("no browser profile at %s", rep.From)
	}
	if err := os.MkdirAll(filepath.Join(dst, profile), 0o700); err != nil {
		return rep, err
	}
	if err := copyFile(filepath.Join(userDataDir, "Local State"), filepath.Join(dst, "Local State")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		rep.Skipped = append(rep.Skipped, "Local State")
	}
	for _, name := range profileEntries {
		src := filepath.Join(rep.From, name)
		target := filepath.Join(dst, profile, name)
		fi, err := os.Lstat(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			rep.Skipped = append(rep.Skipped, name)
			continue
		}
		_ = os.RemoveAll(target)
		if fi.IsDir() {
			if err := copyTree(src, target); err != nil {
				rep.Skipped = append(rep.Skipped, name)
			}
			continue
		}
		if fi.Mode().IsRegular() {
			if err := copyFile(src, target); err != nil {
				rep.Skipped = append(rep.Skipped, name)
			}
		}
	}
	return rep, nil
}

func copyTree(src, dst string) error {
	var failed error
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			failed = err
			return nil
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			if err := copyFile(path, target); err != nil {
				failed = err
			}
		}
		// Links and anything else are not part of a login.
		return nil
	})
	if err != nil {
		return err
	}
	return failed
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
