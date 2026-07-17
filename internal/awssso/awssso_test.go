package awssso

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
)

// TestCacheFilePathMatchesAWSCLIHash pins the cache filename algorithm
// (sha1 of the start URL) against known-good fixtures lifted from
// aws-sdk-go-v2/config's own test data, so this stays interoperable with
// what `aws sso login`/the default credential chain expect.
func TestCacheFilePathMatchesAWSCLIHash(t *testing.T) {
	cases := map[string]string{
		"https://127.0.0.1/start":                            "eb5e43e71ce87dd92ec58903d76debd8ee42aefd",
		"https://my-sso-config-profile-role.awsapps.com/start": "451f9e12ee256d215dc6127714a901985c2dcf16",
		"https://d-123456789a.awsapps.com/start":               "2522b24e4bb6b800675965656425e3d136c896ad",
	}
	for url, wantHash := range cases {
		got := CacheFilePath("/home/x", url)
		want := filepath.Join("/home/x", ".aws", "sso", "cache", wantHash+".json")
		if got != want {
			t.Errorf("CacheFilePath(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestWriteTokenCacheRoundTrip(t *testing.T) {
	home := t.TempDir()
	tok := Token{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		ExpiresAt:    time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	if err := WriteTokenCache(home, "https://example.awsapps.com/start", "us-east-1", tok); err != nil {
		t.Fatalf("WriteTokenCache: %v", err)
	}

	path := CacheFilePath(home, "https://example.awsapps.com/start")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	var got cacheFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal cache file: %v", err)
	}
	if got.AccessToken != "access-123" {
		t.Errorf("accessToken = %q, want %q", got.AccessToken, "access-123")
	}
	if got.ExpiresAt != "2030-01-02T03:04:05Z" {
		t.Errorf("expiresAt = %q, want RFC3339 UTC", got.ExpiresAt)
	}
	if got.StartURL != "https://example.awsapps.com/start" {
		t.Errorf("startUrl = %q, want the start URL preserved", got.StartURL)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode = %v, want 0600 (contains a bearer token)", info.Mode().Perm())
	}
}

func TestWriteProfileCreatesBlock(t *testing.T) {
	home := t.TempDir()
	if err := WriteProfile(home, "my-bedrock", "https://example.awsapps.com/start", "us-east-1", "123456789012", "MyRole", "us-west-2"); err != nil {
		t.Fatalf("WriteProfile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".aws", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"[profile my-bedrock]",
		"sso_start_url = https://example.awsapps.com/start",
		"sso_region = us-east-1",
		"sso_account_id = 123456789012",
		"sso_role_name = MyRole",
		"region = us-west-2",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config content = %q, want it to contain %q", content, want)
		}
	}
}

func TestWriteProfileDoesNotClobberExisting(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "[profile my-bedrock]\nsso_start_url = https://custom-existing/start\n"
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteProfile(home, "my-bedrock", "https://new-url/start", "us-east-1", "999", "OtherRole", "us-west-2"); err != nil {
		t.Fatalf("WriteProfile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".aws", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("existing profile was modified: got %q, want unchanged %q", string(data), original)
	}
}

func TestWriteProfileAppendsAfterExistingContent(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte("[profile other]\nregion = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteProfile(home, "new-profile", "https://x/start", "us-east-1", "1", "R", "us-west-2"); err != nil {
		t.Fatalf("WriteProfile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".aws", "config"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "[profile other]") || !strings.Contains(content, "[profile new-profile]") {
		t.Errorf("expected both profiles present, got %q", content)
	}
}

// --- pollForToken retry/backoff logic, tested against a fake oidcClient ---

type fakeOIDC struct {
	createTokenCalls int
	responses        []func() (*ssooidc.CreateTokenOutput, error)
}

func (f *fakeOIDC) RegisterClient(context.Context, *ssooidc.RegisterClientInput, ...func(*ssooidc.Options)) (*ssooidc.RegisterClientOutput, error) {
	return nil, errors.New("not used in these tests")
}

func (f *fakeOIDC) StartDeviceAuthorization(context.Context, *ssooidc.StartDeviceAuthorizationInput, ...func(*ssooidc.Options)) (*ssooidc.StartDeviceAuthorizationOutput, error) {
	return nil, errors.New("not used in these tests")
}

func (f *fakeOIDC) CreateToken(context.Context, *ssooidc.CreateTokenInput, ...func(*ssooidc.Options)) (*ssooidc.CreateTokenOutput, error) {
	i := f.createTokenCalls
	f.createTokenCalls++
	if i >= len(f.responses) {
		return nil, errors.New("fake exhausted")
	}
	return f.responses[i]()
}

// withFastPolling shrinks pollIntervalUnit for the duration of a test, so
// pollForToken's real retry/backoff logic runs against milliseconds
// instead of seconds.
func withFastPolling(t *testing.T) {
	t.Helper()
	old := pollIntervalUnit
	pollIntervalUnit = time.Millisecond
	t.Cleanup(func() { pollIntervalUnit = old })
}

func TestPollForTokenRetriesOnAuthorizationPending(t *testing.T) {
	withFastPolling(t)
	fake := &fakeOIDC{
		responses: []func() (*ssooidc.CreateTokenOutput, error){
			func() (*ssooidc.CreateTokenOutput, error) { return nil, &types.AuthorizationPendingException{} },
			func() (*ssooidc.CreateTokenOutput, error) { return nil, &types.AuthorizationPendingException{} },
			func() (*ssooidc.CreateTokenOutput, error) {
				return &ssooidc.CreateTokenOutput{AccessToken: aws.String("finally"), ExpiresIn: 3600}, nil
			},
		},
	}
	reg := &ssooidc.RegisterClientOutput{ClientId: aws.String("cid"), ClientSecret: aws.String("secret")}
	auth := &ssooidc.StartDeviceAuthorizationOutput{DeviceCode: aws.String("dc"), Interval: 0, ExpiresIn: 60}

	tok, err := pollForToken(context.Background(), fake, reg, auth)
	if err != nil {
		t.Fatalf("pollForToken: %v", err)
	}
	if aws.ToString(tok.AccessToken) != "finally" {
		t.Errorf("AccessToken = %q, want %q", aws.ToString(tok.AccessToken), "finally")
	}
	if fake.createTokenCalls != 3 {
		t.Errorf("createTokenCalls = %d, want 3 (two pending + one success)", fake.createTokenCalls)
	}
}

func TestPollForTokenReturnsErrorOnOtherFailure(t *testing.T) {
	withFastPolling(t)
	fake := &fakeOIDC{
		responses: []func() (*ssooidc.CreateTokenOutput, error){
			func() (*ssooidc.CreateTokenOutput, error) { return nil, &types.ExpiredTokenException{} },
		},
	}
	reg := &ssooidc.RegisterClientOutput{ClientId: aws.String("cid"), ClientSecret: aws.String("secret")}
	auth := &ssooidc.StartDeviceAuthorizationOutput{DeviceCode: aws.String("dc"), Interval: 0, ExpiresIn: 60}

	if _, err := pollForToken(context.Background(), fake, reg, auth); err == nil {
		t.Error("expected an error for a non-pending CreateToken failure")
	}
}

func TestPollForTokenExpiresWhenDeadlinePasses(t *testing.T) {
	fake := &fakeOIDC{
		responses: []func() (*ssooidc.CreateTokenOutput, error){
			func() (*ssooidc.CreateTokenOutput, error) { return nil, &types.AuthorizationPendingException{} },
		},
	}
	reg := &ssooidc.RegisterClientOutput{ClientId: aws.String("cid"), ClientSecret: aws.String("secret")}
	// ExpiresIn shorter than Interval means the deadline check should fire
	// before ever calling CreateToken a second time.
	auth := &ssooidc.StartDeviceAuthorizationOutput{DeviceCode: aws.String("dc"), Interval: 0, ExpiresIn: 0}

	if _, err := pollForToken(context.Background(), fake, reg, auth); err == nil {
		t.Error("expected an expiry error when the device code's window has passed")
	}
}

func TestLoginCallsOnAuthBeforePolling(t *testing.T) {
	// login() itself calls RegisterClient/StartDeviceAuthorization which
	// the fake doesn't support — this test only exercises that a
	// RegisterClient failure surfaces as an error rather than a panic, and
	// (implicitly) that onAuth is never called if registration fails.
	fake := &fakeOIDC{}
	calledOnAuth := false
	_, err := login(context.Background(), fake, "https://example/start", func(DeviceAuth) { calledOnAuth = true })
	if err == nil {
		t.Fatal("expected an error since the fake's RegisterClient always fails")
	}
	if calledOnAuth {
		t.Error("onAuth should not be called when RegisterClient fails")
	}
}
