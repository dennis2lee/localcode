package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// makeJWT builds a token with the header and claims given and a signature
// nobody checks, the way a test needs one.
func makeJWT(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	seg := func(v map[string]any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	return seg(header) + "." + seg(claims) + "." + base64.RawURLEncoding.EncodeToString([]byte("signature"))
}

// A JWT's header and claims are read, an audience as one string or a list,
// and the dates as times. Read, not verified: localcode carries the token
// and the endpoint checks it.
func TestParseJWTReadsTheClaims(t *testing.T) {
	exp := time.Date(2027, 10, 12, 9, 44, 2, 0, time.UTC)
	tok := makeJWT(t,
		map[string]any{"alg": "RS256", "kid": "gw-2024", "typ": "JWT"},
		map[string]any{"iss": "auth-service", "aud": []any{"gateway-api", "other"}, "exp": exp.Unix(), "nbf": exp.Unix() - 3600, "iat": exp.Unix() - 3600})
	c, ok := ParseJWT(tok)
	if !ok {
		t.Fatal("a JWT was not read as one")
	}
	if c.Algorithm != "RS256" || c.KeyID != "gw-2024" || c.Issuer != "auth-service" {
		t.Errorf("header and issuer = %+v", c)
	}
	if strings.Join(c.Audience, ",") != "gateway-api,other" {
		t.Errorf("audience = %v", c.Audience)
	}
	if !c.Expires.Equal(exp) || !c.NotBefore.Equal(exp.Add(-time.Hour)) || !c.IssuedAt.Equal(exp.Add(-time.Hour)) {
		t.Errorf("dates = %v %v %v, want exp %v", c.Expires, c.NotBefore, c.IssuedAt, exp)
	}

	one, ok := ParseJWT("  " + makeJWT(t, map[string]any{"alg": "ES256"}, map[string]any{"aud": "solo"}) + "\n")
	if !ok || strings.Join(one.Audience, ",") != "solo" || !one.Expires.IsZero() {
		t.Errorf("a token with one audience, no exp and spaces around it = %+v, %v", one, ok)
	}
}

// Anything that is not a JWT says so, and none of it panics: a key pasted by
// hand is often a part of one.
func TestParseJWTRefusesWhatIsNotOne(t *testing.T) {
	good := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": 1})
	parts := strings.Split(good, ".")
	for _, key := range []string{
		"",
		"sk-1234567890",
		"a.b",
		parts[0] + "." + parts[1],
		"!!!." + parts[1] + "." + parts[2],
		parts[0] + ".!!!." + parts[2],
		makeJWT(t, map[string]any{"typ": "JWT"}, map[string]any{"exp": 1}),
		parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`"a string"`)) + "." + parts[2],
		good + ".extra",
	} {
		if _, ok := ParseJWT(key); ok {
			t.Errorf("%q read as a JWT", key)
		}
	}
	// Padded segments, which some encoders write, are still a JWT.
	padded := parts[0] + "==." + parts[1] + "." + parts[2]
	if _, ok := ParseJWT(padded); !ok {
		t.Error("a JWT with padded segments was refused")
	}
}

// The model list reports what the endpoint answered: the ids on a 200,
// the status and what it said on a refusal, so a refused key reads
// differently from an endpoint with no list. The key goes as a bearer token.
func TestModelListReportsTheAnswer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch gotAuth {
		case "Bearer good":
			w.Write([]byte(`{"data":[{"id":"model-a"},{"id":""},{"id":"model-b"}]}`))
		case "Bearer html":
			w.Write([]byte("<html>sign in</html>"))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"invalid token"}}`))
		}
	}))
	defer srv.Close()

	ids, status, detail, err := NewOpenAICompat(srv.URL, "good").ModelList(context.Background())
	if err != nil || status != 200 || strings.Join(ids, ",") != "model-a,model-b" || detail != "" {
		t.Errorf("accepted: %v %d %q %v", ids, status, detail, err)
	}
	if gotAuth != "Bearer good" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	ids, status, detail, err = NewOpenAICompat(srv.URL, "bad").ModelList(context.Background())
	if err != nil || status != 401 || ids != nil || !strings.Contains(detail, "invalid token") {
		t.Errorf("refused: %v %d %q %v", ids, status, detail, err)
	}
	_, status, detail, err = NewOpenAICompat(srv.URL, "html").ModelList(context.Background())
	if err != nil || status != 200 || !strings.HasPrefix(detail, "the answer is not a model list") {
		t.Errorf("a 200 that is not a list: %d %q %v", status, detail, err)
	}
	srv.Close()
	if _, _, _, err := NewOpenAICompat(srv.URL, "good").ModelList(context.Background()); err == nil {
		t.Error("no answer at all was not an error")
	}
}

// The dates are read claim by claim, and a number outside what a date can
// hold does not wrap round: a token valid past the year 9999 is not read
// as having expired, and an exp under one second is no exp at all.
func TestParseJWTDatesAndEdges(t *testing.T) {
	c, ok := ParseJWT(makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"iat": 1000, "nbf": 2000, "exp": 1e19, "aud": []any{"", "api"}}))
	if !ok {
		t.Fatal("not read as a JWT")
	}
	if c.IssuedAt.Unix() != 1000 || c.NotBefore.Unix() != 2000 {
		t.Errorf("iat %d, nbf %d: each claim must be read as itself", c.IssuedAt.Unix(), c.NotBefore.Unix())
	}
	if c.Expires.Year() != 9999 {
		t.Errorf("an exp past what a date holds became %v", c.Expires)
	}
	if strings.Join(c.Audience, ",") != "api" {
		t.Errorf("an empty audience was kept: %q", c.Audience)
	}
	if c, _ := ParseJWT(makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": 0.5, "aud": ""})); !c.Expires.IsZero() || c.Audience != nil {
		t.Errorf("an exp under a second or an empty aud: %+v", c)
	}

	good := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": 1})
	parts := strings.Split(good, ".")
	// One padding character, as well as two, is still the segment.
	header := base64.URLEncoding.EncodeToString([]byte(`{"alg":"ES256","x":123}`))
	if !strings.HasSuffix(header, "=") || strings.HasSuffix(header, "==") {
		t.Fatalf("the test header %q does not end in exactly one =", header)
	}
	if _, ok := ParseJWT(header + "." + parts[1] + "." + parts[2]); !ok {
		t.Error("a header padded with one = was refused")
	}
	// A gateway's token is signed: the third part is there and is base64url.
	for _, sig := range []string{"", "!!!", "==="} {
		if _, ok := ParseJWT(parts[0] + "." + parts[1] + "." + sig); ok {
			t.Errorf("a signature part %q was accepted", sig)
		}
	}
}

// Only an object with a data array is a model list. A sign-in page or an
// {"error": ...} answered with 200 is not, and reads as no list, not as an
// empty one the key was accepted for. The list is asked for with GET on
// /models under the base URL.
func TestModelListNeedsAList(t *testing.T) {
	var method, path string
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Write([]byte(body))
	}))
	defer srv.Close()
	p := NewOpenAICompat(srv.URL+"/v1", "k")

	body = `{"data":[]}`
	ids, status, detail, err := p.ModelList(t.Context())
	if err != nil || status != 200 || len(ids) != 0 || detail != "" {
		t.Errorf("an empty list: %v %d %q %v", ids, status, detail, err)
	}
	if method != http.MethodGet || path != "/v1/models" {
		t.Errorf("asked with %s %s, want GET /v1/models", method, path)
	}
	for _, b := range []string{`{"error":{"message":"sign in first"}}`, `{"data":null}`, `[]`} {
		body = b
		if _, _, detail, _ := p.ModelList(t.Context()); !strings.HasPrefix(detail, "the answer is not a model list") {
			t.Errorf("a 200 of %s read as a list (detail %q)", b, detail)
		}
	}
}
