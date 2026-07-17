// Package awssso implements the AWS IAM Identity Center (SSO) OIDC
// device-authorization flow so `localcode login bedrock` can authenticate
// a user against Bedrock without requiring the AWS CLI to be installed.
// It writes its result to exactly the same places the AWS CLI does
// (~/.aws/sso/cache/<sha1(start-url)>.json and a profile block in
// ~/.aws/config), so the default AWS credential chain — which is all
// internal/provider.Bedrock relies on — picks the credentials up with no
// further plumbing on this project's side.
package awssso

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// Token is an AWS SSO bearer token, along with the client registration it
// was issued under (needed to refresh it later, same as the AWS CLI's own
// cache format).
type Token struct {
	AccessToken           string
	RefreshToken          string
	ClientID              string
	ClientSecret          string
	ExpiresAt             time.Time
	RegistrationExpiresAt time.Time
}

// oidcClient is the subset of ssooidc.Client that the device flow needs —
// narrowed to an interface so the polling/backoff logic can be unit tested
// against a fake without a real AWS endpoint.
type oidcClient interface {
	RegisterClient(ctx context.Context, in *ssooidc.RegisterClientInput, optFns ...func(*ssooidc.Options)) (*ssooidc.RegisterClientOutput, error)
	StartDeviceAuthorization(ctx context.Context, in *ssooidc.StartDeviceAuthorizationInput, optFns ...func(*ssooidc.Options)) (*ssooidc.StartDeviceAuthorizationOutput, error)
	CreateToken(ctx context.Context, in *ssooidc.CreateTokenInput, optFns ...func(*ssooidc.Options)) (*ssooidc.CreateTokenOutput, error)
}

// DeviceAuth is what the caller shows the user to complete login in a
// browser (on any device — device-flow verification URLs are not tied to
// the machine running this process).
type DeviceAuth struct {
	VerificationURIComplete string
	VerificationURI         string
	UserCode                string
	ExpiresIn               time.Duration
}

// Login runs the full device-authorization flow against startURL/ssoRegion:
// registers a public client, starts device authorization, invokes
// onAuth with the URL/code to show the user, and polls CreateToken until
// the user approves (or the code expires). onAuth is called exactly once,
// before polling begins.
func Login(ctx context.Context, startURL, ssoRegion string, onAuth func(DeviceAuth)) (Token, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(ssoRegion))
	if err != nil {
		return Token{}, fmt.Errorf("load AWS config: %w", err)
	}
	client := ssooidc.NewFromConfig(cfg)
	return login(ctx, client, startURL, onAuth)
}

func login(ctx context.Context, client oidcClient, startURL string, onAuth func(DeviceAuth)) (Token, error) {
	reg, err := client.RegisterClient(ctx, &ssooidc.RegisterClientInput{
		ClientName: aws.String("localcode"),
		ClientType: aws.String("public"),
	})
	if err != nil {
		return Token{}, fmt.Errorf("register OIDC client: %w", err)
	}

	auth, err := client.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{
		ClientId:     reg.ClientId,
		ClientSecret: reg.ClientSecret,
		StartUrl:     aws.String(startURL),
	})
	if err != nil {
		return Token{}, fmt.Errorf("start device authorization: %w", err)
	}

	onAuth(DeviceAuth{
		VerificationURIComplete: aws.ToString(auth.VerificationUriComplete),
		VerificationURI:         aws.ToString(auth.VerificationUri),
		UserCode:                aws.ToString(auth.UserCode),
		ExpiresIn:               time.Duration(auth.ExpiresIn) * time.Second,
	})

	tok, err := pollForToken(ctx, client, reg, auth)
	if err != nil {
		return Token{}, err
	}

	regExpiresAt := time.Unix(reg.ClientIdIssuedAt, 0).Add(90 * 24 * time.Hour) // AWS issues public clients with a 90-day registration lifetime
	return Token{
		AccessToken:           aws.ToString(tok.AccessToken),
		RefreshToken:          aws.ToString(tok.RefreshToken),
		ClientID:              aws.ToString(reg.ClientId),
		ClientSecret:          aws.ToString(reg.ClientSecret),
		ExpiresAt:             time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
		RegistrationExpiresAt: regExpiresAt,
	}, nil
}

// pollIntervalUnit scales auth.Interval/auth.ExpiresIn (both in whole
// seconds, per the AWS API) into real durations. Production always uses
// time.Second; tests shrink it so the retry/backoff logic can be exercised
// without actually sleeping for seconds.
var pollIntervalUnit = time.Second

// pollForToken repeats CreateToken at StartDeviceAuthorizationOutput's
// advertised interval (backing off further on a SlowDownException) until
// the user approves, the code expires, or another error occurs.
func pollForToken(ctx context.Context, client oidcClient, reg *ssooidc.RegisterClientOutput, auth *ssooidc.StartDeviceAuthorizationOutput) (*ssooidc.CreateTokenOutput, error) {
	interval := time.Duration(auth.Interval) * pollIntervalUnit
	if interval <= 0 {
		interval = 5 * pollIntervalUnit
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * pollIntervalUnit)

	for {
		if time.Now().After(deadline) {
			return nil, errors.New("device authorization code expired before login completed")
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		tok, err := client.CreateToken(ctx, &ssooidc.CreateTokenInput{
			ClientId:     reg.ClientId,
			ClientSecret: reg.ClientSecret,
			GrantType:    aws.String(deviceGrantType),
			DeviceCode:   auth.DeviceCode,
		})
		if err == nil {
			return tok, nil
		}

		var pending *types.AuthorizationPendingException
		var slow *types.SlowDownException
		switch {
		case errors.As(err, &pending):
			continue // user hasn't approved yet — keep polling at the same interval
		case errors.As(err, &slow):
			interval += 5 * pollIntervalUnit // AWS asked us to back off
			continue
		default:
			return nil, fmt.Errorf("create token: %w", err)
		}
	}
}

// CacheFilePath returns where the AWS CLI (and this package) caches the
// SSO bearer token for a given start URL: ~/.aws/sso/cache/<sha1 hex of
// the start URL>.json — verified against aws-sdk-go-v2/config's own test
// fixtures, not guessed.
func CacheFilePath(home, startURL string) string {
	sum := sha1.Sum([]byte(startURL))
	return filepath.Join(home, ".aws", "sso", "cache", hex.EncodeToString(sum[:])+".json")
}

type cacheFile struct {
	AccessToken           string `json:"accessToken"`
	ExpiresAt             string `json:"expiresAt"`
	RefreshToken          string `json:"refreshToken,omitempty"`
	ClientID              string `json:"clientId,omitempty"`
	ClientSecret          string `json:"clientSecret,omitempty"`
	ClientIDIssuedAt      string `json:"clientIdIssuedAt,omitempty"`
	Region                string `json:"region,omitempty"`
	StartURL              string `json:"startUrl,omitempty"`
	RegistrationExpiresAt string `json:"registrationExpiresAt,omitempty"`
}

// WriteTokenCache writes tok to the AWS-CLI-compatible cache file for
// startURL, so the default AWS credential chain resolves it for any
// profile referencing this start URL without this project needing its own
// Bedrock credential-resolution path.
func WriteTokenCache(home, startURL, ssoRegion string, tok Token) error {
	path := CacheFilePath(home, startURL)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create sso cache dir: %w", err)
	}

	data, err := json.MarshalIndent(cacheFile{
		AccessToken:           tok.AccessToken,
		ExpiresAt:             tok.ExpiresAt.UTC().Format(time.RFC3339),
		RefreshToken:          tok.RefreshToken,
		ClientID:              tok.ClientID,
		ClientSecret:          tok.ClientSecret,
		Region:                ssoRegion,
		StartURL:              startURL,
		RegistrationExpiresAt: tok.RegistrationExpiresAt.UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sso cache: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// Account and Role mirror the SSO service's own listing types, narrowed to
// the fields the profile-selection prompt needs.
type Account struct {
	ID    string
	Name  string
	Email string
}

type Role struct {
	AccountID string
	Name      string
}

// ListAccounts returns every AWS account the bearer token grants access
// to.
func ListAccounts(ctx context.Context, ssoRegion, accessToken string) ([]Account, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(ssoRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	client := sso.NewFromConfig(cfg)

	var out []Account
	var nextToken *string
	for {
		resp, err := client.ListAccounts(ctx, &sso.ListAccountsInput{AccessToken: aws.String(accessToken), NextToken: nextToken})
		if err != nil {
			return nil, fmt.Errorf("list accounts: %w", err)
		}
		for _, a := range resp.AccountList {
			out = append(out, Account{ID: aws.ToString(a.AccountId), Name: aws.ToString(a.AccountName), Email: aws.ToString(a.EmailAddress)})
		}
		if resp.NextToken == nil {
			return out, nil
		}
		nextToken = resp.NextToken
	}
}

// ListAccountRoles returns every SSO role the bearer token can assume in
// accountID.
func ListAccountRoles(ctx context.Context, ssoRegion, accessToken, accountID string) ([]Role, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(ssoRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	client := sso.NewFromConfig(cfg)

	var out []Role
	var nextToken *string
	for {
		resp, err := client.ListAccountRoles(ctx, &sso.ListAccountRolesInput{AccessToken: aws.String(accessToken), AccountId: aws.String(accountID), NextToken: nextToken})
		if err != nil {
			return nil, fmt.Errorf("list account roles: %w", err)
		}
		for _, r := range resp.RoleList {
			out = append(out, Role{AccountID: aws.ToString(r.AccountId), Name: aws.ToString(r.RoleName)})
		}
		if resp.NextToken == nil {
			return out, nil
		}
		nextToken = resp.NextToken
	}
}

// WriteProfile appends a `[profile name]` block for the legacy (non
// sso-session) SSO profile shape to ~/.aws/config, unless a profile with
// that exact name already exists — in which case it's left untouched
// (assume the user configured it deliberately) and this is a no-op.
// region is the region Bedrock calls should use, which may differ from
// ssoRegion (the region IAM Identity Center itself runs in).
func WriteProfile(home, name, startURL, ssoRegion, accountID, roleName, region string) error {
	path := filepath.Join(home, ".aws", "config")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	header := "[profile " + name + "]"
	if strings.Contains(string(existing), header) {
		return nil // don't clobber an existing profile
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	var b strings.Builder
	if len(existing) > 0 {
		if !strings.HasSuffix(string(existing), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s\nsso_start_url = %s\nsso_region = %s\nsso_account_id = %s\nsso_role_name = %s\nregion = %s\n", header, startURL, ssoRegion, accountID, roleName, region)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
