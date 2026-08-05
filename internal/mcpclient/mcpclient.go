// Package mcpclient is a thin wrapper over the mark3labs MCP client for the
// lfpweather-mcp server. It connects over streamable HTTP, discovers the tool
// catalog once, and executes tool calls.
package mcpclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Client is a connected MCP session with a cached tool list.
type Client struct {
	c     *client.Client
	tools []mcp.Tool
}

// New connects to the MCP server at url, initializes the session, and lists the
// available tools. A non-empty bearer token is sent as an Authorization header.
func New(ctx context.Context, url, bearerToken, clientVersion string) (*Client, error) {
	var opts []transport.StreamableHTTPCOption
	if bearerToken != "" {
		opts = append(opts, transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + bearerToken,
		}))
	}

	c, err := client.NewStreamableHttpClient(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("create mcp client: %w", err)
	}

	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("start mcp client: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "lfpweather-agent",
		Version: clientVersion,
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize mcp session: %w", err)
	}

	listed, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("list mcp tools: %w", err)
	}

	return &Client{c: c, tools: listed.Tools}, nil
}

// Tools returns the discovered tool catalog.
func (c *Client) Tools() []mcp.Tool {
	return c.tools
}

// Call invokes a tool by name with the given arguments. It returns the tool's
// text output and whether the tool reported an error result.
func (c *Client) Call(ctx context.Context, name string, args map[string]any) (text string, isError bool, err error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	res, err := c.c.CallTool(ctx, req)
	if err != nil {
		return "", false, err
	}

	return resultText(res.Content), res.IsError, nil
}

// Close ends the MCP session.
func (c *Client) Close() error {
	return c.c.Close()
}

// resultText concatenates the text parts of a tool result, ignoring non-text
// content (images, embedded resources), which these tools do not return.
func resultText(content []mcp.Content) string {
	var sb strings.Builder
	for _, part := range content {
		if t, ok := part.(mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}
