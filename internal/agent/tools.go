package agent

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/mark3labs/mcp-go/mcp"
)

// convertTools maps the MCP tool catalog to Anthropic tool definitions. The MCP
// input schema (a JSON Schema object) carries straight over: its properties and
// required list are what the model needs to build a valid call.
func convertTools(tools []mcp.Tool) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tp := anthropic.ToolParam{
			Name: t.Name,
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: t.InputSchema.Properties,
				Required:   t.InputSchema.Required,
			},
		}
		if t.Description != "" {
			tp.Description = anthropic.String(t.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out
}
