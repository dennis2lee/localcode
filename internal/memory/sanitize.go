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
		case r == '\r', r == '\u2028', r == '\u2029', r == '\u0085':
			// Every other way of ending a line becomes the one the
			// boundary check reads: a model may well take a line
			// separator for a new line, and a boundary after one has to
			// be found to be quoted.
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
	// A name that ends in one of these words, joined by underscores or
	// not (client_secret, aws_secret_access_key), assigned a value. A
	// reference to where the value lives ($VAR, ${VAR}, <placeholder>) is
	// what a note should say instead, so it does not count. The value is
	// checked again by looksLikeValue.
	//
	// After the word, only a separated suffix (secret_key, token-value)
	// or the commonest unseparated ones (secretkey, secretkeys, tokens,
	// password1): a word that merely starts with one (secretary,
	// tokenizer) is not a credential. Before it, anything: clientsecret
	// is a credential, and the rare word that ends in one (betoken) is
	// the price of catching it.
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9_-])[a-z0-9_-]*(password|passwd|passphrase|secret|api[_-]?key|access[_-]?key|token)(?:[_-][a-z0-9_-]*|keys?|s|[0-9]+)?["']?\s*[:=]\s*["']?([^\s"'$<{][^\s"']{7,})`),
}

// looksLikeValue tells a credential from words and numbers: it has a
// letter and a digit, or a capital inside it beside lower
// and lower case letters both, or it is minPassphrase letters or more
// run together, longer than any ordinary word. "change-me-on-first-login",
// "required," and "internationalization" are words about a password;
// "hunter2hunter2", "wJalrXUtnFEMI" and "correcthorsebatterystaple" are
// passwords.
func looksLikeValue(v string) bool {
	// A value with no letter in it is a number: a limit, a size, a port.
	// A password of digits alone is possible, and rarer than the notes
	// that would be refused for "max_tokens: 128000".
	var digit, upper, lower, other bool
	letters := 0
	for i, r := range v {
		switch {
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsUpper(r):
			// A capital that only starts the value is how a word is
			// written ("Changeme", "Required"); one inside it is not.
			if i > 0 {
				upper = true
			}
			letters++
		case unicode.IsLower(r):
			lower = true
			letters++
		default:
			other = true
		}
	}
	if letters == 0 {
		return false
	}
	return digit || (upper && lower) || (!other && letters >= minPassphrase)
}

// quantity is a number with a unit after it (30000000s, 128k, 1.5GB,
// 3600seconds): a limit or a size, which a note about tokens records all
// the time. The unit has to be one: "12345678a" is a value with a letter
// at the end, not a number of anything.
var quantity = regexp.MustCompile(`(?i)^[0-9][0-9.,_]*(?:` +
	`[kmgtp]i?b?|b|bytes?|bits?|` +
	`ns|us|ms|s|secs?|seconds?|m|mins?|minutes?|h|hrs?|hours?|d|days?|w|wks?|weeks?|mo|months?|y|yrs?|years?|` +
	`x|px|pt|em|rem|%|` +
	`tokens?|chars?|characters?|lines?|words?|rows?|items?|requests?|req|rps|qps|tps|calls?|times?|turns?|steps?|retries)?$`)

// minPassphrase is the shortest run of letters taken for a passphrase.
// Past the longest words in ordinary use, of which "internationalization"
// (twenty) is the one a note about software is likeliest to hold.
const minPassphrase = 24

// minKnownSecret is the shortest configured credential looked for by
// value. Shorter, and ordinary words would match.
const minKnownSecret = 8

// LooksSecret reports whether text carries a credential: one of the
// well-known shapes, or any of known (the keys and header values the
// configuration holds) verbatim. The reason names the shape and never the
// value.
func LooksSecret(text string, known []string) (string, bool) {
	generic := secretPatterns[len(secretPatterns)-1]
	for _, re := range secretPatterns {
		if re != generic {
			if re.MatchString(text) {
				return "it contains what looks like a credential (" + describePattern(re) + ")", true
			}
			continue
		}
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			// The sentence's own punctuation, and brackets or Markdown
			// around the value, are not part of it.
			if v := strings.TrimLeft(strings.TrimRight(m[2], ".,;:!?)]}`*_"), "([{`*_"); !quantity.MatchString(v) && looksLikeValue(v) {
				return "it contains what looks like a credential (" + describePattern(re) + ")", true
			}
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
