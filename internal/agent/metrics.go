package agent

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// agentMetrics holds the agent loop's OpenTelemetry instruments. Recording is
// best-effort: when telemetry is disabled the global meter is a no-op.
type agentMetrics struct {
	tokens       metric.Int64Counter
	turns        metric.Int64Histogram
	toolCalls    metric.Int64Counter
	toolDuration metric.Float64Histogram
}

func newAgentMetrics() *agentMetrics {
	meter := otel.Meter("lfpweather-agent")

	tokens, err := meter.Int64Counter(
		"agent.llm.tokens",
		metric.WithDescription("LLM tokens by type (input, output, cache_read, cache_creation)."),
	)
	if err != nil {
		slog.Warn("could not create tokens counter", slog.String("error", err.Error()))
	}

	turns, err := meter.Int64Histogram(
		"agent.llm.turns",
		metric.WithDescription("Model turns taken to answer one question."),
	)
	if err != nil {
		slog.Warn("could not create turns histogram", slog.String("error", err.Error()))
	}

	toolCalls, err := meter.Int64Counter(
		"agent.tool.calls",
		metric.WithDescription("MCP tool calls by tool and status."),
	)
	if err != nil {
		slog.Warn("could not create tool calls counter", slog.String("error", err.Error()))
	}

	toolDuration, err := meter.Float64Histogram(
		"agent.tool.duration",
		metric.WithDescription("MCP tool call duration."),
		metric.WithUnit("s"),
	)
	if err != nil {
		slog.Warn("could not create tool duration histogram", slog.String("error", err.Error()))
	}

	return &agentMetrics{tokens: tokens, turns: turns, toolCalls: toolCalls, toolDuration: toolDuration}
}

// recordRun records the token totals and turn count for one answered question.
func (m *agentMetrics) recordRun(ctx context.Context, input, output, cacheRead, cacheCreation, turns int64) {
	if m == nil {
		return
	}
	if m.tokens != nil {
		m.tokens.Add(ctx, input, metric.WithAttributes(attribute.String("type", "input")))
		m.tokens.Add(ctx, output, metric.WithAttributes(attribute.String("type", "output")))
		m.tokens.Add(ctx, cacheRead, metric.WithAttributes(attribute.String("type", "cache_read")))
		m.tokens.Add(ctx, cacheCreation, metric.WithAttributes(attribute.String("type", "cache_creation")))
	}
	if m.turns != nil {
		m.turns.Record(ctx, turns)
	}
}

// recordTool records one MCP tool call: its duration and whether it errored.
func (m *agentMetrics) recordTool(ctx context.Context, tool string, d time.Duration, isErr bool) {
	if m == nil {
		return
	}
	status := "ok"
	if isErr {
		status = "error"
	}
	if m.toolCalls != nil {
		m.toolCalls.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tool", tool),
			attribute.String("status", status),
		))
	}
	if m.toolDuration != nil {
		m.toolDuration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("tool", tool)))
	}
}
