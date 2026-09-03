// Package update finds out whether a newer localcode has been released,
// fetches it, and hands it to whatever installs software on this platform.
//
// It is deliberately not an auto-updater. Nothing here runs on a timer and
// nothing downloads without being asked: a check is an outbound request to
// GitHub that says which version someone is running, and installing
// replaces the program under the person using it. Both are things to be
// asked for, so both are buttons.
//
// The check and the install are separate for the same reason. Knowing that
// v0.46.0 exists is cheap and harmless; replacing the running install is
// neither, and it is the one that gets a confirmation.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultRepo is where localcode's releases are published.
const DefaultRepo = "dennis2lee/localcode"

// defaultAPI is GitHub's REST endpoint. A field on Checker rather than a
// constant so a test can point it at a server it controls — the
// alternative is a test that only passes with a network connection and a
// published release, which is not a test.
const defaultAPI = "https://api.github.com"

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
	// Digest is GitHub's own checksum, "sha256:<hex>". Empty for a release
	// published before GitHub recorded them, in which case a download is
	// checked by size alone and says so.
	Digest string `json:"digest,omitempty"`
}

// Release is one published version.
type Release struct {
	// Version is the tag with any leading "v" removed, so it compares
	// against the version this binary was stamped with.
	Version string `json:"version"`
	Tag     string `json:"tag"`
	// PageURL is the release's page, for someone who would rather install
	// it themselves — and the only answer on a platform with no installer.
	PageURL   string    `json:"page_url"`
	Notes     string    `json:"notes,omitempty"`
	Published time.Time `json:"published,omitempty"`
	Assets    []Asset   `json:"assets,omitempty"`
}

// Checker asks a repository what its latest release is.
type Checker struct {
	// Repo is "owner/name". Empty means DefaultRepo.
	Repo string
	// API overrides GitHub's address. Empty means the real one.
	API string
	// URL, when set, replaces GitHub entirely: a plain address where the
	// installers are published, config.json's "update_url". See mirror.go
	// for why the two sources cannot share a code path — one answers a
	// question about releases and the other is a directory of files.
	URL string
	// Client is the HTTP client to use. Nil means a plain one with a
	// timeout, because the default client has none and this call is made
	// from a click.
	Client *http.Client
}

func (c Checker) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c Checker) api() string {
	if c.API != "" {
		return strings.TrimSuffix(c.API, "/")
	}
	return defaultAPI
}

func (c Checker) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Latest returns the most recent published release, from update_url when
// one is configured and from GitHub otherwise.
//
// Dispatched here rather than at the call sites, so the check button, the
// install button and anything added later all follow the same setting
// without each having to remember to.
func (c Checker) Latest(ctx context.Context) (Release, error) {
	if strings.TrimSpace(c.URL) != "" {
		return c.LatestFromURL(ctx, c.URL)
	}
	return c.latestFromGitHub(ctx)
}

func (c Checker) latestFromGitHub(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.api(), c.repo())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.client().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("ask GitHub for the latest release: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Release{}, err
	}
	if resp.StatusCode != http.StatusOK {
		// 403 here is nearly always the anonymous rate limit, which is per
		// address and short-lived. Saying "403" alone sends someone looking
		// for a permissions problem they do not have.
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return Release{}, fmt.Errorf("GitHub is rate limiting this address (%s) — try again in a few minutes", resp.Status)
		}
		return Release{}, fmt.Errorf("GitHub answered %s for %s", resp.Status, url)
	}

	var raw struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Release{}, fmt.Errorf("GitHub's answer was not the release JSON: %w", err)
	}
	rel := Release{
		Tag:       raw.TagName,
		Version:   strings.TrimPrefix(raw.TagName, "v"),
		PageURL:   raw.HTMLURL,
		Notes:     raw.Body,
		Published: raw.PublishedAt,
	}
	for _, a := range raw.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size, Digest: a.Digest})
	}
	return rel, nil
}

// Newer reports whether latest is a later version than current.
//
// "dev" — what an unstamped build calls itself — is not a version and
// compares as older than nothing: a build from a working tree is not
// something to offer a downgrade to a release for, and the panel says
// which build it is rather than pretending to know.
func Newer(current, latest string) bool {
	cur, ok := parseVersion(current)
	if !ok {
		return false
	}
	next, ok := parseVersion(latest)
	if !ok {
		return false
	}
	for i := range next {
		if next[i] != cur[i] {
			return next[i] > cur[i]
		}
	}
	return false
}

// parseVersion reads "v1.2.3" or "1.2" into three numbers. Anything that
// is not a version — "dev", a branch name, an empty string — is rejected
// rather than read as zeroes, since 0.0.0 would make every release newer.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	// A pre-release or build suffix is not compared, only trimmed: this
	// picks between releases, and localcode does not publish any.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return [3]int{}, false
	}
	var out [3]int
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return [3]int{}, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// AssetFor picks the file to install on this platform, and says why there
// is none when there is none.
//
// The preference is for the thing the user installed from: on Windows that
// is the MSI, which knows how to replace an existing install, remove the
// old files and keep the Start menu entry. The zip is a fallback for the
// architecture that has no MSI, and it is not something this can install —
// see Apply.
//
// packaged says the running copy came from a package rather than from an
// archive someone unpacked. It means two different things by platform and
// answers the same question in both: on macOS, that this is the .app
// bundle rather than the bare binary; on Linux, that dpkg put it in
// /usr/bin. Offering the other one leaves a second localcode on the
// machine and no way to tell which is running.
// HandoffAssetFor is AssetFor for a handoff, which needs a binary this
// user can run without an installer: on Windows that is the zip rather
// than the MSI, and everywhere else it is what AssetFor picks.
func (r Release) HandoffAssetFor(goos, goarch string, packaged bool) (Asset, error) {
	if goos == "windows" {
		for _, a := range r.Assets {
			if strings.HasSuffix(a.Name, "-windows-"+goarch+".zip") {
				return a, nil
			}
		}
		return Asset{}, fmt.Errorf("release %s has no windows/%s zip to hand off to", r.Version, goarch)
	}
	return r.AssetFor(goos, goarch, packaged)
}

func (r Release) AssetFor(goos, goarch string, packaged bool) (Asset, error) {
	want := func(suffixes ...string) (Asset, bool) {
		for _, suffix := range suffixes {
			for _, a := range r.Assets {
				if strings.HasSuffix(a.Name, suffix) {
					return a, true
				}
			}
		}
		return Asset{}, false
	}

	switch goos {
	case "windows":
		if goarch == "amd64" {
			if a, ok := want("-windows-amd64.msi", "-windows-amd64.zip"); ok {
				return a, nil
			}
		}
		if a, ok := want("-windows-" + goarch + ".zip"); ok {
			return a, nil
		}
	case "darwin":
		// A desktop build lives in LocalCode.app; the command line one is
		// a bare binary in a tarball. Installing the wrong one leaves the
		// person with a second copy of localcode and no idea which is
		// running.
		if packaged {
			if a, ok := want("-darwin-universal-app.tar.gz"); ok {
				return a, nil
			}
		}
		if a, ok := want("-darwin-universal.tar.gz"); ok {
			return a, nil
		}
	case "linux":
		// The .deb only for a copy that dpkg installed. Handing a .deb to
		// someone who unpacked a tarball into ~/bin gives them a file
		// they have to know what to do with; handing a tarball to someone
		// whose localcode is dpkg-managed invites a second copy that
		// shadows the packaged one depending on PATH order.
		if packaged {
			if a, ok := want("-linux-" + goarch + ".deb"); ok {
				return a, nil
			}
		}
		if a, ok := want("-linux-" + goarch + ".tar.gz"); ok {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("release %s has nothing for %s/%s — install it from %s", r.Tag, goos, goarch, r.PageURL)
}

// Download fetches an asset into dir and returns the path it was written
// to, having checked that what arrived is what was advertised.
//
// The check is the point. This file is about to be run as an installer,
// and a truncated download is the likely failure rather than a malicious
// one: a connection dropped at 90% produces an MSI that opens, fails
// halfway, and leaves a broken install. GitHub publishes a SHA-256 for
// every asset, so it is verified when there is one and the size is checked
// either way.
func Download(ctx context.Context, client *http.Client, a Asset, dir string) (string, error) {
	if client == nil {
		// No overall timeout: this is tens of megabytes over whatever
		// connection the machine has, and a deadline that fits a fast one
		// is a slow one that can never update. The context is what ends it.
		client = &http.Client{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", a.Name, resp.Status)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Written under a temporary name and renamed once it has been checked,
	// so a half-finished download can never be found and run by anything —
	// including a later attempt at this.
	tmp, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once the rename below has happened

	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("download %s: %w", a.Name, err)
	}
	if a.Size > 0 && n != a.Size {
		return "", fmt.Errorf("download %s: got %d bytes, the release says %d", a.Name, n, a.Size)
	}
	// Only sha256 is understood. An asset carrying some other algorithm is
	// not silently accepted as unchecked: it is refused, because "we could
	// not check it" is not a thing to decide on the user's behalf while
	// about to run the file.
	if a.Digest != "" {
		want, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok {
			return "", fmt.Errorf("download %s: the release records a %q checksum, which localcode cannot verify", a.Name, a.Digest)
		}
		if got := hex.EncodeToString(sum.Sum(nil)); !strings.EqualFold(got, want) {
			return "", fmt.Errorf("download %s: sha256 is %s, the release says %s", a.Name, got, want)
		}
	}

	// The real name, because an installer is chosen by its extension —
	// msiexec will not open a file called "download-1234".
	path := filepath.Join(dir, filepath.Base(a.Name))
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return path, nil
}
