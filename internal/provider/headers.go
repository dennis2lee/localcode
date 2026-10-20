package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"localcode/internal/debuglog"
)

// newRequest builds every HTTP request a provider sends, with the headers
// its config adds (providers.<name>.headers) already on it. One function,
// so a request site added later cannot leave them off: the model list and
// the /llm-doctor probes reach a gateway through the same door the chat
// stream does, and a gateway that wants a second credential wants it on
// all of them.
//
// They go on first and the caller's own Set calls come after, so the
// headers localcode sets itself win. Config validation already refuses
// those names; this order means one that got past it still cannot replace
// the key or the content type.
//
// The names also go on the request's context. That is how the debug log
// knows to redact their values, and how the redirect policy knows to
// withhold them from another host (see withholdOffHost).
func newRequest(ctx context.Context, method, url string, body io.Reader, headers map[string]string) (*http.Request, error) {
	if len(headers) > 0 {
		names := make([]string, 0, len(headers))
		for k := range headers {
			names = append(names, k)
		}
		ctx = debuglog.WithSecretHeaders(ctx, names...)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	return req, nil
}

// HeaderAsked reports whether an endpoint's refusal says that a request
// header is missing or required, and names the header when the refusal
// does. A gateway that takes a JWT as the bearer token and also wants a
// key of its own in another header answers 401 when that header is
// absent, and a 401 reads as a refused token unless something reads the
// words.
//
// Read generically, from the shape of the sentence and not from any one
// gateway's wording: the word "header" in the same clause as "missing",
// "required" or one of their relatives, and the name is the nearest word
// to "header" that is shaped like a header name: quoted, or with a hyphen
// or an underscore in it. A JSON body is read string by string, so the
// clause is the message and not the punctuation around it.
//
// Authorization is not reported. That header carries api_key, and a
// refusal that names it is about the key, which is what the caller says
// already. Nor is a header that is the token's: a JWT has a header of its
// own, and "the JWT header is missing kid", "Bearer header must be a valid
// JWT" or "header segment is missing typ" is a refused token, not a
// request header to add. Which header is meant is read from the word in
// front of it, so a sentence that names a header to add and mentions the
// token besides ("... header is missing (needed to validate your token)")
// is still read as asking for that header.
func HeaderAsked(body string) (name string, ok bool) {
	for _, s := range refusalStrings(body) {
		for _, clause := range clauseBreak.Split(s, -1) {
			n, found := headerAskedIn(clause)
			if !found {
				continue
			}
			if strings.EqualFold(n, "authorization") {
				return "", false
			}
			if n != "" {
				return n, true
			}
			ok = true
		}
	}
	return "", ok
}

// refusalStrings is the text a refusal is read in: each string in a JSON
// body, in key order, or the body itself when it is not JSON (or was cut
// short before it got here, which leaves JSON that does not parse).
func refusalStrings(body string) []string {
	var doc any
	if json.Unmarshal([]byte(body), &doc) != nil {
		// An object cut short (errorBody cuts LiteLLM's echo of the
		// request, mid-string) is read as one text with its keys taken
		// out: left in, they sat beside the words and "title" or "detail"
		// was taken for a header name. Read as its whole strings only, it
		// lost the message itself, which is the string that was cut.
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			body = jsonKey.ReplaceAllString(body, " ")
		}
		// Escaped quotes read as quotes, so a quoted name in a body that
		// is not JSON is still a quoted name.
		return []string{strings.ReplaceAll(body, `\"`, `"`)}
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, item := range t {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		}
	}
	walk(doc)
	return out
}

// jsonKey is an object's key and its colon.
var jsonKey = regexp.MustCompile(`"(?:[^"\\]|\\.)*"\s*:`)

// clauseBreak ends a clause. Not the quotes, the parentheses or the colon,
// which are where a header's name tends to sit.
var clauseBreak = regexp.MustCompile(`[.;,!?\n]+`)

// headerWord finds the words a header name is looked for around: a run of
// letters, digits, hyphens and underscores, with the quote on either side
// of it if there is one.
var headerWord = regexp.MustCompile("([\"'`]?)([A-Za-z][A-Za-z0-9_-]*)([\"'`]?)")

// askedPhrases are what a sentence that asks for a header says.
var askedPhrases = []string{
	"missing", "require", "absent",
	"not provided", "not present", "not supplied", "not set", "not found", "not sent", "not included",
	"must be", "must include", "must provide", "must send", "must contain",
	"please provide", "please include",
}

// tokenParts are the words a header named by them is the token's, or the
// one that carries it: "JWT header", "Bearer header", "bearer-token
// header", "Authorization header". A name made of other words as well
// ("X-Auth-Token") is a request header like any other.
var tokenParts = map[string]bool{
	"jwt": true, "jws": true, "jwe": true, "jose": true, "token": true, "bearer": true,
	"authorization": true, "auth": true, "id": true, "access": true,
}

// tokenStructure are the words after "header" that make it a part of the
// token: "header segment", "header part".
var tokenStructure = map[string]bool{"segment": true, "part": true, "section": true}

// joseParams are the fields of a JWT's own header. One named beside
// "header" is the token's header being refused, not a request header.
var joseParams = map[string]bool{
	"alg": true, "kid": true, "typ": true, "cty": true, "enc": true, "zip": true,
	"jku": true, "jwk": true, "x5u": true, "x5c": true, "x5t": true, "crit": true,
}

// isTokenWord reports whether w, a word in front of "header", makes that
// header the token's.
func isTokenWord(w string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(w), func(r rune) bool { return r == '-' || r == '_' }) {
		if !tokenParts[part] {
			return false
		}
	}
	return w != ""
}

// containsWord reports whether word is in s with no letter, digit, hyphen
// or underscore on either side, so "token" is found in "invalid token"
// and not in "x-tokenizer-id".
func containsWord(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		if (start == 0 || !isNameByte(s[start-1])) && (end == len(s) || !isNameByte(s[end])) {
			return true
		}
		i = start + 1
	}
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// notAName are the words that can stand next to "header" quoted or
// hyphenated without being one: the keys of a JSON body that no longer
// parses, and the words of the sentence itself.
var notAName = map[string]bool{
	"header": true, "headers": true, "required": true, "missing": true, "request": true,
	"error": true, "message": true, "detail": true, "details": true, "code": true, "type": true,
	"param": true, "msg": true, "reason": true, "status": true, "description": true,
}

// maxNameDistance is how many words from "header" a name is looked for,
// and maxNameLen the longest word taken for one.
const (
	maxNameDistance = 4
	maxNameLen      = 64
)

// headerAskedIn reads one clause. found is set when the clause asks for a
// header, and name is the header it names, or "".
func headerAskedIn(clause string) (name string, found bool) {
	lower := strings.ToLower(clause)
	asked := false
	for _, p := range askedPhrases {
		if strings.Contains(lower, p) {
			asked = true
			break
		}
	}
	if !asked {
		return "", false
	}
	var words []clauseWord
	for _, m := range headerWord.FindAllStringSubmatch(clause, -1) {
		words = append(words, clauseWord{text: m[2], quoted: m[1] != "" && m[1] == m[3]})
	}
	best, bestDist := "", 0
	for i, w := range words {
		if l := strings.ToLower(w.text); l != "header" && l != "headers" {
			continue
		}
		if i > 0 && isTokenWord(words[i-1].text) || i+1 < len(words) && tokenStructure[strings.ToLower(words[i+1].text)] {
			continue
		}
		if joseNear(words, i) {
			continue
		}
		found = true
		for j, c := range words {
			if notAName[strings.ToLower(c.text)] {
				continue
			}
			if !c.quoted && !strings.ContainsAny(c.text, "-_") && !strings.EqualFold(c.text, "authorization") {
				continue
			}
			d := j - i
			if d < 0 {
				d = -d
			}
			// Close by, and short: a name is a word or two away from
			// "header", and a long hyphenated run further off is more
			// likely a piece of an echoed token than a header name.
			if d > maxNameDistance || len(c.text) > maxNameLen {
				continue
			}
			// Nearest wins, and the word before "header" wins a tie:
			// "X-Team-Key header" is the more common order.
			if best == "" || d < bestDist || d == bestDist && j < i {
				best, bestDist = c.text, d
			}
		}
	}
	// "Authorization is required in the request header": the key's
	// header, named too far off to be taken for the name, is still what
	// the sentence is about.
	if found && best == "" && containsWord(lower, "authorization") {
		return "", false
	}
	return best, found
}

// clauseWord is one word of a clause, and whether it was quoted.
type clauseWord struct {
	text   string
	quoted bool
}

// joseNear reports whether a field of a JWT's own header is named, quoted,
// within reach of the "header" at i.
func joseNear(words []clauseWord, i int) bool {
	for j := max(0, i-maxNameDistance); j < len(words) && j <= i+maxNameDistance; j++ {
		if words[j].quoted && joseParams[strings.ToLower(words[j].text)] {
			return true
		}
	}
	return false
}
