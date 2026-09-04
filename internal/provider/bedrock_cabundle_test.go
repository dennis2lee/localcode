package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// v0.98.0 gave the AWS config loader a plain *http.Client so that
// "/debug-log" would see Bedrock. With a custom CA bundle configured the
// loader asserts the concrete awshttp.BuildableClient to add the roots
// to, so every Bedrock call failed at config load with "unable to add
// custom RootCAs HTTPClient, has no WithTransportOptions".
//
// This is that configuration. It needs no network and no credentials:
// loading the config is the step that used to fail.
func TestBedrockLoadsWithACustomCABundle(t *testing.T) {
	t.Setenv("AWS_CA_BUNDLE", writeTestCA(t))
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "us-east-1")

	if _, err := loadBedrockClient(context.Background(), "us-east-1", ""); err != nil {
		t.Fatalf("load with a custom CA bundle: %v", err)
	}
}

// The wrapper delegates to the client the loader resolved rather than
// replacing it, which is what keeps that CA bundle in play.
func TestTheAWSWrapperDelegatesToTheResolvedClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From", "inner")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	var used bool
	inner := doerFunc(func(req *http.Request) (*http.Response, error) {
		used = true
		return http.DefaultClient.Do(req)
	})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := loggingAWSClient(inner).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !used {
		t.Error("the wrapper did not go through the resolved client")
	}
	if resp.StatusCode != http.StatusTeapot || resp.Header.Get("X-From") != "inner" {
		t.Errorf("the answer did not come back intact: %s", resp.Status)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// writeTestCA writes a self-signed certificate for AWS_CA_BUNDLE to
// parse. Generated rather than checked in: a fixture certificate expires
// and takes the test with it.
func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localcode test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
