package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// What "localcode check-jwt-key" asks of a key: what it says about itself, and
// whether the endpoint takes it.

// JWTClaims is what a key that is a JWT says about itself. Read, not
// verified: the endpoint that issued the token holds the key that checks
// its signature, and localcode only carries the token to it.
type JWTClaims struct {
	Algorithm string
	KeyID     string
	Issuer    string
	Audience  []string
	// Zero when the token does not carry the claim.
	Expires   time.Time
	NotBefore time.Time
	IssuedAt  time.Time
}

// ParseJWT reads a JWT's header and claims. ok is false for a key that is
// not one: three dot-separated base64url parts, a header naming its
// algorithm, and a claims object. Nothing in it panics on a malformed key,
// since a key pasted by hand is often a part of one.
func ParseJWT(key string) (JWTClaims, bool) {
	key = strings.TrimSpace(key)
	// Only base64url, its padding, and the dots. Go's decoder skips a line
	// break inside a segment, so a token with one in it read as a JWT and
	// then could not be sent: net/http refuses such a header.
	if strings.IndexFunc(key, func(c rune) bool { return !isJWTChar(c) }) >= 0 {
		return JWTClaims{}, false
	}
	parts := strings.Split(key, ".")
	if len(parts) != 3 {
		return JWTClaims{}, false
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if !decodeSegment(parts[0], &header) || header.Alg == "" {
		return JWTClaims{}, false
	}
	var claims map[string]any
	if !decodeSegment(parts[1], &claims) || claims == nil {
		return JWTClaims{}, false
	}
	// A token a gateway issues is signed, so the third part is there and is
	// base64url, whatever it says.
	if sig := strings.TrimRight(parts[2], "="); sig == "" {
		return JWTClaims{}, false
	} else if _, err := base64.RawURLEncoding.DecodeString(sig); err != nil {
		return JWTClaims{}, false
	}
	out := JWTClaims{Algorithm: header.Alg, KeyID: header.Kid}
	out.Issuer, _ = claims["iss"].(string)
	switch aud := claims["aud"].(type) {
	case string:
		if aud != "" {
			out.Audience = []string{aud}
		}
	case []any:
		for _, a := range aud {
			if s, ok := a.(string); ok && s != "" {
				out.Audience = append(out.Audience, s)
			}
		}
	}
	out.Expires = epoch(claims["exp"])
	out.NotBefore = epoch(claims["nbf"])
	out.IssuedAt = epoch(claims["iat"])
	return out, true
}

func isJWTChar(c rune) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '=' || c == '.'
}

// decodeSegment base64url-decodes one part of a JWT, padded or not, into
// out.
func decodeSegment(seg string, out any) bool {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(seg, "="))
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}

// maxEpoch is the last second of the year 9999. A NumericDate past it is
// read as it: converted as it stood, a number past int64 wrapped round to a
// date in the past, and an unexpired token read as expired.
const maxEpoch = 253402300799

// epoch is a NumericDate claim as a time, or the zero time when the claim
// is absent, not a number, or under a second.
func epoch(v any) time.Time {
	n, ok := v.(float64)
	if !ok || n < 1 {
		return time.Time{}
	}
	return time.Unix(int64(min(n, maxEpoch)), 0).UTC()
}

// ModelList asks the endpoint which models it serves, sending the key, and
// reports what it answered: the model ids on a 200, and otherwise the
// status and the start of what it said, so a refused key reads differently
// from an endpoint that has no model list. err is set only when no answer
// came back at all.
func (p *OpenAICompat) ModelList(ctx context.Context) (ids []string, status int, detail string, err error) {
	req, err := newRequest(ctx, http.MethodGet, p.BaseURL+"/models", nil, p.Headers)
	if err != nil {
		return nil, 0, "", err
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := clientOr(p.Client).Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, shortDetail(string(raw)), nil
	}
	// A model list is an object with a data array. A sign-in page or an
	// {"error": ...} answered with 200 is not one, and was read as an
	// empty list that the key had been accepted for.
	var body struct {
		Data *[]struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Data == nil {
		return nil, resp.StatusCode, "the answer is not a model list: " + shortDetail(string(raw)), nil
	}
	for _, m := range *body.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, resp.StatusCode, "", nil
}

// shortDetail is the start of what an endpoint said, on one line, for a
// report a person reads.
func shortDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

// jwtRefusalHint is what an endpoint's refusal (401 or 403) says about a
// JWT api_key's date: that it has expired, and when, or that it has not, so
// a person reading "401" in a transcript knows whether to get a new token
// or look elsewhere. Empty for any other status, a key that is not a JWT,
// and a token with no exp.
//
// The date and nothing else: the retry and fallback decisions read this
// text, and a count such as "503 days" would read as a server error worth
// retrying.
func jwtRefusalHint(status int, key, body string, now time.Time) string {
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return ""
	}
	c, ok := ParseJWT(key)
	if !ok {
		return ""
	}
	const layout = "2006-01-02 15:04 MST"
	switch {
	// Expired from the exp instant on, as RFC 7519 4.1.4 has it.
	case !c.Expires.IsZero() && !now.Before(c.Expires):
		return fmt.Sprintf("\n\nhint: the JWT in this provider's api_key expired on %s. Get a new token, put it in api_key, and run `localcode check-jwt-key` to confirm the endpoint takes it", c.Expires.Format(layout))
	case !c.NotBefore.IsZero() && now.Before(c.NotBefore):
		// Not valid yet is a date as much as expired is: "valid until"
		// would have said the date was not the cause when it was.
		return fmt.Sprintf("\n\nhint: the JWT in this provider's api_key is not valid until %s. Wait until then, or check the clock on this machine and on the endpoint", c.NotBefore.Format(layout))
	case !c.Expires.IsZero() && strings.Contains(strings.ToLower(body), "expire"):
		// The endpoint says expired and this machine's clock says not: the
		// two clocks disagree, and saying the date is not the cause would
		// contradict the endpoint's own words.
		return fmt.Sprintf("\n\nhint: the endpoint says the token expired, but by this machine's clock the JWT in this provider's api_key is valid until %s. Check the clock here, or get a new token", c.Expires.Format(layout))
	case !c.Expires.IsZero():
		return fmt.Sprintf("\n\nhint: the JWT in this provider's api_key is valid until %s, so its date is not why it was refused. Run `localcode check-jwt-key` to see what the endpoint says", c.Expires.Format(layout))
	}
	return ""
}

// refusalHint is the paragraph a refusal (401 or 403) gets after the
// endpoint's own words, on a turn and on /llm-doctor's raw requests.
func (p *OpenAICompat) refusalHint(status int, body string) string {
	return refusalHint(status, p.APIKey, p.Headers, body, time.Now())
}

// refusalHint says what a refusal is about. When the endpoint says that a
// request header is missing or required (see HeaderAsked), it is about that
// header, whatever the key is: it says whether this provider sends the
// header, and so whether to add it or to check its value. The JWT's date
// follows only when it is a second problem, and is said as one. A date
// hint on its own there sent a person to replace a token the endpoint had
// not complained of, or told them the date was fine and to look elsewhere
// without saying where.
//
// Otherwise it is jwtRefusalHint. Names only, never a value: the values
// are secrets, and this text is shown and logged.
func refusalHint(status int, key string, headers map[string]string, body string, now time.Time) string {
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return ""
	}
	name, asked := HeaderAsked(body)
	if !asked {
		return jwtRefusalHint(status, key, body, now)
	}
	var hint string
	switch {
	case name == "":
		hint = `the endpoint says a request header it needs is missing, and does not say which. Add the one it needs under this provider's "headers" in config.json`
	case sendsHeader(headers, name):
		hint = fmt.Sprintf(`the endpoint asks for request header %s, and this provider sends one, so it is that header's value the endpoint did not take. Check the value under this provider's "headers" in config.json`, name)
	default:
		hint = fmt.Sprintf(`the endpoint asks for request header %s, which this provider does not send. Add it under this provider's "headers" in config.json`, name)
	}
	if c, ok := ParseJWT(key); ok {
		const layout = "2006-01-02 15:04 MST"
		switch {
		case !c.Expires.IsZero() && !now.Before(c.Expires):
			hint += fmt.Sprintf(". Separately, the JWT in api_key expired on %s and needs replacing as well", c.Expires.Format(layout))
		case !c.NotBefore.IsZero() && now.Before(c.NotBefore):
			hint += fmt.Sprintf(". Separately, the JWT in api_key is not valid until %s", c.NotBefore.Format(layout))
		}
	}
	return "\n\nhint: " + hint
}

// sendsHeader reports whether headers sends name: there, under any case,
// with a value.
func sendsHeader(headers map[string]string, name string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, name) && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}
