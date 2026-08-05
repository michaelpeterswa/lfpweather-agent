package broker

import "context"

// Provider resolves a session id to the base URL of the agent that serves it.
type Provider interface {
	// Acquire returns the base URL (scheme://host:port) of the agent for a
	// session, creating the backing sandbox on first use.
	Acquire(ctx context.Context, sessionID string) (baseURL string, err error)
	// Release tears down the backing resource for a session, if any.
	Release(sessionID string)
	// Targets returns the base URLs of the currently active agents, keyed by a
	// stable id, for usage polling.
	Targets() map[string]string
	// Close releases all resources.
	Close(ctx context.Context)
}

// DirectProvider sends every session to one fixed agent URL. It is the
// local/dev provider and the target for tests; it owns no resources.
type DirectProvider struct {
	target string
}

// NewDirectProvider returns a provider that always resolves to target.
func NewDirectProvider(target string) *DirectProvider {
	return &DirectProvider{target: target}
}

func (p *DirectProvider) Acquire(_ context.Context, _ string) (string, error) {
	return p.target, nil
}

func (p *DirectProvider) Release(_ string) {}

func (p *DirectProvider) Targets() map[string]string {
	return map[string]string{"direct": p.target}
}

func (p *DirectProvider) Close(_ context.Context) {}
