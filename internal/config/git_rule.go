package config

import (
	"strings"
	"unicode"
)

// shellWord is one word of a command with its quoting removed. expands is
// true when the shell would change it before the program sees it: a
// parameter ($VAR, ${VAR}, "$VAR"), a glob (*, ?, [) or a brace list. What
// such a word becomes is not knowable here, so a check that depends on
// the word treats it as anything at all.
type shellWord struct {
	text    string
	expands bool
}

// shellWords splits a segment into words the way a POSIX shell does,
// removing quotes as unquoteShellText does. It expands nothing. An
// unquoted $ is marked whatever follows it, which covers $'...' without
// decoding it: $'-c' is marked, not misread as a word that is not -c.
func shellWords(segment string) []shellWord {
	var words []shellWord
	var text strings.Builder
	var quote rune
	var started, expands bool
	flush := func() {
		if started {
			words = append(words, shellWord{text.String(), expands})
		}
		text.Reset()
		started, expands = false, false
	}
	runes := []rune(segment)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				text.WriteRune(r)
			}
		case quote == '"':
			if r == '\\' && i+1 < len(runes) {
				i++
				text.WriteRune(runes[i])
			} else if r == '"' {
				quote = 0
			} else {
				// A parameter still expands inside double quotes:
				// "$OPT" is one word, and that word can be -c. A $
				// that starts nothing, as in "Error$", is literal.
				expands = expands || r == '$' && i+1 < len(runes) && startsExpansion(runes[i+1])
				text.WriteRune(r)
			}
		case r == '\\':
			started = true
			if i+1 < len(runes) {
				i++
				text.WriteRune(runes[i])
			}
		case r == '\'' || r == '"':
			started = true
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			started = true
			expands = expands || strings.ContainsRune("$*?[{", r)
			text.WriteRune(r)
		}
	}
	flush()
	return words
}

// startsExpansion reports whether r, right after a $ inside double quotes,
// makes the $ expand: a name, a positional or special parameter, ${...}
// or $(...).
func startsExpansion(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("{(@*#?$!-", r)
}

// lineRunsGitCommand reports whether a bash line has a git command in it
// that makes git run another command: git -c alias.x='!cmd' x, git rebase
// --exec cmd, git submodule foreach cmd, and the rest of the table below.
//
// It is checked beside hasUnsafeShellConstruct and for the same reason. A
// rule names a program, and such a line runs a program the rule never
// named, so no rule's allow covers it: not the built-in "git *", and not
// a "git *" in the user's config.json, which "always" writes for any
// asked line that starts with git. A session grant does not cover it
// either; see GrantSegments.
//
// Every segment of every reading is checked (shellSegments), so the
// reading the shell uses is among them.
func lineRunsGitCommand(command string) bool {
	for _, segment := range shellSegments(command) {
		words := shellWords(segment)
		if len(words) > 0 && normalizeProgram(words[0].text) == "git" && gitWordsRunCommand(words[1:]) {
			return true
		}
	}
	return false
}

// gitWordsRunCommand walks git's own options, the words before the
// subcommand, and then hands the rest to gitSubcommandRunsCommand.
//
// git takes these options exactly as spelled, with no abbreviation, and
// refuses one it does not know, so the first word that is not an option is
// the subcommand provided every option that takes a separate value is in
// the list below. One missing from it would make its value look like the
// subcommand.
func gitWordsRunCommand(words []shellWord) bool {
	for i := 0; i < len(words); i++ {
		word := words[i]
		if word.expands {
			return true
		}
		if !strings.HasPrefix(word.text, "-") {
			// Lowercased because git finds a subcommand it has no
			// builtin for as a git-<name> program, and on a
			// case-insensitive file system (macOS by default) "git
			// Submodule foreach" runs git-submodule.
			return gitSubcommandRunsCommand(strings.ToLower(word.text), words[i+1:])
		}
		if word.text == "--config-env" || strings.HasPrefix(word.text, "--config-env=") {
			return true
		}
		if strings.HasPrefix(word.text, "--exec-path=") {
			return true
		}
		// Git 2.50.1 handle_options consumes these separate values, including
		// the internal --shallow-file option. It rejects attached -c values.
		switch word.text {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--attr-source", "--shallow-file":
			i++
			if i >= len(words) {
				return word.text == "-c"
			}
			if words[i].expands || (word.text == "-c" && !safeGitConfigKey(words[i].text)) {
				return true
			}
		}
	}
	return false
}

// safeGitConfigKey reports whether a git -c setting is one an agent sets
// for an ordinary command (an identity for a commit, colour, pull's merge
// mode) and that names no program.
//
// This is an allow list on purpose. Git has dozens of keys that name a
// command (alias.*, core.fsmonitor, core.pager, core.editor,
// core.sshCommand, diff.external, credential.helper, gpg.program, ...), so
// a list of the dangerous ones would never be complete. The color section
// holds only colours and switches, so all of it is here.
func safeGitConfigKey(value string) bool {
	key, _, _ := strings.Cut(value, "=")
	key = strings.ToLower(key)
	switch key {
	case "user.name", "user.email", "core.quotepath", "init.defaultbranch", "commit.gpgsign",
		"pull.rebase", "pull.ff":
		return true
	}
	if name, ok := strings.CutPrefix(key, "color."); ok {
		return name != ""
	}
	name, advice := strings.CutPrefix(key, "advice.")
	return advice && name != "" && !strings.Contains(name, ".")
}

// gitSubcommandRunsCommand decides a subcommand from the words after it.
//
// The ones that exist to run something are asked outright. remote-ext runs
// its URL as a command once it reads "connect" on its input, which a
// here-string supplies; merge-index runs the program it is given;
// for-each-repo runs any git arguments, -c included, in every repository
// a configuration key lists. svn runs --authors-prog, and parses its
// options with Perl's Getopt::Long, which takes abbreviations and a single
// dash, so the whole subcommand is asked rather than one spelling of it.
// submodule--helper is the builtin behind submodule and takes foreach the
// same way.
//
// fetch-pack and send-pack are the plumbing under fetch and push, and take
// the program to run on the other end the same way.
//
// For the rest of the table, a word that expands is asked as well, since
// a glob or a variable can produce the option or the action word.
func gitSubcommandRunsCommand(command string, words []shellWord) bool {
	switch command {
	case "filter-branch", "filter-repo", "difftool", "mergetool", "send-email", "instaweb", "daemon", "hook",
		"remote-ext", "merge-index", "for-each-repo", "svn":
		return true
	case "config", "submodule", "submodule--helper", "bisect":
		for _, word := range words {
			if word.expands {
				return true
			}
		}
		switch command {
		case "config":
			return !gitConfigRead(words)
		case "bisect":
			return gitWordsInclude(words, "run")
		default:
			return gitWordsInclude(words, "foreach")
		}
	}
	var long []string
	var short string
	switch command {
	case "rebase":
		long, short = []string{"exec"}, "x"
	case "grep":
		long, short = []string{"open-files-in-pager"}, "O"
	case "clone":
		long, short = []string{"upload-pack", "config", "template"}, "uc"
	case "init":
		long = []string{"template"}
	case "fetch", "pull":
		long = []string{"upload-pack"}
	case "ls-remote":
		long, short = []string{"upload-pack"}, "u"
	case "push", "send-pack":
		long = []string{"receive-pack", "exec"}
	case "fetch-pack":
		long = []string{"upload-pack", "exec"}
	case "archive":
		long = []string{"exec"}
	default:
		return false
	}
	for i, word := range words {
		if command == "grep" && word.text == "--" && (i == 0 || !strings.HasPrefix(words[i-1].text, "-")) {
			// git grep reads every word after "--" as a path, so a glob
			// there ("git grep -n foo -- *.go") names files and cannot
			// become -O. Not after an option, which may take the "--" as
			// its value: "git grep -e -- -Ocmd" runs cmd. And grep only:
			// "git pull . -- --upload-pack=cmd" runs cmd, because pull
			// hands what follows its "--" to fetch.
			return false
		}
		if word.expands || gitCommandOption(word.text, long, short) {
			return true
		}
	}
	return false
}

func gitWordsInclude(words []shellWord, text string) bool {
	for _, word := range words {
		if word.text == text {
			return true
		}
	}
	return false
}

// gitCommandOption reports whether word is one of a subcommand's long
// options, spelled whole or abbreviated (parse-options takes "--exe" for
// "--exec"), or a run of short options that includes one of the letters
// (it takes "-qx cmd" as "-q -x cmd").
func gitCommandOption(word string, long []string, short string) bool {
	if name, ok := strings.CutPrefix(word, "--"); ok {
		name, _, _ = strings.Cut(name, "=")
		for _, option := range long {
			if name != "" && strings.HasPrefix(option, name) {
				return true
			}
		}
		return false
	}
	return strings.HasPrefix(word, "-") && len(word) > 1 && strings.ContainsAny(word[1:], short)
}

// gitConfigRead reports whether a git config command only reads. A write
// is how an alias or a hook path gets into the configuration for a later,
// ordinary-looking git command to run.
func gitConfigRead(words []shellWord) bool {
	var read bool
	var operands []string
	for _, word := range words {
		switch word.text {
		case "--add", "--replace-all", "--unset", "--unset-all", "--rename-section", "--remove-section", "--edit", "-e", "set", "unset", "rename-section", "remove-section", "edit":
			return false
		case "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l":
			read = true
		}
		if !strings.HasPrefix(word.text, "-") {
			operands = append(operands, word.text)
		}
	}
	if len(operands) > 0 && (operands[0] == "get" || operands[0] == "list") {
		return true
	}
	// "git config user.name" reads; "git config user.name x" writes. An
	// option's value is counted as an operand too, which can only make a
	// read look like a write and ask, never the other way.
	return read || len(operands) == 1
}
