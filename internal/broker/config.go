// Package broker fronts the per-conversation agents. It maps a browser session
// to an agent instance (a fixed URL in direct mode, or a per-session Agent
// Sandbox in sandbox mode) and reverse-proxies the streaming chat.
package broker

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the broker runtime configuration.
type Config struct {
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	Port     int    `env:"PORT" envDefault:"8080"`

	// ProviderMode selects how a session resolves to an agent: "direct" proxies
	// every session to a single AgentURL (local/dev); "sandbox" creates one
	// Agent Sandbox per session.
	ProviderMode string `env:"PROVIDER_MODE" envDefault:"direct"`

	// Direct mode.
	AgentURL string `env:"AGENT_URL"`

	// Sandbox mode.
	WarmPoolName     string        `env:"WARM_POOL_NAME"`
	Namespace        string        `env:"NAMESPACE" envDefault:"default"`
	AgentPort        int           `env:"AGENT_PORT" envDefault:"8080"`
	SandboxRouterURL string        `env:"SANDBOX_ROUTER_URL"` // Options.APIURL for the in-cluster router
	SandboxIdleTTL   time.Duration `env:"SANDBOX_IDLE_TTL" envDefault:"15m"`

	// MaxBodyBytes bounds a chat request body.
	MaxBodyBytes int64 `env:"MAX_BODY_BYTES" envDefault:"65536"`

	// ShutdownTimeout bounds the graceful drain on SIGTERM.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
}

// NewConfig parses the broker configuration from the environment.
func NewConfig() (*Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return &cfg, nil
}
