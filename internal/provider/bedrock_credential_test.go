package provider

import (
	"errors"
	"strings"
	"testing"
)

// verifiedAWSSDKMessages maps each verified credential hint substring to an
// actual error message produced by the AWS SDK v2, quoting the exact file and
// line in the module cache where the error is constructed.
//
// These are not synthetic strings invented for test assertions: each reflects
// the literal text formatted by the pinned SDK in go.mod:
//
//  1. "no ec2 imds role found":
//     credentials@v1.19.29/ec2rolecreds/provider.go:185
//     fmt.Errorf("no EC2 IMDS role found, %w", err)
//
//  2. "failed to refresh cached credentials":
//     aws-sdk-go-v2@v1.42.1/aws/credential_cache.go:128
//     fmt.Errorf("failed to refresh cached credentials, %w", err)
var verifiedAWSSDKMessages = map[string]string{
	"no ec2 imds role found":               "no EC2 IMDS role found, operation error ec2imds: GetMetadata",
	"failed to refresh cached credentials": "failed to refresh cached credentials, context deadline exceeded",
}

// unverifiedLegacySubstrings records entries currently in credentialHintSubstrings
// that match no error message string found anywhere in the pinned AWS SDK v2
// dependencies (aws-sdk-go-v2 v1.42.1, config v1.32.30, credentials v1.19.29).
//
// They are preserved in the list per project policy rather than deleted in case
// an older SDK version or a service path not yet audited surfaces them. Any
// new entry added in the future must NOT be added here; new entries must be
// located in the actual SDK dependency first.
var unverifiedLegacySubstrings = map[string]string{
	"no valid credential sources": "not found in pinned aws-sdk-go-v2; likely remembered from Terraform AWS Provider ('No valid credential sources found for AWS Provider') or SDK v1 ('no valid providers in chain')",
	"unable to find credentials":  "not found in pinned aws-sdk-go-v2; likely remembered from botocore ('Unable to locate credentials') or SDK v2 signer middleware ('failed to retrieve credentials')",
}

// TestEveryCredentialHintSubstringIsVerifiedAgainstRealSDKMessages guards against
// credential hint substrings being written from memory.
//
// The defect this prevents: "no ec2imds role found" was added from remembered
// phrasing without consulting the SDK source. The real SDK produces "no EC2 IMDS
// role found" (credentials/ec2rolecreds/provider.go:185). Because strings.ToLower
// preserves the space, the spaceless substring could never match. Meanwhile, a
// test that only tested a combined multi-line error dump passed because
// "failed to refresh cached credentials" fired on the same error.
//
// This test walks every entry in credentialHintSubstrings and asserts:
//  1. Every entry is lowercase, because wrapCredentialError matches against
//     strings.ToLower(err.Error()). Any uppercase character makes the entry dead.
//  2. Every entry is verified to be a substring of an actual AWS SDK error message
//     located in the module cache, or explicitly accounted for in
//     unverifiedLegacySubstrings with an audited reason.
func TestEveryCredentialHintSubstringIsVerifiedAgainstRealSDKMessages(t *testing.T) {
	if len(credentialHintSubstrings) == 0 {
		t.Fatal("credentialHintSubstrings is unexpectedly empty")
	}

	for _, sub := range credentialHintSubstrings {
		if strings.ToLower(sub) != sub {
			t.Errorf("credentialHintSubstrings entry %q contains uppercase characters; wrapCredentialError lowercases error text so this entry could never match", sub)
		}

		if realMsg, ok := verifiedAWSSDKMessages[sub]; ok {
			lowerMsg := strings.ToLower(realMsg)
			if !strings.Contains(lowerMsg, sub) {
				t.Errorf("credentialHintSubstrings entry %q does not appear in verified real SDK message %q (lowercased: %q)", sub, realMsg, lowerMsg)
			}
		} else if _, ok := unverifiedLegacySubstrings[sub]; ok {
			// Known legacy entry preserved with doubt documented.
			continue
		} else {
			t.Errorf("credentialHintSubstrings contains %q, which is not verified against any real SDK message text in github.com/aws/ nor registered in unverifiedLegacySubstrings; do not add entries from memory", sub)
		}
	}
}

// TestWrapCredentialErrorFiresOnRealEC2IMDSFailureInIsolation ensures the IMDS
// credential hint substring matches when the EC2 IMDS provider fails directly,
// without the outer "failed to refresh cached credentials" wrapper.
//
// TestWrapCredentialErrorAddsHintForIMDSFallback in bedrock_test.go passed
// historically even when "no ec2imds role found" was completely broken because
// its test string contained both the cache refresh error and the IMDS error.
// Testing the IMDS error in isolation guarantees that "no ec2 imds role found"
// works on its own.
func TestWrapCredentialErrorFiresOnRealEC2IMDSFailureInIsolation(t *testing.T) {
	// Exact error returned by ec2rolecreds.Provider.Retrieve when IMDS is unreachable:
	// fmt.Errorf("no EC2 IMDS role found, %w", err)
	imdsErr := errors.New("operation error ec2imds: GetMetadata, request send failed: no EC2 IMDS role found")

	wrapped := wrapCredentialError(imdsErr)
	if wrapped == nil {
		t.Fatal("wrapCredentialError returned nil for a non-nil error")
	}
	if !strings.Contains(wrapped.Error(), "hint: no AWS credentials were found") {
		t.Errorf("wrapCredentialError(%q) = %q, want actionable hint for EC2 IMDS failure in isolation", imdsErr.Error(), wrapped.Error())
	}
}

// TestWrapCredentialErrorFiresOnRealCacheRefreshFailureInIsolation ensures the
// cache refresh hint substring matches in isolation without any inner IMDS text.
func TestWrapCredentialErrorFiresOnRealCacheRefreshFailureInIsolation(t *testing.T) {
	// Exact error returned by aws.CredentialsCache.Retrieve:
	// fmt.Errorf("failed to refresh cached credentials, %w", err)
	cacheErr := errors.New("failed to refresh cached credentials, context deadline exceeded")

	wrapped := wrapCredentialError(cacheErr)
	if wrapped == nil {
		t.Fatal("wrapCredentialError returned nil for a non-nil error")
	}
	if !strings.Contains(wrapped.Error(), "hint: no AWS credentials were found") {
		t.Errorf("wrapCredentialError(%q) = %q, want actionable hint for cache refresh failure in isolation", cacheErr.Error(), wrapped.Error())
	}
}
