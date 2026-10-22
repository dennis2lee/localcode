package memory

import (
	"regexp"
	"strings"
	"unicode"
)

// Sanitize takes out of note text what a reader cannot see and a model
// can: format characters (zero-width spaces and joiners, the bidirectional
// overrides that make text display in an order other than the one it is
// read in, a byte-order mark) and control characters other than newline
// and tab. A note is text the model wrote, sometimes after reading
// something hostile, and the person reading it back in /memory has to be
// reading what the model will read.
func Sanitize(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ReplaceAll(s, "\r\n", "\n") {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteByte('\n')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// markerLine matches a line that reproduces one of the boundaries the
// prompt puts around recalled text. A note containing one could close the
// record early and go on as text outside it, which is the whole of what
// the boundary exists to prevent.
var markerLine = regexp.MustCompile(`(?im)^(\s*)(-{3,}\s*(begin|end)\b[^\n]*(recalled|notes|memory)[^\n]*)$`)

// Neutralize sanitizes recalled text and defuses any line in it that
// imitates a record boundary, by quoting it: still readable, no longer
// the boundary.
func Neutralize(s string) string {
	return markerLine.ReplaceAllString(Sanitize(s), "$1> $2")
}

// secretPatterns are shapes of credential that have no business in a
// note: a note is read back into every later session and shown in
// /memory, and a key written there has left the place it was kept.
//
// Shapes with a distinctive prefix or structure only. A rule that caught
// every long random-looking string would refuse commit hashes and
// checksums, which are exactly the kind of thing a project note records.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bsk-(ant-|proj-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	// A reference to where the value lives ($VAR, ${VAR}, <placeholder>)
	// is what a note should say instead, so it does not count.
	regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token)\s*[:=]\s*["']?[^\s"'$<{][^\s"']{7,}`),
}

// minKnownSecret is the shortest configured credential looked for by
// value. Shorter, and ordinary words would match.
const minKnownSecret = 8

// LooksSecret reports whether text carries a credential: one of the
// well-known shapes, or any of known (the keys and header values the
// configuration holds) verbatim. The reason names the shape and never the
// value.
func LooksSecret(text string, known []string) (string, bool) {
	for _, re := range secretPatterns {
		if re.MatchString(text) {
			return "it contains what looks like a credential (" + describePattern(re) + ")", true
		}
	}
	for _, k := range known {
		k = strings.TrimSpace(k)
		if len(k) >= minKnownSecret && strings.Contains(text, k) {
			return "it contains a key or header value from the configuration", true
		}
	}
	return "", false
}

func describePattern(re *regexp.Regexp) string {
	switch re {
	case secretPatterns[0]:
		return "a private key"
	case secretPatterns[len(secretPatterns)-1]:
		return "a password, secret, key or token assigned a value"
	case secretPatterns[7]:
		return "a JSON Web Token"
	default:
		return "an API key or access token"
	}
}
