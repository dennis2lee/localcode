package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"localcode/internal/debuglog"
)

// Reported: with a Bedrock model, the debug log holds the answer and not
// the question. It was true, and neither of the two earlier attempts at
// it would have helped.
//
// The AWS SDK builds a request with no GetBody and, for a signed body,
// ContentLength -1 (smithy transport/http/request.go Build). So the
// "read it up front" path skipped it as streamed, and the "ask GetBody
// for a second copy" path had nothing to ask. The body is teed as the
// transport sends it now, which needs nothing from the client.
//
// This drives the real SDK client against a local server, so it is the
// SDK's own request shape under test rather than a hand-made one.
func TestABedrockRequestIsInTheDebugLog(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = string(body)
		// Enough of an event-stream reply that the SDK does not error
		// before the request is on the wire; the request is what is
		// under test.
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink, err := debuglog.Create(t.TempDir(), "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx := debuglog.With(context.Background(), sink)

	client := bedrockruntime.New(bedrockruntime.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   loggingAWSClient(nil),
	})
	// The error is expected: the fake server answers no real event
	// stream. What matters is that the request went out and was logged.
	_, _ = client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String("us.anthropic.claude-sonnet-5"),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "count to three"}},
		}},
	})
	sink.Close()

	if !strings.Contains(got, "count to three") {
		t.Fatalf("the server did not receive the prompt, so this test proves nothing: %q", got)
	}
	body, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	log := string(body)
	for _, want := range []string{"POST ", "converse-stream", "count to three", "claude-sonnet-5"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log is missing %q from the Bedrock request:\n%s", want, log)
		}
	}
	// The signature must not be in the file, and the body must not have
	// been disturbed on its way out.
	if strings.Contains(log, "secret") {
		t.Errorf("the log carries the secret key:\n%s", log)
	}
}
