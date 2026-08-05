package broker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// agentUsage mirrors the fields of the agent's GET /usage response the broker
// budgets on.
type agentUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// UsagePoller periodically reads each active agent's /usage and feeds the totals
// into the budget tracker. It budgets on input + output tokens — the uncached
// cost drivers (cached reads are ~0.1x price).
type UsagePoller struct {
	provider Provider
	tracker  *BudgetTracker
	client   *http.Client
	interval time.Duration
}

// NewUsagePoller builds a poller over the provider's active agent targets.
func NewUsagePoller(provider Provider, tracker *BudgetTracker, interval time.Duration) *UsagePoller {
	return &UsagePoller{
		provider: provider,
		tracker:  tracker,
		client:   &http.Client{Timeout: 5 * time.Second},
		interval: interval,
	}
}

// Run polls on the interval until stop is closed.
func (p *UsagePoller) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			p.pollOnce()
		}
	}
}

func (p *UsagePoller) pollOnce() {
	targets := p.provider.Targets()
	active := make(map[string]struct{}, len(targets))
	for key, base := range targets {
		active[key] = struct{}{}
		u, err := p.fetch(base)
		if err != nil {
			slog.Debug("usage poll failed", slog.String("target", base), slog.String("error", err.Error()))
			continue
		}
		p.tracker.Record(key, u.InputTokens+u.OutputTokens)
	}
	p.tracker.Prune(active)
}

func (p *UsagePoller) fetch(base string) (agentUsage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/usage", nil)
	if err != nil {
		return agentUsage{}, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return agentUsage{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var u agentUsage
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return agentUsage{}, err
	}
	return u, nil
}
