package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	parts := strings.Split(strings.TrimSpace(key), ".")
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
	out := JWTClaims{Algorithm: header.Alg, KeyID: header.Kid}
	out.Issuer, _ = claims["iss"].(string)
	switch aud := claims["aud"].(type) {
	case string:
		out.Audience = []string{aud}
	case []any:
		for _, a := range aud {
			if s, ok := a.(string); ok {
				out.Audience = append(out.Audience, s)
			}
		}
	}
	out.Expires = epoch(claims["exp"])
	out.NotBefore = epoch(claims["nbf"])
	out.IssuedAt = epoch(claims["iat"])
	return out, true
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

// epoch is a NumericDate claim as a time, or the zero time when the claim
// is absent or not a number.
func epoch(v any) time.Time {
	n, ok := v.(float64)
	if !ok || n <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(n), 0).UTC()
}

// ModelList asks the endpoint which models it serves, sending the key, and
// reports what it answered: the model ids on a 200, and otherwise the
// status and the start of what it said, so a refused key reads differently
// from an endpoint that has no model list. err is set only when no answer
// came back at all.
func (p *OpenAICompat) ModelList(ctx context.Context) (ids []string, status int, detail string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/models", nil)
	if err != nil {
		return nil, 0, "", err
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, shortDetail(string(raw)), nil
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil, resp.StatusCode, "the answer is not a model list: " + shortDetail(string(raw)), nil
	}
	for _, m := range body.Data {
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
