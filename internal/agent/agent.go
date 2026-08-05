// Package agent runs the Claude tool-use loop for one conversation, streaming
// assistant text and executing tools through the MCP client.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/michaelpeterswa/lfpweather-agent/internal/usage"
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
	usage    *usage.Tracker
	metrics  *agentMetrics
	tracer   trace.Tracer
}

// Options configures an Agent.
type Options struct {
	Model        string
	System       string
	MaxTokens    int64
	MaxTurns     int
	MCPTools     []mcp.Tool
	ToolExecutor toolCaller
	Usage        *usage.Tracker // optional; nil disables accounting
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
		usage:    opts.Usage,
		metrics:  newAgentMetrics(),
		tracer:   otel.Tracer("lfpweather-agent"),
	}
}

// Run answers one user message. It appends the exchange to history and returns
// the updated history. Assistant text and tool activity are delivered through
// emit as they happen. emit is called only from the calling goroutine.
func (a *Agent) Run(ctx context.Context, history []anthropic.MessageParam, userText string, emit func(Event)) ([]anthropic.MessageParam, error) {
	ctx, span := a.tracer.Start(ctx, "agent.run", trace.WithAttributes(attribute.String("llm.model", a.model)))
	defer span.End()

	history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(userText)))

	var run usage.Snapshot
	completed := false

	for turn := 0; turn < a.maxTurns; turn++ {
		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(a.model),
			MaxTokens: a.maxTok,
			Messages:  history,
			Tools:     a.toolDefs,
		}
		if a.system != "" {
			// Cache the system prompt and, in render order, the tool schemas
			// that precede it. This fixed prefix is re-sent on every turn of the
			// tool-use loop and every message of a session, so caching it cuts
			// input cost sharply.
			params.System = []anthropic.TextBlockParam{{
				Text:         a.system,
				CacheControl: anthropic.NewCacheControlEphemeralParam(),
			}}
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

		a.usage.Record(msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheReadInputTokens, msg.Usage.CacheCreationInputTokens)
		run.Requests++
		run.InputTokens += msg.Usage.InputTokens
		run.OutputTokens += msg.Usage.OutputTokens
		run.CacheReadTokens += msg.Usage.CacheReadInputTokens
		run.CacheCreationTokens += msg.Usage.CacheCreationInputTokens

		history = append(history, msg.ToParam())

		if msg.StopReason != anthropic.StopReasonToolUse {
			completed = true
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

	slog.Info("llm usage",
		slog.Int64("turns", run.Requests),
		slog.Int64("input_tokens", run.InputTokens),
		slog.Int64("output_tokens", run.OutputTokens),
		slog.Int64("cache_read_tokens", run.CacheReadTokens),
		slog.Int64("cache_creation_tokens", run.CacheCreationTokens),
		slog.Bool("completed", completed),
	)

	a.metrics.recordRun(ctx, run.InputTokens, run.OutputTokens, run.CacheReadTokens, run.CacheCreationTokens, run.Requests)
	span.SetAttributes(
		attribute.Int64("llm.turns", run.Requests),
		attribute.Int64("llm.tokens.input", run.InputTokens),
		attribute.Int64("llm.tokens.output", run.OutputTokens),
		attribute.Bool("llm.completed", completed),
	)

	if !completed {
		// The loop hit the turn cap while the model still wanted tools.
		emit(Event{Type: EventError, Text: "reached the step limit for one question; ask a narrower question"})
	}

	emit(Event{Type: EventDone})
	return history, nil
}

// callTool decodes the model's tool input and runs the MCP tool, turning any
// transport error into an error tool result the model can recover from.
func (a *Agent) callTool(ctx context.Context, name string, input json.RawMessage) (string, bool) {
	ctx, span := a.tracer.Start(ctx, "agent.tool.call", trace.WithAttributes(attribute.String("tool", name)))
	defer span.End()
	start := time.Now()

	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			span.SetStatus(codes.Error, "invalid tool input")
			a.metrics.recordTool(ctx, name, time.Since(start), true)
			return fmt.Sprintf("invalid tool input: %v", err), true
		}
	}

	text, isErr, err := a.tools.Call(ctx, name, args)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool call failed")
		a.metrics.recordTool(ctx, name, time.Since(start), true)
		slog.Error("tool call failed", slog.String("tool", name), slog.String("error", err.Error()))
		return fmt.Sprintf("tool %q failed: %v", name, err), true
	}
	a.metrics.recordTool(ctx, name, time.Since(start), isErr)
	return text, isErr
}
