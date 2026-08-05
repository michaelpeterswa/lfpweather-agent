package broker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
)

// SandboxProvider gives each session its own Agent Sandbox. The agent runs an
// HTTP server on AgentPort inside the sandbox pod; the broker reaches it
// directly at the pod IP over the cluster network (gated by a network policy).
//
// A session reuses its sandbox across messages. Idle sandboxes are deleted by
// the reaper so a closed browser tab does not leak a pod.
type SandboxProvider struct {
	client    *sandbox.Client
	warmPool  string
	namespace string
	agentPort int
	idleTTL   time.Duration

	mu       sync.Mutex
	sessions map[string]*sbEntry
	now      func() time.Time
}

type sbEntry struct {
	claimName string
	baseURL   string
	lastSeen  time.Time
}

// SandboxProviderConfig configures a SandboxProvider.
type SandboxProviderConfig struct {
	WarmPoolName string
	Namespace    string
	AgentPort    int
	RouterURL    string // Options.APIURL for the in-cluster sandbox router
	IdleTTL      time.Duration
}

// NewSandboxProvider builds a provider backed by the Agent Sandbox client. The
// client uses in-cluster Kubernetes config when RestConfig is nil.
func NewSandboxProvider(ctx context.Context, cfg SandboxProviderConfig) (*SandboxProvider, error) {
	if cfg.WarmPoolName == "" {
		return nil, fmt.Errorf("warm pool name is required for sandbox mode")
	}

	client, err := sandbox.NewClient(ctx, sandbox.Options{
		WarmPoolName: cfg.WarmPoolName,
		Namespace:    cfg.Namespace,
		APIURL:       cfg.RouterURL,
	})
	if err != nil {
		return nil, fmt.Errorf("create sandbox client: %w", err)
	}

	return &SandboxProvider{
		client:    client,
		warmPool:  cfg.WarmPoolName,
		namespace: cfg.Namespace,
		agentPort: cfg.AgentPort,
		idleTTL:   cfg.IdleTTL,
		sessions:  make(map[string]*sbEntry),
		now:       time.Now,
	}, nil
}

// Acquire returns the agent URL for a session, creating a sandbox on first use.
func (p *SandboxProvider) Acquire(ctx context.Context, sessionID string) (string, error) {
	p.mu.Lock()
	if e, ok := p.sessions[sessionID]; ok {
		e.lastSeen = p.now()
		url := e.baseURL
		p.mu.Unlock()
		return url, nil
	}
	p.mu.Unlock()

	sb, err := p.client.CreateSandbox(ctx, p.warmPool, p.namespace)
	if err != nil {
		return "", fmt.Errorf("create sandbox: %w", err)
	}

	ip := sb.PodIP()
	if ip == "" {
		_ = p.client.DeleteSandbox(ctx, sb.ClaimName(), p.namespace)
		return "", fmt.Errorf("sandbox %s has no pod IP", sb.ClaimName())
	}
	baseURL := fmt.Sprintf("http://%s:%d", ip, p.agentPort)

	p.mu.Lock()
	p.sessions[sessionID] = &sbEntry{claimName: sb.ClaimName(), baseURL: baseURL, lastSeen: p.now()}
	p.mu.Unlock()

	slog.Info("created sandbox for session", slog.String("session", sessionID), slog.String("claim", sb.ClaimName()))
	return baseURL, nil
}

// Targets returns the agent base URL of every active session.
func (p *SandboxProvider) Targets() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.sessions))
	for id, e := range p.sessions {
		out[id] = e.baseURL
	}
	return out
}

// Release deletes a session's sandbox immediately.
func (p *SandboxProvider) Release(sessionID string) {
	p.mu.Lock()
	e, ok := p.sessions[sessionID]
	if ok {
		delete(p.sessions, sessionID)
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	p.deleteClaim(e.claimName)
}

// Close deletes every sandbox this provider created.
func (p *SandboxProvider) Close(ctx context.Context) {
	p.client.DeleteAll(ctx)
}

// reap deletes sandboxes for sessions idle longer than the TTL.
func (p *SandboxProvider) reap() {
	cutoff := p.now().Add(-p.idleTTL)

	p.mu.Lock()
	var stale []string
	for id, e := range p.sessions {
		if e.lastSeen.Before(cutoff) {
			stale = append(stale, e.claimName)
			delete(p.sessions, id)
		}
	}
	p.mu.Unlock()

	for _, claim := range stale {
		p.deleteClaim(claim)
	}
}

func (p *SandboxProvider) deleteClaim(claimName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.client.DeleteSandbox(ctx, claimName, p.namespace); err != nil {
		slog.Error("failed to delete sandbox", slog.String("claim", claimName), slog.String("error", err.Error()))
	}
}

// Reaper runs reap on an interval until stop is closed.
func (p *SandboxProvider) Reaper(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			p.reap()
		}
	}
}
