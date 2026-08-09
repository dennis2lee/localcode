package config

import "testing"

// An escaped quote must not open a quoted region, because bash does not
// treat it as one. Getting this wrong hid a whole chained command inside
// what the permission check saw as a single allowed segment:
//
//	git status \" && rm -rf ~   ->   ["git status \" && rm -rf ~"]
//
// which the built-in "git *" rule matched, auto-allowing the rm. Verified
// against real bash: the text after && does execute.
func TestSplitShellSegmentsHonorsBackslashOutsideQuotes(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{{
		name:    "escaped double quote does not open a quote",
		command: `git status \" && rm -rf ~`,
		want:    []string{`git status \"`, `rm -rf ~`},
	}, {
		name:    "escaped single quote does not open a quote",
		command: `git log \' ; echo pwned`,
		want:    []string{`git log \'`, `echo pwned`},
	}, {
		// \& is a literal ampersand, so it is not a separator; the &
		// after it still is, exactly as bash reads it.
		name:    "escaped separator is not a separator",
		command: `git status \&& rm -rf ~`,
		want:    []string{`git status \&`, `rm -rf ~`},
	}, {
		name:    "a real quoted separator still does not split",
		command: `git commit -m "fix: a; b" && echo done`,
		want:    []string{`git commit -m "fix: a; b"`, `echo done`},
	}, {
		// Inside single quotes bash does no escaping, so the quote ends
		// at the next ' and the ; that follows is a separator.
		name:    "no escaping inside single quotes",
		command: `echo 'a\' ; rm -rf ~`,
		want:    []string{`echo 'a\'`, `rm -rf ~`},
	}, {
		name:    "a windows path is not mangled",
		command: `dir C:\Users\me && echo ok`,
		want:    []string{`dir C:\Users\me`, `echo ok`},
	}, {
		name:    "trailing backslash does not run off the end",
		command: `echo hi \`,
		want:    []string{`echo hi \`},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitShellSegments(tc.command)
			if len(got) != len(tc.want) {
				t.Fatalf("split %q into %d segments %q, want %d %q", tc.command, len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("segment %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The bypass, expressed as the decision it produced: an allowed prefix
// plus an escaped quote must not auto-allow whatever follows.
func TestEscapedQuoteCannotSmuggleACommandPastAnAllowRule(t *testing.T) {
	c := &Config{}
	if got := c.resolveShellCommand(`git status \" && rm -rf ~`, true); got == DecisionAllow {
		t.Errorf("resolveShellCommand auto-allowed a chained rm behind an escaped quote")
	}
	// The allowed command on its own is unaffected.
	if got := c.resolveShellCommand(`git status`, true); got != DecisionAllow {
		t.Errorf("plain `git status` = %v, want allow", got)
	}
}
