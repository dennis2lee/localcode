package config

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These decisions were checked on 99650aa before adding deny reach.
// The columns are the baseline with skip_permissions off and on.
var denyReachCases = []struct {
	line   string
	deny   bool
	before [2]Decision
}{
	{`if true; then rm -rf x; fi`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`while true; do rm -rf x; done`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`FOO=1 rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`! rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`time rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`{ rm -rf x; }`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`( rm -rf x )`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`command rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`exec rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`nohup rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`sudo rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`timeout 5 rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`nice -n 10 rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`env -i A=1 rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`xargs rm -rf < list`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`sh -c 'rm -rf x'`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`bash -lc "rm -rf x"`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`eval rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`find . -name y -exec rm -rf {} \;`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`echo $(rm -rf x)`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{"echo `rm -rf x`", true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`cat <(rm -rf x)`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`sudo -u root rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`timeout -s KILL 5 rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`nohup nice rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`sudo sh -c 'rm -rf x'`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`xargs -0 sh -c 'rm -rf "$@"' _`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`eval "rm -rf x"`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`bash -c "echo hi; rm -rf x"`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`FOO=1 BAR="a b" rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`then sudo rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`/usr/bin/nohup rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`echo "$(rm -rf x)"`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`watch -n 1 rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`find . -execdir rm -rf {} +`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`echo $(rm -rf x; echo done)`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`(rm -rf x)`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`/bin/rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`sudo /bin/rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`RM -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`busybox sh -c 'rm -rf x'`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`bash -c -- 'rm -rf x'`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`eval -- rm -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`$'rm' -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`bash -c $'rm -rf x'`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`sudo $'r\x6d' -rf x`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`env -S "sudo rm -rf x"`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`env --split-string="rm -rf x"`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`echo x | xargs rm`, true, [2]Decision{DecisionAsk, DecisionAllow}},
	{`ls /bin/rm`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`/usr/bin/git status`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`echo rm -rf x`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`git commit -m "rm -rf x"`, false, [2]Decision{DecisionAllow, DecisionAllow}},
	{`grep -r "rm -rf" .`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`ls rm`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`rmdir x`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`sudo ls`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`timeout 5 ls`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`echo '$(rm -rf x)'`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`find . -name '*.go'`, false, [2]Decision{DecisionAsk, DecisionAllow}},
	{`sh script.sh`, false, [2]Decision{DecisionAsk, DecisionAllow}},
}

func TestDenyReachNoDenyBaseline(t *testing.T) {
	for _, tc := range denyReachCases {
		for i, skip := range []bool{false, true} {
			cfg := Config{SkipPermissions: &skip}
			if got := cfg.ResolvePermission(BashToolName, tc.line, true); got != tc.before[i] {
				t.Errorf("%q skip=%v: got %s, baseline %s", tc.line, skip, got, tc.before[i])
			}
		}
	}
}

func denyReachConfig(skip bool, rules ...PermissionRule) *Config {
	return &Config{SkipPermissions: &skip, Permissions: Permissions{BashToolName: {Rules: rules}}}
}

func TestDenyReachDecisions(t *testing.T) {
	for _, tc := range denyReachCases {
		for i, skip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/skip=%v", tc.line, skip), func(t *testing.T) {
				cfg := denyReachConfig(skip, PermissionRule{Match: "rm *", Decision: DecisionDeny})
				want := tc.before[i]
				if tc.deny {
					want = DecisionDeny
				}
				if got := cfg.ResolvePermission(BashToolName, tc.line, true); got != want {
					t.Fatalf("got %s, want %s", got, want)
				}
			})
		}
	}
}

func TestDenyReachWrappers(t *testing.T) {
	// Independent of the production roster so removing an entry is caught.
	wrappers := []string{
		"command", "builtin", "exec", "nohup", "env", "nice", "ionice",
		"timeout", "gtimeout", "xargs", "sudo", "doas", "su", "stdbuf",
		"chrt", "taskset", "setsid", "flock", "chroot", "unbuffer",
		"caffeinate", "time", "strace", "ltrace", "nsenter", "unshare", "runuser", "firejail",
	}
	for _, wrapper := range wrappers {
		t.Run(wrapper, func(t *testing.T) {
			// The absolute spelling exercises the wrapper table even for time,
			// which the shell also recognizes as a leading reserved word.
			line := "/usr/bin/" + wrapper + " option value A=1 rm -rf x"
			cfg := denyReachConfig(true, PermissionRule{Match: "rm *", Decision: DecisionDeny})
			if got := cfg.ResolvePermission(BashToolName, line, true); got != DecisionDeny {
				t.Fatalf("got %s", got)
			}
		})
	}
}

func TestDenyReachAdditionalForms(t *testing.T) {
	lines := []string{
		`else rm x`, `elif rm x`, `if rm x`, `while rm x`, `until rm x`,
		`do rm x`, `coproc rm x`, `time -p rm x`, `A+=1 rm x`, `A[0]=1 rm x`,
		`A[0]+=1 rm x`, `sudo A=1 ! rm x`, `xargs echo rm -rf x`,
		`su root -c 'echo hi; rm x'`, `watch --interval 1 rm x`, `watch -n1 rm x`,
		`watch -- rm x`, `watch -t rm x`, `watch "sh -c 'rm x'"`,
		`find . -ok rm x \;`, `find . -okdir rm x +`,
		`find . -exec echo hi \; -exec sudo rm x \;`,
		`echo >(rm x)`, `echo $(echo $(rm x))`, `echo $(printf '%s' ')') $(rm x)`,
		`echo "$(echo 'text' $(rm x))"`,
		"echo `echo \\`rm x\\``",
		"echo `echo \\$(rm x)`",
	}
	for _, shell := range []string{"sh", "bash", "zsh", "dash", "ksh", "mksh", "ash"} {
		for _, option := range []string{"-c", "-lc", "-ec", "-xc"} {
			lines = append(lines, shell+" "+option+" 'rm x'")
		}
	}
	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			for _, skip := range []bool{false, true} {
				cfg := denyReachConfig(skip, PermissionRule{Match: "rm *", Decision: DecisionDeny})
				if got := cfg.ResolvePermission(BashToolName, line, true); got != DecisionDeny {
					t.Errorf("skip=%v got %s", skip, got)
				}
			}
		})
	}
}

func TestDenyReachOnlyDenies(t *testing.T) {
	for _, skip := range []bool{false, true} {
		cfg := denyReachConfig(skip,
			PermissionRule{Match: "rm *", Decision: DecisionDeny},
			PermissionRule{Match: "nohup *", Decision: DecisionAllow})
		if got := cfg.ResolvePermission(BashToolName, "nohup rm -rf x", true); got != DecisionDeny {
			t.Fatalf("got %s", got)
		}
		cfg = denyReachConfig(skip, PermissionRule{Match: "rm *", Decision: DecisionAllow})
		for i, tc := range denyReachCases {
			want := tc.before[0]
			if skip {
				want = tc.before[1]
			}
			if got := cfg.ResolvePermission(BashToolName, tc.line, true); got != want {
				t.Errorf("case %d skip=%v got %s, want %s", i, skip, got, want)
			}
		}
	}
	// A normalized candidate must be the literal subject as well.
	cfg := denyReachConfig(true, PermissionRule{Match: "rm -rf x", Decision: DecisionDeny, Literal: true})
	if got := cfg.ResolvePermission(BashToolName, "sudo rm -rf x", true); got != DecisionDeny {
		t.Fatalf("literal deny: %s", got)
	}
	if got := cfg.ResolvePermission(BashToolName, "sudo rm -rf y", true); got != DecisionAllow {
		t.Fatalf("literal nonmatch: %s", got)
	}
}

func TestDenyReachDepth(t *testing.T) {
	cfg := denyReachConfig(false, PermissionRule{Match: "rm *", Decision: DecisionDeny})
	for _, depth := range []int{3, 4, 5} {
		ctx := context.WithValue(context.Background(), denyReachDepthKey{}, depth)
		if got := cfg.denyReaches(ctx, "sudo rm x", true); got != (depth < 4) {
			t.Errorf("depth %d: got %v", depth, got)
		}
	}
	line := "rm x"
	for depth := 1; depth <= 8; depth++ {
		line = "sh -c '" + strings.ReplaceAll(line, "'", "'\\''") + "'"
		want := DecisionDeny
		if depth > 4 {
			want = DecisionAsk
		}
		if got := cfg.ResolvePermission(BashToolName, line, true); got != want {
			t.Errorf("nested shells %d: got %s, want %s", depth, got, want)
		}
	}
}

func TestShellSubstitutionBodies(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{`echo $(rm x)`, []string{`rm x`}},
		{"echo `rm x`", []string{`rm x`}},
		{`cat <(rm x) >(echo y)`, []string{`rm x`, `echo y`}},
		{`echo "$(rm x)"`, []string{`rm x`}},
		{`echo '$(rm x)'`, nil},
		{"echo '`rm x`'", nil},
		{`echo "<(rm x)"`, nil},
		{`echo \$(rm x)`, nil},
		{"echo \\`rm x\\`", nil},
		{`echo "\$(rm x)"`, nil},
		{`echo $(echo $(rm x))`, []string{`echo $(rm x)`}},
		{`echo $(printf '%s' ')')`, []string{`printf '%s' ')'`}},
		{`echo $(printf "%s" ")")`, []string{`printf "%s" ")"`}},
		{`echo $(echo \); rm x)`, []string{`echo \); rm x`}},
		{`echo $((1 + (2)))`, []string{`(1 + (2))`}},
		{`echo $( (rm x) )`, []string{` (rm x) `}},
		{`echo "$(echo "$(rm x)")"`, []string{`echo "$(rm x)"`}},
		{"echo $(echo `rm x`)", []string{"echo `rm x`"}},
		{"echo `echo $(rm x)`", []string{`echo $(rm x)`}},
		{"echo `echo \\`rm x\\``", []string{"echo `rm x`"}},
		{"echo `echo \\$(rm x)`", []string{`echo $(rm x)`}},
		// Still open at the end: the rest of the text, since the segment
		// splitter may have cut the body at a ";" inside it.
		{`echo $(rm x`, []string{`rm x`}},
		{`echo $(echo $(rm x)`, []string{`echo $(rm x)`}},
		{"echo `rm x", []string{"rm x"}},
		{`echo $(rm x) $(unfinished`, []string{`rm x`, `unfinished`}},
		{`echo $(printf '%s' ')`, []string{`printf '%s' ')`}},
		{`echo $(echo ")`, []string{`echo ")`}},
		{`echo $(echo \`, []string{`echo \`}},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			if got := shellSubstitutionBodies(tc.line); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDenyReachLimitsAndLiterals(t *testing.T) {
	for _, line := range []string{
		`echo "<(rm x)"`, `echo \$(rm x)`, `echo "\$(rm x)"`,
		`sh script.sh`, `python -c 'rm x'`, `unknown-wrapper rm x`,
		`$CMD -rf x`, `$(echo rm) -rf x`, `watch echo rm x`,
		`sh --rcfile 'rm x' script.sh`, `1A=1 rm x`, `A-B=1 rm x`,
		`echo '>(rm x)'`, `echo '<(rm x)'`,
	} {
		cfg := denyReachConfig(true, PermissionRule{Match: "rm *", Decision: DecisionDeny})
		if got := cfg.ResolvePermission(BashToolName, line, true); got != DecisionAllow {
			t.Errorf("%q got %s", line, got)
		}
	}
}

// A rule for one exact command reaches it inside a subshell written
// against its words: "(touch marker)" runs "touch marker".
func TestDenyReachExactRuleInsideParens(t *testing.T) {
	on := true
	cfg := Config{SkipPermissions: &on, Permissions: Permissions{BashToolName: ToolPermission{Rules: []PermissionRule{{Match: "touch marker", Decision: DecisionDeny}}}}}
	for _, line := range []string{`(touch marker)`, `( touch marker )`, `((touch marker))`} {
		if got := cfg.ResolvePermission(BashToolName, line, true); got != DecisionDeny {
			t.Errorf("%q = %s, want deny", line, got)
		}
	}
}
