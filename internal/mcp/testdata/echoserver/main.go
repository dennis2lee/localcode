// echoserver is a minimal MCP server fixture used only by
// internal/mcp's tests, to exercise Connect() against a real stdio
// subprocess speaking the actual protocol rather than an in-process mock.
package main

import (
	"context"
	"log"
	"os"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
	Text string `json:"text" jsonschema:"the text to echo back"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "echoserver", Version: "0.0.1"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echo back the given text",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		// A text starting with "error:" comes back as a tool-level error
		// (isError true) carrying the rest verbatim, so tests can exercise
		// the error path of a real server rather than a mock's.
		if rest, ok := strings.CutPrefix(args.Text, "error:"); ok {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: rest}},
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + args.Text}},
		}, nil, nil
	})

	// --array-schema-tool also lists a tool whose input schema is an
	// array, which MCP forbids and the SDK will not register. It is added
	// to the listing on the way out, the only place a broken server's
	// answer can be imitated with this SDK.
	if slices.Contains(os.Args[1:], "--array-schema-tool") {
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				res, err := next(ctx, method, req)
				if list, ok := res.(*mcp.ListToolsResult); ok && err == nil {
					list.Tools = append(list.Tools, &mcp.Tool{
						Name:        "listed_as_array",
						Description: "a tool whose input is not an object",
						InputSchema: map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					})
				}
				return res, err
			}
		})
	}

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
