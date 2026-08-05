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

	"github.com/michaelpeterswa/lfpweather-agent/internal/broker"
	"github.com/michaelpeterswa/lfpweather-agent/internal/logging"
)

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

	slog.Info("welcome to lfpweather-broker!")

	c, err := broker.NewConfig()
	if err != nil {
		slog.Error("could not create config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reaperStop := make(chan struct{})
	provider, err := buildProvider(ctx, c, reaperStop)
	if err != nil {
		slog.Error("could not build provider", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer provider.Close(context.Background())

	srv := broker.NewServer(provider, c.MaxBodyBytes)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", c.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("listening", slog.Int("port", c.Port), slog.String("mode", c.ProviderMode))
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

// buildProvider constructs the session→agent provider for the configured mode.
// In sandbox mode it also starts the idle-sandbox reaper.
func buildProvider(ctx context.Context, c *broker.Config, reaperStop <-chan struct{}) (broker.Provider, error) {
	switch c.ProviderMode {
	case "direct":
		if c.AgentURL == "" {
			return nil, fmt.Errorf("AGENT_URL is required in direct mode")
		}
		return broker.NewDirectProvider(c.AgentURL), nil
	case "sandbox":
		sp, err := broker.NewSandboxProvider(ctx, broker.SandboxProviderConfig{
			WarmPoolName: c.WarmPoolName,
			Namespace:    c.Namespace,
			AgentPort:    c.AgentPort,
			RouterURL:    c.SandboxRouterURL,
			IdleTTL:      c.SandboxIdleTTL,
		})
		if err != nil {
			return nil, err
		}
		go sp.Reaper(reaperStop, time.Minute)
		return sp, nil
	default:
		return nil, fmt.Errorf("unknown PROVIDER_MODE %q (want direct or sandbox)", c.ProviderMode)
	}
}
