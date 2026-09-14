package agent

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The path-or-name rule beside /skill: a registered name wins first, and
// what is left reads as a path by the same shape the unknown-command
// route uses — a slash or a dot is what a path looks like.
func TestSkillPathShapedArgumentsReadAsPaths(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		path bool
	}{
		{"./scratch.md", true},
		{"notes/scratch.md", true},
		{"/tmp/scratch.md", true},
		{"~/skills/scratch.md", true},
		{"~", true},
		{`..\scratch.md`, true},
		{"scratch.md", true},
		{"pdf-tools", false},
		{"pdf_tools-2", false},
		{"", false},
	} {
		if got := looksLikeSkillPath(tc.arg); got != tc.path {
			t.Errorf("looksLikeSkillPath(%q) = %v, want %v", tc.arg, got, tc.path)
		}
	}
}

// What counts as absolute depends on the platform, so the predicate takes
// it as a parameter. The Windows rows are typed out literally: built with
// filepath.Join they would carry this machine's separators and test
// nothing. A merely rooted path is the trap: \work is absolute nowhere,
// C:\work is absolute on Windows, /work is absolute everywhere else.
func TestIsAbsSkillPathCountsAbsolutePerPlatform(t *testing.T) {
	for _, tc := range []struct {
		name string
		goos string
		path string
		abs  bool
	}{
		{"windows drive backslash", "windows", `C:\work\project\scratch.md`, true},
		{"windows drive slash", "windows", "C:/work/project/scratch.md", true},
		{"windows UNC", "windows", `\\host\share\scratch.md`, true},
		{"windows rooted is not absolute", "windows", `\work\project\scratch.md`, false},
		{"windows slash-rooted is not absolute", "windows", "/work/project/scratch.md", false},
		{"windows drive-relative is not absolute", "windows", "C:scratch.md", false},
		{"windows bare relative", "windows", "notes/scratch.md", false},
		{"linux absolute", "linux", "/work/project/scratch.md", true},
		{"linux relative", "linux", "notes/scratch.md", false},
		{"darwin absolute", "darwin", "/work/project/scratch.md", true},
		{"darwin relative", "darwin", "notes/scratch.md", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAbsSkillPath(tc.goos, tc.path); got != tc.abs {
				t.Errorf("isAbsSkillPath(%q, %q) = %v, want %v", tc.goos, tc.path, got, tc.abs)
			}
		})
	}
}

// Which variable names home depends on the platform, so the lookup takes
// it as a parameter. The two variables hold different dirs here, so the
// test proves which one each platform reads.
func TestSkillHomeDirReadsThePlatformsVariable(t *testing.T) {
	t.Setenv("HOME", "/home/posix-user")
	t.Setenv("USERPROFILE", `C:\Users\windows-user`)
	if got := skillHomeDir("windows"); got != `C:\Users\windows-user` {
		t.Errorf("skillHomeDir(windows) = %q, want the USERPROFILE dir", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := skillHomeDir(goos); got != "/home/posix-user" {
			t.Errorf("skillHomeDir(%q) = %q, want the HOME dir", goos, got)
		}
	}
}

// A relative path is a claim about the session's workspace, the way a
// tool's relative path is; ~ expands to home. Both decisions take the
// platform as a parameter, so every row below runs on any machine —
// including the Windows rows, which is what the old version of this test
// could not do: it built its "absolute" path with filepath.Join, which
// spells a merely rooted \work\... on Windows, and stated the POSIX
// reading of that string as if it were universal.
func TestSkillPathsResolveAgainstTheSessionWorkspace(t *testing.T) {
	// Real paths from the running OS go through the same helper, with
	// runtime.GOOS passed in at the call site rather than read inside
	// the assertion.
	hostWS := t.TempDir()
	hostHome := t.TempDir()
	hostAbs := filepath.Join(hostWS, "scratch.md")

	for _, tc := range []struct {
		name      string
		goos      string
		raw       string
		workspace string
		home      string
		want      string
	}{
		{"host relative joins workspace", runtime.GOOS, "notes/scratch.md", hostWS, hostHome,
			filepath.Join(hostWS, "notes/scratch.md")},
		{"host absolute stays untouched", runtime.GOOS, hostAbs, filepath.Join(hostWS, "elsewhere"), hostHome,
			hostAbs},
		{"host tilde expands to home", runtime.GOOS, "~/fromhome.md", hostWS, hostHome,
			filepath.Join(hostHome, "fromhome.md")},
		{"windows drive absolute stays untouched", "windows", `C:\work\project\scratch.md`, `C:\elsewhere`, `C:\Users\alice`,
			`C:\work\project\scratch.md`},
		{"windows UNC absolute stays untouched", "windows", `\\host\share\scratch.md`, `C:\elsewhere`, `C:\Users\alice`,
			`\\host\share\scratch.md`},
		{"windows rooted joins workspace", "windows", `\work\project\scratch.md`, `C:\elsewhere`, `C:\Users\alice`,
			filepath.Join(`C:\elsewhere`, `\work\project\scratch.md`)},
		{"windows slash-rooted joins workspace", "windows", "/work/project/scratch.md", `C:\elsewhere`, `C:\Users\alice`,
			filepath.Join(`C:\elsewhere`, "/work/project/scratch.md")},
		{"windows relative joins workspace", "windows", "notes/scratch.md", `C:\work\project`, `C:\Users\alice`,
			filepath.Join(`C:\work\project`, "notes/scratch.md")},
		{"windows tilde expands to home", "windows", "~/fromhome.md", `C:\work\project`, `C:\Users\alice`,
			filepath.Join(`C:\Users\alice`, "fromhome.md")},
		{"windows bare tilde is home", "windows", "~", `C:\work\project`, `C:\Users\alice`,
			`C:\Users\alice`},
		{"tilde without a home stays relative", "linux", "~/fromhome.md", "/work/project", "",
			filepath.Join("/work/project", "~/fromhome.md")},
		{"linux absolute stays untouched", "linux", "/work/project/scratch.md", "/elsewhere", "/home/alice",
			"/work/project/scratch.md"},
		{"linux tilde expands to home", "linux", "~/fromhome.md", "/work/project", "/home/alice",
			filepath.Join("/home/alice", "fromhome.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSkillPathFor(tc.goos, tc.raw, tc.workspace, tc.home); got != tc.want {
				t.Errorf("resolveSkillPathFor(%q, %q, %q, %q) = %q, want %q",
					tc.goos, tc.raw, tc.workspace, tc.home, got, tc.want)
			}
		})
	}
}
