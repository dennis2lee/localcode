package config

import (
	"context"
	"encoding/base64"
	"strings"
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
//	Write-Output @(New-Item marker)          (an array subexpression)
//	$x = New-Item marker                     (an assignment)
//	if ($true) {New-Item marker}             (a statement's block)
//
// So a PowerShell line is also cut, for the deny rules only, at every
// character that can end a command or start one, inside quotes too:
// ; | & and a line end between commands, ( ) { } around nested ones
// (and $( ) inside a double-quoted string), and = before the command an
// assignment runs. Blind to quotes on purpose: the quoting is
// PowerShell's, double-quoted strings run what is inside $( ), and a
// split too many only makes a deny rule match more. A piece of a quoted
// argument can then match a deny rule, and the line is denied though it
// would not have run the command: the safe direction for a rule somebody
// wrote to forbid it.

// powershellBreaks are the characters a PowerShell line is cut at.
const powershellBreaks = ";|&(){}=\n\r"

// powershellLead are words a PowerShell command can follow on its piece:
// keywords that take a pipeline, and the operators that negate one.
var powershellLead = map[string]bool{
	"return": true, "throw": true, "-not": true, "!": true, "exit": true,
	"do": true, "else": true, "elseif": true, "try": true, "finally": true,
	"begin": true, "process": true, "end": true, "default": true,
}

// powershellPieces is a PowerShell line cut at powershellBreaks, each
// piece without the keywords, casts ([void]) and call operators (".")
// before its command.
func powershellPieces(line string) []string {
	var out []string
	for _, piece := range strings.FieldsFunc(line, func(r rune) bool { return strings.ContainsRune(powershellBreaks, r) }) {
		piece = strings.TrimSpace(piece)
		for piece != "" {
			trimmed := piece
			if strings.HasPrefix(trimmed, "[") {
				if end := strings.IndexByte(trimmed, ']'); end > 0 {
					trimmed = trimmed[end+1:]
				}
			}
			if word, rest, ok := strings.Cut(trimmed, " "); ok && (powershellLead[strings.ToLower(word)] || word == ".") {
				trimmed = rest
			}
			trimmed = strings.TrimLeft(strings.TrimSpace(trimmed), "@$")
			trimmed = strings.TrimSpace(trimmed)
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

// powershellDenied reports a deny rule for the powershell tool that
// matches a piece of line, as written, with its quoting removed, and
// without PowerShell's escape character (Ne`w-Item is New-Item), behind
// the wrappers the bash rules see through, and in the text a piece hands
// to another interpreter (powershellScript). ctx carries the tool.
func (c *Config) powershellDenied(ctx context.Context, line string, staticRequiresPermission bool) bool {
	return c.powershellDeniedAt(ctx, line, staticRequiresPermission, 0)
}

func (c *Config) powershellDeniedAt(ctx context.Context, line string, staticRequiresPermission bool, depth int) bool {
	if depth > 4 {
		return false
	}
	for _, piece := range powershellPieces(line) {
		readings := []string{piece}
		if plain := strings.ReplaceAll(piece, "`", ""); plain != piece {
			readings = append(readings, plain)
		}
		for _, r := range readings {
			// In any case: the powershell tool's rules are matched
			// without regard to it (resolveFolded), as PowerShell finds a
			// command.
			if c.resolveSegment(ctx, r, staticRequiresPermission) == DecisionDeny {
				return true
			}
			if script, ok := powershellScript(r); ok && c.powershellDeniedAt(ctx, script, staticRequiresPermission, depth+1) {
				return true
			}
		}
	}
	return false
}

// powershellScript is the command text a piece hands to another
// interpreter to run: Invoke-Expression's argument, the -Command or
// -EncodedCommand of powershell and pwsh (or what follows their options,
// which Windows PowerShell runs as a command), and cmd's /c or /k. A
// command passed some other way (Start-Process with -ArgumentList, a
// script file) is not seen.
func powershellScript(piece string) (string, bool) {
	words := strings.Fields(piece)
	if len(words) < 2 {
		return "", false
	}
	rest := words[1:]
	switch normalizeProgram(strings.Trim(words[0], `"'`)) {
	case "iex", "invoke-expression":
		if w := strings.ToLower(rest[0]); len(rest) > 1 && len(w) > 1 && strings.HasPrefix("-command", w) {
			rest = rest[1:]
		}
		return strings.Join(rest, " "), true
	case "powershell", "pwsh":
		for i, w := range rest {
			lw := strings.ToLower(w)
			switch {
			case len(lw) > 1 && strings.HasPrefix("-command", lw):
				return strings.Join(rest[i+1:], " "), i+1 < len(rest)
			case len(lw) > 1 && (strings.HasPrefix("-encodedcommand", lw) || lw == "-ec") && i+1 < len(rest):
				return decodePowerShell(strings.Trim(rest[i+1], `"'`))
			case !strings.HasPrefix(lw, "-") && !strings.HasPrefix(lw, "/"):
				return strings.Join(rest[i:], " "), true
			}
		}
	case "cmd":
		for i, w := range rest {
			if lw := strings.ToLower(w); lw == "/c" || lw == "/k" || lw == "/r" {
				return strings.Join(rest[i+1:], " "), i+1 < len(rest)
			}
		}
	}
	return "", false
}

// decodePowerShell reads an -EncodedCommand: base64 of UTF-16LE. Its
// padding is not needed, and is gone from a piece cut at "=".
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
