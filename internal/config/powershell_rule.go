package config

import (
	"context"
	"encoding/base64"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
)

// A PowerShell line is not a bash line, and the bash split misses where
// PowerShell starts a command. Each of these ran New-Item, and each was
// asked about rather than denied under a deny rule "New-Item *", which
// skip_permissions then turned into an allow:
//
//	Write-Output safe\; New-Item marker      (a backslash escapes nothing)
//	Write-Output "a\"; New-Item marker"      (nor inside double quotes)
//	1 | % {New-Item marker}                  (a script block)
//	Write-Output (New-Item marker)           (a parenthesised pipeline)
//	$x = New-Item marker                     (an assignment)
//	iex 'New-Item marker'                    (a string run as a command)
//
// So the deny rules that name a command are also matched against the
// line read as PowerShell might run it (powershellDenied). The line is
// cut at every character that can end a command or start one, inside
// quotes too: ; | & and a line end between commands, ( ) { } around
// nested ones (and $( ) inside a double-quoted string). Blind to quotes
// on purpose: double-quoted strings run what is inside $( ), PowerShell
// takes four kinds of quote mark for each of its two, and a cut too many
// only makes a rule that names a command match more. A piece of a quoted
// argument can then match one ("a; New-Item b" in a commit message), and
// the line is denied though it would not have run the command: the safe
// direction for a rule somebody wrote to forbid it.
//
// Only rules that name something take part. A rule that is a wildcard
// alone ("*") matches every piece, and pieces are not all commands: a
// piece of "git log --format=%h (%s)" is "%s". Such a rule keeps
// deciding the line as split for bash, as before.

// powershellBreaks are the characters a PowerShell line is cut at.
const powershellBreaks = ";|&(){}\n\r"

// powershellLead are words a PowerShell command can follow on its piece:
// keywords that take a pipeline, and the operators that negate one.
var powershellLead = map[string]bool{
	"return": true, "throw": true, "-not": true, "!": true, "exit": true,
	"do": true, "else": true, "elseif": true, "try": true, "finally": true,
	"begin": true, "process": true, "end": true, "default": true, ".": true,
}

var (
	// An assignment's target: a variable, a hashtable key, a cast before
	// either, and an operator that assigns.
	powershellAssign = regexp.MustCompile(`^(\[[^\]]*\]\s*)*\$?[\w:.\[\]]+\s*[-+*/%?]?\?*=\s*`)
	// A redirection before the command: "2>&1" is cut at its "&".
	powershellRedirect = regexp.MustCompile(`^(\d|\*)?>>?`)
	// The quoted strings of a line, for what is piped to Invoke-Expression.
	powershellQuoted = regexp.MustCompile(`'[^']*'|"[^"]*"`)
)

// powershellQuotes are the quote marks PowerShell reads: ' and " and
// their typographic forms, each a quote of the same kind.
const powershellQuotes = "'\"‘’‚‛“”„"

// powershellSplit cuts line at powershellBreaks, but not at one escaped
// with a backtick: "a`;b" is one argument to PowerShell, and "a", two
// backticks and ";b" are two commands (an escaped backtick, then ";").
func powershellSplit(line string) []string {
	var out []string
	var b strings.Builder
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '`' && i+1 < len(runes):
			b.WriteRune(r)
			b.WriteRune(runes[i+1])
			i++
		case strings.ContainsRune(powershellBreaks, r):
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	return append(out, b.String())
}

// powershellPieces is a PowerShell line cut by powershellSplit, each
// piece without the assignment, keywords, casts ([void]), call operator
// (".") and redirections before its command. Tabs are spaces.
func powershellPieces(line string) []string {
	var out []string
	for _, piece := range powershellSplit(line) {
		piece = strings.TrimSpace(strings.ReplaceAll(piece, "\t", " "))
		for piece != "" {
			trimmed := powershellAssign.ReplaceAllString(piece, "")
			if strings.HasPrefix(trimmed, "[") {
				if end := strings.IndexByte(trimmed, ']'); end > 0 {
					trimmed = trimmed[end+1:]
				}
			}
			if word, rest, ok := strings.Cut(trimmed, " "); ok {
				switch {
				case powershellLead[strings.ToLower(word)]:
					trimmed = rest
				case powershellRedirect.MatchString(word):
					trimmed = rest
					if powershellRedirect.FindString(word) == word {
						// The target is the next word.
						_, trimmed, _ = strings.Cut(strings.TrimSpace(rest), " ")
					}
				}
			}
			trimmed = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(trimmed), "@$"))
			if trimmed == piece {
				break
			}
			piece = trimmed
		}
		if piece != "" {
			out = append(out, piece)
		}
	}
	return out
}

// powershellReadings are the texts a piece is matched as: as written,
// without its quote marks, without PowerShell's escape character too
// (Ne`w-Item is New-Item), and with its program named without a module
// or a directory (Microsoft.PowerShell.Management\New-Item is New-Item).
func powershellReadings(piece string) []string {
	out := []string{piece}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	unquoted := strings.Map(func(r rune) rune {
		if strings.ContainsRune(powershellQuotes, r) {
			return -1
		}
		return r
	}, piece)
	add(unquoted)
	plain := strings.ReplaceAll(unquoted, "`", "")
	add(plain)
	word, rest, _ := strings.Cut(strings.TrimSpace(plain), " ")
	base := programBase(word)
	add(strings.TrimSpace(base + " " + rest))
	// A built-in alias is the command it names: "ni marker" is New-Item.
	if cmdlet, ok := powershellAliases[strings.ToLower(base)]; ok {
		add(strings.TrimSpace(cmdlet + " " + rest))
	}
	// A name that is no command is looked for again with Get- before it:
	// "content x" runs Get-Content.
	if base != "" && !strings.Contains(base, "-") {
		add(strings.TrimSpace("Get-" + base + " " + rest))
	}
	return out
}

// powershellAliases are the aliases Windows PowerShell 5.1 and PowerShell
// 7 define for their own commands, and the functions that are a command
// with arguments (mkdir is New-Item). One defined later (Set-Alias) is a
// line that is not a plain command, asked about under skip_permissions.
var powershellAliases = map[string]string{
	"%": "ForEach-Object", "?": "Where-Object", "ac": "Add-Content", "cat": "Get-Content",
	"cd": "Set-Location", "chdir": "Set-Location", "clc": "Clear-Content", "clear": "Clear-Host",
	"cli": "Clear-Item", "clp": "Clear-ItemProperty", "cls": "Clear-Host", "clv": "Clear-Variable",
	"compare": "Compare-Object", "copy": "Copy-Item", "cp": "Copy-Item", "cpi": "Copy-Item",
	"cpp": "Copy-ItemProperty", "curl": "Invoke-WebRequest", "del": "Remove-Item", "diff": "Compare-Object",
	"dir": "Get-ChildItem", "echo": "Write-Output", "epal": "Export-Alias", "epcsv": "Export-Csv",
	"erase": "Remove-Item", "foreach": "ForEach-Object", "gc": "Get-Content", "gci": "Get-ChildItem",
	"gcm": "Get-Command", "gi": "Get-Item", "gl": "Get-Location", "gp": "Get-ItemProperty",
	"gps": "Get-Process", "gsv": "Get-Service", "gv": "Get-Variable", "gwmi": "Get-WmiObject",
	"icm": "Invoke-Command", "iex": "Invoke-Expression", "ihy": "Invoke-History", "ii": "Invoke-Item",
	"ipal": "Import-Alias", "ipcsv": "Import-Csv", "ipmo": "Import-Module", "irm": "Invoke-RestMethod",
	"iwmi": "Invoke-WmiMethod", "iwr": "Invoke-WebRequest", "kill": "Stop-Process", "ls": "Get-ChildItem",
	"md": "New-Item -ItemType Directory", "mkdir": "New-Item -ItemType Directory",
	"mi": "Move-Item", "mount": "New-PSDrive", "move": "Move-Item", "mp": "Move-ItemProperty",
	"mv": "Move-Item", "nal": "New-Alias", "ndr": "New-PSDrive", "ni": "New-Item", "nmo": "New-Module",
	"nv": "New-Variable", "popd": "Pop-Location", "ps": "Get-Process", "pushd": "Push-Location",
	"pwd": "Get-Location", "r": "Invoke-History", "rd": "Remove-Item", "rdr": "Remove-PSDrive",
	"ren": "Rename-Item", "ri": "Remove-Item", "rm": "Remove-Item", "rmdir": "Remove-Item",
	"rni": "Rename-Item", "rnp": "Rename-ItemProperty", "rp": "Remove-ItemProperty", "rv": "Remove-Variable",
	"rvpa": "Resolve-Path", "rwmi": "Remove-WmiObject", "sajb": "Start-Job", "sal": "Set-Alias",
	"saps": "Start-Process", "sasv": "Start-Service", "sc": "Set-Content", "select": "Select-Object",
	"set": "Set-Variable", "si": "Set-Item", "sl": "Set-Location", "sleep": "Start-Sleep",
	"sls": "Select-String", "sort": "Sort-Object", "sp": "Set-ItemProperty", "spps": "Stop-Process",
	"spsv": "Stop-Service", "start": "Start-Process", "sv": "Set-Variable", "swmi": "Set-WmiInstance",
	"tee": "Tee-Object", "type": "Get-Content", "wget": "Invoke-WebRequest", "where": "Where-Object",
	"write": "Write-Output",
}

// programBase is a program's name without its module or directory and
// without an executable extension, in the case it was written in.
func programBase(word string) string {
	base := baseName(word)
	lower := strings.ToLower(base)
	for _, ext := range []string{".exe", ".com", ".bat", ".cmd"} {
		if strings.HasSuffix(lower, ext) {
			return base[:len(base)-len(ext)]
		}
	}
	return base
}

// powershellUnescape is a line with PowerShell's escapes read as a
// double-quoted string reads them: `n and `r a line end, `t a space, a
// backtick before a line end a line continuation. An escaped break stays
// escaped, for powershellSplit.
func powershellUnescape(line string) string {
	var b strings.Builder
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '`' || i+1 >= len(runes) {
			b.WriteRune(runes[i])
			continue
		}
		i++
		switch runes[i] {
		case 'n', 'r', '\n', '\r':
			if runes[i] == '\r' && i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			if runes[i] == 'n' || runes[i] == 'r' {
				b.WriteRune('\n')
			} else {
				b.WriteRune(' ')
			}
		case 't', 'v', 'f':
			b.WriteRune(' ')
		case '0', 'a', 'b', 'e':
		default:
			if strings.ContainsRune(powershellBreaks, runes[i]) || runes[i] == '`' {
				b.WriteRune('`')
			}
			b.WriteRune(runes[i])
		}
	}
	return b.String()
}

// powershellDenied reports a deny rule for the powershell tool that names
// a command and matches a piece of line, read every way
// powershellReadings has, with its escapes as written and as a double-
// quoted string reads them, and in the text a piece hands to another
// interpreter (powershellScripts). ctx carries the agent.
func (c *Config) powershellDenied(ctx context.Context, line string) bool {
	return c.powershellDeniedAt(ctx, line, 0)
}

func (c *Config) powershellDeniedAt(ctx context.Context, line string, depth int) bool {
	if depth > 4 {
		return false
	}
	lines := []string{line}
	if u := powershellUnescape(line); u != line {
		lines = append(lines, u)
	}
	for _, l := range lines {
		pieces := powershellPieces(l)
		for _, piece := range pieces {
			for _, r := range powershellReadings(piece) {
				if c.powershellNamedDeny(ctx, r) {
					return true
				}
			}
			for _, script := range powershellScripts(piece) {
				if c.powershellDeniedAt(ctx, script, depth+1) {
					return true
				}
			}
			// A string piped to Invoke-Expression: every quoted string of
			// the line is taken for one.
			if p := powershellWords(piece); len(p) > 0 && isInvokeExpression(p[0]) {
				for _, q := range powershellQuoted.FindAllString(l, -1) {
					if c.powershellDeniedAt(ctx, q[1:len(q)-1], depth+1) {
						return true
					}
				}
			}
		}
	}
	return false
}

func isInvokeExpression(word string) bool {
	switch normalizeProgram(word) {
	case "iex", "invoke-expression":
		return true
	}
	return false
}

// powershellNamedDeny reports text denied by a rule for the powershell
// tool that names a command, in any case: the last rule that matches it
// decides, as for any tool, and denies it here only when it is a deny
// that names something (not a wildcard alone, and not a flat decision
// for the whole tool). The running agent's rules first, as
// resolveOneStrict reads them: one of its that matches decides.
func (c *Config) powershellNamedDeny(ctx context.Context, text string) bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	// The tool's own rules, then those for every tool ("*"), as
	// resolveRules reads them, each in any case.
	decide := func(perms map[string]ToolPermission) (Decision, bool) {
		for _, key := range []string{PowerShellToolName, "*"} {
			if tp, ok := perms[key]; ok {
				if d, matched := namedRules(tp, text); matched {
					return d, true
				}
			}
		}
		return "", false
	}
	if name := c.AgentFor(ctx); name != "" && c.Agents != nil {
		if agent, ok := c.Agents[name]; ok {
			if d, matched := decide(agent.Permission); matched {
				return d == DecisionDeny
			}
		}
	}
	d, matched := decide(c.Permissions)
	return matched && d == DecisionDeny
}

func namedRules(tp ToolPermission, text string) (Decision, bool) {
	if tp.Flat != "" {
		// A decision for the whole tool names nothing: an allow decides,
		// a deny or an ask is left to the line as split for bash.
		return tp.Flat, tp.Flat == DecisionAllow
	}
	folded := strings.ToLower(text)
	var last *PermissionRule
	for i, r := range tp.Rules {
		if (r.Literal && r.Match == text) || (!r.Literal && globMatch(strings.ToLower(r.Match), folded)) {
			last = &tp.Rules[i]
		}
	}
	if last == nil {
		return "", false
	}
	if last.Decision == DecisionDeny && !last.Literal && strings.Trim(last.Match, "*? ") == "" {
		return DecisionAsk, true
	}
	return last.Decision, true
}

// PowerShellNeedsAPerson reports a powershell line that skip_permissions
// and skip_tools do not answer: one that is more than one plain command,
// while a deny rule for the tool is set. The deny rules are matched
// against a PowerShell line as PowerShell might run it (powershellDenied),
// and PowerShell is a language: a command can be reached through a
// variable, an alias, an escape or a string handed to another
// interpreter, and a reading of the line cannot promise to find every
// one. A person reading the line can. Without a deny rule there is
// nothing to hold, and skip_permissions answers as it does for bash.
func (c *Config) PowerShellNeedsAPerson(ctx context.Context, line string) bool {
	return c.powershellHasDeny(ctx) && !powershellSimple(line)
}

// powershellHasDeny reports a deny rule that applies to the powershell
// tool: its own, the running agent's, or one for every tool ("*").
func (c *Config) powershellHasDeny(ctx context.Context) bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	has := func(perms map[string]ToolPermission) bool {
		for _, key := range []string{PowerShellToolName, "*"} {
			tp, ok := perms[key]
			if !ok {
				continue
			}
			if tp.Flat == DecisionDeny {
				return true
			}
			for _, r := range tp.Rules {
				if r.Decision == DecisionDeny {
					return true
				}
			}
		}
		return false
	}
	if name := c.AgentFor(ctx); name != "" && c.Agents != nil {
		if agent, ok := c.Agents[name]; ok && has(agent.Permission) {
			return true
		}
	}
	return has(c.Permissions)
}

// powershellRunsOthers are programs that run or define other commands:
// interpreters, what runs a string or a file as code, and what names a
// command anew. A line that starts with one is not a plain command.
var powershellRunsOthers = map[string]bool{
	"iex": true, "invoke-expression": true, "icm": true, "invoke-command": true,
	"powershell": true, "pwsh": true, "cmd": true, "start-process": true, "saps": true,
	"start": true, "start-job": true, "sajb": true, "invoke-item": true, "ii": true,
	"source": true, "import-module": true, "ipmo": true,
	"set-alias": true, "sal": true, "new-alias": true, "nal": true,
	"import-alias": true, "ipal": true, "new-module": true, "nmo": true,
	"invoke-history": true, "ihy": true, "r": true,
	"sh": true, "bash": true, "zsh": true, "wsl": true, "python": true, "python3": true,
	"py": true, "node": true, "perl": true, "ruby": true, "wscript": true, "cscript": true,
	"mshta": true, "rundll32": true, "regsvr32": true, "schtasks": true, "forfiles": true,
	"wmic": true, "msiexec": true, "runas": true,
}

// powershellSimple reports a line that is one plain command: letters,
// digits, spaces and the characters no shell reads as an operator, an
// escape, a substitution or a grouping (plainShellRunes); a program that
// is a name, not a path, a script or a call operator, and is not one of
// powershellRunsOthers; no function: or alias: path, through which a
// plain command defines another; and no splatted variable (@args).
//
// It holds the line, not what a plain command arranges for a later one:
// a script written and put on PATH, a drive of the Alias or Function
// provider under another name, a profile the next terminal loads, or a
// program that starts another (WMI, conhost, git's configured hooks).
// docs/USAGE.md says so.
func powershellSimple(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	for _, r := range line {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(plainShellRunes, r) {
			return false
		}
	}
	lower := strings.ToLower(line)
	if strings.Contains(lower, "function:") || strings.Contains(lower, "alias:") {
		return false
	}
	for _, w := range strings.Fields(line) {
		if strings.HasPrefix(w, "@") {
			// Splatting: the arguments are a variable's.
			return false
		}
	}
	words := powershellWords(line)
	if len(words) == 0 {
		return false
	}
	first := words[0]
	if strings.ContainsAny(first, `/\.'"`) || first != strings.Fields(line)[0] {
		return false
	}
	return !powershellRunsOthers[normalizeProgram(first)]
}

// powershellWords is a piece cut at whitespace outside quotes, its quote
// marks removed: a quoted path with a space in it is one word.
func powershellWords(piece string) []string {
	var out []string
	var b strings.Builder
	var quote rune
	in := false
	flush := func() {
		if in {
			out = append(out, b.String())
		}
		b.Reset()
		in = false
	}
	for _, r := range piece {
		switch {
		case quote != 0:
			if strings.ContainsRune(powershellQuotes, r) && quoteKind(r) == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
		case strings.ContainsRune(powershellQuotes, r):
			quote, in = quoteKind(r), true
		case r == ' ' || r == '\t':
			flush()
		default:
			b.WriteRune(r)
			in = true
		}
	}
	flush()
	return out
}

// quoteKind is ' for a single quote mark of any form, " for a double.
func quoteKind(r rune) rune {
	if strings.ContainsRune("'‘’‚‛", r) {
		return '\''
	}
	return '"'
}

// powershellScripts are the command texts a piece may hand to another
// interpreter to run: Invoke-Expression's argument, the -Command or
// -EncodedCommand of powershell and pwsh (also written /Command, or
// -Command:"..." with its value), or what follows their options, which
// Windows PowerShell runs as a command, and cmd's /c, /k or /r, with or
// without a space before the command. Every word that is not an option
// starts a candidate, as the bash rules try every suffix behind a
// wrapper: an option's value ("-ExecutionPolicy Bypass") is not told
// from the command, and only deny decisions are taken from these. A
// command passed some other way (Start-Process with -ArgumentList, a
// script file, a variable) is not seen.
func powershellScripts(piece string) []string {
	words := powershellWords(piece)
	if len(words) < 2 {
		return nil
	}
	rest := words[1:]
	var out []string
	from := func(i int, first string) {
		parts := rest[min(i, len(rest)):]
		if first != "" {
			parts = append([]string{first}, parts...)
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, " "))
		}
	}
	isOption := func(w string) bool { return strings.HasPrefix(w, "-") || strings.HasPrefix(w, "/") }
	// option is an option's name, in lower case with a leading "-", and
	// the value given with a colon.
	option := func(w string) (string, string) {
		name, value, _ := strings.Cut(w, ":")
		name = strings.ToLower(name)
		if strings.HasPrefix(name, "/") {
			name = "-" + name[1:]
		}
		return name, value
	}
	switch normalizeProgram(words[0]) {
	case "iex", "invoke-expression":
		for i, w := range rest {
			if !isOption(w) {
				from(i, "")
			} else if name, value := option(w); value != "" && len(name) > 1 && strings.HasPrefix("-command", name) {
				from(i+1, value)
			}
		}
	case "powershell", "pwsh":
		for i, w := range rest {
			if !isOption(w) {
				from(i, "")
				continue
			}
			name, value := option(w)
			switch {
			case len(name) > 1 && strings.HasPrefix("-command", name):
				from(i+1, value)
			case len(name) > 1 && (strings.HasPrefix("-encodedcommand", name) || name == "-ec"):
				if value == "" && i+1 < len(rest) {
					value = rest[i+1]
				}
				if script, ok := decodePowerShell(value); ok {
					out = append(out, script)
				}
			}
		}
	case "cmd":
		for i, w := range rest {
			lw := strings.ToLower(w)
			if len(lw) >= 2 && (lw[:2] == "/c" || lw[:2] == "/k" || lw[:2] == "/r") {
				from(i+1, w[2:])
			}
		}
	}
	return out
}

// decodePowerShell reads an -EncodedCommand: base64 of UTF-16LE, with
// its padding or without.
func decodePowerShell(s string) (string, bool) {
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil || len(b)%2 != 0 {
		return "", false
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u)), true
}
