// A minimal remote MCP server used by the mcp package's tests: it serves
// the same trivial "echo" tool as the stdio fixture, but over the network,
// so the http and sse transports (and the header plumbing) are exercised
// against a real server rather than a mock transport.
//
// It listens on 127.0.0.1 and prints the chosen URL on stdout, so a test or
// a manual run does not need a fixed port.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
	Text string `json:"text" jsonschema:"text to echo"`
}

func main() {
	transport := flag.String("transport", "http", "http (streamable) or sse")
	requireHeader := flag.String("require-header", "", "if set, KEY:VALUE that every request must carry")
	flag.Parse()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "httpserver", Version: "0.1.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo", Description: "echo the input back"},
		func(ctx context.Context, req *mcpsdk.CallToolRequest, args echoArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + args.Text}},
			}, nil, nil
		})

	var handler http.Handler
	switch *transport {
	case "sse":
		handler = mcpsdk.NewSSEHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	default:
		handler = mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	}

	if *requireHeader != "" {
		key, want, ok := cut(*requireHeader, ":")
		if !ok {
			log.Fatalf("--require-header must be KEY:VALUE")
		}
		inner := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(key) != want {
				http.Error(w, "missing or wrong "+key, http.StatusUnauthorized)
				return
			}
			inner.ServeHTTP(w, r)
		})
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("http://%s\n", ln.Addr().String())
	log.Fatal(http.Serve(ln, handler))
}

func cut(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
