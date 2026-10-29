package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGitRuleAllowed(t *testing.T) {
	for _, line := range []string{
		`git status`, `git diff`, `git --no-pager diff HEAD`,
		`git log --oneline -5`, `git show HEAD`, `git add -A`,
		`git add internal/config/*.go`,
		`git commit -m "fix: use -c and --exec in docs"`,
		`git commit -m 'config: x'`, `git checkout -b feature`,
		`git switch -c feature`, `git push -u origin main`,
		`git push origin main`, `git fetch origin`, `git pull --rebase`,
		`git rebase main`, `git rebase --onto main a b`,
		`git grep -n foo -- '*.go'`, `git cherry-pick -x abc123`,
		`git clone https://example.com/r.git`, `git -C sub status`,
		`git --git-dir=.git log`,
		`git -c user.name=Bot -c user.email=bot@example.com commit -m x`,
		`git -c core.quotepath=off status`,
		`git -c color.ui=never -c init.defaultBranch=main status`,
		`git -c commit.gpgSign=false -c advice.detachedHead=false status`,
		`git -c USER.NAME=Bot status`,
		`git config --get user.name`, `git config user.name --get`,
		`git config --list`, `git config get user.email`, `git config list`,
		`git config -l`, `git config --get-all user.name`,
		`git config --get-regexp '^user\.'`,
		`git config --get-urlmatch http https://example.com`,
		`git --exec-path`, `git stash list`, `git bisect start`,
		`git submodule update --init`,
		`git rebase --`, `git rebase --executive=x`,
		`git grep -o foo`, `git rebase -Xours main`,
		`git rebase \*`, `git rebase '*'`, `git rebase "*"`,
		`git -C 'my dir' status`, `git --git-dir .git status`,
		`git grep -n "Error$" -- '*.go'`, `git grep -n "a$" -- x`,
		`git commit -m "costs $5"`, `git log --grep "$X"`,
	} {
		t.Run(line, func(t *testing.T) {
			if got := (&Config{}).ResolvePermission(BashToolName, line, true); got != DecisionAllow {
				t.Errorf("ResolvePermission(%q) = %v, want allow", line, got)
			}
		})
	}
}

func TestGitRuleAsked(t *testing.T) {
	for _, line := range []string{
		`git -c alias.x='!touch pwned' x`,
		`git -c core.fsmonitor='touch pwned' status`,
		`git rebase --exec 'touch pwned' HEAD~1`,
		`git submodule foreach 'touch pwned'`,
		`git bisect run touch pwned`,
		`git config alias.x '!touch pwned'`,
		"git config alias.x '!touch pwned'\ngit x",
		`git -c core.pager='touch pwned' log`,
		`git -c core.sshCommand='touch pwned' fetch origin`,
		`git -c core.hooksPath=/tmp/h commit -m x`,
		`git -c diff.external='touch pwned' diff`,
		`git -c credential.helper='!touch pwned' fetch`,
		`git -c Alias.X='!touch pwned' x`,
		`git --config-env=core.pager=SHELL log`,
		`git --config-env core.pager=SHELL log`,
		`git --exec-path=/tmp/x status`,
		`git $'-c' alias.x='!touch pwned' x`,
		`git '-c' alias.x='!touch pwned' x`,
		`git \-c alias.x='!touch pwned' x`,
		`git -C 'my dir' submodule foreach 'touch pwned'`,
		`git --git-dir .git submodule foreach 'touch pwned'`,
		`git config --global core.fsmonitor 'touch pwned'`,
		`git config core.hooksPath h`,
		`git submodule --quiet foreach 'touch pwned'`,
		`git rebase -x 'touch pwned' HEAD~1`,
		`git rebase -qx 'touch pwned' HEAD~1`,
		`git rebase --exe='touch pwned' HEAD~1`,
		`git rebase --exec='touch pwned' HEAD~1`, `git rebase * `,
		`git grep -O'touch pwned' foo`,
		`git grep --open-files-in-pager='touch pwned' foo`, `git grep foo *`,
		`git clone -u 'touch pwned' . x`,
		`git clone --upload-pack='touch pwned' . x`,
		`git clone --template=/tmp/t . x`,
		`git clone -c core.fsmonitor='touch pwned' . x`,
		`git init --template=/tmp/t x`,
		`git fetch --upload-pack='touch pwned' .`,
		`git pull --upload-pack='touch pwned' .`,
		`git ls-remote --upload-pack='touch pwned' .`,
		`git ls-remote -u 'touch pwned' .`,
		`git push --receive-pack='touch pwned' . HEAD:x`,
		`git push --exec='touch pwned' . HEAD:x`,
		`git archive --remote=. --exec='touch pwned' HEAD`,
		`git filter-branch --tree-filter 'touch pwned' HEAD`,
		`git filter-repo --commit-callback x`,
		`git difftool -x 'touch pwned' HEAD`, `git mergetool`,
		`git send-email x.patch`, `git instaweb --httpd='touch pwned'`,
		`git daemon`, `git hook run pre-commit`,
		`git {-c,alias.x=!touch\ pwned} x`,
		`git status && git -c alias.x='!touch pwned' x`,
		`git $OPTION status`, `git $SUBCOMMAND`, `git -C $DIR status`,
		`git -c user.name=$NAME status`,
		`git -c advice.some.command=x status`, `git -c advice.=x status`,
		`git -c`, `git config`,
		"git --git-\\\ndir .git submodule foreach 'touch pwned'",
		`git "$OPT" alias.x='!touch pwned' x`,
		`git rebase "$OPT" HEAD~1`, `git rebase "${OPT}" HEAD~1`,
		`git submodule $ACTION 'touch pwned'`,
		`git submodule "$ACTION" 'touch pwned'`,
		`git bisect $ACTION touch pwned`,
		`git config $ACTION user.name x`,
		`git submodule--helper foreach 'touch pwned'`,
		`git remote-ext x 'sh -c touch% pwned' <<< 'connect git-upload-pack'`,
		`git merge-index ./m.sh -a`,
		`git for-each-repo --config=submodule.sub.url -- -c alias.x='!touch pwned' x`,
	} {
		t.Run(line, func(t *testing.T) {
			if got := (&Config{}).ResolvePermission(BashToolName, line, true); got != DecisionAsk {
				t.Errorf("ResolvePermission(%q) = %v, want ask", line, got)
			}
		})
	}
}

func TestGitRuleOptionRows(t *testing.T) {
	rows := []struct {
		command string
		options []string
	}{
		{"rebase", []string{"--exec=x", "--exe=x", "-x", "-qx"}},
		{"grep", []string{"--open-files-in-pager=x", "--open=x", "-O", "-nO"}},
		{"clone", []string{"--upload-pack=x", "--config=x", "--template=x", "-u", "-c", "-qux"}},
		{"init", []string{"--template=x", "--templ=x"}},
		{"fetch", []string{"--upload-pack=x", "--upload=x"}},
		{"pull", []string{"--upload-pack=x", "--upload=x"}},
		{"ls-remote", []string{"--upload-pack=x", "-u", "-qux"}},
		{"push", []string{"--receive-pack=x", "--exec=x", "--rece=x"}},
		{"archive", []string{"--exec=x", "--exe=x"}},
	}
	for _, row := range rows {
		for _, arg := range append(row.options, "$OPTION", "*", "?", "[ab]", "{a,b}") {
			line := "git " + row.command + " " + arg
			t.Run(line, func(t *testing.T) {
				if got := (&Config{}).ResolvePermission(BashToolName, line, true); got != DecisionAsk {
					t.Errorf("ResolvePermission(%q) = %v, want ask", line, got)
				}
			})
		}
	}
}

func TestGitRuleConfigWritesOverrideReads(t *testing.T) {
	for _, write := range []string{
		"--add", "--replace-all", "--unset", "--unset-all", "--rename-section",
		"--remove-section", "--edit", "-e", "set", "unset", "rename-section", "remove-section", "edit",
	} {
		line := "git config --get " + write + " user.name"
		if got := (&Config{}).ResolvePermission(BashToolName, line, true); got != DecisionAsk {
			t.Errorf("ResolvePermission(%q) = %v, want ask", line, got)
		}
	}
}

func TestGitRuleGlobalValues(t *testing.T) {
	for _, option := range []string{"-C", "--git-dir", "--work-tree", "--namespace", "--attr-source", "--shallow-file"} {
		line := "git " + option + " value submodule foreach 'touch pwned'"
		if got := (&Config{}).ResolvePermission(BashToolName, line, true); got != DecisionAsk {
			t.Errorf("ResolvePermission(%q) = %v, want ask", line, got)
		}
	}
}

func TestGitRuleOverrideAndGrants(t *testing.T) {
	line := `git -c alias.x='!touch pwned' x`
	var cfg Config
	if err := json.Unmarshal([]byte(`{"permission":{"bash":[{"match":"git *","decision":"allow"}]}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvePermission(BashToolName, line, true); got != DecisionAsk {
		t.Errorf("persisted allow = %v, want ask", got)
	}
	on := true
	cfg.SkipPermissions = &on
	if got := cfg.ResolvePermission(BashToolName, line, true); got != DecisionAllow {
		t.Errorf("skip permissions = %v, want allow", got)
	}
	cfg.Permissions[BashToolName] = ToolPermission{Rules: []PermissionRule{{Match: "git *", Decision: DecisionDeny}}}
	for _, skip := range []bool{false, true} {
		cfg.SkipPermissions = &skip
		if got := cfg.ResolvePermission(BashToolName, line, true); got != DecisionDeny {
			t.Errorf("deny with skip=%v = %v, want deny", skip, got)
		}
	}
	if segments, ok := GrantSegments(line); ok || segments != nil {
		t.Errorf("GrantSegments = %q, %v, want nil, false", segments, ok)
	}
	if segments, ok := GrantSegments("git status | head -5"); !ok || !reflect.DeepEqual(segments, []string{"git status", "head -5"}) {
		t.Errorf("ordinary GrantSegments = %q, %v", segments, ok)
	}
}

func TestLineRunsGitCommandReadings(t *testing.T) {
	for _, line := range []string{
		`/usr/bin/git -c alias.x='!touch pwned' x`,
		`"git" -c alias.x='!touch pwned' x`,
		`g'i't -c alias.x='!touch pwned' x`,
		`C:\\bin\\git.exe hook run pre-commit`,
		"g\\\nit -c alias.x='!touch pwned' x",
		`git log $'\'' && git hook run pre-commit # '`,
	} {
		if !lineRunsGitCommand(line) {
			t.Errorf("lineRunsGitCommand(%q) = false", line)
		}
	}
	if lineRunsGitCommand("echo git hook run pre-commit") {
		t.Error("non-Git command treated as Git")
	}
}

func TestShellWords(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []shellWord
	}{
		{"", nil},
		{" \tgit\nstatus ", []shellWord{{"git", false}, {"status", false}}},
		{`git -C 'my dir' "sta"tus ''`, []shellWord{{"git", false}, {"-C", false}, {"my dir", false}, {"status", false}, {"", false}}},
		{`git \-c user.name=My\ Name`, []shellWord{{"git", false}, {"-c", false}, {"user.name=My Name", false}}},
		{`"a\"b" 'a\b'`, []shellWord{{`a"b`, false}, {`a\b`, false}}},
		{`$'-c' $VAR ${VAR}`, []shellWord{{"$-c", true}, {"$VAR", true}, {"${VAR}", true}}},
		{`*.go ? [ab] {a,b}`, []shellWord{{"*.go", true}, {"?", true}, {"[ab]", true}, {"{a,b}", true}}},
		{`\$VAR \* \? \[ \{`, []shellWord{{"$VAR", false}, {"*", false}, {"?", false}, {"[", false}, {"{", false}}},
		{`'$VAR' "*?[{"`, []shellWord{{"$VAR", false}, {"*?[{", false}}},
		{`"$OPT" "${OPT}" "$1" "$@" "$(x)"`, []shellWord{{"$OPT", true}, {"${OPT}", true}, {"$1", true}, {"$@", true}, {"$(x)", true}}},
		{`"Error$" "a$ b" "\$OPT" "$"`, []shellWord{{"Error$", false}, {"a$ b", false}, {"$OPT", false}, {"$", false}}},
		{`git 'unfinished * words`, []shellWord{{"git", false}, {"unfinished * words", false}}},
		{`git "unfinished words`, []shellWord{{"git", false}, {"unfinished words", false}}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			if got := shellWords(tc.line); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("shellWords(%q) = %#v, want %#v", tc.line, got, tc.want)
			}
		})
	}
}
