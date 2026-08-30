package update

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The version scripts/check-dist.sh is asked about. Any value works; the
// names are compared with it substituted on both sides.
const checkDistVersion = "9.9.9"

// The platforms this project builds a release for. Every one of them is a
// person who runs the update check and is told either "here is your
// download" or "this release has nothing for you".
var shippedPlatforms = []struct {
	goos, goarch string
}{
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"windows", "amd64"},
	{"windows", "arm64"},
}

// scripts/check-dist.sh asserts dist/ holds the assets a release has to
// publish. It carries its own hand-written list of nine names, which is
// the fourth copy of that list in the repository -- AssetFor's suffix
// table, the packaging scripts, the `gh release create` line in
// RELEASING.md, and now this one. Three of those four are prose or shell
// and cannot be compared to anything by a compiler.
//
// This test compares the one that gates the release against the one that
// serves the download, and it does so behaviourally rather than by
// matching strings: it runs AssetFor over every subset of the nine names
// and every platform, and collects every name AssetFor can ever return.
// That set has to be exactly the nine. A name check-dist.sh requires that
// AssetFor can never pick is an asset built and uploaded for nobody; a
// name AssetFor can pick that check-dist.sh does not require is a
// platform that can be handed a download the release gate never checked
// was there.
//
// The subsets matter and are not thoroughness for its own sake. Two of
// the nine are only ever reached when something else is absent:
// windows-amd64.zip is the fallback for a missing .msi, and the darwin
// bare tarball is what a packaged .app install falls back to. Testing
// only the full release would call both of them orphans.
func TestCheckDistRequiresExactlyWhatAssetForCanPick(t *testing.T) {
	required := requiredAssetNames(t)
	// A floor, not the count. Pinning it at nine would make adding a
	// tenth platform fail here with "expected 9" instead of with the
	// mismatch that actually matters, and would mask both properties
	// below whenever the list simply changed length.
	if len(required) < 5 {
		t.Fatalf("parsed only %d names from check-dist.sh, so this test is checking almost nothing: %v", len(required), required)
	}

	reachable := map[string]bool{}
	// Every subset of the nine. 512 releases, six platforms, two install
	// shapes: small enough to be exhaustive and exhaustive is the point.
	for mask := 0; mask < 1<<len(required); mask++ {
		var assets []Asset
		for i, name := range required {
			if mask&(1<<i) != 0 {
				assets = append(assets, Asset{Name: name})
			}
		}
		rel := Release{Tag: "v" + checkDistVersion, Assets: assets}
		for _, p := range shippedPlatforms {
			for _, packaged := range []bool{true, false} {
				if a, err := rel.AssetFor(p.goos, p.goarch, packaged); err == nil {
					reachable[a.Name] = true
				}
			}
		}
	}

	for _, name := range required {
		if !reachable[name] {
			t.Errorf("check-dist.sh requires %q and AssetFor can never pick it, on any platform, for any release that contains it", name)
		}
	}
	for name := range reachable {
		if !contains(required, name) {
			t.Errorf("AssetFor can serve %q and check-dist.sh does not require it, so a release could ship without it and the gate would pass", name)
		}
	}
}

// And the other half: with all nine present, nobody is turned away. This
// is the property the release actually promises, and the one that breaks
// silently -- a platform whose asset is missing does not get an error
// about a missing file, it gets "release vX.Y.Z has nothing for you",
// from a release that built it.
func TestACompleteReleaseServesEveryPlatform(t *testing.T) {
	required := requiredAssetNames(t)
	assets := make([]Asset, 0, len(required))
	for _, name := range required {
		assets = append(assets, Asset{Name: name})
	}
	rel := Release{Tag: "v" + checkDistVersion, Assets: assets}

	for _, p := range shippedPlatforms {
		for _, packaged := range []bool{true, false} {
			a, err := rel.AssetFor(p.goos, p.goarch, packaged)
			if err != nil {
				t.Errorf("%s/%s packaged=%v: %v", p.goos, p.goarch, packaged, err)
				continue
			}
			if !strings.Contains(a.Name, checkDistVersion) {
				t.Errorf("%s/%s packaged=%v got %q, which is not from this release", p.goos, p.goarch, packaged, a.Name)
			}
		}
	}
}

// An asset dropped from check-dist.sh does not announce itself, which is
// what makes this the direction worth testing hardest. AssetFor falls
// back rather than failing: with no .deb in the release, a dpkg-managed
// install is quietly handed the tarball, and every reachability check
// still passes because nothing asked for a .deb in the first place.
//
// So this asserts the contract AssetFor documents rather than the names
// it returns. On macOS and Linux the two install shapes must resolve to
// two different assets -- that is the whole reason `packaged` is a
// parameter, and its comment says why: offering the other one leaves a
// second localcode on the machine and no way to tell which is running.
// On Windows the shapes collapse, but amd64 must reach the installer and
// arm64 the zip.
//
// Restating the nine names here would defeat the purpose; these are the
// four rules that made it nine.
//
// Verified by removing each of the nine from check-dist.sh in turn: eight
// are caught, here or by the completeness test above. The ninth,
// windows-amd64.zip, is not, and cannot be by any behavioural test --
// AssetFor never picks it while the .msi is present, so it is insurance
// against a missing installer rather than anything a platform is served.
// Dropping it would go unnoticed until the day the .msi failed to build.
func TestTheRequiredAssetsSatisfyEveryInstallShape(t *testing.T) {
	required := requiredAssetNames(t)
	assets := make([]Asset, 0, len(required))
	for _, name := range required {
		assets = append(assets, Asset{Name: name})
	}
	rel := Release{Tag: "v" + checkDistVersion, Assets: assets}

	pick := func(goos, goarch string, packaged bool) string {
		t.Helper()
		a, err := rel.AssetFor(goos, goarch, packaged)
		if err != nil {
			t.Errorf("%s/%s packaged=%v: %v", goos, goarch, packaged, err)
			return ""
		}
		return a.Name
	}

	for _, p := range shippedPlatforms {
		switch p.goos {
		case "darwin", "linux":
			inPackage, unpacked := pick(p.goos, p.goarch, true), pick(p.goos, p.goarch, false)
			if inPackage == "" || unpacked == "" {
				continue
			}
			if inPackage == unpacked {
				t.Errorf("%s/%s: both install shapes resolve to %q, so the asset for one of them is not in check-dist.sh and a release missing it would pass the gate", p.goos, p.goarch, inPackage)
			}
		case "windows":
			want := ".zip"
			if p.goarch == "amd64" {
				want = ".msi"
			}
			if got := pick(p.goos, p.goarch, true); got != "" && !strings.HasSuffix(got, want) {
				t.Errorf("windows/%s resolves to %q, not the %s it must install from", p.goarch, got, want)
			}
		}
	}
}

// requiredAssetNames reads the required=( ... ) list out of
// scripts/check-dist.sh: the basenames, with the version substituted.
//
// Reading the script rather than restating its list is the whole point.
// A fifth copy of these nine names, sitting in a test that passes because
// it agrees with itself, would be the problem this test exists to catch.
func requiredAssetNames(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "check-dist.sh"))
	if err != nil {
		t.Fatalf("read check-dist.sh: %v", err)
	}
	block := regexp.MustCompile(`(?s)\nrequired=\((.*?)\n\)`).FindSubmatch(src)
	if block == nil {
		t.Fatal("no `required=( ... )` array in scripts/check-dist.sh: this test reads that array by name, so renaming or reshaping it needs this test updated rather than left to pass on nothing")
	}

	var names []string
	for _, line := range strings.Split(string(block[1]), "\n") {
		// Entries look like:  "mac/localcode-$VERSION-....tar.gz"  # note
		m := regexp.MustCompile(`"([^"]+)"`).FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.ReplaceAll(m[1], "$VERSION", checkDistVersion)
		names = append(names, filepath.Base(name))
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
