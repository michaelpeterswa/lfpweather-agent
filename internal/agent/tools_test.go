package agent

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestConvertTools(t *testing.T) {
	in := []mcp.Tool{
		{
			Name:        "get_weather_records",
			Description: "record highs and lows",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"period": map[string]any{"type": "string"},
					"at":     map[string]any{"type": "string"},
				},
				Required: []string{"period"},
			},
		},
	}

	out := convertTools(in)
	if len(out) != 1 {
		t.Fatalf("converted %d tools, want 1", len(out))
	}

	tp := out[0].OfTool
	if tp == nil {
		t.Fatal("OfTool is nil")
	}
	if tp.Name != "get_weather_records" {
		t.Errorf("name = %q, want get_weather_records", tp.Name)
	}
	if len(tp.InputSchema.Required) != 1 || tp.InputSchema.Required[0] != "period" {
		t.Errorf("required = %v, want [period]", tp.InputSchema.Required)
	}
	props, ok := tp.InputSchema.Properties.(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", tp.InputSchema.Properties)
	}
	if _, ok := props["period"]; !ok {
		t.Error("properties missing 'period'")
	}
}

func TestConvertToolsEmpty(t *testing.T) {
	if got := convertTools(nil); len(got) != 0 {
		t.Errorf("convertTools(nil) len = %d, want 0", len(got))
	}
}
