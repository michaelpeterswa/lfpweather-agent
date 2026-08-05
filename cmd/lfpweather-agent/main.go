package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/michaelpeterswa/lfpweather-agent/internal/agent"
	"github.com/michaelpeterswa/lfpweather-agent/internal/config"
	"github.com/michaelpeterswa/lfpweather-agent/internal/logging"
	"github.com/michaelpeterswa/lfpweather-agent/internal/mcpclient"
	"github.com/michaelpeterswa/lfpweather-agent/internal/ratelimit"
	"github.com/michaelpeterswa/lfpweather-agent/internal/server"
	"github.com/michaelpeterswa/lfpweather-agent/internal/session"
	"github.com/michaelpeterswa/lfpweather-agent/internal/telemetry"
	"github.com/michaelpeterswa/lfpweather-agent/internal/usage"
)

// defaultSystemPrompt shapes the assistant into a focused weather guide for the
// station. It is overridable via SYSTEM_PROMPT.
const defaultSystemPrompt = `You are the assistant for lfpweather.com, a hyperlocal weather and environment station in Lake Forest Park, Washington.

You answer questions about this station's data using the provided tools: current conditions, history and trends, record highs and lows, air quality, and bird detections. The station reports in US Pacific time (America/Los_Angeles).

The station reports weather and environment metrics only: temperature, humidity, wind, pressure, rainfall, solar and UV, air quality, and bird detections. It does NOT report device telemetry such as battery, signal strength, or firmware.

Guidelines:
- Call list_weather_fields at most once per question, and only when you are unsure which metric the station reports.
- If a requested metric is not in the field list, or is device telemetry (for example battery, voltage, signal), tell the user the station does not report it and stop. Do not call more tools to look for it.
- Use get_current_time when a question depends on "now", "today", or "tonight".
- Prefer get_weather_latest for current conditions, query_weather for history and trends, and get_weather_records for records.
- Use as few tool calls as you can. Never repeat a tool call with the same arguments.
- Keep answers concise and give the numbers with their units (temperature in °F, wind in mph, pressure in inHg, rain in inches).
- Answer only from tool data. If the data does not cover a question, say so. Politely decline questions unrelated to this station's weather and environment.

Formatting (the frontend renders this Markdown grammar; stay inside it):
- Put the headline number or answer in **bold**.
- Use *italic* for a qualifier or a timestamp.
- Use a bulleted list ("- ") for a breakdown, for example a low and a high, or a value for each pollutant.
- Use a numbered list for a sequence or a ranking.
- Use ` + "`inline code`" + ` for an exact field name.
- Do not use headings, tables, block quotes, or images. Keep answers short.`

func main() {
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	slogLevel, err := logging.LogLevelToSlogLevel(logLevel)
	if err != nil {
		log.Fatalf("could not convert log level: %s", err)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel})))

	slog.Info("welcome to lfpweather-agent!", slog.String("version", config.AppVersion))

	c, err := config.NewConfig()
	if err != nil {
		slog.Error("could not create config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	traceVersion := c.TracingVersion
	if traceVersion == "" {
		traceVersion = config.AppVersion
	}
	shutdownTelemetry, err := telemetry.Init(ctx, telemetry.Config{
		MetricsEnabled: c.MetricsEnabled,
		MetricsPort:    c.MetricsPort,
		TracingEnabled: c.TracingEnabled,
		SampleRate:     c.TracingSampleRate,
		Service:        c.TracingService,
		Version:        traceVersion,
	})
	if err != nil {
		slog.Error("could not init telemetry", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()

	// Connect to the MCP server and discover its tools once at startup.
	mcp, err := mcpclient.New(ctx, c.MCPURL, c.MCPBearerToken, config.AppVersion)
	if err != nil {
		slog.Error("could not connect to mcp server", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = mcp.Close() }()
	slog.Info("connected to mcp server", slog.Int("tools", len(mcp.Tools())))

	// Wrap the Anthropic HTTP transport so each model call is a client span and
	// carries trace context. This is a no-op when tracing is disabled.
	anthropicClient := anthropic.NewClient(
		option.WithAPIKey(c.AnthropicAPIKey),
		option.WithHTTPClient(&http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}),
	)

	systemPrompt := defaultSystemPrompt
	if c.SystemPrompt != "" {
		systemPrompt = c.SystemPrompt
	}

	usageTracker := usage.New()

	ag := agent.New(anthropicClient, agent.Options{
		Model:        c.AnthropicModel,
		System:       systemPrompt,
		MaxTokens:    c.AnthropicMaxTokens,
		MaxTurns:     c.MaxTurns,
		MCPTools:     mcp.Tools(),
		ToolExecutor: mcp,
		Usage:        usageTracker,
	})

	store := session.NewStore(c.SessionTTL)
	limiter := ratelimit.New(c.RateLimitRPM, c.RateLimitBurst)

	reaperStop := make(chan struct{})
	go store.Reaper(reaperStop, time.Minute)
	go limiter.Reaper(reaperStop, time.Minute)

	srv := server.New(ag, store, server.Options{
		Timeout:           c.RequestTimeout,
		Limiter:           limiter,
		Usage:             usageTracker,
		TrustForwardedFor: c.TrustForwardedFor,
	})

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", c.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("listening", slog.Int("port", c.Port))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	close(reaperStop)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", slog.String("error", err.Error()))
	}
}
