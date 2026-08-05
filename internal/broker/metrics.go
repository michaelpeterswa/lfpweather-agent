package broker

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metrics holds the broker's OpenTelemetry instruments. Recording is
// best-effort: when telemetry is disabled the global meter is a no-op, and a
// nil instrument (creation failed) is skipped.
type metrics struct {
	chatRequests  metric.Int64Counter
	claimDuration metric.Float64Histogram
}

// newMetrics builds the broker instruments and, when a budget is set, registers
// the daily-budget observable gauges.
func newMetrics(budget *BudgetTracker) *metrics {
	meter := otel.Meter("lfpweather-broker")

	chatRequests, err := meter.Int64Counter(
		"broker.chat.requests",
		metric.WithDescription("Chat requests by outcome."),
	)
	if err != nil {
		slog.Warn("could not create chat requests counter", slog.String("error", err.Error()))
	}

	claimDuration, err := meter.Float64Histogram(
		"broker.sandbox.claim.duration",
		metric.WithDescription("Time to acquire an agent for a session."),
		metric.WithUnit("s"),
	)
	if err != nil {
		slog.Warn("could not create claim duration histogram", slog.String("error", err.Error()))
	}

	if budget != nil {
		registerBudgetGauges(meter, budget)
	}

	return &metrics{chatRequests: chatRequests, claimDuration: claimDuration}
}

// recordChat counts one chat request with its outcome (ok, degraded,
// acquire_error, or proxy_error).
func (m *metrics) recordChat(ctx context.Context, outcome string) {
	if m == nil || m.chatRequests == nil {
		return
	}
	m.chatRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// observeClaim records how long a sandbox claim took and whether it failed.
func (m *metrics) observeClaim(ctx context.Context, d time.Duration, err error) {
	if m == nil || m.claimDuration == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.claimDuration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("result", result)))
}

// registerBudgetGauges exposes the daily token budget as observable gauges, read
// from the tracker on each collection.
func registerBudgetGauges(meter metric.Meter, budget *BudgetTracker) {
	dailyTokens, err1 := meter.Int64ObservableGauge(
		"broker.budget.daily_tokens",
		metric.WithDescription("Tokens spent so far in the current UTC day."),
	)
	remaining, err2 := meter.Int64ObservableGauge(
		"broker.budget.remaining",
		metric.WithDescription("Tokens left in the daily budget (-1 when disabled)."),
	)
	exceeded, err3 := meter.Int64ObservableGauge(
		"broker.budget.exceeded",
		metric.WithDescription("1 when the daily budget is spent, else 0."),
	)
	if err1 != nil || err2 != nil || err3 != nil {
		slog.Warn("could not create budget gauges")
		return
	}

	_, err := meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			s := budget.Snapshot()
			o.ObserveInt64(dailyTokens, s.DailyTokens)
			o.ObserveInt64(remaining, s.Remaining)
			var ex int64
			if s.Exceeded {
				ex = 1
			}
			o.ObserveInt64(exceeded, ex)
			return nil
		},
		dailyTokens, remaining, exceeded,
	)
	if err != nil {
		slog.Warn("could not register budget callback", slog.String("error", err.Error()))
	}
}
