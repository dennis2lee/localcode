package update

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Updating from somewhere that is not GitHub.
//
// GitHub answers "what is the latest release" with JSON: a tag, and an
// asset list with names, sizes and checksums. An internal file share
// answers no such question. What it has is the installers, sitting in a
// directory, and the version is in their names — which is the whole
// difference this file exists to bridge.
//
// So the version is read out of the filenames, because that is the only
// place it is written down. Whatever the page is — an Apache index, a
// Bitbucket downloads listing, an artifact server's JSON, or a direct
// link to one file — the body is scanned for names that look like
// localcode's own installers, and the highest version found is the
// release. That works across all four without knowing which one it is,
// which is the point: an internal host is whatever somebody set up.

// assetRe matches one of localcode's published installer names, with
// whatever URL or path was written immediately before it.
//
// The leading class is deliberately narrow. It has to swallow
// "https://host/path/" so a listing that carries full links is followed
// to the right place, and it has to stop at a quote, a bracket or a space
// so an HTML attribute does not drag `href="` along with it. `=`, `?` and
// `&` are left out for that reason: a listing that links its files with a
// query string is not something this can be sure it is reading correctly,
// and resolving the bare name against the directory is the safer answer.
var assetRe = regexp.MustCompile(`(?i)([A-Za-z0-9._~:/@%+-]*)(localcode-(\d+\.\d+\.\d+)-[a-z0-9.-]+?\.(?:msi|zip|deb|tar\.gz))`)

// LatestFromURL reads a release out of whatever is published at u.
//
// It is the "update_url" path: one page, one version, the files for every
// platform sitting beside each other. No API, no tag, no release notes —
// none of which exist on a file share, and none of which this pretends to
// have.
func (c Checker) LatestFromURL(ctx context.Context, u string) (Release, error) {
	base, err := checkedURL(u)
	if err != nil {
		return Release{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return Release{}, err
	}
	// Asked for as data rather than as a page, so a host that can answer
	// either way gives the machine-readable one. A plain file server
	// ignores it and serves the index, which is equally fine: the scan
	// below does not care which it got.
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
	resp, err := c.client().Do(req)
	if err != nil {
		// The message names the address, because the common failure is a
		// URL typed into config.json by hand.
		return Release{}, fmt.Errorf("could not reach update_url %s: %w", base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Release{}, fmt.Errorf("read %s: %w", base, err)
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("update_url %s answered %s", base, resp.Status)
	}

	// The address counts as part of what was read, so update_url may point
	// straight at one file rather than at a directory of them.
	rel, err := releaseFromListing(base, resp.Request.URL.String()+"\n"+string(body))
	if err != nil {
		return Release{}, err
	}
	return rel, nil
}

// checkedURL parses update_url and refuses the ones that cannot be used
// safely.
//
// https always. http only off the public internet: loopback, the private
// and link-local ranges, carrier-grade NAT, and names that can only be
// internal, including names that merely resolve to such addresses. What
// this URL names is a file that is about to be run as an installer, and
// over plain http anything between here and there can choose which file
// that is. There is no checksum to fall back on either: a file share
// publishes the installer and, usually, nothing else. So on the open
// internet the transport is the only thing standing between a typo'd
// config and arbitrary code, and it has to be the one that authenticates
// the host.
//
// Accepting http inside a closed network only bounds that exposure, it
// does not remove it: anyone already on that network can still substitute
// the file, and the checksum beside it travels the same connection. The
// user running the mirror has made that trade knowingly, and a config
// pasted onto a laptop in a cafe must not make it by accident, which is
// why a name that looks public is refused rather than warned about.
func checkedURL(raw string) (*url.URL, error) {
	return checkedURLWithLookup(raw, systemLookupIP)
}

// systemLookupIP resolves a hostname through the system resolver. It is
// the production argument to checkedURLWithLookup, which takes the
// resolver as a parameter so the decision table is a table and not a
// network test.
func systemLookupIP(host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// checkedURLWithLookup is checkedURL with its DNS answers supplied by the
// caller. https returns before any lookup, exactly as it always has: no
// resolution, no new message, nothing.
func checkedURLWithLookup(raw string, lookup func(string) ([]net.IP, error)) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("update_url is not a URL: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("update_url %q names no host", raw)
	}
	switch u.Scheme {
	case "https":
		return u, nil
	case "http":
		// Decided below, from the host.
	default:
		return nil, fmt.Errorf(
			"update_url must be https, or http to a host on a private network (it is %q). This URL names a file localcode will run as an installer, "+
				"and there is no checksum published beside it to fall back on, so the connection is the only "+
				"thing that says the file came from the host you meant", u.Scheme)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	// The literal before the name rules: a bare IPv6 address has no dots,
	// so the single-label rule below would claim it as a local name.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return u, nil
		}
		return nil, fmt.Errorf(
			"update_url host %q is on the public internet, so plain http is refused. This URL names a file localcode will run as an installer, "+
				"and there is no checksum published beside it to fall back on, so the connection is the only "+
				"thing that says the file came from the host you meant. Serve it over https instead", host)
	}
	if isPrivateHostname(host) {
		return u, nil
	}
	// A name that looks public but resolves privately is exactly the user
	// this is for (mirror.corp.example.com pointing at 10.0.0.5), so
	// the addresses decide, and every one of them has to be private. One
	// public answer means DNS is splitting the name across networks, and
	// refusing is the only reading that keeps the typo'd-config promise.
	ips, err := lookup(host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf(
			"update_url host %q could not be resolved (%v), so localcode cannot tell it is a private mirror: plain http is refused. "+
				"This URL names a file localcode will run as an installer, and there is no checksum published beside it to fall back on. "+
				"Fix the name, or serve it over https", host, err)
	}
	for _, ip := range ips {
		if !isPrivateIP(ip) {
			return nil, fmt.Errorf(
				"update_url host %q resolves to %s, which is on the public internet, so plain http is refused. This URL names a file localcode will run as an installer, "+
					"and there is no checksum published beside it to fall back on, so the connection is the only "+
					"thing that says the file came from the host you meant. Serve it over https instead", host, ip)
		}
	}
	return u, nil
}

// cgnatNet is 100.64/10, carrier-grade NAT: an internal network may well
// use it, and it is not covered by IsPrivate.
var cgnatNet = net.IPNet{IP: net.ParseIP("100.64.0.0"), Mask: net.CIDRMask(10, 32)}

// isPrivateIP reports whether an address is off the public internet:
// loopback, the v4 and v6 private ranges, link-local, and carrier-grade
// NAT. Deliberately not IsPrivate alone, which leaves out three of those.
func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnatNet.Contains(ip)
}

// internalSuffixes are the endings an internal network's names use. Each
// matches with its dot, so "notinternal" is not one; the bare suffix
// itself ("home.arpa") counts too.
var internalSuffixes = []string{".local", ".internal", ".intranet", ".home.arpa"}

// isPrivateHostname reports whether a name is private without asking DNS:
// localhost, a single label with no dots, and the internal suffixes. The
// host arrives lowercased with any trailing dot removed.
func isPrivateHostname(host string) bool {
	if host == "localhost" {
		return true
	}
	if !strings.Contains(host, ".") {
		return true
	}
	for _, s := range internalSuffixes {
		if strings.HasSuffix(host, s) || host == s[1:] {
			return true
		}
	}
	return false
}

// releaseFromListing turns a page into a Release: every installer name it
// mentions, at the highest version it mentions.
//
// The highest rather than the only one, because "there is only ever the
// latest file there" is a promise about how somebody keeps the directory
// and not something to depend on. A leftover from last month sitting
// beside this month's must not make localcode offer a downgrade.
func releaseFromListing(base *url.URL, body string) (Release, error) {
	type found struct {
		name string
		url  string
		// linked records that the page gave a full address for this file,
		// as opposed to a bare name resolved against the directory. Kept
		// because every url below is absolute by the time it is stored —
		// resolving makes it so — and the question is which one the page
		// actually said.
		linked bool
	}
	byVersion := map[string][]found{}
	// name -> where it sits in its version's slice, so a second mention
	// can improve the first rather than being dropped.
	seen := map[string]int{}

	for _, m := range assetRe.FindAllStringSubmatch(body, -1) {
		prefix, name, version := m[1], m[2], m[3]
		href := prefix + name
		abs, err := resolve(base, href)
		if err != nil {
			continue
		}
		key := version + "\x00" + strings.ToLower(name)
		if at, had := seen[key]; had {
			// The same file, mentioned twice. A listing often names it
			// once as text and once as a link, and the link is the one
			// worth keeping: resolving the bare name against the
			// directory guesses at an address the page has already given.
			if isAbsolute(href) && !byVersion[version][at].linked {
				byVersion[version][at].url = abs
				byVersion[version][at].linked = true
			}
			continue
		}
		seen[key] = len(byVersion[version])
		byVersion[version] = append(byVersion[version], found{name: name, url: abs, linked: isAbsolute(href)})
	}
	if len(byVersion) == 0 {
		return Release{}, fmt.Errorf(
			"nothing at %s looks like a localcode installer. Expected a file named like "+
				"\"localcode-1.2.3-darwin-universal.tar.gz\" or \"localcode-1.2.3-windows-amd64.msi\"", base)
	}

	// A scan rather than a sort: "keep it if it is newer than the best so
	// far" is the sentence, and a comparator that expresses it is one
	// that can be written backwards without the code looking wrong.
	best := ""
	for v := range byVersion {
		if best == "" || Newer(best, v) {
			best = v
		}
	}

	rel := Release{
		Version: best,
		Tag:     "v" + best,
		PageURL: base.String(),
	}
	assets := byVersion[best]
	sort.Slice(assets, func(i, j int) bool { return assets[i].name < assets[j].name })
	for _, a := range assets {
		// No size and no digest: a file share publishes the file. The
		// download is checked against a sibling ".sha256" when there is
		// one, and reported as unverified when there is not — see
		// DigestFor and the note the panel shows.
		rel.Assets = append(rel.Assets, Asset{Name: a.name, URL: a.url})
	}
	return rel, nil
}

// isAbsolute reports whether a listing gave a full address rather than a
// name to be resolved against the directory.
func isAbsolute(href string) bool {
	return strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "http://")
}

// resolve turns whatever was written next to a filename into an address.
// A full URL is taken as it is, an absolute path is resolved against the
// host, and a bare name against the directory the listing was read from.
func resolve(base *url.URL, href string) (string, error) {
	ref, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

// DigestFor looks for a checksum published beside an asset, and reports
// "" when there is none.
//
// Optional on purpose. The case this feature exists for is a directory
// somebody drops a build into, and asking them to also publish a checksum
// would be asking for a step that will be skipped. So it is used when it
// is there and its absence is stated rather than hidden: the panel says
// the download could not be verified, which is a true sentence somebody
// can act on.
//
// The file is "<asset>.sha256", the shape sha256sum writes: a hex digest,
// optionally followed by the filename.
func (c Checker) DigestFor(ctx context.Context, assetURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL+".sha256", nil)
	if err != nil {
		return ""
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return ""
	}
	hex := strings.ToLower(fields[0])
	if len(hex) != 64 {
		return ""
	}
	for _, r := range hex {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return ""
		}
	}
	return "sha256:" + hex
}
