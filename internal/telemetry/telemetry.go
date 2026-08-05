// Package telemetry starts OpenTelemetry metrics and traces for the broker and
// the agent. Both signals push over OTLP gRPC to the cluster collector at
// OTEL_EXPORTER_OTLP_ENDPOINT (read from the environment by the exporters).
//
// The agents run one per browser session in short-lived sandboxes, so a
// Prometheus scrape target would churn as pods come and go. Push fits that
// lifecycle; the broker pushes too, for one model across both binaries.
package telemetry

import (
	"context"
	"fmt"
	"time"

	"alpineworks.io/ootel"
	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
)

// Config controls telemetry setup. When both signals are disabled, Init is a
// no-op and the process runs without any exporter.
type Config struct {
	MetricsEnabled bool
	// MetricsPort is where ootel serves its health check. In push mode no
	// /metrics endpoint is exposed, but ootel still binds this port, so it must
	// differ from the application port.
	MetricsPort    int
	TracingEnabled bool
	SampleRate     float64
	Service        string
	Version        string
}

// Init wires the global OpenTelemetry providers and returns a shutdown func to
// flush and close the exporters. The gRPC exporters connect lazily, so Init
// does not fail when the collector is unreachable.
func Init(ctx context.Context, c Config) (func(context.Context) error, error) {
	client := ootel.NewOotelClient(
		// Force OTLP gRPC. ootel defaults some callers to a Prometheus pull
		// endpoint; the ephemeral agents need push instead.
		ootel.WithMetricConfig(
			ootel.NewMetricConfig(c.MetricsEnabled, ootel.ExporterTypeOTLPGRPC, c.MetricsPort),
		),
		ootel.WithTraceConfig(
			ootel.NewTraceConfig(c.TracingEnabled, c.SampleRate, c.Service, c.Version),
		),
	)

	shutdown, err := client.Init(ctx)
	if err != nil {
		return nil, fmt.Errorf("init ootel: %w", err)
	}

	if c.MetricsEnabled {
		if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(5 * time.Second)); err != nil {
			return shutdown, fmt.Errorf("start runtime metrics: %w", err)
		}
		if err := host.Start(); err != nil {
			return shutdown, fmt.Errorf("start host metrics: %w", err)
		}
	}

	return shutdown, nil
}
