package update

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Reading a release from github.com when api.github.com cannot be reached.
//
// Some networks let a browser open github.com and refuse api.github.com:
// a filter that knows the site and not its API, or a proxy that allows
// one host and not the other. github.com answers the same question as
// pages. /releases/latest redirects to the latest release's tag, and the
// release's asset list, the HTML a browser fetches when it expands
// "Assets", links every file and prints the SHA-256 GitHub recorded for
// it beside the link.
//
// That checksum is the one the API reports, from the same publisher, over
// the same TLS to GitHub, so a download is checked exactly as it would be
// from the API: by SHA-256 and by the container its name promises. What
// the page does not give is the exact size, which the SHA-256 already
// pins. And where the page shows no SHA-256 for a file, that file is not
// installed: GitHub records one for every file uploaded now, so its
// absence most likely means the page was misread, and an installer is not
// run on a guess. See Release.checkable.

// defaultWeb is github.com itself.
const defaultWeb = "https://github.com"

// ErrUnchecked marks an asset that would be installed without a checksum
// from a source that always publishes one, which is refused rather than
// installed unchecked.
var ErrUnchecked = errors.New("no published checksum to check the download against")

// web returns github.com's address for the fallback, and false when there
// is no fallback to try. An API pointed somewhere else is not GitHub, and
// github.com is not its other half: a test that points the API at a dead
// server must not end up asking the real github.com instead.
func (c Checker) web() (string, bool) {
	if c.Web != "" {
		return strings.TrimSuffix(c.Web, "/"), true
	}
	if c.API == "" {
		return defaultWeb, true
	}
	return "", false
}

// latestFromPage reads the latest release from github.com's own pages.
func (c Checker) latestFromPage(ctx context.Context, web string) (Release, error) {
	tag, err := c.latestTag(ctx, web)
	if err != nil {
		return Release{}, err
	}
	assets, err := c.pageAssets(ctx, web, tag)
	if err != nil {
		return Release{}, err
	}
	return Release{
		Tag:      tag,
		Version:  strings.TrimPrefix(tag, "v"),
		PageURL:  web + "/" + c.repo() + "/releases/tag/" + url.PathEscape(tag),
		Assets:   assets,
		FromPage: true,
	}, nil
}

// latestTag asks github.com which tag is the latest release, by reading
// where /releases/latest redirects to rather than following it.
func (c Checker) latestTag(ctx context.Context, web string) (string, error) {
	u := web + "/" + c.repo() + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	client := *c.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ask github.com for the latest release: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	loc, err := resp.Location()
	if resp.StatusCode < 300 || resp.StatusCode > 399 || err != nil {
		return "", fmt.Errorf("github.com answered %s for %s, not a redirect to the latest release", resp.Status, u)
	}
	_, tag, ok := strings.Cut(loc.Path, "/releases/tag/")
	if !ok || tag == "" || strings.Contains(tag, "/") {
		return "", fmt.Errorf("github.com sent %s to %s, which is not a release", u, loc.Redacted())
	}
	return tag, nil
}

// pageAssets reads a release's asset list from github.com.
func (c Checker) pageAssets(ctx context.Context, web, tag string) ([]Asset, error) {
	u := web + "/" + c.repo() + "/releases/expanded_assets/" + url.PathEscape(tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("read the files of %s from github.com: %w", tag, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read the files of %s from github.com: %w", tag, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github.com answered %s for %s", resp.Status, u)
	}
	assets := assetsFromPage(resp.Request.URL, c.repo(), tag, string(body))
	if len(assets) == 0 {
		return nil, fmt.Errorf("github.com's file list for %s at %s links no files", tag, u)
	}
	return assets, nil
}

var (
	// downloadLinkRe is a link to a release file, as the asset list
	// writes it: href="/owner/repo/releases/download/tag/name".
	downloadLinkRe = regexp.MustCompile(`href="([^"]*/releases/download/[^"]+)"`)
	digestRe       = regexp.MustCompile(`sha256:([0-9a-fA-F]{64})`)
)

// assetsFromPage reads the files a release's asset list links, each with
// the SHA-256 printed beside it.
//
// Beside means after its link and before the next file's link: the list
// is one row per file, the link first and the checksum further along the
// same row. That is the one thing about the markup this relies on, and it
// relies on it safely. A row read wrongly gives a file another file's
// checksum, and that download is refused by the checksum, not run. A row
// with no checksum, or with two that disagree, gives none, and AssetFor
// refuses to install that file.
func assetsFromPage(base *url.URL, repo, tag, body string) []Asset {
	prefix := "/" + repo + "/releases/download/" + tag + "/"
	links := downloadLinkRe.FindAllStringSubmatchIndex(body, -1)
	var out []Asset
	at := map[string]int{}
	disagree := map[string]bool{}
	for i, m := range links {
		ref, err := url.Parse(html.UnescapeString(body[m[2]:m[3]]))
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		// github.com's own files for this release, and nothing the page
		// happens to link elsewhere.
		if abs.Host != base.Host || len(abs.Path) <= len(prefix) || !strings.EqualFold(abs.Path[:len(prefix)], prefix) {
			continue
		}
		name := abs.Path[len(prefix):]
		if strings.Contains(name, "/") {
			continue
		}
		end := len(body)
		if i+1 < len(links) {
			end = links[i+1][0]
		}
		digest := onlyDigest(body[m[1]:end])
		if j, seen := at[name]; seen {
			// The same file linked twice: a checksum either row gives is
			// kept, and none at all once two rows disagree.
			switch {
			case disagree[name] || digest == "":
			case out[j].Digest == "":
				out[j].Digest = digest
			case out[j].Digest != digest:
				out[j].Digest = ""
				disagree[name] = true
			}
			continue
		}
		at[name] = len(out)
		out = append(out, Asset{Name: name, URL: abs.String(), Digest: digest})
	}
	return out
}

// onlyDigest returns the one SHA-256 in s as "sha256:<hex>", and "" when
// there is none or more than one.
func onlyDigest(s string) string {
	found := ""
	for _, m := range digestRe.FindAllStringSubmatch(s, -1) {
		d := "sha256:" + strings.ToLower(m[1])
		if found != "" && found != d {
			return ""
		}
		found = d
	}
	return found
}
