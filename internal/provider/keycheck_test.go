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

// A token with anything but base64url, padding and dots in it, a line
// break inside a part above all, is not read as a JWT: Go's decoder would
// skip the break, and net/http would then refuse to send the header.
// An exp of exactly one second is a date, the earliest there is.
func TestParseJWTRefusesStrayCharacters(t *testing.T) {
	good := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": 1})
	parts := strings.Split(good, ".")
	for _, bad := range []string{
		parts[0] + "." + parts[1][:4] + "\n" + parts[1][4:] + "." + parts[2],
		parts[0] + "." + parts[1] + "." + parts[2][:3] + "\r" + parts[2][3:],
		parts[0] + " ." + parts[1] + "." + parts[2],
		parts[0] + "." + parts[1] + "." + parts[2] + "+",
	} {
		if _, ok := ParseJWT(bad); ok {
			t.Errorf("%q read as a JWT", bad)
		}
	}
	if c, ok := ParseJWT(good); !ok || c.Expires.Unix() != 1 {
		t.Errorf("exp 1: %v %v", c.Expires, ok)
	}
}

// A refusal (401 or 403) of a JWT api_key says what the token's date is:
// expired and when, or valid until when and so not the cause. Nothing is
// added for another status, a key that is not a JWT, or a token with no exp.
func TestJWTRefusalHint(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	past := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": now.Add(-72 * time.Hour).Unix()})
	future := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": now.Add(300 * 24 * time.Hour).Unix()})
	noExp := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"iss": "x"})
	early := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"nbf": now.Add(2 * time.Hour).Unix(), "exp": now.Add(400 * 24 * time.Hour).Unix()})
	earlyNoExp := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"nbf": now.Add(2 * time.Hour).Unix()})
	for _, tc := range []struct {
		status int
		key    string
		want   string
	}{
		{401, past, "expired on 2026-10-16 12:00 UTC. Get a new token"},
		{403, past, "expired on 2026-10-16 12:00 UTC"},
		{401, future, "valid until 2027-08-15 12:00 UTC, so its date is not why it was refused"},
		{500, past, ""},
		{404, past, ""},
		{401, "sk-not-a-jwt", ""},
		{401, noExp, ""},
		{401, early, "not valid until 2026-10-19 14:00 UTC"},
		{403, earlyNoExp, "not valid until 2026-10-19 14:00 UTC"},
	} {
		got := jwtRefusalHint(tc.status, tc.key, "", now)
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("status %d, key %.12s: %q, want %q", tc.status, tc.key, got, tc.want)
		}
		if strings.Contains(got, tc.key) {
			t.Errorf("the hint carries the key: %q", got)
		}
	}
}

// The hint arrives on the error a turn sees, after the endpoint's own words.
func TestOpenAICompatRefusalSaysWhenTheJWTExpired(t *testing.T) {
	expired := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": time.Now().Add(-48 * time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid or expired token"}}`))
	}))
	defer srv.Close()
	_, err := NewOpenAICompat(srv.URL, expired).Chat(t.Context(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
	if err == nil {
		t.Fatal("a 401 was not an error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "openai-compat endpoint returned 401: ") || !strings.Contains(msg, "invalid or expired token") {
		t.Errorf("the endpoint's own words were lost: %s", msg)
	}
	if !strings.Contains(msg, "hint: the JWT in this provider's api_key expired on ") || !strings.Contains(msg, "localcode check-jwt-key") {
		t.Errorf("no expiry hint: %s", msg)
	}
}

// From the review of the hint: it is a paragraph of its own that names the
// check, and never a part of the token; a token is expired from its exp
// instant on; an endpoint that says "expired" while this clock says valid
// is told apart from a refusal the date has nothing to do with.
func TestJWTRefusalHintEdges(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	atExp := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": now.Unix()})
	future := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": now.Add(300 * 24 * time.Hour).Unix()})

	if got := jwtRefusalHint(401, atExp, "", now); !strings.Contains(got, "expired on 2026-10-19 12:00 UTC") {
		t.Errorf("at the exp instant: %q", got)
	}
	got := jwtRefusalHint(401, future, `{"error":"Token has expired"}`, now)
	if !strings.Contains(got, "the endpoint says the token expired, but by this machine's clock") || strings.Contains(got, "is not why") {
		t.Errorf("an endpoint saying expired, a clock saying valid: %q", got)
	}
	for _, key := range []string{atExp, future} {
		for _, body := range []string{"", "expired"} {
			h := jwtRefusalHint(403, key, body, now)
			if !strings.HasPrefix(h, "\n\nhint: the ") || !strings.Contains(h, "JWT") {
				t.Errorf("not a hint paragraph: %q", h)
			}
			for _, part := range strings.Split(key, ".") {
				if len(part) >= 8 && strings.Contains(h, part) {
					t.Errorf("the hint carries part of the token: %q", h)
				}
			}
		}
	}
	if h := jwtRefusalHint(401, future, "", now); !strings.Contains(h, "localcode check-jwt-key") {
		t.Errorf("the valid-until hint does not name the check: %q", h)
	}
}

// The hint is added for a refusal only, from the status the endpoint sent,
// on the chat path and on /llm-doctor's raw requests alike.
func TestTheHintFollowsTheStatus(t *testing.T) {
	expired := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": time.Now().Add(-48 * time.Hour).Unix()})
	valid := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": time.Now().Add(48 * time.Hour).Unix()})
	code := http.StatusInternalServerError
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		w.Write([]byte(`{"error":"no"}`))
	}))
	defer srv.Close()
	chat := func(key string) string {
		_, err := NewOpenAICompat(srv.URL, key).Chat(t.Context(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
		return err.Error()
	}
	if msg := chat(expired); strings.Contains(msg, "hint:") {
		t.Errorf("a 500 got a JWT hint: %s", msg)
	}
	code = http.StatusUnauthorized
	if msg := chat(valid); !strings.Contains(msg, "valid until") {
		t.Errorf("a 401 of a valid token: %s", msg)
	}
	_, err := NewOpenAICompat(srv.URL, expired).RawChat(t.Context(), []byte(`{"model":"m","messages":[]}`))
	if err == nil || !strings.HasPrefix(err.Error(), "the server answered 401: ") || !strings.Contains(err.Error(), "expired on ") {
		t.Errorf("RawChat on an expired token: %v", err)
	}
}

// A refusal that says a request header is missing or required is read as
// that, from the shape of the sentence rather than any one gateway's
// words, and the name is the header-shaped word nearest "header". A
// refusal that names Authorization is about api_key, and a refusal that
// does not mention a missing header is not this.
func TestHeaderAsked(t *testing.T) {
	for _, tc := range []struct {
		body string
		name string
		ok   bool
	}{
		{`{"error":{"message":"Missing client API key (X-Team-Key header required)","type":"None","param":"None","code":"401"}}`, "X-Team-Key", true},
		{`{"detail":"missing required header: X-Team-Key"}`, "X-Team-Key", true},
		{`{"error":"Header 'x-team-key' is required"}`, "x-team-key", true},
		{`{"error":"The request is missing the \"X-Region\" header."}`, "X-Region", true},
		{"X-Team-Key header is missing", "X-Team-Key", true},
		{"headers missing: X_TEAM_KEY", "X_TEAM_KEY", true},
		{`{"message":"a required header is missing"}`, "", true},
		// Cut short before it got here, so it no longer parses as JSON.
		{`{"error":{"message":"Missing client API key (X-Team-Key header required)","type":"No`, "X-Team-Key", true},
		{`{"error":"Authorization header is missing"}`, "", false},
		{`{"error":"missing Authorization header"}`, "", false},
		{`{"error":"invalid token"}`, "", false},
		{`{"error":{"message":"invalid or expired token"}}`, "", false},
		{`{"error":"invalid X-Team-Key header"}`, "", false},
		{"", "", false},
		// About the token, whose own header (alg, kid) or carrier
		// (Authorization, Bearer) is named: a refused key, not a request
		// header to add.
		{`{"error": "JWT header is missing required 'kid'"}`, "", false},
		{`{"error": "Missing 'alg' in token header"}`, "", false},
		{`{"error": "The Authorization bearer-token header is missing"}`, "", false},
		{`{"error": "Invalid 'Bearer' header: Authorization is required"}`, "", false},
		{`{"error": "Authorization is required in the request header"}`, "", false},
		{`{"error": "The bearer header must be a valid JWT"}`, "", false},
		{`{"error": "Bearer header invalid: user_id not found"}`, "", false},
		// Cut short, with its keys beside the words: "title" is a key,
		// not a header.
		{`{"title": "Missing required header", "detail": "xxxxxxxx`, "", true},
		// A word that only contains "token" is not about the token.
		{`{"error":"Missing required header: X-Tokenizer-Id"}`, "X-Tokenizer-Id", true},
	} {
		name, ok := HeaderAsked(tc.body)
		if name != tc.name || ok != tc.ok {
			t.Errorf("HeaderAsked(%s) = %q, %v; want %q, %v", tc.body, name, ok, tc.name, tc.ok)
		}
	}
}

// When the endpoint says a header is missing, the hint is about the
// header, and never says the token is the cause: not sent, add it; sent,
// its value was not taken. The token's date comes after only when it is a
// second problem, and a valid date is not mentioned at all. A key that is
// not a JWT gets the header hint too. Names only, never a value.
func TestARefusalThatNamesAHeaderIsHintedAsOne(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	past := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": now.Add(-72 * time.Hour).Unix()})
	future := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": now.Add(300 * 24 * time.Hour).Unix()})
	missing := `{"error":{"message":"Missing client API key (X-Team-Key header required)"}}`
	sent := map[string]string{"x-team-key": "made-up-team-key-0001"}
	for _, tc := range []struct {
		name    string
		status  int
		key     string
		headers map[string]string
		body    string
		want    []string
		not     []string
	}{
		{"not sent", 401, future, nil, missing,
			[]string{`asks for request header X-Team-Key, which this provider does not send. Add it under this provider's "headers"`},
			[]string{"valid until", "date", "check-jwt-key"}},
		{"sent", 403, future, sent, missing,
			[]string{"this provider sends one, so it is that header's value the endpoint did not take"},
			[]string{"valid until", "made-up-team-key-0001"}},
		{"sent empty", 401, future, map[string]string{"X-Team-Key": "  "}, missing,
			[]string{"which this provider does not send"}, nil},
		{"no name", 401, future, nil, `{"error":"a required header is missing"}`,
			[]string{"a request header it needs is missing, and does not say which"}, nil},
		{"expired too", 401, past, nil, missing,
			[]string{"does not send. Add it", "Separately, the JWT in api_key expired on 2026-10-16 12:00 UTC and needs replacing as well"},
			[]string{"Get a new token, put it in api_key"}},
		{"not a JWT", 401, "sk-not-a-jwt", nil, missing,
			[]string{"which this provider does not send"}, []string{"JWT"}},
		{"not a refusal", 500, future, nil, missing, nil, []string{"hint"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := refusalHint(tc.status, tc.key, tc.headers, tc.body, now)
			if len(tc.want) > 0 && !strings.HasPrefix(got, "\n\nhint: the endpoint ") {
				t.Errorf("not a hint paragraph about the endpoint: %q", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("the hint does not say %q: %q", w, got)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("the hint says %q: %q", n, got)
				}
			}
		})
	}
	// A refusal that names no header is the JWT hint, as before.
	if got := refusalHint(401, future, sent, `{"error":"invalid token"}`, now); got != jwtRefusalHint(401, future, `{"error":"invalid token"}`, now) {
		t.Errorf("a refusal that names no header: %q", got)
	}
}

// End to end, the turn's error and /llm-doctor's: the endpoint's words,
// then the header hint in place of the date one.
func TestATurnRefusedForAMissingHeaderSaysSo(t *testing.T) {
	valid := makeJWT(t, map[string]any{"alg": "RS256"}, map[string]any{"exp": time.Now().Add(48 * time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Missing client API key (X-Team-Key header required)","code":"401"}}`))
	}))
	defer srv.Close()
	p := NewOpenAICompat(srv.URL, valid)
	_, err := p.Chat(t.Context(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
	if err == nil || !strings.Contains(err.Error(), "X-Team-Key header required") ||
		!strings.Contains(err.Error(), "hint: the endpoint asks for request header X-Team-Key, which this provider does not send") {
		t.Fatalf("the turn's error: %v", err)
	}
	if strings.Contains(err.Error(), "its date is not why") {
		t.Errorf("the turn's error points at the token: %v", err)
	}
	p.Headers = map[string]string{"X-Team-Key": "made-up-team-key-0001"}
	_, err = p.RawChat(t.Context(), []byte(`{"model":"m","messages":[]}`))
	if err == nil || !strings.Contains(err.Error(), "that header's value the endpoint did not take") || strings.Contains(err.Error(), "made-up-team-key-0001") {
		t.Errorf("RawChat with the header sent: %v", err)
	}
}
