// Package config loads the agent runtime configuration from the environment.
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// AppVersion is set at build time with -ldflags -X.
var AppVersion = "dev"

// Config is the full runtime configuration.
type Config struct {
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	Port     int    `env:"PORT" envDefault:"8080"`

	// Anthropic model access.
	AnthropicAPIKey    string `env:"ANTHROPIC_API_KEY,required"`
	AnthropicModel     string `env:"ANTHROPIC_MODEL" envDefault:"claude-sonnet-5"`
	AnthropicMaxTokens int64  `env:"ANTHROPIC_MAX_TOKENS" envDefault:"4096"`

	// MCP server (lfpweather-mcp) providing the data tools. URL points at the
	// streamable HTTP endpoint, e.g.
	// http://lfpweather-mcp.lfpweather.svc.cluster.local:80/mcp.
	MCPURL         string `env:"MCP_URL,required"`
	MCPBearerToken string `env:"MCP_BEARER_TOKEN"`

	// SystemPrompt overrides the built-in system prompt when set.
	SystemPrompt string `env:"SYSTEM_PROMPT"`

	// MaxTurns bounds the tool-use loop per user message.
	MaxTurns int `env:"MAX_TURNS" envDefault:"8"`

	// Per-IP rate limiting on POST /v1/chat. RateLimitRPM <= 0 disables it.
	RateLimitRPM   int `env:"RATE_LIMIT_RPM" envDefault:"20"`
	RateLimitBurst int `env:"RATE_LIMIT_BURST" envDefault:"5"`
	// TrustForwardedFor uses the left-most X-Forwarded-For entry as the client
	// IP. Enable only when a trusted proxy or broker sets that header, otherwise
	// a client can spoof its IP.
	TrustForwardedFor bool `env:"TRUST_FORWARDED_FOR" envDefault:"false"`

	// SessionTTL is how long an idle conversation is kept in memory.
	SessionTTL time.Duration `env:"SESSION_TTL" envDefault:"30m"`

	// RequestTimeout bounds one user message end to end.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"90s"`

	// ShutdownTimeout bounds the graceful drain on SIGTERM.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
}

// NewConfig parses the configuration from the environment.
func NewConfig() (*Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return &cfg, nil
}
