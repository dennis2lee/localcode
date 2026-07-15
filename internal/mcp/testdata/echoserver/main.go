// echoserver is a minimal MCP server fixture used only by
// internal/mcp's tests, to exercise Connect() against a real stdio
// subprocess speaking the actual protocol rather than an in-process mock.
package main

import (
	"context"
	"log"

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
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + args.Text}},
		}, nil, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
