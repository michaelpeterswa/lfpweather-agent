// Package agent runs the Claude tool-use loop for one conversation, streaming
// assistant text and executing tools through the MCP client.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/mark3labs/mcp-go/mcp"
)

// EventType classifies a streamed agent event.
type EventType string

const (
	// EventToken is a chunk of assistant text.
	EventToken EventType = "token"
	// EventTool marks a tool call starting or finishing.
	EventTool EventType = "tool"
	// EventDone marks the end of the response to a user message.
	EventDone EventType = "done"
	// EventError marks a failure; the turn is over.
	EventError EventType = "error"
)

// Event is one item on the agent's output stream.
type Event struct {
	Type   EventType `json:"type"`
	Text   string    `json:"text,omitempty"`   // token text, or error message
	Tool   string    `json:"tool,omitempty"`   // tool name for EventTool
	Status string    `json:"status,omitempty"` // "start" | "done" for EventTool
}

// toolCaller executes an MCP tool. Satisfied by *mcpclient.Client.
type toolCaller interface {
	Call(ctx context.Context, name string, args map[string]any) (text string, isError bool, err error)
}

// Agent holds the model configuration and tool surface for conversations.
type Agent struct {
	client   anthropic.Client
	tools    toolCaller
	toolDefs []anthropic.ToolUnionParam
	model    string
	system   string
	maxTok   int64
	maxTurns int
}

// Options configures an Agent.
type Options struct {
	Model        string
	System       string
	MaxTokens    int64
	MaxTurns     int
	MCPTools     []mcp.Tool
	ToolExecutor toolCaller
}

// New builds an Agent. The Anthropic API key is read from the environment by
// the SDK unless overridden via opts on the client (kept simple here).
func New(client anthropic.Client, opts Options) *Agent {
	return &Agent{
		client:   client,
		tools:    opts.ToolExecutor,
		toolDefs: convertTools(opts.MCPTools),
		model:    opts.Model,
		system:   opts.System,
		maxTok:   opts.MaxTokens,
		maxTurns: opts.MaxTurns,
	}
}

// Run answers one user message. It appends the exchange to history and returns
// the updated history. Assistant text and tool activity are delivered through
// emit as they happen. emit is called only from the calling goroutine.
func (a *Agent) Run(ctx context.Context, history []anthropic.MessageParam, userText string, emit func(Event)) ([]anthropic.MessageParam, error) {
	history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(userText)))

	for turn := 0; turn < a.maxTurns; turn++ {
		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(a.model),
			MaxTokens: a.maxTok,
			Messages:  history,
			Tools:     a.toolDefs,
		}
		if a.system != "" {
			params.System = []anthropic.TextBlockParam{{Text: a.system}}
		}

		stream := a.client.Messages.NewStreaming(ctx, params)
		msg := anthropic.Message{}
		for stream.Next() {
			event := stream.Current()
			if err := msg.Accumulate(event); err != nil {
				return history, fmt.Errorf("accumulate stream: %w", err)
			}
			if delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
				if td, ok := delta.Delta.AsAny().(anthropic.TextDelta); ok && td.Text != "" {
					emit(Event{Type: EventToken, Text: td.Text})
				}
			}
		}
		if err := stream.Err(); err != nil {
			return history, fmt.Errorf("model stream: %w", err)
		}

		history = append(history, msg.ToParam())

		if msg.StopReason != anthropic.StopReasonToolUse {
			break
		}

		results := make([]anthropic.ContentBlockParamUnion, 0)
		for _, block := range msg.Content {
			tu, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			emit(Event{Type: EventTool, Tool: tu.Name, Status: "start"})
			out, isErr := a.callTool(ctx, tu.Name, tu.Input)
			emit(Event{Type: EventTool, Tool: tu.Name, Status: "done"})
			results = append(results, anthropic.NewToolResultBlock(tu.ID, out, isErr))
		}
		history = append(history, anthropic.NewUserMessage(results...))
	}

	emit(Event{Type: EventDone})
	return history, nil
}

// callTool decodes the model's tool input and runs the MCP tool, turning any
// transport error into an error tool result the model can recover from.
func (a *Agent) callTool(ctx context.Context, name string, input json.RawMessage) (string, bool) {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return fmt.Sprintf("invalid tool input: %v", err), true
		}
	}

	text, isErr, err := a.tools.Call(ctx, name, args)
	if err != nil {
		slog.Error("tool call failed", slog.String("tool", name), slog.String("error", err.Error()))
		return fmt.Sprintf("tool %q failed: %v", name, err), true
	}
	return text, isErr
}
