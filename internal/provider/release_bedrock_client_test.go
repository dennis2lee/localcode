package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// expiredSSOSession is the failure this fix exists for: the SSO session
// behind a cached client expired, and every later request fails until the
// process is restarted. Quoted from a real report.
const expiredSSOSession = "bedrock ConverseStream: operation error Bedrock Runtime: ConverseStream, " +
	"get identity: get credentials: failed to refresh cached credentials, " +
	"refresh cached SSO token failed, operation error ssooidc: CreateToken"

// TestExpiredCredentialsMeanADeadClient pins the classifier over a table of
// error values: every shape of "the client can no longer authenticate" must
// drop the cache, and every shape of "the client is fine" must not.
func TestExpiredCredentialsMeanADeadClient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		dead bool
	}{
		// Dead: more than the one phrasing in the report.
		{"sso session expired mid-run", errors.New(expiredSSOSession), true},
		{"sso refresh failed on its own", errors.New("refresh cached SSO token failed, operation error ssooidc: CreateToken"), true},
		{"cached sso token gone with nothing to refresh from", errors.New("cached SSO token is expired, or not present, and cannot be refreshed"), true},
		{"imds chain in isolation", errors.New("no EC2 IMDS role found, operation error ec2imds: GetMetadata"), true},
		{"whole chain refresh failure", errors.New("failed to refresh cached credentials, context deadline exceeded"), true},
		{"credential wrapper with an unfamiliar cause", errors.New("get credentials: something the SDK never said before"), true},
		{"service-side expired token prose", errors.New("An error occurred (UnrecognizedClientException): The security token included in the request is expired"), true},
		{"service-side expired token code", errors.New("ExpiredTokenException: token expired"), true},
		{"wrapped the way Chat wraps it", fmt.Errorf("bedrock ConverseStream: %w", errors.New(expiredSSOSession)), true},
		{"casing is the SDK's, not ours", errors.New("FAILED TO REFRESH CACHED CREDENTIALS, CONTEXT DEADLINE EXCEEDED"), true},

		// Alive: none of these says anything about the client.
		{"nil is not a failure", nil, false},
		{"throttling", errors.New("ThrottlingException: Rate exceeded"), false},
		{"too many requests", errors.New("TooManyRequestsException: too many requests"), false},
		{"service unavailable", errors.New("ServiceUnavailableException: Service unavailable"), false},
		{"model refused the input", errors.New("ValidationException: The provided model identifier is invalid"), false},
		{"model has no entitlement", errors.New("AccessDeniedException: You don't have access to the model with the specified model ID"), false},
		{"model timed out", errors.New("ModelTimeoutException: The model timed out"), false},
		{"plain network blip", errors.New("connection reset by peer"), false},
		{"caller cancelled", context.Canceled, false},
		{"caller timed out", context.DeadlineExceeded, false},
		{"cancelled inside a wrap", fmt.Errorf("bedrock ConverseStream: %w", context.Canceled), false},
		{"sdk cancellation marker", &aws.RequestCanceledError{Err: context.Canceled}, false},
		{"unrelated use of the word token", errors.New("ValidationException: token limit exceeded for this model"), false},
	}

	for _, c := range cases {
		if got := bedrockCredentialsDead(c.err); got != c.dead {
			t.Errorf("bedrockCredentialsDead(%v) = %v, want %v (%s)", c.err, got, c.dead, c.name)
		}
	}
}

// scriptedBedrockClient fails or counts how the test tells it to. A pointer
// is always stored, matching the SDK client, so dropClient's identity
// comparison never meets an uncomparable value here either.
type scriptedBedrockClient struct {
	calls atomic.Int32
	err   error
}

func (f *scriptedBedrockClient) ConverseStream(context.Context, *bedrockruntime.ConverseStreamInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error) {
	f.calls.Add(1)
	return nil, f.err
}

// clientRebuild tracks which client instances the loader handed out and how
// many loads happened, so a test can see a rebuild rather than assume one.
type clientRebuild struct {
	mu     sync.Mutex
	loads  int
	served []*scriptedBedrockClient
}

func (r *clientRebuild) load(err error) bedrockClientLoader {
	return func(context.Context, string, string) (bedrockClient, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.loads++
		fake := &scriptedBedrockClient{err: err}
		r.served = append(r.served, fake)
		return fake, nil
	}
}

func (r *clientRebuild) stats() (loads int, served []*scriptedBedrockClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loads, append([]*scriptedBedrockClient(nil), r.served...)
}

// After a failure that means the cached client can no longer authenticate,
// the next request builds a new one. The request that failed still fails:
// what the caller sees is the error, and only the *next* request is fresh.
// An `aws sso login` between the two is then enough; a restart is not.
func TestBedrockRebuildsTheClientAfterACredentialsFailure(t *testing.T) {
	tracker := &clientRebuild{}
	p := NewBedrock("us-west-2", "profile")
	p.load = tracker.load(errors.New(expiredSSOSession))

	if _, err := p.Chat(context.Background(), minimalBedrockRequest()); err == nil {
		t.Fatal("first Chat error = nil, want the credentials failure")
	} else if !strings.Contains(err.Error(), "failed to refresh cached credentials") {
		t.Fatalf("first Chat error = %q, want the original failure passed to the caller", err)
	}

	// The login happens here, in the real story. The test only needs the
	// next request to stop using the dead client.
	p.load = tracker.load(errors.New("ThrottlingException: Rate exceeded"))
	if _, err := p.Chat(context.Background(), minimalBedrockRequest()); err == nil {
		t.Fatal("second Chat error = nil, want the new client's error")
	} else if !strings.Contains(err.Error(), "ThrottlingException") {
		t.Fatalf("second Chat error = %q, want it served by a rebuilt client", err)
	}

	loads, served := tracker.stats()
	if loads != 2 {
		t.Fatalf("loader ran %d times, want 2: the next request after a credentials failure must rebuild", loads)
	}
	if len(served) != 2 || served[0] == served[1] {
		t.Fatalf("served %d clients, want two distinct instances", len(served))
	}
	if got := served[0].calls.Load(); got != 1 {
		t.Errorf("dead client served %d requests, want exactly the one that failed", got)
	}
	if got := served[1].calls.Load(); got != 1 {
		t.Errorf("rebuilt client served %d requests, want the next one", got)
	}
}

// The neighbour that must not change: a rate limit says nothing about the
// client, so repeated ordinary failures must not rebuild the SDK client on
// every request.
func TestBedrockKeepsTheClientAfterAnOrdinaryFailure(t *testing.T) {
	tracker := &clientRebuild{}
	p := NewBedrock("us-west-2", "profile")
	p.load = tracker.load(errors.New("ThrottlingException: Rate exceeded"))

	for i := 0; i < 3; i++ {
		if _, err := p.Chat(context.Background(), minimalBedrockRequest()); err == nil {
			t.Fatalf("Chat %d error = nil, want the throttling failure", i)
		}
	}

	loads, served := tracker.stats()
	if loads != 1 {
		t.Errorf("loader ran %d times over 3 throttled requests, want 1: ordinary failures must not rebuild", loads)
	}
	if len(served) != 1 || served[0].calls.Load() != 3 {
		t.Errorf("want one client serving all 3 requests, got %d clients", len(served))
	}
}

// Only the client that failed is dropped. Requests run concurrently, and
// one of them may already have rebuilt since this one was sent; throwing
// that rebuild away would turn one expiry into a rebuild per in-flight
// request.
func TestBedrockDropClientKeepsAConcurrentRebuild(t *testing.T) {
	p := NewBedrock("us-west-2", "profile")
	oldFake := &scriptedBedrockClient{err: errors.New(expiredSSOSession)}
	newFake := &scriptedBedrockClient{err: errors.New("ThrottlingException: Rate exceeded")}
	p.client = newFake

	p.dropClient(oldFake)
	if p.client != bedrockClient(newFake) {
		t.Error("dropClient threw away a client another request had already rebuilt")
	}

	p.dropClient(newFake)
	if p.client != nil {
		t.Error("dropClient left the failed client cached")
	}
}

// Concurrent requests share one client and one load: the lock around the
// cache is what this runs under `-race` for.
func TestBedrockConcurrentRequestsShareOneClient(t *testing.T) {
	tracker := &clientRebuild{}
	p := NewBedrock("us-west-2", "profile")
	p.load = tracker.load(errors.New("ThrottlingException: Rate exceeded"))

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Chat(context.Background(), minimalBedrockRequest())
		}()
	}
	wg.Wait()

	if loads, _ := tracker.stats(); loads != 1 {
		t.Errorf("loader ran %d times for 16 concurrent requests, want 1", loads)
	}
}
